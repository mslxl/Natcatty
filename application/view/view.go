// Package view owns the View Contribution registry and the live View
// Instances. A ViewContribution is a plugin-declared view; a ViewInstance is
// one live presentation for a fixed context. The registry is keyed by
// (viewId, contextKey), and multi-instance is opt-in.
//
// The shell owns the surfaces that render an instance; this package owns only
// identity, placement policy, and the bounded restore state. Every declared
// placement is served: Pane, Tab, Page, Window, and Aside.
package view

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// Owner identifies the plugin behind a View Contribution.
type Owner struct {
	PluginID    string
	PluginName  string
	Version     string
	Development bool
	Source      domainplugin.Source
}

// Contribution is one registered view declaration with its qualified id.
type Contribution struct {
	ID      string
	LocalID string
	Title   string
	// TitleKey names a catalog message resolved at display time in the host
	// locale. Title stays the literal fallback.
	TitleKey   string
	Placements []domainplugin.Placement
	Instances  domainplugin.InstancePolicy
	// Surface is native or the sandboxed webview. Empty means native.
	Surface domainplugin.ViewSurface
	// Entry is the packaged HTML entry of a webview View.
	Entry string
	// Bridge is the fixed backend method allowlist page code may call.
	Bridge []string
	Owner  Owner
}

// Allows reports whether the contribution declares placement p.
func (c Contribution) Allows(p domainplugin.Placement) bool {
	for _, placement := range c.Placements {
		if placement == p {
			return true
		}
	}
	return false
}

// Disposer removes one registered contribution and closes its live instances.
// It is idempotent.
type Disposer func()

// Registry errors.
var (
	// ErrUnknownView reports an open or lookup for an unregistered view.
	ErrUnknownView = errors.New("view: unknown view contribution")
	// ErrDuplicateView reports a second registration of the same qualified id.
	ErrDuplicateView = errors.New("view: duplicate view contribution")
	// ErrPlacementDenied reports a placement the contribution did not declare.
	ErrPlacementDenied = errors.New("view: placement is not allowed by the contribution")
	// ErrPlacementNotServed is kept for callers compiled against the earlier
	// Pane-only host. Every declared placement is served now.
	ErrPlacementNotServed = errors.New("view: placement is not served by this host")
	// ErrUnknownInstance reports a move, duplicate, or lookup for an instance
	// id that is not live.
	ErrUnknownInstance = errors.New("view: unknown view instance")
	// ErrPageRequiresSingleton reports a Page placement for a multi-instance
	// contribution. A Page holds at most one instance of a view.
	ErrPageRequiresSingleton = errors.New("view: the page placement requires a singleton view")
	// ErrPageOccupied reports a Page move when another instance of the same
	// view already occupies the page. The source is left unchanged.
	ErrPageOccupied = errors.New("view: the view page is occupied by another instance")
	// ErrDuplicateSingleton reports a duplicate of a singleton contribution.
	ErrDuplicateSingleton = errors.New("view: only multi-instance views can be duplicated")
	// ErrInstanceClosed reports a mutation on a closed instance.
	ErrInstanceClosed = errors.New("view: the instance is closed")
	// ErrStateTooLarge reports restore state above MaxViewStateBytes.
	ErrStateTooLarge = errors.New("view: restore state exceeds the 64 KiB limit")
	// ErrClosed reports a registration or open on a closed registry.
	ErrClosed = errors.New("view: registry is closed")
)

// Registry is the host-side registry of View Contributions and live
// instances. It is safe for concurrent use.
type Registry struct {
	mu            sync.Mutex
	contributions map[string]*registered
	instances     map[string]*Instance
	singletons    map[string]*Instance
	// pages holds the instance occupying each view's Page slot. A Page is
	// single per view, not per (view, context): two contexts of one singleton
	// view still share one page.
	pages    map[string]*Instance
	next     uint64
	closed   bool
	localize func(Owner, string, string) string
}

