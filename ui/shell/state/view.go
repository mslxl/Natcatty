package state

import (
	"errors"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	appview "github.com/mslxl/natcatty/application/view"
	"github.com/mslxl/natcatty/domain/model"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// pageEntry is one open View Page. A resolved page owns a live instance and
// its surface; an unavailable page keeps the restored descriptor so the
// surface can explain what is missing without blocking the Workspace.
type pageEntry struct {
	instance    *appview.Instance
	surface     Surface
	descriptor  domainworkspace.ViewPaneDescriptor
	unavailable *domainworkspace.RestoredView
	err         error
}

// asideEntry is one View Instance placed in a side slot.
type asideEntry struct {
	instance    *appview.Instance
	surface     Surface
	descriptor  domainworkspace.ViewPaneDescriptor
	unavailable *domainworkspace.RestoredView
	err         error
}

// PageViewInfo describes one open View Page for the shell.
type PageViewInfo struct {
	ViewID     string
	ContextKey string
	Title      string
	Surface    Surface
	Err        error
	Visible    bool
}

// AsideViewInfo describes one View Instance placed in a side slot.
type AsideViewInfo struct {
	InstanceID string
	ViewID     string
	ContextKey string
	Title      string
	Surface    Surface
	Err        error
}

// HomePageInfo is one contribution-driven Home page. Built-in Vault pages stay
// in the HomePage enum; a plugin page is listed from its View Contribution.
type HomePageInfo struct {
	ViewID string
	Title  string
}

// placedView is a View Instance detached from its current container. remove
// performs the container-specific removal after every validation has passed,
// which keeps a rejected move atomic.
type placedView struct {
	instance *appview.Instance
	surface  Surface
	remove   func()
}

func (p *placedView) descriptor() domainworkspace.ViewPaneDescriptor {
	if p == nil || p.instance == nil {
		return domainworkspace.ViewPaneDescriptor{}
	}
	return p.instance.Descriptor()
}

func viewTitle(instance *appview.Instance) string {
	if instance == nil {
		return ""
	}
	title := strings.TrimSpace(instance.Title())
	if title == "" {
		title = instance.Contribution().LocalID
	}
	return title
}

func workspacePlacement(placement domainplugin.Placement) domainworkspace.ViewPlacement {
	return domainworkspace.ViewPlacement(placement)
}

func pluginPlacement(placement domainworkspace.ViewPlacement) domainplugin.Placement {
	return domainplugin.Placement(placement)
}

// OpenView opens a View Instance in the requested placement. Only a Page
// invocation can move an existing singleton: opening the same page view again
// focuses it, and opening it from another placement relocates that same
// instance instead of creating a duplicate. Other placements return their
// work Tab id; Page, Window, and Aside return the container's identity.
func (s *State) OpenView(request appview.OpenRequest) (model.TabID, error) {
	if s == nil || s.views == nil || s.viewSurfaces == nil {
		return "", ErrNoViews
	}
	if request.Placement == "" {
		request.Placement = domainplugin.PlacementPane
	}
	// Open moves an existing singleton into the Page slot. Remember where it
	// was so the shell can undo that when it cannot relocate the surface.
	var previous domainplugin.Placement
	if request.Placement == domainplugin.PlacementPage {
		if existing, ok := s.views.SingletonForContext(request.ViewID, request.ContextKey); ok {
			previous = existing.Placement()
		}
	}
	instance, created, err := s.views.Open(request)
	if err != nil {
		// The page slot belongs to another context of this view. The move is
		// rejected, but the existing page is focused so the user sees it.
		if errors.Is(err, appview.ErrPageOccupied) {
			if page, ok := s.views.PageInstance(request.ViewID); ok {
				s.focusExistingView(page)
			}
		}
		return "", err
	}
	if !created {
		if request.Placement == domainplugin.PlacementPage &&
			s.pageShowing(instance) == false {
			return "", s.moveExistingToPage(instance, previous)
		}
		if tabID, ok := s.focusExistingView(instance); ok {
			return tabID, nil
		}
		return "", nil
	}
	surface, err := s.viewSurfaces(instance)
	if err != nil {
		_ = instance.Close()
		return "", err
	}
	return s.placeView(&placedView{instance: instance, surface: surface}, request.Placement), nil
}

// pageShowing reports whether the instance already owns the visible page slot.
func (s *State) pageShowing(instance *appview.Instance) bool {
	if instance == nil {
		return false
	}
	entry := s.pages[instance.ViewID()]
	return entry != nil && entry.instance == instance &&
		s.pageShown == instance.ViewID()
}

// moveExistingToPage relocates an already-open singleton that Open has just
// recorded in the Page slot. The registry accepted the move before the shell
// touches the source; a relocation failure rolls the placement back so a
// rejected move never leaves the registry and the shell disagreeing.
func (s *State) moveExistingToPage(instance *appview.Instance,
	previous domainplugin.Placement) error {
	if instance == nil {
		return ErrViewNotPlaced
	}
	if err := s.relocateExistingView(instance); err != nil {
		if previous != "" && previous != domainplugin.PlacementPage &&
			instance.Placement() == domainplugin.PlacementPage {
			_, _ = s.views.Move(instance.ID(), previous)
		}
		return err
	}
	return nil
}

// relocateExistingView removes the instance from whatever container currently
// shows it and rebuilds the Page entry around the same surface.
func (s *State) relocateExistingView(instance *appview.Instance) error {
	if instance == nil {
		return ErrViewNotPlaced
	}
	content, ok := s.locateViewContent(instance.ID())
	if !ok {
		return ErrViewNotPlaced
	}
	if content.surface == nil {
		surface, err := s.viewSurfaces(instance)
		if err != nil {
			return err
		}
		content.surface = surface
	}
	if content.remove != nil {
		content.remove()
	}
	s.placeView(content, domainplugin.PlacementPage)
	return nil
}

// focusExistingView shows the container already holding an instance. It
// reports the work Tab id when the instance lives in a Tab.
func (s *State) focusExistingView(instance *appview.Instance) (model.TabID, bool) {
	if instance == nil {
		return "", false
	}
	if tab, index, paneID := s.tabShowingView(instance.ID()); tab != nil {
		tab.setFocus(paneID)
		s.active = index
		s.leaveMonitor()
		s.vaultVisible = false
		s.logsVisible = false
		s.sftpVisible = false
		s.pageShown = ""
		s.save()
		s.RequestRedraw()
		return tab.ID, true
	}
	for _, tab := range s.detached {
		if _, _, ok := tabShowingView(tab, instance.ID()); ok {
			// A detached window owns its own focus; the main window can only
			// report that the instance is already shown.
			s.RequestRedraw()
			return tab.ID, true
		}
	}
	for viewID, entry := range s.pages {
		if entry != nil && entry.instance == instance {
			s.showPage(viewID)
			return "", true
		}
	}
	for instanceID, entry := range s.asides {
		if entry != nil && entry.instance == instance {
			s.showAside(instanceID)
			return "", true
		}
	}
	return "", false
}

// onlyViewInstance returns the View Instance when a Tab's whole content is
// one View Pane. Such a Tab follows the View rules for duplicate and detach.
func onlyViewInstance(tab *Tab) *appview.Instance {
	if tab == nil {
		return nil
	}
	ids := tab.paneIDs()
	if len(ids) != 1 {
		return nil
	}
	value := tab.panes[ids[0]]
	if value == nil {
		return nil
	}
	content := value.viewContent()
	if content == nil {
		return nil
	}
	return content.instance
}

// liveViewInstance returns the live View Instance a Pane shows, or nil for a
// Session or an unavailable placeholder.
func liveViewInstance(value *pane) *appview.Instance {
	if value == nil {
		return nil
	}
	content := value.viewContent()
	if content == nil || content.instance == nil || content.instance.Closed() {
		return nil
	}
	return content.instance
}

// tabViewInstances returns every live View Instance a Tab shows, in split-tree
// order. A View placeholder this build cannot open is not an instance and is
// skipped.
func tabViewInstances(tab *Tab) []*appview.Instance {
	if tab == nil {
		return nil
	}
	var instances []*appview.Instance
	for _, paneID := range tab.paneIDs() {
		if instance := liveViewInstance(tab.panes[paneID]); instance != nil {
			instances = append(instances, instance)
		}
	}
	return instances
}

// viewTabID is the work-Tab identity of a live View Instance. A View Tab is
// always named after its instance, so a restored Tab cannot keep a stale id
// that a later open would collide with.
func viewTabID(instance *appview.Instance) model.TabID {
	if instance == nil {
		return ""
	}
	return model.TabID("view:" + instance.ID())
}

// tabIDNamesView reports whether id is the canonical identity of one of the
// View Instances the Tab shows.
func tabIDNamesView(id model.TabID, instances []*appview.Instance) bool {
	for _, instance := range instances {
		if id == viewTabID(instance) {
			return true
		}
	}
	return false
}

// refreshViewTabID keeps a Tab's identity truthful after a View leaves it. A
// Tab showing one View takes that instance's canonical view:<instanceId> id; a
// Tab naming a View it no longer shows takes a remaining View's id, or a fresh
// runtime id when only Sessions are left. Side-panel state keyed by Tab id
// follows the rename, so a re-identified Tab keeps its open panel.
func (s *State) refreshViewTabID(tab *Tab) {
	if s == nil || tab == nil {
		return
	}
	instances := tabViewInstances(tab)
	want := tab.ID
	switch {
	case len(instances) == 1:
		want = viewTabID(instances[0])
	case len(instances) > 1:
		// A mixed Tab may keep an id that still names one of its Views. An id
		// left behind by a View that moved out would misname the Tab, so it
		// becomes the first remaining View's id.
		if !tabIDNamesView(tab.ID, instances) {
			want = viewTabID(instances[0])
		}
	default:
		// A Tab that no longer shows a View cannot keep a view: id: the id
		// would collide with the Tab a later move of that same View creates.
		if strings.HasPrefix(string(tab.ID), "view:") {
			want = ""
		}
	}
	if want == tab.ID {
		return
	}
	old := tab.ID
	tab.ID = s.uniqueRuntimeTabID(want, tab)
	if old != "" && old != tab.ID {
		s.renameTabState(old, tab.ID)
	}
}

// renameTabState moves the per-Tab side-panel entries to the Tab's new id.
func (s *State) renameTabState(old, new model.TabID) {
	if old == new {
		return
	}
	if target, ok := s.filesOpen[old]; ok {
		delete(s.filesOpen, old)
		s.filesOpen[new] = target
	}
	if chosen, ok := s.sideChosen[old]; ok {
		delete(s.sideChosen, old)
		s.sideChosen[new] = chosen
	}
}

// setTabViewPlacement records target in the restore descriptor of every Pane
// showing instanceID, so a saved Pane agrees with the registry placement.
func setTabViewPlacement(tab *Tab, instanceID string, target domainplugin.Placement) {
	if tab == nil || instanceID == "" {
		return
	}
	for _, paneID := range tab.paneIDs() {
		value := tab.panes[paneID]
		if value == nil {
			continue
		}
		content := value.viewContent()
		if content == nil || content.instance == nil || content.instance.ID() != instanceID {
			continue
		}
		content.descriptor.Placement = workspacePlacement(target)
	}
}

// moveTabViewsTo records target for every live View Instance a Tab contains.
// It validates the complete set before touching the registry and rolls already
// applied moves back on failure, so a Tab move and its View placements always
// change together.
func (s *State) moveTabViewsTo(tab *Tab, targets map[string]domainplugin.Placement) bool {
	instances := tabViewInstances(tab)
	if len(instances) == 0 {
		return true
	}
	if s == nil || s.views == nil {
		return false
	}
	for _, instance := range instances {
		target, ok := targets[instance.ID()]
		if !ok || !instance.Contribution().Allows(target) {
			return false
		}
	}
	type appliedMove struct {
		instance *appview.Instance
		previous domainplugin.Placement
		target   domainplugin.Placement
	}
	applied := make([]appliedMove, 0, len(instances))
	for _, instance := range instances {
		move := appliedMove{
			instance: instance,
			previous: instance.Placement(),
			target:   targets[instance.ID()],
		}
		if _, err := s.views.Move(instance.ID(), move.target); err != nil {
			for index := len(applied) - 1; index >= 0; index-- {
				_, _ = s.views.Move(applied[index].instance.ID(), applied[index].previous)
			}
			return false
		}
		applied = append(applied, move)
	}
	for _, move := range applied {
		setTabViewPlacement(tab, move.instance.ID(), move.target)
	}
	return true
}

// detachTabViews moves every live View a departing Tab contains to the Window
// placement. A contribution that does not declare Window refuses the detach
// before the Tab is touched, so the main window layout stays consistent.
func (s *State) detachTabViews(tab *Tab) bool {
	targets := make(map[string]domainplugin.Placement)
	for _, instance := range tabViewInstances(tab) {
		targets[instance.ID()] = domainplugin.PlacementWindow
	}
	return s.moveTabViewsTo(tab, targets)
}

// workTabPlacement is the placement a work Tab expresses for a View Instance:
// Tab when the View is alone in the Tab, Pane when it shares a split. A
// contribution that declares only the alternate records that one; one that
// declares neither cannot live in the container.
func workTabPlacement(instance *appview.Instance, single bool) (domainplugin.Placement, bool) {
	if instance == nil {
		return "", false
	}
	target, alternate := domainplugin.PlacementPane, domainplugin.PlacementTab
	if single {
		target, alternate = alternate, target
	}
	if instance.Contribution().Allows(target) {
		return target, true
	}
	if instance.Contribution().Allows(alternate) {
		return alternate, true
	}
	return "", false
}

// reconcileTabViewState is the post-mutation reconciliation every path that
// removes a Pane from a Tab or moves a Tab between containers must run. It
// records the placement each remaining View now expresses, updates the saved
// descriptor with that placement, and re-identifies the Tab: a single-View Tab
// takes the canonical view:<instanceId> id, a Tab that no longer shows a View
// drops the view: namespace, and per-Tab side state follows the rename. Keeping
// this atomic with the layout mutation is what stops a stale id from colliding
// with the Tab a later move of the same instance creates. A detached Tab keeps
// every View in the Window placement, so only its identity is reconciled here;
// reattach records the work-Tab placement.
func (s *State) reconcileTabViewState(tab *Tab) {
	if s == nil || tab == nil {
		return
	}
	if !s.tabIsDetached(tab) {
		s.settleTabViewPlacements(tab)
	}
	s.refreshViewTabID(tab)
}

// tabIsDetached reports whether the Tab is owned by a detached window.
func (s *State) tabIsDetached(tab *Tab) bool {
	if s == nil || tab == nil {
		return false
	}
	for _, candidate := range s.detached {
		if candidate == tab {
			return true
		}
	}
	return false
}

// settleTabViewPlacements records the placement a work Tab now expresses for
// every View it shows. A Pane leaving a split can leave a View alone in the
// Tab, so the remaining View records Tab instead of a stale Pane. A View that
// cannot accept the new shape keeps its recorded placement rather than
// blocking the Pane operation that already happened.
func (s *State) settleTabViewPlacements(tab *Tab) {
	if s == nil || s.views == nil || tab == nil {
		return
	}
	single := onlyViewInstance(tab) != nil
	for _, instance := range tabViewInstances(tab) {
		target, ok := workTabPlacement(instance, single)
		if !ok || instance.Placement() == target {
			continue
		}
		if _, err := s.views.Move(instance.ID(), target); err != nil {
			continue
		}
		setTabViewPlacement(tab, instance.ID(), target)
	}
}

// reattachTabViews moves every live View a returning Tab contains to the
// placement its resulting container expresses: Tab for a Tab that holds one
// View, Pane for a View sharing a split. If the contribution declares only the
// other work-Tab placement, the View records that one; a contribution that
// accepts neither refuses the reattach so the Tab stays in its window instead
// of carrying a stale placement.
func (s *State) reattachTabViews(tab *Tab) bool {
	instances := tabViewInstances(tab)
	if len(instances) == 0 {
		return true
	}
	single := onlyViewInstance(tab) != nil
	targets := make(map[string]domainplugin.Placement, len(instances))
	for _, instance := range instances {
		target, ok := workTabPlacement(instance, single)
		if !ok {
			return false
		}
		targets[instance.ID()] = target
	}
	return s.moveTabViewsTo(tab, targets)
}

// tabShowingView finds the Tab and Pane that already show an instance.
func (s *State) tabShowingView(instanceID string) (*Tab, int, model.PaneID) {
	if instanceID == "" {
		return nil, -1, ""
	}
	for index, tab := range s.tabs {
		if foundTab, paneID, ok := tabShowingView(tab, instanceID); ok {
			return foundTab, index, paneID
		}
	}
	return nil, -1, ""
}

// tabShowingView finds the Pane in one Tab that shows an instance.
func tabShowingView(tab *Tab, instanceID string) (*Tab, model.PaneID, bool) {
	if tab == nil || instanceID == "" {
		return nil, "", false
	}
	for paneID, value := range tab.panes {
		content := value.viewContent()
		if content == nil || content.instance == nil {
			continue
		}
		if content.instance.ID() == instanceID && domainworkspace.ContainsPane(tab.Root, paneID) {
			return tab, paneID, true
		}
	}
	return nil, "", false
}

// locateViewContent detaches the instance's rendering content from whichever
// container shows it. The returned remove closure performs the mutation; it is
// only run once the target placement has accepted the move.
func (s *State) locateViewContent(instanceID string) (*placedView, bool) {
	for index, tab := range s.tabs {
		if _, paneID, ok := tabShowingView(tab, instanceID); ok {
			value := tab.panes[paneID]
			content := value.viewContent()
			tabIndex := index
			remove := func() {
				if len(tab.paneIDs()) == 1 {
					s.removeTabAt(tabIndex)
					return
				}
				root, err := domainworkspace.RemovePane(tab.Root, paneID)
				if err != nil {
					return
				}
				tab.Root = root
				delete(tab.panes, paneID)
				tab.FocusPath = domainworkspace.FocusPath(root, "")
				// The remaining Views agree with the shape the Tab now has, and
				// a Tab named after the View that left takes a truthful id
				// before the moved instance's new container claims the
				// canonical one.
				s.reconcileTabViewState(tab)
			}
			return &placedView{instance: content.instance, surface: value.surface, remove: remove}, true
		}
	}
	for index, tab := range s.detached {
		if _, paneID, ok := tabShowingView(tab, instanceID); ok {
			value := tab.panes[paneID]
			content := value.viewContent()
			detachedIndex := index
			tabID := tab.ID
			remove := func() {
				if len(tab.paneIDs()) == 1 {
					s.detached = append(s.detached[:detachedIndex], s.detached[detachedIndex+1:]...)
					if s.onDetachedClose != nil {
						s.onDetachedClose(tabID)
					}
					return
				}
				// A mixed Tab keeps its sibling Panes and window alive; only
				// the moving View leaves, exactly as it would from the main
				// window. The leftover Tab is reconciled before the moved
				// instance's new container can claim the canonical id, so the
				// detached window never keeps a view: id that names a View it
				// no longer shows.
				root, err := domainworkspace.RemovePane(tab.Root, paneID)
				if err != nil {
					return
				}
				tab.Root = root
				delete(tab.panes, paneID)
				tab.FocusPath = domainworkspace.FocusPath(root, "")
				s.reconcileTabViewState(tab)
			}
			return &placedView{instance: content.instance, surface: value.surface, remove: remove}, true
		}
	}
	for viewID, entry := range s.pages {
		if entry == nil || entry.instance == nil || entry.instance.ID() != instanceID {
			continue
		}
		pageID := viewID
		remove := func() {
			delete(s.pages, pageID)
			if s.pageShown == pageID {
				s.pageShown = ""
			}
		}
		return &placedView{instance: entry.instance, surface: entry.surface, remove: remove}, true
	}
	for key, entry := range s.asides {
		if entry == nil || entry.instance == nil || entry.instance.ID() != instanceID {
			continue
		}
		asideID := key
		remove := func() { delete(s.asides, asideID) }
		return &placedView{instance: entry.instance, surface: entry.surface, remove: remove}, true
	}
	return nil, false
}

// MoveView relocates a live View Instance to another declared placement. The
// registry validates first; only then does the shell take the surface out of
// its source container. A rejected move therefore leaves the source unchanged.
func (s *State) MoveView(instanceID string, placement domainplugin.Placement) error {
	if s == nil || s.views == nil {
		return ErrNoViews
	}
	instance, ok := s.views.Instance(instanceID)
	if !ok {
		return appview.ErrUnknownInstance
	}
	if instance.Placement() == placement && s.viewPlaced(instance, placement) {
		return nil
	}
	content, ok := s.locateViewContent(instanceID)
	if !ok {
		return ErrViewNotPlaced
	}
	if _, err := s.views.Move(instanceID, placement); err != nil {
		if errors.Is(err, appview.ErrPageOccupied) {
			if page, ok := s.views.PageInstance(instance.ViewID()); ok {
				s.focusExistingView(page)
			}
		}
		return err
	}
	if content.remove != nil {
		content.remove()
	}
	s.placeView(content, placement)
	return nil
}

// viewPlaced reports whether the shell container agrees with the instance's
// recorded placement. It catches a registry state that changed before the
// shell could relocate the surface.
func (s *State) viewPlaced(instance *appview.Instance, placement domainplugin.Placement) bool {
	if instance == nil {
		return false
	}
	switch placement {
	case domainplugin.PlacementPane, domainplugin.PlacementTab:
		tab, _, _ := s.tabShowingView(instance.ID())
		return tab != nil
	case domainplugin.PlacementPage:
		return s.pageShowing(instance)
	case domainplugin.PlacementWindow:
		for _, tab := range s.detached {
			if _, _, ok := tabShowingView(tab, instance.ID()); ok {
				return true
			}
		}
		return false
	case domainplugin.PlacementAside:
		for _, entry := range s.asides {
			if entry != nil && entry.instance == instance {
				return true
			}
		}
	}
	return false
}

// DetachView moves a View Instance to its own window. Detaching preserves the
// instance identity and logical state; it uses the same move path.
func (s *State) DetachView(instanceID string) error {
	return s.MoveView(instanceID, domainplugin.PlacementWindow)
}

// DuplicateView creates an independent multi-instance copy. No state is
// copied: the plugin owns state cloning. Singleton contributions are rejected
// by the registry, including when the copy targets a Window.
func (s *State) DuplicateView(instanceID string, placement domainplugin.Placement) (string, error) {
	if s == nil || s.views == nil || s.viewSurfaces == nil {
		return "", ErrNoViews
	}
	source, ok := s.views.Instance(instanceID)
	if !ok {
		return "", appview.ErrUnknownInstance
	}
	if placement == "" {
		placement = source.Placement()
	}
	copied, err := s.views.Duplicate(instanceID, placement)
	if err != nil {
		return "", err
	}
	surface, err := s.viewSurfaces(copied)
	if err != nil {
		_ = copied.Close()
		return "", err
	}
	s.placeView(&placedView{instance: copied, surface: surface}, placement)
	return copied.ID(), nil
}

// ViewPlacements returns the placements a live instance's contribution
// declares, for the View Pane menu.
func (s *State) ViewPlacements(instanceID string) []domainplugin.Placement {
	if s == nil || s.views == nil {
		return nil
	}
	instance, ok := s.views.Instance(instanceID)
	if !ok {
		return nil
	}
	return append([]domainplugin.Placement(nil), instance.Contribution().Placements...)
}

// ViewCanDuplicate reports whether the instance's contribution is
// multi-instance, so the menu can disable Duplicate for singletons.
func (s *State) ViewCanDuplicate(instanceID string) bool {
	if s == nil || s.views == nil {
		return false
	}
	instance, ok := s.views.Instance(instanceID)
	return ok && instance.Contribution().Instances.Multi()
}

// ViewAllowsPlacement reports whether the contribution declares placement, so
// a menu never offers a target the move would reject.
func (s *State) ViewAllowsPlacement(instanceID string, placement domainplugin.Placement) bool {
	for _, allowed := range s.ViewPlacements(instanceID) {
		if allowed == placement {
			return true
		}
	}
	return false
}

// placeView attaches already-detached content to a placement container. It
// cannot fail: every target container is always available once the registry
// accepted the placement.
func (s *State) placeView(content *placedView, placement domainplugin.Placement) model.TabID {
	if content == nil {
		return ""
	}
	tabID := model.TabID("")
	switch placement {
	case domainplugin.PlacementPage:
		viewID := content.descriptor().ViewID
		if viewID == "" {
			viewID = content.instance.ViewID()
		}
		if s.pages == nil {
			s.pages = make(map[string]*pageEntry)
		}
		s.pages[viewID] = &pageEntry{
			instance:   content.instance,
			surface:    content.surface,
			descriptor: content.descriptor(),
		}
		s.showPage(viewID)
	case domainplugin.PlacementWindow:
		tab := s.newViewTab(content.instance, content.surface, placement)
		s.detached = append(s.detached, tab)
		tabID = tab.ID
		if s.onDetach != nil {
			s.onDetach(tab.ID)
		}
		s.save()
		s.RequestRedraw()
	case domainplugin.PlacementAside:
		if s.asides == nil {
			s.asides = make(map[string]*asideEntry)
		}
		instanceID := content.instance.ID()
		s.asides[instanceID] = &asideEntry{
			instance:   content.instance,
			surface:    content.surface,
			descriptor: content.descriptor(),
		}
		s.showAside(instanceID)
		s.save()
		s.RequestRedraw()
	default: // Pane and Tab share one work Tab holding the View as a Pane.
		tab := s.newViewTab(content.instance, content.surface, placement)
		s.tabs = append(s.tabs, tab)
		s.active = len(s.tabs) - 1
		s.leaveMonitor()
		s.leaveSystemManager()
		s.vaultVisible = false
		s.logsVisible = false
		s.sftpVisible = false
		s.pageShown = ""
		s.refreshBroadcastState()
		s.save()
		s.RequestRedraw()
		tabID = tab.ID
	}
	return tabID
}

// newViewTab builds the work Tab for a View Instance. The tab identity is
// namespaced under view:<instanceId> so a View can never collide with a
// Session or editor tab.
func (s *State) newViewTab(instance *appview.Instance, surface Surface,
	placement domainplugin.Placement) *Tab {
	number := s.nextTabNumber
	s.nextTabNumber++
	paneID := model.PaneID(newRuntimeID("pane-"))
	root := domainworkspace.NewPaneNode(paneID)
	label := viewTitle(instance)
	descriptor := instance.Descriptor()
	descriptor.Placement = workspacePlacement(placement)
	return &Tab{
		// Every View Tab is created through one collision-aware assignment, so
		// a stale mixed Tab left behind by an earlier move cannot duplicate the
		// canonical id and make the payload drop a live View Tab at save time.
		ID:          s.uniqueRuntimeTabID(viewTabID(instance), nil),
		Number:      number,
		TitleNumber: number,
		Label:       label,
		Root:        root,
		FocusPath:   domainworkspace.FocusPath(root, paneID),
		panes: map[model.PaneID]*pane{
			paneID: {
				ID:      paneID,
				content: &viewContent{instance: instance, descriptor: descriptor},
				surface: surface,
			},
		},
	}
}

// RefreshViewTitles re-reads every open View's localized title, including
// Pages and aside slots. It runs after a host locale change without reopening
// any instance.
func (s *State) RefreshViewTitles() {
	if s == nil {
		return
	}
	changed := false
	for _, tab := range s.tabs {
		if refreshViewTabTitle(tab) {
			changed = true
		}
	}
	for _, tab := range s.detached {
		if refreshViewTabTitle(tab) {
			changed = true
		}
	}
	for _, entry := range s.pages {
		// A page title is read live from its instance, so a redraw is all a
		// locale change needs.
		if entry != nil && entry.instance != nil && !entry.instance.Closed() {
			changed = true
		}
	}
	if changed {
		s.RequestRedraw()
	}
}

func refreshViewTabTitle(tab *Tab) bool {
	if tab == nil {
		return false
	}
	changed := false
	for _, value := range tab.panes {
		if value == nil {
			continue
		}
		content := value.viewContent()
		if content == nil || content.instance == nil || content.instance.Closed() {
			continue
		}
		label := viewTitle(content.instance)
		if tab.Label != label {
			tab.Label = label
			changed = true
		}
	}
	return changed
}

// pageTitle and setPageTitle keep the displayed title next to the entry while
// refreshViewTabTitle handles work Tabs.
func (s *State) pageTitle(viewID string) string {
	entry := s.pages[viewID]
	if entry == nil {
		return ""
	}
	if entry.instance != nil {
		return viewTitle(entry.instance)
	}
	if entry.unavailable != nil {
		if entry.unavailable.Title != "" {
			return entry.unavailable.Title
		}
		return entry.unavailable.ViewID
	}
	return entry.descriptor.ViewID
}

// ShowPage shows an open View Page and makes it the home area's surface.
func (s *State) ShowPage(viewID string) bool {
	if s.pages == nil || s.pages[viewID] == nil {
		return false
	}
	s.showPage(viewID)
	return true
}

func (s *State) showPage(viewID string) {
	if s.pages == nil || s.pages[viewID] == nil {
		return
	}
	s.pageShown = viewID
	s.leaveMonitor()
	s.leaveSystemManager()
	s.vaultVisible = false
	s.logsVisible = false
	s.sftpVisible = false
	s.save()
	s.RequestRedraw()
}

// HidePage returns from a View Page to the last built-in home page without
// closing the page.
func (s *State) HidePage() {
	if s.pageShown == "" {
		return
	}
	s.pageShown = ""
	s.ShowVault()
}

// PageVisible reports whether a View Page is the home area's shown surface.
func (s *State) PageVisible() bool {
	return s.pageShown != "" && s.pages != nil && s.pages[s.pageShown] != nil
}

// ShownPageViewID returns the visible View Page's id, or an empty string.
func (s *State) ShownPageViewID() string { return s.pageShown }

// PageView returns one open page's description.
func (s *State) PageView(viewID string) (PageViewInfo, bool) {
	entry := s.pages[viewID]
	if entry == nil {
		return PageViewInfo{}, false
	}
	info := PageViewInfo{
		ViewID:  viewID,
		Title:   s.pageTitle(viewID),
		Surface: entry.surface,
		Err:     entry.err,
		Visible: s.pageShown == viewID,
	}
	if entry.instance != nil {
		info.ContextKey = entry.instance.ContextKey()
	} else if entry.unavailable != nil {
		info.ContextKey = entry.unavailable.ContextKey
	}
	return info, true
}

// PageViews returns every open View Page ordered by view id.
func (s *State) PageViews() []PageViewInfo {
	ids := make([]string, 0, len(s.pages))
	for viewID := range s.pages {
		ids = append(ids, viewID)
	}
	sort.Strings(ids)
	out := make([]PageViewInfo, 0, len(ids))
	for _, viewID := range ids {
		if info, ok := s.PageView(viewID); ok {
			out = append(out, info)
		}
	}
	return out
}

// HomePages lists the View Contributions that declare the Page placement.
// The shell's sidebar renders them beside the built-in Vault pages, so Home
// navigation is contribution-driven rather than hard-coded.
func (s *State) HomePages() []HomePageInfo {
	if s == nil || s.views == nil {
		return nil
	}
	var pages []HomePageInfo
	listed := make(map[string]bool)
	for _, contribution := range s.views.Contributions() {
		if !contributionAllows(contribution, domainplugin.PlacementPage) ||
			contribution.Instances.Multi() {
			// A Page is a singleton slot; a multi-instance contribution can
			// declare it but can never open there.
			continue
		}
		listed[contribution.ID] = true
		title := contribution.Title
		if resolved, ok := s.views.ResolveTitle(contribution.ID); ok {
			title = resolved
		}
		pages = append(pages, HomePageInfo{ViewID: contribution.ID, Title: title})
	}
	// A restored page whose contribution is missing stays reachable from the
	// sidebar, where its unavailable surface explains the absence.
	for viewID := range s.pages {
		if listed[viewID] {
			continue
		}
		title := s.pageTitle(viewID)
		if title == "" {
			title = viewID
		}
		pages = append(pages, HomePageInfo{ViewID: viewID, Title: title})
	}
	sort.Slice(pages, func(i, j int) bool {
		if pages[i].Title != pages[j].Title {
			return pages[i].Title < pages[j].Title
		}
		return pages[i].ViewID < pages[j].ViewID
	})
	return pages
}

func contributionAllows(contribution appview.Contribution, placement domainplugin.Placement) bool {
	for _, allowed := range contribution.Placements {
		if allowed == placement {
			return true
		}
	}
	return false
}

// ClosePage closes a View Page's surface and disposes its instance.
func (s *State) ClosePage(viewID string) bool {
	entry := s.pages[viewID]
	if entry == nil {
		return false
	}
	delete(s.pages, viewID)
	if s.pageShown == viewID {
		s.pageShown = ""
	}
	s.disposeViewEntry(entry.instance, entry.surface)
	if s.pageShown == "" {
		s.ShowVault()
	}
	s.save()
	s.RequestRedraw()
	return true
}

// AsideViews returns every View Instance placed in a side slot, ordered by
// title then instance id.
func (s *State) AsideViews() []AsideViewInfo {
	keys := make([]string, 0, len(s.asides))
	for key := range s.asides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]AsideViewInfo, 0, len(keys))
	for _, key := range keys {
		entry := s.asides[key]
		if entry == nil {
			continue
		}
		info := AsideViewInfo{InstanceID: key}
		if entry.instance != nil {
			info.ViewID = entry.instance.ViewID()
			info.ContextKey = entry.instance.ContextKey()
			info.Title = viewTitle(entry.instance)
		} else if entry.unavailable != nil {
			info.ViewID = entry.unavailable.ViewID
			info.ContextKey = entry.unavailable.ContextKey
			info.Title = entry.unavailable.Title
			if info.Title == "" {
				info.Title = entry.unavailable.ViewID
			}
		}
		info.Surface = entry.surface
		info.Err = entry.err
		out = append(out, info)
	}
	return out
}

