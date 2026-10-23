// Package theme owns the built-in appearance catalogs and the color rules the
// original Electron application used: UI theme presets, terminal color
// schemes, accent presets, app-icon variants, and HSL conversion.
//
// It is pure domain data and arithmetic. MyGo colors and widgets are built in
// ui/theme from these values.
package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Tokens are the nineteen shadcn-style HSL tokens of one UI theme preset.
// Every value is an HSL triplet such as "221.2 83.2% 53.3%".
type Tokens struct {
	Background            string
	Foreground            string
	Card                  string
	CardForeground        string
	Popover               string
	PopoverForeground     string
	Primary               string
	PrimaryForeground     string
	Secondary             string
	SecondaryForeground   string
	Muted                 string
	MutedForeground       string
	Accent                string
	AccentForeground      string
	Destructive           string
	DestructiveForeground string
	Border                string
	Input                 string
	Ring                  string
}

// Preset is one built-in UI theme. ID is persisted, Name is untranslated
// because the original shipped these proper names as data.
type Preset struct {
	ID         string
	Name       string
	Collection string
	Tokens     Tokens
}

// TerminalColors are the twenty colors of a terminal color scheme.
type TerminalColors struct {
	Background    string `json:"background"`
	Foreground    string `json:"foreground"`
	Cursor        string `json:"cursor"`
	Selection     string `json:"selection"`
	Black         string `json:"black"`
	Red           string `json:"red"`
	Green         string `json:"green"`
	Yellow        string `json:"yellow"`
	Blue          string `json:"blue"`
	Magenta       string `json:"magenta"`
	Cyan          string `json:"cyan"`
	White         string `json:"white"`
	BrightBlack   string `json:"brightBlack"`
	BrightRed     string `json:"brightRed"`
	BrightGreen   string `json:"brightGreen"`
	BrightYellow  string `json:"brightYellow"`
	BrightBlue    string `json:"brightBlue"`
	BrightMagenta string `json:"brightMagenta"`
	BrightCyan    string `json:"brightCyan"`
	BrightWhite   string `json:"brightWhite"`
}

// TerminalTheme is one terminal color scheme. Custom themes are imported from
// iTerm2 .itermcolors files and persisted separately from the built-ins.
type TerminalTheme struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Dark   bool           `json:"dark"`
	Custom bool           `json:"custom"`
	Colors TerminalColors `json:"colors"`
}

// Accent is one of the original's eighteen custom-accent presets.
type Accent struct {
	Name string
	HSL  string
}

// IconGroup groups the twelve app-icon variants the way the original picker
// did.
type IconGroup string

const (
	IconGroupClassic IconGroup = "classic"
	IconGroupWhite   IconGroup = "white"
	IconGroupSpecial IconGroup = "special"
)

// IconVariant is one app-icon choice. ID is persisted; Name is the English
// fallback and the Settings window localizes it.
type IconVariant struct {
	ID    string
	Name  string
	Group IconGroup
}

const (
	// DefaultLightTheme is the original's light preset.
	DefaultLightTheme = "snow"
	// DefaultDarkTheme is the original's dark preset.
	DefaultDarkTheme = "midnight"
	// DefaultTerminalTheme is the original's terminal color scheme.
	DefaultTerminalTheme = "netcatty-dark"
	// DefaultIconVariant is the original app icon.
	DefaultIconVariant = "original"
)

// LightPresets returns the 62 light UI presets in the original picker order.
func LightPresets() []Preset { return append([]Preset(nil), lightPresets...) }

// DarkPresets returns the 62 dark UI presets in the original picker order.
func DarkPresets() []Preset { return append([]Preset(nil), darkPresets...) }

// Presets returns one appearance's presets.
func Presets(dark bool) []Preset {
	if dark {
		return DarkPresets()
	}
	return LightPresets()
}

// LookupPreset finds a preset of one appearance by ID.
func LookupPreset(dark bool, id string) (Preset, bool) {
	for _, preset := range Presets(dark) {
		if preset.ID == id {
			return preset, true
		}
	}
	return Preset{}, false
}

