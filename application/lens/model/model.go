// Package model holds the data the terminal tap, the lens engine and the
// views share: command lines, output lines and the blocks they form.
package model

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Link is an OSC 8 hyperlink inside a line's text, as byte offsets.
type Link struct {
	From, To int
	Href     string
}

// Line is one line of a command's output, escapes applied.
type Line struct {
	Text string
	// Styled is the same line with the program's SGR escapes kept, the way
	// Terminal's LensLine arrives with "escapes applied" (flow.exe strings
	// 801257-801258). It is empty when the line carried no escapes.
	Styled string
	Links  []Link
}

// Cmd is a command line as the shell reported it (OSC 133;C).
type Cmd struct {
	Line    string // the line, alias-expanded when the shell reports it
	Typed   string // the line as typed
	Cwd     string
	Program string   // after wrappers, VAR= prefixes and basename
	Args    []string // the arguments after the program
}

// ViewMode is how a block shows: the lens view (native) or the raw output.
type ViewMode int

// Image is a picture a command printed as inline graphics (Kitty APC, iTerm2
// OSC 1337 File, Sixel). Data holds the PNG bytes the graphics parser decoded;
// Path and Alt name it when the source had a name. It is a model type rather
// than lens.Image because the lens package imports this one.
type Image struct {
	Data []byte
	Path string
	Alt  string
}

const (
	ViewAuto ViewMode = iota
	ViewNative
	ViewRaw
)

// Block is one command and its output, the unit Terminal calls a block.
type Block struct {
	ID      int
	Cmd     Cmd
	Started time.Time
	Ended   time.Time
	Status  int
	Exited  bool
	Raw     string // the output as the program wrote it, escapes stripped
	// rawBytes holds the complete raw byte stream of a finished binary-captured
	// block until Raw is first requested (switching to Raw view or Copy), so
	// Native image blocks pay zero formatting cost while preserving every byte.
	rawBytes []byte
	LensID   string // "" when no lens claimed the command
	LensErr  string
	Native   any // the lens view tree (lens.Node); nil means raw
	State    any // the lens state, opaque here
	Mode     ViewMode
	// Progress is the program's OSC 9;4 progress: nil when it reported none,
	// negative for the indeterminate state.
	Progress *float64
	// StateText is the program's OSC 7501 status ("working", "waiting", …).
	StateText string
	// Images are the inline pictures the command printed. They belong to the
	// block, so they stay in the flow with its output instead of floating
	// above the live screen until the buffer is cleared.
	Images []Image
	// Binary marks a command whose lens asked for the raw byte stream (see
	// lens.ByteSink). The tap checks it on the running block: a binary block
	// does not have its bytes run through the screen and split into rows,
	// because that pipeline is meaningless for binary output and floods the
	// store with garbage rows.
	Binary bool
	// Picture marks a block whose native view is a picture. The store cannot
	// inspect the view tree (the lens package owns those types, and importing
	// them here would be a cycle), so the engine sets this when it records a
	// view: a picture's raw view is binary noise, which is what makes replacing
	// it worth confirming.
	Picture bool
}

// showsPicture reports whether the block's native view is a picture, which is
// what makes its raw view destructive enough to confirm first.
func (b *Block) showsPicture() bool { return b.Picture }

// MarkBinary flags the block with id as binary-captured. The engine calls it
// right after Open when the lens asked for raw bytes.
func (s *Store) MarkBinary(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	s.blocks[id].Binary = true
	s.rev++
}

// BinaryRunning reports whether the currently running block is binary-captured.
// The tap reads it on every output byte, so it is cheap: a single locked load.
func (s *Store) BinaryRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.Binary
}

// SetProgress records the running block's OSC 9;4 progress.
func (s *Store) SetProgress(id int, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	v := value
	s.blocks[id].Progress = &v
	s.rev++
}

// SetStateText records the running block's OSC 7501 program status.
func (s *Store) SetStateText(id int, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	s.blocks[id].StateText = text
	s.rev++
}

// AddImage records an inline picture the block with id printed. Data is copied:
// the graphics parser may reuse its buffers, while Snapshot shares the slice
// and callers must treat it as immutable, as they do Native and Raw.
func (s *Store) AddImage(id int, img Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	cp := img
	cp.Data = append([]byte(nil), img.Data...)
	s.blocks[id].Images = append(s.blocks[id].Images, cp)
	s.rev++
}

// View reports the mode the block shows in: a manual choice wins, else the
// lens decides (native when it built a view).
// View is the block's own choice: a manual switch, else native when the lens built
// a view, else raw. It does not know about the global `command_lenses` setting;
// use Store.EffectiveView for what is actually drawn (the two disagreed before).
func (b *Block) View() ViewMode {
	if b.Mode != ViewAuto {
		return b.Mode
	}
	if b.Native != nil {
		return ViewNative
	}
	return ViewRaw
}

