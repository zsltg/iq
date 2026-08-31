package dynamodb

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// fakeDDB is a programmable ddbAPI for testing the retry, pagination, and batching
// loops deterministically — paths a throttling-free DynamoDB Local never drives. Each
// operation delegates to a func field (nil funcs return a zero output) and counts its
// calls.
type fakeDDB struct {
	listTablesFn  func(*awsdynamodb.ListTablesInput) (*awsdynamodb.ListTablesOutput, error)
	scanFn        func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error)
	batchGetFn    func(*awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error)
	batchWriteFn  func(*awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error)
	putFn         func(*awsdynamodb.PutItemInput) (*awsdynamodb.PutItemOutput, error)
	describeFn    func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error)
	executeFn     func(*awsdynamodb.ExecuteStatementInput) (*awsdynamodb.ExecuteStatementOutput, error)
	deleteTableFn func(*awsdynamodb.DeleteTableInput) (*awsdynamodb.DeleteTableOutput, error)

	scanCalls, batchGetCalls, batchWriteCalls, putCalls, execCalls, deleteTableCalls int
}

func (f *fakeDDB) ListTables(_ context.Context, in *awsdynamodb.ListTablesInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.ListTablesOutput, error) {
	if f.listTablesFn != nil {
		return f.listTablesFn(in)
	}
	return &awsdynamodb.ListTablesOutput{}, nil
}

func (f *fakeDDB) DescribeTable(_ context.Context, in *awsdynamodb.DescribeTableInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.DescribeTableOutput, error) {
	if f.describeFn != nil {
		return f.describeFn(in)
	}
	return &awsdynamodb.DescribeTableOutput{}, nil
}

func (f *fakeDDB) Scan(_ context.Context, in *awsdynamodb.ScanInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.ScanOutput, error) {
	f.scanCalls++
	if f.scanFn != nil {
		return f.scanFn(in)
	}
	return &awsdynamodb.ScanOutput{}, nil
}

func (f *fakeDDB) BatchGetItem(_ context.Context, in *awsdynamodb.BatchGetItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.BatchGetItemOutput, error) {
	f.batchGetCalls++
	if f.batchGetFn != nil {
		return f.batchGetFn(in)
	}
	return &awsdynamodb.BatchGetItemOutput{}, nil
}

func (f *fakeDDB) BatchWriteItem(_ context.Context, in *awsdynamodb.BatchWriteItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.BatchWriteItemOutput, error) {
	f.batchWriteCalls++
	if f.batchWriteFn != nil {
		return f.batchWriteFn(in)
	}
	return &awsdynamodb.BatchWriteItemOutput{}, nil
}

func (f *fakeDDB) PutItem(_ context.Context, in *awsdynamodb.PutItemInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error) {
	f.putCalls++
	if f.putFn != nil {
		return f.putFn(in)
	}
	return &awsdynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) DeleteTable(_ context.Context, in *awsdynamodb.DeleteTableInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.DeleteTableOutput, error) {
	f.deleteTableCalls++
	if f.deleteTableFn != nil {
		return f.deleteTableFn(in)
	}
	return &awsdynamodb.DeleteTableOutput{}, nil
}

func (f *fakeDDB) ExecuteStatement(_ context.Context, in *awsdynamodb.ExecuteStatementInput, _ ...func(*awsdynamodb.Options)) (*awsdynamodb.ExecuteStatementOutput, error) {
	f.execCalls++
	if f.executeFn != nil {
		return f.executeFn(in)
	}
	return &awsdynamodb.ExecuteStatementOutput{}, nil
}

// fakeStore builds a table-scoped Store backed by fake with a single integer
// partition key, and disables backoff sleeps for the duration of the test.
func fakeStore(t *testing.T, fake *fakeDDB) *Store {
	t.Helper()
	prev := backoffUnit
	backoffUnit = 0
	t.Cleanup(func() { backoffUnit = prev })
	return &Store{
		client:   fake,
		table:    "t",
		keys:     []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}},
		pageSize: scanBatch,
	}
}

func idItem(id int) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"id": &types.AttributeValueMemberN{Value: strconv.Itoa(id)}}
}

