package vault

import (
	"context"
	"errors"

	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// KnownHostRepository is the Vault's view of recorded host-key trust. The
// connection Manager consumes List and Upsert through its own port; the Vault
// additionally removes a record when the user revokes trust.
type KnownHostRepository interface {
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
	DeleteKnownHost(ctx context.Context, id string) error
}

// WithKnownHosts gives the Service Known Host persistence for the Vault view.
func WithKnownHosts(repo KnownHostRepository) Option {
	return func(s *Service) { s.knownHosts = repo }
}

// ListKnownHosts returns every Known Host in display order.
func (s *Service) ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error) {
	if s.knownHosts == nil {
		return nil, unsupported(ErrKnownHostsUnavailable)
	}
	records, err := s.knownHosts.ListKnownHosts(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return records, nil
}

// DeleteKnownHost revokes one trust decision. Deleting a Known Host never
// touches a Host.
func (s *Service) DeleteKnownHost(ctx context.Context, id string) error {
	if s.knownHosts == nil {
		return unsupported(ErrKnownHostsUnavailable)
	}
	err := s.knownHosts.DeleteKnownHost(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// ErrKnownHostsUnavailable reports a Service built without Known Host
// persistence.
var ErrKnownHostsUnavailable = errors.New("vault: Known Hosts are unavailable")
