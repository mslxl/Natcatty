package vault

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// noteTestRepo is an in-package Note repository fake. UI tests must not import
// an adapter, so the real service runs over this instead of SQLite.
type noteTestRepo struct {
	notes  []domainvault.Note
	groups []domainvault.NoteGroup
	next   int
}

func (f *noteTestRepo) ListNotes(context.Context) ([]domainvault.Note, error) {
	return append([]domainvault.Note(nil), f.notes...), nil
}

func (f *noteTestRepo) GetNote(_ context.Context, id string) (domainvault.Note, error) {
	for _, note := range f.notes {
		if note.ID == id {
			return note, nil
		}
	}
	return domainvault.Note{}, appvault.ErrNotFound
}

func (f *noteTestRepo) SaveNote(_ context.Context, note domainvault.Note) error {
	for i := range f.notes {
		if f.notes[i].ID == note.ID {
			f.notes[i] = note
			return nil
		}
	}
	f.notes = append(f.notes, note)
	return nil
}

func (f *noteTestRepo) DeleteNote(_ context.Context, id string) error {
	for i := range f.notes {
		if f.notes[i].ID == id {
			f.notes = append(f.notes[:i], f.notes[i+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (f *noteTestRepo) ListNoteGroups(context.Context) ([]domainvault.NoteGroup, error) {
	return append([]domainvault.NoteGroup(nil), f.groups...), nil
}

func (f *noteTestRepo) SaveNoteGroup(_ context.Context, group domainvault.NoteGroup) error {
	for i := range f.groups {
		if f.groups[i].Path == group.Path {
			f.groups[i] = group
			return nil
		}
	}
	f.groups = append(f.groups, group)
	return nil
}

func (f *noteTestRepo) DeleteNoteGroup(_ context.Context, path string) error {
	for i := range f.groups {
		if f.groups[i].Path == path {
			f.groups = append(f.groups[:i], f.groups[i+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

type noteTestWriter struct {
	name    string
	content []byte
	calls   int
}

func (w *noteTestWriter) Save(name string, content []byte) (string, error) {
	w.calls++
	w.name = name
	w.content = append([]byte(nil), content...)
	return "/exports/" + name, nil
}

func newNoteProductionState(t *testing.T) (*vaultstate.State, *noteTestWriter) {
	t.Helper()
	repo := &noteTestRepo{}
	sequence := 0
	service := appvault.NewService(nil,
		appvault.WithNotes(repo),
		appvault.WithNoteIDGenerator(func() string {
			sequence++
			return "note-" + strconv.Itoa(sequence)
		}),
	)
	writer := &noteTestWriter{}
	state := vaultstate.NewState(&entityHosts{},
		vaultstate.WithNotes(service),
		vaultstate.WithNoteFileWriter(writer),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, writer
}

// noteContent exercises every block kind the native viewer renders.
const noteContent = "# Restart PostgreSQL\n\n" +
	"Run **pg_isready** before `systemctl restart postgres`.\n\n" +
	"- Check the log\n- Notify the team\n\n" +
	"> Prefer a maintenance window.\n\n" +
	"```sh\npg_isready -h db.internal\n```\n\n" +
	"See [the runbook](https://example.com/runbook).\n\n" +
	"The end."

func createNoteThroughTheView(t *testing.T, state *vaultstate.State, tt *ui.Tester, width, height int) {
	t.Helper()
	m := locales.In("en")
	if err := tt.Click(m.VaultNoteGroupNew()); err != nil {
		t.Fatalf("click New group: %v", err)
	}
	state.NoteGroupForm().Path = "ops/runbooks"
	if err := state.SaveNoteGroup(); err != nil {
		t.Fatalf("SaveNoteGroup: %v", err)
	}
	scrollToEnd(tt, width, height)
	if err := tt.Click(m.VaultNoteNew()); err != nil {
		t.Fatalf("click New Note: %v", err)
	}
	tt.Frame()
	if err := tt.Click(m.VaultHostSave()); err != nil {
		t.Fatalf("save an empty Note: %v", err)
	}
	tt.Frame()
	if !textsContain(tt, m.VaultNoteProblem()) {
		t.Fatalf("the missing-title problem is not localized: %v", tt.Texts())
	}
	form := state.NoteForm()
	form.Title = "Restart database"
	form.Group = "ops/runbooks"
	form.Content = noteContent
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	scrollToEnd(tt, width, height)
}

func TestVaultNotesRenderThroughTheProductionChrome(t *testing.T) {
	m := locales.In("en")
	state, writer := newNoteProductionState(t)

	// Do the workflow at the smallest supported window: the Note actions must
	// be reachable without horizontal clipping.
	const smallW, smallH = 720, 480
	tt := newProductionTester(t, state, smallW, smallH)
	scrollToEnd(tt, smallW, smallH)
	createNoteThroughTheView(t, state, tt, smallW, smallH)

	if state.NoteCount() != 1 || state.NoteGroupCount() != 2 {
		t.Fatalf("notes=%d groups=%d", state.NoteCount(), state.NoteGroupCount())
	}
	scrollToEnd(tt, smallW, smallH)
	for _, want := range []string{
		m.VaultNoteListTitle(1), m.VaultNoteGroupNoteCount(1),
		"Restart database", m.VaultNoteOpen(),
	} {
		assertVisible(t, tt, want, smallW, smallH)
	}

	// Open the native viewer through its real button.
	if err := tt.Click(m.VaultNoteOpen()); err != nil {
		t.Fatalf("click Open: %v", err)
	}
	tt.Frame()
	if !state.ViewingNote() {
		t.Fatalf("the viewer did not open")
	}
	for _, want := range []string{
		"Restart database", "pg_isready", "systemctl restart postgres",
		"Check the log", "Notify the team", "Prefer a maintenance window.",
		"pg_isready -h db.internal", "the runbook", "https://example.com/runbook",
		m.VaultNoteExportMarkdown(),
	} {
		if !textsContain(tt, want) {
			t.Fatalf("%q is not rendered, texts %q", want, tt.Texts())
		}
	}
	if textsContain(tt, "**pg_isready**") {
		t.Fatalf("the viewer shows raw Markdown syntax")
	}

	// Export HTML through the viewer button and prove the bytes are the
	// escaped, self-contained document.
	if err := tt.Click(m.VaultNoteExportHTML()); err != nil {
		t.Fatalf("click Export HTML: %v", err)
	}
	tt.Frame()
	if writer.calls != 1 || !strings.Contains(string(writer.content), "<!DOCTYPE html>") {
		t.Fatalf("HTML export calls=%d content=%q", writer.calls, writer.content)
	}
	if !tt.HasText(m.VaultNoteExportSaved("/exports/Restart database.html")) {
		t.Fatalf("the export result is not shown: %v", tt.Texts())
	}

	// Both sizes: the list and the viewer stay inside the window, and the
	// viewer scrolls to its last paragraph.
	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		state.CloseNoteView()
		page := newProductionTester(t, state, size.width, size.height)
		scrollToEnd(page, size.width, size.height)
		for _, want := range []string{m.VaultNoteListTitle(1), "Restart database"} {
			assertVisible(t, page, want, size.width, size.height)
		}
		if err := page.Click(m.VaultNoteOpen()); err != nil {
			t.Fatalf("%dx%d: click Open: %v", size.width, size.height, err)
		}
		page.Frame()
		for _, text := range page.Texts() {
			rect, ok := page.Find(text)
			if !ok || rect.W <= 0 || rect.H <= 0 {
				continue
			}
			if rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 {
				t.Fatalf("%dx%d: %q overflows horizontally: %+v",
					size.width, size.height, text, rect)
			}
			if rect.H < 8 {
				t.Fatalf("%dx%d: %q is unreadably small: %+v",
					size.width, size.height, text, rect)
			}
		}
		scrollToEnd(page, size.width, size.height)
		assertVisible(t, page, "The end.", size.width, size.height)
		for _, want := range []string{
			m.VaultNoteExportMarkdown(), m.VaultNoteExportHTML(),
			m.VaultNoteExportZIP(), m.VaultHostEdit(), m.VaultNoteBack(),
		} {
			assertVisible(t, page, want, size.width, size.height)
		}
	}
}

// TestVaultNoteSearchAndGroupDeleteInTheView covers the list's search field and
// the non-cascading group delete through the production chrome.
func TestVaultNoteSearchAndGroupDeleteInTheView(t *testing.T) {
	m := locales.In("en")
	state, _ := newNoteProductionState(t)
	state.BeginCreateNoteGroup()
	state.NoteGroupForm().Path = "ops"
	if err := state.SaveNoteGroup(); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	tt := newProductionTester(t, state, 720, 480)
	scrollToEnd(tt, 720, 480)

	// Seed a Note directly so the test starts from a known list.
	state.BeginCreateNote()
	state.NoteForm().Title = "Unique title"
	state.NoteForm().Group = "ops"
	state.NoteForm().Content = "needle in the source"
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	tt.Frame()

	state.SetNoteSearch("NEEDLE")
	tt.Frame()
	scrollToEnd(tt, 720, 480)
	assertVisible(t, tt, "Unique title", 720, 480)
	state.SetNoteSearch("absent")
	tt.Frame()
	if !tt.HasText(m.VaultNoteSearchEmpty("absent")) {
		t.Fatalf("the no-match message is missing: %v", tt.Texts())
	}

	state.SetNoteSearch("")
	state.RequestDeleteNoteGroup("ops")
	tt.Frame()
	if !textsContain(tt, "Delete group") {
		t.Fatalf("the group delete confirmation is missing: %v", tt.Texts())
	}
	scrollToEnd(tt, 720, 480)
	if err := tt.Click(m.VaultHostDelete()); err != nil {
		t.Fatalf("confirm group delete: %v", err)
	}
	tt.Frame()
	if state.NoteCount() != 1 || state.Notes()[0].Group != "" {
		t.Fatalf("the Note was not kept at the root: %+v", state.Notes())
	}
	if state.NoteGroupCount() != 0 {
		t.Fatalf("groups after delete = %+v", state.NoteGroups())
	}
	state.RequestDeleteNote(state.Notes()[0].ID)
	if err := state.ConfirmDeleteNote(); err != nil {
		t.Fatalf("delete Note: %v", err)
	}
	tt.Frame()
	if state.NoteCount() != 0 {
		t.Fatalf("the Note was not deleted")
	}
}
