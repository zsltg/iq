# Clean Code — mini
Robert C. Martin.
- Working code is not clean code; treat cleanliness as part of delivery and leave touched code cleaner within scope.
- Write for local reasoning: a reader follows the path without reconstructing hidden state or wide jumps.
- Precise names, one term per concept; rename when vocabulary hides intent or forces a compensating comment; ban vague `data`, `handle`, `process`, `manager`.
- Small functions, one thing, one level of abstraction, told top-down so intent precedes detail.
- Few meaningful parameters; no boolean flag args, no output params, no grab-bag argument lists; model the concept instead.
- Command-query separation: a function does something or answers something, never both, and never mutates behind the reader's back.
- Keep the happy path readable; isolate error, invalid-state and cleanup handling; prefer typed results or options over null-like sentinels.
- Expose behaviour, not raw representation; no train-wreck access chains, no utility dumping grounds, no mixed-responsibility modules.
- Keep construction, driver, persistence, transaction and vendor details outside business behaviour, behind a local adapter.
- Public APIs small, explicit, hard to misuse; encode required order and boundary logic where readers see it.
- Comments carry why, constraints, warnings or external contracts only; never narrate code, never leave commented-out code.
- Tests are production code: readable, deterministic, aligned with the contract they protect; add or update the test that guards changed behaviour.
- Let design emerge through tests, duplication removal and expressiveness; add no needless abstraction or infrastructure.
- Remove the smell that most increases change cost in the code you touch, but do not silently broaden the task.
