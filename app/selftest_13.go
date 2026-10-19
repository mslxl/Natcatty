//go:build selftest

package app

// Ticket 13 self-test.
//
// Shortcut (plain keys, no modifiers): press F11, then 1, then 3. F11 opens
// the overlay listing the registered checks; Escape cancels. (Plain F12
// belongs to MyGo's inspector in a normal build, so the facility uses F11.)
// The check runs
// on the UI thread one step per frame, so the event loop is never blocked.
//
// Result: %TEMP%\natcatty-selftest-13.json (os.TempDir on other platforms),
// holding pass, steps, errors, and timestamp. The overlay shows the same
// PASS/FAIL status and the path.
//
// The check exercises the production detached-window, single-instance, close
// intent, and App Lock paths. Two steps replace a real modal surface with the
// seams the production design already owns: the close-intent step swaps
// dialogConfirmer, and the App Lock step swaps the shared uiapplock.Gate. Both
// steps name the seam in their recorded detail, so no native dialog blocks
// the run. Everything else uses the real window host, State, Session service,
// and window close hooks.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo/ui"

	appapplock "github.com/mslxl/natcatty/application/applock"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
	"github.com/mslxl/natcatty/domain/model"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/shell/state"
)

const (
	selftest13Ticket = 13
	selftestTimeout  = 10 * time.Second
	selftestPassword = "natcatty-selftest"
)

// activeSelftest is installed once by installSelftest. The terminal key
// bridge reads it on the UI thread.
var activeSelftest *selftest.Harness

// selftestResultDir is os.TempDir in the app; the selftest-tagged tests point
// it at a temporary directory so they do not write the documented %TEMP% file.
var selftestResultDir = os.TempDir()

// installSelftest registers ticket 13's check and attaches the overlay to the
// main window. It is called only from selftest-tagged builds.
func installSelftest(windows *windowManager, shellState *state.State, secondInstance func()) {
	run := &selftest13{
		windows:        windows,
		state:          shellState,
		secondInstance: secondInstance,
	}
	registry := selftest.NewRegistry()
	registry.Register(run.check())
	activeSelftest = selftest.NewHarness(registry,
		selftest.WithResultDir(selftestResultDir),
	)
	windows.selftestBind = activeSelftest.Bind
	windows.selftestView = activeSelftest.View
}

// selftest13 keeps the state the ticket's steps share.
type selftest13 struct {
	windows        *windowManager
	state          *state.State
	secondInstance func()

	detachedTabsBefore int
	mainTab            model.TabID
	sourceTab          model.TabID
	detachedA          model.TabID
	detachedB          model.TabID
}

func (t *selftest13) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest13Ticket,
		Name:   "Detached windows and single-instance behavior",
		Steps: []selftest.Step{
			t.createSessionStep(),
			t.detachStep(),
			t.copyToWindowStep(),
			t.secondInstanceStep(),
			t.closeIntentStep(),
			t.appLockStep(),
			t.cleanupStep(),
		},
	}
}

// createSessionStep opens a local shell through the real Session service.
func (t *selftest13) createSessionStep() selftest.Step {
	var created bool
	var deadline time.Time
	return selftest.Step{
		Name: "create-session",
		Run: func(*ui.Context) selftest.Outcome {
			t.state.Flush()
			if !created {
				t.detachedTabsBefore = len(t.state.DetachedTabIDs())
				if err := t.state.NewTab(); err != nil {
					return selftest.Failed(fmt.Errorf("open a local shell: %w", err))
				}
				index := t.state.ActiveTab()
				id, ok := t.state.TabID(index)
				if !ok {
					return selftest.Failed(errors.New("the new Tab has no identity"))
				}
				t.mainTab = id
				created = true
				deadline = time.Now().Add(selftestTimeout)
			}
			if mainSurface(t.state, t.mainTab) == nil {
				return pending(deadline, "the local shell Session")
			}
			return selftest.Passed("a real local shell Session is running", "")
		},
	}
}

