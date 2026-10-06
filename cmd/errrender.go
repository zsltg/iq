package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
)

// filterSyntaxError enriches a gojq parse error with the filter text the CLI
// boundary owns, so the renderer can draw a caret under the offending token when
// --error.format.text.verbose is on. The query core returns only gojq's
// *ParseError (the filter string is a CLI concern); runJQ/runCombine splice it in
// here. It is transparent to errors.Is/As via Unwrap.
type filterSyntaxError struct {
	filter string
	offset int
	token  string
	err    error
}

// Error returns the wrapped chain's message unchanged, so text output and log
// records read exactly as before the enrichment.
func (e *filterSyntaxError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped chain so errors.Is/As see through the enrichment.
func (e *filterSyntaxError) Unwrap() error { return e.err }

// asSyntaxError wraps err into a filterSyntaxError when its chain carries a gojq
// *ParseError, recording where in filter the parse failed. It returns err
// unchanged when there is no parse error to enrich, so non-syntax errors pass
// through untouched.
func asSyntaxError(filter string, err error) error {
	if err == nil {
		return nil
	}
	var pe *gojq.ParseError
	if !errors.As(err, &pe) {
		return err
	}
	return &filterSyntaxError{filter: filter, offset: pe.Offset, token: pe.Token, err: err}
}

// report renders the two-line caret span for a syntax error: the filter, then a
// caret under the offending token. gojq's Offset counts bytes read when the
// error fired (just past the token), so the caret is placed at the token's
// start. The filter is treated as a single line (CLI filters almost always are);
// a multi-line filter still renders, with the caret measured from the start.
func (e *filterSyntaxError) report() string {
	// offset counts bytes read up to the error, so col = offset - len(token) is
	// the token's start and never exceeds len(filter); it can only underflow if a
	// token is reported at offset 0, which max clamps to column 0.
	col := max(0, e.offset-len(e.token))
	var b strings.Builder
	b.WriteString("    ")
	b.WriteString(e.filter)
	b.WriteByte('\n')
	b.WriteString("    ")
	b.WriteString(strings.Repeat(" ", col))
	b.WriteString("^\n")
	return b.String()
}

// renderError writes err to w in the format cfg selects. text (the default and
// the fallback for any unresolved format) preserves the historical `iq: <err>`
// line; json emits a machine-readable object. It is a no-op on a nil error.
func renderError(w io.Writer, cfg *config, err error) {
	if err == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(cfg.errorFormat), "json") {
		renderErrorJSON(w, err)
		return
	}
	renderErrorText(w, cfg, err)
}

// renderErrorText writes the `iq: <message>` line, then (for a jq syntax error
// with --error.format.text.verbose) a caret span, then (with --error.stack) the
// wrapped cause chain. Every printed string is redacted so a wrapped driver
// error cannot leak a credential.
func renderErrorText(w io.Writer, cfg *config, err error) {
	redact := newRedactor(err)
	_, _ = fmt.Fprintln(w, "iq: "+redact(err.Error()))
	var se *filterSyntaxError
	if cfg.errorTextVerbose && errors.As(err, &se) {
		_, _ = fmt.Fprint(w, se.report())
	}
	if cfg.errorStack {
		for _, frame := range stackFrames(err) {
			_, _ = fmt.Fprintln(w, "  - "+redact(frame))
		}
	}
}

// errorJSON is the shape of a rendered error in --error.format json.
type errorJSON struct {
	Error errorBody `json:"error"`
}

// errorBody carries the message and, for a syntax error, the parse position; the
// cause chain is always included in json (matching sq, where json always prints
// the stack).
type errorBody struct {
	Message string   `json:"message"`
	Causes  []string `json:"causes,omitempty"`
	Offset  *int     `json:"offset,omitempty"`
	Token   string   `json:"token,omitempty"`
}

