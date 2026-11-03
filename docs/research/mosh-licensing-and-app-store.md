# GPLv3, App Store, and clean-room constraints for mosh

Status: research / legal-risk assessment — **not legal advice**  
Date: 2026-11-03  
Scope: whether a mosh-compatible client can ship in an iOS App Store app without
inheriting GPLv3, and what a defensible clean-room process requires. This is the
mosh counterpart to the separate EternalTerminal (ET) ticket.

Sources verified against:

- mosh upstream at `decd9b705eb81626f694335b8d5940538beb06da` (2026-03-06);
  latest release `mosh-1.4.0` (2022-10-27).
- MoshCatty release `moshcatty-0.1.8`, commit
  `554b9d305e7ac4b11de740d764bbc3e05f816d7b` (2026-07-17).
- mosh-go at `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f` (2026-04-05).
- Apple App Store Review Guidelines, last updated 2026-10-16.
- Apple Media Services Terms and Conditions (US), last updated 2026-09-14.
- GPLv3, version 3, 29 June 2007.
- RFC 7253, May 2014.
- Everything else is dated inline or retrieved on 2026-11-03.

**Read this as engineering and legal-risk research, not as legal advice.** The
"settled" statements below are statements about what the documents say, not
predictions of how a court would rule. Every conclusion that depends on
enforceability, ownership, or patent scope is marked for counsel.

---

## Answers

### A1. Can a mosh-compatible client ship in an iOS App Store app without inheriting GPLv3?

**Yes — if it is an independently written implementation that is not a copy,
translation, port, or in-process link of GPL code, and it is licensed
permissively.** Copyright protects the particular expression (source code,
comments, documentation, schema text, test harnesses), not the protocol's
functional facts. A clean implementation of the mosh wire protocol can
therefore be MIT/Apache-2.0 and can sit inside a proprietary App Store app.

**No — if "mosh-compatible" is achieved by using, porting, translating, or
linking GPL code.** The three failure modes are:

| Approach | Licensing result | iOS App Store consequence |
|---|---|---|
| Bundle upstream `mosh-client` or MoshCatty as a separate executable, desktop-style | The GPL binary stays GPL; the host app may be an "aggregate" under GPLv3 §5, but the binary must still be conveyed with its license and Corresponding Source | Desktop only in practice. iOS apps are code-signed and sandboxed; the established iOS approach is to compile mosh in-process. The App Store terms still restrict the GPL component. |
| Translate/port GPL C++/Rust into Go, or copy GPL source/comments/test files | The port is a "modified version"/derivative work; GPLv3 §5(c) requires the entire work to be licensed under GPLv3 | The whole app must be GPLv3. App Store distribution then collides with Apple's EULA/Usage Rules (§10 further restrictions) and code signing/DRM (§6 Installation Information). |
| Statically or dynamically link GPL mosh/MoshCatty into the iOS app | The app and the GPL library form a combined work; GPLv3 applies to the whole app | Same collision. Blink Shell's maintainer states this explicitly: "The reason why the GPL still applies is because we have to compile it as a static library" ([mosh-devel, 2016-06-15](https://mailman.mit.edu/pipermail/mosh-devel/2016-June/001343.html)). |

