package vault

import (
	"context"
	"fmt"
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

// markerLine is a list marker and the item text its own line holds.
type markerLine struct{ marker, text string }

// noteListFixtures are lists whose items hold more than one block (a
// paragraph and a nested list). Ticket 22's viewer centered the marker
// against the whole item, which pushed it onto a child's line; these fixtures
// pin the laid-out geometry instead of only "the list rendered".
var noteListFixtures = []struct {
	name    string
	title   string
	content string
	check   func(t *testing.T, tt *ui.Tester, width, height int)
}{
	{
		name:  "ordered item with a nested unordered list",
		title: "Ordered nested",
		content: "1. First\n" +
			"2. Second\n" +
			"   - sub A\n" +
			"     - deep A\n" +
			"   - sub B\n" +
			"3. Third",
		check: func(t *testing.T, tt *ui.Tester, width, height int) {
			for _, line := range []markerLine{
				{"1.", "First"}, {"2.", "Second"}, {"3.", "Third"},
			} {
				assertMarkerOnFirstLine(t, tt, width, height, line.marker, line.text)
			}
			// The first bullet in the layout is sub A's, the item's nested
			// child: it keeps its own marker on its own line.
			assertMarkerOnFirstLine(t, tt, width, height, "•", "sub A")
			assertIndentedAfter(t, tt, width, height, "Second", "sub A")
			assertIndentedAfter(t, tt, width, height, "sub A", "deep A")
			assertBelow(t, tt, width, height, "sub A", "sub B")
		},
	},
	{
		name:  "unordered item with nested children",
		title: "Unordered nested",
		content: "- Parent\n" +
			"  - child one\n" +
			"  - child two\n" +
			"- Next",
		check: func(t *testing.T, tt *ui.Tester, width, height int) {
			// The first bullet is Parent's; the defect put it on the
			// "child one" line.
			assertMarkerOnFirstLine(t, tt, width, height, "•", "Parent")
			assertIndentedAfter(t, tt, width, height, "Parent", "child one")
			assertIndentedAfter(t, tt, width, height, "Parent", "child two")
			// Parent, child one, child two, and Next each keep a marker, and
			// none is inherited by a child.
			if got := countTexts(tt, "•"); got != 4 {
				t.Fatalf("bullets in the laid out list = %d, want 4: %v", got, tt.Texts())
			}
			assertBelow(t, tt, width, height, "child two", "Next")
		},
	},
	{
		name:  "unordered item with an ordered list three levels deep",
		title: "Mixed nested",
		content: "- Parent\n" +
			"  1. step one\n" +
			"     - deep one\n" +
			"  2. step two\n" +
			"- Next",
		check: func(t *testing.T, tt *ui.Tester, width, height int) {
			// Parent's bullet comes first in the layout; deep one's bullet is
			// the second, past the reach of Find, so its line is checked
			// through the text and the bullet count.
			assertMarkerOnFirstLine(t, tt, width, height, "•", "Parent")
			assertMarkerOnFirstLine(t, tt, width, height, "1.", "step one")
			assertMarkerOnFirstLine(t, tt, width, height, "2.", "step two")
			assertIndentedAfter(t, tt, width, height, "Parent", "step one")
			assertIndentedAfter(t, tt, width, height, "step one", "deep one")
			if got := countTexts(tt, "•"); got != 3 {
				t.Fatalf("bullets in the mixed list = %d, want 3: %v", got, tt.Texts())
			}
		},
	},
}

// TestVaultNoteViewerAlignsListMarkersWithFirstLines asserts, from the laid
// out boxes, that an item's marker shares its first line even when the item
// continues with a nested list, at both production sizes.
func TestVaultNoteViewerAlignsListMarkersWithFirstLines(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		for _, fixture := range noteListFixtures {
			t.Run(fmt.Sprintf("%s/%dx%d", fixture.name, size.width, size.height), func(t *testing.T) {
				state, _ := newNoteProductionState(t)
				state.BeginCreateNote()
				state.NoteForm().Title = fixture.title
				state.NoteForm().Content = fixture.content
				if err := state.SaveNote(); err != nil {
					t.Fatalf("seed note %q: %v", fixture.title, err)
				}
				tt := newProductionTester(t, state, size.width, size.height)
				scrollToEnd(tt, size.width, size.height)
				if err := tt.Click(m.VaultNoteOpen()); err != nil {
					t.Fatalf("open the note: %v", err)
				}
				tt.Frame()
				fixture.check(t, tt, size.width, size.height)
			})
		}
	}
}

