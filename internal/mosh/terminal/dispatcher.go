// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"strconv"
	"strings"
)

const maxParams = 65535
const maximumClipboardSize = 16 * 1024

type functionType uint8

const (
	functionEscape functionType = iota
	functionCSI
	functionControl
)

type dispatchFunction func(*Framebuffer, *Dispatcher)

// Dispatcher is Terminal::Dispatcher: parameter collection and the dispatch
// table that applies ECMA-48 functions to a framebuffer. TerminalToHost is the
// reply stream stock calls terminal_to_host.
type Dispatcher struct {
	params         string
	parsedParams   []int
	parsed         bool
	dispatchChars  string
	OSCString      []rune
	TerminalToHost string
}

// NewDispatcher returns an empty dispatcher.
func NewDispatcher() *Dispatcher { return &Dispatcher{} }

// Clone returns a deep copy of the parameter and OSC collection state.
func (d *Dispatcher) Clone() *Dispatcher {
	if d == nil {
		return nil
	}
	out := *d
	out.parsedParams = append([]int(nil), d.parsedParams...)
	out.OSCString = append([]rune(nil), d.OSCString...)
	return &out
}

// NewParamChar appends one parameter byte.
func (d *Dispatcher) NewParamChar(ch rune) {
	if ch != ';' && !(ch >= '0' && ch <= '9') {
		return
	}
	if len(d.params) < 100 {
		d.params += string(byte(ch))
	}
	d.parsed = false
}

// Collect appends an intermediate or private byte.
func (d *Dispatcher) Collect(ch rune) {
	if len(d.dispatchChars) < 8 && ch <= 255 {
		d.dispatchChars += string(byte(ch))
	}
}

// Clear drops collected parameters and dispatch characters.
func (d *Dispatcher) Clear() {
	d.params = ""
	d.dispatchChars = ""
	d.parsed = false
}

// GetDispatchChars returns the collected dispatch key.
func (d *Dispatcher) GetDispatchChars() string { return d.dispatchChars }

func parseParameter(segment string) int {
	// strtol in the C implementation never sees whitespace or signs for the
	// parameter grammar, but keep its empty/non-numeric result.
	if segment == "" {
		return -1
	}
	value := 0
	for i := 0; i < len(segment); i++ {
		if segment[i] < '0' || segment[i] > '9' {
			break
		}
		value = value*10 + int(segment[i]-'0')
		if value > maxParams {
			return -1
		}
	}
	if value == 0 && (len(segment) == 0 || segment[0] < '0' || segment[0] > '9') {
		return -1
	}
	return value
}

func (d *Dispatcher) parseParams() {
	if d.parsed {
		return
	}
	d.parsedParams = d.parsedParams[:0]
	for _, segment := range strings.Split(d.params, ";") {
		d.parsedParams = append(d.parsedParams, parseParameter(segment))
	}
	d.parsed = true
}

// Getparam returns parameter n, or defaultval when absent or below one.
func (d *Dispatcher) Getparam(n int, defaultval int) int {
	if !d.parsed {
		d.parseParams()
	}
	ret := defaultval
	if n < len(d.parsedParams) {
		ret = d.parsedParams[n]
	}
	if ret < 1 {
		ret = defaultval
	}
	return ret
}

// ParamCount returns the number of parsed parameters.
func (d *Dispatcher) ParamCount() int {
	if !d.parsed {
		d.parseParams()
	}
	return len(d.parsedParams)
}

// OSCPut appends to the pending operating-system-command string.
func (d *Dispatcher) OSCPut(ch rune) {
	if len(d.OSCString) < maximumClipboardSize {
		d.OSCString = append(d.OSCString, ch)
	}
}

// OSCStart resets the pending operating-system-command string.
func (d *Dispatcher) OSCStart() { d.OSCString = nil }