// ResolvePreset returns the requested preset, or the original default when the
// stored ID does not exist in this build.
func ResolvePreset(dark bool, lightID, darkID string) Preset {
	id := lightID
	if dark {
		id = darkID
	}
	if preset, ok := LookupPreset(dark, id); ok {
		return preset
	}
	fallback := DefaultLightTheme
	if dark {
		fallback = DefaultDarkTheme
	}
	preset, _ := LookupPreset(dark, fallback)
	return preset
}

// TerminalThemes returns every built-in terminal theme, including the UI
// matches that the picker hides.
func TerminalThemes() []TerminalTheme {
	return append([]TerminalTheme(nil), builtinTerminalThemes...)
}

// uiMatchTerminalThemeIDs are the original's UI-matched schemes. They exist so
// a terminal can blend with a UI theme, but the picker hides them.
var uiMatchTerminalThemeIDs = map[string]bool{
	"ui-snow": true, "ui-pure-white": true, "ui-ivory": true, "ui-mist": true,
	"ui-mint": true, "ui-sand": true, "ui-lavender": true,
	"ui-pure-black": true, "ui-midnight": true, "ui-deep-blue": true,
	"ui-vscode": true, "ui-graphite": true, "ui-obsidian": true, "ui-forest": true,
}

// IsUIMatchTerminalTheme reports whether id is a hidden UI-matched scheme.
func IsUIMatchTerminalTheme(id string) bool { return uiMatchTerminalThemeIDs[id] }

// VisibleTerminalThemes returns the built-in themes the picker offers, in the
// original order.
func VisibleTerminalThemes() []TerminalTheme {
	out := make([]TerminalTheme, 0, len(builtinTerminalThemes)-len(uiMatchTerminalThemeIDs))
	for _, preset := range builtinTerminalThemes {
		if !uiMatchTerminalThemeIDs[preset.ID] {
			out = append(out, preset)
		}
	}
	return out
}

// LookupTerminalTheme finds a built-in terminal theme by ID.
func LookupTerminalTheme(id string) (TerminalTheme, bool) {
	for _, preset := range builtinTerminalThemes {
		if preset.ID == id {
			return preset, true
		}
	}
	return TerminalTheme{}, false
}

// FindTerminalTheme finds a built-in or custom theme. Built-ins win an ID
// collision, which is also why the store refuses custom IDs that match one.
func FindTerminalTheme(id string, custom []TerminalTheme) (TerminalTheme, bool) {
	if preset, ok := LookupTerminalTheme(id); ok {
		return preset, true
	}
	for _, preset := range custom {
		if preset.ID == id {
			return preset, true
		}
	}
	return TerminalTheme{}, false
}

// LookupIconVariant finds an app-icon variant by ID.
func LookupIconVariant(id string) (IconVariant, bool) {
	for _, variant := range iconVariants {
		if variant.ID == id {
			return variant, true
		}
	}
	return IconVariant{}, false
}

// ValidIconVariant reports whether id names a built-in app icon.
func ValidIconVariant(id string) bool {
	_, ok := LookupIconVariant(id)
	return ok
}

// ResolveIconVariant returns the requested variant or the original icon.
func ResolveIconVariant(id string) IconVariant {
	if variant, ok := LookupIconVariant(id); ok {
		return variant
	}
	variant, _ := LookupIconVariant(DefaultIconVariant)
	return variant
}

// IconVariants returns all twelve variants in picker order.
func IconVariants() []IconVariant { return append([]IconVariant(nil), iconVariants...) }

// IconGroupOrder returns the picker's groups in order.
func IconGroupOrder() []IconGroup {
	return []IconGroup{IconGroupClassic, IconGroupWhite, IconGroupSpecial}
}

// IconVariantsInGroup returns the variants of one group in picker order.
func IconVariantsInGroup(group IconGroup) []IconVariant {
	out := make([]IconVariant, 0, 6)
	for _, variant := range iconVariants {
		if variant.Group == group {
			out = append(out, variant)
		}
	}
	return out
}

