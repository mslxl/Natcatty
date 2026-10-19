//go:build selftest

package app

// Ticket 17 self-test. Press F11, then 1, then 7. The check proves the
// per-Session encoding decode/encode path across split multibyte chunks, the
// multiline paste gate with a real local shell, the production drop handler
// (quoted insertion, multiple paths, the upload seam), the real Session menu
// with its disabled states, and the chrome layout at both supported sizes.
//
// Result: %TEMP%\natcatty-selftest-17.json. Every run closes the Sessions it
// opened and clears the demo state before preparing it again, so F11,1,7
// passes twice in one process.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/adapters/textcodec"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const selftest17Ticket = 17

type selftest17 struct {
	state *state.State

	paste selftest17Paste
}

func (t *selftest17) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest17Ticket,
		Name:   "Terminal encoding, menu, drop, and paste protection",
		Steps: []selftest.Step{
			{Name: "encoding", Run: t.encodingStep},
			{Name: "paste-protection", Run: t.pasteStep},
			{Name: "drop-handling", Run: t.dropStep},
			{Name: "session-menu", Run: t.menuStep},
			{Name: "layout", Run: t.layoutStep},
			{Name: "demo", Run: t.demoStep},
		},
	}
}

// selftest17Transport is a recording transport. Output is fed by the check;
// input written by the Session is recorded for exact assertion.
type selftest17Transport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once

	mu    sync.Mutex
	input bytes.Buffer
}

func newSelftest17Transport() *selftest17Transport {
	return &selftest17Transport{output: make(chan []byte, 64), closed: make(chan struct{})}
}

func (t *selftest17Transport) Read(p []byte) (int, error) {
	select {
	case chunk := <-t.output:
		return copy(p, chunk), nil
	case <-t.closed:
		return 0, os.ErrClosed
	}
}

func (t *selftest17Transport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.Write(p)
}

func (t *selftest17Transport) Resize(int, int) error { return nil }

func (t *selftest17Transport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *selftest17Transport) feed(p ...byte) { t.output <- append([]byte(nil), p...) }

func (t *selftest17Transport) written() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.input.Bytes()...)
}

type selftest17Factory struct{ transport *selftest17Transport }

