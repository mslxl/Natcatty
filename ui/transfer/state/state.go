// Package state owns the main window's SFTP surface state: the two panes,
// their Tabs, browsing history, selections, sorting, hidden-file setting,
// bookmarks, and the running file operation. Window-scoped state lives here,
// as ADR-0006 requires, apart from the view that renders it.
package state

import (
	"context"
	"sync"

	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// Side identifies one of the two panes.
type Side int

const (
	// Left is the left pane.
	Left Side = iota
	// Right is the right pane.
	Right
)

// Sides returns both sides in display order.
func Sides() []Side { return []Side{Left, Right} }

// Other returns the opposite pane.
func (s Side) Other() Side {
	if s == Left {
		return Right
	}
	return Left
}

// TabKind is what a Tab browses.
type TabKind string

const (
	// KindLocal browses the local filesystem.
	KindLocal TabKind = "local"
	// KindRemote browses an SFTP Host.
	KindRemote TabKind = "remote"
)

// Tab is one browser Tab: its connection, current directory, listing,
// selection, ordering, and hidden-file setting. A Tab owns its FileSystem;
// closing the Tab closes it.
type Tab struct {
	ID        int
	Kind      TabKind
	HostID    model.HostID
	HostLabel string
	FS        apptransfer.FileSystem
	Path      string
	// Draft is the editable path field's text.
	Draft     string
	Entries   []domainsftp.Entry
	Selection map[string]bool
	// Anchor is the last plain-clicked row, for Shift range selection.
	Anchor string
	Sort   domainsftp.Sort
	// ShowHidden is the per-Tab hidden-file setting.
	ShowHidden bool
	Loading    bool
	Problem    error
	// Scroll is the list's persistent vertical offset.
	Scroll ui.ScrollState

	back    []string
	forward []string
	// loadSeq discards a listing that a newer navigation superseded.
	loadSeq int
	closed  bool

	// followPath is the last working directory this Tab followed from OSC 7.
	// While the Tab is elsewhere, the same report is treated as the user
	// having navigated away and is not re-applied. followMiss is a path that
	// was reported but is not a directory here, so it is not retried on every
	// shell prompt.
	followPath string
	followMiss string
}

// Title returns the Tab's display label: its Host's label, or its Host id.
func (t *Tab) Title() string {
	if t == nil {
		return ""
	}
	if t.Kind == KindLocal {
		return "local"
	}
	if t.HostLabel != "" {
		return t.HostLabel
	}
	return string(t.HostID)
}

// Visible returns the rows the list shows: hidden files filtered, the parent
// entry pinned first, and the rest sorted by the Tab's order.
func (t *Tab) Visible() []domainsftp.Entry {
	if t == nil {
		return nil
	}
	var parent *domainsftp.Entry
	rest := make([]domainsftp.Entry, 0, len(t.Entries))
	for _, entry := range t.Entries {
		if entry.Name == ".." {
			copied := entry
			parent = &copied
			continue
		}
		if domainsftp.Visible(entry, t.ShowHidden) {
			rest = append(rest, entry)
		}
	}
	sorted := domainsftp.SortEntries(rest, t.Sort)
	if parent != nil {
		return append([]domainsftp.Entry{*parent}, sorted...)
	}
	return sorted
}

// SelectedPaths returns the selected paths in listing order. The parent
// entry is never selected.
func (t *Tab) SelectedPaths() []string {
	if t == nil || len(t.Selection) == 0 {
		return nil
	}
	out := make([]string, 0, len(t.Selection))
	for _, entry := range t.Visible() {
		if entry.Name == ".." {
			continue
		}
		if t.Selection[entry.Path] {
			out = append(out, entry.Path)
		}
	}
	return out
}

// SelectedEntry returns the only selected entry, when exactly one is
// selected.
func (t *Tab) SelectedEntry() (domainsftp.Entry, bool) {
	paths := t.SelectedPaths()
	if len(paths) != 1 {
		return domainsftp.Entry{}, false
	}
	for _, entry := range t.Entries {
		if entry.Path == paths[0] {
			return entry, true
		}
	}
	return domainsftp.Entry{}, false
}

// Destination returns where a paste or drop writes: the only selected
// directory, or the current directory.
func (t *Tab) Destination() string {
	if entry, ok := t.SelectedEntry(); ok && entry.IsDir() && entry.Name != ".." {
		return entry.Path
	}
	return t.Path
}

// Pane is one side's ordered Tabs.
type Pane struct {
	Tabs   []*Tab
	Active int
}

// Tab returns the active Tab, or nil.
func (p *Pane) Tab() *Tab {
	if p == nil || p.Active < 0 || p.Active >= len(p.Tabs) {
		return nil
	}
	return p.Tabs[p.Active]
}

// State is the main window's SFTP surface state. It is mutated only on the UI
// thread; goroutines append outcomes to a locked queue that Flush applies.
type State struct {
	service     *apptransfer.Service
	keybindings *appkeybindings.Service
	// settings reads the live SFTP preferences. The app supplies it so a
	// Settings change applies to the open surface without a restart.
	settings func() domainpreferences.SFTPEditor
	// focusedSession names the Terminal Session the user is looking at, so a
	// background Session's prompt does not steer an SFTP pane. It is read on
	// the UI thread.
	focusedSession func() model.SessionID

	panes   [2]*Pane
	focused Side

	localBookmarks  domainsftp.BookmarkSet
	globalBookmarks domainsftp.BookmarkSet

	clipboard *clipboard

	dialog  *Dialog
	notice  string
	problem error

	// conflictShowing is the queued conflict currently in the dialog, and
	// promptQueue holds other attention rows waiting for their turn. Several
	// Transfers can reach a conflict at once; showing them one at a time keeps
	// every answer tied to the row it names.
	conflictShowing domainsftp.TaskID
	promptQueue     []domainsftp.TaskID

	// batchSeq makes each started transfer batch unique within this surface, so
	// apply-to-all never leaks across two pastes.
	batchSeq int

	hostTargets []apptransfer.HostTarget
	hostsLoaded bool
	hostsLoad   bool

	operation *operationState
	// queueOperations tracks every queue-backed row the surface is watching,
	// so a second Transfer enqueued while the first is still running still
	// finishes its own notice, refresh, and conflict prompt. operation is the
	// one the operation bar currently shows.
	queueOperations map[domainsftp.TaskID]*operationState

	// transferQueue is the global Transfer queue behind pane copies, when the
	// app wired one. centerOpen shows its Transfer center.
	transferQueue *apptransfer.Queue
	centerOpen    bool
	centerError   string

	pending []pendingNav

	queueMu sync.Mutex
	queue   []func()

	redraw func()

	started bool
	nextID  int
	ctx     context.Context
	cancel  context.CancelFunc
}

// Option customizes a State.
type Option func(*State)

// WithRedrawRequest sets the function that wakes the main window after a
// goroutine finishes.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// WithKeybindings gives the surface the active shortcut catalog, so the pane
// handlers follow the user's scheme and overrides.
func WithKeybindings(service *appkeybindings.Service) Option {
	return func(s *State) { s.keybindings = service }
}

// WithPreferences supplies the live SFTP settings: auto-sync, follow-terminal
// cwd, and compressed upload. A surface without a provider runs with all three
// off; the app always supplies one, and the domain defaults enable compressed
// uploads.
func WithPreferences(settings func() domainpreferences.SFTPEditor) Option {
	return func(s *State) { s.settings = settings }
}

// WithFocusedSession supplies the Terminal Session the user is looking at.
// Follow-cwd ignores reports from other Sessions. Without it, a report steers
// the pane for its Host regardless of focus.
func WithFocusedSession(fn func() model.SessionID) Option {
	return func(s *State) { s.focusedSession = fn }
}

// sftpSettings returns the current SFTP settings snapshot.
func (s *State) sftpSettings() domainpreferences.SFTPEditor {
	if s.settings == nil {
		return domainpreferences.SFTPEditor{}
	}
	return s.settings()
}

// NewState builds the surface state. Each side starts with one Tab: the left
// is local, the right waits for a Host choice.
func NewState(service *apptransfer.Service, options ...Option) *State {
	ctx, cancel := context.WithCancel(context.Background())
	s := &State{
		service: service,
		panes: [2]*Pane{
			Left:  {Tabs: nil, Active: -1},
			Right: {Tabs: nil, Active: -1},
		},
		focused:         Left,
		queueOperations: map[domainsftp.TaskID]*operationState{},
		ctx:             ctx,
		cancel:          cancel,
	}
	for _, option := range options {
		option(s)
	}
	return s
}

// Shutdown cancels running work and closes every Tab's transport. The app
// calls it when the window closes.
func (s *State) Shutdown() {
	s.cancel()
	for _, side := range Sides() {
		for _, tab := range s.panes[side].Tabs {
			tab.closed = true
			if tab.FS != nil {
				_ = tab.FS.Close()
			}
		}
		s.panes[side].Tabs = nil
		s.panes[side].Active = -1
	}
}

// EnsureStarted opens the initial local Tab once. The view calls it on its
// first frame; tests may call it directly.
func (s *State) EnsureStarted() {
	if s.started {
		return
	}
	s.started = true
	s.OpenLocal(Left)
	// Load both persisted bookmark sets once, off the UI thread. A malformed
	// file is reported rather than silently treated as an empty list.
	go func() {
		global, globalErr := s.service.GlobalBookmarks(s.ctx)
		local, localErr := s.service.LocalBookmarks(s.ctx)
		s.post(func() {
			if s.ctx.Err() != nil {
				return
			}
			if globalErr == nil {
				s.globalBookmarks = global
			}
			if localErr == nil {
				s.localBookmarks = local
			}
			if globalErr != nil || localErr != nil {
				s.notice = "bookmarks-unreadable"
			}
			s.RequestRedraw()
		})
	}()
}

// Bindings returns the active keyboard catalog for the surface's scoped
// shortcuts. It is empty when the app did not supply one, as in tests.
func (s *State) Bindings() []domainkeybindings.Active {
	if s.keybindings == nil {
		return nil
	}
	return s.keybindings.Active()
}

// Shortcut returns the active chord for a catalog action, in the user's
// scheme, or "" when there is no binding service or the action is disabled.
// The Transfer center uses it to name the local shortcut it documents.
func (s *State) Shortcut(action string) string {
	for _, active := range s.Bindings() {
		if active.Binding.Action == action {
			return active.Chord
		}
	}
	return ""
}

// LocalAvailable reports whether this build can open a local pane.
func (s *State) LocalAvailable() bool { return s.service != nil && s.service.LocalAvailable() }

// RemoteAvailable reports whether this build can open a remote pane.
func (s *State) RemoteAvailable() bool { return s.service != nil && s.service.RemoteAvailable() }

// Pane returns one side's Tab list.
func (s *State) Pane(side Side) *Pane { return s.panes[side] }

// Tab returns a side's active Tab.
func (s *State) Tab(side Side) *Tab { return s.panes[side].Tab() }

// ActiveTab returns the focused side's active Tab.
func (s *State) ActiveTab() *Tab { return s.Tab(s.focused) }

// Focused returns the focused side.
func (s *State) Focused() Side { return s.focused }

// Focus moves keyboard focus to a side.
func (s *State) Focus(side Side) {
	if s.focused != side {
		s.focused = side
		s.RequestRedraw()
	}
}

// Flush applies completed goroutine work on the UI thread. The view calls it
// first each frame.
func (s *State) Flush() {
	s.queueMu.Lock()
	pending := s.queue
	s.queue = nil
	s.queueMu.Unlock()
	for _, fn := range pending {
		fn()
	}
	s.applyPendingNavigation()
}

// post queues an outcome for the UI thread and wakes the window.
func (s *State) post(fn func()) {
	s.queueMu.Lock()
	s.queue = append(s.queue, fn)
	s.queueMu.Unlock()
	s.RequestRedraw()
}

// RequestRedraw wakes the main window.
func (s *State) RequestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}

