package desktop

import (
	"context"
	"fmt"
	"sync"
)

// Repository persists the desktop settings. The composition root uses a JSON
// file beside the App Lock settings; tests use an in-memory fake.
type Repository interface {
	Load(ctx context.Context) (Settings, error)
	Save(ctx context.Context, settings Settings) error
}

// Hotkeys registers system-wide accelerators. MyGo's GlobalShortcut module
// implements it; tests use a fake.
type Hotkeys interface {
	Register(accelerator string, fn func()) error
	Unregister(accelerator string)
}

// AutoLauncher manages launch at login through the operating system. It is the
// source of truth for the effective state.
type AutoLauncher interface {
	Supported() bool
	Enabled() (bool, error)
	SetEnabled(enabled bool) error
}

// Notifier shows a desktop notification. The adapter localizes the typed
// request; it never receives user-visible text from this package.
type Notifier interface {
	Supported() bool
	Notify(notification Notification) error
}

// Option customizes a Service.
type Option func(*Service)

// WithGOOS sets the platform whose defaults and accelerator spelling apply.
func WithGOOS(goos string) Option {
	return func(s *Service) {
		if goos != "" {
			s.goos = goos
		}
	}
}

// Service owns the persisted settings and applies them to the OS ports. It is
// safe from any goroutine.
type Service struct {
	mu     sync.Mutex
	repo   Repository
	hotkey Hotkeys
	auto   AutoLauncher
	notify Notifier
	goos   string

	settings Settings
	caps     Capabilities

	// toggle is the production hotkey action. It is invoked on the UI thread
	// by the hotkey callback.
	toggle func()
	// onModeChange is notified after a hotkey mode change has been applied and
	// saved. The composition root uses it to retire the Dropdown Panel.
	onModeChange func(HotkeyMode)
	// onSourceChange is notified after a Quake content-source change has been
	// saved. The composition root uses it to retire the Dropdown Panel so the
	// main window's render ownership is restored under the new route.
	onSourceChange func(QuakeSource)
	// registered is the accelerator currently claimed from Hotkeys.
	registered string
	// hotkeyProblem is the last registration failure, surfaced in Settings.
	hotkeyProblem error
}

// NewService builds the desktop use cases. Any port may be nil; the matching
// capability is then false and the setter returns ErrUnsupported.
func NewService(repo Repository, hotkeys Hotkeys, auto AutoLauncher, notifier Notifier, opts ...Option) *Service {
	service := &Service{
		repo:     repo,
		hotkey:   hotkeys,
		auto:     auto,
		notify:   notifier,
		goos:     "windows",
		settings: Defaults("windows"),
	}
	for _, opt := range opts {
		opt(service)
	}
	service.settings = Defaults(service.goos)
	service.caps = Capabilities{
		Tray:         true,
		AutoStart:    auto != nil && auto.Supported(),
		GlobalHotkey: hotkeys != nil,
		Dropdown:     DropdownSupported(service.goos),
	}
	return service
}

// OnHotkeyModeChange installs the listener called after a hotkey mode change
// has been applied and saved. The composition root uses it to hide the
// Dropdown Panel when the shortcut changes meaning. It replaces any previous
// listener and may be nil.
func (s *Service) OnHotkeyModeChange(fn func(HotkeyMode)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onModeChange = fn
}

// OnQuakeSourceChange installs the listener called after a Quake content
// source change has been saved. The composition root uses it to hide a Panel
// that was rendering the previous source and restore the main window. It
// replaces any previous listener and may be nil.
func (s *Service) OnQuakeSourceChange(fn func(QuakeSource)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSourceChange = fn
}

// SetTraySupported records whether the native tray could be created. The
// composition root may learn this only after readiness.
func (s *Service) SetTraySupported(supported bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caps.Tray = supported
}

// Load reads the settings record. A missing or malformed record yields
// defaults. Autostart is refreshed separately with RefreshAutoStart, because
// MyGo's login-item API only answers once the application is ready.
func (s *Service) Load(ctx context.Context) error {
	settings := Defaults(s.goos)
	if s.repo != nil {
		loaded, err := s.repo.Load(ctx)
		if err != nil {
			return err
		}
		settings = Normalize(loaded, s.goos)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
	return nil
}

// RefreshAutoStart re-reads the OS login-item state, which is the source of
// truth, and caches it. Call it once the application is ready.
func (s *Service) RefreshAutoStart() {
	s.mu.Lock()
	auto := s.auto
	enabled := s.caps.AutoStart
	s.mu.Unlock()
	if auto == nil || !enabled {
		return
	}
	state, err := auto.Enabled()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.settings.AutoStart = state
	s.mu.Unlock()
}

// Settings returns a copy of the current settings.
func (s *Service) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Capabilities returns the host integration capabilities. Notification and
// autostart support are asked lazily because the MyGo backend only answers
// once the application is ready.
func (s *Service) Capabilities() Capabilities {
	s.mu.Lock()
	caps := s.caps
	notifier := s.notify
	auto := s.auto
	s.mu.Unlock()
	if notifier != nil {
		caps.Notifications = notifier.Supported()
	}
	if auto != nil {
		caps.AutoStart = auto.Supported()
	}
	return caps
}

// HotkeyProblem returns the last registration failure, or nil.
func (s *Service) HotkeyProblem() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hotkeyProblem
}

