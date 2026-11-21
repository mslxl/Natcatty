package appview

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func TestTailFromKeepsOnlyRecordedPromptBoundaryOrRunningCommand(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "finished output"}},
		{{Text: "PS C:/work> git status"}},
		{{Text: "status output"}},
		{{Text: "PS C:/work> "}},
	}
	if got := tailFrom(lines, 3, Tail{PromptRow: 3, PromptRowKnown: true}); got != 3 {
		t.Fatalf("idle tail starts at %d, want recorded row 3", got)
	}
	// While a command runs the live area starts at the recorded prompt row,
	// which is where the finished block's captured prompt band starts, so the
	// band replaces rows already on screen and nothing moves at OSC 133;D.
	if got := tailFrom(lines, 3, Tail{Running: "git status", PromptRow: 1, PromptRowKnown: true}); got != 1 {
		t.Fatalf("running tail starts at %d, want the prompt row 1", got)
	}
	// The echoed command's row is the fallback for a boundary that does not
	// name this replay.
	if got := tailFrom(lines, 3, Tail{Running: "git status", PromptRow: 9, PromptRowKnown: true}); got != 1 {
		t.Fatalf("running tail with a stale boundary starts at %d, want the echo row 1", got)
	}
}

func TestTailFromUsesRawScreenWhenPromptBoundaryIsUnknown(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "custom prompt without a marker"}},
		{{Text: "output"}},
		{{Text: "another custom prompt"}},
	}
	if got := tailFrom(lines, 2, Tail{}); got != 0 {
		t.Fatalf("unknown prompt boundary starts at %d, want raw screen row 0", got)
	}
}

func TestTailFromKeepsConsecutiveEmptyPromptLines(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "PS C:/work> echo done"}},
		{{Text: "done"}},
		{{Text: "PS C:/work> "}},
		{{Text: "PS C:/work> "}},
		{{Text: "PS C:/work> "}},
	}
	if got := tailFrom(lines, 4, Tail{PromptRow: 2, PromptRowKnown: true}); got != 2 {
		t.Fatalf("idle tail starts at %d, want recorded row 2", got)
	}
}

func TestTailFromUsesRecordedPromptBoundaryForArbitraryPrompt(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "old command output"}},
		{{Text: "⟦2026-11-14T12:00:01Z|id=4f91⟧ "}},
		{{Text: "⟦2026-11-14T12:00:02Z|id=7a20⟧ "}},
		{{Text: "⟦2026-11-14T12:00:03Z|id=b812⟧ "}},
	}
	if got := tailFrom(lines, 3, Tail{
		PromptRow:      1,
		PromptRowKnown: true,
	}); got != 1 {
		t.Fatalf("recorded prompt boundary starts at %d, want 1", got)
	}
}

// lastContentRow scans the live rows and must tolerate a cursor that the
// mirror/visible crop put outside them (a clear-screen or a replay).
func TestLastContentRowToleratesACursorOutsideTheRows(t *testing.T) {
	if got := lastContentRow(nil, -5); got != -1 {
		t.Fatalf("lastContentRow(no rows, -5) = %d, want -1", got)
	}
	if got := lastContentRow(nil, 4); got != -1 {
		t.Fatalf("lastContentRow(no rows, 4) = %d, want -1", got)
	}
}

// The shell integration's boundary is a row recorded earlier. A shell that
// clears the screen without a new OSC 133;B (Ctrl+L) leaves the boundary below
// the cursor, and a prompt run always starts at or above its input: cropping at
// the stale row hid the redrawn prompt and the line being typed entirely, with
// no row to scroll to.
func TestTailFromClampsAStalePromptRowBelowTheCursor(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "old output"}},
		{{Text: "old prompt"}},
		{{Text: "more old output"}},
		{{Text: "PROMPT> typed after clear"}},
		{{Text: ""}},
	}
	if got := tailFrom(lines, 3, Tail{PromptRow: 6, PromptRowKnown: true}); got > 3 {
		t.Fatalf("stale boundary below the cursor starts the live area at %d, want at most the cursor row 3", got)
	}
	if got := tailFrom(lines, 3, Tail{PromptRow: 1, PromptRowKnown: true}); got != 1 {
		t.Fatalf("an in-range boundary above the cursor starts at %d, want 1", got)
	}
}

