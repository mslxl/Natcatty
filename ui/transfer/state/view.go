package state

import (
	"strings"

	"github.com/egoist/mygo/ui"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// matchesFilter reports whether the Tab's filename filter keeps entry. The
// match ignores case, as the original's filter bar does.
func (t *Tab) matchesFilter(entry domainsftp.Entry) bool {
	if t.Filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(entry.Name), strings.ToLower(t.Filter))
}

// GoHome lists the Tab's home folder.
func (s *State) GoHome(side Side) {
	if tab := s.Tab(side); tab != nil && tab.Home != "" {
		s.Navigate(side, tab.Home)
	}
}

// EditPath swaps the breadcrumb for the path field, filled with the current
// folder. CancelPathEdit puts the breadcrumb back without navigating.
func (s *State) EditPath(side Side) {
	if tab := s.Tab(side); tab != nil {
		s.focused = side
		tab.Draft = tab.Path
		tab.EditingPath = true
		s.RequestRedraw()
	}
}

// CancelPathEdit closes the path field.
func (s *State) CancelPathEdit(side Side) {
	if tab := s.Tab(side); tab != nil && tab.EditingPath {
		tab.EditingPath = false
		tab.Draft = tab.Path
		s.RequestRedraw()
	}
}

// ToggleFilter shows or hides the filter bar. Hiding it clears the filter,
// so a closed bar never leaves the listing narrowed.
func (s *State) ToggleFilter(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	tab.FilterOpen = !tab.FilterOpen
	if !tab.FilterOpen {
		tab.Filter = ""
	}
	s.RequestRedraw()
}

// EscapeFilter is Esc in the filter field: it clears the text first and
// closes the bar on the next press.
func (s *State) EscapeFilter(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	if tab.Filter != "" {
		tab.Filter = ""
	} else {
		tab.FilterOpen = false
	}
	s.RequestRedraw()
}

// ToggleCompact switches the list between its comfortable and compact rows.
func (s *State) ToggleCompact(side Side) {
	if tab := s.Tab(side); tab != nil {
		tab.Compact = !tab.Compact
		s.RequestRedraw()
	}
}

// SelectForMenu is a right-click on a row: a row outside the selection
// becomes the only selected row, so the menu acts on what was clicked; a row
// inside it keeps the selection, so the menu acts on all of it.
func (s *State) SelectForMenu(side Side, entry domainsftp.Entry) {
	tab := s.Tab(side)
	if tab == nil || entry.Name == ".." {
		return
	}
	s.focused = side
	if !tab.Selection[entry.Path] {
		tab.Selection = map[string]bool{entry.Path: true}
		tab.Anchor = entry.Path
	}
	s.RequestRedraw()
}

// MoveSelection moves the selection one row up or down; extend grows it
// from the anchor as Shift does.
func (s *State) MoveSelection(side Side, delta int, extend bool) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	rows := tab.Visible()
	if len(rows) == 0 {
		return
	}
	current := -1
	for i, row := range rows {
		if tab.Selection[row.Path] && (current < 0 || delta > 0) {
			current = i
		}
	}
	next := 0
	if current >= 0 {
		next = min(max(current+delta, 0), len(rows)-1)
	} else if delta < 0 {
		next = len(rows) - 1
	}
	if rows[next].Name == ".." {
		if len(rows) == 1 {
			return
		}
		if next == 0 {
			next = 1
		}
	}
	entry := rows[next]
	if extend && tab.Anchor != "" {
		s.Click(side, entry, ui.Shift, false)
		return
	}
	tab.Selection = map[string]bool{entry.Path: true}
	tab.Anchor = entry.Path
	s.focused = side
	s.RequestRedraw()
}

// CloseTabAt closes one Tab of a pane, whether or not it is the active one.
func (s *State) CloseTabAt(side Side, index int) {
	pane := s.panes[side]
	if index < 0 || index >= len(pane.Tabs) {
		return
	}
	// Closing a background Tab keeps the Tab the user is looking at.
	active := pane.Tab()
	pane.Active = index
	s.CloseTab(side)
	for i, tab := range pane.Tabs {
		if tab == active {
			pane.Active = i
		}
	}
}

// StripTasks are the Transfers the queue strip under the panes lists: every
// Transfer except the finished ones the user cleared.
func (s *State) StripTasks() []domainsftp.Task {
	tasks := s.QueueTasks()
	out := tasks[:0:0]
	for _, task := range tasks {
		if !s.clearedTasks[task.ID] {
			out = append(out, task)
		}
	}
	return out
}

// ClearFinished drops the completed and cancelled Transfers from the strip.
// Failed ones stay, so their Retry is still at hand.
func (s *State) ClearFinished() {
	for _, task := range s.QueueTasks() {
		if task.Status == domainsftp.TaskCompleted || task.Status == domainsftp.TaskCancelled {
			if s.clearedTasks == nil {
				s.clearedTasks = map[domainsftp.TaskID]bool{}
			}
			s.clearedTasks[task.ID] = true
		}
	}
	s.RequestRedraw()
}

// HasFinished reports whether the strip lists a Transfer Clear would drop.
func (s *State) HasFinished() bool {
	for _, task := range s.StripTasks() {
		if task.Status == domainsftp.TaskCompleted || task.Status == domainsftp.TaskCancelled {
			return true
		}
	}
	return false
}

// ChooseLocal opens a local Tab, replacing the Host picker.
func (s *State) ChooseLocal(side Side) {
	s.dialog = nil
	s.OpenLocal(side)
	s.showNextConflictPrompt()
	s.RequestRedraw()
}
