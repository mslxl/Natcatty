# Roll out the in-process Mosh/ET clients behind an explicit beta and a gated default switch

The application-owned mosh and ET clients (ADRs 0015 and 0016) are selectable
globally and per Host. At the time this ADR was accepted the release default
was still the bundled client; this ADR records how the beta becomes the
default: an explicit provenance bit
separates a user's durable choice from a value that only reflects the old
default; one `transport.ReleasePolicy` declaration is the release switch; the
flip requires both parity gate summaries (tickets 27 and 32) to be green; the
bundled clients remain selectable per Host for one full release cycle; and a
platform-specific blocker can be waived for one extra cycle with a typed
reason, gate, evidence, and expiry. A rollback runbook records the triggers,
the per-Host switch, the previous-app-release path, and the release-owner
decision.

**Status**: accepted and implemented, then superseded by removal. The default
flip landed after the 27/32 gates and the packaged artifact test jobs passed;
the run-bound evidence is recorded in
[transport-rollout.md](../releases/transport-rollout.md#flip-evidence). The
bundled Mosh client was subsequently removed (ADR-0015). On 2026-11-07 the
bundled ET client and the entire selection feature described here were removed:
the `domain/model/transport` policy, the settings provenance and migration
machinery, the per-Host override, the client-choice UI, the retention window,
the waivers, and the bundled assets no longer exist. Both clients are
in-process-only, old `transportImplementation` values are ignored on load, and
no rollback switch remains.

**Amends ADR-0015 and ADR-0016**: both say "No waivers" for the parity release
gate. That rule remains in force for the parity claim itself: a gate is green
only with full matrix evidence, and no waiver declares the in-process client
green. The per-platform waiver here is narrower — it keeps one platform on the
bundled client for one extra cycle and never relaxes `Policy.Validate`'s
requirement that both global parity summaries are recorded before any default
flip.

**Considered Options**:
- Keep the default hardcoded to bundled and flip it in a later rewrite: avoids
  provenance, but leaves no way to distinguish an explicit bundled choice from
  a default-written one, so the flip would either strand every existing user on
  bundled or override users who chose it.
- Store a separate explicit-selection field instead of a provenance bit: works,
  but duplicates the implementation value and creates two sources of truth in
  the UI and import/export.
- Provenance bit plus a per-load resolver (chosen): one persisted value stays
  authoritative, the flag says whether it was chosen, and the store recomputes
  non-explicit values through the release policy on every load. A default flip
  and a waiver expiry then need no data migration and no Host re-save.
- A per-platform environment override of the default: rejected because it can
  diverge silently between packaging and runtime and is not testable in the
  repository.
- A per-platform waiver table validated as release data (chosen): exact
  `GOOS/GOARCH`, typed reason, the failing gate, a detail naming the evidence,
  and a mandatory expiry. It never relaxes the global gate requirement and
  never overrides an explicit per-Host or global choice.

**Consequences**:
- `Transport.ImplementationExplicit` is always written by the settings store
  (preferences schema version 2). Version 1 documents are migrated
  field-by-field on load. A version 1 document whose value equals the old
  default is treated as never chosen; this is the only information the old
  document contains, and the one-time cost is documented in
  `docs/releases/transport-rollout.md`.
- New installs, updated installs that never chose, and expired waivers all
  follow `transport.ReleasePolicy` without a settings or Host migration.
- An explicit `bundled` choice survives every later default flip. An unknown
  value is preserved verbatim and the Session factory keeps failing closed with
  `transport.ErrUnknownImplementation`.
- The in-process client is a first-class explicit choice globally and per Host
  for EternalTerminal, and the stale "not available yet" strings are gone.
  Mosh has one implementation and shows no choice.
- `transportImplementation` round-trips through Netcatty JSON export/import;
  the importer also accepts the `mosh` and `et` protocol values those Hosts
  use. Unknown values survive import for the factory to reject.
- The bundled ET client remained the documented rollback path for one full
  release cycle: release `N` (the default-switch release) plus the stable
  release line `N+1`. That retention ended early: the bundled client, its
  notices, assets, manifests, fetch tooling, and the selection machinery were
  removed on 2026-11-07, so no release carries a bundled transport client. The
  bundled Mosh client had been removed earlier; every Mosh Session uses the
  in-process client.
- The release owner's 2026-11-06 risk acceptance for tickets 15 and 19 is
  referenced by the rollback runbook. It is not a legal certification and does
  not change the notice or source-offer requirements.
- The CI gate wiring, the packaged artifact tests for macOS, Linux, and Windows
  arm64, and the default flip after 27/32 became green have landed; the
  run-bound evidence is recorded in
  [transport-rollout.md](../releases/transport-rollout.md#flip-evidence).
