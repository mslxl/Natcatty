package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// Data-provider runtime bounds. They apply to one provider instance and every
// profile connection it serves.
const (
	// DefaultDataOpenTimeout bounds one connection open or profile test,
	// including a host-resolved secret or tunnel.
	DefaultDataOpenTimeout = 30 * time.Second
	// DefaultDataCallTimeout bounds ping, capabilities, cancel, close,
	// transactions, metadata, and plan.
	DefaultDataCallTimeout = 2 * time.Second
	// DefaultDataFetchTimeout bounds one query or fetch batch. A provider may
	// stream a large result, so the fetch bound matches the open bound.
	DefaultDataFetchTimeout = 30 * time.Second
	// MaxDataCallTimeout is the hard cap the runtime applies to one
	// host-to-guest data-provider call.
	MaxDataCallTimeout = DefaultDataOpenTimeout
	// DefaultDataIdleTTL is how long a pooled connection stays idle before the
	// host closes it.
	DefaultDataIdleTTL = 5 * time.Minute
	// MaxDataRequestBytes bounds the JSON request one data method may send.
	MaxDataRequestBytes = 4 << 20
	// MaxDataResponseBytes bounds one data-provider JSON response. The result
	// envelope itself is bounded by the domain batch limits.
	MaxDataResponseBytes = 8 << 20
	// InteractiveDataRows is the row/document/pair budget one interactive
	// query may deliver before the host reports the limit.
	InteractiveDataRows = 100_000
	// InteractiveDataBytes is the encoded-byte budget one interactive query
	// may deliver before the host reports the limit.
	InteractiveDataBytes = 64 << 20
	// dataCancelWindow bounds the ids remembered for cooperative
	// cancellation.
	dataCancelWindow = 64
)

// DataLimits overrides the data-provider runtime bounds. Zero fields keep the
// defaults; interactive budgets may be lowered for tests but never raised.
type DataLimits struct {
	OpenTimeout      time.Duration
	CallTimeout      time.Duration
	FetchTimeout     time.Duration
	IdleTTL          time.Duration
	InteractiveRows  int
	InteractiveBytes int
}

// Normalize fills zero values with the contract defaults and caps each bound
// so a caller cannot disable a deadline or widen a budget past the host.
func (l DataLimits) Normalize() DataLimits {
	if l.OpenTimeout <= 0 || l.OpenTimeout > MaxDataCallTimeout {
		l.OpenTimeout = DefaultDataOpenTimeout
	}
	if l.CallTimeout <= 0 || l.CallTimeout > MaxDataCallTimeout {
		l.CallTimeout = DefaultDataCallTimeout
	}
	if l.FetchTimeout <= 0 || l.FetchTimeout > MaxDataCallTimeout {
		l.FetchTimeout = DefaultDataFetchTimeout
	}
	if l.IdleTTL <= 0 {
		l.IdleTTL = DefaultDataIdleTTL
	}
	if l.InteractiveRows <= 0 || l.InteractiveRows > InteractiveDataRows {
		l.InteractiveRows = InteractiveDataRows
	}
	if l.InteractiveBytes <= 0 || l.InteractiveBytes > InteractiveDataBytes {
		l.InteractiveBytes = InteractiveDataBytes
	}
	return l
}

// DataHostOptions configures a data-provider host. Audit receives opens,
// operation classes, profile tests, hidden secret reads, limits,
// cancellations, and denials; it never receives query input, parameter
// values, result content, or secret content. Schedule makes the idle TTL
// deterministic in tests.
type DataHostOptions struct {
	Audit    AuditSink
	Now      func() time.Time
	Schedule func(time.Duration, func()) func()
	Limits   DataLimits
	// Profiles and Secrets are the Vault-backed profile ports. Without them,
	// profile CRUD and connections fail closed.
	Profiles DataProfileStore
	Secrets  DataProfileSecrets
	// Tunnel resolves a profile's via Host. A profile that names a Host fails
	// closed when this port is absent.
	Tunnel DataTunnelSource
	// NewProfileID overrides profile id generation for deterministic tests.
	NewProfileID func() string
}

// DataProviderHost owns every registered data provider, the profile records,
// and the pooled connections. It is safe for concurrent use.
type DataProviderHost struct {
	audit     AuditSink
	now       func() time.Time
	schedule  func(time.Duration, func()) func()
	limits    DataLimits
	profiles  DataProfileStore
	secrets   DataProfileSecrets
	tunnel    DataTunnelSource
	newID     func() string
	next      requestCounter
	closeOnce sync.Once

	mu          sync.Mutex
	providers   map[string]*dataProviderEntry
	instances   map[domainplugin.FiberID]*dataProviderInstance
	connections map[*DataConnection]struct{}
	pool        map[string][]*DataConnection
	closed      bool
}

// NewDataProviderHost returns an empty host. Profile ports may be attached
// later through the options only; a nil port fails closed.
func NewDataProviderHost(options DataHostOptions) *DataProviderHost {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	schedule := options.Schedule
	if schedule == nil {
		schedule = func(delay time.Duration, fn func()) func() {
			timer := time.AfterFunc(delay, fn)
			return func() { timer.Stop() }
		}
	}
	newID := options.NewProfileID
	if newID == nil {
		newID = newDataProfileID
	}
	return &DataProviderHost{
		audit:       options.Audit,
		now:         now,
		schedule:    schedule,
		limits:      options.Limits.Normalize(),
		profiles:    options.Profiles,
		secrets:     options.Secrets,
		tunnel:      options.Tunnel,
		newID:       newID,
		providers:   make(map[string]*dataProviderEntry),
		instances:   make(map[domainplugin.FiberID]*dataProviderInstance),
		connections: make(map[*DataConnection]struct{}),
		pool:        make(map[string][]*DataConnection),
	}
}

// DataProviderInvoker is the fiber-side port a data host calls. A fiber
// implements it with the same one-call serialization as dispatch, so the
// provider never races lifecycle calls on one instance.
type DataProviderInvoker interface {
	InvokeDataProvider(ctx context.Context, request DataProviderRequest) ([]byte, error)
	// SupportsDataProvider reports whether the live module exports
	// natcatty_data_provider_request.
	SupportsDataProvider() bool
}

// DataProviderRequest is one host-to-guest data-provider call. The guest
// answers through the data_provider_respond host import while the call is in
// flight; the runtime returns the copied JSON response envelope.
type DataProviderRequest struct {
	// ID is unique per data-provider call. The guest echoes it in the response
	// import so a late or mismatched response is rejected.
	ID uint64
	// Method is one member of the generated data-provider method set.
	Method domainpluginabi.DataProviderMethod
	// Payload is the UTF-8 JSON method body. It never contains host tunnel
	// credentials or profile secret references.
	Payload []byte
}

// DataProviderCallbacks are the host-side operations a data provider calls
// through the generated imports.
type DataProviderCallbacks struct {
	// Cancelled reports whether the host asked an in-flight request to stop.
	Cancelled func(requestID uint64) bool
}

type dataProviderEntry struct {
	instance *dataProviderInstance
	provider *DataProvider
}

// DataProvider is one registered data-provider handle. It is safe for
// concurrent use.
type DataProvider struct {
	host         *DataProviderHost
	instance     *dataProviderInstance
	contribution domainplugin.DataProvider
	qualifiedID  string
	maxMode      domainplugin.DataAccessMode
	// generation is bumped by Unregister under the host mutex. Acquire captures
	// it when it resolves the provider and re-checks it before publishing a
	// connection, so a guest open that raced an unregister sweep cannot
	// resurrect the provider or leak a connection into the host map.
	generation uint64
}

// ID returns the host-wide qualified provider ID.
func (p *DataProvider) ID() string { return p.qualifiedID }

// Contribution returns the static declaration read at activation.
func (p *DataProvider) Contribution() domainplugin.DataProvider { return p.contribution }

// MaxAccessMode returns the most privileged mode the activation grant allows.
func (p *DataProvider) MaxAccessMode() domainplugin.DataAccessMode { return p.maxMode }

// Register publishes one activated data provider. A duplicate or invalid
// declaration, an unsupported grant, and a module without the data-provider
// export are rejected. Registration is transactional: a rejected call leaves
// no entry behind.
func (h *DataProviderHost) Register(fiberID domainplugin.FiberID, ref PluginRef,
	contribution domainplugin.DataProvider, maxMode domainplugin.DataAccessMode,
	invoker DataProviderInvoker) (*DataProvider, error) {

	if h == nil {
		return nil, ErrDataUnavailable
	}
	if invoker == nil || !invoker.SupportsDataProvider() {
		return nil, fmt.Errorf("plugin %s: data provider %q requires the %s export",
			ref.ID, contribution.ID, domainpluginabi.ExportDataProviderRequest)
	}
	if err := domainplugin.ValidateDataProvider(ref.ID, contribution, 0); err != nil {
		return nil, err
	}
	if !maxMode.Valid() {
		return nil, fmt.Errorf("plugin %s: data provider %q has no valid access grant",
			ref.ID, contribution.ID)
	}
	qualifiedID := domainplugin.QualifiedDataProviderID(ref.ID, contribution.ID)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if _, exists := h.providers[qualifiedID]; exists {
		h.mu.Unlock()
		return nil, fmt.Errorf("plugin: data provider %q is already registered", qualifiedID)
	}
	instance := h.instances[fiberID]
	if instance == nil {
		instance = newDataProviderInstance(h, fiberID, ref, invoker)
		h.instances[fiberID] = instance
	} else if instance.invoker != invoker || !instance.activeState() {
		h.mu.Unlock()
		return nil, fmt.Errorf("plugin %s: data provider instance for fiber %q is not usable",
			ref.ID, fiberID)
	}
	provider := &DataProvider{
		host: h, instance: instance, contribution: contribution,
		qualifiedID: qualifiedID, maxMode: maxMode,
	}
	h.providers[qualifiedID] = &dataProviderEntry{
		instance: instance, provider: provider,
	}
	h.mu.Unlock()
	h.recordDataEvent(provider, "register", "ok", maxMode, "", 0, 0)
	return provider, nil
}

// Unregister removes provider and closes every connection it served. It is
// idempotent.
func (h *DataProviderHost) Unregister(provider *DataProvider) error {
	if h == nil || provider == nil {
		return nil
	}
	h.mu.Lock()
	entry, ok := h.providers[provider.qualifiedID]
	if !ok || entry.provider != provider {
		h.mu.Unlock()
		return nil
	}
	delete(h.providers, provider.qualifiedID)
	// Invalidate every Acquire that resolved this registration before the
	// sweep. The bump is written under the same lock Acquire uses to re-check
	// and publish, so an open that completes afterwards is torn down instead of
	// inserted into the host map.
	provider.generation++
	remaining := 0
	for _, candidate := range h.providers {
		if candidate.instance == entry.instance {
			remaining++
		}
	}
	var closing []*DataConnection
	for connection := range h.connections {
		if connection.provider == provider {
			// The claim is the single teardown owner: a connection already
			// claimed by a working lease, a release, or the idle expirer is
			// closed and audited by that path, never a second time here.
			if h.claimConnectionCloseLocked(connection) {
				closing = append(closing, connection)
			}
		}
	}
	h.mu.Unlock()
	// Every claimed connection is closed through the forced path. A concurrent
	// host.Close may mark the shared instance inactive between the claim and
	// this call; the claim, not the instance state, makes this teardown the
	// single owner, so the guest close must still be attempted and the audit
	// row written only for that attempt.
	for _, connection := range closing {
		if provider.closeConnection(connection.token) {
			h.auditDataClose(connection, "unloaded")
		}
	}
	if remaining == 0 {
		entry.instance.markInactive()
		h.mu.Lock()
		if h.instances[entry.instance.fiberID] == entry.instance {
			delete(h.instances, entry.instance.fiberID)
		}
		h.mu.Unlock()
	}
	h.recordDataEvent(provider, "unregister", "ok", provider.maxMode, "", 0, 0)
	return nil
}

// Close unregisters every provider and closes every pooled connection.
// Containers normally dispose each fiber's registration, so Close is the
// defensive whole-host shutdown path.
func (h *DataProviderHost) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	instances := make([]*dataProviderInstance, 0, len(h.instances))
	for _, entry := range h.providers {
		instances = append(instances, entry.instance)
	}
	h.providers = make(map[string]*dataProviderEntry)
	h.instances = make(map[domainplugin.FiberID]*dataProviderInstance)
	closing := make([]*DataConnection, 0, len(h.connections))
	for connection := range h.connections {
		// The claim also clears the idle timer under h.mu. A connection some
		// other path already claimed is not closed or audited twice.
		if h.claimConnectionCloseLocked(connection) {
			closing = append(closing, connection)
		}
	}
	h.connections = make(map[*DataConnection]struct{})
	h.pool = make(map[string][]*DataConnection)
	h.mu.Unlock()
	for _, connection := range closing {
		if connection.provider.closeConnection(connection.token) {
			h.auditDataClose(connection, "host-closed")
		}
	}
	for _, instance := range instances {
		instance.markInactive()
	}
}

// Lookup returns the provider registered under a qualified ID.
func (h *DataProviderHost) Lookup(qualifiedID string) (*DataProvider, bool) {
	provider := h.provider(qualifiedID)
	return provider, provider != nil
}

func (h *DataProviderHost) provider(qualifiedID string) *DataProvider {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.providers[qualifiedID]
	if !ok {
		return nil
	}
	return entry.provider
}

