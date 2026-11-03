# Eternal Terminal protocol and in-process native integration

Status: research / decision input  
Date: 2026-11-04  
Scope: what an Eternal Terminal (ET) client must implement; whether upstream
`MisterTea/EternalTerminal` can run in-process on iOS and Android inside a MyGo
app; existing ET clients; stock `etserver` interoperability; and a workable
Natcatty integration plan.

Evidence labels:

- **[Verified]** read directly in the cited source, protocol file, or official
  documentation at the cited version/commit.
- **[Estimate]** engineering judgement, not a fact stated by upstream or by a
  vendor. Estimates are deliberately ranges.

## 1. Bottom line

**[Verified]** An ET client is not just a terminal emulator over a socket. It
must perform an SSH bootstrap, run the ET connect handshake, encrypt and frame
the stream, speak the terminal packet protocol, keep the link alive, and
reconnect with sequence-number catch-up. The full checklist is in section 9.

**[Verified]** Upstream ET cannot be used as-is as an in-process library on iOS
or Android:

- Its client launches the system `ssh` with `fork()` + `execvp()` on POSIX and
  `CreateProcess()` on Windows
  ([`SubprocessToString.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/SubprocessToString.cpp)).
  iOS apps may not spawn child processes, as Apple DTS states directly
  ([Apple DTS 747499](https://developer.apple.com/forums/thread/747499)).
- Its local `Console` implementation is a real terminal using `termios` and
  `ioctl(TIOCGWINSZ)`
  ([`PsuedoTerminalConsole.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/PsuedoTerminalConsole.hpp)),
  which is not the shape of a mobile app's terminal widget.
- The build produces static libraries (`et-lib`, `TerminalCommon`) and
  executables, but no shared library, no installed headers, and no `extern
  "C"` surface
  ([`CMakeLists.txt`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/CMakeLists.txt)).
- It depends on C++17, OpenSSL, protobuf, libsodium, zlib, and a group of
  vendored C++ helpers (section 3.2), none of which has a supported mobile
  build in this repository.
- MyGo's phone native pipeline compiles **C** sources into a signed framework
  or APK library and explicitly says phones cannot take libraries built
  elsewhere; it has no documented C++ or prebuilt-static-library path
  ([MyGo PR 940 `docs/mobile.md`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md),
  [`cmd/mygo/mobile_natives.go`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_natives.go)).

**[Estimate]** The practical path is a **pure-Go ET client** that reuses
Natcatty's existing `golang.org/x/crypto/ssh` Connection Manager for the SSH
bootstrap and for a `direct-tcpip` channel to `etserver`, implements
`application/session.Transport`, and plugs into MyGo's terminal through
`terminal.Options{Conn: ...}` with no local PTY. A protocol-6 client is roughly
**5–8 engineer-weeks** from scratch, or **3–5 engineer-weeks** by forking the
Apache-2.0 `tphakala/et-go` protocol core. Adding v7 challenge/reset support is
another **1–2 engineer-weeks**. A C++ native-library wrapper is roughly
**12–24 engineer-weeks (3–6 months)** with high platform and MyGo-runtime risk,
and is not recommended.

**[Verified]** A second, independent blocker exists for the product goal: at
MyGo PR 940's head, the terminal plugin is not listed among the mobile plugins,
and its `mygo-plugin.json` contains only desktop builds of `libghostty-vt`.
ET on mobile therefore also depends on MyGo shipping a mobile terminal
implementation; the ET protocol client alone does not make a usable mobile
terminal. See section 4.6.

## 2. ET architecture and protocol

### 2.1 Three processes

**[Verified]** ET uses three processes
([`docs/protocol.md` at et-v7.1.1](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/docs/protocol.md)):

- `et`: the client, on the user's machine.
- `etserver`: the daemon/router on the server, listening on TCP port 2022 by
  default.
- `etterminal`: a per-session process, running as the user, that owns the
  server-side PTY and shell.

SSH is used only to authenticate and to launch `etterminal`. The ET protocol
itself then runs over a separate TCP connection to `etserver`. The client does
not use an SSH channel for terminal traffic.

### 2.2 SSH bootstrap

**[Verified]** The client generates a 16-character id and a 32-character
passkey, then runs a remote command of this shape over SSH
([`SshSetupHandler.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/SshSetupHandler.cpp)):

```sh
echo '<id>/<passkey>_<TERM>' | etterminal --verbose=N
```

The first three characters of the id are set to `XXX` so a new server
regenerates both id and passkey. `etterminal` reads the line from stdin, splits
it on `_`, requires exactly two tokens, regenerates the credentials when the id
starts with `XXX`, sets `TERM` to the second token, prints
`IDPASSKEY:<id>/<passkey>` to stdout, and daemonizes
([`TerminalMain.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalMain.cpp)).
The client searches stdout for `IDPASSKEY:` and reads 49 characters: 16 id,
one slash, 32 passkey. The old, generated credentials are discarded when the
server regenerates them.

Consequences for any reimplementation:

- Id and passkey must be alphanumeric (the server-side split and crypto code
  assume no `_` or `/` in them).
- `TERM` must not contain `_`; `etterminal` splits the whole stdin line on `_`
  and aborts unless there are exactly two tokens. `xterm-256color` is safe.
- The remote command contains the credentials. On servers that do not
  regenerate, the passkey is visible in the remote process list; new servers
  regenerate and print the live credentials instead.
- SSH authentication is delegated to the system `ssh` in upstream. Password,
  passphrase, host-key, and MFA behavior comes from the user's SSH setup, not
  from ET.

**[Verified]** On POSIX, `SubprocessToStringInteractive` creates a pipe,
`fork()`s, `dup2`s the pipe to stdout, and calls `execvp("ssh", ...)`; on
Windows it uses `CreateProcess`. This is the hard iOS blocker and the reason a
mobile integration cannot simply compile the existing client.

### 2.3 Connect handshake

**[Verified]** The client connects to `etserver` on the ET TCP port (default
2022) and sends a `ConnectRequest` containing `clientId` and `version` as a
protobuf, preceded by an 8-byte `int64_t` length in **host byte order**
([`SocketHandler.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/SocketHandler.hpp),
[`ET.proto` at et-v6.2.10](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/proto/ET.proto)).
The request is not encrypted, because the client and server do not yet share a
key.

Protocol 6 statuses are:

| Status | Value | Meaning |
|---|---:|---|
| `NEW_CLIENT` | 1 | New session; proceed |
| `RETURNING_CLIENT` | 2 | Existing session; proceed with recovery |
| `INVALID_KEY` | 3 | Unknown client id (or bad v7 proof) |
| `MISMATCHED_PROTOCOL` | 4 | Client/server protocol versions differ |

**[Verified]** In protocol 6, `etserver` looks up the id in `clientKeys`,
which `etterminal` populated over the local FIFO. It does **not** verify a
passkey at connect time. The passkey is the 32-byte secretbox key; a wrong key
fails at the first decryption. A known id is enough to be treated as a
returning client and to displace the previous socket
([`ServerConnection.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/ServerConnection.cpp),
[`ServerClientConnection.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/ServerClientConnection.cpp)).
This is a denial-of-service weakness, not a confidentiality break: without the
passkey the attacker cannot decrypt or forge traffic, but can force a
reconnect/takeover. v7's challenge closes it (section 2.7).

**[Verified]** After the connect response, the client sends an `INITIAL_PAYLOAD`
packet and waits for `INITIAL_RESPONSE`. `etserver` requires the first packet
on an established session to be `INITIAL_PAYLOAD`; a keepalive or other packet
first is fatal
([`TerminalServer.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalServer.cpp)).
The payload carries jump-host mode and reverse-tunnel requests in v6; v7 adds
environment, no-PTY, command, exit-status capability, no-shell, and disconnect
timeout fields.

### 2.4 Framing and encryption

**[Verified]** Once connected, packets use a 4-byte length in **network byte
order** (`htonl`/`ntohl`), followed by a 2-byte packet header and the payload
([`BackedReader.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/BackedReader.cpp),
[`BackedWriter.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/BackedWriter.cpp),
[`Packet.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/Packet.hpp)):

```
+----------------+----------------+---------------------------+
| 4-byte length  | encrypted flag | packet type | payload ... |
| network order  | 1 byte         | 1 byte      | ciphertext  |
+----------------+----------------+---------------------------+
```

The length covers the 2-byte header plus the encrypted payload. The encrypted
payload is the plaintext plus libsodium's 16-byte secretbox MAC.

**[Verified]** Crypto is `crypto_secretbox_easy` from libsodium
([`CryptoHandler.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/CryptoHandler.cpp)):

- 32-byte key (the passkey bytes).
- 24-byte nonce, all zero except the last byte: 0 for client-to-server, 1 for
  server-to-client.
- The nonce is incremented as a little-endian integer **before** every encrypt
  or decrypt.
- The Go standard-library-adjacent `golang.org/x/crypto/nacl/secretbox` is
  NaCl-compatible with libsodium's secretbox and uses the same 32-byte key and
  24-byte nonce. `golang.org/x/crypto` is already a Natcatty dependency
  (`go.mod` at commit `240c8c6aa6790f26c447d6761b0037a28c753f9f`).

Handshake protobufs (`ConnectRequest`, `ConnectResponse`, `SequenceHeader`,
`CatchupBuffer`) use the 8-byte host-order length framing and are not encrypted.
Established packet traffic uses the 4-byte network-order framing and is
encrypted. `readProto` caps a handshake protobuf at 128 MiB.

