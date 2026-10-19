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
	repo          HostRepository
	keys          KeyRepository
	identities    IdentityRepository
	inspector     KeyInspector
	secrets       FieldSecrets
	newID         func() model.HostID
	newKeyID      func() model.KeyID
	newIdentityID func() model.IdentityID
	now           func() time.Time
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

// WithSecrets gives the Service the field-secret store that encrypts Host
// credentials and SSH Key material. Without it, a draft that carries a
// credential is refused rather than written in plaintext.
func WithSecrets(secrets FieldSecrets) Option {
	return func(s *Service) { s.secrets = secrets }
}

// WithKeys gives the Service SSH Key persistence.
func WithKeys(repo KeyRepository) Option {
	return func(s *Service) { s.keys = repo }
}

// WithIdentities gives the Service Identity persistence.
func WithIdentities(repo IdentityRepository) Option {
	return func(s *Service) { s.identities = repo }
}

// WithKeyInspector gives the Service the key-format parser that validates
// imported material and reports its public key. Without it, key creation is
// refused: the Service never pretends unvalidated material is usable.
func WithKeyInspector(inspector KeyInspector) Option {
	return func(s *Service) { s.inspector = inspector }
}

// WithKeyIDGenerator sets how the Service names SSH Keys.
func WithKeyIDGenerator(fn func() model.KeyID) Option {
	return func(s *Service) {
		if fn != nil {
			s.newKeyID = fn
		}
	}
}

// WithIdentityIDGenerator sets how the Service names Identities.
func WithIdentityIDGenerator(fn func() model.IdentityID) Option {
	return func(s *Service) {
		if fn != nil {
			s.newIdentityID = fn
		}
	}
}

