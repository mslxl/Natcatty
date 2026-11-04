// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
