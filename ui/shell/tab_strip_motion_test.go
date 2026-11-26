package shell

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// tabStripPreferences is ticket 20's default setting: every Tab event on, no
// Reduce Motion, and the standard speed tier. Each case changes what it tests.
func tabStripPreferences() domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = true
	setting.TabOpen = true
	setting.TabClose = true
	setting.TabSwitch = true
	setting.TabReorder = true
	setting.Speed = domainpreferences.MotionStandard
	return setting
}

// newTabStripGraph builds a state over the follow surfaces: light enough to
// drive strip events without a terminal, and able to keep them alive across
// the frames a test builds.
func newTabStripGraph(t *testing.T, setting *domainpreferences.Motion) *followGraph {
	t.Helper()
	service := appsession.NewService(multiFactory{})
	t.Cleanup(service.CloseAll)
	graph := &followGraph{}
	options := []state.Option{
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			surface := &followSurface{text: fmt.Sprintf("strip-terminal-%d", len(graph.surfaces)+1)}
			graph.surfaces = append(graph.surfaces, surface)
			return surface, nil
		}),
	}
	if setting != nil {
		options = append(options, state.WithMotion(func() domainpreferences.Motion { return *setting }))
	}
	graph.state = state.NewState(options...)
	return graph
}

// addStripTabs opens n Tabs and returns their identities in strip order.
func addStripTabs(t *testing.T, s *state.State, n int) []model.TabID {
	t.Helper()
	for range n {
		if err := s.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	ids := make([]model.TabID, n)
	for index := range n {
		id, ok := s.TabID(index)
		if !ok {
			t.Fatalf("no Tab at index %d", index)
		}
		ids[index] = id
	}
	return ids
}

// tabStripProbe drives one strip runtime from a real Context without a Tab
// strip, so a test can read the run it planned. It is the seam between the
// state's event stream and tabStripMotion.
type tabStripProbe struct {
	state   *state.State
	strip   tabStripMotion
	signals tabStripSignals
}

func (p *tabStripProbe) view() func(*ui.Context) {
	return func(c *ui.Context) {
		p.strip.beginFrame(c, p.state, p.signals)
		ui.Column(c).Fill()
	}
}

// newTabStripProbe mounts the probe and consumes whatever event the setup
// left, so the test's own event is the next one it sees.
func newTabStripProbe(t *testing.T, s *state.State) (*tabStripProbe, *ui.Tester) {
	t.Helper()
	p := &tabStripProbe{state: s}
	tt := ui.NewTester(p.view(), 480, 80)
	tt.SetLocale("en")
	tt.Frame()
	return p, tt
}

// stripTravel is an element's animated box on frames after its event; Find
// reports the animated box, intersected with the visible clip.
func stripTravel(t *testing.T, tt *ui.Tester, id model.TabID) ui.Rect {
	t.Helper()
	r, ok := tt.Find(tabStripBoxLabel(id))
	if !ok {
		t.Fatalf("the strip item of Tab %q is not labeled", id)
	}
	return r
}

// stripEvent asserts the latest state event and returns it.
func stripEvent(t *testing.T, s *state.State, kind motion.Kind) motion.Event {
	t.Helper()
	event := mustLatestEvent(t, s)
	if event.Kind != kind {
		t.Fatalf("latest motion event = %v, want %v", event.Kind, kind)
	}
	return event
}

// TestTabStripTransitionsArePositionAndSizeOnly pins the ticket's transition
// shape: Position and Size at 180 ms, a collapsing Enter and Exit, and no
// Colors, so workTabFill's selected fill cannot fade.
func TestTabStripTransitionsArePositionAndSizeOnly(t *testing.T) {
	animated := tabStripTransition(180*time.Millisecond, 180*time.Millisecond)
	if animated.Duration != 180*time.Millisecond || animated.ExitDuration != 180*time.Millisecond {
		t.Fatalf("animated transition = %s/%s, want 180ms/180ms", animated.Duration, animated.ExitDuration)
	}
	if !animated.Position || !animated.Size {
		t.Fatalf("animated transition animates Position=%v Size=%v, want both", animated.Position, animated.Size)
	}
	if animated.Colors || animated.Shadows || animated.Width || animated.Height {
		t.Fatalf("animated transition flags = Colors:%v Shadows:%v Width:%v Height:%v, want none",
			animated.Colors, animated.Shadows, animated.Width, animated.Height)
	}
	if animated.Enter == nil || !animated.Enter.Collapse || animated.Exit == nil || !animated.Exit.Collapse {
		t.Fatalf("animated transition Enter=%+v Exit=%+v, want collapsing both", animated.Enter, animated.Exit)
	}

	passive := tabStripPassiveTransition(120 * time.Millisecond)
	if passive.Duration != -1 {
		t.Fatalf("passive layout duration = %s, want the instant -1", passive.Duration)
	}
	if passive.ExitDuration != 120*time.Millisecond {
		t.Fatalf("passive exit duration = %s, want 120ms", passive.ExitDuration)
	}
	if !passive.Position || passive.Size || passive.Colors || passive.Shadows {
		t.Fatalf("passive flags = Position:%v Size:%v Colors:%v Shadows:%v, want Position alone",
			passive.Position, passive.Size, passive.Colors, passive.Shadows)
	}
	if passive.Exit == nil || !passive.Exit.Collapse {
		t.Fatalf("passive Exit = %+v, want a collapsing exit", passive.Exit)
	}

	warped := tabStripTransition(-1, 180*time.Millisecond)
	if warped.Duration != -1 || warped.ExitDuration != 180*time.Millisecond {
		t.Fatalf("warp transition = %s/%s, want an instant layout with a warm exit", warped.Duration, warped.ExitDuration)
	}
}

// TestTabStripRuntimeGatesEvents reads the run each state event starts: the
// three Tab events animate at 180 ms, a switch warps, and every off switch or
// a hidden window warps without a run.
func TestTabStripRuntimeGatesEvents(t *testing.T) {
	setting := tabStripPreferences()

	t.Run("Tab open animates", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		event := stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run == nil || p.strip.run.kind != motion.TabOpen || p.strip.run.duration != 180*time.Millisecond {
			t.Fatalf("the open run = %+v, want a 180ms TabOpen", p.strip.run)
		}
		if !p.strip.animate || p.strip.warp {
			t.Fatalf("the open frame animate=%v warp=%v, want animate alone", p.strip.animate, p.strip.warp)
		}
		transition, ok := p.strip.transition(setting, false)
		if !ok || transition.Duration != 180*time.Millisecond || transition.Colors {
			t.Fatalf("the open transition = %+v (%v), want 180ms Position/Size only", transition, ok)
		}
		if p.strip.zIndex(event.TabID, false) != 0 {
			t.Fatalf("an opening Tab paints above its siblings")
		}
	})

	t.Run("Tab close animates", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		ids := addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		if !g.state.CloseTab(0) {
			t.Fatalf("CloseTab failed")
		}
		tt.Frame()
		event := stripEvent(t, g.state, motion.TabClose)
		if event.TabID != ids[0] {
			t.Fatalf("the close event names %q, want %q", event.TabID, ids[0])
		}
		if p.strip.run == nil || p.strip.run.kind != motion.TabClose || p.strip.run.duration != 180*time.Millisecond {
			t.Fatalf("the close run = %+v, want a 180ms TabClose", p.strip.run)
		}
	})

	t.Run("Tab reorder animates and paints the moved item above", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		ids := addStripTabs(t, g.state, 3)
		p, tt := newTabStripProbe(t, g.state)
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Frame()
		event := stripEvent(t, g.state, motion.TabReorder)
		if event.TabID != ids[0] || event.TargetTabID != ids[2] {
			t.Fatalf("the reorder event = %+v, want %q after %q", event, ids[0], ids[2])
		}
		if p.strip.run == nil || p.strip.run.kind != motion.TabReorder || p.strip.run.duration != 180*time.Millisecond {
			t.Fatalf("the reorder run = %+v, want a 180ms TabReorder", p.strip.run)
		}
		if got := p.strip.zIndex(ids[0], false); got != 1 {
			t.Fatalf("the moved Tab's z-index = %d, want 1", got)
		}
		if got := p.strip.zIndex(ids[1], false); got != 0 {
			t.Fatalf("a still Tab's z-index = %d, want 0", got)
		}
		if got := p.strip.zIndex(ids[1], true); got != 1 {
			t.Fatalf("the dragged Tab's z-index = %d, want 1", got)
		}
		// Under the pointer the item's layout is instant, while the copy's
		// exit keeps the run's duration.
		passive, ok := p.strip.transition(setting, true)
		if !ok || passive.Duration != -1 || passive.ExitDuration != 180*time.Millisecond || passive.Colors {
			t.Fatalf("the dragged transition = %+v (%v), want an instant layout with a 180ms exit", passive, ok)
		}
	})

	t.Run("an idle Tab keeps the warm close exit", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, _ := newTabStripProbe(t, g.state)
		if p.strip.run != nil || p.strip.animate || p.strip.warp {
			t.Fatalf("an idle strip is run=%+v animate=%v warp=%v, want rest",
				p.strip.run, p.strip.animate, p.strip.warp)
		}
		transition, ok := p.strip.transition(setting, false)
		if !ok {
			t.Fatalf("an idle Tab carries no transition, so a close copy could not use its exit")
		}
		if transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the idle transition = %s/exit %s, want an instant layout with a 180ms exit",
				transition.Duration, transition.ExitDuration)
		}
		if transition.Colors || transition.Exit == nil || !transition.Exit.Collapse {
			t.Fatalf("the idle transition = %+v, want a collapsing exit and no Colors", transition)
		}
		// A strip that starts with every switch off still records the warm
		// exit: a switch turned on in the same update as a close must find a
		// usable exit record from the previous off build (ticket 20).
		off := setting
		off.TabClose = false
		g2 := newTabStripGraph(t, &off)
		addStripTabs(t, g2.state, 2)
		p2, _ := newTabStripProbe(t, g2.state)
		transition, ok = p2.strip.transition(off, false)
		if !ok || transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the fresh off build attached %+v (%v), want the instant layout with a warm 180ms exit",
				transition, ok)
		}
		off.Enabled = false
		transition, ok = p2.strip.transition(off, false)
		if !ok || transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the master-off build attached %+v (%v), want the instant layout with a warm 180ms exit",
				transition, ok)
		}
	})

	t.Run("Tab switch warps", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 3)
		p, tt := newTabStripProbe(t, g.state)
		g.state.SelectTab(0)
		tt.Frame()
		stripEvent(t, g.state, motion.TabSwitch)
		if p.strip.run != nil || p.strip.animate || !p.strip.warp {
			t.Fatalf("a switch left run=%+v animate=%v warp=%v, want a warp alone",
				p.strip.run, p.strip.animate, p.strip.warp)
		}
		transition, ok := p.strip.transition(setting, false)
		if !ok || transition.Duration != -1 || transition.ExitDuration != 180*time.Millisecond {
			t.Fatalf("the switch transition = %+v (%v), want an instant layout that still arms the 180ms close exit",
				transition, ok)
		}
		if tt.FramePending() {
			t.Fatalf("the armed close record asked for frames on the warp itself")
		}
	})

	t.Run("Tab switch cancels an earlier strip run", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		ids := addStripTabs(t, g.state, 3)
		p, tt := newTabStripProbe(t, g.state)
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Frame()
		if p.strip.run == nil {
			t.Fatalf("the reorder did not start a run")
		}
		g.state.SelectTab(0)
		tt.Frame()
		if p.strip.run != nil || !p.strip.warp {
			t.Fatalf("a switch during a reorder left run=%+v warp=%v, want the run gone and a warp", p.strip.run, p.strip.warp)
		}
	})

	offCases := []struct {
		name   string
		change func(*domainpreferences.Motion)
		event  func(t *testing.T, g *followGraph, ids []model.TabID)
		kind   motion.Kind
	}{
		{
			name:   "master off",
			change: func(m *domainpreferences.Motion) { m.Enabled = false },
			event: func(t *testing.T, g *followGraph, _ []model.TabID) {
				if err := g.state.NewTab(); err != nil {
					t.Fatal(err)
				}
			},
			kind: motion.TabOpen,
		},
		{
			name:   "Tab open off",
			change: func(m *domainpreferences.Motion) { m.TabOpen = false },
			event: func(t *testing.T, g *followGraph, _ []model.TabID) {
				if err := g.state.NewTab(); err != nil {
					t.Fatal(err)
				}
			},
			kind: motion.TabOpen,
		},
		{
			name:   "Tab close off",
			change: func(m *domainpreferences.Motion) { m.TabClose = false },
			event: func(t *testing.T, g *followGraph, _ []model.TabID) {
				if !g.state.CloseTab(0) {
					t.Fatalf("CloseTab failed")
				}
			},
			kind: motion.TabClose,
		},
		{
			name:   "Tab reorder off",
			change: func(m *domainpreferences.Motion) { m.TabReorder = false },
			event: func(t *testing.T, g *followGraph, ids []model.TabID) {
				if !g.state.MoveTab(ids[0], ids[2], true) {
					t.Fatalf("MoveTab failed")
				}
			},
			kind: motion.TabReorder,
		},
	}
	for _, tc := range offCases {
		t.Run(tc.name, func(t *testing.T) {
			live := setting
			tc.change(&live)
			g := newTabStripGraph(t, &live)
			ids := addStripTabs(t, g.state, 3)
			p, tt := newTabStripProbe(t, g.state)
			tc.event(t, g, ids)
			tt.Frame()
			stripEvent(t, g.state, tc.kind)
			if p.strip.run != nil || p.strip.animate || !p.strip.warp {
				t.Fatalf("%s left run=%+v animate=%v warp=%v, want a warp with no run",
					tc.name, p.strip.run, p.strip.animate, p.strip.warp)
			}
			transition, _ := p.strip.transition(live, false)
			// The warp shows this frame's layout at once, and it still records
			// the warm close exit whatever the switches say: a switch turned
			// on in the next update with a close must find a usable record
			// there. The removal guard suppresses the record on any frame that
			// would otherwise paint it as a ghost (ticket 20).
			wantExit := 180 * time.Millisecond
			if transition.Duration != -1 || transition.ExitDuration != wantExit {
				t.Fatalf("%s transition = %s/%s, want -1/%s",
					tc.name, transition.Duration, transition.ExitDuration, wantExit)
			}
		})
	}

	t.Run("Reduce Motion warps", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run != nil || !p.strip.warp {
			t.Fatalf("Reduce Motion left run=%+v warp=%v, want a warp", p.strip.run, p.strip.warp)
		}
		if p.strip.exit {
			t.Fatalf("Reduce Motion left an exit transition armed")
		}
	})

	t.Run("hidden window warps", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		tt.SetWindowHidden(true)
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run != nil || !p.strip.warp {
			t.Fatalf("a hidden window left run=%+v warp=%v, want a warp", p.strip.run, p.strip.warp)
		}
	})

	t.Run("shown window warps an event it missed", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		tt.SetWindowHidden(true)
		tt.Frame()
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.SetWindowHidden(false)
		stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run != nil || !p.strip.warp {
			t.Fatalf("an event missed while hidden left run=%+v warp=%v, want a warp", p.strip.run, p.strip.warp)
		}
	})

	t.Run("an overflowing reveal warps", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		p.signals.revealActive, p.signals.overflow = true, true
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run != nil || !p.strip.warp {
			t.Fatalf("a reveal over an overflowing strip left run=%+v warp=%v, want a warp", p.strip.run, p.strip.warp)
		}
	})

	t.Run("a reveal that does not overflow still animates", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		p, tt := newTabStripProbe(t, g.state)
		p.signals.revealActive, p.signals.overflow = true, false
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run == nil || p.strip.run.kind != motion.TabOpen {
			t.Fatalf("a reveal that fits left run=%+v, want the open run", p.strip.run)
		}
	})

	t.Run("first frame warps", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		addStripTabs(t, g.state, 2)
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		p := &tabStripProbe{state: g.state}
		ui.NewTester(p.view(), 480, 80)
		stripEvent(t, g.state, motion.TabOpen)
		if p.strip.run != nil || !p.strip.warp {
			t.Fatalf("the first frame left run=%+v warp=%v, want a warp", p.strip.run, p.strip.warp)
		}
	})
}