// Notice returns the last operation message code, or "".
func (s *State) Notice() string { return s.notice }

// SetNotice sets the status message code.
func (s *State) SetNotice(code string) {
	s.notice = code
	s.RequestRedraw()
}

// Problem returns the last fatal surface problem.
func (s *State) Problem() error { return s.problem }

// Dialog returns the open dialog, or nil.
func (s *State) Dialog() *Dialog { return s.dialog }

// OpenLocal adds a local Tab to a side and makes it active.
func (s *State) OpenLocal(side Side) {
	tab := &Tab{
		ID:        s.nextID,
		Kind:      KindLocal,
		Selection: map[string]bool{},
		Sort:      domainsftp.Sort{Field: domainsftp.SortName},
	}
	s.nextID++
	s.addTab(side, tab)
	s.load(tab, "")
}

// OpenRemote adds a remote Tab for a Host and makes it active.
func (s *State) OpenRemote(side Side, hostID model.HostID, label string) {
	tab := &Tab{
		ID:        s.nextID,
		Kind:      KindRemote,
		HostID:    hostID,
		HostLabel: label,
		Selection: map[string]bool{},
		Sort:      domainsftp.Sort{Field: domainsftp.SortName},
	}
	s.nextID++
	s.addTab(side, tab)
	s.openRemote(tab)
}

