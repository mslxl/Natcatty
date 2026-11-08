// Package remote implements the Atuin v18 HTTP sync protocol behind the
// historysync.Remote port. It is the only place that knows Atuin's HTTP,
// MessagePack, and encryption details: callers exchange domain accounts,
// series, and sync records.
//
// The adapter is pinned to the Atuin client at commit
// ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e (v18.24.5). It sends that version
// as the Atuin-Version header, requires a v18 server, and fails closed on an
// unknown major version or a capability protocol this build does not
// implement.
//
// Only the history record tag is exposed. The delete-all-store endpoint at
// DELETE /api/v0/store is deliberately absent: no method builds that path.
// Requests are never retried here; the sync engine owns retry and backoff.
// The pinned server silently ignores a duplicate insert, so an upload is
// acknowledged only after the record stored at every sent (history, host,
// index) has been read back and compared with what was sent. A duplicate that
// the server already holds with different metadata or plaintext is therefore a
// conflict, never a silent success. The conflict reports ErrUploadConflict so
// the engine does not retry a permanent series-index collision, and ErrRemote
// so every caller still treats the upload as failed.
//
// A username/password login mints a legacy CLI session token, which this
// adapter sends with the Token scheme. Atuin Hub can answer the login with a
// Hub API token (auth "hub") that requires the Bearer scheme. The Remote port
// cannot return or change an auth kind after a login, so a Hub token and any
// unknown auth kind are refused fail-closed instead of being stored and sent
// with the wrong scheme.
//
// The adapter never logs. Error values carry a sentinel and, at most, a fixed
// method and path: never a token, key, request body, response body, or record
// plaintext.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

const (
	// DefaultTimeout bounds one HTTP request. The caller's context can cancel
	// it earlier.
	DefaultTimeout = 30 * time.Second
	// DefaultPageSize is used before the server's page-size capability is read.
	DefaultPageSize = 100
	// maxBodyBytes bounds one response read into memory. The pinned server's
	// default maximum record size is 1 GiB, so a remote peer could put a record
	// on the wire that this cap refuses. A page larger than the cap fails
	// closed with ErrRemote instead of being partially applied: reading a GiB
	// into memory to accept one pathological record would make the client a
	// memory-exhaustion target, and this client's own records are many orders
	// of magnitude smaller.
	maxBodyBytes = 64 << 20

	headerAtuinVersion = "Atuin-Version"

	pathLogin        = "/login"
	pathAccount      = "/api/v0/me"
	pathCapabilities = "/api/v0/capabilities"
	pathRecord       = "/api/v0/record"
	pathRecordNext   = "/api/v0/record/next"

	// defaultAppVersion is the fallback for a build without a package manifest.
	// Keep it in step with mygo.json; the composition root passes the packaged
	// version through UserAgentForVersion.
	defaultAppVersion = "0.1.0-beta.1"
	userAgentProduct  = "natcatty"
	// DefaultUserAgent names this client when a caller supplies no version.
	DefaultUserAgent = userAgentProduct + "/" + defaultAppVersion
)

// UserAgentForVersion returns the production User-Agent for a packaged version.
// An empty version falls back to DefaultUserAgent.
func UserAgentForVersion(version string) string {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return DefaultUserAgent
	}
	return userAgentProduct + "/" + trimmed
}

// Internal request errors. They become a public historysync sentinel before
// they leave the adapter.
var (
	errUnauthorized = errors.New("atuin remote: unauthorized")
	errNotFound     = errors.New("atuin remote: not found")
	errBadRequest   = errors.New("atuin remote: rejected request")
)

// uploadConflictError reports that the server acknowledged an upload but does
// not hold the record that was sent at that index. It reports
// ErrUploadConflict so the engine parks a permanent series-index collision
// instead of retrying it forever, and ErrRemote so a caller that knows only
// the generic remote failure still treats the upload as failed and must not
// advance its checkpoint. The message never carries a credential or record
// plaintext: only the index and a fixed reason.
type uploadConflictError struct {
	index  uint64
	reason string
}

func (conflict *uploadConflictError) Error() string {
	return fmt.Sprintf("atuin remote: upload conflict at index %d: %s", conflict.index, conflict.reason)
}

