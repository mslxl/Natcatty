package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

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
			m.SessionLoggingOn(),
			m.SessionLoggingStop(),
			m.SessionLogFormatText(),
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

		// Within the status bar, the activity dot and command text do not
		// overlap the logging state or the controls.
		if status.X+status.W > rects[m.SessionCommandRunning("make test")].X+epsilon {
			t.Fatalf("%dx%d activity dot overlaps the command text", size[0], size[1])
		}
		command := rects[m.SessionCommandRunning("make test")]
		logging := rects[m.SessionLoggingOn()]
		if command.X+command.W > logging.X+epsilon {
			t.Fatalf("%dx%d command text overlaps the logging state: command %+v logging %+v",
				size[0], size[1], command, logging)
		}
		if logging.X+logging.W > rects[m.SessionLogFormatText()].X+epsilon {
			t.Fatalf("%dx%d logging state overlaps the format select", size[0], size[1])
		}
		stop := rects[m.SessionLoggingStop()]
		if rects[m.SessionLogFormatText()].X+rects[m.SessionLogFormatText()].W > stop.X+epsilon {
			t.Fatalf("%dx%d format select overlaps the log button", size[0], size[1])
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
	if !tester.HasText(m.SessionLoggingStart()) {
		t.Fatalf("Start is missing: %v", tester.Texts())
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
