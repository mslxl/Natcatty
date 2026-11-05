package vault

import (
	"context"
	"errors"
	"fmt"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// NetworkProfileRepository persists Network Profiles. The record holds no
// credential plaintext: Auth Keys, Private Keys, and Preshared Keys live in
// the field-secret store and the record carries only Has* markers.
type NetworkProfileRepository interface {
	ListNetworkProfiles(ctx context.Context) ([]domainvault.NetworkProfile, error)
	GetNetworkProfile(ctx context.Context, id string) (domainvault.NetworkProfile, error)
	SaveNetworkProfile(ctx context.Context, profile domainvault.NetworkProfile) error
	DeleteNetworkProfile(ctx context.Context, id string) error
}

// NetworkProfileStateCleaner removes the local runtime state a Network Profile
// owns, such as an embedded Tailscale node's state directory. The deletion
// confirmation belongs to the UI; this port makes the cleanup part of the
// confirmed delete instead of an orphaned directory. Implementations must
// remove only state for the given profile and be idempotent.
type NetworkProfileStateCleaner interface {
	RemoveNetworkProfileState(ctx context.Context, profile domainvault.NetworkProfile) error
}

// WithNetworkProfiles gives the Service Network Profile persistence.
func WithNetworkProfiles(repo NetworkProfileRepository) Option {
	return func(s *Service) { s.networks = repo }
}

// WithNetworkProfileStateCleaner gives the Service the cleanup that runs when
// a confirmed Network Profile delete removes runtime state.
func WithNetworkProfileStateCleaner(cleaner NetworkProfileStateCleaner) Option {
	return func(s *Service) { s.networkState = cleaner }
}

// WithNetworkProfileIDGenerator sets how the Service names Network Profiles.
func WithNetworkProfileIDGenerator(fn func() string) Option {
	return func(s *Service) {
		if fn != nil {
			s.newNetworkProfileID = fn
		}
	}
}

// WithNetworkPeerIDGenerator sets how the Service names WireGuard peers. A
// peer ID is stable for the peer's lifetime and addresses its Preshared Key.
func WithNetworkPeerIDGenerator(fn func() string) Option {
	return func(s *Service) {
		if fn != nil {
			s.newNetworkPeerID = fn
		}
	}
}

// ListNetworkProfiles returns every Network Profile in display order.
func (s *Service) ListNetworkProfiles(ctx context.Context) ([]domainvault.NetworkProfile, error) {
	if s.networks == nil {
		return nil, unsupported(ErrNetworkProfilesUnavailable)
	}
	profiles, err := s.networks.ListNetworkProfiles(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return profiles, nil
}

// NetworkProfile returns one Network Profile.
func (s *Service) NetworkProfile(ctx context.Context, id string) (domainvault.NetworkProfile, error) {
	if s.networks == nil {
		return domainvault.NetworkProfile{}, unsupported(ErrNetworkProfilesUnavailable)
	}
	profile, err := s.networks.GetNetworkProfile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.NetworkProfile{}, ErrNotFound
	}
	if err != nil {
		return domainvault.NetworkProfile{}, internal(err)
	}
	return profile, nil
}

// CreateNetworkProfile validates and persists a Network Profile. Credential
// plaintext is written to the field-secret store; the returned record carries
// only Has* markers.
func (s *Service) CreateNetworkProfile(ctx context.Context,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {

	if s.networks == nil {
		return domainvault.NetworkProfile{}, unsupported(ErrNetworkProfilesUnavailable)
	}
	normalized, err := domainvault.NormalizeNetworkProfileDraft(draft)
	if err != nil {
		return domainvault.NetworkProfile{}, invalid(err)
	}
	profiles, err := s.networks.ListNetworkProfiles(ctx)
	if err != nil {
		return domainvault.NetworkProfile{}, internal(err)
	}

	now := s.now()
	profile := domainvault.NetworkProfile{
		ID:        s.newNetworkProfileID(),
		Label:     normalized.Label,
		Kind:      normalized.Kind,
		OrderKey:  nextNetworkProfileOrder(profiles),
		CreatedAt: now,
		UpdatedAt: now,
	}
	var written []SecretRef
	rollback := func() {
		for _, ref := range written {
			_ = s.secrets.Delete(ctx, ref)
		}
	}
	switch normalized.Kind {
	case domainvault.NetworkKindTailscale:
		config := normalized.Tailscale
		profile.Tailscale = &domainvault.TailscaleNetwork{
			Hostname:   config.Hostname,
			ControlURL: config.ControlURL,
			Ephemeral:  config.Ephemeral,
			Tags:       append([]string(nil), config.Tags...),
		}
		if config.AuthKey != "" {
			if s.secrets == nil {
				return domainvault.NetworkProfile{}, unsupported(ErrSecretsUnavailable)
			}
			ref := NetworkProfileSecretRef(profile.ID, FieldNetworkAuthKey)
			if err := s.secrets.Set(ctx, ref, config.AuthKey); err != nil {
				return domainvault.NetworkProfile{}, secretError(err)
			}
			written = append(written, ref)
			profile.Tailscale.HasAuthKey = s.SecretMode() != SecretModeSession
		}
	case domainvault.NetworkKindWireGuard:
		config := normalized.WireGuard
		profile.WireGuard = &domainvault.WireGuardNetwork{
			Addresses:  append([]string(nil), config.Addresses...),
			DNS:        append([]string(nil), config.DNS...),
			MTU:        config.MTU,
			ListenPort: config.ListenPort,
			Peers:      make([]domainvault.WireGuardPeer, 0, len(config.Peers)),
		}
		if config.PrivateKey != "" {
			if s.secrets == nil {
				return domainvault.NetworkProfile{}, unsupported(ErrSecretsUnavailable)
			}
			ref := NetworkProfileSecretRef(profile.ID, FieldNetworkPrivateKey)
			if err := s.secrets.Set(ctx, ref, config.PrivateKey); err != nil {
				return domainvault.NetworkProfile{}, secretError(err)
			}
			written = append(written, ref)
			profile.WireGuard.HasPrivateKey = s.SecretMode() != SecretModeSession
		}
		for _, peer := range config.Peers {
			stored := domainvault.WireGuardPeer{
				// A create never trusts a supplied peer ID: the ID addresses a
				// field secret, and an imported document must not be able to
				// point at another profile's Preshared Key.
				ID:                  s.newNetworkPeerID(),
				PublicKey:           peer.PublicKey,
				Endpoint:            peer.Endpoint,
				AllowedIPs:          append([]string(nil), peer.AllowedIPs...),
				PersistentKeepalive: peer.PersistentKeepalive,
			}
			if peer.PresharedKey != "" {
				if s.secrets == nil {
					rollback()
					return domainvault.NetworkProfile{}, unsupported(ErrSecretsUnavailable)
				}
				ref := NetworkPeerSecretRef(stored.ID, FieldNetworkPresharedKey)
				if err := s.secrets.Set(ctx, ref, peer.PresharedKey); err != nil {
					rollback()
					return domainvault.NetworkProfile{}, secretError(err)
				}
				written = append(written, ref)
				stored.HasPresharedKey = s.SecretMode() != SecretModeSession
			}
			profile.WireGuard.Peers = append(profile.WireGuard.Peers, stored)
		}
	}
	if err := s.networks.SaveNetworkProfile(ctx, profile); err != nil {
		rollback()
		return domainvault.NetworkProfile{}, internal(err)
	}
	return profile, nil
}

// UpdateNetworkProfile replaces a profile's label and configuration. Identity,
// creation time, order, Kind, and every credential the draft leaves empty are
// preserved. A Kind change is refused because it would orphan the old
// configuration's secrets.
func (s *Service) UpdateNetworkProfile(ctx context.Context, id string,
	draft domainvault.NetworkProfileDraft) (domainvault.NetworkProfile, error) {

	if s.networks == nil {
		return domainvault.NetworkProfile{}, unsupported(ErrNetworkProfilesUnavailable)
	}
	existing, err := s.networks.GetNetworkProfile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.NetworkProfile{}, ErrNotFound
	}
	if err != nil {
		return domainvault.NetworkProfile{}, internal(err)
	}
	normalized, err := domainvault.NormalizeNetworkProfileDraft(draft)
	if err != nil {
		return domainvault.NetworkProfile{}, invalid(err)
	}
	if normalized.Kind != existing.Kind {
		return domainvault.NetworkProfile{}, invalid(validationError(
			domainvault.FieldNetworkProfile, domainvault.RuleInvalid))
	}

	updated := existing
	updated.Label = normalized.Label
	updated.UpdatedAt = s.now()
	written := make([]secretReplacement, 0, 2)
	rollback := func() {
		for _, replacement := range written {
			rollbackSecret(ctx, s.secrets, replacement.ref, replacement.previous)
		}
	}

	switch normalized.Kind {
	case domainvault.NetworkKindTailscale:
		config := normalized.Tailscale
		updated.Tailscale = &domainvault.TailscaleNetwork{
			Hostname:   config.Hostname,
			ControlURL: config.ControlURL,
			Ephemeral:  config.Ephemeral,
			Tags:       append([]string(nil), config.Tags...),
			HasAuthKey: existing.Tailscale != nil && existing.Tailscale.HasAuthKey,
		}
		if config.AuthKey != "" {
			if s.secrets == nil {
				return domainvault.NetworkProfile{}, unsupported(ErrSecretsUnavailable)
			}
			ref := NetworkProfileSecretRef(existing.ID, FieldNetworkAuthKey)
			previous, err := s.readSecretValue(ctx, ref)
			if err != nil {
				return domainvault.NetworkProfile{}, err
			}
			if err := s.secrets.Set(ctx, ref, config.AuthKey); err != nil {
				return domainvault.NetworkProfile{}, secretError(err)
			}
			written = append(written, secretReplacement{ref: ref, previous: previous})
			updated.Tailscale.HasAuthKey = s.SecretMode() != SecretModeSession
		}
	case domainvault.NetworkKindWireGuard:
		config := normalized.WireGuard
		previousPeers := make(map[string]domainvault.WireGuardPeer)
		if existing.WireGuard != nil {
			for _, peer := range existing.WireGuard.Peers {
				previousPeers[peer.ID] = peer
			}
		}
		next := &domainvault.WireGuardNetwork{
			Addresses:     append([]string(nil), config.Addresses...),
			DNS:           append([]string(nil), config.DNS...),
			MTU:           config.MTU,
			ListenPort:    config.ListenPort,
			Peers:         make([]domainvault.WireGuardPeer, 0, len(config.Peers)),
			HasPrivateKey: existing.WireGuard != nil && existing.WireGuard.HasPrivateKey,
		}
		updated.WireGuard = next
		if config.PrivateKey != "" {
			if s.secrets == nil {
				return domainvault.NetworkProfile{}, unsupported(ErrSecretsUnavailable)
			}
			ref := NetworkProfileSecretRef(existing.ID, FieldNetworkPrivateKey)
			previous, err := s.readSecretValue(ctx, ref)
			if err != nil {
				return domainvault.NetworkProfile{}, err
			}
			if err := s.secrets.Set(ctx, ref, config.PrivateKey); err != nil {
				return domainvault.NetworkProfile{}, secretError(err)
			}
			written = append(written, secretReplacement{ref: ref, previous: previous})
			next.HasPrivateKey = s.SecretMode() != SecretModeSession
		}
		kept := make(map[string]bool, len(config.Peers))
		for _, peer := range config.Peers {
			peerID := peer.ID
			previousPeer, exists := previousPeers[peerID]
			if peerID == "" || !exists {
				// A supplied ID this profile does not own is not trusted; it
				// would address another profile's secret.
				peerID = s.newNetworkPeerID()
				previousPeer = domainvault.WireGuardPeer{}
				exists = false
			}
			kept[peerID] = true
			stored := domainvault.WireGuardPeer{
				ID:                  peerID,
				PublicKey:           peer.PublicKey,
				Endpoint:            peer.Endpoint,
				AllowedIPs:          append([]string(nil), peer.AllowedIPs...),
				PersistentKeepalive: peer.PersistentKeepalive,
				HasPresharedKey:     exists && previousPeer.HasPresharedKey,
			}
			if peer.PresharedKey != "" {
				if s.secrets == nil {
					rollback()
					return domainvault.NetworkProfile{}, unsupported(ErrSecretsUnavailable)
				}
				ref := NetworkPeerSecretRef(peerID, FieldNetworkPresharedKey)
				previous, err := s.readSecretValue(ctx, ref)
				if err != nil {
					rollback()
					return domainvault.NetworkProfile{}, err
				}
				if err := s.secrets.Set(ctx, ref, peer.PresharedKey); err != nil {
					rollback()
					return domainvault.NetworkProfile{}, secretError(err)
				}
				written = append(written, secretReplacement{ref: ref, previous: previous})
				stored.HasPresharedKey = s.SecretMode() != SecretModeSession
			}
			next.Peers = append(next.Peers, stored)
		}
		if err := s.networks.SaveNetworkProfile(ctx, updated); err != nil {
			rollback()
			return domainvault.NetworkProfile{}, internal(err)
		}
		// A peer removed by this edit must not leave its Preshared Key behind.
		if s.secrets != nil {
			for peerID := range previousPeers {
				if kept[peerID] {
					continue
				}
				_ = s.secrets.Delete(ctx, NetworkPeerSecretRef(peerID, FieldNetworkPresharedKey))
			}
		}
		return updated, nil
	}
	if err := s.networks.SaveNetworkProfile(ctx, updated); err != nil {
		rollback()
		return domainvault.NetworkProfile{}, internal(err)
	}
	return updated, nil
}

// DeleteNetworkProfile removes a profile, clears every Host and Host Group
// reference to it, removes the field secrets it owned, and deletes the local
// runtime state it accumulated (for example a Tailscale node identity).
// Referenced Hosts and Host Groups survive with the reference cleared; no
// other record is deleted. Runtime-state cleanup runs before any record is
// touched, so a failed cleanup leaves the profile deletable again instead of
// stranding credential material.
func (s *Service) DeleteNetworkProfile(ctx context.Context, id string) error {
	if s.networks == nil {
		return unsupported(ErrNetworkProfilesUnavailable)
	}
	profile, err := s.networks.GetNetworkProfile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	if s.networkState != nil {
		if err := s.networkState.RemoveNetworkProfileState(ctx, profile); err != nil {
			return internal(fmt.Errorf("remove state for Network Profile %q: %w", id, err))
		}
	}
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return internal(err)
	}
	for _, host := range hosts {
		if host.NetworkProfileID != id {
			continue
		}
		host.NetworkProfileID = ""
		host.UpdatedAt = s.now()
		if err := s.repo.Save(ctx, host); err != nil {
			return internal(err)
		}
	}
	if s.groups != nil {
		groups, err := s.groups.ListGroups(ctx)
		if err != nil {
			return internal(err)
		}
		for _, group := range groups {
			if group.NetworkProfileID != id {
				continue
			}
			group.NetworkProfileID = ""
			group.UpdatedAt = s.now()
			if err := s.groups.SaveGroup(ctx, group); err != nil {
				return internal(err)
			}
		}
	}
	if err := s.networks.DeleteNetworkProfile(ctx, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return internal(err)
	}
	// Ciphertext removal is best-effort: a locked store must not block the
	// record delete, and an orphan ciphertext is never plaintext.
	if s.secrets != nil {
		for _, field := range []string{FieldNetworkAuthKey, FieldNetworkPrivateKey} {
			_ = s.secrets.Delete(ctx, NetworkProfileSecretRef(id, field))
		}
		if profile.WireGuard != nil {
			for _, peer := range profile.WireGuard.Peers {
				_ = s.secrets.Delete(ctx, NetworkPeerSecretRef(peer.ID, FieldNetworkPresharedKey))
			}
		}
	}
	return nil
}

