package fileopen

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
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

// sharedRemote is the file behind two transports. A lost connection replaces
// the transport but not the remote file, so a re-acquired target still sees
// the version the session read.
type sharedRemote struct {
	mu       sync.Mutex
	content  []byte
	modified time.Time
	writes   int
}

func (r *sharedRemote) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.content)
}

func (r *sharedRemote) write(content []byte) {
	r.mu.Lock()
	r.content = append([]byte(nil), content...)
	r.writes++
	r.mu.Unlock()
}

func (r *sharedRemote) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

// lostTransport is one transport for a sharedRemote. Killing it models the
// sshd and its sftp-server going away: the handle stays dead even after the
// server is restarted, so a retry must not reuse it. Reconnect hands out the
// successor the app acquires for the same Host.
type lostTransport struct {
	remote *sharedRemote
	path   string

	mu         sync.Mutex
	dead       bool
	reconnects int
	successor  *lostTransport
	dialErr    error
}

func (t *lostTransport) Path() string { return t.path }

func (t *lostTransport) fail() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dead {
		return errors.New("connection reset by peer")
	}
	return nil
}

func (t *lostTransport) Stat(context.Context) (RemoteStat, error) {
	if err := t.fail(); err != nil {
		return RemoteStat{}, err
	}
	t.remote.mu.Lock()
	defer t.remote.mu.Unlock()
	return RemoteStat{
		Size: int64(len(t.remote.content)), Modified: t.remote.modified,
	}, nil
}

func (t *lostTransport) Read(context.Context) (io.ReadCloser, error) {
	if err := t.fail(); err != nil {
		return nil, err
	}
	t.remote.mu.Lock()
	defer t.remote.mu.Unlock()
	return io.NopCloser(bytes.NewReader(append([]byte(nil), t.remote.content...))), nil
}

func (t *lostTransport) Write(_ context.Context, content []byte) error {
	if err := t.fail(); err != nil {
		return err
	}
	t.remote.write(content)
	return nil
}

// Reconnect re-acquires the Host's transport. A dial failure is reported as
// such; a successful dial returns the new transport for the same file.
func (t *lostTransport) Reconnect(context.Context) (RemoteTarget, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reconnects++
	if t.dialErr != nil {
		return nil, t.dialErr
	}
	if t.successor == nil {
		return nil, errors.New("ssh: connect: connection refused")
	}
	return t.successor, nil
}

func (t *lostTransport) kill() {
	t.mu.Lock()
	t.dead = true
	t.mu.Unlock()
}

func (t *lostTransport) reconnectCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reconnects
}

