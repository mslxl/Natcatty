// Package selftest is the keyboard-driven self-test facility. It is developer
// tooling: the normal application never imports it, so it is not linked into
// the shipped binary. The ticket-specific checks and the wiring live in
// files guarded by the selftest build tag.
//
// # Shortcut
//
// A window registers the entry point with MyGo's Context.OnShortcut, not a
// native menu accelerator: MyGo v0.5.5 has no native TranslateAccelerator
// path, and the automated tester cannot hold OS modifier state. The keys are
// plain, without modifiers:
//
//	F11        show the check list
//	1 3        the two-digit ticket number; F11 1 3 runs ticket 13
//	Escape     cancel the overlay or a running check
//
// F11 is deliberate: MyGo v0.5.5 reserves plain F12 for its inspector
// (ui/input.go, ui/inspector.go), which is enabled in a normal `go build`.
// F11 is not reserved by the framework's native UI.
//
// Bind must be called with the window's root Context, before any App Lock
// gate or other container builds children: Context.OnShortcut then registers
// a window shortcut rather than an element-scoped one. View builds the
// content and the overlay; it can live inside the gate.
//
// While the overlay is open, its backdrop takes the keyboard focus and
// consumes text input, so the digits that select a check never reach a
// focused terminal behind it.
//
// # Result
//
// A finished check writes JSON to the process's temporary directory:
//
//	%TEMP%\natcatty-selftest-<pid>\natcatty-selftest-13.json   (on Windows)
//	$TMPDIR/natcatty-selftest-<pid>/natcatty-selftest-13.json  (elsewhere)
//
// The file carries pass, steps, errors, and timestamp. The overlay shows the
// same status and the path after the run.
//
// Checks run one step per frame on the UI thread, and a waiting step asks for
// the next frame with Context.After, so the event loop is never blocked.
package selftest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Outcome is what one step reports for one frame.
type Outcome struct {
	// Waiting asks the harness to call the step again on a later frame.
	Waiting bool
	// Pass is meaningful when Waiting is false.
	Pass bool
	// Detail is a human-readable note shown in the overlay and the result.
	Detail string
	// Seam names the injectable seam a step used instead of a native dialog,
	// as "dialogConfirmer" or "uiapplock.Gate".
	Seam string
	// Err makes the whole check fail.
	Err error
}

// Pending asks for another frame without finishing the step.
var Pending = Outcome{Waiting: true}

// Passed completes a step successfully.
func Passed(detail, seam string) Outcome {
	return Outcome{Pass: true, Detail: detail, Seam: seam}
}

// Failed completes a step and fails the whole check.
func Failed(err error) Outcome {
	return Outcome{Pass: false, Err: err}
}

// Step is one observable part of a check. Run executes on the UI thread.
type Step struct {
	Name string
	Run  func(*ui.Context) Outcome
}

// StepResult is the recorded outcome of one Step.
type StepResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	Seam   string `json:"seam,omitempty"`
}

// Result is the machine-readable self-test outcome.
type Result struct {
	Ticket    int          `json:"ticket"`
	Name      string       `json:"name"`
	Pass      bool         `json:"pass"`
	Steps     []StepResult `json:"steps"`
	Errors    []string     `json:"errors"`
	Timestamp string       `json:"timestamp"`
	Path      string       `json:"path,omitempty"`
}

// Check is one registered ticket self-check.
type Check struct {
	Ticket int
	Name   string
	Steps  []Step
}

// Registry holds the checks a build registered.
type Registry struct {
	checks map[int]Check
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{checks: make(map[int]Check)}
}

// Register adds a check. Tickets are exactly two digits so F11 plus two keys
// identifies every check.
func (r *Registry) Register(check Check) {
	if check.Ticket < 10 || check.Ticket > 99 {
		panic(fmt.Sprintf("selftest: ticket %d is not two digits", check.Ticket))
	}
	if check.Name == "" {
		panic(fmt.Sprintf("selftest: ticket %d has no name", check.Ticket))
	}
	if len(check.Steps) == 0 {
		panic(fmt.Sprintf("selftest: ticket %d has no steps", check.Ticket))
	}
	if _, exists := r.checks[check.Ticket]; exists {
		panic(fmt.Sprintf("selftest: ticket %d is registered twice", check.Ticket))
	}
	r.checks[check.Ticket] = check
}