// AsideView returns one aside slot by instance id.
func (s *State) AsideView(instanceID string) (AsideViewInfo, bool) {
	for _, info := range s.AsideViews() {
		if info.InstanceID == instanceID {
			return info, true
		}
	}
	return AsideViewInfo{}, false
}

// ShowAside selects a View Instance's aside tool in the active Tab's side
// panel, opening the panel when it is closed.
func (s *State) ShowAside(instanceID string) bool {
	return s.showAside(instanceID)
}

func (s *State) showAside(instanceID string) bool {
	if s.asides == nil || s.asides[instanceID] == nil {
		return false
	}
	tab, _ := s.activeTab()
	if tab == nil {
		return false
	}
	if s.filesOpen == nil {
		s.filesOpen = map[model.TabID]filesTarget{}
	}
	if _, open := s.filesOpen[tab.ID]; !open {
		s.filesOpen[tab.ID] = filesTarget{}
	}
	if s.sideChosen == nil {
		s.sideChosen = map[model.TabID]SideToolID{}
	}
	s.sideChosen[tab.ID] = asideToolID(instanceID)
	s.RequestRedraw()
	return true
}

// CloseAside closes an aside slot's surface and disposes its instance.
func (s *State) CloseAside(instanceID string) bool {
	entry := s.asides[instanceID]
	if entry == nil {
		return false
	}
	delete(s.asides, instanceID)
	s.disposeViewEntry(entry.instance, entry.surface)
	s.save()
	s.RequestRedraw()
	return true
}

