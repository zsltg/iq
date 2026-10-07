package couchbase

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanStatement(t *testing.T) {
	st := &Store{bucket: "b", scope: "s", coll: "c"}
	const head = "SELECT META(t).id AS k, t AS v FROM `b`.`s`.`c` t WHERE "
	const tail = " ORDER BY META(t).id LIMIT $page"
	require.Equal(t, head+"META(t).id > $after"+tail, st.scanStatement(""))
	require.Equal(t, head+"META(t).id > $after AND (x = $p)"+tail, st.scanStatement("x = $p"))
}