// detachStep moves the Tab into a real detached window and proves its
// Session stayed alive.
func (t *selftest13) detachStep() selftest.Step {
	var moved bool
	var deadline time.Time
	return selftest.Step{
		Name: "detach-tab",
		Run: func(*ui.Context) selftest.Outcome {
			t.state.Flush()
			if !moved {
				index := tabIndex(t.state, t.mainTab)
				if index < 0 {
					return selftest.Failed(errors.New("the Session Tab vanished before detaching"))
				}
				if !t.state.DetachTab(index) {
					return selftest.Failed(errors.New("DetachTab rejected the active Tab"))
				}
				ids := t.state.DetachedTabIDs()
				if len(ids) != t.detachedTabsBefore+1 {
					return selftest.Failed(fmt.Errorf("detached Tabs = %d, want %d",
						len(ids), t.detachedTabsBefore+1))
				}
				t.detachedA = ids[len(ids)-1]
				moved = true
				deadline = time.Now().Add(selftestTimeout)
			}
			if !t.state.DetachedTabExists(t.detachedA) {
				return selftest.Failed(errors.New("the detached Tab disappeared"))
			}
			if t.state.ActiveSessionCountDetached(t.detachedA) == 0 ||
				detachedSurface(t.state, t.detachedA) == nil {
				return pending(deadline, "the detached Session")
			}
			window := t.windows.detached[t.detachedA]
			if window == nil || window.IsDestroyed() {
				return pending(deadline, "the detached window")
			}
			return selftest.Passed(
				"the Session kept running in a real detached window", "")
		},
	}
}

// copyToWindowStep duplicates a Tab into a new window with fresh Sessions and
// leaves the source Tab in the main Workspace.
func (t *selftest13) copyToWindowStep() selftest.Step {
	var created, copied bool
	var deadline time.Time
	return selftest.Step{
		Name: "copy-tab-to-window",
		Run: func(*ui.Context) selftest.Outcome {
			t.state.Flush()
			if !created {
				if err := t.state.NewTab(); err != nil {
					return selftest.Failed(fmt.Errorf("open the copy source: %w", err))
				}
				index := t.state.ActiveTab()
				id, ok := t.state.TabID(index)
				if !ok {
					return selftest.Failed(errors.New("the copy source has no identity"))
				}
				t.sourceTab = id
				created = true
				deadline = time.Now().Add(selftestTimeout)
			}
			sourceIndex := tabIndex(t.state, t.sourceTab)
			if sourceIndex < 0 {
				return selftest.Failed(errors.New("the copy source Tab disappeared"))
			}
			sourceSurface := mainSurface(t.state, t.sourceTab)
			if sourceSurface == nil {
				return pending(deadline, "the copy source Session")
			}
			if !copied {
				if err := t.state.DuplicateTabToWindow(sourceIndex); err != nil {
					return selftest.Failed(fmt.Errorf("copy the Tab to a new window: %w", err))
				}
				ids := t.state.DetachedTabIDs()
				if len(ids) != t.detachedTabsBefore+2 {
					return selftest.Failed(fmt.Errorf("detached Tabs = %d, want %d",
						len(ids), t.detachedTabsBefore+2))
				}
				t.detachedB = ids[len(ids)-1]
				if t.detachedB == t.detachedA {
					return selftest.Failed(errors.New("the copy reused the detached Tab"))
				}
				copied = true
				deadline = time.Now().Add(selftestTimeout)
			}
			if tabIndex(t.state, t.sourceTab) < 0 {
				return selftest.Failed(errors.New("copying moved the source Tab"))
			}
			copySurface := detachedSurface(t.state, t.detachedB)
			if copySurface == nil || t.state.ActiveSessionCountDetached(t.detachedB) == 0 {
				return pending(deadline, "the copied Session")
			}
			if copySurface == sourceSurface {
				return selftest.Failed(errors.New("the copy shares the source Session"))
			}
			window := t.windows.detached[t.detachedB]
			if window == nil || window.IsDestroyed() {
				return pending(deadline, "the copied window")
			}
			return selftest.Passed(
				"the copy opened fresh Sessions in a new window and left the source Tab", "")
		},
	}
}

