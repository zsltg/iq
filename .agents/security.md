# Security index

This file maps the security controls of iq to the OWASP lists.
It also gives the security rules that no scanner checks.
It does not repeat the rules in other files:

- The threat model and the redaction and storage rules are in [REVIEW.md](../REVIEW.md#threat-model).
- The MCP server rules are in [REVIEW.md](../REVIEW.md#mcp-server).
- The input, query, error, and secret rules are in [AGENTS.md](../AGENTS.md#code-and-safety).
- The gate procedures are in [DEVELOPMENT.md](../DEVELOPMENT.md#security-gate-scriptssecuritysh).

iq is a local command-line tool. It has no accounts, no sessions, no web frontend, no listening socket, and no telemetry.
Runtime security is the behavior of the `iq` binary and `iq mcp`. Build security is the dependency, CI, and release chain. Keep the two apart.

## OWASP Top 10 (2025)

- A01 Broken Access Control, including SSRF: `iq mcp` registers a write, exec, or destructive tool only when `--allow` opens its capability (`cmd/mcp.go`). The jq read path does not write. iq connects only to the host of a connection URI that the user supplies or saves. It also connects to the hosts that the driver SDK discovers from that host. Examples are the HBase region servers from ZooKeeper and the regional endpoints of the AWS SDK for DynamoDB. The AWS default credential chain also connects to credential endpoints, for example STS, SSO, and the EC2 or ECS metadata service. The AWS configuration and environment of the user select them. MCP tools and `source()` take source names from the registry, not connection URIs. Gate: review, `cmd` tests.
- A02 Security Misconfiguration: `Config.Save` writes the config file with mode `0600`. `config.ModeWarning` reports a wider mode on a file with an inline password. TLS comes from the scheme or options of the connection URI. Gate: review, gosec in `make check`.
- A03 Software Supply Chain Failures: the scanners are govulncheck, osv-scanner with the license allowlist, gitleaks, and the capslock baseline. Renovate delays new releases. CI pins actions by commit SHA, and Harden-Runner blocks egress. The one pin exception is the SLSA generator in `release.yml`, which must be referenced by tag. Gate: `make security`, `make capabilities`, CI.
- A04 Cryptographic Failures: iq has no cryptography of its own. Passwords go to the OS keyring by default (`internal/secret`). TLS comes from the driver SDK and the Go standard library. Gate: review.
- A05 Injection: values are query parameters. Identifiers pass a whitelist or the quoting helper of the driver, for example `quoteIdent` in `drivers/cassandra`. gojq compiles without environment access and without a module loader. `iq exec` forwards native input verbatim by design, and MCP gates it with `--allow exec`. Gate: review, gosec.
- A06 Insecure Design: read-only by default. Only the write paths listed in REVIEW.md write. `--dry-run` previews a write without a write. A real call to an MCP destructive tool needs `confirm: true` or a confirmation that the user accepts in the MCP client. A call with `dry_run: true` needs no confirmation and does not write. The exception is `iq_exec`: it has no preview and no per-call confirmation, so `--allow exec` is the only gate. Gate: review.
- A07 Authentication Failures: iq has no authentication of its own. It passes the credentials of the user to the database. A credential comes from the keyring, the environment, the config file, or a credential provider of the driver SDK. An example of a provider is the AWS default credential chain. A credential never comes from source code. Gate: gitleaks.
- A08 Software or Data Integrity Failures: each release carries checksums with a keyless cosign signature and a CycloneDX SBOM (`.goreleaser.yaml`). It also carries SLSA level 3 build provenance for every artifact (`release.yml`). A successful write stores the value exactly as supplied. Gate: release workflow, round-trip tests.
- A09 Security Logging and Alerting Failures: log attributes pass `redactAttr` (`cmd/logging.go`). iq is a local tool and has no alerting. Gate: `cmd` logging tests.
- A10 Mishandling of Exceptional Conditions: user-facing errors pass the redactor in `cmd/errrender.go`. MCP tool errors pass `redactErr` and `toolError`. Dump decoders return an error for malformed input. Gate: fuzz targets in `drivers/file`, `internal/query`, `internal/rawpred`.

## API Security Top 10 (2023) for `iq mcp`

`iq mcp` is the only API surface. It runs over stdio for one local client.

- API1 Broken Object Level Authorization: tools reach only the saved sources of the user who starts the server. The database authorizes each call.
- API4 Unrestricted Resource Consumption: `--max-items` and `--max-bytes` cap the result of `iq_query` only. A per-call `max_items` or `max_bytes` only lowers them. The byte cap applies to each encoded item before it joins the result, so it limits output, not memory or work. `iq_inspect`, `iq_schema` (with `sample: 0`), and `iq_exec` have no result cap. The database permissions and the deadline of each call are the remaining limits.
- API5 Broken Function Level Authorization: a tool outside the `--allow` set is not registered.
- API7 Server Side Request Forgery: see A01.
- API8 Security Misconfiguration: read-only by default. Stdout carries only JSON-RPC messages.
- API10 Unsafe Consumption of APIs: database responses are untrusted. See A05 and A10.

API2, API3, API6, and API9 do not apply. `iq mcp` has no server authentication, no object properties for a client to set, no business flows, and one tool list.

## ASVS 5.0 cross-walk

- V1 Encoding and Sanitization: A05.
- V2 Validation and Business Logic: input checks at function entry, closed value sets such as `--allow` and the persistable option allowlist in `cmd/config.go`.
- V5 File Handling: dump files are untrusted and streamed. `drivers/file` unwraps gzip as a stream.
- V8 Authorization: A01 and API5. `--allow` decides which functions the MCP client can call, and the database authorizes each call. See [REVIEW.md](../REVIEW.md#mcp-server).
- V11 Cryptography: A04.
- V12 Secure Communication: TLS by connection URI. Certificate checks stay on unless the user selects a scheme that turns them off, for example Neo4j `+ssc`.
- V13 Configuration: A02. Secrets live in the keyring, the environment, or the `0600` config file.
- V14 Data Protection: no telemetry. Displays redact passwords unless the user supplies `--reveal`.
- V15 Secure Coding and Architecture: A03 and the capslock baseline.
- V16 Security Logging and Error Handling: A09 and A10.

V3, V4, V6, V7, V9, V10, and V17 do not apply. iq has no web frontend, no HTTP service, no accounts, no sessions, no tokens, no OAuth, and no WebRTC.

Reopen triggers:

- An HTTP transport for `iq mcp`, or any listening socket, reopens V4, V6, V7, inbound V12, and API2. It also extends V8 to more than one user. Read those chapters in full first.
- An outbound host that does not come from a connection URI or from the default discovery of the driver SDK reopens SSRF.

## Rules for authors

No scanner checks these rules. A review does.

- Egress: connect only to three kinds of host. These are the host of the connection URI, the hosts that the driver SDK discovers from it, and the credential endpoints of an SDK credential provider. The AWS configuration and environment of the user select those endpoints. Never build a host, a port, or a connection URI from database records, dump content, or MCP tool arguments. Cluster metadata that the SDK reads for discovery is not a record. A new outbound host or a new discovery path is a review trigger.
- TLS: never set `InsecureSkipVerify` or lower the minimum TLS version in code. An insecure mode is a scheme or an option that the user writes in the connection URI, and the docs name the risk.
- jq sandbox: every `gojq.Compile` call omits `gojq.WithEnvironLoader` and `gojq.WithModuleLoader`. An MCP filter comes from an AI agent, and the environment can hold `IQ_*` connection URIs with passwords. A new jq function that reads files, the network, or the environment is a review trigger. `source()` is the one exception, and it reads only through the registry.
- Redaction: reuse the existing redactors: `redactURL` (`cmd/source.go`), `redactErr` (`cmd/ping.go`), `newRedactor` (`cmd/errrender.go`), `redactAttr` (`cmd/logging.go`), and `redactedConfig` (`cmd/config.go`). Do not write a second redactor for a new display.
- MCP tools: a tool that writes, runs native input, or deletes goes behind an `--allow` capability. A new tool that returns records applies the `--max-items` and `--max-bytes` caps. A tool argument that names a target takes a source handle, never a connection URI.
- Dump files: stream the records. Never use dump content as a file path, a host, or a command.
- Subprocesses: iq starts three kinds of process, and none takes input from data. `iq config edit` runs the editor of the user. The AWS SDK for DynamoDB runs a `credential_process` command from the AWS configuration file of the user. On Linux, the keyring dependency runs a helper to find the D-Bus session bus. [capslock-baseline.notes.md](../capslock-baseline.notes.md) records all three decisions. Never start a process or a shell with input from a database, a dump file, or an MCP tool.
- CI workflows: follow the workflow rules in [REVIEW.md](../REVIEW.md#critical-areas). List each new outbound host in the Harden-Runner `allowed-endpoints` of the job.

## Gates

- `make check` runs gosec through golangci-lint.
- `make security` runs govulncheck, the osv-scanner dependency scan, the osv-scanner license allowlist, the collection of third-party license texts, gitleaks, and zizmor. It writes SPDX and CycloneDX SBOMs to `dist/` with syft. The release SBOM is a separate file that cyclonedx-gomod makes.
- `make capabilities` runs the capslock drift gate.
- CI also runs CodeQL and OpenSSF Scorecard, and Socket reviews each dependency change.

Read [DEVELOPMENT.md](../DEVELOPMENT.md#quality-gates) for the procedures and prerequisites.
