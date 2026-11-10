package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// languageCall mirrors the wire request a provider receives. The test decodes
// the payload the host actually marshaled instead of trusting internal types.
type languageCall struct {
	RequestID  uint64                          `json:"requestId"`
	Kind       domainplugin.LanguageCapability `json:"-"`
	DocumentID string                          `json:"documentId"`
	Version    uint64                          `json:"version"`
	LanguageID string                          `json:"languageId"`
	Content    string                          `json:"content"`
	Cursor     appplugin.LanguagePosition      `json:"cursor"`
	Selection  *appplugin.LanguageSelection    `json:"selection,omitempty"`
	Context    appplugin.LanguageContext       `json:"context"`
	Capability domainplugin.LanguageCapability `json:"capability"`
}

type languageFake struct {
	mu       sync.Mutex
	calls    []languageCall
	canceled []uint64

	respond func(languageCall) ([]byte, error)
	started chan languageCall
	release chan struct{}
	block   bool
}

func (f *languageFake) InvokeLanguage(ctx context.Context,
	request appplugin.LanguageRequest) ([]byte, error) {
	var call languageCall
	if err := json.Unmarshal(request.Payload, &call); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}
	call.RequestID = request.ID
	call.Kind = request.Kind
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- call
	}
	if f.block {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.respond == nil {
		return nil, errors.New("test provider has no response")
	}
	return f.respond(call)
}

func (f *languageFake) CancelLanguage(context.Context, uint64) error {
	return nil
}

func (f *languageFake) recordCancel(requestID uint64) {
	f.mu.Lock()
	f.canceled = append(f.canceled, requestID)
	f.mu.Unlock()
}

func (f *languageFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *languageFake) lastCall(t *testing.T) languageCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("the provider was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *languageFake) cancelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.canceled)
}

func (f *languageFake) callsOf(kind domainplugin.LanguageCapability) []languageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []languageCall
	for _, call := range f.calls {
		if call.Kind == kind {
			out = append(out, call)
		}
	}
	return out
}

// fakeLanguageScheduler is a deterministic clock for debounce tests. A timer
// fires only when Advance moves the clock to its due time; Stop removes it
// before it can run. No test assertion depends on wall-clock sleeps.
type fakeLanguageScheduler struct {
	mu     sync.Mutex
	now    time.Duration
	nextID int
	timers map[int]*fakeLanguageTimer
	runs   int
}

type fakeLanguageTimer struct {
	scheduler *fakeLanguageScheduler
	id        int
	due       time.Duration
	callback  func()
	fired     bool
}

func newFakeLanguageScheduler() *fakeLanguageScheduler {
	return &fakeLanguageScheduler{timers: make(map[int]*fakeLanguageTimer)}
}

func (s *fakeLanguageScheduler) AfterFunc(delay time.Duration,
	callback func()) appplugin.LanguageTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	timer := &fakeLanguageTimer{
		scheduler: s, id: id, due: s.now + delay, callback: callback,
	}
	s.timers[id] = timer
	return timer
}

func (t *fakeLanguageTimer) Stop() bool {
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

// Advance moves the fake clock by delta and synchronously runs every callback
// that becomes due, ordered by due time and then creation order.
func (s *fakeLanguageScheduler) Advance(delta time.Duration) {
	target := s.now + delta
	for {
		s.mu.Lock()
		var next *fakeLanguageTimer
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
		s.runs++
		callback := next.callback
		s.mu.Unlock()
		callback()
	}
}

// runCount reports how many timer callbacks have executed, including ones
// that exited on an epoch mismatch. A canceled timer never runs.
func (s *fakeLanguageScheduler) runCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs
}