type registered struct {
	contribution Contribution
	instances    map[*Instance]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		contributions: make(map[string]*registered),
		instances:     make(map[string]*Instance),
		singletons:    make(map[string]*Instance),
		pages:         make(map[string]*Instance),
	}
}

// SetLocalizer installs the host contribution localizer. A contribution's
// TitleKey is resolved on every Title call, so a locale change updates open
// views without reopening them.
func (r *Registry) SetLocalizer(fn func(Owner, string, string) string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.localize = fn
	r.mu.Unlock()
}

func (r *Registry) localizeTitle(owner Owner, key, literal string) string {
	if r == nil || key == "" {
		return literal
	}
	r.mu.Lock()
	fn := r.localize
	r.mu.Unlock()
	if fn == nil {
		return literal
	}
	return fn(owner, key, literal)
}

// Register publishes one View Contribution for an owner. The qualified id keeps
// plugins disjoint. The returned disposer removes the view and closes every
// instance it owns.
func (r *Registry) Register(owner Owner, declaration domainplugin.ViewContribution) (Disposer, error) {
	if r == nil {
		return nil, ErrClosed
	}
	if owner.PluginID == "" || declaration.ID == "" {
		return nil, fmt.Errorf("view: owner and view id are required")
	}
	if len(declaration.Placements) == 0 {
		return nil, fmt.Errorf("view: %s declares no placements", declaration.ID)
	}
	for _, placement := range declaration.Placements {
		if !placement.Valid() {
			return nil, fmt.Errorf("view: %s declares unknown placement %q", declaration.ID, placement)
		}
	}
	if !declaration.Instances.Valid() {
		return nil, fmt.Errorf("view: %s declares unknown instance policy %q", declaration.ID, declaration.Instances)
	}
	if declaration.Instances == "" {
		declaration.Instances = domainplugin.InstanceSingleton
	}
	qualified := domainplugin.QualifiedViewID(owner.PluginID, declaration.ID)

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	if _, exists := r.contributions[qualified]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrDuplicateView, qualified)
	}
	registeredView := &registered{
		contribution: Contribution{
			ID:         qualified,
			LocalID:    declaration.ID,
			Title:      declaration.Title,
			TitleKey:   declaration.TitleKey,
			Placements: append([]domainplugin.Placement(nil), declaration.Placements...),
			Instances:  declaration.Instances,
			Surface:    declaration.Surface,
			Entry:      declaration.Entry,
			Bridge:     append([]string(nil), declaration.Bridge...),
			Owner:      owner,
		},
		instances: make(map[*Instance]bool),
	}
	r.contributions[qualified] = registeredView
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { r.unregister(qualified) })
	}, nil
}

func (r *Registry) unregister(qualified string) {
	r.mu.Lock()
	registeredView := r.contributions[qualified]
	delete(r.contributions, qualified)
	var instances []*Instance
	if registeredView != nil {
		for instance := range registeredView.instances {
			instances = append(instances, instance)
		}
	}
	for _, instance := range instances {
		delete(r.instances, instance.id)
		delete(r.singletons, singletonKey(instance.viewID, instance.contextKey))
		if r.pages[instance.viewID] == instance {
			delete(r.pages, instance.viewID)
		}
	}
	r.mu.Unlock()
	for _, instance := range instances {
		instance.close()
	}
}

