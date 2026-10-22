// Package vaultbackup implements local Vault backups: automatic restore
// points after committed Vault changes, an encrypted on-disk history, and the
// protective backup that every restore or bulk apply takes first.
//
// The package declares the ports it consumes (Snapshotter, Store,
// RetentionStore), so the SQLite adapter and the encrypted-file adapter stay
// outside the use case. Nothing here imports MyGo, a database, or a UI.
package vaultbackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

// Snapshotter reads and writes the Vault's complete logical state in one
// transaction. The SQLite adapter implements it.
type Snapshotter interface {
	// Snapshot returns a consistent image of every Vault table.
	Snapshot(ctx context.Context) (domainbackup.Snapshot, error)
	// Validate rejects a snapshot that this build cannot restore, such as one
	// written against a different schema version.
	Validate(ctx context.Context, snapshot domainbackup.Snapshot) error
	// Restore replaces the Vault in one transaction. On any error the Vault
	// must remain exactly as it was before the call.
	Restore(ctx context.Context, snapshot domainbackup.Snapshot) error
}

// SaveInput is one backup the Store encrypts and writes.
type SaveInput struct {
	ID               string
	CreatedAt        int64
	Reason           domainbackup.Reason
	SourceAppVersion string
	TargetAppVersion string
	Fingerprint      string
	// Payload is the canonical snapshot bytes. The Store encrypts exactly
	// these bytes and records their length as the backup's size.
	Payload []byte
	Counts  domainbackup.Counts
}

// Store is the encrypted backup history. List returns newest first.
type Store interface {
	List(ctx context.Context) ([]domainbackup.Summary, error)
	Save(ctx context.Context, input SaveInput) (domainbackup.Summary, error)
	Read(ctx context.Context, id string) (domainbackup.Summary, []byte, error)
	Prune(ctx context.Context, keep int) (int, error)
	Clear(ctx context.Context) (int, error)
	EncryptionAvailable() bool
}

// RetentionStore persists the user's retention choice between launches.
type RetentionStore interface {
	Load() int
	SaveRetention(retention int) error
}

// Errors the UI localizes.
var (
	// ErrEncryptionUnavailable reports that no secure keychain can hold the
	// backup key, so a plaintext backup would be the only alternative. The
	// service refuses that.
	ErrEncryptionUnavailable = errors.New("vaultbackup: secure keychain unavailable")
	// ErrPayloadTooLarge reports a snapshot above the 25 MiB cap.
	ErrPayloadTooLarge = errors.New("vaultbackup: payload exceeds the size cap")
	// ErrNotFound reports an unknown backup id.
	ErrNotFound = errors.New("vaultbackup: backup not found")
	// ErrIncompatibleSnapshot reports a backup this build cannot restore.
	ErrIncompatibleSnapshot = errors.New("vaultbackup: incompatible snapshot")
	// ErrCorrupt reports a backup whose payload fails verification.
	ErrCorrupt = errors.New("vaultbackup: backup failed verification")
)

// ProtectiveBackupError reports that a destructive operation was aborted
// because its mandatory protective backup could not be written. The Vault is
// unchanged.
type ProtectiveBackupError struct{ Cause error }

func (e *ProtectiveBackupError) Error() string {
	return "vaultbackup: protective backup failed: " + e.Cause.Error()
}

func (e *ProtectiveBackupError) Unwrap() error { return e.Cause }

// StatusKind names the last backup outcome the panel reports.
type StatusKind string

const (
	StatusNone               StatusKind = ""
	StatusCreated            StatusKind = "created"
	StatusDeduped            StatusKind = "deduped"
	StatusSkippedTooLarge    StatusKind = "skipped_too_large"
	StatusSkippedUnavailable StatusKind = "skipped_unavailable"
	StatusSkippedFailed      StatusKind = "skipped_failed"
	StatusRetentionSaved     StatusKind = "retention_saved"
	StatusCleared            StatusKind = "cleared"
	StatusRestored           StatusKind = "restored"
	StatusRestoreFailed      StatusKind = "restore_failed"
)

// Status is the most recent backup outcome. It exists so a skipped or pruned
// automatic backup is visible to the user instead of failing silently.
type Status struct {
	Kind StatusKind
	// Pruned is how many older entries retention removed.
	Pruned int
	// Deleted is how many entries an explicit clear removed. It is separate
	// from Pruned so the panel can report the two actions independently
	// instead of counting the same entries twice.
	Deleted int
	Detail  string
}

// CreateResult reports one backup attempt.
type CreateResult struct {
	Created bool
	Backup  domainbackup.Summary
	Pruned  int
}

// Option customizes a Service.
type Option func(*Service)

// WithClock sets the time source. Tests pin it.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithIDGenerator sets the backup id source. Tests pin it.
func WithIDGenerator(next func() string) Option {
	return func(s *Service) {
		if next != nil {
			s.newID = next
		}
	}
}

