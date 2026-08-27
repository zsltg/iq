package query

import (
	"encoding/json"
	"fmt"
	"math/rand"
)

// docShape names the three synthetic document sizes the benchmarks sweep, so a
// benchmark can report cost against a realistic spread of payload sizes rather
// than one arbitrary document.
type docShape int

const (
	// shapeFlat is a small flat object, roughly 300 bytes of scalar fields.
	shapeFlat docShape = iota
	// shapeNested is a medium object with nested sub-objects, roughly 3 KB.
	shapeNested
	// shapeArray is a large object carrying arrays of sub-objects, roughly 30 KB.
	shapeArray
)

// String names the shape for a sub-benchmark label.
func (s docShape) String() string {
	switch s {
	case shapeFlat:
		return "flat300B"
	case shapeNested:
		return "nested3KB"
	case shapeArray:
		return "array30KB"
	default:
		return "unknown"
	}
}

// benchShapes is the shape sweep every size-parameterized benchmark ranges over.
var benchShapes = []docShape{shapeFlat, shapeNested, shapeArray}

// benchRates is the match-rate sweep for the selectivity-sensitive benchmarks:
// the fraction of documents that carry "status":"active".
var benchRates = []float64{0.01, 0.10, 0.50, 1.00}

// benchDocCount picks a document count per shape so the total corpus stays a few
// megabytes regardless of shape, keeping every benchmark's working set comparable.
func benchDocCount(shape docShape) int {
	switch shape {
	case shapeFlat:
		return 2000
	case shapeNested:
		return 500
	case shapeArray:
		return 100
	default:
		return 100
	}
}

// genCorpus builds a deterministic slice of documents for a shape at a match rate.
// The seed is fixed so a run is repeatable — no wall-clock or global-rand
// nondeterminism — and matches are spread evenly across the slice (not clustered
// at the front) via a floor-step so the count is exactly round(n*rate). Each
// document is a plain map[string]any of Go-native scalars, the same shape a decode
// followed by numfmt normalization produces.
func genCorpus(shape docShape, n int, rate float64) []map[string]any {
	rng := rand.New(rand.NewSource(0xC0FFEE ^ int64(shape)<<8)) //nolint:gosec // deterministic corpus, not security.
	docs := make([]map[string]any, n)
	matched := 0
	for i := range docs {
		// Even spread: the floor of i*rate steps up exactly round(n*rate) times.
		want := int(float64(i+1) * rate)
		match := want > matched
		if match {
			matched++
		}
		docs[i] = genDoc(rng, shape, match)
	}
	return docs
}

// genDoc builds one document of the given shape. It always carries a top-level
// "status" field ("active" when match, else "inactive") so the pre-filter can
// extract it from raw bytes, plus a spread of scalar, nested, and array fields to
// exercise the number-conversion and decode paths.
func genDoc(rng *rand.Rand, shape docShape, match bool) map[string]any {
	status := "inactive"
	if match {
		status = "active"
	}
	doc := map[string]any{
		"id":      rng.Intn(1_000_000),
		"status":  status,
		"name":    randString(rng, 12),
		"email":   randString(rng, 8) + "@example.com",
		"age":     rng.Intn(90),
		"score":   rng.Float64() * 100,
		"active":  rng.Intn(2) == 0,
		"created": fmt.Sprintf("2026-%02d-%02dT%02d:00:00Z", rng.Intn(12)+1, rng.Intn(28)+1, rng.Intn(24)),
		"balance": rng.Int63n(1_000_000_000),
	}
	if shape == shapeFlat {
		return doc
	}
	doc["profile"] = map[string]any{
		"bio":      randString(rng, 40),
		"location": randString(rng, 16),
		"website":  "https://" + randString(rng, 10) + ".example.com",
		"settings": map[string]any{
			"theme":         randString(rng, 6),
			"notifications": rng.Intn(2) == 0,
			"language":      randString(rng, 4),
			"timezone":      randString(rng, 8),
		},
	}
	doc["metrics"] = map[string]any{
		"visits":    rng.Intn(100_000),
		"purchases": rng.Intn(500),
		"revenue":   rng.Float64() * 10_000,
		"rating":    rng.Float64() * 5,
	}
	if shape == shapeNested {
		return doc
	}
	events := make([]any, 80)
	for i := range events {
		events[i] = map[string]any{
			"ts":     fmt.Sprintf("2026-01-%02dT%02d:%02d:00Z", rng.Intn(28)+1, rng.Intn(24), rng.Intn(60)),
			"kind":   randString(rng, 10),
			"amount": rng.Float64() * 1000,
			"count":  rng.Intn(1000),
			"note":   randString(rng, 24),
		}
	}
	doc["events"] = events
	tags := make([]any, 40)
	for i := range tags {
		tags[i] = randString(rng, 8)
	}
	doc["tags"] = tags
	return doc
}

