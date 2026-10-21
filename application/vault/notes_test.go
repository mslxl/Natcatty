package vault

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeNotes struct {
	notes  []domainvault.Note
	groups []domainvault.NoteGroup
}

func (f *fakeNotes) ListNotes(context.Context) ([]domainvault.Note, error) {
	notes := append([]domainvault.Note(nil), f.notes...)
	sort.SliceStable(notes, func(i, j int) bool {
		if notes[i].OrderKey != notes[j].OrderKey {
			return notes[i].OrderKey < notes[j].OrderKey
		}
		return notes[i].Title < notes[j].Title
	})
	return notes, nil
}

func (f *fakeNotes) GetNote(_ context.Context, id string) (domainvault.Note, error) {
	for _, note := range f.notes {
		if note.ID == id {
			return note, nil
		}
	}
	return domainvault.Note{}, ErrNotFound
}

func (f *fakeNotes) SaveNote(_ context.Context, note domainvault.Note) error {
	for i, existing := range f.notes {
		if existing.ID == note.ID {
			f.notes[i] = note
			return nil
		}
	}
	f.notes = append(f.notes, note)
	return nil
}

func (f *fakeNotes) DeleteNote(_ context.Context, id string) error {
	for i, note := range f.notes {
		if note.ID == id {
			f.notes = append(f.notes[:i], f.notes[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeNotes) ListNoteGroups(context.Context) ([]domainvault.NoteGroup, error) {
	groups := append([]domainvault.NoteGroup(nil), f.groups...)
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].OrderKey != groups[j].OrderKey {
			return groups[i].OrderKey < groups[j].OrderKey
		}
		return groups[i].Path < groups[j].Path
	})
	return groups, nil
}

func (f *fakeNotes) SaveNoteGroup(_ context.Context, group domainvault.NoteGroup) error {
	for i, existing := range f.groups {
		if existing.Path == group.Path {
			f.groups[i] = group
			return nil
		}
	}
	f.groups = append(f.groups, group)
	return nil
}

func (f *fakeNotes) DeleteNoteGroup(_ context.Context, path string) error {
	for i, group := range f.groups {
		if group.Path == path {
			f.groups = append(f.groups[:i], f.groups[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func noteService(t *testing.T) (*Service, *fakeNotes) {
	t.Helper()
	repo := &fakeNotes{}
	sequence := 0
	clock := func() time.Time { return time.Unix(1700000000, 0).UTC() }
	service := NewService(nil, WithNotes(repo),
		WithNoteIDGenerator(func() string {
			sequence++
			return "note-" + strconv.Itoa(sequence)
		}),
		WithClock(clock),
	)
	return service, repo
}

func TestNoteServiceCRUD(t *testing.T) {
	service, repo := noteService(t)
	ctx := context.Background()

	note, err := service.CreateNote(ctx, domainvault.NoteDraft{
		Title:   "Restart database",
		Content: "# Restart\n\nRun `systemctl restart postgres`.\n",
		Group:   "ops/db",
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if note.ID == "" || note.OrderKey == 0 || repo.notes[0].Content != note.Content {
		t.Fatalf("created note = %+v / repo = %+v", note, repo.notes)
	}

	updated, err := service.UpdateNote(ctx, note.ID, domainvault.NoteDraft{
		Title:   "Restart PostgreSQL",
		Content: note.Content + "\nVerify with `pg_isready`.\n",
		Group:   "ops/db/prod",
	})
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if updated.Title != "Restart PostgreSQL" || updated.Group != "ops/db/prod" ||
		updated.CreatedAt != note.CreatedAt || updated.OrderKey != note.OrderKey {
		t.Fatalf("updated note = %+v", updated)
	}

	found, err := service.SearchNotes(ctx, "pg_isready")
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	if len(found) != 1 || found[0].ID != note.ID {
		t.Fatalf("search = %+v", found)
	}

	if err := service.DeleteNote(ctx, note.ID); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if err := service.DeleteNote(ctx, note.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestNoteServiceValidatesAndListsImpliedGroups(t *testing.T) {
	service, _ := noteService(t)
	ctx := context.Background()
	if _, err := service.CreateNote(ctx, domainvault.NoteDraft{Content: "no title"}); err == nil {
		t.Fatalf("a Note without a title was accepted")
	}
	if _, err := service.CreateNote(ctx, domainvault.NoteDraft{Title: "a", Group: "ops/db"}); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	groups, err := service.ListNoteGroups(ctx)
	if err != nil {
		t.Fatalf("ListNoteGroups: %v", err)
	}
	if len(groups) != 2 || groups[0].Path != "ops" || groups[1].Path != "ops/db" {
		t.Fatalf("implied groups = %+v", groups)
	}
	if _, err := service.CreateNoteGroup(ctx, domainvault.NoteGroupDraft{Path: "ops/db"}); err != nil {
		t.Fatalf("CreateNoteGroup over an implied path: %v", err)
	}
	if _, err := service.CreateNoteGroup(ctx, domainvault.NoteGroupDraft{Path: "ops/db"}); !isKind(err, KindConflict) {
		t.Fatalf("duplicate group = %v, want conflict", err)
	}
}

func TestNoteGroupRenameMovesNotesAndDeleteKeepsThemAtTheRoot(t *testing.T) {
	service, repo := noteService(t)
	ctx := context.Background()
	if _, err := service.CreateNoteGroup(ctx, domainvault.NoteGroupDraft{Path: "ops/db"}); err != nil {
		t.Fatalf("CreateNoteGroup: %v", err)
	}
	note, err := service.CreateNote(ctx, domainvault.NoteDraft{
		Title: "Backup", Content: "pg_dump", Group: "ops/db",
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	if _, err := service.RenameNoteGroup(ctx, "ops/db", "runbooks/db"); err != nil {
		t.Fatalf("RenameNoteGroup: %v", err)
	}
	if repo.notes[0].Group != "runbooks/db" {
		t.Fatalf("note group after rename = %q", repo.notes[0].Group)
	}

	if err := service.DeleteNoteGroup(ctx, "runbooks"); err != nil {
		t.Fatalf("DeleteNoteGroup: %v", err)
	}
	if len(repo.notes) != 1 || repo.notes[0].ID != note.ID || repo.notes[0].Group != "" {
		t.Fatalf("deleting a group changed its Note: %+v", repo.notes)
	}
	groups, err := service.ListNoteGroups(ctx)
	if err != nil {
		t.Fatalf("ListNoteGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("groups after delete = %+v", groups)
	}
}

func TestNoteExportsRoundTrip(t *testing.T) {
	service, _ := noteService(t)
	ctx := context.Background()
	source := "# Restart\n\nRun `systemctl restart <db>`.\n"
	note, err := service.CreateNote(ctx, domainvault.NoteDraft{
		Title: "Restart <database>", Content: source, Group: "ops/db",
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	markdownExport, err := service.ExportNote(ctx, note.ID, NoteExportMarkdown)
	if err != nil {
		t.Fatalf("ExportNote markdown: %v", err)
	}
	if string(markdownExport.Content) != source {
		t.Fatalf("markdown export = %q, want the original source", markdownExport.Content)
	}
	if markdownExport.Filename != "Restart -database-.md" {
		t.Fatalf("filename = %q", markdownExport.Filename)
	}

	// The exported Markdown re-imports identically through the normal path.
	reimported, err := service.CreateNote(ctx, domainvault.NoteDraft{
		Title: "Restart <database>", Content: string(markdownExport.Content), Group: "ops/db",
	})
	if err != nil {
		t.Fatalf("re-import exported Markdown: %v", err)
	}
	if reimported.Content != note.Content {
		t.Fatalf("round trip changed content: %q != %q", reimported.Content, note.Content)
	}

	htmlExport, err := service.ExportNote(ctx, note.ID, NoteExportHTML)
	if err != nil {
		t.Fatalf("ExportNote HTML: %v", err)
	}
	page := string(htmlExport.Content)
	for _, want := range []string{"<!DOCTYPE html>", "Restart &lt;database&gt;", "&lt;db&gt;", "<code>systemctl restart &lt;db&gt;</code>"} {
		if !strings.Contains(page, want) {
			t.Fatalf("HTML export is missing %q: %s", want, page)
		}
	}
	if strings.Contains(page, "src=\"http") || strings.Contains(page, "<link ") {
		t.Fatalf("HTML export is not self-contained: %s", page)
	}

	archiveExport, err := service.ExportNotes(ctx, NoteExportZIP)
	if err != nil {
		t.Fatalf("ExportNotes: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archiveExport.Content), int64(len(archiveExport.Content)))
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}
	entries := map[string]string{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		data, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		entries[file.Name] = string(data)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %v", entries)
	}
	if entries["ops/db/Restart -database-.md"] != source {
		t.Fatalf("archive entry = %q", entries["ops/db/Restart -database-.md"])
	}
	if entries["ops/db/Restart -database--2.md"] != source {
		t.Fatalf("duplicate entry = %q", entries["ops/db/Restart -database--2.md"])
	}
}

func TestNoteExportRejectsUnknownFormatsAndUnavailableStorage(t *testing.T) {
	service, _ := noteService(t)
	ctx := context.Background()
	note, err := service.CreateNote(ctx, domainvault.NoteDraft{Title: "x", Content: "y"})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := service.ExportNote(ctx, note.ID, NoteExportFormat("pdf")); !isKind(err, KindUnsupported) {
		t.Fatalf("unknown format = %v, want unsupported", err)
	}
	if _, err := service.ExportNotes(ctx, NoteExportMarkdown); !isKind(err, KindUnsupported) {
		t.Fatalf("multi-note markdown = %v, want unsupported", err)
	}
	empty := NewService(nil)
	if _, err := empty.ListNotes(ctx); !errors.Is(err, ErrNotesUnavailable) {
		t.Fatalf("ListNotes without a repo = %v", err)
	}
}

func isKind(err error, kind Kind) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Kind == kind
}
