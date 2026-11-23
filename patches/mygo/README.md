# MyGo patches

MyGo is a git submodule at `third_party/mygo`, pinned by the parent
repository's gitlink to upstream v0.10.0
(`ebde9399baf2734b3361856a53f4036859973d1d`). The submodule is never edited in
place in this repository's history: the patches in this directory are applied
to a clean checkout before building.

Run `scripts/apply-mygo-patches.sh` after checking out the repository, or use
`just prepare-mygo`. The script initializes the submodule, resets it to the
pinned commit (removing untracked files), and applies every `*.patch` here in
name order. It also refuses to prepare the build when `go.mod`'s
`github.com/egoist/mygo` version and the submodule's exact tag differ: the
replace directive would otherwise compile one release while `go.mod` names
another. `just dev`, `just test`, and `just build` depend on it, and CI runs
it before Go.

The patches are relative to v0.10.0 and are applied in name order, because
later ones build on earlier ones (0004 extends the tracking modes 0002 adds).
If the submodule moves to another upstream commit, rebase them and re-run the
archive and terminal tests.

If GitHub over HTTPS is unavailable in your environment, point the submodule
at SSH locally (not committed):

```sh
git config submodule.third_party/mygo.url git@github.com:egoist/mygo.git
```

## Why the patches exist

Upstream `plugins/terminal/view.go` computes the link under the pointer before
it decides whether the terminal program is taking the mouse. A program with
DEC mouse tracking on (modes 9, 1000, 1001, 1002, or 1003) therefore received
no report for a Command+click over a link-looking string; MyGo opened the link
instead. TUIs commonly bind Command/Alt+click to their own action. The patches
keep the link policy in MyGo, where the ordering lives, without vendoring its
source into this repository.

## The phone keyboard patch

`0005-ios-reshow-keyboard-after-manual-dismissal.patch` is one of the two
phone-only patches (the web-view one is below). The MyGo iOS host in v0.10.0
cannot re-show the soft keyboard after the
user dismisses it with the keyboard's own key: `SurfaceWindow.setTextInput`
calls `becomeFirstResponder()`, but the view never stopped being first
responder, so UIKit does nothing. The patch tracks an explicit dismissal and
uses `reloadInputViews()` (or a fresh `becomeFirstResponder()`) only when a
tap asks for the keyboard, so the resize after a dismissal does not raise it
again.

## The phone web-view patch

`0006-mobile-data-stores-cookies-downloads.patch` adds what the phone browser
needs from MyGo's mobile host. In v0.10.0 `mobile.Backend` embeds
`unsupported.Backend`, so `NewDataStore` returns `errUnsupported`, and no
cookie or download call exists at all. The patch adds the wire calls and
events, the Go implementation behind them, and the iOS (`WKWebsiteDataStore`,
`WKHTTPCookieStore`, `WKDownloadDelegate`) and Android (`ProfileStore`,
`CookieManager`, `WebView.setDownloadListener`) host sides.

It touches `internal/mobile/{wire/gen/spec.go,wire/msgs.go,backend.go,
window.go,events.go,mobile_test.go}` plus the generated `Wire.swift` and
`Wire.kt`, `datastore.go`, `datastore_test.go`, and the four host files
`internal/mobile/ios/Sources/MyGoHost/{Host,WebWindow}.swift` and
`internal/mobile/android/mygo/src/main/java/dev/mygo/{HostImpl,WebWindow}.kt`.
The generated wire files are produced with
`go generate ./internal/mobile/wire` after editing the spec; regenerate rather
than editing them by hand. The calls only reach mobile hosts, so the desktop
backend and its behaviour are unchanged.

At an upgrade, drop the patch if the release carries per-site data stores,
cookie access and download events; otherwise rework it against the new host
sources. Keeping it is a product decision, as the ADR says for new patches.

## The fork carries the same desktop fixes

Natcatty's desktop and phone terminal now run the source fork in `ui/terminal`
(it forked MyGo's terminal so the phone could use one core). The four desktop
fixes above are therefore carried in that fork's source as well, and the copy
in this directory keeps MyGo's own bundled plugin consistent. When an upgrade
moves either tree, update both: the patches here for the submodule, and the
forked files for the terminal Natcatty builds.
