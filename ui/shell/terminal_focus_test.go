package shell

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// typingTransport is a live Session transport that records what the terminal
// sent. It stays open until the Session closes, so typed input is observable
// without the Session exiting first.
type typingTransport struct {
	mu      sync.Mutex
	written bytes.Buffer
	output  chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newTypingTransport() *typingTransport {
	return &typingTransport{
		output: make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

func (t *typingTransport) Read(p []byte) (int, error) {
	select {
	case data := <-t.output:
		n := copy(p, data)
		if n < len(data) {
			select {
			case t.output <- data[n:]:
			case <-t.closed:
				return n, nil
			}
		}
		return n, nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *typingTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.Write(p)
}

func (t *typingTransport) Resize(int, int) error { return nil }

func (t *typingTransport) Wait() (int, error) { <-t.closed; return 0, io.EOF }

func (t *typingTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *typingTransport) input() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.String()
}

func (t *typingTransport) emit(text string) {
	select {
	case t.output <- []byte(text):
	case <-t.closed:
	}
}

// typingFactory hands out one recording transport per opened Session, in the
// order the Session service asks for it.
type typingFactory struct {
	mu         sync.Mutex
	transports []*typingTransport
}

func (f *typingFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	transport := newTypingTransport()
	f.transports = append(f.transports, transport)
	return transport, nil
}

func (f *typingFactory) at(index int) *typingTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[index]
}

// newTypingState builds a shell whose Panes hold the production terminal
// surface, so a test types through the same focus path as the app.
func newTypingState(t *testing.T, factory *typingFactory, extra ...state.Option) *state.State {
	t.Helper()
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	options := []state.Option{state.WithSessions(service, func(s *appsession.Session) (state.Surface, error) {
		return sessionui.New(s,
			sessionui.WithRedraw(func() {}),
			sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
			sessionui.WithDropAction(func() domainterminal.DropAction {
				return domainterminal.DropInsert
			}),
			sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
		)
	})}
	return state.NewState(append(options, extra...)...)
}

// eventuallyInput waits for input to reach transport, refreshing frames while
// it does. The deadline is the failure bound; the loop checks the real
// recorded bytes on every pass.
func eventuallyInput(t *testing.T, tester *ui.Tester, transport *typingTransport, want string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(transport.input(), want) {
			return true
		}
		tester.Frame()
		time.Sleep(time.Millisecond)
	}
	return strings.Contains(transport.input(), want)
}

// TestNewTabTerminalTakesTheKeyboardWithoutAClick is the regression for the
// reported bug: a fresh local Tab showed a terminal that received no bytes
// until the user clicked it. A new Tab's focused terminal must hold the
// keyboard from the first frame.
func TestNewTabTerminalTakesTheKeyboardWithoutAClick(t *testing.T) {
	factory := &typingFactory{}
	st := newTypingState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	// No click and no explicit focus request: the terminal already has the
	// keyboard.
	tester.TypeKey(0, ui.KeyE, "echo hello")
	if !eventuallyInput(t, tester, factory.at(0), "echo hello") {
		t.Fatalf("typed input never reached the new Tab's Session: %q", factory.at(0).input())
	}
}

// TestComposeKeepsTheKeyboardFromTheTerminal proves the terminal never
// reclaims the keyboard on a steady frame: once the user focuses the Compose
// draft, typed text edits the draft instead of reaching the Session.
func TestComposeKeepsTheKeyboardFromTheTerminal(t *testing.T) {
	factory := &typingFactory{}
	st := newTypingState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)
	st.OpenCompose()

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(locales.In("en").ComposePlaceholder()); err != nil {
		t.Fatalf("focus the compose draft: %v", err)
	}
	tester.Type("a draft")
	tester.Frame()

	if got := *st.ComposeDraft(); got != "a draft" {
		t.Fatalf("compose draft = %q, want %q", got, "a draft")
	}
	if got := factory.at(0).input(); got != "" {
		t.Fatalf("the terminal stole the keyboard from Compose: %q", got)
	}
}

// TestTabSwitchRestoresTheTerminalKeyboard proves a Terminal re-shown by a
// Tab switch takes the keyboard again, as the original called term.focus()
// when the visible Tab changed.
func TestTabSwitchRestoresTheTerminalKeyboard(t *testing.T) {
	factory := &typingFactory{}
	st := newTypingState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	tester.Type("one")
	if !eventuallyInput(t, tester, factory.at(0), "one") {
		t.Fatalf("the first Tab did not take the keyboard: %q", factory.at(0).input())
	}

	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 2)
	tester.Frame()
	tester.Type("two")
	if !eventuallyInput(t, tester, factory.at(1), "two") {
		t.Fatalf("the new Tab did not take the keyboard: %q", factory.at(1).input())
	}

	// Going back to a Tab whose terminal was already shown and then hidden
	// must not need a click either.
	st.SelectTab(0)
	tester.Frame()
	tester.Type("back")
	if !eventuallyInput(t, tester, factory.at(0), "back") {
		t.Fatalf("the re-shown first Tab did not take the keyboard: %q", factory.at(0).input())
	}
	if strings.Contains(factory.at(1).input(), "back") {
		t.Fatalf("input for the re-shown first Tab reached the second Tab: %q", factory.at(1).input())
	}
}

