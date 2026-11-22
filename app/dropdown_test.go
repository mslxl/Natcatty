package app

import (
	"math"
	"slices"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeScreen is the display seam the Dropdown Panel positions itself against.
// It mirrors MyGo's nearest-display rule so a test can move the pointer from
// one display to another without a real screen.
type fakeScreen struct {
	cursor   mygo.Point
	displays []mygo.Display
	// nearestOverrides the real lookup when set, so a test can report "no
	// display under the pointer" while a primary display still exists.
	nearest func(mygo.Point) (mygo.Display, bool)
}

func (s *fakeScreen) CursorScreenPoint() mygo.Point { return s.cursor }

func (s *fakeScreen) DisplayNearestPoint(point mygo.Point) mygo.Display {
	if s.nearest != nil {
		if display, ok := s.nearest(point); ok {
			return display
		}
		return mygo.Display{}
	}
	best, bestDistance := mygo.Display{}, math.Inf(1)
	for _, display := range s.displays {
		b := display.Bounds
		dx := math.Max(math.Max(float64(b.X-point.X), 0), float64(point.X-(b.X+b.Width)))
		dy := math.Max(math.Max(float64(b.Y-point.Y), 0), float64(point.Y-(b.Y+b.Height)))
		if distance := dx*dx + dy*dy; distance < bestDistance {
			best, bestDistance = display, distance
		}
	}
	return best
}

func (s *fakeScreen) PrimaryDisplay() mygo.Display {
	for _, display := range s.displays {
		if display.Primary {
			return display
		}
	}
	if len(s.displays) > 0 {
		return s.displays[0]
	}
	return mygo.Display{}
}

// vibrantStyle is a test Interface Style whose window shows a material, so
// the shared vibrancy path is observable. Both shipped styles fill the window
// with the theme's color.
type vibrantStyle struct{ kit.Style }

func (vibrantStyle) Material(role kit.SurfaceRole) kit.Material {
	if role == kit.SurfaceWindow {
		return kit.Material{Kind: kit.MaterialVibrant}
	}
	return kit.Classic.Material(role)
}

func primaryDisplay() mygo.Display {
	return mygo.Display{
		ID:       1,
		Primary:  true,
		Bounds:   mygo.Rectangle{Width: 1920, Height: 1080},
		WorkArea: mygo.Rectangle{X: 0, Y: 25, Width: 1920, Height: 1040},
	}
}

func secondDisplay() mygo.Display {
	return mygo.Display{
		ID:       2,
		Bounds:   mygo.Rectangle{X: 1920, Width: 1280, Height: 1024},
		WorkArea: mygo.Rectangle{X: 1920, Width: 1280, Height: 1024},
	}
}

func newDropdownGraph(t *testing.T) (*windowGraph, *fakeScreen) {
	t.Helper()
	graph := newWindowGraph(t, nil)
	screen := &fakeScreen{cursor: mygo.Point{X: 400, Y: 300}, displays: []mygo.Display{primaryDisplay()}}
	graph.manager.SetScreenProvider(screen)
	return graph, screen
}

func dropdownWindow(t *testing.T, graph *windowGraph) *fakeWindow {
	t.Helper()
	window, ok := graph.manager.dropdown.(*fakeWindow)
	if !ok || window == nil {
		t.Fatalf("dropdown window = %T, want the fake host window", graph.manager.dropdown)
	}
	return window
}

// TestDropdownToggleCreatesAHiddenPanelAndPositionsItBeforeItsFirstShow pins
// the creation contract: a frameless Panel, created hidden, without a StateKey,
// positioned with SetBounds before Show and Focus. It also proves every later
// toggle reuses the same native window.
func TestDropdownToggleCreatesAHiddenPanelAndPositionsItBeforeItsFirstShow(t *testing.T) {
	graph, _ := newDropdownGraph(t)

	graph.manager.ToggleDropdown()

	if len(graph.host.windows) != 1 {
		t.Fatalf("windows = %d, want one Dropdown Panel", len(graph.host.windows))
	}
	window := dropdownWindow(t, graph)
	if !window.spec.Panel {
		t.Fatal("the dropdown is not a native Panel: it would appear in the taskbar and app switcher")
	}
	if !window.spec.Hidden {
		t.Fatal("the dropdown was created visible instead of hidden")
	}
	if window.spec.StateKey != "" {
		t.Fatalf("StateKey = %q, want none: the dropdown must not restore persisted bounds", window.spec.StateKey)
	}
	// The primary display's work area starts below its menu bar and is
	// 1040 high, so the default half height is 520.
	want := windowBounds{X: 0, Y: 25, Width: 1920, Height: 520}
	if window.bounds != want {
		t.Fatalf("bounds = %+v, want %+v", window.bounds, want)
	}
	wantEvents := []string{"create", "set-bounds", "show", "focus"}
	if !slices.Equal(window.events, wantEvents) {
		t.Fatalf("window events = %v, want %+v", window.events, wantEvents)
	}
	if !window.shown || !window.focused {
		t.Fatalf("shown=%v focused=%v, want a visible, focused panel", window.shown, window.focused)
	}
	if len(graph.manager.panels) != 1 || graph.manager.panels[0] != window {
		t.Fatal("the dropdown did not join the manager's panel enumeration")
	}

	// A second press hides it without destroying it; a third shows the same
	// native window.
	graph.manager.ToggleDropdown()
	if window.shown {
		t.Fatal("the second toggle did not hide the panel")
	}
	if window.destroyed {
		t.Fatal("hiding the dropdown destroyed its native window")
	}
	graph.manager.ToggleDropdown()
	if len(graph.host.windows) != 1 {
		t.Fatalf("windows = %d, want the same one reused", len(graph.host.windows))
	}
	if graph.manager.dropdown != window {
		t.Fatal("the dropdown created a second native window instead of reusing the first")
	}
	if !window.shown || !window.focused {
		t.Fatal("the reused panel was not shown and focused")
	}
}

// TestDropdownBoundsRecomputeFromThePointerDisplayOnEveryShow proves the
// geometry is not cached: moving the pointer to another display between two
// shows places the next show on that display's work area.
func TestDropdownBoundsRecomputeFromThePointerDisplayOnEveryShow(t *testing.T) {
	graph, screen := newDropdownGraph(t)
	screen.displays = append(screen.displays, secondDisplay())

	graph.manager.ToggleDropdown()
	first := dropdownWindow(t, graph)
	if want := (windowBounds{X: 0, Y: 25, Width: 1920, Height: 520}); first.bounds != want {
		t.Fatalf("first bounds = %+v, want the primary work area %+v", first.bounds, want)
	}
	graph.manager.ToggleDropdown()

	// Move the pointer onto the second display.
	screen.cursor = mygo.Point{X: 2400, Y: 400}
	before := len(first.events)
	graph.manager.ToggleDropdown()

	second := dropdownWindow(t, graph)
	if second != first {
		t.Fatal("the second show created a new native window")
	}
	want := windowBounds{X: 1920, Y: 0, Width: 1280, Height: 512}
	if second.bounds != want {
		t.Fatalf("second bounds = %+v, want the second work area %+v", second.bounds, want)
	}
	// Every show positions before it shows, so no frame appears at the
	// previous display's rectangle.
	events := second.events[before:]
	if !slices.Equal(events, []string{"set-bounds", "show", "focus"}) {
		t.Fatalf("second show events = %v, want set-bounds, show, focus", events)
	}
}

// TestDropdownFallsBackToThePrimaryDisplay covers a platform that cannot name
// the display under the pointer: the primary display's work area is used.
func TestDropdownFallsBackToThePrimaryDisplay(t *testing.T) {
	graph, screen := newDropdownGraph(t)
	screen.nearest = func(mygo.Point) (mygo.Display, bool) { return mygo.Display{}, false }

	graph.manager.ToggleDropdown()

	window := dropdownWindow(t, graph)
	want := windowBounds{X: 0, Y: 25, Width: 1920, Height: 520}
	if window.bounds != want {
		t.Fatalf("bounds = %+v, want the primary work area %+v", window.bounds, want)
	}
}

// TestDropdownClampsItsHeightToTheWorkArea covers a short work area: the half
// height still leaves the Quake minimum, unless the work area itself is
// smaller.
func TestDropdownClampsItsHeightToTheWorkArea(t *testing.T) {
	graph, screen := newDropdownGraph(t)
	screen.displays = []mygo.Display{{
		ID:       3,
		Primary:  true,
		Bounds:   mygo.Rectangle{Width: 800, Height: 200},
		WorkArea: mygo.Rectangle{Width: 800, Height: 200},
	}}

	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 120 {
		t.Fatalf("height = %d, want the 120 DIP minimum", got)
	}
}

// TestDropdownHeightUsesTheConfiguredUnitAndModeMinimum pins the geometry
// settings end to end: percent and pixel values reach the Panel, each mode
// has its own setting, a changed value applies on the next show rather than
// resizing the open Panel, and the Quake/top-down minimums clamp a small
// configured height.
func TestDropdownHeightUsesTheConfiguredUnitAndModeMinimum(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	// The fake primary work area is 1040 DIPs high.

	q.settings.QuakeHeight, q.settings.QuakeHeightUnit = 300, appdesktop.HeightUnitPX
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 300 {
		t.Fatalf("quake pixel height = %d, want 300", got)
	}
	graph.manager.ToggleDropdown()

	q.settings.QuakeHeight, q.settings.QuakeHeightUnit = 25, appdesktop.HeightUnitPercent
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 260 {
		t.Fatalf("quake percent height = %d, want 25%% of 1040", got)
	}
	graph.manager.ToggleDropdown()

	// A tiny configured percentage still leaves the Quake minimum.
	q.settings.QuakeHeight, q.settings.QuakeHeightUnit = 10, appdesktop.HeightUnitPercent
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 120 {
		t.Fatalf("quake minimum height = %d, want 120", got)
	}
	graph.manager.ToggleDropdown()

	// Top-down has its own setting and a larger minimum.
	q.mode = appdesktop.HotkeyModeTopdown
	q.settings.TopdownHeight, q.settings.TopdownHeightUnit = 50, appdesktop.HeightUnitPX
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 480 {
		t.Fatalf("topdown minimum height = %d, want 480", got)
	}
	graph.manager.ToggleDropdown()

	q.settings.TopdownHeight, q.settings.TopdownHeightUnit = 80, appdesktop.HeightUnitPercent
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 832 {
		t.Fatalf("topdown percent height = %d, want 80%% of 1040", got)
	}
}

// TestDropdownHeightOnAShortWorkAreaUsesTheWorkAreaHeight proves the
// mode-specific minimum never exceeds a smaller work area: both modes take
// the whole work area instead of overflowing it.
func TestDropdownHeightOnAShortWorkAreaUsesTheWorkAreaHeight(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph
	graph.manager.SetScreenProvider(&fakeScreen{displays: []mygo.Display{{
		ID:       9,
		Primary:  true,
		Bounds:   mygo.Rectangle{Width: 800, Height: 300},
		WorkArea: mygo.Rectangle{Width: 800, Height: 300},
	}}})

	q.settings.QuakeHeight, q.settings.QuakeHeightUnit = 10, appdesktop.HeightUnitPercent
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 120 {
		t.Fatalf("quake short-work-area height = %d, want 120", got)
	}
	graph.manager.ToggleDropdown()

	q.mode = appdesktop.HotkeyModeTopdown
	q.settings.TopdownHeight, q.settings.TopdownHeightUnit = 10, appdesktop.HeightUnitPercent
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 300 {
		t.Fatalf("topdown short-work-area height = %d, want the 300 DIP work area", got)
	}
}

// TestDropdownHideSwitchDisablesBlur covers the blur trigger switch: with it
// off, losing focus leaves the Panel visible; with it on, blur hides without
// destroying the window.
func TestDropdownHideSwitchDisablesBlur(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph

	q.settings.HideOnBlur = false
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)
	window.Blur()
	if !window.shown {
		t.Fatal("turning off the blur trigger did not keep the Panel visible")
	}

	q.settings.HideOnBlur = true
	window.Blur()
	if window.shown {
		t.Fatal("turning the blur trigger back on did not hide the Panel")
	}
	if window.destroyed {
		t.Fatal("blur destroyed the Panel")
	}
}