func (s *State) disposeViewEntry(instance *appview.Instance, surface Surface) {
	if surface != nil {
		surface.Close()
	}
	if instance != nil {
		_ = instance.Close()
	}
}

// asideToolID and viewToolID namespace the side panel's View tools.
func asideToolID(instanceID string) SideToolID {
	return SideToolID("aside:" + instanceID)
}

func viewToolID(viewID string) SideToolID {
	return SideToolID("view:" + viewID)
}

// sideViewTitles resolves a View tool id to its contribution or instance
// title, or an empty string for a built-in tool.
func (s *State) sideViewTitle(id SideToolID) string {
	if viewID, ok := strings.CutPrefix(string(id), "view:"); ok {
		if s.views != nil {
			if title, exists := s.views.ResolveTitle(viewID); exists {
				return title
			}
		}
		return viewID
	}
	if instanceID, ok := strings.CutPrefix(string(id), "aside:"); ok {
		if entry := s.asides[instanceID]; entry != nil {
			if entry.instance != nil {
				return viewTitle(entry.instance)
			}
			if entry.unavailable != nil {
				if entry.unavailable.Title != "" {
					return entry.unavailable.Title
				}
				return entry.unavailable.ViewID
			}
		}
	}
	return ""
}

// SideViewLabel is the side panel strip's label for a View tool.
func (s *State) SideViewLabel(id SideToolID) string { return s.sideViewTitle(id) }

