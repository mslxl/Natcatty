package theme

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrInvalidITermcolors reports a file that is not a usable Apple plist
// .itermcolors scheme: malformed XML, no root dictionary, or a scheme without
// both a background and a foreground color.
var ErrInvalidITermcolors = errors.New("theme: invalid .itermcolors file")

// itermColorKeys maps an .itermcolors key to the field it fills. "Bold Color"
// exists in the file format but MyGo's terminal theme has no bold color, so it
// is ignored exactly as the native renderer ignores it.
var itermColorKeys = map[string]string{
	"Ansi 0 Color":  "black",
	"Ansi 1 Color":  "red",
	"Ansi 2 Color":  "green",
	"Ansi 3 Color":  "yellow",
	"Ansi 4 Color":  "blue",
	"Ansi 5 Color":  "magenta",
	"Ansi 6 Color":  "cyan",
	"Ansi 7 Color":  "white",
	"Ansi 8 Color":  "brightBlack",
	"Ansi 9 Color":  "brightRed",
	"Ansi 10 Color": "brightGreen",
	"Ansi 11 Color": "brightYellow",
	"Ansi 12 Color": "brightBlue",
	"Ansi 13 Color": "brightMagenta",
	"Ansi 14 Color": "brightCyan",
	"Ansi 15 Color": "brightWhite",

	"Background Color": "background",
	"Foreground Color": "foreground",
	"Cursor Color":     "cursor",
	"Selection Color":  "selection",
}

// ParseITermcolors parses an Apple plist XML .itermcolors scheme into a custom
// terminal theme. name becomes the display name; the caller assigns the ID.
//
// The parser mirrors the original: real components are 0..1, integer
// components are 0..255, a color needs all three components, only background
// and foreground are required, and the missing ANSI colors fall back to the
// original defaults. A missing cursor uses the foreground; a missing selection
// uses #264f78 on a dark background and #add6ff on a light one.
func ParseITermcolors(data []byte, name string) (TerminalTheme, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return TerminalTheme{}, fmt.Errorf("%w: empty file", ErrInvalidITermcolors)
	}
	decoder := xml.NewDecoder(bytes.NewReader(trimmed))

	var found map[string]string
	depth := 0
	rootDepth := -1
	key := ""
	plistSeen := false

	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return TerminalTheme{}, fmt.Errorf("%w: %v", ErrInvalidITermcolors, tokenErr)
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if rootDepth < 0 {
				if element.Name.Local == "plist" {
					plistSeen = true
				}
				if element.Name.Local == "dict" {
					if !plistSeen {
						return TerminalTheme{}, fmt.Errorf("%w: no plist root", ErrInvalidITermcolors)
					}
					rootDepth = depth
					found = make(map[string]string)
				}
				continue
			}
			if depth != rootDepth+1 {
				// Content nested below a value (for example the Alpha
				// Component inside a color dict) is consumed by the color
				// parser or skipped below.
				if err := decoder.Skip(); err != nil {
					return TerminalTheme{}, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
				}
				depth--
				continue
			}
			switch element.Name.Local {
			case "key":
				var text string
				if err := decoder.DecodeElement(&text, &element); err != nil {
					return TerminalTheme{}, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
				}
				depth--
				key = strings.TrimSpace(text)
			case "dict":
				pending := key
				fields, ok, parseErr := parseColorDict(decoder, depth)
				depth--
				if parseErr != nil {
					return TerminalTheme{}, parseErr
				}
				key = ""
				if !ok {
					continue
				}
				if field := itermColorKeys[pending]; field != "" && fields.color != "" {
					found[field] = fields.color
				}
			default:
				if err := decoder.Skip(); err != nil {
					return TerminalTheme{}, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
				}
				depth--
				key = ""
			}
		case xml.EndElement:
			if rootDepth >= 0 && depth == rootDepth && element.Name == (xml.Name{Local: "dict"}) {
				rootDepth = -1
			}
			depth--
		}
	}

	if found == nil {
		return TerminalTheme{}, fmt.Errorf("%w: no root dictionary", ErrInvalidITermcolors)
	}
	background, foreground := found["background"], found["foreground"]
	if !ValidHex(background) || !ValidHex(foreground) {
		return TerminalTheme{}, fmt.Errorf("%w: background and foreground are required", ErrInvalidITermcolors)
	}

	luminance, _ := HexLuminance(background)
	dark := luminance < 0.5
	selection := found["selection"]
	if !ValidHex(selection) {
		selection = "#add6ff"
		if dark {
			selection = "#264f78"
		}
	}
	cursor := found["cursor"]
	if !ValidHex(cursor) {
		cursor = foreground
	}
	defaults := TerminalColors{
		Background: background, Foreground: foreground,
		Cursor: cursor, Selection: selection,
		Black: "#000000", Red: "#cc0000", Green: "#00cc00", Yellow: "#cccc00",
		Blue: "#0000cc", Magenta: "#cc00cc", Cyan: "#00cccc", White: "#cccccc",
		BrightBlack: "#666666", BrightRed: "#ff0000", BrightGreen: "#00ff00",
		BrightYellow: "#ffff00", BrightBlue: "#0000ff", BrightMagenta: "#ff00ff",
		BrightCyan: "#00ffff", BrightWhite: "#ffffff",
	}
	for field, color := range found {
		if ValidHex(color) {
			setTerminalColor(&defaults, field, NormalizeHex(color))
		}
	}
	defaults.Background = NormalizeHex(background)
	defaults.Foreground = NormalizeHex(foreground)
	if err := ValidateTerminalColors(defaults); err != nil {
		return TerminalTheme{}, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
	}
	if strings.TrimSpace(name) == "" {
		name = "Imported Theme"
	}
	return TerminalTheme{Name: strings.TrimSpace(name), Dark: dark, Custom: true, Colors: defaults}, nil
}