// Lookup returns the check for ticket.
func (r *Registry) Lookup(ticket int) (Check, bool) {
	check, ok := r.checks[ticket]
	return check, ok
}

// Checks returns the registered checks in ticket order.
func (r *Registry) Checks() []Check {
	tickets := make([]int, 0, len(r.checks))
	for ticket := range r.checks {
		tickets = append(tickets, ticket)
	}
	sort.Ints(tickets)
	checks := make([]Check, len(tickets))
	for index, ticket := range tickets {
		checks[index] = r.checks[ticket]
	}
	return checks
}

// State is the overlay's phase.
type State int

const (
	// StateHidden draws nothing and accepts only F11.
	StateHidden State = iota
	// StateMenu lists the checks and accepts a two-digit ticket.
	StateMenu
	// StateRunning executes one step per frame.
	StateRunning
	// StatePassed shows the passing result.
	StatePassed
	// StateFailed shows the failing result and its errors.
	StateFailed
)

// Option customizes a Harness.
type Option func(*Harness)

// WithResultDir writes result files under dir. The default is a directory
// private to this process (see DefaultResultDir).
func WithResultDir(dir string) Option {
	return func(h *Harness) {
		if dir != "" {
			h.resultDir = dir
		}
	}
}

// WithClock sets the clock used for timestamps. Tests make it deterministic.
func WithClock(now func() time.Time) Option {
	return func(h *Harness) {
		if now != nil {
			h.now = now
		}
	}
}

// Harness owns the overlay and the running check. It is touched only on the
// UI thread.
type Harness struct {
	registry  *Registry
	resultDir string
	now       func() time.Time

	state   State
	digits  string
	problem string
	check   Check
	step    int
	result  Result
	written bool
}

// DefaultResultDir returns a result directory private to this process. Two
// Natcatty instances, or an app run beside a `go test` run, must not overwrite
// each other's result files in the shared system temp directory.
func DefaultResultDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("natcatty-selftest-%d", os.Getpid()))
}

// NewHarness returns a harness for registry.
func NewHarness(registry *Registry, opts ...Option) *Harness {
	if registry == nil {
		registry = NewRegistry()
	}
	harness := &Harness{
		registry:  registry,
		resultDir: DefaultResultDir(),
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(harness)
	}
	return harness
}

// Bind registers the self-test window shortcuts. Call it with the window's
// root Context before any container builds children, so MyGo treats the keys
// as window shortcuts. Install it on the main window only.
func (h *Harness) Bind(c *ui.Context) {
	if c == nil {
		return
	}
	c.OnShortcut(0, ui.KeyF11, func() {
		if h.state != StateRunning {
			h.toggle()
			c.Invalidate()
		}
	})
	if h.state == StateHidden {
		return
	}
	c.OnShortcut(0, ui.KeyEscape, func() {
		h.cancel()
		c.Invalidate()
	})
	if h.state == StateRunning {
		return
	}
	for key := ui.Key0; key <= ui.Key9; key++ {
		digit := int(key - ui.Key0)
		c.OnShortcut(0, key, func() {
			h.pressDigit(digit)
			c.Invalidate()
		})
	}
}

// View renders content and, above it, the overlay and step driver. Bind must
// already have run at the window's root scope.
func (h *Harness) View(content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		if content != nil {
			content(c)
		}
		h.advance(c)
		h.draw(c)
	}
}

// State returns the overlay phase.
func (h *Harness) State() State { return h.state }

// Visible reports whether the overlay is drawn.
func (h *Harness) Visible() bool { return h.state != StateHidden }

// Running reports whether a check is executing.
func (h *Harness) Running() bool { return h.state == StateRunning }

