package dynamodb

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	awsdynamodb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// richItem holds every attribute type, a fractional and a huge number, and nested
// maps and lists. The key attribute id is an N, as in fakeStore.
func richItem() map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"id":    &types.AttributeValueMemberN{Value: "7"},
		"s":     &types.AttributeValueMemberS{Value: "text"},
		"price": &types.AttributeValueMemberN{Value: "1.50"},
		"big":   &types.AttributeValueMemberN{Value: "12345678901234567890123456789012345678"},
		"b":     &types.AttributeValueMemberB{Value: []byte{0, 1, 2, 0xff}},
		"yes":   &types.AttributeValueMemberBOOL{Value: true},
		"no":    &types.AttributeValueMemberBOOL{Value: false},
		"nil":   &types.AttributeValueMemberNULL{Value: true},
		"ss":    &types.AttributeValueMemberSS{Value: []string{"y", "x"}},
		"ns":    &types.AttributeValueMemberNS{Value: []string{"1.0", "2"}},
		"bs":    &types.AttributeValueMemberBS{Value: [][]byte{{1}, {2, 3}}},
		"m": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"inner": &types.AttributeValueMemberL{Value: []types.AttributeValue{
				&types.AttributeValueMemberN{Value: "0.10"},
				&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
					"deep": &types.AttributeValueMemberB{Value: []byte("hi")},
				}},
			}},
		}},
		"emptyl": &types.AttributeValueMemberL{Value: []types.AttributeValue{}},
		"emptym": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}},
	}
}

func richNative() map[string]any {
	return map[string]any{
		"id":    map[string]any{"N": "7"},
		"s":     map[string]any{"S": "text"},
		"price": map[string]any{"N": "1.50"},
		"big":   map[string]any{"N": "12345678901234567890123456789012345678"},
		"b":     map[string]any{"B": "AAEC/w=="},
		"yes":   map[string]any{"BOOL": true},
		"no":    map[string]any{"BOOL": false},
		"nil":   map[string]any{"NULL": true},
		"ss":    map[string]any{"SS": []any{"y", "x"}},
		"ns":    map[string]any{"NS": []any{"1.0", "2"}},
		"bs":    map[string]any{"BS": []any{"AQ==", "AgM="}},
		"m": map[string]any{"M": map[string]any{
			"inner": map[string]any{"L": []any{
				map[string]any{"N": "0.10"},
				map[string]any{"M": map[string]any{"deep": map[string]any{"B": "aGk="}}},
			}},
		}},
		"emptyl": map[string]any{"L": []any{}},
		"emptym": map[string]any{"M": map[string]any{}},
	}
}

func TestEncodeItem(t *testing.T) {
	got, err := EncodeItem(richItem())
	require.NoError(t, err)
	require.Equal(t, richNative(), got)
}

func TestEncodeItemRefusesAnUnknownAttributeType(t *testing.T) {
	_, err := EncodeItem(map[string]types.AttributeValue{
		"a": &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.UnknownUnionMember{Tag: "X"}}},
	})
	require.ErrorIs(t, err, ErrUnknownAttribute)
	require.ErrorContains(t, err, `attribute "a": element 0: unsupported attribute type`)
}

func TestEncodeThenParseIsTheIdentity(t *testing.T) {
	enc, err := EncodeItem(richItem())
	require.NoError(t, err)
	raw, err := json.Marshal(enc)
	require.NoError(t, err)
	back, err := ParseItem(raw)
	require.NoError(t, err)
	require.Equal(t, richItem(), back)
}

func TestNativeScan(t *testing.T) {
	pages := [][]map[string]types.AttributeValue{{idItem(1), idItem(2), idItem(3)}, {richItem()}}
	call := 0
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		out := &awsdynamodb.ScanOutput{Items: pages[call]}
		if call == 0 {
			out.LastEvaluatedKey = idItem(3)
		}
		call++
		return out, nil
	}}
	s := fakeStore(t, fake)
	s.pageSize = 2
	var sizes []int
	var recs []query.Record
	require.NoError(t, s.NativeScan(context.Background(), func(b []query.Record) error {
		sizes = append(sizes, len(b))
		recs = append(recs, b...)
		return nil
	}))
	require.Equal(t, []int{2, 2}, sizes)
	require.Len(t, recs, 4)
	require.Equal(t, query.Record{
		Key: "1", Type: "item", Form: query.FormDynamoJSON,
		Value: map[string]any{"id": map[string]any{"N": "1"}},
	}, recs[0])
	require.Equal(t, "7", recs[3].Key)
	require.Equal(t, richNative(), recs[3].Value)
}

