# A raw view carries no block chrome

A block switched to Raw, and a command no lens claimed, show the program's
output and nothing of the block's own: no rail down the left edge and no foot
badges (exit status, how long the command took, progress, status text, a lens's
error). Raw is what the program printed, the way `ls` with no lens shows it; the
native view is where the block's presentation, and its metadata, belong. The
hover bar stays — faded out until the pointer is over the block — because it is
the only way back to Native; the raw body's "N lines over this command's output
limit · click for the whole output" notice stays too, because it names output
that is being withheld rather than adding metadata of its own.

**Status**: accepted and implemented.

**Considered Options**:
- Keep Terminal's `.sf-block` chrome in both views: consistent with the
  reference app, but it puts "exit 0" and "took 12ms" after the output, so a raw
  block reads as output with lines appended that the program never printed — the
  user ruled this out.
- Hide the hover bar as well: nothing would be left of the block, but then a
  raw block could never be switched back to Native; the bar draws nothing while
  the pointer is elsewhere.
- Hide only the foot and keep the rail: still marks the block, but a rail is
  decoration of the block, not of the output, and raw is to look like a command
  no lens claimed.

**Consequences**:
- A failed command in Raw is not marked in red; its exit status is read in
  Native (the flow still shows the prompt and output like any cell block).
- Turning `command_lenses` off draws every lens-claimed block in Raw, so the
  transcript then looks entirely like plain terminal output.
- New block chrome must be drawn only outside the Raw branch of `renderBlock`;
  the test `TestRawViewEndsWithTheOutputOnly` guards the foot.