// TestDropdownHideSwitchDisablesEscape covers the Escape trigger switch: with
// it off, the close request is vetoed without hiding; with it on, it hides
// without destroying.
func TestDropdownHideSwitchDisablesEscape(t *testing.T) {
	q := newQuakeGraph(t)
	graph := q.windowGraph

	q.settings.HideOnEscape = false
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)
	window.Close()
	if !window.shown {
		t.Fatal("turning off the Escape trigger did not keep the Panel visible")
	}
	if window.destroyed {
		t.Fatal("the vetoed Escape destroyed the Panel")
	}

	q.settings.HideOnEscape = true
	window.Close()
	if window.shown {
		t.Fatal("turning the Escape trigger back on did not hide the Panel")
	}
	if window.destroyed {
		t.Fatal("Escape destroyed the Panel")
	}
}

// TestConfiguredDropdownHeightFallsBackOnIncompleteRecords guards the
// defensive path used when settings never passed through the service: a zero
// height or unknown unit takes the mode default instead of collapsing the
// Panel.
func TestConfiguredDropdownHeightFallsBackOnIncompleteRecords(t *testing.T) {
	workArea := windowBounds{Width: 1920, Height: 1000}
	empty := appdesktop.Settings{}
	if got := configuredDropdownHeight(empty, false, workArea); got != 500 {
		t.Fatalf("empty quake settings height = %d, want the 50%% default", got)
	}
	if got := configuredDropdownHeight(empty, true, workArea); got != 800 {
		t.Fatalf("empty topdown settings height = %d, want the 80%% default", got)
	}

	bogus := appdesktop.Settings{
		QuakeHeight:     300,
		QuakeHeightUnit: appdesktop.HeightUnit("bogus"),
	}
	if got := configuredDropdownHeight(bogus, false, workArea); got != 500 {
		t.Fatalf("unknown unit height = %d, want the 50%% default", got)
	}
}