func TestNativeScanErrors(t *testing.T) {
	boom := errBoom
	tests := []struct {
		name    string
		fake    *fakeDDB
		noTable bool
		fn      func([]query.Record) error
		wantErr string
	}{
		{"no table", &fakeDDB{}, true, func([]query.Record) error { return nil }, "no table"},
		{
			"scan error",
			&fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) { return nil, boom }},
			false, func([]query.Record) error { return nil }, "dynamodb scan",
		},
		{
			"callback error stops the scan",
			&fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
				return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{idItem(1)}}, nil
			}},
			false, func([]query.Record) error { return boom }, "boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fakeStore(t, tt.fake)
			s.pageSize = 1
			if tt.noTable {
				s.keys = nil
			}
			err := s.NativeScan(context.Background(), tt.fn)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestNativeRoundTrip proves the property of the whole feature: the records that
// NativeScan emits, fed to Put, write back the very attribute values that the scan
// read, including the types that the normalized form loses.
func TestNativeRoundTrip(t *testing.T) {
	src := fakeStore(t, &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{richItem(), idItem(9)}}, nil
	}})
	var recs []query.Record
	require.NoError(t, src.NativeScan(context.Background(), func(b []query.Record) error {
		recs = append(recs, b...)
		return nil
	}))

	dst, cf := ctxStore(t, &fakeDDB{})
	for _, mode := range []query.WriteMode{query.Upsert, query.InsertOnly} {
		cf.puts = nil
		_, err := dst.Put(context.Background(), recs, mode)
		require.NoError(t, err)
		require.Len(t, cf.puts, 2)
		require.Equal(t, richItem(), cf.puts[0].Item)
		require.Equal(t, idItem(9), cf.puts[1].Item)
	}
}

// TestNativeRoundTripSurvivesAJSONDump sends the records through the typed dump
// envelope, as a dump file does, before they reach Put.
func TestNativeRoundTripSurvivesAJSONDump(t *testing.T) {
	enc, err := EncodeItem(richItem())
	require.NoError(t, err)
	var line []byte
	line, err = json.Marshal(map[string]any{"key": "7", "type": "item", "form": "dynamodb-json", "value": enc})
	require.NoError(t, err)
	rec, err := query.DecodeTypedRecord(line)
	require.NoError(t, err)
	require.Equal(t, query.FormDynamoJSON, rec.Form)

	dst, cf := ctxStore(t, &fakeDDB{})
	_, err = dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, richItem(), cf.puts[0].Item)
}

func TestPutNativeKeepsTheKeyOverlay(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	// The record key is authoritative: the typed key attribute replaces the
	// tag in the value, as it does for the normalized form.
	rec := query.Record{Key: "5", Form: query.FormDynamoJSON, Value: map[string]any{
		"id": map[string]any{"S": "other"}, "v": map[string]any{"N": "1.5"},
	}}
	_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberN{Value: "5"},
		"v":  &types.AttributeValueMemberN{Value: "1.5"},
	}, cf.puts[0].Item)
}