// WithRetentionStore sets where the retention choice is persisted. Without
// one the service keeps the default in memory.
func WithRetentionStore(store RetentionStore) Option {
	return func(s *Service) {
		if store != nil {
			s.retentionStore = store
			s.retention = domainbackup.SanitizeRetention(store.Load())
		}
	}
}

// Service owns the backup policy and serializes every backup mutation.
type Service struct {
	snapshotter    Snapshotter
	store          Store
	retentionStore RetentionStore
	now            func() time.Time
	newID          func() string

	mu          sync.Mutex
	retention   int
	pending     atomic.Bool
	suppress    atomic.Bool
	last        Status
	started     bool
	stop        chan struct{}
	done        chan struct{}
	wake        chan struct{}
	workerOnce  sync.Once
	stoppedOnce sync.Once
}

// NewService returns a Service that snapshots the Vault and stores backups.
func NewService(snapshotter Snapshotter, store Store, opts ...Option) *Service {
	service := &Service{
		snapshotter: snapshotter,
		store:       store,
		now:         time.Now,
		newID:       randomID,
		retention:   domainbackup.DefaultRetention,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		wake:        make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		// crypto/rand failing is unrecoverable for a security feature; fall
		// back to the clock so the caller still gets a unique-enough id.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

// Start begins the automatic-backup worker. It coalesces notifications: a
// burst of Vault writes produces as few snapshots as the worker can take
// while writes keep arriving, and never uses a timer.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	s.workerOnce.Do(func() {
		go s.loop()
	})
}

// Stop ends the worker and waits for the current backup to finish.
func (s *Service) Stop() {
	s.stoppedOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
}

func (s *Service) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
			for {
				if !s.pending.Swap(false) {
					break
				}
				if s.suppress.Load() {
					continue
				}
				// Create records its own status. An automatic backup failure
				// must never reach the user as a crash: the panel shows it.
				_, _ = s.Create(context.Background(), domainbackup.ReasonVaultChange, "", "")
			}
		}
	}
}

// NotifyVaultChanged asks for an automatic backup after a committed Vault
// change. It never blocks the mutation and never starts work while a restore
// holds the barrier: the pending flag and the wake channel are the whole
// hand-off, so a slow snapshot cannot stall the UI.
func (s *Service) NotifyVaultChanged() {
	if s.suppress.Load() {
		return
	}
	s.pending.Store(true)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// LastStatus returns the most recent outcome for the panel.
func (s *Service) LastStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *Service) setStatus(status Status) {
	s.last = status
}

// Retention reports the active limit.
func (s *Service) Retention() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retention
}

// SetRetention persists a new limit, clamps it to 1-100, and prunes the
// history immediately. It returns the applied limit and how many backups were
// removed.
func (s *Service) SetRetention(ctx context.Context, value int) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	applied := domainbackup.SanitizeRetention(value)
	if s.retentionStore != nil {
		if err := s.retentionStore.SaveRetention(applied); err != nil {
			return s.retention, 0, fmt.Errorf("vaultbackup: save retention: %w", err)
		}
	}
	s.retention = applied
	pruned, err := s.store.Prune(ctx, applied)
	if err != nil {
		s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		return applied, 0, err
	}
	s.setStatus(Status{Kind: StatusRetentionSaved, Pruned: pruned})
	return applied, pruned, nil
}

// EncryptionAvailable reports whether the keychain can hold the backup key.
func (s *Service) EncryptionAvailable() bool { return s.store.EncryptionAvailable() }

// Create takes a backup for reason. Automatic callers ignore the error and
// read LastStatus; protective callers must abort on it.
func (s *Service) Create(ctx context.Context, reason domainbackup.Reason,
	sourceVersion, targetVersion string) (CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(ctx, reason, sourceVersion, targetVersion, false)
}

// CreateProtective takes the mandatory backup before a restore or bulk apply.
func (s *Service) CreateProtective(ctx context.Context) (CreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(ctx, domainbackup.ReasonBeforeRestore, "", "", true)
}

