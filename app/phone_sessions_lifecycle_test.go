package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	xssh "golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
	"github.com/mslxl/natcatty/ui/terminal"
)

// phoneShell is one interactive shell served by an in-process SSH server. The
// test writes output while its Session is hibernated, which is what proves the
// Session's replay, not the fake server, carries the scrollback back to a
// resumed terminal.
type phoneShell struct {
	started chan xssh.Channel
	once    sync.Once
	mu      sync.Mutex
	channel xssh.Channel
}

func newPhoneShell() *phoneShell {
	return &phoneShell{started: make(chan xssh.Channel, 1)}
}

// serve is the fakessh Shell handler: it announces the channel, writes a short
// banner, and then consumes input until the channel closes.
func (s *phoneShell) serve(channel xssh.Channel) {
	s.mu.Lock()
	s.channel = channel
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	_, _ = io.WriteString(channel, "remote shell ready\r\n")
	_, _ = io.Copy(io.Discard, channel)
}

// write sends output to the remote shell, as a running program would.
func (s *phoneShell) write(t *testing.T, text string) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the SSH server never opened a shell")
	}
	s.mu.Lock()
	channel := s.channel
	s.mu.Unlock()
	if _, err := io.WriteString(channel, text); err != nil {
		t.Fatalf("write to the remote shell: %v", err)
	}
}

// realPhoneFixture is the phone's composition root over real in-process SSH
// servers: the production Transport factory, Connection Manager, and Session
// service, with a fake only at the wire endpoint.
type realPhoneFixture struct {
	runtime *phoneSessionRuntime
	hosts   []domainvault.Host
	shells  []*phoneShell

	mu    sync.Mutex
	conns []net.Conn
}

// newRealPhoneFixture builds the phone root with enough distinct Hosts for the
// tests that open several Sessions at once. Distinct Hosts matter: the
// Connection Manager parks and reuses a transport, so two Sessions for the same
// Host would share one SSH connection instead of opening two.
func newRealPhoneFixture(t *testing.T) *realPhoneFixture {
	t.Helper()
	fixture := &realPhoneFixture{hosts: make([]domainvault.Host, 8)}
	known := make(map[model.HostID]domainvault.Host, len(fixture.hosts))
	for i := range fixture.hosts {
		fixture.hosts[i] = domainvault.Host{
			ID:       model.HostID(fmt.Sprintf("prod-%d", i+1)),
			Label:    fmt.Sprintf("prod box %d", i+1),
			Hostname: fmt.Sprintf("prod-%d.example.com", i+1),
			Port:     2222,
			Username: "deploy",
			Protocol: domainvault.ProtocolSSH,
		}
		known[fixture.hosts[i].ID] = fixture.hosts[i]
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		clientConn, serverConn := fakessh.Pipe()
		shell := newPhoneShell()
		if _, err := fakessh.Start(serverConn, fakessh.Options{
			Password: "s3cret",
			Shell:    shell.serve,
		}); err != nil {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, err
		}
		fixture.mu.Lock()
		fixture.conns = append(fixture.conns, clientConn)
		fixture.shells = append(fixture.shells, shell)
		fixture.mu.Unlock()
		return clientConn, nil
	}
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(dial),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	manager := appconnection.NewManager(
		dialer,
		vaultPasswords{vault: factoryPasswords{value: "s3cret"}},
		&factoryKnownHosts{},
		appconnection.WithPrompter(remoteCommandPrompter{}),
	)
	routes := vaultRoutes{vault: &fakeRouteVault{
		hosts:   known,
		secrets: map[string]string{},
	}}
	fixture.runtime = &phoneSessionRuntime{
		service:     appsession.NewService(newPhoneTransportFactory(manager, routes, nil, nil)),
		connections: manager,
	}
	t.Cleanup(func() {
		fixture.runtime.Close()
		fixture.mu.Lock()
		conns := append([]net.Conn(nil), fixture.conns...)
		fixture.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return fixture
}

// open opens Host n as the phone shell would: the returned handle has no
// terminal view attached.
func (f *realPhoneFixture) open(t *testing.T, n int) *phoneSessionHandle {
	t.Helper()
	if n >= len(f.hosts) {
		t.Fatalf("fixture has %d Hosts, asked for %d", len(f.hosts), n)
	}
	handle, err := f.runtime.OpenHost(context.Background(), f.hosts[n])
	if err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	real, ok := handle.(*phoneSessionHandle)
	if !ok {
		t.Fatalf("OpenHost returned %T, want the application handle", handle)
	}
	return real
}

// shell returns the fixture's n-th in-process SSH shell, in dial order.
func (f *realPhoneFixture) shell(t *testing.T, n int) *phoneShell {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.shells) {
		t.Fatalf("only %d SSH shells were started, want %d", len(f.shells), n+1)
	}
	return f.shells[n]
}

// newRealTerminal builds the production libghostty-vt terminal over a Session
// view. The host that runs this test provides the shared library; a machine
// without it skips, exactly as the phone terminal tests do.
func newRealTerminal(t *testing.T, conn io.ReadWriteCloser) *terminal.Terminal {
	t.Helper()
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	return term
}

// awaitTerminalText waits for the real terminal to render all of want.
func awaitTerminalText(t *testing.T, term *terminal.Terminal, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		text := term.Text()
		missing := ""
		for _, needle := range want {
			if !strings.Contains(text, needle) {
				missing = needle
				break
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal text = %q, missing %q", text, missing)
		}
		time.Sleep(time.Millisecond)
	}
}

// memorySample is one recorded measurement. The Go heap covers the app's own
// allocations; RSS also covers the terminal emulator's native allocation,
// which the Go runtime does not report.
type memorySample struct {
	alloc, inuse uint64
	rssKiB       int64
	haveRSS      bool
}

// sampleMemory reports the heap after two collections, so short-lived
// allocations from opening the Sessions are not counted as retained memory.
func sampleMemory() memorySample {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	sample := memorySample{alloc: stats.HeapAlloc, inuse: stats.HeapInuse}
	// ps reports the resident set in KiB on both macOS and Linux. A platform
	// without it still gets the Go heap numbers.
	if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output(); err == nil {
		if rss, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			sample.rssKiB, sample.haveRSS = rss, true
		}
	}
	return sample
}