// EffectiveView is what the block actually shows: a manual choice always wins,
// then Terminal's `command_lenses` setting, then "native when the lens built a view".
// The renderer and the header both use this, so there is one definition.
func (s *Store) EffectiveView(b *Block) ViewMode {
	s.mu.Lock()
	on := s.lensesOn
	s.mu.Unlock()
	if b.Mode != ViewAuto {
		return b.Mode
	}
	if !on {
		return ViewRaw
	}
	if b.Native != nil {
		return ViewNative
	}
	return ViewRaw
}

// Elapsed is how long the command ran.
func (b *Block) Elapsed() time.Duration {
	if b.Started.IsZero() {
		return 0
	}
	end := b.Ended
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(b.Started)
}

// Hooks receive what the tap sees, in order. The lens engine implements them.
type Hooks interface {
	Begin(cmd Cmd)
	Line(l Line)
	End(status int)
	OSC(code string, args []string)
}

// Store keeps the blocks of one terminal and feeds the hooks.
type Store struct {
	mu      sync.Mutex
	uid     uint64
	blocks  []Block
	cur     *Block
	curRaw  []byte
	rev     uint64
	epoch   uint64
	nextID  int
	hooks   Hooks
	pending []Line // lines seen before a lens claimed the command

	// lensesOn is Terminal's `command_lenses` setting: when off, a block shows raw
	// output unless it was switched by hand (Block.Mode).
	lensesOn bool

	// pendingRaw is the block whose switch to the raw view waits for the user's
	// confirmation (see RequestRaw), or -1. A picture's raw view is binary
	// noise, so replacing the picture with it is asked about first.
	pendingRaw int
}

// storeSeq numbers the stores so their block ids never collide.
var storeSeq uint64

// NewStore makes an empty store.
func NewStore(hooks Hooks) *Store {
	storeSeq++
	return &Store{uid: storeSeq, hooks: hooks, lensesOn: true, pendingRaw: -1}
}

// LensesOn reports whether command lenses may claim new commands. When it is
// off, a recognized command is not claimed and prints cells (no .sf-block);
// blocks already on screen keep their lens, so the setting can still switch them
// between native and raw. This is Terminal's `command_lenses` setting ("Native
// command output"), whose toggle also switches blocks already on screen.
func (s *Store) LensesOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lensesOn
}

// SetLenses turns command lenses on or off for every block that has no manual
// choice of its own.
func (s *Store) SetLenses(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lensesOn == on {
		return
	}
	s.lensesOn = on
	s.rev++
}

// Revision grows whenever the blocks change; views snapshot on change.
func (s *Store) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev
}

// LastID is the id of the block added last, or -1 when there is none. A hook
// called from Begin sees the new block's id here.
func (s *Store) LastID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.blocks) == 0 {
		return -1
	}
	return s.blocks[len(s.blocks)-1].ID
}

// HasRunning reports whether the terminal tap currently owns an unfinished
// command block. A shell integration handshake from a child interactive shell
// uses this to retire the parent shell-launch command before the child starts
// reporting its own commands.
func (s *Store) HasRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

// Snapshot returns a copy of the block list. The caller may replace a Block, its
// ID/Raw/Status fields, Cmd.Args and Progress without touching the store (Args and
// Progress are deep-copied). The lens view (Native/State) and the Raw string are
// shared by design: they are immutable once written, and copying an arbitrary
// view tree on every frame would cost more than it protects.
func (s *Store) Snapshot() []Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Block, len(s.blocks))
	copy(out, s.blocks)
	// Deep-copy the mutable parts: callers may edit the copy, and a shallow copy
	// would let them change the store's command args or progress ring.
	for i := range out {
		if out[i].Cmd.Args != nil {
			args := make([]string, len(out[i].Cmd.Args))
			copy(args, out[i].Cmd.Args)
			out[i].Cmd.Args = args
		}
		if out[i].Progress != nil {
			v := *out[i].Progress
			out[i].Progress = &v
		}
	}
	return out
}

// SetNative records the lens view for the block with id.
func (s *Store) SetNative(id int, lensID string, state, native any, err error) {
	s.setNative(id, lensID, state, native, err, false)
}

// SetNativePicture is SetNative for a view that shows a picture: it also marks
// the block, so a switch to the raw view (a wall of image bytes) is confirmed
// first. The engine knows the view tree and the store does not, so the fact
// travels with the call instead of being re-derived here.
func (s *Store) SetNativePicture(id int, lensID string, state, native any, err error) {
	s.setNative(id, lensID, state, native, err, true)
}