// asideContext derives the explicit context a side panel passes to a View.
// A Host panel gets its Host id; a labeled local panel gets "local:<label>";
// a target with neither is app-wide ("global").
func asideContext(target SideTarget) string {
	if target.Host != "" {
		return string(target.Host)
	}
	if target.Label != "" {
		return "local:" + target.Label
	}
	return "global"
}

// viewAsideContributions lists side slot contributions in contribution order.
func (s *State) viewAsideContributions() []appview.Contribution {
	if s == nil || s.views == nil {
		return nil
	}
	var contributions []appview.Contribution
	for _, contribution := range s.views.Contributions() {
		if contributionAllows(contribution, domainplugin.PlacementAside) {
			contributions = append(contributions, contribution)
		}
	}
	return contributions
}

// viewAsideFace resolves a contribution-driven side tool for a tab, opening
// its instance lazily with the target's explicit context. A singleton that is
// already open elsewhere moves into the aside instead of appearing in two
// placements at once.
func (s *State) viewAsideFace(viewID string, target SideTarget) func(*ui.Context) {
	if s == nil || s.views == nil || s.viewSurfaces == nil {
		return unavailableViewFace(viewID)
	}
	contextKey := asideContext(target)
	if entry := s.asideForViewContext(viewID, contextKey); entry != nil && entry.surface != nil {
		return func(c *ui.Context) { entry.surface.View(c) }
	}
	instance, created, err := s.views.Open(appview.OpenRequest{
		ViewID:     viewID,
		ContextKey: contextKey,
		Placement:  domainplugin.PlacementAside,
	})
	if err != nil {
		return unavailableViewFace(viewID)
	}
	if !created && instance.Placement() != domainplugin.PlacementAside {
		if err := s.MoveView(instance.ID(), domainplugin.PlacementAside); err != nil {
			return unavailableViewFace(viewID)
		}
	}
	if entry := s.asideFor(instance); entry != nil && entry.surface != nil {
		s.showAside(instance.ID())
		return func(c *ui.Context) { entry.surface.View(c) }
	}
	surface, err := s.viewSurfaces(instance)
	if err != nil {
		if created {
			_ = instance.Close()
		}
		return unavailableViewFace(viewID)
	}
	if s.asides == nil {
		s.asides = make(map[string]*asideEntry)
	}
	s.asides[instance.ID()] = &asideEntry{
		instance:   instance,
		surface:    surface,
		descriptor: instance.Descriptor(),
	}
	s.showAside(instance.ID())
	return func(c *ui.Context) { surface.View(c) }
}

