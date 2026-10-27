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
	"net/url"
	"strings"
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
	// ProblemReauthorization reports that the provider rejected its refresh
	// token and a fresh interactive authorization is required. It is
	// actionable: the UI offers Reconnect instead of a generic failure.
	ProblemReauthorization Problem = "reauthorization"
	// ProblemCredentials reports that a WebDAV endpoint rejected the stored
	// username/password or bearer token. It is actionable: the UI offers
	// Reconfigure instead of retrying the same credential.
	ProblemCredentials Problem = "credentials-rejected"
	// ProblemForbidden is a WebDAV 403, which is a permission problem rather
	// than a rejected credential.
	ProblemForbidden Problem = "forbidden"
	// ProblemTLS reports that the endpoint's TLS certificate could not be
	// verified. The app never disables certificate verification.
	ProblemTLS Problem = "tls"
	// ProblemInvalidEndpoint reports a WebDAV URL that is not a usable
	// http/https collection.
	ProblemInvalidEndpoint Problem = "invalid-endpoint"
	// ProblemInsecureEndpoint is the explicit-confirmation gate for a plain
	// HTTP WebDAV endpoint. No credential is sent until the user confirms.
	ProblemInsecureEndpoint Problem = "insecure-endpoint"
	// ProblemRedirect reports a redirect to another host or an HTTPS to HTTP
	// downgrade, which this build refuses rather than leak the credential.
	ProblemRedirect Problem = "unsafe-redirect"
	// ProblemWrongRegion reports that the S3 bucket lives in a different
	// region from the one the request was signed for (PermanentRedirect).
	// No retry is attempted: the user must correct the region.
	ProblemWrongRegion Problem = "wrong-region"
	// ProblemShrinkGuard reports that the outgoing payload would delete too
	// much of a family compared with the merge anchor or cloud copy. The UI
	// offers Restore and an explicitly confirmed Force push; automatic sync
	// never forces.
	ProblemShrinkGuard Problem = "shrink-guard"
	// ProblemTombstoneGuard reports that the outgoing payload would replace
	// the whole cloud Vault with nothing (or with tombstones for everything).
	ProblemTombstoneGuard Problem = "tombstone-guard"
	// ProblemBackup reports that a mandatory protective backup could not be
	// written, so the destructive operation was refused.
	ProblemBackup Problem = "backup-failed"
	// ProblemConvergentMigration reports that convergent sync could not be
	// enabled because the migration plan is fail-closed: an unknown or newer
	// schema, a mismatched envelope, a damaged replica, a legacy payload
	// without a trusted baseline, unresolved conflicts, or a suspected shrink.
	// The cloud bytes are left untouched.
	ProblemConvergentMigration Problem = "convergent-migration-blocked"
	// ProblemConvergentDivergence reports that two replicas with the same
	// causal history serialize differently, or that a provider did not
	// converge after all verification rounds. No side is overwritten
	// automatically; the UI offers inspect, restore, and re-sync.
	ProblemConvergentDivergence Problem = "convergent-divergence"
	// ProblemConvergentCorrupt reports a damaged or partially migrated v2
	// local replica or envelope. The service refuses to fall back to v1.
	ProblemConvergentCorrupt Problem = "convergent-corrupt"
	// ProblemConvergentPaused reports that convergent sync was initialized and
	// later paused. The legacy writer never resumes implicitly.
	ProblemConvergentPaused Problem = "convergent-paused"
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
	// StatusConflict reports an unresolved both-sides change that survived a
	// restart and blocks further syncs until the user resolves it.
	StatusConflict Status = "conflict"
)

// Errors the UI maps onto localized messages.
var (
	ErrNotConfigured     = errors.New("sync: the provider is not connected")
	ErrPasswordRequired  = errors.New("sync: master password required")
	ErrEmptyVault        = errors.New("sync: refusing to overwrite the cloud copy with an empty Vault")
	ErrConflict          = errors.New("sync: the cloud copy changed during the upload")
	ErrAlreadyInProgress = errors.New("sync: a sync is already running")
	// ErrShrinkGuard refuses an upload that would delete too many records.
	ErrShrinkGuard = errors.New("sync: the upload would delete too much of the Vault")
	// ErrTombstoneGuard refuses an upload that would empty the cloud Vault.
	ErrTombstoneGuard = errors.New("sync: the upload would replace the cloud Vault with nothing")
	// ErrBackupUnavailable reports that the mandatory protective backup could
	// not be written, so the destructive operation was aborted.
	ErrBackupUnavailable = errors.New("sync: the protective backup could not be written")
	// ErrUnsupportedVersion refuses a cloud copy written by a newer schema
	// instead of overwriting it with a payload this build cannot read.
	ErrUnsupportedVersion = errors.New("sync: the cloud copy uses a newer sync schema")
	// ErrConvergentMigration refuses to enable v2 unless every migration input
	// is understood and has a trusted baseline.
	ErrConvergentMigration = errors.New("sync: convergent migration is blocked")
	// ErrConvergentDivergence refuses to overwrite a genuinely divergent
	// replica.
	ErrConvergentDivergence = errors.New("sync: convergent replicas diverged")
	// ErrConvergentCorrupt reports a damaged or incomplete local v2 record.
	ErrConvergentCorrupt = errors.New("sync: the local convergent record is damaged")
	// ErrConvergentPaused refuses to fall through to the legacy writer after
	// convergent sync has been initialized.
	ErrConvergentPaused = errors.New("sync: convergent sync is paused")
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

	// ErrReauthorizationRequired reports that the provider rejected the
	// refresh token (invalid_grant). The service starts a fresh browser
	// authorization and resumes the operation; if that cannot complete, the
	// UI shows the actionable reconnect state.
	ErrReauthorizationRequired = errors.New("sync: the provider requires a fresh authorization")
	// ErrAuthorizationTimeout reports that the loopback redirect did not
	// arrive before the authorization deadline.
	ErrAuthorizationTimeout = errors.New("sync: authorization timed out")
	// ErrAuthorizationState reports a redirect whose state parameter did not
	// match the one this attempt generated.
	ErrAuthorizationState = errors.New("sync: authorization state did not match")
	// ErrBrowserUnavailable reports that no system-browser opener is wired.
	ErrBrowserUnavailable = errors.New("sync: no system browser is available")

	// ErrCredentialsRejected reports that a provider rejected the stored
	// credential itself (WebDAV 401). Unlike ErrUnauthorized it is not
	// retryable with a refresh: the user must re-enter the credential.
	ErrCredentialsRejected = errors.New("sync: the provider rejected the stored credentials")
	// ErrForbidden reports that the credential is valid but the provider
	// refuses this operation (HTTP 403). It is distinct from a rejected
	// credential so the UI can say what to fix.
	ErrForbidden = errors.New("sync: the provider refused access")
	// ErrTLS reports that the endpoint's certificate chain could not be
	// verified. This build never disables certificate verification.
	ErrTLS = errors.New("sync: the endpoint certificate could not be verified")
	// ErrInvalidEndpoint reports a provider endpoint that cannot be used.
	ErrInvalidEndpoint = errors.New("sync: the provider endpoint is invalid")
	// ErrInsecureEndpoint reports a plain-HTTP endpoint whose credentials
	// would travel in clear. The caller must ask the user for explicit
	// confirmation before retrying.
	ErrInsecureEndpoint = errors.New("sync: the provider endpoint is not encrypted")
	// ErrRedirectRefused reports a redirect this build will not follow: a
	// different host (which could receive the credential) or an HTTPS to
	// plain-HTTP downgrade.
	ErrRedirectRefused = errors.New("sync: the provider redirected to an unsafe location")
	// ErrWrongRegion reports an S3 PermanentRedirect: the bucket belongs to a
	// different region from the one in the SigV4 credential scope. The
	// original SDK client did not follow region redirects, and neither does
	// this build; the user corrects the region instead.
	ErrWrongRegion = errors.New("sync: the S3 bucket is in a different region")
)

