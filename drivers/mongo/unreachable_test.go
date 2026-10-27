package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// lazyStore returns a Store whose client connected lazily to a port that
// nothing listens on. Each operation then fails at server selection.
func lazyStore(t *testing.T) *Store {
	t.Helper()
	opts := options.Client().ApplyURI("mongodb://127.0.0.1:1/iq?serverSelectionTimeoutMS=500")
	client, err := mongo.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return &Store{
		client:     client,
		db:         client.Database("iq"),
		collection: "books",
		pageSize:   scanBatch,
		decimal:    numfmt.DecimalAuto,
	}
}

// TestOperationsHideTopology runs each operation against an unreachable
// server. The error must keep the operation prefix and must not contain the
// topology description of the driver.
func TestOperationsHideTopology(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		run    func(ctx context.Context, s *Store) error
	}{
		{"get", "mongodb find: ", func(ctx context.Context, s *Store) error {
			_, err := s.Get(ctx, []string{"a"})
			return err
		}},
		{"scan", "mongodb find: ", func(ctx context.Context, s *Store) error {
			return s.ScanBatches(ctx, func(map[string]any) error { return nil })
		}},
		{"estimate count", "mongodb estimated count: ", func(ctx context.Context, s *Store) error {
			_, err := s.EstimateCount(ctx)
			return err
		}},
		{"raw command", "mongodb command: ", func(ctx context.Context, s *Store) error {
			_, err := s.Query(ctx, []string{`{"ping":1}`})
			return err
		}},
		{"upsert", "mongodb bulk write: ", func(ctx context.Context, s *Store) error {
			_, err := s.Put(ctx, []query.Record{{Key: "a", Value: map[string]any{"x": 1}}}, query.Upsert)
			return err
		}},
		{"insert only", "mongodb insert: ", func(ctx context.Context, s *Store) error {
			_, err := s.Put(ctx, []query.Record{{Key: "a", Value: map[string]any{"x": 1}}}, query.InsertOnly)
			return err
		}},
		{"delete", "mongodb delete: ", func(ctx context.Context, s *Store) error {
			_, err := s.Delete(ctx, []string{"a"})
			return err
		}},
		{"clear", "mongodb delete: ", func(ctx context.Context, s *Store) error {
			return s.Clear(ctx)
		}},
		{"drop", "mongodb drop: ", func(ctx context.Context, s *Store) error {
			return s.Drop(ctx)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := tt.run(ctx, lazyStore(t))

			require.ErrorContains(t, err, tt.prefix+"no reachable server: ")
			require.NotContains(t, err.Error(), "topology:")
		})
	}
}
