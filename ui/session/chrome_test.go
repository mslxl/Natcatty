package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestChromeFitsAtDefaultAndMinimumSizes renders the real status bar and
// banners at 1100x720 and the 720x480 minimum. Every text and control must stay
// inside the viewport and the rows must not overlap.
func TestChromeFitsAtDefaultAndMinimumSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		model := chromeModel{
			Command: appsession.CommandState{
				Text: "make test", Running: true,
			},
			Busy: true,
			Transfer: appsession.TransferState{
				Active: true, Kind: appsession.TransferZMODEM,
			},
			Logging: appsession.LogStatus{
				Active: true,
				Format: domainterminal.LogFormatText,
				Path:   `C:\Users\example\logs\host\2026-10-20T10-00-00.txt`,
			},
			ClipboardPending: true,
			LogFormat:        domainterminal.LogFormatText,
			LogDirectory:     `C:\Users\example\logs`,
		}
		tester := ui.NewTester(func(c *ui.Context) {
			renderChrome(c, model, locales.In(c.Locale()), c.Theme(), chromeActions{})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		wanted := []string{
			m.SessionActivityBusy(),
			m.SessionCommandRunning("make test"),
			m.SessionMenuLabel(),
			m.SessionLoggingPath(model.Logging.Path),
			m.SessionTransferActive("ZMODEM"),
			m.SessionTransferCancel(),
			m.SessionClipboardRequest(),
			m.SessionClipboardAllow(),
			m.SessionClipboardDeny(),
		}
		rects := make(map[string]ui.Rect, len(wanted))
		const epsilon = 0.5
		for _, text := range wanted {
			rect, ok := tester.Find(text)
			if !ok {
				t.Fatalf("%dx%d does not show %q: %v", size[0], size[1], text, tester.Texts())
			}
			if rect.X < -epsilon || rect.Y < -epsilon ||
				rect.X+rect.W > float32(size[0])+epsilon ||
				rect.Y+rect.H > float32(size[1])+epsilon {
				t.Fatalf("%dx%d clips %q: %+v", size[0], size[1], text, rect)
			}
			rects[text] = rect
		}

		// The status bar, log detail, transfer banner, and clipboard banner
		// are four stacked rows; each row starts after the previous one.
		status := rects[m.SessionActivityBusy()]
		detail := rects[m.SessionLoggingPath(model.Logging.Path)]
		transfer := rects[m.SessionTransferActive("ZMODEM")]
		clipboard := rects[m.SessionClipboardRequest()]
		if !(status.Y < detail.Y && detail.Y < transfer.Y && transfer.Y < clipboard.Y) {
			t.Fatalf("%dx%d row order = status %+v detail %+v transfer %+v clipboard %+v",
				size[0], size[1], status, detail, transfer, clipboard)
		}

		// Within the status bar, the activity dot, command text, and Session
		// menu do not overlap. The menu is the only fixed control at the right
		// edge, so it stays inside the Pane at every width.
		command := rects[m.SessionCommandRunning("make test")]
		if status.X+status.W > command.X+epsilon {
			t.Fatalf("%dx%d activity dot overlaps the command text", size[0], size[1])
		}
		menu := rects[m.SessionMenuLabel()]
		if command.X+command.W > menu.X+epsilon {
			t.Fatalf("%dx%d command text overlaps the Session menu: command %+v menu %+v",
				size[0], size[1], command, menu)
		}
		allow := rects[m.SessionClipboardAllow()]
		deny := rects[m.SessionClipboardDeny()]
		if allow.X+allow.W > deny.X+epsilon {
			t.Fatalf("%dx%d clipboard buttons overlap", size[0], size[1])
		}
	}
}