// Contribution returns one registered contribution by qualified id.
func (r *Registry) Contribution(qualified string) (Contribution, bool) {
	if r == nil {
		return Contribution{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	registeredView, ok := r.contributions[qualified]
	if !ok {
		return Contribution{}, false
	}
	return copyContribution(registeredView.contribution), true
}

// ResolveTitle returns a registered contribution's display title resolved in
// the current host locale, with the manifest literal as the fallback.
func (r *Registry) ResolveTitle(qualified string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	registeredView := r.contributions[qualified]
	if registeredView == nil {
		r.mu.Unlock()
		return "", false
	}
	contribution := registeredView.contribution
	r.mu.Unlock()
	return r.localizeTitle(contribution.Owner, contribution.TitleKey, contribution.Title), true
}

// Contributions returns every registered contribution ordered by qualified id.
func (r *Registry) Contributions() []Contribution {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Contribution, 0, len(r.contributions))
	for _, registeredView := range r.contributions {
		out = append(out, copyContribution(registeredView.contribution))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// OpenRequest asks for one View Instance.
type OpenRequest struct {
	ViewID     string
	ContextKey string
	Placement  domainplugin.Placement
	State      []byte
}

// Open returns a live View Instance for the request. A singleton contribution
// returns the existing instance for the same (viewId, contextKey) and reports
// created=false; opening it in the Page placement moves that instance into the
// page slot. A multi-instance contribution always creates one, and cannot use
// Page.
func (r *Registry) Open(request OpenRequest) (*Instance, bool, error) {
	if r == nil {
		return nil, false, ErrClosed
	}
	if request.Placement == "" {
		request.Placement = domainplugin.PlacementPane
	}
	if len(request.State) > domainworkspace.MaxViewStateBytes {
		return nil, false, ErrStateTooLarge
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false, ErrClosed
	}
	registeredView, ok := r.contributions[request.ViewID]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrUnknownView, request.ViewID)
	}
	if !request.Placement.Valid() || !registeredView.contribution.Allows(request.Placement) {
		return nil, false, fmt.Errorf("%w: %s for %s", ErrPlacementDenied, request.Placement, request.ViewID)
	}
	multi := registeredView.contribution.Instances.Multi()
	if request.Placement == domainplugin.PlacementPage {
		if multi {
			return nil, false, fmt.Errorf("%w: %s", ErrPageRequiresSingleton, request.ViewID)
		}
		if holder := r.livePage(request.ViewID); holder != nil {
			// The slot may only be entered by the instance already in it; a
			// different context's singleton would otherwise steal the page.
			key := singletonKey(request.ViewID, request.ContextKey)
			if r.singletons[key] != holder {
				return nil, false, fmt.Errorf("%w: %s", ErrPageOccupied, request.ViewID)
			}
		}
	}
	key := singletonKey(request.ViewID, request.ContextKey)
	if !multi {
		if existing := r.singletons[key]; existing != nil && !existing.isClosed() {
			if request.Placement == domainplugin.PlacementPage &&
				existing.Placement() != domainplugin.PlacementPage {
				if holder := r.livePage(request.ViewID); holder != nil && holder != existing {
					return nil, false, fmt.Errorf("%w: %s", ErrPageOccupied, request.ViewID)
				}
				existing.setPlacement(domainplugin.PlacementPage)
				r.pages[request.ViewID] = existing
			}
			return existing, false, nil
		}
	}
	r.next++
	instance := &Instance{
		registry:     r,
		id:           fmt.Sprintf("vi-%d", r.next),
		viewID:       request.ViewID,
		contextKey:   request.ContextKey,
		contribution: registeredView.contribution,
		placement:    request.Placement,
		state:        append([]byte(nil), request.State...),
	}
	r.instances[instance.id] = instance
	if !multi {
		r.singletons[key] = instance
	}
	if request.Placement == domainplugin.PlacementPage {
		r.pages[request.ViewID] = instance
	}
	registeredView.instances[instance] = true
	return instance, true, nil
}

// Move relocates a live instance to another declared placement. The instance
// identity, context, and state are unchanged. The move is atomic: an
// undeclared placement, a Page occupied by another instance, or a Page for a
// multi-instance view leaves the source exactly as it was.
func (r *Registry) Move(id string, target domainplugin.Placement) (*Instance, error) {
	if r == nil {
		return nil, ErrClosed
	}
	if !target.Valid() {
		return nil, fmt.Errorf("%w: %s", ErrPlacementDenied, target)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	instance, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownInstance, id)
	}
	if instance.isClosed() {
		return nil, ErrInstanceClosed
	}
	if !instance.contribution.Allows(target) {
		return nil, fmt.Errorf("%w: %s for %s", ErrPlacementDenied, target, instance.viewID)
	}
	if target == instance.Placement() {
		if target == domainplugin.PlacementPage {
			r.pages[instance.viewID] = instance
		}
		return instance, nil
	}
	if target == domainplugin.PlacementPage {
		if instance.contribution.Instances.Multi() {
			return nil, fmt.Errorf("%w: %s", ErrPageRequiresSingleton, instance.viewID)
		}
		if holder := r.livePage(instance.viewID); holder != nil && holder != instance {
			return nil, fmt.Errorf("%w: %s", ErrPageOccupied, instance.viewID)
		}
	}
	instance.setPlacement(target)
	if target == domainplugin.PlacementPage {
		r.pages[instance.viewID] = instance
	} else if r.pages[instance.viewID] == instance {
		delete(r.pages, instance.viewID)
	}
	return instance, nil
}

