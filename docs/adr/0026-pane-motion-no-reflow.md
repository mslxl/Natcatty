# Terminal motion freezes the grid and resizes once

Animating a Pane or a Tab body must not re-lay-out the terminal every frame.
When a motion event changes the layout, Natcatty computes the final geometry
and the final terminal grid once, gives every affected Pane its final grid, and
resizes its PTY once. The animation then moves that box, whose content stays at
the final grid. A Pane's content is never scaled: a shrinking Pane's final grid
is drawn at its own cell size and clipped by the moving box rather than
enlarged to the old, larger box, and a growing Pane is laid out at each
animated box, its pinned final grid drawn 1:1 and clipped, rather than
raster-scaled up from a smaller paint. The animation's last frame is the final
layout. The PTY sees one size change per affected Pane per event; the terminal
content is never re-laid-out per frame and never rewrapped while the animation
runs.

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
content once, to the final grid; the animation then moves the Pane's box around
that frozen content. During the animation there is no `GridFor` per frame and
no `Resize` per frame. A surviving Pane that grows uses MyGo's `Match`
transition with `ScaleContent` off: the Pane is laid out at each animated box,
and its pinned final grid draws at its own cell size and is clipped by that
box. `ScaleContent` would lay the Pane out once at the final size and
raster-scale that paint into the smaller, starting box, drawing its text and
header smaller than life before they grow back — the shrink defect mirrored on
the other side of the change. A surviving Pane that shrinks is drawn as an
opaque, window-level overlay: its copy is laid out at each animated box, so the
final grid draws at its own cell size and the moving box clips it. Scaling that
copy up to the old, larger box would make the text grow before it settles,
which is the same defect. An entering or exiting Pane is drawn the same way,
and the exit ghost may be a snapshot. The last frame is the final layout at the
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
- Freeze the final grid and animate the box around it (chosen): one resize, a
  stable PTY, and a final frame that is already correct. The growing Pane's box
  reveals more of its final grid as it opens and the shrinking Pane's box clips
  it, so the text keeps its size throughout; at 150–240 ms and with `popin`,
  `slide`, `gnomed` or `fade`, the clipped transient is the accepted trade.

**Consequences**:
- A Pane's content never changes size during the animation. A growing Pane's
  box reveals more of its final grid as it opens, and a shrinking Pane's box
  clips it; neither scales the text. The final frame is at the final grid and
  is crisp; the transient is short and is the price of not rewrapping. A user
  who prefers no geometry change at all can choose `fade`.
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
  switch, zoom, and the terminal Tab's side panel open and close. The side
  panel opens and closes as a drawer: its slot's size along the dock axis is
  animated, the divider moves with it, and the Panes take the room it leaves.
  The panel's content is laid out once at its final size and anchored to the
  dock's fixed edge, so the moving slot clips it instead of squeezing it. The
  Panes' final grids are pinned at the change; the drawer re-lays their boxes
  every frame without `GridFor` or `Resize`, and a closing panel keeps painting
  its last built frame until the slot reaches zero. It does not cover the Tab
  strip (no PTY; its own labels may reflow), window resize (no animation),
  divider drag (warp), or restore and first render (no animation).
- Reduce Motion or a disabled event warps to the final layout; the one PTY
  size change still happens if the size changed. A terminal surface that dies
  during the animation falls back to its snapshot, then to blank.
- The test invariant is the resize count: one PTY size change per affected
  Pane per event, never one per frame. `ui.Tester` frames should show the
  content laid out or clipped, never rewrapped or scaled, and the final frame
  should match the final grid. Any future animation that puts a terminal in a
  `Size` transition must follow this ADR.
