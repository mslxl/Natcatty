//go:build windows

package app

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// TestNatcattyWindowsLocalPowerShellLSBuildsFilesLens covers the actual Local
// Terminal default-shell path, without the optional capture hook. The adapter
// must still install the built-in PowerShell integration so a plain `ls` has a
// command boundary and renders its files.ls view.
func TestNatcattyWindowsLocalPowerShellLSBuildsFilesLens(t *testing.T) {
	if _, err := exec.LookPath("pwsh.exe"); err != nil {
		t.Skip("PowerShell 7 is not installed")
	}
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindLocal,
		Cols: 120,
		Rows: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := sessionui.New(session,
		sessionui.WithRedraw(func() {}),
		sessionui.WithRawTerminal(func() bool { return false }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) {
		surface.ViewFocused(c, true)
	}, 1200, 800)
	tester.SetLocale("en")
	tester.Frame()
	time.Sleep(2 * time.Second)
	if _, err := session.TerminalConn().Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		blocks := surface.LensBlocks()
		if len(blocks) == 1 && blocks[0].Exited {
			tester.Frame()
			if blocks[0].LensID != "files.ls" {
				t.Fatalf("lens = %q, want files.ls; block=%+v", blocks[0].LensID, blocks[0])
			}
			if blocks[0].Native == nil {
				t.Fatal("files.ls did not build a native view")
			}
			if !tester.HasText("LastWriteTime") || !tester.HasText("Name") {
				t.Fatalf("files.ls block was not rendered: texts=%v", tester.Texts())
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for files.ls: blocks=%+v", surface.LensBlocks())
}
