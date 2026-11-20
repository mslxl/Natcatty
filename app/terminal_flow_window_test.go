package app

// A regression test for the reported "input runs out of the window and cannot
// be scrolled to" behavior: when the flow (finished command rows plus the live
// screen) is taller than the Pane, the newest prompt and its cursor must stay
// inside the window at the flow's end. The Surface used to size its root
// column to the flow's content, so it could not shrink by the Pane header
// above it and the flow's last rows were laid out below the window.

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	sessionui "github.com/mslxl/natcatty/ui/session"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// newFlowWindowState is a local Session whose shell has a deterministic,
// incrementing prompt and whose cursor is a steady red block: the test can
// then name the newest prompt row and find the cursor without waiting out a
// blink. It skips when zsh is unavailable, as the other real-shell tests do.
func newFlowWindowState(t *testing.T) *shellstate.State {
	return newFlowWindowStateWithPrompt(t, "PS1=\"P${__flow_n}> \"")
}

// newFlowWindowStateWithPrompt is newFlowWindowState with a caller-chosen
// prompt assignment; prompt is a zsh statement run from precmd after the
// counter increments, so it can set PS1 from $'...' text with real newlines.
func newFlowWindowStateWithPrompt(t *testing.T, prompt string) *shellstate.State {
	t.Helper()
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed here")
	}
	home := t.TempDir()
	// The integration points ZDOTDIR at its own directory and sources the
	// user's files from $HOME, so the test prompt lives in $HOME/.zshrc.
	zshrc := "setopt prompt_subst\n" +
		"PROMPT_EOL_MARK=''\n" +
		"unsetopt correct\n" +
		"HISTFILE=/dev/null\n" +
		"SAVEHIST=0\n" +
		"__flow_n=0\n" +
		"precmd() { __flow_n=$((__flow_n + 1)); " + prompt + " }\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("NETCATTY_USER_ZDOTDIR", "")
	t.Setenv("NETCATTY_HOOK", "")
	t.Setenv("SHELL", shell)
	t.Setenv("PROMPT_EOL_MARK", "")
	t.Setenv("PS1", "")
	// Make the installed shell hook stay silent, as it does on machines where
	// starship or atuin owns the prompt: the flow then has no OSC 133;B
	// boundary, which is the rougher path the bug showed up on.
	t.Setenv("STARSHIP_SHELL", "zsh")
	t.Setenv("STARSHIP_SESSION_KEY", "")
	t.Setenv("ATUIN_SESSION", "")
	t.Setenv("ATUIN_SHLVL", "")
	t.Setenv("ITERM_SHELL_INTEGRATION_INSTALLED", "")

	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	theme := domaintheme.TerminalTheme{
		ID:   "flow-window-test",
		Name: "flow window test",
		Dark: true,
		Colors: domaintheme.TerminalColors{
			Background: "#000000",
			Foreground: "#ffffff",
			Cursor:     "#ff0000",
		},
	}
	st := shellstate.NewState(shellstate.WithSessions(service, func(s *appsession.Session) (shellstate.Surface, error) {
		return sessionui.New(s,
			sessionui.WithRedraw(func() {}),
			sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
			sessionui.WithDropAction(func() domainterminal.DropAction { return domainterminal.DropInsert }),
			sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
			sessionui.WithTerminalTheme(func() domaintheme.TerminalTheme { return theme }),
			sessionui.WithTerminalCursor(func() (domainpreferences.CursorStyle, bool) {
				return domainpreferences.CursorBlock, false
			}),
		)
	}))
	t.Cleanup(st.CloseAllSessions)
	return st
}

func flowPromptRect(tester *ui.Tester, index int) (ui.Rect, bool) {
	want := fmt.Sprintf("P%d>", index)
	for _, text := range tester.Texts() {
		if strings.TrimRight(text, " ") == want {
			return tester.Find(text)
		}
	}
	return ui.Rect{}, false
}

