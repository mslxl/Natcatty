package theme

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestCatalogsMatchTheOriginalCounts(t *testing.T) {
	if got := len(LightPresets()); got != 62 {
		t.Errorf("light presets = %d, want 62", got)
	}
	if got := len(DarkPresets()); got != 62 {
		t.Errorf("dark presets = %d, want 62", got)
	}
	if got := len(TerminalThemes()); got != 198 {
		t.Errorf("terminal themes = %d, want 198", got)
	}
	// The original hides exactly fourteen UI-matched schemes.
	if got := len(VisibleTerminalThemes()); got != 184 {
		t.Errorf("visible terminal themes = %d, want 184", got)
	}
	if got := len(Accents()); got != 18 {
		t.Errorf("accents = %d, want 18", got)
	}
	if got := len(IconVariants()); got != 12 {
		t.Errorf("icon variants = %d, want 12", got)
	}
}

func TestCatalogIDsAreUniqueAndEveryTokenParses(t *testing.T) {
	for _, group := range []struct {
		name    string
		presets []Preset
	}{{"light", LightPresets()}, {"dark", DarkPresets()}} {
		seen := make(map[string]bool, len(group.presets))
		for _, preset := range group.presets {
			if preset.ID == "" || preset.Name == "" {
				t.Errorf("%s preset %q has an empty ID or name", group.name, preset.ID)
			}
			if seen[preset.ID] {
				t.Errorf("duplicate %s preset ID %q", group.name, preset.ID)
			}
			seen[preset.ID] = true
			for field, value := range map[string]string{
				"background": preset.Tokens.Background, "foreground": preset.Tokens.Foreground,
				"card": preset.Tokens.Card, "cardForeground": preset.Tokens.CardForeground,
				"popover": preset.Tokens.Popover, "popoverForeground": preset.Tokens.PopoverForeground,
				"primary": preset.Tokens.Primary, "primaryForeground": preset.Tokens.PrimaryForeground,
				"secondary": preset.Tokens.Secondary, "secondaryForeground": preset.Tokens.SecondaryForeground,
				"muted": preset.Tokens.Muted, "mutedForeground": preset.Tokens.MutedForeground,
				"accent": preset.Tokens.Accent, "accentForeground": preset.Tokens.AccentForeground,
				"destructive":           preset.Tokens.Destructive,
				"destructiveForeground": preset.Tokens.DestructiveForeground,
				"border":                preset.Tokens.Border, "input": preset.Tokens.Input, "ring": preset.Tokens.Ring,
			} {
				if !ValidHSL(value) {
					t.Errorf("%s/%s token %s = %q is not HSL", group.name, preset.ID, field, value)
				}
			}
		}
	}
	seen := make(map[string]bool, len(TerminalThemes()))
	for _, preset := range TerminalThemes() {
		if seen[preset.ID] {
			t.Errorf("duplicate terminal theme ID %q", preset.ID)
		}
		seen[preset.ID] = true
		if err := ValidateTerminalColors(preset.Colors); err != nil {
			t.Errorf("%s: %v", preset.ID, err)
		}
	}
}

func TestDefaultsAndFallbacks(t *testing.T) {
	if got := ResolvePreset(false, "", "").ID; got != DefaultLightTheme {
		t.Errorf("light fallback = %q", got)
	}
	if got := ResolvePreset(true, "", "").ID; got != DefaultDarkTheme {
		t.Errorf("dark fallback = %q", got)
	}
	if got := ResolvePreset(false, "midnight", "snow").ID; got != DefaultLightTheme {
		t.Errorf("resolved a dark preset in light mode: %q", got)
	}
	if got := ResolvePreset(true, "snow", "snow").ID; got != DefaultDarkTheme {
		t.Errorf("resolved a light preset in dark mode: %q", got)
	}
	if preset, ok := LookupPreset(true, "midnight"); !ok || preset.Name != "Midnight" {
		t.Errorf("midnight = %+v, %v", preset, ok)
	}
	if _, ok := LookupTerminalTheme("netcatty-dark"); !ok {
		t.Error("netcatty-dark missing")
	}
	if _, ok := LookupTerminalTheme("ui-snow"); !ok {
		t.Error("hidden UI match is no longer findable by ID")
	}
	if got := ResolveIconVariant("nope").ID; got != DefaultIconVariant {
		t.Errorf("icon fallback = %q", got)
	}
}

