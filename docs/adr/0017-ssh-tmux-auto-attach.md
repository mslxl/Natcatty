# SSH + tmux auto-attach as a per-Host SSH modifier

SSH + tmux auto-attach is a formal, per-Host, opt-in capability. It is implemented as a modifier on the SSH transport, not as a new Host protocol: when enabled, the remote command creates or attaches a deterministic `natcatty-<sanitized-host-id>` tmux session, and reconnects reattach to it. It does not apply to mosh/ET Hosts by default.

**Status**: accepted

**Considered Options**:
- A first-class `SSH+tmux` Host protocol: makes the capability visible but duplicates SSH routing, authentication, and settings for no transport-level benefit.
- Automatic activation whenever tmux is detected: surprising and changes existing Host behavior without consent.
- A per-connection toggle only: forces users to opt in on every connection.
- A per-Host opt-in SSH modifier (chosen).

**Consequences**:
- Default off; each SSH Host has an `autoAttachTmux` setting. The deterministic name allows one named session per Host in v1; custom names, multiple sessions, a kill-session UI, and non-POSIX login shells are post-v1.
- Detaching closes the tab without error and leaves the remote session running; the next connect reattaches. Nested `$TMUX` environments are detected and not wrapped, with a notice.
- If tmux is missing or shell integration fails, the app falls back to a plain login shell on the same SSH connection and shows a one-time notice. It never installs tmux automatically and never fails the connection because of it.
- The capability works over plain SSH and is the persistence story of last resort. Mobile UX and its role in the deferred mobile fallback wait for the mobile gate.
