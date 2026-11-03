# Third-party notices

Natcatty Native is a modified version of
[Netcatty](https://github.com/binaricat/Netcatty) and is distributed under
GPL-3.0-or-later. The application's own license and modification notice are in
`LICENSE` and `NOTICE`. This file lists every third-party component that the
current desktop distribution includes, the license each component uses, and the
copyright notices gathered so far. Provenance gaps are marked `TODO(ticket 15)`
and must be resolved before the next distribution.

The corresponding source for a release is this repository at the release tag
(for example <https://github.com/mslxl/natcatty/tree/v0.1.0>); build
instructions are in `docs/packaging.md`. The app never downloads code at
runtime: the listed Go modules are compiled in, and the listed client binaries
and native libraries are packaged with the app.

**This file is engineering work, not legal advice. Provenance gaps are marked
`TODO(ticket 15)` and require human and legal review.**

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
| `github.com/dustin/go-humanize` | v1.0.1 | MIT | Copyright (c) 2005-2008 Dustin Sallings |
| `github.com/ebitengine/purego` | v0.11.1 | Apache-2.0 | Copyright The Ebitengine Authors (SPDX-FileCopyrightText in the source files) |
| `github.com/egoist/mygo` | pinned in `go.mod` | MIT | Copyright (c) 2026 MyGo contributors |
| `github.com/glebarez/go-sqlite` | v1.21.2 | BSD-3-Clause | Copyright (c) 2017 The Sqlite Authors. All rights reserved. |
| `github.com/glebarez/sqlite` | v1.11.0 | MIT | Copyright (c) 2013-NOW Jinzhu |
| `github.com/go-text/typesetting` | v0.3.5 | BSD-3-Clause (dual-licensed Unlicense OR BSD-3-Clause; this distribution relies on BSD-3-Clause) | Copyright 2021 The go-text authors |
| `github.com/google/uuid` | v1.3.0 | BSD-3-Clause | Copyright (c) 2009,2014 Google Inc. All rights reserved. |
| `github.com/jinzhu/inflection` | v1.0.0 | MIT | Copyright (c) 2015 - Jinzhu |
| `github.com/jinzhu/now` | v1.1.5 | MIT | Copyright (c) 2013-NOW Jinzhu |
| `github.com/kayrus/putty` | v1.0.5 | Apache-2.0 | Copyright kayrus and the putty package contributors (the package LICENSE carries the Apache-2.0 text without a copyright line) |
| `github.com/kr/fs` | v0.1.0 | BSD-3-Clause | Copyright (c) 2012 The Go Authors. All rights reserved. |
| `github.com/mattn/go-isatty` | v0.0.17 | MIT (Expat) | Copyright (c) Yasuhiro MATSUMOTO <mattn.jp@gmail.com> |
| `github.com/pkg/sftp` | v1.13.11 | BSD-2-Clause | Copyright (c) 2013, Dave Cheney |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | Copyright (c) 2012 The Go Authors. All rights reserved. |
| `golang.org/x/crypto` | v0.58.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/image` | v0.46.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `golang.org/x/text` | v0.43.0 | BSD-3-Clause | Copyright 2009 The Go Authors. |
| `gorm.io/gorm` | v1.31.1 | MIT | Copyright (c) 2013-present Jinzhu |
| `modernc.org/libc` | v1.22.5 | BSD-3-Clause | Copyright (c) 2017 The Libc Authors. All rights reserved.; portions Copyright (c) 2009 The Go Authors. All rights reserved. |
| `modernc.org/mathutil` | v1.5.0 | BSD-3-Clause | Copyright (c) 2014 The mathutil Authors. All rights reserved.; bundled mersenne code Copyright (c) 2014 The mersenne Authors. All rights reserved. |
| `modernc.org/memory` | v1.5.0 | BSD-3-Clause | Copyright (c) 2017 The Memory Authors. All rights reserved.; portions Copyright (c) 2009 The Go Authors. All rights reserved.; mmap-go portion Copyright (c) 2011, Evan Shaw <edsrzf@gmail.com> |
| `modernc.org/sqlite` | v1.23.1 | BSD-3-Clause | Copyright (c) 2017 The Sqlite Authors. All rights reserved. |

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
- **unicode-ident** (a Rust crate, listed below) carries Unicode data under the
  Unicode License V3. Full text:
  [Unicode License V3](#unicode-license-v3).

## MoshCatty client and its Rust crates

- Component: `mosh-client` from MoshCatty `moshcatty-0.1.8`
- Release repository: <https://github.com/binaricat/MoshCatty>
- Release tag and corresponding source:
  <https://github.com/binaricat/MoshCatty/tree/moshcatty-0.1.8>
- License: GPL-3.0-or-later
- Authors declared in `Cargo.toml`: `binaricat`, `Netcatty contributors`
- Full text: [`LICENSE`](../LICENSE) and
  [`mosh/LICENSE-MoshCatty-GPL-3.0.txt`](mosh/LICENSE-MoshCatty-GPL-3.0.txt)
- Packaged binaries: `resources/mosh/<target>/mosh-client` (and
  `mosh-client.exe` on Windows)
- Provenance and per-target checksums: `mosh/BUILD-PROVENANCE.json`,
  `mosh/README.md`

MoshCatty is an independent pure-Rust client. The bundled Windows x64 binary
was compared byte-for-byte with the published `moshcatty-0.1.8` asset in the
ticket 13 audit. The `mosh/README.md` and `mosh/BUILD-PROVENANCE.json` text that
described MoshCatty as a build of `mobile-shell/mosh` is inaccurate and is
being corrected under `TODO(ticket 15)`.

The 49 third-party Rust crates pinned by MoshCatty's `Cargo.lock` are compiled
into `mosh-client`. The license expressions below are the ones published by
crates.io for those exact versions. A full per-crate copyright-notice appendix
and the mosh-go MIT notice are still missing from the distribution; that work is
tracked by ticket 15 (`TODO(ticket 15)`).

| Crate | Version | License expression |
| --- | --- | --- |
| `adler2` | 2.0.1 | 0BSD OR MIT OR Apache-2.0 |
| `aes` | 0.8.4 | MIT OR Apache-2.0 |
| `base64` | 0.22.1 | MIT OR Apache-2.0 |
| `cfg-if` | 1.0.4 | MIT OR Apache-2.0 |
| `cfg_aliases` | 0.2.1 | MIT |
| `cipher` | 0.4.4 | MIT OR Apache-2.0 |
| `cpufeatures` | 0.2.17 | MIT OR Apache-2.0 |
| `crc32fast` | 1.5.0 | MIT OR Apache-2.0 |
| `crypto-common` | 0.1.7 | MIT OR Apache-2.0 |
| `flate2` | 1.1.9 | MIT OR Apache-2.0 |
| `generic-array` | 0.14.7 | MIT |
| `getrandom` | 0.2.17 | MIT OR Apache-2.0 |
| `hex` | 0.4.3 | MIT OR Apache-2.0 |
| `inout` | 0.1.4 | MIT OR Apache-2.0 |
| `libc` | 0.2.186 | MIT OR Apache-2.0 |
| `miniz_oxide` | 0.8.9 | MIT OR Zlib OR Apache-2.0 |
| `once_cell` | 1.21.4 | MIT OR Apache-2.0 |
| `ppv-lite86` | 0.2.21 | MIT OR Apache-2.0 |
| `proc-macro2` | 1.0.106 | MIT OR Apache-2.0 |
| `quinn-udp` | 0.5.14 | MIT OR Apache-2.0 |
| `quote` | 1.0.46 | MIT OR Apache-2.0 |
| `rand` | 0.8.6 | MIT OR Apache-2.0 |
| `rand_chacha` | 0.3.1 | MIT OR Apache-2.0 |
| `rand_core` | 0.6.4 | MIT OR Apache-2.0 |
| `simd-adler32` | 0.3.9 | MIT |
| `socket2` | 0.6.5 | MIT OR Apache-2.0 |
| `syn` | 2.0.118 | MIT OR Apache-2.0 |
| `thiserror` | 2.0.18 | MIT OR Apache-2.0 |
| `thiserror-impl` | 2.0.18 | MIT OR Apache-2.0 |
| `typenum` | 1.20.1 | MIT OR Apache-2.0 |
| `unicode-ident` | 1.0.24 | (MIT OR Apache-2.0) AND Unicode-3.0 |
| `unicode-width` | 0.2.2 | MIT OR Apache-2.0 |
| `version_check` | 0.9.5 | MIT/Apache-2.0 |
| `wasi` | 0.11.1+wasi-snapshot-preview1 | Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT |
| `windows-link` | 0.2.1 | MIT OR Apache-2.0 |
| `windows-sys` | 0.60.2 | MIT OR Apache-2.0 |
| `windows-sys` | 0.61.2 | MIT OR Apache-2.0 |
| `windows-targets` | 0.53.5 | MIT OR Apache-2.0 |
| `windows_aarch64_gnullvm` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_aarch64_msvc` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_i686_gnu` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_i686_gnullvm` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_i686_msvc` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_x86_64_gnu` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_x86_64_gnullvm` | 0.53.1 | MIT OR Apache-2.0 |
| `windows_x86_64_msvc` | 0.53.1 | MIT OR Apache-2.0 |
| `zerocopy` | 0.8.54 | BSD-2-Clause OR Apache-2.0 OR MIT |
| `zerocopy-derive` | 0.8.54 | BSD-2-Clause OR Apache-2.0 OR MIT |
| `zeroize` | 1.8.2 | Apache-2.0 OR MIT |

`TODO(ticket 15)` also covers the MIT notice for `mosh-go` material that the
ticket 13 audit found adapted into MoshCatty (MIT, Copyright 2026 Unix Shells),
and the crate-level copyright notices. Do not publish a release while these
notices are missing from the distribution; the human reviewer must decide
whether the current text is sufficient.

## EternalTerminal client (`et`)

- Component: `et` from Netcatty-et-bin `et-bin-6.2.10-1`
- Release repository: <https://github.com/binaricat/Netcatty-et-bin>
- Upstream: <https://github.com/MisterTea/EternalTerminal> at `et-v6.2.10`
  (commit `f9a584ac06b2f1730b5bdf0a27150f28478368fb`)
- License: Apache License 2.0
- Full text: [`et/LICENSE-EternalTerminal-Apache-2.0.txt`](et/LICENSE-EternalTerminal-Apache-2.0.txt)
- Upstream `NOTICE`: none exists at the pinned `et-v6.2.10` ref (the release
  build tree and upstream tag were checked); Apache-2.0 therefore requires the
  license text and any notices that the build does carry, which ship next to
  the binary
- Packaged binaries: `resources/et/<target>/et` (and `et.exe` on Windows)
- Provenance and build notes: `et/BUILD-PROVENANCE.json`, `et/README.md`

## Platform components that are not distributed

Natcatty uses the operating system's webview (WKWebView on macOS, WebView2 on
Windows, WebKitGTK on Linux) and the system font collection. Those components
are provided by the operating system and are not copied into Natcatty's
artifacts. The `.deb` does not depend on or bundle a CJK font.

## License texts

### MIT license

MIT License

The copyright notice for each component is listed in the component table above.

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

### Unicode License V3

UNICODE LICENSE V3

COPYRIGHT AND PERMISSION NOTICE

Copyright © 1991-2026 Unicode, Inc.

NOTICE TO USER: Carefully read the following legal agreement. BY
DOWNLOADING, INSTALLING, COPYING OR OTHERWISE USING DATA FILES, AND/OR
SOFTWARE, YOU UNEQUIVOCALLY ACCEPT, AND AGREE TO BE BOUND BY, ALL OF THE
TERMS AND CONDITIONS OF THIS AGREEMENT. IF YOU DO NOT AGREE, DO NOT
DOWNLOAD, INSTALL, COPY, DISTRIBUTE OR USE THE DATA FILES OR SOFTWARE.

Permission is hereby granted, free of charge, to any person obtaining a
copy of data files and any associated documentation (the "Data Files") or
software and any associated documentation (the "Software") to deal in the
Data Files or Software without restriction, including without limitation
the rights to use, copy, modify, merge, publish, distribute, and/or sell
copies of the Data Files or Software, and to permit persons to whom the
Data Files or Software are furnished to do so, provided that either (a)
this copyright and permission notice appear with all copies of the Data
Files or Software, or (b) this copyright and permission notice appear in
associated Documentation.

THE DATA FILES AND SOFTWARE ARE PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF
THIRD PARTY RIGHTS.

IN NO EVENT SHALL THE COPYRIGHT HOLDER OR HOLDERS INCLUDED IN THIS NOTICE
BE LIABLE FOR ANY CLAIM, OR ANY SPECIAL INDIRECT OR CONSEQUENTIAL DAMAGES,
OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS,
WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION,
ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THE DATA
FILES OR SOFTWARE.

Except as contained in this notice, the name of a copyright holder shall
not be used in advertising or otherwise to promote the sale, use or other
dealings in these Data Files or Software without prior written
authorization of the copyright holder.

## Verification and open gaps

- A Go release check (`internal/legal/legal.go`, run as
  `go run ./internal/tools/releasecheck check-legal -dir DIR`) fails when
  `LICENSE`, `NOTICE`, `THIRD-PARTY-NOTICES.md`, or the per-release
  `SOURCE-OFFER.md` is missing or empty in an artifact. Add `-repository` to
  check a checkout, where the notices live under `resources/`.
- `internal/legal` also tests the repository's own legal files, so
  `go test ./internal/legal` fails if they disappear.
- **Gap, tracked by ticket 15:** per-crate Rust copyright notices and the
  mosh-go MIT notice; MoshCatty provenance wording; reproducible build evidence
  for MoshCatty.
- **Gap, human action:** the published `v0.1.0` release remains uncorrected
  until a human with release rights applies
  `docs/releases/v0.1.0-correction.md` or publishes a corrective release.
- **Pending human and legal review:** derivative-work scope, copyright-holder
  lines, and whether this notice set is sufficient for each distribution
  channel. This file does not certify legal compliance.