// providerRegistration resolves one provider together with the generation of
// the registration it belongs to. Callers that run a guest call open hold the
// generation and pass it to registrationLiveLocked before acting on anything
// the call returned.
func (h *DataProviderHost) providerRegistration(qualifiedID string) (*DataProvider, uint64) {
	if h == nil {
		return nil, 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.providers[qualifiedID]
	if !ok {
		return nil, 0
	}
	return entry.provider, entry.provider.generation
}

// registrationLiveLocked reports whether provider is still the live
// registration generation was captured from and its instance can still serve
// calls. h.mu must be held.
func (h *DataProviderHost) registrationLiveLocked(provider *DataProvider,
	generation uint64) bool {

	if h.closed || provider == nil || provider.generation != generation ||
		!provider.instance.activeState() {
		return false
	}
	entry, ok := h.providers[provider.qualifiedID]
	return ok && entry.provider == provider
}

// Callbacks returns the host imports for one fiber's data-provider calls. The
// callbacks resolve the fiber's instance on every call, so they stay valid
// across registration, unload, and recycle. The result is never nil.
func (h *DataProviderHost) Callbacks(fiberID domainplugin.FiberID) *DataProviderCallbacks {
	return &DataProviderCallbacks{
		Cancelled: func(requestID uint64) bool {
			if h == nil {
				return false
			}
			h.mu.Lock()
			instance := h.instances[fiberID]
			h.mu.Unlock()
			return instance != nil && instance.cancelRequested(requestID)
		},
	}
}

// DataErrorKind is the closed failure vocabulary of a data provider. It
// mirrors the provider vocabulary where the meaning is shared and adds the
// data-specific invalid, denied, not-found, conflict, limit, and unsupported
// cases.
type DataErrorKind string

const (
	DataErrorInvalid     DataErrorKind = "invalid"
	DataErrorDenied      DataErrorKind = "denied"
	DataErrorNotFound    DataErrorKind = "not-found"
	DataErrorConflict    DataErrorKind = "conflict"
	DataErrorLimit       DataErrorKind = "limit"
	DataErrorUnsupported DataErrorKind = "unsupported"
	DataErrorUnreachable DataErrorKind = "unreachable"
	DataErrorAuthFailed  DataErrorKind = "auth-failed"
	DataErrorProtocol    DataErrorKind = "protocol-error"
	DataErrorTimeout     DataErrorKind = "timeout"
	DataErrorCancelled   DataErrorKind = "cancelled"
	DataErrorFaulted     DataErrorKind = "provider-faulted"
	DataErrorUnavailable DataErrorKind = "unavailable"
)

// DataErrorKindByName maps a guest status string to the closed kind. An
// unknown or empty value is a protocol error; a guest cannot invent a status.
func DataErrorKindByName(name string) (DataErrorKind, bool) {
	switch DataErrorKind(strings.ToLower(strings.TrimSpace(name))) {
	case DataErrorInvalid:
		return DataErrorInvalid, true
	case DataErrorDenied:
		return DataErrorDenied, true
	case DataErrorNotFound:
		return DataErrorNotFound, true
	case DataErrorConflict:
		return DataErrorConflict, true
	case DataErrorLimit:
		return DataErrorLimit, true
	case DataErrorUnsupported:
		return DataErrorUnsupported, true
	case DataErrorUnreachable:
		return DataErrorUnreachable, true
	case DataErrorAuthFailed:
		return DataErrorAuthFailed, true
	case DataErrorProtocol:
		return DataErrorProtocol, true
	case DataErrorTimeout:
		return DataErrorTimeout, true
	case DataErrorCancelled:
		return DataErrorCancelled, true
	case DataErrorFaulted:
		return DataErrorFaulted, true
	case DataErrorUnavailable:
		return DataErrorUnavailable, true
	default:
		return DataErrorProtocol, false
	}
}

// DataError is a typed failure from a data provider. Message is a short
// provider-supplied explanation; it never carries query input, parameter
// values, result content, or secret content.
type DataError struct {
	Kind     DataErrorKind
	Provider string
	Message  string
}

func (e *DataError) Error() string {
	kind := string(e.Kind)
	if kind == "" {
		kind = "data-error"
	}
	if e.Provider != "" {
		if e.Message != "" {
			return fmt.Sprintf("data provider %s from %s: %s", kind, e.Provider, e.Message)
		}
		return fmt.Sprintf("data provider %s from %s", kind, e.Provider)
	}
	if e.Message != "" {
		return fmt.Sprintf("data provider %s: %s", kind, e.Message)
	}
	return fmt.Sprintf("data provider %s", kind)
}

// dataSecretFragmentBytes is the shortest leading fragment of a resolved
// secret that the host treats as an echo. Below this bound a fragment carries
// too little secret material to be useful, while a provider that returns part
// of a long secret is still rejected: any prefix at or above the bound
// contains the threshold prefix, so one containment check covers every
// qualifying length. A short secret is still checked in full, both as a
// contiguous run and as an ordered subsequence.
const dataSecretFragmentBytes = 8

// dataSecretSeparatorBytes are the separator-class bytes a provider can
// interleave between the bytes of a secret while the surrounding text keeps a
// legitimate shape: "h.u.n.t.e.r.2" is a valid DNS name, "s3cr3t-key" is a
// plausible identifier, and long plan or summary text can pad every byte with
// spaces or punctuation. The guard rescans every candidate with these bytes
// removed, and the ordered-subsequence rule catches any other interleaved junk.
const dataSecretSeparatorBytes = " \t\n\r\v\f!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// dataTextSeparatorTable is the byte lookup form of dataSecretSeparatorBytes.
// The long-text normalizer and the base64 orientation walk classify every byte
// of a multi-megabyte candidate, and a linear table lookup keeps that pass from
// multiplying by the size of the separator set.
var dataTextSeparatorTable = func() [256]bool {
	var table [256]bool
	for i := 0; i < len(dataSecretSeparatorBytes); i++ {
		table[dataSecretSeparatorBytes[i]] = true
	}
	return table
}()

// dataSecretDecodeDepth bounds iterative decoding. A provider can pre-escape
// or percent-encode its text once per layer and the host can escape it again,
// so two passes are not enough: a per-character match can sit behind three or
// more nested \uXXXX/JSON/Go or %XX layers. Every intermediate form is scanned,
// and the work is bounded at a depth no realistic message stack reaches.
const dataSecretDecodeDepth = 8

// dataSecretDecodeWalkBound is the hard upper bound on the decoded forms one
// candidate can produce. The walk follows up to three linear transforms
// (escape, percent-query, percent-path) depth-first to dataSecretDecodeDepth,
// so a full ternary tree has (3^(depth+1)-1)/2 nodes and deduplication can only
// lower that. Unlike a breadth-first form cap, no decoy can consume the depth
// budget of a pure chain: every branch is followed to the documented depth.
const dataSecretDecodeWalkBound = 9841 // (3^9 - 1) / 2

// dataSecretBase64Depth bounds nested base64 layers. A decoded token is scanned
// through the same pipeline, so base64(base64(secret)) is still caught; deeper
// nesting is rejected rather than chased. The step is bounded by the token and
// decoded-byte budgets as well: a benign chain shrinks below the shortest
// secret long before this depth, while a provider that keeps nesting consumes
// the budgets and fails closed.
const dataSecretBase64Depth = 64

// dataSecretBase64TokenBudget bounds how many base64 tokens one guarded scan
// may decode. It is not a skip counter: when the budget is exhausted the scan
// fails closed, because a token that is never decoded could carry the secret.
// The budget is generous enough for ordinary text with a few encoded words and
// small enough that a wall of tokens cannot buy unbounded work.
const dataSecretBase64TokenBudget = 256

// dataSecretBase64GapBytes is the longest run of non-base64 bytes that is
// treated as a split inside one token rather than a token boundary. A provider
// can interleave a control byte, a high byte, or a short punctuation run; the
// joins reconnect the token so its decoded payload is still inspected.
const dataSecretBase64GapBytes = 8

// dataSecretBase64DecodedByteBudget bounds the decoded bytes one guarded scan
// may inspect across every base64 token and orientation. Exceeding it fails
// closed: the host never silently skips an undecoded token.
const dataSecretBase64DecodedByteBudget = 48 << 20

// dataSecretDecodedScanBytes bounds the decoded payload that is run through the
// full recursive pipeline. Larger decoded payloads are compared with the raw
// secret bytes and the threshold prefix only. The raw comparison still catches
// a plain secret in a large token; refusing to recurse keeps one huge benign
// base64 blob from costing an unbounded number of decoded forms.
const dataSecretDecodedScanBytes = 64 << 10

// dataSecretScanWorkBudget bounds the total bytes-equivalent work one guard
// scan may perform across every decoding, spelling, base64, and subsequence
// stage. The limit is deliberately expressed in work units rather than in
// tokens or forms: a provider can make any single stage repeat, and only a
// total budget turns a multi-second pathological text into an immediate
// fail-closed protocol error. Every stage charges the work it actually
// performs as it progresses, so a benign text that merely contains every secret
// byte is not rejected for it, while a dense anchor field still cannot buy
// seconds of host CPU after the guest call has returned.
//
// The prefix and case-equivalence fixes restored scans that the unsound
// prefilters had skipped, and the work model charges them at their real cost:
// the sliding-state update is two byte-equivalent units, and a variant without
// separator bytes is scanned only on the separated spelling or its stripped
// form, never both. Round 13 removes the remaining duplicate scans: the
// separator probe is decided from the byte counts, non-ASCII foldings are
// skipped for ASCII candidates, a plus-decoded form whose compared values are
// all space-free skips the spelling scan, and base64 orientations and decoded
// payloads already fully scanned at the same or a shallower depth are not
// scanned again.
//
// The bound is 192 MiB (201,326,592 units) because a benign 4 MiB diagnostic
// whose every field carries a "+" -- the query decoder materializes an extra
// multi-megabyte form -- performs 189,873,012 charged units (94.3% of the
// bound) and is delivered in about 175 ms on the reference machine. Other
// benign 4 MiB shapes, including the mixed-case text with a public qualifier,
// perform 100-129 million units and are delivered in 90-140 ms. Pathological
// 4 MiB inputs latch at the bound and fail closed in
// 80-130 ms: the dense anchor field with a 257-byte, 1 KiB, or 64 KiB
// secret, and a base64-alphabet-dense plus text that also decodes hundreds of
// thousands of repeated payloads. The 17-byte anchored-secret field finishes
// at 137,712,732 units without matching any rule and is therefore delivered;
// the public 17-byte construction is rejected by detection in about 27 ms, not
// by the budget.
const dataSecretScanWorkBudget = 192 << 20

// dataSecretChargeChunk bounds how much uncharged work a long scan can
// accumulate before the budget is checked. A passing scan never exceeds the
// budget by more than one chunk, and an exhausted budget aborts promptly
// instead of only at the end of a multi-megabyte pass.
const dataSecretChargeChunk = 64 << 10

// dataSecretBase64SeenBytes bounds the orientation text one guard scan
// remembers for duplicate suppression. A decoded form can share its
// separator-stripped (or stripped-reversed) orientation with the form it was
// decoded from, and a deterministic scan of an identical string at the same or
// shallower nesting depth cannot find anything the earlier scan missed. The
// cap keeps that memory bounded: once it is reached, later orientations are
// simply scanned again rather than remembered.
const dataSecretBase64SeenBytes = 32 << 20

// dataSecretDecodedSeenBytes bounds the decoded payload bytes one guard scan
// remembers for duplicate suppression. A repeated unit in a large diagnostic
// decodes to the same payload at every repetition, and a deterministic scan of
// identical bytes at the same or a shallower nesting depth cannot find anything
// the earlier scan missed. Only payloads small enough for the spelling pipeline
// are remembered, so this cap doubles as a memory bound. Once it is reached,
// later payloads are simply scanned again rather than remembered.
const dataSecretDecodedSeenBytes = 8 << 20

// dataSecretLongNormalizedSeenBytes bounds the normalized long-text forms one
// guard scan remembers for duplicate suppression. A decoded form can normalize
// to the same bytes as its parent (the query decoder's plus-to-space change is
// separator-only for an ASCII text), and a deterministic containment scan of
// identical bytes would repeat a comparison that already found no match. Once
// the cap is reached, later forms are simply scanned again rather than
// remembered.
const dataSecretLongNormalizedSeenBytes = 32 << 20

// dataSecretDecodeFormBytes bounds the decoded forms one candidate may
// remember during the escape/percent walk. The walk is depth-first and keeps a
// best-depth entry for every distinct form it visits; without a byte cap a
// pathological multi-megabyte text with an exponential decode tree could hold
// hundreds of copies before the work budget latches. Reaching the cap means the
// candidate has already produced more decoded text than any benign diagnostic;
// the scan fails closed instead of dropping a branch.
const dataSecretDecodeFormBytes = 64 << 20

// dataSecretDecodeWalkBytes bounds the total decoded-form bytes one shared scan
// revisits through the escape/percent walk. The work budget alone lets a
// deeply branching multi-megabyte pattern perform tens of whole-form passes
// before it latches, which keeps a single pathological diagnostic on the CPU
// long after an ordinary encoded text has been decided. A benign text visits
// its input and a small number of decoded forms; a branching decode bomb
// revisits far more. Reaching the cap latches fail-closed: the walk stops
// instead of silently dropping the branch that would have exceeded it.
const dataSecretDecodeWalkBytes = 32 << 20

// dataSecretDecodedKey distinguishes a payload decoded at the canonical
// alignment, which is followed through the recursive pipeline, from one probed
// at a shifted alignment, which is spelling-scanned only. The two modes perform
// different work, so a completed shifted probe cannot stand in for a canonical
// scan.
type dataSecretDecodedKey struct {
	text    string
	aligned bool
}

// dataSecretScanBudget is the per-scan budget threaded through one guard scan.
// The zero value is usable: a nil or zero budget is replaced by a fresh one.
// work and decodedBytes are cumulative; the token budget is a remaining count.
type dataSecretScanBudget struct {
	work         int64
	base64Tokens int
	decodedBytes int
	exceeded     bool
	// base64Seen records derived base64 orientation strings already fully
	// scanned in this guard scan and the shallowest nesting depth at which each
	// was scanned. An identical orientation reached again at an equal or
	// shallower depth repeats an exhaustive scan that found no match, so the
	// duplicate is skipped. The bytes counter bounds the remembered text.
	base64Seen      map[string]int
	base64SeenBytes int
	// decodedSeen records decoded payloads already fully processed in this
	// guard scan and the shallowest nesting depth at which each was processed.
	// A repeated diagnostic unit decodes to the same payload at every
	// repetition; rescanning identical bytes at an equal or shallower depth
	// repeats an exhaustive scan that found no match. The bytes counter bounds
	// the remembered payloads.
	decodedSeen      map[dataSecretDecodedKey]int
	decodedSeenBytes int
	// longNormalizedSeen records normalized long-text forms already checked, so
	// several decoded forms that normalize identically (the plus decoder's
	// separator-only change is the common case) perform one containment pass.
	longNormalizedSeen      map[string]struct{}
	longNormalizedSeenBytes int
	// decodeFormBytes accounts for the decoded form strings remembered by the
	// escape/percent walk. It bounds the walk's memory independently of the work
	// budget and latches fail-closed at its cap.
	decodeFormBytes int
	// decodeWalkBytes accounts for the whole-form bytes the escape/percent walk
	// revisits. It bounds the actual work a branching decode pattern can cause
	// and latches fail-closed at its cap.
	decodeWalkBytes int64
}

func newDataSecretScanBudget() *dataSecretScanBudget {
	return &dataSecretScanBudget{base64Tokens: dataSecretBase64TokenBudget}
}

// charge adds work units and reports false once the budget is exhausted. The
// first failure latches exceeded so callers can distinguish a budget stop from
// a clean scan in tests without exposing it to the provider.
func (b *dataSecretScanBudget) charge(units int64) bool {
	if b == nil {
		return true
	}
	if units > 0 {
		b.work += units
	}
	if b.work > dataSecretScanWorkBudget {
		b.exceeded = true
		return false
	}
	return true
}

// takeBase64Token consumes one token credit, failing closed at zero.
func (b *dataSecretScanBudget) takeBase64Token() bool {
	if b == nil {
		return true
	}
	if b.base64Tokens <= 0 {
		b.exceeded = true
		return false
	}
	b.base64Tokens--
	return true
}

// takeDecodedBytes consumes decoded-payload bytes, failing closed when the
// cumulative decoded size would exceed the budget.
func (b *dataSecretScanBudget) takeDecodedBytes(n int) bool {
	if b == nil || n <= 0 {
		return true
	}
	if b.decodedBytes+n > dataSecretBase64DecodedByteBudget {
		b.exceeded = true
		return false
	}
	b.decodedBytes += n
	return true
}

// enterBase64Orientation reports whether one base64 orientation must be
// scanned at depth. The first scan of a string remembers its depth; a repeat at
// an equal or shallower depth is skipped because the earlier scan was
// exhaustive (an exhausted budget or token budget stops the whole guard scan
// instead of returning a clean result). A repeat at a deeper depth is scanned,
// because the nesting cap and recursion depth differ. Memory is bounded by
// dataSecretBase64SeenBytes; an orientation that cannot be remembered is simply
// rescanned.
func (b *dataSecretScanBudget) enterBase64Orientation(value string, depth int) bool {
	if b == nil {
		return true
	}
	if seen, ok := b.base64Seen[value]; ok {
		if seen <= depth {
			return false
		}
		b.base64Seen[value] = depth
		return true
	}
	if b.base64SeenBytes+len(value) > dataSecretBase64SeenBytes {
		return true
	}
	if b.base64Seen == nil {
		b.base64Seen = make(map[string]int, 8)
	}
	b.base64Seen[value] = depth
	b.base64SeenBytes += len(value)
	return true
}

// enterDecodedPayload records that one decoded payload is about to be processed
// and reports whether the processing may be skipped. An identical payload
// processed earlier at an equal or shallower base64 depth performed the same
// deterministic comparison against the same guard values, so no later
// occurrence at that depth can change the decision. A deeper occurrence is also
// skippable: it would perform strictly less recursive work. A shallower
// occurrence must be reprocessed because the recursion allowance differs.
func (b *dataSecretScanBudget) enterDecodedPayload(payload string, aligned bool, depth int) bool {
	if b == nil {
		return true
	}
	key := dataSecretDecodedKey{text: payload, aligned: aligned}
	if seen, ok := b.decodedSeen[key]; ok {
		if seen <= depth {
			return false
		}
		b.decodedSeen[key] = depth
		return true
	}
	if b.decodedSeenBytes+len(payload) > dataSecretDecodedSeenBytes {
		return true
	}
	if b.decodedSeen == nil {
		b.decodedSeen = make(map[dataSecretDecodedKey]int, 8)
	}
	b.decodedSeen[key] = depth
	b.decodedSeenBytes += len(payload)
	return true
}

// enterLongNormalized records that one normalized long-text form is about to be
// scanned and reports whether the scan may be skipped because identical bytes
// were already checked in this guard scan. The comparison is deterministic over
// the same needle set, so a repeat cannot change the decision. Memory is
// bounded by dataSecretLongNormalizedSeenBytes; a form that cannot be
// remembered is scanned again rather than skipped.
func (b *dataSecretScanBudget) enterLongNormalized(value string) bool {
	if b == nil {
		return true
	}
	if _, ok := b.longNormalizedSeen[value]; ok {
		return false
	}
	if b.longNormalizedSeenBytes+len(value) > dataSecretLongNormalizedSeenBytes {
		return true
	}
	if b.longNormalizedSeen == nil {
		b.longNormalizedSeen = make(map[string]struct{}, 8)
	}
	b.longNormalizedSeen[value] = struct{}{}
	b.longNormalizedSeenBytes += len(value)
	return true
}

// takeDecodeFormBytes accounts for one decoded form the walk is about to
// remember. Reaching the cap latches exceeded and reports false: the caller
// fails closed instead of dropping a branch it could not scan.
func (b *dataSecretScanBudget) takeDecodeFormBytes(n int) bool {
	if b == nil || n <= 0 {
		return true
	}
	if b.decodeFormBytes+n > dataSecretDecodeFormBytes {
		b.exceeded = true
		return false
	}
	b.decodeFormBytes += n
	return true
}

// takeDecodeWalkBytes accounts for one whole-form pass performed by the
// escape/percent walk. Reaching the cap latches exceeded and reports false: the
// caller fails closed instead of letting a branching decode pattern keep
// revisiting forms until the much larger work budget runs out.
func (b *dataSecretScanBudget) takeDecodeWalkBytes(n int) bool {
	if b == nil || n <= 0 {
		return true
	}
	if b.decodeWalkBytes+int64(n) > dataSecretDecodeWalkBytes {
		b.exceeded = true
		return false
	}
	b.decodeWalkBytes += int64(n)
	return true
}

// chargeChunked charges units of work in bounded pieces. It is used before a
// materialization whose size is known and inside long scans: a budget stop can
// then abort before the whole stage runs and cannot overshoot the budget by
// more than one dataSecretChargeChunk.
func (b *dataSecretScanBudget) chargeChunked(units int64) bool {
	if b == nil || units <= 0 {
		return true
	}
	for units > 0 {
		piece := units
		if piece > dataSecretChargeChunk {
			piece = dataSecretChargeChunk
		}
		if !b.charge(piece) {
			return false
		}
		units -= piece
	}
	return true
}

// dataSecretSubsequenceBytes bounds the full ordered-subsequence rule. The rule
// exists to catch a secret interleaved with junk in a short message, identity,
// or status text, where an accidental match is unlikely and the text is cheap
// to scan exhaustively. Above this bound the scan is linear and normalized:
// every decoded form is reduced to a fixed set of whole-text normalized
// spellings (case-folded, Unicode-folded, separator-stripped, and their
// reversals) and each is checked for contiguous containment of the secret's
// threshold fragment. Letter-interleaved echoes in long text are a documented
// residual rather than an O(n*m) subsequence scan: the provider already owns
// the secret and can return it verbatim through opaque row or document payloads,
// so a multi-megabyte interleaving scan buys no trust while its false-positive
// and CPU cost is unbounded. Separator-interleaved echoes remain caught because
// separator stripping is part of the normalized form.
const dataSecretSubsequenceBytes = 4096

// dataSecretGuard holds the resolved plaintext secret values of one open
// connection or profile test. A provider must never echo them. Every
// provider-supplied message and every test result is checked against the full
// text before any truncation. The host fails closed with a protocol error
// instead of redacting: a redacted value still reveals the secret's length and
// prefix, and the provider is untrusted.
type dataSecretGuard struct {
	// values are the secrets exactly as resolved. They back the raw byte
	// containment check containsSecret, which is applied to decoded base64
	// payloads whose bytes need not be valid text.
	values []string
	// variants expands every value into the spellings a provider can return:
	// separator-stripped, reversed, both, case-folded, Unicode-folded, and each
	// combination. Base64 spellings of the raw value are included so an encoded
	// echo is caught even when its token is too large to decode. The set is
	// computed once when the guard is built and drives the short-text rules.
	variants []string
	// longNeedles are the threshold fragments of every normalized spelling,
	// keyed by a rare byte for one linear multi-needle containment pass per
	// normalized long-text form. A fragment at or above dataSecretPrefixBytes is
	// compared whole, so one check covers the full-value and threshold-prefix
	// rules; the folded forms of uppercase-only base64 spellings are included
	// here as the case-insensitive upper proxies.
	longNeedles dataTextLongMatchSet
	// longBase64Needles are the threshold fragments of the raw base64 spellings
	// (forward and reversed) compared case-sensitively against the unnormalized
	// long-text forms. Base64 is case-sensitive, so these must never be compared
	// against the case-folded form.
	longBase64Needles dataTextLongMatchSet
	// longRawNeedles are the raw threshold fragments of the resolved values,
	// compared byte-exactly with decoded base64 payloads and other binary forms.
	longRawNeedles dataTextLongMatchSet
	// upperFullProxies compensate for skipping the whole-string upper-case
	// spelling family on a long ASCII candidate. A base64 spelling is not
	// closed under case folding like a dataTextSpellings form, so matching the
	// folded spelling against upperFullProxies is not the same comparison as
	// matching the variant directly. For a variant with no lowercase byte the
	// two are equivalent: upper(s) contains v exactly when lower(s) contains
	// lower(v), for containment, threshold prefix, and ordered subsequence
	// alike. Proxies are only consulted on the ASCII long path, where the upper
	// family is skipped; short text and non-ASCII text keep the literal
	// spellings.
	upperFullProxies []string
	// upperPrefixProxies cover the one asymmetric case: a variant whose
	// threshold prefix has no lowercase byte but whose remaining bytes do. The
	// upper-case spelling's prefix rule can match, while its full-value rules
	// cannot because the upper spelling contains no lowercase byte. Only the
	// contiguous prefix proxy is kept, never the full value, so the ordered
	// subsequence rule is not widened.
	upperPrefixProxies []string
	// spaceFreeVariants reports that no compared value contains a space. The
	// query decoder's plus form changes only "+" to space, so it can create the
	// only non-separator-visible difference: a value containing a space can
	// match at a position that held "+" in the parent. For a space-free value
	// every spelling-rule match in the plus form exists unchanged in the parent
	// ("+" cannot match the plus form at all, and every other byte is
	// identical), so echoesDepth skips the redundant spelling scan. The base64
	// stage still runs, because "+" is a base64 alphabet character.
	spaceFreeVariants bool
}

// dataTextSecretPrefix returns the threshold fragment of one variant: its first
// dataSecretFragmentBytes bytes, or the whole variant when it is shorter. The
// contiguous rule matches exactly this prefix, so it is the correct unit for
// the byte-count and presence prefilters.
func dataTextSecretPrefix(value string) string {
	if len(value) > dataSecretFragmentBytes {
		return value[:dataSecretFragmentBytes]
	}
	return value
}

// dataTextHasLowerASCII reports whether text contains a lowercase ASCII letter.
func dataTextHasLowerASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= 'a' && text[i] <= 'z' {
			return true
		}
	}
	return false
}

// dataTextCountsHaveSeparators reports whether the counted text contains any
// separator-class byte. The counts of a stripped spelling have every separator
// byte zeroed, so this also tells the long spelling path whether the walk will
// emit a distinct stripped form for the spelling.
func dataTextCountsHaveSeparators(counts *[256]int32) bool {
	if counts == nil {
		return false
	}
	for i := 0; i < len(dataSecretSeparatorBytes); i++ {
		if counts[dataSecretSeparatorBytes[i]] > 0 {
			return true
		}
	}
	return false
}

