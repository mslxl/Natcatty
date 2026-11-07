# Transport rollback runbook (retired)

Status: **Retired.** Every Mosh and EternalTerminal Session uses its in-process
client. No bundled client, per-Host override, release default, or per-platform
waiver exists, so there is no transport switch to roll back. This document is
kept as a historical decision record; the previous revision described the
per-Host and per-platform rollback paths that were removed with the bundled ET
client.

## Previous-app-release rollback path

When a transport regression in an app release must be undone:

1. **Publish or point users at the previous stable release** (or the last
   stable release known to be good). Installers and checksums are immutable
   release assets; never retag or rewrite a published release.
2. **Installers retain user data.** The Vault database, keys, known hosts, and
   preferences live outside the app bundle, so a downgrade keeps them.
3. **Settings and Host records are forward-compatible.** Fields this build no
   longer writes, such as `transportImplementation`, are ignored by
   `encoding/json`; older builds that still know the bundled client keep
   working from their own stored values.
4. **Record the release rollback** below with the release tag, platforms,
   trigger, and follow-up work.

Do not delete the fallback release's installers, notices, or source offer.

## Decision record

Append one record per decision. Keep it factual and link evidence; this is an
engineering record, not a legal certification.

```markdown
### <YYYY-MM-DD> — <release tag> — <decision>

- Decision: <flip default | hold | release rollback | bundled-client removal>
- Platforms affected: <goos/goarch list>
- Trigger: <link the failure, run, or owner decision>
- Evidence: <CI run, parity summary, issue, or crash report>
- Gates: mosh-parity <green|not green, link>; et-parity <green|not green, link>
- Scope: <Hosts/users/platforms>
- Owner: <release owner>
- Follow-up: <fix, ticket>
- Legal/compliance note: <none unless a notice or source-offer change is involved>
```

### 2026-11-07 — default-switch build (0.1.0-beta.1) — flip default

- Decision: flip default
- Platforms affected: all supported platforms. New installs and installs with
  no explicit transport choice adopted `in_process`; explicit global and
  per-Host `bundled` choices kept their stored value. No platform waiver was
  needed.
- Trigger: scheduled flip, not a rollback trigger. Both parity gates and the
  packaged artifact test jobs were green on the evidence commit.
- Evidence: commit `5784818d9243e8f8b5c7b2512a3fa0a55147b717` on `main`:
  [mosh-parity 42273516811](https://github.com/mslxl/Natcatty/actions/runs/42273516811),
  [et-parity 42273340203](https://github.com/mslxl/Natcatty/actions/runs/42273340203),
  [artifact-tests 42273340294](https://github.com/mslxl/Natcatty/actions/runs/42273340294),
  [ci 42273340204](https://github.com/mslxl/Natcatty/actions/runs/42273340204).
- Gates: mosh-parity green
  ([run 42273516811](https://github.com/mslxl/Natcatty/actions/runs/42273516811));
  et-parity green
  ([run 42273340203](https://github.com/mslxl/Natcatty/actions/runs/42273340203)).
- Scope: new installs and non-explicit settings on every platform.
- Owner: release owner.
- Follow-up: the bundled Mosh client was removed on 2026-11-07; Mosh has no
  per-Host rollback.
- Legal/compliance note: none; no notice, license, or source-offer file
  changed.

### 2026-11-07 — all releases — bundled ET client removed

- Decision: bundled-client removal
- Platforms affected: all supported platforms.
- Trigger: owner decision to ship in-process clients only.
- Evidence: removal of `resources/et/`, `internal/tools/fetchtransportbins`,
  `adapters/bundle`, the `domain/model/transport` selection policy, and every
  client-choice UI and settings field. The in-process ET client and its parity
  lab remain.
- Gates: mosh-parity and et-parity remain green (runs linked above).
- Scope: every Host and platform. No per-Host, global, or per-platform
  transport switch remains.
- Owner: release owner.
- Follow-up: none.
- Legal/compliance note: the bundled Netcatty-et-bin source entry was removed
  from the source offer and third-party notices; the vendored et-go Apache-2.0
  attribution remains.

## Risk acceptance

On 2026-11-06 the release owner recorded a risk acceptance for the remaining
human/legal gates in tickets 15 (mosh provenance audit and relicensing) and 19
(host GPL compliance remediation), and marked both `wontfix`. The compliance
artifacts remain in every artifact and are not removed: GPL-3.0-or-later
`LICENSE`/`NOTICE`, `THIRD-PARTY-NOTICES.md`, `internal/mosh/NOTICE` (stock mosh
port plus mosh-go MIT), et-go Apache-2.0 `LICENSE`/`NOTICE`, and
`SOURCE-OFFER.md`.

That acceptance is a release-owner risk decision, not a legal certification.
It does not authorize removing notices or in-process client sources.