// TestDropdownHidesOnBlurAndEscapeWithoutDestroying proves the two platform
// hide paths, blur and the Escape-triggered close request, only hide the
// Panel and the hotkey shows it again.
func TestDropdownHidesOnBlurAndEscapeWithoutDestroying(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	window.Blur()
	if window.shown {
		t.Fatal("blur did not hide the dropdown")
	}
	if window.destroyed {
		t.Fatal("blur destroyed the dropdown")
	}

	graph.manager.ToggleDropdown()
	if !window.shown {
		t.Fatal("the hotkey did not show the hidden dropdown again")
	}
	if graph.manager.dropdown != window || len(graph.host.windows) != 1 {
		t.Fatal("re-showing created a second native window")
	}

	// Escape reaches the Panel as a close request. The dropdown refuses the
	// close and hides itself, so the window is reusable.
	window.Close()
	if window.destroyed {
		t.Fatal("the Escape close request destroyed the dropdown")
	}
	if window.shown {
		t.Fatal("the refused close did not hide the dropdown")
	}
	graph.manager.ToggleDropdown()
	if !window.shown || window.destroyed {
		t.Fatal("the dropdown was not shown again after the refused close")
	}
}

// TestDropdownHidesFromItsOwnHideButton proves the panel's content draws a
// hide button, and clicking it hides the same window.
func TestDropdownHidesFromItsOwnHideButton(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	tester := render(window)
	label := locales.In("en").DropdownHide()
	if !tester.HasText(label) {
		t.Fatalf("the dropdown content has no hide button: %v", tester.Texts())
	}
	if err := tester.Click(label); err != nil {
		t.Fatalf("clicking the hide button: %v", err)
	}
	if window.shown {
		t.Fatal("the hide button did not hide the dropdown")
	}
	if window.destroyed {
		t.Fatal("the hide button destroyed the dropdown")
	}
}

