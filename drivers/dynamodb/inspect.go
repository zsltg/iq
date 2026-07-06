package dynamodb

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// InspectTables lists the tables in the source's region, the DynamoDB analogue of
// listing a keyspace's tables. It paginates over ListTables' ExclusiveStartTableName
// cursor and returns the names as a JSON-ready list. It backs the CLI `iq inspect`
// tables subcommand; a raw PartiQL surface cannot express it, so it is a driver method
// the CLI calls directly rather than a statement routed through Query.
func (s *Store) InspectTables(ctx context.Context) (any, error) {
	names := []any{}
	p := dynamodb.NewListTablesPaginator(s.client, &dynamodb.ListTablesInput{})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("dynamodb list tables: %w", err)
		}
		for _, n := range out.TableNames {
			names = append(names, n)
		}
	}
	return map[string]any{"tables": names}, nil
}

// InspectTable describes the selected table's schema and size, the DynamoDB analogue of
// describing a table's columns: its status, key schema, attribute definitions, item
// count and size, billing mode, and any secondary index names. It needs a table
// selected and errors cleanly when none is.
func (s *Store) InspectTable(ctx context.Context) (any, error) {
	if s.table == "" {
		return nil, fmt.Errorf("table needs a table; address it as handle.table or set ?table= on the source url")
	}
	out, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &s.table})
	if err != nil {
		return nil, fmt.Errorf("dynamodb describe table: %w", err)
	}
	td := out.Table
	if td == nil {
		return nil, fmt.Errorf("dynamodb: table %q not found", s.table)
	}

	keySchema := make([]any, 0, len(td.KeySchema))
	for _, ks := range td.KeySchema {
		keySchema = append(keySchema, map[string]any{
			"attribute": aws.ToString(ks.AttributeName),
			"keyType":   string(ks.KeyType),
		})
	}
	attrs := make([]any, 0, len(td.AttributeDefinitions))
	for _, ad := range td.AttributeDefinitions {
		attrs = append(attrs, map[string]any{
			"attribute": aws.ToString(ad.AttributeName),
			"type":      string(ad.AttributeType),
		})
	}
	gsis := make([]any, 0, len(td.GlobalSecondaryIndexes))
	for _, gsi := range td.GlobalSecondaryIndexes {
		gsis = append(gsis, aws.ToString(gsi.IndexName))
	}

	desc := map[string]any{
		"name":                 aws.ToString(td.TableName),
		"status":               string(td.TableStatus),
		"itemCount":            aws.ToInt64(td.ItemCount),
		"sizeBytes":            aws.ToInt64(td.TableSizeBytes),
		"keySchema":            keySchema,
		"attributeDefinitions": attrs,
		"billingMode":          billingMode(td.BillingModeSummary),
	}
	if len(gsis) > 0 {
		desc["globalSecondaryIndexes"] = gsis
	}
	return desc, nil
}

// billingMode renders the table's billing mode as a plain string, defaulting to
// PROVISIONED (DynamoDB's default) when the summary is absent.
func billingMode(bm *types.BillingModeSummary) string {
	if bm == nil {
		return string(types.BillingModeProvisioned)
	}
	return string(bm.BillingMode)
}
