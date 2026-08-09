# GQL engine simplification strategy digest

Five serial experiments were kept. Production Go source fell from 22,190 to
22,137 lines without changing exported API documentation, tests, dependencies,
or allocation budgets.

## Successful approaches

- Removed private wrappers only after production and same-package call-site checks.
- Replaced hand-written slice deletion, concatenation, and map cloning with Go standard-library algorithms.
- Centralized repeated scalar decisions only when the helper is small enough to inline.
- Reused the existing policy operation lookup instead of retaining a second authorization scan.

## Guardrails that mattered

- Observable error text changes were rejected even when Staticcheck preferred them.
- Compiler and Gateway allocations remained fixed at 22/51 and 75 respectively.
- Full package tests, Race, Vet, API surface hashing, and consumer integration were checked independently of line count.
- No tests, dependencies, generated artifacts, or public compatibility paths were changed to improve the metric.

## Final frontier

Further meaningful reduction requires a new measured run rather than extending
this one: the remaining large files contain behavior-heavy compiler, gateway,
policy, and schema code where broader consolidation needs dedicated conformance
fixtures and per-operation benchmarks.
