package desktop

import (
	"context"
	"errors"
	"reflect"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestDefaultsPreserveOriginalValues(t *testing.T) {
	windows := Defaults("windows")
	if !windows.ShowTrayIcon || !windows.CloseToTray || !windows.GlobalHotkeyEnabled {
		t.Fatalf("tray defaults = %+v", windows)
	}
	if windows.ToggleWindowHotkey != "Ctrl + `" {
		t.Fatalf("windows hotkey = %q", windows.ToggleWindowHotkey)
	}
	if !windows.SSHDeepLinks {
		t.Fatal("ssh deep links default off")
	}
	if !windows.NotifySessionEnded || !windows.NotifyConnectionFailed || !windows.NotifyForwardingFailed {
		t.Fatalf("notification defaults = %+v", windows)
	}
	if windows.AutoStart {
		t.Fatal("auto start default on")
	}
	if got := Defaults("darwin").ToggleWindowHotkey; got != "⌃ + `" {
		t.Fatalf("darwin hotkey = %q", got)
	}
}

func TestNormalizeTreatsVersionZeroAsDefaults(t *testing.T) {
	settings := Normalize(Settings{}, "windows")
	if !settings.ShowTrayIcon || settings.Version != SettingsVersion {
		t.Fatalf("normalized = %+v", settings)
	}

	settings = Normalize(Settings{Version: 1, ToggleWindowHotkey: "  "}, "windows")
	if settings.ToggleWindowHotkey != "Ctrl + `" {
		t.Fatalf("empty hotkey = %q", settings.ToggleWindowHotkey)
	}

	settings = Normalize(Settings{Version: 1, ShowTrayIcon: false, ToggleWindowHotkey: " Ctrl+Shift+Space "}, "windows")
	if settings.ShowTrayIcon {
		t.Fatal("an explicit false was overwritten")
	}
	if settings.ToggleWindowHotkey != "Ctrl+Shift+Space" {
		t.Fatalf("hotkey = %q", settings.ToggleWindowHotkey)
	}
}

func TestAcceleratorTranslatesMacSymbols(t *testing.T) {
	if got := Accelerator("⌃ + `"); got != "Ctrl + `" {
		t.Fatalf("accelerator = %q", got)
	}
	if got := Accelerator("⌘ + ⌥ + Space"); got != "Super + Alt + Space" {
		t.Fatalf("accelerator = %q", got)
	}
}

func TestParseDeepLink(t *testing.T) {
	target, err := ParseDeepLink("ssh://alice:p%40ss@Example.COM:2222")
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Kind: TargetSSH, Hostname: "example.com", Username: "alice", Password: "p@ss", Port: 2222, Raw: "ssh://alice:p%40ss@Example.COM:2222"}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("target = %+v, want %+v", target, want)
	}

	target, err = ParseDeepLink("ssh://[2001:DB8::1]")
	if err != nil {
		t.Fatal(err)
	}
	if target.Hostname != "2001:db8::1" || target.TargetPort() != 22 {
		t.Fatalf("ipv6 target = %+v", target)
	}

	if _, err := ParseDeepLink("https://example.com"); !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("https error = %v", err)
	}
	if _, err := ParseDeepLink("ssh://"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("empty host error = %v", err)
	}
	if _, err := ParseDeepLink("ssh://host:0"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("port error = %v", err)
	}
	if _, err := ParseDeepLink("ssh://host:70000"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("port error = %v", err)
	}
	telnet, err := ParseDeepLink("telnet://host:23")
	if err != nil {
		t.Fatal(err)
	}
	if telnet.Kind != TargetTelnet || telnet.Port != 23 {
		t.Fatalf("telnet target = %+v", telnet)
	}
}

