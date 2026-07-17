package query

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// drainSource runs a RecordSource to completion, discarding every page, so a
// benchmark measures only the source's decode cost.
func drainSource(b *testing.B, src RecordSource) {
	b.Helper()
	err := src(context.Background(), func(batch []Record) error {
		return nil
	})
	if err != nil {
		b.Fatalf("drain source: %v", err)
	}
}

// BenchmarkJSONLSourceTyped measures the typed JSONL import path (the
// {key,type,value} envelope decode plus number normalization) per doc shape.
func BenchmarkJSONLSourceTyped(b *testing.B) {
	for _, shape := range benchShapes {
		docs := genCorpus(shape, benchDocCount(shape), 1.0)
		corpus := encodeJSONLTyped(docs)
		b.Run(shape.String(), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(corpus)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainSource(b, JSONLSource(bytes.NewReader(corpus), 100, false))
			}
		})
	}
}

// BenchmarkJSONLSourcePlain measures the plain JSONL import path (whole value per
// line, no envelope) per doc shape.
func BenchmarkJSONLSourcePlain(b *testing.B) {
	for _, shape := range benchShapes {
		docs := genCorpus(shape, benchDocCount(shape), 1.0)
		corpus := encodeJSONLPlain(docs)
		b.Run(shape.String(), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(corpus)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainSource(b, JSONLSource(bytes.NewReader(corpus), 100, true))
			}
		})
	}
}

// BenchmarkJSONSourceArray measures the top-level-array import path (typed records
// inside a single JSON array) per doc shape.
func BenchmarkJSONSourceArray(b *testing.B) {
	for _, shape := range benchShapes {
		docs := genCorpus(shape, benchDocCount(shape), 1.0)
		corpus := encodeJSONArrayTyped(docs)
		b.Run(shape.String(), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(corpus)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				drainSource(b, JSONSource(bytes.NewReader(corpus), 100, false))
			}
		})
	}
}

// BenchmarkWriteJSONL measures the export half: encoding Records to a typed JSONL
// dump, per doc shape.
func BenchmarkWriteJSONL(b *testing.B) {
	for _, shape := range benchShapes {
		docs := genCorpus(shape, benchDocCount(shape), 1.0)
		recs := corpusRecords(docs)
		// Size the output once for the byte rate.
		var sizer bytes.Buffer
		if err := WriteJSONL(&sizer, recs); err != nil {
			b.Fatalf("size WriteJSONL: %v", err)
		}
		out := int64(sizer.Len())
		b.Run(shape.String(), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(out)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := WriteJSONL(io.Discard, recs); err != nil {
					b.Fatalf("WriteJSONL: %v", err)
				}
			}
		})
	}
}
