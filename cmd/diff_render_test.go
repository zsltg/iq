package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fatih/color"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
)

func TestReportRenderHuman(t *testing.T) {
	rep := report{
		Data: []diff.ItemDelta{
			{Key: "gone", Op: diff.OpRemove, Old: map[string]any{"v": 1}},
			{Key: "keep", Op: diff.OpChange, Changes: []diff.Change{{Path: []string{"v"}, Op: diff.OpChange, Old: 1, New: 9}}},
			{Key: "new", Op: diff.OpAdd, New: map[string]any{"v": 3}},
		},
		Schema:    []diff.Change{{Path: []string{".email"}, Op: diff.OpAdd, New: map[string]any{"types": []any{"string"}}}},
		dataRun:   true,
		schemaRun: true,
	}
	left := sourceSpec{handle: "a", driver: "redis"}
	right := sourceSpec{handle: "b", driver: "redis"}
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, left, right, diffModes{}))
	out := buf.String()

	require.Contains(t, out, "a (redis)  →  b (redis)")
	require.Contains(t, out, "# data")
	require.Contains(t, out, "- gone")
	require.Contains(t, out, "~ keep")
	require.Contains(t, out, "    ~ v: 1 → 9")
	require.Contains(t, out, "+ new")
	require.Contains(t, out, "1 added, 1 removed, 1 changed")
	require.Contains(t, out, "# schema")
	require.Contains(t, out, "+ .email")
}

// coloredDiffReport builds a report exercising every colored op across a
// tree-diff layer (stats) and a data layer: an add, a remove, a tree change,
// plus a data-mode item add and an item change carrying a nested field change.
func coloredDiffReport() report {
	return report{
		Data: []diff.ItemDelta{
			{Key: "gone", Op: diff.OpRemove, Old: map[string]any{"v": 1}},
			{Key: "keep", Op: diff.OpChange, Changes: []diff.Change{{Path: []string{"v"}, Op: diff.OpChange, Old: 1, New: 9}}},
			{Key: "new", Op: diff.OpAdd, New: map[string]any{"v": 3}},
		},
		Stats: []diff.Change{
			{Path: []string{"mem", "used"}, Op: diff.OpChange, Old: 1, New: 2},
			{Path: []string{"mem", "gone"}, Op: diff.OpRemove, Old: 5},
			{Path: []string{"mem", "add"}, Op: diff.OpAdd, New: 7},
		},
		dataRun:  true,
		statsRun: true,
	}
}

// TestReportRenderColoredStripsToPlain proves the colored human report reduces
// byte-for-byte to the uncolored one across every op — an add, a remove, and a
// change in a tree diff, plus a data-mode item add and an item change with a
// nested field change — and that color off emits zero escapes.
func TestReportRenderColoredStripsToPlain(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	t.Cleanup(func() { color.NoColor = orig })

	rep := coloredDiffReport()
	left := sourceSpec{handle: "a", driver: "redis"}
	right := sourceSpec{handle: "b", driver: "redis"}

	color.NoColor = true
	var plainBuf bytes.Buffer
	require.NoError(t, rep.render(&plainBuf, left, right, diffModes{}))
	plain := plainBuf.String()
	require.NotContains(t, plain, "\x1b[", "color off must emit no escapes")

	color.NoColor = false
	var colorBuf bytes.Buffer
	require.NoError(t, rep.render(&colorBuf, left, right, diffModes{}))
	got := colorBuf.String()
	require.Contains(t, got, "\x1b[", "colored report must carry ANSI escapes")
	require.Equal(t, plain, stripANSI(got), "stripANSI must equal the plain rendering")
}

// TestReportRenderColoredRoles pins the palette role each op carries: add rows
// green, remove rows red, the change symbol+path yellow with the old value red,
// the new value green, and the arrow left plain.
func TestReportRenderColoredRoles(t *testing.T) {
	// Not parallel: flips the global color mode.
	orig := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = orig })

	rep := coloredDiffReport()
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, sourceSpec{handle: "a"}, sourceSpec{handle: "b"}, diffModes{}))
	got := buf.String()

	// Raw SGR codes for each role.
	require.Contains(t, got, "\x1b[32m", "green add code present")
	require.Contains(t, got, "\x1b[31m", "red remove code present")
	require.Contains(t, got, "\x1b[33m", "yellow change code present")

	// Whole-line add and remove rows in one color wrap.
	require.Contains(t, got, pal.add.Sprint("+ mem.add  7"), "tree add line green")
	require.Contains(t, got, pal.remove.Sprint("- mem.gone  5"), "tree remove line red")

	// A tree change line: symbol+path yellow, old red, new green, arrow plain.
	require.Contains(t, got,
		pal.change.Sprintf("%s %s", "~", "mem.used")+": "+pal.remove.Sprint("1")+" → "+pal.add.Sprint("2"),
		"change line pins yellow path, red old, green new, plain arrow")

	// Data-mode item add colored across the line; the change item header yellow.
	require.Contains(t, got, pal.add.Sprint(`+ new  {"v":3}`), "data item add line green")
	require.Contains(t, got, pal.change.Sprint("~ keep"), "data item change header yellow")
}

func TestReportRenderNoDifferences(t *testing.T) {
	rep := report{Data: []diff.ItemDelta{}, dataRun: true}
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, sourceSpec{handle: "a"}, sourceSpec{handle: "b"}, diffModes{}))
	require.Contains(t, buf.String(), "no differences")
}

