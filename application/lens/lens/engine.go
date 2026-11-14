package lens

import (
	"strconv"
	"strings"
	"time"

	"github.com/mslxl/natcatty/application/lens/lens/parse"
	"github.com/mslxl/natcatty/application/lens/model"
)

// Engine drives the lenses from what the terminal tap sees: it normalizes a
// reported command, finds the lens that claims it, feeds the lens the output
// lines, and stores the view it builds on the block.
type Engine struct {
	store *model.Store
	cur   *run
	cx    *Ctx
}

type run struct {
	id    int
	lens  Lens
	state any
	clear bool
	cmd   model.Cmd

	// lines/lastView throttle the live rebuild of the view: Terminal's native blocks
	// update while the command runs instead of staying raw until it ends.
	lines    int
	lastView time.Time
}

// liveViewEvery is how many output lines may pass between live view rebuilds,
// and liveViewMin is the shortest interval between them.
const (
	liveViewEvery = 8
	liveViewMin   = 80 * time.Millisecond
)

// clearers are the commands that empty the screen: their block empties the
// block list too, as Terminal's `clear` does, instead of lingering as an empty
// card.
var clearers = map[string]bool{
	"clear": true, "cls": true, "clear-host": true, "clearhost": true,
}

func isClear(cmd model.Cmd) bool {
	if len(cmd.Args) > 0 {
		return false
	}
	return clearers[strings.ToLower(cmd.Program)]
}

// NewEngine makes an engine; Attach gives it the store it fills.
func NewEngine() *Engine {
	return &Engine{cx: &Ctx{Logf: func(string, ...any) {}}}
}

// Attach binds the engine to the store whose hooks it is.
func (e *Engine) Attach(s *model.Store) { e.store = s }

// ByID returns a fresh lens with that id, or nil.
func ByID(id string) Lens {
	for _, d := range registry {
		if d.ID == id {
			return d.New()
		}
	}
	return nil
}

// Begin claims a command when a lens matches it.
func (e *Engine) Begin(cmd model.Cmd) {
	match := Normalize(cmd.Line, cmd.Typed, cmd.Cwd)
	// The prompt row shows what the user typed (an alias stays an alias), while
	// the lens is chosen from the parsed command.
	display := match
	if cmd.Typed != "" && cmd.Typed != cmd.Line {
		display = Normalize(cmd.Typed, cmd.Typed, cmd.Cwd)
	}
	id := e.store.LastID()
	e.store.SetCmd(id, display) // the block always carries a normalized command
	if isClear(match) {
		e.cur = &run{id: id, clear: true}
		return
	}
	// Terminal's setting is the gate for the whole lens path: with "Native command
	// output" off, a recognized command is not claimed and prints cells. Blocks
	// already on screen keep their lens, so the setting can still switch them.
	if !e.store.LensesOn() {
		e.cur = nil
		return
	}
	// Terminal's gate: a pipeline, sequence, background job or redirection is not a
	// simple command, so no lens takes it over.
	reported := cmd.Line
	if reported == "" {
		reported = cmd.Typed
	}
	if !IsSimple(reported) {
		e.cur = nil
		return
	}
	l := Lookup(match)
	if l == nil && cmd.Typed != "" && cmd.Typed != cmd.Line {
		// The shell expanded an alias to something no lens knows (PowerShell's
		// `pwd` is `Get-Location`, `echo` is `Write-Output`): match the typed form
		// instead, so the alias still opens its command's view (Terminal: "Aliases
		// such as gst open the same native views as their commands").
		if alt := Normalize(cmd.Typed, cmd.Typed, cmd.Cwd); Lookup(alt) != nil {
			match, l = alt, Lookup(alt)
		}
	}
	if l == nil {
		e.cur = nil
		return
	}
	st, err := l.Open(match, e.cx)
	e.cur = &run{id: id, lens: l, state: st, cmd: match}
	if err != nil {
		e.store.SetNative(id, l.ID(), st, nil, err)
		e.cur = nil
		return
	}
	e.store.SetNative(id, l.ID(), st, nil, nil)
}

