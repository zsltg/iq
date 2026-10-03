package cmd

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// budgetWriter accepts a fixed number of writes, then fails every one.
type budgetWriter struct {
	left int
	buf  bytes.Buffer
}

var errBudget = errors.New("write budget spent")

func (w *budgetWriter) Write(p []byte) (int, error) {
	if w.left <= 0 {
		return 0, errBudget
	}
	w.left--
	return w.buf.Write(p)
}

func TestInGroup(t *testing.T) {
	list := []iqconfig.Handle{{Name: "x"}, {Name: "g"}, {Name: "g/a"}, {Name: "gx"}}

	got := inGroup(list, "g")

	require.Equal(t, []iqconfig.Handle{{Name: "g"}, {Name: "g/a"}}, got)
	require.Equal(t, []iqconfig.Handle{{Name: "x"}, {Name: "g"}, {Name: "g/a"}, {Name: "gx"}}, list, "the input list stays unchanged")
}

func TestWriteNoSourcesReturnsWriteError(t *testing.T) {
	err := writeNoSources(&budgetWriter{}, "g")

	require.ErrorIs(t, err, errBudget)
}

func TestFileFormatIgnoresOtherDrivers(t *testing.T) {
	path := writeJSONL(t, `{"key":"1","value":{"a":1}}`+"\n")

	require.NotEmpty(t, fileFormat("file", iqfile.URL(path)), "a file source reports its format")
	require.Empty(t, fileFormat("redis", iqfile.URL(path)), "another driver never reports one")
}

func TestGroupCellsColorsTheActiveGroup(t *testing.T) {
	cf := newSeed()

	active := groupCells("g", true, cf, false)
	idle := groupCells("g", false, cf, false)

	require.Same(t, pal.active, active[0].c)
	require.Nil(t, idle[0].c)
}

func TestRenderInspectResultsReturnsWriteErrors(t *testing.T) {
	one := []inspectResult{{sub: "one", value: 1}}
	tests := []struct {
		name    string
		budget  int
		results []inspectResult
	}{
		{"the header write fails", 0, nil},
		{"a result write fails", 1, one},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := inspectRequest{out: &budgetWriter{left: tt.budget}, st: &fakeStore{}, cfg: &config{}}

			err := renderInspectResults(req, tt.results)

			require.ErrorIs(t, err, errBudget)
		})
	}
}
