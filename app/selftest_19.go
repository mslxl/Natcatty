//go:build selftest

package app

// Ticket 19 self-test. Press F11, then 1, then 9. The check proves that a real
// local shell survives hibernate and resume with its state, that the observer
// renders the owning Session's screen and live feed through a shadow terminal,
// that neither path creates a second live terminal view, and that both entry
// points and the observer fit the supported window sizes.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-19.json. The steps close the Session they
// opened and remove their temporary directory, so F11,1,9 passes twice in one
// process.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

const (
	selftest19Ticket  = 19
	selftest19Timeout = 30 * time.Second
	selftest19Before  = "NATCAT_HIBERNATE_BEFORE"
	selftest19Observe = "NATCAT_OBSERVER_MARKER"
	selftest19Live    = "NATCAT_OBSERVER_LIVE"
)

type selftest19 struct {
	started  bool
	stage    int
	deadline time.Time

	dir     string
	inner   string
	service *appsession.Service
	session *appsession.Session
	surface *sessionui.Surface
	tester  *ui.Tester

	before appsession.Snapshot
}

func (t *selftest19) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest19Ticket,
		Name:   "Session hibernate and observer snapshots",
		Steps: []selftest.Step{
			{Name: "hibernate-resume", Run: t.hibernateStep},
			{Name: "observer-snapshot", Run: t.observerStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest19) hibernateStep(*ui.Context) selftest.Outcome {
	if t.started && t.service == nil {
		// A previous run failed before cleanup.
		t.started = false
	}
	if !t.started {
		return t.startShell()
	}

	switch t.stage {
	case 1:
		if !selftest19Exists(filepath.Join(t.inner, "before.txt")) ||
			!strings.Contains(t.surface.ScreenText(), selftest19Before) {
			return pending(t.deadline, "the shell to change directory and print its marker")
		}
		t.before = t.session.Snapshot()
		m := locales.In("en")
		if err := t.tester.Click(m.SessionMenuFor(1)); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("open the Session menu: %w", err)))
		}
		if err := t.tester.ChooseMenuItem(m.SessionMenuHibernate()); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("Hibernate: %w", err)))
		}
		t.tester.Frame()
		if !t.surface.Hibernated() {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the surface did not hibernate: %v", t.tester.Texts())))
		}
		if got := t.session.LiveViews(); got != 0 {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"hibernate kept %d live views", got)))
		}
		if text := t.surface.ScreenText(); text != "" {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"a hibernated surface still renders %q", text)))
		}
		if !t.tester.HasText(m.SessionHibernatedTitle()) {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the hibernated placeholder is missing: %v", t.tester.Texts())))
		}
		hibernated := t.session.Snapshot()
		if !hibernated.Hibernated {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the Session snapshot does not report hibernation")))
		}
		if hibernated.ID != t.before.ID || hibernated.Kind != t.before.Kind ||
			hibernated.Status != t.before.Status ||
			hibernated.Connection != t.before.Connection {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"hibernate changed the Session: before %+v, after %+v", t.before, hibernated)))
		}
		t.stage = 2
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 2:
		// The transport is still alive while the screen is hibernated: a
		// command typed through the Session's own duplex runs in the shell.
		if _, err := t.session.TerminalConn().Write([]byte(
			selftest19Touch("during.txt") + "\r")); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"write while hibernated: %w", err)))
		}
		t.stage = 3
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 3:
		if !selftest19Exists(filepath.Join(t.inner, "during.txt")) {
			return pending(t.deadline, "the shell to run a command while hibernated")
		}
		m := locales.In("en")
		if err := t.tester.Click(m.SessionResume()); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("Resume: %w", err)))
		}
		t.tester.Frame()
		t.stage = 4
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 4:
		if !strings.Contains(t.surface.ScreenText(), selftest19Before) {
			return pending(t.deadline, "the resumed screen to replay the hibernated output")
		}
		if t.surface.Hibernated() {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"Resume left the surface hibernated")))
		}
		if got := t.session.LiveViews(); got != 1 {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"Resume left %d live views, want 1", got)))
		}
		after := t.session.Snapshot()
		if after.Hibernated {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the Session still reports hibernation after Resume")))
		}
		if after.ID != t.before.ID || after.Kind != t.before.Kind ||
			after.Status != t.before.Status ||
			after.Connection != t.before.Connection ||
			after.Cols != t.before.Cols || after.Rows != t.before.Rows ||
			after.Encoding != t.before.Encoding {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"Resume changed the Session: before %+v, after %+v", t.before, after)))
		}
		// The resumed view writes to the same shell with its working
		// directory intact: a relative marker lands in "inner".
		t.tester.ClickAt(450, 560)
		t.tester.Type(selftest19Touch("after_resume.txt"))
		t.tester.Key(0, ui.KeyEnter)
		t.stage = 5
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 5:
		if !selftest19Exists(filepath.Join(t.inner, "after_resume.txt")) {
			return pending(t.deadline, "the same shell to run a command after resume")
		}
		if !selftest19Exists(filepath.Join(t.inner, "during.txt")) {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the command run while hibernated did not survive")))
		}
		t.stage = 0
		return selftest.Passed(
			"a real local shell kept its process and working directory through Hibernate and Resume, output produced while hibernated replayed, and the Session had one live view after Resume",
			"sessionui.Surface + session.Hibernate/Resume + localpty.Factory")
	}
	return t.finishWith(selftest.Failed(fmt.Errorf(
		"hibernate-resume reached unknown stage %d", t.stage)))
}

