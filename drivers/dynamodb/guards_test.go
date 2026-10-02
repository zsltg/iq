package dynamodb

import (
	"context"
	"strconv"
	"testing"
	"time"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestRetryBoundIsEightAttempts(t *testing.T) {
	// The bound is part of the documented behavior, so the tests pin its value rather
	// than reading the constant.
	t.Run("get", func(t *testing.T) {
		fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
			return &awsdynamodb.BatchGetItemOutput{UnprocessedKeys: in.RequestItems}, nil
		}}
		s := fakeStore(t, fake)
		_, err := s.Get(context.Background(), []string{"1"})
		require.ErrorContains(t, err, "8 attempt(s)")
		require.Equal(t, 8, fake.batchGetCalls)
	})
	t.Run("clear", func(t *testing.T) {
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
		require.ErrorContains(t, err, "8 attempt(s)")
		require.Equal(t, 8, fake.batchWriteCalls)
	})
}

func TestBackoffDefaults(t *testing.T) {
	require.Equal(t, 20*time.Millisecond, backoffUnit)
}

func TestBackoffJitterIsHalfTheBase(t *testing.T) {
	prevUnit, prevRand := backoffUnit, randInt63n
	t.Cleanup(func() { backoffUnit, randInt63n = prevUnit, prevRand })
	backoffUnit = time.Millisecond
	var bound int64
	randInt63n = func(n int64) int64 {
		bound = n
		return 0
	}
	// Attempt 2 has a base of four units, so the jitter is drawn from [0, base/2].
	require.NoError(t, backoff(context.Background(), 2))
	require.Equal(t, int64(2*time.Millisecond)+1, bound)
}

func TestOpenRejectsBadInput(t *testing.T) {
	t.Run("a url with the wrong scheme", func(t *testing.T) {
		_, err := Open(context.Background(), "mysql://us-east-1/", "", nil, numfmt.DecimalAuto)
		require.ErrorContains(t, err, "must start with dynamodb://")
	})
	t.Run("a url that does not parse", func(t *testing.T) {
		_, err := parseURL("dynamodb://us-east-1/\x7f", "")
		require.ErrorContains(t, err, "parse dynamodb url")
	})
	t.Run("an unreadable aws config", func(t *testing.T) {
		t.Setenv("AWS_RETRY_MODE", "bogus")
		_, err := Open(context.Background(), "dynamodb://us-east-1/?endpoint=http://127.0.0.1:1", "", nil, numfmt.DecimalAuto)
		require.ErrorContains(t, err, "load aws config")
	})
}

func TestGetRejectsAMalformedKeyBeforeAnyRead(t *testing.T) {
	fake := &fakeDDB{}
	s := fakeStore(t, fake)
	_, err := s.Get(context.Background(), []string{"not-a-number"})
	require.ErrorContains(t, err, "is not a number")
	require.Zero(t, fake.batchGetCalls)
}

func TestQueryWrapsTheExecuteStatementError(t *testing.T) {
	fake := &fakeDDB{executeFn: func(*awsdynamodb.ExecuteStatementInput) (*awsdynamodb.ExecuteStatementOutput, error) {
		return nil, errBoom
	}}
	s := fakeStore(t, fake)
	_, err := s.Query(context.Background(), []string{"SELECT", "*", "FROM", "t"})
	require.ErrorContains(t, err, "dynamodb:")
	require.ErrorIs(t, err, errBoom)
}

func TestEstimateCountErrors(t *testing.T) {
	t.Run("no table selected", func(t *testing.T) {
		fake := &fakeDDB{}
		s := &Store{client: fake, table: "t"}
		_, err := s.EstimateCount(context.Background())
		require.ErrorIs(t, err, errNoTable)
	})
	t.Run("describe fails", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return nil, errBoom
		}}
		s := fakeStore(t, fake)
		_, err := s.EstimateCount(context.Background())
		require.ErrorContains(t, err, "dynamodb describe table")
		require.ErrorIs(t, err, errBoom)
	})
}

func TestInspectWrapsDescribeAndListErrors(t *testing.T) {
	t.Run("list tables", func(t *testing.T) {
		fake := &fakeDDB{listTablesFn: func(*awsdynamodb.ListTablesInput) (*awsdynamodb.ListTablesOutput, error) {
			return nil, errBoom
		}}
		s := &Store{client: fake, table: "t"}
		_, err := s.InspectTables(context.Background())
		require.ErrorContains(t, err, "dynamodb list tables")
		require.ErrorIs(t, err, errBoom)
	})
	t.Run("describe table", func(t *testing.T) {
		fake := &fakeDDB{describeFn: func(*awsdynamodb.DescribeTableInput) (*awsdynamodb.DescribeTableOutput, error) {
			return nil, errBoom
		}}
		s := &Store{client: fake, table: "t"}
		_, err := s.InspectTable(context.Background())
		require.ErrorContains(t, err, "dynamodb describe table")
		require.ErrorIs(t, err, errBoom)
	})
}

