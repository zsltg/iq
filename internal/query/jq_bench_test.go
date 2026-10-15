package query

import (
	"context"
	"testing"

	"github.com/itchyny/gojq"
)

// residualFilterExpr is the client-side residual filter that the pre-filter sits
// in front of: a streamable, `.[]`-rooted top-level equality select.
const residualFilterExpr = `.[] | select(.status=="active")`

// mustCompile parses and compiles expr or fails the benchmark.
func mustCompile(b *testing.B, expr string) *gojq.Code {
	b.Helper()
	q, err := gojq.Parse(expr)
	if err != nil {
		b.Fatalf("parse %q: %v", expr, err)
	}
	code, err := gojq.Compile(q)
	if err != nil {
		b.Fatalf("compile %q: %v", expr, err)
	}
	return code
}

// BenchmarkResidualFilter measures the client-side residual filter cost: running
// the compiled gojq program over already-decoded pages, per shape and match rate.
// This is the full-gojq work the engine does today over every scanned page. A
// raw-byte pre-filter tries to avoid that cost for non-matching documents.
func BenchmarkResidualFilter(b *testing.B) {
	code := mustCompile(b, residualFilterExpr)
	ctx := context.Background()
	sink := func(v any) error { return nil }
	for _, shape := range benchShapes {
		for _, rate := range benchRates {
			docs := genCorpus(shape, benchDocCount(shape), rate)
			pages := corpusPages(docs, 100)
			b.Run(shape.String()+"/"+rateLabel(rate), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for _, page := range pages {
						if err := runCode(ctx, code, page, sink); err != nil {
							b.Fatalf("runCode: %v", err)
						}
					}
				}
			})
		}
	}
}

// BenchmarkSingletonFilter measures what a keyed run pays. It uses the same
// compiled residual over the same corpus, but one gojq iterator per item instead
// of one per page. That is how RunKeyed applies a spec filter to keep every
// item's key. Read it against BenchmarkResidualFilter. The delta between the two
// is the cost of the per-item application. It is also why the unfiltered read
// path still collects pages rather than routing through RunKeyed.
func BenchmarkSingletonFilter(b *testing.B) {
	code := mustCompile(b, residualFilterExpr)
	ctx := context.Background()
	sink := func(v any) error { return nil }
	for _, shape := range benchShapes {
		for _, rate := range benchRates {
			docs := genCorpus(shape, benchDocCount(shape), rate)
			singles := corpusPages(docs, 1)
			b.Run(shape.String()+"/"+rateLabel(rate), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					for _, one := range singles {
						if err := runCode(ctx, code, one, sink); err != nil {
							b.Fatalf("runCode: %v", err)
						}
					}
				}
			})
		}
	}
}

// rateLabel formats a match rate as a compact percentage for a sub-benchmark name.
func rateLabel(rate float64) string {
	switch rate {
	case 0.01:
		return "rate01pct"
	case 0.10:
		return "rate10pct"
	case 0.50:
		return "rate50pct"
	case 1.00:
		return "rate100pct"
	default:
		return "rateX"
	}
}
