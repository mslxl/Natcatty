# VNC is a first-class Host protocol

A Host may select VNC (`vnc`, port 5900) like SSH, Mosh, EternalTerminal,
Telnet, or a Local shell. VNC has its own descriptor in
`domain/model/protocol`, its own Session transport (`adapters/vnc`), and no
SSH handshake or credential bundle. Capability questions — whether a protocol
uses SSH auth, may serve as a jump hop, what port it defaults to, and whether
its transport is plaintext — are answered from the descriptor registry rather
than by comparing protocol names. A small set of behaviors remains
protocol-specific and does compare the `vnc` value: the viewer's controls and
defaults, the dedicated VNC password slot and its routing, and the editor's
VNC section.

A VNC Host keeps the route controls an SSH or Telnet Host has. Jump chains,
proxies, algorithm overrides, and a Network Profile describe how to reach the
endpoint. `adapters/vnc.Factory` receives the same three dial seams the other
transports use: a routed raw stream for a jump chain or proxy, a Provider-backed
dial for a Network Profile, and `net.Dialer` when the Host has no route. The
adapter validates the start section before dialing, so a malformed Host opens no
jump chain. A routed stream is already SSH-encrypted; the adapter is told that
with `Factory.Routed` and negotiates an inner credential type without layering
TLS inside the tunnel. A Host with only a Network Profile dials once through its
Provider. `Manager.DialStream` still returns a raw `net.Conn`; VNC, like Telnet,
has exactly one stream and is not pooled.

VNC is the one **pixel-surface exception**. A byte-stream Session renders a
terminal from `Transport`; a VNC Session has no `Transport` and instead carries
a `DisplaySession`. `DisplaySession` is the single new application seam. Its
receive direction is one typed event channel (frame, resize, bell, cut text,
cursor shape, desktop name, error); its send direction is goroutine-safe key,
pointer, clipboard, update-request, desktop-resize, and close methods serialized
by the adapter onto one writer. Frames cross the seam as image snapshots, never
as the adapter's live decode buffer, and are coalesced to a bounded rate. The
UI's `Surface` interface is unchanged: `ui/vnc.Surface` implements it and renders
the bitmap, so a VNC pane splits, detaches, renames, hibernates, and restores
like any other pane. Byte-only behavior — capture, codec, scrollback, command
history, compose, broadcast, and terminal resize — is skipped by `KindVNC`.

Credentials are separated. A VNC password lives in its own field-secret slot
(`vnc_password`, `FieldVNCPassword`); the Host record carries only the
non-secret `HasVNCPassword` marker. The VNC credential path — the editor,
Create/Update, the VNC Session start section, and import — never reads, writes,
or migrates the SSH password slot (`password`), even when a Host record carries
both markers. A legacy record that also advertises `HasPassword` still exports
that independent SSH credential in JSON; CSV leaves the SSH `Password` cell
empty for a VNC Host, so only `VNCPassword` carries a credential. Import
normalizes the VNC Host and writes no SSH password for it. A change from VNC to another
protocol clears only the VNC slot. A VNC username reuses the Host's ordinary
`Username`; only VeNCrypt Plain subtypes and Apple DH send it. Portable export
carries the marker on the Host and the plaintext in a dedicated `vncPassword`
field only when secrets are included; import writes it back to
`FieldVNCPassword`, never to `FieldPassword`. Sync treats the credential as an
ordinary field secret under `entity: host, field: vnc_password`, and the viewer
configuration as an ordinary Host JSON field. Both portable formats carry the
viewer configuration and the credential. JSON writes a `vnc` object and a
dedicated `vncPassword` field. CSV keeps the original fourteen columns and
appends `VNC` and `VNCPassword`: the VNC cell is a marker-prefixed list of
`key=value` pairs, and the credential is a raw cell written only when secrets
are included. An older CSV reader ignores the appended columns, and this
importer accepts both the old fourteen-column file and the extended one.

