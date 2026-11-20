package phoneshell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/terminal"
)

// fakeHostStore is the Vault's Host list, with an optional read failure.
type fakeHostStore struct {
	hosts []domainvault.Host
	err   error
}

func (s *fakeHostStore) List(context.Context) ([]domainvault.Host, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]domainvault.Host(nil), s.hosts...), nil
}

// fakeHandle is one phone Session. The handle owns the Session state and its
// one live view, as the real application Session does: Attach claims the view,
// Hibernate releases it without ending the Session, Resume claims it again,
// and only End ends the Session. The view itself is the handle, because a fake
// terminal screen only needs the output buffer and input recorder.
type fakeHandle struct {
	mu         sync.Mutex
	output     bytes.Buffer
	written    bytes.Buffer
	done       chan struct{}
	viewDone   chan struct{}
	attached   bool
	hibernated bool
	ended      bool
	// resumeErr, when set, makes Resume fail after the next Hibernate.
	resumeErr error
}

func newFakeHandle(output string) *fakeHandle {
	h := &fakeHandle{done: make(chan struct{}), viewDone: make(chan struct{})}
	h.output.WriteString(output)
	return h
}

// Read serves the queued output, as a Session view does, then blocks until the
// view detaches or the Session ends.
func (h *fakeHandle) Read(p []byte) (int, error) {
	h.mu.Lock()
	if h.output.Len() > 0 {
		n, err := h.output.Read(p)
		h.mu.Unlock()
		return n, err
	}
	viewDone, done := h.viewDone, h.done
	h.mu.Unlock()
	select {
	case <-viewDone:
		return 0, io.EOF
	case <-done:
		return 0, io.EOF
	}
}

func (h *fakeHandle) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.written.Write(p)
}

func (h *fakeHandle) Resize(int, int) error { return nil }

// Close detaches the view: it is what a terminal screen's Close reaches.
func (h *fakeHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attached {
		h.attached = false
		close(h.viewDone)
	}
	return nil
}

func (h *fakeHandle) Attach() (io.ReadWriteCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attached {
		return nil, errors.New("fake handle: a view is already attached")
	}
	if h.ended {
		return nil, io.EOF
	}
	h.attachLocked()
	return h, nil
}

func (h *fakeHandle) Hibernate() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ended {
		return false
	}
	if h.attached {
		h.attached = false
		close(h.viewDone)
	}
	h.hibernated = true
	return true
}

func (h *fakeHandle) Resume() (io.ReadWriteCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hibernated {
		return nil, errors.New("fake handle: not hibernated")
	}
	if h.resumeErr != nil {
		err := h.resumeErr
		h.resumeErr = nil
		return nil, err
	}
	h.hibernated = false
	h.attachLocked()
	return h, nil
}

// attachLocked claims the view. The caller holds h.mu.
func (h *fakeHandle) attachLocked() {
	h.attached = true
	h.viewDone = make(chan struct{})
}

func (h *fakeHandle) Done() <-chan struct{} { return h.done }

func (h *fakeHandle) End() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ended {
		h.ended = true
		if h.attached {
			h.attached = false
			close(h.viewDone)
		}
		close(h.done)
	}
	return nil
}

func (h *fakeHandle) input() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.written.String()
}

func (h *fakeHandle) rendered() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.output.String()
}

func (h *fakeHandle) isAttached() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attached
}

func (h *fakeHandle) isEnded() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ended
}

// failNextResume makes the next Resume fail, modelling a transport that ended
// while its Session was hibernated.
func (h *fakeHandle) failNextResume(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resumeErr = err
}

// fakeOpener records the Hosts it was asked to open.
type fakeOpener struct {
	mu      sync.Mutex
	err     error
	output  string
	hosts   []domainvault.Host
	handles []*fakeHandle
}

func (o *fakeOpener) OpenHost(_ context.Context, host domainvault.Host) (SessionHandle, error) {
	o.mu.Lock()
	o.hosts = append(o.hosts, host)
	err := o.err
	output := o.output
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	handle := newFakeHandle(output)
	o.mu.Lock()
	o.handles = append(o.handles, handle)
	o.mu.Unlock()
	return handle, nil
}

