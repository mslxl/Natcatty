package appview

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// row is one row of spans, as the replay draws it.
func row(text string) []ui.Span { return []ui.Span{{Text: text}} }

// A printed file name is a link on the cells it occupies, with the span a
// hover underline and a preview need.
func TestPathAtRowReportsPrintedPaths(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		col    int
		want   string
		x0, x1 int
	}{
		{"bare archive", "release.tar.gz", 0, "release.tar.gz", 0, 14},
		{"after a prompt", "$ tar -xzf release.tar.gz", 13, "release.tar.gz", 11, 25},
		{"absolute path", "see /tmp/log.txt now", 6, "/tmp/log.txt", 4, 16},
		{"path with a line", "at src/main.go:42", 5, "src/main.go:42", 3, 17},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, x0, x1, ok := pathAtRow(row(testCase.text), testCase.col)
			if !ok || got != testCase.want || x0 != testCase.x0 || x1 != testCase.x1 {
				t.Fatalf("pathAtRow(%q, %d) = %q %d..%d %v, want %q %d..%d",
					testCase.text, testCase.col, got, x0, x1, ok,
					testCase.want, testCase.x0, testCase.x1)
			}
		})
	}
}

// A dotted version number is not a printed file, and a cell outside the token
// names no link.
func TestPathAtRowIgnoresNonPaths(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
		col  int
	}{
		{"version", "version 1.2.3 is out", 9},
		{"plain prose", "no path here", 3},
		{"outside the token", "release.tar.gz", 20},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if path, _, _, ok := pathAtRow(row(testCase.text), testCase.col); ok {
				t.Fatalf("pathAtRow(%q, %d) = %q, want no path", testCase.text, testCase.col, path)
			}
		})
	}
}

// A URL reports the cells of the token without the punctuation around it, so
// the underline stops at the link and the click opens what was printed.
func TestURLAtRowReportsTheTokenSpan(t *testing.T) {
	spans := row("see (https://example.com/x), then more")
	uri, x0, x1, ok := urlAtRow(spans, 8)
	if !ok || uri != "https://example.com/x" || x0 != 5 || x1 != 26 {
		t.Fatalf("urlAtRow = %q %d..%d %v, want https://example.com/x 5..26", uri, x0, x1, ok)
	}
}

// A wide rune before the link counts two columns, as the grid draws it.
func TestPathAtRowCountsWideCells(t *testing.T) {
	path, x0, x1, ok := pathAtRow(row("日 release.tar.gz"), 5)
	if !ok || path != "release.tar.gz" || x0 != 3 || x1 != 17 {
		t.Fatalf("pathAtRow = %q %d..%d %v, want release.tar.gz 3..17", path, x0, x1, ok)
	}
}
