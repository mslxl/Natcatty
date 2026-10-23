package state

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// The tests drive the real surface state over an in-memory filesystem. Both
// panes and the copy engine go through the same application service the app
// wires, so the listing, selection, dialog, and operation paths are exercised
// end to end without an SSH server or the user's disk.

const waitTimeout = 5 * time.Second

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", waitTimeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// settle flushes goroutine outcomes until cond holds, so the condition sees
// the same state the view would render.
func settle(t *testing.T, s *State, what string, cond func() bool) {
	t.Helper()
	waitFor(t, what, func() bool {
		s.Flush()
		return cond()
	})
}

type fakeNode struct {
	dir  bool
	data []byte
	mode uint32
	mod  time.Time
}

// fakeDisk is the shared tree behind every handle. Handles differ only in
// their root, which lets an opener hand out independent connections.
type fakeDisk struct {
	mu    sync.Mutex
	nodes map[string]*fakeNode
}

func newFakeDisk() *fakeDisk {
	return &fakeDisk{nodes: map[string]*fakeNode{
		"/": {dir: true, mode: 0o755},
	}}
}

func (d *fakeDisk) mkdirs(dirs ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dir := range dirs {
		dir = path.Clean(dir)
		d.nodes[dir] = &fakeNode{dir: true, mode: 0o755}
	}
}

func (d *fakeDisk) put(file, content string, mode uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nodes[path.Clean(file)] = &fakeNode{data: []byte(content), mode: mode}
}

func (d *fakeDisk) has(file string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.nodes[path.Clean(file)]
	return ok
}

func (d *fakeDisk) isDir(file string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	node, ok := d.nodes[path.Clean(file)]
	return ok && node.dir
}

func (d *fakeDisk) read(file string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	node, ok := d.nodes[path.Clean(file)]
	if !ok || node.dir {
		return "", false
	}
	return string(node.data), true
}

func (d *fakeDisk) permission(file string) (uint32, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	node, ok := d.nodes[path.Clean(file)]
	if !ok {
		return 0, false
	}
	return node.mode, true
}

type fakeFS struct {
	disk    *fakeDisk
	root    string
	backing string

	mu     sync.Mutex
	closed bool
}

func (f *fakeFS) Backing() string {
	if f.backing != "" {
		return f.backing
	}
	return "fake"
}

func (f *fakeFS) Canonical(file string) string { return path.Clean(file) }

func (f *fakeFS) Root(context.Context) (string, error) { return f.root, nil }

func (f *fakeFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
	dir = path.Clean(dir)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	node, ok := f.disk.nodes[dir]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if !node.dir {
		return nil, errors.New("not a directory")
	}
	prefix := dir
	if prefix != "/" {
		prefix += "/"
	}
	var entries []domainsftp.Entry
	for name, child := range f.disk.nodes {
		if name == dir || !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		entry := domainsftp.Entry{
			Name:    rest,
			Path:    name,
			Mode:    child.mode,
			Size:    int64(len(child.data)),
			ModTime: child.mod,
		}
		if child.dir {
			entry.Kind = domainsftp.KindDir
		} else {
			entry.Kind = domainsftp.KindFile
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (f *fakeFS) Stat(_ context.Context, file string) (domainsftp.Entry, error) {
	file = path.Clean(file)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	node, ok := f.disk.nodes[file]
	if !ok {
		return domainsftp.Entry{}, fs.ErrNotExist
	}
	entry := domainsftp.Entry{
		Name:    path.Base(file),
		Path:    file,
		Mode:    node.mode,
		Size:    int64(len(node.data)),
		ModTime: node.mod,
	}
	if node.dir {
		entry.Kind = domainsftp.KindDir
	} else {
		entry.Kind = domainsftp.KindFile
	}
	return entry, nil
}

func (f *fakeFS) Mkdir(_ context.Context, dir string) error {
	dir = path.Clean(dir)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	if _, ok := f.disk.nodes[dir]; ok {
		return fs.ErrExist
	}
	f.disk.nodes[dir] = &fakeNode{dir: true, mode: 0o755}
	return nil
}

func (f *fakeFS) MkdirAll(_ context.Context, dir string) error {
	dir = path.Clean(dir)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	for current := dir; current != "/" && current != "."; current = path.Dir(current) {
		f.disk.nodes[current] = &fakeNode{dir: true, mode: 0o755}
	}
	f.disk.nodes["/"] = &fakeNode{dir: true, mode: 0o755}
	return nil
}

func (f *fakeFS) Open(_ context.Context, file string) (io.ReadCloser, error) {
	file = path.Clean(file)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	node, ok := f.disk.nodes[file]
	if !ok || node.dir {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), node.data...))), nil
}

func (f *fakeFS) Create(_ context.Context, file string) (io.WriteCloser, error) {
	return &fakeWriter{files: f, file: path.Clean(file)}, nil
}

func (f *fakeFS) Remove(_ context.Context, file string) error {
	file = path.Clean(file)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	node, ok := f.disk.nodes[file]
	if !ok {
		return fs.ErrNotExist
	}
	if node.dir {
		prefix := file + "/"
		for name := range f.disk.nodes {
			if strings.HasPrefix(name, prefix) {
				return errors.New("directory is not empty")
			}
		}
	}
	delete(f.disk.nodes, file)
	return nil
}

func (f *fakeFS) RemoveAll(_ context.Context, file string) error {
	file = path.Clean(file)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	prefix := file + "/"
	for name := range f.disk.nodes {
		if name == file || strings.HasPrefix(name, prefix) {
			delete(f.disk.nodes, name)
		}
	}
	return nil
}

