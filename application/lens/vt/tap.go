// Package vt taps the terminal's byte stream: it parses the OSC the shell
// integration sends (133 command marks, 7 working directory, 9;4 progress,
// 7501 program status), splits the output into lines, and fills the block
// store. It only observes; the bytes go on to the terminal unchanged.
package vt

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

const (
	stGround = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	// stString and stStringEsc swallow a control string's payload: APC (Kitty
	// graphics), DCS (Sixel), PM and SOS carry pictures or private data, not
	// command output. Without them the payload after ESC _ or ESC P became
	// block text ("Gf=100,a=T;iVBORw…"), the garbage a picture left in its
	// command's output.
	stString
	stStringEsc
)

// Tap is the incremental parser. Feed it every byte the PTY produced, in
// order; it may be called from any goroutine, and the store guards itself.
type Tap struct {
	store *model.Store

	state  int
	osc    []byte
	line   []byte
	raw    []byte
	sawCR  bool
	bells  atomic.Int64
	colsMu sync.RWMutex
	cols   int // capture width in cells; 0 disables soft-wrap joining
	col    int // cells used by the current logical line

	// ansi keeps the same line with escapes, and escLine/escBlock mark where the
	// last escape began so our own OSC marks (133/7/9/7501) can be dropped from
	// it while the program's colours stay.
	ansi      []byte
	escStart  int
	escActive bool

	// inCommand is between OSC 133;C and OSC 133;D.
	inCommand bool
	// alt is DECSET ?47/?1047/?1049 inside a command: a full-screen program
	// (vim, less, top) owns the screen, and what it paints is not the
	// command's output. The terminal restores the main screen when the
	// program quits, so the tap ignores the painting in between instead of
	// keeping a monochrome copy of the TUI in the block.
	alt bool
	// Netcatty's session hook reports the exact typed command and cwd in a
	// private OSC 1337 marker immediately before the standard OSC 133;C mark.
	// Keeping it here lets the Terminal lens path work with Natcatty's existing
	// capture hook as well as shells that put the line directly in OSC 133;C.
	pendingTyped string
	pendingCwd   string

	// The capture screen: Terminal feeds a lens the rows of an 8-row scratch screen as
	// they scroll off it, not the bytes' line feeds. Rows emitted by the screen are
	// drained into the store after each step.
	rowsModel *rows
	rowsOut   []Line
	csi       []byte
	cwd       string

	// Progress is what OSC 9;4 reported last.
	Progress float64
	HasProg  bool
	// Status is what OSC 7501 reported last: working, waiting, finished, failed.
	Status string
}

// New makes a tap that fills store.
func New(store *model.Store) *Tap { return &Tap{store: store} }

// WithCols tells the tap the pseudo-console's width so it can join rows that
// the console soft-wrapped. Terminal does the same in its lens capture ("soft-
// wrapped rows joined", flow.exe strings line 801257-801258), because a lens
// must see one logical line, not the display rows.
// Cwd is the working directory the shell last reported with OSC 7.
func (t *Tap) Cwd() string { return t.cwd }

func (t *Tap) WithCols(cols int) *Tap {
	t.SetCols(cols)
	return t
}

// SetCols sets the width the console soft-wraps at; the app resizes it when the
// window changes size, from the UI thread while the reader feeds this tap.
func (t *Tap) SetCols(cols int) {
	t.colsMu.Lock()
	t.cols = cols
	t.colsMu.Unlock()
	if t.rowsModel != nil && cols > 0 {
		t.rowsModel.setCols(cols)
	}
}

// captureRows is the height of the tap's capture screen: tall enough for the
// absolute row addressing that Windows' ConPTY emits when it reflows output.
const captureRows = 200

// screen is the capture screen: the command's output is written into it, and the
// rows it scrolls off are the lines a lens reads.
func (t *Tap) screen() *rows {
	if t.rowsModel == nil {
		cols := t.Cols()
		if cols <= 0 {
			cols = 200 // no width known: rows cannot wrap, so nothing is misjoined
		}
		t.rowsModel = newRowsH(cols, captureRows)
	}
	return t.rowsModel
}

// drain hands the rows the screen emitted to the block: one lens line each, and the
// same text as raw output.
func (t *Tap) drain() {
	for _, ln := range t.rowsOut {
		if ln.Text != "" {
			t.store.AddLine(model.Line{Text: ln.Text, Styled: ln.Styled})
		}
		t.store.Append([]byte(ln.Text + "\n"))
	}
	t.rowsOut = t.rowsOut[:0]
	if t.state != stString && t.state != stStringEsc {
		// A control string's introducer stays in the buffer until its end cuts
		// it: the payload arrives over many reads, and the offset recorded at
		// the introducer only means anything while the buffer it points into
		// is the same one.
		t.ansi = t.ansi[:0]
	}
}

