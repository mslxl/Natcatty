# Transport rollout: in-process clients

Status: **Complete and closed.** Every Mosh and EternalTerminal Session uses its
in-process client. The bundled-vs-in-process choice, the release default, the
settings migration and provenance machinery, the bundled ET retention window,
and the per-platform waivers were all removed together with the bundled ET
client. Stored `transportImplementation` values from older builds are ignored
by `encoding/json`; no settings-file migration, Host edit, or re-save is
required.

## Flip evidence

The default switch was recorded after these runs reported green on `main` at
commit `5784818d9243e8f8b5c7b2512a3fa0a55147b717`, the parent of the flip:

| Check | Run | Result |
| --- | --- | --- |
| Mosh parity (ticket 32) | [run 42273516811](https://github.com/mslxl/Natcatty/actions/runs/42273516811) | green, all 10 jobs |
| ET parity (ticket 27) | [run 42273340203](https://github.com/mslxl/Natcatty/actions/runs/42273340203) | green, all 12 jobs |
| Packaged artifact tests (macOS, Linux, Windows arm64) | [run 42273340294](https://github.com/mslxl/Natcatty/actions/runs/42273340294) | green, all 6 jobs |
| CI | [run 42273340204](https://github.com/mslxl/Natcatty/actions/runs/42273340204) | green, all 7 jobs |

The mosh-parity push trigger is path-filtered, so that run was dispatched on
`main` at the same commit instead of being triggered by the push. The Windows
arm64 packaged artifact test was launch-only; session mode for Windows arm64
remained the ticket-35 gap.

## Bundled ET retention: closed

The former retention window kept the bundled ET client selectable per Host for
one full release cycle after the default switch. That window and its
preconditions are gone: the bundled client under `resources/et/`, the
`internal/tools/fetchtransportbins` tooling, `adapters/bundle`, and the
`domain/model/transport` selection policy were removed on 2026-11-07. No
release ships a bundled transport client, and no UI offers a client choice.

The retention tests named by earlier revisions
(`TestETHostOverrideSelectsTheBundledImplementation`,
`TestTransportDefaultSwitchSettingsFileRoundTrip`) no longer exist; the
replacement guarantees are the fail-closed factory tests in
`app/session_factory_implementation_test.go`.

## Rollback

There is no per-Host, global, or per-platform transport switch left to flip. A
transport regression is handled by rolling back the app release itself; see
[transport-rollback.md](transport-rollback.md#decision-record).
