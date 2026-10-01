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
- da214413f2737db99961296445e31f16 drivers/redis/kv.go:28 branch/if — the same short-circuit's body cleared; equivalent for the same reason, and it saves only the two no-op pipelines.
- d5c43949128e64f8fcace984b59a0dd9 drivers/redis/kv.go:308 branch/if — the same guard's body cleared; equivalent for the same reason (newly covered by the multi-round scan test, which ends on an empty page).
- df468c19faeee5b4855eb3e06e7f8690 drivers/redis/kv.go:44 expression/context-nil — `p.Type(ctx, k)` → nil; queued command context, discarded by BatchProcess.
- 82cf8c47e08e4bf648c2549f9998eceb drivers/redis/kv.go:65 expression/context-nil — `readerFor(ctx, ...)` → nil; the ctx reaches only the queued value command.
- 8e3cb2b99806944a03766b588f09b7f3 drivers/redis/kv.go:109 expression/context-nil — `p.Get(ctx, key)` → nil; queued command context, discarded by BatchProcess.
- 7d856bc2fba007d175485f9528a6817e drivers/redis/kv.go:111 expression/context-nil — `p.HGetAll(ctx, key)` → nil; same.
- 1fb448713cd88962f83dc9c79c3c175a drivers/redis/kv.go:113 expression/context-nil — `p.LRange(ctx, key, 0, -1)` → nil; same.
- 09c8a3967bc3b986c5b79cf08a22f839 drivers/redis/kv.go:115 expression/context-nil — `p.SMembers(ctx, key)` → nil; same.
- c90adcfa21033ba9fd3b0f7ebec6c2de drivers/redis/kv.go:117 expression/context-nil — `p.ZRangeWithScores(ctx, key, 0, -1)` → nil; same.
- 9235d952f28969607ac6973afc1a54eb drivers/redis/kv.go:119 expression/context-nil — `p.XRange(ctx, key, "-", "+")` → nil; same.
- f60032c63c642e82b7f4a5a12fda2a86 drivers/redis/kv.go:121 expression/context-nil — `p.JSONGet(ctx, key)` → nil; same.
- 4b7421b81e3b7df89422c407399ec562 drivers/redis/kv.go:27 numbers/decrementer — Get's `len(keys) == 0` short-circuit made `== -1`, never true; an empty read then falls through to two pipelines that queue nothing, and `(*Pipeline).Exec` returns before any round trip, so the identical empty map comes back.
- b8332d504b6a15ebea35bef846cb687e drivers/redis/kv.go:307 numbers/decrementer — scanPages' `len(page) == 0` flush guard made `== -1`; an empty page reaches build, whose zero-key read is the same no-op pipeline, and the `len(batch) == 0` check below returns the identical nil.
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
- bcda4e484c659dcd1b8d4a1ba61f1296 drivers/redis/write.go:23 numbers/decrementer — Put's `len(batch) == 0` short-circuit made `== -1`; an empty batch reaches putUpsert, whose pipeline queues nothing, so the zero WriteStat and nil error are identical.
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
- 7beb181f5718acd4619b4ffa114e7674 drivers/couchdb/couchdb.go:113 expression/remove — parseURL's path fallback `if p := strings.Trim(u.Path, "/"); p != "" && !strings.Contains(p, "/")` with the `p != ""` conjunct dropped. The block is reached only when `db == ""`, and the conjunct can only newly admit `p == ""`, whose body then runs `db = p`, assigning "" to a db that is already "". Both forms leave the same connConfig.
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
- 5986e10e44283e82ec27bdf8ec5c28c5 internal/config/config.go:166 expression/error-guard — Save's `if err := f.Chmod(0o600); err != nil` guard cleared. The mutation keeps the call and drops only its error branch, and fchmod on a temp file this process just created in a directory it owns fails only for EROFS or EPERM: a read-only or unwritable parent makes `os.CreateTemp` fail two lines earlier instead, so no hermetic test can reach the branch. Same class as the accepted drivers/file/cache.go:292 entry.
- 061b6cb1bc1b16906b4432e09eb26d62 internal/config/config.go:170 expression/error-guard — the TOML encode guard. `toml.NewEncoder(f).Encode(c)` fails only when the writer fails: the encoder was probed with invalid UTF-8 in a key and a value, and with a NUL byte, and escaped all three rather than erroring, so no Config shape produces one. A write to a just-created private temp file then needs a full or quota-limited filesystem, which is the accepted drivers/file/cache.go:292 case.
- 0df46f720e89d90f15fa3fdcc3304172 internal/config/config.go:174 expression/error-guard — the temp file's `f.Close()` guard. Every write has already returned by then, so close(2) on a local regular file reports an error only for a delayed-writeback EIO or ENOSPC, which no in-process test can arrange.
- d227e4fa42331b2df7c5d87664e3b333 internal/config/config.go:250 expression/remove — moveSource's `if c.Group != "" && !c.hasGroup(c.Group)` with the non-empty conjunct dropped. The conjunct can only newly admit `c.Group == ""`, and `hasGroup("")` tests the prefix "/", which no stored handle carries, so the body then assigns "" to a Group that is already "". (With a hand-written "/x" handle `hasGroup("")` is true and the branch stays skipped either way.) Same class as the accepted drivers/couchdb/couchdb.go:113 entry.
- 8727d6e6f7cd10c9cdd62603dce5379d internal/config/options.go:49 branch/if — GetOption's `if !ok { return "", false }` body cleared. optionsFor returns `ok == false` only together with a nil map, and the next statement indexes that same nil map, yielding the identical "" and false. The guard is a readability shortcut, not a behavioural one; same class as the accepted internal/pushdown/pushdown.go:405 entry.
- 6e235b84704b53af98d39f9e0ab9dd94 internal/config/options.go:90 expression/comparison — OptionList's `sort.Slice` less function widened from `<` to `<=`. opts is built by ranging a map, so no two Key values are equal, and on distinct keys `<=` is the same predicate as `<`. That the call sorts at all is pinned by TestOptionListSortsByKey, which kills the `return false` mutant on the same line.
- 5d5732b03749eec931d19cd8409094c7 internal/config/options.go:83 expression/remove — OptionList's `if handle != "" && !ok` with the non-empty conjunct dropped. optionsFor's first statement returns `ok == true` for every empty handle, so `!ok` is already false wherever the dropped conjunct would be, and the guard beside it decides every case. Same class as the accepted drivers/couchdb/write.go:69 entry.