**[Verified]** `MAX_BACKUP_BYTES` is 64 MiB. The writer keeps encrypted,
serialized packets for replay; when the oldest packets exceed that bound they
are dropped, and a reconnect that needs them fails with
"Client is too far behind server"
([`BackedWriter.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/BackedWriter.hpp),
[`BackedWriter.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/BackedWriter.cpp)).

### 2.5 Terminal packets

**[Verified]** Packet types in v6
([`ETerminal.proto` at et-v6.2.10](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/proto/ETerminal.proto)):

| Type | Value | Direction | Meaning |
|---|---:|---|---|
| `KEEP_ALIVE` | 0 | both | Liveness echo |
| `TERMINAL_BUFFER` | 1 | both | Raw terminal bytes |
| `TERMINAL_INFO` | 2 | client | Window size / control |
| `PORT_FORWARD_DESTINATION_REQUEST` | 5 | both | Port forwarding |
| `PORT_FORWARD_DESTINATION_RESPONSE` | 6 | both | Port forwarding |
| `PORT_FORWARD_DATA` | 7 | both | Port forwarding |
| `TERMINAL_USER_INFO` | 8 | etterminal -> etserver | Register id/passkey |
| `TERMINAL_INIT` | 9 | etserver -> etterminal | Start the shell |
| `JUMPHOST_INIT` | 10 | etserver -> etterminal | Jump relay mode |

The client-side terminal loop reads up to 16 KiB from its local console and
sends `TERMINAL_BUFFER` packets; server output is sent back as
`TERMINAL_BUFFER` and written to the console. A size change is sent as
`TERMINAL_INFO{row, column, width, height}`; the server applies
`row`/`column` to the remote PTY's `winsize`. The server rate-limits input to
1024 newlines per second unless started with `--noratelimit`
([`TerminalClient.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalClient.cpp),
[`UserTerminalHandler.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/UserTerminalHandler.cpp)).

**[Verified]** Keepalive behavior: the client sends `KEEP_ALIVE` after
`keepaliveDuration` seconds (default/maximum 5). If the next interval expires
without an echo, it closes the socket and reconnects. The server's
`SERVER_KEEP_ALIVE_DURATION` is 11 seconds, chosen to be at least double the
client value
([`Headers.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/Headers.hpp),
[`TerminalClient.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalClient.cpp)).
An implementation must not send a keepalive before `INITIAL_PAYLOAD`.

### 2.6 Reconnect, resume, and sequence catch-up