func TestLoadKeySchema(t *testing.T) {
	t.Run("partition key only", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
				KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
				AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeN}},
			}}, nil
		}}
		s := &Store{client: fake, table: "t"}
		require.NoError(t, s.loadKeySchema(context.Background()))
		require.Equal(t, []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}, s.keys)
	})

	t.Run("composite orders partition then sort", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
				// Deliberately list the sort key first to prove ordering is by KeyType, not input order.
				KeySchema: []types.KeySchemaElement{
					{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
					{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
				},
				AttributeDefinitions: []types.AttributeDefinition{
					{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
					{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeN},
				},
			}}, nil
		}}
		s := &Store{client: fake, table: "t"}
		require.NoError(t, s.loadKeySchema(context.Background()))
		require.Equal(t, []KeyAttr{
			{name: "pk", typ: types.ScalarAttributeTypeS},
			{name: "sk", typ: types.ScalarAttributeTypeN},
		}, s.keys)
	})

	t.Run("no partition key errors", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
				KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange}},
			}}, nil
		}}
		s := &Store{client: fake, table: "t"}
		require.ErrorContains(t, s.loadKeySchema(context.Background()), "no partition key")
	})

	t.Run("nil table errors", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return &awsdynamodb.DescribeTableOutput{}, nil
		}}
		s := &Store{client: fake, table: "t"}
		require.ErrorContains(t, s.loadKeySchema(context.Background()), "not found")
	})
}

func TestGetChunksKeysWithoutMisrouting(t *testing.T) {
	// 150 requested keys must split into two BatchGetItem calls (100 then 50); each key
	// must resolve to its own item, so a mis-computed chunk index (wrong slice bounds)
	// leaves some keys unresolved and fails the value assertions.
	var sizes []int
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		ka := in.RequestItems["t"]
		sizes = append(sizes, len(ka.Keys))
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": append([]map[string]types.AttributeValue{}, ka.Keys...)},
		}, nil
	}}
	s := fakeStore(t, fake)

	keys := make([]string, 0, 150)
	for i := 1; i <= 150; i++ {
		keys = append(keys, strconv.Itoa(i))
	}
	got, err := s.Get(context.Background(), keys)
	require.NoError(t, err)
	require.Len(t, got, 150)
	for i := 1; i <= 150; i++ {
		k := strconv.Itoa(i)
		require.NotNil(t, got[k], "key %s unresolved", k)
		require.Equal(t, i, got[k].(map[string]any)["id"])
	}
	require.Equal(t, []int{100, 50}, sizes)
	require.Equal(t, 2, fake.batchGetCalls)
}

func TestGetExactChunkBoundary(t *testing.T) {
	// Exactly batchGetMax keys must issue exactly one BatchGetItem call — an off-by-one
	// on the chunk boundary would add a second, empty request.
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		ka := in.RequestItems["t"]
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": append([]map[string]types.AttributeValue{}, ka.Keys...)},
		}, nil
	}}
	s := fakeStore(t, fake)
	keys := make([]string, 0, batchGetMax)
	for i := 1; i <= batchGetMax; i++ {
		keys = append(keys, strconv.Itoa(i))
	}
	_, err := s.Get(context.Background(), keys)
	require.NoError(t, err)
	require.Equal(t, 1, fake.batchGetCalls)
}

func TestGetRetriesUnprocessedThenSucceeds(t *testing.T) {
	calls := 0
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		calls++
		ka := in.RequestItems["t"]
		if calls == 1 {
			// Resolve nothing; return the whole request as unprocessed to force a retry.
			return &awsdynamodb.BatchGetItemOutput{UnprocessedKeys: map[string]types.KeysAndAttributes{"t": ka}}, nil
		}
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": append([]map[string]types.AttributeValue{}, ka.Keys...)},
		}, nil
	}}
	s := fakeStore(t, fake)
	got, err := s.Get(context.Background(), []string{"7"})
	require.NoError(t, err)
	require.Equal(t, 7, got["7"].(map[string]any)["id"])
	require.Equal(t, 2, fake.batchGetCalls) // exactly one retry after the unprocessed round
}

func TestGetUnprocessedExhausted(t *testing.T) {
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		// Never make progress: always return the request as unprocessed.
		return &awsdynamodb.BatchGetItemOutput{UnprocessedKeys: in.RequestItems}, nil
	}}
	s := fakeStore(t, fake)
	_, err := s.Get(context.Background(), []string{"1"})
	require.ErrorContains(t, err, "unprocessed")
	// The loop is bounded: one call per attempt up to maxUnprocessed, then it gives up.
	require.Equal(t, maxUnprocessed, fake.batchGetCalls)
}