// RegisteredHotkey returns the accelerator currently claimed, or "".
func (s *Service) RegisteredHotkey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registered
}

// Apply registers or removes the global hotkey for the current mode. Call it
// once at startup after Load, and again only through the setters. toggle runs
// on the UI thread when the accelerator is pressed.
func (s *Service) Apply(toggle func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toggle = toggle
	return s.applyHotkeyLocked()
}

// Close releases the global shortcut. It is safe to call more than once.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registered != "" && s.hotkey != nil {
		s.hotkey.Unregister(s.registered)
	}
	s.registered = ""
	s.toggle = nil
}

// SetShowTrayIcon persists the tray visibility preference.
func (s *Service) SetShowTrayIcon(show bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings.ShowTrayIcon
	s.settings.ShowTrayIcon = show
	if err := s.saveLocked(); err != nil {
		s.settings.ShowTrayIcon = previous
		return err
	}
	return nil
}

// SetCloseToTray persists the close-to-tray preference.
func (s *Service) SetCloseToTray(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings.CloseToTray
	s.settings.CloseToTray = enabled
	if err := s.saveLocked(); err != nil {
		s.settings.CloseToTray = previous
		return err
	}
	return nil
}

// SetSSHDeepLinks persists the ssh:// deep-link preference.
func (s *Service) SetSSHDeepLinks(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings.SSHDeepLinks
	s.settings.SSHDeepLinks = enabled
	if err := s.saveLocked(); err != nil {
		s.settings.SSHDeepLinks = previous
		return err
	}
	return nil
}

// SetNotification persists one notification event toggle.
func (s *Service) SetNotification(event Event, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	target, ok := s.settings.notificationToggle(event)
	if !ok {
		return &Error{Kind: KindValidation, Cause: fmt.Errorf("unknown notification event %q", event)}
	}
	previous := *target
	*target = enabled
	if err := s.saveLocked(); err != nil {
		*target = previous
		return err
	}
	return nil
}

// SetHotkeyMode changes what the global shortcut does. A mode that claims the
// accelerator registers it before the value is persisted; none unregisters it
// and leaves no callback behind. On a registration failure the previous mode
// and its accelerator are restored and KindHotkey is returned. A successful
// change is announced to OnHotkeyModeChange after it is saved.
func (s *Service) SetHotkeyMode(mode HotkeyMode) error {
	if !mode.Valid() {
		return &Error{Kind: KindValidation, Cause: fmt.Errorf("unknown hotkey mode %q", mode)}
	}
	s.mu.Lock()
	if mode.RegistersHotkey() && s.hotkey == nil {
		s.mu.Unlock()
		return ErrUnsupported
	}
	previous := s.settings.HotkeyMode
	previousRegistered := s.registered
	s.settings.HotkeyMode = mode
	if err := s.applyHotkeyLocked(); err != nil {
		s.settings.HotkeyMode = previous
		s.hotkeyProblem = err
		// applyHotkeyLocked already released the old accelerator; claim it
		// again so the previous mode keeps working until the user fixes the
		// new shortcut.
		s.restoreRegisteredLocked(previousRegistered)
		s.mu.Unlock()
		return err
	}
	if err := s.saveLocked(); err != nil {
		s.settings.HotkeyMode = previous
		_ = s.applyHotkeyLocked()
		s.mu.Unlock()
		return err
	}
	listener := s.onModeChange
	s.mu.Unlock()
	if listener != nil {
		listener(mode)
	}
	return nil
}

// SetQuakeSource selects what the Quake dropdown renders. It persists the
// value and, only after a successful save, announces it. An unknown source is
// a validation error and changes nothing.
func (s *Service) SetQuakeSource(source QuakeSource) error {
	if !source.Valid() {
		return &Error{Kind: KindValidation, Cause: fmt.Errorf("unknown quake source %q", source)}
	}
	s.mu.Lock()
	previous := s.settings.QuakeSource
	s.settings.QuakeSource = source
	if err := s.saveLocked(); err != nil {
		s.settings.QuakeSource = previous
		s.mu.Unlock()
		return err
	}
	listener := s.onSourceChange
	s.mu.Unlock()
	if listener != nil {
		listener(source)
	}
	return nil
}

