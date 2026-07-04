# Refactoring — mini
Martin Fowler. Read on demand when restructuring existing code without changing behaviour. Pairs with clean-code.mini (smells) and a-philosophy-of-software-design.mini (target shape).
- Refactoring is behaviour-preserving design work in small steps; never disguise a feature, migration or redesign as cleanup.
- Work in small, reversible, buildable, testable steps; split a patch too large to reason about locally.
- Establish a safety net first: characterization tests for unclear behaviour; never delete a failing test to finish cleanup.
- Use preparatory then follow-up refactoring around a change: reshape the local structure that makes the change awkward, make the behaviour change, then clean debt it introduced.
- Fix the current blocking smell, not every smell in sight: duplication, long function, long parameter list, global, divergent change, shotgun surgery, feature envy, primitive obsession, repeated conditional, speculative generality.
- Prefer the simplest named move: rename, extract, inline, move, introduce a parameter or value object, encapsulate a field or collection, decompose a conditional, guard clause, substitute a clearer algorithm.
- Make names and functions reveal intent; rename before deeper work when a bad name blocks understanding.
- Put behaviour and state with the concept that owns them; split a module with more than one reason to change; separate policy from formatting, transport, persistence and I/O.
- Simplify conditionals honestly with guard clauses, extracted predicates or lookup tables only when they reduce repeated branching.
- Use abstraction only when current evidence justifies it; remove pass-through layers, vague utilities and just-in-case interfaces.
- Preserve error semantics unless deliberately changing them; keep the patch reviewable, structural edits separated from behaviour where practical.
- Stop when the requested change is easy, the blocking smell is gone and the next cleanup would be speculative.
