package vnc

import (
	"image"
	"image/color"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// TestApplyRenameTrimsAndRejectsEmpty pins the VNC rename dialog's hand-off:
// the Pane's Session gets the trimmed title, and an empty title is rejected so
// the Tab strip cannot go blank.
func TestApplyRenameTrimsAndRejectsEmpty(t *testing.T) {
	var renamed []string
	surface := &Surface{paneActions: sessionui.PaneActions{
		Rename: func(title string) { renamed = append(renamed, title) },
	}}
	surface.renameValue = "  lab desk  "
	if !surface.applyRename() {
		t.Fatal("applyRename rejected a non-empty title")
	}
	if len(renamed) != 1 || renamed[0] != "lab desk" {
		t.Fatalf("renamed = %v, want one trimmed title", renamed)
	}
	surface.renameValue = "   "
	if surface.applyRename() {
		t.Fatal("applyRename accepted a blank title")
	}
	if len(renamed) != 1 {
		t.Fatalf("blank title reached Rename: %v", renamed)
	}
}

// TestPendingFrameRebuildsABitmapDroppedByHibernate covers the frame that can
// arrive while the Pane shows its hibernation placeholder. Hibernate keeps the
// last frame's pixels but frees the drawn bitmap; applying that pending frame
// must recreate the bitmap instead of updating nil.
func TestPendingFrameRebuildsABitmapDroppedByHibernate(t *testing.T) {
	size := image.Rect(0, 0, 2, 2)
	frame := image.NewRGBA(size)
	green := color.RGBA{R: 20, G: 220, B: 20, A: 255}
	for i := 0; i < len(frame.Pix); i += 4 {
		frame.Pix[i] = green.R
		frame.Pix[i+1] = green.G
		frame.Pix[i+2] = green.B
		frame.Pix[i+3] = green.A
	}
	surface := &Surface{
		pixels:  image.NewRGBA(size),
		pending: &appsession.DisplayFrame{Image: frame},
	}

	surface.applyPendingFrame()

	if surface.bitmap == nil {
		t.Fatal("the pending frame did not rebuild the bitmap")
	}
	if got := surface.pixels.RGBAAt(1, 1); got != green {
		t.Fatalf("pixel = %v, want the pending frame's color", got)
	}
}