func (f *fakeFS) Rename(_ context.Context, oldPath, newPath string) error {
	oldPath, newPath = path.Clean(oldPath), path.Clean(newPath)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	node, ok := f.disk.nodes[oldPath]
	if !ok {
		return fs.ErrNotExist
	}
	if _, exists := f.disk.nodes[newPath]; exists {
		return fs.ErrExist
	}
	delete(f.disk.nodes, oldPath)
	f.disk.nodes[newPath] = node
	if node.dir {
		prefix := oldPath + "/"
		for name, child := range f.disk.nodes {
			if strings.HasPrefix(name, prefix) {
				delete(f.disk.nodes, name)
				f.disk.nodes[newPath+strings.TrimPrefix(name, oldPath)] = child
			}
		}
	}
	return nil
}

func (f *fakeFS) Chmod(_ context.Context, file string, mode uint32) error {
	file = path.Clean(file)
	f.disk.mu.Lock()
	defer f.disk.mu.Unlock()
	node, ok := f.disk.nodes[file]
	if !ok {
		return fs.ErrNotExist
	}
	node.mode = mode
	return nil
}

func (f *fakeFS) Join(parts ...string) string {
	if len(parts) == 0 {
		return f.root
	}
	return path.Join(parts...)
}

func (f *fakeFS) Parent(file string) string { return path.Dir(path.Clean(file)) }
func (f *fakeFS) Base(file string) string   { return path.Base(path.Clean(file)) }
func (f *fakeFS) Style() apptransfer.PathStyle {
	return apptransfer.StylePOSIX
}

func (f *fakeFS) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeFS) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type fakeWriter struct {
	files *fakeFS
	file  string
	buf   bytes.Buffer
}

func (w *fakeWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *fakeWriter) Close() error {
	w.files.disk.mu.Lock()
	defer w.files.disk.mu.Unlock()
	w.files.disk.nodes[w.file] = &fakeNode{data: append([]byte(nil), w.buf.Bytes()...), mode: 0o644}
	return nil
}

// opener hands out handles over one disk and remembers them so a test can
// assert that closing a Tab closes its transport.
type opener struct {
	disk    *fakeDisk
	root    string
	remote  bool
	backing string
	err     error

	mu      sync.Mutex
	handles []*fakeFS
}

func (o *opener) open() (apptransfer.FileSystem, error) {
	if o.err != nil {
		return nil, o.err
	}
	backing := o.backing
	if backing == "" {
		if o.remote {
			backing = "remote"
		} else {
			backing = "local"
		}
	}
	handle := &fakeFS{disk: o.disk, root: o.root, backing: backing}
	o.mu.Lock()
	o.handles = append(o.handles, handle)
	o.mu.Unlock()
	return handle, nil
}

func (o *opener) OpenLocal(context.Context) (apptransfer.FileSystem, error) { return o.open() }

func (o *opener) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return o.open()
}

func (o *opener) last() *fakeFS {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.handles) == 0 {
		return nil
	}
	return o.handles[len(o.handles)-1]
}

type fixedHosts []apptransfer.HostTarget

func (h fixedHosts) Hosts(context.Context) ([]apptransfer.HostTarget, error) {
	return h, nil
}

type memoryBookmarks struct {
	mu         sync.Mutex
	items      []domainsftp.Bookmark
	local      []domainsftp.Bookmark
	loadErr    error
	saves      int
	localSaves int
}

func (m *memoryBookmarks) LoadBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domainsftp.Bookmark(nil), m.items...), m.loadErr
}

func (m *memoryBookmarks) SaveBookmarks(_ context.Context, items []domainsftp.Bookmark) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append([]domainsftp.Bookmark(nil), items...)
	m.saves++
	return nil
}

func (m *memoryBookmarks) LoadLocalBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domainsftp.Bookmark(nil), m.local...), m.loadErr
}

func (m *memoryBookmarks) SaveLocalBookmarks(_ context.Context, items []domainsftp.Bookmark) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.local = append([]domainsftp.Bookmark(nil), items...)
	m.localSaves++
	return nil
}

func (m *memoryBookmarks) saved() []domainsftp.Bookmark {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domainsftp.Bookmark(nil), m.items...)
}

func (m *memoryBookmarks) savedLocal() []domainsftp.Bookmark {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domainsftp.Bookmark(nil), m.local...)
}

type fixture struct {
	state    *State
	local    *fakeDisk
	remote   *fakeDisk
	localOp  *opener
	remoteOp *opener
	bookmark *memoryBookmarks
}

// newFixture builds the surface over a local tree and a remote tree. The left
// pane is opened and settled before it returns.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureSeed(t, nil)
}

func newFixtureSeed(t *testing.T, seed []domainsftp.Bookmark) *fixture {
	t.Helper()
	return newFixtureWithBookmarks(t, seed, nil)
}

func newFixtureWithBookmarks(t *testing.T, seed, localSeed []domainsftp.Bookmark) *fixture {
	t.Helper()
	localDisk := newFakeDisk()
	localDisk.mkdirs("/home", "/home/me", "/home/me/folder")
	localDisk.put("/home/me/alpha.txt", "alpha", 0o644)
	localDisk.put("/home/me/beta.bin", strings.Repeat("b", 100), 0o644)
	localDisk.put("/home/me/folder/inside.txt", "inside", 0o600)
	localDisk.put("/home/me/.secret", "hidden", 0o600)

	remoteDisk := newFakeDisk()

	localOp := &opener{disk: localDisk, root: "/home/me", backing: "local"}
	remoteOp := &opener{disk: remoteDisk, root: "/", remote: true, backing: "remote:h1"}
	bookmarks := &memoryBookmarks{items: seed, local: localSeed}

	service := apptransfer.NewService(localOp, remoteOp,
		apptransfer.WithHosts(fixedHosts{{ID: "h1", Label: "Test", Hostname: "test.example", Username: "me", Port: 22}}),
		apptransfer.WithBookmarks(bookmarks),
	)
	state := NewState(service)
	t.Cleanup(state.Shutdown)
	state.EnsureStarted()

	f := &fixture{
		state:    state,
		local:    localDisk,
		remote:   remoteDisk,
		localOp:  localOp,
		remoteOp: remoteOp,
		bookmark: bookmarks,
	}
	f.waitLoaded(t, Left)
	return f
}

