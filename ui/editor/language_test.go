package editor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type fakeLanguage struct {
	mu        sync.Mutex
	opened    []appplugin.LanguageDocument
	updated   []appplugin.LanguageDocument
	positions []appplugin.LanguagePosition
	closed    []string
	version   uint64
	results   map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults
	requested map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults
	timings   appplugin.LanguageTimings
}

func newFakeLanguage() *fakeLanguage {
	return &fakeLanguage{
		results:   make(map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults),
		requested: make(map[domainplugin.LanguageCapability]appplugin.LanguageDocumentResults),
		timings:   appplugin.LanguageTimings{CompletionDebounce: 5 * time.Millisecond, DiagnosticsDebounce: 5 * time.Millisecond},
	}
}

func (f *fakeLanguage) Open(document appplugin.LanguageDocument) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, document)
	f.version = 1
	return f.version, nil
}

func (f *fakeLanguage) Update(document appplugin.LanguageDocument) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, document)
	f.version++
	// Publish results for the new version immediately, as a provider whose
	// debounce has already elapsed would.
	for capability, result := range f.results {
		result.Version = f.version
		result.DocumentID = document.ID
		f.results[capability] = result
	}
	return f.version, nil
}

func (f *fakeLanguage) SetPosition(documentID string, cursor appplugin.LanguagePosition,
	selection *appplugin.LanguageSelection) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.positions = append(f.positions, cursor)
	return true
}

func (f *fakeLanguage) CloseDocument(documentID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, documentID)
}

func (f *fakeLanguage) Results(documentID string,
	capability domainplugin.LanguageCapability) (appplugin.LanguageDocumentResults, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[capability]
	if !ok || result.DocumentID != documentID {
		return appplugin.LanguageDocumentResults{}, false
	}
	return result, true
}

func (f *fakeLanguage) Request(_ context.Context, documentID string,
	capability domainplugin.LanguageCapability) (appplugin.LanguageDocumentResults, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.requested[capability]
	if !ok {
		return appplugin.LanguageDocumentResults{}, errors.New("no result")
	}
	return result, nil
}

func (f *fakeLanguage) Timings() appplugin.LanguageTimings { return f.timings }

func (f *fakeLanguage) setResults(capability domainplugin.LanguageCapability,
	result appplugin.LanguageDocumentResults) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[capability] = result
}

func (f *fakeLanguage) clearResults(capability domainplugin.LanguageCapability) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.results, capability)
}

func (f *fakeLanguage) openCalls() []appplugin.LanguageDocument {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appplugin.LanguageDocument(nil), f.opened...)
}

func (f *fakeLanguage) updateCalls() []appplugin.LanguageDocument {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appplugin.LanguageDocument(nil), f.updated...)
}

func (f *fakeLanguage) closedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

func (f *fakeLanguage) cursorCalls() []appplugin.LanguagePosition {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appplugin.LanguagePosition(nil), f.positions...)
}

// fakeEditorScheduler is a deterministic clock for the language flush timer.
// Timers fire only when Advance reaches their due time, so the render
// assertions never depend on a wall-clock sleep.
type fakeEditorScheduler struct {
	mu     sync.Mutex
	now    time.Duration
	nextID int
	timers map[int]*fakeEditorTimer
}

type fakeEditorTimer struct {
	scheduler *fakeEditorScheduler
	id        int
	due       time.Duration
	callback  func()
	fired     bool
}

func newFakeEditorScheduler() *fakeEditorScheduler {
	return &fakeEditorScheduler{timers: make(map[int]*fakeEditorTimer)}
}

func (s *fakeEditorScheduler) AfterFunc(delay time.Duration,
	callback func()) languageTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	timer := &fakeEditorTimer{
		scheduler: s, id: id, due: s.now + delay, callback: callback,
	}
	s.timers[id] = timer
	return timer
}

func (t *fakeEditorTimer) Stop() bool {
	s := t.scheduler
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.fired {
		return false
	}
	if _, pending := s.timers[t.id]; !pending {
		return false
	}
	delete(s.timers, t.id)
	return true
}

func (s *fakeEditorScheduler) Advance(delta time.Duration) {
	target := s.now + delta
	for {
		s.mu.Lock()
		var next *fakeEditorTimer
		for _, timer := range s.timers {
			if timer.due > target {
				continue
			}
			if next == nil || timer.due < next.due ||
				(timer.due == next.due && timer.id < next.id) {
				next = timer
			}
		}
		if next == nil {
			s.now = target
			s.mu.Unlock()
			return
		}
		delete(s.timers, next.id)
		next.fired = true
		s.now = next.due
		callback := next.callback
		s.mu.Unlock()
		callback()
	}
}