func (s *Store) setNative(id int, lensID string, state, native any, err error, picture bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	b := &s.blocks[id]
	b.LensID = lensID
	b.State = state
	b.Native = native
	b.Picture = picture
	if err != nil {
		b.LensErr = err.Error()
	} else {
		b.LensErr = ""
	}
	s.rev++
}

// Raw is the captured output text of the block with id, or "" when there is no
// such block. For a binary-captured block, the complete raw byte stream is
// formatted on first demand and cached on the block.
func (s *Store) Raw(id int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return ""
	}
	b := &s.blocks[id]
	if b.Binary && b.Exited && b.Raw == "" && len(b.rawBytes) > 0 {
		b.Raw = FormatBinaryRaw(b.rawBytes)
		b.rawBytes = nil
		s.rev++
	}
	return b.Raw
}

// PartialRaw is the output captured so far for the running block with id (the
// block's Raw until it ends), and "" for any other block.
func (s *Store) PartialRaw(id int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.cur.ID != id {
		return ""
	}
	if s.cur.Binary {
		return FormatBinaryRaw(s.curRaw)
	}
	return string(s.curRaw)
}

// SetCmd records the normalized command on the block with id.
func (s *Store) SetCmd(id int, cmd Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	s.blocks[id].Cmd = cmd
	s.rev++
}

// SetMode records a manual native/raw choice for the block with id.
func (s *Store) SetMode(id int, mode ViewMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	s.blocks[id].Mode = mode
	// A mode the user just made is no longer a pending switch: an accepted
	// Raw, or a switch back to Native, settles the question.
	if s.pendingRaw == id {
		s.pendingRaw = -1
	}
	s.rev++
}

// PendingRaw is the id of the block whose switch to the raw view is waiting for
// the user's confirmation, or -1 when nothing waits.
//
// The raw view of a picture block is a wall of binary, and the picture it
// replaces is the reason the block exists: `cat photo.jpg` is readable as a
// thumbnail and unreadable as bytes. The switch is offered, not taken: asking
// first keeps a stray click on the toolbar from destroying the view, and the
// confirmation names what the raw view will be so the choice is informed.
func (s *Store) PendingRaw() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingRaw
}

// RequestRaw asks for the raw view of the block with id. A block whose view is
// a picture does not switch yet: it is put aside as PendingRaw until ConfirmRaw
// or CancelRaw. Every other block switches at once, because its raw view is the
// text it printed and nothing is lost.
//
// It reports whether the switch happened right away.
func (s *Store) RequestRaw(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return false
	}
	if s.blocks[id].showsPicture() {
		s.pendingRaw = id
		s.rev++
		return false
	}
	s.blocks[id].Mode = ViewRaw
	if s.pendingRaw == id {
		s.pendingRaw = -1
	}
	s.rev++
	return true
}

// ConfirmRaw takes the pending switch to the raw view, and reports whether one
// was waiting.
func (s *Store) ConfirmRaw() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.pendingRaw
	if id < 0 || id >= len(s.blocks) {
		s.pendingRaw = -1
		return false
	}
	s.blocks[id].Mode = ViewRaw
	s.pendingRaw = -1
	s.rev++
	return true
}

// CancelRaw drops the pending switch, leaving the picture on screen.
func (s *Store) CancelRaw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingRaw < 0 {
		return
	}
	s.pendingRaw = -1
	s.rev++
}

// Clear drops every block.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = nil
	s.cur = nil
	s.curRaw = nil
	s.nextID = 0
	s.epoch++
	s.rev++
}

// Epoch counts Clear calls. Block ids restart at zero after a Clear, so data a
// caller keeps per block id (a block's captured prompt rows, say) must be
// dropped when the epoch it was captured under no longer matches.
func (s *Store) Epoch() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch
}

// UID identifies this store's run: block ids restart at zero after Clear, so the
// renderer combines the uid with the id and the block's start time to key
// per-block UI state.
func (s *Store) UID() uint64 { return s.uid }

// Block returns the block with this id, or nil. The pointer stays valid until the
// block list grows; callers that only read one field are safe (the UI mutex holds
// the render).
func (s *Store) Block(id int) *Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return nil
	}
	return &s.blocks[id]
}

// Begin starts a block for a command the shell reported.
func (s *Store) Begin(cmd Cmd) int {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	// Copy the caller's Args: a shell-side slice must not be able to change a
	// block after it started (Snapshot copies it back out for the same reason).
	if cmd.Args != nil {
		args := make([]string, len(cmd.Args))
		copy(args, cmd.Args)
		cmd.Args = args
	}
	s.blocks = append(s.blocks, Block{
		ID:      id,
		Cmd:     cmd,
		Started: time.Now(),
	})
	s.cur = &s.blocks[len(s.blocks)-1]
	s.curRaw = s.curRaw[:0]
	s.pending = s.pending[:0]
	s.rev++
	h := s.hooks
	s.mu.Unlock()
	if h != nil {
		h.Begin(cmd)
	}
	return id
}

