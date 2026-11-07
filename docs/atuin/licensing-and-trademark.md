# Atuin licensing, attribution, and trademark constraints

Recorded for the Atuin-compatible history sync feature. This is engineering
policy documentation, not legal advice. Human and legal review remain required
before release.

## Upstream project and reference pin

| Item | Value |
| --- | --- |
| Project | Atuin (`atuinsh/atuin`) |
| Reference release | v18.24.5 |
| Reference commit | `ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e` |
| Release date | 2026-11-04 |
| Copyright | Copyright (c) 2026 Atuin, Inc. |
| License | MIT |
| Source | <https://github.com/atuinsh/atuin/tree/ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e> |
| License file | <https://github.com/atuinsh/atuin/blob/ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e/LICENSE> |

## MIT notice and attribution

Netcatty implements the Atuin v18 history sync wire protocol independently in
Go. Where Netcatty contains code, constants, or data derived from Atuin, those
portions are used under the MIT license and the following notice applies to
them.

```text
MIT License

Copyright (c) 2026 Atuin, Inc.

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
```

Attribution: protocol behavior, record encoding, encryption, and endpoint
semantics are to be implemented against the pinned Atuin source; the official
Atuin CLI and server will serve as conformance oracles. The MIT license permits
reuse in this GPL-3.0-or-later application; MIT is compatible with the GPL, so
the project's own license does not conflict with it.

### Release-time notice obligation

- Keep this notice and attribution with any Atuin-derived source.
- Before the first release that contains Atuin-derived code, add an Atuin entry
  to [`resources/THIRD-PARTY-NOTICES.md`](../../resources/THIRD-PARTY-NOTICES.md)
  and to the shipped notice surface (About/Licenses). The entry must carry the
  copyright line, the MIT text, the pinned version and commit, and what was
  derived.
- Do not remove or alter the copyright line, and do not describe Atuin-derived
  portions as GPL-only.
- At the time of this ticket, no Atuin source is vendored or copied into this
  repository. This section records the obligation for the sync adapter that
  follows; it is not a claim that Atuin code already ships.

### Prepared third-party notice entry

This is the entry to add to
[`resources/THIRD-PARTY-NOTICES.md`](../../resources/THIRD-PARTY-NOTICES.md)
when Atuin-derived code, constants, or data first ships. It is deliberately not
in the shipped notices yet, because adding it before any derived material exists
would falsely imply that Atuin code is included in the current distribution.

```markdown
## Atuin history sync protocol reference

- Component: Atuin, the reference implementation of the Atuin v18 history sync
  wire protocol
- Version: v18.24.5, commit `ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e`
- Source: <https://github.com/atuinsh/atuin/tree/ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e>
- License: MIT (full text: [MIT license](#mit-license))
- Copyright: Copyright (c) 2026 Atuin, Inc.
- Shipped notice: `docs/atuin/licensing-and-trademark.md`
- Derived material: protocol behavior, record encoding, encryption
  construction, and endpoint semantics implemented for interoperability. No
  Atuin binary, application source tree, logo, or other brand asset is bundled.
- Independent client: Netcatty is an independent client and is not affiliated
  with Atuin, Inc. "Atuin" is used descriptively to identify the upstream
  project and protocol.
```

## Trademark and naming constraints

The MIT license covers code. It does not grant rights to the "Atuin" name,
logo, wordmark, or other brand assets. Trademark registration status was not
verified, so these rules assume the name and marks belong to Atuin, Inc.

1. **Use the name descriptively and nominatively only.** Refer to the upstream
   project, its server software, and the protocol when that is what the
   sentence means. Do not use "Atuin" as the name of a Netcatty feature, add-on,
   edition, or product line.
2. **Preferred UI and documentation labels:**
   - "Atuin-compatible sync"
   - "History sync (Atuin v18)"
   - "Connect your Atuin account"
   - "Atuin server URL"
   - "Atuin master key"
   - "Netcatty is an independent client and is not affiliated with Atuin, Inc."

   "Atuin integration" may be used as a descriptive section title in
   documentation where the surrounding text makes independence clear; prefer
   "Atuin-compatible sync" in product surfaces.
3. **Avoid product-name and endorsement uses:** "Netcatty Atuin", "Atuin for
   Netcatty", "Official Atuin client", "Powered by Atuin", "Atuin partner",
   "Atuin-certified", and any phrasing that implies Atuin, Inc. produces,
   approves, sponsors, or supports Netcatty.
4. **No brand assets.** Do not use the Atuin logo, app icon, wordmark,
   typeface styling, or official artwork in the Netcatty UI, installer,
   website, screenshots, or marketing. Do not imitate Atuin's visual identity.
5. **Disclaimer placement.** The independent-client disclaimer must appear in
   the Atuin account panel and sync status surface (visible without opening a
   details control), in the top-level documentation for this feature, and
   adjacent to the first prominent use of the name in README or marketing copy.
6. **Hosted service references.** The default address `https://api.atuin.sh` and
   the phrase "Atuin server" are functional references to the service the user
   chooses to connect. They must not suggest a partnership. The user supplies
   the account; Netcatty does not register accounts.
7. **Self-hosted servers.** Referring to "self-hosted Atuin" or "Atuin v18
   server" is descriptive of the software the user runs and does not imply
   endorsement.
8. **Comply with maintainer requests.** If Atuin, Inc. or the maintainers ask
   for a naming or disclaimer change, make the change promptly and keep the
   compatibility statement factual. Do not argue trademark rights in public.

### Pending review

- Human/legal trademark review is pending.
- Until review closes, do not ship Atuin-branded artwork and do not use
  "official", "partner", or endorsement language anywhere.