## internal/render — full-scan equivalents (accepted 2026-08-31, test/internal-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 8 escapes, 7 of them
killed. The package is exercised mostly from `cmd`, and a mutant counts as killed only by its own
package's tests, so the assertions had to move here. All seven were assertion gaps in the
colored-JSON writer: the plain round trip was compared with `require.JSONEq`, which unmarshals
both sides and so forgives exactly what the two encoder settings decide — HTML escaping and the
two-space layout — and the colored encoder was never asked to render an HTML metacharacter, never
re-encoded often enough for Go's randomised map order to expose an unsorted pass, and never made
to fail. The plain and colored round trips now compare line by line, eight keys are re-encoded ten
times, and a channel drives the encode failure. The update run surfaced one more escape the new
failure test had turned from uncovered into covered, the `%w` wrap on that failure; it was killed
with an `errors.As` assertion on the `*json.UnsupportedTypeError` cause, not accepted, and its id
removed from the baseline before the closing run verified the one below. It survives because the
call it removes only re-asserts the library default. It was verified by hand — the mutation applied
to the real file, the whole package suite still green — before acceptance.
- bed35ced74f680ddc78ea9611ec83eb3 internal/render/json.go:67 statement/remove — the colored branch's `enc.SetSortMapKeys(true)` call dropped. `jsoncolor.NewEncoder` constructs with `flags: EscapeHTML | SortMapKeys`, so the call sets a bit that is already set and removing it changes no byte of the output. The call stays as a deliberate pin against a library default change, and it is not unasserted: the `true`->`false` flip on the same line is killed by TestNewJSONEncoderColoredSortsMapKeys. (The `SetEscapeHTML(false)` call two lines above is the opposite case — it clears a default bit — and both its removal and its flip are killed.)

