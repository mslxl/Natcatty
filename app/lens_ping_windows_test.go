//go:build windows

package app

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/application/lens/lens"
	appsession "github.com/mslxl/natcatty/application/session"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

func TestNatcattyWindowsPingBuildsNetPingLensInUI(t *testing.T) {
	pwsh, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("PowerShell is not installed")
	}
	if _, err := exec.LookPath("ping.exe"); err != nil {
		t.Skip("ping.exe is not installed")
	}
	enc := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	command := "ping -n 3 127.0.0.1"
	script := strings.Join([]string{
		"$e=[char]27;$b=[char]7",
		`Write-Host -NoNewline "$e]1337;NetcattyExec;powershell;` + enc(`C:\`) + `;` + enc(command) + `$b"`,
		`Write-Host -NoNewline "$e]133;C` + `$b"`,
		command,
		`$code=$LASTEXITCODE`,
		`Write-Host -NoNewline "$e]133;D;$code$b"`,
		"exit $code",
	}, "\n")
	path := filepath.Join(t.TempDir(), "natcatty-ping.ps1")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: []string{pwsh, "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path},
		Cols:    120,
		Rows:    40,
	})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := sessionui.New(session, sessionui.WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		blocks := surface.LensBlocks()
		if len(blocks) == 1 && blocks[0].LensID == "net.ping" && blocks[0].Native != nil {
			if blocks[0].Exited && countNatcattyBars(blocks[0].Native.(lens.Node)) >= 2 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	blocks := surface.LensBlocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].LensID != "net.ping" {
		t.Fatalf("lens = %q, want net.ping", blocks[0].LensID)
	}
	if blocks[0].Native == nil {
		t.Fatal("net.ping did not build a native view")
	}
	if bars := countNatcattyBars(blocks[0].Native.(lens.Node)); bars < 2 {
		t.Fatalf("net.ping bars = %d, want at least 2", bars)
	}
	tester := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 1000, 700)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText("127.0.0.1") {
		t.Fatalf("the UI did not render the ping lens body; block=%+v texts=%v", blocks[0], tester.Texts())
	}
}

func countNatcattyBars(n lens.Node) int {
	if n == nil {
		return 0
	}
	total := 0
	switch v := n.(type) {
	case lens.Bars:
		return len(v.Series)
	case lens.Column:
		for _, child := range v.Children {
			total += countNatcattyBars(child)
		}
	case lens.Row:
		for _, child := range v.Children {
			total += countNatcattyBars(child)
		}
	case lens.Card:
		for _, child := range v.Children {
			total += countNatcattyBars(child)
		}
	case lens.Section:
		for _, child := range v.Children {
			total += countNatcattyBars(child)
		}
	}
	return total
}