func (s *Service) createLocked(ctx context.Context, reason domainbackup.Reason,
	sourceVersion, targetVersion string, allowEmpty bool) (CreateResult, error) {

	if !reason.Valid() {
		return CreateResult{}, fmt.Errorf("vaultbackup: unknown reason %q", reason)
	}
	if !s.store.EncryptionAvailable() {
		s.setStatus(Status{Kind: StatusSkippedUnavailable})
		return CreateResult{}, ErrEncryptionUnavailable
	}
	snapshot, err := s.snapshotter.Snapshot(ctx)
	if err != nil {
		s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		return CreateResult{}, fmt.Errorf("vaultbackup: snapshot: %w", err)
	}
	if snapshot.Empty() && !allowEmpty {
		// An empty Vault has nothing to restore from. The original skipped
		// this case too; a protective backup may still be written so the
		// restore barrier always has its named restore point.
		return CreateResult{}, nil
	}
	payload, err := domainbackup.CanonicalBytes(snapshot)
	if err != nil {
		s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		return CreateResult{}, err
	}
	if len(payload) > domainbackup.MaxPayloadBytes {
		s.setStatus(Status{Kind: StatusSkippedTooLarge})
		return CreateResult{}, ErrPayloadTooLarge
	}
	fingerprint, err := domainbackup.Fingerprint(snapshot)
	if err != nil {
		s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		return CreateResult{}, err
	}

	existing, err := s.store.List(ctx)
	if err != nil {
		s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		return CreateResult{}, err
	}
	// Dedupe compares only the newest record, as the original did. An
	// identical payload keeps that existing entry: it is not rewritten, and
	// its timestamp and reason are not refreshed.
	if len(existing) > 0 && existing[0].Fingerprint == fingerprint {
		s.setStatus(Status{Kind: StatusDeduped})
		return CreateResult{Backup: existing[0]}, nil
	}

	createdAt := s.now().UnixMilli()
	if len(existing) > 0 && createdAt <= existing[0].CreatedAt {
		createdAt = existing[0].CreatedAt + 1
	}
	summary, err := s.store.Save(ctx, SaveInput{
		ID:               s.newID(),
		CreatedAt:        createdAt,
		Reason:           reason,
		SourceAppVersion: sourceVersion,
		TargetAppVersion: targetVersion,
		Fingerprint:      fingerprint,
		Payload:          payload,
		Counts:           snapshot.Counts(),
	})
	if err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			// The Store enforces the same cap; "unavailable" would wrongly
			// blame the keychain.
			s.setStatus(Status{Kind: StatusSkippedTooLarge})
		} else if errors.Is(err, ErrEncryptionUnavailable) {
			s.setStatus(Status{Kind: StatusSkippedUnavailable})
		} else {
			s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		}
		return CreateResult{}, err
	}

	pruned, err := s.store.Prune(ctx, s.retention)
	if err != nil {
		// The backup exists; failing to trim older entries must not turn a
		// successful backup into a failure. The next prune retries.
		s.setStatus(Status{Kind: StatusCreated, Detail: err.Error()})
		return CreateResult{Created: true, Backup: summary}, nil
	}
	s.setStatus(Status{Kind: StatusCreated, Pruned: pruned})
	return CreateResult{Created: true, Backup: summary, Pruned: pruned}, nil
}

// List returns the backup history, newest first.
func (s *Service) List(ctx context.Context) ([]domainbackup.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.List(ctx)
}

// Clear deletes every backup. It does not touch the Vault.
func (s *Service) Clear(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted, err := s.store.Clear(ctx)
	if err != nil {
		s.setStatus(Status{Kind: StatusSkippedFailed, Detail: err.Error()})
		return deleted, err
	}
	s.setStatus(Status{Kind: StatusCleared, Deleted: deleted})
	return deleted, nil
}

// Restore replaces the Vault with the backup id. It verifies the payload,
// takes a mandatory protective backup of the current Vault, and only then
// applies the snapshot in one transaction. Any failure leaves the Vault as it
// was.
func (s *Service) Restore(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, payload, err := s.store.Read(ctx, id)
	if err != nil {
		s.setStatus(Status{Kind: StatusRestoreFailed, Detail: err.Error()})
		return err
	}
	snapshot, err := domainbackup.Parse(payload)
	if err != nil {
		s.setStatus(Status{Kind: StatusRestoreFailed, Detail: err.Error()})
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := s.snapshotter.Validate(ctx, snapshot); err != nil {
		s.setStatus(Status{Kind: StatusRestoreFailed, Detail: err.Error()})
		return fmt.Errorf("%w: %v", ErrIncompatibleSnapshot, err)
	}

	// The protective backup is mandatory. An empty Vault still gets one so a
	// restore always has a named, before_restore restore point.
	if _, err := s.createLocked(ctx, domainbackup.ReasonBeforeRestore, "", "", true); err != nil {
		s.setStatus(Status{Kind: StatusRestoreFailed, Detail: err.Error()})
		return &ProtectiveBackupError{Cause: err}
	}

	// The restore's own writes must not queue another automatic backup. A
	// notification that arrives concurrently is dropped: the restored state
	// is already the target backup.
	s.suppress.Store(true)
	err = func() error {
		defer s.suppress.Store(false)
		return s.snapshotter.Restore(ctx, snapshot)
	}()
	if err != nil {
		s.setStatus(Status{Kind: StatusRestoreFailed, Detail: err.Error()})
		return fmt.Errorf("vaultbackup: restore: %w", err)
	}
	s.setStatus(Status{Kind: StatusRestored})
	return nil
}