## internal/rawpred — full-scan equivalents (accepted 2026-08-31, test/internal-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 6 escapes, 2 of them
killed. isObject had never been called directly, so its arms were only ever reached through
getField, where every false answer collapses to the same fieldAmbiguous the mutated one produces;
a direct table now pins the arms apart, including an array and a string that each carry a `{`
after the first byte. The regex dedup guard was asserted only by entry count, which a
recompile-and-overwrite leaves unchanged, so the test now preps the same (pattern, flags) a second
time and requires the stored *Regexp to be the same pointer. The three below survive because the
mutation cannot change what the code does. Each was verified by hand — the mutation applied to the
real file, the whole package suite still green — before acceptance. The fourth survivor,
rawpred.go:52 statement/return, was already accepted on an earlier branch and is justified above.
- b65de6a58f1ae86fedf8e6fc00a7d2cb internal/rawpred/rawpred.go:728 branch/case — isObject's `case ' ', '\t', '\n', '\r': continue` body cleared. The switch is the last statement in the range body, so falling out of an empty case and continuing the loop are the same control flow; the next byte is read either way.
- 3c63527171ee007923126445b6baa154 internal/rawpred/rawpred.go:728 loop/break — the same arm's `continue` replaced by `break`. Inside a switch, `break` leaves the switch, not the loop, so it too falls to the end of the range body and reads the next byte. (The `default: return false` arm on line 732 is the opposite case — clearing it really does read on — and it is killed by the array and string rows of TestIsObject.)
- d0db686f2471e58afa0132a2be2f5b74 internal/rawpred/rawpred.go:608 expression/error-guard — foldArray's `if cbErr != nil` guard cleared. jsonparser's ArrayEach returns on its own parse error before ever invoking the callback (`if e != nil { return offset, e }` guards the `cb(v, t, ..., e)` call in parser.go), so the error it hands the callback is always nil and the branch is unreachable for any input. The guard stays as the library's callback contract; the walk error it does not cover is the ArrayEach return value, whose guard is killed by the malformed-array rows.