// LastResult returns the most recent finished result.
func (h *Harness) LastResult() (Result, bool) {
	if h.state != StatePassed && h.state != StateFailed {
		return Result{}, false
	}
	return h.result, true
}

func (h *Harness) toggle() {
	switch h.state {
	case StateHidden:
		h.state = StateMenu
		h.digits = ""
		h.problem = ""
	case StateRunning:
		// A running check keeps the overlay until it finishes.
	default:
		h.state = StateHidden
		h.digits = ""
		h.problem = ""
	}
}

func (h *Harness) cancel() {
	h.state = StateHidden
	h.digits = ""
	h.problem = ""
	h.check = Check{}
	h.step = 0
	h.result = Result{}
	h.written = false
}

func (h *Harness) pressDigit(digit int) {
	if h.state == StateRunning || h.state == StateHidden {
		return
	}
	// A digit after a finished check starts a fresh entry instead of
	// appending to the old overlay.
	h.state = StateMenu
	h.digits += strconv.Itoa(digit)
	if len(h.digits) > 2 {
		h.digits = h.digits[len(h.digits)-2:]
	}
	h.problem = ""
	if len(h.digits) < 2 {
		return
	}
	ticket, err := strconv.Atoi(h.digits)
	if err != nil {
		h.problem = "That is not a ticket number."
		h.digits = ""
		return
	}
	check, ok := h.registry.Lookup(ticket)
	if !ok {
		h.problem = fmt.Sprintf("No self-test is registered for ticket %02d.", ticket)
		h.digits = ""
		return
	}
	h.start(check)
}

func (h *Harness) start(check Check) {
	h.check = check
	h.step = 0
	h.digits = ""
	h.problem = ""
	h.written = false
	h.result = Result{
		Ticket:    check.Ticket,
		Name:      check.Name,
		Steps:     []StepResult{},
		Errors:    []string{},
		Timestamp: h.now().UTC().Format(time.RFC3339Nano),
	}
	h.state = StateRunning
}

func (h *Harness) advance(c *ui.Context) {
	if h.state != StateRunning {
		return
	}
	if h.step >= len(h.check.Steps) {
		h.finish()
		return
	}
	step := h.check.Steps[h.step]
	outcome := step.Run(c)
	if outcome.Waiting {
		// Ask for another frame without spinning: the event loop stays free
		// between steps.
		c.After(25 * time.Millisecond)
		return
	}
	record := StepResult{
		Name:   step.Name,
		Pass:   outcome.Pass,
		Detail: outcome.Detail,
		Seam:   outcome.Seam,
	}
	if outcome.Err != nil {
		record.Error = outcome.Err.Error()
	}
	h.result.Steps = append(h.result.Steps, record)
	if !outcome.Pass {
		if outcome.Err == nil {
			outcome.Err = fmt.Errorf("step %q failed", step.Name)
		}
		h.fail(outcome.Err)
		return
	}
	h.step++
	if h.step >= len(h.check.Steps) {
		h.finish()
		return
	}
	// The next step is ready now; ask for the frame that runs it.
	c.Invalidate()
}

func (h *Harness) finish() {
	h.result.Pass = true
	h.write()
}

func (h *Harness) fail(err error) {
	if err == nil {
		err = fmt.Errorf("self-test failed")
	}
	h.result.Pass = false
	h.result.Errors = append(h.result.Errors, err.Error())
	h.write()
}

func (h *Harness) write() {
	if h.written {
		return
	}
	h.written = true
	h.result.Timestamp = h.now().UTC().Format(time.RFC3339Nano)
	name := fmt.Sprintf("natcatty-selftest-%02d.json", h.result.Ticket)
	path := filepath.Join(h.resultDir, name)
	h.result.Path = path
	data, err := json.MarshalIndent(h.result, "", "  ")
	if err == nil {
		data = append(data, '\n')
		if mkdirErr := os.MkdirAll(h.resultDir, 0o755); mkdirErr != nil {
			err = mkdirErr
		} else {
			err = os.WriteFile(path, data, 0o644)
		}
	}
	if err != nil {
		h.result.Pass = false
		h.result.Errors = append(h.result.Errors, "write result: "+err.Error())
	}
	if h.result.Pass {
		h.state = StatePassed
	} else {
		h.state = StateFailed
	}
}

