package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

func newRealTypingState(t *testing.T) *shellstate.State {
	t.Helper()
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	return shellstate.NewState(shellstate.WithSessions(service, func(s *appsession.Session) (shellstate.Surface, error) {
		return sessionui.New(s,
			sessionui.WithRedraw(func() {}),
			sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
			sessionui.WithDropAction(func() domainterminal.DropAction {
				return domainterminal.DropInsert
			}),
			sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
		)
	}))
}

func newRealAutocompleteState(t *testing.T) *shellstate.State {
	t.Helper()
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	return shellstate.NewState(shellstate.WithSessions(service, func(s *appsession.Session) (shellstate.Surface, error) {
		return sessionui.New(s,
			sessionui.WithRedraw(func() {}),
			sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
			sessionui.WithDropAction(func() domainterminal.DropAction {
				return domainterminal.DropInsert
			}),
			sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
			sessionui.WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
				if input == "" {
					return nil
				}
				return []autocomplete.Suggestion{{Text: input + "-suggested", Display: input + "-suggested"}}
			}, func() (bool, bool) { return true, true }),
			sessionui.WithAutocompleteDelay(func() int { return 100 }),
		)
	}))
}

func newRealTypingStateWithVault(t *testing.T) *shellstate.State {
	t.Helper()
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)

	vault := vaultstate.NewState(accelHosts{})
	if err := vault.Reload(); err != nil {
		t.Fatalf("load test Vault: %v", err)
	}

	var shell *shellstate.State
	shell = shellstate.NewState(
		shellstate.WithSessions(service, func(s *appsession.Session) (shellstate.Surface, error) {
			return sessionui.New(s,
				sessionui.WithRedraw(func() {}),
				sessionui.WithPastePolicy(func() (bool, int) { return false, 2 }),
				sessionui.WithDropAction(func() domainterminal.DropAction {
					return domainterminal.DropInsert
				}),
				sessionui.WithEncodingChange(func(domainterminal.Encoding) {}),
			)
		}),
		shellstate.WithVault(vaultui.View(vault, vaultui.WithLocalTerminal(func() {
			_ = shell.NewTab()
		}))),
	)
	t.Cleanup(shell.CloseAllSessions)
	return shell
}

func realSurfaceSession(t *testing.T, st *shellstate.State, tab int) *appsession.Session {
	t.Helper()
	root, _, ok := st.TabTree(tab)
	if !ok {
		t.Fatalf("tab %d has no tree", tab)
	}
	for _, paneID := range domainworkspace.CollectPaneIDs(root) {
		if info, ok := st.PaneInfoAt(tab, paneID); ok && info.Surface != nil {
			if owner, ok := info.Surface.(interface{ Session() *appsession.Session }); ok {
				return owner.Session()
			}
		}
	}
	t.Fatalf("tab %d has no Session surface", tab)
	return nil
}

func waitRealSnapshot(t *testing.T, st *shellstate.State, tab int, predicate func(appsession.Snapshot) bool) appsession.Snapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st.Flush()
		session := realSurfaceSession(t, st, tab)
		snapshot := session.Snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	session := realSurfaceSession(t, st, tab)
	t.Fatalf("timed out waiting for tab %d snapshot: %+v", tab, session.Snapshot())
	return appsession.Snapshot{}
}

func waitForRealShellSurfaces(t *testing.T, st *shellstate.State, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st.Flush()
		live := 0
		for tab := 0; tab < st.TabCount(); tab++ {
			root, _, ok := st.TabTree(tab)
			if !ok {
				continue
			}
			for _, paneID := range domainworkspace.CollectPaneIDs(root) {
				if info, ok := st.PaneInfoAt(tab, paneID); ok && info.Surface != nil {
					live++
				}
			}
		}
		if live == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d real shell surfaces", count)
}

func pumpFrames(tester *ui.Tester, duration time.Duration) {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		tester.Frame()
		time.Sleep(time.Millisecond)
	}
}