## internal/numfmt — full-scan equivalents (accepted 2026-08-31, test/internal-mutation branch)
First full scan of the package (only its diff lines had ever been gated): 3 escapes, none of them
an assertion gap. Two return a named constant that IS the zero value the mutant substitutes, and
the third is a narrowing guard that only has an effect on a 32-bit platform, which the gate does
not run on. Each was verified by hand — the mutation applied to the real file, the whole package
suite still green — before acceptance.
- dd0c601a1d638e7ae052249c40e639c0 internal/numfmt/decimal.go:37 statement/return — ParseDecimalMode's `case "auto": return DecimalAuto, nil` becomes `return 0, nil`. DecimalAuto is the first iota of DecimalMode, deliberately so ("a store opened without an explicit mode defaults to it"), so 0 IS DecimalAuto. The other two arms return non-zero modes and are killed by TestParseDecimalMode.
- 939386efa8e17c4320ce6076daec5835 internal/numfmt/decimal.go:43 statement/return — the same function's `default: return DecimalAuto, fmt.Errorf(...)` becomes `return 0, ...`, identical for the same reason. That the arm errors at all is pinned by the invalid-value rows, which assert the message.
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
- 1e973c7c2926c8a2523d2dce8d009647 internal/diff/diff.go:467 branch/if — equal's `if _, ok := asFloat(b); ok { return false }` body cleared, so a number on the right falls through to `reflect.DeepEqual(a, b)`. The branch is reached only when `asFloat(a)` failed, so a's dynamic type is outside the normalized numeric set while b's is inside it; DeepEqual reports false for any two values of different dynamic types, so the fall-through returns the same false the guard returned. No input can separate the two forms. The guard stays as the statement that a number is never equal to a non-number, and it is not unasserted: the negation of the same condition is killed by the int-versus-string rows.
- 38e51015625f7feb403fd818e673222f internal/diff/diff.go:486 numbers/decrementer — asFloat's `default: return 0, false` becomes `return -1, false`. asFloat is unexported and has two callers, both in equal, and both read the float only inside the `ok` branch; a false second result means the first is discarded, so no value it carries is observable.
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
- a99b26be16234e839cad9b703fdf3066 internal/query/dump.go:174 expression/error-guard — JSONSource's `if _, err := dec.Token(); err != nil` guard, on the token that consumes a top-level array's `[`. The guard is reached only when startsJSONArray has already peeked that `[`, which leaves it buffered in the bufio.Reader, and bufio serves buffered bytes without touching the underlying reader; the decoder needs exactly that one byte to return Delim('['). Probed with a reader that fails immediately after handing out `[`: the failure surfaces at the following `dec.Decode`, whose guard is killed, never at this one.
- b56902b659d6f9f8e09226e3005cad2f internal/query/dump.go:155 expression/remove — convertNumber' `if i, err := t.Int64(); err == nil && int64(int(i)) == i` with the round-trip conjunct dropped. The conjunct exists so a 32-bit build falls through to the *big.Int branch for a value that overflows its `int`; on the linux/amd64 the gate runs, `int` is 64 bits and the round trip is the identity. Same class as the accepted internal/numfmt/convert.go:41 entry, which is the same guard in the read path.
- b2cd4944894a055379941789463c373e internal/query/dump.go:188 expression/remove — JSONSource's `if !array && errors.Is(err, io.EOF)` with the `!array` conjunct dropped, so an io.EOF from Decode would end an array walk quietly instead of being reported. In array mode Decode is only reached past `if array && !dec.More()`, and encoding/json's More returns false exactly when its peek fails, which at end of input it does; so inside an array the decoder is never called with nothing left to read, and it answers a truncated array with io.ErrUnexpectedEOF, which this condition does not match either way. The other two mutants of the same condition are the reachable ones, and TestJSONSourceRejectsTrailingGarbage kills both.

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
- 4c2b9ffba20864009b884446505bd613 drivers/hbase/filter.go:30 expression/error-guard — ScanFiltered's `if err != nil` after `hrpc.NewScanStr(ctx, s.table, opts...)`. gohbase's constructors fail only when an option function fails (`baseScan` returns `applyOptions`' error and nothing else), and the two options here are `hrpc.MaxVersions(1)`, which errors only for a call that is not a Get or Scan or for a count above MaxInt32, and `hrpc.Filters(f)`, which errors only if `ConstructPBFilter` does, i.e. a `proto.Marshal` of a well-formed message. No input reaches the branch. Same class as the write.go:45 entry below.
- 4a9c13a319df546e3ed695312b651c07 drivers/hbase/hbase.go:105 branch/if — Open's `if dl, ok := ctx.Deadline(); ok { ... }` body cleared, so the ZooKeeper, region-lookup and region-read timeouts are left at gohbase's own defaults.
- a9a81c63acc8912b13d1ef62aab03c79 drivers/hbase/hbase.go:106 statement/remove — the `opts = append(opts, gohbase.ZookeeperTimeout(d), ...)` inside that branch, the same change by another route. Both are unobservable against a reachable cluster, and against an unreachable one gohbase does not honour either bound as a deadline: probed with an already-expired context against the live cluster, and with a 5-second context against a refused port, `Open` ran past ten minutes and past two minutes respectively rather than returning, because the ZooKeeper client retries the lookup and `ClusterStatus` takes no context of its own. So no bounded test can separate the two forms. The timeouts stay as the honest expression of the caller's deadline; that `Open` does not in fact fail fast on an unreachable quorum is a driver bug to fix separately, not a missing assertion.
- 3968ff34cb25de5b9e23b668c371d569 drivers/hbase/hbase.go:117 expression/error-guard — Open's `if _, err := st.admin.ClusterStatus(); err != nil` guard cleared. `ClusterStatus` returns an error only when the cluster cannot be reached, which is exactly the case the two probes above showed hangs rather than returning, so the branch is unreachable in bounded time from a test. Its body is reachable in principle and stays.
- 1776c8f21dc36c86651923884fe2b278 drivers/hbase/hbase.go:322 numbers/decrementer — execScan's `limit := 0` becomes `limit := -1`. The variable is overwritten whenever a limit argument is given, and its only other reader is `if limit > 0 && len(rows) >= limit`, which is false for 0 and -1 alike, so no scan can tell the two initialisations apart. That the cap itself works is pinned by TestQueryScanLimitOne and TestQueryScanWithLimit, which kill the incrementer on the same line and on the comparison.
- 8f294e8c90fdcb1f13cb00b98a822f0a drivers/hbase/normalize.go:219 numbers/decrementer — toFloat64's `strconv.ParseFloat(n, 64)` bit width becomes 63.
- e55c46ed4e95912550a77f73604d5c8a drivers/hbase/normalize.go:219 numbers/incrementer — the same width becomes 65. strconv's `parseFloatPrefix` branches on `bitSize == 32` and nothing else, so 63, 64 and 65 all take the identical `atof64` path and return the identical value and error; unlike ParseInt, ParseFloat has no range check to widen or narrow. The ParseInt call two functions above is the opposite case, and its bit-width mutants are killed by the largest- and smallest-int64 rows of TestToInt64.
- 9fddefd1d54bf2948977ac86903c2380 drivers/hbase/normalize.go:71 statement/return — parseColType's `default: return ctAuto, fmt.Errorf(...)` becomes `return 0, ...`. ctAuto is the first iota of colType, deliberately so ("a column absent from the map is ctAuto"), so 0 IS ctAuto. That the arm errors at all, and that every named arm returns its own non-zero type, is pinned by TestParseColType.
- e489c6e21e9851bf6a0bdaa23d41f351 drivers/hbase/normalize.go:253 statement/return — colTypeFor's `return ctAuto` for an undeclared column becomes `return 0`, identical for the same reason. The declared branch above it returns the stored type and is killed by the typed rows of TestRowFromCells.
- e3d5e1a379fb395b8de6739dee4b99a9 drivers/hbase/write.go:45 expression/error-guard — Put's `if err != nil` after `hrpc.NewPut(ctx, []byte(s.table), rk, values)` on the insert-only path. `baseMutate` stores its arguments and returns `applyOptions`' error, and this call passes no options, so the constructor cannot fail for any record. Same class as the filter.go:30 entry above. The upsert path carries the identical guard a few lines below, and mutago reaches the same verdict about it by the other route: its body at write.go:68 is reported NOT COVERED, because no record can make the constructor fail.

Neo4j, accepted 2026-09-07 after the first full scan of the package (687 mutants, 48 escapes;
37 killed). Every entry below survives the whole suite, and each was confirmed by hand: apply the
mutant diff, run the package tests, see them pass, restore. The five context-nil entries are true
for neo4j driver v5.28.4 only; a driver upgrade may make them killable while they stay recorded
here, so re-check them when the SDK moves.

- 298e2e7f0fd1fb9845bac5d8c284dd4d drivers/neo4j/filter.go:26 branch/if — the non-narrowing early return. Every non-narrowing path of translate returns an empty where, and ScanBatches is pagedScan(ctx, "", nil, fn), so the fall-through runs the same query. It differs only by unused f0..fN parameters, which Neo4j accepts and the trace never prints (it logs the cypher without parameters).
- dce9edc83ff61c97a206243ee0015234 drivers/neo4j/inspect.go:66 expression/context-nil — readRows passes a nil context to session. Driver v5.28.4 NewSession gives the context to computeCacheKey alone, which ignores it unless SessionConfig.Auth is set, and this driver never sets it.
- 478cf8fc2fb53c4d33b07c1ce9e04374 drivers/neo4j/inspect.go:67 expression/context-nil — the deferred sess.Close(nil). The result is discarded and the context reaches only pool and router cleanup, which no caller can observe.
- a9e25828b1be84d62366cdd519c5fed3 drivers/neo4j/neo4j.go:189 expression/context-nil — drv.Close(nil) on an Open error path. driverWithContext.Close sets d.pool = nil before it reads the context, the return is discarded by `_ =`, and Open returns the same error and no Store, so nothing observable differs.
- aeff47385385d4d61b1ad2a7f3346251 drivers/neo4j/neo4j.go:205 expression/context-nil — the same Close(nil) on the second Open error path, for the same reason.
- 5f5f9808a15d113e86883c429ea2b0fb drivers/neo4j/neo4j.go:261 expression/context-nil — NewSession(nil, ...). Same as inspect.go:66: the context reaches computeCacheKey alone and is ignored without SessionConfig.Auth.
- d337011e0e53169a08882e5f038ad66a drivers/neo4j/neo4j.go:443 expression/context-nil — s.session(nil, ...). Same reason as neo4j.go:261.
- 811eef4ad362892a16888080cfd62d01 drivers/neo4j/neo4j.go:537 expression/context-nil — driver.Close(nil). d.pool = nil happens before the context is used, so the driver closes either way and the context reaches only socket teardown.
- 3804221283486b6225fcc6969342d4a1 drivers/neo4j/neo4j.go:311 expression/remove — the `s.target.key != ""` conjunct of the duplicate-key guard. Without ?key= the key is elementId, and WHERE elementId(n) IN $ids cannot return one id twice, so dup is never true on that branch.
- f1996a65b72e580c7dbc4ad343fe2af8 drivers/neo4j/neo4j.go:518 expression/remove — the `!ok` disjunct of `!ok || len(items) != 1`. A failed type assertion leaves items nil, so len(items) != 1 is already true and both forms return false.
- 413b18704336923c1f7c242b795d1f94 drivers/neo4j/normalize.go:84 numbers/incrementer — the -1 precision of strconv.FormatFloat. strconv treats every negative precision as shortest (`shortest := prec < 0`), so -1 and -2 produce the same text.
- 6ff99294550a67616b4207c48087c766 drivers/neo4j/write.go:199 expression/error-guard — the Consume error guard of DETACH DELETE. NOT equivalent, but unkillable without fault injection: the statement binds only $eids and elementId(), neither of which can raise at consume time, syntax and database errors surface at Run, Neo4j Community has no constraint a delete can violate, and a cancelled context always fails the earlier resolve Run first. A deadline timed to expire between Run and Consume would be flaky, so no test was written.
- acdc7481f2cf6f6c39a20cf18cef9276 drivers/neo4j/write.go:104 expression/remove — the `!ok` disjunct of `!ok || keyVal == nil`. A failed type assertion leaves keyVal nil, so the second disjunct already covers it.
- 54c1a923120c4509f713c3cc8089b171 drivers/neo4j/write.go:181 expression/remove — the `s.target.key != ""` conjunct of Delete's duplicate-key guard, for the same reason as neo4j.go:311.
- 7bf22d86ca3ec15be3c9d55b8c794c3f drivers/neo4j/write.go:34 statement/return — `return stat` where stat is still its zero value. Nothing writes to stat before this line, so the mutant returns the same value.
- 556958714b0de87e4484c16f29561a93 drivers/neo4j/write.go:43 statement/return — the same zero-value return on the next early path.
- cb38925ce60476663a9f97d2282c3546 drivers/neo4j/write.go:47 statement/return — the same zero-value return on the empty-batch path. The test asserts the exact value, and the mutant returns that value too.

Elasticsearch, accepted 2026-09-09 after the first full scan of the package. Every entry below
was confirmed by hand: apply the mutant diff, run the package tests, see them pass, restore.
The closing run scored 599 killed, 9 equivalent and 90 not covered. Read the note on
elasticsearch.go:133 before you touch that line: the site holds two operand variants and only
one of them is equivalent.

- cfe6c6f5e179c0956aa53891e61aaad6 drivers/elasticsearch/elasticsearch.go:301 branch/if — the body of that same early return, for the same reason.
- 808ba5da7eb543934244a0a380e73460 drivers/elasticsearch/elasticsearch.go:133 expression/remove — the first conjunct of the lenient path-as-index rule, the test that the trimmed path is not empty. It only decides the empty case, and there the assignment sets index to the empty string it already holds. The sibling operand, the check that the path holds no slash, does change behaviour and a test kills it.
- 24b90a952906e8193eeaa058b4e6e130 drivers/elasticsearch/elasticsearch.go:300 numbers/decrementer — the len(hits) == 0 early return. With no hits the copy loop runs zero times, len(page) > 0 is false so fn is never called, and the len(hits) < s.pageSize test returns on the same iteration. The two paths agree for every page size of one or more, and pageSize is scanBatch on every path that reaches here.
- 623005b05d81beb25bc45b14e310ada9 drivers/elasticsearch/filter.go:50 composite/field-clear — the class: stringClass field of the keyword and ip case. stringClass is iota, so it is the zero value of fieldClass and clearing the field is a no-op.
- a012bb4171c8f10389864efbe5865517 drivers/elasticsearch/filter.go:59 composite/field-clear — the same field on the text sub-field case, for the same reason.
- 2ff0f65b4bc4ad51c50759283b7b5f9f drivers/elasticsearch/filter.go:273 expression/remove — the e.Value == nil disjunct of the push guard. matchesLiteral type-asserts a nil value against string, float64 and bool, returns false for each, and falls to false in the default, so a null literal never pushes either way.
- edc0317f0eafe93f186cf96f5f0aed5e drivers/elasticsearch/inspect.go:61 expression/error-guard — the json.Unmarshal error guard. The argument is a json.RawMessage that a successful decode produced, so it is always valid JSON and an unmarshal into any cannot fail. The branch is unreachable.
- 2cf1585dd1d3c631b2404c491783451c drivers/elasticsearch/write.go:42 expression/error-guard — a json.Marshal error guard over a map whose only values are strings, the bulk action line. json.Marshal cannot fail on that shape, so the branch is unreachable.
- 55272235d47f149d13bc11209a2a4669 drivers/elasticsearch/write.go:166 expression/error-guard — the same guard over the delete action line. The sibling guard at write.go:45 marshals caller data, can fail, and a test kills it.

Couchbase, accepted 2026-09-26 after the verdict re-scan of the package (657 mutants, 579
killed, 15 escaped, 63 not covered, 0 errors). Nine escapes were test gaps and are now killed.
Every entry below was confirmed by hand against a live cluster: apply the mutant diff to the
fixed tree, run the package tests, see them pass, restore. The couchbase.go:383 decode guard
is not here: it moved into readPage, and TestReadPage kills it now.

- 03f465282ea2ab1de8d3cd108574f6e2 drivers/couchbase/couchbase.go:287 composite/field-clear — the Context field of BulkOpOptions in bulkDo. gocb v2.12.4 ignores that field on the couchbase:// KV path (it reads Timeout only), so clearing it changes nothing. bulkDo refuses a cancelled context before the call, and tests kill that check.
- d99383f25be1adf6e32bf6868049df0c drivers/couchbase/couchbase.go:324 expression/error-guard — the decode guard in Get. The bulk get uses rawTranscoder, whose Decode fails only for a target that is not *[]byte. The target here is always a *[]byte, so the branch is unreachable.
- 8c63029eadfa51b0c3dfbf77fcf5ba29 drivers/couchbase/inspect.go:45 statement/remove — sort.Strings on the bucket names. The test cluster holds one bucket, so the order cannot differ. A kill needs a second bucket in the test cluster, which costs about 15 to 20 s per suite run. Reversible: remove this entry when the suite gets a second bucket.
- f05e1f5ec07a3ef80cf1d53906d5538e drivers/couchbase/inspect.go:85 statement/remove — the WHERE clause that limits the index list to the source bucket. With one bucket in the cluster the filter removes nothing. Same cost and same reversal as inspect.go:45.
- aee2807a16f12adee9de297bbd985046 drivers/couchbase/write.go:148 statement/return — the empty-keys return in existingKeys, an empty map changed to nil. The only caller reads the result with `_, ok := existing[key]`, and a lookup in a nil map also gives false. One hand run failed, in TestQueryScopeQualified (a COUNT(*) that returned 1 instead of 3). Two more runs passed, so that failure was a flake in the count test, not a kill.

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
- 69a6bab3fd68 internal/diff/patch.go:27 expression/error-guard — new in v2.10.16. The error guard after json.Marshal(patch) becomes `if false`. jsondiff.Compare marshals both inputs and unmarshals them into plain JSON values before it builds the patch, so every operation value is a map, slice, string, float64, bool or nil. json.Marshal cannot fail on those values, so the guard is unreachable. A value that cannot be marshaled fails earlier in Compare, and TestPatchMarshalError asserts that error.

## internal/render, mutago v2.10.16 re-baseline (accepted 2026-09-29, test/mutation-rebaseline-small branch)
The mutago bump from v2.7.7 to v2.10.16 changed every mutant id. A full scan of the package found 9 escapes. Eight were a test gap: v2.10.16 adds a field-clear mutant for each of the eight fields of the jsonColors palette, and no test compared the exact colored bytes. TestNewJSONEncoderColorsEachRole now pins the output of each syntax role (key, string, number, bool, null, bytes, time and punctuation), and each of the eight clears fails it when applied by hand. The one below is the same edit as an accepted entry above, with a new id. It was applied by hand to the real file, and the full package suite passed.
- 7d94bfbcfb47 internal/render/json.go:67 statement/remove — replaces bed35ced74f6 (json.go:67 statement/remove). The `enc.SetSortMapKeys(true)` call is removed. jsoncolor.NewEncoder v0.9.1 starts with `flags: EscapeHTML | SortMapKeys`, so the call sets a bit that is already set. The flip to `false` on the same line is killed by TestNewJSONEncoderColoredSortsMapKeys.

Keyring default store, accepted 2026-09-29 in the pull request that makes the keyring the default store of `iq add`.

- 9a221f56b0b51dedaf1de1918384b218 cmd/password.go:32 branch/case — the ErrNotFound case of keyringFree returns nil. The mutator replaces the case body with a zero-value return, which is `return nil` again, so the mutant is the same code.
- 9aeae887cb1de5c888584aacb549d977 cmd/password.go:113 expression/error-guard — the UseKeyring error guard in keepPassword. The add command added the source to the config a few lines earlier with cf.Add, and UseKeyring fails only for an unknown source, so the branch cannot be reached. mutago scored the same guard KILLED in one earlier run; with the mutation applied by hand, the full cmd suite passes.
- f47547a1950ba78bf71b5349ab4cb2b1 cmd/password.go:122 expression/error-guard — the SetSourceURL error guard in the fallback of keepPassword. The source exists and the raw URI passed the scheme and parse checks, so it is not blank. SetSourceURL fails only for an unknown source or a blank URL, so the branch cannot be reached.
- a4302bede0d913e4708c747159c3f4ba cmd/password.go:122 conditional/negated — the same guard with `err == nil`. SetSourceURL returns nil, so the mutant returns `false, nil` from the guard, which is what the next line returns.
- fc3ff1d05b4e76156483f3a0215e55cb cmd/keyring_cmd.go:286 expression/error-guard — the write error of a `would migrate` line under --dry-run. The output is the command's writer, and no test writer fails, so the branch cannot be reached in a test.
- 15045794c5f126a17eda63f81545e7e0 cmd/keyring_cmd.go:298 expression/error-guard — the UseKeyring error guard in the migrate loop. migrateTargets resolved the source from the same config, so UseKeyring cannot fail for an unknown source.
- d0b761fc9d1e8678984a5fcac08b75c9 cmd/keyring_cmd.go:302 expression/error-guard — the SetSourceURL error guard in the migrate loop. The source exists, and migrateSecret returned a stripped URL that is not blank, so SetSourceURL cannot fail. mutago scored it KILLED in one local run; with the mutation applied by hand, the full cmd suite passes.
- e4dd7cceaed99e103693bed574cca5ae cmd/inspect.go:411 loop/break — the `continue` that skips the columns read when no table is set, in the reads builder of inspectCassandra. columns is the last name in cassandraInspectCmds, so `break` at that name ends the loop at the same point as `continue`. Both forms build the same map.
- a16d692b2fa184be4aad87122f92888a cmd/diff.go:174 expression/remove — `!m.data` removed from the default-layer check in diffModes.validate. The branch only sets `m.data = true`. When m.data is already true, the assignment changes nothing, so both forms leave the same modes.
- 78166f5f603a2e75640963621736ee78 cmd/diff.go:201 statement/remove — `meter.Stop()` on the --patch path of runDiff. newProgressMeter returns nil for a writer that is not a terminal, so under test the call does nothing. The same case as the ac861b46 entry for the old diff.go:123, which the refactor moved here.
- ff1dc6972d154428ed50cdd5c399dc56 cmd/diff.go:215 statement/remove — `meter.Stop()` on the --data path of runDiff. The same reason as the 78166f5f entry above.
