package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// ErrUnknownSession reports a restored Pane descriptor this shell cannot
// reopen.
var ErrUnknownSession = errors.New("shell: unknown Session kind")

// Tab is one work Tab: an ordered split tree whose leaves are Panes. A Pane is
// bound to one Session. The Tab's identity and number are stable for its
// lifetime; a duplicate gets a fresh identity and Copy generation.
type Tab struct {
	ID     model.TabID
	Number int
	// TitleNumber is the number an auto title shows. A duplicate keeps the
	// source's number while getting its own stable Tab Number.
	TitleNumber int
	Label       string
	AutoTitle   bool
	// Copy counts duplicate generations: 0 is the original, 1 the first copy.
	Copy      int
	Dirty     bool
	Root      *domainworkspace.Node
	FocusPath []int
	panes     map[model.PaneID]*pane
}

type pane struct {
	ID         model.PaneID
	descriptor domainworkspace.SessionDescriptor
	session    *appsession.Session
	surface    Surface
	err        error
	pending    bool
}

// TabOptions seeds a new Tab.
type TabOptions struct {
	Label      string
	AutoTitle  bool
	Copy       int
	Descriptor domainworkspace.SessionDescriptor
}

// TabInfo is the immutable Tab description the view renders.
type TabInfo struct {
	ID          model.TabID
	Number      int
	TitleNumber int
	Label       string
	AutoTitle   bool
	Copy        int
	Dirty       bool
	Active      bool
}

// PaneInfo is the immutable Pane description the view renders.
type PaneInfo struct {
	ID      model.PaneID
	Number  int
	Label   string
	Focused bool
	Pending bool
	Err     error
	Surface Surface
}

// DetachedTabInfo describes a Tab that left the main window and now belongs to
// its own window. Panes lists the Tab's Pane identities in tree order.
type DetachedTabInfo struct {
	ID          model.TabID
	Number      int
	TitleNumber int
	Label       string
	AutoTitle   bool
	// Copy counts duplicate generations, as Tab.Copy does.
	Copy  int
	Panes []model.PaneID
}

// DragKind distinguishes the two things the shell moves by drag and drop.
type DragKind int

const (
	// DragKindTab reorders a Tab.
	DragKindTab DragKind = iota
	// DragKindPane moves a Pane within or between Tabs.
	DragKindPane
)

// DragItem is the value a Tab or Pane drags. A single type lets the Tab strip
// take both: a Tab reorders, while a Pane moves into that Tab.
type DragItem struct {
	Kind   DragKind
	TabID  model.TabID
	PaneID model.PaneID
}

// TabDrag builds a Tab drag value.
func TabDrag(tabID model.TabID) DragItem {
	return DragItem{Kind: DragKindTab, TabID: tabID}
}

// PaneDrag builds a Pane drag value.
func PaneDrag(tabID model.TabID, paneID model.PaneID) DragItem {
	return DragItem{Kind: DragKindPane, TabID: tabID, PaneID: paneID}
}

func newRuntimeID(prefix string) string {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	for i := 0; i < 6; i++ {
		id[i] = byte(milliseconds >> (8 * (5 - i)))
	}
	if _, err := rand.Read(id[6:]); err != nil {
		// crypto/rand does not fail on supported platforms, but the identity
		// must remain usable in tests and unusual sandboxes.
		for i := 6; i < len(id); i++ {
			id[i] = byte(milliseconds >> uint(i%6*8))
		}
	}
	return prefix + hex.EncodeToString(id[:])
}

func (s *State) newTab(opts TabOptions) *Tab {
	number := s.nextTabNumber
	s.nextTabNumber++
	if opts.Descriptor.Number == 0 {
		opts.Descriptor.Number = number
	}
	paneID := model.PaneID(newRuntimeID("pane-"))
	return &Tab{
		ID:          model.TabID(newRuntimeID("tab-")),
		Number:      number,
		TitleNumber: number,
		Label:       opts.Label,
		AutoTitle:   opts.AutoTitle,
		Copy:        opts.Copy,
		Root:        domainworkspace.NewPaneNode(paneID),
		panes: map[model.PaneID]*pane{
			paneID: {ID: paneID, descriptor: opts.Descriptor},
		},
	}
}

func (t *Tab) paneIDs() []model.PaneID {
	if t == nil {
		return nil
	}
	return domainworkspace.CollectPaneIDs(t.Root)
}

// attachOnlySession binds the Session and surface of a Tab that has exactly
// one Pane.
func (t *Tab) attachOnlySession(session *appsession.Session, surface Surface) {
	ids := t.paneIDs()
	if len(ids) != 1 {
		return
	}
	value := t.panes[ids[0]]
	value.session = session
	value.surface = surface
}

func (t *Tab) focusedPane() (*pane, model.PaneID) {
	ids := t.paneIDs()
	if len(ids) == 0 {
		return nil, ""
	}
	node, ok := domainworkspace.PaneAtPath(t.Root, t.FocusPath)
	if !ok || node == nil {
		node, ok = domainworkspace.PaneAtPath(t.Root, domainworkspace.FocusPath(t.Root, ""))
		if !ok || node == nil {
			return nil, ""
		}
	}
	return t.panes[node.PaneID], node.PaneID
}

func (t *Tab) setFocus(paneID model.PaneID) {
	if path := domainworkspace.PathTo(t.Root, paneID); path != nil {
		t.FocusPath = append([]int{}, path...)
	}
}

func (s *State) activeTab() (*Tab, int) {
	if s.active < 0 || s.active >= len(s.tabs) {
		return nil, -1
	}
	return s.tabs[s.active], s.active
}