// Duplicate creates an independent instance of a multi-instance view. The new
// instance shares the view and context but copies no state. target may be
// empty to use the source's placement.
func (r *Registry) Duplicate(id string, target domainplugin.Placement) (*Instance, error) {
	if r == nil {
		return nil, ErrClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	source, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownInstance, id)
	}
	if source.isClosed() {
		return nil, ErrInstanceClosed
	}
	if !source.contribution.Instances.Multi() {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateSingleton, source.viewID)
	}
	if target == "" {
		target = source.Placement()
	}
	if !target.Valid() || !source.contribution.Allows(target) {
		return nil, fmt.Errorf("%w: %s for %s", ErrPlacementDenied, target, source.viewID)
	}
	if target == domainplugin.PlacementPage {
		return nil, fmt.Errorf("%w: %s", ErrPageRequiresSingleton, source.viewID)
	}
	registeredView := r.contributions[source.viewID]
	if registeredView == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownView, source.viewID)
	}
	r.next++
	instance := &Instance{
		registry:     r,
		id:           fmt.Sprintf("vi-%d", r.next),
		viewID:       source.viewID,
		contextKey:   source.contextKey,
		contribution: source.contribution,
		placement:    target,
	}
	r.instances[instance.id] = instance
	registeredView.instances[instance] = true
	return instance, nil
}

// PageInstance returns the instance occupying a view's Page slot.
func (r *Registry) PageInstance(viewID string) (*Instance, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	instance := r.livePage(viewID)
	return instance, instance != nil
}

func (r *Registry) livePage(viewID string) *Instance {
	instance := r.pages[viewID]
	if instance == nil || instance.isClosed() {
		return nil
	}
	return instance
}

// SingletonForContext returns the live singleton instance for a view and
// context. Multi-instance contributions have no singleton entry.
func (r *Registry) SingletonForContext(viewID, contextKey string) (*Instance, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	instance := r.singletons[singletonKey(viewID, contextKey)]
	if instance == nil || instance.isClosed() {
		return nil, false
	}
	return instance, true
}

// Instance returns one live instance by id.
func (r *Registry) Instance(id string) (*Instance, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	instance, ok := r.instances[id]
	return instance, ok
}

// Instances returns every live instance.
func (r *Registry) Instances() []*Instance {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Instance, 0, len(r.instances))
	for _, instance := range r.instances {
		out = append(out, instance)
	}
	return out
}

// Close closes every instance and rejects later registrations and opens.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	instances := make([]*Instance, 0, len(r.instances))
	for _, instance := range r.instances {
		instances = append(instances, instance)
	}
	r.instances = make(map[string]*Instance)
	r.singletons = make(map[string]*Instance)
	r.pages = make(map[string]*Instance)
	r.contributions = make(map[string]*registered)
	r.mu.Unlock()
	for _, instance := range instances {
		instance.close()
	}
}