func TestPaneRendersConsecutiveEmptyPromptLines(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	term.Feed([]byte("PS C:/work> echo done\r\ndone\r\n" +
		"PS C:/work> \r\nPS C:/work> \r\nPS C:/work> "))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 8}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{PromptRow: 2, PromptRowKnown: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	tester.Frame()

	text := strings.Join(tester.Texts(), "\n")
	if got := strings.Count(text, "PS C:/work>"); got != 3 {
		t.Fatalf("rendered prompt count = %d, want 3 consecutive prompts: %q", got, text)
	}
}

// zsh's and bash's clear-screen (Ctrl+L) erase the screen and repaint the
// prompt without running precmd, so no OSC 133;B refreshes the recorded row.
// The recorded row still named the pre-clear prompt, below the redrawn one, and
// the live crop then started below the cursor: the user saw an empty live area
// and could not scroll the prompt or the line being typed back into view.
func TestPaneKeepsTheTypedLineAfterAClearScreenRedraw(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	term.Feed([]byte("old output\r\nPROMPT> "))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	// The boundary the session recorded at OSC 133;B, before the clear.
	tail := Tail{PromptRow: 1, PromptRowKnown: true}
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 8}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, tail, Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	tester.Frame()
	if text := strings.Join(tester.Texts(), "\n"); !strings.Contains(text, "PROMPT>") {
		t.Fatalf("the prompt is not drawn before the clear: %q", text)
	}

	term.Feed([]byte("\x1b[H\x1b[2JPROMPT> typed-after-clear"))
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	text := strings.Join(tester.Texts(), "\n")
	for _, want := range []string{"PROMPT>", "typed-after-clear"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the redrawn prompt's %q is missing after the clear: %q", want, text)
		}
	}
}

// The replay cap keeps the screen and the newest 400 rows of scrollback. A row
// recorded at OSC 133;B counts in the uncapped replay the session's mirror
// keeps; once output pushes the cap's window down, that index names a row below
// the prompt. Typing a wrapped command then cropped away the prompt and its
// first rows, so the input ran out of the live area with nothing to scroll to.
func TestPaneKeepsWrappedInputVisibleWhenTheReplayWindowSlides(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: 256 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 10)
	var history strings.Builder
	for i := 0; i < 380; i++ {
		fmt.Fprintf(&history, "line-%04d\r\n", i)
	}
	history.WriteString("PROMPT> ")
	term.Feed([]byte(history.String()))

	// The row the session records at OSC 133;B, counted before the cap. The
	// replay is still under the cap here, so the row is also the frame's own.
	row, alt, ok := CursorReplayRow(term.Snapshot(), Size{Cols: 40, Rows: 10})
	if !ok || alt {
		t.Fatalf("no main-screen cursor in the replay: ok=%t alt=%t", ok, alt)
	}
	if row != 380 {
		t.Fatalf("the prompt's recorded row = %d, want its row in the uncapped replay 380", row)
	}

	// Type a command long enough to push the replay past the cap. The window
	// slides as the rows are appended, and the prompt moves up inside it, while
	// the recorded row keeps its value.
	for i := 0; i < 60; i++ {
		term.Feed([]byte("typed-0123456789 "))
	}

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 10}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{PromptRow: row, PromptRowKnown: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	text := strings.Join(tester.Texts(), "\n")
	for _, want := range []string{"PROMPT>", "typed-0123"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the wrapped input's %q is missing after the window slid: %q", want, text)
		}
	}
}

