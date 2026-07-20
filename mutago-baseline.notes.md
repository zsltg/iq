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
