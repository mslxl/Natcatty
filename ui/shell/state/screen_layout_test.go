package state

import (
	"image"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// layoutSurface is a VNC Pane Surface that reports its screen geometry and
// records the layouts the Tab sends through it.
type layoutSurface struct {
	fakeSurface
	index    int
	rect     image.Rectangle
	ready    bool
	refuses  bool
	listener func()
	layouts  [][]sessionui.ScreenRect
}

func (s *layoutSurface) ScreenGeometry() (int, image.Rectangle, bool) {
	return s.index, s.rect, s.ready
}

func (s *layoutSurface) LayoutScreens(screens []sessionui.ScreenRect) bool {
	if s.refuses {
		return false
	}
	s.layouts = append(s.layouts, append([]sessionui.ScreenRect(nil), screens...))
	return true
}

func (s *layoutSurface) SetScreenGeometryListener(fn func()) { s.listener = fn }

func layoutPane(id model.PaneID, screen int, surface Surface) *pane {
	return &pane{
		ID:         id,
		descriptor: domainworkspace.SessionDescriptor{Kind: domainworkspace.SessionVNC, Screen: screen},
		surface:    surface,
	}
}

// TestTabDrivesTheDesktopFromEveryPane proves the Tab sends one layout built
// from all its Panes: each screen takes its Pane's size and relative position,
// and only the lowest screen's Pane sends it.
func TestTabDrivesTheDesktopFromEveryPane(t *testing.T) {
	first := &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 100, 80)}
	second := &layoutSurface{index: 1, ready: true, rect: image.Rect(100, 0, 180, 80)}
	tab := splitScreenTab(
		layoutPane("pane-1", 0, first),
		layoutPane("pane-2", 1, second),
	)

	(&State{}).driveTabScreens(tab)

	if len(second.layouts) != 0 {
		t.Fatalf("the second Pane sent %v; only the Tab's driver may", second.layouts)
	}
	if len(first.layouts) != 1 {
		t.Fatalf("layouts = %v, want one request from the driver", first.layouts)
	}
	screens := first.layouts[0]
	want := []sessionui.ScreenRect{
		{Index: 0, Rect: image.Rect(0, 0, 100, 80)},
		{Index: 1, Rect: image.Rect(100, 0, 180, 80)},
	}
	if len(screens) != len(want) {
		t.Fatalf("screens = %v, want %v", screens, want)
	}
	for i := range want {
		if screens[i] != want[i] {
			t.Fatalf("screen %d = %+v, want %+v", i, screens[i], want[i])
		}
	}
}

// TestTabWaitsForEveryPane proves a Pane that has not drawn, or one that is
// still connecting, stops the layout: asking without it would drop the screen
// its index owns and renumber its peers.
func TestTabWaitsForEveryPane(t *testing.T) {
	cases := []struct {
		name string
		tab  *Tab
	}{
		{
			name: "a Pane that has not drawn",
			tab: splitScreenTab(
				layoutPane("pane-1", 0, &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 64, 48)}),
				layoutPane("pane-2", 1, &layoutSurface{index: 1}),
			),
		},
		{
			name: "a Pane still connecting",
			tab: splitScreenTab(
				layoutPane("pane-1", 0, &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 64, 48)}),
				layoutPane("pane-2", 1, nil),
			),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			(&State{}).driveTabScreens(tc.tab)
			for _, value := range tc.tab.panes {
				if surface, ok := value.surface.(*layoutSurface); ok && len(surface.layouts) != 0 {
					t.Fatalf("a layout was sent without every Pane: %v", surface.layouts)
				}
			}
		})
	}
}

// TestTabLeavesTheLayoutAloneWhileMagnified proves a zoomed Tab does not size
// the desktop from the one Pane it shows, which would shrink the screens its
// hidden members still own.
func TestTabLeavesTheLayoutAloneWhileMagnified(t *testing.T) {
	surface := &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 400, 300)}
	tab := splitScreenTab(layoutPane("pane-1", 0, surface))
	tab.Magnified = "pane-1"

	(&State{}).driveTabScreens(tab)

	if len(surface.layouts) != 0 {
		t.Fatalf("a magnified Tab sent %v", surface.layouts)
	}
}