func TestPaneKeepsEmptyPromptRunThenRendersFinishedCommandOnce(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(60, 10)

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tail := Tail{PromptRow: 0, PromptRowKnown: true}

	// Three empty Enters are three real prompt rows. They do not create a
	// command block, and they must all remain visible while the shell is idle.
	term.Feed([]byte(
		"PS C:/work> \r\n" +
			"PS C:/work> \r\n" +
			"PS C:/work> ",
	))
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 60, Rows: 10}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, tail, Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 360)
	tester.Frame()
	if got := strings.Count(strings.Join(tester.Texts(), "\n"), "PS C:/work>"); got != 3 {
		t.Fatalf("idle empty-prompt count = %d, want 3", got)
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("empty Enters created %d block(s), want 0", got)
	}

	// The next real command is one history item. Its command echo and output
	// must each be represented once by the finished block; the live tail keeps
	// only the newest prompt.
	term.Feed([]byte("echo hi\r\nhi\r\nPS C:/work> "))
	tail.PromptRow = 4
	id := store.Begin(model.Cmd{Line: "echo hi", Typed: "echo hi", Cwd: "C:/work"})
	store.Append([]byte("hi\n"))
	store.AddLine(model.Line{Text: "hi"})
	store.End(0)
	if got := len(store.Snapshot()); got != 1 {
		t.Fatalf("real command created %d block(s), want 1", got)
	}

	tester.Frame()
	text := strings.Join(tester.Texts(), "\n")
	// The block has no captured prompt rows (it was begun directly on the
	// store, with no shell mark), so its prompt row is the typed line alone.
	// The app no longer invents a prompt from the shell name: the real prompt
	// is captured from the screen (see Pane's PromptRows hook).
	if got := strings.Count(text, "echo hi"); got != 1 {
		t.Fatalf("command echo rendered %d times, want 1: %q", got, text)
	}
	outputs := 0
	for _, line := range tester.Texts() {
		if line == "hi" {
			outputs++
		}
	}
	if outputs != 1 {
		t.Fatalf("command output rendered %d times, want 1: %q", outputs, text)
	}
	if block := store.Block(id); block == nil || !block.Exited {
		t.Fatalf("real command block is not finished: %+v", block)
	}
}

// The flow used to carry its 10 DIP padding inside the scrolled content, so
// even a single idle prompt overflowed the viewport by 20 DIP: the scroll bar
// appeared with nothing to scroll, and the offset the layout allowed sat below
// the end Follow pinned, which read as "the reader scrolled up" and stopped the
// flow following the live line. Both showed up as input on the last row drifting
// out of the window.
func TestPaneFollowsTheIdleTailWithoutPhantomOverflow(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	term.Feed([]byte("PS C:/work> "))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 8}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{PromptRow: 0, PromptRowKnown: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	if scroll.MaxY != 0 || scroll.Y != 0 {
		t.Fatalf("idle flow scrolls: Y=%v MaxY=%v, want 0/0", scroll.Y, scroll.MaxY)
	}
	if !follow.Pinned() {
		t.Fatal("idle flow stopped following its tail")
	}
}

// Once the live screen is taller than the viewport the flow must keep
// following the cursor row: typing on the last line is what keeps it in view.
func TestPaneFollowsTheTailAfterItOverflows(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	var out strings.Builder
	for i := 0; i < 24; i++ {
		out.WriteString("line ")
		out.WriteString(strconv.Itoa(i))
		out.WriteString("\r\n")
	}
	term.Feed([]byte(out.String()))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 8}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	for i := 0; i < 6; i++ {
		tester.Frame()
	}
	if scroll.MaxY <= 0 {
		t.Fatalf("overflowing live screen reports MaxY=%v, want > 0", scroll.MaxY)
	}
	if !follow.Pinned() {
		t.Fatalf("flow stopped following the tail: Y=%v MaxY=%v", scroll.Y, scroll.MaxY)
	}
	if delta := scroll.Y - scroll.MaxY; delta < -1 || delta > 1 {
		t.Fatalf("follow left the offset at Y=%v, want the end MaxY=%v", scroll.Y, scroll.MaxY)
	}
	if !tester.HasText("line 23") {
		t.Fatal("the newest line is not rendered")
	}
}

func TestPaneUsesRawTerminalWhenPromptBoundaryIsUnknown(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(60, 8)
	term.Feed([]byte("history\r\nready [session=raw] "))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	store.Begin(model.Cmd{Line: "history", Typed: "history", Cwd: "C:/work"})
	store.Append([]byte("history\n"))
	store.AddLine(model.Line{Text: "history"})
	store.End(0)

	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 60, Rows: 8}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	tester.Frame()

	text := strings.Join(tester.Texts(), "\n")
	if got := strings.Count(text, "history"); got != 1 {
		t.Fatalf("raw terminal rendered history %d times, want once: %q", got, text)
	}
	if !strings.Contains(text, "ready [session=raw]") {
		t.Fatalf("raw terminal omitted its current prompt: %q", text)
	}
}

func TestFollowDetachesAboveEndAndRepinsAtEnd(t *testing.T) {
	var follow Follow
	scroll := &ui.ScrollState{Y: 100, MaxY: 100}
	if !follow.Update(scroll) || !follow.Pinned() {
		t.Fatal("initial flow did not pin to its end")
	}
	scroll.Y = 40
	if follow.Update(scroll) || follow.Pinned() {
		t.Fatal("scrolling up did not detach the flow")
	}
	scroll.Y = 100
	if !follow.Update(scroll) || !follow.Pinned() {
		t.Fatal("returning to the end did not repin the flow")
	}
}