func TestScanPaginatesAndBatchesPages(t *testing.T) {
	// Two server pages; the second is reached only if ExclusiveStartKey is threaded and
	// the break condition is correct. 250 items across the pages must arrive in pages of
	// 100 (the pageSize batching).
	page := 0
	fake := &fakeDDB{scanFn: func(in *awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		page++
		switch page {
		case 1:
			require.Nil(t, in.ExclusiveStartKey)
			items := make([]map[string]types.AttributeValue, 0, 150)
			for i := 1; i <= 150; i++ {
				items = append(items, idItem(i))
			}
			return &awsdynamodb.ScanOutput{Items: items, LastEvaluatedKey: idItem(150)}, nil
		default:
			require.NotNil(t, in.ExclusiveStartKey) // proves the cursor was threaded
			items := make([]map[string]types.AttributeValue, 0, 100)
			for i := 151; i <= 250; i++ {
				items = append(items, idItem(i))
			}
			return &awsdynamodb.ScanOutput{Items: items}, nil // empty LastEvaluatedKey ends the scan
		}
	}}
	s := fakeStore(t, fake)

	var pageSizes []int
	total := 0
	err := s.ScanBatches(context.Background(), func(batch map[string]any) error {
		pageSizes = append(pageSizes, len(batch))
		total += len(batch)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 250, total)
	require.Equal(t, 2, fake.scanCalls)
	require.Equal(t, []int{100, 100, 50}, pageSizes) // re-batched into even pages of 100
}

func TestScanNoTrailingEmptyPage(t *testing.T) {
	// 200 items (an exact multiple of the page size) must arrive as two full pages with
	// no trailing empty page — the final flush must be guarded on a non-empty page.
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		items := make([]map[string]types.AttributeValue, 0, 200)
		for i := 1; i <= 200; i++ {
			items = append(items, idItem(i))
		}
		return &awsdynamodb.ScanOutput{Items: items}, nil
	}}
	s := fakeStore(t, fake)
	var pageSizes []int
	require.NoError(t, s.ScanBatches(context.Background(), func(batch map[string]any) error {
		pageSizes = append(pageSizes, len(batch))
		return nil
	}))
	require.Equal(t, []int{100, 100}, pageSizes)
}

func TestScanFilteredSetsFilterExpression(t *testing.T) {
	var gotFilter *string
	fake := &fakeDDB{scanFn: func(in *awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		gotFilter = in.FilterExpression
		return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{idItem(1)}}, nil
	}}
	s := fakeStore(t, fake)
	pred := predicate.Eq{Path: []string{"author"}, Value: "x"}
	require.NoError(t, s.ScanFiltered(context.Background(), pred, func(map[string]any) error { return nil }))
	require.NotNil(t, gotFilter)
	require.Equal(t, "#n1 = :v1", *gotFilter)
}

func TestClearMultiBatchAndRetry(t *testing.T) {
	// One scan page of 26 items forces two BatchWriteItem chunks (25 + 1); the first
	// chunk returns an unprocessed leftover once, forcing a retry.
	scanned := false
	firstWrite := true
	var writeSizes []int
	fake := &fakeDDB{
		scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
			if scanned {
				return &awsdynamodb.ScanOutput{}, nil
			}
			scanned = true
			items := make([]map[string]types.AttributeValue, 0, 26)
			for i := 1; i <= 26; i++ {
				items = append(items, idItem(i))
			}
			return &awsdynamodb.ScanOutput{Items: items}, nil
		},
		batchWriteFn: func(in *awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error) {
			reqs := in.RequestItems["t"]
			writeSizes = append(writeSizes, len(reqs))
			if firstWrite {
				firstWrite = false
				// Return one item as unprocessed to force exactly one retry.
				return &awsdynamodb.BatchWriteItemOutput{UnprocessedItems: map[string][]types.WriteRequest{"t": {reqs[0]}}}, nil
			}
			return &awsdynamodb.BatchWriteItemOutput{}, nil
		},
	}
	s := fakeStore(t, fake)
	require.NoError(t, s.Clear(context.Background()))
	// chunk 1 (25) → retry (1) → chunk 2 (1): three BatchWriteItem calls.
	require.Equal(t, []int{25, 1, 1}, writeSizes)
	require.Equal(t, 3, fake.batchWriteCalls)
}

func TestClearBackoffHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeDDB{
		scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
			return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{idItem(1)}}, nil
		},
		batchWriteFn: func(in *awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error) {
			cancel() // cancel before the retry sleep
			return &awsdynamodb.BatchWriteItemOutput{UnprocessedItems: in.RequestItems}, nil
		},
	}
	// Keep a real (tiny) backoff so the cancel is observed in the sleep.
	prev := backoffUnit
	backoffUnit = 50 * time.Millisecond
	t.Cleanup(func() { backoffUnit = prev })
	s := &Store{client: fake, table: "t", keys: []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}, pageSize: scanBatch}
	err := s.Clear(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestClearExactBatchBoundary(t *testing.T) {
	// Exactly 50 items (two full batches of 25) must issue exactly two BatchWriteItem
	// calls — an off-by-one on the chunk boundary would add a third, empty batch.
	var sizes []int
	fake := &fakeDDB{
		scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
			items := make([]map[string]types.AttributeValue, 0, 50)
			for i := 1; i <= 50; i++ {
				items = append(items, idItem(i))
			}
			return &awsdynamodb.ScanOutput{Items: items}, nil
		},
		batchWriteFn: func(in *awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error) {
			sizes = append(sizes, len(in.RequestItems["t"]))
			return &awsdynamodb.BatchWriteItemOutput{}, nil
		},
	}
	s := fakeStore(t, fake)
	require.NoError(t, s.Clear(context.Background()))
	require.Equal(t, []int{25, 25}, sizes)
	require.Equal(t, 2, fake.batchWriteCalls)
}

func TestInspectTablesPaginates(t *testing.T) {
	page := 0
	fake := &fakeDDB{listTablesFn: func(*awsdynamodb.ListTablesInput) (*awsdynamodb.ListTablesOutput, error) {
		page++
		if page == 1 {
			return &awsdynamodb.ListTablesOutput{TableNames: []string{"a", "b"}, LastEvaluatedTableName: aws.String("b")}, nil
		}
		return &awsdynamodb.ListTablesOutput{TableNames: []string{"c"}}, nil
	}}
	s := &Store{client: fake, table: "t"}
	res, err := s.InspectTables(context.Background())
	require.NoError(t, err)
	require.Equal(t, []any{"a", "b", "c"}, res.(map[string]any)["tables"])
}

func TestInspectTableErrors(t *testing.T) {
	t.Run("no table selected", func(t *testing.T) {
		s := &Store{client: &fakeDDB{}, table: ""}
		_, err := s.InspectTable(context.Background())
		require.ErrorContains(t, err, "needs a table")
	})
	t.Run("nil description", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return &awsdynamodb.DescribeTableOutput{}, nil
		}}
		s := &Store{client: fake, table: "t"}
		_, err := s.InspectTable(context.Background())
		require.ErrorContains(t, err, "not found")
	})
}

func TestInspectTableMetadata(t *testing.T) {
	fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
		return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
			TableName:            aws.String("books"),
			TableStatus:          types.TableStatusActive,
			ItemCount:            aws.Int64(3),
			TableSizeBytes:       aws.Int64(234),
			KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
			AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeN}},
		}}, nil
	}}
	s := &Store{client: fake, table: "books"}
	res, err := s.InspectTable(context.Background())
	require.NoError(t, err)
	m := res.(map[string]any)
	require.Equal(t, "books", m["name"])
	require.Equal(t, int64(3), m["itemCount"])
	require.Equal(t, int64(234), m["sizeBytes"])
	require.Equal(t, "ACTIVE", m["status"])
	require.Equal(t, string(types.BillingModeProvisioned), m["billingMode"]) // absent summary defaults to PROVISIONED
	// Each schema entry carries both its attribute name and its type, so a dropped
	// field or a skipped loop body is visible, not just a length change.
	require.Equal(t, []any{map[string]any{"attribute": "id", "keyType": "HASH"}}, m["keySchema"])
	require.Equal(t, []any{map[string]any{"attribute": "id", "type": "N"}}, m["attributeDefinitions"])
	require.NotContains(t, m, "globalSecondaryIndexes") // omitted when the table has none
}

func TestInspectTableBillingMode(t *testing.T) {
	tests := []struct {
		name    string
		summary *types.BillingModeSummary
		want    string
	}{
		{"absent summary defaults to provisioned", nil, string(types.BillingModeProvisioned)},
		{"on-demand", &types.BillingModeSummary{BillingMode: types.BillingModePayPerRequest}, "PAY_PER_REQUEST"},
		{"provisioned", &types.BillingModeSummary{BillingMode: types.BillingModeProvisioned}, "PROVISIONED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
				return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
					TableName:          aws.String("t"),
					BillingModeSummary: tt.summary,
				}}, nil
			}}
			s := &Store{client: fake, table: "t"}
			res, err := s.InspectTable(context.Background())
			require.NoError(t, err)
			require.Equal(t, tt.want, res.(map[string]any)["billingMode"])
		})
	}
}