// waitLoaded returns once a side has a transport and a settled listing.
func (f *fixture) waitLoaded(t *testing.T, side Side) *Tab {
	t.Helper()
	var tab *Tab
	settle(t, f.state, "a settled listing", func() bool {
		tab = f.state.Tab(side)
		return tab != nil && tab.FS != nil && tab.Path != "" && !tab.Loading
	})
	return tab
}

func (f *fixture) openRemote(t *testing.T, side Side) *Tab {
	t.Helper()
	f.state.OpenRemote(side, "h1", "Test")
	return f.waitLoaded(t, side)
}

func entryNamed(t *testing.T, tab *Tab, name string) domainsftp.Entry {
	t.Helper()
	for _, entry := range tab.Entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("listing has no %q: %+v", name, tab.Entries)
	return domainsftp.Entry{}
}

func selectOne(t *testing.T, s *State, side Side, name string) {
	t.Helper()
	tab := s.Tab(side)
	entry := entryNamed(t, tab, name)
	s.Click(side, entry, 0, false)
	if !tab.Selection[entry.Path] {
		t.Fatalf("%q is not selected after a click", name)
	}
}

func waitIdle(t *testing.T, s *State) {
	t.Helper()
	settle(t, s, "the file operation to finish", func() bool {
		return !s.Busy() && s.operation != nil && !s.operation.state.Running
	})
}