func okLanguageResponse(version uint64, data string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"code":"ok","version":%d,"data":%s}`, version, data)), nil
}

// jsonString encodes s as a JSON string literal so test payloads stay valid.
func jsonString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

type languageFixture struct {
	host      *appplugin.LanguageHost
	authority *appplugin.Authority
	audit     *recordingAudit
	ref       appplugin.PluginRef
	fiber     domainplugin.FiberID
	now       time.Time
}

func newLanguageFixture(t *testing.T, builtin appplugin.LanguageBuiltin,
	timings appplugin.LanguageTimings) *languageFixture {
	t.Helper()
	return newLanguageFixtureWithScheduler(t, builtin, timings, nil)
}

func newLanguageFixtureWithScheduler(t *testing.T, builtin appplugin.LanguageBuiltin,
	timings appplugin.LanguageTimings,
	scheduler appplugin.LanguageScheduler) *languageFixture {
	t.Helper()
	authority := appplugin.NewAuthority()
	audit := &recordingAudit{}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	host := appplugin.NewLanguageHost(appplugin.LanguageHostOptions{
		Authority: authority,
		Audit:     audit,
		Now:       func() time.Time { return now },
		Timings:   timings,
		Builtin:   builtin,
		Scheduler: scheduler,
	})
	t.Cleanup(host.Close)
	return &languageFixture{
		host: host, authority: authority, audit: audit,
		ref: appplugin.PluginRef{
			ID: "acme.language", Name: "Acme Language", Version: "1.0.0",
			Source: domainplugin.SourceLocalFile,
		},
		fiber: domainplugin.NewFiberID("installed", "acme.language", 1),
		now:   now,
	}
}

func (f *languageFixture) grant(t *testing.T) {
	t.Helper()
	f.grantRef(t, f.ref)
}

func (f *languageFixture) grantRef(t *testing.T, ref appplugin.PluginRef) {
	t.Helper()
	f.authority.Grant(appplugin.SubjectForRef(ref), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionEditorLanguage,
		Scope:      domainplugin.Scope{},
		GrantedAt:  f.now,
	})
}

func (f *languageFixture) register(t *testing.T, provider domainplugin.LanguageProvider,
	invoker appplugin.LanguageInvoker) appplugin.Disposer {
	t.Helper()
	disposer, err := f.host.Register(f.ref, f.fiber, provider, invoker)
	if err != nil {
		t.Fatalf("Register(%q): %v", provider.ID, err)
	}
	return disposer
}

func languageDocument(id, languageID, content string) appplugin.LanguageDocument {
	return appplugin.LanguageDocument{
		ID:         id,
		LanguageID: languageID,
		Content:    content,
		Cursor:     appplugin.LanguagePosition{Line: 0, Column: 0},
		Context:    appplugin.LanguageContext{Kind: "editor"},
	}
}

func waitLanguage(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func serviceCode(t *testing.T, err error) string {
	t.Helper()
	var service *appplugin.ServiceError
	if !errors.As(err, &service) {
		t.Fatalf("error %v is not a *ServiceError", err)
	}
	return service.Code
}

func TestLanguageHostRegistrationRequiresGrant(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	invoker := &languageFake{}
	provider := domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}

	_, err := fixture.host.Register(fixture.ref, fixture.fiber, provider, invoker)
	if err == nil {
		t.Fatal("Register accepted a provider without editor.language")
	}
	if code := serviceCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("code = %q, want denied", code)
	}
	if !fixture.audit.hasReason("language.register:denied") {
		t.Fatal("the denied registration was not audited")
	}

	fixture.grant(t)
	if _, err := fixture.host.Register(fixture.ref, fixture.fiber, provider, nil); err == nil {
		t.Fatal("Register accepted a nil invoker")
	}
	disposer := fixture.register(t, provider, invoker)
	if disposer == nil {
		t.Fatal("Register returned no disposer")
	}

	fixture.grant(t)
	if _, err := fixture.host.Register(fixture.ref, fixture.fiber, provider, invoker); err == nil {
		t.Fatal("Register accepted a duplicate provider id")
	}

	foreign := domainplugin.LanguageProvider{
		ID: "foreign", Languages: []string{"other.plugin.rune"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHover},
	}
	if _, err := fixture.host.Register(fixture.ref, fixture.fiber, foreign, invoker); err == nil {
		t.Fatal("Register accepted a foreign plugin-owned language")
	}

	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := disposer(context.Background()); err != nil {
		t.Fatalf("disposer: %v", err)
	}
	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if result.Providers != 0 {
		t.Fatalf("providers after disposal = %d, want 0", result.Providers)
	}
}

func TestLanguageHostPluginOwnedLanguageIDsAreExclusive(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"hover":null}`)
	}}
	plain := appplugin.PluginRef{
		ID: "acme", Name: "Acme", Version: "1.0.0", Source: domainplugin.SourceLocalFile,
	}
	nested := appplugin.PluginRef{
		ID: "acme.hello", Name: "Acme Hello", Version: "1.0.0",
		Source: domainplugin.SourceLocalFile,
	}
	plainFiber := domainplugin.NewFiberID("installed", "acme", 1)
	nestedFiber := domainplugin.NewFiberID("installed", "acme.hello", 1)
	fixture.grantRef(t, plain)
	fixture.grantRef(t, nested)

	claim := func(ref appplugin.PluginRef, fiber domainplugin.FiberID,
		providerID string, languages ...string) (appplugin.Disposer, error) {
		return fixture.host.Register(ref, fiber, domainplugin.LanguageProvider{
			ID: providerID, Languages: languages,
			Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHover},
		}, invoker)
	}

	// The nested plugin identifier registers first and owns the language. The
	// "acme" prefix check alone would also accept "acme.hello.rune" for plugin
	// "acme", because dots are legal in the suffix.
	disposer, err := claim(nested, nestedFiber, "rune", "acme.hello.rune")
	if err != nil {
		t.Fatalf("the first claim failed: %v", err)
	}
	_, err = claim(plain, plainFiber, "rune", "acme.hello.rune")
	var registration *appplugin.LanguageRegistrationError
	if !errors.As(err, &registration) {
		t.Fatalf("the nested ownership collision was accepted: %v", err)
	}
	if !strings.Contains(registration.Problem, `already owned by plugin "acme.hello"`) {
		t.Fatalf("registration problem = %q", registration.Problem)
	}

	// Disposing the owner releases the identifier for the next registration.
	if err := disposer(context.Background()); err != nil {
		t.Fatalf("disposer: %v", err)
	}
	if _, err := claim(plain, plainFiber, "rune", "acme.hello.rune"); err != nil {
		t.Fatalf("claim after disposal: %v", err)
	}
	// A second provider from the same plugin may serve the same language.
	if _, err := claim(plain, plainFiber, "rune-two", "acme.hello.rune"); err != nil {
		t.Fatalf("same-plugin claim: %v", err)
	}

	// Built-in identifiers remain add-only for every plugin.
	if _, err := claim(plain, plainFiber, "sql-plain", "sql"); err != nil {
		t.Fatalf("built-in claim by acme: %v", err)
	}
	if _, err := claim(nested, nestedFiber, "sql-nested", "sql"); err != nil {
		t.Fatalf("built-in claim by acme.hello: %v", err)
	}

	// The reverse registration order gives the first plugin the identifier.
	reverse := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	reverse.grantRef(t, plain)
	reverse.grantRef(t, nested)
	claimReverse := func(ref appplugin.PluginRef, fiber domainplugin.FiberID) error {
		_, err := reverse.host.Register(ref, fiber, domainplugin.LanguageProvider{
			ID: "rune", Languages: []string{"acme.hello.rune"},
			Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHover},
		}, invoker)
		return err
	}
	if err := claimReverse(plain, plainFiber); err != nil {
		t.Fatalf("the first reverse claim failed: %v", err)
	}
	if err := claimReverse(nested, nestedFiber); err == nil {
		t.Fatal("the reverse nested ownership collision was accepted")
	}
}