// NewService returns a Service that persists Hosts through repo.
func NewService(repo HostRepository, opts ...Option) *Service {
	service := &Service{
		repo:          repo,
		newID:         newHostID,
		newKeyID:      newSSHKeyID,
		newIdentityID: newIdentityID,
		now:           time.Now,
	}
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

// Get returns one Host. The connection layer uses it to resolve a jump chain
// and the Host's proxy configuration.
func (s *Service) Get(ctx context.Context, id model.HostID) (domainvault.Host, error) {
	host, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Host{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Host{}, internal(err)
	}
	return host, nil
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
	passwordStored := normalized.Password != ""
	proxyPasswordStored := normalized.Proxy != nil && normalized.Proxy.Password != ""
	if err := s.persistPassword(ctx, host.ID, normalized.Password); err != nil {
		return domainvault.Host{}, err
	}
	if err := s.persistProxyPassword(ctx, host.ID, normalized.Proxy); err != nil {
		// The Host record is never written, so the password written above must
		// not outlive the failed Create.
		if passwordStored {
			_ = s.secrets.Delete(ctx, HostSecretRef(host.ID, FieldPassword))
		}
		return domainvault.Host{}, err
	}
	// Session-only credentials are deliberately forgotten on restart, so the
	// Host record must not claim a persisted password.
	host.HasPassword = passwordStored && s.SecretMode() != SecretModeSession
	if host.Proxy != nil {
		host.Proxy.HasPassword = proxyPasswordStored && s.SecretMode() != SecretModeSession
	}
	if err := s.repo.Save(ctx, host); err != nil {
		// Delete by what was written, not by what the Host advertises: a
		// session-only credential is stored but deliberately not advertised.
		if passwordStored {
			_ = s.secrets.Delete(ctx, HostSecretRef(host.ID, FieldPassword))
		}
		if proxyPasswordStored {
			_ = s.secrets.Delete(ctx, HostSecretRef(host.ID, FieldProxyPassword))
		}
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

	// Read every credential that a write will replace before writing any of
	// them, so an unreadable value aborts before the store is modified.
	passwordChanged := normalized.Password != ""
	previousPassword, err := s.readSecret(ctx, existing.ID, FieldPassword, passwordChanged)
	if err != nil {
		return domainvault.Host{}, err
	}
	// A removal or a new password both replace the stored proxy credential.
	proxyReplaced := normalized.Proxy == nil || normalized.Proxy.Password != ""
	previousProxy, err := s.readSecret(ctx, existing.ID, FieldProxyPassword, proxyReplaced)
	if err != nil {
		return domainvault.Host{}, err
	}

	var passwordWritten, proxyWritten bool
	rollback := func() {
		if passwordWritten {
			rollbackSecret(ctx, s.secrets, HostSecretRef(updated.ID, FieldPassword), previousPassword)
		}
		if proxyWritten {
			rollbackSecret(ctx, s.secrets, HostSecretRef(updated.ID, FieldProxyPassword), previousProxy)
		}
	}

	if passwordChanged {
		if err := s.persistPassword(ctx, updated.ID, normalized.Password); err != nil {
			return domainvault.Host{}, err
		}
		passwordWritten = true
		if s.SecretMode() != SecretModeSession {
			updated.HasPassword = true
		}
	} else {
		updated.HasPassword = existing.HasPassword
	}

	if normalized.Proxy != nil {
		if err := s.persistProxyPassword(ctx, updated.ID, normalized.Proxy); err != nil {
			rollback()
			return domainvault.Host{}, err
		}
		switch {
		case normalized.Proxy.Password != "":
			proxyWritten = true
			updated.Proxy.HasPassword = s.SecretMode() != SecretModeSession
		case existing.Proxy != nil:
			updated.Proxy.HasPassword = existing.Proxy.HasPassword
		}
	} else if s.secrets != nil {
		if err := s.secrets.Delete(ctx, HostSecretRef(updated.ID, FieldProxyPassword)); err != nil &&
			!errors.Is(err, ErrSecretNotFound) {
			rollback()
			return domainvault.Host{}, secretError(err)
		}
		proxyWritten = true
	}

	if err := s.repo.Save(ctx, updated); err != nil {
		rollback()
		return domainvault.Host{}, internal(err)
	}
	return updated, nil
}

// priorSecret is the value a write replaces. err is ErrSecretNotFound when no
// value existed, which tells rollback to delete the new value.
type priorSecret struct {
	value string
	err   error
}

// readSecret reads a previous value only when a write is about to replace it.
func (s *Service) readSecret(ctx context.Context, id model.HostID, field string, needed bool) (priorSecret, error) {
	if !needed || s.secrets == nil {
		return priorSecret{err: ErrSecretNotFound}, nil
	}
	value, err := s.secrets.Get(ctx, HostSecretRef(id, field))
	if err == nil {
		return priorSecret{value: value}, nil
	}
	if errors.Is(err, ErrSecretNotFound) {
		return priorSecret{err: ErrSecretNotFound}, nil
	}
	// Never overwrite ciphertext that cannot be read.
	return priorSecret{}, secretError(err)
}

// rollbackSecret restores a replaced secret or removes a newly written one.
func rollbackSecret(ctx context.Context, secrets FieldSecrets, ref SecretRef, previous priorSecret) {
	if secrets == nil {
		return
	}
	if previous.err == nil {
		_ = secrets.Set(ctx, ref, previous.value)
		return
	}
	_ = secrets.Delete(ctx, ref)
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
	// Remove the Host's credentials best-effort. A locked or unavailable
	// secret store leaves an orphan ciphertext, never plaintext, and must
	// not block deleting the Host.
	if s.secrets != nil {
		for _, field := range []string{FieldPassword, FieldPassphrase, FieldProxyPassword} {
			_ = s.secrets.Delete(ctx, HostSecretRef(id, field))
		}
	}
	return nil
}

// persistPassword encrypts a draft's password into the field-secret store.
// An empty password means "leave the stored credential as it is".
func (s *Service) persistPassword(ctx context.Context, id model.HostID, password string) error {
	if password == "" {
		return nil
	}
	if s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	if err := s.secrets.Set(ctx, HostSecretRef(id, FieldPassword), password); err != nil {
		return secretError(err)
	}
	return nil
}

// persistProxyPassword encrypts a proxy password. An empty value keeps any
// stored password, matching the Host password's update semantics.
func (s *Service) persistProxyPassword(ctx context.Context, id model.HostID, proxy *domainvault.ProxyConfig) error {
	if proxy == nil || proxy.Password == "" {
		return nil
	}
	if s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	if err := s.secrets.Set(ctx, HostSecretRef(id, FieldProxyPassword), proxy.Password); err != nil {
		return secretError(err)
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
	host.IdentityID = draft.IdentityID
	host.KeyID = draft.KeyID
	host.AuthMethod = draft.AuthMethod
	host.UseSSHAgent = draft.UseSSHAgent
	host.RequiresMFA = draft.RequiresMFA
	host.HostChain = domainvault.CloneHostChain(draft.HostChain)
	host.Proxy = domainvault.CloneProxy(draft.Proxy)
	host.LegacyAlgorithms = draft.LegacyAlgorithms
	host.SkipEcdsaHostKey = draft.SkipEcdsaHostKey
	host.Algorithms = domainvault.CloneAlgorithms(draft.Algorithms)
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
