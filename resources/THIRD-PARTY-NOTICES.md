# Third-party notices

Netcatty distributes two client binaries next to the application. Both are
optional: without the matching bundle, the Mosh or EternalTerminal transport
fails with a typed error and the app does not execute a system copy.

## mosh-client

- Component: `mosh-client` from MoshCatty `moshcatty-0.1.8`
- Source: <https://github.com/binaricat/MoshCatty>
- Upstream: <https://github.com/mobile-shell/mosh>
- License: GNU General Public License, version 3 or later
- Full text: `mosh/LICENSE-MoshCatty-GPL-3.0.txt`
- Provenance and per-target checksums: `mosh/BUILD-PROVENANCE.json`,
  `mosh/README.md`

GPL-3.0-or-later requires the corresponding source. Netcatty's distribution
must accompany the binary with the license text and a written offer or link to
the MoshCatty source at the release tag.

## EternalTerminal client (`et`)

- Component: `et` from Netcatty-et-bin `et-bin-6.2.10-1`
- Source: <https://github.com/binaricat/Netcatty-et-bin>
- Upstream: <https://github.com/MisterTea/EternalTerminal> at `et-v6.2.10`
  (commit `f9a584ac06b2f1730b5bdf0a27150f28478368fb`)
- License: Apache License 2.0
- Full text: `et/LICENSE-EternalTerminal-Apache-2.0.txt`
- Provenance and build notes: `et/BUILD-PROVENANCE.json`, `et/README.md`

## Verification

Every launch recomputes the binary's SHA-256 and byte size against the
manifest compiled into the application (`adapters/bundle/manifests.go`). The
fetch tool verifies release archive digests before extraction. A binary that
does not match is never executed.
