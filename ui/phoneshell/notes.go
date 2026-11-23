package phoneshell

// The Notes pages. The Vault section lists Vault Notes grouped by Note Group
// and edits them through the same application Vault service the desktop
// surface uses, so a Note changed on the phone is the Note a desktop reload
// reads. The Session Tools surface draws the same records in its overlay, with
// its own per-Pane selection and draft, so opening Notes from a terminal never
// changes the route or detaches the terminal.
//
// Export never writes the Vault. A Note is rendered by the shared service into
// Markdown or HTML (or every Note into a ZIP), handed to a file writer, and on
// a phone that writer copies it through the system's save picker
// (mygo.Dialog.SaveCopy, which is Files on iOS and the storage access
// framework on Android). Cancelling the picker returns no path and changes
// nothing.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// noteUnavailable is the Notes pages' state when the Vault service was built
// without Note persistence. No production root leaves it unset.
const noteUnavailable = "Notes are not available on this device."

// noteForm is one open Note editor. Content is the Markdown source exactly as
// typed; the shared service normalizes the title and group and keeps the
// source byte for byte.
type noteForm struct {
	id      string
	title   string
	group   string
	content string
}

// noteGroupForm is one open Note Group editor. originalPath is empty for a new
// group and the path being renamed otherwise.
type noteGroupForm struct {
	originalPath string
	path         string
}

// notesViewState is one Notes surface's navigation state: the search, the open
// viewer, the open editors, and the pending delete. The Vault section owns one,
// and every Pane's Tools surface owns another, so a draft in one Pane never
// appears in another.
type notesViewState struct {
	query string

	draft        *noteForm
	draftProblem string

	groupDraft   *noteGroupForm
	groupProblem string

	viewing     string
	pendingNote string

	pendingGroup string

	result        string
	resultProblem bool

	exportMessage string
	exportProblem bool
}

// notePages owns the shared Note and Note Group records and the Vault section's
// view state. A Tools surface draws the same records with its own view state.
type notePages struct {
	store  vaultstate.Notes
	writer vaultstate.NoteFileWriter

	status  vaultStatus
	problem string
	notes   []domainvault.Note
	groups  []domainvault.NoteGroup

	vault notesViewState
}

func newNotePages() *notePages { return &notePages{status: vaultLoading} }

// WithNoteWriter gives the Notes pages the place an export goes. Without it the
// production writer copies the rendered file through the system save picker;
// tests inject a writer that records the bytes and can cancel.
func WithNoteWriter(writer vaultstate.NoteFileWriter) Option {
	return func(s *Shell) { s.vault.notes.writer = writer }
}

// phoneNoteWriter writes one export to a temporary file and hands it to the
// system's save picker through mygo.Dialog.SaveCopy. On phones the picker is
// Files (iOS) or the storage access framework (Android); on a desktop it is the
// native save dialog. An empty returned path means the user cancelled, and the
// temporary file is removed either way: the Vault Note itself is never touched.
type phoneNoteWriter struct{}

func (phoneNoteWriter) Save(name string, content []byte) (string, error) {
	dir, err := mygo.App.Path(mygo.PathTemp)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "natcatty-note-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return mygo.Dialog.SaveCopy(path, name)
}

// loadNotes binds the shared Vault service and replaces the Note and Note Group
// lists. A service built without Note persistence is this page's own
// unavailable state, never the Shell's Load failure.
func (v *vaultPages) loadNotes(ctx context.Context) {
	n := v.notes
	if n == nil {
		return
	}
	if store, ok := v.store.(vaultstate.Notes); ok {
		n.store = store
	}
	if n.store == nil {
		n.status, n.problem = vaultFailed, noteUnavailable
		return
	}
	_ = n.reload(ctx)
}

