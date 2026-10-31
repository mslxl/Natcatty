package transfer

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// TestSessionPanelBrowsesTheSessionHost is the regression where a remote
// Session's panel listed this computer: the shell points the panel at the
// Host before its first frame, and the first frame used to open a local Tab
// over it.
func TestSessionPanelBrowsesTheSessionHost(t *testing.T) {
	local := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(local, "local-only.txt"), "local")
	writeFile(t, filepath.Join(remote, "remote-only.txt"), "remote")

	state := transferstate.NewState(newViewService(local, remote))
	t.Cleanup(state.Shutdown)
	state.ShowRemote(transferstate.Left, "h1", "Test")

	tester := ui.NewTester(SidePanel(state, 360), 360, 480)
	tester.SetLocale("en")
	tester.Frame()
	frameUntil(t, tester, "the remote listing", func() bool {
		tab := state.Tab(transferstate.Left)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	tester.Frame()

	tab := state.Tab(transferstate.Left)
	if tab.Kind != transferstate.KindRemote || tab.HostID != "h1" {
		t.Fatalf("the panel shows a %v Tab for %q, want the Session's Host", tab.Kind, tab.HostID)
	}
	if got := len(state.Pane(transferstate.Left).Tabs); got != 1 {
		t.Fatalf("the panel's pane has %d Tabs, want only the Host's", got)
	}
	if !tester.HasText("remote-only.txt") || tester.HasText("local-only.txt") {
		t.Fatalf("the panel does not list the Host's files: %v", tester.Texts())
	}
}