// Dispatch performs one control, escape, or CSI function.
func (d *Dispatcher) Dispatch(kind functionType, ch rune, fb *Framebuffer) {
	key := d.dispatchChars
	if kind == functionEscape || kind == functionCSI {
		d.Collect(ch)
		key = d.dispatchChars
	} else {
		if ch > 255 {
			return
		}
		// string(byte(ch)) would UTF-8-encode values above 0x7f; the
		// dispatch key is a raw byte string.
		key = string([]byte{byte(ch)})
	}

	fn, clearsWrap, ok := lookupFunction(kind, key)
	if !ok {
		fb.DS.NextPrintWillWrap = false
		return
	}
	if clearsWrap {
		fb.DS.NextPrintWillWrap = false
	}
	fn(fb, d)
}

// OSCDispatch applies a completed OSC string: clipboard (OSC 52;c;) and title
// or icon name (OSC 0/1/2). It is stock Dispatcher::OSC_dispatch.
func (d *Dispatcher) OSCDispatch(fb *Framebuffer) {
	osc := d.OSCString
	if len(osc) >= 5 && osc[0] == '5' && osc[1] == '2' && osc[2] == ';' &&
		osc[3] == 'c' && osc[4] == ';' {
		fb.Clipboard = append([]rune(nil), osc[5:]...)
		return
	}
	if len(osc) < 1 {
		return
	}
	cmdNum := -1
	offset := 0
	if osc[0] == ';' {
		cmdNum = 0
		offset = 1
	} else if len(osc) >= 2 && osc[1] == ';' {
		cmdNum = int(osc[0] - '0')
		offset = 2
	}
	setIcon := cmdNum == 0 || cmdNum == 1
	setTitle := cmdNum == 0 || cmdNum == 2
	if !setIcon && !setTitle {
		return
	}
	fb.TitleInitialized = true
	length := len(osc)
	if length > 256 {
		length = 256
	}
	title := append([]rune(nil), osc[offset:length]...)
	if setIcon {
		fb.IconName = title
	}
	if setTitle {
		fb.WindowTitle = title
	}
}

func clearLine(fb *Framebuffer, row, start, end int) {
	for col := start; col <= end; col++ {
		fb.ResetCell(fb.Cell(row, col))
	}
}

func csiEL(fb *Framebuffer, d *Dispatcher) {
	switch d.Getparam(0, 0) {
	case 0:
		clearLine(fb, -1, fb.DS.cursorCol, fb.DS.width-1)
	case 1:
		clearLine(fb, -1, 0, fb.DS.cursorCol)
	case 2:
		fb.ResetRow(fb.Row(-1))
	}
}

func csiED(fb *Framebuffer, d *Dispatcher) {
	switch d.Getparam(0, 0) {
	case 0:
		clearLine(fb, -1, fb.DS.cursorCol, fb.DS.width-1)
		for y := fb.DS.cursorRow + 1; y < fb.DS.height; y++ {
			fb.ResetRow(fb.Row(y))
		}
	case 1:
		for y := 0; y < fb.DS.cursorRow; y++ {
			fb.ResetRow(fb.Row(y))
		}
		clearLine(fb, -1, 0, fb.DS.cursorCol)
	case 2:
		for y := 0; y < fb.DS.height; y++ {
			fb.ResetRow(fb.Row(y))
		}
	}
}

func csiCursorMove(fb *Framebuffer, d *Dispatcher) {
	num := d.Getparam(0, 1)
	switch d.dispatchChars[0] {
	case 'A':
		fb.DS.MoveRow(-num, true)
	case 'B':
		fb.DS.MoveRow(num, true)
	case 'C':
		fb.DS.MoveCol(num, true, false)
	case 'D':
		fb.DS.MoveCol(-num, true, false)
	case 'H', 'f':
		fb.DS.MoveRow(d.Getparam(0, 1)-1, false)
		fb.DS.MoveCol(d.Getparam(1, 1)-1, false, false)
	}
}

func csiDA(fb *Framebuffer, d *Dispatcher)  { d.TerminalToHost += "\033[?62c" }
func csiSDA(fb *Framebuffer, d *Dispatcher) { d.TerminalToHost += "\033[>1;10;0c" }

