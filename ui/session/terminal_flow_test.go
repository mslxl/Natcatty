package sessionui

import (
	"context"
	"encoding/base64"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
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

	tester := ui.NewTester(func(c *ui.Context) {
		surfaceFrame(c, surface, true)
	}, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	texts := tester.Texts()
	count := 0
	for _, text := range texts {
		if text == "file.txt" {
			count++
		}
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

	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	for i := 1; i <= 5; i++ {
		name := base64.StdEncoding.EncodeToString([]byte("image-" + string(rune('0'+i)) + ".png"))
		transport.send("\x1b]1337;File=name=" + name + ";inline=1:" + png + "\x07")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(surface.flow.imagesSnapshot()) != 4 {
		time.Sleep(5 * time.Millisecond)
	}
	images := surface.flow.imagesSnapshot()
	if len(images) != 4 {
		t.Fatalf("retained images = %d, want 4", len(images))
	}
	if images[0].Alt != "image-2.png" || images[3].Alt != "image-5.png" {
		t.Fatalf("retained image order = [%q, ..., %q], want image-2.png through image-5.png",
			images[0].Alt, images[3].Alt)
	}
	surface.clearBuffer()
	if got := len(surface.flow.imagesSnapshot()); got != 0 {
		t.Fatalf("images after Clear blocks = %d, want 0", got)
	}
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
	waitForTerminalBlock(t, surface, 1)
	block := surface.flow.store.Snapshot()[0]
	if block.Cmd.Typed != "echo hibernate" || block.Raw != "while hibernated\n" {
		t.Fatalf("hibernated Lens replay = %+v", block)
	}
}
