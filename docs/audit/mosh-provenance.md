# MoshCatty 0.1.8 and mosh-go provenance: notice corrections and audit draft

**Status: agent-drafted internal report. This is not an independent
provenance/derivative-work audit and not legal advice.** No external auditor,
counsel, upstream maintainer, or contributor has reviewed this document. It
records the evidence a coding agent inspected while correcting the repository
notices for ticket 15, what was changed, and the exact review that remains. It
must not be cited as provenance clearance, a legal opinion, or proof of license
compliance.

**Historical scope note (2026-11-07):** the bundled MoshCatty client and its
`resources/mosh/` assets have been removed, and the shared
`adapters/bundle` manifest/resolver cited below was removed with the bundled
EternalTerminal client. Every Mosh Session now uses the in-process Go client.
This report remains as the record of the removed component's audit; the
shipping attribution is the stock-mosh and mosh-go material in
`internal/mosh/NOTICE` and `resources/THIRD-PARTY-NOTICES.md`.

- Ticket: 15, "Commission mosh provenance audit and fix notices" (HITL)
- Date: 2026-11-04
- Branch: `ticket-15-provenance`, based on `2f013f84c`
- Related decisions: ADR-0014 (host app stays GPL-3.0-or-later) and ADR-0015
  (desktop moves toward an application-owned pure-Go mosh client; the bundled
  MoshCatty adapter is still present and shipped on this branch)
- Related research:
  - `docs/research/mosh-go-and-moshcatty-provenance.md` on
    `research/mosh-provenance` at
    `c06706f1dc8f1e3dc2bf4a68a736aea4749d8409`
  - `docs/research/mosh-licensing-and-app-store.md` on
    `research/mosh-licensing` at
    `afbda0e1576147ad4e4c8c9c253544b69f3897eb`
  - Both are throwaway research branches and are not merged into this branch.

## 1. Scope

In scope:

- MoshCatty 0.1.8 and the `mosh-client` binaries bundled under
  `resources/mosh/`.
- mosh-go as the source of MIT-licensed expression adapted into MoshCatty.
- The 49 third-party Rust crates pinned by MoshCatty's `Cargo.lock`.
- The notices and provenance files that describe these components.

Out of scope:

- EternalTerminal (`et`), which is Apache-2.0 and tracked separately.
- Mosh protocol correctness except where it bears on provenance.
- The iOS/App Store decision and any future relicensing decision.
- Any legal conclusion about derivative-work status, ownership, or license
  enforceability.

## 2. Methodology and evidence examined

Evidence inspected directly for this report:

- Ticket 15, ticket 13's research audit, ADR-0014, ADR-0015, and the two
  research documents named above.
- A full clone of `binaricat/MoshCatty` at tag `moshcatty-0.1.8`
  (`554b9d305e7ac4b11de740d764bbc3e05f816d7b`, 2026-07-17). The clone has 53
  commits across all refs and no `CONTRIBUTING`, CLA, DCO, or copyright
  assignment file.
- `Cargo.toml`, `Cargo.lock` (SHA-256
  `423afa7d1c07136ba2f41f6e4d396e8563a5f7c659964d138dbce8352608142b`), the
  release workflow, and the source comments and test files listed in section
  3.4.
- A clone of `unixshells/mosh-go` at
  `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f` (2026-04-05), including its
  `LICENSE` (exact byte match with the notice now shipped as
  `resources/mosh/LICENSE-MoshGo-MIT.txt`).
- The committed Windows x64 binary `resources/mosh/win32-x64/mosh-client.exe`:
  SHA-256 `e44616a22038f1742f765c7de553796ba4d1698f2c1f7a7cfb0dafd33ecdc78a`,
  545,280 bytes, matching `resources/mosh/BUILD-PROVENANCE.json` and the digest
  compiled into `adapters/bundle/manifests.go`.
- All 49 locked crate archives downloaded from `static.crates.io`. Each
  archive's SHA-256 matches the `checksum` recorded in MoshCatty 0.1.8's
  `Cargo.lock` (49 of 49 verified). Their `Cargo.toml`, `LICENSE*`, and
  `NOTICE*` files were read from the registry sources and archives; no crate
  code was executed.
- The checked-in notice, provenance, and source-offer files listed in section
  6.

Evidence taken from ticket 13 research and not independently re-run here:

- The normalized comment comparison against upstream mosh `decd9b7` (0 matches
  for both mosh-go and MoshCatty) and the structural comparison of the
  implementations.
- The binary string inspection (Rust/Cargo paths, no C++ mosh symbols) and the
  byte-for-byte comparison of the committed Windows binary with the published
  `moshcatty-0.1.8` release asset.
