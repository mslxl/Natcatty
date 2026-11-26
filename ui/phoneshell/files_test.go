package phoneshell

// Behavior tests for the phone Files pages. They drive the real Shell and the
// real shared transfer service over temporary local trees standing in for two
// Hosts' filesystems, so browsing, cross-Host copy, conflict handling, and the
// Follow terminal directory rule all run through the same application code the
// phone root wires.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// localTree is an in-memory Host's filesystem: the local OS adapter rooted at
// a temporary directory, with a Host-specific Backing so the copy engine sees
// two distinct stores. A test can make one listing fail once.
type localTree struct {
	apptransfer.FileSystem
	root    string
	backing string

	mu      sync.Mutex
	failDir map[string]int
	// failCreates makes the next count file writes fail once the copy engine
	// starts writing.
	failCreates int
}

func newLocalTree(t *testing.T, name string) *localTree {
	t.Helper()
	files, err := adaptersftp.NewLocal().OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("open a local tree: %v", err)
	}
	return &localTree{
		FileSystem: files,
		root:       t.TempDir(),
		backing:    "test:" + name,
		failDir:    make(map[string]int),
	}
}

func (f *localTree) Root(context.Context) (string, error) { return f.root, nil }

func (f *localTree) Backing() string { return f.backing }

// failNext makes the next count listings of dir fail.
func (f *localTree) failNext(dir string, count int) {
	f.mu.Lock()
	f.failDir[dir] = count
	f.mu.Unlock()
}

func (f *localTree) List(ctx context.Context, dir string) ([]domainsftp.Entry, error) {
	f.mu.Lock()
	count := f.failDir[dir]
	if count > 0 {
		f.failDir[dir] = count - 1
	}
	f.mu.Unlock()
	if count > 0 {
		return nil, errors.New("temporary read failure")
	}
	return f.FileSystem.List(ctx, dir)
}

func (f *localTree) Create(ctx context.Context, path string) (io.WriteCloser, error) {
	f.mu.Lock()
	if f.failCreates > 0 {
		f.failCreates--
		f.mu.Unlock()
		return nil, errors.New("write refused")
	}
	f.mu.Unlock()
	return f.FileSystem.Create(ctx, path)
}

// fakeFileOpener is the shared transfer service's remote opener over the two
// test trees. It can be told to fail a Host's open.
type fakeFileOpener struct {
	mu    sync.Mutex
	trees map[model.HostID]apptransfer.FileSystem
	errs  map[model.HostID]error
}

func (o *fakeFileOpener) OpenRemote(_ context.Context, hostID model.HostID, _ string) (apptransfer.FileSystem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.errs[hostID]; err != nil {
		return nil, err
	}
	files, ok := o.trees[hostID]
	if !ok {
		return nil, errors.New("no such test Host")
	}
	return files, nil
}

func (o *fakeFileOpener) setErr(hostID model.HostID, err error) {
	o.mu.Lock()
	o.errs[hostID] = err
	o.mu.Unlock()
}

func (o *fakeFileOpener) setTree(hostID model.HostID, files apptransfer.FileSystem) {
	o.mu.Lock()
	o.trees[hostID] = files
	o.mu.Unlock()
}

// fakePicker serves the device picker. answer is returned until the test
// changes it; err stands in for a canceled or refused picker.
type fakePicker struct {
	mu     sync.Mutex
	answer []string
	err    error
	calls  int
}

func (p *fakePicker) pick() ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return append([]string(nil), p.answer...), p.err
}

func (p *fakePicker) set(answer ...string) {
	p.mu.Lock()
	p.answer = answer
	p.err = nil
	p.mu.Unlock()
}

// filesFixture is the phone Shell over the shared transfer service and two
// temporary Host trees.
type filesFixture struct {
	*phoneFixture
	source *localTree
	target *localTree
	opener *fakeFileOpener
	queue  *apptransfer.Queue
	picker *fakePicker
}

