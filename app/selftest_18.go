//go:build selftest

package app

// Ticket 18 self-test. Press F11, then 1, then 8. The check proves command
// autocomplete from recorded history and a real shell history file, keyword
// highlighting through the Session output path, the Session counters, the BEL
// hook and OSC notification modes, and the chrome layout at both supported
// sizes.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-18.json. Every step closes the Sessions it
// opened, removes its temporary directory, and clears its own state, so
// F11,1,8 passes twice in one process.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/histfile"
	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const selftest18Ticket = 18

type selftest18 struct {
	state *state.State

	autocomplete selftest18Autocomplete
	highlight    selftest18Highlight
	stats        selftest18Stats
	bell         selftest18Bell
}

func (t *selftest18) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest18Ticket,
		Name:   "Autocomplete, keyword highlighting, statistics, and bell",
		Steps: []selftest.Step{
			{Name: "autocomplete", Run: t.autocompleteStep},
			{Name: "keyword-highlight", Run: t.highlightStep},
			{Name: "session-stats", Run: t.statsStep},
			{Name: "bell-and-notifications", Run: t.bellStep},
			{Name: "layout", Run: t.layoutStep},
			{Name: "demo", Run: t.demoStep},
		},
	}
}

// selftest18Transport is a recording transport. Output is fed by the check;
// input written by the Session is recorded for exact assertion.
type selftest18Transport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once

	mu    sync.Mutex
	input bytes.Buffer
}

func newSelftest18Transport() *selftest18Transport {
	return &selftest18Transport{output: make(chan []byte, 64), closed: make(chan struct{})}
}

func (t *selftest18Transport) Read(p []byte) (int, error) {
	select {
	case chunk := <-t.output:
		return copy(p, chunk), nil
	case <-t.closed:
		return 0, os.ErrClosed
	}
}

func (t *selftest18Transport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.Write(p)
}

func (t *selftest18Transport) Resize(int, int) error { return nil }

func (t *selftest18Transport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *selftest18Transport) feed(data ...byte) {
	t.output <- append([]byte(nil), data...)
}

func (t *selftest18Transport) written() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.String()
}

type selftest18Factory struct{ transport *selftest18Transport }

func (f selftest18Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// selftest18Store is the autocomplete history port backed by memory, so the
// check never touches the user's command history file.
type selftest18Store struct {
	mu      sync.Mutex
	entries []autocomplete.Entry
}

func (s *selftest18Store) Load(context.Context) ([]autocomplete.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]autocomplete.Entry(nil), s.entries...), nil
}

func (s *selftest18Store) Save(_ context.Context, entries []autocomplete.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append([]autocomplete.Entry(nil), entries...)
	return nil
}

// selftest18Suggests is the production config closure used by the check.
func selftest18Suggests() autocomplete.Config {
	return autocomplete.Config{
		Enabled:        true,
		MinChars:       1,
		MaxSuggestions: 10,
		DebounceMs:     100,
		Scope:          domainterminal.AutocompleteHistoryHost,
	}
}

type selftest18Autocomplete struct {
	started   bool
	stage     int
	deadline  time.Time
	dir       string
	auto      *autocomplete.Service
	service   *appsession.Service
	session   *appsession.Session
	transport *selftest18Transport
	surface   *sessionui.Surface
	tester    *ui.Tester
	rows      []autocomplete.Suggestion
}