// secondInstanceStep runs the same callback MyGo runs when a later launch
// hands over, and observes that it raised the existing main window.
func (t *selftest13) secondInstanceStep() selftest.Step {
	return selftest.Step{
		Name: "second-instance-focus",
		Run: func(*ui.Context) selftest.Outcome {
			if t.secondInstance == nil {
				return selftest.Failed(errors.New("the single-instance callback is not installed"))
			}
			main := t.windows.Main()
			if main == nil || main.IsDestroyed() {
				return selftest.Failed(errors.New("the main window is not open"))
			}
			before := t.windows.mainFocuses
			t.secondInstance()
			if t.windows.Main() != main {
				return selftest.Failed(errors.New("the second launch replaced the main window"))
			}
			if t.windows.mainFocuses != before+1 {
				return selftest.Failed(fmt.Errorf("focus requests = %d, want %d",
					t.windows.mainFocuses, before+1))
			}
			return selftest.Passed(
				"the single-instance callback raised the existing main window", "")
		},
	}
}

// closeIntentStep uses the dialogConfirmer seam: deny must keep the window
// open, and allow must proceed, without a native dialog blocking automation.
func (t *selftest13) closeIntentStep() selftest.Step {
	return selftest.Step{
		Name: "close-intent",
		Run: func(*ui.Context) selftest.Outcome {
			if t.state.ActiveSessionCountMain() == 0 {
				return selftest.Failed(errors.New("there is no active main Session to gate"))
			}
			saved := t.windows.confirmer
			seam := &scriptedConfirmer{answers: []bool{false, true}}
			t.windows.confirmer = seam
			denied := t.windows.allowMainClose()
			allowed := t.windows.allowMainClose()
			t.windows.confirmer = saved

			if denied {
				return selftest.Failed(errors.New("the main window closed without consent"))
			}
			if !allowed {
				return selftest.Failed(errors.New("the main window stayed open after consent"))
			}
			if len(seam.prompts) != 2 {
				return selftest.Failed(fmt.Errorf("close-intent prompts = %d, want 2", len(seam.prompts)))
			}
			m := locales.In("en")
			for _, prompt := range seam.prompts {
				if prompt.Title != m.CloseWindowTitle() {
					return selftest.Failed(fmt.Errorf("prompt title = %q, want %q",
						prompt.Title, m.CloseWindowTitle()))
				}
			}
			return selftest.Passed(
				"deny kept the window open and allow proceeded; dialogConfirmer seam used, no native dialog shown",
				"dialogConfirmer")
		},
	}
}

// appLockStep uses the shared-gate seam: it swaps the window manager's gate
// for a real Gate and App Lock Service backed by an in-memory repository, then
// renders the production detached-window content and confirms the lock screen
// hides it.
func (t *selftest13) appLockStep() selftest.Step {
	return selftest.Step{
		Name: "app-lock-detached",
		Run: func(*ui.Context) selftest.Outcome {
			if !t.state.DetachedTabExists(t.detachedA) {
				return selftest.Failed(errors.New("the detached Tab was closed before the App Lock check"))
			}
			verifier, err := domainapplock.NewVerifier(selftestPassword, rand.Reader)
			if err != nil {
				return selftest.Failed(fmt.Errorf("build the App Lock verifier: %w", err))
			}
			repository := &selftestLockRepository{settings: domainapplock.Settings{
				Enabled:          true,
				TimeoutMinutes:   15,
				PasswordVerifier: verifier,
			}}
			service := appapplock.NewService(repository)
			if err := service.Load(context.Background()); err != nil {
				return selftest.Failed(fmt.Errorf("load App Lock: %w", err))
			}
			if !service.Snapshot().Locked {
				return selftest.Failed(errors.New("an enabled App Lock did not start locked"))
			}

			// Render the exact content function the live detached window was
			// given, under a gate that is locked, so this proves the window's
			// own surface is hidden rather than a rebuilt copy of it.
			content := t.windows.detachedViews[t.detachedA]
			if content == nil {
				return selftest.Failed(errors.New("the detached window has no recorded content"))
			}
			saved := t.windows.gate
			t.windows.gate = uiapplock.NewGate(service, uiapplock.WithRedrawRequest(func() {}))
			tester := ui.NewTester(content, 900, 600)
			tester.SetLocale("en")
			tester.Frame()
			m := locales.In("en")
			hidden := tester.HasText(m.AppLockTitle()) && !tester.HasText(m.DetachedMoveToMain())
			unlockErr := service.Unlock(selftestPassword)
			unlocked := !service.Snapshot().Locked
			t.windows.gate = saved

			if !hidden {
				return selftest.Failed(errors.New(
					"the detached window rendered its content while App Lock was locked"))
			}
			if unlockErr != nil || !unlocked {
				return selftest.Failed(fmt.Errorf("unlock the App Lock seam: %v", unlockErr))
			}
			return selftest.Passed(
				"the shared gate showed only the lock screen for detached content; uiapplock.Gate seam used",
				"uiapplock.Gate")
		},
	}
}

