package file

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	iqddb "github.com/zsltg/iq/drivers/dynamodb"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// nativeScan collects the native records of a store, in scan order.
func nativeScan(t *testing.T, st *Store) []query.Record {
	t.Helper()
	var out []query.Record
	require.NoError(t, st.NativeScan(context.Background(), func(batch []query.Record) error {
		out = append(out, batch...)
		return nil
	}))
	return out
}

const nativeExport = `{"Item":{"pk":{"S":"a"},"sk":{"N":"3"},"price":{"N":"1.50"},"big":{"N":"12345678901234567890"},"raw":{"B":"AAH/"},"tags":{"SS":["y","x"]},"nums":{"NS":["1.0","2"]},"blobs":{"BS":["AQ=="]},"gone":{"NULL":true},"m":{"M":{"l":{"L":[{"N":"0.10"},{"BOOL":false}]}}}}}
{"Item":{"pk":{"S":"b"},"sk":{"N":"10"}}}
`

func TestNativeScanKeepsTheDynamoDBTypes(t *testing.T) {
	u := writeDump(t, "export.json", []byte(nativeExport), "format=dynamodb-json&keys=pk:S,sk:N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	recs := nativeScan(t, st)
	require.Equal(t, []query.Record{
		{Key: `["a","3"]`, Type: "item", Form: query.FormDynamoJSON, Value: map[string]any{
			"pk":    map[string]any{"S": "a"},
			"sk":    map[string]any{"N": "3"},
			"price": map[string]any{"N": "1.50"},
			"big":   map[string]any{"N": "12345678901234567890"},
			"raw":   map[string]any{"B": "AAH/"},
			"tags":  map[string]any{"SS": []any{"y", "x"}},
			"nums":  map[string]any{"NS": []any{"1.0", "2"}},
			"blobs": map[string]any{"BS": []any{"AQ=="}},
			"gone":  map[string]any{"NULL": true},
			"m": map[string]any{"M": map[string]any{
				"l": map[string]any{"L": []any{map[string]any{"N": "0.10"}, map[string]any{"BOOL": false}}},
			}},
		}},
		{Key: `["b","10"]`, Type: "item", Form: query.FormDynamoJSON, Value: map[string]any{
			"pk": map[string]any{"S": "b"}, "sk": map[string]any{"N": "10"},
		}},
	}, recs)
}

func TestNativeScanKeyMatchesTheTypedScanKey(t *testing.T) {
	u := writeDump(t, "export.json", []byte(nativeExport), "format=dynamodb-json&keys=pk:S,sk:N")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	typed := collect(t, st)
	for _, r := range nativeScan(t, st) {
		require.Contains(t, typed, r.Key)
	}
}

func TestNativeScanReadsAGzipScanOutput(t *testing.T) {
	scan := `{"Items":[{"pk":{"S":"a"},"n":{"N":"2.50"}}],"Count":1}`
	u := writeDump(t, "scan.json.gz", gzipBytes(t, []byte(scan)), "format=dynamodb-json&keys=pk")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	recs := nativeScan(t, st)
	require.Len(t, recs, 1)
	require.Equal(t, map[string]any{"pk": map[string]any{"S": "a"}, "n": map[string]any{"N": "2.50"}}, recs[0].Value)
}

func TestNativeScanSkipsTheDecodeCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.json")
	require.NoError(t, os.WriteFile(path, []byte(nativeExport), 0o600))
	cacheDir := t.TempDir()
	u := URL(path) + "?format=dynamodb-json&keys=pk:S,sk:N"
	cache := CacheConfig{Dir: cacheDir, Enabled: true, MinSize: 1, Index: true}
	st, err := Open(u, numfmt.DecimalAuto, cache)
	require.NoError(t, err)

	// A typed scan fills the cache. A native scan after it must still give the
	// native form, which the cache cannot hold.
	typed := collect(t, st)
	require.Equal(t, map[string]any{"pk": "b", "sk": 10}, typed[`["b","10"]`].Value)
	entries, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "the typed scan must have written a cache")
	recs := nativeScan(t, st)
	require.Equal(t, query.FormDynamoJSON, recs[0].Form)
	require.Equal(t, map[string]any{"S": "b"}, recs[1].Value.(map[string]any)["pk"])
}

