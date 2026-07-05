package mongo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/render"
)

// scanBatch is the default page size for ScanBatches: how many documents to
// accumulate before handing a page to the caller, bounding streaming memory.
const scanBatch = 100

// errNoCollection is returned when a collection-scoped operation runs without a
// collection selected. It is a sentinel so the CLI can surface a clear hint.
var errNoCollection = errors.New("mongodb: no collection selected; pass --collection")

// Store adapts one MongoDB database to the query ports. The jq path is scoped to
// a single collection (the keyspace); the raw path runs database commands and
// needs no collection.
type Store struct {
	client     *mongo.Client
	db         *mongo.Database
	collection string
	pageSize   int
	decimal    numfmt.DecimalMode
}

// Open connects to the MongoDB server named by a mongodb:// URI and verifies the
// connection with a ping so a bad URI or unreachable server fails fast. The
// database is taken from the URI path; collection is the jq keyspace (may be
// empty for raw-only use). When trace is non-nil, each query command the driver
// issues is logged to it (the CLI's --verbose trace); handshake and auth commands
// are omitted, so no credential is written. dec chooses how Decimal128 values are
// presented to the filter.
func Open(ctx context.Context, uri, collection string, trace io.Writer, dec numfmt.DecimalMode) (*Store, error) {
	dbName, err := databaseFromURI(uri)
	if err != nil {
		return nil, err
	}
	clientOpts := options.Client().ApplyURI(uri)
	if trace != nil {
		clientOpts.SetMonitor(newCommandMonitor(trace))
	}
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}
	return &Store{
		client:     client,
		db:         client.Database(dbName),
		collection: collection,
		pageSize:   scanBatch,
		decimal:    dec,
	}, nil
}

// databaseFromURI extracts the database name from the path of a mongodb:// URI.
// A missing database is an error, because both the jq and raw paths run against
// a specific database.
func databaseFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse mongodb uri: %w", err)
	}
	db := strings.Trim(u.Path, "/")
	if db == "" || strings.Contains(db, "/") {
		return "", fmt.Errorf("mongodb uri must name a database, e.g. mongodb://host:27017/mydb")
	}
	return db, nil
}

// Get fetches the documents whose _id matches one of keys and returns them keyed
// by _id string. A key with no document maps to nil. Empty keys short-circuit
// with no round-trip.
func (s *Store) Get(ctx context.Context, keys []string) (map[string]any, error) {
	if s.collection == "" {
		return nil, errNoCollection
	}
	out := make(map[string]any, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	filter := bson.M{"_id": bson.M{"$in": idValues(keys)}}
	cur, err := s.db.Collection(s.collection).Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("mongodb find: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("mongodb decode: %w", err)
		}
		out[keyOf(doc["_id"])] = s.normalize(doc)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("mongodb cursor: %w", err)
	}
	// A requested key with no document reads as null, matching the KV contract.
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			out[k] = nil
		}
	}
	return out, nil
}

// ScanBatches streams the whole collection, handing the caller each page of
// {_id: document} as the cursor yields it. A streaming caller keeps only one
// page in memory. Bounded by ctx; stops at the first error from fn or the driver.
func (s *Store) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.scanWith(ctx, bson.M{}, fn)
}

// ScanFiltered streams only the documents matching pred, translating it to a
// native Mongo query so the server does the filtering. pred is a conservative
// superset (the caller re-runs the full jq), so returning extra documents is
// safe and returning too few is not — which is why only equality is pushed.
func (s *Store) ScanFiltered(ctx context.Context, pred predicate.Node, fn func(batch map[string]any) error) error {
	return s.scanWith(ctx, toFilter(pred), fn)
}

// scanWith runs a filtered cursor and pages the results, the shared body of
// ScanBatches and ScanFiltered.
func (s *Store) scanWith(ctx context.Context, filter bson.M, fn func(batch map[string]any) error) error {
	if s.collection == "" {
		return errNoCollection
	}
	opts := options.Find().SetBatchSize(int32(s.pageSize))
	cur, err := s.db.Collection(s.collection).Find(ctx, filter, opts)
	if err != nil {
		return fmt.Errorf("mongodb find: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	page := make(map[string]any, s.pageSize)
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return fmt.Errorf("mongodb decode: %w", err)
		}
		page[keyOf(doc["_id"])] = s.normalize(doc)
		if len(page) >= s.pageSize {
			if err := fn(page); err != nil {
				return err
			}
			page = make(map[string]any, s.pageSize)
		}
	}
	if err := cur.Err(); err != nil {
		return fmt.Errorf("mongodb cursor: %w", err)
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// EstimateCount returns a cheap, approximate count of the collection's documents
// from the server's cached metadata (estimatedDocumentCount), so a full scan can
// show a rough total without a second pass over the data. It is meaningful only
// for an unfiltered whole-collection scan — the engine requests it only then —
// and may be stale under concurrent writes, so the caller treats it as a hint.
// A missing collection is the same error the scan paths return.
func (s *Store) EstimateCount(ctx context.Context) (int64, error) {
	if s.collection == "" {
		return 0, errNoCollection
	}
	n, err := s.db.Collection(s.collection).EstimatedDocumentCount(ctx)
	if err != nil {
		return 0, fmt.Errorf("mongodb estimated count: %w", err)
	}
	return n, nil
}

// Query runs a raw database command. args must be a single JSON command document
// (for example `{"find":"books","filter":{...}}`); it is decoded order-preserving
// so the command name stays first, run with RunCommand, and the reply normalized.
func (s *Store) Query(ctx context.Context, args []string) (any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("mongodb raw expects one JSON command document")
	}
	var cmd bson.D
	if err := bson.UnmarshalExtJSON([]byte(args[0]), false, &cmd); err != nil {
		return nil, fmt.Errorf("parse mongodb command: %w", err)
	}
	var res bson.M
	if err := s.db.RunCommand(ctx, cmd).Decode(&res); err != nil {
		return nil, fmt.Errorf("mongodb command: %w", err)
	}
	return s.normalize(res), nil
}

// FormatRaw renders a raw command reply as indented JSON, the natural form for a
// document store, syntax-highlighted when colored is set.
func (s *Store) FormatRaw(v any, colored bool) string {
	out, err := render.JSON(v, colored)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return out
}

// Close disconnects the client.
func (s *Store) Close() error {
	return s.client.Disconnect(context.Background())
}