func (s *State) tabByID(id model.TabID) (*Tab, int) {
	for index, tab := range s.tabs {
		if tab.ID == id {
			return tab, index
		}
	}
	return nil, -1
}

// tabByAnyID finds a Tab in the main Workspace or in a detached window. Pane
// outcomes use it so a Session opened for a duplicated-to-window Tab is
// applied to the right owner.
func (s *State) tabByAnyID(id model.TabID) (*Tab, int) {
	if tab, index := s.tabByID(id); tab != nil {
		return tab, index
	}
	for _, tab := range s.detached {
		if tab.ID == id {
			return tab, -1
		}
	}
	return nil, -1
}

// TabCount returns the number of work Tabs.
func (s *State) TabCount() int { return len(s.tabs) }

// ActiveTab returns the index of the active work Tab, or -1 when there is
// none.
func (s *State) ActiveTab() int { return s.active }

// TabNumbers returns the stable numbers of the work Tabs, in order.
func (s *State) TabNumbers() []int {
	numbers := make([]int, len(s.tabs))
	for i, tab := range s.tabs {
		numbers[i] = tab.Number
	}
	return numbers
}

// TabLabel returns a Tab's title label and stable number. An empty label means
// a local Session, whose title the view localizes from the number.
func (s *State) TabLabel(index int) (string, int, bool) {
	if index < 0 || index >= len(s.tabs) {
		return "", 0, false
	}
	return s.tabs[index].Label, s.tabs[index].Number, true
}

// RenameTab sets a Tab's user title. An empty title is rejected, so a Pane's
// Rename dialog cannot leave the tab strip blank. The title persists and stops
// following the automatic local-session counter.
func (s *State) RenameTab(index int, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || index < 0 || index >= len(s.tabs) {
		return false
	}
	s.tabs[index].Label = title
	s.tabs[index].AutoTitle = false
	s.save()
	return true
}

// RenameDetachedTab sets a detached window's Tab title under the same rules.
func (s *State) RenameDetachedTab(id model.TabID, title string) bool {
	title = strings.TrimSpace(title)
	tab := s.detachedTabByID(id)
	if title == "" || tab == nil {
		return false
	}
	tab.Label = title
	tab.AutoTitle = false
	s.save()
	return true
}

// TabInfo returns everything the tab strip renders for one Tab.
func (s *State) TabInfo(index int) (TabInfo, bool) {
	if index < 0 || index >= len(s.tabs) {
		return TabInfo{}, false
	}
	tab := s.tabs[index]
	return TabInfo{
		ID:          tab.ID,
		Number:      tab.Number,
		TitleNumber: tab.TitleNumber,
		Label:       tab.Label,
		AutoTitle:   tab.AutoTitle,
		Copy:        tab.Copy,
		Dirty:       tab.Dirty,
		Active:      index == s.active && !s.vaultVisible && !s.logsVisible,
	}, true
}

// TabID returns the stable identity of the Tab at index.
func (s *State) TabID(index int) (model.TabID, bool) {
	if index < 0 || index >= len(s.tabs) {
		return "", false
	}
	return s.tabs[index].ID, true
}

// TabTree returns the Tab's split tree and focus path for rendering.
func (s *State) TabTree(index int) (*domainworkspace.Node, []int, bool) {
	if index < 0 || index >= len(s.tabs) {
		return nil, nil, false
	}
	return s.tabs[index].Root, append([]int(nil), s.tabs[index].FocusPath...), true
}

// PaneInfoAt returns the Pane with id in the Tab at index.
func (s *State) PaneInfoAt(tabIndex int, id model.PaneID) (PaneInfo, bool) {
	if tabIndex < 0 || tabIndex >= len(s.tabs) {
		return PaneInfo{}, false
	}
	return paneInfo(s.tabs[tabIndex], id)
}

// FocusedPaneID returns the Pane that owns focus in the Tab at index.
func (s *State) FocusedPaneID(index int) (model.PaneID, bool) {
	if index < 0 || index >= len(s.tabs) {
		return "", false
	}
	_, id := s.tabs[index].focusedPane()
	return id, id != ""
}

// SelectTab makes the Tab at index active and shows it. Out-of-range indexes
// are ignored. Choosing a Tab marks its activity seen.
func (s *State) SelectTab(index int) {
	if index < 0 || index >= len(s.tabs) {
		return
	}
	s.active = index
	s.tabs[index].Dirty = false
	s.vaultVisible = false
	s.logsVisible = false
	s.sftpVisible = false
	s.save()
}

// ActiveSurface returns the terminal surface of the focused Pane in the active
// Tab, or nil when there is none.
func (s *State) ActiveSurface() Surface {
	tab, _ := s.activeTab()
	if tab == nil {
		return nil
	}
	value, _ := tab.focusedPane()
	if value == nil {
		return nil
	}
	return value.surface
}

// ActiveSnapshot returns the Session snapshot of the focused Pane in the
// active Tab. It reports false when there is no Tab or no live Session.
func (s *State) ActiveSnapshot() (appsession.Snapshot, bool) {
	tab, _ := s.activeTab()
	if tab == nil {
		return appsession.Snapshot{}, false
	}
	value, _ := tab.focusedPane()
	if value == nil || value.session == nil {
		return appsession.Snapshot{}, false
	}
	return value.session.Snapshot(), true
}

// SetTabDirty marks or clears a Tab's activity indicator.
func (s *State) SetTabDirty(index int, dirty bool) {
	if index < 0 || index >= len(s.tabs) {
		return
	}
	if s.tabs[index].Dirty != dirty {
		s.tabs[index].Dirty = dirty
		s.RequestRedraw()
	}
}

