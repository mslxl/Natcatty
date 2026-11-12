package state

import (
	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// ReadOnlyTab is a read-only reference to one work Tab. A second renderer,
// such as the dropdown's follow view, draws a Tab through it. It exposes the
// Pane tree, Pane lookup, focus, rename, and retry. It deliberately has no
// close, split, move, detach, hibernate, resume, or split-ratio method, so
// drawing the main Workspace in another window cannot change its Tab order,
// active index, split tree, or Session ownership. Focus and rename are view
// state owned by the Workspace and are allowed because a remounted follow view
// must be able to reclaim the keyboard and the user may still rename a
// Session.
//
// The reference is bound by the Tab's stable identity, not by index, so a
// reorder or closure elsewhere cannot make it address the wrong Tab. A zero
// ReadOnlyTab is safe and reports nothing.
type ReadOnlyTab struct {
	s  *State
	id model.TabID
}

// ReadOnlyTab returns a read-only reference to the work Tab at index.
func (s *State) ReadOnlyTab(index int) (ReadOnlyTab, bool) {
	if index < 0 || index >= len(s.tabs) {
		return ReadOnlyTab{}, false
	}
	return ReadOnlyTab{s: s, id: s.tabs[index].ID}, true
}

// ActiveReadOnlyTab returns a read-only reference to the active work Tab. It
// reports false when the main Workspace has no active Tab.
func (s *State) ActiveReadOnlyTab() (ReadOnlyTab, bool) {
	return s.ReadOnlyTab(s.active)
}

// ID returns the Tab's stable identity.
func (t ReadOnlyTab) ID() model.TabID { return t.id }

func (t ReadOnlyTab) tab() (*Tab, bool) {
	if t.s == nil || t.id == "" {
		return nil, false
	}
	tab, _ := t.s.tabByID(t.id)
	return tab, tab != nil
}

func (t ReadOnlyTab) index() (int, bool) {
	if t.s == nil || t.id == "" {
		return -1, false
	}
	_, index := t.s.tabByID(t.id)
	return index, index >= 0
}

// Tree returns the Tab's split tree and focus path for rendering, with the
// focus path copied so a renderer cannot mutate the Workspace through it.
func (t ReadOnlyTab) Tree() (*domainworkspace.Node, []int, bool) {
	tab, ok := t.tab()
	if !ok {
		return nil, nil, false
	}
	return tab.Root, append([]int(nil), tab.FocusPath...), true
}

// PaneInfo returns the Pane with id in the Tab.
func (t ReadOnlyTab) PaneInfo(id model.PaneID) (PaneInfo, bool) {
	tab, ok := t.tab()
	if !ok {
		return PaneInfo{}, false
	}
	return t.s.paneInfo(tab, id)
}

// FocusedPaneID returns the Pane that owns focus in the Tab.
func (t ReadOnlyTab) FocusedPaneID() (model.PaneID, bool) {
	tab, ok := t.tab()
	if !ok {
		return "", false
	}
	_, id := tab.focusedPane()
	return id, id != ""
}

// FocusPane gives focus to one Pane in the Tab. The renderer calls it every
// frame it mounts the Tab, because MyGo prunes an unmounted subtree's element
// state and the follow view must take the keyboard back.
func (t ReadOnlyTab) FocusPane(id model.PaneID) bool {
	index, ok := t.index()
	if !ok {
		return false
	}
	return t.s.FocusPaneID(index, id)
}

// RenamePane gives one Pane's Session a user title, exactly as the main
// window's pane header does.
func (t ReadOnlyTab) RenamePane(id model.PaneID, title string) bool {
	index, ok := t.index()
	if !ok {
		return false
	}
	return t.s.RenamePane(index, id, title)
}

// RetryPane reopens a failed Pane's Session from its own descriptor.
func (t ReadOnlyTab) RetryPane(id model.PaneID) bool {
	if _, ok := t.tab(); !ok {
		return false
	}
	return t.s.RetryPane(t.id, id)
}

// BrowserOpen reports whether any Pane in the Tab shows an open browser
// panel. A split renderer mounts every Pane, so one open webview anywhere in
// the Tab prevents the whole Tab from being rendered in another window.
func (t ReadOnlyTab) BrowserOpen() bool {
	tab, ok := t.tab()
	if !ok {
		return false
	}
	for _, value := range tab.panes {
		if value == nil || value.surface == nil {
			continue
		}
		if shown, ok := value.surface.(BrowserShown); ok && shown.BrowserOpen() {
			return true
		}
	}
	return false
}

// ActiveBrowserOpen reports whether any Pane in the active Tab shows an open
// browser panel. It is the placeholder decision a follow or topdown renderer
// makes before mounting the main Workspace's Surface in the dropdown.
func (s *State) ActiveBrowserOpen() bool {
	tab, ok := s.ActiveReadOnlyTab()
	return ok && tab.BrowserOpen()
}