// TestDropdownResolvesItsContentProviderOnce proves the native window keeps
// one root content function across shows: later content routing changes what
// that function renders, not which function the window holds.
func TestDropdownResolvesItsContentProviderOnce(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	calls := 0
	graph.manager.SetDropdownContentProvider(func() func(*ui.Context) {
		calls++
		return func(*ui.Context) {}
	})

	graph.manager.ToggleDropdown()
	graph.manager.ToggleDropdown()
	graph.manager.ToggleDropdown()

	if calls != 1 {
		t.Fatalf("content provider resolved %d times, want once at creation", calls)
	}
	if graph.manager.dropdown == nil {
		t.Fatal("the content provider test created no Panel")
	}
}

// TestDropdownHeightComesFromTheInstalledProvider is the seam the height
// settings and the top-down mode use: a provider replaces the default half
// height, and every show asks it again.
func TestDropdownHeightComesFromTheInstalledProvider(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	height := func(workArea windowBounds) int { return workArea.Height / 4 }
	graph.manager.SetDropdownHeightProvider(height)

	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 260 {
		t.Fatalf("height = %d, want a quarter of the 1040 DIP work area", got)
	}

	graph.manager.ToggleDropdown()
	graph.manager.SetDropdownHeightProvider(func(workArea windowBounds) int {
		return workArea.Height * 8 / 10
	})
	graph.manager.ToggleDropdown()
	if got := dropdownWindow(t, graph).bounds.Height; got != 832 {
		t.Fatalf("height = %d, want the changed provider's 832", got)
	}
}

