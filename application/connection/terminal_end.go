package connection

// TerminalEndReason classifies why a terminal channel stopped. The values
// mirror the original exit vocabulary so reconnect policy can retry a
// transport drop but not a deliberate shell exit.
type TerminalEndReason string

const (
	// TerminalEndExited is a normal shell exit, including a non-zero status.
	TerminalEndExited TerminalEndReason = "exited"
	// TerminalEndSignaled is a server-side signal such as SIGHUP.
	TerminalEndSignaled TerminalEndReason = "signaled"
	// TerminalEndError is a transport failure or an exit status that never
	// arrived because the connection dropped.
	TerminalEndError TerminalEndReason = "error"
	// TerminalEndClosed is a clean channel close without an exit status.
	TerminalEndClosed TerminalEndReason = "closed"
	// TerminalEndTimeout is an administrative idle logout or connect timeout.
	TerminalEndTimeout TerminalEndReason = "timeout"
)

// TerminalEnd is one terminal's final state.
type TerminalEnd struct {
	Reason      TerminalEndReason
	ExitCode    int
	HasExitCode bool
}

// EndingTerminal is implemented by terminals that can classify their end. The
// adapter waits for the remote exit status once and caches the result.
type EndingTerminal interface {
	End() TerminalEnd
}