// reload re-reads only the Note lists, so a save does not disturb the other
// Vault pages' state.
func (n *notePages) reload(ctx context.Context) error {
	if n.store == nil {
		n.status, n.problem = vaultFailed, noteUnavailable
		return errors.New("notes are unavailable")
	}
	notes, err := n.store.ListNotes(ctx)
	if err != nil {
		n.status, n.problem = vaultFailed, "Could not load Notes."
		return err
	}
	groups, err := n.store.ListNoteGroups(ctx)
	if err != nil {
		n.status, n.problem = vaultFailed, "Could not load Note Groups."
		return err
	}
	n.notes, n.groups, n.status, n.problem = notes, groups, vaultReady, ""
	return nil
}

// notesSummary is the Vault landing row's second line.
func (v *vaultPages) notesSummary() string {
	if v.notes == nil {
		return "Unavailable"
	}
	switch v.notes.status {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(v.notes.notes) {
	case 0:
		return "No Notes yet"
	case 1:
		return "1 Note"
	default:
		return fmt.Sprintf("%d Notes", len(v.notes.notes))
	}
}

// drawNotes renders the Vault section's Notes page. Opening a Note, editor, or
// group editor replaces the list so a half-filled form cannot be mistaken for
// a saved record.
func (v *vaultPages) drawNotes(c *ui.Context) {
	n := v.notes
	if n == nil || n.store == nil {
		width := pageContentWidth(c)
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, noteUnavailable, nil)
			})
		})
		return
	}
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			n.drawContent(c, &n.vault, false)
		})
	})
}

// drawTool renders the Notes tool inside one Pane's Tools surface. The surface
// already owns the Scroll and the terminal stays attached, so this draws the
// content only.
func (n *notePages) drawTool(c *ui.Context, view *notesViewState) {
	if n == nil || n.store == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, noteUnavailable, nil)
		return
	}
	n.drawContent(c, view, true)
}

func (n *notePages) drawContent(c *ui.Context, view *notesViewState, tools bool) {
	switch {
	case view.draft != nil:
		n.drawEditor(c, view)
	case view.groupDraft != nil:
		n.drawGroupEditor(c, view)
	case view.viewing != "":
		n.drawViewer(c, view, tools)
	default:
		n.drawList(c, view)
	}
}

// drawList renders the grouped list. A search flattens it into matches across
// title, Markdown source, and Note Group path.
func (n *notePages) drawList(c *ui.Context, view *notesViewState) {
	kit.SectionBar(c, "Notes", func() {
		if kit.GhostButton(c, icons.RefreshCw, "Refresh notes").Clicked() {
			_ = n.reload(context.Background())
		}
	})
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.GhostButton(c, icons.Plus, "Add note").Clicked() {
			view.beginCreateNote("")
		}
		if kit.GhostButton(c, icons.FolderPlus, "Add group").Clicked() {
			view.beginCreateGroup()
		}
		if kit.GhostButton(c, icons.Download, "Export all").Clicked() {
			n.exportAll(view)
		}
	})
	kit.FormRow(c, "Search", func() {
		ui.TextInput(c, &view.query).Placeholder("Titles, text, or groups").FillWidth()
	})

	switch n.status {
	case vaultLoading:
		kit.EmptyHint(c, "Loading Notes…")
		return
	case vaultFailed:
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, n.problem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				_ = n.reload(context.Background())
			}
		})
		if len(n.notes) == 0 && len(n.groups) == 0 {
			return
		}
	default:
		if n.problem != "" {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, n.problem, nil)
		}
	}
	n.drawResult(c, view)

	if strings.TrimSpace(view.query) != "" {
		matches := domainvault.SearchNotes(n.notes, view.query)
		ui.Text(c, fmt.Sprintf("%d matching Notes", len(matches))).FontSize(12).
			TextColor(c.Theme().TextMuted)
		if len(matches) == 0 {
			kit.EmptyHint(c, "No Notes match this search.")
			return
		}
		for _, note := range matches {
			note := note
			n.drawNoteRow(c, view, note, note.Group)
		}
		n.drawDeleteConfirmations(c, view)
		return
	}

	if len(n.notes) == 0 && len(n.groups) == 0 {
		kit.EmptyHint(c, "No Notes yet. Notes added on the desktop appear here.")
		return
	}
	for _, group := range n.groups {
		group := group
		n.drawGroupRow(c, view, group)
		for _, note := range noteInGroup(n.notes, group.Path) {
			note := note
			n.drawNoteRow(c, view, note, "")
		}
	}
	root := noteInGroup(n.notes, "")
	if len(root) > 0 {
		ui.Text(c, "Vault root").FontSize(13).FontWeight(600)
		for _, note := range root {
			note := note
			n.drawNoteRow(c, view, note, "")
		}
	}
	n.drawDeleteConfirmations(c, view)
}

