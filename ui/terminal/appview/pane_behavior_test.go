package appview

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

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
	if got := tailFrom(lines, 3, Tail{Running: "git status", PromptRowKnown: true}); got != 1 {
		t.Fatalf("running tail starts at %d, want 1", got)
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

func TestTailFromDoesNotRequirePromptShapeWhenBoundaryIsRecorded(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "old command"}},
		{{Text: "output"}},
		{{Text: "ready [session=9c4f] "}},
	}
	if got := tailFrom(lines, 2, Tail{PromptRow: 2, PromptRowKnown: true}); got != 2 {
		t.Fatalf("custom prompt boundary starts at %d, want 2", got)
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
	if got := strings.Count(text, "PS C:/work> echo hi"); got != 1 {
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