func visibleNames(tab *Tab) []string {
	names := make([]string, 0, len(tab.Visible()))
	for _, entry := range tab.Visible() {
		names = append(names, entry.Name)
	}
	return names
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestPanesBrowseIndependently(t *testing.T) {
	f := newFixture(t)
	left := f.state.Tab(Left)
	if left.Path != "/home/me" || left.FS == nil {
		t.Fatalf("left pane did not start in the local root: %+v", left)
	}
	if contains(visibleNames(left), ".secret") {
		t.Fatal("a hidden file is visible by default")
	}

	f.state.OpenLocal(Right)
	right := f.waitLoaded(t, Right)
	if right.Path != "/home/me" || right.ID == left.ID {
		t.Fatalf("right pane did not get its own Tab: %+v", right)
	}

	f.state.ToggleHidden(Left)
	if !left.ShowHidden || right.ShowHidden {
		t.Fatal("the hidden-file setting leaked between panes")
	}
	if !contains(visibleNames(left), ".secret") || contains(visibleNames(right), ".secret") {
		t.Fatalf("hidden files did not follow their pane: left=%v right=%v", visibleNames(left), visibleNames(right))
	}

	f.state.Focus(Left)
	f.state.Navigate(Left, "/home/me/folder")
	settle(t, f.state, "the left pane to enter folder", func() bool {
		return left.Path == "/home/me/folder" && !left.Loading
	})
	if right.Path != "/home/me" {
		t.Fatalf("navigating the left pane moved the right pane to %q", right.Path)
	}

	selectOne(t, f.state, Left, "inside.txt")
	if len(right.Selection) != 0 {
		t.Fatalf("the selection leaked to the other pane: %v", right.Selection)
	}

	f.state.SetSort(Left, domainsftp.Sort{Field: domainsftp.SortSize, Descending: true})
	f.state.ToggleFoldersFirst(Left)
	if right.Sort.Field != domainsftp.SortName || right.Sort.Descending || right.Sort.MixKinds {
		t.Fatalf("the ordering leaked to the other pane: %+v", right.Sort)
	}
	if left.Sort.Field != domainsftp.SortSize || !left.Sort.Descending || !left.Sort.MixKinds {
		t.Fatalf("the left ordering was not applied: %+v", left.Sort)
	}
}

func TestTabsKeepTheirOwnLocationAndSelection(t *testing.T) {
	f := newFixture(t)
	first := f.state.Tab(Left)

	f.state.OpenLocal(Left)
	second := f.waitLoaded(t, Left)
	if len(f.state.Pane(Left).Tabs) != 2 || second.ID == first.ID {
		t.Fatalf("opening a Tab did not add one: %+v", f.state.Pane(Left).Tabs)
	}

	f.state.Navigate(Left, "/home/me/folder")
	settle(t, f.state, "the second Tab to enter folder", func() bool {
		return second.Path == "/home/me/folder" && !second.Loading
	})
	selectOne(t, f.state, Left, "inside.txt")

	f.state.SelectTab(Left, 0)
	if f.state.Tab(Left) != first || first.Path != "/home/me" || len(first.Selection) != 0 {
		t.Fatalf("the first Tab lost its state: %+v", first)
	}
	selectOne(t, f.state, Left, "alpha.txt")

	f.state.SelectTab(Left, 1)
	if f.state.Tab(Left) != second || second.Path != "/home/me/folder" || !second.Selection["/home/me/folder/inside.txt"] {
		t.Fatalf("the second Tab lost its state: %+v", second)
	}

	f.state.Duplicate(Left)
	duplicate := f.waitLoaded(t, Left)
	if len(f.state.Pane(Left).Tabs) != 3 || duplicate.Path != "/home/me/folder" || duplicate.ID == second.ID {
		t.Fatalf("duplicate did not copy the location into a new Tab: %+v", f.state.Pane(Left).Tabs)
	}

	handle := f.localOp.last()
	f.state.CloseTab(Left)
	if len(f.state.Pane(Left).Tabs) != 2 {
		t.Fatalf("closing the active Tab left %d Tabs", len(f.state.Pane(Left).Tabs))
	}
	if handle == nil || !handle.isClosed() {
		t.Fatal("closing a Tab did not close its filesystem")
	}
}

func TestUploadCopiesTheSelectionToTheRemotePane(t *testing.T) {
	f := newFixture(t)
	f.openRemote(t, Right)
	selectOne(t, f.state, Left, "alpha.txt")

	f.state.Transfer(Left)
	waitIdle(t, f.state)
	if got, ok := f.remote.read("/alpha.txt"); !ok || got != "alpha" {
		t.Fatalf("upload wrote %q, %v; want alpha", got, ok)
	}
	if _, ok := f.local.read("/home/me/alpha.txt"); !ok {
		t.Fatal("upload removed the source")
	}
	snapshot := f.state.Snapshot()
	if snapshot == nil || snapshot.Kind != domainsftp.OpUpload || snapshot.Err != nil {
		t.Fatalf("upload result is wrong: %+v", snapshot)
	}
	if f.state.Notice() != "operation-done" {
		t.Fatalf("notice is %q", f.state.Notice())
	}
	if snapshot.FilesDone != 1 || snapshot.FilesTotal != 1 {
		t.Fatalf("progress did not finish: %+v", snapshot)
	}
	settle(t, f.state, "the remote pane to refresh", func() bool {
		tab := f.state.Tab(Right)
		return !tab.Loading && contains(visibleNames(tab), "alpha.txt")
	})
}

func TestDownloadCopiesFromRemoteToLocal(t *testing.T) {
	f := newFixture(t)
	f.remote.put("/remote.txt", "payload", 0o644)
	f.openRemote(t, Right)
	selectOne(t, f.state, Right, "remote.txt")

	f.state.Transfer(Right)
	waitIdle(t, f.state)
	if got, ok := f.local.read("/home/me/remote.txt"); !ok || got != "payload" {
		t.Fatalf("download wrote %q, %v", got, ok)
	}
	if snapshot := f.state.Snapshot(); snapshot == nil || snapshot.Kind != domainsftp.OpDownload {
		t.Fatalf("download kind is wrong: %+v", snapshot)
	}
}

func TestOverwritePromptReplacesOnlyAfterConfirmation(t *testing.T) {
	f := newFixture(t)
	f.remote.put("/alpha.txt", "old", 0o644)
	f.openRemote(t, Right)
	selectOne(t, f.state, Left, "alpha.txt")

	f.state.Transfer(Left)
	settle(t, f.state, "the overwrite prompt", func() bool {
		return f.state.Dialog() != nil && f.state.Dialog().Kind == DialogOverwrite
	})
	if got, _ := f.remote.read("/alpha.txt"); got != "old" {
		t.Fatalf("the destination changed before confirmation: %q", got)
	}
	if f.state.Busy() {
		t.Fatal("the operation still reports busy while waiting for the prompt")
	}
	if snapshot := f.state.Snapshot(); snapshot != nil {
		t.Fatalf("the prompt left a failed operation in the bar: %+v", snapshot)
	}
	if f.state.Notice() != "overwrite-required" {
		t.Fatalf("notice is %q", f.state.Notice())
	}
	if dialog := f.state.Dialog(); len(dialog.Paths) != 1 || dialog.Paths[0] != "/home/me/alpha.txt" {
		t.Fatalf("the prompt carries %v", dialog.Paths)
	}

	f.state.SubmitDialog()
	if f.state.Dialog() != nil {
		t.Fatal("confirming the replacement left the prompt open")
	}
	waitIdle(t, f.state)
	if got, _ := f.remote.read("/alpha.txt"); got != "alpha" {
		t.Fatalf("the destination was not replaced: %q", got)
	}
}

func TestCopyAndCutThroughTheClipboard(t *testing.T) {
	f := newFixture(t)
	f.openRemote(t, Right)
	selectOne(t, f.state, Left, "alpha.txt")

	f.state.CopySelection(Left)
	if f.state.Notice() != "copied" || !f.state.HasClipboard() {
		t.Fatalf("copy did not fill the clipboard: %q", f.state.Notice())
	}
	f.state.Focus(Right)
	f.state.Paste(Right)
	waitIdle(t, f.state)
	if got, _ := f.remote.read("/alpha.txt"); got != "alpha" {
		t.Fatalf("paste wrote %q", got)
	}
	if _, ok := f.local.read("/home/me/alpha.txt"); !ok {
		t.Fatal("a copy paste removed the source")
	}

	f.state.Focus(Left)
	f.state.CutSelection(Left)
	f.state.Focus(Right)
	f.state.Paste(Right)
	settle(t, f.state, "the cut overwrite prompt", func() bool {
		return f.state.Dialog() != nil && f.state.Dialog().Kind == DialogOverwrite
	})
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if _, ok := f.local.read("/home/me/alpha.txt"); ok {
		t.Fatal("a cut paste left the source behind")
	}
	if _, ok := f.remote.read("/alpha.txt"); !ok {
		t.Fatal("the cut paste removed the destination")
	}
	settle(t, f.state, "the source listing to refresh after the move", func() bool {
		return !contains(visibleNames(f.state.Tab(Left)), "alpha.txt")
	})
	if f.state.HasClipboard() {
		t.Fatal("a cut clipboard survived its paste")
	}
}

func TestCopyIntoItsOwnSubtreeIsRefused(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "folder")
	payload := f.state.BeginDrag(Left)
	if payload == nil {
		t.Fatal("the folder did not produce a drag payload")
	}
	f.state.DropPane(Left, "/home/me/folder", payload)
	f.state.Flush()
	if f.state.Notice() != "copy-into-self" {
		t.Fatalf("notice = %q", f.state.Notice())
	}
	if f.local.has("/home/me/folder/folder") {
		t.Fatal("the copy created a folder inside itself")
	}
}

func TestCopyBetweenLocalTabsIntoSourceSubtreeIsRefused(t *testing.T) {
	f := newFixture(t)
	// The retester's scenario: a second local Tab starts at the same root and
	// enters the folder that the first Tab is about to copy.
	f.state.OpenLocal(Right)
	f.waitLoaded(t, Right)
	f.state.Navigate(Right, "/home/me/folder")
	settle(t, f.state, "the second local Tab to enter the folder", func() bool {
		return f.state.Tab(Right).Path == "/home/me/folder" && !f.state.Tab(Right).Loading
	})

	selectOne(t, f.state, Left, "folder")
	f.state.Transfer(Left)
	f.state.Flush()

	if got := f.state.Notice(); got != "copy-into-self" {
		t.Fatalf("cross-Tab copy notice = %q, want copy-into-self", got)
	}
	if f.state.Busy() {
		t.Fatal("the refused copy still started an operation")
	}
	if f.local.has("/home/me/folder/folder") {
		t.Fatal("the refused copy created a folder inside its source")
	}
	if got, ok := f.local.read("/home/me/folder/inside.txt"); !ok || got != "inside" {
		t.Fatalf("the refused copy changed the source file: %q, %v", got, ok)
	}
}

