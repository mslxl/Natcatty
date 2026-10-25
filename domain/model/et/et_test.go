package et

import (
	"errors"
	"strings"
	"testing"
)

func TestConfigNormalizeAndValidate(t *testing.T) {
	cfg := Config{
		Hostname: " example.test ",
		Username: " alice ",
		Auth:     Auth{Method: " KEY "},
		Jump:     &Hop{Hostname: " jump.test "},
	}.Normalized()
	if cfg.Port != DefaultPort || cfg.ETPort != DefaultETPort {
		t.Fatalf("ports = %d/%d", cfg.Port, cfg.ETPort)
	}
	if cfg.Auth.Method != AuthKey {
		t.Fatalf("auth method = %q", cfg.Auth.Method)
	}
	if cfg.Jump.Port != DefaultPort || cfg.Jump.ETPort != DefaultETPort {
		t.Fatalf("jump ports = %d/%d", cfg.Jump.Port, cfg.Jump.ETPort)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := (Config{}).Normalized().Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty hostname = %v", err)
	}
	bad := Config{Hostname: "host", ETPort: 70000}.Normalized()
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad et port = %v", err)
	}
	bad = Config{Hostname: "host", Auth: Auth{Method: "knock"}}.Normalized()
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad method = %v", err)
	}
	bad = Config{Hostname: "host", Jump: &Hop{}}.Normalized()
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty jump = %v", err)
	}
}

func TestCheckRoute(t *testing.T) {
	if err := CheckRoute(0, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckRoute(1, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckRoute(1, true); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("proxy error = %v", err)
	}
	if err := CheckRoute(2, false); !errors.Is(err, ErrMultipleJumpHosts) {
		t.Fatalf("two jumps error = %v", err)
	}
	if err := CheckRoute(2, true); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("proxy precedence = %v", err)
	}
}

