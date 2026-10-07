package dynamodb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

type ctxKey struct{}

// ctxFake records the context and the request of each call, then delegates to the
// embedded fakeDDB, so a test can prove that a helper passes both on unchanged.
type ctxFake struct {
	*fakeDDB
	puts      []*awsdynamodb.PutItemInput
	putCtxs   []context.Context
	getCtxs   []context.Context
	writes    []*awsdynamodb.BatchWriteItemInput
	writeCtxs []context.Context
	describes []*awsdynamodb.DescribeTableInput
	descCtxs  []context.Context
}

func (f *ctxFake) PutItem(ctx context.Context, in *awsdynamodb.PutItemInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.PutItemOutput, error) {
	f.puts = append(f.puts, in)
	f.putCtxs = append(f.putCtxs, ctx)
	return f.fakeDDB.PutItem(ctx, in, o...)
}

func (f *ctxFake) BatchGetItem(ctx context.Context, in *awsdynamodb.BatchGetItemInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.BatchGetItemOutput, error) {
	f.getCtxs = append(f.getCtxs, ctx)
	return f.fakeDDB.BatchGetItem(ctx, in, o...)
}

func (f *ctxFake) BatchWriteItem(ctx context.Context, in *awsdynamodb.BatchWriteItemInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.BatchWriteItemOutput, error) {
	f.writes = append(f.writes, in)
	f.writeCtxs = append(f.writeCtxs, ctx)
	return f.fakeDDB.BatchWriteItem(ctx, in, o...)
}

func (f *ctxFake) DescribeTable(ctx context.Context, in *awsdynamodb.DescribeTableInput, o ...func(*awsdynamodb.Options)) (*awsdynamodb.DescribeTableOutput, error) {
	f.describes = append(f.describes, in)
	f.descCtxs = append(f.descCtxs, ctx)
	return f.fakeDDB.DescribeTable(ctx, in, o...)
}

// tagged returns a context that carries a marker value, so a test can tell it from any
// other context.
func tagged() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, "marker")
}

func requireTagged(t *testing.T, ctxs []context.Context) {
	t.Helper()
	require.NotEmpty(t, ctxs)
	for _, c := range ctxs {
		require.Equal(t, "marker", c.Value(ctxKey{}))
	}
}

func ctxStore(t *testing.T, fake *fakeDDB) (*Store, *ctxFake) {
	t.Helper()
	s := fakeStore(t, fake)
	cf := &ctxFake{fakeDDB: fake}
	s.client = cf
	return s, cf
}

func TestPutRequestFields(t *testing.T) {
	rec := func(id int, title string) query.Record {
		return query.Record{Key: strconv.Itoa(id), Value: map[string]any{"id": 999, "title": title}}
	}
	t.Run("upsert sends the item with no condition", func(t *testing.T) {
		s, cf := ctxStore(t, &fakeDDB{})
		_, err := s.Put(tagged(), []query.Record{rec(1, "A"), rec(2, "B")}, query.Upsert)
		require.NoError(t, err)
		require.Len(t, cf.puts, 2)
		for i, title := range []string{"A", "B"} {
			require.Equal(t, "t", aws.ToString(cf.puts[i].TableName))
			require.Equal(t, map[string]types.AttributeValue{
				"id":    &types.AttributeValueMemberN{Value: strconv.Itoa(i + 1)},
				"title": &types.AttributeValueMemberS{Value: title},
			}, cf.puts[i].Item)
			require.Nil(t, cf.puts[i].ConditionExpression)
			require.Nil(t, cf.puts[i].ExpressionAttributeNames)
		}
		requireTagged(t, cf.putCtxs)
		requireTagged(t, cf.getCtxs)
	})
	t.Run("insert-only guards the partition key", func(t *testing.T) {
		s, cf := ctxStore(t, &fakeDDB{})
		_, err := s.Put(tagged(), []query.Record{rec(1, "A")}, query.InsertOnly)
		require.NoError(t, err)
		require.Len(t, cf.puts, 1)
		require.Equal(t, "attribute_not_exists(#pk)", aws.ToString(cf.puts[0].ConditionExpression))
		require.Equal(t, map[string]string{"#pk": "id"}, cf.puts[0].ExpressionAttributeNames)
		requireTagged(t, cf.putCtxs)
	})
	t.Run("insert-only on a composite key guards the partition key only", func(t *testing.T) {
		s, cf := ctxStore(t, &fakeDDB{})
		s.keys = []KeyAttr{{name: "pk", typ: types.ScalarAttributeTypeS}, {name: "sk", typ: types.ScalarAttributeTypeN}}
		_, err := s.Put(context.Background(),
			[]query.Record{{Key: `["a","1"]`, Value: map[string]any{"x": 1}}}, query.InsertOnly)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"#pk": "pk"}, cf.puts[0].ExpressionAttributeNames)
	})
}

