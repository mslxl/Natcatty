// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	moshcore "github.com/mslxl/natcatty/internal/mosh"
	"github.com/mslxl/natcatty/internal/mosh/terminal"
)

// drainQueue returns every byte currently queued. Renderer tests emit
// synchronously, so this is a complete frame without waiting.
func drainQueue(q *outputQueue) []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []byte
	for _, chunk := range q.chunks {
		out = append(out, chunk...)
	}
	q.chunks = nil
	q.bytes = 0
	return out
}

// unsetTestEnv removes name for the test and restores the process value after.
func unsetTestEnv(t *testing.T, name string) {
	t.Helper()
	old, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

// TestNoTitlePrefixUsesEnvPresence covers stock's getenv semantics: the
// variable suppresses the prefix even when its value is empty.
func TestNoTitlePrefixUsesEnvPresence(t *testing.T) {
	unsetTestEnv(t, "MOSH_TITLE_NOPREFIX")
	if noTitlePrefix() {
		t.Fatal("an unset MOSH_TITLE_NOPREFIX suppressed the prefix")
	}
	t.Setenv("MOSH_TITLE_NOPREFIX", "")
	if !noTitlePrefix() {
		t.Fatal("an empty MOSH_TITLE_NOPREFIX did not suppress the prefix")
	}
	t.Setenv("MOSH_TITLE_NOPREFIX", "1")
	if !noTitlePrefix() {
		t.Fatal("a non-empty MOSH_TITLE_NOPREFIX did not suppress the prefix")
	}
}

func hostState(num, oldNum uint64, newest bool, instructions ...moshcore.HostInstruction) moshcore.Update {
	return moshcore.Update{HostStates: []moshcore.HostState{{
		Num: num, OldNum: oldNum, Instructions: instructions, Newest: newest,
	}}}
}

type goldenEventJSON struct {
	Input  []byte `json:"input"`
	Resize []int  `json:"resize"`
}

type goldenFileJSON struct {
	Name    string            `json:"name"`
	Width   int               `json:"width"`
	Height  int               `json:"height"`
	Events  []goldenEventJSON `json:"events"`
	Frames  [][]byte          `json:"frames"`
	Screens []string          `json:"screens"`
}

// TestRendererMatchesStockDisplayGoldens feeds the same terminal events the
// stock oracle saw as SSP host states and requires the renderer's complete
// output stream to be byte-equal to stock mosh-client's Display output. This
// is the adapter-level form of the committed terminal corpus.
func TestRendererMatchesStockDisplayGoldens(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "internal", "mosh", "terminal",
		"testdata", "parity", "goldens", "*.json"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no parity goldens found")
	}
	for _, path := range paths {
		path := path
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var golden goldenFileJSON
			if err := json.Unmarshal(data, &golden); err != nil {
				t.Fatalf("decode golden: %v", err)
			}

			queue := newOutputQueue(4 << 20)
			renderer := newTerminalRendererWithOptions(queue, golden.Width, golden.Height,
				terminalRendererOptions{Display: terminal.DefaultDisplayOptions()})
			renderer.setScreenSizeHandler(func(int, int, uint64) {})
			if err := renderer.emitInitial(); err != nil {
				t.Fatalf("emit initial: %v", err)
			}

			stateNum := uint64(0)
			for _, event := range golden.Events {
				stateNum++
				var instruction moshcore.HostInstruction
				if len(event.Resize) == 2 {
					instruction = moshcore.HostInstruction{
						Resize: true, Width: int32(event.Resize[0]), Height: int32(event.Resize[1]),
					}
				} else {
					instruction = moshcore.HostInstruction{HostString: event.Input}
				}
				if err := renderer.applyUpdate(hostState(stateNum, stateNum-1, true, instruction)); err != nil {
					t.Fatalf("apply state %d: %v", stateNum, err)
				}
				if err := renderer.emitLatest(); err != nil {
					t.Fatalf("emit state %d: %v", stateNum, err)
				}
			}

			var want bytes.Buffer
			want.WriteString(renderer.display.Open())
			for _, frame := range golden.Frames {
				want.Write(frame)
			}
			got := drainQueue(queue)
			if !bytes.Equal(got, want.Bytes()) {
				t.Fatalf("renderer output differs from stock\n got %q\nwant %q", got, want.Bytes())
			}
		})
	}
}