// Bells counts the bells the program rang (BEL, 0x07). An OSC may end with a BEL
// too, and that one is not a bell, so only the ground state counts.
func (t *Tap) Bells() int64 { return t.bells.Load() }

// applyCSI follows the cursor operations a program uses to draw its rows without
// line feeds (PowerShell's formatting does), and the erases it uses to redraw.
func (t *Tap) applyCSI(final byte) {
	if !t.inCommand {
		return
	}
	if final == 'h' || final == 'l' {
		// DECSET/DECRST: of the modes a program sets, only the alternate
		// screen matters to the transcript.
		if len(t.csi) > 0 && t.csi[0] == '?' {
			for _, field := range strings.Split(string(t.csi[1:]), ";") {
				if n, err := strconv.Atoi(field); err == nil && (n == 47 || n == 1047 || n == 1049) {
					t.alt = final == 'h'
					return
				}
			}
		}
		return
	}
	if t.alt {
		return // the program's painting is not the command's output
	}
	r := t.screen()
	parts := strings.Split(string(t.csi), ";")
	arg := func(i, def int) int {
		if i < len(parts) {
			if n, err := strconv.Atoi(parts[i]); err == nil && n > 0 {
				return n
			}
		}
		return def
	}
	switch final {
	case 'H', 'f':
		row := arg(0, 1) - 1
		if row > r.curR {
			// Jumping down: the rows in between are finished and go out first.
			r.advanceTo(row, &t.rowsOut)
			t.drain()
		}
		r.moveTo(row, arg(1, 1)-1)
	case 'A':
		r.moveTo(r.curR-arg(0, 1), r.curC)
	case 'B':
		row := r.curR + arg(0, 1)
		if row > r.curR {
			r.advanceTo(row, &t.rowsOut)
			t.drain()
		}
		r.moveTo(row, r.curC)
	case 'C':
		r.moveTo(r.curR, r.curC+arg(0, 1))
	case 'D':
		r.moveTo(r.curR, r.curC-arg(0, 1))
	case 'G':
		r.moveTo(r.curR, arg(0, 1)-1)
	case 'K':
		r.eraseLine()
	case 'J':
		if arg(0, 1) == 2 {
			r.eraseDisplay()
		}
	case 'm':
		r.style[r.curR] = append(r.style[r.curR], 0x1b, '[')
		r.style[r.curR] = append(r.style[r.curR], t.csi...)
		r.style[r.curR] = append(r.style[r.curR], 'm')
	}
}

// Cols is the width the tap joins wrapped rows at.
func (t *Tap) Cols() int {
	t.colsMu.RLock()
	defer t.colsMu.RUnlock()
	return t.cols
}