func (h *Harness) draw(c *ui.Context) {
	if h.state == StateHidden {
		return
	}
	theme := c.Theme()
	ui.Overlay(c, func() {
		// Dim the window so the developer overlay is unmistakably on top and
		// the content behind it stays readable but muted. The backdrop also
		// takes the keyboard focus and swallows text: a focused terminal
		// behind the overlay must not receive the digits that select a check.
		backdrop := ui.Box(c.Key("selftest-backdrop")).Absolute().
			Left(0).Top(0).Right(0).Bottom(0).
			Background(ui.RGBA(0, 0, 0, 0.30)).
			Focusable().AutoFocus().Label("Self-test overlay")
		backdrop.HandleInput(func(event ui.InputEvent) bool {
			switch event.Kind {
			case ui.InputText, ui.InputCompose, ui.InputCommand,
				ui.InputKeyDown, ui.InputKeyUp:
				return true
			}
			return false
		})
		ui.Column(c).Absolute().Top(16).Left(16).Width(520).Padding(12).Gap(6).
			Radius(8).Background(theme.Surface).Border(1, theme.Accent).Children(func() {
			ui.Text(c, "Self-test").FontSize(15).Bold().TextColor(theme.Accent)
			switch h.state {
			case StateMenu:
				ui.Text(c, "F11, then a two-digit ticket number. Escape cancels.").
					FontSize(12).TextColor(theme.TextMuted)
				for _, check := range h.registry.Checks() {
					ui.Text(c, fmt.Sprintf("%02d  %s", check.Ticket, check.Name)).FontSize(12)
				}
				if h.digits != "" {
					ui.Text(c, "Ticket: "+h.digits+"_").FontSize(12)
				}
				if h.problem != "" {
					ui.Text(c, h.problem).FontSize(12).TextColor(uitheme.DangerText(theme))
				}
			case StateRunning:
				ui.Text(c, fmt.Sprintf("Running %02d  %s", h.check.Ticket, h.check.Name)).FontSize(12)
				ui.Text(c, fmt.Sprintf("Step %d/%d: %s", min(h.step+1, len(h.check.Steps)),
					len(h.check.Steps), h.check.Steps[min(h.step, len(h.check.Steps)-1)].Name)).
					FontSize(12).TextColor(theme.TextMuted)
				h.drawSteps(c, theme)
			case StatePassed, StateFailed:
				label := "PASS"
				color := theme.Accent
				if h.state == StateFailed {
					label = "FAIL"
					color = uitheme.DangerText(theme)
				}
				ui.Text(c, fmt.Sprintf("%s  %02d  %s", label, h.result.Ticket, h.result.Name)).
					FontSize(14).Bold().TextColor(color)
				h.drawSteps(c, theme)
				for _, problem := range h.result.Errors {
					ui.Text(c, problem).FontSize(12).TextColor(uitheme.DangerText(theme))
				}
				if h.result.Path != "" {
					ui.Text(c, "Result: "+h.result.Path).FontSize(11).TextColor(theme.TextMuted)
				}
				ui.Text(c, "F11 closes, Escape cancels.").FontSize(11).TextColor(theme.TextMuted)
			}
		})
	})
}

func (h *Harness) drawSteps(c *ui.Context, theme *ui.Theme) {
	for _, step := range h.result.Steps {
		marker := "ok  "
		color := theme.TextMuted
		if !step.Pass {
			marker = "fail"
			color = uitheme.DangerText(theme)
		}
		line := fmt.Sprintf("%s  %s", marker, step.Name)
		if step.Seam != "" {
			line += " (seam: " + step.Seam + ")"
		}
		ui.Text(c, line).FontSize(11).TextColor(color)
	}
}