- The mosh-dart sibling-lineage observation and the OCB license/patent
  discussion.
- The external legal-source review in the licensing research.

Limits and cautions:

- Only the Windows x64 binary is committed locally. The Linux x64, Linux arm64,
  and macOS universal binaries were not inspected in this work; their digests
  are pinned in the manifest and the release archive digests are recorded.
- No similarity, clone-detection, or AI-detection tool was run for this report.
  The research-branch result is evidence, not a completed independent audit.
- No code from the cloned external repositories was executed.
- Nothing here is a legal opinion.

## 3. Findings: MoshCatty 0.1.8

### 3.1 Origin and version

| Item | Value |
| --- | --- |
| Repository | <https://github.com/binaricat/MoshCatty> |
| Release | `moshcatty-0.1.8`, published 2026-07-17T06:27:35Z |
| Tag commit | `554b9d305e7ac4b11de740d764bbc3e05f816d7b` (merge of PR #7) |
| Initial commit | `4f76b33641d58a68f62716bb7b51d1f2faa3505a` (2026-07-10) |
| Release workflow run | <https://github.com/binaricat/MoshCatty/actions/runs/29559896639> |
| Language / package | Rust, Cargo package `moshcatty`, binary `src/bin/mosh_client.rs` |
| Declared license | GPL-3.0-or-later (`Cargo.toml:7`, `LICENSE`) |
| Declared authors | `binaricat`, `Netcatty contributors` (`Cargo.toml:9`) |
| Windows arm64 | Not built; the app returns a typed unsupported-platform error |

### 3.2 The bundled binary is MoshCatty, not upstream mosh

MoshCatty's release workflow checks out MoshCatty and runs
`cargo build --release` for Linux x64, Linux arm64, macOS universal, and
Windows x64. It never checks out or builds `mobile-shell/mosh`, and there is no
C++ toolchain step.

The previous repository text said the opposite: that MoshCatty builds mosh from
upstream `mobile-shell/mosh` and that upstream mosh could be offered as the
Corresponding Source. That was false and has been corrected. Upstream mosh is a
protocol and behavioral reference only and is not the source of the bundled
binary.

The committed Windows x64 binary matches the digest recorded for the published
release asset, as described in section 2. The ticket 13 research additionally
found Rust standard-library and Cargo registry paths and no C++ mosh symbols in
the binary.

### 3.3 Corresponding source and modifications

- The Corresponding Source for the bundled binaries is MoshCatty's Rust source
  at tag `moshcatty-0.1.8`, commit
  `554b9d305e7ac4b11de740d764bbc3e05f816d7b`.
- Natcatty does not modify MoshCatty's source. It packages the published
  release binaries, verifies archive and binary digests when fetching them, and
  re-verifies the binary before every launch.
- Offering `mobile-shell/mosh` as the Corresponding Source for this binary
  would not satisfy GPL section 6 for MoshCatty's code and was removed from the
  provenance text and the generated source offer.

### 3.4 MIT-derived expression from mosh-go and the missing notice

MoshCatty contains MIT-licensed expression adapted from `unixshells/mosh-go`.
This is more than opportunistic interop vectors:

| Evidence | Location |
| --- | --- |
| Tests explicitly adapted from mosh-go (`ocb_test`, `fragment_test`, `pb_test`, `transport_test`) | `tests/protocol_suite.rs:1-6` |
| Fragment layout described as upstream mosh / mosh-go layout | `src/fragment.rs:1` |
| Transport aligned with mosh-go's simplified single-pending queue; critical fixes compared with mosh-go/stock | `src/transport.rs:1-10` |
| Protobuf width/height encoding explicitly matches mosh-go | `src/pb.rs:1-3`, `src/pb.rs:41` |
| Framebuffer documented as shaped after mosh-go `framebuffer.go` | `src/framebuffer.rs:1-5` |
| Prediction documented as mosh-go pending-list core plus stock overlay semantics | `src/prediction.rs:1-4` |
| Framebuffer/confirm/overlay port commit | `79d6c55ab7db68d805ef2dfdd615124b199d6668` (2026-07-12, `feat(prediction): mosh-go Framebuffer + Confirm + Overlay paint path`) |
| README credits mosh-go interop vectors and a mosh-go Diff path | `README.md:68,78,247,284` |

MIT permits this material to be combined into a GPL-3.0-or-later work, but it
requires the copyright notice and permission notice to be included in copies or
substantial portions. MoshCatty 0.1.8 does not ship an MIT notice, and Netcatty
did not ship one either. The omission is now corrected:

- `resources/mosh/LICENSE-MoshGo-MIT.txt` reproduces mosh-go's MIT license and
  `Copyright (c) 2026 Unix Shells` exactly.
- `resources/THIRD-PARTY-NOTICES.md` identifies the adapted material, the
  mosh-go commit, and the notice.
- `resources/mosh/BUILD-PROVENANCE.json` records the mosh-go third-party
  notice as machine-readable provenance.

The notice is required because adapted MIT expression is present in the
bundled binary. mosh-go is not itself packaged as a separate binary or Go
dependency in this repository.

### 3.5 Ownership, contributors, and AI assistance

A full clone of the tag has 53 commits across all refs. The author census is:

| Author identity | Commits |
| --- | --- |
| `bincxz <16399091+binaricat@users.noreply.github.com>` | 40 |
| `陈大猫 <16399091+binaricat@users.noreply.github.com>` | 7 |
| `LAPTOP-O016UC3M\Qi Chen <chenqi@snowtech.com.cn>` (machine-named identity) | 4 |
| `binaricat <dev@binaricat.com>` | 2 |

There is no `CONTRIBUTING`, CLA, DCO, or copyright assignment file. The
repository also has three merge commits from branches named
`codex/fix-2121-high-latency`, `codex/issue-2121-release-hardening`, and
`codex/fix-backspace-local-echo`. Codex is an AI coding agent. These facts do
not show copying, but they mean the chain of title and the authorship of
AI-assisted output are not documented well enough for relicensing. They do not
block the current GPL distribution.

### 3.6 Reproducible build

No reproducible-build evidence exists for MoshCatty 0.1.8:

- The release workflow installs or selects the floating `stable` Rust
  toolchain and has no `rust-toolchain.toml` or pinned compiler version.
- It publishes archives and `SHA256SUMS` but no rebuild comparison, build
  attestation, build log with a pinned environment, or provenance statement.
- The Windows binary corresponds to the published asset, but correspondence to
  a rebuild from source was not shown.

The repository now states this explicitly in `resources/mosh/README.md`,
`resources/mosh/BUILD-PROVENANCE.json` (`reproducibility.status` is
`not-established`), and the third-party notices. The binaries must not be
described as reproducible until independent evidence exists.

### 3.7 GPL obligations under the current distribution decision

ADR-0014 keeps the host app GPL-3.0-or-later, so using a GPL client is
license-compatible and relicensing consent is not a precondition for desktop
distribution. The distribution still needs:

- the GPL-3.0-or-later text (present at the repository root and under
  `resources/mosh/`),
- a corresponding-source offer naming MoshCatty's tag and commit (present in
  `NOTICE`, the generated `SOURCE-OFFER.md`, and the notices), and
