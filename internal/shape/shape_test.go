package shape_test

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/diff"
	"github.com/zsltg/iq/internal/shape"
)

// fieldTypes infers a single field "f" holding each of vals (one item per value)
// and returns the projected Comparable types for that field.
func fieldTypes(vals ...any) []any {
	items := make(map[string]any, len(vals))
	for i, v := range vals {
		items[strconv.Itoa(i)] = map[string]any{"f": v}
	}
	return shape.Infer(items).Comparable()[".f"].(map[string]any)["types"].([]any)
}

func TestInfer(t *testing.T) {
	t.Run("object items report field types and presence", func(t *testing.T) {
		items := map[string]any{
			"1": map[string]any{"name": "a", "age": 30},
			"2": map[string]any{"name": "b"},
		}
		got := shape.Infer(items).Comparable()
		require.Equal(t, map[string]any{"types": []any{"object"}}, got["$root"])
		require.Equal(t, map[string]any{"types": []any{"string"}, "presence": "required"}, got[".name"])
		require.Equal(t, map[string]any{"types": []any{"integer"}, "presence": "optional"}, got[".age"])
	})

	t.Run("nested objects flatten to dotted paths", func(t *testing.T) {
		items := map[string]any{
			"1": map[string]any{"addr": map[string]any{"city": "A"}},
		}
		got := shape.Infer(items).Comparable()
		require.Equal(t, map[string]any{"types": []any{"object"}, "presence": "required"}, got[".addr"])
		require.Equal(t, map[string]any{"types": []any{"string"}, "presence": "required"}, got[".addr.city"])
	})

	t.Run("mixed root types collect distinct sorted types", func(t *testing.T) {
		items := map[string]any{
			"a": "plain string",
			"b": []any{1, 2},
			"c": map[string]any{"k": 1},
		}
		got := shape.Infer(items).Comparable()
		require.Equal(t, map[string]any{"types": []any{"array", "object", "string"}}, got["$root"])
	})

	t.Run("a field with two types records both", func(t *testing.T) {
		items := map[string]any{
			"1": map[string]any{"v": 1},
			"2": map[string]any{"v": "x"},
		}
		got := shape.Infer(items).Comparable()
		require.Equal(t, map[string]any{"types": []any{"integer", "string"}, "presence": "required"}, got[".v"])
	})

	t.Run("presence is parent-relative: an optional parent keeps an always-present child required", func(t *testing.T) {
		// addr appears in 2 of 3 items; every addr that appears has a city. The
		// global-denominator bug would read city as optional (2/3); parent-relative
		// presence reads it required (2/2 addr instances).
		items := map[string]any{
			"1": map[string]any{"addr": map[string]any{"city": "A"}},
			"2": map[string]any{"addr": map[string]any{"city": "B"}},
			"3": map[string]any{"other": 1},
		}
		got := shape.Infer(items).Comparable()
		require.Equal(t, "optional", got[".addr"].(map[string]any)["presence"], "addr is in 2 of 3 items")
		require.Equal(t, "required", got[".addr.city"].(map[string]any)["presence"], "city is in every addr")
	})
}

func TestInferKinds(t *testing.T) {
	tests := []struct {
		name string
		vals []any
		want []any
	}{
		{"int is integer", []any{int(5)}, []any{"integer"}},
		{"every int width is integer", []any{int8(1), int64(2), uint32(3)}, []any{"integer"}},
		{"big.Int past int64 is integer", []any{big.NewInt(7)}, []any{"integer"}},
		{"integral float is integer", []any{float64(5)}, []any{"integer"}},
		{"fractional float is number", []any{float64(5.5)}, []any{"number"}},
		{"integer and fractional float collapse to number", []any{int(5), float64(5.5)}, []any{"number"}},
		{"integer and integral float stay integer", []any{int(5), float64(6)}, []any{"integer"}},
		{"json.Number integral literal is integer", []any{json.Number("42")}, []any{"integer"}},
		{"json.Number fractional literal is number", []any{json.Number("4.2")}, []any{"number"}},
		{"json.Number exponent integral is integer", []any{json.Number("1e3")}, []any{"integer"}},
		{"json.Number integer literal past float64 range is integer", []any{json.Number(strings.Repeat("9", 400))}, []any{"integer"}},
		{"bool is bool", []any{true}, []any{"bool"}},
		{"null is null", []any{nil}, []any{"null"}},
		{"integer with string keeps both", []any{int(1), "x"}, []any{"integer", "string"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fieldTypes(tt.vals...))
		})
	}
}

func TestInferIntegerCollapseKeepsOtherKinds(t *testing.T) {
	// The integer kind folds into number, and every other kind must stay. Go
	// randomizes the order of the kind set, so the projection runs 20 times. A
	// loop that stops at the integer kind loses the kinds after it in most passes.
	s := shape.Infer(map[string]any{
		"1": map[string]any{"f": 1},
		"2": map[string]any{"f": 1.5},
		"3": map[string]any{"f": "s"},
		"4": map[string]any{"f": true},
		"5": map[string]any{"f": nil},
	})
	for pass := range 20 {
		got := s.Comparable()[".f"].(map[string]any)["types"]
		require.Equal(t, []any{"bool", "null", "number", "string"}, got, "pass %d", pass)
	}
}