func TestInspectTableMultipleSchemaEntries(t *testing.T) {
	// A composite table renders every key schema and attribute definition entry, so a
	// loop that stops after the first is visible.
	fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
		return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
			TableName: aws.String("events"),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
			},
			AttributeDefinitions: []types.AttributeDefinition{
				{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
				{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeN},
			},
		}}, nil
	}}
	s := &Store{client: fake, table: "events"}
	res, err := s.InspectTable(context.Background())
	require.NoError(t, err)
	m := res.(map[string]any)
	require.Equal(t, []any{
		map[string]any{"attribute": "pk", "keyType": "HASH"},
		map[string]any{"attribute": "sk", "keyType": "RANGE"},
	}, m["keySchema"])
	require.Equal(t, []any{
		map[string]any{"attribute": "pk", "type": "S"},
		map[string]any{"attribute": "sk", "type": "N"},
	}, m["attributeDefinitions"])
}

func TestInspectTableIncludesIndexes(t *testing.T) {
	fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
		return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{
			TableName:              aws.String("t"),
			GlobalSecondaryIndexes: []types.GlobalSecondaryIndexDescription{{IndexName: aws.String("by-author")}},
		}}, nil
	}}
	s := &Store{client: fake, table: "t"}
	res, err := s.InspectTable(context.Background())
	require.NoError(t, err)
	require.Equal(t, []any{"by-author"}, res.(map[string]any)["globalSecondaryIndexes"])
}

func TestTableOf(t *testing.T) {
	require.Equal(t, "books", tableOf(&awsdynamodb.ScanInput{TableName: aws.String("books")}))
	require.Empty(t, tableOf(&awsdynamodb.ScanInput{}))       // nil TableName pointer
	require.Empty(t, tableOf(&awsdynamodb.ListTablesInput{})) // no TableName field
	require.Empty(t, tableOf((*awsdynamodb.ScanInput)(nil)))  // nil pointer
	require.Empty(t, tableOf("not-a-struct"))                 // non-struct
	require.Empty(t, tableOf(42))                             // non-pointer, non-struct
	// A TableName field that is not a pointer is skipped, never dereferenced: IsNil
	// panics on a non-nilable kind, so both guards before it must hold.
	require.Empty(t, tableOf(struct{ TableName string }{TableName: "books"}))
	require.Empty(t, tableOf(&struct{ TableName int }{TableName: 7}))
	// A struct value (not a pointer) carrying a pointer TableName is still read.
	require.Equal(t, "books", tableOf(struct{ TableName *string }{TableName: aws.String("books")}))
}

func TestFormatRawFallback(t *testing.T) {
	s := &Store{}
	// A well-formed value renders as JSON.
	require.Contains(t, s.FormatRaw(map[string]any{"a": 1}, false), `"a"`)
	// A value the JSON renderer cannot encode falls back to its Go form, never an error.
	require.NotEmpty(t, s.FormatRaw(make(chan int), false))
}

func TestKeyProjectionComposite(t *testing.T) {
	s := &Store{keys: []KeyAttr{
		{name: "pk", typ: types.ScalarAttributeTypeS},
		{name: "sk", typ: types.ScalarAttributeTypeN},
	}}
	proj, names := s.keyProjection()
	require.Equal(t, "#k0, #k1", proj)
	require.Equal(t, map[string]string{"#k0": "pk", "#k1": "sk"}, names)
}

func TestQueryPaginatesNextToken(t *testing.T) {
	page := 0
	fake := &fakeDDB{executeFn: func(in *awsdynamodb.ExecuteStatementInput) (*awsdynamodb.ExecuteStatementOutput, error) {
		page++
		if page == 1 {
			require.Nil(t, in.NextToken)
			return &awsdynamodb.ExecuteStatementOutput{
				Items:     []map[string]types.AttributeValue{idItem(1)},
				NextToken: aws.String("tok"),
			}, nil
		}
		require.NotNil(t, in.NextToken)
		return &awsdynamodb.ExecuteStatementOutput{Items: []map[string]types.AttributeValue{idItem(2)}}, nil
	}}
	s := fakeStore(t, fake)
	res, err := s.Query(context.Background(), []string{"SELECT * FROM t"})
	require.NoError(t, err)
	require.Len(t, res.([]any), 2)
	require.Equal(t, 2, fake.execCalls)
}