// TestPhoneSessionLifecycleAgainstRealSSHServer drives the application Session
// through the phone's handle against a real SSH server in this process: attach,
// a refused second attach, hibernate with the connection alive, resume with the
// scrollback replayed, and an explicit close that ends the Session.
func TestPhoneSessionLifecycleAgainstRealSSHServer(t *testing.T) {
	fixture := newRealPhoneFixture(t)
	handle := fixture.open(t, 0)
	session := handle.session

	conn, err := handle.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got := session.LiveViews(); got != 1 {
		t.Fatalf("live views after Attach = %d, want 1", got)
	}
	if _, err := handle.Attach(); err == nil {
		t.Fatal("a second Attach claimed a second view")
	} else if !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("second Attach error = %v, want a refused view", err)
	}

	term := newRealTerminal(t, conn)
	awaitTerminalText(t, term, "remote shell ready")

	// Output produced while the Session is visible reaches its terminal.
	fixture.shell(t, 0).write(t, "before hibernate\r\n")
	awaitTerminalText(t, term, "before hibernate")

	// Leaving the page closes the terminal and hibernates the Session: the
	// transport and the state stay, but no live view does.
	if err := term.Close(); err != nil {
		t.Fatalf("closing the terminal: %v", err)
	}
	if got := session.LiveViews(); got != 0 {
		t.Fatalf("closing the terminal left %d live views, want none", got)
	}
	if !handle.Hibernate() {
		t.Fatal("Hibernate returned false for a running Session")
	}
	if got := session.LiveViews(); got != 0 {
		t.Fatalf("a hibernated Session kept %d live views", got)
	}
	if !session.Snapshot().Hibernated {
		t.Fatal("the Session does not report Hibernated")
	}

	// Output produced while hidden is retained by the Session's bounded replay,
	// not by the terminal (which is gone).
	fixture.shell(t, 0).write(t, "while hibernated\r\n")
	waitFor(t, "the hibernated output to be retained", func() bool {
		return session.Snapshot().Stats.OutputBytes >=
			int64(len("remote shell ready\r\nbefore hibernate\r\nwhile hibernated\r\n"))
	})

	// Returning resumes the same Session; a fresh terminal replays the whole
	// scrollback and then follows live output.
	resumed, err := handle.Resume()
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := session.LiveViews(); got != 1 {
		t.Fatalf("live views after Resume = %d, want 1", got)
	}
	resumedTerm := newRealTerminal(t, resumed)
	awaitTerminalText(t, resumedTerm, "before hibernate", "while hibernated")
	fixture.shell(t, 0).write(t, "after resume\r\n")
	awaitTerminalText(t, resumedTerm, "after resume")

	// Typing through the resumed view reaches the remote shell: the resumed
	// connection is a real live view, not a replay-only reader.
	if _, err := resumed.Write([]byte("echo resumed\r")); err != nil {
		t.Fatalf("writing through the resumed view: %v", err)
	}

	// Only an explicit close ends the Session. The phone's close path releases
	// the terminal first, then ends the Session.
	if got := session.LiveViews(); got != 1 {
		t.Fatalf("live views before the explicit close = %d, want 1", got)
	}
	if err := resumedTerm.Close(); err != nil {
		t.Fatalf("closing the resumed terminal: %v", err)
	}
	if err := handle.End(); err != nil {
		t.Fatalf("End: %v", err)
	}
	select {
	case <-handle.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the Session did not end after End")
	}
	if got := session.LiveViews(); got != 0 {
		t.Fatalf("an ended Session reports %d live views", got)
	}
}

