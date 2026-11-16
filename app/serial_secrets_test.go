package app

import (
	"context"
	"errors"
	"testing"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeSerialSecretReader struct {
	values map[string]string
	err    error
}

func (f fakeSerialSecretReader) GetPassword(_ context.Context, profile string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	value, ok := f.values[profile]
	return value, ok, nil
}

func TestSerialAutoLoginOptionsReadsTheProfilePassword(t *testing.T) {
	cfg := domainserial.Config{
		Path: "/dev/ttyUSB0",
		AutoLogin: &domainserial.AutoLogin{
			Enabled: true, Username: "admin", PasswordSet: true, MaxAttempts: 2,
			Replies: []domainserial.AutoLoginReply{{Expect: "Site code:", Respond: "lab-1"}},
		},
	}.Normalized()
	reader := fakeSerialSecretReader{values: map[string]string{"/dev/ttyUSB0": "saved-secret"}}

	options := serialAutoLoginOptions(context.Background(), reader, cfg)
	if options == nil {
		t.Fatal("a configured profile produced no options")
	}
	if options.Username != "admin" || options.Password != "saved-secret" ||
		!options.PasswordSet || options.MaxAttempts != 2 {
		t.Fatalf("options = %+v, want the profile and stored password", options)
	}
}

func TestSerialAutoLoginOptionsKeepsTheUsernameWhenTheSecretFails(t *testing.T) {
	cfg := domainserial.Config{
		Path:      "COM3",
		AutoLogin: &domainserial.AutoLogin{Enabled: true, Username: "admin", PasswordSet: true},
	}.Normalized()
	options := serialAutoLoginOptions(context.Background(),
		fakeSerialSecretReader{err: errors.New("locked")}, cfg)
	if options == nil || options.Username != "admin" || options.PasswordSet {
		t.Fatalf("options = %+v, want the username without a password", options)
	}
}

func TestSerialAutoLoginOptionsIsIdleWhenOff(t *testing.T) {
	cfg := domainserial.Config{
		Path:      "COM3",
		AutoLogin: &domainserial.AutoLogin{Username: "admin", PasswordSet: true},
	}.Normalized()
	if options := serialAutoLoginOptions(context.Background(),
		fakeSerialSecretReader{values: map[string]string{"COM3": "secret"}}, cfg); options != nil {
		t.Fatalf("options = %+v, want nil while auto-login is off", options)
	}
}

func TestTelnetAutoLoginOptionsUsesAQuickConnectLogin(t *testing.T) {
	host := domainvault.Host{Label: "console", Hostname: "console.local", Username: "admin"}
	options := telnetAutoLoginOptions(context.Background(), nil, host, "one-shot")
	if options == nil || options.Username != "admin" ||
		options.Password != "one-shot" || !options.PasswordSet {
		t.Fatalf("options = %+v, want the quick-connect login", options)
	}
}

func TestTelnetAutoLoginOptionsIsIdleWithoutCredentials(t *testing.T) {
	host := domainvault.Host{Hostname: "console.local"}
	if options := telnetAutoLoginOptions(context.Background(), nil, host, ""); options != nil {
		t.Fatalf("options = %+v, want nil without credentials", options)
	}
}
