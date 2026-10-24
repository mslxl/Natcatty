//go:build selftest

package app

// Ticket 36 self-test. Press F11, then 3, then 6. The check proves compressed
// upload, pane auto-sync, and follow-cwd through the app's own wiring:
//
//   - Compressed upload: the default-on setting reaches the copy engine, which
//     streams one gzip tar to a remote tar over an exec channel. A forced
//     extraction failure cleans its staging directory and falls back to the
//     ordinary per-file upload; the next attempt promotes the extracted tree.
//   - Auto-sync: with the setting on, a local pane and a remote pane on
//     different trees move one child or parent step together, and a pane that
//     is not at the same named level is left alone.
//   - Follow-cwd: a shell that emits OSC 7 through the real Session tap steers
//     the local pane to each reported directory.
//   - Persistence: the three settings the run used are read back by a fresh
//     preferences service over the same file, which is the restart path.
//
// The remote pane is the in-process pkg/sftp server ticket 30 uses. Its exec
// channel is a fake tar that extracts the stream the engine packs; no live
// SSH host is needed. The three settings go through the real preferences
// service, so each change is written to disk before the surface reads it.
//
// Every listing and transfer completes off the UI thread. The steps wait on
// their real state with a hard deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-36.json.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/pkg/sftp"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	adaptersstore "github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/selftest"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest36Ticket = 36

const selftest36Wait = 20 * time.Second

type selftest36 struct {
	workdir         string
	localRoot       string
	remoteRoot      string
	remoteRootPath  string
	preferencesPath string
	state           *transferstate.State
	remote          *selftest36Remote
	executor        *selftest36Executor
	preferences     *apppreferences.Service
	defaults        domainpreferences.SFTPEditor
	transport       *selftest36Transport
	session         *appsession.Session
	service         *appsession.Service
	cleanup         func()

	deadline time.Time

	compressedPhase int
	autoSyncPhase   int
	followPhase     int
	// finished marks a completed run. The registry reuses the check value, so
	// the next F11,3,6 starts from a clean instance and fresh fixtures.
	finished bool
}

func (t *selftest36) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest36Ticket,
		Name:   "Compressed upload, auto-sync, and follow-cwd",
		Steps: []selftest.Step{
			{Name: "compressed-upload-falls-back-then-succeeds", Run: t.compressedStep},
			{Name: "auto-sync-keeps-panes-aligned", Run: t.autoSyncStep},
			{Name: "follow-cwd-follows-osc7", Run: t.followStep},
			{Name: "settings-survive-a-restart", Run: t.persistenceStep},
		},
	}
}

// selftest36Remote opens an SFTP filesystem over an in-process server and
// hands the same filesystem an exec channel for tar.
type selftest36Remote struct {
	root string
	exec appconnection.Executor

	mu      sync.Mutex
	closers []io.Closer
}

func (o *selftest36Remote) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(o.root))
	if err != nil {
		_ = clientSide.Close()
		return nil, err
	}
	go func() { _ = server.Serve() }()
	options := []adaptersftp.RemoteOption{adaptersftp.WithBacking("remote:selftest")}
	if o.exec != nil {
		options = append(options, adaptersftp.WithExecutor(o.exec))
	}
	remote, err := adaptersftp.NewRemote(clientSide, func(error) error { return nil }, options...)
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	o.mu.Lock()
	o.closers = append(o.closers, remote, server)
	o.mu.Unlock()
	return remote, nil
}

func (o *selftest36Remote) Close() {
	o.mu.Lock()
	closers := o.closers
	o.closers = nil
	o.mu.Unlock()
	for _, closer := range closers {
		_ = closer.Close()
	}
}

type selftest36Hosts struct{}

func (selftest36Hosts) Hosts(context.Context) ([]apptransfer.HostTarget, error) {
	return []apptransfer.HostTarget{{ID: "selftest", Label: "Loopback", Hostname: "127.0.0.1", Username: "tester", Port: 22}}, nil
}

// selftest36Executor is an exec transport whose tar extracts the stream it is
// given. failNext makes one extraction exit non-zero after writing a partial
// file, which is the failure the upload must clean up and survive.
type selftest36Executor struct {
	mu        sync.Mutex
	opens     int
	extracted int
	failNext  bool
}

