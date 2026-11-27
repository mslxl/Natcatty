package state

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// FilesPanel is the SFTP panel a Session Tab shows beside its terminal. The
// app wires one panel state for the window; the shell shows it for the Tab
// that opened it and points it at that Tab's target.
type FilesPanel struct {
	// View renders the panel's SFTP tool about width DIPs wide.
	View func(width float32) func(*ui.Context)
	// ShowRemote browses a remote target, keeping the listing a Tab already
	// has. target is a saved Host or an unsaved Quick Connect endpoint.
	ShowRemote func(target FilesTarget)
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

// FilesTarget is what a Tab's side panel browses. Local selects this
// computer. Otherwise it names a saved Host, or an unsaved Quick Connect
// endpoint when HostID is empty and Hostname is set.
type FilesTarget struct {
	// Local browses this computer, for a local Session.
	Local bool
	// HostID is the Vault Host, empty for an unsaved or local target.
	HostID model.HostID
	// Label names the target in the panel.
	Label string
	// Hostname, Port, and Username describe an unsaved Quick Connect endpoint.
	Hostname string
	Port     int
	Username string
	// Password is the one-shot credential the Quick Connect Session already
	// holds. It stays in memory, reaches only the SSH handshake, and is never
	// persisted with the Workspace.
	Password string
	// Jumps is the ad-hoc jump chain the Session dialed, memory-only like the
	// password. Empty for a saved Host, whose chain is resolved from the
	// Vault. The slice makes FilesTarget non-comparable; compare with
	// sameFilesTarget.
	Jumps []appsession.Jump
}

// Saved reports whether the target is a Vault Host.
func (t FilesTarget) Saved() bool { return !t.Local && t.HostID != "" }

// sameFilesTarget reports whether two targets browse the same file system:
// every scalar field equal and the same jump chain, ordered.
func sameFilesTarget(a, b FilesTarget) bool {
	if a.Local != b.Local || a.HostID != b.HostID || a.Label != b.Label ||
		a.Hostname != b.Hostname || a.Port != b.Port || a.Username != b.Username ||
		a.Password != b.Password || len(a.Jumps) != len(b.Jumps) {
		return false
	}
	for i := range a.Jumps {
		if a.Jumps[i] != b.Jumps[i] {
			return false
		}
	}
	return true
}

// FilesTargetFor reports the file system a Session's side panel browses. Only
// SSH-based transports (SSH, Mosh, and ET) offer a remote file system; every
// other transport has none. A remote Session with no saved Host is an unsaved
// Quick Connect target: its endpoint and one-shot password come from the
// Session itself.
func FilesTargetFor(session *appsession.Session, label string) (FilesTarget, bool) {
	if session == nil {
		return FilesTarget{}, false
	}
	switch session.Snapshot().Kind {
	case appsession.KindSSH, appsession.KindMosh, appsession.KindET:
		if id := session.HostID(); id != "" {
			return FilesTarget{HostID: id, Label: label}, true
		}
		endpoint, ok := session.RemoteEndpoint()
		if !ok {
			return FilesTarget{}, false
		}
		return FilesTarget{
			Label:    label,
			Hostname: endpoint.Hostname,
			Port:     endpoint.Port,
			Username: endpoint.Username,
			Password: endpoint.Password,
			Jumps:    append([]appsession.Jump(nil), endpoint.Jumps...),
		}, true
	case appsession.KindLocal:
		return FilesTarget{Local: true}, true
	}
	return FilesTarget{}, false
}

// RenderOwner names the window that renders a shell surface this frame. The
// main window and the dropdown can both draw a Tab, but side-frame bookkeeping
// is kept per owner so one renderer's finish cannot stop a tool another
// renderer is still showing.
type RenderOwner string

const (
	// RenderOwnerMain is the main window. It is also the default for callers
	// that predate owner-scoped bookkeeping.
	RenderOwnerMain RenderOwner = "main"
	// RenderOwnerDropdown is the dropdown panel: the Quake workspace or the
	// main shell's follow/topdown view.
	RenderOwnerDropdown RenderOwner = "dropdown"
)

// renderOwner normalizes the empty owner to the main window, so the original
// single-renderer calls keep their behavior.
func renderOwner(owner RenderOwner) RenderOwner {
	if owner == "" {
		return RenderOwnerMain
	}
	return owner
}

// SideToolID names one of the side panel's tools, in the original's terms.
type SideToolID string

const (
	SideSFTP    SideToolID = "sftp"
	SideScripts SideToolID = "scripts"
	SideHistory SideToolID = "history"
	SideTheme   SideToolID = "theme"
	SideSystem  SideToolID = "system"
	SideNotes   SideToolID = "notes"
)

// SideTarget is what a Tab's side panel serves: the Session's Host, or this
// computer when Host is empty.
type SideTarget struct {
	Host  model.HostID
	Label string
}

// SideTool is a side panel tool besides SFTP, such as Scripts or System.
type SideTool struct {
	ID SideToolID
	// View renders the tool for target about width DIPs wide.
	View func(target SideTarget, width float32) func(*ui.Context)
	// Hide, when set, is called once the tool is no longer drawn, so a tool
	// that samples a Host can stop.
	Hide func()
}

// WithFilesPanel gives Session Tabs their SFTP side panel. Without it the
// Pane toolbar has no Files button.
func WithFilesPanel(panel FilesPanel) Option {
	return func(s *State) { s.filesPanel = &panel }
}

// WithAutoOpenSidePanel supplies the live "Open the side panel automatically"
// setting. When it reports true, a remote Session that finishes connecting
// opens its Tab's side panel on that Session's file system. Without it the
// panel stays closed until the user opens it.
func WithAutoOpenSidePanel(enabled func() bool) Option {
	return func(s *State) {
		if enabled != nil {
			s.autoOpenSidePanel = enabled
		}
	}
}

// WithSideTools adds tools to the side panel after SFTP, in the order given.
func WithSideTools(tools ...SideTool) Option {
	return func(s *State) {
		for _, tool := range tools {
			if tool.ID != "" && tool.ID != SideSFTP && tool.View != nil {
				s.sideTools = append(s.sideTools, tool)
			}
		}
	}
}

// FilesAvailable reports whether Session Tabs can open the side panel.
func (s *State) FilesAvailable() bool { return s.sftpToolAvailable() || len(s.sideTools) > 0 }

func (s *State) sftpToolAvailable() bool { return s.filesPanel != nil && s.filesPanel.View != nil }

// SideTools lists the tools the Tab's open panel offers, in strip order. It
// is empty when the panel is closed.
func (s *State) SideTools(tab model.TabID) []SideToolID {
	if _, open := s.filesOpen[tab]; !open {
		return nil
	}
	var ids []SideToolID
	if s.sftpToolAvailable() {
		ids = append(ids, SideSFTP)
	}
	for _, tool := range s.sideTools {
		ids = append(ids, tool.ID)
	}
	return ids
}

// SideTool is the tool the Tab's panel shows: the one chosen last, or the
// first the panel offers, SFTP when it has it, as the original's default.
func (s *State) SideTool(tab model.TabID) SideToolID {
	tools := s.SideTools(tab)
	if chosen, ok := s.sideChosen[tab]; ok {
		for _, id := range tools {
			if id == chosen {
				return chosen
			}
		}
	}
	if len(tools) == 0 {
		return ""
	}
	return tools[0]
}

// SetSideTool shows another tool in the Tab's panel.
func (s *State) SetSideTool(tab model.TabID, id SideToolID) {
	if s.sideChosen == nil {
		s.sideChosen = map[model.TabID]SideToolID{}
	}
	s.sideChosen[tab] = id
	s.redraw()
}

// FinishSideFrame tells the tool the main window drew the frame before that it
// is no longer drawn: its Tab closed the panel, chose another tool, or stopped
// showing. It is FinishSideFrameFor for the main renderer.
func (s *State) FinishSideFrame() {
	s.FinishSideFrameFor(RenderOwnerMain)
}

// FinishSideFrameFor tells the tool a renderer drew the frame before that it
// stopped drawing, unless another renderer is still drawing it. The shell
// calls it once at the end of every frame of the renderer that owns the
// surface. Keeping the bookkeeping per owner is what lets the main window and
// a follow view coexist without one stopping the other's live tool.
func (s *State) FinishSideFrameFor(owner RenderOwner) {
	owner = renderOwner(owner)
	last := s.sideLast[owner]
	drawn := s.sideDrawn[owner]
	delete(s.sideDrawn, owner)
	if s.sideLast == nil {
		s.sideLast = map[RenderOwner]SideToolID{}
	}
	s.sideLast[owner] = drawn
	if last == "" || last == drawn || s.sideToolDrawnByOther(owner, last) {
		return
	}
	if s.sideHidden[last] {
		return
	}
	for _, tool := range s.sideTools {
		if tool.ID == last && tool.Hide != nil {
			if s.sideHidden == nil {
				s.sideHidden = map[SideToolID]bool{}
			}
			s.sideHidden[last] = true
			tool.Hide()
		}
	}
}

// HoldSideToolFor marks id as still drawn by owner this frame. A side panel
// that is closing keeps painting its last built frame while its slot shrinks,
// so the tool must stay live until the panel leaves, even though the Tab's
// panel is already closed and FilesViewFor returns nil. It is the drawer's
// equivalent of drawing the tool.
func (s *State) HoldSideToolFor(owner RenderOwner, id SideToolID) {
	if id == "" {
		return
	}
	owner = renderOwner(owner)
	if s.sideDrawn == nil {
		s.sideDrawn = map[RenderOwner]SideToolID{}
	}
	s.sideDrawn[owner] = id
	delete(s.sideHidden, id)
}

// sideToolDrawnByOther reports whether another renderer draws id in this
// frame. A tool shared by the two windows therefore stays alive until the
// last one stops drawing it.
func (s *State) sideToolDrawnByOther(owner RenderOwner, id SideToolID) bool {
	for other, drawn := range s.sideDrawn {
		if other != owner && drawn == id {
			return true
		}
	}
	return false
}

// FilesOpen reports whether the Tab shows the SFTP panel.
func (s *State) FilesOpen(tab model.TabID) bool {
	_, open := s.filesOpen[tab]
	return open
}

// ToggleFiles opens the Tab's SFTP panel on host, or on this computer when
// host is empty, and closes it when it already shows that target.
func (s *State) ToggleFiles(tab model.TabID, host model.HostID, label string) {
	target := FilesTarget{Local: host == "", HostID: host, Label: label}
	s.ToggleFilesTarget(tab, target)
}

// ToggleFilesTarget opens the Tab's side panel on target and closes it when it
// already shows that target.
func (s *State) ToggleFilesTarget(tab model.TabID, target FilesTarget) {
	if !s.FilesAvailable() || tab == "" {
		return
	}
	if current, open := s.filesOpen[tab]; open && sameFilesTarget(current, target) {
		s.CloseFiles(tab)
		return
	}
	s.OpenFiles(tab, target)
}

// OpenFiles opens the Tab's side panel on target, leaving an already open
// panel on that same target alone.
func (s *State) OpenFiles(tab model.TabID, target FilesTarget) {
	if !s.FilesAvailable() || tab == "" {
		return
	}
	if current, open := s.filesOpen[tab]; open && sameFilesTarget(current, target) {
		return
	}
	if s.filesOpen == nil {
		s.filesOpen = map[model.TabID]FilesTarget{}
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

// autoOpenFilesOnConnect opens the Tab's side panel for a Session that just
// attached, when the setting asks for it. It applies to remote Sessions only:
// the setting lives with the SFTP editor and a local shell has no remote to
// browse. A transport without a file system (Telnet, VNC) is left alone.
func (s *State) autoOpenFilesOnConnect(tab model.TabID, session *appsession.Session, label string) {
	if s.autoOpenSidePanel == nil || !s.autoOpenSidePanel() {
		return
	}
	if session == nil || session.Snapshot().Kind == appsession.KindLocal {
		return
	}
	target, ok := FilesTargetFor(session, label)
	if !ok || target.Local {
		return
	}
	s.OpenFiles(tab, target)
}

// ToggleActiveFiles is the "Toggle side panel" shortcut: it opens the active
// Tab's panel on the focused Session's file system, or closes it when it is
// open. It reports false when the focused Pane has no file system to show.
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
	target, ok := FilesTargetFor(value.session, value.descriptor.Label)
	if !ok {
		return false
	}
	s.ToggleFilesTarget(tab.ID, target)
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

// FilesView returns the tool the Tab's side panel shows for the main window,
// about width DIPs wide and pointed at the Tab's target, or nil when the Tab's
// panel is closed. It is FilesViewFor for the main renderer.
func (s *State) FilesView(tab model.TabID, width float32) func(*ui.Context) {
	return s.FilesViewFor(RenderOwnerMain, tab, width)
}

// FilesViewFor returns the tool the Tab's side panel shows for the renderer
// owner, about width DIPs wide and pointed at the Tab's target, or nil when
// the Tab's panel is closed. Painting the tool records it as drawn by owner,
// so FinishSideFrameFor only stops it after the last renderer stops drawing
// it. The chosen tool and the panel's target stay shared: background follows
// the Tab, not the window that draws it.
func (s *State) FilesViewFor(owner RenderOwner, tab model.TabID, width float32) func(*ui.Context) {
	target, open := s.filesOpen[tab]
	if !open || !s.FilesAvailable() {
		return nil
	}
	owner = renderOwner(owner)
	id := s.SideTool(tab)
	if s.sideDrawn == nil {
		s.sideDrawn = map[RenderOwner]SideToolID{}
	}
	s.sideDrawn[owner] = id
	// Drawing the tool again makes it live after an earlier stop, so the next
	// time it leaves the screen it is told once more.
	delete(s.sideHidden, id)
	if id != SideSFTP {
		for _, tool := range s.sideTools {
			if tool.ID == id {
				return tool.View(SideTarget{Host: target.HostID, Label: target.Label}, width)
			}
		}
		return nil
	}
	if s.filesShown == nil || !sameFilesTarget(*s.filesShown, target) {
		shown := target
		s.filesShown = &shown
		if target.Local {
			if s.filesPanel.ShowLocal != nil {
				s.filesPanel.ShowLocal()
			}
		} else if s.filesPanel.ShowRemote != nil {
			s.filesPanel.ShowRemote(target)
		}
	}
	return s.filesPanel.View(width)
}

func (s *State) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