- the third-party notices for the MIT-derived material and Rust crates (now
  added).

Relicensing MoshCatty under MIT/Apache-2.0 is a separate question, necessary
only if a permissive mobile/App Store path reopens. It is not required or
approved by this report.

## 4. Findings: mosh-go

| Item | Value |
| --- | --- |
| Repository | <https://github.com/unixshells/mosh-go> |
| Audited HEAD | `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f` (2026-04-05) |
| Code tag | `v0.5.2` = `0284bd652aab3dfff5b0b74945c23901d7537b61` (2026-03-31) |
| Initial commit | `bb08f71e30ecaf9b6f1490becde1773ba64df24a` (2026-03-12) |
| Declared license | MIT, `Copyright (c) 2026 Unix Shells` |
| Releases / CI | No GitHub Releases and no CI directory |
| Activity | 2026-03-12 to 2026-04-05; dormant since |

Findings:

- **GPL-derived expression risk.** The research branch found no literal comment
  copying against upstream mosh `decd9b7`, and the implementation is
  structurally much simpler than upstream's transport, framebuffer, prediction,
  and terminal-display code. The same-day MIT sibling `mosh-dart` supports a
  common in-house lineage. That is evidence against literal translation, not
  proof of independent creation.
- **MIT validity.** The MIT declaration is facially valid: the repository
  contains the standard MIT text and no GPL headers or upstream source files.
  It has no NOTICE file, no per-file notices, no provenance statement, and no
  generation record. Non-literal translation, AI-generated reproduction, and
  the OCB reference remain open questions that only an independent similarity
  and provenance review can address.
- **Notice obligation.** The MIT notice must travel with adapted material in
  MoshCatty regardless of the GPL combination. That obligation is now met in
  this repository.