func (t *selftest19) observerStep(*ui.Context) selftest.Outcome {
	if t.session == nil || t.surface == nil || t.tester == nil {
		return t.finishWith(selftest.Failed(fmt.Errorf(
			"the shell Session from the hibernate step is missing")))
	}
	m := locales.In("en")
	switch t.stage {
	case 0:
		t.tester.ClickAt(450, 560)
		t.tester.Type("echo " + selftest19Observe)
		t.tester.Key(0, ui.KeyEnter)
		t.stage = 1
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 1:
		if !strings.Contains(t.surface.ScreenText(), selftest19Observe) {
			return pending(t.deadline, "the shell to print the observer marker")
		}
		t.before = t.session.Snapshot()
		if err := t.tester.Click(m.SessionMenuFor(1)); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("open the Session menu: %w", err)))
		}
		if err := t.tester.ChooseMenuItem(m.SessionMenuObserveSnapshot()); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("Observer snapshot: %w", err)))
		}
		t.tester.Frame()
		if !t.surface.Observing() {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the observer did not open: %v", t.tester.Texts())))
		}
		if got := t.session.LiveViews(); got != 1 {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the observer claimed a live view: %d live views", got)))
		}
		screen, ok := t.surface.ObserverScreen()
		if !ok {
			return t.finishWith(selftest.Failed(fmt.Errorf("the observer has no snapshot")))
		}
		if screen.SessionID != t.before.ID {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"snapshot Session %q, want the owner %q", screen.SessionID, t.before.ID)))
		}
		if screen.Cols != t.before.Cols || screen.Rows != t.before.Rows {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"snapshot size %dx%d, want the owner's %dx%d",
				screen.Cols, screen.Rows, t.before.Cols, t.before.Rows)))
		}
		if !strings.Contains(string(screen.Data), selftest19Observe) {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"snapshot data lacks the owner's output: %q", screen.Data)))
		}
		if !t.tester.HasText(m.SessionObserverTitle()) {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the observer chrome is missing: %v", t.tester.Texts())))
		}
		t.stage = 2
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 2:
		if !strings.Contains(t.surface.ObserverText(), selftest19Observe) {
			return pending(t.deadline, "the observer to render the shadow snapshot")
		}
		// Output after the observation arrives through the blocking feed,
		// while the owning view keeps its own stream.
		if _, err := t.session.TerminalConn().Write([]byte(
			"echo " + selftest19Live + "\r")); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"write through the owning Session: %w", err)))
		}
		t.stage = 3
		t.deadline = time.Now().Add(selftest19Timeout)
		return selftest.Pending
	case 3:
		if !strings.Contains(t.surface.ObserverText(), selftest19Live) {
			return pending(t.deadline, "the observer feed to deliver new output")
		}
		if !strings.Contains(t.surface.ScreenText(), selftest19Live) {
			return pending(t.deadline, "the owning screen to receive the same output")
		}
		if got := t.session.LiveViews(); got != 1 {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"observing changed the live view count to %d", got)))
		}
		if err := t.tester.Click(m.SessionObserverClose()); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("Close observer: %w", err)))
		}
		t.tester.Frame()
		if t.surface.Observing() {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the observer stayed open after Close")))
		}
		if _, ok := t.surface.ObserverScreen(); ok {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the observer snapshot survived Close")))
		}
		if got := t.session.LiveViews(); got != 1 {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"closing the observer changed the live view count to %d", got)))
		}
		if !strings.Contains(t.surface.ScreenText(), selftest19Live) {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"the owning screen lost its live output")))
		}
		return selftest.Passed(
			"the observer rendered the owner's snapshot and live feed through its own shadow terminal, never claimed a second live view, and left the owning screen intact after Close",
			"sessionui.Surface + session.Observe + shadow terminal.View")
	}
	return t.finishWith(selftest.Failed(fmt.Errorf(
		"observer-snapshot reached unknown stage %d", t.stage)))
}

