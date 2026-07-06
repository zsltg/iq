package cassandra

import (
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
)

func TestParseColumnTypes(t *testing.T) {
	got, err := ParseColumnTypes("id=uuid, age = int ,name=text,active=bool")
	require.NoError(t, err)
	require.Equal(t, map[string]gocql.Type{
		"id":     gocql.TypeUUID,
		"age":    gocql.TypeInt,
		"name":   gocql.TypeText,
		"active": gocql.TypeBoolean,
	}, got)

	empty, err := ParseColumnTypes("")
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestParseColumnTypesErrors(t *testing.T) {
	tests := []struct {
		name string
		hint string
		msg  string
	}{
		{"no equals", "id", "want column=cqltype"},
		{"empty column", "=int", "empty column name"},
		{"unknown type", "id=frobnicate", "unsupported ?types="},
		{"collection type", "tags=list", "unsupported ?types="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseColumnTypes(tc.hint)
			require.ErrorContains(t, err, tc.msg)
		})
	}
}

func TestBindStringDelegates(t *testing.T) {
	v, err := BindString(gocql.TypeInt, "42")
	require.NoError(t, err)
	require.Equal(t, 42, v)

	_, err = BindString(gocql.TypeInt, "nope")
	require.ErrorContains(t, err, "not an integer")
}

func TestKeyOfColumns(t *testing.T) {
	row := map[string]any{"part": "p1", "clust": 5, "v": "x"}
	require.Equal(t, "p1", KeyOfColumns([]string{"part"}, row))
	require.Equal(t, `["p1","5"]`, KeyOfColumns([]string{"part", "clust"}, row))
}
