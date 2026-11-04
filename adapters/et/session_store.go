// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/bootstrap"
	appvault "github.com/mslxl/natcatty/application/vault"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
)

// secretCleanupTimeout bounds clearing a saved session when a transport closes.
// The store is local; a stuck delete must not block app shutdown.
const secretCleanupTimeout = 5 * time.Second

// SessionRecord is the credential pair a resumable ET session keeps in the
// secret store. It is secret material: never log it or put it in a workspace
// descriptor. The non-secret resume key locates it.
type SessionRecord struct {
	ID      string
	Passkey string
}

// SessionStore persists resumable ET session credentials. A store returns
// appvault.ErrSecretNotFound when no record exists for a resume key. The
// InProcessFactory never logs the record and clears it on an explicit Session
// close.
type SessionStore interface {
	Save(ctx context.Context, resumeKey string, record SessionRecord) error
	Load(ctx context.Context, resumeKey string) (SessionRecord, error)
	Delete(ctx context.Context, resumeKey string) error
}

// resumeSession loads a saved credential pair for resumeKey. A missing record
// starts a fresh session; any other store failure is typed and never contains
// the credentials.
func (f InProcessFactory) resumeSession(ctx context.Context, resumeKey string) (bool, bootstrap.Credentials, error) {
	if f.Sessions == nil || resumeKey == "" {
		return false, bootstrap.Credentials{}, nil
	}
	record, err := f.Sessions.Load(ctx, resumeKey)
	if errors.Is(err, appvault.ErrSecretNotFound) {
		return false, bootstrap.Credentials{}, nil
	}
	if err != nil {
		return false, bootstrap.Credentials{}, fmt.Errorf("%w: read saved credentials: %w",
			etmodel.ErrSessionUnavailable, err)
	}
	if record.ID == "" || record.Passkey == "" {
		return false, bootstrap.Credentials{}, fmt.Errorf("%w: saved credentials are incomplete",
			etmodel.ErrSessionUnavailable)
	}
	return true, bootstrap.NewCredentials(record.ID, record.Passkey), nil
}

// saveSession persists a freshly bootstrapped pair. Without a store or a key
// the session stays memory-only.
func (f InProcessFactory) saveSession(ctx context.Context, resumeKey string, credentials bootstrap.Credentials) error {
	if f.Sessions == nil || resumeKey == "" {
		return nil
	}
	record := SessionRecord{ID: credentials.ID, Passkey: credentials.Passkey()}
	if err := f.Sessions.Save(ctx, resumeKey, record); err != nil {
		return fmt.Errorf("et: save resumable session: %w", err)
	}
	return nil
}

// clearSession removes a saved pair after it is known to be unusable. Deletion
// failures are logged without values; a later successful Save overwrites the
// leftovers.
func (f InProcessFactory) clearSession(ctx context.Context, resumeKey string) {
	if f.Sessions == nil || resumeKey == "" {
		return
	}
	if err := f.Sessions.Delete(ctx, resumeKey); err != nil {
		f.logger().Debug("et: unusable saved session could not be cleared", "err", err)
	}
}

// forgetSession returns the transport's explicit-close cleanup. It is nil when
// nothing can have been saved.
func (f InProcessFactory) forgetSession(resumeKey string) func() {
	if f.Sessions == nil || resumeKey == "" {
		return nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), secretCleanupTimeout)
		defer cancel()
		f.clearSession(ctx, resumeKey)
	}
}

// SecretSessionStore keeps ET session credentials in the existing encrypted
// field-secret store. The record's entity id is the opaque resume key from the
// workspace descriptor; the ET session id and passkey are separate encrypted
// fields, so neither ever appears in workspace JSON.
type SecretSessionStore struct {
	Secrets appvault.FieldSecrets
}

var _ SessionStore = SecretSessionStore{}

// Save writes both halves of the record. A failure after the id was written
// removes it again, so a load never observes a half record.
func (s SecretSessionStore) Save(ctx context.Context, resumeKey string, record SessionRecord) error {
	if s.Secrets == nil {
		return errors.New("et: no secret store for ET session credentials")
	}
	if resumeKey == "" || record.ID == "" || record.Passkey == "" {
		// The message intentionally identifies no value.
		return errors.New("et: incomplete ET session credentials")
	}
	if err := s.Secrets.Set(ctx, appvault.ETSessionSecretRef(resumeKey, appvault.FieldETSessionID), record.ID); err != nil {
		return fmt.Errorf("et: save ET session id: %w", err)
	}
	if err := s.Secrets.Set(ctx, appvault.ETSessionSecretRef(resumeKey, appvault.FieldETSessionKey), record.Passkey); err != nil {
		_ = s.Secrets.Delete(ctx, appvault.ETSessionSecretRef(resumeKey, appvault.FieldETSessionID))
		return fmt.Errorf("et: save ET session passkey: %w", err)
	}
	return nil
}

// Load reads both halves of the record. A missing half is reported as
// appvault.ErrSecretNotFound, like a missing record: the caller starts a fresh
// session and overwrites the leftovers.
func (s SecretSessionStore) Load(ctx context.Context, resumeKey string) (SessionRecord, error) {
	if s.Secrets == nil {
		return SessionRecord{}, appvault.ErrSecretNotFound
	}
	id, err := s.Secrets.Get(ctx, appvault.ETSessionSecretRef(resumeKey, appvault.FieldETSessionID))
	if err != nil {
		return SessionRecord{}, err
	}
	passkey, err := s.Secrets.Get(ctx, appvault.ETSessionSecretRef(resumeKey, appvault.FieldETSessionKey))
	if err != nil {
		return SessionRecord{}, err
	}
	return SessionRecord{ID: id, Passkey: passkey}, nil
}

// Delete removes both halves. A half or entirely absent record is fine; any
// other store failure is reported so the caller can log it without the values.
func (s SecretSessionStore) Delete(ctx context.Context, resumeKey string) error {
	if s.Secrets == nil {
		return nil
	}
	var errs []error
	for _, field := range []string{appvault.FieldETSessionID, appvault.FieldETSessionKey} {
		err := s.Secrets.Delete(ctx, appvault.ETSessionSecretRef(resumeKey, field))
		if err != nil && !errors.Is(err, appvault.ErrSecretNotFound) {
			errs = append(errs, fmt.Errorf("et: delete ET session field %s: %w", field, err))
		}
	}
	return errors.Join(errs...)
}
