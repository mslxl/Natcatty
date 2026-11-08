package historysync

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// Account errors. They never include a token, key, server response body, or
// record payload.
var (
	// ErrNotConfigured reports a key operation attempted before the account
	// has a server address and token.
	ErrNotConfigured = errors.New("historysync: account is not configured")
	// ErrInvalidLogin reports a login request missing its credentials or
	// mixing token and password authentication.
	ErrInvalidLogin = errors.New("historysync: invalid login request")
	// ErrInvalidSyncAddress reports a non-HTTPS address, except for a loopback
	// development address.
	ErrInvalidSyncAddress = errors.New("historysync: invalid sync address")
	// ErrKeyChangeConfirmation reports a different key without the explicit
	// confirmation that remote history becomes unreadable.
	ErrKeyChangeConfirmation = errors.New("historysync: key change needs confirmation")
	// ErrAccountChangeRequiresLogout reports a login for a different account
	// while one is still connected. One account per install; log out first.
	ErrAccountChangeRequiresLogout = errors.New("historysync: account change requires logout")
	// ErrKeyValidationFailed reports an adapter that could not validate a key
	// against a non-empty account. Failing closed prevents the first upload
	// with an unverified key.
	ErrKeyValidationFailed = errors.New("historysync: master key validation failed")
)

// LoginRequest is one account panel submission. Token is a pasted session or
// Bearer token; Username and Password are the login flow. Exactly one flow is
// used. The token is never returned in a Status.
type LoginRequest struct {
	ServerURL string
	Username  string
	Password  string
	Token     string
	AuthMode  atuinmodel.AuthMode
}

// Status is the account panel's view of the one Atuin account. It never
// contains the session token, master key, request bodies, or decrypted
// payloads. Detail is reserved for a sanitized message; remote errors are
// reduced to a Problem code.
type Status struct {
	State          atuinmodel.SyncState
	Problem        atuinmodel.Problem
	Detail         string
	Configured     bool
	ServerURL      string
	Username       string
	AuthMode       atuinmodel.AuthMode
	AccountID      string
	ServerVersion  string
	KeyFingerprint string
	KeyState       atuinmodel.KeyState
	// KeyConfirmed is true only after the user explicitly accepted an
	// unverified key for an empty account. A later key change clears it. The
	// sync engine must require verified or confirmed before its first upload.
	KeyConfirmed   bool
	LastSyncAt     time.Time
	PendingUploads int
	NextRetryAt    time.Time
}

// AccountSettings is the narrow device-local settings port. The preferences
// service implements it; tests fake it.
type AccountSettings interface {
	HistorySettings() domainpreferences.History
	UpdateHistorySettings(func(*domainpreferences.History)) error
}

// MetadataStore is the device-local metadata port the SQLite store
// implements. The account service keeps the writer UUID untouched and clears
// only the account identity and sync checkpoints on logout or key change.
type MetadataStore interface {
	HistoryMetadata(context.Context) (domainhistory.Metadata, error)
	SaveHistoryMetadata(context.Context, domainhistory.Metadata) error
}

// AccountService owns the single Atuin account, the master key lifecycle, and
// account validation. It never syncs records: the Remote port's Download and
// Upload are only declared here for the engine in ticket 14.
//
// Account operations are serialized; Status is safe to call from the UI
// thread while an operation runs because it reads only local state and a
// cached snapshot.
type AccountService struct {
	settings  AccountSettings
	secrets   Secrets
	metadata  MetadataStore
	factory   Factory
	appLocked func() bool

	opMu     sync.Mutex
	statusMu sync.Mutex
	cached   Status
}

// AccountOption customizes an AccountService.
type AccountOption func(*AccountService)

// WithAccountAppLocked supplies a predicate that reports whether App Lock is
// engaged. While it is, every account operation that could read or write a
// credential fails closed before touching the secret store. Status still reads
// only device-local metadata and never decrypts a credential.
func WithAccountAppLocked(locked func() bool) AccountOption {
	return func(s *AccountService) {
		if locked != nil {
			s.appLocked = locked
		}
	}
}