func TestParseCommandLine(t *testing.T) {
	targets, problems := ParseCommandLine([]string{"-l", "root", "-P", "2200", "Example.com"})
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	want := Target{Kind: TargetSSH, Hostname: "example.com", Username: "root", Port: 2200, Raw: "Example.com"}
	if !reflect.DeepEqual(targets[0], want) {
		t.Fatalf("target = %+v, want %+v", targets[0], want)
	}

	targets, problems = ParseCommandLine([]string{"ssh://alice@Host:22"})
	if len(problems) != 0 || len(targets) != 1 {
		t.Fatalf("targets = %+v problems = %v", targets, problems)
	}
	if targets[0].Username != "alice" || targets[0].Hostname != "host" {
		t.Fatalf("target = %+v", targets[0])
	}

	targets, _ = ParseCommandLine([]string{"user@host:2022"})
	if len(targets) != 1 || targets[0].Username != "user" || targets[0].Port != 2022 {
		t.Fatalf("target = %+v", targets)
	}

	targets, _ = ParseCommandLine([]string{"--hidden", "--", "host"})
	if len(targets) != 1 || targets[0].Hostname != "host" {
		t.Fatalf("targets = %+v", targets)
	}

	targets, _ = ParseCommandLine([]string{"--port=abc", "host"})
	if len(targets) != 1 {
		t.Fatalf("a bad port must not drop the host: %+v", targets)
	}

	if _, problems = ParseCommandLine([]string{"-p"}); len(problems) != 0 {
		t.Fatalf("a dangling flag produced %v", problems)
	}
	if _, problems = ParseCommandLine([]string{"ssh://bad:port"}); len(problems) == 0 {
		t.Fatal("an invalid URL produced no problem")
	}

	// PuTTY-style switches: the password operand must not become a Host.
	targets, problems = ParseCommandLine([]string{
		"-ssh", "-l", "root", "-P", "2202", "-pw", "s3cret", "box.example"})
	if len(problems) != 0 || len(targets) != 1 {
		t.Fatalf("putty targets = %+v problems = %v", targets, problems)
	}
	if targets[0].Hostname != "box.example" || targets[0].Username != "root" ||
		targets[0].Port != 2202 || targets[0].Password != "s3cret" {
		t.Fatalf("putty target = %+v", targets[0])
	}

	// SecureCRT-style switches used by bastion launchers, with a key path
	// operand that must also be consumed.
	targets, problems = ParseCommandLine([]string{
		"/SSH2", "/L", "admin", "/P", "2222", "/PASSWORD", "hunter2",
		"-i", `C:\keys\id_rsa`, "jump.example"})
	if len(problems) != 0 || len(targets) != 1 {
		t.Fatalf("securecrt targets = %+v problems = %v", targets, problems)
	}
	if targets[0].Hostname != "jump.example" || targets[0].Username != "admin" ||
		targets[0].Port != 2222 || targets[0].Password != "hunter2" {
		t.Fatalf("securecrt target = %+v", targets[0])
	}

	targets, problems = ParseCommandLine([]string{"-telnet", "telnet.example"})
	if len(problems) != 0 || len(targets) != 1 || targets[0].Kind != TargetTelnet {
		t.Fatalf("telnet targets = %+v problems = %v", targets, problems)
	}

	targets, _ = ParseCommandLine([]string{`C:\Users\me\notes.txt`, "real.example"})
	if len(targets) != 1 || targets[0].Hostname != "real.example" {
		t.Fatalf("a file path became a Host: %+v", targets)
	}

	targets, _ = ParseCommandLine([]string{"2200", "host"})
	if len(targets) != 1 || targets[0].Port != 2200 {
		t.Fatalf("a bare positional port was not consumed: %+v", targets)
	}

	if _, problems = ParseCommandLine([]string{"-serial", "COM3"}); len(problems) == 0 {
		t.Fatal("an unsupported transport produced no problem")
	}
}

