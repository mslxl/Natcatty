# Layered package boundaries with consumer-owned ports

We organize the rewrite as `domain/<context>` (pure entities and rules),
`application/<context>` (use cases; declares the narrow interfaces it needs),
`adapters/<technology>` (implements those interfaces), `ui/<area>` (MyGo views),
`platform/<os>` (OS primitives), and `app` (composition root). Ports are
consumer-owned rather than centralized because a single `ports` package tends to
become a god-interface, and per-context interfaces keep dependency direction
explicit; a `go list`-based architecture test enforces that `domain` and
`application` never import MyGo, adapters, or UI. This buys testability and keeps
pre-1.0 MyGo churn at the edge, at the cost of slightly more interface declarations.

**Status**: accepted

**Considered Options**:
- A single `internal/ports` package: easier to browse, but grows into an
  all-purpose interface set and blurs which context owns each contract.
- Interfaces declared in adapters: fewer files, but creates reverse dependencies.

**Consequences**:
- Adapters import `application` for the interfaces they implement, so the
  dependency arrow points inward and never back out.
- `internal/platform` uses build tags for OS-specific primitives; adapters may
  depend on it, while `domain` and `application` may not.
- `cmd/natcatty-mcp` and `internal/mcp` form a separate process boundary and must
  not import `application`; they communicate through `internal/adapters/mcprpc`.