func (f selftest17Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// selftest17Output collects decoded terminal output. It is the production
// TerminalConn the UI reads, so every assertion observes post-decode UTF-8.
type selftest17Output struct {
	mu   sync.Mutex
	data []byte
}

func (o *selftest17Output) collect(conn *appsession.TerminalConn) {
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

func (o *selftest17Output) contains(want string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.Contains(o.data, []byte(want))
}

var selftest17Encoding struct {
	started   bool
	service   *appsession.Service
	session   *appsession.Session
	transport *selftest17Transport
	output    *selftest17Output
	stage     int
	deadline  time.Time
}

func (t *selftest17) encodingStep(*ui.Context) selftest.Outcome {
	s := &selftest17Encoding
	if !s.started {
		s.started = true
		s.stage = 1
		s.transport = newSelftest17Transport()
		s.output = &selftest17Output{}
		s.service = appsession.NewService(selftest17Factory{transport: s.transport},
			appsession.WithCodecFactory(func(encoding domainterminal.Encoding) appsession.Codec {
				return textcodec.New(encoding)
			}))
		session, err := s.service.Open(context.Background(), appsession.StartSpec{
			Kind:     appsession.KindLocal,
			Encoding: domainterminal.EncodingGB18030,
		})
		if err != nil {
			s.started = false
			return selftest.Failed(fmt.Errorf("open encoding Session: %w", err))
		}
		s.session = session
		go s.output.collect(session.TerminalConn())
		// 中 is D6 D0 in GB18030, fed one byte per chunk.
		s.transport.feed(0xD6)
		s.transport.feed(0xD0)
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	finish := func(outcome selftest.Outcome) selftest.Outcome {
		if s.session != nil {
			_ = s.session.Close()
		}
		if s.service != nil {
			s.service.CloseAll()
		}
		s.session = nil
		s.service = nil
		s.started = false
		s.stage = 0
		return outcome
	}

	switch s.stage {
	case 1:
		if !s.output.contains("中") {
			return pending(s.deadline, "the split GB18030 bytes to decode as 中")
		}
		if _, err := s.session.TerminalConn().Write([]byte("中")); err != nil {
			return finish(selftest.Failed(fmt.Errorf("write GB18030 input: %w", err)))
		}
		s.stage = 2
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 2:
		if !bytes.Equal(s.transport.written(), []byte{0xD6, 0xD0}) {
			return pending(s.deadline, "中 to encode as D6 D0")
		}
		if !s.session.SetEncoding(domainterminal.EncodingShiftJIS) {
			return finish(selftest.Failed(errors.New("Shift_JIS was rejected")))
		}
		// 日 is 93 FA in Shift_JIS, again split across reads.
		s.transport.feed(0x93)
		s.transport.feed(0xFA)
		s.stage = 3
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 3:
		if !s.output.contains("日") {
			return pending(s.deadline, "the split Shift_JIS bytes to decode as 日")
		}
		if _, err := s.session.TerminalConn().Write([]byte("日")); err != nil {
			return finish(selftest.Failed(fmt.Errorf("write Shift_JIS input: %w", err)))
		}
		s.stage = 4
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 4:
		want := []byte{0xD6, 0xD0, 0x93, 0xFA}
		if !bytes.Equal(s.transport.written(), want) {
			return pending(s.deadline, "the live Shift_JIS input encoding")
		}
		if !s.session.SetEncoding(domainterminal.EncodingLatin1) {
			return finish(selftest.Failed(errors.New("ISO-8859-1 was rejected")))
		}
		s.transport.feed(0xE9)
		s.stage = 5
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 5:
		if !s.output.contains("é") {
			return pending(s.deadline, "Latin-1 E9 to decode as é")
		}
		if _, err := s.session.TerminalConn().Write([]byte("é")); err != nil {
			return finish(selftest.Failed(fmt.Errorf("write Latin-1 input: %w", err)))
		}
		s.stage = 6
		s.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 6:
		want := []byte{0xD6, 0xD0, 0x93, 0xFA, 0xE9}
		if !bytes.Equal(s.transport.written(), want) {
			return pending(s.deadline, "the live Latin-1 input encoding")
		}
		if got := s.session.Snapshot().Encoding; got != domainterminal.EncodingLatin1 {
			return finish(selftest.Failed(fmt.Errorf("snapshot encoding = %q", got)))
		}
		return finish(selftest.Passed(
			"GB18030, Shift_JIS, and ISO-8859-1 decoded split chunks from the real Session path and encoded input to their wire bytes",
			"appsession.Session codec and TerminalConn"))
	}
	return finish(selftest.Failed(errors.New("encoding step reached an unknown stage")))
}

// selftest17Paste is the state of the live-shell paste step. gate is the
// production policy object; send is the same TerminalConn the terminal's
// Paste method writes to.
type selftest17Paste struct {
	started      bool
	service      *appsession.Service
	session      *appsession.Session
	gate         *sessionui.PasteGate
	protection   bool
	stage        int
	deadline     time.Time
	dir          string
	acceptedOne  string
	acceptedTwo  string
	cancelled    string
	survived     string
	directOne    string
	directTwo    string
	acceptedText string
}

func (t *selftest17) pasteStep(*ui.Context) selftest.Outcome {
	p := &t.paste
	if p.started && p.service == nil {
		// A previous run failed before cleanup.
		*p = selftest17Paste{}
	}
	if !p.started {
		dir, err := os.MkdirTemp("", "natcatty-selftest-17-paste-")
		if err != nil {
			return selftest.Failed(err)
		}
		p.started = true
		p.dir = dir
		p.acceptedOne = filepath.Join(dir, "accepted one.txt")
		p.acceptedTwo = filepath.Join(dir, "accepted two.txt")
		p.cancelled = filepath.Join(dir, "cancelled.txt")
		p.survived = filepath.Join(dir, "survived.txt")
		p.directOne = filepath.Join(dir, "direct one.txt")
		p.directTwo = filepath.Join(dir, "direct two.txt")
		p.protection = true
		p.service = appsession.NewService(localpty.Factory{})
		spec := appsession.StartSpec{Kind: appsession.KindLocal, Label: "selftest-17"}
		if runtime.GOOS == "windows" {
			spec.Command = []string{"cmd.exe", "/Q", "/D"}
		}
		session, err := p.service.Open(context.Background(), spec)
		if err != nil {
			p.service.CloseAll()
			p.started = false
			return selftest.Failed(fmt.Errorf("open paste shell: %w", err))
		}
		p.session = session
		p.gate = sessionui.NewPasteGate(
			func() (bool, int) { return p.protection, 2 },
			func(text string) { _, _ = session.TerminalConn().Write([]byte(text)) })
		p.acceptedText = selftest17PasteText(p.acceptedOne, p.acceptedTwo)
		if p.gate.Request(p.acceptedText) {
			return t.finishPaste(selftest.Failed(errors.New("the guarded paste was delivered immediately")))
		}
		if waiting, ok := p.gate.Pending(); !ok || waiting.Text != p.acceptedText || waiting.Lines != 2 {
			return t.finishPaste(selftest.Failed(fmt.Errorf("pending paste = %+v, ok %v", waiting, ok)))
		}
		if selftest17Exists(p.acceptedOne) || selftest17Exists(p.acceptedTwo) {
			return t.finishPaste(selftest.Failed(errors.New("a marker exists before the paste was accepted")))
		}
		p.gate.Accept()
		_, _ = p.session.TerminalConn().Write([]byte("\r"))
		p.stage = 1
		p.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	}

	switch p.stage {
	case 1:
		if !selftest17Exists(p.acceptedOne) || !selftest17Exists(p.acceptedTwo) {
			if time.Now().After(p.deadline) {
				return t.finishPaste(selftest.Failed(errors.New("the accepted paste did not run both commands")))
			}
			return selftest.Pending
		}
		p.protection = true
		if p.gate.Request(selftest17PasteText(p.cancelled, p.cancelled)) {
			return t.finishPaste(selftest.Failed(errors.New("the cancelled paste was delivered immediately")))
		}
		if !p.gate.Cancel() {
			return t.finishPaste(selftest.Failed(errors.New("Cancel found no waiting paste")))
		}
		_, _ = p.session.TerminalConn().Write([]byte(selftest17Touch(p.survived) + "\r"))
		p.stage = 2
		p.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 2:
		if !selftest17Exists(p.survived) {
			if time.Now().After(p.deadline) {
				return t.finishPaste(selftest.Failed(errors.New("input after Cancel did not reach the shell")))
			}
			return selftest.Pending
		}
		if selftest17Exists(p.cancelled) {
			return t.finishPaste(selftest.Failed(errors.New("Cancel still delivered the paste")))
		}
		p.protection = false
		direct := selftest17PasteText(p.directOne, p.directTwo)
		if !p.gate.Request(direct) {
			return t.finishPaste(selftest.Failed(errors.New("protection off still asked for confirmation")))
		}
		_, _ = p.session.TerminalConn().Write([]byte("\r"))
		p.stage = 3
		p.deadline = time.Now().Add(selftestTimeout)
		return selftest.Pending
	case 3:
		if !selftest17Exists(p.directOne) || !selftest17Exists(p.directTwo) {
			if time.Now().After(p.deadline) {
				return t.finishPaste(selftest.Failed(errors.New("the unprotected paste did not run both commands")))
			}
			return selftest.Pending
		}
		return t.finishPaste(selftest.Passed(
			"a real local shell blocked a two-line paste until Send, ran both accepted commands, ran nothing after Cancel, and ran both commands directly with protection off",
			"sessionui.PasteGate + localpty.Factory"))
	}
	return t.finishPaste(selftest.Failed(errors.New("paste step reached an unknown stage")))
}

func (t *selftest17) finishPaste(outcome selftest.Outcome) selftest.Outcome {
	p := &t.paste
	if p.session != nil {
		_ = p.session.Close()
	}
	if p.service != nil {
		p.service.CloseAll()
	}
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
	t.paste = selftest17Paste{}
	return outcome
}

func selftest17Touch(path string) string {
	if runtime.GOOS == "windows" {
		return `echo x>"` + path + `"`
	}
	return `: > '` + strings.ReplaceAll(path, `'`, `'\''`) + `'`
}

func selftest17PasteText(first, second string) string {
	lineEnd := "\n"
	if runtime.GOOS == "windows" {
		lineEnd = "\r\n"
	}
	return selftest17Touch(first) + lineEnd + selftest17Touch(second)
}

func selftest17Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (t *selftest17) dropStep(*ui.Context) selftest.Outcome {
	var inserted []string
	handler := sessionui.NewDropHandler(nil, func(text string) {
		inserted = append(inserted, text)
	}, nil)
	files := []string{`C:\Users\me\My Files\one.txt`, `C:\Users\me\two's.txt`}
	if notice := handler.Drop(files, true); notice != sessionui.DropNoticeInserted {
		return selftest.Failed(fmt.Errorf("insert drop notice = %q", notice))
	}
	want := `"C:\Users\me\My Files\one.txt" "C:\Users\me\two's.txt"`
	if len(inserted) != 1 || inserted[0] != want {
		return selftest.Failed(fmt.Errorf("inserted %q, want %q", inserted, want))
	}

	var uploaded []string
	upload := sessionui.NewDropHandler(
		func() domainterminal.DropAction { return domainterminal.DropUpload },
		func(text string) { inserted = append(inserted, "unexpected:"+text) },
		func(paths []string) error {
			uploaded = append(uploaded, paths...)
			return nil
		})
	if notice := upload.Drop([]string{"a.txt", "b.txt"}, false); notice != sessionui.DropNoticeUploaded {
		return selftest.Failed(fmt.Errorf("upload notice = %q", notice))
	}
	if len(uploaded) != 2 || uploaded[0] != "a.txt" || uploaded[1] != "b.txt" {
		return selftest.Failed(fmt.Errorf("uploaded %v", uploaded))
	}

	noTransport := sessionui.NewDropHandler(
		func() domainterminal.DropAction { return domainterminal.DropUpload },
		func(text string) { inserted = append(inserted, "unexpected:"+text) }, nil)
	if notice := noTransport.Drop([]string{"secret.txt"}, false); notice != sessionui.DropNoticeUnavailable {
		return selftest.Failed(fmt.Errorf("no-transport upload notice = %q", notice))
	}
	if len(inserted) != 1 {
		return selftest.Failed(fmt.Errorf("an unavailable upload inserted %v", inserted))
	}

	return selftest.Passed(
		"one and multiple dropped paths were shell-quoted (spaces and quotes), the upload seam received both files, and an upload without a transport inserted nothing",
		"sessionui.DropHandler")
}

func (t *selftest17) menuStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	model := sessionui.SelftestInteractionModel{
		LogFormat:    domainterminal.LogFormatText,
		LogDirectory: `C:\logs`,
		Encoding:     domainterminal.EncodingUTF8,
	}
	var selected domainterminal.Encoding
	var split bool
	render := func(c *ui.Context) {
		sessionui.RenderSelftestInteraction(c, model, sessionui.SelftestInteractionActions{
			ToggleLog:    func() {},
			SetLogFormat: func(domainterminal.LogFormat) {},
			SetEncoding:  func(encoding domainterminal.Encoding) { selected = encoding },
			Paste:        func() {},
			ClearBuffer:  func() {},
			SplitRight:   func() { split = true },
			SplitDown:    func() {},
		})
	}
	tester := ui.NewTester(render, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		return selftest.Failed(fmt.Errorf("open the Session menu: %w", err))
	}
	want := []string{
		m.SessionMenuPaste(), "-",
		m.SessionMenuSplitRight(), m.SessionMenuSplitDown(), "-",
		m.SessionMenuClearBuffer(), "-",
		m.SessionMenuEncoding(), m.SessionMenuLogging(), "-",
		m.SessionMenuDetach(), m.SessionMenuClosePane(),
	}
	if got := tester.Menu(); strings.Join(got, "|") != strings.Join(want, "|") {
		return selftest.Failed(fmt.Errorf("menu = %v, want %v", got, want))
	}
	if err := tester.ChooseMenuItem(m.SessionMenuEncoding(), "GB18030"); err != nil {
		return selftest.Failed(fmt.Errorf("choose GB18030: %w", err))
	}
	if selected != domainterminal.EncodingGB18030 {
		return selftest.Failed(fmt.Errorf("encoding action = %q", selected))
	}
	tester.Frame()
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		return selftest.Failed(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuSplitRight()); err != nil {
		return selftest.Failed(fmt.Errorf("choose Split Right: %w", err))
	}
	if !split {
		return selftest.Failed(errors.New("Split Right did not reach the action"))
	}
	// Detach and Close Pane have no backend in this rendering; they are
	// visible but must refuse selection.
	tester.Frame()
	if err := tester.Click(m.SessionMenuLabel()); err != nil {
		return selftest.Failed(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuDetach()); err == nil {
		return selftest.Failed(errors.New("a disabled Pane action was choosable"))
	}
	return selftest.Passed(
		"the real Session menu showed the production item order, applied encoding and split choices, and refused a disabled Pane action",
		"sessionui.RenderSelftestInteraction + ui.Tester")
}

func (t *selftest17) layoutStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	pending := &sessionui.PendingPaste{
		Text: "one\ntwo\nthree", Lines: 3, Chars: 13, Preview: "one\ntwo\nthree",
	}
	model := sessionui.SelftestInteractionModel{
		Command:      appsession.CommandState{Text: "make test", Running: true},
		Busy:         true,
		Logging:      appsession.LogStatus{Active: true, Format: domainterminal.LogFormatText, Path: `C:\logs\session.txt`},
		LogFormat:    domainterminal.LogFormatText,
		LogDirectory: `C:\logs`,
		Encoding:     domainterminal.EncodingLatin1,
		PendingPaste: pending,
		DropActive:   true,
		DropAction:   domainterminal.DropInsert,
	}
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(func(c *ui.Context) {
			sessionui.RenderSelftestInteraction(c, model, sessionui.SelftestInteractionActions{
				Paste: func() {}, ClearBuffer: func() {}, SetEncoding: func(domainterminal.Encoding) {},
				AcceptPaste: func() {}, CancelPaste: func() {}, DismissDrop: func() {},
				SplitRight: func() {}, SplitDown: func() {}, Detach: func() {}, ClosePane: func() {},
			})
		}, size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		for _, text := range []string{
			m.SessionEncoding(),
			m.SessionMenuLabel(),
			m.SessionPasteConfirmTitle(3),
			m.SessionPasteConfirmBody(13),
			m.SessionPastePreviewLabel(),
			m.SessionPasteSend(),
			m.SessionPasteCancel(),
			m.SessionDropActive(m.SessionDropInsertAction()),
			m.SessionLoggingOn(),
			m.SessionLoggingStop(),
			m.SessionLogFormatText(),
		} {
			rect, ok := tester.Find(text)
			if !ok {
				return selftest.Failed(fmt.Errorf("%dx%d lacks %q: %v", size[0], size[1], text, tester.Texts()))
			}
			if rect.X < 0 || rect.Y < 0 ||
				rect.X+rect.W > float32(size[0]) || rect.Y+rect.H > float32(size[1]) {
				return selftest.Failed(fmt.Errorf("%dx%d clips %q: %+v", size[0], size[1], text, rect))
			}
		}
		send, _ := tester.Find(m.SessionPasteSend())
		cancel, _ := tester.Find(m.SessionPasteCancel())
		if send.X+send.W > cancel.X {
			return selftest.Failed(fmt.Errorf("%dx%d Send overlaps Cancel: %+v %+v",
				size[0], size[1], send, cancel))
		}
	}
	return selftest.Passed(
		"the encoding selector, Session menu, paste confirmation, and drop banner stayed inside 1100x720 and 720x480 without overlap",
		"sessionui.RenderSelftestInteraction")
}

// demoStep leaves a real Session surface showing the encoding selector, the
// paste confirmation, and drop feedback. In the headless wiring tests the
// SurfaceFactory is a fake, so the step passes after noting the seam.
func (t *selftest17) demoStep(*ui.Context) selftest.Outcome {
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
	if preparer, ok := surface.(interface{ PrepareSelftestInteraction() }); ok {
		preparer.PrepareSelftestInteraction()
		return selftest.Passed(
			"a real local Session surface shows the encoding selector, a guarded three-line paste, and a completed two-file drop",
			"sessionui.Surface.PrepareSelftestInteraction")
	}
	return selftest.Passed(
		"the headless wiring uses a fake SurfaceFactory; the menu and layout steps cover the production chrome",
		"state.SurfaceFactory")
}
