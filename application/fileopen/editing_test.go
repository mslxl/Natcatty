package fileopen

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// fakeTarget is an in-memory remote file with observable write failures.
type fakeTarget struct {
	mu        sync.Mutex
	path      string
	content   []byte
	modified  time.Time
	failRead  bool
	failWrite bool
	writes    int
}

func newFakeTarget(path, content string) *fakeTarget {
	return &fakeTarget{path: path, content: []byte(content), modified: time.Unix(1000, 0)}
}

func (t *fakeTarget) Path() string { return t.path }

func (t *fakeTarget) Stat(context.Context) (RemoteStat, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return RemoteStat{Size: int64(len(t.content)), Modified: t.modified}, nil
}

func (t *fakeTarget) Read(context.Context) (io.ReadCloser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failRead {
		return nil, errors.New("connection reset")
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), t.content...))), nil
}

func (t *fakeTarget) Write(_ context.Context, content []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failWrite {
		return errors.New("connection reset")
	}
	t.content = append([]byte(nil), content...)
	t.writes++
	return nil
}

func (t *fakeTarget) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.content)
}

func (t *fakeTarget) changeRemote(content string) {
	t.mu.Lock()
	t.content = []byte(content)
	t.mu.Unlock()
}

func (t *fakeTarget) setFailWrite(fail bool) {
	t.mu.Lock()
	t.failWrite = fail
	t.mu.Unlock()
}

func (t *fakeTarget) writeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writes
}

func internalPreferences() *fakePreferences {
	settings := domainpreferences.Defaults()
	settings.SFTPEditor.DefaultOpener = domainpreferences.Opener{Kind: domainpreferences.OpenerInternal}
	return &fakePreferences{settings: domainpreferences.Normalize(settings)}
}

func externalPreferences() *fakePreferences {
	settings := domainpreferences.Defaults()
	settings.SFTPEditor.DefaultOpener = domainpreferences.Opener{
		Kind: domainpreferences.OpenerExternal,
		Application: domainpreferences.ExternalApplication{
			Path: "/usr/bin/editor", Name: "Editor",
		},
	}
	return &fakePreferences{settings: domainpreferences.Normalize(settings)}
}

func remoteRequest(name, content string, target RemoteTarget) Request {
	return Request{
		Name: name, Remote: true, Size: int64(len(content)), Target: target,
		Source: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within the deadline", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInternalRemoteSaveWritesBackAndRefreshesTheBaseline(t *testing.T) {
	root := t.TempDir()
	editor := &fakeEditor{}
	target := newFakeTarget("/home/me/notes.txt", "alpha")
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(editor),
		WithTempRoot(root))

	result, err := service.Open(context.Background(),
		remoteRequest("notes.txt", "alpha", target))
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == "" || len(editor.files) != 1 {
		t.Fatalf("result = %+v, editor calls = %d", result, len(editor.files))
	}
	file := editor.files[0]
	if file.Session != result.Session || file.Save == nil {
		t.Fatalf("editor file = %+v, want a session and a save callback", file)
	}

	if err := file.Save(context.Background(), []byte("alpha beta"), false); err != nil {
		t.Fatal(err)
	}
	if got := target.text(); got != "alpha beta" {
		t.Fatalf("remote content = %q", got)
	}
	// The post-save baseline is the version just written, so an unchanged
	// remote does not conflict with the user's own save.
	if err := file.Save(context.Background(), []byte("alpha beta gamma"), false); err != nil {
		t.Fatalf("the second save conflicted with our own write: %v", err)
	}
	if got := target.text(); got != "alpha beta gamma" {
		t.Fatalf("remote content after the second save = %q", got)
	}

	tempDir := filepath.Dir(result.LocalPath)
	if _, err := os.Stat(tempDir); err != nil {
		t.Fatalf("the materialized temp directory is missing: %v", err)
	}
	service.Close(result.Session)
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("closing the session left the temp directory: %v", err)
	}
}