// Duplicate duplicates a side's active Tab at the same location.
func (s *State) Duplicate(side Side) {
	source := s.Tab(side)
	if source == nil {
		return
	}
	tab := &Tab{
		ID:         s.nextID,
		Kind:       source.Kind,
		HostID:     source.HostID,
		HostLabel:  source.HostLabel,
		Selection:  map[string]bool{},
		Sort:       source.Sort,
		ShowHidden: source.ShowHidden,
	}
	s.nextID++
	s.addTab(side, tab)
	if tab.Kind == KindRemote {
		host := tab.HostID
		label := tab.HostLabel
		path := source.Path
		go func() {
			files, err := s.service.OpenRemote(s.ctx, host, label)
			if err == nil && path == "" {
				// The source Tab was duplicated before its first listing
				// arrived. Its transport is the only source of a start
				// directory, so ask it rather than leaving a blank Tab.
				path, err = files.Root(s.ctx)
				if err != nil {
					_ = files.Close()
				}
			}
			if err != nil {
				s.post(func() {
					if tab.closed {
						return
					}
					tab.Loading = false
					tab.Problem = err
				})
				return
			}
			s.post(func() {
				if tab.closed {
					_ = files.Close()
					return
				}
				tab.FS = files
				s.loadAt(tab, path, false)
				// The Host's SFTP connection is live again, so an interrupted
				// Transfer that used it can continue from its checkpoint.
				s.reattachHost(tab.HostID, files)
			})
		}()
		return
	}
	// A duplicated local Tab owns a new filesystem, like any other local Tab.
	// Calling loadAt here would run before that transport exists and leave the
	// Tab empty, so open the transport at the copied location.
	s.load(tab, source.Path)
}

