package neo4j

import (
	"bytes"
	"context"
	"maps"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ctxKey is the type of the marker key that tests put in a context.
type ctxKey struct{}

// markedContext returns a context that carries a marker value, so a test can tell it
// from context.Background.
func markedContext() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, "marked")
}

// isMarked reports whether ctx is the context that markedContext returned.
func isMarked(ctx context.Context) bool {
	return ctx != nil && ctx.Value(ctxKey{}) == "marked"
}

// fakeDriver hands out one fakeSession and records each NewSession call.
type fakeDriver struct {
	neo4j.DriverWithContext
	session *fakeSession
	configs []neo4j.SessionConfig
	ctxs    []context.Context
}

func (d *fakeDriver) NewSession(ctx context.Context, cfg neo4j.SessionConfig) neo4j.SessionWithContext {
	d.ctxs = append(d.ctxs, ctx)
	d.configs = append(d.configs, cfg)
	return d.session
}

// fakeSession records every Run call and answers from a script.
type fakeSession struct {
	neo4j.SessionWithContext
	script    []fakeReply
	runs      []runCall
	closed    int
	closeCtxs []context.Context
}

// runCall holds what one Run call received. params is a copy, because pagedScan
// changes the same map between pages.
type runCall struct {
	ctx    context.Context
	cypher string
	params map[string]any
}

// fakeReply is the answer to one Run call: a failure, or a result.
type fakeReply struct {
	err error
	res *fakeResult
}

func (s *fakeSession) Run(ctx context.Context, cypher string, params map[string]any, _ ...func(*neo4j.TransactionConfig)) (neo4j.ResultWithContext, error) {
	s.runs = append(s.runs, runCall{ctx: ctx, cypher: cypher, params: maps.Clone(params)})
	reply := s.script[len(s.runs)-1]
	if reply.err != nil {
		return nil, reply.err
	}
	return reply.res, nil
}

func (s *fakeSession) Close(ctx context.Context) error {
	s.closed++
	s.closeCtxs = append(s.closeCtxs, ctx)
	return nil
}

// fakeResult streams records one by one and then reports err.
type fakeResult struct {
	neo4j.ResultWithContext
	recs     []*neo4j.Record
	pos      int
	err      error
	nextCtxs []context.Context
}

func (r *fakeResult) Next(ctx context.Context) bool {
	r.nextCtxs = append(r.nextCtxs, ctx)
	if r.pos >= len(r.recs) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeResult) Record() *neo4j.Record { return r.recs[r.pos-1] }

func (r *fakeResult) Err() error { return r.err }

// rows builds a reply that streams the given records.
func rows(recs ...*neo4j.Record) fakeReply { return fakeReply{res: &fakeResult{recs: recs}} }

// failing builds a reply whose result reports err after its records.
func failing(err error, recs ...*neo4j.Record) fakeReply {
	return fakeReply{res: &fakeResult{recs: recs, err: err}}
}

// newFakeStore builds a Store over a fake driver that answers with replies.
func newFakeStore(tgt target, replies ...fakeReply) (*Store, *fakeDriver, *bytes.Buffer) {
	trace := &bytes.Buffer{}
	sess := &fakeSession{script: replies}
	drv := &fakeDriver{session: sess}
	return &Store{driver: drv, database: "db1", target: tgt, trace: trace}, drv, trace
}
