package state

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/markdown"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeNoteStore is a small in-memory Note repository for state tests. Group
// semantics match the application service: a rename remaps prefixes, and a
// delete keeps Notes by moving them to the root.
type fakeNoteStore struct {
	notes  []domainvault.Note
	groups []domainvault.NoteGroup
	next   int
}

func (f *fakeNoteStore) ListNotes(context.Context) ([]domainvault.Note, error) {
	return append([]domainvault.Note(nil), f.notes...), nil
}

func (f *fakeNoteStore) CreateNote(_ context.Context, draft domainvault.NoteDraft) (domainvault.Note, error) {
	normalized, err := domainvault.NormalizeNoteDraft(draft)
	if err != nil {
		return domainvault.Note{}, fakeInvalid(err)
	}
	f.next++
	now := time.Unix(1700000000+int64(f.next), 0).UTC()
	note := domainvault.Note{
		ID:        "note-" + strconv.Itoa(f.next),
		Title:     normalized.Title,
		Content:   normalized.Content,
		Group:     normalized.Group,
		OrderKey:  int64(f.next) * 1000,
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.notes = append(f.notes, note)
	return note, nil
}

func (f *fakeNoteStore) UpdateNote(_ context.Context, id string, draft domainvault.NoteDraft) (domainvault.Note, error) {
	normalized, err := domainvault.NormalizeNoteDraft(draft)
	if err != nil {
		return domainvault.Note{}, fakeInvalid(err)
	}
	for i := range f.notes {
		if f.notes[i].ID != id {
			continue
		}
		f.notes[i].Title = normalized.Title
		f.notes[i].Content = normalized.Content
		f.notes[i].Group = normalized.Group
		return f.notes[i], nil
	}
	return domainvault.Note{}, appvault.ErrNotFound
}

func (f *fakeNoteStore) DeleteNote(_ context.Context, id string) error {
	for i := range f.notes {
		if f.notes[i].ID == id {
			f.notes = append(f.notes[:i], f.notes[i+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (f *fakeNoteStore) ListNoteGroups(context.Context) ([]domainvault.NoteGroup, error) {
	saved := make([]string, 0, len(f.groups))
	for _, group := range f.groups {
		saved = append(saved, group.Path)
	}
	paths := domainvault.CollectNoteGroupPaths(saved, f.notes)
	out := make([]domainvault.NoteGroup, 0, len(paths))
	for _, path := range paths {
		out = append(out, domainvault.NoteGroup{Path: path})
	}
	return out, nil
}

func (f *fakeNoteStore) CreateNoteGroup(_ context.Context, draft domainvault.NoteGroupDraft) (domainvault.NoteGroup, error) {
	normalized, err := domainvault.NormalizeNoteGroupDraft(draft)
	if err != nil {
		return domainvault.NoteGroup{}, fakeInvalid(err)
	}
	for _, group := range f.groups {
		if group.Path == normalized.Path {
			return domainvault.NoteGroup{}, &appvault.Error{Kind: appvault.KindConflict}
		}
	}
	group := domainvault.NoteGroup{Path: normalized.Path, OrderKey: int64(len(f.groups)+1) * 1000}
	f.groups = append(f.groups, group)
	return group, nil
}

func (f *fakeNoteStore) RenameNoteGroup(_ context.Context, path, newPath string) (domainvault.NoteGroup, error) {
	oldPath := domainvault.NormalizeGroup(path)
	normalized, err := domainvault.NormalizeNoteGroupDraft(domainvault.NoteGroupDraft{Path: newPath})
	if err != nil {
		return domainvault.NoteGroup{}, fakeInvalid(err)
	}
	for i := range f.groups {
		if domainvault.NoteGroupAtOrBelow(f.groups[i].Path, oldPath) {
			f.groups[i].Path = domainvault.ReplaceNoteGroupPrefix(f.groups[i].Path, oldPath, normalized.Path)
		}
	}
	for i := range f.notes {
		if domainvault.NoteGroupAtOrBelow(f.notes[i].Group, oldPath) {
			f.notes[i].Group = domainvault.ReplaceNoteGroupPrefix(f.notes[i].Group, oldPath, normalized.Path)
		}
	}
	return domainvault.NoteGroup{Path: normalized.Path}, nil
}

func (f *fakeNoteStore) DeleteNoteGroup(_ context.Context, path string) error {
	root := domainvault.NormalizeGroup(path)
	kept := f.groups[:0]
	for _, group := range f.groups {
		if !domainvault.NoteGroupAtOrBelow(group.Path, root) {
			kept = append(kept, group)
		}
	}
	f.groups = kept
	for i := range f.notes {
		if domainvault.NoteGroupAtOrBelow(f.notes[i].Group, root) {
			f.notes[i].Group = ""
		}
	}
	return nil
}

func (f *fakeNoteStore) ExportNote(_ context.Context, id string, format appvault.NoteExportFormat) (appvault.NoteExport, error) {
	for _, note := range f.notes {
		if note.ID != id {
			continue
		}
		name := strings.ReplaceAll(note.Title, " ", "-") + "." + string(format)
		switch format {
		case appvault.NoteExportMarkdown:
			return appvault.NoteExport{Format: format, Filename: name, Content: []byte(note.Content)}, nil
		case appvault.NoteExportHTML:
			return appvault.NoteExport{Format: format, Filename: name, Content: []byte(markdown.HTMLPage(note.Title, note.Content))}, nil
		case appvault.NoteExportZIP:
			content, err := fakeZip(note)
			return appvault.NoteExport{Format: format, Filename: name, Content: content}, err
		}
	}
	return appvault.NoteExport{}, appvault.ErrNotFound
}

func (f *fakeNoteStore) ExportNotes(_ context.Context, format appvault.NoteExportFormat) (appvault.NoteExport, error) {
	if format != appvault.NoteExportZIP {
		return appvault.NoteExport{}, appvault.ErrNotFound
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, note := range f.notes {
		writer, err := archive.Create(note.Title + ".md")
		if err != nil {
			return appvault.NoteExport{}, err
		}
		if _, err := writer.Write([]byte(note.Content)); err != nil {
			return appvault.NoteExport{}, err
		}
	}
	if err := archive.Close(); err != nil {
		return appvault.NoteExport{}, err
	}
	return appvault.NoteExport{Format: format, Filename: "all.zip", Content: buffer.Bytes()}, nil
}

func fakeZip(note domainvault.Note) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	writer, err := archive.Create(note.Title + ".md")
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write([]byte(note.Content)); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func fakeInvalid(err error) error {
	return &appvault.Error{Kind: appvault.KindValidation, Cause: err}
}

type recordingWriter struct {
	name    string
	content []byte
	calls   int
	cancel  bool
	err     error
}

func (w *recordingWriter) Save(name string, content []byte) (string, error) {
	w.calls++
	w.name = name
	w.content = append([]byte(nil), content...)
	if w.err != nil {
		return "", w.err
	}
	if w.cancel {
		return "", nil
	}
	return "/exports/" + name, nil
}

func newNoteState(t *testing.T) (*State, *fakeNoteStore, *recordingWriter) {
	t.Helper()
	store := &fakeNoteStore{}
	writer := &recordingWriter{}
	state := NewState(&fakeHosts{},
		WithNotes(store),
		WithNoteFileWriter(writer),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, store, writer
}

func TestNoteStateCRUDValidationAndSearch(t *testing.T) {
	state, _, _ := newNoteState(t)

	// A missing title must not open or persist anything.
	state.BeginCreateNote()
	if !state.EditingNote() {
		t.Fatalf("the Note editor did not open")
	}
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if state.Problem() != ProblemNote {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemNote)
	}
	if !state.EditingNote() || state.NoteCount() != 0 {
		t.Fatalf("an invalid Note was saved: editing=%v count=%d",
			state.EditingNote(), state.NoteCount())
	}

	// Create through the same editor.
	source := "# Restart PostgreSQL\n\nRun **`pg_isready`** after the restart.\n"
	form := state.NoteForm()
	form.Title = "Restart database"
	form.Group = "ops/db"
	form.Content = source
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if state.Problem() != ProblemNone || state.EditingNote() {
		t.Fatalf("editor did not close: problem=%q editing=%v", state.Problem(), state.EditingNote())
	}
	if state.NoteCount() != 1 || state.NoteGroups()[1].Path != "ops/db" {
		t.Fatalf("notes=%+v groups=%+v", state.Notes(), state.NoteGroups())
	}
	note, ok := state.NoteByID("note-1")
	if !ok || note.Content != source {
		t.Fatalf("stored Note = %+v, %v", note, ok)
	}

	// Update through the editor.
	if !state.BeginEditNote(note.ID) {
		t.Fatalf("BeginEditNote rejected a listed Note")
	}
	state.NoteForm().Title = "Restart PostgreSQL"
	state.NoteForm().Content = source + "\nThen check `systemctl status postgresql`.\n"
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote update: %v", err)
	}
	if updated, _ := state.NoteByID(note.ID); updated.Title != "Restart PostgreSQL" {
		t.Fatalf("updated Note = %+v", updated)
	}

	// Search is case-insensitive and covers the Markdown and the group.
	for _, query := range []string{"POSTGRESQL", "pg_isready", "ops/db", "systemctl"} {
		state.SetNoteSearch(query)
		if matches := state.NotesMatchingSearch(); len(matches) != 1 ||
			matches[0].ID != note.ID {
			t.Fatalf("query %q matched %+v", query, matches)
		}
	}
	state.SetNoteSearch("no-such-token")
	if len(state.NotesMatchingSearch()) != 0 {
		t.Fatalf("an unmatched search returned Notes")
	}
	state.SetNoteSearch("")
	if !state.SearchingNotes() && len(state.NotesMatchingSearch()) != 1 {
		t.Fatalf("clearing the search did not list every Note")
	}
}

func TestNoteStateGroupRenameDeleteAndViewer(t *testing.T) {
	state, _, _ := newNoteState(t)
	state.BeginCreateNoteGroup()
	state.NoteGroupForm().Path = "ops/db"
	if err := state.SaveNoteGroup(); err != nil {
		t.Fatalf("SaveNoteGroup: %v", err)
	}
	if len(state.NoteGroups()) != 2 {
		t.Fatalf("groups = %+v", state.NoteGroups())
	}

	state.BeginCreateNote()
	state.NoteForm().Title = "Backup"
	state.NoteForm().Group = "ops/db"
	state.NoteForm().Content = "# Backup\n\nRun `pg_dump`.\n"
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	note, _ := state.NoteByID("note-1")

	// The viewer opens on a listed Note and closes without touching the data.
	if !state.BeginViewNote(note.ID) || !state.ViewingNote() || state.ViewingNoteID() != note.ID {
		t.Fatalf("viewer did not open")
	}
	state.CloseNoteView()
	if state.ViewingNote() {
		t.Fatalf("viewer did not close")
	}

	// Rename the group; the Note follows it.
	if !state.BeginEditNoteGroup("ops/db") {
		t.Fatalf("BeginEditNoteGroup rejected a visible group")
	}
	state.NoteGroupForm().Path = "runbooks/db"
	if err := state.SaveNoteGroup(); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if moved, _ := state.NoteByID(note.ID); moved.Group != "runbooks/db" {
		t.Fatalf("Note group after rename = %q", moved.Group)
	}

	// Deleting the group keeps the Note and moves it to the root.
	state.RequestDeleteNoteGroup("runbooks")
	if state.PendingDeleteNoteGroup() != "runbooks" {
		t.Fatalf("the group delete confirmation did not open")
	}
	if err := state.ConfirmDeleteNoteGroup(); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if state.NoteCount() != 1 || state.Notes()[0].Group != "" {
		t.Fatalf("notes after group delete = %+v", state.Notes())
	}
	if state.NoteGroupCount() != 0 {
		t.Fatalf("groups after delete = %+v", state.NoteGroups())
	}

	// Deleting the viewed Note closes the viewer.
	if !state.BeginViewNote(note.ID) {
		t.Fatalf("viewer did not reopen")
	}
	state.RequestDeleteNote(note.ID)
	if err := state.ConfirmDeleteNote(); err != nil {
		t.Fatalf("delete Note: %v", err)
	}
	if state.NoteCount() != 0 || state.ViewingNote() {
		t.Fatalf("delete left count=%d viewing=%v", state.NoteCount(), state.ViewingNote())
	}
}

func TestNoteStateExportsThroughTheFileWriter(t *testing.T) {
	state, _, writer := newNoteState(t)
	state.ExportAllNotes()
	if state.NoteExportProblem() != ProblemNoteExportEmpty {
		t.Fatalf("empty export problem = %q", state.NoteExportProblem())
	}

	source := "# Restart\n\nRun `systemctl restart postgres`.\n"
	state.BeginCreateNote()
	state.NoteForm().Title = "Restart database"
	state.NoteForm().Group = "ops/db"
	state.NoteForm().Content = source
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	note, _ := state.NoteByID("note-1")
	if !state.BeginViewNote(note.ID) {
		t.Fatalf("viewer did not open")
	}

	// Markdown is saved byte for byte and the path is reported.
	if !state.ExportNote(appvault.NoteExportMarkdown) {
		t.Fatalf("markdown export failed: %q", state.NoteExportProblem())
	}
	if writer.calls != 1 || string(writer.content) != source {
		t.Fatalf("writer got name=%q content=%q", writer.name, writer.content)
	}
	if state.NoteExportPath() != "/exports/Restart-database.markdown" {
		t.Fatalf("export path = %q", state.NoteExportPath())
	}

	// HTML is self-contained and escaped.
	if !state.ExportNote(appvault.NoteExportHTML) {
		t.Fatalf("HTML export failed: %q", state.NoteExportProblem())
	}
	page := string(writer.content)
	for _, want := range []string{"<!DOCTYPE html>", "<code>systemctl restart postgres</code>"} {
		if !strings.Contains(page, want) {
			t.Fatalf("HTML export is missing %q", want)
		}
	}

	// ZIP and the whole-Vault export reach the writer.
	if !state.ExportNote(appvault.NoteExportZIP) {
		t.Fatalf("ZIP export failed: %q", state.NoteExportProblem())
	}
	if !state.ExportAllNotes() {
		t.Fatalf("all-Note export failed: %q", state.NoteExportProblem())
	}
	if writer.name != "all.zip" {
		t.Fatalf("all-Note export name = %q", writer.name)
	}
	reader, err := zip.NewReader(bytes.NewReader(writer.content), int64(len(writer.content)))
	if err != nil || len(reader.File) != 1 {
		t.Fatalf("all-Note ZIP = %v, %v", reader, err)
	}

	// A cancelled save dialog is not reported as a failure or a success.
	before := state.NoteExportPath()
	writer.cancel = true
	if state.ExportNote(appvault.NoteExportMarkdown) {
		t.Fatalf("a cancelled export reported success")
	}
	if state.NoteExportProblem() != ProblemNone || state.NoteExportPath() != before {
		t.Fatalf("cancel left problem=%q path=%q", state.NoteExportProblem(), state.NoteExportPath())
	}
	writer.cancel = false

	// A writer failure is localized through ProblemNoteExport.
	writer.err = errors.New("disk full")
	if state.ExportNote(appvault.NoteExportHTML) {
		t.Fatalf("a failed writer reported success")
	}
	if state.NoteExportProblem() != ProblemNoteExport {
		t.Fatalf("writer problem = %q", state.NoteExportProblem())
	}
}

func TestNoteStateWithoutStorageReportsInternal(t *testing.T) {
	state := NewState(nil)
	state.BeginCreateNote()
	state.NoteForm().Title = "x"
	if err := state.SaveNote(); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemInternal)
	}
	if err := state.SaveNoteGroup(); err != nil {
		t.Fatalf("SaveNoteGroup: %v", err)
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("group problem = %q, want %q", state.Problem(), ProblemInternal)
	}
}