func TestPutBuildsEveryItemBeforeTheFirstWrite(t *testing.T) {
	s, cf := ctxStore(t, &fakeDDB{})
	_, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "2", Value: map[string]any{"id": 2, "bad": struct{}{}}},
	}, query.InsertOnly)
	require.ErrorContains(t, err, "cannot write value")
	require.Empty(t, cf.puts)
}

func TestPutDiscardsThePartialCountOnError(t *testing.T) {
	calls := 0
	fake := &fakeDDB{putFn: func(*awsdynamodb.PutItemInput) (*awsdynamodb.PutItemOutput, error) {
		calls++
		switch calls {
		case 1:
			return &awsdynamodb.PutItemOutput{}, nil
		case 2:
			return nil, &types.ConditionalCheckFailedException{}
		}
		return nil, errBoom
	}}
	s, cf := ctxStore(t, fake)
	stat, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "2", Value: map[string]any{"id": 2}},
		{Key: "3", Value: map[string]any{"id": 3}},
	}, query.InsertOnly)
	require.ErrorIs(t, err, errBoom)
	require.Equal(t, query.WriteStat{}, stat)
	require.Len(t, cf.puts, 3) // written in batch order up to the failure
}

func TestPutTallyCoversEveryOutcome(t *testing.T) {
	calls := 0
	fake := &fakeDDB{putFn: func(*awsdynamodb.PutItemInput) (*awsdynamodb.PutItemOutput, error) {
		calls++
		if calls == 2 {
			return nil, &types.ConditionalCheckFailedException{}
		}
		return &awsdynamodb.PutItemOutput{}, nil
	}}
	s, _ := ctxStore(t, fake)
	stat, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "2", Value: map[string]any{"id": 2}},
		{Key: "3", Value: map[string]any{"id": 3}},
		{Key: "4", Value: map[string]any{"id": 4}},
	}, query.InsertOnly)
	require.NoError(t, err)
	require.Equal(t, query.WriteStat{Written: 3, Skipped: 1}, stat)
}

func TestDeleteRequestFieldsAndContext(t *testing.T) {
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": {idItem(2)}},
		}, nil
	}}
	s, cf := ctxStore(t, fake)
	stat, err := s.Delete(tagged(), []string{"1", "2", "2"})
	require.NoError(t, err)
	// Key 2 exists. The count follows the position of each requested key.
	require.Equal(t, query.DeleteStat{Deleted: 2, Missing: 1}, stat)
	require.Len(t, cf.writes, 1)
	require.Len(t, cf.writes[0].RequestItems, 1)
	reqs := cf.writes[0].RequestItems["t"]
	require.Len(t, reqs, 3)
	for i, want := range []int{1, 2, 2} {
		require.Equal(t, idItem(want), reqs[i].DeleteRequest.Key)
		require.Nil(t, reqs[i].PutRequest)
	}
	requireTagged(t, cf.writeCtxs)
	requireTagged(t, cf.getCtxs)
}

func TestDeleteItemsChunksAndResendsOnlyTheLeftovers(t *testing.T) {
	var sent []int
	fake := &fakeDDB{batchWriteFn: func(in *awsdynamodb.BatchWriteItemInput) (*awsdynamodb.BatchWriteItemOutput, error) {
		reqs := in.RequestItems["t"]
		sent = append(sent, len(reqs))
		if len(sent) == 1 {
			// Return the last two requests of the first chunk as unprocessed.
			return &awsdynamodb.BatchWriteItemOutput{
				UnprocessedItems: map[string][]types.WriteRequest{"t": reqs[len(reqs)-2:]},
			}, nil
		}
		return &awsdynamodb.BatchWriteItemOutput{}, nil
	}}
	s, cf := ctxStore(t, fake)
	items := make([]map[string]types.AttributeValue, 26)
	for i := range items {
		items[i] = idItem(i)
	}
	require.NoError(t, s.deleteItems(tagged(), items))
	require.Equal(t, []int{25, 2, 1}, sent)
	requireTagged(t, cf.writeCtxs)
	last := cf.writes[1].RequestItems["t"]
	require.Equal(t, idItem(23), last[0].DeleteRequest.Key)
	require.Equal(t, idItem(24), last[1].DeleteRequest.Key)
}

