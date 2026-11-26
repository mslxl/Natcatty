package quake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Ticket 21c freezes the Quake Panel's final frame for its applicable Tab
// events under both Interface Styles. It records only what is stable across
// hosts: the bar and body boxes (built from style metrics, not font metrics),
// the shown Surface identity, and the Tab order and selection. A Quake Tab's
// width is measured from its title text, so widths are checked relationally
// (positive, ordered, non-overlapping) instead of being stored, and no pixels
// are compared.
const (
	quakeSnapshotUpdateEnv = "NATCATTY_MOTION_UPDATE_GOLDEN"
	quakeSnapshotDir       = "testdata/motion"
)

type quakeSnapshotRect struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	W float32 `json:"w"`
	H float32 `json:"h"`
}

func quakeSnapshotRectOf(r ui.Rect) quakeSnapshotRect {
	return quakeSnapshotRect{X: r.X, Y: r.Y, W: r.W, H: r.H}
}

// quakeSnapshotTab is one final bar item. Selected is the Tab's active state,
// which changes at once and does not animate; the title is the locale's, not a
// measured width.
type quakeSnapshotTab struct {
	Title    string `json:"title"`
	Selected bool   `json:"selected"`
}

// quakeFinalFrame is one settled Quake Panel.
type quakeFinalFrame struct {
	Bar        quakeSnapshotRect  `json:"bar"`
	Body       quakeSnapshotRect  `json:"body"`
	Surface    string             `json:"surface,omitempty"`
	SurfaceBox quakeSnapshotRect  `json:"surfaceBox"`
	Tabs       []quakeSnapshotTab `json:"tabs"`
}

func quakeFinalTheme() *ui.Theme {
	preset := domaintheme.ResolvePreset(true, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
	return uitheme.UI(true, preset, uitheme.Options{})
}

// quakeSnapshotWindow is one styled Panel with its bar and body runtimes
// exposed, so the test can settle on their real state instead of a clock.
type quakeSnapshotWindow struct {
	fixture *viewFixture
	state   *State
	tt      *ui.Tester
	strip   *quakeStripMotion
	body    *quakeBodyMotion
	message locales.Messages
}

func quakeMotionAllOn() domainpreferences.Motion {
	setting := standardMotion()
	setting.Enabled = true
	setting.TabOpen = true
	setting.TabClose = true
	setting.TabSwitch = true
	setting.TabReorder = true
	return setting
}

// newQuakeSnapshotWindow opens tabs local Quake Tabs and mounts a styled Panel
// over them. Every tab mutation happens before the first frame, so the Panel
// warps the initial events and starts from a resting final frame.
func newQuakeSnapshotWindow(t *testing.T, style kit.Style, tabs int) *quakeSnapshotWindow {
	t.Helper()
	setting := quakeMotionAllOn()
	fixture := newViewFixture(t, WithMotion(func() domainpreferences.Motion { return setting }))
	state := fixture.state
	if err := state.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	for range tabs - 1 {
		if err := state.AddLocalTab(); err != nil {
			t.Fatalf("AddLocalTab: %v", err)
		}
	}
	strip := &quakeStripMotion{}
	body := &quakeBodyMotion{}
	tt := ui.NewTester(styletest.Styled(style, quakeFinalTheme(), motionPanel(state, strip, body)), 800, 400)
	tt.SetLocale("en")
	w := &quakeSnapshotWindow{
		fixture: fixture, state: state, tt: tt, strip: strip, body: body,
		message: locales.In("en"),
	}
	renderUntil(t, tt, "the Quake Tabs and their bodies", func() bool {
		if state.TabCount() != tabs {
			return false
		}
		for index := range tabs {
			tab, ok := state.TabAt(index)
			if !ok || tab.Surface == nil {
				return false
			}
		}
		return true
	})
	w.settle(t)
	return w
}

// settle advances the real runtimes until both are idle and no frame is
// pending. Resting there makes a final frame independent of the clock.
func (w *quakeSnapshotWindow) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.tt.Frame()
		if w.strip.run == nil && w.body.phase == quakeBodyIdle && !w.tt.FramePending() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Quake final frame did not settle: strip %+v body %v pending %v",
				w.strip.run, w.body.phase, w.tt.FramePending())
		}
		w.tt.Advance(16 * time.Millisecond)
	}
}

