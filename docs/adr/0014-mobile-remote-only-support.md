# The phone app is a remote-only, natively rendered terminal

Natcatty ships iOS 17+ and Android 8+ (API 26) builds from the same codebase as
the desktop app. A phone build carries only remote Session kinds — SSH, telnet,
Mosh and EternalTerminal. The phone composition root registers no local PTY and
no serial adapter, and a Local or Serial StartSpec fails closed. The terminal is
natcatty's own fork of libghostty-vt, compiled natively into the app on every
platform. `ui.WebView` is used only for the Host Browser's web area, reached
through that Host's dedicated SSH Channel lease; the terminal and every other
phone surface — the Sessions, Hosts, Vault and Settings shell, its dialogs and
its Session tool pages — stay native, per
[ADR-0007](0007-webview-boundary.md). On iOS the static library reaches the app
through a staged `*_ios_arm64.syso` object built with `ld -r -all_load` — the
device and simulator slices are staged separately — with an empty `#cgo LDFLAGS`,
so MyGo's Swift final link is untouched (method A). Android links one static
archive per ABI through `#cgo LDFLAGS` with 16 KB page alignment. MyGo v0.10.x is
the floor. The phone
Workspace is the shared `domain/workspace` split tree, not a flat one-Tab-per-
Session, one-Pane-per-Tab structure: it persists the split structure, focus,
split ratios, each Pane's `Dir`, and Pane/Tab IDs, so a phone-saved Workspace
opens unchanged on the desktop. A phone or a folded iPhoneDuo renders only the
focused Pane and switches Panes within the same Tab; a tablet and an expanded
iPhoneDuo render the visible Panes of the same tree. Hidden Panes keep running,
and only an explicit close ends a Session. Desktop behaviour does not change.

**Status**: accepted

**Considered Options**:
- A webview terminal, or a webview hosting the native shell: rejected. It cannot
  deliver native IME composition, selection, mouse-mode latching, or the input
  behaviour phones need, and it conflicts with
  [ADR-0007](0007-webview-boundary.md)'s boundary. The webview stays confined to
  the Host Browser's web area.
- Ship MyGo's terminal plugin unchanged: rejected. Upstream has no phone
  library, its ABI is pre-1.0, and its loader is desktop-only.
- A flat phone Workspace — one Tab per Session, one Pane per Tab: rejected. It
  cannot round-trip the shared split tree or restore focus, ratios and each
  Pane's directory.
- Desktop-only Session kinds and chrome on phones — local PTY, serial, and
  window chrome: rejected for v1. A phone is not a workstation; those Session
  kinds and that chrome do not belong there.
- The shared-core terminal fork with one loader per platform, on the shared
  split-tree Workspace (chosen).

**Consequences**:
- Phones get their own composition root and a four-section native shell
  (Sessions, Hosts, Vault, Settings); `domain` and `application` are shared with
  the desktop, and the phone dependency gate keeps desktop UI packages out of
  the phone graph.
- The phone round-trips the shared `domain/workspace` split tree — split
  structure, focus, ratios, each Pane's `Dir` and Pane/Tab IDs — so a phone-saved
  Workspace opens unchanged on the desktop. Hidden Panes keep running with one
  live view per Session.
- WebView remains confined to the Host Browser's web area per ADR-0007; the
  terminal and every other phone surface stay native.
- The terminal core is one package for both platforms; only the loader differs,
  so the desktop keeps its dynamic-library path.
- The staged iOS object must match the target: a device object (platform 2) in a
  simulator build (platform 7) fails the compile gate before the app starts.
- Local shell and serial remain desktop-only; the phone dependency gate fails
  when either reaches the phone graph.
- iOS background limits are accepted: the app reconnects or resumes on
  foreground, and server-side persistence is the story of last resort.
- The pinned terminal ABI and its migration plan are the subject of
  [ADR-0023](0023-pin-libghostty-vt-0.1.0-dev.md).