// newDataSecretGuard returns nil when there is nothing to guard, so callers
// can use the zero value safely.
func newDataSecretGuard(secrets map[string]string) *dataSecretGuard {
	values := make([]string, 0, len(secrets))
	variants := make([]string, 0, len(secrets)*16)
	longNeedles := make([]string, 0, len(secrets)*16)
	base64Needles := make([]string, 0, len(secrets)*8)
	rawNeedles := make([]string, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets)*16)
	add := func(variant string) {
		if variant == "" {
			return
		}
		if _, ok := seen[variant]; ok {
			return
		}
		seen[variant] = struct{}{}
		variants = append(variants, variant)
	}
	for _, value := range secrets {
		if value == "" {
			continue
		}
		values = append(values, value)
		rawNeedles = append(rawNeedles, dataTextSecretPrefix(value))
		for _, variant := range dataTextSpellings(value) {
			add(variant)
			longNeedles = append(longNeedles, dataTextLongNeedlesOf(variant)...)
		}
		for _, encoded := range dataTextBase64Encodings(value) {
			add(encoded)
			// The long scan is linear and normalized. A base64 spelling is not
			// closed under case folding, so its reversals are added here: the
			// reversed needle compared with the forward form is exactly the
			// original spelling compared with a reversed form, and keeping both
			// directions in the needle set replaces materializing every reversed
			// multi-megabyte form. Each spelling contributes both its own
			// threshold fragment and the reversed fragment, so a reversed
			// threshold prefix is still caught without building the reversed
			// multi-megabyte candidate form.
			reversed := dataTextReversed(encoded)
			add(reversed)
			for _, spelling := range []string{encoded, reversed} {
				prefix := dataTextSecretPrefix(spelling)
				base64Needles = append(base64Needles, prefix,
					dataTextByteReversed(prefix))
				// The upper-case proxy: a base64 spelling with no lowercase byte
				// can match an upper-cased candidate through the case-folded form.
				// Only the threshold fragment matters for contiguous containment,
				// which is the long-text rule.
				if dataTextIsASCII(prefix) && !dataTextHasLowerASCII(prefix) {
					if folded := dataTextCaseFolded(prefix); folded != prefix {
						longNeedles = append(longNeedles, folded,
							dataTextByteReversed(folded))
					}
				}
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	// Build the upper-case compensation proxies from the finished variant set.
	// A variant with no lowercase byte is case-insensitive in the upper
	// direction: it can match an upper-cased spelling, so its lower-case form
	// is added as a full proxy. A variant that only has a lowercase-free
	// threshold prefix gets the contiguous prefix proxy instead. A variant
	// already present in the set (for example the folded spelling of the secret
	// itself) needs no proxy.
	rawVariants := append([]string(nil), variants...)
	upperFull := make([]string, 0, 4)
	upperPrefix := make([]string, 0, 4)
	seenProxy := make(map[string]struct{}, 8)
	addUpperFull := func(proxy string) {
		if proxy == "" {
			return
		}
		if _, ok := seen[proxy]; ok {
			return
		}
		if _, ok := seenProxy[proxy]; ok {
			return
		}
		seenProxy[proxy] = struct{}{}
		upperFull = append(upperFull, proxy)
	}
	addUpperPrefix := func(proxy string) {
		if proxy == "" {
			return
		}
		if _, ok := seen[proxy]; ok {
			return
		}
		if _, ok := seenProxy[proxy]; ok {
			return
		}
		seenProxy[proxy] = struct{}{}
		upperPrefix = append(upperPrefix, proxy)
	}
	for _, variant := range rawVariants {
		prefix := dataTextSecretPrefix(variant)
		if dataTextIsASCII(variant) && !dataTextHasLowerASCII(variant) {
			// The upper spelling can match the full variant. Digits and
			// punctuation fold to themselves, so only a lowercase copy that
			// differs (an uppercase letter is present) is needed.
			if folded := dataTextCaseFolded(variant); folded != variant {
				addUpperFull(folded)
			}
			continue
		}
		if dataTextIsASCII(prefix) && !dataTextHasLowerASCII(prefix) {
			// Only the contiguous prefix rule can match the upper spelling;
			// the full value cannot because it contains a lowercase (or
			// non-ASCII) byte.
			if folded := dataTextCaseFolded(prefix); folded != prefix {
				addUpperPrefix(folded)
			}
		}
	}
	// The short-text path scans the original case-preserving spelling with the
	// full variant set; the long path uses the normalized needle set built above.

	spaceFree := true
	for _, group := range [][]string{variants, upperFull, upperPrefix} {
		for _, value := range group {
			if strings.IndexByte(value, ' ') >= 0 {
				spaceFree = false
				break
			}
		}
		if !spaceFree {
			break
		}
	}
	return &dataSecretGuard{
		values:             values,
		variants:           variants,
		longNeedles:        newDataTextLongMatchSet(longNeedles),
		longBase64Needles:  newDataTextLongMatchSet(base64Needles),
		longRawNeedles:     newDataTextLongMatchSet(rawNeedles),
		upperFullProxies:   upperFull,
		upperPrefixProxies: upperPrefix,
		spaceFreeVariants:  spaceFree,
	}
}

// echoes reports whether text carries a resolved secret. The text is checked
// as received, after every layer of Go/JSON escape and percent decoding, and
// in each normalized spelling: separator-class bytes removed, reversed, both,
// case-folded, Unicode-compatibility-folded, decomposed without combining
// marks, the explicit ligature/stroke foldings, and each combination. A host
// message that quotes guest text with %q, a provider that pre-escapes,
// percent-encodes, base64-encodes, re-cases, reverses, or separator-interleaves
// a value can all hide a secret in one of those forms. Each form is checked
// for a full secret or a threshold fragment as a contiguous run and, for
// bounded text, for the complete secret as an ordered subsequence. Short
// secrets are included so a dotted spelling of a short value cannot
// reconstruct it.
//
// Callers pass the exact text they will return or display (a bounded message,
// a sanitized identity, a closed verdict, plan text, a summary message), so
// the work stays proportional to the returned text rather than an arbitrary
// provider-supplied value.
//
// A true result means "fail closed": it covers both an observed echo and an
// exhausted scan budget. Callers map it to the same constant protocol error, so
// a provider cannot tell an exhausted guard from a detected echo and no
// candidate text ever reaches the caller.
func (g *dataSecretGuard) echoes(text string) bool {
	if g == nil || text == "" {
		return false
	}
	return g.echoesWithin(text, newDataSecretScanBudget())
}

// echoesWithin scans text with a caller-supplied budget. A single budget spans
// every field of one result or one profile test, so a provider cannot multiply
// the work by spreading text across many small diagnostic fields.
func (g *dataSecretGuard) echoesWithin(text string, budget *dataSecretScanBudget) bool {
	if g == nil || text == "" {
		return false
	}
	if budget == nil {
		budget = newDataSecretScanBudget()
	}
	return g.echoesDepth(text, 0, true, budget)
}

// echoesDepth scans one text through decoding, spelling, and base64 stages.
// base64Depth bounds nested base64 only. Escape and percent layers are walked
// depth-first inside walkDataTextDecodings, whose depth budget is independent
// of the base64 token budget, so neither stage can starve the other. Each
// intermediate decoded form is charged and scanned as soon as it is produced.
// subsequence enables the ordered-subsequence rule for short provider-visible
// text and is ignored above dataSecretSubsequenceBytes, where the linear
// normalized-containment rule applies instead.
//
// Short text keeps the exhaustive spelling and full-subsequence rules because
// it is cheap and the extra rules catch interleavings that matter for status
// and identity fields. Long text uses the linear scan in echoesLongWithin.
func (g *dataSecretGuard) echoesDepth(text string, base64Depth int,
	subsequence bool, budget *dataSecretScanBudget) bool {

	if g == nil || text == "" || base64Depth > dataSecretBase64Depth {
		return false
	}
	if len(text) > dataSecretSubsequenceBytes {
		return g.echoesLongWithin(text, base64Depth, budget)
	}
	return walkDataTextDecodingsWithinForms(text, budget, func(form dataTextDecodedForm) bool {
		// The query decoder's plus form changes only "+" to space. When no
		// compared value contains a space, that form cannot add a spelling-rule
		// match: a space-free value cannot use a new space, a value containing
		// "+" cannot match a form with no "+", and every other byte is
		// identical, so the parent's spelling scan already decided every
		// spelling rule. The base64 stage still runs, because base64 extraction
		// is affected by those bytes ("+" is an alphabet character).
		if form.separatorEquivalent && g.spaceFreeVariants {
			return g.echoesBase64(form.text, base64Depth, budget)
		}
		if g.echoesSpellings(form.text, subsequence, budget) {
			return true
		}
		return g.echoesBase64(form.text, base64Depth, budget)
	})
}

// echoesSpellings applies every normalized spelling of one decoded candidate.
// A candidate whose folded bytes cannot contain any secret variant is skipped
// before a single spelling is materialized: folding, separator removal, and
// reversal never introduce a byte outside the per-rune folded expansion, so no
// spelling can match a variant that needs a byte the expansion does not have.
// This keeps long benign plans and summaries cheap without weakening any rule,
// and it is why a benign text that merely contains every secret byte is not
// rejected for it.
//
// The remaining spellings are streamed, not built all at once. Each folding and
// each normalized spelling charges the bytes it copies before it is built, and
// an exhausted budget aborts the stream mid-way, so a multi-megabyte non-ASCII
// construction cannot materialize every spelling before the first charge bites.
func (g *dataSecretGuard) echoesSpellings(text string, subsequence bool,
	budget *dataSecretScanBudget) bool {

	if g == nil || text == "" {
		return false
	}
	if budget == nil {
		budget = newDataSecretScanBudget()
	}
	if len(text) <= dataSecretSubsequenceBytes {
		// The cheap rules are scoped to a small text, but the pass is still
		// charged so an already exhausted budget fails closed here as well.
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		for _, spelling := range dataTextSpellings(text) {
			if g.echoesShortCandidate(spelling, subsequence) {
				return true
			}
		}
		return false
	}
	// Above the short-text bound the scan is linear. The normalized forms and
	// the multi-needle matcher are documented on echoesLongWithin.
	return g.echoesLongWithin(text, 0, budget)
}

// echoesLongWithin scans one candidate above dataSecretSubsequenceBytes with
// the linear normalized-containment rule. Every escape/percent form the
// candidate can decode to is visited by the charged depth-first walk; each form
// is reduced to a small fixed set of whole-text normalizations and checked for
// contiguous containment of a normalized secret fragment. There is no anchor
// search, no sliding window, and no ordered-subsequence rule: those make the
// work quadratic in the candidate length and turn a benign multi-megabyte text
// that happens to contain every secret byte into a false positive. The
// documented residual is a secret interleaved with arbitrary letters in long
// text; separator-class interleaving is still caught because separator
// stripping is one of the normalizations.
func (g *dataSecretGuard) echoesLongWithin(text string, base64Depth int,
	budget *dataSecretScanBudget) bool {

	if g == nil || text == "" || base64Depth > dataSecretBase64Depth {
		return false
	}
	if budget == nil {
		budget = newDataSecretScanBudget()
	}
	return walkDataTextLongDecodingsWithinForms(text, budget,
		func(form dataTextDecodedForm) bool {
			return g.echoesLongForm(form, base64Depth, budget)
		})
}

// echoesLongForm applies the fixed long-text rules to one decoded form: the
// case-sensitive base64 spellings against the form and its separator-stripped
// copy, the normalized spellings against the folded/stripped branch, and the
// base64 token walk for encoded payloads. The separator-only plus form skips
// the spelling and base64-needle comparisons when no compared value contains a
// space: its normalized form is identical to the parent's, and a base64 needle
// is alphabet-only while the change turns an alphabet byte into a separator, so
// the parent's comparison is a superset. The token walk still runs because the
// changed byte does change token boundaries.
func (g *dataSecretGuard) echoesLongForm(form dataTextDecodedForm, base64Depth int,
	budget *dataSecretScanBudget) bool {

	text := form.text
	if text == "" {
		return false
	}
	// The query decoder's plus form changes only '+' (itself a separator byte)
	// to a space. When no compared value contains a separator byte, its stripped
	// and normalized strings are byte-identical to its parent's, which the
	// depth-first walk visited first, and its base64 runs are subsets of the
	// parent's runs or of the shared stripped orientation, whose four group
	// alignments cover any start offset. The whole form is therefore redundant
	// and is skipped; this is what keeps a four-megabyte diagnostic dense in
	// plus signs from paying the entire extraction pipeline twice.
	if form.separatorEquivalent && g.spaceFreeVariants {
		return false
	}
	stripped, ascii, counts, ok := dataTextLongStripCounts(text, budget)
	if !ok {
		return true
	}
	if g.echoesLongSpellings(text, stripped, ascii, &counts, false, budget) {
		return true
	}
	return g.echoesLongBase64(text, stripped, base64Depth, budget)
}

// echoesLongSpellings applies the normalized and case-sensitive spelling rules
// to one decoded form. The separator-only plus form skips the whole spelling
// stage when no compared value contains a space: "+" is itself in the separator
// class, so that form's stripped and normalized strings are byte-identical to
// its parent's (which the depth-first walk visited first), and a base64 needle
// is alphabet-only while the change turns an alphabet byte into a separator, so
// the parent's case-sensitive comparison is a superset. The base64 token walk
// still runs for the plus form because the changed byte does change token
// boundaries.
//
// counts, when non-nil, are the exact ASCII byte counts of text from the strip
// pass. A needle is a short byte string, so it can occur in a spelling only
// when every byte it needs, with its multiplicity, occurs in that spelling's
// bytes. The count prefilter therefore skips a needle match, and the normalize
// build that feeds it, whenever the corresponding byte multiset cannot carry
// any needle. A multi-encoding diagnostic legitimately decodes to several
// distinct forms; without this prefilter each form pays for the full normalized
// and base64-needle pipelines even when it cannot contain one of the short
// fragments.
func (g *dataSecretGuard) echoesLongSpellings(text, stripped string, ascii bool,
	counts *[256]int32, separatorEquivalent bool, budget *dataSecretScanBudget) bool {

	if separatorEquivalent && g.spaceFreeVariants {
		return false
	}
	base64Possible := true
	normalizedPossible := true
	if ascii && counts != nil {
		raw := dataTextLongCountsUint8(counts)
		strippedCounts := dataTextCountsWithoutSeparators(counts)
		strippedBytes := dataTextLongCountsUint8(&strippedCounts)
		base64Possible = g.longBase64Needles.anyCouldFit(&raw) ||
			(stripped != text && g.longBase64Needles.anyCouldFit(&strippedBytes))
		folded := dataTextLongFoldCounts(counts)
		normalizedPossible = g.longNeedles.anyCouldFit(&folded)
		if !base64Possible && !normalizedPossible {
			return false
		}
	}
	if base64Possible {
		if g.longBase64Needles.matches(text, budget) {
			return true
		}
		if stripped != text && g.longBase64Needles.matches(stripped, budget) {
			return true
		}
	}
	if !normalizedPossible {
		return false
	}
	normalized, ok := dataTextLongNormalizeWithin(text, stripped, ascii, budget)
	if !ok {
		return true
	}
	if budget.enterLongNormalized(normalized) {
		if g.longNeedles.matches(normalized, budget) {
			return true
		}
	}
	return false
}

// dataTextLongCountsUint8 saturates raw byte counts for the multi-needle count
// prefilter, whose multiplicities are at most the eight-byte threshold
// fragment. Saturation cannot turn a possible match into an impossible one.
func dataTextLongCountsUint8(counts *[256]int32) [256]uint8 {
	var saturated [256]uint8
	for i, n := range counts {
		if n > 255 {
			n = 255
		}
		saturated[i] = uint8(n)
	}
	return saturated
}

// dataTextLongFoldCounts converts exact raw ASCII byte counts into the counts a
// normalized long-text form can carry: both cases of every letter are marked
// because the fold maps one to the other, and separator bytes are kept because
// the count filter is only a necessary condition. This is the uint8-count
// equivalent of dataTextAddFoldCounts for pure ASCII text.
func dataTextLongFoldCounts(counts *[256]int32) [256]uint8 {
	folded := dataTextLongCountsUint8(counts)
	for c := byte('A'); c <= 'Z'; c++ {
		lower := c - 'A' + 'a'
		sum := int(folded[c]) + int(folded[lower])
		if sum > 255 {
			sum = 255
		}
		folded[lower] = uint8(sum)
	}
	return folded
}

// dataTextLongNormalizeWithin builds the normalized long-text forms and charges
// every pass. stripped is the separator-stripped candidate already computed by
// the caller, and ascii reports that the candidate is pure ASCII. For ASCII the
// normalized form is exactly the case-folded stripped form, so no Unicode fold
// is needed. Non-ASCII text pays the case fold, compatibility fold, explicit
// substitution pass, and separator strip it actually performs.
func dataTextLongNormalizeWithin(text, stripped string, ascii bool,
	budget *dataSecretScanBudget) (string, bool) {

	if ascii {
		if !budget.chargeChunked(int64(len(stripped))) {
			return "", false
		}
		return dataTextLongLowerASCII(stripped), true
	}
	if !budget.chargeChunked(int64(len(text))) {
		return "", false
	}
	folded := dataTextCaseFolded(text)
	if !budget.chargeChunked(int64(len(folded)) * 2) {
		return "", false
	}
	folded = foldDataTextUnicode(folded)
	if !budget.chargeChunked(int64(len(folded))) {
		return "", false
	}
	special := foldDataTextSpecial(folded)
	normalized, _, ok := dataTextLongStrip(special, budget)
	if !ok {
		return "", false
	}
	return normalized, true
}

// dataTextLongStrip removes separator-class bytes in one charged pass and
// reports whether the candidate is pure ASCII. The separator check is a table
// lookup, so a multi-megabyte scan stays linear in the candidate length. The
// detection and copy are fused: a text with no separators returns the original
// string and discards the scratch buffer, so the common benign case pays one
// charged pass rather than a probe followed by a second copy pass.
func dataTextLongStrip(text string, budget *dataSecretScanBudget) (string, bool, bool) {
	stripped, ascii, _, ok := dataTextLongStripCounts(text, budget)
	return stripped, ascii, ok
}

// dataTextLongStripCounts is dataTextLongStrip with the exact byte counts of
// the candidate from the same pass. The counts feed the long-text needle count
// prefilter, so a decoded form that cannot carry any needle skips the
// normalized and base64-needle pipelines without a second count pass.
func dataTextLongStripCounts(text string, budget *dataSecretScanBudget) (
	string, bool, [256]int32, bool) {

	var counts [256]int32
	if text == "" {
		return "", true, counts, true
	}
	if !budget.chargeChunked(int64(len(text))) {
		return "", false, counts, false
	}
	ascii := true
	separators := false
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		counts[c]++
		if c >= utf8.RuneSelf {
			ascii = false
		}
		if dataTextSeparatorTable[c] {
			separators = true
			continue
		}
		out = append(out, c)
	}
	if !separators {
		return text, ascii, counts, true
	}
	return string(out), ascii, counts, true
}

// dataTextLongLowerASCII lower-cases a pure ASCII candidate in one pass. The
// ASCII-only fold is an exact byte homomorphism, which is why the long path can
// fuse it with the separator strip without changing any comparison.
func dataTextLongLowerASCII(text string) string {
	changed := false
	out := make([]byte, len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
			changed = true
		}
		out[i] = c
	}
	if !changed {
		return text
	}
	return string(out)
}

// dataTextLongNormalizeValue normalizes one guard variant for the long-text
// needle set. Guard variants are bounded by the resolved secret size, so this
// construction-time pass is uncharged and linear.
func dataTextLongNormalizeValue(text string) string {
	if text == "" {
		return ""
	}
	if dataTextIsASCII(text) {
		return stripDataSecretSeparators(dataTextCaseFolded(text))
	}
	folded := dataTextCaseFolded(text)
	folded = foldDataTextUnicode(folded)
	folded = foldDataTextSpecial(folded)
	return stripDataSecretSeparators(folded)
}

