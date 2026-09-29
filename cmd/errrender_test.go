package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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

// redactCase is one input to the redaction tests. The redactor, log handler,
// error render and MCP tests use the same rows, so each surface proves the same
// guarantee.
type redactCase struct {
	name string
	err  error
	// secret is text that must not occur in any output. It is empty for a row
	// with no credential, and such a row must not change.
	secret string
	// want is the redacted message of err.
	want string
}

// parseURLErr returns the *url.Error that url.Parse gives for raw.
func parseURLErr(t *testing.T, raw string) error {
	t.Helper()
	_, err := url.Parse(raw)
	require.Error(t, err)
	return err
}

// redactCases returns one row for each branch of the redactor.
func redactCases(t *testing.T) []redactCase {
	t.Helper()
	const secret = "review-secret"
	return []redactCase{
		{
			name:   "quoted password in a message",
			err:    errors.New("dial redis://u:'review-secret'@host:bad/0 failed"),
			secret: secret,
			want:   "dial (unparseable URI) failed",
		},
		{
			name:   "parse error for a password with a space",
			err:    parseURLErr(t, "redis://u:x review-secret@host/0"),
			secret: secret,
			want:   `parse "(unparseable URI)": net/url: invalid userinfo`,
		},
		{
			name:   "parse error for a password with a double quote",
			err:    parseURLErr(t, `redis://u:a"review-secret@host/0`),
			secret: secret,
			want:   `parse "(unparseable URI)": net/url: invalid userinfo`,
		},
		{
			name:   "wrapped parse error",
			err:    fmt.Errorf("open source: %w", fmt.Errorf("parse redis url: %w", parseURLErr(t, "redis://u:a review-secret@host/0"))),
			secret: secret,
			want:   `open source: parse redis url: parse "(unparseable URI)": net/url: invalid userinfo`,
		},
		{
			name:   "parse error inside a joined error",
			err:    errors.Join(errors.New("first"), parseURLErr(t, "redis://u:x review-secret@host/0")),
			secret: secret,
			want:   "first\n" + `parse "(unparseable URI)": net/url: invalid userinfo`,
		},
		{
			name:   "parse error for a bad port",
			err:    parseURLErr(t, "redis://u:review-secret@host:bad/0"),
			secret: secret,
			want:   `parse "(unparseable URI)": invalid port ":bad" after host`,
		},
		{
			name:   "url inside single quotes",
			err:    errors.New("dial 'redis://u:review-secret@host/0' failed"),
			secret: secret,
			want:   "dial 'redis://u:xxxxx@host/0' failed",
		},
		{
			name:   "quoted url in a driver error",
			err:    errors.New(`connect "mongodb://review:review-secret@localhost:27017/db": context deadline exceeded`),
			secret: secret,
			want:   `connect "mongodb://review:xxxxx@localhost:27017/db": context deadline exceeded`,
		},
		{
			name:   "url in a callback error",
			err:    fmt.Errorf("scan batches: %w", errors.New("callback: dial redis://review:review-secret@localhost:6379/0")),
			secret: secret,
			want:   "scan batches: callback: dial redis://review:xxxxx@localhost:6379/0",
		},
		{
			name: "parseable url error keeps its location",
			err: &url.Error{
				Op:  "Get",
				URL: "https://u:review-secret@host/",
				Err: errors.New("dial tcp: connection refused"),
			},
			secret: secret,
			want:   `Get "https://u:xxxxx@host/": dial tcp: connection refused`,
		},
		{
			name:   "percent-escaped password in a message",
			err:    errors.New("dial redis://u:p%40ss%20w%22rd@host:6379/0 failed"),
			secret: "p%40ss",
			want:   "dial redis://u:xxxxx@host:6379/0 failed",
		},
		{
			name:   "percent-escaped password in a parse error for a bad port",
			err:    parseURLErr(t, "redis://u:p%40ss%20w%22rd@host:bad/0"),
			secret: "p%40ss",
			want:   `parse "(unparseable URI)": invalid port ":bad" after host`,
		},
		{
			name:   "open-store error for a URI with a bad port",
			err:    fmt.Errorf("open source %q: %w", "repro", parseURLErr(t, "redis://review:dummy-password@localhost:bad/0")),
			secret: "dummy-password",
			want:   `open source "repro": parse "(unparseable URI)": invalid port ":bad" after host`,
		},
		{
			name: "credential-free url before an email address",
			err:  errors.New("dial redis://localhost:6379/0 failed, mail ops@iq.dev"),
			want: "dial redis://localhost:6379/0 failed, mail ops@iq.dev",
		},
		{
			name: "file path that holds an at sign",
			err:  fmt.Errorf("open source: %w", errors.New("open file:///tmp/a@b/dump.json: no such file or directory")),
			want: "open source: open file:///tmp/a@b/dump.json: no such file or directory",
		},
	}
}