// TestTabStripDurationsScaleWithSpeed checks the 180 ms base for all three Tab
// events at each speed tier: 270 ms slow, 180 ms standard, 108 ms fast.
func TestTabStripDurationsScaleWithSpeed(t *testing.T) {
	cases := []struct {
		name  string
		speed domainpreferences.MotionSpeed
		want  time.Duration
	}{
		{"slow", domainpreferences.MotionSlow, 270 * time.Millisecond},
		{"standard", domainpreferences.MotionStandard, 180 * time.Millisecond},
		{"fast", domainpreferences.MotionFast, 108 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := tabStripPreferences()
			setting.Speed = tc.speed
			g := newTabStripGraph(t, &setting)
			addStripTabs(t, g.state, 2)
			p, tt := newTabStripProbe(t, g.state)

			if transition, ok := p.strip.transition(setting, false); !ok ||
				transition.Duration != -1 || transition.ExitDuration != tc.want {
				t.Fatalf("the idle close exit = %+v (%v), want %s", transition, ok, tc.want)
			}

			if err := g.state.NewTab(); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if p.strip.run == nil || p.strip.run.kind != motion.TabOpen || p.strip.run.duration != tc.want {
				t.Fatalf("the open run = %+v, want %s", p.strip.run, tc.want)
			}
			if !g.state.CloseTab(0) {
				t.Fatalf("CloseTab failed")
			}
			tt.Frame()
			if p.strip.run == nil || p.strip.run.kind != motion.TabClose || p.strip.run.duration != tc.want {
				t.Fatalf("the close run = %+v, want %s", p.strip.run, tc.want)
			}
			first, _ := g.state.TabID(0)
			last, _ := g.state.TabID(g.state.TabCount() - 1)
			if !g.state.MoveTab(first, last, true) {
				t.Fatalf("MoveTab failed")
			}
			tt.Frame()
			if p.strip.run == nil || p.strip.run.kind != motion.TabReorder || p.strip.run.duration != tc.want {
				t.Fatalf("the reorder run = %+v, want %s", p.strip.run, tc.want)
			}
		})
	}
}

// tabStripWindowN is tabBodyWindowIn at a chosen window size. The ordinary
// strip tests use a window wide enough that the strip does not scroll, so a
// position change can only be the transition; the overflow test uses the main
// window's minimum.
func tabStripWindowN(t *testing.T, setting domainpreferences.Motion, n, width, height int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion) {
	t.Helper()
	g, tt, wm, _ := tabStripWindowWithReveal(t, setting, n, width, height)
	return g, tt, wm
}

// tabStripWindowWithReveal is tabStripWindowN with the strip's runtime
// exposed through the test seam, so a case can read its run, generation and
// previous-frame boxes.
func tabStripWindowWithReveal(t *testing.T, setting domainpreferences.Motion, n, width, height int) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion, *workTabReveal) {
	t.Helper()
	g := motionGraph(t, setting)
	for range n {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, n)
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(view, width, height)
	tt.SetLocale("en")
	settlePane(t, tt, g, n-1)
	var all []*realTerminalSurface
	for index := range n {
		all = append(all, paneSurfaces(t, g, index)...)
	}
	for index := range n {
		g.state.SelectTab(index)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
	}
	pumpUntil(t, tt, "every Tab body's final grid", func() bool { return gridsSettled(all...) })
	return g, tt, wm, reveal
}

// steppingStripView builds view and then asks for the next frame itself, so
// Tester.Advance runs exactly the builds its duration reaches instead of
// settle's up-to-20 builds at one still clock. The event paint is the one
// frame a real window shows before its next refresh; a defect there must not
// be hidden by a second build at the same time (ticket 20's wide first
// overflow). The Tester also keeps its duplicate-key check on, so every build
// this harness makes proves the keyed row and items stay unique.
func steppingStripView(view func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		view(c)
		c.Invalidate()
	}
}

// steppedStripGraph opens n Tabs, runs prepare (which may rename them), and
// mounts the real shell at width×height with a stepping view. It advances to
// rest one build at a time before returning the graph, its Tester, and the
// injected strip reveal.
func steppedStripGraph(t *testing.T, setting domainpreferences.Motion, n, width, height int,
	prepare func(t *testing.T, g *realTerminalGraph)) (*realTerminalGraph, *ui.Tester, *workTabReveal) {
	t.Helper()
	g := motionGraph(t, setting)
	for range n {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, n)
	if prepare != nil {
		prepare(t, g)
	}
	reveal := newWorkTabReveal()
	view, _ := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(steppingStripView(view), width, height)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	return g, tt, reveal
}

// TestTabStripOpenEntersAndWidens opens a Tab at a wide window: the new item
// starts collapsed, widens over the 180 ms, and ends at the strip's final
// layout.
func TestTabStripOpenEntersAndWidens(t *testing.T) {
	g, tt, _ := tabStripWindowN(t, tabStripPreferences(), 3, 1600, 600)
	first := stripTravel(t, tt, g.mustTabID(t, 0))
	last := stripTravel(t, tt, g.mustTabID(t, 2))

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 4)
	opened := g.mustTabID(t, 3)
	tt.Frame()
	stripEvent(t, g.state, motion.TabOpen)

	start := stripTravel(t, tt, opened)
	if start.W >= last.W/2 {
		t.Fatalf("the opening Tab starts at width %v, want it collapsed under %v", start.W, last.W/2)
	}
	tt.Advance(90 * time.Millisecond)
	mid := stripTravel(t, tt, opened)
	if mid.W <= start.W+1 || mid.W >= last.W-1 {
		t.Fatalf("the opening Tab is %v wide mid-run, want between %v and %v", mid.W, start.W, last.W)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	final := stripTravel(t, tt, opened)
	if abs32(final.W-last.W) > 1 {
		t.Fatalf("the final opening Tab is %v wide, want the strip's %v", final.W, last.W)
	}
	if abs32(stripTravel(t, tt, g.mustTabID(t, 0)).X-first.X) > 1 {
		t.Fatalf("the opening Tab moved an earlier Tab's final position")
	}
}

// TestTabStripCloseOfABackgroundTabMakesRoomAndLeavesTheBody closes a Tab
// that is not current: the strip animates the room and ticket 19's body stays
// idle.
func TestTabStripCloseOfABackgroundTabMakesRoomAndLeavesTheBody(t *testing.T) {
	g, tt, wm := tabStripWindowN(t, tabStripPreferences(), 4, 1600, 600)
	ids := []model.TabID{
		g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3),
	}
	beforeA := stripTravel(t, tt, ids[0])
	beforeB := stripTravel(t, tt, ids[1])
	frames := wm.body.animationFrames

	if !g.state.CloseTab(0) {
		t.Fatalf("CloseTab failed")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabClose)
	if wm.body.phase != bodyIdle {
		t.Fatalf("a background close started a body run: %v", wm.body.phase)
	}
	if wm.body.animationFrames != frames {
		t.Fatalf("a background close asked for %d body frames", wm.body.animationFrames-frames)
	}
	if _, ok := tt.Find(tabStripBoxLabel(ids[0])); ok {
		t.Fatalf("the closed Tab's strip copy is still labeled")
	}
	if start := stripTravel(t, tt, ids[1]); !rectNearOK(start, beforeB, 1) {
		t.Fatalf("the next Tab starts at %+v, want its old place %+v", start, beforeB)
	}
	tt.Advance(90 * time.Millisecond)
	mid := stripTravel(t, tt, ids[1])
	if mid.X >= beforeB.X-1 || mid.X <= beforeA.X+1 {
		t.Fatalf("the next Tab is at x=%v mid-run, want between %v and %v", mid.X, beforeA.X, beforeB.X)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if final := stripTravel(t, tt, ids[1]); abs32(final.X-beforeA.X) > 1 {
		t.Fatalf("the next Tab ends at x=%v, want the closed Tab's place %v", final.X, beforeA.X)
	}
}

