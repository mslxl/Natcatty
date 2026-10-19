// Package applock owns the App Lock use case: loading the verifier, locking
// and unlocking the UI gate, and closing it after a configured idle timeout.
// It consumes a settings repository port so the file adapter stays outside
// this package.
package applock

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"

	domainapplock "github.com/mslxl/natcatty/domain/applock"
)

// Repository is the persistence port App Lock consumes.
type Repository interface {
	Load(ctx context.Context) (domainapplock.Settings, error)
	Save(ctx context.Context, settings domainapplock.Settings) error
}

// Reason says why the gate is closed. It is not user-visible text.
type Reason string

const (
	// ReasonStartup is the lock applied when a locked app starts.
	ReasonStartup Reason = "startup"
	// ReasonIdle is the lock applied after the idle timeout.
	ReasonIdle Reason = "idle"
	// ReasonManual is the lock the user asks for.
	ReasonManual Reason = "manual"
)

// Errors the UI maps to localized messages.
var (
	ErrIncorrectPassword = errors.New("applock: incorrect password")
	ErrEmptyPassword     = errors.New("applock: empty password")
	ErrLockDisabled      = errors.New("applock: lock disabled")
)

// Snapshot is the App Lock state the UI renders.
type Snapshot struct {
	Enabled        bool
	Locked         bool
	Reason         Reason
	TimeoutMinutes int
	CanLock        bool
}

// Service owns App Lock state. It is safe from any goroutine.
type Service struct {
	mu           sync.Mutex
	repo         Repository
	settings     domainapplock.Settings
	locked       bool
	reason       Reason
	lastActivity time.Time
	now          func() time.Time
	random       io.Reader
	notify       func()
}

// Option customizes a Service.
type Option func(*Service)

// WithClock sets the service's source of time. Tests use it to make idle
// timeouts deterministic.
func WithClock(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.now = fn
		}
	}
}

// WithRandom sets the source of the verifier salt. Tests use it to make
// verifier records deterministic.
func WithRandom(reader io.Reader) Option {
	return func(s *Service) {
		if reader != nil {
			s.random = reader
		}
	}
}

// WithRedrawRequest sets the function called after App Lock state changes.
func WithRedrawRequest(fn func()) Option {
	return func(s *Service) { s.notify = fn }
}

