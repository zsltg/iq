// Package hbase adapts an Apache HBase table to the query ports. A table is
// modelled as Map[rowkey, row]: the row key, rendered to a string, is the key; the
// row — every cell grouped as a nested {family: {qualifier: value}} object — is the
// value. The jq filter runs client-side over rows fetched by row key or streamed
// from the table, so the semantics match every other backend behind the KV port.
//
// HBase stores no types: a cell is raw bytes. A value is presented honestly by
// default — valid UTF-8 as text, else base64 — and a caller who knows a column's
// encoding declares it in the URL (?types=cf:q=long) for an exact, reversible
// mapping via the standard HBase Bytes layout. The row key is likewise text-or-base64
// unless ?keytype= declares it.
//
// HBase has no query language, so the raw path (iq exec) is a small, safe verb set —
// get, scan, count, put, delete — mapped straight onto RPC, never a built query
// string, so it is injection-safe by construction.
package hbase

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/filter"
	"github.com/tsuna/gohbase/hrpc"
	"github.com/tsuna/gohbase/pb"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the page size for ScanBatches and TypedScan: how many rows to
// accumulate before handing a page to the caller, bounding streaming memory.
const scanBatch = 100

// defaultNamespace is the HBase namespace an unqualified table name belongs to.
const defaultNamespace = "default"

// errNoTable is returned when a table-scoped operation runs without a table
// selected. It is a sentinel so the CLI can surface a clear hint.
var errNoTable = errors.New("hbase: no table selected; address it as handle.table or set ?table= in the source url")

// hbaseClient is the subset of gohbase.Client the Store calls. The concrete client
// satisfies it; abstracting it lets the read, write, and scan paths be unit-tested
// with a fake, without a live region server.
type hbaseClient interface {
	Get(g *hrpc.Get) (*hrpc.Result, error)
	Put(p *hrpc.Mutate) (*hrpc.Result, error)
	Delete(d *hrpc.Mutate) (*hrpc.Result, error)
	CheckAndPut(p *hrpc.Mutate, family, qualifier string, expectedValue []byte) (bool, error)
	Scan(s *hrpc.Scan) hrpc.Scanner
	Close()
}

// hbaseAdmin is the subset of gohbase.AdminClient the Store calls, for the
// reachability probe, table listing, and drop.
type hbaseAdmin interface {
	ClusterStatus() (*pb.ClusterStatus, error)
	ListTableNames(t *hrpc.ListTableNames) ([]*pb.TableName, error)
	DisableTable(t *hrpc.DisableTable) error
	DeleteTable(t *hrpc.DeleteTable) error
}

// Store adapts one HBase table to the query ports. The jq and write paths are scoped
// to a single table (the keyspace of the KV model); the raw path names its own table
// per command and needs no default.
type Store struct {
	client     hbaseClient
	admin      hbaseAdmin
	table      string
	types      typeMap
	rowkeyType colType
	pageSize   int
	trace      io.Writer
}

// Open connects to the HBase cluster named by an hbase:// URL and verifies the
// connection with a bounded ClusterStatus probe (gohbase connects lazily, so this is
// what fails fast on an unreachable ZooKeeper quorum or master). The table (the jq
// keyspace, may be empty for raw-only or list-only use) is the address override when
// non-empty, else the URL's ?table= default, and its existence is checked once at
// connect. When trace is non-nil, each executed operation is logged to it (the CLI's
// --verbose trace) with cell values redacted. dec is unused: HBase has no decimal
// type, so declared doubles present as plain float64.
func Open(ctx context.Context, rawURL, address string, trace io.Writer, _ numfmt.DecimalMode) (*Store, error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return nil, err
	}
	// gohbase logs region discovery at INFO to its own logger; discard it so the
	// backend never writes to the CLI's stderr. iq's own tracing is the --verbose
	// trace (trace writer), not the driver's internal chatter.
	opts := []gohbase.Option{
		gohbase.ZookeeperRoot(cc.znode),
		gohbase.Logger(slog.New(slog.DiscardHandler)),
	}
	// Bound ZooKeeper discovery and per-region RPC by the caller's deadline, so a
	// stalled cluster never hangs the command (an infinite wait is forbidden).
	if dl, ok := ctx.Deadline(); ok {
		d := time.Until(dl)
		opts = append(opts, gohbase.ZookeeperTimeout(d), gohbase.RegionLookupTimeout(d), gohbase.RegionReadTimeout(d))
	}
	st := &Store{
		client:     gohbase.NewClient(cc.zkquorum, opts...),
		admin:      gohbase.NewAdminClient(cc.zkquorum, opts...),
		table:      cc.table,
		types:      cc.types,
		rowkeyType: cc.rowkeyType,
		pageSize:   scanBatch,
		trace:      trace,
	}
	if _, err := st.admin.ClusterStatus(); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("connect hbase: %w", err)
	}
	if cc.table != "" {
		if err := st.verifyTable(ctx, cc.table); err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	return st, nil
}

