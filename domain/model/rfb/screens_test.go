package rfb

import "testing"

func TestScreenRowLayoutLaysScreensLeftToRight(t *testing.T) {
	width, height, screens, err := ScreenRowLayout(2, 640, 480)
	if err != nil {
		t.Fatalf("ScreenRowLayout: %v", err)
	}
	if width != 1280 || height != 480 {
		t.Fatalf("desktop = %dx%d, want 1280x480", width, height)
	}
	if len(screens) != 2 {
		t.Fatalf("screens = %+v, want 2", screens)
	}
	if screens[0] != (Screen{ID: 1, Width: 640, Height: 480}) {
		t.Fatalf("screen 0 = %+v", screens[0])
	}
	if screens[1] != (Screen{ID: 2, X: 640, Width: 640, Height: 480}) {
		t.Fatalf("screen 1 = %+v", screens[1])
	}
}

func TestScreenRowLayoutRejectsImpossibleLayouts(t *testing.T) {
	cases := []struct {
		name          string
		count         int
		width, height uint16
	}{
		{"no screens", 0, 640, 480},
		{"too many screens", MaxScreens + 1, 10, 10},
		{"zero width", 2, 0, 480},
		{"zero height", 2, 640, 0},
		{"union overflows the wire", 2, 40000, 480},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := ScreenRowLayout(tc.count, tc.width, tc.height); err == nil {
				t.Fatal("ScreenRowLayout accepted the layout")
			}
		})
	}
}