func TestInferFormats(t *testing.T) {
	tests := []struct {
		name string
		vals []any
		want []any
	}{
		{"all rfc3339 keeps date-time", []any{"2020-01-02T03:04:05Z", "2021-06-07T08:09:10+01:00"}, []any{"string(date-time)"}},
		{"one non-date-time drops the format", []any{"2020-01-02T03:04:05Z", "hello"}, []any{"string"}},
		{"all calendar dates keeps date", []any{"2020-01-02", "1999-12-31"}, []any{"string(date)"}},
		{"structurally-dated but invalid is not a date", []any{"2020-13-40"}, []any{"string"}},
		{"one non-date drops the format", []any{"2020-01-02", "2020-01-02x"}, []any{"string"}},
		{"all uuids keeps uuid", []any{"12345678-1234-1234-1234-123456789abc", "ABCDEF00-0000-0000-0000-000000000000"}, []any{"string(uuid)"}},
		{"one non-uuid drops the format", []any{"12345678-1234-1234-1234-123456789abc", "nope"}, []any{"string"}},
		{"a date and a date-time share no format", []any{"2020-01-02", "2020-01-02T03:04:05Z"}, []any{"string"}},
		{"plain strings have no format", []any{"a", "b"}, []any{"string"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fieldTypes(tt.vals...))
		})
	}
}

func TestInferArrays(t *testing.T) {
	t.Run("scalar array unifies element types under []", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": map[string]any{"tags": []any{"a", "b"}}}).Comparable()
		require.Equal(t, map[string]any{"types": []any{"array"}, "presence": "required"}, got[".tags"])
		require.Equal(t, map[string]any{"types": []any{"string"}}, got[".tags[]"])
	})

	t.Run("object array recurses and counts element-child presence against elements seen", func(t *testing.T) {
		items := map[string]any{"1": map[string]any{"items": []any{
			map[string]any{"qty": 1, "note": "x"},
			map[string]any{"qty": 2},
		}}}
		got := shape.Infer(items).Comparable()
		require.Equal(t, []any{"array"}, got[".items"].(map[string]any)["types"])
		require.Equal(t, []any{"object"}, got[".items[]"].(map[string]any)["types"])
		require.Equal(t, map[string]any{"types": []any{"integer"}, "presence": "required"}, got[".items[].qty"], "qty in every element")
		require.Equal(t, map[string]any{"types": []any{"string"}, "presence": "optional"}, got[".items[].note"], "note in 1 of 2 elements")
	})

	t.Run("empty array records array and nothing below", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": map[string]any{"items": []any{}}}).Comparable()
		require.Equal(t, map[string]any{"types": []any{"array"}, "presence": "required"}, got[".items"])
		require.NotContains(t, got, ".items[]")
	})

	t.Run("mixed element types collect both", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": map[string]any{"vals": []any{1, "x"}}}).Comparable()
		require.Equal(t, []any{"integer", "string"}, got[".vals[]"].(map[string]any)["types"])
	})

	t.Run("nested arrays compose [][]", func(t *testing.T) {
		got := shape.Infer(map[string]any{"1": map[string]any{"m": []any{[]any{1, 2}, []any{3}}}}).Comparable()
		require.Equal(t, []any{"array"}, got[".m"].(map[string]any)["types"])
		require.Equal(t, []any{"array"}, got[".m[]"].(map[string]any)["types"])
		require.Equal(t, []any{"integer"}, got[".m[][]"].(map[string]any)["types"])
	})
}

func TestInferFeedsTree(t *testing.T) {
	a := shape.Infer(map[string]any{"1": map[string]any{"name": "x"}}).Comparable()
	b := shape.Infer(map[string]any{"1": map[string]any{"name": "x", "email": "y"}}).Comparable()
	changes := diff.Tree(a, b)
	require.Equal(t, []diff.Change{
		{Path: []string{".email"}, Op: diff.OpAdd, New: map[string]any{"types": []any{"string"}, "presence": "required"}},
	}, changes)
}

func TestInferFloatKindInfinities(t *testing.T) {
	// +Inf and -Inf are not integral, so floatKind must classify both as number:
	// the IsInf guard cannot be dropped, widened to ||, or narrowed to one sign.
	tests := []struct {
		name string
		val  any
	}{
		{"positive infinity is number", math.Inf(1)},
		{"negative infinity is number", math.Inf(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, []any{"number"}, fieldTypes(tt.val))
		})
	}
}

func TestInferMixedKindFormat(t *testing.T) {
	// A field holding formatted dates and an integer renders the string kind as
	// string(date) but the integer kind as integer: the format decoration is gated
	// on the string kind and is never applied to a non-string kind.
	require.Equal(t, []any{"integer", "string(date)"}, fieldTypes("2020-01-02", "1999-12-31", 5))
}

