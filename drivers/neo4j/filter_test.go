package neo4j

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

func TestTranslatePushdown(t *testing.T) {
	tests := []struct {
		name       string
		node       predicate.Node
		wantWhere  string
		wantParams map[string]any
		wantNarrow bool
	}{
		{
			name:       "equality",
			node:       predicate.Eq{Path: []string{"name"}, Value: "Ada"},
			wantWhere:  "n[$f0] = $f1",
			wantParams: map[string]any{"f0": "name", "f1": "Ada"},
			wantNarrow: true,
		},
		{
			name:       "equality to null is IS NULL",
			node:       predicate.Eq{Path: []string{"name"}, Value: nil},
			wantWhere:  "n[$f0] IS NULL",
			wantParams: map[string]any{"f0": "name"},
			wantNarrow: true,
		},
		{
			name:       "exists",
			node:       predicate.Exists{Path: []string{"age"}},
			wantWhere:  "n[$f0] IS NOT NULL",
			wantParams: map[string]any{"f0": "age"},
			wantNarrow: true,
		},
		{
			name:       "not exists",
			node:       predicate.NotExists{Path: []string{"age"}},
			wantWhere:  "n[$f0] IS NULL",
			wantParams: map[string]any{"f0": "age"},
			wantNarrow: true,
		},
		{
			name:       "and drops the unpushable conjunct",
			node:       predicate.And{predicate.Eq{Path: []string{"a"}, Value: 1.0}, predicate.Cmp{Path: []string{"b"}, Op: predicate.Gt, Value: 2.0}},
			wantWhere:  "(n[$f0] = $f1)",
			wantParams: map[string]any{"f0": "a", "f1": 1.0},
			wantNarrow: true,
		},
		{
			name:       "or of two equalities",
			node:       predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1.0}, predicate.Eq{Path: []string{"b"}, Value: 2.0}},
			wantWhere:  "(n[$f0] = $f1 OR n[$f2] = $f3)",
			wantParams: map[string]any{"f0": "a", "f1": 1.0, "f2": "b", "f3": 2.0},
			wantNarrow: true,
		},
		{
			name:       "range does not narrow",
			node:       predicate.Cmp{Path: []string{"age"}, Op: predicate.Gt, Value: 30.0},
			wantNarrow: false,
		},
		{
			name:       "not-equal does not narrow",
			node:       predicate.Ne{Path: []string{"a"}, Value: 1.0},
			wantNarrow: false,
		},
		{
			name:       "nested path does not narrow",
			node:       predicate.Eq{Path: []string{"author", "name"}, Value: "Ada"},
			wantNarrow: false,
		},
		{
			name:       "reserved key does not narrow",
			node:       predicate.Eq{Path: []string{"_id"}, Value: "4:x:1"},
			wantNarrow: false,
		},
		{
			name:       "or with an unpushable branch does not narrow",
			node:       predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1.0}, predicate.Cmp{Path: []string{"b"}, Op: predicate.Lt, Value: 2.0}},
			wantNarrow: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &cypherBuilder{params: map[string]any{}, variable: "n"}
			where, narrow := b.translate(tt.node)
			assert.Equal(t, tt.wantNarrow, narrow)
			if !tt.wantNarrow {
				return
			}
			assert.Equal(t, tt.wantWhere, where)
			assert.Equal(t, tt.wantParams, b.params)
		})
	}
}

func TestTranslatePushdownRelationshipVariable(t *testing.T) {
	b := &cypherBuilder{params: map[string]any{}, variable: "r"}
	where, narrow := b.translate(predicate.Eq{Path: []string{"since"}, Value: 2019.0})
	require.True(t, narrow)
	assert.Equal(t, "r[$f0] = $f1", where)
}

func TestTranslatePushdownSkipsRelationshipEnvelopeKeys(t *testing.T) {
	for _, key := range []string{"_type", "_start", "_end"} {
		t.Run(key, func(t *testing.T) {
			b := &cypherBuilder{params: map[string]any{}, variable: "r"}
			_, narrow := b.translate(predicate.Eq{Path: []string{key}, Value: "x"})
			assert.False(t, narrow, "the computed %s envelope key is not a pushable property", key)
		})
	}
}
