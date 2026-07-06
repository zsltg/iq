package dynamodb

import (
	"context"
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
		keys:     []keyAttr{{name: "id", typ: types.ScalarAttributeTypeN}},
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
		require.Equal(t, []keyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}, s.keys)
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
		require.Equal(t, []keyAttr{
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
	s := &Store{client: fake, table: "t", keys: []keyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}, pageSize: scanBatch}
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
	require.Len(t, m["keySchema"], 1)
	require.NotContains(t, m, "globalSecondaryIndexes") // omitted when the table has none
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
	require.Equal(t, "", tableOf(&awsdynamodb.ScanInput{}))       // nil TableName pointer
	require.Equal(t, "", tableOf(&awsdynamodb.ListTablesInput{})) // no TableName field
	require.Equal(t, "", tableOf((*awsdynamodb.ScanInput)(nil)))  // nil pointer
	require.Equal(t, "", tableOf("not-a-struct"))                 // non-struct
	require.Equal(t, "", tableOf(42))                             // non-pointer, non-struct
}

func TestFormatRawFallback(t *testing.T) {
	s := &Store{}
	// A well-formed value renders as JSON.
	require.Contains(t, s.FormatRaw(map[string]any{"a": 1}, false), `"a"`)
	// A value the JSON renderer cannot encode falls back to its Go form, never an error.
	require.NotEmpty(t, s.FormatRaw(make(chan int), false))
}

func TestKeyProjectionComposite(t *testing.T) {
	s := &Store{keys: []keyAttr{
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
