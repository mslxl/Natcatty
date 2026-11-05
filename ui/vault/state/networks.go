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
// field secrets are removed by the service; no Host is touched.
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
