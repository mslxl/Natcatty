package state

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// fakeComposeDirectory is the picker's Session state and lister seam. Tests
// change cwd and live under the lock, then refresh the picker.
type fakeComposeDirectory struct {
	mu          sync.Mutex
	cwd         string
	live        bool
	err         error
	listings    map[string]ComposeDirectoryListing
	requests    []string
	listBegin   chan struct{}
	listRelease chan struct{}
}

func newFakeComposeDirectory(cwd string) *fakeComposeDirectory {
	return &fakeComposeDirectory{cwd: cwd, live: true, listings: map[string]ComposeDirectoryListing{}}
}

func (f *fakeComposeDirectory) Session(model.SessionID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cwd, f.live
}

func (f *fakeComposeDirectory) List(_ context.Context, session *appsession.Session, path string) (ComposeDirectoryListing, error) {
	f.mu.Lock()
	f.requests = append(f.requests, path)
	err := f.err
	listing, ok := f.listings[path]
	if !ok {
		listing = ComposeDirectoryListing{Path: path}
	}
	begin, release := f.listBegin, f.listRelease
	f.mu.Unlock()
	if begin != nil {
		close(begin)
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return ComposeDirectoryListing{}, err
	}
	return listing, nil
}

func (f *fakeComposeDirectory) setCWD(cwd string) {
	f.mu.Lock()
	f.cwd = cwd
	f.mu.Unlock()
}

func (f *fakeComposeDirectory) setLive(live bool) {
	f.mu.Lock()
	f.live = live
	f.mu.Unlock()
}

func (f *fakeComposeDirectory) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeComposeDirectory) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeComposeDirectory) add(path string, entries ...ComposeDirectoryEntry) {
	f.mu.Lock()
	f.listings[path] = ComposeDirectoryListing{Path: path, Entries: entries}
	f.mu.Unlock()
}

// addListing answers request with a different resolved path, as the home
// fallback and a ".." listing do.
func (f *fakeComposeDirectory) addListing(request, resolved string, entries ...ComposeDirectoryEntry) {
	f.mu.Lock()
	f.listings[request] = ComposeDirectoryListing{Path: resolved, Entries: entries}
	f.mu.Unlock()
}

// openComposeDirectoryPicker builds a shell with one Tab and opens the picker
// for it. The fake source answers for that Session.
func openComposeDirectoryPicker(t *testing.T, source *fakeComposeDirectory) (*inputGraph, model.SessionID) {
	t.Helper()
	graph := newInputGraph(t, 1, WithComposeDirectory(ComposeDirectorySource{
		List: source.List, Session: source.Session,
	}))
	id := graph.sessionID(t, 0)
	if !graph.state.OpenComposeDirectoryFor(id) {
		t.Fatal("the picker did not open")
	}
	return graph, id
}

