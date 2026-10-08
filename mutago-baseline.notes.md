# mutago baseline justifications

One line per accepted entry in `mutago-baseline.json`, grouped by file: the 12-char id
prefix, `file:line`, mutator, then a one-line equivalence proof. The purpose is to let a
refactor-resurfaced entry (mutago's ids are content hashes, so a refactor mints a new id for
the same site) be re-accepted knowingly instead of re-litigated from commit archaeology, and to
keep every accepted survivor honest about *why* it survives.

Two rules bind this file:
- An entry here NEVER justifies skipping the clean-room acceptance procedure: a new survivor is
  killed first, and only accepted (with `IQ_MUTATION_UPDATE_BASELINE=1` plus a line here) once it
  is shown to be a genuine equivalent or an irreducible flaky-order escape. This file records
  past acceptances; it does not pre-authorize new ones.
- "justification pending" means the entry was accepted in the cited commit but its proof is not
  reconstructed here; treat it as unproven and re-derive before relying on it. It is a seed, not
  a verdict.

Residue doctrine (unchanged, first-line): refactor a reducible equivalent class away — keyset
pagination, shared response helpers, order-deterministic tests — before baselining it.

## drivers/redis/filter.go — go-redis per-command context is inert (accepted 57bea97, 575cc7c; 59/92 re-accepted 6942d82)
The `expression/context-nil` mutator nils the `ctx` handed to a per-command call inside a
pipeline. `(*Pipeline).BatchProcess` discards that per-command context — the outer `Pipelined(ctx)`
governs the actual I/O and its deadline — so nil-ing the inner context is behaviorally inert. The
59/92 pair are the same two sites as 56..92, re-accepted under fresh content-hash ids after a
refactor shifted them.
- 505413a1ae7c drivers/redis/filter.go:59 expression/context-nil — same site as :56 re-accepted after refactor (content-hash id changed).
- 578155c117f2 drivers/redis/filter.go:91 expression/context-nil — inner ctx discarded by BatchProcess; outer Pipelined(ctx) governs I/O.
- 3e53982b48bf drivers/redis/filter.go:92 expression/context-nil — same site as :91 re-accepted after refactor (content-hash id changed).

## internal/rawpred/rawpred.go — return of the deliberate zero value (accepted 57bea97)
- 13548ffece9a internal/rawpred/rawpred.go:52 statement/return — `return MayMatch`; MayMatch is the zero-value verdict, so a return-zero mutant is byte-identical.

## internal/parquetout/writer.go — parquet-export residue (accepted 6942d82; proofs held by that session)
Accepted during the parquet-export work; the equivalence proofs are not reconstructed here.
Justification pending (parquet session) — re-derive before relying on any of these.
- f54a44ed2ade internal/parquetout/writer.go:329 numbers/incrementer — accepted in 6942d82; justification pending (parquet session).

## internal/shape/shape.go — schema-inference residue (accepted 95f8e57)
The schema projection walks Go maps (`n.kinds`, `n.fields`, `o.vals`) whose iteration order is
randomized, so this cluster is dominated by flaky-order-dependent escapes (killable only on some
seeds — labeled not-equivalent per the order-dependent-residue doctrine) plus a few genuine
zero-value / redundant-guard equivalents. Confident proofs are stated; the rest are seeded from
the 95f8e57 acceptance as pending and must be re-derived before relying on them.
- 38583e7a2e76 internal/shape/shape.go:487 expression/remove — floatKind finite/integral guard conjunct; accepted in 95f8e57; justification pending.

## cmd/diff.go, internal/diff — LCS alignment, set-arrays, and RFC 6902 patch (accepted <SHA>)
The diff walk gained an LCS array alignment, an order-insensitive set mode, and a JSON Patch
output. Its irreducible survivors are three classes: a spinner whose effect is unobservable
without a TTY, zero-value struct-field writes, and DP-table perturbations that change how a tie
is broken without changing the reported alignment. The alignment class is the interesting one:
the contract these mutants must violate is "the reported deltas are exactly the elements outside
a longest common subsequence", which TestTreeArrayAlignmentIsMinimal checks exhaustively over
every array pair up to length 4 on three symbols and TestTreeArrayAlignmentIsMinimalAtScale
checks over 4000 seeded pairs up to length 12 on four symbols. A mutant that merely picks a
different equally-minimal alignment satisfies that contract; a mutant that degrades the table
does not: the fill-loop `dp[i+0][j]` degradation is caught by the scale test and was fixed rather
than accepted here.
- a0d9419cf8fce8254c35a8a66e5673aa internal/diff/diff.go:184 composite/field-clear — `Op: OpAdd` dropped from the surplus-right Change literal; OpAdd is iota 0, the zero value of Op, so the cleared field holds the value it was assigned.
- 2aa60a55faebcce062fa39df2fa39d79 internal/diff/diff.go:232 composite/field-clear — same zero-value identity for the set-mode surplus-right Change literal.
- 5ee09328de4c8bb5cae525db203582d4 internal/diff/diff.go:300 numbers/incrementer — `dp[i][j] = dp[i+1][j+1] + 1` becomes `+ 2`. Every match contributes the same constant, so the table becomes a uniform scaling of the LCS-length table (2*L with a 0 base) and every `>=` comparison in the fill and the backtrack orders identically. Provably equivalent, not merely untested.
- 15a18bbc73e394d89110ab55fa1589c9 internal/diff/diff.go:301 expression/comparison — the fill tie-break `dp[i+1][j] >= dp[i][j+1]` becomes `>`. Both directions select an equally long common subsequence, so both alignments are minimal and satisfy the contract; output was byte-identical across the 21866 exhaustive pairs and the 4000 seeded pairs.
- d61c1d13909632b24aff56c4b2ae5d2c cmd/backend.go:33 expression/context-nil — ctx passed to wrapStoreLogging feeds only `(*slog.Logger).Enabled`, which normalizes a nil ctx to `context.Background()` before the handler runs (log/slog/logger.go), so no handler can observe the substitution.
- 8d8b67cfaa5a2f9ae268a758c53d80ae cmd/backend.go:42 expression/context-nil — same slog.Enabled nil-ctx normalization, at the Enabled call inside wrapStoreLogging.
- 0994bc19cf0021d62b93daf5a7054485 cmd/jq.go:166 expression/context-nil — same slog.Enabled nil-ctx normalization, at the plan-record gating check.
- fdbace0a2e553747170ca5df7e0d1925 cmd/logging.go:250 expression/context-nil — same slog.Enabled nil-ctx normalization, at traceSink's DEBUG-sink probe.

