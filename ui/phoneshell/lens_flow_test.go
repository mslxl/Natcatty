package phoneshell

// The phone terminal's Lens flow and touch links, driven offscreen over the
// real libghostty-vt terminal. The tests tap the cell the fixture prints, so
// they exercise the pointer geometry, the shared link rules and the shared
// appview flow together.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// lensTestConn is a Session view for one test: it serves the scripted output,
// records what the terminal sends, and then blocks until closed.
type lensTestConn struct {
	mu      sync.Mutex
	out     bytes.Buffer
	written bytes.Buffer
	wrote   chan struct{}
	data    chan struct{}

	done chan struct{}
	once sync.Once
}

func newLensTestConn(output string) *lensTestConn {
	conn := &lensTestConn{
		wrote: make(chan struct{}, 8),
		data:  make(chan struct{}, 8),
		done:  make(chan struct{}),
	}
	conn.out.WriteString(output)
	return conn
}

// push appends output after the screen exists, as a live Session would.
func (c *lensTestConn) push(output string) {
	c.mu.Lock()
	c.out.WriteString(output)
	c.mu.Unlock()
	select {
	case c.data <- struct{}{}:
	default:
	}
}

func (c *lensTestConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if c.out.Len() > 0 {
			n, err := c.out.Read(p)
			c.mu.Unlock()
			return n, err
		}
		c.mu.Unlock()
		select {
		case <-c.data:
		case <-c.done:
			return 0, io.EOF
		}
	}
}

func (c *lensTestConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	n, err := c.written.Write(p)
	c.mu.Unlock()
	select {
	case c.wrote <- struct{}{}:
	default:
	}
	return n, err
}

func (c *lensTestConn) Resize(int, int) error { return nil }

func (c *lensTestConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *lensTestConn) writtenString() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written.String()
}

// newLensTestScreen builds the real terminal over scripted output and asks
// for the Lens flow.
func newLensTestScreen(t *testing.T, output string) (*ghosttyScreen, *lensTestConn) {
	t.Helper()
	conn := newLensTestConn(output)
	screen, err := newTerminalScreen(conn, DefaultTerminalAppearance())
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = screen.Close() })
	ghostty := screen.(*ghosttyScreen)
	ghostty.SetLensMode(true)
	return ghostty, conn
}

// cellPoint is the centre of the replay row and column a target is printed
// on, in the coordinates the Pane observes. The live grid is the tail of the
// replay and, while the flow follows, is pinned to the bottom of the Pane.
func cellPoint(t *testing.T, screen *ghosttyScreen, target string) (float32, float32) {
	t.Helper()
	cols, rows := screen.term.Size()
	if cols <= 0 || rows <= 0 {
		t.Fatalf("terminal grid is %dx%d", cols, rows)
	}
	frame := appview.ScreenFrame(screen.term, appview.Size{Cols: cols, Rows: rows}, false)
	base := len(frame.Lines) - rows
	if base < 0 {
		base = 0
	}
	for row, spans := range frame.Lines {
		text := appview.RowText(spans)
		col := strings.Index(text, target)
		if col < 0 {
			continue
		}
		gridRow := row - base
		if gridRow < 0 || gridRow >= rows {
			continue
		}
		g := screen.tapGeom
		if g.cellW <= 0 || g.cellH <= 0 || g.height <= 0 {
			t.Fatalf("tap geometry was not measured: %+v", g)
		}
		top := g.height - g.bottomInset - float32(rows)*g.cellH
		return g.padX + (float32(col)+0.5)*g.cellW, top + (float32(gridRow)+0.5)*g.cellH
	}
	t.Fatalf("%q is not on the live screen", target)
	return 0, 0
}

func frameTwice(tt *ui.Tester) {
	tt.Frame()
	tt.Frame()
}

func waitForWrite(t *testing.T, conn *lensTestConn) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if written := conn.writtenString(); written != "" {
			return written
		}
		if time.Now().After(deadline) {
			t.Fatalf("the terminal sent nothing; want the extraction command")
		}
		select {
		case <-conn.wrote:
		case <-time.After(time.Millisecond):
		}
	}
}

func TestTappingAPrintedURLOpensTheSystemBrowser(t *testing.T) {
	const url = "https://example.com/docs"
	screen, conn := newLensTestScreen(t, url+"\r\n")
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	frameTwice(tt)

	x, y := cellPoint(t, screen, url)
	tt.ClickAt(x, y)

	if got := tt.OpenedURLs(); len(got) != 1 || got[0] != url {
		t.Fatalf("opened %v, want [%s]", got, url)
	}
	if written := conn.writtenString(); written != "" {
		t.Fatalf("a URL tap typed %q into the terminal", written)
	}
	if snapshot := string(screen.term.Snapshot()); !strings.Contains(snapshot, url) {
		t.Fatalf("the terminal lost its content after the tap: %q", snapshot)
	}
}

