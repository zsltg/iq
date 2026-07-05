package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"
)

// parseErr returns the *gojq.ParseError produced by parsing a malformed filter,
// wrapped the way the query core wraps it.
func parseErr(t *testing.T, filter string) error {
	t.Helper()
	_, err := gojq.Parse(filter)
	require.Error(t, err)
	return fmt.Errorf("parse expression: %w", err)
}

func TestRenderErrorText(t *testing.T) {
	var buf bytes.Buffer
	renderError(&buf, &config{errorFormat: "text"}, errors.New("boom"))
	require.Equal(t, "iq: boom\n", buf.String())
}

// TestRenderErrorUnknownFormatFallsBackToText guards the bootstrap path where a
// flag-parse error leaves errorFormat at an unresolved value.
func TestRenderErrorUnknownFormatFallsBackToText(t *testing.T) {
	var buf bytes.Buffer
	renderError(&buf, &config{errorFormat: "xml"}, errors.New("boom"))
	require.Equal(t, "iq: boom\n", buf.String())
}

func TestRenderErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	renderError(&buf, &config{errorFormat: "json"}, errors.New("boom"))

	var got errorJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Equal(t, "boom", got.Error.Message)
	require.Empty(t, got.Error.Causes, "a single-frame error needs no cause breakdown")
}

func TestRenderErrorJSONIncludesCauses(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("combine: %w", fmt.Errorf("open source: %w", errors.New("refused")))
	renderError(&buf, &config{errorFormat: "json"}, err)

	var got errorJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Equal(t, []string{"combine", "open source", "refused"}, got.Error.Causes)
}

func TestRenderErrorStackText(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("combine: %w", fmt.Errorf("open source: %w", errors.New("refused")))
	renderError(&buf, &config{errorFormat: "text", errorStack: true}, err)

	out := buf.String()
	require.Contains(t, out, "iq: combine: open source: refused")
	require.Contains(t, out, "  - combine")
	require.Contains(t, out, "  - open source")
	require.Contains(t, out, "  - refused")
}

// TestRenderErrorStackSingleFrame checks a leaf error prints no redundant stack
// line (the single frame equals the message already shown).
func TestRenderErrorStackSingleFrame(t *testing.T) {
	var buf bytes.Buffer
	renderError(&buf, &config{errorFormat: "text", errorStack: true}, errors.New("boom"))
	require.Equal(t, "iq: boom\n", buf.String())
}

// TestRenderErrorRedactsCredentials checks a wrapped URL-bearing error is
// scrubbed before printing, in both the message and the stack.
func TestRenderErrorRedactsCredentials(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("open source: %w", errors.New("dial redis://user:hunter2@host:6379"))
	renderError(&buf, &config{errorFormat: "text", errorStack: true}, err)

	out := buf.String()
	require.NotContains(t, out, "hunter2")
	require.Contains(t, out, "xxxxx")
}

func TestAsSyntaxError(t *testing.T) {
	t.Run("enriches a parse error", func(t *testing.T) {
		err := asSyntaxError("a |", parseErr(t, "a |"))
		var se *filterSyntaxError
		require.ErrorAs(t, err, &se)
		require.Equal(t, "a |", se.filter)
		require.Positive(t, se.offset)
	})

	t.Run("passes a non-parse error through", func(t *testing.T) {
		plain := errors.New("not a parse error")
		require.Equal(t, plain, asSyntaxError("a", plain))
	})

	t.Run("nil stays nil", func(t *testing.T) {
		require.NoError(t, asSyntaxError("a", nil))
	})
}

// TestRenderErrorSyntaxSpan checks the caret report shows under --error.format.
// text.verbose and is suppressed when it is off.
func TestRenderErrorSyntaxSpan(t *testing.T) {
	err := asSyntaxError(".a | @", parseErr(t, ".a | @"))

	var on bytes.Buffer
	renderError(&on, &config{errorFormat: "text", errorTextVerbose: true}, err)
	lines := strings.Split(strings.TrimRight(on.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, "    .a | @", lines[1])
	require.Equal(t, "         ^", lines[2]) // caret under the offending "@"

	var off bytes.Buffer
	renderError(&off, &config{errorFormat: "text", errorTextVerbose: false}, err)
	require.NotContains(t, off.String(), "^")
}

// TestRenderErrorSyntaxJSON checks a syntax error carries its offset/token in
// json regardless of the verbose text flag.
func TestRenderErrorSyntaxJSON(t *testing.T) {
	err := asSyntaxError(".a | @", parseErr(t, ".a | @"))
	var buf bytes.Buffer
	renderError(&buf, &config{errorFormat: "json"}, err)

	var got errorJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.NotNil(t, got.Error.Offset)
	require.Equal(t, "@", got.Error.Token)
}

// TestSyntaxReportCaretClamp exercises the caret column math directly, including
// the underflow clamp (a token wider than the offset pins the caret to column 0).
func TestSyntaxReportCaretClamp(t *testing.T) {
	// offset 4, token "foo" (len 3) -> caret at column 1.
	mid := (&filterSyntaxError{filter: ".x foo", offset: 4, token: "foo"}).report()
	require.Equal(t, "    .x foo\n     ^\n", mid)

	// offset 1, token "long" (len 4) -> col would be -3, clamped to 0.
	start := (&filterSyntaxError{filter: "abc", offset: 1, token: "long"}).report()
	require.Equal(t, "    abc\n    ^\n", start)
}

func TestCauseChain(t *testing.T) {
	err := fmt.Errorf("a: %w", fmt.Errorf("b: %w", errors.New("c")))
	require.Equal(t, []string{"a", "b", "c"}, causeChain(err))
	require.Equal(t, []string{"solo"}, causeChain(errors.New("solo")))
}
