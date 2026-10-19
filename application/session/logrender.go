package session

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// logRenderer turns terminal output into a readable transcript. It is not a
// full terminal emulator: it tracks the normal screen, cursor movement, simple
// erase operations, and SGR color so logs stay legible without reproducing
// TUIs (alternate-screen painting is omitted, as in the original).
type logRenderer struct {
	lines []logLine
	row   int
	col   int
	style logStyle
	state rendererState
	param []byte
	osc   []byte
	oscST bool
	utf8  []byte
	alt   bool
}

type logLine struct {
	cells []logCell
	at    time.Time
}

type logCell struct {
	character rune
	style     logStyle
}

type logStyle struct {
	foreground string
	background string
	bold       bool
	italic     bool
	underline  bool
	inverse    bool
}

type rendererState int

const (
	rendererGround rendererState = iota
	rendererEscape
	rendererCSI
	rendererOSC
	rendererIgnoreString
)

func newLogRenderer() *logRenderer {
	return &logRenderer{}
}

// feed consumes a chunk of terminal output. Parser state survives chunk
// boundaries, which is required because OSC/CSI sequences split across reads.
func (r *logRenderer) feed(data []byte, at time.Time) {
	for index := 0; index < len(data); index++ {
		byteValue := data[index]
		switch r.state {
		case rendererEscape:
			r.handleEscape(byteValue)
		case rendererCSI:
			r.handleCSIByte(byteValue)
		case rendererOSC:
			r.handleOSCByte(byteValue)
		case rendererIgnoreString:
			r.handleIgnoreStringByte(byteValue)
		default:
			if len(r.utf8) > 0 {
				r.utf8 = append(r.utf8, byteValue)
				if !utf8.FullRune(r.utf8) {
					continue
				}
				character, _ := utf8.DecodeRune(r.utf8)
				r.utf8 = r.utf8[:0]
				r.writeRune(character, at)
				continue
			}
			if byteValue >= 0x80 {
				if byteValue <= 0x9f {
					// C1 control: 0x9B is CSI; the rest are ignored.
					if byteValue == 0x9b {
						r.beginCSI()
					}
					continue
				}
				r.utf8 = append(r.utf8[:0], byteValue)
				if !utf8.FullRune(r.utf8) {
					continue
				}
				character, _ := utf8.DecodeRune(r.utf8)
				r.utf8 = r.utf8[:0]
				r.writeRune(character, at)
				continue
			}
			r.handleGround(byteValue, at)
		}
	}
}

func (r *logRenderer) handleGround(byteValue byte, at time.Time) {
	switch {
	case byteValue == 0x1b:
		r.state = rendererEscape
	case byteValue == 0x07 || byteValue == 0x00:
		// Bell and NUL produce no text.
	case byteValue == '\r':
		r.col = 0
	case byteValue == '\n' || byteValue == 0x0b || byteValue == 0x0c:
		r.newline()
	case byteValue == '\b':
		if r.col > 0 {
			r.col--
		}
	case byteValue == '\t':
		if r.alt {
			return
		}
		next := (r.col/8 + 1) * 8
		for r.col < next {
			r.writeRune(' ', at)
		}
	case byteValue < 0x20 || byteValue == 0x7f:
		// Other C0 controls do not paint.
	default:
		r.writeRune(rune(byteValue), at)
	}
}

func (r *logRenderer) newline() {
	r.col = 0
	if r.alt {
		return
	}
	r.row++
	r.ensureLine()
}

func (r *logRenderer) handleEscape(byteValue byte) {
	r.state = rendererGround
	switch byteValue {
	case '[':
		r.beginCSI()
	case ']':
		r.osc = r.osc[:0]
		r.oscST = false
		r.state = rendererOSC
	case 'P', '^', '_':
		r.state = rendererIgnoreString
	case 'c':
		r.reset()
	case '7', '8', 'D', 'E', 'M':
		// Auto-wrap and cursor movement have no useful transcript effect.
	}
}

func (r *logRenderer) beginCSI() {
	r.param = r.param[:0]
	r.state = rendererCSI
}

