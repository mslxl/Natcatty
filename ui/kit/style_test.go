package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestStyleForFallsBackToClassic(t *testing.T) {
	for id, want := range map[string]string{"macos": StyleMacOS, "classic": StyleClassic, "": StyleClassic, "aqua": StyleClassic} {
		if got := StyleFor(id).ID(); got != want {
			t.Errorf("StyleFor(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSetStyleChangesTheActiveStyleAndNilRestoresClassic(t *testing.T) {
	SetStyle(MacOS)
	if CurrentStyle().ID() != StyleMacOS {
		t.Fatalf("current = %q after SetStyle(MacOS)", CurrentStyle().ID())
	}
	SetStyle(nil)
	if CurrentStyle().ID() != StyleClassic {
		t.Fatalf("current = %q after SetStyle(nil)", CurrentStyle().ID())
	}
}

func TestMacOSMetricsUseTheMacOSScaleWithCapsuleButtons(t *testing.T) {
	m := MacOS.Metrics()
	if m.Font.Body != 13 || m.Font.Footnote != 11 || m.Font.Caption != 10 {
		t.Fatalf("macOS type scale = %+v, want body 13, footnote 11, caption 10", m.Font)
	}
	if m.Radius.Button != m.Radius.Pill {
		t.Fatalf("macOS button radius = %v, want a capsule", m.Radius.Button)
	}
	if m.Radius.Card < 14 || m.Radius.Card > 16 {
		t.Fatalf("macOS card radius = %v, want 14-16", m.Radius.Card)
	}
	if c := Classic.Metrics(); c.Radius.Button != c.Radius.Control {
		t.Fatalf("Classic button radius = %v, want the control radius %v", c.Radius.Button, c.Radius.Control)
	}
}

func TestClassicKeepsThePresetThemeAndMacOSFadesTheBorders(t *testing.T) {
	for _, dark := range []bool{false, true} {
		base := &ui.Theme{Dark: dark, Background: ui.RGB(250, 250, 250), Surface: ui.RGB(255, 255, 255),
			Border: ui.RGB(200, 200, 200), Text: ui.RGB(10, 10, 10), FontSize: 14, Radius: 6}
		if dark {
			base.Background, base.Surface, base.Border, base.Text = ui.RGB(20, 20, 20), ui.RGB(30, 30, 30), ui.RGB(70, 70, 70), ui.RGB(240, 240, 240)
		}
		if got := Classic.Theme(base); *got != *base {
			t.Fatalf("Classic changed the theme: %+v", got)
		}
		got := MacOS.Theme(base)
		if got == base {
			t.Fatal("MacOS changed the preset's theme in place")
		}
		if got.Background != base.Background || got.Text != base.Text {
			t.Fatalf("MacOS changed the preset colors: %+v", got)
		}
		if got.Border == base.Border || contrastTo(got.Border, base.Background) >= contrastTo(base.Border, base.Background) {
			t.Fatalf("dark=%v: macOS border %v is not fainter than %v", dark, got.Border, base.Border)
		}
		if got.FontSize != 13 {
			t.Fatalf("macOS body size = %v, want 13", got.FontSize)
		}
	}
}

// contrastTo is how far a is from b in summed channel distance.
func contrastTo(a, b ui.Color) int {
	d := func(x, y uint8) int { return max(int(x)-int(y), int(y)-int(x)) }
	return d(a.R, b.R) + d(a.G, b.G) + d(a.B, b.B)
}

func TestFillClearsTheWindowOverItsVibrancyOrPaintsItsBackground(t *testing.T) {
	SetStyle(MacOS)
	t.Cleanup(func() { SetStyle(nil) })
	under := ui.RGB(0, 255, 0)
	theme := &ui.Theme{Background: ui.RGB(255, 0, 0), Surface: ui.RGB(0, 0, 255), Text: ui.RGB(0, 0, 0)}
	for _, vibrant := range []bool{true, false} {
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(theme)
			c.Root().Background(under)
			Fill(c, ui.Column(c).Size(40, 40), SurfaceWindow)
		}, 80, 80)
		tt.SetVibrancy(vibrant)
		tt.Frame()
		got := tt.Image().RGBAAt(20, 20)
		want := theme.Background
		if vibrant {
			// Clear: what is under it shows.
			want = under
		}
		if got.R != want.R || got.G != want.G || got.B != want.B {
			t.Fatalf("vibrancy=%v: window pixel = %v, want %v", vibrant, got, want)
		}
	}
}

func TestHighContrastFillsTheMacOSGlassAndVibrancyOpaque(t *testing.T) {
	SetStyle(MacOS)
	t.Cleanup(func() { SetStyle(nil) })
	theme := &ui.Theme{Background: ui.RGB(255, 0, 0), Surface: ui.RGB(0, 0, 255), Text: ui.RGB(0, 0, 0)}
	for _, tc := range []struct {
		role SurfaceRole
		want ui.Color
	}{
		{SurfaceWindow, theme.Background},
		{SurfaceSidebar, theme.Surface},
		{SurfaceOverlay, theme.Surface},
	} {
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(theme)
			c.Root().Background(ui.RGB(0, 255, 0))
			Fill(c, ui.Column(c).Size(40, 40), tc.role)
		}, 80, 80)
		tt.SetVibrancy(true)
		tt.SetPreferences(ui.Preferences{HighContrast: true})
		tt.Frame()
		got := tt.Image().RGBAAt(20, 20)
		if got.R != tc.want.R || got.G != tc.want.G || got.B != tc.want.B || got.A != 255 {
			t.Fatalf("role %d: pixel = %v, want the opaque %v", tc.role, got, tc.want)
		}
	}
}

func TestHighContrastKeepsThePresetBorders(t *testing.T) {
	base := &ui.Theme{Background: ui.RGB(250, 250, 250), Border: ui.RGB(200, 200, 200), FontSize: 14}
	var got *ui.Theme
	tt := ui.NewTester(func(c *ui.Context) { got = StyleTheme(c, MacOS, base) }, 10, 10)
	tt.SetPreferences(ui.Preferences{HighContrast: true})
	tt.Frame()
	if got.Border != base.Border || got.FontSize != 13 {
		t.Fatalf("high contrast macOS theme: border %v, size %v; want the preset's border and 13", got.Border, got.FontSize)
	}
}