func (e *selftest36Executor) Exec(context.Context, string) (appconnection.ExecResult, error) {
	return appconnection.ExecResult{}, appconnection.ErrExecUnavailable
}

func (e *selftest36Executor) OpenExec(_ context.Context, command string) (appconnection.ExecStream, error) {
	dir, ok := selftest36TarDir(command)
	if !ok {
		return nil, fmt.Errorf("selftest: unexpected exec command %q", command)
	}
	e.mu.Lock()
	e.opens++
	fail := e.failNext
	e.failNext = false
	e.mu.Unlock()
	reader, writer := io.Pipe()
	return &selftest36Stream{
		exec:         e,
		dir:          selftest36OSPath(dir),
		fail:         fail,
		stderrReader: reader,
		stderrWriter: writer,
		done:         make(chan struct{}),
	}, nil
}

func (e *selftest36Executor) counts() (opens, extracted int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opens, e.extracted
}

func (e *selftest36Executor) noteExtracted() {
	e.mu.Lock()
	e.extracted++
	e.mu.Unlock()
}

// selftest36Stream buffers the archive on stdin and extracts it when the
// producer signals end-of-input, like tar would after seeing EOF.
type selftest36Stream struct {
	exec *selftest36Executor
	dir  string
	fail bool

	mu      sync.Mutex
	archive bytes.Buffer
	once    sync.Once
	code    int

	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter
	done         chan struct{}
}

func (s *selftest36Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archive.Write(p)
}

func (s *selftest36Stream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *selftest36Stream) Stderr() io.Reader { return s.stderrReader }

func (s *selftest36Stream) CloseWrite() error {
	s.finish(true)
	return nil
}

func (s *selftest36Stream) Close() error {
	s.finish(false)
	return nil
}

func (s *selftest36Stream) ExitCode() (int, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code, nil
}

func (s *selftest36Stream) finish(extract bool) {
	s.once.Do(func() {
		switch {
		case extract && s.fail:
			// The engine must remove this staging directory before falling
			// back, so a leftover would be visible under the destination.
			_ = os.MkdirAll(s.dir, 0o755)
			_ = os.WriteFile(filepath.Join(s.dir, "partial.tmp"), []byte("partial"), 0o644)
			s.mu.Lock()
			s.code = 2
			s.mu.Unlock()
			_, _ = io.WriteString(s.stderrWriter, "tar: selftest extraction failed")
		case extract:
			if err := s.unpack(); err != nil {
				s.mu.Lock()
				s.code = 2
				s.mu.Unlock()
				_, _ = fmt.Fprintf(s.stderrWriter, "tar: %v", err)
			} else {
				s.exec.noteExtracted()
			}
		}
		_ = s.stderrWriter.Close()
		close(s.done)
	})
}

