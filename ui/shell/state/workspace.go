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
	appview "github.com/mslxl/natcatty/application/view"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainsessiontitle "github.com/mslxl/natcatty/domain/sessiontitle"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// ErrUnknownSession reports a restored Pane descriptor this shell cannot
// reopen.
var ErrUnknownSession = errors.New("shell: unknown Session kind")

// ErrUnavailablePane reports a restored Pane whose content kind this build
// cannot show, such as a View or an unknown kind. The Pane stays in the layout
// as a placeholder; it is not retried.
var ErrUnavailablePane = errors.New("shell: Pane content unavailable")

// etResumeCleanupTimeout bounds clearing one saved ET pair from the shell. The
// store is local, so deletes are normally immediate; the bound keeps a stuck
// store from blocking a Pane close or a restore fallback indefinitely.
const etResumeCleanupTimeout = 5 * time.Second

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
	// GeneratedTitle marks a title composed from the merged Panes' connection
	// labels, such as "web-01/db-02". It keeps following pane renames and
	// membership until the user renames the Tab.
	GeneratedTitle bool
	// Copy counts duplicate generations: 0 is the original, 1 the first copy.
	Copy      int
	Dirty     bool
	Root      *domainworkspace.Node
	FocusPath []int
	panes     map[model.PaneID]*pane
}

// paneContent is the shell-side union of Pane contents. Session content owns
// the live Session; View content will own a host-owned View instance. Only
// Session content is constructed in this release.
type paneContent interface {
	kind() domainworkspace.PaneKind
	// restore returns the kinded descriptor that rebuilds this content.
	restore() domainworkspace.PaneDescriptor
}

// sessionContent is a Pane's terminal Session and its non-secret restore
// descriptor. session is nil until the asynchronous open succeeds.
type sessionContent struct {
	descriptor domainworkspace.SessionDescriptor
	session    *appsession.Session
	surface    Surface
	err        error
	pending    bool
	// agent is the last coding-CLI decision for the Session. It carries across
	// ambiguous OSC titles, as the original's provider tracking does, and is
	// remade when the Session or its title changes.
	agent        bool
	titleSeen    string
	titleSession *appsession.Session
}

func (c *sessionContent) kind() domainworkspace.PaneKind { return domainworkspace.PaneKindSession }

func (c *sessionContent) restore() domainworkspace.PaneDescriptor {
	return domainworkspace.NewSessionPaneDescriptor(c.descriptor)
}

// viewContent is a Pane's live View Instance and its bounded restore
// descriptor. instance is nil only for a restored placeholder this build
// cannot open; the placements work adds View restore.
type viewContent struct {
	instance   *appview.Instance
	descriptor domainworkspace.ViewPaneDescriptor
}

func (c *viewContent) kind() domainworkspace.PaneKind { return domainworkspace.PaneKindView }

func (c *viewContent) restore() domainworkspace.PaneDescriptor {
	view := c.descriptor
	if c.instance != nil && !c.instance.Closed() {
		view = c.instance.Descriptor()
	}
	return domainworkspace.PaneDescriptor{Kind: domainworkspace.PaneKindView, View: &view}
}

type pane struct {
	ID      model.PaneID
	content paneContent
	// unavailable preserves a restored View or unknown content kind. Such a
	// Pane stays in the layout as an error placeholder and never opens.
	unavailable *domainworkspace.PaneDescriptor
	surface     Surface
	err         error
	pending     bool
}

func newSessionPane(id model.PaneID, descriptor domainworkspace.SessionDescriptor) *pane {
	return &pane{ID: id, content: &sessionContent{descriptor: descriptor}}
}

func (p *pane) sessionContent() *sessionContent {
	if p == nil {
		return nil
	}
	content, _ := p.content.(*sessionContent)
	return content
}

// viewContent returns the View content of this Pane, or nil for Session and
// unavailable content.
func (p *pane) viewContent() *viewContent {
	if p == nil {
		return nil
	}
	content, _ := p.content.(*viewContent)
	return content
}

// session returns the live Session bound to this Pane, or nil for a Pane that
// is still opening or holds non-Session content.
func (p *pane) session() *appsession.Session {
	if content := p.sessionContent(); content != nil {
		return content.session
	}
	return nil
}

// sessionDescriptor returns the Session restore data, or nil for non-Session
// content.
func (p *pane) sessionDescriptor() *domainworkspace.SessionDescriptor {
	if content := p.sessionContent(); content != nil {
		return &content.descriptor
	}
	return nil
}

// restoreDescriptor returns the kinded descriptor persisted for this Pane.
func (p *pane) restoreDescriptor() domainworkspace.PaneDescriptor {
	if p == nil {
		return domainworkspace.PaneDescriptor{}
	}
	if p.unavailable != nil {
		return *p.unavailable
	}
	if p.content != nil {
		return p.content.restore()
	}
	return domainworkspace.PaneDescriptor{}
}

// cloneForDuplicate copies one Pane into a duplicated Tab. A Session gets a
// fresh ET resume key and opens again; an unavailable placeholder is copied
// as-is without opening anything.
func (p *pane) cloneForDuplicate(id model.PaneID) *pane {
	if content := p.sessionContent(); content != nil {
		clone := newSessionPane(id, withFreshResumeKey(content.descriptor))
		clone.pending = true
		return clone
	}
	if p.unavailable != nil {
		descriptor := *p.unavailable
		return &pane{ID: id, unavailable: &descriptor, err: ErrUnavailablePane}
	}
	if p.content != nil {
		// A View Pane without a live instance has no opener; keep the duplicate
		// as the same unavailable placeholder. Live View Panes are duplicated
		// through the View registry before cloneTab copies a Tab.
		descriptor := p.content.restore()
		return &pane{ID: id, unavailable: &descriptor, err: ErrUnavailablePane}
	}
	return &pane{ID: id}
}

// contentKind reports the restored content kind for this Pane.
func (p *pane) contentKind() domainworkspace.PaneKind {
	if p == nil {
		return ""
	}
	if p.unavailable != nil {
		return p.unavailable.Kind
	}
	if p.content != nil {
		return p.content.kind()
	}
	return ""
}

// TabOptions seeds a new Tab.
type TabOptions struct {
	Label          string
	AutoTitle      bool
	GeneratedTitle bool
	Copy           int
	Content        domainworkspace.PaneDescriptor
}

// TabInfo is the immutable Tab description the view renders.
type TabInfo struct {
	ID          model.TabID
	Number      int
	TitleNumber int
	// Label is the Tab's stored title: the connection label snapshot, or a
	// legacy user-chosen title.
	Label string
	// Title is what the tab strip shows, resolved from the focused Session's
	// connection label, its OSC 0/2 title, and the Dynamic tab title setting.
	// Empty means the view falls back to the numbered local-session name.
	Title     string
	AutoTitle bool
	Copy      int
	Dirty     bool
	Active    bool
	// ViewInstanceID is set when the Tab's only content is a View Instance.
	// The tab menu then offers the View duplicate rule instead of cloning the
	// pane as an unavailable placeholder.
	ViewInstanceID string
	// ViewPlacement is the instance's current placement (Pane or Tab for a
	// work Tab).
	ViewPlacement domainplugin.Placement
	// ViewMulti reports whether the View contribution allows duplicates.
	ViewMulti bool
}

