# Clean Architecture — mini
Robert C. Martin.
- Do not let details become the architecture; business policy stays independent, dependencies point inward, volatile mechanisms stay replaceable.
- Source dependencies point inward toward higher-level policy; the query core must not import a NoSQL driver, CLI framework, config reader or output formatter.
- Put enterprise rules in domain entities; put application orchestration in focused use cases, one action each.
- Cross use-case boundaries with plain request and response models; never pass a driver row, framework context or flag struct into or out of core policy.
- Treat databases, the CLI, filesystem, clock, network and vendor SDKs as outer-layer details behind ports, gateways or adapters.
- Inner layers own the interfaces they need; outer layers implement them; concrete wiring lives in the composition root (`main`).
- Keep adapters humble: a driver adapter, CLI command or output presenter translates formats and delegates, it holds no business decision.
- Organize by capability, not by technical bucket; the structure reveals what the tool does, not which library it uses.
- Choose boundaries by volatility, policy importance and substitution value; use the lightest enforceable boundary — a package or interface — not a service split.
- Enforce boundaries in code: package layout, interfaces, import rules, tests; a diagram or a `common` folder is not enforcement.
- Test entities and use cases without a real database, network or driver, using fakes; test adapters separately at the seam.
- When a driver, config read or transport format leaks into core policy, move the translation outward; when an adapter holds a rule, move it inward.
- Preserve behaviour while improving dependency direction; prefer incremental boundary extraction over rewrites; name architectural debt you cannot safely fix now.