func flowWaitPrompt(t *testing.T, st *shellstate.State, tester *ui.Tester, index int, timeout time.Duration) ui.Rect {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st.Flush()
		tester.Frame()
		if rect, ok := flowPromptRect(tester, index); ok {
			for i := 0; i < 4; i++ {
				st.Flush()
				tester.Frame()
			}
			rect, _ = flowPromptRect(tester, index)
			return rect
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("prompt P%d> never appeared; texts=%q", index, tester.Texts())
	return ui.Rect{}
}

// flowCursorRows returns the red rows in the terminal area: the steady cursor
// is the theme's cursor color and nothing else in these rows is red.
func flowCursorRows(img *image.RGBA, minX int) []int {
	b := img.Bounds()
	if minX < b.Min.X {
		minX = b.Min.X
	}
	rows := map[int]bool{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := minX; x < b.Max.X; x++ {
			off := img.PixOffset(x, y)
			if img.Pix[off] > 96 && img.Pix[off+1] < 96 && img.Pix[off+2] < 96 {
				rows[y] = true
				break
			}
		}
	}
	var out []int
	for y := 0; y < 1<<20; y++ {
		if rows[y] {
			out = append(out, y)
		}
	}
	return out
}

// TestTerminalFlowKeepsTheInputLineInsideAShortWindow opens a short window,
// makes the flow taller than it with a command's output, then presses Enter.
// At every prompt the newest line and its cursor must be fully inside the
// window: the flow is at its end, so there is nothing to scroll to make it
// visible.
func TestTerminalFlowKeepsTheInputLineInsideAShortWindow(t *testing.T) {
	const (
		window = 460
		width  = 1200
	)
	st := newFlowWindowState(t)
	if err := st.NewTab(); err != nil {
		t.Fatalf("new tab: %v", err)
	}
	waitForRealShellSurfaces(t, st, 1)
	tester := ui.NewTester(shellui.View(st), width, window)
	tester.SetLocale("en")
	tester.Frame()
	// Focus the terminal: the cursor overlay only draws while focused.
	tester.ClickAt(width/2, window/2)
	tester.Frame()
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})

	check := func(index int) {
		t.Helper()
		rect := flowWaitPrompt(t, st, tester, index, 20*time.Second)
		if rect.H <= 0 || rect.Y+rect.H > window {
			t.Fatalf("prompt P%d> is not fully inside the %d-DIP window: rect=(%.1f,%.1f %.1fx%.1f) texts=%q",
				index, window, rect.X, rect.Y, rect.W, rect.H, tester.Texts())
		}
		rows := flowCursorRows(tester.Image(), int(rect.X)-2)
		if len(rows) == 0 {
			t.Fatalf("no cursor is drawn with prompt P%d> at (%.1f,%.1f)", index, rect.X, rect.Y)
		}
		top, bottom := rows[0], rows[len(rows)-1]
		if bottom >= window {
			t.Fatalf("cursor runs past the bottom of the %d-DIP window: rows %d..%d, prompt at %.1f", window, top, bottom, rect.Y)
		}
		if delta := top - int(rect.Y); delta < -15 || delta > 8 {
			t.Fatalf("cursor is not on prompt P%d>'s row: cursor top %d, prompt y %.1f", index, top, rect.Y)
		}
	}

	check(1)
	// A long output makes the flow taller than the Pane without needing dozens
	// of prompts; the live screen below it stays a normal grid.
	const prelude = "seq 1 200"
	tester.Type(prelude)
	typed := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st.Flush()
		tester.Frame()
		if realSurfaceSession(t, st, 0).Snapshot().Input == prelude {
			typed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !typed {
		t.Fatalf("the shell never received %q", prelude)
	}
	tester.Key(0, ui.KeyEnter)
	check(2)
	for step := 0; step < 6; step++ {
		tester.Key(0, ui.KeyEnter)
		check(3 + step)
	}
}
