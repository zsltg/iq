package jqfmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExplainArgBuiltins(t *testing.T) {
	// Each builtin whose note renders its arguments gets one row with the right
	// arity. The wrong-arity rows pin the fallback: the static builtin table first,
	// then "call <name>".
	tests := []struct {
		src  string
		desc string
	}{
		{`select(.a)`, "keep inputs where .a"},
		{`map(.a)`, "apply .a to each element"},
		{`map_values(.a)`, "map each value with .a"},
		{`sort_by(.a)`, "sort by .a"},
		{`group_by(.a)`, "group by .a"},
		{`unique_by(.a)`, "unique by .a"},
		{`min_by(.a)`, "minimum by .a"},
		{`max_by(.a)`, "maximum by .a"},
		{`has("k")`, `has key "k"`},
		{`contains("x")`, `contains "x"`},
		{`del(.a)`, "delete .a"},
		{`limit(2; .[])`, "first 2 of .[]"},
		{`split(",")`, `split on ","`},
		{`join(",")`, `join with ","`},
		{`test("a")`, `regex test "a"`},
		{`match("a")`, `regex match "a"`},
		{`startswith("a")`, `starts with "a"`},
		{`endswith("a")`, `ends with "a"`},
		{`ltrimstr("a")`, `trim prefix "a"`},
		{`rtrimstr("a")`, `trim suffix "a"`},
		// The wrong arity for each arity class.
		{`select(.a; .b)`, "call select"},
		{`limit(1)`, "call limit"},
		{`limit(1; .a; .b)`, "call limit"},
		{`split`, "call split"},
		{`map`, "call map"},
		// A builtin that accepts more arguments renders only the first.
		{`split("a"; "g")`, `split on "a"`},
		{`test("a"; "g")`, `regex test "a"`},
		{`match("a"; "g")`, `regex match "a"`},
		// A builtin from the static table with an argument falls through to it.
		{`first(.a)`, "first element"},
		// An argument renders on one line, and its text is never a format verb.
		{`select(.a | .b)`, "keep inputs where .a | .b"},
		{`select(.a == "%s %d")`, `keep inputs where .a == "%s %d"`},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			stages := ExplainQuery(mustParse(t, tt.src), false)
			require.Len(t, stages, 1)
			require.Equal(t, tt.desc, stages[0].Desc)
		})
	}
}
