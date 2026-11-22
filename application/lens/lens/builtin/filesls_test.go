package builtin

import (
	"strings"
	"testing"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func spanText(spans []lens.Span) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

// A plain `ls` prints names and little else, so the lens builds no widget and
// the block keeps the raw output. A list of the bare names added nothing, and
// a list guessed from an error line or a recursive listing's heading was worse
// than the text the program printed.
func TestFilesLSPlainListingStaysRaw(t *testing.T) {
	cases := map[string][]string{
		"names":             {"Applications  cp-solution  notes.txt"},
		"icons":             {"\uf115 Applications   \uf15b notes.txt"},
		"quoted":            {"\uf115 'My Documents'  \uf15b notes.txt"},
		"program error":     {"ls: cannot access 'missing': No such file or directory"},
		"recursive heading": {"src:", "  a.go", "  b.go"},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			l := lens.ByID("files.ls")
			if l == nil {
				t.Fatal("files.ls is not registered")
			}
			state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"--icons", "auto"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range lines {
				if err := l.Line(state, model.Line{Text: line}); err != nil {
					t.Fatal(err)
				}
			}
			node, err := l.View(state)
			if err != nil {
				t.Fatal(err)
			}
			if node != nil {
				t.Fatalf("view = %T, want raw output (nil view)", node)
			}
		})
	}
}