// NoteSessionActivity queues a Tab activity marker for a Session that
// changed outside the UI thread. Flush applies it to the layout; see
// applySessionActivity.
func (s *State) NoteSessionActivity(id model.SessionID) {
	if id == "" {
		return
	}
	s.mu.Lock()
	s.activity = append(s.activity, id)
	s.mu.Unlock()
	s.RequestRedraw()
}

// applySessionActivity marks the Tab owning id with unseen activity unless
// that Session's Pane is the focused Pane of the active Tab, where the change
// is already visible.
func (s *State) applySessionActivity(id model.SessionID) {
	for index, tab := range s.tabs {
		for paneID, value := range tab.panes {
			if value.session == nil || value.session.Snapshot().ID != id {
				continue
			}
			if index == s.active {
				if _, focused := tab.focusedPane(); focused == paneID {
					return
				}
			}
			if !tab.Dirty {
				tab.Dirty = true
				s.RequestRedraw()
			}
			return
		}
	}
}

// CloseActiveTab closes the active work Tab's terminal surfaces and Sessions,
// then removes the Tab and selects the preceding Tab, or the new first Tab.
// It reports whether a Tab was closed.
func (s *State) CloseActiveTab() bool {
	return s.CloseTab(s.active)
}

// CloseTab closes the Tab at index.
func (s *State) CloseTab(index int) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	tab := s.tabs[index]
	closeTab(tab)
	s.removeTabAt(index)
	s.save()
	s.RequestRedraw()
	return true
}

// CloseOtherTabs closes every Tab except the one at index.
func (s *State) CloseOtherTabs(index int) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	keep := s.tabs[index]
	closed := false
	for _, tab := range s.tabs {
		if tab == keep {
			continue
		}
		closeTab(tab)
		closed = true
	}
	s.tabs = []*Tab{keep}
	s.active = 0
	s.vaultVisible = false
	s.logsVisible = false
	s.refreshBroadcastState()
	if closed {
		s.save()
		s.RequestRedraw()
	}
	return closed
}

// CloseTabsRight closes every Tab after index.
func (s *State) CloseTabsRight(index int) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	if index == len(s.tabs)-1 {
		return false
	}
	for _, tab := range s.tabs[index+1:] {
		closeTab(tab)
	}
	s.tabs = s.tabs[:index+1]
	if s.active > index {
		s.active = index
	}
	s.refreshBroadcastState()
	s.save()
	s.RequestRedraw()
	return true
}

// DuplicateTab clones the Tab at index, opens fresh Sessions for every Pane,
// inserts the copy after the source, and selects it.
func (s *State) DuplicateTab(index int) error {
	if index < 0 || index >= len(s.tabs) {
		return ErrNoActiveTab
	}
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	source := s.tabs[index]
	copyTab, err := s.cloneTab(source)
	if err != nil {
		return err
	}

	s.tabs = append(s.tabs, nil)
	copy(s.tabs[index+2:], s.tabs[index+1:])
	s.tabs[index+1] = copyTab
	s.active = index + 1
	s.vaultVisible = false
	s.logsVisible = false

	for newID, value := range copyTab.panes {
		// A duplicate opens where its source last was.
		s.requestPaneOpen(copyTab.ID, newID, value.descriptor, true)
	}
	s.save()
	s.RequestRedraw()
	return nil
}

// DuplicateTabToWindow clones the Tab at index into a new window, opening
// fresh Sessions for every Pane and leaving the source untouched.
func (s *State) DuplicateTabToWindow(index int) error {
	if index < 0 || index >= len(s.tabs) {
		return ErrNoActiveTab
	}
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	copyTab, err := s.cloneTab(s.tabs[index])
	if err != nil {
		return err
	}
	s.detached = append(s.detached, copyTab)
	for newID, value := range copyTab.panes {
		s.requestPaneOpen(copyTab.ID, newID, value.descriptor, true)
	}
	if s.onDetach != nil {
		s.onDetach(copyTab.ID)
	}
	s.RequestRedraw()
	return nil
}

// cloneTab makes a fresh Tab with new Pane identities and pending Sessions.
func (s *State) cloneTab(source *Tab) (*Tab, error) {
	mapping := make(map[model.PaneID]model.PaneID)
	for _, id := range source.paneIDs() {
		mapping[id] = model.PaneID(newRuntimeID("pane-"))
	}
	root, err := cloneTabTree(source.Root, mapping)
	if err != nil {
		return nil, err
	}

	copyTab := &Tab{
		ID:          model.TabID(newRuntimeID("tab-")),
		Number:      s.nextTabNumber,
		TitleNumber: source.TitleNumber,
		Label:       source.Label,
		AutoTitle:   source.AutoTitle,
		Copy:        source.Copy + 1,
		Root:        root,
		panes:       make(map[model.PaneID]*pane, len(mapping)),
	}
	if copyTab.TitleNumber == 0 {
		copyTab.TitleNumber = source.Number
	}
	s.nextTabNumber++
	for oldID, newID := range mapping {
		descriptor := source.panes[oldID].descriptor
		copyTab.panes[newID] = &pane{ID: newID, descriptor: descriptor, pending: true}
	}
	copyTab.setFocus(mapping[mustFocusedID(source)])
	return copyTab, nil
}

func mustFocusedID(tab *Tab) model.PaneID {
	_, id := tab.focusedPane()
	return id
}

func cloneTabTree(n *domainworkspace.Node, mapping map[model.PaneID]model.PaneID) (*domainworkspace.Node, error) {
	if n == nil {
		return nil, domainworkspace.ErrPaneNotFound
	}
	if n.IsPane() {
		mapped, ok := mapping[n.PaneID]
		if !ok {
			return nil, domainworkspace.ErrPaneNotFound
		}
		return domainworkspace.NewPaneNode(mapped), nil
	}
	first, err := cloneTabTree(n.First, mapping)
	if err != nil {
		return nil, err
	}
	second, err := cloneTabTree(n.Second, mapping)
	if err != nil {
		return nil, err
	}
	return domainworkspace.NewSplitNode(
		model.SplitID(newRuntimeID("split-")), n.Direction, n.Ratio, first, second), nil
}