func newFilesFixture(t *testing.T) *filesFixture {
	return newFilesFixtureWithQueue(t, nil)
}

// newFilesFixtureWithQueue builds the fixture over the shared transfer service
// and the global queue. repo, when non-nil, persists the queue.
func newFilesFixtureWithQueue(t *testing.T, repo apptransfer.QueueRepository) *filesFixture {
	t.Helper()
	source := newLocalTree(t, "prod")
	target := newLocalTree(t, "target")
	writeTreeFile(t, source.root, "alpha.txt", "alpha")
	writeTreeFile(t, source.root, "report.txt", "report")
	if err := os.MkdirAll(filepath.Join(source.root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTreeFile(t, source.root, "sub/beta.txt", "beta")
	writeTreeFile(t, target.root, "existing.txt", "old")

	opener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{
			"prod":   source,
			"target": target,
		},
		errs: make(map[model.HostID]error),
	}
	// The local opener serves device uploads; the remote opener stands in for
	// the two Hosts.
	service := apptransfer.NewService(adaptersftp.NewLocal(), opener)
	options := []apptransfer.QueueOption{}
	if repo != nil {
		options = append(options, apptransfer.WithQueueRepository(repo))
	}
	queue := apptransfer.NewQueue(service, options...)
	if err := queue.Start(context.Background()); err != nil {
		t.Fatalf("start transfer queue: %v", err)
	}
	t.Cleanup(queue.Shutdown)
	picker := &fakePicker{}
	hosts := append(phoneHosts(), domainvault.Host{
		ID: "target", Label: "target box", Hostname: "target.example.com",
		Port: 22, Username: "deploy", Protocol: domainvault.ProtocolSSH,
	})
	sessionOpener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, hosts, sessionOpener,
		WithFiles(service), WithTransferQueue(queue),
		WithTransferSourcePicker(picker.pick), WithScreenFactory(fakeScreens))
	return &filesFixture{
		phoneFixture: fixture, source: source, target: target,
		opener: opener, queue: queue, picker: picker,
	}
}

func writeTreeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readTreeFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func (f *filesFixture) click(t *testing.T, label string) {
	t.Helper()
	if err := f.tester.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts: %q", label, err, f.tester.Texts())
	}
	f.tester.Frame()
}

// pumpFrames keeps drawing for d so a navigation a frame may have started has
// time to run and land. It is how a test proves a follow did not fire.
func pumpFrames(f *phoneFixture, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f.tester.Frame()
		f.tester.Advance(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	f.tester.Frame()
}

// hasExactText reports whether one drawn text node equals want. The path text
// is the browser's current directory, so an exact match distinguishes it from
// a longer path that merely contains it.
func hasExactText(texts []string, want string) bool {
	for _, text := range texts {
		if text == want {
			return true
		}
	}
	return false
}

// openHostFiles opens prod box's Host detail and then its Files page.
func (f *filesFixture) openHostFiles(t *testing.T) {
	t.Helper()
	f.click(t, "Details for prod box")
	f.settle()
	f.click(t, "Open Files for prod box")
	f.settle()
	f.await(t, "the source listing", func() bool {
		return f.tester.HasText("alpha.txt")
	})
}

// openSessionFiles opens prod box as a Session and then the Files tool.
func (f *filesFixture) openSessionFiles(t *testing.T) *fakeHandle {
	t.Helper()
	handle := f.openSession(t, "prod box")
	f.openToolsPanel(t, "prod box")
	f.click(t, "Open Files")
	f.await(t, "the source listing in Files", func() bool {
		return f.tester.HasText("alpha.txt")
	})
	return handle
}

// startCopy selects a file, chooses the target Host, and starts the copy.
func (f *filesFixture) startCopy(t *testing.T, name string) {
	t.Helper()
	f.click(t, "Select "+name)
	f.click(t, "Copy to another Host")
	f.click(t, "Copy to target box")
	f.await(t, "the destination panel", func() bool {
		return f.tester.HasText("Start copy")
	})
	f.click(t, "Start copy")
}

// TestHostDetailAndSessionToolsOpenFiles is the entry bullet: both a capable
// Host's detail page and its Session Tools surface reach the browser.
func TestHostDetailAndSessionToolsOpenFiles(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.openHostFiles(t)
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod/files" {
		t.Fatalf("Hosts path = %q, want /host/prod/files", got)
	}
	if !fixture.tester.HasText(fixture.source.root) {
		t.Fatalf("the Files page does not name its directory; texts: %q", fixture.tester.Texts())
	}

	fixture = newFilesFixture(t)
	fixture.openSessionFiles(t)
	if !fixture.tester.HasText("alpha.txt") {
		t.Fatalf("the Session Files tool did not list the Host; texts: %q", fixture.tester.Texts())
	}

	// A Host whose protocol cannot carry SFTP has no Files entry.
	fixture = newFilesFixture(t)
	fixture.click(t, "Details for udp box")
	fixture.settle()
	if fixture.tester.HasText("Open Files for udp box") {
		t.Fatalf("a Mosh Host offered Files; texts: %q", fixture.tester.Texts())
	}
}

