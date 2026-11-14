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

func TestTailFromKeepsOnlyPromptOrRunningCommand(t *testing.T) {
	lines := [][]ui.Span{
		{{Text: "finished output"}},
		{{Text: "PS C:/work> git status"}},
		{{Text: "status output"}},
		{{Text: "PS C:/work> "}},
	}
	if got := tailFrom(lines, 3, Tail{Prompt: "PS C:/work>"}); got != 3 {
		t.Fatalf("idle tail starts at %d, want 3", got)
	}
	if got := tailFrom(lines, 3, Tail{Running: "git status"}); got != 1 {
		t.Fatalf("running tail starts at %d, want 1", got)
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
			nil, Bell{Col: -1, Row: -1}, Tail{Prompt: "PS C:/work>", Running: "running"},
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
