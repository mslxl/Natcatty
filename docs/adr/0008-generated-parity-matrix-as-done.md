# The generated parity matrix is the definition of done

Functional parity is tracked in `docs/parity/matrix.yaml`, with a committed generated
`matrix.md`, and a rewrite is complete only when every in-scope row links original code
at `73d15da7` and at least one passing automated test (or a signed, enumerated manual-QA
exception). This makes "原功能" measurable and prevents features from silently
disappearing; the cost is ongoing maintenance of the matrix and its lint rules.

**Status**: accepted

**Consequences**:
- A CI lint rejects in-scope rows without original paths or resolvable tests.
- Accepted gaps must be signed in `docs/parity/limitations.md`.
- The same row IDs anchor MCP conformance and manual QA checklists.