An unrouted VNC connection may run without transport encryption. The protocol
registry's **plaintext** capability drives the editor's warning, and the
adapter's post-negotiation `SecurityGate` asks once per Host before credentials
leave the client when the negotiated type is genuinely plaintext. The
acknowledgement is persisted per Host in `VNCConfig.PlaintextWarningAcknowledged`;
a routed Host suppresses the warning because the tunnel is the encryption
layer. Authentication preference is strongest-first: RA2ne, RA2-256, RA2,
VeNCrypt (X509 before TLS before Plain), Apple DH, the VNC password challenge,
then None. X509 verification uses the system trust store and the Host name with
no insecure-skip switch. A server offering only unimplemented types fails closed
with a typed error and the hint to add an SSH route; the client never downgrades
to None silently.

The endpoint is always stored as the real TCP port. The editor's display-number
input mode is presentation and validation only (display 0–99 maps to 5900–5999);
switching modes never changes the saved target.

**Status**: accepted (2026-11-22)

**Considered Options**:
- **Treat VNC as SSH with a graphical channel.** Rejected: VNC speaks RFB, not
  SSH, and RFB has no channels. Every SSH credential control, SFTP, forwarding,
  monitoring, X11, and Test action would be dead or misleading.
- **Reuse the terminal `Surface` by drawing pixels into a character grid.**
  Rejected: a framebuffer is not a byte stream. It would force scrollback,
  capture, compose, broadcast, and terminal resize onto data they cannot
  represent, and it would break crisp scaling.
- **Introduce a Pane content union.** Rejected: the existing `shell.Surface`
  interface already is the extension point. `ui/vnc.Surface` implements it; no
  Pane refactor was needed for the first landing.
- **Carry the VNC password in the SSH password slot.** Rejected: changing one
  credential would silently change the other, and the ticket requires them
  independent. A dedicated slot also keeps export, import, sync, and deletion
  on the same generic secret path as every other field credential.
- **Native TLS everywhere, including a routed Host.** Rejected: the SSH tunnel
  is already end-to-end encrypted, and no major VNC client double-encrypts a
  tunnel. Layering TLS adds a handshake and a second trust decision without
  adding protection.
- **Fall back to None when the server offers nothing implemented.** Rejected:
  silent downgrade is indistinguishable from a secure connection to the user.
  The adapter fails closed and points at the SSH route.

**Consequences**:
- A VNC Host may have a Host Chain, a Proxy Profile, and a Network Profile.
  Their authentication, known-host checks, and algorithm policies run the SSH
  path; the VNC target has no host key to trust.
- Host Groups may use VNC as their default protocol. VNC viewer preferences
  (fit, cursor, shared, view-only, acknowledgement) stay per Host; a group
  default carries only the protocol and port, matching the Telnet precedent.
- A VNC Host and an SSH Host on the same address remain distinct because Host
  identity includes the protocol.
- Byte-only connectors hide a VNC Host and reject it with a typed error if
  reached another way. The Vault Test action, which performs an SSH handshake,
  is hidden for VNC.
- Reconnect follows the SSH policy (five seconds, on error or unexpected close,
  never after an authentication failure) and offers a manual reconnect. One
  pane owns one stream; connection reuse and observer views remain out of
  scope.
- The capability matrix is covered by `domain/model/protocol` tests. Changing a
  capability is a product decision; tests fail if one flips silently.
- **Documented deviations.** RA2ne and RA2ne-256 authenticate but install no
  session encryption, so they are classified plaintext and an unrouted Host
  warns even though RA2 proper (which adds AES-EAX) does not. For a routed Host
  the preference order is deliberately different from the unrouted table: the
  non-VeNCrypt types come before VeNCrypt because selecting the VeNCrypt
  container commits the client before its subtype list is known, and a server
  that then offers only TLS subtypes leaves no acceptable choice; the only
  VeNCrypt subtype a routed Host accepts is Plain. Apple Diffie-Hellman is
  implemented, but it encrypts only the credential block with a DH-derived AES
  key; the session that follows is still plaintext, so it warns too, and its
  64-byte credential halves cap a username or password at 63 bytes. The viewer's
  keyboard fidelity is bounded by MyGo v0.7.4: it exposes no USB HID code and
  no Print Screen, Fn, modifier-side, numpad, or media identities, and its
  `Key` is reduced to a fixed US layout, so the physical ANSI-US position is the
  first resolution tier and an unmapped layout character falls back to that
  position. The resolver already understands the richer tiers; a later MyGo
  that reports them needs no resolver change.