func (conflict *uploadConflictError) Unwrap() []error {
	return []error{historysync.ErrUploadConflict, historysync.ErrRemote}
}

// Options configure the adapter. The zero value uses the system TLS trust
// store, a bounded timeout, and a User-Agent naming Netcatty and its version.
type Options struct {
	// HTTPClient replaces the default client. Its redirect policy is always
	// replaced with the adapter's refusal policy: a supplied policy must not
	// be able to forward a credential to another origin.
	HTTPClient *http.Client
	// Timeout bounds one request when HTTPClient has no timeout. The zero value
	// is DefaultTimeout.
	Timeout time.Duration
	// UserAgent overrides the default "natcatty/<version>" value.
	UserAgent string
}

// Factory creates one operation-scoped Remote per Open. It is safe for
// concurrent use and retains no credentials.
type Factory struct {
	userAgent string
	http      *http.Client
}

// NewFactory returns a Factory. It performs no network work.
func NewFactory(options Options) *Factory {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	userAgent := strings.TrimSpace(options.UserAgent)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	var client http.Client
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}
	if client.Timeout <= 0 {
		client.Timeout = timeout
	}
	// A redirected sync request could forward the token to another origin.
	// Refuse redirects and treat the 3xx as an ordinary remote failure, even
	// when the caller supplied a client with a more permissive policy.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Factory{userAgent: userAgent, http: &client}
}

// Open returns one operation-scoped Remote. Credentials are copied, so the
// caller may wipe its Config immediately after Open.
func (factory *Factory) Open(_ context.Context, config historysync.Config) (historysync.Remote, error) {
	base, err := normalizeBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	token := config.Token
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("atuin remote: token contains a line break")
	}
	username := strings.TrimSpace(config.Username)
	password := config.Password
	tokenFlow := token != ""
	passwordFlow := username != "" && password != ""
	switch {
	case tokenFlow && passwordFlow:
		return nil, errors.New("atuin remote: both a token and a password were supplied")
	case !tokenFlow && !passwordFlow:
		return nil, errors.New("atuin remote: no token or username/password supplied")
	}
	authMode := atuinmodel.NormalizeAuthMode(config.AuthMode)
	if passwordFlow && authMode != atuinmodel.AuthModeSession {
		// Only the login endpoint mints a session token; Bearer is for a pasted
		// Hub token.
		return nil, errors.New("atuin remote: password login requires session auth")
	}

	remote := &Remote{
		base:      base,
		authMode:  authMode,
		http:      factory.http,
		userAgent: factory.userAgent,
		token:     token,
		username:  username,
		password:  password,
		pageSize:  DefaultPageSize,
	}
	if len(config.MasterKey) > 0 {
		key, err := wire.KeyFromBytes(config.MasterKey)
		if err != nil {
			return nil, err
		}
		remote.key = key
		remote.haveKey = true
	}
	return remote, nil
}

// Remote is one operation-scoped Atuin connection. A sync engine uses it from
// one goroutine; the token and key are guarded anyway so a status read cannot
// race an authentication.
type Remote struct {
	base      *url.URL
	authMode  atuinmodel.AuthMode
	http      *http.Client
	userAgent string

	mu       sync.RWMutex
	token    string
	username string
	password string
	key      wire.Key
	haveKey  bool
	pageSize int
}

var (
	_ historysync.Remote  = (*Remote)(nil)
	_ historysync.Factory = (*Factory)(nil)
)

// normalizeBaseURL validates the configured base URL and returns it without a
// trailing slash. Credentials, a query, and a fragment are refused so a token
// can never hide in the address.
func normalizeBaseURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return nil, errors.New("atuin remote: invalid sync address")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("atuin remote: sync address must not carry credentials, a query, or a fragment")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "https"
	case "http":
		// Plain HTTP is already limited to loopback development addresses by
		// historysync.NormalizeSyncAddress. Refuse a non-loopback host here too
		// so a caller that bypasses that policy cannot send a token in clear.
		if !isLoopbackHost(parsed.Hostname()) {
			return nil, errors.New("atuin remote: plain HTTP is allowed only for loopback")
		}
		parsed.Scheme = "http"
	default:
		return nil, errors.New("atuin remote: sync address must use https")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// endpoint joins one fixed protocol path onto the configured base, preserving
