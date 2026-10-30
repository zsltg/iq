package couchdb

import (
	"context"
	"io"
	"strconv"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// countingRows serves n document rows and counts the reads and the closes. kivik may close the
// driver rows more than once, so the tests check only that a close happened.
type countingRows struct {
	n      int
	reads  int
	closed int
}

func (c *countingRows) Next(row *driver.Row) error {
	if c.reads >= c.n {
		return io.EOF
	}
	c.reads++
	id := strconv.Itoa(c.reads)
	*row = *docRow(id, `{"_id":"`+id+`","a":1}`)
	return nil
}

func (c *countingRows) Close() error {
	c.closed++
	return nil
}

func (c *countingRows) UpdateSeq() string { return "" }

func (c *countingRows) Offset() int64 { return 0 }

func (c *countingRows) TotalRows() int64 { return int64(c.n) }

func TestQueryRowLimit(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		wantDocs  int
		wantReads int
	}{
		{"no limit reads all", 0, 5, 5},
		{"limit stops early", 2, 2, 2},
		{"limit above the result", 9, 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			rows := &countingRows{n: 5}
			mdb.ExpectFind().WillExecute(func(context.Context, any, driver.Options) (driver.Rows, error) {
				return rows, nil
			})
			ctx := query.WithRowLimit(context.Background(), tt.limit)

			res, err := st.Query(ctx, []string{`{"a":1}`})

			require.NoError(t, err)
			out, ok := res.(map[string]any)
			require.True(t, ok)
			require.Len(t, out["docs"], tt.wantDocs)
			require.Equal(t, tt.wantReads, rows.reads)
			require.Positive(t, rows.closed)
		})
	}
}
