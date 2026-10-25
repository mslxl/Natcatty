package settings

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

// SyncService is the cloud-sync application seam the Settings surface drives.
// The app supplies *appsync.Service; tests supply a fake.
type SyncService interface {
	Snapshot() appsync.Snapshot
	Load(ctx context.Context) error
	SetPassword(password string) error
	ClearPassword()
	SetAutoSync(ctx context.Context, enabled bool) error
	SetStrategy(ctx context.Context, strategy domainsync.Strategy) error
	Connect(ctx context.Context, password string, onDevice func(appsync.DeviceCode)) error
	Disconnect(ctx context.Context) error
	Sync(ctx context.Context, trigger appsync.Trigger) (appsync.Result, error)
}

// SyncOption customizes a SyncState.
type SyncOption func(*SyncState)

// WithSyncRedrawRequest sets the function called after a worker finishes.
func WithSyncRedrawRequest(fn func()) SyncOption {
	return func(s *SyncState) { s.redraw = fn }
}

// SyncState owns the Settings sync surface. UI-thread fields are mutated only
// on the UI thread; worker goroutines append outcomes to a locked queue that
// Flush applies.
type SyncState struct {
	service SyncService
	redraw  func()

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	queue []func()

	password   string
	busy       bool
	lastErr    error
	disconnect bool
}

// NewSyncState builds the sync surface from the application service.
func NewSyncState(service SyncService, opts ...SyncOption) *SyncState {
	ctx, cancel := context.WithCancel(context.Background())
	state := &SyncState{service: service, ctx: ctx, cancel: cancel}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Shutdown cancels in-flight device polling and syncs.
func (s *SyncState) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Flush applies outcomes queued by goroutines. The view calls it every frame.
func (s *SyncState) Flush() {
	s.mu.Lock()
	queue := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, fn := range queue {
		fn()
	}
}

func (s *SyncState) post(fn func()) {
	s.mu.Lock()
	s.queue = append(s.queue, fn)
	s.mu.Unlock()
	if s.redraw != nil {
		s.redraw()
	}
}

// Snapshot returns the current application state.
func (s *SyncState) Snapshot() appsync.Snapshot { return s.service.Snapshot() }

// Password returns the master-password field.
func (s *SyncState) Password() string { return s.password }

// SetPassword updates the master-password field.
func (s *SyncState) SetPassword(value string) { s.password = value }

// Busy reports whether a worker is running.
func (s *SyncState) Busy() bool { return s.busy }

// Err returns the last worker error, or nil. The view maps it to a catalog
// string; raw Go error text is never rendered.
func (s *SyncState) Err() error { return s.lastErr }

// Error returns the last worker error text for logs and tests.
func (s *SyncState) Error() string {
	if s.lastErr == nil {
		return ""
	}
	return s.lastErr.Error()
}

// AskDisconnect opens the confirmation.
func (s *SyncState) AskDisconnect() { s.disconnect = true }

// CancelDisconnect closes the confirmation.
func (s *SyncState) CancelDisconnect() { s.disconnect = false }

// Disconnecting reports whether the confirmation is open.
func (s *SyncState) Disconnecting() bool { return s.disconnect }

// Connect starts the GitHub device flow with the entered master password.
func (s *SyncState) Connect() {
	if s.busy {
		return
	}
	password := s.password
	if password == "" {
		s.lastErr = nil
		return
	}
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := state.service.SetPassword(password)
		if err == nil {
			err = state.service.Connect(state.ctx, password, nil)
		}
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
				return
			}
			state.password = ""
		})
	}()
}

// Disconnect forgets the token and baseline after confirmation.
func (s *SyncState) Disconnect() {
	if s.busy {
		return
	}
	s.disconnect = false
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := state.service.Disconnect(state.ctx)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// SyncNow runs a manual sync. The service keeps the password after the first
// successful call, so the field may be empty on later syncs.
func (s *SyncState) SyncNow() {
	if s.busy {
		return
	}
	password := s.password
	s.busy = true
	s.lastErr = nil
	state := s
	go func() {
		err := error(nil)
		if password != "" {
			err = state.service.SetPassword(password)
		}
		if err == nil {
			_, err = state.service.Sync(state.ctx, appsync.TriggerManual)
		}
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
				return
			}
			state.password = ""
		})
	}()
}

