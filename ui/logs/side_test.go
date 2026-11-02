package logs

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/ui/locales"
)

type sentCommand struct {
	command string
	run     bool
}

func newHistoryPanelTester(t *testing.T, ok bool) (*ui.Tester, *[]sentCommand) {
	t.Helper()
	source := seededSource()
	source.entries = append(source.entries, domainhistory.Entry{
		ID: "entry-other", SessionID: "session-2", HostID: "host-2", HostLabel: "db-01",
		Scope: "host:host-2", Command: "pg_dump prod", Source: domainhistory.SourceSession,
		RecordedAt: time.Now().Add(-90 * time.Minute),
	})
	var sent []sentCommand
	panel := NewHistoryPanel(NewState(source, &fakeActions{}), func(command string, run bool) bool {
		sent = append(sent, sentCommand{command, run})
		return ok
	})
	tt := ui.NewTester(panel.View("host-1"), 360, 600)
	tt.SetLocale("en")
	tt.Frame()
	return tt, &sent
}

func TestHistoryPanelListsThisHostThenEveryHost(t *testing.T) {
	m := locales.In("en")
	tt, _ := newHistoryPanelTester(t, true)
	if !tt.HasText("command-00 --run") || tt.HasText("pg_dump prod") {
		t.Fatalf("this Host's History is wrong, texts %q", tt.Texts())
	}
	if !tt.HasText(m.SidePanelHistoryCount(30)) || !tt.HasText(m.SidePanelJustNow()) {
		t.Fatalf("the count or the time is missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.SidePanelHistoryAllHosts()); err != nil {
		t.Fatalf("click All hosts: %v", err)
	}
	tt.Frame()
	if !tt.HasText("pg_dump prod") || !tt.HasText(m.SidePanelHistoryCount(31)) {
		t.Fatalf("every Host's History lacks the other Host, texts %q", tt.Texts())
	}
	if !tt.HasText("db-01 · " + m.SidePanelHoursAgo(1)) {
		t.Fatalf("the other Host's row does not name it, texts %q", tt.Texts())
	}
}

func TestHistoryPanelPastesAndRunsTheNewestCommand(t *testing.T) {
	m := locales.In("en")
	tt, sent := newHistoryPanelTester(t, true)
	if err := tt.Click(m.SidePanelHistoryPaste()); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	tt.Frame()
	if err := tt.Click(m.SidePanelHistoryRun()); err != nil {
		t.Fatalf("click Run: %v", err)
	}
	tt.Frame()
	want := []sentCommand{{"command-00 --run", false}, {"command-00 --run", true}}
	if len(*sent) != 2 || (*sent)[0] != want[0] || (*sent)[1] != want[1] {
		t.Fatalf("sent %+v, want %+v", *sent, want)
	}
	if tt.HasText(m.SidePanelHistorySendFailed()) {
		t.Fatal("a delivered command reported a failure")
	}
}

func TestHistoryPanelSaysWhenNoTerminalTookTheCommand(t *testing.T) {
	m := locales.In("en")
	tt, _ := newHistoryPanelTester(t, false)
	if err := tt.Click(m.SidePanelHistoryRun()); err != nil {
		t.Fatalf("click Run: %v", err)
	}
	tt.Frame()
	if !tt.HasText(m.SidePanelHistorySendFailed()) {
		t.Fatalf("an undelivered command was silent, texts %q", tt.Texts())
	}
}
