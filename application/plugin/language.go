package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Language-service limits from the editor contract. The document snapshot is
// the only place document content crosses into a provider; every result shape
// is bounded so a provider cannot grow host memory or the render cost without
// limit.
const (
	// MaxLanguageDocumentBytes is the per-document snapshot cap.
	MaxLanguageDocumentBytes = 1 << 20

	// MaxLanguageCompletionItems, MaxLanguageDiagnostics, and
	// MaxLanguageFormattingEdits bound the collection shapes.
	MaxLanguageCompletionItems = 1000
	MaxLanguageDiagnostics     = 500
	MaxLanguageFormattingEdits = 1000
	// MaxLanguageFormattingBytes bounds the total new text in one formatting
	// result.
	MaxLanguageFormattingBytes = 512 << 10
	// MaxLanguageHoverBytes bounds one hover result.
	MaxLanguageHoverBytes = 8 << 10
	// MaxLanguageHighlights bounds one highlight result after stacking.
	MaxLanguageHighlights = 20000
	// MaxLanguageSignatures and MaxLanguageSignatureParameters bound
	// signature help.
	MaxLanguageSignatures          = 8
	MaxLanguageSignatureParameters = 64
	MaxLanguageLocations           = 256
	MaxLanguageCodeActions         = 100
	MaxLanguageResponseBytes       = 1 << 20
	MaxLanguageDocumentIDBytes     = 256
	MaxLanguageProviderLabelBytes  = 256
	MaxLanguageDetailBytes         = 1 << 10
	MaxLanguageDocumentationBytes  = 8 << 10
	MaxLanguageMessageBytes        = 8 << 10
	MaxLanguageInsertTextBytes     = 4 << 10
	MaxLanguageContextValueBytes   = 4 << 10
)

// LanguageTimings carries the debounce windows. The editor schedules
// completion and diagnostics after the user stops typing.
type LanguageTimings struct {
	CompletionDebounce  time.Duration
	DiagnosticsDebounce time.Duration
}

// DefaultLanguageTimings returns the contract debounce windows.
func DefaultLanguageTimings() LanguageTimings {
	return LanguageTimings{
		CompletionDebounce:  150 * time.Millisecond,
		DiagnosticsDebounce: 300 * time.Millisecond,
	}
}

func (t LanguageTimings) normalize() LanguageTimings {
	defaults := DefaultLanguageTimings()
	if t.CompletionDebounce <= 0 {
		t.CompletionDebounce = defaults.CompletionDebounce
	}
	if t.DiagnosticsDebounce <= 0 {
		t.DiagnosticsDebounce = defaults.DiagnosticsDebounce
	}
	return t
}

// LanguageDeadline returns the per-capability host deadline. It mirrors the
// editor contract: completion is fast, diagnostics may take a second, and
// formatting may take two.
func LanguageDeadline(capability domainplugin.LanguageCapability) time.Duration {
	switch capability {
	case domainplugin.LanguageHighlight:
		return 100 * time.Millisecond
	case domainplugin.LanguageCompletion:
		return 200 * time.Millisecond
	case domainplugin.LanguageDiagnostics:
		return time.Second
	case domainplugin.LanguageFormatting:
		return 2 * time.Second
	case domainplugin.LanguageHover:
		return 200 * time.Millisecond
	case domainplugin.LanguageSignatureHelp:
		return 200 * time.Millisecond
	case domainplugin.LanguageNavigation:
		return 500 * time.Millisecond
	case domainplugin.LanguageCodeActions:
		return time.Second
	default:
		return time.Second
	}
}

// LanguagePosition is a zero-based line and column in the document.
type LanguagePosition struct {
	Line   uint32 `json:"line"`
	Column uint32 `json:"column"`
}

// LanguageRange is a half-open range in the document.
type LanguageRange struct {
	Start LanguagePosition `json:"start"`
	End   LanguagePosition `json:"end"`
}

// LanguageSelection is the editor's optional selection.
type LanguageSelection struct {
	Start LanguagePosition `json:"start"`
	End   LanguagePosition `json:"end"`
}

// LanguageContext is the opaque editor binding handed to providers. Kind is
// matched against a provider's declared contexts; Value is passed through
// unchanged.
type LanguageContext struct {
	Kind  string `json:"kind,omitempty"`
	Value string `json:"value,omitempty"`
}

// LanguageDocument is a full snapshot. The host owns version generation; a
// caller never supplies it. Content is capped at MaxLanguageDocumentBytes.
type LanguageDocument struct {
	ID         string
	LanguageID string
	Content    string
	Cursor     LanguagePosition
	Selection  *LanguageSelection
	Context    LanguageContext
}

// LanguageHighlight is one highlighted range. Kind is a closed token name.
type LanguageHighlight struct {
	Range LanguageRange `json:"range"`
	Kind  string        `json:"kind"`
}

