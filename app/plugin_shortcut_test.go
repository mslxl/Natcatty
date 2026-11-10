package app

// The shortcut-precedence integration test lives in the composition root
// because it needs both the real plugin runtime (a committed WASM guest) and
// MyGo's real window accelerator table. It constructs the same View Pane
// surface the app builds, feeds it the app's real window shortcut table, and
// drives it through MyGo's headless window, so a window accelerator and a
// capturing plugin node meet in the shipped routing path.

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appview "github.com/mslxl/natcatty/application/view"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/pluginview"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// TestPluginViewKeyCaptureBeatsWindowAccelerator proves the two halves of the
// shortcut precedence rule through the production surface builder:
//
//   - a host accelerator wins while the focused plugin node does not capture
//     keys: the host action runs and the guest receives no key,
//   - the same chord reaches the guest, and the host action does not run, once
//     that focused node declares key capture.
func TestPluginViewKeyCaptureBeatsWindowAccelerator(t *testing.T) {
	run := startCounterSample(t, true)

	// The real window manager owns the accelerator table the app binds at
	// each window root. One menu accelerator is enough to exercise the
	// shipped path; MenuAccelerators builds it from the menu, as the app does.
	windows := newWindowManager(nil, nil, nil, nil, nil, nil)
	hostRuns := 0
	saveMenu := mygo.NewMenu([]*mygo.MenuItem{{
		ID:          "menu.shortcutTestSave",
		Label:       "Save",
		Accelerator: "CmdOrCtrl+S",
		Click: func(*mygo.MenuItem, *mygo.Window) {
			hostRuns++
		},
	}})
	windows.SetAccelerators(MenuAccelerators(saveMenu))
	if len(windows.accelerators) != 1 {
		t.Fatalf("window accelerators = %+v", windows.accelerators)
	}

	// The app opens the view through its real registry and builder. Poll is
	// left nil so the test can read the real session's event queue after a
	// frame; polling itself is covered by the counter view e2e.
	shell := state.NewState(state.WithViews(run.views,
		func(instance *appview.Instance) (state.Surface, error) {
			return newPluginViewSurface(instance,
				func() *appplugin.Manager { return run.manager },
				pluginViewHost{
					Environment: func() appplugin.UIEnvironment {
						return appplugin.UIEnvironment{
							Locale:    "en",
							Direction: appplugin.UIDirectionLTR,
							Shortcuts: windows.HostShortcuts(),
						}
					},
					RunCommand: func(command string) {
						if !windows.RunHostShortcut(command) {
							t.Errorf("unknown host shortcut token %q", command)
						}
					},
				},
				func() {})
		}))
	if _, err := shell.OpenView(appview.OpenRequest{
		ViewID: counterSampleViewID, Placement: domainplugin.PlacementPane,
	}); err != nil {
		t.Fatalf("OpenView: %v", err)
	}
	paneID, ok := shell.FocusedPaneID(0)
	if !ok {
		t.Fatal("the View Tab has no focused Pane")
	}
	info, ok := shell.PaneInfoAt(0, paneID)
	if !ok || info.Surface == nil {
		t.Fatalf("View Pane = %+v, ok = %v", info, ok)
	}
	focused := info.Surface.(state.FocusedSurface)

	uiSession, ok := run.manager.Container().NativeUI(appplugin.PluginRef{
		ID: counterSampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the sample has no live UI session")
	}
	inputHandle, ok := uiSession.NodeHandle(counterSampleInputNode)
	if !ok {
		t.Fatal("the sample input has no handle")
	}
	if _, err := uiSession.Listen(domainnativeui.HandleFromNode(inputHandle), domainnativeui.EventKeyDown); err != nil {
		t.Fatalf("listen key_down: %v", err)
	}
	if _, err := uiSession.Listen(domainnativeui.HandleFromNode(inputHandle), domainnativeui.EventKeyUp); err != nil {
		t.Fatalf("listen key_up: %v", err)
	}

	// Render the Pane the way the shell does: inside a container element, so
	// the plugin view's own shortcut registration is element-scoped, and
	// behind the window manager's real accelerator binding, as every window
	// is. The named Pane is focused.
	tt := ui.NewTester(func(c *ui.Context) {
		windows.withAccelerators(func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() {
				focused.ViewFocused(c, true)
			})
		})(c)
	}, 640, 480)
	tt.Frame()
	shell.SelectTab(0)
	tt.Frame()

	// The production builder fed the live accelerator table to the session.
	want, ok := pluginview.NativeShortcut(ui.Cmd, ui.KeyS, "")
	if !ok {
		t.Fatal("Cmd+S does not convert to a native shortcut")
	}
	env := uiSession.Environment()
	if len(env.Shortcuts) != 1 || env.Shortcuts[0].Mods != want.Mods || env.Shortcuts[0].Key != want.Key {
		t.Fatalf("session environment shortcuts = %+v, want %+v", env.Shortcuts, want)
	}
	if env.Locale != "en" || env.Direction != appplugin.UIDirectionLTR {
		t.Fatalf("session environment = %+v", env)
	}
	// Start from an empty queue; the guest never registered a session event.
	uiSession.Poll(0)

	// Host shortcut, no capture: the host action runs and no key reaches the
	// guest, neither on the press nor on the release.
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if hostRuns != 1 {
		t.Fatalf("host command runs = %d, want 1", hostRuns)
	}
	if events := uiSession.Poll(0); len(events) != 0 {
		t.Fatalf("an uncaptured host shortcut reached the guest: %+v", events)
	}

	// The focused input declares key capture. This is the same atomic patch
	// path the guest uses through ui.apply.
	if _, err := uiSession.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{{
		Kind:  domainnativeui.OpSetProp,
		Node:  domainnativeui.IDFromNode(counterSampleInputNode),
		Prop:  domainnativeui.PropKeyCapture,
		Value: domainnativeui.BoolValue(true),
	}}}); err != nil {
		t.Fatalf("set key capture: %v", err)
	}
	tt.Frame()
	if !uiSession.CapturesKeys() {
		t.Fatal("the focused input did not declare key capture")
	}

	// The same chord now reaches the guest, and the host action does not run.
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if hostRuns != 1 {
		t.Fatalf("a capturing node let the host command run: %d runs", hostRuns)
	}
	events := uiSession.Poll(0)
	var downs, ups []domainnativeui.Event
	for _, event := range events {
		switch event.Kind {
		case domainnativeui.EventKeyDown:
			downs = append(downs, event)
		case domainnativeui.EventKeyUp:
			ups = append(ups, event)
		}
	}
	if len(downs) != 1 || downs[0].Key != domainnativeui.KeyS || downs[0].Mods != want.Mods {
		t.Fatalf("captured key_down events = %+v", downs)
	}
	if downs[0].Node != domainnativeui.HandleFromNode(inputHandle) {
		t.Fatalf("key_down node = %#x, want the focused input", uint64(downs[0].Node))
	}
	if len(ups) != 1 || ups[0].Key != domainnativeui.KeyS || ups[0].Mods != want.Mods {
		t.Fatalf("captured key_up events = %+v", ups)
	}

	// Releasing capture returns the chord to the host accelerator.
	if _, err := uiSession.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{{
		Kind:  domainnativeui.OpSetProp,
		Node:  domainnativeui.IDFromNode(counterSampleInputNode),
		Prop:  domainnativeui.PropKeyCapture,
		Value: domainnativeui.BoolValue(false),
	}}}); err != nil {
		t.Fatalf("clear key capture: %v", err)
	}
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if hostRuns != 2 {
		t.Fatalf("host command runs after capture cleared = %d, want 2", hostRuns)
	}
	if events := uiSession.Poll(0); len(events) != 0 {
		t.Fatalf("a cleared capture still reached the guest: %+v", events)
	}
	shell.CloseTab(0)
}