// SideViewUnavailable reports whether a View tool has no surface to draw. The
// shell renders its localized unavailable placeholder instead of the raw id.
func (s *State) SideViewUnavailable(id SideToolID) bool {
	if s == nil {
		return false
	}
	if viewID, ok := strings.CutPrefix(string(id), "view:"); ok {
		if s.views == nil {
			return true
		}
		_, exists := s.views.Contribution(viewID)
		return !exists
	}
	if instanceID, ok := strings.CutPrefix(string(id), "aside:"); ok {
		entry := s.asides[instanceID]
		return entry == nil || entry.surface == nil
	}
	return false
}

func (s *State) asideFor(instance *appview.Instance) *asideEntry {
	if instance == nil || s.asides == nil {
		return nil
	}
	return s.asides[instance.ID()]
}

// entryContext is the explicit context an aside slot was opened for.
func entryContext(entry *asideEntry) string {
	if entry == nil {
		return ""
	}
	if entry.instance != nil {
		return entry.instance.ContextKey()
	}
	if entry.unavailable != nil {
		return entry.unavailable.ContextKey
	}
	return ""
}

func unavailableViewFace(viewID string) func(*ui.Context) {
	return func(c *ui.Context) {
		ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Text(c, viewID).FontSize(12).TextColor(c.Theme().TextMuted)
		})
	}
}