// DetachTab removes the Tab at index from the main window without closing its
// Sessions, and reports the move through WithDetachHandler. Ticket 13 owns the
// detached window that adopts it.
func (s *State) DetachTab(index int) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	tab := s.tabs[index]
	s.removeTabAt(index)
	s.detached = append(s.detached, tab)
	if s.onDetach != nil {
		s.onDetach(tab.ID)
	}
	s.save()
	s.RequestRedraw()
	return true
}

// DetachedTabIDs returns the Tabs that left the main window.
func (s *State) DetachedTabIDs() []model.TabID {
	ids := make([]model.TabID, len(s.detached))
	for index, tab := range s.detached {
		ids[index] = tab.ID
	}
	return ids
}

// DetachedTabInfo returns the description of one detached Tab.
func (s *State) DetachedTabInfo(id model.TabID) (DetachedTabInfo, bool) {
	tab := s.detachedTabByID(id)
	if tab == nil {
		return DetachedTabInfo{}, false
	}
	panes := tab.paneIDs()
	return DetachedTabInfo{
		ID:          tab.ID,
		Number:      tab.Number,
		TitleNumber: tab.TitleNumber,
		Label:       tab.Label,
		AutoTitle:   tab.AutoTitle,
		Copy:        tab.Copy,
		Panes:       panes,
	}, true
}

// DetachedTabTree returns a detached Tab's split tree and focus path.
func (s *State) DetachedTabTree(id model.TabID) (*domainworkspace.Node, []int, bool) {
	tab := s.detachedTabByID(id)
	if tab == nil {
		return nil, nil, false
	}
	return tab.Root, append([]int(nil), tab.FocusPath...), true
}

// DetachedPaneInfoAt returns the Pane with paneID in a detached Tab.
func (s *State) DetachedPaneInfoAt(tabID model.TabID, paneID model.PaneID) (PaneInfo, bool) {
	tab := s.detachedTabByID(tabID)
	if tab == nil {
		return PaneInfo{}, false
	}
	return paneInfo(tab, paneID)
}

// DetachedTabExists reports whether a detached window still owns the Tab.
func (s *State) DetachedTabExists(id model.TabID) bool {
	return s.detachedTabByID(id) != nil
}

// FocusDetachedPaneID moves focus inside a detached Tab.
func (s *State) FocusDetachedPaneID(tabID model.TabID, paneID model.PaneID) bool {
	tab := s.detachedTabByID(tabID)
	if tab == nil {
		return false
	}
	if !domainworkspace.ContainsPane(tab.Root, paneID) {
		return false
	}
	tab.setFocus(paneID)
	s.RequestRedraw()
	return true
}

// SetDetachedSplitRatio stores a split ratio in a detached Tab so a resize
// survives until the Tab moves back.
func (s *State) SetDetachedSplitRatio(tabID model.TabID, splitID model.SplitID, ratio float32) bool {
	tab := s.detachedTabByID(tabID)
	if tab == nil {
		return false
	}
	root, ok := domainworkspace.WithSplitRatio(tab.Root, splitID, ratio)
	if !ok {
		return false
	}
	tab.Root = root
	return true
}

// RequestDetachedClose asks the composition root to close a detached Tab's
// window. The app owns close intent and the window lifecycle.
func (s *State) RequestDetachedClose(id model.TabID) {
	if s.onDetachedClose != nil {
		s.onDetachedClose(id)
	}
}

// ReattachDetachedTab moves a detached Tab back into the main Workspace,
// keeping its Sessions and surfaces alive. The composition root closes the
// now-empty window.
func (s *State) ReattachDetachedTab(id model.TabID) bool {
	for index, tab := range s.detached {
		if tab.ID != id {
			continue
		}
		s.detached = append(s.detached[:index], s.detached[index+1:]...)
		s.tabs = append(s.tabs, tab)
		s.active = len(s.tabs) - 1
		s.vaultVisible = false
		s.logsVisible = false
		// A reattached Session is again part of the main target list. It is
		// not selected automatically; the panel shows it as an available row.
		s.refreshBroadcastState()
		s.save()
		if s.onDetachReattached != nil {
			s.onDetachReattached(id)
		}
		s.RequestRedraw()
		return true
	}
	return false
}

// DetachPane moves one Pane out of a Tab into its own detached window. When
// the Pane is the Tab's only leaf the whole Tab moves. The Session and its
// surface are transferred, not recreated.
func (s *State) DetachPane(tabIndex int, paneID model.PaneID) bool {
	if tabIndex < 0 || tabIndex >= len(s.tabs) {
		return false
	}
	tab := s.tabs[tabIndex]
	value, ok := tab.panes[paneID]
	if !ok || !domainworkspace.ContainsPane(tab.Root, paneID) {
		return false
	}
	if len(tab.paneIDs()) == 1 {
		return s.DetachTab(tabIndex)
	}

	focused, _ := tab.focusedPane()
	focusedID := model.PaneID("")
	if focused != nil {
		focusedID = focused.ID
	}
	root, err := domainworkspace.RemovePane(tab.Root, paneID)
	if err != nil {
		return false
	}
	tab.Root = root
	delete(tab.panes, paneID)
	remainingFocus := domainworkspace.FocusAfterRemove(root, focusedID, paneID)
	tab.FocusPath = domainworkspace.FocusPath(root, remainingFocus)

	label := value.descriptor.Label
	autoTitle := label == ""
	number := s.nextTabNumber
	s.nextTabNumber++
	detachedRoot := domainworkspace.NewPaneNode(paneID)
	detached := &Tab{
		ID:          model.TabID(newRuntimeID("tab-")),
		Number:      number,
		TitleNumber: value.descriptor.Number,
		Label:       label,
		AutoTitle:   autoTitle,
		Root:        detachedRoot,
		FocusPath:   domainworkspace.FocusPath(detachedRoot, paneID),
		panes:       map[model.PaneID]*pane{paneID: value},
	}
	if detached.TitleNumber == 0 {
		detached.TitleNumber = number
	}
	s.detached = append(s.detached, detached)
	s.refreshBroadcastState()
	if s.onDetach != nil {
		s.onDetach(detached.ID)
	}
	s.save()
	s.RequestRedraw()
	return true
}

