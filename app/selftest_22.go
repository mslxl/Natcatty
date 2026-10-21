//go:build selftest

package app

// Ticket 22 self-test. Press F11, then 2, then 2. The check proves Vault Notes
// and Note Groups, the native Markdown viewer, search, and all three exports
// through the real SQLite store, the real application service, the real Vault
// view, and the real shell chrome:
//
//   - CRUD: a Note Group and a Note are created, updated, searched, and
//     deleted through the view. Deleting a group keeps its Notes at the root.
//   - Native Markdown: the viewer renders headings, emphasis, code, lists,
//     quotes, a table, and a link without raw Markdown syntax or a webview.
//   - Exports: Markdown is byte-identical to the source and re-imports
//     identically; HTML is escaped and self-contained; single and whole-Vault
//     ZIPs preserve the Note Group path. The files are written and read back.
//   - Layout: the real shell, sidebar, and tab strip render the list and the
//     viewer at 1100x720 and 720x480 without horizontal overflow, and the
//     viewer scrolls to its last paragraph. A nested list item's marker sits
//     on the item's first line, not centered against its children, and the
//     source editor opens at the beginning of a long Note, including after
//     cancel and reopen.
//
// Result: %TEMP%\natcatty-selftest-22.json. Every resource is closed and the
// temporary directory removed, so F11,2,2 passes twice in one process.

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest22Ticket = 22

const selftest22Content = "# Restart PostgreSQL\n\n" +
	"Run **pg_isready** before `systemctl restart <db>`.\n\n" +
	"- Check the log\n- Notify the team\n\n" +
	"> Prefer a maintenance window.\n\n" +
	"```sh\npg_isready -h db.internal\n```\n\n" +
	"| Check | Command |\n| --- | --- |\n| Ready | `pg_isready` |\n\n" +
	"See [the runbook](https://example.com/runbook).\n\n" +
	"The end."

type selftest22 struct {
	dir      string
	store    *store.Store
	service  *appvault.Service
	shell    *shellstate.State
	sessions *appsession.Service
	factory  *selftest21Factory
	state    *vaultstate.State
	tester   *ui.Tester
	writer   *selftest22Writer

	mainNote domainvault.Note
}

