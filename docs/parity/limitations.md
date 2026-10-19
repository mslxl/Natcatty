# Parity limitations and exclusions

Every row in `docs/parity/matrix.yaml` with `status: excluded` has a section
below. Each section records the accepted gap or deliberate exclusion and the
decision that authorized it. `internal/tools/paritygen` fails when an excluded
row has no section, when a section has no `Decision:` line, or when a section
names a row that is missing or not excluded.

The original behavior is defined by commit `73d15da7`. The decision sources are
the native rewrite spec, the ADRs, and the research inventory listed in
`.scratch/rewrite/`.

## Out-of-scope product surfaces

### `plugins.runtime`

The original `.ncpkg` plugin runtime, plugin-provided views, menus, keybindings,
themes, terminal protocols, and sync providers are removed from the native
product.

Decision: excluded by the rewrite spec "Out of Scope" and ADR-0001; ticket 01.
Ticket: 01

### `ai.features`

The original AI assistant, providers, built-in agent, harness, context
compaction, AI settings, and `harness.*` MCP tools are removed. The MCP surface
in scope is the external tool server only.

Decision: excluded by the rewrite spec "Out of Scope"; ticket 01.
Ticket: 01

### `migration.electron-data`

User data is not migrated from the old Electron app. Importing from other
products (PuTTY, MobaXterm, FinalShell, SecureCRT, `ssh_config`) is a separate
in-scope capability and is not migration.

Decision: excluded by the rewrite spec "Out of Scope"; ticket 01.
Ticket: 01

### `cli.internal-tool`

The original `netcatty-tool-cli.cjs` is not reimplemented. The external MCP
server is the required programmatic surface.

Decision: excluded by the rewrite spec "Out of Scope"; ticket 60.
Ticket: 60

### `sync.smb-provider`

SMB was declared but not implemented in the original sync provider catalog.
Plugin providers and SMB are out of scope for the native sync implementation.

Decision: excluded by the rewrite spec "Out of Scope"; ticket 49.
Ticket: 49

### `settings.custom-css`

Custom CSS is removed from the native UI. The legacy custom-CSS setting is kept
as an inert, migration-safe value.

Decision: excluded by the rewrite spec "Out of Scope"; ticket 27.
Ticket: 27

### `release.signing-update`

Code signing, notarization, auto-update, and update metadata are not required.
Release builds are buildable installers only; macOS artifacts are ad hoc signed.

Decision: excluded by the rewrite spec "Out of Scope" and ADR-0005; ticket 60.
Ticket: 60

## Accepted MyGo v0.5.5 limitations

### `terminal.search`

MyGo v0.5.5 terminal widgets expose no content-search API, so terminal search
cannot be implemented through the supported surface. The UI keeps the
affordance disabled and labels the limitation.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 18.
Ticket: 18

### `terminal.selection-export`

MyGo v0.5.5 exposes no exported selection getter. Selection-driven features that
need the selected text are limited to what the native widget reports through its
public events.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 17.
Ticket: 17

### `terminal.cursor-geometry`

MyGo v0.5.5 exposes no cursor position or cell geometry. Overlays that need exact
cell coordinates cannot be implemented and are omitted.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 17.
Ticket: 17

### `terminal.inline-images`

MyGo v0.5.5 does not render inline terminal images. Sixel, Kitty graphics, and
iTerm2 inline-image protocols are parsed only as far as needed to keep the text
stream correct.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 18.
Ticket: 18

### `terminal.live-multiview`

MyGo v0.5.5 allows one live view of a terminal surface. Hibernation snapshots and
observer snapshots are implemented instead of a second live view.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 19.
Ticket: 19

### `terminal.accessibility-tree`

MyGo v0.5.5 exposes no accessibility tree for terminal cells. Native widgets
remain addressable, but terminal content is not exposed as an a11y tree.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 17.
Ticket: 17

### `terminal.write-receipts`

MyGo v0.5.5 exposes no write receipts for terminal input. Input ordering is
enforced app-side, but the UI cannot observe remote write completion.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 17.
Ticket: 17

### `terminal.kitty-key-encoding`

MyGo v0.5.5 cannot produce exact per-target Kitty keyboard key encodings. Common
Kitty keyboard modes still work through the widget's encoding.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 17.
Ticket: 17

### `terminal.process-isolation`

The original ran terminal work in a separate Electron Renderer process. The
native app has no Renderer, so terminal work runs in Go with bounded queues and
no process-isolation boundary.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 17.
Ticket: 17

### `applock.biometric`

MyGo v0.5.5 exposes no biometric API. The App Lock settings keep the biometric
flags for forward compatibility, but they are inert and the UI says so.

Decision: accepted for v1 by the rewrite spec "Out of Scope" and user story 57;
Ticket: 04
ticket 04.

### `browser.cookie-partitioning`

MyGo v0.5.5 has no per-webview session partition, so the host-routed browser
panel cannot guarantee per-host cookie isolation. Cookies remain local-only and
the limitation is documented in the browser panel.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 48.
Ticket: 48

## Protocol and platform limitations

### `connections.ssh-compression`

`golang.org/x/crypto/ssh` supports only `none` compression. A Host configured
with any other compression fails with a typed unsupported error rather than
silently downgrading.

Decision: accepted by the rewrite spec "Connections and SSH" and ADR-0002;
Ticket: 06
ticket 06.

### `connections.legacy-algorithms`

`hmac-md5`, `hmac-md5-96`, `hmac-sha2-256-96`, `hmac-ripemd160`, and
Diffie-Hellman group 15/17/18 are not implemented by `x/crypto/ssh` and cannot
be offered even in legacy mode. `aes256-cbc` is likewise absent. Legacy mode
does add `aes128-cbc`, `3des-cbc`, DH group14/1-sha1, group-exchange-sha1,
`ssh-rsa`, and `ssh-dss` through an explicit insecure opt-in per Host.

Decision: accepted by the rewrite spec "Connections and SSH" and ADR-0002;
per-Host legacy mode added by ticket 08.
Ticket: 06

### `platform.windows-arm64`

Bundled MoshCatty and `et` binaries are unavailable for `windows/arm64`, and the
rewrite does not fall back to system binaries. Windows arm64 is therefore not a
supported release target.

Decision: accepted by ADR-0005 and the rewrite spec "Long-tail protocols";
Ticket: 59
ticket 59.
