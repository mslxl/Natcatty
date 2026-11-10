package plugin

import (
	"context"
	"fmt"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// MaxClipboardBytes bounds one clipboard read or write. The guest cannot use
// the clipboard as a bulk channel.
const MaxClipboardBytes = 1 << 20

// Clipboard is the consumer-owned port over the host clipboard. The app
// implements it with the UI toolkit; tests supply a memory implementation.
type Clipboard interface {
	ReadText(ctx context.Context) (string, error)
	WriteText(ctx context.Context, text string) error
}

// MemoryClipboard is a deterministic Clipboard for tests and for a
// composition that has no desktop clipboard.
type MemoryClipboard struct {
	mu   sync.Mutex
	text string
}

// NewMemoryClipboard returns a clipboard holding initial.
func NewMemoryClipboard(initial string) *MemoryClipboard {
	return &MemoryClipboard{text: initial}
}

// ReadText implements Clipboard.
func (c *MemoryClipboard) ReadText(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text, nil
}

// WriteText implements Clipboard.
func (c *MemoryClipboard) WriteText(_ context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.text = text
	return nil
}

// ClipboardRead returns the current clipboard text. It requires the distinct
// clipboard.read permission and is audited.
func (s *ServiceSession) ClipboardRead(ctx context.Context) (string, error) {
	if err := s.check(ServiceClipboard, "read",
		domainplugin.PermissionClipboardRead, domainplugin.Scope{}); err != nil {
		return "", err
	}
	if s.host.clipboard == nil {
		return "", s.fail(ServiceClipboard, "read",
			domainplugin.PermissionClipboardRead, domainplugin.Scope{}, "unavailable",
			serviceError(ServiceClipboard, "read", domainplugin.PermissionClipboardRead,
				domainplugin.Scope{}, ErrServiceUnavailable))
	}
	text, err := s.host.clipboard.ReadText(ctx)
	if err != nil {
		return "", s.fail(ServiceClipboard, "read",
			domainplugin.PermissionClipboardRead, domainplugin.Scope{}, "fault",
			serviceError(ServiceClipboard, "read", domainplugin.PermissionClipboardRead,
				domainplugin.Scope{}, err))
	}
	if len(text) > MaxClipboardBytes {
		limited := &LimitError{Resource: "clipboard bytes", Limit: MaxClipboardBytes}
		return "", s.fail(ServiceClipboard, "read",
			domainplugin.PermissionClipboardRead, domainplugin.Scope{}, "limit",
			serviceError(ServiceClipboard, "read", domainplugin.PermissionClipboardRead,
				domainplugin.Scope{}, limited))
	}
	s.record(ServiceClipboard, "read", domainplugin.PermissionClipboardRead,
		domainplugin.Scope{}, ServiceCodeOK)
	return text, nil
}

// ClipboardWrite replaces the clipboard. It requires the distinct
// clipboard.write permission and is audited.
func (s *ServiceSession) ClipboardWrite(ctx context.Context, text string) error {
	if err := s.check(ServiceClipboard, "write",
		domainplugin.PermissionClipboardWrite, domainplugin.Scope{}); err != nil {
		return err
	}
	if len(text) > MaxClipboardBytes {
		limited := &LimitError{Resource: "clipboard bytes", Limit: MaxClipboardBytes}
		return s.fail(ServiceClipboard, "write",
			domainplugin.PermissionClipboardWrite, domainplugin.Scope{}, "limit",
			serviceError(ServiceClipboard, "write", domainplugin.PermissionClipboardWrite,
				domainplugin.Scope{}, limited))
	}
	if s.host.clipboard == nil {
		return s.fail(ServiceClipboard, "write",
			domainplugin.PermissionClipboardWrite, domainplugin.Scope{}, "unavailable",
			serviceError(ServiceClipboard, "write", domainplugin.PermissionClipboardWrite,
				domainplugin.Scope{}, ErrServiceUnavailable))
	}
	if err := s.host.clipboard.WriteText(ctx, text); err != nil {
		return s.fail(ServiceClipboard, "write",
			domainplugin.PermissionClipboardWrite, domainplugin.Scope{}, "fault",
			serviceError(ServiceClipboard, "write", domainplugin.PermissionClipboardWrite,
				domainplugin.Scope{}, err))
	}
	s.record(ServiceClipboard, "write", domainplugin.PermissionClipboardWrite,
		domainplugin.Scope{}, ServiceCodeOK)
	return nil
}

// HostSettings is the consumer-owned port over the host's read-only settings
// namespace. Keys are stable strings such as "app.locale"; there is no write
// path in v1.
type HostSettings interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// MemoryHostSettings is a deterministic HostSettings for tests.
type MemoryHostSettings struct {
	Values map[string]string
}

// Get implements HostSettings.
func (m MemoryHostSettings) Get(_ context.Context, key string) (string, bool, error) {
	value, ok := m.Values[key]
	return value, ok, nil
}

// SettingsRead returns one read-only host setting. It requires the
// key-scoped settings.read permission.
func (s *ServiceSession) SettingsRead(ctx context.Context, key string) (string, error) {
	scope := domainplugin.Scope{Kind: domainplugin.ScopeSettings, Key: key}
	if key == "" {
		return "", s.fail(ServiceSettings, "read", domainplugin.PermissionSettingsRead,
			scope, ServiceCodeInvalid,
			serviceError(ServiceSettings, "read", domainplugin.PermissionSettingsRead,
				scope, &StorageKeyError{Problem: "a settings key is required"}))
	}
	if err := s.check(ServiceSettings, "read",
		domainplugin.PermissionSettingsRead, scope); err != nil {
		return "", err
	}
	if s.host.hostSettings == nil {
		return "", s.fail(ServiceSettings, "read", domainplugin.PermissionSettingsRead,
			scope, "unavailable",
			serviceError(ServiceSettings, "read", domainplugin.PermissionSettingsRead,
				scope, ErrServiceUnavailable))
	}
	value, ok, err := s.host.hostSettings.Get(ctx, key)
	if err != nil {
		return "", s.fail(ServiceSettings, "read", domainplugin.PermissionSettingsRead,
			scope, "fault",
			serviceError(ServiceSettings, "read", domainplugin.PermissionSettingsRead, scope, err))
	}
	if !ok {
		return "", s.fail(ServiceSettings, "read", domainplugin.PermissionSettingsRead,
			scope, ServiceCodeNotFound,
			&ServiceError{Service: ServiceSettings, Method: "read", Code: ServiceCodeNotFound,
				Permission: domainplugin.PermissionSettingsRead, Scope: scope,
				Message: fmt.Sprintf("host setting %q is not available", key),
				Cause:   &StorageNotFoundError{Key: key}})
	}
	s.record(ServiceSettings, "read", domainplugin.PermissionSettingsRead, scope, ServiceCodeOK)
	return value, nil
}