// a self-hosted server's path prefix.
func (r *Remote) endpoint(path string) *url.URL {
	endpoint := *r.base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawPath = ""
	return &endpoint
}

// Authenticate validates a pasted token against the account endpoint or logs
// in with username/password. A login returns the minted session token; a
// validated pasted token returns none.
func (r *Remote) Authenticate(ctx context.Context) (historysync.AuthResult, error) {
	r.mu.RLock()
	token := r.token
	username := r.username
	password := r.password
	r.mu.RUnlock()

	if token != "" {
		var account meResponse
		if err := r.getJSON(ctx, pathAccount, nil, &account, true); err != nil {
			switch {
			case errors.Is(err, errUnauthorized), errors.Is(err, errNotFound), errors.Is(err, errBadRequest):
				// The account endpoint uses HTTP 400 for a missing or
				// undecodable Authorization header. On this endpoint that is
				// a terminal authentication/configuration failure (for
				// example Bearer against an OSS server), not a retryable
				// remote error.
				return historysync.AuthResult{}, historysync.ErrAuthFailed
			default:
				return historysync.AuthResult{}, publicRemoteError(err)
			}
		}
		return historysync.AuthResult{Account: historysync.Account{
			ServerURL: r.base.String(),
			Username:  strings.TrimSpace(account.Username),
		}}, nil
	}

	if username == "" || password == "" {
		return historysync.AuthResult{}, historysync.ErrAuthFailed
	}
	body, err := json.Marshal(loginRequest{Username: username, Password: password})
	if err != nil {
		return historysync.AuthResult{}, fmt.Errorf("%w: could not encode login", historysync.ErrRemote)
	}
	// The encoded body contains the password. Zero the buffer once the request
	// has completed so the credential does not linger on the heap; the password
	// string itself is immutable and cannot be wiped.
	defer zeroBytes(body)
	var login loginResponse
	if err := r.postJSON(ctx, pathLogin, nil, body, &login, false); err != nil {
		switch {
		case errors.Is(err, errUnauthorized), errors.Is(err, errNotFound):
			return historysync.AuthResult{}, historysync.ErrAuthFailed
		default:
			return historysync.AuthResult{}, publicRemoteError(err)
		}
	}
	if login.Session == "" {
		return historysync.AuthResult{}, fmt.Errorf("%w: login returned no session", historysync.ErrRemote)
	}
	switch authKind := strings.TrimSpace(login.Auth); authKind {
	case "", "cli":
		// A legacy CLI session token is sent with the Token scheme. An old
		// server omits the field; the pinned client treats that as "cli".
	case "hub":
		// The port has no way to switch this operation to the Bearer scheme,
		// so storing a Hub token would send it with the wrong scheme forever.
		return historysync.AuthResult{}, fmt.Errorf("%w: the server minted a Hub token this login cannot use", historysync.ErrAuthFailed)
	default:
		return historysync.AuthResult{}, fmt.Errorf("%w: the server returned an unsupported login auth kind", historysync.ErrAuthFailed)
	}

	r.mu.Lock()
	r.token = login.Session
	r.password = ""
	r.mu.Unlock()

	return historysync.AuthResult{Token: login.Session, Account: historysync.Account{
		ServerURL: r.base.String(),
		Username:  username,
		UserID:    strings.TrimSpace(login.UserID),
	}}, nil
}

// Capabilities reads the server protocol version and capability set. It fails
// closed unless the server is the pinned major version and advertises every
// capability this client uses at a protocol version it implements.
func (r *Remote) Capabilities(ctx context.Context) (historysync.Capabilities, error) {
	var document capabilityDocument
	body, version, err := r.do(ctx, http.MethodGet, pathCapabilities, nil, nil, true)
	if err != nil {
		if errors.Is(err, errNotFound) {
			// A v18 server exposes capabilities; a 404 means this is not a
			// server this client can negotiate with, not a transient error.
			return historysync.Capabilities{}, fmt.Errorf("%w: the server does not expose capabilities", historysync.ErrIncompatibleServer)
		}
		return historysync.Capabilities{}, publicRemoteError(err)
	}
	if err := decodeJSON(body, &document); err != nil {
		return historysync.Capabilities{}, err
	}
	capabilities, err := parseCapabilities(document, version)
	if err != nil {
		return historysync.Capabilities{}, err
	}
	r.mu.Lock()
	r.pageSize = capabilities.PageSize
	r.mu.Unlock()
	return capabilities, nil
}