// hostOpener gives each Host its own tree, so two remote Tabs can sit at the
// same path on different servers.
type hostOpener struct {
	local *fakeDisk
	hosts map[model.HostID]*fakeDisk
}

func (o *hostOpener) OpenLocal(context.Context) (apptransfer.FileSystem, error) {
	return &fakeFS{disk: o.local, root: "/home", backing: "local"}, nil
}

func (o *hostOpener) OpenRemote(_ context.Context, host model.HostID, _ string) (apptransfer.FileSystem, error) {
	return &fakeFS{disk: o.hosts[host], root: "/", backing: "remote:" + string(host)}, nil
}

func TestCopyBetweenDifferentHostsAtTheSamePathIsAllowed(t *testing.T) {
	local := newFakeDisk()
	local.mkdirs("/home")
	source := newFakeDisk()
	source.mkdirs("/srv")
	source.put("/srv/inside.txt", "inside", 0o644)
	dest := newFakeDisk()
	dest.mkdirs("/srv")

	service := apptransfer.NewService(
		&hostOpener{local: local, hosts: map[model.HostID]*fakeDisk{"h1": source, "h2": dest}},
		&hostOpener{local: local, hosts: map[model.HostID]*fakeDisk{"h1": source, "h2": dest}},
	)
	state := NewState(service)
	t.Cleanup(state.Shutdown)
	state.EnsureStarted()
	settle(t, state, "the local listing", func() bool {
		tab := state.Tab(Left)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	state.OpenRemote(Left, "h1", "One")
	state.OpenRemote(Right, "h2", "Two")
	settle(t, state, "both remote listings", func() bool {
		left, right := state.Tab(Left), state.Tab(Right)
		return left != nil && right != nil && left.FS != nil && right.FS != nil &&
			!left.Loading && !right.Loading && left.Path == "/" && right.Path == "/"
	})
	state.Navigate(Right, "/srv")
	settle(t, state, "the destination Host to enter /srv", func() bool {
		tab := state.Tab(Right)
		return tab.Path == "/srv" && !tab.Loading
	})

	selectOne(t, state, Left, "srv")
	state.Transfer(Left)
	waitIdle(t, state)
	if got := state.Notice(); got == "copy-into-self" {
		t.Fatal("copies between different Hosts were mistaken for a self-copy")
	}
	if got, ok := dest.read("/srv/srv/inside.txt"); !ok || got != "inside" {
		t.Fatalf("cross-Host copy wrote %q, %v", got, ok)
	}
}

func TestCopyBetweenTwoTabsOnOneHostIntoItsSubtreeIsRefused(t *testing.T) {
	local := newFakeDisk()
	local.mkdirs("/home")
	source := newFakeDisk()
	source.mkdirs("/srv", "/srv/folder")
	source.put("/srv/folder/inside.txt", "inside", 0o644)
	opener := func() *hostOpener {
		return &hostOpener{local: local, hosts: map[model.HostID]*fakeDisk{"h1": source}}
	}

	service := apptransfer.NewService(opener(), opener())
	state := NewState(service)
	t.Cleanup(state.Shutdown)
	state.EnsureStarted()
	settle(t, state, "the local listing", func() bool {
		tab := state.Tab(Left)
		return tab != nil && tab.FS != nil && !tab.Loading
	})
	// Two separate connections to one Host address the same tree.
	state.OpenRemote(Left, "h1", "One")
	settle(t, state, "the first remote listing", func() bool {
		tab := state.Tab(Left)
		return tab != nil && tab.FS != nil && !tab.Loading && tab.Path == "/" && len(tab.Visible()) > 0
	})
	state.OpenRemote(Right, "h1", "One again")
	settle(t, state, "the second remote listing", func() bool {
		tab := state.Tab(Right)
		return tab != nil && tab.FS != nil && !tab.Loading && tab.Path == "/" && len(tab.Visible()) > 0
	})
	state.Navigate(Right, "/srv/folder")
	settle(t, state, "the second connection to enter /srv/folder", func() bool {
		tab := state.Tab(Right)
		return tab.Path == "/srv/folder" && !tab.Loading
	})

	selectOne(t, state, Left, "srv")
	state.Transfer(Left)
	state.Flush()
	if got := state.Notice(); got != "copy-into-self" {
		t.Fatalf("same-Host copy notice = %q, want copy-into-self", got)
	}
	if state.Busy() {
		t.Fatal("the refused copy still started an operation")
	}
	if source.has("/srv/folder/srv") {
		t.Fatal("the refused copy created a folder inside its source")
	}
	if got, ok := source.read("/srv/folder/inside.txt"); !ok || got != "inside" {
		t.Fatalf("the refused copy changed the source file: %q, %v", got, ok)
	}
}

func secondLocalAt(t *testing.T, f *fixture, path string) {
	t.Helper()
	f.state.OpenLocal(Right)
	f.waitLoaded(t, Right)
	f.state.Navigate(Right, path)
	settle(t, f.state, "the second local Tab to enter "+path, func() bool {
		tab := f.state.Tab(Right)
		return tab.Path == path && !tab.Loading
	})
}

func TestCrossTabPasteIntoSourceSubtreeIsRefused(t *testing.T) {
	f := newFixture(t)
	secondLocalAt(t, f, "/home/me/folder")
	selectOne(t, f.state, Left, "folder")
	f.state.CopySelection(Left)
	f.state.Paste(Right)
	f.state.Flush()

	if got := f.state.Notice(); got != "copy-into-self" {
		t.Fatalf("cross-Tab paste notice = %q", got)
	}
	if f.local.has("/home/me/folder/folder") {
		t.Fatal("the refused paste created a folder inside its source")
	}
}

func TestCrossTabMoveIntoSourceSubtreeIsRefused(t *testing.T) {
	f := newFixture(t)
	secondLocalAt(t, f, "/home/me/folder")
	selectOne(t, f.state, Left, "folder")
	f.state.MoveToOther(Left)
	f.state.Flush()

	if got := f.state.Notice(); got != "copy-into-self" {
		t.Fatalf("cross-Tab move notice = %q", got)
	}
	if !f.local.has("/home/me/folder") || f.local.has("/home/me/folder/folder") {
		t.Fatal("the refused move changed the source tree")
	}
}

func TestCrossTabDragIntoSourceSubtreeIsRefused(t *testing.T) {
	f := newFixture(t)
	secondLocalAt(t, f, "/home/me/folder")
	selectOne(t, f.state, Left, "folder")
	payload := f.state.BeginDrag(Left)
	if payload == nil {
		t.Fatal("the folder did not produce a drag payload")
	}
	f.state.DropPane(Right, "/home/me/folder", payload)
	f.state.Flush()

	if got := f.state.Notice(); got != "copy-into-self" {
		t.Fatalf("cross-Tab drag notice = %q", got)
	}
	if f.local.has("/home/me/folder/folder") {
		t.Fatal("the refused drag created a folder inside its source")
	}
}

func TestDroppedOSFolderIntoItsSubtreeIsRefused(t *testing.T) {
	f := newFixture(t)
	secondLocalAt(t, f, "/home/me/folder")
	f.state.DropFiles(Right, "/home/me/folder", []string{"/home/me/folder"})
	settle(t, f.state, "the refused OS drop", func() bool {
		return f.state.Notice() == "copy-into-self"
	})
	if f.state.Busy() {
		t.Fatal("the refused OS drop left an operation running")
	}
	if f.local.has("/home/me/folder/folder") {
		t.Fatal("the refused OS drop created a folder inside its source")
	}
}

func TestClosingTheSourceTabClearsTheClipboard(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "alpha.txt")
	f.state.CopySelection(Left)
	if !f.state.HasClipboard() {
		t.Fatal("copy did not fill the clipboard")
	}
	f.state.CloseTab(Left)
	if f.state.HasClipboard() {
		t.Fatal("the clipboard survived the source Tab closing")
	}
}

func TestDuplicateOfALoadingRemoteTabOpensItsRoot(t *testing.T) {
	f := newFixture(t)
	f.state.OpenRemote(Right, "h1", "Test")
	// Duplicate before Flush applies the open, exactly as a click on the menu
	// can while the first listing is still in flight.
	f.state.Duplicate(Right)
	settle(t, f.state, "both remote Tabs", func() bool {
		pane := f.state.Pane(Right)
		if len(pane.Tabs) != 2 {
			return false
		}
		for _, tab := range pane.Tabs {
			if tab.FS == nil || tab.Loading || tab.Path == "" {
				return false
			}
		}
		return true
	})
	for _, tab := range f.state.Pane(Right).Tabs {
		if tab.Path != "/" {
			t.Fatalf("a duplicated Tab opened at %q", tab.Path)
		}
	}
}

func TestSamePanePasteDuplicatesInsteadOfOverwriting(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "alpha.txt")
	f.state.CopySelection(Left)
	f.state.Paste(Left)
	waitIdle(t, f.state)
	if got, ok := f.local.read("/home/me/alpha (copy 1).txt"); !ok || got != "alpha" {
		t.Fatalf("same-pane paste did not duplicate: %q %v", got, ok)
	}
	if got, _ := f.local.read("/home/me/alpha.txt"); got != "alpha" {
		t.Fatalf("same-pane paste replaced the original: %q", got)
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "alpha.txt")

	f.state.RequestDelete(Left)
	dialog := f.state.Dialog()
	if dialog == nil || dialog.Kind != DialogDelete || len(dialog.Paths) != 1 {
		t.Fatalf("delete did not open a confirmation: %+v", dialog)
	}
	if !f.local.has("/home/me/alpha.txt") {
		t.Fatal("the file was deleted before confirmation")
	}
	f.state.CloseDialog()
	if !f.local.has("/home/me/alpha.txt") {
		t.Fatal("cancelling the prompt deleted the file")
	}

	f.state.RequestDelete(Left)
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if f.local.has("/home/me/alpha.txt") {
		t.Fatal("confirming the prompt did not delete the file")
	}
	if snapshot := f.state.Snapshot(); snapshot == nil || snapshot.Kind != domainsftp.OpDelete {
		t.Fatalf("delete result is wrong: %+v", snapshot)
	}
}

