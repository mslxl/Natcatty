package state

import (
	"context"
	"strconv"
	"strings"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Groups is the Host Group use-case seam. The application Service implements
// it; tests provide a fake.
type Groups interface {
	ListGroups(ctx context.Context) ([]domainvault.Group, error)
	CreateGroup(ctx context.Context, draft domainvault.GroupDraft) (domainvault.Group, error)
	UpdateGroup(ctx context.Context, path string, draft domainvault.GroupDraft) (domainvault.Group, error)
	DeleteGroup(ctx context.Context, path string) error
	AssignHostGroup(ctx context.Context, id model.HostID, groupPath string) error
	// AssignHostsGroup moves several Hosts in one all-or-nothing operation.
	AssignHostsGroup(ctx context.Context, ids []model.HostID, groupPath string) error
}

// ProxyProfiles is the Proxy Profile use-case seam.
type ProxyProfiles interface {
	ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error)
	CreateProxyProfile(ctx context.Context, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error)
	UpdateProxyProfile(ctx context.Context, id string, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error)
	DeleteProxyProfile(ctx context.Context, id string) error
}

// KnownHosts is the Known Host use-case seam.
type KnownHosts interface {
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
	DeleteKnownHost(ctx context.Context, id string) error
}

// DragItem is the value a Host card or row drags. A Host Group row, or the
// Vault root, accepts it and moves every Host it carries. HostID is the Host
// the pointer pressed, so a receiver can lead with it; HostIDs is the whole
// marquee selection when the pressed Host is part of one.
type DragItem struct {
	HostID  model.HostID
	HostIDs []model.HostID
}

// HostDrag returns the drag payload for one Host.
func HostDrag(id model.HostID) DragItem { return DragItem{HostID: id, HostIDs: []model.HostID{id}} }

// HostsDrag returns the drag payload for a marquee selection, the pressed Host
// first so a drop can report the same lead the user grabbed.
func HostsDrag(pressed model.HostID, ids []model.HostID) DragItem {
	ordered := make([]model.HostID, 0, len(ids)+1)
	ordered = append(ordered, pressed)
	for _, id := range ids {
		if id != pressed {
			ordered = append(ordered, id)
		}
	}
	return DragItem{HostID: pressed, HostIDs: ordered}
}

// Hosts returns every Host the drag carries, the pressed Host included. A
// payload built before multi-selection carried only HostID.
func (d DragItem) Hosts() []model.HostID {
	if len(d.HostIDs) > 0 {
		return d.HostIDs
	}
	if d.HostID != "" {
		return []model.HostID{d.HostID}
	}
	return nil
}

// DragPayload is what a drop target that moves Hosts accepts: a Host card or
// row drag (DragItem) or the box selection swept from the page background
// (MarqueeDrag). Both answer with the Hosts the drop moves.
type DragPayload interface {
	DropHosts() []model.HostID
}

// DropHosts returns the Hosts a Host drag moves.
func (d DragItem) DropHosts() []model.HostID { return d.Hosts() }

// MarqueeDrag is the value the Hosts page background drags while the pointer
// sweeps a selection box over the Hosts. Host cards and rows accept it and add
// themselves to the selection. Swept reads the selection at drop time, since
// the box grows after the press that started it.
type MarqueeDrag struct {
	Swept func() []model.HostID
}

// DropHosts returns the Hosts the box selected. A receiver that is not a Group
// never asks, so dropping the box outside a Group moves nothing.
func (m MarqueeDrag) DropHosts() []model.HostID {
	if m.Swept == nil {
		return nil
	}
	return m.Swept()
}

// WithGroups gives the Vault surface Host Group management and defaults.
func WithGroups(groups Groups) Option {
	return func(s *State) { s.groups = groups }
}

// WithProxyProfiles gives the Vault surface Proxy Profile management.
func WithProxyProfiles(proxies ProxyProfiles) Option {
	return func(s *State) { s.proxies = proxies }
}

// WithKnownHosts gives the Vault surface the Known Host trust list.
func WithKnownHosts(known KnownHosts) Option {
	return func(s *State) { s.knownHosts = known }
}

