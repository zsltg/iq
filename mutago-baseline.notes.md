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