// TestRealLocalTerminalInputAfterVaultClickAndFocusTransition exercises the
// actual Vault -> Terminal action, then models the native window-focus
// transition that can overlap the first terminal frame. It deliberately waits
// six seconds before the first text and checks both the Session input line and
// the transport byte counter after Enter.
func TestRealLocalTerminalInputAfterVaultClickAndFocusTransition(t *testing.T) {
	st := newRealTypingStateWithVault(t)
	tester := ui.NewTester(shellui.View(st), 1200, 760)
	tester.SetLocale("en")
	tester.Frame()

	m := locales.In("en")
	if err := tester.Click(m.VaultLocalTerminal()); err != nil {
		t.Fatalf("click Vault Local Terminal: %v", err)
	}
	tester.Frame()
	waitForRealShellSurfaces(t, st, 1)

	// The native window can still be settling focus after the Vault button
	// created the terminal. Exercise the blur/return sequence before typing.
	tester.SetFocused(false)
	tester.SetFocused(true)
	pumpFrames(tester, 6*time.Second)

	before := waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})
	tester.TypeKey(0, ui.KeyE, "echo VAULT_FIRST")
	typed := waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "echo VAULT_FIRST"
	})
	tester.Key(0, ui.KeyEnter)
	after := waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == ""
	})
	if after.Stats.InputBytes <= before.Stats.InputBytes ||
		typed.Stats.InputBytes <= before.Stats.InputBytes {
		t.Fatalf("first Vault-created Terminal input did not reach the transport: before=%+v typed=%+v after=%+v",
			before.Stats, typed.Stats, after.Stats)
	}
}

// TestRealLocalTerminalInputSurvivesShellOutputAndTabSwitches drives the
// production Windows ConPTY/PowerShell path through the shell UI. It mirrors
// the reported sequence: type before and after delayed command output, create
// a second Local Terminal, type there, switch back, then switch forward.
func TestRealLocalTerminalInputSurvivesShellOutputAndTabSwitches(t *testing.T) {
	st := newRealTypingState(t)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealShellSurfaces(t, st, 1)

	tester := ui.NewTester(shellui.View(st), 1200, 760)
	tester.SetLocale("en")
	tester.Frame()
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})

	typeTabInput := func(tab int, text string) {
		t.Helper()
		// TypeKey models the physical key + committed text ordering used by
		// Windows, rather than only injecting a text event.
		tester.TypeKey(0, ui.KeyE, text)
		waitRealSnapshot(t, st, tab, func(s appsession.Snapshot) bool {
			return s.Input == text
		})
	}
	typeTabInput(0, "echo NATCAT_ONE")
	tester.Key(0, ui.KeyEnter)
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == ""
	})
	pumpFrames(tester, 1200*time.Millisecond)
	typeTabInput(0, "echo NATCAT_TWO")
	tester.Key(0, ui.KeyEnter)
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == ""
	})

	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealShellSurfaces(t, st, 2)
	tester.Frame()
	waitRealSnapshot(t, st, 1, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive
	})
	typeTabInput(1, "echo NATCAT_NEW")
	tester.Key(0, ui.KeyEnter)
	waitRealSnapshot(t, st, 1, func(s appsession.Snapshot) bool {
		return s.Input == ""
	})
	pumpFrames(tester, 1800*time.Millisecond)
	typeTabInput(1, "echo NATCAT_AGAIN")
	tester.Key(0, ui.KeyEnter)
	waitRealSnapshot(t, st, 1, func(s appsession.Snapshot) bool {
		return s.Input == ""
	})

	st.SelectTab(0)
	tester.Frame()
	typeTabInput(0, "echo NATCAT_BACK")
	tester.Key(0, ui.KeyEnter)
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == ""
	})
	st.SelectTab(1)
	tester.Frame()
	typeTabInput(1, "echo NATCAT_FINAL")
}

func TestRealLocalTerminalInputSurvivesAutocompleteDebounce(t *testing.T) {
	st := newRealAutocompleteState(t)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealShellSurfaces(t, st, 1)
	tester := ui.NewTester(shellui.View(st), 1200, 760)
	tester.SetLocale("en")
	tester.Frame()
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})

	tester.TypeKey(0, ui.KeyE, "echo NATCAT_AUTOCOMPLETE")
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "echo NATCAT_AUTOCOMPLETE"
	})
	pumpFrames(tester, 1200*time.Millisecond)
	if !tester.HasText("echo NATCAT_AUTOCOMPLETE-suggested") {
		t.Fatalf("autocomplete did not appear: %v", tester.Texts())
	}
	tester.TypeKey(0, ui.KeyX, "X")
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "echo NATCAT_AUTOCOMPLETEX"
	})
}

