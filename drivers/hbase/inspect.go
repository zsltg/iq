package hbase

import (
	"context"
	"sort"
)

// InspectTables lists the tables in the source's namespace (or the table's namespace
// when one is addressed), the HBase analogue of listing a keyspace's tables. HBase
// has no query language, so it is a driver method the CLI calls directly rather than a
// statement routed through Query. The names are returned sorted for stable output.
func (s *Store) InspectTables(ctx context.Context) (any, error) {
	namespace := defaultNamespace
	if s.table != "" {
		namespace, _ = splitTable(s.table)
	}
	names, err := s.listTableNames(ctx, namespace)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(names))
	for _, n := range names {
		tables = append(tables, string(n.Qualifier))
	}
	sort.Strings(tables)
	return map[string]any{"namespace": namespace, "tables": tables}, nil
}