func TestLanguageFlushRendersOnlyAfterTheDebounce(t *testing.T) {
	service := &fakeService{}
	clock := newFakeEditorScheduler()
	redraws := 0
	language := newFakeLanguage()
	language.setResults(domainplugin.LanguageCompletion, appplugin.LanguageDocumentResults{
		DocumentID: "editor:s1",
		Version:    1,
		Completions: []appplugin.LanguageCompletionItem{
			{Label: "sample_keyword", InsertText: "sample_keyword"},
		},
	})
	language.setResults(domainplugin.LanguageDiagnostics, appplugin.LanguageDocumentResults{
		DocumentID: "editor:s1",
		Version:    1,
		Diagnostics: []appplugin.LanguageDiagnostic{{
			Range:    appplugin.LanguageRange{},
			Severity: appplugin.LanguageSeverityWarning,
			Message:  "TODO marker found by acme.language-provider",
		}},
	})
	state := NewState(service,
		WithLanguage(language),
		WithRedrawRequest(func() { redraws++ }),
		withLanguageScheduler(clock),
	)
	openFixture(t, state, service, "demo.sql")
	redraws = 0
	state.SyncLanguage()

	// The fake router's windows are 5ms each, so the flush is armed at 25ms.
	clock.Advance(24 * time.Millisecond)
	if redraws != 0 {
		t.Fatalf("the editor was invalidated %d times before the debounce", redraws)
	}
	if completions := state.Completions(); len(completions) != 0 {
		t.Fatalf("completions became visible before the debounce: %+v", completions)
	}
	if diagnostics := state.Diagnostics(); len(diagnostics) != 0 {
		t.Fatalf("diagnostics became visible before the debounce: %+v", diagnostics)
	}
	clock.Advance(time.Millisecond)
	if redraws != 1 {
		t.Fatalf("redraw requests after the debounce = %d, want 1", redraws)
	}
	if completions := state.Completions(); len(completions) != 0 {
		t.Fatalf("results were applied outside Flush: %+v", completions)
	}
	state.Flush()
	if completions := state.Completions(); len(completions) != 1 ||
		completions[0].Label != "sample_keyword" {
		t.Fatalf("completions after the flush = %+v", completions)
	}
	if diagnostics := state.Diagnostics(); len(diagnostics) != 1 ||
		diagnostics[0].Message != "TODO marker found by acme.language-provider" {
		t.Fatalf("diagnostics after the flush = %+v", diagnostics)
	}

	// Rendering with the published sample data shows it in the editor view.
	tester := ui.NewTester(View(state), 800, 600)
	tester.SetLocale("en")
	tester.Frame()
	if _, ok := tester.Find("sample_keyword"); !ok {
		t.Fatal("the completion label is not rendered in the editor")
	}
	if _, ok := tester.Find("TODO marker found by acme.language-provider"); !ok {
		t.Fatal("the diagnostic message is not rendered in the editor")
	}
}

func TestOpenBindsTheDocumentAndSyncsFullSnapshots(t *testing.T) {
	service := &fakeService{}
	language := newFakeLanguage()
	language.setResults(domainplugin.LanguageCompletion, appplugin.LanguageDocumentResults{
		Completions: []appplugin.LanguageCompletionItem{
			{Label: "sample_keyword", InsertText: "sample_keyword"},
		},
	})
	language.setResults(domainplugin.LanguageDiagnostics, appplugin.LanguageDocumentResults{
		Diagnostics: []appplugin.LanguageDiagnostic{{
			Range:    appplugin.LanguageRange{},
			Severity: appplugin.LanguageSeverityWarning,
			Message:  "TODO marker found by acme.language-provider",
		}},
	})
	state := NewState(service, WithLanguage(language))
	openFixture(t, state, service, "demo.sql")

	state.SyncLanguage()
	opened := language.openCalls()
	if len(opened) != 1 {
		t.Fatalf("open calls = %d", len(opened))
	}
	if opened[0].ID != "editor:s1" || opened[0].LanguageID != "sql" ||
		opened[0].Content != "alpha" ||
		opened[0].Cursor != (appplugin.LanguagePosition{Line: 0, Column: 5}) ||
		opened[0].Context.Kind != "editor" {
		t.Fatalf("open snapshot = %+v", opened[0])
	}
	if state.LanguageID() != "sql" || state.LanguageDocumentID() != "editor:s1" ||
		state.LanguageVersion() != 1 {
		t.Fatalf("language binding = %q %q %d", state.LanguageID(),
			state.LanguageDocumentID(), state.LanguageVersion())
	}

	*state.Content() = "select 1 -- TODO\nselect 2"
	state.SyncLanguage()
	updated := language.updateCalls()
	if len(updated) != 1 {
		t.Fatalf("update calls = %d", len(updated))
	}
	if updated[0].Content != "select 1 -- TODO\nselect 2" ||
		updated[0].Cursor != (appplugin.LanguagePosition{Line: 1, Column: 8}) ||
		updated[0].Context.Kind != "editor" {
		t.Fatalf("update snapshot = %+v", updated[0])
	}
	if state.LanguageVersion() != 2 {
		t.Fatalf("version = %d, want 2", state.LanguageVersion())
	}
	// The unchanged buffer is not sent twice.
	state.SyncLanguage()
	if got := len(language.updateCalls()); got != 1 {
		t.Fatalf("idempotent SyncLanguage sent %d updates", got)
	}
	if completions := state.Completions(); len(completions) != 1 ||
		completions[0].Label != "sample_keyword" {
		t.Fatalf("completions = %+v", completions)
	}
	if diagnostics := state.Diagnostics(); len(diagnostics) != 1 ||
		diagnostics[0].Message != "TODO marker found by acme.language-provider" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	// The view renders the bounded provider text without host chrome.
	tester := ui.NewTester(View(state), 800, 600)
	tester.SetLocale("en")
	tester.Frame()
	if _, ok := tester.Find("sample_keyword"); !ok {
		t.Fatal("the completion label is not rendered in the editor")
	}
	if _, ok := tester.Find("TODO marker found by acme.language-provider"); !ok {
		t.Fatal("the diagnostic message is not rendered in the editor")
	}

	// Unloading a provider clears its router result. The editor must drop the
	// cached completion output instead of rendering data it can no longer
	// justify.
	language.clearResults(domainplugin.LanguageCompletion)
	state.SyncLanguage()
	if completions := state.Completions(); len(completions) != 0 {
		t.Fatalf("completions after unload = %+v", completions)
	}
	if diagnostics := state.Diagnostics(); len(diagnostics) != 1 {
		t.Fatalf("diagnostics after unload = %+v", diagnostics)
	}

	state.Close()
	state.Discard()
	if closed := language.closedIDs(); len(closed) != 1 || closed[0] != "editor:s1" {
		t.Fatalf("closed documents = %v", closed)
	}
}

