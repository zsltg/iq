package file

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// hugeStringRDB is a 22-byte RDB file. It declares a string value of 2^40 bytes
// and then ends. A parser that allocates the declared size throws a fatal
// out-of-memory error, which no code can recover.
func hugeStringRDB() []byte {
	var b bytes.Buffer
	b.WriteString("REDIS0009")
	b.Write([]byte{0x00, 0x01, 'k', 0x81})
	b.Write([]byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00})
	b.WriteByte(0xff)
	return b.Bytes()
}

func TestRDBSourceRejectsHugeDeclaredLength(t *testing.T) {
	err := rdbSource(bytes.NewReader(hugeStringRDB()), 10)(
		context.Background(), func([]query.Record) error { return nil })
	require.Error(t, err)
	require.ErrorContains(t, err, "parse rdb")
}
