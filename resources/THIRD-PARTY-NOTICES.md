# Third-party notices

Natcatty Native is a modified version of
[Netcatty](https://github.com/binaricat/Netcatty) and is distributed under
GPL-3.0-or-later. The application's own license and modification notice are in
`LICENSE` and `NOTICE`. This file lists every third-party component that the
current desktop distribution includes, the license each component uses, and the
copyright notices and provenance evidence gathered so far. This file was
corrected by an agent for ticket 15. It is not an independent provenance audit,
not a legal opinion, and it does not certify compliance.

The corresponding source for a release is this repository at the release tag
(for example <https://github.com/mslxl/natcatty/tree/v0.1.0>); build
instructions are in `docs/packaging.md`. The app never downloads code at
runtime: the listed Go modules are compiled in, and the listed client binaries
and native libraries are packaged with the app.

**This file is engineering work, not legal advice and not an audit report.
Independent provenance/derivative-work review and legal review remain pending;
the open items are listed at the end and in `docs/audit/mosh-provenance.md`.**

## Natcatty and upstream Netcatty

- Component: Natcatty Native (this fork) and the upstream
  [binaricat/Netcatty](https://github.com/binaricat/Netcatty) work it modifies
- License: GNU General Public License, version 3 or later (GPL-3.0-or-later)
- Full text: [`LICENSE`](../LICENSE)
- Modification notice and copyright statement: [`NOTICE`](../NOTICE)
- Upstream source: <https://github.com/binaricat/Netcatty>
- Upstream metadata identified `binaricat` as the package author and declared
  `GPL-3.0-or-later`; copyright in upstream contributions remains with the
  respective authors
- **Pending human/legal review:** the exact copyright-holder line for the fork
  and the derivative-work analysis are not legally certified here.

## MyGo and its bundled native libraries

- Component: MyGo, the Go desktop UI framework
- Version: the `github.com/egoist/mygo` version pinned in `go.mod`
- Source: <https://github.com/egoist/mygo>
- License: MIT
- Copyright: Copyright (c) 2026 MyGo contributors
- Full text: [MIT license](#mit-license)

MyGo is compiled into the application and also packages these native or
embedded components:

- **libghostty-vt**, the terminal emulator library used by MyGo's terminal
  plugin. Source: <https://github.com/ghostty-org/ghostty/tree/befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028>.
  License: MIT. Copyright (c) 2024 Mitchell Hashimoto, Ghostty contributors.
  Full text: [MIT license](#mit-license).
- **MyGo shader support** (`internal/shader`), derived from Ebiten's shader
  package. License: Apache-2.0. Copyright 2020 The Ebiten Authors. Full text:
  [Apache License 2.0](#apache-license-20).
- MyGo's `plugins/sqlite` native SQLite library is **not** part of this app's
  dependency graph and is not distributed with it; Natcatty uses the pure-Go
  `github.com/glebarez/sqlite` and `modernc.org/sqlite` stack instead.

## Go modules compiled into the desktop binary

The following modules are selected by `go.mod`/`go.sum` and linked into the
Natcatty executable on every desktop target. The version column is the version
resolved for the current module graph; a release must regenerate this table
whenever `go.mod` changes. The copyright notices below are those carried by
each module's own `LICENSE` file.

| Module | Version | License | Copyright notice |
| --- | --- | --- | --- |
| `github.com/BurntSushi/toml` | v1.5.0 | MIT | Copyright (c) 2013 TOML authors |
| `github.com/dustin/go-humanize` | v1.0.1 | MIT | Copyright (c) 2005-2008 Dustin Sallings |
| `github.com/ebitengine/purego` | v0.11.1 | Apache-2.0 | Copyright The Ebitengine Authors (SPDX-FileCopyrightText in the source files) |
| `github.com/egoist/mygo` | pinned in `go.mod` | MIT | Copyright (c) 2026 MyGo contributors |
| `github.com/glebarez/go-sqlite` | v1.21.2 | BSD-3-Clause | Copyright (c) 2017 The Sqlite Authors. All rights reserved. |
| `github.com/glebarez/sqlite` | v1.11.0 | MIT | Copyright (c) 2013-NOW Jinzhu |
| `github.com/go-text/typesetting` | v0.3.5 | BSD-3-Clause (dual-licensed Unlicense OR BSD-3-Clause; this distribution relies on BSD-3-Clause) | Copyright 2021 The go-text authors |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | Copyright (c) 2009,2014 Google Inc. All rights reserved. |
| `github.com/jinzhu/inflection` | v1.0.0 | MIT | Copyright (c) 2015 - Jinzhu |
| `github.com/jinzhu/now` | v1.1.5 | MIT | Copyright (c) 2013-NOW Jinzhu |
| `github.com/kayrus/putty` | v1.0.5 | Apache-2.0 | Copyright kayrus and the putty package contributors (the package LICENSE carries the Apache-2.0 text without a copyright line) |
| `github.com/kr/fs` | v0.1.0 | BSD-3-Clause | Copyright (c) 2012 The Go Authors. All rights reserved. |
| `github.com/mattn/go-isatty` | v0.0.24 | MIT (Expat) | Copyright (c) Yasuhiro MATSUMOTO <mattn.jp@gmail.com> |
| `github.com/pkg/sftp` | v1.13.11 | BSD-2-Clause | Copyright (c) 2013, Dave Cheney |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | Copyright (c) 2012 The Go Authors. All rights reserved. |
| `github.com/tetratelabs/wazero` | v1.12.0 | Apache-2.0 | Copyright 2020-2023 wazero authors |
| `golang.org/x/crypto` | v0.58.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/image` | v0.46.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/net` | v0.60.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/text` | v0.43.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `google.golang.org/protobuf` | v1.36.12 | BSD-3-Clause | Copyright (c) 2018 The Go Authors. All rights reserved. |
| `gorm.io/gorm` | v1.31.1 | MIT | Copyright (c) 2013-present Jinzhu |
| `modernc.org/libc` | v1.22.5 | BSD-3-Clause | Copyright (c) 2017 The Libc Authors. All rights reserved.; portions Copyright (c) 2009 The Go Authors. All rights reserved. |
| `modernc.org/mathutil` | v1.5.0 | BSD-3-Clause | Copyright (c) 2014 The mathutil Authors. All rights reserved.; bundled mersenne code Copyright (c) 2014 The mersenne Authors. All rights reserved. |
| `modernc.org/memory` | v1.5.0 | BSD-3-Clause | Copyright (c) 2017 The Memory Authors. All rights reserved.; portions Copyright (c) 2009 The Go Authors. All rights reserved.; mmap-go portion Copyright (c) 2011, Evan Shaw <edsrzf@gmail.com> |
| `modernc.org/sqlite` | v1.23.1 | BSD-3-Clause | Copyright (c) 2017 The Sqlite Authors. All rights reserved. |

`github.com/google/go-cmp` is an indirect requirement because the protobuf
module's tests import it; no Natcatty package imports it and it is not linked
into the distribution.

## Network Provider dependency baseline

The in-process Tailscale and WireGuard network providers link the modules below
through `adapters/tailscale` and `adapters/wireguard`. They are in the `go.mod`
module graph, and the minimal import probe in `internal/netprobe` compiles the
packages the adapters use under `CGO_ENABLED=0` on every supported target. The
pinned graph is listed here so every linked module is auditable; upgrading
either provider dependency follows the audit requirements recorded in `go.mod`.

| Module | Version | License | Copyright notice |
| --- | --- | --- | --- |
| `filippo.io/edwards25519` | v1.2.0 | BSD-3-Clause | Copyright (c) 2009 The Go Authors. All rights reserved. |
| `github.com/akutz/memconn` | v0.1.0 | Apache-2.0 | Copyright akutz/memconn contributors (the Apache-2.0 LICENSE has no copyright line) |
| `github.com/alexbrainman/sspi` | v0.0.0-20250919150558-7d374ff0d59e | BSD-3-Clause | Copyright (c) 2012 The Go Authors. All rights reserved. |
| `github.com/coder/websocket` | v1.8.15 | ISC | Copyright (c) 2025 Coder |
| `github.com/creachadair/msync` | v0.10.1 | BSD-3-Clause | Copyright (C) 2022, Michael J. Fromberger |
| `github.com/dblohm7/wingoes` | v0.0.0-20260526185140-fb298caac7ca | BSD-3-Clause | Copyright (c) 2022, Tailscale Inc. |
| `github.com/fxamacker/cbor/v2` | v2.9.3 | MIT | Copyright (c) 2019-present Faye Amacker |
| `github.com/gaissmai/bart` | v0.29.0 | MIT | Copyright (c) 2026 Karl Gaissmaier |
| `github.com/go-json-experiment/json` | v0.0.0-20260820222146-c27c302e5fc3 | BSD-3-Clause | Copyright (c) 2020 The Go Authors. All rights reserved. |
| `github.com/godbus/dbus/v5` | v5.2.2 | BSD-2-Clause | Copyright (c) 2013, Georg Reinke, Google |
| `github.com/golang/groupcache` | v0.0.0-20241129210726-2c02b8208cf8 | Apache-2.0 | Copyright the groupcache authors (the Apache-2.0 LICENSE has no copyright line) |
| `github.com/google/btree` | v1.1.3 | Apache-2.0 | Copyright the btree authors (the Apache-2.0 LICENSE has no copyright line) |
| `github.com/google/go-cmp` | v0.7.0 | BSD-3-Clause | Copyright (c) 2017 The Go Authors. All rights reserved. |
| `github.com/hdevalence/ed25519consensus` | v0.2.0 | BSD-3-Clause | Copyright (c) 2009 The Go Authors. All rights reserved.; Copyright (c) 2020 Henry de Valence. All rights reserved. |
| `github.com/huin/goupnp` | v1.3.0 | BSD-2-Clause | Copyright (c) 2013, John Beisley |
| `github.com/jsimonetti/rtnetlink` | v1.4.2 | MIT | Copyright (C) 2016 Jeroen Simonetti |
| `github.com/klauspost/compress` | v1.20.0 | BSD-3-Clause (bundled components also carry Apache-2.0 and MIT notices) | Copyright (c) 2012 The Go Authors. All rights reserved.; Copyright (c) 2019 Klaus Post. All rights reserved. |
| `github.com/mdlayher/netlink` | v1.11.2 | MIT | Copyright (C) 2016-2026 Matt Layher |
| `github.com/mdlayher/socket` | v0.7.0 | MIT | Copyright (C) 2021 Matt Layher |
| `github.com/mitchellh/go-ps` | v1.0.0 | MIT | Copyright (c) 2014 Mitchell Hashimoto |
| `github.com/pires/go-proxyproto` | v0.15.0 | Apache-2.0 | Copyright the go-proxyproto authors (the Apache-2.0 LICENSE has no copyright line) |
| `github.com/safchain/ethtool` | v0.7.0 | Apache-2.0 | Copyright the ethtool authors (the Apache-2.0 LICENSE has no copyright line) |
| `github.com/tailscale/certstore` | v0.1.1-0.20260409135935-3638fb84b77d | MIT | Copyright (c) 2017 Ben Toews. |
| `github.com/tailscale/go-winio` | v0.0.0-20231025203758-c4f33415bf55 | MIT | Copyright (c) 2015 Microsoft |
| `github.com/tailscale/hujson` | v0.0.0-20260727124030-b80ff77dac4f | BSD-3-Clause | Copyright (c) 2019 Tailscale Inc. All rights reserved. |
| `github.com/tailscale/peercred` | v0.0.0-20250107143737-35a0c7bd7edc | BSD-3-Clause | Copyright (c) 2021, Tailscale Inc. |
| `github.com/tailscale/web-client-prebuilt` | v0.0.0-20260917222731-e0ed2d0d0fea | BSD-3-Clause | Copyright (c) 2020 Tailscale Inc & AUTHORS. |
| `github.com/tailscale/wireguard-go` | v0.0.0-20260928213032-417aef361226 | MIT | Copyright (C) 2017-2023 WireGuard LLC. All Rights Reserved. |
| `github.com/x448/float16` | v0.8.4 | MIT | Copyright (c) 2019 Montgomery Edwards and Faye Amacker |
| `go4.org/mem` | v0.0.0-20240501181205-ae6ca9944745 | Apache-2.0 | Copyright the go4.org authors (the Apache-2.0 LICENSE has no copyright line) |
| `go4.org/netipx` | v0.0.0-20260823151212-3075585bcbeb | BSD-3-Clause | Copyright (c) 2020 The Inet.af AUTHORS. All rights reserved. |
| `golang.org/x/exp` | v0.0.0-20260908205506-85c1c2202aba | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/net` | v0.60.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/oauth2` | v0.37.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/sync` | v0.24.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/term` | v0.47.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/time` | v0.16.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.zx2c4.com/wintun` | v0.0.0-20230126152724-0fa3db229ce2 | MIT | Copyright WireGuard LLC (the MIT LICENSE has no copyright line) |
| `golang.zx2c4.com/wireguard` | v0.0.0-20261024220952-b3bc704ef0ee | MIT | Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved. |
| `golang.zx2c4.com/wireguard/windows` | v1.0.1 | MIT | Copyright (C) 2018-2026 WireGuard LLC. All Rights Reserved. |
| `gvisor.dev/gvisor` | v0.0.0-20260915211658-a6f909f08a72 | Apache-2.0 | Copyright the gVisor authors (the Apache-2.0 LICENSE has no copyright line; the module carries an AUTHORS file) |
| `tailscale.com` | v1.104.5 | BSD-3-Clause | Copyright (c) 2020 Tailscale Inc & contributors. |

The pinned `tailscale.com` and `golang.zx2c4.com/wireguard` versions are
recorded in `go.mod` with their upgrade and audit requirements. The `replace`
directive for `golang.zx2c4.com/wireguard/tun/netstack` only selects the pinned
main module; it does not add or change a license.

Additional third-party portions embedded in those modules:

- **HarfBuzz** (bundled in `github.com/go-text/typesetting/harfbuzz`):
  MIT. Copyright (c) 2021 B. KUGLER; Copyright (c) 2010-2020 Google, Inc.;
  Copyright (c) 2018-2020 Ebrahim Byagowi; Copyright (c) 2019-2020 Facebook,
  Inc.; Copyright (c) 2012 Mozilla Foundation; Copyright (c) 2011 Codethink
  Limited. Full text: [MIT license](#mit-license).
- **Go standard library**: BSD-3-Clause. Copyright 2009 The Go Authors. Full
  text: [BSD 3-Clause license](#bsd-3-clause-license).
- **modernc.org/mathutil bundled `mersenne`**: BSD-3-Clause. Copyright (c)
  2014 The mersenne Authors. Full text:
  [BSD 3-Clause license](#bsd-3-clause-license).
- **modernc.org/memory bundled `mmap-go`**: BSD-3-Clause. Copyright (c) 2011,
  Evan Shaw <edsrzf@gmail.com>. Full text:
  [BSD 3-Clause license](#bsd-3-clause-license).

## In-process Mosh client

- Component: the Go Mosh client in `adapters/mosh` and `internal/mosh`; the
  terminal model under `internal/mosh/terminal` is a port of stock mosh 1.4.0
  (parser, emulator, framebuffer, dispatcher, display, user input, and terminal
  overlays)
- Upstream terminal model: stock mosh 1.4.0 `src/terminal/`,
  `src/statesync/completeterminal.cc`, and `src/frontend/terminaloverlay.cc`
- Source:
  <https://github.com/mobile-shell/mosh/releases/download/mosh-1.4.0/mosh-1.4.0.tar.gz>
- SHA-256:
  `872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd`
- Copyright: Copyright (C) 2012 Keith Winstein and mosh contributors
- License: GPL-3.0-or-later (full text: [`LICENSE`](../LICENSE))
- Shipped notice: [`internal/mosh/NOTICE`](../internal/mosh/NOTICE)
- Parity harness and recorded image/source provenance:
  `internal/mosh/terminal/testdata/parity/harness/README.md`

The Go port is distributed under the app's GPL-3.0-or-later license. The parity
harness downloads and builds the pinned stock source only inside a Docker image
at test time; no stock mosh source or binary is committed or shipped with the
application. Natcatty does not bundle, ship, or launch a separate `mosh-client`
binary: every Mosh Session uses this in-process client.

**mosh-go OCB3 adaptation.** The OCB3 implementation in `internal/mosh/ocb.go`
is adapted from [`unixshells/mosh-go`](https://github.com/unixshells/mosh-go)
at commit `0284bd652aab3dfff5b0b74945c23901d7537b61` (tag `v0.5.2`),
Copyright (c) 2026 Unix Shells, under the MIT license. The wire framing,
protobuf field numbers, fragment handling, and SSP behavior were implemented
against the mosh project's protocol and source code, with mosh-go as a
reference. The MIT notice travels with the source at
[`internal/mosh/NOTICE`](../internal/mosh/NOTICE); the [MIT text](#mit-license)
below applies to the adapted portions.

## EternalTerminal client (in-process, vendored et-go)

- Component: the pure-Go EternalTerminal client forked from
  [`tphakala/et-go`](https://github.com/tphakala/et-go)
- No separate `et` binary is bundled or launched; every EternalTerminal
  Session uses this in-process client.
- Upstream commit: `bc33dd76b558418bc1079ba27c8c477b8145eea5`
  (`ci: bump golangci-lint to v2.15.0`)
- License: Apache License 2.0
- Copyright: Copyright 2026 Tomi P. Hakala
- Vendored path: `adapters/et/internal/etgo`
- Local documents: `adapters/et/internal/etgo/LICENSE`,
  `adapters/et/internal/etgo/NOTICE`, and
  `adapters/et/internal/etgo/PROVENANCE.md`. The provenance file lists every
  copied package and each modification from upstream.
- Modifications: Go import paths rewritten to this module, Apache-2.0 SPDX
  headers added, the console package reduced to the portable `Size` type, and
  the upstream subprocess SSH bootstrap replaced by a command builder that
  runs `etterminal` through `connection.Manager`. `etcp` and `seal` are
  extended locally for EternalTerminal 7.1.1 challenge authentication, reset
  recovery, RETRY_LATER handling, epoch rekey, and displacement detection; see
  `PROVENANCE.md`.
- The protocol buffer definitions under `protocol/proto` are copied from
  [EternalTerminal](https://github.com/MisterTea/EternalTerminal) at tag
  `et-v7.1.1`, commit `a8318ca4598756ca3c5f4711cce12a65faeff57d`.
  Copyright Jason Gauci and contributors, Apache License 2.0
- Full text: [Apache License 2.0](#apache-license-20)
- **Pending human/legal review:** this attribution follows the vendored
  tree's `PROVENANCE.md`; it has not been independently reviewed.

## Platform components that are not distributed

Natcatty uses the operating system's webview (WKWebView on macOS, WebView2 on
Windows, WebKitGTK on Linux) and the system font collection. Those components
are provided by the operating system and are not copied into Natcatty's
artifacts. The `.deb` does not depend on or bundle a CJK font.

## License texts

### MIT license

MIT License

The copyright notice for each component is listed in the component sections above.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

### BSD 3-Clause license

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

### BSD 2-Clause license

Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met:

 * Redistributions of source code must retain the above copyright notice, this list of conditions and the following disclaimer.
 * Redistributions in binary form must reproduce the above copyright notice, this list of conditions and the following disclaimer in the documentation and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

### ISC license

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

### Apache License 2.0

                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "{}"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright {yyyy} {name of copyright owner}

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.

### LLVM exception to the Apache License 2.0

The `wasi` crate offers this exception as one of its license choices:

As an exception, if, as a result of your compiling your source code, portions
of this Software are embedded into an Object form of such source code, you
may redistribute such embedded portions in such Object form without complying
with the conditions of Sections 4(a), 4(b) and 4(d) of the License.

In addition, if you combine or link compiled forms of this Software with
software that is licensed under the GPLv2 ("Combined Software") and if a
court of competent jurisdiction determines that the patent provision (Section
3), the indemnity provision (Section 9) or other Section of the License
conflicts with the conditions of the GPLv2, you may retroactively and
prospectively choose to deem waived or otherwise exclude such Section(s) of
the License, but only in their entirety and only with respect to the Combined
Software.

## Verification and open gaps

- `internal/legal` tests the repository's own legal files and the pinned
  in-process Mosh and EternalTerminal provenance, so `go test
  ./internal/legal` fails if a required notice or provenance marker
  disappears.
- **Ticket 01 baseline:** the release validation job runs the pinned scanner
  `golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`; upgrading the scanner is a
  dependency-baseline change and needs the same audit as a `go.mod` change. At
  this baseline it reported no vulnerabilities affecting called code and three
  module-only findings that the code does not call: GO-2026-6866 and
  GO-2026-6860 in `golang.org/x/net v0.60.0` (fixed upstream in v0.62.0), and
  GO-2026-5932 in `golang.org/x/crypto v0.58.0` (fixed upstream in
  v0.58.1-0.20261009181718-2f1f834330b2). The release gate is a point-in-time
  advisory check, not a standing guarantee.
- **Bundled Mosh client removed:** the former bundled `mosh-client` binary,
  its shipped Rust crate notices, and its packaged resources no longer ship.
  Every Mosh Session uses the in-process Go client described above.
- **Bundled ET client removed:** the former Netcatty-et-bin `et-bin-6.2.10-1`
  binary (`et`/`et.exe`), its provenance/license/README files, and its packaged
  resources no longer ship. Every EternalTerminal Session uses the in-process
  vendored et-go client described above.
- **Not established / pending human and independent review:** independent
  verification that the mosh-go portions contain no GPL-derived expression;
  legal conclusions on derivative-work status, MIT/GPL compatibility, and the
  effectiveness of the grants; ownership and AI-assistance provenance for
  mosh-go; OCB patent scope. These are human-in-the-loop items in
  `docs/audit/mosh-provenance.md` and are not completed by this file.
- **Gap, human action:** the published `v0.1.0` release remains uncorrected
  until a human with release rights applies
  `docs/releases/v0.1.0-correction.md` or publishes a corrective release.
- **Pending human and legal review:** derivative-work scope, copyright-holder
  lines, and whether this notice set is sufficient for each distribution
  channel. This file does not certify legal compliance and no independent audit
  has been completed.