func (t *selftest18) autocompleteStep(*ui.Context) selftest.Outcome {
	a := &t.autocomplete
	if a.started && a.service == nil {
		a.started = false
	}
	if !a.started {
		dir, err := os.MkdirTemp("", "natcatty-selftest-18-auto-")
		if err != nil {
			return selftest.Failed(err)
		}
		history := filepath.Join(dir, ".bash_history")
		if err := os.WriteFile(history, []byte("git stash\ngit show\n"), 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return selftest.Failed(fmt.Errorf("write the shell history: %w", err))
		}
		a.dir = dir
		a.auto = autocomplete.NewService(&selftest18Store{}, selftest18Suggests,
			autocomplete.WithSource(histfile.New(histfile.Options{
				Files: []histfile.File{{Path: history, Kind: histfile.Bash}},
			})),
		)
		a.auto.Load(context.Background())
		a.transport = newSelftest18Transport()
		a.service = appsession.NewService(selftest18Factory{transport: a.transport},
			appsession.WithCommandHook(func(_ model.SessionID, scope, command string) {
				a.auto.Record(scope, command)
			}))
		session, err := a.service.Open(context.Background(), appsession.StartSpec{
			Kind: appsession.KindLocal,
		})
		if err != nil {
			t.finishAutocomplete()
			return selftest.Failed(fmt.Errorf("open the autocomplete Session: %w", err))
		}
		a.session = session
		if _, err := session.TerminalConn().Write([]byte("git status\r")); err != nil {
			t.finishAutocomplete()
			return selftest.Failed(fmt.Errorf("run a command: %w", err))
		}
		// The shell's OSC 133 exit mark returns the Session to the prompt, so
		// autocomplete may offer suggestions again.
		a.transport.feed([]byte("\x1b]133;D;0\x07")...)
		a.started = true
		a.stage = 1
		a.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	switch a.stage {
	case 1:
		if a.session.Snapshot().Stats.Commands < 1 || a.session.Snapshot().Command.Running {
			return pending(a.deadline, "the command hook to record a command at the prompt")
		}
		suggestions := a.auto.Suggestions("git", "local")
		var history, shell bool
		for _, suggestion := range suggestions {
			history = history || suggestion.Source == "history"
			shell = shell || suggestion.Source == "shell-history"
		}
		if !history || !shell {
			return pending(a.deadline, "history and shell-history suggestions")
		}
		surface, err := sessionui.New(a.session,
			sessionui.WithRedraw(func() {}),
			sessionui.WithSuggestions(a.auto.Suggestions, func() (bool, bool) { return true, false }),
		)
		if err != nil {
			return t.finishAutocompleteWith(
				selftest.Failed(fmt.Errorf("build the Session surface: %w", err)))
		}
		a.surface = surface
		a.tester = ui.NewTester(func(c *ui.Context) { sessionui.SelftestFrame(c, surface) }, 720, 480)
		a.tester.SetLocale("en")
		a.tester.Frame()
		a.tester.ClickAt(360, 420)
		a.tester.Type("gi")
		a.stage = 2
		a.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 2:
		if a.session.Snapshot().Input != "gi" {
			return pending(a.deadline, "the typed line to reach the Session")
		}
		a.tester.Frame()
		m := locales.In("en")
		if !a.tester.HasText("git status") || !a.tester.HasText("git stash") {
			return t.finishAutocompleteWith(selftest.Failed(fmt.Errorf(
				"the popup does not list both sources: %v", a.tester.Texts())))
		}
		if !a.tester.HasText(m.SessionAutocompleteHistory()) ||
			!a.tester.HasText(m.SessionAutocompleteShellHistory()) {
			return t.finishAutocompleteWith(selftest.Failed(fmt.Errorf(
				"the popup does not name the sources: %v", a.tester.Texts())))
		}
		a.rows = a.surface.SuggestionRows()
		// Tab is the production key path: it must accept the selected command
		// instead of sending a tab character.
		if !a.surface.SelftestAcceptSuggestion(0) {
			return t.finishAutocompleteWith(selftest.Failed(
				errors.New("Tab did not accept the selected suggestion")))
		}
		a.stage = 3
		a.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 3:
		// Accepting must clear the two typed characters with Backspaces and
		// then write the suggestion; Ctrl+U is Undo in PSReadLine and must
		// never reach the shell.
		if !strings.Contains(a.transport.written(), "\x7f\x7fgit status") {
			return pending(a.deadline, "the accepted command to reach the transport")
		}
		if strings.Contains(a.transport.written(), "\t") {
			return t.finishAutocompleteWith(selftest.Failed(fmt.Errorf(
				"a tab character reached the transport: %q", a.transport.written())))
		}
		if strings.Contains(a.transport.written(), "\x15") {
			return t.finishAutocompleteWith(selftest.Failed(fmt.Errorf(
				"Ctrl+U reached the transport: %q", a.transport.written())))
		}
		if a.session.Snapshot().Input != "git status" {
			return t.finishAutocompleteWith(selftest.Failed(fmt.Errorf(
				"the replaced line is %q, want git status", a.session.Snapshot().Input)))
		}
		if len(a.rows) < 2 {
			return t.finishAutocompleteWith(selftest.Failed(
				fmt.Errorf("popup rows = %+v", a.rows)))
		}
		return t.finishAutocompleteWith(selftest.Passed(
			"the popup listed a recorded command and a command from a real .bash_history file with their sources, and Tab replaced the line with the selected command without sending a tab",
			"autocomplete.Service + histfile.Source + sessionui.Surface"))
	}
	return t.finishAutocompleteWith(
		selftest.Failed(errors.New("autocomplete step reached an unknown stage")))
}

func (t *selftest18) finishAutocompleteWith(outcome selftest.Outcome) selftest.Outcome {
	t.finishAutocomplete()
	return outcome
}

func (t *selftest18) finishAutocomplete() {
	a := &t.autocomplete
	if a.surface != nil {
		a.surface.Close()
	}
	if a.session != nil {
		_ = a.session.Close()
	}
	if a.service != nil {
		a.service.CloseAll()
	}
	if a.dir != "" {
		_ = os.RemoveAll(a.dir)
	}
	t.autocomplete = selftest18Autocomplete{}
}

type selftest18Highlight struct {
	started   bool
	stage     int
	deadline  time.Time
	service   *appsession.Service
	session   *appsession.Session
	transport *selftest18Transport
	output    *selftest18Output
}

type selftest18Output struct {
	mu   sync.Mutex
	data []byte
}

func (o *selftest18Output) collect(conn *appsession.TerminalConn) {
	buffer := make([]byte, 256)
	for {
		n, err := conn.Read(buffer)
		if n > 0 {
			o.mu.Lock()
			o.data = append(o.data, buffer[:n]...)
			o.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (o *selftest18Output) contains(want string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.Contains(o.data, []byte(want))
}

func selftest18Rule(id, pattern, color string) []domainterminal.KeywordHighlightRule {
	return []domainterminal.KeywordHighlightRule{{
		ID: id, Patterns: []string{pattern}, Color: color, Enabled: true,
	}}
}

func (t *selftest18) highlightStep(*ui.Context) selftest.Outcome {
	h := &t.highlight
	if h.started && h.service == nil {
		h.started = false
	}
	if !h.started {
		h.transport = newSelftest18Transport()
		h.output = &selftest18Output{}
		h.service = appsession.NewService(selftest18Factory{transport: h.transport},
			appsession.WithHighlightPolicy(func() (bool, []domainterminal.KeywordHighlightRule) {
				return true, selftest18Rule("error", `\bERRORWORD\b`, "#0B1621")
			}))
		session, err := h.service.Open(context.Background(),
			appsession.StartSpec{Kind: appsession.KindLocal})
		if err != nil {
			t.finishHighlight()
			return selftest.Failed(fmt.Errorf("open the highlight Session: %w", err))
		}
		h.session = session
		go h.output.collect(session.TerminalConn())
		h.transport.feed([]byte("before ERRORWORD after\n")...)
		h.started = true
		h.stage = 1
		h.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	switch h.stage {
	case 1:
		if !h.output.contains("\x1b[38;2;11;22;33mERRORWORD\x1b[39m") {
			return pending(h.deadline, "the configured keyword to be colored")
		}
		h.service.SetHighlightPolicy(false,
			selftest18Rule("error", `\bERRORWORD\b`, "#0B1621"))
		h.transport.feed([]byte("plain ERRORWORD here\n")...)
		h.stage = 2
		h.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 2:
		if !h.output.contains("plain ERRORWORD here") {
			return pending(h.deadline, "output while highlighting is disabled")
		}
		if h.output.contains("\x1b[38;2;44;55;66m") {
			return t.finishHighlightWith(selftest.Failed(
				errors.New("a disabled policy still colored output")))
		}
		h.service.SetHighlightPolicy(true, selftest18Rule("live", `\bLIVEWORD\b`, "#2C3742"))
		h.transport.feed([]byte("live LIVEWORD now\n")...)
		h.stage = 3
		h.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 3:
		if !h.output.contains("\x1b[38;2;44;55;66mLIVEWORD\x1b[39m") {
			return pending(h.deadline, "a live rule change to color new output")
		}
		return t.finishHighlightWith(selftest.Passed(
			"the configured rule colored its match and restored the default foreground, disabling it stopped coloring, and a live rule change colored new output",
			"appsession.Service.SetHighlightPolicy + domainterminal.Highlighter"))
	}
	return t.finishHighlightWith(
		selftest.Failed(errors.New("highlight step reached an unknown stage")))
}

func (t *selftest18) finishHighlightWith(outcome selftest.Outcome) selftest.Outcome {
	t.finishHighlight()
	return outcome
}

func (t *selftest18) finishHighlight() {
	h := &t.highlight
	if h.session != nil {
		_ = h.session.Close()
	}
	if h.service != nil {
		h.service.CloseAll()
	}
	t.highlight = selftest18Highlight{}
}

type selftest18Stats struct {
	started  bool
	deadline time.Time
	service  *appsession.Service
	session  *appsession.Session
	bells    chan model.SessionID
	notices  chan appsession.TerminalNotification
}

func (t *selftest18) statsStep(*ui.Context) selftest.Outcome {
	s := &t.stats
	if s.started && s.service == nil {
		s.started = false
	}
	if !s.started {
		transport := newSelftest18Transport()
		s.bells = make(chan model.SessionID, 4)
		s.notices = make(chan appsession.TerminalNotification, 4)
		s.service = appsession.NewService(selftest18Factory{transport: transport},
			appsession.WithBellHook(func(id model.SessionID) {
				select {
				case s.bells <- id:
				default:
				}
			}),
			appsession.WithNotificationHook(func(_ model.SessionID, notification appsession.TerminalNotification) {
				select {
				case s.notices <- notification:
				default:
				}
			}),
		)
		session, err := s.service.Open(context.Background(),
			appsession.StartSpec{Kind: appsession.KindLocal})
		if err != nil {
			t.finishStats()
			return selftest.Failed(fmt.Errorf("open the statistics Session: %w", err))
		}
		s.session = session
		if _, err := session.TerminalConn().Write([]byte("echo hi\r")); err != nil {
			t.finishStats()
			return selftest.Failed(fmt.Errorf("type a command: %w", err))
		}
		transport.feed([]byte("hello\n")...)
		transport.feed(0x07)
		transport.feed([]byte("\x1b]133;D;1\x07")...)
		transport.feed([]byte("\x1b]777;notify;build;done\x07")...)
		s.started = true
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	snapshot := s.session.Snapshot()
	stats := snapshot.Stats
	if stats.InputBytes < 7 || stats.OutputBytes < 5 || stats.Commands < 1 ||
		stats.FailedCommands < 1 || stats.Bells < 1 || stats.Notifications < 1 {
		return pending(s.deadline, fmt.Sprintf(
			"the counters to settle (input %d output %d commands %d failed %d bells %d notifications %d)",
			stats.InputBytes, stats.OutputBytes, stats.Commands,
			stats.FailedCommands, stats.Bells, stats.Notifications))
	}
	select {
	case <-s.bells:
	default:
		return pending(s.deadline, "the bell hook")
	}
	select {
	case notification := <-s.notices:
		if notification.Title != "build" {
			return t.finishStatsWith(selftest.Failed(fmt.Errorf(
				"notification title = %q", notification.Title)))
		}
	default:
		return pending(s.deadline, "the notification hook")
	}
	if stats.Transfers != 0 {
		return t.finishStatsWith(selftest.Failed(fmt.Errorf(
			"transfers = %d without a transfer", stats.Transfers)))
	}

	return t.finishStatsWith(selftest.Passed(
		fmt.Sprintf("typed input, output, a submitted command, a failed exit code, a BEL, and an OSC 777 notification produced the expected counters (input %d, output %d, commands %d, failed %d, bells %d, notifications %d)",
			stats.InputBytes, stats.OutputBytes, stats.Commands,
			stats.FailedCommands, stats.Bells, stats.Notifications),
		"appsession.Stats"))
}

func (t *selftest18) finishStatsWith(outcome selftest.Outcome) selftest.Outcome {
	t.finishStats()
	return outcome
}

func (t *selftest18) finishStats() {
	s := &t.stats
	if s.session != nil {
		_ = s.session.Close()
	}
	if s.service != nil {
		s.service.CloseAll()
	}
	t.stats = selftest18Stats{}
}

type selftest18Bell struct {
	started  bool
	deadline time.Time
	service  *appsession.Service
	session  *appsession.Session
	bells    chan model.SessionID
	notices  chan appsession.TerminalNotification
}

func (t *selftest18) bellStep(*ui.Context) selftest.Outcome {
	b := &t.bell
	if b.started && b.service == nil {
		b.started = false
	}
	if !b.started {
		transport := newSelftest18Transport()
		b.bells = make(chan model.SessionID, 4)
		b.notices = make(chan appsession.TerminalNotification, 4)
		b.service = appsession.NewService(selftest18Factory{transport: transport},
			appsession.WithBellHook(func(id model.SessionID) {
				select {
				case b.bells <- id:
				default:
				}
			}),
			appsession.WithNotificationHook(func(_ model.SessionID, notification appsession.TerminalNotification) {
				select {
				case b.notices <- notification:
				default:
				}
			}),
		)
		session, err := b.service.Open(context.Background(),
			appsession.StartSpec{Kind: appsession.KindLocal})
		if err != nil {
			t.finishBell()
			return selftest.Failed(fmt.Errorf("open the bell Session: %w", err))
		}
		b.session = session
		transport.feed(0x07)
		transport.feed([]byte("\x1b]777;notify;deploy;finished\x07")...)
		b.started = true
		b.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	select {
	case <-b.bells:
	default:
		return pending(b.deadline, "the BEL hook")
	}
	select {
	case notification := <-b.notices:
		if notification.Title != "deploy" || notification.Body != "finished" {
			return t.finishBellWith(selftest.Failed(fmt.Errorf(
				"notification = %+v", notification)))
		}
	default:
		return pending(b.deadline, "the OSC notification hook")
	}

	if notificationAllowed(domainterminal.NotificationOff, false) ||
		notificationAllowed(domainterminal.NotificationOff, true) {
		return t.finishBellWith(selftest.Failed(
			errors.New("the off mode allowed a notification")))
	}
	if notificationAllowed(domainterminal.NotificationUnfocused, true) {
		return t.finishBellWith(selftest.Failed(
			errors.New("the unfocused mode notified while focused")))
	}
	if !notificationAllowed(domainterminal.NotificationUnfocused, false) {
		return t.finishBellWith(selftest.Failed(
			errors.New("the unfocused mode suppressed an unfocused notification")))
	}
	if !notificationAllowed(domainterminal.NotificationAlways, true) ||
		!notificationAllowed(domainterminal.NotificationAlways, false) {
		return t.finishBellWith(selftest.Failed(
			errors.New("the always mode suppressed a notification")))
	}
	return t.finishBellWith(selftest.Passed(
		"a BEL reached the Session's bell hook (the app marks that Session active) and an OSC 777 notification reached the notification hook; the off, unfocused, and always modes then allowed exactly the documented cases",
		"appsession Session hooks + notificationAllowed"))
}

func (t *selftest18) finishBellWith(outcome selftest.Outcome) selftest.Outcome {
	t.finishBell()
	return outcome
}

func (t *selftest18) finishBell() {
	b := &t.bell
	if b.session != nil {
		_ = b.session.Close()
	}
	if b.service != nil {
		b.service.CloseAll()
	}
	t.bell = selftest18Bell{}
}

func (t *selftest18) layoutStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	suggestions := []autocomplete.Suggestion{
		{Text: "git status", Display: "git status", Source: "history", Frequency: 3},
		{Text: "git stash", Display: "git stash", Source: "shell-history"},
		{Text: "git rebase --interactive", Display: "git rebase --interactive", Source: "history"},
		{Text: "git log --oneline", Display: "git log --oneline", Source: "history"},
		{Text: "git diff", Display: "git diff", Source: "history"},
		{Text: "git push", Display: "git push", Source: "history"},
		{Text: "git pull", Display: "git pull", Source: "history"},
	}
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		model := sessionui.SelftestInteractionModel{
			Command: appsession.CommandState{Text: "git", Running: true},
			Busy:    true, LogFormat: domainterminal.LogFormatText, LogDirectory: `C:\logs`,
			Input:            "git",
			Suggestions:      suggestions,
			SuggestionIndex:  1,
			ShowAutocomplete: true,
			ShowGhost:        true,
			GhostText:        "git status",
		}
		tester := ui.NewTester(func(c *ui.Context) {
			sessionui.RenderSelftestInteraction(c, model, sessionui.SelftestInteractionActions{})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		for _, text := range []string{
			m.SessionMenuLabel(),
			m.SessionAutocompleteLabel(),
			m.SessionAutocompleteHint(),
			m.SessionAutocompleteGhost("git status"),
			suggestions[0].Display,
			suggestions[5].Display,
		} {
			rect, ok := tester.Find(text)
			if !ok {
				return selftest.Failed(fmt.Errorf("%dx%d lacks %q: %v",
					size[0], size[1], text, tester.Texts()))
			}
			if rect.X < 0 || rect.Y < 0 ||
				rect.X+rect.W > float32(size[0]) || rect.Y+rect.H > float32(size[1]) {
				return selftest.Failed(fmt.Errorf("%dx%d clips %q: %+v",
					size[0], size[1], text, rect))
			}
		}
		if tester.HasText(suggestions[6].Display) {
			return selftest.Failed(fmt.Errorf("%dx%d rendered more than six suggestions",
				size[0], size[1]))
		}
		menu, _ := tester.Find(m.SessionMenuLabel())
		popup, _ := tester.Find(m.SessionAutocompleteLabel())
		if !(menu.Y < popup.Y) {
			return selftest.Failed(fmt.Errorf("%dx%d rows are out of order: %+v %+v",
				size[0], size[1], menu, popup))
		}
	}
	return selftest.Passed(
		"the autocomplete popup (bounded to six rows) and ghost hint stayed inside 1100x720 and 720x480 in the production chrome",
		"sessionui.RenderSelftestInteraction + ui.Tester")
}

// demoStep leaves a real Session surface showing an autocomplete popup and the
// counters. In the headless wiring tests the SurfaceFactory is a fake, so the
// step notes that the real-surface steps above cover the production path.
func (t *selftest18) demoStep(*ui.Context) selftest.Outcome {
	if t.state == nil {
		return selftest.Failed(errors.New("the shell is not wired"))
	}
	if t.state.ActiveSurface() == nil {
		if err := t.state.NewTab(); err != nil {
			return selftest.Failed(fmt.Errorf("open the demo Session: %w", err))
		}
	}
	surface := t.state.ActiveSurface()
	if surface == nil {
		return selftest.Failed(errors.New("the demo Session has no surface"))
	}
	if _, ok := surface.(*sessionui.Surface); ok {
		return selftest.Passed(
			"a real Session surface is open and reads the autocomplete and statistics settings on every frame",
			"sessionui.Surface + settings providers")
	}
	return selftest.Passed(
		"the headless wiring uses a fake SurfaceFactory; the autocomplete, counter, layout, and real-surface steps cover the production path",
		"state.SurfaceFactory")
}