// TestRendererNeverForwardsRawHostBytes proves the renderer only forwards the
// Display encoder's canonical output. The host string contains sequences the
// encoder rewrites or drops; forwarding the source bytes would show up
// verbatim.
func TestRendererNeverForwardsRawHostBytes(t *testing.T) {
	queue := newOutputQueue(1 << 20)
	renderer := newTerminalRendererWithOptions(queue, 8, 3,
		terminalRendererOptions{Display: terminal.DefaultDisplayOptions()})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}
	drainQueue(queue)

	raw := "\x1b[999;999H\x1b[2K\x1b]52;c;ZGF0YQ==\x1b\\X"
	if err := renderer.applyUpdate(hostState(1, 0, true,
		moshcore.HostInstruction{HostString: []byte(raw)})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := renderer.emitLatest(); err != nil {
		t.Fatalf("emit: %v", err)
	}
	out := string(drainQueue(queue))
	if strings.Contains(out, raw) {
		t.Fatalf("renderer forwarded the raw host string: %q", out)
	}
	for _, rawOnly := range []string{"\x1b[999;999H", "\x1b[2K", "\x1b]52;c;ZGF0YQ==\x1b\\"} {
		if strings.Contains(out, rawOnly) {
			t.Fatalf("renderer forwarded raw host sequence %q in %q", rawOnly, out)
		}
	}
	// The encoder must still have rendered the visible text and clipboard.
	if !strings.Contains(out, "X") {
		t.Fatalf("encoder output lost the visible text: %q", out)
	}
	if !strings.Contains(out, "\x1b]52;c;ZGF0YQ==\x07") {
		// Display's clipboard payload is the parsed clipboard runes, which is
		// the same body; this asserts the canonical OSC 52 path ran.
		t.Fatalf("encoder output lost the clipboard update: %q", out)
	}
}

// TestRendererInputTranslation covers DECCKM forcing plus the remote's actual
// cursor-key mode, including an SS3 sequence split across writes.
func TestRendererInputTranslation(t *testing.T) {
	queue := newOutputQueue(1 << 20)
	renderer := newTerminalRendererWithOptions(queue, 8, 3,
		terminalRendererOptions{Display: terminal.DefaultDisplayOptions()})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})

	if got := string(renderer.translateInput([]byte("\x1bOA"))); got != "\x1b[A" {
		t.Fatalf("normal-mode SS3 A = %q, want CSI A", got)
	}
	if err := renderer.applyUpdate(hostState(1, 0, true,
		moshcore.HostInstruction{HostString: []byte("\x1b[?1h")})); err != nil {
		t.Fatalf("apply DECCKM: %v", err)
	}
	if got := string(renderer.translateInput([]byte("\x1bOA"))); got != "\x1bOA" {
		t.Fatalf("application-mode SS3 A = %q, want SS3 A", got)
	}
	if got := string(renderer.translateInput([]byte("\x1bOP"))); got != "\x1bOP" {
		t.Fatalf("PF1 SS3 P = %q, want unchanged", got)
	}

	// The translator keeps the one-byte SS3 lookahead across Write calls.
	var got []byte
	for _, chunk := range []string{"\x1b", "O", "A"} {
		got = append(got, renderer.translateInput([]byte(chunk))...)
	}
	if string(got) != "\x1bOA" {
		t.Fatalf("split SS3 A = %q, want SS3 A", got)
	}
}

