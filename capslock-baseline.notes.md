# capslock baseline justifications

One line per justified row in `capslock-baseline.json`, grouped by package: `packageDir`,
capability, then the call path and why it is accepted. The purpose is to let a row that
resurfaces after a dependency bump or a refactor be re-accepted knowingly instead of
re-litigated from vendor docs, and to keep every *interesting* capability honest about how
it is reached.

Scope, stated honestly. The baseline holds 194 `(package, capability)` rows and this file
annotates four of them. Only the rows that carry real authority — `EXEC`,
`ARBITRARY_EXECUTION`, `MODIFY_SYSTEM_STATE/*`, `SYSTEM_CALLS` — plus known false positives
are justified here; the bulk (`NETWORK`, `FILES`, `REFLECT`, `RUNTIME`, `READ_SYSTEM_STATE`,
`OPERATING_SYSTEM`, `UNANALYZED`, `UNSAFE_POINTER` on a driver that talks to a database over
a socket) are the expected shape of this program and stay unannotated.

Four rules bind this file:
- An entry here NEVER pre-authorizes a new row. A gained capability is read at the call path
  capslock prints, justified with a line here, and only then recorded with
  `IQ_CAPS_UPDATE_BASELINE=1`. This file records past acceptances; it does not license new
  ones.
- capslock prints ONE example path per `(package, capability)` pair, not every path. A row's
  printed path can be an interface-dispatch artifact while the package holds the capability
  for an entirely different, genuine reason — the `cmd` EXEC entry below is exactly that.
- Package granularity compares the capability *set*, so a package that already holds a
  capability can gain new call paths into it invisibly. An entry here is a justification for
  the row, never a claim that the row's paths are exhaustive.
- The baseline is linux-only. `IQ_CAPS_GOOS=darwin|windows` reports a different set by
  design (`internal/secret` is the widest divergence); those runs are review evidence, never
  a gate verdict, and never a baseline.

## github.com/zsltg/iq/internal/secret — EXEC (the Linux Secret Service backend)
Expected. The OS keyring backend reaches `os/exec` through D-Bus session discovery: godbus
shells out to find the session bus address when `DBUS_SESSION_BUS_ADDRESS` is unset. Verified
path, `(OSKeyring).Delete` being one entry point of several:

    secret.go:57         github.com/zalando/go-keyring.Delete
    keyring_unix.go:139  go-keyring/secret_service.NewSecretService
    secret_service.go:51 github.com/godbus/dbus/v5.SessionBus
    conn.go:138          dbus.getSessionBusAddress
    conn.go:87           dbus.getSessionBusPlatformAddress
    conn_other.go:19     os/exec.Command

This is the platform's own credential store, which is the point of the package: it is why
`iq` never has to hold a secret itself. The same package's `SYSTEM_CALLS` and
`MODIFY_SYSTEM_STATE/ENV` rows come from the same dependency chain.

## github.com/zsltg/iq/cmd — EXEC ($EDITOR, deliberate and already annotated)
`iq config edit` launches the user's configured editor: `cmd/config.go:344`,
`exec.CommandContext(cmd.Context(), editor, args...)`, carrying
`//nolint:gosec // G204: launches the user's configured $EDITOR, by design.` A config-trusting
exec surface, accepted at the point it was written.

Note the mismatch: the path capslock prints for this row is *not* the editor. It resolves
`(*os/exec.Cmd).writerDescriptor$1` as one implementation of a `func() error` reached through
go-redis's `onCloseHooks.run` — interface dispatch across the analyzed set, the same class as
the `internal/render` false positive below. The row is genuine, the printed evidence for it
is not; read `cmd/config.go:344` instead.

## github.com/zsltg/iq/drivers/dynamodb, github.com/zsltg/iq/internal/query — EXEC (AWS credential_process)
Inherited AWS shared-config behaviour, accepted alongside the `$EDITOR` surface above.
`drivers/dynamodb/dynamodb.go:126` calls `config.LoadDefaultConfig`, so the full shared-config
credential chain is live — the static dummy credentials are added only when `?endpoint=` is
set (DynamoDB Local). One link of that chain is `credential_process`, which by design runs a
command from `~/.aws/config`. Verified path:

    write.go:54            (*dynamodb.Client).PutItem
    presign_middleware.go  (*credentials/processcreds.Provider).Retrieve
    provider.go:181        processcreds.Provider.executeCredentialProcess
    provider.go:106        os/exec.CommandContext

`internal/query` carries the same row transitively: `(*query.Copier).Copy` → `dynamodb.Store.Put`
→ the chain above. Exploiting it means already controlling `~/.aws/config` or the process
environment, at which point the account is owned; and removing it would mean refusing the
standard AWS credential chain. Accepted as a config-trusting exec surface, not a defect.

## github.com/zsltg/iq/internal/render — NETWORK (FALSE POSITIVE, do not re-litigate)
Interface dispatch, not a network call. `json.Encoder` takes an `io.Writer`, and capslock
resolves that against every `io.Writer` implementation in the analyzed set — including
`(*net/http.http2responseWriter).Write`. The package writes to a buffer and to whatever the
caller passes; it opens nothing. Confirmation: `internal/render` analyzed *alone* reports only
`{REFLECT, UNANALYZED, UNSAFE_POINTER}`, and gains `NETWORK`, `FILES`, `ARBITRARY_EXECUTION`
and the rest purely from the whole-tree analysis scope. The same artifact inflates every
driver's row set — it is why a whole-tree run shows 11–13 capabilities per driver while
`drivers/redis` alone shows 8 — and it is the reason the baseline is only ever compared
against a run of the same scope.

## github.com/zsltg/iq/internal/diff, github.com/zsltg/iq/internal/jqfmt — ARBITRARY_EXECUTION, MODIFY_SYSTEM_STATE/SIGNALS, NETWORK (FALSE POSITIVE, Go 1.27 encoding/json)
The same interface-dispatch artifact as `internal/render`, surfaced by the Go 1.27 toolchain
bump (capslock v0.3.3, 2026-08-27). From 1.27 `encoding/json` is implemented atop
`encoding/json/v2`: its `jsontext` decoder reads through an `io.Reader`, which capslock
resolves against every implementation in the analyzed set (the printed path lands in
`klauspost/compress/s2.Decode`), and `encoding/json.transformMarshalError` compares errors
against `os/signal.signalError`, which is the `SIGNALS` row. Both packages only call
`json.Marshal` / `json.Unmarshal` on in-memory values: `diff.canonicalKey` to build a
canonical set key, `jqfmt` through `gojq.Parse` unescaping string literals. Confirmation:
analyzed *alone* with the same capslock, `internal/diff` reports `{REFLECT, UNANALYZED,
UNSAFE_POINTER}` and `internal/jqfmt` `{FILES, REFLECT, UNANALYZED, UNSAFE_POINTER}`, so
every row above comes purely from the whole-tree scope. They were the last two json-calling
packages not already saturated, which is why the toolchain bump moved only these two.
