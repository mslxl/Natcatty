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

// eza's --icons (the `ls` alias on many setups) prints a Nerd Font glyph from
// a private use area before each entry, as a whitespace-separated field of its
// own. Reading those glyphs as names listed every icon on a row above its
// file, and the repeated glyphs gave the list equal keys.
func TestFilesLSPlainListingIgnoresIconGlyphs(t *testing.T) {
	l := lens.ByID("files.ls")
	if l == nil {
		t.Fatal("files.ls is not registered")
	}
	state, err := l.Open(model.Cmd{Program: "eza", Args: []string{"--icons", "auto", "--git"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		" Applications    cp-solution    Documents",
		" go              notes.txt",
	} {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	list, ok := node.(lens.List)
	if !ok {
		t.Fatalf("view = %T, want lens.List", node)
	}
	var got []string
	for _, item := range list.Items {
		got = append(got, spanText(item.Label))
	}
	want := []string{"Applications", "cp-solution", "Documents", "go", "notes.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("list labels = %q, want %q", got, want)
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
		Text: "-rw-r--r--  1 mslxl staff 1.5K Nov 19 12:00  notes.txt",
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