// Append adds output bytes to the running block.
func (s *Store) Append(b []byte) {
	s.mu.Lock()
	if s.cur != nil {
		s.curRaw = append(s.curRaw, b...)
	}
	s.mu.Unlock()
}

// AddLine hands one parsed output line to the hooks and the raw buffer.
func (s *Store) AddLine(l Line) {
	s.mu.Lock()
	if s.cur == nil {
		s.mu.Unlock()
		return
	}
	s.pending = append(s.pending, l)
	s.rev++
	h := s.hooks
	s.mu.Unlock()
	if h != nil {
		h.Line(l)
	}
}

// binaryRawWrapCols wraps long runs of binary output that have no newlines,
// matching how a terminal wraps raw binary bytes onto screen rows.
const binaryRawWrapCols = 120

// AppendBytes hands a chunk of raw output bytes to the hooks of a binary
// block, and retains the complete raw byte stream in curRaw so switching the
// block to Raw shows every byte of the command's output like a normal terminal.
func (s *Store) AppendBytes(b []byte) {
	s.mu.Lock()
	if s.cur == nil || !s.cur.Binary {
		s.mu.Unlock()
		return
	}
	s.curRaw = append(s.curRaw, b...)
	h := s.hooks
	s.mu.Unlock()
	if h == nil {
		return
	}
	// Bytes is the binary counterpart of Line; the hooks interface predates
	// binary capture, so it is reached through a type assertion rather than a
	// new method on it.
	if be, ok := h.(interface{ Bytes([]byte) }); ok {
		be.Bytes(b)
	}
}

// End closes the running block with the program's exit status.
func (s *Store) End(status int) {
	s.mu.Lock()
	if s.cur == nil {
		s.mu.Unlock()
		return
	}
	binary := s.cur.Binary
	s.cur.Ended = time.Now()
	s.cur.Status = status
	s.cur.Exited = true
	if binary {
		// Transfer ownership of the complete raw byte slice without copying or
		// eagerly formatting: Native view stays instant, and Raw() formats the
		// entire stream on demand if the user switches to Raw or copies it.
		s.cur.rawBytes = s.curRaw
		s.curRaw = nil
	} else {
		s.cur.Raw = StripANSI(string(s.curRaw))
	}
	s.cur = nil
	s.rev++
	h := s.hooks
	s.mu.Unlock()
	if h != nil {
		h.End(status)
	}
}

// FormatBinaryRaw renders raw binary bytes the way a terminal prints them:
// escape sequences stripped, invalid UTF-8 bytes replaced with U+FFFD,
// non-printable C0/C1 control codes dropped (preserving newlines and tabs),
// and long newline-less runs wrapped to binaryRawWrapCols.
func FormatBinaryRaw(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	cleaned := StripANSI(string(data))
	var b strings.Builder
	b.Grow(len(cleaned))
	col := 0
	for i := 0; i < len(cleaned); {
		r, size := utf8.DecodeRuneInString(cleaned[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			col++
		} else if r == '\n' {
			b.WriteByte('\n')
			col = 0
			continue
		} else if r == '\t' {
			b.WriteByte('\t')
			col += 4
		} else if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		} else {
			b.WriteRune(r)
			col++
		}
		if col >= binaryRawWrapCols {
			b.WriteByte('\n')
			col = 0
		}
	}
	return b.String()
}

// OSC hands a non-133 OSC to the hooks.
func (s *Store) OSC(code string, args []string) {
	s.mu.Lock()
	h := s.hooks
	s.mu.Unlock()
	if h != nil {
		h.OSC(code, args)
	}
}

// stripANSI removes escape sequences so the raw view reads as text.
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			i++
			continue
		}
		// ESC [ ... final byte in @..~
		if i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		// ESC ] ... BEL or ESC \
		if i+1 < len(s) && s[i+1] == ']' {
			j := i + 2
			for j < len(s) && s[j] != 0x07 {
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		// APC (ESC _), DCS (ESC P), PM (ESC ^) and SOS (ESC X) ... BEL or ESC \:
		// an inline picture's payload (Kitty graphics, Sixel) is not text and
		// must not reach a block's output.
		if i+1 < len(s) && (s[i+1] == '_' || s[i+1] == 'P' || s[i+1] == '^' || s[i+1] == 'X') {
			j := i + 2
			for j < len(s) && s[j] != 0x07 {
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j++
					break
				}
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		// Two-byte escape.
		i += 2
	}
	return b.String()
}
