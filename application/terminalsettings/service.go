// Package terminalsettings persists and validates the terminal behavior
// settings. The Settings window edits them through the Service; the
// composition root reads a snapshot whenever it opens a Session or a
// notification arrives.
package terminalsettings

import (
	"context"
	"sync"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Store loads and saves the terminal settings group. The app backs it with a
// JSON file beside the other versioned settings.
type Store interface {
	Load(context.Context) (domainterminal.Settings, error)
	Save(context.Context, domainterminal.Settings) error
}

// Option customizes a Service.
type Option func(*Service)

// WithChangeHook observes successful updates. The app uses it to redraw the
// Settings window and to apply policy changes to new Sessions.
func WithChangeHook(fn func(domainterminal.Settings)) Option {
	return func(s *Service) { s.onChange = fn }
}

// Service owns the loaded settings. It is safe from any goroutine.
type Service struct {
	store    Store
	onChange func(domainterminal.Settings)

	mu       sync.Mutex
	settings domainterminal.Settings
	loaded   bool
}

// NewService returns a Service backed by store. Call Load before reading.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{store: store, settings: domainterminal.Defaults()}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Load reads the persisted settings. A missing or malformed store is the
// store's concern; the Service still exposes the normalized result.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	settings, err := store.Load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = domainterminal.Normalize(settings)
	s.loaded = true
	s.mu.Unlock()
	return nil
}

// Settings returns the current group.
func (s *Service) Settings() domainterminal.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Update applies change to a copy, normalizes it, saves it, and publishes it.
// A save failure leaves the previous value in place, so the UI never shows a
// setting the app is not using.
func (s *Service) Update(change func(*domainterminal.Settings)) error {
	if change == nil {
		return nil
	}
	s.mu.Lock()
	next := s.settings
	change(&next)
	next = domainterminal.Normalize(next)
	store := s.store
	hook := s.onChange
	s.mu.Unlock()

	if store != nil {
		if err := store.Save(context.Background(), next); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.settings = next
	s.loaded = true
	s.mu.Unlock()
	if hook != nil {
		hook(next)
	}
	return nil
}

// SetLoggingEnabled enables or disables automatic Session logging.
func (s *Service) SetLoggingEnabled(enabled bool) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.LoggingEnabled = enabled
	})
}

// SetLogDirectory sets the root log directory. An empty directory disables
// automatic logs without changing the enabled flag.
func (s *Service) SetLogDirectory(directory string) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.LogDirectory = directory
	})
}

// SetLogFormat sets the automatic log format.
func (s *Service) SetLogFormat(format domainterminal.LogFormat) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.LogFormat = format
	})
}

// SetLogTimestamps enables or disables line timestamps in text and HTML logs.
func (s *Service) SetLogTimestamps(enabled bool) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.LogTimestamps = enabled
	})
}

// SetOSC52 sets the OSC 52 clipboard policy.
func (s *Service) SetOSC52(access domainterminal.ClipboardAccess) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.OSC52 = access
	})
}

// SetOSCNotifications sets when program notifications reach the desktop.
func (s *Service) SetOSCNotifications(mode domainterminal.NotificationMode) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.OSCNotifications = mode
	})
}

// EncodingFor returns the encoding a new Session in scope inherits. A Host
// that remembers its last choice wins over the default.
func (s *Service) EncodingFor(scope string) domainterminal.Encoding {
	s.mu.Lock()
	defer s.mu.Unlock()
	if encoding, ok := s.settings.Encodings[scope]; ok && encoding.Valid() {
		return encoding
	}
	return domainterminal.NormalizeEncoding(s.settings.Encoding)
}

// SetEncoding sets the default encoding for Sessions without a remembered
// scope.
func (s *Service) SetEncoding(encoding domainterminal.Encoding) error {
	if !encoding.Valid() {
		return nil
	}
	return s.Update(func(settings *domainterminal.Settings) {
		settings.Encoding = encoding
	})
}

// SetEncodingFor remembers the encoding a Session in scope is using. It is
// called when the user switches encoding in a live Session, so reopening the
// Host inherits the choice as the original did.
func (s *Service) SetEncodingFor(scope string, encoding domainterminal.Encoding) error {
	if scope == "" || !encoding.Valid() {
		return nil
	}
	return s.Update(func(settings *domainterminal.Settings) {
		if settings.Encodings == nil {
			settings.Encodings = make(map[string]domainterminal.Encoding)
		}
		settings.Encodings[scope] = encoding
		if scope == domainterminal.EncodingScopeLocal {
			settings.Encoding = encoding
		}
	})
}

// PasteConfirmation returns the multiline paste policy.
func (s *Service) PasteConfirmation() (bool, int) {
	settings := s.Settings()
	return settings.PasteProtection, domainterminal.NormalizePasteMinLines(settings.PasteMinLines)
}

// SetPasteProtection enables or disables multiline paste confirmation.
func (s *Service) SetPasteProtection(enabled bool) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.PasteProtection = enabled
	})
}

// SetPasteMinLines sets the line count at which a protected paste asks.
func (s *Service) SetPasteMinLines(lines int) error {
	return s.Update(func(settings *domainterminal.Settings) {
		settings.PasteMinLines = domainterminal.NormalizePasteMinLines(lines)
	})
}

// SetDropAction sets what dropping files on a terminal does.
func (s *Service) SetDropAction(action domainterminal.DropAction) error {
	if !action.Valid() {
		return nil
	}
	return s.Update(func(settings *domainterminal.Settings) {
		settings.DropAction = action
	})
}