func (t *selftest22) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest22Ticket,
		Name:   "Vault Notes, native Markdown, search, and exports",
		Steps: []selftest.Step{
			{Name: "note-and-group-crud", Run: t.crudStep},
			{Name: "native-markdown-viewer", Run: t.markdownStep},
			{Name: "note-search", Run: t.searchStep},
			{Name: "exports-round-trip", Run: t.exportStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest22) crudStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")

	// Create a Note Group through the real editor.
	t.scrollVault()
	if err := t.tester.Click(m.VaultNoteGroupNew()); err != nil {
		return t.fail(fmt.Errorf("open the Note Group editor: %w", err))
	}
	t.tester.Frame()
	t.state.NoteGroupForm().Path = "ops/runbooks"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the Note Group: %w", err))
	}
	t.tester.Frame()
	if !hasNoteGroup(t.service, "ops/runbooks") {
		return t.fail(fmt.Errorf("Note Group ops/runbooks was not persisted"))
	}

	// Create the main Note through the real editor.
	t.scrollVault()
	if err := t.tester.Click(m.VaultNoteNew()); err != nil {
		return t.fail(fmt.Errorf("open the Note editor: %w", err))
	}
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save an empty Note: %w", err))
	}
	t.tester.Frame()
	if !t.tester.HasText(m.VaultNoteProblem()) {
		return t.fail(fmt.Errorf("the missing-title problem is not localized: %v",
			t.tester.Texts()))
	}
	form := t.state.NoteForm()
	form.Title = "Restart database"
	form.Group = "ops/runbooks"
	form.Content = selftest22Content
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the Note: %w", err))
	}
	t.tester.Frame()
	main, ok := noteNamed(t.state, "Restart database")
	if !ok {
		return t.fail(fmt.Errorf("the Note is not listed: %v", t.tester.Texts()))
	}
	if main.Content != selftest22Content || main.Group != "ops/runbooks" {
		return t.fail(fmt.Errorf("stored Note = %+v", main))
	}
	t.mainNote = main

	// Update it through the editor.
	if !t.state.BeginEditNote(main.ID) {
		return t.fail(fmt.Errorf("BeginEditNote rejected the listed Note"))
	}
	t.state.NoteForm().Title = "Restart PostgreSQL"
	t.state.NoteForm().Content = selftest22Content + "\n\nVerified."
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the updated Note: %w", err))
	}
	t.tester.Frame()
	if updated, ok := noteNamed(t.state, "Restart PostgreSQL"); !ok ||
		!strings.HasSuffix(updated.Content, "Verified.") {
		return t.fail(fmt.Errorf("the Note update was not listed"))
	}
	t.mainNote, _ = noteNamed(t.state, "Restart PostgreSQL")

	// Create and delete a throwaway Note through the confirmation.
	t.scrollVault()
	if err := t.tester.Click(m.VaultNoteNew()); err != nil {
		return t.fail(fmt.Errorf("open the second Note editor: %w", err))
	}
	t.tester.Frame()
	t.state.NoteForm().Title = "Delete me"
	t.state.NoteForm().Content = "temporary"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the second Note: %w", err))
	}
	t.tester.Frame()
	throwaway, ok := noteNamed(t.state, "Delete me")
	if !ok {
		return t.fail(fmt.Errorf("the second Note is not listed"))
	}
	t.state.RequestDeleteNote(throwaway.ID)
	t.tester.Frame()
	if !t.tester.HasText(m.VaultNoteDeleteConfirm("Delete me")) {
		return t.fail(fmt.Errorf("the Note delete confirmation is missing: %v",
			t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm the Note delete: %w", err))
	}
	t.tester.Frame()
	if _, ok := noteNamed(t.state, "Delete me"); ok {
		return t.fail(fmt.Errorf("the deleted Note is still listed"))
	}

	// A group delete must not delete its Notes: they move to the root.
	t.state.BeginCreateNoteGroup()
	t.state.NoteGroupForm().Path = "trash"
	if err := t.state.SaveNoteGroup(); err != nil {
		return t.fail(fmt.Errorf("create the trash group: %w", err))
	}
	t.state.BeginCreateNote()
	t.state.NoteForm().Title = "Moved"
	t.state.NoteForm().Group = "trash"
	t.state.NoteForm().Content = "kept"
	if err := t.state.SaveNote(); err != nil {
		return t.fail(fmt.Errorf("create the moved Note: %w", err))
	}
	t.state.RequestDeleteNoteGroup("trash")
	t.tester.Frame()
	if !t.tester.HasText(m.VaultNoteGroupDeleteConfirm("trash")) {
		return t.fail(fmt.Errorf("the group delete confirmation is missing: %v",
			t.tester.Texts()))
	}
	t.scrollVault()
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm the group delete: %w", err))
	}
	t.tester.Frame()
	moved, ok := noteNamed(t.state, "Moved")
	if !ok || moved.Group != "" {
		return t.fail(fmt.Errorf("group delete removed or kept the Note in a group: %+v", moved))
	}
	if hasNoteGroup(t.service, "trash") {
		return t.fail(fmt.Errorf("the trash group survived its delete"))
	}
	if err := t.service.DeleteNote(context.Background(), moved.ID); err != nil {
		return t.fail(fmt.Errorf("delete the moved Note: %w", err))
	}
	if err := t.state.Reload(); err != nil {
		return t.fail(fmt.Errorf("reload after cleanup: %w", err))
	}
	t.tester.Frame()
	return selftest.Passed(
		"real store and Vault view: group create/delete, Note create/update/delete, "+
			"and a non-cascading group delete",
		"appvault.Service + store.Store + vaultui.View + ui.Tester")
}

func (t *selftest22) markdownStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	if !t.state.BeginViewNote(t.mainNote.ID) {
		return t.fail(fmt.Errorf("the viewer rejected the listed Note"))
	}
	t.tester.Frame()
	if !t.state.ViewingNote() {
		return t.fail(fmt.Errorf("the native viewer did not open"))
	}
	for _, want := range []string{
		"Restart PostgreSQL", "Run ", "pg_isready", "systemctl restart <db>",
		"Check the log", "Notify the team", "Prefer a maintenance window.",
		"pg_isready -h db.internal", "Check", "Command", "Ready",
		"the runbook", "https://example.com/runbook", m.VaultNoteExportHTML(),
	} {
		if !t.tester.HasText(want) {
			return t.fail(fmt.Errorf("%q is not rendered natively: %v", want, t.tester.Texts()))
		}
	}
	if t.tester.HasText("**pg_isready**") || t.tester.HasText("# Restart") {
		return t.fail(fmt.Errorf("the viewer shows raw Markdown syntax: %v", t.tester.Texts()))
	}
	if !t.tester.HasText(m.VaultNoteViewerGroup("ops/runbooks")) {
		return t.fail(fmt.Errorf("the viewer does not show the group: %v", t.tester.Texts()))
	}
	t.state.CloseNoteView()
	t.tester.Frame()
	return selftest.Passed(
		"the native viewer renders headings, emphasis, code, lists, quotes, a table, and a link",
		"domain/markdown + vaultui.noteViewer + ui.RichText")
}