- **Not a shipped binary.** mosh-go is not packaged as a binary and is not a Go
  module dependency on this branch. It is a source reference and the source of
  adapted expression; the notice is stated precisely as such.

The mosh-go MIT text shipped in `resources/mosh/LICENSE-MoshGo-MIT.txt` is an
exact match with the upstream `LICENSE`.

## 5. Findings: the 49 Rust crates

The locked set is permissive and GPL-compatible. No crate is copyleft. License
families include:

- MIT (`cfg_aliases`, `generic-array`, `simd-adler32`)
- MIT OR Apache-2.0 (the majority)
- 0BSD OR MIT OR Apache-2.0 (`adler2`)
- MIT OR Zlib OR Apache-2.0 (`miniz_oxide`)
- (MIT OR Apache-2.0) AND Unicode-3.0 (`unicode-ident`)
- BSD-2-Clause OR Apache-2.0 OR MIT (`zerocopy`, `zerocopy-derive`)
- Apache-2.0 OR MIT (`zeroize`)
- Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT (`wasi`)
- `MIT/Apache-2.0` as published for `version_check` (treated here as
  `MIT OR Apache-2.0`)

For each crate, `resources/mosh/RUST-DEPENDENCIES.json` records the name,
version, license expression, repository/homepage, and copyright/authors with
the source of that line. The corresponding rows and full license texts are in
`resources/THIRD-PARTY-NOTICES.md`.

Metadata provenance:

- Versions come from MoshCatty 0.1.8's `Cargo.lock`; the JSON records the lock
  file's SHA-256.
- All 49 crate archives downloaded from `static.crates.io` match the
  `Cargo.lock` checksums.
- License expressions, repositories, homepages, and `authors` fields come from
  each published `Cargo.toml`.
- Copyright lines come from the crate's shipped `LICENSE*` files where present.
  For `once_cell`, `proc-macro2`, `quote`, `syn`, `thiserror`,
  `thiserror-impl`, and `wasi`, the shipped license file has no copyright line;
  the table uses the `authors` field and says so. Whether that is a sufficient
  copyright notice for those crates is a human review item, not something this
  report resolves.
- `cfg_aliases` ships a `NOTICES.md` for MIT-licensed code derived from
  `tectonic_cfg_support::target_cfg!`; the notices summarize that attribution
  and reproduce the generic MIT text, not `NOTICES.md` verbatim.
  No other locked crate shipped an Apache-2.0 `NOTICE` file.
- `unicode-ident` carries Unicode data under the Unicode License V3; the exact
  shipped text (copyright 1991-2023) is included.

There are no known copyleft or notice-blocking crates in this set, but the
crate metadata was not reviewed by counsel or an independent auditor.

## 6. Corrections made in this ticket

| File | Change |
| --- | --- |
| `resources/THIRD-PARTY-NOTICES.md` | Replaced the false "built from upstream mosh" story with MoshCatty's real origin, tag commit, build command, source location, modification status, and protocol-reference role; added the mosh-go MIT notice and the 49-crate table with copyright/authors; added 0BSD, Zlib, and LLVM-exception texts; corrected the Unicode V3 text to the shipped 1991-2023 version; marked reproducible-build evidence as not established and the independent audit as pending. |
| `resources/mosh/README.md` | Corrected the source, corresponding-source, modification, dependency, mosh-go, and reproducibility sections; added the exact source commit and release workflow. |
| `resources/mosh/BUILD-PROVENANCE.json` | Replaced the `upstream` source claim with a `source` object (repository, tag, commit, build command, workflow) and a `protocolReference` object marked "not the source"; added the mosh-go third-party notice; added `reproducibility.status = not-established` with no evidence. |
| `resources/mosh/LICENSE-MoshGo-MIT.txt` | New exact copy of mosh-go's MIT license, `Copyright (c) 2026 Unix Shells`. |
| `resources/mosh/RUST-DEPENDENCIES.json` | New machine-readable record of the 49 locked crates, their metadata, copyright source, `Cargo.lock` SHA-256, and evidence method. |
| `internal/legal/legal.go` | Source offer now names MoshCatty as a pure-Rust source at its commit, marks upstream mosh as a protocol reference only, and names the mosh-go MIT notice; repository check requires the mosh-go notice and source commit. |
| `internal/legal/legal_test.go` | Tests now enforce the mosh-go notice, the source commit, the protocol-reference role, the unreproduced build status, and all 49 crate rows/license texts. |
| `adapters/bundle/bundle_test.go` | Bundle test now requires the mosh-go notice, the source commit, the Rust dependency manifest, the protocol reference, and the non-reproducible status. |
| `docs/audit/mosh-provenance.md` | This draft report. |

