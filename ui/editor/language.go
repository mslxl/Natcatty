package editor

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// LanguageService is the editor's language-service seam. The plugin manager's
// router implements it; tests supply a fake. The editor only forwards full
// snapshots and renders bounded data; it never parses the language.
type LanguageService interface {
	Open(document appplugin.LanguageDocument) (uint64, error)
	Update(document appplugin.LanguageDocument) (uint64, error)
	SetPosition(documentID string, cursor appplugin.LanguagePosition,
		selection *appplugin.LanguageSelection) bool
	CloseDocument(documentID string)
	Results(documentID string,
		capability domainplugin.LanguageCapability) (appplugin.LanguageDocumentResults, bool)
	Request(ctx context.Context, documentID string,
		capability domainplugin.LanguageCapability) (appplugin.LanguageDocumentResults, error)
	Timings() appplugin.LanguageTimings
}

var _ LanguageService = (*appplugin.LanguageHost)(nil)

// documentLanguage is the language binding of one open document. It lives
// beside the buffer so a document without a known language costs nothing.
type documentLanguage struct {
	id         string
	documentID string
	version    uint64
	cursor     appplugin.LanguagePosition
	selection  *appplugin.LanguageSelection
	context    appplugin.LanguageContext
	sent       string
	results    map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults
}

// WithLanguage attaches the language-service router to the editor state.
func WithLanguage(service LanguageService) Option {
	return func(s *State) { s.language = service }
}

// SetLanguage attaches or replaces the language-service router after
// construction. The app builds the plugin manager after the editor state, so
// it wires the router here; a document already open is registered lazily on
// the next SyncLanguage.
func (s *State) SetLanguage(service LanguageService) { s.language = service }

// SyncLanguage forwards the current buffer to the language router and refreshes
// the rendered results. It is idempotent and cheap on the UI thread: content is
// sent only when it changed, and the host debounces completion and diagnostics.
func (s *State) SyncLanguage() {
	doc := s.doc
	if doc == nil || s.language == nil {
		return
	}
	if doc.language == nil {
		s.openLanguageDocument(doc)
		return
	}
	if doc.language.id == "" {
		return
	}
	if doc.content != doc.language.sent {
		doc.language.sent = doc.content
		doc.language.cursor = endPosition(doc.content)
		version, err := s.language.Update(s.languageDocument(doc))
		if err != nil {
			// The snapshot was rejected (for example the document cap). Drop
			// results that no longer describe the buffer; the next edit
			// retries with a smaller snapshot.
			doc.language.results = make(map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults)
			return
		}
		doc.language.version = version
		doc.language.results = make(map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults)
		s.scheduleLanguageFlush(doc)
	}
	s.refreshLanguageResults(doc)
}

// SetLanguageCursor moves the cursor and selection the next request carries.
// The document version does not change, matching the host contract.
func (s *State) SetLanguageCursor(cursor appplugin.LanguagePosition,
	selection *appplugin.LanguageSelection) {
	doc := s.doc
	if doc == nil || s.language == nil || doc.language == nil || doc.language.id == "" {
		return
	}
	doc.language.cursor = cursor
	doc.language.selection = selection
	if s.language.SetPosition(doc.language.documentID, cursor, selection) {
		s.scheduleLanguageFlush(doc)
	}
}

// RequestLanguage routes one non-debounced capability synchronously. The view
// uses it for explicit user actions; completion and diagnostics arrive through
// the router's own debounce.
func (s *State) RequestLanguage(ctx context.Context,
	capability domainplugin.LanguageCapability) (appplugin.LanguageDocumentResults, bool) {
	doc := s.doc
	if doc == nil || s.language == nil || doc.language == nil || doc.language.id == "" {
		return appplugin.LanguageDocumentResults{}, false
	}
	result, err := s.language.Request(ctx, doc.language.documentID, capability)
	if err != nil {
		return appplugin.LanguageDocumentResults{}, false
	}
	if result.Version != doc.language.version {
		return appplugin.LanguageDocumentResults{}, false
	}
	doc.language.results[capability] = result
	return result, true
}

func (s *State) openLanguageDocument(doc *document) {
	language := &documentLanguage{id: LanguageIDForName(doc.name)}
	doc.language = language
	if language.id == "" {
		return
	}
	language.documentID = "editor:" + string(doc.id)
	language.context = appplugin.LanguageContext{Kind: "editor"}
	language.cursor = endPosition(doc.content)
	language.sent = doc.content
	language.results = make(map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults)
	version, err := s.language.Open(s.languageDocument(doc))
	if err != nil {
		language.id = ""
		return
	}
	language.version = version
	s.scheduleLanguageFlush(doc)
}

// closeLanguageDocument unregisters the document from the router so its timers
// and in-flight requests stop with the editor tab.
func (s *State) closeLanguageDocument(doc *document) {
	if s.language == nil || doc.language == nil {
		return
	}
	if doc.language.id == "" {
		return
	}
	s.language.CloseDocument(doc.language.documentID)
}

