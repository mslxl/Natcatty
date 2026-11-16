# Dropdown Panel is a fourth native window kind

ADR-0006 fixes the native window topology at a main window, a singleton
settings window, and detached session and editor windows. The global shortcut
now has four user-selectable Hotkey Modes, and two of them need a fourth kind:
a **Dropdown Panel** that pulls down from the top of the display under the
pointer, floats over other applications and full-screen Spaces, and renders
either a Quake Workspace it owns or the main window's Workspace. This ADR
records that Panel, its lifecycle, its relationship to the main and detached
windows, the one-live-view rule that keeps rendering single-owner, and where
the four modes are dispatched.

**Status**: accepted

**Amends [ADR-0006](0006-native-window-and-tab-model.md)**: ADR-0006 says the
app has one main window, one settings window, and detached session/editor
windows, and that a window's Workspace belongs to that window. It does not
provide for a floating Panel that borrows another window's Workspace. This ADR
adds the Dropdown Panel as a fourth native window kind and separates Workspace
ownership from rendering ownership: in Quake own mode the Panel owns a
memory-only Workspace of its own; in Follow Mode and top-down mode it renders
the main window's Workspace without owning it, and no Tab moves between
Workspaces. The rest of ADR-0006 remains in force: state stays partitioned per
window (`ui/quake` is window-scoped), and every window's content passes
through the one shared App Lock gate. ADR-0006 carries a matching "Amended by"
note so the conflict is visible from both sides.

**Considered Options**:
- Turn the main window itself into the Panel: rejected. Toggle mode must keep
  a normal, activating main window with native lifetime semantics; making it
  always-on-top, non-activating, frameless, and absent from the taskbar would
  change tray, quit, restore, and Dock behavior for every user.
- Use a detached window for Quake: rejected. Detached windows are user Tabs
  with their own persisted Workspace; they count as windows that keep the app
  alive, appear in the taskbar and app switcher, and on macOS activate the app.
  Follow Mode and top-down also must not detach or move main-window Tabs.
- Create one Panel per mode, or destroy and recreate the Panel when the mode
  or Quake source changes: rejected. The proven design routes one root
  `Content` inside one native Panel, so the terminal keeps its Surface and the
  window is never recreated mid-session. A fallback that recreates the Panel is
  a user-facing decision, not a silent implementation detail.
- Ship a separate Quake application or a second app instance: out of scope. It
  would fork Sessions, the tray list, notifications, Command History, App Lock,
  and the connection pool.
- Dispatch the four modes only in `app`: rejected. Registration, accelerator
  rollback, persistence, and settings validation belong in
  `application/desktop`, where fake `Hotkeys` and `Repository` ports make them
  testable without MyGo or windows.
- Persist Panel bounds per display: rejected for v1. The pointer can move to a
  different display or a display can be unplugged between shows; recomputing
  from the current work area is simpler and never restores an off-screen
  rectangle.

**Consequences**:
- **Window kind and enumeration.** The window kinds are main, settings,
  detached, and dropdown. The Dropdown Panel is requested at creation with
  MyGo's `Panel: true`; it is not a normal window converted after the fact. It
  joins the window manager's single enumeration so theme, shortcut preamble,
  opacity, vibrancy, icon, locale invalidation, and shared redraws apply to it,
  but it is marked as not counting toward "the app still has a window". It
  therefore never keeps the app alive and never blocks quit.
- **Relationship to main and detached windows.** The Panel is app-owned, not
  Tab-owned: no user action detaches or moves a Tab into it, it has no entry in
  the main Tab strip, and it is not restored from the main Workspace's
  persisted layout. Detached windows remain per-Tab, count as windows that keep
  the app alive, and keep their own persisted Workspace.
- **Creation and destruction.** The Panel is a singleton, created lazily on
  its first show and hidden afterwards. A close request, blur, and Escape hide
  it instead of destroying it; `OnClosed` only clears the manager's reference
  and restores rendering ownership. Quit hides and destroys it before the
  Quake Sessions are shut down. Mode and Quake-source changes hide a visible
  Panel and restore the main window; the next show creates or reuses the one
  Panel and routes it to the new mode.
- **Geometry.** The Panel is created hidden so its first bounds are applied
  before the first `Show`. It has no `StateKey` and MyGo restores no bounds for
  it. Each show reads `CursorScreenPoint`, selects `DisplayNearestPoint` and its
  `WorkArea` (falling back to the primary display), then sets a full-width,
  top-aligned rectangle whose height comes from the mode's percent or pixel
  setting, clamped to that mode's minimum and the work area. V1 does not allow
  dragging, resizing, or configuring width.