func TestRecursiveDeleteRemovesADirectoryTree(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "folder")
	f.state.RequestDelete(Left)
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if f.local.has("/home/me/folder") || f.local.has("/home/me/folder/inside.txt") {
		t.Fatal("the directory tree survived a confirmed delete")
	}
}

func TestDialogsValidateBeforeMutating(t *testing.T) {
	f := newFixture(t)

	f.state.RequestNewFolder(Left)
	f.state.SetDialogDraft("bad/name")
	f.state.SubmitDialog()
	if dialog := f.state.Dialog(); dialog == nil || dialog.Err != "invalid-name" {
		t.Fatalf("an invalid folder name was not refused: %+v", dialog)
	}
	if f.local.has("/home/me/bad/name") {
		t.Fatal("an invalid name created a directory")
	}
	f.state.SetDialogDraft("created")
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if !f.local.isDir("/home/me/created") {
		t.Fatal("the new folder was not created")
	}

	f.state.RequestNewFile(Left)
	f.state.SetDialogDraft("notes.txt")
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if got, ok := f.local.read("/home/me/notes.txt"); !ok || got != "" {
		t.Fatalf("the new file is %q, %v", got, ok)
	}

	selectOne(t, f.state, Left, "alpha.txt")
	f.state.RequestRename(Left)
	if draft := f.state.Dialog().Draft; draft != "alpha.txt" {
		t.Fatalf("the rename prompt starts with %q", draft)
	}
	f.state.SetDialogDraft("renamed.txt")
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if f.local.has("/home/me/alpha.txt") || !f.local.has("/home/me/renamed.txt") {
		t.Fatal("rename did not move the file")
	}

	selectOne(t, f.state, Left, "beta.bin")
	f.state.RequestChmod(Left)
	f.state.SetDialogDraft("999")
	f.state.SubmitDialog()
	if dialog := f.state.Dialog(); dialog == nil || dialog.Err != "invalid-mode" {
		t.Fatalf("an invalid mode was not refused: %+v", dialog)
	}
	f.state.SetDialogDraft("0600")
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if mode, _ := f.local.permission("/home/me/beta.bin"); mode != 0o600 {
		t.Fatalf("mode is %04o, want 0600", mode)
	}
}