func TestInferFormatObserveOrder(t *testing.T) {
	// Array elements are observed in deterministic slice order, so a format a later
	// element breaks must fall away. Both orders per format pin every operand of the
	// all-or-nothing intersection: the format-first order catches a stale-carry (a
	// dropped current-value operand), the format-last order catches a dropped
	// accumulated-value operand.
	const (
		dt   = "2020-01-02T03:04:05Z"
		date = "2020-01-02"
		uuid = "12345678-1234-1234-1234-123456789abc"
	)
	tests := []struct {
		name  string
		elems []any
	}{
		{"a non-date-time before a date-time drops date-time", []any{"plain", dt}},
		{"a date-time before a non-date-time drops date-time", []any{dt, "plain"}},
		{"a non-date before a date drops date", []any{"plain", date}},
		{"a date before a non-date drops date", []any{date, "plain"}},
		{"a non-uuid before a uuid drops uuid", []any{"plain", uuid}},
		{"a uuid before a non-uuid drops uuid", []any{uuid, "plain"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shape.Infer(map[string]any{"1": map[string]any{"f": tt.elems}}).Comparable()
			require.Equal(t, []any{"string"}, got[".f[]"].(map[string]any)["types"])
		})
	}
}

func TestInferFloatKinds(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want []any
	}{
		{"NaN is number", math.NaN(), []any{"number"}},
		{"float32 NaN is number", float32(math.NaN()), []any{"number"}},
		{"integral float32 is integer", float32(5), []any{"integer"}},
		{"fractional float32 is number", float32(5.5), []any{"number"}},
		{"float32 infinity is number", float32(math.Inf(1)), []any{"number"}},
		{"negative zero is integer", math.Copysign(0, -1), []any{"integer"}},
		{"uintptr is integer", uintptr(3), []any{"integer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fieldTypes(tt.val))
		})
	}
}

func TestInferKindNames(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want []any
	}{
		{"null", nil, []any{"null"}},
		{"bool", true, []any{"bool"}},
		{"integer", 1, []any{"integer"}},
		{"number", 1.5, []any{"number"}},
		{"string", "s", []any{"string"}},
		{"array", []any{1}, []any{"array"}},
		{"object", map[string]any{"k": 1}, []any{"object"}},
		{"unknown", struct{}{}, []any{"unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fieldTypes(tt.val))
		})
	}
}

func TestInferUnknownKindSchema(t *testing.T) {
	doc := shape.Infer(map[string]any{"1": map[string]any{"f": struct{}{}}}).JSONSchema("t")
	f := doc["properties"].(map[string]any)["f"].(map[string]any)
	require.Equal(t, map[string]any{}, f, "an unknown-only node has no type")
}

func TestInferEmptyObjectProjection(t *testing.T) {
	t.Run("at a field", func(t *testing.T) {
		s := shape.Infer(map[string]any{"1": map[string]any{"f": map[string]any{}}})
		require.Equal(t, map[string]any{
			"$root": map[string]any{"types": []any{"object"}},
			".f":    map[string]any{"types": []any{"object"}, "presence": "required"},
		}, s.Comparable())
		require.Equal(t, map[string]any{
			"$schema":    "https://json-schema.org/draft/2020-12/schema",
			"title":      "t",
			"type":       "object",
			"properties": map[string]any{"f": map[string]any{"type": "object"}},
			"required":   []any{"f"},
		}, s.JSONSchema("t"))
	})

	t.Run("inside a collapsing map", func(t *testing.T) {
		items := map[string]any{}
		for i := range 8 {
			s := strconv.Itoa(i)
			items[s] = map[string]any{"f": map[string]any{"id" + s: map[string]any{}}}
		}
		s := shape.Infer(items)
		require.Equal(t, map[string]any{
			"$root": map[string]any{"types": []any{"object"}},
			".f":    map[string]any{"types": []any{"map"}, "presence": "required"},
			".f{}":  map[string]any{"types": []any{"object"}},
		}, s.Comparable())
		f := s.JSONSchema("t")["properties"].(map[string]any)["f"]
		require.Equal(t, map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "object"},
		}, f)
	})
}

func TestInferEmptyArrayInsideMap(t *testing.T) {
	items := map[string]any{}
	for i := range 8 {
		s := strconv.Itoa(i)
		items[s] = map[string]any{"f": map[string]any{"id" + s: []any{}}}
	}
	sh := shape.Infer(items)
	require.Equal(t, map[string]any{
		"$root": map[string]any{"types": []any{"object"}},
		".f":    map[string]any{"types": []any{"map"}, "presence": "required"},
		".f{}":  map[string]any{"types": []any{"array"}},
	}, sh.Comparable())
	f := sh.JSONSchema("t")["properties"].(map[string]any)["f"]
	require.Equal(t, map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "array"},
	}, f)
}