// SetAutoSync persists the automatic-sync preference.
func (s *SyncState) SetAutoSync(enabled bool) {
	if s.busy {
		return
	}
	s.busy = true
	state := s
	go func() {
		err := state.service.SetAutoSync(state.ctx, enabled)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// SetStrategy persists the conflict strategy.
func (s *SyncState) SetStrategy(strategy domainsync.Strategy) {
	if s.busy {
		return
	}
	s.busy = true
	state := s
	go func() {
		err := state.service.SetStrategy(state.ctx, strategy)
		state.post(func() {
			state.busy = false
			if err != nil {
				state.lastErr = err
			}
		})
	}()
}

// syncSection renders the Sync & Cloud area.
func syncSection(c *ui.Context, state *SyncState, m locales.Messages, t *ui.Theme) {
	state.Flush()
	snapshot := state.Snapshot()
	section(c, m.SettingsSyncTitle(), m.SettingsSyncDescription(), t, func() {
		if !snapshot.Connected {
			syncSetup(c, state, snapshot, m, t)
			return
		}
		syncConnected(c, state, snapshot, m, t)
	})
}

func syncSetup(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsSyncNotConnected()).FontSize(12).TextColor(t.TextMuted)
	syncDeviceCode(c, snapshot, m, t)
	syncPasswordField(c, state, m)
	ui.Text(c, m.SettingsSyncMasterPasswordHint()).FontSize(11).TextColor(t.TextMuted)
	ui.Text(c, m.SettingsSyncSecurityNote()).FontSize(11).TextColor(t.TextMuted)
	syncButtons(c, state, snapshot, m)
	syncStatus(c, state, snapshot, m, t)
}

// syncDeviceCode shows the one-time code while GitHub authorization is in
// flight. Connect starts in the setup view, so the block must render before
// the account is connected.
func syncDeviceCode(c *ui.Context, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	code := snapshot.DeviceCode
	if code.UserCode == "" {
		return
	}
	ui.Column(c).Padding(10).Gap(4).Background(t.Background).Border(1, t.Border).Children(func() {
		ui.Text(c, m.SettingsSyncDeviceCode()).FontSize(11).TextColor(t.TextMuted)
		ui.Text(c, code.UserCode).FontSize(18).Bold()
		ui.Text(c, m.SettingsSyncDeviceCodeHint(code.VerificationURI)).FontSize(11).
			TextColor(t.TextMuted)
	})
}

func syncPasswordField(c *ui.Context, state *SyncState, m locales.Messages) {
	ui.Field(c, m.SettingsSyncMasterPassword(), func() {
		password := state.Password()
		if ui.TextInput(c, &password).Password().Width(280).Changed() {
			state.SetPassword(password)
		}
	})
}

func syncConnected(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsSyncConnectedAs(snapshot.Login)).Bold()
	if snapshot.HasPassword {
		ui.Text(c, m.SettingsSyncReady()).FontSize(11).TextColor(t.TextMuted)
	} else {
		ui.Text(c, m.SettingsSyncNeedsPassword()).FontSize(11).TextColor(t.Warning)
		syncPasswordField(c, state, m)
	}
	syncDeviceCode(c, snapshot, m, t)

	ui.Field(c, m.SettingsSyncAutoSync(), func() {
		auto := snapshot.AutoSync
		if ui.Checkbox(c, &auto, "").Changed() {
			state.SetAutoSync(auto)
			c.Invalidate()
		}
	})
	strategies := []string{m.SettingsSyncNewest(), m.SettingsSyncLocal(), m.SettingsSyncRemote()}
	values := []domainsync.Strategy{domainsync.StrategyNewest, domainsync.StrategyLocal, domainsync.StrategyRemote}
	strategy := strategies[syncStrategyIndex(values, snapshot.Strategy)]
	ui.Field(c, m.SettingsSyncConflictStrategy(), func() {
		if ui.Select(c, &strategy, strategies).Width(190).Changed() {
			state.SetStrategy(values[indexOf(strategies, strategy)])
			c.Invalidate()
		}
	})
	ui.Text(c, syncLastSynced(m, snapshot.LastSyncedAt)).FontSize(11).TextColor(t.TextMuted)

	syncButtons(c, state, snapshot, m)
	syncStatus(c, state, snapshot, m, t)

	if state.Disconnecting() {
		ui.Column(c).Padding(10).Gap(8).Background(t.Background).Border(1, t.Danger).Children(func() {
			ui.Text(c, m.SettingsSyncDisconnectConfirm()).FontSize(12)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, m.SettingsSyncDisconnect()).Clicked() {
					state.Disconnect()
					c.Invalidate()
				}
				if ui.Button(c, m.SettingsCancel()).Clicked() {
					state.CancelDisconnect()
					c.Invalidate()
				}
			})
		})
	}
}

func syncButtons(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages) {
	ui.Row(c).Gap(8).Children(func() {
		if !snapshot.Connected {
			if ui.Button(c, m.SettingsSyncConnect()).Disabled(state.Busy()).Clicked() {
				state.Connect()
				c.Invalidate()
			}
			return
		}
		if ui.Button(c, m.SettingsSyncSyncNow()).Disabled(state.Busy()).Clicked() {
			state.SyncNow()
			c.Invalidate()
		}
		if ui.Button(c, m.SettingsSyncDisconnect()).Disabled(state.Busy()).Clicked() {
			state.AskDisconnect()
			c.Invalidate()
		}
	})
}

