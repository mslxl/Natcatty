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