// dataTextLongNeedlesOf returns the contiguous needles of one normalized
// variant: its threshold fragment and the reversed forms of that fragment. A
// fragment at or above dataSecretFragmentBytes is the whole contiguous unit: a
// full recorded secret contains its threshold prefix, so one containment check
// decides the full-value and threshold-prefix rules together. Reversing the
// needle rather than materializing reversed multi-megabyte candidate forms is
// exact for ASCII; the byte-reversed copy covers invalid UTF-8 and the
// rune-reversed copy covers the rune reversal the short-text spelling walk
// emits for valid UTF-8.
func dataTextLongNeedlesOf(variant string) []string {
	normalized := dataTextLongNormalizeValue(variant)
	if normalized == "" {
		return nil
	}
	needle := dataTextSecretPrefix(normalized)
	needles := []string{needle}
	if reversed := dataTextByteReversed(needle); reversed != needle {
		needles = append(needles, reversed)
	}
	if reversed := dataTextReversed(needle); reversed != needle {
		needles = append(needles, reversed)
	}
	return needles
}

// dataTextLongNeedle is one needle anchored at a rarely occurring byte.
// offset is the anchor's position within value; the matcher reads the candidate
// at position-offset and compares the whole needle only when the anchor byte
// matches. Choosing the byte with the lowest multiplicity inside the needle
// keeps an ordinary scan from paying a full comparison at every occurrence of a
// common first letter, without needing a per-candidate frequency table.
type dataTextLongNeedle struct {
	value  string
	offset int
	guard  int
	// need/mult are the distinct bytes of value with their multiplicities, used
	// by the small-payload count prefilter.
	need      [dataSecretFragmentBytes]byte
	mult      [dataSecretFragmentBytes]uint8
	needCount int
}

// dataTextLongMatchSet is the multi-needle matcher for one long-text channel.
// byPivot maps an anchor byte to the needles that contain it at their chosen
// offset. The zero value is usable and matches nothing.
type dataTextLongMatchSet struct {
	byPivot [256][]dataTextLongNeedle
	pivots  []byte
}

// newDataTextLongMatchSet builds a deduplicated anchored needle set. The guard
// construction passes are bounded by the resolved secrets, not by candidate
// text, so this work is not charged.
func newDataTextLongMatchSet(values []string) dataTextLongMatchSet {
	var set dataTextLongMatchSet
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		var counts [256]int
		for i := 0; i < len(value); i++ {
			counts[value[i]]++
		}
		pivot, best := 0, -1
		for i := 0; i < len(value); i++ {
			if best < 0 || counts[value[i]] < best {
				pivot, best = i, counts[value[i]]
			}
		}
		// A second, independently chosen rare byte sharply reduces how often a
		// pivot hit pays for a full candidate comparison on ordinary prose. The
		// guard probe is charged and the full compare only runs when it matches.
		guard, guardBest := 0, -1
		for i := 0; i < len(value); i++ {
			if i == pivot {
				continue
			}
			if guardBest < 0 || counts[value[i]] < guardBest {
				guard, guardBest = i, counts[value[i]]
			}
		}
		needle := dataTextLongNeedle{value: value, offset: pivot, guard: guard}
		for i := 0; i < len(value); i++ {
			c := value[i]
			found := false
			for j := 0; j < needle.needCount; j++ {
				if needle.need[j] == c {
					needle.mult[j]++
					found = true
					break
				}
			}
			if !found && needle.needCount < len(needle.need) {
				needle.need[needle.needCount] = c
				needle.mult[needle.needCount] = 1
				needle.needCount++
			}
		}
		if len(set.byPivot[value[pivot]]) == 0 {
			set.pivots = append(set.pivots, value[pivot])
		}
		set.byPivot[value[pivot]] = append(set.byPivot[value[pivot]], needle)
	}
	return set
}

// matches reports whether text contains any needle contiguously. The text pass
// and every candidate comparison are charged before they perform work, and an
// exhausted budget returns true so the caller fails closed: a needle that was
// never compared might have matched.
func (s *dataTextLongMatchSet) matches(text string, budget *dataSecretScanBudget) bool {
	if len(text) == 0 || s == nil {
		return false
	}
	pending := int64(0)
	flush := func() bool {
		chunk := pending
		pending = 0
		return budget.chargeChunked(chunk)
	}
	for i := 0; i < len(text); i++ {
		pending++
		if pending >= dataSecretChargeChunk && !flush() {
			return true
		}
		for _, needle := range s.byPivot[text[i]] {
			start := i - needle.offset
			if start < 0 || start+len(needle.value) > len(text) {
				continue
			}
			// The pivot byte already matched. Probe the second rare byte before
			// paying for the whole comparison; the probe is charged as the byte
			// it reads.
			pending++
			if pending >= dataSecretChargeChunk && !flush() {
				return true
			}
			if text[start+needle.guard] != needle.value[needle.guard] {
				continue
			}
			pending += int64(len(needle.value))
			if pending >= dataSecretChargeChunk && !flush() {
				return true
			}
			if text[start:start+len(needle.value)] == needle.value {
				return true
			}
		}
	}
	if !budget.chargeChunked(pending) {
		return true
	}
	return false
}

// dataTextLongPrefilterBytes bounds the decoded payloads for which the count
// prefilter is cheaper than the normalized spelling passes. Above it almost
// every byte occurs and the prefilter cannot reject anything, so the caller
// skips it.
const dataTextLongPrefilterBytes = 512

// anyCouldFit reports whether any needle's multiset could occur in a payload
// with the supplied folded byte counts. It is a necessary-condition filter:
// false means no needle can be a contiguous substring of any spelling (case
// folds, separator stripping, and reversal only permute or remove bytes, never
// create a byte), so the caller can skip the spelling passes safely.
func (s *dataTextLongMatchSet) anyCouldFit(counts *[256]uint8) bool {
	for _, pivot := range s.pivots {
		for _, needle := range s.byPivot[pivot] {
			fits := true
			for i := 0; i < needle.needCount; i++ {
				if counts[needle.need[i]] < needle.mult[i] {
					fits = false
					break
				}
			}
			if fits {
				return true
			}
		}
	}
	return false
}

// dataTextAddFoldCounts accumulates the ASCII byte counts a payload contributes
// after the normalization pipeline (case fold, unicode compatibility fold,
// special substitutions). It is used only as a necessary-condition filter: it
// counts every byte a normalized form could contain, including separator bytes
// that stripping later removes, so a false result is conclusive. ASCII letters
// are counted in both cases so a lower-case normalized needle still fits an
// upper-case payload spelling. Invalid UTF-8 bytes are copied through by every
// fold and contribute no ASCII.
func dataTextAddFoldCounts(text string, counts *[256]uint8) {
	add := func(c byte) {
		if counts[c] < 255 {
			counts[c]++
		}
		if c >= 'A' && c <= 'Z' && counts[c+'a'-'A'] < 255 {
			counts[c+'a'-'A']++
		}
	}
	for i := 0; i < len(text); {
		c := text[i]
		// Every literal byte can reach a spelling: raw payloads are compared
		// byte-exactly, invalid UTF-8 bytes are preserved by every fold, and an
		// unmapped non-ASCII rune keeps its own bytes. Counting them is a
		// necessary condition for every raw needle and cannot over-reject.
		add(c)
		if c < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		for j := 1; j < size; j++ {
			add(text[i+j])
		}
		lowered := unicode.ToLower(r)
		if !unicode.Is(unicode.Mn, lowered) {
			// The normalized form carries the folded rune's own encoding, which
			// may differ from the input bytes for a case-mapped letter, and the
			// fold table may emit non-ASCII bytes (fraction slashes, middle
			// dots). Count every output byte, not only the ASCII ones.
			var folded string
			if mapped, ok := dataTextFoldRunes[lowered]; ok {
				folded = foldDataTextSpecial(mapped)
			} else {
				folded = foldDataTextSpecial(string(lowered))
			}
			for j := 0; j < len(folded); j++ {
				add(folded[j])
			}
		}
		i += size
	}
}

// longPayloadCouldCarry is the count prefilter for one small decoded payload.
// found reports whether a needle could still occur, and ok is false only when
// the budget stopped the scan, in which case the caller fails closed.
func (g *dataSecretGuard) longPayloadCouldCarry(payload string,
	budget *dataSecretScanBudget) (found, ok bool) {

	if payload == "" {
		return false, true
	}
	if len(payload) > dataTextLongPrefilterBytes {
		return true, true
	}
	if !budget.chargeChunked(int64(len(payload))) {
		return true, false
	}
	var counts [256]uint8
	dataTextAddFoldCounts(payload, &counts)
	if g.longRawNeedles.anyCouldFit(&counts) ||
		g.longNeedles.anyCouldFit(&counts) ||
		g.longBase64Needles.anyCouldFit(&counts) {
		return true, true
	}
	return false, true
}

// echoesLongBase64 extracts and decodes base64 tokens from the long-text
// orientations that can carry an encoded secret: the form itself, its
// separator-stripped copy, the dot-only orientation when a URL-safe token was
// dotted, and the short-gap join when non-separator bytes split a token. Every
// orientation is charged before it is built, each token decode is charged, and
// every decoded payload is compared byte-exactly with the raw secret fragments.
// An exhausted budget returns true (fail closed), never a silent skip.
func (g *dataSecretGuard) echoesLongBase64(text, stripped string,
	base64Depth int, budget *dataSecretScanBudget) bool {

	if g == nil || text == "" {
		return false
	}
	if budget == nil {
		budget = newDataSecretScanBudget()
	}
	minBytes := g.minSecretBytes()
	if minBytes <= 0 {
		return false
	}
	// The dot-only orientation matters when a dotted URL-safe token contains
	// "-" or "_": the general separator strip would remove those data bytes, but
	// the dots-only copy keeps them. When the text has neither, the stripped
	// form already joins the same alphabet runs.
	dotMode := strings.IndexByte(text, '.') >= 0 &&
		(strings.IndexByte(text, '-') >= 0 || strings.IndexByte(text, '_') >= 0)
	// One charged pass covers the plain orientation, the short-gap probe (a
	// short run of non-separator bytes can split one token), and the dot-only
	// copy. Separate probe passes made punctuation-heavy diagnostics pay several
	// full scans before decoding a single token.
	if !budget.chargeChunked(int64(len(text))) {
		return true
	}
	match, hasGap := g.echoesLongBase64Text(text, dotMode, base64Depth, minBytes, budget)
	if match {
		return true
	}
	if stripped != text && budget.enterBase64Orientation(stripped, base64Depth) {
		if !budget.chargeChunked(int64(len(stripped))) {
			return true
		}
		if g.echoesLongBase64Orientation(stripped, base64Depth, minBytes, budget) {
			return true
		}
	}
	if hasGap {
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		joined := joinDataTextBase64Gaps(text, dataSecretBase64GapBytes)
		if joined != "" && joined != text && joined != stripped &&
			budget.enterBase64Orientation(joined, base64Depth) {
			if !budget.chargeChunked(int64(len(joined))) {
				return true
			}
			if g.echoesLongBase64Orientation(joined, base64Depth, minBytes, budget) {
				return true
			}
		}
	}
	return false
}

// echoesLongBase64Text walks one form once for the plain orientation's maximal
// alphabet runs, the short-gap probe, and (when dotMode is set) the dotted
// orientation's joined runs. match reports a match or an exhausted budget; the
// caller fails closed on either. hasGap reports whether a short run of
// non-separator bytes may have split a token. A dot-delimited run is decoded
// twice, once per orientation, matching the separate passes it replaces.
func (g *dataSecretGuard) echoesLongBase64Text(text string, dotMode bool,
	base64Depth, minBytes int, budget *dataSecretScanBudget) (match, hasGap bool) {

	for i := 0; i < len(text); {
		c := text[i]
		if !dataTextBase64Byte(c) && !(dotMode && c == '.') {
			start := i
			nonSeparator := false
			for i < len(text) && !dataTextBase64Byte(text[i]) &&
				!(dotMode && text[i] == '.') {
				if !dataTextSeparatorTable[text[i]] {
					nonSeparator = true
				}
				i++
			}
			if nonSeparator && i-start <= dataSecretBase64GapBytes {
				hasGap = true
			}
			continue
		}
		start := i
		dots := false
		for i < len(text) && (dataTextBase64Byte(text[i]) ||
			(dotMode && text[i] == '.')) {
			if text[i] == '.' {
				dots = true
			}
			i++
		}
		run := text[start:i]
		if !dots {
			if dataTextBase64RunWorth(run, minBytes) &&
				g.echoesLongBase64Run(run, base64Depth, minBytes, budget) {
				return true, hasGap
			}
			continue
		}
		// Plain orientation: each dot-delimited segment is an alphabet run.
		for s := 0; s < len(run); {
			for s < len(run) && run[s] == '.' {
				s++
			}
			segmentStart := s
			for s < len(run) && run[s] != '.' {
				s++
			}
			segment := run[segmentStart:s]
			if dataTextBase64RunWorth(segment, minBytes) &&
				g.echoesLongBase64Run(segment, base64Depth, minBytes, budget) {
				return true, hasGap
			}
		}
		// Dotted orientation: the joined run contains only alphabet bytes, so
		// the run decoder is called directly; no separate run-finding scan is
		// needed.
		joined := strings.ReplaceAll(run, ".", "")
		if joined == "" {
			continue
		}
		if !budget.chargeChunked(int64(len(joined))) {
			return true, hasGap
		}
		if !budget.enterBase64Orientation(joined, base64Depth) {
			continue
		}
		if dataTextBase64RunWorth(joined, minBytes) &&
			g.echoesLongBase64Run(joined, base64Depth, minBytes, budget) {
			return true, hasGap
		}
	}
	return false, hasGap
}

// dataTextBase64RunWorth reports whether an alphabet run is long enough to
// decode to the shortest secret. The run decoder repeats the exact per-token
// check for each group alignment.
func dataTextBase64RunWorth(run string, minBytes int) bool {
	if len(run) < minBytes {
		return false
	}
	return !(len(run)/4 < minBytes && dataTextBase64DecodedLen(run) < minBytes)
}