// TestRendererReconstructsOutOfOrderStates mirrors SSP's prospective resend:
// a late state is retained as a reference and a later diff applies to it, not
// to the previously drawn state.
func TestRendererReconstructsOutOfOrderStates(t *testing.T) {
	queue := newOutputQueue(1 << 20)
	renderer := newTerminalRendererWithOptions(queue, 8, 3,
		terminalRendererOptions{Display: terminal.DefaultDisplayOptions()})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}

	steps := []moshcore.Update{
		hostState(1, 0, true, moshcore.HostInstruction{HostString: []byte("A")}),
		hostState(3, 1, true, moshcore.HostInstruction{HostString: []byte("C")}),
		hostState(2, 1, false, moshcore.HostInstruction{HostString: []byte("B")}),
		hostState(4, 2, true, moshcore.HostInstruction{HostString: []byte("D")}),
	}
	for i, update := range steps {
		if err := renderer.applyUpdate(update); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if err := renderer.emitLatest(); err != nil {
			t.Fatalf("step %d emit: %v", i, err)
		}
	}
	renderer.mu.Lock()
	fb := renderer.latest.Framebuffer()
	var row strings.Builder
	for i := 0; i < 4 && i < len(fb.Rows[0].Cells); i++ {
		if !fb.Rows[0].Cells[i].Empty() {
			row.WriteString(fb.Rows[0].Cells[i].Contents)
		}
	}
	renderer.mu.Unlock()
	if row.String() != "ABD" {
		t.Fatalf("latest state row = %q, want %q", row.String(), "ABD")
	}
}

// TestRendererResizeBeforeOutput blocks the inverse resize signal and proves
// no display bytes for the new size become readable until the handler returns.
func TestRendererResizeBeforeOutput(t *testing.T) {
	queue := newOutputQueue(1 << 20)
	renderer := newTerminalRendererWithOptions(queue, 8, 3,
		terminalRendererOptions{Display: terminal.DefaultDisplayOptions()})
	handlerEntered := make(chan [2]int, 1)
	release := make(chan struct{})
	renderer.setScreenSizeHandler(func(cols, rows int, _ uint64) {
		handlerEntered <- [2]int{cols, rows}
		<-release
	})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}
	drainQueue(queue)

	if err := renderer.applyUpdate(hostState(1, 0, true,
		moshcore.HostInstruction{Resize: true, Width: 12, Height: 5},
		moshcore.HostInstruction{HostString: []byte("resized")})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- renderer.emitLatest() }()

	var size [2]int
	select {
	case size = <-handlerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("renderer never called the resize handler")
	}
	if size != [2]int{12, 5} {
		t.Fatalf("handler size = %v, want [12 5]", size)
	}
	// While the handler is blocked, the queue must not expose display bytes
	// encoded for the new size.
	if out := drainQueue(queue); len(out) != 0 {
		t.Fatalf("output escaped before the resize was applied: %q", out)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renderer did not finish after the resize handler returned")
	}
	out := drainQueue(queue)
	if len(out) == 0 || !bytes.Contains(out, []byte("resized")) {
		t.Fatalf("output after resize = %q, want rendered text", out)
	}
}

// newOverlayTestRenderer builds an initialized renderer whose overlay clock is
// controlled by the caller. The escape key is unset so the stock default hint
// ("Ctrl-^") is deterministic.
func newOverlayTestRenderer(t *testing.T, clock func() uint64) (*terminalRenderer, *outputQueue) {
	t.Helper()
	unsetTestEnv(t, "MOSH_ESCAPE_KEY")
	queue := newOutputQueue(1 << 20)
	// Wide enough for the full notification strings: Display clips the bar to
	// the framebuffer width.
	renderer := newTerminalRendererWithOptions(queue, 80, 4, terminalRendererOptions{
		Display: terminal.DefaultDisplayOptions(),
		Clock:   clock,
	})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}
	drainQueue(queue)
	return renderer, queue
}

