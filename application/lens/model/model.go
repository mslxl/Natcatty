// Package model holds the data the terminal tap, the lens engine and the
// views share: command lines, output lines and the blocks they form.
package model

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// PromptText is the prompt this app draws for a directory: the prompt row above a
// block shows it, outside the block's chrome (Terminal keeps the prompt line a terminal
// row, not part of .sf-block).
func PromptText(dir string) string {
	if dir == "" {
		dir = "~"
	}
	return fmt.Sprintf("PS %s>", dir)
}

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
	LensID  string // "" when no lens claimed the command
	LensErr string
	Native  any // the lens view tree (lens.Node); nil means raw
	State   any // the lens state, opaque here
	Mode    ViewMode
	// Progress is the program's OSC 9;4 progress: nil when it reported none,
	// negative for the indeterminate state.
	Progress *float64
	// StateText is the program's OSC 7501 status ("working", "waiting", …).
	StateText string
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
	nextID  int
	hooks   Hooks
	pending []Line // lines seen before a lens claimed the command

	// lensesOn is Terminal's `command_lenses` setting: when off, a block shows raw
	// output unless it was switched by hand (Block.Mode).
	lensesOn bool
}

// storeSeq numbers the stores so their block ids never collide.
var storeSeq uint64

// NewStore makes an empty store.
func NewStore(hooks Hooks) *Store {
	storeSeq++
	return &Store{uid: storeSeq, hooks: hooks, lensesOn: true}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return
	}
	b := &s.blocks[id]
	b.LensID = lensID
	b.State = state
	b.Native = native
	if err != nil {
		b.LensErr = err.Error()
	} else {
		b.LensErr = ""
	}
	s.rev++
}

// Raw is the captured output text of the block with id, or "" when there is no
// such block.
func (s *Store) Raw(id int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 0 || id >= len(s.blocks) {
		return ""
	}
	return s.blocks[id].Raw
}

// PartialRaw is the output captured so far for the running block with id (the
// block's Raw until it ends), and "" for any other block.
func (s *Store) PartialRaw(id int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.cur.ID != id {
		return ""
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
	s.rev++
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

// End closes the running block with the program's exit status.
func (s *Store) End(status int) {
	s.mu.Lock()
	if s.cur == nil {
		s.mu.Unlock()
		return
	}
	s.cur.Ended = time.Now()
	s.cur.Status = status
	s.cur.Exited = true
	s.cur.Raw = StripANSI(string(s.curRaw))
	s.cur = nil
	s.rev++
	h := s.hooks
	s.mu.Unlock()
	if h != nil {
		h.End(status)
	}
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
		// Two-byte escape.
		i += 2
	}
	return b.String()
}
