package shell

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// realSurfaceFactory opens a real Session surface for every Pane, so the
// layout test renders the production status bar and Session menu inside the
// real shell sidebar, tab strip, and split tree.
type realSurfaceFactory struct{}

func (realSurfaceFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return newFakeTransport(), nil
}

func newRealSurfaceState(t *testing.T) *state.State {
	t.Helper()
	service := appsession.NewService(realSurfaceFactory{})
	t.Cleanup(service.CloseAll)
	return state.NewState(state.WithSessions(service, func(s *appsession.Session) (state.Surface, error) {
		return sessionui.New(s,
			sessionui.WithRedraw(func() {}),
			sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
			sessionui.WithDropAction(func() domainterminal.DropAction {
				return domainterminal.DropInsert
			}),
			sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
		)
	}))
}

func waitForRealSurfaces(t *testing.T, st *state.State, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.Flush()
		live := 0
		for index := 0; index < st.TabCount(); index++ {
			root, _, ok := st.TabTree(index)
			if !ok {
				continue
			}
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				if info, ok := st.PaneInfoAt(index, id); ok && info.Surface != nil {
					live++
				}
			}
		}
		if live == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d real Pane surfaces", count)
}

// TestSessionMenuStaysReachableInRealWindowLayouts is D1's regression: the
// Session menu used to be the last in a row of fixed-width logging and
// encoding controls, so at 720x480 it sat outside the terminal card, and the
// right Pane of a 1100x720 split had its menu entirely off-window. Every Pane
// must now expose its menu inside its own Pane box at both sizes, including
// left/right and top/bottom splits.
func TestSessionMenuStaysReachableInRealWindowLayouts(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{720, 480}, {1100, 720}} {
		t.Run(layoutName(size[0], size[1], "single"), func(t *testing.T) {
			st := newRealSurfaceState(t)
			if err := st.NewTab(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, st, 1)
			tt := ui.NewTester(View(st), size[0], size[1])
			tt.SetLocale("en")
			tt.Frame()

			menu := assertMenuInsidePane(t, tt, m, 1, size)
			if menu.X < float32(sidebarWidth) {
				t.Fatalf("single Pane menu starts left of the sidebar: %+v", menu)
			}
			if err := tt.Click(m.SessionMenuFor(1)); err != nil {
				t.Fatalf("click the single Pane menu: %v", err)
			}
			if len(tt.Menu()) == 0 {
				t.Fatalf("%dx%d single Pane menu did not open after a click", size[0], size[1])
			}
		})

		t.Run(layoutName(size[0], size[1], "split-right"), func(t *testing.T) {
			st := newRealSurfaceState(t)
			if err := st.NewTab(); err != nil {
				t.Fatal(err)
			}
			if err := st.SplitRight(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, st, 2)
			tt := ui.NewTester(View(st), size[0], size[1])
			tt.SetLocale("en")
			tt.Frame()

			left := assertMenuInsidePane(t, tt, m, 1, size)
			right := assertMenuInsidePane(t, tt, m, 2, size)
			if right.X <= left.X {
				t.Fatalf("%dx%d right Pane menu is not to the right of the left one: left %+v right %+v",
					size[0], size[1], left, right)
			}
			// Both menus must be clickable, not merely painted.
			for _, number := range []int{1, 2} {
				if err := tt.Click(m.SessionMenuFor(number)); err != nil {
					t.Fatalf("click menu %d: %v", number, err)
				}
				if len(tt.Menu()) == 0 {
					t.Fatalf("%dx%d menu %d did not open after a click", size[0], size[1], number)
				}
				tt.CloseMenu()
			}
		})

		t.Run(layoutName(size[0], size[1], "split-down"), func(t *testing.T) {
			st := newRealSurfaceState(t)
			if err := st.NewTab(); err != nil {
				t.Fatal(err)
			}
			if err := st.SplitDown(); err != nil {
				t.Fatal(err)
			}
			waitForRealSurfaces(t, st, 2)
			tt := ui.NewTester(View(st), size[0], size[1])
			tt.SetLocale("en")
			tt.Frame()

			top := assertMenuInsidePane(t, tt, m, 1, size)
			bottom := assertMenuInsidePane(t, tt, m, 2, size)
			if bottom.Y <= top.Y {
				t.Fatalf("%dx%d bottom Pane menu is not below the top one: top %+v bottom %+v",
					size[0], size[1], top, bottom)
			}
			for _, number := range []int{1, 2} {
				if err := tt.Click(m.SessionMenuFor(number)); err != nil {
					t.Fatalf("click menu %d: %v", number, err)
				}
				if len(tt.Menu()) == 0 {
					t.Fatalf("%dx%d stacked menu %d did not open after a click", size[0], size[1], number)
				}
				tt.CloseMenu()
			}
		})
	}
}

func layoutName(width, height int, kind string) string {
	return kind + "-" + strconv.Itoa(width) + "x" + strconv.Itoa(height)
}

// assertMenuInsidePane finds pane's region and its Session menu and asserts
// the whole menu control sits inside the Pane and the window.
func assertMenuInsidePane(t *testing.T, tt *ui.Tester, m locales.Messages, pane int, size [2]int) ui.Rect {
	t.Helper()
	region, ok := tt.Find(m.PaneRegion(pane))
	if !ok {
		t.Fatalf("%dx%d does not show Pane %d: %v", size[0], size[1], pane, tt.Texts())
	}
	menu, ok := tt.Find(m.SessionMenuFor(pane))
	if !ok {
		t.Fatalf("%dx%d does not show the Session menu for Pane %d: %v",
			size[0], size[1], pane, tt.Texts())
	}
	const epsilon = 0.5
	if menu.X < region.X-epsilon || menu.Y < region.Y-epsilon ||
		menu.X+menu.W > region.X+region.W+epsilon ||
		menu.Y+menu.H > region.Y+region.H+epsilon {
		t.Fatalf("%dx%d Pane %d menu %+v is outside its Pane %+v",
			size[0], size[1], pane, menu, region)
	}
	if menu.X < -epsilon || menu.Y < -epsilon ||
		menu.X+menu.W > float32(size[0])+epsilon ||
		menu.Y+menu.H > float32(size[1])+epsilon {
		t.Fatalf("%dx%d Pane %d menu is outside the window: %+v",
			size[0], size[1], pane, menu)
	}
	// The menu is the header's 22-DIP icon button; anything smaller is a
	// control the header squeezed.
	if menu.W < 22-epsilon || menu.H < 22-epsilon {
		t.Fatalf("%dx%d Pane %d menu is squeezed to %.1fx%.1f DIPs: %+v",
			size[0], size[1], pane, menu.W, menu.H, menu)
	}
	return menu
}