func (t *selftest22) searchStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	for _, query := range []string{"PG_ISREADY", "maintenance", "ops/runbooks", "Verified."} {
		t.state.SetNoteSearch(query)
		t.tester.Frame()
		t.scrollVault()
		if t.state.NotesMatchingSearch() == nil ||
			len(t.state.NotesMatchingSearch()) != 1 ||
			t.state.NotesMatchingSearch()[0].ID != t.mainNote.ID {
			return t.fail(fmt.Errorf("query %q matched %+v", query,
				t.state.NotesMatchingSearch()))
		}
		if !t.tester.HasText("Restart PostgreSQL") {
			return t.fail(fmt.Errorf("query %q did not render its match: %v", query,
				t.tester.Texts()))
		}
	}
	t.state.SetNoteSearch("absent-token")
	t.tester.Frame()
	if len(t.state.NotesMatchingSearch()) != 0 {
		return t.fail(fmt.Errorf("an unmatched search returned Notes"))
	}
	if !t.tester.HasText(m.VaultNoteSearchEmpty("absent-token")) {
		return t.fail(fmt.Errorf("the no-match message is missing: %v", t.tester.Texts()))
	}
	t.state.SetNoteSearch("")
	t.tester.Frame()
	if len(t.state.NotesMatchingSearch()) != 1 {
		return t.fail(fmt.Errorf("clearing the query did not list the Note"))
	}
	return selftest.Passed(
		"search is case-insensitive over title, Markdown source, and group path, and the no-match state is localized",
		"vaultstate.SetNoteSearch + domainvault.SearchNotes + vaultui.View")
}

func (t *selftest22) exportStep(*ui.Context) selftest.Outcome {
	if !t.state.BeginViewNote(t.mainNote.ID) {
		return t.fail(fmt.Errorf("the viewer rejected the Note before export"))
	}
	t.tester.Frame()

	// Markdown: byte-identical, written to disk, and re-imported identically.
	if !t.state.ExportNote(appvault.NoteExportMarkdown) {
		return t.fail(fmt.Errorf("Markdown export failed: %q", t.state.NoteExportProblem()))
	}
	markdownExport := t.writer.last
	if !bytes.Equal(markdownExport.content, []byte(t.mainNote.Content)) {
		return t.fail(fmt.Errorf("Markdown export changed the source"))
	}
	written, err := os.ReadFile(markdownExport.path)
	if err != nil {
		return t.fail(fmt.Errorf("read the Markdown export: %w", err))
	}
	if !bytes.Equal(written, []byte(t.mainNote.Content)) {
		return t.fail(fmt.Errorf("the written Markdown differs from the Note"))
	}
	reimported, err := t.service.CreateNote(context.Background(), domainvault.NoteDraft{
		Title:   t.mainNote.Title,
		Content: string(written),
		Group:   "ops/runbooks",
	})
	if err != nil {
		return t.fail(fmt.Errorf("re-import the exported Markdown: %w", err))
	}
	if reimported.Content != t.mainNote.Content {
		return t.fail(fmt.Errorf("the re-imported content differs"))
	}
	if err := t.service.DeleteNote(context.Background(), reimported.ID); err != nil {
		return t.fail(fmt.Errorf("delete the re-imported Note: %w", err))
	}

	// HTML: self-contained and escaped.
	if !t.state.ExportNote(appvault.NoteExportHTML) {
		return t.fail(fmt.Errorf("HTML export failed: %q", t.state.NoteExportProblem()))
	}
	page := string(t.writer.last.content)
	for _, want := range []string{
		"<!DOCTYPE html>", "Restart PostgreSQL", "systemctl restart &lt;db&gt;",
		"<code>pg_isready</code>", "<table>", "https://example.com/runbook",
	} {
		if !strings.Contains(page, want) {
			return t.fail(fmt.Errorf("HTML export is missing %q", want))
		}
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "<link ") ||
		strings.Contains(page, "src=\"http") {
		return t.fail(fmt.Errorf("HTML export is not self-contained"))
	}

	// One Note as ZIP, with its group path as directories.
	if !t.state.ExportNote(appvault.NoteExportZIP) {
		return t.fail(fmt.Errorf("ZIP export failed: %q", t.state.NoteExportProblem()))
	}
	entry, content, err := selftest22ArchiveEntry(t.writer.last.content,
		"ops/runbooks/Restart PostgreSQL.md")
	if err != nil {
		return t.fail(err)
	}
	if entry == "" || content != t.mainNote.Content {
		return t.fail(fmt.Errorf("single ZIP entry = %q / %q", entry, content))
	}

	// The whole Vault as ZIP.
	if !t.state.ExportAllNotes() {
		return t.fail(fmt.Errorf("all-Note export failed: %q", t.state.NoteExportProblem()))
	}
	if t.writer.last.name != "netcatty-notes-all.zip" {
		return t.fail(fmt.Errorf("all-Note archive name = %q", t.writer.last.name))
	}
	_, all, err := selftest22ArchiveEntry(t.writer.last.content,
		"ops/runbooks/Restart PostgreSQL.md")
	if err != nil {
		return t.fail(err)
	}
	if all != t.mainNote.Content {
		return t.fail(fmt.Errorf("all-Note ZIP content differs"))
	}
	t.state.CloseNoteView()
	t.tester.Frame()
	return selftest.Passed(
		"Markdown round-trips byte for byte, HTML is escaped and self-contained, and ZIPs preserve group paths",
		"vaultui.View + appvault.ExportNote/ExportNotes + archive/zip + markdown.HTMLPage")
}