func TestLanguageHostDocumentSnapshotsVersionsAndCap(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, invoker)

	selection := &appplugin.LanguageSelection{
		Start: appplugin.LanguagePosition{Line: 0, Column: 0},
		End:   appplugin.LanguagePosition{Line: 0, Column: 5},
	}
	document := appplugin.LanguageDocument{
		ID:         "doc-1",
		LanguageID: "sql",
		Content:    "select 1",
		Cursor:     appplugin.LanguagePosition{Line: 0, Column: 5},
		Selection:  selection,
		Context:    appplugin.LanguageContext{Kind: "editor", Value: "schema:main"},
	}
	version, err := fixture.host.Open(document)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if version != 1 {
		t.Fatalf("first version = %d, want 1", version)
	}

	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	call := invoker.lastCall(t)
	if call.DocumentID != "doc-1" || call.Version != 1 || call.LanguageID != "sql" ||
		call.Content != "select 1" || call.Cursor != document.Cursor ||
		call.Selection == nil || *call.Selection != *selection ||
		call.Context != document.Context ||
		call.Capability != domainplugin.LanguageCompletion || call.RequestID == 0 {
		t.Fatalf("provider request = %+v", call)
	}
	if result.Version != 1 || result.DocumentID != "doc-1" ||
		result.Capability != domainplugin.LanguageCompletion {
		t.Fatalf("result = %+v", result)
	}
	if !result.GeneratedAt.Equal(fixture.now) {
		t.Fatalf("generated at = %v, want %v", result.GeneratedAt, fixture.now)
	}

	document.Content = "select 2"
	document.Cursor = appplugin.LanguagePosition{Line: 0, Column: 8}
	version, err = fixture.host.Update(document)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if version != 2 {
		t.Fatalf("updated version = %d, want 2", version)
	}
	if _, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion); err != nil {
		t.Fatalf("Request after Update: %v", err)
	}
	if call := invoker.lastCall(t); call.Version != 2 || call.Content != "select 2" {
		t.Fatalf("request after Update = %+v", call)
	}
	if got, ok := fixture.host.Version("doc-1"); !ok || got != 2 {
		t.Fatalf("Version = %d, %v", got, ok)
	}

	if _, err := fixture.host.Open(document); err == nil {
		t.Fatal("Open accepted an already open document")
	} else {
		var exists *appplugin.LanguageDocumentExistsError
		if !errors.As(err, &exists) {
			t.Fatalf("error = %v, want LanguageDocumentExistsError", err)
		}
	}
	if _, err := fixture.host.Request(context.Background(), "missing",
		domainplugin.LanguageCompletion); err == nil {
		t.Fatal("Request accepted an unknown document")
	} else {
		var missing *appplugin.LanguageDocumentNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("error = %v, want LanguageDocumentNotFoundError", err)
		}
	}
	if _, err := fixture.host.Request(context.Background(), "doc-1",
		"teleport"); err == nil {
		t.Fatal("Request accepted an unknown capability")
	}

	// The document snapshot cap is exactly one MiB.
	atCap := strings.Repeat("a", appplugin.MaxLanguageDocumentBytes)
	if _, err := fixture.host.Open(languageDocument("at-cap", "sql", atCap)); err != nil {
		t.Fatalf("Open at the document cap: %v", err)
	}
	overCap := atCap + "a"
	if _, err := fixture.host.Open(languageDocument("over-cap", "sql", overCap)); err == nil {
		t.Fatal("Open accepted a document above the cap")
	}

	fixture.host.CloseDocument("doc-1")
	if _, ok := fixture.host.Version("doc-1"); ok {
		t.Fatal("CloseDocument left the document open")
	}
	if results := fixture.host.AllResults("doc-1"); len(results) != 0 {
		t.Fatalf("results after close = %+v", results)
	}
}

func TestLanguageHostCompletionStacksAndDeduplicates(t *testing.T) {
	builtin := appplugin.LanguageBuiltin{
		Complete: func(context.Context, appplugin.LanguageDocument) []appplugin.LanguageCompletionItem {
			return []appplugin.LanguageCompletionItem{
				{Label: "shared", SortKey: "a"},
				{Label: "builtin-only", SortKey: "d"},
			}
		},
	}
	fixture := newLanguageFixture(t, builtin, appplugin.LanguageTimings{})
	fixture.grant(t)
	high := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[
			{"label":"shared","sortKey":"z"},
			{"label":"alpha","sortKey":"b"}
		]}`)
	}}
	low := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[
			{"label":"beta","sortKey":"c"},
			{"label":"alpha","sortKey":"z"}
		]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "high", Languages: []string{"sql"}, Priority: 10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, high)
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "low", Languages: []string{"sql"}, Priority: -10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, low)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	var labels []string
	for _, item := range result.Completions {
		labels = append(labels, item.Label)
	}
	want := []string{"shared", "alpha", "beta", "builtin-only"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Fatalf("completion labels = %v, want %v", labels, want)
	}
	if result.Providers != 2 {
		t.Fatalf("providers = %d, want 2", result.Providers)
	}
	if _, ok := fixture.host.Results("doc-1", domainplugin.LanguageCompletion); !ok {
		t.Fatal("the completion result was not stored for the document")
	}
	if got := fixture.host.Timings(); got.CompletionDebounce != 150*time.Millisecond ||
		got.DiagnosticsDebounce != 300*time.Millisecond {
		t.Fatalf("default timings = %+v", got)
	}
}

func TestLanguageHostHighlightCannotReplaceTheBuiltinLayer(t *testing.T) {
	builtin := appplugin.LanguageBuiltin{
		Highlight: func(appplugin.LanguageDocument) []appplugin.LanguageHighlight {
			return []appplugin.LanguageHighlight{{
				Range: appplugin.LanguageRange{
					Start: appplugin.LanguagePosition{Line: 0, Column: 0},
					End:   appplugin.LanguagePosition{Line: 0, Column: 6},
				},
				Kind: "keyword",
			}}
		},
	}
	fixture := newLanguageFixture(t, builtin, appplugin.LanguageTimings{})
	fixture.grant(t)
	high := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"highlights":[
			{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":3}},"kind":"function"},
			{"range":{"start":{"line":0,"column":7},"end":{"line":0,"column":10}},"kind":"type"}
		]}`)
	}}
	low := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"highlights":[
			{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":6}},"kind":"string"}
		]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "high", Languages: []string{"sql"}, Priority: 10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHighlight},
	}, high)
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "low", Languages: []string{"sql"}, Priority: -10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHighlight},
	}, low)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select x")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageHighlight)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(result.Highlights) != 2 {
		t.Fatalf("highlights = %+v", result.Highlights)
	}
	if result.Highlights[0].Kind != "keyword" ||
		result.Highlights[0].Range.End.Column != 6 {
		t.Fatalf("the base highlight was replaced: %+v", result.Highlights[0])
	}
	if result.Highlights[1].Kind != "type" ||
		result.Highlights[1].Range.Start.Column != 7 {
		t.Fatalf("the non-overlapping provider token was lost: %+v", result.Highlights[1])
	}
}