// TestTabKeepsAFreedScreenPlaceholder proves a closed middle Pane leaves a
// placeholder screen behind: the surviving Panes keep their screen numbers, so
// their screens stay theirs.
func TestTabKeepsAFreedScreenPlaceholder(t *testing.T) {
	first := &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 100, 80)}
	last := &layoutSurface{index: 2, ready: true, rect: image.Rect(200, 0, 300, 80)}
	tab := splitScreenTab(
		layoutPane("pane-1", 0, first),
		layoutPane("pane-3", 2, last),
	)

	(&State{}).driveTabScreens(tab)

	screens := first.layouts[0]
	if len(screens) != 3 {
		t.Fatalf("screens = %v, want the freed index kept", screens)
	}
	if screens[0].Index != 0 || screens[1].Index != 1 || screens[2].Index != 2 {
		t.Fatalf("indexes = %v, want 0,1,2", screens)
	}
	if screens[1].Rect.Empty() {
		t.Fatalf("the freed screen has no rectangle: %v", screens[1].Rect)
	}
	if screens[2].Rect != image.Rect(200, 0, 300, 80) {
		t.Fatalf("screen 2 = %v, want its Pane's rectangle", screens[2].Rect)
	}
	// The placeholder sits to the right of every used screen, so it cannot
	// overlap one.
	for _, screen := range []sessionui.ScreenRect{screens[0], screens[2]} {
		if screens[1].Rect.Overlaps(screen.Rect) {
			t.Fatalf("the placeholder %v overlaps %v", screens[1].Rect, screen.Rect)
		}
	}
}

// TestDetachedPaneKeepsItsOwnScreen proves a window still drives the screens
// of the Panes that left it. Both windows connect to the same server, so one
// desktop layout must cover both; a layout built from the staying window alone
// would drop the detached Pane's screen, and that Pane would fall back to the
// whole framebuffer — the two windows showing the same picture.
func TestDetachedPaneKeepsItsOwnScreen(t *testing.T) {
	stayed := &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 200, 160)}
	left := &layoutSurface{index: 1, ready: true, rect: image.Rect(0, 0, 180, 160)}
	mainTab := splitScreenTab(layoutPane("pane-1", 0, stayed))
	detachedTab := splitScreenTab(layoutPane("pane-2", 1, left))
	state := &State{tabs: []*Tab{mainTab}, detached: []*Tab{detachedTab}}
	state.wireScreenLayout(mainTab)
	state.wireScreenLayout(detachedTab)

	// The Pane that stayed takes the whole split area, so its geometry report
	// drives the shared desktop.
	stayed.listener()

	if len(stayed.layouts) != 1 {
		t.Fatalf("layouts = %v, want one request", stayed.layouts)
	}
	screens := stayed.layouts[0]
	if len(screens) != 2 {
		t.Fatalf("screens = %v, want the staying and the detached screen", screens)
	}
	if screens[0].Rect.Empty() || screens[1].Rect.Empty() {
		t.Fatalf("a screen has no rectangle: %v", screens)
	}
	if screens[0].Rect.Overlaps(screens[1].Rect) {
		t.Fatalf("screens overlap: %v and %v", screens[0].Rect, screens[1].Rect)
	}

	// The detached window's own draw reaches the same desktop, since the server
	// has one layout for every connection.
	left.listener()
	if len(stayed.layouts) != 2 {
		t.Fatalf("layouts = %v, want the detached window's report to drive too", stayed.layouts)
	}
	second := stayed.layouts[1]
	if len(second) != len(screens) {
		t.Fatalf("the detached window drove %v, want the same two screens", second)
	}
	for i := range screens {
		if second[i] != screens[i] {
			t.Fatalf("screen %d = %+v, want the same layout %+v", i, second[i], screens[i])
		}
	}
}

// TestWireScreenLayoutDrivesFromThePaneListener proves the shell connects a
// Pane's geometry reports to the Tab's driver: the Tab sends the layout when
// the Pane says it moved.
func TestWireScreenLayoutDrivesFromThePaneListener(t *testing.T) {
	surface := &layoutSurface{index: 0, ready: true, rect: image.Rect(0, 0, 64, 48)}
	tab := splitScreenTab(layoutPane("pane-1", 0, surface))

	(&State{}).wireScreenLayout(tab)
	if surface.listener == nil {
		t.Fatal("the Pane was not given a geometry listener")
	}
	surface.listener()

	if len(surface.layouts) != 1 {
		t.Fatalf("layouts = %v, want one request after the report", surface.layouts)
	}
}

// TestTabFallsThroughAPaneThatCannotDrive proves a Pane that declines to drive
// (a hibernated or view-only one) passes the layout to the next Pane instead of
// silencing the Tab.
func TestTabFallsThroughAPaneThatCannotDrive(t *testing.T) {
	silent := &layoutSurface{index: 0, ready: true, refuses: true, rect: image.Rect(0, 0, 100, 80)}
	next := &layoutSurface{index: 1, ready: true, rect: image.Rect(100, 0, 180, 80)}
	tab := splitScreenTab(
		layoutPane("pane-1", 0, silent),
		layoutPane("pane-2", 1, next),
	)

	(&State{}).driveTabScreens(tab)

	if len(silent.layouts) != 0 {
		t.Fatalf("the declining Pane recorded %v", silent.layouts)
	}
	if len(next.layouts) != 1 {
		t.Fatalf("the next Pane recorded %v, want the layout", next.layouts)
	}
}
