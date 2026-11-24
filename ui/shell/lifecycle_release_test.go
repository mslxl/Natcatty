package shell

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// releaseTrackingSurface is the hidden-Tab seam under test: the shell must ask
// it to let go of guest input when another Tab becomes visible.
type releaseTrackingSurface struct {
	name string
	mu   sync.Mutex
	held int
}

func (s *releaseTrackingSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, s.name).Fill()
}

func (s *releaseTrackingSurface) Close() {}

func (s *releaseTrackingSurface) ReleaseInput() {
	s.mu.Lock()
	s.held++
	s.mu.Unlock()
}

func (s *releaseTrackingSurface) releases() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

func (s *releaseTrackingSurface) reset() {
	s.mu.Lock()
	s.held = 0
	s.mu.Unlock()
}

// TestSwitchingTabsReleasesTheHiddenPanesInput drives the real shell frame and
// clicks another Tab, proving the shell does not rely on the hidden Surface's
// View being built to release a held key.
func TestSwitchingTabsReleasesTheHiddenPanesInput(t *testing.T) {
	service := appsession.NewService(realSurfaceFactory{})
	t.Cleanup(service.CloseAll)

	var mu sync.Mutex
	var surfaces []*releaseTrackingSurface
	st := state.NewState(state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
		mu.Lock()
		defer mu.Unlock()
		surface := &releaseTrackingSurface{name: fmt.Sprintf("surface-%d", len(surfaces)+1)}
		surfaces = append(surfaces, surface)
		return surface, nil
	}))
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st.Flush()
		mu.Lock()
		opened := len(surfaces)
		mu.Unlock()
		if opened == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d Pane surfaces opened", opened)
		}
		time.Sleep(time.Millisecond)
	}
	st.RenameTab(0, "First desk")
	st.RenameTab(1, "Second desk")

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	outgoing, ok := st.ActiveSurface().(*releaseTrackingSurface)
	if !ok || outgoing == nil {
		t.Fatal("the active Tab has no tracking surface")
	}
	if err := tester.Click("First desk"); err != nil {
		t.Fatalf("click the other Tab: %v", err)
	}
	if st.ActiveTab() != 0 {
		t.Fatalf("active Tab = %d, want 0", st.ActiveTab())
	}
	if got := outgoing.releases(); got != 1 {
		t.Fatalf("hidden Surface releases = %d, want 1", got)
	}
}

// TestSwitchingTabsReleasesEveryPaneOfASplitTab drives a real split through
// the shell frame: a pointer button can be down in the non-focused member of
// the split, so switching Tabs must release every Pane, not just the focused
// one.
func TestSwitchingTabsReleasesEveryPaneOfASplitTab(t *testing.T) {
	service := appsession.NewService(realSurfaceFactory{})
	t.Cleanup(service.CloseAll)

	var mu sync.Mutex
	var surfaces []*releaseTrackingSurface
	st := state.NewState(state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
		mu.Lock()
		defer mu.Unlock()
		surface := &releaseTrackingSurface{name: fmt.Sprintf("surface-%d", len(surfaces)+1)}
		surfaces = append(surfaces, surface)
		return surface, nil
	}))
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurfaces := func(want int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			st.Flush()
			mu.Lock()
			opened := len(surfaces)
			mu.Unlock()
			if opened == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d Pane surfaces opened, want %d", opened, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitForSurfaces(2)

	// Split the first Tab, then make it the visible one again.
	st.SelectTab(0)
	if err := st.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForSurfaces(3)
	st.RenameTab(0, "First desk")
	st.RenameTab(1, "Second desk")

	root, _, ok := st.TabTree(0)
	if !ok {
		t.Fatal("the first Tab has no tree")
	}
	ids := domainworkspace.CollectPaneIDs(root)
	if len(ids) != 2 {
		t.Fatalf("first Tab panes = %v, want a split", ids)
	}
	outgoing := make([]*releaseTrackingSurface, 0, len(ids))
	for _, id := range ids {
		info, ok := st.PaneInfoAt(0, id)
		if !ok || info.Surface == nil {
			t.Fatalf("Pane %q has no Surface", id)
		}
		surface, ok := info.Surface.(*releaseTrackingSurface)
		if !ok {
			t.Fatalf("Pane %q surface is %T, want the tracking surface", id, info.Surface)
		}
		outgoing = append(outgoing, surface)
	}
	incomingInfo, ok := st.PaneInfoAt(1, mustPaneID(t, st, 1))
	if !ok || incomingInfo.Surface == nil {
		t.Fatal("the second Tab has no Surface")
	}
	incoming, ok := incomingInfo.Surface.(*releaseTrackingSurface)
	if !ok {
		t.Fatalf("incoming surface is %T, want the tracking surface", incomingInfo.Surface)
	}

	mu.Lock()
	for _, surface := range surfaces {
		surface.reset()
	}
	mu.Unlock()

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click("Second desk"); err != nil {
		t.Fatalf("click the other Tab: %v", err)
	}
	if st.ActiveTab() != 1 {
		t.Fatalf("active Tab = %d, want 1", st.ActiveTab())
	}
	for index, surface := range outgoing {
		if got := surface.releases(); got != 1 {
			t.Fatalf("hidden split Pane %d releases = %d, want 1", index, got)
		}
	}
	if got := incoming.releases(); got != 0 {
		t.Fatalf("incoming Surface releases = %d, want 0", got)
	}
}

func mustPaneID(t *testing.T, st *state.State, tabIndex int) model.PaneID {
	t.Helper()
	root, _, ok := st.TabTree(tabIndex)
	if !ok {
		t.Fatalf("Tab %d does not exist", tabIndex)
	}
	ids := domainworkspace.CollectPaneIDs(root)
	if len(ids) == 0 {
		t.Fatalf("Tab %d has no Panes", tabIndex)
	}
	return ids[0]
}
