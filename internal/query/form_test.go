package query_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/zsltg/iq/internal/query"
)

func TestFormNames(t *testing.T) {
	tests := []struct {
		form     query.Form
		name     string
		dumpName string
	}{
		{query.FormJSON, "json", ""},
		{query.FormDynamoJSON, "dynamodb-json", "dynamodb-json"},
		{query.FormMongoEJSON, "mongo-ejson", "mongo-ejson"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.name, tt.form.String())
			require.Equal(t, tt.dumpName, tt.form.DumpName())
			got, err := query.ParseForm(tt.name)
			require.NoError(t, err)
			require.Equal(t, tt.form, got)
		})
	}
}

func TestFormZeroValueIsJSON(t *testing.T) {
	require.Equal(t, query.FormJSON, query.Record{}.Form)
}

func TestParseForm(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    query.Form
		wantErr string
	}{
		{"empty is json", "", query.FormJSON, ""},
		{"unknown", "bson", query.FormJSON, `unknown record form "bson"`},
		{"case matters", "DynamoDB-JSON", query.FormJSON, `unknown record form "DynamoDB-JSON"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := query.ParseForm(tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormStringOutOfRange(t *testing.T) {
	require.Equal(t, "form(99)", query.Form(99).String())
	require.Equal(t, "form(-1)", query.Form(-1).String())
	// The first index past the names table must not panic.
	require.Equal(t, "form(3)", query.Form(3).String())
}

func TestCheckForms(t *testing.T) {
	rec := func(key string, f query.Form) query.Record { return query.Record{Key: key, Form: f} }
	tests := []struct {
		name    string
		own     query.Form
		batch   []query.Record
		wantErr string
	}{
		{"empty batch", query.FormJSON, nil, ""},
		{"json passes a plain driver", query.FormJSON, []query.Record{rec("a", query.FormJSON)}, ""},
		{"json passes a native driver", query.FormDynamoJSON, []query.Record{rec("a", query.FormJSON)}, ""},
		{"own form passes", query.FormDynamoJSON, []query.Record{rec("a", query.FormDynamoJSON)}, ""},
		{
			"plain driver refuses a native form", query.FormJSON,
			[]query.Record{rec("a", query.FormJSON), rec("k1", query.FormDynamoJSON)},
			`record "k1" has form "dynamodb-json", but this destination accepts only form "json"`,
		},
		{
			"native driver refuses another native form", query.FormDynamoJSON,
			[]query.Record{rec("k2", query.FormMongoEJSON)},
			`record "k2" has form "mongo-ejson", but this destination accepts only forms "json" and "dynamodb-json"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := query.CheckForms("demo", tt.own, tt.batch)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "demo: "+tt.wantErr)
		})
	}
}

func TestWriteJSONLOmitsTheJSONForm(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, query.WriteJSONL(&buf, []query.Record{
		{Key: "a", Type: "string", Value: "x"},
		{Key: "b", Type: "item", Form: query.FormDynamoJSON, Value: map[string]any{"n": map[string]any{"N": "1.50"}}},
	}))
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	require.JSONEq(t, `{"key":"a","type":"string","value":"x"}`, lines[0])
	require.NotContains(t, lines[0], "form")
	require.JSONEq(t, `{"key":"b","type":"item","form":"dynamodb-json","value":{"n":{"N":"1.50"}}}`, lines[1])
}

func TestTypedDumpFormRoundTrip(t *testing.T) {
	in := []query.Record{
		{Key: "a", Type: "string", Value: "x"},
		{Key: "b", Type: "item", Form: query.FormDynamoJSON, Value: map[string]any{
			"n": map[string]any{"N": "12345678901234567890.5"},
			"s": map[string]any{"SS": []any{"p", "q"}},
		}},
		{Key: "c", Type: "document", Form: query.FormMongoEJSON, Value: map[string]any{"_id": map[string]any{"$numberInt": "7"}}},
	}
	var buf bytes.Buffer
	require.NoError(t, query.WriteJSONL(&buf, in))
	got := drainSource(t, query.JSONLSource(&buf, 10, false))
	require.Equal(t, in, got)
}

func TestTypedSourcesReadTheForm(t *testing.T) {
	const line = `{"key":"b","type":"item","form":"dynamodb-json","value":{"n":{"N":"1.50"}}}`
	want := []query.Record{{Key: "b", Type: "item", Form: query.FormDynamoJSON, Value: map[string]any{"n": map[string]any{"N": "1.50"}}}}
	tests := []struct {
		name string
		src  query.RecordSource
	}{
		{"jsonl", query.JSONLSource(strings.NewReader(line), 10, false)},
		{"json stream", query.JSONSource(strings.NewReader(line), 10, false)},
		{"json array", query.JSONSource(strings.NewReader("["+line+"]"), 10, false)},
		{"yaml", query.YAMLSource(strings.NewReader(yamlOf(t, line)), 10, false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, want, drainSource(t, tt.src))
		})
	}
}

// yamlOf renders a typed JSON line as a YAML document.
func yamlOf(t *testing.T, line string) string {
	t.Helper()
	var v any
	require.NoError(t, yaml.Unmarshal([]byte(line), &v))
	out, err := yaml.Marshal(v)
	require.NoError(t, err)
	return string(out)
}

func TestTypedSourcesRefuseAnUnknownForm(t *testing.T) {
	const line = `{"key":"b","type":"item","form":"bson-v9","value":{}}`
	tests := []struct {
		name string
		src  query.RecordSource
	}{
		{"jsonl", query.JSONLSource(strings.NewReader(line), 10, false)},
		{"json array", query.JSONSource(strings.NewReader("["+line+"]"), 10, false)},
		{"yaml", query.YAMLSource(strings.NewReader(yamlOf(t, line)), 10, false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.src(context.Background(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, `record "b": unknown record form "bson-v9"`)
			require.ErrorIs(t, err, query.ErrUnknownForm)
		})
	}
}

func TestPlainSourceIgnoresAFormField(t *testing.T) {
	got := drainSource(t, query.JSONLSource(strings.NewReader(`{"form":"bson-v9"}`), 10, true))
	require.Equal(t, []query.Record{{Value: map[string]any{"form": "bson-v9"}}}, got)
}

func TestTransformKeepsTheForm(t *testing.T) {
	tf, err := query.NewTransform(query.TransformOptions{Filter: "."})
	require.NoError(t, err)
	out, err := tf(query.Record{Key: "k", Form: query.FormDynamoJSON, Value: map[string]any{"a": 1}})
	require.NoError(t, err)
	require.Equal(t, query.FormDynamoJSON, out[0].Form)
}
