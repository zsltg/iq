package couchbase

import (
	"errors"
	"fmt"
)

// errNonBytesTarget and errEncodeUnsupported guard rawTranscoder: it only decodes into
// a *[]byte and never encodes, so a misuse fails loudly rather than silently.
var (
	errNonBytesTarget    = errors.New("couchbase: raw transcoder decodes only into *[]byte")
	errEncodeUnsupported = errors.New("couchbase: raw transcoder does not encode")
)

// tracef writes one trace line to the --verbose trace writer, when set. It is produced
// one layer up from the SDK — the driver knows the statement and op names — so the
// trace carries parameter placeholders ($after, $page) only, never a bound value and
// never a credential. A nil writer disables tracing.
func (s *Store) tracef(format string, args ...any) {
	if s.trace == nil {
		return
	}
	_, _ = fmt.Fprintf(s.trace, "couchbase> "+format+"\n", args...)
}