// NewService returns a Service with App Lock disabled until Load reads the
// stored settings.
func NewService(repo Repository, opts ...Option) *Service {
	service := &Service{
		repo:     repo,
		settings: domainapplock.DefaultSettings(),
		now:      time.Now,
		random:   rand.Reader,
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Load reads the stored settings. An enabled App Lock starts locked for the
// startup reason, matching the original app.
func (s *Service) Load(ctx context.Context) error {
	settings, err := s.repo.Load(ctx)
	if err != nil {
		return err
	}
	now := s.now()

	s.mu.Lock()
	s.settings = domainapplock.NormalizeSettings(settings)
	s.lastActivity = now
	s.locked = s.settings.CanLock()
	s.reason = ""
	if s.locked {
		s.reason = ReasonStartup
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

// Snapshot returns the current state.
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Enabled:        s.settings.Enabled,
		Locked:         s.locked,
		Reason:         s.reason,
		TimeoutMinutes: s.settings.TimeoutMinutes,
		CanLock:        s.settings.CanLock(),
	}
}

// Settings returns a copy of the normalized settings.
func (s *Service) Settings() domainapplock.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := s.settings
	if settings.PasswordVerifier != nil {
		verifier := *settings.PasswordVerifier
		verifier.Salt = append([]byte(nil), verifier.Salt...)
		verifier.Hash = append([]byte(nil), verifier.Hash...)
		settings.PasswordVerifier = &verifier
	}
	return settings
}

// Enable creates a verifier for password and turns App Lock on. The current
// session stays unlocked; the next start begins locked.
func (s *Service) Enable(ctx context.Context, password string) error {
	verifier, err := domainapplock.NewVerifier(password, s.random)
	if err != nil {
		if errors.Is(err, domainapplock.ErrPasswordRequired) {
			return ErrEmptyPassword
		}
		return err
	}

	s.mu.Lock()
	settings := domainapplock.NormalizeSettings(domainapplock.Settings{
		Enabled:          true,
		TimeoutMinutes:   s.settings.TimeoutMinutes,
		PasswordVerifier: verifier,
	})
	s.mu.Unlock()

	if err := s.repo.Save(ctx, settings); err != nil {
		return err
	}

	s.mu.Lock()
	s.settings = settings
	s.locked = false
	s.reason = ""
	s.lastActivity = s.now()
	s.mu.Unlock()
	s.changed()
	return nil
}

// Disable turns App Lock off after checking the current password.
func (s *Service) Disable(ctx context.Context, currentPassword string) error {
	s.mu.Lock()
	verifier := s.settings.PasswordVerifier
	s.mu.Unlock()
	if verifier == nil || !verifier.Verify(currentPassword) {
		return ErrIncorrectPassword
	}

	s.mu.Lock()
	settings := domainapplock.NormalizeSettings(domainapplock.Settings{
		TimeoutMinutes: s.settings.TimeoutMinutes,
	})
	s.mu.Unlock()
	if err := s.repo.Save(ctx, settings); err != nil {
		return err
	}

	s.mu.Lock()
	s.settings = settings
	s.locked = false
	s.reason = ""
	s.mu.Unlock()
	s.changed()
	return nil
}

// SetTimeout stores an offered idle timeout. An unsupported value falls back
// to the default.
func (s *Service) SetTimeout(ctx context.Context, minutes int) error {
	s.mu.Lock()
	settings := s.settings
	settings.TimeoutMinutes = domainapplock.NormalizeTimeoutMinutes(minutes)
	s.mu.Unlock()

	if err := s.repo.Save(ctx, settings); err != nil {
		return err
	}

	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	s.changed()
	return nil
}

// Lock closes the gate. It needs an enabled App Lock.
func (s *Service) Lock(reason Reason) error {
	s.mu.Lock()
	if !s.settings.CanLock() {
		s.mu.Unlock()
		return ErrLockDisabled
	}
	s.locked = true
	s.reason = reason
	s.mu.Unlock()
	s.changed()
	return nil
}

// Unlock opens the gate after checking password.
func (s *Service) Unlock(password string) error {
	s.mu.Lock()
	if !s.settings.CanLock() {
		s.mu.Unlock()
		return ErrLockDisabled
	}
	if password == "" {
		s.mu.Unlock()
		return ErrEmptyPassword
	}
	verifier := s.settings.PasswordVerifier
	s.mu.Unlock()

	if !verifier.Verify(password) {
		return ErrIncorrectPassword
	}

	s.mu.Lock()
	s.locked = false
	s.reason = ""
	s.lastActivity = s.now()
	s.mu.Unlock()
	s.changed()
	return nil
}

// RecordActivity notes user input while the gate is open, postponing the idle
// lock.
func (s *Service) RecordActivity() {
	s.mu.Lock()
	if !s.locked {
		s.lastActivity = s.now()
	}
	s.mu.Unlock()
}

// LockIfIdle closes the gate when the configured idle timeout has passed. It
// reports whether it locked.
func (s *Service) LockIfIdle(now time.Time) bool {
	s.mu.Lock()
	if s.locked || !s.settings.CanLock() || s.settings.TimeoutMinutes <= 0 {
		s.mu.Unlock()
		return false
	}
	elapsed := int64(now.Sub(s.lastActivity) / time.Second)
	if !s.settings.ShouldLockAfterIdle(elapsed) {
		s.mu.Unlock()
		return false
	}
	s.locked = true
	s.reason = ReasonIdle
	s.mu.Unlock()
	s.changed()
	return true
}

// IdleDeadline returns when the idle lock is due, while it can still engage.
func (s *Service) IdleDeadline() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked || !s.settings.CanLock() || s.settings.TimeoutMinutes <= 0 {
		return time.Time{}, false
	}
	return s.lastActivity.Add(time.Duration(s.settings.TimeoutMinutes) * time.Minute), true
}

func (s *Service) changed() {
	s.mu.Lock()
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}
