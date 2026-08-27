package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
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
	_, _ = fmt.Fprintln(w, "iq: "+redactMessage(err.Error()))
	var se *filterSyntaxError
	if cfg.errorTextVerbose && errors.As(err, &se) {
		_, _ = fmt.Fprint(w, se.report())
	}
	if cfg.errorStack {
		for _, frame := range stackFrames(err) {
			_, _ = fmt.Fprintln(w, "  - "+redactMessage(frame))
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
	body := errorBody{Message: redactMessage(err.Error())}
	for _, frame := range stackFrames(err) {
		body.Causes = append(body.Causes, redactMessage(frame))
	}
	var se *filterSyntaxError
	if errors.As(err, &se) {
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
			if trimmed := strings.TrimSuffix(msg, msgs[i+1]); trimmed != msg {
				msg = strings.TrimRight(trimmed, ": ")
			}
		}
		if msg != "" {
			frames = append(frames, msg)
		}
	}
	return frames
}

// urlLike matches a scheme://... substring so redactMessage can scrub a
// connection URL embedded in a wrapped driver error before it is printed. The
// stop set excludes whitespace and quotes; trailing prose punctuation the class
// still admits (a URL inside parentheses or before a comma) is split off in
// redactMessage before parsing.
var urlLike = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"']+`)

// trailingDelims is what a URL match may pick up when it is embedded in a
// sentence: prose punctuation, and the backtick that closes a code span in our
// own advice strings. redactMessage trims it before url.Parse. A backtick is
// never part of a URL, so trimming one can only sharpen the match: left on, it
// lands in the host and fails the parse outright, or in the path and survives as
// a %60 the URL never had.
//
// The trim is a run, not a single byte, because a URL can close a parenthetical
// and a sentence at once ("(mongodb://host/db)."). The cost is that a URL truly
// ending in one of these loses it — an elided "scheme://..." keeps no ellipsis —
// so a message iq writes itself spells its example URL out in full.
const trailingDelims = ".,;:!?)]}>`"

// redactMessage redacts any connection-URL substring in msg, so --error.stack
// and --error.format json (which surface the whole wrapped cause chain, raw leaf
// driver errors included) never print a credential.
//
// This is the top-level backstop of a two-layer scheme. The first layer,
// redactErr, replaces the exact URL iq dialed wherever a driver echoes it, at
// each command boundary; this layer catches any scheme://… URL by shape, even
// one iq did not itself construct. The only secret iq handles is the connection
// password, which always rides in the URL userinfo (scheme://user:pass@host) —
// the keyring form is spliced back into that same shape before connecting — so
// url.Redacted, which masks the userinfo password, covers the entire secret
// surface. A parse failure is redacted to a placeholder rather than passed
// through, so the fallback is always safe (it over-redacts, never leaks).
// Query parameters are deliberately left intact: they carry no secret today
// (neo4j's ?key= is a property name, not a credential), so a source that ever
// puts a secret outside the userinfo must extend redactURL rather than rely here.
func redactMessage(msg string) string {
	return urlLike.ReplaceAllStringFunc(msg, func(m string) string {
		// Split off the trailing delimiters so url.Parse sees a clean URL and masks
		// its password, instead of failing on the stray byte and collapsing the
		// whole span to "(unparseable URI)". The password is masked either way;
		// this only keeps the surrounding message readable.
		u := strings.TrimRight(m, trailingDelims)
		return redactURL(u) + m[len(u):]
	})
}
