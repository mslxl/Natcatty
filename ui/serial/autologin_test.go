package serial

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// fakeSecretStore is the dialog's encrypted-store seam in memory.
type fakeSecretStore struct {
	values  map[string]string
	getErr  error
	setErr  error
	gets    []string
	sets    []string
	deletes []string
}

func newFakeSecretStore(values map[string]string) *fakeSecretStore {
	return &fakeSecretStore{values: values}
}

func (f *fakeSecretStore) GetPassword(_ context.Context, profile string) (string, bool, error) {
	f.gets = append(f.gets, profile)
	if f.getErr != nil {
		return "", false, f.getErr
	}
	value, ok := f.values[profile]
	return value, ok, nil
}

func (f *fakeSecretStore) SetPassword(_ context.Context, profile, password string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, profile)
	if f.values == nil {
		f.values = make(map[string]string)
	}
	f.values[profile] = password
	return nil
}

func (f *fakeSecretStore) DeletePassword(_ context.Context, profile string) error {
	f.deletes = append(f.deletes, profile)
	delete(f.values, profile)
	return nil
}

func autoLoginState(t *testing.T, opener *recordedOpen, secrets SecretStore) *State {
	t.Helper()
	options := []Option{WithRedraw(func() {})}
	if secrets != nil {
		options = append(options, WithSecretStore(secrets))
	}
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "COM3"}}, nil), opener.open, options...)
	state.Show()
	return state
}

// TestAutoLoginOffLeavesTheConfigUnchanged keeps the dialog usable exactly as
// before the feature: fields can hold text, but nothing is persisted and the
// opened configuration has no profile.
func TestAutoLoginOffLeavesTheConfigUnchanged(t *testing.T) {
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(nil)
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginPassword("secret")
	state.SetAutoLoginReplies("Hostname: => console")

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	cfg := opener.last(t)
	if cfg.AutoLogin != nil {
		t.Fatalf("config auto-login = %+v, want nil while disabled", cfg.AutoLogin)
	}
	if len(secrets.sets) != 0 || len(secrets.deletes) != 0 {
		t.Fatalf("secret writes = %v/%v, want none", secrets.sets, secrets.deletes)
	}
}

// TestConnectStoresThePasswordAndKeepsItOutOfTheConfig proves the password
// travels only to the field-secret store: the configuration that reaches the
// opener carries the profile but can never serialize the secret.
func TestConnectStoresThePasswordAndKeepsItOutOfTheConfig(t *testing.T) {
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(nil)
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginPassword("pa55-secret")
	state.SetAutoLoginAttempts("2")
	state.SetAutoLoginReplies("Site code: => lab-1\nHostname = console")

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	cfg := opener.last(t)
	if cfg.AutoLogin == nil || !cfg.AutoLogin.Enabled || cfg.AutoLogin.Username != "admin" ||
		!cfg.AutoLogin.PasswordSet || cfg.AutoLogin.MaxAttempts != 2 {
		t.Fatalf("auto-login = %+v, want the saved profile", cfg.AutoLogin)
	}
	if len(cfg.AutoLogin.Replies) != 2 ||
		cfg.AutoLogin.Replies[0] != (domainserial.AutoLoginReply{Expect: "Site code:", Respond: "lab-1"}) ||
		cfg.AutoLogin.Replies[1] != (domainserial.AutoLoginReply{Expect: "Hostname", Respond: "console"}) {
		t.Fatalf("replies = %+v, want both parsed rules", cfg.AutoLogin.Replies)
	}
	if got := secrets.values[cfg.SecretProfileID()]; got != "pa55-secret" {
		t.Fatalf("stored password = %q, want the typed value", got)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if strings.Contains(string(encoded), "pa55-secret") {
		t.Fatalf("config JSON contains the password: %s", encoded)
	}
	if state.AutoLoginPassword() != "" {
		t.Fatal("Connect kept the typed password in the form")
	}
}

// TestPortChangeDoesNotReplayThePreviousPortsPassword is the reported
// sequence: connect COM3 with a password, reopen the dialog, select COM5, and
// connect. COM5 must not inherit COM3's credential, either in the opened
// configuration or in the secret store.
func TestPortChangeDoesNotReplayThePreviousPortsPassword(t *testing.T) {
	const password = "MaxAttempts=1"
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(nil)
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginPassword(password)

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect COM3: %v", err)
	}
	if got := secrets.values["COM3"]; got != password {
		t.Fatalf("stored COM3 password = %q, want the typed value", got)
	}
	if got := state.AutoLoginPassword(); got != "" {
		t.Fatalf("the typed password survived Connect: %q", got)
	}

	state.Show()
	state.SetPort("COM5")
	if got := state.AutoLoginPassword(); got != "" {
		t.Fatalf("a port change kept the previous port's password: %q", got)
	}
	if err := state.Connect(); err != nil {
		t.Fatalf("Connect COM5: %v", err)
	}
	cfg := opener.last(t)
	if cfg.Path != "COM5" {
		t.Fatalf("opened path = %q, want COM5", cfg.Path)
	}
	if cfg.AutoLogin == nil || cfg.AutoLogin.PasswordSet {
		t.Fatalf("COM5 auto-login = %+v, want no inherited credential", cfg.AutoLogin)
	}
	if _, ok := secrets.values["COM5"]; ok {
		t.Fatal("the secret store gained a COM5 entry from the COM3 password")
	}
	if got := secrets.values["COM3"]; got != password {
		t.Fatalf("the COM3 password changed to %q", got)
	}
}

