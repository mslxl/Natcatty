# MyGo ARB/ICU flat-key catalogs with generated accessors

User-visible strings live in MyGo ARB-style JSON catalogs for the original app's five
locales (en, es, ru, zh-CN, zh-TW), use flat keys and ICU messages, and are consumed
through committed generated Go accessors; domain and application code contain no display
strings. This gives compile-time-checkable keys and correct pluralization; the cost is a
catalog-generation step and a custom CI check because MyGo treats missing translations
as warnings.

**Status**: accepted

**Consequences**:
- `go tool mygo generate` must run after catalog changes; CI verifies no diff.
- A Go test enforces identical key sets and matching argument names/kinds across locales.
- MCP uses stable English protocol codes rather than localized display strings.
