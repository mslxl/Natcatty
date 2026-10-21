package state

import (
	"context"
	"strings"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Notes is the Vault Note use-case seam. The application Service implements
// it; tests provide a fake.
type Notes interface {
	ListNotes(ctx context.Context) ([]domainvault.Note, error)
	CreateNote(ctx context.Context, draft domainvault.NoteDraft) (domainvault.Note, error)
	UpdateNote(ctx context.Context, id string, draft domainvault.NoteDraft) (domainvault.Note, error)
	DeleteNote(ctx context.Context, id string) error

	ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error)
	CreateNoteGroup(ctx context.Context, draft domainvault.NoteGroupDraft) (domainvault.NoteGroup, error)
	RenameNoteGroup(ctx context.Context, path, newPath string) (domainvault.NoteGroup, error)
	DeleteNoteGroup(ctx context.Context, path string) error

	ExportNote(ctx context.Context, id string, format appvault.NoteExportFormat) (appvault.NoteExport, error)
	ExportNotes(ctx context.Context, format appvault.NoteExportFormat) (appvault.NoteExport, error)
}

// NoteFileWriter saves a generated export. An empty path with a nil error
// means the user cancelled the save dialog. The app wires this to a native
// save dialog; tests record the bytes.
type NoteFileWriter interface {
	Save(suggestedName string, content []byte) (string, error)
}

// NoteForm is the editable text of one Vault Note. Content is the Markdown
// source exactly as the user typed it.
type NoteForm struct {
	ID      string
	Title   string
	Group   string
	Content string
}

// NoteGroupForm is the editable path of one Note Group.
type NoteGroupForm struct {
	OriginalPath string
	Path         string
}

// WithNotes gives the Vault surface Vault Note management.
func WithNotes(notes Notes) Option {
	return func(s *State) { s.notes = notes }
}

// WithNoteFileWriter gives the Vault surface the place exports go.
func WithNoteFileWriter(writer NoteFileWriter) Option {
	return func(s *State) { s.noteWriter = writer }
}

// Notes returns the listed Vault Notes in repository order.
func (s *State) Notes() []domainvault.Note { return s.noteItems }

// NoteCount returns the number of listed Vault Notes.
func (s *State) NoteCount() int { return len(s.noteItems) }

// NoteGroups returns saved Note Groups plus every path a Note implies.
func (s *State) NoteGroups() []domainvault.NoteGroup { return s.noteGroupItems }

// NoteGroupCount returns the number of visible Note Group paths.
func (s *State) NoteGroupCount() int { return len(s.noteGroupItems) }

// NoteByID returns one listed Vault Note.
func (s *State) NoteByID(id string) (domainvault.Note, bool) {
	for _, note := range s.noteItems {
		if note.ID == id {
			return note, true
		}
	}
	return domainvault.Note{}, false
}

// NoteGroupByPath returns one visible Note Group.
func (s *State) NoteGroupByPath(path string) (domainvault.NoteGroup, bool) {
	for _, group := range s.noteGroupItems {
		if group.Path == path {
			return group, true
		}
	}
	return domainvault.NoteGroup{}, false
}

// NotesInGroup returns the Notes directly in groupPath. The Vault root is "".
func (s *State) NotesInGroup(groupPath string) []domainvault.Note {
	return domainvault.NotesInGroup(s.noteItems, groupPath)
}

// NoteGroupNoteCount returns the direct Note count of one group path.
func (s *State) NoteGroupNoteCount(path string) int {
	return len(s.NotesInGroup(path))
}

// NoteSearch returns the current search query.
func (s *State) NoteSearch() string { return s.noteSearch }

// SetNoteSearch updates the Note search query.
func (s *State) SetNoteSearch(query string) {
	s.noteSearch = query
	s.redraw()
}

// SearchingNotes reports whether a Note search is active.
func (s *State) SearchingNotes() bool { return strings.TrimSpace(s.noteSearch) != "" }

// NotesMatchingSearch returns the Notes matching the current query in
// repository order. An empty query returns every Note.
func (s *State) NotesMatchingSearch() []domainvault.Note {
	return domainvault.SearchNotes(s.noteItems, s.noteSearch)
}

// EditingNote reports whether the Note editor is open.
func (s *State) EditingNote() bool { return s.editingNote }