// restoreViewPane rebuilds one View Pane from a restore descriptor. A missing
// contribution or a failed surface keeps the Pane as an explained placeholder
// instead of failing the whole Workspace restore.
func (s *State) restoreViewPane(id model.PaneID, descriptor domainworkspace.PaneDescriptor) *pane {
	value := &pane{ID: id}
	if descriptor.View == nil || strings.TrimSpace(descriptor.View.ViewID) == "" {
		stored := descriptor
		value.unavailable = &stored
		value.err = ErrUnavailablePane
		return value
	}
	instance, surface, err := s.openRestoredView(*descriptor.View)
	if err != nil {
		stored := descriptor
		value.unavailable = &stored
		value.err = ErrUnavailablePane
		return value
	}
	view := instance.Descriptor()
	view.Placement = descriptor.View.PlacementOrDefault()
	value.content = &viewContent{instance: instance, descriptor: view}
	value.surface = surface
	return value
}

// openRestoredView opens one instance and its surface for restore.
func (s *State) openRestoredView(descriptor domainworkspace.ViewPaneDescriptor) (*appview.Instance, Surface, error) {
	if s.views == nil || s.viewSurfaces == nil {
		return nil, nil, ErrNoViews
	}
	placement := descriptor.PlacementOrDefault()
	instance, _, err := s.views.Open(appview.OpenRequest{
		ViewID:     descriptor.ViewID,
		ContextKey: descriptor.ContextKey,
		Placement:  pluginPlacement(placement),
		State:      descriptor.State,
	})
	if err != nil {
		return nil, nil, err
	}
	surface, err := s.viewSurfaces(instance)
	if err != nil {
		_ = instance.Close()
		return nil, nil, err
	}
	return instance, surface, nil
}