func TestReportRenderJSON(t *testing.T) {
	rep := report{
		Data:    []diff.ItemDelta{{Key: "new", Op: diff.OpAdd, New: map[string]any{"v": 3}}},
		dataRun: true,
	}
	var buf bytes.Buffer
	require.NoError(t, rep.render(&buf, sourceSpec{handle: "a"}, sourceSpec{handle: "b"}, diffModes{json: true}))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	data := got["data"].([]any)
	require.Len(t, data, 1)
	first := data[0].(map[string]any)
	require.Equal(t, "new", first["key"])
	require.Equal(t, "add", first["op"]) // Op marshals as its name, not an integer
}

// errAfter is an io.Writer that succeeds n times, then fails every write. It
// drives the error-propagation paths of the render helpers.
type errAfter struct{ n int }

func (w *errAfter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, fmt.Errorf("write failed")
	}
	w.n--
	return len(p), nil
}

// failAt is an io.Writer that fails on exactly the at-th write (1-indexed) and
// succeeds on every other. It isolates a single error return: if a helper wrongly
// swallowed that error and continued, the later writes still succeed, so the
// missing propagation surfaces as a nil result.
type failAt struct{ at, n int }

func (w *failAt) Write(p []byte) (int, error) {
	w.n++
	if w.n == w.at {
		return 0, fmt.Errorf("write failed")
	}
	return len(p), nil
}

func TestRenderPropagatesWriteErrors(t *testing.T) {
	change := diff.Change{Path: []string{"v"}, Op: diff.OpChange, Old: 1, New: 2}
	add := diff.Change{Path: []string{"v"}, Op: diff.OpAdd, New: 2}
	item := diff.ItemDelta{Key: "k", Op: diff.OpChange, Changes: []diff.Change{change}}
	addItem := diff.ItemDelta{Key: "k", Op: diff.OpAdd, New: 1}

	t.Run("renderChangeLine change", func(t *testing.T) {
		require.Error(t, renderChangeLine(&errAfter{0}, "", change))
	})
	t.Run("renderChangeLine add", func(t *testing.T) {
		require.Error(t, renderChangeLine(&errAfter{0}, "", add))
	})
	t.Run("renderChanges heading", func(t *testing.T) {
		require.Error(t, renderChanges(&errAfter{0}, "t", []diff.Change{change}))
	})
	t.Run("renderChanges body", func(t *testing.T) {
		require.Error(t, renderChanges(&errAfter{1}, "t", []diff.Change{change}))
	})
	t.Run("renderChanges body error is returned not swallowed", func(t *testing.T) {
		// Fail only the change-line write (2nd): heading and summary succeed, so a
		// helper that dropped the renderChangeLine error would return nil.
		require.Error(t, renderChanges(&failAt{at: 2}, "t", []diff.Change{change}))
	})
	t.Run("renderItems change-line error is returned not swallowed", func(t *testing.T) {
		// Writes: heading(1), item key line(2), change line(3), summary(4). Fail the
		// change line only.
		require.Error(t, renderItems(&failAt{at: 3}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems change-header error is returned not swallowed", func(t *testing.T) {
		// Fail only the item key line (2nd): the change line and summary succeed, so
		// a helper that dropped the header error would return nil.
		require.Error(t, renderItems(&failAt{at: 2}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems add-row error is returned not swallowed", func(t *testing.T) {
		// Writes: heading(1), add row(2), summary(3). Fail the add row only.
		require.Error(t, renderItems(&failAt{at: 2}, "t", []diff.ItemDelta{addItem}))
	})
	t.Run("renderItems heading", func(t *testing.T) {
		require.Error(t, renderItems(&errAfter{0}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems change body", func(t *testing.T) {
		require.Error(t, renderItems(&errAfter{1}, "t", []diff.ItemDelta{item}))
	})
	t.Run("renderItems add body", func(t *testing.T) {
		require.Error(t, renderItems(&errAfter{1}, "t", []diff.ItemDelta{addItem}))
	})
	t.Run("writeSummary empty", func(t *testing.T) {
		require.Error(t, writeSummary(&errAfter{0}, diff.Summary{}))
	})
	t.Run("writeSummary counts", func(t *testing.T) {
		require.Error(t, writeSummary(&errAfter{0}, diff.Summary{Added: 1}))
	})
	t.Run("render header", func(t *testing.T) {
		rep := report{dataRun: true}
		require.Error(t, rep.render(&errAfter{0}, sourceSpec{}, sourceSpec{}, diffModes{}))
	})
	t.Run("render data section", func(t *testing.T) {
		rep := report{Data: []diff.ItemDelta{item}, dataRun: true}
		require.Error(t, rep.render(&errAfter{1}, sourceSpec{}, sourceSpec{}, diffModes{}))
	})
	t.Run("render stats section", func(t *testing.T) {
		rep := report{Stats: []diff.Change{change}, statsRun: true}
		require.Error(t, rep.render(&errAfter{1}, sourceSpec{}, sourceSpec{}, diffModes{}))
	})
	t.Run("render schema section", func(t *testing.T) {
		rep := report{Schema: []diff.Change{change}, schemaRun: true}
		require.Error(t, rep.render(&errAfter{1}, sourceSpec{}, sourceSpec{}, diffModes{}))
	})
}
