//go:build windows

package app

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/adapters/shellhook"
	appsession "github.com/mslxl/natcatty/application/session"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

func TestNatcattyWindowsNestedPowerShellLens(t *testing.T) {
	surface, session := openNestedWindowsSurface(t)
	send := nestedSend(t, session)
	waitScreenText(t, surface, "PS ")

	send("pwsh")
	waitNestedBlocks(t, surface, 1)
	send("ls")
	waitNestedBlocks(t, surface, 2, "", "files.ls")
	waitNestedNative(t, surface, 1)
	blocks := surface.LensBlocks()
	if blocks[1].Native == nil {
		t.Fatalf("child pwsh ls did not build a native Lens: %+v", blocks[1])
	}

	send("exit")
	waitNestedBlocks(t, surface, 3)
	send("ls")
	waitNestedBlocks(t, surface, 4, "", "files.ls", "", "files.ls")
	waitNestedNative(t, surface, 3)
	blocks = surface.LensBlocks()
	if blocks[3].Native == nil {
		t.Fatalf("parent pwsh ls did not build a native Lens: %+v", blocks[3])
	}
}

func TestNatcattyWindowsNestedShLens(t *testing.T) {
	if _, err := exec.LookPath("sh.exe"); err != nil {
		t.Skip("sh.exe is not installed")
	}
	surface, session := openNestedWindowsSurface(t)
	send := nestedSend(t, session)
	waitScreenText(t, surface, "PS ")

	send("sh")
	waitNestedBlocks(t, surface, 1)
	send("ls")
	waitNestedBlocks(t, surface, 2, "", "files.ls")
	waitNestedNative(t, surface, 1)
	if surface.LensBlocks()[1].Native == nil {
		t.Fatalf("sh ls did not build a native Lens: %+v", surface.LensBlocks()[1])
	}
}

func TestNatcattyWindowsNestedShWithinShLens(t *testing.T) {
	if _, err := exec.LookPath("sh.exe"); err != nil {
		t.Skip("sh.exe is not installed")
	}
	surface, session := openNestedWindowsSurface(t)
	send := nestedSend(t, session)
	waitScreenText(t, surface, "PS ")

	send("sh")
	waitNestedBlocks(t, surface, 1)
	send("sh")
	waitNestedBlocks(t, surface, 2, "", "")
	send("ls")
	waitNestedBlocks(t, surface, 3, "", "", "files.ls")
	waitNestedNative(t, surface, 2)
}

func TestNatcattyWindowsCmdToPowerShellLens(t *testing.T) {
	surface, session := openNestedWindowsSurface(t)
	send := nestedSend(t, session)
	waitScreenText(t, surface, "PS ")

	send("cmd")
	time.Sleep(500 * time.Millisecond)
	send("pwsh")
	waitNestedBlocks(t, surface, 1)
	send("ls")
	waitNestedBlocks(t, surface, 2, "", "files.ls")
	waitNestedNative(t, surface, 1)
	if surface.LensBlocks()[1].Native == nil {
		t.Fatalf("cmd -> pwsh ls did not build a native Lens: %+v", surface.LensBlocks()[1])
	}
}

func TestNatcattyWindowsCmdPowerShellShLens(t *testing.T) {
	if _, err := exec.LookPath("sh.exe"); err != nil {
		t.Skip("sh.exe is not installed")
	}
	surface, session := openNestedWindowsSurface(t)
	send := nestedSend(t, session)
	waitScreenText(t, surface, "PS ")

	send("cmd")
	time.Sleep(500 * time.Millisecond)
	send("pwsh")
	waitNestedBlocks(t, surface, 1)
	send("sh")
	waitNestedBlocks(t, surface, 2, "", "")
	send("ls")
	waitNestedBlocks(t, surface, 3, "", "", "files.ls")
	waitNestedNative(t, surface, 2)
}

func openNestedWindowsSurface(t *testing.T) (*sessionui.Surface, *appsession.Session) {
	t.Helper()
	service := appsession.NewService(
		localpty.Factory{},
		appsession.WithCaptureHook(shellhook.NewPlanner()),
	)
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
	tester.Frame()
	return surface, session
}

func nestedSend(t *testing.T, session *appsession.Session) func(string) {
	t.Helper()
	return func(command string) {
		t.Helper()
		if _, err := session.TerminalConn().Write([]byte(command + "\r")); err != nil {
			t.Fatal(err)
		}
	}
}

func waitNestedBlocks(t *testing.T, surface *sessionui.Surface, count int, lensIDs ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		blocks := surface.LensBlocks()
		if len(blocks) >= count {
			ok := true
			for index, want := range lensIDs {
				if blocks[index].LensID != want {
					ok = false
					break
				}
			}
			if ok {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for blocks=%d lens=%v; got %+v screen=%q", count, lensIDs, surface.LensBlocks(), surface.ScreenText())
}

func waitNestedNative(t *testing.T, surface *sessionui.Surface, index int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		blocks := surface.LensBlocks()
		if index < len(blocks) && blocks[index].Native != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for native Lens at %d; got %+v", index, surface.LensBlocks())
}

func waitScreenText(t *testing.T, surface *sessionui.Surface, needle string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(surface.ScreenText(), needle) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; screen=%q", needle, surface.ScreenText())
}
