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
			chromeFrame(c, model, locales.In(c.Locale()), c.Theme(), chromeActions{})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		wanted := []string{
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

		// The toolbar, log detail, transfer banner, and clipboard banner are
		// four stacked rows; each row starts after the previous one.
		menu := rects[m.SessionMenuLabel()]
		detail := rects[m.SessionLoggingPath(model.Logging.Path)]
		transfer := rects[m.SessionTransferActive("ZMODEM")]
		clipboard := rects[m.SessionClipboardRequest()]
		if !(menu.Y < detail.Y && detail.Y < transfer.Y && transfer.Y < clipboard.Y) {
			t.Fatalf("%dx%d row order = menu %+v detail %+v transfer %+v clipboard %+v",
				size[0], size[1], menu, detail, transfer, clipboard)
		}
		allow := rects[m.SessionClipboardAllow()]
		deny := rects[m.SessionClipboardDeny()]
		if allow.X+allow.W > deny.X+epsilon {
			t.Fatalf("%dx%d clipboard buttons overlap", size[0], size[1])
		}
	}
}

// TestChromeRefusesLoggingWithoutADirectory keeps Start visible but refused
// when Settings has no directory yet, without a standing hint above the
// terminal of a user who never logs.
func TestChromeRefusesLoggingWithoutADirectory(t *testing.T) {
	m := locales.In("en")
	tester := ui.NewTester(func(c *ui.Context) {
		chromeFrame(c, chromeModel{
			Command:   appsession.CommandState{},
			LogFormat: domainterminal.LogFormatText,
		}, locales.In(c.Locale()), c.Theme(), chromeActions{})
	}, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if tester.HasText(m.SessionLoggingNoDirectory()) {
		t.Fatalf("the missing directory shows as a standing hint: %v", tester.Texts())
	}
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

// TestChromeSaysNothingWhenTheSessionHasNothingToSay keeps the terminal
// right under the Pane header: an idle Session with no log folder shows no
// status line, no "No command", and no hint to choose a folder.
func TestChromeSaysNothingWhenTheSessionHasNothingToSay(t *testing.T) {
	m := locales.In("en")
	tester := ui.NewTester(func(c *ui.Context) {
		renderChrome(c, chromeModel{
			LogFormat: domainterminal.LogFormatText,
		}, locales.In(c.Locale()), c.Theme(), chromeActions{})
	}, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	for _, unwanted := range []string{
		m.SessionCommandIdle(), m.SessionLoggingNoDirectory(), m.SessionMenuLabel(),
	} {
		if tester.HasText(unwanted) {
			t.Fatalf("an idle Session shows %q: %v", unwanted, tester.Texts())
		}
	}
	if texts := tester.Texts(); len(texts) != 0 {
		t.Fatalf("an idle Session's chrome shows %v, want nothing", texts)
	}
}

// TestChromeShowsLogFailure keeps the failure text in the row under the
// status bar rather than replacing the path.
func TestChromeShowsLogFailure(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(func(c *ui.Context) {
			chromeFrame(c, chromeModel{
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

func TestChromeAutocompleteFitsAtDefaultAndMinimumSizes(t *testing.T) {
	m := locales.In("en")
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
		}
		tester := ui.NewTester(func(c *ui.Context) {
			chromeFrame(c, model, locales.In(c.Locale()), c.Theme(), chromeActions{})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		wanted := []string{
			m.SessionMenuLabel(),
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
		if !(rects[m.SessionMenuLabel()].Y < rects[m.SessionAutocompleteLabel()].Y) {
			t.Fatalf("%dx%d rows are out of order: menu %+v popup %+v",
				size[0], size[1], rects[m.SessionMenuLabel()],
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

// chromeFrame draws the chrome under the Pane toolbar, as a Pane header and
// its Session do in the shell.
func chromeFrame(c *ui.Context, model chromeModel, m locales.Messages, t *ui.Theme, actions chromeActions) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(30).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Column(c).Grow(1)
			toolbar(c, model, m, t, actions)
		})
		renderChrome(c, model, m, t, actions)
	})
}

// surfaceFrame draws a Surface under its toolbar, as the shell's Pane does.
func surfaceFrame(c *ui.Context, surface *Surface, focused bool) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(30).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Column(c).Grow(1)
			surface.Toolbar(c)
		})
		ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			if focused {
				surface.ViewFocused(c, true)
			} else {
				surface.View(c)
			}
		})
	})
}