// CloseTab closes a side's active Tab. Closing the last Tab leaves an empty
// pane.
func (s *State) CloseTab(side Side) {
	pane := s.panes[side]
	tab := pane.Tab()
	if tab == nil {
		return
	}
	tab.closed = true
	if tab.FS != nil {
		_ = tab.FS.Close()
	}
	// A paste must never reach a transport the closed Tab owned.
	if s.clipboard != nil && s.clipboard.Tab == tab {
		s.clipboard = nil
	}
	pane.Tabs = append(pane.Tabs[:pane.Active], pane.Tabs[pane.Active+1:]...)
	if len(pane.Tabs) == 0 {
		pane.Active = -1
	} else if pane.Active >= len(pane.Tabs) {
		pane.Active = len(pane.Tabs) - 1
	}
	s.RequestRedraw()
}

// SelectTab makes one Tab active.
func (s *State) SelectTab(side Side, index int) {
	pane := s.panes[side]
	if index >= 0 && index < len(pane.Tabs) {
		pane.Active = index
		s.focused = side
		s.RequestRedraw()
	}
}

func (s *State) addTab(side Side, tab *Tab) {
	pane := s.panes[side]
	pane.Tabs = append(pane.Tabs, tab)
	pane.Active = len(pane.Tabs) - 1
	s.focused = side
	s.RequestRedraw()
}

// openRemote connects a remote Tab and lists its root.
func (s *State) openRemote(tab *Tab) {
	tab.Loading = true
	go func() {
		files, err := s.service.OpenRemote(s.ctx, tab.HostID, tab.HostLabel)
		if err != nil {
			s.post(func() {
				if tab.closed {
					return
				}
				tab.Loading = false
				tab.Problem = err
			})
			return
		}
		root, err := files.Root(s.ctx)
		if err != nil {
			_ = files.Close()
			s.post(func() {
				if tab.closed {
					return
				}
				tab.Loading = false
				tab.Problem = err
			})
			return
		}
		s.post(func() {
			if tab.closed {
				_ = files.Close()
				return
			}
			tab.FS = files
			s.loadAt(tab, root, false)
			// Reconnecting a Host in the SFTP pane is the product's reconnect
			// path: bind this live filesystem to its interrupted Transfers so
			// the center's Resume continues instead of refusing.
			s.reattachHost(tab.HostID, files)
		})
	}()
}

