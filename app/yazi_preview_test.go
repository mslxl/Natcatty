//go:build darwin || linux

package app

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// A full-screen program that previews a picture with the Kitty graphics
// protocol must show it. yazi transmits the image, places it through Unicode
// placeholder cells, and owns the alternate screen, which the pane draws with
// the terminal's own view. The pane's coarse grid used to size the terminal
// alongside that view, a cell apart: the program was resized twice a frame,
// repainted forever, and its placeholders were wiped before a frame showed
// them, so the preview stayed empty (and once the payload itself was drawn as
// text).
func TestYaziPreviewShowsItsPicture(t *testing.T) {
	if _, err := exec.LookPath("yazi"); err != nil {
		t.Skip("yazi is not installed here")
	}
	dir := t.TempDir()
	writePreviewPicture(t, filepath.Join(dir, "red.png"))

	service := appsession.NewService(localpty.Factory{}, appsession.WithHighlightPolicy(
		func() (bool, []domainterminal.KeywordHighlightRule) {
			// The shipped defaults, on: the keyword highlighter must leave the
			// picture's base64 alone as it crosses the Session's write chunks.
			return true, domainterminal.DefaultKeywordHighlightRules()
		},
	))
	t.Cleanup(service.CloseAll)
	session, err := service.Open(t.Context(), appsession.StartSpec{
		Kind: appsession.KindLocal,
		Dir:  dir,
		Cols: 100,
		Rows: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	surface, err := sessionui.New(session, sessionui.WithRedraw(func() {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)

	tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 620)
	tester.SetLocale("en")
	tester.Frame()
	time.Sleep(300 * time.Millisecond)
	tester.ClickAt(450, 560)
	tester.Type("yazi")
	tester.Key(0, ui.KeyEnter)
	pumpTUIFrames(tester, 4*time.Second)

	// The picture is drawn into the preview pane: a scaled 640×480 red fill
	// covers far more than the few pixels a misplaced or unscaled copy would.
	if got := redPixels(tester.Image()); got < 10_000 {
		t.Fatalf("the preview pane shows %d red pixels, want the picture", got)
	}
	if screen := surface.ScreenText(); strings.Contains(screen, "\x1b_G") ||
		strings.Contains(screen, ";AAAA") || strings.Contains(screen, "/gAA") {
		t.Errorf("the graphics payload was drawn as text: %.200q", screen)
	}

	// A full-screen program paints the whole screen, its background included,
	// and the pane must give it the whole area: the flow's rounded padding used
	// to frame yazi's black screen with the pane's own colour. The screen's
	// background is whatever colour yazi paints most of its middle with, so the
	// check does not depend on the program's theme.
	if img := tester.Image(); img != nil {
		b := img.Bounds()
		counts := map[color.RGBA]int{}
		for y := b.Min.Y + b.Dy()/3; y < b.Max.Y-b.Dy()/3; y++ {
			for x := b.Min.X + b.Dx()/4; x < b.Max.X-b.Dx()/4; x++ {
				counts[img.RGBAAt(x, y)]++
			}
		}
		var bg color.RGBA
		best := -1
		for c, n := range counts {
			if n > best {
				bg, best = c, n
			}
		}
		mid := b.Min.Y + b.Dy()/2
		// The app's own chrome frames the pane by about 16 px on each side.
		for _, x := range []int{b.Min.X + 18, b.Max.X - 18} {
			if got := img.RGBAAt(x, mid); got != bg {
				t.Fatalf("pixel (%d,%d) is %v, not the program's background %v: the pane insets the program",
					x, mid, got, bg)
			}
		}
	}

	// The program keeps the keyboard while its picture is drawn by the
	// terminal's own view: q quits yazi and the shell that follows takes the
	// next command. A pane that swapped the element owning the keyboard and
	// never gave it back answered the mouse alone.
	tester.Type("q")
	pumpTUIFrames(tester, time.Second)
	tester.Type("echo SENT\n")
	pumpTUIFrames(tester, 3*time.Second)
	if text := session.ScreenText().Text; !strings.Contains(text, "SENT") {
		t.Fatalf("the keyboard never reached the program; screen %.300q", text)
	}
}

// writePreviewPicture writes a red 640×480 PNG, the picture yazi previews.
func writePreviewPicture(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