- **Workspace ownership.** In Quake own mode the Panel owns the **Quake
  Workspace**: a flat, memory-only list of terminal Tabs, one Session and
  Surface each, with no split Panes and no disk persistence. Its first show
  opens a local Shell; hiding, showing, and mode switches keep the same
  Sessions. Its Tabs never enter the main window's Tab strip.
- **Borrowed rendering.** Follow Mode renders the main Workspace's active Tab
  and top-down renders the complete main shell, both from the main state.
  Neither calls `DetachTab`, `Hibernate`, or `Resume`, and neither changes Tab
  order, the active index, the split tree, or Session ownership. The mini Tab
  strip in Follow Mode only selects a Tab. When the active Tab is not a
  terminal, or a browser panel is open, the Panel shows a neutral state that
  offers the main window instead of mounting an unmountable surface.
- **Single live view.** A Session still has exactly one live terminal view.
  Exactly one window renders a shell Surface in a frame: the **render owner**
  is `main` or `dropdown`. While the Panel owns rendering, the main window
  draws a neutral placeholder and the Panel's renderer takes over the shell's
  per-frame `Flush` and `FinishSideFrame`, so those run exactly once. Showing
  the Panel hides the main window only if it was visible; hiding the Panel
  shows and focuses it again only if the Panel hid it, so a window the user had
  already put away stays hidden. Own mode leaves the main window visible and
  does not transfer ownership.
- **Rendering costs.** Follow Mode resizes the live terminal to the Panel while
  it is shown and back when it hides, so scrollback may reflow; users who need
  the main window's exact scrollback should use own mode. MyGo prunes the
  element state of a subtree that leaves the tree, so switching routes
  re-establishes transient state and re-requests focus every frame. The
  terminal state itself lives on the app-owned `Terminal`, not in element
  state.
- **Hotkey dispatch.** The four-mode decision is split in two layers.
  `application/desktop` owns the `HotkeyMode` values (`none`, `toggle`,
  `quake`, `topdown`), their defaults, normalization, one
  `ToggleWindowHotkey` accelerator, registration and rollback, persistence,
  and a post-change notification. The composition root's
  `app.desktopController.HandleHotkey` owns the window action: `none` is a
  no-op, `toggle` raises or hides the main window, and `quake`/`topdown`
  toggle the one Dropdown Panel. The application layer never manipulates a
  Panel or a Quake Workspace. `none` unregisters the accelerator without
  registering an empty callback, so the shortcut returns to the system. A
  failed registration rolls the mode back. Unknown values normalize to the
  defaults, and setters reject them. This keeps
  [ADR-0001](0001-layered-package-boundaries.md)'s dependency direction: the
  application layer declares its ports and never imports MyGo or window code.
- **Settings migration.** `SettingsVersion` stays at 1 and there is no
  migration. New fields are additive and missing values load their defaults;
  the removed `globalHotkeyEnabled` key is ignored on decode and disappears on
  the next save. Other desktop settings are left untouched. The old enable
  checkbox is replaced by the four-mode choice, and Quake is the default.
- **macOS non-activating trade-off.** MyGo's non-activating `Panel` option
  gives the window the launcher-panel behavior this feature needs: above all
  apps, on whichever Space is current, over full-screen apps, and absent from
  the taskbar, Dock, and app switcher. On macOS, showing and focusing it gives
  it the keyboard without activating the app, and the previous application
  gets the keyboard back when the Panel hides. The trade-offs are that the app
  is not frontmost while the Panel is up, the previous application keeps the
  menu bar, and the Panel has no native window controls and cannot be
  minimized, maximized, or made full screen; its content draws a hide button
  in the dropdown frame. Windows and Linux may activate the app when the Panel
  is focused; the platform behavior is documented rather than promised to
  match macOS.
- **Capability and fallback.** `Capabilities.Dropdown` reports whether the
  platform supports the Panel. Where it is false, Settings keeps `none` and
  `toggle` and hides or disables `quake` and `topdown` with an explanation.
  Every platform still compiles.
- **Hide triggers.** The hotkey always toggles. Blur, Escape, and click-outside
  only hide. Blur and Escape have their own settings. Where the platform
  reports click-outside as focus loss, the gesture reuses the blur signal and
  therefore shares `HideOnBlur`; v1 installs no global mouse listener. This
  platform difference is documented in the settings copy.
- **Appearance.** The Panel follows the live theme, Interface Style, opacity,
  and vibrancy through the shared window enumeration; it asks the active style
  for its recipes as [ADR-0020](0020-interface-style.md) requires rather than
  baking in a look. The Quake route uses the terminal theme; Follow Mode and
  top-down use the shell theme. The Panel's content sits behind the shared App
  Lock gate, so locking replaces it with the lock screen and the Quake Sessions
  keep running but stay invisible.