// GroupForm is the editable text of one Host Group. Port stays a string so a
// malformed value is rejected instead of silently becoming zero.
type GroupForm struct {
	// ID is the edited group's stable identity, empty while creating.
	// OriginalPath is its path when the editor opened, for the header and for
	// deciding whether the path changed.
	ID               string
	OriginalPath     string
	Path             string
	Notes            string
	Username         string
	Port             string
	Protocol         string
	AuthMethod       string
	IdentityID       string
	KeyID            string
	UseSSHAgent      bool
	RequiresMFA      bool
	ProxyProfileID   string
	NetworkProfileID string
	// HostChain is the group's default jump chain. Its hops are edited as
	// checkboxes; ToggleGroupJumpHost keeps their order.
	HostChain        *domainvault.HostChain
	LegacyAlgorithms bool
	SkipEcdsaHostKey bool
	Algorithms       *domainvault.AlgorithmOverrides
	// X11Forwarding and X11WarningAcknowledged are the group's X11 default for
	// member SSH Hosts. A member Host that owns an X11 choice keeps it.
	X11Forwarding          bool
	X11WarningAcknowledged bool
}

// ProxyForm is the editable text of one Proxy Profile.
type ProxyForm struct {
	ID       string
	Label    string
	Type     string
	Host     string
	Port     string
	Command  string
	Username string
}

// Groups returns the listed Host Groups in repository order.
func (s *State) Groups() []domainvault.Group { return s.groupItems }

// GroupCount returns the number of listed Host Groups.
func (s *State) GroupCount() int { return len(s.groupItems) }

// GroupMemberCount returns the number of Hosts at or below one group path.
func (s *State) GroupMemberCount(path string) int {
	count := 0
	for _, host := range s.items {
		if domainvault.IsGroupAtOrBelow(host.Group, path) {
			count++
		}
	}
	return count
}

// RootHostCount returns the number of Hosts with no Host Group.
func (s *State) RootHostCount() int {
	count := 0
	for _, host := range s.items {
		if domainvault.NormalizeGroup(host.Group) == "" {
			count++
		}
	}
	return count
}

// EditingGroup reports whether the Host Group editor is open.
func (s *State) EditingGroup() bool { return s.editingGroup }

// GroupForm returns the Host Group editor's form. It is valid while
// EditingGroup.
func (s *State) GroupForm() *GroupForm { return &s.groupForm }

// PendingDeleteGroup returns the Host Group awaiting delete confirmation.
func (s *State) PendingDeleteGroup() string { return s.pendingDeleteGroup }

// BeginCreateGroup opens the Host Group editor with a new group.
func (s *State) BeginCreateGroup() {
	s.closeEditors()
	s.editingGroup = true
	s.groupForm = GroupForm{Protocol: string(domainvault.ProtocolSSH)}
	s.problem = ProblemNone
	s.redraw()
}

