package editor

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appfileopen "github.com/mslxl/natcatty/application/fileopen"
)

// promptGeometry asserts the prompt is a compact, centred confirmation inside
// the client area instead of the full editor frame the acceptance rejected.
func promptGeometry(t *testing.T, view func(*ui.Context), width, height int) ui.Rect {
	t.Helper()
	tester := ui.NewTester(view, width, height)
	tester.SetLocale("en")
	tester.Frame()
	rect, ok := tester.Find("editor-conflict-prompt")
	if !ok {
		return ui.Rect{}
	}
	if rect.W > float32(width)-8 || rect.H > float32(height)/2 {
		t.Fatalf("the conflict prompt is %vx%v at %dx%d, want a compact dialog inside the client",
			rect.W, rect.H, width, height)
	}
	if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(width) || rect.Y+rect.H > float32(height) {
		t.Fatalf("the conflict prompt %+v is outside the %dx%d client", rect, width, height)
	}
	centre := rect.X + rect.W/2
	if delta := centre - float32(width)/2; delta < -1 || delta > 1 {
		t.Fatalf("the conflict prompt centre is %.1f, want %.1f", centre, float32(width)/2)
	}
	return rect
}

func TestConflictPromptIsACompactCentredConfirmation(t *testing.T) {
	for _, size := range []struct {
		width, height int
	}{{1100, 720}, {720, 480}, {704, 421}} {
		service := &fakeService{conflictUntilOverwrite: true}
		state := NewState(service)
		openFixture(t, state, service, "notes.txt")
		state.Save()
		waitFor(t, "the conflict prompt", func() bool {
			state.Flush()
			return !state.Saving() && state.ConflictPending()
		})
		rect := promptGeometry(t, View(state), size.width, size.height)
		if rect.H == 0 {
			t.Fatalf("no conflict prompt at %dx%d", size.width, size.height)
		}
		t.Logf("conflict prompt at %dx%d: %.0fx%.0f at (%.0f,%.0f)",
			size.width, size.height, rect.W, rect.H, rect.X, rect.Y)
		// The actions are inside the same compact box, not stranded in an
		// empty editor frame.
		tester := ui.NewTester(View(state), size.width, size.height)
		tester.SetLocale("en")
		tester.Frame()
		cancel, ok := tester.Find("Cancel")
		if !ok {
			t.Fatalf("no Cancel at %dx%d", size.width, size.height)
		}
		if cancel.Y < rect.Y || cancel.Y+cancel.H > rect.Y+rect.H {
			t.Fatalf("the Cancel button %+v is outside the prompt %+v", cancel, rect)
		}
	}
}

func TestSaveFailurePromptIsCompactAndCentred(t *testing.T) {
	for _, size := range []struct {
		width, height int
	}{{1100, 720}, {720, 480}, {704, 421}} {
		service := &fakeService{}
		state := NewState(service)
		state.Notify(appfileopen.WatchEvent{
			Kind: appfileopen.WatchFailed, Session: "s1", Name: "notes.txt",
		})
		state.Flush()
		if !state.FailurePending() {
			t.Fatal("the watch failure did not open a prompt")
		}
		tester := ui.NewTester(View(state), size.width, size.height)
		tester.SetLocale("en")
		tester.Frame()
		rect, ok := tester.Find("editor-failure-prompt")
		if !ok {
			t.Fatalf("no failure prompt at %dx%d", size.width, size.height)
		}
		if rect.W > float32(size.width)-8 || rect.H > float32(size.height)/2 {
			t.Fatalf("the failure prompt is %vx%v at %dx%d, want a compact dialog inside the client",
				rect.W, rect.H, size.width, size.height)
		}
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.W > float32(size.width) ||
			rect.Y+rect.H > float32(size.height) {
			t.Fatalf("the failure prompt %+v is outside the %dx%d client", rect, size.width, size.height)
		}
		if delta := rect.X + rect.W/2 - float32(size.width)/2; delta < -1 || delta > 1 {
			t.Fatalf("the failure prompt centre is %.1f, want %.1f",
				rect.X+rect.W/2, float32(size.width)/2)
		}
		if retry, ok := tester.Find("Retry"); !ok || retry.Y < rect.Y || retry.Y+retry.H > rect.Y+rect.H {
			t.Fatalf("the Retry button %+v is outside the prompt %+v", retry, rect)
		}
	}
}