// TestEscapeKeyNameMatchesStock covers mosh-client's MOSH_ESCAPE_KEY parsing
// and the string it appends to notification bars.
func TestEscapeKeyNameMatchesStock(t *testing.T) {
	unsetTestEnv(t, "MOSH_ESCAPE_KEY")
	if got := escapeKeyName(); got != "Ctrl-^" {
		t.Fatalf("unset escape key name = %q, want Ctrl-^", got)
	}
	for _, testCase := range []struct {
		value string
		want  string
	}{
		{"", ""},     // empty disables the quit hint
		{"x", `"x"`}, // stock quotes a printable key
		{"\x01", "Ctrl-A"},
		{"\x1e", "Ctrl-^"},
		{"\x03", "Ctrl-^"}, // stock refuses Ctrl-C
		{"\x04", "Ctrl-^"},
		{"\x0a", "Ctrl-^"},
		{"\x0c", "Ctrl-^"},
		{"\x0d", "Ctrl-^"},
		{"ab", "Ctrl-^"}, // only a single byte is accepted
	} {
		t.Setenv("MOSH_ESCAPE_KEY", testCase.value)
		if got := escapeKeyName(); got != testCase.want {
			t.Errorf("MOSH_ESCAPE_KEY=%q: name = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

// TestRendererConnectingNotification covers stock's startup bar: while
// remote_state_num is zero, the main loop wakes at least every 250ms and shows
// the announced UDP port plus the quit hint. The first server state clears it.
func TestRendererConnectingNotification(t *testing.T) {
	now := uint64(1000)
	renderer, queue := newOverlayTestRenderer(t, func() uint64 { return now })
	renderer.setUDPPort(60001)

	now = 1200
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain before 250ms: %v", err)
	}
	if out := string(drainQueue(queue)); len(out) != 0 {
		t.Fatalf("connecting bar drawn before 250ms: %q", out)
	}

	now = 1251
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain after 250ms: %v", err)
	}
	out := string(drainQueue(queue))
	for _, want := range []string{
		"Nothing received from server on UDP port 60001.",
		"[To quit: Ctrl-^ .]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("connecting output %q does not contain %q", out, want)
		}
	}

	// The message is non-permanent, but stock re-sets it on every wake while
	// still connecting, so it must not expire after its first second.
	now = 2400
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain while still connecting: %v", err)
	}
	renderer.mu.Lock()
	message := string(renderer.overlays.Notifications().NotificationString())
	renderer.mu.Unlock()
	if message != connectingNotification(60001) {
		t.Fatalf("connecting bar expired while still connecting: %q", message)
	}

	if err := renderer.applyUpdate(hostState(1, 0, true,
		moshcore.HostInstruction{HostString: []byte("connected")})); err != nil {
		t.Fatalf("apply state: %v", err)
	}
	now += 10
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain after connect: %v", err)
	}
	out = string(drainQueue(queue))
	if strings.Contains(out, "Nothing received from server") {
		t.Fatalf("connecting bar survived the first server state: %q", out)
	}
	if !strings.Contains(out, "connected") {
		t.Fatalf("connected frame %q does not render the server state", out)
	}
}

// TestRendererEmptyEscapeKeyHidesQuitHint covers stock's MOSH_ESCAPE_KEY=""
// state: set_escape_key_string is never called, so notification bars carry no
// "[To quit: ... .]" suffix at all.
func TestRendererEmptyEscapeKeyHidesQuitHint(t *testing.T) {
	t.Setenv("MOSH_ESCAPE_KEY", "")
	now := uint64(0)
	queue := newOutputQueue(1 << 20)
	renderer := newTerminalRendererWithOptions(queue, 80, 2, terminalRendererOptions{
		Display: terminal.DefaultDisplayOptions(),
		Clock:   func() uint64 { return now },
	})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}
	drainQueue(queue)
	renderer.setUDPPort(60003)
	now = 300
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	out := string(drainQueue(queue))
	if !strings.Contains(out, "Nothing received from server on UDP port 60003.") {
		t.Fatalf("connecting output = %q", out)
	}
	if strings.Contains(out, "To quit") {
		t.Fatalf("empty MOSH_ESCAPE_KEY still rendered a quit hint: %q", out)
	}
}

