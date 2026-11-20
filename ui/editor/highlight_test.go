package editor

// Ticket 56: view tests. The tokenizer's colors reach the rendered frame
// through the editable TextArea, a non-matching name stays plain, and
// turning highlighting off leaves the document in one color. The last test
// checks that highlighting follows an edit without moving the caret.

import (
	"image"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	uitheme "github.com/mslxl/natcatty/ui/theme"
)

const nginxConfig = "# proxy settings\n" +
	"server {\n" +
	"    listen 80;\n" +
	"    server_name \"example.com\";\n" +
	"}\n"

// renderEditor opens name with content and renders the editor. It returns
// the view's theme as well, so the test can compare the painted pixels with
// the exact colors the highlighter asks for.
func renderEditor(t *testing.T, name, content string) (*ui.Tester, *State, *ui.Theme) {
	t.Helper()
	service := &fakeService{}
	state := NewState(service)
	openFixture(t, state, service, name)
	*state.Content() = content
	view := View(state)
	var theme *ui.Theme
	tester := ui.NewTester(func(c *ui.Context) {
		theme = c.Theme()
		view(c)
	}, 900, 620)
	tester.SetLocale("en")
	tester.Frame()
	if theme == nil {
		t.Fatal("the view did not render a frame")
	}
	return tester, state, theme
}

// editorInterior returns the text area's box inside its border, padding, and
// focus ring, so a pixel check sees the text and not the chrome.
func editorInterior(t *testing.T, tester *ui.Tester) ui.Rect {
	t.Helper()
	rect, ok := tester.Find("editor-content")
	if !ok {
		t.Fatal("the editor's text area is not in the frame")
	}
	inset := float32(14)
	rect.X += inset
	rect.Y += inset
	rect.W -= 2 * inset
	rect.H -= 2 * inset
	if rect.W <= 0 || rect.H <= 0 {
		t.Fatalf("the editor's text area is too small to inspect: %+v", rect)
	}
	return rect
}

func paintsColor(img *image.RGBA, rect ui.Rect, color ui.Color) bool {
	x0, y0 := max(0, int(rect.X)), max(0, int(rect.Y))
	x1, y1 := min(img.Rect.Dx(), int(rect.X+rect.W)), min(img.Rect.Dy(), int(rect.Y+rect.H))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			offset := img.PixOffset(x, y)
			if img.Pix[offset] == color.R && img.Pix[offset+1] == color.G &&
				img.Pix[offset+2] == color.B && img.Pix[offset+3] == color.A {
				return true
			}
		}
	}
	return false
}

func highlightColors(t *ui.Theme) map[string]ui.Color {
	return map[string]ui.Color{
		"comment":   t.TextMuted,
		"string":    uitheme.SuccessText(t),
		"directive": uitheme.AccentBodyText(t),
	}
}

// TestNginxDocumentPaintsHighlightedRuns is the acceptance: an Nginx name
// colors comments, strings, and directives in the frame, while the same text
// under a non-matching name stays plain.
func TestNginxDocumentPaintsHighlightedRuns(t *testing.T) {
	tester, _, theme := renderEditor(t, "nginx.conf", nginxConfig)
	img := tester.Image()
	rect := editorInterior(t, tester)
	for name, color := range highlightColors(theme) {
		if !paintsColor(img, rect, color) {
			t.Errorf("the Nginx document paints no %s color (%+v)", name, color)
		}
	}

	plainTester, _, plainTheme := renderEditor(t, "notes.txt", nginxConfig)
	plainImage := plainTester.Image()
	plainRect := editorInterior(t, plainTester)
	for name, color := range highlightColors(plainTheme) {
		if paintsColor(plainImage, plainRect, color) {
			t.Errorf("the plain document paints the %s color (%+v)", name, color)
		}
	}
}

// TestHighlightingSwitchLeavesTheTextPlain turns the switch off and checks
// that every syntax color disappears from the same document.
func TestHighlightingSwitchLeavesTheTextPlain(t *testing.T) {
	tester, state, theme := renderEditor(t, "nginx.conf", nginxConfig)
	if !state.Highlighting() {
		t.Fatal("highlighting starts off")
	}
	if err := tester.Click("Highlight"); err != nil {
		t.Fatalf("the highlighting switch is not clickable: %v", err)
	}
	tester.Frame()
	if state.Highlighting() {
		t.Fatal("clicking the switch left highlighting on")
	}
	img := tester.Image()
	rect := editorInterior(t, tester)
	for name, color := range highlightColors(theme) {
		if paintsColor(img, rect, color) {
			t.Errorf("highlighting off still paints the %s color (%+v)", name, color)
		}
	}
}

// TestHighlightingKeepsTheCaretAndEditsWorking checks the "cursor does not
// jump" bullet: a frame with colors leaves the caret in place, and typing
// moves it forward in the same buffer.
func TestHighlightingKeepsTheCaretAndEditsWorking(t *testing.T) {
	tester, state, _ := renderEditor(t, "nginx.conf", nginxConfig)
	before, active := tester.TextCaret()
	if !active {
		t.Fatal("the editor's text area has no caret")
	}
	tester.Frame()
	settled, active := tester.TextCaret()
	if !active {
		t.Fatal("the editor's text area lost input focus")
	}
	if settled != before {
		t.Fatalf("a highlighted frame moved the caret from %+v to %+v", before, settled)
	}

	tester.Type("x")
	tester.Frame()
	if got := *state.Content(); !strings.HasSuffix(got, "x") {
		t.Fatalf("the typed text did not reach the buffer: %q", got)
	}
	after, _ := tester.TextCaret()
	if after.X <= settled.X {
		t.Fatalf("typing moved the caret from %.1f to %.1f, want forward", settled.X, after.X)
	}
}