// Target returns the [namespace:]table an hbase:// source addresses: the address
// override wins over the URL's ?table= default. It lets the CLI show and reason about
// a source's table without duplicating the URL parsing.
func Target(rawURL, address string) (table string, err error) {
	cc, err := parseURL(rawURL, address)
	if err != nil {
		return "", err
	}
	return cc.table, nil
}

// verifyTable confirms the selected table exists, so a bad table fails fast at
// connect with a clear message rather than at the first query. It matches on the
// exact namespace and qualifier, listing only the candidate namespace.
func (s *Store) verifyTable(ctx context.Context, table string) error {
	ns, qualifier := splitTable(table)
	names, err := s.listTableNames(ctx, ns)
	if err != nil {
		return err
	}
	for _, n := range names {
		if string(n.Qualifier) == qualifier {
			return nil
		}
	}
	return fmt.Errorf("hbase: table %q not found in namespace %q", qualifier, ns)
}

// listTableNames lists the tables in a namespace, the shared body of verifyTable and
// the inspect tables read.
func (s *Store) listTableNames(ctx context.Context, namespace string) ([]*pb.TableName, error) {
	req, err := hrpc.NewListTableNames(ctx, hrpc.ListNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("hbase list tables: %w", err)
	}
	names, err := s.admin.ListTableNames(req)
	if err != nil {
		return nil, fmt.Errorf("hbase list tables: %w", err)
	}
	return names, nil
}

// splitTable splits a [namespace:]table into its namespace and qualifier, defaulting
// the namespace to "default" when unqualified.
func splitTable(table string) (namespace, qualifier string) {
	if ns, q, ok := strings.Cut(table, ":"); ok {
		return ns, q
	}
	return defaultNamespace, table
}

// Get fetches the rows whose key is one of keys and returns them keyed by the same
// string key. A key with no row is absent from the map. Each key is a bounded point Get (HBase's
// efficient single-row read); empty keys short-circuit with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.table == "" {
		return nil, errNoTable
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	s.traceOp("get %s rows=%d", s.table, len(keys))
	for _, k := range keys {
		rk, err := encodeRowKey(s.rowkeyType, k)
		if err != nil {
			return nil, err
		}
		req, err := hrpc.NewGet(ctx, []byte(s.table), rk, hrpc.MaxVersions(1))
		if err != nil {
			return nil, fmt.Errorf("hbase get: %w", err)
		}
		res, err := s.client.Get(req)
		if err != nil {
			return nil, fmt.Errorf("hbase get: %w", err)
		}
		// A key with no cells is left out, matching the KV contract.
		if len(res.Cells) == 0 {
			continue
		}
		out[k] = rowFromCells(res.Cells, s.types)
	}
	return out, nil
}

// ScanBatches streams the whole table, handing the caller each page of {key: row} as
// the region-server cursor yields it, so a streaming caller keeps only one page in
// memory. Bounded by ctx; stops at the first error from fn or the region server.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	if s.table == "" {
		return errNoTable
	}
	s.traceOp("scan %s", s.table)
	req, err := hrpc.NewScanStr(ctx, s.table, hrpc.MaxVersions(1))
	if err != nil {
		return fmt.Errorf("hbase scan: %w", err)
	}
	return s.pageScan(req, fn)
}