// TestPhoneHoldsOneLiveViewAcrossSeveralRealSessions is the shell-level rule
// over real servers: every open Session exists, but only the resumed one owns
// a terminal view.
func TestPhoneHoldsOneLiveViewAcrossSeveralRealSessions(t *testing.T) {
	const sessions = 3
	fixture := newRealPhoneFixture(t)
	handles := make([]*phoneSessionHandle, 0, sessions)
	for i := 0; i < sessions; i++ {
		handles = append(handles, fixture.open(t, i))
	}

	// Show the first Session, then hide it and show the second, exactly as the
	// shell's one-place rule does.
	first, err := handles[0].Attach()
	if err != nil {
		t.Fatalf("Attach first: %v", err)
	}
	firstTerm := newRealTerminal(t, first)
	awaitTerminalText(t, firstTerm, "remote shell ready")
	_ = firstTerm.Close()
	if !handles[0].Hibernate() {
		t.Fatal("Hibernate on the first Session returned false")
	}
	second, err := handles[1].Attach()
	if err != nil {
		t.Fatalf("Attach second: %v", err)
	}
	secondTerm := newRealTerminal(t, second)
	t.Cleanup(func() { _ = secondTerm.Close() })
	awaitTerminalText(t, secondTerm, "remote shell ready")

	live := 0
	for _, handle := range handles {
		live += handle.session.LiveViews()
	}
	if live != 1 {
		t.Fatalf("%d Sessions hold a live view, want exactly 1", live)
	}
	for i, handle := range handles {
		if want := i == 1; (handle.session.LiveViews() == 1) != want {
			t.Fatalf("Session %d live views = %d, want attached=%v",
				i, handle.session.LiveViews(), want)
		}
	}
}