// TestChromeExplainsMissingLogDirectory shows why Start is disabled when
// Settings has no directory yet.
func TestChromeExplainsMissingLogDirectory(t *testing.T) {
	m := locales.In("en")
	tester := ui.NewTester(func(c *ui.Context) {
		renderChrome(c, chromeModel{
			Command:   appsession.CommandState{},
			LogFormat: domainterminal.LogFormatText,
		}, locales.In(c.Locale()), c.Theme(), chromeActions{})
	}, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(m.SessionLoggingNoDirectory()) {
		t.Fatalf("missing directory is not explained: %v", tester.Texts())
	}
	// Start is in the Session menu now; with no directory it must be visible
	// but refused.
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		t.Fatalf("open the Session menu: %v", err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuLogging(), m.SessionLoggingStart()); err == nil {
		t.Fatal("Start was choosable with no log directory")
	}
	if tester.HasText(m.SessionLoggingPath("")) {
		t.Fatalf("an empty path was rendered")
	}
}

// TestChromeKeepsTheCommandReadableAtTheMinimumWidth keeps the missing-folder
// sentence in the detail row so the command indicator still has room at
// 720x480. Before this, the sentence shared the status bar and squeezed the
// command down to its ellipsis.
func TestChromeKeepsTheCommandReadableAtTheMinimumWidth(t *testing.T) {
	m := locales.In("en")
	tester := ui.NewTester(func(c *ui.Context) {
		renderChrome(c, chromeModel{
			LogFormat: domainterminal.LogFormatText,
		}, locales.In(c.Locale()), c.Theme(), chromeActions{})
	}, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	command, ok := tester.Find(m.SessionCommandIdle())
	if !ok {
		t.Fatalf("the command indicator is missing: %v", tester.Texts())
	}
	if command.W < 120 {
		t.Fatalf("the command indicator is squeezed to %.1f DIPs: %+v", command.W, command)
	}
	hint, ok := tester.Find(m.SessionLoggingNoDirectory())
	if !ok {
		t.Fatalf("the missing directory is not explained: %v", tester.Texts())
	}
	if hint.Y < command.Y+command.H {
		t.Fatalf("the missing-directory hint shares the status bar: command %+v hint %+v", command, hint)
	}
}

// TestChromeShowsLogFailure keeps the failure text in the row under the
// status bar rather than replacing the path.
func TestChromeShowsLogFailure(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(func(c *ui.Context) {
			renderChrome(c, chromeModel{
				LogFormat:    domainterminal.LogFormatHTML,
				LogDirectory: `C:\logs`,
				LogProblem:   true,
			}, locales.In(c.Locale()), c.Theme(), chromeActions{})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		if !tester.HasText(m.SessionLoggingFailed()) {
			t.Fatalf("%dx%d does not show the logging failure: %v", size[0], size[1], tester.Texts())
		}
		if tester.HasText(m.SessionLoggingNoDirectory()) {
			t.Fatalf("%dx%d shows the no-directory hint despite a saved directory", size[0], size[1])
		}
	}
}

func TestChromeStatsAndAutocompleteFitAtDefaultAndMinimumSizes(t *testing.T) {
	m := locales.In("en")
	stats := appsession.Stats{
		InputBytes: 120, OutputBytes: 4096, Commands: 3,
		FailedCommands: 1, Bells: 2, Notifications: 4, Transfers: 5,
	}
	suggestions := []autocomplete.Suggestion{
		{Text: "git status", Display: "git status", Source: "history", Frequency: 3},
		{Text: "git stash", Display: "git stash", Source: "shell-history"},
		{Text: "git rebase --interactive", Display: "git rebase --interactive", Source: "history"},
		{Text: "git log --oneline", Display: "git log --oneline", Source: "history"},
		{Text: "git diff", Display: "git diff", Source: "history"},
		{Text: "git push", Display: "git push", Source: "history"},
		{Text: "git pull", Display: "git pull", Source: "history"},
	}
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		model := chromeModel{
			Command:          appsession.CommandState{Text: "git", Running: true},
			Busy:             true,
			LogFormat:        domainterminal.LogFormatText,
			LogDirectory:     `C:\logs`,
			Input:            "git",
			Suggestions:      suggestions,
			SuggestionIndex:  1,
			ShowAutocomplete: true,
			ShowGhost:        true,
			GhostText:        "git status",
			ShowStats:        true,
			Stats:            stats,
		}
		tester := ui.NewTester(func(c *ui.Context) {
			renderChrome(c, model, locales.In(c.Locale()), c.Theme(), chromeActions{})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		summary := m.SessionStatsSummary(120, 4096, 3, 1, 2, 4, 5)
		wanted := []string{
			m.SessionActivityBusy(),
			m.SessionCommandRunning("git"),
			m.SessionMenuLabel(),
			summary,
			m.SessionAutocompleteLabel(),
			m.SessionAutocompleteHint(),
			m.SessionAutocompleteGhost("git status"),
			suggestions[0].Display,
			suggestions[5].Display,
		}
		rects := make(map[string]ui.Rect, len(wanted))
		const epsilon = 0.5
		for _, text := range wanted {
			rect, ok := tester.Find(text)
			if !ok {
				t.Fatalf("%dx%d does not show %q: %v", size[0], size[1], text, tester.Texts())
			}
			if rect.X < -epsilon || rect.Y < -epsilon ||
				rect.X+rect.W > float32(size[0])+epsilon ||
				rect.Y+rect.H > float32(size[1])+epsilon {
				t.Fatalf("%dx%d clips %q: %+v", size[0], size[1], text, rect)
			}
			rects[text] = rect
		}
		// The popup is bounded to six rows: the seventh suggestion must not
		// be rendered.
		if tester.HasText(suggestions[6].Display) {
			t.Fatalf("%dx%d rendered more than six suggestions", size[0], size[1])
		}
		if !(rects[m.SessionActivityBusy()].Y < rects[summary].Y &&
			rects[summary].Y < rects[m.SessionAutocompleteLabel()].Y) {
			t.Fatalf("%dx%d rows are out of order: status %+v stats %+v popup %+v",
				size[0], size[1], rects[m.SessionActivityBusy()], rects[summary],
				rects[m.SessionAutocompleteLabel()])
		}
	}
}

func TestSuggestionSourceLabels(t *testing.T) {
	m := locales.In("en")
	if got := suggestionSourceLabel("history", m); got != m.SessionAutocompleteHistory() {
		t.Fatalf("history label = %q", got)
	}
	if got := suggestionSourceLabel("shell-history", m); got != m.SessionAutocompleteShellHistory() {
		t.Fatalf("shell label = %q", got)
	}
	if got := suggestionSourceLabel("plugin", m); got != "plugin" {
		t.Fatalf("unknown label = %q", got)
	}
}

func TestCommandTextCoversEveryCommandState(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		name    string
		command appsession.CommandState
		want    string
	}{
		{"idle", appsession.CommandState{}, m.SessionCommandIdle()},
		{"running with text", appsession.CommandState{Text: "go test", Running: true},
			m.SessionCommandRunning("go test")},
		{"running without text", appsession.CommandState{Running: true},
			m.SessionCommandRunning(m.SessionCommandIdle())},
		{"finished", appsession.CommandState{Text: "go test"},
			m.SessionCommandFinished("go test")},
		{"exited", appsession.CommandState{Text: "false", HasExitCode: true, ExitCode: 1},
			m.SessionCommandExited("false", 1)},
	}
	for _, test := range cases {
		if got := commandText(m, test.command); got != test.want {
			t.Fatalf("%s text = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestChromeFormatsRoundTrip(t *testing.T) {
	m := locales.In("en")
	labels := formatLabels(m)
	if len(labels) != len(chromeFormats) || len(labels) != 3 {
		t.Fatalf("format labels = %v", labels)
	}
	for index, format := range chromeFormats {
		got := formatAtIndex(indexOfFormat(format))
		if got != format {
			t.Fatalf("format %q round-tripped to %q", format, got)
		}
		if labels[index] == "" || strings.TrimSpace(labels[index]) == "" {
			t.Fatalf("format index %d has no label", index)
		}
	}
	if formatAtIndex(-1) != domainterminal.LogFormatText ||
		formatAtIndex(99) != domainterminal.LogFormatText {
		t.Fatal("out-of-range format did not fall back to text")
	}
}