func TestPaneUsesOneScrollForHistoryAndLiveReplay(t *testing.T) {
	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	for i := 0; i < 20; i++ {
		store.Begin(model.Cmd{Line: "echo history", Typed: "echo history", Cwd: "C:/work"})
		store.Append([]byte("history-row\n"))
		store.AddLine(model.Line{Text: "history-row"})
		store.End(0)
	}

	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("PS C:/work> running\r\nlive-row"))

	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 6}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{PromptRow: 0, PromptRowKnown: true, Running: "running"},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	tester.Frame()
	tester.Frame()
	texts := strings.Join(tester.Texts(), "\n")
	if !strings.Contains(texts, "history-row") || !strings.Contains(texts, "live-row") {
		t.Fatalf("history and live rows are not in the same Pane flow: %q", texts)
	}
	if scroll.MaxY <= 0 {
		t.Fatalf("Pane did not expose a unified scroll range: %+v", scroll)
	}
	if !follow.Pinned() {
		t.Fatal("Pane did not keep the unified flow pinned at its end")
	}
}

func TestPaneKeepsInputFocusWhenAHistoryBlockIsInserted(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	focus := true
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 6}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil,
			Hooks{Key: "stable-live", Focus: focus}, func(string) {})
	}, 900, 300)
	tester.Frame()

	// Inserting a finished command adds a sibling before the live screen. The
	// live input node must keep its identity, so the next frame does not need to
	// call Focus again.
	store.Begin(model.Cmd{Line: "echo history", Typed: "echo history", Cwd: "C:/work"})
	store.Append([]byte("history-row\n"))
	store.AddLine(model.Line{Text: "history-row"})
	store.End(0)
	focus = false
	tester.Frame()
	tester.TypeKey(0, ui.KeyX, "x")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && conn.Written() != "x" {
		time.Sleep(time.Millisecond)
	}
	if got := conn.Written(); got != "x" {
		t.Fatalf("input after history insertion reached %q, want x", got)
	}
}

// A multi-line prompt must reach the live area whole. The shell integration
// emits OSC 133;B before the prompt's text, so the recorded row is the prompt's
// first line while the cursor sits on its last one. Starting the tail at the
// cursor hid the first line and drew the cursor above the line being typed.
func TestPaneKeepsAMultiLinePromptAboveTheCursor(t *testing.T) {
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 8)
	term.Feed([]byte("~ via x\r\nat 10:37 > "))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 8}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{PromptRow: 0, PromptRowKnown: true, PromptAfterCommand: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	tester.Frame()

	text := strings.Join(tester.Texts(), "\n")
	if !strings.Contains(text, "~ via x") || !strings.Contains(text, "at 10:37 >") {
		t.Fatalf("two-line prompt was cropped to the cursor's row: %q", text)
	}
}

// copyMods is the chord the Pane takes Copy on: Command on macOS, and
// Control+Shift elsewhere so Control+C stays the program's interrupt.
func copyMods() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Cmd
	}
	return ui.Cmd | ui.Shift
}

// Dragging over the live screen selects its cells, the Copy gesture puts the
// exact text on the clipboard, and the keyboard stays on the terminal so the
// next key still reaches the program.
func TestPaneSelectsLiveTextAndKeepsTyping(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("hello world"))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var sel Selection
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 6}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil,
			Hooks{Key: "select-live", Focus: true, Selection: &sel}, func(string) {})
	}, 900, 300)
	tester.Frame()

	// The cursor cell at the end of the row is drawn as a blank, so the row's
	// text node carries it too.
	r, ok := tester.Find("hello world ")
	if !ok {
		t.Fatalf("live row not found: %q", tester.Texts())
	}
	tester.Press(r.X+1, r.Y+r.H/2)
	tester.Move(r.X+r.W-1, r.Y+r.H/2)
	tester.Release(r.X+r.W-1, r.Y+r.H/2)
	if sel.Empty() {
		t.Fatal("dragging over the live screen made no selection")
	}

	tester.Key(copyMods(), ui.KeyC)
	if got := tester.Clipboard(); got != "hello world" {
		t.Fatalf("copied %q, want %q", got, "hello world")
	}

	tester.TypeKey(0, ui.KeyX, "x")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && conn.Written() != "x" {
		time.Sleep(time.Millisecond)
	}
	if got := conn.Written(); got != "x" {
		t.Fatalf("typing after a selection reached %q, want x", got)
	}
}

