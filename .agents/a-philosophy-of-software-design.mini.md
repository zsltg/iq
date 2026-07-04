# A Philosophy of Software Design — mini
John Ousterhout. Design-judgment doctrine, always loaded: module shape, deep-vs-shallow, the complexity budget. Complements clean-code.mini, which owns naming and functions.
- Complexity is anything making code hard to understand or change; its causes are dependencies and obscurity, its symptoms change amplification, high cognitive load and unknown unknowns.
- Reduced complexity is the primary success metric; a first working patch is not done if it worsens future changeability.
- Complexity is incremental; hold zero tolerance for each new "just this once" increment.
- Prefer deep modules: a small, simple interface over substantial hidden functionality; reject shallow pass-throughs, thin wrappers and tiny split-outs that add names without hiding anything.
- Design interfaces around what callers need, not how the implementation works; avoid mode flags, setup sequences and knobs that expose internals.
- Information hiding: encapsulate volatile decisions — representation, storage shape, protocol, format, performance hacks — inside the module that owns them.
- Pull complexity downward: better the implementer suffers once than every caller forever; prefer sensible defaults over configuration.
- Different layer, different abstraction; a pass-through method with the same signature in and out signals a wrong boundary.
- Define errors out of existence: shape APIs so the error case cannot arise instead of making every caller handle it; handle the residue at a boundary.
- Design it twice: sketch two alternatives before committing to a non-trivial interface or decomposition.
- Beware temporal decomposition: organize around stable responsibilities, not execution order (prepare/process/finalize).
- Names, consistency and obviousness are design information; surprising code is complexity even when short.
- Comments reduce complexity by recording contracts, invariants and rationale callers should not reconstruct; never to compensate for bad names or flow.
- Add patterns, frameworks or optimizations only when they reduce complexity here or measurement proves the tradeoff; hide optimization behind a stable interface.
- The end state is obvious code: a reader's first guess about what it does is right.