// TestPortChangeBeforeConnectDiscardsTheTypedPassword covers the same leak
// before the first connect: the field is filled for COM3, the user switches to
// COM5, and Connect must not store the COM3 value for COM5.
func TestPortChangeBeforeConnectDiscardsTheTypedPassword(t *testing.T) {
	const password = "MaxAttempts=1"
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(nil)
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginPassword(password)
	state.SetPort("COM5")

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	cfg := opener.last(t)
	if cfg.Path != "COM5" || cfg.AutoLogin == nil || cfg.AutoLogin.PasswordSet {
		t.Fatalf("COM5 config = %+v, want no typed COM3 credential", cfg)
	}
	if len(secrets.values) != 0 {
		t.Fatalf("secret store = %v, want no entry", secrets.values)
	}
}

// TestReconnectToTheSamePortReusesTheStoredPassword documents the lifecycle
// choice: the typed field is cleared after a connect, so a second connect to
// the same port finds the stored profile password and re-enables auto-login.
func TestReconnectToTheSamePortReusesTheStoredPassword(t *testing.T) {
	const password = "MaxAttempts=1"
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(nil)
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginPassword(password)
	if err := state.Connect(); err != nil {
		t.Fatalf("first Connect: %v", err)
	}

	state.Show()
	if err := state.Connect(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	cfg := opener.last(t)
	if cfg.Path != "COM3" || cfg.AutoLogin == nil || !cfg.AutoLogin.PasswordSet {
		t.Fatalf("reconnect config = %+v, want the stored COM3 password reused", cfg)
	}
	if got := secrets.values["COM3"]; got != password {
		t.Fatalf("stored password = %q, want it unchanged", got)
	}
}

// TestConnectKeepsAnExistingPasswordWhenTheFieldIsEmpty keeps framing edits
// from silently disabling a device's saved login.
func TestConnectKeepsAnExistingPasswordWhenTheFieldIsEmpty(t *testing.T) {
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(map[string]string{"COM3": "saved-secret"})
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	cfg := opener.last(t)
	if cfg.AutoLogin == nil || !cfg.AutoLogin.PasswordSet {
		t.Fatalf("auto-login = %+v, want the existing password kept", cfg.AutoLogin)
	}
	if got := secrets.values["COM3"]; got != "saved-secret" {
		t.Fatalf("stored password = %q, want it unchanged", got)
	}
}

func TestConnectForgetsTheStoredPassword(t *testing.T) {
	opener := &recordedOpen{}
	secrets := newFakeSecretStore(map[string]string{"COM3": "saved-secret"})
	state := autoLoginState(t, opener, secrets)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginForgetPassword(true)

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, ok := secrets.values["COM3"]; ok {
		t.Fatal("the stored password was not deleted")
	}
	if cfg := opener.last(t); cfg.AutoLogin == nil || cfg.AutoLogin.PasswordSet {
		t.Fatalf("auto-login = %+v, want PasswordSet false", cfg.AutoLogin)
	}
}

// TestConnectRefusesAPasswordWithoutASecretStore reports the missing store
// instead of silently dropping a credential.
func TestConnectRefusesAPasswordWithoutASecretStore(t *testing.T) {
	opener := &recordedOpen{}
	state := autoLoginState(t, opener, nil)
	state.SetAutoLoginEnabled(true)
	state.SetAutoLoginUsername("admin")
	state.SetAutoLoginPassword("secret")

	err := state.Connect()
	if !errors.Is(err, ErrNoSecretStore) {
		t.Fatalf("Connect error = %v, want ErrNoSecretStore", err)
	}
	if opener.count() != 0 {
		t.Fatalf("opener calls = %d, want none", opener.count())
	}
	if !state.Visible() {
		t.Fatal("the failure closed the dialog")
	}
}

func TestParseRepliesIgnoresUnreadableLines(t *testing.T) {
	got := parseReplies("Hostname: => console\nnot a rule\n => empty\nSite = lab\n")
	want := []domainserial.AutoLoginReply{
		{Expect: "Hostname:", Respond: "console"},
		{Expect: "Site", Respond: "lab"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseReplies = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("parseReplies[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}