// TestTerminalInputSurvivesDelayedOutputAndTabSwitches reproduces the exact
// timing reported by the desktop app: a new Local Terminal is usable for a
// moment, asynchronous shell/Lens output arrives and causes redraws, a second
// Terminal is opened, and input is tried before and after switching between
// the two Tabs. Every keystroke must still reach the active Session.
func TestTerminalInputSurvivesDelayedOutputAndTabSwitches(t *testing.T) {
	factory := &typingFactory{}
	st := newTypingState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)

	tester := ui.NewTester(View(st), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	tester.TypeKey(0, ui.KeyO, "one")
	if !eventuallyInput(t, tester, factory.at(0), "one") {
		t.Fatalf("first Terminal did not accept initial input: %q", factory.at(0).input())
	}

	// Simulate the shell's prompt/output and the Lens protocol arriving
	// after the first frame, while the user leaves the Tab untouched.
	factory.at(0).emit("\x1b]1337;NetcattyExec;powershell;Qzpcd29yaw;ZWNobyBvbmU\x07")
	factory.at(0).emit("\x1b]133;C\x07one output\r\n\x1b]133;D;0\x07")
	pumpFrames(tester, 500*time.Millisecond)
	tester.TypeKey(0, ui.KeyT, "two")
	if !eventuallyInput(t, tester, factory.at(0), "onetwo") {
		t.Fatalf("Terminal 1 stopped accepting input after delayed Lens output: %q", factory.at(0).input())
	}

	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 2)
	tester.Frame()
	tester.TypeKey(0, ui.KeyN, "new")
	if !eventuallyInput(t, tester, factory.at(1), "new") {
		t.Fatalf("new Terminal stopped accepting its first input: %q", factory.at(1).input())
	}

	// The second terminal gets output a little later, which is the window in
	// which the reported bug makes its input stop working.
	factory.at(1).emit("\x1b]1337;NetcattyExec;powershell;Qzpcd29yaw;ZWNobyBuZXc=\x07")
	factory.at(1).emit("\x1b]133;C\x07new output\r\n\x1b]133;D;0\x07")
	pumpFrames(tester, 750*time.Millisecond)
	tester.TypeKey(0, ui.KeyE, "again")
	if !eventuallyInput(t, tester, factory.at(1), "newagain") {
		t.Fatalf("new Terminal stopped accepting input after delayed output: %q", factory.at(1).input())
	}

	st.SelectTab(0)
	tester.Frame()
	tester.TypeKey(0, ui.KeyB, "back")
	if !eventuallyInput(t, tester, factory.at(0), "onetwoback") {
		t.Fatalf("old Terminal stopped accepting input after switching back: %q", factory.at(0).input())
	}

	st.SelectTab(1)
	tester.Frame()
	tester.TypeKey(0, ui.KeyF, "final")
	if !eventuallyInput(t, tester, factory.at(1), "newagainfinal") {
		t.Fatalf("new Terminal stopped accepting input after switching forward: %q", factory.at(1).input())
	}
}

func pumpFrames(tester *ui.Tester, duration time.Duration) {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		tester.Frame()
		time.Sleep(time.Millisecond)
	}
}

// TestOnlyTheFocusedPaneTakesTheKeyboardWithoutAClick proves a split's
// non-focused Pane does not steal the keyboard from the focused one. The
// focused terminal is built first and the other last, so an unconditional
// focus on every terminal would end with the wrong Pane focused.
func TestOnlyTheFocusedPaneTakesTheKeyboardWithoutAClick(t *testing.T) {
	factory := &typingFactory{}
	st := newTypingState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)
	if err := st.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 2)

	root, _, ok := st.TabTree(0)
	if !ok {
		t.Fatal("the split Tab has no tree")
	}
	panes := domainworkspace.CollectPaneIDs(root)
	if len(panes) != 2 {
		t.Fatalf("the split Tab has %d Panes, want 2", len(panes))
	}
	// Split focuses the new right Pane. Focus the left one so its terminal is
	// built before the non-focused right terminal.
	if !st.FocusPaneID(0, panes[0]) {
		t.Fatal("FocusPaneID returned false")
	}

	tester := ui.NewTester(View(st), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	tester.Type("focused")
	if !eventuallyInput(t, tester, factory.at(0), "focused") {
		t.Fatalf("the focused Pane did not take the keyboard: left %q right %q",
			factory.at(0).input(), factory.at(1).input())
	}
	if got := factory.at(1).input(); got != "" {
		t.Fatalf("the non-focused Pane stole input: %q", got)
	}
}
