package hbase

import (
	"context"
	"errors"
	"fmt"

	"github.com/tsuna/gohbase/hrpc"

	"github.com/zsltg/iq/internal/query"
)

// ErrRowTooLarge reports a row with more cells in one column family than the
// limit, read without the unbounded opt-in.
var ErrRowTooLarge = errors.New("too many cells")

// readOptions returns the options of a Get or Scan request. A request reads only
// the latest version of each cell. Unless ctx is unbounded, it also asks the server
// for at most one cell more than the limit in each column family of a row. The
// extra cell lets admitRow tell a row at the limit from a row over it, and the
// server never builds a larger reply.
func (s *Store) readOptions(ctx context.Context) []func(hrpc.Call) error {
	opts := []func(hrpc.Call) error{hrpc.MaxVersions(1)}
	if query.Unbounded(ctx) {
		return opts
	}
	return append(opts, hrpc.MaxResultsPerColumnFamily(s.maxRowCells+1))
}

// admitRow refuses a row that has more cells in one column family than the limit,
// unless ctx is unbounded. The error names the row key, never a value.
func (s *Store) admitRow(ctx context.Context, key string, cells []*hrpc.Cell) error {
	if query.Unbounded(ctx) {
		return nil
	}
	count := map[string]uint32{}
	for _, c := range cells {
		family := string(c.Family)
		count[family]++
		if count[family] > s.maxRowCells {
			return fmt.Errorf("read row %q: %w: a column family has more than %d cells, so read it with --unbounded",
				key, ErrRowTooLarge, s.maxRowCells)
		}
	}
	return nil
}

// readRow checks the cells of the row key against the limit and decodes them.
func (s *Store) readRow(ctx context.Context, key string, cells []*hrpc.Cell) (map[string]any, error) {
	if err := s.admitRow(ctx, key, cells); err != nil {
		return nil, err
	}
	return s.keyedRow(ctx, key, cells), nil
}