// Line feeds one output line to the running lens. The view is rebuilt every few
// lines so a running block already shows native content, the way Terminal's blocks
// "show their latest lines" while the command runs.
func (e *Engine) Line(l model.Line) {
	if e.cur == nil || e.cur.lens == nil {
		return
	}
	if err := e.cur.lens.Line(e.cur.state, l); err != nil {
		e.store.SetNative(e.cur.id, e.cur.lens.ID(), e.cur.state, nil, err)
		e.cur = nil
		return
	}
	e.cur.lines++
	if e.cur.lines%liveViewEvery != 0 && time.Since(e.cur.lastView) < liveViewMin {
		return
	}
	e.cur.lastView = time.Now()
	if node, err := e.cur.lens.View(e.cur.state); err == nil && node != nil {
		e.store.SetNative(e.cur.id, e.cur.lens.ID(), e.cur.state, node, nil)
	}
}

// End finishes the running lens and stores the view it built; a nil view (or
// an error) leaves the block showing raw output, as in Terminal.
func (e *Engine) End(status int) {
	if e.cur == nil {
		return
	}
	r := e.cur
	e.cur = nil
	if r.clear {
		e.store.Clear() // `clear` empties the screen and the blocks
		return
	}
	if err := r.lens.Finish(r.state, status); err != nil {
		e.store.SetNative(r.id, r.lens.ID(), r.state, nil, err)
		return
	}
	node, err := r.lens.View(r.state)
	if err != nil {
		e.store.SetNative(r.id, r.lens.ID(), r.state, nil, err)
		return
	}
	// A lens that produced no view still gets Terminal's shared JSON parser when the
	// command asked for JSON: `parse.json_tree` renders it, the same parser the
	// built-in lenses use (flow.exe strings line 802657-802668).
	if node == nil && (parse.HasJSONFlag(r.cmd.Line) || emitsJSON(r.cmd)) {
		if tree, ok := JSONTree(e.store.Raw(r.id)); ok {
			node = tree
		}
	}
	e.store.SetNative(r.id, r.lens.ID(), r.state, node, err)
}

// OSC records the program's own progress and status on the running block, as
// Terminal shows them on the block chrome: OSC 9;4 progress (state 3 is
// indeterminate) and OSC 7501 program status.
func (e *Engine) OSC(code string, args []string) {
	id := e.store.LastID()
	switch code {
	case "9":
		if len(args) < 2 || args[0] != "4" {
			return
		}
		state := args[1]
		switch state {
		case "3": // indeterminate: no percentage is sent
			e.store.SetProgress(id, -1)
			return
		case "0": // cleared: keep the last value
			return
		}
		if len(args) < 3 {
			return
		}
		pct, err := strconv.Atoi(args[2])
		if err != nil {
			return
		}
		e.store.SetProgress(id, float64(pct)/100)
	case "7501":
		text := strings.Join(args, ";")
		if text == "" {
			return
		}
		e.store.SetStateText(id, text)
	}
}

// Event hands a node event back to the lens that owns the block, and stores
// the view it builds again.
func (e *Engine) Event(blockID int, ev Event) {
	for _, b := range e.store.Snapshot() {
		if b.ID != blockID || b.LensID == "" {
			continue
		}
		l := ByID(b.LensID)
		if l == nil || b.State == nil {
			return
		}
		if err := l.Event(b.State, ev); err != nil {
			return
		}
		node, err := l.View(b.State)
		e.store.SetNative(blockID, b.LensID, b.State, node, err)
		return
	}
}

// emitsJSON reports commands that always print JSON, whether or not the line
// carries a JSON flag: `cargo metadata` is JSON by definition.
func emitsJSON(cmd model.Cmd) bool {
	if cmd.Program == "cargo" {
		for _, a := range cmd.Args {
			if a == "metadata" {
				return true
			}
			if !strings.HasPrefix(a, "-") {
				return false // the first operand is the subcommand
			}
		}
	}
	return false
}
