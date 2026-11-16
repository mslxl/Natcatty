package state

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

type sideProbe struct {
	targets []SideTarget
	hidden  int
}

func (p *sideProbe) tool(id SideToolID, sampled bool) SideTool {
	tool := SideTool{ID: id, View: func(target SideTarget, _ float32) func(*ui.Context) {
		p.targets = append(p.targets, target)
		return func(*ui.Context) {}
	}}
	if sampled {
		tool.Hide = func() { p.hidden++ }
	}
	return tool
}

// A local Session's panel offers every tool a remote one does: System samples
// this computer as it samples a Host.
func TestSideToolsFollowSFTPOnLocalAndRemoteTabsAlike(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithFilesPanel((&filesProbe{}).panel()),
		WithSideTools(probe.tool(SideScripts, false), probe.tool(SideSystem, true)))
	s.ToggleFiles("remote-tab", "h1", "Host one")
	s.ToggleFiles("local-tab", "", "")

	want := []SideToolID{SideSFTP, SideScripts, SideSystem}
	if got := s.SideTools("remote-tab"); !slices.Equal(got, want) {
		t.Fatalf("remote Tab tools = %v", got)
	}
	if got := s.SideTools("local-tab"); !slices.Equal(got, want) {
		t.Fatalf("local Tab tools = %v, want %v", got, want)
	}
	if s.SideTool("remote-tab") != SideSFTP {
		t.Fatalf("the first tool is %q, want SFTP", s.SideTool("remote-tab"))
	}
	s.SetSideTool("local-tab", SideSystem)
	s.FilesView("local-tab", 300)
	if len(probe.targets) != 1 || probe.targets[0].Host != "" {
		t.Fatalf("System on the local Tab was pointed at %+v, want this computer", probe.targets)
	}
}

func TestSideToolsWithoutSFTPStillOpenThePanel(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithSideTools(probe.tool(SideNotes, false)))
	if !s.FilesAvailable() {
		t.Fatal("a panel with only Notes is unavailable")
	}
	s.ToggleFiles("tab", "", "")
	if s.SideTool("tab") != SideNotes || s.FilesView("tab", 300) == nil {
		t.Fatalf("the panel shows %q", s.SideTool("tab"))
	}
}

// Side-frame bookkeeping is per renderer: the main window and the dropdown
// can draw the same tool, and neither may hide it while the other still draws
// it. It is hidden exactly once, when the last renderer stops.
func TestSideFrameBookkeepingIsIsolatedPerRenderer(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithFilesPanel((&filesProbe{}).panel()),
		WithSideTools(probe.tool(SideSystem, true)))
	s.ToggleFiles("tab", "h1", "Host one")
	s.SetSideTool("tab", SideSystem)

	// Frame 1: the main window draws System.
	s.FilesViewFor(RenderOwnerMain, "tab", 300)
	s.FinishSideFrameFor(RenderOwnerMain)
	if probe.hidden != 0 {
		t.Fatalf("System was hidden while the main window drew it: %d", probe.hidden)
	}

	// Frame 2: both renderers draw the same tool. Neither finish may stop it
	// for the other.
	s.FilesViewFor(RenderOwnerMain, "tab", 300)
	s.FilesViewFor(RenderOwnerDropdown, "tab", 300)
	s.FinishSideFrameFor(RenderOwnerMain)
	s.FinishSideFrameFor(RenderOwnerDropdown)
	if probe.hidden != 0 {
		t.Fatalf("a renderer hid the tool another drew: %d", probe.hidden)
	}

	// Frame 3: only the dropdown draws. The main window has stopped, but its
	// finish must still see the dropdown's draw and leave the tool alone.
	s.FilesViewFor(RenderOwnerDropdown, "tab", 300)
	s.FinishSideFrameFor(RenderOwnerMain)
	s.FinishSideFrameFor(RenderOwnerDropdown)
	if probe.hidden != 0 {
		t.Fatalf("the main window hid System while the dropdown drew it: %d", probe.hidden)
	}

	// Frame 4: the last renderer stops drawing it. The tool stops exactly once.
	s.FinishSideFrameFor(RenderOwnerDropdown)
	if probe.hidden != 1 {
		t.Fatalf("System was hidden %d times after the last renderer stopped, want 1", probe.hidden)
	}
	s.FinishSideFrameFor(RenderOwnerMain)
	if probe.hidden != 1 {
		t.Fatalf("System was hidden again: %d", probe.hidden)
	}
}

