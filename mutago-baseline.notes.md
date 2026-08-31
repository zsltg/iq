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
- b7a67e872a5e drivers/redis/filter.go:56 expression/context-nil — inner ctx discarded by BatchProcess; outer Pipelined(ctx) governs I/O.
- 505413a1ae7c drivers/redis/filter.go:59 expression/context-nil — same site as :56 re-accepted after refactor (content-hash id changed).
- 7598e64951e3 drivers/redis/filter.go:89 expression/context-nil — inner ctx discarded by BatchProcess; outer Pipelined(ctx) governs I/O.
- 578155c117f2 drivers/redis/filter.go:91 expression/context-nil — inner ctx discarded by BatchProcess; outer Pipelined(ctx) governs I/O.
- 3e53982b48bf drivers/redis/filter.go:92 expression/context-nil — same site as :91 re-accepted after refactor (content-hash id changed).

## internal/rawpred/rawpred.go — return of the deliberate zero value (accepted 57bea97)
- 13548ffece9a internal/rawpred/rawpred.go:52 statement/return — `return MayMatch`; MayMatch is the zero-value verdict, so a return-zero mutant is byte-identical.

## internal/parquetout/writer.go — parquet-export residue (accepted 6942d82; proofs held by that session)
Accepted during the parquet-export work; the equivalence proofs are not reconstructed here.
Justification pending (parquet session) — re-derive before relying on any of these.
- 12e79a7c2825 internal/parquetout/writer.go:85 expression/error-guard — accepted in 6942d82; justification pending (parquet session).
- 7865be46f9ec internal/parquetout/writer.go:329 numbers/decrementer — accepted in 6942d82; justification pending (parquet session).
- f54a44ed2ade internal/parquetout/writer.go:329 numbers/incrementer — accepted in 6942d82; justification pending (parquet session).
- 5ce25ced3604 internal/parquetout/writer.go:359 expression/comparison — accepted in 6942d82; justification pending (parquet session).

## internal/pushdown/conjuncts.go — leading `.[]` stage carries no conjunct (accepted aa139a4)
- 3662a8d1c95d internal/pushdown/conjuncts.go:40 numbers/decrementer — `range stages[1:]`→`stages[0:]`; stages[0] is the leading `.[]`, which yields no select arg and is skipped, so including it is inert.

## internal/shape/shape.go — schema-inference residue (accepted 95f8e57)
The schema projection walks Go maps (`n.kinds`, `n.fields`, `o.vals`) whose iteration order is
randomized, so this cluster is dominated by flaky-order-dependent escapes (killable only on some
seeds — labeled not-equivalent per the order-dependent-residue doctrine) plus a few genuine
zero-value / redundant-guard equivalents. Confident proofs are stated; the rest are seeded from
the 95f8e57 acceptance as pending and must be re-derived before relying on them.
- 6b1f8a784f4f internal/shape/shape.go:244 loop/break — `continue`→`break` over the randomized `n.kinds` map; escapes only when kindInteger is visited before other kinds. Order-dependent, not a true equivalent.
- 544efe91883e internal/shape/shape.go:461 statement/return — `return kindNull`; kindNull is iota 0 (the zero kind), so a return-zero mutant is byte-identical.
- fe1bac8ba09d internal/shape/shape.go:293 expression/remove — object-case guard `len(n.fields) > 0`; accepted in 95f8e57; justification pending.
- c6d581179b01 internal/shape/shape.go:308 expression/remove — array-items guard `n.elem != nil`; accepted in 95f8e57; justification pending.
- 766fe5db8afb internal/shape/shape.go:354 statement/remove — `seen[name]` dedup bookkeeping; order/coverage-dependent, accepted in 95f8e57; justification pending.
- 38583e7a2e76 internal/shape/shape.go:487 expression/remove — floatKind finite/integral guard conjunct; accepted in 95f8e57; justification pending.
- af10555f0c70 internal/shape/shape.go:567 expression/remove — formats.mergeFrom date-time AND-collapse; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- 3309d6f97333 internal/shape/shape.go:567 expression/remove — formats.mergeFrom date-time AND-collapse; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- aa907ec49a4d internal/shape/shape.go:567 statement/remove — formats.mergeFrom date-time assignment; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- 82a74944465a internal/shape/shape.go:568 expression/remove — formats.mergeFrom date AND-collapse; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- 457d6848da6d internal/shape/shape.go:568 expression/remove — formats.mergeFrom date AND-collapse; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- bf8096272848 internal/shape/shape.go:568 statement/remove — formats.mergeFrom date assignment; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- 02fd69899d32 internal/shape/shape.go:569 expression/remove — formats.mergeFrom uuid AND-collapse; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- 6be5c0bb2756 internal/shape/shape.go:569 expression/remove — formats.mergeFrom uuid AND-collapse; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- e9849fa5dcec internal/shape/shape.go:569 statement/remove — formats.mergeFrom uuid assignment; map-collapse order-dependent, accepted in 95f8e57; justification pending.
- fcc263bd5b29 internal/shape/shape.go:620 expression/logical — enumAcc.mergeFrom `e.over || o.over` over-flag short-circuit; accepted in 95f8e57; justification pending.
- a489e3fe1948 internal/shape/shape.go:620 expression/remove — enumAcc.mergeFrom over-flag guard side; accepted in 95f8e57; justification pending.
- 67c061d70537 internal/shape/shape.go:620 expression/remove — enumAcc.mergeFrom over-flag guard side; accepted in 95f8e57; justification pending.