// LanguageCompletionItem is one completion candidate.
type LanguageCompletionItem struct {
	Label         string `json:"label"`
	InsertText    string `json:"insertText,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Documentation string `json:"documentation,omitempty"`
	SortKey       string `json:"sortKey,omitempty"`
	FilterKey     string `json:"filterKey,omitempty"`
}

// LanguageDiagnostic is one editor diagnostic.
type LanguageDiagnostic struct {
	Range    LanguageRange `json:"range"`
	Severity int           `json:"severity"`
	Code     string        `json:"code,omitempty"`
	Message  string        `json:"message"`
	Source   string        `json:"source,omitempty"`
}

// Diagnostic severities. They are a closed set so the host can render them
// consistently.
const (
	LanguageSeverityError       = 1
	LanguageSeverityWarning     = 2
	LanguageSeverityInformation = 3
	LanguageSeverityHint        = 4
)

// LanguageTextEdit replaces one range with plain text.
type LanguageTextEdit struct {
	Range   LanguageRange `json:"range"`
	NewText string        `json:"newText"`
}

// LanguageHover is plain text or restricted markdown. HTML is never accepted;
// markdown is sanitized host-side.
type LanguageHover struct {
	Contents string `json:"contents"`
	Format   string `json:"format,omitempty"`
}

// LanguageSignatureParameter is one parameter hint.
type LanguageSignatureParameter struct {
	Label         string `json:"label"`
	Documentation string `json:"documentation,omitempty"`
}

// LanguageSignature is one callable signature.
type LanguageSignature struct {
	Label           string                       `json:"label"`
	Documentation   string                       `json:"documentation,omitempty"`
	Parameters      []LanguageSignatureParameter `json:"parameters,omitempty"`
	ActiveParameter int                          `json:"activeParameter,omitempty"`
}

// LanguageSignatureHelp is the signature-help result.
type LanguageSignatureHelp struct {
	Signatures      []LanguageSignature `json:"signatures"`
	ActiveSignature int                 `json:"activeSignature,omitempty"`
}

// LanguageLocation is one same-document navigation target.
type LanguageLocation struct {
	DocumentID string        `json:"documentId"`
	Range      LanguageRange `json:"range"`
	Label      string        `json:"label,omitempty"`
}

// LanguageCodeAction is one available code action. It returns an edit set,
// never HTML or a callback.
type LanguageCodeAction struct {
	Title string             `json:"title"`
	Kind  string             `json:"kind,omitempty"`
	Edits []LanguageTextEdit `json:"edits,omitempty"`
}

// LanguageProviderFailure reports one provider that could not answer. The
// failure is isolated: other providers and the built-in layer still produce
// results.
type LanguageProviderFailure struct {
	PluginID   string                          `json:"pluginId"`
	ProviderID string                          `json:"providerId"`
	Fiber      domainplugin.FiberID            `json:"fiber"`
	Capability domainplugin.LanguageCapability `json:"capability"`
	Code       string                          `json:"code"`
	Message    string                          `json:"message,omitempty"`
}

// LanguageDocumentResults is one merged result batch. Exactly one capability
// is populated per batch. Version identifies the snapshot the result belongs
// to; a caller must discard a result whose version is no longer current.
type LanguageDocumentResults struct {
	DocumentID  string                          `json:"documentId"`
	Version     uint64                          `json:"version"`
	Capability  domainplugin.LanguageCapability `json:"capability"`
	Highlights  []LanguageHighlight             `json:"highlights,omitempty"`
	Completions []LanguageCompletionItem        `json:"completions,omitempty"`
	Diagnostics []LanguageDiagnostic            `json:"diagnostics,omitempty"`
	Formatting  []LanguageTextEdit              `json:"formatting,omitempty"`
	Hover       *LanguageHover                  `json:"hover,omitempty"`
	Signature   *LanguageSignatureHelp          `json:"signatureHelp,omitempty"`
	Navigation  []LanguageLocation              `json:"navigation,omitempty"`
	CodeActions []LanguageCodeAction            `json:"codeActions,omitempty"`
	Truncated   bool                            `json:"truncated,omitempty"`
	Providers   int                             `json:"providers"`
	Failures    []LanguageProviderFailure       `json:"failures,omitempty"`
	GeneratedAt time.Time                       `json:"generatedAt"`
}

// LanguageProviderStatus describes one registered provider for diagnostics.
type LanguageProviderStatus struct {
	PluginID    string
	ProviderID  string
	Fiber       domainplugin.FiberID
	Priority    int
	Languages   []string
	Degraded    bool
	FailureCode string
	FailureText string
}

// LanguageInvoker is the per-fiber port the router uses to call the guest. A
// fiber implements it over the container's call serialization.
type LanguageInvoker interface {
	InvokeLanguage(ctx context.Context, request LanguageRequest) ([]byte, error)
	CancelLanguage(ctx context.Context, requestID uint64) error
}

// LanguageRegistrationError reports a language contribution the host rejects.
type LanguageRegistrationError struct {
	PluginID   string
	ProviderID string
	Problem    string
}

func (e *LanguageRegistrationError) Error() string {
	return fmt.Sprintf("plugin %s language provider %q: %s", e.PluginID, e.ProviderID, e.Problem)
}

// LanguageDocumentNotFoundError reports a request for a document the host does
// not track.
type LanguageDocumentNotFoundError struct {
	DocumentID string
}

func (e *LanguageDocumentNotFoundError) Error() string {
	return "language: document " + e.DocumentID + " is not open"
}

// LanguageDocumentExistsError reports an Open for an already open document.
type LanguageDocumentExistsError struct {
	DocumentID string
}

func (e *LanguageDocumentExistsError) Error() string {
	return "language: document " + e.DocumentID + " is already open"
}

// LanguageScheduler schedules one debounce callback and is the host's only
// source of time for re-arming language work. The default implementation uses
// time.AfterFunc; tests inject a deterministic clock through it so no assertion
// depends on wall-clock sleeps. Implementations must be safe for concurrent
// use.
type LanguageScheduler interface {
	AfterFunc(delay time.Duration, callback func()) LanguageTimer
}

// LanguageTimer is one scheduled debounce callback.
type LanguageTimer interface {
	Stop() bool
}

// realLanguageScheduler is the production scheduler.
type realLanguageScheduler struct{}

func (realLanguageScheduler) AfterFunc(delay time.Duration,
	callback func()) LanguageTimer {
	return time.AfterFunc(delay, callback)
}

// LanguageHostOptions wires the language router. Authority is required: a
// provider is registered only when editor.language is granted.
type LanguageHostOptions struct {
	Authority *Authority
	Audit     AuditSink
	Now       func() time.Time
	Timings   LanguageTimings
	Builtin   LanguageBuiltin
	// Scheduler arms the debounce timers. Nil selects real time.
	Scheduler LanguageScheduler
}

type languageProvider struct {
	ref      PluginRef
	fiber    domainplugin.FiberID
	contract domainplugin.LanguageProvider
	invoker  LanguageInvoker
	sequence uint64
}

func (p *languageProvider) key() string { return string(p.fiber) + "/" + p.contract.ID }

// LanguageHost routes document snapshots to registered providers, merges their
// data-only results, and keeps the built-in layer underneath. It never parses
// the document language; providers own indexing and language semantics.
type LanguageHost struct {
	authority *Authority
	audit     AuditSink
	now       func() time.Time
	timings   LanguageTimings
	builtin   LanguageBuiltin
	scheduler LanguageScheduler

	mu        sync.Mutex
	providers map[string]*languageProvider
	docs      map[string]*languageDocumentState
	sequence  uint64
	closed    bool

	nextRequest atomic.Uint64

	cancelMu sync.Mutex
	canceled map[uint64]domainplugin.FiberID

	degradedMu sync.Mutex
	degraded   map[string]languageProviderFailure
}

type languageDocumentState struct {
	mu       sync.Mutex
	snapshot LanguageDocument
	version  uint64
	epoch    uint64
	closed   bool
	results  map[domainplugin.LanguageCapability]LanguageDocumentResults
	timers   map[domainplugin.LanguageCapability]LanguageTimer
	inflight map[uint64]languageInflight
	// superseded records requests a newer snapshot canceled. It is updated
	// under the same lock that removes the in-flight entry, so the call's
	// return path always observes the cancellation that happened while it
	// ran, without a separate map lookup racing the clear.
	superseded map[uint64]bool
}

type languageInflight struct {
	key   string
	fiber domainplugin.FiberID
}

type languageProviderFailure struct {
	code    string
	message string
}

// NewLanguageHost returns a router over the supplied authority and audit sink.
func NewLanguageHost(options LanguageHostOptions) *LanguageHost {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	audit := options.Audit
	if audit == nil {
		audit = NopAudit{}
	}
	scheduler := options.Scheduler
	if scheduler == nil {
		scheduler = realLanguageScheduler{}
	}
	return &LanguageHost{
		authority: options.Authority,
		audit:     audit,
		now:       now,
		timings:   options.Timings.normalize(),
		builtin:   options.Builtin,
		scheduler: scheduler,
		providers: make(map[string]*languageProvider),
		docs:      make(map[string]*languageDocumentState),
		canceled:  make(map[uint64]domainplugin.FiberID),
		degraded:  make(map[string]languageProviderFailure),
	}
}

// Timings returns the effective debounce windows.
func (h *LanguageHost) Timings() LanguageTimings { return h.timings }

// Register adds one provider declaration for a fiber. It checks the closed
// contribution grammar again, verifies that editor.language is granted, and
// enforces the built-in add-only/plugin-owned exclusive rule. The returned
// disposer removes the provider and its results.
func (h *LanguageHost) Register(ref PluginRef, fiber domainplugin.FiberID,
	contract domainplugin.LanguageProvider, invoker LanguageInvoker) (Disposer, error) {
	if h == nil {
		return nil, errors.New("language: router is not configured")
	}
	if invoker == nil {
		return nil, &LanguageRegistrationError{
			PluginID: ref.ID, ProviderID: contract.ID,
			Problem: "the plugin instance does not implement the language provider exports",
		}
	}
	if err := contract.Validate(ref.ID, "language.provider"); err != nil {
		return nil, &LanguageRegistrationError{
			PluginID: ref.ID, ProviderID: contract.ID, Problem: err.Error(),
		}
	}
	for _, language := range contract.Languages {
		if !domainplugin.IsBuiltinLanguageID(language) &&
			!domainplugin.IsPluginOwnedLanguageID(ref.ID, language) {
			return nil, &LanguageRegistrationError{
				PluginID: ref.ID, ProviderID: contract.ID,
				Problem: fmt.Sprintf("language %q is not owned by plugin %q", language, ref.ID),
			}
		}
	}
	if h.authority != nil {
		if err := h.authority.Check(SubjectForRef(ref),
			domainplugin.PermissionEditorLanguage, domainplugin.Scope{}); err != nil {
			h.record(ref, "register", "denied")
			return nil, &ServiceError{
				Service: "language", Method: "register", Code: ServiceCodeDenied,
				Permission: domainplugin.PermissionEditorLanguage, Message: err.Error(), Cause: err,
			}
		}
	} else {
		return nil, &ServiceError{
			Service: "language", Method: "register", Code: ServiceCodeDenied,
			Permission: domainplugin.PermissionEditorLanguage,
			Message:    ErrPermissionNotGranted.Error(), Cause: ErrPermissionNotGranted,
		}
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	provider := &languageProvider{
		ref: ref, fiber: fiber, contract: cloneLanguageProvider(contract),
		invoker: invoker,
	}
	provider.sequence = h.sequence
	h.sequence++
	key := provider.key()
	if _, exists := h.providers[key]; exists {
		h.mu.Unlock()
		return nil, &LanguageRegistrationError{
			PluginID: ref.ID, ProviderID: contract.ID,
			Problem: "a provider with this id is already registered for the fiber",
		}
	}
	if problem := h.languageOwnershipProblem(ref, contract); problem != "" {
		h.mu.Unlock()
		return nil, &LanguageRegistrationError{
			PluginID: ref.ID, ProviderID: contract.ID, Problem: problem,
		}
	}
	h.providers[key] = provider
	h.mu.Unlock()
	h.record(ref, "register", ServiceCodeOK)
	h.refreshOpenDocuments()

	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			h.mu.Lock()
			delete(h.providers, key)
			h.mu.Unlock()
			h.degradedMu.Lock()
			delete(h.degraded, key)
			h.degradedMu.Unlock()
			// An unloaded provider's output must not linger in the editor.
			// Recomputing every open document republishes the built-in layer
			// and whatever providers remain.
			h.refreshOpenDocuments()
		})
		return nil
	}, nil
}

// languageOwnershipProblem reports the first plugin-owned language identifier
// another registered plugin already claimed. Built-in identifiers are
// add-only, so any number of plugins may serve the same built-in language. A
// plugin-owned identifier belongs to the first plugin that registers it:
// because plugin identifiers themselves contain dots, "acme" claiming
// "acme.hello.rune" and "acme.hello" claiming the same identifier both pass
// their own prefix check, so the registered provider set must be the
// authority. The caller holds h.mu.
func (h *LanguageHost) languageOwnershipProblem(ref PluginRef,
	contract domainplugin.LanguageProvider) string {
	for _, language := range contract.Languages {
		if domainplugin.IsBuiltinLanguageID(language) {
			continue
		}
		for _, existing := range h.providers {
			if existing.ref.ID == ref.ID {
				continue
			}
			for _, claimed := range existing.contract.Languages {
				if claimed == language {
					return fmt.Sprintf("language %q is already owned by plugin %q",
						language, existing.ref.ID)
				}
			}
		}
	}
	return ""
}

// refreshOpenDocuments drops the stored results of every open document and
// re-arms the debounced capabilities. It runs after the provider set changes
// so an unloaded provider disappears immediately and a newly registered one
// gets a chance to answer.
func (h *LanguageHost) refreshOpenDocuments() {
	h.mu.Lock()
	docs := make([]*languageDocumentState, 0, len(h.docs))
	for _, state := range h.docs {
		docs = append(docs, state)
	}
	h.mu.Unlock()
	for _, state := range docs {
		state.mu.Lock()
		if state.closed {
			state.mu.Unlock()
			continue
		}
		state.epoch++
		state.results = make(map[domainplugin.LanguageCapability]LanguageDocumentResults)
		state.mu.Unlock()
		h.cancelInflight(state)
		h.schedule(state, domainplugin.LanguageCompletion, h.timings.CompletionDebounce)
		h.schedule(state, domainplugin.LanguageDiagnostics, h.timings.DiagnosticsDebounce)
	}
}

// Open registers a document and returns its first version. Completion and
// diagnostics are scheduled with the contract debounce.
func (h *LanguageHost) Open(document LanguageDocument) (uint64, error) {
	if h == nil {
		return 0, errors.New("language: router is not configured")
	}
	if err := validateLanguageDocument(document); err != nil {
		return 0, err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, ErrClosed
	}
	if _, exists := h.docs[document.ID]; exists {
		h.mu.Unlock()
		return 0, &LanguageDocumentExistsError{DocumentID: document.ID}
	}
	state := &languageDocumentState{
		snapshot:   cloneLanguageDocument(document),
		version:    1,
		results:    make(map[domainplugin.LanguageCapability]LanguageDocumentResults),
		timers:     make(map[domainplugin.LanguageCapability]LanguageTimer),
		inflight:   make(map[uint64]languageInflight),
		superseded: make(map[uint64]bool),
	}
	h.docs[document.ID] = state
	h.mu.Unlock()
	h.schedule(state, domainplugin.LanguageCompletion, h.timings.CompletionDebounce)
	h.schedule(state, domainplugin.LanguageDiagnostics, h.timings.DiagnosticsDebounce)
	return 1, nil
}

// Update replaces the snapshot, bumps the version monotonically, cancels the
// previous pending work, and reschedules the debounced capabilities.
func (h *LanguageHost) Update(document LanguageDocument) (uint64, error) {
	if h == nil {
		return 0, errors.New("language: router is not configured")
	}
	if err := validateLanguageDocument(document); err != nil {
		return 0, err
	}
	h.mu.Lock()
	state := h.docs[document.ID]
	if state == nil {
		h.mu.Unlock()
		return 0, &LanguageDocumentNotFoundError{DocumentID: document.ID}
	}
	h.mu.Unlock()

	state.mu.Lock()
	state.version++
	version := state.version
	state.epoch++
	state.snapshot = cloneLanguageDocument(document)
	for _, timer := range state.timers {
		if timer != nil {
			timer.Stop()
		}
	}
	// Only the current document version may publish results. Any provider
	// response still in flight is stale, so its result is discarded.
	state.results = make(map[domainplugin.LanguageCapability]LanguageDocumentResults)
	state.mu.Unlock()

	h.cancelInflight(state)
	h.schedule(state, domainplugin.LanguageCompletion, h.timings.CompletionDebounce)
	h.schedule(state, domainplugin.LanguageDiagnostics, h.timings.DiagnosticsDebounce)
	return version, nil
}

// SetPosition updates the cursor and selection without bumping the document
// version. The next debounced completion uses the new position.
func (h *LanguageHost) SetPosition(documentID string, cursor LanguagePosition,
	selection *LanguageSelection) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	state := h.docs[documentID]
	h.mu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	state.snapshot.Cursor = cursor
	if selection == nil {
		state.snapshot.Selection = nil
	} else {
		copy := *selection
		state.snapshot.Selection = &copy
	}
	state.epoch++
	state.mu.Unlock()
	// Both debounced capabilities carry the snapshot, including the cursor and
	// selection, so the position change must re-arm diagnostics as well as
	// completion under the new epoch. Re-arming only completion leaves the
	// pending diagnostics timer pointing at the pre-move epoch, and it exits
	// without ever publishing.
	h.schedule(state, domainplugin.LanguageCompletion, h.timings.CompletionDebounce)
	h.schedule(state, domainplugin.LanguageDiagnostics, h.timings.DiagnosticsDebounce)
	return true
}

// CloseDocument stops timers, cancels in-flight requests, and drops the
// document's results.
func (h *LanguageHost) CloseDocument(documentID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	state := h.docs[documentID]
	delete(h.docs, documentID)
	h.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	state.closed = true
	for _, timer := range state.timers {
		if timer != nil {
			timer.Stop()
		}
	}
	state.timers = make(map[domainplugin.LanguageCapability]LanguageTimer)
	state.results = make(map[domainplugin.LanguageCapability]LanguageDocumentResults)
	state.mu.Unlock()
	h.cancelInflight(state)
}

// Version returns the current monotonic version of one document.
func (h *LanguageHost) Version(documentID string) (uint64, bool) {
	if h == nil {
		return 0, false
	}
	h.mu.Lock()
	state := h.docs[documentID]
	h.mu.Unlock()
	if state == nil {
		return 0, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.version, true
}

// Results returns the latest merged result batch for one document and
// capability.
func (h *LanguageHost) Results(documentID string,
	capability domainplugin.LanguageCapability) (LanguageDocumentResults, bool) {
	if h == nil {
		return LanguageDocumentResults{}, false
	}
	h.mu.Lock()
	state := h.docs[documentID]
	h.mu.Unlock()
	if state == nil {
		return LanguageDocumentResults{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	result, ok := state.results[capability]
	return cloneLanguageResults(result), ok
}

// AllResults returns every stored result batch for one document.
func (h *LanguageHost) AllResults(documentID string) map[domainplugin.LanguageCapability]LanguageDocumentResults {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	state := h.docs[documentID]
	h.mu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	out := make(map[domainplugin.LanguageCapability]LanguageDocumentResults, len(state.results))
	for capability, result := range state.results {
		out[capability] = cloneLanguageResults(result)
	}
	return out
}

// Request routes one capability synchronously to every matching provider and
// returns the merged result. Callers on the UI thread should run it off that
// thread; the debounced path already does.
func (h *LanguageHost) Request(ctx context.Context,
	documentID string, capability domainplugin.LanguageCapability) (LanguageDocumentResults, error) {
	if h == nil {
		return LanguageDocumentResults{}, errors.New("language: router is not configured")
	}
	if !domainplugin.IsLanguageCapability(string(capability)) {
		return LanguageDocumentResults{}, &LanguageRegistrationError{
			Problem: fmt.Sprintf("unknown language capability %q", capability),
		}
	}
	h.mu.Lock()
	state := h.docs[documentID]
	h.mu.Unlock()
	if state == nil {
		return LanguageDocumentResults{}, &LanguageDocumentNotFoundError{DocumentID: documentID}
	}
	state.mu.Lock()
	snapshot := cloneLanguageDocument(state.snapshot)
	version := state.version
	state.mu.Unlock()
	result := h.route(ctx, state, snapshot, version, capability)
	state.mu.Lock()
	if state.version == result.Version {
		state.results[capability] = cloneLanguageResults(result)
	}
	state.mu.Unlock()
	return result, nil
}

// schedule arms one debounce timer. A newer Update or SetPosition stops the
// previous timer and increments the epoch, so a timer that already fired
// cannot publish a stale result.
func (h *LanguageHost) schedule(state *languageDocumentState,
	capability domainplugin.LanguageCapability, delay time.Duration) {
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return
	}
	epoch := state.epoch
	if previous := state.timers[capability]; previous != nil {
		previous.Stop()
	}
	timer := h.scheduler.AfterFunc(delay, func() {
		state.mu.Lock()
		if state.closed || state.epoch != epoch {
			state.mu.Unlock()
			return
		}
		snapshot := cloneLanguageDocument(state.snapshot)
		version := state.version
		state.timers[capability] = nil
		state.mu.Unlock()
		result := h.route(context.Background(), state, snapshot, version, capability)
		state.mu.Lock()
		if !state.closed && state.epoch == epoch && state.version == result.Version {
			state.results[capability] = cloneLanguageResults(result)
		}
		state.mu.Unlock()
	})
	state.timers[capability] = timer
	state.mu.Unlock()
}

// markCanceled records cooperative cancellation for an in-flight request. The
// guest may poll the language_cancelled import.
func (h *LanguageHost) markCanceled(requestID uint64, fiber domainplugin.FiberID) {
	if requestID == 0 {
		return
	}
	h.cancelMu.Lock()
	h.canceled[requestID] = fiber
	h.cancelMu.Unlock()
}

// Canceled reports whether requestID was superseded for fiber.
func (h *LanguageHost) Canceled(fiber domainplugin.FiberID, requestID uint64) bool {
	if h == nil {
		return false
	}
	h.cancelMu.Lock()
	defer h.cancelMu.Unlock()
	owner, ok := h.canceled[requestID]
	return ok && owner == fiber
}

// cancelInflight marks every request in state as canceled and asks each
// provider to discard it. The cancel export is best-effort because the
// request may still hold the single in-flight call slot.
func (h *LanguageHost) cancelInflight(state *languageDocumentState) {
	state.mu.Lock()
	requests := make(map[uint64]languageInflight, len(state.inflight))
	for requestID, request := range state.inflight {
		requests[requestID] = request
		// Supersession is recorded in the same critical section that drops the
		// in-flight entry. The provider call's return path then always sees
		// the cancellation that happened while it ran: it either finds the
		// request still in flight here, or this loop sees no request to mark.
		state.superseded[requestID] = true
		// The guest polls language_cancelled while its call is still running,
		// so record the fiber now rather than after the snapshot is taken.
		h.markCanceled(requestID, request.fiber)
	}
	state.inflight = make(map[uint64]languageInflight)
	state.mu.Unlock()

	h.mu.Lock()
	providers := make(map[uint64]*languageProvider, len(requests))
	for requestID, request := range requests {
		if provider := h.providers[request.key]; provider != nil {
			providers[requestID] = provider
		}
	}
	h.mu.Unlock()
	for requestID, provider := range providers {
		go func(provider *languageProvider, requestID uint64) {
			_ = provider.invoker.CancelLanguage(context.Background(), requestID)
		}(provider, requestID)
	}
}

// Route performs one merged request batch across the built-in layer and every
// matching provider.
func (h *LanguageHost) route(ctx context.Context, state *languageDocumentState,
	snapshot LanguageDocument, version uint64,
	capability domainplugin.LanguageCapability) LanguageDocumentResults {
	result := LanguageDocumentResults{
		DocumentID:  snapshot.ID,
		Version:     version,
		Capability:  capability,
		GeneratedAt: h.now().UTC(),
	}
	providers := h.matchingProviders(snapshot, capability)
	builtinResult := h.builtinResult(ctx, snapshot, capability)
	switch capability {
	case domainplugin.LanguageFormatting:
		// Formatting has exactly one winner: the highest-priority plugin
		// provider, or the built-in when no plugin provider exists. A failed
		// winner degrades only itself, so the built-in still formats.
		if len(providers) > 0 {
			chosen := providers[0]
			if h.isDegraded(chosen) {
				providers = nil
			} else {
				edits, truncated, failure := h.callFormatting(ctx, state, snapshot, version, chosen)
				if failure != nil {
					// A superseded or busy call is not a provider fault:
					// the next document version retries it. Only a real
					// failure degrades the provider.
					if failureDegrades(failure) {
						result.Failures = append(result.Failures, *failure)
						h.markDegraded(chosen, failure.Code, failure.Message)
					}
					providers = nil
				} else {
					result.Formatting = edits
					result.Truncated = truncated
					result.Providers = 1
				}
			}
		}
		if len(providers) == 0 {
			result.Formatting = builtinResult.Formatting
			if len(result.Formatting) > 0 {
				result.Providers = 1
			}
		}
		return result
	}

	h.mergeBuiltin(&result, builtinResult)
	for _, provider := range providers {
		if h.isDegraded(provider) {
			continue
		}
		response, failure := h.callProvider(ctx, state, snapshot, version, provider, capability)
		if failure != nil {
			// Cooperative cancellation and the single-call-slot busy state
			// are transient scheduling outcomes, not provider faults. They
			// are skipped without degrading the provider so rapid typing
			// cannot knock a healthy provider out for the session.
			if failureDegrades(failure) {
				result.Failures = append(result.Failures, *failure)
				h.markDegraded(provider, failure.Code, failure.Message)
			}
			continue
		}
		result.Providers++
		h.mergeProvider(&result, provider, response)
	}
	h.applyLanguageCaps(&result)
	return result
}

// failureDegrades reports whether a provider failure marks the provider
// degraded. A canceled request was superseded by a newer document version and
// a busy one lost the single in-flight call slot; both retry on the next
// version without being provider faults.
func failureDegrades(failure *LanguageProviderFailure) bool {
	return failure.Code != "canceled" && failure.Code != "busy"
}

// matchingProviders returns the providers that serve the document language,
// context, and capability, ordered by highest priority first and then by
// registration order.
func (h *LanguageHost) matchingProviders(snapshot LanguageDocument,
	capability domainplugin.LanguageCapability) []*languageProvider {
	h.mu.Lock()
	providers := make([]*languageProvider, 0, len(h.providers))
	for _, provider := range h.providers {
		providers = append(providers, provider)
	}
	h.mu.Unlock()
	matched := providers[:0]
	for _, provider := range providers {
		if !provider.contract.SupportsLanguage(snapshot.LanguageID, snapshot.Context.Kind) {
			continue
		}
		if !provider.contract.Supports(capability) {
			continue
		}
		matched = append(matched, provider)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].contract.Priority != matched[j].contract.Priority {
			return matched[i].contract.Priority > matched[j].contract.Priority
		}
		return matched[i].sequence < matched[j].sequence
	})
	return matched
}

// applyLanguageCaps enforces the result collection caps after stacking. String
// truncation happens while decoding one provider response; collection caps are
// applied globally here.
func (h *LanguageHost) applyLanguageCaps(result *LanguageDocumentResults) {
	switch result.Capability {
	case domainplugin.LanguageHighlight:
		if len(result.Highlights) > MaxLanguageHighlights {
			result.Highlights = result.Highlights[:MaxLanguageHighlights]
			result.Truncated = true
		}
	case domainplugin.LanguageCompletion:
		sort.SliceStable(result.Completions, func(i, j int) bool {
			left, right := result.Completions[i], result.Completions[j]
			if left.SortKey != right.SortKey {
				return left.SortKey < right.SortKey
			}
			return left.Label < right.Label
		})
		if len(result.Completions) > MaxLanguageCompletionItems {
			result.Completions = result.Completions[:MaxLanguageCompletionItems]
			result.Truncated = true
		}
	case domainplugin.LanguageDiagnostics:
		if len(result.Diagnostics) > MaxLanguageDiagnostics {
			result.Diagnostics = result.Diagnostics[:MaxLanguageDiagnostics]
			result.Truncated = true
		}
	case domainplugin.LanguageHover:
		if result.Hover != nil {
			contents, truncated := sanitizeLanguageText(result.Hover.Contents, MaxLanguageHoverBytes)
			result.Hover.Contents = contents
			result.Truncated = result.Truncated || truncated
		}
	case domainplugin.LanguageSignatureHelp:
		if result.Signature != nil && len(result.Signature.Signatures) > MaxLanguageSignatures {
			result.Signature.Signatures = result.Signature.Signatures[:MaxLanguageSignatures]
			result.Truncated = true
		}
	case domainplugin.LanguageNavigation:
		if len(result.Navigation) > MaxLanguageLocations {
			result.Navigation = result.Navigation[:MaxLanguageLocations]
			result.Truncated = true
		}
	case domainplugin.LanguageCodeActions:
		if len(result.CodeActions) > MaxLanguageCodeActions {
			result.CodeActions = result.CodeActions[:MaxLanguageCodeActions]
			result.Truncated = true
		}
	}
}

// Degraded reports the providers currently skipped after a failure.
func (h *LanguageHost) Degraded() []LanguageProviderStatus {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	providers := make([]*languageProvider, 0, len(h.providers))
	for _, provider := range h.providers {
		providers = append(providers, provider)
	}
	h.mu.Unlock()
	sort.Slice(providers, func(i, j int) bool { return providers[i].key() < providers[j].key() })
	h.degradedMu.Lock()
	defer h.degradedMu.Unlock()
	var out []LanguageProviderStatus
	for _, provider := range providers {
		status := LanguageProviderStatus{
			PluginID:   provider.ref.ID,
			ProviderID: provider.contract.ID,
			Fiber:      provider.fiber,
			Priority:   provider.contract.Priority,
			Languages:  append([]string(nil), provider.contract.Languages...),
		}
		if failure, ok := h.degraded[provider.key()]; ok {
			status.Degraded = true
			status.FailureCode = failure.code
			status.FailureText = failure.message
			out = append(out, status)
		}
	}
	return out
}

// RetryProvider clears the degraded mark so the next request tries the
// provider again.
func (h *LanguageHost) RetryProvider(fiber domainplugin.FiberID, providerID string) bool {
	if h == nil {
		return false
	}
	key := string(fiber) + "/" + providerID
	h.degradedMu.Lock()
	_, existed := h.degraded[key]
	delete(h.degraded, key)
	h.degradedMu.Unlock()
	return existed
}

func (h *LanguageHost) isDegraded(provider *languageProvider) bool {
	h.degradedMu.Lock()
	defer h.degradedMu.Unlock()
	_, degraded := h.degraded[provider.key()]
	return degraded
}

func (h *LanguageHost) markDegraded(provider *languageProvider, code, message string) {
	h.degradedMu.Lock()
	h.degraded[provider.key()] = languageProviderFailure{code: code, message: message}
	h.degradedMu.Unlock()
}

// Close stops every timer and forgets every document. It is idempotent.
func (h *LanguageHost) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	docs := make([]*languageDocumentState, 0, len(h.docs))
	for _, state := range h.docs {
		docs = append(docs, state)
	}
	h.docs = make(map[string]*languageDocumentState)
	h.providers = make(map[string]*languageProvider)
	h.mu.Unlock()
	for _, state := range docs {
		state.mu.Lock()
		state.closed = true
		for _, timer := range state.timers {
			if timer != nil {
				timer.Stop()
			}
		}
		state.timers = nil
		state.results = nil
		state.mu.Unlock()
	}
	h.cancelMu.Lock()
	h.canceled = make(map[uint64]domainplugin.FiberID)
	h.cancelMu.Unlock()
	h.degradedMu.Lock()
	h.degraded = make(map[string]languageProviderFailure)
	h.degradedMu.Unlock()
}

// record appends one audit row for a language-service access. It never records
// document content or result payloads.
func (h *LanguageHost) record(ref PluginRef, method, result string) {
	if h == nil || h.audit == nil {
		return
	}
	h.audit.Record(domainplugin.AuditEvent{
		Time:       h.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   ref.ID,
		Source:     ref.Source,
		Version:    ref.Version,
		Permission: domainplugin.PermissionEditorLanguage,
		Reason:     domainplugin.ServiceAuditReason("language", method, result),
	})
}

// Built-in helpers are implemented in language_merge.go.

func cloneLanguageProvider(provider domainplugin.LanguageProvider) domainplugin.LanguageProvider {
	provider.Languages = append([]string(nil), provider.Languages...)
	provider.Capabilities = append([]domainplugin.LanguageCapability(nil), provider.Capabilities...)
	provider.Contexts = append([]string(nil), provider.Contexts...)
	return provider
}

func cloneLanguageDocument(document LanguageDocument) LanguageDocument {
	if document.Selection != nil {
		copy := *document.Selection
		document.Selection = &copy
	}
	return document
}

func cloneLanguageResults(result LanguageDocumentResults) LanguageDocumentResults {
	result.Highlights = append([]LanguageHighlight(nil), result.Highlights...)
	result.Completions = append([]LanguageCompletionItem(nil), result.Completions...)
	result.Diagnostics = append([]LanguageDiagnostic(nil), result.Diagnostics...)
	result.Formatting = append([]LanguageTextEdit(nil), result.Formatting...)
	result.Navigation = append([]LanguageLocation(nil), result.Navigation...)
	result.CodeActions = append([]LanguageCodeAction(nil), result.CodeActions...)
	result.Failures = append([]LanguageProviderFailure(nil), result.Failures...)
	if result.Hover != nil {
		copy := *result.Hover
		result.Hover = &copy
	}
	if result.Signature != nil {
		copy := *result.Signature
		copy.Signatures = append([]LanguageSignature(nil), result.Signature.Signatures...)
		result.Signature = &copy
	}
	return result
}

// validateLanguageDocument enforces the document-shape contract: a bounded
// identifier, a bounded full snapshot, and a valid cursor/selection.
func validateLanguageDocument(document LanguageDocument) error {
	if document.ID == "" {
		return &LanguageRegistrationError{Problem: "document id is required"}
	}
	if len(document.ID) > MaxLanguageDocumentIDBytes {
		return &LimitError{Resource: "language document id bytes", Limit: MaxLanguageDocumentIDBytes}
	}
	if document.LanguageID == "" {
		return &LanguageRegistrationError{Problem: "document language id is required"}
	}
	if !validLanguageID(document.LanguageID) {
		return &LanguageRegistrationError{
			Problem: fmt.Sprintf("language id %q is not a valid identifier", document.LanguageID),
		}
	}
	if len(document.Content) > MaxLanguageDocumentBytes {
		return &LimitError{Resource: "language document bytes", Limit: MaxLanguageDocumentBytes}
	}
	if strings.ContainsRune(document.Content, 0) {
		return &LanguageRegistrationError{Problem: "document content contains a NUL byte"}
	}
	if err := validateLanguagePosition(document.Cursor); err != nil {
		return err
	}
	if document.Selection != nil {
		if err := validateLanguageRange(LanguageRange{
			Start: document.Selection.Start, End: document.Selection.End,
		}); err != nil {
			return err
		}
	}
	if len(document.Context.Kind) > 64 || len(document.Context.Value) > MaxLanguageContextValueBytes {
		return &LimitError{Resource: "language context bytes", Limit: MaxLanguageContextValueBytes}
	}
	return nil
}

func validLanguageID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if i == 0 {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				return false
			}
			continue
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validateLanguagePosition rejects absurd coordinates early so a malicious
// provider cannot make the host allocate from a line index.
func validateLanguagePosition(position LanguagePosition) error {
	if position.Line > 10_000_000 || position.Column > 1_000_000 {
		return &LanguageRegistrationError{
			Problem: fmt.Sprintf("position %d:%d is out of range", position.Line, position.Column),
		}
	}
	return nil
}

func validateLanguageRange(rng LanguageRange) error {
	if err := validateLanguagePosition(rng.Start); err != nil {
		return err
	}
	if err := validateLanguagePosition(rng.End); err != nil {
		return err
	}
	if rng.End.Line < rng.Start.Line ||
		(rng.End.Line == rng.Start.Line && rng.End.Column < rng.Start.Column) {
		return &LanguageRegistrationError{Problem: "range end is before its start"}
	}
	return nil
}