func syncStatus(c *ui.Context, state *SyncState, snapshot appsync.Snapshot, m locales.Messages, t *ui.Theme) {
	if problem := syncProblemText(m, snapshot.Problem); problem != "" {
		ui.Text(c, problem).FontSize(12).TextColor(t.Danger)
		return
	}
	if err := state.Err(); err != nil {
		// Worker errors carry typed sentinels; never render Go error text.
		text := syncProblemText(m, syncProblemForError(err))
		if text == "" {
			text = m.SettingsSyncOperationFailed()
		}
		ui.Text(c, text).FontSize(12).TextColor(t.Danger)
		return
	}
	status := syncStatusText(m, snapshot.Status)
	if status != "" {
		ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)
	}
}

func syncStatusText(m locales.Messages, status appsync.Status) string {
	switch status {
	case appsync.StatusAuthorizing:
		return m.SettingsSyncStatusAuthorizing()
	case appsync.StatusSyncing:
		return m.SettingsSyncStatusSyncing()
	case appsync.StatusSynced:
		return m.SettingsSyncStatusSynced()
	case appsync.StatusFailed:
		return m.SettingsSyncStatusFailed()
	case appsync.StatusNeedsPassword:
		return m.SettingsSyncStatusNeedsPassword()
	default:
		return ""
	}
}

func syncProblemText(m locales.Messages, problem appsync.Problem) string {
	switch problem {
	case appsync.ProblemEmptyVault:
		return m.SettingsSyncProblemEmptyVault()
	case appsync.ProblemConflict:
		return m.SettingsSyncProblemConflict()
	case appsync.ProblemWrongPassword:
		return m.SettingsSyncProblemWrongPassword()
	case appsync.ProblemCorrupt:
		return m.SettingsSyncProblemCorrupt()
	case appsync.ProblemUnauthorized:
		return m.SettingsSyncProblemUnauthorized()
	case appsync.ProblemRateLimited:
		return m.SettingsSyncProblemRateLimited()
	case appsync.ProblemNetwork:
		return m.SettingsSyncProblemNetwork()
	case appsync.ProblemAuthorization:
		return m.SettingsSyncProblemAuthorization()
	case appsync.ProblemNotConfigured:
		return m.SettingsSyncProblemNotConfigured()
	case appsync.ProblemPasswordRequired:
		return m.SettingsSyncProblemPasswordRequired()
	case appsync.ProblemAlreadyInProgress:
		return m.SettingsSyncProblemAlreadyInProgress()
	case appsync.ProblemLocal:
		return m.SettingsSyncProblemLocal()
	case appsync.ProblemUnsupported:
		return m.SettingsSyncProblemUnsupported()
	default:
		return ""
	}
}

// syncProblemForError maps a worker error onto the same localized problem
// vocabulary the service stores. Unknown errors fall back to the generic
// operation-failed string.
func syncProblemForError(err error) appsync.Problem {
	switch {
	case errors.Is(err, appsync.ErrEmptyVault):
		return appsync.ProblemEmptyVault
	case errors.Is(err, appsync.ErrAlreadyInProgress):
		return appsync.ProblemAlreadyInProgress
	case errors.Is(err, appsync.ErrPasswordRequired):
		return appsync.ProblemPasswordRequired
	case errors.Is(err, appsync.ErrNotConfigured):
		return appsync.ProblemNotConfigured
	case errors.Is(err, appsync.ErrConflict):
		return appsync.ProblemConflict
	case errors.Is(err, appsync.ErrWrongPassword):
		return appsync.ProblemWrongPassword
	case errors.Is(err, appsync.ErrRemoteCorrupt):
		return appsync.ProblemCorrupt
	case errors.Is(err, appsync.ErrUnsupportedVersion):
		return appsync.ProblemUnsupported
	case errors.Is(err, appsync.ErrUnauthorized):
		return appsync.ProblemUnauthorized
	case errors.Is(err, appsync.ErrRateLimited):
		return appsync.ProblemRateLimited
	case errors.Is(err, appsync.ErrNetwork):
		return appsync.ProblemNetwork
	case errors.Is(err, appsync.ErrAuthorizationDenied), errors.Is(err, appsync.ErrDeviceCodeExpired),
		errors.Is(err, appsync.ErrAuthorizationPending), errors.Is(err, appsync.ErrSlowDown):
		return appsync.ProblemAuthorization
	default:
		return appsync.ProblemNone
	}
}

func syncLastSynced(m locales.Messages, at int64) string {
	if at <= 0 {
		return m.SettingsSyncNeverSynced()
	}
	return m.SettingsSyncLastSynced(formatSyncTime(at))
}

func formatSyncTime(milliseconds int64) string {
	return time.UnixMilli(milliseconds).Local().Format("2006-01-02 15:04")
}

func syncStrategyIndex(values []domainsync.Strategy, value domainsync.Strategy) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}