func TestLanguageHostFormattingHasOneWinner(t *testing.T) {
	builtin := appplugin.LanguageBuiltin{
		Format: func(context.Context, appplugin.LanguageDocument) []appplugin.LanguageTextEdit {
			return []appplugin.LanguageTextEdit{{
				Range:   appplugin.LanguageRange{},
				NewText: "builtin",
			}}
		},
	}
	fixture := newLanguageFixture(t, builtin, appplugin.LanguageTimings{})
	fixture.grant(t)
	high := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"edits":[
			{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":0}},"newText":"winner"}
		]}`)
	}}
	low := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"edits":[
			{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":0}},"newText":"loser"}
		]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "high", Languages: []string{"sql"}, Priority: 10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageFormatting},
	}, high)
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "low", Languages: []string{"sql"}, Priority: -10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageFormatting},
	}, low)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageFormatting)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(result.Formatting) != 1 || result.Formatting[0].NewText != "winner" {
		t.Fatalf("formatting = %+v", result.Formatting)
	}
	if low.callCount() != 0 {
		t.Fatalf("the lower-priority formatting provider ran %d times", low.callCount())
	}

	// A failing winner degrades only itself and falls back to the built-in
	// formatting; the lower-priority provider still does not run.
	high.respond = func(languageCall) ([]byte, error) {
		return nil, errors.New("formatter crashed")
	}
	result, err = fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageFormatting)
	if err != nil {
		t.Fatalf("Request after failure: %v", err)
	}
	if len(result.Formatting) != 1 || result.Formatting[0].NewText != "builtin" {
		t.Fatalf("formatting fallback = %+v", result.Formatting)
	}
	if len(result.Failures) != 1 || result.Failures[0].Code != "fault" {
		t.Fatalf("formatting failures = %+v", result.Failures)
	}
	if low.callCount() != 0 {
		t.Fatalf("the lower-priority provider ran after the winner failed")
	}
}

func TestLanguageHostFailureIsolationAndRetry(t *testing.T) {
	builtin := appplugin.LanguageBuiltin{
		Diagnostics: func(context.Context, appplugin.LanguageDocument) []appplugin.LanguageDiagnostic {
			return []appplugin.LanguageDiagnostic{{
				Range:    appplugin.LanguageRange{},
				Severity: appplugin.LanguageSeverityInformation,
				Message:  "builtin diagnostic",
			}}
		},
	}
	fixture := newLanguageFixture(t, builtin, appplugin.LanguageTimings{})
	fixture.grant(t)
	failing := &languageFake{respond: func(languageCall) ([]byte, error) {
		return nil, errors.New("provider failed")
	}}
	working := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"diagnostics":[
			{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},
			 "severity":1,"message":"provider diagnostic","code":"demo","source":"demo"}
		]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "failing", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageDiagnostics},
	}, failing)
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "working", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageDiagnostics},
	}, working)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageDiagnostics)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if result.Diagnostics[0].Message != "builtin diagnostic" ||
		result.Diagnostics[1].Message != "provider diagnostic" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if len(result.Failures) != 1 || result.Failures[0].ProviderID != "failing" ||
		result.Failures[0].Code != "fault" {
		t.Fatalf("failures = %+v", result.Failures)
	}
	degraded := fixture.host.Degraded()
	if len(degraded) != 1 || degraded[0].ProviderID != "failing" ||
		!degraded[0].Degraded || degraded[0].FailureCode != "fault" {
		t.Fatalf("degraded = %+v", degraded)
	}

	// A degraded provider is skipped on the next request; the working provider
	// and the built-in still answer.
	result, err = fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageDiagnostics)
	if err != nil {
		t.Fatalf("Request while degraded: %v", err)
	}
	if len(result.Diagnostics) != 2 || len(result.Failures) != 0 {
		t.Fatalf("degraded result = %+v", result)
	}
	if failing.callCount() != 1 {
		t.Fatalf("the degraded provider ran %d times", failing.callCount())
	}

	if !fixture.host.RetryProvider(fixture.fiber, "failing") {
		t.Fatal("RetryProvider did not find the degraded provider")
	}
	if len(fixture.host.Degraded()) != 0 {
		t.Fatal("RetryProvider left the provider degraded")
	}
}

func TestLanguageHostUnloadClearsPublishedResults(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[{"label":"before-unload"}]}`)
	}}
	provider := domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}
	disposer := fixture.register(t, provider, invoker)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if result, ok := fixture.host.Results("doc-1", domainplugin.LanguageCompletion); !ok ||
		len(result.Completions) != 1 {
		t.Fatalf("stored result = %+v, %v", result, ok)
	}
	if err := disposer(context.Background()); err != nil {
		t.Fatalf("disposer: %v", err)
	}
	if result, ok := fixture.host.Results("doc-1", domainplugin.LanguageCompletion); ok {
		t.Fatalf("the unloaded provider's result survived: %+v", result)
	}

	// Registering a replacement clears the old state and serves the document.
	replacement := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[{"label":"after-register"}]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "replacement", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, replacement)
	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request after register: %v", err)
	}
	if len(result.Completions) != 1 || result.Completions[0].Label != "after-register" {
		t.Fatalf("result after register = %+v", result)
	}
}

func TestLanguageHostBusyIsTransientNotDegrading(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{respond: func(languageCall) ([]byte, error) {
		return nil, appplugin.ErrCallInFlight
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, invoker)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(result.Failures) != 0 || len(fixture.host.Degraded()) != 0 {
		t.Fatalf("busy result = %+v, degraded = %+v", result, fixture.host.Degraded())
	}
	invoker.respond = func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[{"label":"after-busy"}]}`)
	}
	result, err = fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request after busy: %v", err)
	}
	if len(result.Completions) != 1 || result.Completions[0].Label != "after-busy" ||
		result.Providers != 1 {
		t.Fatalf("result after busy = %+v", result)
	}
}

func TestLanguageHostTimeoutAndStaleVersion(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	slow := &languageFake{block: true, release: make(chan struct{})}
	defer close(slow.release)
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "slow", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHighlight},
	}, slow)
	stale := &languageFake{respond: func(languageCall) ([]byte, error) {
		return okLanguageResponse(99, `{"highlights":[]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "stale", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHighlight},
	}, stale)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	start := time.Now()
	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageHighlight)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if elapsed := time.Since(start); elapsed < appplugin.LanguageDeadline(domainplugin.LanguageHighlight) {
		t.Fatalf("the slow provider returned after %v, before its deadline", elapsed)
	}
	codes := make(map[string]bool)
	for _, failure := range result.Failures {
		codes[failure.Code] = true
	}
	if !codes["timeout"] || !codes["stale"] {
		t.Fatalf("failure codes = %+v", result.Failures)
	}
	if len(result.Highlights) != 0 || result.Providers != 0 {
		t.Fatalf("failed providers contributed results: %+v", result)
	}
}