// cleanupStep closes the detached windows through their real native close
// hook and removes the Tabs this check created, so the app returns to its
// prior state.
func (t *selftest13) cleanupStep() selftest.Step {
	var started bool
	var deadline time.Time
	return selftest.Step{
		Name: "close-detached-windows",
		Run: func(*ui.Context) selftest.Outcome {
			t.state.Flush()
			if !started {
				// Closing a window with an active Session asks for intent;
				// this seam answers yes so no dialog blocks the check.
				saved := t.windows.confirmer
				t.windows.confirmer = &scriptedConfirmer{answers: []bool{true, true}}
				for _, id := range []model.TabID{t.detachedA, t.detachedB} {
					if id == "" || !t.state.DetachedTabExists(id) {
						continue
					}
					if window := t.windows.detached[id]; window != nil && !window.IsDestroyed() {
						window.Close()
						continue
					}
					// The window was already gone; close its Tab so the
					// Session does not outlive the check.
					t.state.CloseDetachedTab(id)
				}
				if index := tabIndex(t.state, t.sourceTab); index >= 0 {
					t.state.CloseTab(index)
				}
				t.sourceTab = ""
				// Restore the real confirmer now that the windows have asked.
				t.windows.confirmer = saved
				started = true
				deadline = time.Now().Add(selftestTimeout)
			}

			remaining := 0
			for _, id := range []model.TabID{t.detachedA, t.detachedB} {
				if id != "" && t.state.DetachedTabExists(id) {
					remaining++
				}
			}
			if remaining == 0 {
				return selftest.Passed(
					"the real window close hook removed both detached Tabs and ended their Sessions; dialogConfirmer seam used for close intent",
					"dialogConfirmer")
			}
			return pending(deadline, "the detached windows to close")
		},
	}
}

// scriptedConfirmer is the injectable close-intent seam used by the check. It
// records the prompts the production code builds and answers from a script.
type scriptedConfirmer struct {
	answers []bool
	prompts []closePrompt
	index   int
}

func (c *scriptedConfirmer) Confirm(prompt closePrompt) bool {
	c.prompts = append(c.prompts, prompt)
	answer := false
	if c.index < len(c.answers) {
		answer = c.answers[c.index]
	}
	c.index++
	return answer
}

// selftestLockRepository is the App Lock repository port backed by memory, so
// the check can lock and unlock without touching the user's settings file.
type selftestLockRepository struct {
	settings domainapplock.Settings
}

func (r *selftestLockRepository) Load(context.Context) (domainapplock.Settings, error) {
	return r.settings, nil
}

func (r *selftestLockRepository) Save(_ context.Context, settings domainapplock.Settings) error {
	r.settings = settings
	return nil
}

func pending(deadline time.Time, what string) selftest.Outcome {
	if time.Now().After(deadline) {
		return selftest.Failed(fmt.Errorf("timed out after %s waiting for %s", selftestTimeout, what))
	}
	return selftest.Pending
}

func tabIndex(s *state.State, id model.TabID) int {
	if id == "" {
		return -1
	}
	for index := 0; index < s.TabCount(); index++ {
		if got, ok := s.TabID(index); ok && got == id {
			return index
		}
	}
	return -1
}

// mainSurface returns the focused Pane's live surface in the main Tab.
func mainSurface(s *state.State, id model.TabID) state.Surface {
	index := tabIndex(s, id)
	if index < 0 {
		return nil
	}
	paneID, ok := s.FocusedPaneID(index)
	if !ok {
		return nil
	}
	info, ok := s.PaneInfoAt(index, paneID)
	if !ok {
		return nil
	}
	return info.Surface
}

// detachedSurface returns the detached Tab's first live surface.
func detachedSurface(s *state.State, id model.TabID) state.Surface {
	info, ok := s.DetachedTabInfo(id)
	if !ok {
		return nil
	}
	for _, paneID := range info.Panes {
		pane, ok := s.DetachedPaneInfoAt(id, paneID)
		if ok && pane.Surface != nil {
			return pane.Surface
		}
	}
	return nil
}