// echoesLongBase64Orientation walks the maximal base64-alphabet runs of one
// orientation. The orientation pass is already charged by the caller; each run
// that is long enough to decode to the shortest secret is decoded and compared.
func (g *dataSecretGuard) echoesLongBase64Orientation(text string, base64Depth,
	minBytes int, budget *dataSecretScanBudget) bool {

	for i := 0; i < len(text); {
		if !dataTextBase64Byte(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && dataTextBase64Byte(text[i]) {
			i++
		}
		run := text[start:i]
		if len(run) < minBytes {
			continue
		}
		if len(run)/4 < minBytes && dataTextBase64DecodedLen(run) < minBytes {
			continue
		}
		if g.echoesLongBase64Run(run, base64Depth, minBytes, budget) {
			return true
		}
	}
	return false
}

// echoesLongBase64Run decodes one alphabet run at every group alignment and
// compares the decoded bytes with the raw secret fragments. A run larger than
// the decoded-scan bound is decoded at the canonical alignment only: all four
// alignments of a multi-megabyte run would quadruple the work for a payload
// that is already far past the size where the recursive pipeline runs, and a
// direct base64 spelling of a secret inside such a run is caught by the
// case-sensitive needle matcher before decoding. Nested unaligned encodings
// inside a distributed run are a documented residual.
func (g *dataSecretGuard) echoesLongBase64Run(run string, base64Depth, minBytes int,
	budget *dataSecretScanBudget) bool {

	alignments := 4
	if len(run) > dataSecretDecodedScanBytes {
		alignments = 1
	}
	for offset := 0; offset < alignments && offset+4 <= len(run); offset++ {
		token := run[offset:]
		if dataTextBase64DecodedLen(token) < minBytes {
			continue
		}
		if !budget.chargeChunked(int64(len(token))) {
			return true
		}
		decoded, ok := dataTextBase64DecodeToken(token)
		if !ok {
			continue
		}
		if !budget.takeDecodedBytes(len(decoded)) {
			return true
		}
		payload := string(decoded)
		// The count prefilter governs the direct byte/spelling scan of this
		// payload only. It must not suppress the nested whole-token recursion: a
		// payload that cannot itself contain a needle can still decode one level
		// deeper to bytes that do.
		could, ok := g.longPayloadCouldCarry(payload, budget)
		if !ok {
			return true
		}
		if could && g.longRawNeedles.matches(payload, budget) {
			return true
		}
		if len(decoded) > dataSecretDecodedScanBytes {
			continue
		}
		if offset != 0 {
			// A shifted alignment is a probe for a token embedded after leading
			// alphabet bytes. Probe its spellings but do not recurse: every
			// offset would otherwise fan out into its own decode tree, and a
			// text with tens of thousands of benign tokens would pay the escape
			// and percent closure of every shifted decode. The count prefilter
			// therefore also gates the closure here. The canonical alignment of
			// every run is fully processed, so the encoded content of a token
			// that starts at the canonical alignment is still caught; a nested
			// encoding inside a shifted probe is a documented residual.
			if !could {
				continue
			}
			if !budget.enterDecodedPayload(payload, true, base64Depth) {
				continue
			}
			if g.echoesLongPayloadSpellings(payload, true, budget) {
				return true
			}
			continue
		}
		if g.echoesLongDecodedPayload(payload, base64Depth+1, minBytes, could, budget) {
			return true
		}
	}
	return false
}

// echoesLongPayloadSpellings applies the spelling rules to a decoded payload
// without starting another token walk. It uses the linear normalized forms for
// every payload, not only those above the short-text bound: the payload was
// produced by the long-text base64 channel, the small fixed needle set covers
// the forward, case-folded, separator-stripped, reversed, and encoded spellings,
// and this keeps a text with hundreds of thousands of benign tokens from paying
// the full short-text spelling family for each one.
//
// The payload's escape and percent decoding closure is still walked, with the
// same depth bound and per-stage charging as the top-level text: a token can
// decode to an escaped or percent-encoded spelling, and the short-text path has
// always decoded those layers. could is the caller's count prefilter for the
// payload's own bytes. It skips only the direct form's spelling scan; a derived
// form is always scanned, because decoding changes the byte counts the
// prefilter reasoned about.
func (g *dataSecretGuard) echoesLongPayloadSpellings(payload string, could bool,
	budget *dataSecretScanBudget) bool {

	if !could {
		// No direct needle can fit the payload's own bytes, so only a decoded
		// form can still carry one. A form exists only when the payload holds an
		// escape, percent, or plus marker. One charged probe keeps a random
		// binary token from paying the walk for every alignment.
		if !budget.chargeChunked(int64(len(payload))) {
			return true
		}
		if !dataTextHasDecodeMarker(payload) {
			return false
		}
	}
	direct := true
	return walkDataTextLongDecodingsWithinForms(payload, budget,
		func(form dataTextDecodedForm) bool {
			formCould := could
			if !direct {
				// The caller's prefilter describes the payload itself, not a decoded
				// form. Re-running it on each derived form keeps a random binary
				// payload that happens to contain an escape marker from paying the
				// full spelling scan at every decode layer while still descending
				// into deeper forms: a form whose own bytes cannot carry a needle
				// cannot match one either.
				var ok bool
				formCould, ok = g.longPayloadCouldCarry(form.text, budget)
				if !ok {
					return true
				}
			}
			direct = false
			if !formCould {
				return false
			}
			if form.separatorEquivalent && g.spaceFreeVariants {
				return false
			}
			stripped, ascii, counts, ok := dataTextLongStripCounts(form.text, budget)
			if !ok {
				return true
			}
			return g.echoesLongSpellings(form.text, stripped, ascii, &counts, false, budget)
		})
}

// echoesLongDecodedPayload is the recursive stage for a canonical decoded
// payload: raw bytes, normalized spellings, and nested whole-token base64 up to
// dataSecretBase64Depth. Reaching the depth cap with a payload that could still
// carry a token latches the fail-closed decision instead of skipping the layer.
// could is the caller's count-prefilter result for this payload and gates only
// the spelling scan; the recursion is still attempted when the payload cannot
// directly contain a needle, because one more decode layer might.
func (g *dataSecretGuard) echoesLongDecodedPayload(payload string, base64Depth,
	minBytes int, could bool, budget *dataSecretScanBudget) bool {

	if !budget.enterDecodedPayload(payload, false, base64Depth) {
		return false
	}
	if g.echoesLongPayloadSpellings(payload, could, budget) {
		return true
	}
	if base64Depth >= dataSecretBase64Depth {
		if !budget.chargeChunked(int64(len(payload))) {
			return true
		}
		if !dataTextCouldCarryBase64Token(payload, minBytes, budget) {
			return false
		}
		budget.exceeded = true
		return true
	}
	token, ok := dataTextWholeBase64Token(payload)
	if !ok || dataTextBase64DecodedLen(token) < minBytes {
		return false
	}
	if !budget.takeBase64Token() {
		return true
	}
	if !budget.chargeChunked(int64(len(token))) {
		return true
	}
	decoded, ok := dataTextBase64DecodeToken(token)
	if !ok {
		return false
	}
	if !budget.takeDecodedBytes(len(decoded)) {
		return true
	}
	inner := string(decoded)
	innerCould, seedOK := g.longPayloadCouldCarry(inner, budget)
	if !seedOK {
		return true
	}
	if innerCould && g.longRawNeedles.matches(inner, budget) {
		return true
	}
	if len(decoded) > dataSecretDecodedScanBytes {
		return false
	}
	return g.echoesLongDecodedPayload(inner, base64Depth+1, minBytes, innerCould, budget)
}

// echoesShortCandidate applies the cheap ≤4096-byte rules unchanged. The text
// is small, so the full ordered-subsequence rule is charged nothing beyond the
// candidate pass its caller already took.
func (g *dataSecretGuard) echoesShortCandidate(text string, subsequence bool) bool {
	for _, variant := range g.variants {
		if dataTextCarriesContiguously(text, variant) {
			return true
		}
	}
	if !subsequence {
		return false
	}
	for _, variant := range g.variants {
		if variant == "" {
			continue
		}
		if dataTextIsSubsequence(text, variant) {
			return true
		}
	}
	return false
}

// dataTextCountsWithoutSeparators returns counts with every separator-class
// byte zeroed. Stripping separators removes exactly those bytes, so this is the
// exact count of stripDataSecretSeparators(text) without a second pass.
func dataTextCountsWithoutSeparators(counts *[256]int32) [256]int32 {
	derived := *counts
	for i := 0; i < len(dataSecretSeparatorBytes); i++ {
		derived[dataSecretSeparatorBytes[i]] = 0
	}
	return derived
}

// dataTextCountsCaseFolded returns the exact byte counts of the case-folded
// form of an ASCII text from its own counts. dataTextCaseFolded maps every
// ASCII uppercase letter to its lowercase counterpart and leaves every other
// byte alone, so the counts move without a second pass over the text.
func dataTextCountsCaseFolded(counts *[256]int32) [256]int32 {
	derived := *counts
	for c := byte('A'); c <= 'Z'; c++ {
		if n := derived[c]; n > 0 {
			derived[c] = 0
			derived[c-'A'+'a'] += n
		}
	}
	return derived
}

// echoesBase64 extracts base64 tokens from the orientation spellings of one
// candidate, decodes them, compares their bytes with the raw secret bytes, and
// scans a bounded decoded text through the full pipeline. Standard and URL-safe
// alphabets, padded and unpadded, are all accepted. A malformed token is
// skipped, never fatal. Budget exhaustion is different from malformed input:
// when the token, decoded-byte, or work budget is used up the scan fails closed
// rather than skipping the candidate token and delivering it.
func (g *dataSecretGuard) echoesBase64(text string, base64Depth int,
	budget *dataSecretScanBudget) bool {

	if len(text) < 4 {
		return false
	}
	if budget == nil {
		budget = newDataSecretScanBudget()
	}
	minBytes := g.minSecretBytes()
	if minBytes <= 0 {
		return false
	}
	if base64Depth >= dataSecretBase64Depth {
		// The nesting cap was reached with text that could still carry base64.
		// Refusing to look further is the only safe answer: skipping the layer
		// would deliver a token the guard never examined. Record the limit
		// decision so callers and tests can distinguish it from a clean scan. The
		// orientation probe below is charged like any other scan.
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		if !dataTextCouldCarryBase64Token(text, minBytes, budget) {
			return false
		}
		budget.exceeded = true
		return true
	}
	// Building the orientation spellings copies the candidate several times.
	// Each copy is charged before it is materialized, so a multi-megabyte text
	// fails closed as soon as the budget cannot cover the next copy instead of
	// allocating and scanning every orientation first.
	return dataTextWalkBase64Candidates(text, budget, func(candidate string) bool {
		// Skip an orientation this scan already examined at an equal or
		// shallower base64 depth. A decoded form shares its stripped and
		// stripped-reversed orientations with its parent, and rescanning a
		// deterministic, already-completed orientation cannot change the
		// decision.
		enter := budget.enterBase64Orientation(candidate, base64Depth)
		if !enter {
			return false
		}
		// The token walker scans the orientation once. Charge that pass, and
		// charge each alignment as it is decoded and compared below. Runs that
		// are too short to decode to the shortest secret are probed once instead
		// of four times.
		if !budget.chargeChunked(int64(len(candidate))) {
			return true
		}
		exhausted := walkDataTextBase64TokensAtLeast(candidate, minBytes, func(run string, offset int) bool {
			if !budget.chargeChunked(int64(len(run))) {
				return true
			}
			if dataTextBase64DecodedLen(run) < minBytes {
				return false
			}
			decoded, ok := dataTextBase64DecodeToken(run)
			if !ok {
				return false
			}
			// Canonical tokens consume one credit. Exhaustion fails closed; no
			// decoded token is ever silently dropped. Alignment probes are charged
			// as work but not as tokens, because they re-decode the same run.
			if offset == 0 && !budget.takeBase64Token() {
				return true
			}
			if !budget.takeDecodedBytes(len(decoded)) {
				return true
			}
			payload := string(decoded)
			// Raw byte comparison covers a secret that is not valid UTF-8 and
			// catches the threshold prefix before any text normalization. The
			// comparison charges one text-length scan per resolved value before
			// each scan it performs.
			if g.containsSecret(payload, budget) {
				return true
			}
			if len(decoded) > dataSecretDecodedScanBytes {
				// A large decoded payload is compared raw only at every alignment;
				// the raw comparison was charged above and recursing into it would
				// let one benign blob produce an unbounded tree of decodes. The
				// canonical alignment already applies this bound, and a shifted
				// alignment must not be more thorough than it: running the full
				// spelling pipeline over three shifted copies of a multi-megabyte
				// payload is the same unbounded-shape cost under another name.
				return false
			}
			if offset != 0 {
				// A non-canonical alignment is a probe for a token embedded after
				// leading alphabet bytes. Check its spellings but do not recurse:
				// every offset would otherwise fan out into its own decode tree. A
				// repeated diagnostic unit decodes to the same probe payload at
				// every repetition, so a payload already probed at this depth is
				// skipped rather than re-scanned for each copy.
				if !budget.enterDecodedPayload(payload, true, base64Depth) {
					return false
				}
				return g.echoesSpellings(payload, false, budget)
			}
			return g.echoesDecodedPayload(payload, base64Depth+1, budget)
		})
		return exhausted
	})
}

// echoesDecodedPayload checks one base64-decoded payload. Raw bytes and the
// normalized spellings are compared with the contiguous and threshold rules;
// the ordered-subsequence rule is deliberately not applied here, because a
// short secret is very likely to appear as a subsequence of arbitrary binary
// data and would reject benign binary payloads. Nested base64 is followed only
// when the whole payload is a single base64 token, which is the
// base64(base64(secret)) shape: scanning every alphabet run of decoded binary
// data would multiply the work without adding trust, since the same bytes are
// already covered by the raw comparison.
func (g *dataSecretGuard) echoesDecodedPayload(payload string, base64Depth int,
	budget *dataSecretScanBudget) bool {

	// The same repeated diagnostic unit decodes to this payload once per copy.
	// A completed canonical scan of identical bytes at an equal or shallower
	// depth already applied every spelling and recursive rule, so the copy can
	// be skipped without losing a comparison.
	if !budget.enterDecodedPayload(payload, false, base64Depth) {
		return false
	}
	if g.echoesSpellings(payload, false, budget) {
		return true
	}
	if base64Depth >= dataSecretBase64Depth {
		if !budget.chargeChunked(int64(len(payload))) {
			return true
		}
		if !dataTextCouldCarryBase64Token(payload, g.minSecretBytes(), budget) {
			return false
		}
		budget.exceeded = true
		return true
	}
	token, ok := dataTextWholeBase64Token(payload)
	if !ok || dataTextBase64DecodedLen(token) < g.minSecretBytes() {
		return false
	}
	decoded, ok := dataTextBase64DecodeToken(token)
	if !ok {
		return false
	}
	if !budget.takeBase64Token() {
		return true
	}
	if !budget.takeDecodedBytes(len(decoded)) {
		return true
	}
	inner := string(decoded)
	if g.containsSecret(inner, budget) {
		return true
	}
	if len(decoded) > dataSecretDecodedScanBytes {
		// The raw comparison charged its own scan; a larger payload is not
		// recursed into, which keeps one benign blob from producing an
		// unbounded tree.
		return false
	}
	return g.echoesDecodedPayload(inner, base64Depth+1, budget)
}

// dataTextWholeBase64Token reports whether text, after trimming surrounding
// whitespace, is one run of base64-alphabet bytes. Nested base64 is followed
// only in this whole-token shape.
func dataTextWholeBase64Token(text string) (string, bool) {
	token := strings.TrimSpace(text)
	if len(token) < 4 {
		return "", false
	}
	for i := 0; i < len(token); i++ {
		if !dataTextBase64Byte(token[i]) {
			return "", false
		}
	}
	return token, true
}

// minSecretBytes returns the shortest resolved secret in bytes. A base64 token
// whose decoded form is shorter than this cannot carry any secret.
func (g *dataSecretGuard) minSecretBytes() int {
	if g == nil || len(g.values) == 0 {
		return 0
	}
	shortest := len(g.values[0])
	for _, value := range g.values[1:] {
		if len(value) < shortest {
			shortest = len(value)
		}
	}
	return shortest
}

// dataTextSpellings returns every normalized spelling of one string that the
// guard compares: every Unicode folding (compatibility, decomposition without
// combining marks, whole-string case folding, and the explicit ligature and
// stroked-letter substitutions), then each form as-is, with separator-class
// bytes removed, reversed, and both of those reversed. A reversed full secret,
// a decomposed or fullwidth secret, "STRASSE" for "Straße", and a case-folded
// secret all reconstruct the value under one of these forms. Reversal is by
// rune so a multi-byte secret reversed by a provider is still caught.
//
// The guard scans the stream from dataTextWalkSpellings instead, so a large
// candidate never materializes every spelling at once. This collector keeps the
// full set available for guard construction and tests.
func dataTextSpellings(text string) []string {
	spellings := make([]string, 0, 32)
	dataTextWalkSpellings(text, dataTextSpellingOptions{}, nil,
		func(spelling string, _ *[256]int32, _ bool) bool {
			spellings = append(spellings, spelling)
			return false
		})
	return spellings
}

// dataTextReversalWork returns the bytes-equivalent work of reversing text.
// ASCII reversal copies the bytes once; a rune reversal decodes the string to
// runes, reverses them, and encodes them again, so it touches the bytes twice.
func dataTextReversalWork(text string) int64 {
	if dataTextIsASCII(text) {
		return int64(len(text))
	}
	return int64(len(text)) * 2
}

// dataTextSpellingOptions controls which spellings the walk emits and which
// bytes it can reuse from the caller.
type dataTextSpellingOptions struct {
	// canonicalASCII skips the redundant whole-string upper-case spelling family
	// for a long ASCII candidate: matching upper(text) case-sensitively against
	// upper(variant) is equivalent to matching the folded spelling already
	// emitted against the variant's own lower spelling, while the original case
	// is still scanned exactly.
	canonicalASCII bool
	// skipReversals skips the reversal-derived spellings of a long ASCII
	// candidate. The caller then scans the byte-reversed variants against the
	// forward spellings: reverse(spelling) carries variant exactly when spelling
	// carries reverse(variant), so the comparison set is the same while half the
	// materialized spellings are dropped. The caller compensates for the one
	// asymmetric rule (the contiguous threshold prefix) per variant.
	skipReversals bool
	// asciiInput reports that text is pure ASCII. Every form the walk derives
	// from it is then ASCII as well, so the non-ASCII fold and the explicit
	// substitution probe cannot change any form and are skipped without a scan.
	asciiInput bool
	// baseCounts is the caller's already-computed byte count of text. It avoids
	// charging a second full count pass for the first spelling.
	baseCounts *[256]int32
	// foldedASCII is the case-folded form of an ASCII text, computed by the
	// caller in the same pass that produced baseCounts. It avoids charging a
	// second full pass for strings.ToLower. It is nil for non-ASCII text, where
	// folding is not a byte homomorphism.
	foldedASCII *string
}

// dataTextWalkSpellings streams every spelling in the order dataTextSpellings
// returns them, charging each materialization to budget before it happens, and
// reports the exact byte counts of each spelling so a caller does not need a
// separate count pass. A nil budget scans without charging and leaves the
// counts nil; that is the guard-construction and short-text path.
//
// The third callback argument is true for spellings derived from the
// case-folded form of text. The long ASCII path uses it to select the
// upper-case compensation proxies: those comparisons are made against the
// folded spelling, where the skipped upper-case spelling would have been
// compared.
//
// visit returns true to stop. The walk reports true when it stopped, either
// because visit asked to or because the budget was exhausted; callers that care
// check budget.exceeded.
func dataTextWalkSpellings(text string, options dataTextSpellingOptions,
	budget *dataSecretScanBudget,
	visit func(string, *[256]int32, bool) bool) bool {

	seen := make(map[string]struct{}, 32)
	countCache := make(map[string]*[256]int32, 8)
	countsOf := func(value string) (*[256]int32, bool) {
		if options.baseCounts != nil && value == text {
			return options.baseCounts, true
		}
		if cached, ok := countCache[value]; ok {
			return cached, true
		}
		if budget == nil {
			return nil, true
		}
		if !budget.chargeChunked(int64(len(value))) {
			return nil, false
		}
		counts := dataTextByteCounts(value)
		countCache[value] = &counts
		return &counts, true
	}
	add := func(value string, counts *[256]int32, foldedForm bool) bool {
		if value == "" {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
		return visit(value, counts, foldedForm)
	}
	addNormalized := func(value string, foldedForm bool) bool {
		counts, ok := countsOf(value)
		if !ok {
			return true
		}
		if add(value, counts, foldedForm) {
			return true
		}
		// The separator probe and, when it fires, the stripped copy both scan the
		// whole value; charge the copy before it is built. When the exact byte
		// counts are already known, they decide whether any separator byte occurs
		// without a probe, so a separator-free value performs and charges no scan.
		// A stripped spelling always reaches this branch with separator-free
		// counts, which is why the strip of the strip is not billed again.
		hasSeparators := counts == nil || dataTextCountsHaveSeparators(counts)
		if hasSeparators && !budget.chargeChunked(int64(len(value))) {
			return true
		}
		stripped := value
		if hasSeparators {
			stripped = stripDataSecretSeparators(value)
		}
		var strippedCounts *[256]int32
		if stripped != value {
			if counts != nil {
				derived := dataTextCountsWithoutSeparators(counts)
				strippedCounts = &derived
			}
			if add(stripped, strippedCounts, foldedForm) {
				return true
			}
		}
		if options.skipReversals {
			return false
		}
		if !budget.chargeChunked(dataTextReversalWork(value)) {
			return true
		}
		reversed := dataTextReversed(value)
		if reversed == value {
			return false
		}
		// Reversal preserves byte counts.
		if add(reversed, counts, foldedForm) {
			return true
		}
		if stripped == value {
			return false
		}
		if !budget.chargeChunked(dataTextReversalWork(stripped)) {
			return true
		}
		// The historical final strip(reversed(value)) spelling is byte-for-byte
		// identical to reversed(stripped(value)) and never added a new entry, so
		// it is not rebuilt.
		return add(dataTextReversed(stripped), strippedCounts, foldedForm)
	}

	// Foldings last, matching the order of the other spelling forms.
	folded := ""
	if options.foldedASCII != nil {
		folded = *options.foldedASCII
	} else {
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		folded = dataTextCaseFolded(text)
	}
	if options.baseCounts != nil && options.foldedASCII != nil {
		// For ASCII text case folding is exactly A-Z to a-z, so the folded
		// spelling's byte counts are derived from the counts already computed
		// for the candidate. The lower and upper case forms are the same
		// spelling compared under different cases; charging a second full count
		// pass for the folded form would bill work that is not performed.
		derived := dataTextCountsCaseFolded(options.baseCounts)
		countCache[folded] = &derived
	}
	forms := make([]string, 0, 8)
	seenForms := make(map[string]struct{}, 8)
	addForm := func(value string) bool {
		if value == "" {
			return false
		}
		if _, ok := seenForms[value]; ok {
			return false
		}
		seenForms[value] = struct{}{}
		forms = append(forms, value)
		return false
	}
	addForm(text)
	addForm(folded)
	for _, base := range append([]string(nil), forms...) {
		if options.asciiInput || dataTextIsASCII(base) {
			continue
		}
		// The compatibility decomposition walks every rune through the fold
		// table and rebuilds the string; charge the decode and encode touches.
		if !budget.chargeChunked(int64(len(base)) * 2) {
			return true
		}
		addForm(foldDataTextUnicode(base))
	}
	// Apply the explicit substitutions last so they also cover the decomposed
	// forms. The snapshot keeps the loop from growing while it iterates.
	if !options.asciiInput {
		for _, form := range append([]string(nil), forms...) {
			// foldDataTextSpecial first probes for its substitution set, then
			// rebuilds only when a substitution is present. An ASCII candidate
			// cannot contain any substitution rune, so the probe is skipped
			// rather than performed and charged for it.
			if !budget.chargeChunked(int64(len(form))) {
				return true
			}
			if special := foldDataTextSpecial(form); special != form {
				if !budget.chargeChunked(int64(len(form))) {
					return true
				}
				addForm(special)
			}
		}
	}
	for _, form := range forms {
		if addNormalized(form, form == folded) {
			return true
		}
	}
	if !options.canonicalASCII {
		// Keep the historical simple case spellings in addition to the foldings.
		// Both go through the valid-span folds: strings.ToLower and strings.ToUpper
		// on invalid UTF-8 replace every invalid byte with U+FFFD, which would make
		// unrelated binary payloads compare equal. The lowercase fold is one of the
		// foldings already streamed above, so only the uppercase form can add a
		// spelling.
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		if upper := dataTextCaseUpper(text); upper != text {
			if addNormalized(upper, false) {
				return true
			}
		}
	}
	return false
}

// dataTextCaseUpper is the Unicode uppercase counterpart of dataTextCaseFolded
// with the same invalid-UTF-8 guarantee: valid spans are uppercased and invalid
// bytes are copied through unchanged.
func dataTextCaseUpper(text string) string {
	if dataTextIsASCII(text) || utf8.ValidString(text) {
		return strings.ToUpper(text)
	}
	var upper strings.Builder
	upper.Grow(len(text))
	changed := false
	for i := 0; i < len(text); {
		c := text[i]
		if c < utf8.RuneSelf {
			if c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
				changed = true
			}
			upper.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			upper.WriteByte(text[i])
			i++
			continue
		}
		if raised := unicode.ToUpper(r); raised != r {
			upper.WriteRune(raised)
			changed = true
		} else {
			upper.WriteString(text[i : i+size])
		}
		i += size
	}
	if !changed {
		return text
	}
	return upper.String()
}

// dataTextCaseFolded applies whole-string case folding. strings.ToLower is the
// simple Unicode lowercase mapping; the multi-character foldings that full
// case folding adds (sharp s, long s, final sigma) are applied by
// foldDataTextSpecial to every form, including this one, so "Straße" folds to
// "strasse" and the sharp-s substitution is symmetric. ASCII text stays on a
// fast path because ToLower never allocates when nothing changes.
//
// Text that is not valid UTF-8 is folded span by span. strings.ToLower on such
// a text replaces every invalid byte with U+FFFD, which would make two
// unrelated binary payloads compare equal and reject a benign result. Invalid
// bytes are copied through unchanged so binary comparisons stay byte-exact.
func dataTextCaseFolded(text string) string {
	if dataTextIsASCII(text) || utf8.ValidString(text) {
		return strings.ToLower(text)
	}
	return foldDataTextCaseValidSpans(text)
}

// foldDataTextCaseValidSpans lowercases the valid UTF-8 runs of text and copies
// invalid bytes verbatim. The ASCII fast path keeps a mostly-ASCII binary
// payload from paying a unicode.ToLower call per byte.
func foldDataTextCaseValidSpans(text string) string {
	var folded strings.Builder
	folded.Grow(len(text))
	changed := false
	for i := 0; i < len(text); {
		c := text[i]
		if c < utf8.RuneSelf {
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
				changed = true
			}
			folded.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			folded.WriteByte(text[i])
			i++
			continue
		}
		if lowered := unicode.ToLower(r); lowered != r {
			folded.WriteRune(lowered)
			changed = true
		} else {
			folded.WriteString(text[i : i+size])
		}
		i += size
	}
	if !changed {
		return text
	}
	return folded.String()
}

