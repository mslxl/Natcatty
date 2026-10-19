//go:build selftest

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/ui/selftest"
)

// TestTicket14CheckRunsThroughTheKeyboard drives F11,1,4 from the initial
// Vault view through the production App Lock gate and the real desktop
// controller. Native ports that need an interactive desktop are faked at
// their seams; the tray, deep-link routing, notifications, hotkey, and
// autostart logic is the production code.
func TestTicket14CheckRunsThroughTheKeyboard(t *testing.T) {
	dir := t.TempDir()
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	graph := newWindowGraph(t, unlockedGate(t))
	repository := &testDesktopRepository{settings: appdesktop.Defaults("windows")}
	service := appdesktop.NewService(repository, &testHotkeys{},
		&testAutoLauncher{supported: true}, &testNotifier{}, appdesktop.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	controller := newDesktopController(graph.manager, graph.state, nil, nil, service,
		&fakeTrayBackend{}, func() string { return "en" }, func() {})
	t.Cleanup(controller.Shutdown)
	installSelftest(graph.manager, graph.state, controller, func([]string, string) {
		graph.manager.SecondInstance()
	})
	t.Cleanup(func() { activeSelftest = nil })

	graph.manager.OpenMain()
	window := graph.host.windows[0]
	if window == nil {
		t.Fatal("the main window was not created")
	}

	tester := ui.NewTester(window.spec.Content, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText("Vault") {
		t.Fatalf("the main window does not start on the Vault view: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	tester.Key(0, ui.Key1)
	tester.Frame()
	tester.Key(0, ui.Key4)
	tester.Frame()

	deadline := time.Now().Add(10 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		t.Fatalf("ticket 14 self-test failed: %+v", result)
	}
	if len(result.Steps) != 7 {
		t.Fatalf("steps = %d, want 7: %+v", len(result.Steps), result.Steps)
	}
	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{"trayBackend", "targetOpener", "desktop.Service ports",
		"desktop.Hotkeys", "desktop.AutoLauncher"} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-14.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  14") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}