// randAlphabet is the alphabet randString draws from; it stays ASCII so encoded
// sizes are predictable.
const randAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randString returns a deterministic pseudo-random string of length n.
func randString(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randAlphabet[rng.Intn(len(randAlphabet))]
	}
	return string(b)
}

// encodeJSONLTyped renders the corpus as a typed JSONL dump ({key,type,value} per
// line), the input a typed-mode JSONLSource consumes.
func encodeJSONLTyped(docs []map[string]any) []byte {
	var buf []byte
	for i, d := range docs {
		rec := dumpRecord{Key: fmt.Sprintf("k%d", i), Type: "document", Value: d}
		line, err := json.Marshal(rec)
		if err != nil {
			panic(err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return buf
}

// encodeJSONLPlain renders the corpus as plain JSONL: one whole document value per
// line, the input a plain-mode JSONLSource consumes.
func encodeJSONLPlain(docs []map[string]any) []byte {
	var buf []byte
	for _, d := range docs {
		line, err := json.Marshal(d)
		if err != nil {
			panic(err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return buf
}

// encodeJSONArrayTyped renders the corpus as a single top-level JSON array of typed
// {key,type,value} records, the input the array branch of JSONSource consumes.
func encodeJSONArrayTyped(docs []map[string]any) []byte {
	recs := make([]dumpRecord, len(docs))
	for i, d := range docs {
		recs[i] = dumpRecord{Key: fmt.Sprintf("k%d", i), Type: "document", Value: d}
	}
	buf, err := json.Marshal(recs)
	if err != nil {
		panic(err)
	}
	return buf
}

// rawDocs renders each document to its own JSON byte slice, the raw-bytes input
// the pre-filter prototype extracts from and, on a match, fully decodes.
func rawDocs(docs []map[string]any) [][]byte {
	out := make([][]byte, len(docs))
	for i, d := range docs {
		b, err := json.Marshal(d)
		if err != nil {
			panic(err)
		}
		out[i] = b
	}
	return out
}

// corpusRecords wraps the corpus as Records for the WriteJSONL export benchmark.
func corpusRecords(docs []map[string]any) []Record {
	recs := make([]Record, len(docs))
	for i, d := range docs {
		recs[i] = Record{Key: fmt.Sprintf("k%d", i), Type: "document", Value: d}
	}
	return recs
}

// corpusPages groups the corpus into {key: value} pages, the pre-decoded input the
// residual-filter benchmark drives runCode over.
func corpusPages(docs []map[string]any, pageSize int) []map[string]any {
	var pages []map[string]any
	for i := 0; i < len(docs); i += pageSize {
		end := min(i+pageSize, len(docs))
		page := make(map[string]any, end-i)
		for j := i; j < end; j++ {
			page[fmt.Sprintf("k%d", j)] = docs[j]
		}
		pages = append(pages, page)
	}
	return pages
}

// totalBytes sums the length of every raw slice, for b.SetBytes on the byte-rate
// benchmarks.
func totalBytes(raw [][]byte) int64 {
	var n int64
	for _, b := range raw {
		n += int64(len(b))
	}
	return n
}
