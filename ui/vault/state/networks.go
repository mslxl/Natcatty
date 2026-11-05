package state

import (
	"context"
	"errors"
	"strconv"
	"strings"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// NetworkProfiles is the Network Profile use-case seam. The application
// Service implements it; tests provide a fake.
type NetworkProfiles interface {
	ListNetworkProfiles(ctx context.Context) ([]domainvault.NetworkProfile, error)
	CreateNetworkProfile(ctx context.Context, draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error)
	UpdateNetworkProfile(ctx context.Context, id string, draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error)
	DeleteNetworkProfile(ctx context.Context, id string) error
}

// NetworkPeerForm is one editable WireGuard peer row. Numeric fields stay text
// so a malformed value can be rejected instead of silently becoming zero.
type NetworkPeerForm struct {
	ID                  string
	PublicKey           string
	PresharedKey        string
	HasPresharedKey     bool
	Endpoint            string
	AllowedIPs          string
	PersistentKeepalive string
}

// NetworkProfileForm is the editable text of one Network Profile. Kind decides
// which group of fields the editor shows. Credential fields are write-only:
// the saved record contributes only the Has* markers.
type NetworkProfileForm struct {
	ID   string
	Kind string

	Label string

	// Tailscale Net
	Hostname   string
	AuthKey    string
	HasAuthKey bool
	ControlURL string
	Ephemeral  bool
	Tags       string

	// WireGuard Net
	PrivateKey    string
	HasPrivateKey bool
	Addresses     string
	DNS           string
	MTU           string
	ListenPort    string
	Peers         []NetworkPeerForm
}

// WithNetworkProfiles gives the Vault surface Network Profile management.
func WithNetworkProfiles(profiles NetworkProfiles) Option {
	return func(s *State) { s.networks = profiles }
}

// NetworkProfileRuntimeUsage reports whether a Network Profile is in use by a
// live Session or an active Connection lease right now. It is advisory: the app
// wires runtime state, tests a fake, and a nil function reports no use.
type NetworkProfileRuntimeUsage func(profileID string) bool

// WithNetworkProfileRuntimeUsage gives the delete confirmation a live-use
// warning so the user knows a Provider will close under running Sessions. The
// lookup may read the Vault and must not load credentials; the delete banner
// calls it only while a confirmation is open.
func WithNetworkProfileRuntimeUsage(fn NetworkProfileRuntimeUsage) Option {
	return func(s *State) { s.networkUsage = fn }
}

// NetworkProfileInUse reports whether a Network Profile is currently used by a
// live Session or active Connection lease. It is safe to call while rendering
// the delete confirmation; no answer means not in use.
func (s *State) NetworkProfileInUse(id string) bool {
	if s == nil || s.networkUsage == nil || id == "" {
		return false
	}
	return s.networkUsage(id)
}

// NetworkProfiles returns the listed Network Profiles in repository order.
func (s *State) NetworkProfiles() []domainvault.NetworkProfile { return s.networkItems }

// NetworkProfileCount returns the number of listed Network Profiles.
func (s *State) NetworkProfileCount() int { return len(s.networkItems) }

// NetworkProfileByID returns one listed Network Profile.
func (s *State) NetworkProfileByID(id string) (domainvault.NetworkProfile, bool) {
	for _, profile := range s.networkItems {
		if profile.ID == id {
			return profile, true
		}
	}
	return domainvault.NetworkProfile{}, false
}

// NetworkProfileSource names where an editor's effective Network Profile
// selection comes from. It lets the view say whether the value belongs to the
// record being edited or is inherited.
type NetworkProfileSource string

const (
	// NetworkProfileSourceNone means neither the record nor an ancestor sets a
	// Network Profile.
	NetworkProfileSourceNone NetworkProfileSource = ""
	// NetworkProfileSourceRecord means the record being edited holds the
	// reference: a Host's own selection or the edited Host Group's default.
	NetworkProfileSourceRecord NetworkProfileSource = "record"
	// NetworkProfileSourceInherited means the selection comes from a Host
	// Group: a Host's group, or an ancestor of the edited Host Group.
	NetworkProfileSourceInherited NetworkProfileSource = "inherited"
)

// NetworkProfileLabel returns a profile's display label, or the raw reference
// when the profile no longer exists. The latter lets an editor show a dangling
// soft reference instead of pretending nothing is selected.
func (s *State) NetworkProfileLabel(id string) string {
	if id == "" {
		return ""
	}
	if profile, ok := s.NetworkProfileByID(id); ok {
		return profile.Label
	}
	return id
}

// NetworkProfileUsage counts the records whose effective selection a delete
// would change. A Host or Host Group that inherits the profile from an
// ancestor is counted too: clearing the owning reference changes its route
// even though the record itself holds no reference.
func (s *State) NetworkProfileUsage(id string) (hosts, groups int) {
	if id == "" {
		return 0, 0
	}
	for _, group := range s.groupItems {
		if domainvault.ResolveGroupDefaults(s.groupItems, group.Path).NetworkProfileID == id {
			groups++
		}
	}
	for _, host := range s.items {
		effective := host.NetworkProfileID
		if effective == "" {
			effective = domainvault.ResolveGroupDefaults(s.groupItems, host.Group).NetworkProfileID
		}
		if effective == id {
			hosts++
		}
	}
	return hosts, groups
}

// HostNetworkProfileSource reports the Host editor's effective selection: the
// Host's own reference, the Host Group default, or nothing. It also returns the
// Host Group path that owns an inherited selection and the profile ID.
func (s *State) HostNetworkProfileSource() (NetworkProfileSource, string, string) {
	if id := strings.TrimSpace(s.form.NetworkProfileID); id != "" {
		return NetworkProfileSourceRecord, "", id
	}
	group := domainvault.NormalizeGroup(s.form.Group)
	if id := domainvault.ResolveGroupDefaults(s.groupItems, group).NetworkProfileID; id != "" {
		return NetworkProfileSourceInherited, s.networkProfileOwner(group), id
	}
	return NetworkProfileSourceNone, "", ""
}

// GroupNetworkProfileSource reports the Host Group editor's effective
// selection: the group's own reference, an ancestor's default, or nothing. It
// names the ancestor that actually holds the reference.
func (s *State) GroupNetworkProfileSource() (NetworkProfileSource, string, string) {
	if id := strings.TrimSpace(s.groupForm.NetworkProfileID); id != "" {
		return NetworkProfileSourceRecord, "", id
	}
	parent := domainvault.ParentGroupPath(s.groupForm.Path)
	if id := domainvault.ResolveGroupDefaults(s.groupItems, parent).NetworkProfileID; id != "" {
		return NetworkProfileSourceInherited, s.networkProfileOwner(parent), id
	}
	return NetworkProfileSourceNone, "", ""
}

// networkProfileOwner returns the deepest group at or above path that holds
// its own Network Profile reference, matching the child-last merge order.
func (s *State) networkProfileOwner(path string) string {
	owner := ""
	for _, ancestor := range domainvault.GroupAncestors(path) {
		for _, group := range s.groupItems {
			if group.Path == ancestor && group.NetworkProfileID != "" {
				owner = ancestor
				break
			}
		}
	}
	return owner
}

// EditingNetworkProfile reports whether the Network Profile editor is open.
func (s *State) EditingNetworkProfile() bool { return s.editingNetwork }

// NetworkProfileForm returns the Network Profile editor's form. It is valid
// while EditingNetworkProfile.
func (s *State) NetworkProfileForm() *NetworkProfileForm { return &s.networkForm }

// PendingDeleteNetworkProfile returns the Network Profile awaiting delete
// confirmation.
func (s *State) PendingDeleteNetworkProfile() string { return s.pendingDeleteNetwork }

// BeginCreateNetworkProfile opens the editor for one Kind. An unknown Kind is
// refused rather than silently replaced.
func (s *State) BeginCreateNetworkProfile(kind domainvault.NetworkProfileKind) bool {
	switch kind {
	case domainvault.NetworkKindTailscale:
	case domainvault.NetworkKindWireGuard:
	default:
		s.problem = ProblemNetworkProfile
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingNetwork = true
	s.networkForm = NetworkProfileForm{Kind: string(kind)}
	s.resetNetworkImport()
	if kind == domainvault.NetworkKindWireGuard {
		// A WireGuard profile needs at least one peer, so the editor opens with
		// an empty row instead of a validation error after the first Save.
		s.networkForm.Peers = []NetworkPeerForm{{}}
	}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// BeginEditNetworkProfile opens the editor with a listed profile. Secret
// fields stay empty; their Has* markers tell the editor they are already
// saved.
func (s *State) BeginEditNetworkProfile(id string) bool {
	profile, ok := s.NetworkProfileByID(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingNetwork = true
	form := NetworkProfileForm{
		ID:    profile.ID,
		Kind:  string(profile.Kind),
		Label: profile.Label,
	}
	switch profile.Kind {
	case domainvault.NetworkKindTailscale:
		if profile.Tailscale != nil {
			form.Hostname = profile.Tailscale.Hostname
			form.HasAuthKey = profile.Tailscale.HasAuthKey
			form.ControlURL = profile.Tailscale.ControlURL
			form.Ephemeral = profile.Tailscale.Ephemeral
			form.Tags = strings.Join(profile.Tailscale.Tags, ", ")
		}
	case domainvault.NetworkKindWireGuard:
		if profile.WireGuard != nil {
			form.HasPrivateKey = profile.WireGuard.HasPrivateKey
			form.Addresses = strings.Join(profile.WireGuard.Addresses, ", ")
			form.DNS = strings.Join(profile.WireGuard.DNS, ", ")
			if profile.WireGuard.MTU != 0 {
				form.MTU = strconv.Itoa(profile.WireGuard.MTU)
			}
			if profile.WireGuard.ListenPort != 0 {
				form.ListenPort = strconv.Itoa(profile.WireGuard.ListenPort)
			}
			for _, peer := range profile.WireGuard.Peers {
				row := NetworkPeerForm{
					ID:              peer.ID,
					PublicKey:       peer.PublicKey,
					HasPresharedKey: peer.HasPresharedKey,
					Endpoint:        peer.Endpoint,
					AllowedIPs:      strings.Join(peer.AllowedIPs, ", "),
				}
				if peer.PersistentKeepalive != 0 {
					row.PersistentKeepalive = strconv.Itoa(peer.PersistentKeepalive)
				}
				form.Peers = append(form.Peers, row)
			}
		}
	}
	s.networkForm = form
	s.resetNetworkImport()
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelNetworkProfileEdit closes the Network Profile editor without saving.
func (s *State) CancelNetworkProfileEdit() {
	s.editingNetwork = false
	s.networkForm = NetworkProfileForm{}
	s.resetNetworkImport()
	s.problem = ProblemNone
	s.redraw()
}

// AddNetworkPeer appends an empty WireGuard peer row.
func (s *State) AddNetworkPeer() {
	if !s.editingNetwork || domainvault.NetworkProfileKind(s.networkForm.Kind) != domainvault.NetworkKindWireGuard {
		return
	}
	s.networkForm.Peers = append(s.networkForm.Peers, NetworkPeerForm{})
	s.redraw()
}

// RemoveNetworkPeer drops one WireGuard peer row. An out-of-range index is a
// no-op so a stale click cannot panic.
func (s *State) RemoveNetworkPeer(index int) {
	if !s.editingNetwork || index < 0 || index >= len(s.networkForm.Peers) {
		return
	}
	s.networkForm.Peers = append(s.networkForm.Peers[:index], s.networkForm.Peers[index+1:]...)
	s.redraw()
}

// SaveNetworkProfile validates and persists the Network Profile form.
func (s *State) SaveNetworkProfile() error {
	if s.networks == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	draft, problem := s.networkDraft()
	if problem != ProblemNone {
		s.problem = problem
		s.redraw()
		return nil
	}
	var err error
	if s.networkForm.ID == "" {
		_, err = s.networks.CreateNetworkProfile(context.Background(), draft)
	} else {
		_, err = s.networks.UpdateNetworkProfile(context.Background(), s.networkForm.ID, draft)
	}
	if err != nil {
		s.problem = networkProblemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingNetwork = false
	s.networkForm = NetworkProfileForm{}
	s.resetNetworkImport()
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteNetworkProfile opens the confirmation for one profile.
func (s *State) RequestDeleteNetworkProfile(id string) {
	s.pendingDeleteNetwork = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteNetworkProfile closes the Network Profile delete confirmation.
func (s *State) CancelDeleteNetworkProfile() {
	s.pendingDeleteNetwork = ""
	s.redraw()
}

// ConfirmDeleteNetworkProfile deletes the profile awaiting confirmation. Its
// field secrets and every Host and Host Group reference to it are removed by
// the service; the records themselves survive.
func (s *State) ConfirmDeleteNetworkProfile() error {
	if s.pendingDeleteNetwork == "" {
		return nil
	}
	id := s.pendingDeleteNetwork
	s.pendingDeleteNetwork = ""
	if s.networks == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.networks.DeleteNetworkProfile(context.Background(), id); err != nil {
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

// NetworkProfileWarnings returns the non-blocking warnings for the form's
// current values, so the editor can show them before Save.
func (s *State) NetworkProfileWarnings() []domainvault.NetworkWarning {
	draft := domainvault.NetworkProfileDraft{
		Kind: domainvault.NetworkProfileKind(s.networkForm.Kind),
	}
	if draft.Kind == domainvault.NetworkKindTailscale {
		draft.Tailscale = &domainvault.TailscaleNetworkDraft{
			ControlURL: s.networkForm.ControlURL,
		}
	}
	if draft.Kind == domainvault.NetworkKindWireGuard {
		draft.WireGuard = &domainvault.WireGuardNetworkDraft{
			DNS: splitFormList(s.networkForm.DNS),
		}
	}
	return domainvault.NetworkProfileWarnings(draft)
}

func (s *State) networkDraft() (domainvault.NetworkProfileDraft, Problem) {
	draft := domainvault.NetworkProfileDraft{
		Label: s.networkForm.Label,
		Kind:  domainvault.NetworkProfileKind(s.networkForm.Kind),
	}
	switch draft.Kind {
	case domainvault.NetworkKindTailscale:
		draft.Tailscale = &domainvault.TailscaleNetworkDraft{
			Hostname:   s.networkForm.Hostname,
			AuthKey:    s.networkForm.AuthKey,
			ControlURL: s.networkForm.ControlURL,
			Ephemeral:  s.networkForm.Ephemeral,
			Tags:       splitFormList(s.networkForm.Tags),
		}
	case domainvault.NetworkKindWireGuard:
		mtu := 0
		if trimmed := strings.TrimSpace(s.networkForm.MTU); trimmed != "" {
			parsed, err := strconv.Atoi(trimmed)
			if err != nil {
				return domainvault.NetworkProfileDraft{}, ProblemNetworkMTU
			}
			mtu = parsed
		}
		listenPort := 0
		if trimmed := strings.TrimSpace(s.networkForm.ListenPort); trimmed != "" {
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed < 0 || parsed > 65535 {
				return domainvault.NetworkProfileDraft{}, ProblemNetworkListenPort
			}
			listenPort = parsed
		}
		config := &domainvault.WireGuardNetworkDraft{
			PrivateKey: s.networkForm.PrivateKey,
			Addresses:  splitFormList(s.networkForm.Addresses),
			DNS:        splitFormList(s.networkForm.DNS),
			MTU:        mtu,
			ListenPort: listenPort,
		}
		for _, row := range s.networkForm.Peers {
			keepalive := 0
			if trimmed := strings.TrimSpace(row.PersistentKeepalive); trimmed != "" {
				parsed, err := strconv.Atoi(trimmed)
				if err != nil || parsed < 0 || parsed > 65535 {
					return domainvault.NetworkProfileDraft{}, ProblemNetworkKeepalive
				}
				keepalive = parsed
			}
			config.Peers = append(config.Peers, domainvault.WireGuardPeerDraft{
				ID:                  row.ID,
				PublicKey:           row.PublicKey,
				PresharedKey:        row.PresharedKey,
				Endpoint:            row.Endpoint,
				AllowedIPs:          splitFormList(row.AllowedIPs),
				PersistentKeepalive: keepalive,
			})
		}
		draft.WireGuard = config
	default:
		return domainvault.NetworkProfileDraft{}, ProblemNetworkProfile
	}
	return draft, ProblemNone
}

// networkProblemFor maps a Network Profile save failure to the editor's
// problem. A form-level validation failure is about the profile's own fields,
// so it does not surface as the generic secret-field problem.
func networkProblemFor(err error) Problem {
	var typed *appvault.Error
	if errors.As(err, &typed) && typed.Kind == appvault.KindValidation {
		return ProblemNetworkProfile
	}
	return problemFor(err)
}

// splitFormList splits a comma- or newline-separated editor field into values,
// trimming whitespace and dropping empties.
func splitFormList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
