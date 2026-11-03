# Mosh client protocol and existing implementations

Status: research for Ticket 43 / MyGo PR 940  
Date: 2026-11-03  
Scope: what a clean-room pure-Go Mosh client must implement, what is separable, what already
exists, how versions and platforms interoperate, and a rough engineering estimate. GPL/App
Store legal analysis is explicitly out of scope; a separate ticket owns it. This note records
license facts it encountered but draws no legal conclusion.

**Evidence labels used below**

- **Source** — read in the pinned upstream or implementation source at the version named.
- **Vendor claim** — the project's own documentation or test claim; not independently rerun here.
- **Estimate** — engineering judgement, not measured.

Source versions are pinned in section 1.0. All URLs are cited with a tag or commit.

---

## Answers

1. **What must a clean-room Go client implement for stock interop?**
   A network stack that speaks Mosh protocol v2 over UDP: 22-character Base64 key parsing,
   AES-128-OCB3 with a 12-byte nonce and 16-byte tag, the 8-byte direction/sequence header,
   a 4-byte big-endian timestamp header, zlib-compressed proto2 `TransportInstruction`
   fragments, the SSP state machine (state numbering, ACK, retransmit, throwaway, shutdown),
   the client `UserMessage` (keystrokes plus resize), and the server `HostMessage`
   (`HostBytes` ANSI output, resize, EchoAck). It must send its initial terminal size in the
   first state and send the shutdown marker on exit. The SSH bootstrap and `MOSH CONNECT`
   parsing are outside UDP; this app already implements them.

2. **What is separable?**
   Prediction/local echo is entirely client-side and is not on the wire; a client without it
   still interoperates, it only feels laggier. The local terminal emulator and renderer are
   separable if the app already has a VT emulator that can consume the ANSI `HostBytes`
   stream (MyGo's terminal plugin is such an emulator). Truecolor, OSC 52 clipboard, OSC 8
   hyperlinks, notifications, title handling, terminfo/smcup, roaming, port hopping, ECN,
   IPv6, and prospective-resend optimizations are parity/robustness layers, not requirements
   for the basic handshake and state exchange. The one caveat is that full prediction parity
   and exact terminal semantics need a shadow framebuffer or an emulator API that exposes
   cursor/cell state; a pure ANSI passthrough is not enough for those.

3. **What already exists?**
   Upstream C++ Mosh 1.4.0 (GPL) is the mature reference. MoshCatty 0.1.8 (Rust, GPL) is a
   full-featured client used by this app's current bundle. `unixshells/mosh-go` (MIT, pure Go,
   client and server) is the closest permissive Go implementation, but it is intentionally
   simplified and has concrete transport/parity gaps. `bu/moshgo` (GPL, client-only) is a
   work-in-progress with a useful byte-level `SPEC.md`. `Arunjay4213/mosh-rs` (Rust, GPL,
   crates.io `mosh` 0.1.3) is early. The Blink iOS fork (GPL) proves iOS integration but is
   an old C fork. No mature, permissively licensed, full-parity pure-Go client exists.

4. **Primary sources and gaps.**
   The protobuf schemas, upstream C++ source, man pages, 1.4.0 release notes, RFC 7253, and
   the 2012 Mosh paper are the primary sources. There is no formal byte-level Mosh wire/SSP
   specification. OCB3 has a formal RFC and test vectors; Mosh ships OCB tests from
   draft-krovetz-ocb-03, a Base64 vector table, and a nonce-uniqueness test, but no golden
   encrypted datagrams or SSP conformance corpus. A clean-room project should create its own
   specification and a packet/trace conformance harness against stock servers.

5. **Interop.**
   `MOSH_PROTOCOL_VERSION = 2` in 1.2.2, 1.3.2, 1.4.0, and master. The 1.3.2 release notes
   explicitly claim backward compatibility with clients back to 0.96 and servers back to
   1.0.9. 1.4.0 stays on protocol v2 and adds truecolor, OSC 52, `--predict-overwrite`, and
   paste prediction suppression. Master (unreleased) adds OSC 8 hyperlinks and is still
   protocol v2. A client should target both stock 1.3.2 and 1.4.0 servers; 1.3.2 still ships
   in Debian 11 and Ubuntu 22.04.

6. **Effort.**
   Reusing the existing SSH bootstrap and a capable app terminal emulator, a protocol-complete
   MVP (no prediction parity, no roaming/port hopping) is roughly **6–12 engineer-weeks**;
   full stock-client parity is roughly **19–31 engineer-weeks** (about 4.5–7 engineer-months)
   for one senior Go engineer including tests. These are estimates with wide error bars; see
   section 7. The biggest cost driver is terminal/prediction semantics and interop QA, not
   OCB3 or SSP alone.

---

## 1. Protocol reference

### 1.0 Pinned sources

| Source | Version / commit | Used for |
|---|---|---|
| `mobile-shell/mosh` | tag `mosh-1.4.0`, commit `094fdba5ca7566efd98e1c36fb2295ef508c145a` | Reference client/server, proto files, tests, man pages |
| `mobile-shell/mosh` | tag `mosh-1.3.2`, commit `6852787f213de885b9e565535f22681de5e98f94` | Older server/client behavior and compatibility notes |
| `mobile-shell/mosh` | tag `mosh-1.2.2`, commit `bb07e52193bb2edc363912fc2d70a9cdcd530185` | Protocol-version history |
| `mobile-shell/mosh` | master `decd9b705eb81626f694335b8d5940538beb06da` (2026-03-22) | Unreleased changes (OSC 8) |
| `binaricat/MoshCatty` | release `moshcatty-0.1.8`, commit `554b9d305e7ac4b11de740d764bbc3e05f816d7b` | Existing bundled client and vendor audit |
| `unixshells/mosh-go` | tag `v0.5.2`, commit `0284bd652aab3dfff5b0b74945c23901d7537b61`; master `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f` | Closest pure-Go implementation |
| `bu/moshgo` | commit `69ac65e63167180122ca2c264c01fb618a572f63` | Alternative Go WIP and spec |
| `Arunjay4213/mosh-rs` | crates.io `mosh` 0.1.3, 2026-10-14 | Rust implementation survey |
| `egoist/mygo` PR 940 | head `7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a` (open, updated 2026-11-02) | Mobile runtime context |
| Repology | fetched 2026-11-03 | Distribution versions |

Upstream references below use the tag URL `https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/...`;
the tag resolves to the commit above. MoshCatty references use the release tag; mosh-go
references use the commit hash because the interesting behavior is on master, not only v0.5.2.

### 1.1 Protocol version and release differences