func TestPutInsertOnlySkipsExisting(t *testing.T) {
	fake := &fakeDDB{putFn: func(in *awsdynamodb.PutItemInput) (*awsdynamodb.PutItemOutput, error) {
		require.NotNil(t, in.ConditionExpression) // insert-only sets the guard
		return nil, &types.ConditionalCheckFailedException{}
	}}
	s := fakeStore(t, fake)
	stat, err := s.Put(context.Background(),
		[]query.Record{{Key: "1", Value: map[string]any{"id": 1}}}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, 1, stat.Skipped)
	require.Equal(t, 0, stat.Written)
	require.Zero(t, fake.batchGetCalls) // insert-only never pre-reads
}

func TestPutUpsertCountsOverwritesFromKeyOnlyPreRead(t *testing.T) {
	var gotProj string
	var gotNames map[string]string
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		ka := in.RequestItems["t"]
		gotProj = aws.ToString(ka.ProjectionExpression)
		gotNames = ka.ExpressionAttributeNames
		// Key 1 already exists; key 2 does not.
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": {idItem(1)}},
		}, nil
	}}
	s := fakeStore(t, fake)
	stat, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "A"}},
		{Key: "2", Value: map[string]any{"id": 2, "title": "B"}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 1}, stat)
	// The pre-read projected to the key attribute only, so it never fetched values.
	require.Equal(t, "#k0", gotProj)
	require.Equal(t, map[string]string{"#k0": "id"}, gotNames)
	require.Equal(t, 1, fake.batchGetCalls)
	require.Equal(t, 2, fake.putCalls)
}

func TestPutUpsertPreReadExactChunkBoundary(t *testing.T) {
	// Exactly batchGetMax keys must pre-read in exactly one BatchGetItem call — an
	// off-by-one on the chunk boundary would add a second, empty request.
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		ka := in.RequestItems["t"]
		require.NotEmpty(t, ka.Keys) // never a spurious empty pre-read
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": append([]map[string]types.AttributeValue{}, ka.Keys...)},
		}, nil
	}}
	s := fakeStore(t, fake)
	batch := make([]query.Record, 0, batchGetMax)
	for i := 1; i <= batchGetMax; i++ {
		batch = append(batch, query.Record{Key: strconv.Itoa(i), Value: map[string]any{"id": i}})
	}
	stat, err := s.Put(context.Background(), batch, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, 1, fake.batchGetCalls)
	require.Equal(t, query.WriteStat{Overwritten: batchGetMax}, stat) // all pre-existed
}

func TestPutUpsertPreReadError(t *testing.T) {
	fake := &fakeDDB{batchGetFn: func(*awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		return nil, errors.New("throttled")
	}}
	s := fakeStore(t, fake)
	_, err := s.Put(context.Background(),
		[]query.Record{{Key: "1", Value: map[string]any{"id": 1}}}, query.Upsert)
	require.ErrorContains(t, err, "dynamodb batch get")
	require.Zero(t, fake.putCalls) // a failed pre-read aborts before any write
}

func TestPutUpsertDeduplicatesPreReadKeys(t *testing.T) {
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		// BatchGetItem rejects duplicate keys, so the pre-read must send key 1 once.
		require.Len(t, in.RequestItems["t"].Keys, 1)
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": {idItem(1)}},
		}, nil
	}}
	s := fakeStore(t, fake)
	stat, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1, "title": "A"}},
		{Key: "1", Value: map[string]any{"id": 1, "title": "B"}},
	}, query.Upsert)
	require.NoError(t, err)
	// Both writes land on the existing key, so both count as overwrites.
	require.Equal(t, query.WriteStat{Overwritten: 2}, stat)
	require.Equal(t, 1, fake.batchGetCalls)
}

func TestDropDeletesTable(t *testing.T) {
	fake := &fakeDDB{}
	s := fakeStore(t, fake)
	require.NoError(t, s.Drop(context.Background()))
	require.Equal(t, 1, fake.deleteTableCalls)
}

func TestEstimateCountFromMetadata(t *testing.T) {
	fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
		return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{ItemCount: aws.Int64(42)}}, nil
	}}
	s := fakeStore(t, fake)
	n, err := s.EstimateCount(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(42), n)
}

// errBoom is the sentinel a fake returns when the test asserts the driver keeps the
// backend's cause in the error chain.
var errBoom = errors.New("boom")

func TestGetGuards(t *testing.T) {
	t.Run("no table selected", func(t *testing.T) {
		// The guard fires before any key decoding, so an empty key list still reports
		// the missing table rather than an empty result.
		fake := &fakeDDB{}
		s := &Store{client: fake, table: "t"}
		_, err := s.Get(context.Background(), nil)
		require.ErrorIs(t, err, errNoTable)
		require.Zero(t, fake.batchGetCalls)
	})

	t.Run("empty keys return an allocated empty map", func(t *testing.T) {
		fake := &fakeDDB{}
		s := fakeStore(t, fake)
		got, err := s.Get(context.Background(), nil)
		require.NoError(t, err)
		require.NotNil(t, got) // an allocated map, never a nil one
		require.Empty(t, got)
		require.Zero(t, fake.batchGetCalls) // no round-trip for an empty request
	})
}