func TestMatchHostRequiresExactlyOneEffectiveMatch(t *testing.T) {
	hosts := []domainvault.Host{
		{ID: "1", Hostname: "example.com", Port: 22, Username: "alice", Protocol: domainvault.ProtocolSSH},
		{ID: "2", Hostname: "example.com", Port: 2222, Username: "alice", Protocol: domainvault.ProtocolSSH},
		{ID: "3", Hostname: "other.example", Port: 22, Username: "bob", Protocol: domainvault.ProtocolSSH},
		{ID: "4", Hostname: "example.com", Port: 22, Username: "bob", Protocol: domainvault.ProtocolTelnet},
	}
	target := Target{Kind: TargetSSH, Hostname: "example.com", Username: "alice", Port: 0}
	host, ok := MatchHost(hosts, target)
	if !ok || host.ID != "1" {
		t.Fatalf("match = %+v, %v", host, ok)
	}

	target.Username = "bob"
	if _, ok := MatchHost(hosts, target); ok {
		t.Fatal("matched a Telnet Host")
	}

	hosts = append(hosts, domainvault.Host{ID: "5", Hostname: "EXAMPLE.com", Port: 22, Username: "alice", Protocol: domainvault.ProtocolSSH})
	if _, ok := MatchHost(hosts, Target{Kind: TargetSSH, Hostname: "example.com", Username: "alice"}); ok {
		t.Fatal("two candidates matched")
	}

	draft := DraftHost(Target{Kind: TargetSSH, Hostname: "new.example", Username: "root"})
	if draft.Label != "root@new.example" || draft.Port != 22 || draft.Protocol != domainvault.ProtocolSSH {
		t.Fatalf("draft = %+v", draft)
	}
	if draft.ID != "" {
		t.Fatalf("a draft must not invent an ID: %q", draft.ID)
	}
}

type fakeRepository struct {
	settings Settings
	loadErr  error
	saveErr  error
	saves    []Settings
}

func (r *fakeRepository) Load(context.Context) (Settings, error) {
	return r.settings, r.loadErr
}

func (r *fakeRepository) Save(_ context.Context, settings Settings) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves = append(r.saves, settings)
	return nil
}

type fakeHotkeys struct {
	registered map[string]func()
	fail       map[string]error
	order      []string
}

func (h *fakeHotkeys) Register(accelerator string, fn func()) error {
	if err := h.fail[accelerator]; err != nil {
		return err
	}
	if h.registered == nil {
		h.registered = map[string]func(){}
	}
	h.registered[accelerator] = fn
	h.order = append(h.order, accelerator)
	return nil
}

func (h *fakeHotkeys) Unregister(accelerator string) {
	delete(h.registered, accelerator)
}

type fakeAutoLauncher struct {
	enabled   bool
	supported bool
	failSet   bool
}

func (a *fakeAutoLauncher) Supported() bool { return a.supported }
func (a *fakeAutoLauncher) Enabled() (bool, error) {
	return a.enabled, nil
}
func (a *fakeAutoLauncher) SetEnabled(enabled bool) error {
	if a.failSet {
		return errors.New("denied")
	}
	a.enabled = enabled
	return nil
}

type fakeNotifier struct {
	supported bool
	notified  []Notification
}

func (n *fakeNotifier) Supported() bool { return n.supported }
func (n *fakeNotifier) Notify(notification Notification) error {
	n.notified = append(n.notified, notification)
	return nil
}

