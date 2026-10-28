package sessionui

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
)

// scriptedTransport is a real transport for a headless Session. Output is
// queued by the test and consumed by the Session's reader; input is recorded
// so a test can prove the transport stayed alive while the screen was
// hibernated.
type scriptedTransport struct {
	output  chan []byte
	closed  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	pending []byte
	written []byte
	cols    int
	rows    int
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{
		output: make(chan []byte, 16),
		closed: make(chan struct{}),
	}
}

func (t *scriptedTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.written = append(t.written, p...)
	t.mu.Unlock()
	return len(p), nil
}

func (t *scriptedTransport) Input() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.written)
}

func (t *scriptedTransport) Read(p []byte) (int, error) {
	select {
	case data := <-t.output:
		n := copy(p, data)
		if n < len(data) {
			// Keep the remainder for the next read rather than dropping it.
			t.output <- data[n:]
		}
		return n, nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *scriptedTransport) Resize(cols, rows int) error {
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.mu.Unlock()
	return nil
}

func (t *scriptedTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *scriptedTransport) emit(text string) {
	select {
	case t.output <- []byte(text):
	case <-t.closed:
	}
}

type scriptedFactory struct{ transport *scriptedTransport }

func (f scriptedFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func newScriptedSurface(t *testing.T) (*Surface, *scriptedTransport) {
	t.Helper()
	transport := newScriptedTransport()
	service := appsession.NewService(scriptedFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	surface.SetPaneActions(PaneActions{
		Title: "Local Session 1", MenuLabel: locales.In("en").SessionMenuFor(1),
	})
	return surface, transport
}

func surfaceTester(surface *Surface) *ui.Tester {
	tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 600)
	tester.SetLocale("en")
	return tester
}

func waitScreenContains(t *testing.T, surface *Surface, want string) {
	t.Helper()
	eventually(t, func() bool { return strings.Contains(surface.ScreenText(), want) })
}

// settledSessionSnapshot pumps frames until the viewport resize that MyGo's
// terminal started asynchronously has reached the Session.
//
// MyGo's terminal calls Conn.Resize from a goroutine, so a snapshot taken
// after a fixed number of frames can race that first resize and record a
// stale size. Hibernation then closes that terminal and Resume attaches a
// new one that applies the real size, which makes a post-resume comparison
// look like a legitimate state change. Waiting for the transport's last
// recorded size to match the Session snapshot on two consecutive frames
// gives the test a stable baseline.
func settledSessionSnapshot(t *testing.T, surface *Surface, transport *scriptedTransport, tester *ui.Tester) appsession.Snapshot {
	t.Helper()
	var (
		settled            appsession.Snapshot
		lastCols, lastRows int
	)
	eventually(t, func() bool {
		transport.mu.Lock()
		cols, rows := transport.cols, transport.rows
		transport.mu.Unlock()

		tester.Frame()
		snapshot := surface.Session().Snapshot()

		stable := cols == lastCols && rows == lastRows
		lastCols, lastRows = cols, rows
		settled = snapshot

		return cols > 0 && rows > 0 &&
			snapshot.Cols == cols && snapshot.Rows == rows && stable
	})
	return settled
}

func TestHibernateAndResumeKeepTheScreenAndSessionState(t *testing.T) {
	m := locales.In("en")
	surface, transport := newScriptedSurface(t)
	transport.emit("owner line one\r\n")
	tester := surfaceTester(surface)
	tester.Frame()
	waitScreenContains(t, surface, "owner line one")
	tester.Frame()

	before := settledSessionSnapshot(t, surface, transport, tester)
	beforeText := surface.ScreenText()
	if !strings.Contains(beforeText, "owner line one") {
		t.Fatalf("owner screen lacks its output: %q", beforeText)
	}

	if err := tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuHibernate()); err != nil {
		t.Fatalf("Hibernate: %v", err)
	}
	tester.Frame()

	if !surface.Hibernated() {
		t.Fatal("the surface did not hibernate")
	}
	if surface.Session().LiveViews() != 0 {
		t.Fatalf("a view stayed attached while hibernated: %d", surface.Session().LiveViews())
	}
	if surface.ScreenText() != "" {
		t.Fatalf("a hibernated surface still renders a screen: %q", surface.ScreenText())
	}
	hibernated := surface.Session().Snapshot()
	if !hibernated.Hibernated {
		t.Fatal("the Session snapshot does not report hibernation")
	}
	if hibernated.ID != before.ID || hibernated.Kind != before.Kind ||
		hibernated.Status != before.Status || hibernated.Connection != before.Connection {
		t.Fatalf("hibernation changed Session state: before %+v, after %+v", before, hibernated)
	}
	if !tester.HasText(m.SessionHibernatedTitle()) {
		t.Fatalf("the hibernated placeholder is not shown: %v", tester.Texts())
	}

	// The transport is alive while hibernated: input still reaches it and
	// output still enters the Session's replay.
	if _, err := surface.Session().TerminalConn().Write([]byte("while hibernated\n")); err != nil {
		t.Fatalf("write while hibernated: %v", err)
	}
	if !waitFor(t, func() bool {
		return strings.Contains(transport.Input(), "while hibernated")
	}) {
		t.Fatalf("input did not reach the transport while hibernated: %q", transport.Input())
	}
	transport.emit("while hibernated\r\n")

	if err := tester.Click(m.SessionResume()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	tester.Frame()
	eventually(t, func() bool {
		return strings.Contains(surface.ScreenText(), "owner line one") &&
			strings.Contains(surface.ScreenText(), "while hibernated")
	})

	if surface.Hibernated() {
		t.Fatal("Resume left the surface hibernated")
	}
	if surface.Session().LiveViews() != 1 {
		t.Fatalf("Resume left %d live views, want 1", surface.Session().LiveViews())
	}
	after := surface.Session().Snapshot()
	if after.Hibernated {
		t.Fatal("the Session still reports hibernation after Resume")
	}
	if after.ID != before.ID || after.Kind != before.Kind ||
		after.Status != before.Status || after.Connection != before.Connection ||
		after.Cols != before.Cols || after.Rows != before.Rows ||
		after.Encoding != before.Encoding {
		t.Fatalf("Resume changed Session state: before %+v, after %+v", before, after)
	}

	// Live output continues on the resumed view.
	transport.emit("after resume\r\n")
	eventually(t, func() bool { return strings.Contains(surface.ScreenText(), "after resume") })
}

// TestResumedTerminalTakesTheKeyboardWithoutAClick proves Resume gives the
// keyboard back to the terminal, as a user who hibernated and resumed expects
// to keep typing without clicking. The focused Pane's terminal is the only
// one asking for the keyboard.
func TestResumedTerminalTakesTheKeyboardWithoutAClick(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	tester.Type("before")
	eventually(t, func() bool { return strings.Contains(transport.Input(), "before") })

	if !surface.Hibernate() {
		t.Fatal("Hibernate returned false")
	}
	tester.Frame()
	if err := surface.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	tester.Frame()

	// No click after Resume: the rebuilt terminal must hold the keyboard.
	tester.Type("after")
	if !waitFor(t, func() bool { return strings.Contains(transport.Input(), "after") }) {
		t.Fatalf("input after Resume never reached the transport: %q", transport.Input())
	}
}

func TestObserverRendersTheOwningScreensSnapshotAndFeed(t *testing.T) {
	m := locales.In("en")
	surface, transport := newScriptedSurface(t)
	transport.emit("owner line one\r\nowner line two\r\n")
	tester := surfaceTester(surface)
	tester.Frame()
	waitScreenContains(t, surface, "owner line two")
	tester.Frame()

	owner := surface.Session().Snapshot()
	ownerText := surface.ScreenText()

	if err := tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuObserveSnapshot()); err != nil {
		t.Fatalf("Observer snapshot: %v", err)
	}
	tester.Frame()

	if !surface.Observing() {
		t.Fatalf("the observer did not open: %v", tester.Texts())
	}
	if got := surface.Session().LiveViews(); got != 1 {
		t.Fatalf("the observer claimed a live view: %d live views", got)
	}
	screen, ok := surface.ObserverScreen()
	if !ok {
		t.Fatal("the observer has no snapshot")
	}
	if screen.SessionID != owner.ID {
		t.Fatalf("snapshot Session %q, want the owner %q", screen.SessionID, owner.ID)
	}
	if screen.Cols != owner.Cols || screen.Rows != owner.Rows {
		t.Fatalf("snapshot size %dx%d, want the owner's %dx%d",
			screen.Cols, screen.Rows, owner.Cols, owner.Rows)
	}
	if !strings.Contains(string(screen.Data), "owner line two") {
		t.Fatalf("snapshot data lacks the owner's output: %q", screen.Data)
	}
	if !tester.HasText(m.SessionObserverTitle()) || !tester.HasText(m.SessionObserverClose()) {
		t.Fatalf("the observer chrome is incomplete: %v", tester.Texts())
	}
	eventually(t, func() bool {
		return strings.Contains(surface.ObserverText(), "owner line one") &&
			strings.Contains(surface.ObserverText(), "owner line two")
	})

	// Output produced after the observation arrives through the feed, and
	// the owning view still receives it too.
	transport.emit("observer sees live output\r\n")
	tester.Frame()
	eventually(t, func() bool {
		return strings.Contains(surface.ObserverText(), "observer sees live output")
	})
	eventually(t, func() bool {
		return strings.Contains(surface.ScreenText(), "observer sees live output")
	})
	if got := surface.Session().LiveViews(); got != 1 {
		t.Fatalf("observing changed the live view count: %d", got)
	}

	if err := tester.Click(m.SessionObserverClose()); err != nil {
		t.Fatalf("Close observer: %v", err)
	}
	tester.Frame()
	if surface.Observing() {
		t.Fatal("the observer stayed open after Close")
	}
	if _, ok := surface.ObserverScreen(); ok {
		t.Fatal("the observer snapshot survived Close")
	}
	// The live screen was never displaced or duplicated.
	if surface.ScreenText() != ownerText && !strings.Contains(surface.ScreenText(), "observer sees live output") {
		t.Fatalf("the owner's screen changed while observing: %q", surface.ScreenText())
	}
	if got := surface.Session().LiveViews(); got != 1 {
		t.Fatalf("closing the observer changed the live view count: %d", got)
	}
}

func TestObserverSnapshotsAHibernatedSessionWithoutAttachingAView(t *testing.T) {
	surface, transport := newScriptedSurface(t)
	transport.emit("hibernated content\r\n")
	tester := surfaceTester(surface)
	tester.Frame()
	waitScreenContains(t, surface, "hibernated content")
	tester.Frame()

	if !surface.Hibernate() {
		t.Fatal("Hibernate failed")
	}
	tester.Frame()
	if surface.Session().LiveViews() != 0 {
		t.Fatal("hibernate kept a live view")
	}
	transport.emit("produced while away\r\n")

	if err := surface.OpenObserver(); err != nil {
		t.Fatalf("OpenObserver: %v", err)
	}
	tester.Frame()
	if surface.Session().LiveViews() != 0 {
		t.Fatalf("observing reattached a live view: %d", surface.Session().LiveViews())
	}
	eventually(t, func() bool {
		text := surface.ObserverText()
		return strings.Contains(text, "hibernated content") &&
			strings.Contains(text, "produced while away")
	})

	surface.CloseObserver()
	tester.Frame()
	if err := surface.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	tester.Frame()
	eventually(t, func() bool {
		text := surface.ScreenText()
		return strings.Contains(text, "hibernated content") &&
			strings.Contains(text, "produced while away")
	})
	if surface.Session().LiveViews() != 1 {
		t.Fatalf("Resume left %d live views, want 1", surface.Session().LiveViews())
	}
}

func TestHibernateAndObserverEntryPointsFitTheMinimumWindow(t *testing.T) {
	m := locales.In("en")
	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		size := size
		t.Run(strconv.Itoa(size.width)+"x"+strconv.Itoa(size.height), func(t *testing.T) {
			surface, transport := newScriptedSurface(t)
			transport.emit("layout screen\r\n")
			tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, size.width, size.height)
			tester.SetLocale("en")
			tester.Frame()
			waitScreenContains(t, surface, "layout screen")
			tester.Frame()

			if !surface.Hibernate() {
				t.Fatal("Hibernate failed")
			}
			tester.Frame()
			assertInside(t, tester, size.width, size.height,
				m.SessionHibernatedTitle(), m.SessionHibernatedBody(),
				m.SessionResume(), m.SessionMenuObserveSnapshot())

			if err := surface.OpenObserver(); err != nil {
				t.Fatalf("OpenObserver: %v", err)
			}
			tester.Frame()
			assertInside(t, tester, size.width, size.height,
				m.SessionObserverTitle(), m.SessionObserverClose())

			surface.CloseObserver()
			tester.Frame()
		})
	}
}

func assertInside(t *testing.T, tester *ui.Tester, width, height int, texts ...string) {
	t.Helper()
	for _, text := range texts {
		rect, ok := tester.Find(text)
		if !ok {
			t.Fatalf("%q is not rendered: %v", text, tester.Texts())
		}
		if rect.X < 0 || rect.Y < 0 ||
			rect.X+rect.W > float32(width)+0.5 || rect.Y+rect.H > float32(height)+0.5 {
			t.Fatalf("%q is clipped at %dx%d: %+v", text, width, height, rect)
		}
	}
}