func TestTappingAnArchiveNameFillsItsExtractionCommand(t *testing.T) {
	screen, conn := newLensTestScreen(t, "bundle.tar.gz\r\n")
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	frameTwice(tt)

	x, y := cellPoint(t, screen, "bundle.tar.gz")
	tt.ClickAt(x, y)

	want, ok := domainterminal.ArchiveExtractCommand("bundle.tar.gz")
	if !ok {
		t.Fatal("the fixture is not an archive")
	}
	if written := waitForWrite(t, conn); written != want {
		t.Fatalf("filled %q, want %q", written, want)
	}
	if strings.Contains(want, "\n") {
		t.Fatalf("the filled command is submitted: %q", want)
	}
}

func TestAFailedOpenShowsANoticeAndKeepsTheTerminal(t *testing.T) {
	const url = "https://example.com/missing"
	screen, conn := newLensTestScreen(t, url+"\r\n")
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	frameTwice(tt)
	tt.FailOpenURL(errors.New("no browser on this device"))

	x, y := cellPoint(t, screen, url)
	tt.ClickAt(x, y)
	tt.Frame()

	if !tt.HasText("Could not open " + url) {
		t.Fatalf("no failure notice; texts: %v", tt.Texts())
	}
	if written := conn.writtenString(); written != "" {
		t.Fatalf("a failed URL tap typed %q into the terminal", written)
	}
	if snapshot := string(screen.term.Snapshot()); !strings.Contains(snapshot, url) {
		t.Fatalf("the terminal lost its content after the failed open: %q", snapshot)
	}
}

// While a command runs, the live rows are that program's input: an archive
// name printed there must not fill an extraction command into its stdin. The
// desktop's archive handler declines in the same state.
func TestTappingAnArchiveWhileACommandRunsTypesNothing(t *testing.T) {
	screen, conn := newLensTestScreen(t, "")
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	frameTwice(tt)
	conn.push("\x1b]133;A\x07$ \x1b]133;B\x07tar -tf\x1b]133;C;tar -tf\x07bundle.tar.gz\r\n")

	deadline := time.Now().Add(2 * time.Second)
	for !screen.flow.running() {
		if time.Now().After(deadline) {
			t.Fatal("the flow did not record the running command")
		}
		tt.Frame()
	}
	tt.Frame()

	x, y := cellPoint(t, screen, "bundle.tar.gz")
	tt.ClickAt(x, y)
	tt.Frame()

	if written := conn.writtenString(); written != "" {
		t.Fatalf("a running command's archive tap typed %q", written)
	}
}

func TestTappingALocalPathOpensNothing(t *testing.T) {
	screen, conn := newLensTestScreen(t, "/etc/passwd\r\n")
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	frameTwice(tt)

	x, y := cellPoint(t, screen, "/etc/passwd")
	tt.ClickAt(x, y)

	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Fatalf("a local path opened %v", got)
	}
	if written := conn.writtenString(); written != "" {
		t.Fatalf("a local path typed %q into the terminal", written)
	}
}

// lensPromptFixture is a shell session with one finished command and the next
// prompt: the Lens flow has a block to draw above the live screen.
const lensPromptFixture = "\x1b]133;A\x07$ \x1b]133;B\x07pwd\r\n" +
	"\x1b]133;C;pwd\x07" + "/home/me\r\n" + "\x1b]133;D;0\x07" +
	"\x1b]133;A\x07$ \x1b]133;B\x07"

func TestLensModeDrawsFinishedCommandsInline(t *testing.T) {
	screen, conn := newLensTestScreen(t, "")
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	// Let the pane size the terminal first, as a live Session does. Output
	// that arrives after that keeps the prompt row the boundary mirror
	// recorded: a later width change would rightly forget it.
	frameTwice(tt)
	conn.push(lensPromptFixture)

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, known := screen.flow.promptBoundary()
		if len(screen.flow.store.Snapshot()) > 0 && known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the flow did not record the finished command")
		}
		tt.Frame()
	}
	tt.Frame()

	if !tt.HasText("files.pwd") {
		t.Fatalf("no finished command block; texts: %v", tt.Texts())
	}

	// Leaving the global Lens mode returns the same terminal to its own view:
	// the block chrome is gone, the Session's screen is not.
	screen.SetLensMode(false)
	tt.Frame()
	if tt.HasText("files.pwd") {
		t.Fatalf("the block stayed after leaving Lens mode; texts: %v", tt.Texts())
	}
	if snapshot := string(screen.term.Snapshot()); !strings.Contains(snapshot, "/home/me") {
		t.Fatalf("the terminal lost its content when leaving Lens mode: %q", snapshot)
	}

	screen.SetLensMode(true)
	tt.Frame()
	if !tt.HasText("files.pwd") {
		t.Fatalf("the block did not return with Lens mode; texts: %v", tt.Texts())
	}
}
