# Lens views scroll with the terminal

A lens's native view is flow content, not a viewport. It has no scroll container
of its own, no scrollbar, and no focus that would scroll it: every row of a
list, every row of an ANSI screen, every code line and every diff line takes its
natural height, and the terminal flow's one scroll reaches all of them. When a
lens wants to shorten long content, it says so with its own `Clamp` ("N more
lines · Show all lines") or `Overflow` node, which names the omitted rows and
can be lifted; a hidden scroll box that silently keeps rows out of reach is not
an option. A line or a cell too wide for the view wraps; it does not pan.

`ui/terminal/lensview` owns this. `renderList` builds the rows into a
`ui.Column` instead of a `ui.List` (MyGo's list virtualises its rows inside a
scroll container, which used to be capped at 240 DIP here), reusing the shared
row-selection store that table rows already use: a click chooses a row, a
second click on the chosen row activates it. `renderANSI` draws the whole
mini-terminal instead of capping it at 300 DIP behind a `ui.Scroll`. The code
node's editor is built with `editor.State{FitContent: true, SoftWrap: true}`:
it lays out as high as its lines (wrapped rows included), wraps a long line
instead of keeping it behind a sideways scroll, has nothing to scroll so it
paints no bars and lets the wheel through to the flow. Its body takes the
height the editor asks for, not one counted from line breaks, so nothing is
clipped. The editor's own gutter numbers its lines and follows the rows a
wrapped line takes. A snippet that numbers from a line other than 1
(`code(text, lang, start)`) keeps that editor and draws its numbers beside it,
one box per line as high as the editor laid the line's wrapped rows, because
the editor counts from 1 and offers no offset; folding is off on such a
snippet so the numbers still match the lines shown.

Markdown documents are rendered with `markdown.Options{WrapCode: true}`, so a
fenced block's long lines wrap with the document. MyGo builds every Markdown
table and display-math block inside a `ui.ScrollHorizontal` and offers no option
to change that, so `markdown.Options.Block` takes those two block kinds over:
`renderMarkdownTable` draws a header over a hairline and rows of cells that wrap
at column widths fitted to the view, keeping emphasis, code, links, alignment
and math in the cells; a math block draws its TeX wrapped and centred as muted
monospace. The remaining blocks render as MyGo renders them.

**Status**: accepted and implemented.

**Considered Options**:
- Keep MyGo's `ui.List` and hide its bars with `Scrollbars(false)`: the bars go,
  but the list stays a scroll region — it virtualises rows, consumes the wheel
  over itself, takes keyboard focus, and its rows are still bounded by the
  element's height. The transcript still has two scrolls; only one of them is
  invisible.
- Wrap each widget in a fixed-height box and clip it: no bar, but the rest of
  the content is unreachable. This is the failure a capped Markdown document or
  diff had before their caps were lifted.
- Give each widget its own "show all" control: workable, but it invents a
  second expansion model beside the nodes' own Clamp and Overflow, and a list
  with a few hundred rows would open collapsed by default.
- Leave a wide Markdown table to MyGo's horizontal scroll: the rest of the row
  is reachable, but the document gains a second scroll to learn, and on a
  transcript of many blocks it is never clear which one the wheel or Shift-wheel
  will move. Wrapping keeps the columns' meaning and the flow's one scroll.
- Render every node into the flow and keep one scroll (chosen): the wheel,
  PageDown, Home/End and the scrollbar all mean the same thing everywhere in
  the transcript, and the end of any widget is simply the next row.

**Consequences**:
- Lists and ANSI screens are no longer virtualised. A list of ten thousand rows
  builds ten thousand elements; the nodes that can produce such a list are
  expected to cap themselves with `Clamp`/`Overflow` first. The command's own
  output limit and its "click for the whole output" notice still bound what a
  block carries. The code node keeps its editor, which still paints only the
  lines in view, but it must measure and wrap every line to fit its content.
- A Markdown table wider than the view wraps rather than pans: its columns
  share the view's width in proportion to their content, cell padding
  included. A table of many columns in a narrow view therefore leaves only a
  few characters per cell and becomes tall — at a phone-width pane, seven
  columns wrap "epsilon" over three lines. That is the same trade the rest of the
  flow makes: it stays readable and scrollable, where panning hid the rest of
  the row behind a second scroll.
- A snippet numbered from a line other than 1 measures its numbers from the
  editor's last layout: in the frame that first shows it, or right after a
  resize, the numbers are one row a line and settle onto the wrapped rows on
  the next frame. Callers that want the numbers aligned give the tree a stable
  scope with `RenderInto`/`RenderScoped`, as blocks do; the unscoped `Render`
  gives its tree a new scope every frame, so it never carries the layout and
  its numbers stay one a line.
- Arrow keys, PageDown and the wheel over a list move the terminal, not a
  selection inside the widget. Selection is pointer-driven (click, then a
  second click to activate), matching tables; the lens still receives the same
  `select`, `activate` and `change` events.
- A list's filter box stays, but it is the only focusable part of the list.
- The raw (non-native) body of a block is unchanged: Terminal's capped
  `.sf-list-scroll.max` viewport and the "N lines over this command's output
  limit" notice are terminal output, not a lens widget, and the notice can lift
  the cap.
- New node kinds added to `lens.Node` must follow the same rule: no nested
  `ui.Scroll`, no `ui.List`, no editor bars, and no `ui.ScrollHorizontal`
  (ADR-0022 is the check for review). Markdown content that MyGo would put in
  one is taken over through `Options.Block` or `Options.WrapCode`.