// TestFilesBrowseParentAndDirectPath covers the browsing bullet: open a
// directory, return to its parent, and type a path directly.
func TestFilesBrowseParentAndDirectPath(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.openHostFiles(t)

	fixture.click(t, "Open sub")
	fixture.await(t, "the subdirectory listing", func() bool {
		return fixture.tester.HasText("beta.txt")
	})
	sub := filepath.Join(fixture.source.root, "sub")
	if !fixture.tester.HasText(sub) {
		t.Fatalf("the page does not show %q after navigating; texts: %q", sub, fixture.tester.Texts())
	}

	fixture.click(t, "Up")
	fixture.await(t, "the parent listing", func() bool {
		return fixture.tester.HasText("alpha.txt")
	})
	if !fixture.tester.HasText(fixture.source.root) {
		t.Fatalf("Up did not restore the root; texts: %q", fixture.tester.Texts())
	}

	// Edit the path field directly and Go there.
	fixture.click(t, "Path")
	fixture.tester.Type(sub)
	fixture.click(t, "Go")
	fixture.await(t, "the typed path", func() bool {
		return fixture.tester.HasText("beta.txt") && fixture.tester.HasText(sub)
	})
}

// TestFilesCopyAcrossHosts is the cross-Host copy bullet: the selection goes
// to the chosen Host's directory through the shared copy engine.
func TestFilesCopyAcrossHosts(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	fixture.await(t, "the copy result", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if got := readTreeFile(t, fixture.target.root, "report.txt"); got != "report" {
		t.Fatalf("target report.txt = %q, want the source content", got)
	}
}

// TestFilesConflictOverwrite replaces the destination after an explicit
// choice.
func TestFilesConflictOverwrite(t *testing.T) {
	fixture := newFilesFixture(t)
	writeTreeFile(t, fixture.target.root, "report.txt", "old")
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	fixture.await(t, "the conflict", func() bool {
		return fixture.tester.HasText(`"report.txt" already exists in the destination.`)
	})
	fixture.click(t, "Overwrite")
	fixture.await(t, "the overwrite result", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if got := readTreeFile(t, fixture.target.root, "report.txt"); got != "report" {
		t.Fatalf("overwrite left %q, want the source content", got)
	}
}

// TestFilesConflictSkip leaves the destination untouched.
func TestFilesConflictSkip(t *testing.T) {
	fixture := newFilesFixture(t)
	writeTreeFile(t, fixture.target.root, "report.txt", "old")
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	fixture.await(t, "the conflict", func() bool {
		return fixture.tester.HasText(`"report.txt" already exists in the destination.`)
	})
	fixture.click(t, "Skip")
	fixture.await(t, "the skip result", func() bool {
		return fixture.tester.HasText("Copied 0 files to target box.")
	})
	if got := readTreeFile(t, fixture.target.root, "report.txt"); got != "old" {
		t.Fatalf("skip changed the destination to %q", got)
	}
}

// TestFilesConflictRenameKeepsTheExtension uses the shared duplicate policy,
// which places "(copy)" before the extension.
func TestFilesConflictRenameKeepsTheExtension(t *testing.T) {
	fixture := newFilesFixture(t)
	writeTreeFile(t, fixture.target.root, "report.txt", "old")
	fixture.openHostFiles(t)
	fixture.startCopy(t, "report.txt")
	fixture.await(t, "the conflict", func() bool {
		return fixture.tester.HasText(`"report.txt" already exists in the destination.`)
	})
	fixture.click(t, "Rename")
	fixture.await(t, "the rename result", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if got := readTreeFile(t, fixture.target.root, "report (copy).txt"); got != "report" {
		t.Fatalf("renamed copy = %q, want the source content", got)
	}
	if got := readTreeFile(t, fixture.target.root, "report.txt"); got != "old" {
		t.Fatalf("rename changed the original to %q", got)
	}
}

// TestFilesFollowsTheTerminalDirectory is the follow bullet: while Follow is
// on, a change in the shell's directory moves the browser with it.
func TestFilesFollowsTheTerminalDirectory(t *testing.T) {
	fixture := newFilesFixture(t)
	handle := fixture.openSessionFiles(t)
	handle.setCWD(fixture.source.root)

	fixture.click(t, "Follow terminal directory")
	fixture.tester.Frame()

	sub := filepath.Join(fixture.source.root, "sub")
	handle.setCWD(sub)
	fixture.await(t, "the followed directory", func() bool {
		return fixture.tester.HasText("beta.txt") && fixture.tester.HasText(sub)
	})
}

// TestFilesKeepsItsPathWhenAReadFails is the failure bullet: a failed read
// leaves the current directory in place, states the problem, and Retry reads
// the same path again.
func TestFilesKeepsItsPathWhenAReadFails(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.openHostFiles(t)
	sub := filepath.Join(fixture.source.root, "sub")
	fixture.source.failNext(sub, 1)

	fixture.click(t, "Open sub")
	fixture.await(t, "the read failure", func() bool {
		return fixture.tester.HasText("Could not open " + sub)
	})
	if !fixture.tester.HasText(fixture.source.root) {
		t.Fatalf("a failed read lost the current path; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("alpha.txt") {
		t.Fatalf("a failed read lost the current listing; texts: %q", fixture.tester.Texts())
	}

	fixture.click(t, "Retry")
	fixture.await(t, "the retried listing", func() bool {
		return fixture.tester.HasText("beta.txt") && fixture.tester.HasText(sub)
	})
}

// TestFilesOpenFailureRetries covers the open failure: the page states the
// reason and Retry opens the Host without losing anything else.
func TestFilesOpenFailureRetries(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.opener.setErr("prod", errors.New("connection refused"))
	fixture.click(t, "Details for prod box")
	fixture.settle()
	fixture.click(t, "Open Files for prod box")
	fixture.settle()
	fixture.await(t, "the open failure", func() bool {
		return fixture.tester.HasText("Could not open Files for this Host:") &&
			fixture.tester.HasText("connection refused")
	})

	fixture.opener.setErr("prod", nil)
	fixture.click(t, "Retry")
	fixture.await(t, "the retried listing", func() bool {
		return fixture.tester.HasText("alpha.txt")
	})
}

// TestFilesCopyFailureRetries repeats the remembered plan without asking for
// the selection again. The first attempt fails while writing, and Retry runs
// the same copy after the failure is cleared.
func TestFilesCopyFailureRetries(t *testing.T) {
	fixture := newFilesFixture(t)
	fixture.openHostFiles(t)
	fixture.click(t, "Select report.txt")
	fixture.click(t, "Copy to another Host")
	fixture.click(t, "Copy to target box")
	fixture.await(t, "the destination panel", func() bool {
		return fixture.tester.HasText("Start copy")
	})
	fixture.target.failCreates = 1
	fixture.click(t, "Start copy")
	fixture.await(t, "the copy failure", func() bool {
		return fixture.tester.HasText("Could not copy:")
	})

	fixture.click(t, "Retry")
	fixture.await(t, "the retried destination panel", func() bool {
		return fixture.tester.HasText("Start copy")
	})
	fixture.click(t, "Start copy")
	fixture.await(t, "the retried copy", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if got := readTreeFile(t, fixture.target.root, "report.txt"); got != "report" {
		t.Fatalf("retried copy wrote %q", got)
	}
}

// TestFilesCopyRetryKeepsDestinationAndAnswers proves Retry continues the
// remembered copy: a non-root destination and the user's conflict answer
// survive the failure, so Retry neither resets the path to the target root nor
// asks the same conflict again.
func TestFilesCopyRetryKeepsDestinationAndAnswers(t *testing.T) {
	fixture := newFilesFixture(t)
	inbox := filepath.Join(fixture.target.root, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	writeTreeFile(t, fixture.target.root, "inbox/report.txt", "old")

	fixture.openHostFiles(t)
	fixture.click(t, "Select report.txt")
	fixture.click(t, "Copy to another Host")
	fixture.click(t, "Copy to target box")
	fixture.await(t, "the destination panel", func() bool {
		return fixture.tester.HasText("Start copy")
	})

	// Choose a destination that is not the target's root and answer the
	// conflict there.
	if err := fixture.tester.Click("Destination"); err != nil {
		t.Fatalf("focus the destination: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Key(ui.Cmd, ui.KeyA)
	fixture.tester.Type(inbox)
	fixture.tester.Frame()
	fixture.target.failCreates = 1
	fixture.click(t, "Start copy")
	fixture.await(t, "the conflict", func() bool {
		return fixture.tester.HasText(`"report.txt" already exists in the destination.`)
	})
	fixture.click(t, "Overwrite")
	fixture.await(t, "the copy failure", func() bool {
		return fixture.tester.HasText("Could not copy:")
	})

	// Retry reopens the remembered destination, not the target root, and keeps
	// the Overwrite answer. The retried copy therefore runs without a second
	// conflict question.
	fixture.click(t, "Retry")
	fixture.await(t, "the retried destination panel", func() bool {
		return fixture.tester.HasText("Start copy")
	})
	if !fixture.tester.HasText(inbox) {
		t.Fatalf("Retry lost the destination %q; texts: %q", inbox, fixture.tester.Texts())
	}
	fixture.click(t, "Start copy")
	fixture.await(t, "the retried copy", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if fixture.tester.HasText(`"report.txt" already exists in the destination.`) {
		t.Fatalf("Retry asked the conflict again; texts: %q", fixture.tester.Texts())
	}
	if got := readTreeFile(t, fixture.target.root, "inbox/report.txt"); got != "report" {
		t.Fatalf("retried copy wrote %q to the remembered destination, want the source content", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.target.root, "report.txt")); !os.IsNotExist(err) {
		t.Fatalf("Retry copied to the target root instead of %q", inbox)
	}
}

// TestFilesFollowToggleKeepsTheNextManualMove covers the toggle baseline: when
// Follow is turned on while the terminal already sits in the browser's
// directory, the next manual move must stick instead of being snapped back.
func TestFilesFollowToggleKeepsTheNextManualMove(t *testing.T) {
	fixture := newFilesFixture(t)
	handle := fixture.openSessionFiles(t)
	handle.setCWD(fixture.source.root)
	sub := filepath.Join(fixture.source.root, "sub")

	fixture.click(t, "Follow terminal directory")
	fixture.tester.Frame()

	fixture.click(t, "Open sub")
	fixture.await(t, "the manual navigation", func() bool {
		return fixture.tester.HasText(sub) ||
			hasExactText(fixture.tester.Texts(), fixture.source.root)
	})

	// A follow that did not record the terminal's matching directory would
	// treat this frame's unchanged terminal directory as a move and return the
	// browser to the root.
	pumpFrames(fixture.phoneFixture, 200*time.Millisecond)
	if !fixture.tester.HasText("beta.txt") || !fixture.tester.HasText(sub) {
		t.Fatalf("Follow moved the browser out of the manually chosen directory; texts: %q",
			fixture.tester.Texts())
	}
	if hasExactText(fixture.tester.Texts(), fixture.source.root) {
		t.Fatalf("Follow snapped the browser back to the terminal directory; texts: %q",
			fixture.tester.Texts())
	}
}

// TestFilesToolRequiresASavedHost is the entry-eligibility bullet for a
// Session's Host: the shared opener resolves a saved Host by ID, so a Quick
// Connect Host or a restored Host whose record was deleted must not offer a
// Files button that can only fail.
func TestFilesToolRequiresASavedHost(t *testing.T) {
	t.Run("saved Host keeps the tool", func(t *testing.T) {
		fixture := newFilesFixture(t)
		fixture.openSession(t, "prod box")
		fixture.openToolsPanel(t, "prod box")
		if !fixture.tester.HasText("Open Files") {
			t.Fatalf("a saved SSH Session lost its Files tool; texts: %q", fixture.tester.Texts())
		}
	})

	t.Run("Quick Connect Host has no tool", func(t *testing.T) {
		fixture := newQuickConnectFilesFixture(t)
		openQuickConnectForm(t, fixture)
		typeInto(t, fixture.tester, "Target", "deploy@example.com:2222")
		click(t, fixture.tester, "Connect")
		fixture.await(t, "the Quick Connect Session to attach", func() bool {
			handle := fixture.opener.last()
			return handle != nil && handle.isAttached()
		})

		fixture.openToolsPanel(t, "deploy@example.com")
		if !fixture.tester.HasText("Command history") {
			t.Fatalf("the Tools catalog did not open; texts: %q", fixture.tester.Texts())
		}
		if fixture.tester.HasText("Open Files") {
			t.Fatalf("an Unsaved Host offered a Files tool that cannot open; texts: %q",
				fixture.tester.Texts())
		}
	})

	t.Run("restored Host deleted from the Vault has no tool", func(t *testing.T) {
		gone := domainvault.Host{
			ID: "gone", Label: "gone box", Hostname: "gone.example.com", Port: 22,
			Username: "deploy", Protocol: domainvault.ProtocolSSH,
		}
		store := &fakeSessionStore{payload: storedTabs(gone)}
		opener := &fakeOpener{output: "restored output"}
		fileOpener := &fakeFileOpener{
			trees: map[model.HostID]apptransfer.FileSystem{},
			errs:  map[model.HostID]error{},
		}
		service := apptransfer.NewService(nil, fileOpener)
		fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
			WithSessionStore(store), WithFiles(service))
		fixture.selectSessions(t)
		fixture.await(t, "the restored Pane", func() bool {
			return opener.count() == 1
		})
		if err := fixture.tester.Click("Open session gone box"); err != nil {
			t.Fatalf("open the restored Session: %v; texts: %q", err, fixture.tester.Texts())
		}
		fixture.settle()
		fixture.await(t, "the restored terminal", func() bool {
			return fixture.tester.HasText("restored output")
		})

		fixture.openToolsPanel(t, "gone box")
		if !fixture.tester.HasText("Command history") {
			t.Fatalf("the Tools catalog did not open; texts: %q", fixture.tester.Texts())
		}
		if fixture.tester.HasText("Open Files") {
			t.Fatalf("a restored Host with no Vault record offered Files; texts: %q",
				fixture.tester.Texts())
		}
	})
}

// TestFilesTargetOpenRetryKeepsDestinationAndAnswers is the three-step path: a
// copy to a non-root destination fails while writing, the first Retry fails to
// reopen the target, and the target-error Retry must still resume the
// remembered destination and conflict answer instead of resetting to the root.
func TestFilesTargetOpenRetryKeepsDestinationAndAnswers(t *testing.T) {
	fixture := newFilesFixture(t)
	inbox := filepath.Join(fixture.target.root, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	writeTreeFile(t, fixture.target.root, "inbox/report.txt", "old")

	fixture.openHostFiles(t)
	fixture.click(t, "Select report.txt")
	fixture.click(t, "Copy to another Host")
	fixture.click(t, "Copy to target box")
	fixture.await(t, "the destination panel", func() bool {
		return fixture.tester.HasText("Start copy")
	})
	if err := fixture.tester.Click("Destination"); err != nil {
		t.Fatalf("focus the destination: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Key(ui.Cmd, ui.KeyA)
	fixture.tester.Type(inbox)
	fixture.tester.Frame()
	fixture.target.failCreates = 1
	fixture.click(t, "Start copy")
	fixture.await(t, "the conflict", func() bool {
		return fixture.tester.HasText(`"report.txt" already exists in the destination.`)
	})
	fixture.click(t, "Overwrite")
	fixture.await(t, "the copy failure", func() bool {
		return fixture.tester.HasText("Could not copy:")
	})
	// A failed copy leaves no destination transport open, so the destination
	// panel's action must not remain as a no-op button.
	if fixture.tester.HasText("Start copy") {
		t.Fatalf("the destination panel stayed open without a target transport; texts: %q",
			fixture.tester.Texts())
	}

	// Step 1: the copy-failure Retry reopens the target, which now fails.
	fixture.opener.setErr("target", errors.New("connection refused"))
	fixture.click(t, "Retry")
	fixture.await(t, "the target open failure", func() bool {
		return fixture.tester.HasText("Could not open the target Host:") &&
			fixture.tester.HasText("connection refused")
	})

	// Step 2: clear the failure and use the target-error Retry. It must reopen
	// the remembered destination, not the target root.
	fixture.opener.setErr("target", nil)
	fixture.click(t, "Retry")
	fixture.await(t, "the reopened destination panel", func() bool {
		return fixture.tester.HasText("Start copy")
	})
	if !fixture.tester.HasText(inbox) {
		t.Fatalf("the target-error Retry lost the destination %q; texts: %q",
			inbox, fixture.tester.Texts())
	}

	// Step 3: the remembered Overwrite answer lets the copy run without asking
	// the same conflict again.
	fixture.click(t, "Start copy")
	fixture.await(t, "the copied file", func() bool {
		return fixture.tester.HasText("Copied 1 file to target box.")
	})
	if fixture.tester.HasText(`"report.txt" already exists in the destination.`) {
		t.Fatalf("the target-error Retry discarded the conflict answer; texts: %q",
			fixture.tester.Texts())
	}
	if got := readTreeFile(t, fixture.target.root, "inbox/report.txt"); got != "report" {
		t.Fatalf("retried copy wrote %q to the remembered destination, want the source content", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.target.root, "report.txt")); !os.IsNotExist(err) {
		t.Fatalf("the copy landed in the target root instead of %q", inbox)
	}
}

// newQuickConnectFilesFixture builds the Sessions-only shell over a Quick
// Connect opener and the shared transfer service, so the Files eligibility can
// be observed on a Host with no Vault record.
func newQuickConnectFilesFixture(t *testing.T) *phoneFixture {
	t.Helper()
	fileOpener := &fakeFileOpener{
		trees: map[model.HostID]apptransfer.FileSystem{},
		errs:  map[model.HostID]error{},
	}
	service := apptransfer.NewService(nil, fileOpener)
	inner := &fakeOpener{output: "quick ready"}
	opener := &quickConnectOpener{fakeOpener: inner}
	store := &fakeHostStore{}
	settings := newFakeSettings()
	shell := New(
		WithHosts(store),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
		WithSettings(settings),
		WithFiles(service),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	fixture := &phoneFixture{
		shell: shell, store: store, opener: inner, settings: settings,
		context: context.Background(),
	}
	fixture.tester = ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	fixture.tester.Frame()
	return fixture
}
