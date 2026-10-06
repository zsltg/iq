package dynamodb

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/testimage"
)

// sharedURL is the base dynamodb:// URL (region + endpoint, no table) the integration
// tests connect to: an ephemeral DynamoDB Local container started once for the whole
// package. It stays empty when integration tests are skipped (-short) or an external
// endpoint is supplied (IQ_DYNAMODB_URL).
var sharedURL string

// TestMain provisions DynamoDB Local before the suite runs. flag.Parse must run before
// testing.Short is read, and os.Exit skips deferred cleanup, so the work lives in
// runTests where the defer fires before the process exits.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	base := os.Getenv("IQ_DYNAMODB_URL")
	if base == "" {
		ctx := context.Background()
		image, err := testimage.Ref("dynamodb")
		if err != nil {
			fmt.Fprintf(os.Stderr, "dynamodb image: %v\n", err)
			return 1
		}
		// A generic container, because there is no dedicated testcontainers module.
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			Image:        image,
			ExposedPorts: []string{"8000/tcp"},
			WaitingFor:   wait.ForListeningPort("8000/tcp"),
			Started:      true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start dynamodb-local container: %v\n", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(container) }()
		host, err := container.Host(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dynamodb container host: %v\n", err)
			return 1
		}
		port, err := container.MappedPort(ctx, "8000")
		if err != nil {
			fmt.Fprintf(os.Stderr, "dynamodb container port: %v\n", err)
			return 1
		}
		base = fmt.Sprintf("dynamodb://us-east-1/?endpoint=http://%s:%s", host, port.Port())
	}
	sharedURL = base
	return m.Run()
}

// testURL returns the base dynamodb:// URL for integration tests: the IQ_DYNAMODB_URL
// override first, then the ephemeral container from TestMain, then a local default.
func testURL() string {
	if u := os.Getenv("IQ_DYNAMODB_URL"); u != "" {
		return u
	}
	if sharedURL != "" {
		return sharedURL
	}
	return "dynamodb://us-east-1/?endpoint=http://localhost:8000"
}

// adminClient builds a raw DynamoDB client against the test endpoint, used by the
// harness to create, seed, and drop tables (operations the driver under test does not
// expose). It mirrors the driver's own local-endpoint credential handling.
func adminClient(t *testing.T) *dynamodb.Client {
	t.Helper()
	cc, err := parseURL(testURL(), "")
	require.NoError(t, err)
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cc.region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy", "dummy", "")))
	require.NoError(t, err)
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if cc.endpoint != "" {
			o.BaseEndpoint = aws.String(cc.endpoint)
		}
	})
}

// openIntegration skips under -short, otherwise opens a table-scoped Store against the
// test endpoint and registers cleanup. The table must already exist.
func openIntegration(t *testing.T, table string) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping dynamodb integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, testURL(), table, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seedTable creates a table with the given key schema and attribute definitions, seeds
// the supplied items, registers a drop on cleanup, and returns a table-scoped Store for
// it. It skips under -short. The table is on-demand so no throughput must be specified.
func seedTable(t *testing.T, name string, keySchema []types.KeySchemaElement, attrs []types.AttributeDefinition, items ...map[string]types.AttributeValue) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping dynamodb integration test in -short mode")
	}
	client := adminClient(t)
	ctx := context.Background()

	_, _ = client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	_ = dynamodb.NewTableNotExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 30*time.Second)

	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(name),
		KeySchema:            keySchema,
		AttributeDefinitions: attrs,
		BillingMode:          types.BillingModePayPerRequest,
	})
	require.NoError(t, err)
	require.NoError(t, dynamodb.NewTableExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 30*time.Second))

	for _, item := range items {
		_, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(name), Item: item})
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	})
	return openIntegration(t, name)
}

// hashKey builds the single-partition-key schema and attribute definition shared by
// most test tables, keyed by a string attribute name of scalar type typ.
func hashKey(attr string, typ types.ScalarAttributeType) ([]types.KeySchemaElement, []types.AttributeDefinition) {
	return []types.KeySchemaElement{{AttributeName: aws.String(attr), KeyType: types.KeyTypeHash}},
		[]types.AttributeDefinition{{AttributeName: aws.String(attr), AttributeType: typ}}
}
