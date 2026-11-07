// Package fake is an in-process Atuin v18 server used by tests and acceptance
// runs for the account and sync services. It lives in the application context
// because it implements the historysync Remote port, and the architecture
// rules keep application tests independent of adapter packages. It has no
// network traffic, record encryption, or Atuin binary, so account validation,
// sync status, paging, upload, and retry are exercised against one
// deterministic server.
package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
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
	// Statuses is the record status reported to the account service. Records
	// appended by Upload also contribute a higher max index for their series.
	Statuses []historysync.SeriesStatus
	// Validation is the result of ValidateMasterKey. Empty derives it from
	// Statuses: verified when history exists, unverified otherwise.
	Validation historysync.KeyValidation

	// Records is the fake server's initial record log. Upload appends to it and
	// Download pages it.
	Records []historysync.SyncRecord
	// StatusIgnoresRecords makes RecordStatus report only Statuses, hiding the
	// records Upload would conflict with. It models a status response that
	// lags a commit whose acknowledgement was lost.
	StatusIgnoresRecords bool

	// DownloadErrSequence and UploadErrSequence inject one error per call; an
	// explicit nil entry is a successful call. DownloadErr and UploadErr fail
	// every call.
	DownloadErr         error
	UploadErr           error
	DownloadErrSequence []error
	UploadErrSequence   []error

	// DownloadHook and UploadHook run before the call's injected error and
	// after its counter. Tests use them to block a remote call without sleeps
	// or to observe the exact paging and batching.
	DownloadHook func(series historysync.Series, start int64, count int)
	UploadHook   func(records []historysync.SyncRecord)

	// The following errors are injected at one call each for account
	// operations.
	OpenErr         error
	AuthErr         error
	CapabilitiesErr error
	RecordStatusErr error
	ValidationErr   error
}

// Calls counts Remote method calls.
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
// contract, while retaining the fake server's records and call log across
// operations. It is safe for concurrent use.
type Factory struct {
	options Options

	mu    sync.Mutex
	opens int
	last  *Remote

	records      map[string][]historysync.SyncRecord
	events       []string
	downloadErrs []error
	uploadErrs   []error
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
	factory := &Factory{
		options:      options,
		records:      make(map[string][]historysync.SyncRecord),
		downloadErrs: slices.Clone(options.DownloadErrSequence),
		uploadErrs:   slices.Clone(options.UploadErrSequence),
	}
	for _, record := range options.Records {
		key := seriesKey(record.SeriesHost, record.Tag)
		factory.records[key] = append(factory.records[key], cloneRecord(record))
	}
	for key := range factory.records {
		sortRecords(factory.records[key])
	}
	return factory
}

// Open creates one scoped fake Remote.
func (f *Factory) Open(_ context.Context, config historysync.Config) (historysync.Remote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.options.OpenErr != nil {
		return nil, f.options.OpenErr
	}
	remote := &Remote{factory: f, options: f.options, config: config}
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

// ServerRecords returns the fake server's log in series and index order.
func (f *Factory) ServerRecords() []historysync.SyncRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var records []historysync.SyncRecord
	keys := make([]string, 0, len(f.records))
	for key := range f.records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, record := range f.records[key] {
			records = append(records, cloneRecord(record))
		}
	}
	return records
}

// Events returns the remote call order. Account calls are not recorded;
// Download and Upload entries prove download-before-upload and batch order.
func (f *Factory) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// AppendRecord adds one record to the fake server, as another device would
// between two syncs.
func (f *Factory) AppendRecord(record historysync.SyncRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := seriesKey(record.SeriesHost, record.Tag)
	f.records[key] = append(f.records[key], cloneRecord(record))
	sortRecords(f.records[key])
}