// The same row numbers name different text on the main and alternate screens:
// a selection made over the shell must not copy vim's rows when the program
// takes the screen, and one made inside the program must not outlive it.
func TestSelectionDropsWhenTheScreenChanges(t *testing.T) {
	var sel Selection
	sel.SyncScreen(false)
	sel.Start(0, 0)
	sel.Drag(0, 4)
	sel.Release()
	sel.SyncScreen(false)
	if sel.Empty() {
		t.Fatal("the selection was dropped on the screen it was made on")
	}
	sel.SyncScreen(true)
	if !sel.Empty() {
		t.Fatal("the main screen's selection survived into the alternate screen")
	}
	sel.Start(2, 1)
	sel.Drag(2, 3)
	sel.SyncScreen(true)
	if sel.Empty() {
		t.Fatal("the alternate screen's selection was dropped on the same screen")
	}
	sel.SyncScreen(false)
	if !sel.Empty() {
		t.Fatal("the alternate screen's selection outlived the program")
	}
}

// The cell grid, not the drawn text, decides what a selection copies: a wide
// rune takes two columns and must not be cut in half.
func TestSelectionTextKeepsWideRunesWhole(t *testing.T) {
	sc := parseScreen([]byte("héllo 你好"), 20, 4, false)
	var sel Selection
	sel.Start(0, 1)
	sel.Drag(0, 6)
	if got := sc.selectionText(sel); got != "éllo 你" {
		t.Fatalf("selection text = %q, want %q", got, "éllo 你")
	}
	sel.Start(0, 0)
	sel.Drag(0, 0)
	if !sel.Empty() {
		t.Fatal("a press without a drag left a selection")
	}
}

// The word and line gestures take cells, not drawn text: a word is the run of
// non-blank cells under the pointer with a wide rune kept whole, and a line is
// the whole row. A double click on blank space stays a click.
func TestSelectWordAndLine(t *testing.T) {
	sc := parseScreen([]byte("hello,  world  你好"), 24, 4, false)
	var sel Selection
	sel.SelectWord(sc, 0, 0)
	a, b := sel.Bounds()
	if a.Row != 0 || a.Col != 0 || b.Row != 0 || b.Col != 5 {
		t.Fatalf("double click on hello selected %+v..%+v, want columns 0..5", a, b)
	}
	sel.SelectWord(sc, 0, 10)
	a, b = sel.Bounds()
	if a.Col != 8 || b.Col != 12 {
		t.Fatalf("double click on world selected columns %d..%d, want 8..12", a.Col, b.Col)
	}
	// The second half of a wide rune names the rune, which is selected whole.
	sel.SelectWord(sc, 0, 17)
	a, b = sel.Bounds()
	if a.Col != 15 || b.Col != 18 {
		t.Fatalf("double click on a wide rune selected columns %d..%d, want 15..18", a.Col, b.Col)
	}
	sel.SelectWord(sc, 0, 13)
	if !sel.Empty() {
		t.Fatalf("a double click on blank space selected %+v..%+v", a, b)
	}
	sel.SelectLine(sc, 0)
	if a, b = sel.Bounds(); a.Col != 0 || b.Col != 23 {
		t.Fatalf("triple click selected columns %d..%d, want the whole row 0..23", a.Col, b.Col)
	}
}

