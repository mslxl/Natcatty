package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// noteTestVault is an in-memory Vault service for the Notes pages. Its Note
// half applies the shared domain normalization and grouping rules, so a test
// exercises what the application service does; only persistence is replaced.
// The real SQLite pairing is covered by the app package.
type noteTestVault struct {
	fakeVault

	mu     sync.Mutex
	notes  []domainvault.Note
	groups []domainvault.NoteGroup
	seq    int

	// noteErr is returned by every Note write, which drives the save-failure
	// path. groupErr and deleteErr do the same for groups and deletes.
	noteErr   error
	groupErr  error
	deleteErr error
}

func newNoteTestVault() *noteTestVault {
	return &noteTestVault{
		notes: []domainvault.Note{
			{ID: "n1", Title: "Runbook", Group: "ops", Content: "# Runbook\nrestart the service"},
			{ID: "n2", Title: "Scratch", Content: "loose thoughts"},
		},
		groups: []domainvault.NoteGroup{{Path: "ops"}},
	}
}

func (v *noteTestVault) ListNotes(context.Context) ([]domainvault.Note, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]domainvault.Note(nil), v.notes...), nil
}

func (v *noteTestVault) CreateNote(_ context.Context, draft domainvault.NoteDraft) (domainvault.Note, error) {
	normalized, err := domainvault.NormalizeNoteDraft(draft)
	if err != nil {
		return domainvault.Note{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.noteErr != nil {
		return domainvault.Note{}, v.noteErr
	}
	v.seq++
	note := domainvault.Note{
		ID:       fmt.Sprintf("phone-note-%d", v.seq),
		Title:    normalized.Title,
		Content:  normalized.Content,
		Group:    normalized.Group,
		OrderKey: int64(v.seq),
	}
	v.notes = append(v.notes, note)
	return note, nil
}

func (v *noteTestVault) UpdateNote(_ context.Context, id string, draft domainvault.NoteDraft) (domainvault.Note, error) {
	normalized, err := domainvault.NormalizeNoteDraft(draft)
	if err != nil {
		return domainvault.Note{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.noteErr != nil {
		return domainvault.Note{}, v.noteErr
	}
	for index := range v.notes {
		if v.notes[index].ID != id {
			continue
		}
		note := &v.notes[index]
		note.Title = normalized.Title
		note.Content = normalized.Content
		note.Group = normalized.Group
		return *note, nil
	}
	return domainvault.Note{}, appvault.ErrNotFound
}

func (v *noteTestVault) DeleteNote(_ context.Context, id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.deleteErr != nil {
		return v.deleteErr
	}
	for index := range v.notes {
		if v.notes[index].ID == id {
			v.notes = append(v.notes[:index], v.notes[index+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (v *noteTestVault) ListNoteGroups(context.Context) ([]domainvault.NoteGroup, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	byPath := make(map[string]domainvault.NoteGroup, len(v.groups))
	saved := make([]string, 0, len(v.groups))
	for _, group := range v.groups {
		byPath[group.Path] = group
		saved = append(saved, group.Path)
	}
	paths := domainvault.CollectNoteGroupPaths(saved, v.notes)
	out := make([]domainvault.NoteGroup, 0, len(paths))
	for _, path := range paths {
		group, ok := byPath[path]
		if !ok {
			group = domainvault.NoteGroup{Path: path}
		}
		out = append(out, group)
	}
	return out, nil
}

func (v *noteTestVault) CreateNoteGroup(_ context.Context, draft domainvault.NoteGroupDraft) (domainvault.NoteGroup, error) {
	normalized, err := domainvault.NormalizeNoteGroupDraft(draft)
	if err != nil {
		return domainvault.NoteGroup{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.groupErr != nil {
		return domainvault.NoteGroup{}, v.groupErr
	}
	for _, group := range v.groups {
		if group.Path == normalized.Path {
			return domainvault.NoteGroup{}, &appvault.Error{Kind: appvault.KindConflict}
		}
	}
	group := domainvault.NoteGroup{Path: normalized.Path}
	v.groups = append(v.groups, group)
	return group, nil
}

func (v *noteTestVault) RenameNoteGroup(_ context.Context, path, newPath string) (domainvault.NoteGroup, error) {
	oldPath := domainvault.NormalizeGroup(path)
	normalized, err := domainvault.NormalizeNoteGroupDraft(domainvault.NoteGroupDraft{Path: newPath})
	if err != nil {
		return domainvault.NoteGroup{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.groupErr != nil {
		return domainvault.NoteGroup{}, v.groupErr
	}
	if domainvault.NoteGroupAtOrBelow(normalized.Path, oldPath) {
		return domainvault.NoteGroup{}, &appvault.Error{
			Kind: appvault.KindValidation,
			Cause: &domainvault.ValidationError{
				Field: domainvault.FieldNoteGroup, Rule: domainvault.RuleInvalid,
			},
		}
	}
	found := false
	for index := range v.groups {
		if v.groups[index].Path == oldPath {
			v.groups[index].Path = normalized.Path
			found = true
			continue
		}
		if domainvault.NoteGroupAtOrBelow(v.groups[index].Path, oldPath) {
			v.groups[index].Path = domainvault.ReplaceNoteGroupPrefix(
				v.groups[index].Path, oldPath, normalized.Path)
			found = true
		}
	}
	for index := range v.notes {
		if domainvault.NoteGroupAtOrBelow(v.notes[index].Group, oldPath) &&
			v.notes[index].Group != "" {
			v.notes[index].Group = domainvault.ReplaceNoteGroupPrefix(
				v.notes[index].Group, oldPath, normalized.Path)
		}
	}
	if !found {
		return domainvault.NoteGroup{}, appvault.ErrNotFound
	}
	return domainvault.NoteGroup{Path: normalized.Path}, nil
}

func (v *noteTestVault) DeleteNoteGroup(_ context.Context, path string) error {
	root := domainvault.NormalizeGroup(path)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.deleteErr != nil {
		return v.deleteErr
	}
	matched := false
	kept := v.groups[:0]
	for _, group := range v.groups {
		if domainvault.NoteGroupAtOrBelow(group.Path, root) {
			matched = true
			continue
		}
		kept = append(kept, group)
	}
	v.groups = kept
	for index := range v.notes {
		if v.notes[index].Group != "" &&
			domainvault.NoteGroupAtOrBelow(v.notes[index].Group, root) {
			matched = true
			v.notes[index].Group = ""
		}
	}
	if !matched {
		return appvault.ErrNotFound
	}
	return nil
}

func (v *noteTestVault) ExportNote(_ context.Context, id string, format appvault.NoteExportFormat) (appvault.NoteExport, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, note := range v.notes {
		if note.ID != id {
			continue
		}
		export := appvault.NoteExport{Format: format, Filename: note.Title + ".md"}
		switch format {
		case appvault.NoteExportHTML:
			export.Filename = note.Title + ".html"
			export.Content = []byte("<html><body>" + note.Content + "</body></html>")
		default:
			export.Content = []byte(note.Content)
		}
		return export, nil
	}
	return appvault.NoteExport{}, appvault.ErrNotFound
}

func (v *noteTestVault) ExportNotes(context.Context, appvault.NoteExportFormat) (appvault.NoteExport, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return appvault.NoteExport{
		Format:   appvault.NoteExportZIP,
		Filename: "netcatty-notes-all.zip",
		Content:  []byte("zip"),
	}, nil
}

func (v *noteTestVault) noteByTitle(title string) (domainvault.Note, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, note := range v.notes {
		if note.Title == title {
			return note, true
		}
	}
	return domainvault.Note{}, false
}

func (v *noteTestVault) groupExists(path string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, group := range v.groups {
		if group.Path == path {
			return true
		}
	}
	return false
}

// recordingNoteWriter stands in for the system save picker. cancel models the
// user dismissing it: Save returns no path and no error.
type recordingNoteWriter struct {
	mu      sync.Mutex
	cancel  bool
	calls   int
	name    string
	content []byte
	err     error
}

func (w *recordingNoteWriter) Save(name string, content []byte) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.name = name
	w.content = append([]byte(nil), content...)
	if w.err != nil {
		return "", w.err
	}
	if w.cancel {
		return "", nil
	}
	return "/saved/" + name, nil
}

func (w *recordingNoteWriter) snapshot() (int, string, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls, w.name, string(w.content)
}

// newNotesTester opens the Vault section's Notes page over a fake Vault.
func newNotesTester(t *testing.T, vault VaultStore, opts ...Option) (*Shell, *ui.Tester) {
	t.Helper()
	options := []Option{WithVault(vault)}
	options = append(options, opts...)
	shell := New(options...)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Vault)
	tt := ui.NewTester(shell.View(Vault), 390, 844)
	tt.Frame()
	if err := tt.Click("Notes"); err != nil {
		t.Fatalf("open Notes: %v; texts: %q", err, tt.Texts())
	}
	tt.Advance(600 * time.Millisecond)
	return shell, tt
}

// TestNotesListGroupsSearchAndCRUD covers the ticket's first box: Notes are
// grouped by Note Group, and create, edit, delete, search, and Note Group
// management all reach the shared store.
func TestNotesListGroupsSearchAndCRUD(t *testing.T) {
	vault := newNoteTestVault()
	shell, tt := newNotesTester(t, vault)

	for _, want := range []string{"ops", "Runbook", "Vault root", "Scratch"} {
		if !tt.HasText(want) {
			t.Fatalf("the grouped list lacks %q; texts: %q", want, tt.Texts())
		}
	}

	// A search flattens the list into matches across title, body, and group.
	if err := tt.Click("Search"); err != nil {
		t.Fatalf("focus the search: %v", err)
	}
	tt.Type("runbook")
	tt.Frame()
	if !tt.HasText("1 matching Notes") {
		t.Fatalf("the search did not narrow to one Note; texts: %q", tt.Texts())
	}
	if tt.HasText("Scratch") {
		t.Fatalf("the search still lists an unmatched Note; texts: %q", tt.Texts())
	}
	// Clear the search so the grouped list is back for the CRUD steps.
	shell.vault.notes.vault.query = ""
	tt.Frame()

	// Create a Note in the group; the store receives it.
	if err := tt.Click("Add note"); err != nil {
		t.Fatalf("click Add note: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Title"); err != nil {
		t.Fatalf("focus Title: %v", err)
	}
	tt.Type("Phone note")
	if err := tt.Click("Group"); err != nil {
		t.Fatalf("focus Group: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Content"); err != nil {
		t.Fatalf("focus Content: %v", err)
	}
	tt.Type("hello from the phone")
	if err := tt.Click("Save note"); err != nil {
		t.Fatalf("save the Note: %v", err)
	}
	tt.Frame()
	created, ok := vault.noteByTitle("Phone note")
	if !ok {
		t.Fatalf("the shared store lacks the new Note; texts: %q", tt.Texts())
	}
	if created.Group != "ops" || !strings.Contains(created.Content, "hello from the phone") {
		t.Fatalf("stored Note = %+v, want the phone's draft", created)
	}

	// Edit it; identity is preserved and the new text reaches the store.
	if err := tt.Click("Edit note Phone note"); err != nil {
		t.Fatalf("click Edit note: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Title"); err != nil {
		t.Fatalf("focus Title in edit: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Save note"); err != nil {
		t.Fatalf("save the edited Note: %v", err)
	}
	tt.Frame()
	edited, ok := vault.noteByTitle("Phone note v2")
	if !ok || edited.ID != created.ID {
		t.Fatalf("the edit did not replace the same Note: %+v", edited)
	}

	// Delete it through the confirmation.
	if err := tt.Click("Delete note Phone note v2"); err != nil {
		t.Fatalf("click Delete note: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm the delete: %v", err)
	}
	tt.Frame()
	if _, ok := vault.noteByTitle("Phone note v2"); ok {
		t.Fatal("the deleted Note is still in the shared store")
	}

	// Note Group create, rename, and delete.
	if err := tt.Click("Add group"); err != nil {
		t.Fatalf("click Add group: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Group path"); err != nil {
		t.Fatalf("focus Group path: %v", err)
	}
	tt.Type("team")
	if err := tt.Click("Save group"); err != nil {
		t.Fatalf("save the Note Group: %v", err)
	}
	tt.Frame()
	if !vault.groupExists("team") {
		t.Fatalf("the shared store lacks the new Note Group; texts: %q", tt.Texts())
	}
	if err := tt.Click("Edit group team"); err != nil {
		t.Fatalf("click Edit group: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Group path"); err != nil {
		t.Fatalf("focus Group path in rename: %v", err)
	}
	tt.Type("2")
	if err := tt.Click("Save group"); err != nil {
		t.Fatalf("rename the Note Group: %v", err)
	}
	tt.Frame()
	if vault.groupExists("team") || !vault.groupExists("team2") {
		t.Fatalf("the rename did not reach the shared store; groups: %+v", vault.groups)
	}
	if err := tt.Click("Delete group team2"); err != nil {
		t.Fatalf("click Delete group: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm the group delete: %v", err)
	}
	tt.Frame()
	if vault.groupExists("team2") {
		t.Fatal("the deleted Note Group is still in the shared store")
	}
}

// TestNotesExportCancelKeepsContent is the export box: the writer receives the
// rendered Note, cancelling the system picker changes nothing, and a completed
// save is reported.
func TestNotesExportCancelKeepsContent(t *testing.T) {
	vault := newNoteTestVault()
	writer := &recordingNoteWriter{cancel: true}
	_, tt := newNotesTester(t, vault, WithNoteWriter(writer))

	if err := tt.Click("Open note Runbook"); err != nil {
		t.Fatalf("open the Note: %v", err)
	}
	tt.Frame()
	original, _ := vault.noteByTitle("Runbook")

	if err := tt.Click("Export Markdown"); err != nil {
		t.Fatalf("export the Note: %v", err)
	}
	tt.Frame()
	calls, name, content := writer.snapshot()
	if calls != 1 || name != "Runbook.md" {
		t.Fatalf("export handed %q after %d calls, want Runbook.md once", name, calls)
	}
	if content != original.Content {
		t.Fatalf("export content = %q, want the Note's Markdown %q", content, original.Content)
	}
	after, _ := vault.noteByTitle("Runbook")
	if after.Content != original.Content || after.Title != original.Title || after.Group != original.Group {
		t.Fatalf("cancelling the export changed the Note: %+v", after)
	}
	if tt.HasText("Saved ") {
		t.Fatalf("a cancelled export claimed success; texts: %q", tt.Texts())
	}

	// The same action with the picker accepted reports the saved file.
	writer.mu.Lock()
	writer.cancel = false
	writer.mu.Unlock()
	if err := tt.Click("Export Markdown"); err != nil {
		t.Fatalf("export again: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Saved Runbook.md.") {
		t.Fatalf("a completed export was not reported; texts: %q", tt.Texts())
	}
	after, _ = vault.noteByTitle("Runbook")
	if after.Content != original.Content {
		t.Fatal("exporting rewrote the Note's content")
	}
}

// TestNotesSaveFailureKeepsTheDraftAndRetries covers the failure box: a write
// failure keeps every typed character and offers a retry that succeeds.
func TestNotesSaveFailureKeepsTheDraftAndRetries(t *testing.T) {
	vault := newNoteTestVault()
	vault.noteErr = errors.New("disk is full")
	shell, tt := newNotesTester(t, vault)

	if err := tt.Click("Add note"); err != nil {
		t.Fatalf("click Add note: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Title"); err != nil {
		t.Fatalf("focus Title: %v", err)
	}
	tt.Type("Draft")
	if err := tt.Click("Content"); err != nil {
		t.Fatalf("focus Content: %v", err)
	}
	tt.Type("keep me")
	if err := tt.Click("Save note"); err != nil {
		t.Fatalf("save the Note: %v", err)
	}
	tt.Frame()

	view := &shell.vault.notes.vault
	if view.draft == nil {
		t.Fatalf("a failed save closed the editor; texts: %q", tt.Texts())
	}
	if view.draft.title != "Draft" || view.draft.content != "keep me" {
		t.Fatalf("a failed save lost the draft: %+v", view.draft)
	}
	if !tt.HasText("Could not save the Note. Your changes are kept; try again.") {
		t.Fatalf("the failure is not explained; texts: %q", tt.Texts())
	}
	if !tt.HasText("Retry save") {
		t.Fatalf("the failure offers no retry; texts: %q", tt.Texts())
	}
	if _, ok := vault.noteByTitle("Draft"); ok {
		t.Fatal("a failed save wrote the Note anyway")
	}

	vault.mu.Lock()
	vault.noteErr = nil
	vault.mu.Unlock()
	if err := tt.Click("Retry save"); err != nil {
		t.Fatalf("retry the save: %v", err)
	}
	tt.Frame()
	if view.draft != nil {
		t.Fatalf("the successful retry left the editor open: %+v", view.draft)
	}
	if _, ok := vault.noteByTitle("Draft"); !ok {
		t.Fatalf("the successful retry did not write the Note; texts: %q", tt.Texts())
	}
}

// TestToolsNotesReturnsToTheSamePaneAndPosition is the Session Tools box:
// opening Notes from the overlay does not change the route, the terminal stays
// attached, and Done returns to the same Pane with its scroll intact.
func TestToolsNotesReturnsToTheSamePaneAndPosition(t *testing.T) {
	vault := newNoteTestVault()
	opener := &fakeOpener{output: "remote prompt $ "}
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener,
		WithVault(vault),
		WithCommandHistory(newFakeCommandHistory(toolHistoryEntries()...)))
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})

	pane := fixture.focusedPane()
	screen := fixture.focusedScreen(t)
	screen.scrolled = true
	paneID := pane.id
	path := fixture.shell.Router(Sessions).Path()

	fixture.openToolsPanel(t, "prod box")
	if err := fixture.tester.Click("Open Notes"); err != nil {
		t.Fatalf("open the Notes tool: %v; texts: %q", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("Runbook") || !fixture.tester.HasText("Vault root") {
		t.Fatalf("the Notes tool did not list the shared Notes; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.shell.Router(Sessions).Path(); got != path {
		t.Fatalf("opening Notes changed the route to %q, want %q", got, path)
	}
	if !handle.isAttached() {
		t.Fatal("opening Notes hibernated the Session")
	}

	if err := fixture.tester.Click("Open note Runbook"); err != nil {
		t.Fatalf("open a Note inside the tool: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText("restart the service") {
		t.Fatalf("the Note viewer did not render the content; texts: %q", fixture.tester.Texts())
	}
	if got := fixture.shell.Router(Sessions).Path(); got != path {
		t.Fatalf("opening a Note changed the route to %q, want %q", got, path)
	}

	// Back to the Pane: Done closes the whole surface without a route change.
	if err := fixture.tester.Click("Done"); err != nil {
		t.Fatalf("close the Tools surface: %v", err)
	}
	fixture.tester.Frame()
	if got := fixture.shell.Router(Sessions).Path(); got != path {
		t.Fatalf("closing Tools changed the route to %q, want %q", got, path)
	}
	if !fixture.tester.HasText("remote prompt $") {
		t.Fatalf("the terminal did not return; texts: %q", fixture.tester.Texts())
	}
	if pane := fixture.focusedPane(); pane == nil || pane.id != paneID {
		t.Fatalf("closing Tools changed the focused Pane: %v", pane)
	}
	if !fixture.focusedScreen(t).scrolled {
		t.Fatal("closing Tools lost the terminal's scrollback position")
	}
	if !handle.isAttached() || fixture.liveScreens() != 1 {
		t.Fatal("closing Tools reattached or dropped the Session view")
	}
}