var iconVariants = []IconVariant{
	{ID: "original", Name: "Original", Group: IconGroupClassic},
	{ID: "bright", Name: "Bright", Group: IconGroupClassic},
	{ID: "dark", Name: "Dark", Group: IconGroupClassic},
	{ID: "colorful", Name: "Colorful", Group: IconGroupClassic},
	{ID: "high-contrast", Name: "High contrast", Group: IconGroupClassic},
	{ID: "white-navy", Name: "White · Navy cat", Group: IconGroupWhite},
	{ID: "white-sky", Name: "White · Sky cat", Group: IconGroupWhite},
	{ID: "white-rose", Name: "White · Rose cat", Group: IconGroupWhite},
	{ID: "white-emerald", Name: "White · Emerald cat", Group: IconGroupWhite},
	{ID: "white-amber", Name: "White · Amber cat", Group: IconGroupWhite},
	{ID: "white-violet", Name: "White · Violet cat", Group: IconGroupWhite},
	{ID: "rainbow", Name: "Rainbow cat", Group: IconGroupSpecial},
}

// Accents returns the original's eighteen accent presets in picker order.
func Accents() []Accent { return append([]Accent(nil), accents...) }

var accents = []Accent{
	{"Sky", "199 89% 48%"},
	{"Blue", "221.2 83.2% 53.3%"},
	{"Indigo", "234 89% 62%"},
	{"Violet", "262.1 83.3% 57.8%"},
	{"Purple", "271 81% 56%"},
	{"Fuchsia", "292 84% 61%"},
	{"Pink", "330 81% 60%"},
	{"Rose", "346.8 77.2% 49.8%"},
	{"Red", "0 84.2% 60.2%"},
	{"Orange", "24.6 95% 53.1%"},
	{"Amber", "38 92% 50%"},
	{"Yellow", "48 96% 53%"},
	{"Lime", "84 81% 44%"},
	{"Green", "142.1 76.2% 36.3%"},
	{"Emerald", "160 84% 39%"},
	{"Teal", "173 80% 40%"},
	{"Cyan", "189 94% 43%"},
	{"Slate", "215 16% 47%"},
}

// HSL is a color as the original stored theme tokens: hue in degrees,
// saturation and lightness in 0..1.
type HSL struct {
	H float64
	S float64
	L float64
}

// ParseHSL parses the original's "<h> <s>% <l>%" token, accepting decimals and
// surrounding whitespace. It enforces the same shape as the original
// validator: no sign, no unit on the hue, percent on saturation and lightness.
func ParseHSL(value string) (HSL, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 3 {
		return HSL{}, false
	}
	if !strings.HasSuffix(fields[1], "%") || !strings.HasSuffix(fields[2], "%") {
		return HSL{}, false
	}
	h, ok := parseNonNegative(fields[0])
	if !ok {
		return HSL{}, false
	}
	s, ok := parseNonNegative(strings.TrimSuffix(fields[1], "%"))
	if !ok {
		return HSL{}, false
	}
	l, ok := parseNonNegative(strings.TrimSuffix(fields[2], "%"))
	if !ok {
		return HSL{}, false
	}
	if s > 100 || l > 100 {
		return HSL{}, false
	}
	return HSL{H: math.Mod(h, 360), S: s / 100, L: l / 100}, true
}

func parseNonNegative(value string) (float64, bool) {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

// ValidHSL reports whether value is an acceptable HSL token.
func ValidHSL(value string) bool {
	_, ok := ParseHSL(value)
	return ok
}

// RGB converts the color to 8-bit sRGB channels.
func (c HSL) RGB() (uint8, uint8, uint8) {
	h := math.Mod(c.H, 360)
	if h < 0 {
		h += 360
	}
	s := clamp01(c.S)
	l := clamp01(c.L)
	if s == 0 {
		v := uint8(math.Round(l * 255))
		return v, v, v
	}
	chroma := (1 - math.Abs(2*l-1)) * s
	hp := h / 60
	x := chroma * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g, b = chroma, x, 0
	case hp < 2:
		r, g, b = x, chroma, 0
	case hp < 3:
		r, g, b = 0, chroma, x
	case hp < 4:
		r, g, b = 0, x, chroma
	case hp < 5:
		r, g, b = x, 0, chroma
	default:
		r, g, b = chroma, 0, x
	}
	m := l - chroma/2
	return uint8(math.Round((r + m) * 255)), uint8(math.Round((g + m) * 255)), uint8(math.Round((b + m) * 255))
}

