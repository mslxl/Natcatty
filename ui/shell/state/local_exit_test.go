package state

import (
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// A local shell that exits normally is the common way to leave a local
// terminal, so its Tab closes by default instead of keeping the ended screen.
func TestALocalShellExitClosesTheTabByDefault(t *testing.T) {
	state, factory := newEndingState(t, appsession.End{
		Reason: appsession.EndReasonExited, ExitCode: 0, HasExitCode: true,
	})
	if err := state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitFor(t, "the local Tab", func() bool {
		state.Flush()
		return state.TabCount() == 1
	})

	if err := factory.last().Close(); err != nil {
		t.Fatalf("Close transport: %v", err)
	}
	waitFor(t, "the exited local Tab to close", func() bool {
		state.Flush()
		return state.TabCount() == 0
	})
}

// Turning the setting off keeps the ended screen, matching how remote
// Sessions behave.
func TestTheLocalAutoCloseSettingCanKeepTheExitScreen(t *testing.T) {
	state, factory := newEndingState(t, appsession.End{
		Reason: appsession.EndReasonExited, ExitCode: 0, HasExitCode: true,
	}, WithLocalAutoCloseOnExit(func() bool { return false }))
	if err := state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitFor(t, "the local Tab", func() bool {
		state.Flush()
		return state.TabCount() == 1
	})

	if err := factory.last().Close(); err != nil {
		t.Fatalf("Close transport: %v", err)
	}
	waitFor(t, "the local Session to exit", func() bool {
		state.Flush()
		snapshot, ok := state.ActiveSnapshot()
		return ok && snapshot.Status == appsession.StatusExited
	})
	if state.TabCount() != 1 {
		t.Fatalf("tabs = %d after an exit, want the ended Tab", state.TabCount())
	}
}

// A failed local transport is not a normal exit: it keeps its error screen so
// the user can see what happened.
func TestAFailedLocalSessionKeepsItsExitScreen(t *testing.T) {
	state, factory := newEndingState(t, appsession.End{Reason: appsession.EndReasonError})
	if err := state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitFor(t, "the local Tab", func() bool {
		state.Flush()
		return state.TabCount() == 1
	})

	if err := factory.last().Close(); err != nil {
		t.Fatalf("Close transport: %v", err)
	}
	waitFor(t, "the failed local Session to exit", func() bool {
		state.Flush()
		snapshot, ok := state.ActiveSnapshot()
		return ok && snapshot.Status == appsession.StatusExited &&
			snapshot.EndReason == appsession.EndReasonError
	})
	if state.TabCount() != 1 {
		t.Fatalf("tabs = %d after a failed local Session, want the error screen", state.TabCount())
	}
}

// In a split, only the local Pane whose shell exited closes; the Tab and its
// other live Pane stay.
func TestALocalExitClosesOnlyItsPane(t *testing.T) {
	state, factory := newEndingState(t, appsession.End{
		Reason: appsession.EndReasonExited, ExitCode: 0, HasExitCode: true,
	})
	if err := state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if err := state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitFor(t, "the split Pane", func() bool {
		state.Flush()
		return len(panesOf(t, state, 0)) == 2
	})

	if err := factory.transportAt(0).Close(); err != nil {
		t.Fatalf("Close transport: %v", err)
	}
	waitFor(t, "the exited local Pane to close", func() bool {
		state.Flush()
		return len(panesOf(t, state, 0)) == 1
	})
	if state.TabCount() != 1 {
		t.Fatalf("tabs = %d, want the Tab with its live Pane", state.TabCount())
	}
}
