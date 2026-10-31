package dynamodb

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

const digits38 = "12345678901234567890123456789012345678"

func TestCheckNumber(t *testing.T) {
	nines38 := "9.9999999999999999999999999999999999999"
	tests := []struct {
		name string
		in   string
		want string // empty means valid
	}{
		{"zero", "0", ""},
		{"negative zero", "-0", ""},
		{"zero with fraction", "0.000", ""},
		{"zero with a huge exponent", "0e999", ""},
		{"one", "1", ""},
		{"negative", "-1", ""},
		{"plus sign", "+1.5", ""},
		{"fraction only", ".5", ""},
		{"trailing point", "5.", ""},
		{"exponent", "1e5", ""},
		{"38 digits", digits38, ""},
		{"38 digits with a fraction", "1234567890123456789.0123456789012345678", ""},
		{"38 digits negative", "-" + digits38, ""},
		{"39 digits", digits38 + "9", "has 39 significant digits"},
		{"39 digits negative", "-" + digits38 + "9", "has 39 significant digits"},
		{"39 digits with a fraction", "1234567890123456789.01234567890123456789", "has 39 significant digits"},
		{"trailing zeros do not count", "1" + "0000000000000000000000000000000000000000", ""},
		{"zeros around 38 digits do not count", "00" + digits38 + "000.000", ""},
		{"39 digits then zeros", digits38 + "9000", "has 39 significant digits"},
		{"min magnitude", "1E-130", ""},
		{"min magnitude negative", "-1E-130", ""},
		{"min magnitude as a fraction", "0.1E-129", ""},
		{"below min magnitude", "1E-131", "is outside the DynamoDB range"},
		{"below min magnitude negative", "-1E-131", "is outside the DynamoDB range"},
		{"below min magnitude as a fraction", "0.01E-129", "is outside the DynamoDB range"},
		{"below min magnitude, long zeros", "0." + "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001", "is outside the DynamoDB range"},
		{"max magnitude", nines38 + "E+125", ""},
		{"max magnitude negative", "-" + nines38 + "E+125", ""},
		{"max magnitude lower case e", nines38 + "e125", ""},
		{"max magnitude as an integer", "999999999999999999999999999999999999990000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", ""},
		{"max magnitude as an integer, one more zero", "9999999999999999999999999999999999999900000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", "is outside the DynamoDB range"},
		{"1E+125", "1E+125", ""},
		{"above max magnitude", "1E+126", "is outside the DynamoDB range"},
		{"above max magnitude negative", "-1E+126", "is outside the DynamoDB range"},
		{"above max magnitude, shifted", "10E+125", "is outside the DynamoDB range"},
		{"above max magnitude, 38 digits", "1" + "0000000000000000000000000000000000000000" + "0E+86", "is outside the DynamoDB range"},
		{"exponent beyond int", "1e99999999999999999999", "is outside the DynamoDB range"},
		{"negative exponent beyond int", "1e-99999999999999999999", "is outside the DynamoDB range"},
		{"NaN", "NaN", "is not a finite decimal number"},
		{"infinity", "+Inf", "is not a finite decimal number"},
		{"empty", "", "is not a finite decimal number"},
		{"sign only", "-", "is not a finite decimal number"},
		{"point only", ".", "is not a finite decimal number"},
		{"two signs", "+-1", "is not a finite decimal number"},
		{"empty exponent", "1e", "is not a finite decimal number"},
		{"signs in the exponent", "1e+-5", "is not a finite decimal number"},
		{"exponent without a mantissa", "e5", "is not a finite decimal number"},
		{"hex", "0x10", "is not a finite decimal number"},
		{"underscore", "1_0", "is not a finite decimal number"},
		{"space", "1 ", "is not a finite decimal number"},
		{"two points", "1.2.3", "is not a finite decimal number"},
		{"fraction exponent", "1e1.5", "is not a finite decimal number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNumber(tt.in)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
			require.ErrorContains(t, err, tt.in)
		})
	}
}

