// Package dynamodb adapts an Amazon DynamoDB table to the query ports. A table is
// modelled as Map[key, item]: an item's full primary key (the partition key, then
// the sort key when the table has one), rendered to a string, is the key; the item,
// each attribute normalized to a JSON-ready value, is the value. The jq filter runs
// client-side over items fetched by primary key or streamed from the table, so the
// semantics match every other backend behind the KV port.
//
// A partition-key-only table renders its key as the partition value's bare canonical
// string (like Mongo's _id); a table with a sort key renders a JSON array of the two
// key attributes in schema order. Reversing a key back to typed DynamoDB values needs
// the table's key schema, which is read once at connect time.
//
// Credentials never travel in the URL: the AWS default credential chain (environment,
// shared config, IAM role) resolves them, so a secret never touches iq's config or
// keyring. When ?endpoint= points at DynamoDB Local, non-secret static dummy
// credentials are supplied because the local emulator ignores them.
package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the page size for ScanBatches and TypedScan: how many items to
// accumulate before handing a page to the caller, bounding streaming memory. A
// single DynamoDB Scan returns up to 1MB regardless; this re-batches that into
// even pages for the caller.
const scanBatch = 100

// batchGetMax and batchWriteMax are DynamoDB's hard per-request item limits for
// BatchGetItem and BatchWriteItem, the chunk sizes Get and Clear page by.
const (
	batchGetMax   = 100
	batchWriteMax = 25
)

// maxUnprocessed bounds the retry loop that drains BatchGetItem/BatchWriteItem
// leftovers, so a persistently throttled table fails fast rather than looping
// forever (an infinite wait is forbidden).
const maxUnprocessed = 8

// errNoTable is returned when a table-scoped operation runs without a table
// selected. It is a sentinel so the CLI can surface a clear hint.
var errNoTable = errors.New("dynamodb: no table selected; address it as handle.table or set ?table= in the source url")

// KeyAttr is one primary-key attribute: its name and its DynamoDB scalar type
// (S, N, or B), used to encode and decode the string key. It is exported (built live
// from DescribeTable, or offline from a ?keys= hint via ParseKeySchema) so a dump
// reader keys an item exactly as a live scan does.
type KeyAttr struct {
	name string
	typ  types.ScalarAttributeType
}

// ddbAPI is the subset of the DynamoDB client the Store calls. The concrete
// *dynamodb.Client satisfies it; abstracting it lets the retry, pagination, and
// batching loops be tested with a fake that returns UnprocessedKeys/Items and
// multi-page scans, paths a throttling-free backend like DynamoDB Local never drives.
type ddbAPI interface {
	ListTables(context.Context, *dynamodb.ListTablesInput, ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	BatchGetItem(context.Context, *dynamodb.BatchGetItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error)
	BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteTable(context.Context, *dynamodb.DeleteTableInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteTableOutput, error)
	ExecuteStatement(context.Context, *dynamodb.ExecuteStatementInput, ...func(*dynamodb.Options)) (*dynamodb.ExecuteStatementOutput, error)
}

// Store adapts one DynamoDB table to the query ports. The jq and write paths are
// scoped to a single table (the keyspace of the KV model); the raw path runs
// arbitrary PartiQL and needs no table.
type Store struct {
	client   ddbAPI
	table    string
	keys     []KeyAttr // primary key in schema order: partition key, then sort key
	pageSize int
	decimal  numfmt.DecimalMode
}

// connConfig is the parsed form of a dynamodb:// source URL.
type connConfig struct {
	region   string
	table    string
	endpoint string
}

// Open connects to DynamoDB in the region named by a dynamodb:// URL and verifies
// the connection with a bounded ListTables probe (the SDK client is otherwise lazy,
// so this is what fails fast on a bad region, endpoint, or credentials). The table
// (the jq keyspace, may be empty for raw-only use) is the address override when
// non-empty, else the URL's ?table= default. When a table is selected its key schema
// is read once so keys can be encoded and reversed. When trace is non-nil, each SDK
// operation is logged to it (the CLI's --verbose trace) with parameters redacted. dec
// chooses how numeric values are presented to the filter.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}

	client, err := newClient(ctx, cc, trace)
	if err != nil {
		return nil, err
	}

	// Bounded reachability probe: validates region, endpoint, and credentials, the
	// DynamoDB analogue of establishing a session — Open fails fast on a bad source.
	if _, err := client.ListTables(ctx, &dynamodb.ListTablesInput{Limit: aws.Int32(1)}); err != nil {
		return nil, fmt.Errorf("connect dynamodb: %w", err)
	}

	st := &Store{
		client:   client,
		table:    cc.table,
		pageSize: scanBatch,
		decimal:  dec,
	}
	if cc.table != "" {
		if err := st.loadKeySchema(ctx); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// newClient loads the AWS config and builds the DynamoDB client for cc. A custom
// endpoint (DynamoDB Local) gets static dummy credentials: the emulator ignores them
// but the SDK still needs a provider.
func newClient(ctx context.Context, cc connConfig, trace io.Writer) (*dynamodb.Client, error) {
	loadOpts := []func(*config.LoadOptions) error{config.WithRegion(cc.region)}
	if cc.endpoint != "" {
		// DynamoDB Local ignores credentials but the SDK still requires a provider;
		// supply non-secret static dummies so credential resolution never fails.
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("dummy", "dummy", ""),
		))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return dynamodb.NewFromConfig(cfg, clientOption(cc, trace)), nil
}

// clientOption sets the endpoint override when cc names one, and logs each SDK
// operation to trace when it is non-nil.
func clientOption(cc connConfig, trace io.Writer) func(*dynamodb.Options) {
	return func(o *dynamodb.Options) {
		if cc.endpoint != "" {
			o.BaseEndpoint = aws.String(cc.endpoint)
		}
		if trace != nil {
			o.APIOptions = append(o.APIOptions, traceMiddleware(trace))
		}
	}
}

// Target returns the region and table a dynamodb:// source addresses: the address
// override wins over the URL's ?table= default. It lets the CLI show and reason about
// a source's region and table without duplicating the URL parsing.
func Target(rawURL, address string) (region, table string, err error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return "", "", err
	}
	return cc.region, cc.table, nil
}

