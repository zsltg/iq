package couchdb

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"
)

// allDocsRows builds one document row for each id.
func allDocsRows(ids ...string) *mockdb.Rows {
	rows := mockdb.NewRows()
	for _, id := range ids {
		rows.AddRow(docRow(id, fmt.Sprintf(`{"_id":%q}`, id)))
	}
	return rows
}

// expectAllDocsWithCtx answers one _all_docs request with rows and checks the context.
func expectAllDocsWithCtx(t *testing.T, mdb *mockdb.DB, rows *mockdb.Rows) {
	t.Helper()
	mdb.ExpectAllDocs().WillExecute(func(ctx context.Context, _ driver.Options) (driver.Rows, error) {
		requireKeyed(t, ctx)
		return rows.Final(), nil
	})
}

func TestScanBatchesPageBoundaries(t *testing.T) {
	// Pages fill to pageSize and the last one is the remainder. A design document
	// takes no slot. An exact multiple leaves no empty page.
	tests := []struct {
		name string
		ids  []string
		want []int
	}{
		{"remainder", []string{"1", "2", "3", "4", "5"}, []int{2, 2, 1}},
		{"exact multiple", []string{"1", "2", "3", "4"}, []int{2, 2}},
		{"design document takes no slot", []string{"1", "_design/x", "2", "3", "4"}, []int{2, 2}},
		{"only a design document", []string{"_design/x"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 2, 1)
			expectAllDocsWithCtx(t, mdb, allDocsRows(tt.ids...))

			var sizes []int
			require.NoError(t, st.ScanBatches(keyedCtx(), func(batch map[string]any) error {
				require.NotEmpty(t, batch)
				sizes = append(sizes, len(batch))
				return nil
			}))

			require.Equal(t, tt.want, sizes)
		})
	}
}