func TestLanguageHostDebouncesCompletionAndDiagnostics(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{},
		appplugin.LanguageTimings{
			CompletionDebounce:  80 * time.Millisecond,
			DiagnosticsDebounce: 120 * time.Millisecond,
		})
	fixture.grant(t)
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		switch call.Kind {
		case domainplugin.LanguageCompletion:
			return okLanguageResponse(call.Version, `{"items":[]}`)
		case domainplugin.LanguageDiagnostics:
			return okLanguageResponse(call.Version, `{"diagnostics":[]}`)
		default:
			return nil, errors.New("unexpected capability")
		}
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{
			domainplugin.LanguageCompletion, domainplugin.LanguageDiagnostics,
		},
	}, invoker)

	document := languageDocument("doc-1", "sql", "select 1")
	if _, err := fixture.host.Open(document); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if count := invoker.callCount(); count != 0 {
		t.Fatalf("the provider ran %d times before the debounce elapsed", count)
	}

	waitLanguage(t, 2*time.Second, "the debounced completion", func() bool {
		result, ok := fixture.host.Results("doc-1", domainplugin.LanguageCompletion)
		return ok && result.Version == 1
	})
	waitLanguage(t, 2*time.Second, "the debounced diagnostics", func() bool {
		result, ok := fixture.host.Results("doc-1", domainplugin.LanguageDiagnostics)
		return ok && result.Version == 1
	})

	document.Content = "select 2"
	version, err := fixture.host.Update(document)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if version != 2 {
		t.Fatalf("version after Update = %d", version)
	}
	waitLanguage(t, 2*time.Second, "the completion for the new version", func() bool {
		result, ok := fixture.host.Results("doc-1", domainplugin.LanguageCompletion)
		return ok && result.Version == 2
	})
	invoker.mu.Lock()
	for _, call := range invoker.calls {
		if call.Content == "select 1" && call.Version == 2 {
			invoker.mu.Unlock()
			t.Fatalf("a debounced request mixed the old content with version 2: %+v", call)
		}
	}
	invoker.mu.Unlock()
}

// debounceResponder answers the two debounced capabilities with empty data.
func debounceResponder(call languageCall) ([]byte, error) {
	switch call.Kind {
	case domainplugin.LanguageCompletion:
		return okLanguageResponse(call.Version, `{"items":[]}`)
	case domainplugin.LanguageDiagnostics:
		return okLanguageResponse(call.Version, `{"diagnostics":[]}`)
	default:
		return nil, errors.New("unexpected capability")
	}
}

func registerDebounceProvider(t *testing.T, fixture *languageFixture,
	invoker appplugin.LanguageInvoker) {
	t.Helper()
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{
			domainplugin.LanguageCompletion, domainplugin.LanguageDiagnostics,
		},
	}, invoker)
}

func TestLanguageHostDebounceIsDrivenByTheInjectedClock(t *testing.T) {
	clock := newFakeLanguageScheduler()
	fixture := newLanguageFixtureWithScheduler(t, appplugin.LanguageBuiltin{},
		appplugin.DefaultLanguageTimings(), clock)
	fixture.grant(t)
	invoker := &languageFake{respond: debounceResponder}
	registerDebounceProvider(t, fixture, invoker)

	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if runs := clock.runCount(); runs != 0 {
		t.Fatalf("%d timer callbacks ran synchronously at Open", runs)
	}
	clock.Advance(149 * time.Millisecond)
	if calls := len(invoker.callsOf(domainplugin.LanguageCompletion)); calls != 0 {
		t.Fatalf("completion ran %d times before 150ms", calls)
	}
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 0 {
		t.Fatalf("diagnostics ran %d times before 150ms", calls)
	}
	clock.Advance(time.Millisecond)
	if calls := len(invoker.callsOf(domainplugin.LanguageCompletion)); calls != 1 {
		t.Fatalf("completion ran %d times at 150ms, want 1", calls)
	}
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 0 {
		t.Fatalf("diagnostics ran %d times at 150ms, want 0", calls)
	}
	if result, ok := fixture.host.Results("doc-1",
		domainplugin.LanguageCompletion); !ok || result.Version != 1 {
		t.Fatalf("stored completion = %+v, %v", result, ok)
	}

	clock.Advance(149 * time.Millisecond) // 299ms
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 0 {
		t.Fatalf("diagnostics ran %d times before 300ms", calls)
	}
	clock.Advance(time.Millisecond) // 300ms
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 1 {
		t.Fatalf("diagnostics ran %d times at 300ms, want 1", calls)
	}
	if result, ok := fixture.host.Results("doc-1",
		domainplugin.LanguageDiagnostics); !ok || result.Version != 1 {
		t.Fatalf("stored diagnostics = %+v, %v", result, ok)
	}

	// Advancing further never repeats a debounced request.
	clock.Advance(time.Hour)
	if calls := len(invoker.callsOf(domainplugin.LanguageCompletion)); calls != 1 {
		t.Fatalf("completion ran %d times after a further hour", calls)
	}
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 1 {
		t.Fatalf("diagnostics ran %d times after a further hour", calls)
	}
}