// unpack reproduces what the remote tar does: create directories and files
// under the extraction directory in archive order.
func (s *selftest36Stream) unpack() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	s.mu.Lock()
	raw := append([]byte(nil), s.archive.Bytes()...)
	s.mu.Unlock()
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	root := filepath.Clean(s.dir)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(header.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("entry %q escapes the staging directory", header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("entry %q is not a regular file or directory", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, archive); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
}

// selftest36TarDir reads the extraction directory out of the one command the
// compressed upload issues.
func selftest36TarDir(command string) (string, bool) {
	const prefix = "tar -xzf - -C "
	if !strings.HasPrefix(command, prefix) {
		return "", false
	}
	quoted := strings.TrimPrefix(command, prefix)
	if len(quoted) < 2 || quoted[0] != '\'' || quoted[len(quoted)-1] != '\'' {
		return "", false
	}
	return strings.ReplaceAll(quoted[1:len(quoted)-1], `'\''`, "'"), true
}

// selftest36OSPath turns the server's POSIX view of a local path back into an
// OS path, mirroring pkg/sftp's Windows bridge ("/C:/dir" to "C:\dir").
func selftest36OSPath(p string) string {
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// selftest36Transport stands in for a shell. Output written through send is
// read by the real Session tap, so OSC 7 parsing is exercised.
type selftest36Transport struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
}

func newSelftest36Transport() *selftest36Transport {
	reader, writer := io.Pipe()
	return &selftest36Transport{reader: reader, writer: writer}
}

func (t *selftest36Transport) Read(p []byte) (int, error) { return t.reader.Read(p) }
func (t *selftest36Transport) Write(p []byte) (int, error) {
	return len(p), nil
}
func (t *selftest36Transport) Resize(int, int) error { return nil }
func (t *selftest36Transport) Close() error {
	t.once.Do(func() {
		_ = t.writer.Close()
		_ = t.reader.Close()
	})
	return nil
}
func (t *selftest36Transport) send(value string) {
	_, _ = io.WriteString(t.writer, value)
}

type selftest36Factory struct {
	transport *selftest36Transport
}

func (f *selftest36Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func (t *selftest36) setup() error {
	workdir, err := os.MkdirTemp("", "natcatty-selftest-36-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	t.localRoot = filepath.Join(workdir, "local")
	t.remoteRoot = filepath.Join(workdir, "remote")
	t.preferencesPath = filepath.Join(workdir, "preferences.json")
	for _, dir := range []string{
		t.localRoot,
		t.remoteRoot,
		filepath.Join(t.localRoot, "upload", "sub"),
		filepath.Join(t.localRoot, "upload2"),
		filepath.Join(t.localRoot, "work", "alpha"),
		filepath.Join(t.localRoot, "followed"),
		filepath.Join(t.remoteRoot, "work", "alpha"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for path, content := range map[string]string{
		filepath.Join(t.localRoot, "upload", "one.txt"):        "one",
		filepath.Join(t.localRoot, "upload", "sub", "two.bin"): "two",
		filepath.Join(t.localRoot, "upload2", "three.txt"):     "three",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}

	t.executor = &selftest36Executor{failNext: true}
	t.remote = &selftest36Remote{root: t.remoteRoot, exec: t.executor}
	service := apptransfer.NewService(
		adaptersftp.NewLocal(),
		t.remote,
		apptransfer.WithHosts(selftest36Hosts{}),
		apptransfer.WithBookmarks(adaptersftp.NewBookmarks(
			filepath.Join(workdir, "sftp-bookmarks.json"),
			filepath.Join(workdir, "sftp-local-bookmarks.json"),
		)),
	)
	// The three settings go through the real persisted preferences service,
	// exactly as the app wires them into the surface.
	t.preferences = apppreferences.NewService(adaptersstore.OpenPreferences(t.preferencesPath))
	if err := t.preferences.Load(context.Background()); err != nil {
		return err
	}
	t.defaults = t.preferences.Settings().SFTPEditor
	t.state = transferstate.NewState(service,
		transferstate.WithPreferences(func() domainpreferences.SFTPEditor {
			return t.preferences.Settings().SFTPEditor
		}),
	)
	t.deadline = time.Now().Add(selftest36Wait)
	t.cleanup = func() {
		t.stopSession()
		if t.state != nil {
			t.state.Shutdown()
		}
		t.remote.Close()
		_ = os.RemoveAll(t.workdir)
	}
	return nil
}

func (t *selftest36) stopSession() {
	if t.session != nil {
		_ = t.session.Close()
		t.session = nil
	}
	if t.transport != nil {
		t.transport.Close()
		t.transport = nil
	}
	t.service = nil
}

// flush applies completed listing work on the UI thread. Every step calls it
// first; a phase that only observes state would otherwise never see a load
// finish.
func (t *selftest36) flush() {
	if t.state != nil {
		t.state.Flush()
	}
}

func (t *selftest36) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not finish within %s", what, selftest36Wait))
	}
	return selftest.Pending
}

// settled reports whether a pane finished its listing.
func (t *selftest36) settled(side transferstate.Side) bool {
	tab := t.state.Tab(side)
	return tab != nil && tab.FS != nil && !tab.Loading && tab.Path != ""
}

func selftest36SamePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

// selftest36Describe reports a pane's observable state for a timeout message.
func selftest36Describe(tab *transferstate.Tab) string {
	if tab == nil {
		return "(no tab)"
	}
	return fmt.Sprintf("(path=%q loading=%v entries=%d problem=%v)", tab.Path, tab.Loading, len(tab.Entries), tab.Problem)
}

// selectEntry selects one row of a pane by name.
func (t *selftest36) selectEntry(side transferstate.Side, name string) error {
	tab := t.state.Tab(side)
	if tab == nil {
		return fmt.Errorf("the pane is not open")
	}
	for _, entry := range tab.Entries {
		if entry.Name != name {
			continue
		}
		t.state.Focus(side)
		t.state.Click(side, entry, 0, false)
		return nil
	}
	return fmt.Errorf("the pane has no %q", name)
}

func (t *selftest36) compressedStep(*ui.Context) selftest.Outcome {
	if t.finished {
		*t = selftest36{}
	}
	t.flush()
	switch t.compressedPhase {
	case 0:
		if err := t.setup(); err != nil {
			return selftest.Failed(fmt.Errorf("the compressed-upload fixtures were not created: %v", err))
		}
		if !t.defaults.UseCompressedUpload {
			return selftest.Failed(fmt.Errorf("compressed upload is not on by default: %+v", t.defaults))
		}
		t.state.EnsureStarted()
		t.compressedPhase = 1
		return selftest.Pending
	case 1:
		t.state.Flush()
		left := t.state.Tab(transferstate.Left)
		if left == nil || left.FS == nil {
			return t.expired("the local pane")
		}
		if !left.Loading && !selftest36SamePath(left.Path, t.localRoot) {
			t.state.Navigate(transferstate.Left, t.localRoot)
			t.compressedPhase = 2
			return selftest.Pending
		}
		if left.Loading || !selftest36SamePath(left.Path, t.localRoot) {
			return t.expired("the local fixture listing")
		}
		t.state.OpenRemote(transferstate.Right, "selftest", "Loopback")
		t.compressedPhase = 3
		return selftest.Pending
	case 2:
		if !t.settled(transferstate.Left) || !selftest36SamePath(t.state.Tab(transferstate.Left).Path, t.localRoot) {
			return t.expired("the local fixture listing " + selftest36Describe(t.state.Tab(transferstate.Left)))
		}
		t.state.OpenRemote(transferstate.Right, "selftest", "Loopback")
		t.compressedPhase = 3
		return selftest.Pending
	case 3:
		if !t.settled(transferstate.Right) {
			return t.expired("the remote pane")
		}
		right := t.state.Tab(transferstate.Right)
		// The in-process server reports its working directory in POSIX form
		// ("/C:/..." on Windows). Keep it so every expected remote path is
		// built by the same adapter.
		t.remoteRootPath = right.Path
		if !t.settled(transferstate.Left) {
			return t.expired("the local fixture listing")
		}
		if err := t.selectEntry(transferstate.Left, "upload"); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.compressedPhase = 4
		return selftest.Pending
	case 4:
		t.state.Flush()
		if t.state.Busy() {
			return t.expired("the upload whose extraction fails")
		}
		opens, extracted := t.executor.counts()
		if opens != 1 || extracted != 0 {
			return selftest.Failed(fmt.Errorf("the failed extraction opened %d archives and extracted %d, want 1 and 0", opens, extracted))
		}
		if err := t.checkUploaded("upload", map[string]string{
			"one.txt":                       "one",
			filepath.Join("sub", "two.bin"): "two",
		}); err != nil {
			return selftest.Failed(fmt.Errorf("the per-file fallback %v", err))
		}
		if staging, _ := filepath.Glob(filepath.Join(t.remoteRoot, ".netcatty-extract-*")); len(staging) != 0 {
			return selftest.Failed(fmt.Errorf("the failed extraction left staging directories: %v", staging))
		}
		if _, err := os.Stat(filepath.Join(t.remoteRoot, "upload", "partial.tmp")); err == nil {
			return selftest.Failed(fmt.Errorf("the failed extraction's partial file was published"))
		}
		if !t.settled(transferstate.Left) {
			return t.expired("the local listing after the fallback")
		}
		if err := t.selectEntry(transferstate.Left, "upload2"); err != nil {
			return selftest.Failed(err)
		}
		t.state.Transfer(transferstate.Left)
		t.compressedPhase = 5
		return selftest.Pending
	}

	t.state.Flush()
	if t.state.Busy() {
		return t.expired("the compressed upload")
	}
	opens, extracted := t.executor.counts()
	if opens != 2 || extracted != 1 {
		return selftest.Failed(fmt.Errorf("the successful upload opened %d archives and extracted %d, want 2 and 1", opens, extracted))
	}
	if err := t.checkUploaded("upload2", map[string]string{"three.txt": "three"}); err != nil {
		return selftest.Failed(fmt.Errorf("the compressed upload %v", err))
	}
	if staging, _ := filepath.Glob(filepath.Join(t.remoteRoot, ".netcatty-extract-*")); len(staging) != 0 {
		return selftest.Failed(fmt.Errorf("the compressed upload left staging directories: %v", staging))
	}
	return selftest.Passed(
		"a forced tar failure was cleaned up and fell back to the per-file upload, then the next folder was promoted from one extracted archive",
		"transferstate.State -> apptransfer.copyCompressed -> adaptersftp.Remote.ExtractTar (fake exec tar)")
}

// checkUploaded reads each expected file under the remote root.
func (t *selftest36) checkUploaded(dir string, files map[string]string) error {
	for name, want := range files {
		path := filepath.Join(t.remoteRoot, dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("did not write %s: %v", path, err)
		}
		if string(got) != want {
			return fmt.Errorf("wrote %q to %s, want %q", got, path, want)
		}
	}
	return nil
}

func (t *selftest36) autoSyncStep(*ui.Context) selftest.Outcome {
	t.flush()
	switch t.autoSyncPhase {
	case 0:
		if err := t.preferences.Update(func(settings *domainpreferences.Settings) {
			settings.SFTPEditor.AutoSync = true
		}); err != nil {
			return selftest.Failed(fmt.Errorf("auto-sync was not persisted: %v", err))
		}
		right := t.state.Tab(transferstate.Right)
		if right == nil || right.FS == nil {
			return selftest.Failed(fmt.Errorf("the remote pane closed before auto-sync"))
		}
		t.state.Navigate(transferstate.Right, right.FS.Join(t.remoteRootPath, "work"))
		t.autoSyncPhase = 1
		return selftest.Pending
	case 1:
		if !t.settled(transferstate.Right) || !t.remoteAt("work") {
			return t.expired("the remote pane to enter work")
		}
		t.state.Navigate(transferstate.Left, filepath.Join(t.localRoot, "work"))
		t.autoSyncPhase = 2
		return selftest.Pending
	case 2:
		if !t.settled(transferstate.Left) || !selftest36SamePath(t.state.Tab(transferstate.Left).Path, filepath.Join(t.localRoot, "work")) {
			return t.expired("the local pane to enter work")
		}
		// Both panes now name the same level, so a child step on one side is
		// a child step on the other. An unaligned pane would not move.
		t.state.Navigate(transferstate.Left, filepath.Join(t.localRoot, "work", "alpha"))
		t.autoSyncPhase = 3
		return selftest.Pending
	case 3:
		if !t.settled(transferstate.Left) || !selftest36SamePath(t.state.Tab(transferstate.Left).Path, filepath.Join(t.localRoot, "work", "alpha")) {
			return t.expired("the local pane to enter alpha")
		}
		if !t.settled(transferstate.Right) || !t.remoteAt("work", "alpha") {
			return t.expired("the remote pane to follow alpha")
		}
		t.state.GoParent(transferstate.Left)
		t.autoSyncPhase = 4
		return selftest.Pending
	}

	if !t.settled(transferstate.Left) || !selftest36SamePath(t.state.Tab(transferstate.Left).Path, filepath.Join(t.localRoot, "work")) {
		return t.expired("the local pane to go up")
	}
	if !t.settled(transferstate.Right) || !t.remoteAt("work") {
		return t.expired("the remote pane to go up")
	}
	return selftest.Passed(
		"aligned panes moved into alpha together and back up together; the earlier unaligned navigation moved only its own pane",
		"transferstate.State.autoSyncTarget (local tree + remote tree)")
}

// remoteAt reports whether the remote pane is at the named path under its root.
func (t *selftest36) remoteAt(parts ...string) bool {
	tab := t.state.Tab(transferstate.Right)
	if tab == nil || tab.FS == nil {
		return false
	}
	want := tab.FS.Join(append([]string{t.remoteRootPath}, parts...)...)
	return tab.FS.Canonical(tab.Path) == tab.FS.Canonical(want)
}

func (t *selftest36) followStep(*ui.Context) selftest.Outcome {
	t.flush()
	switch t.followPhase {
	case 0:
		if err := t.preferences.Update(func(settings *domainpreferences.Settings) {
			settings.SFTPEditor.FollowTerminalCwd = true
		}); err != nil {
			return selftest.Failed(fmt.Errorf("follow-cwd was not persisted: %v", err))
		}
		t.transport = newSelftest36Transport()
		var sessions *appsession.Service
		sessions = appsession.NewService(&selftest36Factory{transport: t.transport},
			appsession.WithChangeHook(func(snapshot appsession.Snapshot) {
				forwardTerminalCwd(sessions, t.state, snapshot)
			}),
		)
		t.service = sessions
		session, err := sessions.OpenLocal(context.Background())
		if err != nil {
			return selftest.Failed(fmt.Errorf("a local Session did not open: %v", err))
		}
		t.session = session
		t.transport.send(selftest36OSC7(filepath.Join(t.localRoot, "followed")))
		t.followPhase = 1
		return selftest.Pending
	case 1:
		if !t.settled(transferstate.Left) ||
			!selftest36SamePath(t.state.Tab(transferstate.Left).Path, filepath.Join(t.localRoot, "followed")) {
			return t.expired("the local pane to follow the first OSC 7")
		}
		t.transport.send(selftest36OSC7(filepath.Join(t.localRoot, "work", "alpha")))
		t.followPhase = 2
		return selftest.Pending
	}

	if !t.settled(transferstate.Left) ||
		!selftest36SamePath(t.state.Tab(transferstate.Left).Path, filepath.Join(t.localRoot, "work", "alpha")) {
		return t.expired("the local pane to follow the second OSC 7")
	}
	if tab := t.state.Tab(transferstate.Right); tab == nil || !t.remoteAt("work") {
		return selftest.Failed(fmt.Errorf("follow-cwd disturbed the auto-synced remote pane"))
	}
	t.stopSession()
	return selftest.Passed(
		"the real Session tap parsed two OSC 7 reports and the local pane entered each directory while the remote pane stayed put",
		"appsession tap (OSC 7) -> app.forwardTerminalCwd -> transferstate.NoteTerminalCwd")
}

// selftest36OSC7 builds the file URL a shell emits, with the drive form the
// parser expects on Windows.
func selftest36OSC7(path string) string {
	slash := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(slash) >= 2 && slash[1] == ':' {
		slash = "/" + slash
	}
	return "\x1b]7;file://selftest" + slash + "\x07"
}

func (t *selftest36) persistenceStep(*ui.Context) selftest.Outcome {
	if !t.defaults.UseCompressedUpload || t.defaults.AutoSync || t.defaults.FollowTerminalCwd {
		return selftest.Failed(fmt.Errorf("the defaults before the run = %+v", t.defaults))
	}
	// A fresh service over the same file is the restart path: it has no
	// in-memory state from this run.
	restarted := apppreferences.NewService(adaptersstore.OpenPreferences(t.preferencesPath))
	if err := restarted.Load(context.Background()); err != nil {
		return selftest.Failed(fmt.Errorf("the preferences file did not load after a restart: %v", err))
	}
	got := restarted.Settings().SFTPEditor
	if !got.UseCompressedUpload || !got.AutoSync || !got.FollowTerminalCwd {
		return selftest.Failed(fmt.Errorf("settings after a restart = %+v, want all three on", got))
	}
	if t.cleanup != nil {
		t.cleanup()
		t.cleanup = nil
	}
	t.finished = true
	return selftest.Passed(
		"compressed upload stayed on and the auto-sync and follow-cwd changes were read back by a new preferences service",
		"apppreferences.Service + adapters/store.PreferencesStore (atomic JSON)")
}