// Feed parses a chunk of the program's output.
func (t *Tap) Feed(b []byte) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		// Keep every byte for the styled line; an OSC that turns out to be one of
		// our own marks is truncated back to escStart when it is dispatched. An
		// alternate screen's painting is not kept: it belongs to the program,
		// not to the command's output. A control string's payload is not kept
		// either, byte by byte as it arrives: an image is read in many reads,
		// and the buffer is emptied after each of them, so remembering the
		// introducer's offset alone could never cut the payload back out.
		if !t.alt && t.state != stString && t.state != stStringEsc {
			t.ansi = append(t.ansi, c)
		}
		switch t.state {
		case stGround:
			switch {
			case c == 0x07:
				// A bell: Terminal answers it with a ripple from the cursor, so it must
				// not reach the line the lens reads.
				if !t.alt {
					t.ansi = t.ansi[:len(t.ansi)-1]
				}
				t.bells.Add(1)
			case c == 0x1b:
				t.state = stEsc
				t.escStart = len(t.ansi) - 1
				t.escActive = true
			case !t.inCommand:
				// Between the marks the screen shows the prompt and the typed line;
				// none of that is the command's output.
			case t.alt:
				// A full-screen program's alternate screen; the terminal
				// restores the main screen when it quits, so there is
				// nothing to add to the command's output.
			case c == '\n':
				t.screen().newline(&t.rowsOut)
				t.drain()
			case c == '\r':
				t.screen().cr()
			case c == '\b':
				t.screen().back()
			case c == '\t':
				t.screen().tab()
			default:
				if c < 0x80 {
					t.screen().put(rune(c), 1)
					continue
				}
				// Decode the rune so a wide (CJK/emoji) cell counts as two.
				r, size := utf8.DecodeRune(b[i:])
				if r == utf8.RuneError && size == 1 {
					size = 1
				}
				t.screen().put(r, parse.RuneWidth(r))
				i += size - 1
			}
		case stEsc:
			switch c {
			case '[':
				t.state = stCSI
			case ']':
				t.osc = t.osc[:0]
				t.state = stOSC
			case '_', 'P', '^', 'X':
				// APC, DCS and the PM/SOS strings carry no command output. The
				// whole sequence, payload included, is taken back out of the
				// styled line; the graphics parser reads the stream separately.
				t.state = stString
			default:
				t.state = stGround // two byte escape, ignored
			}
		case stString:
			switch c {
			case 0x07:
				t.endString()
			case 0x1b:
				t.state = stStringEsc
			}
		case stStringEsc:
			if c == '\\' {
				t.endString()
			} else {
				t.state = stString
			}
		case stCSI:
			if c >= 0x40 && c <= 0x7e {
				t.applyCSI(c)
				t.csi = t.csi[:0]
				t.state = stGround // final byte
			} else {
				t.csi = append(t.csi, c)
			}
		case stOSC:
			switch c {
			case 0x07:
				t.dispatchOSC()
				t.state = stGround
			case 0x1b:
				t.state = stOSCEsc
			default:
				t.osc = append(t.osc, c)
			}
		case stOSCEsc:
			if c == '\\' {
				t.dispatchOSC()
				t.state = stGround
			} else {
				t.osc = append(t.osc, 0x1b, c)
				t.state = stOSC
			}
		}
	}
	// Flush after the chunk as well: the output may share the chunk with the
	// OSC that ends the command, and the store must see the bytes first.
	t.flushRaw()
}

// flushRaw hands the output seen so far to the running block. It runs before
// every OSC is dispatched, so a command's last output is never lost when it
// arrives in the same read as the mark that ends the command.
func (t *Tap) flushRaw() { t.drain() }

// Flush ends a line the program left open (no trailing newline).
func (t *Tap) Flush() { t.flushLine() }

// endString closes an APC/DCS/PM/SOS payload. Every byte of it, including the
// introducer and the terminator, is cut back out of the styled line: the
// picture a program printed is read by the graphics parser, and the block's
// text must not show its base64 or Sixel payload.
func (t *Tap) endString() {
	if t.escActive && t.escStart >= 0 && t.escStart <= len(t.ansi) {
		t.ansi = t.ansi[:t.escStart]
	}
	t.escActive = false
	t.state = stGround
}

func (t *Tap) flushLine() {
	t.screen().flush(&t.rowsOut)
	t.drain()
}

