# Terminal motion freezes the grid and resizes once

Animating a Pane or a Tab body must not re-lay-out the terminal every frame.
When a motion event changes the layout, Natcatty computes the final geometry
and the final terminal grid once, gives every affected Pane its final grid, and
resizes its PTY once. The animation then moves and scales the frame already
painted at that grid — the surviving Pane natively with `Match + ScaleContent`,
the entering or exiting Pane as an opaque overlay — and its last frame is the
final layout. The PTY sees one size change per affected Pane per event; the
terminal text is transformed, not rewrapped, while the animation runs.

**Status**: accepted; implementation follows the Pane and Tab motion spec.

**Context**: Hyprland's `windowsMove` animates a window's position and size as
compositor-side geometry; it does not ask the application to lay out its
content at every intermediate size. MyGo's `ElementTransition` with `Size`
does ask for that: the content lays out at each size on the way, "as a panel
opening wraps its text anew" (`third_party/mygo/docs/ui/transitions.md`). For
a terminal that means a new grid and a new PTY size every frame. The Pane
viewport facts measured 20 resizes over 21 frames for a per-frame ratio change,
and the text rewrapped every frame. Natcatty is both the compositor and the
client, so it has to choose the client-side behavior explicitly.

**Decision**: A motion event is a layout change plus a visual transition, not
a sequence of layout changes. On the event, Natcatty records the old and new
rects, builds the final layout, computes each affected Pane's final grid, sets
that grid, and delivers one PTY size change. The terminal may reflow its
content once, to the final grid; the animation then transforms that painted
frame. During the animation there is no `GridFor` per frame and no `Resize` per
frame. The surviving Pane uses MyGo's `Match + ScaleContent` so its painted
content is scaled rather than re-laid-out; an entering or exiting Pane is drawn
as an opaque, window-level overlay (a live frame or a snapshot) and transformed
from its old rect to its new rect. The last frame is the final layout at the
final grid, so text is crisp at rest. A Pane whose size does not change gets no
resize; a closing Pane's PTY is already closed and its ghost is a frame, not a
live layout. Reduce Motion or a disabled event warps to the final layout; the
PTY still gets the one size change if the size changed.

**Considered Options**:
- Per-frame reflow: let MyGo's `Size` transition lay the terminal out at every
  intermediate size and resize the PTY as the grid changes. It is the least
  code, but it produces a PTY resize storm, rewraps the text every frame, and
  makes the animation unreadable. Rejected.
- Snapshot every frame: freeze a bitmap at the start and animate only that.
  It avoids reflow, but the content is stale for the whole animation and the
  bitmap is raster-scaled; it is suitable for a closing ghost, not for the
  surviving or entering Pane. Rejected as the general route; retained for the
  exit ghost.
- Delay the PTY resize until the animation ends: the PTY sees one size, but
  the terminal runs at the old size while the animation plays, and output
  produced during the animation wraps at the old width; the final resize can
  rewrap a burst at once. The visual grid and the PTY grid also disagree for
  the whole animation. Rejected.
- Freeze the final grid and transform the painted frame (chosen): one resize,
  a stable PTY, and a final frame that is already correct. The cost is that
  text may stretch or squash while it moves; at 150–240 ms and with `popin`,
  `slide`, `gnomed` or `fade`, that is the accepted trade.

**Consequences**:
- Text can stretch or squash during the animation. The final frame is at the
  final grid and is crisp; the transient is short and is the price of not
  rewrapping. A user who prefers no text distortion can choose `fade`, which
  leaves the geometry fixed.
- Output produced during the animation is not visible in the frozen frame; it
  is not lost, and the final frame shows the latest output. The PTY keeps
  running at the final size. Showing live output would require per-frame
  reflow, which this ADR rejects.
- The per-Pane viewport fix is a prerequisite. The final grid must be computed
  from the Pane's own box; the package-level `appview.availableWidth/Height`
  that siblings overwrite would make the "final" grid the sibling's. The
  implementation spec makes that fix its first ticket.
- The animation's from and to rects are recorded at the change, not read from
  `LayoutBounds()`, which is the previous frame's box; otherwise the first
  frame lags by one frame.
- The overlay must be opaque and window-level. The exiting Pane's ghost is a
  snapshot first, a live surface with delayed destruction second, and blank
  last. The entering Pane is an opaque overlay of its final-grid frame.
- The rule covers Pane open, close and reorder, Tab body open, close and
  switch, and zoom. It does not cover the Tab strip (no PTY; its own labels may
  reflow), window resize (no animation), divider drag (warp), or restore and
  first render (no animation).
- Reduce Motion or a disabled event warps to the final layout; the one PTY
  size change still happens if the size changed. A terminal surface that dies
  during the animation falls back to its snapshot, then to blank.
- The test invariant is the resize count: one PTY size change per affected
  Pane per event, never one per frame. `ui.Tester` frames should show the
  content transformed, not rewrapped, and the final frame should match the
  final grid. Any future animation that puts a terminal in a `Size` transition
  must follow this ADR.
