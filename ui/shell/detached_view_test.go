package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/shell/state"
)

func newDetachedGraph(t *testing.T) (*state.State, *[]model.TabID, *[]model.TabID) {
	t.Helper()
	service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)
	detached := &[]model.TabID{}
	reattached := &[]model.TabID{}
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithDetachHandler(func(id model.TabID) {
			*detached = append(*detached, id)
		}),
		state.WithDetachReattachHandler(func(id model.TabID) {
			*reattached = append(*reattached, id)
		}),
	)
	return shellState, detached, reattached
}

func TestDetachedWindowRendersItsTabAndMovesItBack(t *testing.T) {
	shellState, detached, reattached := newDetachedGraph(t)
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	if !shellState.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	if len(*detached) != 1 {
		t.Fatalf("detached windows = %v, want one", *detached)
	}
	tabID := (*detached)[0]

	tester := ui.NewTester(DetachedView(shellState, tabID), 640, 480)
	tester.Frame()
	if !tester.HasText("terminal-surface") {
		t.Fatalf("detached window does not render its Session: %v", tester.Texts())
	}
	if !tester.HasText("Move to Main Window") {
		t.Fatalf("detached window has no move-to-main action: %v", tester.Texts())
	}
	if err := tester.Click("Move to Main Window"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if shellState.TabCount() != 1 {
		t.Fatalf("main tabs = %d after moving back, want 1", shellState.TabCount())
	}
	if len(*reattached) != 1 || (*reattached)[0] != tabID {
		t.Fatalf("reattached = %v, want %q", *reattached, tabID)
	}
	if shellState.DetachedTabExists(tabID) {
		t.Fatal("the detached Tab still exists after moving back")
	}
}

// assertInside fails when text is missing or its box leaves the window.
func assertInside(t *testing.T, tester *ui.Tester, width, height int, text string) ui.Rect {
	t.Helper()
	r, ok := tester.Find(text)
	if !ok {
		t.Fatalf("no element shows %q: %v", text, tester.Texts())
	}
	const epsilon = 0.5
	if r.X < -epsilon || r.Y < -epsilon ||
		r.X+r.W > float32(width)+epsilon || r.Y+r.H > float32(height)+epsilon {
		t.Fatalf("%q is clipped at %dx%d: box %+v", text, width, height, r)
	}
	return r
}

// assertSideBySide fails when two boxes on the same line overlap.
func assertSideBySide(t *testing.T, left, right ui.Rect) {
	t.Helper()
	if left.X < right.X+right.W && right.X < left.X+left.W &&
		left.Y < right.Y+right.H && right.Y < left.Y+left.H {
		t.Fatalf("boxes overlap: %+v and %+v", left, right)
	}
}

// TestDetachedWindowFitsAtDefaultAndMinimumSizes renders the detached window
// at the app's main-window default 1100x720 and at its own 480x320 minimum,
// asserting the header and its actions stay inside the window without
// overlapping.
func TestDetachedWindowFitsAtDefaultAndMinimumSizes(t *testing.T) {
	for _, size := range [][2]int{{1100, 720}, {480, 320}} {
		shellState, _, _ := newDetachedGraph(t)
		if err := shellState.NewTab(); err != nil {
			t.Fatal(err)
		}
		if !shellState.DetachTab(0) {
			t.Fatal("DetachTab returned false")
		}
		tabID := shellState.DetachedTabIDs()[0]

		tester := ui.NewTester(DetachedView(shellState, tabID), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		if !tester.HasText("terminal-surface") {
			t.Fatalf("%dx%d does not render the Session: %v", size[0], size[1], tester.Texts())
		}
		title := assertInside(t, tester, size[0], size[1], "Local Session 1")
		move := assertInside(t, tester, size[0], size[1], "Move to Main Window")
		closeWindow := assertInside(t, tester, size[0], size[1], "Close Window")
		assertSideBySide(t, title, move)
		assertSideBySide(t, move, closeWindow)
	}
}

// TestDetachedWindowHeaderShrinksALongTitle keeps a long Host name from
// pushing the header actions out of the minimum-size window.
func TestDetachedWindowHeaderShrinksALongTitle(t *testing.T) {
	shellState, _, _ := newDetachedGraph(t)
	label := "a very long production bastion host name that must not push the actions away"
	if err := shellState.OpenHost(domainvault.Host{Label: label}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 200 && shellState.TabCount() == 0; index++ {
		shellState.Flush()
		time.Sleep(time.Millisecond)
	}
	shellState.Flush()
	if shellState.TabCount() == 0 {
		t.Fatal("the Host Session did not open")
	}
	if !shellState.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	tabID := shellState.DetachedTabIDs()[0]

	tester := ui.NewTester(DetachedView(shellState, tabID), 480, 320)
	tester.SetLocale("en")
	tester.Frame()
	title := assertInside(t, tester, 480, 320, label)
	move := assertInside(t, tester, 480, 320, "Move to Main Window")
	closeWindow := assertInside(t, tester, 480, 320, "Close Window")
	assertSideBySide(t, title, move)
	assertSideBySide(t, move, closeWindow)
}

func TestDetachedWindowCloseActionAsksTheCompositionRoot(t *testing.T) {
	var requested []model.TabID
	service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithDetachedCloseRequest(func(id model.TabID) {
			requested = append(requested, id)
		}),
	)
	if err := shellState.NewTab(); err != nil {
		t.Fatal(err)
	}
	if !shellState.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	id := shellState.DetachedTabIDs()[0]

	tester := ui.NewTester(DetachedView(shellState, id), 640, 480)
	tester.Frame()
	if err := tester.Click("Close Window"); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || requested[0] != id {
		t.Fatalf("close requests = %v, want %q", requested, id)
	}
}