// SetToggleWindowHotkey changes the accelerator, registering it before the
// value is persisted. On failure the previous shortcut stays active.
func (s *Service) SetToggleWindowHotkey(accelerator string) error {
	if s.hotkey == nil {
		return ErrUnsupported
	}
	accelerator = Accelerator(accelerator)
	if accelerator == "" {
		return fmt.Errorf("%w: empty accelerator", ErrHotkey)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings.ToggleWindowHotkey
	previousRegistered := s.registered
	s.settings.ToggleWindowHotkey = accelerator
	if err := s.applyHotkeyLocked(); err != nil {
		s.settings.ToggleWindowHotkey = previous
		s.hotkeyProblem = err
		// applyHotkeyLocked already unregistered the old accelerator; a
		// best-effort re-registration keeps the old shortcut working.
		if s.settings.HotkeyMode.RegistersHotkey() {
			s.restoreRegisteredLocked(previousRegistered)
		}
		return err
	}
	if err := s.saveLocked(); err != nil {
		s.settings.ToggleWindowHotkey = previous
		_ = s.applyHotkeyLocked()
		return err
	}
	return nil
}

// ResetToggleHotkey restores the platform default.
func (s *Service) ResetToggleHotkey() error {
	return s.SetToggleWindowHotkey(DefaultToggleHotkey(s.goos))
}

// SetAutoStart changes the OS login item. On failure it re-reads the actual
// state and caches that, mirroring the original app's rollback.
func (s *Service) SetAutoStart(enabled bool) error {
	s.mu.Lock()
	auto := s.auto
	s.mu.Unlock()
	if auto == nil || !auto.Supported() {
		return ErrUnsupported
	}
	if err := s.auto.SetEnabled(enabled); err != nil {
		actual := false
		if current, readErr := s.auto.Enabled(); readErr == nil {
			actual = current
		}
		s.mu.Lock()
		s.settings.AutoStart = actual
		_ = s.saveLocked()
		s.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrAutoStart, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.settings.AutoStart
	s.settings.AutoStart = enabled
	if err := s.saveLocked(); err != nil {
		s.settings.AutoStart = previous
		return err
	}
	return nil
}

// Notify emits a notification when the event is enabled and supported. A
// disabled event is a no-op, not an error, so callers on background paths can
// ignore the result.
func (s *Service) Notify(notification Notification) error {
	s.mu.Lock()
	enabled := s.settings.NotificationEnabled(notification.Event)
	notifier := s.notify
	s.mu.Unlock()
	if !enabled || notifier == nil || !notifier.Supported() {
		return nil
	}
	return notifier.Notify(notification)
}

// applyHotkeyLocked unregisters the old accelerator and, for every mode but
// none, claims the configured one. None leaves nothing registered, so the
// accelerator returns to the system instead of an empty callback holding it.
func (s *Service) applyHotkeyLocked() error {
	if s.registered != "" && s.hotkey != nil {
		s.hotkey.Unregister(s.registered)
		s.registered = ""
	}
	s.hotkeyProblem = nil
	if !s.settings.HotkeyMode.RegistersHotkey() {
		return nil
	}
	if s.hotkey == nil {
		s.hotkeyProblem = ErrUnsupported
		return s.hotkeyProblem
	}
	accelerator := Accelerator(s.settings.ToggleWindowHotkey)
	if accelerator == "" {
		s.hotkeyProblem = fmt.Errorf("%w: empty accelerator", ErrHotkey)
		return s.hotkeyProblem
	}
	if err := s.hotkey.Register(accelerator, s.fireToggle); err != nil {
		s.hotkeyProblem = fmt.Errorf("%w: %v", ErrHotkey, err)
		return s.hotkeyProblem
	}
	s.registered = accelerator
	return nil
}

// restoreRegisteredLocked re-claims an accelerator a failed apply released,
// so a rejected change does not leave the old mode dead until restart.
func (s *Service) restoreRegisteredLocked(accelerator string) {
	if accelerator == "" || s.hotkey == nil {
		return
	}
	if err := s.hotkey.Register(accelerator, s.fireToggle); err == nil {
		s.registered = accelerator
	}
}

// fireToggle runs the registered callback without holding the service lock.
func (s *Service) fireToggle() {
	s.mu.Lock()
	toggle := s.toggle
	s.mu.Unlock()
	if toggle != nil {
		toggle()
	}
}

func (s *Service) saveLocked() error {
	if s.repo == nil {
		return nil
	}
	return s.repo.Save(context.Background(), s.settings)
}
