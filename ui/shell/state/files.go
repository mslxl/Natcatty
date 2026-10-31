package state

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// FilesPanel is the SFTP panel a Session Tab shows beside its terminal. The
// app wires one panel state for the window; the shell shows it for the Tab
// that opened it and points it at that Tab's Host.
type FilesPanel struct {
	// View renders the panel's SFTP tool about width DIPs wide.
	View func(width float32) func(*ui.Context)
	// ShowRemote browses a Host, keeping a listing it already has.
	ShowRemote func(hostID model.HostID, label string)
	// ShowLocal browses this computer, for a local Session.
	ShowLocal func()
	// Dock reads the "Side panel position" setting and SetDock changes it.
	// Without them the panel stays at DockLeft, the original's default.
	Dock    func() PanelDock
	SetDock func(PanelDock)
}

// PanelDock is the edge of the Tab the side panel sits at.
type PanelDock string

const (
	DockLeft   PanelDock = "left"
	DockRight  PanelDock = "right"
	DockBottom PanelDock = "bottom"
)

// The side panel's size, as the original's: 420 DIPs wide by default, between
// 280 and 1200, and the terminal always keeps 320 beside it. Docked at the
// bottom it takes a share of the Tab's height instead.
const (
	filesDefaultWidth  float32 = 420
	filesMinWidth      float32 = 280
	filesMaxWidth      float32 = 1200
	filesTerminalWidth float32 = 320
	filesDefaultRatio  float32 = 62
)

// filesTarget is what a Tab's panel browses: a Host, or this computer when
// host is empty.
type filesTarget struct {
	host  model.HostID
	label string
}

// WithFilesPanel gives Session Tabs their SFTP side panel. Without it the
// Pane toolbar has no Files button.
func WithFilesPanel(panel FilesPanel) Option {
	return func(s *State) { s.filesPanel = &panel }
}

// FilesAvailable reports whether Session Tabs can open the SFTP panel.
func (s *State) FilesAvailable() bool { return s.filesPanel != nil && s.filesPanel.View != nil }

// FilesOpen reports whether the Tab shows the SFTP panel.
func (s *State) FilesOpen(tab model.TabID) bool {
	_, open := s.filesOpen[tab]
	return open
}

// ToggleFiles opens the Tab's SFTP panel on host, or on this computer when
// host is empty, and closes it when it already shows that target.
func (s *State) ToggleFiles(tab model.TabID, host model.HostID, label string) {
	if !s.FilesAvailable() || tab == "" {
		return
	}
	target := filesTarget{host: host, label: label}
	if current, open := s.filesOpen[tab]; open && current.host == target.host {
		s.CloseFiles(tab)
		return
	}
	if s.filesOpen == nil {
		s.filesOpen = map[model.TabID]filesTarget{}
	}
	s.filesOpen[tab] = target
	s.redraw()
}

// CloseFiles closes the Tab's SFTP panel.
func (s *State) CloseFiles(tab model.TabID) {
	if _, open := s.filesOpen[tab]; !open {
		return
	}
	delete(s.filesOpen, tab)
	s.redraw()
}

// ToggleActiveFiles is the "Toggle side panel" shortcut: it opens the active
// Tab's panel on the focused Session's Host, or closes it when it is open.
// It reports false when the focused Pane has no file system to show.
func (s *State) ToggleActiveFiles() bool {
	tab, _ := s.activeTab()
	if tab == nil || !s.FilesAvailable() {
		return false
	}
	if s.FilesOpen(tab.ID) {
		s.CloseFiles(tab.ID)
		return true
	}
	value, _ := tab.focusedPane()
	if value == nil || value.session == nil {
		return false
	}
	session := value.session
	switch session.Snapshot().Kind {
	case appsession.KindSSH:
		if session.HostID() == "" {
			return false
		}
		s.ToggleFiles(tab.ID, session.HostID(), value.descriptor.Label)
	case appsession.KindLocal:
		s.ToggleFiles(tab.ID, "", "")
	default:
		return false
	}
	return true
}

// FilesDock is the edge the side panel sits at.
func (s *State) FilesDock() PanelDock {
	if s.filesPanel != nil && s.filesPanel.Dock != nil {
		switch dock := s.filesPanel.Dock(); dock {
		case DockLeft, DockRight, DockBottom:
			return dock
		}
	}
	return DockLeft
}

// SetFilesDock moves the side panel to an edge of the Tab.
func (s *State) SetFilesDock(dock PanelDock) {
	if s.filesPanel != nil && s.filesPanel.SetDock != nil {
		s.filesPanel.SetDock(dock)
	}
	s.redraw()
}

// FilesWidth is the docked-left or docked-right panel's width in DIPs,
// shared by every Tab so the panel keeps the width the user dragged it to.
// within is the Tab's width; the terminal keeps filesTerminalWidth of it.
func (s *State) FilesWidth(within float32) *float32 {
	if s.filesWidth <= 0 {
		s.filesWidth = filesDefaultWidth
	}
	limit := filesMaxWidth
	if within > 0 {
		limit = min(limit, within-filesTerminalWidth)
	}
	s.filesWidth = min(max(s.filesWidth, filesMinWidth), max(limit, filesMinWidth))
	return &s.filesWidth
}

// FilesRatio is the share of the Tab's height the terminal keeps above a
// panel docked at the bottom.
func (s *State) FilesRatio() *float32 {
	if s.filesRatio <= 0 || s.filesRatio >= 100 {
		s.filesRatio = filesDefaultRatio
	}
	return &s.filesRatio
}

// FilesView returns the panel's SFTP tool for the Tab, about width DIPs wide
// and pointed at the Tab's target, or nil when the Tab's panel is closed.
func (s *State) FilesView(tab model.TabID, width float32) func(*ui.Context) {
	target, open := s.filesOpen[tab]
	if !open || !s.FilesAvailable() {
		return nil
	}
	if s.filesShown == nil || *s.filesShown != target {
		shown := target
		s.filesShown = &shown
		if target.host == "" {
			if s.filesPanel.ShowLocal != nil {
				s.filesPanel.ShowLocal()
			}
		} else if s.filesPanel.ShowRemote != nil {
			s.filesPanel.ShowRemote(target.host, target.label)
		}
	}
	return s.filesPanel.View(width)
}

func (s *State) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