// RecordStatus reads the highest index of every history series. Other Atuin
// tags are ignored even when the server lists them.
func (r *Remote) RecordStatus(ctx context.Context) ([]historysync.SeriesStatus, error) {
	body, _, err := r.do(ctx, http.MethodGet, pathRecord, nil, nil, true)
	if err != nil {
		return nil, publicRemoteError(err)
	}
	return parseRecordStatus(body)
}

// Download reads one page of history records starting at start. Records under
// another tag are ignored; a malformed record, a foreign series, or an
// out-of-order index rejects the page instead of being skipped silently.
func (r *Remote) Download(ctx context.Context, series historysync.Series, start int64, count int) (historysync.Page, error) {
	if series.Tag != domainhistory.RecordTagHistory {
		return historysync.Page{}, fmt.Errorf("%w: only the history tag may be downloaded", historysync.ErrRemote)
	}
	seriesHost, ok := wire.CanonicalUUID(series.SeriesHost)
	if !ok {
		return historysync.Page{}, fmt.Errorf("%w: series host is not a UUID", historysync.ErrRemote)
	}
	if start < 0 {
		return historysync.Page{}, fmt.Errorf("%w: negative start index", historysync.ErrRemote)
	}
	key, haveKey := r.keyCopy()
	if !haveKey {
		return historysync.Page{}, historysync.ErrKeyMismatch
	}
	// Wipe the operation-scoped copy when the call ends. The remote keeps its
	// own copy for later operations; copies made inside wire helpers are
	// transient and cannot all be reached.
	defer key.Wipe()
	effectiveCount := r.effectivePageSize(count)
	records, err := r.fetchWirePage(ctx, seriesHost, start, effectiveCount)
	if err != nil {
		return historysync.Page{}, err
	}
	return decodePage(records, seriesHost, start, key)
}

// fetchWirePage requests one page of loose records by (host, tag, start,
// count) and validates the envelope. It is the single place that builds the
// paging query and maps a failed request onto the port's errors.
func (r *Remote) fetchWirePage(ctx context.Context, seriesHost string, start int64, count int) ([]wire.Record, error) {
	query := url.Values{
		"host":  {seriesHost},
		"tag":   {domainhistory.RecordTagHistory},
		"start": {strconv.FormatInt(start, 10)},
		"count": {strconv.Itoa(count)},
	}
	body, _, err := r.do(ctx, http.MethodGet, pathRecordNext, query, nil, true)
	if err != nil {
		return nil, publicRemoteError(err)
	}
	return parseWireRecords(body, count)
}

// Upload sends one ordered batch of history records. It never retries. The
// pinned server ignores a duplicate insert and acknowledges a POST without
// metadata, so success is reported only after the stored record at every sent
// index has been read back and compared with what was sent. A server holding a
// different record, or no record, at a sent index is a conflict: acknowledging
// it would let the engine advance its checkpoint and lose the local command.
func (r *Remote) Upload(ctx context.Context, records []historysync.SyncRecord) error {
	if len(records) == 0 {
		return nil
	}
	key, haveKey := r.keyCopy()
	if !haveKey {
		return historysync.ErrKeyMismatch
	}
	defer key.Wipe()
	if err := validateUploadBatch(records); err != nil {
		return err
	}
	encoded := make([]wire.Record, 0, len(records))
	for _, record := range records {
		wireRecord, err := encodeRecord(record, key)
		if err != nil {
			return err
		}
		encoded = append(encoded, wireRecord)
	}
	body, err := json.Marshal(encoded)
	if err != nil {
		return fmt.Errorf("%w: could not encode records", historysync.ErrRemote)
	}
	if _, _, err := r.do(ctx, http.MethodPost, pathRecord, nil, body, true); err != nil {
		return publicRemoteError(err)
	}
	return r.verifyUpload(ctx, encoded, key)
}