// NewAccountService returns the account service. factory may be nil until the
// real adapter exists; operations then report ErrRemoteUnavailable.
func NewAccountService(settings AccountSettings, secrets Secrets, metadata MetadataStore,
	factory Factory, opts ...AccountOption) *AccountService {
	service := &AccountService{settings: settings, secrets: secrets, metadata: metadata, factory: factory}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func (s *AccountService) appLockEngaged() bool {
	return s != nil && s.appLocked != nil && s.appLocked()
}

// appLockStatus fails an account operation while App Lock is engaged. It reads
// only device-local metadata; localStatus's App Lock branch performs no secret
// Has or Get.
func (s *AccountService) appLockStatus(ctx context.Context) (Status, error) {
	if !s.appLockEngaged() {
		return Status{}, nil
	}
	status := s.localStatus(ctx)
	s.setCached(status)
	return status, ErrAppLocked
}

// NormalizeSyncAddress validates and normalizes an Atuin base URL. HTTPS is
// required; plain HTTP is accepted only for a loopback development address.
// A self-signed CA must be trusted by the operating system, so it needs no
// special URL form. Credentials in the URL, a query, or a fragment are
// rejected so a token can never hide in an address.
func NormalizeSyncAddress(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrInvalidSyncAddress
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", ErrInvalidSyncAddress
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidSyncAddress
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "https"
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", ErrInvalidSyncAddress
		}
		parsed.Scheme = "http"
	default:
		return "", ErrInvalidSyncAddress
	}
	// Host names are case-insensitive. Normalizing here keeps an address
	// comparison from treating API.ATUIN.SH as a different server from
	// api.atuin.sh, while a genuinely different server still differs.
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

// sameAccountAddress reports whether the effective sync address belongs to
// the last connected account. An absent or invalid identity fails closed: a
// stored token must never be sent to an address this service has not recorded
// as that account's server.
func sameAccountAddress(existing domainhistory.AccountIdentity, effective string) bool {
	if existing.ServerURL == "" {
		return false
	}
	recorded, err := NormalizeSyncAddress(existing.ServerURL)
	if err != nil {
		return false
	}
	return recorded == effective
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// Login authenticates the single account. It stores the session token in the
// field-secret store, saves only non-secret settings, records the account
// identity, and returns a status that never displays the token again.
func (s *AccountService) Login(ctx context.Context, request LoginRequest) (Status, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if status, err := s.appLockStatus(ctx); err != nil {
		return status, err
	}
	if err := s.secretsReady(); err != nil {
		status := s.localStatus(ctx)
		s.setCached(status)
		return status, err
	}
	settings := s.settings.HistorySettings()
	address, err := NormalizeSyncAddress(firstNonEmpty(request.ServerURL, settings.SyncAddress))
	if err != nil {
		status := s.localStatus(ctx)
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrInvalidSyncAddress
	}
	authMode := atuinmodel.NormalizeAuthMode(request.AuthMode)
	passwordFlow := request.Password != "" || (request.Token == "" && request.Username != "")
	switch {
	case request.Token != "" && request.Password != "":
		return s.localStatus(ctx), ErrInvalidLogin
	case request.Token == "" && request.Password == "":
		return s.localStatus(ctx), ErrInvalidLogin
	case passwordFlow && strings.TrimSpace(request.Username) == "":
		return s.localStatus(ctx), ErrInvalidLogin
	case passwordFlow && authMode != atuinmodel.AuthModeSession:
		// The login endpoint mints a session token; only a pasted token may
		// use Bearer auth.
		return s.localStatus(ctx), ErrInvalidLogin
	}

	metadata, err := s.metadata.HistoryMetadata(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	connected, err := s.secrets.Has(ctx, SessionTokenRef())
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	if connected && !sameAccountAddress(metadata.Account, address) {
		// Do not even open a remote: its config would carry the stored master
		// key to an address that does not belong to the connected account.
		status := s.localStatus(ctx)
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrAccountChangeRequiresLogout
	}
	storedKey, keyPresent, err := s.loadMasterKey(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	if keyPresent {
		defer domainhistory.WipeMasterKey(storedKey)
	}

	config := Config{
		BaseURL: address, AuthMode: authMode,
		Token: request.Token, Username: request.Username, Password: request.Password,
		MasterKey: storedKey,
	}
	defer config.Wipe()
	remote, err := s.openRemote(ctx, config)
	if err != nil {
		status := s.statusFromLocalError(err)
		s.setCached(status)
		return status, err
	}
	defer func() { _ = remote.Close() }()

	session, err := remote.Authenticate(ctx)
	if err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, publicRemoteError(err)
	}
	token := request.Token
	if passwordFlow {
		token = session.Token
		if token == "" {
			status := s.statusForRemoteError(ErrAuthFailed, s.localStatus(ctx))
			s.setCached(status)
			return status, ErrAuthFailed
		}
	}
	account := session.Account
	if account.ServerURL == "" {
		account.ServerURL = address
	}
	if account.Username == "" {
		account.Username = strings.TrimSpace(request.Username)
	}

	capabilities, err := remote.Capabilities(ctx)
	if err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, publicRemoteError(err)
	}
	if err := capabilities.Check(); err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, ErrIncompatibleServer
	}
	// One account is connected at a time. The stored identity also survives
	// logout, so a reconnect can later tell the same account from a change;
	// the token decides whether an account is still connected.
	if connected && accountChanged(metadata.Account, address, account) {
		status := s.localStatus(ctx)
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrAccountChangeRequiresLogout
	}

	username := settings.Username
	if account.Username != "" {
		username = account.Username
	}
	if err := s.settings.UpdateHistorySettings(func(history *domainpreferences.History) {
		history.SyncAddress = address
		history.AuthMode = authMode
		if username != "" {
			history.Username = username
		}
	}); err != nil {
		return s.statusFromLocalError(err), err
	}
	metadata.Account = domainhistory.AccountIdentity{ServerURL: address, UserID: account.UserID}
	if err := s.metadata.SaveHistoryMetadata(ctx, metadata); err != nil {
		return s.statusFromLocalError(err), err
	}
	if err := s.secrets.Set(ctx, SessionTokenRef(), token); err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}

	status := s.inspect(ctx, remote, capabilities, metadata, address, username, authMode, keyPresent)
	s.setCached(status)
	return status, nil
}