// TestDropdownFollowsTheLiveTheme proves the Panel's content is built under
// the same per-frame theme provider as every window: the root paints the live
// background without reopening the Panel.
func TestDropdownFollowsTheLiveTheme(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	theme := ui.LightTheme()
	theme.Background = ui.RGB(1, 2, 3)
	graph.manager.SetThemeProvider(func() *ui.Theme { return theme })
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	tester := ui.NewTester(window.spec.Content, window.bounds.Width, window.bounds.Height)
	tester.SetLocale("en")
	tester.Frame()
	// Below the bar the placeholder paints nothing, so the root's live theme
	// background shows through.
	got := tester.Image().RGBAAt(window.bounds.Width/2, window.bounds.Height*3/4)
	if got.R != 1 || got.G != 2 || got.B != 3 {
		t.Fatalf("dropdown background = %v, want the live theme's (1,2,3)", got)
	}
}

// TestDropdownFollowsSharedAppearanceAndNeverKeepsTheAppAlive proves the Panel
// takes the shared opacity, vibrancy, and redraw paths, yet closing the main
// window still ends the app's windows.
func TestDropdownFollowsSharedAppearanceAndNeverKeepsTheAppAlive(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	graph.manager.OpenMain()
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	graph.manager.SetStyleProvider(func() kit.Style { return vibrantStyle{Style: kit.Classic} })
	graph.manager.SetOpacity(0.4)
	if window.opacity != 0.4 {
		t.Fatalf("panel opacity = %v, want the live 0.4", window.opacity)
	}
	graph.manager.SetOpacity(1)
	if window.vibrancy != mygo.VibrancySidebar {
		t.Fatalf("panel vibrancy = %q, want the style's sidebar material", window.vibrancy)
	}
	window.invalidations = 0
	graph.manager.InvalidateAll()
	if window.invalidations == 0 {
		t.Fatal("InvalidateAll skipped the panel")
	}

	graph.manager.Main().Close()
	if !graph.manager.noWindowsLeft() {
		t.Fatal("a live Panel counted as a window that keeps the app alive")
	}
	if !graph.manager.Quitting() {
		t.Fatal("closing the last counted window did not mark the quit")
	}
}