// TransportLost models the pane target's classification: a permission
// refusal is the remote's own answer, everything else is a lost connection.
func (t *lostTransport) TransportLost(cause error) bool {
	return !errors.Is(cause, fs.ErrPermission)
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

// A retry after a real transport loss must acquire a fresh transport for the
// same Host and upload through it. Reusing the dead handle is the defect the
// independent GUI acceptance found: the prompt stayed and the remote did not
// change although sshd was listening again.
func TestExternalRetryReacquiresTheTransportAfterALoss(t *testing.T) {
	root := t.TempDir()
	events := make(chan WatchEvent, 8)
	remote := &sharedRemote{content: []byte("start"), modified: time.Unix(1000, 0)}
	first := &lostTransport{remote: remote, path: "/home/me/watch.txt"}
	second := &lostTransport{remote: remote, path: "/home/me/watch.txt"}
	first.successor = second
	service := New(externalPreferences(), &fakeLauncher{},
		WithTempRoot(root),
		WithWatchInterval(10*time.Millisecond),
		WithWatchDebounce(20*time.Millisecond),
		WithWatchObserver(func(event WatchEvent) { events <- event }))
	result, err := service.Open(context.Background(),
		remoteRequest("watch.txt", "start", first))
	if err != nil {
		t.Fatal(err)
	}
	first.kill()
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
	if got := remote.text(); got != "start" {
		t.Fatalf("the failed upload wrote %q through the dead transport", got)
	}
	if first.reconnectCount() != 0 {
		t.Fatalf("the background watcher dialed %d times; only an explicit retry may reconnect",
			first.reconnectCount())
	}

	// sshd is back on the same port: a fresh transport can be acquired.
	if err := service.RetryExternal(context.Background(), result.Session); err != nil {
		t.Fatalf("the retry after the transport returned failed: %v", err)
	}
	if first.reconnectCount() != 1 {
		t.Fatalf("the retry acquired a transport %d times, want 1", first.reconnectCount())
	}
	if got := remote.text(); got != "kept edit" {
		t.Fatalf("the retry wrote %q", got)
	}
	if got := remote.writeCount(); got != 1 {
		t.Fatalf("remote writes = %d, want 1 through the re-acquired transport", got)
	}
	// The fresh target is the session's target now, so a later save uses it.
	if err := os.WriteFile(result.LocalPath, []byte("after reconnect"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the upload after the reconnect", func() bool {
		return remote.text() == "after reconnect"
	})
	if first.reconnectCount() != 1 {
		t.Fatalf("a later save dialed again: reconnects = %d", first.reconnectCount())
	}
	service.Close(result.Session)
}

// A failed retry (the server is still down) keeps the local copy and reports
// the failure; it must not write through the dead transport.
func TestRetryThatCannotReacquireKeepsTheFailure(t *testing.T) {
	root := t.TempDir()
	events := make(chan WatchEvent, 8)
	remote := &sharedRemote{content: []byte("start"), modified: time.Unix(1000, 0)}
	first := &lostTransport{remote: remote, path: "/home/me/watch.txt"}
	first.dialErr = errors.New("ssh: connect: connection refused")
	service := New(externalPreferences(), &fakeLauncher{},
		WithTempRoot(root),
		WithWatchInterval(10*time.Millisecond),
		WithWatchDebounce(20*time.Millisecond),
		WithWatchObserver(func(event WatchEvent) { events <- event }))
	result, err := service.Open(context.Background(),
		remoteRequest("watch.txt", "start", first))
	if err != nil {
		t.Fatal(err)
	}
	first.kill()
	if err := os.WriteFile(result.LocalPath, []byte("still local"), 0o600); err != nil {
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
	kind, ok := KindOf(service.RetryExternal(context.Background(), result.Session))
	if !ok || kind != ErrorSave {
		t.Fatalf("the failed retry kind = %q, %v; want the save kind", kind, ok)
	}
	if got := remote.text(); got != "start" {
		t.Fatalf("the failed retry wrote %q", got)
	}
	if content, err := os.ReadFile(result.LocalPath); err != nil || string(content) != "still local" {
		t.Fatalf("the local copy after the failed retry = %q, %v", content, err)
	}
	fresh := &lostTransport{remote: remote, path: "/home/me/watch.txt"}
	first.mu.Lock()
	first.dialErr = nil
	first.successor = fresh
	first.mu.Unlock()
	if err := service.RetryExternal(context.Background(), result.Session); err != nil {
		t.Fatalf("the second retry failed: %v", err)
	}
	if got := remote.text(); got != "still local" {
		t.Fatalf("the second retry wrote %q", got)
	}
	service.Close(result.Session)
}

// The internal editor's Save is also an explicit retry: after the transport
// dies, the next Save re-acquires the Host rather than reusing the dead
// handle, and a remote change during the outage is still guarded.
func TestInternalSaveReacquiresTheTransportAfterALoss(t *testing.T) {
	editor := &fakeEditor{}
	remote := &sharedRemote{content: []byte("alpha"), modified: time.Unix(1000, 0)}
	first := &lostTransport{remote: remote, path: "/home/me/notes.txt"}
	second := &lostTransport{remote: remote, path: "/home/me/notes.txt"}
	first.successor = second
	first.dialErr = errors.New("ssh: connect: connection refused")
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(editor),
		WithTempRoot(t.TempDir()))
	result, err := service.Open(context.Background(),
		remoteRequest("notes.txt", "alpha", first))
	if err != nil {
		t.Fatal(err)
	}
	first.kill()
	err = service.Save(context.Background(), result.Session, []byte("my buffer"), false)
	if kind, ok := KindOf(err); !ok || kind != ErrorSave {
		t.Fatalf("error = %v, kind = %q; want the save kind", err, kind)
	}
	if first.reconnectCount() != 1 {
		t.Fatalf("the failed save reconnected %d times, want 1", first.reconnectCount())
	}
	// The first re-acquisition also failed, so nothing is written yet.
	if got := remote.text(); got != "alpha" {
		t.Fatalf("the failed save wrote %q", got)
	}

	// Once sshd is back, the next Save (the user's retry) uploads through a
	// freshly acquired transport.
	first.mu.Lock()
	first.dialErr = nil
	first.mu.Unlock()
	if err := service.Save(context.Background(), result.Session, []byte("my buffer"), false); err != nil {
		t.Fatalf("the save retry after the transport returned failed: %v", err)
	}
	if got := remote.text(); got != "my buffer" {
		t.Fatalf("the save retry wrote %q", got)
	}
	service.Close(result.Session)
}

// writeRefusal keeps a live transport but fails the write with the remote's
// own answer, standing in for a permission or path refusal.
type writeRefusal struct {
	*lostTransport
	cause error
}

func (t writeRefusal) Write(context.Context, []byte) error { return t.cause }

// A save that failed for a reason a new connection cannot change must not
// dial the Host: the user sees the remote's answer, not a login prompt.
func TestSaveDoesNotDialForANonTransportFailure(t *testing.T) {
	remote := &sharedRemote{content: []byte("alpha"), modified: time.Unix(1000, 0)}
	first := &lostTransport{remote: remote, path: "/home/me/notes.txt"}
	service := New(internalPreferences(), &fakeLauncher{}, WithEditor(&fakeEditor{}),
		WithTempRoot(t.TempDir()))
	result, err := service.Open(context.Background(),
		remoteRequest("notes.txt", "alpha", writeRefusal{lostTransport: first, cause: fs.ErrPermission}))
	if err != nil {
		t.Fatal(err)
	}
	err = service.Save(context.Background(), result.Session, []byte("my buffer"), false)
	if kind, ok := KindOf(err); !ok || kind != ErrorSave {
		t.Fatalf("error = %v, kind = %q; want the save kind", err, kind)
	}
	if first.reconnectCount() != 0 {
		t.Fatalf("a permission failure reconnected %d times, want 0", first.reconnectCount())
	}
	if got := remote.text(); got != "alpha" {
		t.Fatalf("the refused save wrote %q", got)
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
