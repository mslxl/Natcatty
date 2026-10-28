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

The original design required each untrusted website to run in a sandboxed
WebContentsView with "an Electron session partition separate from the
application and separate for each host profile. Tabs for one host may share
website cookies; different hosts must not" (`docs/designs/host-routed-browser.md`).
MyGo v0.5.5 exposes one shared cookie store and no per-webview session
partition, so the native browser cannot enforce that boundary. Every Host's
gateway uses the same loopback host, 127.0.0.2 (or the documented 127.0.0.1
fallback on macOS, which assigns only 127.0.0.1), and cookies are
keyed by host and path rather than by port or remote origin. Browser panels
opened on different Hosts can therefore share or overwrite cookies, and a
favicon or download fetch can carry a cookie another Host's page set at the
same path.
Cookies remain local; the app edge reads the shared store only to attach that
Cookie header to favicon and download requests, and web content has no product
bridge for reading them.

Decision: accepted for v1 by the rewrite spec "Out of Scope"; ticket 48.
Ticket: 48

### `browser.error-page-localization`

Chromium's own blocked and load-error page is rendered inside the native web
view, not by the app. MyGo v0.5.5 creates its WebView2/WebKit views without a
UI-language option, an error-page override, or a hook that would let the app
replace that document, so its body text follows the browser engine's locale
and stays English under zh-CN. The app's own banner above it, its controls,
and its bookmark and download panes are localized. Hiding the native view from
a page failure would also hide the page still on screen after a stopped or
download-aborted navigation, which is why the app keeps the engine page rather
than forcing an app-rendered replacement.

Decision: accepted for v1 as an engine-owned surface; ticket 48.
Ticket: 48

### `settings.mcp-mode-popup-accessibility`

MyGo v0.5.5's `ui.Select` exposes the selected value and the ExpandCollapse
state through UIA, and the popup is fully operable with the pointer and the
keyboard, but the option rows the native popup draws are absent from the
accessibility tree. A screen reader therefore cannot enumerate the three
External MCP permission modes from the open popup; the selected mode is still
announced, and the mode description and the last-activity strip state the
active mode in text. The original component used a native select whose options
were part of the browser accessibility tree. The product keeps the combobox as
its mode control and records the gap instead of claiming an accessible popup.

Decision: accepted as a MyGo v0.5.5 widget limitation; ticket 58 records the
gap rather than replacing the product's mode control.
Ticket: 58

## Threat-model boundaries

### `mcp.blocklist-threat-model`

The terminal command blocklist is a textual safety net over the commands an
external MCP client submits. It canonicalizes the shell's own in-model quoting,
continuations, `$IFS`, simple assignments, every bounded POSIX brace group,
any command piped into a shell (including through common wrappers such as
`sudo`, `env`, and `command`), `find -delete`, PowerShell constant folding and
same-command alias resolution, `%ComSpec%`/`$env:ComSpec`, and a nested
`cmd /c`, `/k`, `/r`, or glued `/c` payload; a command word that is still
dynamic after that reading fails closed. Arbitrary computed command construction is out of scope: a command word
decoded at run time by an interpreter from data the guard cannot see, an
external script, or a function built and invoked dynamically can still do what
the textual matcher cannot observe. Serial and network-device raw Sessions skip
the blocklist by design. The rewrite makes no OS-sandbox, capability-drop, or
process-isolation claim, and the in-model corpus above is what ticket 58
enforces and tests, including its disposable-marker execution proof.

Decision: accepted as a textual safety net; arbitrary computed command
construction is excluded while the in-model corpus is enforced; ticket 58.
Ticket: 58

## Provider verification limits

### `sync.google-drive-live-endpoints`

The Google Drive provider is verified against a local fake that mirrors the
authorize, token, userinfo, Drive v3, and upload endpoints, including PKCE
S256 validation, refresh rotation, invalid_grant, pagination, and per-account
appDataFolder isolation. Live Google endpoints are not contacted by tests or
the self-test because they require a provisioned Google Cloud OAuth client, a
human consent, and a real Drive quota.