// load opens a local Tab's start directory.
func (s *State) load(tab *Tab, path string) {
	if tab.Kind != KindLocal {
		return
	}
	tab.Loading = true
	go func() {
		files, err := s.service.OpenLocal(s.ctx)
		if err != nil {
			s.post(func() {
				if tab.closed {
					return
				}
				tab.Loading = false
				tab.Problem = err
			})
			return
		}
		root := path
		if root == "" {
			root, err = files.Root(s.ctx)
			if err != nil {
				_ = files.Close()
				s.post(func() {
					if tab.closed {
						return
					}
					tab.Loading = false
					tab.Problem = err
				})
				return
			}
		}
		s.post(func() {
			if tab.closed {
				_ = files.Close()
				return
			}
			tab.FS = files
			s.loadAt(tab, root, false)
		})
	}()
}

// Navigate changes the active Tab's directory, remembering history.
func (s *State) Navigate(side Side, path string) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	s.loadAtUser(tab, path, true)
}

// Refresh reloads the active Tab's directory.
func (s *State) Refresh(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	s.loadAt(tab, tab.Path, false)
}

// GoParent navigates to the parent directory.
func (s *State) GoParent(side Side) {
	tab := s.Tab(side)
	if tab == nil || tab.FS == nil {
		return
	}
	s.loadAtUser(tab, tab.FS.Parent(tab.Path), true)
}

// Back navigates to the previous directory.
func (s *State) Back(side Side) {
	tab := s.Tab(side)
	if tab == nil || len(tab.back) == 0 {
		return
	}
	path := tab.back[len(tab.back)-1]
	tab.back = tab.back[:len(tab.back)-1]
	tab.forward = append(tab.forward, tab.Path)
	s.loadAtUser(tab, path, false)
}

// Forward navigates to the next directory in history.
func (s *State) Forward(side Side) {
	tab := s.Tab(side)
	if tab == nil || len(tab.forward) == 0 {
		return
	}
	path := tab.forward[len(tab.forward)-1]
	tab.forward = tab.forward[:len(tab.forward)-1]
	tab.back = append(tab.back, tab.Path)
	s.loadAtUser(tab, path, false)
}

// loadAt lists dir on the Tab, applying the result only when no newer
// navigation superseded it. A failure keeps the previous listing.
func (s *State) loadAt(tab *Tab, dir string, remember bool) {
	s.loadAtNavigation(tab, dir, remember, false)
}

// loadAtUser lists dir for a navigation the user asked for. When auto-sync is
// on and the panes are aligned, the other pane follows the same step once this
// listing succeeds; a failed navigation never drags the other pane along.
func (s *State) loadAtUser(tab *Tab, dir string, remember bool) {
	s.loadAtNavigation(tab, dir, remember, true)
}

// loadAtNavigation is the one listing pump. mirror marks a user navigation.
func (s *State) loadAtNavigation(tab *Tab, dir string, remember, mirror bool) {
	if tab == nil || tab.FS == nil || dir == "" {
		return
	}
	previous := tab.Path
	if remember && dir != tab.Path {
		tab.back = append(tab.back, tab.Path)
		tab.forward = nil
	}
	tab.loadSeq++
	seq := tab.loadSeq
	tab.Loading = true
	files := tab.FS
	s.RequestRedraw()
	go func() {
		entries, err := s.service.List(s.ctx, files, dir)
		s.post(func() {
			if tab.closed || seq != tab.loadSeq {
				return
			}
			tab.Loading = false
			if err != nil {
				tab.Problem = err
				return
			}
			tab.Problem = nil
			tab.Path = dir
			tab.Draft = dir
			tab.Entries = entries
			tab.Selection = map[string]bool{}
			tab.Anchor = ""
			if mirror {
				s.mirrorNavigation(tab, previous, dir)
			}
		})
	}()
}

// mirrorNavigation follows a completed user navigation in the other pane when
// auto-sync applies. It uses loadAt, not loadAtUser, so a mirror can never
// mirror back and loop.
func (s *State) mirrorNavigation(source *Tab, previous, dir string) {
	side, target, ok := s.autoSyncTarget(source, previous, dir)
	if !ok {
		return
	}
	other := s.Tab(side)
	if other == nil {
		return
	}
	s.loadAt(other, target, true)
}