// diff8 is the distance between two channel values.
func diff8(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// countTabFill counts the pixels along a Tab's bottom fill row that are not
// fill. An exit copy collapses and fades toward an edge, so its remaining
// paint is a shrinking, fading run rather than a fixed color at one point.
func countTabFill(tt *ui.Tester, box ui.Rect, fill [3]uint8) int {
	img := tt.Image()
	y := int(box.Y + box.H - 3)
	count := 0
	for x := int(box.X) + 1; x < int(box.X+box.W)-1; x++ {
		c := img.RGBAAt(x, y)
		if diff8(c.R, fill[0]) >= 2 || diff8(c.G, fill[1]) >= 2 || diff8(c.B, fill[2]) >= 2 {
			count++
		}
	}
	return count
}

// TestTabStripCloseExitAnimates proves the closed Tab itself leaves over the
// strip's duration: its selected fill still covers its place on the event
// frame, shrinks over the run, and is gone once the 180 ms ends. Closing the
// current Tab gives the copy the selected fill, which the bar's fill cannot be
// mistaken for.
func TestTabStripCloseExitAnimates(t *testing.T) {
	g, tt, _ := tabStripWindowN(t, tabStripPreferences(), 3, 1600, 600)
	closed := g.mustTabID(t, 2)
	box := stripTravel(t, tt, closed)
	active := tabFillPixel(tt, box)
	viewport, ok := tt.Find(locales.In("en").WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	r8, g8, b8 := samplePixel(tt, viewport.X+viewport.W-150, box.Y+box.H-3)
	bar := [3]uint8{r8, g8, b8}
	if active == bar {
		t.Fatalf("the selected Tab fill and the bar fill are both %v", active)
	}
	if got := countTabFill(tt, box, bar); got < int(box.W/2) {
		t.Fatalf("the selected Tab covers %d fill pixels of its %v box", got, box.W)
	}

	if !g.state.CloseActiveTab() {
		t.Fatalf("CloseActiveTab failed")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabClose)
	start := countTabFill(tt, box, bar)
	if start < int(box.W/2) {
		t.Fatalf("the closing Tab has no exit copy on its event frame: %d fill pixels", start)
	}
	tt.Advance(30 * time.Millisecond)
	mid := countTabFill(tt, box, bar)
	if mid <= 0 || mid >= start {
		t.Fatalf("the exit copy paints %d pixels mid-run, want fewer than %d and more than 0", mid, start)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if got := countTabFill(tt, box, bar); got != 0 {
		t.Fatalf("the closed Tab's exit did not clear: %d painted pixels remain", got)
	}
	if got := tabFillPixel(tt, box); got != bar {
		t.Fatalf("the closed Tab's place = %v after the run, want the bar's %v", got, bar)
	}
}

// TestTabStripReorderSlidesTheMovedTabAndLeavesTheBody moves a Tab after the
// last one: the strip's items slide, the moved Tab paints above (asserted in
// the runtime test), and the body does not move.
func TestTabStripReorderSlidesTheMovedTabAndLeavesTheBody(t *testing.T) {
	g, tt, wm := tabStripWindowN(t, tabStripPreferences(), 3, 1600, 600)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
	beforeA := stripTravel(t, tt, ids[0])
	beforeB := stripTravel(t, tt, ids[1])
	beforeC := stripTravel(t, tt, ids[2])
	frames := wm.body.animationFrames

	if !g.state.MoveTab(ids[0], ids[2], true) {
		t.Fatalf("MoveTab failed")
	}
	tt.Frame()
	event := stripEvent(t, g.state, motion.TabReorder)
	if event.TabID != ids[0] || event.TargetTabID != ids[2] {
		t.Fatalf("the reorder event = %+v, want %q after %q", event, ids[0], ids[2])
	}
	if wm.body.phase != bodyIdle || wm.body.animationFrames != frames {
		t.Fatalf("a strip reorder moved the body: phase %v, +%d frames",
			wm.body.phase, wm.body.animationFrames-frames)
	}
	if start := stripTravel(t, tt, ids[0]); !rectNearOK(start, beforeA, 1) {
		t.Fatalf("the moved Tab starts at %+v, want its old place %+v", start, beforeA)
	}
	tt.Advance(90 * time.Millisecond)
	mid := stripTravel(t, tt, ids[0])
	if mid.X <= beforeA.X+1 || mid.X >= beforeC.X-1 {
		t.Fatalf("the moved Tab is at x=%v mid-run, want between %v and %v", mid.X, beforeA.X, beforeC.X)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if final := stripTravel(t, tt, ids[0]); abs32(final.X-beforeC.X) > 1 {
		t.Fatalf("the moved Tab ends at x=%v, want the last place %v", final.X, beforeC.X)
	}
	if final := stripTravel(t, tt, ids[1]); abs32(final.X-beforeA.X) > 1 {
		t.Fatalf("the second Tab ends at x=%v, want the first place %v", final.X, beforeA.X)
	}
	if final := stripTravel(t, tt, ids[2]); abs32(final.X-beforeB.X) > 1 {
		t.Fatalf("the third Tab ends at x=%v, want the second place %v", final.X, beforeB.X)
	}
}

// tabFillPixel samples a Tab's fill below its title, where no glyph or control
// box covers the fill.
func tabFillPixel(tt *ui.Tester, r ui.Rect) [3]uint8 {
	r8, g8, b8 := samplePixel(tt, r.X+r.W/2, r.Y+r.H-3)
	return [3]uint8{r8, g8, b8}
}

// TestTabStripSwitchKeepsGeometryAndChangesTheFillAtOnce switches Tabs: no
// strip item moves, and the selected fill swaps in the same frame, with no
// color transition to fade it.
func TestTabStripSwitchKeepsGeometryAndChangesTheFillAtOnce(t *testing.T) {
	g, tt, _ := tabStripWindowN(t, tabStripPreferences(), 3, 1600, 600)
	ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
	boxes := []ui.Rect{stripTravel(t, tt, ids[0]), stripTravel(t, tt, ids[1]), stripTravel(t, tt, ids[2])}
	activeFill := func() [3]uint8 { return tabFillPixel(tt, boxes[2]) }
	idleFill := func() [3]uint8 { return tabFillPixel(tt, boxes[0]) }
	active, idle := activeFill(), idleFill()
	if active == idle {
		t.Fatalf("the active and idle Tab fills are both %v; the theme cannot show a selection", active)
	}

	g.state.SelectTab(0)
	tt.Frame()
	stripEvent(t, g.state, motion.TabSwitch)
	for index, id := range ids {
		if got := stripTravel(t, tt, id); !rectNearOK(got, boxes[index], 1) {
			t.Fatalf("the switch moved Tab %q from %+v to %+v", id, boxes[index], got)
		}
	}
	if got := tabFillPixel(tt, boxes[0]); got != active {
		t.Fatalf("the newly selected Tab fill = %v on the switch frame, want %v at once", got, active)
	}
	if got := tabFillPixel(tt, boxes[2]); got != idle {
		t.Fatalf("the deselected Tab fill = %v on the switch frame, want %v at once", got, idle)
	}
	tt.Advance(60 * time.Millisecond)
	if got := tabFillPixel(tt, boxes[0]); got != active {
		t.Fatalf("the selected fill faded to %v mid-frame, want %v", got, active)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	for index, id := range ids {
		if got := stripTravel(t, tt, id); !rectNearOK(got, boxes[index], 1) {
			t.Fatalf("the settled switch moved Tab %q from %+v to %+v", id, boxes[index], got)
		}
	}
}

// TestTabStripOverflowRevealWarpsAtMinimumWidth opens a Tab at the main
// window's minimum width with the strip overflowing: the reveal and the
// transition would fight, so the strip warps to the final layout and the new
// Tab is scrolled into view on that frame.
func TestTabStripOverflowRevealWarpsAtMinimumWidth(t *testing.T) {
	g := motionGraph(t, tabStripPreferences())
	for range 6 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForRealSurfaces(t, g.state, 6)
	view, _ := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 720, 480)
	tt.SetLocale("en")
	tt.Frame()
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 7)
	opened := g.mustTabID(t, 6)
	tt.Frame()
	stripEvent(t, g.state, motion.TabOpen)
	start := stripTravel(t, tt, opened)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	final := stripTravel(t, tt, opened)
	if !rectNearOK(start, final, 1) {
		t.Fatalf("the overflowing open animated from %+v to %+v, want the final layout at once", start, final)
	}
	if start.X < viewport.X-1 || start.X+start.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v", start, viewport)
	}
}

// followTabID names a follow state's Tab, whose title is what the mini strip
// labels its items with.
func followTabID(t *testing.T, s *state.State, index int) model.TabID {
	t.Helper()
	id, ok := s.TabID(index)
	if !ok {
		t.Fatalf("no Tab at index %d", index)
	}
	return id
}

// followStripFrame mounts only the mini strip over a follow state.
func followStripFrame(t *testing.T, graph *followGraph, titles ...string) *ui.Tester {
	t.Helper()
	if len(titles) != graph.state.TabCount() {
		t.Fatalf("%d titles for %d Tabs", len(titles), graph.state.TabCount())
	}
	for index, title := range titles {
		pane, ok := graph.state.FocusedPaneID(index)
		if !ok {
			t.Fatalf("Tab %d has no focused Pane", index)
		}
		if !graph.state.RenamePane(index, pane, title) {
			t.Fatalf("renaming Tab %d's Pane failed", index)
		}
	}
	tt := ui.NewTester(FollowBar(graph.state), 600, 120)
	tt.SetLocale("en")
	tt.Frame()
	return tt
}

// followStripRect finds a mini Tab item by its Pane title; the pane Tab labels
// its whole box with the title.
func followStripRect(t *testing.T, tt *ui.Tester, title string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(title)
	if !ok {
		t.Fatalf("the mini strip has no item %q", title)
	}
	return r
}

// followFill samples a mini Tab's fill below its title, where no glyph,
// control or underline covers it.
func followFill(tt *ui.Tester, r ui.Rect) [3]uint8 {
	r8, g8, b8 := samplePixel(tt, r.X+r.W/2, r.Y+r.H/2)
	return [3]uint8{r8, g8, b8}
}

// TestFollowBarStripMotion checks the Panel's mini strip follows the strip's
// rules at its own size: an open slides a full-width item into place, a close
// slides its copy out, a reorder slides, a switch moves nothing, and the
// selected fill changes in the same frame. It never animates Size (ticket 20,
// Follow consistency).
func TestFollowBarStripMotion(t *testing.T) {
	graph := newFollowGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.waitForSurfaces(t, 3)
	titles := []string{"follow-strip-a", "follow-strip-b", "follow-strip-c"}
	tt := followStripFrame(t, graph, titles...)
	ids := []model.TabID{followTabID(t, graph.state, 0), followTabID(t, graph.state, 1), followTabID(t, graph.state, 2)}
	beforeA := followStripRect(t, tt, titles[0])
	beforeB := followStripRect(t, tt, titles[1])
	beforeC := followStripRect(t, tt, titles[2])

	if !graph.state.MoveTab(ids[0], ids[2], true) {
		t.Fatalf("MoveTab failed")
	}
	tt.Frame()
	stripEvent(t, graph.state, motion.TabReorder)
	if start := followStripRect(t, tt, titles[0]); !rectNearOK(start, beforeA, 1) {
		t.Fatalf("the mini strip's moved Tab starts at %+v, want its old place %+v", start, beforeA)
	}
	tt.Advance(90 * time.Millisecond)
	if mid := followStripRect(t, tt, titles[0]); mid.X <= beforeA.X+1 || mid.X >= beforeC.X-1 {
		t.Fatalf("the mini strip's moved Tab is at x=%v mid-run, want between %v and %v", mid.X, beforeA.X, beforeC.X)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if final := followStripRect(t, tt, titles[0]); abs32(final.X-beforeC.X) > 1 {
		t.Fatalf("the mini strip's moved Tab ends at x=%v, want the last place %v", final.X, beforeC.X)
	}

	// A switch changes no mini strip geometry.
	switched := []ui.Rect{
		followStripRect(t, tt, titles[0]), followStripRect(t, tt, titles[1]), followStripRect(t, tt, titles[2]),
	}
	graph.state.SelectTab(0)
	tt.Frame()
	stripEvent(t, graph.state, motion.TabSwitch)
	for index, title := range titles {
		if got := followStripRect(t, tt, title); !rectNearOK(got, switched[index], 1) {
			t.Fatalf("the mini strip's switch moved %q from %+v to %+v", title, switched[index], got)
		}
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	// A new Tab takes its final width at once and slides in from the right.
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.waitForSurfaces(t, 4)
	const openedTitle = "follow-strip-d"
	pane, ok := graph.state.FocusedPaneID(3)
	if !ok || !graph.state.RenamePane(3, pane, openedTitle) {
		t.Fatalf("renaming the opening Tab's Pane failed")
	}
	tt.Frame()
	stripEvent(t, graph.state, motion.TabOpen)
	start := followStripRect(t, tt, openedTitle)
	if abs32(start.W-beforeA.W) > 1 {
		t.Fatalf("the mini strip's opening Tab starts at width %v, want its final %v: Follow animates Position only", start.W, beforeA.W)
	}
	tt.Advance(90 * time.Millisecond)
	mid := followStripRect(t, tt, openedTitle)
	if abs32(mid.W-beforeA.W) > 1 {
		t.Fatalf("the mini strip's opening Tab is %v wide mid-run, want its final %v throughout", mid.W, beforeA.W)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	final := followStripRect(t, tt, openedTitle)
	if abs32(final.W-beforeB.W) > 1 {
		t.Fatalf("the mini strip's opening Tab ends at width %v, want the strip's %v", final.W, beforeB.W)
	}
	if start.X <= final.X+1 || mid.X <= final.X+1 || mid.X >= start.X-1 {
		t.Fatalf("the opening Tab slid from x=%v through x=%v to x=%v, want a right-to-left Position-only slide",
			start.X, mid.X, final.X)
	}

	// Closing a non-current mini Tab slides the remaining items and never
	// collapses their widths. The order is [b, c, a, d] here, so closing b
	// moves c to the start.
	bBox := followStripRect(t, tt, titles[1])
	cBox := followStripRect(t, tt, titles[2])
	if !graph.state.CloseTab(0) {
		t.Fatalf("CloseTab failed")
	}
	tt.Frame()
	stripEvent(t, graph.state, motion.TabClose)
	if _, ok := tt.Find(titles[1]); ok {
		t.Fatalf("the closed mini Tab still has a label")
	}
	if start := followStripRect(t, tt, titles[2]); !rectNearOK(start, cBox, 1) {
		t.Fatalf("the mini strip's next Tab starts at %+v, want its old place %+v", start, cBox)
	}
	tt.Advance(90 * time.Millisecond)
	if mid := followStripRect(t, tt, titles[2]); mid.X <= bBox.X+1 || mid.X >= cBox.X-1 {
		t.Fatalf("the mini strip's remaining Tab is at x=%v mid-close, want between %v and %v",
			mid.X, bBox.X, cBox.X)
	}
	if got := followStripRect(t, tt, titles[2]).W; abs32(got-beforeA.W) > 1 {
		t.Fatalf("the mini strip's remaining Tab is %v wide mid-close, want %v", got, beforeA.W)
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if got := followStripRect(t, tt, titles[2]); abs32(got.X-bBox.X) > 1 {
		t.Fatalf("the mini strip's remaining Tab ends at x=%v, want %v", got.X, bBox.X)
	}
	if got := followStripRect(t, tt, titles[2]).W; abs32(got-beforeA.W) > 1 {
		t.Fatalf("the mini strip's remaining Tab ends %v wide, want %v", got, beforeA.W)
	}

	// A switch changes the selected fill in the same frame and never fades
	// it: the mini strip has no Colors transition.
	boxes := []ui.Rect{
		followStripRect(t, tt, titles[2]), followStripRect(t, tt, titles[0]), followStripRect(t, tt, openedTitle),
	}
	activeFill := followFill(tt, boxes[2])
	idleFill := followFill(tt, boxes[0])
	if activeFill == idleFill {
		t.Fatalf("the active and idle mini fills are both %v; the theme cannot show a selection", activeFill)
	}
	graph.state.SelectTab(0)
	tt.Frame()
	stripEvent(t, graph.state, motion.TabSwitch)
	if got := followFill(tt, boxes[0]); got != activeFill {
		t.Fatalf("the newly selected mini fill = %v on the switch frame, want %v at once", got, activeFill)
	}
	if got := followFill(tt, boxes[2]); got != idleFill {
		t.Fatalf("the deselected mini fill = %v on the switch frame, want %v at once", got, idleFill)
	}
	tt.Advance(60 * time.Millisecond)
	if got := followFill(tt, boxes[0]); got != activeFill {
		t.Fatalf("the mini fill faded to %v mid-frame, want %v", got, activeFill)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()

	// Closing the active mini Tab keeps its copy at full width as it leaves;
	// once the run ends, the place is the bar's again. The order is now
	// [c, a, d], so d's old place is past the remaining items.
	graph.state.SelectTab(2)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	closedBox := followStripRect(t, tt, openedTitle)
	if !graph.state.CloseTab(2) {
		t.Fatalf("CloseTab failed")
	}
	tt.Frame()
	stripEvent(t, graph.state, motion.TabClose)
	if got := followFill(tt, closedBox); got != activeFill {
		t.Fatalf("the closing mini Tab's copy = %v on its event frame, want the selected %v", got, activeFill)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if got := followFill(tt, closedBox); got == activeFill {
		t.Fatalf("the closing mini Tab's copy still paints %v after its run", got)
	}
}

// stripBarFill samples the Tab bar beside a Tab, the fill a Tab's exit copy
// would cover on its way out.
func stripBarFill(tt *ui.Tester, viewport, box ui.Rect) [3]uint8 {
	r8, g8, b8 := samplePixel(tt, viewport.X+viewport.W-40, box.Y+box.H-3)
	return [3]uint8{r8, g8, b8}
}

// TestTabStripBatchCloseWarps proves a batch close (CloseOtherTabs, or a
// CloseTabsRight closing several) warps the strip too: the final layout shows
// on the event frame, no removed Tab paints a per-Tab exit, the body stays
// idle, and the strip asks for no animation frames. It also covers the batch
// arriving while a strip exit or reorder is in flight: the old copy and the
// in-flight movement stop with it.
func TestTabStripBatchCloseWarps(t *testing.T) {
	message := locales.In("en")
	checkWarp := func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, wm *workspacePaneMotion,
		reveal *workTabReveal, closedBox ui.Rect, remaining model.TabID, stripFramesBefore, bodyFramesBefore int) {
		t.Helper()
		if reveal.strip.run != nil {
			t.Fatalf("a batch close started a strip run: %+v", reveal.strip.run)
		}
		if reveal.strip.animationFrames != stripFramesBefore {
			t.Fatalf("a batch close asked for %d strip frames", reveal.strip.animationFrames-stripFramesBefore)
		}
		if wm.body.phase != bodyIdle || wm.body.animationFrames != bodyFramesBefore {
			t.Fatalf("a batch close moved the body: phase %v, +%d frames",
				wm.body.phase, wm.body.animationFrames-bodyFramesBefore)
		}
		viewport, ok := tt.Find(message.WorkTabsLabel())
		if !ok {
			t.Fatalf("the strip has no viewport label")
		}
		bar := stripBarFill(tt, viewport, closedBox)
		if got := countTabFill(tt, closedBox, bar); got != 0 {
			t.Fatalf("the batch close painted %d per-Tab exit pixels", got)
		}
		// The surviving strip is at its final layout on the event frame.
		remainingBox := stripTravel(t, tt, remaining)
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if final := stripTravel(t, tt, remaining); !rectNearOK(final, remainingBox, 1) {
			t.Fatalf("the surviving Tab moved from %+v to %+v after the batch close frame", remainingBox, final)
		}
	}

	t.Run("CloseTabsRight at rest", func(t *testing.T) {
		g, tt, wm, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		viewport, _ := tt.Find(message.WorkTabsLabel())
		closedBox := stripTravel(t, tt, ids[3])
		if got := countTabFill(tt, closedBox, stripBarFill(tt, viewport, closedBox)); got == 0 {
			t.Fatalf("the active Tab paints no fill before the close")
		}
		stripFrames, bodyFrames := reveal.strip.animationFrames, wm.body.animationFrames
		warp := g.state.BodyWarpSeq()
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		if got := g.state.BodyWarpSeq(); got != warp+1 {
			t.Fatalf("batch CloseTabsRight raised the body warp sequence %d times, want once", got-warp)
		}
		tt.Frame()
		checkWarp(t, g, tt, wm, reveal, closedBox, ids[0], stripFrames, bodyFrames)
	})

	t.Run("CloseOtherTabs at rest", func(t *testing.T) {
		g, tt, wm, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3)}
		viewport, _ := tt.Find(message.WorkTabsLabel())
		closedBox := stripTravel(t, tt, ids[3])
		if got := countTabFill(tt, closedBox, stripBarFill(tt, viewport, closedBox)); got == 0 {
			t.Fatalf("the active Tab paints no fill before the close")
		}
		stripFrames, bodyFrames := reveal.strip.animationFrames, wm.body.animationFrames
		warp := g.state.BodyWarpSeq()
		if !g.state.CloseOtherTabs(0) {
			t.Fatalf("CloseOtherTabs failed")
		}
		if got := g.state.BodyWarpSeq(); got != warp+1 {
			t.Fatalf("batch CloseOtherTabs raised the body warp sequence %d times, want once", got-warp)
		}
		tt.Frame()
		checkWarp(t, g, tt, wm, reveal, closedBox, ids[0], stripFrames, bodyFrames)
	})

	t.Run("CloseOtherTabs during a strip exit", func(t *testing.T) {
		g, tt, wm, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 5, 1600, 600)
		ids := []model.TabID{
			g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3), g.mustTabID(t, 4),
		}
		if !g.state.CloseTab(1) {
			t.Fatalf("CloseTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabClose)
		if reveal.strip.run == nil {
			t.Fatalf("the single close did not start a strip exit")
		}
		tt.Advance(30 * time.Millisecond)
		viewport, _ := tt.Find(message.WorkTabsLabel())
		closedBox := stripTravel(t, tt, ids[4])
		if got := countTabFill(tt, closedBox, stripBarFill(tt, viewport, closedBox)); got == 0 {
			t.Fatalf("the active Tab paints no fill while the strip exit runs")
		}
		stripFrames, bodyFrames := reveal.strip.animationFrames, wm.body.animationFrames
		if !g.state.CloseOtherTabs(0) {
			t.Fatalf("CloseOtherTabs failed")
		}
		tt.Frame()
		checkWarp(t, g, tt, wm, reveal, closedBox, ids[0], stripFrames, bodyFrames)
	})

	t.Run("CloseTabsRight during a strip reorder", func(t *testing.T) {
		g, tt, wm, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 5, 1600, 600)
		ids := []model.TabID{
			g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2), g.mustTabID(t, 3), g.mustTabID(t, 4),
		}
		if !g.state.MoveTab(ids[0], ids[4], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabReorder)
		if reveal.strip.run == nil {
			t.Fatalf("the reorder did not start a strip run")
		}
		tt.Advance(30 * time.Millisecond)
		viewport, _ := tt.Find(message.WorkTabsLabel())
		closedBox := stripTravel(t, tt, ids[4])
		if got := countTabFill(tt, closedBox, stripBarFill(tt, viewport, closedBox)); got == 0 {
			t.Fatalf("the active Tab paints no fill while the reorder runs")
		}
		stripFrames, bodyFrames := reveal.strip.animationFrames, wm.body.animationFrames
		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		tt.Frame()
		checkWarp(t, g, tt, wm, reveal, closedBox, ids[1], stripFrames, bodyFrames)
	})
}

// TestTabStripFirstOverflowRevealWarpsAtMinimumWidth opens the first Tab that
// does not fit at the minimum window: the reveal scrolls the strip on the same
// frame, so the strip must warp to the final layout instead of running a
// 180 ms enter under a jumping scroll offset.
func TestTabStripFirstOverflowRevealWarpsAtMinimumWidth(t *testing.T) {
	setting := tabStripPreferences()
	g := motionGraph(t, setting)
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForRealSurfaces(t, g.state, 2)
	reveal := newWorkTabReveal()
	view, _ := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(view, 720, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	message := locales.In("en")
	if _, ok := tt.Find(message.WorkTabsLabel()); !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the two Tab strip already overflows (%v); the case needs the first overflow", reveal.scroll.MaxX)
	}
	first := g.mustTabID(t, 0)
	stripFrames := reveal.strip.animationFrames

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 3)
	opened := g.mustTabID(t, 2)
	tt.Frame()
	stripEvent(t, g.state, motion.TabOpen)
	if reveal.strip.run != nil {
		t.Fatalf("the first overflowing reveal started a strip run: %+v", reveal.strip.run)
	}
	if reveal.strip.animationFrames != stripFrames {
		t.Fatalf("the first overflowing reveal asked for %d strip frames", reveal.strip.animationFrames-stripFrames)
	}
	viewport, _ := tt.Find(message.WorkTabsLabel())
	start := stripTravel(t, tt, opened)
	if start.W <= 0 {
		t.Fatalf("the opened Tab has no width on the event frame")
	}
	if start.X < viewport.X-1 || start.X+start.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event frame", start, viewport)
	}
	eventFirst := stripTravel(t, tt, first)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	final := stripTravel(t, tt, opened)
	finalFirst := stripTravel(t, tt, first)
	if !rectNearOK(start, final, 1) {
		t.Fatalf("the first overflowing open animated from %+v to %+v, want the final layout at once", start, final)
	}
	if !rectNearOK(eventFirst, finalFirst, 1) {
		t.Fatalf("the first Tab snapped from %+v to %+v around the first overflow", eventFirst, finalFirst)
	}
}

// TestTabStripOverflowCloseClampWarpsAtMinimumWidth closes a non-current Tab
// in an overflowing strip at the main window's minimum. The layout clamps the
// scroll offset on the event frame and every item would jump under the exit
// animation, so the event warps instead. One case is scrolled to the end; the
// other starts just short of the end by less than the Tab's whole row
// contribution, so it is inside the old maximum but past the new one: a close
// removes the body, both insertion placeholders and three row gaps, and an
// offset that looks like it still fits really clamps (ticket 20).
func TestTabStripOverflowCloseClampWarpsAtMinimumWidth(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	for _, tc := range []struct {
		name          string
		interiorDelta float32
		atEnd         bool
	}{
		{name: "at the end", atEnd: true},
		{name: "interior one DIP short of the close's body", interiorDelta: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 6, 720, 480)
			message := locales.In("en")
			viewport, ok := tt.Find(message.WorkTabsLabel())
			if !ok {
				t.Fatalf("the strip has no viewport label")
			}
			watched := g.mustTabID(t, 5)
			if !tc.atEnd {
				closing := g.mustTabID(t, 1)
				width := reveal.strip.widths[closing]
				if width <= 0 {
					t.Fatalf("no width recorded for the closing Tab (%v)", width)
				}
				preMax := reveal.scroll.MaxX
				if preMax <= 0.5 {
					t.Fatalf("the strip is not overflowing (MaxX=%v)", preMax)
				}
				reveal.scroll.X = preMax - width - tc.interiorDelta
				tt.Frame()
				if reveal.scroll.X >= preMax-0.5 {
					t.Fatalf("the interior offset did not apply (X=%v MaxX=%v)",
						reveal.scroll.X, reveal.scroll.MaxX)
				}
			}
			if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
				t.Fatalf("the strip is not scrolled at 6 Tabs (X=%v MaxX=%v); the case needs overflow",
					reveal.scroll.X, reveal.scroll.MaxX)
			}
			beforeX := reveal.scroll.X
			preMax := reveal.scroll.MaxX
			stripFrames := reveal.strip.animationFrames

			if !g.state.CloseTab(1) {
				t.Fatalf("CloseTab failed")
			}
			tt.Frame()
			stripEvent(t, g.state, motion.TabClose)
			if reveal.strip.run != nil {
				t.Fatalf("the clamped close started a strip run: %+v", reveal.strip.run)
			}
			if reveal.strip.animationFrames != stripFrames {
				t.Fatalf("the clamped close asked for %d strip frames", reveal.strip.animationFrames-stripFrames)
			}
			if reveal.scroll.X >= beforeX-0.5 {
				t.Fatalf("the close did not clamp the strip's scroll (X=%v from %v); the case needs the jump",
					reveal.scroll.X, beforeX)
			}
			if !tc.atEnd && beforeX <= reveal.scroll.MaxX+0.5 {
				t.Fatalf("the interior close started at X=%v, inside the new MaxX=%v; the case needs the offset past it",
					beforeX, reveal.scroll.MaxX)
			}
			if abs32(reveal.scroll.X-reveal.scroll.MaxX) > 1 {
				t.Fatalf("the clamped close rests at X=%v but MaxX=%v, want the new maximum",
					reveal.scroll.X, reveal.scroll.MaxX)
			}
			if preMax <= reveal.scroll.MaxX+0.5 {
				t.Fatalf("the close did not shrink the content (preMax=%v postMax=%v)",
					preMax, reveal.scroll.MaxX)
			}
			event := stripTravel(t, tt, watched)
			if event.X < viewport.X-1 || event.X+event.W > viewport.X+viewport.W+1 {
				t.Fatalf("the watched Tab %+v is not inside the strip %+v on the close frame", event, viewport)
			}
			tt.Advance(400 * time.Millisecond)
			tt.Frame()
			final := stripTravel(t, tt, watched)
			if !rectNearOK(event, final, 1) {
				t.Fatalf("the watched Tab jumped from %+v to %+v under the scroll clamp, want the final layout at once",
					event, final)
			}
		})
	}
}

