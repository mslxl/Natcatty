package sessionui

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	lensmodel "github.com/mslxl/natcatty/application/lens/model"
	"github.com/mslxl/natcatty/application/lens/vt"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// terminalFlowTransport is a controllable Session transport. It lets these
// tests drive the same decoded bytes that a local PTY or ConPTY would produce.
type terminalFlowTransport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once
}

func newTerminalFlowTransport() *terminalFlowTransport {
	return &terminalFlowTransport{
		output: make(chan []byte, 8),
		closed: make(chan struct{}),
	}
}

func (t *terminalFlowTransport) Read(p []byte) (int, error) {
	select {
	case data := <-t.output:
		return copy(p, data), nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *terminalFlowTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *terminalFlowTransport) Resize(int, int) error       { return nil }
func (t *terminalFlowTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *terminalFlowTransport) send(data string) { t.output <- []byte(data) }

type terminalFlowFactory struct{ transport *terminalFlowTransport }

func (f terminalFlowFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func waitForTerminalBlock(t *testing.T, surface *Surface, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := len(surface.flow.store.Snapshot()); got >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d block(s); got %d", want, len(surface.flow.store.Snapshot()))
}

// waitForFinishedTerminalBlock waits for the wanted block to have seen its
// completion mark, not merely to exist. A block is published as soon as its
// command starts, so asserting its output right after waiting for the block
// reads a half-built record and fails whenever the D mark is a frame late.
func waitForFinishedTerminalBlock(t *testing.T, surface *Surface, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		blocks := surface.flow.store.Snapshot()
		if len(blocks) >= want && blocks[want-1].Exited {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for block %d to finish; got %+v", want, surface.flow.store.Snapshot())
}

func TestTerminalFlowTracksPromptRowsWithoutPromptText(t *testing.T) {
	flow := &terminalFlow{}
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	flow.bindBellTracker(term)

	// The prompt deliberately contains no conventional shell marker. OSC 133;B
	// is the boundary, and the visible text is arbitrary.
	flow.feedBell([]byte("\x1b]133;A\x07\x1b]133;B\x07⟦session=alpha⟧ "))
	row, ok := flow.promptBoundary(appview.Size{Cols: 40, Rows: 8})
	if !ok || row != 0 {
		t.Fatalf("first prompt boundary = (%d, %t), want (0, true)", row, ok)
	}

	// An empty Enter redraws another arbitrary prompt. The live run must still
	// begin at the first prompt row, without inspecting its text.
	flow.feedBell([]byte("\r\n\x1b]133;A\x07\x1b]133;B\x07⟦session=beta⟧ "))
	row, ok = flow.promptBoundary(appview.Size{Cols: 40, Rows: 8})
	if !ok || row != 0 {
		t.Fatalf("empty-prompt run boundary = (%d, %t), want (0, true)", row, ok)
	}

	// A real command starts a fresh run after its completion marker.
	flow.feedBell([]byte("\r\n\x1b]133;C\x07result\r\n\x1b]133;D;0\x07\r\n\x1b]133;A\x07\x1b]133;B\x07⟦session=gamma⟧ "))
	row, ok = flow.promptBoundary(appview.Size{Cols: 40, Rows: 8})
	if !ok || row != 4 {
		t.Fatalf("post-command prompt boundary = (%d, %t), want (4, true)", row, ok)
	}
}

func TestTerminalFlowKeepsPromptBoundaryWhenPaneResizes(t *testing.T) {
	flow := &terminalFlow{}
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	flow.bindBellTracker(term)
	flow.feedBell([]byte("\x1b]133;A\x07\x1b]133;B\x07prompt> "))

	// In the real Pane, inserting a finished Lens block changes the live
	// terminal's row count. The OSC 133 boundary remains authoritative across
	// that resize and must continue to let the Pane render the block.
	flow.setSize(40, 4)
	if row, ok := flow.promptBoundary(appview.Size{Cols: 40, Rows: 4}); !ok || row != 0 {
		t.Fatalf("resized prompt boundary = (%d, %t), want (0, true)", row, ok)
	}
}

// The prompt row is recorded in the replay's own coordinate, before the Pane's
// row cap drops the oldest rows above the screen. Recorded against the capped
// window, the row named a line below the prompt as soon as the window slid, and
// the Pane cropped the prompt and the line being typed out of the live area.
func TestTerminalFlowRecordsThePromptRowFromTheReplayStart(t *testing.T) {
	flow := &terminalFlow{}
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 10)
	flow.bindBellTracker(term)

	// More rows than the Pane's replay cap keeps.
	var history strings.Builder
	for i := 0; i < 420; i++ {
		history.WriteString("line-" + strconv.Itoa(i) + "\r\n")
	}
	history.WriteString("\x1b]133;A\x07\x1b]133;B\x07PROMPT> ")
	flow.feedBell([]byte(history.String()))

	// The cursor is on row 420 of the replay. The capped window holds rows
	// 21..420, where the same line is row 399.
	row, ok := flow.promptBoundary(appview.Size{Cols: 40, Rows: 10})
	if !ok || row != 420 {
		t.Fatalf("prompt boundary = (%d, %t), want the replay-absolute row (420, true)", row, ok)
	}
}

// zsh's and bash's clear-screen (Ctrl+L) erase the screen and repaint the
// prompt without a new OSC 133;B, so the recorded boundary named the pre-clear
// prompt. The live area cropped from there and drew neither the repainted
// prompt nor the line being typed, with nothing to scroll to.
func TestTerminalFlowReanchorsThePromptAfterAClearScreen(t *testing.T) {
	flow := &terminalFlow{}
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	flow.bindBellTracker(term)

	flow.feedBell([]byte("old output\r\n\x1b]133;A\x07\x1b]133;B\x07PROMPT> "))
	if row, ok := flow.promptBoundary(appview.Size{Cols: 40, Rows: 8}); !ok || row != 1 {
		t.Fatalf("prompt boundary = (%d, %t), want (1, true)", row, ok)
	}

	// The line editor's clear-screen sequence, then the repainted prompt and
	// the typed text, all without an OSC 133 marker.
	flow.feedBell([]byte("\x1b[H\x1b[2JPROMPT> typed"))
	if row, ok := flow.promptBoundary(appview.Size{Cols: 40, Rows: 8}); !ok || row != 0 {
		t.Fatalf("boundary after the clear = (%d, %t), want the repainted prompt's row (0, true)", row, ok)
	}
}

// A width change re-wraps the scrollback, so the recorded absolute row names a
// different line. Kept as valid, it cropped the live area at the wrong line:
// finished output was replayed under the blocks that already showed it. The
// row is forgotten until the shell redraws its prompt with a fresh OSC 133;B,
// which it does on a resize.
func TestTerminalFlowForgetsPromptRowAfterAWiderPane(t *testing.T) {
	flow := &terminalFlow{}
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	flow.bindBellTracker(term)
	flow.feedBell([]byte("\x1b]133;A\x07\x1b]133;B\x07prompt> "))
	if row, ok := flow.promptBoundary(appview.Size{Cols: 40, Rows: 8}); !ok || row != 0 {
		t.Fatalf("prompt boundary = (%d, %t), want (0, true)", row, ok)
	}

	flow.setSize(80, 8)
	if row, ok := flow.promptBoundary(appview.Size{Cols: 80, Rows: 8}); ok {
		t.Fatalf("boundary = (%d, true) after a width change, want it forgotten", row)
	}

	// The shell redraws its prompt on the new width and reports a new row.
	flow.feedBell([]byte("\r\x1b]133;A\x07\x1b]133;B\x07prompt> "))
	if _, ok := flow.promptBoundary(appview.Size{Cols: 80, Rows: 8}); !ok {
		t.Fatal("the redrawn prompt's boundary was not recorded")
	}
}

func TestTerminalSurfaceBuildsFinishedBlockWithoutMutatingTerminalState(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	transport.send("\x1b]1337;NetcattyExec;powershell;" +
		"Qzpcd29yaw;Z2l0IHN0YXR1cyAtLXNob3J0\x07")
	transport.send("\x1b]133;C\x07 M file.txt\r\n\x1b]133;D;0\x07")
	waitForTerminalBlock(t, surface, 1)

	block := surface.flow.store.Snapshot()[0]
	if !block.Exited {
		t.Fatal("finished command was left running")
	}
	if block.Cmd.Typed != "git status --short" {
		t.Fatalf("typed command = %q", block.Cmd.Typed)
	}
	if block.Raw != " M file.txt\n" {
		t.Fatalf("raw output = %q", block.Raw)
	}

	if got := surface.terminal.Text(); got == "" {
		t.Fatal("finished output was removed from the shell terminal; mygo-flow keeps the shell state intact")
	}
	surface.flow.setSize(40, 4)

	tester := ui.NewTester(func(c *ui.Context) {
		surfaceFrame(c, surface, true)
	}, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	texts := tester.Texts()
	count := 0
	for _, text := range texts {
		if strings.Contains(text, "file.txt") {
			count++
		}
	}
	if count == 0 {
		t.Fatalf("finished output was hidden above the live viewport: %v", texts)
	}
	if count > 1 {
		t.Fatalf("finished output was rendered more than once: %v", texts)
	}
}

func TestTerminalSurfaceBellSnapshotUsesTheBELPosition(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	// Bytes after BEL move the shell cursor before the next frame. The queued
	// event must retain the cell at BEL, exactly as mygo-flow's scanner does.
	surface.flow.feedBell([]byte("abc\x07\x1b[2C"))
	bell, ok := surface.flow.takeBell()
	if !ok {
		t.Fatal("BEL did not queue a ripple event")
	}
	if bell.Col != 3 || bell.Row != 0 {
		t.Fatalf("BEL cursor = (%d,%d), want (3,0)", bell.Col, bell.Row)
	}
}

func TestTerminalSurfaceReplaysDecodedHistoryWhenCreatedLate(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}

	transport.send("\x1b]1337;NetcattyExec;powershell;Qzpcd29yaw;ZWNobyBoaXN0b3J5\x07")
	transport.send("\x1b]133;C\x07history\r\n\x1b]133;D;0\x07")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && session.Snapshot().Stats.OutputBytes == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	waitForTerminalBlock(t, surface, 1)
	block := surface.flow.store.Snapshot()[0]
	if block.Cmd.Typed != "echo history" || block.Raw != "history\n" {
		t.Fatalf("late replay block = %+v", block)
	}
}

func TestTerminalSurfaceReplayIsClearedWithSessionBuffer(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	transport.send("\x1b]1337;NetcattyExec;powershell;Qzpcd29yaw;ZWNobyBoaXN0b3J5\x07")
	transport.send("\x1b]133;C\x07history\r\n\x1b]133;D;0\x07")
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminalBlock(t, surface, 1)
	surface.Close()
	session.ClearBuffer()

	next, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Close)
	time.Sleep(50 * time.Millisecond)
	if got := len(next.flow.store.Snapshot()); got != 0 {
		t.Fatalf("cleared replay rebuilt %d old blocks", got)
	}
}

func TestTerminalSurfaceClearBufferMatchesMyGoFlowReset(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	surface.terminal.Feed([]byte("old screen\r\n"))
	surface.flowScroll.Y = 12
	surface.flowFollow.Update(&surface.flowScroll)
	if !surface.flowFollow.Pinned() {
		t.Fatal("test setup did not pin the flow")
	}
	surface.clearBuffer()

	if got := surface.terminal.Text(); got != "" {
		t.Fatalf("terminal text after Clear blocks = %q, want empty", got)
	}
	if surface.flowScroll.Y != math.MaxFloat32 {
		t.Fatalf("scroll Y after Clear blocks = %v, want MaxFloat32", surface.flowScroll.Y)
	}
	if surface.flowFollow.Pinned() {
		t.Fatal("Clear blocks left the old follow state pinned")
	}
}

// Clear blocks empties the visible terminal, so the boundary mirror must be
// re-seeded from it. A mirror left holding the old rows records the next
// prompt's boundary counted from a first line the cleared replay no longer
// has: the row lands below the live area, the Pane falls back to drawing the
// whole replay, and a finished command appears both as its block and again as
// raw rows.
func TestTerminalSurfaceClearBufferResetsThePromptBoundary(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	cols, rows := surface.terminal.Size()
	size := appview.Size{Cols: cols, Rows: rows}
	var old strings.Builder
	for i := 0; i < 40; i++ {
		old.WriteString("old row\r\n")
	}
	old.WriteString("\x1b]133;A\x07\x1b]133;B\x07old> ")
	surface.flow.feedBell([]byte(old.String()))
	if row, ok := surface.flow.promptBoundary(size); !ok || row != 40 {
		t.Fatalf("prompt boundary = (%d, %t), want (40, true)", row, ok)
	}

	surface.clearBuffer()
	if row, ok := surface.flow.promptBoundary(size); ok {
		t.Fatalf("boundary = (%d, true) after Clear blocks, want it forgotten", row)
	}

	// The first prompt after the clear counts from the cleared screen's first
	// row, as the visible terminal does.
	surface.flow.feedBell([]byte("\x1b]133;A\x07\x1b]133;B\x07PROMPT> "))
	if row, ok := surface.flow.promptBoundary(size); !ok || row != 0 {
		t.Fatalf("boundary after the clear = (%d, %t), want (0, true)", row, ok)
	}
}

func TestTerminalSurfaceKeepsOnlyTheLastFourInlineImages(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	// Each picture fills its single pixel with a different red value, so the
	// retained four can be told apart by content. The sequences also carry the
	// iTerm2 `name` parameter, which must not become a caption: a terminal
	// shows the picture alone, as Kitty does.
	pngs := make([]string, 6)
	for i := 1; i <= 5; i++ {
		pngs[i] = singlePixelPNG(t, uint8(i))
		name := base64.StdEncoding.EncodeToString([]byte("image-" + string(rune('0'+i)) + ".png"))
		transport.send("\x1b]1337;File=name=" + name + ";inline=1:" + pngs[i] + "\x07")
	}
	last, err := base64.StdEncoding.DecodeString(pngs[5])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		images := surface.flow.imagesSnapshot()
		if len(images) == 4 && bytes.Equal(images[3].Data, last) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	images := surface.flow.imagesSnapshot()
	if len(images) != 4 {
		t.Fatalf("retained images = %d, want 4", len(images))
	}
	for i, image := range images {
		want, err := base64.StdEncoding.DecodeString(pngs[i+2])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(image.Data, want) {
			t.Fatalf("retained image %d is not picture %d", i, i+2)
		}
		if image.Alt != "" {
			t.Fatalf("picture %d was captioned %q", i+2, image.Alt)
		}
	}
	surface.clearBuffer()
	if got := len(surface.flow.imagesSnapshot()); got != 0 {
		t.Fatalf("images after Clear blocks = %d, want 0", got)
	}
}

// A program's graphics-capability probes are not pictures. yazi sends two
// Kitty queries (a=q) and DECRQSS's sixel "$q" before it draws; decoding them
// attached tiny images to the command's block, which reappeared as marks under
// the command once yazi quit.
func TestTerminalSurfaceIgnoresGraphicsCapabilityProbes(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	transport.send("\x1b]133;C;yazi;yazi\x07" +
		"\x1b_Gi=278941603,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\" +
		"\x1b_Gi=916472805,s=1,v=1,a=q,t=s,f=24,S=3;L3lhemktSEJDVlBUTENSM1lJR1hRQk80M0NXVUhI\x1b\\" +
		"\x1bP$q q\x1b\\" +
		"\x1b]133;D;0\x07")
	waitForFinishedTerminalBlock(t, surface, 1)

	blocks := surface.flow.store.Snapshot()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := len(blocks[0].Images); got != 0 {
		t.Fatalf("the command block kept %d probe images, want none", got)
	}
	if images := surface.flow.imagesSnapshot(); len(images) != 0 {
		t.Fatalf("the flow kept %d probe images, want none", len(images))
	}
	if !surface.flow.hasPictures() {
		t.Fatal("a program that queries the graphics protocol must ask for the terminal's own view")
	}
}

// singlePixelPNG is a one-pixel PNG whose red channel marks it.
func singlePixelPNG(t *testing.T, red uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: red, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestTerminalSurfaceKeepsLensReplayAcrossHibernateResume(t *testing.T) {
	transport := newTerminalFlowTransport()
	service := appsession.NewService(terminalFlowFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	transport.send("\x1b]1337;NetcattyExec;powershell;Qzpcd29yaw;ZWNobyBoaWJlcm5hdGU\x07")
	transport.send("\x1b]133;C\x07")
	if !surface.Hibernate() {
		t.Fatal("Hibernate returned false")
	}
	transport.send("while hibernated\r\n\x1b]133;D;0\x07")
	if err := surface.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForFinishedTerminalBlock(t, surface, 1)
	block := surface.flow.store.Snapshot()[0]
	if block.Cmd.Typed != "echo hibernate" || block.Raw != "while hibernated\n" {
		t.Fatalf("hibernated Lens replay = %+v", block)
	}
}

// A block's captured prompt rows are paired to it by the OSC 133;C mark. Block
// ids restart at zero when a command clears the store, so the capture keeps the
// epoch it was taken under: rows captured for an old block must never appear
// above a new block that happens to reuse its id.
func TestTerminalFlowKeepsCapturedPromptRowsWithTheirOwnBlock(t *testing.T) {
	flow := &terminalFlow{store: lensmodel.NewStore(nil)}
	term, err := terminal.New(terminal.Options{Conn: newTerminalFlowTransport(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	flow.bindBellTracker(term)
	tap := vt.New(flow.store)
	// The same order as the live subscription: the tap sees the bytes first,
	// its mark is taken, then the mirror captures at the same mark.
	emit := func(data []byte) {
		tap.Feed(data)
		flow.notePromptMarks(tap.TakeCommandMarks())
		flow.feedBell(data)
		flow.pairPrompts()
	}
	rowText := func(rows [][]ui.Span) string {
		var b strings.Builder
		for _, row := range rows {
			for _, sp := range row {
				b.WriteString(sp.Text)
			}
		}
		return strings.TrimRight(b.String(), " ")
	}

	emit([]byte("\x1b]133;A\x07\x1b]133;B\x07one> "))
	emit([]byte("echo old\r\n\x1b]133;C;echo old\x07"))
	if rows := flow.promptRowsFor(0); rowText(rows) != "one> echo old" {
		t.Fatalf("first block's prompt rows = %q, want the shell's drawn row", rowText(rows))
	}
	emit([]byte("\x1b]133;D;0\x07"))

	// The `clear` command empties the store, so the next block starts at id 0
	// again. The old rows must not come back with it.
	flow.store.Clear()
	if rows := flow.promptRowsFor(0); rows != nil {
		t.Fatalf("the cleared block's prompt rows survived: %q", rowText(rows))
	}
	emit([]byte("\x1b]133;A\x07\x1b]133;B\x07two> "))
	emit([]byte("echo new\r\n\x1b]133;C;echo new\x07"))
	if rows := flow.promptRowsFor(0); rowText(rows) != "two> echo new" {
		t.Fatalf("the reused block id shows %q, want its own prompt", rowText(rows))
	}
}