Decision: excluded from automated acceptance by ticket 50; the provider's
defaults remain the original Electron production endpoints and the
NATCATTY_GDRIVE_* overrides exist only for the fake. A signed-in user with a
Google client id can exercise the live flow.
Ticket: 50

### `sync.onedrive-live-endpoints`

The OneDrive provider is verified against a local fake that mirrors Microsoft's
authorize, token, `/me`, and Graph approot metadata and content endpoints,
including PKCE S256 validation, refresh rotation, invalid_grant, eTag conflict
handling, and per-account approot isolation. Live Microsoft endpoints are not
contacted by tests or the self-test because they require a provisioned
Microsoft Entra app registration with the app-folder delegated permission, a
human consent, and a real OneDrive quota.

Decision: excluded from automated acceptance by ticket 51; the provider's
defaults remain the original Electron production endpoints and the
NATCATTY_ONEDRIVE_* overrides exist only for the fake. A signed-in user with a
Microsoft client id can exercise the live flow.
Ticket: 51

### `sync.webdav-live-endpoints`

The WebDAV provider is verified against an in-process fake that mirrors the
RFC 4918 methods and the RFC 7617/7616 authentication surface, including a real
Digest challenge with a stale nonce, conditional ETags, 401/403/404/412,
redirects, and self-signed TLS. It does not contact a public WebDAV service or
a vendor-specific endpoint (Nextcloud, ownCloud, SharePoint); those need a live
account and credentials, and their extensions are outside the generic adapter.

Decision: excluded from automated acceptance by ticket 52. A local real WebDAV
server would count as real, but this suite deliberately starts none, and the
fake is never described as a live service.
Ticket: 52

### `sync.s3-live-endpoints`

The S3 provider is verified against an in-process fake that independently
recomputes SigV4, enforces an optional session token, serves path-style
`<endpoint>/<bucket>/<key>` requests, rejects a wrong region with
`PermanentRedirect`, enforces `If-Match` and `If-None-Match`, and returns
403/404/412/5xx. It does not contact AWS S3, MinIO, Ceph, Cloudflare R2, or any
other commercial S3-compatible service; those need real credentials, a
provisioned bucket, and a network, which CI cannot supply.

Decision: excluded from automated acceptance by ticket 53. A local real
S3-compatible server would count as real, but this suite starts none, and the
fake is never described as AWS.
Ticket: 53

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

### `connections.fido-sk-hardware-validation`

No FIDO2/U2F hardware key is available on the build machine, so physical device
touch, PIN entry, and vendor middleware cannot be exercised. Ticket 42 verifies
the agent wire contract, the OpenSSH PROTOCOL.u2f signature framing, and the
30-second device deadline against an in-process fake agent, and its self-test
records the same evidence. Real-hardware behavior remains unverified and is not
claimed as parity.

Decision: accepted by ADR-0004 (security keys are signed by the system OpenSSH
agent, never software-signed); ticket 42.
Ticket: 42

### `connections.fido-sk-prompts-and-keygen`

The system OpenSSH agent owns the touch and PIN prompts, so the app neither
shows its own prompt nor controls PIN timing. The original owned its agent child
and askpass bridge, which let it implement retry/cancel/fallback and exclude PIN
entry time from the 30-second deadline; here the whole agent round-trip shares
one 30-second deadline. Security-key generation with `ssh-keygen` (resident and
verify-required flags) is likewise not implemented; users generate keys with
their OpenSSH installation.

Decision: accepted by ADR-0004, which delegates prompting to the system agent;
ticket 42.
Ticket: 42

### `connections.fido-sk-openssh-dependency`

Security-key authentication requires a system OpenSSH installation with an
agent. ADR-0004 deliberately avoids bundling OpenSSH and defers a pure-Go
CTAP2/HID stack, so a machine without the agent cannot use a hardware key. The
app reports this as a typed unsupported failure with localized guidance rather
than a raw transport error.

Decision: accepted by ADR-0004; ticket 42.
Ticket: 42
