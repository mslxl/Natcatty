package state

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
)

type filesProbe struct {
	remote []model.HostID
	local  int
	width  float32
	dock   PanelDock
}

func (p *filesProbe) panel() FilesPanel {
	return FilesPanel{
		View: func(width float32) func(*ui.Context) {
			p.width = width
			return func(*ui.Context) {}
		},
		ShowRemote: func(host model.HostID, _ string) { p.remote = append(p.remote, host) },
		ShowLocal:  func() { p.local++ },
		Dock:       func() PanelDock { return p.dock },
		SetDock:    func(dock PanelDock) { p.dock = dock },
	}
}

func TestFilesPanelDocksLeftUnlessTheSettingSaysOtherwise(t *testing.T) {
	probe := &filesProbe{}
	s := NewState(WithFilesPanel(probe.panel()))
	if got := s.FilesDock(); got != DockLeft {
		t.Fatalf("an unset dock is %q, want the original's left", got)
	}
	s.SetFilesDock(DockBottom)
	if probe.dock != DockBottom || s.FilesDock() != DockBottom {
		t.Fatalf("moving the panel stored %q and reads %q", probe.dock, s.FilesDock())
	}
	probe.dock = "ceiling"
	if got := s.FilesDock(); got != DockLeft {
		t.Fatalf("an unknown dock reads %q, want left", got)
	}
}

func TestFilesPanelWidthKeepsTheOriginalsBounds(t *testing.T) {
	s := NewState(WithFilesPanel((&filesProbe{}).panel()))
	if got := *s.FilesWidth(1280); got != filesDefaultWidth {
		t.Fatalf("the default width is %v, want %v", got, filesDefaultWidth)
	}
	*s.FilesWidth(1280) = 100
	if got := *s.FilesWidth(1280); got != filesMinWidth {
		t.Fatalf("a dragged-narrow panel is %v wide, want the %v minimum", got, filesMinWidth)
	}
	*s.FilesWidth(1280) = 5000
	if got := *s.FilesWidth(1280); got != 1280-filesTerminalWidth {
		t.Fatalf("a dragged-wide panel is %v wide, want the terminal to keep %v", got, filesTerminalWidth)
	}
	if got := *s.FilesWidth(4000); got > filesMaxWidth {
		t.Fatalf("the panel grew to %v, past the %v maximum", got, filesMaxWidth)
	}
}

// TestFilesViewPointsThePanelAtTheTabsHost covers the shell half of the
// remote-panel regression: each Tab's panel is pointed at its own target,
// and switching back to a Tab points it again.
func TestFilesViewPointsThePanelAtTheTabsHost(t *testing.T) {
	probe := &filesProbe{}
	s := NewState(WithFilesPanel(probe.panel()))
	s.ToggleFiles("remote-tab", "h1", "Host one")
	s.ToggleFiles("local-tab", "", "")

	if s.FilesView("remote-tab", 420) == nil {
		t.Fatal("an open panel has no view")
	}
	if len(probe.remote) != 1 || probe.remote[0] != "h1" || probe.local != 0 {
		t.Fatalf("the remote Tab's panel showed remote=%v local=%d", probe.remote, probe.local)
	}
	if probe.width != 420 {
		t.Fatalf("the panel was told it is %v wide, want 420", probe.width)
	}
	s.FilesView("local-tab", 420)
	if probe.local != 1 {
		t.Fatalf("the local Tab's panel did not show this computer: local=%d", probe.local)
	}
	s.FilesView("remote-tab", 420)
	if len(probe.remote) != 2 || probe.remote[1] != "h1" {
		t.Fatalf("returning to the remote Tab did not point the panel back: %v", probe.remote)
	}

	s.ToggleFiles("remote-tab", "h1", "Host one")
	if s.FilesOpen("remote-tab") || s.FilesView("remote-tab", 420) != nil {
		t.Fatal("toggling the open panel's target did not close it")
	}
}