// NoteForm returns the Note editor's form. It is valid while EditingNote.
func (s *State) NoteForm() *NoteForm { return &s.noteForm }

// PendingDeleteNote returns the Note awaiting delete confirmation.
func (s *State) PendingDeleteNote() string { return s.pendingDeleteNote }

// ViewingNote reports whether the native Note viewer is open.
func (s *State) ViewingNote() bool { return s.viewingNote != "" }

// ViewingNoteID returns the Note shown in the viewer, or "".
func (s *State) ViewingNoteID() string { return s.viewingNote }

// BeginCreateNote opens the Note editor with an empty Markdown source.
func (s *State) BeginCreateNote() {
	s.closeEditors()
	s.editingNote = true
	s.noteForm = NoteForm{}
	s.problem = ProblemNone
	s.redraw()
}

// BeginEditNote opens the Note editor with a listed Note.
func (s *State) BeginEditNote(id string) bool {
	note, ok := s.NoteByID(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingNote = true
	s.noteForm = NoteForm{
		ID:      note.ID,
		Title:   note.Title,
		Group:   note.Group,
		Content: note.Content,
	}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// BeginViewNote opens the native Markdown viewer for one listed Note.
func (s *State) BeginViewNote(id string) bool {
	if _, ok := s.NoteByID(id); !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.viewingNote = id
	s.noteExportProblem = ProblemNone
	s.noteExportPath = ""
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CloseNoteView closes the native Note viewer.
func (s *State) CloseNoteView() {
	s.viewingNote = ""
	s.noteExportProblem = ProblemNone
	s.noteExportPath = ""
	s.redraw()
}

// CancelNoteEdit closes the Note editor without saving.
func (s *State) CancelNoteEdit() {
	s.editingNote = false
	s.noteForm = NoteForm{}
	s.problem = ProblemNone
	s.redraw()
}

// SaveNote validates and persists the Note form.
func (s *State) SaveNote() error {
	if s.notes == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	draft := domainvault.NoteDraft{
		Title:   s.noteForm.Title,
		Group:   s.noteForm.Group,
		Content: s.noteForm.Content,
	}
	var err error
	if s.noteForm.ID == "" {
		_, err = s.notes.CreateNote(context.Background(), draft)
	} else {
		_, err = s.notes.UpdateNote(context.Background(), s.noteForm.ID, draft)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingNote = false
	s.noteForm = NoteForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteNote opens the delete confirmation for one Note.
func (s *State) RequestDeleteNote(id string) {
	s.pendingDeleteNote = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteNote closes the Note delete confirmation.
func (s *State) CancelDeleteNote() {
	s.pendingDeleteNote = ""
	s.redraw()
}

// ConfirmDeleteNote deletes the Note awaiting confirmation.
func (s *State) ConfirmDeleteNote() error {
	if s.pendingDeleteNote == "" {
		return nil
	}
	id := s.pendingDeleteNote
	s.pendingDeleteNote = ""
	if s.notes == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.notes.DeleteNote(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if s.viewingNote == id {
		s.viewingNote = ""
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// EditingNoteGroup reports whether the Note Group editor is open.
func (s *State) EditingNoteGroup() bool { return s.editingNoteGroup }

// NoteGroupForm returns the Note Group editor's form.
func (s *State) NoteGroupForm() *NoteGroupForm { return &s.noteGroupForm }

// PendingDeleteNoteGroup returns the Note Group awaiting confirmation.
func (s *State) PendingDeleteNoteGroup() string { return s.pendingDeleteNoteGroup }

// BeginCreateNoteGroup opens the Note Group editor with a new path.
func (s *State) BeginCreateNoteGroup() {
	s.closeEditors()
	s.editingNoteGroup = true
	s.noteGroupForm = NoteGroupForm{}
	s.problem = ProblemNone
	s.redraw()
}

// BeginEditNoteGroup opens the Note Group editor for a rename.
func (s *State) BeginEditNoteGroup(path string) bool {
	group, ok := s.NoteGroupByPath(path)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	s.closeEditors()
	s.editingNoteGroup = true
	s.noteGroupForm = NoteGroupForm{OriginalPath: group.Path, Path: group.Path}
	s.problem = ProblemNone
	s.redraw()
	return true
}

// CancelNoteGroupEdit closes the Note Group editor without saving.
func (s *State) CancelNoteGroupEdit() {
	s.editingNoteGroup = false
	s.noteGroupForm = NoteGroupForm{}
	s.problem = ProblemNone
	s.redraw()
}

// SaveNoteGroup creates or renames the edited Note Group. A rename also moves
// every Note in the subtree.
func (s *State) SaveNoteGroup() error {
	if s.notes == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	var err error
	if s.noteGroupForm.OriginalPath == "" {
		_, err = s.notes.CreateNoteGroup(context.Background(),
			domainvault.NoteGroupDraft{Path: s.noteGroupForm.Path})
	} else {
		_, err = s.notes.RenameNoteGroup(context.Background(),
			s.noteGroupForm.OriginalPath, s.noteGroupForm.Path)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editingNoteGroup = false
	s.noteGroupForm = NoteGroupForm{}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDeleteNoteGroup opens the delete confirmation for one Note Group.
func (s *State) RequestDeleteNoteGroup(path string) {
	s.pendingDeleteNoteGroup = path
	s.problem = ProblemNone
	s.redraw()
}

// CancelDeleteNoteGroup closes the Note Group delete confirmation.
func (s *State) CancelDeleteNoteGroup() {
	s.pendingDeleteNoteGroup = ""
	s.redraw()
}

// ConfirmDeleteNoteGroup deletes the group awaiting confirmation. Its Notes
// are kept and move to the Vault root, as the original non-cascading delete
// does.
func (s *State) ConfirmDeleteNoteGroup() error {
	if s.pendingDeleteNoteGroup == "" {
		return nil
	}
	path := s.pendingDeleteNoteGroup
	s.pendingDeleteNoteGroup = ""
	if s.notes == nil {
		s.problem = ProblemInternal
		s.redraw()
		return nil
	}
	if err := s.notes.DeleteNoteGroup(context.Background(), path); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// NoteExportProblem returns the failure of the last Note export.
func (s *State) NoteExportProblem() Problem { return s.noteExportProblem }

// NoteExportPath returns the path the last export wrote, or "".
func (s *State) NoteExportPath() string { return s.noteExportPath }

// ExportNote writes the viewed Note in format through the Note file writer.
// It reports false when the export failed or the user cancelled the save.
func (s *State) ExportNote(format appvault.NoteExportFormat) bool {
	if s.viewingNote == "" {
		return false
	}
	return s.export(func() (appvault.NoteExport, error) {
		return s.notes.ExportNote(context.Background(), s.viewingNote, format)
	})
}

// ExportAllNotes writes every Note as a ZIP archive through the Note file
// writer. An empty Vault is reported rather than writing an empty archive.
func (s *State) ExportAllNotes() bool {
	if len(s.noteItems) == 0 {
		s.noteExportProblem = ProblemNoteExportEmpty
		s.redraw()
		return false
	}
	return s.export(func() (appvault.NoteExport, error) {
		return s.notes.ExportNotes(context.Background(), appvault.NoteExportZIP)
	})
}

// ClearNoteExportResult clears the previous export's path and problem.
func (s *State) ClearNoteExportResult() {
	s.noteExportProblem = ProblemNone
	s.noteExportPath = ""
	s.redraw()
}

func (s *State) export(build func() (appvault.NoteExport, error)) bool {
	if s.notes == nil {
		s.noteExportProblem = ProblemInternal
		s.redraw()
		return false
	}
	if s.noteWriter == nil {
		s.noteExportProblem = ProblemNoteExportWriter
		s.redraw()
		return false
	}
	export, err := build()
	if err != nil {
		s.noteExportProblem = problemFor(err)
		s.redraw()
		return false
	}
	path, err := s.noteWriter.Save(export.Filename, export.Content)
	if err != nil {
		s.noteExportProblem = ProblemNoteExport
		s.redraw()
		return false
	}
	if path == "" {
		// The user cancelled the save dialog; do not claim an export.
		s.noteExportProblem = ProblemNone
		s.redraw()
		return false
	}
	s.noteExportPath = path
	s.noteExportProblem = ProblemNone
	s.redraw()
	return true
}