func TestOperationsNeedATable(t *testing.T) {
	// A store with no key schema has no table to act on, so each operation refuses
	// before it talks to the backend.
	tests := []struct {
		name string
		run  func(s *Store) error
	}{
		{"scan filtered", func(s *Store) error {
			return s.ScanFiltered(context.Background(), nil, func(map[string]any) error { return nil })
		}},
		{"put", func(s *Store) error {
			_, err := s.Put(context.Background(), nil, query.Upsert)
			return err
		}},
		{"clear", func(s *Store) error { return s.Clear(context.Background()) }},
		{"delete", func(s *Store) error {
			_, err := s.Delete(context.Background(), nil)
			return err
		}},
		{"drop", func(s *Store) error { return s.Drop(context.Background()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDDB{}
			err := tt.run(&Store{client: fake, table: "t"})
			require.ErrorIs(t, err, errNoTable)
			require.Zero(t, fake.scanCalls+fake.batchGetCalls+fake.batchWriteCalls+fake.putCalls+fake.deleteTableCalls)
		})
	}
}

func TestPutRejectsAnUnrepresentableValueBeforeAnyWrite(t *testing.T) {
	fake := &fakeDDB{}
	s := fakeStore(t, fake)
	_, err := s.Put(context.Background(),
		[]query.Record{{Key: "1", Value: map[string]any{"id": 1, "bad": struct{}{}}}}, query.Upsert)
	require.ErrorContains(t, err, "cannot write value")
	require.Zero(t, fake.putCalls+fake.batchGetCalls)
}

func TestExistingKeysKeepsReadingPastADuplicateAndAKeylessRecord(t *testing.T) {
	var asked []string
	fake := &fakeDDB{batchGetFn: func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		for _, k := range in.RequestItems["t"].Keys {
			asked = append(asked, k["id"].(*types.AttributeValueMemberN).Value)
		}
		return &awsdynamodb.BatchGetItemOutput{Responses: map[string][]map[string]types.AttributeValue{
			"t": {idItem(1), idItem(2), idItem(3)},
		}}, nil
	}}
	s := fakeStore(t, fake)
	stat, err := s.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "", Value: map[string]any{"id": 9}},
		{Key: "2", Value: map[string]any{"id": 2}},
		{Key: "3", Value: map[string]any{"id": 3}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, []string{"1", "2", "3"}, asked)
	require.Equal(t, query.WriteStat{Written: 1, Overwritten: 4}, stat)
}

func TestExistingKeySetReadsEveryChunk(t *testing.T) {
	keys := make([]string, 0, batchGetMax+50)
	for i := 1; i <= batchGetMax+50; i++ {
		keys = append(keys, strconv.Itoa(i))
	}
	var sizes []int
	fake := &fakeDDB{}
	fake.batchGetFn = func(in *awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		if fake.batchGetCalls > 3 {
			return nil, errBoom // a loop that does not advance must fail fast, not hang
		}
		sizes = append(sizes, len(in.RequestItems["t"].Keys))
		return &awsdynamodb.BatchGetItemOutput{}, nil
	}
	s := fakeStore(t, fake)
	_, err := s.existingKeySet(context.Background(), keys)
	require.NoError(t, err)
	require.Equal(t, []int{batchGetMax, 50}, sizes)
}

func TestDeleteReadsNothingForAMalformedKey(t *testing.T) {
	fake := &fakeDDB{}
	s := fakeStore(t, fake)
	_, err := s.Delete(context.Background(), []string{"not-a-number"})
	require.ErrorContains(t, err, "is not a number")
	require.Zero(t, fake.batchGetCalls+fake.batchWriteCalls)
}

func TestDeleteSurfacesThePreReadError(t *testing.T) {
	fake := &fakeDDB{batchGetFn: func(*awsdynamodb.BatchGetItemInput) (*awsdynamodb.BatchGetItemOutput, error) {
		return nil, errBoom
	}}
	s := fakeStore(t, fake)
	stat, err := s.Delete(context.Background(), []string{"1"})
	require.ErrorIs(t, err, errBoom)
	require.Equal(t, query.DeleteStat{}, stat)
	require.Zero(t, fake.batchWriteCalls) // nothing is deleted after a failed pre-read
}

func TestClearWrapsTheScanError(t *testing.T) {
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		return nil, errBoom
	}}
	s := fakeStore(t, fake)
	err := s.Clear(context.Background())
	require.ErrorContains(t, err, "dynamodb scan")
	require.ErrorIs(t, err, errBoom)
	require.Zero(t, fake.batchWriteCalls)
}

func TestKeyAndItemErrorsPropagate(t *testing.T) {
	composite := &Store{keys: []KeyAttr{
		{name: "pk", typ: types.ScalarAttributeTypeS},
		{name: "sk", typ: types.ScalarAttributeTypeN},
	}}
	single := &Store{keys: []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}}
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{"composite key part of the wrong type", func() error {
			_, err := composite.decodeKey(`["a","zz"]`)
			return err
		}, "is not a number"},
		{"item attribute with no representation", func() error {
			_, err := single.toItem(recordValue{key: "1", value: map[string]any{"bad": struct{}{}}})
			return err
		}, "cannot write value"},
		{"item key of the wrong type", func() error {
			_, err := single.toItem(recordValue{key: "zz", value: map[string]any{"title": "Go"}})
			return err
		}, "is not a number"},
		{"nested map value with no representation", func() error {
			_, err := toAttributeValue(map[string]any{"a": map[string]any{"b": struct{}{}}})
			return err
		}, "cannot write value"},
		{"nested list value with no representation", func() error {
			_, err := toAttributeValue([]any{[]any{struct{}{}}})
			return err
		}, "cannot write value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, tt.run(), tt.want)
		})
	}
}