// TestDropdownQuitHidesAndDestroysThePanelWithoutBlocking proves the quit path
// leaves no native window behind and never asks a close question.
func TestDropdownQuitHidesAndDestroysThePanelWithoutBlocking(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	graph.manager.ShutdownDropdown()

	if !window.destroyed {
		t.Fatal("quitting left the Dropdown Panel alive")
	}
	if window.shown {
		t.Fatal("quitting left the Dropdown Panel visible")
	}
	if graph.manager.dropdown != nil {
		t.Fatal("the manager still points at a destroyed dropdown")
	}
	if len(graph.manager.panels) != 0 {
		t.Fatalf("panels = %d, want the destroyed panel removed", len(graph.manager.panels))
	}
	if len(graph.confirmer.prompts) != 0 {
		t.Fatalf("quitting asked a close prompt: %+v", graph.confirmer.prompts)
	}
}

// TestDropdownQuitSequenceClosesThePanelWithoutAVeto replays MyGo v0.6.0's
// prepareQuit order: OnBeforeQuit confirms the quit, then every window is
// closed, then OnWillQuit runs. The Panel must answer its own close rather
// than veto it; a veto aborts prepareQuit after the main window is already
// gone, and OnWillQuit never runs.
func TestDropdownQuitSequenceClosesThePanelWithoutAVeto(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	main := graph.manager.OpenMain()
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	// OnBeforeQuit: no Sessions are open, so the quit is accepted without a
	// prompt.
	if !graph.manager.ConfirmQuit() {
		t.Fatal("ConfirmQuit refused the quit")
	}
	// prepareQuit closes every window before OnWillQuit, main first because it
	// was created first.
	main.Close()
	window.Close()

	if !window.destroyed {
		t.Fatal("the Dropdown Panel vetoed the quit sequence's close")
	}
	if !main.IsDestroyed() {
		t.Fatal("the main window survived the quit sequence")
	}
	if graph.manager.dropdown != nil {
		t.Fatal("the manager still points at a destroyed dropdown")
	}
	if !graph.manager.noWindowsLeft() {
		t.Fatal("the quit sequence left a window the manager still counts")
	}
	if len(graph.confirmer.prompts) != 0 {
		t.Fatalf("the quit sequence asked a close prompt: %+v", graph.confirmer.prompts)
	}
}

// TestDropdownAnswersItsOwnCloseWhileTheQuitClosesOtherWindows pins the close
// hook during a confirmed quit. MyGo closes windows in creation order, so a
// Panel created before the Settings window reaches its close while another
// counted window is still open; the Panel must not veto then either.
func TestDropdownAnswersItsOwnCloseWhileTheQuitClosesOtherWindows(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	graph.manager.OpenMain()
	graph.manager.ToggleDropdown() // created before Settings
	graph.manager.OpenSettings()   // created after the Panel
	window := dropdownWindow(t, graph)

	if !graph.manager.ConfirmQuit() {
		t.Fatal("ConfirmQuit refused the quit")
	}
	graph.manager.Main().Close()
	if window.destroyed {
		t.Fatal("the main window's close destroyed the Panel before MyGo closed it")
	}
	window.Close()

	if !window.destroyed {
		t.Fatal("the Panel vetoed its close while the quit was confirmed")
	}
	if graph.manager.dropdown != nil {
		t.Fatal("the manager still points at a destroyed dropdown")
	}
	if len(graph.confirmer.prompts) != 0 {
		t.Fatalf("the quit sequence asked a close prompt: %+v", graph.confirmer.prompts)
	}
}

// TestClosingTheLastCountedWindowDestroysThePanel proves a created Panel does
// not keep MyGo's window list non-empty after the last counted window closes.
// MyGo triggers its last-window quit only when Windows() is empty, so the
// hidden Panel must be destroyed; otherwise the app keeps running with no
// visible window and OnWillQuit never runs.
func TestClosingTheLastCountedWindowDestroysThePanel(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	graph.manager.OpenMain()
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)
	// Hide it: a live Panel is still in MyGo's window list while hidden.
	graph.manager.ToggleDropdown()

	graph.manager.Main().Close()

	if !window.destroyed {
		t.Fatal("closing the last counted window left the hidden Panel alive")
	}
	if graph.manager.dropdown != nil {
		t.Fatal("the manager still points at a destroyed dropdown")
	}
	if !graph.manager.noWindowsLeft() {
		t.Fatal("the hidden Panel still counts as an open window")
	}
	if !graph.manager.Quitting() {
		t.Fatal("closing the last counted window did not mark the quit")
	}
	if len(graph.confirmer.prompts) != 0 {
		t.Fatalf("closing the only window asked a prompt: %+v", graph.confirmer.prompts)
	}
}