// Provider identifiers persisted in the Sync Profile. They match the original
// provider names.
const (
	ProviderGitHub      = "github"
	ProviderGoogleDrive = "google"
	ProviderOneDrive    = "onedrive"
	ProviderWebDAV      = "webdav"
	ProviderS3          = "s3"
)

// NormalizeProvider maps empty and unknown historical values onto the
// GitHub profile this build has always written.
func NormalizeProvider(provider string) string {
	switch provider {
	case ProviderGoogleDrive:
		return ProviderGoogleDrive
	case ProviderOneDrive:
		return ProviderOneDrive
	case ProviderWebDAV:
		return ProviderWebDAV
	case ProviderS3:
		return ProviderS3
	default:
		return ProviderGitHub
	}
}

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
	// ID is the provider's stable account id. Google returns a numeric-ish
	// string, GitHub a numeric id. It is the merge-anchor identity.
	ID string
	// Login is the display identity: the GitHub login or the Google email.
	Login string
}

// Identity returns the value compared across authorizations. A provider that
// reports no id falls back to the login so a same-name reconnect keeps its
// anchor.
func (g Grant) Identity() string {
	if g.ID != "" {
		return g.ID
	}
	return g.Login
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
	User(ctx context.Context, token domainsync.Token) (Grant, error)
	// Read returns the provider's vault file. exists is false when the
	// account has no Netcatty cloud copy yet.
	Read(ctx context.Context, token domainsync.Token) (file RemoteFile, exists bool, err error)
	// Write creates the file when current.ID is empty and updates it
	// otherwise. A lost conditional write is ErrConflict.
	Write(ctx context.Context, token domainsync.Token, current RemoteFile, content []byte) (RemoteFile, error)
}

// DeviceAuthorizer is implemented by remotes that run the RFC 8628 device
// grant (GitHub today).
type DeviceAuthorizer interface {
	RequestDeviceCode(ctx context.Context) (DeviceCode, error)
	PollDeviceToken(ctx context.Context, code DeviceCode) (domainsync.Token, error)
}

// BrowserAuthorizer is implemented by remotes that use an authorization code
// with PKCE in the system browser and a loopback redirect (Google Drive).
// Authorize must open the URL through open, listen on loopback, validate the
// redirect's state, and exchange the code without a client secret. Tests pass
// an opener that drives a local fake.
type BrowserAuthorizer interface {
	Authorize(ctx context.Context, open func(url string) error) (domainsync.Token, Grant, error)
}

// TokenRefresher is implemented by remotes whose access tokens expire. A
// rejected refresh token is ErrReauthorizationRequired; the service turns
// that into a fresh browser authorization.
type TokenRefresher interface {
	Refresh(ctx context.Context, token domainsync.Token) (domainsync.Token, error)
}

// WebDAVSettings is the WebDAV form's user-entered connection state. Password
// is the basic/digest secret and Token the bearer secret; exactly one of them
// is used for the selected AuthType.
type WebDAVSettings struct {
	Endpoint string
	AuthType string
	Username string
	Password string
	Token    string
}

// WebDAVConnector is implemented by remotes that connect with explicit,
// user-entered settings rather than an interactive authorization. Prepare
// validates and normalizes the endpoint and reports whether the connection
// would send credentials without transport encryption; the service refuses to
// call Connect until the user has explicitly confirmed that case.
type WebDAVConnector interface {
	Prepare(settings WebDAVSettings) (WebDAVSettings, bool, error)
	Connect(ctx context.Context, settings WebDAVSettings) (domainsync.Token, Grant, error)
}