## cmd/man.go, cmd/complete.go — man page and completion equivalents (accepted 2026-07-22, install-packaging branch)
The man generator and the dynamic completion helpers leave five genuine equivalents: two
zero-value directive identities, two guards reachable only by a `Use` string starting with a
space (which cobra's name derivation makes impossible), and a fall-through that emits a
byte-identical line.
- 2ee430f494f0 cmd/complete.go:75 statement/return — same ShellCompDirectiveDefault==0 identity on completeCacheClear's success return.
- ec576c164a72 cmd/complete.go:365 statement/return: the same ShellCompDirectiveDefault==0 identity on the success return of completeCacheClear. The line changed when candidates got descriptions in feat/completion-descriptions, so the mutant has a new ID. Accepted 2026-10-08.
- 2cc310b7cd64 cmd/man.go:163 expression/comparison — manUsageLine `i >= 0` → `i > 0` differs only when IndexByte returns 0, i.e. a Use beginning with a space; cobra derives Name() from the first token, so no such command can exist.
- 9adb645adba3 cmd/man.go:163 numbers/incrementer — same impossible-input class: the mutated bound differs only for a Use whose first byte is the space.

## cmd/explain.go — jq-stage annotation equivalents (accepted 2026-07-22, explain-jq-descriptions branch)
The `--verbose` per-stage jq annotation leaves four genuine equivalents: one unreachable
error guard and a note-column max loop whose result is independent of its start value and of
strict-vs-nonstrict comparison.
- 3ea86735ab2d cmd/explain.go:246 numbers/incrementer — writeJQExplained's note column starts at `col := 0`, then becomes the max visible width over the note-bearing lines; any non-negative start converges to that same maximum when a note exists and is unused when none do.
- 28f644a6cfbd cmd/explain.go:249 expression/comparison — the max loop's `w > col` and `w >= col` pick the same maximum, since an equal width leaves col unchanged either way.

## cmd/resolve.go — dotted-address walk equivalents (accepted 2026-07-24, couchbase-scope-address branch)
splitSourceArg walks the dots right to left looking for the longest prefix that names a
source. Both surviving mutants only widen that walk to consider a prefix ending at index 0,
which is the empty string, and `(*Config).Resolve` returns false for an empty name before
touching the source map (internal/config/config.go:453) — so the extra step can never match
and the walk falls through to the same result.
- 6277c273c32b cmd/resolve.go:165 expression/comparison — the walk's `i > 0` → `i >= 0` adds one iteration for a leading-dot argument, resolving `arg[:0]` == "", which Resolve rejects outright.

## cmd — source-spec equivalents (accepted 2026-07-29, feat/source-spec branch)
Two survivors whose mutations cannot change behaviour, for unrelated reasons. The explain
entry is the third recording of one guard: it was accepted at :130 and again at :155, and
this branch moved it once more by passing the stage's own resolved url instead of a local,
which re-hashes the id. The sourcespec entry mutates an argument the callee only consults on
a path the caller has already excluded.
- 569a2c9df78609ff09ef028db4b61b1e cmd/sourcespec.go:91 conditional/bool-literal — flips the isDst argument of resolveEndpoint, which that function reads only when the argument is the empty string (cmd/data.go:82-87); resolveSourceSpec rejects an empty or all-space name two statements earlier, so the flipped value is never consulted from this call site.

## cmd — typed-format equivalent (accepted 2026-08-08, fix/typed-format-rejects-gron branch)
One survivor, equivalent because the flag it drops and the fallback it drops through to select
the same rendering. The entry is pre-existing behaviour, surfaced only because this branch
edited that return to add the gron disjuncts, pulling the line into the diff scope. Every other
disjunct in the chain is killable and was killed: dropping any of them makes a rejected or
pretty rendering fall back to jsonl, which the format table and rejection table in
cmd/move_test.go both assert against. Verified by hand before acceptance — the mutation applied
to a file copy passes the whole cmd suite, so the ESCAPED verdict is real and not a flake.
- 6157a43f092c0968579680e291eba8bf cmd/move.go:230 expression/remove — clears `cfg.jsonl` from anyFormatFlag, but selectTypedFormat already early-returns formatJSONL when no format flag is set, so `--typed --jsonl` reaches formatJSONL either way (through selectFormat originally, through the default under the mutant); the flags are mutually exclusive, so no combination separates the two paths.
- 3f457cc19b54cb4bf565b520d24a98d3 drivers/file/bloom.go:24 numbers/decrementer — lowers the bit floor in `max(n*bloomBitsPerKey, 8)` to 7; the floor only binds at n=0 and the byte count is `(bits+7)/8`, which is 1 for both 7 and 8, so the allocated filter is identical. The sibling incrementer (floor 9, two bytes at n=0) is killed by TestBloomSizing. Verified by hand: the mutation applied to a file copy passes the whole file-driver suite.

## cmd — insert path equivalents (accepted 2026-08-28, feat/mcp branch)
Three survivors in applyInsert, the write path `iq --insert` and the MCP iq_insert tool share.
Two guard an unreachable branch and one is a batching size with no observable result; every
other mutant on the path is killed by TestApplyInsertRedisIntegration (dry-run replace neither
asks nor clears, a replace asks then clears, a refused confirmation aborts before clearing,
insert-only skips, upsert overwrites, the transform shapes the write, the label and the outcome
line are reported) and TestApplyInsertNeedsADestination.
- cc46f0c68e46b1b7ced227270766bb4e cmd/move.go:189 composite/field-clear — clears `PageSize: movePageSize` from the Copier, which then batches 100 records instead of 500; batching changes neither the records written nor the reported counts, and the write is observable only through those.

## drivers/file — Windows drive-path mapping (accepted 2026-08-30, fix/windows-tests branch)
parseFileURL folds the RFC 8089 drive form (file:///C:/dir/file) to a native path through
nativePath(runtime.GOOS, path); nativePath itself is tested for every OS by parameter, so all
its mutants are killed on Linux.
- e34537e600cfd647de83b718dbb6f09c drivers/file/file.go:190 statement/remove — drops the `path = nativePath(runtime.GOOS, path)` call in parseFileURL; nativePath returns its input unchanged for every goos but windows, so on the Linux gate host the removal is a no-op. Only a Windows test run can observe it (TestURLRoundTrip does, through DumpPath, on windows-latest in CI).

## drivers/mongo — full-scan equivalents (accepted 2026-08-30, test/mongo-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 48 escapes, all of
them killed by strengthened tests except the six below. Four are driver-context normalization
or an unreachable decode guard, two are a bit size the standard library ignores. Every one was
verified by hand — the mutation applied to a file copy passes the whole package suite — before
acceptance; the killed ones are covered by the cancelled-context table, the mid-fetch and
mid-scan cancellation tests, the unordered-write tests and the command-monitor test.
- d70002bbf70b drivers/mongo/mongo.go:100 expression/context-nil — `_ = client.Disconnect(ctx)` on Open's failed-ping path; the result is discarded and the driver maps a nil ctx to context.Background(), so neither the returned error nor the (nil) store changes.
- c5ea53e8585d drivers/mongo/mongo.go:143 expression/context-nil — `defer func() { _ = cur.Close(ctx) }()`; Close's only effect is a best-effort killCursors whose error is discarded, and a nil ctx becomes context.Background() inside the driver.
- f492a2761e42 drivers/mongo/mongo.go:255 expression/context-nil — `s.client.Disconnect(context.Background())` → `Disconnect(nil)`; the driver's first act is `if ctx == nil { ctx = context.Background() }`, so the mutant is byte-identical to the original call.
- b97da05ec7d2 drivers/mongo/normalize.go:86 numbers/decrementer — `strconv.ParseFloat(str, 64)` → bitSize 63; ParseFloat special-cases only bitSize 32 and sends every other value down the 64-bit path, so the parsed float is identical (verified against Go 1.27).
- b537ea92d1b6 drivers/mongo/normalize.go:86 numbers/incrementer — same site with bitSize 65; identical for the same reason.

## drivers/file — full-scan equivalents (accepted 2026-08-31, test/file-mutation branch)
First full scan of the package; only its diff lines had ever been gated. CI's scan of the
unstrengthened package reported 216 escaped mutants, 210 of them new (covered-code MSI
81.8%). Strengthened tests killed 169 of those; the 41 below are genuine equivalents, in
seven classes: returning a named constant whose value is its type's zero, so `return 0` is
byte-identical (15 entries, every `FormatUnknown`/`nodeKind` return); a `<` already guarded
by an inequality, or one whose operands are unique by construction; a guard the next
statement re-derives, or whose condition no caller can produce; a disjunct subsumed by the
one beside it; an arithmetic or formatting change the standard library — or the OR-composed
expression around it — ignores; bookkeeping with no observable output; and a failure only an
unwritable filesystem or a second OS user could produce. Each was verified by hand (the
mutation applied to a file copy passes the whole package suite) before acceptance. One
surprise in the update run, cache.go:492 loop/break, was re-verified with IQ_MUTATION_MUTANT
and found killable — a new bounded-read test had merely turned a previously uncovered line
into a covered one — so it was killed by a test and removed from the baseline, not accepted.
- 4702d30bfae5c4cd2c150a2749ab46c5 drivers/file/cache.go:574 expression/comparison — the DumpPath `<` runs only inside `if out[i].DumpPath != out[j].DumpPath`, so `<=` and `<` agree on every pair that branch can see.
- 9a12822cbaba3824543b385811b459ce drivers/file/cache.go:576 expression/comparison — the File tiebreak `<=` differs from `<` only for two entries with the same file name, which one directory cannot hold.
- 5d6cecd548efb192558ac422ac748d85 drivers/file/cassandra.go:39 conditional/bool-literal — `cr.ReuseRecord = false`; the reader copies the header and reads each row's fields into fresh maps before the next Read, so reuse is an allocation choice with no observable effect.
- 7a06a12c45b6dca481dfe2fd5aee631d drivers/file/cassandra.go:40 numbers/incrementer — `cr.FieldsPerRecord = -1` becomes -2; encoding/csv treats every negative value alike (no width check), and recordForCSVRow still validates the width against the header.
- 43f599983251773dc7d810463ff1148c drivers/file/detect.go:150 expression/remove — drops `!errors.Is(err, bufio.ErrBufferFull)`; the peek asks a 4096-byte bufio.Reader for 512 bytes, so ErrBufferFull is unreachable and the conjunct is inert.
- 096350411b1ca4f288cc8a590ab26c74 drivers/file/detect.go:161 expression/comparison — `len(head) > 512` becomes `>= 512`, which differs only at exactly 512, where `head = head[:512]` is the identity.
- f5f3cd412a70e9727295719eb6854896 drivers/file/detect.go:161 numbers/decrementer — `> 511` admits the same single extra length, 512, where the truncation is again the identity; the incrementer `> 513`, which does change the sniffed head, is killed by TestDetectSniffWindow.
- e97a69fca836d2c77f988578c6c9d6c2 drivers/file/detect.go:221 numbers/incrementer — `len(h) > 0` becomes `> 1`, differing only for the one-byte head "[": stepping past it leaves "" and not stepping leaves "[", and json.Decoder rejects both, so firstJSONObject reports false either way.
- 248b371646e765dbb2445df91d1c2727 drivers/file/detect.go:250 numbers/decrementer — `int(head[1])<<7`; the four bytes are combined with OR, so with head[3] zero the value never exceeds 0xFFFEFF (under the 16 MiB cap) whichever shift is used, and with head[3] non-zero both forms exceed the cap exactly when the lower bytes are non-zero, so the verdict cannot change.
- 086a59f5a54db6ae79e7ae744c2b77d1 drivers/file/detect.go:250 numbers/decrementer — `int(head[2])<<15`, equivalent by the same OR argument.
- 630fc938740df3516a7d96c6a0f8c55d drivers/file/detect.go:250 numbers/incrementer — `int(head[1])<<9`, equivalent by the same OR argument; the sibling shifts that can cross the five-byte minimum or the 16 MiB cap (<<17, <<23, <<25 and the >> forms) are all killed by TestLooksLikeBSON.
- a2bdbf15126dcf96ad1de5d8ae5377cf drivers/file/neo4j.go:264 numbers/incrementer — strconv.FormatFloat precision -1 becomes -2; strconv selects the shortest representation for any negative precision, so the rendered literal is identical.
- fadcb92636adebbf9e7ca1372b57b3a2 drivers/file/rdb.go:93 expression/comparison — the score `<` runs only inside `if Score != Score`, so `<=` and `<` agree on every pair that branch can see.
- 0a4fab84db634e38ce728550886ffef1 drivers/file/rdb.go:127 expression/comparison — the same guarded-by-!= identity for the stream's millisecond comparison.
- f934ce4c8d9813c87d06db2abb53ebcf drivers/file/rdb.go:95 expression/comparison — the member tiebreak `<=` differs from `<` only for two entries with the same member, which one sorted set cannot hold; the sibling sequence tiebreak at :129, where a dump can repeat an id, is killed by TestStreamValueOrdering.

## internal/pushdown — full-scan equivalents (accepted 2026-08-31, test/pushdown-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 70 escapes, 65 of them
killed by the new guard tables (selectArg, extractPred's shape guards, isNot, isLength,
intLiteral, constString, literalOf) plus the reversed-comparison, rejected-builtin and
truncated-regex rows added to the Compile and portable-regex tables. The four below survive
because the mutation cannot change what the function returns. Each was verified by hand — the
mutation applied to the real file, the whole package suite still green — before acceptance. The
fifth survivor, conjuncts.go:40, was already accepted on an earlier branch and is justified
above.
- 7d928d6fc391cca2558e457578feb8e8 internal/pushdown/pushdown.go:599 numbers/decrementer — `strconv.ParseFloat(t.Number, 64)` → bitSize 63; ParseFloat special-cases only bitSize 32 and sends every other value down the 64-bit path, so the parsed float is identical (same reasoning as the drivers/mongo normalize.go:86 pair).
- 0153fe57aa10de9180740096dab9718d internal/pushdown/pushdown.go:599 numbers/incrementer — the same site with bitSize 65, identical for the same reason.

## drivers/dynamodb — full-scan equivalents (accepted 2026-08-31, test/dynamodb-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 85 escapes, 65 of
them killed by new tests — the typed values the dump reader builds and every decode guard's
own message and cause, the exact attribute value each pushed equality binds, the int64
boundary and the exact-string presentation of an overflowing integer, the rendered table
description entry by entry, the wrapped backend causes, a cancelled batch-get backoff, a
callback error mid-scan, the write paths where a failure must not read as a skip or a silent
drop, and what Open builds (its bounded ListTables probe driven through a local SDK stub,
since the probe runs before the Store's injectable client exists). One further escape needed
no test: Store.region was written by Open and read by nothing, so it was deleted as dead code
and its mutant no longer exists. The update run surfaced nine more escapes that the new tests
had turned from uncovered into covered (the `%w` wraps on the BOOL/NULL/L/SS/NS/BS decode
paths and on Open's two failure paths); those were killed with errors.As assertions on the
cause, not accepted, and their ids removed from the baseline before the closing run verified
the remaining set. The 19 below survive because the mutation cannot change what the code
does, in six classes: a guard the following statement re-derives; a conjunct the expression
around it already implies; a base or bit size the standard library treats identically; the
width of a random jitter, which is a distribution and not a value a test can pin; a reflect
guard the invalid zero Value already satisfies; and a driver-context normalization. Each was
verified by hand — the mutation applied to the real file, the whole package suite still green
— before acceptance.
- 86aeab55ccdf60a8c57b260d950a79b1 drivers/dynamodb/dynamodb.go:125 expression/context-nil — `config.LoadDefaultConfig(ctx, loadOpts...)` → nil ctx. The loader consults ctx only for remote resolution (IMDS region and credentials), which an explicit region plus either static dummy or lazily-resolved credentials never reaches, so the config and error it returns are identical.
- 21e32368a191b718fbc19b867fc8d0a2 drivers/dynamodb/filter.go:29 expression/logical — `if f, ok := compile(pred); ok && f.expr != ""` → `||`. compile reports false only from a `return frag{}, false`, whose expr is "", so both forms are false on every failure and true on every success.
- 5b56a6bee3f7be39963668752a5d0fcd drivers/dynamodb/filter.go:29 expression/remove — the same guard's `f.expr != ""` conjunct dropped; ok is true only for a fragment with a non-empty expr, so the conjunct is inert.
- cef917dc83163a2012ff9f1731e40987 drivers/dynamodb/plan.go:25 expression/logical — the same pair on ExplainPlan's `ok && f.display != ""`, equivalent for the same reason (display, like expr, is non-empty exactly when ok).
- dc7b703757d8953f2392aa9ec1a1dd83 drivers/dynamodb/plan.go:25 expression/remove — the dropped `f.display != ""` conjunct; same reason.
- ea04b88488efbbb39940f97da595cd5f drivers/dynamodb/filter.go:192 numbers/incrementer — `strconv.FormatFloat(t, 'g', -1, 64)` → precision -2; strconv selects the shortest representation for any negative precision, so the bound N literal is byte-identical (same as drivers/file/neo4j.go:264).
- 27719415840fab5cc1c1c95178cafa60 drivers/dynamodb/normalize.go:295 numbers/incrementer — the same FormatFloat identity on the write path.
- 8f40be339f598f2ff82cf2eedc3f51a3 drivers/dynamodb/normalize.go:93 numbers/decrementer — `strconv.ParseFloat(s, 64)` → bitSize 63; ParseFloat special-cases only bitSize 32 and sends every other value down the 64-bit path (same as drivers/mongo normalize.go:86).
- 10cdb27c736ce44ee5012b4cb54422ce drivers/dynamodb/normalize.go:93 numbers/incrementer — the same site with bitSize 65.
- c4a107b075b68a6af3a9749b439cc47b drivers/dynamodb/normalize.go:88 numbers/incrementer — `new(big.Int).SetString(s, 10)` → base 11; only the ok flag is read, and isIntLiteral has already restricted s to an optional sign and decimal digits, each a valid base-11 digit too. The base-9 sibling, where '9' is not, is killed by TestNumberValue.

## drivers/redis — full-scan equivalents (accepted 2026-08-31, test/redis-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 611 mutants, 57 escapes,
three of them the per-command-context entries already accepted above, whose content-hash ids still
match. Of the other 54, 26 were killed by new tests — the cursor walk over more than one SCAN
round, a page error that must abort the walk rather than be masked by a later page that succeeds,
the cursor's own failure, the mid-read race both read paths tolerate (staged with a pipeline hook:
a vanished key is absence, a retyped key is a named read failure), the store's decimal mode
reaching the plain read path's RedisJSON reader, dedupe's collapse, the malformed-document decode
guard, the chunked DEL accounting summed over three chunks, the expiry a rewritten string must not
inherit, the empty-aggregate guards that keep an argument-less command off the wire, the
keyless-record sentinel under both write modes, the zero score a rejected coercion returns beside
its error, Close's error rather than a silent nil, the AUTH redaction's HELLO scoping, the package
init that silences go-redis's global logger (observed by making the library want to log in a child
copy of the test binary, since that logger is a global with no getter which captured stderr at its
own init), and the cause behind every wrapped error those tests reach. Two more needed no test:
queueElements took a context and a pipeliner it never used, so the dead parameters were deleted
and their mutants no longer exist. The 27 below are the remaining 26 plus one the new tests turned
from uncovered into covered (kv.go:308), and they fall into three classes: go-redis's per-command
context, which `(*Pipeline).BatchProcess` discards (the class of the accepted entries above, now
enumerated across the whole package); a short-circuit whose absence reaches a pipeline with no
commands, which `(*Pipeline).Exec` returns from before any round trip; and a negative FormatFloat
precision the standard library treats identically. Covered-code MSI 88.76 -> 94.36.
- df468c19faeee5b4855eb3e06e7f8690 drivers/redis/kv.go:44 expression/context-nil — `p.Type(ctx, k)` → nil; queued command context, discarded by BatchProcess.
- 82cf8c47e08e4bf648c2549f9998eceb drivers/redis/kv.go:65 expression/context-nil — `readerFor(ctx, ...)` → nil; the ctx reaches only the queued value command.
- 8e3cb2b99806944a03766b588f09b7f3 drivers/redis/kv.go:109 expression/context-nil — `p.Get(ctx, key)` → nil; queued command context, discarded by BatchProcess.
- 7d856bc2fba007d175485f9528a6817e drivers/redis/kv.go:111 expression/context-nil — `p.HGetAll(ctx, key)` → nil; same.
- 1fb448713cd88962f83dc9c79c3c175a drivers/redis/kv.go:113 expression/context-nil — `p.LRange(ctx, key, 0, -1)` → nil; same.
- 09c8a3967bc3b986c5b79cf08a22f839 drivers/redis/kv.go:115 expression/context-nil — `p.SMembers(ctx, key)` → nil; same.
- c90adcfa21033ba9fd3b0f7ebec6c2de drivers/redis/kv.go:117 expression/context-nil — `p.ZRangeWithScores(ctx, key, 0, -1)` → nil; same.
- 9235d952f28969607ac6973afc1a54eb drivers/redis/kv.go:119 expression/context-nil — `p.XRange(ctx, key, "-", "+")` → nil; same.
- f60032c63c642e82b7f4a5a12fda2a86 drivers/redis/kv.go:121 expression/context-nil — `p.JSONGet(ctx, key)` → nil; same.
- 8a9bf3c7fc9698a61264d7cbbfb66a2a drivers/redis/write.go:42 expression/context-nil — `p.Del(ctx, r.Key)` → nil; queued command context, discarded by BatchProcess.
- 8fca4cd2ecaabb8998ad75f915d6ddf8 drivers/redis/write.go:43 expression/context-nil — `queueWrite(ctx, ...)` → nil; every command it queues is pipelined, so the discarded context is the only thing that changes.
- e0e06a97c60cab83f63ed7906582cba8 drivers/redis/write.go:73 expression/context-nil — `p.Exists(ctx, r.Key)` → nil; same as :42.
- 21848caa08750bedf1d5471ae101afce drivers/redis/write.go:112 expression/context-nil — `p.Set(ctx, r.Key, v, 0)` → nil; same.
- a04e5bf09f892f9e29ea63165b12d21c drivers/redis/write.go:114 expression/context-nil — `queueHash(ctx, ...)` → nil; its only use of the ctx is the pipelined HSET.
- fd6546d693a3f03c55a54cd937e5fd90 drivers/redis/write.go:116 expression/context-nil — `p.RPush(ctx, ...)` → nil inside queueElements' add callback; the queued RPUSH's context is discarded.
- 4a203a6e60413fcf367ad1a4b0931d60 drivers/redis/write.go:118 expression/context-nil — `p.SAdd(ctx, ...)` → nil in the same callback shape; same.
- 129103cbe3cfcef7266e59a4bdda9da1 drivers/redis/write.go:120 expression/context-nil — `queueZSet(ctx, ...)` → nil; its only use of the ctx is the pipelined ZADD.
- e23f12865e3f460429c444e1bd5a3fef drivers/redis/write.go:122 expression/context-nil — `queueStream(ctx, ...)` → nil; its only use of the ctx is the pipelined XADDs.
- f4519986eedb3f27d6008b866c7acbfc drivers/redis/write.go:172 expression/context-nil — `p.HSet(ctx, r.Key, fields...)` → nil; same as :42.
- 9a00a0bb240ab3ca47176eab991cfa6e drivers/redis/write.go:226 expression/context-nil — `p.ZAdd(ctx, r.Key, members...)` → nil; same.
- 33265e6357968b93d0166f6c3a04ec74 drivers/redis/write.go:258 expression/context-nil — `p.XAdd(ctx, &goredis.XAddArgs{...})` → nil; same.
- 760ad7f757c7ac930a6a6fa68b5bb156 drivers/redis/write.go:277 numbers/incrementer — `strconv.FormatFloat(t, 'g', -1, 64)` → precision -2; strconv selects the shortest representation for any negative precision, so the rendered scalar is byte-identical (same as drivers/dynamodb/filter.go:192).

## drivers/couchdb — full-scan equivalents (accepted 2026-08-31, test/couchdb-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 52 escapes, 50 of
them killed by new tests. Most of the residue was driver behaviour a live CouchDB cannot be
asked to produce — a row that fails to scan, a document body that is not an object, an
iteration that breaks mid-walk, a bulk write reporting a per-document conflict, a reply with
no paging bookmark, a row with neither id nor revision — so those paths are now driven
through kivik's own mock driver (`mockdb`, the same module, so no new dependency) against a
Store holding a scripted `*kivik.Client`. The rest were plain assertion gaps: the four
inspect projections were checked for "non-empty" instead of field by field, documentBody's
identity strip was only ever observed through a round trip, the `$gte`/`$lte` arms of the
range push and the one-branch and empty `Or` were never compiled, and the short page that
ends a `_find` walk saved a round trip nothing counted (the request trace now counts it).
The update run surfaced three more escapes the new tests had turned from uncovered into
covered — the `%w` wraps on parseURL's url failure, on Query's Mango parse failure, and on
findPaged's scan failure; those were killed with `errors.As`/`errors.Is` assertions on the
cause, not accepted, and their ids removed from the baseline before the closing run verified
the remaining pair. The two below survive because the mutation cannot change what the code
does: one is an assignment of the value the variable already holds, the other a conjunct the
guard beside it already implies. Each was verified by hand — the mutation applied to the real
file, the whole package suite still green — before acceptance.
- d02d70f46a0e4f06f96cdc0cb91ee619 drivers/couchdb/write.go:69 expression/remove — upsert's `if _, existed := revs[batch[i].Key]; existed && batch[i].Key != ""` with the key-non-empty conjunct dropped. revs comes from currentRevs, which sends only the batch's non-empty keys to _all_docs, and currentRevsForKeys stores a row only under the id the server echoed back, so `existed` is already false for every keyless record. The conjunct beside it decides every case.

## internal/config — full-scan equivalents (accepted 2026-08-31, test/config-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 26 escapes, 19 of
them killed by new tests. Most were plain assertion gaps: Path's user-config-dir failure and
Load's TOML parse failure had never been produced, Save's error paths were only ever driven
through the happy path, Move reported which handles it moved and RemoveAll which sources it
deleted without either being read back, moveSource never had to keep a group that still had
members, moveGroup never re-targeted a member onto another member, Resolve's group
namespacing was never asked to leave a slashed name alone or to skip the prefix with no group
active, and validateHandle's leading and doubled slash arms were unexercised. Two failures a
hermetic test can produce were added rather than assumed away: a parent path that is a file
(mkdir fails) and a target path that is a directory (rename fails). The migration and
sorting escapes were map-order artefacts — `continue` turned to `break`, a dropped
`sort.Strings` — so those fixtures now carry enough entries and repeat enough loads that
landing on the passing order by chance is impossible. The update run surfaced five more
escapes the new tests had turned from uncovered into covered — the `%w` wraps on the
user-config-dir, TOML parse, mkdir and rename failures, and OptionList's comparison replaced
by `return false`; those were killed with `errors.Unwrap`/`errors.As` assertions on the cause
and a five-key sort read ten times, not accepted, and their ids removed from the baseline
before the closing run verified the seven below. One non-test change came out of it: Groups'
`seen` map held a bool nothing ever read, so it holds `struct{}` now and the set has no value
left to mutate. The seven below survive because the mutation cannot change what the code does
or because the syscall it guards cannot be made to fail in-process. Each was verified by
hand — the mutation applied to the real file, the whole package suite still green — before
acceptance.
- 6e235b84704b53af98d39f9e0ab9dd94 internal/config/options.go:90 expression/comparison — OptionList's `sort.Slice` less function widened from `<` to `<=`. opts is built by ranging a map, so no two Key values are equal, and on distinct keys `<=` is the same predicate as `<`. That the call sorts at all is pinned by TestOptionListSortsByKey, which kills the `return false` mutant on the same line.

## internal/numfmt — full-scan equivalents (accepted 2026-08-31, test/internal-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 3 escapes, none of them
an assertion gap. Two return a named constant that IS the zero value the mutant substitutes, and
the third is a narrowing guard that only has an effect on a 32-bit platform, which the gate does
not run on. Each was verified by hand — the mutation applied to the real file, the whole package
suite still green — before acceptance.
- 7c019bb775c298a196100f0acb68bc55 internal/numfmt/convert.go:41 expression/remove — ConvertNumber's `if i, err := n.Int64(); err == nil && int64(int(i)) == i` with the round-trip conjunct dropped. The conjunct exists so a 32-bit build falls through to the *big.Int branch for a value that overflows its `int`; on the linux/amd64 the gate runs, `int` is 64 bits and the round trip is the identity, so the conjunct is constantly true and no input can separate the two forms. Out-of-int64 literals are covered by the big-int row, which fails `err == nil` two operands earlier.

## internal/diff — full-scan equivalents (accepted 2026-08-31, test/diffquery-mutation branch)
First full scan of the package since the LCS/set-arrays/patch work (the CI `deep-mutate` job was
preempted mid-run, so its list stopped inside the composite mutator): 347 mutants, 18 escapes, 5 of
them already accepted above and one more (diff.go:317) no longer covered. Seven of the thirteen new
ones were killed. Six were the `Summary.Empty` conjunction, which two rows — all-zero and
all-non-zero — cannot separate from a disjunction, a re-associated guard or a dropped conjunct,
since every rewriting agrees on those two; a five-row table now asserts that one non-zero counter
alone makes a summary non-empty. The seventh was the positional fallback's surplus-right Add, whose
value was never read back: the over-the-cap subtest asserted the delta's path and op and let the
`New` field go, so it now compares the whole Change, and a mirrored case pins the surplus-left
Remove the same way (that arm had no coverage at all before). The six below survive because the
mutation cannot change what the code does: four write a field the value it already holds or a
return the caller never reads, one is a guard whose fall-through answers identically. Each was
verified by hand — the mutation applied to the real file, the whole package suite still green —
before acceptance.
- ad6f9cb96664edfdca29fd45e9fe103e internal/diff/diff.go:144 composite/field-clear — `Op: OpAdd` dropped from walkMap's right-only Change literal. OpAdd is iota 0, the zero value of Op and deliberately so ("an unset Op is never a silent Change"), so the cleared field holds the value it was assigned. Same class as the accepted diff.go:184 and diff.go:232 entries above.
- 4d45f48d5f17eb374e9b73b85fabbd4b internal/diff/diff.go:207 composite/field-clear — the same zero-value identity for walkPositional's surplus-right Change literal. The other field-clear on this line, the one that drops `New: b[i]`, is not equivalent and is killed by TestTreeArrayMemoryGuard's whole-Change comparison.
- 3c8e23fa70268383fbbf7bb2ff2a5890 internal/diff/diff.go:390 composite/field-clear — the same zero-value identity for KeyedOpt's right-only ItemDelta literal.
- 21ce2b52bd662b3b6a2f4c5416d8dd48 internal/diff/diff.go:486 numbers/incrementer — the same return, `return 1, false`, discarded for the same reason.

## internal/query — full-scan equivalents (accepted 2026-08-31, test/diffquery-mutation branch)
First full scan of the package (its CI `deep-mutate` job was preempted before the first verdict, so
the escape list came from a local scan): 532 mutants, 77 escapes, 68 of them killed by new tests.
Four themes account for nearly all of them. The `%w` wraps were never followed: eleven of them read
identically as `%v`, so every one now has an `errors.Is`, `errors.As` or `errors.Unwrap` assertion
on the cause rather than a substring check on the message. The ports were never asked which context
they received: Get, ScanBatches, EstimateCount, Store.Query, Putter.Put, the RecordSource and
SourceOpener.Open all now record it, and a marker value proves the caller's context arrives instead
of a substituted nil. The page arithmetic was only ever counted, never sized: a record count that
straddles the default (101 against 100) and a page size of one now pin both the fallback and the
`<= 0` test, in the Copier and in JSONLSource alike, and an exact multiple proves no empty trailing
batch reaches the store. And the failure paths of the dump readers had no tests at all: an encode
failure, a cancelled context, a refusing consumer, a reader error, a malformed typed envelope, a
malformed plain value, a stray `]` where the decoder's own "is there more" probe says no, an
unparsable trailer, a one-byte document, and a line above and below the 16 MiB cap are all driven
now. The remaining three themes were narrower: the bound `source()` function's arity (one or two
arguments, so a zero- or three-argument call must fail at compile), which of two arguments a
type-mismatch message reports, and WriteStat's Overwritten and Skipped counters, which no copy had
ever folded across pages.
The update run surfaced three more escapes the new tests had turned from uncovered into covered —
the `%w` wraps on decodeValue's and JSONSource's decode failures, and the partial WriteStat a copy
returns when a page fails partway; those were killed with `errors.Is`/`errors.As` assertions on the
cause and a putter that fails only on its third batch, not accepted, and the baseline was
regenerated so it holds exactly the nine below.
The nine below survive because the mutation cannot change what the code does. Each was re-run in
isolation with `IQ_MUTATION_MUTANT=<id>` after the new tests landed and escaped again, and the
mechanism was checked against the source of the library it depends on.
- d25802f1e59c4bea621c28f39ce8f1ce internal/query/copy.go:267 numbers/incrementer — scalarKey's `strconv.FormatFloat(t, 'g', -1, 64)` precision becomes -2. strconv's ftoa takes `shortest := prec < 0`, so every negative precision is the same request: the smallest digit count that round-trips. -1 is the documented spelling of it, not a magnitude.
- 03128e9587f09b02833dfea08be65ead internal/query/dump.go:51 arithmetic/bitwise — the scanner's initial buffer size `64 << 10` becomes `64 >> 10`, i.e. 0.
- 50515d01f31f491ea13616fd1b25e52a internal/query/dump.go:51 numbers/decrementer — the same size becomes `63 << 10`.
- 8ea21c3710dc9d5f6bab1c689ca51b2c internal/query/dump.go:51 numbers/decrementer — the same size becomes `64 << 9`.
- 51b008c9093f675582c640c84d593146 internal/query/dump.go:51 numbers/incrementer — the same size becomes `65 << 10`.
- 89ecb8a92c78788933091fd53c9a717b internal/query/dump.go:51 numbers/incrementer — the same size becomes `64 << 11`. All five are the `initBuf` argument to `sc.Buffer`, an allocation hint and nothing else: bufio.Scanner doubles its buffer on demand (from `startBufSize` when it is handed an empty one) and caps the growth at `maxTokenSize`, so every starting size reaches the same 16 MiB ceiling and accepts and refuses exactly the same lines. The second argument is the one that decides behaviour, and all five of its mutants are killed by TestJSONLSourceLineCap, which reads a line just under the cap and refuses one just over it.
- b56902b659d6f9f8e09226e3005cad2f internal/query/dump.go:155 expression/remove — convertNumber' `if i, err := t.Int64(); err == nil && int64(int(i)) == i` with the round-trip conjunct dropped. The conjunct exists so a 32-bit build falls through to the *big.Int branch for a value that overflows its `int`; on the linux/amd64 the gate runs, `int` is 64 bits and the round trip is the identity. Same class as the accepted internal/numfmt/convert.go:41 entry, which is the same guard in the read path.

## drivers/hbase — full-scan equivalents (accepted 2026-09-01, test/hbase-mutation branch)
First full scan of the package with a live cluster (its CI `deep-mutate` job ran with no HBase
server, so every integration test skipped and its 103-escape list measured the unit tests alone):
743 mutants, 11 escapes, one of them killed before the closing run. The fakes were the whole
problem: they replayed rows back and asserted nothing about the request, so the context bound to
every RPC, the table each raw verb names, the single-version limit, the key-only and
single-column filters and the existence-only pre-read were all invisible. The fake now attaches a
stub region to each call and records its context, table and protobuf, and new tables assert those
directly, which killed the whole context-nil, MaxVersions and filter families at once. The rest
were plain assertion gaps: the `--verbose` trace was checked with one `Contains` and is now
compared line for line per operation, the arity guards had no surplus-argument or missing-argument
rows, the two `family:qualifier` guards were only ever fed a colon-less column so neither empty
half was exercised, the bad-column message never had to name the column it rejected, the raw
verbs' `{"ok": true}` reply went unread, `limit 1` never exercised the inclusive cap, a missing key
sat last in the Get list so a `break` looked like a `continue`, `parseURL`'s error table asserted
only that an error came back and not which guard produced it, `toInt64`/`toFloat64` were never
called directly so their zero-beside-an-error and their base and bit width went unpinned,
`guardCell` was never asked to sort more than one candidate, and no test drove a backend failure
past the client, the admin or the encoder. One integration test now pins the fields `Open` wires
out of the url, which is what the four `Store`-literal field-clears needed.
The update run surfaced one escape the new tests had turned from uncovered into covered — the
`%w` wrap on `parseURL`'s query-parse failure; it was killed with an `errors.As` assertion on the
`url.EscapeError` cause, verified with `IQ_MUTATION_MUTANT`, and its id removed from the baseline
by hand before the closing run verified the ten below. Covered-code MSI 82.66 -> 98.49.
The ten below survive because the mutation cannot change what the code does, or because the
failure it guards cannot be produced in bounded time. Each was checked against the source of the
library it depends on, and the three `Open` entries were probed twice with real connections.
- 8f294e8c90fdcb1f13cb00b98a822f0a drivers/hbase/normalize.go:219 numbers/decrementer — toFloat64's `strconv.ParseFloat(n, 64)` bit width becomes 63.
- e55c46ed4e95912550a77f73604d5c8a drivers/hbase/normalize.go:219 numbers/incrementer — the same width becomes 65. strconv's `parseFloatPrefix` branches on `bitSize == 32` and nothing else, so 63, 64 and 65 all take the identical `atof64` path and return the identical value and error; unlike ParseInt, ParseFloat has no range check to widen or narrow. The ParseInt call two functions above is the opposite case, and its bit-width mutants are killed by the largest- and smallest-int64 rows of TestToInt64.
- e489c6e21e9851bf6a0bdaa23d41f351 drivers/hbase/normalize.go:253 statement/return — colTypeFor's `return ctAuto` for an undeclared column becomes `return 0`, identical for the same reason. The declared branch above it returns the stored type and is killed by the typed rows of TestRowFromCells.

Neo4j, accepted 2026-09-07 after the first full scan of the package (687 mutants, 48 escapes;
37 killed). Every entry below survives the whole suite, and each was confirmed by hand: apply the
mutant diff, run the package tests, see them pass, restore. The five context-nil entries are true
for neo4j driver v5.28.4 only; a driver upgrade may make them killable while they stay recorded
here, so re-check them when the SDK moves.

- dce9edc83ff61c97a206243ee0015234 drivers/neo4j/inspect.go:66 expression/context-nil — readRows passes a nil context to session. Driver v5.28.4 NewSession gives the context to computeCacheKey alone, which ignores it unless SessionConfig.Auth is set, and this driver never sets it.
- 478cf8fc2fb53c4d33b07c1ce9e04374 drivers/neo4j/inspect.go:67 expression/context-nil — the deferred sess.Close(nil). The result is discarded and the context reaches only pool and router cleanup, which no caller can observe.
- a9e25828b1be84d62366cdd519c5fed3 drivers/neo4j/neo4j.go:189 expression/context-nil — drv.Close(nil) on an Open error path. driverWithContext.Close sets d.pool = nil before it reads the context, the return is discarded by `_ =`, and Open returns the same error and no Store, so nothing observable differs.
- aeff47385385d4d61b1ad2a7f3346251 drivers/neo4j/neo4j.go:205 expression/context-nil — the same Close(nil) on the second Open error path, for the same reason.
- 5f5f9808a15d113e86883c429ea2b0fb drivers/neo4j/neo4j.go:261 expression/context-nil — NewSession(nil, ...). Same as inspect.go:66: the context reaches computeCacheKey alone and is ignored without SessionConfig.Auth.
- d337011e0e53169a08882e5f038ad66a drivers/neo4j/neo4j.go:443 expression/context-nil — s.session(nil, ...). Same reason as neo4j.go:261.
- 811eef4ad362892a16888080cfd62d01 drivers/neo4j/neo4j.go:537 expression/context-nil — driver.Close(nil). d.pool = nil happens before the context is used, so the driver closes either way and the context reaches only socket teardown.
- 3804221283486b6225fcc6969342d4a1 drivers/neo4j/neo4j.go:311 expression/remove — the `s.target.key != ""` conjunct of the duplicate-key guard. Without ?key= the key is elementId, and WHERE elementId(n) IN $ids cannot return one id twice, so dup is never true on that branch.
- 413b18704336923c1f7c242b795d1f94 drivers/neo4j/normalize.go:84 numbers/incrementer — the -1 precision of strconv.FormatFloat. strconv treats every negative precision as shortest (`shortest := prec < 0`), so -1 and -2 produce the same text.
- 54c1a923120c4509f713c3cc8089b171 drivers/neo4j/write.go:181 expression/remove — the `s.target.key != ""` conjunct of Delete's duplicate-key guard, for the same reason as neo4j.go:311.

Elasticsearch, accepted 2026-09-09 after the first full scan of the package. Every entry below
was confirmed by hand: apply the mutant diff, run the package tests, see them pass, restore.
The closing run scored 599 killed, 9 equivalent and 90 not covered. Read the note on
elasticsearch.go:133 before you touch that line: the site holds two operand variants and only
one of them is equivalent.

- 2caea306608e3d24aea0ce6532721adf drivers/elasticsearch/elasticsearch.go:130 statement/return — the mutant returns the flavor 0 for the elasticsearch scheme. flavorES is the first iota value, so it is 0 already and the program does not change. Applied by hand: the package suite passes.
- 4fa7d370b448bb06ea859ebdb5dd08f4 drivers/elasticsearch/elasticsearch.go:132 statement/return — the same flavor 0 for the elasticsearch+s scheme, for the same reason.
- 623005b05d81beb25bc45b14e310ada9 drivers/elasticsearch/filter.go:50 composite/field-clear — the class: stringClass field of the keyword and ip case. stringClass is iota, so it is the zero value of fieldClass and clearing the field is a no-op.
- a012bb4171c8f10389864efbe5865517 drivers/elasticsearch/filter.go:59 composite/field-clear — the same field on the text sub-field case, for the same reason.

Couchbase, accepted 2026-09-26 after the verdict re-scan of the package (657 mutants, 579
killed, 15 escaped, 63 not covered, 0 errors). Nine escapes were test gaps and are now killed.
Every entry below was confirmed by hand against a live cluster: apply the mutant diff to the
fixed tree, run the package tests, see them pass, restore. The couchbase.go:383 decode guard
is not here: it moved into readPage, and TestReadPage kills it now.

- 03f465282ea2ab1de8d3cd108574f6e2 drivers/couchbase/couchbase.go:287 composite/field-clear — the Context field of BulkOpOptions in bulkDo. gocb v2.12.4 ignores that field on the couchbase:// KV path (it reads Timeout only), so clearing it changes nothing. bulkDo refuses a cancelled context before the call, and tests kill that check.

Config mode warning, accepted 2026-09-28 in the pull request that adds the warning.

- 0d9a7238636cc3a7370371551e2a6560 internal/config/modewarning.go:19 expression/error-guard — the Path error return in ModeWarning. When Path fails it returns "", and os.Stat("") on the next line fails too, which returns "" as well. The two paths agree for every input.

Negative zero, accepted 2026-09-28 in the pull request that keeps the sign of -0.

- 7b8aaed0ceea6c187853a46f1fdba74f internal/numfmt/convert.go:46 numbers/incrementer — math.Copysign(0, -1) becomes math.Copysign(0, -2). Copysign takes only the sign of its second argument, so both return the same negative zero.
- 5ab21f74bc541533b57a70b6d2c9ab31 internal/query/dump.go:152 numbers/incrementer — the same Copysign(0, -1) to Copysign(0, -2) change in the typed dump reader, equivalent for the same reason.

## internal/numfmt, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-small branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 3 escapes. One was a test gap: the `return s` fallback in ConvertNumber for a token that is not a valid integer literal. The new "malformed integer literal keeps its text" row of TestConvertNumber kills it. The two below are the same edits as the accepted dd0c601a1d63 and 939386efa8e1 entries above, with new ids. Each was applied by hand to the real file, and the full package suite passed.
- 6a0dd0157ce2 internal/numfmt/decimal.go:37 branch/case — replaces dd0c601a1d63 (decimal.go:37 statement/return). The edit is the same, `return DecimalAuto, nil` becomes `return 0, nil`, and v2.10.16 now files it under branch/case. DecimalAuto is the first iota of DecimalMode, so 0 is DecimalAuto.
- e0616ce792cb internal/numfmt/decimal.go:43 statement/return — replaces 939386efa8e1 (decimal.go:43 statement/return). The default arm returns 0 in place of DecimalAuto with the same error. The two values are identical for the same reason.

## internal/diff, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-small branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 6 escapes. One was a test gap: the length check of the array arm in `same`, the LCS anchor predicate. Without it a nested array anchors to a longer array that starts with the same elements. The new "nested array anchoring compares lengths" row of TestTree kills it. Three of the five below are the same edits as accepted entries above, with new ids. Each of the five was applied by hand to the real file, and the full package suite passed.
- 33f8933f85f2 internal/diff/diff.go:260 arithmetic/base — replaces 6d0a9cacd4c7 (diff.go:260 arithmetic/base). The map capacity hint `len(a)+len(b)` becomes `len(a) - len(b)`. A capacity hint only sizes the allocation, and the runtime treats a negative hint as zero.
- cba7aae52682 internal/diff/diff.go:467 branch/if — replaces 1e973c7c2926 (diff.go:467 branch/if). The `return false` body is cleared, so the call falls through to reflect.DeepEqual. That point is reached only when a is not a number and b is one, so DeepEqual also returns false.
- e3b8b730e858 internal/diff/diff.go:486 numbers/decrementer — replaces 38e51015625f (diff.go:486 numbers/decrementer). asFloat returns -1 in place of 0 with ok false. Both callers read the float only when ok is true, so the value is never used.
- bbf28e84e5c2 internal/diff/diff.go:486 branch/case — new in v2.10.16. The mutator replaces the body of the default arm with a return of the zero values, `return 0, false`. That is the original statement, so the mutant is the original code with a different indentation.
- 889bc176c322 internal/diff/diff.go:493 composite/field-clear — the `Op: OpAdd` field of the Change that `addition` returns is cleared. OpAdd is the zero value of Op (iota), so the cleared field holds the same value. The four old entries of this class at the inline Change literals collapsed into this one helper.
- 69a6bab3fd68 internal/diff/patch.go:27 expression/error-guard — new in v2.10.16. The error guard after json.Marshal(patch) becomes `if false`. jsondiff.Compare marshals both inputs and unmarshals them into plain JSON values before it builds the patch, so every operation value is a map, slice, string, float64, bool or nil. json.Marshal cannot fail on those values, so the guard is unreachable. A value that cannot be marshaled fails earlier in Compare, and TestPatchMarshalError asserts that error.

## internal/render, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-small branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 9 escapes. Eight were a test gap: v2.10.16 adds a field-clear mutant for each of the eight fields of the jsonColors palette, and no test compared the exact colored bytes. TestNewJSONEncoderColorsEachRole now pins the output of each syntax role (key, string, number, bool, null, bytes, time and punctuation), and each of the eight clears fails it when applied by hand. The one below is the same edit as an accepted entry above, with a new id. It was applied by hand to the real file, and the full package suite passed.
- 7d94bfbcfb47 internal/render/json.go:67 statement/remove — replaces bed35ced74f6 (json.go:67 statement/remove). The `enc.SetSortMapKeys(true)` call is removed. jsoncolor.NewEncoder v0.9.1 starts with `flags: EscapeHTML | SortMapKeys`, so the call sets a bit that is already set. The flip to `false` on the same line is killed by TestNewJSONEncoderColoredSortsMapKeys.

Keyring default store, accepted 2026-09-29 in the pull request that makes the keyring the default store of `iq add`.

- 9a221f56b0b51dedaf1de1918384b218 cmd/password.go:32 branch/case — the ErrNotFound case of keyringFree returns nil. The mutator replaces the case body with a zero-value return, which is `return nil` again, so the mutant is the same code.
- 9aeae887cb1de5c888584aacb549d977 cmd/password.go:113 expression/error-guard — the UseKeyring error guard in keepPassword. The add command added the source to the config a few lines earlier with cf.Add, and UseKeyring fails only for an unknown source, so the branch cannot be reached. mutago scored the same guard KILLED in one earlier run; with the mutation applied by hand, the full cmd suite passes.
- f47547a1950ba78bf71b5349ab4cb2b1 cmd/password.go:122 expression/error-guard — the SetSourceURL error guard in the fallback of keepPassword. The source exists and the raw URI passed the scheme and parse checks, so it is not blank. SetSourceURL fails only for an unknown source or a blank URL, so the branch cannot be reached.
- a4302bede0d913e4708c747159c3f4ba cmd/password.go:122 conditional/negated — the same guard with `err == nil`. SetSourceURL returns nil, so the mutant returns `false, nil` from the guard, which is what the next line returns.
- e4dd7cceaed99e103693bed574cca5ae cmd/inspect.go:411 loop/break — the `continue` that skips the columns read when no table is set, in the reads builder of inspectCassandra. columns is the last name in cassandraInspectCmds, so `break` at that name ends the loop at the same point as `continue`. Both forms build the same map.
- e1c81375bec9e0cb1043c2e2146bd3d0 cmd/diff.go:262 statement/remove — `meter.Stop()` in `metered`. newProgressMeter returns nil for a writer that is not a terminal, so under test the call does nothing. The same reason as the 78166f5f entry above.

## internal/rawpred, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-2 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 13 new escape ids. Five were a test gap. jsonparser hands a string element back without its quotes, so the string "{}" reads as an empty object, and no test gave evalElement such an element. New rows in TestEvalElemMatch and TestEvalNoneMatch now require unknown for it. Without the type check an ElemMatch drops that document, but jq errors on it. TestCountKeys pins the (0, false) result of countKeys, and TestRanks now pins the 0 that valueRank returns with ok false and requires that no rank is 0. Each of the five was applied by hand and failed the package suite. The rawpred.go:130 statement/return escape keeps its accepted id 13548ffece9a. The eight ids below cover 11 sites. Each site was applied by hand to the real file, and the full package suite passed.
- 11979afff782 internal/rawpred/rawpred.go:604 branch/case — new in v2.10.16. The default arm of evalAny gets its own statement back with a different indentation, so the program does not change.
- 744e052285de internal/rawpred/rawpred.go:804 branch/case — new in v2.10.16. The same indentation-only edit on the default arms of docRank (line 804) and valueRank (line 817). The id covers both sites.
- a237d926ff29 internal/rawpred/rawpred.go:596 branch/if — new in v2.10.16. The `return anyState{}, false` body in evalAny is cleared, so a field that is not found falls through to the type switch. getField returns jsonparser.Unknown as the type on every path that is not fieldFound, so the switch takes its default arm and returns the same `anyState{}, false`.
- df7620fb7781 internal/rawpred/rawpred.go:762 branch/case — replaces b65de6a58f1a (rawpred.go:728 branch/case). The `continue` of the whitespace arm in isObject is removed. The switch is the last statement of the loop body, so the loop reads the next byte either way.
- 43d0f4633226 internal/rawpred/rawpred.go:762 loop/break — replaces 3c63527171ee (rawpred.go:728 loop/break). The same `continue` becomes `break`. In a switch, `break` leaves the switch and not the loop, so the loop also reads the next byte.
- 3e984136a4de internal/rawpred/rawpred.go:613 expression/error-guard — replaces d0db686f2471 (rawpred.go:608 expression/error-guard). The `if cbErr != nil` guard in foldArray becomes `if false`. The old reason is stale: jsonparser v1.2.0 now passes a parse error to the callback. But ArrayEach returns that same error right after the callback, and foldArray then returns `anyState{}, false` and discards s. So the extra s.add call of the mutant has no effect on the result.
- 195c3ea27153 internal/rawpred/rawpred.go:779 numbers/incrementer — new in v2.10.16. `nullRank = iota + 1` becomes `iota + 2`. All six ranks move up by one, so their order stays the same and no rank becomes 0. The code only compares ranks with each other. The `iota - 1` and `iota + 0` edits on the same line make a rank 0, and TestRanks kills them.

## internal/jqfmt, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-2 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 18 escapes, and 16 were a test gap. v2.10.16 adds a field-clear mutant for each entry of the builtinDesc table, and TestExplain read only the length and keys entries. TestExplainBuiltinTable now pins the note of each of the 17 builtins. No test asked for a colored explain, so the dropped `colored` field of the printer in stageText escaped. TestExplainColoredStageText now requires the colored pretty print for each stage. The removed `p.suffixes(t)` call in leafTerm escaped because no golden case had a one-line call with a suffix. The `env.HOME` and `first(.a).b` rows of TestFormatGolden kill it. Each of the 16 was applied by hand and failed the package suite. The two below were applied by hand to the real file, and the full package suite passed.
- 51bd6749f549 internal/jqfmt/jqfmt.go:546 branch/case — new in v2.10.16. The default arm of termNeedsBreak gets its own `return false` back with a different indentation, so the program does not change.
- 28d4238337bf internal/jqfmt/jqfmt.go:726 branch/case — new in v2.10.16. The same indentation-only edit on the `return ""` default arm of describeTerm.

## internal/config, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-2 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 19 new escape ids, and 10 were a test gap. v2.10.16 turns an error guard into `if false`, so each guard now needs a test that takes its error path. TestLoadFailures and TestSaveFailures now make Path fail, make Load read a directory, and make Save create its temp file in a directory it cannot write (skipped on Windows and for root). TestSaveCreatesDir now requires mode 0700 on the new directory. No test in the package called List, so three edits of its sort escaped. TestList reads a six-source list ten times. Remove and RemoveAll never had to keep a group that still has members, and new rows in TestRemove and TestRemoveAll do that. Each of the 10 was applied by hand and failed the package suite. The update run found one more escape that the new tests had made covered: the `%w` wrap of the read failure in Load. An errors.As check on the *fs.PathError cause now kills it, so it is not in the baseline. The modewarning.go:19 and options.go:90 escapes keep their accepted ids 0d9a7238636c and 6e235b84704b. Six of the nine below are the same edits as accepted entries above, with new ids. The nine ids cover 11 sites. Each site was applied by hand to the real file, and the full package suite passed.
- 2a0c1f51551b internal/config/config.go:561 branch/case — new in v2.10.16. The default arm of isHandleRune gets its own `return false` back with a different indentation, so the program does not change.
- 0e4e5234cdbd internal/config/config.go:474 expression/comparison — new in v2.10.16. The less function of the sort in List changes from `<` to `<=`. The names are the keys of the Sources map, so no two are equal, and on distinct names `<=` gives the same result as `<`. This is the same class as the accepted 6e235b84704b entry. The `>=`, `return false` and removed-sort edits on the same line are killed by TestList.
- 641d1216c473 internal/config/config.go:166 expression/error-guard — replaces 5986e10e4428 (config.go:166 expression/error-guard). The Chmod guard in Save becomes `if false`. The old reason holds: fchmod on a temp file that this process just created fails only for EROFS or EPERM, and an unwritable directory makes os.CreateTemp fail first.
- 6efbb69d231f internal/config/config.go:170 expression/error-guard — replaces 061b6cb1bc1b (config.go:170 expression/error-guard). The TOML encode guard becomes `if false`. The old reason holds: the encoder fails only when the write fails, and a write to a new private temp file fails only on a full disk.
- a482f07d6b55 internal/config/config.go:174 expression/error-guard — replaces 0df46f720e89 (config.go:174 expression/error-guard). The Close guard of the temp file becomes `if false`. The old reason holds: close on a local file fails only for a delayed write error, which no test can cause.
- 548bd8505d7b internal/config/config.go:250 expression/remove — replaces d227e4fa4233 (config.go:250 expression/remove). The `c.Group != ""` operand of the group check is removed in moveSource (line 250), Remove (line 355) and RemoveAll (line 412). The id covers the three sites. The removed operand is false only when Group is already "", and then the body sets Group to "" again.
- 50512d894333 internal/config/options.go:49 branch/if — replaces 8727d6e6f7cd (options.go:49 branch/if). The `return "", false` body in GetOption is cleared. optionsFor gives ok false only with a nil map, and a read of a nil map gives the same "" and false.
- 5422040feb4d internal/config/options.go:83 expression/remove — replaces 5d5732b03749 (options.go:83 expression/remove). The `handle != ""` operand in OptionList is removed. optionsFor gives ok true for every empty handle, so `!ok` is already false where the removed operand is false.
- 40e2d3779af2 internal/config/options.go:74 statement/remove — new in v2.10.16. The write back `c.Sources[full] = src` in UnsetOption is removed. src is a copy of the Source struct, but its Options field is the same map as the stored one, so the delete on the line above already changed the stored source.

## internal/shape, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-3 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 28 escapes. Nine had an accepted id that did not change, and 19 had a new id. Ten of the new ids were a test gap. A json.Number with 400 digits has no ".eE" but does not fit a float64, and the new row of TestInferKinds requires integer for it. No test merged enum state in a map collapse, so five edits of enumAcc.mergeFrom escaped. TestJSONSchemaEnumMergeInMap now requires that an over value keeps the merged enum over, that nine distinct strings across the values set over, and that eight distinct strings still give an enum. The formats.mergeFrom edits that keep only one side of the intersection escaped, because no test merged one plain string with many carriers. TestInferMapsFormatMergeOnePlain does that. A one-side edit gives the format of a carrier in 40 of 41 merge orders, and each corpus is inferred ten times. These two new tests also kill six old ids that were accepted as order-dependent: 3309d6f97333, 457d6848da6d and 6be5c0bb2756 (keep the first side) and aa907ec49a4d, bf8096272848 and e9849fa5dcec (assignment removed). They also kill fcc263bd5b29 and 67c061d70537 at shape.go:620. TestInferIntegerCollapseKeepsOtherKinds projects five kinds 20 times, so the old order-dependent 6b1f8a784f4f (`continue` to `break` at shape.go:244) now fails in almost every pass. Each of these was applied by hand and failed the package suite. The old 38583e7a2e76 (shape.go:487 expression/remove) still escapes with the same id. The removed operand is `!math.IsNaN(f)`, and `NaN == math.Trunc(NaN)` is false, so a NaN still gives kindNumber. Each of the eight below was applied by hand to the real file, and the full package suite passed.
- c8cef7cee018 internal/shape/shape.go:461 branch/case — new in v2.10.16. The nil arm of kindOf returns 0 with a different indentation. kindNull is the first iota of kind, so 0 is kindNull.
- d4b2f616e4ae internal/shape/shape.go:587 branch/case — new in v2.10.16. The default arm of formats.resolve gets its own `return ""` back with a different indentation, so the program does not change.
- 8028e5c64c63 internal/shape/shape.go:525 branch/if — new in v2.10.16. The `return false` body of the regexp check in matchDate is cleared, so every string goes to time.Parse with the layout "2006-01-02". That layout accepts only four ASCII digits, a dash, two ASCII digits, a dash and two ASCII digits, with nothing after them. That is the same shape as the regexp `^\d{4}-\d{2}-\d{2}$`, so the regexp only saves a parse.
- 752c26a125ae internal/shape/shape.go:577 branch/if — new in v2.10.16. The `return ""` body for a node that saw no string is cleared in formats.resolve. hasString is false only on the zero value of formats: observe sets it with the three flags, and mergeFrom copies o only when o.hasString is true. So the three flags are false, and the switch returns "" from its default arm.
- 9785c3f69ec5 internal/shape/shape.go:605 branch/if — new in v2.10.16. The `return` body for an enum that is over is cleared in enumAcc.observe, so vals fills again after over. over is never set back to false. values returns nil while over is true, and mergeFrom never reads the vals of a side that is over, so the extra values have no effect.
- c40445fcd328 internal/shape/shape.go:308 expression/remove — replaces c6d581179b01 (shape.go:308 expression/remove). The `n.kinds.has(kindArray)` operand of the items check is removed. accumulate sets elem only in the array case, where kindOf has added kindArray to the same node. mergeNode creates dst.elem only for a src with an elem, and it adds the kinds of src to dst. So elem is not nil only when the node has kindArray.
- 957826442f65 internal/shape/shape.go:620 expression/remove — replaces a489e3fe1948 (shape.go:620 expression/remove). The `e.over` operand of the over check in enumAcc.mergeFrom is removed. When only e is over, the mutant goes on to the loop, and e.over stays true, because no path sets it back to false. values returns nil while over is true, so the enum is the same. The `o.over` removal on the same line is killed by TestJSONSchemaEnumMergeInMap.
- bf24e4cb4666 internal/shape/shape.go:354 statement/remove — replaces 766fe5db8afb (shape.go:354 statement/remove). The `seen[name]` write in schemaTypes is removed, so a JSON Schema name that two kinds share is not deduplicated. Only kindObject and kindMap share a name ("object"). A node gets kindMap only through kinds.replace, which removes kindObject. inferMaps collapses a node before it visits the children, so mergeNode never merges a node that holds kindMap. So no node holds both kinds, and no name comes twice.

## internal/parquetout, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-3 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The CI scan of the package on f566ec9 found 25 escapes, and 21 were a test gap. TestToInt64 and TestToFloat64 read the value only when ok was true, so the 16 edits of a `return 0, false` to -1 or 1 escaped. Both tests now require the 0 in every row. These rows also kill the old 7865be46f9ec and f54a44ed2ade (writer.go:329). The new "2^63 as float is out of range" row of TestToInt64 kills the `>=` to `>` edit in floatToInt64 and the old 5ce25ced3604 (writer.go:359). float64(math.MaxInt64) rounds up to 2^63, which int64 cannot hold. v2.10.16 adds const mutants, and the tests read the sample and page sizes from the constants, so a change of a constant also changed the test. TestWriterSampleSize and TestWriterPageSize now use the literal counts 1000 and 500. Each of the 21 was applied by hand and failed the package suite. The three ids below cover four sites. Each site was applied by hand to the real file, and the full package suite passed.
- f422a8f89bf2 internal/parquetout/writer.go:85 expression/error-guard — replaces 12e79a7c2825 (writer.go:85 expression/error-guard), whose proof was pending. The error guard after pqarrow.NewFileWriter becomes `if false`. In arrow-go v18.6.0, NewFileWriter returns an error only from the schema steps: ToParquet, the schema normalize for WithStoreSchema, and NewSchemaManifest. inferPlan builds the schema from a closed set of types: int64, float64, boolean, utf8, timestamp[ns, UTC], date32, list, map with a utf8 key that is not nullable, and a struct with at least one field (objectType returns arrow.json for an object with no properties). Each of these converts without an error at the default writer properties. A failed write of the magic header is a panic in file.NewParquetWriter, not an error. So the branch cannot be reached.
- 907b6d8fa486 internal/parquetout/writer.go:94 statement/remove — new in v2.10.16. The `pw.sample = nil` line in start is removed. start sets started to true first, so Add never appends to the sample again and Close never calls start again. The line only lets the garbage collector free the sample, so no output changes.
- 86a6546efa5e internal/parquetout/writer.go:339 branch/case — new in v2.10.16. The default arms of toInt64 (line 339) and toFloat64 (line 403) get their own `return 0, false` back with a different indentation, so the program does not change. The id covers both sites.


## internal/pushdown, mutago v2.10.16 re-baseline (accepted 2026-09-30, test/mutation-rebaseline-3 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The CI scan of the package on f566ec9 found 56 escapes. Three kept their old ids, and 35 were a test gap. extractExact and exactPipe had no guard tests, so each dropped or misjoined clause of their shape guards escaped. TestExtractExactShapeGuards and TestPipeShapeGuards now give both extractors a query with no term, an operator query that carries a term, a term with no function, and a builtin with a suffix. TestPathOf gives pathOf one row per guard clause, and new rows of TestConstString give constString an operator query and a query with no term. New rows of TestCompileNotPushable reject a filter that is not streamable, an optional path, a path piped into a path, and a builtin with a suffix, with and without `| not`. TestUnwrapDepthCap pins the cap of 512 wrappers with literal depths. The `continue` to `break` edit in Compile stops at the first stage that is not a select. The new "an identity stage between two selects" row of TestCompilePushable requires the select after a `.` stage. Each of the 35 was applied by hand and failed the package suite. The old entries 3662a8d1c95d (conjuncts.go:40), 7d928d6fc391 and 0153fe57aa10 (pushdown.go:599) still escape with the same ids, and their reasons hold. The 11 ids below cover 18 sites. Each site below was applied by hand to the real file, and the full package suite passed.
- a8ff75800f75 internal/pushdown/pushdown.go:166 statement/return — the same site as a14ba2b8f94c. flip returns 0 in place of predicate.Gt for Lt. Gt is the first iota of predicate.Op, so 0 is Gt.
- 46d886441bcf internal/pushdown/pushdown.go:166 branch/case — new in v2.10.16. The Lt arm of flip becomes `_ = predicate.Gt` and `return 0`. 0 is Gt, as for a14ba2b8f94c.
- 781445a762d4 internal/pushdown/pushdown.go:300 branch/if — new in v2.10.16. The body of `if !ok` in negate is cleared. extractExact returns a nil node with every false ok, so the type switch goes to its default arm and returns nil, false.
- 5f0cba885491 internal/pushdown/pushdown.go:405 branch/if — the same site as 335919d48799. The body of `if !ok` in intLiteral is cleared. literalOf returns a nil value with every false ok, so the float64 assertion fails and the function returns 0, false.
- 5616b72b9e59 internal/pushdown/pushdown.go:422 branch/if — new in v2.10.16. The body of `if !ok` in stringLit is cleared. literalOf returns a nil value with every false ok, so the string assertion gives "" and false.
- bdbdd9ff2f20 internal/pushdown/pushdown.go:42 branch/case — new in v2.10.16. The arm gets its own `return nil, false` back with a different indentation, so the program does not change. The id covers the byte-identical arms at lines 136, 184, 215, 310, 350, 379 and 616.
- 461a79b3c892 internal/pushdown/conjuncts.go:154 branch/case — new in v2.10.16. The same indentation edit in the default arm of firstUnsafeComponent.

## internal/query, mutago v2.10.16 re-baseline (accepted 2026-09-30, test/mutation-rebaseline-4 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 49 escapes. Eight kept their old ids (copy.go:267, dump.go:152, dump.go:155 and the five initBuf edits at dump.go:52). The other 41 had a new id, and 40 of them were a test gap. The compile guards of Run, RunKeyed, the cross engine and the combiner had no test with a filter that parses but does not compile. RunKeyed had no parse, fetch or estimate test. JSONSource and YAMLSource had no test of their page sizes, a cancel, a keyless record, a first-read error or a YAML syntax error. Transform had no filter that fails at run time, and no copy failed on its last flush. Each new test was proved by hand: the mutation applied to a copy of the file makes the package suite fail.
The scan also disproves the old reason for b2cd4944894a (dump.go:188 expression/remove, now at line 202). A reader error that wraps io.EOF inside a top-level array makes Decode return an error that matches io.EOF. Without the `!array` operand the walk then ends in silence and reports a truncated array as complete. TestJSONSourceReportsAnEOFReadErrorInsideAnArray now kills it. The old id stays in the baseline, but its reason does not hold.
The update run surfaced five more ids that the new tests had turned from uncovered into covered. Four were killed: the %w wrap of the item filter error in Transform (copy.go:189), a consumer that refuses a full page in JSONSource and YAMLSource (dump.go:213 and dump.go:274, one id), and white space before a top-level array (dump.go:236 conditional/negated and numbers/incrementer). Their ids were removed from the baseline by hand before the closing run.
- f005955aac26 internal/query/dump.go:236 expression/error-guard — new in v2.10.16. The guard on br.Discard(1) in startsJSONArray becomes `if false`. The loop reaches Discard only after br.Peek(1) returned a byte, so that byte is in the buffer, and bufio.Reader.Discard of buffered bytes returns no error. The same class as the drivers/file/filter.go:197 entry.

## drivers/file, mutago v2.10.16 re-baseline (accepted 2026-09-30, test/mutation-rebaseline-4 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 68 escapes. Eighteen kept their old ids. The other 50 had 45 new ids, because mutago gives byte-identical mutants one id: c5cb81f8778b also covers cache.go:169, 83e6e22888ec also covers cache.go:170, f3a87a702590 also covers cache.go:463 and cache.go:471, and 57437d498f41 also covers cache.go:472. Nine new ids were a test gap, and each new test was proved by hand: the mutation applied to a copy of the file makes the package suite fail.
The three array readers had the gap that matters most. The scan disproves the old reasons for fa325da92f2a (filter.go:138 expression/remove) and eee383190b76 (mongo.go:106 expression/remove). Since Go 1.27, encoding/json runs on the json/v2 decoder. A reader error that wraps io.EOF inside a top-level array makes Decode return an error that matches io.EOF. Without the `!array` operand, the raw prefilter, the mongoexport reader and the APOC reader then end in silence and report a truncated array as complete. The original code reports the error. New tests kill all three (TestScanFilteredReportsAnEOFReadErrorInsideAnArray, TestExtJSONSourceReportsAnEOFReadErrorInsideAnArray, TestNeo4jSourceReportsAnEOFReadErrorInsideAnArray). The old ids stay in the baseline, but their reasons do not hold. The scan also disproves the reason for 83eab4bb13c7 (cache.go:576 statement/return): sort.Slice is not stable above 12 entries, so the file name tiebreak does change the order. TestListCacheTieBreaksByFileNameAtScale kills it.
The other new kills: a prefiltered scan of a dump that was removed after Open (filter.go:79), Open of a dump whose format it cannot detect (file.go:90), a %w check on the trailer error of readCache (cache.go:193), a RemoveCache walk with a .cbor file after a skipped entry (cache.go:604), and the panic of the two CBOR mode builders on a bad option (cbordump.go:46 and cbordump.go:55).
Each entry below was applied by hand to a copy of the file, and the whole package suite passes.
- 29af2feed21c drivers/file/cache.go:122 expression/error-guard — replaces 6d61c0aa8f84 (cache.go:122 expression/error-guard). The old reason holds: filepath.Abs gives "" with its error, and the next os.Stat("") fails, so the function returns the same `cacheMeta{}, false`.
- c5cb81f8778b drivers/file/cache.go:164 expression/error-guard — new in v2.10.16. The os.Open guard in cacheFresh becomes `if false`. f is then a nil *os.File, whose Read and Close return os.ErrInvalid, so readHeader fails and the function returns false. The same id covers the readHeader guard at line 169: readHeader gives a zero header with its error, and cacheable makes m.size at least the size floor, which is at least 1, so `h.Size == m.size` is false.
- 83e6e22888ec drivers/file/cache.go:165 branch/if — new in v2.10.16. The `return false` bodies at lines 165 and 170 are cleared. The function then falls through to the same false for the reasons of c5cb81f8778b.
- 0df8cf0a5627 drivers/file/cache.go:188 expression/error-guard — new in v2.10.16. The fileSize guard in readCache becomes `if false`. fileSize calls Stat on a file that os.Open opened in the line before, and fstat on an open descriptor fails only for a kernel I/O fault. No hermetic test can make it fail. fileSize itself is covered by TestFileSizeReportsAStatFailure.
- e85d315b99a7 drivers/file/cache.go:292 expression/error-guard — replaces fe55b530d196 (cache.go:292 expression/error-guard). The old reason holds: writeHeader fails only if a write to the temp file that the line before created fails.
- f250ccd3990d drivers/file/cache.go:369 expression/error-guard — new in v2.10.16. The guard on cborEnc.Marshal of the cache header becomes `if false`. The header is a fixed struct of one string and four integers, and the encoder with default options fails only for a type it cannot encode. Probed with a non-UTF-8 path, a NUL byte, and extreme integers: all encode with no error.
- 57437d498f41 drivers/file/cache.go:459 branch/if — new in v2.10.16. The `return nil, false` bodies at lines 459 and 472 are cleared. The function then gives the same `nil, false` for the reasons of f3a87a702590.
- 0e85f403fc96 drivers/file/cache.go:475 expression/remove — replaces c7fa9b921071 (cache.go:475 expression/remove). The old reason holds: readIndex gives a zero indexBlock with its error, so `len(idx.Pages) == 0` is already true.
- 760492cbcbc3 drivers/file/cache.go:614 statement/return — replaces 83535655adb5 (cache.go:614 statement/return). The old reason holds: all cache files are in one directory, whose permissions decide if a remove fails, so a failing run has removed nothing.
- 9dd45cbc07ee drivers/file/cache.go:625 expression/error-guard — new in v2.10.16. The os.Open guard in headerOf becomes `if false`. readHeader on the nil *os.File fails with os.ErrInvalid, so the next guard returns the same `cacheHeader{}, false`.
- e69e4896a635 drivers/file/cassandra.go:99 branch/if — replaces a5776bc0b54a (cassandra.go:99 branch/if). The old reason holds: bindKeyValue's default branch returns the raw string, the same value that TypeText returns.
- aa284ba67a46 drivers/file/cassandra.go:99 statement/remove — new in v2.10.16. The same `ct = gocql.TypeText` line is removed, equivalent for the same reason.
- 634ae94e5f73 drivers/file/cassandra.go:39 statement/remove — replaces e9d2453f6f93 (cassandra.go:39 statement/remove). The old reason holds: the reader copies the header and reads each row into a new map before the next Read, so ReuseRecord has no effect that a caller can see.
- 8ec5648f8f93 drivers/file/detect.go:206 branch/if — replaces d89bdc4dfe24 (detect.go:206 branch/if). The old reason holds: obj is nil there, so the function falls through to the same FormatMongoexport return.
- 954e3993f127 drivers/file/detect.go:99 statement/return — replaces 368581278664 (detect.go:99 statement/return). FormatUnknown is iota 0, so `return 0` is the same value.
- 5230a0adc714 drivers/file/detect.go:145 statement/return — replaces e9779a9c0682 (detect.go:145 statement/return). The same zero-value identity.
- 786fa4d6df06 drivers/file/detect.go:151 statement/return — replaces eeb84b031cd5 (detect.go:151 statement/return). The same zero-value identity.
- 8af963f6e1c4 drivers/file/detect.go:170 statement/return — replaces 853420eac2c8 (detect.go:170 statement/return). The same zero-value identity.
- 31a6ad8b6605 drivers/file/detect.go:196 statement/return — replaces e959cd5dc29e (detect.go:196 statement/return). The same zero-value identity.
- 42238f235be3 drivers/file/file.go:159 statement/return — replaces 3010f9fcee4e (file.go:159 statement/return). The same zero-value identity.
- a962a06f156a drivers/file/file.go:173 statement/return — new in v2.10.16. The url.Parse failure in parseFileURL returns 0 in place of FormatUnknown. The same zero-value identity.
- 5390488733d7 drivers/file/file.go:176 statement/return — replaces 7c903f929870 (file.go:176 statement/return). The same zero-value identity.
- 0e7102f7ee31 drivers/file/file.go:188 statement/return — replaces 344b074185e8 (file.go:188 statement/return). The same zero-value identity.
- aa1f8c4c2431 drivers/file/file.go:195 statement/return — replaces bc9401a966b3 (file.go:195 statement/return). The same zero-value identity.
- 63eecb02c775 drivers/file/file.go:182 expression/remove — replaces fdb6613b3cb7 (file.go:182 expression/remove). The old reason holds: with an empty host the body sets `path = "" + path`, which does not change it.
- 0dd5ade7ed6f drivers/file/mongo.go:96 expression/error-guard — replaces 9547ee9cc2ce (mongo.go:93 expression/error-guard). The same Token guard as a4b4e3c598fa, probed the same way.
- 3647f8ebf312 drivers/file/mongo.go:139 expression/error-guard — new in v2.10.16. The Discard(1) guard in startsArray, the same class as a39ce3ac5c75.
- 8d8c1c16ad5a drivers/file/neo4j.go:56 expression/error-guard — replaces 3b80a9a0b928 (neo4j.go:56 expression/error-guard). The same Token guard as a4b4e3c598fa, probed the same way.
- eb55263f9e7b drivers/file/neo4j.go:110 statement/return — replaces cef35d51bf2f (neo4j.go:110 statement/return). nodeKind is iota 0, so `return 0` is the same value.
- a174143d50fd internal/parquetout/writer.go:366 numbers/decrementer — widenSigned returns 0 or -1 with ok=false, and both callers read the number only when ok is true.
- cc6327719e12 internal/pushdown/pushdown.go:638 branch/case — the mutant changes only the indentation of `return false`, so the compiled code is the same.
- 21f581b8a3e8 internal/rawpred/rawpred.go:305 branch/case — scalarType returns the type with ok=false, and eqFound reads the type only when ok is true.
- 28263c0cb664 internal/rawpred/rawpred.go:305 statement/return — same as 21f581b8a3e8, `return 0, false` drops a type that no caller reads.
- d6efec9719c17eca14fb9f5bf0b7b36b drivers/file/cache.go:525 branch/if — drops the return after a failed readTrailer, which leaves the offset at 0; readIndex then decodes from the magic bytes, a CBOR byte string that cannot fill the index array, so it fails and the next guard returns the same `cacheIndex{}, false`.
- 90bcbb793447fd2b4811835b81bb6465 internal/query/dump.go:223 expression/error-guard — dec.Token reads the `[` that startsJSONArray peeked just before, so it cannot fail at that point and the guard never fires.

## drivers/mongo, mutago v2.10.16 re-baseline (accepted 2026-10-01, test/mutation-rebaseline-5 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The CI scan of the package on f566ec9 found 21 escapes. Six of them kept their old ids: the context-nil entries d70002bbf70b, c5ea53e8585d and f492a2761e42, and the ParseFloat bit size entries b97da05ec7d2 and b537ea92d1b6. The id c5ea53e8585d also covers the byte-identical `cur.Close(ctx)` defer at mongo.go:183, which has the same reason. These old reasons hold on the current code, and each was applied by hand again. The other 15 had a new id, and 13 of them were a test gap. TestCommandMonitorSkipsHandshakeCommands sends each of the nine skipped commands and a find to the monitor, so the removal of each entry of tracedSkip fails. TestOpenRejectsBadURI gives Open a URI with no database, a URI that does not parse and a bad client option, and requires the cause in the chain. TestDatabaseFromURI has a row for a URI that does not parse. TestOpenWithCanceledContextFails requires context.Canceled in the chain of the ping error. TestPutRejectsNonObjectValue now runs both write modes, so the insert-only path also rejects a scalar. The new tests turned two more mutants from uncovered into covered, the %w wraps at mongo.go:97 and mongo.go:118, and TestOpenRejectsBadURI kills them too. Each new test was proved by hand: the mutation applied to a copy of the file makes the package suite fail.
Each entry below was applied by hand to a copy of the file, and the whole package suite passes against the compose MongoDB.
- e009479610d0 drivers/mongo/mongo.go:146 expression/error-guard — replaces 925ed1e9b0f7 (mongo.go:146 expression/error-guard). The guard on cur.Decode in Get becomes `if false`. The old reason holds: Decode into a bson.M fails only for malformed wire data, which a real server does not send. Probed with a document that a raw BSON insert gave an invalid UTF-8 string: the server stores it, and Decode returns it with no error.
- 26963a4db923 drivers/mongo/mongo.go:188 expression/error-guard — new in v2.10.16. The same Decode guard in scanWith, equivalent for the same reason.

## drivers/redis, mutago v2.10.16 re-baseline (accepted 2026-10-01, test/mutation-rebaseline-5 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The CI scan of the package on f566ec9 found 65 escapes. Twenty-six of them kept an old id: the per-command context entries of kv.go, write.go and filter.go, and 760ad7f757c7 (write.go:277). The `queueWrite(ctx, p, r)` call at write.go:88 is byte-identical to the one at write.go:43, so it shares the id 8fca4cd2ecaa. These old reasons hold on the current code, and each was applied by hand again. The other 39 had a new id, and 32 of them were a test gap. The readers had no test of a failed reply, so TestReaderReportsReadError gives each reader a reply with an error, and TestReaderReportsMalformedJSON gives both RedisJSON readers a reply that is not JSON. Get had no test of a failed TYPE pipeline, a key that vanished between the pipelines, a failed value pipeline or a failed read of one key. TestGetFailsFastOnTypeError, TestGetToleratesAVanishedKey, TestGetSurfacesAFailedValuePipeline and TestGetNamesTheKeyThatFailedToRead stage these with a canceled context or a pipeline hook. TypedScan had none of the walk tests that ScanBatches has. New tests walk more than one SCAN round, scan an empty keyspace, stop at the first page error, report a failed SCAN and report a failed value pipeline, and TestTypedGetFailsFastOnTypeError covers typedGet. TestQueueWriteRejectsANonScalarPart puts an object where each type needs a scalar or a number, and TestPutInsertOnlyRejectsABadRecord and TestPutInsertOnlySurfacesAFailedWrite cover the insert-only path. EstimateCount and Delete get a test of a failed command. TestFormatReplyQuotingEscapes now quotes 0x1f, the highest control character. Each new test was proved by hand: the mutation applied to a copy of the file makes the package suite fail.
The update run surfaced seven more escapes that the new tests had turned from uncovered into covered: the %w wraps of the scalar and score errors in queueWrite, queueHash, queueElements, queueZSet and queueStream (write.go:110, 168, 192, 218, 222, 244 and 254). TestQueueWriteRejectsANonScalarPart now also requires the cause in the chain, and kills all seven. Their six ids (write.go:110 and write.go:192 share one) were removed from the baseline by hand before the closing run.
Each entry below was applied by hand to a copy of the file, and the whole package suite passes against the compose Redis.
- c8b89e151b0a drivers/redis/kv.go:27 numbers/decrementer — replaces 4b7421b81e3b (kv.go:27 numbers/decrementer). The old reason holds: an empty read reaches two pipelines that queue nothing, and `(*Pipeline).Exec` returns before any round trip, so the same empty map comes back.
- 50dc30729c92 drivers/redis/kv.go:28 branch/if — replaces da214413f273 (kv.go:28 branch/if). The same short-circuit with its body cleared, equivalent for the same reason.
- 203b5cc8dbdf drivers/redis/kv.go:307 numbers/decrementer — replaces b8332d504b6a (kv.go:307 numbers/decrementer). The old reason holds: an empty page reaches build, whose read of zero keys is the same pipeline with no commands, and the `len(batch) == 0` check below returns the same nil.
- a8bd49ef9ac8 drivers/redis/kv.go:308 branch/if — replaces d5c43949128e (kv.go:308 branch/if). The same flush guard with its body cleared, equivalent for the same reason.
- 4f2a7dcc7f06 drivers/redis/write.go:23 numbers/decrementer — replaces bcda4e484c65 (write.go:23 numbers/decrementer). The old reason holds: an empty batch reaches a write path whose pipelines queue nothing, so the zero WriteStat and the nil error are the same.
- f28d80fb93b2 drivers/redis/format.go:68 branch/if — new in v2.10.16. The `return s` body in indent becomes `_ = s`. The branch runs only when s has no newline, and then strings.ReplaceAll on "\n" returns s unchanged.
- 5a5725afc8ee drivers/redis/write.go:281 branch/case — new in v2.10.16. The nil arm of redisString gets its own `return "", nil` back with a different indentation, so the program does not change.

## drivers/couchdb, mutago v2.10.16 re-baseline (accepted 2026-10-02, test/mutation-rebaseline-5 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. Two old entries stay: 7beb181f5718 (couchdb.go:113) and d02d70f46a0e (write.go:69). Their old reasons hold on the current code. The new tests turned five more `%w` wraps from uncovered into covered, at couchdb.go:69, 176, 183, 248 and 257. TestOpenRejectsAHostTheClientCannotParse, TestScanBatchesSurfacesARowFailure and TestQuerySurfacesARowFailure now require a cause behind the wrap with `errors.Unwrap`, so they kill all five. The check names no platform error value, so it holds on Linux, macOS and Windows. Each kill was proved by hand: the `%w` changed to `%v` in a copy of the file makes the suite fail. The ids of the five wraps were removed from the baseline by hand before the closing run.
Each entry below was applied by hand to a copy of the file, and the whole package suite passes against the compose CouchDB.
- db9a7a275cd0 drivers/couchdb/couchdb.go:149 expression/error-guard — new in v2.10.16. The guard on rows.ID in Get becomes `if false`. ScanDoc has already succeeded on the same row, and rows.ID reads the row that ScanDoc read, so it cannot fail.
- f07d39503f42 drivers/couchdb/couchdb.go:113 expression/remove — a second id for the line of 7beb181f5718. The old reason holds: the dropped `p != ""` conjunct can only admit `p == ""`, and then `db = p` assigns "" to a db that is already "".
- 68c285503c1b drivers/couchdb/filter.go:133 branch/case — new in v2.10.16. The `default` arm of the selector switch gets its own `return nil, false` back with a different indentation, so the program does not change.
- 522cc819bc3a drivers/couchdb/filter.go:272 branch/case — new in v2.10.16. The `case 0` arm gets its own `return nil, false` back with a different indentation, so the program does not change.

## drivers/hbase write and column parse equivalents (accepted 2026-10-02, chore/codescene-hbase branch)

- c8cdfa3f41d73640362c25eabc73b652 drivers/hbase/hbase.go:533 branch/if. The `return` in parseColumn's `!ok` branch is removed. Then `strings.Cut` has returned the whole column as family and "" as qualifier, so the next check, `qualifier == ""`, returns the same error with the same text. Applied by hand: the hbase package suite passes.
- c1f39b268d560d67a93ac86870baccac drivers/hbase/write.go:86 and :108 expression/error-guard. The guard after `hrpc.NewPut` in insertRow and in upsertRow becomes `if false`. One id covers both sites, because the two guards have the same text. `hrpc.NewPut` fails only when an option function fails, and the call passes no option. No input reaches the branch. Same class as the filter.go:30 entry above.
- c223f1d777dd1154a92d31aa9dbddfcb drivers/hbase/write.go:303 expression/error-guard. The guard after `hrpc.NewDel` in deleteRow becomes `if false`. `hrpc.NewDel` fails only for a failed option or for DeleteOneVersion on a whole-row delete, and the call passes no option. No input reaches the branch.
- 5829bf7b397e273e2aefb4d79b516f53 drivers/hbase/write.go:96 and :117 statement/return. The final `return written, nil` of insertRow and of upsertRow becomes `return 0, nil`. `written` is the first iota of putOutcome, so 0 is `written`. The program does not change. Applied by hand: the hbase package suite passes.
- 933de0cfb9d791e796db848c7da811be drivers/hbase/write.go:104 statement/return. The `return written, err` after the failed pre-read in upsertRow becomes `return 0, err`. The value is the same, as `written` is 0. Put also drops the outcome when the error is not nil.
- c5432a3c1c564b9ec485207ff3207f92 drivers/hbase/write.go:112 statement/return. The `return written, ...` after the failed Put in upsertRow becomes `return 0, ...`. The value is the same, as `written` is 0.
## internal/rawpred surrogate escape guard (accepted 2026-10-02, fix/rawpred-invalid-utf8 branch)

- 9562bb13dc8e internal/rawpred/rawpred.go:167 numbers/decrementer. The advance `b = b[i+2:]` becomes `b = b[i+1:]`. The next search then skips the `u` and finds the same following backslash, so the result does not change. Applied by hand: the rawpred suite passes.
- 7ec1da62f278 internal/rawpred/rawpred.go:146 statement/return. In `Matcher.Match`, `return MayMatch` becomes `return 0`. MayMatch is the first `iota` value, so it is 0 and the program does not change. Applied by hand: the rawpred suite passes.

## drivers/hbase value beside an error (accepted 2026-10-02, chore/codescene-hbase branch)

- 5076340803d9daa6824c1241c59f8ad6 drivers/hbase/hbase.go:444 numbers/decrementer. In `scanLimit`, `return 0, fmt.Errorf(...)` for a limit that is not a number becomes `return -1, ...`. The only caller, `execScan`, returns at once on a non-nil error and never reads the limit. Applied by hand: the hbase suite passes.
- a3206d00a97184a0d84846f3d40175b1 drivers/hbase/hbase.go:444 numbers/incrementer. The same line becomes `return 1, ...`. The same caller ignores the value on error. Applied by hand: the hbase suite passes.
- ae4f777cff504ef6fb3ad390db46a154 drivers/hbase/write.go:91 statement/return. In `insertRow`, `return written, fmt.Errorf(...)` after a failed CheckAndPut becomes `return 0, ...`. The only caller, the write loop in `Put`, returns at once on a non-nil error and never reads the outcome. `written` is 0 as the first `iota` value, so the value is the same. Applied by hand: the hbase suite passes.

## drivers/dynamodb, mutago v2.10.16 re-baseline (accepted 2026-10-02, test/mutation-rebaseline-6 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The seed scan of the package found 42 new escapes. Most of them were a test gap. The new tests in `guards_test.go` kill them, and each was proved by hand: the mutation applied to a copy of the file makes the package suite fail under `go test -short`. The `backoff` jitter draw now goes through the variable `randInt63n`, so a test can read the bound that the draw gets. Ten ids are equivalent and are accepted here.
- c39f055cdfef drivers/dynamodb/dynamodb.go:244 numbers/decrementer — replaces e37357b89d71 (dynamodb.go:244 numbers/decrementer). The old reason holds: with `== -1` an empty key list reaches the chunk loop, which does not run, so Get returns the same allocated empty map.
- 698cc489431c drivers/dynamodb/dynamodb.go:245 branch/if — replaces ef1b2e4aead9 (dynamodb.go:245 branch/if). The same short-circuit with its body cleared, equivalent for the same reason.
- 0be71c824c9a drivers/dynamodb/filter.go:82 branch/case — new in v2.10.16. The mutant only changes the indentation of `return frag{}, false` in the `default` case of build. The program is the same.
- f2aa482d00e1 drivers/dynamodb/filter.go:198 branch/case — new in v2.10.16. The same indentation-only change in the `default` case of scalarAV.
- dc1673858b27 drivers/dynamodb/filter.go:138 numbers/decrementer — replaces 500722b4e1c5 (filter.go:138 numbers/decrementer). The old reason holds: an empty Or reaches join, whose loop does not run and whose `len(exprs) == 0` check returns the same `frag{}, false`.
- 8605c87ec5fc drivers/dynamodb/filter.go:139 branch/if — new in v2.10.16. The `return frag{}, false` of the same guard is cleared. An empty Or then takes the same path through join, so the result is the same.
- dbf71b5517dd drivers/dynamodb/normalize.go:29 branch/case — new in v2.10.16. The same id also covers line 31. Both mutants change only the indentation of `return nil` in a case of Normalize. The program is the same.
- 866d3431d8c8 drivers/dynamodb/trace.go:47 branch/if — replaces 007970fbff29 (trace.go:47 branch/if). The old reason holds: Elem() of a nil pointer is the invalid zero Value, whose Kind is not Struct, so the next check returns the same empty string.
- ce4131049a00 drivers/dynamodb/trace.go:55 expression/remove — replaces ee4f978b2ea5 (trace.go:55 expression/remove). The old reason holds: an invalid Value has the Kind Invalid, never Pointer, so the dropped `f.IsValid()` conjunct is inert.
- 8a05b55045e9 drivers/dynamodb/write.go:239 expression/error-guard — new in v2.10.16. The decodeKey guard in Delete is unreachable. The call to existingKeySet at the top of Delete decodes every key with the same function and returns the first failure, so the second decode of the same keys cannot fail. TestDeleteReadsNothingForAMalformedKey kills the guard in existingKeySet (write.go:107).

## drivers/hbase, mutago v2.10.16 re-baseline (accepted 2026-10-02, test/mutation-rebaseline-6 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The seed scan of the package found 34 new escapes. Eight of them were a test gap. They are killed by the tests in `guards_test.go`, and each was proved by hand: the mutation applied to a copy of the file makes the package suite fail. One of the eight showed a real bug. `Open` called `ClusterStatus`, which takes no context and retries a dead quorum without end, so `Open` ignored the caller's deadline. `Open` now runs the call in `probe`, which returns at the deadline, and TestOpenStopsAtTheCallersDeadline kills the old guard on that call (hbase.go:117). The same id as hbase.go:92 also covers the twin guards in `execPut`, and the same id as hbase.go:246 also covers the twin skip in `execScan`. TestExecPutRejectsAValueThatDoesNotEncode and the "exec scan" case of TestScansSkipAnEmptyResult cover them. The 23 ids below are equivalent or unreachable, and each was checked by hand: the package suite passes with the mutation applied.
- e2dcbf5b273c drivers/hbase/filter.go:52 branch/case — new in v2.10.16. The mutant only changes the indentation of `return nil, false` in the `default` case. The program is the same.
- 9e33d5ad5a2c drivers/hbase/filter.go:30 expression/error-guard — replaces 4c2b9ffba208 (filter.go:30 expression/error-guard). The old reason holds: gohbase builds a scan with `baseScan`, which fails only when an option function fails, and the options here (MaxVersions and the filters of toFilter) never fail.
- 92b7be31c500 drivers/hbase/hbase.go:105 branch/if — replaces 4a9c13a319df (hbase.go:105 branch/if). The old reason holds: the timeouts from the deadline go to unexported gohbase fields, and `probe` now ends the wait at the deadline whatever those fields hold.
- 96f694f86d74 drivers/hbase/hbase.go:106 statement/remove — replaces a9a81c63acc8 (hbase.go:106 statement/remove). The same reason.
- 176e29d6dfc8 drivers/hbase/hbase.go:162 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewListTableNames(ctx, hrpc.ListNamespace(namespace))`. The only option is ListNamespace, which stores the string and cannot fail, the same class as filter.go:30.
- b66e8322a9e1 drivers/hbase/hbase.go:199 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewGet(..., hrpc.MaxVersions(1))` in Get. MaxVersions fails only above math.MaxInt32, so a constant 1 cannot fail.
- 513d9019c6a9 drivers/hbase/hbase.go:224 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewScanStr(..., hrpc.MaxVersions(1))` in pageScan, the same reason.
- 29c134f7c741 drivers/hbase/hbase.go:303 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewGet` in execGet, the same reason.
- b36dbf578de7 drivers/hbase/hbase.go:331 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewScanStr` in execScan, the same reason.
- 14a3d957e56d drivers/hbase/hbase.go:364 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewScanStr` with a KeyOnlyFilter in execCount. ConstructPBFilter of that filter cannot fail.
- 7a1fcf072122 drivers/hbase/hbase.go:406 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewPut` in execPut with no options. `baseMutate` stores its arguments and has no option to fail.
- 005fad56f1ae drivers/hbase/hbase.go:435 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewDel` in execDelete. NewDel fails only for the DeleteOneVersion option, which is not used.
- 2ae6ad4db5cc drivers/hbase/normalize.go:71 statement/return — replaces 9fddefd1d54b (normalize.go:71 statement/return). The old reason holds: ctAuto is iota 0, so `return 0` is the same value.
- a6395147e7eb drivers/hbase/normalize.go:261 branch/if — new in v2.10.16. The `return s` of rowKeyString is cleared for a string. The code then returns `fmt.Sprintf("%v", v)` of the same string, which is the same string.
- 95c381b70969 drivers/hbase/parse.go:117 branch/if — new in v2.10.16. The early `return out, nil` of parseTypeMap for an empty parameter is cleared. The loop then sees one empty entry, skips it and returns the same empty map. TestParseTypeMapReturnsAnAllocatedMapForNoEntries kills the statement/return mutant at the same line (a58a386076ad).
- e7a0b91cec6d drivers/hbase/parse.go:129 expression/remove — new in v2.10.16. The `!ok` term of `!ok || family == "" || qualifier == ""` is removed. When the colon is missing, strings.Cut gives an empty qualifier, so the next term is true.
- 359112adf9a9 drivers/hbase/write.go:93 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewGet` in rowExists, which has no option.
- 19b61554aeb1 drivers/hbase/write.go:175 expression/error-guard — new in v2.10.16. The guard after `hrpc.NewScanStr` with a KeyOnlyFilter in Clear, the same reason as hbase.go:364.
- 42d4957c9fd8 drivers/hbase/write.go:240 expression/error-guard. The same guard as a8d7bd9f01b5 after #61 moved Clear's row delete into the eachRow callback, so the id changed. The reason is the same: hrpc.NewDel with no option cannot fail. CI applied the mutant (PR #64), and the whole hbase package suite passed against a real HBase, with 19 rows through the callback.

## drivers/neo4j, mutago v2.10.16 re-baseline (accepted 2026-10-02, test/mutation-rebaseline-6 branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The seed scan of the package found 48 new escapes. Twenty of the ids were a test gap. They are killed by the tests in `guards_test.go`, and each was proved by hand against a live Neo4j: the mutation applied to a copy of the file makes the package suite fail. A database name that does not exist makes each statement fail when it starts, which kills the guards after `s.run` and the inspect wrappers. A list as the key property makes the server fail while rows stream, which kills the guards after `res.Err`. The same id as neo4j.go:177 also covers the twin guards at neo4j.go:301 (Get, killed by the same missing-database test) and write.go:196 (the second `s.run` of Delete). The second one cannot fail alone, because the first `s.run` of Delete fails first on a missing database. The 18 ids below are equivalent or unreachable. Each was checked by hand: the package suite passes with the mutation applied. The four context-nil entries are true for neo4j driver v5.28.4 only: `NewSession` gives the context to `computeCacheKey`, which reads it only when `SessionConfig.Auth` is set, and this driver never sets it. Re-check them when the SDK moves.
- d41537a3e6f0 drivers/neo4j/filter.go:26 branch/if — replaces 298e2e7f0fd1 (filter.go:26 branch/if). The old reason holds: the early return is cleared and the code falls to `pagedScan` with an empty where clause, which is what ScanBatches runs. It selects every node, so it is a superset of any filter.
- 2c99ed30892c drivers/neo4j/filter.go:87 branch/case — new in v2.10.16. The mutant only changes the indentation of `return "", false` in the `default` case of translate. The program is the same.
- 61612fd1d1bc drivers/neo4j/normalize.go:77 branch/case — new in v2.10.16. The same indentation-only change in a case of normalizeValue.
- f2a1426ab6fa drivers/neo4j/neo4j.go:185 expression/error-guard — new in v2.10.16. The guard after `neo4j.NewDriverWithContext`. parseURL has already parsed the same URL and has checked the scheme against the six that the driver accepts, so the driver cannot refuse the DSN. Probed with a port out of range, an empty host and an empty port: the driver accepts all of them.
- 3c940ca2e300 drivers/neo4j/neo4j.go:418 expression/error-guard — new in v2.10.16. The guard after `res.Single` in EstimateCount. The statement `RETURN count(n)` always gives one record, and a server failure shows at `s.run` before this point.
- cd76a50a5bec drivers/neo4j/neo4j.go:486 expression/error-guard — new in v2.10.16. The `res.Err()` guard of keyConstraintExists. SHOW CONSTRAINTS cannot fail while its rows stream, and a failure at the start shows at `s.run`, which TestIntegrationAMissingDatabaseFailsEveryOperation kills.
- 7a5af99bc6af drivers/neo4j/neo4j.go:288 expression/context-nil — new in v2.10.16. `s.session(nil, ...)` in Get. The reason is the driver note in the section intro.
- 3792d440b998 drivers/neo4j/neo4j.go:342 expression/context-nil — new in v2.10.16. The deferred `sess.Close(nil)` in pagedScan. The result is discarded and the context reaches only pool and router cleanup, as in the old entry 478cf8fc2fb5.
- e4360d945dae drivers/neo4j/write.go:59 expression/context-nil — new in v2.10.16. `s.session(nil, ...)` in Put, the same reason as neo4j.go:288.
- db6ebc3fa992 drivers/neo4j/write.go:60 expression/context-nil — new in v2.10.16. The deferred `sess.Close(nil)` in Put, the same reason as neo4j.go:342.
- 6190739a858a drivers/neo4j/neo4j.go:518 expression/remove — replaces f1996a65b72e (neo4j.go:518 expression/remove). The old reason holds: a failed type assertion leaves `items` nil, so `len(items) != 1` is already true.
- 43ade25089e7 drivers/neo4j/write.go:104 expression/remove — replaces acdc7481f2cf (write.go:104 expression/remove). The old reason holds: a failed type assertion leaves `keyVal` nil, so the second term already covers it.
- 212eefa8dcfa drivers/neo4j/write.go:34 statement/return — replaces 7bf22d86ca3e (write.go:34 statement/return). The old reason holds: nothing has written to `stat` yet, so `return query.WriteStat{}` gives the same zero value.
- 89d25a612abe drivers/neo4j/write.go:43 statement/return — replaces 556958714b0d (write.go:43 statement/return). The same zero value on the next early path.
- 0ba92daf02c8 drivers/neo4j/write.go:47 statement/return — replaces cb38925ce604 (write.go:47 statement/return). The same zero value on the empty-batch path.
- ce994a0aab32 drivers/neo4j/write.go:72 expression/error-guard — new in v2.10.16. The `Consume` guard in Put. Probed with a nested map, a nested list, a list with null, a mixed list and a list of maps as property values: the server rejects each of them when the statement starts, so the failure shows at `s.run` and never at Consume. Killing this guard needs a failure at commit time, which a test cannot cause.
- be4477090eaa drivers/neo4j/write.go:132 expression/error-guard — new in v2.10.16. The `Consume` guard in Clear. `MATCH ... DETACH DELETE` with a bound limit raises no error at Consume, for the same reason as write.go:72.
- baf69e35e4a9 drivers/neo4j/write.go:199 expression/error-guard — replaces 6ff99294550a (write.go:199 expression/error-guard). The old reason holds: not equivalent, but unkillable without fault injection, because the statement binds only `$eids` and `elementId()`.
- 7f0f2b267ee0 drivers/neo4j/write.go:54 statement/return — new in v2.10.16. The recordProps error return in Put becomes `return query.WriteStat{}, err`. Nothing has written to `stat` before this loop, so it is still the zero value and the two returns are the same.
- fdc773b99d52 drivers/neo4j/write.go:69 statement/return — new in v2.10.16. The `s.run` error return in Put, the same zero-value reason.
- 3036ab815721 drivers/neo4j/write.go:196 expression/error-guard — new in v2.10.16. The guard after the second `s.run` of Delete (the DETACH DELETE). It shares its id with the guard at write.go:172, which TestIntegrationAMissingDatabaseFailsEveryOperation kills. This second guard is unreachable in a test: a missing database already fails the first `s.run`, and the server has no other way to refuse a statement that the first one just ran on the same session. The package suite passes with it set to `if false`.

## drivers/elasticsearch, mutago v2.10.16 re-baseline (accepted 2026-10-02, test/mutation-rebaseline-7 branch)

The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. The CI scan of the package on 21e3ee6 found 27 new ids (29 lines). Twelve were a test gap, and `errorpath_test.go` kills them. The tests check the default page size of 100 on the wire and in Open, the status range up to 299 on both reply paths, a request that cannot be built, an unparsable URL, a query the encoder cannot write, a refused point-in-time, an undecodable `_source` in Get, a transport failure in Query, a scalar value in Put, and a failed item in a bulk delete. Each kill was proved by hand: the mutant applied to a copy of the file makes the package suite fail.
Each entry below was applied by hand to a copy of the file, and the whole package suite passes against the compose Elasticsearch and OpenSearch.
- f04c23496233 drivers/elasticsearch/elasticsearch.go:78 expression/error-guard. The guard on the newClient error in Open. Both client constructors fail only on an address that does not parse or on a transport option that Open never sets. parseURL builds the address from a scheme and a host that url.Parse already accepted, so the constructor cannot fail. The byte-identical guard on the parseURL error at line 74 is killed by TestOpenReportsAnUnusableURL.
- 3a1bb75ef107 drivers/elasticsearch/elasticsearch.go:301 branch/if. Replaces cfe6c6f5e179. The old reason holds: with no hits the loop copies nothing, fn is not called, and the `len(hits) < s.pageSize` test returns on the same pass. The byte-identical `return nil` of that last-page test at line 330 is equivalent too. The scan then asks for one more page, which is empty and ends the scan.
- ed3662c4589d elasticsearch.go:300 numbers/decrementer. Replaces 24b90a952906. The old reason holds: `len(hits) == -1` is never true, and an empty page leaves the scan through the last-page test with the same result.
- fda29052c4f1 elasticsearch.go:133 expression/remove. Replaces 808ba5da7eb5. The old reason holds: the dropped `p != ""` operand only decides the empty case, and there `index = p` assigns the empty string that index already holds.
- 7b6f31fe97c1 drivers/elasticsearch/filter.go:273 expression/remove. Replaces 2ff0f65b4bc4. The old reason holds: matchesLiteral returns false for a nil value in every class, so a null literal never pushes.
- 1a8d2283dbb5 drivers/elasticsearch/inspect.go:61 expression/error-guard. Replaces edc0317f0eaf. The old reason holds: the input is a json.RawMessage that a successful decode produced, so it is valid JSON and the unmarshal cannot fail.
- a7c7a76d4c1e drivers/elasticsearch/write.go:42 expression/error-guard. Replaces 2cf1585dd1d3. The old reason holds: the action line is a map of strings, and json.Marshal cannot fail on it.
- b18b4785fd2c drivers/elasticsearch/write.go:166 expression/error-guard. Replaces 55272235d47f. The same reason, for the delete action line.
- 95eb99737aa4 drivers/elasticsearch/elasticsearch.go:374 expression/error-guard. The marshal of the search body in Query. The body comes from json.Unmarshal of the argument, and every value that decode produces can be encoded again, so the marshal cannot fail.
- bdcf812d0f28 elasticsearch.go:195 expression/error-guard. The marshal of `{"ids": keys}` in Get. A slice of strings always encodes, so the marshal cannot fail.
- 5ed05b8d4680 drivers/elasticsearch/write.go:136 expression/error-guard. The marshal of the constant match-all query in Clear. A constant map of maps always encodes, so the marshal cannot fail.
- 19af9d472c08 drivers/elasticsearch/filter.go:173 branch/case. The `default` arm of exactPush gets its own `return false` back with a different indentation, so the program does not change.
- a8fd0ac6d4ce drivers/elasticsearch/filter.go:264 branch/case. The `default` arm of toQuery gets its own `return nil, false` back with a different indentation, so the program does not change.
- 053b362dccb4 drivers/elasticsearch/filter.go:320 branch/case. The same for the `default` arm of matchesLiteral.
- 24a897f31448 drivers/elasticsearch/plan.go:85 branch/case. The same for the `default` arm of explainQuery.

## drivers/cassandra, mutago v2.10.16 re-baseline (accepted 2026-10-03, test/mutation-rebaseline-7 branch)

Each id below is accepted as equivalent. The CI deep scan run 37104978614 ran the whole suite against a real cluster with the mutant applied, and it passed.

- 7e5f822f4c0e drivers/cassandra/cassandra.go:194 statement/return. withPort returns "" for the host "", so a bare `return ""` gives the same value.
- e3fbe0439463 cassandra.go:218 branch/case. gocql.Any is 0, so the mutated `return 0, nil` gives the same value as the Any arm.
- d06006f320db cassandra.go:218 statement/return. The same reason as e3fbe0439463.
- 6e13daad3a93 drivers/cassandra/dump.go:90 statement/return. gocql.TypeCustom is 0, so `return 0, err` gives the same value.
- 3aee2298976b drivers/cassandra/filter.go:25 expression/logical. `ok` is true only when the where clause is not empty, so `ok || where != ""` equals `ok`.
- b9d63085d8c0 filter.go:25 expression/remove. For the same reason, `ok && true` equals `ok`.
- 709c562b7db9 filter.go:58 branch/case. The `default` arm gets its own return back with a different indentation, so the program does not change.
- 942beb070798 drivers/cassandra/normalize.go:33 branch/case. The same for the nil arm.
- b08bd25f067a normalize.go:114 numbers/incrementer. strconv.ParseFloat treats every bit size other than 32 as 64.
- 9a092cf8f039 normalize.go:114 numbers/decrementer. The same reason.
- 89501b5f5cda normalize.go:275 numbers/incrementer. The same reason.
- adc6b59ebec0 normalize.go:275 numbers/decrementer. The same reason.
- 213f869f6c6a normalize.go:524 numbers/incrementer. strconv.FormatFloat treats every negative precision as the shortest form.
- ba7aa88733c7 drivers/cassandra/plan.go:26 expression/remove. The same reason as filter.go:25, in ExplainPlan.
- f337b662f270 plan.go:26 expression/logical. The same reason as filter.go:25, in ExplainPlan.
- 36ed252c4e81 drivers/cassandra/write.go:206 expression/error-guard. The pre-read in Delete already decoded every key, so the second decode cannot fail.

## drivers/couchbase, mutago v2.10.16 re-baseline (accepted 2026-10-04, test/mutation-rebaseline-8 branch)

Each id below is accepted as equivalent. The CI deep scan run 37183581854 ran the whole suite against a real cluster with the mutant applied, and it passed.

- 262ecd91cde5 drivers/couchbase/couchbase.go:324 expression/error-guard. rawTranscoder Decode fails only for a target that is not *[]byte, and the target in Get is always *[]byte, so the guard cannot fire. Reused from the v2.7.7 id d99383f2.
- c6b3294bc91b couchbase.go:423 expression/error-guard. the scan statement cannot fail mid-stream on a live cluster, so the readPage error guard cannot fire. TestReadPage covers readPage with a fake.
- 2e5c697a03c1 couchbase.go:468 expression/error-guard. gocb Row into *json.RawMessage copies the bytes and cannot fail.
- be449d0d9f11 filter.go:83 branch/case. mutago replaces the default case body with the same zero-value return, so the program does not change.
- 219f1bac9602 filter.go:180 branch/case. the same reason as be449d0d, in the where builder.
- cd56dc039c95 filter.go:243 branch/case. the same reason as be449d0d, for the empty case of and.
- b951dc94ce1e inspect.go:19 statement/defer-remove. QueryResult.Next does not close the stream, so a missing deferred Close only leaks a response body. No result differs.
- b754003e7d68 inspect.go:23 expression/error-guard. gocb Row into *json.RawMessage cannot fail, the same reason as 2e5c697a.
- bf328d470039 inspect.go:28 expression/error-guard. nothing makes a system:nodes query fail while it streams, so the rows.Err guard cannot fire.
- 9e5c7b01507b inspect.go:45 statement/remove. sort.Strings on the bucket names. The test cluster holds one bucket, so the order cannot differ. Reused from the v2.7.7 id 8c63029e.
- 60824250c307 inspect.go:96 expression/error-guard. gocb Row into *json.RawMessage cannot fail, the same reason as 2e5c697a.
- 09431de6600a inspect.go:101 expression/error-guard. nothing makes a system:indexes query fail while it streams, so the rows.Err guard cannot fire.
- 9b4997ade86c write.go:148 statement/return. the empty-keys return in existingKeys gives nil instead of an empty map, and the only caller does a lookup. Reused from the v2.7.7 id aee2807a.
- f08fa040ecb1 write.go:187 expression/error-guard. a DELETE cannot be made to fail while it streams, so the rows.Err guard in Clear cannot fire.
- 65ed89eaa467 write.go:191 expression/error-guard. rows.Close returns only the stream error that rows.Err already returned, so the Close guard in Clear cannot fire.

Some ids share their text with other lines of the same file, so mutago tests one copy per scan. The ids 262ecd91, c6b3294b, 2e5c697a, b951dc94, b754003e, bf328d47, 60824250, 09431de6, 9b4997ad and f08fa040 each belong to a group that mixes a covered copy with an unreachable one. A scan that picks the covered copy kills the mutant, which is harmless.

## cmd — equivalents found in the v2.10.22 re-baseline (accepted 2026-10-08, test/mutago-v2.10.22-rebaseline branch)
Each reason was checked against the current code. A scan id covers every site with the same mutated text.
- c739e1a3bc61cebd56152ac1f7c29114 cmd/driver.go:359 branch/if — the `if !ok` return after `urlQuery` in `foreignAddressParam`. `urlQuery` returns nil `Values` when the URL does not parse, and `Get` on nil `Values` returns an empty string, so the loop finds no parameter and the function still returns an empty string. The twin guard in `urlAddressName` (:337) has the same text and the same id, and the same reason.
- 5e4580b2556b2cde77edbca340764176 cmd/driver.go:436 loop/break — the `continue` on a read-only driver in `expectedSchemes`. `file` is the only read-only driver and the last entry of the registry, so `break` ends the loop at the same point. Both forms build the same list.
- 86c9dcd26e266041aa1b63cbf5441f14 cmd/config.go:106 composite/field-clear — clears `Args: cobra.NoArgs` on the `config` parent command. This command has no `Run`, so cobra prints help before it checks the arguments. Without `Args`, cobra uses the legacy check, which accepts arguments for a command that has a parent.
- 4bc6851dc56e20be60e6008757ad921e cmd/config.go:301 expression/error-guard — the `effectiveOption` error guard in `listAllOptions`. The function fails only when a key has no flag. Every key in `persistableOptions` has a flag, so the branch cannot be reached.
- ffb02d7a9fc069ddadc049221353165c cmd/config.go:333 expression/error-guard — the `Load` error guard in `config edit`. The same `Load` just succeeded, and `Load` of a missing file returns an empty config and no error, so the branch cannot be reached.
- f6139e1758ce3e87493327eb35cc2b87 cmd/config.go:366 expression/error-guard — the `toml.Marshal` error guard in `config view`. The configuration struct always encodes, so the branch cannot be reached.
- a23b9ab0725b2d119275baf1bd822219 cmd/config.go:384 expression/comparison — `len(cf.Sources) > 0` becomes `>= 0` in `redactedConfig`. An empty source map encodes to the same TOML as a nil map because the field has `omitempty`.
- fa45e4b11d139a226647e88c5279f3f2 cmd/config.go:384 numbers/decrementer — the same check as `>= 0`, now `> -1`. An empty source map encodes to the same TOML as a nil map.
- 20ad1b1927d0a7604d3535a14a7a50dc cmd/cache.go:54 composite/field-clear — clears `Args: cobra.NoArgs` on the `cache` parent command. The same reason as the `config` entry at config.go:106: this command has no `Run`, so cobra prints help first.
- ab5f9b2221751753c700454eb4450c08 cmd/cache.go:179 expression/remove — `schemeOf(src.URL) == "file"` becomes `true` in `dumpPathArg`. `DumpPath` rejects a URL that is not a file URL, so the code falls through to `return arg`, which is the result of the original check.
- 82ae2a754192a20c132eae4bcad90bc3 cmd/cache.go:179 expression/logical — `ok && schemeOf(...) == "file"` becomes `ok || ...`. A missing source has an empty URL and the check fails, and `DumpPath` rejects every URL that is not a file URL. The result is `arg` in each case.
- ad8b61b9dc1b7fc131ed2296a752cdf5 cmd/source.go:199 numbers/incrementer — `i >= 0` becomes `i >= 1` in `handleBase`. `seg` has its leading slashes removed, so `IndexByte` cannot return 0.
- f228fbc4fb0463a16817d6e408d255e4 cmd/source.go:199 expression/comparison — `i >= 0` becomes `i > 0` in `handleBase`. The same reason: `seg` has no leading slash, so `i` is never 0.
- acd928bfd5edbb9818239da5df60477d cmd/source.go:228 branch/if — the `DumpPath` error return in `fileStem`. `DumpPath` returns an empty path on an error, `Base` of an empty path is `.`, and the extension trim leaves an empty stem. The next check then returns `"", false`, the same as the original.
- c19661420165dc0782739497c27657d7 cmd/source.go:506 expression/error-guard — the `SetGroup("")` error guard in `iq group --clear`. `SetGroup` with an empty group always clears it and returns nil, so the branch cannot be reached.
- 0a8a1fbd2583d0a893748b4be0606a17 cmd/logging.go:60 statement/return — `return o, err` becomes `return logOptions{}, err` for a bad `IQ_LOG` value in `resolveLogOptions`. Both callers (root.go and mcp.go) return the error and never read the options.
- d1e92ada966cbf979725812183e2f435 cmd/logging.go:98 statement/return — the same change for a bad log level. Both callers drop the options on an error.
- ac108a768b2d26ddfcdd39d4de6a6a41 cmd/logging.go:110 statement/return — the same change for a bad log format. Both callers drop the options on an error.
- b2cd4f1dd85c08001a8943a34f3141f4 cmd/logging.go:245 statement/return — `return slog.LevelInfo, nil` becomes `return 0, nil` in `parseLogLevel`. `slog.LevelInfo` is 0, so the value is the same.
- 1cf291f033d8c5faaf449dc55cdbb7b7 cmd/logging.go:245 branch/case — the `INFO` case body is replaced by `return 0, nil`. `slog.LevelInfo` is 0, so the value is the same.
- 8aa9eef1943dd572e8cca60c94cf3fdf cmd/output.go:386 expression/errorf-wrap — `%w` becomes `%v` in the `close yaml` error of `yamlFormatter.flush`. `Close` can fail only when the writer fails, and `Encode` already reports that failure first, so no test can reach the branch. The color formatter has the same text (:408) and the same id.
- 33105492944e7011cc7ca4546e7c403b cmd/output.go:404 expression/error-guard — the `Encode` error guard in `colorYAMLFormatter.emit`. The encoder writes to a `bytes.Buffer`, which cannot fail, and a value that yaml.v3 cannot encode panics instead of returning an error. The panic is handled in `yamlFormatter.emit` only, so the branch cannot be reached.

## drivers/file/cache.go — cache index equivalents (accepted 2026-10-08, test/mutago-v2.10.22-rebaseline branch)
- ba702cbf1446c0fa97e719fd9d0ce662 drivers/file/cache.go:517 expression/error-guard — the `fileSize` guard (:517) and the `readTrailer` guard (:524) in `openIndex` share this id. With the first guard removed, `size` is 0, and `readTrailer` rejects it as too small. With the second guard removed, `readTrailer` returns offset 0 on an error. Offset 0 holds the cache magic, whose first byte is a CBOR byte string, so decoding it as the array-shaped index block always fails. In both cases `openIndex` returns false. Checked by hand: both mutants applied, `go test -short ./drivers/file/...` passes.
- e5f7886924951ea6a44f8bc8b539b5da drivers/file/cache.go:540 loop/break — the `break` on `l.done()` in `lookupPages` becomes `continue`. The lookup never becomes undone, so every later page also continues before the context check and the decode. The loop then ends with true and `l.out` is unchanged. Only the number of iterations differs. Checked by hand: `go test -short ./drivers/file/...` passes.

## cmd refactor (CodeScene round 3, PR #115)

- 40eefbb72fe8 cmd/keyring_cmd.go:425 expression/error-guard — the `UseKeyring` error guard in stageMigration is removed. Moved from 15045794c5f1 (old keyring_cmd.go:298) with the same reason: UseKeyring and SetSourceURL fail only when lookup finds no such source, so the next SetSourceURL fails with the same error and runs the same rollback.
- b9483a340b75 cmd/keyring_cmd.go:314 expression/error-guard — the `ClearKeyring` error guard in runKeyringRm is removed. Moved from f9fa458eebd8 (old keyring_cmd.go:235) with the same reason: the source comes from a Resolve that succeeded on the same loaded config, so ClearKeyring cannot fail.
- 77d9d2edee6f cmd/keyring_cmd.go:581 expression/remove — `err != nil || !has` becomes `!has` in pruneEntry. keyringHas returns has false on every error path, so `!has` is true whenever err is not nil, and `return false, err` runs either way.
- 2ead2c98e663 cmd/complete.go:314 branch/if — the `return ""` of the `!ok` branch in sourceDriverName is removed. An unresolved source is the zero value with an empty URL, and driverName("") is "" because no driver has the empty scheme, so the function returns "" either way.
- 905e3f41357f cmd/mcp_tools.go:519 expression/remove — the `err == nil` operand in queryRun.finish is removed. A nil error passes through asSyntaxError, scanHint and toolError, and each returns nil for nil, so the result is nil either way.
- 637e4f3664f3 cmd/mcp_tools.go:306 expression/error-guard — the error guard after explainResult in toolExplain is removed. explainResult fails only when gojq.Parse fails, and UsesSource and buildJQPlan parse the same filter earlier in the same call and return first, so the guard is unreachable.
- acd9d9538231 cmd/move.go:176 statement/return — `return query.Upsert` becomes `return 0` in writeModeFor. Upsert is the first iota value of WriteMode, so both return the same value.

## drivers/redis pipeline commands (CodeScene round 3, PR #119)

Each of these mutants replaces the ctx of a command that is queued on a go-redis Pipeliner, or of a helper that only passes the ctx on to such a command, with nil. Pipeline.Process only appends the command, and Exec runs every queued command with the Exec ctx, so the per-command ctx is never read (go-redis v9.21.0). The fake pins the Exec ctx of each pipeline.

- 208e2961166b drivers/redis/filter.go:56 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 6afe31978331 drivers/redis/filter.go:71 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- a1b832bb6302 drivers/redis/kv.go:63 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 14e9c483f1a9 drivers/redis/kv.go:133 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 25e72a85bb59 drivers/redis/kv.go:135 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 929d74254b5e drivers/redis/kv.go:144 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 7dd82b66f822 drivers/redis/kv.go:146 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- c54bd4fa807c drivers/redis/kv.go:148 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 119e4613a065 drivers/redis/kv.go:150 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- bc1c6edf483e drivers/redis/kv.go:152 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 3b23fdba61fb drivers/redis/kv.go:154 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- ee6a87705834 drivers/redis/write.go:39 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 90827cde3b57 drivers/redis/write.go:59 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 496f91634281 drivers/redis/write.go:60 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- ed995db838ac drivers/redis/write.go:147 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 9ae40ee74daa drivers/redis/write.go:159 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- c40b23842805 drivers/redis/write.go:171 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 8e29e857eb1c drivers/redis/write.go:181 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 2e5f88fe7422 drivers/redis/write.go:297 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.
- 0602aeb56673 drivers/redis/write.go:322 expression/context-nil — the ctx of a queued pipeline command, or of a helper that only queues commands, becomes nil.

- 0e7c67c66d9b drivers/couchdb/write.go:304 statement/remove — the `docs = make([]any, 0, len(keys))` capacity hint in deletions is removed. A nil slice and an empty slice behave the same: append allocates on demand, and slices.Chunk of nil yields no chunk, so BulkDocs is not called either way.
