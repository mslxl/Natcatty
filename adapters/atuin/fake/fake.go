// Package fake is an in-process Atuin v18 server used by tests and acceptance
// runs for the account and sync services. It implements the historysync
// Remote port without network traffic, record encryption, or the Atuin binary,
// so account validation and sync status can be exercised before the real HTTP
// adapter exists. Download and Upload refuse on purpose: no record sync runs
// in this ticket.
package fake

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

const (
	defaultUserID      = "fake-atuin-user"
	defaultMintedToken = "fake-atuin-session-token"
	defaultPageSize    = 100
)

// ErrRecordSyncNotImplemented reports a Download or Upload call. Account
// validation and status must never sync records; tests assert the counter
// stays at zero.
var ErrRecordSyncNotImplemented = errors.New("fake atuin: record sync is not implemented")

// CompatibleCapabilities returns the capabilities of the pinned Atuin v18
// server. Callers mutate a copy to model an unknown version or a missing
// capability.
func CompatibleCapabilities() historysync.Capabilities {
	return historysync.Capabilities{
		ServerVersion: historysync.PinnedAtuinVersion,
		Major:         historysync.PinnedAtuinMajor,
		Minor:         24,
		Patch:         5,
		PageSize:      defaultPageSize,
		Values: map[string]int{
			historysync.CapabilityServer:   1,
			historysync.CapabilityPageSize: 1,
		},
	}
}

// Options is the fake server's behavior. Empty expected credentials accept any
// non-empty credential. An empty Account is derived from the login request. A
// completely empty Capabilities value means CompatibleCapabilities.
type Options struct {
	// Account is the identity Authenticate reports. ServerURL and Username
	// default to the login config when empty; UserID defaults to a stable fake
	// id.
	Account historysync.Account
	// ExpectedToken is the only pasted or Bearer token accepted. Empty accepts
	// any non-empty token.
	ExpectedToken string
	// ExpectedUsername and ExpectedPassword are the only password-flow
	// credentials accepted. Empty accepts any non-empty value.
	ExpectedUsername string
	ExpectedPassword string
	// MintedToken is returned by a successful password login. Empty uses a
	// stable fake token.
	MintedToken string
	// Capabilities is advertised to the account service. A zero value uses the
	// pinned compatible set.
	Capabilities historysync.Capabilities
	// Statuses is the record status reported to the account service.
	Statuses []historysync.SeriesStatus
	// Validation is the result of ValidateMasterKey. Empty derives it from
	// Statuses: verified when history exists, unverified otherwise.
	Validation historysync.KeyValidation

	// The following errors are injected at one call each.
	OpenErr         error
	AuthErr         error
	CapabilitiesErr error
	RecordStatusErr error
	ValidationErr   error
}

// Calls counts Remote method calls. Account operations use Authenticate,
// Capabilities, RecordStatus, and ValidateMasterKey; Download and Upload must
// remain zero until the sync engine in a later ticket.
type Calls struct {
	Authenticate      int
	Capabilities      int
	RecordStatus      int
	Download          int
	Upload            int
	ValidateMasterKey int
	Close             int
}

// Factory creates a scoped Remote per operation, matching the real adapter's
// contract. It is safe for concurrent use.
type Factory struct {
	options Options

	mu    sync.Mutex
	opens int
	last  *Remote
}

// NewFactory returns a factory. A zero Capabilities value is filled with
// CompatibleCapabilities so the default fake accepts the pinned v18 surface.
func NewFactory(options Options) *Factory {
	if options.Capabilities.ServerVersion == "" && options.Capabilities.Major == 0 {
		options.Capabilities = CompatibleCapabilities()
	}
	if options.Statuses != nil {
		options.Statuses = slices.Clone(options.Statuses)
	}
	return &Factory{options: options}
}

// Open creates one scoped fake Remote.
func (f *Factory) Open(_ context.Context, config historysync.Config) (historysync.Remote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.options.OpenErr != nil {
		return nil, f.options.OpenErr
	}
	remote := &Remote{options: f.options, config: config}
	f.last = remote
	return remote, nil
}

// Opens reports how many scoped remotes the factory created.
func (f *Factory) Opens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