// A program that reports the mouse keeps plain clicks and drags, but Shift is
// still the Pane's selection gesture: the modifier skips the report, and the
// click starts a selection instead of doing nothing. Without it the report runs
// and no selection starts.
func TestShiftClickSelectsWhileAProgramReportsTheMouse(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("hello world\x1b[?1000h\x1b[?1006h"))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var sel Selection
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 6}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil,
			Hooks{Key: "shift-select", Focus: true, Selection: &sel}, func(string) {})
	}, 900, 300)
	tester.Frame()

	r, ok := tester.Find("hello world ")
	if !ok {
		t.Fatalf("live row not found: %q", tester.Texts())
	}
	y := r.Y + r.H/2
	tester.ClickAt(r.X+1, y)
	if !sel.Empty() {
		t.Fatal("a plain click started a selection while the program reports the mouse")
	}
	// The report reaches the connection from the input path, so the press and
	// its release can land after ClickAt returns. Wait for both before taking
	// the baseline; otherwise the plain click's late release looks like the
	// Shift-click's write.
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(conn.Written(), "\x1b[<0;") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("a plain click did not reach the program: %q", conn.Written())
		}
		time.Sleep(time.Millisecond)
	}

	before := conn.Written()
	cellW, _ := CellSize()
	shiftX := r.X + 4*cellW
	tester.ClickAtWith(ui.Shift, shiftX, y)
	// A click starts a selection where it lands: the anchor and the caret
	// are one cell until a drag moves it.
	if care := sel.Caret(); care != (Cell{Row: 0, Col: 4}) {
		t.Errorf("Shift-click selected at %+v, want column 4", care)
	}
	// Give the input path the same bounded moment before reading the writes.
	deadline = time.Now().Add(250 * time.Millisecond)
	for conn.Written() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := conn.Written(); got != before {
		t.Errorf("Shift-click reached the program: added %q", strings.TrimPrefix(got, before))
	}
}

// Double and triple clicks take a word and a line, as a terminal's do, and a
// second click is counted as a double click at the same point.
func TestDoubleClickSelectsAWord(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 6)
	term.Feed([]byte("hello world"))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	var sel Selection
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 6}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1}, Tail{}, Cursor{}, nil,
			Hooks{Key: "double-click", Focus: true, Selection: &sel}, func(string) {})
	}, 900, 300)
	tester.Frame()

	r, ok := tester.Find("hello world ")
	if !ok {
		t.Fatalf("live row not found: %q", tester.Texts())
	}
	cellW, _ := CellSize()
	x, y := r.X+2*cellW, r.Y+r.H/2
	tester.ClickAt(x, y)
	tester.ClickAt(x, y)
	if a, b := sel.Bounds(); a.Col != 0 || b.Col != 4 {
		t.Fatalf("a double click selected columns %d..%d, want the word 0..4", a.Col, b.Col)
	}
	tester.ClickAt(x, y)
	if a, b := sel.Bounds(); a.Col != 0 || b.Col != 39 {
		t.Fatalf("a triple click selected columns %d..%d, want the whole row 0..39", a.Col, b.Col)
	}
}

// findTextPrefix returns the box of the first element whose text starts with
// prefix: the cursor cell adds a trailing blank to the row it sits on.
func findTextPrefix(t *ui.Tester, prefix string) (ui.Rect, bool) {
	for _, text := range t.Texts() {
		if strings.HasPrefix(text, prefix) {
			return t.Find(text)
		}
	}
	return ui.Rect{}, false
}

// replayLines is the replay's rows as plain trimmed strings, for messages.
func replayLines(lines [][]ui.Span) []string {
	out := make([]string, len(lines))
	for i, spans := range lines {
		var line strings.Builder
		for _, sp := range spans {
			line.WriteString(sp.Text)
		}
		out[i] = strings.TrimRight(line.String(), " ")
	}
	return out
}

// screenRowContaining is the absolute row of the first replay row holding want.
func screenRowContaining(t *testing.T, term *terminal.Terminal, size Size, want string) int {
	t.Helper()
	frame := ScreenFrame(term, size, false)
	for i, spans := range frame.Lines {
		var line strings.Builder
		for _, sp := range spans {
			line.WriteString(sp.Text)
		}
		if strings.Contains(line.String(), want) {
			return i
		}
	}
	t.Fatalf("the replayed screen has no row holding %q: %q", want, replayLines(frame.Lines))
	return -1
}