func TestBookmarksStayPerPaneAndPersistGlobals(t *testing.T) {
	f := newFixture(t)
	left := f.state.Tab(Left)

	f.state.ToggleBookmark(Left)
	if !f.state.Bookmarked(Left) {
		t.Fatal("the local folder was not bookmarked")
	}
	if len(f.state.Bookmarks(Left)) != 1 {
		t.Fatalf("the local bookmark list has %d entries", len(f.state.Bookmarks(Left)))
	}
	if len(f.state.Bookmarks(Right)) != 0 {
		t.Fatal("a local bookmark appeared on the remote pane")
	}
	settle(t, f.state, "the local bookmark to persist", func() bool {
		items := f.bookmark.savedLocal()
		return len(items) == 1 && items[0].Path == left.Path
	})
	f.state.RemoveBookmark(Left, domainsftp.Bookmark{Path: left.Path}.Key())
	if f.state.Bookmarked(Left) {
		t.Fatal("removing the bookmark left it in place")
	}
	settle(t, f.state, "the removed local bookmark to persist", func() bool {
		return len(f.bookmark.savedLocal()) == 0
	})

	f.openRemote(t, Right)
	f.state.ToggleBookmark(Right)
	if !f.state.Bookmarked(Right) {
		t.Fatal("the remote folder was not bookmarked")
	}
	settle(t, f.state, "the global bookmark to persist", func() bool {
		items := f.bookmark.saved()
		return len(items) == 1 && items[0].HostID == "h1" && items[0].Path == "/"
	})

	bookmark := f.state.Bookmarks(Right)[0]
	f.state.Focus(Left)
	f.state.GoBookmark(Left, bookmark)
	settle(t, f.state, "the bookmark to open a remote Tab", func() bool {
		tab := f.state.Tab(Left)
		return tab != nil && tab.Kind == KindRemote && tab.FS != nil && tab.Path == "/"
	})
}

func TestLocalBookmarksLoadOnStart(t *testing.T) {
	f := newFixtureWithBookmarks(t, nil, []domainsftp.Bookmark{{
		Label: "/srv/local", Path: "/srv/local",
	}})
	settle(t, f.state, "the persisted local bookmarks", func() bool {
		return len(f.state.Bookmarks(Left)) == 1
	})
	if got := f.state.Bookmarks(Left)[0]; got.Path != "/srv/local" || got.HostID != "" {
		t.Fatalf("loaded bookmark is %+v", got)
	}
	if len(f.state.Bookmarks(Right)) != 0 {
		t.Fatal("a local bookmark appeared on a remote pane")
	}
}

func TestGlobalBookmarksLoadOnStart(t *testing.T) {
	f := newFixtureSeed(t, []domainsftp.Bookmark{{
		Label: "Test:/srv", Path: "/srv", HostID: "h1", HostLabel: "Test",
	}})
	settle(t, f.state, "the persisted global bookmarks", func() bool {
		return len(f.state.Bookmarks(Left)) == 1
	})
	if got := f.state.Bookmarks(Left)[0]; got.Path != "/srv" || got.HostID != "h1" {
		t.Fatalf("loaded bookmark is %+v", got)
	}
}

func TestHostPickerOpensTheChosenHost(t *testing.T) {
	f := newFixture(t)
	f.state.RequestHostPicker(Right)
	settle(t, f.state, "the host list", func() bool {
		return !f.state.HostsLoading() && len(f.state.HostTargets()) == 1
	})
	target := f.state.HostTargets()[0]
	if target.Label != "Test" || target.Hostname != "test.example" || target.Port != 22 {
		t.Fatalf("host target is %+v", target)
	}
	f.state.ChooseHost(Right, target.ID, target.Label)
	right := f.waitLoaded(t, Right)
	if right.Kind != KindRemote || right.HostID != "h1" || right.Title() != "Test" {
		t.Fatalf("remote Tab is %+v", right)
	}
}

func TestDragBetweenPanesCopiesAndSamePaneDropMoves(t *testing.T) {
	f := newFixture(t)
	f.openRemote(t, Right)
	selectOne(t, f.state, Left, "alpha.txt")

	payload := f.state.BeginDrag(Left)
	if payload == nil || len(payload.Paths) != 1 || payload.Paths[0] != "/home/me/alpha.txt" {
		t.Fatalf("drag payload is %+v", payload)
	}
	f.state.DropPane(Right, "/", payload)
	waitIdle(t, f.state)
	if got, _ := f.remote.read("/alpha.txt"); got != "alpha" {
		t.Fatalf("drag between panes copied %q", got)
	}
	if _, ok := f.local.read("/home/me/alpha.txt"); !ok {
		t.Fatal("a cross-pane drag removed the source")
	}

	left := f.state.Tab(Left)
	entry := entryNamed(t, left, "alpha.txt")
	drag := f.state.DragFor(Left, entry)
	f.state.DropPane(Left, "/home/me/folder", drag)
	waitIdle(t, f.state)
	if f.local.has("/home/me/alpha.txt") {
		t.Fatal("a same-pane drop left the source in place")
	}
	if got, ok := f.local.read("/home/me/folder/alpha.txt"); !ok || got != "alpha" {
		t.Fatalf("the moved file is %q, %v", got, ok)
	}
}