func (t *selftest22) layoutStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	t.shell.Flush()
	t.shell.ShowVault()
	view := shellui.View(t.shell)

	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		page := ui.NewTester(view, size.width, size.height)
		page.SetLocale("en")
		page.Frame()
		scrollShell21(page, size.width, size.height)
		for _, want := range []string{
			"Vault", m.VaultNoteListTitle(1), "Restart PostgreSQL", m.VaultNoteOpen(),
		} {
			if rect, ok := page.Find(want); !ok ||
				rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 ||
				rect.Y < -0.5 || rect.Y+rect.H > float32(size.height)+0.5 {
				return t.fail(fmt.Errorf("%dx%d: %q is missing or clipped: %+v, %v",
					size.width, size.height, want, rect, ok))
			}
		}
		if err := page.Click(m.VaultNoteOpen()); err != nil {
			return t.fail(fmt.Errorf("%dx%d: open the Note viewer: %w", size.width, size.height, err))
		}
		page.Frame()
		for _, want := range []string{
			"Restart PostgreSQL", "Check the log", "Prefer a maintenance window.",
			m.VaultNoteExportMarkdown(), m.VaultNoteExportHTML(), m.VaultNoteExportZIP(),
			m.VaultHostEdit(), m.VaultNoteBack(),
		} {
			if !page.HasText(want) {
				return t.fail(fmt.Errorf("%dx%d: %q is not rendered: %v",
					size.width, size.height, want, page.Texts()))
			}
		}
		for _, text := range page.Texts() {
			rect, ok := page.Find(text)
			if !ok || rect.W <= 0 || rect.H <= 0 {
				continue
			}
			if rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 {
				return t.fail(fmt.Errorf("%dx%d: %q overflows horizontally: %+v",
					size.width, size.height, text, rect))
			}
			if rect.H < 8 {
				return t.fail(fmt.Errorf("%dx%d: %q is unreadably small: %+v",
					size.width, size.height, text, rect))
			}
		}
		scrollShell21(page, size.width, size.height)
		if rect, ok := page.Find("Verified."); !ok ||
			rect.Y < -0.5 || rect.Y+rect.H > float32(size.height)+0.5 {
			return t.fail(fmt.Errorf("%dx%d: the viewer did not scroll to the end: %+v, %v",
				size.width, size.height, rect, ok))
		}
		if err := page.Click(m.VaultNoteBack()); err != nil {
			return t.fail(fmt.Errorf("%dx%d: close the viewer: %w", size.width, size.height, err))
		}
		page.Frame()

		// A nested list item's marker must share its first line. Swap the
		// Note's source for the fixture, then put the original back.
		if _, err := t.service.UpdateNote(context.Background(), t.mainNote.ID, domainvault.NoteDraft{
			Title: t.mainNote.Title, Group: t.mainNote.Group, Content: selftest22NestedLists,
		}); err != nil {
			return t.fail(fmt.Errorf("%dx%d: seed the nested lists: %w", size.width, size.height, err))
		}
		if err := t.state.Reload(); err != nil {
			return t.fail(fmt.Errorf("%dx%d: reload the nested lists: %w", size.width, size.height, err))
		}
		if !t.state.BeginViewNote(t.mainNote.ID) {
			return t.fail(fmt.Errorf("%dx%d: open the nested list viewer", size.width, size.height))
		}
		page.Frame()
		for _, line := range []struct{ marker, text string }{
			{"1.", "First"}, {"2.", "Second"}, {"3.", "Third"}, {"•", "sub A"},
		} {
			if err := selftest22MarkerOnFirstLine(page, line.marker, line.text); err != nil {
				return t.fail(fmt.Errorf("%dx%d: %w", size.width, size.height, err))
			}
		}
		t.state.CloseNoteView()
		page.Frame()
		if _, err := t.service.UpdateNote(context.Background(), t.mainNote.ID, domainvault.NoteDraft{
			Title: t.mainNote.Title, Group: t.mainNote.Group, Content: t.mainNote.Content,
		}); err != nil {
			return t.fail(fmt.Errorf("%dx%d: restore the Note: %w", size.width, size.height, err))
		}
		if err := t.state.Reload(); err != nil {
			return t.fail(fmt.Errorf("%dx%d: reload the Note: %w", size.width, size.height, err))
		}
		page.Frame()

		// Opening, cancelling, and reopening the source editor must each
		// start at the beginning of the long Note.
		for round := 0; round < 2; round++ {
			if !t.state.BeginEditNote(t.mainNote.ID) {
				return t.fail(fmt.Errorf("%dx%d: open the Note editor", size.width, size.height))
			}
			page.Frame()
			if err := selftest22SourceStartsAtTop(page, m, size.width, size.height); err != nil {
				return t.fail(fmt.Errorf("%dx%d, round %d: %w", size.width, size.height, round, err))
			}
			if err := page.Click(m.VaultHostCancel()); err != nil {
				return t.fail(fmt.Errorf("%dx%d: cancel the Note editor: %w", size.width, size.height, err))
			}
			page.Frame()
		}
	}
	t.cleanup()
	return selftest.Passed(
		"the real shell renders the Note list and native viewer at 1100x720 and 720x480 "+
			"without clipping, nested list markers sit on their item's first line, and the "+
			"source editor opens at the beginning on open and reopen",
		"shellui.View + vaultui.View + ui.Tester")
}

