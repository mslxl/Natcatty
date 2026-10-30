package state

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
)

// FilesPanel is the SFTP panel a Session Tab shows beside its terminal. The
// app wires one panel state for the window; the shell shows it for the Tab
// that opened it and points it at that Tab's Host.
type FilesPanel struct {
	View func(close func()) func(*ui.Context)
	// ShowRemote browses a Host, keeping a listing it already has.
	ShowRemote func(hostID model.HostID, label string)
	// ShowLocal browses this computer, for a local Session.
	ShowLocal func()
}

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

// FilesRatio is the share of the Tab's width the terminal keeps beside an
// open panel, shared by every Tab so the panel keeps the width the user gave
// it.
func (s *State) FilesRatio() *float32 {
	if s.filesRatio <= 0 || s.filesRatio >= 100 {
		s.filesRatio = 68
	}
	return &s.filesRatio
}

// FilesView returns the panel's view for the Tab, pointed at the Tab's
// target, or nil when the Tab's panel is closed.
func (s *State) FilesView(tab model.TabID) func(*ui.Context) {
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
	return s.filesPanel.View(func() { s.CloseFiles(tab) })
}

func (s *State) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