func TestRealLocalTerminalInputAfterClickingNewTabs(t *testing.T) {
	st := newRealTypingState(t)
	tester := ui.NewTester(shellui.View(st), 1200, 760)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.MenuNewTab()); err != nil {
		t.Fatalf("click New Tab: %v", err)
	}
	tester.Frame()
	waitForRealShellSurfaces(t, st, 1)
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})
	first := realSurfaceSession(t, st, 0)
	tester.TypeKey(0, ui.KeyF, "first")
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "first"
	})
	tester.Key(0, ui.KeyEnter)
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool { return s.Input == "" })

	if err := tester.Click(m.MenuNewTab()); err != nil {
		t.Fatalf("click second New Tab: %v", err)
	}
	tester.Frame()
	waitForRealShellSurfaces(t, st, 2)
	waitRealSnapshot(t, st, 1, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})
	second := realSurfaceSession(t, st, 1)
	tester.TypeKey(0, ui.KeyS, "second")
	waitRealSnapshot(t, st, 1, func(s appsession.Snapshot) bool {
		return s.Input == "second"
	})
	pumpFrames(tester, 1500*time.Millisecond)
	tester.TypeKey(0, ui.KeyA, "again")
	waitRealSnapshot(t, st, 1, func(s appsession.Snapshot) bool {
		return s.Input == "secondagain"
	})

	firstTab, ok := st.TabInfo(0)
	if !ok {
		t.Fatal("first Tab disappeared")
	}
	firstTabLabel := firstTab.Title
	if firstTabLabel == "" {
		firstTabLabel = m.LocalSessionTab(1)
	}
	if err := tester.Click(firstTabLabel); err != nil {
		t.Fatalf("click first Local Session Tab: %v", err)
	}
	tester.Frame()
	tester.TypeKey(0, ui.KeyB, "back")
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "back"
	})
	if first == second {
		t.Fatal("the two clicked Local Tabs share one Session")
	}
	if second.Snapshot().Input != "secondagain" {
		t.Fatalf("second Session input changed after switching back: %q", second.Snapshot().Input)
	}
}

func TestRealLocalTerminalInputAfterWindowFocusReturns(t *testing.T) {
	st := newRealTypingState(t)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealShellSurfaces(t, st, 1)
	tester := ui.NewTester(shellui.View(st), 1200, 760)
	tester.SetLocale("en")
	tester.Frame()
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})
	tester.TypeKey(0, ui.KeyE, "before")
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "before"
	})
	tester.SetFocused(false)
	pumpFrames(tester, 1000*time.Millisecond)
	tester.SetFocused(true)
	tester.Frame()
	tester.TypeKey(0, ui.KeyA, "after")
	waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "beforeafter"
	})
}

// TestRealLocalTerminalInputAfterIdle reproduces the reported sequence:
// create a Local Terminal from the New Tab control, leave it untouched while
// the shell settles for several seconds, then type. The idle loop also records
// the Session counters so a future failure distinguishes lost UI focus from
// an input queue/ConPTY write that did not arrive at the Session.
func TestRealLocalTerminalInputAfterIdle(t *testing.T) {
	st := newRealTypingState(t)
	tester := ui.NewTester(shellui.View(st), 1200, 760)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(locales.In("en").MenuNewTab()); err != nil {
		t.Fatalf("click New Tab: %v", err)
	}
	tester.Frame()
	waitForRealShellSurfaces(t, st, 1)
	before := waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Connection == appsession.ConnectionStatusActive && s.Stats.OutputBytes > 0
	})

	// Keep building frames for longer than the reported "a few seconds" while
	// sending no input at all. This exercises cursor animation, prompt output,
	// and any Lens flow updates that arrive after the tab was created.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		tester.Frame()
		time.Sleep(time.Millisecond)
	}
	afterIdle := realSurfaceSession(t, st, 0).Snapshot()
	if afterIdle.Status != appsession.StatusRunning || afterIdle.Connection != appsession.ConnectionStatusActive {
		t.Fatalf("Local Terminal stopped while idle: status=%s connection=%s notice=%v",
			afterIdle.Status, afterIdle.Connection, afterIdle.Notice)
	}
	if afterIdle.Stats.InputBytes != before.Stats.InputBytes || afterIdle.Input != before.Input {
		t.Fatalf("idle changed input unexpectedly: before=%+v after=%+v", before, afterIdle)
	}

	tester.TypeKey(0, ui.KeyI, "after-idle")
	afterType := waitRealSnapshot(t, st, 0, func(s appsession.Snapshot) bool {
		return s.Input == "after-idle"
	})
	if afterType.Stats.InputBytes <= afterIdle.Stats.InputBytes {
		t.Fatalf("typed input did not reach Session transport queue: idle=%+v typed=%+v", afterIdle.Stats, afterType.Stats)
	}
}