// TestRendererConnectingTimeoutNotification mirrors stock's 15-second message.
// Stock also calls network->start_shutdown() at that point; this ticket
// deliberately wires only the visible notification, as documented on
// maintainOverlays.
func TestRendererConnectingTimeoutNotification(t *testing.T) {
	now := uint64(0)
	renderer, queue := newOverlayTestRenderer(t, func() uint64 { return now })
	renderer.setUDPPort(60002)
	now = 15001
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain at timeout: %v", err)
	}
	out := string(drainQueue(queue))
	if !strings.Contains(out, "mosh: Timed out waiting for server...") {
		t.Fatalf("timeout output = %q", out)
	}
}

// TestRendererCountUpRedrawsOnTimer proves the overlay timer, not a server
// update, drives the last-contact count-up exactly as stock's select loop does.
func TestRendererCountUpRedrawsOnTimer(t *testing.T) {
	now := uint64(0)
	renderer, queue := newOverlayTestRenderer(t, func() uint64 { return now })
	if err := renderer.applyUpdate(hostState(1, 0, true,
		moshcore.HostInstruction{HostString: []byte("x")})); err != nil {
		t.Fatalf("apply state: %v", err)
	}
	if err := renderer.emitLatest(); err != nil {
		t.Fatalf("emit: %v", err)
	}
	drainQueue(queue)

	now = 7000
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain at 7s: %v", err)
	}
	out := string(drainQueue(queue))
	if !strings.Contains(out, "mosh: Last contact 7 seconds ago. [To quit: Ctrl-^ .]") {
		t.Fatalf("7s count-up output = %q", out)
	}

	// Past 60 seconds stock slows the count-up to one update every 3 seconds.
	now = 71000
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain at 71s: %v", err)
	}
	// The encoder emits a minimal diff: only the changed duration and the
	// escape hint moved, so the bar prefix is not repeated in this frame.
	out = string(drainQueue(queue))
	if !strings.Contains(out, "1:11 ago.") {
		t.Fatalf("71s count-up output = %q", out)
	}
}

// TestRendererServerAckedSelectsReplyCountUp proves ServerAcked participates in
// stock's "reply" explanation instead of leaving only the last-contact clock.
func TestRendererServerAckedSelectsReplyCountUp(t *testing.T) {
	now := uint64(0)
	renderer, queue := newOverlayTestRenderer(t, func() uint64 { return now })
	if err := renderer.applyUpdate(hostState(1, 0, true,
		moshcore.HostInstruction{HostString: []byte("x")})); err != nil {
		t.Fatalf("apply state: %v", err)
	}
	if err := renderer.emitLatest(); err != nil {
		t.Fatalf("emit: %v", err)
	}
	drainQueue(queue)

	// A fresh server word arrives, but the server has not acknowledged our
	// sent state for 11 seconds: stock reports the reply clock, not contact.
	now = 11000
	if err := renderer.applyUpdate(hostState(2, 1, true,
		moshcore.HostInstruction{HostString: []byte("y")})); err != nil {
		t.Fatalf("apply second state: %v", err)
	}
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	out := string(drainQueue(queue))
	if !strings.Contains(out, "mosh: Last reply 11 seconds ago.") {
		t.Fatalf("reply count-up output = %q", out)
	}

	// Acknowledging now clears both late conditions; the next timer draw
	// removes the bar. The injected clock reads itself, so the wall timestamp
	// passed by the transport is ignored.
	renderer.serverAcked(time.Time{})
	now = 12000
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain after ack: %v", err)
	}
	out = string(drainQueue(queue))
	if strings.Contains(out, "Last reply") {
		t.Fatalf("reply bar survived the acknowledgement: %q", out)
	}
}

