package redis

import (
	"fmt"
	"strconv"
	"strings"
)

// ANSI SGR codes for redis-cli reply tokens, chosen to match the JSON palette:
// strings green, integers magenta, nil dim.
const (
	colorString  = "32"
	colorInteger = "35"
	colorNil     = "90"
)

// FormatReply renders a query result in redis-cli's cooked style: bulk strings
// quoted, integers as `(integer) N`, a nil reply as `(nil)`, and arrays as a
// numbered, indented list. Status replies such as OK and PONG are shown quoted
// because go-redis collapses RESP simple and bulk strings into one Go type. When
// colored is set, value tokens are wrapped in ANSI color; the numbered-list
// index stays plain, as it is structure rather than a value.
func FormatReply(v any, colored bool) string {
	switch t := v.(type) {
	case nil:
		return paint(colored, colorNil, "(nil)")
	case int64:
		return paint(colored, colorInteger, "(integer) "+strconv.FormatInt(t, 10))
	case string:
		return paint(colored, colorString, quote(t))
	case []byte:
		return paint(colored, colorString, quote(string(t)))
	case []any:
		return formatArray(t, colored)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// paint wraps s in the given SGR code when colored is set, otherwise returns s
// unchanged so the plain rendering is untouched.
func paint(colored bool, code, s string) string {
	if !colored {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// formatArray renders a reply array as a right-aligned, numbered list, indenting
// any nested array under its index so the layout matches redis-cli.
func formatArray(items []any, colored bool) string {
	if len(items) == 0 {
		return "(empty array)"
	}
	width := len(strconv.Itoa(len(items)))
	lines := make([]string, len(items))
	for i, item := range items {
		prefix := fmt.Sprintf("%*d) ", width, i+1)
		lines[i] = prefix + indent(FormatReply(item, colored), len(prefix))
	}
	return strings.Join(lines, "\n")
}

// indent pads every line after the first with n spaces so a nested reply aligns
// under its index prefix.
func indent(s string, n int) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	return strings.ReplaceAll(s, "\n", "\n"+strings.Repeat(" ", n))
}

// quote wraps a string in double quotes and escapes it the way redis-cli does:
// backslash, quote and the common control characters symbolically, anything
// else non-printable as \xHH.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