// When both renderers stop showing a tool in the same frame, its Hide runs
// once rather than once per renderer.
func TestSideFrameHidesOnceWhenBothRenderersStopTogether(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithFilesPanel((&filesProbe{}).panel()),
		WithSideTools(probe.tool(SideSystem, true)))
	s.ToggleFiles("tab", "h1", "Host one")
	s.SetSideTool("tab", SideSystem)

	s.FilesViewFor(RenderOwnerMain, "tab", 300)
	s.FilesViewFor(RenderOwnerDropdown, "tab", 300)
	s.FinishSideFrameFor(RenderOwnerMain)
	s.FinishSideFrameFor(RenderOwnerDropdown)

	// The next frame draws nothing in either renderer.
	s.FinishSideFrameFor(RenderOwnerMain)
	s.FinishSideFrameFor(RenderOwnerDropdown)
	if probe.hidden != 1 {
		t.Fatalf("System was hidden %d times, want exactly 1", probe.hidden)
	}
}

// The default files path keeps the main window's bookkeeping, so callers that
// do not name a renderer behave exactly as before.
func TestDefaultSideFrameBookkeepingIsTheMainRenderer(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithFilesPanel((&filesProbe{}).panel()),
		WithSideTools(probe.tool(SideSystem, true)))
	s.ToggleFiles("tab", "h1", "Host one")
	s.SetSideTool("tab", SideSystem)

	s.FilesView("tab", 300)
	s.FinishSideFrame()
	if probe.hidden != 0 {
		t.Fatal("the default renderer hid a tool it still draws")
	}
	s.FinishSideFrameFor(RenderOwnerMain)
	if probe.hidden != 1 {
		t.Fatalf("default FinishSideFrame and FinishSideFrameFor(main) disagreed: %d", probe.hidden)
	}
}

func TestEachTabRemembersItsToolAndAHiddenToolIsTold(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithFilesPanel((&filesProbe{}).panel()),
		WithSideTools(probe.tool(SideHistory, false), probe.tool(SideSystem, true)))
	s.ToggleFiles("a", "h1", "Host one")
	s.ToggleFiles("b", "h2", "Host two")

	s.SetSideTool("a", SideSystem)
	if s.SideTool("a") != SideSystem || s.SideTool("b") != SideSFTP {
		t.Fatalf("tools a=%q b=%q", s.SideTool("a"), s.SideTool("b"))
	}
	s.FilesView("a", 300)
	s.FinishSideFrame()
	if len(probe.targets) != 1 || probe.targets[0].Host != "h1" || probe.targets[0].Label != "Host one" {
		t.Fatalf("System was pointed at %+v", probe.targets)
	}
	if probe.hidden != 0 {
		t.Fatal("System was hidden while drawn")
	}

	// Showing Tab b draws SFTP, so System stops once that frame is done.
	s.FilesView("b", 300)
	s.FinishSideFrame()
	if probe.hidden != 1 {
		t.Fatalf("System was hidden %d times after it left the screen, want 1", probe.hidden)
	}
	s.FilesView("b", 300)
	s.FinishSideFrame()
	if probe.hidden != 1 {
		t.Fatalf("System was hidden again: %d", probe.hidden)
	}

	// A tool the Tab no longer offers falls back to the first one.
	s.ToggleFiles("local", "", "")
	s.SetSideTool("local", "gone")
	if s.SideTool("local") != SideSFTP {
		t.Fatalf("a Tab that chose a missing tool shows %q", s.SideTool("local"))
	}
}