// validateUploadBatch enforces the invariant read-back verification relies on:
// within one writer series, indexes are strictly increasing, so each sent
// record has a distinct server key. A batch may carry more than one series.
func validateUploadBatch(records []historysync.SyncRecord) error {
	previous := make(map[string]int64, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		host, ok := wire.CanonicalUUID(record.SeriesHost)
		if !ok {
			return fmt.Errorf("%w: series host is not a UUID", historysync.ErrRemote)
		}
		if seen[host] && record.Index <= previous[host] {
			return fmt.Errorf("%w: upload batch is not ordered by a unique index", historysync.ErrRemote)
		}
		previous[host] = record.Index
		seen[host] = true
	}
	return nil
}

// verifyUpload reads back each series' index range and confirms the server
// stored exactly the records that were sent. It decrypts with the operation's
// master key and compares the outer identity (id, index, host, timestamp,
// version, tag) and the plaintext payload. Extra records in a range are
// ignored; a missing index or any difference is a conflict.
func (r *Remote) verifyUpload(ctx context.Context, sent []wire.Record, key wire.Key) error {
	if len(sent) == 0 {
		return nil
	}
	series := make(map[string][]wire.Record, 1)
	order := make([]string, 0, 1)
	for _, record := range sent {
		host, ok := wire.CanonicalUUID(record.SeriesHost)
		if !ok {
			return fmt.Errorf("%w: series host is not a UUID", historysync.ErrRemote)
		}
		if _, exists := series[host]; !exists {
			order = append(order, host)
		}
		series[host] = append(series[host], record)
	}
	for _, host := range order {
		if err := r.verifySeriesUpload(ctx, host, series[host], key); err != nil {
			return err
		}
	}
	return nil
}

// verifySeriesUpload reads back one series' index range. Its records are in
// strictly increasing index order, as validateUploadBatch requires.
func (r *Remote) verifySeriesUpload(ctx context.Context, seriesHost string, sent []wire.Record, key wire.Key) error {
	start := sent[0].Index
	end := sent[len(sent)-1].Index

	expected := make(map[uint64]wire.Record, len(sent))
	for _, record := range sent {
		expected[record.Index] = record
	}
	found := make(map[uint64]wire.Record, len(sent))
	position := start
	// A batch is page-sized in normal use. The bound keeps a sparse or
	// hostile range from turning one upload into unbounded HTTP traffic.
	const maxReadBackPages = 1024
	for pages := 0; position <= end; pages++ {
		if pages >= maxReadBackPages {
			return &uploadConflictError{index: position, reason: "server record range is too large to verify"}
		}
		span := end - position + 1
		requested := DefaultPageSize
		if span < uint64(requested) {
			requested = int(span)
		}
		page, err := r.fetchWirePage(ctx, seriesHost, int64(position), r.effectivePageSize(requested))
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		next := position
		for _, record := range page {
			if record.Tag != domainhistory.RecordTagHistory || record.SeriesHost != seriesHost {
				continue
			}
			if record.Index < position {
				return &uploadConflictError{index: position, reason: "server returned an out-of-order record"}
			}
			if _, wanted := expected[record.Index]; wanted {
				if _, duplicate := found[record.Index]; duplicate {
					return &uploadConflictError{index: record.Index, reason: "server returned an index more than once"}
				}
				found[record.Index] = record
			}
			if record.Index >= next {
				next = record.Index
				if next != math.MaxUint64 {
					next++
				}
			}
		}
		if next <= position {
			return &uploadConflictError{index: position, reason: "server returned no forward progress"}
		}
		position = next
	}
	for _, record := range sent {
		serverRecord, ok := found[record.Index]
		if !ok {
			return &uploadConflictError{index: record.Index, reason: "the server does not hold the uploaded record"}
		}
		if !sameUploadedRecord(record, serverRecord, key) {
			return &uploadConflictError{index: record.Index, reason: "the server holds a different record"}
		}
	}
	return nil
}

