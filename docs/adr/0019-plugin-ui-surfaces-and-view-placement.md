# Plugin UI surfaces and view placement

Plugin UI is native-first with an optional sandboxed web surface, and a plugin view must
keep its logical state when the user moves it between placements. We define a native
`netcatty.ui` ABI over a retained node tree with atomic patch batches, plus a sandboxed
WebView for web-based plugins, and a view model with five placements. This keeps
host-owned focus, IME, accessibility, theming, and localization while letting one View
Instance survive a move; the cost is a large host-owned UI ABI and a strict WebView
sandbox. The ABI defines the full widget set now, including data grids, graphs, canvas,
rich text, virtual lists, custom fonts, animations, and an embedded WebView node.

**Status**: accepted

**Amends**: ADR-0006 (view tab clause), ADR-0007 (WebView boundary).

**Considered Options**:
- WebView-only plugin UI: one rendering model, but abandons native accessibility,
  theming, and performance, and makes every plugin a browser application.
- Native-only plugin UI: stronger consistency, but blocks web-based plugins and rich
  embedded content.
- Recreate the view on every placement move: simpler containers, but loses unsaved state
  and contradicts instance identity.

**Consequences**:
- The native ABI is a retained tree with atomic patch batches, host-owned layout,
  focus, IME, and accessibility, a closed widget set, and an SDK declarative layer.
  Text nodes carry strings the plugin has already resolved.
- Plugin WebViews use the sandbox from ADR-0007: per-plugin origin
  `natcatty-plugin://<plugin-id>/`, strict CSP, a verified bridge, and no access to the
  host browser session. The host-routed browser remains the only WebView outside that
  sandbox.
- Placements are `pane`, `tab`, `page`, `window`, and `aside`. Moving a view preserves
  the same View Instance and its logical state; `page` is singleton per view; view tabs
  share the work-tab strip under the `view:<instanceId>` namespace and are not editor
  tabs.
- Plugin strings resolve through the host i18n service at display time. The UI ABI gains
  logical `start`/`end` layout mirrored under RTL and a `locale_changed` event.
- The Home page, home sidebar, and side tool become contribution-driven registries; the
  main window's fixed root surface set is unchanged.
- System panel and System manager contributions are data-only typed descriptors rendered
  by the host inside one host-owned Extensions tab; the native UI ABI is reached only
  through `aside`, `page`, or `window` View Instances, never inside the built-in system
  surfaces.