func (o *fakeOpener) last() *fakeHandle {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.handles) == 0 {
		return nil
	}
	return o.handles[len(o.handles)-1]
}

func (o *fakeOpener) opened() []domainvault.Host {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]domainvault.Host(nil), o.hosts...)
}

// liveViews sums the fake Sessions' attached views: the phone holds at most
// one across every Tab.
func (o *fakeOpener) liveViews() int {
	o.mu.Lock()
	handles := append([]*fakeHandle(nil), o.handles...)
	o.mu.Unlock()
	live := 0
	for _, handle := range handles {
		if handle.isAttached() {
			live++
		}
	}
	return live
}

func (o *fakeOpener) ended() int {
	o.mu.Lock()
	handles := append([]*fakeHandle(nil), o.handles...)
	o.mu.Unlock()
	ended := 0
	for _, handle := range handles {
		if handle.isEnded() {
			ended++
		}
	}
	return ended
}

func (o *fakeOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.handles)
}

// setErr changes the failure the next OpenHost reports, so a test can make a
// restore fail and then succeed without rebuilding the shell.
func (o *fakeOpener) setErr(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
}

// fakeScreen shows the handle's output and forwards typed text to it. It is
// the test's stand-in for libghostty-vt.
type fakeScreen struct {
	handle *fakeHandle
}

func (s *fakeScreen) View(c *ui.Context) ui.Element {
	column := ui.Column(c).Fill().Gap(4).Padding(8)
	column.Children(func() {
		ui.Text(c, s.handle.rendered())
		ui.Box(c).Fill().HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputText || ev.Text == "" {
				return false
			}
			_, _ = s.handle.Write([]byte(ev.Text))
			return true
		}).AutoFocus()
	})
	// Output can arrive between frames; repaint while the Session is open.
	c.After(10 * time.Millisecond)
	return column
}

func (s *fakeScreen) Close() error { return s.handle.Close() }

func fakeScreens(conn io.ReadWriteCloser) (TerminalScreen, error) {
	handle, ok := conn.(*fakeHandle)
	if !ok {
		return nil, errors.New("fake screen: not a fake handle")
	}
	return &fakeScreen{handle: handle}, nil
}

// phoneFixture is one shell over a fake store, opener, screen, and settings.
type phoneFixture struct {
	shell    *Shell
	store    *fakeHostStore
	opener   *fakeOpener
	settings *fakeSettings
	tester   *ui.Tester
	hosts    []domainvault.Host
	context  context.Context
}

func newPhoneFixture(t *testing.T, hosts []domainvault.Host, opener *fakeOpener) *phoneFixture {
	t.Helper()
	return newPhoneFixtureWith(t, hosts, opener)
}

// newPhoneFixtureWith is newPhoneFixture with extra options, so a test can
// wire persistence or anything else the app root supplies.
func newPhoneFixtureWith(t *testing.T, hosts []domainvault.Host, opener *fakeOpener, opts ...Option) *phoneFixture {
	t.Helper()
	store := &fakeHostStore{hosts: hosts}
	settings := newFakeSettings()
	options := []Option{
		WithHosts(store),
		WithSessionOpener(opener),
		WithScreenFactory(fakeScreens),
		WithSettings(settings),
	}
	options = append(options, opts...)
	shell := New(options...)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	fixture := &phoneFixture{
		shell: shell, store: store, opener: opener, settings: settings,
		hosts: hosts, context: context.Background(),
	}
	// The tab bar opens on Sessions in the app; start a Hosts test there.
	shell.Select(Hosts)
	// The tester draws whichever section the shell has selected, so a Host tap
	// that switches to Sessions is visible on the next frame.
	fixture.tester = ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	fixture.tester.Frame()
	return fixture
}

// settle finishes a router's slide-in transition so the new page's elements
// have their final boxes before a test clicks them.
func (f *phoneFixture) settle() {
	f.tester.Advance(600 * time.Millisecond)
	f.tester.Frame()
}

