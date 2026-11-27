// Package vt taps the terminal's byte stream: it parses the OSC the shell
// integration sends (133 command marks, 7 working directory, 9;4 progress,
// 7501 program status), splits the output into lines, and fills the block
// store. It only observes; the bytes go on to the terminal unchanged.
package vt

import (
	"bytes"
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

	// binBuf accumulates ground-state bytes while the running command is
	// binary-captured (lens.ByteSink). They are flushed to the engine through
	// the store at the end of every chunk (holding back any trailing escape
	// prefix that might be a split OSC 133;D) and in full on command end.
	binBuf []byte
	// binTail keeps the last binTerminatorScanBytes of the binary stream across
	// chunks so an OSC 133;D split across two PTY reads is still recognized and
	// re-emitted whole into the filtered terminal stream.
	binTail []byte
	// binaryCapture caches t.store.BinaryRunning() for the active command so
	// FeedFilter does not lock the store on every byte of a multi-megabyte file.
	binaryCapture bool

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

	// marks records one entry per OSC 133;C the stream delivered: the block
	// that mark started (-1 when it started none: Enter on an empty prompt,
	// Nushell's duplicate mark) and the store epoch at the mark. The terminal
	// flow pairs these in order with the prompt rows its own mirror captures at
	// the same marks, which is how a finished block gets the shell's real
	// prompt.
	marksMu sync.Mutex
	marks   []CommandMark

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

// CommandMark is one OSC 133;C a tap saw: the block it started, and the store
// epoch at that moment. The epoch is read in the tap, before a later OSC 133;D
// in the same read can clear the store and restart block ids, so a consumer
// that keys data by block id can tell which run the id belongs to.
type CommandMark struct {
	ID    int
	Epoch uint64
}

// maxMarks bounds the command-mark queue. The flow drains it after every feed,
// so the cap only matters for a mirror that is never bound (its captures are
// not recorded either, and both queues keep the same tail).
const maxMarks = 4096

// New makes a tap that fills store.
func New(store *model.Store) *Tap { return &Tap{store: store} }

// recordMark appends one OSC 133;C mark: the block it started, or -1.
func (t *Tap) recordMark(id int) {
	mark := CommandMark{ID: id, Epoch: t.store.Epoch()}
	t.marksMu.Lock()
	t.marks = append(t.marks, mark)
	if len(t.marks) > maxMarks {
		t.marks = append([]CommandMark(nil), t.marks[len(t.marks)-maxMarks:]...)
	}
	t.marksMu.Unlock()
}

// TakeCommandMarks returns and clears the OSC 133;C marks recorded since the
// last call, in stream order. The terminal flow pairs them with the prompt rows
// it captured at the same marks.
func (t *Tap) TakeCommandMarks() []CommandMark {
	t.marksMu.Lock()
	defer t.marksMu.Unlock()
	marks := t.marks
	t.marks = nil
	return marks
}

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
	t.FeedFilter(b)
}