func escDECALN(fb *Framebuffer, d *Dispatcher) {
	for y := 0; y < fb.DS.height; y++ {
		for x := 0; x < fb.DS.width; x++ {
			fb.ResetCell(fb.Cell(y, x))
			cell := fb.Cell(y, x)
			cell.Append('E')
		}
	}
}

func ctrlLF(fb *Framebuffer, d *Dispatcher) { fb.MoveRowsAutoscroll(1) }
func ctrlCR(fb *Framebuffer, d *Dispatcher) { fb.DS.MoveCol(0, false, false) }
func ctrlBS(fb *Framebuffer, d *Dispatcher) { fb.DS.MoveCol(-1, true, false) }
func ctrlRI(fb *Framebuffer, d *Dispatcher) { fb.MoveRowsAutoscroll(-1) }

func ctrlNEL(fb *Framebuffer, d *Dispatcher) {
	fb.DS.MoveCol(0, false, false)
	fb.MoveRowsAutoscroll(1)
}

func horizontalTab(fb *Framebuffer, count int) {
	col := fb.DS.NextTab(count)
	if col == -1 {
		col = fb.DS.width - 1
	}
	wrapSave := fb.DS.NextPrintWillWrap
	fb.DS.MoveCol(col, false, false)
	fb.DS.NextPrintWillWrap = wrapSave
}

func ctrlHT(fb *Framebuffer, d *Dispatcher) { horizontalTab(fb, 1) }

func csiCxT(fb *Framebuffer, d *Dispatcher) {
	param := d.Getparam(0, 1)
	if d.dispatchChars[0] == 'Z' {
		param = -param
	}
	if param == 0 {
		return
	}
	horizontalTab(fb, param)
}

func ctrlHTS(fb *Framebuffer, d *Dispatcher) { fb.DS.SetTab() }

func csiTBC(fb *Framebuffer, d *Dispatcher) {
	switch d.Getparam(0, 0) {
	case 0:
		fb.DS.ClearTab(fb.DS.cursorCol)
	case 3:
		fb.DS.ClearDefaultTabs()
		for x := 0; x < fb.DS.width; x++ {
			fb.DS.ClearTab(x)
		}
	}
}

func decMode(param int, fb *Framebuffer) *bool {
	switch param {
	case 1:
		return &fb.DS.ApplicationModeCursorKeys
	case 3:
		fb.DS.MoveRow(0, false)
		fb.DS.MoveCol(0, false, false)
		for y := 0; y < fb.DS.height; y++ {
			fb.ResetRow(fb.Row(y))
		}
		return nil
	case 5:
		return &fb.DS.ReverseVideo
	case 6:
		fb.DS.MoveRow(0, false)
		fb.DS.MoveCol(0, false, false)
		return &fb.DS.OriginMode
	case 7:
		return &fb.DS.AutoWrapMode
	case 25:
		return &fb.DS.CursorVisible
	case 1004:
		return &fb.DS.MouseFocusEvent
	case 1007:
		return &fb.DS.MouseAlternateScroll
	case 2004:
		return &fb.DS.BracketedPaste
	}
	return nil
}

func setIfAvailable(mode *bool, value bool) {
	if mode != nil {
		*mode = value
	}
}

func csiDECSM(fb *Framebuffer, d *Dispatcher) {
	for i := 0; i < d.ParamCount(); i++ {
		param := d.Getparam(i, 0)
		if param == 9 || (param >= 1000 && param <= 1003) {
			fb.DS.MouseReportingMode = MouseReportingMode(param)
		} else if param == 1005 || param == 1006 || param == 1015 {
			fb.DS.MouseEncodingMode = MouseEncodingMode(param)
		} else {
			setIfAvailable(decMode(param, fb), true)
		}
	}
}