// noteInGroup returns the Notes directly in path. It is the shared domain
// rule, so the phone groups a Note exactly as the desktop tree does.
func noteInGroup(notes []domainvault.Note, path string) []domainvault.Note {
	return domainvault.NotesInGroup(notes, path)
}

func (n *notePages) drawGroupRow(c *ui.Context, view *notesViewState, group domainvault.NoteGroup) {
	row := kit.ListRow(c).Label("Edit group " + group.Path)
	handled := false
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, group.Path).FontWeight(600).SingleLine()
			ui.Text(c, fmt.Sprintf("%d Notes", len(noteInGroup(n.notes, group.Path)))).
				FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
		})
		if kit.IconButton(c, icons.Trash, "Delete group "+group.Path).Clicked() {
			view.pendingGroup = group.Path
			view.pendingNote = ""
			handled = true
		}
		ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
	})
	if !handled && row.Clicked() {
		view.beginEditGroup(group.Path)
	}
}

func (n *notePages) drawNoteRow(c *ui.Context, view *notesViewState, note domainvault.Note, meta string) {
	row := kit.ListRow(c).Label("Open note " + note.Title)
	handled := false
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, note.Title).FontWeight(600).SingleLine()
			detail := noteSummary(note)
			if meta != "" {
				detail = meta + " · " + detail
			}
			ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
		})
		if kit.IconButton(c, icons.Pencil, "Edit note "+note.Title).Clicked() {
			view.beginEditNote(note)
			handled = true
		}
		if kit.IconButton(c, icons.Trash, "Delete note "+note.Title).Clicked() {
			view.pendingNote = note.ID
			view.pendingGroup = ""
			handled = true
		}
		ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
	})
	if !handled && row.Clicked() {
		view.viewing = note.ID
		view.exportMessage = ""
		view.exportProblem = false
		view.result = ""
		view.resultProblem = false
	}
}

// noteSummary is the row's second line: the Note Group or the first source
// line, whichever identifies the Note better.
func noteSummary(note domainvault.Note) string {
	if group := strings.TrimSpace(note.Group); group != "" {
		return group
	}
	for _, line := range strings.Split(note.Content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 60 {
				return line[:60] + "…"
			}
			return line
		}
	}
	return "Empty Note"
}

func (n *notePages) drawResult(c *ui.Context, view *notesViewState) {
	if view.exportMessage != "" {
		tone := c.Theme().Success
		icon := icons.CircleCheck
		if view.exportProblem {
			tone, icon = c.Theme().Danger, icons.TriangleAlert
		}
		kit.Notice(c, icon, tone, view.exportMessage, nil)
	}
	if view.result != "" {
		tone := c.Theme().Success
		icon := icons.CircleCheck
		if view.resultProblem {
			tone, icon = c.Theme().Danger, icons.TriangleAlert
		}
		kit.Notice(c, icon, tone, view.result, nil)
	}
}

func (n *notePages) drawDeleteConfirmations(c *ui.Context, view *notesViewState) {
	if view.pendingNote != "" {
		note, ok := n.noteByID(view.pendingNote)
		if !ok {
			view.pendingNote = ""
		} else {
			cancelled, confirmed := kit.ConfirmBar(c,
				"Delete the Note "+note.Title+"?",
				func() {
					kit.EmptyHint(c, "The Note is removed from the shared Vault on every device.")
				},
				"Cancel", "Delete")
			switch {
			case cancelled:
				view.pendingNote = ""
			case confirmed:
				n.deleteNote(context.Background(), view, note.ID)
			}
		}
	}
	if view.pendingGroup != "" {
		path := view.pendingGroup
		cancelled, confirmed := kit.ConfirmBar(c,
			"Delete the Note Group "+path+"?",
			func() {
				kit.EmptyHint(c, "Its Notes are kept and move to the Vault root.")
			},
			"Cancel", "Delete")
		switch {
		case cancelled:
			view.pendingGroup = ""
		case confirmed:
			n.deleteGroup(context.Background(), view, path)
		}
	}
}

