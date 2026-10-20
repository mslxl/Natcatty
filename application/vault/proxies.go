package vault

import (
	"context"
	"errors"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ProxyProfileRepository persists Proxy Profiles. Config passwords are not
// used by the profile editor; the record holds no credential plaintext.
type ProxyProfileRepository interface {
	ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error)
	GetProxyProfile(ctx context.Context, id string) (domainvault.ProxyProfile, error)
	SaveProxyProfile(ctx context.Context, profile domainvault.ProxyProfile) error
	DeleteProxyProfile(ctx context.Context, id string) error
}

// WithProxyProfiles gives the Service Proxy Profile persistence.
func WithProxyProfiles(repo ProxyProfileRepository) Option {
	return func(s *Service) { s.proxies = repo }
}

// ListProxyProfiles returns every Proxy Profile in display order.
func (s *Service) ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error) {
	if s.proxies == nil {
		return nil, unsupported(ErrProxyProfilesUnavailable)
	}
	profiles, err := s.proxies.ListProxyProfiles(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return profiles, nil
}

// CreateProxyProfile validates and persists a Proxy Profile.
func (s *Service) CreateProxyProfile(ctx context.Context, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	if s.proxies == nil {
		return domainvault.ProxyProfile{}, unsupported(ErrProxyProfilesUnavailable)
	}
	normalized, err := domainvault.NormalizeProxyProfileDraft(draft)
	if err != nil {
		return domainvault.ProxyProfile{}, invalid(err)
	}
	profiles, err := s.proxies.ListProxyProfiles(ctx)
	if err != nil {
		return domainvault.ProxyProfile{}, internal(err)
	}
	now := s.now()
	profile := domainvault.ProxyProfile{
		ID:        s.newProxyProfileID(),
		Label:     normalized.Label,
		Config:    normalized.Config,
		OrderKey:  nextProxyProfileOrder(profiles),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.proxies.SaveProxyProfile(ctx, profile); err != nil {
		return domainvault.ProxyProfile{}, internal(err)
	}
	return profile, nil
}

// UpdateProxyProfile replaces a profile's label and configuration. Identity,
// creation time, and order are preserved.
func (s *Service) UpdateProxyProfile(ctx context.Context, id string, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	if s.proxies == nil {
		return domainvault.ProxyProfile{}, unsupported(ErrProxyProfilesUnavailable)
	}
	existing, err := s.proxies.GetProxyProfile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.ProxyProfile{}, ErrNotFound
	}
	if err != nil {
		return domainvault.ProxyProfile{}, internal(err)
	}
	normalized, err := domainvault.NormalizeProxyProfileDraft(draft)
	if err != nil {
		return domainvault.ProxyProfile{}, invalid(err)
	}
	existing.Label = normalized.Label
	existing.Config = normalized.Config
	existing.UpdatedAt = s.now()
	if err := s.proxies.SaveProxyProfile(ctx, existing); err != nil {
		return domainvault.ProxyProfile{}, internal(err)
	}
	return existing, nil
}

// DeleteProxyProfile removes a profile and clears every Host and Host Group
// reference to it. Referenced Hosts and groups are not deleted.
func (s *Service) DeleteProxyProfile(ctx context.Context, id string) error {
	if s.proxies == nil {
		return unsupported(ErrProxyProfilesUnavailable)
	}
	if _, err := s.proxies.GetProxyProfile(ctx, id); errors.Is(err, ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return internal(err)
	}

	hosts, err := s.repo.List(ctx)
	if err != nil {
		return internal(err)
	}
	for _, host := range hosts {
		if host.ProxyProfileID != id {
			continue
		}
		host.ProxyProfileID = ""
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
			if group.ProxyProfileID != id {
				continue
			}
			group.ProxyProfileID = ""
			group.UpdatedAt = s.now()
			if err := s.groups.SaveGroup(ctx, group); err != nil {
				return internal(err)
			}
		}
	}

	err = s.proxies.DeleteProxyProfile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// ProxyProfile returns one profile.
func (s *Service) ProxyProfile(ctx context.Context, id string) (domainvault.ProxyProfile, error) {
	if s.proxies == nil {
		return domainvault.ProxyProfile{}, unsupported(ErrProxyProfilesUnavailable)
	}
	profile, err := s.proxies.GetProxyProfile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.ProxyProfile{}, ErrNotFound
	}
	if err != nil {
		return domainvault.ProxyProfile{}, internal(err)
	}
	return profile, nil
}

func nextProxyProfileOrder(profiles []domainvault.ProxyProfile) int64 {
	var max int64
	for _, profile := range profiles {
		if profile.OrderKey > max {
			max = profile.OrderKey
		}
	}
	return max + orderStep
}

// ErrProxyProfilesUnavailable reports a Service built without Proxy Profile
// persistence.
var ErrProxyProfilesUnavailable = errors.New("vault: Proxy Profiles are unavailable")