func TestGetMalformedKeyInASecondChunkReturnsNothing(t *testing.T) {
	keys := make([]string, 0, batchGetMax+1)
	for i := 1; i <= batchGetMax; i++ {
		keys = append(keys, strconv.Itoa(i))
	}
	keys = append(keys, "not-a-number")
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		return &awsdynamodb.BatchGetItemOutput{
			Responses: map[string][]map[string]types.AttributeValue{"t": in.RequestItems["t"].Keys},
		}, nil
	}}
	s := fakeStore(t, fake)
	got, err := s.Get(context.Background(), keys)
	require.ErrorContains(t, err, "is not a number")
	require.Nil(t, got)
	require.Equal(t, 1, fake.batchGetCalls) // the second chunk never reaches the backend
}

func TestGetPassesTheContextAndKeys(t *testing.T) {
	s, cf := ctxStore(t, &fakeDDB{})
	_, err := s.Get(tagged(), []string{"1"})
	require.NoError(t, err)
	requireTagged(t, cf.getCtxs)
}

func describeOut(keys []types.KeySchemaElement, defs ...types.AttributeDefinition) *awsdynamodb.DescribeTableOutput {
	return &awsdynamodb.DescribeTableOutput{Table: &types.TableDescription{KeySchema: keys, AttributeDefinitions: defs}}
}

func TestLoadKeySchemaDetails(t *testing.T) {
	hash := func(n string) types.KeySchemaElement {
		return types.KeySchemaElement{AttributeName: aws.String(n), KeyType: types.KeyTypeHash}
	}
	tests := []struct {
		name string
		out  *awsdynamodb.DescribeTableOutput
		want []KeyAttr
	}{
		{"a key without a definition has the zero type", describeOut([]types.KeySchemaElement{hash("id")}), []KeyAttr{{name: "id"}}},
		{"the later partition key wins", describeOut([]types.KeySchemaElement{hash("a"), hash("b")}), []KeyAttr{{name: "b"}}},
		{
			"an unknown key type is ignored",
			describeOut([]types.KeySchemaElement{hash("a"), {AttributeName: aws.String("z"), KeyType: "OTHER"}}),
			[]KeyAttr{{name: "a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) { return tt.out, nil }}
			s := &Store{client: fake, table: "t"}
			require.NoError(t, s.loadKeySchema(context.Background()))
			require.Equal(t, tt.want, s.keys)
		})
	}
}

func TestLoadKeySchemaRequestAndFailures(t *testing.T) {
	t.Run("request carries the table and the context", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return describeOut([]types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}}), nil
		}}
		cf := &ctxFake{fakeDDB: fake}
		s := &Store{client: cf, table: "books"}
		require.NoError(t, s.loadKeySchema(tagged()))
		require.Len(t, cf.describes, 1)
		require.Equal(t, "books", aws.ToString(cf.describes[0].TableName))
		requireTagged(t, cf.descCtxs)
	})
	t.Run("describe error keeps the cause and leaves keys unset", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return nil, errBoom
		}}
		s := &Store{client: fake, table: "books"}
		err := s.loadKeySchema(context.Background())
		require.ErrorIs(t, err, errBoom)
		require.ErrorContains(t, err, `dynamodb describe table "books"`)
		require.Nil(t, s.keys)
	})
	t.Run("no partition key leaves keys unset", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return describeOut([]types.KeySchemaElement{{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange}}), nil
		}}
		s := &Store{client: fake, table: "books"}
		require.EqualError(t, s.loadKeySchema(context.Background()), `dynamodb: table "books" has no partition key`)
		require.Nil(t, s.keys)
	})
}

