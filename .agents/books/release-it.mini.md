# Release It! — mini
Michael T. Nygard.
- A passing happy path is not readiness; design the failure semantics, limits and diagnosis surface before production defines them for you.
- Assume every dependency, connection and query can fail slowly, partially or for a long time; code for production mess, do not merely tolerate it by accident.
- Put an explicit, intentional timeout on every outbound call and wait; never rely on a driver default, never allow an infinite wait.
- Retry only when the operation is safe for caller and provider; bound count and total time, use backoff with jitter, never retry a validation or permanent error.
- Isolate a failing dependency with fast failure and, where a call is flaky and repeated, a circuit breaker, so one slow database cannot stall the whole run.
- Budget scarce resources explicitly: release every connection and handle deterministically, and do not hold one across a slow remote call.
- Bound result sets: stream or paginate large payloads instead of loading a whole collection into memory.
- Treat external responses as untrusted: validate shape, status and plausibility before acting on a driver result; malformed data must not poison later state.
- Fail fast when continuing hides unrecoverable trouble or holds a scarce resource; surface the failure with enough context to diagnose.
- Build diagnostics at the boundary: structured context, latency, error and retry signals, without logging secrets or spamming a retry storm.
- Make startup, config validation and one-off jobs fail safely, observable and restartable where practical.