func TestGetWrapsBatchGetError(t *testing.T) {
	fake := &fakeDDB{batchGetFn: func(*awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		return nil, errBoom
	}}
	s := fakeStore(t, fake)
	_, err := s.Get(context.Background(), []string{"1"})
	require.ErrorContains(t, err, "dynamodb batch get")
	require.ErrorIs(t, err, errBoom) // the backend cause stays in the chain
}

func TestGetBackoffHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		cancel() // cancel before the retry sleep
		return &awsdynamodb.BatchGetItemOutput{UnprocessedKeys: in.RequestItems}, nil
	}}
	// Keep a real (tiny) backoff so the cancel is observed in the sleep.
	prev := backoffUnit
	backoffUnit = 50 * time.Millisecond
	t.Cleanup(func() { backoffUnit = prev })
	s := &Store{client: fake, table: "t", keys: []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}, pageSize: scanBatch}
	_, err := s.Get(ctx, []string{"1"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, fake.batchGetCalls) // the cancelled backoff stops the retry loop
}

func TestScanWrapsScanError(t *testing.T) {
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		return nil, errBoom
	}}
	s := fakeStore(t, fake)
	err := s.ScanBatches(context.Background(), func(map[string]any) error { return nil })
	require.ErrorContains(t, err, "dynamodb scan")
	require.ErrorIs(t, err, errBoom) // the backend cause stays in the chain
}

func TestScanStopsOnCallbackError(t *testing.T) {
	// 150 items fill one page mid-scan; the callback's error must abort the scan there,
	// so the second (trailing) page is never handed over.
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		items := make([]map[string]types.AttributeValue, 0, 150)
		for i := 1; i <= 150; i++ {
			items = append(items, idItem(i))
		}
		return &awsdynamodb.ScanOutput{Items: items}, nil
	}}
	s := fakeStore(t, fake)
	pages := 0
	err := s.ScanBatches(context.Background(), func(map[string]any) error {
		pages++
		return errBoom
	})
	require.ErrorIs(t, err, errBoom)
	require.Equal(t, 1, pages) // aborted at the first full page, not after the trailing one
}

func TestQueryRejectsEmptyStatement(t *testing.T) {
	fake := &fakeDDB{}
	s := fakeStore(t, fake)
	_, err := s.Query(context.Background(), nil)
	require.ErrorContains(t, err, "empty statement")
	require.Zero(t, fake.execCalls) // never round-trips an empty statement
}

func TestEstimateCountMissingMetadata(t *testing.T) {
	tests := []struct {
		name  string
		table *types.TableDescription
	}{
		{"nil table description", nil},
		{"nil item count", &types.TableDescription{TableName: aws.String("t")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
				return &awsdynamodb.DescribeTableOutput{Table: tt.table}, nil
			}}
			s := fakeStore(t, fake)
			// Both halves of the guard must be checked before the pointer is read, so
			// missing metadata is a zero hint, never a panic.
			n, err := s.EstimateCount(context.Background())
			require.NoError(t, err)
			require.Zero(t, n)
		})
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	prev := backoffUnit
	backoffUnit = 2 * time.Millisecond
	t.Cleanup(func() { backoffUnit = prev })
	// The wait is jittered upward by up to half the base, so the base is a hard lower
	// bound and twice the base a hard upper one. maxWait is 0 where only the lower
	// bound is meaningful (a short wait cannot be timed tightly from above).
	tests := []struct {
		name             string
		attempt          int
		minWait, maxWait time.Duration
	}{
		{"first attempt waits one unit", 0, 2 * time.Millisecond, 0},
		{"second attempt doubles it", 1, 4 * time.Millisecond, 0},
		{"growth is exponential up to the cap", 5, 64 * time.Millisecond, 128 * time.Millisecond},
		{"past the cap the wait stops growing", 6, 64 * time.Millisecond, 128 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A timer never fires early, so the lower bound holds for every sample. It
			// can fire late under load, so the upper bound is asserted on the fastest
			// of at most three samples, which no scheduling hiccup can inflate.
			var fastest time.Duration
			for i := range 3 {
				start := time.Now()
				require.NoError(t, backoff(context.Background(), tt.attempt))
				elapsed := time.Since(start)
				require.GreaterOrEqual(t, elapsed, tt.minWait)
				if i == 0 || elapsed < fastest {
					fastest = elapsed
				}
				if tt.maxWait == 0 || fastest < tt.maxWait {
					break
				}
			}
			if tt.maxWait > 0 {
				require.Less(t, fastest, tt.maxWait)
			}
		})
	}
}

