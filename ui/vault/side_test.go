package vault

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/vault/section"
)

func TestSideScriptsRunASnippetInTheFocusedSession(t *testing.T) {
	m := locales.In("en")
	state, _, runner, _ := newSnippetView(t, nil,
		domainvault.Snippet{ID: "s1", Label: "Disk usage", Command: "df -h", Package: "ops"},
		domainvault.Snippet{ID: "s2", Label: "Restart", Command: "systemctl restart {{service:nginx}}"},
	)
	runner.hasHost = true
	var opened []section.Section
	panels := NewSidePanels(state, func(page section.Section) { opened = append(opened, page) })
	tt := ui.NewTester(panels.Scripts(), 360, 600)
	tt.SetLocale("en")
	tt.Frame()
	for _, want := range []string{"ops", "Disk usage", "df -h", "Restart"} {
		if !tt.HasText(want) {
			t.Fatalf("Scripts lacks %q, texts %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Disk usage"); err != nil {
		t.Fatalf("click a Snippet: %v", err)
	}
	tt.Frame()
	if len(runner.activeSent) != 1 || runner.activeSent[0] != "df -h" {
		t.Fatalf("sent %q, want df -h", runner.activeSent)
	}

	// A Snippet with variables asks for them in the panel first.
	if err := tt.Click("Restart"); err != nil {
		t.Fatalf("click a Snippet with variables: %v", err)
	}
	tt.Frame()
	if len(runner.activeSent) != 1 || state.SnippetRunForm() == nil {
		t.Fatalf("the variables were not asked for: sent %q", runner.activeSent)
	}
	if tt.HasText("Disk usage") {
		t.Fatalf("the run form left the list on screen, texts %q", tt.Texts())
	}

	state.CancelSnippetRun()
	tt.Frame()
	if err := tt.Click(m.VaultSnippetNew()); err != nil {
		t.Fatalf("click New Snippet: %v", err)
	}
	if len(opened) != 1 || opened[0] != section.Snippets {
		t.Fatalf("New Snippet opened %v", opened)
	}
}

func TestSideNotesReadANoteInPlace(t *testing.T) {
	state, _ := newNoteProductionState(t)
	state.BeginCreateNote()
	form := state.NoteForm()
	form.Title = "Runbook"
	form.Content = "# Restart\n\nRun **deploy**."
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	note := domainvault.Note{Title: "Runbook"}
	panels := NewSidePanels(state, nil)
	tt := ui.NewTester(panels.Notes(), 360, 600)
	tt.SetLocale("en")
	tt.Frame()
	if !tt.HasText(note.Title) || !tt.HasText("Restart") {
		t.Fatalf("Notes lacks the Note or its preview, texts %q", tt.Texts())
	}
	if err := tt.Click(note.Title); err != nil {
		t.Fatalf("click the Note: %v", err)
	}
	tt.Frame()
	if !tt.HasText("deploy") {
		t.Fatalf("the Note did not open in the panel, texts %q", tt.Texts())
	}
	if err := tt.Click(locales.In("en").VaultNoteBack()); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	tt.Frame()
	if tt.HasText("deploy") {
		t.Fatalf("Back did not return to the list, texts %q", tt.Texts())
	}
}