// pageScan drives a scanner, handing the caller a page of {key: row} every pageSize
// rows, the shared body of ScanBatches and ScanFiltered. The scanner fetches
// successive server batches as it advances; the pageSize batching here is the
// streaming contract to fn.
func (s *Store) pageScan(req *hrpc.Scan, fn func(batch map[string]any) error) error {
	scanner := s.client.Scan(req)
	page := make(map[string]any, s.pageSize)
	for {
		res, err := scanner.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = scanner.Close()
			return fmt.Errorf("hbase scan: %w", err)
		}
		if len(res.Cells) == 0 {
			continue
		}
		key := rowKeyString(s.rowkeyType, res.Cells[0].Row)
		page[key] = rowFromCells(res.Cells, s.types)
		if len(page) >= s.pageSize {
			if err := fn(page); err != nil {
				_ = scanner.Close()
				return err
			}
			page = make(map[string]any, s.pageSize)
		}
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// Query runs a raw HBase verb: HBase has no query language, so this is a small,
// fixed set of shell-style commands mapped straight onto RPC — get, scan, count
// (reads), and put, delete (writes) — each naming its own table. Values are encoded
// through the same typed contract as the structured write path and passed as RPC
// arguments, never concatenated into a query, so the surface is injection-safe.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("hbase: empty command")
	}
	verb := strings.ToLower(args[0])
	s.traceOp("exec %s", verb)
	switch verb {
	case "get":
		return s.execGet(ctx, args[1:])
	case "scan":
		return s.execScan(ctx, args[1:])
	case "count":
		return s.execCount(ctx, args[1:])
	case "put":
		return s.execPut(ctx, args[1:])
	case "delete":
		return s.execDelete(ctx, args[1:])
	default:
		return nil, fmt.Errorf("hbase: unknown command %q; want get, scan, count, put, or delete", verb)
	}
}

// execGet runs `get <table> <rowkey>`, returning the row as a nested object or null.
func (s *Store) execGet(ctx context.Context, args []string) (any, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("hbase: get needs <table> <rowkey>")
	}
	table, key := args[0], args[1]
	rk, err := encodeRowKey(s.rowkeyType, key)
	if err != nil {
		return nil, err
	}
	req, err := hrpc.NewGet(ctx, []byte(table), rk, hrpc.MaxVersions(1))
	if err != nil {
		return nil, fmt.Errorf("hbase get: %w", err)
	}
	res, err := s.client.Get(req)
	if err != nil {
		return nil, fmt.Errorf("hbase get: %w", err)
	}
	if len(res.Cells) == 0 {
		return nil, nil
	}
	return rowFromCells(res.Cells, s.types), nil
}

// execScan runs `scan <table> [limit]`, returning up to limit rows as {rowkey: row}.
// An absent or non-positive limit scans the whole table.
func (s *Store) execScan(ctx context.Context, args []string) (any, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("hbase: scan needs <table> [limit]")
	}
	limit := 0
	if len(args) == 2 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, fmt.Errorf("hbase: scan limit %q is not a number", args[1])
		}
		limit = n
	}
	req, err := hrpc.NewScanStr(ctx, args[0], hrpc.MaxVersions(1))
	if err != nil {
		return nil, fmt.Errorf("hbase scan: %w", err)
	}
	rows := map[string]any{}
	scanner := s.client.Scan(req)
	for {
		res, err := scanner.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = scanner.Close()
			return nil, fmt.Errorf("hbase scan: %w", err)
		}
		if len(res.Cells) == 0 {
			continue
		}
		rows[rowKeyString(s.rowkeyType, res.Cells[0].Row)] = rowFromCells(res.Cells, s.types)
		if limit > 0 && len(rows) >= limit {
			_ = scanner.Close()
			break
		}
	}
	return rows, nil
}

