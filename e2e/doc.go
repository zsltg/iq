// Package e2e holds black-box smoke tests that build and run the iq binary and
// assert on its real stdout and exit codes. It has no runtime code; see cli_test.go
// for the offline flows. live_test.go adds flows that drive a real backend, gated on
// IQ_REDIS_URL / IQ_MONGO_URL (skipped when unset, with no localhost fallback) so the
// offline suite never needs a running server.
package e2e