// Last returns the most recently opened Remote, or nil. Tests use it to read
// call counters after an account operation closes the remote.
func (f *Factory) Last() *Remote {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// Remote is one operation-scoped fake server connection. It stores the login
// config only until Close wipes the master key.
type Remote struct {
	options Options
	config  historysync.Config

	mu     sync.Mutex
	calls  Calls
	closed bool
}

var _ historysync.Remote = (*Remote)(nil)
var _ historysync.Factory = (*Factory)(nil)

// Authenticate validates the token or username/password flow and returns the
// configured account. A password login mints a token.
func (r *Remote) Authenticate(context.Context) (historysync.AuthResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.Authenticate++
	if r.options.AuthErr != nil {
		return historysync.AuthResult{}, r.options.AuthErr
	}
	token := r.config.Token
	passwordFlow := token == "" && r.config.Password != ""
	if token == "" && !passwordFlow {
		return historysync.AuthResult{}, historysync.ErrAuthFailed
	}
	if r.options.ExpectedToken != "" && token != r.options.ExpectedToken {
		return historysync.AuthResult{}, historysync.ErrAuthFailed
	}
	if passwordFlow {
		if r.options.ExpectedUsername != "" && r.config.Username != r.options.ExpectedUsername {
			return historysync.AuthResult{}, historysync.ErrAuthFailed
		}
		if r.options.ExpectedPassword != "" && r.config.Password != r.options.ExpectedPassword {
			return historysync.AuthResult{}, historysync.ErrAuthFailed
		}
		token = r.options.MintedToken
		if token == "" {
			token = defaultMintedToken
		}
	}

	account := r.options.Account
	if account.ServerURL == "" {
		account.ServerURL = r.config.BaseURL
	}
	if account.Username == "" {
		account.Username = r.config.Username
	}
	if account.Username == "" {
		account.Username = "fake-atuin-user"
	}
	if account.UserID == "" {
		account.UserID = defaultUserID
	}
	return historysync.AuthResult{Token: token, Account: account}, nil
}

// Capabilities returns the configured capability document.
func (r *Remote) Capabilities(context.Context) (historysync.Capabilities, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.Capabilities++
	if r.options.CapabilitiesErr != nil {
		return historysync.Capabilities{}, r.options.CapabilitiesErr
	}
	capabilities := r.options.Capabilities
	capabilities.Values = maps.Clone(capabilities.Values)
	capabilities.Enforced = slices.Clone(capabilities.Enforced)
	return capabilities, nil
}

// RecordStatus returns the configured series statuses. It never downloads a
// record.
func (r *Remote) RecordStatus(context.Context) ([]historysync.SeriesStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.RecordStatus++
	if r.options.RecordStatusErr != nil {
		return nil, r.options.RecordStatusErr
	}
	return slices.Clone(r.options.Statuses), nil
}

// Download refuses: the account service must not sync records.
func (r *Remote) Download(context.Context, historysync.Series, int64, int) (historysync.Page, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.Download++
	return historysync.Page{}, ErrRecordSyncNotImplemented
}

// Upload refuses: the account service must not sync records.
func (r *Remote) Upload(context.Context, []historysync.SyncRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.Upload++
	return ErrRecordSyncNotImplemented
}

// ValidateMasterKey reports the configured validation. With no explicit
// validation, an account with history is verified and an empty account is
// unverified. It refuses a missing or wrong-length operation key so tests
// cannot accidentally prove validation with no key material. A real adapter
// must decrypt a record; this fake has no ciphertext to decrypt.
func (r *Remote) ValidateMasterKey(context.Context) (historysync.KeyValidation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.ValidateMasterKey++
	if r.options.ValidationErr != nil {
		return "", r.options.ValidationErr
	}
	if len(r.config.MasterKey) != domainhistory.MasterKeyBytes {
		return "", historysync.ErrKeyMismatch
	}
	if r.options.Validation != "" {
		return r.options.Validation, nil
	}
	if historysync.HasHistory(r.options.Statuses) {
		return historysync.KeyValidationVerified, nil
	}
	return historysync.KeyValidationUnverified, nil
}

// Close wipes the master key and marks the scoped connection closed.
func (r *Remote) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.Close++
	r.closed = true
	domainhistory.WipeMasterKey(r.config.MasterKey)
	r.config.Token = ""
	r.config.Password = ""
	return nil
}

// Calls returns a snapshot of the call counters.
func (r *Remote) Calls() Calls {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Closed reports whether Close ran.
func (r *Remote) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// HistoryStatus returns a remote-writer history series with maxIndex. It is a
// convenience for account tests that need a non-empty account.
func HistoryStatus(maxIndex int64) historysync.SeriesStatus {
	return historysync.SeriesStatus{
		Series: historysync.Series{
			SeriesHost: "fake-remote-writer",
			Tag:        domainhistory.RecordTagHistory,
		},
		MaxIndex: maxIndex,
	}
}

// AuthMode is the normalized mode the fake validated; it is exposed for
// acceptance notes without exposing the token.
func (r *Remote) AuthMode() atuinmodel.AuthMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return atuinmodel.NormalizeAuthMode(r.config.AuthMode)
}