// Remote is one operation-scoped fake server connection. It stores the login
// config only until Close wipes the master key.
type Remote struct {
	factory *Factory
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

// RecordStatus returns the configured series statuses plus the max index of
// every series the fake server holds.
func (r *Remote) RecordStatus(context.Context) ([]historysync.SeriesStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls.RecordStatus++
	if r.options.RecordStatusErr != nil {
		return nil, r.options.RecordStatusErr
	}
	return r.factory.statuses(), nil
}

// Download pages one series. It never returns a record before start and
// reports NextIndex after the last record.
func (r *Remote) Download(_ context.Context, series historysync.Series, start int64, count int) (historysync.Page, error) {
	r.mu.Lock()
	r.calls.Download++
	r.mu.Unlock()

	if r.options.DownloadHook != nil {
		r.options.DownloadHook(series, start, count)
	}
	if err := r.factory.nextDownloadError(); err != nil {
		return historysync.Page{}, err
	}
	if r.options.DownloadErr != nil {
		return historysync.Page{}, r.options.DownloadErr
	}
	r.factory.mu.Lock()
	all := r.factory.records[seriesKey(series.SeriesHost, series.Tag)]
	var page []historysync.SyncRecord
	for _, record := range all {
		if record.Index < start {
			continue
		}
		if count > 0 && len(page) >= count {
			break
		}
		page = append(page, cloneRecord(record))
	}
	r.factory.mu.Unlock()
	r.factory.recordEvent(fmt.Sprintf("download:%s:%d", series.SeriesHost, start))
	next := start
	if len(page) > 0 {
		next = page[len(page)-1].Index + 1
	}
	return historysync.Page{Records: page, NextIndex: next}, nil
}

// Upload stores one ordered batch. A series index the server already holds is
// reported as a duplicate conflict with the server's metadata; the real
// adapter maps Atuin's 409 response to the same error.
func (r *Remote) Upload(_ context.Context, records []historysync.SyncRecord) error {
	r.mu.Lock()
	r.calls.Upload++
	r.mu.Unlock()

	if r.options.UploadHook != nil {
		r.options.UploadHook(records)
	}
	if err := r.factory.nextUploadError(); err != nil {
		return err
	}
	if r.options.UploadErr != nil {
		return r.options.UploadErr
	}
	r.factory.mu.Lock()
	defer r.factory.mu.Unlock()
	for _, record := range records {
		if !record.Valid() {
			return historysync.ErrRemote
		}
		key := seriesKey(record.SeriesHost, record.Tag)
		for _, existing := range r.factory.records[key] {
			if existing.Index == record.Index {
				return &historysync.DuplicateRecordError{Existing: cloneRecord(existing)}
			}
		}
	}
	for _, record := range records {
		key := seriesKey(record.SeriesHost, record.Tag)
		r.factory.records[key] = append(r.factory.records[key], cloneRecord(record))
	}
	if len(records) > 0 {
		r.factory.events = append(r.factory.events, fmt.Sprintf(
			"upload:%s:%d-%d", records[0].SeriesHost, records[0].Index, records[len(records)-1].Index))
	}
	return nil
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
	if historysync.HasHistory(r.factory.statuses()) {
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

// AuthMode is the normalized mode the fake validated; it is exposed for
// acceptance notes without exposing the token.
func (r *Remote) AuthMode() atuinmodel.AuthMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return atuinmodel.NormalizeAuthMode(r.config.AuthMode)
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

func (f *Factory) statuses() []historysync.SeriesStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	merged := make(map[string]historysync.SeriesStatus)
	for _, status := range f.options.Statuses {
		merged[seriesKey(status.SeriesHost, status.Tag)] = status
	}
	if f.options.StatusIgnoresRecords {
		return sortedStatuses(merged)
	}
	for key, records := range f.records {
		if len(records) == 0 {
			continue
		}
		maxIndex := records[len(records)-1].Index
		if current, ok := merged[key]; !ok || current.MaxIndex < maxIndex {
			merged[key] = historysync.SeriesStatus{
				Series: historysync.Series{
					SeriesHost: records[len(records)-1].SeriesHost,
					Tag:        records[len(records)-1].Tag,
				},
				MaxIndex: maxIndex,
			}
		}
	}
	return sortedStatuses(merged)
}

func sortedStatuses(merged map[string]historysync.SeriesStatus) []historysync.SeriesStatus {
	statuses := make([]historysync.SeriesStatus, 0, len(merged))
	for _, status := range merged {
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].SeriesHost == statuses[j].SeriesHost {
			return statuses[i].Tag < statuses[j].Tag
		}
		return statuses[i].SeriesHost < statuses[j].SeriesHost
	})
	return statuses
}

func (f *Factory) nextDownloadError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.downloadErrs) == 0 {
		return nil
	}
	err := f.downloadErrs[0]
	f.downloadErrs = f.downloadErrs[1:]
	return err
}

func (f *Factory) nextUploadError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.uploadErrs) == 0 {
		return nil
	}
	err := f.uploadErrs[0]
	f.uploadErrs = f.uploadErrs[1:]
	return err
}

func (f *Factory) recordEvent(event string) {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
}

func seriesKey(seriesHost, tag string) string {
	return seriesHost + "\x00" + tag
}

func cloneRecord(record historysync.SyncRecord) historysync.SyncRecord {
	clone := record
	if record.Entry != nil {
		entry := *record.Entry
		clone.Entry = &entry
	}
	return clone
}

func sortRecords(records []historysync.SyncRecord) {
	sort.Slice(records, func(i, j int) bool { return records[i].Index < records[j].Index })
}
