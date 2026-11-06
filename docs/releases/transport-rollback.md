# Transport rollback runbook

This runbook covers the in-process mosh and EternalTerminal clients. It is
used while the release default is `in_process` and the bundled clients remain
shipped (release `N` and `N+1`; see
[transport-rollout.md](transport-rollout.md#bundled-retention-one-full-release-cycle)).

The bundled client is always one per-Host switch away. No rollback requires
editing a database, rewriting a settings document, or removing the bundled
assets.

## Triggers

Open the rollback decision when any of these is observed on a supported
server/platform:

1. **Connection failure:** a Host that opened with the bundled client fails to
   establish or authenticate with the in-process client against stock
   `mosh-server` 1.3.2/1.4.0 or `etserver` 6.2.10/7.1.1. A single reproducible
   class is enough; it does not have to affect every server version.
2. **Terminal corruption:** corrupted display, lost input, wrong resize, or
   incorrect exit status in a way that changes command results.
3. **Resume/roaming regression:** mosh roaming or ET reset/`--attach` loses a
   Session where the bundled client preserved it, or reattaches with a wrong
   state.
4. **Crash or hang:** the app exits, panics, or wedges when opening, resizing,
   or closing a Mosh/ET Session through the in-process client.
5. **Credential/route regression:** host-key verification, jump chains, proxies,
   or authentication behave differently from the bundled client in a way that
   weakens or breaks the configured route.
6. **Packaging failure:** a platform's beta artifacts are missing required
   notices or fail the packaged smoke test. (The packaged smoke matrix covers
   macOS, Linux, and Windows arm64; Windows arm64 session mode remains a
   documented gap until ticket 35.)

Do not wait for a second platform to fail. A platform-specific blocker uses a
waiver (see the rollout document) rather than holding every platform.

## Per-Host switch (first response)

For an individual affected Host:

1. Open the Vault, edit the Host, and find **Transport implementation**.
2. Select **Bundled client**. This writes an explicit per-Host override; it is
   preserved across app restarts and is not a temporary toggle.
3. Save and reconnect. If the Host connects, the in-process client is the
   trigger; record the Host, protocol, server version, and app build in the
   rollback decision record.
4. Leave **Use global default** selected for unaffected Hosts. The rollback is
   per Host by design.

For every Mosh/ET Host on one machine, use **Settings → Transport →
Transport implementation → Bundled client**. This is an explicit global choice
and survives later release-default changes.

## Per-platform waiver (one extra cycle)

When the blocker is confined to one platform and the release default has
already flipped:

1. Add a `transport.Waiver` to `ReleasePolicy.Waivers` with the exact
   `GOOS/GOARCH`, a typed reason, the failing gate, a detail with the evidence,
   and an expiry at the end of the extra cycle.
2. Run `go test ./domain/model/transport ./adapters/store -count=1`.
3. Ship the waiver in a patch release on that platform. The global default is
   unchanged; other platforms keep the in-process client.
4. At expiry the waiver stops applying automatically. Extending it requires a
   new, explicitly recorded decision.

Do not add a waiver that silently ignores a failed global gate. A missing
global parity summary fails `Policy.Validate`; a waiver only pins one
platform.

## Previous-app-release rollback path

When the app release itself must be rolled back (not just one Host):

1. **Stop the rollout.** Do not publish another stable release with
   `ReleasePolicy.Default = InProcess` until the decision record is resolved.
2. **Publish or point users at the previous stable release** `N-1` (or the last
   stable release known to be good). Installers and checksums are immutable
   release assets; never retag or rewrite a published release.
3. **Installers retain user data.** The Vault database, keys, known hosts, and
   preferences live outside the app bundle. A downgrade keeps them.
4. **Settings compatibility.** Version 2 preferences add
   `transport.implementationExplicit`; older builds ignore the unknown field
   and read `transport.implementation`. No downgrade migration is required.
   After the flip, `implementation` can hold `in_process` for two kinds of
   users: one who chose the beta explicitly
   (`implementationExplicit: true`), and one who never chose but saved settings
   after the flip — `PreferencesStore.Save` writes the resolved default into
   `implementation` while keeping `implementationExplicit: false`. An older
   build ignores the flag in both cases, keeps `in_process`, and uses whichever
   in-process client it contains. Because the downgrade can affect explicit
   and non-explicit users alike, do not rely on the provenance flag: tell
   affected users to choose **Bundled client** before downgrading to a
   pre-beta build, or reset the Transport group after downgrading.
5. **Host records are forward-compatible.** Older builds ignore
   `transportImplementation`; an explicitly bundled Host keeps working because
   `bundled` is a value every build recognizes.
6. **Record the release rollback** in the decision record below, including the
   release tag, platforms, trigger, and the follow-up work.

The previous release remains the supported fallback. Do not delete its
installers, notices, or source offer.

## Decision record

Append one record per decision (flip, waiver, per-Host rollback campaign, or
app-release rollback). Keep it factual and link evidence; this is an
engineering record, not a legal certification.

```markdown
### <YYYY-MM-DD> — <release tag> — <decision>

- Decision: <flip default | hold | per-Host rollback | platform waiver | release rollback>
- Platforms affected: <goos/goarch list>
- Trigger: <one of the triggers above; link the failure or run>
- Evidence: <CI run, parity summary, issue, or crash report>
- Gates: mosh-parity <green|not green, link>; et-parity <green|not green, link>
- Scope: <Hosts/users/platforms>
- Owner: <release owner>
- Follow-up: <fix, waiver expiry, ticket>
- Legal/compliance note: <none unless a notice or source-offer change is involved>
```

### 2026-11-07 — default-switch build (0.1.0-beta.1) — flip default

- Decision: flip default
- Platforms affected: all supported platforms. New installs and installs with
  no explicit transport choice adopt `in_process`; explicit global and per-Host
  `bundled` choices keep their stored value. No platform waiver was needed.
- Trigger: scheduled flip, not a rollback trigger. Both parity gates and the
  packaged smoke jobs were green on the evidence commit.
- Evidence: commit `5784818d9243e8f8b5c7b2512a3fa0a55147b717` on `main`:
  [mosh-parity 42273516811](https://github.com/mslxl/Natcatty/actions/runs/42273516811),
  [et-parity 42273340203](https://github.com/mslxl/Natcatty/actions/runs/42273340203),
  [packaged-smoke 42273340294](https://github.com/mslxl/Natcatty/actions/runs/42273340294),
  [ci 42273340204](https://github.com/mslxl/Natcatty/actions/runs/42273340204).
- Gates: mosh-parity green
  ([run 42273516811](https://github.com/mslxl/Natcatty/actions/runs/42273516811));
  et-parity green
  ([run 42273340203](https://github.com/mslxl/Natcatty/actions/runs/42273340203)).
- Scope: new installs and non-explicit settings on every platform; per-Host
  overrides and explicit bundled choices are unchanged.
- Owner: release owner.
- Follow-up: ticket 35's bundled-client removal stays blocked until release
  N+2 per the retention window in
  [transport-rollout.md](transport-rollout.md#bundled-retention-one-full-release-cycle).
- Legal/compliance note: none; no notice, license, or source-offer file
  changed.

## Risk acceptance

On 2026-11-06 the release owner recorded a risk acceptance for the remaining
human/legal gates in tickets 15 (mosh provenance audit and relicensing) and 19
(host GPL compliance remediation), and marked both `wontfix`. The compliance
artifacts remain in every artifact and are not removed: GPL-3.0-or-later
`LICENSE`/`NOTICE`, `THIRD-PARTY-NOTICES.md`, `internal/mosh/NOTICE` (stock mosh
port plus mosh-go MIT), et-go Apache-2.0 `LICENSE`/`NOTICE`, bundled-client
notices, and `SOURCE-OFFER.md`.

That acceptance is a release-owner risk decision, not a legal certification.
It does not authorize removing notices or bundled assets. Ticket 35's removal
of the bundled clients stays out of scope until the retention window below has
elapsed.