func TestToAttributeValueNumbers(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string // the N text; empty when an error is wanted
		err  string
	}{
		{"int", 123, "123", ""},
		{"min int64", int64(math.MinInt64), "-9223372036854775808", ""},
		{"float", 2.5, "2.5", ""},
		{"float exponent", 1e21, "1e+21", ""},
		{"float small", 1e-7, "1e-07", ""},
		{"float large in range", 1.5e108, "1.5e+108", ""},
		{"json number kept", json.Number("1.50"), "1.50", ""},
		{"json number 38 digits", json.Number(digits38), digits38, ""},
		{"json number 39 digits", json.Number(digits38 + "9"), "", "has 39 significant digits"},
		{"json number in range", json.Number("9.9999999999999999999999999999999999999E+125"), "9.9999999999999999999999999999999999999E+125", ""},
		{"json number too large", json.Number("1E+126"), "", "is outside the DynamoDB range"},
		{"json number too small", json.Number("1E-131"), "", "is outside the DynamoDB range"},
		{"json number not a number", json.Number("abc"), "", "is not a finite decimal number"},
		{"float far above max", 1e200, "", "is outside the DynamoDB range"},
		{"float over max", 1e126, "", "is outside the DynamoDB range"},
		{"float under min", 5e-324, "", "is outside the DynamoDB range"},
		{"float NaN", math.NaN(), "", "is not a finite decimal number"},
		{"float +Inf", math.Inf(1), "", "is not a finite decimal number"},
		{"float -Inf", math.Inf(-1), "", "is not a finite decimal number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			av, err := toAttributeValue(map[string]any{"n": tt.in})
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				require.ErrorContains(t, err, `attribute "n"`)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &types.AttributeValueMemberN{Value: tt.want}, av.(*types.AttributeValueMemberM).Value["n"])
		})
	}
}

func TestToAttributeValueNumberErrorNamesThePath(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"top level has no path", 1e126, "dynamodb: number 1e+126 is outside"},
		{"map", map[string]any{"a": 1e126}, `dynamodb: attribute "a": number 1e+126`},
		{"nested map", map[string]any{"a": map[string]any{"b": 1e126}}, `dynamodb: attribute "a.b": number`},
		{"list", map[string]any{"a": []any{1, 2, 1e126}}, `dynamodb: attribute "a[2]": number`},
		{"list in map in list", []any{map[string]any{"a": []any{1e126}}}, `dynamodb: attribute "[0].a[0]": number`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := toAttributeValue(tt.in)
			require.ErrorContains(t, err, tt.want)
			var ne *numberError
			require.ErrorAs(t, err, &ne) // the cause stays in the chain
		})
	}
}

func TestToAttributeValueBigIntNamesTheAttribute(t *testing.T) {
	big5 := big.NewInt(5)
	_, err := toAttributeValue(map[string]any{"a": []any{big5}})
	require.EqualError(t, err, `dynamodb: attribute "a[0]": cannot write value 5 (*big.Int)`)

	_, err = toAttributeValue(big5)
	require.EqualError(t, err, "dynamodb: cannot write value 5 (*big.Int)")
}

func TestKeyAVRejectsAnOutOfRangeNumber(t *testing.T) {
	k := KeyAttr{name: "id", typ: types.ScalarAttributeTypeN}
	av, err := keyAV(k, "9.9999999999999999999999999999999999999E+125")
	require.NoError(t, err)
	require.Equal(t, &types.AttributeValueMemberN{Value: "9.9999999999999999999999999999999999999E+125"}, av)

	_, err = keyAV(k, "1E+126")
	require.ErrorContains(t, err, `key attribute "id"`)
	require.ErrorContains(t, err, "is outside the DynamoDB range")
	var ne *numberError
	require.ErrorAs(t, err, &ne) // the cause stays in the chain

	_, err = keyAV(k, digits38+"9")
	require.ErrorContains(t, err, "has 39 significant digits")
}

func TestPutRejectsAnOutOfRangeNumberBeforeAnyWrite(t *testing.T) {
	fake := &fakeDDB{}
	s := fakeStore(t, fake)
	_, err := s.Put(context.Background(),
		[]query.Record{
			{Key: "1", Value: map[string]any{"id": 1}},
			{Key: "2", Value: map[string]any{"id": 2, "score": map[string]any{"v": 1e126}}},
		}, query.Upsert)
	require.ErrorContains(t, err, `attribute "score.v"`)
	require.Zero(t, fake.putCalls+fake.batchGetCalls)
}