// await polls frames until cond holds, with a hard deadline.
func (f *phoneFixture) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts: %q", what, f.tester.Texts())
		}
		f.tester.Frame()
		f.tester.Advance(20 * time.Millisecond)
		// The open runs on a goroutine, so the loop must also yield real time.
		time.Sleep(time.Millisecond)
	}
	f.tester.Frame()
}

func phoneHosts() []domainvault.Host {
	return []domainvault.Host{
		{
			ID: "prod", Label: "prod box", Hostname: "prod.example.com", Port: 2222,
			Username: "deploy", Protocol: domainvault.ProtocolSSH,
		},
		{
			ID: "console", Label: "console", Hostname: "console.example.com", Port: 23,
			Protocol: domainvault.ProtocolTelnet,
		},
		{
			ID: "udp", Label: "udp box", Hostname: "udp.example.com", Port: 22,
			Protocol: domainvault.ProtocolMosh,
		},
		{
			ID: "local", Label: "desktop shell", Hostname: "localhost", Protocol: domainvault.ProtocolLocal,
		},
		{
			ID: "serial", Label: "serial console", Hostname: "/dev/tty.usbserial",
			Protocol: domainvault.Protocol("serial"),
		},
	}
}

func TestPhoneHostsListOnlyRemoteHosts(t *testing.T) {
	opener := &fakeOpener{}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	if got := fixture.shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("Hosts path = %q, want /", got)
	}
	fixture.await(t, "the Hosts list", func() bool {
		return fixture.tester.HasText("prod box") && fixture.tester.HasText("console")
	})
	if !fixture.tester.HasText("udp box") {
		t.Errorf("a remote Mosh Host is missing; texts: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("desktop shell") || fixture.tester.HasText("serial console") {
		t.Errorf("a local or serial Host appeared on the phone: %q", fixture.tester.Texts())
	}
	// The detail line must distinguish two Hosts: account, endpoint, protocol.
	if !fixture.tester.HasText("deploy@prod.example.com:2222") {
		t.Errorf("SSH row lacks its endpoint; texts: %q", fixture.tester.Texts())
	}
	if !fixture.tester.HasText("console.example.com:23 · telnet") {
		t.Errorf("Telnet row lacks its protocol; texts: %q", fixture.tester.Texts())
	}

	// The filter is the list's, not the store's: the desktop still sees every
	// Host, and the phone never loads the local ones into a row.
	if len(fixture.store.hosts) != len(phoneHosts()) {
		t.Fatalf("store lost Hosts: %d", len(fixture.store.hosts))
	}
}

func TestPhoneHostTapOpensTabAndSwitchesToSessions(t *testing.T) {
	opener := &fakeOpener{output: "hello from remote\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	if err := fixture.tester.Click("Open prod box"); err != nil {
		t.Fatalf("click Host row: %v", err)
	}
	fixture.tester.Frame()
	if got := fixture.shell.Selected(); got != Sessions {
		t.Fatalf("selected section = %v, want Sessions", got)
	}
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("Sessions path = %q, want the pushed terminal page", got)
	}
	// The Hosts section keeps its own stack at the page the user left.
	if got := fixture.shell.Router(Hosts).Path(); got != "/" {
		t.Fatalf("Hosts path = %q, want its root untouched", got)
	}
	fixture.await(t, "the terminal output", func() bool {
		return fixture.tester.HasText("hello from remote")
	})
}

