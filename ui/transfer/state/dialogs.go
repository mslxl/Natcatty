package state

import (
	"context"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// clipboard is a pane-local copy/cut payload. It keeps the source FileSystem
// so a paste still works after the source Tab is closed.
type clipboard struct {
	Side  Side
	Tab   *Tab
	FS    apptransfer.FileSystem
	Paths []string
	Cut   bool
}

// DialogKind is which prompt is open.
type DialogKind string

const (
	// DialogNewFolder creates a directory.
	DialogNewFolder DialogKind = "new-folder"
	// DialogNewFile creates an empty file.
	DialogNewFile DialogKind = "new-file"
	// DialogRename renames the single selected entry.
	DialogRename DialogKind = "rename"
	// DialogChmod edits one entry's permission bits.
	DialogChmod DialogKind = "chmod"
	// DialogDelete confirms removing the selection.
	DialogDelete DialogKind = "delete"
	// DialogConflict asks how to resolve an existing destination: stop, skip,
	// replace, duplicate, or merge.
	DialogConflict DialogKind = "conflict"
	// DialogHostPicker chooses a Host for a remote pane.
	DialogHostPicker DialogKind = "host-picker"
)

// Dialog is one open prompt. The view renders it and routes Enter/Escape to
// Submit and Cancel; every decision that mutates files goes through here.
type Dialog struct {
	Kind  DialogKind
	Side  Side
	Tab   *Tab
	Paths []string
	// Draft is the editable text: a name, a path, or a mode.
	Draft string
	// Err reports a validation failure without closing the prompt.
	Err string

	// Conflict carries the destination details for DialogConflict.
	Conflict *domainsftp.Conflict
	// TaskID links a queued conflict to its durable row. A direct conflict
	// leaves it empty.
	TaskID domainsftp.TaskID
	// ApplyToAll is the conflict prompt's checkbox state.
	ApplyToAll bool

	// resolve answers a conflict prompt. It is called once, after the dialog
	// closes, with the action the user chose and whether it covers the batch.
	resolve func(action apptransfer.ConflictPolicy, applyAll bool)
}

// RequestNewFolder opens the new-folder prompt.
func (s *State) RequestNewFolder(side Side) {
	tab := s.Tab(side)
	if tab == nil || tab.FS == nil {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	s.dialog = &Dialog{Kind: DialogNewFolder, Side: side, Tab: tab, Draft: ""}
	s.RequestRedraw()
}

// RequestNewFile opens the new-file prompt.
func (s *State) RequestNewFile(side Side) {
	tab := s.Tab(side)
	if tab == nil || tab.FS == nil {
		s.notice = "pane-unavailable"
		s.RequestRedraw()
		return
	}
	s.dialog = &Dialog{Kind: DialogNewFile, Side: side, Tab: tab, Draft: ""}
	s.RequestRedraw()
}

// RequestRename opens the rename prompt for the single selected entry.
func (s *State) RequestRename(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	entry, ok := tab.SelectedEntry()
	if !ok || entry.Name == ".." {
		s.notice = "select-one"
		s.RequestRedraw()
		return
	}
	s.dialog = &Dialog{
		Kind:  DialogRename,
		Side:  side,
		Tab:   tab,
		Paths: []string{entry.Path},
		Draft: entry.Name,
	}
	s.RequestRedraw()
}

// RequestChmod opens the permission prompt for the single selected entry.
func (s *State) RequestChmod(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	entry, ok := tab.SelectedEntry()
	if !ok || entry.Name == ".." {
		s.notice = "select-one"
		s.RequestRedraw()
		return
	}
	s.dialog = &Dialog{
		Kind:  DialogChmod,
		Side:  side,
		Tab:   tab,
		Paths: []string{entry.Path},
		Draft: domainsftp.FormatOctal(entry.Mode),
	}
	s.RequestRedraw()
}

// RequestDelete opens the delete confirmation. Deletion is destructive, so
// nothing is removed until the view's confirm button calls SubmitDialog.
func (s *State) RequestDelete(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	paths := tab.SelectedPaths()
	if len(paths) == 0 {
		s.notice = "nothing-selected"
		s.RequestRedraw()
		return
	}
	s.dialog = &Dialog{Kind: DialogDelete, Side: side, Tab: tab, Paths: paths}
	s.RequestRedraw()
}

// RequestHostPicker opens the Host chooser for a remote pane.
func (s *State) RequestHostPicker(side Side) {
	s.dialog = &Dialog{Kind: DialogHostPicker, Side: side}
	s.RequestRedraw()
	s.loadHosts()
}

// loadHosts fetches the saved Host list once.
func (s *State) loadHosts() {
	if s.hostsLoaded || s.hostsLoad {
		return
	}
	s.hostsLoad = true
	go func() {
		hosts, err := s.service.Hosts(s.ctx)
		s.post(func() {
			s.hostsLoad = false
			if err != nil {
				s.notice = "hosts-unavailable"
				return
			}
			s.hostsLoaded = true
			s.hostTargets = hosts
		})
	}()
}

// HostTargets returns the saved Hosts for the picker.
func (s *State) HostTargets() []apptransfer.HostTarget { return s.hostTargets }

// HostsLoading reports whether the Host list is still loading.
func (s *State) HostsLoading() bool { return s.hostsLoad }

// ChooseHost opens a remote Tab for a Host, replacing the picker.
func (s *State) ChooseHost(side Side, hostID model.HostID, label string) {
	s.dialog = nil
	s.OpenRemote(side, hostID, label)
	s.showNextConflictPrompt()
	s.RequestRedraw()
}

// SetDialogDraft edits the prompt's text.
func (s *State) SetDialogDraft(text string) {
	if s.dialog != nil {
		s.dialog.Draft = text
		s.dialog.Err = ""
	}
}

// SetDialogApplyToAll sets the conflict prompt's apply-to-all checkbox.
func (s *State) SetDialogApplyToAll(value bool) {
	if s.dialog != nil && s.dialog.Kind == DialogConflict {
		s.dialog.ApplyToAll = value
	}
}

// ResolveDialogConflict answers the open conflict prompt. An action the
// destination type cannot support keeps the prompt open with an error; a
// valid action closes it and forwards the answer to the waiting transfer.
func (s *State) ResolveDialogConflict(action apptransfer.ConflictPolicy, applyAll bool) {
	dialog := s.dialog
	if dialog == nil || dialog.Kind != DialogConflict {
		return
	}
	conflict := dialog.Conflict
	if !action.Valid() || conflict == nil {
		dialog.Err = "invalid-conflict-action"
		s.RequestRedraw()
		return
	}
	if action == apptransfer.ConflictMerge && !conflict.CanMerge() {
		dialog.Err = "invalid-conflict-action"
		s.RequestRedraw()
		return
	}
	if action == apptransfer.ConflictReplace && !conflict.CanReplace() {
		dialog.Err = "invalid-conflict-action"
		s.RequestRedraw()
		return
	}
	resolve := dialog.resolve
	s.dialog = nil
	s.conflictShowing = ""
	if resolve != nil {
		resolve(action, applyAll)
	}
	s.showNextConflictPrompt()
	s.RequestRedraw()
}

// CloseDialog dismisses the prompt without acting. Dismissing a conflict is
// the original implementation's Skip: the conflicting item is left untouched
// and the rest of the batch continues.
func (s *State) CloseDialog() {
	dialog := s.dialog
	if dialog == nil {
		return
	}
	if dialog.Kind == DialogConflict {
		s.ResolveDialogConflict(apptransfer.ConflictSkip, dialog.ApplyToAll)
		return
	}
	s.dialog = nil
	// A transfer may have reached its conflict while another prompt was open.
	// Closing that prompt is what frees the dialog slot for it.
	s.showNextConflictPrompt()
	s.RequestRedraw()
}

// SubmitDialog validates and runs the prompt's action. A validation failure
// keeps the prompt open with an error; everything else closes it and runs the
// operation asynchronously.
func (s *State) SubmitDialog() {
	dialog := s.dialog
	if dialog == nil {
		return
	}
	switch dialog.Kind {
	case DialogNewFolder, DialogNewFile:
		name := dialog.Draft
		if err := domainsftp.ValidateName(name); err != nil {
			dialog.Err = "invalid-name"
			s.RequestRedraw()
			return
		}
		tab := dialog.Tab
		s.dialog = nil
		kind := domainsftp.OpMkdir
		if dialog.Kind == DialogNewFile {
			kind = domainsftp.OpCreateFile
		}
		s.runOneShot(kind, tab, func(ctx context.Context) error {
			var err error
			if dialog.Kind == DialogNewFolder {
				_, err = s.service.Mkdir(ctx, tab.FS, tab.Path, name)
			} else {
				_, err = s.service.CreateFile(ctx, tab.FS, tab.Path, name)
			}
			return err
		})
	case DialogRename:
		name := dialog.Draft
		if err := domainsftp.ValidateName(name); err != nil {
			dialog.Err = "invalid-name"
			s.RequestRedraw()
			return
		}
		tab := dialog.Tab
		path := dialog.Paths[0]
		s.dialog = nil
		s.runOneShot(domainsftp.OpRename, tab, func(ctx context.Context) error {
			return s.service.Rename(ctx, tab.FS, path, name)
		})
	case DialogChmod:
		mode, err := domainsftp.ParseMode(dialog.Draft)
		if err != nil {
			dialog.Err = "invalid-mode"
			s.RequestRedraw()
			return
		}
		tab := dialog.Tab
		path := dialog.Paths[0]
		s.dialog = nil
		s.runOneShot(domainsftp.OpChmod, tab, func(ctx context.Context) error {
			return s.service.Chmod(ctx, tab.FS, path, mode)
		})
	case DialogDelete:
		side := dialog.Side
		s.dialog = nil
		s.DeleteSelection(side)
	case DialogConflict:
		// The conflict prompt answers through its action buttons; Enter is not
		// one of the five choices, so it must not guess.
	case DialogHostPicker:
		// Host choice arrives through ChooseHost.
	}
	s.showNextConflictPrompt()
	s.RequestRedraw()
}

// Bookmarks returns the bookmarks a side can show: the persisted global list
// plus the pane-local list on a local pane.
func (s *State) Bookmarks(side Side) []domainsftp.Bookmark {
	items := s.globalBookmarks.Items()
	if tab := s.Tab(side); tab != nil && tab.Kind == KindLocal {
		items = append(s.localBookmarks.Items(), items...)
	}
	return items
}

// Bookmarked reports whether a Tab's current directory is bookmarked.
func (s *State) Bookmarked(side Side) bool {
	tab := s.Tab(side)
	if tab == nil || tab.Path == "" {
		return false
	}
	if tab.Kind == KindLocal {
		return s.localBookmarks.Contains(domainsftp.Bookmark{Path: tab.Path}) ||
			s.globalBookmarks.Contains(domainsftp.Bookmark{Path: tab.Path})
	}
	return s.globalBookmarks.Contains(domainsftp.Bookmark{Path: tab.Path, HostID: tab.HostID})
}

// ToggleBookmark adds or removes the active Tab's directory. Local pane
// bookmarks are kept on this device; global bookmarks for remote locations
// are shared by every remote pane.
func (s *State) ToggleBookmark(side Side) {
	tab := s.Tab(side)
	if tab == nil || tab.Path == "" {
		return
	}
	if tab.Kind == KindLocal {
		bookmark := domainsftp.Bookmark{Label: tab.Path, Path: tab.Path}
		// A persisted global bookmark for this directory is removed from the
		// file rather than shadowed by a local duplicate.
		if s.globalBookmarks.Contains(bookmark) {
			s.globalBookmarks.Remove(bookmark.Key())
			s.saveGlobalBookmarks()
			s.RequestRedraw()
			return
		}
		s.localBookmarks.Toggle(bookmark)
		s.saveLocalBookmarks()
		s.RequestRedraw()
		return
	}
	bookmark := domainsftp.Bookmark{
		Label:     tab.HostLabel + ":" + tab.Path,
		Path:      tab.Path,
		HostID:    tab.HostID,
		HostLabel: tab.HostLabel,
	}
	s.globalBookmarks.Toggle(bookmark)
	s.saveGlobalBookmarks()
	s.RequestRedraw()
}

// RemoveBookmark deletes one bookmark by key.
func (s *State) RemoveBookmark(side Side, key string) {
	tab := s.Tab(side)
	removed := false
	if tab != nil && tab.Kind == KindLocal {
		removed = s.localBookmarks.Remove(key)
		if removed {
			s.saveLocalBookmarks()
		}
	}
	if s.globalBookmarks.Remove(key) {
		removed = true
		s.saveGlobalBookmarks()
	}
	if removed {
		s.RequestRedraw()
	}
}

// saveGlobalBookmarks persists the global set asynchronously.
func (s *State) saveGlobalBookmarks() {
	set := domainsftp.NewBookmarkSet(s.globalBookmarks.Items())
	go func() {
		_ = s.service.SaveGlobalBookmarks(s.ctx, set)
	}()
}

// saveLocalBookmarks persists the local-pane set asynchronously.
func (s *State) saveLocalBookmarks() {
	set := domainsftp.NewBookmarkSet(s.localBookmarks.Items())
	go func() {
		_ = s.service.SaveLocalBookmarks(s.ctx, set)
	}()
}

// GoBookmark navigates a side to a bookmark, opening a remote Tab when the
// bookmark belongs to another Host.
func (s *State) GoBookmark(side Side, bookmark domainsftp.Bookmark) {
	tab := s.Tab(side)
	if bookmark.HostID == "" {
		if tab != nil {
			s.loadAt(tab, bookmark.Path, true)
			return
		}
		s.OpenLocal(side)
		// The local Tab connects asynchronously; navigate once it exists.
		s.pendingNavigation(side, "")
		return
	}
	if tab != nil && tab.Kind == KindRemote && tab.HostID == bookmark.HostID {
		s.loadAt(tab, bookmark.Path, true)
		return
	}
	s.OpenRemote(side, bookmark.HostID, bookmark.HostLabel)
	s.pendingNavigation(side, bookmark.Path)
}

// pendingNavigation waits for a side's newly opened Tab to get its transport,
// then navigates it. It polls on the UI thread via the redraw queue rather
// than blocking a goroutine.
func (s *State) pendingNavigation(side Side, path string) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	s.pending = append(s.pending, pendingNav{side: side, tabID: tab.ID, path: path})
}

type pendingNav struct {
	side  Side
	tabID int
	path  string
}

// applyPendingNavigation runs after listings settle; it navigates a Tab that
// has a transport and is still the side's active Tab.
func (s *State) applyPendingNavigation() {
	if len(s.pending) == 0 {
		return
	}
	remaining := s.pending[:0]
	for _, nav := range s.pending {
		tab := s.Tab(nav.side)
		if tab == nil || tab.ID != nav.tabID || tab.FS == nil {
			if tab != nil && tab.ID == nav.tabID {
				remaining = append(remaining, nav)
			}
			continue
		}
		if nav.path != "" && tab.Path != nav.path {
			s.loadAt(tab, nav.path, false)
		}
	}
	s.pending = remaining
}

// Run executes a catalog action against the focused pane. It reports whether
// the action belongs to the surface.
func (s *State) Run(action string) bool {
	side := s.focused
	switch action {
	case "sftpCopy":
		s.CopySelection(side)
	case "sftpCut":
		s.CutSelection(side)
	case "sftpPaste":
		s.Paste(side)
	case "sftpSelectAll":
		s.SelectAll(side)
	case "sftpRename":
		s.RequestRename(side)
	case "sftpDelete":
		s.RequestDelete(side)
	case "sftpRefresh":
		s.Refresh(side)
	case "sftpNewFolder":
		s.RequestNewFolder(side)
	case "sftpOpen":
		if tab := s.Tab(side); tab != nil {
			if entry, ok := tab.SelectedEntry(); ok {
				s.Open(side, entry)
			}
		}
	case "sftpGoParent":
		s.GoParent(side)
	case "sftpNavigateTo":
		s.NavigateDraft(side)
	default:
		return false
	}
	return true
}

// ToggleFocus switches keyboard focus to the other pane.
func (s *State) ToggleFocus() {
	s.Focus(s.focused.Other())
}
