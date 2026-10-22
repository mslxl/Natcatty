package app

import (
	"context"
	"runtime"
	"sort"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/command"
	appsession "github.com/mslxl/natcatty/application/session"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

func TestParseAccelerator(t *testing.T) {
	tests := []struct {
		spec string
		mods ui.Modifiers
		key  ui.Key
		ok   bool
	}{
		{"CmdOrCtrl+T", ui.Cmd, ui.KeyT, true},
		{"CmdOrCtrl+Shift+B", ui.Cmd | ui.Shift, ui.KeyB, true},
		{"CmdOrCtrl+Alt+Left", ui.Cmd | ui.Alt, ui.KeyLeft, true},
		{"CmdOrCtrl+Shift+E", ui.Cmd | ui.Shift, ui.KeyE, true},
		{"Shift+CmdOrCtrl+X", ui.Shift | ui.Cmd, ui.KeyX, true},
		{"Ctrl+F5", ui.Ctrl, ui.KeyF5, true},
		{"CmdOrCtrl+?", 0, 0, false},
	}
	for _, test := range tests {
		mods, key, ok := parseAccelerator(test.spec)
		if ok != test.ok {
			t.Errorf("parseAccelerator(%q) ok = %v, want %v", test.spec, ok, test.ok)
			continue
		}
		if !ok {
			continue
		}
		if mods != test.mods || key != test.key {
			t.Errorf("parseAccelerator(%q) = %v+%v, want %v+%v",
				test.spec, mods, key, test.mods, test.key)
		}
	}
}

// TestMenuAcceleratorsComeFromTheMenu proves the table is derived from the
// menu's own labels and dispatches the same command the item would.
func TestMenuAcceleratorsComeFromTheMenu(t *testing.T) {
	registry := command.NewRegistry()
	var dispatched []command.ID
	for _, id := range []command.ID{command.NewTab, command.OpenBackups, command.Quit} {
		id := id
		if err := registry.Register(id, func(context.Context) error {
			dispatched = append(dispatched, id)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	bindings := MenuAccelerators(BuildMenu(registry, "en"))
	byCombo := make(map[string]acceleratorBinding, len(bindings))
	for _, binding := range bindings {
		byCombo[acceleratorName(binding.mods, binding.key)] = binding
	}

	want := []string{
		acceleratorName(ui.Cmd, ui.KeyT),
		acceleratorName(ui.Cmd, ui.KeyW),
		acceleratorName(ui.Cmd|ui.Shift, ui.KeyB),
		acceleratorName(ui.Cmd|ui.Shift, ui.KeyE),
		acceleratorName(ui.Cmd|ui.Shift, ui.KeyD),
		acceleratorName(ui.Cmd|ui.Shift, ui.KeyW),
		acceleratorName(ui.Cmd|ui.Alt, ui.KeyLeft),
		acceleratorName(ui.Cmd|ui.Alt, ui.KeyRight),
		acceleratorName(ui.Cmd|ui.Alt, ui.KeyUp),
		acceleratorName(ui.Cmd|ui.Alt, ui.KeyDown),
	}
	if runtime.GOOS != "darwin" {
		want = append(want, acceleratorName(ui.Cmd, ui.KeyQ))
	}
	for _, combo := range want {
		if _, ok := byCombo[combo]; !ok {
			t.Errorf("menu accelerator %s is missing; got %v", combo, keysOfAccelerators(byCombo))
		}
	}

	// The standard Edit menu contributes no app bindings: Ctrl+C/Ctrl+V and
	// the terminal's Ctrl+Shift+C/V must never be claimed by this table.
	for _, forbidden := range []string{
		acceleratorName(ui.Cmd, ui.KeyC),
		acceleratorName(ui.Cmd, ui.KeyV),
		acceleratorName(ui.Cmd|ui.Shift, ui.KeyC),
		acceleratorName(ui.Cmd|ui.Shift, ui.KeyV),
	} {
		if _, ok := byCombo[forbidden]; ok {
			t.Errorf("menu accelerators claimed %s, which belongs to the terminal", forbidden)
		}
	}

	// The binding runs the menu item's own Click, which dispatches the app
	// command through the registry.
	backups := byCombo[acceleratorName(ui.Cmd|ui.Shift, ui.KeyB)]
	backups.item.Click(backups.item, nil)
	if len(dispatched) != 1 || dispatched[0] != command.OpenBackups {
		t.Fatalf("dispatched = %v, want [%s]", dispatched, command.OpenBackups)
	}
}

func acceleratorName(mods ui.Modifiers, key ui.Key) string {
	return string(rune('0'+mods)) + "/" + string(rune(key))
}

func keysOfAccelerators(byCombo map[string]acceleratorBinding) []string {
	keys := make([]string, 0, len(byCombo))
	for key := range byCombo {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// accelHosts is the minimal Vault seam the accelerator test needs.
type accelHosts struct{}

func (accelHosts) List(context.Context) ([]domainvault.Host, error) { return nil, nil }
func (accelHosts) Create(context.Context, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (accelHosts) Update(context.Context, model.HostID, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (accelHosts) Delete(context.Context, model.HostID) error { return nil }

// accelBackups lets the Backups command open the real panel.
type accelBackups struct{ items []domainbackup.Summary }

func (b *accelBackups) List(context.Context) ([]domainbackup.Summary, error) {
	return append([]domainbackup.Summary(nil), b.items...), nil
}
func (b *accelBackups) Restore(context.Context, string) error { return nil }
func (b *accelBackups) Clear(context.Context) (int, error) {
	count := len(b.items)
	b.items = nil
	return count, nil
}
func (b *accelBackups) Retention() int { return domainbackup.DefaultRetention }
func (b *accelBackups) SetRetention(context.Context, int) (int, int, error) {
	return domainbackup.DefaultRetention, 0, nil
}
func (b *accelBackups) EncryptionAvailable() bool    { return true }
func (b *accelBackups) LastStatus() appbackup.Status { return appbackup.Status{} }

// accelChord is one key the focused terminal surface received.
type accelChord struct {
	mods ui.Modifiers
	key  ui.Key
}

// accelSurface is a focused terminal stand-in that records every key its
// input handler receives.
type accelSurface struct{ keys []accelChord }

func (s *accelSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "accelerator terminal").Fill().Focus().HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown {
			s.keys = append(s.keys, accelChord{mods: ev.Mods, key: ev.Key})
			return true
		}
		return false
	})
}

func (s *accelSurface) Close() {}

func (s *accelSurface) received(mods ui.Modifiers, key ui.Key) bool {
	for _, chord := range s.keys {
		if chord.mods == mods && chord.key == key {
			return true
		}
	}
	return false
}

// TestMenuAcceleratorsFireFromKeyChordsAndLeaveTheTerminalAlone drives real
// key chords through the production window content. Ctrl+T opens a tab,
// Ctrl+Shift+B opens the Backups panel from the Vault, Ctrl+Q dispatches Quit,
// and Ctrl+C with and without Shift still reaches the focused terminal.
func TestMenuAcceleratorsFireFromKeyChordsAndLeaveTheTerminalAlone(t *testing.T) {
	service := appsession.NewService(&wiringFactory{
		transport: &wiringTransport{closed: make(chan struct{})},
	})
	t.Cleanup(service.CloseAll)

	surface := &accelSurface{}
	vaultState := vaultstate.NewState(accelHosts{}, vaultstate.WithBackups(&accelBackups{}))
	if err := vaultState.Reload(); err != nil {
		t.Fatalf("load the Vault: %v", err)
	}
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return surface, nil
		}),
		state.WithVault(vaultui.View(vaultState)),
	)
	t.Cleanup(shellState.CloseAllSessions)

	quits := 0
	registry := newRegistry(shellState, func() { quits++ }, func() error { return nil }, vaultState, func() {})
	host := &fakeWindowHost{}
	manager := newWindowManager(host, &recordingConfirmer{}, nil, shellState, nil,
		func() string { return "en" })
	manager.SetAccelerators(MenuAccelerators(BuildMenu(registry, "en")))
	manager.OpenMain()

	tester := ui.NewTester(host.windows[0].spec.Content, 1100, 720)
	tester.SetLocale("en")
	// The first frame registers the window shortcuts; MyGo routes the next
	// key against that table.
	tester.Frame()
	tester.Frame()

	tester.Key(ui.Cmd, ui.KeyT)
	tester.Frame()
	if shellState.TabCount() != 1 {
		t.Fatalf("Ctrl+T left %d tabs, want 1", shellState.TabCount())
	}
	tester.Frame()

	// Ctrl+C, Ctrl+V, and their Shift variants are not menu accelerators: the
	// terminal must keep receiving all four, as it did before native dispatch.
	for _, chord := range []struct {
		mods ui.Modifiers
		key  ui.Key
	}{
		{ui.Ctrl, ui.KeyC},
		{ui.Ctrl, ui.KeyV},
		{ui.Ctrl | ui.Shift, ui.KeyC},
		{ui.Ctrl | ui.Shift, ui.KeyV},
	} {
		tester.Key(chord.mods, chord.key)
		tester.Frame()
		if !surface.received(chord.mods, chord.key) {
			t.Errorf("%v+%v did not reach the terminal, got %v", chord.mods, chord.key, surface.keys)
		}
	}

	before := len(surface.keys)
	// Cmd is the platform's shortcut modifier: Super on macOS, Control
	// elsewhere, matching the CmdOrCtrl labels the menu shows.
	tester.Key(ui.Cmd|ui.Shift, ui.KeyB)
	tester.Frame()
	if !vaultState.BackupsOpen() {
		t.Fatal("the Local backups accelerator did not open the panel")
	}
	if !tester.HasText(locales.In("en").VaultBackupTitle()) {
		t.Fatalf("the Backups panel is not visible, texts %q", tester.Texts())
	}
	if len(surface.keys) != before {
		t.Fatalf("the Local backups accelerator also reached the terminal: %v", surface.keys[before:])
	}

	if runtime.GOOS != "darwin" {
		tester.Key(ui.Cmd, ui.KeyQ)
		tester.Frame()
		if quits != 1 {
			t.Fatalf("Ctrl+Q dispatched %d quits, want 1", quits)
		}
	}
}