func serviceForTest(t *testing.T) (*Service, *fakeRepository, *fakeHotkeys, *fakeAutoLauncher, *fakeNotifier) {
	t.Helper()
	repository := &fakeRepository{settings: Defaults("windows")}
	hotkeys := &fakeHotkeys{fail: map[string]error{}}
	auto := &fakeAutoLauncher{supported: true}
	notifier := &fakeNotifier{supported: true}
	service := NewService(repository, hotkeys, auto, notifier, WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return service, repository, hotkeys, auto, notifier
}

func TestServiceLoadUsesOSAutoStartState(t *testing.T) {
	repository := &fakeRepository{settings: Defaults("windows")}
	repository.settings.AutoStart = true
	auto := &fakeAutoLauncher{supported: true, enabled: false}
	service := NewService(repository, &fakeHotkeys{fail: map[string]error{}}, auto, &fakeNotifier{supported: true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.RefreshAutoStart()
	if service.Settings().AutoStart {
		t.Fatal("the persisted value overrode the OS source of truth")
	}
}

func TestServiceAutoStartRollsBackOnFailure(t *testing.T) {
	repository := &fakeRepository{settings: Defaults("windows")}
	repository.settings.AutoStart = true
	auto := &fakeAutoLauncher{supported: true, enabled: true, failSet: true}
	service := NewService(repository, &fakeHotkeys{fail: map[string]error{}}, auto, &fakeNotifier{supported: true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := service.SetAutoStart(false)
	if !errors.Is(err, ErrAutoStart) {
		t.Fatalf("error = %v", err)
	}
	if !service.Settings().AutoStart {
		t.Fatal("the cached state did not roll back to the actual login-item state")
	}
	if len(repository.saves) == 0 {
		t.Fatal("the rollback state was not persisted")
	}

	auto.supported = false
	service = NewService(repository, nil, auto, nil)
	if !errors.Is(service.SetAutoStart(true), ErrUnsupported) {
		t.Fatal("an unsupported autostart did not report ErrUnsupported")
	}
}

func TestServiceHotkeyRegistrationAndRollback(t *testing.T) {
	service, _, hotkeys, _, _ := serviceForTest(t)
	fired := 0
	if err := service.Apply(func() { fired++ }); err != nil {
		t.Fatal(err)
	}
	if service.RegisteredHotkey() != "Ctrl + `" {
		t.Fatalf("registered = %q", service.RegisteredHotkey())
	}
	hotkeys.registered["Ctrl + `"]()
	if fired != 1 {
		t.Fatalf("toggle fired %d times", fired)
	}

	hotkeys.fail["Ctrl+Shift+Space"] = errors.New("already taken")
	if err := service.SetToggleWindowHotkey("Ctrl+Shift+Space"); !errors.Is(err, ErrHotkey) {
		t.Fatalf("error = %v", err)
	}
	if service.Settings().ToggleWindowHotkey != "Ctrl + `" {
		t.Fatalf("settings = %q", service.Settings().ToggleWindowHotkey)
	}
	if _, ok := hotkeys.registered["Ctrl + `"]; !ok {
		t.Fatal("the previous shortcut was not restored")
	}

	if err := service.SetGlobalHotkeyEnabled(false); err != nil {
		t.Fatal(err)
	}
	if service.RegisteredHotkey() != "" || len(hotkeys.registered) != 0 {
		t.Fatalf("registered = %q %+v", service.RegisteredHotkey(), hotkeys.registered)
	}
}

func TestServiceNotificationsRespectTogglesAndSupport(t *testing.T) {
	service, repository, _, _, notifier := serviceForTest(t)
	if err := service.Notify(Notification{Event: EventSessionEnded, Target: "box"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notified) != 1 || notifier.notified[0].Target != "box" {
		t.Fatalf("notified = %+v", notifier.notified)
	}
	if err := service.SetNotification(EventSessionEnded, false); err != nil {
		t.Fatal(err)
	}
	if err := service.Notify(Notification{Event: EventSessionEnded}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notified) != 1 {
		t.Fatalf("a disabled event was notified: %+v", notifier.notified)
	}
	if len(repository.saves) == 0 || repository.saves[len(repository.saves)-1].NotifySessionEnded {
		t.Fatal("the notification toggle was not persisted")
	}

	notifier.supported = false
	service = NewService(repository, nil, nil, notifier)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Notify(Notification{Event: EventConnectionFailed}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notified) != 1 {
		t.Fatal("an unsupported notifier received a notification")
	}
}
