# mosh-go and MoshCatty provenance audit

Status: research  
Date: 2026-11-03  
Scope: provenance, licensing, ownership, and usability of the candidate mosh
implementations. Protocol engineering (SSP, prediction, terminal emulation) is
covered only where it affects provenance or licensing.

Sources verified against:

- `unixshells/mosh-go` at `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f`
  (2026-04-05); released tag `v0.5.2` is
  `0284bd652aab3dfff5b0b74945c23901d7537b61` (2026-03-31). The initial commit is
  `bb08f71e30ecaf9b6f1490becde1773ba64df24a` (2026-03-12).
- `unixshells/mosh-dart` at
  `4725f1bf14d29c2aaa22dba09a9c66075cfa6e6d` (2026-03-12), the sibling MIT
  implementation used to establish lineage.
- `binaricat/MoshCatty` at tag `moshcatty-0.1.8`, commit
  `554b9d305e7ac4b11de740d764bbc3e05f816d7b` (2026-07-17); initial commit
  `4f76b33641d58a68f62716bb7b51d1f2faa3505a` (2026-07-10).
- Upstream mosh at `decd9b705eb81626f694335b8d5940538beb06da`
  (`mosh-1.4.0-39-gdecd9b7`, GPL-3.0-or-later).
- The Netcatty worktree at `240c8c6aa6790f26c447d6761b0037a28c753f9f`.

Method: full git histories, source comments, license files, GitHub release
metadata, release-asset hashes, and strings in the committed Windows binary.
A normalized comment-comparison script was run against upstream mosh; its
limitations are stated where used. No code from the cloned repositories was
executed (the Go toolchain is absent and running freshly cloned external test
code is not permitted in this environment).

---

## 1. Executive summary

1. **The bundled client is not built from `mobile-shell/mosh`.** MoshCatty
   0.1.8 is a pure-Rust reimplementation. Its release workflow runs
   `cargo build --release`, its release notes say "Pure Rust `mosh-client` for
   Netcatty", and the committed Windows binary contains Rust standard-library
   and Cargo registry paths, not C++ mosh symbols. The local provenance files
   say the opposite and must be corrected.
2. **MoshCatty contains MIT-derived material from mosh-go**, not only
   "interop vectors". Its initial commit already described itself as using
   "mosh-go's simplified single-pending send queue" and "mosh-go layout", its
   protocol tests are adapted from mosh-go tests, and a later commit ported
   mosh-go's framebuffer/prediction path. The MIT copyright and permission
   notice is absent from both the MoshCatty repository and Netcatty's
   `resources/` tree.
3. **mosh-go is a plausible MIT base, but it is not pre-cleared.** No literal
   copying of GPL mosh comments was found, its architecture is substantially
   simpler than upstream's, and its closest relative is the same author's
   MIT `mosh-dart` project, committed 91 seconds later. That supports the MIT
   declaration. It does not prove independent creation, and there are no
   provenance records, no CI, no releases, and several concrete client bugs.
4. **MoshCatty cannot be relicensed by editing its license file.** Relicensing
   needs (a) copyright consolidation for every contributor, (b) preservation of
   the mosh-go MIT notice, and (c) proof that no part is a derivative of GPL
   mosh expression. The third condition is the hard one; the code was written
   with upstream heavily consulted and its own documentation cites upstream
   line by line.
5. **Recommendation:** fix the notices immediately; commission a provenance
   audit of MoshCatty and mosh-go; use the audit result to relicense MoshCatty
   under a permissive license for the cross-platform/iOS client, with a
   documented clean-room replacement for any module the audit cannot clear.
   Keep mosh-go as the audited MIT fallback/oracle for Go-side work, not as the
   sole shipped client, because it is dormant and has known SSP defects.

---

## 2. `unixshells/mosh-go` audit

### 2.1 Repository facts

| Item | Value |
| --- | --- |
| Repository | <https://github.com/unixshells/mosh-go> |
| Created | 2026-03-13T01:08:20Z |
| Initial commit | `bb08f71`, 2026-03-12T18:33:41-07:00, 5,445 lines in one commit |
| HEAD audited | `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f`, 2026-04-05 |
| Code tag | `v0.5.2` = `0284bd6`, 2026-03-31 |
| Commits / tags / GitHub releases | 34 / 8 / 0 |
| License | MIT, "Copyright (c) 2026 Unix Shells" (`LICENSE`) |
| Activity | 2026-03-12 to 2026-04-05 only; no commits since |
| Community signals | 27 stars, 11 forks, 2 open issues (2026-11-03) |
| CI | No `.github` directory; no GitHub Actions runs |
| Module | `github.com/unixshells/mosh-go`, `go 1.26.0` (`go.mod`) |

The eight tags (`v0.1.0` through `v0.5.2`) were all cut between 2026-03-12 and
2026-03-31. There are no GitHub Releases, only tags; the README's
`go install ...@latest` therefore resolves to the repository's default branch
state, not a published release.

### 2.2 What the code says about upstream mosh

mosh-go is explicit that it implements the mosh wire protocol and that it used
the C implementation as a reference. The relevant comments are original prose,
not copied text:

| Location | Claim |
| --- | --- |
| `fragment.go:8` | `// Fragment wire format (mosh network.cc):` followed by the 8-byte instruction id, 15-bit fragment number plus final bit, and payload layout. |
| `fragment.go:20-21` | `// Maximum payload per fragment. Mosh uses 1300 to stay under typical MTU.` then `maxFragmentPayload = 1300`. |
| `pb.go:9-10` | `// Hand-rolled protobuf encoding for the three mosh .proto schemas.` / `// Field numbers match upstream mobile-shell/mosh exactly.` |
| `pb.go:16-22` | Documents `TransportInstruction` fields 1-7 (`protocol_version`, `old_num`, `new_num`, `ack_num`, `throwaway_num`, `diff`, `chaff`). |
| `pb.go:56-58`, `70-71` | Documents nested `HostBytes` field 4, `ResizeMessage` fields 5/6, `EchoAck` field 8, `Keystroke` field 4. |
| `transport.go:290` | `// Process SSP fields (matching upstream mosh recv logic).` |
| `client.go:224-225` | `// This matches the Dart mosh client's behavior: at most one unacked // state in flight.` |

