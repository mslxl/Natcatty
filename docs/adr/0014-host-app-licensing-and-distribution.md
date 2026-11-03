# Keep the host app GPL-3.0-or-later and defer mobile/App Store distribution

The native Natcatty app is a GPL-3.0-or-later derivative of `binaricat/Netcatty`: the rewrite has no root LICENSE, but it ports code and data from the upstream app and copies substantial expression (locale strings, catalogs, presets, themes, icon). It stays GPL-3.0-or-later. Desktop distribution and the mosh/ET migration proceed under that license; mobile and App Store work is deferred until a licensing route exists. A GPLv3 app cannot ship on the App Store without an exception or non-assertion from every upstream copyright holder, and a fork cannot grant that unilaterally.

**Status**: accepted

**Considered Options**:
- Seek permission, relicense, or an App Store non-assertion from all upstream copyright holders: keeps the App Store option, but is legally uncertain and depends on contributors the fork does not control.
- Keep the desktop app GPL and build a separate clean-room permissive mobile app: preserves the App Store target but is a new product that cannot reuse current code, catalogs, locale strings, or icon.
- Keep GPL and distribute outside the App Store: desktop direct, Android via Play, iOS only through limited non-App-Store channels.
- Pause mobile and finish desktop first (chosen together with keeping GPL).

**Consequences**:
- Restore a root GPL-3.0-or-later LICENSE, modification notice, third-party notices, per-release corresponding-source offer, in-app About/Licenses surface, and SPDX/release checks; correct the published `v0.1.0`.
- GPL-compatible reuse is admissible, so MoshCatty may be used as a reference or adapted; permissive-license constraints no longer bind desktop work.
- Mobile is deferred. Reopen only if all upstream copyright holders grant a non-assertion or relicense, a separate clean-room permissive mobile app is chartered, or non-App-Store iOS distribution is accepted.
- MyGo, the Go dependencies, MoshCatty, and EternalTerminal are all GPL-compatible; no dependency blocks desktop distribution.