No file claims that an independent audit or legal review has been completed.

## 7. What remains unverified

- No independent provenance or derivative-work audit of MoshCatty or mosh-go
  has been performed.
- No legal review has confirmed the derivative-work analysis, the
  MIT/GPL-3.0-or-later combination, the copyright-holder lines, or the
  sufficiency of these notices.
- Whether any MoshCatty module is a non-literal translation of GPL mosh
  expression is unresolved. The prediction, framebuffer, ANSI-apply, and
  transport modules are the highest-risk candidates.
- Whether mosh-go is an independent creation or an AI-generated reproduction
  of GPL expression is unresolved. There is no generation record.
- Ownership of the machine-named commits and the Codex-assisted commits is
  unproven. There is no CLA, DCO, or assignment.
- Reproducible-build evidence for MoshCatty is missing, and no target besides
  Windows x64 was inspected locally.
- The OCB implementation's provenance and patent/license status were not
  reviewed here; see the licensing research for the open questions.
- The seven crates whose shipped license files have no copyright line rely on
  their `Cargo.toml` authors field; whether that satisfies every notice
  obligation is unreviewed.
- The published `v0.1.0` release still carries the old notices until a human
  with release rights applies `docs/releases/v0.1.0-correction.md` or publishes
  a corrective release.

## 8. Human and independent review still required

1. **Commission an independent provenance and derivative-work audit** of
   MoshCatty 0.1.8 and mosh-go. It must include chain-of-title review, a
   reproducible similarity scan against `mobile-shell/mosh` `decd9b7`, the OCB
   reference, and other permissive sources, review of the prediction,
   framebuffer, ANSI-apply, and transport modules, and a written conclusion on
   whether any expression is GPL-derived. This agent-drafted report does not
   satisfy that requirement.
2. **Obtain legal review** of the derivative-work analysis, the MIT notice
   obligation, the GPL-3.0-or-later source offer, the contributor and
   AI-assistance facts, and the App Store question if a mobile path reopens.
3. **Contributor consent or assignment is required before any relicensing.**
   It becomes necessary only if a permissive mobile/App Store path reopens.
   Resolve the two owner-named identities and the machine-named identity, and
   the Codex-assisted commits, and document the AI-generation provenance.
4. **Produce reproducible-build evidence** for MoshCatty 0.1.8: pin the Rust
   toolchain, record the build environment and flags, rebuild from the tag, and
   compare with the published assets, or document why that is not achievable
   and what independent attestations substitute for it. Until then, do not
   describe the binaries as reproducible.
5. **Verify the non-Windows binaries** against their published assets and, if
   possible, reproduce them.
6. **Have a human release owner correct the published `v0.1.0` notices** or
   publish a corrective release. This report does not modify released
   artifacts.
7. **Confirm the notice basis for the seven crates without a license-file
   copyright line** (`once_cell`, `proc-macro2`, `quote`, `syn`, `thiserror`,
   `thiserror-impl`, and `wasi`). Their rows rely on the `Cargo.toml` authors
   field; confirm that this is a sufficient copyright notice and that no
   separate holder attribution is required.

## 9. Evidence index

- MoshCatty tag commit:
  `554b9d305e7ac4b11de740d764bbc3e05f816d7b`
- MoshCatty initial commit:
  `4f76b33641d58a68f62716bb7b51d1f2faa3505a`
- MoshCatty `Cargo.lock` SHA-256:
  `423afa7d1c07136ba2f41f6e4d396e8563a5f7c659964d138dbce8352608142b`
- mosh-go audited commit:
  `8dca5c67ec8e09f71a4dc8eda9216f2f4ee7ec0f`
- mosh-go `v0.5.2`:
  `0284bd652aab3dfff5b0b74945c23901d7537b61`
- Bundled Windows binary SHA-256:
  `e44616a22038f1742f765c7de553796ba4d1698f2c1f7a7cfb0dafd33ecdc78a`
- MoshCatty release asset SHA-256:
  `ee437592c351361e47c600fafe04ce97a2551aa61fbef1fb3d6dda565835311e`
- Research provenance audit:
  `c06706f1dc8f1e3dc2bf4a68a736aea4749d8409`
- Research licensing/app-store assessment:
  `afbda0e1576147ad4e4c8c9c253544b69f3897eb`
- Crate checksum verification: 49 of 49 archives match `Cargo.lock`; the
  result set was generated from the official `static.crates.io` archives on
  2026-11-04. The metadata is recorded in
  `resources/mosh/RUST-DEPENDENCIES.json`.