The field numbers and wire layout are interoperability facts; the surrounding
implementation is hand-written Go. There are no GPL headers, no copied mosh
copyright notices, and no `mobile-shell/mosh` source files in the repository.

### 2.3 Is mosh-go a translation or derivative of GPL mosh?

This audit used two tests.

**Comment comparison.** Every line comment in the non-test Go files was
normalized (lowercase, punctuation removed) and searched for as a substring in
the normalized text of upstream mosh `decd9b7` (`.cc`, `.h`, `.hpp`, `.pl`,
`.proto`, `.c`). Result: **0 exact matches**. The same method found 0 matches
between MoshCatty's Rust comments and upstream. This is evidence against
literal comment copying; it cannot detect translated code with rewritten
comments.

**Structural comparison.** The implementations are not line-by-line
translations:

- `transport.go` is a simplified "single pending diff" SSP, not a port of
  `transportsender-impl.h`. It lacks upstream's `sent_states` queue,
  `assumed_receiver_state`, `send_interval()`, chaff generation, shutdown
  sequence, and prospective-resend optimization. Upstream's sender keeps a
  list of timestamped states and rationalizes them; mosh-go keeps one pending
  diff and an ack watermark.
- `fragment.go` implements the same wire format as `transportfragment.cc` but
  uses a simpler assembler with a 1 MiB cap; upstream's `Fragmenter` also
  decides instruction ids from state changes and compresses before fragmenting.
- `framebuffer.go` is a small ANSI diff engine (cells, SGR, cursor). Upstream
  `terminaldisplay.cc` handles titles, icon name, clipboard, reverse video,
  bell, hyperlinks, and mosh's silent-move optimization; mosh-go's model does
  not.
- `predict.go` is a 207-line printable-rune predictor. Upstream
  `terminaloverlay.cc` is roughly 870 lines with epochs, glitch repair,
  flagging hysteresis, cursor prediction, and frame acknowledgements.
- `server.go` uses `unixshells/vt-go` as a terminal emulator; upstream uses its
  own `CompleteTerminal`/`Terminal::Framebuffer` stack.
- `ocb.go` is an RFC 7253 implementation built on Go's `crypto/aes`; upstream
  bundles a C OCB3 reference implementation (`src/crypto/ocb_internal.cc`).

**The sibling project matters.** `mosh-dart` was created the same day, 8
seconds after mosh-go's repository, and its initial commit is 91 seconds after
mosh-go's initial commit, by the same author and organization, also MIT. Its
fragment, protobuf, and OCB files share comments and structure with mosh-go:
for example both carry the exact comment "Mosh layout: [final:1 bit]
[fragment_num:15 bits] (big-endian uint16)" and "Field numbers match upstream
mobile-shell/mosh exactly". See
`mosh-dart/lib/mosh_fragment.dart` and `mosh-dart/lib/mosh_pb.dart` at
`4725f1b`. This is strong evidence of a common in-house MIT lineage rather than
a direct translation of the C++ implementation. It is not proof of independent
creation: both could have been generated from the same GPL source or by the
same AI system.

**Conclusion.** On the evidence available, mosh-go is a wire-compatible
reimplementation that copies protocol facts (field numbers, fragment layout,
constants), not a literal translation of GPL mosh. The MIT declaration is
facially consistent with that. It is not legally proven, and the absence of any
provenance statement or generation record is the main residual risk.

### 2.4 Is the MIT declaration valid?

The repository contains only a standard MIT license:
`Copyright (c) 2026 Unix Shells`, permission grant, notice-retention condition,
and warranty disclaimer (`LICENSE:1-21`). There is no NOTICE file, no
third-party attribution file, and no per-file license header.

Things that could invalidate the declaration for some material:

1. **Non-literal translation of GPL expression.** If any function is a
   line-by-line translation of `transportsender-impl.h`,
   `networktransport-impl.h`, `terminaldisplay.cc`, or `terminaloverlay.cc`,
   the translation is a derivative work and the GPL governs that part. The
   structural differences above make this unlikely for the modules inspected,
   but a similarity audit is the only way to be confident.
2. **The protobuf schema.** `pb.go` mirrors the message hierarchy and field
   numbers of mosh's GPL-licensed `.proto` files. Field numbers and wire types
   are functional interoperability facts; whether the message structure is
   protectable expression is a legal question, not a code-review question.
3. **The OCB implementation.** mosh's bundled OCB3 reference
   (`src/crypto/ocb_internal.cc`) is permissively licensed but requires its
   notice to be retained, and it records a patent grant for OCB. mosh-go's
   `ocb.go` has no OCB attribution. No verbatim copy was found; if it was
   written from the RFC rather than the reference, no notice is required. If
   it was translated from the reference, the OCB notice and patent statement
   are missing.
4. **AI generation.** The initial 5,445-line commit, the same-day sibling
   implementation, and later terse fixes are consistent with heavy AI
   assistance. mosh-go has no disclosure or generation record. An AI system
   reproducing GPL code can create a derivative work regardless of the prompt.
5. **No chain-of-title documentation.** There is no CLA, DCO sign-off, or
   assignment in either mosh-go or mosh-dart. The organization's ownership is
   asserted only by the LICENSE copyright line.

**Assessment:** the MIT declaration is usable only as a starting point. It
should be backed by a written provenance statement and indemnity from Unix
Shells, or by an independent similarity audit, before it is relied on for App
Store distribution.

### 2.5 Test coverage and maturity

Static inventory:

- 88 test functions across eight `_test.go` files; 3,128 lines of tests versus
  3,216 lines of non-test Go. The ratio is healthy.
- Coverage includes RFC 7253 OCB vectors, tamper rejection, fragment
  out-of-order reassembly, protobuf malformed-input cases, transport
  sequencing/retransmission, prediction, server PTY lifecycle, and client
  send/recv.
- Two tests exercise the real C implementation, but only if the binary happens
  to be installed: `client_test.go:226-229` (`TestClientAgainstRealMoshServer`,
  skips when `mosh-server` is absent) and `server_test.go:204-210`
  (`TestWithRealMoshClient`, skips when `mosh-client` is absent). In CI or on a
  developer machine without mosh installed, the headline "wire-compatible"
  claim is not exercised against the reference.
- There is no CI workflow, no fuzz target, and no race-detector job. The README
  says `go test -race ./...` passes, but no automation enforces it.
