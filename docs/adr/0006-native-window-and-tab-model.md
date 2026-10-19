# Native window and tab model

We reproduce the original window topology natively: one main window with a fixed root
surface set (`vault`, `sftp`), ordered work tabs, and a separate editor-tab namespace; a
singleton settings window; detached session and editor windows; and a tray entry point.
This preserves navigation and restore behavior users already rely on; the cost is that
UI state must be partitioned per window and the app-lock gate must wrap every window.

**Status**: accepted

**Considered Options**:
- A single-window app with an in-window settings page: simpler state, but breaks
  existing detach/settings workflows and persisted layout expectations.
- A web-style router: no fit for native windows and multi-window state.

**Consequences**:
- Window-scoped state lives in `ui/<area>/state`; shared state stays in `application`.
- Plugin view tabs are removed; editor tabs remain a separate namespace.
- Tray on Linux degrades to a small native window because AppIndicator exposes a menu
  only.