The clean path is an in-process, permissively licensed implementation. It also
satisfies App Store Review Guideline 2.5.2's self-contained requirement, because
the implementation is compiled into the signed app and does not download or
install code ([App Store Review Guidelines, 2.5.2](https://developer.apple.com/app-store/review/guidelines/), last updated 2026-10-16).

The mosh-owned iOS non-assertion in `COPYING.iOS` does **not** make a
proprietary app safe if it links mosh: it is a non-enforcement statement about
the App Store/GPL conflict, not a relicense, and it does not override GPLv3
copyleft or cover the app's own code. It also does not cover MoshCatty, which is
a separate implementation with its own GPL license and no waiver.

### A2. What does a defensible clean-room process require?

A "clean room" is an evidentiary process for proving independent creation. It is
not a legal safe harbor: a court still applies the ordinary copyright tests, and
patents, trademarks, and trade secrets are separate. The minimum defensible
process is:

1. **Define the target in functional terms.** Protocol version (mosh protocol
   v2), `mosh-server` compatibility target (1.4.x), OCB3 parameters, SSP
   behavior, fragmentation, terminal-state synchronization, and prediction
   behavior. Keep the target a specification, not a code shape.
2. **Inventory sources before work starts.** Public specs: the mosh paper
   (USENIX ATC '12), RFC 7253, Protobuf wire-format docs, RFC 1950/1951 (zlib),
   RFC 768/6298/3168 (UDP/RTO/ECN), ECMA-48/xterm control sequences, RFC 3629
   (UTF-8). Permissive code may be used only after a provenance audit. GPL
   source is a "dirty" source.
3. **Separate the teams and the machines.** The "dirty" team may read GPL
   source and writes a functional specification; the "clean" team never reads
   GPL source and implements from the specification. Alternatively, write the
   spec from public documents and black-box interop only. Record access,
   dates, and attestations.
4. **Filter the specification.** Remove source code, comments, identifiers,
   prose, field-name choices, and any expressive selection/arrangement. Record
   only functional requirements and observable behavior. Have counsel review
   the spec boundary.
5. **Implement independently.** No copy/paste, no mechanical translation, no
   code generation from GPL source, no linking to GPL code, and no copying GPL
   test files. A port is a derivative work even if the syntax changes; 17
   U.S.C. §101 expressly lists "translation" as a derivative work, and GPLv3 §0
   defines "modify" to include copying or adapting the work.
6. **Test from the spec, not from the GPL test suite.** Use RFC 7253 Appendix A
   vectors and independently generated mosh vectors; interop-test against a
   stock `mosh-server` (running GPL software is not distribution and does not
   make the implementation a derivative). Do not copy mosh's GPL test source or
   its test data compilation.
7. **Review for similarity and provenance.** Run license/provenance scanning,
   compare the new code against the GPL implementation, review the result with
   counsel, and retain the reports. The documentation package should include a
   bill of materials, source log, spec versions, access logs, contributor
   attestations, similarity reports, and decision records.
8. **Resolve patents and trademarks separately.** Clean-room copyright hygiene
   does nothing for the OCB patent question or the "mosh" name. See §5 and §8.

The mosh-specific complication is that there is **no formal wire specification**.
The paper describes SSP and the cryptographic/datagram design, but the exact
protobuf field numbers, fragmentation details, state-machine edge cases, and
terminal/prediction behavior live in GPL source. The paper even disagrees with
the code on one detail: it says "128-bit nonce" ([mosh paper, §2.2](https://mosh.org/mosh-paper-draft.pdf)),
while the code defines `NONCE_LEN = 12` ([`src/crypto/crypto.h`](https://github.com/mobile-shell/mosh/blob/master/src/crypto/crypto.h)).
A clean-room team therefore needs either (a) a permissively licensed base with
documented provenance, or (b) a disciplined black-box compatibility program and
a written functional spec derived from observed behavior.

### A3. What is the OCB3 patent and license status, and is a pure-Go OCB3 implementation permissible?

**Probably yes, subject to counsel confirming residual patent scope.** The record:

- Mosh uses **AES-128-OCB3**, not OCB2. RFC 7253 (May 2014, Informational,
  CFRG) specifies OCB3, including Appendix A test vectors
  ([RFC 7253](https://www.rfc-editor.org/rfc/rfc7253.txt)). Mosh's own OCB
  reference file is titled "OCB Version 3" and is licensed under a permissive
  ISC-style license (Copyright 2012 Ted Krovetz; Copyright 2022 Google LLC)
  ([`src/crypto/ocb_internal.cc`](https://github.com/mobile-shell/mosh/blob/master/src/crypto/ocb_internal.cc)).
- The OCB inventor, Phillip Rogaway, held U.S. Patent 7,046,802
  (application 09/918,615, "Method and apparatus for facilitating efficient
  authenticated encryption", filed 2001-07-30, granted 2006-05-16). The Google
  Patents record shows status **Expired - Fee Related**, with the legal event
  "PATENT EXPIRED DUE TO NONPAYMENT OF MAINTENANCE FEES" effective
  **2014-05-16** ([US 7,046,802](https://patents.google.com/patent/US7046802B2/en)).
- In a 2021-02-27 CFRG message, the inventor wrote: "I can confirm that I have
  abandoned all OCB patents and placed into the public domain all OCB-related IP
  of mine" ([IETF CFRG archive](https://mailarchive.ietf.org/arch/msg/cfrg/qLTveWOdTJcLn4HP3ev-vrj05Vg/)).
  His OCB page now says: "All OCB-related IP of mine is in the public domain: as
  far as I am concerned, you may use it, without restrictions, with my blessing"
  and "I long ago stopped paying my patent-renewal fees"
  ([OCB licensing page](https://web.cs.ucdavis.edu/~rogaway/ocb/license.htm)).
- Mosh's bundled `ocb-license.html` grants a royalty-free patent license only
  for software distributed under the GNU GPL and "reserves all other rights,
  including without limitation licensing for software not distributed under the
  GNU General Public License" ([mosh `ocb-license.html`](https://github.com/mobile-shell/mosh/blob/master/ocb-license.html)).
  That GPL-only grant would **not** cover a permissive implementation. It is
  irrelevant only if the patents are truly expired or public domain.

So a pure-Go OCB3 implementation is technically and likely legally permissible:
use Go's `crypto/aes`, implement OCB3 per RFC 7253 (12-byte nonce, 16-byte tag,
empty AAD for mosh), and test against RFC 7253 Appendix A. The open items are
the second OCB patent application (09/948,084), any continuations or foreign
counterparts, and the precise legal effect of the inventor's public-domain
statement. Those need counsel; see §8.

### A4. What do existing mosh-compatible implementations and legal analyses show?

| Implementation | License (declared) | Notes |
|---|---|---|
| **mosh-go** (`unixshells/mosh-go`) | **MIT** ([LICENSE](https://github.com/unixshells/mosh-go/blob/main/LICENSE)) | Pure-Go client and server, wire-compatible with C mosh, no CGO. 34 commits, first commit 2026-03-12, latest 2026-04-05. It is the only permissive implementation found. Its comments explicitly cite "mosh network.cc" and "Field numbers match upstream mobile-shell/mosh exactly"; it has no clean-room/provenance statement, and it adds optional "latch" extension fields. MIT is declared, but provenance must be audited before relying on it. |
| **MoshCatty** (`binaricat/MoshCatty`) | **GPL-3.0-or-later** (`Cargo.toml`; README) | Rust client, wire-compatible with stock `mosh-server` 1.4.x, release 2026-07-17. Source comments describe it as a "mosh-go port" and say cases were "adapted from mosh-go"; it ships no iOS waiver and no MIT notice. |
| **mosh-rs** (`Arunjay4213/mosh-rs`) | **GPL-3.0** (GitHub API, 2026-10-13) | "mosh, the mobile shell, rewritten in Rust"; a port-style implementation, hence GPL. |
| **mosh-rs** (`wilsonglasser/mosh-rs`) | **GPL-3.0** (GitHub API, 2026-08-23) | Wire-compatible Rust client for SSP. |
| **Blink Shell** | **GPL-3.0** plus iOS additional terms ([blink.sh/blink-gpl](https://blink.sh/blink-gpl)) | iOS App Store app that statically links mosh; the whole app is GPL and its additional terms include the same kind of iOS non-assertion as mosh's `COPYING.iOS`. This is a GPL-app precedent, not a proprietary-app precedent. |
| **Termius** | Proprietary | Supports mosh, no public source/provenance. It shows a proprietary implementation is possible in the market, but provides no reusable legal analysis. |

Legal analyses found:

- FSF's 2008 statement: iPhone code signing/"tivoization" is incompatible with
  GPLv3 ([FSF, 2008-07-31](https://www.fsf.org/news/iphone-is-incompatible-with-free-software/);
  [FSF article, 2008-07-30](https://www.fsf.org/blogs/community/why-free-software-and-apples-iphone-dont-mix)).
- FSF's GNU Go enforcement: Apple's App Store Terms of Service imposed
  restrictions forbidden by the GPL, and Apple removed GNU Go
  ([FSF, 2010-05-25](https://www.fsf.org/news/2010-05-app-store-compliance/);
  [follow-up, 2010-05-26](https://www.fsf.org/blogs/licensing/more-about-the-app-store-gpl-enforcement)).
  The follow-up states the analysis "would apply to all versions of the GNU GPL
  and AGPL."
- FSF's 2018 retrospective confirms "GNU Go was removed after issues with GPL
  compliance on Apple's end"
  ([FSF, 2018-07-31](https://www.fsf.org/blogs/community/apple-app-store-anniversary-marks-ten-years-of-proprietary-appsploitation)).
- VideoLAN/VLC: VLC for iOS was removed on 2011-01-07 after a GPL complaint; the
  VideoLAN developer wrote that "the incompatibility between the GNU General
  Public License and the AppStore terms of use is resolved - the hard way"
  ([archived Planet VideoLAN, 2011-01-08](https://web.archive.org/web/20110111004349/http://planet.videolan.org/)).
  VLC returned in 2013 after a full rewrite, bi-licensed MPLv2/GPLv2+, with
  MPLv2 applicable for App Store distribution
  ([VideoLAN press release, 2013-07-18](https://www.videolan.org/press/ios2out.html)).
- Blink/mosh-devel: the Blink maintainer describes the static-library linkage and
  GPL consequence, and the project ships GPLv3 plus iOS additional terms
  ([mosh-devel, 2016-06-15](https://mailman.mit.edu/pipermail/mosh-devel/2016-June/001343.html)).

No published legal opinion specific to a mosh protocol clean-room was found.
The closest materials are the FSF App Store analyses, the VideoLAN statements,
and the Blink/mosh-devel licensing discussion. Treat the absence as a research
gap, not as evidence that the path is safe.

### A5. What does this mean for this repository today?

- `resources/mosh/` bundles **MoshCatty 0.1.8**, which is **GPL-3.0-or-later**
  Rust code, not an upstream C++ mosh build. Its `Cargo.toml` says
  `license = "GPL-3.0-or-later"`; its README says the same
  ([MoshCatty 0.1.8](https://github.com/binaricat/MoshCatty/tree/moshcatty-0.1.8)).
- MoshCatty has **no `COPYING.iOS` and no iOS/App Store waiver** — only the GPL
  text in `LICENSE`. Therefore the upstream mosh iOS non-assertion does **not**
  apply to this bundled binary.
- `resources/mosh/README.md` says "MoshCatty's release workflow builds mosh from
  upstream `mobile-shell/mosh`", and `resources/mosh/BUILD-PROVENANCE.json` says
  "MoshCatty publishes prebuilt mosh-client binaries from upstream mosh." Those
  statements are inaccurate: MoshCatty is a Rust implementation, not an upstream
  mosh build. This matters because it could lead the team to assume the mosh
  `COPYING.iOS` waiver covers the bundle. It does not.
- MoshCatty's source comments say it is a "mosh-go port" and that cases were
  "adapted from mosh-go"; it has no MIT notice file, and
  `resources/THIRD-PARTY-NOTICES.md` has no MIT notice for mosh-go-derived
  material. If substantial MIT code or tests were copied, MIT's notice
  requirement is not currently satisfied. Audit and correct this for the desktop
  distribution regardless of the iOS decision.
- `adapters/bundle/bundle.go` and `adapters/bundle/manifests.go` are an integrity
  and resolution layer for desktop binaries (Windows/Linux/macOS). They pin
  SHA-256 and size, but have no iOS target and no license logic. Hash
  verification is orthogonal to licensing.
- `resources/et/` is Apache-2.0; ET is covered by a separate ticket. Apache-2.0
  is permissive and App Store-compatible, but it does not help mosh.
- The host app in this worktree has no root `LICENSE` file, while
  `docs/research/go-mygo-rewrite.md` records the original Netcatty as
  GPL-3.0-or-later. If the native app is a derivative of that GPL original, the
  app itself may already be GPL. That is an adjacent, potentially larger
  App Store issue than mosh; see §8.

**Recommendation in one line:** do not put MoshCatty or upstream mosh into the
iOS app in-process. Use a permissive implementation (audited mosh-go, a
clean-room Go implementation, or a permissively relicensed MoshCatty if
ownership/provenance is clean), keep OCB3 from RFC 7253, and keep the desktop
GPL bundle separate and compliant.

---

## 1. mosh licensing and GPLv3 obligations

### 1.1 Upstream mosh is GPLv3-or-later

Upstream mosh's `COPYING` is the GNU GPL version 3 text, and the source headers
say "either version 3 of the License, or (at your option) any later version"
([`COPYING`](https://github.com/mobile-shell/mosh/blob/master/COPYING);
[`src/network/network.cc`](https://github.com/mobile-shell/mosh/blob/master/src/network/network.cc)).
GitHub reports the repository license as GPL-3.0. The latest release is
`mosh-1.4.0` (2022-10-27)
([release](https://github.com/mobile-shell/mosh/releases/tag/mosh-1.4.0)).

Mosh also carries an OpenSSL linking exception in its source headers: the
copyright holders permit linking with OpenSSL "under certain conditions" and
require the GPL to govern all code other than OpenSSL
([`src/network/network.cc`](https://github.com/mobile-shell/mosh/blob/master/src/network/network.cc)).
The OCB reference file is separately permissive (ISC-style), and the OCB patent
grant is in `ocb-license.html` (see §5).

### 1.2 The GPLv3 terms that matter

The following quotations are from GPLv3, version 3, 29 June 2007
([official text](https://www.gnu.org/licenses/gpl-3.0.en.html); identical text in
mosh's `COPYING`).

**Derivative/porting.** GPLv3 §0:

> To "modify" a work means to copy from or adapt all or part of the work in a
> fashion requiring copyright permission, other than the making of an exact
> copy. The resulting work is called a "modified version" of the earlier work or
> a work "based on" the earlier work.

**Whole-work copyleft for modified versions.** GPLv3 §5(c):

> You must license the entire work, as a whole, under this License to anyone who
> comes into possession of a copy. This License will therefore apply, along with
> any applicable section 7 additional terms, to the whole of the work, and all
> its parts, regardless of how they are packaged.

**Aggregation.** GPLv3 §5, final paragraph:

> A compilation of a covered work with other separate and independent works,
> which are not by their nature extensions of the covered work, and which are
> not combined with it such as to form a larger program, in or on a volume of a
> storage or distribution medium, is called an "aggregate" if the compilation
> and its resulting copyright are not used to limit the access or legal rights
> of the compilation's users beyond what the individual works permit. Inclusion
> of a covered work in an aggregate does not cause this License to apply to the
> other parts of the aggregate.

**Object code and Installation Information.** GPLv3 §6 requires
Corresponding Source when conveying object code, and, for object code conveyed
"in, or with, or specifically for use in, a User Product," requires Installation
Information sufficient to install and execute modified versions. The definition
of Installation Information includes "any methods, procedures, authorization
keys, or other information required to install and execute modified versions of
a covered work in that User Product."

**No further restrictions.** GPLv3 §7:

> All other non-permissive additional terms are considered "further
> restrictions" within the meaning of section 10.

GPLv3 §10:

> You may not impose any further restrictions on the exercise of the rights
> granted or affirmed under this License.

**Patents.** GPLv3 §11 grants a non-exclusive, worldwide, royalty-free patent
license from each contributor under its essential patent claims, and imposes
conditions on conveying a covered work while knowingly relying on a patent
license.

### 1.3 What this means for each use mode

- **Porting/translating mosh or MoshCatty into Go** is a modified version. The
  port must be GPLv3, and the entire distributed work must be GPLv3. A
  proprietary iOS app cannot absorb the port.
- **Statically or dynamically linking GPL mosh/MoshCatty into the app** makes a
  combined work. GPLv3 §5(c) applies to the whole app. Blink's maintainer's
  statement that "the GPL still applies because we have to compile it as a
  static library" is a direct, real-world confirmation
  ([mosh-devel, 2016-06-15](https://mailman.mit.edu/pipermail/mosh-devel/2016-June/001343.html)).
- **Bundling a separate GPL executable** (the current desktop design) may be an
  aggregate, so the host app need not become GPL. But the GPL binary is still
  conveyed, so its license, source, notices, and Corresponding Source
  obligations apply. The aggregate theory is also fact-specific; do not assume
  it covers a tightly integrated UI/terminal session without counsel.
- **Reading GPL source to understand behavior** is not restricted by the GPL,
  but the resulting implementation must still be independent; a detailed
  translation or a spec that copies expressive structure can be a derivative.
  See §4.

### 1.4 The mosh `COPYING.iOS` non-assertion

Upstream mosh added `COPYING.iOS` on 2012-07-20 in commit
`19e7bf9744c1396087046f01bc4d0f2668ae2373` ("Add iOS waiver and new OCB
license"), together with the OCB license note. The full text says:

> The Mosh developers are aware that the terms of service that apply to apps
> distributed via Apple's App Store services may conflict with rights granted
> under Mosh's license, the GNU General Public License, version 3. The Mosh
> copyright holders do not wish this conflict to prevent the otherwise-compliant
> distribution of Mosh-derived apps via the App Store. Therefore, we have
> committed not to pursue any license violation that results solely from the
> conflict between the GPLv3 and the Apple App Store terms of service. In other
> words, as long as you comply with the GPL in all other respects, including its
> requirements to provide users with source code and the text of the license, we
> will not object to your distribution of Mosh through the App Store.

([mosh `COPYING.iOS`](https://github.com/mobile-shell/mosh/blob/master/COPYING.iOS).)

What it is and is not:

- It is a **non-assertion commitment** by the upstream mosh copyright holders,
  not a relicense and not an amendment to GPLv3. It does not grant Apple or the
  distributor any new copyright license; it says the mosh copyright holders will
  not object to the App Store/GPL conflict if the distributor otherwise complies.
- It is expressly conditioned on complying with the GPL in all other respects,
  including source and license text.
- It does not bind every contributor in a proven way. The file speaks of "The
  Mosh copyright holders"; the project's AUTHORS file lists several developers,
  and no contributor license agreement is evident in the repository. Whether
  the statement binds all copyright holders is a counsel question.
- It does not cover MoshCatty. MoshCatty is a separate Rust implementation with
  its own GPL-3.0-or-later license and no `COPYING.iOS`.
- It does not cure GPLv3 copyleft for the host app. If the host app links mosh,
  the app is a combined work and must be GPL; the non-assertion only addresses
  the App Store/GPL conflict for the mosh code.
- It does not solve App Store Review Guideline 2.5.2, code signing, trademark,
  patents, or export compliance.

The OCB license note in the same commit records that the inventor licensed OCB
in mosh under the GPL with the OpenSSL linking exception and the `COPYING.iOS`
waiver: "Mosh with the two GPL exemptions you specify in the attached note is
freely licensed to use for any OCB-related IP that I own"
([`ocb-license.html`](https://github.com/mobile-shell/mosh/blob/master/ocb-license.html)).
That is a mosh-specific patent grant, not a general permissive grant.

### 1.5 MoshCatty is GPL, and the repository's provenance docs are wrong

MoshCatty is a Rust implementation, not an upstream mosh build:

- [`Cargo.toml`](https://github.com/binaricat/MoshCatty/blob/moshcatty-0.1.8/Cargo.toml)
  declares `license = "GPL-3.0-or-later"`.
- The README says "GPL-3.0-or-later — same family as Mosh and Netcatty"
  ([MoshCatty README](https://github.com/binaricat/MoshCatty/tree/moshcatty-0.1.8)).
- The source is Rust (`src/*.rs`), with modules for `crypto`, `fragment`, `pb`,
  `transport`, `terminal`, `framebuffer`, and `prediction`.
- Its only license file is `LICENSE` (GPLv3 text). It has no `COPYING.iOS`, no
  App Store waiver, and no MIT notice file.

The source comments describe the code as a "mosh-go port" and say cases were
"adapted from mosh-go"; the README says "Protocol references: Mosh paper /
upstream field numbers; interop vectors from mosh-go (MIT)". Mosh-go is MIT, so
GPL relicensing of MIT-derived material is permitted, but the MIT copyright and
permission notice must be preserved for substantial copied portions. MoshCatty
does not appear to include that notice, and neither does this repository's
`resources/THIRD-PARTY-NOTICES.md`. This is a compliance item to audit for the
current desktop distribution, independent of the iOS question.

It also means MoshCatty's GPL may be the author's licensing choice rather than
an inherited obligation: if the Rust code is an independent port of MIT mosh-go
and does not copy mosh C++ code, the authors could in principle relicense it
permissively. That is an option, not a conclusion; ownership and provenance must
be audited (see §8).

---

## 2. Apple's terms and why GPLv3 conflicts with App Store distribution

### 2.1 Guideline 2.5.2 is a separate constraint, not the GPL conflict

The App Store Review Guidelines, last updated 2026-10-16, say in 2.5.2:

> Apps should be self-contained in their bundles, and may not read or write data
> outside the designated container area, nor may they download, install, or
> execute code which introduces or changes features or functionality of the app,
> including other apps. Educational apps designed to teach, develop, or allow
> students to test executable code may, in limited circumstances, download code
> provided that such code is not used for other purposes. Such apps must make
> the source code provided by the app completely viewable and editable by the
> user.

([App Store Review Guidelines, 2.5.2](https://developer.apple.com/app-store/review/guidelines/).)

This is an **engineering** rule: the app must be self-contained, and it may not
download/install/execute code that changes its features. An in-process,
compiled-in mosh implementation satisfies it. A design that downloads a mosh
binary or plugin at runtime does not. The guideline does not mention GPL, and
the GPL conflict is not 2.5.2; the conflict is with the App Store's license and
usage terms. But 2.5.2 independently rules out the "download a separate mosh
binary" shortcut and reinforces the in-process architecture.

Guideline 5.2.1 adds:

> Generally: Don't use protected third-party material such as trademarks,
> copyrighted works, or patented ideas in your app without permission, and don't
> include misleading, false, or copycat representations, names, or metadata in
> your app bundle or developer name. Apps should be submitted by the person or
> legal entity that owns or has licensed the intellectual property and other
> relevant rights.

([App Store Review Guidelines, 5.2.1](https://developer.apple.com/app-store/review/guidelines/).)

A GPL distributor relies on the GPL as its permission. If it does not comply
with the GPL, 5.2.1 is a separate review/removal risk. Apple does not appear to
police GPL in App Review; enforcement is usually complaint-driven (see §2.3).

### 2.2 The Standard EULA and Usage Rules impose further restrictions

Apple's Standard EULA for App Store apps grants a limited, **nontransferable**
license:

> Licensor grants to you a nontransferable license to use the Licensed
> Application on any Apple-branded products that you own or control and as
> permitted by the Usage Rules. ... You may not transfer, redistribute or
> sublicense the Licensed Application ... You may not copy (except as permitted
> by this license and the Usage Rules), reverse-engineer, disassemble, attempt
> to derive the source code of, modify, or create derivative works of the
> Licensed Application ...

([Apple Standard EULA](https://www.apple.com/legal/internet-services/itunes/dev/stdeula/),
retrieved 2026-11-03; the page shows no revision date.)

Apple's Media Services Terms and Conditions (US), last updated 2026-09-14, add
Usage Rules:

> You may use the Services and Content only for personal, noncommercial
> purposes ... You may not tamper with or circumvent any security technology
> included with the Services or Content. You may access our Services only using
> Apple's software, and may not modify or use modified versions of such
> software.

([Apple Media Services Terms and Conditions, §F](https://www.apple.com/legal/internet-services/itunes/us/terms.html).)

The same terms allow Apple to terminate the license and remove content:

> If you fail, or Apple suspects that you have failed, to comply with any of the
> provisions of this Agreement, Apple may, without notice to you: (i) terminate
> this Agreement and/or your Apple Account ... and/or (ii) terminate your
> license to the software ...

These are the "further restrictions" the FSF points to. They limit devices,
redistribution, modification, and derivative works, and they give Apple
termination/removal power. GPLv3 §10 says a distributor "may not impose any
further restrictions on the exercise of the rights granted or affirmed under
this License." GPLv3 §7 labels non-permissive additional terms "further
restrictions." For GPLv3, the additional §6 Installation Information
requirement is also implicated by Apple's code signing and DRM: the distributor
cannot give recipients the information/keys needed to install a modified
version on the iPhone.

### 2.3 FSF statements and App Store precedents

**FSF, 2008.** The FSF's statement on the iPhone describes the code-signing model:

> In order for any program to be installed on the iPhone, the program must be
> cryptographically signed. ... If the software has been modified in any way,
> the signature check will fail. ... This system of rejecting software that
> doesn't pass a signature check — even when modifications to the software are
> legally permitted — was made famous by TiVo, and so is called "tivoization".
> Apple forces all programs distributed through the iTunes App Store to be
> signed in this way.

([FSF, "Why free software and Apple's iPhone don't mix", 2008-07-30](https://www.fsf.org/blogs/community/why-free-software-and-apples-iphone-dont-mix);
see also [FSF news, 2008-07-31](https://www.fsf.org/news/iphone-is-incompatible-with-free-software/).)

The FSF's position is that this restriction is incompatible with GPLv3's
requirement that users be able to install and run modified versions. This is the
GPLv3 §6 Installation Information argument.

**FSF and GNU Go, 2010.** The FSF wrote to Apple about GNU Go:

> The primary problem is that Apple imposes numerous legal restrictions on use
> and distribution of GNU Go through the iTunes Store Terms of Service, which is
> forbidden by section 6 of GPLv2. ... Unfortunately, instead of amending their
> terms of service to work with the GNU GPL, Apple have decided to remove GNU Go
> from the App Store.

([FSF, 2010-05-25](https://www.fsf.org/news/2010-05-app-store-compliance/).)

The follow-up explains the mechanism and states the analysis "would apply to all
versions of the GNU GPL and AGPL" ([FSF, 2010-05-26](https://www.fsf.org/blogs/licensing/more-about-the-app-store-gpl-enforcement)).
This is an FSF position and an enforcement outcome, not a court ruling.

**VLC, 2011/2013.** VLC for iOS was removed from the App Store on 2011-01-07
after a GPL complaint. The VideoLAN developer wrote:

> On January 7th, Apple removed VLC media player from its application store for
> iDevices. Thus the incompatibility between the GNU General Public License and
> the AppStore terms of use is resolved - the hard way.

([archived Planet VideoLAN, 2011-01-08](https://web.archive.org/web/20110111004349/http://planet.videolan.org/).)

The same developer's 2011-01-09 post cautions that Apple never gave a definitive
reason for the removal; the attribution to GPL is widely believed but not
established by Apple. The salient precedent is the outcome: a GPL app was
removed after a contributor's complaint.

VLC returned in 2013 after a full rewrite, bi-licensed MPLv2/GPLv2+, with MPLv2
applicable for App Store distribution:

> This is more than an upgrade of the initial version: it's a full re-write. ...
> VLC for iOS is fully open-source. Its code will be available online by
> tomorrow and is bi-licensed under both the Mozilla Public License Version 2 as
> well as the GNU General Public License Version 2 or later. The MPLv2 is
> applicable for distribution on the App Store.

([VideoLAN press release, 2013-07-18](https://www.videolan.org/press/ios2out.html).)

This is the clearest precedent for a **relicensing/rewrite** solution: MPLv2 is
file-level copyleft and is treated as App Store-compatible. It does not help a
proprietary app that wants to link GPL code, but it is an option for code the
project owns.

**Blink Shell, 2016–present.** Blink Shell is a GPLv3 iOS app on the App Store.
Its additional terms include an iOS non-assertion nearly identical to mosh's
`COPYING.iOS`:

> The Blink Shell developers are aware that the terms of service that apply to
> apps distributed via Apple's App Store services may conflict with rights
> granted under Blink Shell's license, the GNU General Public License, version
> 3. The Blink Shell copyright holders do not wish this conflict to prevent the
> otherwise-compliant distribution of Blink Shell-derived apps via the App
> Store. Therefore, we have committed not to pursue any license violation that
> results solely from the conflict between the GPLv3 and the Apple App Store
> terms of service.

([Blink Shell Terms of Use, copyright 2022](https://blink.sh/blink-gpl).)

Blink statically links mosh and is GPL as a whole. Its maintainer's 2016
mosh-devel explanation is the best public description of the technical/legal
mechanics:

> The reason why the GPL still applies is because we have to compile it as a
> static library. ... I feel comfortable (and even relieved) with doing Blink
> GPL too.

([mosh-devel, 2016-06-15](https://mailman.mit.edu/pipermail/mosh-devel/2016-June/001343.html).)

Blink is a precedent for **a GPL app with a copyright-holder iOS waiver**, not
for a proprietary app that links GPL mosh. The waiver is a non-assertion, and
the app's own code is GPL.

**What is not established.** To our knowledge, no court has ruled that the
current App Store terms are "further restrictions" under GPLv3. Apple does not publish a GPL policy, and
the App Review Guidelines do not mention GPL. The risk is real but
complaint-driven, as VLC and GNU Go show; it is not a guaranteed App Review
rejection. That uncertainty is for counsel, not for engineering to resolve.

---

## 3. Can an independent implementation be permissively licensed?

### 3.1 Protocol facts vs copyrightable expression

U.S. copyright does not protect ideas, procedures, processes, systems, methods of
operation, concepts, principles, or discoveries:

> In no case does copyright protection for an original work of authorship extend
> to any idea, procedure, process, system, method of operation, concept,
> principle, or discovery, regardless of the form in which it is described,
> explained, illustrated, or embodied in such work.

(17 U.S.C. §102(b), [Copyright Office](https://www.copyright.gov/title17/92chap1.html).)

A protocol's wire format is functional. In the mosh case, the following are
facts/procedures, not protected expression:

- UDP datagram layout and the three SSP modules (cryptographic, datagram,
  transport) ([mosh paper, §2](https://mosh.org/mosh-paper-draft.pdf)).
- AES-128-OCB3 with a 12-byte nonce and 16-byte tag, empty AAD; the 63-bit
  sequence number plus direction bit; the datagram fields
  (`direction`, `seq`, `timestamp`, `timestamp_reply`, `payload`).
- Protocol version 2 ([`src/network/network.h`](https://github.com/mobile-shell/mosh/blob/master/src/network/network.h));
  the `MOSH CONNECT <port> <key>` line
  ([`src/frontend/mosh-server.cc`](https://github.com/mobile-shell/mosh/blob/master/src/frontend/mosh-server.cc)).
- The TransportInstruction field numbers and semantics (old/new/ack/throwaway
  number plus diff) ([`src/protobufs/transportinstruction.proto`](https://github.com/mobile-shell/mosh/blob/master/src/protobufs/transportinstruction.proto);
  [mosh paper, Figure 4](https://mosh.org/mosh-paper-draft.pdf)).
- Fragmentation above 1,400 bytes; SSP's idempotent diffs; ack/throwaway
  semantics; RTO and timestamp behavior.
- Terminal-emulation behavior that follows public standards (ECMA-48, xterm
  control sequences, UTF-8) and mosh's observable prediction behavior.

The following are expression and must not be copied:

- C++/Rust/Go source, comments, identifiers, file structure, and error strings.
- The `.proto` file text, including comments and the selection/arrangement of
  names and fields. The field numbers are facts, but a verbatim schema file is
  not.
- The mosh man pages and README prose.
- Test harnesses, scripts, and the selection/arrangement of test vectors.
- Mosh's terminal parser/framebuffer code and its detailed prediction/overlay
  implementation.

Two important legal boundaries:

- **Translation is a derivative work.** 17 U.S.C. §101 defines a derivative work
  as "a work based upon one or more preexisting works, such as a translation,
  musical arrangement, ... or any other form in which a work may be recast,
  transformed, or adapted" ([Copyright Office](https://www.copyright.gov/title17/92chap1.html)).
  GPLv3 §0 defines "modify" as copying or adapting the work. A line-by-line Go
  port of C++ mosh is a modified version, not an independent implementation.
- **Functional elements get thin protection, but not zero.** The Second Circuit's
  abstraction-filtration-comparison test filters out elements dictated by
  efficiency, external factors, or standard techniques, and recognizes that
  "the fact that two programs contain the same efficient structure may as
  likely lead to an inference of independent creation as it does to one of
  copying"
  ([Computer Assocs. Int'l v. Altai, 982 F.2d 693 (2d Cir. 1992)](https://law.resource.org/pub/us/case/reporter/F2/982/982.F2d.693.91-7935.91-7893.762.html)).
  That helps a genuine independent implementation, but it does not license
  copying the expression.

Reverse engineering to learn functional elements can be fair use. In *Sega
Enterprises v. Accolade*, the Ninth Circuit held that disassembling object code
to understand the unprotected functional requirements for compatibility was fair
use where there was no other way to access those elements
([977 F.2d 1510 (9th Cir. 1992)](https://law.resource.org/pub/us/case/reporter/F2/977/977.F2d.1510.92-15655.html);
[U.S. Copyright Office summary](https://www.copyright.gov/fair-use/summaries/segaenters-accolade-9thcir1992.pdf)).
*Sega* supports a black-box/interop study, not wholesale copying of code or
tests.

### 3.2 The mosh specification gap

There is no IETF RFC for the mosh wire protocol. The public paper describes SSP
and the design, but not the exact on-the-wire details:

- It says AES-128 in OCB mode and a "128-bit nonce", while the implementation
  defines `NONCE_LEN = 12`; OCB3 in mosh uses a 12-byte nonce
  ([mosh paper, §2.2](https://mosh.org/mosh-paper-draft.pdf);
  [`src/crypto/crypto.h`](https://github.com/mobile-shell/mosh/blob/master/src/crypto/crypto.h);
  [`src/crypto/ocb_internal.cc`](https://github.com/mobile-shell/mosh/blob/master/src/crypto/ocb_internal.cc)).
- It gives the TransportInstruction fields but not their protobuf field numbers;
  the numbers are in the GPL `.proto` files.
- It describes fragmentation above 1,400 bytes and the transport state machine,
  but not every edge case, timeout, ECN behavior, or prediction/overlay rule.
- The terminal parser/framebuffer/prediction code is a large, mosh-specific
  behavior surface; public terminal standards do not fully specify it.

This is the central clean-room risk: a team that starts from the paper alone
will not produce a wire-compatible client; a team that starts from the GPL
source must maintain a strict dirty/clean separation, and a team that copies
enough detail to be compatible may create a derivative work. The practical
solutions are:

1. Use a permissively licensed implementation with documented provenance
   (currently only mosh-go claims MIT; audit it).
2. Commission a clean-room specification and implementation with a documented
   dirty-team/clean-team process, black-box interop against `mosh-server`, and
   counsel review.
3. Re-license an existing implementation you own (MoshCatty) if its provenance
   and ownership are clean, preserving third-party notices.

### 3.3 A defensible clean-room record

A process that is likely to be defensible as evidence of independent creation
should produce, at minimum:

- A written **scope and compatibility target**: mosh 1.4.x, protocol v2, OCB3,
  SSP, fragmentation, terminal/prediction behavior.
- A **source inventory** with licenses and dates: mosh paper, RFC 7253, protobuf
  encoding, zlib RFCs, UDP/RTO/ECN RFCs, ECMA-48/xterm, UTF-8; any permissive
  code; the GPL source as "dirty" input only.
- **Team separation**: dirty team reads GPL source and writes a functional
  specification; clean team never reads GPL source. Separate repositories,
  machines, and access controls; dated attestations.
- **Specification hygiene**: remove code, comments, identifiers, prose, and
  expressive selection/arrangement; record only functional requirements and
  observed behavior; counsel review.
- **Independent implementation**: no copy/paste, no mechanical translation, no
  generated code from GPL source, no linking, no copied GPL tests.
- **Independent tests**: RFC 7253 Appendix A vectors; mosh-specific vectors
  generated from the spec/black-box; interop tests against stock `mosh-server`;
  do not copy mosh's GPL test files.
- **Similarity/provenance review**: license scanning, code comparison, manual
  review, counsel review; retain the reports.
- **Documentation**: bill of materials, source log, access log, spec versions,
  contributor attestations, decision records, and the final similarity report.

### 3.4 What clean-room does not solve

- **It is not a statutory safe harbor.** Independent creation is a defense to
  copyright copying, but a court can still find substantial similarity. *Altai*
  itself is instructive: the rewrite avoided copyright in the literal code, but
  the court remanded a trade-secret claim because the rewrite was conducted with
  actual knowledge of the prior misappropriation
  ([Altai, 982 F.2d 693](https://law.resource.org/pub/us/case/reporter/F2/982/982.F2d.693.91-7935.91-7893.762.html)).
  For mosh the source is public under GPL, so trade-secret risk is lower than in
  *Altai*, but the principle stands: process is evidence, not immunity.
- **It does not address patents.** OCB is a separate patent question (§5).
- **It does not address trademarks.** The "mosh" name and any logo are separate.
- **It does not make GPL code usable.** If the implementation copies or links
  GPL code, clean-room paperwork does not change the license.
- **It does not fix a tainted permissive base.** An MIT declaration is only as
  good as its provenance. If mosh-go translated GPL code, its MIT license may
  not be valid for the tainted portions.

---

## 4. OCB3: patents, licenses, and a pure-Go implementation

### 4.1 What mosh uses

Mosh uses AES-128-OCB3. The mosh paper says "AES-128 in the Offset Cookbook
(OCB) mode" and describes the sequence-number/direction nonce construction
([mosh paper, §2.2](https://mosh.org/mosh-paper-draft.pdf)). The implementation
uses OCB Version 3 (the RFC 7253 variant), a 12-byte nonce, a 16-byte tag, and
empty AAD ([`src/crypto/ocb_internal.cc`](https://github.com/mobile-shell/mosh/blob/master/src/crypto/ocb_internal.cc);
[`src/crypto/crypto.h`](https://github.com/mobile-shell/mosh/blob/master/src/crypto/crypto.h);
MoshCatty's protocol audit records the same parameters). Do not implement OCB2:
OCB2 is a different, older mode with known forgery issues; mosh's reference code
is OCB3.

### 4.2 Patent record

- U.S. Patent 7,046,802, application 09/918,615, "Method and apparatus for
  facilitating efficient authenticated encryption", inventor Phillip Rogaway,
  filed 2001-07-30, granted 2006-05-16, assignee "Individual". Google Patents
  status: **Expired - Fee Related**; legal event "PATENT EXPIRED DUE TO
  NONPAYMENT OF MAINTENANCE FEES" effective 2014-05-16; the page also lists an
  adjusted expiration of 2023-11-20. The 2023 date is the nominal term; the
  2014 lapse event is the operative record
  ([US 7,046,802](https://patents.google.com/patent/US7046802B2/en)).
- IETF IPR disclosure #559 lists application 09/918,615 and says "Some uses (eg,
  GNU GPL code) free; other uses require a small one-time fee"
  ([IETF IPR 559](https://datatracker.ietf.org/ipr/559/), submitted 2005-04-06).
- The inventor's 2021-02-27 CFRG message: "I can confirm that I have abandoned
  all OCB patents and placed into the public domain all OCB-related IP of mine"
  ([CFRG archive](https://mailarchive.ietf.org/arch/msg/cfrg/qLTveWOdTJcLn4HP3ev-vrj05Vg/)).
- The OCB licensing page: "All OCB-related IP of mine is in the public domain:
  as far as I am concerned, you may use it, without restrictions, with my
  blessing" and "I long ago stopped paying my patent-renewal fees"
  ([OCB page](https://web.cs.ucdavis.edu/~rogaway/ocb/license.htm)).

The mosh `ocb-license.html` grant is narrower: it grants a royalty-free license
"if practiced by software distributed under the terms of any version of the GNU
General Public License" and "reserves all other rights, including without
limitation licensing for software not distributed under the GNU General Public
License" ([mosh `ocb-license.html`](https://github.com/mobile-shell/mosh/blob/master/ocb-license.html)).
The grant lists both applications, 09/918,615 and 09/948,084. A permissive
implementation cannot rely on that grant. It can rely on the patent
expiration/public-domain statements if they are effective. That is the counsel
question.

### 4.3 Is a pure-Go OCB3 implementation permissible?

Technically, yes: Go's standard library has AES; OCB3 is a mode built from AES
and public operations; RFC 7253 gives the algorithm and test vectors. Mosh's
reference code is permissively licensed (ISC-style), so copying that C code into
Go would not be a GPL problem, but a translation would still be a derivative of
the reference code's expression unless done independently; writing from RFC 7253
is cleaner. A pure-Go implementation can be MIT/Apache-2.0.

The remaining risks are legal, not technical:

- Whether the 2021 public-domain statement covers all OCB-related rights,
  including the second application (09/948,084), continuations, divisionals, and
  foreign counterparts.
- Whether any assignee or successor holds rights the inventor could not
  disclaim. The Google Patents record lists the assignee as "Individual"; the
  IETF IPR lists the patent holder as the inventor. That is helpful but not a
  legal opinion.
- Whether the GPL-only grant in mosh matters if any patent residue remains.
- Export-control/App Store encryption declarations for AES-128-OCB. This is
  likely a mass-market exemption question, but it is separate from copyright.

**Bottom line:** a pure-Go OCB3 implementation is a reasonable engineering
choice; have counsel confirm the patent/public-domain analysis and complete the
patent search before relying on it.

---

## 5. Existing implementations and what they teach

### 5.1 mosh-go (MIT) is the only permissive base found

`unixshells/mosh-go` declares MIT in its `LICENSE` and implements a pure-Go
client and server, wire-compatible with the C implementation. It has 34 commits,
from 2026-03-12 to 2026-04-05, a single author, no CGO, and tests against the C
reference implementation ([repository](https://github.com/unixshells/mosh-go);
[README](https://github.com/unixshells/mosh-go/blob/main/README.md)). Its files
cover OCB3, SSP transport, fragmentation, protobuf encoding, framebuffer, and
prediction.

Provenance concerns:

- The README does not claim clean-room provenance; it says "Wire-compatible with
  the standard C mosh implementation."
- `fragment.go` says "Fragment wire format (mosh network.cc)"; `pb.go` says
  "Field numbers match upstream mobile-shell/mosh exactly." Those are facts, but
  they show the author consulted the GPL implementation. That is not itself
  infringement, but it weakens a bare "MIT therefore clean" assumption.
- It adds optional "latch" extension fields 8 and 9. Standard mosh ignores
  unknown protobuf fields, so the core client should interoperate, but a
  strict-compatibility implementation should not depend on the extensions.
- Its dependencies (`charmbracelet/x/ansi`, `creack/pty`, `unixshells/vt-go`,
  `golang.org/x/crypto`, `golang.org/x/term`) need a license BOM; they appear
  permissive but should be audited.

If mosh-go's provenance is clean, it is the fastest path: fork it, preserve the
MIT notice, add a mosh 1.4.x compatibility suite, and use it in-process. If its
provenance is not clean, its MIT declaration is not a substitute for a
clean-room implementation.

### 5.2 MoshCatty (GPL) is the current bundle, but not usable in the iOS app

MoshCatty is a Rust client, wire-compatible with stock `mosh-server` 1.4.x. It
declares GPL-3.0-or-later, has no iOS waiver, and its source comments show
mosh-go influence. It is the bundled binary in `resources/mosh/` and is fine for
the desktop distribution only if the GPL obligations (source, license, notices)
are met. It cannot be linked into a proprietary iOS app without making the app
GPL. Its GPL may be a licensing choice (because mosh-go is MIT), so relicensing
is worth exploring, but only after an ownership/provenance audit and MIT-notice
review.

### 5.3 Other implementations are GPL

`Arunjay4213/mosh-rs` (GPL-3.0, 2026-10-13) and `wilsonglasser/mosh-rs`
(GPL-3.0, 2026-08-23) are GPL. This is consistent with port-style
implementations: if they translate or adapt mosh code, GPL is the correct
license. It also means the permissive option is narrow: mosh-go is the only
declared-permissive implementation found, and its provenance is the critical
question.

### 5.4 Blink is the GPL-app precedent

Blink Shell is GPLv3 with iOS additional terms and is on the App Store. It
statically links mosh and treats the whole app as GPL. It demonstrates that a
GPLv3 app plus a copyright-holder iOS non-assertion can be distributed on the
App Store, and it demonstrates the static-link consequence. It does not
demonstrate that a proprietary app can link GPL mosh.

---

## 6. Decision matrix

| Option | Copyright/license outcome | iOS App Store viability | Notes |
|---|---|---|---|
| Keep bundling MoshCatty as a separate desktop executable | Desktop app may be an aggregate; MoshCatty remains GPL; GPL source/license obligations apply | Not the iOS path; desktop only | Current repo needs provenance/notice corrections and a source offer. |
| Link MoshCatty into the iOS app | Whole app GPLv3 | Requires the whole app to be GPL plus an iOS waiver (Blink model); not compatible with a proprietary app | MoshCatty has no waiver; its authors could add one, but the app's code would still need to be GPL. |
| Link upstream mosh into the iOS app | Whole app GPLv3 | Upstream `COPYING.iOS` helps with the App Store conflict for mosh code, but the app must still be GPL; not a proprietary path | mosh is GPL; the waiver is a non-assertion, not a relicense. |
| Port/translate mosh or MoshCatty to Go | Derivative work; GPLv3 | No, unless the whole app is GPL | Translation is expressly a derivative work. |
| Use mosh-go (MIT) in-process | MIT, if provenance is clean; preserve notice | Yes, subject to audit, OCB/trademark/export checks | Fastest permissive path; provenance audit is the gating item. |
| Clean-room Go implementation | MIT/Apache-2.0 | Yes | Highest confidence if documented; highest engineering cost; no formal mosh wire spec. |
| Re-license MoshCatty to MIT/Apache-2.0 | Permissive, if the authors own the code and MIT notices are preserved | Yes, subject to audit | Potentially cheaper than a new implementation; requires ownership/provenance review. |
| Re-license to MPLv2 (VLC model) | File-level copyleft; App Store compatible | Yes, if all copyright holders agree | VLC precedent; not needed if MIT/Apache is available. |
| GPL the entire app + iOS waiver | GPLv3 | Possible (Blink model) | Changes the product's licensing model; requires copyright-holder waivers and source compliance. |

---

## 7. Risks

**High**

- **Using the bundled MoshCatty in-process on iOS** makes the app GPLv3 and
  triggers the App Store/GPL conflict; MoshCatty has no iOS waiver.
- **Assuming upstream mosh's `COPYING.iOS` covers MoshCatty.** It does not, on
  its face. The repository's own provenance docs incorrectly describe MoshCatty
  as an upstream mosh build, which increases this risk.
- **Porting or translating mosh/MoshCatty code** is a derivative work; the app
  becomes GPLv3.
- **Relying on mosh-go's MIT declaration without a provenance audit.** Its
  comments cite mosh's `network.cc` and field numbers; if it translated GPL
  code, the MIT license is not effective for the tainted portions.

**Medium**

- **App Store removal risk is complaint-driven.** VLC and GNU Go were removed
  after GPL complaints. Apple does not appear to detect GPL in App Review, but a
  contributor or competitor can complain; 5.2.1 also requires rights.
- **mosh `COPYING.iOS` scope/enforceability.** It may not bind every
  contributor; it is a non-assertion, not a relicense; it does not cure copyleft
  for the host app.
- **OCB patent residue.** The inventor's public-domain statement and the
  expiration record are strong, but the second application, continuations, and
  foreign counterparts need confirmation.
- **No formal mosh wire specification.** The paper/code differences and the
  large terminal/prediction surface make clean-room compatibility expensive and
  similarity risk non-trivial.
- **MoshCatty's missing MIT notice.** If it copied mosh-go code or tests, the
  current desktop distribution and `resources/THIRD-PARTY-NOTICES.md` may be
  missing a required notice.
- **Host app's own license.** This worktree has no `LICENSE`; the original
  Netcatty is recorded as GPL-3.0-or-later in
  `docs/research/go-mygo-rewrite.md`. If the native app is a derivative, the app
  itself is GPL and the App Store problem predates mosh.

**Low / separate**

- **Trademark.** Use "mosh-compatible" descriptively; do not imply endorsement
  or use the mosh logo. A trademark search/policy review is needed.
- **Export compliance.** AES-128-OCB triggers App Store encryption questions;
  likely a mass-market exemption, but review.
- **ET.** Apache-2.0, permissive, in-process native library is the separate
  ticket; preserve LICENSE/NOTICE and patent terms.

---

## 8. Unknowns / needs counsel

These are the questions that engineering cannot settle. Do not treat any of the
"probable" answers above as a legal conclusion.

1. **mosh `COPYING.iOS` scope and effect.** Is it an additional permission under
   GPLv3 §7, a non-enforcement covenant, or both? Does it bind every mosh
   copyright holder/contributor, or only the project lead who committed it? Does
   it cover a proprietary app that merely aggregates mosh, or only a GPL-covered
   combined work? Can a downstream distributor rely on it after modifying mosh?
2. **App Store terms as "further restrictions."** No court has ruled on the
   current Apple EULA/Usage Rules versus GPLv3. The FSF's position is clear;
   Apple's practice is complaint-driven. How much risk does the current terms
   text create for a GPLv3 app, and does the answer differ for a separate GPL
   binary versus an in-process link?
3. **MoshCatty ownership/relicensing.** Do the MoshCatty authors own all
   copyright in the Rust code? Are there contributor assignments? Can they
   relicense to MIT/Apache-2.0? Does the code contain any mosh C++ translation
   or other GPL-derived material? If mosh-go-derived material was copied, is the
   MIT notice sufficient and has it been preserved?
4. **mosh-go provenance.** Is its MIT declaration valid for all files? Its
   comments cite mosh's `network.cc` and field numbers; did it translate GPL
   code, or did it implement from the paper/observed behavior? A file-by-file
   provenance audit (commit history, author statements, code comparison) is
   needed before treating it as a clean base.
5. **OCB patent completeness.** Confirm the status of application 09/948,084,
   any continuations/divisionals/reissues, and foreign counterparts; confirm the
   2021 public-domain statement's legal effect and that no assignee/successor
   retains rights; confirm the GPL-only mosh grant is irrelevant if the patents
   are expired/public domain.
6. **mosh protocol patents.** Is any part of SSP, the datagram format, or the
   prediction/overlay behavior covered by patents? A patent search is needed;
   the mosh paper does not include an IPR statement.
7. **Clean-room specification derivation.** If a "dirty" team reads GPL source
   and writes a functional spec, at what point does the spec become a derivative
   or a prohibited translation? How detailed can field numbers/state tables be
   before they carry protected expression? Counsel should review the spec
   template and the first spec.
8. **Trademark.** Is "mosh" a registered trademark in relevant jurisdictions?
   What is the permissible nominative use for "mosh-compatible"? Is there a
   project trademark policy? (No formal policy was found in the mosh repository;
   this is an open question.)
9. **Export controls.** What App Store Connect encryption declaration is needed
   for AES-128-OCB? Is the mass-market exemption available in all target
   countries?
10. **The host app's own license/provenance.** Is the native rewrite a derivative
    of the GPL-3.0-or-later Electron Netcatty? If yes, the app itself is GPL and
    the App Store conflict is not solved by a permissive mosh module. This needs
    a separate copyright/provenance review before iOS submission.
11. **App Review.** Will an in-process mosh implementation raise 2.5.2 or 5.2.1
    questions? The design is self-contained and does not download code, but App
    Review is fact-specific. A pre-submission review/expedited appeal plan may be
    warranted.
12. **ET.** Separate ticket; confirm Apache-2.0 NOTICE/patent-termination
    compliance and whether the native library is linked in a way that preserves
    the Apache notices.

---

## 9. Recommended engineering constraints (not legal advice)

1. **Do not ship MoshCatty or upstream mosh in-process in the iOS app.** That
   makes the app GPLv3 and collides with the App Store terms; MoshCatty has no
   iOS waiver.
2. **Choose a permissive base deliberately:**
   - **Option A: audit and use mosh-go (MIT).** File-by-file provenance review;
     preserve the MIT notice; add a compatibility suite; keep dependencies
     permissive and documented. This is the fastest path if the audit is clean.
   - **Option B: clean-room Go implementation.** Follow §3.3; document the
     dirty/clean split; use RFC 7253 and the mosh paper, plus black-box interop;
     budget for the terminal/prediction surface.
   - **Option C: relicense MoshCatty to MIT/Apache-2.0.** Only if ownership and
     provenance are clean and the mosh-go MIT notice is preserved. This may be
     cheaper than B, but it is not a shortcut around the audit.
3. **Implement OCB3 from RFC 7253.** Use AES-128, 12-byte nonce, 16-byte tag,
   empty AAD for mosh; test against RFC 7253 Appendix A; do not implement OCB2.
   Have counsel confirm the patent/public-domain analysis.
4. **Keep the iOS implementation in-process and self-contained.** No downloaded
   binaries or plugins; no runtime code execution; this satisfies 2.5.2's
   self-contained rule.
5. **License the new implementation MIT or Apache-2.0.** Include the license,
   notices, and any patent terms; avoid the "mosh" name/logo in app branding.
6. **Fix the current repository notices and provenance docs.** Correct the
   "upstream mosh" claims in `resources/mosh/README.md` and
   `resources/mosh/BUILD-PROVENANCE.json`; add the mosh-go MIT notice if
   applicable; keep the desktop GPL bundle's source offer accurate.
7. **Keep the desktop GPL bundle separate and compliant.** If the desktop app
   continues to distribute MoshCatty, provide its GPL license, source, and
   notices; do not assume the iOS analysis changes the desktop obligations.
8. **Resolve the host app's license before iOS submission.** If it is a
   derivative of GPL Netcatty, the app itself needs a licensing decision and,
   if it is to be GPL, an iOS waiver/source-compliance strategy. A permissive
   mosh module does not fix that.
9. **Do not open-source or distribute the clean-room spec if it contains
   translated GPL expression.** Keep the spec under review; publish only what
   counsel approves.
10. **Get counsel review before release.** The open items in §8 are not
    engineering decisions.

---

## 10. Sources

All URLs retrieved 2026-11-03 unless a different date is stated.

### Licenses and license texts

- GNU GPL version 3, 29 June 2007:
  <https://www.gnu.org/licenses/gpl-3.0.en.html>.
- mosh `COPYING`, source headers, and `COPYING.iOS`:
  <https://github.com/mobile-shell/mosh/blob/master/COPYING>;
  <https://github.com/mobile-shell/mosh/blob/master/COPYING.iOS>;
  <https://github.com/mobile-shell/mosh/blob/master/src/network/network.cc>.
- mosh `ocb-license.html`:
  <https://github.com/mobile-shell/mosh/blob/master/ocb-license.html>.
- mosh OCB reference code:
  <https://github.com/mobile-shell/mosh/blob/master/src/crypto/ocb_internal.cc>.
- MoshCatty 0.1.8:
  <https://github.com/binaricat/MoshCatty/tree/moshcatty-0.1.8>;
  `Cargo.toml`, `LICENSE`, `README.md`, source comments.
- mosh-go:
  <https://github.com/unixshells/mosh-go>; `LICENSE`, `README.md`,
  `fragment.go`, `pb.go`.
- Arunjay4213/mosh-rs:
  <https://github.com/Arunjay4213/mosh-rs> (GitHub API license: GPL-3.0,
  2026-10-13).
- wilsonglasser/mosh-rs:
  <https://github.com/wilsonglasser/mosh-rs> (GitHub API license: GPL-3.0,
  2026-08-23).
- Blink Shell:
  <https://github.com/blinksh/blink>;
  <https://blink.sh/blink-gpl> (copyright 2022).
- EternalTerminal upstream:
  <https://github.com/MisterTea/EternalTerminal> (GitHub API license:
  Apache-2.0); Apache License 2.0: <https://www.apache.org/licenses/LICENSE-2.0>.

### mosh protocol and cryptography

- mosh paper, Keith Winstein and Hari Balakrishnan, "Mosh: An Interactive
  Remote Shell for Mobile Clients", USENIX ATC '12, 2012-06-13/15:
  <https://mosh.org/mosh-paper-draft.pdf>;
  <https://www.usenix.org/conference/atc12/technical-sessions/presentation/winstein>.
- mosh protocol constant and nonce:
  <https://github.com/mobile-shell/mosh/blob/master/src/network/network.h>;
  <https://github.com/mobile-shell/mosh/blob/master/src/crypto/crypto.h>;
  <https://github.com/mobile-shell/mosh/blob/master/src/frontend/mosh-server.cc>;
  <https://github.com/mobile-shell/mosh/blob/master/src/protobufs/transportinstruction.proto>.
- RFC 7253, "The OCB Authenticated-Encryption Algorithm", May 2014:
  <https://www.rfc-editor.org/rfc/rfc7253.txt>.
- OCB licensing page:
  <https://web.cs.ucdavis.edu/~rogaway/ocb/license.htm>.
- IETF IPR disclosure 559:
  <https://datatracker.ietf.org/ipr/559/>.
- CFRG OCB patent abandonment message, 2021-02-27:
  <https://mailarchive.ietf.org/arch/msg/cfrg/qLTveWOdTJcLn4HP3ev-vrj05Vg/>.
- U.S. Patent 7,046,802:
  <https://patents.google.com/patent/US7046802B2/en>.
- Protobuf encoding:
  <https://protobuf.dev/programming-guides/encoding/>.
- RFC 1950/1951 (zlib):
  <https://www.rfc-editor.org/rfc/rfc1950.txt>;
  <https://www.rfc-editor.org/rfc/rfc1951.txt>.
- RFC 768 (UDP), RFC 6298 (RTO), RFC 3168 (ECN), RFC 3629 (UTF-8):
  <https://www.rfc-editor.org/rfc/rfc768.txt>;
  <https://www.rfc-editor.org/rfc/rfc6298.txt>;
  <https://www.rfc-editor.org/rfc/rfc3168.txt>;
  <https://www.rfc-editor.org/rfc/rfc3629.txt>.
- ECMA-48:
  <https://ecma-international.org/publications-and-standards/standards/ecma-48/>;
  xterm control sequences:
  <https://invisible-island.net/xterm/ctlseqs/ctlseqs.html>.

### Apple terms

- App Store Review Guidelines, last updated 2026-10-16:
  <https://developer.apple.com/app-store/review/guidelines/>.
- Apple Licensed Application End User License Agreement:
  <https://www.apple.com/legal/internet-services/itunes/dev/stdeula/>.
- Apple Media Services Terms and Conditions (US), last updated 2026-09-14:
  <https://www.apple.com/legal/internet-services/itunes/us/terms.html>.

### FSF, VideoLAN, and Blink statements

- FSF, "Why free software and Apple's iPhone don't mix", 2008-07-30:
  <https://www.fsf.org/blogs/community/why-free-software-and-apples-iphone-dont-mix>.
- FSF news, "FSF demonstrates iPhone's incompatibility with free software and
  GPLv3", 2008-07-31:
  <https://www.fsf.org/news/iphone-is-incompatible-with-free-software/>.
- FSF, "GPL Enforcement in Apple's App Store", 2010-05-25:
  <https://www.fsf.org/news/2010-05-app-store-compliance/>.
- FSF, "More about the App Store GPL Enforcement", 2010-05-26:
  <https://www.fsf.org/blogs/licensing/more-about-the-app-store-gpl-enforcement>.
- FSF, "Apple App Store anniversary marks ten years of proprietary
  appsploitation", 2018-07-31:
  <https://www.fsf.org/blogs/community/apple-app-store-anniversary-marks-ten-years-of-proprietary-appsploitation>.
- Archived Planet VideoLAN posts, 2011-01-08/09:
  <https://web.archive.org/web/20110111004349/http://planet.videolan.org/>.
- VideoLAN press release, "VLC for iOS 2.0", 2013-07-18:
  <https://www.videolan.org/press/ios2out.html>.
- mosh-devel, "Blink and Mosh changes released", 2016-06-15:
  <https://mailman.mit.edu/pipermail/mosh-devel/2016-June/001343.html>.

### Copyright and clean-room law

- 17 U.S.C. §101 (definitions, including "derivative work") and §102(b)
  (ideas/procedures/systems not copyrightable):
  <https://www.copyright.gov/title17/92chap1.html>.
- Computer Assocs. Int'l, Inc. v. Altai, Inc., 982 F.2d 693 (2d Cir. 1992):
  <https://law.resource.org/pub/us/case/reporter/F2/982/982.F2d.693.91-7935.91-7893.762.html>.
- Sega Enterprises Ltd. v. Accolade, Inc., 977 F.2d 1510 (9th Cir. 1992):
  <https://law.resource.org/pub/us/case/reporter/F2/977/977.F2d.1510.92-15655.html>;
  U.S. Copyright Office Fair Use Index summary:
  <https://www.copyright.gov/fair-use/summaries/segaenters-accolade-9thcir1992.pdf>.

### Repository evidence

- `resources/mosh/README.md`, `resources/mosh/BUILD-PROVENANCE.json`,
  `resources/mosh/LICENSE-MoshCatty-GPL-3.0.txt`.
- `resources/THIRD-PARTY-NOTICES.md`.
- `resources/et/README.md`, `resources/et/BUILD-PROVENANCE.json`,
  `resources/et/LICENSE-EternalTerminal-Apache-2.0.txt`.
- `adapters/bundle/manifests.go`, `adapters/bundle/bundle.go`.
- `docs/research/go-mygo-rewrite.md` (records the original Netcatty
  GPL-3.0-or-later licensing caution).

### Sources not accessed / limitations

- The current Apple Developer Program License Agreement (DPLA) PDF was not
  retrieved; the Standard EULA, Media Services Terms, and Review Guidelines were
  used instead. The DPLA could add developer-side restrictions and should be
  reviewed by counsel.
- Justia, CourtListener, OpenJurist, and Leagle were blocked by Cloudflare/JS for
  the case opinions; the *Altai* and *Sega* texts above are from
  law.resource.org, and the Sega summary is from the U.S. Copyright Office.
- No formal VideoLAN press release for the 2011 removal was found; the archived
  Planet VideoLAN posts are the primary developer statements, and contemporary
  reporting was used only for corroboration.
- No published legal analysis specific to mosh clean-room implementation was
  found. This is a research gap, not a clean bill of health.
