package state

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSFTPIsAPeerRootSurface(t *testing.T) {
	view := func(c *ui.Context) { ui.Text(c, "SFTP surface") }
	state := NewState(WithSFTP(view))
	if state.SFTPVisible() || state.SFTPView() == nil {
		t.Fatal("the SFTP surface starts hidden but must have a view")
	}

	state.ShowSFTP()
	if !state.SFTPVisible() {
		t.Fatal("ShowSFTP did not show the SFTP surface")
	}
	if state.VaultVisible() || state.LogsVisible() {
		t.Fatal("showing SFTP left another root surface visible")
	}

	state.ShowVault()
	if state.SFTPVisible() {
		t.Fatal("returning to the Vault kept SFTP visible")
	}

	state.ShowSFTP()
	state.ShowLogs()
	if state.SFTPVisible() || !state.LogsVisible() {
		t.Fatalf("sftp=%v logs=%v after ShowLogs", state.SFTPVisible(), state.LogsVisible())
	}
}