func TestUnknownExtensionHasNoLanguageBinding(t *testing.T) {
	service := &fakeService{}
	language := newFakeLanguage()
	state := NewState(service, WithLanguage(language))
	openFixture(t, state, service, "notes.txt")
	state.SyncLanguage()
	if state.LanguageID() != "" || state.LanguageDocumentID() != "" {
		t.Fatalf("binding = %q %q", state.LanguageID(), state.LanguageDocumentID())
	}
	if len(language.openCalls()) != 0 {
		t.Fatal("the router was asked to open a language-less document")
	}
}

func TestSetLanguageAfterConstructionCursorAndExplicitRequest(t *testing.T) {
	service := &fakeService{}
	state := NewState(service)
	openFixture(t, state, service, "demo.go")
	state.SyncLanguage()
	if state.LanguageID() != "" {
		t.Fatal("a language router appeared before SetLanguage")
	}

	language := newFakeLanguage()
	language.requested[domainplugin.LanguageHover] = appplugin.LanguageDocumentResults{
		Version: 1,
		Hover:   &appplugin.LanguageHover{Contents: "hover"},
	}
	state.SetLanguage(language)
	state.SyncLanguage()
	if len(language.openCalls()) != 1 || state.LanguageID() != "go" {
		t.Fatalf("open after SetLanguage = %v, language %q",
			language.openCalls(), state.LanguageID())
	}
	selection := &appplugin.LanguageSelection{
		Start: appplugin.LanguagePosition{Line: 0, Column: 0},
		End:   appplugin.LanguagePosition{Line: 0, Column: 1},
	}
	state.SetLanguageCursor(appplugin.LanguagePosition{Line: 2, Column: 3}, selection)
	cursors := language.cursorCalls()
	if len(cursors) != 1 || cursors[0] != (appplugin.LanguagePosition{Line: 2, Column: 3}) {
		t.Fatalf("cursor calls = %+v", cursors)
	}
	if state.LanguageVersion() != 1 {
		t.Fatalf("moving the cursor changed the version to %d", state.LanguageVersion())
	}
	result, ok := state.RequestLanguage(context.Background(), domainplugin.LanguageHover)
	if !ok || result.Hover == nil || result.Hover.Contents != "hover" {
		t.Fatalf("RequestLanguage = %+v, %v", result, ok)
	}
	language.requested[domainplugin.LanguageHover] = appplugin.LanguageDocumentResults{
		Version: 99,
		Hover:   &appplugin.LanguageHover{Contents: "stale"},
	}
	if _, ok := state.RequestLanguage(context.Background(), domainplugin.LanguageHover); ok {
		t.Fatal("a stale explicit result was accepted")
	}
}

func TestLanguageIDForName(t *testing.T) {
	tests := map[string]string{
		"main.go": "go", "app.tsx": "typescript", "query.SQL": "sql",
		"README.md": "markdown", "config.yaml": "yaml", "run.sh": "shell",
		"notes.txt": "", "Makefile": "", "plugin.toml": "toml",
	}
	for name, want := range tests {
		if got := LanguageIDForName(name); got != want {
			t.Errorf("LanguageIDForName(%q) = %q, want %q", name, got, want)
		}
	}
}