func TestPutNativeKeylessNeedsTheKeyAttribute(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	_, err := dst.Put(context.Background(), []query.Record{
		{Form: query.FormDynamoJSON, Value: map[string]any{"v": map[string]any{"N": "1"}}},
	}, query.Upsert)
	require.ErrorContains(t, err, `record missing key attribute "id"`)
	require.Empty(t, cf.puts)

	_, err = dst.Put(context.Background(), []query.Record{
		{Form: query.FormDynamoJSON, Value: map[string]any{"id": map[string]any{"N": "3"}}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, idItem(3), cf.puts[0].Item)
}

func TestPutNativeRefusesMalformedValues(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantErr string
	}{
		{"not an object", []any{"x"}, "native value must be an item object"},
		{"attribute is not an object", map[string]any{"a": "plain"}, `attribute "a"`},
		{"no tag", map[string]any{"a": map[string]any{}}, "exactly one type tag, got 0"},
		{"two tags", map[string]any{"a": map[string]any{"S": "x", "N": "1"}}, "exactly one type tag, got 2"},
		{"unknown tag", map[string]any{"a": map[string]any{"Z": "x"}}, `unknown attribute type tag "Z"`},
		{"lower case tag", map[string]any{"a": map[string]any{"s": "x"}}, `unknown attribute type tag "s"`},
		{"S holds a number", map[string]any{"a": map[string]any{"S": 5}}, "decode string value"},
		{"N holds a number", map[string]any{"a": map[string]any{"N": 5}}, "decode string value"},
		{"N holds a float", map[string]any{"a": map[string]any{"N": 1.5}}, "decode string value"},
		{"BOOL holds a string", map[string]any{"a": map[string]any{"BOOL": "true"}}, "decode BOOL"},
		{"B is not base64", map[string]any{"a": map[string]any{"B": "***"}}, "decode B"},
		{"SS holds a number", map[string]any{"a": map[string]any{"SS": []any{1}}}, "decode SS"},
		{"BS is not base64", map[string]any{"a": map[string]any{"BS": []any{"***"}}}, "decode BS element"},
		{"L holds a plain value", map[string]any{"a": map[string]any{"L": []any{"x"}}}, "decode attribute value"},
		{"M holds a plain value", map[string]any{"a": map[string]any{"M": map[string]any{"k": 1}}}, `attribute "k"`},
		{"N is not a number", map[string]any{"a": map[string]any{"N": "abc"}}, `attribute "a": "abc" is not a finite decimal number`},
		{"N is NaN", map[string]any{"a": map[string]any{"N": "NaN"}}, "is not a finite decimal number"},
		{"N has 39 digits", map[string]any{"a": map[string]any{"N": "123456789012345678901234567890123456789"}}, "39 significant digits"},
		{"N is out of range", map[string]any{"a": map[string]any{"N": "1e126"}}, "outside the DynamoDB range"},
		{"nested N is bad", map[string]any{"a": map[string]any{"M": map[string]any{"b": map[string]any{"L": []any{map[string]any{"N": "x"}}}}}}, `attribute "a.b[0]"`},
		{"NS member is bad", map[string]any{"a": map[string]any{"NS": []any{"1", "x"}}}, `attribute "a"`},
		{"empty SS", map[string]any{"a": map[string]any{"SS": []any{}}}, "SS must not be empty"},
		{"empty NS", map[string]any{"a": map[string]any{"NS": []any{}}}, "NS must not be empty"},
		{"empty BS", map[string]any{"a": map[string]any{"BS": []any{}}}, "BS must not be empty"},
		{"NULL false", map[string]any{"a": map[string]any{"NULL": false}}, "NULL must be true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst, cf := ctxStore(t, &fakeDDB{})
			rec := query.Record{Key: "kk", Form: query.FormDynamoJSON, Value: tt.value}
			_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, cf.puts)
		})
	}
}

func TestPutNativeNamesTheKeyInAnError(t *testing.T) {
	dst, _ := ctxStore(t, &fakeDDB{})
	rec := query.Record{Key: "12", Form: query.FormDynamoJSON, Value: map[string]any{"a": map[string]any{"Z": "x"}}}
	_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
	require.ErrorContains(t, err, `dynamodb: record "12" is not valid DynamoDB JSON`)
}

func TestPutRefusesAForeignForm(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	_, err := dst.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"id": 1}},
		{Key: "2", Form: query.FormMongoEJSON, Value: map[string]any{"id": map[string]any{"$numberInt": "2"}}},
	}, query.Upsert)
	require.ErrorContains(t, err, `dynamodb: record "2" has form "mongo-ejson", but this destination accepts only forms "json" and "dynamodb-json"`)
	require.Empty(t, cf.puts)
}

func TestPutNativeBuildsEveryItemBeforeTheFirstWrite(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	_, err := dst.Put(context.Background(), []query.Record{
		{Key: "1", Form: query.FormDynamoJSON, Value: map[string]any{"a": map[string]any{"N": "1"}}},
		{Key: "2", Form: query.FormDynamoJSON, Value: map[string]any{"a": map[string]any{"N": "nope"}}},
	}, query.Upsert)
	require.Error(t, err)
	require.Empty(t, cf.puts)
}

func TestPutNativeRefusesAnInvalidByte(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	rec := query.Record{Key: "1", Form: query.FormDynamoJSON, Value: map[string]any{"a": map[string]any{"S": "x\xffy"}}}
	_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
	require.Error(t, err)
	require.Empty(t, cf.puts)
}

func TestPutMixedFormsInOneBatch(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	_, err := dst.Put(context.Background(), []query.Record{
		{Key: "1", Value: map[string]any{"v": "a"}},
		{Key: "2", Form: query.FormDynamoJSON, Value: map[string]any{"v": map[string]any{"S": "b"}}},
	}, query.Upsert)
	require.NoError(t, err)
	require.Equal(t, map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberN{Value: "1"}, "v": &types.AttributeValueMemberS{Value: "a"},
	}, cf.puts[0].Item)
	require.Equal(t, map[string]types.AttributeValue{
		"id": &types.AttributeValueMemberN{Value: "2"}, "v": &types.AttributeValueMemberS{Value: "b"},
	}, cf.puts[1].Item)
}