// renderErrorJSON writes the error as a JSON object. The cause chain is always
// present; a syntax error also carries its byte offset and token.
func renderErrorJSON(w io.Writer, err error) {
	redact := newRedactor(err)
	body := errorBody{Message: redact(err.Error())}
	for _, frame := range stackFrames(err) {
		body.Causes = append(body.Causes, redact(frame))
	}
	if se, ok := errors.AsType[*filterSyntaxError](err); ok {
		off := se.offset
		body.Offset = &off
		body.Token = se.token
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(errorJSON{Error: body})
}

// stackFrames returns the cause breakdown to print for --error.stack (and json
// causes), or nil when the error has a single frame — in that case the frame
// equals the already-printed message, so a one-line "stack" would only repeat it.
func stackFrames(err error) []string {
	frames := causeChain(err)
	if len(frames) <= 1 {
		return nil
	}
	return frames
}

// causeChain reduces err's %w unwrap chain to distinct frames, outermost first:
// each layer is trimmed to the prefix it added over the layer it wraps, so the
// chain reads as "combine", "open source", "<leaf>" instead of repeating the
// leaf at every level. iq wraps with %w rather than carrying goroutine traces,
// so this is its analog of a stack.
func causeChain(err error) []string {
	var msgs []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		msgs = append(msgs, e.Error())
	}
	frames := make([]string, 0, len(msgs))
	for i, msg := range msgs {
		if i+1 < len(msgs) {
			if trimmed, ok := strings.CutSuffix(msg, msgs[i+1]); ok {
				msg = strings.TrimRight(trimmed, ": ")
			}
		}
		if msg != "" {
			frames = append(frames, msg)
		}
	}
	return frames
}

// urlLike matches a scheme://... substring so the pattern step of newRedactor
// can scrub a connection URI in an error before iq prints it. The stop set is
// whitespace and the double quote. A single quote is valid in the userinfo
// (RFC 3986 sub-delims), so the match continues past it. If the match stopped
// there, a quoted password would pass through unmasked. Trailing punctuation
// that the class admits (a URI in parentheses, before a comma or inside single
// quotes) is split off before the parse.
var urlLike = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"]+`)

// trailingDelims is what a URI match can pick up when it is in a sentence:
// prose punctuation, the single quote that closes a quoted URI, and the backtick
// that closes a code span in our own advice strings. The pattern step trims it
// before url.Parse. If the closing single quote or backtick stays on, it goes
// into the port or the host and the parse fails, or it goes into the path and
// stays as an escape that the URI did not have.
//
// The trim is a run, not a single byte, because a URI can close a parenthetical
// and a sentence at once ("(mongodb://host/db)."). The cost is that a URI that
// ends in one of these bytes loses it. An elided "scheme://..." keeps no
// ellipsis, so a message that iq writes spells its example URI out in full.
const trailingDelims = ".,;:!?)]}>`'"

// newRedactor returns a function that removes the connection password from a
// message about err. The error render, the log hook, the MCP error rows and
// redactErr use it, so iq never prints or logs a credential.
//
// The only secret that iq handles is the connection password. It is always in
// the URI userinfo (scheme://user:pass@host). The keyring form is put back into
// that shape before iq connects. The function has two steps:
//
//  1. The typed step. It finds each *url.Error in the unwrap chain of err and
//     replaces the quoted URI in its message with the redacted form. This step
//     is necessary because url.Parse puts the full input in its error, and a
//     URI that does not parse can hold a password with a space or a double
//     quote that the pattern step cannot find.
//  2. The pattern step. It finds each scheme://... substring and replaces it
//     with redactURL of that substring. A URI that does not parse becomes a
//     placeholder, so the step can remove too much but it never leaks.
//
// Query parameters stay unchanged. No driver puts a secret in them today (the
// neo4j ?key= is a property name, not a credential). A source that puts a
// secret outside the userinfo must extend redactURL.
//
// err can be nil. Then only the pattern step runs.
func newRedactor(err error) func(string) string {
	var pairs []string
	for stack := []error{err}; len(stack) > 0; {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if ue, ok := e.(*url.Error); ok { //nolint:errorlint // The loop walks the chain itself, to find every *url.Error.
			pairs = append(pairs, strconv.Quote(ue.URL), strconv.Quote(redactURL(ue.URL)))
		}
		switch u := e.(type) { //nolint:errorlint // The loop walks the chain itself, to find every *url.Error.
		case interface{ Unwrap() error }:
			stack = append(stack, u.Unwrap())
		case interface{ Unwrap() []error }:
			stack = append(stack, u.Unwrap()...)
		}
	}
	typed := strings.NewReplacer(pairs...)
	return func(msg string) string {
		return urlLike.ReplaceAllStringFunc(typed.Replace(msg), func(m string) string {
			// Split off the trailing delimiters so url.Parse sees a clean URI and
			// masks its password. Without the trim, the parse fails on the stray
			// byte and the whole span becomes "(unparseable URI)". The password is
			// masked in both cases. The trim only keeps the message readable.
			u := strings.TrimRight(m, trailingDelims)
			return redactURL(u) + m[len(u):]
		})
	}
}