// TestPhoneHibernationMemoryWithSeveralSessions records the heap cost of the
// phone's lifecycle rather than relying on an impression of responsiveness:
// several real Sessions, each attached once to a real libghostty-vt terminal
// and then hibernated, with one resumed as the phone's single live view.
//
// The numbers are logged and recorded in docs/mobile-milestones.md. The
// assertions here check the structural claim (one live view, the rest
// hibernated) so a regression in the rule fails even on a noisy heap.
func TestPhoneHibernationMemoryWithSeveralSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("heap measurement is not meaningful in -short runs")
	}
	const sessions = 6
	baseline := sampleMemory()

	fixture := newRealPhoneFixture(t)
	handles := make([]*phoneSessionHandle, 0, sessions)
	for i := 0; i < sessions; i++ {
		handle := fixture.open(t, i)
		conn, err := handle.Attach()
		if err != nil {
			t.Fatalf("Attach session %d: %v", i, err)
		}
		term := newRealTerminal(t, conn)
		awaitTerminalText(t, term, "remote shell ready")
		// A page of output per Session, so each hibernated Session holds a
		// non-trivial replay buffer rather than an empty one.
		var page strings.Builder
		for line := 0; line < 500; line++ {
			fmt.Fprintf(&page, "session %d line %d\r\n", i, line)
		}
		fixture.shell(t, i).write(t, page.String())
		awaitTerminalText(t, term, fmt.Sprintf("session %d line 499", i))
		if err := term.Close(); err != nil {
			t.Fatalf("close session %d terminal: %v", i, err)
		}
		if !handle.Hibernate() {
			t.Fatalf("Hibernate session %d returned false", i)
		}
		handles = append(handles, handle)
	}

	// With every Session hibernated the phone holds no live view at all.
	hibernated := sampleMemory()
	liveViews := 0
	for i, handle := range handles {
		liveViews += handle.session.LiveViews()
		if !handle.session.Snapshot().Hibernated {
			t.Fatalf("Session %d is not hibernated", i)
		}
	}
	if liveViews != 0 {
		t.Fatalf("%d live views with every Session hibernated, want none", liveViews)
	}

	// One live view, as the phone's rule requires, costs one terminal screen.
	live, err := handles[0].Resume()
	if err != nil {
		t.Fatalf("Resume the live Session: %v", err)
	}
	liveTerm := newRealTerminal(t, live)
	awaitTerminalText(t, liveTerm, "session 0 line 499")

	liveSample := sampleMemory()
	liveViews = 0
	for _, handle := range handles {
		liveViews += handle.session.LiveViews()
	}
	if liveViews != 1 {
		t.Fatalf("%d live views with %d Sessions open, want 1", liveViews, sessions)
	}

	// Logged for the recorded measurement. Signed deltas are reported rather
	// than hidden, because a GC immediately after many allocations can leave
	// the heap smaller than the earlier baseline.
	t.Logf("phone hibernation memory: sessions=%d "+
		"heapAlloc baseline=%d B hibernated=%d B delta=%+d B perSession=%+d B "+
		"oneLive=%d B liveDelta=%+d B "+
		"heapInuse baseline=%d B hibernated=%d B delta=%+d B oneLive=%d B liveDelta=%+d B "+
		"rssKiB baseline=%d hibernated=%d oneLive=%d haveRSS=%v "+
		"go=%s os=%s",
		sessions,
		baseline.alloc, hibernated.alloc, int64(hibernated.alloc)-int64(baseline.alloc),
		(int64(hibernated.alloc)-int64(baseline.alloc))/sessions,
		liveSample.alloc, int64(liveSample.alloc)-int64(hibernated.alloc),
		baseline.inuse, hibernated.inuse, int64(hibernated.inuse)-int64(baseline.inuse),
		liveSample.inuse, int64(liveSample.inuse)-int64(hibernated.inuse),
		baseline.rssKiB, hibernated.rssKiB, liveSample.rssKiB, baseline.haveRSS && hibernated.haveRSS && liveSample.haveRSS,
		runtime.Version(), runtime.GOOS)
}

// phoneShellHostList is the Vault Host list the shell reads.
type phoneShellHostList struct{ hosts []domainvault.Host }

func (l phoneShellHostList) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), l.hosts...), nil
}

// The Hosts tab never lists or moves Host Groups; the Vault's Hosts family
// does, and its own tests drive those operations.
func (l phoneShellHostList) ListGroups(context.Context) ([]domainvault.Group, error) {
	return nil, nil
}

func (l phoneShellHostList) AssignHostsGroup(context.Context, []model.HostID, string) error {
	return nil
}

// phoneRecordingOpener keeps the handles the shell opened, so a test can ask the
// real Session service what the UI did.
type phoneRecordingOpener struct {
	inner   phoneshell.SessionOpener
	mu      sync.Mutex
	handles []*phoneSessionHandle
}

func (o *phoneRecordingOpener) OpenHost(ctx context.Context, host domainvault.Host) (phoneshell.SessionHandle, error) {
	handle, err := o.inner.OpenHost(ctx, host)
	if err != nil {
		return nil, err
	}
	real, ok := handle.(*phoneSessionHandle)
	if !ok {
		return nil, fmt.Errorf("the phone runtime returned %T", handle)
	}
	o.mu.Lock()
	o.handles = append(o.handles, real)
	o.mu.Unlock()
	return handle, nil
}

func (o *phoneRecordingOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.handles)
}

func (o *phoneRecordingOpener) at(n int) *phoneSessionHandle {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.handles[n]
}