// autoSyncTarget decides where the other pane should go after source moved
// from previous to dir. Two panes on one Backing can follow any path directly.
// Panes on different trees follow only a one-step child or parent move while
// both name the same directory level, which is what keeps a local tree and a
// remote tree in step without guessing an unrelated absolute path.
func (s *State) autoSyncTarget(source *Tab, previous, dir string) (Side, string, bool) {
	if !s.sftpSettings().AutoSync || source == nil || source.FS == nil ||
		dir == "" || dir == previous {
		return Left, "", false
	}
	sourceSide, ok := s.sideOf(source)
	if !ok {
		return Left, "", false
	}
	otherSide := sourceSide.Other()
	other := s.Tab(otherSide)
	if other == nil || other.FS == nil || other.closed || other.Loading || other.Path == "" {
		return Left, "", false
	}
	if other.FS.Backing() == source.FS.Backing() {
		if other.Path == dir {
			return Left, "", false
		}
		return otherSide, dir, true
	}
	if previous == "" || source.FS.Base(previous) != other.FS.Base(other.Path) {
		return Left, "", false
	}
	if source.FS.Parent(dir) == previous {
		name := source.FS.Base(dir)
		if name == "" || name == "." || name == ".." {
			return Left, "", false
		}
		for _, entry := range other.Entries {
			if entry.Name != name || !entry.IsDir() {
				continue
			}
			target := other.FS.Join(other.Path, name)
			if target == other.Path {
				return Left, "", false
			}
			return otherSide, target, true
		}
		return Left, "", false
	}
	if source.FS.Parent(previous) == dir {
		target := other.FS.Parent(other.Path)
		if target == other.Path {
			return Left, "", false
		}
		return otherSide, target, true
	}
	return Left, "", false
}

// sideOf locates a Tab's pane.
func (s *State) sideOf(tab *Tab) (Side, bool) {
	for _, side := range Sides() {
		for _, candidate := range s.panes[side].Tabs {
			if candidate == tab {
				return side, true
			}
		}
	}
	return Left, false
}

// NoteTerminalCwd applies a working directory a Session reported with OSC 7.
// With follow-terminal-cwd on, the pane browsing that Session's Host follows;
// a local Session steers a local pane. A Tab that the user has navigated away
// from the last followed directory is not pulled back by a repeated report,
// and a path that is not a directory is not retried on every prompt.
func (s *State) NoteTerminalCwd(sessionID model.SessionID, hostID model.HostID, local bool, cwd string) {
	// Checking here rather than only inside the posted closure keeps a shell
	// prompt from queueing work when follow-cwd is off. Settings() is safe off
	// the UI thread, and the closure checks again because it may change before
	// the frame that drains the queue.
	if cwd == "" || !s.sftpSettings().FollowTerminalCwd {
		return
	}
	s.post(func() {
		if s.ctx.Err() != nil || !s.sftpSettings().FollowTerminalCwd {
			return
		}
		if s.focusedSession != nil {
			if focused := s.focusedSession(); focused != "" && focused != sessionID {
				return
			}
		}
		tab := s.followTab(hostID, local)
		if tab == nil || tab.FS == nil || tab.closed {
			return
		}
		path := tab.FS.Canonical(cwd)
		if path == "" || path == tab.Path || path == tab.followPath || path == tab.followMiss {
			return
		}
		// Claim the report now so a burst of prompt redraws starts one check.
		tab.followPath = path
		tab.followMiss = ""
		files := tab.FS
		seq := tab.loadSeq
		go func() {
			info, err := files.Stat(s.ctx, path)
			s.post(func() {
				if tab.closed || tab.FS != files {
					return
				}
				if err != nil || !info.IsDir() {
					tab.followMiss = path
					return
				}
				if seq != tab.loadSeq || path == tab.Path {
					return
				}
				s.loadAt(tab, path, true)
			})
		}()
	})
}

// followTab picks the pane that represents a Session's Host. The focused
// pane wins when both panes show it; otherwise the other pane is used.
func (s *State) followTab(hostID model.HostID, local bool) *Tab {
	candidates := []Side{s.focused, s.focused.Other()}
	for _, side := range candidates {
		tab := s.Tab(side)
		if tab == nil {
			continue
		}
		if local && tab.Kind == KindLocal {
			return tab
		}
		if !local && tab.Kind == KindRemote && hostID != "" && tab.HostID == hostID {
			return tab
		}
	}
	return nil
}