// TestDropdownContentIsBehindTheSharedAppLockGate proves locking hides the
// dropdown's content, and the panel's own frame stays usable so it can still
// be hidden while locked.
func TestDropdownContentIsBehindTheSharedAppLockGate(t *testing.T) {
	var graph *windowGraph
	gate := lockedGate(t, func() {
		if graph != nil {
			graph.manager.InvalidateAll()
		}
	})
	graph = newWindowGraph(t, gate)
	screen := &fakeScreen{cursor: mygo.Point{X: 400, Y: 300}, displays: []mygo.Display{primaryDisplay()}}
	graph.manager.SetScreenProvider(screen)
	graph.manager.SetDropdownContentProvider(func() func(*ui.Context) {
		return func(c *ui.Context) { ui.Text(c, "dropdown-content") }
	})
	graph.manager.ToggleDropdown()
	window := dropdownWindow(t, graph)

	m := locales.In("en")
	tester := render(window)
	if !tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the locked dropdown did not show the gate: %v", tester.Texts())
	}
	if tester.HasText("dropdown-content") {
		t.Fatalf("the locked dropdown rendered its content: %v", tester.Texts())
	}
	if !tester.HasText(m.DropdownHide()) {
		t.Fatalf("the locked dropdown lost its hide button: %v", tester.Texts())
	}

	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !tester.HasText("dropdown-content") {
		t.Fatalf("unlocking did not release the dropdown content: %v", tester.Texts())
	}
}

// TestDropdownAcceleratorsYieldToTheRenderedPanesGrab proves the Panel gates
// its shortcuts on the Pane its routed content shows: a grabbed Quake or
// top-down VNC Pane receives the chord, and the accelerators return when the
// pane releases the grab.
func TestDropdownAcceleratorsYieldToTheRenderedPanesGrab(t *testing.T) {
	graph, _ := newDropdownGraph(t)
	fired := 0
	item := &mygo.MenuItem{
		Label:       "New tab",
		Accelerator: "Cmd+T",
		Click:       func(*mygo.MenuItem, *mygo.Window) { fired++ },
	}
	graph.manager.SetAccelerators([]acceleratorBinding{{mods: ui.Cmd, key: ui.KeyT, item: item}})

	var keys []ui.Key
	graph.manager.SetDropdownContentProvider(func() func(*ui.Context) {
		return func(c *ui.Context) {
			ui.Column(c).Fill().Focusable().AutoFocus().HandleInput(func(event ui.InputEvent) bool {
				if event.Kind == ui.InputKeyDown {
					keys = append(keys, event.Key)
				}
				return true
			})
		}
	})
	grabbed := false
	graph.manager.SetDropdownKeyboardGrabProvider(func() bool { return grabbed })

	tester := ui.NewTester(graph.manager.dropdownView(), 800, 600)
	tester.Frame()
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyT)
	tester.Frame()
	if fired != 1 {
		t.Fatalf("accelerator fired %d times without a grab, want 1", fired)
	}
	if len(keys) != 0 {
		t.Fatalf("the panel's pane saw the ungrabbed chord: %v", keys)
	}

	grabbed = true
	tester.Frame()
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyT)
	tester.Frame()
	if fired != 1 {
		t.Fatalf("accelerator fired while grabbed: %d, want 1", fired)
	}
	if len(keys) != 1 || keys[0] != ui.KeyT {
		t.Fatalf("the grabbed chord reached the pane as %v, want [T]", keys)
	}

	grabbed = false
	tester.Frame()
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyT)
	tester.Frame()
	if fired != 2 {
		t.Fatalf("accelerator did not return after the grab: %d, want 2", fired)
	}
}