func TestParseHSLMatchingTheOriginalValidator(t *testing.T) {
	valid := map[string]HSL{
		"221.2 83.2% 53.3%": {H: 221.2, S: 0.832, L: 0.533},
		"0 0% 100%":         {H: 0, S: 0, L: 1},
		" 199 89% 48% ":     {H: 199, S: 0.89, L: 0.48},
	}
	for input, want := range valid {
		got, ok := ParseHSL(input)
		if !ok {
			t.Fatalf("%q rejected", input)
		}
		if math.Abs(got.H-want.H) > 0.001 || math.Abs(got.S-want.S) > 0.001 ||
			math.Abs(got.L-want.L) > 0.001 {
			t.Errorf("%q = %+v, want %+v", input, got, want)
		}
	}
	for _, input := range []string{"", "199 89 48", "199% 89% 48%", "-1 50% 50%",
		"199 101% 50%", "199 50% 101%", "199 50% 50% extra"} {
		if _, ok := ParseHSL(input); ok {
			t.Errorf("%q accepted", input)
		}
	}
}

func TestHSLToHexAndReadableForeground(t *testing.T) {
	cases := map[string]string{
		"0 0% 0%":           "#000000",
		"0 0% 100%":         "#ffffff",
		"0 100% 50%":        "#ff0000",
		"120 100% 50%":      "#00ff00",
		"240 100% 50%":      "#0000ff",
		"221.2 83.2% 53.3%": "#2563eb",
	}
	for token, want := range cases {
		hsl, ok := ParseHSL(token)
		if !ok {
			t.Fatalf("%q rejected", token)
		}
		if got := hsl.Hex(); got != want {
			t.Errorf("%q -> %s, want %s", token, got, want)
		}
	}
	if got := ReadableForeground("0 0% 100%", "0 0% 0%"); got != "0 0% 0%" {
		t.Errorf("white background foreground = %q", got)
	}
	if got := ReadableForeground("0 0% 0%", "0 0% 100%"); got != "0 0% 100%" {
		t.Errorf("black background foreground = %q", got)
	}
	if got := ReadableForeground("bad", "fallback"); got != "fallback" {
		t.Errorf("fallback = %q", got)
	}
	if got := ReadableForeground("0 100% 50%", ""); got != "0 0% 0%" {
		t.Errorf("red background picked = %q", got)
	}
}

func TestReadableForegroundMatchesTheOriginalContrastRule(t *testing.T) {
	// The original compared WCAG contrast ratios against black and white and
	// preferred white on ties.
	for _, token := range []string{"221.2 83.2% 53.3%", "0 0% 50%", "38 92% 50%",
		"142.1 76.2% 36.3%", "0 0% 100%", "0 0% 0%"} {
		hsl, _ := ParseHSL(token)
		luminance := hsl.RelativeLuminance()
		want := "0 0% 100%"
		if ContrastRatio(0, luminance) > ContrastRatio(1, luminance) {
			want = "0 0% 0%"
		}
		if got := ReadableForeground(token, ""); got != want {
			t.Errorf("%q -> %q, want %q", token, got, want)
		}
	}
}

func TestValidHexAndLuminance(t *testing.T) {
	for _, value := range []string{"#000000", "#ffffff", "#AbCdEf"} {
		if !ValidHex(value) {
			t.Errorf("%q rejected", value)
		}
	}
	for _, value := range []string{"", "#fff", "#gggggg", "000000", "#0000000"} {
		if ValidHex(value) {
			t.Errorf("%q accepted", value)
		}
	}
	if luminance, ok := HexLuminance("#ffffff"); !ok || math.Abs(luminance-1) > 1e-9 {
		t.Errorf("white luminance = %v, %v", luminance, ok)
	}
	if luminance, _ := HexLuminance("#000000"); luminance != 0 {
		t.Errorf("black luminance = %v", luminance)
	}
}

const sampleITerm = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Ansi 0 Color</key>
	<dict>
		<key>Blue Component</key>
		<real>0.0</real>
		<key>Green Component</key>
		<real>0.0</real>
		<key>Red Component</key>
		<real>0.0</real>
	</dict>
	<key>Ansi 1 Color</key>
	<dict>
		<key>Blue Component</key>
		<real>0.1</real>
		<key>Green Component</key>
		<real>0.2</real>
		<key>Red Component</key>
		<real>0.9</real>
	</dict>
	<key>Background Color</key>
	<dict>
		<key>Alpha Component</key>
		<real>1</real>
		<key>Blue Component</key>
		<real>0.2</real>
		<key>Green Component</key>
		<real>0.1</real>
		<key>Red Component</key>
		<real>0.05</real>
	</dict>
	<key>Foreground Color</key>
	<dict>
		<key>Blue Component</key>
		<real>255</real>
		<key>Green Component</key>
		<real>255</real>
		<key>Red Component</key>
		<integer>255</integer>
	</dict>
	<key>Selection Color</key>
	<dict>
		<key>Blue Component</key>
		<real>0.5</real>
		<key>Green Component</key>
		<real>0.25</real>
		<key>Red Component</key>
		<real>0.75</real>
	</dict>