// TestRendererNetworkErrorNotification covers set/clear and expiry of stock's
// sendto error string.
func TestRendererNetworkErrorNotification(t *testing.T) {
	now := uint64(0)
	renderer, queue := newOverlayTestRenderer(t, func() uint64 { return now })

	renderer.setNetworkError("sendto: no route to host")
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	out := string(drainQueue(queue))
	for _, want := range []string{"mosh: sendto: no route to host", "[To quit: Ctrl-^ .]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("network-error output %q does not contain %q", out, want)
		}
	}

	// set_network_error expires after ACK_INTERVAL + 100ms.
	now = 3101
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain at expiry: %v", err)
	}
	if out := string(drainQueue(queue)); strings.Contains(out, "no route to host") {
		t.Fatalf("expired network error was redrawn: %q", out)
	}

	// clear_network_error shortens a live error to one second.
	now = 4000
	renderer.setNetworkError("sendto: host unreachable")
	now = 4500
	renderer.clearNetworkError()
	now = 5501
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain after clear: %v", err)
	}
	if out := string(drainQueue(queue)); strings.Contains(out, "host unreachable") {
		t.Fatalf("cleared network error was redrawn: %q", out)
	}
}

// failingLink is a datagramLink whose writes fail, for the transport-level
// network-error wiring test.
type failingLink struct {
	writeErr error
}

func (l *failingLink) writeOne([]byte) error           { return l.writeErr }
func (l *failingLink) readFrom([]byte) (int, error)    { return 0, io.EOF }
func (l *failingLink) setReadDeadline(time.Time) error { return nil }
func (l *failingLink) close() error                    { return nil }

// TestTransportWriteErrorShowsNetworkNotification proves the transport's send
// path reaches SetNetworkError/ClearNetworkError, not just the renderer API.
func TestTransportWriteErrorShowsNetworkNotification(t *testing.T) {
	now := uint64(0)
	renderer, queue := newOverlayTestRenderer(t, func() uint64 { return now })
	link := &failingLink{writeErr: errors.New("no route to host")}
	transport := &inProcessTransport{
		queue:    queue,
		renderer: renderer,
		link:     link,
		done:     make(chan struct{}),
	}

	if err := transport.writeDatagrams([][]byte{[]byte("datagram")}); err != nil {
		t.Fatalf("transient send failure escaped as an error: %v", err)
	}
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	if out := string(drainQueue(queue)); !strings.Contains(out, "mosh: sendto: no route to host") {
		t.Fatalf("transport send error did not reach the notification bar: %q", out)
	}

	link.writeErr = nil
	if err := transport.writeDatagrams([][]byte{[]byte("datagram")}); err != nil {
		t.Fatalf("successful send: %v", err)
	}
	now = 1101
	if err := renderer.maintainOverlays(); err != nil {
		t.Fatalf("maintain after recovery: %v", err)
	}
	if out := string(drainQueue(queue)); strings.Contains(out, "no route to host") {
		t.Fatalf("network error survived a successful send: %q", out)
	}
}

// TestNetworkErrorMessageMatchesStock covers socket-error formatting: stock
// shows "sendto: " or "recvmsg: " plus strerror, never Go's decorated
// "write udp 127.0.0.1:...: sendto: ..." shape or a doubled operation prefix.
func TestNetworkErrorMessageMatchesStock(t *testing.T) {
	opErr := &net.OpError{
		Op:  "write",
		Net: "udp",
		Err: os.NewSyscallError("sendto", syscall.ENETUNREACH),
	}
	if got, want := networkErrorMessage("sendto", opErr),
		"sendto: "+syscall.ENETUNREACH.Error(); got != want {
		t.Fatalf("formatted send error = %q, want %q", got, want)
	}
	if got, want := networkErrorMessage("recvmsg", errors.New("boom")), "recvmsg: boom"; got != want {
		t.Fatalf("formatted recv error = %q, want %q", got, want)
	}
}

// TestTransportCallsServerAcked proves the read loop reports the server's
// acknowledgement timestamp from a real Receive, as process_network_input does.
func TestTransportCallsServerAcked(t *testing.T) {
	server := startFakeServer(t)
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcement()},
		ProbeTimeout: 500 * time.Millisecond,
		clock:        func() uint64 { return 42 },
	}
	transport, err := factory.Open(context.Background(), inProcessTestSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	readUntil(t, transport, "fake$ ", 2*time.Second)

	if _, err := transport.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitInput(t, server, "echo hi", time.Second)
	readUntil(t, transport, "hi\r\n", 2*time.Second)

	inner := transport.(*inProcessTransport)
	inner.renderer.mu.Lock()
	acked := inner.renderer.lastServerAck
	inner.renderer.mu.Unlock()
	if acked != 42 {
		t.Fatalf("server ack timestamp = %d, want 42 from the injected clock", acked)
	}
}