- `MOSH_PROTOCOL_VERSION = 2` is defined at
  [network.h L52](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L52).
  The same value is present in 1.3.2 and 1.2.2; master still has it at
  [network.h L53](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/network/network.h#L53).
- The receiver throws on mismatch:
  [networktransport-impl.h L79-L81](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h#L79-L81).
- 1.3.2's release announcement says it is "backwards-compatible with mosh-clients back to
  0.96 and mosh-servers back to 1.0.9":
  [mosh-1.3.2 release](https://github.com/mobile-shell/mosh/releases/tag/mosh-1.3.2)
  (published 2017-07-22).
- 1.4.0 (published 2022-10-27) lists true color, OSC 52 clipboard, `--predict-overwrite`,
  no prediction on large pastes, syslog, and other fixes:
  [mosh-1.4.0 release](https://github.com/mobile-shell/mosh/releases/tag/mosh-1.4.0).
  These are terminal/client features; the wire protocol is still v2.
- Master adds OSC 8 hyperlink rendering in the terminal layer (for example
  [terminaldisplay.cc L275-L276](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L275-L276)
  and [L533-L538](https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/src/terminal/terminaldisplay.cc#L533-L538)).
  It is unreleased as of 2026-11-03; do not treat it as required for 1.4.0 parity.
- 1.4.0's `Renditions` uses 25-bit truecolor values (mask `0x1000000` plus RGB) at
  [terminalframebuffer.h L59-L77](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminalframebuffer.h#L59-L77);
  1.3.2 uses 12-bit foreground/background fields at
  [terminalframebuffer.h L58-L60](https://github.com/mobile-shell/mosh/blob/mosh-1.3.2/src/terminal/terminalframebuffer.h#L58-L60).
  This is why a 1.3.2 client may not render a 1.4.0 server's truecolor output correctly even
  though the transport interoperates.

### 1.2 SSH bootstrap and `MOSH CONNECT` (outside the UDP protocol)

The stock wrapper is `scripts/mosh.pl`. Its sequence, verified in source:

1. It invokes SSH with `-n`, and adds `-tt` when a PTY is used:
   [mosh.pl L358-L361](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L358-L361).
2. The remote command is `mosh-server new`, plus `-c COLORS`, `-s` or `-i IP`, optional
   `-p PORT[:PORT2]`, one `-l NAME=VALUE` per locale variable, and optional `-- command`:
   [mosh.pl L377-L393](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L377-L393).
3. `mosh-server` chooses a 128-bit key, binds a UDP port, and prints on a fresh line:
   `MOSH CONNECT <port> <key>`:
   [mosh-server.cc L435-L441](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L435-L441).
   The key is the 22-character unpadded Base64 form of 16 bytes; the man page calls it a
   22-byte Base64 session key:
   [mosh-client.1](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-client.1).
   The wrapper's regex accepts exactly 22 `[A-Za-z0-9/+]` characters, optionally followed by
   whitespace:
   [mosh.pl L429-L433](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L429-L433).
   The server's own port range is 60001–60999:
   [network.h L136-L137](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L136-L137).
   The man page says 60000–61000, which is a documentation/implementation discrepancy:
   [mosh-server.1 L41-L42](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-server.1#L41-L42).
   `-p 0` asks the operating system to pick a port:
   [mosh-server.1 L73-L75](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-server.1#L73-L75).
4. The wrapper may print/parse `MOSH IP <address>` (when resolving locally) or ask the remote
   shell to print `MOSH SSH_CONNECTION <client-ip> <client-port> <server-ip> <server-port>`;
   it uses the server IP so the UDP socket targets the same host the SSH connection reached:
   [mosh.pl L363-L370](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L363-L370),
   [mosh.pl L417-L447](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L417-L447).
5. It sets `MOSH_KEY` and execs `mosh-client <ip> <port>`:
   [mosh.pl L462-L465](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L462-L465).
   `mosh-client` refuses to run without `MOSH_KEY` and unsets it after reading:
   [mosh-client.cc L168-L184](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-client.cc#L168-L184).
6. The server waits 60 seconds for the first client state. If no remote state has arrived by
   then, it exits `No connection within 60 seconds`:
   [mosh-server.cc L682](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L682),
   [mosh-server.cc L914-L918](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L914-L918).
7. Locale variables are passed through `-l`. The server first adopts its native locale; only
   if that is not UTF-8 does it clear and apply client-supplied variables, then fail if it is
   still not UTF-8:
   [mosh-server.cc L330-L363](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L330-L363).
   The stock variable list has 15 names:
   [mosh.pl L470-L482](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L470-L482):
   `LANG`, `LANGUAGE`, `LC_CTYPE`, `LC_NUMERIC`, `LC_TIME`, `LC_COLLATE`, `LC_MONETARY`,
   `LC_MESSAGES`, `LC_PAPER`, `LC_NAME`, `LC_ADDRESS`, `LC_TELEPHONE`, `LC_MEASUREMENT`,
   `LC_IDENTIFICATION`, `LC_ALL`.
8. `-c COLORS` controls the `TERM` the server advertises to the remote program (256 colors
   becomes `xterm-256color`); the server also sets `NCURSES_NO_UTF8_ACS=1` and unsets `STY`:
   [mosh-server.cc L552-L569](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L552-L569).
   The client-side `TERM` is separate; this app passes `TERM=xterm-256color` and sets
   `MOSH_NO_TERM_INIT=1` because its terminal already owns screen initialization:
   [adapters/mosh/mosh.go L303](adapters/mosh/mosh.go),
   [domain/model/mosh/mosh.go L216-L249](domain/model/mosh/mosh.go).

What this means for the product: **the SSH bootstrap is not part of the clean-room UDP client**.
The repository already has the handshake parser, locale propagation, key redaction, and
`MOSH IP` handling in [domain/model/mosh/mosh.go](domain/model/mosh/mosh.go) and
[adapters/mosh/mosh.go](adapters/mosh/mosh.go). A pure-Go client can reuse that layer and
only replace the PTY launch with an in-process connection.

### 1.3 UDP datagram format

Every UDP datagram is:

```
 offset  size  field
 0       8     direction_seq, big-endian uint64
                 bit 63 = direction: 1 = server->client, 0 = client->server
                 bits 62..0 = sequence number
 8       n     AES-128-OCB3 ciphertext of the plaintext below, with a 16-byte tag appended
                 (empty AAD)
```

The plaintext inside the ciphertext is:

```
 offset  size  field
 0       2     timestamp, big-endian uint16 milliseconds
 2       2     timestamp_reply, big-endian uint16; 0xffff means "no timestamp to echo"
 4       ...   fragment bytes (or empty for a heartbeat/ack)
```

Sources: packet construction/parsing at
[network.cc L70-L100](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L70-L100);
encryption layout at
[crypto.cc L200-L250](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.cc#L200-L250).
The 8-byte sequence is the last 8 bytes of the 12-byte OCB nonce; the first 4 bytes are
implicit zero:
[network.h L105-L108](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L105-L108).
OCB adds 16 bytes; the network header adds 8 + 4 bytes, so the fixed overhead is **28 bytes**
before the fragment:
[network.h L205-L207](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L205-L207),
[crypto.h L148](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.h#L148).

The direction bit prevents replay/reflection: the receiver asserts that an incoming packet's
direction is the opposite of its own sending direction:
[network.cc L511-L513](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L511-L513).
The timestamp is a 16-bit millisecond clock; `timestamp_reply` echoes a previously received
timestamp so the peer can compute RTT. The 16-bit clock wraps about every 65.5 seconds; use
modular subtraction.

**MTU and sizes**

| Item | IPv4 | IPv6 | Source |
|---|---:|---:|---|
| IP MTU used by Mosh | 1280 | 1280 | [network.h L118-L131](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L118-L131) |
| IP+UDP header guess | 28 | 64 (40 base + 16 extension + 8 UDP) | [network.h L104-L116](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L104-L116) |
| UDP payload MTU (`Connection::MTU`) | 1252 | 1216 | [network.cc L200-L211](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L200-L211) |
| Compressed fragment payload capacity | 1214 | 1178 | MTU − 12 network header − 16 OCB tag − 10 fragment header |
| Crypto receive buffer | 2048 | 2048 | [crypto.h L146](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.h#L146) |

Mosh deliberately disables fragmentation/PMTUD discovery on the IPv4 socket and uses a
conservative 1280-byte MTU:
[network.cc L160-L163](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L160-L163).
A client that uses the common 1300-byte payload without subtracting these headers can emit
datagrams larger than the path MTU; see the mosh-go gap in section 4.3.

**Replay and out-of-order handling.** The receiver checks the sequence against an expected
counter. Packets with `seq < expected_receiver_seq` are not used for timestamp or address
targeting, but their payload is still returned to the fragment reassembler:
[network.cc L515-L519](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L515-L519).
The decrypt still happened first. A Go implementation should not simply discard older
sequence numbers before decrypt/reassembly; that loses fragments under reordering.

### 1.4 Fragments, zlib, and protobuf

A fragment is:

```
 offset  size  field
 0       8     instruction id, big-endian uint64
 8       2     high bit = final fragment; low 15 bits = fragment number
 10      ...   compressed bytes
```

Source: [transportfragment.cc L56-L89](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportfragment.cc#L56-L89).
The fragment id identifies one `TransportInstruction`; retransmissions of the same instruction
reuse it. `make_fragments` subtracts the 10-byte fragment header from the supplied MTU:
[transportfragment.cc L157-L195](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportfragment.cc#L157-L195).
The fragment contents are a slice of a zlib stream produced by `compress()`; the receive side
uses `uncompress()` with a 4 MiB buffer (`2048 * 2048`):
[compressor.h L41-L43](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/compressor.h#L41-L43),
[compressor.cc L41-L57](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/compressor.cc#L41-L57).

The decompressed payload is a proto2 `TransportBuffers.Instruction`:

| Field | Number | Type | Meaning |
|---|---:|---|---|
| `protocol_version` | 1 | uint32 | Must be 2 |
| `old_num` | 2 | uint64 | Base state this diff applies to |
| `new_num` | 3 | uint64 | New state number |
| `ack_num` | 4 | uint64 | Highest remote state received |
| `throwaway_num` | 5 | uint64 | Earliest state the peer may discard |
| `diff` | 6 | bytes | `HostMessage` or `UserMessage` bytes |
| `chaff` | 7 | bytes | Random padding; ignored semantically |

Source: [transportinstruction.proto L1-L18](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/protobufs/transportinstruction.proto#L1-L18).
The `diff` direction is fixed by role: server-to-client diffs are `HostBuffers.HostMessage`;
client-to-server diffs are `ClientBuffers.UserMessage`. See the schemas in
[hostinput.proto L1-L32](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/protobufs/hostinput.proto#L1-L32)
and [userinput.proto L1-L27](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/protobufs/userinput.proto#L1-L27).

`HostMessage` contains repeated `Instruction`s with extensions:
`hostbytes` = 2 (`HostBytes.hoststring`, field 4), `resize` = 3 (`width` field 5, `height`
field 6), and `echoack` = 7 (`echo_ack_num` field 8).
`UserMessage` contains repeated `Instruction`s with `keystroke` = 2 (`keys` field 4) and
`resize` = 3 (same width/height fields). Unknown extensions must be ignored for forward
compatibility; 1.4.0 explicitly ignores unknown renditions in the terminal layer.

`chaff` is generated by the sender but not interpreted:
[transportsender-impl.h L320-L334](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L320-L334).
A minimal client may send an empty `chaff`; it is not a requirement for interop.

### 1.5 SSP: state synchronization, ACK, retransmission, shutdown

SSP is the heart of Mosh and is not separately specified. The verified behavior is:

**State numbering and diffs**

- Both sides start with state 0. Each state is a full terminal state (server side) or a
  user-input stream (client side). A sender sends `old_num` (the base state the diff applies
  to), `new_num`, `ack_num`, `throwaway_num`, and the serialized difference.
- Server state is `Terminal::Complete`; its diff is a `HostMessage` built by
  `Complete::diff_from`:
  [completeterminal.cc L72-L97](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/completeterminal.cc#L72-L97).
- Client state is `UserStream`; its diff is a `UserMessage` with batched keystrokes and
  resize actions:
  [user.cc L61-L106](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/user.cc#L61-L106).
- A receiver drops a state whose `new_num` it already has, and ignores a state whose
  `old_num` is missing (it may be out of order or discarded):
  [networktransport-impl.h L88-L111](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h#L88-L111).
- It can hold up to 1024 received states. If the queue is over the limit, it refuses new
  states for 15 seconds (receiver quench):
  [networktransport-impl.h L113-L131](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h#L113-L131).
- `throwaway_num` tells the receiver the earliest state it must keep; states below it are
  deleted:
  [networktransport-impl.h L169-L183](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h#L169-L183).
- The sender keeps at most 32 sent states and, when over the limit, erases 16 from the middle
  of the queue:
  [transportsender-impl.h L227-L235](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L227-L235).
- ACK processing removes states strictly below the acknowledged number:
  [transportsender-impl.h L358-L380](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L358-L380).

**Timing and retransmission**

| Constant | Value | Source |
|---|---:|---|
| Send interval | `ceil(SRTT/2)`, clamped 20–250 ms | [transportsender.h L52-L53](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender.h#L52-L53), [transportsender-impl.h L72-L85](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L72-L85) |
| Delayed ACK | 100 ms after data | [transportsender.h L55](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender.h#L55) |
| Empty ACK interval | 3000 ms | [transportsender.h L54](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender.h#L54) |
| Active retry window | 10 s | [transportsender.h L57](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender.h#L57) |
| Client send-mindelay | 1 ms (stock client sets it) | [stmclient.cc L257](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc#L257) |
| Server send-mindelay | 8 ms default | [transportsender-impl.h L64](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L64) |
| RTO | `ceil(SRTT + 4*RTTVAR)`, clamp 50–1000 ms | [network.cc L623-L632](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L623-L632) |
| Initial SRTT / RTTVAR | 1000 ms / 500 ms | [network.cc L248-L249](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L248-L249) |
| Shutdown retries | 16 packets | [transportsender.h L56](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender.h#L56) |
| Shutdown give-up | 10 s | [transportsender-impl.h L382-L395](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L382-L395) |
| Receiver state queue | 1024 | [networktransport-impl.h L120](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h#L120) |
| Receiver quench | 15 s | [networktransport-impl.h L129](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h#L129) |
| Sent state queue | 32 | [transportsender-impl.h L231](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L231) |

The sender computes a diff from the assumed receiver state, sends it, and retransmits if it
does not receive an ACK. Delayed ACKs are triggered by data; empty ACKs keep the session
alive and let the peer prune. The send rate is tied to SRTT so Mosh does not fill network
queues; this is also why Ctrl-C remains responsive on slow links:
[transportsender-impl.h L88-L125](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L88-L125).

**Shutdown**

- The client starts shutdown on ESC `.`, EOF on stdin, or a signal:
  [stmclient.cc L484-L527](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc#L484-L527).
- While shutting down, the sender emits an empty-ACK state whose `new_num` is
  `UINT64_MAX` (all ones) and increments `shutdown_tries`:
  [transportsender-impl.h L205-L225](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L205-L225),
  [L248-L251](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L248-L251).
- It exits when the shutdown is acknowledged, when `SHUTDOWN_RETRIES` (16) or 10 seconds is
  exhausted, or when it receives and acknowledges the peer's shutdown:
  [transportsender-impl.h L382-L395](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h#L382-L395),
  [stmclient.cc L514-L527](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc#L514-L527).
- The server has the same shutdown paths, so a clean shutdown ends the remote process:
  [mosh-server.cc L880-L893](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L880-L893).

If a client simply closes its UDP socket without the shutdown state, the server does **not**
exit by default. `MOSH_SERVER_NETWORK_TMOUT` is unset by default and the server waits
indefinitely for the client to reappear; only a positive value sets a timeout:
[mosh-server.cc L381-L395](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L381-L395),
[mosh-server.1 L95-L106](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-server.1#L95-L106).
This makes shutdown a required behavior for a client that wants to avoid orphaned servers.

**Roaming, ECN, port hopping**

- The socket sets `IP_TOS` to `0x02` (ECN-capable transport only) and requests `IP_RECVTOS`:
  [network.cc L166-L179](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L166-L179).
- On ECN congestion, it lowers the echoed timestamp by 500 ms to make the peer slow down:
  [network.cc L525-L532](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L525-L532),
  [network.h L145](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L145).
- The server adopts a new source address after an authenticated packet with a higher
  sequence; the client opens additional local ports and hops every 10 seconds when no
  round trip has succeeded. Up to 10 ports stay open; old sockets are kept 60 seconds:
  [network.cc L117-L145](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L117-L145),
  [network.cc L424-L426](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L424-L426),
  [network.h L140-L143](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h#L140-L143).
  These are robustness features; a basic client can omit them and still exchange state.

### 1.6 AES-128-OCB3, nonces, and replay protection

- Key: 16 bytes, carried as 22 Base64 characters (or 24 with `==` padding). The man page
  describes a 128-bit AES key:
  [mosh-client.1](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-client.1).
- AEAD: OCB3 with AES-128, a 12-byte nonce, a 16-byte tag, and empty associated data. The
  nonce is 4 zero bytes followed by the 8-byte big-endian direction/sequence value:
  [crypto.cc L200-L250](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.cc#L200-L250).
- The wire form is the 8-byte nonce suffix followed by ciphertext and tag. The first four
  zero bytes are implicit.
- The sender refuses to encrypt more than `2^47` blocks with one key, because OCB's
  privacy/authenticity bound degrades as the square of the number of blocks; upstream kills
  the session after that:
  [crypto.cc L229-L248](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.cc#L229-L248).
- Replay protection is sequence-based plus the direction bit. The receiver also validates
  that the direction is the opposite of its send direction, which prevents an attacker from
  reflecting packets back to the sender:
  [network.cc L511-L519](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc#L511-L519).
- There is no rekeying inside Mosh and no forward secrecy beyond the SSH bootstrap. The key
  is a bearer secret; the stock client unsets the environment variable and the app redacts
  the `MOSH CONNECT` line from the terminal.

The normative algorithm is **RFC 7253**:
[RFC 7253](https://www.rfc-editor.org/rfc/rfc7253). Upstream's OCB test file cites
`draft-krovetz-ocb-03` Appendix A, not RFC 7253's Appendix A:
[ocb-aes.cc L178](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/tests/ocb-aes.cc#L178),
[L465](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/tests/ocb-aes.cc#L465).
A clean implementation should test against RFC 7253 vectors and then cross-test against a
real mosh-server; do not assume upstream's vector file is an RFC vector file.

### 1.7 Terminal state, `HostBytes`, resize, and EchoAck

The server maintains a full terminal model (`Terminal::Complete`: UTF-8 parser, emulator,
framebuffer). The client does **not** receive a cell-grid diff. When the framebuffer changes,
`Complete::diff_from` calls `Display::new_frame(true, previous_fb, current_fb)` and puts the
resulting ANSI byte string in a `HostBytes` instruction:
[completeterminal.cc L82-L93](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/completeterminal.cc#L82-L93),
[terminaldisplay.cc L61-L150](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminaldisplay.cc#L61-L150).
If the size changed, it emits a separate `Resize` instruction as well; if the EchoAck changed,
it emits an `EchoAck` instruction. The client applies each instruction to its own `Complete`:
[completeterminal.cc L104-L122](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/completeterminal.cc#L104-L122).

Important consequences:

- `HostBytes` is an **ANSI delta stream**, not a serialized screen. The client must apply it
  in order to a terminal state that matches the state the server used as its base. If the app
  forwards it to an existing VT emulator, that emulator's behavior must match Mosh's
  `Display` closely enough for every sequence Mosh emits. Differences in wide-character
  width, combining marks, unknown SGR, or resize/reflow can cause visible drift.
- On a size change, `Display::new_frame` resets the scrolling region and emits
  `ESC[0m ESC[H ESC[2J`, then redraws:
  [terminaldisplay.cc L133-L149](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminaldisplay.cc#L133-L149).
  It also emits bell, title/icon OSC 0/1/2, clipboard OSC 52, reverse-video mode, cursor
  state, and cell SGR sequences:
  [terminaldisplay.cc L61-L150](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminaldisplay.cc#L61-L150).
- The 1.4.0 cell model supports truecolor (`0x1000000 | r<<16 | g<<8 | b`) and 25-bit
  foreground/background fields:
  [terminalframebuffer.h L52-L96](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminalframebuffer.h#L52-L96).
  1.3.2 has only 12-bit color fields, so truecolor is a server-1.4.0/client-1.4.0 feature.
- Terminal modes are part of the state, not a local preference. `Display::open` forces
  application-cursor mode (`ESC[?1h`), and the host-to-client diff emits cursor visibility,
  reverse video, bracketed paste (`?2004`), mouse reporting (`?1000`–`?1003`), focus events
  (`?1004`), and mouse encodings (`?1005`/`?1006`/`?1015`) when they change:
  [terminaldisplay.cc L48-L58](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminaldisplay.cc#L48-L58),
  [L280-L337](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminaldisplay.cc#L280-L337),
  [terminalframebuffer.h L277-L305](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminalframebuffer.h#L277-L305).
  An embedding terminal must honor these sequences, and the client must translate arrow keys
  using the state it has applied; otherwise mouse/paste/arrow behavior diverges from stock
  `mosh-client`.
- Resize from the client is a `UserMessage` instruction. The server applies only the last
  consecutive resize action to the PTY via `TIOCSWINSZ`:
  [mosh-server.cc L740-L764](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L740-L764).
  The server's next host state carries a `Resize` extension and a redraw.
- The client's first state should include its real terminal size. The stock client creates
  its local terminal with the window size and pushes `Parser::Resize` **before** the first
  network tick:
  [stmclient.cc L252-L260](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc#L252-L260).
  Without this, the server starts at its default 80x24 until a later resize arrives.
- EchoAck: the server records the input-frame number when it processes user input
  (`terminal.register_input_frame(last_remote_num, now)`) and, after a 50 ms echo timeout,
  emits the newest input frame that has had time to echo:
  [mosh-server.cc L767-L770](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L767-L770),
  [completeterminal.h L60](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/completeterminal.h#L60),
  [completeterminal.cc L130-L160](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/completeterminal.cc#L130-L160).
  This is primarily for prediction confirmation; a basic client can ignore it.

For a pure-Go client the terminal layer is therefore separable into two levels:

1. **Display-only**: hand `HostBytes` to the app's terminal emulator and send user input
   bytes back. This is enough to see the remote screen, provided the emulator applies ANSI
   faithfully.
2. **Stock-comparable**: maintain a shadow `Complete`/framebuffer so prediction, EchoAck,
   cursor geometry, and server-authoritative resize behave like the C++ client. This is the
   expensive part.

### 1.8 Locale and environment propagation

- Both ends must use a UTF-8 locale. The wrapper forwards the 15 locale variables above; the
  server prefers its own UTF-8 locale and only applies client values if its native locale is
  not UTF-8:
  [mosh-server.cc L330-L363](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L330-L363).
- UDP carries no environment. Locale, `TERM`, and `COLORS` are established at bootstrap.
- The server sets the child `TERM` from `-c` and `NCURSES_NO_UTF8_ACS=1`; the local client
  uses its own `TERM` for terminfo:
  [mosh-server.cc L552-L569](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L552-L569).
- `MOSH_NO_TERM_INIT` suppresses the client's alternate-screen initialization when an
  embedding terminal already owns it:
  [mosh.pl L462-L465](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L462-L465).
  This app sets it in [adapters/mosh/mosh.go L303](adapters/mosh/mosh.go).
- The app's current handoff is already stock-shaped: `DefaultLang = "en_US.UTF-8"`, 15 locale
  names, `-l` arguments, and `MOSH_KEY`/`LANG`/`TERM` client environment:
  [domain/model/mosh/mosh.go L25-L31](domain/model/mosh/mosh.go),
  [L154-L199](domain/model/mosh/mosh.go),
  [L216-L249](domain/model/mosh/mosh.go).

### 1.9 Prediction and local echo

Prediction is a **client-only overlay**. The server never sees it. Every keystroke is still
sent in the `UserMessage`; prediction only changes what the client displays while waiting for
confirmation.

Verified behavior in 1.4.0:

- Modes are `adaptive` (default), `always`, `never`, and an internal `experimental`; the
  wrapper maps `--predict` / `MOSH_PREDICTION_DISPLAY`:
  [mosh.1 L124-L135](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh.1#L124-L135),
  [mosh.pl L187-L210](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl#L187-L210).
- Prediction parses the same input stream through a terminal parser. Printable single-width
  characters are inserted at the predicted cursor; `0x7f` handles backspace; parser actions
  for left/right arrows are handled:
  [terminaloverlay.cc L652-L780](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.cc#L652-L780),
  [L843-L849](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.cc#L843-L849).
- Control characters and unknown-width characters make predictions tentative; wide characters
  are not predicted (`/* XXX handle wide characters */`):
  [terminaloverlay.cc L746-L749](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.cc#L746-L749).
  The last column is special-cased as tentative because shells and editors differ:
  [terminaloverlay.cc L758-L762](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.cc#L758-L762).
- 1.4.0 supports overwrite prediction (`--predict-overwrite`) and suppresses prediction when
  a large paste is detected:
  [mosh.1 L146-L151](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh.1#L146-L151),
  [mosh-1.4.0 release](https://github.com/mobile-shell/mosh/releases/tag/mosh-1.4.0).
- Adaptive display has SRTT and glitch thresholds: predictions appear when SRTT is above
  ~20–30 ms, flagging starts around 50–80 ms, an outstanding prediction older than 250 ms is
  a glitch, and predictions older than 5000 ms are underlined:
  [terminaloverlay.h L203-L213](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.h#L203-L213).
- Predictions are confirmed against the framebuffer or killed by epoch; EchoAck helps the
  client learn when a sent input frame is old enough to have been echoed:
  [terminaloverlay.h L150-L152](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.h#L150-L152),
  [stmclient.cc L298-L300](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc#L298-L300).

Prediction is therefore the clearest "UX layer" in the protocol. It can be omitted from a
first pure-Go client without breaking interop, but full stock parity requires porting the
rules above, not just echoing printable characters.

### 1.10 Liveness, timeouts, and clean exit

- Initial server wait: 60 s for the first authenticated client (1.4.0):
  [mosh-server.cc L682](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L682),
  [L914-L918](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc#L914-L918).
- After connection, the server waits indefinitely by default for the client to return;
  `MOSH_SERVER_NETWORK_TMOUT` can impose a positive timeout:
  [mosh-server.1 L95-L106](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-server.1#L95-L106).
- The client detects a silent server from its own timers and shows a notification; it starts
  shutdown on EOF/signal/escape:
  [stmclient.cc L484-L527](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc#L484-L527).
- Clean shutdown uses the `UINT64_MAX` empty-ACK state described in section 1.5. Without it,
  the remote `mosh-server` process stays around until the shell exits or an external timeout
  kills it.

---

## 2. Required vs separable

| Component | Required for stock interop? | Why | Can be deferred? |
|---|---|---|---|
| SSH bootstrap and `MOSH CONNECT` | Yes, but outside UDP | Establishes key, port, IP, locale | Already implemented in this app |
| Base64 key parsing and AES-128-OCB3 | Yes | Every datagram is encrypted/authenticated | No |
| 8-byte direction/sequence + timestamp header | Yes | Nonce, replay, RTT, direction | No |
| zlib + proto2 `TransportInstruction` + fragment header | Yes | Carries SSP state | No |
| SSP state numbering, ACK, retransmit, throwaway | Yes | Reliability over UDP | No |
| Protocol version = 2 | Yes | Receiver rejects mismatch | No |
| Initial resize in first state | Yes for correct dimensions | Server otherwise starts 80x24 | Can be added immediately after first state, but stock sends it first |
| `UserMessage` keystrokes | Yes | User input | No |
| `HostMessage` HostBytes apply | Yes | Remote screen | No |
| `HostMessage` Resize apply | Mostly | Server-authoritative size/redraw | Can ignore if local resize always drives; parity needs it |
| EchoAck apply | No | Prediction confirmation only | Yes for basic client |
| Shutdown marker | Yes for clean server exit | Otherwise server waits indefinitely | No, if orphan servers matter |
| Prediction | No | Client-only UX | Yes; full parity later |
| Local terminal emulator/renderer | No if app has one | `HostBytes` is ANSI | Yes if reusing app terminal |
| Shadow framebuffer | No if no prediction | Needed for prediction/echo/cursor parity | Yes for MVP |
| Truecolor / OSC 52 / OSC 8 | No for connection | Terminal-level output | Truecolor recommended; OSC later |
| Terminfo/smcup/rmcup, escape key, notifications, title prefix | No | Local UX | Yes in embedded UI |
| Roaming / port hopping / ECN | No for a stable network | Resilience | Yes for MVP; parity needs them |
| IPv6 | No on IPv4-only networks | Address family | Yes; parity needs it |
| Chaff / prospective resend / send pacing | No | Traffic shaping/optimization | Yes |
| Server implementation | Not needed | Product is a client | Out of scope |

**Minimum viable protocol client**: network stack + HostBytes pump to an ANSI terminal +
keystroke/resize writer + initial resize + clean shutdown. Prediction, shadow framebuffer,
roaming, truecolor-specific handling, and local terminal semantics are additive.

The important non-obvious separable boundary is **display generation**. Stock `mosh-client`
re-emulates `HostBytes` into its own `Complete` and then re-renders to stdout. A pure-Go
client that already owns a VT emulator can skip the second emulation and feed the ANSI stream
directly. That removes the largest piece of stock client code, but it also means the app's
emulator becomes part of the protocol's correctness surface. If that emulator differs from
Mosh's `Display`/`Emulator`, the screen can drift even though packets are correct.

---

## 3. Existing implementations

### 3.1 Upstream C++ Mosh

- Repository: [mobile-shell/mosh](https://github.com/mobile-shell/mosh).
- Version examined: tag `mosh-1.4.0`, commit `094fdba5ca7566efd98e1c36fb2295ef508c145a`.
- License: GPL-3.0-or-later (with an OpenSSL linking exception in source headers); the exact
  legal consequences are the separate ticket's job.
- Scope: full client and server, terminal emulator, prediction, OCB test suite, end-to-end
  tests. About 16.8k lines of `.cc`/`.h` under `src/` including tests (measured with `wc -l`).
- Maturity: the reference implementation. It is the oracle for protocol behavior.
- Use to us: read for protocol facts; run as a black-box interop server; do not copy code if
  the clean-room constraint is strict.

### 3.2 MoshCatty 0.1.8

- Repository: [binaricat/MoshCatty](https://github.com/binaricat/MoshCatty); tag
  [moshcatty-0.1.8](https://github.com/binaricat/MoshCatty/releases/tag/moshcatty-0.1.8),
  commit `554b9d305e7ac4b11de740d764bbc3e05f816d7b` (2026-07-17).
- License: GPL-3.0-or-later (`Cargo.toml`, README, and the release assets).
- Language/scope: Rust client library and `mosh-client` binary. It does not do SSH bootstrap;
  the caller supplies host, port, and `MOSH_KEY`. About 11.2k Rust lines under `src/`
  (including inline test modules), plus roughly 320 `#[test]` functions in `src/` and 16 in
  `tests/`.
- Current app relationship: the pinned bundle is MoshCatty 0.1.8. The adapter resolves and
  SHA-256-verifies the binary on every launch, never falls back to `PATH`, and launches it on
  a PTY with `MOSH_KEY`, `TERM`, locale, and `MOSH_NO_TERM_INIT`:
  [adapters/mosh/mosh.go](adapters/mosh/mosh.go),
  [adapters/bundle/bundle.go](adapters/bundle/bundle.go),
  [adapters/bundle/manifests.go](adapters/bundle/manifests.go).
- Vendor audit: the repository's
  [protocol audit](https://github.com/binaricat/MoshCatty/blob/554b9d305e7ac4b11de740d764bbc3e05f816d7b/docs/protocol-audit-2026-07-15.md)
  claims real interop against Ubuntu's official `mosh-server` 1.4.0 and 1.3.2, including
  10% loss/reorder/1% duplicate traffic, a 65-second blackout, port hopping, shutdown,
  terminal edge cases (wide characters, resize, clear/redraw), a 1,446,920-byte compressed
  instruction on a 1.3.2 server, and the 4 MiB state boundary. It also lists remaining release blockers: Windows/ConPTY
  manual testing and public IPv6 near-MTU/blackout testing. **This is a vendor claim; this
  note did not rerun those tests.**
- Engineering value: strong proof that a non-C full client is feasible and a good behavioral
  checklist. It is not a permissive dependency, and it is not a drop-in Go library.

**Provenance discrepancy found in this repo.** `resources/mosh/README.md` says
"MoshCatty's release workflow builds mosh from upstream `mobile-shell/mosh`" and
`BUILD-PROVENANCE.json` says it "publishes prebuilt mosh-client binaries from upstream mosh":
[resources/mosh/README.md L54-L57](resources/mosh/README.md),
[resources/mosh/BUILD-PROVENANCE.json](resources/mosh/BUILD-PROVENANCE.json).
MoshCatty's release workflow actually runs `cargo build --release` and packages
`target/release/mosh-client`:
[release.yml L46](https://github.com/binaricat/MoshCatty/blob/554b9d305e7ac4b11de740d764bbc3e05f816d7b/.github/workflows/release.yml#L46),
[L82](https://github.com/binaricat/MoshCatty/blob/554b9d305e7ac4b11de740d764bbc3e05f816d7b/.github/workflows/release.yml#L82).
MoshCatty is a Rust reimplementation, not a build of upstream C. This is a documentation and
supply-chain provenance defect: the provenance record should state that the binary is built
from the MoshCatty Rust source at the release tag, not from upstream C, unless the binaries
were produced differently outside the published workflow. Whether that distinction has legal
consequences is for the separate ticket, not this note.

### 3.3 `mosh-go` (unixshells / forks)

- Repository: [unixshells/mosh-go](https://github.com/unixshells/mosh-go). Tag `v0.5.2`
  (`0284bd652aab3dfff5b0b74945c23901d7537b61`, 2026-03-31); master
  `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f` (2026-04-05). MIT license per the repository.
  There are forks, including `Jasonbr/mosh-go` at v0.5.3 and `ys-ll/mosh-go`.
- Scope: pure-Go client and server, no cgo. Core files are small: `client.go` 327,
  `transport.go` 504, `fragment.go` 155, `ocb.go` 266, `pb.go` 653, `framebuffer.go` 454,
  `server.go` 614 lines at master. It implements OCB3, fragmentation, SSP, protobuf, a PTY
  server, and a WASM path.
- Claims: "wire-compatible with the standard C mosh implementation", with tests against a
  real `mosh-server` when installed. The repository has 88 Go tests, including
  `TestClientAgainstRealMoshServer`, `TestWithRealMoshClient`, and
  `TestOCBRFC7253Vectors`. **These tests were not executed here** (see section 9); the
  repository's own README is the only evidence for their passing.
- Engineering value: the best starting reference for Go idioms and a useful proof that the
  crypto/SSP surface is tractable in pure Go. It is not full parity.

Concrete gaps observed at master `8dca5c6` (all source-verified):

| Gap | Evidence | Impact |
|---|---|---|
| `Close` closes the socket without sending the `UINT64_MAX` shutdown state | [client.go L197-L207](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/client.go#L197-L207) | Remote `mosh-server` can wait indefinitely (default server behavior) |
| Receiver drops `seq <= seqInMax` before decrypt/reassembly | [transport.go L219-L224](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/transport.go#L219-L224) | Out-of-order fragments are lost; upstream returns older packets' payloads for reassembly |
| `ThrowawayNum` is always 0 | [transport.go L176-L184](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/transport.go#L176-L184) | Peer never gets a pruning hint; long unidirectional sessions can grow state |
| No incoming protocol-version check | `Recv` parses `TransportInstruction` without checking field 1; outbound sets 2 at [L177](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/transport.go#L177) | Accepts incompatible peers instead of failing |
| Received-state list capped at 128, not 1024 | [transport.go L339-L345](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/transport.go#L339-L345) | More likely to discard valid states under loss/reordering |
| Fixed 1300-byte fragment payload | [fragment.go L20-L21](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/fragment.go#L20-L21) | Datagram can exceed the IPv4/IPv6 MTU budget (1214/1178) |
| 1 MiB reassembly and decompression cap | [fragment.go L85](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/fragment.go#L85), [transport.go L491-L503](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/transport.go#L491-L503) | Rejects larger screen updates; upstream allows 4 MiB |
| Fixed RTO 250 ms–10 s, no SRTT-derived send interval | [transport.go L72-L76](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/transport.go#L72-L76) | Less responsive and less congestion-friendly than upstream's 50–1000 ms RTO and 20–250 ms pacing |
| At most one unacked state in flight | [client.go L222-L238](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/client.go#L222-L238) | Throughput and keystroke latency are RTT-bound; stock can pipeline up to 32 states |
| `Dial` is `udp4`-only; no roaming/port hopping/ECN | [client.go L52](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/client.go#L52) | IPv6 and network-change resilience are absent from the convenience API |
| Client only emits `HostBytes`; ignores `Resize` and `EchoAck` instructions | [client.go L285-L301](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/client.go#L285-L301) | Server-authoritative resize and prediction confirmation are lost |
| Prediction is unreleased and simplified | `predict.go` was added after v0.5.2; it predicts printable runes only, resets on `0x7f`, and has no arrow/echo-ack/adaptive parity ([predict.go L51-L79](https://github.com/unixshells/mosh-go/blob/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f/predict.go#L51-L79)) | Not stock prediction; only wired into `cmd/mosh-wasm` |
| Tests against stock mosh were not run here | README claim; test names exist | Interop is plausible but unverified by this research |

Engineering verdict: use mosh-go as a reference for structure and tests, not as an unqualified
dependency. Even if its MIT license is accepted, addressing the table above is substantial
work, and its provenance relative to the GPL reference is a separate legal question.

### 3.4 `bu/moshgo`

- Repository: [bu/moshgo](https://github.com/bu/moshgo); commit
  `69ac65e63167180122ca2c264c01fb618a572f63` (2026-07-24).
- License: GPL-3.0-or-later.
- Scope: client only; protocol version 2; no `mosh-server` or SSH bootstrap.
- Status: work in progress, explicitly AI-generated (README credits Claude Opus 4.8). It has
  a 20 KB `SPEC.md` byte-level contract and a 35 KB `PLAN.md`, plus a layered package layout
  (`internal/crypto/ocb`, `internal/network`, `internal/transport`, `internal/statesync`,
  `internal/terminal`, `internal/overlay`).
- Engineering value: the `SPEC.md`/`PLAN.md` are useful checklists and a second reading of the
  protocol. Not mature enough to depend on, and GPL.

### 3.5 Rust implementations

- `Arunjay4213/mosh-rs`: crates.io [`mosh`](https://crates.io/crates/mosh) 0.1.3, published
  2026-10-14; GPL-3.0-or-later; description says "The mobile shell (mosh) in Rust:
  wire-compatible with mosh 1.4". Repository
  [Arunjay4213/mosh-rs](https://github.com/Arunjay4213/mosh-rs) claims a native Windows client
  and scrollback. It is early (0.1.x) and no independent interop evidence was reviewed here.
- `wilsonglasser/mosh-rs`: crates.io [`mosh-rs`](https://crates.io/crates/mosh-rs) 0.1.0,
  published 2026-08-23; GPL-3.0-or-later; description: "A wire-compatible Rust client for
  mosh's State Synchronization Protocol, interoperating with the stock C++ mosh-server".
  Repository created 2026-08-23 with no stars; very early.
- MoshCatty (section 3.2) is the most mature Rust example.

### 3.6 Blink iOS fork

- Repository: [blinksh/mosh](https://github.com/blinksh/mosh), branch `ios`, commit
  `8671e873e3d365ef5611ff629a808b094b639cb3` (2016-12-23); repo pushed 2026-03-14.
- License: GPL-3.0. Description: "Mosh version library for iOS".
- Engineering value: proof that a Mosh client can be embedded in an iOS app, but it is an old
  fork of the C implementation, not a permissive library. The App Store/GPL question is the
  separate ticket's job.

### 3.7 Other Go packages are wrappers, not clients

`pkg.go.dev` also lists `github.com/artyom/mosh` (an MIT-licensed wrapper around
`mosh-client` that supports SOCKS proxies), `github.com/liyu1981/moshpf` (port-forwarding
wrapper), and `gitlab.hive.thyth.com/chronostruct/go-mosh` (only exports `GetVersion` and
`Timestamp`). These are not full protocol implementations. GitHub's repository search is
noisy because "mosh" is a common name; the significant Go protocol implementations found are
mosh-go and `bu/moshgo`.

### 3.8 Summary table

| Project | Language / license | Client? | Server? | Maturity | Use |
|---|---|---|---|---|---|
| upstream mosh 1.4.0 | C++ / GPL-3.0-or-later | Yes | Yes | Reference, 2022 | Oracle and spec source |
| MoshCatty 0.1.8 | Rust / GPL-3.0-or-later | Yes | No | Full client; vendor interop audit; 2026 | Behavioral checklist; current bundle |
| mosh-go v0.5.2/master | Go / MIT | Yes | Yes | Simplified; 88 tests; test execution unverified | Reference, not drop-in |
| bu/moshgo | Go / GPL-3.0-or-later | Yes | No | WIP, AI-generated; 2026 | SPEC/PLAN checklist |
| mosh-rs (Arunjay) | Rust / GPL-3.0-or-later | Yes | No | 0.1.3, early | Reference only |
| mosh-rs (wilsonglasser) | Rust / GPL-3.0-or-later | Yes | No | 0.1.0, very early | Reference only |
| Blink mosh | C / GPL-3.0 | Library | Yes? | 2016 C fork for iOS | iOS feasibility proof |
| artyom/mosh, moshpf | Go / MIT or other | Wrapper | No | Utility | Not protocol implementations |

---

## 4. Primary sources, test vectors, and specification gaps

### 4.1 Source hierarchy

1. **Protobuf schemas** are the closest thing to a normative message contract:
   `transportinstruction.proto`, `hostinput.proto`, `userinput.proto` at 1.4.0.
2. **Upstream C++ source and comments** define the byte layout, timers, queues, terminal
   behavior, and bootstrap. There is no separate wire specification.
3. **Man pages** define CLI/environment behavior (`mosh.1`, `mosh-server.1`,
   `mosh-client.1`), including the 22-character key and locale fallback.
4. **Release notes** define version-level feature differences and the 1.3.2 compatibility
   claim.
5. **The 2012 paper**, [Mosh: An Interactive Remote Shell for Mobile Clients](https://mosh.org/mosh-paper.pdf),
   describes the design goals and SSP at a conceptual level. It does not provide a byte-level
   protocol specification. The mosh.org site and README describe the product behavior:
   [mosh.org](https://mosh.org/), [README.md](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/README.md).
6. **RFC 7253** is the normative OCB3 algorithm:
   [RFC 7253](https://www.rfc-editor.org/rfc/rfc7253).
7. **Observed traffic and stock binaries** are the only way to verify behavior not documented
   in source comments (for example, exact terminal sequence output).

### 4.2 Existing test vectors and tests

- **OCB3**: upstream `src/tests/ocb-aes.cc` cites draft-krovetz-ocb-03 Appendix A and tests
  encrypt/decrypt with multiple lengths, including bad-tag rejection:
  [ocb-aes.cc](https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/tests/ocb-aes.cc#L178).
  RFC 7253 has its own Appendix A vectors. mosh-go has `TestOCBRFC7253Vectors` (vendor claim)
  and MoshCatty's audit says it passes RFC 7253 and mosh-go vectors.
- **Base64**: upstream has a fixed native/encoded table in `src/tests/base64_vector.h` and a
  generator `genbase64.pl`. This is useful for key parsing.
- **Nonce uniqueness**: `src/tests/nonce-incr.cc` inserts one million packets of each
  direction/pattern into a set and checks all 4 million nonces are unique.
- **Encrypt/decrypt round trip**: `src/tests/encrypt-decrypt.cc`.
- **End-to-end tests** under `src/tests/` include:
  `emulation-attributes-truecolor.test`, `emulation-80th-column.test`,
  `emulation-wrap-across-frames.test`, `prediction-unicode.test`, `window-resize.test`,
  `network-no-diff.test`, `server-network-timeout.test`, `pty-deadlock.test`, `repeat.test`,
  and `e2e-success.test`/`e2e-failure.test`. These are a good conformance checklist even if
  the exact tmux harness is not reusable.
- **No golden packet corpus**: upstream does not ship encrypted datagram fixtures or a
  standalone SSP conformance suite. MoshCatty and bu/moshgo have their own tests, but they
  are not independent standards.

### 4.3 Gaps where no formal spec exists

- **SSP wire behavior**: only the paper and source. Exact ACK timing, queue sizes, throwaway
  semantics, shutdown state (`UINT64_MAX`), and receiver quench are implementation details.
- **Fragment framing and zlib choice**: no document; source only.
- **Timestamps and RTT**: no document; source only.
- **Terminal diff semantics**: `Display::new_frame` is implementation-defined. The ANSI
  sequences it emits are not a standard "Mosh screen diff" format. Any client that re-emulates
  them must match its parser/emulator.
- **Locale bootstrap**: man pages and `mosh.pl` only; the server's "native locale wins"
  behavior is source-level.
- **Roaming/ECN/port hopping**: source-level; no compatibility guarantee or test corpus.
- **Version compatibility**: the 1.3.2 release note is the clearest public claim; there is no
  maintained compatibility matrix. Protocol v2 being constant is the strongest evidence.
- **MoshCatty's own audit** notes that some Unicode width differences are terminal-emulator
  dependent and cannot be judged from a single server frame; this is a real class of
  client-side divergence, not a wire bug.

### 4.4 Recommended conformance corpus for a clean-room client

1. Decrypt/inspect harness that can read a captured UDP stream and print `dir_seq`,
   timestamps, fragment id/number/final, protobuf fields, and the raw diff.
2. Golden traces from stock 1.3.2 and 1.4.0 servers: initial state, typing, backspace, arrows,
   resize, clear, truecolor, OSC 52, title, Ctrl-C, and shutdown. Store both the wire bytes and
   the expected terminal output.
3. An SSP fault-injection harness: drop/reorder/duplicate fragments, delay ACKs, fill the
   receiver queue, blackhole for 60 s, change source address, and verify recovery.
4. An emulator parity harness: feed the same Mosh `HostBytes` stream to the app terminal and
   to stock `mosh-client`, and compare screen text, cursor, attributes, and scrollback.
5. Crypto vectors: RFC 7253 Appendix A plus independent AES-OCB test vectors; do not rely on a
   single implementation's test file.
6. Key/handshake tests: 22/24-character keys, split `MOSH CONNECT`, ANSI/OSC noise, missing
   newline, `MOSH IP`, and `MOSH SSH_CONNECTION`.

The wire facts themselves are not copyrightable in most jurisdictions, but copying GPL test
files or source comments may be; the separate legal ticket should decide how the corpus is
built.

---

## 5. Interop matrix

### 5.1 Protocol versions

| Client version | Server 1.2.2 | Server 1.3.2 | Server 1.4.0 | Server master (2026-03-22) |
|---|---|---|---|---|
| 1.2.2 | v2, source-compatible | v2, source-compatible | v2; truecolor output may be unsupported | v2; OSC 8 may be unsupported |
| 1.3.2 | v2, source-compatible | v2, release-note compatible | v2; truecolor output may render incorrectly | v2; OSC 8 may be ignored |
| 1.4.0 | v2, source-compatible | v2, release-note compatible | v2, reference | v2; OSC 8 may be ignored |
| Proposed Go client | target v2 | target v2; primary test target | target v2; primary test target | best effort, not required |

"Source-compatible" means the protocol version and message schema are the same in the pinned
source; it does **not** mean this research ran the combination. "Release-note compatible" is
the 1.3.2 announcement's claim for servers back to 1.0.9:
[mosh-1.3.2 release](https://github.com/mobile-shell/mosh/releases/tag/mosh-1.3.2).
The combinations not marked as tested are expectations, not measured results.

### 5.2 Version feature differences

| Feature | 1.3.2 | 1.4.0 | master (unreleased) |
|---|---|---|---|
| Protocol version | 2 | 2 | 2 |
| Truecolor | No (12-bit render fields) | Yes (25-bit render fields) | Yes |
| OSC 52 clipboard | No | Yes | Yes |
| `--predict-overwrite` | No | Yes | Yes |
| Suppress prediction on large paste | No | Yes | Yes |
| OSC 8 hyperlinks | No | No | Yes |
| Syslog connection logging | No | Yes | Yes |
| Latest-consecutive resize fix | No | Yes | Yes |
| Release date | 2017-07-22 | 2022-10-27 | unreleased |

Sources: release pages above; 1.4.0 `Renditions` and 1.3.2 `Renditions`; master terminal
display. A client that only needs to connect can ignore all of these; a client that claims
visual parity cannot.

### 5.3 Platforms and packages

Official availability, per [mosh.org](https://mosh.org/): GNU/Linux, BSD, macOS, Solaris,
Android, Chrome, and iOS. The site explicitly says there is **no native Windows executable**
and recommends Cygwin (with a full-featured terminal such as mintty) or the Chrome version:
[mosh.org](https://mosh.org/).

Distribution versions observed on 2026-11-03 via
[Repology](https://repology.org/project/mosh/versions):

| Platform / repository | Version | Note |
|---|---|---|
| Debian 11 | 1.3.2 | old stable; still relevant for compatibility |
| Debian 12, 13, unstable | 1.4.0 | current |
| Ubuntu 18.04, 20.04, 22.04 | 1.3.2 | 22.04 is still widely deployed |
| Ubuntu 24.04 and newer | 1.4.0 | current |
| Fedora 39–44, rawhide | 1.4.0 | current |
| Arch, Alpine, FreeBSD, OpenBSD, MacPorts, Homebrew, Nix, Termux | 1.4.0 | current per Repology |
| NetBSD (pkgsrc) | listed by mosh.org | not in the Repology filter used here |
| macOS upstream package | 1.4.0 `.pkg` release asset | architecture not rechecked here |
| Windows | no native mosh | Cygwin, WSL, or Chrome per mosh.org |
| iOS | no upstream package | Blink Shell ships a GPL C fork |
| Android | Termux package 1.4.0; JuiceSSH had Mosh support | No native library/API |

Platform engineering notes:

- **Linux/BSD/macOS**: full PTY, termios, `TIOCSWINSZ`, `IP_RECVTOS`, `IP_TOS`, and IPv6 are
  available. Stock Mosh uses them.
- **iOS**: no fork/exec of a bundled `mosh-client` in the App Store sandbox; an in-process
  client is the natural fit. Blink proves the protocol can be embedded. MyGo PR 940 provides
  the iOS host/runtime (Go `c-archive` linked by Swift), but does not by itself provide a Mosh
  terminal integration:
  [MyGo PR 940](https://github.com/egoist/mygo/pull/940) (head `7153b6c`, open).
- **Android**: Termux runs the Unix binary; an in-process Go client via MyGo's `c-shared`
  Android host is possible. UDP sockets and background/doze behavior need testing.
- **Windows**: no official native server; a pure-Go client could still connect to a Linux
  server. ConPTY and socket option differences matter for the app's terminal layer.
- **Mobile background/roaming**: Mosh's design assumes the client can keep a UDP socket and
  change source address/port. iOS and Android may suspend sockets when backgrounded. The
  protocol supports reconnect/roaming, but the app must keep a socket or re-create it and
  resume the same key/port; this is platform work not covered by the wire protocol.

### 5.4 Recommended test matrix

At minimum:

- Servers: stock `mosh-server` 1.3.2 and 1.4.0 on Linux; one macOS 1.4.0 server if available;
  optional master build for forward-compatibility.
- Client: proposed Go client on macOS/Linux first, then iOS simulator and Android emulator,
  then physical devices.
- Networks: clean LAN; 100/300/500 ms RTT; 5–10% loss; reordering; duplication; 60 s
  blackhole; NAT rebinding; Wi-Fi/cellular handoff; IPv6-only and dual-stack.
- Terminal: 80x24, 120x40, 1x1, very wide, resize during output, Unicode/emoji/combining,
  truecolor SGR, OSC 52, title, bracketed paste, mouse reporting, Ctrl-C, Ctrl-L repaint.
- Lifecycle: attach/detach, app background/foreground, network change, clean exit, server
  crash, orphan-server detection, key redaction, log redaction.
- Payloads: large screen update above 1 MiB (the mosh-go cap) and near 4 MiB, to catch
  decompression/state limits.
- Security: wrong key/tag, replay, reflected direction, oversized datagram, decompression
  bomb, malformed protobuf/fragment, out-of-range resize.

---

## 6. Architecture options for this product

### 6.1 What the app already has

- SSH connection manager and bootstrap command construction, including `MOSH CONNECT`
  sniffing, ANSI/OSC tolerance, key redaction, and timeout:
  [domain/model/mosh/mosh.go](domain/model/mosh/mosh.go),
  [adapters/mosh/mosh.go](adapters/mosh/mosh.go).
- Bundled MoshCatty binary verification with SHA-256/size on every launch and no `PATH`
  fallback:
  [adapters/bundle/bundle.go](adapters/bundle/bundle.go),
  [adapters/bundle/manifests.go](adapters/bundle/manifests.go),
  [docs/parity/matrix.yaml](docs/parity/matrix.yaml) (`mosh.bundle`).
- A MyGo terminal plugin (desktop) backed by libghostty-vt with an
  `io.ReadWriteCloser` connection seam and optional `Resize(cols, rows)`:
  [docs/research/go-mygo-rewrite.md](docs/research/go-mygo-rewrite.md) sections 3.1 and 4.6.
  It supports 24-bit color, wide characters/emoji, alternate screen, mouse, bracketed paste,
  OSC 8, OSC 52, title, and scrollback. It does not expose cursor geometry or cell styling
  publicly, which matters for prediction overlays.

### 6.2 Two implementation shapes

**Shape A — network-only client feeding the app terminal.**

Write a pure-Go `moshclient` package that exposes an `io.ReadWriteCloser`-like connection:

```
Read  -> ordered HostBytes ANSI deltas for the terminal emulator
Write -> keystrokes as UserMessage actions
Resize(cols, rows) -> UserMessage Resize
```

The package owns SSP, UDP, crypto, fragmentation, protobuf, ACK/retransmit, initial resize,
and shutdown. The MyGo terminal plugin owns display and scrollback. This is the lowest-cost
MVP and maps cleanly onto MyGo's existing `Options.Conn` seam:
[docs/research/go-mygo-rewrite.md](docs/research/go-mygo-rewrite.md) section 4.6.

Risks: ANSI emulator divergence, no prediction, no server-authoritative resize unless the
bridge interprets `HostMessage.Resize`, and the plugin may not be available on iOS/Android.

**Shape B — stock-comparable client.**

Add a shadow `Complete`/framebuffer, prediction engine, EchoAck handling, local ANSI renderer
or overlay, and terminal semantics matching upstream. This is full parity and the estimate in
section 7. It can still reuse the app's terminal for final display, but the shadow model is
needed to predict and to validate server frames.

**Recommendation:** build Shape A first as a vertical slice against a stock 1.4.0 server,
with the existing bundle as fallback. Measure the terminal-parity gap. Only then decide whether
Shape B is required for the product; do not commit to full prediction parity before the
emulator parity harness exists.

### 6.3 Clean-room process

- Write an independent specification from the proto schemas, man pages, paper, RFC 7253, and
  black-box observations. Keep this document and the conformance corpus as the spec.
- Implement from the spec. Do not copy GPL source, comments, identifiers, or test files.
- Use stock `mosh-server` and `mosh-client` as black-box oracles: run them, capture packets,
  compare screen output. Use MoshCatty and mosh-go as behavioral cross-checks, not as code to
  paste.
- Treat mosh-go's MIT file as a reference only until its provenance is reviewed; its MIT
  header does not by itself settle whether it is a derivative of GPL code. That is a legal
  question, not an engineering one.
- Keep the key out of logs, crash dumps, and UI; unset it after dialing, as the stock client
  does. The app's existing `MOSH CONNECT` redaction should be preserved.

### 6.4 Revisiting ADR-0005

ADR-0005 rejected a pure-Go client because "mosh needs AES-128-OCB3 and SSP" and those were
considered substantial greenfield work:
[docs/adr/0005-bundle-mosh-et-platform-resources.md L14](docs/adr/0005-bundle-mosh-et-platform-resources.md).
The evidence here refines that: OCB3 + SSP + framing are bounded and have a working pure-Go
existence proof (`mosh-go` core is about 3.2k lines), while the real cost is terminal
semantics, prediction parity, roaming/lifecycle, and interop QA. If the product only needs a
solid mobile client that displays the remote screen and sends input through the app's existing
terminal, Shape A is materially cheaper than the ADR implies. That is an engineering input to
the product decision, not a reversal of the ADR.

---

## 7. Work breakdown and effort

**Assumptions for the estimate (all estimates, not commitments):**

- One senior Go engineer with existing knowledge of terminal protocols; includes tests and
  review but not legal, product design, App Store submission, or release engineering.
- Reuses the existing SSH bootstrap/handshake in this repo.
- Reuses a capable app terminal emulator (MyGo terminal or equivalent) for display. If a
  renderer/emulator must be written from scratch, add 3–6 engineer-weeks.
- "Full parity" means the stock 1.4.0 client feature set on a stable desktop network,
  including prediction modes, truecolor/OSC 52, roaming/port hopping/ECN, IPv6, clean
  shutdown, and terminal edge cases. It excludes implementing `mosh-server`, OS packaging,
  and legal work.
- Error bars are wide; no team velocity data exists for this codebase. The implementation-size
  anchors are MoshCatty (~11.2k Rust lines under `src/`), mosh-go (~3.2k core Go lines for a
  simplified implementation), and upstream (~15.6k non-test C++ lines).

| Workstream | MVP (engineer-weeks) | Full parity (engineer-weeks) | Notes |
|---|---:|---:|---|
| Protocol spec, packet/trace harness, conformance corpus | 0.5–1.0 | 1.5–2.5 | Capture/decrypt, golden traces, fuzz inputs |
| Crypto, framing, zlib, proto2 codec | 1.0–1.5 | 2.0–3.0 | OCB3 vectors, nonce/replay, extensions, limits |
| SSP state machine, ACK/retransmit, shutdown | 1.5–2.5 | 3.0–5.0 | Queues, timers, throwaway, shutdown, roaming/ECN |
| Terminal bridge and shadow state | 1.0–2.0 | 4.0–6.0 | Shape A bridge; full parity adds shadow/emulator parity |
| Prediction/local echo | 0 | 2.0–3.5 | Modes, backspace/arrows, epoch, EchoAck, glitch/underline |
| Client UX and platform integration | 1.0–2.0 | 2.0–3.5 | Input encoding, escape key, resize, lifecycle, mobile |
| Interop and QA | 1.0–2.0 | 3.0–5.0 | 1.3.2/1.4.0, loss/reorder, IPv6, MTU, Unicode, long sessions |
| Hardening (fuzz, limits, secrets, telemetry-free diagnostics) | 0–0.5 | 1.0–2.0 | Resource caps, malformed input, cleanup |
| **Total** | **6.0–11.5** | **18.5–30.5** | Round to 6–12 and 19–31 engineer-weeks |

Additional cost if the app has no suitable terminal emulator on the target platform: +3–6
engineer-weeks (or more for mobile-specific rendering). Additional cost if full roaming/port
hopping/ECN parity is required on iOS/Android: included in the full-parity column but carries
platform risk. Ongoing upstream tracking after parity: roughly 1–2 engineer-weeks per year
for small changes (OSC 8, terminal fixes), more if upstream changes SSP.

**Schedule shape:** two engineers can plausibly deliver Shape A in 3–6 calendar weeks and
full parity in 3–4 calendar months, but the dependency chain (spec → crypto/SSP → terminal
bridge → prediction → QA) and mobile platform risk make the single-engineer estimate more
useful for planning.

**What could blow the estimate up:** terminal emulator divergence, iOS/Android socket
lifecycle, prediction parity, and a requirement to support old servers below 1.3.2. What
could shrink it: the app already has SSH bootstrap, terminal rendering, and a bundled client
as fallback; the wire protocol is stable v2 and has two independent implementations to test
against.

---

## Answers (detailed)

### A1. Required protocol surface

| Layer | Must implement | Evidence |
|---|---|---|
| Bootstrap | SSH exec, `MOSH CONNECT` parse, key/port/IP, locale | Already in repo; upstream `scripts/mosh.pl`, `mosh-server.cc` |
| Crypto | Base64 key, AES-128-OCB3, 12-byte nonce, 16-byte tag, empty AAD | `crypto.cc`, RFC 7253 |
| Datagram | 8-byte direction/seq, 4-byte timestamps, 28-byte overhead | `network.cc`, `network.h` |
| Framing | 10-byte fragment header, zlib, 4 MiB receive limit | `transportfragment.cc`, `compressor.h` |
| Messages | proto2 `TransportInstruction`, `HostMessage`, `UserMessage` | `.proto` files |
| SSP | state numbers, old/new/ack/throwaway, ACK/retransmit, queues, shutdown | `networktransport-impl.h`, `transportsender-impl.h` |
| Client state | keystrokes, resize, initial size | `user.cc`, `stmclient.cc` |
| Server state | apply `HostBytes`, `Resize`, `EchoAck` | `completeterminal.cc` |
| Liveness | timestamps/RTT, 60 s initial wait, shutdown marker | `network.cc`, `mosh-server.cc` |
| Version | send and verify protocol 2 | `network.h`, `networktransport-impl.h` |

### A2. What can be a UX layer

- Prediction/local echo: yes, entirely client-side. It does not affect interop. Full parity
  means porting the real rules, not just echoing printable characters.
- Display: yes, if the app's VT emulator accepts the ANSI `HostBytes`. The emulator becomes
  responsible for matching Mosh's terminal semantics; this is the main parity risk.
- Truecolor/OSC 52/OSC 8, notifications, title prefix, escape key, terminfo: local UX.
- Roaming, port hopping, ECN, prospective resend, chaff, adaptive pacing: resilience and
  traffic-shaping optimizations; not required for a stable-network MVP.
- IPv6, Windows/macOS quirks, mobile lifecycle: platform layers, not protocol changes.

### A3. Which parts are required specifically for stock `mosh-server` interop

Everything in the "network stack + UserMessage + HostBytes apply + initial resize + shutdown"
row of section 2. Prediction, local display rendering, truecolor-specific parsing, OSC 52,
and roaming are not required to establish a session and exchange input/output. Protocol v2 and
the protobuf field numbers are non-negotiable. EchoAck is optional for basic interop but
needed for stock-quality prediction.

### A4. Existing implementations verdict

No permissive full-parity Go client. Mosh-go is the best Go reference but has the concrete
gaps in section 3.3 and unverified test execution. MoshCatty is the strongest full client but
GPL Rust. bu/moshgo is a WIP GPL client with a useful spec. mosh-rs and Blink are references,
not dependencies. Build the network core ourselves; use the others as oracles and checklists.

### A5. Primary-source gaps

No formal SSP/wire spec; no golden datagram corpus; terminal diff semantics are
implementation-defined; timers/queues are source-only; version compatibility is a release-note
claim rather than a maintained matrix. RFC 7253 and the proto files are the strongest
normative anchors. Build a spec and conformance corpus; do not assume upstream tests are
reusable under a clean-room constraint.

### A6. Interop and effort summary

Target protocol v2, test against stock 1.3.2 and 1.4.0; 1.3.2 is still deployed on Debian 11
and Ubuntu 22.04. 1.4.0's truecolor/OSC 52 features are terminal-level. A Shape A MVP is
roughly 6–12 engineer-weeks; full parity is roughly 19–31 engineer-weeks, dominated by
terminal/prediction parity and QA.

---

## Unknowns / needs verification

1. **Live interop was not run.** This research read source and implementation documentation;
   it did not execute stock `mosh-server`, stock `mosh-client`, MoshCatty, or mosh-go. The
   test matrix in section 5.4 is a plan, not results. In particular, the claimed mosh-go and
   MoshCatty interop tests need independent reproduction.
2. **1.3.2 client vs 1.4.0 truecolor server.** The wire is v2, but a 1.3.2 client may render
   truecolor SGR incorrectly because its render fields are 12-bit. Verify screen output, not
   just connection success.
3. **Master OSC 8 vs 1.4.0.** Master is unreleased and emits OSC 8 hyperlinks; whether a
   1.4.0 client ignores them safely or shows artifacts is unverified.
4. **mosh-go test claims.** `TestClientAgainstRealMoshServer`, `TestWithRealMoshClient`, and
   `TestOCBRFC7253Vectors` exist, but were not run; their pass/fail status on the pinned
   commits is unknown. The gaps in section 3.3 are static-source findings.
5. **MoshCatty binary provenance.** The release workflow builds Rust, but this repo's
   `resources/mosh/README.md` and `BUILD-PROVENANCE.json` say the binaries come from upstream
   C. The published binaries were not rebuilt from tag `0.1.8` to confirm they match the tag
   or the workflow. This is both a documentation defect and a supply-chain verification gap.
6. **MyGo terminal plugin on iOS/Android.** MyGo PR 940 adds the mobile runtime; the
   repository's desktop research describes a libghostty-vt terminal plugin, but this note did
   not verify that the same terminal plugin (or an equivalent ANSI emulator) is available on
   iOS/Android. This is the largest integration unknown for Shape A.
7. **Emulator parity.** Mosh's `HostBytes` is ANSI generated by its own `Display`; the app's
   emulator may differ on wide characters, combining marks, unknown SGR, resize/reflow, and
   scrollback. MoshCatty's audit reports Unicode-width differences even between official
   `Display` and xterm.js. A parity harness is required before claiming stock-identical
   rendering.
8. **Mobile socket lifecycle.** Whether iOS/Android keep the UDP socket alive across
   background/foreground, Wi-Fi/cellular handoff, and NAT rebind is platform- and
   OS-version-dependent; no device testing was done here. Mosh's roaming/port hopping helps
   but does not guarantee the app can keep or recreate its socket.
9. **Prediction parity.** The exact stock behavior depends on the local framebuffer, parser,
   EchoAck, and adaptive thresholds. The effort estimate includes it, but no prototype was
   built to validate the 2–3.5 engineer-week range.
10. **Maximum state sizes in practice.** Upstream's 4 MiB decompression/state limit and
    1024-state queue are source-verified; how often real mobile sessions approach them (and
    how the app terminal handles large redraws) is unmeasured.
11. **ECN and IPv6 support on target OSes.** `IP_TOS`/`IP_RECVTOS`/IPv6 socket options and
    their behavior on iOS/Android/Windows were not tested.
12. **Protocol behavior below 1.2.2 / above 1.4.0.** The 1.3.2 release note claims compatibility
    back to clients 0.96 and servers 1.0.9; those old endpoints were not tested. The proposed
    client should target 1.3.2 and 1.4.0, not ancient versions, unless a product requirement
    says otherwise.
13. **MoshCatty's audit completeness.** Its claims are vendor claims; it lists Windows/ConPTY
    and public IPv6 near-MTU/blackhole testing as remaining blockers. Do not treat its audit
    as independent certification.
14. **mosh-go MIT provenance.** The repository says MIT; whether the code is a derivative of
    GPL source is a legal ticket question. Engineering should not assume the MIT label settles
    reuse rights.

---

## Sources not accessible / limitations

- **MyGo PR 940** was initially hard to locate but was accessed successfully at
  [egoist/mygo#940](https://github.com/egoist/mygo/pull/940), head
  `7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a`, open, updated 2026-11-02. Its body is about the
  mobile runtime; it does not contain Mosh-specific design or testing.
- **Stock Mosh binaries were not installed or run.** No live client/server capture, no tmux
  end-to-end test, and no packet-level verification against a real server occurred.
- **mosh-go tests were not executed.** Running tests from an external clone would require
  executing code not authored for this task; the environment also lacked a `go` binary in
  `PATH`. All mosh-go findings are static source inspection.
- **The Mosh paper PDF was not text-extracted.** `pdftotext`, `mutool`, `qpdf`, Ghostscript,
  and Python PDF libraries were unavailable. The paper URL is cited for design context; the
  protocol facts in this note come from source, schemas, man pages, and release notes.
- **RFC 7253 vectors were not independently recomputed.** No Python `cryptography`/AESOCB
  implementation was available. Upstream's draft-03 vectors, mosh-go's claimed RFC vectors,
  and MoshCatty's claimed vectors were not re-run.
- **GitHub code search was intermittent.** Repository search worked but is noisy because
  "mosh" is a common name; the list of Go packages in section 3.7 is not guaranteed
  exhaustive. `pkg.go.dev` search was reachable.
- **MoshCatty binaries were not reproduced.** The release asset checksums in this repo were
  verified previously, but this note did not rebuild MoshCatty from tag `0.1.8`.
- **No App Store, GPL, or export-control analysis** was performed; that is the separate
  ticket.

---

## References

### Upstream Mosh

- Repository: https://github.com/mobile-shell/mosh
- 1.4.0 tag: https://github.com/mobile-shell/mosh/releases/tag/mosh-1.4.0 (published
  2022-10-27; commit `094fdba5ca7566efd98e1c36fb2295ef508c145a`)
- 1.3.2 tag: https://github.com/mobile-shell/mosh/releases/tag/mosh-1.3.2 (published
  2017-07-22; commit `6852787f213de885b9e565535f22681de5e98f94`)
- 1.2.2 tag: https://github.com/mobile-shell/mosh/releases/tag/mosh-1.2.2 (commit
  `bb07e52193bb2edc363912fc2d70a9cdcd530185`)
- master: https://github.com/mobile-shell/mosh/commit/decd9b705eb81626f694335b8d5940538beb06da
  (2026-03-22)
- Bootstrap: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/scripts/mosh.pl
- Server: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-server.cc
- Client: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/mosh-client.cc
- Session loop: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/stmclient.cc
- Network: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.cc,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/network.h
- SSP send: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender-impl.h,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportsender.h
- SSP receive: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/networktransport-impl.h
- Fragments: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/transportfragment.cc,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/network/compressor.h
- Crypto: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.cc,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/crypto/crypto.h
- Terminal complete state: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/completeterminal.cc,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/statesync/user.cc
- Terminal rendering: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminaldisplay.cc,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/terminal/terminalframebuffer.h
- Prediction: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.cc,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/frontend/terminaloverlay.h
- Protobufs: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/protobufs/transportinstruction.proto,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/protobufs/hostinput.proto,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/src/protobufs/userinput.proto
- Tests: https://github.com/mobile-shell/mosh/tree/mosh-1.4.0/src/tests
- Man pages: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh.1,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-server.1,
  https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/man/mosh-client.1
- README: https://github.com/mobile-shell/mosh/blob/mosh-1.4.0/README.md

### Specifications and background

- RFC 7253, The OCB Authenticated-Encryption Algorithm: https://www.rfc-editor.org/rfc/rfc7253
- Mosh paper: https://mosh.org/mosh-paper.pdf
- Mosh project site: https://mosh.org/
- Repology package versions: https://repology.org/project/mosh/versions

### Existing implementations

- MoshCatty: https://github.com/binaricat/MoshCatty
- MoshCatty 0.1.8: https://github.com/binaricat/MoshCatty/releases/tag/moshcatty-0.1.8
- MoshCatty protocol audit: https://github.com/binaricat/MoshCatty/blob/554b9d305e7ac4b11de740d764bbc3e05f816d7b/docs/protocol-audit-2026-07-15.md
- mosh-go v0.5.2: https://github.com/unixshells/mosh-go/tree/0284bd652aab3dfff5b0b74945c23901d7537b61
- mosh-go master: https://github.com/unixshells/mosh-go/tree/8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f
- mosh-go fork: https://github.com/Jasonbr/mosh-go
- bu/moshgo: https://github.com/bu/moshgo
- mosh-rs (Arunjay): https://github.com/Arunjay4213/mosh-rs,
  https://crates.io/crates/mosh
- mosh-rs (wilsonglasser): https://github.com/wilsonglasser/mosh-rs,
  https://crates.io/crates/mosh-rs
- Blink mosh: https://github.com/blinksh/mosh
- artyom/mosh wrapper: https://github.com/artyom/mosh
- moshpf: https://github.com/liyu1981/moshpf

### Product context

- MyGo PR 940: https://github.com/egoist/mygo/pull/940
- MyGo repo: https://github.com/egoist/mygo
- This repo's current Mosh adapter: `adapters/mosh/mosh.go`
- This repo's handshake model: `domain/model/mosh/mosh.go`
- This repo's bundle verification: `adapters/bundle/bundle.go`, `adapters/bundle/manifests.go`
- This repo's research on MyGo terminal/plugin architecture:
  `docs/research/go-mygo-rewrite.md`
- ADR-0005: `docs/adr/0005-bundle-mosh-et-platform-resources.md`
- Ticket 43 parity matrix: `docs/parity/matrix.yaml`