// sameUploadedRecord compares one sent wire record with the record the server
// stored at the same index. Ciphertext is not comparable because every attempt
// uses a fresh content key, so the decrypted plaintext is compared instead. A
// read-back record that does not decrypt under this operation's key is not
// ours; that is a difference at the sent index, so it is reported as a
// conflict rather than as a key mismatch.
func sameUploadedRecord(sent, server wire.Record, key wire.Key) bool {
	sentID, sentOK := wire.CanonicalUUID(sent.ID)
	serverID, serverOK := wire.CanonicalUUID(server.ID)
	sentHost, sentHostOK := wire.CanonicalUUID(sent.SeriesHost)
	serverHost, serverHostOK := wire.CanonicalUUID(server.SeriesHost)
	if !sentOK || !serverOK || !sentHostOK || !serverHostOK {
		return false
	}
	if sentID != serverID || sentHost != serverHost || sent.Index != server.Index ||
		sent.Timestamp != server.Timestamp || sent.Version != server.Version || sent.Tag != server.Tag {
		return false
	}
	sentPlaintext, err := sent.Decrypt(key)
	if err != nil {
		return false
	}
	serverPlaintext, err := server.Decrypt(key)
	if err != nil {
		return false
	}
	return bytes.Equal(sentPlaintext, serverPlaintext)
}

// ValidateMasterKey decrypts one existing history record. An account with no
// history cannot verify a key and reports unverified.
func (r *Remote) ValidateMasterKey(ctx context.Context) (historysync.KeyValidation, error) {
	statuses, err := r.RecordStatus(ctx)
	if err != nil {
		return "", err
	}
	var sample *historysync.SeriesStatus
	for index := range statuses {
		status := statuses[index]
		if status.Tag == domainhistory.RecordTagHistory && status.MaxIndex >= 0 {
			sample = &status
			break
		}
	}
	if sample == nil {
		return historysync.KeyValidationUnverified, nil
	}
	key, haveKey := r.keyCopy()
	if !haveKey {
		return "", historysync.ErrKeyMismatch
	}
	defer key.Wipe()
	seriesHost, ok := wire.CanonicalUUID(sample.SeriesHost)
	if !ok {
		return "", fmt.Errorf("%w: series host is not a UUID", historysync.ErrRemote)
	}
	records, err := r.fetchWirePage(ctx, seriesHost, 0, 1)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.Tag != domainhistory.RecordTagHistory {
			continue
		}
		if record.SeriesHost != seriesHost {
			return "", fmt.Errorf("%w: record belongs to another series", historysync.ErrRemote)
		}
		if _, err := record.Decrypt(key); err != nil {
			if errors.Is(err, wire.ErrKeyMismatch) {
				return historysync.KeyValidationMismatch, nil
			}
			return "", fmt.Errorf("%w: record could not be decoded", historysync.ErrRemote)
		}
		return historysync.KeyValidationVerified, nil
	}
	return historysync.KeyValidationUnverified, nil
}

// Close wipes the credentials and key held for this operation.
func (r *Remote) Close() error {
	r.mu.Lock()
	r.token = ""
	r.password = ""
	r.key.Wipe()
	r.haveKey = false
	r.mu.Unlock()
	return nil
}

func (r *Remote) keyCopy() (wire.Key, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.key, r.haveKey
}

// zeroBytes overwrites a buffer that held a credential. It is best effort:
// Go strings cannot be wiped and a compiler is not obliged to preserve a store
// it cannot observe.
func zeroBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}

func (r *Remote) effectivePageSize(requested int) int {
	if requested <= 0 {
		requested = DefaultPageSize
	}
	r.mu.RLock()
	pageSize := r.pageSize
	r.mu.RUnlock()
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if requested > pageSize {
		requested = pageSize
	}
	return requested
}