// TestRedactor checks the message and every cause frame of each row. A row with
// a credential must not show it anywhere. A row with no credential must not
// change.
func TestRedactor(t *testing.T) {
	for _, tc := range redactCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			redact := newRedactor(tc.err)
			got := redact(tc.err.Error())
			require.Equal(t, tc.want, got)
			for _, frame := range causeChain(tc.err) {
				out := redact(frame)
				if tc.secret == "" {
					require.Equal(t, frame, out)
					continue
				}
				require.NotContains(t, out, tc.secret)
			}
		})
	}
}

// TestRenderErrorRedactsEveryRow renders each row as text, as text with the
// stack, and as JSON with its causes. The complete output must not show a
// credential, and a row with no credential keeps its message.
func TestRenderErrorRedactsEveryRow(t *testing.T) {
	cfgs := []struct {
		name string
		cfg  *config
	}{
		{"text", &config{errorFormat: "text"}},
		{"text with stack", &config{errorFormat: "text", errorStack: true}},
		{"json", &config{errorFormat: "json"}},
	}
	for _, tc := range redactCases(t) {
		for _, c := range cfgs {
			t.Run(tc.name+"/"+c.name, func(t *testing.T) {
				var buf bytes.Buffer
				renderError(&buf, c.cfg, tc.err)
				out := buf.String()
				if tc.secret != "" {
					require.NotContains(t, out, tc.secret)
				}
				if c.cfg.errorFormat == "json" {
					var got errorJSON
					require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
					require.Equal(t, tc.want, got.Error.Message)
					return
				}
				require.True(t, strings.HasPrefix(out, "iq: "+tc.want+"\n"), out)
			})
		}
	}
}

func TestRedactorPatternStep(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"bare url masks the password",
			"dial redis://user:hunter2@host:6379 failed",
			"dial redis://user:xxxxx@host:6379 failed",
		},
		{
			"trailing comma is kept, password still masked",
			"contacted redis://user:hunter2@host:6379, giving up",
			"contacted redis://user:xxxxx@host:6379, giving up",
		},
		{
			"url in parentheses redacts cleanly",
			"error at (mongodb://user:hunter2@host:27017/db).",
			"error at (mongodb://user:xxxxx@host:27017/db).",
		},
		{
			"query params are preserved, password masked",
			"neo4j://neo4j:hunter2@host:7687/?key=id&label=Person timed out",
			"neo4j://neo4j:xxxxx@host:7687/?key=id&label=Person timed out",
		},
		{
			"a url inside single quotes keeps its location",
			"dial 'redis://user:hunter2@host:6379' failed",
			"dial 'redis://user:xxxxx@host:6379' failed",
		},
		{
			"a quoted password masks the whole url",
			"dial redis://user:'hunter2'@host:bad/0 failed",
			"dial (unparseable URI) failed",
		},
		{
			"a message with no url is unchanged",
			"authentication failed for user admin",
			"authentication failed for user admin",
		},
		{
			"a backticked url masks the password without a stray escape",
			"dialed `mongodb://user:hunter2@host:27017/db` twice",
			"dialed `mongodb://user:xxxxx@host:27017/db` twice",
		},
		{
			// The advice iq itself prints for an unknown source: a scheme literal
			// inside a code span, which the closing backtick used to make
			// unparseable, collapsing the URI to "(unparseable URI)" and swallowing
			// the backtick that closed the span.
			"our own scheme-literal advice survives intact",
			"unknown source \"nosuch\"; run `iq ls` (register a dump with `iq add file:///path/to/dump.json` to read one)",
			"unknown source \"nosuch\"; run `iq ls` (register a dump with `iq add file:///path/to/dump.json` to read one)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newRedactor(nil)(tt.in)
			require.Equal(t, tt.want, got)
			require.NotContains(t, got, "hunter2")
		})
	}
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