// TestPhoneShellAppliesTheLifecycleToRealSessions runs the ticket's rule
// through the actual phone shell over real in-process SSH servers: a Host tap
// shows one Session, going back hibernates it while keeping the connection,
// returning resumes it, switching sections follows the same rule, and only the
// explicit close disconnects it. Closing the Tab's last Pane leaves an empty
// in-memory Tab with its new-session entry.
func TestPhoneShellAppliesTheLifecycleToRealSessions(t *testing.T) {
	fixture := newRealPhoneFixture(t)
	opener := &phoneRecordingOpener{inner: fixture.runtime}
	shell := phoneshell.New(
		phoneshell.WithHosts(phoneShellHostList{hosts: fixture.hosts[:2]}),
		phoneshell.WithSessionOpener(opener),
	)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(phoneshell.Hosts)
	tester := ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	tester.Frame()

	// Open the first Host: the shell pushes one Session page and attaches the
	// phone's one live view.
	if err := tester.Click("Open prod box 1"); err != nil {
		t.Fatalf("open Host: %v", err)
	}
	tester.Frame()
	settle := func() {
		tester.Advance(600 * time.Millisecond)
		tester.Frame()
	}
	await := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; texts: %q", what, tester.Texts())
			}
			tester.Frame()
			tester.Advance(20 * time.Millisecond)
			time.Sleep(time.Millisecond)
		}
		tester.Frame()
	}
	await("the first Session to attach", func() bool {
		return opener.count() == 1 && opener.at(0).session.LiveViews() == 1
	})
	await("the real terminal page", func() bool {
		return tester.HasText("Connected · deploy@prod-1.example.com:2222")
	})
	first := opener.at(0)

	// Back to the list: hibernated, not closed.
	settle()
	if err := tester.Click("Back"); err != nil {
		t.Fatalf("back: %v", err)
	}
	settle()
	await("the first Session to hibernate", func() bool {
		return first.session.LiveViews() == 0 && first.session.Snapshot().Hibernated
	})
	if !tester.HasText("Hibernated · deploy@prod-1.example.com:2222") {
		t.Fatalf("the list does not report the hibernated Session; texts: %q", tester.Texts())
	}

	// Return: the same Session resumes, without a new open.
	if err := tester.Click("Open session prod box 1"); err != nil {
		t.Fatalf("return to the Session: %v", err)
	}
	settle()
	await("the first Session to resume", func() bool {
		return first.session.LiveViews() == 1 && !first.session.Snapshot().Hibernated
	})
	if opener.count() != 1 {
		t.Fatalf("returning opened %d Sessions, want the original one", opener.count())
	}

	// A section switch hibernates it; selecting Sessions again resumes it
	// because the route still names it.
	settle()
	shell.Select(phoneshell.Hosts)
	tester.Frame()
	await("hibernation on a section switch", func() bool {
		return first.session.LiveViews() == 0
	})
	shell.Select(phoneshell.Sessions)
	tester.Frame()
	await("resume on returning to Sessions", func() bool {
		return first.session.LiveViews() == 1
	})
	select {
	case <-first.session.Done():
		t.Fatal("navigation ended the Session")
	default:
	}

	// Only the explicit close disconnects the Session; a live one asks first.
	settle()
	if err := tester.Click("Close session prod box 1"); err != nil {
		t.Fatalf("request close: %v", err)
	}
	tester.Frame()
	if !tester.HasText("Close prod box 1?") {
		t.Fatalf("the live Session did not ask for confirmation; texts: %q", tester.Texts())
	}
	if err := tester.Click("Close"); err != nil {
		t.Fatalf("confirm close: %v", err)
	}
	settle()
	await("the Session to end", func() bool {
		select {
		case <-first.session.Done():
			return true
		default:
			return false
		}
	})
	// The closed Pane was the Tab's last, so the Tab stays as an empty
	// in-memory Tab with its new-session entry rather than disappearing.
	if !tester.HasText("This Tab is empty.") || !tester.HasText("New session") {
		t.Fatalf("the closed Pane left no empty Tab; texts: %q", tester.Texts())
	}
	if err := tester.Click("Back to Sessions"); err != nil {
		t.Fatalf("back to the list: %v", err)
	}
	settle()
	if !tester.HasText("Empty · new session") {
		t.Fatalf("the empty Tab is not listed; texts: %q", tester.Texts())
	}
}