// ActiveSessionCount returns the active Sessions in every window: running or
// still opening. Exit screens and failed Panes do not count.
func (s *State) ActiveSessionCount() int {
	return activePaneCountIn(s.tabs) + activePaneCountIn(s.detached)
}

// ActiveSessionCountMain returns the active Sessions in the main window.
func (s *State) ActiveSessionCountMain() int {
	return activePaneCountIn(s.tabs)
}

// ActiveSessionCountDetached returns the active Sessions in one detached
// window.
func (s *State) ActiveSessionCountDetached(id model.TabID) int {
	tab := s.detachedTabByID(id)
	if tab == nil {
		return 0
	}
	return activePaneCountIn([]*Tab{tab})
}

// CloseMainSessions ends the Sessions shown by the main window without
// touching detached windows. The persisted layout is left intact.
func (s *State) CloseMainSessions() {
	for _, tab := range s.tabs {
		closeTab(tab)
	}
}

// CloseAllSessions ends the Sessions of every window. The app calls it on
// quit, after the windows are already gone.
func (s *State) CloseAllSessions() {
	s.CloseMainSessions()
	for _, tab := range s.detached {
		closeTab(tab)
	}
	s.detached = nil
}

// CloseDetachedTab closes a Tab that left the main window, ending its
// Sessions. It is also the idempotent close hook for a detached window.
func (s *State) CloseDetachedTab(id model.TabID) bool {
	for index, tab := range s.detached {
		if tab.ID != id {
			continue
		}
		closeTab(tab)
		s.detached = append(s.detached[:index], s.detached[index+1:]...)
		s.RequestRedraw()
		return true
	}
	return false
}

// detachedTabByID finds a Tab owned by a detached window.
func (s *State) detachedTabByID(id model.TabID) *Tab {
	for _, tab := range s.detached {
		if tab.ID == id {
			return tab
		}
	}
	return nil
}

// activePaneCountIn counts Panes with a running Session or an open in flight.
func activePaneCountIn(tabs []*Tab) int {
	count := 0
	for _, tab := range tabs {
		if tab == nil {
			continue
		}
		for _, value := range tab.panes {
			if value == nil {
				continue
			}
			if value.pending {
				count++
				continue
			}
			if value.session != nil && value.session.Snapshot().Status == appsession.StatusRunning {
				count++
			}
		}
	}
	return count
}

// paneInfo describes one Pane for either a main or a detached Tab.
func paneInfo(tab *Tab, id model.PaneID) (PaneInfo, bool) {
	value, ok := tab.panes[id]
	if !ok || !domainworkspace.ContainsPane(tab.Root, id) {
		return PaneInfo{}, false
	}
	focused, focusedID := tab.focusedPane()
	return PaneInfo{
		ID:      id,
		Number:  value.descriptor.Number,
		Label:   value.descriptor.Label,
		Focused: id == focusedID && focused != nil,
		Pending: value.pending,
		Err:     value.err,
		Surface: value.surface,
	}, true
}

// SplitFocusedPane splits the focused Pane on side and opens a Session of the
// same kind in the new Pane.
func (s *State) SplitFocusedPane(side domainworkspace.Side) error {
	tab, _ := s.activeTab()
	if tab == nil {
		return ErrNoActiveTab
	}
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	value, focusedID := tab.focusedPane()
	if value == nil {
		return ErrNoActiveTab
	}
	newPaneID := model.PaneID(newRuntimeID("pane-"))
	root, err := domainworkspace.SplitAt(
		tab.Root, focusedID, newPaneID,
		model.SplitID(newRuntimeID("split-")), side)
	if err != nil {
		return err
	}
	tab.Root = root
	tab.panes[newPaneID] = &pane{
		ID:         newPaneID,
		descriptor: value.descriptor,
		pending:    true,
	}
	tab.setFocus(newPaneID)
	// The new Pane inherits the source Session's last known directory, as the
	// original split did when it copied the shell's current cwd.
	s.requestPaneOpen(tab.ID, newPaneID, value.descriptor, true)
	s.save()
	s.RequestRedraw()
	return nil
}

// SplitRight splits the focused Pane into left and right halves.
func (s *State) SplitRight() error { return s.SplitFocusedPane(domainworkspace.SideRight) }

// SplitDown splits the focused Pane into top and bottom halves.
func (s *State) SplitDown() error { return s.SplitFocusedPane(domainworkspace.SideBottom) }

