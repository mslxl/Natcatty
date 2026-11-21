package vnc

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model/rfb"
)

// TestResizeScreensSinglePreservesIDAndFlags proves a single remembered screen
// keeps its identity and flags and is simply set to the new desktop size.
func TestResizeScreensSinglePreservesIDAndFlags(t *testing.T) {
	screens := []rfb.Screen{{
		ID: 7, X: 0, Y: 0, Width: 100, Height: 80, Flags: 5,
	}}
	got := resizeScreens(screens, 100, 80, 160, 120)
	want := []rfb.Screen{{ID: 7, X: 0, Y: 0, Width: 160, Height: 120, Flags: 5}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("resizeScreens = %+v, want %+v", got, want)
	}
}

// TestResizeScreensTwoScreensScaleProportionally proves a multi-screen layout
// is scaled to the new desktop while preserving each screen's ID and flags.
func TestResizeScreensTwoScreensScaleProportionally(t *testing.T) {
	screens := []rfb.Screen{
		{ID: 1, X: 0, Y: 0, Width: 100, Height: 100, Flags: 1},
		{ID: 2, X: 100, Y: 0, Width: 100, Height: 100, Flags: 2},
	}
	got := resizeScreens(screens, 200, 100, 100, 50)
	if len(got) != 2 {
		t.Fatalf("resizeScreens returned %d screens, want 2", len(got))
	}
	if got[0].ID != 1 || got[0].Flags != 1 || got[0].X != 0 || got[0].Y != 0 ||
		got[0].Width != 50 || got[0].Height != 50 {
		t.Fatalf("first screen = %+v, want ID 1 at 0,0 50x50 flags 1", got[0])
	}
	if got[1].ID != 2 || got[1].Flags != 2 || got[1].X != 50 || got[1].Y != 0 ||
		got[1].Width != 50 || got[1].Height != 50 {
		t.Fatalf("second screen = %+v, want ID 2 at 50,0 50x50 flags 2", got[1])
	}
}

// TestResizeScreensSingleNonZeroOriginIsClamped proves a single remembered
// screen at a nonzero origin is clamped so its geometry fits the requested
// desktop instead of extending past it.
func TestResizeScreensSingleNonZeroOriginIsClamped(t *testing.T) {
	screens := []rfb.Screen{{
		ID: 3, X: 5, Y: 5, Width: 200, Height: 200, Flags: 1,
	}}
	got := resizeScreens(screens, 200, 200, 80, 60)
	if len(got) != 1 {
		t.Fatalf("resizeScreens returned %d screens, want 1", len(got))
	}
	screen := got[0]
	if screen.ID != 3 || screen.Flags != 1 {
		t.Fatalf("screen identity changed: %+v", screen)
	}
	if screen.Width == 0 || screen.Height == 0 {
		t.Fatalf("screen %+v has a zero dimension", screen)
	}
	if int(screen.X)+int(screen.Width) > 80 || int(screen.Y)+int(screen.Height) > 60 {
		t.Fatalf("screen %+v does not fit the 80x60 desktop", screen)
	}
}

// TestResizeScreensMultiScreenUnionClamped proves a remembered multi-screen
// layout whose union differs from the remembered desktop is still clamped to
// the requested desktop.
func TestResizeScreensMultiScreenUnionClamped(t *testing.T) {
	screens := []rfb.Screen{
		{ID: 1, X: 0, Y: 0, Width: 100, Height: 100, Flags: 1},
		{ID: 2, X: 150, Y: 50, Width: 100, Height: 100, Flags: 2},
	}
	got := resizeScreens(screens, 200, 100, 80, 60)
	if len(got) != 2 {
		t.Fatalf("resizeScreens returned %d screens, want 2", len(got))
	}
	for _, screen := range got {
		if screen.Width == 0 || screen.Height == 0 {
			t.Fatalf("screen %+v has a zero dimension", screen)
		}
		if int(screen.X)+int(screen.Width) > 80 || int(screen.Y)+int(screen.Height) > 60 {
			t.Fatalf("screen %+v does not fit the 80x60 desktop", screen)
		}
	}
}

// TestResizeScreensNeverEmitsZeroSize proves a layout squeezed into a tiny
// desktop clamps instead of producing an unrepresentable zero-size screen.
func TestResizeScreensNeverEmitsZeroSize(t *testing.T) {
	screens := []rfb.Screen{
		{ID: 1, Width: 100, Height: 100, Flags: 1},
		{ID: 2, X: 100, Width: 100, Height: 100, Flags: 2},
	}
	got := resizeScreens(screens, 200, 100, 2, 2)
	for _, screen := range got {
		if screen.Width == 0 || screen.Height == 0 {
			t.Fatalf("screen %+v has a zero dimension", screen)
		}
		if int(screen.X)+int(screen.Width) > 2 || int(screen.Y)+int(screen.Height) > 2 {
			t.Fatalf("screen %+v does not fit the 2x2 desktop", screen)
		}
	}
}