// assertMarkerOnFirstLine fails unless marker and first are on the same
// visual line: their boxes overlap by at least half the marker's height.
func assertMarkerOnFirstLine(t *testing.T, tt *ui.Tester, width, height int, marker, first string) {
	t.Helper()
	markerRect, ok := tt.Find(marker)
	if !ok || markerRect.H <= 0 {
		t.Fatalf("%dx%d: marker %q is not laid out: %+v", width, height, marker, markerRect)
	}
	firstRect, ok := tt.Find(first)
	if !ok || firstRect.H <= 0 {
		t.Fatalf("%dx%d: item text %q is not laid out: %+v", width, height, first, firstRect)
	}
	overlap := min(markerRect.Y+markerRect.H, firstRect.Y+firstRect.H) - max(markerRect.Y, firstRect.Y)
	if overlap < min(markerRect.H, firstRect.H)/2 {
		t.Fatalf("%dx%d: marker %q %+v is not on the first line of %q %+v",
			width, height, marker, markerRect, first, firstRect)
	}
}

func countTexts(tt *ui.Tester, text string) int {
	n := 0
	for _, shown := range tt.Texts() {
		if shown == text {
			n++
		}
	}
	return n
}

// assertBelow fails unless inner is laid out below outer.
func assertBelow(t *testing.T, tt *ui.Tester, width, height int, outer, inner string) {
	t.Helper()
	outerRect, ok := tt.Find(outer)
	if !ok {
		t.Fatalf("%dx%d: %q is not laid out", width, height, outer)
	}
	innerRect, ok := tt.Find(inner)
	if !ok {
		t.Fatalf("%dx%d: %q is not laid out", width, height, inner)
	}
	if innerRect.Y <= outerRect.Y {
		t.Fatalf("%dx%d: %q %+v is not below %q %+v", width, height, inner, innerRect, outer, outerRect)
	}
}

// assertIndentedAfter fails unless inner starts below outer and further in.
func assertIndentedAfter(t *testing.T, tt *ui.Tester, width, height int, outer, inner string) {
	t.Helper()
	assertBelow(t, tt, width, height, outer, inner)
	outerRect, _ := tt.Find(outer)
	innerRect, _ := tt.Find(inner)
	if innerRect.X <= outerRect.X {
		t.Fatalf("%dx%d: %q %+v is not indented past %q %+v", width, height, inner, innerRect, outer, outerRect)
	}
}

// noteEditorFixture is longer than the editor's 240 DIP content field, so a
// source scrolled to its end hides the first lines.
const noteEditorFixture = "# Restart PostgreSQL\n\n" +
	"Run **pg_isready** before `systemctl restart <db>`.\n\n" +
	"## Steps\n\n" +
	"1. Check the log.\n2. Notify the team.\n\n" +
	"> Prefer a maintenance window.\n\n" +
	"```sh\npg_isready -h db.internal\n```\n\n" +
	"| Check | Command |\n| --- | --- |\n| Ready | `pg_isready` |\n\n" +
	"The last line is here."

// TestVaultNoteEditorOpensAtTheSourceStart opens, cancels, and reopens the
// editor at both production sizes. Focusing the Content label keeps the
// caret where opening put it, and its visual position is read from the text
// area's own scroll: a caret on the first line is the source's start.
func TestVaultNoteEditorOpensAtTheSourceStart(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state, _ := newNoteProductionState(t)
			state.BeginCreateNote()
			state.NoteForm().Title = "Restart database"
			state.NoteForm().Content = noteEditorFixture
			if err := state.SaveNote(); err != nil {
				t.Fatalf("seed the note: %v", err)
			}
			tt := newProductionTester(t, state, size.width, size.height)
			scrollToEnd(tt, size.width, size.height)

			for round := 0; round < 2; round++ {
				if err := tt.Click(m.VaultHostEdit()); err != nil {
					t.Fatalf("round %d: open the Note editor: %v", round, err)
				}
				tt.Frame()
				label, ok := tt.Find(m.VaultNoteContent())
				if !ok || label.Y < 0 || label.Y+label.H > float32(size.height) {
					t.Fatalf("round %d: the Content field is not visible: %+v", round, label)
				}
				// A click on the label focuses the control without moving the
				// caret, so the caret still shows where opening put it.
				if err := tt.Click(m.VaultNoteContent()); err != nil {
					t.Fatalf("round %d: focus the Content field: %v", round, err)
				}
				tt.Frame()
				if !tt.Focused(m.VaultNoteContent()) {
					t.Fatalf("round %d: the Content field did not take the focus", round)
				}
				caret, active := tt.TextCaret()
				if !active {
					t.Fatalf("round %d: the text input is not active", round)
				}
				// A caret MyGo could not place in the text falls back to the
				// whole element box (its height), which must not pass as a
				// visible first line.
				if caret.H <= 0 || caret.H > label.H*2 {
					t.Fatalf("round %d: the caret bounds are not a visible line: %+v", round, caret)
				}
				if caret.Y < label.Y-1 || caret.Y > label.Y+label.H+1 {
					t.Fatalf("round %d: the source opens scrolled away from its first line: "+
						"caret %+v, Content label %+v", round, caret, label)
				}
				if err := tt.Click(m.VaultHostCancel()); err != nil {
					t.Fatalf("round %d: cancel the editor: %v", round, err)
				}
				tt.Frame()
				scrollToEnd(tt, size.width, size.height)
			}
		})
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