- The project has been dormant since 2026-04-05. Two server-side issues are
  open: `Server.Close()` panics after `ServeRW` (#4, #5).

Known correctness issues in the audited HEAD (all in the client path unless
noted):

| Issue | Evidence | Impact |
| --- | --- | --- |
| Client always sends `throwaway_num = 0` | `transport.go:181` (`ThrowawayNum: 0, // client doesn't tell server to throw away states`) versus upstream `transportsender-impl.h:314` (`inst.set_throwaway_num(sent_states.front().num)`). Upstream's receiver quenches after 1,024 retained states (`networktransport-impl.h:118-130`). | A C mosh-server never prunes the client's received states. Long sessions can degrade to roughly one accepted state per quench interval. Filed as issue #3; closed by the reporter with an apology, but the code is unchanged. |
| Out-of-order datagrams are dropped, not returned | `transport.go:220-222` returns `nil` for `seq <= seqInMax` with the comment `// replay`. Upstream `network.cc:483-485` explicitly returns the payload for out-of-order packets (while not using them for RTT/targeting), and mosh-go's own descendant MoshCatty lists accepting out-of-order crypto sequence as a "critical fix". | Reordered UDP packets are discarded. SSP retransmission eventually recovers them, but reassembly stalls and latency degrades under reordering. |
| Timestamp reply uses 0 instead of 0xFFFF for "none" | `transport.go:426` sends `t.lastTS`, which is the zero value before the first received packet; `transport.go:438-445` treats reply `0` as "no reply". Upstream uses `uint16_t(-1)` = 0xFFFF (`network.cc:100-111`, `503`), as does MoshCatty (`src/transport.rs:44, 779-784`). | Early packets can carry a bogus reply timestamp, and a genuine 0xFFFF reply is processed as a real sample (then filtered if implausible). RTT estimation is less well defined than upstream. |
| An ack can clear an unsent pending diff | `transport.go:295-301` clears `pendingDiff` when `ackedByRemote >= sentNum`, without checking `diffSent`. Filed as issue #2. `Client.tick()` reduces exposure by calling `SetPending` only when `AckedByRemote() >= SentNum()` (`client.go:230-237`), but the public `Transport` API allows the unsafe sequence. | A keystroke diff can be silently dropped when the transport is driven directly (for example a resume/reattach integration). |
| `Server.Close()` panics after `ServeRW` | Open issues #4 and #5; `server.go` dereferences `s.cmd` when no command was started. | Server-side only; does not affect a client-only integration, but indicates the maintenance state. |

The OCB implementation is hand-rolled cryptographic mode code. Its tests
include RFC 7253 vectors and tamper rejection, but there is no external
cryptographic review. For a production client, the safest path is to replace
`ocb.go` with a reviewed OCB implementation or budget for a focused audit.

### 2.6 The latch extension fields 8/9

mosh-go adds optional fields for the Unix Shells "latch" product:

- `TransportInstruction` field 8, `LatchCaps []byte`, a capability bitfield.
  It is emitted only when non-empty (`pb.go:31`, `173-175`; `transport.go:183`).
- `HostInstruction` and `UserInstruction` field 9, `LatchControl`, containing
  field 10 (`type`) and optional field 11 (`payload`) (`pb.go:34-50`,
  `320-325`, `366-375`, `533-542`). Control types 1-6 are session
  list/switch/create operations (`pb.go:40-47`); `CapSessionControl` is bit 0
  (`pb.go:49-51`).
- The README documents them at `README.md:90-99` as forward-compatible and
  says standard mosh ignores them.

In the built-in client and server, `SetCaps` is never called (`grep` finds it
only in `transport.go` and the protobuf tests), so `LatchCaps` is empty and
field 8 is omitted; `LatchControl` is nil and field 9 is omitted. The
extensions are therefore inert for a plain mosh session. Risks: they are not
standardized, they occupy field numbers that upstream mosh could later assign,
and any future peer that does not preserve unknown protobuf fields would
mis-handle them. If Natcatty does not need latch, strip or ignore them and pin
the behavior with an interop test against stock mosh.

### 2.7 Can mosh-go be the base of the Go mosh client?

Yes, provisionally, as a pinned fork rather than an unmodified dependency.

- It is MIT, pure Go for the client path (`client.go` imports only the
  standard library: `encoding/base64`, `fmt`, `net`, `os`, `sync`, `time`;
  `server.go` is tagged `//go:build !js` and imports `creack/pty`, `os/exec`,
  and `unixshells/vt-go`).
- It exposes a useful client API (`Dial`, `DialConn`,
  `DialConnRaw`, `Send`, `Resize`, `Recv`, `Tick`) and an embeddable `Conn`
  interface for custom transports.
- It needs at least the throwaway, out-of-order, timestamp-reply, and ack-race
  fixes above before it can be trusted for long-lived sessions.
- For iOS/Android, the server half and its `creack/pty` dependency must be
  excluded or the package split; the client path itself is platform-neutral,
  but I did not verify an iOS toolchain build. Go's runtime and `gomobile`
  integration would need a separate architecture and App Store review.
- Dependency notices are also incomplete: `go.mod` lists
  `charmbracelet/x/ansi`, `creack/pty`, `vt-go`, `golang.org/x/crypto`, and
  `golang.org/x/term` (the README's dependency list at `README.md:122-124` is
  stale), and no NOTICE file ships them.

---

## 3. MoshCatty 0.1.8 audit

### 3.1 What the bundled binary actually is

| Item | Value |
| --- | --- |
| Repository | <https://github.com/binaricat/MoshCatty> |
| Release | `moshcatty-0.1.8`, published 2026-07-17T06:27:35Z |
| Tag commit | `554b9d305e7ac4b11de740d764bbc3e05f816d7b` (merge of PR #7) |
| Release workflow run | <https://github.com/binaricat/MoshCatty/actions/runs/29559896639>, `head_sha=554b9d3`, success |
| License | GPL-3.0-or-later (`Cargo.toml:7`, `LICENSE`, `README.md:280-284`) |
| Language | Rust (Cargo package `moshcatty`, binary `src/bin/mosh_client.rs`) |
| Release body | "Pure Rust `mosh-client` for Netcatty." (`release.yml:217`) |

The release workflow checks out the repository and runs `cargo build --release`
on Linux x64 (`release.yml:38,46`), Linux arm64 (`72,82`), macOS
(`108-109`), and Windows (`131`). It never checks out or builds
`mobile-shell/mosh`, and there is no C++ toolchain step. The workflow's publish
job uploads the four archives and `SHA256SUMS` (`release.yml:177+`).

The committed Windows binary was verified end to end:

| Check | Result |
| --- | --- |
| Local `resources/mosh/win32-x64/mosh-client.exe` SHA-256 | `e44616a22038f1742f765c7de553796ba4d1698f2c1f7a7cfb0dafd33ecdc78a` |
| Downloaded `mosh-client-win32-x64.tar.gz` from the 0.1.8 release | SHA-256 `ee437592c351361e47c600fafe04ce97a2551aa61fbef1fb3d6dda565835311e`, matching `BUILD-PROVENANCE.json` and the GitHub release asset digest |
| Extracted binary SHA-256 | `e44616a2...c78a`, byte-identical to the committed file (`cmp` reports no difference) |
| `file` | `PE32+ executable for MS Windows 6.00 (console), x86-64` |
| Strings | `src\bin\mosh_client.rs`, `/rustc/8bab26f4.../library/...`, `quinn-udp-0.5.14`, `aes-0.8.4`, `base64-0.22.1`, `flate2-1.1.9`, `once_cell-1.21.4`, `unicode-width-0.2.2`; these match `Cargo.lock` |
| C++ mosh markers | No `stmclient`, `Display::new_frame`, `MobileShell`, or C++ runtime strings found |

**Conclusion:** the bundled client is MoshCatty, a Rust implementation that is
wire-compatible with stock mosh. `mobile-shell/mosh` is a protocol reference
and behavioral test target, not the source of this binary.

### 3.2 MIT-derived material from mosh-go

MoshCatty's own source and history credit mosh-go, but the credit is scattered
and incomplete. The evidence is stronger than "interop vectors":

| Location | Evidence |
| --- | --- |
| `tests/protocol_suite.rs:1-6` (initial commit and current) | "Cases adapted from: `unixshells/mosh-go` (`ocb_test`, `fragment_test`, `pb_test`, `transport_test`)". This is copied/adapted MIT test code, present from the first MoshCatty commit. |
| `src/fragment.rs:1` (initial commit `4f76b33`) | "Datagram fragmentation (upstream mosh `network.cc` / mosh-go layout)." |
| `src/transport.rs:1-10` (initial commit) | "Matches upstream mobile-shell/mosh framing ... with mosh-go's simplified single-pending send queue. Critical fixes vs early draft: accept out-of-order crypto seq ... emit throwaway_num ... timestamp reply uses 0xFFFF..." |
| `src/pb.rs:1-3` (initial commit) | "Hand-rolled protobuf codecs matching upstream mobile-shell/mosh field numbers. Width/height are proto2 int32 encoded as varints (not zigzag), matching mosh-go." |
| Commit `79d6c55` (2026-07-12) | "feat(prediction): mosh-go Framebuffer + Confirm + Overlay paint path ... Ports mosh-go predict_test cases". The commit adds 452 lines to `src/framebuffer.rs` and 408 to `src/ansi_apply.rs`. |
| `src/framebuffer.rs:1-5` (current) | "Client cell grid + Diff, shaped after mosh-go `framebuffer.go`." |
| `src/prediction.rs:1-4` (current) | "mosh-go pending-list core + stock overlay semantics. Base API matches [mosh-go `predict.go`]..." |
| `src/display.rs:1-4` (current) | "predict/confirm tests stay co-located with the mosh-go port." |
| `README.md:68,78,247,284` | "stock overlay rules on a mosh-go Diff path"; "mosh-go interop vectors in CI"; "interop vectors from mosh-go (MIT)". |

The initial MoshCatty release already depended on mosh-go's design for its
framing, protobuf encoding, and single-pending transport. The later port added
mosh-go's framebuffer and prediction core. This is a derivative of MIT code,
not merely an independent implementation that happened to use the same wire
format.

Compatibility is not the problem: MIT is GPL-compatible, so MoshCatty may be
distributed under GPL-3.0-or-later while containing MIT code. The problem is
notice retention. MIT requires "the above copyright notice and this permission
notice" to be included in all copies or substantial portions. Neither the
MoshCatty repository nor Netcatty's `resources/mosh/` tree contains an MIT
license text or the "Copyright (c) 2026 Unix Shells" notice. The one-line
"interop vectors from mosh-go (MIT)" in `README.md:284` is an attribution, not
the required notice, and it understates the scope by mentioning only vectors.

MoshCatty's own MIT-derived portions cannot be relicensed exclusively by
MoshCatty's owner; they remain available under MIT, and the notice must travel
with them. They can, however, be included in a permissively licensed combined
work, which is exactly what a relicensing effort would need.

### 3.3 License and ownership

- The repository is GPL-3.0-or-later from its first commit: `Cargo.toml:7`,
  `LICENSE`, and `README.md:280-284` all say so. The initial commit
  (`4f76b33`, 2026-07-10) already had this license and the mosh-go test
  attribution.
- The GitHub owner is the same account that owns Netcatty
  (`binaricat/Netcatty`), and the README describes MoshCatty as Netcatty's
  engine. The Cargo authors are `["binaricat", "Netcatty contributors"]`
  (`Cargo.toml:9`).
- Git metadata contains 53 commits. The GitHub contributors API attributes 47
  to the owner account and two each to two anonymous identities, one of which
  is a Windows laptop hostname with a different email. In other words, the
  work is substantially one maintainer's, but not literally single-author.
- There is no `CONTRIBUTING`, CLA, copyright assignment, or DCO file in the
  repository. "Netcatty contributors" is not a legal person that can consent to
  relicensing.
- Pull requests #5, #6, and #7 were merged from branches named
  `codex/fix-2121-high-latency`, `codex/issue-2121-release-hardening`, and
  `codex/fix-backspace-local-echo` (`git log --merges`). Codex is OpenAI's
  coding agent. This is evidence that recent protocol and prediction hardening
  was AI-assisted; it is not evidence of copying, but it is a provenance fact
  an audit must address.

### 3.4 Does MoshCatty contain GPL mosh expression?

MoshCatty is already GPL, so this question does not affect its current
distribution. It matters only for relicensing.

- No literal comment copying was found: the normalized comment comparison
  against upstream `decd9b7` returned 0 matches.
- No upstream C/C++ source files, headers, or generated protobuf files are
  present. The implementation is Rust throughout.
- However, the code was written with upstream open and explicitly aligned to
  it. `src/prediction.rs:6-15` says the prediction rules follow
  `terminaloverlay.cc`/`.h`, and the constants at `src/prediction.rs:26-37`
  (30/20 ms SRTT hysteresis, 80/50 ms flagging, 250 ms glitch, 5,000 ms flag,
  10 repairs, 150 ms repair interval) mirror upstream's parameters.
  `src/transport.rs:31-56` defines `ACK_DELAY` 100 ms, `ACTIVE_RETRY_TIMEOUT`
  10 s, `SHUTDOWN_RETRIES` 16, `SENT_STATE_CAP` 32, `TS_NO_REPLY` 0xFFFF, and
  a 15 s receiver-quench interval, again matching upstream behavior. The
  repository's own `docs/protocol-audit-2026-07-15.md` and
  `docs/protocol-gap-research-2026-07-15.md` cite upstream source line by line
  and describe fixes made to match it.
- Behavioral alignment is not automatically copyright infringement; an
  independent implementation can aim at the same observable behavior. But
  line-by-line study plus close structural correspondence is the fact pattern
  a non-literal-copying claim would target. A clean-room record is the only
  reliable way to negate it.

**Verdict:** there is no positive evidence of literal GPL copying, but there is
also no evidence of clean-room development. The code is GPL today, so the
question is deferred until relicensing. Any audit must examine the prediction,
terminal-display, and transport modules first, because those are the ones most
closely modeled on upstream.

### 3.5 Can MoshCatty be relicensed?

On paper, yes, if all of the following hold:

1. The copyright owner(s) consent. The repository owner appears to be the same
   entity as Netcatty's owner, and most commits are theirs, but the two
   anonymous commits and the AI-assisted commits need written assignment or
   consent.
2. The MIT-derived mosh-go portions retain the MIT notice and are identified in
   the new distribution. MIT permits sublicensing, so the combined work can be
   MIT/Apache-2.0, but the original notice cannot be removed.
3. An audit finds no GPL-derived expression, or the affected modules are
   replaced by a clean-room implementation. If any part is a derivative of
   `mobile-shell/mosh`, that part cannot be relicensed without the upstream
   copyright holders' permission.
4. All Rust dependencies' notices are added. The locked dependency set is
   permissive (MIT, Apache-2.0, BSD-2-Clause, 0BSD, Zlib, Unicode-3.0, and
   Apache-2.0 WITH LLVM-exception), so there is no copyleft blocker, but
   attribution and License/NOTICE texts are still required. `Cargo.lock`
   currently pins 49 third-party packages.
5. Prior GPL releases remain GPL. Relicensing applies only to new versions;
   recipients of `moshcatty-0.1.0` through `0.1.8` keep their GPL rights.

Relicensing is therefore a legal and provenance project, not a `Cargo.toml`
edit. The same analysis applies to dual licensing: the owner can offer
permissive terms for new versions while leaving old releases GPL.

---

## 4. Local provenance files: confirmed errors and gaps

The audit confirms both defects anticipated by the ticket and finds additional
notice gaps.

| File | Current claim | Finding | Correction needed |
| --- | --- | --- | --- |
| `resources/mosh/README.md:12` | `Upstream mosh: https://github.com/mobile-shell/mosh` | MoshCatty is an independent Rust implementation; upstream mosh is a protocol reference, not the binary's source. | Rename to "Protocol reference" and add the direct source (MoshCatty tag/commit). |
| `resources/mosh/README.md:54-58` | "MoshCatty's release workflow builds mosh from upstream `mobile-shell/mosh` ... use the MoshCatty repository at the release tag, or upstream mosh at the version named in MoshCatty's release notes." | False. The workflow runs `cargo build --release` on MoshCatty. No upstream mosh version is named in the 0.1.8 release notes. | Say MoshCatty's Rust source at tag `moshcatty-0.1.8` / commit `554b9d3` is the corresponding source; delete the upstream-source alternative. |
| `resources/mosh/BUILD-PROVENANCE.json:11-14` | `"upstream": {"repository": "mobile-shell/mosh", ...}` | Misidentifies the source. | Either remove this field or mark it explicitly as a protocol reference, and add `source.commit` `554b9d305e7ac4b11de740d764bbc3e05f816d7b`. |
| `resources/mosh/BUILD-PROVENANCE.json:45-49` | `"MoshCatty publishes prebuilt mosh-client binaries from upstream mosh."` | False; they are built from MoshCatty's Rust source with Cargo. | Replace with the Cargo build provenance and tag commit. |
| `resources/THIRD-PARTY-NOTICES.md:11` | `Upstream: https://github.com/mobile-shell/mosh` | Same misidentification. | Mark as protocol reference; name MoshCatty as source. |
| `resources/THIRD-PARTY-NOTICES.md:9-19` | Only GPL-3.0 text and a source-offer sentence. | Missing the MIT notice for mosh-go-derived material; missing notices for the Rust dependency tree. | Add the mosh-go MIT copyright/license text and a dependency notice list (or a generated NOTICE file). |
| `resources/mosh/LICENSE-MoshCatty-GPL-3.0.txt` | GPL-3.0 text only. | Correct for MoshCatty's own license, insufficient for the combined work. | Keep, and add `LICENSE-MoshGo-MIT.txt` or equivalent. |

The existing automated check only looks for presence, not accuracy:
`adapters/bundle/bundle_test.go:212-264` reads `THIRD-PARTY-NOTICES.md` and
requires the substrings `mosh-client`, `GPL-3.0`, `EternalTerminal`, and
`Apache License 2.0`, then checks that each `README.md`, license, and
`BUILD-PROVENANCE.json` exists and that the binary hash matches the manifest.
It never checks that the named upstream is the actual source. That is why the
wrong provenance survived. A corrected check should assert the direct source
repository and commit, and should require the MIT notice file.

The parity matrix repeats the claim that the notices are adequate
(`docs/parity/matrix.yaml:4658` and `:4814`, `docs/parity/matrix.md:129,262`);
those entries also need correction once the files are fixed.

### What a correct notice must contain

For MoshCatty 0.1.8 as bundled:

1. MoshCatty's GPL-3.0-or-later text and the exact source location (tag
   `moshcatty-0.1.8`, commit `554b9d3`).
2. The MIT notice: `Copyright (c) 2026 Unix Shells` plus the full MIT
   permission/warranty text, covering mosh-go-derived code and tests.
3. A third-party notice file for the Rust dependencies in `Cargo.lock`
   (at least the license texts and copyright lines; Apache-2.0 NOTICE files
   where the crate ships them).
4. A statement that `mobile-shell/mosh` is a protocol reference and is not the
   source of the bundled binary.
5. For the App Store, a separate legal conclusion that GPL-3.0-or-later is
   compatible with the distribution terms; today it is not, which is why a
   permissive client is a requirement, not a preference.

---

## 5. Recommendation

### 5.1 Options compared

| Option | Licensing fit | Product fit | Effort | Main risk |
| --- | --- | --- | --- | --- |
| A. Use mosh-go as the audited base | MIT; needs notice + provenance audit | Go only; pure-Go desktop; iOS requires Go runtime/gomobile and package splitting | Low to medium | Dormant project, known SSP bugs, hand-rolled OCB, no CI, iOS integration |
| B. Relicense MoshCatty | GPL today; permissive after owner consent and audit | Rust; already shipped; cross-platform; easiest iOS embedding; prediction/ConPTY work already done | Medium | GPL-derived expression, anonymous contributors, MIT notice, AI-assisted provenance |
| C. Commission a clean-room implementation | Cleanest permissive title | Can target Rust/Swift/Go; full control | High | Cost/time, protocol complexity, must re-prove interop and prediction |

### 5.2 Primary recommendation

**Relicense MoshCatty under a permissive license (MIT or Apache-2.0) for the
cross-platform/iOS client, conditional on a provenance audit, and keep mosh-go
as the audited MIT fallback and interoperability oracle for Go-side work.**

Why this order:

- MoshCatty is the implementation already shipping in Netcatty. It has nine
  releases, CI on three operating systems, ConPTY handling, a prediction
  engine, and cross-platform packaging. That is real product work that a
  from-scratch clean-room would have to repeat.
- It is Rust, which embeds into an iOS app more naturally than a Go runtime.
  The existing macOS universal build shows the cross-compilation path.
- It is owned by the same organization as Netcatty. Ownership consolidation is
  plausible; mosh-go is owned by a different organization (Unix Shells), so
  relying on mosh-go means relying on a third party for provenance and
  maintenance.
- The MIT-derived mosh-go portions are compatible with a permissive outcome;
  they need attribution, not removal.
- mosh-go is a good MIT implementation, but it is dormant, has no CI or
  releases, and has concrete long-session and packet-reordering defects. It is
  better used as a second implementation for tests and for the Go desktop
  path than as the sole client.

The sequence should be:

1. **Immediately** correct `resources/mosh/README.md`,
   `BUILD-PROVENANCE.json`, and `THIRD-PARTY-NOTICES.md`; add the mosh-go MIT
   notice and the dependency notices. This is required whether or not the
   architecture changes.
2. Commission a provenance audit (section 6) of MoshCatty and mosh-go. Include
   a similarity scan against `mobile-shell/mosh` `decd9b7`, the OCB reference,
   and mosh-go.
3. Obtain written copyright assignments/CLAs from every MoshCatty contributor,
   including the anonymous commits, and document the Codex-assisted commits.
4. If the audit clears MoshCatty: relicense new releases under MIT or
   Apache-2.0, keep the MIT notice, add iOS targets, and retire the GPL binary
   from new Netcatty releases. Old releases stay GPL.
5. If the audit flags only specific modules (most likely prediction, terminal
   display, or transport): commission a clean-room replacement for those
   modules and keep the rest. This is cheaper than a full clean-room.
6. If the audit cannot clear the core or ownership fails: use mosh-go for the
   Go client and commission a clean-room Rust/Swift client for iOS. Do not ship
   an unaudited GPL binary into the App Store path.

### 5.3 What not to do

- Do not treat "MoshCatty is GPL-3.0-or-later" as a substitute for the
  mosh-go MIT notice. GPL compatibility governs the combination; it does not
  erase the MIT attribution condition.
- Do not relicense by changing `Cargo.toml` without contributor assignments
  and the audit. That would create a new licensing defect rather than fixing
  one.
- Do not use upstream `mobile-shell/mosh` as the "corresponding source" for
  MoshCatty. It is not the source of the binary and would not satisfy GPL §6
  for MoshCatty's code.
- Do not assume mosh-go is clean because no GPL comment was copied. Non-literal
  translation and AI-generated reproduction remain open until audited.

---

## 6. What a provenance audit must prove

The audit should be independent of the developers and produce a written report
per repository. At minimum it must establish:

1. **Chain of title.** Every commit's author, employer/contractor status,
   assignment or CLA, and the ownership of AI-assisted output. Resolve the two
   anonymous MoshCatty commits and the Codex branches.
2. **No GPL expression in mosh-go.** Compare mosh-go against
   `mobile-shell/mosh` `decd9b7`, the OCB reference, and any other
   permissively licensed source. Use token/identifier/comment/structure
   similarity tools, manual review of the transport, fragment, framebuffer,
   prediction, and OCB modules, and developer attestations about what source
   was read and what tools generated the code.
3. **No GPL expression in MoshCatty (for relicensing).** The same, with special
   attention to `src/prediction.rs`, `src/framebuffer.rs`, `src/ansi_apply.rs`,
   and `src/transport.rs`, which are the modules most closely aligned with
   upstream. Distinguish behavioral compatibility from copied expression.
4. **Identification of all MIT-derived material.** Enumerate every mosh-go
   file/test/vector ported into MoshCatty, confirm the MIT notice covers it,
   and decide whether to keep it under MIT or reimplement it.
5. **Dependency and notice completeness.** Audit the locked Rust crates and Go
   modules, including Apache-2.0 NOTICE files, Unicode-3.0, BSD, 0BSD, and
   Zlib terms. Generate a machine-checkable NOTICE file and assert it in CI.
6. **Binary/source correspondence.** Rebuild from the tag with the pinned
   toolchain and `Cargo.lock`, record the compiler version and build flags, and
   compare with the release asset (or document why reproducibility is not
   achievable and what attestations substitute for it).
7. **Protocol provenance.** Document that field numbers, fragmented layout,
   OCB parameters, and SSP constants were derived from public specifications
   and black-box interoperability, not copied source. Keep the evidence.
8. **Patent and App Store review.** Obtain counsel's view on the OCB patent
   grant (`src/crypto/ocb_internal.cc` header) and on GPL-3.0 versus Apple's
   App Store terms. This is separate from copyright.
9. **Known-bug remediation.** For mosh-go, prove the throwaway, out-of-order,
   timestamp-reply, and ack-race fixes with regression tests and a long-session
   test against a stock `mosh-server`. For MoshCatty, prove the binary's
   behavior matches the audited source and that the prediction path has an
   interop test against stock mosh.

---

## 7. What a clean-room process would need

If the audit cannot clear a module, or if the organization wants a defensible
permissive title without relying on the current code:

1. **Two-team separation.** A specification team may read the GPL source and
   write a behavioral specification. An implementation team must never read
   the GPL source or the existing GPL implementation. Use separate
   repositories, access controls, and signed attestations.
2. **Specification, not pseudocode.** The spec should describe observable
   behavior: wire layout, field numbers, state transitions, timing constants,
   error handling, and interoperability requirements. It must not be a
   line-by-line transcription or a code-shaped pseudocode dump.
3. **Public sources first.** Mosh paper (USENIX ATC 2012), RFC 7253, the
   published protobuf wire format, the mosh man pages, and black-box packet
   captures from a stock client/server. Where the only source is GPL code, the
   spec team records the fact and the implementation team receives only the
   behavioral statement.
4. **Black-box tests.** Interoperate with stock `mosh-client`/`mosh-server`
   binaries as test oracles. Do not copy GPL test code; use RFC 7253 vectors
   and independently generated vectors. Record the stock version and commit.
5. **Independent implementation.** The implementation team writes the client
   from the spec. Keep design documents and review records. Use only
   permissively licensed dependencies, with notices.
6. **Similarity review.** Before release, run code-similarity and
   clone-detection tools against `mobile-shell/mosh`, mosh-go, and MoshCatty.
   Counsel reviews the report and the chain-of-custody records.
7. **Module boundary.** A clean-room can be limited to the suspect modules
   (for example prediction/overlay/terminal display) if the rest of the code
   has a clean title and permissive notices. Define the boundary in writing.
8. **Ongoing hygiene.** Keep a `PROVENANCE.md` and a NOTICE file in the
   repository; require DCO/CLA sign-off; keep AI-generation disclosures; and
   make the notice check part of CI.

---

## Answers

**1. Can `unixshells/mosh-go` (MIT) be used as the base for the Go mosh
client?**

Yes, provisionally. It is MIT-licensed, pure Go for the client path, wire
compatible with stock mosh, and has a substantial test suite (88 tests, about
3,128 test lines). The MIT declaration is facially plausible: the code has no
GPL headers, no copied upstream comments (0 normalized matches), a structurally
different and much simpler SSP than `transportsender-impl.h`, and a same-day
MIT sibling implementation (`mosh-dart`) by the same author. However, it is not
pre-cleared: there is no provenance record, no CI, no release, no activity
after 2026-04-05, and several concrete client defects (`throwaway_num = 0`,
dropped out-of-order datagrams, timestamp-reply 0 instead of 0xFFFF, a latent
ack/pending-diff race, and a server-only `Close` panic). Use it as a pinned fork
after an audit and those fixes, not as an unmodified dependency.

**2. What is the provenance of the bundled MoshCatty client?**

The bundled binary is MoshCatty 0.1.8 from
<https://github.com/binaricat/MoshCatty>, a pure-Rust reimplementation licensed
GPL-3.0-or-later, built by `cargo build --release` in the MoshCatty release
workflow (run 29559896639 at commit `554b9d3`). It is not built from
`mobile-shell/mosh`. The committed Windows binary is byte-identical to the
published 0.1.8 release asset and contains Rust/Cargo paths and no C++ mosh
symbols. It contains MIT-derived material from mosh-go: the protocol test suite
is adapted from mosh-go tests, the initial transport/protobuf/fragment code
describes itself as using mosh-go's layout and single-pending queue, and commit
`79d6c55` ported mosh-go's framebuffer/confirm/overlay prediction path. The MIT
notice is missing from both MoshCatty and Netcatty's distribution.

**3. Are the local provenance files incorrect, and is a notice missing?**

Yes. `resources/mosh/README.md:12,54-58` and
`resources/mosh/BUILD-PROVENANCE.json:11-14,45-49` describe MoshCatty as built
from upstream `mobile-shell/mosh`; that is false.
`resources/THIRD-PARTY-NOTICES.md:11` repeats the upstream error and lists only
GPL-3.0. No MIT notice for mosh-go-derived material exists anywhere under
`resources/`, and no notices exist for the roughly 49 Rust crates statically
linked into the binary. The automated check
(`adapters/bundle/bundle_test.go:212-264`) verifies presence and hashes but not
the accuracy of the upstream claim, so it does not catch the error.

**4. Relicense MoshCatty, use mosh-go, or commission a clean-room?**

Primary: **relicense MoshCatty under MIT or Apache-2.0 after a provenance
audit and copyright consolidation, and add the missing notices now.** It is
the already-shipped, cross-platform, Rust implementation and the best fit for
iOS; the MIT-derived portions are compatible with a permissive outcome. Keep
mosh-go as the audited MIT fallback and interoperability oracle for Go-side
work. Commission a clean-room for the modules the audit cannot clear, or for
the whole client if ownership or provenance fails. A full clean-room from day
one is the most expensive option and is not justified until the audit shows it
is necessary.

---

## Unknowns / needs verification

- **No legal conclusion.** This document is a technical provenance audit, not
  legal advice. Whether mosh-go's or MoshCatty's code is a derivative work, and
  whether the MIT/GPL grants are effective, requires counsel.
- **mosh-go's generation process is undocumented.** The initial 5,445-line
  commit and the same-day `mosh-dart` sibling are consistent with AI-assisted
  generation. No tool, prompt, or generation record is available. A
  similarity audit and developer attestations are needed.
- **mosh-dart itself was not fully audited.** It was cloned and compared for
  lineage only; its provenance has the same open questions as mosh-go. If
  mosh-go is used, audit both.
- **No reproducible build of MoshCatty was attempted.** The release asset is
  byte-identical to the bundled binary and the release workflow ran at
  `554b9d3`, but I did not rebuild from source or verify the Cargo toolchain
  version. Reproducibility and the exact compiler/toolchain remain unverified.
- **MoshCatty's contributor identities are not fully resolved.** Git metadata
  and the GitHub contributors API show a second anonymous author identity and
  a different email; the repository has no CLA/assignment. The legal right to
  relicense is therefore unproven.
- **The scope of the mosh-go port is not fully enumerated.** The source
  headers and commit messages identify framebuffer, prediction, transport,
  protobuf, fragment, and test cases, but a file-by-file provenance map has not
  been produced. This is required for the notice and for relicensing.
- **Whether any MoshCatty code is a non-literal translation of GPL mosh is
  unresolved.** The audit found no literal comment copying and the code is
  Rust, but `src/prediction.rs` and `src/transport.rs` are explicitly aligned
  with `terminaloverlay.cc` and upstream timing constants. A clean-room record
  or a detailed similarity review is needed.
- **mosh-go was not built or tested here.** The Go toolchain is absent on this
  machine, and running freshly cloned external test code was not permitted. The
  test counts and coverage descriptions are static inventory, not a verified
  test run.
- **iOS/Android buildability of mosh-go is unverified.** `server.go` is
  `//go:build !js` and imports `creack/pty`; a client-only iOS build would need
  a package split or build tags. Go runtime/gomobile integration and App Store
  review were not tested.
- **mosh-go's OCB provenance and patent status are unverified.** No verbatim
  copy of the OCB reference was found, but the permissive OCB notice and the
  patent grant statement are absent. Counsel should review the OCB patent grant
  before App Store distribution.
- **mosh-go's dependency notices are incomplete.** `go.mod` lists
  `charmbracelet/x/ansi`, `creack/pty`, `vt-go`, `golang.org/x/crypto`, and
  `golang.org/x/term`; no NOTICE file ships them. The exact notice obligations
  were not enumerated.
- **MoshCatty's Rust dependency notices were checked only via crates.io
  metadata.** All 49 locked packages are permissive, but Apache-2.0 NOTICE
  files, copyright lines, and the Unicode-3.0 terms were not collected.
- **The latch extension fields 8/9 have no public specification.** Only
  mosh-go's README and code define them. Their collision risk with future
  upstream mosh fields and their behavior against non-protobuf-preserving
  peers are unverified.
- **The bundled binary's macOS/Linux variants were not verified in this
  audit.** Only the committed Windows x64 binary was downloaded, compared, and
  inspected. The other three digests are recorded in `BUILD-PROVENANCE.json`
  and match the GitHub release asset digests, but the extracted binaries were
  not inspected.

---

## Sources

Repositories and commits:

- mosh-go: <https://github.com/unixshells/mosh-go>, HEAD
  `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f`, tag `v0.5.2`
  `0284bd652aab3dfff5b0b74945c23901d7537b61`, initial commit
  `bb08f71e30ecaf9b6f1490becde1773ba64df24a`.
- mosh-dart: <https://github.com/unixshells/mosh-dart>, initial commit
  `4725f1bf14d29c2aaa22dba09a9c66075cfa6e6d`.
- MoshCatty: <https://github.com/binaricat/MoshCatty>, tag `moshcatty-0.1.8`
  commit `554b9d305e7ac4b11de740d764bbc3e05f816d7b`, initial commit
  `4f76b33641d58a68f62716bb7b51d1f2faa3505a`, prediction-port commit
  `79d6c55ab7db68d805ef2dfdd615124b199d6668`.
- Upstream mosh: <https://github.com/mobile-shell/mosh>, commit
  `decd9b705eb81626f694335b8d5940538beb06da`.

Key files:

- mosh-go: `fragment.go`, `pb.go`, `transport.go`, `client.go`, `server.go`,
  `ocb.go`, `framebuffer.go`, `predict.go`, `go.mod`, `LICENSE`, `README.md`
  at the HEAD above.
- mosh-dart: `lib/mosh_fragment.dart`, `lib/mosh_pb.dart`,
  `lib/mosh_transport.dart`, `lib/ocb.dart`, `README.md`, `LICENSE` at
  `4725f1b`.
- MoshCatty: `Cargo.toml`, `Cargo.lock`, `LICENSE`, `README.md`,
  `.github/workflows/release.yml`, `.github/workflows/ci.yml`,
  `src/framebuffer.rs`, `src/prediction.rs`, `src/display.rs`,
  `src/transport.rs`, `src/pb.rs`, `src/fragment.rs`,
  `tests/protocol_suite.rs`, `docs/prediction.md`,
  `docs/protocol-audit-2026-07-15.md`,
  `docs/protocol-gap-research-2026-07-15.md` at `554b9d3`; initial versions at
  `4f76b33`.
- Upstream mosh: `src/network/transportfragment.{h,cc}`,
  `src/network/transportsender-impl.h`, `src/network/networktransport-impl.h`,
  `src/network/network.cc`, `src/terminal/terminaldisplay.cc`,
  `src/frontend/terminaloverlay.{h,cc}`, `src/protobufs/*.proto`,
  `src/crypto/ocb_internal.cc`, `COPYING` at `decd9b7`.

Release and issue references:

- MoshCatty 0.1.8 release:
  <https://github.com/binaricat/MoshCatty/releases/tag/moshcatty-0.1.8>.
- MoshCatty release run:
  <https://github.com/binaricat/MoshCatty/actions/runs/29559896639>.
- mosh-go issue #2 (pending-diff races):
  <https://github.com/unixshells/mosh-go/issues/2>.
- mosh-go issue #3 (`throwaway_num = 0`):
  <https://github.com/unixshells/mosh-go/issues/3>.
- mosh-go issue #4 (`Server.Close` panic):
  <https://github.com/unixshells/mosh-go/issues/4>.
- mosh-go issue #5 (fix PR):
  <https://github.com/unixshells/mosh-go/issues/5>.

Local files:

- `resources/mosh/README.md`, `resources/mosh/BUILD-PROVENANCE.json`,
  `resources/mosh/LICENSE-MoshCatty-GPL-3.0.txt`,
  `resources/THIRD-PARTY-NOTICES.md`.
- `adapters/bundle/bundle_test.go:212-264`,
  `adapters/bundle/manifests.go`, `docs/parity/matrix.yaml:4658,4814`,
  `docs/parity/matrix.md:129,262`.