// BeginEditGroup opens the Host Group editor with a listed group's defaults.
func (s *State) BeginEditGroup(path string) bool {
	group, ok := s.findGroup(path)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingGroup = true
	s.groupForm = GroupForm{
		ID:                     group.ID,
		OriginalPath:           group.Path,
		Path:                   group.Path,
		Notes:                  group.Notes,
		Username:               group.Username,
		Port:                   groupPortText(group),
		Protocol:               string(group.Protocol),
		AuthMethod:             string(group.AuthMethod),
		IdentityID:             string(group.IdentityID),
		KeyID:                  string(group.KeyID),
		UseSSHAgent:            group.UseSSHAgent,
		RequiresMFA:            group.RequiresMFA,
		ProxyProfileID:         group.ProxyProfileID,
		NetworkProfileID:       group.NetworkProfileID,
		HostChain:              domainvault.CloneHostChain(group.HostChain),
		LegacyAlgorithms:       group.LegacyAlgorithms,
		SkipEcdsaHostKey:       group.SkipEcdsaHostKey,
		Algorithms:             domainvault.CloneAlgorithms(group.Algorithms),
		X11Forwarding:          group.X11Forwarding,
		X11WarningAcknowledged: group.X11WarningAcknowledged,
	}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelGroupEdit closes the Host Group editor without saving.
func (s *State) CancelGroupEdit() {
	s.editingGroup = false
	s.groupForm = GroupForm{}
	s.problem = ProblemNone
	s.redraw()
}

// SaveGroup validates and persists the Host Group form.
func (s *State) SaveGroup() error {
	if s.groups == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	draft, problem := s.groupDraft()
	if problem != ProblemNone {
		s.problem = problem
		s.redraw()
		return nil
	}

	var err error
	if s.groupForm.ID == "" {
		_, err = s.groups.CreateGroup(context.Background(), draft)
	} else {
		_, err = s.groups.UpdateGroup(context.Background(), s.groupForm.ID, draft)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingGroup = false
	s.groupForm = GroupForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteGroup opens the confirmation for one Host Group.
func (s *State) RequestDeleteGroup(path string) {
	s.pendingDeleteGroup = path
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteGroup closes the Host Group delete confirmation.
func (s *State) CancelDeleteGroup() {
	s.pendingDeleteGroup = ""
	s.redraw()
}

// ConfirmDeleteGroup deletes the group awaiting confirmation. Member Hosts
// survive and move to the Vault root; no Host is deleted.
func (s *State) ConfirmDeleteGroup() error {
	if s.pendingDeleteGroup == "" {
		return nil
	}
	path := s.pendingDeleteGroup
	s.pendingDeleteGroup = ""
	if s.groups == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	group, ok := s.findGroup(path)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return nil
	}
	if err := s.groups.DeleteGroup(context.Background(), group.ID); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// MoveHostToGroup moves one Host into a group, or to the Vault root when path
// is empty. Drag and drop calls this; the Host keeps every other field.
func (s *State) MoveHostToGroup(id model.HostID, path string) bool {
	return s.MoveHostsToGroup([]model.HostID{id}, path)
}

// MoveHostsToGroup moves every Host in a marquee selection into a group, or to
// the Vault root when path is empty. The drop is one assignment: an unknown
// group or a failed write leaves every Host where it was. The selection is
// cleared only after the Vault reloads without the move.
func (s *State) MoveHostsToGroup(ids []model.HostID, path string) bool {
	if s.groups == nil {
		s.problem = ProblemInternal
		s.redraw()
		return false
	}
	groupID := ""
	if path != "" {
		group, ok := s.findGroup(path)
		if !ok {
			s.problem = ProblemNotFound
			s.redraw()
			return false
		}
		groupID = group.ID
	}
	if err := s.groups.AssignHostsGroup(context.Background(), ids, groupID); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return false
	}
	if err := s.load(); err != nil {
		s.redraw()
		return false
	}
	s.clearSelection()
	s.problem = ProblemNone
	s.redraw()
	return true
}

// Chosen reports whether a Host is in the marquee selection.
func (s *State) Chosen(id model.HostID) bool { return s.chosen[id] }

// ChosenCount returns how many Hosts the marquee selection holds.
func (s *State) ChosenCount() int { return len(s.chosen) }

// SelectedHostIDs returns the marquee selection in Vault list order, so a drop
// applies Hosts in the order the user sees them.
func (s *State) SelectedHostIDs() []model.HostID {
	ids := make([]model.HostID, 0, len(s.chosen))
	for _, host := range s.items {
		if s.chosen[host.ID] {
			ids = append(ids, host.ID)
		}
	}
	return ids
}

// BeginMarquee starts a box selection. Additive keeps the current selection,
// as a modifier press does; otherwise the box replaces it. Repeated calls
// while the drag lasts change nothing.
func (s *State) BeginMarquee(additive bool) {
	if s.marqueeActive {
		return
	}
	s.marqueeActive = true
	s.marqueeAdditive = additive
	s.marqueeBase = make(map[model.HostID]bool, len(s.chosen))
	for id := range s.chosen {
		s.marqueeBase[id] = true
	}
	s.marqueeBaseSelected = s.selected
	if !additive {
		s.chosen = nil
		s.selected = ""
	}
	s.redraw()
}

// MarqueeOver adds a Host the box touches to the selection. The last Host
// touched is the lead for single-Host actions. It reports whether the
// selection changed.
func (s *State) MarqueeOver(id model.HostID) bool {
	if !s.marqueeActive {
		return false
	}
	_, ok := s.find(id)
	if !ok {
		return false
	}
	changed := !s.chosen[id] || s.selected != id
	if s.chosen == nil {
		s.chosen = map[model.HostID]bool{}
	}
	s.chosen[id] = true
	s.selected = id
	if changed {
		s.redraw()
	}
	return changed
}

// SweepMarquee sets an in-flight box selection to every Host the rectangle
// encloses, not just the Host under the pointer. Additive drags keep the
// selection they started from. It reports whether the selection changed.
func (s *State) SweepMarquee(ids []model.HostID) bool {
	if !s.marqueeActive {
		return false
	}
	next := make(map[model.HostID]bool, len(s.marqueeBase)+len(ids))
	if s.marqueeAdditive {
		for id, on := range s.marqueeBase {
			if on {
				next[id] = true
			}
		}
	}
	for _, id := range ids {
		if _, ok := s.find(id); ok {
			next[id] = true
		}
	}
	if len(next) == 0 {
		next = nil
	}
	lead := model.HostID("")
	for _, host := range s.items {
		if next[host.ID] {
			lead = host.ID
		}
	}
	changed := lead != s.selected || len(next) != len(s.chosen)
	if !changed {
		for id := range next {
			if !s.chosen[id] {
				changed = true
				break
			}
		}
	}
	s.chosen = next
	s.selected = lead
	if changed {
		s.redraw()
	}
	return changed
}

// MarqueeActive reports whether a box selection drag is in progress.
func (s *State) MarqueeActive() bool { return s.marqueeActive }

// EndMarquee finishes a box selection, keeping what it chose.
func (s *State) EndMarquee() {
	if !s.marqueeActive {
		return
	}
	s.marqueeActive = false
	s.marqueeAdditive = false
	s.marqueeBase = nil
	s.marqueeBaseSelected = ""
	s.redraw()
}

// CancelMarquee abandons a box selection, restoring the selection it started
// from.
func (s *State) CancelMarquee() {
	if !s.marqueeActive {
		return
	}
	s.chosen = s.marqueeBase
	s.selected = s.marqueeBaseSelected
	s.marqueeActive = false
	s.marqueeAdditive = false
	s.marqueeBase = nil
	s.marqueeBaseSelected = ""
	s.redraw()
}

// ClearSelection empties the marquee selection.
func (s *State) ClearSelection() {
	s.clearSelection()
	s.redraw()
}

func (s *State) clearSelection() {
	s.chosen = nil
}

// ProxyProfiles returns the listed Proxy Profiles in repository order.
func (s *State) ProxyProfiles() []domainvault.ProxyProfile { return s.proxyItems }

// ProxyProfileCount returns the number of listed Proxy Profiles.
func (s *State) ProxyProfileCount() int { return len(s.proxyItems) }

// ProxyProfileByID returns one listed Proxy Profile.
func (s *State) ProxyProfileByID(id string) (domainvault.ProxyProfile, bool) {
	for _, profile := range s.proxyItems {
		if profile.ID == id {
			return profile, true
		}
	}
	return domainvault.ProxyProfile{}, false
}

// ProxyProfileLabel returns a profile's display label, or the raw reference
// when the profile no longer exists, so an editor can show a dangling soft
// reference instead of pretending nothing is selected.
func (s *State) ProxyProfileLabel(id string) string {
	if id == "" {
		return ""
	}
	if profile, ok := s.ProxyProfileByID(id); ok {
		return profile.Label
	}
	return id
}

// EditingProxyProfile reports whether the Proxy Profile editor is open.
func (s *State) EditingProxyProfile() bool { return s.editingProxy }

// ProxyForm returns the Proxy Profile editor's form. It is valid while
// EditingProxyProfile.
func (s *State) ProxyForm() *ProxyForm { return &s.proxyForm }

// PendingDeleteProxyProfile returns the Proxy Profile awaiting confirmation.
func (s *State) PendingDeleteProxyProfile() string { return s.pendingDeleteProxy }

// BeginCreateProxyProfile opens the editor with a SOCKS5 profile.
func (s *State) BeginCreateProxyProfile() {
	s.closeEditors()
	s.editingProxy = true
	s.proxyForm = ProxyForm{Type: string(domainvault.ProxySOCKS5), Port: "1080"}
	s.problem = ProblemNone
	s.redraw()
}

// BeginEditProxyProfile opens the editor with a listed profile.
func (s *State) BeginEditProxyProfile(id string) bool {
	profile, ok := s.ProxyProfileByID(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingProxy = true
	s.proxyForm = ProxyForm{
		ID:       profile.ID,
		Label:    profile.Label,
		Type:     string(profile.Config.Type),
		Host:     profile.Config.Host,
		Port:     proxyPortText(profile.Config.Port),
		Command:  profile.Config.Command,
		Username: profile.Config.Username,
	}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelProxyEdit closes the Proxy Profile editor without saving.
func (s *State) CancelProxyEdit() {
	s.editingProxy = false
	s.proxyForm = ProxyForm{}
	s.problem = ProblemNone
	s.redraw()
}

// SaveProxyProfile validates and persists the Proxy Profile form.
func (s *State) SaveProxyProfile() error {
	if s.proxies == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	draft, problem := s.proxyDraft()
	if problem != ProblemNone {
		s.problem = problem
		s.redraw()
		return nil
	}

	var err error
	if s.proxyForm.ID == "" {
		_, err = s.proxies.CreateProxyProfile(context.Background(), draft)
	} else {
		_, err = s.proxies.UpdateProxyProfile(context.Background(), s.proxyForm.ID, draft)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingProxy = false
	s.proxyForm = ProxyForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteProxyProfile opens the confirmation for one profile.
func (s *State) RequestDeleteProxyProfile(id string) {
	s.pendingDeleteProxy = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteProxyProfile closes the Proxy Profile delete confirmation.
func (s *State) CancelDeleteProxyProfile() {
	s.pendingDeleteProxy = ""
	s.redraw()
}

// ConfirmDeleteProxyProfile deletes the profile awaiting confirmation. Hosts
// and Host Groups that referenced it survive with the reference cleared.
func (s *State) ConfirmDeleteProxyProfile() error {
	if s.pendingDeleteProxy == "" {
		return nil
	}
	id := s.pendingDeleteProxy
	s.pendingDeleteProxy = ""
	if s.proxies == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.proxies.DeleteProxyProfile(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// KnownHosts returns the recorded trust decisions in display order.
func (s *State) KnownHosts() []domainconn.KnownHost { return s.knownItems }

// InvalidateKnownHosts marks the trust list stale after a decision made off the
// UI thread, such as a host-key prompt answered during a connection. It is safe
// to call from any goroutine; FlushKnownHosts applies the change on the UI
// thread, following the same pattern as Test outcomes.
func (s *State) InvalidateKnownHosts() {
	s.knownMu.Lock()
	s.knownStale = true
	s.knownMu.Unlock()
	s.redraw()
}

// FlushKnownHosts reloads the trust list when InvalidateKnownHosts marked it
// stale. The view calls it on the UI thread before rendering.
func (s *State) FlushKnownHosts() {
	s.knownMu.Lock()
	stale := s.knownStale
	s.knownStale = false
	s.knownMu.Unlock()
	if !stale {
		return
	}
	_ = s.loadKnownHosts()
}

// loadKnownHosts replaces the listed Known Hosts from the seam. Reload and the
// off-thread invalidation both go through it so the two paths cannot drift.
func (s *State) loadKnownHosts() error {
	if s.knownHosts == nil {
		return nil
	}
	known, err := s.knownHosts.ListKnownHosts(context.Background())
	if err != nil {
		s.problem = ProblemInternal
		return err
	}
	s.knownItems = known
	return nil
}

// KnownHostCount returns the number of recorded Known Hosts.
func (s *State) KnownHostCount() int { return len(s.knownItems) }

// PendingDeleteKnownHost returns the Known Host awaiting confirmation.
func (s *State) PendingDeleteKnownHost() string { return s.pendingDeleteKnown }

// RequestDeleteKnownHost opens the confirmation for one Known Host.
func (s *State) RequestDeleteKnownHost(id string) {
	s.pendingDeleteKnown = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteKnownHost closes the Known Host confirmation.
func (s *State) CancelDeleteKnownHost() {
	s.pendingDeleteKnown = ""
	s.redraw()
}

// ConfirmDeleteKnownHost revokes one trust record. It never touches a Host.
func (s *State) ConfirmDeleteKnownHost() error {
	if s.pendingDeleteKnown == "" {
		return nil
	}
	id := s.pendingDeleteKnown
	s.pendingDeleteKnown = ""
	if s.knownHosts == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.knownHosts.DeleteKnownHost(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// KnownHostByID returns one listed trust record.
func (s *State) KnownHostByID(id string) (domainconn.KnownHost, bool) {
	for _, record := range s.knownItems {
		if record.ID == id {
			return record, true
		}
	}
	return domainconn.KnownHost{}, false
}

// EffectiveHost returns a Host with its Host Group defaults applied and its
// Proxy Profile materialized. The list and the Host editor show this resolved
// view; the stored Host itself is unchanged.
func (s *State) EffectiveHost(id model.HostID) (domainvault.Host, bool) {
	host, ok := s.find(id)
	if !ok {
		return domainvault.Host{}, false
	}
	effective := domainvault.ApplyGroupDefaults(host,
		domainvault.ResolveGroupDefaults(s.groupItems, host.Group))
	// A Host that left Port empty inherits its group's port, and without one
	// the protocol default is the endpoint a connection would use.
	effective.Port = domainvault.EffectivePort(effective.Protocol, effective.Port)
	if effective.Proxy == nil && effective.ProxyProfileID != "" {
		if profile, ok := s.ProxyProfileByID(effective.ProxyProfileID); ok {
			effective.Proxy = domainvault.CloneProxy(&profile.Config)
		}
	}
	return effective, true
}

// EffectiveItems returns every Host with group defaults applied. It is the
// list's display view, so an inherited default is visible on its members.
func (s *State) EffectiveItems() []domainvault.Host {
	items := make([]domainvault.Host, 0, len(s.items))
	for _, host := range s.items {
		effective, ok := s.EffectiveHost(host.ID)
		if !ok {
			continue
		}
		items = append(items, effective)
	}
	return items
}

func (s *State) findGroup(path string) (domainvault.Group, bool) {
	normalized := domainvault.NormalizeGroup(path)
	for _, group := range s.groupItems {
		if group.Path == normalized {
			return group, true
		}
	}
	return domainvault.Group{}, false
}

func (s *State) groupDraft() (domainvault.GroupDraft, Problem) {
	port := 0
	if trimmed := strings.TrimSpace(s.groupForm.Port); trimmed != "" {
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return domainvault.GroupDraft{}, ProblemPortInvalid
		}
		port = parsed
	}
	return domainvault.GroupDraft{
		Path:                   s.groupForm.Path,
		Notes:                  s.groupForm.Notes,
		Username:               s.groupForm.Username,
		Port:                   port,
		Protocol:               domainvault.Protocol(s.groupForm.Protocol),
		AuthMethod:             domainvault.HostAuthMethod(s.groupForm.AuthMethod),
		IdentityID:             model.IdentityID(s.groupForm.IdentityID),
		KeyID:                  model.KeyID(s.groupForm.KeyID),
		UseSSHAgent:            s.groupForm.UseSSHAgent,
		RequiresMFA:            s.groupForm.RequiresMFA,
		ProxyProfileID:         s.groupForm.ProxyProfileID,
		NetworkProfileID:       s.groupForm.NetworkProfileID,
		HostChain:              domainvault.CloneHostChain(s.groupForm.HostChain),
		LegacyAlgorithms:       s.groupForm.LegacyAlgorithms,
		SkipEcdsaHostKey:       s.groupForm.SkipEcdsaHostKey,
		Algorithms:             domainvault.CloneAlgorithms(s.groupForm.Algorithms),
		X11Forwarding:          s.groupForm.X11Forwarding,
		X11WarningAcknowledged: s.groupForm.X11WarningAcknowledged && s.groupForm.X11Forwarding,
	}, ProblemNone
}

// InheritedValueSource names where an editor's effective value comes from.
// The editors show the resolved value read-only and point at its owner, so an
// inherited Host Group default is never mistaken for the record's own value.
type InheritedValueSource string

const (
	// InheritedValueSourceNone means neither the record nor an ancestor sets
	// the value.
	InheritedValueSourceNone InheritedValueSource = ""
	// InheritedValueSourceRecord means the record being edited holds the
	// value: the Host's own selection or the edited Host Group's default.
	InheritedValueSourceRecord InheritedValueSource = "record"
	// InheritedValueSourceInherited means the value comes from a Host Group:
	// a Host's group, or an ancestor of the edited Host Group.
	InheritedValueSourceInherited InheritedValueSource = "inherited"
)

// HostProxyProfileSource reports the Host editor's effective Proxy Profile:
// the Host's own reference, the Host Group default, or nothing. A manual Proxy
// already owns the route, so the group profile would not be used and is not
// reported as inherited.
func (s *State) HostProxyProfileSource() (InheritedValueSource, string, string) {
	if id := strings.TrimSpace(s.form.ProxyProfileID); id != "" {
		return InheritedValueSourceRecord, "", id
	}
	if s.proxy != nil ||
		domainvault.Protocol(s.form.Protocol) == domainvault.ProtocolLocal {
		return InheritedValueSourceNone, "", ""
	}
	group := domainvault.NormalizeGroup(s.form.Group)
	if id := domainvault.ResolveGroupDefaults(s.groupItems, group).ProxyProfileID; id != "" {
		return InheritedValueSourceInherited,
			s.groupDefaultOwner(group, func(g domainvault.Group) bool {
				return g.ProxyProfileID != ""
			}), id
	}
	return InheritedValueSourceNone, "", ""
}

// GroupProxyProfileSource reports the Host Group editor's effective Proxy
// Profile: the group's own reference, an ancestor's default, or nothing.
func (s *State) GroupProxyProfileSource() (InheritedValueSource, string, string) {
	if id := strings.TrimSpace(s.groupForm.ProxyProfileID); id != "" {
		return InheritedValueSourceRecord, "", id
	}
	parent := domainvault.ParentGroupPath(s.groupForm.Path)
	if id := domainvault.ResolveGroupDefaults(s.groupItems, parent).ProxyProfileID; id != "" {
		return InheritedValueSourceInherited,
			s.groupDefaultOwner(parent, func(g domainvault.Group) bool {
				return g.ProxyProfileID != ""
			}), id
	}
	return InheritedValueSourceNone, "", ""
}

// HostJumpChainSource reports the Host editor's effective jump chain: the
// Host's own hops, the Host Group default, or nothing. The returned IDs are in
// connection order, first hop closest to the client.
func (s *State) HostJumpChainSource() (InheritedValueSource, string, []model.HostID) {
	if ids := s.hostChain.IDs(); len(ids) > 0 {
		return InheritedValueSourceRecord, "", ids
	}
	if domainvault.Protocol(s.form.Protocol) == domainvault.ProtocolLocal {
		return InheritedValueSourceNone, "", nil
	}
	group := domainvault.NormalizeGroup(s.form.Group)
	if chain := domainvault.ResolveGroupDefaults(s.groupItems, group).HostChain; chain != nil {
		return InheritedValueSourceInherited,
			s.groupDefaultOwner(group, func(g domainvault.Group) bool {
				return len(g.HostChain.IDs()) > 0
			}), chain.IDs()
	}
	return InheritedValueSourceNone, "", nil
}

// GroupJumpChainSource reports the Host Group editor's effective jump chain:
// the group's own hops, an ancestor's default, or nothing.
func (s *State) GroupJumpChainSource() (InheritedValueSource, string, []model.HostID) {
	if len(s.groupForm.HostChain.IDs()) > 0 {
		return InheritedValueSourceRecord, "", s.groupForm.HostChain.IDs()
	}
	parent := domainvault.ParentGroupPath(s.groupForm.Path)
	if chain := domainvault.ResolveGroupDefaults(s.groupItems, parent).HostChain; chain != nil {
		return InheritedValueSourceInherited,
			s.groupDefaultOwner(parent, func(g domainvault.Group) bool {
				return len(g.HostChain.IDs()) > 0
			}), chain.IDs()
	}
	return InheritedValueSourceNone, "", nil
}

// HostX11Source reports the Host editor's effective X11 preference: the
// Host's own explicit choice, the Host Group default, or nothing. The value is
// effective only for plain SSH; another transport never inherits it. An
// explicit Host off is a record value, not an inheritance.
func (s *State) HostX11Source() (InheritedValueSource, string, bool) {
	if s.form.X11ForwardingSet {
		return InheritedValueSourceRecord, "", s.form.X11Forwarding
	}
	group := domainvault.NormalizeGroup(s.form.Group)
	defaults := domainvault.ResolveGroupDefaults(s.groupItems, group)
	protocol := domainvault.Protocol(s.form.Protocol)
	if protocol == "" {
		protocol = defaults.Protocol
	}
	if protocol == "" {
		protocol = domainvault.ProtocolSSH
	}
	if protocol == domainvault.ProtocolSSH && defaults.X11Enabled() {
		return InheritedValueSourceInherited,
			s.groupDefaultOwner(group, func(g domainvault.Group) bool {
				return g.X11Forwarding && g.X11WarningAcknowledged
			}), true
	}
	return InheritedValueSourceNone, "", false
}

// groupDefaultOwner returns the deepest group at or above path whose defaults
// hold the value selected by holds. Every inheritance source uses it so an
// inherited value names the same owner regardless of which default it is.
func (s *State) groupDefaultOwner(path string, holds func(domainvault.Group) bool) string {
	owner := ""
	for _, ancestor := range domainvault.GroupAncestors(path) {
		for _, group := range s.groupItems {
			if group.Path == ancestor && holds(group) {
				owner = ancestor
				break
			}
		}
	}
	return owner
}

// HostLabel returns a Host's display label, or the raw reference when the Host
// is no longer listed, so a dangling jump hop stays visible.
func (s *State) HostLabel(id model.HostID) string {
	if id == "" {
		return ""
	}
	for _, host := range s.items {
		if host.ID == id {
			if host.Label != "" {
				return host.Label
			}
			return string(host.ID)
		}
	}
	return string(id)
}

// JumpHostLabels returns the display labels of a chain's hops in order.
func (s *State) JumpHostLabels(ids []model.HostID) []string {
	labels := make([]string, 0, len(ids))
	for _, id := range ids {
		labels = append(labels, s.HostLabel(id))
	}
	return labels
}

// JumpHostCandidates lists the Hosts that can serve as a jump hop, in Vault
// order. Only SSH-family endpoints can carry a session; a Local shell, Telnet,
// or another transport would never be dialed as a hop.
func (s *State) JumpHostCandidates() []domainvault.Host {
	out := make([]domainvault.Host, 0, len(s.items))
	for _, host := range s.items {
		if host.Protocol == "" || host.Protocol.UsesSSHAuth() {
			out = append(out, host)
		}
	}
	return out
}

// GroupJumpHostSelected reports whether the Host Group editor's own chain
// lists the Host.
func (s *State) GroupJumpHostSelected(id model.HostID) bool {
	for _, candidate := range s.groupForm.HostChain.IDs() {
		if candidate == id {
			return true
		}
	}
	return false
}

// ToggleGroupJumpHost adds a Host to the Host Group editor's chain or removes
// it. A newly checked Host goes last, so the chain reflects the order hops
// were chosen: first is closest to the client.
func (s *State) ToggleGroupJumpHost(id model.HostID) {
	if id == "" {
		return
	}
	next := make([]model.HostID, 0, len(s.groupForm.HostChain.IDs())+1)
	found := false
	for _, candidate := range s.groupForm.HostChain.IDs() {
		if candidate == id {
			found = true
			continue
		}
		next = append(next, candidate)
	}
	if !found {
		next = append(next, id)
	}
	if len(next) == 0 {
		s.groupForm.HostChain = nil
	} else {
		s.groupForm.HostChain = &domainvault.HostChain{HostIDs: next}
	}
	s.redraw()
}

func (s *State) proxyDraft() (domainvault.ProxyProfileDraft, Problem) {
	port := 0
	if trimmed := strings.TrimSpace(s.proxyForm.Port); trimmed != "" {
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return domainvault.ProxyProfileDraft{}, ProblemPortInvalid
		}
		port = parsed
	}
	return domainvault.ProxyProfileDraft{
		Label: s.proxyForm.Label,
		Config: domainvault.ProxyConfig{
			Type:     domainvault.ProxyType(strings.ToLower(strings.TrimSpace(s.proxyForm.Type))),
			Host:     s.proxyForm.Host,
			Port:     port,
			Command:  s.proxyForm.Command,
			Username: s.proxyForm.Username,
		},
	}, ProblemNone
}

func groupPortText(group domainvault.Group) string {
	if group.Port == 0 {
		return ""
	}
	return strconv.Itoa(group.Port)
}

func proxyPortText(port int) string {
	if port == 0 {
		return ""
	}
	return strconv.Itoa(port)
}