// Instance is one live presentation for a fixed view and context. Its identity
// and context never change; only the bounded restore state may move.
type Instance struct {
	registry     *Registry
	id           string
	viewID       string
	contextKey   string
	contribution Contribution
	placement    domainplugin.Placement

	mu     sync.Mutex
	state  []byte
	closed bool
}

// ID returns the stable instance identity. It survives a move between
// placements.
func (i *Instance) ID() string { return i.id }

// ViewID returns the qualified view id.
func (i *Instance) ViewID() string { return i.viewID }

// ContextKey returns the context fixed at creation.
func (i *Instance) ContextKey() string { return i.contextKey }

// Title returns the contribution's host display title in the current host
// locale, with the manifest literal as the fallback.
func (i *Instance) Title() string {
	if i == nil {
		return ""
	}
	return i.registry.localizeTitle(i.contribution.Owner,
		i.contribution.TitleKey, i.contribution.Title)
}

// Owner returns the owning plugin identity.
func (i *Instance) Owner() Owner { return i.contribution.Owner }

// Contribution returns the declaration that created the instance.
func (i *Instance) Contribution() Contribution { return copyContribution(i.contribution) }

// Placement returns the current placement.
func (i *Instance) Placement() domainplugin.Placement {
	if i == nil {
		return ""
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.placement
}

func (i *Instance) setPlacement(placement domainplugin.Placement) {
	i.mu.Lock()
	i.placement = placement
	i.mu.Unlock()
}

// Descriptor returns the bounded restore descriptor for this instance.
func (i *Instance) Descriptor() domainworkspace.ViewPaneDescriptor {
	i.mu.Lock()
	defer i.mu.Unlock()
	return domainworkspace.ViewPaneDescriptor{
		ViewID:     i.viewID,
		ContextKey: i.contextKey,
		Placement:  domainworkspace.ViewPlacement(i.placement),
		State:      append([]byte(nil), i.state...),
	}
}

// State returns a copy of the bounded restore state.
func (i *Instance) State() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]byte(nil), i.state...)
}

// SetState replaces the restore state. State above the 64 KiB cap is rejected
// and leaves the previous value.
func (i *Instance) SetState(state []byte) error {
	if i == nil {
		return ErrInstanceClosed
	}
	if len(state) > domainworkspace.MaxViewStateBytes {
		return ErrStateTooLarge
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return ErrInstanceClosed
	}
	i.state = append([]byte(nil), state...)
	return nil
}

// Closed reports whether the instance was disposed.
func (i *Instance) Closed() bool {
	if i == nil {
		return true
	}
	return i.isClosed()
}

func (i *Instance) isClosed() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closed
}

// Close disposes the instance and removes it from the registry. It is
// idempotent.
func (i *Instance) Close() error {
	if i == nil {
		return nil
	}
	if i.registry != nil {
		i.registry.remove(i)
	}
	i.close()
	return nil
}

func (i *Instance) close() {
	i.mu.Lock()
	i.closed = true
	i.mu.Unlock()
}

func (r *Registry) remove(instance *Instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[instance.id]; !ok {
		return
	}
	delete(r.instances, instance.id)
	if !instance.contribution.Instances.Multi() {
		key := singletonKey(instance.viewID, instance.contextKey)
		if r.singletons[key] == instance {
			delete(r.singletons, key)
		}
	}
	if r.pages[instance.viewID] == instance {
		delete(r.pages, instance.viewID)
	}
	if registeredView := r.contributions[instance.viewID]; registeredView != nil {
		delete(registeredView.instances, instance)
	}
}

func singletonKey(viewID, contextKey string) string {
	return viewID + "\x00" + contextKey
}

func copyContribution(contribution Contribution) Contribution {
	contribution.Placements = append([]domainplugin.Placement(nil), contribution.Placements...)
	contribution.Bridge = append([]string(nil), contribution.Bridge...)
	return contribution
}