// fullScreenTerminal is a 40x10 terminal whose main screen holds some history
// and a prompt, then a full-screen program's alternate screen: a title row, a
// body, and its command line on the bottom row, where the cursor sits.
func fullScreenTerminal(t *testing.T, historyRows int) (*terminal.Terminal, int) {
	t.Helper()
	term, err := terminal.New(terminal.Options{Conn: newNopConn(), Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	term.Resize(40, 10)
	var main strings.Builder
	for i := 0; i < historyRows; i++ {
		main.WriteString(fmt.Sprintf("old-%02d\r\n", i))
	}
	main.WriteString("PROMPT> vim\r\n")
	term.Feed([]byte(main.String()))
	promptRow := screenRowContaining(t, term, Size{Cols: 40, Rows: 10}, "PROMPT> vim")
	term.Feed([]byte("\x1b[?1049h\x1b[2J\x1b[H"))
	term.Feed([]byte("vim title\r\n~~ row\r\n~\r\n~\r\n~\r\n~\r\n~\r\n~\r\n~\r\n"))
	// vim's command line, on the screen's last row: the cursor is there.
	term.Feed([]byte("\x1b[10;1H:q!"))
	return term, promptRow
}

// A full-screen program owns the alternate screen, and its snapshot holds that
// screen alone, with no scrollback. The shell integration's prompt row (and a
// running command's echo) name rows the replay no longer has. Cropping to them
// kept only the rows from there or from the cursor down: the program's own rows
// above the cursor were not drawn at all, so typing on the bottom rows ran out
// of the window and no scroll could reveal them.
func TestPaneKeepsAFullScreenProgramsWholeScreen(t *testing.T) {
	for _, historyRows := range []int{2, 12} {
		t.Run(fmt.Sprintf("prompt at %d rows of history", historyRows), func(t *testing.T) {
			term, promptRow := fullScreenTerminal(t, historyRows)

			eng := lens.NewEngine()
			store := model.NewStore(eng)
			eng.Attach(store)
			var scroll ui.ScrollState
			var follow Follow
			tester := ui.NewTester(func(c *ui.Context) {
				Pane(c, store, eng, term, Size{Cols: 40, Rows: 10}, nil, &scroll, &follow,
					nil, Bell{Col: -1, Row: -1},
					Tail{PromptRow: promptRow, PromptRowKnown: true, PromptAfterCommand: true},
					Cursor{}, nil, Hooks{}, func(string) {})
			}, 900, 300)
			for i := 0; i < 4; i++ {
				tester.Frame()
			}

			texts := strings.Join(tester.Texts(), "\n")
			for _, want := range []string{"vim title", "~~ row", ":q!"} {
				if !strings.Contains(texts, want) {
					t.Fatalf("the full-screen program's row %q is missing: %q", want, texts)
				}
			}
			// The whole screen is drawn in order, top row first.
			if strings.Index(texts, "vim title") > strings.Index(texts, ":q!") {
				t.Fatalf("the full-screen rows are not in screen order: %q", texts)
			}
			// And the bottom row, where the program takes its input, is inside
			// the window rather than below it.
			// The cursor cell adds a blank to the row's text node, so the
			// exact match cannot name the command line.
			bottom, ok := findTextPrefix(tester, ":q!")
			if !ok {
				t.Fatalf("the program's command line is not laid out: %q", texts)
			}
			if bottom.Y+bottom.H > 300 {
				t.Fatalf("the program's command line is below the window: %+v", bottom)
			}
		})
	}
}

// Mouse reports carry screen coordinates. With the live area cropped to the
// prompt row, a full-screen program's clicks named rows shifted by the
// scrollback above the screen.
func TestPaneReportsFullScreenMouseInScreenRows(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	term.Resize(40, 10)
	term.Feed([]byte("old-00\r\nold-01\r\nold-02\r\nold-03\r\nold-04\r\nold-05\r\nPROMPT> vim\r\n"))
	promptRow := screenRowContaining(t, term, Size{Cols: 40, Rows: 10}, "PROMPT> vim")
	term.Feed([]byte("\x1b[?1049h\x1b[2J\x1b[Htop row\r\n\x1b[10;1H:x\x1b[?1000h\x1b[?1006h"))

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 10}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{PromptRow: promptRow, PromptRowKnown: true, PromptAfterCommand: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	row, ok := findTextPrefix(tester, "top row")
	if !ok {
		t.Fatalf("the program's first row is missing: %q", tester.Texts())
	}
	cellW, cellH := CellSize()
	if cellW <= 0 || cellH <= 0 {
		t.Fatal("the live cells were not measured")
	}
	// The third cell of the screen's first row: the report counts from 1.
	tester.Press(row.X+2*cellW+cellW/2, row.Y+cellH/2)
	// The program's reply is written from the input path; on a loaded runner it
	// can land just after Press returns, so give it a bounded moment rather than
	// reading once.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(conn.Written(), "\x1b[<0;3;1M") {
		if time.Now().After(deadline) {
			t.Fatalf("mouse press reported %q, want the screen's first row", conn.Written())
		}
		time.Sleep(time.Millisecond)
	}
}

// A program on the main screen counts mouse rows from its own screen, not from
// the first scrollback row the replay keeps. The click was reported at the
// replay's absolute row, so a program with mouse tracking below some output
// (readline's mouse support, fzf --height) selected a row shifted by the
// scrollback length.
func TestPaneReportsMainScreenMouseInScreenRows(t *testing.T) {
	conn := newNopConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: 64 << 10})
	if err != nil {
		t.Skipf("the terminal plugin is unavailable here: %v", err)
	}
	defer func() { _ = term.Close() }()
	size := Size{Cols: 40, Rows: 10}
	term.Resize(size.Cols, size.Rows)
	term.Feed([]byte("old-00\r\nold-01\r\nold-02\r\nold-03\r\nold-04\r\nold-05\r\nPROMPT> prog\r\n"))
	for i := 0; i < 12; i++ {
		term.Feed([]byte(fmt.Sprintf("out-%02d\r\n", i)))
	}
	term.Feed([]byte("\x1b[?1000h\x1b[?1006h"))
	promptRow := screenRowContaining(t, term, size, "PROMPT> prog")

	// Where the program's row of out-03 sits in the replay, and what its own
	// screen counts it as.
	frame := ScreenFrame(term, size, false)
	absolute := -1
	for i, line := range replayLines(frame.Lines) {
		if strings.HasPrefix(line, "out-03") {
			absolute = i
		}
	}
	if absolute < 0 || frame.sc == nil {
		t.Fatalf("out-03 is not in the replay: %q", replayLines(frame.Lines))
	}
	if frame.sc.top == 0 {
		t.Fatal("the test needs scrollback above the program's screen")
	}
	want := absolute - frame.sc.top + 1

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, size, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{PromptRow: promptRow, PromptRowKnown: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 400)
	for i := 0; i < 3; i++ {
		tester.Frame()
	}
	row, ok := findTextPrefix(tester, "out-03")
	if !ok {
		t.Fatalf("the program's row is not drawn: %q", tester.Texts())
	}
	cellW, cellH := CellSize()
	if cellW <= 0 || cellH <= 0 {
		t.Fatal("the live cells were not measured")
	}
	tester.Press(row.X+cellW/2, row.Y+cellH/2)
	// The report reaches the connection from the input path, so it can land
	// just after Press returns on a loaded runner.
	wantSeq := fmt.Sprintf("\x1b[<0;1;%dM", want)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(conn.Written(), wantSeq) {
		if time.Now().After(deadline) {
			t.Fatalf("mouse press reported %q, want %q", conn.Written(), wantSeq)
		}
		time.Sleep(time.Millisecond)
	}
}