// NetworkProfileSecret reads a profile-level credential for the Provider that
// needs it. Field must match the profile's Kind.
func (s *Service) NetworkProfileSecret(ctx context.Context, id, field string) (string, error) {
	profile, err := s.NetworkProfile(ctx, id)
	if err != nil {
		return "", err
	}
	if s.secrets == nil {
		return "", unsupported(ErrSecretsUnavailable)
	}
	switch field {
	case FieldNetworkAuthKey:
		if profile.Kind != domainvault.NetworkKindTailscale {
			return "", invalidField(field)
		}
	case FieldNetworkPrivateKey:
		if profile.Kind != domainvault.NetworkKindWireGuard {
			return "", invalidField(field)
		}
	default:
		return "", invalidField(field)
	}
	value, err := s.secrets.Get(ctx, NetworkProfileSecretRef(id, field))
	if err != nil {
		return "", secretError(err)
	}
	return value, nil
}

// NetworkPeerPresharedKey reads one peer's Preshared Key. The peer must belong
// to the profile, so a stale ID cannot read another profile's credential.
func (s *Service) NetworkPeerPresharedKey(ctx context.Context, profileID, peerID string) (string, error) {
	profile, err := s.NetworkProfile(ctx, profileID)
	if err != nil {
		return "", err
	}
	found := false
	if profile.WireGuard != nil {
		for _, peer := range profile.WireGuard.Peers {
			if peer.ID == peerID {
				found = true
				break
			}
		}
	}
	if !found {
		return "", ErrNotFound
	}
	if s.secrets == nil {
		return "", unsupported(ErrSecretsUnavailable)
	}
	value, err := s.secrets.Get(ctx, NetworkPeerSecretRef(peerID, FieldNetworkPresharedKey))
	if err != nil {
		return "", secretError(err)
	}
	return value, nil
}

// secretReplacement is a written secret and the value it replaced, so a failed
// Save restores the store exactly.
type secretReplacement struct {
	ref      SecretRef
	previous priorSecret
}

func nextNetworkProfileOrder(profiles []domainvault.NetworkProfile) int64 {
	var max int64
	for _, profile := range profiles {
		if profile.OrderKey > max {
			max = profile.OrderKey
		}
	}
	return max + orderStep
}

// validationError builds a domain validation error for a rule the service
// itself enforces, such as a Kind change.
func validationError(field domainvault.Field, rule domainvault.Rule) error {
	return &domainvault.ValidationError{Field: field, Rule: rule}
}

// ErrNetworkProfilesUnavailable reports a Service built without Network
// Profile persistence.
var ErrNetworkProfilesUnavailable = errors.New("vault: Network Profiles are unavailable")