func TestNativeScanRefusesOtherFormats(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content string
		query   string
	}{
		{"typed jsonl", "d.jsonl", `{"key":"a","type":"string","value":"x"}` + "\n", ""},
		{"mongoexport", "m.json", `{"_id":"a"}` + "\n", "format=mongoexport"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := Open(writeDump(t, tt.file, []byte(tt.content), tt.query), numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			err = st.NativeScan(context.Background(), func([]query.Record) error { return nil })
			require.ErrorIs(t, err, ErrNoNativeForm)
		})
	}
}

func TestNativeScanNeedsTheKeySchema(t *testing.T) {
	u := writeDump(t, "export.json", []byte(nativeExport), "format=dynamodb-json")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.NativeScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "needs a key schema")
}

func TestNativeScanReportsABadItem(t *testing.T) {
	u := writeDump(t, "export.json", []byte(`{"Item":{"pk":{"Z":"a"}}}`), "format=dynamodb-json&keys=pk")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.NativeScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, `unknown attribute type tag "Z"`)
}

func TestNativeScanOfAPipe(t *testing.T) {
	st, err := OpenPipe(bytes.NewReader([]byte(`{"x":1}`)), FormatJSONL, numfmt.DecimalAuto)
	require.NoError(t, err)
	err = st.NativeScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorIs(t, err, ErrNoNativeForm)
}

// A typed dump that carries a form must keep the form through a typed scan, with
// the decode cache on or off. A lost form makes the destination write typed JSON as a plain map.
func TestTypedScanKeepsTheFormOfADump(t *testing.T) {
	dump := `{"key":"1","type":"item","form":"dynamodb-json","value":{"id":{"N":"1"},"p":{"N":"1.50"}}}` + "\n"
	for _, cached := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "native.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(dump), 0o600))
		cache := CacheConfig{}
		if cached {
			cache = CacheConfig{Dir: t.TempDir(), Enabled: true, MinSize: 1, Index: true}
		}
		st, err := Open(URL(path), numfmt.DecimalAuto, cache)
		require.NoError(t, err)
		for range 2 { // the second pass reads the cache when one exists
			recs := collect(t, st)
			require.Equal(t, query.FormDynamoJSON, recs["1"].Form, "cached=%v", cached)
			require.Equal(t, map[string]any{"id": map[string]any{"N": "1"}, "p": map[string]any{"N": "1.50"}}, recs["1"].Value)
		}
	}
}

func TestDecodeCacheRefusesANativeRecord(t *testing.T) {
	_, err := encodeRecord(query.Record{Key: "k", Form: query.FormDynamoJSON, Value: map[string]any{}})
	require.ErrorContains(t, err, `the cache cannot hold form "dynamodb-json"`)
}

func TestNativeScanReportsAnUnreadableDump(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		content []byte
		query   string
		remove  bool
		want    string
	}{
		{"missing dump file", "export.json", []byte(nativeExport), "format=dynamodb-json&keys=pk:S,sk:N", true, "open dump"},
		{"bad gzip head", "export.json.gz", []byte{0x1f, 0x8b, 0x00}, "format=dynamodb-json&keys=pk", false, "open gzip dump"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := writeDump(t, tc.file, tc.content, tc.query)
			st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			if tc.remove {
				require.NoError(t, os.Remove(st.path))
			}
			called := false
			err = st.NativeScan(context.Background(), func([]query.Record) error { called = true; return nil })
			require.ErrorContains(t, err, tc.want)
			require.False(t, called)
		})
	}
}

func TestNativeRecordForItemRefusesAnUnknownAttribute(t *testing.T) {
	keys, err := iqddb.ParseKeySchema("pk")
	require.NoError(t, err)
	item := map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "a"},
		"x":  &types.UnknownUnionMember{Tag: "Z"},
	}
	rec, err := nativeRecordForItem(keys, item)
	require.ErrorIs(t, err, iqddb.ErrUnknownAttribute)
	require.ErrorContains(t, err, `dynamodb dump item "a": attribute "x": unsupported attribute type`)
	require.Equal(t, query.Record{}, rec)
}

func TestDynamoItemsStopsOnABuildError(t *testing.T) {
	boom := errors.New("boom")
	build := func([]iqddb.KeyAttr, map[string]types.AttributeValue) (query.Record, error) {
		return query.Record{}, boom
	}
	src, err := dynamoItems(strings.NewReader(nativeExport), 10, Hints{Keys: "pk:S,sk:N"}, build)
	require.NoError(t, err)
	called := false
	err = src(context.Background(), func([]query.Record) error { called = true; return nil })
	require.ErrorIs(t, err, boom)
	require.False(t, called)
}
