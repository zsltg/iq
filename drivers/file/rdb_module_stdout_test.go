package file

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

const (
	rdbOpModuleAux = 247
	rdbOpSelectDB  = 254
	rdbOpEOF       = 255
	rdbTypeString  = 0
	rdbTypeModule2 = 7
)

// unknownModuleID is the ID of a module type that no handler knows. It packs the
// nine-character name "unknown-m" in 6-bit codes, then a 4-bit encoding version of 1.
func unknownModuleID() uint64 {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var id uint64
	for _, c := range []byte("unknown-m") {
		id |= uint64(bytes.IndexByte([]byte(charset), c))
		id <<= 6
	}
	id <<= 4
	return id | 1
}

// writeLen64 writes a value as an RDB 64-bit length: a 0x81 marker, then 8 bytes.
func writeLen64(b *bytes.Buffer, v uint64) {
	b.WriteByte(0x81)
	_ = binary.Write(b, binary.BigEndian, v)
}

// writeUnknownModule writes a module ID and a payload that has one unsigned integer.
func writeUnknownModule(b *bytes.Buffer) {
	writeLen64(b, unknownModuleID())
	b.WriteByte(2) // module opcode for an unsigned integer
	b.WriteByte(7) // the integer, as a 6-bit length
	b.WriteByte(0) // module opcode for the end of the payload
}

func writeStringEntry(b *bytes.Buffer, key, value string) {
	b.WriteByte(rdbTypeString)
	b.WriteByte(byte(len(key)))
	b.WriteString(key)
	b.WriteByte(byte(len(value)))
	b.WriteString(value)
}

// moduleRDB builds a version 9 dump. The string entries "a" and "c" surround the
// module data, so the test shows that the decoder continues past it.
func moduleRDB(withAux, withValue bool) []byte {
	var b bytes.Buffer
	b.WriteString("REDIS0009")
	if withAux {
		b.WriteByte(rdbOpModuleAux)
		writeUnknownModule(&b)
	}
	b.WriteByte(rdbOpSelectDB)
	b.WriteByte(0)
	writeStringEntry(&b, "a", "1")
	if withValue {
		b.WriteByte(rdbTypeModule2)
		b.WriteByte(1)
		b.WriteString("m")
		writeUnknownModule(&b)
	}
	writeStringEntry(&b, "c", "3")
	b.WriteByte(rdbOpEOF)
	b.Write(make([]byte, 8)) // checksum, zero means none
	return b.Bytes()
}

// captureStdout runs fn and returns what fn wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	func() {
		defer func() {
			os.Stdout = orig
			require.NoError(t, w.Close())
		}()
		fn()
	}()
	out := <-done
	require.NoError(t, r.Close())
	return out
}

func TestRDBSourceUnknownModuleWritesNothingToStdout(t *testing.T) {
	tests := []struct {
		name      string
		withAux   bool
		withValue bool
	}{
		{name: "module aux entry", withAux: true},
		{name: "module value entry", withValue: true},
		{name: "both entries", withAux: true, withValue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := moduleRDB(tt.withAux, tt.withValue)
			var keys []string
			var err error
			out := captureStdout(t, func() {
				err = rdbSource(bytes.NewReader(data), 10)(
					context.Background(), func(batch []query.Record) error {
						for _, rec := range batch {
							keys = append(keys, rec.Key)
						}
						return nil
					})
			})
			require.NoError(t, err)
			require.Empty(t, out)
			require.Equal(t, []string{"a", "c"}, keys)
		})
	}
}