func (r *logRenderer) handleCSIByte(byteValue byte) {
	if byteValue == 0x1b {
		r.state = rendererEscape
		return
	}
	if byteValue == 0x18 || byteValue == 0x1a {
		r.state = rendererGround
		return
	}
	if byteValue < 0x20 {
		return
	}
	if byteValue >= 0x40 && byteValue <= 0x7e {
		r.applyCSI(byteValue, string(r.param))
		r.param = r.param[:0]
		r.state = rendererGround
		return
	}
	if len(r.param) < 128 {
		r.param = append(r.param, byteValue)
	}
}

func (r *logRenderer) handleOSCByte(byteValue byte) {
	if byteValue == 0x07 {
		r.state = rendererGround
		return
	}
	if r.oscST {
		r.oscST = false
		if byteValue == '\\' {
			r.state = rendererGround
			return
		}
	}
	if byteValue == 0x1b {
		r.oscST = true
		return
	}
	if len(r.osc) < 8192 {
		r.osc = append(r.osc, byteValue)
	}
}

func (r *logRenderer) handleIgnoreStringByte(byteValue byte) {
	if byteValue == 0x07 {
		r.state = rendererGround
		return
	}
	if byteValue == 0x1b {
		r.state = rendererEscape
	}
}

func (r *logRenderer) applyCSI(final byte, raw string) {
	private := strings.HasPrefix(raw, "?") || strings.HasPrefix(raw, ">") || strings.HasPrefix(raw, "=")
	parameters := parseCSIParameters(strings.TrimLeft(raw, "?>=<!"))
	first := 0
	if len(parameters) > 0 {
		first = parameters[0]
	}
	switch final {
	case 'h', 'l':
		if private {
			enabled := final == 'h'
			for _, parameter := range parameters {
				switch parameter {
				case 47, 1047, 1049:
					r.alt = enabled
					if enabled {
						r.col = 0
					}
				}
			}
		}
	case 'A':
		r.moveRow(-defaultOne(first))
	case 'B':
		r.moveRow(defaultOne(first))
	case 'C':
		if !r.alt {
			r.col += defaultOne(first)
		}
	case 'D':
		if !r.alt {
			r.col -= defaultOne(first)
			if r.col < 0 {
				r.col = 0
			}
		}
	case 'E':
		r.moveRow(defaultOne(first))
		r.col = 0
	case 'F':
		r.moveRow(-defaultOne(first))
		r.col = 0
	case 'G':
		if !r.alt {
			r.col = defaultOne(first) - 1
		}
	case 'H', 'f':
		row := 1
		column := 1
		if len(parameters) > 0 && parameters[0] > 0 {
			row = parameters[0]
		}
		if len(parameters) > 1 && parameters[1] > 0 {
			column = parameters[1]
		}
		if !r.alt {
			r.row += row - 1
			if r.row < 0 {
				r.row = 0
			}
			r.col = column - 1
			r.ensureLine()
		}
	case 'J':
		r.eraseDisplay(first)
	case 'K':
		r.eraseLine(first)
	case 'm':
		r.applySGR(parameters)
	case 's':
		// Cursor save/restore is not needed for a transcript.
	case 'u':
	case 'X':
		r.eraseCharacters(defaultOne(first))
	}
}

func (r *logRenderer) moveRow(delta int) {
	if r.alt || delta == 0 {
		return
	}
	r.row += delta
	if r.row < 0 {
		r.row = 0
	}
	r.ensureLine()
}

func (r *logRenderer) eraseDisplay(mode int) {
	if r.alt {
		return
	}
	switch mode {
	case 1:
		if r.row > 0 {
			r.lines = append([]logLine(nil), r.lines[r.row:]...)
			r.row = 0
		}
		r.ensureLine()
		limit := r.col
		if limit > len(r.lines[0].cells) {
			limit = len(r.lines[0].cells)
		}
		for index := 0; index < limit; index++ {
			r.lines[0].cells[index] = logCell{character: ' ', style: r.style}
		}
	case 2, 3:
		r.ensureLine()
		if hasContent(r.lines[r.row]) {
			r.row = len(r.lines)
			r.ensureLine()
		} else {
			r.lines = r.lines[:r.row]
			r.ensureLine()
		}
		r.col = 0
	default:
		r.ensureLine()
		if r.col < len(r.lines[r.row].cells) {
			r.lines[r.row].cells = r.lines[r.row].cells[:r.col]
		}
		if r.row+1 < len(r.lines) {
			r.lines = r.lines[:r.row+1]
		}
	}
}