// beginCreateNote opens the editor with an empty draft. group prefills the
// Note Group the caller is looking at, if any.
func (v *notesViewState) beginCreateNote(group string) {
	v.draft = &noteForm{group: group}
	v.draftProblem = ""
	v.groupDraft = nil
	v.groupProblem = ""
	v.viewing = ""
	v.pendingNote = ""
	v.pendingGroup = ""
	v.result = ""
	v.resultProblem = false
}

// beginEditNote opens the editor with a listed Note's editable fields.
func (v *notesViewState) beginEditNote(note domainvault.Note) {
	v.draft = &noteForm{
		id:      note.ID,
		title:   note.Title,
		group:   note.Group,
		content: note.Content,
	}
	v.draftProblem = ""
	v.groupDraft = nil
	v.groupProblem = ""
	v.viewing = ""
	v.pendingNote = ""
	v.pendingGroup = ""
	v.result = ""
	v.resultProblem = false
}

func (v *notesViewState) beginCreateGroup() {
	v.groupDraft = &noteGroupForm{}
	v.groupProblem = ""
	v.draft = nil
	v.draftProblem = ""
	v.viewing = ""
	v.pendingNote = ""
	v.pendingGroup = ""
	v.result = ""
	v.resultProblem = false
}

func (v *notesViewState) beginEditGroup(path string) {
	v.groupDraft = &noteGroupForm{originalPath: path, path: path}
	v.groupProblem = ""
	v.draft = nil
	v.draftProblem = ""
	v.viewing = ""
	v.pendingNote = ""
	v.pendingGroup = ""
	v.result = ""
	v.resultProblem = false
}

func (n *notePages) drawEditor(c *ui.Context, view *notesViewState) {
	f := view.draft
	title := "New Note"
	if f.id != "" {
		title = "Edit Note"
	}
	kit.SectionBar(c, title, nil)
	if view.draftProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, view.draftProblem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry save").Clicked() {
				n.submitNote(view)
			}
		})
	}
	kit.FormRow(c, "Title", func() {
		ui.TextInput(c, &f.title).Placeholder("A name you recognize").FillWidth()
	})
	kit.FormRow(c, "Group", func() {
		ui.TextInput(c, &f.group).Placeholder("ops/runbooks").FillWidth()
	}).Description("A slash-separated Note Group path. Leave blank for the Vault root.")
	kit.FormRow(c, "Content", func() {
		ui.TextArea(c, &f.content).Height(240).Placeholder("Markdown")
	}).Description("The Markdown source is saved exactly as typed.")
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.PrimaryButton(c, "", "Save note").Clicked() {
			n.submitNote(view)
		}
		if kit.SecondaryButton(c, "", "Cancel").Clicked() {
			view.draft = nil
			view.draftProblem = ""
		}
	})
}

func (n *notePages) drawGroupEditor(c *ui.Context, view *notesViewState) {
	f := view.groupDraft
	title := "New Note Group"
	if f.originalPath != "" {
		title = "Rename Note Group"
	}
	kit.SectionBar(c, title, nil)
	if view.groupProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, view.groupProblem, nil)
	}
	kit.FormRow(c, "Group path", func() {
		ui.TextInput(c, &f.path).Placeholder("ops/runbooks").FillWidth()
	}).Description("Letters, digits, underscores, and dashes, separated by slashes.")
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.PrimaryButton(c, "", "Save group").Clicked() {
			n.submitGroup(view)
		}
		if kit.SecondaryButton(c, "", "Cancel").Clicked() {
			view.groupDraft = nil
			view.groupProblem = ""
		}
	})
}