**[Verified]** A dropped TCP connection does not end the remote session. The
`etterminal` process continues running, and `etserver` keeps the client id
registered. The v6 server has no disconnect timer: the code contains a TODO to
add one
([`TerminalServer.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalServer.cpp)).
v7 adds an optional `disconnect_timeout` (default 0, meaning no timeout)
(section 2.7).

The client reconnect loop runs once per second. It connects again with the same
id and version. The server replies `RETURNING_CLIENT`. The two sides then
exchange, unencrypted and length-prefixed, a `SequenceHeader` containing the
last sequence number each has received, followed by a `CatchupBuffer`
containing the missing encrypted packets. Each `BackedReader`/`BackedWriter`
increments its sequence number for every packet; the catch-up mechanism assumes
those sequence numbers are preserved across a reconnect
([`ClientConnection.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/ClientConnection.cpp),
[`Connection.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/Connection.cpp)).

**[Verified]** Sequence numbers are per direction and start at zero. Because
the passkey and nonce also start at zero and advance per packet, a new process
must not simply reconnect with the same id and fresh sequence/nonce state:
that would reuse keystream from the first connection. v7's reset path exists
precisely to make a fresh process safe (section 2.7). Protocol 6 has no safe
fresh-process reattach. In-process reconnects are safe because the same
reader/writer objects retain both sequence and nonce state.

If the peer is too far behind for the 64 MiB replay buffer, recovery fails with
"Client is too far behind server." A catch-up exchange itself is a protobuf
limited to 128 MiB, so an implementation must bound both the retained ring and
the catch-up message.

### 2.7 Protocol v7 delta

**[Verified]** `PROTOCOL_VERSION` remains **6** through v7.1.1. The v7 changes
are capability fields and new messages within protocol 6, not a version bump
([`ET.proto` at et-v7.1.1](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/proto/ET.proto),
[`Headers.hpp` at et-v7.1.1](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/src/base/Headers.hpp),
and the protocol document's statement: "The challenge is a capability within
protocol 6, not a version bump").

v7 adds:

- `ConnectRequest.resetIntent` (field 3) and `supportsChallenge` (field 4).
- `ConnectStatus.RETRY_LATER` (5).
- `ConnectResponse.authChallenge`, `resetRequired`, `resetProof`, `resetSalt`.
- `ConnectAuth.proof`: a keyed BLAKE2b-256 proof over the client id, protocol
  version, 32-byte challenge, and `resetIntent`.
- `SequenceHeader.reset` and `resetSalt`; reset recovery zeroes both sequence
  numbers and catch-up state, and rekeys from
  `BLAKE2b-256(salt, baseKey)`.
- `TERMINAL_CLOSE` (11) and `TERMINAL_EXIT_STATUS` (12).
- `TerminalBuffer.is_stderr`, `TerminalInfo.command`/`commandversion`,
  `InitialPayload.environmentvariables`, `no_pty`, `command`,
  `supports_exit_status`, `no_shell`, `disconnect_timeout_seconds`, and
  `TerminalUserInfo.ptyactive`.

**[Verified]** The challenge exchange is: client sends
`ConnectRequest{supportsChallenge=true}`; server sends a fresh 32-byte
`authChallenge`; client answers with `ConnectAuth{proof}`; server sends the
final `NEW_CLIENT` or `RETURNING_CLIENT` response, including a `resetProof`
over the challenge, status, reset decision, and salt. A bad proof yields
`INVALID_KEY`
([`ClientConnection.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/src/base/ClientConnection.cpp),
[`ServerConnection.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/src/base/ServerConnection.cpp)).

**[Verified]** Source-level nuance: the v7 protocol document says "A fresh
client process (`--attach`, or any initial connect) sets
`ConnectRequest.resetIntent`." The implementation is narrower. `TerminalClient`
passes `_resumeSavedSession` as `_resetIntent`; that flag is true only on the
saved-session attach path. A normal initial connect by a new client sends
`resetIntent=false`
([`TerminalClient.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/src/terminal/TerminalClient.cpp),
[`TerminalClientMain.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/src/terminal/TerminalClientMain.cpp)).
An implementer should follow the source, not the document's parenthetical.

**[Verified]** Legacy interoperability:

- A v7 client sends `supportsChallenge=true`. A v6 server ignores the unknown
  fields, replies without `authChallenge`, and the v7 client accepts the
  legacy response when `resetIntent` is false. Normal v7-to-v6 sessions work.
- A v7 client using `--attach` sets `resetIntent=true`. Against a v6 server it
  gets no challenge and a `NEW_CLIENT`/`RETURNING_CLIENT` response, and throws
  `LEGACY_SERVER_REATTACH_ERROR` ("Server does not support session reattach;
  upgrade etserver").
- A v6 client sends no `supportsChallenge`. A v7 server treats it as a
  `legacyPeer`: it authenticates by id only, does not send `RETRY_LATER`, and
  cannot safely resume it after an `etserver` restart; that case returns
  `INVALID_KEY`.
- After an `etserver` restart, a surviving `etterminal` re-registers with
  `TerminalUserInfo.ptyactive`; until it does, v7 clients receive
  `RETRY_LATER`. Legacy peers do not.

**[Verified]** v7 `--attach` is a single-client handoff, not multi-attach. It
opens a new client process with the saved id/passkey and `resetIntent=true`,
which causes the server to reset sequence history and rekey. The terminal
screen is not transferred; the new terminal starts with whatever local
scrollback/screen it has. The remote shell may redraw on a resize or output,
but ET has no screen-state snapshot. This is a product-visible limitation for
mobile app relaunch.

**[Verified]** `disconnect_timeout`: `etserver --disconnect-timeout MINUTES`
or `et.cfg` sets a global; a client may set
`InitialPayload.disconnect_timeout_seconds` to override it per session. Zero,
the default, keeps a disconnected terminal alive
([protocol document](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/docs/protocol.md)).
The v6 server has no equivalent timer.

### 2.8 Jump hosts, port forwarding, and multiple clients

**[Verified]** Upstream jump-host mode is a relay, not an SSH `ProxyJump` for
the ET socket. The client runs `ssh -J jumphost target` to launch the
destination `etterminal`, then runs a second `ssh jumphost` to launch
`etterminal --jump --dsthost=<target> --dstport=<et-port>`. The jump-host
`etterminal` relays to the destination `etserver`, and the client connects its
ET TCP socket to the jump host's `--jport`
([`SshSetupHandler.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/SshSetupHandler.cpp),
[`UserJumphostHandler.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/UserJumphostHandler.cpp)).
Natcatty currently exposes exactly one `--jumphost`/`--jport` and no proxy
(section 7).

**[Verified]** Port forwarding exists upstream: forward, reverse, dynamic, and
SSH-agent forwarding are implemented as ET packets over the same encrypted
stream. The current Natcatty ET configuration does not expose tunnels; they are
outside Ticket 43's terminal parity scope.

**[Verified]** Multiple clients do not share one session. `etserver` has one
`ServerClientConnection` per client id. A second connection with the same id
gets `RETURNING_CLIENT`, and `recoverClient` closes the previous socket before
recovering. There is no fan-out or multi-attach. Multiple sessions require
distinct ids. v7 `--attach` is a takeover/handoff of the single client slot,
not a second viewer.

**[Verified]** Scrollback is not a protocol feature. The protocol has no
request for historical scrollback; the only history is the live
`TERMINAL_BUFFER` stream and the reconnect catch-up buffer (up to 64 MiB of
recent encrypted packets). Scrollback lives in the client terminal emulator.
A newly attached client does not receive the previous screen or scrollback.

### 2.9 Interoperability matrix

Protocol version is the key discriminator:

| Version | `PROTOCOL_VERSION` | Source |
|---|---:|---|
| v5.1.10 | 4 | [`Headers.hpp` at et-v5.1.10](https://github.com/MisterTea/EternalTerminal/blob/et-v5.1.10/src/base/Headers.hpp) |
| v6.0.1 | 6 | [`Headers.hpp` at et-v6.0.1](https://github.com/MisterTea/EternalTerminal/blob/et-v6.0.1/src/base/Headers.hpp) |
| v6.2.10 | 6 | [`Headers.hpp` at et-v6.2.10](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/Headers.hpp) |
| v6.2.11 | 6 | [`Headers.hpp` at et-v6.2.11](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.11/src/base/Headers.hpp) |
| v7.0.0 | 6 | proto at tag (no challenge/reset fields) |
| v7.1.1 | 6 | [`Headers.hpp` at et-v7.1.1](https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/src/base/Headers.hpp) |

Tag/release dates from the upstream release API: v6.2.10 published
2025-07-21, v6.2.11 2025-07-22, v7.0.0 2026-07-07, v7.1.1 2026-10-29.

Client/server behavior:

| Server | Protocol-6 client (legacy handshake) | v7.1-style client | Notes |
|---|---|---|---|
| v5.1.x | `MISMATCHED_PROTOCOL` | `MISMATCHED_PROTOCOL` | Protocol 4; not supported |
| v6.0.1–v6.2.11 | New session and in-process reconnect work; fresh-process reattach unsafe | Normal new session works via legacy; `--attach` fails with `LEGACY_SERVER_REATTACH_ERROR` | No challenge, no `RETRY_LATER`, no v7 exit status |
| v7.0.0 | New session and in-process reconnect work | Normal new session works via legacy; `--attach` fails because v7.0.0 has no challenge fields | v7.0.0 proto has no challenge/reset/exit-status |
| v7.1.1 | New and in-process reconnect work via legacy; no v7 reset/attach/exit status | Full challenge/reset/attach/exit-status path | Legacy peers cannot resume after an `etserver` restart |

**[Verified]** `et-v7.0.0`'s `proto/ET.proto` still has only the v6
`ConnectRequest`/`ConnectStatus` fields and no `authChallenge`, `resetIntent`,
or `RETRY_LATER`; `et-v7.1.1` adds them. `et-v7.0.0`'s
`proto/ETerminal.proto` also lacks `TERMINAL_EXIT_STATUS` and
`supports_exit_status`; `et-v7.1.1` adds them. This is why the table separates
v7.0.0 from v7.1.1.

A recommended minimum interop gate for Natcatty: stock `etserver` v6.2.10
(the version Natcatty currently bundles) and v7.1.1, with both a legacy
protocol-6 client and a v7-capability client; plus v6.2.11 and v7.0.0 if those
server versions are in the supported matrix.

## 3. Upstream source, build, and library feasibility

### 3.1 Source layout and build targets

**[Verified]** The repository is C++17 and CMake
([`CMakeLists.txt`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/CMakeLists.txt)):

- `src/base`: sockets, framing, crypto, `BackedReader`/`BackedWriter`,
  `ClientConnection`, `ServerConnection`, `ServerClientConnection`,
  subprocess, logging, daemonization.
- `src/terminal`: `TerminalClient`, `TerminalServer`, `etterminal` main,
  `UserTerminalHandler`, `UserJumphostHandler`, `Console`,
  `PsuedoTerminalConsole`, forwarding.
- `src/htm`: HTM client/server.

CMake builds two static libraries, `et-lib` and `TerminalCommon`, and the
executables `et`, `etserver`, `etterminal`, `htm`, and `htmd`. On non-Windows
it installs `etserver` and `etterminal`; on Windows it installs only `et`.
There is no `add_library(... SHARED)`, no exported header set, no
`install(DIRECTORY include...)`, and no `extern "C"` declaration anywhere under
`src` (zero matches in `git grep -F 'extern "C"' et-v6.2.10 -- src`). The
static libraries are build artifacts, not a supported embedding API.

### 3.2 Dependencies

**[Verified]** CMake requires ZLIB, OpenSSL, Threads, libsodium, and Protobuf;
it optionally finds Unwind, UTempter, SELinux, and Sentry
([`CMakeLists.txt`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/CMakeLists.txt)).
It also vendors/builds easylogging++, PlatformFolders, ThreadPool, sole,
base64, cpp-httplib, nlohmann-json, simpleini, cxxopts, and Catch2.
`vcpkg.json` lists zlib, openssl, protobuf, libsodium, sentry-native,
nlohmann-json, simpleini, platform-folders, cpp-httplib, cxxopts, and catch2
([`vcpkg.json`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/vcpkg.json)).
The repository uses git submodules for these dependencies
([`.gitmodules`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/.gitmodules)).
Boost is only for old macOS (`find_package(Boost 1.76 ...)` when the macOS SDK
is older than 10.15).

The static build script manually builds CMake, autoconf, automake, libtool,
libsodium, and protobuf before configuring ET
([`build_static.sh`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/build_static.sh)).
This is a Linux-oriented static-binary recipe, not a mobile-library recipe.

### 3.3 Runtime blockers on iOS and Android

**[Verified]** Upstream's client is a process, not a library:

- SSH bootstrap uses `fork`/`execvp` on POSIX; iOS forbids child processes
  (DTS 747499).
- The local console is a real terminal: `tcgetattr`/`cfmakeraw`/`tcsetattr`
  and `ioctl(TIOCGWINSZ)` in `PsuedoTerminalConsole`; upstream `TerminalClient`
  calls `read(consoleFd, ...)` and `console->write(...)`.
- `etterminal` daemonizes with `DaemonCreator::createSessionLeader()`, writes
  logs, and resolves `HOME`, SSH config, temp paths, and platform folders.
- `etserver` and `etterminal` are server-side processes; the mobile app only
  needs the client, but the client still depends on the process-oriented
  console and subprocess code.
- CMake has only a few Android conditionals (`if(ANDROID)` around the Unwind
  include and `CORE_LIBRARIES`), and no iOS/Android toolchain file, vcpkg
  triplet, or mobile artifact. There is no official Android AAR or iOS
  framework.

Android is less absolute than iOS: an app can create child processes, and
Termux-style environments can run `et`. But an ordinary Android app has no
system `ssh`, no user shell environment, and no guarantee of a usable PTY; the
client would still need a host-provided SSH implementation and a non-PTY
console. Shipping and executing an `et` binary from the APK also conflicts
with modern Android W^X/execution expectations and app-size/ABI concerns. It is
not a credible in-process route.

### 3.4 The `Console` seam does make a wrapper technically possible

**[Verified]** `Console` is an abstract class with `getTerminalInfo()`,
`setup()`, `teardown()`, `getFd()`, and a virtual `write()`
([`Console.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/Console.hpp)).
`TerminalClient`'s public constructor accepts a `shared_ptr<Console>` and the
id/passkey; `run(command, noexit)` does not call `SshSetupHandler` itself
([`TerminalClient.hpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalClient.hpp),
[`TerminalClientMain.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalClientMain.cpp)).
Upstream issue #779 demonstrates exactly this seam: a prototype replaces the
TTY console with a pipe plus scrollback buffer and exposes it over a Unix
socket, and states that `TerminalClient::run()` and the reconnect path are
unchanged, with no changes to `etserver`, `etterminal`, or the wire protocol
([issue #779](https://github.com/MisterTea/EternalTerminal/issues/779)).

So a native wrapper could:

1. Do SSH bootstrap in the host app (Go, Swift, or Kotlin).
2. Construct `TerminalClient` with the resulting id/passkey.
3. Supply a custom `Console` backed by a socketpair/pipe and a size field.
4. Supply a custom `SocketHandler` if the ET TCP connection is to be carried
   through the app's SSH channel rather than a raw socket.

That is a real technical path. It does not remove the C++/OpenSSL/protobuf/
libsodium build, C ABI, mobile toolchain, or MyGo packaging problems, which are
the expensive parts.

### 3.5 Is there a stable C ABI or shared library today?

**[Verified]** No. There is no `extern "C"`, no installed header set, no shared
library target, and no documented embedding API. The only reusable units are
the static `et-lib` and `TerminalCommon` libraries, plus C++ headers that are
not installed. A wrapper would have to add:

- a new C ABI translation unit with opaque handles and `extern "C"` functions;
- a custom `Console` (and likely a custom `SocketHandler`) bridging to the
  host;
- exception containment at the ABI boundary;
- a shared-library or framework build target and an install/packaging story;
- all dependencies cross-compiled for iOS and each Android ABI.

None of that exists upstream. "Static library with a stable C ABI" is therefore
a new project, not a configuration flag.

## 4. In-process integration options

### 4.1 Option A: static libraries + cgo

**[Verified]** Upstream can produce static `et-lib` and `TerminalCommon`;
`build_static.sh` exists. But cgo linking a C++ static library into a Go mobile
app requires:

- a C++ standard library and exceptions/RTTI on both platforms;
- static OpenSSL, protobuf, libsodium, zlib, and the helper libraries built for
  each target;
- a C ABI wrapper around the C++ classes;
- a replacement for the `ssh` subprocess and the local PTY console;
- MyGo's build to accept the resulting static library and cgo directives.

**[Verified]** MyGo PR 940's mobile model keeps `import "C"` in
`internal/mobile`'s glue and says plugins need no cgo; the documented app path
is cgo only to link the Go runtime on phones
([`docs/mobile.md`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md)).
There is no documented way for a Natcatty plugin or app package to add its own
cgo C++ library to the iOS c-archive or Android c-shared build.

Verdict: technically conceivable, but it fights both ET and MyGo. It also
carries the largest app-size, build-matrix, and App Review surface. Not
recommended.

### 4.2 Option B: dynamic library + purego / `NativeLibrary`

**[Verified]** MyGo loads plugins with purego and `mygo.NativeLibrary(name)`:
on Android it returns `lib<name>.so`; on iOS it returns
`Frameworks/<name>.framework/<name>`
([`native.go`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/native.go)).
On iOS, a dynamic library must be shipped inside a framework and signed with
the app; Apple DTS says "iOS-based platforms do not allow you to ship a
'naked' dynamic library. You must wrap it in a framework"
([Apple DTS 130502](https://developer.apple.com/forums/thread/130502)). There is
no blanket prohibition on `dlopen`/`dlsym`, but App Review has final authority.
MyGo's mobile build creates exactly such a framework for plugin C code and
signs it with the app.

**[Verified]** However, MyGo's phone native pipeline compiles the plugin's C
sources itself and says: "Phones can't take libraries built elsewhere as
desktops do: on iOS they are signed with the app, and only a compiler of the
SDK targets them"
([`cmd/mygo/mobile_natives.go`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_natives.go)).
The archive extractor only appends `.c` files to the compiler arguments; the
documented language is C. The `libraries` manifest key is for desktop native
libraries; on phones, a library the plugin compiles takes their place.

A prebuilt ET C++ framework/.so would therefore need a MyGo runtime change to
package and sign it, plus a C ABI. That is a platform-project dependency, not
just an ET integration. Verdict: not viable without MyGo changes, and still
carries the C++ dependency/build burden.

### 4.3 Option C: MyGo plugin with Swift/Kotlin glue

**[Verified]** MyGo plugins can have Swift and Kotlin plugin classes that call
into Go through `mygo.Native("name").Call(...)`
([MyGo plugin docs](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md),
[`docs/mobile.md`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md)).
This is a good way to expose platform APIs, but ET is a stateful C++ client
plus SSH plus terminal protocol. Reimplementing ET in Swift and Kotlin would
duplicate the protocol, crypto, framing, and reconnect logic in two languages,
with no shared tests or code. Wrapping the C++ client from Swift/Kotlin has the
same C ABI and mobile-build problems as option B. Verdict: not recommended.

### 4.4 Option D: pure-Go ET client (recommended)

**[Verified]** Natcatty already has every host-side primitive needed:

- `application/session.Transport` is `io.ReadWriteCloser` plus
  `Resize(cols, rows int) error`
  ([`application/session/session.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/application/session/session.go)).
- `ui/session/surface.go` builds MyGo's terminal with
  `terminal.Options{Conn: conn, ...}`, so any `Transport` works without an
  ET-specific UI path
  ([`ui/session/surface.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/ui/session/surface.go)).
- `application/connection.Manager` exposes a `Lease` with `OpenExec` for the
  SSH bootstrap and `Forwarding().DialContext` for a `direct-tcpip` channel
  through the authenticated SSH connection
  ([`application/connection/manager.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/application/connection/manager.go)).
- The Mosh bootstrap in `app/transports.go` is a working template: acquire a
  lease, call `lease.OpenExec(ctx, cfg.RemoteCommand())`, parse the bootstrap
  output, and tie the channel to the lease's lifetime
  ([`app/transports.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/app/transports.go)).
- `adapters/ssh/terminal.go` already opens `direct-tcpip` channels through the
  SSH host with `client.DialContext`
  ([`adapters/ssh/terminal.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/adapters/ssh/terminal.go)).

A pure-Go ET transport would:

1. Use `lease.OpenExec` to run the upstream-compatible bootstrap command on the
   target (through the existing jump/proxy/known-host/auth path).
2. Parse `IDPASSKEY:<id>/<passkey>` from the exec output.
3. Use `lease.Forwarding().DialContext(ctx, "tcp", etEndpoint)` to open an ET
   TCP channel through the same SSH connection. The default `etserver` binds
   wildcard port 2022, so `127.0.0.1:2022` works on the target unless the
   operator set `bind_ip`
   ([`TcpSocketHandler.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/base/TcpSocketHandler.cpp),
   [`TerminalServerMain.cpp`](https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/src/terminal/TerminalServerMain.cpp)).
4. Speak the ET protocol over that channel, expose `Read`/`Write`/`Resize`,
   and keep the lease for the session's lifetime.
5. Plug into the existing `application/session` and terminal UI unchanged.

This path has no local PTY, no subprocess, no C++, no dynamic library, and no
temporary SSH home/askpass. It works anywhere `x/crypto/ssh` and
`golang.org/x/crypto/nacl/secretbox` compile, including iOS and Android, once
MyGo's terminal UI is available there.

**[Verified]** `golang.org/x/crypto/ssh` is pure Go and already pinned at
v0.58.0 in `go.mod`; `golang.org/x/crypto/nacl/secretbox` is in the same
module. `google.golang.org/protobuf` is not yet a Natcatty dependency, but it is
pure Go and mobile-safe if generated proto code is used. Alternatively, a
small hand-written protobuf codec can be used, but generated code is less
error-prone and matches the upstream `.proto` files.

### 4.5 iOS App Store and Android platform constraints

**iOS / App Store**

- **[Verified]** App Store Review Guidelines 2.5.2: "Apps should be
  self-contained in their bundles, and may not read or write data outside the
  designated container area, nor may they download, install, or execute code
  which introduces or changes features or functionality of the app, including
  other apps"
  ([App Store Review Guidelines](https://developer.apple.com/app-store/review/guidelines/#software-requirements)).
  Bundled code is fine; downloading code to add features is not. A pure-Go
  client compiled into the app is the safest posture. A bundled, signed
  framework is also bundled code, not downloaded code.
- **[Verified]** Apple DTS: "You are correct that iOS apps are not allowed to
  spawn child processes" (Quinn, Feb 2024)
  ([Apple DTS 747499](https://developer.apple.com/forums/thread/747499)). This
  rules out the upstream `ssh` subprocess design and any "ship the et binary
  and exec it" plan.
- **[Verified]** Apple DTS: no blanket prohibition on `dlopen`/`dlsym`, but a
  dynamic library must be inside a framework, and App Review decides
  ([Apple DTS 130502](https://developer.apple.com/forums/thread/130502)).
  MyGo already signs embedded frameworks with the app
  ([`docs/mobile.md`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md)).
- iOS can suspend or kill an app in the background. ET's server-side session
  survives, but a killed client process can only safely reattach with v7
  challenge/reset; v6 has no safe fresh-process reattach. Plan for this in the
  product behavior and persistence design.

**Android**

- **[Verified]** NDK-supported ABIs are arm64-v8a, armeabi-v7a, x86, and
  x86_64; native libraries go in the APK at `/lib/<abi>/lib<name>.so`
  ([Android NDK ABIs](https://developer.android.com/ndk/guides/abis)). Google
  Play requires a 64-bit build.
- **[Verified]** Apps targeting Android 15 (API 35) and higher must support
  16 KB page sizes on 64-bit devices. NDK r28+ defaults to 16 KB alignment;
  older NDKs need `-Wl,-z,max-page-size=16384` (and commonly
  `-Wl,-z,common-page-size=16384`), and any prebuilt shared library must be
  rebuilt/reimported with that alignment
  ([Android 16 KB page sizes](https://developer.android.com/guide/practices/page-sizes)).
  MyGo's Android native build already passes
  `-Wl,-z,max-page-size=16384`
  ([`mobile_natives.go`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_natives.go)).
- **[Verified]** An Android app can create child processes, but an ordinary app
  has no system `ssh` and no terminal environment. That does not make upstream
  `et` a practical library. A pure-Go client needs no NDK C++ and no child
  process.
- Android background/Doze restrictions mean a long-lived session may need a
  foreground service or may simply reconnect on resume. That is an app-runtime
  concern, not an ET protocol change.

### 4.6 MyGo PR 940 mobile runtime: what it does and does not give us

**[Verified]** PR 940 ("Run apps on iOS and Android") was open, unmerged, and
marked not mergeable at head
`7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a` when checked (updated
2026-11-02). Natcatty currently pins MyGo v0.5.5, not this PR
([`go.mod`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/go.mod)).
Treat everything below as PR-head behavior, not current Natcatty behavior.

What PR 940 provides:

- iOS: Go code links with Xcode's clang (cgo) into a c-archive linked by a
  Swift host; the CLI builds and signs the bundle.
- Android: Go code is a shared library per ABI, linked by the NDK clang and
  built by Gradle.
- Plugin native C code: the CLI compiles `.c` sources per target with the SDK
  compiler; iOS gets a signed framework in the bundle, Android a `.so` in the
  APK; `mygo.NativeLibrary(name)` returns the right path for purego.
- Plugins need no cgo; `import "C"` is kept in MyGo's `internal/mobile` glue.
- Phones do not accept libraries built elsewhere; the CLI must compile them.

What PR 940 does **not** provide at this head:

- **[Verified]** The official mobile plugin list in `docs/mobile.md` includes
  location, maps, camera, imagepicker, videoplayer, biometrics, sensors, push,
  purchases, permissionflow, preferences, sqlite, and background. It does not
  include `terminal`
  ([`docs/mobile.md`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md)).
- **[Verified]** `plugins/terminal/mygo-plugin.json` lists `libghostty-vt`
  builds only for darwin-amd64/arm64, linux-amd64/arm64, and
  windows-amd64/arm64. There is no `mobile` section and no iOS/Android entry
  ([`plugins/terminal/mygo-plugin.json`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/mygo-plugin.json)).
  `terminal.New` calls `Load()`, which fails if the platform has no supported
  `libghostty-vt` path.
- **[Verified]** The terminal package itself is generic over any
  `io.ReadWriteCloser`: `Options.Conn` is the remote stream, and a `Conn` with
  `Resize(cols, rows int) error` is told the size
  ([`terminal.go`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/terminal.go),
  [`docs/plugins/terminal.md`](https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/plugins/terminal.md)).
  The missing piece on phones is the VT/native terminal library, not the
  stream API.
- **[Verified]** `internal/pty` has Unix implementations for darwin/linux and
  a stub for other systems; iOS/Android compile the Unix files. But a remote
  ET session does not need a local PTY, so PTY availability is not the ET
  blocker. The VT library is.

Implication: a pure-Go ET client is portable to mobile, but the product cannot
show it on a phone until MyGo's terminal plugin has a mobile VT implementation
or Natcatty uses another terminal renderer. That work belongs to the MyGo
mobile-runtime ticket, not to the ET client, and should be tracked as a hard
dependency.

### 4.7 Option comparison

| Option | ET code reused | iOS | Android | MyGo fit | Effort | Verdict |
|---|---|---|---|---|---|---|
| Upstream binary + PTY | Whole client | Blocked (no child processes) | No system ssh; binary/exec issues | Current desktop design | Low but nonviable | Reject |
| Static lib + cgo | C++ core | Needs C++ toolchain, C ABI, deps | Same + ABIs | cgo outside documented plugin path | High | Not recommended |
| Dynamic lib + purego | C++ core | Framework signing, C ABI | .so per ABI, 16 KB | Needs prebuilt-lib support | High | Not viable without MyGo changes |
| Swift/Kotlin plugin | None; reimplement | Native | Native | Good plugin fit | Very high / duplicated | Not recommended |
| Pure-Go client | Protocol reimplementation | Pure Go, no PTY, no subprocess | Pure Go, no NDK | Direct `Transport` + `terminal.Conn` | Medium | **Recommended** |

## 5. Existing ET clients

### 5.1 `tphakala/et-go`

**[Verified]** Pure-Go client at commit
`bc33dd76b558418bc1079ba27c8c477b8145eea5` (2026-10-19), Apache-2.0 with a
`NOTICE` that also attributes the upstream protocol definitions. README status:
"MVP. Interactive shell with transparent reconnect against etserver 7.0.0; port
forwarding is not implemented yet." It requires an OpenSSH client on `PATH`;
its README says a `ProxyJump` host is not used for the ET server connection and
it has no ET jumphost. It does not implement port forwarding.

Size and maturity: 110 Go files total, 49 test files, 10,014 non-test lines,
one local commit in the shallow clone (the GitHub history has 12 commits), no
tags or releases. It is the closest existing Go implementation, but it is an
MVP, not a library.

Useful pieces:

- `internal/protocol`: generated protobuf types and packet headers.
- `internal/wire`: 4-byte network-order framing, 2-byte packet header,
  128 MiB limits.
- `internal/seal`: `x/crypto/nacl/secretbox` with upstream nonce direction and
  increment.
- `internal/etcp`: a reconnecting, sequenced, encrypted packet connection with
  a 64 MiB replay limit, 5-second keepalive, backpressure, and a `NetDialer`
  interface that accepts any `DialContext` implementation. This is exactly the
  seam needed to run ET over Natcatty's SSH `direct-tcpip` channel.
- `internal/etservertest`: an in-process server fixture for tests.

Caveats: every useful package is under `internal/`, so Natcatty cannot import
it as a module; it must vendor/fork the code (Apache-2.0 permits this with
license/NOTICE retention). The implementation is protocol 6 only; it does not
implement v7 challenge/reset/exit-status. Its bootstrap and console are built
around an external `ssh` and a local terminal; Natcatty would replace those
with `Lease.OpenExec` and a `Transport`.

### 5.2 `minpeter/et.rs`

**[Verified]** Rust workspace at commit
`b62c79afe4599bc5e9db2bfd68a78651f5cab979`, `Cargo.toml` declares
`license = "Apache-2.0"` and version `0.0.47`; the repository has no top-level
`LICENSE` file (only a libvterm license was found in the tree). README: "Wire-
compatible with upstream protocol version 6: an et.rs client can talk to a C++
etserver, and a C++ et client can talk to an et.rs server, in both terminal and
port-forwarding modes." It implements client and server roles, terminal and
port forwarding, jumphost, reverse tunnels, agent forwarding, SSH config, and
Windows ConPTY. It has 30 releases; latest `et@0.0.47` on 2026-10-28.

Value to Natcatty: an excellent **conformance oracle** and a possible CI
server/client for cross-implementation tests. Not embeddable in MyGo: Rust,
native libsodium, no C ABI, and MyGo's phone native pipeline is C-only.

### 5.3 `AArnott/eternal-terminal-client`

**[Verified]** Rust client at commit
`26344d1ddc9537a168317df0080d688881c3a91a`, MIT, version 0.1.1,
Windows-first, protocol 6, external OpenSSH for bootstrap. Implemented:
connect, encrypt, interactive shell, resize, keepalive, automatic reconnect,
optional jumphost. Not implemented: forward/reverse tunnels, SSH agent
forwarding, full `ssh_config`. Useful as a compact reference and as a second
interop check; not a library for MyGo.

### 5.4 Upstream C++ client

**[Verified]** Apache-2.0, mature, full-featured, and the reference
implementation. It is a process, not a library, and its mobile blockers are the
subject of this document.

### 5.5 License summary

| Component | License | Reuse in Natcatty |
|---|---|---|
| Upstream ET (code and `.proto`) | Apache-2.0 | Compatible; keep LICENSE/NOTICE |
| `et-go` | Apache-2.0 (with NOTICE) | Compatible; keep LICENSE/NOTICE if vendored |
| `et.rs` | Cargo declares Apache-2.0; no top-level LICENSE file found | Reference/oracle only; do not vendor without license clarification |
| `AArnott/eternal-terminal-client` | MIT | Reference only |

ET has no GPL component in the client path (unlike Mosh). A pure-Go
reimplementation or an `et-go` fork keeps the client Apache-2.0 and avoids the
GPL obligations that the bundled Mosh client carries.

## 6. Natcatty today

All paths below are at Natcatty commit
`240c8c6aa6790f26c447d6761b0037a28c753f9f`.

**[Verified]** Current ET is a bundled-process adapter:

- `adapters/et/et.go` defines `Process` (read/write/resize/wait/kill), resolves
  the bundled binary, prepares a private `HOME` with `ssh_config`, key files, a
  known-hosts snapshot, an `SSH_ASKPASS` wrapper, and spawns the bundled `et`
  on a PTY. `buildArgs` passes `-p <et-port>`, `--ssh-option`, and
  `--jumphost user@host --jport <jump-et-port>`
  ([`adapters/et/et.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/adapters/et/et.go)).
- `domain/model/et/et.go` defines `DefaultETPort = 2022`, `Config`, `Hop`, and
  `CheckRoute`, which rejects a proxy and more than one jump host
  ([`domain/model/et/et.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/domain/model/et/et.go)).
- `adapters/bundle/manifests.go` pins `et-bin-6.2.10-1` from
  `binaricat/Netcatty-et-bin` for win32-x64, linux-x64, linux-arm64, and
  darwin-universal
  ([`adapters/bundle/manifests.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/adapters/bundle/manifests.go)).
- `resources/et/README.md` records the upstream ref `et-v6.2.10`, commit
  `f9a584ac06b2f1730b5bdf0a27150f28478368fb`, Apache-2.0 license, checksums,
  telemetry-disabled provenance, and the one-jump/no-proxy restriction
  ([`resources/et/README.md`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/resources/et/README.md)).
- `app/transports.go` enriches the Host with Vault credentials, known hosts,
  and one jump Host, then delegates to the process adapter
  ([`app/transports.go`](https://github.com/mslxl/natcatty/blob/240c8c6aa6790f26c447d6761b0037a28c753f9f/app/transports.go)).
- `application/session.Transport` is the seam the session/UI already use;
  `ui/session/surface.go` creates `terminal.Options{Conn: conn}` around it.
  The current ET process adapter returns a `transport` that implements
  `ReadWriteCloser`/`Resize`/`Wait`.

The bundled `et` client performs its own SSH bootstrap and ET handshake, so
Natcatty currently supplies a whole SSH environment to it. That is exactly the
layer a pure-Go client can replace with Natcatty's Connection Manager.

## 7. Recommended integration design

### 7.1 Shape

**[Estimate]** Add a pure-Go ET transport under `adapters/et` (or a new
`adapters/et` package split into `bootstrap.go`, `wire.go`, `crypto.go`,
`transport.go`), keep `domain/model/et.Config` and the existing Host/Vault
plumbing, and change `app/transports.go` to use it. The UI does not need an
ET-specific change:

```
Host/Vault -> app.etTransportFactory -> adapters/et (pure Go)
            -> application/session.Transport
            -> ui/session.Surface -> terminal.Options{Conn: conn}
```

Do not use the generic `ReconnectPolicy` for ET. The ET transport must own its
reconnect loop and keep `Read` blocked across transient drops; otherwise the
Session layer would see an EOF/error and open a new ET session with a new id,
losing the server-side session. The current bundled `et` behaves this way
already; the replacement must match it.

### 7.2 Bootstrap sequence

1. Acquire an `appconnection.Lease` for the Host, with the existing jumps,
   proxy, auth, algorithms, known-host policy, and keepalive. Reuse the Mosh
   bootstrap pattern.
2. Generate a cryptographically random 16-character id and 32-character
   passkey from `[A-Za-z0-9]` using `crypto/rand` with rejection sampling.
   Prefix the id with `XXX` so a modern `etterminal` regenerates both.
3. Run, over `lease.OpenExec`:

   ```sh
   echo '<id>/<passkey>_xterm-256color' | etterminal --verbose=0
   ```

   Validate that the id/passkey are alphanumeric and the TERM has no `_`.
4. Parse stdout for `IDPASSKEY:`; take exactly 49 characters; validate id
   length 16, passkey length 32, and the charset. Treat missing/malformed
   output as a typed bootstrap error, including the common "etterminal not
   found" and "ssh authentication failed" cases.
5. Open the ET TCP channel with
   `lease.Forwarding().DialContext(ctx, "tcp", etEndpoint)`. Prefer
   `127.0.0.1:<ETPort>` because the SSH server is the target host; if the
   product needs an ET server on another address, add a dedicated field rather
   than overloading the SSH hostname. The default `etserver` binds wildcard
   port 2022, but an operator may set `bind_ip`.
6. Speak the ET connect handshake over that channel and send
   `INITIAL_PAYLOAD`; wait for `INITIAL_RESPONSE`. Keep the lease held for the
   session's lifetime; close it only when the ET transport closes.
7. Return a `Transport` whose `Resize` sends `TERMINAL_INFO`, whose `Read`
   yields terminal output, and whose `Write` sends `TERMINAL_BUFFER`. Implement
   `Wait()` for the optional exit-code path (v7 only; 0 otherwise).

One SSH lease can carry the bootstrap exec and the direct-tcpip channel because
`x/crypto/ssh` multiplexes channels. The exact behavior of `OpenExec` with
etterminal's stdin/daemonization and multiple concurrent channels on a pooled
lease should be proven with an integration test early (section 10).

### 7.3 Reconnect and resume behavior

- **In-process network drop:** let the ET transport reconnect with the same
  id/passkey, exchange `SequenceHeader`/`CatchupBuffer`, and continue. Do not
  surface the drop to the Session.
- **App suspension:** if the process survives, the same loop works on resume.
- **App process death:** protocol 6 cannot safely reattach from fresh nonce and
  sequence state. Persist the session id/passkey plus endpoint only if v7
  reset/attach is implemented, and then expect a blank local screen (ET has no
  screen snapshot). Store the passkey in secure storage, never in logs or the
  restore payload. If v7 is not implemented, do not advertise process-death
  resume; the remote session may still be alive, but the client cannot safely
  reattach.
- **Server restart:** v7 clients see `RETRY_LATER` during the recovery grace
  period if the surviving `etterminal` re-registers; legacy clients get
  `INVALID_KEY`. Document this per server version.
- **Session end:** on remote shell exit, end the Session. With v7, optionally
  request `supports_exit_status` and report the exit code; with v6, exit status
  is not available and the upstream client exits 0. For a user-requested close,
  decide whether to leave the remote session alive (upstream behavior) or
  implement a v7 kill; do not silently leak sessions on mobile without a
  product decision.

### 7.4 Routing, jump hosts, and proxy

**[Verified]** The current process adapter supports exactly one jump because
upstream `et` exposes one `--jumphost`; it rejects a proxy because it cannot
hand a proxy to its ET socket. A pure-Go client using the Connection Manager's
SSH path does not have those limitations: the SSH connection can use multiple
jumps and a proxy, and the ET TCP channel is a `direct-tcpip` channel through
the final SSH connection. This is a product opportunity, not a protocol
requirement.

**[Estimate]** For the first migration, preserve the current typed restrictions
(one jump, no proxy) behind the existing `CheckRoute` so behavior and error
messages do not change. Then, as a deliberate follow-up, lift them once the
Connection Manager path and direct-tcpip behavior are proven. Do not silently
change routing semantics in the same change that removes the bundled binary.

The current `Hop.ETPort` (the ET port on the jump host) becomes unused if the
client dials the target through the final SSH connection. Keep the field for
saved-Host compatibility or deprecate it with a migration; do not reinterpret
it silently. If a deployment has `etserver` bound only on a non-loopback
address, or runs it on a different host than the SSH target, add an explicit ET
address/port override rather than guessing.

### 7.5 Security

- Generate id/passkey with `crypto/rand`; never use `math/rand`.
- v6: treat the id as a capability that is sent in clear and use a long random
  passkey; be aware that a known id can displace a session. v7 challenge is the
  fix; implement it before relying on reattach or on hostile multi-tenant
  networks.
- Never log the passkey, the bootstrap command line, or the `IDPASSKEY` line.
  Scrub them from error output.
- Keep credentials in the Vault/keychain; do not write a temporary SSH home,
  askpass map, or key files for ET. The SSH layer's existing secret handling is
  sufficient.
- Enforce host-key verification through the Connection Manager/Vault. The
  current `StrictHostKeyChecking=accept-new` snapshot behavior should not be
  weakened by the rewrite.
- Treat all server-provided protobuf fields as untrusted: bound lengths,
  validate packet types, reject huge catch-up buffers, and avoid panics on
  malformed data.
- If v7 is implemented, verify `resetProof` and `resetSalt` exactly; do not
  accept a reset decision that the authenticated handshake did not select.

### 7.6 Removing the bundle

**[Estimate]** Do the migration behind a feature flag or a build tag:

1. Land the pure-Go transport with parity tests against stock `etserver`
   v6.2.10 and v7.1.1, while keeping the bundled adapter as a fallback.
2. Compare terminal output, resize, keepalive, reconnect, jump, known-host,
   Vault auth, cancellation, and error taxonomy on the supported desktop
   targets.
3. Switch the default to pure Go on platforms where it passes; keep the bundle
   only where a blocking gap remains.
4. Remove `adapters/et` process code, `adapters/bundle` ET manifests/assets,
   `resources/et`, the askpass flag/handler, and the ET bundle tests. Update
   `docs/parity/matrix.yaml` (`et.bundle`, `et.environment`, `et.session`) and
   `resources/THIRD-PARTY-NOTICES.md` in the same change.
5. Keep the Apache-2.0 license/NOTICE for any vendored `et-go`/upstream proto
   code.

### 7.7 Test plan

Unit tests:

- 8-byte host-order handshake and 4-byte network-order packet framing,
  including boundary lengths and malformed lengths.
- `secretbox` nonce direction and little-endian increment; cross-test against a
  known libsodium/upstream vector or an independent implementation.
- Connect status handling: NEW, RETURNING, INVALID_KEY, MISMATCHED_PROTOCOL,
  and v7 RETRY_LATER.
- Bootstrap marker parsing with shell noise, split reads, a missing trailing
  newline, and an old server that does not regenerate.
- TERM validation (`_` rejection), id/passkey generation, credential scrubbing.
- Packet routing: buffer, keepalive, info, close, exit status, unknown types.
- Resize mapping, initial size, 80x24 default, zero-size handling.
- Reconnect/catch-up: sequence preservation, 64 MiB limit, too-far-behind
  error, duplicate/out-of-order packets, backpressure.
- Cancellation and close: no goroutine leaks, lease release, channel close.

Integration/interop:

- Stock `etserver` v6.2.10 and v7.1.1 in a Linux CI container, with
  `etterminal`; run a new session, type input, resize, drop the TCP link, and
  resume in-process.
- Compare the pure-Go client against upstream `et` v6.2.10 and `et.rs`/`et-go`
  on the same server.
- v7.1.1: challenge handshake, `--attach`/reset, RETRY_LATER after an
  `etserver` restart, exit status, disconnect timeout.
- Legacy matrix: v6 client vs v7.1.1 server; v7.1.1 client vs v6.2.10 server;
  v5.1.10 must be rejected.
- Jump host: SSH jump chain + direct-tcpip; one jump and (if enabled) multiple
  jumps/proxy; target `etserver` only reachable from the target's loopback.
- Auth: password, key, certificate, agent, MFA, known-host mismatch; no
  temporary files left behind.
- Mobile (once MyGo terminal exists): iOS/Android network switch, background
  suspend/resume, process kill/relaunch (v7 only), memory/backpressure, 16 KB
  page build, app-size check.

Fuzzing and race:

- Fuzz ConnectResponse/protobuf parsing, catch-up buffers, and terminal packet
  payloads.
- `go test -race` for the transport, reconnect, and close paths.

## 8. Work breakdown and effort

All figures are **[Estimate]**, for one experienced Go engineer familiar with
SSH and terminal protocols, assuming a stock `etserver` is available and no
server-side ET changes are required. They exclude MyGo terminal mobile work.

### 8.1 Pure-Go parity client

| Work | With `et-go` fork | From scratch |
|---|---:|---:|
| Protocol core: protobuf, framing, secretbox, handshake, terminal packets | 1–1.5 w | 2–3 w |
| SSH bootstrap through Connection Manager; marker parsing; direct-tcpip | 0.5–1 w | 1 w |
| `Transport` adapter, lifecycle, resize, exit status, error taxonomy | 0.5–1 w | 1 w |
| Reconnect, catch-up, keepalive, backpressure, limits | 0.5–1 w | 1–1.5 w |
| Interop tests, jump/proxy, server matrix, fuzzing, race | 0.5–1 w | 1–1.5 w |
| Migration, bundle removal, docs, parity matrix | 0.5 w | 0.5–1 w |
| **Total** | **3–5 w** | **5–8 w** |

Add **1–2 engineer-weeks** for v7 challenge/reset/attach/exit-status and the
associated proof/rekey tests. Add **2–3 engineer-weeks** if port forwarding is
also in scope; it is not in Ticket 43's current terminal parity scope.

The `et-go` fork saves time because it already contains protocol 6, secretbox,
framing, reconnect/catch-up, backpressure, and a test server. The main work is
removing its external-`ssh` bootstrap and local-console assumptions, adding
Natcatty's lease/direct-tcpip integration, and hardening/verifying it.

### 8.2 Native C++ wrapper

| Work | Estimate |
|---|---:|
| Cross-compile OpenSSL, protobuf, libsodium, zlib, helpers; iOS/Android toolchains; static/shared variants | 3–6 w |
| C ABI wrapper: opaque handles, lifecycle, callbacks, exception containment | 2–4 w |
| Custom `Console` and likely `SocketHandler` bridging to the host | 3–5 w |
| MyGo packaging/signing changes for prebuilt frameworks/libraries or C++ sources | 3–6 w |
| iOS/Android integration, App Review, 16 KB/ABI, device testing, fallback | 3–6 w |
| **Total** | **12–24 w (3–6 months)**, high risk |

This estimate is optimistic because it assumes the wrapper can rely on the
host app for SSH and does not need to port `etserver`/`etterminal`. If MyGo
does not accept prebuilt libraries, the platform work alone can exceed the
whole pure-Go estimate.

### 8.3 Swift/Kotlin reimplementation

**[Estimate]** 16–30 engineer-weeks, two independent protocol implementations,
duplicated crypto/reconnect tests, and no shared code with desktop. Not
recommended.

### 8.4 Recommended sequencing

1. **Protocol spike (1 week):** prove pure-Go connect/bootstrap/terminal I/O
   against a stock `etserver` v6.2.10 in CI; decide whether to fork `et-go`.
2. **Desktop parity (2–3 weeks):** transport, Connection Manager integration,
   reconnect, tests, feature flag.
3. **v7 capability (1–2 weeks):** challenge, reset/attach, exit status,
   RETRY_LATER, tests.
4. **Bundle removal + mobile readiness (1–2 weeks):** remove process code and
   assets, update parity docs, verify MyGo terminal mobile availability; if
   absent, pause mobile UI work and track the MyGo dependency.

## 9. Answers

### 9.1 What must an ET client implement?

**[Verified]** At minimum:

1. **SSH bootstrap:** generate 16-char id and 32-char passkey; run the
   `echo '<id>/<passkey>_<TERM>' | etterminal --verbose=N` command over SSH;
   parse `IDPASSKEY:<id>/<passkey>`; handle server regeneration and old servers;
   enforce the no-underscore TERM rule.
2. **SSH authentication:** password, key, certificate, agent, MFA, known-host
   verification, jump/proxy as configured. ET itself does not authenticate the
   user.
3. **ET connect:** TCP to `etserver` (default 2022); 8-byte host-order length +
   `ConnectRequest{clientId,version}`; handle NEW/RETURNING/INVALID_KEY/
   MISMATCHED_PROTOCOL; v7 challenge if supported.
4. **Crypto/framing:** libsodium-compatible secretbox with the 32-byte passkey,
   24-byte nonce with direction byte, little-endian increment; 4-byte
   network-order frames; 2-byte packet header; encrypt/decrypt payloads.
5. **Initial payload:** send `INITIAL_PAYLOAD` first; wait for
   `INITIAL_RESPONSE`; propagate server errors.
6. **Terminal I/O:** input -> `TERMINAL_BUFFER`; output -> terminal; resize ->
   `TERMINAL_INFO{row,column,width,height}`; keepalive every 5 seconds and
   reconnect if the echo is missed.
7. **Reconnect:** retry every second with the same id; handle RETURNING_CLIENT;
   exchange `SequenceHeader` and `CatchupBuffer`; preserve sequence/nonce
   state; enforce the 64 MiB replay bound; end on INVALID_KEY.
8. **Session lifecycle:** remote shell exit, optional v7 exit status/close,
   optional v7 disconnect timeout; no scrollback protocol request; one client
   per id.
9. **Jump host:** either upstream's two-SSH relay (`--jumphost`/`--jport`) or,
   preferably for Natcatty, a `direct-tcpip` channel through the existing SSH
   chain.
10. **Optional features not in Ticket 43 scope:** forward/reverse/dynamic port
    forwarding, SSH-agent forwarding, `-N` no-terminal mode, v7 no-PTY/command
    modes.

### 9.2 Can upstream ET run in-process on iOS and Android inside a MyGo app?

**No, not as-is.** **[Verified]** reasons:

- The client `fork`/`exec`s the system `ssh`; iOS forbids child processes
  (Apple DTS 747499).
- The client's local `Console` is a PTY/termios terminal, not a mobile terminal
  stream; a custom `Console` is possible but is not provided as a public,
  stable embedding API.
- There is no shared library target, no exported header set, no `extern "C"`,
  and no mobile toolchain/build in upstream.
- The C++ dependency set (OpenSSL, protobuf, libsodium, zlib, helpers) has no
  supported iOS/Android build here.
- MyGo's phone native pipeline compiles C, not C++, and rejects prebuilt
  libraries; it has no documented path for a C++ static/dynamic ET library.
- Android could technically spawn processes, but an ordinary app has no system
  `ssh` and no terminal environment; this is not a practical library route.

A C++ wrapper is technically possible because `TerminalClient` accepts a
custom `Console` and the id/passkey, and issue #779 proves the seam. It is not
a ready-made integration; it is a new mobile C++ project with a C ABI, a
dependency cross-build, a host SSH bridge, and MyGo packaging changes.

### 9.3 Can it be built as a static/dynamic library with a stable C ABI?

**[Verified]** Today: no. Upstream builds static `et-lib` and
`TerminalCommon`, but they are not an embedding API; there is no shared target,
no installed headers, no C ABI, and no mobile build.

**[Estimate]** A wrapper could be built by adding a C ABI layer and a custom
`Console`/`SocketHandler`, but it would still need all dependencies
cross-compiled and a MyGo packaging path. A dynamic-library route additionally
needs an iOS framework (signed with the app) and Android `.so` per ABI with
16 KB alignment. This is a 12–24 engineer-week project with high risk, not a
build-flag change.

### 9.4 What is the recommended path and effort?

**Pure-Go ET client over Natcatty's SSH Connection Manager**, exposing the
existing `Transport`, with MyGo's terminal connected via `Options.Conn`.
**3–5 engineer-weeks** with an `et-go` fork, **5–8** from scratch, plus **1–2**
for v7. This is the only path that removes the bundled binary and works on
desktop, iOS, and Android with one implementation, subject to MyGo shipping a
mobile terminal.

### 9.5 Session resume, scrollback, and multiple clients

- In-process reconnect: supported by ET's sequence/catch-up protocol; must be
  owned by the transport.
- Fresh-process resume: v6 is unsafe because nonce/sequence state is lost; v7
  `--attach`/reset makes it safe but does not restore the screen. Protocol 6
  stock servers cannot do it.
- Scrollback: client-side only; no protocol request; catch-up covers only
  recent packets (64 MiB), not arbitrary history.
- Multiple clients: not supported for one id; a second client displaces the
  first. Multiple sessions need distinct ids.
- Jump host: upstream supports one relay jump; Natcatty currently exposes one;
  a pure-Go client can do more via SSH `direct-tcpip`.

### 9.6 Interoperability

- Protocol 4 (v5.1.x) is incompatible with protocol 6.
- v6.0.1–v6.2.11 and v7.0.0–v7.1.1 all use protocol 6.
- A protocol-6 legacy client works for normal sessions against all protocol-6
  servers, including v7.1.1, but loses v7 challenge/reset/attach/exit-status.
- A v7.1-style client works normally against v6 servers via the legacy
  handshake, but `--attach` fails against v6 and v7.0.0.
- v7.1.1 adds the challenge/reset/attach/exit-status path; v7.0.0 does not
  have those proto fields.

### 9.7 Existing clients and licenses

`et-go` is the closest Go base (Apache-2.0, protocol 6, MVP, vendored/fork
required, no v7). `et.rs` is a strong Rust conformance oracle (Apache-2.0
metadata, no top-level LICENSE file, not embeddable). `AArnott` is a compact
Rust MVP/reference (MIT). Upstream is the Apache-2.0 reference but a process,
not a library.

### 9.8 What still blocks mobile?

The ET client itself can be pure Go and mobile-safe. The remaining hard
dependency is **MyGo's terminal on phones**: at PR 940 head, the terminal plugin
has only desktop `libghostty-vt` builds and is absent from the mobile plugin
list. ET-on-mobile is therefore blocked on the MyGo mobile-runtime ticket until
that terminal support exists. This should be reflected in scheduling and in the
product parity claims.

## 10. Unknowns / needs verification

These are open items, not conclusions:

1. **MyGo terminal mobile support at PR 940 head.** The manifest and plugin
   list indicate no mobile terminal. Confirm whether a later MyGo commit or the
   mobile-runtime ticket adds a mobile `libghostty-vt`/VT renderer, and at what
   cost/size. This is the largest product-level unknown.
2. **MyGo C++/prebuilt-library support.** Confirm whether any MyGo path can
   package a prebuilt C++ static/shared library or framework without changing
   the runtime. The current docs say phones cannot take libraries built
   elsewhere and only document C sources. Do not assume a workaround.
3. **Apple App Review treatment of a bundled C++ ET framework.** Apple's
   written rules allow bundled, signed code and do not ban `dlopen`, but App
   Review has final authority. A pure-Go client avoids this risk; a wrapper
   should get a review/precedent before committing.
4. **v7 screen behavior on attach.** ET has no screen snapshot; confirm how
   much a shell/full-screen app redraws after `--attach` and whether Natcatty
   should implement a local snapshot/replay (MyGo terminal `Mirror`/`Snapshot`
   or a bounded raw-output replay). This affects the mobile relaunch UX.
5. **Stock server versions in the field.** The interop matrix covers tags, but
   actual deployments may be older protocol 4 or v6 builds. Add a version probe
   and clear `MISMATCHED_PROTOCOL` error; do not assume v7 features.
6. **`OpenExec` semantics through Natcatty's Connection Manager.**
   Confirm that it provides stdin for the `echo ... | etterminal` command,
   returns stdout without a PTY, handles etterminal's daemonization/exit, and
   allows a concurrent `direct-tcpip` channel on the same lease. The Mosh
   bootstrap is strong evidence, but ET-specific integration tests are needed.
7. **Direct-tcpip availability and address.** Some SSH servers disable
   `AllowTcpForwarding`; some deployments bind `etserver` to a non-loopback
   address or run it on a different host. Decide the default ET address
   (`127.0.0.1` vs SSH hostname) and add an explicit override if needed.
8. **Jump-host semantics after the rewrite.** Upstream's `--jumphost`/`--jport`
   relay and a direct-tcpip-through-SSH design are not identical. Confirm the
   product wants direct-tcpip behavior (including multiple jumps/proxy) and how
   to migrate saved `Hop.ETPort` values.
9. **iOS/Android background behavior.** Verify iOS suspension/termination and
   Android Doze/foreground-service behavior on real devices. ET's reconnect
   covers network drops, not OS process death; v7 attach is the only safe
   fresh-process path.
10. **16 KB page deadline and ABI policy.** The Android page-size policy is
    evolving. Re-check the current Google Play deadline and target-SDK
    requirements before shipping; ensure any prebuilt library is 16 KB aligned
    and all required ABIs are present.
11. **`et.rs` license file.** Cargo declares Apache-2.0, but no top-level
    LICENSE was found. If Natcatty wants to vendor any part, clarify the
    license with the author; for now use it only as a test oracle.
12. **`et-go` maintenance.** It is an MVP with no releases; a fork needs
    ownership, upstream tracking, and its own test matrix. Confirm the
    maintainer's intent or budget for a vendored fork.
13. **Performance on mobile.** Benchmark Go `secretbox`, protobuf, and
    high-throughput terminal output on representative iOS/Android devices;
    terminal traffic is usually low, but large pastes and `cat` output should
    be measured.
14. **Secure persistence of the session passkey.** v7 attach needs the
    id/passkey after process death. Decide whether to store it in the Vault,
    Keychain/Keystore, or not at all; define retention and deletion. Do not put
    it in the existing restore payload if that payload is not secret.
15. **Exact v7.1.0 vs v7.1.1 feature split.** v7.1.1 is verified to have
    challenge/reset/attach/exit-status; v7.0.0 is verified not to. Confirm
    v7.1.0's proto/behavior before claiming support for it; the safe matrix is
    v6.x and v7.1.1.
16. **Sources not executed here.** No stock `etserver`, iOS/Android device,
    Go toolchain, protobuf compiler, or upstream build was available in this
    research environment. Protocol claims are source/documentation-verified,
    not end-to-end measured by the author of this document. The existing
    Natcatty tests and the `et-go`/`et.rs` projects provide independent
    evidence, but a prototype must prove the integration.

## 11. References

Upstream ET:

- Repository: <https://github.com/MisterTea/EternalTerminal>
- v6.2.10 tag/commit: <https://github.com/MisterTea/EternalTerminal/tree/et-v6.2.10>
  (`f9a584ac06b2f1730b5bdf0a27150f28478368fb`)
- v6.2.11: <https://github.com/MisterTea/EternalTerminal/tree/et-v6.2.11>
  (`7f566c0f3504e32554d98e71e573976255af91fb`)
- v7.0.0: <https://github.com/MisterTea/EternalTerminal/tree/et-v7.0.0>
  (`7656a32a5bc15c6746726a27a5a4ba1e468fab6e`)
- v7.1.1: <https://github.com/MisterTea/EternalTerminal/tree/et-v7.1.1>
  (`a8318ca4598756ca3c5f4711cce12a65faeff57d`)
- v5.1.10: <https://github.com/MisterTea/EternalTerminal/tree/et-v5.1.10>
  (`e5af047ad4aa5bfe22c9bdf02b9ef24d03cee4b5`)
- v6.0.1: <https://github.com/MisterTea/EternalTerminal/tree/et-v6.0.1>
  (`6d1620a561349d4745e3f794759e6fb653023628`)
- Protocol document: <https://github.com/MisterTea/EternalTerminal/blob/et-v7.1.1/docs/protocol.md>
- Source files: `src/base/{Headers,SocketHandler,Packet,CryptoHandler,BackedReader,BackedWriter,Connection,ClientConnection,ServerConnection,ServerClientConnection,SubprocessToString,TcpSocketHandler}.{hpp,cpp}`,
  `src/terminal/{TerminalClient,TerminalServer,TerminalMain,SshSetupHandler,UserTerminalHandler,UserJumphostHandler,Console,PsuedoTerminalConsole}.{hpp,cpp}` at the tags above.
- Protos: `proto/ET.proto`, `proto/ETerminal.proto` at the tags above.
- Build: `CMakeLists.txt`, `vcpkg.json`, `.gitmodules`, `build_static.sh`.
- License: <https://github.com/MisterTea/EternalTerminal/blob/et-v6.2.10/LICENSE>
- Issue #779 (custom `Console`): <https://github.com/MisterTea/EternalTerminal/issues/779>
- Issue #452 (protocol/other-language discussion): <https://github.com/MisterTea/EternalTerminal/issues/452>

Existing clients:

- `et-go`: <https://github.com/tphakala/et-go/tree/bc33dd76b558418bc1079ba27c8c477b8145eea5>
- `et.rs`: <https://github.com/minpeter/et.rs/tree/b62c79afe4599bc5e9db2bfd68a78651f5cab979>
- `AArnott/eternal-terminal-client`: <https://github.com/AArnott/eternal-terminal-client/tree/26344d1ddc9537a168317df0080d688881c3a91a>

MyGo:

- PR 940: <https://github.com/egoist/mygo/pull/940>
- PR-head docs: <https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md>
- PR-head native compiler: <https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_natives.go>
- PR-head terminal plugin: <https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal>
- PR-head `native.go`: <https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/native.go>

Apple:

- App Store Review Guidelines 2.5.2: <https://developer.apple.com/app-store/review/guidelines/#software-requirements>
- DTS 747499 (child processes): <https://developer.apple.com/forums/thread/747499>
- DTS 130502 (`dlopen`/frameworks): <https://developer.apple.com/forums/thread/130502>

Android:

- NDK ABIs: <https://developer.android.com/ndk/guides/abis>
- 16 KB page sizes: <https://developer.android.com/guide/practices/page-sizes>
- 64-bit requirement: <https://developer.android.com/google/play/requirements/64-bit>

Natcatty (local repository, HEAD `240c8c6aa6790f26c447d6761b0037a28c753f9f`):

- `application/session/session.go`, `application/session/conn.go`
- `application/connection/manager.go`
- `adapters/ssh/terminal.go`
- `adapters/et/et.go`
- `domain/model/et/et.go`
- `adapters/bundle/manifests.go`
- `resources/et/README.md`
- `app/transports.go`
- `ui/session/surface.go`
- `go.mod`
- `docs/parity/matrix.yaml` (`et.bundle`, `et.environment`, `et.session`)

## 12. Verification note

The upstream tags and source text were read directly from the repository at the
cited commits. The Natcatty files were read in the research worktree at
`240c8c6aa6790f26c447d6761b0037a28c753f9f`. The MyGo PR-head files were read at
`7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a`; PR status was open/unmerged at the
time of research. No stock `etserver`, mobile device, Go compiler, protobuf
compiler, or upstream build was available, so the effort figures and some
runtime claims remain estimates and should be validated by the spike in
section 8.4.