// interiorCloseWindow is one eight-Tab strip scrolled to an interior offset
// before a non-current close: MaxX - closingWidth - delta. The Tabs share one
// title, so the row is eight equal bodies and the delta is the only variable.
type interiorCloseWindow struct {
	graph     *followGraph
	tester    *ui.Tester
	reveal    *workTabReveal
	watched   [2]model.TabID
	startX    float32
	preMax    float32
	preBuilds int
	preFrames int
}

// newInteriorCloseWindow builds the window at the 720-DIP minimum, applies the
// offset in a real build, and returns it with the closing Tab still open.
func newInteriorCloseWindow(t *testing.T, delta int) *interiorCloseWindow {
	t.Helper()
	return newInteriorCloseWindowAt(t, float32(delta))
}

// TestTabStripInteriorCloseClampWarpsAtMinimumWidth is the undercount
// regression: eight equal Tabs in a 720-DIP strip-only window, scrolled inside
// the old maximum by less than the closing Tab's whole row contribution. The
// close removes two insertion placeholders and three row gaps besides the
// body, so these offsets really clamp; predicting the removal as body plus one
// gap started a TabClose exit under the jump. Every delta in the affected
// window must warp on its event paint (no run, no frame, offset and boxes at
// once); the next delta must still animate the close.
func TestTabStripInteriorCloseClampWarpsAtMinimumWidth(t *testing.T) {
	for _, style := range []struct {
		name          string
		s             kit.Style
		clampDeltas   []int
		animateDeltas []int
	}{
		{
			name: "classic", s: kit.Classic,
			// Classic's gap is 0: the undercount was 2*2 DIPs. The animate
			// control is 8 DIPs clear of the boundary: the close guard treats
			// an offset at or near the new maximum as a warp, and the bound
			// overstates the removal by 4 DIPs, so an offset at X=postMax (the
			// old delta 4) now warps. The dedicated boundary test covers
			// X=postMax and its neighbours.
			clampDeltas: []int{0, 1, 2, 3}, animateDeltas: []int{12},
		},
		{
			name: "macos", s: kit.MacOS,
			// macOS's gap is 4 DIPs: the undercount was 2*2 + 2*4 DIPs. The
			// boundary moved the same way: delta 16 is X=postMax, so the
			// animate control sits 8 DIPs below it.
			clampDeltas:   []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			animateDeltas: []int{24},
		},
	} {
		t.Run(style.name, func(t *testing.T) {
			kit.SetStyle(style.s)
			t.Cleanup(func() { kit.SetStyle(kit.Classic) })
			for _, delta := range style.clampDeltas {
				t.Run(fmt.Sprintf("clamp delta=%d", delta), func(t *testing.T) {
					interiorCloseClamps(t, delta)
				})
			}
			for _, delta := range style.animateDeltas {
				t.Run(fmt.Sprintf("animate delta=%d", delta), func(t *testing.T) {
					interiorCloseAnimates(t, delta)
				})
			}
		})
	}
}

// interiorCloseClamps closes the background Tab at an interior offset that the
// row's real removal puts past the new maximum, and proves the event paint
// warps: one build, no TabClose run, no animation frame, the offset at once at
// the new maximum, and the surviving boxes final and stable.
func interiorCloseClamps(t *testing.T, delta int) {
	t.Helper()
	w := newInteriorCloseWindow(t, delta)
	if !w.graph.state.CloseTab(1) {
		t.Fatalf("CloseTab failed")
	}
	w.tester.Advance(17 * time.Millisecond)
	stripEvent(t, w.graph.state, motion.TabClose)
	if got := w.reveal.strip.frames - w.preBuilds; got != 1 {
		t.Fatalf("the close built %d event paints, want exactly one", got)
	}
	if w.reveal.strip.run != nil {
		t.Fatalf("the clamped interior close started a strip run: %+v", w.reveal.strip.run)
	}
	if !w.reveal.strip.warp {
		t.Fatalf("the clamped interior close did not warp on its event paint")
	}
	if got := w.reveal.strip.animationFrames - w.preFrames; got != 0 {
		t.Fatalf("the clamped interior close asked for %d strip frames", got)
	}
	postMax := w.reveal.scroll.MaxX
	if postMax >= w.preMax-0.5 {
		t.Fatalf("the close did not shrink the content (preMax=%v postMax=%v)", w.preMax, postMax)
	}
	if w.startX <= postMax+0.5 {
		t.Fatalf("the interior offset X=%v is not past the new maximum %v; the case needs the clamp",
			w.startX, postMax)
	}
	if got := w.reveal.scroll.X; abs32(got-postMax) > 1 {
		t.Fatalf("the clamped close rests at X=%v but MaxX=%v, want the new maximum", got, postMax)
	}
	boxes := [2]ui.Rect{
		stripTravel(t, w.tester, w.watched[0]),
		stripTravel(t, w.tester, w.watched[1]),
	}

	w.tester.Advance(400 * time.Millisecond)
	if w.reveal.strip.run != nil {
		t.Fatalf("the clamped interior close left a run after settling: %+v", w.reveal.strip.run)
	}
	if got := w.reveal.strip.animationFrames - w.preFrames; got != 0 {
		t.Fatalf("the clamped interior close animated %d frames in total", got)
	}
	if abs32(w.reveal.scroll.X-postMax) > 1 || abs32(w.reveal.scroll.MaxX-postMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's %v",
			w.reveal.scroll.X, w.reveal.scroll.MaxX, postMax)
	}
	for index, id := range w.watched {
		if final := stripTravel(t, w.tester, id); !rectNearOK(boxes[index], final, 1) {
			t.Fatalf("the watched Tab %q jumped from %+v to %+v under the scroll clamp, want the final layout at once",
				id, boxes[index], final)
		}
	}
}

// interiorCloseAnimates is the control just outside the affected window: the
// same row closed one DIP further from the end does not clamp, so the TabClose
// strip exit must still start and reach its final layout.
func interiorCloseAnimates(t *testing.T, delta int) {
	t.Helper()
	w := newInteriorCloseWindow(t, delta)
	if !w.graph.state.CloseTab(1) {
		t.Fatalf("CloseTab failed")
	}
	w.tester.Advance(17 * time.Millisecond)
	stripEvent(t, w.graph.state, motion.TabClose)
	if w.reveal.strip.run == nil || w.reveal.strip.run.kind != motion.TabClose {
		t.Fatalf("the non-clamping interior close did not start a TabClose run: %+v", w.reveal.strip.run)
	}
	if w.reveal.strip.warp {
		t.Fatalf("the non-clamping interior close warped")
	}
	if got := w.reveal.strip.animationFrames - w.preFrames; got != 1 {
		t.Fatalf("the non-clamping interior close asked for %d strip frames on its event paint, want one", got)
	}
	if abs32(w.reveal.scroll.X-w.startX) > 1 {
		t.Fatalf("the non-clamping interior close moved the offset from %v to %v",
			w.startX, w.reveal.scroll.X)
	}
	if w.reveal.scroll.X > w.reveal.scroll.MaxX+0.5 {
		t.Fatalf("the non-clamping interior close left the offset past its maximum (X=%v MaxX=%v)",
			w.reveal.scroll.X, w.reveal.scroll.MaxX)
	}

	w.tester.Advance(400 * time.Millisecond)
	if w.reveal.strip.run != nil {
		t.Fatalf("the non-clamping interior close left a run after settling: %+v", w.reveal.strip.run)
	}
	final := stripTravel(t, w.tester, w.watched[0])
	w.tester.Advance(200 * time.Millisecond)
	if next := stripTravel(t, w.tester, w.watched[0]); !rectNearOK(final, next, 1) {
		t.Fatalf("the watched Tab settled at %+v, then moved to %+v", final, next)
	}
}

// stripOnlyView renders just the work Tab strip, so a test can observe the
// frames the strip runtime itself requests without a terminal or body
// animation in the count.
func stripOnlyView(s *state.State, reveal *workTabReveal) func(*ui.Context) {
	return func(c *ui.Context) {
		workTabs(c, s, locales.In(c.Locale()), c.Theme(), reveal)
	}
}

// TestTabStripSwitchInterruptingReorderStopsFrames switches Tabs while a strip
// reorder is mid-run: the run is cancelled, the final layout shows at once,
// and the strip requests no further frames. The layout target does not change
// on a switch, so a warp transition alone would leave MyGo's in-flight
// channels moving; the row's new generation drops them.
func TestTabStripSwitchInterruptingReorderStopsFrames(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 3)
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(stripOnlyView(g.state, reveal), 1600, 120)
	tt.SetLocale("en")
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatalf("the resting strip asks for frames")
	}
	ids := []model.TabID{followTabID(t, g.state, 0), followTabID(t, g.state, 1), followTabID(t, g.state, 2)}
	boxes := []ui.Rect{
		stripTravel(t, tt, ids[0]), stripTravel(t, tt, ids[1]), stripTravel(t, tt, ids[2]),
	}

	if !g.state.MoveTab(ids[0], ids[2], true) {
		t.Fatalf("MoveTab failed")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabReorder)
	if reveal.strip.run == nil {
		t.Fatalf("the reorder did not start a strip run")
	}
	if !tt.FramePending() {
		t.Fatalf("the reorder asks for no frames; the case needs it mid-run")
	}
	tt.Advance(90 * time.Millisecond)

	g.state.SelectTab(0)
	tt.Frame()
	stripEvent(t, g.state, motion.TabSwitch)
	if reveal.strip.run != nil {
		t.Fatalf("the switch left a strip run: %+v", reveal.strip.run)
	}
	if tt.FramePending() {
		t.Fatalf("the strip still asks for frames after the switch cancelled its reorder")
	}
	// The reordered layout is final on the switch frame: [id1, id2, id0].
	want := map[model.TabID]ui.Rect{ids[1]: boxes[0], ids[2]: boxes[1], ids[0]: boxes[2]}
	for id, box := range want {
		if got := stripTravel(t, tt, id); !rectNearOK(got, box, 1) {
			t.Fatalf("the interrupted reorder left Tab %q at %+v, want the final %+v", id, got, box)
		}
	}
	tt.Advance(30 * time.Millisecond)
	if tt.FramePending() {
		t.Fatalf("the strip asks for frames a frame after the interrupting switch")
	}
}