func (t *selftest19) layoutStep(*ui.Context) selftest.Outcome {
	if t.surface == nil || t.tester == nil {
		return t.finishWith(selftest.Failed(fmt.Errorf(
			"the shell Session from the hibernate step is missing")))
	}
	m := locales.In("en")
	if !t.surface.Hibernate() {
		return t.finishWith(selftest.Failed(fmt.Errorf("Hibernate failed before layout")))
	}
	t.tester.Frame()
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		t.tester.SetSize(size[0], size[1])
		t.tester.Frame()
		if err := selftest19Inside(t.tester, size[0], size[1],
			m.SessionHibernatedTitle(), m.SessionHibernatedBody(),
			m.SessionResume(), m.SessionMenuObserveSnapshot()); err != nil {
			return t.finishWith(selftest.Failed(err))
		}
		if err := t.surface.OpenObserver(); err != nil {
			return t.finishWith(selftest.Failed(fmt.Errorf("OpenObserver: %w", err)))
		}
		t.tester.Frame()
		if got := t.session.LiveViews(); got != 0 {
			return t.finishWith(selftest.Failed(fmt.Errorf(
				"observing a hibernated Session attached %d live views", got)))
		}
		if err := selftest19Inside(t.tester, size[0], size[1],
			m.SessionObserverTitle(), m.SessionObserverHint(),
			m.SessionObserverClose()); err != nil {
			return t.finishWith(selftest.Failed(err))
		}
		t.surface.CloseObserver()
		t.tester.Frame()
	}
	if err := t.surface.Resume(); err != nil {
		return t.finishWith(selftest.Failed(fmt.Errorf("Resume after layout: %w", err)))
	}
	t.tester.Frame()
	if t.surface.Hibernated() || t.session.LiveViews() != 1 {
		return t.finishWith(selftest.Failed(fmt.Errorf(
			"layout left the Session hibernated or without a view")))
	}
	outcome := selftest.Passed(
		"the hibernated placeholder, Resume, observer controls, and observer screen fit inside 1100x720 and 720x480",
		"sessionui.Surface + ui.Tester")
	return t.finishWith(outcome)
}

// startShell opens a real local shell and issues the first commands through the
// production terminal view.
func (t *selftest19) startShell() selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-19-")
	if err != nil {
		return selftest.Failed(err)
	}
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return selftest.Failed(err)
	}
	t.dir, t.inner = dir, inner
	t.service = appsession.NewService(localpty.Factory{})
	spec := appsession.StartSpec{
		Kind:  appsession.KindLocal,
		Dir:   dir,
		Cols:  100,
		Rows:  30,
		Label: "selftest-19",
	}
	if runtime.GOOS == "windows" {
		spec.Command = []string{"cmd.exe", "/Q", "/D"}
	}
	session, err := t.service.Open(context.Background(), spec)
	if err != nil {
		t.finish()
		return selftest.Failed(fmt.Errorf("open the local shell: %w", err))
	}
	t.session = session
	surface, err := sessionui.New(session, sessionui.WithRedraw(func() {}))
	if err != nil {
		t.finish()
		return selftest.Failed(fmt.Errorf("build the Session surface: %w", err))
	}
	t.surface = surface
	t.surface.SetPaneActions(sessionui.PaneActions{
		Title: "selftest-19", MenuLabel: locales.In("en").SessionMenuFor(1),
	})
	t.tester = ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 620)
	t.tester.SetLocale("en")
	t.tester.Frame()
	t.tester.ClickAt(450, 560)
	t.tester.Type("cd inner")
	t.tester.Key(0, ui.KeyEnter)
	t.tester.Type("echo " + selftest19Before)
	t.tester.Key(0, ui.KeyEnter)
	t.tester.Type(selftest19Touch("before.txt"))
	t.tester.Key(0, ui.KeyEnter)
	t.started = true
	t.stage = 1
	t.deadline = time.Now().Add(selftest19Timeout)
	return selftest.Pending
}

func (t *selftest19) finishWith(outcome selftest.Outcome) selftest.Outcome {
	t.finish()
	return outcome
}

func (t *selftest19) finish() {
	if t.surface != nil {
		t.surface.Close()
	}
	if t.session != nil {
		_ = t.session.Close()
	}
	if t.service != nil {
		t.service.CloseAll()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.started = false
	t.stage = 0
	t.dir, t.inner = "", ""
	t.service, t.session, t.surface, t.tester = nil, nil, nil, nil
}

func selftest19Touch(relative string) string {
	if runtime.GOOS == "windows" {
		return "echo x>" + relative
	}
	return ": > '" + relative + "'"
}

func selftest19Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func selftest19Inside(tester *ui.Tester, width, height int, texts ...string) error {
	for _, text := range texts {
		rect, ok := tester.Find(text)
		if !ok {
			return fmt.Errorf("%dx%d lacks %q: %v", width, height, text, tester.Texts())
		}
		if rect.X < 0 || rect.Y < 0 ||
			rect.X+rect.W > float32(width) || rect.Y+rect.H > float32(height) {
			return fmt.Errorf("%dx%d clips %q: %+v", width, height, text, rect)
		}
	}
	return nil
}