// SetMasterKey parses and validates a BIP39 or base64 master key, stores it in
// the field-secret store, and records only its fingerprint. A different key
// requires confirmChange because existing remote history becomes unreadable;
// a confirmed change resets sync checkpoints while keeping local history and
// the writer UUID.
func (s *AccountService) SetMasterKey(ctx context.Context, input string, confirmChange bool) (Status, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if status, err := s.appLockStatus(ctx); err != nil {
		return status, err
	}
	if err := s.secretsReady(); err != nil {
		status := s.localStatus(ctx)
		s.setCached(status)
		return status, err
	}
	settings := s.settings.HistorySettings()
	address, err := NormalizeSyncAddress(settings.SyncAddress)
	if err != nil {
		return s.localStatus(ctx), ErrInvalidSyncAddress
	}
	tokenPresent, err := s.secrets.Has(ctx, SessionTokenRef())
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	if !tokenPresent {
		return s.localStatus(ctx), ErrNotConfigured
	}
	metadata, err := s.metadata.HistoryMetadata(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	if !sameAccountAddress(metadata.Account, address) {
		// The token is present, so this operation would send it (and the key
		// being configured) to an address the connected account never owned.
		// Fail before decrypting the token or opening a remote; the user must
		// log out first.
		status := s.localStatus(ctx)
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrAccountChangeRequiresLogout
	}
	key, err := domainhistory.ParseMasterKey(input)
	if err != nil {
		return s.localStatus(ctx), err
	}
	defer domainhistory.WipeMasterKey(key)
	fingerprint := domainhistory.KeyFingerprint(key)
	token, err := s.secrets.Get(ctx, SessionTokenRef())
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	changed := metadata.KeyFingerprint != "" && metadata.KeyFingerprint != fingerprint
	sameKey := metadata.KeyFingerprint != "" && metadata.KeyFingerprint == fingerprint
	if changed && !confirmChange {
		return s.localStatus(ctx), ErrKeyChangeConfirmation
	}

	config := Config{
		BaseURL: address, AuthMode: atuinmodel.NormalizeAuthMode(settings.AuthMode),
		Token: token, MasterKey: key,
	}
	defer config.Wipe()
	remote, err := s.openRemote(ctx, config)
	if err != nil {
		status := s.statusFromLocalError(err)
		s.setCached(status)
		return status, err
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.Authenticate(ctx); err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, publicRemoteError(err)
	}
	capabilities, err := remote.Capabilities(ctx)
	if err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, publicRemoteError(err)
	}
	if err := capabilities.Check(); err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, ErrIncompatibleServer
	}
	statuses, err := remote.RecordStatus(ctx)
	if err != nil {
		status := s.statusForRemoteError(err, s.localStatus(ctx))
		s.setCached(status)
		return status, publicRemoteError(err)
	}

	hasHistory := HasHistory(statuses)
	validation := KeyValidationUnverified
	if hasHistory {
		validation, err = remote.ValidateMasterKey(ctx)
		if err != nil {
			if errors.Is(err, ErrKeyMismatch) {
				validation = KeyValidationMismatch
			} else {
				status := s.statusForRemoteError(err, s.localStatus(ctx))
				s.setCached(status)
				return status, publicRemoteError(err)
			}
		}
	}
	switch validation {
	case KeyValidationMismatch:
		if !changed {
			status := s.statusForValidation(s.localStatus(ctx), validation, hasHistory,
				metadata.KeyFingerprint, settings)
			s.setCached(status)
			return status, ErrKeyMismatch
		}
	case KeyValidationVerified, KeyValidationUnverified:
		if validation == KeyValidationUnverified && hasHistory {
			status := s.localStatus(ctx)
			status.State = atuinmodel.SyncError
			status.Problem = atuinmodel.ProblemError
			s.setCached(status)
			return status, ErrKeyValidationFailed
		}
	default:
		status := s.localStatus(ctx)
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrKeyValidationFailed
	}

	encoded := base64.StdEncoding.EncodeToString(key)
	previous, previousErr := s.secrets.Get(ctx, MasterKeyRef())
	hadPrevious := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, ErrSecretNotFound) {
		return s.statusFromLocalError(previousErr), publicSecretError(previousErr)
	}
	if err := s.secrets.Set(ctx, MasterKeyRef(), encoded); err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	metadata.KeyFingerprint = fingerprint
	// A confirmation only applies to the exact unverified key the user
	// accepted for an account with no history. A new key, or a key validated
	// against a real record, needs no confirmation and clears the old one.
	metadata.UnverifiedKeyConfirmed = validation == KeyValidationUnverified &&
		sameKey && metadata.UnverifiedKeyConfirmed
	if changed {
		// Sync checkpoints belong to the old key. Local records, tombstones,
		// the writer UUID, and the account identity survive.
		metadata.Series = nil
	}
	if err := s.metadata.SaveHistoryMetadata(ctx, metadata); err != nil {
		if hadPrevious {
			_ = s.secrets.Set(ctx, MasterKeyRef(), previous)
		} else {
			_ = s.secrets.Delete(ctx, MasterKeyRef())
		}
		return s.statusFromLocalError(err), err
	}

	status := s.statusForValidation(s.localStatus(ctx), validation, hasHistory, fingerprint, settings)
	status.KeyFingerprint = fingerprint
	status.KeyConfirmed = metadata.UnverifiedKeyConfirmed
	if !settings.SyncEnabled {
		// The key is stored, but sync remains off. Report the same disabled
		// status Status() and Check() report instead of an idle key warning.
		status.State = atuinmodel.SyncDisabled
		status.Problem = atuinmodel.ProblemDisabled
	}
	s.setCached(status)
	return status, nil
}