// TestTabStripWideFirstOverflowRevealWarps opens a Tab whose final width
// pushes a fitting strip over the viewport for the first time. The view
// predicts with a Work Tab's maximum width, so the event warps and the
// revealed final layout shows with no strip frame of its own. This test reads
// the settled Tester.Frame() result only; the live event paint that a real
// window shows is measured by
// TestTabStripWideFirstOverflowWarpsOnTheEventPaint, which steps one build at
// a time (ticket 20).
func TestTabStripWideFirstOverflowRevealWarps(t *testing.T) {
	setting := tabStripPreferences()
	g := motionGraph(t, setting)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	// An 18-character title lays the Tab out between WorkTabMinWidth and its
	// maximum: the previous frame's content plus WorkTabMaxWidth is what
	// predicts the overflow.
	if !g.state.RenameTab(0, "wide-overflow-tttt") {
		t.Fatalf("RenameTab failed")
	}
	reveal := newWorkTabReveal()
	view, _ := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(view, 720, 480)
	tt.SetLocale("en")
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	message := locales.In("en")
	if _, ok := tt.Find(message.WorkTabsLabel()); !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the one wide Tab already overflows (MaxX=%v); the case needs the first overflow",
			reveal.scroll.MaxX)
	}
	first := g.mustTabID(t, 0)
	frames := reveal.strip.animationFrames

	if err := g.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 2)
	opened := g.mustTabID(t, 1)
	if opened == first {
		t.Fatalf("the duplicate reused the source's Tab ID")
	}
	tt.Frame()
	stripEvent(t, g.state, motion.TabOpen)
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the duplicated strip does not overflow (MaxX=%v); the case needs it", reveal.scroll.MaxX)
	}
	if reveal.scroll.X <= 0.5 {
		t.Fatalf("the reveal did not scroll the overflowing strip (X=%v)", reveal.scroll.X)
	}
	eventScroll := reveal.scroll.X
	if reveal.strip.run != nil {
		t.Fatalf("the wide first overflow started a strip run: %+v", reveal.strip.run)
	}
	if reveal.strip.animationFrames != frames {
		t.Fatalf("the wide first overflow asked for %d strip frames",
			reveal.strip.animationFrames-frames)
	}
	viewport, _ := tt.Find(message.WorkTabsLabel())
	start := stripTravel(t, tt, opened)
	if start.W <= 0 {
		t.Fatalf("the opened Tab has no width on the event frame")
	}
	if start.X < viewport.X-1 || start.X+start.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event frame",
			start, viewport)
	}
	eventFirst := stripTravel(t, tt, first)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	final := stripTravel(t, tt, opened)
	finalFirst := stripTravel(t, tt, first)
	if !rectNearOK(start, final, 1) {
		t.Fatalf("the wide first overflowing open animated from %+v to %+v, want the final layout at once",
			start, final)
	}
	if !rectNearOK(eventFirst, finalFirst, 1) {
		t.Fatalf("the existing Tab snapped from %+v to %+v around the first overflow",
			eventFirst, finalFirst)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 {
		t.Fatalf("the reveal scroll moved from %v to %v after the event frame",
			eventScroll, reveal.scroll.X)
	}
}

// TestTabStripWideFirstOverflowWarpsOnTheEventPaint is the live-paint
// regression for ticket 20's narrow-window conflict rule. It steps one build
// at a time, so the frame a real window would show is measured before any
// later build could cancel a transition. Before the maximum-width prediction,
// this frame started the TabOpen enter: the opened Tab painted collapsed at
// w=0 under the reveal's scroll jump, and only the build after cancelled it.
func TestTabStripWideFirstOverflowWarpsOnTheEventPaint(t *testing.T) {
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 1, 720, 480,
		func(t *testing.T, g *realTerminalGraph) {
			if !g.state.RenameTab(0, "wide-overflow-16") {
				t.Fatalf("RenameTab failed")
			}
		})
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the one wide Tab already overflows (MaxX=%v); the case needs the first overflow",
			reveal.scroll.MaxX)
	}
	first := g.mustTabID(t, 0)
	preFrames, preGeneration, preBuilds := reveal.strip.animationFrames, reveal.strip.generation, reveal.strip.frames

	if err := g.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 2)
	opened := g.mustTabID(t, 1)
	if opened == first {
		t.Fatalf("the duplicate reused the source's Tab ID")
	}

	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the wide first overflow started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the wide first overflow asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the wide first overflow remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	// The reveal scrolled the strip on this very frame; a live enter would be
	// sliding under that jump.
	if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
		t.Fatalf("the reveal did not scroll the overflowing strip on the event paint (X=%v MaxX=%v)",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	eventScroll := reveal.scroll.X
	eventOpened := stripTravel(t, tt, opened)
	eventFirst := stripTravel(t, tt, first)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}

	tt.Advance(400 * time.Millisecond)
	finalOpened := stripTravel(t, tt, opened)
	finalFirst := stripTravel(t, tt, first)
	if !rectNearOK(eventOpened, finalOpened, 1) {
		t.Fatalf("the wide first overflowing open painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, finalOpened)
	}
	if !rectNearOK(eventFirst, finalFirst, 1) {
		t.Fatalf("the first Tab painted %+v on the event frame, then %+v, want the final layout at once",
			eventFirst, finalFirst)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 {
		t.Fatalf("the reveal scroll moved from %v to %v after the event paint", eventScroll, reveal.scroll.X)
	}
}

// TestTabStripMinWidthManyTabsOverflowWarpsOnTheEventPaint keeps the primary
// narrow-window case on the same live event paint: an already overflowing
// strip whose reveal scrolls must warp on the event build, not on the next.
func TestTabStripMinWidthManyTabsOverflowWarpsOnTheEventPaint(t *testing.T) {
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 6, 720, 480, nil)
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the six Tab strip does not overflow (MaxX=%v); the case needs overflow",
			reveal.scroll.MaxX)
	}
	watched := g.mustTabID(t, 5)
	preFrames, preGeneration, preBuilds := reveal.strip.animationFrames, reveal.strip.generation, reveal.strip.frames

	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 7)
	opened := g.mustTabID(t, 6)

	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the min-width many-Tabs overflow started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the min-width many-Tabs overflow asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the min-width many-Tabs overflow remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	eventScroll := reveal.scroll.X
	eventOpened := stripTravel(t, tt, opened)
	eventWatched := stripTravel(t, tt, watched)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}

	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, final)
	}
	if final := stripTravel(t, tt, watched); !rectNearOK(eventWatched, final, 1) {
		t.Fatalf("the watched Tab painted %+v on the event frame, then %+v, want the final layout at once",
			eventWatched, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 {
		t.Fatalf("the reveal scroll moved from %v to %v after the event paint", eventScroll, reveal.scroll.X)
	}
}

// maxWidthTabTitle lays a Work Tab out at its maximum width, so the strip's
// prediction arithmetic is decided by the recorded widths alone.
const maxWidthTabTitle = "zzzz-this-is-a-max-width-title-zzzz-zzzz"

// TestTabStripDetachedRecordDoesNotUnderpredictOnTheEventPaint detaches a Tab
// through its hover menu. A detach records no motion event, so the strip never
// consumes a TabClose for it and keeps the leaving Tab's width record. The next
// open's prediction must not subtract that record from the recorded content,
// which no longer contains the Tab: with the subtraction the prediction reads
// ~335 in a 382-wide strip and starts a collapsed enter under the reveal's
// scroll jump; the row then warps on the build after, too late for the frame a
// window showed.
func TestTabStripDetachedRecordDoesNotUnderpredictOnTheEventPaint(t *testing.T) {
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 2, 720, 480,
		func(t *testing.T, g *realTerminalGraph) {
			if !g.state.RenameTab(0, maxWidthTabTitle) {
				t.Fatalf("RenameTab failed")
			}
		})
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
		t.Fatalf("the max-width Tab does not overflow the strip (X=%v MaxX=%v); the case needs the overflow",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	if reveal.boxes.Content.W <= viewport.W {
		t.Fatalf("the two-Tab content %v fits the viewport %v; the case needs the overflow",
			reveal.boxes.Content.W, viewport.W)
	}
	detached := g.mustTabID(t, 1)
	detachedBox := stripTravel(t, tt, detached)
	revealScrollX, revealMaxX := reveal.scroll.X, reveal.scroll.MaxX

	// The active Tab's menu sits on its trailing edge; its Detach Tab item is
	// the UI-reachable path that leaves no motion event behind.
	if err := tt.Click(message.TabMenuGlyph()); err != nil {
		t.Fatalf("the active Tab's menu control cannot be clicked: %v", err)
	}
	if err := tt.ChooseMenuItem(message.TabDetach()); err != nil {
		t.Fatalf("Detach Tab: %v", err)
	}
	tt.Advance(400 * time.Millisecond)
	if g.state.TabCount() != 1 {
		t.Fatalf("tabs = %d after Detach Tab, want 1", g.state.TabCount())
	}
	if ids := g.state.DetachedTabIDs(); len(ids) != 1 || ids[0] != detached {
		t.Fatalf("detached Tabs = %v, want %q", ids, detached)
	}
	if reveal.scroll.X > 0.5 || reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the one-Tab strip still scrolls (X=%v MaxX=%v); the case needs it settled at the start",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	// The detach records no TabClose, so the width record it measured is still
	// here while the boxes already describe a row without it. The open
	// decision bounds the row from the state, so this stale record cannot
	// undercount it (ticket 20).
	leak, leaked := reveal.strip.widths[detached]
	if !leaked || abs32(leak-detachedBox.W) > 1 {
		t.Fatalf("the detached Tab's width record = %v (present %v); the case needs the leaked record of its %v box",
			leak, leaked, detachedBox.W)
	}
	kept := g.mustTabID(t, 0)
	if keptWidth := reveal.strip.widths[kept]; abs32(keptWidth-kit.WorkTabMaxWidth) > 1 {
		t.Fatalf("the kept Tab's record = %v; the case needs its max-width record", keptWidth)
	}
	preFrames, preGeneration, preBuilds := reveal.strip.animationFrames, reveal.strip.generation, reveal.strip.frames

	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 2)
	opened := g.mustTabID(t, 1)

	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the detached record's open started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the detached record's open asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the detached record's open remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	// The prediction must have warped the reveal; the live enter this test
	// guards against painted the opened Tab collapsed at w=0 under it.
	if abs32(reveal.scroll.X-revealScrollX) > 1 || abs32(reveal.scroll.MaxX-revealMaxX) > 1 {
		t.Fatalf("the event paint scrolled the strip to X=%v MaxX=%v, want the setup's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, revealScrollX, revealMaxX)
	}
	eventOpened := stripTravel(t, tt, opened)
	eventKept := stripTravel(t, tt, kept)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	if !rectNearOK(eventOpened, detachedBox, 1) {
		t.Fatalf("the opened Tab paints %+v on the event paint, want the departed Tab's final %+v",
			eventOpened, detachedBox)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}
	if eventKept.W < 1 {
		t.Fatalf("the kept Tab paints collapsed (w=%v) on the event paint", eventKept.W)
	}

	// The event paint is the final layout: nothing moves afterwards.
	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if final := stripTravel(t, tt, kept); !rectNearOK(eventKept, final, 1) {
		t.Fatalf("the kept Tab painted %+v on its event frame, then settled at %+v", eventKept, final)
	}
	if abs32(reveal.scroll.X-revealScrollX) > 1 || abs32(reveal.scroll.MaxX-revealMaxX) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, revealScrollX, revealMaxX)
	}
}

// TestTabStripSupersededCloseRecordDoesNotUnderpredictOnTheEventPaint closes a
// Tab and opens another in the same frame. The state keeps only the latest
// motion event, so the TabClose is never consumed and the closed Tab's width
// record survives; the next open's prediction must not subtract it from
// content the boxes already exclude, or the entering Tab paints collapsed at
// w=0 under the reveal's scroll jump.
func TestTabStripSupersededCloseRecordDoesNotUnderpredictOnTheEventPaint(t *testing.T) {
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 1, 720, 480, nil)
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if !g.state.RenameTab(0, "a") {
		t.Fatalf("RenameTab failed")
	}
	tt.Advance(400 * time.Millisecond)
	if reveal.scroll.MaxX > 0.5 {
		t.Fatalf("the one-Tab strip overflows (MaxX=%v); the case needs it fitting",
			reveal.scroll.MaxX)
	}
	closed := g.mustTabID(t, 0)
	closedWidth := stripTravel(t, tt, closed).W

	// Both happen between two builds: CloseTab records a TabClose that NewTab's
	// TabOpen supersedes, so the strip never sees the close.
	if !g.state.CloseTab(0) {
		t.Fatalf("CloseTab failed")
	}
	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 1)
	survivor := g.mustTabID(t, 0)
	tt.Advance(400 * time.Millisecond)
	leak, leaked := reveal.strip.widths[closed]
	if !leaked || abs32(leak-closedWidth) > 1 {
		t.Fatalf("the superseded close left the record %v (present %v), want the closed Tab's %v",
			leak, leaked, closedWidth)
	}

	if !g.state.RenameTab(0, maxWidthTabTitle) {
		t.Fatalf("RenameTab failed")
	}
	preRename := reveal.strip.frames
	tt.Advance(2 * 17 * time.Millisecond)
	if got := reveal.strip.frames - preRename; got != 2 {
		t.Fatalf("the rename step built %d frames, want the two that settle its layout", got)
	}
	if record := reveal.strip.widths[survivor]; abs32(record-kit.WorkTabMaxWidth) > 1 {
		t.Fatalf("the renamed Tab's width record = %v; the case needs the max-width layout", record)
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 2)
	opened := g.mustTabID(t, 1)
	preFrames, preGeneration, preBuilds := reveal.strip.animationFrames, reveal.strip.generation, reveal.strip.frames

	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the superseded close's open started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the superseded close's open asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the superseded close's open remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	// The documented repro layout: the max-width Tab plus the opened one
	// overflows 720's strip and its reveal lands at X 36.82361 / MaxX 62.82361.
	if abs32(reveal.scroll.X-36.82361) > 1 || abs32(reveal.scroll.MaxX-62.82361) > 1 {
		t.Fatalf("the event paint scrolls to X=%v MaxX=%v, want X=36.82361 MaxX=62.82361",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	eventOpened := stripTravel(t, tt, opened)
	eventKept := stripTravel(t, tt, survivor)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	if abs32(eventOpened.X-392.9873) > 1 || abs32(eventOpened.W-173.0127) > 1 {
		t.Fatalf("the opened Tab paints %+v on the event paint, want x=392.9873 w=173.0127",
			eventOpened)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}
	if eventKept.W < 1 {
		t.Fatalf("the kept Tab paints collapsed (w=%v) on the event paint", eventKept.W)
	}

	// The event paint is the final layout: nothing moves afterwards.
	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-36.82361) > 1 || abs32(reveal.scroll.MaxX-62.82361) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=36.82361 MaxX=62.82361",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
}

// TestTabStripPendingLabelSwapWarpsThePrediction is the same-count Pending
// Connection case: a Host leaving the connecting row and another joining it
// keeps the count but changes the labels and so the row's width. The open
// decision bounds the Pending rows from the labels this frame draws, so the
// swapped, wider row is seen on its own frame instead of guessed from a
// recorded row signature. The state cannot hold a live Pending Connection in
// this graph (a real dial clears it on Flush), so the decision seam takes the
// row.
func TestTabStripPendingLabelSwapWarpsThePrediction(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	addStripTabs(t, g.state, 1)
	if !g.state.RenameTab(0, "a") {
		t.Fatalf("RenameTab failed")
	}
	reveal := newWorkTabReveal()
	// The boxes are the Tester's own width, so only the row's width decides:
	// the short Pending row fits the 500-DIP viewport, the same-count long one
	// overflows it, and adding a label overflows it further.
	reveal.boxes = kit.TabStripBoxes{
		Viewport: ui.Rect{W: 500},
		Content:  ui.Rect{W: 200},
	}
	reveal.boxesWidth = 720
	const long = "long-host-name-that-keeps-going.example.com"

	var same, swapped, grown bool
	var shortBound, longBound float32
	view := func(c *ui.Context) {
		same = reveal.openWillOverflow(c, g.state, locales.In("en"), []string{"a"})
		swapped = reveal.openWillOverflow(c, g.state, locales.In("en"), []string{long})
		grown = reveal.openWillOverflow(c, g.state, locales.In("en"), []string{"a", long})
		shortBound = reveal.contentWidthBound(c, g.state, locales.In("en"), []string{"a"})
		longBound = reveal.contentWidthBound(c, g.state, locales.In("en"), []string{long})
	}
	tt := ui.NewTester(view, 720, 480)
	tt.SetLocale("en")
	tt.Frame()
	if same {
		t.Fatalf("the short Pending row overflowed the 500-DIP viewport; the case needs the fitting baseline")
	}
	if !swapped {
		t.Fatalf("a same-count Pending label swap did not warp the open: the wider row was not bounded")
	}
	if !grown {
		t.Fatalf("a Pending row gaining a label did not warp the open")
	}
	if longBound <= shortBound {
		t.Fatalf("the long Pending row's bound %v is not wider than the short row's %v", longBound, shortBound)
	}
}