func TestLanguageHostSetPositionReschedulesDiagnostics(t *testing.T) {
	clock := newFakeLanguageScheduler()
	fixture := newLanguageFixtureWithScheduler(t, appplugin.LanguageBuiltin{},
		appplugin.DefaultLanguageTimings(), clock)
	fixture.grant(t)
	invoker := &languageFake{respond: debounceResponder}
	registerDebounceProvider(t, fixture, invoker)

	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	clock.Advance(50 * time.Millisecond)
	moved := appplugin.LanguagePosition{Line: 2, Column: 7}
	selection := &appplugin.LanguageSelection{Start: moved, End: moved}
	if !fixture.host.SetPosition("doc-1", moved, selection) {
		t.Fatal("SetPosition reported the document is not open")
	}

	// Open armed diagnostics at 300ms; the cursor move must re-arm it at
	// 350ms under the new epoch instead of leaving the old timer to expire.
	clock.Advance(299 * time.Millisecond) // 349ms
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 0 {
		t.Fatalf("diagnostics ran %d times before the re-armed deadline", calls)
	}
	clock.Advance(time.Millisecond) // 350ms
	diagnostics := invoker.callsOf(domainplugin.LanguageDiagnostics)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics ran %d times, want exactly 1", len(diagnostics))
	}
	if diagnostics[0].Cursor != moved {
		t.Fatalf("diagnostics cursor = %+v, want %+v", diagnostics[0].Cursor, moved)
	}
	if diagnostics[0].Version != 1 {
		t.Fatalf("diagnostics version = %d, want 1", diagnostics[0].Version)
	}
	if result, ok := fixture.host.Results("doc-1",
		domainplugin.LanguageDiagnostics); !ok || result.Version != 1 {
		t.Fatalf("stored diagnostics = %+v, %v", result, ok)
	}

	// Completion was re-armed too; it carries the new position as well.
	completions := invoker.callsOf(domainplugin.LanguageCompletion)
	if len(completions) != 1 || completions[0].Cursor != moved {
		t.Fatalf("completion calls = %+v", completions)
	}
	clock.Advance(time.Hour)
	if calls := len(invoker.callsOf(domainplugin.LanguageDiagnostics)); calls != 1 {
		t.Fatalf("diagnostics ran %d times after a further hour, want 1", calls)
	}
}

func TestLanguageHostSupersessionCancelsOlderDebounceTimers(t *testing.T) {
	clock := newFakeLanguageScheduler()
	fixture := newLanguageFixtureWithScheduler(t, appplugin.LanguageBuiltin{},
		appplugin.DefaultLanguageTimings(), clock)
	fixture.grant(t)
	invoker := &languageFake{respond: debounceResponder}
	registerDebounceProvider(t, fixture, invoker)

	document := languageDocument("doc-1", "sql", "select 1")
	if _, err := fixture.host.Open(document); err != nil {
		t.Fatalf("Open: %v", err)
	}
	clock.Advance(100 * time.Millisecond)
	document.Content = "select 2"
	version, err := fixture.host.Update(document)
	if err != nil || version != 2 {
		t.Fatalf("Update = %d, %v", version, err)
	}

	// The completion armed at Open was due at 150ms. Update replaced it, so
	// no callback runs at all: the epoch guard alone would still run it.
	clock.Advance(50 * time.Millisecond) // 150ms
	if runs := clock.runCount(); runs != 0 {
		t.Fatalf("%d superseded timer callbacks ran", runs)
	}
	if calls := len(invoker.callsOf(domainplugin.LanguageCompletion)); calls != 0 {
		t.Fatalf("the superseded completion ran %d times", calls)
	}

	clock.Advance(100 * time.Millisecond) // 250ms: the new completion is due
	completions := invoker.callsOf(domainplugin.LanguageCompletion)
	if len(completions) != 1 || completions[0].Version != 2 {
		t.Fatalf("completion calls = %+v, want one at version 2", completions)
	}
	clock.Advance(150 * time.Millisecond) // 400ms: the new diagnostics is due
	diagnostics := invoker.callsOf(domainplugin.LanguageDiagnostics)
	if len(diagnostics) != 1 || diagnostics[0].Version != 2 {
		t.Fatalf("diagnostics calls = %+v, want one at version 2", diagnostics)
	}
	clock.Advance(time.Hour)
	if runs := clock.runCount(); runs != 2 {
		t.Fatalf("%d timer callbacks ran, want exactly the two re-armed ones", runs)
	}
	if result, ok := fixture.host.Results("doc-1",
		domainplugin.LanguageCompletion); !ok || result.Version != 2 {
		t.Fatalf("stored completion = %+v, %v", result, ok)
	}
}

func TestLanguageHostHonoredCancellationIsTransientNotAFault(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{
		CompletionDebounce: time.Hour, DiagnosticsDebounce: time.Hour,
	})
	fixture.grant(t)
	base := &languageFake{
		block: true, release: make(chan struct{}), started: make(chan languageCall, 1),
		respond: func(call languageCall) ([]byte, error) {
			// The committed sample returns success with no response when it
			// observes host cancellation; emulate exactly that outcome.
			return nil, &appplugin.LanguageNoResponseError{
				Provider: "demo", Kind: call.Kind,
			}
		},
	}
	canceled := make(chan uint64, 1)
	invoker := &cancelRecordingInvoker{languageFake: base, canceled: canceled}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageDiagnostics},
	}, invoker)

	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	type outcome struct {
		result appplugin.LanguageDocumentResults
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fixture.host.Request(context.Background(), "doc-1",
			domainplugin.LanguageDiagnostics)
		done <- outcome{result: result, err: err}
	}()
	var inFlight languageCall
	select {
	case inFlight = <-base.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the provider call never started")
	}

	document := languageDocument("doc-1", "sql", "select 2")
	if _, err := fixture.host.Update(document); err != nil {
		t.Fatalf("Update: %v", err)
	}
	select {
	case requestID := <-canceled:
		if requestID != inFlight.RequestID {
			t.Fatalf("cancel asked for request %d, want %d", requestID, inFlight.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the superseded request was not canceled")
	}
	close(base.release)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the superseded request did not return")
	}
	if got.err != nil {
		t.Fatalf("Request: %v", got.err)
	}
	if len(got.result.Failures) != 0 {
		t.Fatalf("an honored cancellation was reported as a fault: %+v", got.result.Failures)
	}
	if degraded := fixture.host.Degraded(); len(degraded) != 0 {
		t.Fatalf("an honored cancellation degraded the provider: %+v", degraded)
	}
	if len(got.result.Diagnostics) != 0 || got.result.Providers != 0 {
		t.Fatalf("the superseded result was published: %+v", got.result)
	}
	if !fixture.audit.hasReason("language.diagnostics:canceled") {
		t.Fatal("the honored cancellation was not audited as canceled")
	}
	if result, ok := fixture.host.Results("doc-1", domainplugin.LanguageDiagnostics); ok {
		t.Fatalf("the superseded result was stored: %+v", result)
	}
}

