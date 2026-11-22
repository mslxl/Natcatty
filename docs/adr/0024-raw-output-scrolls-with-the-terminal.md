# Raw output scrolls with the terminal

A block's raw (non-native) body, and the output of a command no lens claimed,
are flow content like a lens view: every row takes its natural height, the
transcript's one scroll reaches all of them, and nothing inside a block scrolls,
pans or clips. The rule is one scroll per transcript, whatever draws the rows.
ADR-0022 set it for lens views and left raw output out; this ADR lifts that
exception at the user's direction.

`ui/terminal/blockview` owns this. `renderRaw` and `renderCell` draw one
selectable `ui.Column` of terminal-font rows on the grid's own row pitch, with
no `ui.Scroll`, no fixed-height box and no `Clip`. The command's output limit
still bounds what a block builds: `maxRawLines` (500) and Terminal's "N lines
over this command's output limit · click for the whole output" notice, whose
click lifts the cap. A long output therefore grows the transcript, and the
flow's scroll — the wheel, PageDown, Home/End and the one scrollbar — reaches
its last line.

Terminal itself keeps a capped `.sf-list-scroll.max` viewport inside a raw
block. This app deliberately departs from that: a second scrollbar inside a
transcript is the failure ADR-0022 removed from lens widgets, and it applies to
raw output just the same.

**Status**: accepted and implemented.

**Considered Options**:
- Keep Terminal's 260 DIP raw viewport: the block stays short, but its output is
  reachable only through a second scrollbar — the behaviour the user ruled out.
- Cap the visible rows with a "show all" control of the widget's own: a second
  expansion model beside the existing output-limit notice, which already
  provides the way to lift the cap.
- Draw all rows in the flow (chosen): the transcript has one scroll, a block's
  height follows its output, and the body stays one selectable document whose
  selection and context menu work across every row.

**Consequences**:
- A raw block can be as tall as its output limit allows (500 lines before the
  user lifts it). The output limit, not a viewport, is what keeps a block from
  growing without bound.
- A line too wide for the pane wraps, as every other flow row does; it does not
  pan.
- The raw body no longer has an inner edge of its own, so a block's card has no
  scrollbar to align to; the flow's bar is the only one.
- A list or other lens widget that wants a shorter transcript still shortens
  itself with `Clamp`/`Overflow`, as ADR-0022 requires, rather than with a
  scroll.