// execCount runs `count <table>`, returning the number of rows. It scans key-only
// (one cell per row) so it never ships cell values just to count.
func (s *Store) execCount(ctx context.Context, args []string) (any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("hbase: count needs <table>")
	}
	req, err := hrpc.NewScanStr(ctx, args[0], hrpc.Filters(filter.NewKeyOnlyFilter(false)))
	if err != nil {
		return nil, fmt.Errorf("hbase count: %w", err)
	}
	count := 0
	scanner := s.client.Scan(req)
	for {
		res, err := scanner.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = scanner.Close()
			return nil, fmt.Errorf("hbase count: %w", err)
		}
		if len(res.Cells) > 0 {
			count++
		}
	}
	return count, nil
}

// execPut runs `put <table> <rowkey> <family:qualifier> <value>`, writing one cell.
// The value is encoded through the column's declared type (UTF-8 by default).
func (s *Store) execPut(ctx context.Context, args []string) (any, error) {
	if len(args) != 4 {
		return nil, fmt.Errorf("hbase: put needs <table> <rowkey> <family:qualifier> <value>")
	}
	table, key, column, value := args[0], args[1], args[2], args[3]
	family, qualifier, ok := strings.Cut(column, ":")
	if !ok || family == "" || qualifier == "" {
		return nil, fmt.Errorf("hbase: column %q must be family:qualifier", column)
	}
	rk, err := encodeRowKey(s.rowkeyType, key)
	if err != nil {
		return nil, err
	}
	cell, err := encodeCell(colTypeFor(s.types, family, qualifier), rawValue(colTypeFor(s.types, family, qualifier), value))
	if err != nil {
		return nil, err
	}
	values := map[string]map[string][]byte{family: {qualifier: cell}}
	req, err := hrpc.NewPut(ctx, []byte(table), rk, values)
	if err != nil {
		return nil, fmt.Errorf("hbase put: %w", err)
	}
	if _, err := s.client.Put(req); err != nil {
		return nil, fmt.Errorf("hbase put: %w", err)
	}
	return map[string]any{"ok": true}, nil
}

// execDelete runs `delete <table> <rowkey> [family:qualifier]`, removing one cell or
// (with no column) the whole row.
func (s *Store) execDelete(ctx context.Context, args []string) (any, error) {
	if len(args) < 2 || len(args) > 3 {
		return nil, fmt.Errorf("hbase: delete needs <table> <rowkey> [family:qualifier]")
	}
	table, key := args[0], args[1]
	rk, err := encodeRowKey(s.rowkeyType, key)
	if err != nil {
		return nil, err
	}
	var values map[string]map[string][]byte
	if len(args) == 3 {
		family, qualifier, ok := strings.Cut(args[2], ":")
		if !ok || family == "" || qualifier == "" {
			return nil, fmt.Errorf("hbase: column %q must be family:qualifier", args[2])
		}
		values = map[string]map[string][]byte{family: {qualifier: nil}}
	}
	req, err := hrpc.NewDel(ctx, []byte(table), rk, values)
	if err != nil {
		return nil, fmt.Errorf("hbase delete: %w", err)
	}
	if _, err := s.client.Delete(req); err != nil {
		return nil, fmt.Errorf("hbase delete: %w", err)
	}
	return map[string]any{"ok": true}, nil
}

// rawValue coerces a raw CLI string argument to the value shape encodeCell expects
// for a column's type: a numeric type parses the string, a bool reads "true", and
// text/bytes take the string as-is (encodeCell validates base64 for bytes).
func rawValue(t colType, s string) any {
	switch t {
	case ctBool:
		return s == "true"
	default:
		return s
	}
}

// FormatRaw renders a raw reply as indented JSON, the natural form for normalized
// rows, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close releases the client and admin connection pools.
func (s *Store) Close() error {
	s.client.Close()
	return nil
}

// traceOp writes one line to the --verbose trace when enabled. Only the operation
// and table are written; cell values are never traced, since they may carry data.
func (s *Store) traceOp(format string, args ...any) {
	if s.trace == nil {
		return
	}
	_, _ = fmt.Fprintf(s.trace, "hbase> "+format+"\n", args...)
}