// TestRendererCloseRestoresTerminal requires the queued shutdown bytes to
// reset application-cursor-key mode, styles, cursor visibility, and mouse
// reporting, as Display::close does.
func TestRendererCloseRestoresTerminal(t *testing.T) {
	queue := newOutputQueue(1 << 20)
	renderer := newTerminalRendererWithOptions(queue, 8, 3,
		terminalRendererOptions{Display: terminal.DefaultDisplayOptions()})
	renderer.setScreenSizeHandler(func(int, int, uint64) {})
	if err := renderer.emitInitial(); err != nil {
		t.Fatalf("emit initial: %v", err)
	}
	drainQueue(queue)
	renderer.close()
	out := string(drainQueue(queue))
	if !strings.Contains(out, "\x1b[?1l") || !strings.Contains(out, "\x1b[?25h") {
		t.Fatalf("close output = %q, want DECCKM reset and cursor shown", out)
	}
	if strings.Contains(out, "\x1b[?1049l") {
		t.Fatalf("desktop close must not leave an alternate screen: %q", out)
	}
}

// TestOutputQueueSealsFinalBytes proves a normal push racing renderer.close
// cannot land after the display-restoration bytes. The queue is full, so the
// late push must either be rejected by the seal or woken and rejected.
func TestOutputQueueSealsFinalBytes(t *testing.T) {
	queue := newOutputQueue(1)
	if err := queue.push([]byte("old")); err != nil {
		t.Fatalf("initial push: %v", err)
	}
	late := make(chan error, 1)
	go func() { late <- queue.push([]byte("late")) }()

	queue.pushFinal([]byte("final"))
	select {
	case err := <-late:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("late push error = %v, want io.ErrClosedPipe", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a push blocked behind the final bytes was never released")
	}
	queue.pushFinal([]byte("close"))
	if got := string(drainQueue(queue)); got != "oldfinalclose" {
		t.Fatalf("queue after close = %q, want oldfinalclose", got)
	}
}

// TestDisplayAlternateScreenExit verifies the stand-alone terminal capability
// path used by stock mosh-client with terminfo smcup/rmcup.
func TestDisplayAlternateScreenExit(t *testing.T) {
	display := terminal.NewDisplay(terminal.XtermDisplayOptions())
	if got := display.Open(); got != "\x1b[?1049h\x1b[?1h" {
		t.Fatalf("open = %q", got)
	}
	closeBytes := display.Close()
	if !strings.HasPrefix(closeBytes, "\x1b[?1l\x1b[0m\x1b[?25h") ||
		!strings.HasSuffix(closeBytes, "\x1b[?1049l") {
		t.Fatalf("close = %q", closeBytes)
	}
}

// Ensure the test helper's event conversion fails loudly if a golden event has
// neither form.
func TestGoldenEventsAreWellFormed(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "..", "internal", "mosh", "terminal",
		"testdata", "parity", "goldens", "*.json"))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var golden goldenFileJSON
		if err := json.Unmarshal(data, &golden); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		for i, event := range golden.Events {
			if len(event.Input) == 0 && len(event.Resize) != 2 {
				t.Fatalf("%s event %d is neither input nor resize: %+v", path, i, event)
			}
			if len(event.Resize) != 0 && len(event.Resize) != 2 {
				t.Fatalf("%s event %d has malformed resize %v", path, i, event.Resize)
			}
		}
		if len(golden.Frames) != len(golden.Events)+1 || len(golden.Screens) != len(golden.Frames) {
			t.Fatalf("%s frame/screen count mismatch: %d frames, %d screens, %d events",
				path, len(golden.Frames), len(golden.Screens), len(golden.Events))
		}
	}
}