// S3Settings is the S3 form's user-entered connection state. AccessKeyID,
// SecretAccessKey, and SessionToken are secrets: they go to the secure token
// store and never into the profile, a snapshot, or a log.
type S3Settings struct {
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// S3Connector is implemented by remotes that connect with explicit S3
// settings rather than an interactive authorization. Prepare validates and
// normalizes the settings and reports whether the endpoint would send the
// signing material without transport encryption; the service refuses to call
// Connect until the user has explicitly confirmed that case.
type S3Connector interface {
	Prepare(settings S3Settings) (S3Settings, bool, error)
	Connect(ctx context.Context, settings S3Settings) (domainsync.Token, Grant, error)
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
	Provider  string `json:"provider"`
	Connected bool   `json:"connected"`
	Login     string `json:"login"`
	// AccountID is the provider's stable account identity. A different value
	// across authorizations rotates the merge anchor.
	AccountID       string              `json:"accountId,omitempty"`
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
	// AnchorVersion is the merge-anchor format. It is 3 for the encrypted
	// base payload introduced with issue 54; an unknown version is ignored
	// and the next successful sync writes a fresh one.
	AnchorVersion int `json:"anchorVersion,omitempty"`
	// SyncBase is the encrypted merge anchor: the last successfully synced
	// payload, sealed with the master password exactly like the cloud copy.
	// cloud-sync.json therefore contains no plaintext entity or credential.
	SyncBase string `json:"syncBase,omitempty"`
	// BaseSignature is the SHA-256 of the plaintext base payload. A mismatch
	// means the anchor is corrupt and the merge falls back to no base.
	BaseSignature string `json:"baseSignature,omitempty"`
	// PendingConflict is an unresolved both-sides change. It persists across
	// restarts; the encrypted cloud copy is kept so resolution does not
	// depend on the network.
	PendingConflict *PendingConflict `json:"pendingConflict,omitempty"`
	// RemoteBackups retains recent cloud copies that a resolution or restore
	// overwrote, so either side can be recovered. Content is the original
	// encrypted envelope.
	RemoteBackups []RemoteBackup `json:"remoteBackups,omitempty"`
	// Convergent holds the v2 CRDT replica and trusted materialized baselines.
	// Only ciphertext is persisted. The device-local Enabled flag pauses v2
	// without deleting any causal history; SchemaVersion 2 is the explicit
	// migration marker.
	Convergent ConvergentConfig `json:"convergent,omitempty"`
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
	// Writes is the number of provider writes performed. A repeated sync after
	// convergence reports zero, which is how the UI and self-test prove
	// idempotence.
	Writes int
}

// Snapshot is the state the UI renders.
type Snapshot struct {
	Connected    bool
	Provider     string
	Login        string
	AutoSync     bool
	Strategy     domainsync.Strategy
	Status       Status
	Problem      Problem
	DeviceCode   DeviceCode
	LastSyncedAt int64
	LastAction   domainsync.Action
	HasPassword  bool
	// WebDAV connection state for the setup form. The secret is never part of
	// the snapshot.
	WebDAVEndpoint string
	WebDAVAuthType string
	WebDAVUsername string
	// WebDAVInsecure reports a saved plain-HTTP endpoint. Automatic sync is
	// never started for one, and a manual sync asks for confirmation first.
	WebDAVInsecure bool
	// S3 connection state for the setup form. None of the signing material is
	// ever part of the snapshot.
	S3Endpoint string
	S3Region   string
	S3Bucket   string
	S3Prefix   string
	// S3Insecure reports a saved plain-HTTP endpoint, with the same gate as
	// WebDAV: automatic sync never starts, and a manual sync asks first.
	S3Insecure bool
	// Guard is the destructive-sync finding that blocked the last upload.
	// The zero value means no guard is active.
	Guard GuardFinding
	// Conflict is the unresolved both-sides change, including per-item detail
	// and whether the current strategy may merge. It survives a restart.
	Conflict *ConflictInfo
	// Backups lists restorable restore points: local Vault backups and cloud
	// copies that a resolution overwrote. Newest first.
	Backups []BackupInfo
	// BackupsProblem reports why the backup list could not be loaded.
	BackupsProblem Problem
	// Convergent is the v2 enable/verification/divergence surface.
	Convergent ConvergentInfo
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

// WithBackups sets the local Vault backup port. When configured, every
// destructive local apply takes a protective backup first and fails closed if
// it cannot. Conflict resolution additionally requires the port, so
// overwriting either side always writes a backup.
func WithBackups(backups Backups) Option {
	return func(s *Service) {
		if backups != nil {
			s.backups = backups
		}
	}
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

// WithProvider names the default provider of a new profile and registers the
// service's primary remote under it. The zero value is GitHub.
func WithProvider(provider string) Option {
	return func(s *Service) {
		provider = NormalizeProvider(provider)
		if s.remote != nil {
			// NewService registered the primary remote under GitHub. When the
			// caller names a different default, that registration would route
			// a later switch back to GitHub into the wrong provider.
			if s.remotes[ProviderGitHub] == s.remote {
				delete(s.remotes, ProviderGitHub)
			}
			s.remotes[provider] = s.remote
		}
		s.provider = provider
	}
}

// WithRemote registers a remote for another provider, so one Sync Profile can
// switch between GitHub and Google Drive.
func WithRemote(provider string, remote Remote) Option {
	return func(s *Service) {
		if remote != nil {
			s.remotes[NormalizeProvider(provider)] = remote
		}
	}
}

// WithBrowserOpener supplies the system-browser opener a browser-authorized
// provider uses. The composition root passes mygo.Shell.OpenExternal; tests
// pass a fake. The callback runs on the sync goroutine and must not touch UI
// state directly.
func WithBrowserOpener(open func(url string) error) Option {
	return func(s *Service) { s.openBrowser = open }
}

// Service owns the sync state. Snapshot, NotifyLocalChange, and Stop are safe
// from any goroutine; Sync and Connect serialize on one lock.
type Service struct {
	mu     sync.Mutex
	syncMu sync.Mutex
	// tokenMu serializes token rotation. Concurrent callers that find an
	// expired credential wait here and then reuse the rotated one instead of
	// refreshing again.
	tokenMu sync.Mutex

	config   Config
	remote   Remote
	remotes  map[string]Remote
	provider string
	vault    Vault
	configs  ConfigStore
	tokens   TokenStore
	backups  Backups
	waiter   Waiter
	now      func() time.Time
	debounce time.Duration
	random   io.Reader
	notify   func()
	// openBrowser opens a provider's authorization URL in the system browser.
	openBrowser func(url string) error
	// onSnapshotApplied, when set, reports that a downloaded cloud copy has
	// been written locally. It runs on the sync goroutine.
	onSnapshotApplied func()

	password       string
	status         Status
	problem        Problem
	device         DeviceCode
	lastAction     domainsync.Action
	guard          GuardFinding
	backupsList    []BackupInfo
	backupsProblem Problem
	convergent     ConvergentInfo

	// WebDAV connection state. The secrets stay in the token store; these are
	// the non-secret fields the setup form prefills.
	webdavEndpoint string
	webdavAuthType string
	webdavUsername string
	webdavInsecure bool
	// S3 connection state. The signing material stays in the token store;
	// these are the non-secret fields the setup form prefills.
	s3Endpoint string
	s3Region   string
	s3Bucket   string
	s3Prefix   string
	s3Insecure bool
	// insecureConfirmedEndpoint is the exact endpoint the user explicitly
	// confirmed in this process. It is never persisted, so a later process
	// asks again; automatic sync never uses it.
	insecureConfirmedEndpoint string

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
		remotes:  map[string]Remote{ProviderGitHub: remote},
		provider: ProviderGitHub,
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
	if s.config.Provider == "" {
		s.config.Provider = s.provider
	}
	if remote, ok := s.remotes[s.config.Provider]; ok {
		s.remote = remote
	} else {
		// A profile naming a provider this build did not register is not
		// usable. Keep the default so the surface shows the truth.
		s.config.Provider = s.provider
		s.remote = s.remotes[s.provider]
	}
	s.status = StatusNeedsSetup
	s.problem = ProblemNotConfigured
	if s.config.Connected {
		s.status = StatusNeedsPassword
		s.problem = ProblemPasswordRequired
	}
	if s.config.PendingConflict != nil {
		// A conflict blocks every sync, automatic or manual, until it is
		// resolved. Loading it here is what makes resolution survive a
		// restart.
		s.status = StatusConflict
		s.problem = ProblemConflict
	}
	if s.config.AnchorVersion != AnchorVersion {
		// An anchor this build does not understand is ignored; the next
		// successful sync writes a fresh one.
		s.config.SyncBase = ""
		s.config.BaseSignature = ""
	}
	s.convergent = convergentInfoFor(s.config)
	if s.config.Convergent.SchemaVersion == 2 && !s.config.Convergent.Enabled {
		s.problem = ProblemConvergentPaused
	}
	provider := s.config.Provider
	s.mu.Unlock()
	if provider == ProviderWebDAV || provider == ProviderS3 {
		// Best effort: the form's non-secret fields survive a restart. A
		// missing or corrupt credential is simply the unconfigured form.
		if token, err := s.tokens.Load(ctx); err == nil {
			s.rememberWebDAV(token)
			s.rememberS3(token)
		}
	}
	s.changed()
	return nil
}

// rememberWebDAV caches the non-secret WebDAV form fields from a credential.
// It never copies the password or bearer token into the profile or snapshot.
func (s *Service) rememberWebDAV(token domainsync.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token.Provider != "" && token.Provider != ProviderWebDAV {
		s.webdavEndpoint = ""
		s.webdavAuthType = ""
		s.webdavUsername = ""
		s.webdavInsecure = false
		return
	}
	if token.WebDAV == nil || token.WebDAV.Endpoint == "" {
		s.webdavEndpoint = ""
		s.webdavAuthType = ""
		s.webdavUsername = ""
		s.webdavInsecure = false
		return
	}
	s.webdavEndpoint = token.WebDAV.Endpoint
	s.webdavAuthType = domainsync.NormalizeWebDAVAuthType(token.WebDAV.AuthType)
	s.webdavUsername = token.WebDAV.Username
	s.webdavInsecure = EndpointIsInsecure(token.WebDAV.Endpoint)
}

// rememberS3 caches the non-secret S3 form fields from a credential. It never
// copies the access key id, secret access key, or session token into the
// profile or snapshot.
func (s *Service) rememberS3(token domainsync.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token.Provider != "" && token.Provider != ProviderS3 {
		s.s3Endpoint = ""
		s.s3Region = ""
		s.s3Bucket = ""
		s.s3Prefix = ""
		s.s3Insecure = false
		return
	}
	if token.S3 == nil || token.S3.Endpoint == "" {
		s.s3Endpoint = ""
		s.s3Region = ""
		s.s3Bucket = ""
		s.s3Prefix = ""
		s.s3Insecure = false
		return
	}
	s.s3Endpoint = token.S3.Endpoint
	s.s3Region = token.S3.Region
	s.s3Bucket = token.S3.Bucket
	s.s3Prefix = token.S3.Prefix
	s.s3Insecure = EndpointIsInsecure(token.S3.Endpoint)
}

// EndpointIsInsecure reports whether a normalized endpoint would send its
// credentials without transport encryption. It is the application layer's
// copy of the rule; adapters perform the authoritative normalization.
func EndpointIsInsecure(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http")
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
		Connected:      s.config.Connected,
		Provider:       NormalizeProvider(s.config.Provider),
		Login:          s.config.Login,
		AutoSync:       s.config.AutoSync,
		Strategy:       domainsync.NormalizeStrategy(s.config.Strategy),
		Status:         s.status,
		Problem:        s.problem,
		DeviceCode:     s.device,
		LastSyncedAt:   s.config.LastSyncedAt,
		LastAction:     s.lastAction,
		HasPassword:    s.password != "",
		WebDAVEndpoint: s.webdavEndpoint,
		WebDAVAuthType: s.webdavAuthType,
		WebDAVUsername: s.webdavUsername,
		WebDAVInsecure: s.webdavInsecure,
		S3Endpoint:     s.s3Endpoint,
		S3Region:       s.s3Region,
		S3Bucket:       s.s3Bucket,
		S3Prefix:       s.s3Prefix,
		S3Insecure:     s.s3Insecure,
		Guard:          s.guard,
		Conflict:       conflictInfoFor(s.config),
		Backups:        append([]BackupInfo(nil), s.backupsList...),
		BackupsProblem: s.backupsProblem,
		Convergent:     s.convergent,
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

// SetProvider switches the Sync Profile's provider. The provider token and the
// merge anchor belong to the old account, so both are cleared and the profile
// must authorize again; local Vault data is untouched.
func (s *Service) SetProvider(ctx context.Context, provider string) error {
	provider = NormalizeProvider(provider)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.mu.Lock()
	remote, ok := s.remotes[provider]
	if !ok {
		s.mu.Unlock()
		return ErrNotConfigured
	}
	if NormalizeProvider(s.config.Provider) == provider && s.remote == remote {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if err := s.tokens.Clear(ctx); err != nil {
		return err
	}
	config := s.currentConfig()
	config.Provider = provider
	config.Connected = false
	config.Login = ""
	config.AccountID = ""
	clearAnchor(&config)
	s.mu.Lock()
	s.config = config
	s.remote = remote
	s.status = StatusNeedsSetup
	s.problem = ProblemNotConfigured
	s.device = DeviceCode{}
	s.webdavEndpoint = ""
	s.webdavAuthType = ""
	s.webdavUsername = ""
	s.webdavInsecure = false
	s.s3Endpoint = ""
	s.s3Region = ""
	s.s3Bucket = ""
	s.s3Prefix = ""
	s.s3Insecure = false
	s.insecureConfirmedEndpoint = ""
	s.convergent = ConvergentInfo{}
	s.mu.Unlock()
	if err := s.configs.Save(ctx, config); err != nil {
		return err
	}
	s.changed()
	return nil
}

// Connect authorizes the profile's provider. GitHub shows a device code; a
// browser provider opens the system browser and waits on its loopback
// redirect. onDevice is called with the device code before the first poll and
// is nil for browser providers.
func (s *Service) Connect(ctx context.Context, password string, onDevice func(DeviceCode)) error {
	if password == "" {
		// Reconnect after a rejected refresh does not ask for the master
		// password again: it is still held for this run once it was accepted.
		s.mu.Lock()
		password = s.password
		s.mu.Unlock()
	}
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

	token, grant, err := s.authorize(ctx, onDevice)
	if err != nil {
		s.fail(problemFor(err))
		return err
	}
	if saveErr := s.saveToken(ctx, token); saveErr != nil {
		s.fail(ProblemLocal)
		return saveErr
	}
	if saveErr := s.applyGrant(ctx, grant); saveErr != nil {
		s.fail(ProblemLocal)
		return saveErr
	}
	s.mu.Lock()
	s.device = DeviceCode{}
	s.status = StatusIdle
	s.problem = ProblemNone
	s.mu.Unlock()
	s.changed()
	return nil
}

// ConnectWebDAV connects the profile to a WebDAV collection with explicit
// user-entered settings. Prepare validates and normalizes the endpoint before
// anything else happens. A plain-HTTP endpoint returns ErrInsecureEndpoint
// without storing or sending the credential unless the caller confirms; that
// confirmation is explicit, per connect, and never persisted. On success the
// credential (including the username/password or bearer token) goes to the
// secure token store and only non-secret fields enter the profile.
func (s *Service) ConnectWebDAV(ctx context.Context, password string, settings WebDAVSettings, confirmInsecure bool) error {
	if password == "" {
		s.mu.Lock()
		password = s.password
		s.mu.Unlock()
	}
	if password == "" {
		return ErrPasswordRequired
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	remote := s.remotes[ProviderWebDAV]
	connector, ok := remote.(WebDAVConnector)
	if !ok {
		s.fail(ProblemNotConfigured)
		return ErrNotConfigured
	}
	normalized, insecure, err := connector.Prepare(settings)
	if err != nil {
		s.fail(problemFor(err))
		return err
	}
	if insecure && !confirmInsecure {
		// The gate runs before the token store is written and before any
		// request, so no secret can leave the process unconfirmed.
		s.fail(ProblemInsecureEndpoint)
		return ErrInsecureEndpoint
	}

	s.mu.Lock()
	s.password = password
	s.status = StatusAuthorizing
	s.problem = ProblemNone
	s.device = DeviceCode{}
	s.mu.Unlock()
	s.changed()

	token, grant, err := connector.Connect(ctx, normalized)
	if err != nil {
		s.fail(problemFor(err))
		return err
	}
	token.Provider = ProviderWebDAV
	if saveErr := s.tokens.Save(ctx, token); saveErr != nil {
		s.fail(ProblemLocal)
		return saveErr
	}

	s.mu.Lock()
	config := s.config
	if NormalizeProvider(config.Provider) != ProviderWebDAV {
		// A WebDAV profile never reuses another provider's baseline.
		clearAnchor(&config)
	}
	config.Provider = ProviderWebDAV
	s.config = config
	s.remote = remote
	s.webdavEndpoint = normalized.Endpoint
	s.webdavAuthType = domainsync.NormalizeWebDAVAuthType(normalized.AuthType)
	s.webdavUsername = normalized.Username
	s.webdavInsecure = insecure
	if insecure {
		s.insecureConfirmedEndpoint = normalized.Endpoint
	} else {
		s.insecureConfirmedEndpoint = ""
	}
	s.mu.Unlock()

	if saveErr := s.applyGrant(ctx, grant); saveErr != nil {
		s.fail(ProblemLocal)
		return saveErr
	}
	s.mu.Lock()
	s.status = StatusIdle
	s.problem = ProblemNone
	s.mu.Unlock()
	s.changed()
	return nil
}

// WebDAVSettings returns the non-secret form state for the active WebDAV
// profile. The password and bearer token are never part of this value.
func (s *Service) WebDAVSettings() WebDAVSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return WebDAVSettings{
		Endpoint: s.webdavEndpoint,
		AuthType: s.webdavAuthType,
		Username: s.webdavUsername,
	}
}

// ConnectS3 connects the profile to an S3-compatible bucket with explicit
// user-entered settings. Prepare validates and normalizes the settings before
// anything else happens. A plain-HTTP endpoint returns ErrInsecureEndpoint
// without storing or sending the signing material unless the caller confirms;
// that confirmation is explicit, per connect, and never persisted. On success
// the whole connection, including the access key id, secret access key, and
// session token, goes to the secure token store and only non-secret fields
// enter the profile.
func (s *Service) ConnectS3(ctx context.Context, password string, settings S3Settings, confirmInsecure bool) error {
	if password == "" {
		s.mu.Lock()
		password = s.password
		s.mu.Unlock()
	}
	if password == "" {
		return ErrPasswordRequired
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	remote := s.remotes[ProviderS3]
	connector, ok := remote.(S3Connector)
	if !ok {
		s.fail(ProblemNotConfigured)
		return ErrNotConfigured
	}
	normalized, insecure, err := connector.Prepare(settings)
	if err != nil {
		s.fail(problemFor(err))
		return err
	}
	if insecure && !confirmInsecure {
		// The gate runs before the token store is written and before any
		// request, so no signing material can leave the process unconfirmed.
		s.fail(ProblemInsecureEndpoint)
		return ErrInsecureEndpoint
	}

	s.mu.Lock()
	s.password = password
	s.status = StatusAuthorizing
	s.problem = ProblemNone
	s.device = DeviceCode{}
	s.mu.Unlock()
	s.changed()

	token, grant, err := connector.Connect(ctx, normalized)
	if err != nil {
		s.fail(problemFor(err))
		return err
	}
	token.Provider = ProviderS3
	if saveErr := s.tokens.Save(ctx, token); saveErr != nil {
		s.fail(ProblemLocal)
		return saveErr
	}

	s.mu.Lock()
	config := s.config
	if NormalizeProvider(config.Provider) != ProviderS3 {
		// An S3 profile never reuses another provider's baseline.
		clearAnchor(&config)
	}
	config.Provider = ProviderS3
	s.config = config
	s.remote = remote
	s.s3Endpoint = normalized.Endpoint
	s.s3Region = normalized.Region
	s.s3Bucket = normalized.Bucket
	s.s3Prefix = normalized.Prefix
	s.s3Insecure = insecure
	if insecure {
		s.insecureConfirmedEndpoint = normalized.Endpoint
	} else {
		s.insecureConfirmedEndpoint = ""
	}
	s.mu.Unlock()

	if saveErr := s.applyGrant(ctx, grant); saveErr != nil {
		s.fail(ProblemLocal)
		return saveErr
	}
	s.mu.Lock()
	s.status = StatusIdle
	s.problem = ProblemNone
	s.mu.Unlock()
	s.changed()
	return nil
}

// S3Settings returns the non-secret form state for the active S3 profile. The
// access key id, secret access key, and session token are never part of this
// value.
func (s *Service) S3Settings() S3Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return S3Settings{
		Endpoint: s.s3Endpoint,
		Region:   s.s3Region,
		Bucket:   s.s3Bucket,
		Prefix:   s.s3Prefix,
	}
}

// ConfirmInsecureEndpoint records the user's explicit confirmation for the
// stored plain-HTTP endpoint in this process. It is used by "Sync anyway"
// after the warning; a saved profile in a later process asks again, and
// automatic sync never consults it.
func (s *Service) ConfirmInsecureEndpoint(ctx context.Context) bool {
	token, err := s.tokens.Load(ctx)
	if err != nil {
		return false
	}
	endpoint := ""
	switch {
	case token.Provider == ProviderWebDAV && token.WebDAV != nil:
		endpoint = token.WebDAV.Endpoint
	case token.Provider == ProviderS3 && token.S3 != nil:
		endpoint = token.S3.Endpoint
	default:
		return false
	}
	if !EndpointIsInsecure(endpoint) {
		return false
	}
	s.mu.Lock()
	s.insecureConfirmedEndpoint = endpoint
	s.mu.Unlock()
	return true
}

// authorize runs whichever grant the active provider implements.
func (s *Service) authorize(ctx context.Context, onDevice func(DeviceCode)) (domainsync.Token, Grant, error) {
	remote := s.activeRemote()
	if browser, ok := remote.(BrowserAuthorizer); ok {
		if s.openBrowser == nil {
			return domainsync.Token{}, Grant{}, ErrBrowserUnavailable
		}
		return browser.Authorize(ctx, s.openBrowser)
	}
	device, ok := remote.(DeviceAuthorizer)
	if !ok {
		return domainsync.Token{}, Grant{}, ErrNotConfigured
	}
	code, err := device.RequestDeviceCode(ctx)
	if err != nil {
		return domainsync.Token{}, Grant{}, err
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
		token, err := device.PollDeviceToken(ctx, code)
		switch {
		case err == nil:
			grant, userErr := remote.User(ctx, token)
			if userErr != nil {
				return domainsync.Token{}, Grant{}, userErr
			}
			return token, grant, nil
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
		case errors.Is(err, ErrAuthorizationPending):
			// Keep the server-provided cadence.
		case errors.Is(err, ErrDeviceCodeExpired), errors.Is(err, ErrAuthorizationDenied):
			return domainsync.Token{}, Grant{}, err
		default:
			return domainsync.Token{}, Grant{}, err
		}
		if err := s.waiter.Wait(ctx, interval); err != nil {
			return domainsync.Token{}, Grant{}, err
		}
	}
}

// applyGrant connects the profile to the authorized account. A different
// account (Google id/email, GitHub id/login) rotates the merge anchor so the
// new account's cloud copy never merges against the previous account's
// baseline. The same account keeps it.
func (s *Service) applyGrant(ctx context.Context, grant Grant) error {
	s.mu.Lock()
	config := s.config
	previous := config.AccountID
	if previous == "" {
		previous = config.Login
	}
	next := grant.Identity()
	sameAccount := previous == next
	if !sameAccount {
		clearAnchor(&config)
	}
	config.Connected = true
	config.Login = grant.Login
	config.AccountID = next
	s.config = config
	s.mu.Unlock()
	if err := s.configs.Save(ctx, config); err != nil {
		return err
	}
	s.changed()
	return nil
}

// clearAnchorFields forgets the merge anchor and any conflict detail that only
// makes sense for the previous cloud copy.
func clearAnchorFields(config *Config) {
	config.AnchorVersion = 0
	config.SyncBase = ""
	config.BaseSignature = ""
	config.PendingConflict = nil
}

// clearAnchor forgets the baseline a merge would otherwise use. Retained cloud
// copies belong to the previous account's envelope key, so they are dropped
// too; local Vault backups stay untouched.
func clearAnchor(config *Config) {
	clearAnchorFields(config)
	config.RemoteBackups = nil
	config.LocalSignature = ""
	config.RemoteID = ""
	config.RemoteETag = ""
	config.RemoteUpdatedAt = 0
	config.LastSyncedAt = 0
	// A v2 provider baseline is trust in one remote identity. Replacing the
	// account, endpoint, bucket, or provider invalidates it together with the
	// legacy anchor. The explicit migration marker is dropped too, so the new
	// remote cannot inherit trust from the old one.
	config.Convergent = ConvergentConfig{}
}

// Disconnect forgets the provider token and the baseline. It leaves the local
// Vault untouched and keeps the selected provider.
func (s *Service) Disconnect(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if err := s.tokens.Clear(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	deviceID, deviceName, appVersion := s.config.DeviceID, s.config.DeviceName, s.config.AppVersion
	s.config = Config{
		Provider:   NormalizeProvider(s.config.Provider),
		DeviceID:   deviceID,
		DeviceName: deviceName,
		AppVersion: appVersion,
		Strategy:   domainsync.DefaultStrategy,
	}
	s.status = StatusNeedsSetup
	s.problem = ProblemNotConfigured
	s.device = DeviceCode{}
	s.guard = GuardFinding{}
	s.backupsList = nil
	s.backupsProblem = ProblemNone
	s.webdavEndpoint = ""
	s.webdavAuthType = ""
	s.webdavUsername = ""
	s.webdavInsecure = false
	s.s3Endpoint = ""
	s.s3Region = ""
	s.s3Bucket = ""
	s.s3Prefix = ""
	s.s3Insecure = false
	s.insecureConfirmedEndpoint = ""
	s.mu.Unlock()
	if err := s.configs.Save(ctx, s.currentConfig()); err != nil {
		return err
	}
	s.changed()
	return nil
}

func (s *Service) activeRemote() Remote {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remote
}

func (s *Service) providerValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return NormalizeProvider(s.config.Provider)
}

// saveToken persists the credential under the active provider's identity.
func (s *Service) saveToken(ctx context.Context, token domainsync.Token) error {
	token.Provider = s.providerValue()
	return s.tokens.Save(ctx, token)
}

// ensureFreshToken loads the stored credential, rejects one issued for a
// different provider, and refreshes an expired token once.
func (s *Service) ensureFreshToken(ctx context.Context) (domainsync.Token, error) {
	token, err := s.tokens.Load(ctx)
	if err != nil {
		return domainsync.Token{}, err
	}
	if token.Provider == "" {
		// A credential stored before providers existed belongs to whatever
		// profile holds it. Explicit provider tags still guard against a token
		// crossing providers after a switch.
		token.Provider = s.providerValue()
	}
	if token.Provider != s.providerValue() {
		return domainsync.Token{}, errNoToken
	}
	if !s.needsRefresh(token) {
		return token, nil
	}
	return s.refreshToken(ctx, token)
}

func (s *Service) needsRefresh(token domainsync.Token) bool {
	if token.ExpiresAt == 0 {
		return false
	}
	return s.now().UnixMilli() >= token.ExpiresAt-60_000
}

// refreshToken rotates the credential. The mutex makes concurrent callers
// single-flight: a waiter reloads the store and reuses the token another
// caller just persisted instead of refreshing again. A provider rejection
// (invalid_grant) starts a fresh browser authorization and resumes.
func (s *Service) refreshToken(ctx context.Context, current domainsync.Token) (domainsync.Token, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()

	stored, loadErr := s.tokens.Load(ctx)
	if loadErr == nil && stored.AccessToken != "" && stored.AccessToken != current.AccessToken &&
		!s.needsRefresh(stored) {
		return stored, nil
	}
	source := current
	if loadErr == nil && stored.AccessToken != "" {
		source = stored
	}
	refresher, ok := s.activeRemote().(TokenRefresher)
	if !ok {
		return domainsync.Token{}, ErrUnauthorized
	}
	if source.RefreshToken == "" {
		return domainsync.Token{}, ErrReauthorizationRequired
	}
	refreshed, err := refresher.Refresh(ctx, source)
	if errors.Is(err, ErrReauthorizationRequired) {
		return s.reauthorize(ctx, source)
	}
	if err != nil {
		return domainsync.Token{}, err
	}
	merged := refreshed
	if merged.RefreshToken == "" {
		// Google's refresh response usually omits the refresh token. Losing
		// it would make the stored credential unrefreshable.
		merged.RefreshToken = source.RefreshToken
	}
	if merged.TokenType == "" {
		merged.TokenType = source.TokenType
	}
	if merged.Scope == "" {
		merged.Scope = source.Scope
	}
	if saveErr := s.saveToken(ctx, merged); saveErr != nil {
		return domainsync.Token{}, saveErr
	}
	return merged, nil
}

// reauthorize starts a fresh browser flow after the provider rejected the
// refresh token. The caller retries its operation with the returned token.
func (s *Service) reauthorize(ctx context.Context, stale domainsync.Token) (domainsync.Token, error) {
	browser, ok := s.activeRemote().(BrowserAuthorizer)
	if !ok || s.openBrowser == nil {
		return stale, fmt.Errorf("%w: %w", ErrReauthorizationRequired, ErrBrowserUnavailable)
	}
	s.mu.Lock()
	s.status = StatusAuthorizing
	s.problem = ProblemNone
	s.mu.Unlock()
	s.changed()
	token, grant, err := browser.Authorize(ctx, s.openBrowser)
	if err != nil {
		return stale, fmt.Errorf("%w: %w", ErrReauthorizationRequired, err)
	}
	if saveErr := s.saveToken(ctx, token); saveErr != nil {
		return stale, saveErr
	}
	if saveErr := s.applyGrant(ctx, grant); saveErr != nil {
		return stale, saveErr
	}
	return token, nil
}

// readRemote retries a 401 once with a refreshed credential.
func (s *Service) readRemote(ctx context.Context, remote Remote, token domainsync.Token) (RemoteFile, bool, domainsync.Token, error) {
	file, exists, err := remote.Read(ctx, token)
	if !errors.Is(err, ErrUnauthorized) {
		return file, exists, token, err
	}
	refreshed, refreshErr := s.refreshToken(ctx, token)
	if refreshErr != nil {
		return RemoteFile{}, false, token, refreshErr
	}
	file, exists, err = remote.Read(ctx, refreshed)
	return file, exists, refreshed, err
}

// writeRemote retries a 401 once with a refreshed credential.
func (s *Service) writeRemote(ctx context.Context, remote Remote, token domainsync.Token, current RemoteFile, content []byte) (RemoteFile, domainsync.Token, error) {
	written, err := remote.Write(ctx, token, current, content)
	if !errors.Is(err, ErrUnauthorized) {
		return written, token, err
	}
	refreshed, refreshErr := s.refreshToken(ctx, token)
	if refreshErr != nil {
		return RemoteFile{}, token, refreshErr
	}
	written, err = remote.Write(ctx, refreshed, current, content)
	return written, refreshed, err
}

// Sync runs one manual or automatic sync. It is safe to call from the UI
// thread: the caller usually runs it in the UI's worker goroutine. A guard is
// never overridden by this entry point.
func (s *Service) Sync(ctx context.Context, trigger Trigger) (Result, error) {
	return s.sync(ctx, trigger, false)
}

// ForcePush reruns a manual sync with the destructive guards explicitly
// overridden. It is only reachable from the UI's confirmed Force push action;
// automatic syncs never pass force and the service refuses it for TriggerAuto.
// The protected cloud copy is still recorded in RemoteBackups before the write.
func (s *Service) ForcePush(ctx context.Context) (Result, error) {
	return s.sync(ctx, TriggerManual, true)
}

func (s *Service) sync(ctx context.Context, trigger Trigger, force bool) (Result, error) {
	if !s.syncMu.TryLock() {
		return Result{Trigger: trigger}, ErrAlreadyInProgress
	}
	defer s.syncMu.Unlock()
	if trigger != TriggerManual {
		// There is no automatic force path, by construction.
		force = false
	}
	return s.syncLocked(ctx, trigger, force)
}

func (s *Service) syncLocked(ctx context.Context, trigger Trigger, force bool) (Result, error) {
	s.mu.Lock()
	password := s.password
	configured := s.config.Connected
	pending := s.config.PendingConflict != nil
	s.mu.Unlock()
	if !configured {
		s.fail(ProblemNotConfigured)
		return Result{Trigger: trigger}, ErrNotConfigured
	}
	if pending {
		if force && trigger == TriggerManual {
			// Force push is the explicit, confirmed destructive choice: keep
			// the local Vault and overwrite the conflicting cloud copy, after a
			// backup and with the guards still evaluated.
			return s.resolveLocked(ctx, ResolutionLocal, true)
		}
		// An unresolved conflict blocks every other trigger, including
		// automatic sync. Overwriting either side before the user chooses would
		// be the silent data loss the conflict state exists to prevent.
		s.mu.Lock()
		s.status = StatusConflict
		s.problem = ProblemConflict
		s.mu.Unlock()
		s.changed()
		return Result{Trigger: trigger}, ErrConflict
	}
	if password == "" {
		s.mu.Lock()
		s.status = StatusNeedsPassword
		s.problem = ProblemPasswordRequired
		s.mu.Unlock()
		s.changed()
		return Result{Trigger: trigger}, ErrPasswordRequired
	}

	token, err := s.ensureFreshToken(ctx)
	if err != nil {
		if errors.Is(err, errNoToken) {
			s.fail(ProblemNotConfigured)
			return Result{Trigger: trigger}, ErrNotConfigured
		}
		s.fail(problemFor(err))
		return Result{Trigger: trigger}, err
	}
	insecureEndpoint := ""
	switch {
	case token.WebDAV != nil && EndpointIsInsecure(token.WebDAV.Endpoint):
		insecureEndpoint = token.WebDAV.Endpoint
	case token.S3 != nil && EndpointIsInsecure(token.S3.Endpoint):
		insecureEndpoint = token.S3.Endpoint
	}
	if insecureEndpoint != "" {
		// A plain-HTTP endpoint never syncs on its own: automatic sync would
		// send the credential with no person in the loop, and a manual sync
		// requires the warning to be confirmed in this process.
		if trigger == TriggerAuto {
			s.fail(ProblemInsecureEndpoint)
			return Result{Trigger: trigger}, ErrInsecureEndpoint
		}
		s.mu.Lock()
		confirmed := s.insecureConfirmedEndpoint == insecureEndpoint
		s.mu.Unlock()
		if !confirmed {
			s.fail(ProblemInsecureEndpoint)
			return Result{Trigger: trigger}, ErrInsecureEndpoint
		}
	}

	s.mu.Lock()
	s.status = StatusSyncing
	s.syncing = true
	s.problem = ProblemNone
	s.guard = GuardFinding{}
	s.mu.Unlock()
	s.changed()

	local, snapErr := s.vault.Snapshot(ctx)
	if snapErr != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, snapErr
	}
	localSignature, err := local.Signature()
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	config := s.currentConfig()
	base := s.loadBase(ctx)

	if config.Convergent.SchemaVersion != 0 {
		// A v2 marker must never fall through to the legacy writer. An
		// unknown marker is a partial/corrupt migration and fails closed; a
		// paused profile waits for the user to resume.
		if config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
			s.finishSync(ProblemConvergentCorrupt)
			return Result{Trigger: trigger}, ErrConvergentCorrupt
		}
		if !config.Convergent.Enabled {
			s.mu.Lock()
			s.problem = ProblemConvergentPaused
			s.mu.Unlock()
			s.finishSync(ProblemConvergentPaused)
			return Result{Trigger: trigger}, ErrConvergentPaused
		}
		return s.syncConvergent(ctx, trigger, force, token, local, config)
	}

	remote := s.activeRemote()
	remoteFile, exists, token, readErr := s.readRemote(ctx, remote, token)
	if readErr != nil {
		s.finishSync(problemFor(readErr))
		return Result{Trigger: trigger}, readErr
	}

	var remotePayload Payload
	remoteUpdatedAt := int64(0)
	if exists {
		var problem Problem
		remotePayload, remoteUpdatedAt, problem, err = s.decodeRemote(remoteFile)
		if err != nil {
			s.finishSync(problem)
			return Result{Trigger: trigger}, err
		}
	}

	if !local.HasData() {
		return s.syncEmptyLocal(ctx, trigger, force, token, local, localSignature, config, base,
			remoteFile, exists, remotePayload)
	}

	if !exists {
		outgoing := s.prepareOutgoing(local, base, nil, config.DeviceID)
		if blocked, result, guardErr := s.applyGuard(outgoing, base, nil, force, trigger); blocked {
			return result, guardErr
		}
		_, err := s.upload(ctx, trigger, force, token, outgoing, local, localSignature,
			base, nil, RemoteFile{}, config, "")
		if err != nil {
			return Result{Trigger: trigger}, err
		}
		s.completeSync(domainsync.ActionUpload)
		return Result{Trigger: trigger, Action: domainsync.ActionUpload, Uploaded: true}, nil
	}

	localChanged := config.LocalSignature == "" || config.LocalSignature != localSignature
	remoteChanged := config.RemoteUpdatedAt == 0 ||
		config.RemoteUpdatedAt != remoteUpdatedAt ||
		(config.RemoteETag != "" && config.RemoteETag != remoteFile.ETag)

	action := domainsync.Decide(localChanged, remoteChanged, config.Strategy)
	if !remotePayload.HasData() {
		// Never let an empty cloud copy replace a populated local Vault: a
		// first-ever upload is the only correct action.
		action = domainsync.ActionUpload
	}

	switch action {
	case domainsync.ActionDownload:
		return s.download(ctx, trigger, remoteFile, remotePayload, remoteUpdatedAt)
	case domainsync.ActionMerge:
		return s.mergeAndUpload(ctx, trigger, force, token, local, localSignature, config, base,
			remoteFile, remotePayload)
	case domainsync.ActionUpload:
		outgoing := s.prepareOutgoing(local, base, &remotePayload, config.DeviceID)
		if blocked, result, guardErr := s.applyGuard(outgoing, base, &remotePayload, force, trigger); blocked {
			return result, guardErr
		}
		_, err := s.upload(ctx, trigger, force, token, outgoing, local, localSignature,
			base, &remotePayload, remoteFile, config, "")
		if err != nil {
			return Result{Trigger: trigger}, err
		}
		s.completeSync(domainsync.ActionUpload)
		return Result{Trigger: trigger, Action: domainsync.ActionUpload, Uploaded: true}, nil
	default:
		var basePtr *Payload
		if base != nil {
			basePtr = base
		} else {
			basePtr = &local
		}
		if err := s.recordSync(ctx, remoteFile, localSignature, remoteUpdatedAt, basePtr); err != nil {
			s.finishSync(ProblemLocal)
			return Result{Trigger: trigger}, err
		}
		s.completeSync(domainsync.ActionNone)
		return Result{Trigger: trigger, Action: domainsync.ActionNone}, nil
	}
}

// syncEmptyLocal handles the tombstone case: the local Vault has no synced
// records. With no populated cloud copy this is the existing empty-Vault
// refusal. With one, an unconfirmed sync is blocked as a tombstone guard; an
// explicit manual ForcePush uploads the empty payload with tombstones for the
// old entities.
func (s *Service) syncEmptyLocal(
	ctx context.Context,
	trigger Trigger,
	force bool,
	token domainsync.Token,
	local Payload,
	localSignature string,
	config Config,
	base *Payload,
	remoteFile RemoteFile,
	exists bool,
	remotePayload Payload,
) (Result, error) {
	var reference *Payload
	if exists && remotePayload.HasData() {
		reference = &remotePayload
	}
	if reference == nil && (base == nil || !base.HasData()) {
		// There is no populated copy anywhere to protect: this is the
		// original empty-Vault refusal, not a tombstone overwrite.
		s.finishSync(ProblemEmptyVault)
		return Result{Trigger: trigger}, ErrEmptyVault
	}
	finding := DetectDestructiveSync(local, base, reference)
	if blocked, result, guardErr := s.applyGuardFinding(finding, force, trigger); blocked {
		return result, guardErr
	}
	outgoing := s.prepareOutgoing(local, base, reference, config.DeviceID)
	_, err := s.upload(ctx, trigger, force, token, outgoing, local, localSignature,
		base, reference, remoteFile, config, "")
	if err != nil {
		return Result{Trigger: trigger}, err
	}
	s.completeSync(domainsync.ActionUpload)
	return Result{Trigger: trigger, Action: domainsync.ActionUpload, Uploaded: true}, nil
}

// mergeAndUpload runs the selected three-way merge, applies the result locally
// (so both sides converge on exactly what is uploaded), takes a protective
// backup first, and then publishes it.
func (s *Service) mergeAndUpload(
	ctx context.Context,
	trigger Trigger,
	force bool,
	token domainsync.Token,
	local Payload,
	localSignature string,
	config Config,
	base *Payload,
	remoteFile RemoteFile,
	remotePayload Payload,
) (Result, error) {
	merged, err := MergePayloads(base, local, remotePayload, MergeOptions{
		PreserveDeletions: domainsync.IsPreserveDeletionsStrategy(config.Strategy),
	})
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	outgoing := s.prepareOutgoing(merged.Payload, base, &remotePayload, config.DeviceID)
	if blocked, result, guardErr := s.applyGuard(outgoing, base, &remotePayload, force, trigger); blocked {
		return result, guardErr
	}
	// Both sides are overwritten by a merge: retain the cloud copy that is
	// being replaced and take a local restore point before applying.
	if err := s.retainRemoteBackup(ctx, remoteFile, "merge"); err != nil {
		s.finishSync(ProblemBackup)
		return Result{Trigger: trigger}, err
	}
	if err := s.ensureProtectiveBackup(ctx); err != nil {
		s.finishSync(ProblemBackup)
		return Result{Trigger: trigger}, err
	}
	if err := s.vault.Apply(ctx, outgoing); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	s.notifySnapshotApplied()
	applied, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	appliedSignature, err := applied.Signature()
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	if _, err := s.upload(ctx, trigger, force, token, outgoing, applied, appliedSignature,
		base, &remotePayload, remoteFile, config, "merge"); err != nil {
		return Result{Trigger: trigger}, err
	}
	s.completeSync(domainsync.ActionMerge)
	return Result{
		Trigger:    trigger,
		Action:     domainsync.ActionMerge,
		Uploaded:   true,
		Downloaded: true,
	}, nil
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
// the new baseline. Both the normal path and the guard-restore path use it so
// the next sync converges to a no-op.
func (s *Service) download(
	ctx context.Context,
	trigger Trigger,
	remoteFile RemoteFile,
	remotePayload Payload,
	remoteUpdatedAt int64,
) (Result, error) {
	if err := s.ensureProtectiveBackup(ctx); err != nil {
		s.finishSync(ProblemBackup)
		return Result{Trigger: trigger}, err
	}
	if err := s.vault.Apply(ctx, remotePayload); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	// The Vault now holds the cloud copy. Notify Vault-backed surfaces before
	// the baseline bookkeeping: even if saving the baseline fails, the local
	// data has already changed and the UI must show it.
	s.notifySnapshotApplied()
	applied, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	appliedSignature, err := applied.Signature()
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	if err := s.recordSync(ctx, remoteFile, appliedSignature, remoteUpdatedAt, &applied); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: trigger}, err
	}
	s.completeSync(domainsync.ActionDownload)
	return Result{Trigger: trigger, Action: domainsync.ActionDownload, Downloaded: true}, nil
}

// upload encrypts and writes the payload. Every write is preceded by the
// destructive-sync guard. A conditional-write loss is surfaced as a durable
// conflict instead of silently overwriting the concurrent device.
//
// local is the payload that must become the applied baseline's local copy (for
// a merge, the already-applied merged snapshot), localSignature its signature,
// base the outgoing payload stored as the merge anchor, and remote the cloud
// reference used by the guards.
func (s *Service) upload(
	ctx context.Context,
	trigger Trigger,
	force bool,
	token domainsync.Token,
	outgoing Payload,
	local Payload,
	localSignature string,
	base *Payload,
	remotePayload *Payload,
	current RemoteFile,
	config Config,
	backupReason string,
) (RemoteFile, error) {
	remote := s.activeRemote()
	if blocked, _, guardErr := s.applyGuard(outgoing, base, remotePayload, force, trigger); blocked {
		return RemoteFile{}, guardErr
	}
	s.mu.Lock()
	guardOverridden := force && s.guard.Suspicious()
	s.mu.Unlock()
	if guardOverridden {
		// An explicitly forced destructive write always retains the copy it
		// replaces, independent of the caller's reason.
		backupReason = "guard-force"
	}
	if backupReason != "" {
		if err := s.retainRemoteBackup(ctx, current, backupReason); err != nil {
			s.finishSync(ProblemBackup)
			return RemoteFile{}, err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		envelope, err := domainsync.EncryptPayload(outgoing, s.passwordValue(), domainsync.EncryptOptions{
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
			return RemoteFile{}, err
		}
		content, err := domainsync.MarshalFile(envelope)
		if err != nil {
			s.finishSync(ProblemLocal)
			return RemoteFile{}, err
		}
		written, nextToken, writeErr := s.writeRemote(ctx, remote, token, current, content)
		token = nextToken
		if errors.Is(writeErr, ErrConflict) && attempt == 0 {
			// Another device wrote between our read and our write. Re-read it
			// once. An empty cloud copy is safe to fill; otherwise surface a
			// durable conflict and let the user choose, exactly as the
			// original conflict state machine does.
			fresh, exists, readToken, readErr := s.readRemote(ctx, remote, token)
			token = readToken
			if readErr != nil {
				s.finishSync(problemFor(readErr))
				return RemoteFile{}, readErr
			}
			if !exists {
				current = RemoteFile{}
				continue
			}
			freshPayload, freshUpdatedAt, problem, decodeErr := s.decodeRemote(fresh)
			if decodeErr != nil {
				s.finishSync(problem)
				return RemoteFile{}, decodeErr
			}
			if !freshPayload.HasData() {
				current = fresh
				continue
			}
			return RemoteFile{}, s.surfaceConflict(ctx, trigger, config, local, localSignature,
				fresh, freshPayload, freshUpdatedAt)
		}
		if writeErr != nil {
			s.finishSync(problemFor(writeErr))
			return RemoteFile{}, writeErr
		}
		if written.UpdatedAt == 0 {
			written.UpdatedAt = envelope.Meta.UpdatedAt
		}
		written.Content = content
		// An upload always publishes exactly outgoing, so that is the new
		// merge anchor. The caller's base stays the guard reference only.
		if err := s.recordSync(ctx, written, localSignature, written.UpdatedAt, &outgoing); err != nil {
			s.finishSync(ProblemLocal)
			return RemoteFile{}, err
		}
		return written, nil
	}
	s.finishSync(ProblemConflict)
	return RemoteFile{}, ErrConflict
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

// recordSync persists the new baseline and seals the payload that both sides
// now agree on as the merge anchor. The caller reports a save failure as
// ProblemLocal, so a sync is never presented as successful when the next run
// would not know what it synced.
func (s *Service) recordSync(ctx context.Context, remote RemoteFile, localSignature string, remoteUpdatedAt int64, base *Payload) error {
	var encoded, signature string
	if base != nil {
		var err error
		encoded, signature, err = s.encodeAnchor(ctx, *base)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	config := s.config
	config.LocalSignature = localSignature
	config.RemoteID = remote.ID
	config.RemoteETag = remote.ETag
	config.RemoteUpdatedAt = remoteUpdatedAt
	config.LastSyncedAt = s.now().UnixMilli()
	config.PendingConflict = nil
	if base != nil {
		config.AnchorVersion = AnchorVersion
		config.SyncBase = encoded
		config.BaseSignature = signature
	} else {
		clearAnchorFields(&config)
	}
	s.config = config
	s.guard = GuardFinding{}
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
	// A saved plain-HTTP endpoint never auto-syncs. The service refuses the
	// automatic trigger as well; skipping the timer keeps the preference from
	// producing a failure on every local edit.
	if s.stopped || !s.config.AutoSync || s.webdavInsecure || s.s3Insecure {
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
	case errors.Is(err, ErrReauthorizationRequired):
		return ProblemReauthorization
	case errors.Is(err, ErrCredentialsRejected):
		return ProblemCredentials
	case errors.Is(err, ErrForbidden):
		return ProblemForbidden
	case errors.Is(err, ErrTLS):
		return ProblemTLS
	case errors.Is(err, ErrInvalidEndpoint):
		return ProblemInvalidEndpoint
	case errors.Is(err, ErrInsecureEndpoint):
		return ProblemInsecureEndpoint
	case errors.Is(err, ErrRedirectRefused):
		return ProblemRedirect
	case errors.Is(err, ErrWrongRegion):
		return ProblemWrongRegion
	case errors.Is(err, ErrUnauthorized), errors.Is(err, errNoToken):
		return ProblemUnauthorized
	case errors.Is(err, ErrRateLimited):
		return ProblemRateLimited
	case errors.Is(err, ErrNetwork):
		return ProblemNetwork
	case errors.Is(err, ErrAuthorizationDenied), errors.Is(err, ErrDeviceCodeExpired),
		errors.Is(err, ErrAuthorizationPending), errors.Is(err, ErrSlowDown),
		errors.Is(err, ErrAuthorizationState), errors.Is(err, ErrAuthorizationTimeout),
		errors.Is(err, ErrBrowserUnavailable):
		return ProblemAuthorization
	case errors.Is(err, ErrConflict):
		return ProblemConflict
	case errors.Is(err, ErrShrinkGuard):
		return ProblemShrinkGuard
	case errors.Is(err, ErrTombstoneGuard):
		return ProblemTombstoneGuard
	case errors.Is(err, ErrBackupUnavailable):
		return ProblemBackup
	case errors.Is(err, ErrUnsupportedVersion):
		return ProblemUnsupported
	case errors.Is(err, ErrConvergentMigration):
		return ProblemConvergentMigration
	case errors.Is(err, ErrConvergentDivergence):
		return ProblemConvergentDivergence
	case errors.Is(err, ErrConvergentCorrupt):
		return ProblemConvergentCorrupt
	case errors.Is(err, ErrConvergentPaused):
		return ProblemConvergentPaused
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