// CloseFocusedPane closes the focused Pane and collapses its sibling subtree.
// Closing the last Pane closes its Tab. Focus moves to a deterministic
// remaining Pane.
func (s *State) CloseFocusedPane() bool {
	tab, index := s.activeTab()
	if tab == nil {
		return false
	}
	value, focusedID := tab.focusedPane()
	if value == nil {
		return false
	}
	if len(tab.paneIDs()) == 1 {
		return s.CloseTab(index)
	}

	root, err := domainworkspace.RemovePane(tab.Root, focusedID)
	if err != nil {
		return false
	}
	next := domainworkspace.FocusAfterRemove(root, focusedID, focusedID)
	closePane(value)
	delete(tab.panes, focusedID)
	tab.Root = root
	if next == "" {
		return s.CloseTab(index)
	}
	tab.setFocus(next)
	s.refreshBroadcastState()
	s.save()
	s.RequestRedraw()
	return true
}

// FocusPaneID gives focus to one Pane in the Tab at index.
func (s *State) FocusPaneID(index int, paneID model.PaneID) bool {
	if index < 0 || index >= len(s.tabs) {
		return false
	}
	tab := s.tabs[index]
	if !domainworkspace.ContainsPane(tab.Root, paneID) {
		return false
	}
	tab.setFocus(paneID)
	s.save()
	s.RequestRedraw()
	return true
}

// SetSplitRatio updates a split's divider ratio, which a drag changed.
func (s *State) SetSplitRatio(index int, splitID model.SplitID, ratio float32) bool {
	if index < 0 || index >= len(s.tabs) || splitID == "" {
		return false
	}
	root, ok := domainworkspace.WithSplitRatio(s.tabs[index].Root, splitID, ratio)
	if !ok {
		return false
	}
	s.tabs[index].Root = root
	s.save()
	return true
}

// FocusPane moves focus to the geometrically nearest Pane in direction,
// wrapping around the layout.
func (s *State) FocusPane(direction domainworkspace.FocusDirection) bool {
	tab, _ := s.activeTab()
	if tab == nil {
		return false
	}
	_, focusedID := tab.focusedPane()
	if focusedID == "" {
		return false
	}
	next := domainworkspace.NextFocus(tab.Root, focusedID, direction)
	if next == "" || next == focusedID {
		return false
	}
	tab.setFocus(next)
	s.save()
	s.RequestRedraw()
	return true
}

// MoveTab reorders a Tab relative to another target. It reports false for an
// unknown target or a drop onto itself.
func (s *State) MoveTab(source, target model.TabID, after bool) bool {
	if source == target {
		return false
	}
	sourceIndex, targetIndex := -1, -1
	for index, tab := range s.tabs {
		switch tab.ID {
		case source:
			sourceIndex = index
		case target:
			targetIndex = index
		}
	}
	if sourceIndex < 0 || targetIndex < 0 {
		return false
	}
	tab := s.tabs[sourceIndex]
	var active *Tab
	if s.active >= 0 && s.active < len(s.tabs) {
		active = s.tabs[s.active]
	}
	s.tabs = append(s.tabs[:sourceIndex], s.tabs[sourceIndex+1:]...)
	if sourceIndex < targetIndex {
		targetIndex--
	}
	insert := targetIndex
	if after {
		insert++
	}
	s.tabs = append(s.tabs, nil)
	copy(s.tabs[insert+1:], s.tabs[insert:])
	s.tabs[insert] = tab
	for index, candidate := range s.tabs {
		if candidate == active {
			s.active = index
			break
		}
	}
	s.save()
	s.RequestRedraw()
	return true
}

// MovePane moves a Pane beside another Pane. Within one Tab the Pane is
// removed and reinserted; across Tabs the Pane changes owner. Dropping a Pane
// on itself is rejected.
func (s *State) MovePane(sourceTabID model.TabID, sourcePaneID model.PaneID,
	targetTabID model.TabID, targetPaneID model.PaneID, side domainworkspace.Side) bool {
	if sourcePaneID == targetPaneID {
		return false
	}
	source, sourceIndex := s.tabByID(sourceTabID)
	target, targetIndex := s.tabByID(targetTabID)
	if source == nil || target == nil {
		return false
	}
	value, ok := source.panes[sourcePaneID]
	if !ok || !domainworkspace.ContainsPane(source.Root, sourcePaneID) {
		return false
	}
	if !domainworkspace.ContainsPane(target.Root, targetPaneID) {
		return false
	}

	if source == target {
		root, err := domainworkspace.MoveWithin(
			source.Root, sourcePaneID, targetPaneID,
			model.SplitID(newRuntimeID("split-")), side)
		if err != nil {
			return false
		}
		source.Root = root
		source.setFocus(sourcePaneID)
		s.save()
		s.RequestRedraw()
		return true
	}

	sourceRoot, err := domainworkspace.RemovePane(source.Root, sourcePaneID)
	if err != nil {
		return false
	}
	targetRoot, err := domainworkspace.SplitAt(
		target.Root, targetPaneID, sourcePaneID,
		model.SplitID(newRuntimeID("split-")), side)
	if err != nil {
		return false
	}

	delete(source.panes, sourcePaneID)
	target.panes[sourcePaneID] = value
	target.Root = targetRoot
	target.setFocus(sourcePaneID)

	if len(domainworkspace.CollectPaneIDs(sourceRoot)) == 0 {
		// The source Tab lost its last Pane; it ceases to exist. Its Sessions
		// all moved with the Pane, so nothing is closed.
		s.removeTabAt(sourceIndex)
		if sourceIndex < targetIndex {
			targetIndex--
		}
		s.active = targetIndex
	} else {
		source.Root = sourceRoot
		source.FocusPath = domainworkspace.FocusPath(sourceRoot, "")
		s.active = targetIndex
	}
	s.vaultVisible = false
	s.logsVisible = false
	s.save()
	s.RequestRedraw()
	return true
}