// Hex renders the color as "#rrggbb".
func (c HSL) Hex() string {
	r, g, b := c.RGB()
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// String renders a normalized HSL token.
func (c HSL) String() string {
	return fmt.Sprintf("%s %s%% %s%%", formatNumber(c.H), formatNumber(c.S*100), formatNumber(c.L*100))
}

func formatNumber(value float64) string {
	if math.Abs(value-math.Round(value)) < 0.0005 {
		return strconv.Itoa(int(math.Round(value)))
	}
	return strconv.FormatFloat(math.Round(value*10)/10, 'f', -1, 64)
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func toLinear(channel uint8) float64 {
	value := float64(channel) / 255
	if value <= 0.03928 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

// RelativeLuminance returns WCAG relative luminance, as the original's
// accent-foreground rule computed it.
func (c HSL) RelativeLuminance() float64 {
	r, g, b := c.RGB()
	return 0.2126*toLinear(r) + 0.7152*toLinear(g) + 0.0722*toLinear(b)
}

// ContrastRatio returns the WCAG contrast ratio between two luminances.
func ContrastRatio(a, b float64) float64 {
	lighter, darker := math.Max(a, b), math.Min(a, b)
	return (lighter + 0.05) / (darker + 0.05)
}

// ReadableForeground returns white or black, whichever contrasts more with the
// background token. It returns fallback when the background cannot be parsed,
// matching the original's behavior.
func ReadableForeground(backgroundToken, fallback string) string {
	background, ok := ParseHSL(backgroundToken)
	if !ok {
		return fallback
	}
	luminance := background.RelativeLuminance()
	if ContrastRatio(1, luminance) >= ContrastRatio(0, luminance) {
		return "0 0% 100%"
	}
	return "0 0% 0%"
}

// Mix interpolates two colors. weight is how much of b is in the result.
func Mix(a, b HSL, weight float64) HSL {
	weight = clamp01(weight)
	return HSL{
		H: a.H + shortestHueDelta(a.H, b.H)*weight,
		S: a.S + (b.S-a.S)*weight,
		L: a.L + (b.L-a.L)*weight,
	}
}

func shortestHueDelta(from, to float64) float64 {
	delta := math.Mod(to-from, 360)
	if delta > 180 {
		delta -= 360
	}
	if delta < -180 {
		delta += 360
	}
	return delta
}

// WithLightness returns the color with a new lightness.
func (c HSL) WithLightness(lightness float64) HSL {
	c.L = clamp01(lightness)
	return c
}

// ValidHex reports whether value is an opaque "#rrggbb" color. Hex colors are
// the terminal scheme format; UI tokens are HSL.
func ValidHex(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// NormalizeHex lowercases a validated hex color.
func NormalizeHex(value string) string { return strings.ToLower(value) }

// HexLuminance returns the BT.709 relative luminance of a #rrggbb color, which
// decides whether an imported terminal scheme is dark.
func HexLuminance(value string) (float64, bool) {
	if !ValidHex(value) {
		return 0, false
	}
	parse := func(part string) uint8 {
		number, _ := strconv.ParseUint(part, 16, 8)
		return uint8(number)
	}
	r := toLinear(parse(value[1:3]))
	g := toLinear(parse(value[3:5]))
	b := toLinear(parse(value[5:7]))
	return 0.2126*r + 0.7152*g + 0.0722*b, true
}

// ValidateTerminalColors reports the first missing or malformed color of a
// terminal scheme.
func ValidateTerminalColors(colors TerminalColors) error {
	fields := []struct {
		name  string
		value string
	}{
		{"background", colors.Background}, {"foreground", colors.Foreground},
		{"cursor", colors.Cursor}, {"selection", colors.Selection},
		{"black", colors.Black}, {"red", colors.Red}, {"green", colors.Green},
		{"yellow", colors.Yellow}, {"blue", colors.Blue}, {"magenta", colors.Magenta},
		{"cyan", colors.Cyan}, {"white", colors.White},
		{"brightBlack", colors.BrightBlack}, {"brightRed", colors.BrightRed},
		{"brightGreen", colors.BrightGreen}, {"brightYellow", colors.BrightYellow},
		{"brightBlue", colors.BrightBlue}, {"brightMagenta", colors.BrightMagenta},
		{"brightCyan", colors.BrightCyan}, {"brightWhite", colors.BrightWhite},
	}
	for _, field := range fields {
		if !ValidHex(field.value) {
			return fmt.Errorf("theme: %s is not a #rrggbb color", field.name)
		}
	}
	return nil
}