// NavigateDraft navigates the active Tab to its edited path field.
func (s *State) NavigateDraft(side Side) {
	tab := s.Tab(side)
	if tab == nil || tab.Draft == "" || tab.Draft == tab.Path {
		return
	}
	s.loadAtUser(tab, tab.Draft, true)
}

// SetDraft records a Tab's edited path text.
func (s *State) SetDraft(tab *Tab, text string) {
	if tab != nil {
		tab.Draft = text
	}
}

// Click selects a row. Ctrl toggles, Shift extends from the anchor. The
// parent entry is never selected; double-click opens it.
func (s *State) Click(side Side, entry domainsftp.Entry, mods ui.Modifiers, double bool) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	s.focused = side
	if entry.Name == ".." {
		if double {
			s.loadAtUser(tab, entry.Path, true)
		}
		s.RequestRedraw()
		return
	}
	if double {
		s.Open(side, entry)
		return
	}
	if mods&(ui.Ctrl|ui.Super) != 0 {
		if tab.Selection[entry.Path] {
			delete(tab.Selection, entry.Path)
		} else {
			tab.Selection[entry.Path] = true
			tab.Anchor = entry.Path
		}
		s.RequestRedraw()
		return
	}
	if mods&ui.Shift != 0 && tab.Anchor != "" {
		visible := tab.Visible()
		from, to := -1, -1
		for i, row := range visible {
			if row.Path == tab.Anchor {
				from = i
			}
			if row.Path == entry.Path {
				to = i
			}
		}
		if from >= 0 && to >= 0 {
			tab.Selection = map[string]bool{}
			if from > to {
				from, to = to, from
			}
			for i := from; i <= to; i++ {
				if visible[i].Name != ".." {
					tab.Selection[visible[i].Path] = true
				}
			}
			s.RequestRedraw()
			return
		}
	}
	tab.Selection = map[string]bool{entry.Path: true}
	tab.Anchor = entry.Path
	s.RequestRedraw()
}

// Open activates a row: a directory navigates, a file starts a transfer to
// the other pane when it can, otherwise it is reported as unavailable.
func (s *State) Open(side Side, entry domainsftp.Entry) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	if entry.IsDir() || entry.Name == ".." {
		s.loadAtUser(tab, entry.Path, true)
		return
	}
	s.notice = "open-file"
	s.RequestRedraw()
}

// SelectAll selects every visible entry except the parent entry.
func (s *State) SelectAll(side Side) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	tab.Selection = map[string]bool{}
	for _, entry := range tab.Visible() {
		if entry.Name != ".." {
			tab.Selection[entry.Path] = true
		}
	}
	s.RequestRedraw()
}

// ClearSelection clears one Tab's selection.
func (s *State) ClearSelection(side Side) {
	if tab := s.Tab(side); tab != nil {
		tab.Selection = map[string]bool{}
	}
}

// SetSort changes a side's ordering.
func (s *State) SetSort(side Side, order domainsftp.Sort) {
	if tab := s.Tab(side); tab != nil {
		tab.Sort = order.Normalized()
		s.RequestRedraw()
	}
}

// ToggleSort orders by a field, flipping direction when it is already active.
func (s *State) ToggleSort(side Side, field domainsftp.SortField) {
	tab := s.Tab(side)
	if tab == nil {
		return
	}
	if tab.Sort.Field == field {
		tab.Sort.Descending = !tab.Sort.Descending
	} else {
		tab.Sort.Field = field
		tab.Sort.Descending = false
	}
	s.RequestRedraw()
}

// ToggleHidden flips a side's hidden-file setting.
func (s *State) ToggleHidden(side Side) {
	if tab := s.Tab(side); tab != nil {
		tab.ShowHidden = !tab.ShowHidden
		s.RequestRedraw()
	}
}

// ToggleFoldersFirst flips the "directories first" rule.
func (s *State) ToggleFoldersFirst(side Side) {
	if tab := s.Tab(side); tab != nil {
		tab.Sort.MixKinds = !tab.Sort.MixKinds
		s.RequestRedraw()
	}
}
