package rawpred_test

import (
	"testing"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// benchDoc is one document for every BenchmarkMatcher case. The key "dup" occurs
// twice, so a path through it is ambiguous.
var benchDoc = []byte(`{"id":1,"name":"widget","tags":["a","b","c"],"price":12.5,` +
	`"qty":7,"meta":{"owner":"ops","zone":"eu"},"dup":1,"dup":2}`)

// benchVerdict keeps the result of each Match call, so the compiler cannot drop it.
var benchVerdict rawpred.Verdict

// BenchmarkMatcher measures the per-document cost of a prepared Matcher. Each case
// is one node type on a field that is present, absent, or ambiguous. The Matcher is
// built once per case, outside the timed loop, as a driver builds it once per scan.
func BenchmarkMatcher(b *testing.B) {
	present := []string{"qty"}
	absent := []string{"missing"}
	ambiguous := []string{"dup"}
	cases := []struct {
		name string
		pred predicate.Node
	}{
		{"Eq/present", predicate.Eq{Path: present, Value: 7.0}},
		{"Eq/absent", predicate.Eq{Path: absent, Value: 7.0}},
		{"Eq/ambiguous", predicate.Eq{Path: ambiguous, Value: 7.0}},
		{"Exists/present", predicate.Exists{Path: present}},
		{"Exists/absent", predicate.Exists{Path: absent}},
		{"Exists/ambiguous", predicate.Exists{Path: ambiguous}},
		{"Size/present", predicate.Size{Path: []string{"tags"}, N: 3}},
		{"Size/absent", predicate.Size{Path: absent, N: 3}},
		{"Size/ambiguous", predicate.Size{Path: ambiguous, N: 3}},
		{"Cmp/present", predicate.Cmp{Path: present, Op: predicate.Gt, Value: 5.0}},
		{"Cmp/absent", predicate.Cmp{Path: absent, Op: predicate.Gt, Value: 5.0}},
		{"Cmp/ambiguous", predicate.Cmp{Path: ambiguous, Op: predicate.Gt, Value: 5.0}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			m := rawpred.NewMatcher(c.pred)
			b.ReportAllocs()
			for b.Loop() {
				benchVerdict = m.Match(benchDoc)
			}
		})
	}
}