// parsedColor is one color dictionary's decoded value.
type parsedColor struct {
	color string
}

// parseColorDict consumes a color dictionary and its EndElement. The caller
// has already consumed the <dict> StartElement at dictDepth and keeps the key
// that preceded it.
func parseColorDict(decoder *xml.Decoder, dictDepth int) (parsedColor, bool, error) {
	components := map[string]float64{}
	depth := dictDepth
	component := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			return parsedColor{}, false, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if depth == dictDepth+1 {
				switch element.Name.Local {
				case "key":
					var text string
					if err := decoder.DecodeElement(&text, &element); err != nil {
						return parsedColor{}, false, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
					}
					depth--
					component = strings.TrimSpace(text)
					continue
				case "real", "integer":
					var text string
					if err := decoder.DecodeElement(&text, &element); err != nil {
						return parsedColor{}, false, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
					}
					depth--
					value, ok := parseNonNegative(strings.TrimSpace(text))
					if !ok {
						component = ""
						continue
					}
					if element.Name.Local == "integer" {
						value /= 255
					}
					switch component {
					case "Red Component":
						components["r"] = value
					case "Green Component":
						components["g"] = value
					case "Blue Component":
						components["b"] = value
					}
					component = ""
					continue
				}
			}
			if err := decoder.Skip(); err != nil {
				return parsedColor{}, false, fmt.Errorf("%w: %v", ErrInvalidITermcolors, err)
			}
			depth--
		case xml.EndElement:
			depth--
			if depth < dictDepth {
				r, rok := components["r"]
				g, gok := components["g"]
				b, bok := components["b"]
				if !rok || !gok || !bok {
					return parsedColor{}, false, nil
				}
				clamp := func(v float64) uint8 {
					if v < 0 {
						v = 0
					}
					if v > 1 {
						v = 1
					}
					return uint8(v*255 + 0.5)
				}
				return parsedColor{color: fmt.Sprintf("#%02x%02x%02x", clamp(r), clamp(g), clamp(b))}, true, nil
			}
		}
	}
}

// setTerminalColor writes one parsed color field.
func setTerminalColor(colors *TerminalColors, field, value string) {
	switch field {
	case "background":
		colors.Background = value
	case "foreground":
		colors.Foreground = value
	case "cursor":
		colors.Cursor = value
	case "selection":
		colors.Selection = value
	case "black":
		colors.Black = value
	case "red":
		colors.Red = value
	case "green":
		colors.Green = value
	case "yellow":
		colors.Yellow = value
	case "blue":
		colors.Blue = value
	case "magenta":
		colors.Magenta = value
	case "cyan":
		colors.Cyan = value
	case "white":
		colors.White = value
	case "brightBlack":
		colors.BrightBlack = value
	case "brightRed":
		colors.BrightRed = value
	case "brightGreen":
		colors.BrightGreen = value
	case "brightYellow":
		colors.BrightYellow = value
	case "brightBlue":
		colors.BrightBlue = value
	case "brightMagenta":
		colors.BrightMagenta = value
	case "brightCyan":
		colors.BrightCyan = value
	case "brightWhite":
		colors.BrightWhite = value
	}
}