## drivers/file/filter.go — buffered-reader guards over an already-peeked byte (accepted eebfe15)
The streaming JSON scanner peeks a byte before consuming it, so the follow-up read cannot fail,
and one comparison is a 64-bit tautology.
- 37d9c30e8d5e drivers/file/filter.go:113 expression/error-guard — Token() on the already-peeked buffered `[` cannot error.
- fa325da92f2a drivers/file/filter.go:138 expression/remove — `!array` is redundant: array-mode Decode is only reached when More() is true, so a clean io.EOF is impossible there.
- 7ce847b35cfa drivers/file/filter.go:179 expression/error-guard — Discard(1) after a successful Peek(1) of a buffered byte cannot fail.
- 31da6664b0f3 drivers/file/filter.go:225 expression/remove — `int64(int(i)) == i` is a tautology on 64-bit int platforms; load-bearing only on 32-bit.

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
- ac861b4637fd21e3a689283922c34589 cmd/diff.go:123 statement/remove — `meter.Stop()` on the --patch read path; newProgressMeter returns nil for a non-terminal writer, so under test the call is already a no-op and nothing observable changes. Killable only by a TTY-backed integration harness we do not have.
- a0d9419cf8fce8254c35a8a66e5673aa internal/diff/diff.go:184 composite/field-clear — `Op: OpAdd` dropped from the surplus-right Change literal; OpAdd is iota 0, the zero value of Op, so the cleared field holds the value it was assigned.
- 2aa60a55faebcce062fa39df2fa39d79 internal/diff/diff.go:232 composite/field-clear — same zero-value identity for the set-mode surplus-right Change literal.
- 6d0a9cacd4c78468d741252ffe9d7741 internal/diff/diff.go:260 arithmetic/base — `make(map[string]struct{}, len(a)+len(b))` capacity hint becomes `len(a)-len(b)`; a map capacity hint is an allocation sizing argument only, never a semantic one, and a negative hint is legal.
- 5ee09328de4c8bb5cae525db203582d4 internal/diff/diff.go:300 numbers/incrementer — `dp[i][j] = dp[i+1][j+1] + 1` becomes `+ 2`. Every match contributes the same constant, so the table becomes a uniform scaling of the LCS-length table (2*L with a 0 base) and every `>=` comparison in the fill and the backtrack orders identically. Provably equivalent, not merely untested.
- 15a18bbc73e394d89110ab55fa1589c9 internal/diff/diff.go:301 expression/comparison — the fill tie-break `dp[i+1][j] >= dp[i][j+1]` becomes `>`. Both directions select an equally long common subsequence, so both alignments are minimal and satisfy the contract; output was byte-identical across the 21866 exhaustive pairs and the 4000 seeded pairs.
- 6b9701424bb1fdd55fb5f04bc7611431 internal/diff/diff.go:317 numbers/decrementer — the backtrack tie-break `dp[i+1][j] >= dp[i][j+1]` becomes `dp[i+1][j] >= dp[i][j]`. That case is reached only when `same(a[i], b[j])` is false, and the fill defines a non-matching cell as `dp[i][j] = max(dp[i+1][j], dp[i][j+1])`; so `dp[i+1][j] >= max(dp[i+1][j], dp[i][j+1])` holds exactly when `dp[i+1][j] >= dp[i][j+1]`. The mutated condition is the original condition. Proven, and consistent with no counterexample in an exhaustive search to length 6 and 200000 randomized pairs.
- d61c1d13909632b24aff56c4b2ae5d2c cmd/backend.go:33 expression/context-nil — ctx passed to wrapStoreLogging feeds only `(*slog.Logger).Enabled`, which normalizes a nil ctx to `context.Background()` before the handler runs (log/slog/logger.go), so no handler can observe the substitution.
- 8d8b67cfaa5a2f9ae268a758c53d80ae cmd/backend.go:42 expression/context-nil — same slog.Enabled nil-ctx normalization, at the Enabled call inside wrapStoreLogging.
- 0994bc19cf0021d62b93daf5a7054485 cmd/jq.go:166 expression/context-nil — same slog.Enabled nil-ctx normalization, at the plan-record gating check.
- fdbace0a2e553747170ca5df7e0d1925 cmd/logging.go:250 expression/context-nil — same slog.Enabled nil-ctx normalization, at traceSink's DEBUG-sink probe.