// restorePageEntry rebuilds a Page placement descriptor.
func (s *State) restorePageEntry(restored domainworkspace.RestoredView) *pageEntry {
	entry := &pageEntry{unavailable: &restored, err: ErrUnavailablePane}
	view := domainworkspace.ViewPaneDescriptor{
		ViewID:     restored.ViewID,
		ContextKey: restored.ContextKey,
		Placement:  domainworkspace.ViewPlacementPage,
		State:      restored.State,
	}
	instance, surface, err := s.openRestoredView(view)
	if err != nil {
		return entry
	}
	entry.instance = instance
	entry.surface = surface
	entry.descriptor = instance.Descriptor()
	entry.unavailable = nil
	entry.err = nil
	return entry
}

// restoreAsideEntry rebuilds an Aside placement descriptor.
func (s *State) restoreAsideEntry(restored domainworkspace.RestoredView) (*asideEntry, string) {
	entry := &asideEntry{unavailable: &restored, err: ErrUnavailablePane}
	key := "restored:" + restored.ViewID + "\x00" + restored.ContextKey
	view := domainworkspace.ViewPaneDescriptor{
		ViewID:     restored.ViewID,
		ContextKey: restored.ContextKey,
		Placement:  domainworkspace.ViewPlacementAside,
		State:      restored.State,
	}
	instance, surface, err := s.openRestoredView(view)
	if err != nil {
		return entry, key
	}
	entry.instance = instance
	entry.surface = surface
	entry.descriptor = instance.Descriptor()
	entry.unavailable = nil
	entry.err = nil
	return entry, instance.ID()
}