// dataTextIsASCII reports whether every byte is ASCII. ASCII text needs no
// Unicode normalization and no explicit substitutions, which keeps the
// large-input scan cheap.
func dataTextIsASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return false
		}
	}
	return true
}

// foldDataTextUnicode applies the NFKC-style fold: every rune is replaced by
// its compatibility and canonical decomposition with combining marks removed.
// A precomposed "é", a decomposed "e"+U+0301, and a fullwidth "ｅ" all fold to
// "e", so the guard treats them as the same spelling. The decomposition data
// lives in data_text_fold_table.go because the application layer may import
// only the standard library and domain.
//
// Invalid UTF-8 bytes are copied verbatim for the same reason as in
// dataTextCaseFolded, and ASCII runes skip the fold-table lookup entirely so a
// mostly-ASCII payload with a few invalid bytes stays linear and cheap.
func foldDataTextUnicode(text string) string {
	var folded strings.Builder
	folded.Grow(len(text))
	changed := false
	for i := 0; i < len(text); {
		c := text[i]
		if c < utf8.RuneSelf {
			folded.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			folded.WriteByte(c)
			i++
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			changed = true
			i += size
			continue
		}
		if mapped, ok := dataTextFoldRunes[r]; ok {
			folded.WriteString(mapped)
			changed = true
			i += size
			continue
		}
		folded.WriteString(text[i : i+size])
		i += size
	}
	if !changed {
		return text
	}
	return folded.String()
}

// foldDataTextSpecial expands the characters the ticket names as
// interchangeable with ASCII sequences, plus the multi-character mappings of
// full case folding that simple lowercasing does not produce. Case folding has
// already mapped the uppercase variants; both cases are listed so the helper
// is safe on its own.
//
// The function walks the text in one pass and returns it unchanged when no
// substitution rune occurs. It deliberately has no ContainsAny prefilter: the
// substitution set contains multi-byte runes, so ContainsAny decodes a rune at
// every byte, which is far slower than this ASCII fast path and turned
// multi-megabyte decoded payloads into seconds of work the caller's one-scan
// charge does not model.
func foldDataTextSpecial(text string) string {
	// The builder is allocated lazily on the first substitution so a text
	// without one is returned as-is after a single read-only pass; decoded
	// base64 payloads are commonly multi-megabyte and unchanged.
	var folded *strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		if c < utf8.RuneSelf {
			if folded != nil {
				folded.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			// Preserve invalid bytes verbatim; a rune conversion would turn them
			// into U+FFFD and make unrelated binary payloads compare equal.
			if folded != nil {
				folded.WriteByte(c)
			}
			i++
			continue
		}
		var replacement string
		switch r {
		case 'ß', 'ẞ':
			replacement = "ss"
		case 'æ', 'Æ':
			replacement = "ae"
		case 'ø', 'Ø':
			replacement = "o"
		case 'œ', 'Œ':
			replacement = "oe"
		case 'đ', 'Đ':
			replacement = "d"
		case 'ł', 'Ł':
			replacement = "l"
		case 'þ', 'Þ':
			replacement = "th"
		case 'ſ':
			replacement = "s"
		case 'ς':
			replacement = "σ"
		default:
			if folded != nil {
				folded.WriteString(text[i : i+size])
			}
			i += size
			continue
		}
		if folded == nil {
			builder := &strings.Builder{}
			builder.Grow(len(text) + 8)
			builder.WriteString(text[:i])
			folded = builder
		}
		folded.WriteString(replacement)
		i += size
	}
	if folded == nil {
		return text
	}
	return folded.String()
}

// dataTextReversed returns text with its runes in reverse order. Text that is
// not valid UTF-8 is reversed byte-wise: converting it through []rune would
// replace every invalid byte with U+FFFD, destroying the raw bytes a binary
// secret is matched against. ASCII reversal is byte-wise either way.
func dataTextReversed(text string) string {
	if !dataTextIsASCII(text) && utf8.ValidString(text) {
		runes := []rune(text)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return string(runes)
	}
	bytes := []byte(text)
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	return string(bytes)
}

// dataTextByteReversed returns text with its bytes in reverse order. The
// long-text reversal equivalence is byte-based: a variant prefix is looked for
// in the byte-reversed spelling, so its counterpart is the byte-reversed
// prefix, which is not the rune reversal for non-ASCII text.
func dataTextByteReversed(text string) string {
	bytes := []byte(text)
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	return string(bytes)
}

// dataTextCarriesContiguously reports whether one candidate carries value as a
// full substring or a threshold prefix. These rules apply to text of any
// length. The value is checked even when it is shorter than the threshold, so a
// short secret is always matched in full.
func dataTextCarriesContiguously(text, value string) bool {
	if value == "" {
		return false
	}
	if strings.Contains(text, value) {
		return true
	}
	return len(value) >= dataSecretFragmentBytes &&
		strings.Contains(text, value[:dataSecretFragmentBytes])
}

// dataTextByteCounts records how often each byte occurs in text.
func dataTextByteCounts(text string) [256]int32 {
	var counts [256]int32
	for i := 0; i < len(text); i++ {
		counts[text[i]]++
	}
	return counts
}

// walkDataTextDecodings visits text and every intermediate form reachable by
// applying one linear decoding step (escape, percent-query, or percent-path) at
// a time, depth-first, up to dataSecretDecodeDepth steps. The transforms are
// deterministic and never expand their input, so the reachable forms form a
// finite DAG. A form is re-expanded only when it is reached at a strictly
// shallower depth than before, which makes the documented depth guarantee
// independent of discovery order and of how many alternative forms exist. There
// is no breadth cap: a decoy cannot consume the depth or form budget of a pure
// chain, because every branch is followed to the documented depth. visit is
// called for every form as it is produced and returns true to stop the walk.
func walkDataTextDecodings(text string, visit func(string) bool) bool {
	if text == "" {
		return false
	}
	if visit(text) {
		return true
	}
	bestDepth := map[string]int{text: 0}
	var walk func(form string, depth int) bool
	walk = func(form string, depth int) bool {
		if depth >= dataSecretDecodeDepth {
			return false
		}
		for _, next := range decodeDataTextSteps(form) {
			nextDepth := depth + 1
			if previous, seen := bestDepth[next]; seen && previous <= nextDepth {
				continue
			}
			bestDepth[next] = nextDepth
			if visit(next) {
				return true
			}
			if walk(next, nextDepth) {
				return true
			}
		}
		return false
	}
	return walk(text, 0)
}

// dataTextDecodeMarkers records which linear decode transforms can change a
// text. One scan decides all three, so the charged walk can account for exactly
// the probes it performs: the old helpers each probed for their own marker, so
// a form with no markers cost four uncharged scans behind one charge.
type dataTextDecodeMarkers struct {
	escape  bool
	percent bool
	plus    bool
}

// dataTextDecodedForm is one form produced by a linear decoding step.
// separatorEquivalent reports that the step changed only separator-class bytes
// (the query decoder's "+" to space, when no percent escape decoded), so every
// non-separator byte is identical to the parent form.
type dataTextDecodedForm struct {
	text                string
	separatorEquivalent bool
}

// dataTextHasDecodeMarker reports whether any linear decoder can change text.
// It is the cheap form of dataTextDecodeMarkersOf for callers that only need
// to know whether a decode walk could produce a form.
func dataTextHasDecodeMarker(text string) bool {
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\', '%', '+':
			return true
		}
	}
	return false
}

// dataTextDecodeMarkersOf scans text once for the escape, percent, and plus
// markers.
func dataTextDecodeMarkersOf(text string) dataTextDecodeMarkers {
	var markers dataTextDecodeMarkers
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			markers.escape = true
		case '%':
			markers.percent = true
		case '+':
			markers.plus = true
		}
		if markers.escape && markers.percent && markers.plus {
			break
		}
	}
	return markers
}

// decoders returns how many linear decode passes these markers select for the
// short-text walk. Each selected decoder scans the whole form once.
func (m dataTextDecodeMarkers) decoders() int64 {
	passes := int64(0)
	if m.escape {
		passes++
	}
	if m.percent || m.plus {
		passes++
	}
	if m.percent && m.plus {
		passes++
	}
	return passes
}

// decodersLong returns how many linear decode passes the long-text walk selects.
// The long rules compare normalized needles and alphabet-only base64 needles, so
// a percent decoder whose only difference from its sibling is "+" versus a
// space is redundant: the path form keeps "+" as an alphabet byte and therefore
// dominates the query form, whose runs are strict subsets and whose stripped and
// folded forms are identical. When both markers are present only the path decode
// runs; a plus-only decode is skipped entirely because its parent dominates it.
// Escape decoding is never dominated.
func (m dataTextDecodeMarkers) decodersLong() int64 {
	passes := int64(0)
	if m.escape {
		passes++
	}
	if m.percent {
		passes++
	}
	return passes
}

// walkDataTextDecodingsWithin is walkDataTextDecodings with actual-work
// charging. The single marker scan, each selected decoder pass, and each
// decoded form are charged before they run; an exhausted budget stops the walk
// and the caller maps it to the fail-closed protocol error. Because the decoder
// materializes a form before returning it, the decoded form's charge is taken
// immediately after the build and before any scan of it, so no decoded bytes
// are ever scanned uncharged.
func walkDataTextDecodingsWithin(text string, budget *dataSecretScanBudget,
	visit func(string) bool) bool {

	return walkDataTextDecodingsWithinForms(text, budget,
		func(form dataTextDecodedForm) bool { return visit(form.text) })
}

// walkDataTextDecodingsWithinForms is walkDataTextDecodingsWithin with the
// produced form's separator-equivalence flag, so a caller that has proved the
// spelling scan is redundant (no compared value contains a separator byte) can
// skip it while still running the base64 extraction the changed bytes affect.
func walkDataTextDecodingsWithinForms(text string, budget *dataSecretScanBudget,
	visit func(dataTextDecodedForm) bool) bool {

	return walkDataTextDecodingsMode(text, budget, false, visit)
}

// walkDataTextLongDecodingsWithinForms is the long-text decode walk. It emits
// only the forms that are not dominated by a sibling or a parent under the long
// scan rules: the long rules look for normalized needles and base64-alphabet
// needles, so replacing a "+" with a space never adds a possible match.
// Dropping the dominated query branch removes a whole duplicated decode subtree
// per encoding combination, which is what keeps a diagnostic that mixes
// escapes, percent escapes, and plus signs inside the shared budget. The
// short-text walk keeps every branch because its full-value and
// ordered-subsequence rules can distinguish a space from a plus.
func walkDataTextLongDecodingsWithinForms(text string, budget *dataSecretScanBudget,
	visit func(dataTextDecodedForm) bool) bool {

	return walkDataTextDecodingsMode(text, budget, true, visit)
}

func walkDataTextDecodingsMode(text string, budget *dataSecretScanBudget,
	longText bool, visit func(dataTextDecodedForm) bool) bool {

	if text == "" {
		return false
	}
	if !budget.chargeChunked(int64(len(text))) {
		return true
	}
	if visit(dataTextDecodedForm{text: text}) {
		return true
	}
	bestDepth := map[string]int{text: 0}
	var walk func(form string, depth int) bool
	walk = func(form string, depth int) bool {
		if depth >= dataSecretDecodeDepth {
			return false
		}
		// Bound the number of whole-form passes a branching decode pattern can
		// cause, then charge the marker scan and each decode pass the markers
		// select before the decoders run. The output materialization is charged
		// below.
		if !budget.takeDecodeWalkBytes(len(form)) {
			return true
		}
		if !budget.chargeChunked(int64(len(form))) {
			return true
		}
		markers := dataTextDecodeMarkersOf(form)
		passes := markers.decoders()
		if longText {
			passes = markers.decodersLong()
		}
		if passes > 0 {
			if !budget.chargeChunked(int64(len(form)) * passes) {
				return true
			}
		}
		for _, next := range decodeDataTextStepsMarked(form, markers, longText) {
			if !budget.chargeChunked(int64(len(next.text))) {
				return true
			}
			nextDepth := depth + 1
			if previous, seen := bestDepth[next.text]; seen && previous <= nextDepth {
				continue
			}
			// Remembering a new form grows the walk's memory. The cap bounds it
			// independently of the work budget; reaching it fails closed instead of
			// dropping the branch unexamined.
			if _, seen := bestDepth[next.text]; !seen {
				if !budget.takeDecodeFormBytes(len(next.text)) {
					return true
				}
			}
			bestDepth[next.text] = nextDepth
			if visit(next) {
				return true
			}
			if walk(next.text, nextDepth) {
				return true
			}
		}
		return false
	}
	return walk(text, 0)
}

// decodeDataTextSteps applies one linear decoding step and returns every
// distinct result: Go/JSON escape decoding, query percent decoding ("+" is a
// space), and path percent decoding ("+" stays literal). Percent-encoded
// backslashes and escaped percent signs both compose across steps, which a
// single fixed order would miss.
func decodeDataTextSteps(text string) []string {
	forms := decodeDataTextStepsMarked(text, dataTextDecodeMarkersOf(text), false)
	values := make([]string, len(forms))
	for i, form := range forms {
		values[i] = form.text
	}
	return values
}