func TestInternalRemoteSaveRefusesToReplaceAChangedFile(t *testing.T) {
	editor := &fakeEditor{}
	target := newFakeTarget("/home/me/notes.txt", "alpha")
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(editor),
		WithTempRoot(t.TempDir()))
	result, err := service.Open(context.Background(),
		remoteRequest("notes.txt", "alpha", target))
	if err != nil {
		t.Fatal(err)
	}
	// A same-size edit with the same modification time is still a change; the
	// content digest catches what the mtime cannot.
	target.changeRemote("beta!")

	err = service.Save(context.Background(), result.Session, []byte("mine"), false)
	kind, ok := KindOf(err)
	if !ok || kind != ErrorConflict {
		t.Fatalf("error = %v, kind = %q; want the conflict kind", err, kind)
	}
	conflict, ok := ConflictOf(err)
	if !ok || conflict.Name != "notes.txt" || conflict.Path != "/home/me/notes.txt" {
		t.Fatalf("conflict = %+v, %v", conflict, ok)
	}
	if got := target.text(); got != "beta!" {
		t.Fatalf("the refused save wrote %q", got)
	}

	if err := service.Save(context.Background(), result.Session, []byte("mine"), true); err != nil {
		t.Fatalf("the approved overwrite failed: %v", err)
	}
	if got := target.text(); got != "mine" {
		t.Fatalf("overwrite wrote %q", got)
	}
	service.Close(result.Session)
}

func TestSaveFailureKeepsTheSessionForARetry(t *testing.T) {
	editor := &fakeEditor{}
	target := newFakeTarget("/home/me/notes.txt", "alpha")
	target.setFailWrite(true)
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(editor),
		WithTempRoot(t.TempDir()))
	result, err := service.Open(context.Background(),
		remoteRequest("notes.txt", "alpha", target))
	if err != nil {
		t.Fatal(err)
	}
	err = service.Save(context.Background(), result.Session, []byte("my buffer"), false)
	if kind, ok := KindOf(err); !ok || kind != ErrorSave {
		t.Fatalf("error = %v, kind = %q; want the save kind", err, kind)
	}
	if got := target.text(); got != "alpha" {
		t.Fatalf("a failed save wrote %q", got)
	}

	target.setFailWrite(false)
	if err := service.Save(context.Background(), result.Session, []byte("my buffer"), false); err != nil {
		t.Fatalf("the retry failed after the connection returned: %v", err)
	}
	if got := target.text(); got != "my buffer" {
		t.Fatalf("the retry wrote %q", got)
	}
	service.Close(result.Session)
}

func TestOversizedInternalFileIsRefusedBeforeDownloading(t *testing.T) {
	root := t.TempDir()
	editor := &fakeEditor{}
	sourceCalled := false
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(editor),
		WithTempRoot(root))
	_, err := service.Open(context.Background(), Request{
		Name: "huge.txt", Remote: true, Size: MaxEditorBytes + 1,
		Source: func(context.Context) (io.ReadCloser, error) {
			sourceCalled = true
			return io.NopCloser(strings.NewReader("x")), nil
		},
	})
	if kind, ok := KindOf(err); !ok || kind != ErrorTooLarge {
		t.Fatalf("error = %v, kind = %q; want the too-large kind", err, kind)
	}
	if sourceCalled {
		t.Error("the oversized file was downloaded")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("the refused open left %v", entries)
	}
}

func TestLocalInternalSaveWritesTheLocalFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.txt")
	if err := os.WriteFile(path, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	editor := &fakeEditor{}
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(editor),
		WithTempRoot(t.TempDir()))
	result, err := service.Open(context.Background(), Request{
		Name: "local.txt", LocalPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Save(context.Background(), result.Session, []byte("local edit"), false); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "local edit" {
		t.Fatalf("local content = %q", content)
	}
	service.Close(result.Session)
}

func TestExternalEditUploadsAfterTheDebounce(t *testing.T) {
	root := t.TempDir()
	launcher := &fakeLauncher{}
	events := make(chan WatchEvent, 8)
	target := newFakeTarget("/home/me/watch.txt", "start")
	service := New(externalPreferences(), launcher,
		WithTempRoot(root),
		WithWatchInterval(10*time.Millisecond),
		WithWatchDebounce(80*time.Millisecond),
		WithWatchObserver(func(event WatchEvent) { events <- event }))
	result, err := service.Open(context.Background(),
		remoteRequest("watch.txt", "start", target))
	if err != nil {
		t.Fatal(err)
	}
	if result.Session == "" || len(launcher.calls) != 1 {
		t.Fatalf("result = %+v, launcher = %+v", result, launcher.calls)
	}
	// Two quick saves are one settled version; the debounce must upload the
	// final content once, not both intermediate versions.
	if err := os.WriteFile(result.LocalPath, []byte("external one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.LocalPath, []byte("external two, longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the debounced external upload", func() bool {
		return target.text() == "external two, longer"
	})
	if got := target.writeCount(); got != 1 {
		t.Errorf("uploads = %d, want the two rapid saves coalesced into one", got)
	}
	select {
	case event := <-events:
		if event.Kind != WatchSynced || event.Session != result.Session {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher did not report the sync")
	}
	service.Close(result.Session)
	if _, err := os.Stat(filepath.Dir(result.LocalPath)); !os.IsNotExist(err) {
		t.Fatalf("closing the external session left its temp directory: %v", err)
	}
}

func TestExternalConflictDoesNotOverwriteUntilApproved(t *testing.T) {
	root := t.TempDir()
	events := make(chan WatchEvent, 8)
	target := newFakeTarget("/home/me/watch.txt", "start")
	service := New(externalPreferences(), &fakeLauncher{},
		WithTempRoot(root),
		WithWatchInterval(10*time.Millisecond),
		WithWatchDebounce(20*time.Millisecond),
		WithWatchObserver(func(event WatchEvent) { events <- event }))
	result, err := service.Open(context.Background(),
		remoteRequest("watch.txt", "start", target))
	if err != nil {
		t.Fatal(err)
	}
	target.changeRemote("remote edit")
	if err := os.WriteFile(result.LocalPath, []byte("local edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	var conflict WatchEvent
	waitFor(t, "the watch conflict event", func() bool {
		select {
		case event := <-events:
			if event.Kind == WatchConflict {
				conflict = event
				return true
			}
		default:
		}
		return false
	})
	if conflict.Conflict == nil || conflict.Conflict.Name != "watch.txt" {
		t.Fatalf("conflict event = %+v", conflict)
	}
	if got := target.text(); got != "remote edit" {
		t.Fatalf("the conflicted watch wrote %q", got)
	}

	// While the conflict is pending, the watcher's settled-change path must not
	// bypass it. Driving that path directly keeps the assertion deterministic:
	// with the guard removed, this call would upload "local edit 2".
	if err := os.WriteFile(result.LocalPath, []byte("local edit 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, ok := service.session(result.Session)
	if !ok {
		t.Fatal("the editing session disappeared")
	}
	service.takeSettledChange(context.Background(), sess)
	if got := target.text(); got != "remote edit" {
		t.Fatalf("a pending conflict was bypassed: remote = %q", got)
	}

	if err := service.ResolveExternal(context.Background(), result.Session, true); err != nil {
		t.Fatal(err)
	}
	if got := target.text(); got != "local edit 2" {
		t.Fatalf("the approved overwrite wrote %q", got)
	}
	service.Close(result.Session)
}

func TestExternalLostConnectionKeepsTheLocalCopyForRetry(t *testing.T) {
	root := t.TempDir()
	events := make(chan WatchEvent, 8)
	target := newFakeTarget("/home/me/watch.txt", "start")
	target.setFailWrite(true)
	service := New(externalPreferences(), &fakeLauncher{},
		WithTempRoot(root),
		WithWatchInterval(10*time.Millisecond),
		WithWatchDebounce(20*time.Millisecond),
		WithWatchObserver(func(event WatchEvent) { events <- event }))
	result, err := service.Open(context.Background(),
		remoteRequest("watch.txt", "start", target))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.LocalPath, []byte("kept edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed upload event", func() bool {
		select {
		case event := <-events:
			return event.Kind == WatchFailed
		default:
			return false
		}
	})
	content, err := os.ReadFile(result.LocalPath)
	if err != nil || string(content) != "kept edit" {
		t.Fatalf("the local copy was lost: %q, %v", content, err)
	}
	if got := target.text(); got != "start" {
		t.Fatalf("the failed upload wrote %q", got)
	}

	target.setFailWrite(false)
	if err := service.RetryExternal(context.Background(), result.Session); err != nil {
		t.Fatal(err)
	}
	if got := target.text(); got != "kept edit" {
		t.Fatalf("the retry wrote %q", got)
	}
	service.Close(result.Session)
}

func TestSweepTempRemovesOnlyOrphans(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "natcatty-open-stale")
	unrelated := filepath.Join(root, "something-else")
	for _, dir := range []string{orphan, unrelated} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	service := New(internalPreferences(), &fakeLauncher{}, WithTempRoot(root))
	removed, err := service.SweepTemp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("the orphan was not removed: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("an unrelated directory was removed: %v", err)
	}
}