func csiDECRM(fb *Framebuffer, d *Dispatcher) {
	for i := 0; i < d.ParamCount(); i++ {
		param := d.Getparam(i, 0)
		if param == 9 || (param >= 1000 && param <= 1003) {
			fb.DS.MouseReportingMode = MouseReportingNone
		} else if param == 1005 || param == 1006 || param == 1015 {
			fb.DS.MouseEncodingMode = MouseEncodingDefault
		} else {
			setIfAvailable(decMode(param, fb), false)
		}
	}
}

func ansiMode(param int, fb *Framebuffer) *bool {
	if param == 4 {
		return &fb.DS.InsertMode
	}
	return nil
}

func csiSM(fb *Framebuffer, d *Dispatcher) {
	for i := 0; i < d.ParamCount(); i++ {
		if mode := ansiMode(d.Getparam(i, 0), fb); mode != nil {
			*mode = true
		}
	}
}

func csiRM(fb *Framebuffer, d *Dispatcher) {
	for i := 0; i < d.ParamCount(); i++ {
		if mode := ansiMode(d.Getparam(i, 0), fb); mode != nil {
			*mode = false
		}
	}
}

func csiDECSTBM(fb *Framebuffer, d *Dispatcher) {
	top := d.Getparam(0, 1)
	bottom := d.Getparam(1, fb.DS.height)
	if bottom <= top || top > fb.DS.height || (top == 0 && bottom == 1) {
		return
	}
	fb.DS.SetScrollingRegion(top-1, bottom-1)
	fb.DS.MoveRow(0, false)
	fb.DS.MoveCol(0, false, false)
}

func ctrlBEL(fb *Framebuffer, d *Dispatcher) { fb.BellCount++ }

func csiSGR(fb *Framebuffer, d *Dispatcher) {
	for i := 0; i < d.ParamCount(); i++ {
		rendition := d.Getparam(i, 0)
		if (rendition == 38 || rendition == 48) && d.ParamCount()-i >= 3 &&
			d.Getparam(i+1, -1) == 5 {
			if rendition == 38 {
				fb.DS.SetForegroundColor(uint32(d.Getparam(i+2, 0)))
			} else {
				fb.DS.SetBackgroundColor(uint32(d.Getparam(i+2, 0)))
			}
			i += 2
			continue
		}
		if (rendition == 38 || rendition == 48) && d.ParamCount()-i >= 5 &&
			d.Getparam(i+1, -1) == 2 {
			red := uint32(d.Getparam(i+2, 0))
			green := uint32(d.Getparam(i+3, 0))
			blue := uint32(d.Getparam(i+4, 0))
			color := MakeTrueColor(red, green, blue)
			if rendition == 38 {
				fb.DS.SetForegroundColor(color)
			} else {
				fb.DS.SetBackgroundColor(color)
			}
			i += 4
			continue
		}
		fb.DS.renditions.SetRendition(rendition)
	}
}

func escDECSC(fb *Framebuffer, d *Dispatcher) { fb.DS.SaveCursor() }

func escDECRC(fb *Framebuffer, d *Dispatcher) { fb.DS.RestoreCursor() }

func csiDSR(fb *Framebuffer, d *Dispatcher) {
	switch d.Getparam(0, 0) {
	case 5:
		d.TerminalToHost += "\033[0n"
	case 6:
		d.TerminalToHost += "\033[" + strconv.Itoa(fb.DS.cursorRow+1) + ";" +
			strconv.Itoa(fb.DS.cursorCol+1) + "R"
	}
}

func csiIL(fb *Framebuffer, d *Dispatcher) {
	fb.InsertLine(fb.DS.cursorRow, d.Getparam(0, 1))
	fb.DS.MoveCol(0, false, false)
}

func csiDL(fb *Framebuffer, d *Dispatcher) {
	fb.DeleteLine(fb.DS.cursorRow, d.Getparam(0, 1))
	fb.DS.MoveCol(0, false, false)
}