// decodeDataTextStepsMarked is decodeDataTextSteps with the marker scan already
// done by the caller. It only runs the decoders whose marker is present, so it
// does not repeat the probe the caller charged.
//
// longText selects the dominated-branch pruning described on
// walkDataTextLongDecodingsWithinForms. When both percent and plus markers are
// present the path form is emitted alone: it keeps "+" where the query form
// would substitute a space, so every alphabet run of the query form is a subset
// of the corresponding path run, and the stripped and folded forms are
// identical. A plus-only decode is not emitted at all because its parent
// dominates it for the same reason. When only the percent marker is present the
// two decoders produce the same text, so the single query-style decode is kept.
func decodeDataTextStepsMarked(text string, markers dataTextDecodeMarkers,
	longText bool) []dataTextDecodedForm {

	forms := make([]dataTextDecodedForm, 0, 3)
	seen := make(map[string]struct{}, 3)
	add := func(value string, ok, separatorEquivalent bool) {
		if !ok || value == "" || value == text {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		forms = append(forms, dataTextDecodedForm{
			text: value, separatorEquivalent: separatorEquivalent,
		})
	}
	if markers.escape {
		escaped, byteEscaped, escapedOK := decodeDataTextEscapesUnchecked(text)
		add(escaped, escapedOK, false)
		// A \u00NN escape has both a rune and a byte interpretation. The byte
		// form is a separate candidate because a provider that escaped raw
		// latin-1 bytes writes the same spelling; the charged walk bills the
		// materialization like any other returned form.
		if byteEscaped != "" {
			add(byteEscaped, true, false)
		}
	}
	if longText {
		if markers.percent {
			if markers.plus {
				path, pathOK := decodeDataTextPercentUnchecked(text, false)
				add(path, pathOK, false)
			} else {
				query, queryOK, separatorOnly := decodeDataTextPercentUncheckedForms(text, true)
				add(query, queryOK, separatorOnly)
			}
		}
		return forms
	}
	if markers.percent || markers.plus {
		query, queryOK, separatorOnly := decodeDataTextPercentUncheckedForms(text, true)
		add(query, queryOK, separatorOnly)
	}
	// Without a raw "+", query and path decoding differ only in how they treat
	// that byte, so their outputs are identical; one decoder pass suffices.
	if markers.percent && markers.plus {
		path, pathOK := decodeDataTextPercentUnchecked(text, false)
		add(path, pathOK, false)
	}
	return forms
}

// decodeDataTextPercentWith decodes URL percent escapes in text. A malformed or
// truncated sequence is copied through unchanged, so text with a stray "%" is
// never lost and later valid sequences are still decoded. It reports false when
// nothing changed.
func decodeDataTextPercentWith(text string, plusAsSpace bool) (string, bool) {
	if !strings.Contains(text, "%") && (!plusAsSpace || !strings.Contains(text, "+")) {
		return "", false
	}
	return decodeDataTextPercentUnchecked(text, plusAsSpace)
}

// decodeDataTextPercentUnchecked is decodeDataTextPercentWith with the marker
// probe already done by the caller. It always performs the full scan.
func decodeDataTextPercentUnchecked(text string, plusAsSpace bool) (string, bool) {
	decoded, changed, _ := decodeDataTextPercentUncheckedForms(text, plusAsSpace)
	return decoded, changed
}

// decodeDataTextPercentUncheckedForms is decodeDataTextPercentUnchecked with a
// third result: separatorOnly reports that the only changes were query plusses
// decoded to spaces, so every non-separator byte is unchanged. The caller uses
// it to avoid repeating the spelling scan of the parent form.
func decodeDataTextPercentUncheckedForms(text string, plusAsSpace bool) (
	decodedText string, changed, separatorOnly bool) {

	var decoded strings.Builder
	decoded.Grow(len(text))
	changedPlus := false
	changedPercent := false
	for i := 0; i < len(text); {
		switch {
		case plusAsSpace && text[i] == '+':
			decoded.WriteByte(' ')
			changedPlus = true
			i++
		case text[i] == '%' && i+2 < len(text):
			high, highOK := dataTextHexValue(text[i+1])
			low, lowOK := dataTextHexValue(text[i+2])
			if !highOK || !lowOK {
				decoded.WriteByte(text[i])
				i++
				continue
			}
			decoded.WriteByte(high<<4 | low)
			changedPercent = true
			i += 3
		default:
			decoded.WriteByte(text[i])
			i++
		}
	}
	changed = changedPlus || changedPercent
	if !changed {
		return "", false, false
	}
	return decoded.String(), true, changedPlus && !changedPercent
}

func dataTextHexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// dataTextBase64Encodings returns the standard and URL-safe base64 spellings,
// padded and unpadded, of value. They are guard variants, so an encoded echo
// is rejected even when the token is too large to decode or is embedded with
// no token boundary.
func dataTextBase64Encodings(value string) []string {
	raw := []byte(value)
	encodings := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(encoded string) {
		if encoded == "" {
			return
		}
		if _, ok := seen[encoded]; ok {
			return
		}
		seen[encoded] = struct{}{}
		encodings = append(encodings, encoded)
	}
	add(base64.StdEncoding.EncodeToString(raw))
	add(base64.RawStdEncoding.EncodeToString(raw))
	add(base64.URLEncoding.EncodeToString(raw))
	add(base64.RawURLEncoding.EncodeToString(raw))
	return encodings
}

// dataTextBase64Stripped removes the separator-class bytes from text in one
// pass and reports the two properties that let the orientation walk skip
// redundant copies:
//
//   - hasDot reports that text contains a dot, so the dots-only orientation
//     differs from text.
//   - joinSame reports that joinDataTextBase64Gaps would return exactly the
//     stripped string. That holds exactly when every maximal non-alphabet run
//     is at most gap bytes long and consists only of separator-class bytes (the
//     gap join drops such a run and the separator strip drops the same bytes)
//     and no alphabet byte is itself in the separator class: '+', '-', '/',
//     and '_' are both, and the join keeps them inside an alphabet run while
//     the strip removes them. A longer run, a non-separator byte inside a run,
//     or one of those four alphabet characters makes the orientations differ.
//
// The returned string is text itself when no separator byte occurs, matching
// stripDataSecretSeparators.
func dataTextBase64Stripped(text string, gap int) (stripped string, hasDot, joinSame bool) {
	joinSame = true
	changed := false
	var out []byte
	runLen := 0
	runHasNonSeparator := false
	flushRun := func() {
		if runLen > gap || runHasNonSeparator {
			joinSame = false
		}
		runLen = 0
		runHasNonSeparator = false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '.' {
			hasDot = true
		}
		if dataTextBase64Byte(c) {
			if runLen > 0 {
				flushRun()
			}
			// The separator class and the URL-safe/standard alphabets overlap on
			// '+', '-', '/', and '_'. The gap join keeps those bytes inside an
			// alphabet run, while the separator strip removes them, so the two
			// orientations differ whenever one occurs; the strip must remove the
			// byte here just like any other separator.
			if dataTextSeparatorByte(c) {
				joinSame = false
				if !changed {
					out = append([]byte(nil), text[:i]...)
					changed = true
				}
				continue
			}
			if changed {
				out = append(out, c)
			}
			continue
		}
		runLen++
		if !dataTextSeparatorByte(c) {
			runHasNonSeparator = true
		}
		if dataTextSeparatorByte(c) {
			if !changed {
				out = append([]byte(nil), text[:i]...)
				changed = true
			}
			continue
		}
		if changed {
			out = append(out, c)
		}
	}
	if runLen > 0 {
		flushRun()
	}
	if !changed {
		return text, hasDot, joinSame
	}
	return string(out), hasDot, joinSame
}

// dataTextWalkBase64Candidates streams the orientation spellings, charging each
// copy before it is built. The candidate is the input itself and costs nothing;
// every derived orientation is a full scan and copy, so the budget is charged
// for the bytes it will touch before the allocation. The separator strip and
// the gap join apply the same removals for ordinary prose, so the walk builds
// and charges the join only when its result would actually differ. An exhausted
// budget stops the walk and the caller maps it to the fail-closed protocol
// error.
func dataTextWalkBase64Candidates(text string, budget *dataSecretScanBudget,
	visit func(string) bool) bool {

	seen := make(map[string]struct{}, 8)
	add := func(value string) bool {
		if value == "" {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
		return visit(value)
	}
	if add(text) {
		return true
	}
	if !budget.chargeChunked(int64(len(text))) {
		return true
	}
	stripped, hasDot, joinSame := dataTextBase64Stripped(text, dataSecretBase64GapBytes)
	if stripped != text {
		if add(stripped) {
			return true
		}
	}
	// A DNS-shaped identity separates base64url characters with dots. Remove
	// only the dots here: the general separator strip also removes "-" and "_",
	// which are data characters in the URL-safe alphabet.
	if hasDot {
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		if withoutDots := strings.ReplaceAll(text, ".", ""); withoutDots != text {
			if add(withoutDots) {
				return true
			}
		}
	}
	if !joinSame {
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		if joined := joinDataTextBase64Gaps(text, dataSecretBase64GapBytes); joined != text {
			if add(joined) {
				return true
			}
		}
	}
	if !budget.chargeChunked(dataTextReversalWork(text)) {
		return true
	}
	reversed := dataTextReversed(text)
	if reversed == text {
		return false
	}
	if add(reversed) {
		return true
	}
	if !budget.chargeChunked(int64(len(reversed))) {
		return true
	}
	strippedReversed, _, joinSameReversed := dataTextBase64Stripped(reversed, dataSecretBase64GapBytes)
	if strippedReversed != reversed {
		if add(strippedReversed) {
			return true
		}
	}
	if !joinSameReversed {
		if !budget.chargeChunked(int64(len(reversed))) {
			return true
		}
		if joinedReversed := joinDataTextBase64Gaps(reversed, dataSecretBase64GapBytes); joinedReversed != reversed {
			if add(joinedReversed) {
				return true
			}
		}
	}
	return false
}

// joinDataTextBase64Gaps removes every run of non-alphabet bytes no longer than
// gap bytes, joining the alphabet runs on both sides. A longer run is replaced
// by a newline boundary, so tokens are not glued across sentence-sized gaps.
// The result still needs the same run extraction as any other orientation.
func joinDataTextBase64Gaps(text string, gap int) string {
	var joined strings.Builder
	joined.Grow(len(text))
	changed := false
	for i := 0; i < len(text); {
		if dataTextBase64Byte(text[i]) {
			joined.WriteByte(text[i])
			i++
			continue
		}
		start := i
		for i < len(text) && !dataTextBase64Byte(text[i]) {
			i++
		}
		if i-start <= gap {
			changed = true
			continue
		}
		joined.WriteByte('\n')
		if i-start != 1 || text[start] != '\n' {
			changed = true
		}
	}
	if !changed {
		return text
	}
	return joined.String()
}

// walkDataTextBase64Tokens visits every alignment of every maximal
// base64-alphabet run in text. Base64 groups are four characters, so a token
// that starts in the middle of a run is recovered by trying the four possible
// leading phases; a token split at a group boundary is recovered at offset
// zero. The offset lets the caller recurse only from the canonical alignment:
// many duplicated decodes of one benign run would otherwise multiply the scan.
// visit returns true to stop.
func walkDataTextBase64Tokens(text string, visit func(token string, offset int) bool) bool {
	for i := 0; i < len(text); {
		if !dataTextBase64Byte(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && dataTextBase64Byte(text[i]) {
			i++
		}
		run := text[start:i]
		for offset := 0; offset < 4 && offset+4 <= len(run); offset++ {
			if visit(run[offset:], offset) {
				return true
			}
		}
	}
	return false
}

// walkDataTextBase64TokensAtLeast is walkDataTextBase64Tokens with a minimum
// decoded size. The one linear pass over text is already charged by the caller
// that materializes and scans the orientation, so this walk does not charge the
// run bytes a second time. Each alignment is charged by visit, which performs
// the decode and comparison. A run whose canonical decoded form is shorter than
// minBytes cannot decode to a secret at any alignment, so it is probed once
// instead of being visited at four alignments; a run at least four times
// minBytes long always decodes to at least that many bytes, so it skips the
// probe. visit returns true to stop, and the walk returns true when it stopped.
func walkDataTextBase64TokensAtLeast(text string, minBytes int,
	visit func(token string, offset int) bool) bool {

	for i := 0; i < len(text); {
		if !dataTextBase64Byte(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && dataTextBase64Byte(text[i]) {
			i++
		}
		run := text[start:i]
		// Decoding never produces more bytes than the run has characters, so a run
		// shorter than minBytes cannot carry the secret at any alignment.
		if len(run) < minBytes {
			continue
		}
		if len(run)/4 < minBytes {
			if dataTextBase64DecodedLen(run) < minBytes {
				continue
			}
		}
		for offset := 0; offset < 4 && offset+4 <= len(run); offset++ {
			if visit(run[offset:], offset) {
				return true
			}
		}
	}
	return false
}

// dataTextCouldCarryBase64Token reports whether any orientation of text still
// contains an alphabet run long enough to decode to a secret. It is the
// fail-closed answer when the base64 nesting cap is reached: the layer above
// the cap may hold a token, and delivering it unexamined would be a skip.
//
// The orientation spellings are built through the charged walk, so this helper
// cannot materialize copies of a large decoded payload uncharged. An exhausted
// budget returns true, which the callers already treat as fail-closed.
func dataTextCouldCarryBase64Token(text string, minBytes int,
	budget *dataSecretScanBudget) bool {

	if minBytes <= 0 {
		return false
	}
	if len(text) > 1<<20 {
		return true
	}
	found := false
	dataTextWalkBase64Candidates(text, budget, func(candidate string) bool {
		if !budget.chargeChunked(int64(len(candidate))) {
			found = true
			return true
		}
		if walkDataTextBase64Tokens(candidate, func(token string, _ int) bool {
			return dataTextBase64DecodedLen(token) >= minBytes
		}) {
			found = true
			return true
		}
		return false
	})
	return found
}

// dataTextBase64Byte reports whether c belongs to the union of the standard
// and URL-safe base64 alphabets plus padding.
func dataTextBase64Byte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '+', c == '/', c == '=', c == '-', c == '_':
		return true
	default:
		return false
	}
}

// dataTextBase64DecodedLen bounds the decoded length of one run without
// decoding it. Padding characters are excluded from the estimate.
func dataTextBase64DecodedLen(run string) int {
	payload := strings.TrimRight(run, "=")
	return len(payload) * 3 / 4
}

// dataTextBase64DecodeToken decodes one alphabet-consistent run. A run that
// mixes the standard and URL-safe alphabets is not valid in either and is
// rejected. Raw (unpadded) decoding accepts both padded and unpadded input
// after the padding is trimmed.
func dataTextBase64DecodeToken(run string) ([]byte, bool) {
	payload := strings.TrimRight(run, "=")
	if payload == "" {
		return nil, false
	}
	// Two cheap byte scans, not ContainsAny: the charset is one byte wide, and
	// ContainsAny decodes runes for it, which turns a multi-megabyte run into
	// seconds of work the caller's run-length charge does not model. The scans
	// stay linear with the decode they precede.
	urlAlphabet := strings.IndexByte(payload, '-') >= 0 || strings.IndexByte(payload, '_') >= 0
	stdAlphabet := strings.IndexByte(payload, '+') >= 0 || strings.IndexByte(payload, '/') >= 0
	if urlAlphabet && stdAlphabet {
		return nil, false
	}
	encoding := base64.RawStdEncoding
	if urlAlphabet {
		encoding = base64.RawURLEncoding
	}
	decoded, err := encoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// containsSecret is the raw byte containment rule used on decoded base64
// payloads, where the bytes need not be valid text. It keeps the full-value and
// threshold-prefix semantics of the text scan in a single scan per value: a
// value at or above the threshold is carried exactly when its threshold prefix
// is carried, so searching the prefix once decides the same predicate as
// searching the full value and then the prefix.
//
// Each scan is charged before it runs, and an exhausted budget returns true
// (fail closed): an unexamined payload is never delivered. The caller must not
// pre-charge a guessed number of scans; the charge here covers the scan that
// actually happens, including the early return on the first match.
func (g *dataSecretGuard) containsSecret(text string, budget *dataSecretScanBudget) bool {
	for _, value := range g.values {
		needle := dataTextSecretPrefix(value)
		if !budget.chargeChunked(int64(len(text))) {
			return true
		}
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// decodeDataTextEscapesUnchecked decodes Go-style escape sequences embedded in
// text without requiring the whole string to be a quoted literal, with the
// backslash marker probe already done by the caller. Both %q output and JSON
// strings use this syntax (JSON additionally allows \/), so this covers a
// secret escaped as \" \\ \n \t \x00 or \u0000. It always performs the full
// scan.
//
// An escape denotes either a byte or a rune, and the difference is observable:
// strconv.UnquoteChar reports \xNN and \NNN as the rune U+00NN, so writing the
// value with WriteRune would encode 0xFE as two UTF-8 bytes (c3 be) and the
// decoded candidate would never match the raw secret byte. The escape
// introducer is inspected instead: \x and octal escapes write the byte exactly,
// while \u and \U escapes write the rune.
//
// A \u00NN escape is ambiguous in practice. Under Go/JSON semantics it is the
// rune U+00NN, but a provider that escaped latin-1 bytes uses the same spelling
// for the raw byte, so the byte interpretation is returned as a second
// candidate in addition to the rune interpretation. The second candidate is
// built only after the first differing escape is seen, so ordinary text without
// byte-valued \u escapes pays nothing for it. Callers that only need the
// primary decoding ignore the second candidate.
func decodeDataTextEscapesUnchecked(text string) (decodedText, byteDecodedText string, ok bool) {
	var decoded strings.Builder
	decoded.Grow(len(text))
	var byteDecoded strings.Builder
	byteAlt := false
	changed := false
	rest := text
	for len(rest) > 0 {
		first := rest[0]
		// JSON permits \/ even though Go quoting does not; normalize it before
		// the Go escape decoder sees it.
		if first == '\\' && len(rest) > 1 && rest[1] == '/' {
			decoded.WriteByte('/')
			if byteAlt {
				byteDecoded.WriteByte('/')
			}
			rest = rest[2:]
			changed = true
			continue
		}
		// quote='"' lets UnquoteChar decode \" (a Go %q or JSON string); an
		// unescaped quote is simply copied byte by byte below.
		value, _, tail, err := strconv.UnquoteChar(rest, '"')
		if err != nil || tail == rest {
			// An invalid escape or a corner case: keep the byte and continue so
			// a valid escape later in the text is still decoded.
			decoded.WriteByte(first)
			if byteAlt {
				byteDecoded.WriteByte(first)
			}
			rest = rest[1:]
			continue
		}
		if first != '\\' {
			// Copy the source bytes verbatim. UnquoteChar reports an invalid
			// UTF-8 byte as U+FFFD, and WriteRune would replace it; a binary
			// candidate must keep its exact bytes.
			span := rest[:len(rest)-len(tail)]
			decoded.WriteString(span)
			if byteAlt {
				byteDecoded.WriteString(span)
			}
			rest = tail
			continue
		}
		changed = true
		if dataTextByteEscape(rest, value) {
			// \xNN and \NNN denote a byte. Write it raw so a decoded binary
			// candidate compares byte-exactly with the resolved secret.
			decoded.WriteByte(byte(value))
			if byteAlt {
				byteDecoded.WriteByte(byte(value))
			}
		} else if dataTextUnicodeByteEscape(rest, value) {
			if !byteAlt {
				// Start the byte-wise candidate from the rune-wise prefix.
				byteAlt = true
				byteDecoded.WriteString(decoded.String())
				byteDecoded.WriteByte(byte(value))
			} else {
				byteDecoded.WriteByte(byte(value))
			}
			decoded.WriteRune(value)
		} else {
			decoded.WriteRune(value)
			if byteAlt {
				byteDecoded.WriteRune(value)
			}
		}
		rest = tail
	}
	if !changed {
		return "", "", false
	}
	decodedText = decoded.String()
	if !byteAlt {
		return decodedText, "", true
	}
	byteDecodedText = byteDecoded.String()
	if byteDecodedText == decodedText {
		return decodedText, "", true
	}
	return decodedText, byteDecodedText, true
}

// dataTextByteEscape reports whether the escape at the start of rest denotes a
// single byte: \xHH or a three-digit octal \NNN whose value fits in a byte.
// value is what strconv.UnquoteChar decoded for the same escape.
func dataTextByteEscape(rest string, value rune) bool {
	if len(rest) < 2 || rest[0] != '\\' {
		return false
	}
	switch {
	case rest[1] == 'x':
		return true
	case rest[1] >= '0' && rest[1] <= '7':
		// A three-digit octal escape above 255 is not a byte; keep the rune
		// decoder's value rather than truncating it.
		return value <= 0xFF
	default:
		return false
	}
}

// dataTextUnicodeByteEscape reports whether the escape at the start of rest is
// \u or \U and its rune value is also a meaningful raw byte (0x80-0xFF). The
// rune and byte interpretations differ exactly for those values: values below
// 0x80 encode as the same single byte, and values above 0xFF are not bytes.
func dataTextUnicodeByteEscape(rest string, value rune) bool {
	if len(rest) < 2 || rest[0] != '\\' {
		return false
	}
	if rest[1] != 'u' && rest[1] != 'U' {
		return false
	}
	return value >= 0x80 && value <= 0xFF
}

// stripDataSecretSeparators removes the separator-class bytes from text. A
// dotted, dashed, underscored, space-padded, or punctuation-separated spelling
// of a secret survives as one contiguous run once the separators are gone.
func stripDataSecretSeparators(text string) string {
	if !strings.ContainsAny(text, dataSecretSeparatorBytes) {
		return text
	}
	stripped := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		if dataTextSeparatorByte(text[i]) {
			continue
		}
		stripped = append(stripped, text[i])
	}
	return string(stripped)
}

// dataTextSeparatorByte reports whether c belongs to the separator class.
func dataTextSeparatorByte(c byte) bool {
	return dataTextSeparatorTable[c]
}

// dataSecretBoundaryWindowBytes caps the per-side raw window scanned around one
// reversed field-order boundary. A cross-boundary echo is a contiguous run
// after separator stripping, so the window must cover the needle plus its
// encoding expansion. The longest long-text needle is the 8-byte threshold
// fragment; one percent-encoding layer is 3x, a \uXXXX escape is 6x, base64 is
// 4/3, and nested decoding can multiply those. A megabyte per side covers all
// shallow and moderately nested spellings; a spelling that expands past the cap
// on one side of a field boundary is the documented bounded-window residual.
const dataSecretBoundaryWindowBytes = 1 << 20

// dataSecretBoundaryWindowMinBytes is the smallest per-side window. An 8-byte
// threshold fragment expands to 24 bytes under one percent layer, 48 under one
// \uXXXX layer, and 648 under four percent layers or 288 under two \uXXXX
// layers, so a kilobyte still covers every raw, escaped, percent, base64, and
// shallow nested 4/6 split. It keeps the window count from shrinking detection
// to a few bytes when a result has many short fields.
const dataSecretBoundaryWindowMinBytes = 1 << 10

// dataSecretBoundaryWindowShare divides the join length into the total window
// allowance: the reverse-order windows scan at most one quarter of the join's
// bytes in total (unless the minimum window forces merged full-join coverage).
// Together with the single forward join scan this keeps each result byte charged
// O(1) across both deterministic orders.
const dataSecretBoundaryWindowShare = 4

// echoesFields reports whether the fields echo a secret in any deterministic
// field order. A provider can split one secret across two result fields, which
// no single-field scan can see. echoes already covers interleaved and
// separator-normalized forms, so a split whose pieces appear in order is caught
// in either order.
func (g *dataSecretGuard) echoesFields(fields ...string) bool {
	if g == nil {
		return false
	}
	return g.echoesFieldsWithin(newDataSecretScanBudget(), fields...)
}

// echoesFieldsWithin is echoesFields with one budget shared by the forward join
// and every reverse-order boundary window, so a provider cannot reset the scan
// allowance by splitting text across fields.
//
// The forward field-order join contains every field's full content, so scanning
// it once covers every interior echo as well as every echo that is contiguous
// in document order; the fields are deliberately not scanned a second time.
// The reverse field-order join differs from the forward one only in the
// combinations that cross a field boundary, so only a bounded window around
// each boundary is scanned for that direction. This is what keeps a diagnostic
// from being charged three times (field, forward join, reverse join).
func (g *dataSecretGuard) echoesFieldsWithin(budget *dataSecretScanBudget,
	fields ...string) bool {

	if g == nil {
		return false
	}
	if budget == nil {
		budget = newDataSecretScanBudget()
	}
	// Empty fields add nothing but a separator. Separators are stripped by the
	// normalized rules and cannot occur inside a base64 or contiguous needle, so
	// dropping them keeps every comparison identical while making the remaining
	// fields adjacent for the reverse-order boundary walk.
	nonEmpty := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			nonEmpty = append(nonEmpty, field)
		}
	}
	if len(nonEmpty) == 0 {
		return false
	}
	if g.echoesWithin(strings.Join(nonEmpty, "\n"), budget) {
		return true
	}
	if len(nonEmpty) < 2 {
		return false
	}
	return g.echoesReversedFieldWindows(nonEmpty, budget)
}

// echoesReversedFieldWindows scans bounded windows around every adjacency of
// the reversed field order. A match that crosses no boundary is an interior
// match and was already covered by the forward join; a match that crosses a
// boundary in the reversed order has its leading fragment in the later field
// and its trailing fragment in the earlier field, so a window spanning the
// separator catches it after the guard's normal decoding and separator
// normalization. Overlapping windows are merged and each window is a slice of
// the reversed join, so the total window bytes never exceed one join length and
// every result byte stays charged O(1). The per-side window is capped at
// dataSecretBoundaryWindowBytes and at an equal share of a quarter of the join
// bytes (with a minimum of dataSecretBoundaryWindowMinBytes). A cross-boundary
// spelling whose encoded form expands past the chosen window on one side is the
// documented bounded-window residual; raw, escaped, percent, base64, and
// shallow nested splits are far inside it, and the forward join covers every
// spelling that is contiguous in document order.
func (g *dataSecretGuard) echoesReversedFieldWindows(fields []string,
	budget *dataSecretScanBudget) bool {

	n := len(fields)
	reversed := make([]string, n)
	total := n - 1
	for i, field := range fields {
		reversed[n-1-i] = field
		total += len(field)
	}
	perSide := dataSecretBoundaryWindowBytes
	if share := total / (dataSecretBoundaryWindowShare * 2 * (n - 1)); share < perSide {
		perSide = share
	}
	if perSide < dataSecretBoundaryWindowMinBytes {
		perSide = dataSecretBoundaryWindowMinBytes
	}
	joined := strings.Join(reversed, "\n")
	type window struct{ start, end int }
	windows := make([]window, 0, n-1)
	offset := 0
	for i := 0; i < n-1; i++ {
		offset += len(reversed[i])
		start := offset - perSide
		if start < 0 {
			start = 0
		}
		end := offset + 1 + perSide
		if end > len(joined) {
			end = len(joined)
		}
		if last := len(windows) - 1; last >= 0 && start <= windows[last].end {
			if end > windows[last].end {
				windows[last].end = end
			}
		} else {
			windows = append(windows, window{start: start, end: end})
		}
		offset++
	}
	for _, w := range windows {
		if w.end <= w.start {
			continue
		}
		if g.echoesWithin(joined[w.start:w.end], budget) {
			return true
		}
	}
	return false
}

// echoesResultDiagnostics reports whether the host-visible diagnostic text of a
// result echoes a secret: the plan text, every plan node label and detail, and
// the summary message. These are checkable diagnostic strings, not provider
// result data.
//
// The fields are concatenated with a newline separator and scanned as one text
// in document order; because that join contains every field's full content,
// scanning the individual fields again would only repeat work. The reversed
// field order is covered by bounded windows around each field boundary: a
// secret split across sibling plan nodes with the leading fragment in the
// later node is contiguous only in that order, and a full reverse join scan
// would double the cost of every benign multi-field diagnostic. The separator
// keeps the chunks from being glued into an unrelated contiguous run while the
// guard's separator spellings still reconstruct adjacent fragments. One budget
// spans the whole result, so spreading the same bytes across a thousand small
// nodes does not multiply the work.
//
// Opaque provider-owned payloads are deliberately out of scope: table cells,
// document and pair values, chunk bytes, metadata names and paths, and column
// names are the provider's own data, which a provider can always return in the
// clear through the same contract. Scanning them would cost unbounded work
// without changing the trust boundary. The split is documented by
// TestDataProviderOpaqueResultPayloadsAreOutsideSecretEchoProtection.
func (g *dataSecretGuard) echoesResultDiagnostics(result domainplugin.DataResult) bool {
	if g == nil {
		return false
	}
	fields := make([]string, 0, 8)
	if result.Summary != nil {
		fields = append(fields, result.Summary.Message)
	}
	if result.Plan != nil {
		fields = append(fields, result.Plan.Text)
		var walk func(nodes []domainplugin.DataPlanNode)
		walk = func(nodes []domainplugin.DataPlanNode) {
			for _, node := range nodes {
				fields = append(fields, node.Label, node.Detail)
				walk(node.Children)
			}
		}
		walk(result.Plan.Nodes)
	}
	return g.echoesFieldsWithin(newDataSecretScanBudget(), fields...)
}

func dataTextIsSubsequence(text, value string) bool {
	index := 0
	for i := 0; i < len(value); i++ {
		next := strings.IndexByte(text[index:], value[i])
		if next < 0 {
			return false
		}
		index += next + 1
	}
	return true
}

// hostDataClass maps one provider-reported class to the host's closed
// operation-class vocabulary. An empty value keeps the historical unknown
// default; every other value outside the closed set is invalid. The raw guest
// text is never returned.
func hostDataClass(class domainplugin.DataOperationClass) (domainplugin.DataOperationClass, bool) {
	if class == "" {
		return domainplugin.DataClassUnknown, true
	}
	if class.Valid() {
		return class, true
	}
	return domainplugin.DataClassUnknown, false
}

// dataAuditClass returns only a closed-vocabulary class for an audit row.
// Provider text that is not a member of the set is replaced with the constant
// unknown class, so guest text can never be persisted in the audit sink even
// if a caller forgets to validate it first.
func dataAuditClass(class domainplugin.DataOperationClass) string {
	if class == "" {
		return ""
	}
	if class.Valid() {
		return string(class)
	}
	return string(domainplugin.DataClassUnknown)
}

func dataErrorStatus(err error) string {
	var dataErr *DataError
	if errors.As(err, &dataErr) {
		return string(dataErr.Kind)
	}
	var providerErr *domainplugin.ProviderError
	if errors.As(err, &providerErr) {
		return string(providerErr.Kind)
	}
	return string(DataErrorFaulted)
}

// dataResponse is the JSON envelope every data-provider method returns.
type dataResponse struct {
	Status         string                         `json:"status"`
	Error          string                         `json:"error,omitempty"`
	Message        string                         `json:"message,omitempty"`
	Connection     uint64                         `json:"connection,omitempty"`
	Query          uint64                         `json:"query,omitempty"`
	Transaction    uint64                         `json:"transaction,omitempty"`
	Result         *domainplugin.DataResult       `json:"result,omitempty"`
	Capabilities   *domainplugin.DataCapabilities `json:"capabilities,omitempty"`
	Metadata       *domainplugin.DataMetadata     `json:"metadata,omitempty"`
	ServerIdentity string                         `json:"serverIdentity,omitempty"`
	AuthResult     string                         `json:"authResult,omitempty"`

	// cancelled is host-internal: the caller stopped waiting while the guest
	// still returned a result. Delivered rows are marked partial.
	cancelled bool
}

// dataOpenRequest is one internal connection open. Secrets are resolved only
// for this call and never become part of a profile record.
type dataOpenRequest struct {
	Profile domainplugin.DataProfile
	Fields  map[string]string
	Secrets map[string]string
	Test    bool
}

type dataOpenPayload struct {
	ProfileID string            `json:"profileId"`
	Provider  string            `json:"provider"`
	Label     string            `json:"label,omitempty"`
	Mode      string            `json:"mode"`
	ViaHost   string            `json:"viaHost,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	Test      bool              `json:"test,omitempty"`
	Tunnel    *DataTunnel       `json:"tunnel,omitempty"`
}

type dataConnectionPayload struct {
	Connection uint64 `json:"connection"`
}

type dataQueryPayload struct {
	Connection uint64                   `json:"connection"`
	Query      string                   `json:"query"`
	Parameters []domainplugin.DataValue `json:"parameters,omitempty"`
}

type dataFetchPayload struct {
	Connection uint64 `json:"connection"`
	Query      uint64 `json:"query"`
}

type dataCancelPayload struct {
	Connection uint64 `json:"connection"`
	Query      uint64 `json:"query"`
}

type dataCommandPayload struct {
	Connection uint64                   `json:"connection"`
	Command    string                   `json:"command"`
	Parameters []domainplugin.DataValue `json:"parameters,omitempty"`
}

type dataTransactionPayload struct {
	Connection  uint64 `json:"connection"`
	Transaction uint64 `json:"transaction"`
}

type dataMetadataPayload struct {
	Connection uint64 `json:"connection"`
	Path       string `json:"path,omitempty"`
	Next       string `json:"next,omitempty"`
}

type dataPlanPayload struct {
	Connection uint64                   `json:"connection"`
	Query      string                   `json:"query"`
	Parameters []domainplugin.DataValue `json:"parameters,omitempty"`
}

// open runs one guest open. A test open is not pooled; the caller closes any
// connection the guest returns.
func (p *DataProvider) open(ctx context.Context, request dataOpenRequest) (dataResponse, error) {
	payload := dataOpenPayload{
		ProfileID: request.Profile.ID,
		Provider:  request.Profile.Provider,
		Label:     boundedDataText(request.Profile.Label, domainplugin.MaxDataProfileLabel),
		Mode:      string(request.Profile.Mode),
		ViaHost:   boundedDataText(request.Profile.ViaHost, domainplugin.MaxDataViaHost),
		Fields:    copyDataFields(request.Fields),
		Secrets:   copyDataFields(request.Secrets),
		Test:      request.Test,
	}
	if request.Profile.ViaHost != "" {
		if p.host.tunnel == nil {
			return dataResponse{}, &DataError{
				Kind: DataErrorUnsupported, Provider: p.qualifiedID,
				Message: "the profile names a Host tunnel but the host provides none",
			}
		}
		tunnel, err := p.host.tunnel.Tunnel(ctx, request.Profile.ViaHost)
		if err != nil {
			return dataResponse{}, &DataError{
				Kind: DataErrorUnavailable, Provider: p.qualifiedID,
				Message: "the host tunnel is unavailable",
			}
		}
		payload.Tunnel = &DataTunnel{Host: tunnel.Host, Endpoint: tunnel.Endpoint}
	}
	return p.invoke(ctx, domainpluginabi.DataProviderMethodOpen, payload,
		p.host.limits.OpenTimeout, newDataSecretGuard(request.Secrets))
}

// closeConnection asks the guest to release one connection exactly once. The
// caller either owns the teardown claim under the host mutex or owns a token
// that was never published, so this is the single close for the token. It
// deliberately bypasses ready(): Unregister and host.Close may mark the shared
// instance inactive after the claim and before this call, and refusing the
// guest call then would lose the close while the audit row is still written.
//
// The call is best-effort: errors are ignored because a claimed connection
// cannot be retried. It reports whether the guest call was actually attempted,
// so the caller writes the close audit row only for a real attempt.
func (p *DataProvider) closeConnection(token uint64) bool {
	if p == nil || p.host == nil || p.instance == nil || token == 0 {
		return false
	}
	payload, err := json.Marshal(dataConnectionPayload{Connection: token})
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.host.limits.CallTimeout)
	defer cancel()
	_, _ = p.instance.invoker.InvokeDataProvider(ctx, DataProviderRequest{
		ID: p.host.next.Next(), Method: domainpluginabi.DataProviderMethodClose,
		Payload: payload,
	})
	return true
}

// invoke runs one data-provider method and returns the decoded envelope. guard
// carries the connection's resolved secrets so a provider message can never
// echo one back to the host.
func (p *DataProvider) invoke(ctx context.Context, method domainpluginabi.DataProviderMethod,
	value any, timeout time.Duration, guard *dataSecretGuard) (dataResponse, error) {

	if err := p.ready(); err != nil {
		return dataResponse{}, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return dataResponse{}, p.protocolError("encode request")
	}
	if len(payload) > MaxDataRequestBytes {
		return dataResponse{}, &DataError{
			Kind: DataErrorLimit, Provider: p.qualifiedID,
			Message: "request exceeds the transport limit",
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return dataResponse{}, p.cancelledError("request was cancelled")
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	requestID := p.host.next.Next()
	watchDone := make(chan struct{})
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				p.instance.cancelRequest(requestID)
			case <-watchDone:
			case <-callCtx.Done():
			}
		}()
	}
	raw, callErr := p.instance.invoker.InvokeDataProvider(callCtx, DataProviderRequest{
		ID: requestID, Method: method, Payload: payload,
	})
	close(watchDone)
	cancelledErr := ctx.Err() != nil
	if callErr != nil {
		if dataErrorIsFatal(callErr) {
			p.instance.markInactive()
		}
		if cancelledErr {
			return dataResponse{}, p.cancelledError("request was cancelled")
		}
		return dataResponse{}, p.mapInvokeError(callErr, guard)
	}
	response, decodeErr := decodeDataResponse(raw, p.qualifiedID)
	if decodeErr != nil {
		return dataResponse{}, decodeErr
	}
	if cancelledErr {
		response.cancelled = true
		if response.Result != nil {
			response.Result.Partial = true
			response.Result.More = false
		}
	}
	if err := p.checkResponse(response, guard); err != nil {
		// A response that arrived after cancellation is still a cancellation,
		// not a provider failure the UI should surface as an error.
		var dataErr *DataError
		if response.cancelled && errors.As(err, &dataErr) &&
			dataErr.Kind == DataErrorCancelled {
			return response, nil
		}
		return response, err
	}
	return response, nil
}

// checkResponse maps a guest status string to the closed error vocabulary.
func (p *DataProvider) checkResponse(response dataResponse, guard *dataSecretGuard) error {
	status := strings.TrimSpace(response.Status)
	if status == "" {
		status = strings.TrimSpace(response.Error)
	}
	return mapDataStatus(p.qualifiedID, status, response.Message, guard)
}

// mapDataStatus maps a provider status and message onto the closed error
// vocabulary. The message is bounded first: the caller-visible text is the
// exact text the guard must judge. A secret beyond the bound is never returned,
// so an oversized or discarded provider value cannot switch scanning off for
// the text that is. A message carrying a secret is replaced with a constant
// protocol error; it is never delivered with the secret redacted in place.
func mapDataStatus(providerID, status, message string, guard *dataSecretGuard) error {
	status = strings.TrimSpace(status)
	if status == "" || status == "ok" {
		return nil
	}
	message = boundedDataText(message, 256)
	if guard.echoes(message) {
		return &DataError{
			Kind: DataErrorProtocol, Provider: providerID,
			Message: "the provider echoed secret content",
		}
	}
	kind, known := DataErrorKindByName(status)
	if !known {
		kind = DataErrorProtocol
	}
	return &DataError{Kind: kind, Provider: providerID, Message: message}
}

func (p *DataProvider) ready() error {
	if p == nil || p.host == nil || p.instance == nil {
		return ErrClosed
	}
	if !p.instance.activeState() {
		return &DataError{
			Kind: DataErrorFaulted, Provider: p.qualifiedID,
			Message: "provider is not active",
		}
	}
	return nil
}

func (p *DataProvider) protocolError(message string) error {
	return &DataError{Kind: DataErrorProtocol, Provider: p.qualifiedID, Message: message}
}

// secretEchoError is the constant fail-closed error for text that decodes or
// folds to a resolved secret. It never carries the candidate text.
func (p *DataProvider) secretEchoError() error {
	return p.protocolError("the provider echoed secret content")
}

func (p *DataProvider) cancelledError(message string) error {
	return &DataError{Kind: DataErrorCancelled, Provider: p.qualifiedID, Message: message}
}

func (p *DataProvider) unsupportedError(message string) error {
	return &DataError{Kind: DataErrorUnsupported, Provider: p.qualifiedID, Message: message}
}

func (p *DataProvider) requireFeature(feature domainplugin.DataFeature) error {
	if !p.contribution.Capabilities.Supports(feature) {
		return p.unsupportedError("provider does not declare " + string(feature))
	}
	return nil
}

func (p *DataProvider) mapInvokeError(err error, guard *dataSecretGuard) error {
	// ErrCallInFlight is a host control signal, not a provider failure. Callers
	// such as Cancel detect it with errors.Is, so it is returned unwrapped.
	if errors.Is(err, ErrCallInFlight) {
		return err
	}
	var dataErr *DataError
	if errors.As(err, &dataErr) {
		// The message is returned to the caller as-is, so bound it before the
		// scan and return the same bounded text the guard judged.
		message := boundedDataText(dataErr.Message, 256)
		if guard.echoes(message) {
			return p.protocolError("the provider echoed secret content")
		}
		if message == dataErr.Message {
			return dataErr
		}
		return &DataError{
			Kind: dataErr.Kind, Provider: dataErr.Provider, Message: message,
		}
	}
	// Bound first: only the exact text that can reach the caller is scanned.
	message := boundedDataText(err.Error(), 256)
	if guard.echoes(message) {
		return p.protocolError("the provider echoed secret content")
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &DataError{Kind: DataErrorTimeout, Provider: p.qualifiedID, Message: message}
	case errors.Is(err, context.Canceled):
		return &DataError{Kind: DataErrorCancelled, Provider: p.qualifiedID, Message: message}
	case errors.Is(err, ErrNotActive), errors.Is(err, ErrClosed), IsUnusable(err):
		return &DataError{Kind: DataErrorFaulted, Provider: p.qualifiedID, Message: message}
	default:
		return &DataError{Kind: DataErrorProtocol, Provider: p.qualifiedID, Message: message}
	}
}

func dataErrorIsFatal(err error) bool {
	if errors.Is(err, ErrNotActive) || errors.Is(err, ErrClosed) || IsUnusable(err) {
		return true
	}
	var dataErr *DataError
	return errors.As(err, &dataErr) && dataErr.Kind == DataErrorFaulted
}

func decodeDataResponse(payload []byte, providerID string) (dataResponse, error) {
	if len(payload) == 0 {
		return dataResponse{}, &DataError{
			Kind: DataErrorProtocol, Provider: providerID,
			Message: "provider returned no response",
		}
	}
	if len(payload) > MaxDataResponseBytes {
		return dataResponse{}, &DataError{
			Kind: DataErrorProtocol, Provider: providerID,
			Message: "provider response is too large",
		}
	}
	var response dataResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return dataResponse{}, &DataError{
			Kind: DataErrorProtocol, Provider: providerID,
			Message: "malformed provider response",
		}
	}
	return response, nil
}

func boundedDataText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max]
}

func nilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// dataProviderInstance is the one module-backed data provider for a fiber,
// shared by every provider and connection it serves.
type dataProviderInstance struct {
	host    *DataProviderHost
	fiberID domainplugin.FiberID
	ref     PluginRef
	invoker DataProviderInvoker

	mu        sync.Mutex
	active    bool
	cancelled [dataCancelWindow]uint64
	cancelAt  int
	cancelLen int
}

func newDataProviderInstance(host *DataProviderHost, fiberID domainplugin.FiberID,
	ref PluginRef, invoker DataProviderInvoker) *dataProviderInstance {

	return &dataProviderInstance{
		host: host, fiberID: fiberID, ref: ref, invoker: invoker, active: true,
	}
}

func (i *dataProviderInstance) activeState() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.active
}

func (i *dataProviderInstance) markInactive() {
	i.mu.Lock()
	i.active = false
	i.mu.Unlock()
}

func (i *dataProviderInstance) cancelRequest(requestID uint64) {
	i.mu.Lock()
	i.cancelled[i.cancelAt] = requestID
	i.cancelAt = (i.cancelAt + 1) % dataCancelWindow
	if i.cancelLen < dataCancelWindow {
		i.cancelLen++
	}
	i.mu.Unlock()
}

func (i *dataProviderInstance) cancelRequested(requestID uint64) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for index := 0; index < i.cancelLen; index++ {
		if i.cancelled[index] == requestID {
			return true
		}
	}
	return false
}

// recordDataEvent appends one content-free audit row. Access carries the
// profile mode, Class the provider's reported operation class, and Bytes a
// size only.
func (h *DataProviderHost) recordDataEvent(provider *DataProvider, action, result string,
	mode domainplugin.DataAccessMode, class domainplugin.DataOperationClass,
	elapsed time.Duration, bytes int) {

	if h == nil || h.audit == nil || provider == nil {
		return
	}
	event := domainplugin.AuditEvent{
		Time:     h.now().UTC(),
		Event:    domainplugin.AuditData,
		PluginID: provider.instance.ref.ID,
		Provider: provider.qualifiedID,
		Reason:   domainplugin.DataAuditReason(action, result),
		Status:   result,
		Access:   string(mode),
		// The class is restricted to the host's closed vocabulary here as a
		// final backstop: no provider-controlled text reaches the audit sink.
		Class: dataAuditClass(class),
	}
	if elapsed > 0 {
		event.DurationMS = elapsed.Milliseconds()
	}
	if bytes > 0 {
		event.Bytes = bytes
	}
	h.audit.Record(event)
}

func (h *DataProviderHost) auditDataClose(connection *DataConnection, result string) {
	if connection == nil {
		return
	}
	h.recordDataEvent(connection.provider, "close", result, connection.mode, "", 0, 0)
}
