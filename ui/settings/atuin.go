package settings

import (
	"context"
	"errors"

	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// HistoryAccount is the account surface the panel drives. The application
// AccountService implements it; tests provide a fake. No method returns a
// token or key.
type HistoryAccount interface {
	Status() historysync.Status
	Login(context.Context, historysync.LoginRequest) (historysync.Status, error)
	SetMasterKey(context.Context, string, bool) (historysync.Status, error)
	ConfirmUnverifiedKey(context.Context) (historysync.Status, error)
	ForgetMasterKey(context.Context) (historysync.Status, error)
	Logout(context.Context) (historysync.Status, error)
	Check(context.Context) (historysync.Status, error)
}

// AtuinLoginMethod is the panel's credential entry choice. Password uses the
// login flow; Token pastes a session or Bearer token.
type AtuinLoginMethod string

const (
	AtuinLoginPassword AtuinLoginMethod = "password"
	AtuinLoginToken    AtuinLoginMethod = "token"
)

// AtuinProblem is a localized account-panel error code. It is never a raw
// service, adapter, or server message, so no credential can leak through it.
type AtuinProblem string

const (
	AtuinProblemNone               AtuinProblem = ""
	AtuinProblemInvalidAddress     AtuinProblem = "invalid_address"
	AtuinProblemInvalidLogin       AtuinProblem = "invalid_login"
	AtuinProblemAuthFailed         AtuinProblem = "auth_failed"
	AtuinProblemKeyInvalid         AtuinProblem = "key_invalid"
	AtuinProblemKeyMismatch        AtuinProblem = "key_mismatch"
	AtuinProblemKeyUnverified      AtuinProblem = "key_unverified"
	AtuinProblemAccountChange      AtuinProblem = "account_change"
	AtuinProblemNotConfigured      AtuinProblem = "not_configured"
	AtuinProblemLocked             AtuinProblem = "locked"
	AtuinProblemSecretsUnavailable AtuinProblem = "secrets_unavailable"
	AtuinProblemIncompatible       AtuinProblem = "incompatible"
	AtuinProblemOffline            AtuinProblem = "offline"
	AtuinProblemRemote             AtuinProblem = "remote"
	AtuinProblemSave               AtuinProblem = "save"
)

// AtuinOption customizes an AtuinState.
type AtuinOption func(*AtuinState)

// WithAtuinRedrawRequest sets the function called after an account action or
// confirmation changes the panel.
func WithAtuinRedrawRequest(fn func()) AtuinOption {
	return func(s *AtuinState) { s.requestRedraw = fn }
}

// WithAtuinSyncStatus supplies the background sync engine's live status. The
// panel merges its last sync time, pending upload count, next retry, and
// state over the account status so the user sees progress without opening the
// engine. A nil provider leaves the account status unchanged.
func WithAtuinSyncStatus(fn func() historysync.SyncStatus) AtuinOption {
	return func(s *AtuinState) { s.syncStatus = fn }
}

// WithAtuinSyncNow supplies the manual trigger. The callback must not block:
// the engine coalesces a manual run with any scheduled one.
func WithAtuinSyncNow(fn func()) AtuinOption {
	return func(s *AtuinState) { s.syncNow = fn }
}

// WithAtuinConfigurationChanged notifies the background engine after the
// account, key, or History settings changed. The engine parks automatic
// retries after a terminal auth, key, or server-version failure; this is the
// relevant change that lets them resume.
func WithAtuinConfigurationChanged(fn func()) AtuinOption {
	return func(s *AtuinState) { s.configurationChanged = fn }
}

// AtuinState owns the Logs area's one-account panel. Credentials exist only
// in the bounded string fields while the user types; a successful login or
// key save clears them. The state never renders or logs them.
type AtuinState struct {
	account HistoryAccount
	status  historysync.Status
	problem AtuinProblem

	method    AtuinLoginMethod
	password  string
	token     string
	masterKey string

	askKeyChange bool
	askForget    bool
	askLogout    bool

	lastHistory          domainpreferences.History
	requestRedraw        func()
	syncStatus           func() historysync.SyncStatus
	syncNow              func()
	configurationChanged func()
}

// NewAtuinState returns the account panel state for account.
func NewAtuinState(account HistoryAccount, opts ...AtuinOption) *AtuinState {
	state := &AtuinState{account: account, method: AtuinLoginPassword}
	for _, opt := range opts {
		opt(state)
	}
	if account != nil {
		state.status = account.Status()
	}
	return state
}

// Status returns the account status with the live sync engine fields merged in.
// The account service owns the identity and key state; the engine owns sync
// progress, so the panel shows the engine's state, last sync time, pending
// upload count, and next retry without waiting for an account action.
func (s *AtuinState) Status() historysync.Status {
	if s == nil {
		return historysync.Status{}
	}
	status := s.status
	if s.syncStatus == nil {
		return status
	}
	synced := s.syncStatus()
	if synced.State != "" {
		status.State = synced.State
	}
	if synced.Problem != "" {
		status.Problem = synced.Problem
	}
	if synced.ServerVersion != "" {
		status.ServerVersion = synced.ServerVersion
	}
	if !synced.LastSyncAt.IsZero() {
		status.LastSyncAt = synced.LastSyncAt
	}
	status.PendingUploads = synced.PendingUploads
	status.NextRetryAt = synced.NextRetryAt
	return status
}

// Problem returns the localized action problem code.
func (s *AtuinState) Problem() AtuinProblem {
	if s == nil {
		return AtuinProblemNone
	}
	return s.problem
}

// LoginMethod returns the selected credential entry mode.
func (s *AtuinState) LoginMethod() AtuinLoginMethod {
	if s == nil || s.method == "" {
		return AtuinLoginPassword
	}
	return s.method
}

// SetLoginMethod selects password or pasted-token entry and clears the other
// credential field. Switching never sends anything.
func (s *AtuinState) SetLoginMethod(method AtuinLoginMethod) {
	if s == nil {
		return
	}
	if method != AtuinLoginToken {
		method = AtuinLoginPassword
	}
	if s.method == method {
		return
	}
	s.method = method
	s.password = ""
	s.token = ""
	s.problem = AtuinProblemNone
	s.redraw()
}

// Password is the password input's bound draft. It is never displayed after a
// successful login.
func (s *AtuinState) Password() *string { return &s.password }

// Token is the pasted-token input's bound draft.
func (s *AtuinState) Token() *string { return &s.token }

// MasterKey is the master-key input's bound draft.
func (s *AtuinState) MasterKey() *string { return &s.masterKey }

// AskingKeyChange reports whether the different-key confirmation is open.
func (s *AtuinState) AskingKeyChange() bool { return s != nil && s.askKeyChange }

// AskingForget reports whether the key-loss confirmation is open.
func (s *AtuinState) AskingForget() bool { return s != nil && s.askForget }

// AskingLogout reports whether the logout confirmation is open.
func (s *AtuinState) AskingLogout() bool { return s != nil && s.askLogout }

// Login submits the form. serverURL and username come from the persisted
// Settings fields, so the text the user sees is the address the service
// validates. authMode applies to a pasted token; the password flow always
// uses a session.
func (s *AtuinState) Login(serverURL, username string, authMode atuinmodel.AuthMode) {
	if s == nil || s.account == nil {
		return
	}
	request := historysync.LoginRequest{ServerURL: serverURL, Username: username}
	if s.LoginMethod() == AtuinLoginToken {
		request.Token = s.token
		request.AuthMode = authMode
	} else {
		request.Password = s.password
		request.AuthMode = atuinmodel.AuthModeSession
	}
	status, err := s.account.Login(context.Background(), request)
	s.finish(status, err)
	if err == nil {
		s.password = ""
		s.token = ""
		s.notifyConfigurationChanged()
	}
	s.redraw()
}

// SyncNow asks the engine for one manual run. The engine coalesces it with a
// scheduled run, so the call is safe to make from the UI thread.
func (s *AtuinState) SyncNow() {
	if s == nil || s.syncNow == nil {
		return
	}
	s.syncNow()
	s.redraw()
}

// Check refreshes the remote status without syncing records.
func (s *AtuinState) Check() {
	if s == nil || s.account == nil {
		return
	}
	status, err := s.account.Check(context.Background())
	s.finish(status, err)
	s.redraw()
}

// SaveMasterKey submits the key draft. A different key only opens the
// replacement confirmation; it is not stored until ConfirmKeyChange.
func (s *AtuinState) SaveMasterKey() {
	if s == nil || s.account == nil {
		return
	}
	s.setMasterKey(false)
}

// ConfirmKeyChange submits the key draft with the explicit replacement
// confirmation.
func (s *AtuinState) ConfirmKeyChange() {
	if s == nil || s.account == nil {
		return
	}
	s.askKeyChange = false
	s.setMasterKey(true)
}

// CancelKeyChange closes the replacement confirmation without changing the
// key.
func (s *AtuinState) CancelKeyChange() {
	if s == nil {
		return
	}
	s.askKeyChange = false
	s.redraw()
}

func (s *AtuinState) setMasterKey(confirm bool) {
	status, err := s.account.SetMasterKey(context.Background(), s.masterKey, confirm)
	if errors.Is(err, historysync.ErrKeyChangeConfirmation) {
		s.askKeyChange = true
		s.problem = AtuinProblemNone
		s.redraw()
		return
	}
	s.finish(status, err)
	if err == nil {
		s.masterKey = ""
		s.notifyConfigurationChanged()
	}
	s.redraw()
}

// ConfirmUnverified records the user's explicit acceptance of a key on an
// empty account.
func (s *AtuinState) ConfirmUnverified() {
	if s == nil || s.account == nil {
		return
	}
	status, err := s.account.ConfirmUnverifiedKey(context.Background())
	s.finish(status, err)
	if err == nil {
		s.notifyConfigurationChanged()
	}
	s.redraw()
}

// AskForget opens the key-loss confirmation. It does not touch the key.
func (s *AtuinState) AskForget() {
	if s == nil {
		return
	}
	s.askForget = true
	s.redraw()
}

// CancelForget closes the key-loss confirmation.
func (s *AtuinState) CancelForget() {
	if s == nil {
		return
	}
	s.askForget = false
	s.redraw()
}

// ConfirmForget deletes the master key and resets sync checkpoints.
func (s *AtuinState) ConfirmForget() {
	if s == nil || s.account == nil {
		return
	}
	s.askForget = false
	status, err := s.account.ForgetMasterKey(context.Background())
	s.finish(status, err)
	s.masterKey = ""
	if err == nil {
		s.notifyConfigurationChanged()
	}
	s.redraw()
}

// AskLogout opens the logout confirmation.
func (s *AtuinState) AskLogout() {
	if s == nil {
		return
	}
	s.askLogout = true
	s.redraw()
}

// CancelLogout closes the logout confirmation.
func (s *AtuinState) CancelLogout() {
	if s == nil {
		return
	}
	s.askLogout = false
	s.redraw()
}

// ConfirmLogout deletes the token and key. Local history and the account
// identity survive.
func (s *AtuinState) ConfirmLogout() {
	if s == nil || s.account == nil {
		return
	}
	s.askLogout = false
	status, err := s.account.Logout(context.Background())
	s.finish(status, err)
	s.password = ""
	s.token = ""
	s.masterKey = ""
	if err == nil {
		s.notifyConfigurationChanged()
	}
	s.redraw()
}

// SyncHistory refreshes local status when the persisted History settings
// changed, so enabling or disabling sync updates the panel immediately. It
// performs no network request.
func (s *AtuinState) SyncHistory(history domainpreferences.History) {
	if s == nil || s.account == nil || history == s.lastHistory {
		return
	}
	s.lastHistory = history
	s.status = s.account.Status()
	s.notifyConfigurationChanged()
}

func (s *AtuinState) notifyConfigurationChanged() {
	if s.configurationChanged != nil {
		s.configurationChanged()
	}
}

func (s *AtuinState) finish(status historysync.Status, err error) {
	s.status = status
	s.problem = problemFor(err)
}

func (s *AtuinState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}

// problemFor maps only local sentinel errors. Unknown errors become a generic
// action failure, never the error text.
func problemFor(err error) AtuinProblem {
	switch {
	case err == nil:
		return AtuinProblemNone
	case errors.Is(err, historysync.ErrInvalidSyncAddress):
		return AtuinProblemInvalidAddress
	case errors.Is(err, historysync.ErrInvalidLogin):
		return AtuinProblemInvalidLogin
	case errors.Is(err, historysync.ErrAuthFailed),
		errors.Is(err, historysync.ErrAuthExpired):
		return AtuinProblemAuthFailed
	case errors.Is(err, historysync.ErrKeyMismatch):
		return AtuinProblemKeyMismatch
	case errors.Is(err, historysync.ErrKeyValidationFailed):
		return AtuinProblemKeyUnverified
	case errors.Is(err, historysync.ErrAccountChangeRequiresLogout):
		return AtuinProblemAccountChange
	case errors.Is(err, historysync.ErrNotConfigured):
		return AtuinProblemNotConfigured
	case errors.Is(err, historysync.ErrSecretsLocked),
		errors.Is(err, historysync.ErrAppLocked):
		return AtuinProblemLocked
	case errors.Is(err, historysync.ErrSecretsUnavailable),
		errors.Is(err, historysync.ErrSecretUnreadable):
		return AtuinProblemSecretsUnavailable
	case errors.Is(err, historysync.ErrIncompatibleServer):
		return AtuinProblemIncompatible
	case errors.Is(err, historysync.ErrOffline):
		return AtuinProblemOffline
	case errors.Is(err, historysync.ErrRemoteUnavailable),
		errors.Is(err, historysync.ErrRemote):
		return AtuinProblemRemote
	case errors.Is(err, domainhistory.ErrMasterKeyEmpty),
		errors.Is(err, domainhistory.ErrMasterKeyEncoding),
		errors.Is(err, domainhistory.ErrMasterKeyMnemonic),
		errors.Is(err, domainhistory.ErrMasterKeyWord),
		errors.Is(err, domainhistory.ErrMasterKeyChecksum),
		errors.Is(err, domainhistory.ErrMasterKeyLength):
		return AtuinProblemKeyInvalid
	default:
		return AtuinProblemSave
	}
}
