package couchbase

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeRows is a pageRows that returns fixed JSON rows and can fail at one step. It
// records Close, so a test can prove that readPage releases the result on every path.
type fakeRows struct {
	rows      []string
	next      int
	rowErr    error
	streamErr error
	closed    bool
}

func (f *fakeRows) Next() bool {
	if f.next >= len(f.rows) {
		return false
	}
	f.next++
	return true
}

func (f *fakeRows) Row(valuePtr any) error {
	if f.rowErr != nil {
		return f.rowErr
	}
	return json.Unmarshal([]byte(f.rows[f.next-1]), valuePtr)
}

func (f *fakeRows) Err() error { return f.streamErr }

func (f *fakeRows) Close() error {
	f.closed = true
	return nil
}

// TestReadPage drives the scan page reader without a cluster. A live cluster cannot make
// the statement that scan builds fail during the row stream, so these paths are
// reachable only through a fake result.
func TestReadPage(t *testing.T) {
	errRow := errors.New("row decode failed")
	errStream := errors.New("stream failed")
	tests := []struct {
		name     string
		rows     *fakeRows
		wantKeys []string
		wantLast string
		wantN    int
		wantErr  error
	}{
		{
			name: "reads every row and returns the last id",
			rows: &fakeRows{rows: []string{
				`{"k":"a","v":{"n":1}}`,
				`{"k":"b","v":{"n":2}}`,
			}},
			wantKeys: []string{"a", "b"},
			wantLast: "b",
			wantN:    2,
		},
		{
			name:    "a row that does not decode fails the page",
			rows:    &fakeRows{rows: []string{`{"k":"a","v":{}}`}, rowErr: errRow},
			wantErr: errRow,
		},
		{
			name:    "a failure in the streamed payload fails the page",
			rows:    &fakeRows{rows: []string{`{"k":"a","v":{}}`}, streamErr: errStream},
			wantErr: errStream,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &Store{pageSize: 10}
			page, last, n, err := st.readPage(tt.rows, nil)
			require.True(t, tt.rows.closed, "readPage must close the result on every path")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorContains(t, err, "couchbase scan")
				require.Nil(t, page, "a failed page must not hand back partial rows")
				require.Empty(t, last, "a failed page must not move the cursor")
				require.Zero(t, n, "a failed page must not count rows")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantKeys, slices.Sorted(maps.Keys(page)))
			require.Equal(t, tt.wantLast, last)
			require.Equal(t, tt.wantN, n)
		})
	}
}