// parseURL parses a dynamodb:// source URL into its connection config. The region is
// the authority (a region like us-east-1 is a valid net/url host, so unlike Cassandra
// this needs no hand-rolled parser). The address override, when non-empty, wins over
// the URL's ?table= default. ?endpoint= overrides the endpoint for DynamoDB Local.
// The region is required, as every DynamoDB call is region-scoped.
func parseURL(rawURL, address string) (connConfig, error) {
	const scheme = "dynamodb://"
	if !strings.HasPrefix(rawURL, scheme) {
		return connConfig{}, fmt.Errorf("dynamodb url must start with %s", scheme)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return connConfig{}, fmt.Errorf("parse dynamodb url: %w", err)
	}
	region := u.Host
	if region == "" {
		return connConfig{}, fmt.Errorf("dynamodb url must name a region, e.g. dynamodb://us-east-1/?table=books")
	}
	q := u.Query()
	table := address
	if table == "" {
		table = q.Get(paramTable)
	}
	return connConfig{region: region, table: table, endpoint: q.Get(paramEndpoint)}, nil
}

// loadKeySchema reads the selected table's primary-key schema (partition key, then
// sort key when present) with each key attribute's scalar type, so keys can be
// encoded and reversed. A table that does not exist is an error the CLI surfaces
// cleanly.
func (s *Store) loadKeySchema(ctx context.Context) error {
	out, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &s.table})
	if err != nil {
		return fmt.Errorf("dynamodb describe table %q: %w", s.table, err)
	}
	if out.Table == nil {
		return fmt.Errorf("dynamodb: table %q not found", s.table)
	}
	keys := keySchemaOf(out.Table)
	if len(keys) == 0 {
		return fmt.Errorf("dynamodb: table %q has no partition key", s.table)
	}
	s.keys = keys
	return nil
}

// keySchemaOf returns the table's primary key in schema order, the partition key then
// the sort key, each with its scalar type. It returns nil when the table has no
// partition key.
func keySchemaOf(td *types.TableDescription) []KeyAttr {
	attrType := make(map[string]types.ScalarAttributeType, len(td.AttributeDefinitions))
	for _, ad := range td.AttributeDefinitions {
		attrType[aws.ToString(ad.AttributeName)] = ad.AttributeType
	}
	var hash *KeyAttr
	var sort *KeyAttr
	for _, ks := range td.KeySchema {
		ka := KeyAttr{name: aws.ToString(ks.AttributeName), typ: attrType[aws.ToString(ks.AttributeName)]}
		switch ks.KeyType {
		case types.KeyTypeHash:
			h := ka
			hash = &h
		case types.KeyTypeRange:
			r := ka
			sort = &r
		}
	}
	if hash == nil {
		return nil
	}
	keys := []KeyAttr{*hash}
	if sort != nil {
		keys = append(keys, *sort)
	}
	return keys
}

