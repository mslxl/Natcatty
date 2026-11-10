# WebView restricted to the host-routed browser

`ui.WebView` is permitted only for the host-routed browser side panel and for the
plugin WebView sandbox; every other surface is native. MyGo v0.5.5 has no per-webview
session partitioning, so treating the webview as a general UI substrate would weaken
both security and consistency; the cost is that arbitrary web content isolation must be
verified and may be impossible without separate windows.

OAuth does not use a webview. GitHub, Google, and Microsoft authorization always open the
system browser with a loopback redirect, matching the original and avoiding a second
browser surface inside the app.

**Amendment (plugin system).** Plugin views and embedded plugin WebView nodes may use
`ui.WebView`, but only through the plugin sandbox defined by ADR-0019: a per-plugin
origin `natcatty-plugin://<plugin-id>/`, strict CSP, a narrow verified bridge, and no
access to the host browser panel's session or bindings. The host-routed browser panel
remains the only WebView outside that sandbox. MyGo v0.7.4 gives each page an explicit
session; the sandbox gives every page of a plugin one ephemeral per-plugin session, so
page storage is in-memory, per-plugin, and gone with the plugin. The origin, CSP, and
bridge discipline remains the boundary that separates plugins, and the host browser
keeps the default session.

**Status**: accepted

**Consequences**:
- Vault, SFTP, settings, terminals, dialogs, tray, OAuth, and the app-lock gate must not
  use a webview.
- `ui.WebView` may be called only from `ui/browser/` and `ui/pluginwebview/`; the
  architecture test enforces this allowlist.
- The browser panel must enforce no Node integration, strict CSP, origin allowlists, and
  a narrow typed bridge.
- Origin gateways prefer the loopback address `127.0.0.2`, which MyGo development builds
  do not auto-trust, and fall back to `127.0.0.1` only where the OS does not assign the
  preferred address (macOS `lo0`). That fallback is inside MyGo's development trust set;
  the app must therefore never register a MyGo binding for the browser gateway, and a
  gateway must never fall back on a bind error other than an unassigned address or an
  unsupported address family.
- Per-host cookie/local-storage isolation is not promised until a prototype proves it.