func TestDroppedOSFilesUploadToTheRemotePane(t *testing.T) {
	f := newFixture(t)
	f.openRemote(t, Right)
	f.state.DropFiles(Right, "", []string{"/home/me/beta.bin"})
	waitIdle(t, f.state)
	if got, ok := f.remote.read("/beta.bin"); !ok || len(got) != 100 {
		t.Fatalf("OS drop wrote %d bytes, %v", len(got), ok)
	}
	if snapshot := f.state.Snapshot(); snapshot == nil || snapshot.Kind != domainsftp.OpUpload {
		t.Fatalf("OS drop kind is %+v", snapshot)
	}
}

func TestDroppedOSFilesPromptBeforeReplacing(t *testing.T) {
	f := newFixture(t)
	f.remote.put("/beta.bin", "old", 0o644)
	f.openRemote(t, Right)
	f.state.DropFiles(Right, "", []string{"/home/me/beta.bin"})
	settle(t, f.state, "the OS drop overwrite prompt", func() bool {
		dialog := f.state.Dialog()
		return dialog != nil && dialog.Kind == DialogOverwrite
	})
	if f.state.Snapshot() != nil {
		t.Fatalf("the prompt left an operation in the bar: %+v", f.state.Snapshot())
	}
	if got, _ := f.remote.read("/beta.bin"); string(got) != "old" {
		t.Fatalf("the drop replaced the file before confirmation: %q", got)
	}
	f.state.SubmitDialog()
	waitIdle(t, f.state)
	if got, ok := f.remote.read("/beta.bin"); !ok || len(got) != 100 {
		t.Fatalf("the confirmed drop wrote %d bytes, %v", len(got), ok)
	}
}

func TestRunActionsBelongToTheSurface(t *testing.T) {
	f := newFixture(t)
	if f.state.Run("sftpSelectAll") != true {
		t.Fatal("select all was not claimed")
	}
	tab := f.state.Tab(Left)
	if len(tab.SelectedPaths()) != len(tab.Visible())-1 {
		t.Fatalf("select all chose %v", tab.SelectedPaths())
	}
	if f.state.Run("terminalNewTab") {
		t.Fatal("an unrelated action was claimed")
	}
	f.state.ClearSelection(Left)
	selectOne(t, f.state, Left, "alpha.txt")
	if !f.state.Run("sftpRename") || f.state.Dialog() == nil || f.state.Dialog().Kind != DialogRename {
		t.Fatal("the rename shortcut did not open its prompt for the single selection")
	}
	f.state.CloseDialog()

	f.state.ClearSelection(Left)
	if !f.state.Run("sftpOpen") {
		t.Fatal("open was not claimed")
	}
	if !f.state.Run("sftpNewFolder") || f.state.Dialog().Kind != DialogNewFolder {
		t.Fatal("new folder did not open its prompt")
	}
}

func TestClickModifiersSelectRanges(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	alpha := entryNamed(t, tab, "alpha.txt")
	beta := entryNamed(t, tab, "beta.bin")
	folder := entryNamed(t, tab, "folder")

	f.state.Click(Left, alpha, 0, false)
	f.state.Click(Left, folder, ui.Shift, false)
	selected := tab.SelectedPaths()
	// The listing is directories-first, then alpha and beta; the shift range
	// covers folder through alpha.
	if len(selected) < 2 || !tab.Selection[alpha.Path] || !tab.Selection[folder.Path] {
		t.Fatalf("shift click selected %v", selected)
	}
	if tab.Selection[beta.Path] {
		t.Fatalf("shift click ran past the anchor: %v", selected)
	}

	f.state.Click(Left, beta, ui.Ctrl, false)
	if !tab.Selection[beta.Path] || !tab.Selection[alpha.Path] {
		t.Fatalf("ctrl click did not add to the selection: %v", tab.SelectedPaths())
	}
	f.state.Click(Left, beta, ui.Ctrl, false)
	if tab.Selection[beta.Path] {
		t.Fatal("ctrl click did not toggle the row off")
	}
}

func TestShortcutsToggleFocus(t *testing.T) {
	f := newFixture(t)
	f.state.OpenLocal(Right)
	f.waitLoaded(t, Right)
	f.state.Focus(Right)
	f.state.ToggleFocus()
	if f.state.Focused() != Left {
		t.Fatalf("focus is %v, want left", f.state.Focused())
	}
}

func TestUnavailablePaneIsReportedNotPanicked(t *testing.T) {
	service := apptransfer.NewService(&opener{disk: newFakeDisk(), root: "/"}, nil)
	state := NewState(service)
	t.Cleanup(state.Shutdown)
	state.EnsureStarted()
	settle(t, state, "the local listing", func() bool {
		tab := state.Tab(Left)
		return tab != nil && tab.FS != nil && tab.Path != ""
	})
	state.Transfer(Left)
	if state.Notice() != "pane-unavailable" {
		t.Fatalf("transfer without a remote pane reported %q", state.Notice())
	}
	state.Paste(Right)
	if state.Notice() != "clipboard-empty" {
		t.Fatalf("paste without a clipboard reported %q", state.Notice())
	}
	state.OpenRemote(Right, "missing", "Missing")
	settle(t, state, "the failed connection to report", func() bool {
		tab := state.Tab(Right)
		return tab != nil && tab.Problem != nil && !tab.Loading
	})
}

func TestOpenFileOnTheSamePaneIsExplained(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "alpha.txt")
	f.state.Run("sftpOpen")
	if f.state.Notice() != "open-file" {
		t.Fatalf("activating a file reported %q", f.state.Notice())
	}
}
