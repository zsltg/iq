package query

import (
	"context"
	"fmt"
	"testing"

	"github.com/buger/jsonparser"
	"github.com/itchyny/gojq"
)

// scanPage is one page of the scan: parallel keys and raw document bytes, the unit
// both the baseline and the pre-filter process before handing survivors to gojq.
type scanPage struct {
	keys []string
	raw  [][]byte
}

// buildScanPages groups the corpus into pages of raw documents with stable keys,
// the shared input both scan strategies consume.
func buildScanPages(docs []map[string]any, pageSize int) []scanPage {
	raw := rawDocs(docs)
	var pages []scanPage
	for i := 0; i < len(raw); i += pageSize {
		end := min(i+pageSize, len(raw))
		p := scanPage{keys: make([]string, 0, end-i), raw: make([][]byte, 0, end-i)}
		for j := i; j < end; j++ {
			p.keys = append(p.keys, fmt.Sprintf("k%d", j))
			p.raw = append(p.raw, raw[j])
		}
		pages = append(pages, p)
	}
	return pages
}

// scanBaseline is today's path: fully decode every document in the page (UseNumber
// + convertNumbers, via decodeValue) then run the residual gojq filter over the
// whole page.
func scanBaseline(ctx context.Context, code *gojq.Code, page scanPage, emit func(any) error) error {
	root := make(map[string]any, len(page.raw))
	for i, raw := range page.raw {
		v, err := decodeValue(string(raw))
		if err != nil {
			return fmt.Errorf("decode %s: %w", page.keys[i], err)
		}
		root[page.keys[i]] = v
	}
	return runCode(ctx, code, root, emit)
}

// scanPrefilter is the prototype: extract the top-level "status" from raw bytes
// with jsonparser and decode only documents that could match (status=="active"),
// falling back to a full decode on any extraction error so the pre-filter stays a
// conservative superset — it may keep a document, never wrongly drop one. The
// residual gojq filter still re-runs over the survivors, so correctness is gojq's,
// not the raw evaluator's. Hand-coded top-level Eq only; this is a measurement rig.
func scanPrefilter(ctx context.Context, code *gojq.Code, page scanPage, emit func(any) error) error {
	root := make(map[string]any, len(page.raw))
	for i, raw := range page.raw {
		status, err := jsonparser.GetString(raw, "status")
		if err == nil && status != "active" {
			continue // Cannot match the top-level Eq; skip the decode.
		}
		v, decErr := decodeValue(string(raw))
		if decErr != nil {
			return fmt.Errorf("decode %s: %w", page.keys[i], decErr)
		}
		root[page.keys[i]] = v
	}
	return runCode(ctx, code, root, emit)
}

// BenchmarkScanFilter compares the baseline decode-everything scan against the
// jsonparser pre-filter, per shape and match rate. The residual gojq filter is
// identical in both arms; only the amount decoded differs. A win shows up as fewer
// ns/op, B/op, and allocs/op at low selectivity with no regression at 100%.
func BenchmarkScanFilter(b *testing.B) {
	code := mustCompile(b, residualFilterExpr)
	ctx := context.Background()
	sink := func(v any) error { return nil }
	strategies := []struct {
		name string
		run  func(context.Context, *gojq.Code, scanPage, func(any) error) error
	}{
		{name: "baseline", run: scanBaseline},
		{name: "prefilter", run: scanPrefilter},
	}
	for _, shape := range benchShapes {
		for _, rate := range benchRates {
			docs := genCorpus(shape, benchDocCount(shape), rate)
			pages := buildScanPages(docs, 100)
			var nbytes int64
			for _, p := range pages {
				nbytes += totalBytes(p.raw)
			}
			for _, st := range strategies {
				b.Run(shape.String()+"/"+rateLabel(rate)+"/"+st.name, func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(nbytes)
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						for _, page := range pages {
							if err := st.run(ctx, code, page, sink); err != nil {
								b.Fatalf("scan: %v", err)
							}
						}
					}
				})
			}
		}
	}
}