// Leaving the alternate screen brings the main screen and its scrollback back,
// so the prompt boundary crops the live area again: the finished history must
// not reappear under the live prompt.
func TestPaneRestoresThePromptTailAfterAFullScreenProgramExits(t *testing.T) {
	term, _ := fullScreenTerminal(t, 12)
	term.Feed([]byte("\x1b[?1049l"))
	term.Feed([]byte("PROMPT> next\r\n"))
	promptRow := screenRowContaining(t, term, Size{Cols: 40, Rows: 10}, "PROMPT> next")
	if strings.Contains(strings.Join(replayLines(ScreenFrame(term, Size{Cols: 40, Rows: 10}, false).Lines), "\n"), "vim title") {
		t.Fatal("the test's main screen still shows the alternate screen")
	}

	eng := lens.NewEngine()
	store := model.NewStore(eng)
	eng.Attach(store)
	var scroll ui.ScrollState
	var follow Follow
	tester := ui.NewTester(func(c *ui.Context) {
		Pane(c, store, eng, term, Size{Cols: 40, Rows: 10}, nil, &scroll, &follow,
			nil, Bell{Col: -1, Row: -1},
			Tail{PromptRow: promptRow, PromptRowKnown: true, PromptAfterCommand: true},
			Cursor{}, nil, Hooks{}, func(string) {})
	}, 900, 300)
	tester.Frame()
	tester.Frame()

	texts := strings.Join(tester.Texts(), "\n")
	if !strings.Contains(texts, "PROMPT> next") {
		t.Fatalf("the live prompt is missing: %q", texts)
	}
	if strings.Contains(texts, "old-00") {
		t.Fatalf("the finished history was replayed under the live prompt: %q", texts)
	}
}