func (r *logRenderer) eraseLine(mode int) {
	if r.alt {
		return
	}
	r.ensureLine()
	line := &r.lines[r.row]
	switch mode {
	case 1:
		limit := r.col
		if limit >= len(line.cells) {
			limit = len(line.cells) - 1
		}
		for index := 0; index <= limit && index < len(line.cells); index++ {
			line.cells[index] = logCell{character: ' ', style: r.style}
		}
	case 2:
		line.cells = nil
	default:
		if r.col < len(line.cells) {
			line.cells = line.cells[:r.col]
		}
	}
}

func (r *logRenderer) eraseCharacters(count int) {
	if r.alt {
		return
	}
	r.ensureLine()
	line := &r.lines[r.row]
	for index := 0; index < count; index++ {
		position := r.col + index
		if position < len(line.cells) {
			line.cells[position] = logCell{character: ' ', style: r.style}
		}
	}
}

func (r *logRenderer) reset() {
	r.style = logStyle{}
	r.alt = false
	r.row = 0
	r.col = 0
	r.lines = nil
}

func (r *logRenderer) writeRune(character rune, at time.Time) {
	if r.alt {
		return
	}
	r.ensureLine()
	line := &r.lines[r.row]
	if line.at.IsZero() && character != ' ' {
		line.at = at
	}
	for len(line.cells) <= r.col {
		line.cells = append(line.cells, logCell{character: ' '})
	}
	line.cells[r.col] = logCell{character: character, style: r.style}
	r.col++
}

func (r *logRenderer) ensureLine() {
	if r.row < 0 {
		r.row = 0
	}
	for len(r.lines) <= r.row {
		r.lines = append(r.lines, logLine{})
	}
	if len(r.lines) > 100000 {
		drop := len(r.lines) - 50000
		r.lines = append([]logLine(nil), r.lines[drop:]...)
		r.row -= drop
		if r.row < 0 {
			r.row = 0
		}
	}
}

// finish ends a live snapshot. There is no pending wrap to discharge; the
// current line is part of the renderer state.
func (r *logRenderer) finish() {}