// MovePaneToTab drops a Pane onto another Tab, placing it beside that Tab's
// focused Pane. Dropping a Pane on its own Tab is rejected.
func (s *State) MovePaneToTab(sourceTabID model.TabID, sourcePaneID model.PaneID,
	targetTabID model.TabID) bool {
	if sourceTabID == targetTabID {
		return false
	}
	target, _ := s.tabByID(targetTabID)
	if target == nil {
		return false
	}
	_, focusedID := target.focusedPane()
	if focusedID == "" {
		return false
	}
	return s.MovePane(sourceTabID, sourcePaneID, targetTabID, focusedID, domainworkspace.SideRight)
}

// WorkspacePayload snapshots the main window's layout for persistence.
// Scrollback and terminal output are deliberately absent.
func (s *State) WorkspacePayload() domainworkspace.Payload {
	payload := domainworkspace.EmptyPayload()
	payload.ActiveTab = s.active
	for _, tab := range s.tabs {
		payload.Tabs = append(payload.Tabs, domainworkspace.RestoredTab{
			ID:          tab.ID,
			Number:      tab.Number,
			TitleNumber: tab.TitleNumber,
			Title:       tab.Label,
			AutoTitle:   tab.AutoTitle,
			Copy:        tab.Copy,
			Root:        domainworkspace.Clone(tab.Root),
			FocusPath:   append([]int(nil), tab.FocusPath...),
		})
		for _, id := range tab.paneIDs() {
			value := tab.panes[id]
			if value == nil {
				continue
			}
			payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
				ID:      id,
				Session: value.descriptor,
			})
		}
	}
	if len(payload.Tabs) == 0 {
		payload.ActiveTab = -1
	}
	return payload
}

// Persist stores the current layout now. Structural actions already save; the
// app calls this at shutdown for the active Tab selection.
func (s *State) Persist() {
	s.refreshWorkingDirectories()
	s.save()
}

// refreshWorkingDirectories records the last directory each terminal reported
// through OSC 7. That value is what restore reopens locally, and what a
// restored SSH Pane replays with cd.
func (s *State) refreshWorkingDirectories() {
	changed := false
	for _, tab := range s.tabs {
		for _, value := range tab.panes {
			reporter, ok := value.surface.(interface{ Dir() string })
			if !ok {
				continue
			}
			dir := reporter.Dir()
			if dir == "" || value.descriptor.Dir == dir {
				continue
			}
			value.descriptor.Dir = dir
			changed = true
		}
	}
	if changed {
		s.save()
	}
}

func (s *State) save() {
	if s.workspace == nil || s.restoring {
		return
	}
	_ = s.workspace.Save(context.Background(), s.WorkspacePayload())
}

// Restore applies the stored Workspace layout. It is a no-op when persistence
// is not configured, the payload is empty, or Tabs already exist. Panes open
// asynchronously and appear when Flush applies their outcomes; output and
// scrollback are not restored.
func (s *State) Restore(ctx context.Context) {
	if s.workspace == nil || len(s.tabs) > 0 || s.sessions == nil || s.surfaces == nil {
		return
	}
	payload := s.workspace.Load(ctx)
	if len(payload.Tabs) == 0 {
		return
	}
	descriptors := make(map[model.PaneID]domainworkspace.SessionDescriptor, len(payload.Panes))
	for _, value := range payload.Panes {
		descriptors[value.ID] = value.Session
	}

	s.restoring = true
	defer func() { s.restoring = false }()

	for _, restored := range payload.Tabs {
		root := domainworkspace.Clone(restored.Root)
		if root == nil {
			continue
		}
		number := restored.Number
		if number <= 0 {
			number = s.nextTabNumber
		}
		if number >= s.nextTabNumber {
			s.nextTabNumber = number + 1
		}
		tab := &Tab{
			ID:          restored.ID,
			Number:      number,
			TitleNumber: restored.TitleNumber,
			Label:       restored.Title,
			AutoTitle:   restored.AutoTitle,
			Copy:        restored.Copy,
			Root:        root,
			panes:       make(map[model.PaneID]*pane),
		}
		if tab.TitleNumber == 0 {
			tab.TitleNumber = number
		}
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			descriptor, ok := descriptors[id]
			if !ok {
				continue
			}
			if descriptor.Kind == domainworkspace.SessionLocal {
				descriptor.Dir = validLocalDir(descriptor.Dir)
			}
			if descriptor.Label == "" {
				descriptor.Label = restored.Title
			}
			tab.panes[id] = &pane{ID: id, descriptor: descriptor, pending: true}
		}
		tab.FocusPath = domainworkspace.SanitizeFocusPath(root, restored.FocusPath)
		s.tabs = append(s.tabs, tab)
	}
	if len(s.tabs) == 0 {
		return
	}
	s.active = payload.ActiveTab
	if s.active < 0 || s.active >= len(s.tabs) {
		s.active = 0
	}
	s.vaultVisible = false
	s.logsVisible = false
	for _, tab := range s.tabs {
		for id, value := range tab.panes {
			s.requestPaneOpen(tab.ID, id, value.descriptor, true)
		}
	}
	s.RequestRedraw()
}

func (s *State) requestPaneOpen(tabID model.TabID, paneID model.PaneID,
	descriptor domainworkspace.SessionDescriptor, restore bool) {
	if s.sessions == nil {
		return
	}
	ctx := s.ctx
	go func() {
		session, err := s.openDescriptor(ctx, descriptor)
		s.mu.Lock()
		s.paneOutcomes = append(s.paneOutcomes, paneOutcome{
			tabID: tabID, paneID: paneID, session: session, restore: restore, err: err,
		})
		s.mu.Unlock()
		s.RequestRedraw()
	}()
}