func csiICH(fb *Framebuffer, d *Dispatcher) {
	cells := d.Getparam(0, 1)
	for i := 0; i < cells; i++ {
		fb.InsertCell(fb.DS.cursorRow, fb.DS.cursorCol)
	}
}

func csiDCH(fb *Framebuffer, d *Dispatcher) {
	cells := d.Getparam(0, 1)
	for i := 0; i < cells; i++ {
		fb.DeleteCell(fb.DS.cursorRow, fb.DS.cursorCol)
	}
}

func csiVPA(fb *Framebuffer, d *Dispatcher) {
	fb.DS.MoveRow(d.Getparam(0, 1)-1, false)
}

func csiHPA(fb *Framebuffer, d *Dispatcher) {
	fb.DS.MoveCol(d.Getparam(0, 1)-1, false, false)
}

func csiECH(fb *Framebuffer, d *Dispatcher) {
	num := d.Getparam(0, 1)
	limit := fb.DS.cursorCol + num - 1
	if limit >= fb.DS.width {
		limit = fb.DS.width - 1
	}
	clearLine(fb, -1, fb.DS.cursorCol, limit)
}

func escRIS(fb *Framebuffer, d *Dispatcher) { fb.Reset() }

func csiDECSTR(fb *Framebuffer, d *Dispatcher) { fb.SoftReset() }

func csiSD(fb *Framebuffer, d *Dispatcher) { fb.Scroll(d.Getparam(0, 1)) }

func csiSU(fb *Framebuffer, d *Dispatcher) { fb.Scroll(-d.Getparam(0, 1)) }

type registryEntry struct {
	fn         dispatchFunction
	clearsWrap bool
}

// stockFunctions mirrors the Function constructors in terminalfunctions.cc.
var stockFunctions = map[functionType]map[string]registryEntry{
	functionCSI: {
		"K":    {csiEL, true},
		"J":    {csiED, true},
		"A":    {csiCursorMove, true},
		"B":    {csiCursorMove, true},
		"C":    {csiCursorMove, true},
		"D":    {csiCursorMove, true},
		"H":    {csiCursorMove, true},
		"f":    {csiCursorMove, true},
		"c":    {csiDA, true},
		">c":   {csiSDA, true},
		"I":    {csiCxT, false},
		"Z":    {csiCxT, false},
		"g":    {csiTBC, false},
		"?h":   {csiDECSM, false},
		"?l":   {csiDECRM, false},
		"h":    {csiSM, true},
		"l":    {csiRM, true},
		"r":    {csiDECSTBM, true},
		"m":    {csiSGR, false},
		"n":    {csiDSR, true},
		"L":    {csiIL, true},
		"M":    {csiDL, true},
		"@":    {csiICH, true},
		"P":    {csiDCH, true},
		"d":    {csiVPA, true},
		"G":    {csiHPA, true},
		"\x60": {csiHPA, true},
		"X":    {csiECH, true},
		"!p":   {csiDECSTR, true},
		"S":    {csiSD, true},
		"T":    {csiSU, true},
	},
	functionEscape: {
		"#8": {escDECALN, true},
		"7":  {escDECSC, true},
		"8":  {escDECRC, true},
		"c":  {escRIS, true},
	},
	functionControl: {
		"\x0a": {ctrlLF, true},
		"\x84": {ctrlLF, true},
		"\x0b": {ctrlLF, true},
		"\x0c": {ctrlLF, true},
		"\x0d": {ctrlCR, true},
		"\x08": {ctrlBS, true},
		"\x8d": {ctrlRI, true},
		"\x85": {ctrlNEL, true},
		"\x09": {ctrlHT, false},
		"\x88": {ctrlHTS, true},
		"\x07": {ctrlBEL, true},
	},
}

func lookupFunction(kind functionType, key string) (dispatchFunction, bool, bool) {
	table, ok := stockFunctions[kind]
	if !ok {
		return nil, false, false
	}
	entry, ok := table[key]
	if !ok {
		return nil, false, false
	}
	return entry.fn, entry.clearsWrap, true
}