// selftest22NestedLists is a list whose second item continues with a nested
// list, the shape that exposed a centered marker.
const selftest22NestedLists = "1. First\n" +
	"2. Second\n" +
	"   - sub A\n" +
	"     - deep A\n" +
	"   - sub B\n" +
	"3. Third"

// selftest22MarkerOnFirstLine proves from the laid out boxes that a list
// item's marker shares the first line of the item's own text.
func selftest22MarkerOnFirstLine(page *ui.Tester, marker, first string) error {
	markerRect, ok := page.Find(marker)
	if !ok || markerRect.H <= 0 {
		return fmt.Errorf("marker %q is not laid out: %+v", marker, markerRect)
	}
	firstRect, ok := page.Find(first)
	if !ok || firstRect.H <= 0 {
		return fmt.Errorf("item text %q is not laid out: %+v", first, firstRect)
	}
	overlap := min(markerRect.Y+markerRect.H, firstRect.Y+firstRect.H) - max(markerRect.Y, firstRect.Y)
	if overlap < min(markerRect.H, firstRect.H)/2 {
		return fmt.Errorf("marker %q %+v is not on the first line of %q %+v",
			marker, markerRect, first, firstRect)
	}
	return nil
}

// selftest22SourceStartsAtTop focuses the Content field without moving its
// caret and proves the caret, and so the text area's scroll, is at the first
// line.
func selftest22SourceStartsAtTop(page *ui.Tester, m locales.Messages, width, height int) error {
	label, ok := page.Find(m.VaultNoteContent())
	if !ok || label.Y < 0 || label.Y+label.H > float32(height) {
		return fmt.Errorf("the Content field is not visible: %+v", label)
	}
	if err := page.Click(m.VaultNoteContent()); err != nil {
		return fmt.Errorf("focus the Content field: %w", err)
	}
	page.Frame()
	caret, active := page.TextCaret()
	if !active {
		return fmt.Errorf("the text input is not active")
	}
	// A caret MyGo could not place in the text falls back to the whole
	// element box (its height), which must not pass as a visible first line.
	if caret.H <= 0 || caret.H > label.H*2 {
		return fmt.Errorf("the caret bounds are not a visible line: %+v", caret)
	}
	if caret.Y < label.Y-1 || caret.Y > label.Y+label.H+1 {
		return fmt.Errorf("the source opens away from its first line: caret %+v, label %+v",
			caret, label)
	}
	if caret.X < 0 || caret.X > float32(width) {
		return fmt.Errorf("the caret is outside the window: %+v", caret)
	}
	return nil
}