// ConfirmUnverifiedKey records the user's explicit acceptance of an
// unverified master key on an empty account. The confirmation is local,
// survives a restart, and is cleared by a key change, forgetting the key, or
// logout. It never makes a key "verified": a later account with history still
// has to decrypt a real record, and a mismatch still fails closed.
func (s *AccountService) ConfirmUnverifiedKey(ctx context.Context) (Status, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if status, err := s.appLockStatus(ctx); err != nil {
		return status, err
	}
	if err := s.secretsReady(); err != nil {
		status := s.localStatus(ctx)
		s.setCached(status)
		return status, err
	}
	metadata, err := s.metadata.HistoryMetadata(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	keyPresent, err := s.secrets.Has(ctx, MasterKeyRef())
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	if !keyPresent || metadata.KeyFingerprint == "" {
		status := s.localStatus(ctx)
		if status.Problem != atuinmodel.ProblemKeyMissing {
			status.State = atuinmodel.SyncNotConfigured
			status.Problem = atuinmodel.ProblemKeyMissing
		}
		s.setCached(status)
		return status, ErrNotConfigured
	}
	metadata.UnverifiedKeyConfirmed = true
	if err := s.metadata.SaveHistoryMetadata(ctx, metadata); err != nil {
		return s.statusFromLocalError(err), err
	}
	status := s.localStatus(ctx)
	if status.State == atuinmodel.SyncIdle {
		status.Problem = atuinmodel.ProblemKeyUnverified
	}
	status.KeyState = atuinmodel.KeyStateUnverified
	status.KeyConfirmed = true
	s.setCached(status)
	return status, nil
}

// ForgetMasterKey deletes only the master key and resets sync checkpoints. It
// is the explicit "I lost my key" action: the token and account stay, the
// fingerprint stays for the key-change warning, and the server history cannot
// be decrypted. There is no server-side recovery.
func (s *AccountService) ForgetMasterKey(ctx context.Context) (Status, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if status, err := s.appLockStatus(ctx); err != nil {
		return status, err
	}
	if err := s.secrets.Delete(ctx, MasterKeyRef()); err != nil &&
		!errors.Is(err, ErrSecretNotFound) {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	metadata, err := s.metadata.HistoryMetadata(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	metadata.Series = nil
	metadata.UnverifiedKeyConfirmed = false
	if err := s.metadata.SaveHistoryMetadata(ctx, metadata); err != nil {
		return s.statusFromLocalError(err), err
	}
	status := s.localStatus(ctx)
	s.setCached(status)
	return status, nil
}

// Logout deletes the token and master key and clears sync checkpoints. It
// deliberately keeps local history, tombstones, the writer UUID, the key
// fingerprint, and the account identity. The identity lets the next connect
// tell a same-account reconnect from an account change; the missing token is
// what makes the account no longer connected. A same-account reconnect
// continues the same writer series.
func (s *AccountService) Logout(ctx context.Context) (Status, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if status, err := s.appLockStatus(ctx); err != nil {
		return status, err
	}
	for _, ref := range []SecretRef{SessionTokenRef(), MasterKeyRef()} {
		if err := s.secrets.Delete(ctx, ref); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return s.statusFromLocalError(err), publicSecretError(err)
		}
	}
	metadata, err := s.metadata.HistoryMetadata(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	metadata.Series = nil
	metadata.UnverifiedKeyConfirmed = false
	if err := s.metadata.SaveHistoryMetadata(ctx, metadata); err != nil {
		return s.statusFromLocalError(err), err
	}
	status := s.localStatus(ctx)
	s.setCached(status)
	return status, nil
}

// Check refreshes the account's sync status without syncing a record. It
// validates the token, requires a compatible v18 server, reads record status,
// and validates the stored key when the account has history. Remote failures
// are reported in the returned Status with a nil error; local credential
// errors are returned.
func (s *AccountService) Check(ctx context.Context) (Status, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if status, err := s.appLockStatus(ctx); err != nil {
		return status, err
	}
	status := s.localStatus(ctx)
	switch status.State {
	case atuinmodel.SyncDisabled, atuinmodel.SyncNotConfigured, atuinmodel.SyncLocked,
		atuinmodel.SyncError:
		s.setCached(status)
		return status, nil
	}
	if err := s.secretsReady(); err != nil {
		return status, err
	}
	settings := s.settings.HistorySettings()
	address, err := NormalizeSyncAddress(settings.SyncAddress)
	if err != nil {
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrInvalidSyncAddress
	}
	tokenPresent, err := s.secrets.Has(ctx, SessionTokenRef())
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	if !tokenPresent {
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
		s.setCached(status)
		return status, nil
	}
	metadata, err := s.metadata.HistoryMetadata(ctx)
	if err != nil {
		return s.statusFromLocalError(err), err
	}
	if !sameAccountAddress(metadata.Account, address) {
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
		s.setCached(status)
		return status, ErrAccountChangeRequiresLogout
	}
	token, err := s.secrets.Get(ctx, SessionTokenRef())
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	key, keyPresent, err := s.loadMasterKey(ctx)
	if err != nil {
		return s.statusFromLocalError(err), publicSecretError(err)
	}
	if keyPresent {
		defer domainhistory.WipeMasterKey(key)
	} else {
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemKeyMissing
		s.setCached(status)
		return status, nil
	}

	config := Config{
		BaseURL: address, AuthMode: atuinmodel.NormalizeAuthMode(settings.AuthMode),
		Token: token, MasterKey: key,
	}
	defer config.Wipe()
	remote, err := s.openRemote(ctx, config)
	if err != nil {
		status = s.statusFromLocalError(err)
		s.setCached(status)
		return status, err
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.Authenticate(ctx); err != nil {
		status = s.statusForRemoteError(err, status)
		s.setCached(status)
		return status, nil
	}
	capabilities, err := remote.Capabilities(ctx)
	if err != nil {
		status = s.statusForRemoteError(err, status)
		s.setCached(status)
		return status, nil
	}
	if err := capabilities.Check(); err != nil {
		status = s.statusForRemoteError(err, status)
		s.setCached(status)
		return status, nil
	}
	status = s.inspect(ctx, remote, capabilities, metadata, address, settings.Username,
		settings.AuthMode, true)
	s.setCached(status)
	return status, nil
}

// Status returns the local account state and the last remote result. It never
// performs a network request and never decrypts the master key.
func (s *AccountService) Status() Status {
	status := s.localStatus(context.Background())
	s.statusMu.Lock()
	cached := s.cached
	s.statusMu.Unlock()
	if cached.ServerURL == "" || cached.ServerURL != status.ServerURL {
		return status
	}
	status.ServerVersion = cached.ServerVersion
	status.LastSyncAt = cached.LastSyncAt
	status.PendingUploads = cached.PendingUploads
	status.NextRetryAt = cached.NextRetryAt
	if cached.KeyFingerprint == status.KeyFingerprint {
		switch cached.KeyState {
		case atuinmodel.KeyStateVerified, atuinmodel.KeyStateMismatch:
			if status.KeyState == atuinmodel.KeyStateUnverified {
				status.KeyState = cached.KeyState
			}
		}
		if status.State == atuinmodel.SyncIdle {
			switch cached.State {
			case atuinmodel.SyncOffline, atuinmodel.SyncAuthExpired,
				atuinmodel.SyncKeyMismatch, atuinmodel.SyncIncompatibleServer,
				atuinmodel.SyncError:
				status.State = cached.State
				status.Problem = cached.Problem
				status.Detail = cached.Detail
			case atuinmodel.SyncIdle:
				// An empty account has no record to validate. Keep the
				// warning the last key operation produced instead of letting
				// localStatus's generic "no problem" overwrite it.
				if cached.Problem == atuinmodel.ProblemKeyUnverified {
					status.Problem = cached.Problem
					status.KeyState = atuinmodel.KeyStateUnverified
				}
			}
		}
	}
	return status
}

// inspect reads record status and validates the key when the account has
// history. It never downloads or uploads a record.
func (s *AccountService) inspect(ctx context.Context, remote Remote, capabilities Capabilities,
	metadata domainhistory.Metadata, address, username string, authMode atuinmodel.AuthMode,
	keyPresent bool) Status {
	status := Status{
		State:          atuinmodel.SyncIdle,
		Configured:     true,
		ServerURL:      address,
		Username:       username,
		AuthMode:       atuinmodel.NormalizeAuthMode(authMode),
		AccountID:      metadata.Account.UserID,
		ServerVersion:  capabilities.ServerVersion,
		KeyFingerprint: metadata.KeyFingerprint,
	}
	if keyPresent {
		status.KeyState = atuinmodel.KeyStateUnverified
		status.KeyConfirmed = metadata.UnverifiedKeyConfirmed && metadata.KeyFingerprint != ""
	} else if metadata.KeyFingerprint != "" {
		status.KeyState = atuinmodel.KeyStateMissing
	} else {
		status.KeyState = atuinmodel.KeyStateNone
	}

	statuses, err := remote.RecordStatus(ctx)
	if err != nil {
		return s.statusForRemoteError(err, status)
	}
	hasHistory := HasHistory(statuses)
	switch {
	case !keyPresent && metadata.KeyFingerprint != "":
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemKeyMissing
		return status
	case !keyPresent:
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
		return status
	case !hasHistory:
		status.State = atuinmodel.SyncIdle
		status.Problem = atuinmodel.ProblemKeyUnverified
		status.KeyState = atuinmodel.KeyStateUnverified
		return status
	}

	validation, err := remote.ValidateMasterKey(ctx)
	if err != nil {
		if errors.Is(err, ErrKeyMismatch) {
			validation = KeyValidationMismatch
		} else {
			return s.statusForRemoteError(err, status)
		}
	}
	return s.statusForValidation(status, validation, hasHistory, metadata.KeyFingerprint,
		s.settings.HistorySettings())
}

func (s *AccountService) statusForValidation(status Status, validation KeyValidation,
	hasHistory bool, fingerprint string, settings domainpreferences.History) Status {
	status.KeyFingerprint = fingerprint
	status.ServerURL = firstNonEmpty(status.ServerURL, settings.SyncAddress)
	status.Username = firstNonEmpty(status.Username, settings.Username)
	status.AuthMode = atuinmodel.NormalizeAuthMode(settings.AuthMode)
	status.Configured = true
	switch validation {
	case KeyValidationVerified:
		status.State = atuinmodel.SyncIdle
		status.Problem = atuinmodel.ProblemNone
		status.KeyState = atuinmodel.KeyStateVerified
	case KeyValidationUnverified:
		status.State = atuinmodel.SyncIdle
		status.Problem = atuinmodel.ProblemKeyUnverified
		status.KeyState = atuinmodel.KeyStateUnverified
		if hasHistory {
			// ValidateMasterKey must decrypt a real record when the account has
			// history. An unverified result is an adapter contract failure; fail
			// closed rather than allow the sync engine to upload with it.
			status.State = atuinmodel.SyncError
			status.Problem = atuinmodel.ProblemError
		}
	case KeyValidationMismatch:
		status.State = atuinmodel.SyncKeyMismatch
		status.Problem = atuinmodel.ProblemKeyMismatch
		status.KeyState = atuinmodel.KeyStateMismatch
	default:
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	}
	if !hasHistory && validation == KeyValidationVerified {
		// An empty account has nothing to verify; the adapter should report
		// unverified, but keep the status honest if it does not.
		status.State = atuinmodel.SyncIdle
		status.Problem = atuinmodel.ProblemKeyUnverified
		status.KeyState = atuinmodel.KeyStateUnverified
	}
	return status
}

// localStatus computes the status from device-local state only.
func (s *AccountService) localStatus(ctx context.Context) Status {
	settings := s.settings.HistorySettings()
	status := Status{
		State:     atuinmodel.SyncNotConfigured,
		Problem:   atuinmodel.ProblemNotConfigured,
		ServerURL: settings.SyncAddress,
		Username:  settings.Username,
		AuthMode:  atuinmodel.NormalizeAuthMode(settings.AuthMode),
	}
	metadata, metadataErr := s.metadata.HistoryMetadata(ctx)
	if metadataErr == nil {
		status.AccountID = metadata.Account.UserID
		status.KeyFingerprint = metadata.KeyFingerprint
	}
	if s.appLockEngaged() {
		// App Lock hides the app. The status may report the lock without
		// touching the secret store at all: Has would reveal credential
		// presence, and Get would decrypt.
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
		return status
	}

	mode := s.secrets.Mode()
	switch mode {
	case SecretModePassphraseLocked:
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
		return status
	case SecretModeUnavailable:
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
		return status
	}

	tokenPresent, err := s.secrets.Has(ctx, SessionTokenRef())
	if err != nil {
		return s.statusForSecretError(err, status)
	}
	keyPresent, err := s.secrets.Has(ctx, MasterKeyRef())
	if err != nil {
		return s.statusForSecretError(err, status)
	}
	switch {
	case keyPresent && metadata.KeyFingerprint == "":
		status.KeyState = atuinmodel.KeyStateUnverified
	case keyPresent:
		status.KeyState = atuinmodel.KeyStateUnverified
		status.KeyConfirmed = metadata.UnverifiedKeyConfirmed
	case metadata.KeyFingerprint != "":
		status.KeyState = atuinmodel.KeyStateMissing
	default:
		status.KeyState = atuinmodel.KeyStateNone
	}

	status.Configured = tokenPresent && strings.TrimSpace(settings.SyncAddress) != ""
	switch {
	case !settings.SyncEnabled:
		status.State = atuinmodel.SyncDisabled
		status.Problem = atuinmodel.ProblemDisabled
	case !status.Configured:
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	case !keyPresent:
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemKeyMissing
	default:
		status.State = atuinmodel.SyncIdle
		status.Problem = atuinmodel.ProblemNone
	}
	if metadataErr != nil {
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	}
	return status
}

func (s *AccountService) statusFromLocalError(err error) Status {
	status := Status{State: atuinmodel.SyncError, Problem: atuinmodel.ProblemError}
	switch {
	case errors.Is(err, ErrSecretsLocked):
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
	case errors.Is(err, ErrSecretsUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	case errors.Is(err, ErrRemoteUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	}
	return status
}

func (s *AccountService) statusForSecretError(err error, status Status) Status {
	switch {
	case errors.Is(err, ErrSecretsLocked):
		status.State = atuinmodel.SyncLocked
		status.Problem = atuinmodel.ProblemLocked
	case errors.Is(err, ErrSecretsUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	default:
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	}
	return status
}

// statusForRemoteError reduces a remote failure to a stable state, problem,
// and empty detail. The adapter's message is deliberately not copied: it may
// contain a request body or credential.
func (s *AccountService) statusForRemoteError(err error, status Status) Status {
	switch {
	case errors.Is(err, ErrAuthExpired), errors.Is(err, ErrAuthFailed):
		status.State = atuinmodel.SyncAuthExpired
		status.Problem = atuinmodel.ProblemAuthExpired
	case errors.Is(err, ErrIncompatibleServer):
		status.State = atuinmodel.SyncIncompatibleServer
		status.Problem = atuinmodel.ProblemIncompatibleServer
	case errors.Is(err, ErrKeyMismatch):
		status.State = atuinmodel.SyncKeyMismatch
		status.Problem = atuinmodel.ProblemKeyMismatch
		status.KeyState = atuinmodel.KeyStateMismatch
	case errors.Is(err, ErrOffline):
		status.State = atuinmodel.SyncOffline
		status.Problem = atuinmodel.ProblemOffline
	case errors.Is(err, ErrRemoteUnavailable):
		status.State = atuinmodel.SyncNotConfigured
		status.Problem = atuinmodel.ProblemNotConfigured
	default:
		status.State = atuinmodel.SyncError
		status.Problem = atuinmodel.ProblemError
	}
	status.Detail = ""
	return status
}

// publicRemoteError returns only a known sentinel so a server message cannot
// leak through an error string.
func publicRemoteError(err error) error {
	switch {
	case errors.Is(err, ErrAuthExpired):
		return ErrAuthExpired
	case errors.Is(err, ErrAuthFailed):
		return ErrAuthFailed
	case errors.Is(err, ErrIncompatibleServer):
		return ErrIncompatibleServer
	case errors.Is(err, ErrKeyMismatch):
		return ErrKeyMismatch
	case errors.Is(err, ErrOffline):
		return ErrOffline
	case errors.Is(err, ErrRemoteUnavailable):
		return ErrRemoteUnavailable
	default:
		return ErrRemote
	}
}

func publicSecretError(err error) error {
	switch {
	case errors.Is(err, ErrSecretNotFound):
		return ErrNotConfigured
	case errors.Is(err, ErrSecretsLocked):
		return ErrSecretsLocked
	case errors.Is(err, ErrSecretsUnavailable):
		return ErrSecretsUnavailable
	case errors.Is(err, ErrSecretUnreadable):
		return ErrSecretUnreadable
	default:
		return err
	}
}

func (s *AccountService) secretsReady() error {
	switch s.secrets.Mode() {
	case SecretModePassphraseLocked:
		return ErrSecretsLocked
	case SecretModeUnavailable:
		return ErrSecretsUnavailable
	default:
		return nil
	}
}

func (s *AccountService) openRemote(ctx context.Context, config Config) (Remote, error) {
	if s.factory == nil {
		return nil, ErrRemoteUnavailable
	}
	remote, err := s.factory.Open(ctx, config)
	if err != nil {
		return nil, ErrRemoteUnavailable
	}
	if remote == nil {
		return nil, ErrRemoteUnavailable
	}
	return remote, nil
}

// loadMasterKey returns the stored key bytes without retaining the encoded
// value. A missing key is not an error; the caller wipes a present key.
func (s *AccountService) loadMasterKey(ctx context.Context) ([]byte, bool, error) {
	present, err := s.secrets.Has(ctx, MasterKeyRef())
	if err != nil {
		return nil, false, err
	}
	if !present {
		return nil, false, nil
	}
	encoded, err := s.secrets.Get(ctx, MasterKeyRef())
	if err != nil {
		return nil, false, err
	}
	key, err := domainhistory.ParseMasterKey(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("%w: stored master key is invalid", ErrSecretUnreadable)
	}
	return key, true, nil
}

func (s *AccountService) setCached(status Status) {
	s.statusMu.Lock()
	s.cached = status
	s.statusMu.Unlock()
}

func accountChanged(existing domainhistory.AccountIdentity, address string, account Account) bool {
	if existing == (domainhistory.AccountIdentity{}) {
		return false
	}
	if SanitizeServerURL(existing.ServerURL) != SanitizeServerURL(address) {
		return true
	}
	if existing.UserID != "" && account.UserID != "" && existing.UserID != account.UserID {
		return true
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