func (s *State) languageDocument(doc *document) appplugin.LanguageDocument {
	return appplugin.LanguageDocument{
		ID:         doc.language.documentID,
		LanguageID: doc.language.id,
		Content:    doc.content,
		Cursor:     doc.language.cursor,
		Selection:  doc.language.selection,
		Context:    doc.language.context,
	}
}

func (s *State) refreshLanguageResults(doc *document) {
	if doc.language == nil || doc.language.id == "" {
		return
	}
	if doc.language.results == nil {
		doc.language.results = make(map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults)
	}
	for _, capability := range []domainplugin.LanguageCapability{
		domainplugin.LanguageCompletion, domainplugin.LanguageDiagnostics,
	} {
		result, ok := s.language.Results(doc.language.documentID, capability)
		if !ok || result.Version != doc.language.version {
			// The router no longer publishes a result for this version: the
			// provider was unloaded or the result was cleared. Drop the cache
			// so the editor stops rendering output it can no longer justify.
			delete(doc.language.results, capability)
			continue
		}
		doc.language.results[capability] = result
	}
}

// languageScheduler arms the debounced redraw that makes host-published
// results visible. The default uses real time; tests inject a deterministic
// clock so a frame assertion never depends on a wall-clock sleep.
type languageScheduler interface {
	AfterFunc(delay time.Duration, callback func()) languageTimer
}

// languageTimer is one scheduled flush callback.
type languageTimer interface {
	Stop() bool
}

type realLanguageScheduler struct{}

func (realLanguageScheduler) AfterFunc(delay time.Duration,
	callback func()) languageTimer {
	return time.AfterFunc(delay, callback)
}

// withLanguageScheduler injects the flush scheduler for tests.
func withLanguageScheduler(scheduler languageScheduler) Option {
	return func(s *State) { s.languageSchedule = scheduler }
}

// scheduleLanguageFlush wakes the UI once the host's debounce windows have
// elapsed so newly published results become visible without another keystroke.
func (s *State) scheduleLanguageFlush(doc *document) {
	if s.redraw == nil || s.language == nil || doc.language == nil {
		return
	}
	if s.languageSchedule == nil {
		s.languageSchedule = realLanguageScheduler{}
	}
	timings := s.language.Timings()
	delay := timings.DiagnosticsDebounce
	if timings.CompletionDebounce > delay {
		delay = timings.CompletionDebounce
	}
	if delay <= 0 {
		delay = appplugin.DefaultLanguageTimings().DiagnosticsDebounce
	}
	documentID := doc.language.documentID
	s.languageSchedule.AfterFunc(delay+20*time.Millisecond, func() {
		s.post(func() {
			// The wake only makes the next frame run SyncLanguage; a closed
			// or replaced document is simply ignored.
			if s.doc != nil && s.doc.language != nil &&
				s.doc.language.documentID == documentID {
				s.refreshLanguageResults(s.doc)
			}
		})
	})
}

// Completions returns the latest bounded completion list for the open
// document.
func (s *State) Completions() []appplugin.LanguageCompletionItem {
	doc := s.doc
	if doc == nil || doc.language == nil {
		return nil
	}
	return doc.language.results[domainplugin.LanguageCompletion].Completions
}

// Diagnostics returns the latest bounded diagnostic list for the open
// document.
func (s *State) Diagnostics() []appplugin.LanguageDiagnostic {
	doc := s.doc
	if doc == nil || doc.language == nil {
		return nil
	}
	return doc.language.results[domainplugin.LanguageDiagnostics].Diagnostics
}

// LanguageID returns the language identifier bound to the open document.
func (s *State) LanguageID() string {
	if s.doc == nil || s.doc.language == nil {
		return ""
	}
	return s.doc.language.id
}

// LanguageVersion returns the monotonic snapshot version the language router
// currently holds for the open document.
func (s *State) LanguageVersion() uint64 {
	if s.doc == nil || s.doc.language == nil {
		return 0
	}
	return s.doc.language.version
}

// LanguageDocumentID returns the opaque document id used on the wire.
func (s *State) LanguageDocumentID() string {
	if s.doc == nil || s.doc.language == nil {
		return ""
	}
	return s.doc.language.documentID
}

// LanguageIDForName maps a file name to a built-in language identifier. The
// mapping is add-only with the host's built-in language set; an unknown
// extension has no language services.
func LanguageIDForName(name string) string {
	extension := strings.ToLower(filepath.Ext(name))
	switch extension {
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".css":
		return "css"
	case ".go":
		return "go"
	case ".html", ".htm":
		return "html"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".sql":
		return "sql"
	case ".toml":
		return "toml"
	case ".ts", ".tsx":
		return "typescript"
	case ".xml":
		return "xml"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return ""
	}
}

// endPosition returns the zero-based line and column just past the last byte
// of content, which is the best available caret position in the binding.
func endPosition(content string) appplugin.LanguagePosition {
	line, column := uint32(0), uint32(0)
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			line++
			column = 0
			continue
		}
		column++
	}
	return appplugin.LanguagePosition{Line: line, Column: column}
}
