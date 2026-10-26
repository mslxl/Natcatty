// Package sync owns the cloud-sync use case: connecting a GitHub account,
// publishing the encrypted Vault as a Gist, pulling the cloud copy back, and
// the debounced automatic sync. It consumes ports for the Vault, the remote
// provider, persistence, and time; adapters provide the concrete GitHub, file,
// keychain, and SQLite implementations.
package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// Trigger says what started a sync.
type Trigger string

const (
	// TriggerManual is the user pressing Sync now.
	TriggerManual Trigger = "manual"
	// TriggerAuto is the debounced automatic sync after a local edit.
	TriggerAuto Trigger = "auto"
)

// Problem is the machine-readable reason the UI localizes. It is never
// user-visible English text.
type Problem string

const (
	ProblemNone              Problem = ""
	ProblemNotConfigured     Problem = "not-configured"
	ProblemPasswordRequired  Problem = "password-required"
	ProblemEmptyVault        Problem = "empty-vault"
	ProblemConflict          Problem = "conflict"
	ProblemWrongPassword     Problem = "wrong-password"
	ProblemCorrupt           Problem = "corrupt-envelope"
	ProblemUnauthorized      Problem = "unauthorized"
	ProblemRateLimited       Problem = "rate-limited"
	ProblemNetwork           Problem = "network"
	ProblemLocal             Problem = "local"
	ProblemAuthorization     Problem = "authorization"
	ProblemAlreadyInProgress Problem = "already-syncing"
	ProblemUnsupported       Problem = "unsupported-version"
)

// Status is the sync surface's state.
type Status string

const (
	StatusIdle          Status = "idle"
	StatusAuthorizing   Status = "authorizing"
	StatusSyncing       Status = "syncing"
	StatusSynced        Status = "synced"
	StatusFailed        Status = "failed"
	StatusNeedsPassword Status = "needs-password"
	StatusNeedsSetup    Status = "needs-setup"
)

// Errors the UI maps onto localized messages.
var (
	ErrNotConfigured     = errors.New("sync: GitHub is not connected")
	ErrPasswordRequired  = errors.New("sync: master password required")
	ErrEmptyVault        = errors.New("sync: refusing to overwrite the cloud copy with an empty Vault")
	ErrConflict          = errors.New("sync: the cloud copy changed during the upload")
	ErrAlreadyInProgress = errors.New("sync: a sync is already running")
	// ErrUnsupportedVersion refuses a cloud copy written by a newer schema
	// instead of overwriting it with a payload this build cannot read.
	ErrUnsupportedVersion = errors.New("sync: the cloud copy uses a newer sync schema")
)

// Provider-level errors the remote adapter reports. The application maps them
// to Problem values and to the device-flow loop.
var (
	ErrAuthorizationPending = errors.New("sync: authorization pending")
	ErrSlowDown             = errors.New("sync: slow down")
	ErrAuthorizationDenied  = errors.New("sync: authorization denied")
	ErrDeviceCodeExpired    = errors.New("sync: device code expired")
	ErrUnauthorized         = errors.New("sync: unauthorized")
	ErrRateLimited          = errors.New("sync: rate limited")
	ErrNetwork              = errors.New("sync: network failure")
	ErrRemoteCorrupt        = errors.New("sync: remote envelope is corrupt")
	ErrWrongPassword        = errors.New("sync: incorrect master password")
)

