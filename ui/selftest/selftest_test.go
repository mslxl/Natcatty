package selftest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// install mirrors the composition root: Bind runs at the window's root
// scope, while View may sit inside a gate or another container.
func install(harness *Harness, content func(*ui.Context)) func(*ui.Context) {
	view := harness.View(content)
	return func(c *ui.Context) {
		harness.Bind(c)
		view(c)
	}
}

// TestDefaultResultDirIsPrivateToTheProcess is the ticket 61 isolation
// regression: the default result path must not be the shared system temp
// directory, so two check processes cannot overwrite each other's result.
func TestDefaultResultDirIsPrivateToTheProcess(t *testing.T) {
	dir := DefaultResultDir()
	if dir == os.TempDir() || dir == filepath.Clean(os.TempDir()) {
		t.Fatalf("DefaultResultDir = %q, which is the shared temp directory", dir)
	}
	if !strings.Contains(dir, strconv.Itoa(os.Getpid())) {
		t.Fatalf("DefaultResultDir = %q, want the process id %d", dir, os.Getpid())
	}
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Fatalf("DefaultResultDir = %q, want it under %q", dir, os.TempDir())
	}
}

// TestHarnessCreatesItsResultDirectory proves a missing result directory is
// created on demand instead of failing the check, which is what a per-process
// directory under %TEMP% needs on its first run.
func TestHarnessCreatesItsResultDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "result")
	harness := NewHarness(nil, WithResultDir(dir))
	harness.result = Result{Ticket: 61, Pass: true}
	harness.write()
	if !harness.result.Pass {
		t.Fatalf("write failed: %v", harness.result.Errors)
	}
	data, err := os.ReadFile(filepath.Join(dir, "natcatty-selftest-61.json"))
	if err != nil {
		t.Fatalf("read the written result: %v", err)
	}
	if !strings.Contains(string(data), `"pass": true`) {
		t.Fatalf("written result = %s", data)
	}
}

// TestF11ThenTicket13RunsTheCheck is the deterministic keyboard proof: it
// sends the real F11, 1, 3 key events through MyGo's shortcut registration and
// asserts that the overlay opened and the ticket 13 check ran. It needs no
// foreground window or OS modifier state.
func TestF11ThenTicket13RunsTheCheck(t *testing.T) {
	registry := NewRegistry()
	invoked := 0
	registry.Register(Check{
		Ticket: 13,
		Name:   "keyboard proof",
		Steps: []Step{{
			Name: "invoke",
			Run: func(*ui.Context) Outcome {
				invoked++
				return Passed("the check ran", "")
			},
		}},
	})

	dir := t.TempDir()
	stamp := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	harness := NewHarness(registry,
		WithResultDir(dir),
		WithClock(func() time.Time { return stamp }),
	)
	view := install(harness, func(c *ui.Context) { ui.Text(c, "content") })
	tester := ui.NewTester(view, 800, 600)
	tester.SetLocale("en")
	tester.Frame()

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !harness.Visible() || harness.State() != StateMenu {
		t.Fatalf("F11 did not open the self-test overlay: state = %v", harness.State())
	}
	if !tester.HasText("Self-test") || !tester.HasText("13  keyboard proof") {
		t.Fatalf("the overlay does not list the registered check: %v", tester.Texts())
	}

	tester.Key(0, ui.Key1)
	tester.Frame()
	if invoked != 0 {
		t.Fatalf("one digit ran the check early")
	}
	tester.Key(0, ui.Key3)
	tester.Frame()
	if invoked != 1 {
		t.Fatalf("F11 1 3 invoked the check %d times, want 1", invoked)
	}
	if harness.State() != StatePassed {
		t.Fatalf("state = %v after the check passed, want StatePassed", harness.State())
	}
	if !tester.HasText("PASS  13  keyboard proof") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}

	result, ok := harness.LastResult()
	if !ok || !result.Pass || result.Ticket != 13 {
		t.Fatalf("LastResult = %+v, %v; want a passing ticket 13", result, ok)
	}
	if len(result.Steps) != 1 || result.Steps[0].Name != "invoke" || !result.Steps[0].Pass {
		t.Fatalf("result steps = %+v, want the invoked step", result.Steps)
	}
	if result.Timestamp != stamp.Format(time.RFC3339Nano) {
		t.Fatalf("timestamp = %q, want the injected clock", result.Timestamp)
	}

	// The documented machine-readable result is on disk.
	path := filepath.Join(dir, "natcatty-selftest-13.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var written Result
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, data)
	}
	if !written.Pass || written.Ticket != 13 || len(written.Steps) != 1 {
		t.Fatalf("written result = %+v", written)
	}
	if written.Errors == nil {
		t.Fatal("written result has no errors array")
	}
	if written.Timestamp == "" {
		t.Fatal("written result has no timestamp")
	}
}