// do performs one request and returns the response body and the server
// version. A non-2xx status becomes an internal or public sentinel; a 2xx
// response without a v18 Atuin-Version header fails closed.
func (r *Remote) do(ctx context.Context, method, path string, query url.Values, body []byte, authenticated bool) ([]byte, serverVersion, error) {
	var zero serverVersion

	endpoint := r.endpoint(path)
	endpoint.RawQuery = query.Encode()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, zero, fmt.Errorf("%w: could not build a request", historysync.ErrRemote)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set(headerAtuinVersion, historysync.PinnedAtuinVersion)
	request.Header.Set("User-Agent", r.userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		r.mu.RLock()
		token := r.token
		r.mu.RUnlock()
		if token != "" {
			scheme := "Token"
			if r.authMode == atuinmodel.AuthModeBearer {
				scheme = "Bearer"
			}
			request.Header.Set("Authorization", scheme+" "+token)
		}
	}

	response, err := r.http.Do(request)
	if err != nil {
		// The transport error can name a URL and must never carry a header into
		// an error string. Report only the fixed method and path.
		return nil, zero, fmt.Errorf("%w: %s %s could not complete", historysync.ErrOffline, method, path)
	}
	defer response.Body.Close()

	// Classify the status before reading the body: an auth failure must remain
	// an auth failure even when the error body is truncated or larger than the
	// cap, and a 404 must not be masked by a body-read error.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, zero, fmt.Errorf("%w: %s %s returned HTTP %d", errUnauthorized, method, path, response.StatusCode)
		case http.StatusNotFound:
			return nil, zero, fmt.Errorf("%w: %s %s returned HTTP %d", errNotFound, method, path, response.StatusCode)
		case http.StatusBadRequest:
			return nil, zero, fmt.Errorf("%w: %s %s returned HTTP %d", errBadRequest, method, path, response.StatusCode)
		default:
			return nil, zero, fmt.Errorf("%w: %s %s returned HTTP %d", historysync.ErrRemote, method, path, response.StatusCode)
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		// A partial read can already contain a minted session token or record
		// data. The caller never receives this buffer on error, so wipe it here.
		zeroBytes(data)
		return nil, zero, fmt.Errorf("%w: %s %s response could not be read", historysync.ErrRemote, method, path)
	}
	if len(data) > maxBodyBytes {
		zeroBytes(data)
		return nil, zero, fmt.Errorf("%w: %s %s response is too large", historysync.ErrRemote, method, path)
	}

	version, err := parseServerVersion(response.Header.Get(headerAtuinVersion))
	if err != nil || version.Major != historysync.PinnedAtuinMajor {
		zeroBytes(data)
		return nil, zero, fmt.Errorf("%w: server does not speak Atuin v%d", historysync.ErrIncompatibleServer, historysync.PinnedAtuinMajor)
	}
	return data, version, nil
}

func (r *Remote) getJSON(ctx context.Context, path string, query url.Values, target any, authenticated bool) error {
	body, _, err := r.do(ctx, http.MethodGet, path, query, nil, authenticated)
	if err != nil {
		return err
	}
	return decodeJSON(body, target)
}

func (r *Remote) postJSON(ctx context.Context, path string, query url.Values, body []byte, target any, authenticated bool) error {
	response, _, err := r.do(ctx, http.MethodPost, path, query, body, authenticated)
	if err != nil {
		return err
	}
	// A POST response may carry a freshly minted session token (the login
	// endpoint does). Decode first, then zero the buffer on every path so the
	// credential does not linger on the heap. This is safe for any endpoint:
	// the decoded value owns the data it needs.
	defer zeroBytes(response)
	if target == nil {
		return nil
	}
	return decodeJSON(response, target)
}

// publicRemoteError maps the internal request sentinels onto the port's
// public vocabulary. It never copies a server message.
func publicRemoteError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errUnauthorized):
		return fmt.Errorf("%w: the server rejected the session token", historysync.ErrAuthExpired)
	case errors.Is(err, errNotFound):
		return fmt.Errorf("%w: the server has no such endpoint", historysync.ErrRemote)
	case errors.Is(err, errBadRequest):
		return fmt.Errorf("%w: the server rejected the request", historysync.ErrRemote)
	default:
		return err
	}
}

// decodeJSON rejects trailing data as well as invalid JSON so a malformed
// response is never half-applied.
func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: malformed response", historysync.ErrRemote)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing data after a response", historysync.ErrRemote)
	}
	return nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Session string `json:"session"`
	Auth    string `json:"auth"`
	UserID  string `json:"user_id"`
}

type meResponse struct {
	Username string `json:"username"`
}

// Sorted statuses keep an account operation deterministic even though the
// server's status document is a JSON object.
func sortStatuses(statuses []historysync.SeriesStatus) {
	sort.Slice(statuses, func(left, right int) bool {
		if statuses[left].SeriesHost != statuses[right].SeriesHost {
			return statuses[left].SeriesHost < statuses[right].SeriesHost
		}
		return statuses[left].Tag < statuses[right].Tag
	})
}