// Get fetches the items whose primary key matches one of keys and returns them keyed
// by the same string key. A key with no item is absent from the map. Keys are fetched with
// BatchGetItem in batches of 100, retrying the throttled leftovers (UnprocessedKeys)
// with bounded backoff. Empty keys short-circuit with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if len(s.keys) == 0 {
		return nil, errNoTable
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	for start := 0; start < len(keys); start += batchGetMax {
		end := min(start+batchGetMax, len(keys))
		reqKeys, err := s.decodeKeys(keys[start:end])
		if err != nil {
			return nil, err
		}
		pending := map[string]types.KeysAndAttributes{s.table: {Keys: reqKeys}}
		if err := s.drainBatchGet(ctx, pending, func(item map[string]types.AttributeValue) {
			out[s.keyOf(item)] = normalizeMap(item, s.decimal)
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// decodeKeys decodes every key to its key attributes, in order, and fails on the
// first malformed key.
func (s *Store) decodeKeys(keys []string) ([]map[string]types.AttributeValue, error) {
	out := make([]map[string]types.AttributeValue, 0, len(keys))
	for _, k := range keys {
		av, err := s.decodeKey(k)
		if err != nil {
			return nil, err
		}
		out = append(out, av)
	}
	return out, nil
}

// drainBatchGet issues BatchGetItem against pending and drains the throttled
// leftovers (UnprocessedKeys) with bounded backoff, calling onItem for every item
// returned across all attempts. The retry count is bounded, so it can never loop
// forever. Get and the upsert key pre-read share it, differing only in the request's
// projection and what they do with each returned item.
func (s *Store) drainBatchGet(ctx context.Context, pending map[string]types.KeysAndAttributes, onItem func(item map[string]types.AttributeValue)) error {
	for attempt := range maxUnprocessed {
		resp, err := s.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: pending})
		if err != nil {
			return fmt.Errorf("dynamodb batch get: %w", err)
		}
		for _, item := range resp.Responses[s.table] {
			onItem(item)
		}
		pending = resp.UnprocessedKeys
		if len(pending) == 0 {
			return nil
		}
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("dynamodb batch get: %d attempt(s) left items unprocessed", maxUnprocessed)
}

// ScanBatches streams the whole table, handing the caller each page of {key: item}
// as the server-side cursor yields it, so a streaming caller keeps only one page in
// memory. Bounded by ctx; stops at the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	if len(s.keys) == 0 {
		return errNoTable
	}
	return s.scan(ctx, &dynamodb.ScanInput{TableName: &s.table}, fn)
}

// scan runs a paged Scan and hands the caller a page of {key: item} every pageSize
// items, the shared body of ScanBatches and ScanFiltered. The SDK paginator walks the
// LastEvaluatedKey cursor (each Scan returns up to 1MB); this re-batches those items
// into even pages for fn, so a page here is a streaming unit, not a server page.
func (s *Store) scan(ctx context.Context, in *dynamodb.ScanInput, fn func(batch map[string]any) error) error {
	page := make(map[string]any, s.pageSize)
	p := dynamodb.NewScanPaginator(s.client, in)
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("dynamodb scan: %w", err)
		}
		page, err = s.addItems(page, out.Items, fn)
		if err != nil {
			return err
		}
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// addItems normalizes items into page under their keys and hands the page to fn each
// time it holds pageSize entries. It returns the page to keep filling.
func (s *Store) addItems(page map[string]any, items []map[string]types.AttributeValue, fn func(batch map[string]any) error) (map[string]any, error) {
	for _, item := range items {
		page[s.keyOf(item)] = normalizeMap(item, s.decimal)
		if len(page) < s.pageSize {
			continue
		}
		if err := fn(page); err != nil {
			return nil, err
		}
		page = make(map[string]any, s.pageSize)
	}
	return page, nil
}

// Query runs a raw PartiQL statement via ExecuteStatement, DynamoDB's SQL surface
// (the analogue of the Cassandra CQL and Mongo command raw paths). The forwarded args
// are joined back into one statement, executed bound to ctx and paginated over
// NextToken, and any result items are normalized. A statement with no result set (a
// write) returns an empty list rather than an error.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("dynamodb: empty statement")
	}
	stmt := strings.Join(args, " ")
	rows := []any{}
	var next *string
	for {
		out, err := s.client.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
			Statement: &stmt,
			NextToken: next,
		})
		if err != nil {
			return nil, fmt.Errorf("dynamodb: %w", err)
		}
		for _, item := range out.Items {
			rows = append(rows, normalizeMap(item, s.decimal))
		}
		if out.NextToken == nil {
			break
		}
		next = out.NextToken
	}
	return rows, nil
}

// EstimateCount returns the table's approximate item count from its metadata, a cheap
// hint for scan progress. DynamoDB updates ItemCount about every six hours, so it is
// honestly stale — which the Estimator contract permits (it is only a progress hint).
func (s *Store) EstimateCount(ctx context.Context) (int64, error) {
	if len(s.keys) == 0 {
		return 0, errNoTable
	}
	out, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: &s.table})
	if err != nil {
		return 0, fmt.Errorf("dynamodb describe table: %w", err)
	}
	if out.Table == nil || out.Table.ItemCount == nil {
		return 0, nil
	}
	return *out.Table.ItemCount, nil
}

// FormatRaw renders a raw statement reply as indented JSON, the natural form for
// normalized items, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the store. The DynamoDB SDK client holds no persistent connection
// (each call is a bounded HTTP request), so there is nothing to release.
func (s *Store) Close() error {
	return nil
}

// backoffUnit is the base backoff delay, exported to the package so tests can zero it
// to make the retry loops run without real sleeps.
var backoffUnit = 20 * time.Millisecond

// randInt63n draws the backoff jitter. It is a variable so a test can see the bound it
// is given.
var randInt63n = rand.Int63n

// backoff sleeps before retrying a throttled batch leftover, with exponential growth
// and jitter, bounded by ctx so it never waits past the caller's deadline.
func backoff(ctx context.Context, attempt int) error {
	base := backoffUnit * time.Duration(1<<min(attempt, 5))
	wait := base + time.Duration(randInt63n(int64(base)/2+1))
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