// waitComposeDirectory waits on the picker's real state with a hard deadline.
// It calls RefreshComposeDirectory like the view does, so a finished listing's
// pending resolved path reaches the bound editor on this (UI) goroutine.
func waitComposeDirectory(t *testing.T, state *State, wantPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state.RefreshComposeDirectory()
		if state.ComposeDirectoryPath() == wantPath && !state.ComposeDirectoryLoading() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("picker path = %q (loading %v, failed %v), want %q",
				state.ComposeDirectoryPath(), state.ComposeDirectoryLoading(),
				state.ComposeDirectoryFailed(), wantPath)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestComposeDirectoryBrowsesFromTheTrackedCWD proves the picker opens at the
// OSC 7 working directory, lists it, and navigates into a directory entry.
func TestComposeDirectoryBrowsesFromTheTrackedCWD(t *testing.T) {
	source := newFakeComposeDirectory("/srv/app")
	source.add("/srv/app",
		ComposeDirectoryEntry{Name: "src", Directory: true},
		ComposeDirectoryEntry{Name: "main.go"},
	)
	source.add("/srv/app/src", ComposeDirectoryEntry{Name: "main.go"})
	graph, _ := openComposeDirectoryPicker(t, source)

	waitComposeDirectory(t, graph.state, "/srv/app")
	if got := graph.state.ComposeDirectoryEntries(); !reflect.DeepEqual(got, []ComposeDirectoryEntry{
		{Name: "src", Directory: true},
		{Name: "main.go"},
	}) {
		t.Fatalf("entries = %+v", got)
	}
	if graph.state.ComposeDirectoryUnknownCWD() {
		t.Error("a tracked CWD was reported as unknown")
	}

	graph.state.NavigateComposeDirectory("src")
	waitComposeDirectory(t, graph.state, "/srv/app/src")
	if got := graph.state.ComposeDirectoryEntries(); !reflect.DeepEqual(got,
		[]ComposeDirectoryEntry{{Name: "main.go"}}) {
		t.Fatalf("entries after navigation = %+v", got)
	}
}

// TestComposeDirectoryFallsBackToHomeWhenTheCWDIsUnknown proves a Session that
// has not reported OSC 7 opens at home and says its CWD is unknown.
func TestComposeDirectoryFallsBackToHomeWhenTheCWDIsUnknown(t *testing.T) {
	source := newFakeComposeDirectory("")
	source.addListing("", "/home/me", ComposeDirectoryEntry{Name: "work", Directory: true})
	graph, _ := openComposeDirectoryPicker(t, source)

	// An empty requested path asks the lister for the home directory.
	waitComposeDirectory(t, graph.state, "/home/me")
	if got := source.requested(); len(got) == 0 || got[0] != "" {
		t.Fatalf("the picker requested %q, want the home directory", got)
	}
	if !graph.state.ComposeDirectoryUnknownCWD() {
		t.Fatal("the picker did not report an unknown CWD")
	}
}

// TestComposeDirectoryRefreshesWhenTheTrackedCWDChanges proves the picker
// follows the Session's OSC 7 directory while it is showing the old root, and
// that a browse inside the old root does not pin the picker there.
func TestComposeDirectoryRefreshesWhenTheTrackedCWDChanges(t *testing.T) {
	source := newFakeComposeDirectory("/one")
	source.add("/one", ComposeDirectoryEntry{Name: "sub", Directory: true})
	source.add("/one/sub")
	source.add("/two", ComposeDirectoryEntry{Name: "file.txt"})
	graph, _ := openComposeDirectoryPicker(t, source)
	waitComposeDirectory(t, graph.state, "/one")

	graph.state.NavigateComposeDirectory("sub")
	waitComposeDirectory(t, graph.state, "/one/sub")

	source.setCWD("/two")
	graph.state.RefreshComposeDirectory()
	waitComposeDirectory(t, graph.state, "/two")
	if got := graph.state.ComposeDirectoryEntries(); !reflect.DeepEqual(got,
		[]ComposeDirectoryEntry{{Name: "file.txt"}}) {
		t.Fatalf("entries after refresh = %+v", got)
	}
}

// TestComposeDirectoryReportsAnUnsupportedShell proves a shell that cannot
// list directories gets its own message instead of a generic failure.
func TestComposeDirectoryReportsAnUnsupportedShell(t *testing.T) {
	source := newFakeComposeDirectory("/srv/app")
	source.setErr(errComposeDirectoryUnsupported)
	graph, _ := openComposeDirectoryPicker(t, source)

	deadline := time.Now().Add(2 * time.Second)
	for !graph.state.ComposeDirectoryUnsupported() {
		if !time.Now().Before(deadline) {
			t.Fatalf("unsupported = false (loading %v, failed %v)",
				graph.state.ComposeDirectoryLoading(), graph.state.ComposeDirectoryFailed())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if graph.state.ComposeDirectoryFailed() {
		t.Fatal("an unsupported shell was also reported as a failed listing")
	}
	if graph.state.ComposeDirectoryLoading() {
		t.Fatal("the picker still reports a listing in flight")
	}
}

// TestComposeDirectoryClosesWhenTheSessionDisconnects proves a dead Session
// removes the picker instead of browsing through it.
func TestComposeDirectoryClosesWhenTheSessionDisconnects(t *testing.T) {
	source := newFakeComposeDirectory("/srv/app")
	graph, _ := openComposeDirectoryPicker(t, source)
	waitComposeDirectory(t, graph.state, "/srv/app")

	source.setLive(false)
	graph.state.RefreshComposeDirectory()
	if graph.state.ComposeDirectoryOpen() {
		t.Fatal("the picker stayed open for a disconnected Session")
	}
}

// TestComposeDirectoryInsertsWithoutSending proves confirming a typed path
// appends it to the compose draft as one quoted word and writes nothing to the
// Session.
func TestComposeDirectoryInsertsWithoutSending(t *testing.T) {
	source := newFakeComposeDirectory("/srv/app")
	graph, _ := openComposeDirectoryPicker(t, source)
	waitComposeDirectory(t, graph.state, "/srv/app")

	graph.state.SetComposeDraft("cd ")
	*graph.state.ComposeDirectoryTyped() = "/srv/app/my dir"
	if !graph.state.ConfirmComposeDirectory() {
		t.Fatal("confirming the path failed")
	}
	if got := *graph.state.ComposeDraft(); got != "cd '/srv/app/my dir'" {
		t.Fatalf("draft = %q", got)
	}
	if got := graph.factory.transport(0).inputString(); got != "" {
		t.Fatalf("confirming wrote %q to the Session", got)
	}
	if graph.state.ComposeDirectoryOpen() {
		t.Fatal("the picker stayed open after confirming")
	}
}

// TestComposeDirectoryConfirmsTheBrowsedDirectoryWhenTheEditorIsEmpty proves
// the confirm action falls back to the current directory.
func TestComposeDirectoryConfirmsTheBrowsedDirectoryWhenTheEditorIsEmpty(t *testing.T) {
	source := newFakeComposeDirectory("/srv/app")
	graph, _ := openComposeDirectoryPicker(t, source)
	waitComposeDirectory(t, graph.state, "/srv/app")

	*graph.state.ComposeDirectoryTyped() = "  "
	if !graph.state.ConfirmComposeDirectory() {
		t.Fatal("confirming the browsed directory failed")
	}
	if got := *graph.state.ComposeDraft(); got != "/srv/app" {
		t.Fatalf("draft = %q", got)
	}
}

// TestComposeDirectoryConfirmingAFileEntryUsesItsFullPath proves a file row
// inserts the entry joined to the browsed directory.
func TestComposeDirectoryConfirmingAFileEntryUsesItsFullPath(t *testing.T) {
	source := newFakeComposeDirectory("/srv/app")
	source.add("/srv/app", ComposeDirectoryEntry{Name: "main.go"})
	graph, _ := openComposeDirectoryPicker(t, source)
	waitComposeDirectory(t, graph.state, "/srv/app")

	if !graph.state.InsertComposeDirectoryEntry("main.go") {
		t.Fatal("confirming the entry failed")
	}
	if got := *graph.state.ComposeDraft(); got != "/srv/app/main.go" {
		t.Fatalf("draft = %q", got)
	}
}

// TestComposeDirectoryListingLeavesTheBoundEditorToTheUIThread proves a
// finished listing never writes the string the TextInput binds directly. It
// leaves the resolved path in the pending field, and RefreshComposeDirectory
// applies it on the UI thread. The listing goroutine used to write the bound
// string under inputMu, which the UI thread reads and writes without that lock.
func TestComposeDirectoryListingLeavesTheBoundEditorToTheUIThread(t *testing.T) {
	source := newFakeComposeDirectory("/requested")
	source.addListing("/requested", "/resolved")
	began := make(chan struct{})
	release := make(chan struct{})
	source.listBegin, source.listRelease = began, release
	graph, _ := openComposeDirectoryPicker(t, source)

	select {
	case <-began:
	case <-time.After(2 * time.Second):
		t.Fatal("the listing did not start")
	}
	typed := graph.state.ComposeDirectoryTyped()
	*typed = "" // the user cleared the editor while the listing was in flight

	// The TextInput reads the bound string without inputMu. Keep such a reader
	// running across completion so `go test -race` reports any listing-goroutine
	// write to the same string. The reader stops before Refresh writes it.
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
				_ = *typed
				runtime.Gosched()
			}
		}
	}()
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for graph.state.ComposeDirectoryPath() != "/resolved" || graph.state.ComposeDirectoryLoading() {
		if !time.Now().Before(deadline) {
			t.Fatalf("listing did not finish: path %q loading %v",
				graph.state.ComposeDirectoryPath(), graph.state.ComposeDirectoryLoading())
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	<-readerDone

	if got := *typed; got != "" {
		t.Fatalf("the listing goroutine wrote the bound editor: %q", got)
	}
	graph.state.RefreshComposeDirectory()
	if got := *typed; got != "/resolved" {
		t.Fatalf("Refresh did not apply the resolved path: %q", got)
	}
}

func TestParseComposeDirectoryOutputOrdersDirectoriesFirst(t *testing.T) {
	listing, err := parseComposeDirectoryOutput(
		"P/srv\x00Dsrc\x00Ddocs\x00Fmain.go\x00F.hidden\x00")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Path != "/srv" {
		t.Fatalf("path = %q", listing.Path)
	}
	want := []ComposeDirectoryEntry{
		{Name: "docs", Directory: true},
		{Name: "src", Directory: true},
		{Name: ".hidden"},
		{Name: "main.go"},
	}
	if !reflect.DeepEqual(listing.Entries, want) {
		t.Fatalf("entries = %+v, want %+v", listing.Entries, want)
	}
	if _, err := parseComposeDirectoryOutput("no marker\n"); err == nil {
		t.Fatal("a listing without a path was accepted")
	}
}

// TestParseComposeDirectoryOutputCannotBeSpoofedByAName proves marker text and
// newlines inside a name stay part of that name. The old newline protocol read
// "__d__docs" as a directory record and a newline inside a name as two entries.
func TestParseComposeDirectoryOutputCannotBeSpoofedByAName(t *testing.T) {
	listing, err := parseComposeDirectoryOutput(
		"P/srv\x00D__D__evil\x00F__d__dir\x00F__f__file\x00Fline\nbreak\x00")
	if err != nil {
		t.Fatal(err)
	}
	want := []ComposeDirectoryEntry{
		{Name: "__D__evil", Directory: true},
		{Name: "__d__dir"},
		{Name: "__f__file"},
		{Name: "line\nbreak"},
	}
	if !reflect.DeepEqual(listing.Entries, want) {
		t.Fatalf("entries = %+v, want %+v", listing.Entries, want)
	}
	// An unknown record cannot supply the required resolved path.
	if _, err := parseComposeDirectoryOutput("__D__not-a-path\x00"); err == nil {
		t.Fatal("an unknown record was accepted as the resolved path")
	}
}

func TestComposeDirectoryCommandsUseTheRecordProtocol(t *testing.T) {
	if got := posixDirectoryCommand("/srv/app"); !strings.Contains(got, `printf 'P%s\000'`) ||
		!strings.Contains(got, `printf '%s%s\000'`) {
		t.Fatalf("POSIX command does not emit NUL records: %q", got)
	}
	if got := powershellDirectoryCommand(`C:\app`); !strings.Contains(got, "[char]0") {
		t.Fatalf("PowerShell command does not emit NUL records: %q", got)
	}
}

func TestComposeDirectoryPathHelpers(t *testing.T) {
	joins := map[[2]string]string{
		{"/srv/app", "src"}:        "/srv/app/src",
		{"/srv/app/", "src"}:       "/srv/app/src",
		{`C:\Users`, "me"}:         `C:\Users\me`,
		{`C:\`, "me"}:              `C:\me`,
		{`\\server\share`, "dir"}:  `\\server\share\dir`,
		{"/srv/app", "/elsewhere"}: "/elsewhere",
		{`C:\Users`, `D:\other`}:   `D:\other`,
		{"", "relative"}:           "relative",
	}
	for input, want := range joins {
		if got := joinComposeDirectory(input[0], input[1]); got != want {
			t.Errorf("joinComposeDirectory(%q, %q) = %q, want %q", input[0], input[1], got, want)
		}
	}

	parents := map[string]string{
		"/srv/app":       "/srv",
		"/srv":           "/",
		"/":              "/",
		`C:\Users\me`:    `C:\Users`,
		`C:\Users`:       `C:\`,
		`C:\`:            `C:\`,
		`\\server\share`: `\\server\share`,
	}
	for input, want := range parents {
		if got := parentComposeDirectory(input); got != want {
			t.Errorf("parentComposeDirectory(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestComposeDirectoryCommandsQuoteThePath(t *testing.T) {
	if got := posixDirectoryCommand("/srv/my app"); !strings.Contains(got, "cd -- '/srv/my app'") {
		t.Fatalf("POSIX command = %q", got)
	}
	if got := powershellDirectoryCommand(`C:\it's here`); !strings.Contains(got, `'C:\it''s here'`) {
		t.Fatalf("PowerShell command = %q", got)
	}
	if got := quoteComposeDirectoryPath("/srv/plain", "posix"); got != "/srv/plain" {
		t.Fatalf("a plain path was quoted: %q", got)
	}
	if got := quoteComposeDirectoryPath("/srv/a'b", "posix"); got != `'/srv/a'\''b'` {
		t.Fatalf("POSIX quote = %q", got)
	}
}