func (s *State) applyPaneOutcome(outcome paneOutcome) {
	tab, _ := s.tabByAnyID(outcome.tabID)
	if tab == nil {
		if outcome.session != nil {
			_ = outcome.session.Close()
		}
		return
	}
	value, ok := tab.panes[outcome.paneID]
	if !ok || !domainworkspace.ContainsPane(tab.Root, outcome.paneID) {
		if outcome.session != nil {
			_ = outcome.session.Close()
		}
		return
	}
	if outcome.err != nil {
		value.pending = false
		value.err = outcome.err
		return
	}
	surface, err := s.surfaces(outcome.session)
	if err != nil {
		_ = outcome.session.Close()
		value.pending = false
		value.err = err
		return
	}
	value.session = outcome.session
	value.surface = surface
	value.pending = false
	value.err = nil
	if outcome.restore {
		restoreRemoteDirectory(outcome.session, value.descriptor)
	}
}

// restoreRemoteDirectory replays the last known remote directory. It accepts
// the same POSIX forms as the original restore: an absolute path, ~, or a
// ~/ path. A Windows path, a relative value, or ~user is ignored. Quoting
// keeps the path shell data.
func restoreRemoteDirectory(session *appsession.Session,
	descriptor domainworkspace.SessionDescriptor) {
	if session == nil || descriptor.Kind != domainworkspace.SessionSSH {
		return
	}
	if !remoteDirEligible(descriptor.Dir) {
		return
	}
	quoted := "'" + strings.ReplaceAll(descriptor.Dir, "'", "'\\''") + "'"
	_, _ = session.TerminalConn().Write([]byte("cd -- " + quoted + "\n"))
}

func remoteDirEligible(dir string) bool {
	switch {
	case dir == "":
		return false
	case strings.HasPrefix(dir, "//"):
		return false
	case strings.HasPrefix(dir, "/"):
		return true
	case dir == "~":
		return true
	default:
		return strings.HasPrefix(dir, "~/")
	}
}

func (s *State) openDescriptor(ctx context.Context,
	descriptor domainworkspace.SessionDescriptor) (*appsession.Session, error) {
	if s.sessions == nil {
		return nil, ErrNoSessions
	}
	spec := appsession.StartSpec{
		Label:         descriptor.Label,
		Dir:           validLocalDir(descriptor.Dir),
		X11Forwarding: descriptor.X11Forwarding,
	}
	switch descriptor.Kind {
	case domainworkspace.SessionLocal:
		spec.Kind = appsession.KindLocal
	case domainworkspace.SessionSSH:
		spec.Kind = appsession.KindSSH
		spec.HostID = descriptor.HostID
		spec.Hostname = descriptor.Hostname
		spec.Port = descriptor.Port
		spec.Username = descriptor.Username
		if s.resolveHost != nil {
			host, ok := s.resolveHost(ctx, descriptor.HostID)
			if !ok {
				return nil, ErrHostUnavailable
			}
			spec.HostID = host.ID
			spec.Hostname = host.Hostname
			spec.Port = host.Port
			spec.Username = host.Username
			spec.X11Forwarding = host.X11Enabled()
			spec.Dir = ""
		}
		spec.Reconnect = &appsession.ReconnectPolicy{}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownSession, descriptor.Kind)
	}
	return s.sessions.Open(ctx, spec)
}

// validLocalDir keeps dir only when it still names a directory. The local PTY
// falls back to the user's home otherwise; clearing it here keeps the
// persisted descriptor honest as well.
func validLocalDir(dir string) string {
	if dir == "" {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

func closeTab(tab *Tab) {
	for _, value := range tab.panes {
		closePane(value)
	}
}

func closePane(value *pane) {
	if value == nil {
		return
	}
	if value.surface != nil {
		value.surface.Close()
	}
	if value.session != nil {
		_ = value.session.Close()
	}
}

// removeTabAt removes the Tab at index and keeps the active index valid. It
// does not close Sessions.
func (s *State) removeTabAt(index int) {
	if index < 0 || index >= len(s.tabs) {
		return
	}
	s.tabs = append(s.tabs[:index], s.tabs[index+1:]...)
	if len(s.tabs) == 0 {
		s.active = -1
		s.vaultVisible = true
		s.refreshBroadcastState()
		return
	}
	if s.active == index {
		if index >= len(s.tabs) {
			s.active = len(s.tabs) - 1
		}
	} else if index < s.active {
		s.active--
	}
	s.vaultVisible = false
	s.logsVisible = false
	s.refreshBroadcastState()
}

// SetTerminalFont restyles every open terminal in the main and detached
// windows. A Surface that cannot restyle live is left alone; a new Session
// reads the setting when its terminal is created.
func (s *State) SetTerminalFont(family string, size float32) {
	apply := func(tab *Tab) {
		if tab == nil {
			return
		}
		for _, pane := range tab.panes {
			if pane == nil {
				continue
			}
			setter, ok := pane.surface.(TerminalFontSetter)
			if !ok {
				continue
			}
			setter.SetTerminalFont(family, size)
		}
	}
	for _, tab := range s.tabs {
		apply(tab)
	}
	for _, tab := range s.detached {
		apply(tab)
	}
}

// SetTerminalTheme recolors every open terminal in the main and detached
// windows. A Surface that cannot recolor live is left alone; a new Session
// reads the setting when its terminal is created.
func (s *State) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	apply := func(tab *Tab) {
		if tab == nil {
			return
		}
		for _, pane := range tab.panes {
			if pane == nil {
				continue
			}
			setter, ok := pane.surface.(TerminalThemeSetter)
			if !ok {
				continue
			}
			setter.SetTerminalTheme(preset)
		}
	}
	for _, tab := range s.tabs {
		apply(tab)
	}
	for _, tab := range s.detached {
		apply(tab)
	}
}
