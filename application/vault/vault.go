// Package vault owns the Vault use cases: creating, editing, deleting, and
// listing Hosts. It declares the HostRepository port it consumes, so the
// SQLite adapter can implement it without this package knowing about storage.
package vault

import (
	"context"
	"errors"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// OrderStep matches the original Vault's ordering steps.
const orderStep = 1000

// HostRepository is the persistence port the Vault consumes. Implementations
// store whole Host aggregates and keep List ordered for display.
type HostRepository interface {
	List(ctx context.Context) ([]domainvault.Host, error)
	Get(ctx context.Context, id model.HostID) (domainvault.Host, error)
	Save(ctx context.Context, host domainvault.Host) error
	Delete(ctx context.Context, id model.HostID) error
}

// Service implements the Vault use cases.
type Service struct {
	repo  HostRepository
	newID func() model.HostID
	now   func() time.Time
}

// Option customizes a Service.
type Option func(*Service)

// WithIDGenerator sets how the Service names Hosts. Tests use it to make IDs
// deterministic; the app uses the ULID default.
func WithIDGenerator(fn func() model.HostID) Option {
	return func(s *Service) {
		if fn != nil {
			s.newID = fn
		}
	}
}

// WithClock sets the Service's source of time. Tests use it to make
// timestamps deterministic.
func WithClock(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.now = fn
		}
	}
}

// NewService returns a Service that persists Hosts through repo.
func NewService(repo HostRepository, opts ...Option) *Service {
	service := &Service{repo: repo, newID: newHostID, now: time.Now}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// List returns every Host in the order the repository reports.
func (s *Service) List(ctx context.Context) ([]domainvault.Host, error) {
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return hosts, nil
}

// Create validates a draft, assigns identity and timestamps, and persists it.
func (s *Service) Create(ctx context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, invalid(err)
	}
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return domainvault.Host{}, internal(err)
	}

	if duplicate(hosts, normalized, "") {
		return domainvault.Host{}, conflict(nil)
	}

	now := s.now()
	host := applyDraft(domainvault.Host{
		ID:        s.newID(),
		OrderKey:  nextOrder(hosts),
		CreatedAt: now,
		UpdatedAt: now,
	}, normalized)
	if err := s.repo.Save(ctx, host); err != nil {
		return domainvault.Host{}, internal(err)
	}
	return host, nil
}

// Update validates a draft and replaces an existing Host's editable fields.
// Identity, creation time, and order are preserved.
func (s *Service) Update(ctx context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	existing, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Host{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Host{}, internal(err)
	}

	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, invalid(err)
	}
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return domainvault.Host{}, internal(err)
	}

	if duplicate(hosts, normalized, id) {
		return domainvault.Host{}, conflict(nil)
	}

	updated := applyDraft(existing, normalized)
	updated.UpdatedAt = s.now()

	if err := s.repo.Save(ctx, updated); err != nil {
		return domainvault.Host{}, internal(err)
	}
	return updated, nil
}

// Delete removes one Host. It never touches another Vault record.
func (s *Service) Delete(ctx context.Context, id model.HostID) error {
	err := s.repo.Delete(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// applyDraft copies a normalized draft's editable fields onto host, leaving
// identity, ordering, and timestamps to the caller.
func applyDraft(host domainvault.Host, draft domainvault.Draft) domainvault.Host {
	host.Label = draft.Label
	host.Hostname = draft.Hostname
	host.Port = draft.Port
	host.Username = draft.Username
	host.Protocol = draft.Protocol
	host.Group = draft.Group
	host.Tags = draft.Tags
	host.Notes = draft.Notes
	host.Pinned = draft.Pinned
	return host
}

// duplicate reports whether any Host other than except has the same merge key
// as draft. An empty except matches every existing Host.
func duplicate(hosts []domainvault.Host, draft domainvault.Draft, except model.HostID) bool {
	candidate := domainvault.Host{
		Label:    draft.Label,
		Hostname: draft.Hostname,
		Port:     draft.Port,
		Username: draft.Username,
		Protocol: draft.Protocol,
		Group:    draft.Group,
	}
	for _, host := range hosts {
		if host.ID != except && host.MergeKey() == candidate.MergeKey() {
			return true
		}
	}
	return false
}

func nextOrder(hosts []domainvault.Host) int64 {
	var max int64
	for _, host := range hosts {
		if host.OrderKey > max {
			max = host.OrderKey
		}
	}
	return max + orderStep
}