func (t *selftest22) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-22-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	database, err := store.Open(context.Background(), filepath.Join(dir, "vault.db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("open store: %w", err)
	}
	service := appvault.NewService(database, appvault.WithNotes(database))
	writer := &selftest22Writer{dir: dir}

	factory := &selftest21Factory{transports: make(map[*selftest21Transport]bool)}
	sessions := appsession.NewService(factory)
	var state *vaultstate.State
	var shell *shellstate.State
	state = vaultstate.NewState(service,
		vaultstate.WithNotes(service),
		vaultstate.WithNoteFileWriter(writer),
	)
	shell = shellstate.NewState(
		shellstate.WithSessions(sessions, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest21Surface{}, nil
		}),
		shellstate.WithVault(vaultui.View(state)),
	)
	if err := state.Reload(); err != nil {
		_ = database.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("load the Vault: %w", err)
	}
	if err := shell.NewTab(); err != nil {
		_ = database.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("open the active local Session: %w", err)
	}
	shell.ShowVault()

	tester := ui.NewTester(vaultui.View(state), 1100, 900)
	tester.SetLocale("en")
	tester.Frame()

	t.dir, t.store, t.service = dir, database, service
	t.state, t.shell, t.sessions, t.factory = state, shell, sessions, factory
	t.tester, t.writer = tester, writer
	return nil
}

func (t *selftest22) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest22) cleanup() {
	if t.shell != nil {
		t.shell.Shutdown()
		t.shell.CloseAllSessions()
	}
	if t.sessions != nil {
		t.sessions.CloseAll()
	}
	if t.factory != nil {
		t.factory.closeAll()
	}
	if t.store != nil {
		_ = t.store.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.store, t.service = "", nil, nil
	t.state, t.shell, t.sessions, t.factory = nil, nil, nil, nil
	t.tester, t.writer = nil, nil
	t.mainNote = domainvault.Note{}
}

func (t *selftest22) scrollVault() { scrollShell21(t.tester, 1100, 900) }

// selftest22Writer writes exports into the self-test's temporary directory and
// records the last one for content checks.
type selftest22Writer struct {
	dir    string
	last   selftest22Export
	writes int
}

type selftest22Export struct {
	name    string
	content []byte
	path    string
}

func (w *selftest22Writer) Save(name string, content []byte) (string, error) {
	path := filepath.Join(w.dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	w.writes++
	w.last = selftest22Export{name: name, content: append([]byte(nil), content...), path: path}
	return path, nil
}

func noteNamed(state *vaultstate.State, title string) (domainvault.Note, bool) {
	for _, note := range state.Notes() {
		if note.Title == title {
			return note, true
		}
	}
	return domainvault.Note{}, false
}

func hasNoteGroup(service *appvault.Service, path string) bool {
	groups, err := service.ListNoteGroups(context.Background())
	if err != nil {
		return false
	}
	for _, group := range groups {
		if group.Path == path {
			return true
		}
	}
	return false
}

// selftest22ArchiveEntry reads one entry from a ZIP and returns its name and
// content.
func selftest22ArchiveEntry(content []byte, want string) (string, string, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", "", fmt.Errorf("open ZIP: %w", err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		if file.Name != want {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return "", "", fmt.Errorf("open %s: %w", want, err)
		}
		data, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			return "", "", fmt.Errorf("read %s: %w", want, err)
		}
		return file.Name, string(data), nil
	}
	return "", "", fmt.Errorf("ZIP entries %v, want %q", names, want)
}