## cmd/man.go, cmd/complete.go — man page and completion equivalents (accepted 2026-07-22, install-packaging branch)
The man generator and the dynamic completion helpers leave five genuine equivalents: two
zero-value directive identities, two guards reachable only by a `Use` string starting with a
space (which cobra's name derivation makes impossible), and a fall-through that emits a
byte-identical line.
- de2fc8366cbb cmd/complete.go:69 statement/return — completeCacheClear error path returns `nil, ShellCompDirectiveDefault`; ShellCompDirectiveDefault is 0, the directive's zero value, so the zero-value return is byte-identical.
- 2ee430f494f0 cmd/complete.go:75 statement/return — same ShellCompDirectiveDefault==0 identity on completeCacheClear's success return.
- 2cc310b7cd64 cmd/man.go:163 expression/comparison — manUsageLine `i >= 0` → `i > 0` differs only when IndexByte returns 0, i.e. a Use beginning with a space; cobra derives Name() from the first token, so no such command can exist.
- 9adb645adba3 cmd/man.go:163 numbers/incrementer — same impossible-input class: the mutated bound differs only for a Use whose first byte is the space.
- 53c0f7192ad9 cmd/man.go:246 branch/if — writeManExampleBlock's empty-line fast path; the general path renders an empty line as manVerbatim("")+"\n" == "\n", byte-identical output.

## cmd/explain.go — jq-stage annotation equivalents (accepted 2026-07-22, explain-jq-descriptions branch)
The `--verbose` per-stage jq annotation leaves four genuine equivalents: one unreachable
error guard and a note-column max loop whose result is independent of its start value and of
strict-vs-nonstrict comparison.
- 5e54a9e0b86c cmd/explain.go:130 expression/error-guard — writeAccessPlan returns an error only if render.JSON fails on a driver-produced filter map, which cannot happen for a plain JSON-able map; the call is the last statement in buildJQPlan, so removing or negating its early return leaves the output byte-identical.
- 3ea86735ab2d cmd/explain.go:246 numbers/incrementer — writeJQExplained's note column starts at `col := 0`, then becomes the max visible width over the note-bearing lines; any non-negative start converges to that same maximum when a note exists and is unused when none do.
- 98ddb7c013bc cmd/explain.go:246 numbers/decrementer — same col-init convergence, with the start decremented to -1.
- 28f644a6cfbd cmd/explain.go:249 expression/comparison — the max loop's `w > col` and `w >= col` pick the same maximum, since an equal width leaves col unchanged either way.
- e17052a49a36 cmd/explain.go:155 expression/error-guard — the buildCombinePlan twin of the 130 guard: writeAccessPlan errors only on an unreachable render.JSON failure, so the error branch never runs and clearing it is byte-identical (flaky-killed as a timeout during the batch baseline run, so recorded here on the follow-up).

## cmd/resolve.go — dotted-address walk equivalents (accepted 2026-07-24, couchbase-scope-address branch)
splitSourceArg walks the dots right to left looking for the longest prefix that names a
source. Both surviving mutants only widen that walk to consider a prefix ending at index 0,
which is the empty string, and `(*Config).Resolve` returns false for an empty name before
touching the source map (internal/config/config.go:453) — so the extra step can never match
and the walk falls through to the same result.
- 6277c273c32b cmd/resolve.go:165 expression/comparison — the walk's `i > 0` → `i >= 0` adds one iteration for a leading-dot argument, resolving `arg[:0]` == "", which Resolve rejects outright.
- 4c668185c33a cmd/resolve.go:165 numbers/decrementer — same empty-prefix iteration via `i > -1`; LastIndex returns -1 only when no dot remains, so the bound admits exactly the same unmatchable step.

## cmd — source-spec equivalents (accepted 2026-07-29, feat/source-spec branch)
Two survivors whose mutations cannot change behaviour, for unrelated reasons. The explain
entry is the third recording of one guard: it was accepted at :130 and again at :155, and
this branch moved it once more by passing the stage's own resolved url instead of a local,
which re-hashes the id. The sourcespec entry mutates an argument the callee only consults on
a path the caller has already excluded.
- b55f9e8e0c94594627063091c26cbb36 cmd/explain.go:151 expression/error-guard — the buildCombinePlan writeAccessPlan guard again: it errors only if render.JSON fails on a driver-produced filter map, which cannot happen for a plain JSON-able map, so the error branch never runs and clearing it leaves the plan byte-identical (same reasoning as the :130 and :155 entries above).
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
- 5617b58cc6aae489cd730271088f1d40 cmd/move.go:172 statement/return — returns nil instead of the "cannot be written to" error when the destination store is not a Putter; drivers/file is the only store without Put, and applyInsert refuses a file destination two statements earlier, before any store is opened, so no destination reaches this branch.
- fbfaeb5c66700fc63114587d8be26a00 cmd/move.go:172 branch/if — drops the same branch; unreachable for the same reason, and kept as the guard a future read-only driver would need.
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
- 925ed1e9b0f7 drivers/mongo/mongo.go:146 expression/error-guard — `cur.Decode(&doc)` into a bson.M cannot fail on a document the server actually returned (the guard covers malformed wire data only), so the branch is unreachable from any test that talks to a real MongoDB.
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
- 83eab4bb13c723cd1be377ed7eea28e1 drivers/file/cache.go:576 statement/return — returns false for the File tiebreak; os.ReadDir yields names already ascending and the tiebreak is by that same name, so entries sharing a DumpPath are already in tiebreak order and no correct DumpPath sort reorders them.
- 6d61c0aa8f84b3c5c64ec83d5d8d0f15 drivers/file/cache.go:122 expression/error-guard — filepath.Abs returns "" with its error and the next statement stats that "", returning the same `cacheMeta{}, false`; the guard only shortens the path to an identical result.
- fe55b530d19646560322fda114d61a31 drivers/file/cache.go:292 expression/error-guard — writeHeader fails only if a write to the just-created temp file fails, which no test can produce without an unwritable or full filesystem; writeHeader's own three write guards are covered directly through an io.Writer.
- 446be0cd7d2f0209723c35f4744b8acd drivers/file/cache.go:467 expression/remove — drops `err != nil` from cacheGet's freshness check; readHeader zeroes its header on error and cacheable guarantees `m.size >= minSize > 0`, so `h.Size != m.size` is already true whenever err is.
- c7fa9b921071d593ab0dabd526f37a95 drivers/file/cache.go:475 expression/remove — drops `err != nil` beside `len(idx.Pages) == 0`; readIndex returns a zero indexBlock on error, whose Pages is nil, so the surviving disjunct already covers it.
- 9b389bd6c4ed2eac39bcaeb01cae5346 drivers/file/cache.go:558 expression/remove — drops `de.IsDir()` from ListCache's filter; a directory that survives the .cbor extension test is then opened by headerOf, whose first read of a directory fails, so it is skipped either way.
- 1cade0d0d9d7a1d1621097ccc066de96 drivers/file/cache.go:495 numbers/incrementer — `i+1 < len(idx.Pages)` becomes `i+2`, widening only the second-to-last page's decode span to the end of the record region; decodePage copies just the wanted keys, so the returned map and the found set are identical.
- 83535655adb596a9db5b41fd3678eeb1 drivers/file/cache.go:614 statement/return — `return removed` becomes `return 0` on a remove failure; every cache file sits in the one directory whose permissions decide that failure, so a failing run has removed nothing and `removed` is already 0.
- a5776bc0b54aeb78b502cfa940066024 drivers/file/cassandra.go:99 branch/if — drops `ct = gocql.TypeText` for an untyped column; bindKeyValue's default branch returns the raw string, exactly what TypeText returns, so the bound value is identical.
- 5d6cecd548efb192558ac422ac748d85 drivers/file/cassandra.go:39 conditional/bool-literal — `cr.ReuseRecord = false`; the reader copies the header and reads each row's fields into fresh maps before the next Read, so reuse is an allocation choice with no observable effect.
- e9d2453f6f93e2a6a01f2fcb1b96e182 drivers/file/cassandra.go:39 statement/remove — the same setting removed, equivalent for the same reason.
- 7a06a12c45b6dca481dfe2fd5aee631d drivers/file/cassandra.go:40 numbers/incrementer — `cr.FieldsPerRecord = -1` becomes -2; encoding/csv treats every negative value alike (no width check), and recordForCSVRow still validates the width against the header.
- 368581278664ad349a7a64af6b604e62 drivers/file/detect.go:99 statement/return — `return FormatUnknown` becomes `return 0`; FormatUnknown is iota 0, so the returned value is unchanged.
- e9779a9c068230d06ee5988e7a00014c drivers/file/detect.go:145 statement/return — the same zero-value identity on the open-failure return.
- eeb84b031cd5a1ce102d7a2ecbb79e8e drivers/file/detect.go:151 statement/return — the same identity on the head-read-failure return.
- 853420eac2c8cb784e68654d32b65e2b drivers/file/detect.go:170 statement/return — the same identity on the empty-dump return.
- e959cd5dc29e1d1f6bc87b40b8bef60d drivers/file/detect.go:196 statement/return — the same identity on the undetectable-format return.
- 43f599983251773dc7d810463ff1148c drivers/file/detect.go:150 expression/remove — drops `!errors.Is(err, bufio.ErrBufferFull)`; the peek asks a 4096-byte bufio.Reader for 512 bytes, so ErrBufferFull is unreachable and the conjunct is inert.
- 096350411b1ca4f288cc8a590ab26c74 drivers/file/detect.go:161 expression/comparison — `len(head) > 512` becomes `>= 512`, which differs only at exactly 512, where `head = head[:512]` is the identity.
- f5f3cd412a70e9727295719eb6854896 drivers/file/detect.go:161 numbers/decrementer — `> 511` admits the same single extra length, 512, where the truncation is again the identity; the incrementer `> 513`, which does change the sniffed head, is killed by TestDetectSniffWindow.
- d89bdc4dfe249d92d0c4085a23e06340 drivers/file/detect.go:206 branch/if — drops the early `return FormatMongoexport` for an undecidable head; obj is nil there, so both key lookups miss and the function falls through to the same FormatMongoexport return.
- e97a69fca836d2c77f988578c6c9d6c2 drivers/file/detect.go:221 numbers/incrementer — `len(h) > 0` becomes `> 1`, differing only for the one-byte head "[": stepping past it leaves "" and not stepping leaves "[", and json.Decoder rejects both, so firstJSONObject reports false either way.
- 248b371646e765dbb2445df91d1c2727 drivers/file/detect.go:250 numbers/decrementer — `int(head[1])<<7`; the four bytes are combined with OR, so with head[3] zero the value never exceeds 0xFFFEFF (under the 16 MiB cap) whichever shift is used, and with head[3] non-zero both forms exceed the cap exactly when the lower bytes are non-zero, so the verdict cannot change.
- 086a59f5a54db6ae79e7ae744c2b77d1 drivers/file/detect.go:250 numbers/decrementer — `int(head[2])<<15`, equivalent by the same OR argument.
- 630fc938740df3516a7d96c6a0f8c55d drivers/file/detect.go:250 numbers/incrementer — `int(head[1])<<9`, equivalent by the same OR argument; the sibling shifts that can cross the five-byte minimum or the 16 MiB cap (<<17, <<23, <<25 and the >> forms) are all killed by TestLooksLikeBSON.
- 3010f9fcee4e99eaa07e15d1bb1f4654 drivers/file/file.go:159 statement/return — `return FormatUnknown, err` becomes `return 0, err`; FormatUnknown is iota 0.
- 7c903f929870bf9edeb9c018e42a5bb5 drivers/file/file.go:176 statement/return — the same identity on the non-file-scheme rejection.
- 344b074185e8c234a613b1468f8e9d0b drivers/file/file.go:188 statement/return — the same identity on the empty-path rejection.
- bc9401a966b3dce56304999f87843784 drivers/file/file.go:195 statement/return — the same identity on the bad-?format= rejection.
- fdb6613b3cb747a4fe5ba6218b64f69b drivers/file/file.go:182 expression/remove — drops `u.Host != ""` from the host-folding guard; with an empty host the body computes `path = "" + path`, so entering it is the identity.
- db769e0a49496a825ed5c21f293d455d drivers/file/file.go:350 conditional/bool-literal — `found[r.Key] = true` becomes `= false`; the assignment still creates the map entry, so len(found) advances identically and the early stop fires at the same record.
- 9547ee9cc2ce06cb474c6fb3e385fcdf drivers/file/mongo.go:93 expression/error-guard — Token() consuming the '[' startsArray already peeked in the same buffered reader cannot fail; the same class as the accepted drivers/file/filter.go:113 entry above.
- eee383190b764df3d797e5964432f5b0 drivers/file/mongo.go:106 expression/remove — drops `!array` from the end-of-stream check; in array mode Decode is only reached when More() is true, so it never returns io.EOF and the dropped conjunct never decides.
- 3b80a9a0b9289a9db028ebb05d5b7bbd drivers/file/neo4j.go:56 expression/error-guard — the same already-peeked-'[' Token guard as mongo.go:93.
- cef35d51bf2f33f899a949a8a887a327 drivers/file/neo4j.go:110 statement/return — `return nodeKind` becomes `return 0`; nodeKind is iota 0. The sibling zero returns on the two error paths are killed by TestNeo4jKeyspaceSelector, which asserts the kind a rejection reports.
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
- 335919d487998a1402b76002b828e906 internal/pushdown/pushdown.go:405 branch/if — intLiteral's `if !ok { return 0, false }` guard cleared. Every false path of literalOf returns a nil value, so the mutant falls through to `f, ok := v.(float64)`, which fails on that same nil and returns the identical `0, false`. The guard is a readability shortcut, not a behavioural one; the two numeric mutations of the same return are killed by TestIntLiteral's non-literal row, which pins the zero.
- a14ba2b8f94c5a42201a396545db60b1 internal/pushdown/pushdown.go:166 statement/return — flip's `case predicate.Lt: return predicate.Gt` becomes `return 0`; Gt is the first iota of predicate.Op, so 0 IS Gt. The other three arms return non-zero operators and are killed by the reversed-comparison rows in TestCompilePushable.
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
- ef1b2e4aead9c14e1deb0eab31192045 drivers/dynamodb/dynamodb.go:245 branch/if — Get's `if len(keys) == 0 { return out, nil }` short-circuit cleared; the chunk loop that follows is then `for start := 0; start < 0`, which does not run, so the same allocated empty map is returned by the path below. The short-circuit saves nothing but a comparison, and the guard above it (no table selected) is killed by TestGetGuards.
- e37357b89d71f19ef606666ae954626c drivers/dynamodb/dynamodb.go:244 numbers/decrementer — the same short-circuit's `== 0` made `== -1`, never true; equivalent for the same reason.
- 86aeab55ccdf60a8c57b260d950a79b1 drivers/dynamodb/dynamodb.go:125 expression/context-nil — `config.LoadDefaultConfig(ctx, loadOpts...)` → nil ctx. The loader consults ctx only for remote resolution (IMDS region and credentials), which an explicit region plus either static dummy or lazily-resolved credentials never reaches, so the config and error it returns are identical.
- 8390730bca2c67847191a42ebd8b4a90 drivers/dynamodb/dynamodb.go:411 arithmetic/base — `rand.Int63n(int64(base)/2+1)` → `*2+1`, widening the jitter window. TestBackoffGrowsAndCaps pins the wait's lower bound (the base) and its exponential cap, which is everything a timer can prove; the width of the random draw above the base is a distribution, and an assertion on it is a probability, not a verdict.
- 8fa7cf9ddfc232ee066bf15e7d901da6 drivers/dynamodb/dynamodb.go:411 numbers/decrementer — the same site with `/1`, equivalent for the same reason.
- f0e52161b2820663ef00a46d2b244682 drivers/dynamodb/dynamodb.go:411 numbers/incrementer — `/3`, narrowing the same window; same reason.
- 8fa37565c7127ec877718764fe038233 drivers/dynamodb/dynamodb.go:411 numbers/incrementer — `+2` in place of `+1`, one nanosecond of extra jitter range; same reason.
- 21e32368a191b718fbc19b867fc8d0a2 drivers/dynamodb/filter.go:29 expression/logical — `if f, ok := compile(pred); ok && f.expr != ""` → `||`. compile reports false only from a `return frag{}, false`, whose expr is "", so both forms are false on every failure and true on every success.
- 5b56a6bee3f7be39963668752a5d0fcd drivers/dynamodb/filter.go:29 expression/remove — the same guard's `f.expr != ""` conjunct dropped; ok is true only for a fragment with a non-empty expr, so the conjunct is inert.
- cef917dc83163a2012ff9f1731e40987 drivers/dynamodb/plan.go:25 expression/logical — the same pair on ExplainPlan's `ok && f.display != ""`, equivalent for the same reason (display, like expr, is non-empty exactly when ok).
- dc7b703757d8953f2392aa9ec1a1dd83 drivers/dynamodb/plan.go:25 expression/remove — the dropped `f.display != ""` conjunct; same reason.
- 500722b4e1c51b35fc16d5b6cc732f60 drivers/dynamodb/filter.go:138 numbers/decrementer — buildOr's `len(or) == 0` made `== -1`; an empty Or then reaches join, whose loop does not run and whose `len(exprs) == 0` check returns the identical `frag{}, false`. The incrementer at the same site, which would drop a one-branch Or, is killed by TestCompilePushable.
- ea04b88488efbbb39940f97da595cd5f drivers/dynamodb/filter.go:192 numbers/incrementer — `strconv.FormatFloat(t, 'g', -1, 64)` → precision -2; strconv selects the shortest representation for any negative precision, so the bound N literal is byte-identical (same as drivers/file/neo4j.go:264).
- 27719415840fab5cc1c1c95178cafa60 drivers/dynamodb/normalize.go:295 numbers/incrementer — the same FormatFloat identity on the write path.
- 8f40be339f598f2ff82cf2eedc3f51a3 drivers/dynamodb/normalize.go:93 numbers/decrementer — `strconv.ParseFloat(s, 64)` → bitSize 63; ParseFloat special-cases only bitSize 32 and sends every other value down the 64-bit path (same as drivers/mongo normalize.go:86).
- 10cdb27c736ce44ee5012b4cb54422ce drivers/dynamodb/normalize.go:93 numbers/incrementer — the same site with bitSize 65.
- c4a107b075b68a6af3a9749b439cc47b drivers/dynamodb/normalize.go:88 numbers/incrementer — `new(big.Int).SetString(s, 10)` → base 11; only the ok flag is read, and isIntLiteral has already restricted s to an optional sign and decimal digits, each a valid base-11 digit too. The base-9 sibling, where '9' is not, is killed by TestNumberValue.
- 007970fbff2655047d22c0a44a1fd690 drivers/dynamodb/trace.go:47 branch/if — tableOf's `if rv.IsNil() { return "" }` cleared; Elem() of a nil pointer is the invalid zero Value, whose Kind is not Struct, so the guard below returns the same "".
- ee4f978b2ea5d131d577f489382eaf36 drivers/dynamodb/trace.go:55 expression/remove — the `f.IsValid()` conjunct dropped from the TableName guard; an invalid Value's Kind is Invalid, never Pointer, so the conjunct beside it already rejects it. The `f.Kind() == reflect.Pointer` conjunct, whose removal makes IsNil panic on a non-pointer TableName field, is killed by TestTableOf.