func TestLanguageHostCancelsSupersededRequests(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{},
		appplugin.LanguageTimings{
			CompletionDebounce:  20 * time.Millisecond,
			DiagnosticsDebounce: time.Hour,
		})
	fixture.grant(t)
	invoker := &languageFake{
		block:   true,
		release: make(chan struct{}),
		started: make(chan languageCall, 4),
		respond: func(call languageCall) ([]byte, error) {
			return okLanguageResponse(call.Version, `{"items":[]}`)
		},
	}
	// Record cooperative cancellation explicitly: the fake invoker replaces the
	// generic no-op so the test observes the request id.
	cancelRecording := &cancelRecordingInvoker{languageFake: invoker, canceled: make(chan uint64, 4)}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, cancelRecording)

	document := languageDocument("doc-1", "sql", "select 1")
	if _, err := fixture.host.Open(document); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var first languageCall
	select {
	case first = <-invoker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the debounced provider call never started")
	}

	document.Content = "select 2"
	if _, err := fixture.host.Update(document); err != nil {
		t.Fatalf("Update: %v", err)
	}
	select {
	case requestID := <-cancelRecording.canceled:
		if requestID != first.RequestID {
			t.Fatalf("cancel asked for request %d, want %d", requestID, first.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the superseded request was not canceled")
	}
	close(invoker.release)

	if _, ok := fixture.host.Results("doc-1", domainplugin.LanguageCompletion); ok {
		t.Fatal("the superseded completion was published")
	}
}

// cancelRecordingInvoker records the ids the router asks to cancel.
type cancelRecordingInvoker struct {
	*languageFake
	canceled chan uint64
}

func (c *cancelRecordingInvoker) CancelLanguage(_ context.Context, requestID uint64) error {
	c.canceled <- requestID
	return nil
}

func TestLanguageHostRejectsHTMLAndUnknownData(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version,
			`{"hover":{"contents":"<script>alert(1)</script> javascript:alert(1) **bold** plain","format":"markdown"}}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHover},
	}, invoker)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageHover)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if result.Hover == nil {
		t.Fatal("the hover result was dropped")
	}
	contents := result.Hover.Contents
	if strings.Contains(contents, "<script>") || strings.Contains(contents, "javascript:") {
		t.Fatalf("hover kept active markup: %q", contents)
	}
	if !strings.Contains(contents, "plain") || result.Hover.Format != "markdown" {
		t.Fatalf("hover = %+v", result.Hover)
	}

	// An unknown field inside the closed data shape is rejected rather than
	// silently rendered.
	invoker.respond = func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version,
			`{"hover":{"contents":"text"},"html":"<script>alert(1)</script>"}`)
	}
	result, err = fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageHover)
	if err != nil {
		t.Fatalf("Request with unknown data: %v", err)
	}
	if result.Hover != nil || len(result.Failures) != 1 {
		t.Fatalf("unknown data result = %+v", result)
	}

	// An unknown hover format is rejected. Retry first because the rejected
	// data above degraded the provider.
	fixture.host.RetryProvider(fixture.fiber, "demo")
	invoker.respond = func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version,
			`{"hover":{"contents":"text","format":"html"}}`)
	}
	result, err = fixture.host.Request(context.Background(), "doc-1", domainplugin.LanguageHover)
	if err != nil {
		t.Fatalf("Request with an HTML format: %v", err)
	}
	if result.Hover != nil || len(result.Failures) != 1 {
		t.Fatalf("HTML hover result = %+v", result)
	}
}

func TestLanguageHostBoundsResultCollections(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	completionItems := make([]string, appplugin.MaxLanguageCompletionItems+50)
	for i := range completionItems {
		completionItems[i] = fmt.Sprintf(`{"label":"item-%04d"}`, i)
	}
	diagnostics := make([]string, appplugin.MaxLanguageDiagnostics+50)
	for i := range diagnostics {
		diagnostics[i] = fmt.Sprintf(
			`{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},`+
				`"severity":2,"message":"diagnostic %04d"}`, i)
	}
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		switch call.Kind {
		case domainplugin.LanguageCompletion:
			return okLanguageResponse(call.Version,
				`{"items":[`+strings.Join(completionItems, ",")+`]}`)
		case domainplugin.LanguageDiagnostics:
			return okLanguageResponse(call.Version,
				`{"diagnostics":[`+strings.Join(diagnostics, ",")+`]}`)
		default:
			return nil, errors.New("unexpected capability")
		}
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{
			domainplugin.LanguageCompletion, domainplugin.LanguageDiagnostics,
		},
	}, invoker)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	completion, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request completion: %v", err)
	}
	if len(completion.Completions) != appplugin.MaxLanguageCompletionItems ||
		!completion.Truncated {
		t.Fatalf("completions = %d truncated %v", len(completion.Completions),
			completion.Truncated)
	}
	diagnosticResult, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageDiagnostics)
	if err != nil {
		t.Fatalf("Request diagnostics: %v", err)
	}
	if len(diagnosticResult.Diagnostics) != appplugin.MaxLanguageDiagnostics ||
		!diagnosticResult.Truncated {
		t.Fatalf("diagnostics = %d truncated %v", len(diagnosticResult.Diagnostics),
			diagnosticResult.Truncated)
	}

	// Merging two hovers cannot exceed the hover byte cap either.
	hover := strings.Repeat("h", appplugin.MaxLanguageHoverBytes/2+1)
	first := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version,
			`{"hover":{"contents":`+jsonString(hover)+`}}`)
	}}
	second := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version,
			`{"hover":{"contents":`+jsonString(hover)+`}}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "hover-a", Languages: []string{"sql"}, Priority: 10,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHover},
	}, first)
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "hover-b", Languages: []string{"sql"}, Priority: 5,
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageHover},
	}, second)
	merged, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageHover)
	if err != nil {
		t.Fatalf("Request hover: %v", err)
	}
	if merged.Hover == nil {
		t.Fatal("the merged hover was dropped")
	}
	if len(merged.Hover.Contents) > appplugin.MaxLanguageHoverBytes || !merged.Truncated {
		t.Fatalf("merged hover bytes = %d truncated %v",
			len(merged.Hover.Contents), merged.Truncated)
	}
}

