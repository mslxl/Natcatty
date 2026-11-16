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
	if !windows.ShowTrayIcon || !windows.CloseToTray {
		t.Fatalf("tray defaults = %+v", windows)
	}
	if windows.HotkeyMode != HotkeyModeQuake {
		t.Fatalf("hotkey mode = %q, want the Quake default", windows.HotkeyMode)
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
	if settings.HotkeyMode != HotkeyModeQuake {
		t.Fatalf("an old record loaded hotkey mode %q, want quake", settings.HotkeyMode)
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

func TestNormalizeRevertsUnknownValuesToDefaults(t *testing.T) {
	settings := Normalize(Settings{
		Version:            1,
		HotkeyMode:         HotkeyMode("bogus"),
		ToggleWindowHotkey: "Ctrl+Shift+Space",
	}, "windows")
	if settings.HotkeyMode != DefaultHotkeyMode {
		t.Fatalf("unknown mode = %q, want %q", settings.HotkeyMode, DefaultHotkeyMode)
	}
	if settings.Version != SettingsVersion {
		t.Fatalf("version = %d, want %d: this change must not bump it", settings.Version, SettingsVersion)
	}

	for _, mode := range []HotkeyMode{HotkeyModeNone, HotkeyModeToggle, HotkeyModeQuake, HotkeyModeTopdown} {
		if !mode.Valid() {
			t.Errorf("%q is not valid", mode)
		}
	}
	if HotkeyModeNone.RegistersHotkey() {
		t.Error("none claims the accelerator")
	}
	for _, mode := range []HotkeyMode{HotkeyModeToggle, HotkeyModeQuake, HotkeyModeTopdown} {
		if !mode.RegistersHotkey() {
			t.Errorf("%q does not claim the accelerator", mode)
		}
	}
}

// TestQuakeSourceDefaultsToOwnAndUnknownValuesNormalize pins the content
// source contract: a new or pre-field record follows the Quake Workspace, and
// an unrecognized stored value returns to that default without touching any
// other desktop setting.
func TestQuakeSourceDefaultsToOwnAndUnknownValuesNormalize(t *testing.T) {
	if got := Defaults("windows").QuakeSource; got != QuakeSourceOwn {
		t.Fatalf("default source = %q, want own", got)
	}
	if got := Normalize(Settings{Version: 1}, "windows").QuakeSource; got != QuakeSourceOwn {
		t.Fatalf("missing source = %q, want own", got)
	}
	settings := Normalize(Settings{
		Version:      1,
		QuakeSource:  QuakeSource("bogus"),
		ShowTrayIcon: true,
	}, "windows")
	if settings.QuakeSource != DefaultQuakeSource {
		t.Fatalf("unknown source = %q, want %q", settings.QuakeSource, DefaultQuakeSource)
	}
	if !settings.ShowTrayIcon {
		t.Fatal("normalizing the source changed an unrelated desktop setting")
	}
	for _, source := range []QuakeSource{QuakeSourceOwn, QuakeSourceFollow} {
		if !source.Valid() {
			t.Errorf("%q is not valid", source)
		}
	}
}

// TestDropdownGeometryDefaults pins the new dropdown behavior defaults:
// Quake at half the work area, top-down at 80%, both in percent, and all
// three hide triggers on.
func TestDropdownGeometryDefaults(t *testing.T) {
	settings := Defaults("windows")
	if settings.QuakeHeight != 50 || settings.QuakeHeightUnit != HeightUnitPercent {
		t.Fatalf("quake geometry = %d %q, want 50 percent", settings.QuakeHeight, settings.QuakeHeightUnit)
	}
	if settings.TopdownHeight != 80 || settings.TopdownHeightUnit != HeightUnitPercent {
		t.Fatalf("topdown geometry = %d %q, want 80 percent", settings.TopdownHeight, settings.TopdownHeightUnit)
	}
	if !settings.HideOnBlur || !settings.HideOnEscape || !settings.HideOnClickOutside {
		t.Fatalf("hide defaults = blur %v escape %v outside %v, want all true",
			settings.HideOnBlur, settings.HideOnEscape, settings.HideOnClickOutside)
	}
}

// TestNormalizeDropdownGeometry pins the defensive rules for persisted
// records: missing fields take the defaults, unknown units return to percent,
// out-of-range values clamp, other values survive, and the version stays 1.
func TestNormalizeDropdownGeometry(t *testing.T) {
	missing := Normalize(Settings{Version: 1, ShowTrayIcon: false}, "windows")
	if missing.QuakeHeight != DefaultQuakeHeight || missing.QuakeHeightUnit != DefaultHeightUnit {
		t.Fatalf("missing quake geometry = %d %q", missing.QuakeHeight, missing.QuakeHeightUnit)
	}
	if missing.TopdownHeight != DefaultTopdownHeight || missing.TopdownHeightUnit != DefaultHeightUnit {
		t.Fatalf("missing topdown geometry = %d %q", missing.TopdownHeight, missing.TopdownHeightUnit)
	}
	if missing.Version != SettingsVersion {
		t.Fatalf("version = %d, want %d", missing.Version, SettingsVersion)
	}
	if missing.ShowTrayIcon {
		t.Fatal("normalizing the geometry changed an unrelated setting")
	}

	unknown := Normalize(Settings{
		Version:           1,
		QuakeHeight:       40,
		QuakeHeightUnit:   HeightUnit("bogus"),
		TopdownHeight:     70,
		TopdownHeightUnit: HeightUnit("bogus"),
	}, "windows")
	if unknown.QuakeHeightUnit != DefaultHeightUnit || unknown.TopdownHeightUnit != DefaultHeightUnit {
		t.Fatalf("unknown units = %q %q", unknown.QuakeHeightUnit, unknown.TopdownHeightUnit)
	}
	if unknown.QuakeHeight != 40 || unknown.TopdownHeight != 70 {
		t.Fatalf("valid heights changed: %+v", unknown)
	}

	clampedPercent := Normalize(Settings{Version: 1, QuakeHeight: 5, TopdownHeight: 200,
		QuakeHeightUnit: HeightUnitPercent, TopdownHeightUnit: HeightUnitPercent}, "windows")
	if clampedPercent.QuakeHeight != MinPercentHeight || clampedPercent.TopdownHeight != MaxPercentHeight {
		t.Fatalf("percent clamps = %d/%d", clampedPercent.QuakeHeight, clampedPercent.TopdownHeight)
	}
	clampedPixels := Normalize(Settings{Version: 1, QuakeHeight: 50, TopdownHeight: 20000,
		QuakeHeightUnit: HeightUnitPX, TopdownHeightUnit: HeightUnitPX}, "windows")
	if clampedPixels.QuakeHeight != MinPixelHeight || clampedPixels.TopdownHeight != MaxPixelHeight {
		t.Fatalf("pixel clamps = %d/%d", clampedPixels.QuakeHeight, clampedPixels.TopdownHeight)
	}
	// A missing height with an explicit pixel unit still has to end up inside
	// the pixel range; the percent default alone would be an invalid 50 px.
	missingPixel := Normalize(Settings{Version: 1, QuakeHeightUnit: HeightUnitPX}, "windows")
	if missingPixel.QuakeHeight != MinPixelHeight {
		t.Fatalf("missing pixel height = %d, want the pixel minimum", missingPixel.QuakeHeight)
	}

	// Normalize must not overwrite an explicit false; only the storage layer
	// can tell a missing key from the stored value.
	explicitOff := Normalize(Settings{Version: 1}, "windows")
	explicitOff.HideOnBlur, explicitOff.HideOnEscape, explicitOff.HideOnClickOutside = false, false, false
	normalized := Normalize(explicitOff, "windows")
	if normalized.HideOnBlur || normalized.HideOnEscape || normalized.HideOnClickOutside {
		t.Fatalf("Normalize turned an explicit false back on: %+v", normalized)
	}
}

// TestHeightValidation pins the two accepted ranges and rejects unknown
// units, matching the setters.
func TestHeightValidation(t *testing.T) {
	for _, test := range []struct {
		value int
		unit  HeightUnit
		ok    bool
	}{
		{10, HeightUnitPercent, true},
		{100, HeightUnitPercent, true},
		{9, HeightUnitPercent, false},
		{101, HeightUnitPercent, false},
		{120, HeightUnitPX, true},
		{10000, HeightUnitPX, true},
		{119, HeightUnitPX, false},
		{10001, HeightUnitPX, false},
		{50, HeightUnit("bogus"), false},
	} {
		err := ValidateHeight(test.value, test.unit)
		if (err == nil) != test.ok {
			t.Errorf("ValidateHeight(%d, %q) = %v, want ok=%v", test.value, test.unit, err, test.ok)
		}
		if err != nil && KindOf(err) != KindValidation {
			t.Errorf("ValidateHeight(%d, %q) kind = %q, want validation", test.value, test.unit, KindOf(err))
		}
	}
}

// TestServiceHeightSettersPersistAndRejectOutOfRange covers the setters: a
// valid pair persists, while an out-of-range value or unknown unit is a
// validation error that leaves the previous pair and saves untouched.
func TestServiceHeightSettersPersistAndRejectOutOfRange(t *testing.T) {
	service, repository, _, _, _ := serviceForTest(t)

	if err := service.SetQuakeHeight(30, HeightUnitPercent); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTopdownHeight(600, HeightUnitPX); err != nil {
		t.Fatal(err)
	}
	settings := service.Settings()
	if settings.QuakeHeight != 30 || settings.QuakeHeightUnit != HeightUnitPercent {
		t.Fatalf("quake = %d %q", settings.QuakeHeight, settings.QuakeHeightUnit)
	}
	if settings.TopdownHeight != 600 || settings.TopdownHeightUnit != HeightUnitPX {
		t.Fatalf("topdown = %d %q", settings.TopdownHeight, settings.TopdownHeightUnit)
	}
	if last := repository.saves[len(repository.saves)-1]; last.TopdownHeight != 600 {
		t.Fatalf("persisted topdown height = %d", last.TopdownHeight)
	}

	savesBefore := len(repository.saves)
	for _, test := range []struct {
		name  string
		value int
		unit  HeightUnit
	}{
		{"percent below", 9, HeightUnitPercent},
		{"percent above", 101, HeightUnitPercent},
		{"pixel below", 119, HeightUnitPX},
		{"pixel above", 10001, HeightUnitPX},
		{"unknown unit", 50, HeightUnit("bogus")},
	} {
		if err := service.SetQuakeHeight(test.value, test.unit); KindOf(err) != KindValidation {
			t.Errorf("%s: error = %v, want validation", test.name, err)
		}
	}
	if service.Settings().QuakeHeight != 30 || service.Settings().QuakeHeightUnit != HeightUnitPercent {
		t.Fatalf("a rejected value changed quake geometry: %+v", service.Settings())
	}
	if len(repository.saves) != savesBefore {
		t.Fatalf("rejected values were persisted: %d saves", len(repository.saves)-savesBefore)
	}
}

// TestServiceHideSwitchesPersist covers the three independent switches,
// including an explicit false surviving the save.
func TestServiceHideSwitchesPersist(t *testing.T) {
	service, repository, _, _, _ := serviceForTest(t)

	if err := service.SetHideOnBlur(false); err != nil {
		t.Fatal(err)
	}
	if err := service.SetHideOnEscape(false); err != nil {
		t.Fatal(err)
	}
	if err := service.SetHideOnClickOutside(false); err != nil {
		t.Fatal(err)
	}
	settings := service.Settings()
	if settings.HideOnBlur || settings.HideOnEscape || settings.HideOnClickOutside {
		t.Fatalf("hide switches = %+v, want all false", settings)
	}
	if last := repository.saves[len(repository.saves)-1]; last.HideOnBlur ||
		last.HideOnEscape || last.HideOnClickOutside {
		t.Fatalf("persisted hide switches = %+v", last)
	}

	// Each switch is independent: turning one back on leaves the others off.
	if err := service.SetHideOnEscape(true); err != nil {
		t.Fatal(err)
	}
	settings = service.Settings()
	if settings.HideOnBlur || !settings.HideOnEscape || settings.HideOnClickOutside {
		t.Fatalf("independent switch state = %+v", settings)
	}
}

// TestServiceHideSwitchRollsBackOnSaveFailure proves a failed save restores
// the previous value instead of leaving memory ahead of disk.
func TestServiceHideSwitchRollsBackOnSaveFailure(t *testing.T) {
	service, repository, _, _, _ := serviceForTest(t)
	repository.saveErr = errors.New("disk full")
	if err := service.SetHideOnBlur(false); err == nil {
		t.Fatal("a failed save was reported as success")
	}
	if !service.Settings().HideOnBlur {
		t.Fatal("the hide switch did not roll back after a failed save")
	}
}

func TestDropdownSupportIsExplicitPerPlatform(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		if !DropdownSupported(goos) {
			t.Errorf("DropdownSupported(%q) = false", goos)
		}
	}
	for _, goos := range []string{"", "js", "android", "freebsd"} {
		if DropdownSupported(goos) {
			t.Errorf("DropdownSupported(%q) = true", goos)
		}
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

	// --open-terminal-shell opens a local Tab. Its operand must become the
	// shell, never a Host, in either the separate or =value form.
	targets, problems = ParseCommandLine([]string{"--open-terminal-shell", "pwsh.exe"})
	if len(problems) != 0 || len(targets) != 1 {
		t.Fatalf("open-terminal targets = %+v problems = %v", targets, problems)
	}
	if targets[0].Kind != TargetLocalShell || targets[0].Shell != "pwsh.exe" {
		t.Fatalf("open-terminal target = %+v", targets[0])
	}
	targets, problems = ParseCommandLine([]string{"--open-terminal-shell=/bin/zsh", "real.example"})
	if len(problems) != 0 || len(targets) != 2 {
		t.Fatalf("mixed targets = %+v problems = %v", targets, problems)
	}
	if targets[0].Kind != TargetLocalShell || targets[0].Shell != "/bin/zsh" ||
		targets[1].Kind != TargetSSH || targets[1].Hostname != "real.example" {
		t.Fatalf("mixed targets = %+v", targets)
	}
	// A quoted executable path keeps its spaces and loses the quotes.
	targets, problems = ParseCommandLine([]string{`--open-terminal-shell="C:\Program Files\PowerShell\7\pwsh.exe"`})
	if len(problems) != 0 || len(targets) != 1 || targets[0].Shell != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("quoted shell targets = %+v problems = %v", targets, problems)
	}
	// A missing or empty shell must be a problem, not a default-shell Tab.
	if _, problems = ParseCommandLine([]string{"--open-terminal-shell"}); len(problems) == 0 {
		t.Fatal("a dangling --open-terminal-shell produced no problem")
	}
	if _, problems = ParseCommandLine([]string{"--open-terminal-shell="}); len(problems) == 0 {
		t.Fatal("an empty --open-terminal-shell produced no problem")
	}
	if targets, problems = ParseCommandLine([]string{"--open-terminal-shell", "--open-terminal-shell=pwsh"}); len(problems) == 0 || len(targets) != 1 {
		t.Fatalf("a shell flag swallowed the next flag: targets = %+v problems = %v", targets, problems)
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
	// failOnce fails the next registration of a key and forgets it, so a
	// rollback retry can succeed.
	failOnce map[string]error
	order    []string
}

func (h *fakeHotkeys) Register(accelerator string, fn func()) error {
	if err := h.fail[accelerator]; err != nil {
		return err
	}
	if err := h.failOnce[accelerator]; err != nil {
		delete(h.failOnce, accelerator)
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
	hotkeys := &fakeHotkeys{fail: map[string]error{}, failOnce: map[string]error{}}
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

	if err := service.SetHotkeyMode(HotkeyModeNone); err != nil {
		t.Fatal(err)
	}
	if service.RegisteredHotkey() != "" || len(hotkeys.registered) != 0 {
		t.Fatalf("registered = %q %+v", service.RegisteredHotkey(), hotkeys.registered)
	}
}

func TestServiceHotkeyModeRegistersExactlyOneAccelerator(t *testing.T) {
	service, repository, hotkeys, _, _ := serviceForTest(t)
	fired := 0
	if err := service.Apply(func() { fired++ }); err != nil {
		t.Fatal(err)
	}
	if len(hotkeys.registered) != 1 {
		t.Fatalf("registered = %+v, want exactly one accelerator", hotkeys.registered)
	}
	if service.RegisteredHotkey() != "Ctrl + `" {
		t.Fatalf("registered = %q", service.RegisteredHotkey())
	}
	hotkeys.registered["Ctrl + `"]()
	if fired != 1 {
		t.Fatalf("the mode callback ran %d times", fired)
	}

	for _, mode := range []HotkeyMode{HotkeyModeToggle, HotkeyModeQuake, HotkeyModeTopdown} {
		if err := service.SetHotkeyMode(mode); err != nil {
			t.Fatalf("SetHotkeyMode(%q): %v", mode, err)
		}
		if len(hotkeys.registered) != 1 {
			t.Fatalf("mode %q registered %+v, want exactly one accelerator", mode, hotkeys.registered)
		}
		if service.RegisteredHotkey() != "Ctrl + `" {
			t.Fatalf("mode %q registered = %q", mode, service.RegisteredHotkey())
		}
		if service.Settings().HotkeyMode != mode {
			t.Fatalf("settings mode = %q, want %q", service.Settings().HotkeyMode, mode)
		}
	}

	if err := service.SetHotkeyMode(HotkeyModeNone); err != nil {
		t.Fatal(err)
	}
	if len(hotkeys.registered) != 0 || service.RegisteredHotkey() != "" {
		t.Fatalf("none left %+v registered", hotkeys.registered)
	}
	if last := repository.saves[len(repository.saves)-1]; last.HotkeyMode != HotkeyModeNone {
		t.Fatalf("saved mode = %q, want none", last.HotkeyMode)
	}
}

func TestServiceHotkeyModeRejectsUnknownValues(t *testing.T) {
	service, repository, hotkeys, _, _ := serviceForTest(t)
	if err := service.Apply(func() {}); err != nil {
		t.Fatal(err)
	}
	savesBefore := len(repository.saves)

	err := service.SetHotkeyMode(HotkeyMode("bogus"))
	if KindOf(err) != KindValidation {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if service.Settings().HotkeyMode != HotkeyModeQuake {
		t.Fatalf("mode = %q, want the previous quake", service.Settings().HotkeyMode)
	}
	if len(repository.saves) != savesBefore {
		t.Fatal("an invalid mode was persisted")
	}
	if len(hotkeys.registered) != 1 {
		t.Fatalf("registered = %+v, want the previous accelerator kept", hotkeys.registered)
	}
}

func TestServiceHotkeyModeRollsBackAndRestoresTheOldAccelerator(t *testing.T) {
	service, _, hotkeys, _, _ := serviceForTest(t)
	if err := service.Apply(func() {}); err != nil {
		t.Fatal(err)
	}
	modes := 0
	service.OnHotkeyModeChange(func(HotkeyMode) { modes++ })

	hotkeys.failOnce["Ctrl + `"] = errors.New("already taken once")
	err := service.SetHotkeyMode(HotkeyModeTopdown)
	if !errors.Is(err, ErrHotkey) {
		t.Fatalf("error = %v, want ErrHotkey", err)
	}
	if service.Settings().HotkeyMode != HotkeyModeQuake {
		t.Fatalf("mode = %q after a failed change", service.Settings().HotkeyMode)
	}
	if modes != 0 {
		t.Fatalf("the mode listener ran %d times for a failed change", modes)
	}
	if _, ok := hotkeys.registered["Ctrl + `"]; !ok {
		t.Fatalf("the old accelerator was not restored: %+v", hotkeys.registered)
	}
	if service.RegisteredHotkey() != "Ctrl + `" {
		t.Fatalf("registered = %q", service.RegisteredHotkey())
	}
	if service.HotkeyProblem() == nil {
		t.Fatal("the registration failure was not recorded for the Settings page")
	}

	// A later successful change clears the problem and notifies once.
	delete(hotkeys.fail, "Ctrl + `")
	if err := service.SetHotkeyMode(HotkeyModeTopdown); err != nil {
		t.Fatal(err)
	}
	if modes != 1 {
		t.Fatalf("the mode listener ran %d times, want 1", modes)
	}
	if service.HotkeyProblem() != nil {
		t.Fatalf("hotkey problem = %v after success", service.HotkeyProblem())
	}
}

func TestServiceNoneModeDoesNotRegisterAToggleHotkeyChange(t *testing.T) {
	service, _, hotkeys, _, _ := serviceForTest(t)
	if err := service.SetHotkeyMode(HotkeyModeNone); err != nil {
		t.Fatal(err)
	}
	hotkeys.fail["Ctrl+Shift+Space"] = errors.New("taken")
	if err := service.SetToggleWindowHotkey("Ctrl+Shift+Space"); err != nil {
		t.Fatalf("changing the shortcut in none mode: %v", err)
	}
	if len(hotkeys.registered) != 0 {
		t.Fatalf("none mode registered %+v", hotkeys.registered)
	}
	if service.Settings().ToggleWindowHotkey != "Ctrl+Shift+Space" {
		t.Fatalf("shortcut = %q", service.Settings().ToggleWindowHotkey)
	}
}

// TestServiceQuakeSourcePersistsAndRejectsUnknown covers the source setter:
// a valid source is saved and announced, and an unknown one is a validation
// error that changes nothing.
func TestServiceQuakeSourcePersistsAndRejectsUnknown(t *testing.T) {
	service, repository, _, _, _ := serviceForTest(t)
	var changed []QuakeSource
	service.OnQuakeSourceChange(func(source QuakeSource) {
		changed = append(changed, source)
	})

	if err := service.SetQuakeSource(QuakeSourceFollow); err != nil {
		t.Fatal(err)
	}
	if service.Settings().QuakeSource != QuakeSourceFollow {
		t.Fatalf("source = %q, want follow", service.Settings().QuakeSource)
	}
	if len(repository.saves) == 0 ||
		repository.saves[len(repository.saves)-1].QuakeSource != QuakeSourceFollow {
		t.Fatalf("persisted source = %+v, want follow", repository.saves)
	}
	if len(changed) != 1 || changed[0] != QuakeSourceFollow {
		t.Fatalf("source listener = %v, want one follow", changed)
	}

	savesBefore := len(repository.saves)
	err := service.SetQuakeSource(QuakeSource("bogus"))
	if KindOf(err) != KindValidation {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if service.Settings().QuakeSource != QuakeSourceFollow {
		t.Fatalf("source = %q after a rejected value", service.Settings().QuakeSource)
	}
	if len(repository.saves) != savesBefore {
		t.Fatal("an invalid source was persisted")
	}
	if len(changed) != 1 {
		t.Fatalf("the source listener ran %d times for a rejected value", len(changed))
	}
}

func TestServiceCapabilitiesReportDropdownSupport(t *testing.T) {
	repository := &fakeRepository{settings: Defaults("windows")}
	service := NewService(repository, &fakeHotkeys{fail: map[string]error{}}, nil, nil, WithGOOS("windows"))
	if !service.Capabilities().Dropdown {
		t.Fatal("windows does not report Dropdown support")
	}
	service = NewService(repository, &fakeHotkeys{fail: map[string]error{}}, nil, nil, WithGOOS("plan9"))
	if service.Capabilities().Dropdown {
		t.Fatal("plan9 reports Dropdown support")
	}
}

func TestServiceNotificationsRespectTogglesAndSupport(t *testing.T) {
	service, repository, _, _, notifier := serviceForTest(t)
	if err := service.Notify(Notification{Event: EventSessionEnded, Target: "box", Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notified) != 1 || notifier.notified[0].Target != "box" ||
		notifier.notified[0].Session != "s1" {
		t.Fatalf("notified = %+v, want the Session identity preserved for click routing", notifier.notified)
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