// restoreWindowTab rebuilds a detached-window descriptor as a one-Pane View
// Tab. A missing view keeps an unavailable Pane so the window can explain it.
func (s *State) restoreWindowTab(restored domainworkspace.RestoredView) *Tab {
	number := s.nextTabNumber
	s.nextTabNumber++
	paneID := model.PaneID(newRuntimeID("pane-"))
	root := domainworkspace.NewPaneNode(paneID)
	label := restored.Title
	if label == "" {
		label = restored.ViewID
	}
	descriptor := domainworkspace.PaneDescriptor{
		Kind: domainworkspace.PaneKindView,
		View: &domainworkspace.ViewPaneDescriptor{
			ViewID:     restored.ViewID,
			ContextKey: restored.ContextKey,
			Placement:  domainworkspace.ViewPlacementWindow,
			State:      restored.State,
		},
	}
	value := s.restoreViewPane(paneID, descriptor)
	id := model.TabID("view:" + newRuntimeID("restored-"))
	if content := value.viewContent(); content != nil && content.instance != nil {
		// A live restored window follows the same view:<instanceId> identity as
		// a View opened this run; an unavailable placeholder keeps a fresh id.
		id = s.uniqueRestoredTabID(viewTabID(content.instance))
		if restored.Title == "" {
			if title := viewTitle(content.instance); title != "" {
				label = title
			}
		}
	}
	return &Tab{
		ID:          id,
		Number:      number,
		TitleNumber: number,
		Label:       label,
		Root:        root,
		FocusPath:   domainworkspace.FocusPath(root, paneID),
		panes:       map[model.PaneID]*pane{paneID: value},
	}
}

// RestorePlacements applies the per-placement View descriptors that are not
// part of a work Tab. It is called by Restore after the Tabs exist. The stored
// Order decides the ordering within each placement; equal orders keep the
// payload order.
func (s *State) RestorePlacements(views []domainworkspace.RestoredView) {
	ordered := append([]domainworkspace.RestoredView(nil), views...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	for _, restored := range ordered {
		switch restored.Placement {
		case domainworkspace.ViewPlacementPage:
			if s.pages == nil {
				s.pages = make(map[string]*pageEntry)
			}
			if _, exists := s.pages[restored.ViewID]; exists {
				continue
			}
			s.pages[restored.ViewID] = s.restorePageEntry(restored)
		case domainworkspace.ViewPlacementAside:
			if s.asides == nil {
				s.asides = make(map[string]*asideEntry)
			}
			entry, key := s.restoreAsideEntry(restored)
			s.asides[key] = entry
		case domainworkspace.ViewPlacementWindow:
			tab := s.restoreWindowTab(restored)
			s.detached = append(s.detached, tab)
			if s.onDetach != nil {
				s.onDetach(tab.ID)
			}
		}
	}
}

// placementViews appends the payload descriptors for the placements outside
// work Tabs: Pages, Windows, and Asides. The order is deterministic so the
// payload does not churn between saves.
func (s *State) placementViews() []domainworkspace.RestoredView {
	var out []domainworkspace.RestoredView
	pageIDs := make([]string, 0, len(s.pages))
	for viewID := range s.pages {
		pageIDs = append(pageIDs, viewID)
	}
	sort.Strings(pageIDs)
	for order, viewID := range pageIDs {
		entry := s.pages[viewID]
		if entry == nil {
			continue
		}
		view := domainworkspace.RestoredView{
			Placement: domainworkspace.ViewPlacementPage,
			Title:     s.pageTitle(viewID),
			Order:     order,
		}
		if entry.instance != nil && !entry.instance.Closed() {
			view.ViewID = entry.instance.ViewID()
			view.ContextKey = entry.instance.ContextKey()
			view.State = entry.instance.State()
		} else if entry.unavailable != nil {
			view.ViewID = entry.unavailable.ViewID
			view.ContextKey = entry.unavailable.ContextKey
		}
		if view.ViewID == "" {
			continue
		}
		out = append(out, view)
	}
	asideIDs := make([]string, 0, len(s.asides))
	for instanceID := range s.asides {
		asideIDs = append(asideIDs, instanceID)
	}
	sort.Strings(asideIDs)
	for order, instanceID := range asideIDs {
		entry := s.asides[instanceID]
		if entry == nil {
			continue
		}
		view := domainworkspace.RestoredView{
			Placement: domainworkspace.ViewPlacementAside,
			Order:     order,
		}
		if entry.instance != nil && !entry.instance.Closed() {
			view.ViewID = entry.instance.ViewID()
			view.ContextKey = entry.instance.ContextKey()
			view.Title = viewTitle(entry.instance)
			view.State = entry.instance.State()
		} else if entry.unavailable != nil {
			view.ViewID = entry.unavailable.ViewID
			view.ContextKey = entry.unavailable.ContextKey
			view.Title = entry.unavailable.Title
		}
		if view.ViewID == "" {
			continue
		}
		out = append(out, view)
	}
	for _, tab := range s.detached {
		for _, paneID := range tab.paneIDs() {
			value := tab.panes[paneID]
			if value == nil {
				continue
			}
			content := value.viewContent()
			if content != nil && content.instance != nil && !content.instance.Closed() {
				out = append(out, domainworkspace.RestoredView{
					Placement:  domainworkspace.ViewPlacementWindow,
					ViewID:     content.instance.ViewID(),
					ContextKey: content.instance.ContextKey(),
					Title:      viewTitle(content.instance),
					State:      content.instance.State(),
				})
				continue
			}
			if value.unavailable == nil || value.unavailable.Kind != domainworkspace.PaneKindView ||
				value.unavailable.View == nil {
				continue
			}
			out = append(out, domainworkspace.RestoredView{
				Placement:  domainworkspace.ViewPlacementWindow,
				ViewID:     value.unavailable.View.ViewID,
				ContextKey: value.unavailable.View.ContextKey,
				Title:      tab.Label,
				State:      value.unavailable.View.State,
			})
		}
	}
	return out
}