func TestPreferredAuthentications(t *testing.T) {
	tests := []struct {
		name     string
		auth     Auth
		password bool
		key      bool
		want     string
	}{
		{"explicit password", Auth{Method: AuthPassword}, true, true, "password,keyboard-interactive"},
		{"automatic", Auth{Method: AuthAuto}, false, false, "publickey,password,keyboard-interactive"},
		{"key and password", Auth{Method: AuthKey}, true, true, "publickey,password,keyboard-interactive"},
		{"key only", Auth{Method: AuthKey}, false, true, "publickey"},
		{"certificate", Auth{Method: AuthCertificate}, false, true, "publickey"},
		{"automatic with password", Auth{Method: AuthAuto}, true, false, "publickey,password,keyboard-interactive"},
		{"nothing", Auth{Method: AuthAuto}, false, false, "publickey,password,keyboard-interactive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PreferredAuthentications(test.auth, test.password, test.key); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestArgs(t *testing.T) {
	cfg := Config{
		Hostname: "example.test",
		Username: "alice",
		ETPort:   DefaultETPort,
		Jump:     &Hop{Hostname: "jump.test", Username: "bob"},
	}.Normalized()
	args := cfg.Args([]string{"StrictHostKeyChecking=accept-new", "LogLevel=ERROR"}, "fallback")
	want := []string{
		"--ssh-option", "StrictHostKeyChecking=accept-new",
		"--ssh-option", "LogLevel=ERROR",
		"--jumphost", "bob@jump.test",
		"--jport", "2022",
		"alice@example.test",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}

	cfg.ETPort = 2023
	cfg.Jump = nil
	cfg.Username = ""
	args = cfg.Args(nil, "fallback")
	want = []string{"-p", "2023", "fallback@example.test"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestLegacyAlgorithmLines(t *testing.T) {
	lines := LegacyAlgorithmLines("  ")
	want := []string{
		"  KexAlgorithms +diffie-hellman-group14-sha1,diffie-hellman-group1-sha1",
		"  Ciphers +aes128-cbc,aes256-cbc,3des-cbc",
		"  HostKeyAlgorithms +ssh-rsa,ssh-dss",
		"  PubkeyAcceptedAlgorithms +ssh-rsa,ssh-dss",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestPasswordPromptMatchers(t *testing.T) {
	matchers := PasswordPromptMatchers("Example.Test", "Alice", 2222)
	want := []string{
		"example.test",
		"alice@example.test",
		"alice@example.test's password",
		"alice@example.test:2222",
	}
	if strings.Join(matchers, "\n") != strings.Join(want, "\n") {
		t.Fatalf("matchers = %#v", matchers)
	}
	if got := PasswordPromptMatchers("host", "", 0); len(got) != 1 || got[0] != "host" {
		t.Fatalf("no-user matchers = %#v", got)
	}
	if got := PasswordPromptMatchers("", "user", 22); got != nil {
		t.Fatalf("empty host = %#v", got)
	}
}

func TestPassphrasePromptMatchers(t *testing.T) {
	matchers := PassphrasePromptMatchers(`C:\Temp\et-home\.ssh\session-key`)
	if len(matchers) != 2 {
		t.Fatalf("matchers = %#v", matchers)
	}
	if !strings.Contains(matchers[0], "session-key") || matchers[1] != "session-key" {
		t.Fatalf("matchers = %#v", matchers)
	}
	if strings.Contains(matchers[0], `\`) {
		t.Fatalf("path not normalized: %q", matchers[0])
	}
}

func TestAskpassEntries(t *testing.T) {
	cfg := Config{
		Hostname: "example.test",
		Port:     2222,
		Username: "alice",
		Auth:     Auth{Password: "s3cret"},
		Jump: &Hop{
			Hostname: "jump.test",
			Username: "bob",
			Auth:     Auth{Password: "jump-pass"},
		},
	}.Normalized()
	entries := AskpassEntries(cfg)
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].Type != "password" || entries[0].Secret != "s3cret" ||
		entries[1].Secret != "jump-pass" {
		t.Fatalf("entries = %#v", entries)
	}
	entries = AddPassphraseEntry(entries, "/tmp/key", "key-pass")
	if len(entries) != 3 || entries[2].Type != "passphrase" || entries[2].Secret != "key-pass" {
		t.Fatalf("entries with passphrase = %#v", entries)
	}
	if entries := AddPassphraseEntry(nil, "/tmp/key", ""); entries != nil {
		t.Fatalf("empty passphrase added an entry: %#v", entries)
	}
}

func TestPickAskpassPassword(t *testing.T) {
	entries := []AskpassPrompt{
		{Type: "password", Matchers: []string{"example.test", "alice@example.test"}, Secret: "target"},
		{Type: "password", Matchers: []string{"jump.test", "bob@jump.test"}, Secret: "jump"},
		{Type: "passphrase", Matchers: []string{"/tmp/key"}, Secret: "key-pass"},
	}
	tests := []struct {
		prompt string
		secret string
		ok     bool
	}{
		{"alice@example.test's password: ", "target", true},
		{"bob@jump.test's password: ", "jump", true},
		{"Password: ", "", false},
		{"Enter passphrase for key '/tmp/key': ", "key-pass", true},
		{"Enter passphrase for key '/tmp/other': ", "", false},
		{"Verification code: ", "", false},
		{"One-time password: ", "", false},
		{"Please enter your 2FA token: ", "", false},
		{"Password: ", "", false},
		{"password (one time): ", "", false},
		{"请输入密码: ", "", false},
		{"密码: ", "", false},
		{"动态验证码: ", "", false},
		{"Enter password to unlock: ", "", false},
	}
	for _, test := range tests {
		got := PickAskpassEntry(test.prompt, entries)
		switch {
		case test.ok && got == nil:
			t.Fatalf("%q: no entry", test.prompt)
		case !test.ok && got != nil:
			t.Fatalf("%q: got %q", test.prompt, got.Secret)
		case test.ok && got.Secret != test.secret:
			t.Fatalf("%q: secret = %q, want %q", test.prompt, got.Secret, test.secret)
		}
	}
}

func TestPickAskpassSingleFallbackAndLongestMatch(t *testing.T) {
	entries := []AskpassPrompt{
		{Type: "password", Matchers: []string{"host"}, Secret: "generic"},
		{Type: "password", Matchers: []string{"user@host's password"}, Secret: "specific"},
	}
	got := PickAskpassEntry("user@host's password:", entries)
	if got == nil || got.Secret != "specific" {
		t.Fatalf("longest matcher lost: %#v", got)
	}

	single := []AskpassPrompt{{Type: "password", Matchers: []string{"unrelated"}, Secret: "only"}}
	got = PickAskpassEntry("Password:", single)
	if got == nil || got.Secret != "only" {
		t.Fatalf("single-entry fallback = %#v", got)
	}

	ambiguous := []AskpassPrompt{
		{Type: "password", Matchers: []string{"host-one"}, Secret: "one"},
		{Type: "password", Matchers: []string{"host-two"}, Secret: "two"},
	}
	if got := PickAskpassEntry("Password:", ambiguous); got != nil {
		t.Fatalf("ambiguous prompt chose %q", got.Secret)
	}
}

// A prompt that names the known login target is a password prompt even when it
// also contains a second-factor word from a localized warning.
func TestPickAskpassKnownLoginPromptWins(t *testing.T) {
	entries := []AskpassPrompt{
		{Type: "password", Matchers: []string{"alice@host's password"}, Secret: "target"},
	}
	got := PickAskpassEntry("alice@host's password (one-time code not required):", entries)
	if got == nil || got.Secret != "target" {
		t.Fatalf("known login prompt = %#v", got)
	}
}