// dispatchOSC handles one complete OSC payload (without ESC ] and terminator).
func (t *Tap) dispatchOSC() {
	t.flushRaw() // the output before the mark belongs to the command it ends
	payload := string(t.osc)
	if payload == "" {
		return
	}
	code, rest, _ := strings.Cut(payload, ";")
	// 133/7/9/7501 are the integration's marks, not the program's output: take
	// them back out of the styled line (the program's colours stay). OSC 1337
	// is an extension namespace: only NatcattyExec is ours; iTerm2 File images
	// must remain available to the graphics parser and block stream.
	switch code {
	case "133", "7", "9", "7501":
		if t.escActive && t.escStart >= 0 && t.escStart <= len(t.ansi) {
			t.ansi = t.ansi[:t.escStart]
		}
	}
	t.escActive = false
	switch code {
	case "1337":
		// A child shell that installed the Natcatty hook announces the same
		// handshake as the parent. The Lens store has one active block, so
		// retire the parent shell-launch command at the child prompt before
		// accepting the child's first OSC 133;C. This keeps the child output
		// in its own block without guessing from prompt text. The Session
		// capture tap has its own nested-shell state; this is only the UI Lens
		// boundary.
		if shell, ok := parseNetcattyHook(rest); ok && t.store.HasRunning() {
			t.flushLine()
			t.store.End(0)
			t.inCommand = false
			t.rowsModel = nil
			t.rowsOut = t.rowsOut[:0]
			t.line = t.line[:0]
			t.ansi = t.ansi[:0]
			t.col = 0
			t.alt = false
			_ = shell
		}
		if cwd, typed, ok := parseNetcattyExec(rest); ok {
			if t.escActive && t.escStart >= 0 && t.escStart <= len(t.ansi) {
				t.ansi = t.ansi[:t.escStart]
			}
			t.pendingCwd = cwd
			t.pendingTyped = typed
		}
	case "133":
		sub, args, _ := strings.Cut(rest, ";")
		switch sub {
		case "C":
			// Nushell emits its own bare OSC 133;C right after the hook's
			// 133;C (the hook's carries no line either, so it started the block
			// from NetcattyExec). Starting a second, empty command there would
			// throw away the output the block is about to receive, so a bare C
			// while a command is already running is that command's mark again.
			if t.inCommand && strings.TrimSpace(args) == "" && t.pendingTyped == "" {
				return
			}
			t.inCommand = true
			t.alt = false
			// A fresh scratch screen; the prompt and the echoed command are not output.
			t.rowsModel = newRowsH(t.screen().cols, captureRows)
			t.rowsOut = t.rowsOut[:0]
			t.line = t.line[:0]
			t.ansi = t.ansi[:0]
			t.col = 0
			parts := strings.SplitN(args, ";", 2)
			typed := decode(parts[0])
			line := typed
			if len(parts) > 1 && parts[1] != "" {
				line = decode(parts[1]) // the alias-expanded line
			}
			if typed == "" && t.pendingTyped != "" {
				typed = t.pendingTyped
				line = typed
			}
			if strings.TrimSpace(line) == "" {
				line = typed // a present but blank expansion falls back to the typed line
			}
			if strings.TrimSpace(line) == "" && strings.TrimSpace(typed) == "" {
				// Enter on an empty prompt: no command ran, so there is no block.
				// Terminal does the same — "Enter on an empty prompt moves the bar and
				// the cursor to the next prompt at once" (changelog 884).
				t.inCommand = false
				t.line = t.line[:0]
				t.ansi = t.ansi[:0]
				t.col = 0
				return
			}
			cwd := t.cwd
			if t.pendingCwd != "" {
				cwd = t.pendingCwd
			}
			t.store.Begin(model.Cmd{Line: line, Typed: typed, Cwd: cwd})
			t.pendingTyped = ""
			t.pendingCwd = ""
		case "D":
			status := 0
			if n, err := strconv.Atoi(strings.TrimSuffix(args, ";")); err == nil {
				status = n
			}
			t.flushLine()
			t.inCommand = false
			t.store.End(status)
		}
	case "7":
		if u := decode(rest); u != "" {
			if parsed, err := url.Parse(u); err == nil && parsed.Path != "" {
				p := parsed.Path
				if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
					p = p[1:] // file://host/C:/work → C:/work
				}
				t.cwd = p
			} else {
				t.cwd = strings.TrimPrefix(u, "file://")
			}
		}
	case "9":
		sub, args, _ := strings.Cut(rest, ";")
		if sub == "4" {
			f := strings.Split(args, ";")
			if len(f) >= 2 {
				if n, err := strconv.Atoi(f[len(f)-1]); err == nil {
					t.Progress = float64(n) / 100
					t.HasProg = f[0] != "0"
				}
			}
		}
	case "7501":
		t.Status = strings.Trim(rest, ";")
	}
	t.store.OSC(code, strings.Split(rest, ";"))
}

func decode(s string) string {
	if s == "" {
		return ""
	}
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func parseNetcattyExec(rest string) (cwd, command string, ok bool) {
	const prefix = "NetcattyExec;"
	if !strings.HasPrefix(rest, prefix) {
		return "", "", false
	}
	fields := strings.Split(rest[len(prefix):], ";")
	if len(fields) != 3 || !validHookShell(fields[0]) {
		return "", "", false
	}
	cwdBytes, err := decodeBase64URL(fields[1])
	if err != nil || !utf8.Valid(cwdBytes) {
		return "", "", false
	}
	commandBytes, err := decodeBase64URL(fields[2])
	if err != nil || len(commandBytes) == 0 || !utf8.Valid(commandBytes) {
		return "", "", false
	}
	return string(cwdBytes), string(commandBytes), true
}

func parseNetcattyHook(rest string) (string, bool) {
	const prefix = "NetcattyHook;"
	if !strings.HasPrefix(rest, prefix) {
		return "", false
	}
	shell := rest[len(prefix):]
	if !validHookShell(shell) {
		return "", false
	}
	return shell, true
}

func decodeBase64URL(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func validHookShell(shell string) bool {
	if shell == "" || len(shell) > 32 {
		return false
	}
	for i := 0; i < len(shell); i++ {
		c := shell[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