// submitNote writes the draft through the shared Vault service. A failure
// keeps the editor and every typed character; the error names a retry and the
// Save action stays available.
func (n *notePages) submitNote(view *notesViewState) {
	f := view.draft
	if f == nil {
		return
	}
	if n.store == nil {
		view.draftProblem = noteUnavailable
		return
	}
	draft := domainvault.NoteDraft{Title: f.title, Group: f.group, Content: f.content}
	var err error
	if f.id == "" {
		_, err = n.store.CreateNote(context.Background(), draft)
	} else {
		_, err = n.store.UpdateNote(context.Background(), f.id, draft)
	}
	if err != nil {
		// Keep the draft exactly as typed. The page shows a retry action and
		// the Save button remains, so the user can fix the cause and submit
		// again without retyping.
		view.draftProblem = noteSaveProblemText(err)
		return
	}
	view.draft = nil
	view.draftProblem = ""
	view.result = "Note saved."
	view.resultProblem = false
	_ = n.reload(context.Background())
}

func (n *notePages) submitGroup(view *notesViewState) {
	f := view.groupDraft
	if f == nil {
		return
	}
	if n.store == nil {
		view.groupProblem = noteUnavailable
		return
	}
	var err error
	if f.originalPath == "" {
		_, err = n.store.CreateNoteGroup(context.Background(),
			domainvault.NoteGroupDraft{Path: f.path})
	} else {
		_, err = n.store.RenameNoteGroup(context.Background(), f.originalPath, f.path)
	}
	if err != nil {
		view.groupProblem = noteGroupProblemText(err)
		return
	}
	view.groupDraft = nil
	view.groupProblem = ""
	view.result = "Note Group saved."
	view.resultProblem = false
	_ = n.reload(context.Background())
}

func (n *notePages) deleteNote(ctx context.Context, view *notesViewState, id string) {
	view.pendingNote = ""
	if n.store == nil {
		view.result, view.resultProblem = noteUnavailable, true
		return
	}
	if err := n.store.DeleteNote(ctx, id); err != nil {
		view.result, view.resultProblem = "Could not delete the Note.", true
		return
	}
	if view.viewing == id {
		view.viewing = ""
	}
	view.result, view.resultProblem = "Note deleted.", false
	_ = n.reload(ctx)
}

func (n *notePages) deleteGroup(ctx context.Context, view *notesViewState, path string) {
	view.pendingGroup = ""
	if n.store == nil {
		view.result, view.resultProblem = noteUnavailable, true
		return
	}
	if err := n.store.DeleteNoteGroup(ctx, path); err != nil {
		view.result, view.resultProblem = "Could not delete the Note Group.", true
		return
	}
	view.result, view.resultProblem = "Note Group deleted. Its Notes moved to the Vault root.", false
	_ = n.reload(ctx)
}

// drawViewer renders one Note's Markdown source. The viewer is read-only; Edit
// opens the editor, and the export actions hand a rendered copy to the user.
func (n *notePages) drawViewer(c *ui.Context, view *notesViewState, tools bool) {
	note, ok := n.noteByID(view.viewing)
	if !ok {
		view.viewing = ""
		view.result, view.resultProblem = "The Note no longer exists.", true
		n.drawList(c, view)
		return
	}
	if tools {
		if kit.GhostButton(c, icons.ChevronLeft, "Back to Notes").Clicked() {
			view.viewing = ""
			view.exportMessage = ""
			view.exportProblem = false
			return
		}
	} else {
		kit.SectionBar(c, "Note", nil)
	}
	ui.Text(c, note.Title).FontSize(22).Bold().Selectable()
	if note.Group == "" {
		ui.Text(c, "Vault root").FontSize(12).TextColor(c.Theme().TextMuted)
	} else {
		ui.Text(c, note.Group).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.PrimaryButton(c, "", "Edit note").Clicked() {
			view.beginEditNote(note)
			return
		}
		if kit.SecondaryButton(c, "", "Export Markdown").Clicked() {
			n.exportNote(view, note.ID, appvault.NoteExportMarkdown)
		}
		if kit.SecondaryButton(c, "", "Export HTML").Clicked() {
			n.exportNote(view, note.ID, appvault.NoteExportHTML)
		}
	})
	n.drawResult(c, view)
	ui.Row(c).FillWidth().MinWidth(0).Padding(12).Background(c.Theme().Surface).
		Border(1, c.Theme().Border).Children(func() {
		ui.Text(c, note.Content).Selectable()
	})
}