func TestScanPagesKeepOrderAndStopOnError(t *testing.T) {
	pages := [][]map[string]types.AttributeValue{{idItem(1), idItem(2), idItem(3)}, {idItem(4), idItem(5)}}
	call := 0
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		out := &awsdynamodb.ScanOutput{Items: pages[call]}
		if call == 0 {
			out.LastEvaluatedKey = idItem(3)
		}
		call++
		return out, nil
	}}
	s := fakeStore(t, fake)
	s.pageSize = 2
	var got [][]string
	require.NoError(t, s.ScanBatches(context.Background(), func(b map[string]any) error {
		got = append(got, keysOf(b))
		return nil
	}))
	require.Equal(t, [][]string{{"1", "2"}, {"3", "4"}, {"5"}}, got)

	call = 0
	fake.scanCalls = 0
	n := 0
	err := s.ScanBatches(context.Background(), func(map[string]any) error {
		n++
		return errBoom
	})
	require.ErrorIs(t, err, errBoom)
	require.Equal(t, 1, n)
	require.Equal(t, 1, fake.scanCalls) // the second Scan page is never fetched
}

// openServer starts a stub DynamoDB endpoint that records each request's headers and
// answers every call with answer.
func openServer(t *testing.T, answer func(w http.ResponseWriter, calls int)) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var seen []http.Header
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		answer(w, int(calls.Add(1)))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return append([]http.Header(nil), seen...)
	}
}

func TestOpenOptionsReachTheClient(t *testing.T) {
	t.Run("region, dummy credentials and endpoint", func(t *testing.T) {
		srv, headers := openServer(t, func(w http.ResponseWriter, _ int) { _, _ = w.Write([]byte(`{"TableNames":[]}`)) })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		st, err := Open(ctx, "dynamodb://eu-west-2/?endpoint="+srv.URL, "", nil, numfmt.DecimalNumber)
		require.NoError(t, err)
		require.Equal(t, numfmt.DecimalNumber, st.decimal)
		require.Equal(t, scanBatch, st.pageSize)
		require.Empty(t, st.table)
		seen := headers()
		require.Len(t, seen, 1)
		auth := seen[0].Get("Authorization")
		require.Contains(t, auth, "Credential=dummy/")
		require.Contains(t, auth, "/eu-west-2/dynamodb/")
	})
	t.Run("trace logs the probe", func(t *testing.T) {
		srv, _ := openServer(t, func(w http.ResponseWriter, _ int) { _, _ = w.Write([]byte(`{"TableNames":[]}`)) })
		var trace strings.Builder
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		_, err := Open(ctx, "dynamodb://us-east-1/?endpoint="+srv.URL, "", &trace, numfmt.DecimalAuto)
		require.NoError(t, err)
		require.Equal(t, "ddb> ListTables\n", trace.String())
	})
	t.Run("no trace writer logs nothing and still connects", func(t *testing.T) {
		srv, _ := openServer(t, func(w http.ResponseWriter, _ int) { _, _ = w.Write([]byte(`{"TableNames":[]}`)) })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		_, err := Open(ctx, "dynamodb://us-east-1/?endpoint="+srv.URL, "", nil, numfmt.DecimalAuto)
		require.NoError(t, err)
	})
	t.Run("a selected table loads its key schema", func(t *testing.T) {
		srv, _ := openServer(t, func(w http.ResponseWriter, calls int) {
			if calls == 1 {
				_, _ = w.Write([]byte(`{"TableNames":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"Table":{"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],` +
				`"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"N"}]}}`))
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		st, err := Open(ctx, "dynamodb://us-east-1/?endpoint="+srv.URL, "books", nil, numfmt.DecimalAuto)
		require.NoError(t, err)
		require.Equal(t, "books", st.table)
		require.Equal(t, []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}, st.keys)
	})
	t.Run("a server error is retried with the default retryer", func(t *testing.T) {
		srv, headers := openServer(t, func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"__type":"com.amazon.coral.service#InternalFailure","message":"x"}`))
		})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		t.Cleanup(cancel)
		_, err := Open(ctx, "dynamodb://us-east-1/?endpoint="+srv.URL, "", nil, numfmt.DecimalAuto)
		require.ErrorContains(t, err, "connect dynamodb")
		require.Len(t, headers(), 3)
	})
}
