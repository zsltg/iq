package couchdb

import (
	"context"
	"errors"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"
)

func TestReadFindCountsTheRowsItRead(t *testing.T) {
	// The count includes design documents, and a failure still reports how many rows
	// were read before it.
	boom := errors.New("connection reset")
	tests := []struct {
		name    string
		rows    *mockdb.Rows
		wantN   int
		wantErr string
	}{
		{
			name:  "all rows",
			rows:  allDocsRows("1", "_design/x", "2"),
			wantN: 3,
		},
		{
			name:    "a document that does not decode",
			rows:    allDocsRows("1", "2").AddRow(docRow("3", `[1]`)),
			wantN:   3,
			wantErr: "decode couchdb document",
		},
		{
			name:    "an iteration failure",
			rows:    allDocsRows("1", "2").AddRowError(boom),
			wantN:   2,
			wantErr: "couchdb find",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			mdb.ExpectFind().WillReturn(tt.rows)
			rows := st.client.DB(st.db).Find(context.Background(), map[string]any{})
			defer func() { _ = rows.Close() }()

			added := 0
			n, err := st.readFind(rows, func(map[string]any) { added++ })

			require.Equal(t, tt.wantN, n)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.Equal(t, 3, added)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestDeleteChunksReturnsZeroOnAFailure(t *testing.T) {
	// A failure returns a zero count, even after an earlier chunk was accepted.
	conflict := errors.New("conflict")
	tests := []struct {
		name    string
		script  [][]driver.BulkResult
		failAt  int
		wantErr string
	}{
		{"request failure on the first chunk", nil, 1, "couchdb bulk delete"},
		{"request failure on a later chunk", nil, 2, "couchdb bulk delete"},
		{
			name:    "per-document failure on the first chunk",
			script:  [][]driver.BulkResult{{{ID: "k1", Error: conflict}}},
			wantErr: `couchdb delete "k1"`,
		},
		{
			name: "per-document failure after an accepted chunk",
			script: [][]driver.BulkResult{
				{{ID: "k1"}, {ID: "k2"}},
				{{ID: "k3", Error: conflict}},
			},
			wantErr: `couchdb delete "k3"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 2, 1)
			rec := &bulkRecorder{t: t, results: tt.script, failAt: tt.failAt}
			for range 3 {
				mdb.ExpectBulkDocs().WillExecute(rec.answer)
			}
			docs := []any{
				map[string]any{"_id": "k1"}, map[string]any{"_id": "k2"}, map[string]any{"_id": "k3"},
			}

			n, err := st.deleteChunks(keyedCtx(), st.client.DB(st.db), docs)

			require.ErrorContains(t, err, tt.wantErr)
			require.Zero(t, n)
		})
	}
}

func TestDeleteChunksCountsAcceptedTombstones(t *testing.T) {
	st, _, mdb := newMockStore(t, 2, 1)
	rec := &bulkRecorder{t: t}
	mdb.ExpectBulkDocs().WillExecute(rec.answer)
	mdb.ExpectBulkDocs().WillExecute(rec.answer)
	docs := []any{map[string]any{"_id": "k1"}, map[string]any{"_id": "k2"}, map[string]any{"_id": "k3"}}

	n, err := st.deleteChunks(keyedCtx(), st.client.DB(st.db), docs)

	require.NoError(t, err)
	require.Equal(t, 3, n)
}