// hasReason reports whether an audit event carries reason.
func (a *recordingAudit) hasReason(reason string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, event := range a.events {
		if event.Reason == reason {
			return true
		}
	}
	return false
}

func (a *recordingAudit) snapshot() []domainplugin.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domainplugin.AuditEvent(nil), a.events...)
}

func TestLanguageHostValidatesDataShapes(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{
			domainplugin.LanguageDiagnostics, domainplugin.LanguageHighlight,
			domainplugin.LanguageNavigation, domainplugin.LanguageCodeActions,
			domainplugin.LanguageSignatureHelp,
		},
	}, invoker)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "select 1")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	tests := []struct {
		name       string
		capability domainplugin.LanguageCapability
		data       string
		validate   func(t *testing.T, result appplugin.LanguageDocumentResults)
	}{
		{
			name: "severity outside the closed set", capability: domainplugin.LanguageDiagnostics,
			data: `{"diagnostics":[{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},"severity":9,"message":"x"}]}`,
			validate: func(t *testing.T, result appplugin.LanguageDocumentResults) {
				if len(result.Diagnostics) != 0 || len(result.Failures) != 1 {
					t.Fatalf("result = %+v", result)
				}
			},
		},
		{
			name: "unknown highlight kind", capability: domainplugin.LanguageHighlight,
			data: `{"highlights":[{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},"kind":"sparkle"}]}`,
			validate: func(t *testing.T, result appplugin.LanguageDocumentResults) {
				if len(result.Highlights) != 0 || len(result.Failures) != 1 {
					t.Fatalf("result = %+v", result)
				}
			},
		},
		{
			name: "cross-document navigation is dropped", capability: domainplugin.LanguageNavigation,
			data: `{"locations":[
				{"documentId":"other","range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},"label":"elsewhere"},
				{"documentId":"doc-1","range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},"label":"here"}
			]}`,
			validate: func(t *testing.T, result appplugin.LanguageDocumentResults) {
				if len(result.Navigation) != 1 || result.Navigation[0].Label != "here" ||
					result.Navigation[0].DocumentID != "doc-1" {
					t.Fatalf("navigation = %+v", result.Navigation)
				}
			},
		},
		{
			name: "code actions carry bounded edits", capability: domainplugin.LanguageCodeActions,
			data: `{"actions":[{"title":"Replace","kind":"quickfix","edits":[
				{"range":{"start":{"line":0,"column":0},"end":{"line":0,"column":1}},"newText":"x"}
			]}]}`,
			validate: func(t *testing.T, result appplugin.LanguageDocumentResults) {
				if len(result.CodeActions) != 1 ||
					len(result.CodeActions[0].Edits) != 1 {
					t.Fatalf("actions = %+v", result.CodeActions)
				}
			},
		},
		{
			name: "signature help keeps a valid active index", capability: domainplugin.LanguageSignatureHelp,
			data: `{"signatures":[
				{"label":"f(a)","parameters":[{"label":"a"}]},
				{"label":"g(b)","parameters":[{"label":"b"}]}
			],"activeSignature":9}`,
			validate: func(t *testing.T, result appplugin.LanguageDocumentResults) {
				if result.Signature == nil || len(result.Signature.Signatures) != 2 ||
					result.Signature.ActiveSignature != 0 {
					t.Fatalf("signature = %+v", result.Signature)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// A rejected shape degrades the provider; each case starts clean so
			// it exercises the shape validation rather than the degraded skip.
			fixture.host.RetryProvider(fixture.fiber, "demo")
			data := test.data
			invoker.respond = func(call languageCall) ([]byte, error) {
				return okLanguageResponse(call.Version, data)
			}
			result, err := fixture.host.Request(context.Background(), "doc-1", test.capability)
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			test.validate(t, result)
		})
	}
}

func TestLanguageHostContextsAndDocumentRouting(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "editor-only", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
		Contexts:     []string{"editor"},
	}, invoker)

	editorDocument := languageDocument("doc-1", "sql", "select 1")
	gridDocument := languageDocument("doc-2", "sql", "select 2")
	gridDocument.Context = appplugin.LanguageContext{Kind: "grid"}
	if _, err := fixture.host.Open(editorDocument); err != nil {
		t.Fatalf("Open editor document: %v", err)
	}
	if _, err := fixture.host.Open(gridDocument); err != nil {
		t.Fatalf("Open grid document: %v", err)
	}
	result, err := fixture.host.Request(context.Background(), "doc-2",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request grid: %v", err)
	}
	if result.Providers != 0 || invoker.callCount() != 0 {
		t.Fatalf("a context-restricted provider ran outside its context: %+v", result)
	}
	result, err = fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion)
	if err != nil {
		t.Fatalf("Request editor: %v", err)
	}
	if result.Providers != 1 || result.DocumentID != "doc-1" || invoker.callCount() != 1 {
		t.Fatalf("editor routing = %+v", result)
	}
	if call := invoker.lastCall(t); call.DocumentID != "doc-1" {
		t.Fatalf("the provider received document %q", call.DocumentID)
	}
}

func TestLanguageHostAuditsWithoutContent(t *testing.T) {
	fixture := newLanguageFixture(t, appplugin.LanguageBuiltin{}, appplugin.LanguageTimings{})
	fixture.grant(t)
	invoker := &languageFake{respond: func(call languageCall) ([]byte, error) {
		return okLanguageResponse(call.Version, `{"items":[]}`)
	}}
	fixture.register(t, domainplugin.LanguageProvider{
		ID: "demo", Languages: []string{"sql"},
		Capabilities: []domainplugin.LanguageCapability{domainplugin.LanguageCompletion},
	}, invoker)
	if _, err := fixture.host.Open(languageDocument("doc-1", "sql", "secret-content")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := fixture.host.Request(context.Background(), "doc-1",
		domainplugin.LanguageCompletion); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if !fixture.audit.hasReason("language.completion:ok") {
		t.Fatal("the successful request was not audited")
	}
	for _, event := range fixture.audit.snapshot() {
		if strings.Contains(event.Reason, "secret-content") {
			t.Fatalf("audit recorded document content: %q", event.Reason)
		}
		if event.Permission != domainplugin.PermissionEditorLanguage {
			t.Fatalf("audit permission = %q", event.Permission)
		}
	}
}