// exportNote renders one Note and hands it to the writer. Cancelling changes
// nothing: an empty saved path is not an error and the Note keeps its content.
func (n *notePages) exportNote(view *notesViewState, id string, format appvault.NoteExportFormat) {
	view.exportMessage = ""
	view.exportProblem = false
	n.export(view, func() (appvault.NoteExport, error) {
		return n.store.ExportNote(context.Background(), id, format)
	})
}

// exportAll renders every Note as the shared ZIP archive, which preserves Note
// Group paths as directories.
func (n *notePages) exportAll(view *notesViewState) {
	view.exportMessage = ""
	view.exportProblem = false
	if len(n.notes) == 0 {
		view.exportMessage = "There are no Notes to export."
		view.exportProblem = false
		return
	}
	n.export(view, func() (appvault.NoteExport, error) {
		return n.store.ExportNotes(context.Background(), appvault.NoteExportZIP)
	})
}

func (n *notePages) export(view *notesViewState,
	build func() (appvault.NoteExport, error)) {

	if n.store == nil {
		view.exportMessage, view.exportProblem = noteUnavailable, true
		return
	}
	if n.writer == nil {
		view.exportMessage = "Saving files is not available on this device."
		view.exportProblem = true
		return
	}
	export, err := build()
	if err != nil {
		view.exportMessage = "Could not export the Note. " + err.Error()
		view.exportProblem = true
		return
	}
	saved, err := n.writer.Save(export.Filename, export.Content)
	if err != nil {
		view.exportMessage = "Could not save the export: " + err.Error()
		view.exportProblem = true
		return
	}
	if saved == "" {
		// The user cancelled the system picker. Claiming an export would be
		// false, and nothing about the Note has changed.
		return
	}
	view.exportMessage = "Saved " + export.Filename + "."
	view.exportProblem = false
}

func (n *notePages) noteByID(id string) (domainvault.Note, bool) {
	for _, note := range n.notes {
		if note.ID == id {
			return note, true
		}
	}
	return domainvault.Note{}, false
}

// noteSaveProblemText explains a Note save failure using the shared validation
// fields, so the phone and desktop name the same problem.
func noteSaveProblemText(err error) string {
	if errors.Is(err, appvault.ErrNotesUnavailable) {
		return noteUnavailable
	}
	var invalid *domainvault.ValidationError
	if errors.As(err, &invalid) {
		switch invalid.Field {
		case domainvault.FieldNote:
			if invalid.Rule == domainvault.RuleRequired {
				return "Enter a title for the Note."
			}
			return "Check the Note title."
		case domainvault.FieldNoteContent:
			return "The Note is too long."
		case domainvault.FieldNoteGroup:
			return "Check the Note Group path. Use slash-separated names."
		}
	}
	return "Could not save the Note. Your changes are kept; try again."
}

func noteGroupProblemText(err error) string {
	if errors.Is(err, appvault.ErrNotesUnavailable) {
		return noteUnavailable
	}
	var appErr *appvault.Error
	if errors.As(err, &appErr) && appErr.Kind == appvault.KindConflict {
		return "A Note Group with that path already exists."
	}
	var invalid *domainvault.ValidationError
	if errors.As(err, &invalid) {
		switch invalid.Field {
		case domainvault.FieldNoteGroup:
			return "Check the Note Group path. Use slash-separated names."
		}
	}
	return "Could not save the Note Group."
}