func TestPutInsertOnlyContinuesPastASkip(t *testing.T) {
	// The first record's key exists (a conditional-check failure); the rest of the
	// batch must still be written, so the skip continues the loop rather than ending it.
	calls := 0
	fake := &fakeDDB{putFn: func(*awsdynamodb.PutItemInput) (*awsdynamodb.PutItemOutput, error) {
		calls++
		if calls == 1 {
			return nil, &types.ConditionalCheckFailedException{}
		}
		return &awsdynamodb.PutItemOutput{}, nil
	}}
	s := fakeStore(t, fake)
	stat, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "2", Value: map[string]any{"id": 2}},
		{Key: "3", Value: map[string]any{"id": 3}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Skipped: 1, Written: 2}, stat)
	require.Equal(t, 3, fake.putCalls)
}

func TestPutErrorsAreNotSilentSkips(t *testing.T) {
	tests := []struct {
		name string
		mode query.WriteMode
		err  error
	}{
		{"insert-only surfaces a non-conditional failure", query.InsertOnly, errBoom},
		{"upsert surfaces a conditional failure", query.Upsert, &types.ConditionalCheckFailedException{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDDB{putFn: func(*awsdynamodb.PutItemInput) (*awsdynamodb.PutItemOutput, error) {
				return nil, tt.err
			}}
			s := fakeStore(t, fake)
			stat, err := s.Put(context.Background(),
				[]query.Record{{Key: "1", Value: map[string]any{"id": 1}}}, tt.mode)
			require.ErrorContains(t, err, "dynamodb put")
			require.ErrorIs(t, err, tt.err)
			require.Equal(t, query.WriteStat{}, stat) // never counted as a skip
		})
	}
}

func TestPutUpsertSkipsTheKeylessRecordInThePreRead(t *testing.T) {
	// A keyless record carries its key attributes in the object; it has no string key
	// to pre-read, and sending an empty one would fail the numeric key decode.
	fake := &fakeDDB{batchGetFn: func(*awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		return &awsdynamodb.BatchGetItemOutput{}, nil
	}}
	s := fakeStore(t, fake)
	stat, err := s.Put(context.Background(),
		[]query.Record{{Key: "", Value: map[string]any{"id": 5}}}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 1}, stat)
	require.Zero(t, fake.batchGetCalls) // nothing left to pre-read
	require.Equal(t, 1, fake.putCalls)
}

func TestClearUnprocessedExhausted(t *testing.T) {
	// The write batch never drains: after the bounded retries the leftovers must be
	// reported, never silently dropped.
	scanned := false
	fake := &fakeDDB{
		scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
			if scanned {
				return &awsdynamodb.ScanOutput{}, nil
			}
			scanned = true
			return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{idItem(1)}}, nil
		},
		batchWriteFn: func(in *awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error) {
			return &awsdynamodb.BatchWriteItemOutput{UnprocessedItems: in.RequestItems}, nil
		},
	}
	s := fakeStore(t, fake)
	err := s.Clear(context.Background())
	require.ErrorContains(t, err, "unprocessed")
	require.Equal(t, maxUnprocessed, fake.batchWriteCalls)
}

func TestDeleteSurfacesTheBatchWriteError(t *testing.T) {
	fake := &fakeDDB{
		batchGetFn: func(*awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
			return &awsdynamodb.BatchGetItemOutput{}, nil
		},
		batchWriteFn: func(*awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error) {
			return nil, errBoom
		},
	}
	s := fakeStore(t, fake)
	stat, err := s.Delete(context.Background(), []string{"1"})
	require.ErrorContains(t, err, "dynamodb batch write")
	require.ErrorIs(t, err, errBoom)
	require.Equal(t, query.DeleteStat{}, stat) // no accounting from a failed delete
}

func TestDropSurfacesTheDeleteTableError(t *testing.T) {
	fake := &fakeDDB{deleteTableFn: func(*awsdynamodb.DeleteTableInput) (*awsdynamodb.DeleteTableOutput, error) {
		return nil, errBoom
	}}
	s := fakeStore(t, fake)
	err := s.Drop(context.Background())
	require.ErrorContains(t, err, "dynamodb delete table")
	require.ErrorIs(t, err, errBoom)
}