func TestPhoneConnectKeepsTheHostsStackWhereItWas(t *testing.T) {
	opener := &fakeOpener{output: "ready\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	// Leave Hosts on a detail page, then connect from it.
	fixture.shell.Router(Hosts).Push("/host/prod")
	fixture.tester.Frame()
	fixture.settle()
	if !fixture.tester.HasText("Endpoint") {
		t.Fatalf("detail page did not render; texts: %q", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Connect to prod box"); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	fixture.tester.Frame()
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod" {
		t.Fatalf("Hosts path = %q, want /host/prod", got)
	}
	if got := fixture.shell.Selected(); got != Sessions {
		t.Fatalf("selected = %v, want Sessions", got)
	}

	// Selecting Hosts again shows the page the user left, not its root.
	fixture.shell.Select(Hosts)
	fixture.tester.Frame()
	if got := fixture.shell.Router(Hosts).Path(); got != "/host/prod" {
		t.Fatalf("Hosts path after reselect = %q, want /host/prod", got)
	}
	if !fixture.tester.HasText("Endpoint") {
		t.Errorf("Hosts did not return to its detail page; texts: %q", fixture.tester.Texts())
	}
}

func TestPhoneTypingReachesTheSession(t *testing.T) {
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	if err := fixture.tester.Click("Open prod box"); err != nil {
		t.Fatalf("click Host row: %v", err)
	}
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})

	// The screen focuses its input; typing goes through it to the Session.
	fixture.tester.Type("ls -la\r")
	fixture.tester.Frame()
	handle := opener.last()
	if handle == nil {
		t.Fatal("no Session handle was opened")
	}
	if got := handle.input(); got != "ls -la\r" {
		t.Fatalf("Session received %q, want the typed line", got)
	}
}

func TestPhoneFailedConnectionShowsErrorAndLeavesNoTab(t *testing.T) {
	opener := &fakeOpener{err: errors.New("dial tcp: connection refused")}
	fixture := newPhoneFixture(t, phoneHosts(), opener)

	if err := fixture.tester.Click("Open prod box"); err != nil {
		t.Fatalf("click Host row: %v", err)
	}
	fixture.await(t, "the connection error", func() bool {
		return fixture.tester.HasText("Could not connect to prod box")
	})
	if !fixture.tester.HasText("Check the Host's address") {
		t.Errorf("error is not actionable; texts: %q", fixture.tester.Texts())
	}
	// The pushed page is the error page, but the Tab is not an open Session.
	if got := fixture.shell.Router(Sessions).Path(); got != "/session/tab-1" {
		t.Fatalf("Sessions path = %q, want the error page", got)
	}
	if tabs := fixture.shell.sessions.list(); len(tabs) != 0 {
		t.Fatalf("failed connection left %d Tab(s): %+v", len(tabs), tabs)
	}
	fixture.settle()

	if err := fixture.tester.Click("Back to Sessions"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	fixture.tester.Frame()
	if got := fixture.shell.Router(Sessions).Path(); got != "/" {
		t.Fatalf("Sessions path = %q, want /", got)
	}
	if !fixture.tester.HasText("No sessions yet") {
		t.Errorf("Sessions root still lists a failed Tab; texts: %q", fixture.tester.Texts())
	}
}

// TestPhoneTerminalDrawsFullScreenProgram exercises the real libghostty-vt
// screen when this test binary can load it: a pager/editor paints its alternate
// screen through the Session handle's Read, and the terminal holds the text.
// Ordinary `go test` runs on a machine without the library skip it; the
// fake-screen tests cover the wiring.
func TestPhoneTerminalDrawsFullScreenProgram(t *testing.T) {
	// A pager/editor's alternate screen: clear, cursor home, paint.
	handle := newFakeHandle("\x1b[?1049h\x1b[2J\x1b[HEDITOR LINE ONE\r\nEDITOR LINE TWO")
	term, err := terminal.New(terminal.Options{Conn: handle})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })

	// The terminal reads output from the handle in the background; wait on the
	// observable text, not on a fixed delay.
	deadline := time.Now().Add(5 * time.Second)
	for {
		text := term.Text()
		if strings.Contains(text, "EDITOR LINE ONE") && strings.Contains(text, "EDITOR LINE TWO") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal text = %q, want both editor lines", text)
		}
		time.Sleep(time.Millisecond)
	}

	screen := &ghosttyScreen{term: term}
	screenView := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	screenView.Frame()
	// The terminal paints cells, not text nodes, so drawing without a panic is
	// the frame's assertion; the text extraction above is authoritative.
	if len(screenView.Texts()) == 0 {
		t.Logf("terminal frame has no text nodes, as expected for cell painting")
	}
}
