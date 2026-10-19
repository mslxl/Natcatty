# App-owned transports with MyGo `Options.Conn`

Every session transport (local PTY, SSH, serial, Telnet, mosh, ET) is created and owned
by the application layer, and MyGo's terminal is always fed through
`terminal.Options.Conn` rather than `Options.Command`. This preserves the original
app's logging, OSC parsing, ZMODEM, activity, prompt interception, and observer
behavior, and avoids MyGo launching its own shell; the cost is that we implement and
maintain the PTY/process plumbing ourselves.

**Status**: accepted

**Considered Options**:
- `Options.Command`: less code, but MyGo owns the process, so raw output, startup
  gating, and transport-specific behavior become inaccessible.
- Embedding xterm.js in a WebView: better terminal API parity, but abandons the native
  UI and reintroduces a web runtime.

**Consequences**:
- `application/session` owns the bounded duplex `TerminalConn` (output, input, resize).
- Hibernate and observer views use `Snapshot()` and a shadow terminal; there is no live
  multi-view.
