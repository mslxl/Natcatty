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

// DragItem is the value a Host row drags. A Host Group row, or the Vault root,
// accepts it and moves the Host there.
type DragItem struct {
	HostID model.HostID
}

// HostDrag returns the drag payload for one Host.
func HostDrag(id model.HostID) DragItem { return DragItem{HostID: id} }

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
	// OriginalPath is the stored path while editing and empty while creating.
	// Changing Path renames the group and remaps its members.
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
	LegacyAlgorithms bool
	SkipEcdsaHostKey bool
	Algorithms       *domainvault.AlgorithmOverrides
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
		OriginalPath:     group.Path,
		Path:             group.Path,
		Notes:            group.Notes,
		Username:         group.Username,
		Port:             groupPortText(group),
		Protocol:         string(group.Protocol),
		AuthMethod:       string(group.AuthMethod),
		IdentityID:       string(group.IdentityID),
		KeyID:            string(group.KeyID),
		UseSSHAgent:      group.UseSSHAgent,
		RequiresMFA:      group.RequiresMFA,
		ProxyProfileID:   group.ProxyProfileID,
		LegacyAlgorithms: group.LegacyAlgorithms,
		SkipEcdsaHostKey: group.SkipEcdsaHostKey,
		Algorithms:       domainvault.CloneAlgorithms(group.Algorithms),
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
	if s.groupForm.OriginalPath == "" {
		_, err = s.groups.CreateGroup(context.Background(), draft)
	} else {
		_, err = s.groups.UpdateGroup(context.Background(), s.groupForm.OriginalPath, draft)
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
	if err := s.groups.DeleteGroup(context.Background(), path); err != nil {
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
	if s.groups == nil {
		s.problem = ProblemInternal
		s.redraw()
		return false
	}
	if err := s.groups.AssignHostGroup(context.Background(), id, path); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return false
	}
	if err := s.load(); err != nil {
		s.redraw()
		return false
	}
	s.problem = ProblemNone
	s.redraw()
	return true
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
		Path:             s.groupForm.Path,
		Notes:            s.groupForm.Notes,
		Username:         s.groupForm.Username,
		Port:             port,
		Protocol:         domainvault.Protocol(s.groupForm.Protocol),
		AuthMethod:       domainvault.HostAuthMethod(s.groupForm.AuthMethod),
		IdentityID:       model.IdentityID(s.groupForm.IdentityID),
		KeyID:            model.KeyID(s.groupForm.KeyID),
		UseSSHAgent:      s.groupForm.UseSSHAgent,
		RequiresMFA:      s.groupForm.RequiresMFA,
		ProxyProfileID:   s.groupForm.ProxyProfileID,
		LegacyAlgorithms: s.groupForm.LegacyAlgorithms,
		SkipEcdsaHostKey: s.groupForm.SkipEcdsaHostKey,
		Algorithms:       domainvault.CloneAlgorithms(s.groupForm.Algorithms),
	}, ProblemNone
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