// TestTabStripFittingOpenStillAnimatesOnTheEventPaint is the guard against
// over-warping: the maximum-width prediction must not stop a clearly fitting
// open from entering. The first case is the default window; the second is the
// narrowest kind of case the bound can still call fitting, a minimum-content
// strip where the previous content plus a maximum-width Tab stays inside the
// viewport.
func TestTabStripFittingOpenStillAnimatesOnTheEventPaint(t *testing.T) {
	checkEntry := func(t *testing.T, tt *ui.Tester, reveal *workTabReveal) {
		t.Helper()
		preFrames, preBuilds := reveal.strip.animationFrames, reveal.strip.frames
		tt.Advance(17 * time.Millisecond)
		if got := reveal.strip.frames - preBuilds; got != 1 {
			t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
		}
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabOpen || !reveal.strip.animate || reveal.strip.warp {
			t.Fatalf("a fitting open left run=%+v animate=%v warp=%v on its event paint, want the TabOpen enter",
				reveal.strip.run, reveal.strip.animate, reveal.strip.warp)
		}
		if got := reveal.strip.animationFrames - preFrames; got != 1 {
			t.Fatalf("a fitting open asked for %d strip frames on its event paint, want 1", got)
		}
	}

	t.Run("default width", func(t *testing.T) {
		g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 1, 1600, 600, nil)
		last := stripTravel(t, tt, g.mustTabID(t, 0))
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForRealSurfaces(t, g.state, 2)
		opened := g.mustTabID(t, 1)
		checkEntry(t, tt, reveal)
		start := stripTravel(t, tt, opened)
		if start.W >= last.W/2 {
			t.Fatalf("the opening Tab starts at width %v, want it collapsed under %v", start.W, last.W/2)
		}
		tt.Advance(90 * time.Millisecond)
		mid := stripTravel(t, tt, opened)
		if mid.W <= start.W+1 || mid.W >= last.W-1 {
			t.Fatalf("the opening Tab is %v wide mid-run, want between %v and %v", mid.W, start.W, last.W)
		}
		tt.Advance(400 * time.Millisecond)
		final := stripTravel(t, tt, opened)
		if abs32(final.W-last.W) > 1 {
			t.Fatalf("the final opening Tab is %v wide, want the strip's %v", final.W, last.W)
		}
	})

	t.Run("minimum content with the maximum-width bound fitting", func(t *testing.T) {
		g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 1, 800, 480,
			func(t *testing.T, g *realTerminalGraph) {
				if !g.state.RenameTab(0, "a") {
					t.Fatalf("RenameTab failed")
				}
			})
		// The default Classic style has no gap between Work Tabs; with it the
		// prediction is still false because the content plus a maximum-width
		// Tab stays inside the viewport.
		if reveal.boxes.Content.W <= 0 || reveal.boxes.Viewport.W <= 0 {
			t.Fatalf("the strip boxes are not built: %+v", reveal.boxes)
		}
		if reveal.boxes.Content.W+kit.WorkTabMaxWidth > reveal.boxes.Viewport.W {
			t.Fatalf("content %v plus a maximum-width Tab does not fit the viewport %v; the case needs a fitting open",
				reveal.boxes.Content.W, reveal.boxes.Viewport.W)
		}
		first := stripTravel(t, tt, g.mustTabID(t, 0))
		if err := g.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForRealSurfaces(t, g.state, 2)
		opened := g.mustTabID(t, 1)
		checkEntry(t, tt, reveal)
		start := stripTravel(t, tt, opened)
		if start.W >= first.W/2 {
			t.Fatalf("the opening Tab starts at width %v, want it collapsed under %v", start.W, first.W/2)
		}
		tt.Advance(400 * time.Millisecond)
		final := stripTravel(t, tt, opened)
		if final.W <= start.W+1 {
			t.Fatalf("the opening Tab stayed at %v, want it widened to its final %v", start.W, final.W)
		}
		if reveal.scroll.MaxX > 0.5 {
			t.Fatalf("the fitting open overflows the strip (MaxX=%v); the case needs a clearly fitting open",
				reveal.scroll.MaxX)
		}
	})
}

// TestTabStripWarpsKeepTheFocusedAddControl proves the add control's keyboard
// focus survives the strip warps too. Its stable key keeps its element
// identity through an at-rest warp (a batch close and a scroll clamp), and a
// warp that must remount the row, such as a Reduce Motion cut, gives the focus
// back to the rebuilt control. The active Tab does not change in these cases,
// so no terminal appearing can legitimately take the keyboard instead.
func TestTabStripWarpsKeepTheFocusedAddControl(t *testing.T) {
	message := locales.In("en")
	addLabel := message.MenuNewTab()

	focusAdd := func(t *testing.T, tt *ui.Tester) {
		t.Helper()
		add, ok := tt.Find(addLabel)
		if !ok {
			t.Fatalf("the strip has no add control")
		}
		// The press focuses the control; the release is off it, so the button
		// cannot click and open a Tab.
		tt.Press(add.X+add.W/2, add.Y+add.H/2)
		tt.Move(add.X+add.W/2, add.Y+add.H/2+40)
		tt.Release(add.X+add.W/2, add.Y+add.H/2+40)
		tt.Frame()
		if !tt.Focused(addLabel) {
			t.Fatalf("the add control does not hold the focus")
		}
	}

	settleAtFirst := func(t *testing.T, g *realTerminalGraph, tt *ui.Tester) {
		t.Helper()
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
	}

	t.Run("CloseTabsRight keeps the focused add", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		settleAtFirst(t, g, tt)
		focusAdd(t, tt)
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames

		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		tt.Frame()
		if g.state.TabCount() != 1 {
			t.Fatalf("tabs = %d after CloseTabsRight, want 1", g.state.TabCount())
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the batch close renamed the row (%d to %d); the add control cannot keep its identity",
				generation, reveal.strip.generation)
		}
		if reveal.strip.run != nil || reveal.strip.animationFrames != frames {
			t.Fatalf("the batch close animated: run %+v, +%d frames",
				reveal.strip.run, reveal.strip.animationFrames-frames)
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("the batch close lost the add control's focus")
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if !tt.Focused(addLabel) {
			t.Fatalf("the add control's focus did not survive the settled batch close")
		}
	})

	t.Run("CloseOtherTabs keeps the focused add", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		settleAtFirst(t, g, tt)
		focusAdd(t, tt)
		generation := reveal.strip.generation

		if !g.state.CloseOtherTabs(0) {
			t.Fatalf("CloseOtherTabs failed")
		}
		tt.Frame()
		if g.state.TabCount() != 1 {
			t.Fatalf("tabs = %d after CloseOtherTabs, want 1", g.state.TabCount())
		}
		if reveal.strip.generation != generation {
			t.Fatalf("CloseOtherTabs renamed the row (%d to %d)", generation, reveal.strip.generation)
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("CloseOtherTabs lost the add control's focus")
		}
	})

	t.Run("overflow close clamp keeps the focused add", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 6, 720, 480)
		if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
			t.Fatalf("the strip is not scrolled at 6 Tabs (X=%v MaxX=%v); the case needs overflow",
				reveal.scroll.X, reveal.scroll.MaxX)
		}
		// The add control sits after every Tab, so the reveal the last Tab did
		// leaves it just past the viewport. Scroll the strip to its end, as a
		// user reaching for it does, before focusing it.
		viewport, ok := tt.Find(message.WorkTabsLabel())
		if !ok {
			t.Fatalf("the strip has no viewport label")
		}
		tt.Wheel(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 200, 0)
		tt.Advance(17 * time.Millisecond)
		if reveal.scroll.X < reveal.scroll.MaxX-0.5 {
			t.Fatalf("the strip did not scroll to its end (X=%v MaxX=%v)", reveal.scroll.X, reveal.scroll.MaxX)
		}
		focusAdd(t, tt)
		generation, frames, beforeX := reveal.strip.generation, reveal.strip.animationFrames, reveal.scroll.X

		if !g.state.CloseTab(1) {
			t.Fatalf("CloseTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabClose)
		if reveal.scroll.X >= beforeX-0.5 {
			t.Fatalf("the close did not clamp the scroll (X=%v from %v); the case needs the jump",
				reveal.scroll.X, beforeX)
		}
		if reveal.strip.run != nil || reveal.strip.animationFrames != frames {
			t.Fatalf("the clamped close animated: run %+v, +%d frames",
				reveal.strip.run, reveal.strip.animationFrames-frames)
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the clamped close renamed the row (%d to %d)", generation, reveal.strip.generation)
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("the clamped close lost the add control's focus")
		}
	})

	t.Run("Reduce Motion cut restores the focused add", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 3, 1600, 600)
		settleAtFirst(t, g, tt)
		focusAdd(t, tt)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabReorder)
		if reveal.strip.run == nil {
			t.Fatalf("the reorder did not start a strip run")
		}
		generation := reveal.strip.generation

		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		if reveal.strip.run != nil {
			t.Fatalf("the Reduce Motion cut left a strip run: %+v", reveal.strip.run)
		}
		if reveal.strip.generation == generation {
			t.Fatalf("the cut did not remount the row; the case needs the remount")
		}
		if !tt.Focused(addLabel) {
			t.Fatalf("the cut lost the add control's focus instead of restoring it")
		}
		tt.Advance(30 * time.Millisecond)
		tt.Frame()
		if !tt.Focused(addLabel) {
			t.Fatalf("the restored add focus did not survive the frame after the cut")
		}
	})
}

// TestTabStripWarpsKeepTheFocusedTabControl proves the warp paths keep the
// keyboard focus on a Tab's item control. A warp at rest rests MyGo's records
// where they are, so the focused control keeps its element ID through a batch
// close, the real menu path and the overflow close clamp. A warp that has to
// cancel an in-flight strip run renames the row, and the rebuilt Copy of the
// focused control takes the focus back.
func TestTabStripWarpsKeepTheFocusedTabControl(t *testing.T) {
	message := locales.In("en")
	glyph := message.TabMenuGlyph()

	// focusMenu gives the active Tab at index the keyboard focus through its
	// menu control: clicking it opens the menu and focuses the control, and
	// closing the menu leaves that focus on the control.
	focusMenu := func(t *testing.T, g *realTerminalGraph, tt *ui.Tester, index int) {
		t.Helper()
		if err := tt.Click(glyph); err != nil {
			t.Fatalf("the Tab %d menu control cannot be clicked: %v", index, err)
		}
		tt.CloseMenu()
		tt.Frame()
		if !tt.Focused(glyph) {
			t.Fatalf("the Tab %d menu control does not hold the focus", index)
		}
	}

	t.Run("CloseTabsRight keeps the focused menu", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		focusMenu(t, g, tt, 0)
		generation, frames := reveal.strip.generation, reveal.strip.animationFrames

		if !g.state.CloseTabsRight(0) {
			t.Fatalf("CloseTabsRight failed")
		}
		tt.Frame()
		if g.state.TabCount() != 1 {
			t.Fatalf("tabs = %d after CloseTabsRight, want 1", g.state.TabCount())
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the batch close renamed the row (%d to %d); the focused control cannot survive",
				generation, reveal.strip.generation)
		}
		if reveal.strip.run != nil || reveal.strip.animationFrames != frames {
			t.Fatalf("the batch close animated: run %+v, +%d frames",
				reveal.strip.run, reveal.strip.animationFrames-frames)
		}
		if !tt.Focused(glyph) {
			t.Fatalf("the batch close lost the menu control's focus")
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		if !tt.Focused(glyph) {
			t.Fatalf("the menu control's focus did not survive the settled batch close")
		}
	})

	t.Run("CloseOtherTabs keeps the focused menu", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		active := g.state.ActiveTab()
		focusMenu(t, g, tt, active)
		generation := reveal.strip.generation

		if !g.state.CloseOtherTabs(active) {
			t.Fatalf("CloseOtherTabs failed")
		}
		tt.Frame()
		if g.state.TabCount() != 1 {
			t.Fatalf("tabs = %d after CloseOtherTabs, want 1", g.state.TabCount())
		}
		if reveal.strip.generation != generation {
			t.Fatalf("CloseOtherTabs renamed the row (%d to %d)", generation, reveal.strip.generation)
		}
		if !tt.Focused(glyph) {
			t.Fatalf("CloseOtherTabs lost the menu control's focus")
		}
	})

	t.Run("Close Other Tabs menu keeps the focused menu", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 4, 1600, 600)
		if err := tt.Click(glyph); err != nil {
			t.Fatalf("the menu control cannot be clicked: %v", err)
		}
		if !tt.Focused(glyph) {
			t.Fatalf("the opened menu control does not hold the focus")
		}
		generation := reveal.strip.generation

		if err := tt.ChooseMenuItem(message.TabCloseOthers()); err != nil {
			t.Fatalf("Close Other Tabs: %v", err)
		}
		tt.Frame()
		if g.state.TabCount() != 1 {
			t.Fatalf("tabs = %d after Close Other Tabs, want 1", g.state.TabCount())
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the menu's batch close renamed the row (%d to %d)",
				generation, reveal.strip.generation)
		}
		if !tt.Focused(glyph) {
			t.Fatalf("the Close Other Tabs menu path lost the menu control's focus")
		}
	})

	t.Run("overflow close clamp keeps the focused menu", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 6, 720, 480)
		if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
			t.Fatalf("the strip is not scrolled at 6 Tabs (X=%v MaxX=%v); the case needs overflow",
				reveal.scroll.X, reveal.scroll.MaxX)
		}
		focusMenu(t, g, tt, g.state.ActiveTab())
		generation, frames, beforeX := reveal.strip.generation, reveal.strip.animationFrames, reveal.scroll.X

		if !g.state.CloseTab(1) {
			t.Fatalf("CloseTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabClose)
		if reveal.scroll.X >= beforeX-0.5 {
			t.Fatalf("the close did not clamp the scroll (X=%v from %v); the case needs the jump",
				reveal.scroll.X, beforeX)
		}
		if reveal.strip.run != nil || reveal.strip.animationFrames != frames {
			t.Fatalf("the clamped close animated: run %+v, +%d frames",
				reveal.strip.run, reveal.strip.animationFrames-frames)
		}
		if reveal.strip.generation != generation {
			t.Fatalf("the clamped close renamed the row (%d to %d)", generation, reveal.strip.generation)
		}
		if !tt.Focused(glyph) {
			t.Fatalf("the clamped close lost the menu control's focus")
		}
	})

	t.Run("switch interrupting a reorder restores the focused menu", func(t *testing.T) {
		g, tt, _, reveal := tabStripWindowWithReveal(t, tabStripPreferences(), 3, 1600, 600)
		ids := []model.TabID{g.mustTabID(t, 0), g.mustTabID(t, 1), g.mustTabID(t, 2)}
		g.state.SelectTab(0)
		tt.Frame()
		tt.Advance(400 * time.Millisecond)
		tt.Frame()

		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Frame()
		stripEvent(t, g.state, motion.TabReorder)
		if reveal.strip.run == nil {
			t.Fatalf("the reorder did not start a strip run")
		}
		frames := reveal.strip.animationFrames
		tt.Advance(90 * time.Millisecond)
		tt.Frame()
		if reveal.strip.animationFrames <= frames {
			t.Fatalf("the reorder asked for no animation frames")
		}

		// Hover the Tab the switch will choose: an inactive Tab shows its
		// controls while hovered, so they can take the keyboard focus before
		// the switch makes it active. Its menu control sits between the title
		// and the close control at the Tab's trailing edge.
		box := stripTravel(t, tt, ids[1])
		tt.Move(box.X+box.W/2, box.Y+box.H/2)
		tt.Frame()
		tt.ClickAt(box.X+box.W-47, box.Y+box.H/2)
		tt.CloseMenu()
		tt.Frame()
		if !tt.Focused(glyph) {
			t.Fatalf("the hovered Tab's menu control does not hold the focus")
		}
		if reveal.strip.focusedTab != ids[1] || reveal.strip.focusedControl != kit.TabFocusMenu {
			t.Fatalf("the focused control recorded %q/%d, want Tab %q's menu",
				reveal.strip.focusedTab, reveal.strip.focusedControl, ids[1])
		}
		if reveal.strip.run == nil {
			t.Fatalf("the reorder ended before the switching Tab's focus")
		}
		generation := reveal.strip.generation

		target, ok := g.state.TabIndexByID(ids[1])
		if !ok {
			t.Fatalf("the focused Tab is gone")
		}
		g.state.SelectTab(target)
		tt.Frame()
		stripEvent(t, g.state, motion.TabSwitch)
		if reveal.strip.run != nil {
			t.Fatalf("the switch left a strip run: %+v", reveal.strip.run)
		}
		if reveal.strip.generation == generation {
			t.Fatalf("the interrupting switch did not remount the row; the case needs the remount")
		}
		if !tt.Focused(glyph) {
			t.Fatalf("the interrupting switch lost the menu control's focus")
		}
		tt.Advance(30 * time.Millisecond)
		tt.Frame()
		if !tt.Focused(glyph) {
			t.Fatalf("the restored menu focus did not survive the frame after the switch")
		}
	})
}