// A plain `ls` on PowerShell is Get-ChildItem, whose columns are worth a
// table: the listing still gets a lens, unlike the name-only case above.
func TestFilesLSPowerShellListingStillBuildsTable(t *testing.T) {
	l := lens.ByID("files.ls")
	state, err := l.Open(model.Cmd{Program: "ls"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`Directory: C:\Users\me`,
		"Mode                 LastWriteTime         Length Name",
		"----                 -------------         ------ ----",
		"d-----         7/22/2026   8:11 PM                .git",
	} {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := node.(lens.Column); !ok {
		t.Fatalf("view = %T, want a column (heading + table)", node)
	}
}

// The long format puts the same icon in front of the name column.
func TestFilesLSLongListingStripsIconGlyph(t *testing.T) {
	l := lens.ByID("files.ls")
	if l == nil {
		t.Fatal("files.ls is not registered")
	}
	state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"-l", "--icons"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Line(state, model.Line{
		Text: "-rw-r--r--  1 mslxl staff 1.5K Nov 19 12:00 \uf15b notes.txt",
	}); err != nil {
		t.Fatal(err)
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	table, ok := node.(lens.Table)
	if !ok {
		t.Fatalf("view = %T, want lens.Table", node)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("table rows = %d, want 1", len(table.Rows))
	}
	name := spanText(table.Rows[0][len(table.Rows[0])-1].Spans)
	if name != "notes.txt" {
		t.Fatalf("name cell = %q, want notes.txt", name)
	}
}

// eza's own long format is not ls's: no link count, a "." for regular files,
// humanised sizes and a day-first date. The user's `ls` and `ll` are eza
// aliases, so every listing they ran arrived in this shape and parsed nothing.
func TestFilesLSEzaDefaultLongListing(t *testing.T) {
	l := lens.ByID("files.ls")
	state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"-l"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"drwxr-xr-x@   - mslxl 22 Nov 21:54 adir",
		".rw-r--r--@   0 mslxl 22 Nov 21:54 new.txt",
		".rw-r--r--@ 2.8k mslxl 21 Nov 13:58 build.log",
		".rw-r--r--@  11M mslxl 21 Nov 13:03 a.a",
		".rw-r--r--@   0 mslxl  2 Jan  2020 old.txt",
	} {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	table, ok := node.(lens.Table)
	if !ok {
		t.Fatalf("view = %T, want lens.Table", node)
	}
	heads := make([]string, len(table.Cols))
	for i, col := range table.Cols {
		heads[i] = col.Head
	}
	if strings.Contains(strings.Join(heads, ","), "Links") {
		t.Fatalf("eza prints no link count, but the table has columns %v", heads)
	}
	if len(table.Rows) != 5 {
		t.Fatalf("table rows = %d, want 5", len(table.Rows))
	}
	last := table.Rows[0][len(table.Rows[0])-1]
	if got := spanText(last.Spans); got != "adir" {
		t.Fatalf("first name = %q, want adir", got)
	}
	if got := spanText(table.Rows[1][len(table.Rows[1])-1].Spans); got != "new.txt" {
		t.Fatalf("second name = %q, want new.txt", got)
	}
	if got := spanText(table.Rows[4][len(table.Rows[4])-1].Spans); got != "old.txt" {
		t.Fatalf("old-date name = %q, want old.txt", got)
	}
}

// A name eza has to quote must arrive unquoted, and an arrow inside quotes is
// part of the name, not a symlink's target.
func TestFilesLSEzaQuotedNamesAndLinks(t *testing.T) {
	l := lens.ByID("files.ls")
	state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"-l"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		".rw-r--r--@   0 mslxl 22 Nov 21:59 'a -> b.txt'",
		".rw-r--r--@   0 mslxl 22 Nov 21:59 \"it's.txt\"",
		".rw-r--r--@   0 mslxl 22 Nov 21:59 'two  spaces.txt'",
		"lrwxr-xr-x@   - mslxl 22 Nov 21:54 alink -> new.txt",
		"lrwxr-xr-x@   - mslxl 22 Nov 21:59 'link name' -> 'with space.txt'",
		".rw-r--r--@   0 mslxl 22 Nov 21:59  'icon  name.txt'",
	} {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	table, ok := node.(lens.Table)
	if !ok {
		t.Fatalf("view = %T, want lens.Table", node)
	}
	want := []string{
		"a -> b.txt",
		"it's.txt",
		"two  spaces.txt",
		"alink",
		"link name",
		"icon  name.txt",
	}
	if len(table.Rows) != len(want) {
		t.Fatalf("table rows = %d, want %d", len(table.Rows), len(want))
	}
	for i, w := range want {
		cell := table.Rows[i][len(table.Rows[i])-1]
		got := spanText(cell.Spans)
		if i == 3 || i == 4 {
			if !strings.HasPrefix(got, w+" → ") {
				t.Fatalf("row %d name = %q, want %q with its target", i, got, w)
			}
			continue
		}
		if got != w {
			t.Fatalf("row %d name = %q, want %q", i, got, w)
		}
	}
}

// -g/--group adds eza's group column, and the header --header prints is not a
// listing row.
func TestFilesLSEzaGroupAndHeader(t *testing.T) {
	l := lens.ByID("files.ls")
	state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"-l", "-g", "--header"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"Permissions Size User  Group Date Modified Name",
		".rw-r--r--@   0 mslxl wheel 22 Nov 21:54 new.txt",
	} {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	table, ok := node.(lens.Table)
	if !ok {
		t.Fatalf("view = %T, want lens.Table", node)
	}
	if len(table.Rows) != 1 {
		t.Fatalf("table rows = %d, want 1 (the header is not a row)", len(table.Rows))
	}
	var heads []string
	for _, col := range table.Cols {
		heads = append(heads, col.Head)
	}
	if !strings.Contains(strings.Join(heads, ","), "Group") {
		t.Fatalf("columns = %v, want the group column", heads)
	}
}

// --git adds a two-cell status column between the date and the name; a
// user's `alias eza='eza --icons auto --git'` applies it to `ls` and `ll`
// too. The status is not part of the file's name.
func TestFilesLSEzaGitStatusColumn(t *testing.T) {
	l := lens.ByID("files.ls")
	state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"-l", "--git"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		".rw-r--r--@  28k mslxl 22 Nov 20:37 -M blockview.go",
		".rw-r--r--@ 2.1k mslxl 22 Nov 18:32 -- blockview_sticky_test.go",
		"drwxr-xr-x@    - mslxl 22 Nov 20:58 -N appview",
		".rw-r--r--@ 0 mslxl 22 Nov 18:32 ??   'new file.txt'",
	}
	for _, line := range lines {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	table, ok := node.(lens.Table)
	if !ok {
		t.Fatalf("view = %T, want lens.Table", node)
	}
	want := []string{"blockview.go", "blockview_sticky_test.go", "appview", "new file.txt"}
	if len(table.Rows) != len(want) {
		t.Fatalf("table rows = %d, want %d", len(table.Rows), len(want))
	}
	for i, name := range want {
		cell := table.Rows[i][len(table.Rows[i])-1]
		if got := spanText(cell.Spans); got != name {
			t.Fatalf("row %d name = %q, want %q", i, got, name)
		}
	}
}

// --time-style swaps eza's date for ISO shapes: "MM-DD HH:MM" (iso),
// "YYYY-MM-DD HH:MM" (long-iso) and the full-iso stamp with its zone.
func TestFilesLSEzaTimeStyles(t *testing.T) {
	cases := map[string]string{
		"iso":      ".rw-r--r--@ 5 mslxl 11-22 21:59 notes.txt",
		"long-iso": ".rw-r--r--@ 5 mslxl 2026-11-22 21:59 notes.txt",
		"full-iso": ".rw-r--r--@ 5 mslxl 2026-11-22 21:59:00.000000000 +0800 notes.txt",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			l := lens.ByID("files.ls")
			state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"-l"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Line(state, model.Line{Text: line}); err != nil {
				t.Fatal(err)
			}
			node, err := l.View(state)
			if err != nil {
				t.Fatal(err)
			}
			table, ok := node.(lens.Table)
			if !ok {
				t.Fatalf("view = %T, want lens.Table for %q", node, line)
			}
			if got := spanText(table.Rows[0][len(table.Rows[0])-1].Spans); got != "notes.txt" {
				t.Fatalf("name = %q, want notes.txt", got)
			}
		})
	}
}
