package redis_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/drivers/redis"
)

func TestFormatReplyScalars(t *testing.T) {
	require.Equal(t, "(nil)", iqredis.FormatReply(nil, false))
	require.Equal(t, "(integer) 42", iqredis.FormatReply(int64(42), false))
	require.Equal(t, "(integer) -7", iqredis.FormatReply(int64(-7), false))
	require.Equal(t, `"hello"`, iqredis.FormatReply("hello", false))
	require.Equal(t, `"bytes"`, iqredis.FormatReply([]byte("bytes"), false))
}

func TestFormatReplyQuotingEscapes(t *testing.T) {
	// Covers the symbolic escapes, a printable that must stay raw, and the \xHH
	// fallback for a low control char, the highest control char and DEL.
	in := "a b\"\\\n\r\t" + string([]byte{0x01, 0x1f, 0x7f})
	require.Equal(t, `"a b\"\\\n\r\t\x01\x1f\x7f"`, iqredis.FormatReply(in, false))
}

func TestFormatReplyEmptyArray(t *testing.T) {
	require.Equal(t, "(empty array)", iqredis.FormatReply([]any{}, false))
}

func TestFormatReplyFlatArray(t *testing.T) {
	got := iqredis.FormatReply([]any{"write", "test", "ship"}, false)
	require.Equal(t, "1) \"write\"\n2) \"test\"\n3) \"ship\"", got)
}

func TestFormatReplyNestedArrayIndents(t *testing.T) {
	got := iqredis.FormatReply([]any{[]any{"a", "b"}, "c"}, false)
	require.Equal(t, "1) 1) \"a\"\n   2) \"b\"\n2) \"c\"", got)
}

func TestFormatReplyColorsValueTokens(t *testing.T) {
	// Colored value tokens are wrapped in ANSI; the numbered-list index, which is
	// structure, stays plain.
	require.Equal(t, "\x1b[90m(nil)\x1b[0m", iqredis.FormatReply(nil, true))
	require.Equal(t, "\x1b[35m(integer) 42\x1b[0m", iqredis.FormatReply(int64(42), true))
	require.Equal(t, "\x1b[32m\"hi\"\x1b[0m", iqredis.FormatReply("hi", true))
	require.Equal(t, "1) \x1b[32m\"a\"\x1b[0m", iqredis.FormatReply([]any{"a"}, true))
	// An empty array is a structural marker, not a value, so it stays plain.
	require.Equal(t, "(empty array)", iqredis.FormatReply([]any{}, true))
}

func TestFormatReplyRightAlignsIndex(t *testing.T) {
	items := make([]any, 10)
	for i := range items {
		items[i] = int64(i)
	}

	lines := strings.Split(iqredis.FormatReply(items, false), "\n")

	require.Equal(t, " 1) (integer) 0", lines[0])
	require.Equal(t, "10) (integer) 9", lines[9])
}