// TestTabStripStaleRenameDuplicateWarpsOnTheEventPaint is the live-paint
// regression for a prediction whose recorded content is one build older than
// the row it describes. A Tab renamed to its maximum width is built once, then
// duplicated: the strip's recorded content is still the pre-rename width, and
// LayoutBounds on the rename's own build still reports the old title's width.
// Only the per-Tab title record knows the width is stale. Without it the
// DuplicateTab event paint starts the enter: the opened Tab paints collapsed
// under the reveal's scroll jump.
func TestTabStripStaleRenameDuplicateWarpsOnTheEventPaint(t *testing.T) {
	const title = "zzzz-this-is-a-max-width-title-zzzz-zzzz"
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 1, 800, 480,
		func(t *testing.T, g *realTerminalGraph) {
			if !g.state.RenameTab(0, "a") {
				t.Fatalf("RenameTab failed")
			}
		})
	first := g.mustTabID(t, 0)
	if !g.state.RenameTab(0, title) {
		t.Fatalf("RenameTab failed")
	}
	// One build draws the new title. The strip's next prediction would still
	// read the pre-rename content, so the title record must mark the tab.
	tt.Advance(17 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the rename started a strip run: %+v", reveal.strip.run)
	}
	preFrames, preBuilds, preGeneration := reveal.strip.animationFrames, reveal.strip.frames, reveal.strip.generation

	if err := g.state.DuplicateTab(0); err != nil {
		t.Fatalf("DuplicateTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 2)
	opened := g.mustTabID(t, 1)
	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the stale-width duplicate started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the stale-width duplicate asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the stale-width duplicate remounted the row on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
		t.Fatalf("the reveal did not scroll the overflowing strip on the event paint (X=%v MaxX=%v)",
			reveal.scroll.X, reveal.scroll.MaxX)
	}
	eventScroll := reveal.scroll.X
	eventOpened := stripTravel(t, tt, opened)
	if abs32(eventOpened.W-kit.WorkTabMaxWidth) > 1 {
		t.Fatalf("the duplicated Tab paints width %v on the event paint, want its final %v",
			eventOpened.W, kit.WorkTabMaxWidth)
	}
	eventFirst := stripTravel(t, tt, first)

	tt.Advance(17 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the paint after the event started a strip run: %+v", reveal.strip.run)
	}
	if got := stripTravel(t, tt, opened); !rectNearOK(got, eventOpened, 1) {
		t.Fatalf("the duplicated Tab paints %+v on the next paint, want the event paint's %+v",
			got, eventOpened)
	}
	if got := stripTravel(t, tt, first); !rectNearOK(got, eventFirst, 1) {
		t.Fatalf("the first Tab paints %+v on the next paint, want the event paint's %+v",
			got, eventFirst)
	}

	tt.Advance(400 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the settled strip still runs: %+v", reveal.strip.run)
	}
	if got := stripTravel(t, tt, opened); !rectNearOK(got, eventOpened, 1) {
		t.Fatalf("the duplicated Tab settles at %+v, want the event paint's %+v", got, eventOpened)
	}
	if got := stripTravel(t, tt, first); !rectNearOK(got, eventFirst, 1) {
		t.Fatalf("the first Tab settles at %+v, want the event paint's %+v", got, eventFirst)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 {
		t.Fatalf("the reveal scroll moved from %v to %v after the event paint", eventScroll, reveal.scroll.X)
	}
}

// TestTabStripMultipleOpensWarpOnTheEventPaint covers opens that arrive
// together: a prediction that accounts for only one new Tab lets the event
// paint start an enter whose reveal immediately overflows, and every new Tab
// paints collapsed. Both the same-frame burst and the one-frame-apart pair
// must warp to the final layout on the paint that adds the overflowing Tab.
func TestTabStripMultipleOpensWarpOnTheEventPaint(t *testing.T) {
	checkNoRun := func(t *testing.T, reveal *workTabReveal, preFrames, preBuilds int) {
		t.Helper()
		if got := reveal.strip.frames - preBuilds; got != 1 {
			t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
		}
		if reveal.strip.run != nil {
			t.Fatalf("the overflowing open started a %v run on its event paint: %+v",
				reveal.strip.run.kind, reveal.strip.run)
		}
		if got := reveal.strip.animationFrames - preFrames; got != 0 {
			t.Fatalf("the overflowing open asked for %d strip frames on its event paint", got)
		}
		if reveal.scroll.MaxX <= 0.5 || reveal.scroll.X <= 0.5 {
			t.Fatalf("the reveal did not scroll the overflowing strip on the event paint (X=%v MaxX=%v)",
				reveal.scroll.X, reveal.scroll.MaxX)
		}
	}
	checkFinal := func(t *testing.T, tt *ui.Tester, reveal *workTabReveal, opened []model.TabID) {
		t.Helper()
		eventScroll := reveal.scroll.X
		boxes := make([]ui.Rect, len(opened))
		for index, id := range opened {
			boxes[index] = stripTravel(t, tt, id)
			if boxes[index].W <= 1 {
				t.Fatalf("the opened Tab %q paints collapsed (w=%v) on the event paint",
					id, boxes[index].W)
			}
		}
		tt.Advance(400 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the settled strip still runs: %+v", reveal.strip.run)
		}
		for index, id := range opened {
			if got := stripTravel(t, tt, id); !rectNearOK(got, boxes[index], 1) {
				t.Fatalf("the opened Tab %q paints %+v on the event frame, then %+v, want the final layout at once",
					id, boxes[index], got)
			}
		}
		if abs32(reveal.scroll.X-eventScroll) > 1 {
			t.Fatalf("the reveal scroll moved from %v to %v after the event paint", eventScroll, reveal.scroll.X)
		}
	}

	newGraph := func(t *testing.T) (*realTerminalGraph, *ui.Tester, *workTabReveal) {
		t.Helper()
		return steppedStripGraph(t, tabStripPreferences(), 1, 800, 480,
			func(t *testing.T, g *realTerminalGraph) {
				if !g.state.RenameTab(0, "a") {
					t.Fatalf("RenameTab failed")
				}
			})
	}

	t.Run("same frame", func(t *testing.T) {
		g, tt, reveal := newGraph(t)
		preFrames, preBuilds, preGeneration := reveal.strip.animationFrames, reveal.strip.frames, reveal.strip.generation
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		waitForRealSurfaces(t, g.state, 3)
		opened := []model.TabID{g.mustTabID(t, 1), g.mustTabID(t, 2)}
		tt.Advance(17 * time.Millisecond)
		checkNoRun(t, reveal, preFrames, preBuilds)
		if reveal.strip.generation != preGeneration {
			t.Fatalf("the overflowing open remounted the row on its event paint (%d to %d)",
				preGeneration, reveal.strip.generation)
		}
		checkFinal(t, tt, reveal, opened)
	})

	t.Run("one frame apart", func(t *testing.T) {
		g, tt, reveal := newGraph(t)
		// The first open is a single small Tab and still fits: it must enter
		// normally, so the second open's warp is not an over-correction that
		// stops every open from animating.
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		waitForRealSurfaces(t, g.state, 2)
		firstOpened := g.mustTabID(t, 1)
		tt.Advance(17 * time.Millisecond)
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabOpen {
			t.Fatalf("the fitting first open did not start a TabOpen enter: %+v", reveal.strip.run)
		}

		preFrames, preBuilds, preGeneration := reveal.strip.animationFrames, reveal.strip.frames, reveal.strip.generation
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		waitForRealSurfaces(t, g.state, 3)
		secondOpened := g.mustTabID(t, 2)
		tt.Advance(17 * time.Millisecond)
		checkNoRun(t, reveal, preFrames, preBuilds)
		// The first open's enter is still playing and a body transition may be
		// in flight, so the warping row takes a new generation instead of a
		// window-wide instant: both enters must be gone and both Tabs final.
		if reveal.strip.generation == preGeneration {
			t.Fatalf("the overflow warp did not drop the in-flight enter; the case needs the remount")
		}
		checkFinal(t, tt, reveal, []model.TabID{firstOpened, secondOpened})
	})
}

// TestTabStripReorderLeavesAnInFlightBodySwitch is ticket 20's contract that
// TabReorder gates the strip reorder and the body does not move. Before the
// body runtime ignored the event, the generic "any other event" path warped a
// playing body switch on the reorder frame, cutting it roughly 150 ms short.
func TestTabStripReorderLeavesAnInFlightBodySwitch(t *testing.T) {
	newBodyGraph := func(t *testing.T) (*realTerminalGraph, *ui.Tester, *workspacePaneMotion, *workTabReveal, []model.TabID) {
		t.Helper()
		g := motionGraph(t, tabStripPreferences())
		for range 3 {
			if err := g.state.NewTab(); err != nil {
				t.Fatalf("NewTab: %v", err)
			}
		}
		waitForRealSurfaces(t, g.state, 3)
		reveal := newWorkTabReveal()
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
		tt := ui.NewTester(steppingStripView(view), 1600, 600)
		tt.SetLocale("en")
		tt.Advance(400 * time.Millisecond)
		g.state.SelectTab(0)
		tt.Advance(400 * time.Millisecond)
		ids := make([]model.TabID, 3)
		for index := range ids {
			ids[index] = g.mustTabID(t, index)
		}
		return g, tt, wm, reveal, ids
	}

	t.Run("switch in flight", func(t *testing.T) {
		g, tt, wm, reveal, ids := newBodyGraph(t)
		eventFrames := wm.body.animationFrames
		g.state.SelectTab(1)
		tt.Advance(17 * time.Millisecond)
		if wm.body.phase != bodyOut {
			t.Fatalf("the switch event frame left the body phase %v, want bodyOut", wm.body.phase)
		}
		tt.Advance(90 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("the switch is in phase %v at +90ms, want bodyIn", wm.body.phase)
		}
		phaseEnd := wm.body.phaseEnd
		framesBefore := wm.body.animationFrames

		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Advance(17 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("the reorder cut the body switch to phase %v, want it still in bodyIn", wm.body.phase)
		}
		if wm.body.warpKey != "" || wm.body.warpFrom != "" || wm.body.warpOut != "" {
			t.Fatalf("the reorder warped the body: key=%q from=%q out=%q",
				wm.body.warpKey, wm.body.warpFrom, wm.body.warpOut)
		}
		if wm.body.event.Kind != motion.TabSwitch {
			t.Fatalf("the reorder replaced the body event with %v, want the TabSwitch", wm.body.event.Kind)
		}
		if !wm.body.phaseEnd.Equal(phaseEnd) {
			t.Fatalf("the reorder retimed the body switch from %v to %v", phaseEnd, wm.body.phaseEnd)
		}
		if got := wm.body.animationFrames - framesBefore; got != 1 {
			t.Fatalf("the reorder frame asked for %d body frames, want the run's own one", got)
		}
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder ||
			reveal.strip.run.duration != 180*time.Millisecond {
			t.Fatalf("the strip reorder run = %+v, want a 180ms TabReorder", reveal.strip.run)
		}

		// The body switch keeps playing while the strip reorder runs.
		tt.Advance(90 * time.Millisecond)
		if wm.body.phase != bodyIn {
			t.Fatalf("the body switch ended early at %v while the strip still reordered", wm.body.phase)
		}
		if reveal.strip.run == nil {
			t.Fatalf("the 180ms strip reorder ended before +107ms")
		}

		// Past the body's own final: it ends normally, then the strip's run
		// ends at its 180ms with no remount.
		tt.Advance(100 * time.Millisecond)
		if wm.body.phase != bodyIdle {
			t.Fatalf("the body switch has not reached its normal final: phase %v", wm.body.phase)
		}
		tt.Advance(120 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the strip reorder outlived its 180ms: %+v", reveal.strip.run)
		}
		if wm.body.out.kind != bodySurfaceNone || wm.body.out.ghost != nil {
			t.Fatalf("the body run leaked its outgoing body: %+v", wm.body.out)
		}
		if got := wm.body.animationFrames; got-eventFrames <= 0 {
			t.Fatalf("the switch asked for no body frames at all")
		}
	})

	t.Run("no extra body frames", func(t *testing.T) {
		run := func(t *testing.T, reorder bool) int {
			t.Helper()
			g, tt, wm, _, ids := newBodyGraph(t)
			g.state.SelectTab(1)
			tt.Advance(17 * time.Millisecond)
			frames := wm.body.animationFrames
			tt.Advance(90 * time.Millisecond)
			if reorder && !g.state.MoveTab(ids[0], ids[2], true) {
				t.Fatalf("MoveTab failed")
			}
			tt.Advance(17 * time.Millisecond)
			tt.Advance(90 * time.Millisecond)
			tt.Advance(100 * time.Millisecond)
			tt.Advance(120 * time.Millisecond)
			if wm.body.phase != bodyIdle {
				t.Fatalf("the body switch did not finish: phase %v", wm.body.phase)
			}
			return wm.body.animationFrames - frames
		}
		with, without := run(t, true), run(t, false)
		if with != without {
			t.Fatalf("a reorder during the switch asked for %d body frames, want the switch's own %d",
				with, without)
		}
	})

	t.Run("at rest", func(t *testing.T) {
		g, tt, wm, reveal, ids := newBodyGraph(t)
		framesBefore := wm.body.animationFrames
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Advance(17 * time.Millisecond)
		if wm.body.phase != bodyIdle {
			t.Fatalf("an at-rest reorder started a body run: phase %v", wm.body.phase)
		}
		if got := wm.body.animationFrames - framesBefore; got != 0 {
			t.Fatalf("an at-rest reorder asked for %d body frames", got)
		}
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
			t.Fatalf("the at-rest reorder did not start the strip run: %+v", reveal.strip.run)
		}
		tt.Advance(400 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the strip reorder did not settle: %+v", reveal.strip.run)
		}
		if wm.body.phase != bodyIdle {
			t.Fatalf("the body became active after the strip reorder settled: phase %v", wm.body.phase)
		}
	})
}