// FeedFilter parses a chunk of the program's output and returns the slice of
// bytes that should continue on to the terminal (everything outside an active
// binary capture, plus the complete OSC 133;D mark that ends one). Filtering
// inline with the parser guarantees exact byte alignment with no separate
// offset tracking that can drift across stripped or highlighted streams.
func (t *Tap) FeedFilter(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	var filtered []byte
	filtering := false
	keepFrom := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		// Keep every byte for the styled line; an OSC that turns out to be one of
		// our own marks is truncated back to escStart when it is dispatched. An
		// alternate screen's painting is not kept: it belongs to the program,
		// not to the command's output. A control string's payload is not kept
		// either, byte by byte as it arrives: an image is read in many reads,
		// and the buffer is emptied after each of them, so remembering the
		// introducer's offset alone could never cut the payload back out.
		if !t.alt && !t.binaryCapture && t.state != stString && t.state != stStringEsc {
			t.ansi = append(t.ansi, c)
		}
		switch t.state {
		case stGround:
			switch {
			case t.inCommand && !t.alt && t.binaryCapture:
				// A binary capture (lens.ByteSink): every byte is the binary
				// payload, including the control characters and the escape
				// introducer. High-entropy payload produces \x1b bytes often
				// enough that the regular escape machine would swallow whole
				// APC/OSC/CSI sequences over them and lose the picture, so the
				// only sequence this mode recognizes is the exact end-of-command
				// mark — OSC 133;D plus its terminator — scanned for with a
				// sliding match over binTail across chunks.
				if !filtering {
					filtering = true
					filtered = make([]byte, 0, len(b))
				}
				if keepFrom < i {
					filtered = append(filtered, b[keepFrom:i]...)
				}
				keepFrom = i + 1
				t.binBuf = append(t.binBuf, c)
				t.binTail = append(t.binTail, c)
				if len(t.binTail) > binTerminatorScanBytes {
					copy(t.binTail, t.binTail[len(t.binTail)-binTerminatorScanBytes:])
					t.binTail = t.binTail[:binTerminatorScanBytes]
				}
				// The terminator can only complete on a BEL or a backslash —
				// anything else is payload, and scanning the tail on every
				// payload byte is waste on a large file.
				if c == 0x07 || c == '\\' {
					if status, termLen, done := matchBinaryTerminator(t.binTail); done {
						// Re-emit the complete OSC 133;D mark into the filtered
						// stream so downstream consumers (session tap, bell
						// mirror, visible terminal) close the command cleanly
						// even when the mark arrived split across two reads.
						filtered = append(filtered, t.binTail[len(t.binTail)-termLen:]...)
						if len(t.binBuf) >= termLen {
							t.binBuf = t.binBuf[:len(t.binBuf)-termLen]
						} else {
							t.binBuf = t.binBuf[:0]
						}
						t.flushBinaryAll()
						t.binTail = t.binTail[:0]
						t.inCommand = false
						t.binaryCapture = false
						t.store.End(status)
					}
				}
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
	if t.binaryCapture {
		t.flushBinarySafe()
	} else {
		t.flushRaw()
	}
	if !filtering {
		return b
	}
	if keepFrom < len(b) {
		filtered = append(filtered, b[keepFrom:]...)
	}
	return filtered
}

// flushBinarySafe hands captured binary bytes to the store while holding back
// any trailing escape sequence inside the last binTerminatorScanBytes bytes so
// an OSC 133;D split across two reads is never fed into the lens payload and
// can be stripped cleanly when its terminator arrives.
func (t *Tap) flushBinarySafe() {
	if len(t.binBuf) == 0 {
		return
	}
	scanStart := len(t.binBuf) - binTerminatorScanBytes
	if scanStart < 0 {
		scanStart = 0
	}
	flushEnd := len(t.binBuf)
	if idx := bytes.IndexByte(t.binBuf[scanStart:], 0x1b); idx >= 0 {
		flushEnd = scanStart + idx
	}
	if flushEnd > 0 {
		t.store.AppendBytes(t.binBuf[:flushEnd])
		remaining := len(t.binBuf) - flushEnd
		copy(t.binBuf, t.binBuf[flushEnd:])
		t.binBuf = t.binBuf[:remaining]
	}
}

// flushBinaryAll hands every remaining captured binary byte to the store.
func (t *Tap) flushBinaryAll() {
	if len(t.binBuf) > 0 {
		t.store.AppendBytes(t.binBuf)
		t.binBuf = t.binBuf[:0]
	}
}

// flushRaw hands the output seen so far to the running block. It runs before
// every OSC is dispatched, so a command's last output is never lost when it
// arrives in the same read as the mark that ends the command.
func (t *Tap) flushRaw() {
	t.flushBinaryAll()
	t.drain()
}

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
			t.binaryCapture = false
			t.binTail = t.binTail[:0]
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
			// Every C the stream delivers gets one mark, even when it starts no
			// block: the terminal flow records one prompt-row capture per C
			// from its own mirror, and the two queues are paired in order.
			markID := -1
			defer func() { t.recordMark(markID) }()
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
			typed, line := parseCommandLine(args)
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
			markID = t.store.Begin(model.Cmd{Line: line, Typed: typed, Cwd: cwd})
			t.binaryCapture = t.store.BinaryRunning()
			t.binTail = t.binTail[:0]
			t.pendingTyped = ""
			t.pendingCwd = ""
		case "D":
			status := 0
			if n, err := strconv.Atoi(strings.TrimSuffix(args, ";")); err == nil {
				status = n
			}
			t.flushLine()
			t.inCommand = false
			t.binaryCapture = false
			t.binTail = t.binTail[:0]
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

// binTerminatorScanBytes bounds how far back matchBinaryTerminator looks in
// the captured payload. It covers "\x1b]133;D;NNNNN;aid=...\x1b\\" with ample
// room without making the scan cost grow with the payload.
const binTerminatorScanBytes = 64

// matchBinaryTerminator reports whether the captured payload ends with the
// shell integration's end-of-command mark, OSC 133;D. The regular escape
// machine cannot be trusted inside binary capture, so this scans the tail of
// the payload for the exact byte sequence: ESC ] 133 ; D, an optional ;status
// of ASCII digits, optional ;key=val printable parameters, then BEL or ESC \.
// It returns the parsed exit status (zero when the mark carries none), the byte
// length of the matched mark, and whether the terminator is complete.
func matchBinaryTerminator(buf []byte) (status, termLen int, done bool) {
	start := len(buf) - binTerminatorScanBytes
	if start < 0 {
		start = 0
	}
	tail := buf[start:]
	// Find the last "\x1b]133;D" in the tail; everything after it must be the
	// optional status, optional parameters, and the terminator.
	idx := -1
	for i := len(tail) - len("\x1b]133;D"); i >= 0; i-- {
		if tail[i] == 0x1b && string(tail[i:i+len("\x1b]133;D")]) == "\x1b]133;D" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, 0, false
	}
	rest := tail[idx+len("\x1b]133;D"):]
	// Optional ";digits".
	if len(rest) > 0 && rest[0] == ';' {
		j := 1
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			status = status*10 + int(rest[j]-'0')
			j++
		}
		// Allow ";aid=..." (no numeric status) or ";0;aid=..." (status + params)
		// or ";0;" (trailing semicolon), as long as every parameter byte is
		// printable ASCII (0x20..0x7e) so binary noise cannot masquerade as params.
		if j == 1 && (len(rest) <= 1 || rest[1] == 0x07 || rest[1] == 0x1b) {
			return 0, 0, false // bare ";" with neither digits nor params
		}
		rest = rest[j:]
		if len(rest) > 0 && rest[0] == ';' {
			k := 1
			for k < len(rest) && rest[k] >= 0x20 && rest[k] <= 0x7e {
				k++
			}
			rest = rest[k:]
		} else if j == 1 {
			// Started with ";" followed by non-digit printable param (e.g. ";aid=1").
			k := 0
			for k < len(rest) && rest[k] >= 0x20 && rest[k] <= 0x7e {
				k++
			}
			rest = rest[k:]
		}
	}
	// The terminator proper: BEL, or ESC \. Either way, rest must be exactly
	// the terminator.
	switch {
	case len(rest) == 1 && rest[0] == 0x07:
		return status, len(tail) - idx, true
	case len(rest) == 2 && rest[0] == 0x1b && rest[1] == '\\':
		return status, len(tail) - idx, true
	}
	return 0, 0, false
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

// parseCommandLine reads the command line a start-of-command mark carries. It
// returns the line as the user typed it and the line with aliases expanded (the
// same when there is none).
//
// The form is the terminal-agnostic one kitty's shell integration uses, so a
// shell script written for any terminal speaks it:
//
//	cmdline=<word>                          bash, and zsh with no alias match
//	cmdline=<word>;cmdline_expanded=<word>  zsh, when an alias expanded
//	cmdline_url=<pct-encoded>               fish, PowerShell, nushell, xonsh
//
// Older sessions of this terminal sent the line separately, in
// OSC 1337;NetcattyExec, and left the mark bare; a mark that carries neither
// key falls back to that layout, where the two fields are base64 text.
func parseCommandLine(args string) (typed, line string) {
	if strings.TrimSpace(args) == "" {
		return "", ""
	}
	var quoted, quotedExpanded string
	var haveQuoted, haveURL bool
	for _, field := range splitMarkFields(args) {
		// Only the mark's own keys count. A bare base64 word can contain '=' —
		// the padding — and must not be read as a key.
		var key string
		switch {
		case strings.HasPrefix(field, "cmdline_url="):
			key = "cmdline_url"
		case strings.HasPrefix(field, "cmdline_expanded="):
			key = "cmdline_expanded"
		case strings.HasPrefix(field, "cmdline="):
			key = "cmdline"
		default:
			continue
		}
		value := field[len(key)+1:]
		switch key {
		case "cmdline":
			quoted, haveQuoted = value, true
		case "cmdline_expanded":
			quotedExpanded = value
		case "cmdline_url":
			if u, err := url.QueryUnescape(value); err == nil {
				typed, line = u, u
				haveURL = true
			}
		}
	}
	if haveURL {
		if strings.TrimSpace(quotedExpanded) != "" {
			if e, err := url.QueryUnescape(quotedExpanded); err == nil {
				line = e
			}
		}
		if strings.TrimSpace(quoted) != "" {
			if t := unquoteShellWord(quoted); strings.TrimSpace(t) != "" {
				typed = t
			}
		}
		return typed, line
	}
	if haveQuoted {
		typed = unquoteShellWord(quoted)
		line = typed
		if strings.TrimSpace(quotedExpanded) != "" {
			if e := unquoteShellWord(quotedExpanded); strings.TrimSpace(e) != "" {
				line = e
			}
		}
		return typed, line
	}
	// No keys: the two-field layout of the earlier mark, where each field is
	// percent-encoded (the same encoding the mark has always used for these).
	parts := strings.SplitN(args, ";", 2)
	typed = decode(parts[0])
	line = typed
	if len(parts) > 1 && parts[1] != "" {
		line = decode(parts[1])
	}
	return typed, line
}

// splitMarkFields splits a mark's parameters on ';', keeping a ';' that belongs
// to a quoted word: a command line like `sh -c 'a; b'` arrives shell-quoted, so
// the quoted regions are skipped over.
func splitMarkFields(args string) []string {
	var fields []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(args) {
				i++
				cur.WriteByte(args[i])
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(args):
			i++
			cur.WriteByte(args[i])
		case c == ';':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	fields = append(fields, cur.String())
	return fields
}

// unquoteShellWord undoes the shell quoting a reported command line carries. zsh
// reports it with its `%q` form (backslash-escaped words), bash and the others
// with single quotes, and PowerShell and fish send a percent-encoded line
// instead (handled before this). A word with no quoting is returned as is.
func unquoteShellWord(word string) string {
	word = strings.TrimSpace(word)
	if word == "" {
		return ""
	}
	if len(word) >= 2 {
		if (word[0] == '\'' && word[len(word)-1] == '\'') ||
			(word[0] == '"' && word[len(word)-1] == '"') {
			inner := word[1 : len(word)-1]
			if word[0] == '\'' {
				return inner
			}
			// Double quotes keep only these escapes; any other backslash is
			// literal, as the shell reads it.
			var b strings.Builder
			for i := 0; i < len(inner); i++ {
				if inner[i] == '\\' && i+1 < len(inner) {
					switch inner[i+1] {
					case '"', '\\', '$', '`':
						b.WriteByte(inner[i+1])
						i++
						continue
					}
				}
				b.WriteByte(inner[i])
			}
			return b.String()
		}
	}
	// %q form: spaces and specials are backslash-escaped. A backslash before a
	// character only ever escapes that character, so dropping it is exact.
	var b strings.Builder
	for i := 0; i < len(word); i++ {
		if word[i] == '\\' && i+1 < len(word) {
			i++
			b.WriteByte(word[i])
			continue
		}
		b.WriteByte(word[i])
	}
	return b.String()
}

// parseNetcattyExec reads the hook's private command marker: the working
// directory and the line as typed, both base64url. The shell the marker names
// is validated but not returned: the prompt row is the shell's own drawing,
// captured from the screen, so its name is not needed to style anything.
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
