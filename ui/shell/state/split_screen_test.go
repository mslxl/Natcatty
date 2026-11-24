package state

import (
	"errors"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// splitSurface is a Pane Surface that speaks the screen-splitting seam.
type splitSurface struct {
	fakeSurface
	canSplit   bool
	screens    int
	prepared   []int
	prepareErr error
}

func (s *splitSurface) CanSplit() bool   { return s.canSplit }
func (s *splitSurface) ScreenCount() int { return s.screens }
func (s *splitSurface) PrepareSplit(count int) error {
	s.prepared = append(s.prepared, count)
	return s.prepareErr
}

func vncPane(id model.PaneID, screen int, surface Surface) *pane {
	return &pane{
		ID:         id,
		descriptor: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionVNC, Screen: screen},
		surface:    surface,
	}
}

func splitScreenTab(panes ...*pane) *Tab {
	tab := &Tab{panes: make(map[model.PaneID]*pane, len(panes))}
	for _, pane := range panes {
		tab.panes[pane.ID] = pane
	}
	return tab
}

// TestPrepareSplitScreenAsksForOneMoreScreen proves a single-screen Tab asks the
// server for a second screen before the new Pane exists.
func TestPrepareSplitScreenAsksForOneMoreScreen(t *testing.T) {
	surface := &splitSurface{canSplit: true, screens: 1}
	focused := vncPane("pane-1", 0, surface)
	tab := splitScreenTab(focused)

	screen, err := (&State{}).prepareSplitScreen(tab, focused)
	if err != nil {
		t.Fatalf("prepareSplitScreen: %v", err)
	}
	if screen != 1 {
		t.Fatalf("screen = %d, want the first free index 1", screen)
	}
	if len(surface.prepared) != 1 || surface.prepared[0] != 2 {
		t.Fatalf("prepared = %v, want one request for 2 screens", surface.prepared)
	}
}

// TestPrepareSplitScreenCountsADetachedPane proves the free-index search spans
// windows: the detached Pane still shows its screen, so a split in the main
// window must not take that index and put two Panes on one screen.
func TestPrepareSplitScreenCountsADetachedPane(t *testing.T) {
	surface := &splitSurface{canSplit: true, screens: 2}
	focused := vncPane("pane-1", 0, surface)
	tab := splitScreenTab(focused)
	detached := splitScreenTab(vncPane("pane-2", 1, &splitSurface{canSplit: true, screens: 2}))
	state := &State{tabs: []*Tab{tab}, detached: []*Tab{detached}}

	screen, err := state.prepareSplitScreen(tab, focused)
	if err != nil {
		t.Fatalf("prepareSplitScreen: %v", err)
	}
	if screen != 2 {
		t.Fatalf("screen = %d, want the index the detached Pane does not own", screen)
	}
	if len(surface.prepared) != 1 || surface.prepared[0] != 3 {
		t.Fatalf("prepared = %v, want one request for 3 screens", surface.prepared)
	}
}

// TestPrepareSplitScreenReusesAFreedScreen proves closing a Pane frees its
// screen for the next split without asking the server for another one.
func TestPrepareSplitScreenReusesAFreedScreen(t *testing.T) {
	surface := &splitSurface{canSplit: true, screens: 3}
	focused := vncPane("pane-1", 0, surface)
	tab := splitScreenTab(focused, vncPane("pane-3", 2, nil))

	screen, err := (&State{}).prepareSplitScreen(tab, focused)
	if err != nil {
		t.Fatalf("prepareSplitScreen: %v", err)
	}
	if screen != 1 {
		t.Fatalf("screen = %d, want the free index 1", screen)
	}
	if len(surface.prepared) != 0 {
		t.Fatalf("prepared = %v, want no request: the layout already has it", surface.prepared)
	}
}

// TestPrepareSplitScreenRefusesWithoutSupport proves a Pane whose remote cannot
// split is refused before any work, so no Pane opens on the same desktop.
func TestPrepareSplitScreenRefusesWithoutSupport(t *testing.T) {
	surface := &splitSurface{canSplit: false}
	focused := vncPane("pane-1", 0, surface)
	tab := splitScreenTab(focused)

	if _, err := (&State{}).prepareSplitScreen(tab, focused); !errors.Is(err, ErrSplitUnavailable) {
		t.Fatalf("prepareSplitScreen = %v, want ErrSplitUnavailable", err)
	}
	if len(surface.prepared) != 0 {
		t.Fatalf("prepared = %v, want no request", surface.prepared)
	}
}

// TestPrepareSplitScreenPropagatesARefusal proves a server that answered a
// multi-screen request without adopting it stops the split.
func TestPrepareSplitScreenPropagatesARefusal(t *testing.T) {
	surface := &splitSurface{canSplit: true, screens: 1, prepareErr: errors.New("refused")}
	focused := vncPane("pane-1", 0, surface)
	tab := splitScreenTab(focused)

	if _, err := (&State{}).prepareSplitScreen(tab, focused); err == nil {
		t.Fatal("prepareSplitScreen accepted a refused multi-screen layout")
	}
}
