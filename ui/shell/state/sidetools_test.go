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

func (p *sideProbe) tool(id SideToolID, remoteOnly bool) SideTool {
	tool := SideTool{ID: id, RemoteOnly: remoteOnly, View: func(target SideTarget, _ float32) func(*ui.Context) {
		p.targets = append(p.targets, target)
		return func(*ui.Context) {}
	}}
	if remoteOnly {
		tool.Hide = func() { p.hidden++ }
	}
	return tool
}

func TestSideToolsFollowSFTPAndSkipRemoteOnlyOnALocalTab(t *testing.T) {
	probe := &sideProbe{}
	s := NewState(WithFilesPanel((&filesProbe{}).panel()),
		WithSideTools(probe.tool(SideScripts, false), probe.tool(SideSystem, true)))
	s.ToggleFiles("remote-tab", "h1", "Host one")
	s.ToggleFiles("local-tab", "", "")

	if got := s.SideTools("remote-tab"); !slices.Equal(got, []SideToolID{SideSFTP, SideScripts, SideSystem}) {
		t.Fatalf("remote Tab tools = %v", got)
	}
	if got := s.SideTools("local-tab"); !slices.Equal(got, []SideToolID{SideSFTP, SideScripts}) {
		t.Fatalf("local Tab tools = %v, want no System", got)
	}
	if s.SideTool("remote-tab") != SideSFTP {
		t.Fatalf("the first tool is %q, want SFTP", s.SideTool("remote-tab"))
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
	s.SetSideTool("local", SideSystem)
	if s.SideTool("local") != SideSFTP {
		t.Fatalf("a local Tab shows %q", s.SideTool("local"))
	}
}
