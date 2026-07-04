package redis_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/internal/redis"
)

func TestFormatReplyScalars(t *testing.T) {
	require.Equal(t, "(nil)", iqredis.FormatReply(nil))
	require.Equal(t, "(integer) 42", iqredis.FormatReply(int64(42)))
	require.Equal(t, "(integer) -7", iqredis.FormatReply(int64(-7)))
	require.Equal(t, `"hello"`, iqredis.FormatReply("hello"))
	require.Equal(t, `"bytes"`, iqredis.FormatReply([]byte("bytes")))
}

func TestFormatReplyQuotingEscapes(t *testing.T) {
	// Covers the symbolic escapes, a printable that must stay raw, and the \xHH
	// fallback for a low control char and DEL.
	in := "a b\"\\\n\r\t" + string([]byte{0x01, 0x7f})
	require.Equal(t, `"a b\"\\\n\r\t\x01\x7f"`, iqredis.FormatReply(in))
}

func TestFormatReplyEmptyArray(t *testing.T) {
	require.Equal(t, "(empty array)", iqredis.FormatReply([]any{}))
}

func TestFormatReplyFlatArray(t *testing.T) {
	got := iqredis.FormatReply([]any{"write", "test", "ship"})
	require.Equal(t, "1) \"write\"\n2) \"test\"\n3) \"ship\"", got)
}

func TestFormatReplyNestedArrayIndents(t *testing.T) {
	got := iqredis.FormatReply([]any{[]any{"a", "b"}, "c"})
	require.Equal(t, "1) 1) \"a\"\n   2) \"b\"\n2) \"c\"", got)
}

func TestFormatReplyRightAlignsIndex(t *testing.T) {
	items := make([]any, 10)
	for i := range items {
		items[i] = int64(i)
	}

	lines := strings.Split(iqredis.FormatReply(items), "\n")

	require.Equal(t, " 1) (integer) 0", lines[0])
	require.Equal(t, "10) (integer) 9", lines[9])
}
