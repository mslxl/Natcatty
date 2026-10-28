# WebView restricted to the host-routed browser

`ui.WebView` is permitted only for the host-routed browser side panel; every other
surface is native. MyGo v0.5.5 has no per-webview session partitioning, so treating the
webview as a general UI substrate would weaken both security and consistency; the cost
is that arbitrary web content isolation must be verified and may be impossible without
separate windows.

OAuth does not use a webview. GitHub, Google, and Microsoft authorization always open the
system browser with a loopback redirect, matching the original and avoiding a second
browser surface inside the app.

**Status**: accepted

**Consequences**:
- Vault, SFTP, settings, terminals, dialogs, tray, OAuth, and the app-lock gate must not
  use a webview.
- The browser panel must enforce no Node integration, strict CSP, origin allowlists, and
  a narrow typed bridge.
- Origin gateways prefer the loopback address `127.0.0.2`, which MyGo development builds
  do not auto-trust, and fall back to `127.0.0.1` only where the OS does not assign the
  preferred address (macOS `lo0`). That fallback is inside MyGo's development trust set;
  the app must therefore never register a MyGo binding for the browser gateway, and a
  gateway must never fall back on a bind error other than an unassigned address or an
  unsupported address family.
- Per-host cookie/local-storage isolation is not promised until a prototype proves it.