// TestOverlayFitsAtDefaultAndMinimumSizes renders the menu and the finished
// result at the main window's default 1100x720 and minimum 720x480 sizes. Every
// line must stay inside the window, and the overlay's lines must not overlap.
func TestOverlayFitsAtDefaultAndMinimumSizes(t *testing.T) {
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		registry := NewRegistry()
		registry.Register(Check{
			Ticket: 13,
			Name:   "Detached windows and single-instance behavior",
			Steps: []Step{
				{Name: "create-session", Run: func(*ui.Context) Outcome {
					return Passed("a real local shell Session is running", "")
				}},
				{Name: "detach-tab", Run: func(*ui.Context) Outcome {
					return Passed("the Session kept running in a real detached window", "")
				}},
				{Name: "copy-tab-to-window", Run: func(*ui.Context) Outcome {
					return Passed("the copy opened fresh Sessions", "")
				}},
				{Name: "second-instance-focus", Run: func(*ui.Context) Outcome {
					return Passed("the single-instance callback raised the existing main window", "")
				}},
				{Name: "close-intent", Run: func(*ui.Context) Outcome {
					return Passed("deny kept the window open and allow proceeded", "dialogConfirmer")
				}},
				{Name: "app-lock-detached", Run: func(*ui.Context) Outcome {
					return Passed("the shared gate showed only the lock screen", "uiapplock.Gate")
				}},
				{Name: "close-detached-windows", Run: func(*ui.Context) Outcome {
					return Passed("the real window close hook removed both detached Tabs", "")
				}},
			},
		})
		harness := NewHarness(registry, WithResultDir(t.TempDir()))
		tester := ui.NewTester(install(harness, func(c *ui.Context) { ui.Text(c, "content") }), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		tester.Key(0, ui.KeyF11)
		tester.Frame()
		assertOverlayFits(t, tester, size[0], size[1])
		if !tester.HasText("Self-test") || !tester.HasText("13  Detached windows") {
			t.Fatalf("%dx%d menu does not list the check: %v", size[0], size[1], tester.Texts())
		}

		tester.Key(0, ui.Key1)
		tester.Frame()
		tester.Key(0, ui.Key3)
		tester.Frame()
		if harness.State() != StatePassed {
			t.Fatalf("%dx%d state = %v, want StatePassed", size[0], size[1], harness.State())
		}
		assertOverlayFits(t, tester, size[0], size[1])
		if !tester.HasText("PASS  13") {
			t.Fatalf("%dx%d does not show PASS: %v", size[0], size[1], tester.Texts())
		}
		if !tester.HasText("Result: ") {
			t.Fatalf("%dx%d does not show the result path: %v", size[0], size[1], tester.Texts())
		}
	}
}

// assertOverlayFits checks every overlay line against the window bounds and
// the line above it. Content drawn under the overlay is ignored.
func assertOverlayFits(t *testing.T, tester *ui.Tester, width, height int) {
	t.Helper()
	const epsilon = 0.5
	var previous ui.Rect
	for _, text := range tester.Texts() {
		// The backdrop's accessibility label spans the window on purpose.
		if text == "content" || text == "Self-test overlay" {
			continue
		}
		r, ok := tester.Find(text)
		if !ok {
			continue
		}
		if r.X < -epsilon || r.Y < -epsilon ||
			r.X+r.W > float32(width)+epsilon || r.Y+r.H > float32(height)+epsilon {
			t.Fatalf("%q is clipped at %dx%d: box %+v", text, width, height, r)
		}
		if previous.H > 0 && !strings.HasPrefix(text, "Ticket:") &&
			r.Y < previous.Y+previous.H-epsilon {
			t.Fatalf("overlay lines overlap at %dx%d: %+v over %+v (%q)",
				width, height, r, previous, text)
		}
		previous = r
	}
}

func TestEscapeCancelsTheOverlay(t *testing.T) {
	registry := NewRegistry()
	registry.Register(Check{Ticket: 13, Name: "unused", Steps: []Step{{
		Name: "never",
		Run: func(*ui.Context) Outcome {
			t.Fatal("Escape did not cancel before the check ran")
			return Passed("", "")
		},
	}}})
	harness := NewHarness(registry, WithResultDir(t.TempDir()))
	tester := ui.NewTester(install(harness, nil), 640, 480)
	tester.SetLocale("en")
	tester.Frame()

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if harness.Visible() {
		t.Fatalf("Escape left the overlay visible")
	}
}

// TestOverlayOpensThroughGateAndConsumesTypedDigits proves the shortcut is a
// window shortcut even when the view is built inside a container (as the App
// Lock gate wraps it), and that the overlay takes the keyboard so the digits
// that select a check never reach the terminal behind it.
func TestOverlayOpensThroughGateAndConsumesTypedDigits(t *testing.T) {
	registry := NewRegistry()
	invoked := 0
	registry.Register(Check{Ticket: 13, Name: "gate proof", Steps: []Step{{
		Name: "invoke",
		Run: func(*ui.Context) Outcome {
			invoked++
			return Passed("ran", "")
		},
	}}})
	harness := NewHarness(registry, WithResultDir(t.TempDir()))

	var typed []string
	content := func(c *ui.Context) {
		// A focused terminal stand-in: it would receive any text the overlay
		// failed to take.
		ui.Column(c).Fill().Children(func() {
			ui.Box(c.Key("terminal")).Size(600, 400).Focusable().AutoFocus().
				HandleInput(func(event ui.InputEvent) bool {
					if event.Kind == ui.InputText {
						typed = append(typed, event.Text)
						return true
					}
					return false
				})
		})
	}
	tester := ui.NewTester(install(harness, content), 800, 600)
	tester.SetLocale("en")
	tester.Frame()

	// Control: text typed before the overlay reaches the focused terminal.
	tester.Type("x")
	tester.Frame()
	if len(typed) != 1 || typed[0] != "x" {
		t.Fatalf("the focused terminal received %q before the overlay", typed)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if harness.State() != StateMenu {
		t.Fatalf("F11 inside a container did not open the overlay: %v", harness.State())
	}
	typed = nil
	tester.TypeKey(0, ui.Key1, "1")
	tester.Frame()
	tester.TypeKey(0, ui.Key3, "3")
	tester.Frame()
	if invoked != 1 {
		t.Fatalf("F11 1 3 invoked the check %d times, want 1", invoked)
	}
	if len(typed) != 0 {
		t.Fatalf("the overlay leaked %q into the focused terminal", typed)
	}
	tester.Type("13")
	tester.Frame()
	if len(typed) != 0 {
		t.Fatalf("the overlay leaked raw text %q into the focused terminal", typed)
	}
}
