package neo4j

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestParseURLReportsAnUnparsableURL(t *testing.T) {
	_, err := parseURL("neo4j://host/\x7f", "")
	require.ErrorContains(t, err, "parse neo4j url")
}

func TestOpenRejectsABadURLBeforeConnecting(t *testing.T) {
	// The deadline is short so that a driver that went on to connect would fail on the
	// deadline, not on the URL.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	st, err := Open(ctx, "mysql://localhost/", "", nil, numfmt.DecimalAuto)
	require.ErrorContains(t, err, "must use neo4j:// or bolt://")
	require.Nil(t, st)
}

func TestOpenKeepsTheConnectivityError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, err := Open(ctx, "bolt://127.0.0.1:1/", "", nil, numfmt.DecimalAuto)
	var connErr *neo4j.ConnectivityError
	require.ErrorAs(t, err, &connErr, "the connection failure keeps the driver error it wraps")
}

func TestPutRejectsARecordThatIsNotAnObjectBeforeAnyRequest(t *testing.T) {
	// The store has no driver: a bad record must return before a session opens.
	s := &Store{target: target{name: "Person", key: "id"}, keyBackedByConstraint: true}
	_, err := s.Put(context.Background(), []query.Record{{Key: "1", Value: "scalar"}}, query.Upsert)
	require.ErrorContains(t, err, "must be a JSON object")
}

func TestDeleteWithNoKeysMakesNoRequest(t *testing.T) {
	// The store has no driver: an empty key list must return before a session opens.
	s := &Store{target: target{name: "Person"}}
	stat, err := s.Delete(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, query.DeleteStat{}, stat)
}

func TestRecordPropsKeepsEveryPropertyAmongReservedKeys(t *testing.T) {
	// Go visits map keys in random order. With many plain keys and several reserved
	// ones, a loop that stopped at the first reserved key would lose a plain key on
	// nearly every run.
	value := map[string]any{"id": "7", "_id": "4:x:1", "_labels": []any{"Person"}, "_type": "KNOWS", "_start": "a", "_end": "b"}
	want := map[string]any{"id": "7"}
	for i := range 40 {
		name := "p" + strconv.Itoa(i)
		value[name] = i
		want[name] = i
	}
	props, _, err := recordProps(query.Record{Key: "7", Value: value}, "id")
	require.NoError(t, err)
	require.Equal(t, want, props)
}

// TestIntegrationAMissingDatabaseFailsEveryOperation pins that each operation reports a
// server failure at the start of a statement with its own context, wrapping the driver
// error, instead of going on with a result that does not exist.
func TestIntegrationAMissingDatabaseFailsEveryOperation(t *testing.T) {
	ctx := integrationOrSkip(t)
	st, err := Open(ctx, testURL()+"?database=it_no_such_db", "", nil, numfmt.DecimalAuto)
	require.NoError(t, err, "a keyless source reads no metadata at Open")
	t.Cleanup(func() { _ = st.Close() })
	// Aim the store at a keyed label without the Open-time constraint check, which
	// would fail on the missing database first.
	st.target = target{name: "ItNoDb", key: "id"}
	st.keyBackedByConstraint = true

	tests := []struct {
		name string
		want string
		run  func() error
	}{
		{"get", "neo4j run", func() error { _, err := st.Get(ctx, []string{"1"}); return err }},
		{"scan", "neo4j run", func() error { return st.ScanBatches(ctx, func(map[string]any) error { return nil }) }},
		{"count", "neo4j run", func() error { _, err := st.EstimateCount(ctx); return err }},
		{"query", "neo4j run", func() error { _, err := st.Query(ctx, []string{"RETURN 1"}); return err }},
		{"constraint check", "neo4j run", func() error { _, err := st.keyConstraintExists(ctx); return err }},
		{"put", "neo4j run", func() error {
			_, err := st.Put(ctx, []query.Record{{Key: "1", Value: map[string]any{"id": "1"}}}, query.Upsert)
			return err
		}},
		{"clear", "neo4j run", func() error { return st.Clear(ctx) }},
		{"delete", "neo4j run", func() error { _, err := st.Delete(ctx, []string{"1"}); return err }},
		{"inspect databases", "neo4j list databases", func() error { _, err := st.InspectDatabases(ctx); return err }},
		{"inspect labels", "neo4j list labels", func() error { _, err := st.InspectLabels(ctx); return err }},
		{"inspect relationship types", "neo4j list relationship types", func() error { _, err := st.InspectRelationshipTypes(ctx); return err }},
		{"inspect constraints", "neo4j list constraints", func() error { _, err := st.InspectConstraints(ctx); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			require.ErrorContains(t, err, tt.want)
			var dbErr *neo4j.Neo4jError
			require.ErrorAs(t, err, &dbErr, "the failure keeps the driver error it wraps")
		})
	}
}

// TestIntegrationAStreamingFailureIsReported pins that a failure the server raises
// while rows stream is reported with the context of the operation, not dropped. The
// key property of the label is a list, which Cypher cannot render as a string.
func TestIntegrationAStreamingFailureIsReported(t *testing.T) {
	ctx := integrationOrSkip(t)
	admin := openStore(t, ctx, "", "")
	exec(t, ctx, admin, "MATCH (n:ItListKey) DETACH DELETE n")
	exec(t, ctx, admin, "CREATE (:ItListKey {id: ['a','b']})")
	st := openStore(t, ctx, "ItListKey", "id")

	tests := []struct {
		name string
		want string
		run  func() error
	}{
		{"get", "neo4j get", func() error { _, err := st.Get(ctx, []string{"a"}); return err }},
		{"delete", "neo4j delete resolve", func() error { _, err := st.Delete(ctx, []string{"a"}); return err }},
		{"raw query", "neo4j query", func() error {
			_, err := st.Query(ctx, []string{"UNWIND [1, 0] AS x RETURN 1/x AS v"})
			return err
		}},
		{"inspect column", "by zero", func() error {
			_, err := st.readColumn(ctx, "UNWIND [1, 0] AS x RETURN 1/x AS v")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			require.ErrorContains(t, err, tt.want)
			var dbErr *neo4j.Neo4jError
			require.ErrorAs(t, err, &dbErr, "the failure keeps the driver error it wraps")
		})
	}

	// The list-key node is still there: a failed delete removes nothing.
	rows, err := admin.Query(ctx, []string{"MATCH (n:ItListKey) RETURN count(n) AS c"})
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{"c": 1}}, rows)
}