// text renders a plain-text transcript with optional timestamp prefixes.
func (r *logRenderer) text(prefix func(time.Time) string) string {
	lines := make([]string, 0, len(r.lines))
	for _, line := range r.lines {
		text := strings.TrimRight(lineText(line), " \t")
		if text == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, prefix(line.at)+text)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// html renders escaped, styled transcript lines. The returned fragment is
// placed inside the pre-wrapped HTML log body.
func (r *logRenderer) html(prefix func(time.Time) string) string {
	lines := make([]string, 0, len(r.lines))
	for _, line := range r.lines {
		lineTextValue := strings.TrimRight(lineText(line), " \t")
		if lineTextValue == "" {
			lines = append(lines, "")
			continue
		}
		var builder strings.Builder
		builder.WriteString(escapeHTML(prefix(line.at)))
		cells := line.cells
		for index := 0; index < len(cells); {
			style := cells[index].style
			end := index
			for end < len(cells) && cells[end].style == style {
				end++
			}
			run := string(cellCharacters(cells[index:end]))
			run = strings.TrimRight(run, " \t")
			if run != "" {
				if style.plain() {
					builder.WriteString(escapeHTML(run))
				} else {
					builder.WriteString(`<span style="`)
					builder.WriteString(style.css())
					builder.WriteString(`">`)
					builder.WriteString(escapeHTML(run))
					builder.WriteString(`</span>`)
				}
			}
			index = end
		}
		lines = append(lines, builder.String())
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func lineText(line logLine) string {
	return string(cellCharacters(line.cells))
}

func cellCharacters(cells []logCell) []rune {
	characters := make([]rune, len(cells))
	for index, cell := range cells {
		if cell.character == 0 {
			characters[index] = ' '
			continue
		}
		characters[index] = cell.character
	}
	return characters
}

func hasContent(line logLine) bool {
	for _, cell := range line.cells {
		if cell.character != 0 && cell.character != ' ' {
			return true
		}
	}
	return false
}

func defaultOne(value int) int {
	if value <= 0 {
		return 1
	}
	return value
}

func parseCSIParameters(raw string) []int {
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(character rune) bool {
		return character == ';' || character == ':'
	})
	parameters := make([]int, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			parameters = append(parameters, 0)
			continue
		}
		value, err := strconv.Atoi(field)
		if err != nil {
			value = 0
		}
		parameters = append(parameters, value)
	}
	return parameters
}

func (r *logRenderer) applySGR(parameters []int) {
	if len(parameters) == 0 {
		parameters = []int{0}
	}
	for index := 0; index < len(parameters); index++ {
		parameter := parameters[index]
		switch {
		case parameter == 0:
			r.style = logStyle{}
		case parameter == 1:
			r.style.bold = true
		case parameter == 3:
			r.style.italic = true
		case parameter == 4:
			r.style.underline = true
		case parameter == 7:
			r.style.inverse = true
		case parameter == 22:
			r.style.bold = false
		case parameter == 23:
			r.style.italic = false
		case parameter == 24:
			r.style.underline = false
		case parameter == 27:
			r.style.inverse = false
		case parameter >= 30 && parameter <= 37:
			r.style.foreground = ansiColor(parameter - 30)
		case parameter == 39:
			r.style.foreground = ""
		case parameter >= 40 && parameter <= 47:
			r.style.background = ansiColor(parameter - 40)
		case parameter == 49:
			r.style.background = ""
		case parameter >= 90 && parameter <= 97:
			r.style.foreground = ansiColor(8 + parameter - 90)
		case parameter >= 100 && parameter <= 107:
			r.style.background = ansiColor(8 + parameter - 100)
		case parameter == 38 || parameter == 48:
			color, consumed := extendedColor(parameters[index+1:])
			if color != "" {
				if parameter == 38 {
					r.style.foreground = color
				} else {
					r.style.background = color
				}
			}
			index += consumed
		}
	}
}

func extendedColor(parameters []int) (string, int) {
	if len(parameters) == 0 {
		return "", 0
	}
	switch parameters[0] {
	case 5:
		if len(parameters) >= 2 {
			return color256(parameters[1]), 2
		}
		return "", 1
	case 2:
		if len(parameters) >= 4 {
			return rgbHex(parameters[1], parameters[2], parameters[3]), 4
		}
		return "", len(parameters)
	default:
		return "", 1
	}
}

func rgbHex(red, green, blue int) string {
	return fmt.Sprintf("#%02x%02x%02x", clampColor(red), clampColor(green), clampColor(blue))
}

func clampColor(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}

func color256(index int) string {
	if index < 0 {
		index = 0
	}
	if index > 255 {
		index = 255
	}
	if index < 16 {
		return ansiColor(index)
	}
	if index < 232 {
		index -= 16
		red := index / 36
		green := (index % 36) / 6
		blue := index % 6
		level := func(value int) int {
			if value == 0 {
				return 0
			}
			return 55 + value*40
		}
		return rgbHex(level(red), level(green), level(blue))
	}
	level := 8 + (index-232)*10
	return rgbHex(level, level, level)
}

func ansiColor(index int) string {
	palette := [...]string{
		"#000000", "#cd0000", "#00cd00", "#cdcd00",
		"#0000ee", "#cd00cd", "#00cdcd", "#e5e5e5",
		"#7f7f7f", "#ff0000", "#00ff00", "#ffff00",
		"#5c5cff", "#ff00ff", "#00ffff", "#ffffff",
	}
	if index < 0 || index >= len(palette) {
		return ""
	}
	return palette[index]
}

func (s logStyle) plain() bool {
	return s.foreground == "" && s.background == "" && !s.bold && !s.italic && !s.underline && !s.inverse
}

func (s logStyle) css() string {
	foreground := s.foreground
	background := s.background
	if s.inverse {
		foreground, background = background, foreground
		if foreground == "" {
			foreground = "var(--term-default-bg)"
		}
		if background == "" {
			background = "var(--term-default-fg)"
		}
	}
	styles := make([]string, 0, 6)
	if foreground != "" {
		styles = append(styles, "color:"+foreground)
	}
	if background != "" {
		styles = append(styles, "background-color:"+background)
	}
	if s.bold {
		styles = append(styles, "font-weight:600")
	}
	if s.italic {
		styles = append(styles, "font-style:italic")
	}
	if s.underline {
		styles = append(styles, "text-decoration:underline")
	}
	return strings.Join(styles, ";")
}