// PaneInfo is the immutable Pane description the view renders.
type PaneInfo struct {
	ID model.PaneID
	// Kind is the content kind this Pane holds. A Pane this build cannot show
	// reports its restored kind (often view or an unknown value) and Err.
	Kind   domainworkspace.PaneKind
	Number int
	// Label is the Pane's connection label: its user rename or Host label.
	Label string
	// Title is the Pane header title, resolved like TabInfo.Title.
	Title   string
	Focused bool
	Pending bool
	Err     error
	Surface Surface
	// ViewInstanceID is set for a live View Pane. The Pane menu uses it to
	// move the same instance between placements, and to disable session-only
	// commands that a View Pane cannot honour.
	ViewInstanceID string
	ViewPlacement  domainplugin.Placement
	ViewPlacements []domainplugin.Placement
	ViewMulti      bool
}

// DetachedTabInfo describes a Tab that left the main window and now belongs to
// its own window. Panes lists the Tab's Pane identities in tree order.
type DetachedTabInfo struct {
	ID          model.TabID
	Number      int
	TitleNumber int
	Label       string
	Title       string
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

// newETResumeKey returns the opaque, non-secret locator for a resumable ET
// Session's saved credentials. Every Pane owns a distinct key.
func newETResumeKey() string { return newRuntimeID("et-resume-") }

// withFreshResumeKey gives a duplicated or split ET Session its own resume
// credentials. Copying the source's key would make two Panes reattach the same
// remote shell and fight over ET's single client slot.
func withFreshResumeKey(descriptor domainworkspace.SessionDescriptor) domainworkspace.SessionDescriptor {
	if descriptor.Kind == domainworkspace.SessionET {
		descriptor.ResumeKey = newETResumeKey()
	}
	return descriptor
}

func (s *State) newTab(opts TabOptions) *Tab {
	number := s.nextTabNumber
	s.nextTabNumber++
	content := opts.Content.Normalized()
	paneID := model.PaneID(newRuntimeID("pane-"))
	value := &pane{ID: paneID}
	if content.Kind == domainworkspace.PaneKindSession && content.Session != nil {
		descriptor := *content.Session
		if descriptor.Number == 0 {
			descriptor.Number = number
		}
		value.content = &sessionContent{descriptor: descriptor}
	} else {
		stored := content
		value.unavailable = &stored
		value.err = ErrUnavailablePane
	}
	return &Tab{
		ID:             model.TabID(newRuntimeID("tab-")),
		Number:         number,
		TitleNumber:    number,
		Label:          opts.Label,
		AutoTitle:      opts.AutoTitle,
		GeneratedTitle: opts.GeneratedTitle,
		Copy:           opts.Copy,
		Root:           domainworkspace.NewPaneNode(paneID),
		panes: map[model.PaneID]*pane{
			paneID: value,
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
	if content := value.sessionContent(); content != nil {
		content.session = session
		value.surface = surface
	}
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

// tabByAnyID finds a Tab in the main Workspace or in a detached window.
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

// tabByPaneID finds the Tab that owns a Pane in the main Workspace or in a
// detached window. Pane ids are unique across every Tab, so a pane outcome
// still reaches the right Pane after the Tab was renamed (view:<instanceId>
// reconciliation) or the Pane moved to another container while its Session was
// opening. Resolve by Pane identity, never by the Tab id captured at request
// time: that id can be stale by the time the outcome is applied.
func (s *State) tabByPaneID(paneID model.PaneID) (*Tab, int) {
	if paneID == "" {
		return nil, -1
	}
	for index, tab := range s.tabs {
		if tab == nil || !domainworkspace.ContainsPane(tab.Root, paneID) {
			continue
		}
		if _, ok := tab.panes[paneID]; ok {
			return tab, index
		}
	}
	for _, tab := range s.detached {
		if tab == nil || !domainworkspace.ContainsPane(tab.Root, paneID) {
			continue
		}
		if _, ok := tab.panes[paneID]; ok {
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

// TabLabel returns a Tab's resolved title and stable number. An empty title
// means the view falls back to the numbered local-session name.
func (s *State) TabLabel(index int) (string, int, bool) {
	if index < 0 || index >= len(s.tabs) {
		return "", 0, false
	}
	return s.tabTitle(s.tabs[index]), s.tabs[index].Number, true
}

// RenameTab gives the Tab at index its user title by renaming the focused
// Pane's Session, as the original's Session-tab rename does. An empty title is
// rejected, so a Pane's Rename dialog cannot leave the tab strip blank.
func (s *State) RenameTab(index int, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || index < 0 || index >= len(s.tabs) {
		return false
	}
	s.renameTab(s.tabs[index], title)
	s.save()
	s.RequestRedraw()
	return true
}

// RenamePane gives one Pane's Session a user title. The pane header's rename
// dialog names exactly the Session it belongs to, even when another Pane of
// the Tab has focus.
func (s *State) RenamePane(tabIndex int, paneID model.PaneID, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || tabIndex < 0 || tabIndex >= len(s.tabs) {
		return false
	}
	tab := s.tabs[tabIndex]
	value, ok := tab.panes[paneID]
	if !ok || !domainworkspace.ContainsPane(tab.Root, paneID) {
		return false
	}
	s.renamePane(tab, value, title)
	s.save()
	s.RequestRedraw()
	return true
}

// RenameDetachedPane is RenamePane for a Pane in a detached Tab.
func (s *State) RenameDetachedPane(id model.TabID, paneID model.PaneID, title string) bool {
	title = strings.TrimSpace(title)
	tab := s.detachedTabByID(id)
	if title == "" || tab == nil {
		return false
	}
	value, ok := tab.panes[paneID]
	if !ok || !domainworkspace.ContainsPane(tab.Root, paneID) {
		return false
	}
	s.renamePane(tab, value, title)
	s.save()
	s.RequestRedraw()
	return true
}

// renameTab applies a user title to the focused Pane's Session, as the
// original's tab rename does for a standalone Session Tab. The name then
// persists with that Session, and a split Tab keeps naming itself after the
// focused Pane. A legacy Tab that was renamed before names belonged to
// Sessions keeps its Tab-level title.
func (s *State) renameTab(tab *Tab, title string) {
	value, _ := tab.focusedPane()
	if value == nil {
		tab.Label = title
		tab.AutoTitle = false
		return
	}
	s.renamePane(tab, value, title)
}

func (s *State) renamePane(tab *Tab, value *pane, title string) {
	if content := value.sessionContent(); content != nil {
		content.descriptor.CustomName = title
	}
	if !tab.AutoTitle && !tab.GeneratedTitle {
		tab.Label = title
	}
}

// TabInfo returns everything the tab strip renders for one Tab.
func (s *State) TabInfo(index int) (TabInfo, bool) {
	if index < 0 || index >= len(s.tabs) {
		return TabInfo{}, false
	}
	tab := s.tabs[index]
	info := TabInfo{
		ID:          tab.ID,
		Number:      tab.Number,
		TitleNumber: tab.TitleNumber,
		Label:       tab.Label,
		Title:       s.tabTitle(tab),
		AutoTitle:   tab.AutoTitle,
		Copy:        tab.Copy,
		Dirty:       tab.Dirty,
		Active:      index == s.active && !s.vaultVisible && !s.logsVisible && !s.PageVisible(),
	}
	if ids := tab.paneIDs(); len(ids) == 1 {
		if content := tab.panes[ids[0]].viewContent(); content != nil && content.instance != nil {
			contribution := content.instance.Contribution()
			info.ViewInstanceID = content.instance.ID()
			info.ViewPlacement = content.instance.Placement()
			info.ViewMulti = contribution.Instances.Multi()
		}
	}
	return info, true
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
	return s.paneInfo(s.tabs[tabIndex], id)
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
	s.leaveMonitor()
	s.leaveSystemManager()
	s.vaultVisible = false
	s.logsVisible = false
	s.sftpVisible = false
	s.pageShown = ""
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
	session := value.session()
	if session == nil {
		return appsession.Snapshot{}, false
	}
	return session.Snapshot(), true
}

// TabSnapshot returns the Session snapshot of the focused Pane in the Tab at
// index, for the tab strip's status dot. It reports false when the Tab has no
// live Session.
func (s *State) TabSnapshot(index int) (appsession.Snapshot, bool) {
	if index < 0 || index >= len(s.tabs) {
		return appsession.Snapshot{}, false
	}
	value, _ := s.tabs[index].focusedPane()
	session := value.session()
	if session == nil {
		return appsession.Snapshot{}, false
	}
	return session.Snapshot(), true
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
			session := value.session()
			if session == nil || session.Snapshot().ID != id {
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
	s.closeTab(tab, false)
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
		s.closeTab(tab, false)
		closed = true
	}
	s.tabs = []*Tab{keep}
	s.active = 0
	s.leaveMonitor()
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
		s.closeTab(tab, false)
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
	if view := onlyViewInstance(s.tabs[index]); view != nil {
		// A View Tab follows the View duplicate rule: multi only, no state.
		_, err := s.DuplicateView(view.ID(), view.Placement())
		return err
	}
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	source := s.tabs[index]
	copyTab, err := s.cloneTab(source, "")
	if err != nil {
		return err
	}

	s.tabs = append(s.tabs, nil)
	copy(s.tabs[index+2:], s.tabs[index+1:])
	s.tabs[index+1] = copyTab
	s.active = index + 1
	s.leaveMonitor()
	s.vaultVisible = false
	s.logsVisible = false

	for newID, value := range copyTab.panes {
		// A duplicate opens where its source last was.
		if descriptor := value.sessionDescriptor(); descriptor != nil {
			s.requestPaneOpen(newID, *descriptor, true)
		}
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
	if view := onlyViewInstance(s.tabs[index]); view != nil {
		// Copying to a Window is the duplicate rule with the Window target.
		_, err := s.DuplicateView(view.ID(), domainplugin.PlacementWindow)
		return err
	}
	if s.sessions == nil || s.surfaces == nil {
		return ErrNoSessions
	}
	copyTab, err := s.cloneTab(s.tabs[index], domainplugin.PlacementWindow)
	if err != nil {
		return err
	}
	s.detached = append(s.detached, copyTab)
	for newID, value := range copyTab.panes {
		if descriptor := value.sessionDescriptor(); descriptor != nil {
			s.requestPaneOpen(newID, *descriptor, true)
		}
	}
	if s.onDetach != nil {
		s.onDetach(copyTab.ID)
	}
	s.RequestRedraw()
	return nil
}

// tabViewDuplicate plans one View Pane copy for a duplicated Tab.
type tabViewDuplicate struct {
	paneID model.PaneID
	source *appview.Instance
	target domainplugin.Placement
}

// tabViewDuplicates validates the View duplicate rule for every live View
// Pane in source. A singleton anywhere in the Tab refuses the whole copy; a
// multi-instance View must allow the target that its copy will record.
func (s *State) tabViewDuplicates(source *Tab,
	requested domainplugin.Placement) ([]tabViewDuplicate, error) {
	var plans []tabViewDuplicate
	for _, paneID := range source.paneIDs() {
		value := source.panes[paneID]
		if value == nil {
			continue
		}
		content := value.viewContent()
		if content == nil || content.instance == nil || content.instance.Closed() {
			continue
		}
		instance := content.instance
		if !instance.Contribution().Instances.Multi() {
			return nil, fmt.Errorf("%w: %s", appview.ErrDuplicateSingleton, instance.ViewID())
		}
		target := requested
		if target == "" {
			switch instance.Placement() {
			case domainplugin.PlacementPane, domainplugin.PlacementTab:
				target = instance.Placement()
			default:
				// A View that lives in a work Tab is a Pane or a Tab; any
				// other recorded value is stale and the copy lands as a Pane.
				target = domainplugin.PlacementPane
			}
		}
		if !instance.Contribution().Allows(target) {
			return nil, fmt.Errorf("%w: %s for %s",
				appview.ErrPlacementDenied, target, instance.ViewID())
		}
		plans = append(plans, tabViewDuplicate{paneID: paneID, source: instance, target: target})
	}
	return plans, nil
}

// duplicateTabViews creates the planned View copies and their surfaces. On a
// failure it disposes everything it created, so a refused duplicate leaks no
// instances or surfaces.
func (s *State) duplicateTabViews(plans []tabViewDuplicate) (map[model.PaneID]*pane, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	if s.views == nil || s.viewSurfaces == nil {
		return nil, ErrNoViews
	}
	panes := make(map[model.PaneID]*pane, len(plans))
	cleanup := func() {
		for _, value := range panes {
			if value.surface != nil {
				value.surface.Close()
			}
			if content := value.viewContent(); content != nil && content.instance != nil {
				_ = content.instance.Close()
			}
		}
	}
	for _, plan := range plans {
		copied, err := s.views.Duplicate(plan.source.ID(), plan.target)
		if err != nil {
			cleanup()
			return nil, err
		}
		surface, err := s.viewSurfaces(copied)
		if err != nil {
			_ = copied.Close()
			cleanup()
			return nil, err
		}
		descriptor := copied.Descriptor()
		descriptor.Placement = workspacePlacement(plan.target)
		panes[plan.paneID] = &pane{
			content: &viewContent{instance: copied, descriptor: descriptor},
			surface: surface,
		}
	}
	return panes, nil
}

// cloneTab makes a fresh Tab with new Pane identities, duplicated View
// Instances, and pending Sessions. viewTarget is the placement the copied
// Views record; an empty target keeps the source's work-Tab placement.
func (s *State) cloneTab(source *Tab, viewTarget domainplugin.Placement) (*Tab, error) {
	viewPlans, err := s.tabViewDuplicates(source, viewTarget)
	if err != nil {
		return nil, err
	}
	mapping := make(map[model.PaneID]model.PaneID)
	for _, id := range source.paneIDs() {
		mapping[id] = model.PaneID(newRuntimeID("pane-"))
	}
	root, err := cloneTabTree(source.Root, mapping)
	if err != nil {
		return nil, err
	}
	viewPanes, err := s.duplicateTabViews(viewPlans)
	if err != nil {
		return nil, err
	}

	copyTab := &Tab{
		ID:             model.TabID(newRuntimeID("tab-")),
		Number:         s.nextTabNumber,
		TitleNumber:    source.TitleNumber,
		Label:          source.Label,
		AutoTitle:      source.AutoTitle,
		GeneratedTitle: source.GeneratedTitle,
		Copy:           source.Copy + 1,
		Root:           root,
		panes:          make(map[model.PaneID]*pane, len(mapping)),
	}
	if copyTab.TitleNumber == 0 {
		copyTab.TitleNumber = source.Number
	}
	s.nextTabNumber++
	for oldID, newID := range mapping {
		if value, ok := viewPanes[oldID]; ok {
			value.ID = newID
			copyTab.panes[newID] = value
			continue
		}
		copyTab.panes[newID] = source.panes[oldID].cloneForDuplicate(newID)
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
	if view := onlyViewInstance(s.tabs[index]); view != nil {
		// Detaching a View Tab is a View move, so the registry records the
		// Window placement and the same instance changes container.
		return s.MoveView(view.ID(), domainplugin.PlacementWindow) == nil
	}
	tab := s.tabs[index]
	// A mixed Tab can hold View Panes beside Sessions. Every live View moves
	// to the Window placement atomically with the Tab, so pane menus and the
	// saved descriptor never keep pointing at the old main-window placement.
	if !s.detachTabViews(tab) {
		return false
	}
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
		Title:       s.tabTitle(tab),
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
	return s.paneInfo(tab, paneID)
}

// DetachedTabExists reports whether a detached window still owns the Tab.
func (s *State) DetachedTabExists(id model.TabID) bool {
	return s.detachedTabByID(id) != nil
}

// DetachedTabForSession returns the detached Tab that holds sessionID. The
// browser opens its native page in the window that owns the Session, so a
// detached Pane must find its own window rather than the main one.
func (s *State) DetachedTabForSession(id model.SessionID) (model.TabID, bool) {
	for _, tab := range s.detached {
		for _, value := range tab.panes {
			session := value.session()
			if session != nil && session.ID() == id {
				return tab.ID, true
			}
		}
	}
	return "", false
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
		// Views that were shown in the window return to the work-Tab placement
		// their container now expresses. A contribution that cannot accept it
		// keeps the whole Tab in its window rather than recording a stale
		// placement.
		if !s.reattachTabViews(tab) {
			return false
		}
		s.detached = append(s.detached[:index], s.detached[index+1:]...)
		s.tabs = append(s.tabs, tab)
		s.active = len(s.tabs) - 1
		s.leaveMonitor()
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
	if content := value.viewContent(); content != nil && content.instance != nil {
		// A View Pane detach is the same View move; it keeps the instance and
		// its surface instead of creating a placeholder window.
		return s.MoveView(content.instance.ID(), domainplugin.PlacementWindow) == nil
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
	// A View left alone in the Tab records Tab and takes the canonical
	// view:<instanceId> id; detaching its sibling Pane changed its container.
	s.reconcileTabViewState(tab)

	if descriptor := value.sessionDescriptor(); descriptor != nil &&
		!tab.AutoTitle && !tab.GeneratedTitle &&
		descriptor.CustomName == "" && strings.TrimSpace(tab.Label) != "" {
		// A legacy Tab-level rename has no Pane to carry it; keep the name on
		// the Pane that leaves.
		descriptor.CustomName = tab.Label
	}
	label := ""
	titleNumber := 0
	if descriptor := value.sessionDescriptor(); descriptor != nil {
		label = descriptor.Label
		titleNumber = descriptor.Number
	}
	number := s.nextTabNumber
	s.nextTabNumber++
	detachedRoot := domainworkspace.NewPaneNode(paneID)
	detached := &Tab{
		ID:          model.TabID(newRuntimeID("tab-")),
		Number:      number,
		TitleNumber: titleNumber,
		Label:       label,
		AutoTitle:   true,
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

// CloseMainSessionsPreserving ends the main window's Sessions without touching
// detached windows, keeping resumable ET credentials for the next app run. The
// main window closing is a graceful end of its Sessions: a quit closes the
// windows before OnWillQuit runs, so this is the only point where the
// credentials can still be preserved.
func (s *State) CloseMainSessionsPreserving() {
	for _, tab := range s.tabs {
		s.closeTab(tab, true)
	}
}

// CloseAllSessions ends the Sessions of every window. The app calls it on
// quit, after the windows are already gone; main-window Tabs are part of the
// persisted Workspace, so their resumable credentials are kept for the next
// run. Detached Tabs are not persisted, so keeping their credentials would
// only orphan them in the secret store.
func (s *State) CloseAllSessions() {
	s.CloseMainSessionsPreserving()
	for _, tab := range s.detached {
		s.closeTab(tab, false)
	}
	s.detached = nil
}

// CloseDetachedTab closes a Tab that left the main window, ending its
// Sessions. It is also the idempotent close hook for a detached window. A
// detached Tab is never restored, so this path clears resumable credentials.
func (s *State) CloseDetachedTab(id model.TabID) bool {
	for index, tab := range s.detached {
		if tab.ID != id {
			continue
		}
		s.closeTab(tab, false)
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
			session := value.session()
			if session != nil && session.Snapshot().Status == appsession.StatusRunning {
				count++
			}
		}
	}
	return count
}

// paneInfo describes one Pane for either a main or a detached Tab.
func (s *State) paneInfo(tab *Tab, id model.PaneID) (PaneInfo, bool) {
	value, ok := tab.panes[id]
	if !ok || !domainworkspace.ContainsPane(tab.Root, id) {
		return PaneInfo{}, false
	}
	focused, focusedID := tab.focusedPane()
	info := PaneInfo{
		ID:      id,
		Kind:    value.contentKind(),
		Focused: id == focusedID && focused != nil,
		Pending: value.pending,
		Err:     value.err,
		Surface: value.surface,
	}
	if descriptor := value.sessionDescriptor(); descriptor != nil {
		info.Number = descriptor.Number
		info.Label = s.connectionLabel(value)
		info.Title = s.paneTitle(value)
	} else if content := value.viewContent(); content != nil && content.instance != nil {
		info.Label = content.instance.Title()
		info.ViewInstanceID = content.instance.ID()
		info.ViewPlacement = content.instance.Placement()
		contribution := content.instance.Contribution()
		info.ViewPlacements = contribution.Placements
		info.ViewMulti = contribution.Instances.Multi()
	} else {
		// An unavailable placeholder has no title of its own; the Tab label
		// keeps the Pane from rendering as a zero-numbered Session.
		info.Label = tab.Label
	}
	return info, true
}

// titleMode returns the live Dynamic tab title setting.
func (s *State) titleMode() domainterminal.DynamicTabTitleMode {
	if s.dynamicTabTitleMode == nil {
		return domainterminal.DynamicTabTitleAll
	}
	return domainterminal.NormalizeDynamicTabTitleMode(s.dynamicTabTitleMode())
}

// hostLabelFor returns the current label of a Pane's Host. A saved Host is looked
// up live, so a Vault rename reaches its open Tabs; the descriptor's snapshot
// is the fallback when the Host is gone or the lookup is not wired.
func (s *State) hostLabelFor(value *pane) string {
	descriptor := value.sessionDescriptor()
	if descriptor == nil {
		return ""
	}
	if descriptor.HostID != "" && s.hostLabel != nil {
		if label, ok := s.hostLabel(descriptor.HostID); ok && strings.TrimSpace(label) != "" {
			return label
		}
	}
	return descriptor.Label
}

// connectionLabel is a Pane's static name: the user's rename, else its current
// Host label.
func (s *State) connectionLabel(value *pane) string {
	if value == nil {
		return ""
	}
	if content := value.viewContent(); content != nil && content.instance != nil {
		return content.instance.Title()
	}
	descriptor := value.sessionDescriptor()
	if descriptor == nil {
		return ""
	}
	return domainsessiontitle.ConnectionLabel(descriptor.CustomName, s.hostLabelFor(value))
}

// paneTitle resolves one Pane's header title from its connection label and the
// live Session's OSC 0/2 title.
func (s *State) paneTitle(value *pane) string {
	if value == nil {
		return ""
	}
	if content := value.viewContent(); content != nil && content.instance != nil {
		return content.instance.Title()
	}
	content := value.sessionContent()
	if content == nil {
		return ""
	}
	var dynamic string
	if content.session != nil {
		dynamic = content.session.Snapshot().Title
	}
	if content.session != content.titleSession || dynamic != content.titleSeen {
		content.agent = domainsessiontitle.AgentSession(content.agent, dynamic)
		content.titleSeen = dynamic
		content.titleSession = content.session
	}
	return domainsessiontitle.Resolve(domainsessiontitle.Input{
		CustomName:   content.descriptor.CustomName,
		HostLabel:    s.hostLabelFor(value),
		DynamicTitle: dynamic,
		Agent:        content.agent,
	}, s.titleMode())
}

// tabTitle resolves the title a Tab shows, before the view adds the numbered
// local fallback. An auto-titled single-Pane Tab follows
// its Session. A split Tab names itself after the focused Pane's connection
// label and counts the other distinct connections, as the original's workspace
// Tabs do: dynamic titles can differ per Pane on one Host, so they are not used
// for the aggregate.
func (s *State) tabTitle(tab *Tab) string {
	if tab == nil {
		return ""
	}
	if tab.GeneratedTitle {
		return s.composedTitle(tab)
	}
	if !tab.AutoTitle {
		return tab.Label
	}
	ids := tab.paneIDs()
	switch len(ids) {
	case 0:
		return tab.Label
	case 1:
		return s.paneTitle(tab.panes[ids[0]])
	}
	value, _ := tab.focusedPane()
	primary := s.connectionLabel(value)
	if primary == "" {
		return tab.Label
	}
	keys := make(map[string]bool, len(ids))
	for _, id := range ids {
		keys[s.connectionKey(tab.panes[id])] = true
	}
	if len(keys) > 1 {
		return fmt.Sprintf("%s +%d", primary, len(keys)-1)
	}
	return primary
}

// composedTitle joins the distinct connection labels of a merged Tab's Panes
// in tree order, as the original's generated workspace titles do
// ("web-01/db-02"). Identical labels collapse, so two Panes on one Host do not
// read "web-01/web-01". An empty result keeps the last stored label.
func (s *State) composedTitle(tab *Tab) string {
	labels := make([]string, 0, len(tab.panes))
	for _, id := range tab.paneIDs() {
		label := strings.TrimSpace(s.connectionLabel(tab.panes[id]))
		if label == "" || containsLabel(labels, label) {
			continue
		}
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return tab.Label
	}
	return strings.Join(labels, "/")
}

func containsLabel(labels []string, label string) bool {
	for _, candidate := range labels {
		if candidate == label {
			return true
		}
	}
	return false
}

// connectionKey identifies the connection a Pane belongs to. Panes on one Host
// share a key even after a rename (the Host ID does not change), local shells
// share one, and serial Panes are distinct per device.
func (s *State) connectionKey(value *pane) string {
	if value == nil {
		return ""
	}
	if content := value.viewContent(); content != nil && content.instance != nil {
		return "view:" + content.instance.ID()
	}
	descriptor := value.sessionDescriptor()
	if descriptor == nil {
		return ""
	}
	if descriptor.HostID != "" {
		return "host:" + string(descriptor.HostID)
	}
	switch descriptor.Kind {
	case domainworkspace.SessionLocal:
		return "local"
	case domainworkspace.SessionSerial:
		if descriptor.Serial != nil {
			return "serial:" + descriptor.Serial.Path
		}
		return "serial"
	default:
		return "pane:" + descriptor.Label + "\x00" + descriptor.Hostname
	}
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
	if content := value.sessionContent(); content == nil {
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
	descriptor := withFreshResumeKey(value.sessionContent().descriptor)
	// A new shell is not the Session the user renamed.
	descriptor.CustomName = ""
	newPane := newSessionPane(newPaneID, descriptor)
	newPane.pending = true
	tab.panes[newPaneID] = newPane
	tab.setFocus(newPaneID)
	// The new Pane inherits the source Session's last known directory, as the
	// original split did when it copied the shell's current cwd.
	s.requestPaneOpen(newPaneID, descriptor, true)
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
	s.closePane(value, false)
	delete(tab.panes, focusedID)
	tab.Root = root
	if next == "" {
		return s.CloseTab(index)
	}
	// Closing a sibling Pane can leave a View alone in the Tab, which is the
	// Tab placement rather than the split's Pane placement, and it can leave a
	// Session-only Tab behind after a View was closed, which cannot keep the
	// view: namespace.
	s.reconcileTabViewState(tab)
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

// viewPlacementTarget is one View Instance's placement after a pending Pane
// move.
type viewPlacementTarget struct {
	instance *appview.Instance
	target   domainplugin.Placement
}

// paneViewPlacements plans the placement every View a cross-Tab Pane move
// affects records. sourcePaneID leaves source and lands in target, so both
// Tabs are described in their post-move shape: a View left alone records Tab,
// a View joining or staying in a split records Pane. The plan is rejected when
// a View cannot live in its resulting container, which keeps the move atomic.
func paneViewPlacements(source *Tab, sourcePaneID model.PaneID,
	target *Tab, value *pane) ([]viewPlacementTarget, bool) {
	sourceSingle := len(source.paneIDs())-1 == 1
	var plan []viewPlacementTarget
	appendInstance := func(instance *appview.Instance, single bool) bool {
		targetPlacement, ok := workTabPlacement(instance, single)
		if !ok {
			return false
		}
		plan = append(plan, viewPlacementTarget{instance: instance, target: targetPlacement})
		return true
	}
	for _, paneID := range source.paneIDs() {
		if paneID == sourcePaneID {
			continue
		}
		if instance := liveViewInstance(source.panes[paneID]); instance != nil {
			if !appendInstance(instance, sourceSingle) {
				return nil, false
			}
		}
	}
	// The target grows by one Pane, so it is always a split after the move.
	for _, paneID := range target.paneIDs() {
		if instance := liveViewInstance(target.panes[paneID]); instance != nil {
			if !appendInstance(instance, false) {
				return nil, false
			}
		}
	}
	if moved := liveViewInstance(value); moved != nil {
		if !appendInstance(moved, false) {
			return nil, false
		}
	}
	return plan, true
}

// applyPaneViewPlacements records the planned placements in the registry. A
// failed move rolls every already-applied one back, so the caller can leave
// the layout untouched.
func (s *State) applyPaneViewPlacements(plan []viewPlacementTarget) bool {
	if len(plan) == 0 {
		return true
	}
	if s.views == nil {
		return false
	}
	type appliedMove struct {
		instance *appview.Instance
		previous domainplugin.Placement
	}
	applied := make([]appliedMove, 0, len(plan))
	for _, move := range plan {
		previous := move.instance.Placement()
		if previous == move.target {
			continue
		}
		if _, err := s.views.Move(move.instance.ID(), move.target); err != nil {
			for index := len(applied) - 1; index >= 0; index-- {
				_, _ = s.views.Move(applied[index].instance.ID(), applied[index].previous)
			}
			return false
		}
		applied = append(applied, appliedMove{instance: move.instance, previous: previous})
	}
	return true
}

// recordPaneViewPlacements updates the restore descriptor of every planned
// View in the affected Tabs, so a saved Pane agrees with the registry.
func recordPaneViewPlacements(tabs []*Tab, plan []viewPlacementTarget) {
	for _, tab := range tabs {
		for _, move := range plan {
			setTabViewPlacement(tab, move.instance.ID(), move.target)
		}
	}
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

	sourcePanes := len(source.paneIDs())
	targetPanes := len(target.paneIDs())
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
	// A drag between a View Tab and a split changes the container each View
	// lives in. Resolve and record those placements before touching the layout,
	// so a contribution that cannot accept the destination refuses the whole
	// move and the registry never disagrees with the shell.
	plan, ok := paneViewPlacements(source, sourcePaneID, target, value)
	if !ok || !s.applyPaneViewPlacements(plan) {
		return false
	}

	delete(source.panes, sourcePaneID)
	target.panes[sourcePaneID] = value
	target.Root = targetRoot
	target.setFocus(sourcePaneID)
	if sourcePanes == 1 && targetPanes == 1 {
		// Dragging one single-Pane Tab onto another merges them into a Tab
		// named after both connections in tree order, as the original's
		// merged-workspace title does. Composed titles keep following pane
		// renames and membership.
		if title := s.composedTitle(target); title != "" {
			target.Label = title
			target.AutoTitle = false
			target.GeneratedTitle = true
		}
	}

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
		// A source Tab named after the View that just left takes the identity
		// of the View it still shows (or drops the view: namespace when only
		// Sessions remain) instead of colliding with the new container the
		// moved instance gets.
		s.reconcileTabViewState(source)
	}
	recordPaneViewPlacements([]*Tab{source, target}, plan)
	s.leaveMonitor()
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
			ID:             tab.ID,
			Number:         tab.Number,
			TitleNumber:    tab.TitleNumber,
			Title:          tab.Label,
			AutoTitle:      tab.AutoTitle,
			GeneratedTitle: tab.GeneratedTitle,
			Copy:           tab.Copy,
			Root:           domainworkspace.Clone(tab.Root),
			FocusPath:      append([]int(nil), tab.FocusPath...),
		})
		for _, id := range tab.paneIDs() {
			value := tab.panes[id]
			if value == nil {
				continue
			}
			payload.Panes = append(payload.Panes, domainworkspace.RestoredPane{
				ID:             id,
				PaneDescriptor: value.restoreDescriptor(),
			})
		}
	}
	payload.Views = s.placementViews()
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
			descriptor := value.sessionDescriptor()
			if descriptor == nil {
				continue
			}
			reporter, ok := value.surface.(interface{ Dir() string })
			if !ok {
				continue
			}
			dir := reporter.Dir()
			if dir == "" || descriptor.Dir == dir {
				continue
			}
			descriptor.Dir = dir
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

// uniqueTabID returns want when it is unused, otherwise the first free
// deterministic "want#n" suffix. An empty want gets a fresh runtime id; only
// collision resolution has to be stable for a given payload.
func uniqueTabID(want model.TabID, used map[model.TabID]bool) model.TabID {
	if want == "" {
		for {
			candidate := model.TabID(newRuntimeID("tab-"))
			if !used[candidate] {
				return candidate
			}
		}
	}
	if !used[want] {
		return want
	}
	for suffix := 2; ; suffix++ {
		candidate := model.TabID(fmt.Sprintf("%s#%d", want, suffix))
		if !used[candidate] {
			return candidate
		}
	}
}

// uniqueRuntimeTabID resolves want against every Tab in the main window and in
// detached windows. exclude is the Tab being renamed, whose current id must not
// count as taken; pass nil when creating a new Tab.
func (s *State) uniqueRuntimeTabID(want model.TabID, exclude *Tab) model.TabID {
	used := make(map[model.TabID]bool, len(s.tabs)+len(s.detached))
	for _, tab := range s.tabs {
		if tab != exclude {
			used[tab.ID] = true
		}
	}
	for _, tab := range s.detached {
		if tab != exclude {
			used[tab.ID] = true
		}
	}
	return uniqueTabID(want, used)
}

// uniqueRestoredTabID resolves want against the Tabs already restored into
// the main window and every detached window.
func (s *State) uniqueRestoredTabID(want model.TabID) model.TabID {
	return s.uniqueRuntimeTabID(want, nil)
}

// assignRestoredTabIDs gives every restored Tab a truthful, unique identity. A
// Tab showing exactly one live View is renamed after that instance, replacing
// the stale stored id, whether or not Sessions share the Tab. The canonical
// View ids are reserved first so a stored id on some other Tab cannot steal
// one. Other Tabs keep a stored id only when it is truthful: a mixed Tab whose
// stored view: id names none of its Views takes the first one's id, and a
// Session-only Tab drops the view: namespace entirely. Later collisions get a
// deterministic suffix.
func assignRestoredTabIDs(tabs []*Tab) {
	used := make(map[model.TabID]bool, len(tabs))
	ids := make(map[*Tab]model.TabID, len(tabs))
	for _, tab := range tabs {
		if instances := tabViewInstances(tab); len(instances) == 1 {
			id := uniqueTabID(viewTabID(instances[0]), used)
			used[id] = true
			ids[tab] = id
		}
	}
	for _, tab := range tabs {
		if _, ok := ids[tab]; ok {
			continue
		}
		want := tab.ID
		instances := tabViewInstances(tab)
		if len(instances) > 1 {
			// A mixed Tab may keep an id that still names one of its Views; a
			// view: id left by the previous run names none of them and would
			// mislead a later move.
			if strings.HasPrefix(string(want), "view:") && !tabIDNamesView(want, instances) {
				want = viewTabID(instances[0])
			}
		} else if strings.HasPrefix(string(want), "view:") {
			// A Tab with no live View cannot keep the view: namespace.
			want = ""
		}
		id := uniqueTabID(want, used)
		used[id] = true
		ids[tab] = id
	}
	for tab, id := range ids {
		tab.ID = id
	}
}

// Restore applies the stored Workspace layout. It is a no-op when persistence
// is not configured, the payload is empty, or Tabs already exist. Panes open
// asynchronously and appear when Flush applies their outcomes; output and
// scrollback are not restored.
func (s *State) Restore(ctx context.Context) {
	if s.workspace == nil || len(s.tabs) > 0 {
		return
	}
	// A View-only Workspace restores without a terminal Session service.
	if (s.sessions == nil || s.surfaces == nil) &&
		(s.views == nil || s.viewSurfaces == nil) {
		return
	}
	payload := s.workspace.Load(ctx)
	if len(payload.Tabs) == 0 && len(payload.Views) == 0 {
		return
	}
	descriptors := make(map[model.PaneID]domainworkspace.PaneDescriptor, len(payload.Panes))
	for _, value := range payload.Panes {
		descriptors[value.ID] = value.PaneDescriptor
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
			ID:             restored.ID,
			Number:         number,
			TitleNumber:    restored.TitleNumber,
			Label:          restored.Title,
			AutoTitle:      restored.AutoTitle,
			GeneratedTitle: restored.GeneratedTitle,
			Copy:           restored.Copy,
			Root:           root,
			panes:          make(map[model.PaneID]*pane),
		}
		if tab.TitleNumber == 0 {
			tab.TitleNumber = number
		}
		for _, id := range domainworkspace.CollectPaneIDs(root) {
			descriptor, ok := descriptors[id]
			if !ok {
				// The sanitizer normally drops a leaf without content, but a
				// direct restore must still start: keep the leaf as a
				// placeholder rather than panicking or losing the layout.
				descriptor = domainworkspace.PaneDescriptor{}
			}
			if descriptor.Normalized().Kind == domainworkspace.PaneKindView {
				tab.panes[id] = s.restoreViewPane(id, descriptor)
				continue
			}
			session, ok := descriptor.SessionContent()
			if !ok || !session.Valid() {
				stored := descriptor
				tab.panes[id] = &pane{
					ID:          id,
					unavailable: &stored,
					err:         ErrUnavailablePane,
				}
				continue
			}
			if session.Kind == domainworkspace.SessionLocal {
				session.Dir = validLocalDir(session.Dir)
			}
			if session.Kind == domainworkspace.SessionPlugin && !s.providerClaimed(session.Protocol) {
				// The provider is missing or disabled. Keep the Pane as an
				// unavailable placeholder and open nothing: no other transport
				// may stand in for the provider.
				stored := descriptor
				tab.panes[id] = &pane{
					ID:          id,
					unavailable: &stored,
					err:         ErrUnavailablePane,
				}
				continue
			}
			if session.Kind == domainworkspace.SessionET && session.ResumeKey == "" {
				// A workspace written before ET resume keys existed still gets a
				// locator, so this run's session becomes resumable too.
				session.ResumeKey = newETResumeKey()
			}
			if session.Label == "" {
				session.Label = restored.Title
			}
			if session.Kind == domainworkspace.SessionLocal &&
				strings.TrimSpace(session.Label) == "" {
				// Local Sessions always have the original's fixed name, including
				// ones restored from a payload written before Tabs carried it.
				session.Label = domainsessiontitle.LocalTerminalLabel
			}
			value := newSessionPane(id, session)
			value.pending = true
			tab.panes[id] = value
		}
		tab.FocusPath = domainworkspace.SanitizeFocusPath(root, restored.FocusPath)
		if ids := tab.paneIDs(); len(ids) == 1 && tab.Label == "" {
			if content := tab.panes[ids[0]].viewContent(); content != nil {
				tab.Label = viewTitle(content.instance)
			}
		}
		s.tabs = append(s.tabs, tab)
	}
	// A View Tab must be named after the instance this run opened, not the id
	// stored by the previous run. Resolve every id before RestorePlacements so
	// a restored window callback never sees an id that later changes.
	assignRestoredTabIDs(s.tabs)
	s.RestorePlacements(payload.Views)
	if len(s.tabs) == 0 {
		// A Workspace may hold only Pages, Windows, and Asides.
		s.active = -1
		s.RequestRedraw()
		return
	}
	s.active = payload.ActiveTab
	if s.active < 0 || s.active >= len(s.tabs) {
		s.active = 0
	}
	s.leaveMonitor()
	s.vaultVisible = false
	s.logsVisible = false
	for _, tab := range s.tabs {
		for id, value := range tab.panes {
			if descriptor := value.sessionDescriptor(); descriptor != nil {
				s.requestPaneOpen(id, *descriptor, true)
			}
		}
	}
	s.RequestRedraw()
}

func (s *State) requestPaneOpen(paneID model.PaneID,
	descriptor domainworkspace.SessionDescriptor, restore bool) {
	if s.sessions == nil {
		return
	}
	ctx := s.ctx
	go func() {
		session, err := s.openDescriptor(ctx, descriptor)
		s.mu.Lock()
		s.paneOutcomes = append(s.paneOutcomes, paneOutcome{
			paneID: paneID, session: session, restore: restore, err: err,
		})
		s.mu.Unlock()
		s.RequestRedraw()
	}()
}

// RetryPane reopens a failed Pane's Session from the Pane's own descriptor. It
// reports false when the Pane does not exist, is still opening, is already
// live, or the shell has no Session service. The retry keeps restore semantics
// so an SSH Pane replays its last known working directory.
func (s *State) RetryPane(tabID model.TabID, paneID model.PaneID) bool {
	if s.sessions == nil || s.surfaces == nil {
		return false
	}
	tab, _ := s.tabByAnyID(tabID)
	if tab == nil {
		return false
	}
	value, ok := tab.panes[paneID]
	if !ok || !domainworkspace.ContainsPane(tab.Root, paneID) {
		return false
	}
	// Only a failed Session Pane retries: a pending or live one already owns a
	// Session or a dial, and a View or unknown placeholder has nothing to retry.
	content := value.sessionContent()
	if content == nil || value.err == nil || value.pending || value.surface != nil {
		return false
	}
	if content.session != nil {
		s.closePane(value, false)
		content.session = nil
	}
	value.pending = true
	value.err = nil
	s.requestPaneOpen(paneID, content.descriptor, true)
	s.RequestRedraw()
	return true
}

func (s *State) applyPaneOutcome(outcome paneOutcome) {
	tab, _ := s.tabByPaneID(outcome.paneID)
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
	content := value.sessionContent()
	if content == nil {
		// The Pane no longer holds Session content; do not leak the Session its
		// earlier open produced.
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
	content.session = outcome.session
	value.surface = surface
	value.pending = false
	value.err = nil
	if outcome.restore {
		restoreRemoteDirectory(outcome.session, content.descriptor)
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
		Label:          descriptor.Label,
		Dir:            validLocalDir(descriptor.Dir),
		X11Forwarding:  descriptor.X11Forwarding,
		AutoAttachTmux: descriptor.AutoAttachTmux,
		ResumeKey:      descriptor.ResumeKey,
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
			spec.NetworkDevice = host.IsNetworkDevice()
			spec.AutoAttachTmux = host.AutoAttachTmuxEnabled()
			spec.Dir = ""
		}
		spec.Reconnect = &appsession.ReconnectPolicy{}
	case domainworkspace.SessionMosh, domainworkspace.SessionET:
		spec.Kind = appsession.KindMosh
		if descriptor.Kind == domainworkspace.SessionET {
			spec.Kind = appsession.KindET
		}
		spec.HostID = descriptor.HostID
		spec.Hostname = descriptor.Hostname
		spec.Port = descriptor.Port
		spec.Username = descriptor.Username
		spec.X11Forwarding = false
		// tmux never applies to Mosh or ET, even if a restored descriptor from
		// an older record carries the flag.
		spec.AutoAttachTmux = false
		if s.resolveHost != nil {
			host, ok := s.resolveHost(ctx, descriptor.HostID)
			if !ok {
				return nil, ErrHostUnavailable
			}
			plan, err := hostSessionTransport(host)
			if err != nil {
				return nil, err
			}
			spec.Protocol = string(host.Protocol)
			spec.HostID = host.ID
			spec.Hostname = host.Hostname
			spec.Port = host.Port
			spec.Username = host.Username
			spec.Mosh = plan.mosh
			spec.ET = plan.et
			spec.Dir = ""
		} else if spec.Kind == appsession.KindMosh {
			cfg := moshmodel.Config{
				Hostname: descriptor.Hostname,
				Port:     descriptor.Port,
				Username: descriptor.Username,
			}.Normalized()
			spec.Mosh = &cfg
		} else {
			cfg := etmodel.Config{
				Hostname: descriptor.Hostname,
				Port:     descriptor.Port,
				Username: descriptor.Username,
			}.Normalized()
			spec.ET = &cfg
		}
		spec.Reconnect = &appsession.ReconnectPolicy{}
	case domainworkspace.SessionTelnet:
		spec.Kind = appsession.KindTelnet
		spec.HostID = descriptor.HostID
		spec.Hostname = descriptor.Hostname
		spec.Port = descriptor.Port
		spec.Username = descriptor.Username
		spec.X11Forwarding = false
		if s.resolveHost != nil {
			host, ok := s.resolveHost(ctx, descriptor.HostID)
			if !ok {
				return nil, ErrHostUnavailable
			}
			spec.HostID = host.ID
			spec.Hostname = host.Hostname
			spec.Port = host.Port
			spec.Username = host.Username
		}
		telnetConfig := domaintelnet.Config{Hostname: spec.Hostname, Port: spec.Port}.Normalized()
		spec.Telnet = &telnetConfig
		// Telnet keeps the manual Reconnect action but does not retry a
		// dropped socket automatically.
		spec.Reconnect = telnetReconnectPolicy()
	case domainworkspace.SessionPlugin:
		// A provider-backed Pane is reopened only through its own protocol.
		// A missing provider fails closed here; the Session factory never
		// falls back to a built-in transport.
		if !s.providerClaimed(descriptor.Protocol) {
			return nil, fmt.Errorf("%w: %q", appsession.ErrProviderUnavailable, descriptor.Protocol)
		}
		spec.Kind = appsession.KindPlugin
		spec.Protocol = descriptor.Protocol
		spec.Provider = descriptor.Provider
		spec.Restore = append([]byte(nil), descriptor.Restore...)
		spec.HostID = descriptor.HostID
		spec.Hostname = descriptor.Hostname
		spec.Port = descriptor.Port
		spec.Username = descriptor.Username
		spec.X11Forwarding = false
		spec.AutoAttachTmux = false
		if s.resolveHost != nil {
			if host, ok := s.resolveHost(ctx, descriptor.HostID); ok {
				spec.HostID = host.ID
				spec.Hostname = host.Hostname
				spec.Port = host.Port
				spec.Username = host.Username
			}
		}
	case domainworkspace.SessionSerial:
		if descriptor.Serial == nil {
			return nil, fmt.Errorf("%w: %q", ErrUnknownSession, descriptor.Kind)
		}
		spec.Kind = appsession.KindSerial
		serialConfig := descriptor.Serial.Normalized()
		spec.Serial = &serialConfig
		spec.Label = descriptor.Label
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownSession, descriptor.Kind)
	}
	return s.openETWithLegacyFallback(ctx, spec, descriptor)
}

// openETWithLegacyFallback opens the Session and handles one stale-secret
// outcome: a restored ET Pane whose saved pair a pre-v7 etserver cannot
// reattach. Persistence is capability-aware, so a pair saved by this build is
// never written for such a server; a pair written by an older build (or by a
// newer build before the server was downgraded) would otherwise make the Pane
// fail permanently and reuse the same pair on every Retry. The shell clears the
// pair and explicitly starts a fresh session, with a visible notice that the
// previous shell could not be restored. It never falls back silently.
func (s *State) openETWithLegacyFallback(ctx context.Context,
	spec appsession.StartSpec, descriptor domainworkspace.SessionDescriptor) (*appsession.Session, error) {
	session, err := s.sessions.Open(ctx, spec)
	if err == nil || !errors.Is(err, etmodel.ErrReattachUnsupported) ||
		descriptor.Kind != domainworkspace.SessionET || descriptor.ResumeKey == "" {
		return session, err
	}
	if s.etResumeStore == nil {
		// Without a store the stale pair cannot be cleared, so retrying would
		// only repeat the same permanent failure. Surface stock's refusal.
		return nil, err
	}
	if clearErr := s.discardETResume(descriptor.ResumeKey); clearErr != nil {
		return nil, fmt.Errorf("%w (the stale ET resume credential could not be cleared: %w)", err, clearErr)
	}
	session, retryErr := s.sessions.Open(ctx, spec)
	if retryErr != nil {
		return nil, retryErr
	}
	session.PublishNotice(appsession.Notice{Code: appsession.NoticeETResumeFallback})
	return session, nil
}

// discardETResume removes one saved ET pair when no live Session owns it.
func (s *State) discardETResume(resumeKey string) error {
	if s.etResumeStore == nil || resumeKey == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), etResumeCleanupTimeout)
	defer cancel()
	return s.etResumeStore.Delete(ctx, resumeKey)
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

// closeDetachedSessions removes Panes whose tmux client detached. The remote
// session keeps running, so the local Session ended by design: its Tab or Pane
// closes without an error and the next Host open attaches again. Only a clean
// tmux detach carries EndReasonDetached, so an ordinary shell exit keeps its
// ended Tab.
func (s *State) closeDetachedSessions() bool {
	changed := false
	for index := len(s.tabs) - 1; index >= 0; index-- {
		tab := s.tabs[index]
		if tab == nil {
			continue
		}
		for _, id := range tab.paneIDs() {
			value := tab.panes[id]
			session := value.session()
			if session == nil {
				continue
			}
			snapshot := session.Snapshot()
			if snapshot.Status != appsession.StatusExited ||
				snapshot.EndReason != appsession.EndReasonDetached {
				continue
			}
			if len(tab.paneIDs()) == 1 {
				s.CloseTab(index)
				changed = true
				break
			}
			// Focus survives unless the detached Pane itself had it; a
			// background detach must not move focus in a split Tab.
			_, focusedID := tab.focusedPane()
			root, err := domainworkspace.RemovePane(tab.Root, id)
			if err != nil {
				continue
			}
			s.closePane(value, false)
			delete(tab.panes, id)
			tab.Root = root
			if next := domainworkspace.FocusAfterRemove(root, focusedID, id); next != "" {
				tab.setFocus(next)
			}
			// The removed Pane can be the only Session beside a View, leaving
			// that View alone in a work Tab: it records Tab and takes the
			// canonical id, exactly as an explicit sibling close would.
			s.reconcileTabViewState(tab)
			changed = true
		}
	}
	if changed {
		s.refreshBroadcastState()
		s.save()
		s.RequestRedraw()
	}
	return changed
}

func (s *State) closeTab(tab *Tab, preserve bool) {
	for _, value := range tab.panes {
		s.closePane(value, preserve)
	}
}

func (s *State) closePane(value *pane, preserve bool) {
	if value == nil {
		return
	}
	if value.surface != nil {
		value.surface.Close()
	}
	if content := value.viewContent(); content != nil {
		// Closing the last Pane showing a View disposes its instance. The
		// surface has already released the plugin's node and listener handles.
		if content.instance != nil {
			_ = content.instance.Close()
		}
		return
	}
	content := value.sessionContent()
	if content == nil {
		// An unavailable placeholder owns no Session resources.
		return
	}
	if content.session != nil {
		if preserve {
			_ = content.session.ClosePreservingResume()
		} else {
			_ = content.session.Close()
		}
		return
	}
	// The Session never opened, so no transport can clear its saved pair. An
	// explicit close still must not leave the secret behind; a preserve close
	// (app quit) keeps it because the Pane is restored next run.
	if !preserve && content.descriptor.Kind == domainworkspace.SessionET {
		_ = s.discardETResume(content.descriptor.ResumeKey)
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
		s.leaveMonitor()
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
	s.leaveMonitor()
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

// SetTerminalCursor applies a cursor shape and blink preference to every open
// terminal in the main and detached windows. A Surface that cannot change live
// is left alone; a new Session reads the setting when its terminal is created.
func (s *State) SetTerminalCursor(style domainpreferences.CursorStyle, blink bool) {
	apply := func(tab *Tab) {
		if tab == nil {
			return
		}
		for _, pane := range tab.panes {
			if pane == nil {
				continue
			}
			setter, ok := pane.surface.(TerminalCursorSetter)
			if !ok {
				continue
			}
			setter.SetTerminalCursor(style, blink)
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
	s.terminalTheme, s.hasTerminalTheme = preset, true
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