// DeviceCode is the one-time code the user enters on GitHub.
type DeviceCode struct {
	DeviceCode      string `json:"deviceCode"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int64  `json:"expiresIn"`
	Interval        int64  `json:"interval"`
}

// Grant is the authorized account.
type Grant struct {
	Login string
}

// RemoteFile is one cloud copy. ID is empty when no file exists yet; ETag is
// the conditional-write validator the provider returned.
type RemoteFile struct {
	ID        string
	ETag      string
	Content   []byte
	UpdatedAt int64
}

// Remote is the provider port. Every method must be safe to call from another
// goroutine and must never require a real network in tests.
type Remote interface {
	RequestDeviceCode(ctx context.Context) (DeviceCode, error)
	PollDeviceToken(ctx context.Context, code DeviceCode) (domainsync.Token, error)
	User(ctx context.Context, token domainsync.Token) (Grant, error)
	// Read returns the configured Gist's vault file. exists is false when the
	// account has no Netcatty Gist yet.
	Read(ctx context.Context, token domainsync.Token) (file RemoteFile, exists bool, err error)
	// Write creates the Gist when current.ID is empty and updates it
	// conditionally otherwise. A lost conditional write is ErrConflict.
	Write(ctx context.Context, token domainsync.Token, current RemoteFile, content []byte) (RemoteFile, error)
}

// Vault is the local Vault port. Snapshot must include plaintext credential
// values so a second device can restore them; Apply rewrites the synced
// families and never touches local-only tables.
type Vault interface {
	Snapshot(ctx context.Context) (Payload, error)
	Apply(ctx context.Context, payload Payload) error
}

// Config is the non-secret sync profile persisted beside the Vault.
type Config struct {
	Provider        string              `json:"provider"`
	Connected       bool                `json:"connected"`
	Login           string              `json:"login"`
	AutoSync        bool                `json:"autoSync"`
	Strategy        domainsync.Strategy `json:"strategy"`
	DeviceID        string              `json:"deviceId"`
	DeviceName      string              `json:"deviceName"`
	AppVersion      string              `json:"appVersion"`
	LastSyncedAt    int64               `json:"lastSyncedAt"`
	RemoteID        string              `json:"remoteId"`
	RemoteETag      string              `json:"remoteEtag"`
	RemoteUpdatedAt int64               `json:"remoteUpdatedAt"`
	LocalSignature  string              `json:"localSignature"`
}

// ConfigStore persists Config.
type ConfigStore interface {
	Load(ctx context.Context) (Config, error)
	Save(ctx context.Context, config Config) error
}

// TokenStore persists the provider credential outside the JSON config.
type TokenStore interface {
	Load(ctx context.Context) (domainsync.Token, error)
	Save(ctx context.Context, token domainsync.Token) error
	Clear(ctx context.Context) error
}

// Waiter sleeps between device-flow polls. Tests substitute a fake; the app
// uses real time.
type Waiter interface {
	Wait(ctx context.Context, d time.Duration) error
}

type realWaiter struct{}

func (realWaiter) Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Result describes a completed sync.
type Result struct {
	Trigger    Trigger
	Action     domainsync.Action
	Uploaded   bool
	Downloaded bool
}

// Snapshot is the state the UI renders.
type Snapshot struct {
	Connected    bool
	Login        string
	AutoSync     bool
	Strategy     domainsync.Strategy
	Status       Status
	Problem      Problem
	DeviceCode   DeviceCode
	LastSyncedAt int64
	LastAction   domainsync.Action
	HasPassword  bool
}

// Option customizes a Service.
type Option func(*Service)

// WithWaiter sets the device-flow sleep source.
func WithWaiter(waiter Waiter) Option {
	return func(s *Service) {
		if waiter != nil {
			s.waiter = waiter
		}
	}
}

// WithClock sets the service's source of time.
func WithClock(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.now = fn
		}
	}
}

// WithDebounce sets the automatic-sync delay after a local edit.
func WithDebounce(d time.Duration) Option {
	return func(s *Service) {
		if d >= 0 {
			s.debounce = d
		}
	}
}

// WithDevice sets the envelope metadata written on upload.
func WithDevice(id, name, appVersion string) Option {
	return func(s *Service) {
		s.config.DeviceID = id
		s.config.DeviceName = name
		s.config.AppVersion = appVersion
	}
}

// WithRedrawRequest sets the callback invoked after state changes.
func WithRedrawRequest(fn func()) Option {
	return func(s *Service) { s.notify = fn }
}

// WithAppliedSnapshot sets the callback invoked after a downloaded cloud copy
// has been written to the local Vault. The app uses it to refresh
// Vault-backed surfaces without a restart. It runs on the goroutine executing
// the sync, so it must not touch UI-thread state directly.
func WithAppliedSnapshot(fn func()) Option {
	return func(s *Service) { s.onSnapshotApplied = fn }
}

// WithRandom sets the envelope's salt and IV source.
func WithRandom(reader io.Reader) Option {
	return func(s *Service) {
		if reader != nil {
			s.random = reader
		}
	}
}

// Service owns the sync state. Snapshot, NotifyLocalChange, and Stop are safe
// from any goroutine; Sync and Connect serialize on one lock.
type Service struct {
	mu     sync.Mutex
	syncMu sync.Mutex

	config   Config
	remote   Remote
	vault    Vault
	configs  ConfigStore
	tokens   TokenStore
	waiter   Waiter
	now      func() time.Time
	debounce time.Duration
	random   io.Reader
	notify   func()
	// onSnapshotApplied, when set, reports that a downloaded cloud copy has
	// been written locally. It runs on the sync goroutine.
	onSnapshotApplied func()

	password   string
	status     Status
	problem    Problem
	device     DeviceCode
	lastAction domainsync.Action

	timer      *time.Timer
	generation uint64
	scheduled  bool
	pending    bool
	syncing    bool
	stopped    bool
}

// NewService configures a Service. Load must be called before use.
func NewService(remote Remote, vault Vault, configs ConfigStore, tokens TokenStore, opts ...Option) *Service {
	service := &Service{
		remote:   remote,
		vault:    vault,
		configs:  configs,
		tokens:   tokens,
		waiter:   realWaiter{},
		now:      time.Now,
		debounce: 3 * time.Second,
		status:   StatusNeedsSetup,
		problem:  ProblemNotConfigured,
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Load reads the stored profile. A missing config is the unconfigured state.
func (s *Service) Load(ctx context.Context) error {
	config, err := s.configs.Load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	deviceID, deviceName, appVersion := s.config.DeviceID, s.config.DeviceName, s.config.AppVersion
	s.config = config
	if s.config.DeviceID == "" {
		s.config.DeviceID = deviceID
	}
	if s.config.DeviceName == "" {
		s.config.DeviceName = deviceName
	}
	if s.config.AppVersion == "" {
		s.config.AppVersion = appVersion
	}
	s.config.Strategy = domainsync.NormalizeStrategy(s.config.Strategy)
	s.status = StatusNeedsSetup
	s.problem = ProblemNotConfigured
	if s.config.Connected {
		s.status = StatusNeedsPassword
		s.problem = ProblemPasswordRequired
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

// SetPassword unlocks cloud sync for this run. The password is never written
// to disk; App Lock and process exit discard it.
func (s *Service) SetPassword(password string) error {
	if password == "" {
		return ErrPasswordRequired
	}
	s.mu.Lock()
	s.password = password
	if s.status == StatusNeedsPassword {
		s.status = StatusIdle
		s.problem = ProblemNone
	}
	s.mu.Unlock()
	s.changed()
	return nil
}

// ClearPassword forgets the master password, for example when App Lock
// engages.
func (s *Service) ClearPassword() {
	s.mu.Lock()
	s.password = ""
	if s.config.Connected {
		s.status = StatusNeedsPassword
		s.problem = ProblemPasswordRequired
	}
	s.mu.Unlock()
	s.changed()
}

// Snapshot returns the current state.
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Connected:    s.config.Connected,
		Login:        s.config.Login,
		AutoSync:     s.config.AutoSync,
		Strategy:     domainsync.NormalizeStrategy(s.config.Strategy),
		Status:       s.status,
		Problem:      s.problem,
		DeviceCode:   s.device,
		LastSyncedAt: s.config.LastSyncedAt,
		LastAction:   s.lastAction,
		HasPassword:  s.password != "",
	}
}

// SetAutoSync persists the automatic-sync preference. Turning it on schedules
// a sync after the debounce interval so an edit made while it was off is not
// lost.
func (s *Service) SetAutoSync(ctx context.Context, enabled bool) error {
	s.mu.Lock()
	config := s.config
	config.AutoSync = enabled
	s.config = config
	s.mu.Unlock()
	if err := s.configs.Save(ctx, config); err != nil {
		return err
	}
	if enabled {
		s.NotifyLocalChange()
	}
	s.changed()
	return nil
}

// SetStrategy persists the whole-payload conflict strategy.
func (s *Service) SetStrategy(ctx context.Context, strategy domainsync.Strategy) error {
	strategy = domainsync.NormalizeStrategy(strategy)
	s.mu.Lock()
	config := s.config
	config.Strategy = strategy
	s.config = config
	s.mu.Unlock()
	if err := s.configs.Save(ctx, config); err != nil {
		return err
	}
	s.changed()
	return nil
}

// Connect runs GitHub's device flow. onDevice is called with the code the user
// must enter before the first poll; it may be nil.
func (s *Service) Connect(ctx context.Context, password string, onDevice func(DeviceCode)) error {
	if password == "" {
		return ErrPasswordRequired
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	s.mu.Lock()
	s.password = password
	s.status = StatusAuthorizing
	s.problem = ProblemNone
	s.device = DeviceCode{}
	s.mu.Unlock()
	s.changed()

	code, err := s.remote.RequestDeviceCode(ctx)
	if err != nil {
		s.fail(problemFor(err))
		return err
	}
	s.mu.Lock()
	s.device = code
	s.mu.Unlock()
	s.changed()
	if onDevice != nil {
		onDevice(code)
	}

	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	for {
		token, err := s.remote.PollDeviceToken(ctx, code)
		switch {
		case err == nil:
			grant, userErr := s.remote.User(ctx, token)
			if userErr != nil {
				s.fail(problemFor(userErr))
				return userErr
			}
			if saveErr := s.tokens.Save(ctx, token); saveErr != nil {
				s.fail(ProblemLocal)
				return saveErr
			}
			s.mu.Lock()
			s.config.Connected = true
			s.config.Login = grant.Login
			config := s.config
			s.device = DeviceCode{}
			s.status = StatusIdle
			s.problem = ProblemNone
			s.mu.Unlock()
			if saveErr := s.configs.Save(ctx, config); saveErr != nil {
				s.fail(ProblemLocal)
				return saveErr
			}
			s.changed()
			return nil
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
		case errors.Is(err, ErrAuthorizationPending):
			// Keep the server-provided cadence.
		case errors.Is(err, ErrDeviceCodeExpired):
			s.fail(ProblemAuthorization)
			return err
		case errors.Is(err, ErrAuthorizationDenied):
			s.fail(ProblemAuthorization)
			return err
		default:
			s.fail(problemFor(err))
			return err
		}
		if err := s.waiter.Wait(ctx, interval); err != nil {
			s.fail(problemFor(err))
			return err
		}
	}
}

// Disconnect forgets the provider token and the baseline. It leaves the local
// Vault untouched.
func (s *Service) Disconnect(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err := s.tokens.Clear(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	deviceID, deviceName, appVersion := s.config.DeviceID, s.config.DeviceName, s.config.AppVersion
	s.config = Config{
		Provider:   "github",
		DeviceID:   deviceID,
		DeviceName: deviceName,
		AppVersion: appVersion,
		Strategy:   domainsync.StrategyNewest,
	}
	s.status = StatusNeedsSetup
	s.problem = ProblemNotConfigured
	s.device = DeviceCode{}
	s.mu.Unlock()
	if err := s.configs.Save(ctx, s.currentConfig()); err != nil {
		return err
	}
	s.changed()
	return nil
}

// Sync runs one manual or automatic sync. It is safe to call from the UI
// thread: the caller usually runs it in the UI's worker goroutine.
func (s *Service) Sync(ctx context.Context, trigger Trigger) (Result, error) {
	if !s.syncMu.TryLock() {
		return Result{Trigger: trigger}, ErrAlreadyInProgress
	}
	defer s.syncMu.Unlock()
	return s.syncLocked(ctx, trigger)
}

func (s *Service) syncLocked(ctx context.Context, trigger Trigger) (Result, error) {
	token, err := s.tokens.Load(ctx)
	if err != nil {
		if errors.Is(err, errNoToken) {
			s.fail(ProblemNotConfigured)
			return Result{Trigger: trigger}, ErrNotConfigured
		}
		s.fail(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	s.mu.Lock()
	password := s.password
	configured := s.config.Connected
	s.mu.Unlock()
	if !configured {
		s.fail(ProblemNotConfigured)
		return Result{Trigger: trigger}, ErrNotConfigured
	}
	if password == "" {
		s.mu.Lock()
		s.status = StatusNeedsPassword
		s.problem = ProblemPasswordRequired
		s.mu.Unlock()
		s.changed()
		return Result{Trigger: trigger}, ErrPasswordRequired
	}

	s.mu.Lock()
	s.status = StatusSyncing
	s.syncing = true
	s.problem = ProblemNone
	s.mu.Unlock()
	s.changed()

	local, snapErr := s.vault.Snapshot(ctx)
	if snapErr != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, snapErr
	}
	if !local.HasData() {
		// The guard runs before any write. An empty local Vault must never
		// replace a populated cloud copy, whether the trigger is manual or
		// automatic.
		s.finishSync(ProblemEmptyVault)
		return Result{Trigger: trigger}, ErrEmptyVault
	}

	remoteFile, exists, readErr := s.remote.Read(ctx, token)
	if readErr != nil {
		s.finishSync(problemFor(readErr))
		return Result{Trigger: trigger}, readErr
	}

	strategy := s.currentConfig().Strategy
	localSignature, err := local.Signature()
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	config := s.currentConfig()

	if !exists {
		return s.upload(ctx, trigger, token, local, localSignature, RemoteFile{}, config)
	}

	remotePayload, remoteUpdatedAt, problem, err := s.decodeRemote(remoteFile)
	if err != nil {
		s.finishSync(problem)
		return Result{Trigger: trigger}, err
	}

	localChanged := config.LocalSignature == "" || config.LocalSignature != localSignature
	remoteChanged := config.RemoteUpdatedAt == 0 ||
		config.RemoteUpdatedAt != remoteUpdatedAt ||
		(config.RemoteETag != "" && config.RemoteETag != remoteFile.ETag)

	action := domainsync.Decide(localChanged, remoteChanged, strategy, local.SyncedAt, remoteUpdatedAt)
	if !remotePayload.HasData() {
		// Never let an empty cloud copy replace a populated local Vault: a
		// first-ever upload is the only correct action.
		action = domainsync.ActionUpload
	}

	switch action {
	case domainsync.ActionDownload:
		return s.download(ctx, trigger, remoteFile, remotePayload, remoteUpdatedAt)
	case domainsync.ActionUpload:
		return s.upload(ctx, trigger, token, local, localSignature, remoteFile, config)
	default:
		if err := s.recordSync(remoteFile, localSignature, remoteUpdatedAt); err != nil {
			s.finishSync(ProblemLocal)
			return Result{Trigger: trigger}, err
		}
		s.completeSync(domainsync.ActionNone)
		return Result{Trigger: trigger, Action: domainsync.ActionNone}, nil
	}
}

// decodeRemote parses and decrypts one cloud copy. It fails closed on a newer
// sync schema (syncSchemaVersion 2 is the CRDT payload owned by ticket 54):
// decoding that document as this build's v1 snapshot would yield an empty
// payload and let an upload erase the cloud copy.
func (s *Service) decodeRemote(file RemoteFile) (Payload, int64, Problem, error) {
	envelope, err := domainsync.UnmarshalFile(file.Content)
	if err != nil {
		return Payload{}, 0, ProblemCorrupt, fmt.Errorf("%w: %v", ErrRemoteCorrupt, err)
	}
	if envelope.Meta.SyncSchemaVersion > 1 {
		return Payload{}, 0, ProblemUnsupported, ErrUnsupportedVersion
	}
	updatedAt := envelope.Meta.UpdatedAt
	if file.UpdatedAt != 0 {
		updatedAt = file.UpdatedAt
	}
	plaintext, err := domainsync.DecryptPayload(envelope, s.passwordValue())
	if err != nil {
		switch {
		case errors.Is(err, domainsync.ErrPasswordRequired):
			return Payload{}, 0, ProblemPasswordRequired, ErrPasswordRequired
		case errors.Is(err, domainsync.ErrWrongPassword):
			return Payload{}, 0, ProblemWrongPassword, ErrWrongPassword
		default:
			return Payload{}, 0, ProblemCorrupt, fmt.Errorf("%w: %v", ErrRemoteCorrupt, err)
		}
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		return Payload{}, 0, ProblemCorrupt, fmt.Errorf("%w: %v", ErrRemoteCorrupt, err)
	}
	return payload, updatedAt, ProblemNone, nil
}

// download applies a decided cloud payload and records the applied snapshot as
// the new baseline. Both the normal path and the lost-write path use it so the
// next sync converges to a no-op.
func (s *Service) download(
	ctx context.Context,
	trigger Trigger,
	remoteFile RemoteFile,
	remotePayload Payload,
	remoteUpdatedAt int64,
) (Result, error) {
	if err := s.vault.Apply(ctx, remotePayload); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	// The Vault now holds the cloud copy. Notify Vault-backed surfaces before
	// the baseline bookkeeping: even if saving the baseline fails, the local
	// data has already changed and the UI must show it.
	s.notifySnapshotApplied()
	appliedSignature, err := s.appliedSignature(ctx)
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	if err := s.recordSync(remoteFile, appliedSignature, remoteUpdatedAt); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	s.completeSync(domainsync.ActionDownload)
	return Result{Trigger: trigger, Action: domainsync.ActionDownload, Downloaded: true}, nil
}

// upload encrypts and writes the local payload. A conditional-write conflict
// re-reads the cloud copy once and decides again, so a concurrent device can
// never be silently overwritten.
func (s *Service) upload(
	ctx context.Context,
	trigger Trigger,
	token domainsync.Token,
	local Payload,
	localSignature string,
	current RemoteFile,
	config Config,
) (Result, error) {
	for attempt := 0; attempt < 2; attempt++ {
		envelope, err := domainsync.EncryptPayload(local, s.passwordValue(), domainsync.EncryptOptions{
			Version:    versionFor(current),
			UpdatedAt:  s.now().UnixMilli(),
			DeviceID:   config.DeviceID,
			DeviceName: config.DeviceName,
			AppVersion: config.AppVersion,
			Random:     s.random,
			Now:        s.now,
		})
		if err != nil {
			s.finishSync(ProblemLocal)
			return Result{Trigger: trigger}, err
		}
		content, err := domainsync.MarshalFile(envelope)
		if err != nil {
			s.finishSync(ProblemLocal)
			return Result{Trigger: trigger}, err
		}
		written, writeErr := s.remote.Write(ctx, token, current, content)
		if errors.Is(writeErr, ErrConflict) && attempt == 0 {
			// Last-write-wins with a fresh observation: read what landed and
			// make the strategy decision again instead of forcing this copy.
			fresh, exists, readErr := s.remote.Read(ctx, token)
			if readErr != nil {
				s.finishSync(problemFor(readErr))
				return Result{Trigger: trigger}, readErr
			}
			if !exists {
				current = RemoteFile{}
				continue
			}
			remotePayload, remoteUpdatedAt, problem, decodeErr := s.decodeRemote(fresh)
			if decodeErr != nil {
				s.finishSync(problem)
				return Result{Trigger: trigger}, decodeErr
			}
			if !remotePayload.HasData() {
				// The same guard as the normal path: an empty cloud copy can
				// never replace this populated Vault, even when the conflict
				// strategy would otherwise choose the remote.
				current = fresh
				continue
			}
			action := domainsync.Decide(true, true, config.Strategy, local.SyncedAt, remoteUpdatedAt)
			if action == domainsync.ActionDownload {
				return s.download(ctx, trigger, fresh, remotePayload, remoteUpdatedAt)
			}
			current = fresh
			continue
		}
		if writeErr != nil {
			s.finishSync(problemFor(writeErr))
			return Result{Trigger: trigger}, writeErr
		}
		if written.UpdatedAt == 0 {
			written.UpdatedAt = envelope.Meta.UpdatedAt
		}
		if err := s.recordSync(written, localSignature, written.UpdatedAt); err != nil {
			s.finishSync(ProblemLocal)
			return Result{Trigger: trigger}, err
		}
		s.completeSync(domainsync.ActionUpload)
		return Result{Trigger: trigger, Action: domainsync.ActionUpload, Uploaded: true}, nil
	}
	s.finishSync(ProblemConflict)
	return Result{Trigger: trigger}, ErrConflict
}

func versionFor(current RemoteFile) int {
	if current.ID == "" {
		return 1
	}
	if envelope, err := domainsync.UnmarshalFile(current.Content); err == nil && envelope.Meta.Version > 0 {
		return envelope.Meta.Version + 1
	}
	return 1
}

// appliedSignature re-reads the Vault after Apply so the baseline records what
// is actually stored. The remote payload's serialization is not a reliable
// substitute: an older writer's syncedAt and field set may differ from this
// build's snapshot, and a stale baseline would make the next sync upload the
// copy it just downloaded.
func (s *Service) appliedSignature(ctx context.Context) (string, error) {
	applied, err := s.vault.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return applied.Signature()
}

// recordSync persists the new baseline. The caller reports a save failure as
// ProblemLocal, so a sync is never presented as successful when the next run
// would not know what it synced.
func (s *Service) recordSync(remote RemoteFile, localSignature string, remoteUpdatedAt int64) error {
	s.mu.Lock()
	config := s.config
	config.LocalSignature = localSignature
	config.RemoteID = remote.ID
	config.RemoteETag = remote.ETag
	config.RemoteUpdatedAt = remoteUpdatedAt
	config.LastSyncedAt = s.now().UnixMilli()
	s.config = config
	s.mu.Unlock()
	if err := s.configs.Save(context.Background(), config); err != nil {
		return fmt.Errorf("sync: save the profile: %w", err)
	}
	return nil
}

func (s *Service) completeSync(action domainsync.Action) {
	s.mu.Lock()
	s.status = StatusSynced
	s.problem = ProblemNone
	s.lastAction = action
	s.syncing = false
	pending := s.pending
	s.pending = false
	s.mu.Unlock()
	s.changed()
	if pending {
		s.NotifyLocalChange()
	}
}

func (s *Service) finishSync(problem Problem) {
	s.mu.Lock()
	s.status = StatusFailed
	s.problem = problem
	s.syncing = false
	pending := s.pending
	s.pending = false
	s.mu.Unlock()
	s.changed()
	if pending {
		s.NotifyLocalChange()
	}
}

// NotifyLocalChange queues a debounced automatic sync. It is called by the
// Vault change hook after a committed write and never blocks.
func (s *Service) NotifyLocalChange() {
	s.mu.Lock()
	if s.stopped || !s.config.AutoSync {
		s.mu.Unlock()
		return
	}
	if s.syncing {
		s.pending = true
		s.mu.Unlock()
		return
	}
	if s.scheduled {
		s.mu.Unlock()
		return
	}
	s.scheduled = true
	s.generation++
	generation := s.generation
	debounce := s.debounce
	s.mu.Unlock()

	s.timer = time.AfterFunc(debounce, func() {
		s.mu.Lock()
		if s.stopped || generation != s.generation {
			s.scheduled = false
			s.mu.Unlock()
			return
		}
		s.scheduled = false
		s.mu.Unlock()
		// Sync records the typed problem through finishSync; the error is not
		// a second state channel.
		_, _ = s.Sync(context.Background(), TriggerAuto)
	})
}

// Stop cancels a pending debounce and prevents new automatic syncs. The app
// calls it at shutdown.
func (s *Service) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.scheduled = false
	timer := s.timer
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// CancelDebounce drops a pending automatic sync without stopping the service.
// Tests and "turn auto sync off" use it.
func (s *Service) CancelDebounce() {
	s.mu.Lock()
	s.generation++
	s.scheduled = false
	timer := s.timer
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (s *Service) currentConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

func (s *Service) passwordValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

func (s *Service) fail(problem Problem) {
	s.mu.Lock()
	s.status = StatusFailed
	s.problem = problem
	s.mu.Unlock()
	s.changed()
}

func (s *Service) changed() {
	s.mu.Lock()
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// notifySnapshotApplied reports that a downloaded payload was written to the
// local Vault. The callback may run on any goroutine and must not block.
func (s *Service) notifySnapshotApplied() {
	s.mu.Lock()
	applied := s.onSnapshotApplied
	s.mu.Unlock()
	if applied != nil {
		applied()
	}
}

func problemFor(err error) Problem {
	switch {
	case err == nil:
		return ProblemNone
	case errors.Is(err, ErrUnauthorized), errors.Is(err, errNoToken):
		return ProblemUnauthorized
	case errors.Is(err, ErrRateLimited):
		return ProblemRateLimited
	case errors.Is(err, ErrNetwork):
		return ProblemNetwork
	case errors.Is(err, ErrAuthorizationDenied), errors.Is(err, ErrDeviceCodeExpired),
		errors.Is(err, ErrAuthorizationPending), errors.Is(err, ErrSlowDown):
		return ProblemAuthorization
	case errors.Is(err, ErrConflict):
		return ProblemConflict
	case errors.Is(err, ErrUnsupportedVersion):
		return ProblemUnsupported
	case errors.Is(err, ErrNotConfigured):
		return ProblemNotConfigured
	case errors.Is(err, ErrPasswordRequired):
		return ProblemPasswordRequired
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ProblemNetwork
	default:
		return ProblemLocal
	}
}

// errNoToken is the application's convention for "the token store has no
// credential". The file/keychain adapter returns it.
var errNoToken = errors.New("sync: no stored token")

// ErrNoToken exposes the sentinel for adapters in another package.
var ErrNoToken = errNoToken

func decodePayloadBytes(data []byte) (Payload, error) {
	var payload Payload
	if len(data) == 0 {
		return payload, fmt.Errorf("empty plaintext")
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

// Signature is a stable hash of the payload used to detect local edits since
// the last sync.
func (p Payload) Signature() (string, error) {
	data, err := domainsync.MarshalJSON(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// SecretValue is one credential carried inside the encrypted payload. The
// plaintext never reaches the provider; the receiving device re-encrypts it
// with its own field key.
type SecretValue struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	Field      string `json:"field"`
	Value      string `json:"value"`
}