// TestTabStripGenerationRemountOpenWarpsOnTheEventPaint is the live-paint
// regression for a prediction with no row to read. A Tab switch cuts an
// in-flight reorder by remounting the row (a body transition holds off the
// window-wide instant), which clears the width records; the remounted row's
// first build records an empty content box. Opening a Tab on the next build
// must warp: a prediction that treats the empty boxes as fitting starts the
// enter, the reveal scrolls the strip under it, and the opened Tab paints
// collapsed at w=0 until the build after cancels it.
func TestTabStripGenerationRemountOpenWarpsOnTheEventPaint(t *testing.T) {
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 2, 720, 480,
		func(t *testing.T, g *realTerminalGraph) {
			if !g.state.RenameTab(0, "a") || !g.state.RenameTab(1, "b") {
				t.Fatalf("RenameTab failed")
			}
		})
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	first, second := g.mustTabID(t, 0), g.mustTabID(t, 1)
	if !g.state.MoveTab(first, second, true) {
		t.Fatalf("MoveTab failed")
	}
	tt.Advance(17 * time.Millisecond)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
		t.Fatalf("the reorder did not start a strip run: %+v", reveal.strip.run)
	}
	g.state.SelectTab(1)
	tt.Advance(17 * time.Millisecond)
	// The switch cuts the reorder by remounting the row, which drops the
	// width records and leaves the remounted row without a content box.
	if reveal.strip.generation == 0 {
		t.Fatalf("the switch did not remount the row; the case needs the empty generation")
	}
	if reveal.boxes.Viewport.W <= 0 || reveal.boxes.Content.W > 0 {
		t.Fatalf("the remounted row's boxes = %+v; the case needs a usable viewport and an empty content",
			reveal.boxes)
	}
	preFrames, preBuilds, preGeneration := reveal.strip.animationFrames, reveal.strip.frames, reveal.strip.generation

	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 3)
	opened := g.mustTabID(t, 2)

	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the remounted row's open started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the remounted row's open asked for %d strip frames on its event paint", got)
	}
	if reveal.strip.generation != preGeneration {
		t.Fatalf("the remounted row's open remounted again on its event paint (%d to %d)",
			preGeneration, reveal.strip.generation)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	if abs32(eventScroll-120.8236) > 1 || abs32(eventMax-146.8236) > 1 {
		t.Fatalf("the event paint scrolls to X=%v MaxX=%v, want X=120.8236 MaxX=146.8236",
			eventScroll, eventMax)
	}
	eventOpened := stripTravel(t, tt, opened)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	if abs32(eventOpened.X-392.9873) > 1 || abs32(eventOpened.W-173.0127) > 1 {
		t.Fatalf("the opened Tab paints %+v on the event paint, want x=392.9873 w=173.0127", eventOpened)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}

	// The event paint is the final layout: nothing moves afterwards.
	tt.Advance(17 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the paint after the event started a strip run: %+v", reveal.strip.run)
	}
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripReattachedRecordOpenWarpsOnTheEventPaint detaches the
// maximum-width Tab, settles, then reattaches it and opens another Tab. A
// detach records no motion event, so the strip keeps the departed Tab's width
// record; a present record the recorded boxes did not draw is not part of the
// recorded content. Trusting it lets the open predict a fit, and the event
// paint shows the opened Tab collapsed at w=0 under the reveal's scroll jump
// until the build after cancels it. The reattach either arrives with the open
// in the same update or on the build before it.
func TestTabStripReattachedRecordOpenWarpsOnTheEventPaint(t *testing.T) {
	for _, tc := range []struct {
		name       string
		width      int
		sameUpdate bool
		wantX      float32
		wantScroll float32
		wantMax    float32
	}{
		{"800 same update", 800, true, 472.9873, 120.8236, 146.8236},
		{"840 same update", 840, true, 512.9873, 80.8236, 106.8236},
		{"840 next build", 840, false, 512.9873, 80.8236, 106.8236},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 2, tc.width, 480,
				func(t *testing.T, g *realTerminalGraph) {
					if !g.state.RenameTab(0, maxWidthTabTitle) || !g.state.RenameTab(1, "a") {
						t.Fatalf("RenameTab failed")
					}
				})
			message := locales.In("en")
			viewport, ok := tt.Find(message.WorkTabsLabel())
			if !ok {
				t.Fatalf("the strip has no viewport label")
			}
			detached := g.mustTabID(t, 0)
			kept := g.mustTabID(t, 1)
			keptBox := stripTravel(t, tt, kept)
			if !g.state.DetachTab(0) {
				t.Fatalf("DetachTab failed")
			}
			tt.Advance(400 * time.Millisecond)
			if g.state.TabCount() != 1 {
				t.Fatalf("tabs = %d after DetachTab, want 1", g.state.TabCount())
			}
			if now := g.mustTabID(t, 0); now != kept {
				t.Fatalf("the kept Tab changed from %q to %q across the detach", kept, now)
			}
			// The detach leaves the width record behind while the boxes already
			// describe a row without it. The open decision bounds the row from
			// the state, so the leaked record cannot undercount it (ticket 20).
			leak, leaked := reveal.strip.widths[detached]
			if !leaked || abs32(leak-kit.WorkTabMaxWidth) > 1 {
				t.Fatalf("the detached Tab's width record = %v (present %v); the case needs the leaked max-width record",
					leak, leaked)
			}
			if keptWidth := reveal.strip.widths[kept]; abs32(keptWidth-keptBox.W) > 1 {
				t.Fatalf("the kept Tab's record = %v; the case needs its %v box", keptWidth, keptBox.W)
			}
			if !g.state.ReattachDetachedTab(detached) {
				t.Fatalf("ReattachDetachedTab failed")
			}
			if !tc.sameUpdate {
				// The reattach's own paint consumes the TabSwitch; the open
				// follows on the next build.
				tt.Advance(17 * time.Millisecond)
			}
			if err := g.state.NewTab(); err != nil {
				t.Fatalf("NewTab: %v", err)
			}
			waitForRealSurfaces(t, g.state, 3)
			opened := g.mustTabID(t, 2)
			preFrames, preBuilds := reveal.strip.animationFrames, reveal.strip.frames

			tt.Advance(17 * time.Millisecond)
			if got := reveal.strip.frames - preBuilds; got != 1 {
				t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
			}
			if reveal.strip.run != nil {
				t.Fatalf("the reattached record's open started a %v run on its event paint: %+v",
					reveal.strip.run.kind, reveal.strip.run)
			}
			if got := reveal.strip.animationFrames - preFrames; got != 0 {
				t.Fatalf("the reattached record's open asked for %d strip frames on its event paint", got)
			}
			eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
			if abs32(eventScroll-tc.wantScroll) > 1 || abs32(eventMax-tc.wantMax) > 1 {
				t.Fatalf("the event paint scrolls to X=%v MaxX=%v, want X=%v MaxX=%v",
					eventScroll, eventMax, tc.wantScroll, tc.wantMax)
			}
			eventOpened := stripTravel(t, tt, opened)
			if eventOpened.W < 1 {
				t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
			}
			if abs32(eventOpened.X-tc.wantX) > 1 || abs32(eventOpened.W-173.0127) > 1 {
				t.Fatalf("the opened Tab paints %+v on the event paint, want x=%v w=173.0127",
					eventOpened, tc.wantX)
			}
			if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
				t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
					eventOpened, viewport)
			}
			if eventReattached := stripTravel(t, tt, detached); eventReattached.W < 1 {
				t.Fatalf("the reattached Tab paints collapsed (w=%v) on the event paint", eventReattached.W)
			}

			tt.Advance(17 * time.Millisecond)
			if reveal.strip.run != nil {
				t.Fatalf("the paint after the event started a strip run: %+v", reveal.strip.run)
			}
			if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
				t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
					eventOpened, next)
			}
			tt.Advance(400 * time.Millisecond)
			if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
				t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
			}
			if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
				t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
					reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
			}
		})
	}
}

// TestTabStripResizeOpenWarpsOnTheResizePaint opens a Tab in the same update
// as a window resize. The prediction's boxes were laid out at the old width,
// so they cannot describe the new strip; predicting from them starts an enter
// whose reveal then scrolls the strip. Each build is captured as it runs, so
// the test reads the paint a window would have shown before any later build
// could cancel the run.
func TestTabStripResizeOpenWarpsOnTheResizePaint(t *testing.T) {
	g := motionGraph(t, tabStripPreferences())
	for range 2 {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, 2)
	if !g.state.RenameTab(0, "a") || !g.state.RenameTab(1, "b") {
		t.Fatalf("RenameTab failed")
	}
	reveal := newWorkTabReveal()
	base, _ := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	type stripPaint struct {
		width              float32
		running            bool
		kind               motion.Kind
		animate, warp      bool
		generation, frames int
		scrollX, scrollMax float32
	}
	var paints []stripPaint
	view := func(c *ui.Context) {
		base(c)
		width, _ := c.Size()
		paint := stripPaint{
			width: width, animate: reveal.strip.animate, warp: reveal.strip.warp,
			generation: reveal.strip.generation, frames: reveal.strip.animationFrames,
			scrollX: reveal.scroll.X, scrollMax: reveal.scroll.MaxX,
		}
		if reveal.strip.run != nil {
			paint.running, paint.kind = true, reveal.strip.run.kind
		}
		paints = append(paints, paint)
		c.Invalidate()
	}
	tt := ui.NewTester(view, 1000, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	if reveal.strip.run != nil {
		t.Fatalf("the setup strip still runs: %+v", reveal.strip.run)
	}

	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForRealSurfaces(t, g.state, 3)
	opened := g.mustTabID(t, 2)
	preFrames, preGeneration := reveal.strip.animationFrames, reveal.strip.generation
	paints = nil

	tt.SetSize(720, 480)
	if len(paints) == 0 {
		t.Fatalf("the resize built no paint")
	}
	firstPaint := paints[0]
	if firstPaint.width != 720 {
		t.Fatalf("the first paint after the resize has window width %v, want 720", firstPaint.width)
	}
	if firstPaint.running {
		t.Fatalf("the resize+open paint started a %v run; the old boxes cannot predict the new strip",
			firstPaint.kind)
	}
	if !firstPaint.warp {
		t.Fatalf("the resize+open paint did not warp the strip")
	}
	if firstPaint.frames != preFrames {
		t.Fatalf("the resize+open paint asked for %d strip frames", firstPaint.frames-preFrames)
	}
	if firstPaint.generation != preGeneration {
		t.Fatalf("the resize+open paint remounted the row (%d to %d)", preGeneration, firstPaint.generation)
	}
	for index, paint := range paints {
		if paint.running && paint.kind == motion.TabOpen {
			t.Fatalf("paint %d after the resize started a TabOpen run", index)
		}
	}
	if reveal.strip.run != nil {
		t.Fatalf("the settled resize still runs: %+v", reveal.strip.run)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	if abs32(eventScroll-120.8236) > 1 || abs32(eventMax-146.8236) > 1 {
		t.Fatalf("the resize paint scrolls to X=%v MaxX=%v, want X=120.8236 MaxX=146.8236",
			eventScroll, eventMax)
	}
	eventOpened := stripTravel(t, tt, opened)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the resize paint", eventOpened.W)
	}
	if abs32(eventOpened.X-392.9873) > 1 || abs32(eventOpened.W-173.0127) > 1 {
		t.Fatalf("the opened Tab paints %+v on the resize paint, want x=392.9873 w=173.0127", eventOpened)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the new strip %+v", eventOpened, viewport)
	}

	tt.Advance(17 * time.Millisecond)
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on the resize paint, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on the resize paint, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the resize paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripDirtyMarkerOpenWarpsOnTheEventPaint covers a width change with
// no new title: a dirty marker takes room in the Tab's row, so a Tab that
// becomes dirty widens without its title changing. The open is sized so the
// clean content plus a maximum-width Tab fits, but the dirty row overflows;
// the state-derived bound measures the marker on the event frame, so the
// paint cannot start an enter on a strip the reveal then overflows.
func TestTabStripDirtyMarkerOpenWarpsOnTheEventPaint(t *testing.T) {
	g, tt, reveal := steppedStripGraph(t, tabStripPreferences(), 1, 816, 480,
		func(t *testing.T, g *realTerminalGraph) {
			if !g.state.RenameTab(0, "medium-title-here") {
				t.Fatalf("RenameTab failed")
			}
		})
	message := locales.In("en")
	viewport, ok := tt.Find(message.WorkTabsLabel())
	if !ok {
		t.Fatalf("the strip has no viewport label")
	}
	dirty := g.mustTabID(t, 0)
	if reveal.boxes.Content.W+kit.WorkTabMaxWidth > reveal.boxes.Viewport.W {
		t.Fatalf("the clean content %v plus a maximum-width Tab does not fit the viewport %v; the case needs a fitting clean prediction",
			reveal.boxes.Content.W, reveal.boxes.Viewport.W)
	}
	cleanBox := stripTravel(t, tt, dirty)
	cleanWidth, recorded := reveal.strip.widths[dirty]
	if !recorded || abs32(cleanWidth-cleanBox.W) > 1 {
		t.Fatalf("the clean Tab's record = %v; the case needs its %v clean box", cleanWidth, cleanBox.W)
	}
	g.state.SetTabDirty(0, true)
	if info, _ := g.state.TabInfo(0); !info.Dirty {
		t.Fatalf("the Tab is not dirty after SetTabDirty")
	}
	if err := g.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if !g.state.RenameTab(1, maxWidthTabTitle) {
		t.Fatalf("RenameTab failed")
	}
	waitForRealSurfaces(t, g.state, 2)
	opened := g.mustTabID(t, 1)
	preFrames, preBuilds := reveal.strip.animationFrames, reveal.strip.frames

	tt.Advance(17 * time.Millisecond)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the step built %d frames, want exactly the event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the dirty-marker open started a %v run on its event paint: %+v",
			reveal.strip.run.kind, reveal.strip.run)
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the dirty-marker open asked for %d strip frames on its event paint", got)
	}
	eventScroll, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	if eventMax <= 0.5 {
		t.Fatalf("the dirty row does not overflow (MaxX=%v); the case needs the post-build overflow",
			eventMax)
	}
	eventOpened := stripTravel(t, tt, opened)
	if eventOpened.W < 1 {
		t.Fatalf("the opened Tab paints collapsed (w=%v) on the event paint", eventOpened.W)
	}
	if abs32(eventOpened.X-402.3696) > 1 || abs32(eventOpened.W-kit.WorkTabMaxWidth) > 1 {
		t.Fatalf("the opened Tab paints %+v on the event paint, want x=402.3696 w=%v",
			eventOpened, kit.WorkTabMaxWidth)
	}
	if eventOpened.X < viewport.X-1 || eventOpened.X+eventOpened.W > viewport.X+viewport.W+1 {
		t.Fatalf("the opened Tab %+v is not revealed inside the strip %+v on the event paint",
			eventOpened, viewport)
	}
	if eventDirty := stripTravel(t, tt, dirty); eventDirty.W < 1 {
		t.Fatalf("the dirty Tab paints collapsed (w=%v) on the event paint", eventDirty.W)
	}

	tt.Advance(17 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the paint after the event started a strip run: %+v", reveal.strip.run)
	}
	if next := stripTravel(t, tt, opened); !rectNearOK(eventOpened, next, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then %+v, want the final layout at once",
			eventOpened, next)
	}
	tt.Advance(400 * time.Millisecond)
	if final := stripTravel(t, tt, opened); !rectNearOK(eventOpened, final, 1) {
		t.Fatalf("the opened Tab painted %+v on its event frame, then settled at %+v", eventOpened, final)
	}
	if abs32(reveal.scroll.X-eventScroll) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventScroll, eventMax)
	}
}

// TestTabStripWidthBoundIncludesTheDirtyMarker pins the open decision's
// marker rule: a marker change without a title change widens the row, so the
// state-derived bound charges it on that frame and still covers the laid-out
// dirty Tab. The close-clamp width record stays a plain measured width.
func TestTabStripWidthBoundIncludesTheDirtyMarker(t *testing.T) {
	const title = "medium-title-here"
	const marker = "●"
	var cleanBound, dirtyBound, cleanActual, dirtyActual float32
	tt := ui.NewTester(func(c *ui.Context) {
		cleanBound = kit.WorkTabWidthBound(c, title, "")
		dirtyBound = kit.WorkTabWidthBound(c, title, marker)
		kit.TabStrip(c, kit.TabWork, kit.TabStripOptions{Height: 36}, func() {
			clean, _ := kit.Tab(c, kit.TabWork, kit.TabSpec{
				Title: title, Close: "x", MenuLabel: "menu", Menu: func(*ui.Menu) {},
				Lead: func() { kit.StatusDot(c, c.Theme().Accent) },
			})
			cleanActual = clean.LayoutBounds().W
			dirty, _ := kit.Tab(c, kit.TabWork, kit.TabSpec{
				Title: title, Marker: marker, Close: "x", MenuLabel: "menu", Menu: func(*ui.Menu) {},
				Lead: func() { kit.StatusDot(c, c.Theme().Accent) },
			})
			dirtyActual = dirty.LayoutBounds().W
		})
	}, 1600, 120)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	if dirtyBound <= cleanBound {
		t.Fatalf("the dirty marker's bound %v is not wider than the clean row's %v", dirtyBound, cleanBound)
	}
	if cleanBound < cleanActual {
		t.Fatalf("the clean bound %v is below the laid-out clean Tab %v", cleanBound, cleanActual)
	}
	if dirtyBound < dirtyActual {
		t.Fatalf("the dirty bound %v is below the laid-out dirty Tab %v", dirtyBound, dirtyActual)
	}
	var strip tabStripMotion
	strip.noteWidth("tab", 212.559)
	if got := strip.widths["tab"]; abs32(got-212.559) > 0.001 {
		t.Fatalf("the close-clamp width record = %v, want the measured 212.559", got)
	}
}