</dict>
</plist>`

func TestParseITermcolorsDefaultsAndIntegers(t *testing.T) {
	preset, err := ParseITermcolors([]byte(sampleITerm), "Sample")
	if err != nil {
		t.Fatal(err)
	}
	if preset.Name != "Sample" || !preset.Custom || !preset.Dark {
		t.Errorf("header = %+v", preset)
	}
	if preset.Colors.Background != "#0d1a33" {
		t.Errorf("background = %q", preset.Colors.Background)
	}
	// The original divided <integer> components by 255.
	if preset.Colors.Foreground != "#ffffff" {
		t.Errorf("foreground = %q", preset.Colors.Foreground)
	}
	if preset.Colors.Red != "#e6331a" {
		t.Errorf("ansi red = %q", preset.Colors.Red)
	}
	if preset.Colors.Cursor != preset.Colors.Foreground {
		t.Errorf("cursor = %q, want the foreground", preset.Colors.Cursor)
	}
	if preset.Colors.Selection != "#bf4080" {
		t.Errorf("selection = %q", preset.Colors.Selection)
	}
	// Missing ANSI colors use the original defaults.
	if preset.Colors.Green != "#00cc00" || preset.Colors.BrightWhite != "#ffffff" {
		t.Errorf("defaults not filled: %+v", preset.Colors)
	}
}

func TestParseITermcolorsSelectionDefaultFollowsBackground(t *testing.T) {
	dark := `<plist><dict><key>Background Color</key><dict>
		<key>Red Component</key><real>0.1</real>
		<key>Green Component</key><real>0.1</real>
		<key>Blue Component</key><real>0.1</real></dict>
		<key>Foreground Color</key><dict>
		<key>Red Component</key><real>0.9</real>
		<key>Green Component</key><real>0.9</real>
		<key>Blue Component</key><real>0.9</real></dict></dict></plist>`
	preset, err := ParseITermcolors([]byte(dark), "")
	if err != nil {
		t.Fatal(err)
	}
	if preset.Colors.Selection != "#264f78" || !preset.Dark {
		t.Errorf("dark defaults = %+v", preset)
	}
	if preset.Name != "Imported Theme" {
		t.Errorf("empty name = %q", preset.Name)
	}

	light := strings.ReplaceAll(dark, "0.1</real>", "0.9</real>")
	preset, err = ParseITermcolors([]byte(light), "Light")
	if err != nil {
		t.Fatal(err)
	}
	if preset.Colors.Selection != "#add6ff" || preset.Dark {
		t.Errorf("light defaults = %+v", preset)
	}
}

func TestParseITermcolorsRejectsInvalidFiles(t *testing.T) {
	cases := map[string]string{
		"empty":    "  ",
		"not xml":  "hello",
		"no plist": `<dict><key>Background Color</key><dict><key>Red Component</key><real>0</real></dict></dict>`,
		"no dict":  `<plist></plist>`,
		"missing background": `<plist><dict><key>Foreground Color</key><dict>
			<key>Red Component</key><real>1</real><key>Green Component</key><real>1</real>
			<key>Blue Component</key><real>1</real></dict></dict></plist>`,
		"incomplete color": `<plist><dict><key>Background Color</key><dict>
			<key>Red Component</key><real>1</real></dict>
			<key>Foreground Color</key><dict>
			<key>Red Component</key><real>1</real><key>Green Component</key><real>1</real>
			<key>Blue Component</key><real>1</real></dict></dict></plist>`,
	}
	for name, input := range cases {
		if _, err := ParseITermcolors([]byte(input), "x"); !errors.Is(err, ErrInvalidITermcolors) {
			t.Errorf("%s: err = %v, want ErrInvalidITermcolors", name, err)
		}
	}
}

func TestValidateTerminalColorsRejectsMalformedFields(t *testing.T) {
	preset, _ := LookupTerminalTheme(DefaultTerminalTheme)
	preset.Colors.Cyan = "teal"
	if err := ValidateTerminalColors(preset.Colors); err == nil {
		t.Error("malformed cyan accepted")
	}
}