func TestNativeScanKeyMatchesTheTypedKey(t *testing.T) {
	s := fakeStore(t, &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{idItem(41)}}, nil
	}})
	var native, typed []string
	require.NoError(t, s.NativeScan(context.Background(), func(b []query.Record) error {
		for _, r := range b {
			native = append(native, r.Key)
		}
		return nil
	}))
	require.NoError(t, s.TypedScan(context.Background(), func(b []query.Record) error {
		for _, r := range b {
			typed = append(typed, r.Key)
		}
		return nil
	}))
	require.Equal(t, typed, native)
	require.Equal(t, []string{strconv.Itoa(41)}, native)
}

type nativeCtxKey struct{}

func TestNativeScanRequestsTheTableWithTheCallerContext(t *testing.T) {
	var table *string
	fake := &fakeDDB{scanFn: func(in *awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		table = in.TableName
		return &awsdynamodb.ScanOutput{}, nil
	}}
	s := fakeStore(t, fake)
	ctx := context.WithValue(context.Background(), nativeCtxKey{}, "marker")
	require.NoError(t, s.NativeScan(ctx, func([]query.Record) error { return nil }))
	require.NotNil(t, table)
	require.Equal(t, "t", *table)
	require.NotNil(t, fake.scanCtx)
	require.Equal(t, "marker", fake.scanCtx.Value(nativeCtxKey{}))
}

func TestNativeScanWrapsTheScanError(t *testing.T) {
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) { return nil, errBoom }}
	s := fakeStore(t, fake)
	err := s.NativeScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorIs(t, err, errBoom)
	require.ErrorContains(t, err, "dynamodb scan")
}

func TestNativeScanStopsOnAnItemThatCannotBeEncoded(t *testing.T) {
	item := idItem(1)
	item["x"] = &types.UnknownUnionMember{Tag: "Z"}
	fake := &fakeDDB{scanFn: func(*awsdynamodb.ScanInput) (*awsdynamodb.ScanOutput, error) {
		return &awsdynamodb.ScanOutput{Items: []map[string]types.AttributeValue{item}}, nil
	}}
	s := fakeStore(t, fake)
	called := false
	err := s.NativeScan(context.Background(), func([]query.Record) error { called = true; return nil })
	require.ErrorIs(t, err, ErrUnknownAttribute)
	require.ErrorContains(t, err, `item "1"`)
	require.False(t, called)
}

func TestEncodeItemReportsAnUnknownAttributeInsideAMap(t *testing.T) {
	item := map[string]types.AttributeValue{
		"m": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"x": &types.UnknownUnionMember{Tag: "Z"},
		}},
	}
	_, err := EncodeItem(item)
	require.ErrorIs(t, err, ErrUnknownAttribute)
	require.ErrorContains(t, err, `attribute "m": attribute "x"`)
}

func TestPutNativeRefusesAValueThatJSONCannotEncode(t *testing.T) {
	dst, cf := ctxStore(t, &fakeDDB{})
	rec := query.Record{Key: "1", Form: query.FormDynamoJSON, Value: map[string]any{"a": make(chan int)}}
	_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
	require.ErrorContains(t, err, `record "1": encode native value`)
	var unsupported *json.UnsupportedTypeError
	require.ErrorAs(t, err, &unsupported)
	require.Empty(t, cf.puts)
}

func TestPutNativeKeepsTheCauseOfAnInvalidValue(t *testing.T) {
	t.Run("parse error", func(t *testing.T) {
		dst, _ := ctxStore(t, &fakeDDB{})
		rec := query.Record{Key: "1", Form: query.FormDynamoJSON, Value: map[string]any{"a": "plain"}}
		_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
		require.ErrorContains(t, err, `record "1" is not valid DynamoDB JSON`)
		var typeErr *json.UnmarshalTypeError
		require.ErrorAs(t, err, &typeErr)
	})
	t.Run("number error", func(t *testing.T) {
		dst, _ := ctxStore(t, &fakeDDB{})
		rec := query.Record{Key: "1", Form: query.FormDynamoJSON, Value: map[string]any{"a": map[string]any{"N": "abc"}}}
		_, err := dst.Put(context.Background(), []query.Record{rec}, query.Upsert)
		require.ErrorContains(t, err, `record "1" is not valid DynamoDB JSON`)
		var numErr *numberError
		require.ErrorAs(t, err, &numErr)
	})
}
