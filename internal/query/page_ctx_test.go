package query_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// pageProbePutter records the context and the slice capacity of every batch it gets.
type pageProbePutter struct {
	markers []any
	caps    []int
}

func (p *pageProbePutter) Put(ctx context.Context, batch []query.Record, _ query.WriteMode) (query.WriteStat, error) {
	p.markers = append(p.markers, ctx.Value(ctxMarker{}))
	p.caps = append(p.caps, cap(batch))
	return query.WriteStat{Written: len(batch)}, nil
}

func copyProbe(t *testing.T, pageSize, n int) *pageProbePutter {
	t.Helper()
	dst := &pageProbePutter{}
	src := func(_ context.Context, fn func([]query.Record) error) error {
		batch := make([]query.Record, n)
		for i := range batch {
			batch[i] = query.Record{Key: "k", Type: "string", Value: "v"}
		}
		return fn(batch)
	}
	c := &query.Copier{Dst: dst, PageSize: pageSize}
	ctx := context.WithValue(context.Background(), ctxMarker{}, "marker")
	_, err := c.Copy(ctx, src, false)
	require.NoError(t, err)
	return dst
}

// TestCopyGivesTheCallerContextToEveryPut copies five records in pages of two. The
// first two Puts come from a page that fills up, the last one from the final flush,
// and each of them must see the caller's context.
func TestCopyGivesTheCallerContextToEveryPut(t *testing.T) {
	dst := copyProbe(t, 2, 5)
	require.Equal(t, []any{"marker", "marker", "marker"}, dst.markers)
}

// TestCopyBuffersAPageInOneAllocation uses a page size that is not a power of two.
// A buffer that grows by append ends with a larger capacity than the page size.
func TestCopyBuffersAPageInOneAllocation(t *testing.T) {
	dst := copyProbe(t, 5, 5)
	require.Equal(t, []int{5}, dst.caps)
}

// TestYAMLSourceBuffersAPageInOneAllocation checks the capacity of the first page.
func TestYAMLSourceBuffersAPageInOneAllocation(t *testing.T) {
	var caps []int
	src := query.YAMLSource(strings.NewReader("a: 1\n---\na: 2\n---\na: 3\n---\na: 4\n---\na: 5\n"), 5, true)
	err := src(context.Background(), func(b []query.Record) error {
		caps = append(caps, cap(b))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int{5}, caps)
}
