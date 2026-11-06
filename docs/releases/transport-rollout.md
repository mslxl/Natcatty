# Transport rollout: beta, default switch, and bundled retention

Status: **The default switch is implemented. The release default is
`in_process`.** The flip landed after the parity gates owned by tickets 27 (ET)
and 32 (mosh) and the packaged smoke tests reported green in CI; the run-bound
evidence is recorded under [Flip evidence](#flip-evidence). This document
defines the switch, the migration, the bundled retention window, and the
per-platform waiver.

## The one switch

The release default lives in exactly one place:

`domain/model/transport/policy.go` — `transport.ReleasePolicy`.

```go
var ReleasePolicy = Policy{
	Default:       InProcess,
	VerifiedGates: []Gate{GateMoshParity, GateETParity},
}
```

`transport.CurrentDefault()` resolves that policy for the running platform and
is consumed by:

- `domain/preferences.Defaults()` (new installs and resets),
- `adapters/store.PreferencesStore.Load` (every persisted document without an
  explicit choice, including old documents and expired waivers),
- `transport.Resolve` (a Session whose global value is absent).

Changing the default in that one declaration changes new installs and every
install that never made an explicit choice. No settings-file migration, Host
edit, or re-save is involved.

## Preconditions for the default flip

Do not change `ReleasePolicy.Default` to `InProcess` until all of the
following are recorded in the same change:

1. **Mosh parity gate (ticket 32) is green.** Link the CI run or published
   parity summary in the PR.
2. **ET parity gate (ticket 27) is green.** Link the CI run or published
   parity summary in the PR.
3. `ReleasePolicy.VerifiedGates` lists `GateMoshParity` and `GateETParity`.
   `Policy.Validate` fails with `ErrGateNotVerified` when either is missing,
   and `go test ./domain/model/transport -run TestReleasePolicyIsValid` fails
   the build. There is no waiver for a missing global gate.
4. The rollback runbook
   ([transport-rollback.md](transport-rollback.md)) is current and the release
   owner has signed its decision record.

## Flip evidence

The default flip was recorded after these runs reported green on `main` at
commit `5784818d9243e8f8b5c7b2512a3fa0a55147b717`, the parent of the flip:

| Check | Run | Result |
| --- | --- | --- |
| Mosh parity (ticket 32) | [run 42273516811](https://github.com/mslxl/Natcatty/actions/runs/42273516811) | green, all 10 jobs |
| ET parity (ticket 27) | [run 42273340203](https://github.com/mslxl/Natcatty/actions/runs/42273340203) | green, all 12 jobs |
| Packaged smoke (macOS, Linux, Windows arm64) | [run 42273340294](https://github.com/mslxl/Natcatty/actions/runs/42273340294) | green, all 6 jobs |
| CI | [run 42273340204](https://github.com/mslxl/Natcatty/actions/runs/42273340204) | green, all 7 jobs |

The mosh-parity push trigger is path-filtered, so that run was dispatched on
`main` at the same commit instead of being triggered by the push. The Windows
arm64 packaged smoke is launch-only, as documented in
[packaged-smoke.md](../ci/packaged-smoke.md); session mode for Windows arm64
remains the ticket-35 gap.

## Explicit choice vs. release default

`Transport.ImplementationExplicit` records provenance. The Settings UI exposes
three global choices:

| UI choice | Stored `implementation` | `implementationExplicit` |
| --- | --- | --- |
| Release default (recommended) | the effective default | `false` |
| Bundled client | `bundled` | `true` |
| In-process client (beta) | `in_process` | `true` |

A per-Host override (`Host.TransportImplementation`) is always explicit when
non-empty; empty inherits the resolved global value. The choice is device-local
and is not part of cloud-sync `PortableSettings`, because the release default
and waivers can differ per platform.

### Migration cases

The settings store resolves the document on every load. The table-driven test
is `TestTransportDefaultSwitchMigratesEachStoredCase` in
`adapters/store/settings_files_test.go`.

| Stored state | Result after the flip |
| --- | --- |
| No settings file (new install) | configured default (`in_process` after the flip) |
| Version 1 document without `transport.implementation` | configured default |
| Version 1 document with the old default `bundled` | configured default; it cannot prove the user chose it |
| Version 1 document with `in_process` | `in_process`, marked explicit |
| Version 2 document with `implementationExplicit: true` | the stored value, including `bundled` |
| Version 2 document with `implementationExplicit: false` | configured default |
| Unknown value from another build in a version 1 or explicit version 2 document | kept verbatim, marked explicit; the Session factory fails closed |
| Unknown value in a version 2 document with `implementationExplicit: false` | configured default; the document records a non-choice, so the value is replaced |
| Old Host record without `transportImplementation` | inherits the resolved global value; no re-save |

Version 2 of the preferences file always writes `implementationExplicit`, so a
non-choice stays a non-choice across later releases. Version 1 documents have
no provenance bit: a stored non-default value could only have come from an
explicit opt-in, while a stored `bundled` could be either an explicit choice or
a default-written value. The migration treats that ambiguous case as never
chosen. **One-time cost:** a version 1 user who explicitly selected `bundled`
while it was the default is moved to the new default and may need to select
Bundled once more. This is documented rather than guessed because the old
document contains no evidence either way.

An unknown value is never rewritten when the document proves it was a choice:
a version 1 document, or a version 2 document with
`implementationExplicit: true`. It stays in the document and the Session
factory reports `transport.ErrUnknownImplementation`, so a newer build's choice
is not silently replaced. A version 2 document with
`implementationExplicit: false` records a non-choice, so it follows the
configured default even when its stored value is unknown. The global and
per-Host controls both offer a one-click repair (Release default / Bundled /
Use global default).

## Bundled retention: one full release cycle

"One full release cycle" means:

- the **default-switch release** (release `N`, the first stable release whose
  `ReleasePolicy.Default` is `in_process`), and
- the **entire following stable release line** `N+1`.

The bundled clients stay selectable per Host in both `N` and `N+1`. Removal is
eligible only for release `N+2`, and only after the rollback runbook has been
retired or updated and the release owner records that decision. Pre-releases,
betas, and nightly builds do not shorten the window; `N+1` must be a stable
release users can hold as a fallback.

Until ticket 35 removes them, do not delete or change:

- bundled mosh/ET clients under `resources/`,
- `internal/mosh/NOTICE`, the et-go Apache-2.0 `LICENSE`/`NOTICE`, bundled
  client notices,
- `THIRD-PARTY-NOTICES.md`, root `LICENSE`/`NOTICE`, or `SOURCE-OFFER.md`,
- the fetch/packaging tooling.

The retention tests are:

- `TestBundledStaysSelectableAfterTheDefaultSwitch` in
  `app/session_factory_implementation_test.go` — with the global choice at
  `in_process`, a Host override of `bundled` still opens the bundled client,
  and a Host without an override inherits the switched default.
- `TestTransportDefaultSwitchSettingsFileRoundTrip` in
  `adapters/store/settings_files_test.go` — an explicit bundled choice
  survives the flip; a recorded non-choice does not.

## Per-platform waiver

A platform with a specific blocker may stay on the bundled client for one
extra cycle. This is not a parity waiver: ADR-0015 and ADR-0016 still require
full matrix evidence for a gate to be green, and `Policy.Validate` still
requires both global parity summaries before any default flip. The waiver only
delays the default adoption on one platform. Add a `transport.Waiver` to
`ReleasePolicy.Waivers`:

```go
Waivers: []transport.Waiver{{
	Platform: transport.Platform{OS: "linux", Arch: "amd64"},
	Reason:   transport.WaiverParityBlocker,
	Gate:     transport.GateETParity,
	Detail:   "ET parity run <link> still fails on linux/amd64",
	Expires:  time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC),
}},
```

Rules enforced by `Policy.Validate` and its tests:

- The platform is an exact `GOOS/GOARCH` pair, each side a lower-case ASCII
  letter/digit identifier; whitespace and punctuation are rejected. Other
  platforms are unaffected.
- `Reason` is one of `parity-blocker`, `packaging-blocker`,
  `platform-unsupported`.
- `Gate` names the gate that is not green (`mosh-parity`, `et-parity`, or
  `packaging`); the detail must identify the specific blocker and evidence.
  `VerifiedGates` accepts only those three values, without duplicates.
- `Expires` is required. The expiry is exclusive: on and after that instant the
  waiver stops applying and the platform adopts the release default without a
  data migration.
- Waivers are rejected while the default is `bundled` (they would do nothing),
  and duplicate platforms are rejected so resolution never depends on table
  order.
- A waiver only changes the platform default. An explicit per-Host `bundled`
  choice still wins, and an explicit global beta opt-in is still honored.

The tests are `TestPolicyKeepsABlockedPlatformOnBundled`,
`TestWaiversAreIgnoredWhileTheDefaultIsBundled`,
`TestPolicyRejectsIncompleteWaivers`, and
`TestTransportWaiverExpiresWithoutADataMigration`.

## Release-owner checklist

1. Both parity summaries green; links recorded in the PR and in
   `VerifiedGates`.
2. Run `go test ./domain/model/transport ./adapters/store ./app -count=1`.
3. Flip `ReleasePolicy.Default` and record both parity gates in
   `VerifiedGates`; keep `Bundled` selectable in the UI. The Settings
   effective-default line and beta hint read the policy at runtime.
   Default-behavior tests compare against `CurrentDefault()`, and tests that
   exercise the bundled client's own behavior select it explicitly through
   `ChooseImplementation`, so the production switch needs no further catalog
   or test edits. Run `env -u PS1 go test ./... -count=1` with the flip
   applied before committing it.
4. Add waivers, if any, with reason, gate, detail, and expiry.
5. Verify a fresh profile, an updated version 1 profile, and a Host with no
   override; see the migration table above.
6. Record the decision in
   [transport-rollback.md](transport-rollback.md#decision-record).
7. Keep the bundled clients, notices, and rollback path for `N+1` as defined
   above.