// capture reads the settled frame. It rejects a Panel that is still showing a
// moving copy, so a capture can never freeze an intermediate frame.
func (w *quakeSnapshotWindow) capture(t *testing.T) quakeFinalFrame {
	t.Helper()
	w.settle(t)
	if w.strip.run != nil || w.body.phase != quakeBodyIdle {
		t.Fatalf("the capture saw a moving Quake Panel: strip %+v body %v", w.strip.run, w.body.phase)
	}
	bar, ok := w.tt.Find(w.message.AppName())
	if !ok {
		t.Fatalf("the Quake bar is missing: %v", w.tt.Texts())
	}
	body, ok := w.tt.Find("quake-body-root")
	if !ok {
		t.Fatalf("the Quake body root is missing: %v", w.tt.Texts())
	}
	frame := quakeFinalFrame{Bar: quakeSnapshotRectOf(bar), Body: quakeSnapshotRectOf(body)}
	active, ok := w.state.Active()
	if !ok {
		t.Fatalf("the Quake workspace has no active Tab")
	}
	if active.Surface == nil {
		t.Fatalf("the active Quake Tab has no Surface")
	}
	surface, ok := active.Surface.(*viewSurface)
	if !ok {
		t.Fatalf("the active Quake Tab surface = %T, want the fixture's view surface", active.Surface)
	}
	frame.Surface = surface.name
	box, ok := w.tt.Find(surface.name)
	if !ok {
		t.Fatalf("the final frame does not show %q: %v", surface.name, w.tt.Texts())
	}
	frame.SurfaceBox = quakeSnapshotRectOf(box)
	frame.Tabs = []quakeSnapshotTab{}
	var previous ui.Rect
	for index, tab := range w.state.Tabs() {
		title := tab.Label
		if title == "" {
			title = w.message.LocalSessionTab(tab.Number)
		}
		rect, ok := w.tt.Find(title)
		if !ok {
			t.Fatalf("the bar is missing the Tab %q: %v", title, w.tt.Texts())
		}
		if rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("the Tab %q has no box: %+v", title, rect)
		}
		if index > 0 && rect.X < previous.X+previous.W-0.5 {
			t.Fatalf("the Tab %q overlaps the one before it: %+v then %+v", title, previous, rect)
		}
		if index > 0 && rect.H != previous.H {
			t.Fatalf("the Tabs %q and the one before differ in height: %v vs %v", title, rect.H, previous.H)
		}
		previous = rect
		frame.Tabs = append(frame.Tabs, quakeSnapshotTab{Title: title, Selected: tab.Active})
	}
	return frame
}

// quakeFrameCase is one applicable Quake event.
type quakeFrameCase struct {
	name string
	tabs int
	act  func(t *testing.T, w *quakeSnapshotWindow)
}

func quakeFrameCases() []quakeFrameCase {
	return []quakeFrameCase{
		{
			name: "tab-open", tabs: 1,
			act: func(t *testing.T, w *quakeSnapshotWindow) {
				if err := w.state.AddLocalTab(); err != nil {
					t.Fatalf("AddLocalTab: %v", err)
				}
				renderUntil(t, w.tt, "the opened Tab", func() bool {
					tab, ok := w.state.TabAt(1)
					return w.state.TabCount() == 2 && ok && tab.Surface != nil
				})
			},
		},
		{
			name: "tab-close-inactive", tabs: 3,
			act: func(t *testing.T, w *quakeSnapshotWindow) {
				if !w.state.CloseTab(0) {
					t.Fatalf("CloseTab(0) returned false")
				}
			},
		},
		{
			name: "tab-close-active", tabs: 3,
			act: func(t *testing.T, w *quakeSnapshotWindow) {
				if !w.state.CloseTab(2) {
					t.Fatalf("CloseTab(2) returned false")
				}
			},
		},
		{
			name: "tab-switch", tabs: 3,
			act: func(t *testing.T, w *quakeSnapshotWindow) {
				w.state.SelectTab(0)
			},
		},
	}
}

// TestQuakeMotionFinalFramesMatchSnapshots is Quake's ticket 21c final-frame
// gate. Each case starts from a resting Panel, drives one Tab event, settles,
// and compares the frozen order, selection, shown Surface and body geometry
// with the recorded fixture for the active Interface Style.
func TestQuakeMotionFinalFramesMatchSnapshots(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		style := kit.CurrentStyle()
		cases := quakeFrameCases()
		got := make(map[string]quakeFinalFrame, len(cases))
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				w := newQuakeSnapshotWindow(t, style, tc.tabs)
				tc.act(t, w)
				got[tc.name] = w.capture(t)
			})
		}
		if t.Failed() {
			return
		}
		checkQuakeSnapshotFile(t, style.ID(), got)
	})
}

func checkQuakeSnapshotFile(t *testing.T, styleID string, got map[string]quakeFinalFrame) {
	t.Helper()
	path := filepath.Join(quakeSnapshotDir, styleID+".json")
	if os.Getenv(quakeSnapshotUpdateEnv) == "1" {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal the %s Quake snapshot: %v", styleID, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create the Quake snapshot directory: %v", err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the %s Quake snapshot (run with %s=1 to record it): %v",
			styleID, quakeSnapshotUpdateEnv, err)
	}
	var want map[string]quakeFinalFrame
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	for name, gotFrame := range got {
		wantFrame, ok := want[name]
		if !ok {
			t.Errorf("%s has no snapshot for %q (run with %s=1 to record it)",
				path, name, quakeSnapshotUpdateEnv)
			continue
		}
		if !reflect.DeepEqual(wantFrame, gotFrame) {
			wantJSON, _ := json.MarshalIndent(wantFrame, "", "  ")
			gotJSON, _ := json.MarshalIndent(gotFrame, "", "  ")
			t.Errorf("Quake final frame %q in %s changed\nwant:\n%s\ngot:\n%s", name, path, wantJSON, gotJSON)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s records Quake final frame %q but the test no longer builds it", path, name)
		}
	}
}
