package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
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

// serialTestKeyRing is the OS credential store the real field-secret adapter is
// tested against.
type serialTestKeyRing struct {
	values map[string][]byte
}

func (k *serialTestKeyRing) Get(key string) ([]byte, error) {
	value, ok := k.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (k *serialTestKeyRing) Set(key string, value []byte) error {
	k.values[key] = append([]byte(nil), value...)
	return nil
}

func (k *serialTestKeyRing) Delete(key string) error {
	delete(k.values, key)
	return nil
}

// TestSerialSecretStoreSharesOnePasswordAcrossPortSpellings round-trips through
// the real encrypted field-secret store: a password saved under COM3 is found
// under every spelling of the same device, and a named pipe keeps one identity
// across its two local prefixes. A different device stays separate.
func TestSerialSecretStoreSharesOnePasswordAcrossPortSpellings(t *testing.T) {
	const password = "MaxAttempts=1"
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	secretsStore, err := store.OpenSecretStore(ctx, database,
		&serialTestKeyRing{values: map[string][]byte{}})
	if err != nil {
		t.Fatalf("open the secret store: %v", err)
	}
	t.Cleanup(secretsStore.Close)
	secrets := serialSecrets{store: secretsStore}

	if err := secrets.SetPassword(ctx, (domainserial.Config{Path: "COM3"}).SecretProfileID(), password); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	for _, spelling := range []string{"COM3", "com3", `\\.\COM3`, ` \\.\com3 `} {
		value, found, err := secrets.GetPassword(ctx, (domainserial.Config{Path: spelling}).SecretProfileID())
		if err != nil || !found || value != password {
			t.Fatalf("GetPassword(%q) = %q, %v, %v; want the COM3 password", spelling, value, found, err)
		}
	}
	if _, found, err := secrets.GetPassword(ctx,
		(domainserial.Config{Path: "COM4"}).SecretProfileID()); err != nil || found {
		t.Fatalf("COM4 found the COM3 password (found=%v, err=%v)", found, err)
	}

	if err := secrets.SetPassword(ctx,
		(domainserial.Config{Path: `\\.\pipe\Console`}).SecretProfileID(), password); err != nil {
		t.Fatalf("SetPassword pipe: %v", err)
	}
	if _, found, err := secrets.GetPassword(ctx,
		(domainserial.Config{Path: `\\?\pipe\CONSOLE`}).SecretProfileID()); err != nil || !found {
		t.Fatalf("the pipe's second spelling did not find the password (found=%v, err=%v)", found, err)
	}
}

// serialTestHosts is a one-Host Vault repository.
type serialTestHosts struct {
	host domainvault.Host
}

func (h *serialTestHosts) List(context.Context) ([]domainvault.Host, error) {
	return []domainvault.Host{h.host}, nil
}

func (h *serialTestHosts) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	if h.host.ID != id {
		return domainvault.Host{}, appvault.ErrNotFound
	}
	return h.host, nil
}

func (h *serialTestHosts) Save(context.Context, domainvault.Host) error { return nil }
func (h *serialTestHosts) Delete(context.Context, model.HostID) error   { return nil }

// serialTestSecrets is the Vault's field-secret port in memory.
type serialTestSecrets struct {
	values map[appvault.SecretRef]string
}

func (s *serialTestSecrets) Mode() appvault.SecretMode { return appvault.SecretModeOS }

func (s *serialTestSecrets) Set(_ context.Context, ref appvault.SecretRef, value string) error {
	s.values[ref] = value
	return nil
}

func (s *serialTestSecrets) Get(_ context.Context, ref appvault.SecretRef) (string, error) {
	value, ok := s.values[ref]
	if !ok {
		return "", appvault.ErrSecretNotFound
	}
	return value, nil
}

func (s *serialTestSecrets) Delete(_ context.Context, ref appvault.SecretRef) error {
	delete(s.values, ref)
	return nil
}

func (s *serialTestSecrets) SetPassphrase(context.Context, string) error        { return nil }
func (s *serialTestSecrets) UnlockWithPassphrase(context.Context, string) error { return nil }
func (s *serialTestSecrets) UseSessionOnly(context.Context) error               { return nil }

// TestTelnetAutoLoginOptionsPrefersTheSavedVaultLogin covers the Vault branch
// that a Quick Connect cannot exercise: a saved Telnet Host's stored login
// replaces the one-shot password typed into the dialog, matching what Copy
// Credentials shows.
func TestTelnetAutoLoginOptionsPrefersTheSavedVaultLogin(t *testing.T) {
	const password = "MaxAttempts=1"
	saved := domainvault.Host{
		ID: "host-telnet", Label: "console", Hostname: "console.local",
		Username: "vault-user", Protocol: domainvault.ProtocolTelnet,
	}
	secrets := &serialTestSecrets{values: map[appvault.SecretRef]string{
		appvault.HostSecretRef(saved.ID, appvault.FieldPassword): password,
	}}
	service := appvault.NewService(&serialTestHosts{host: saved}, appvault.WithSecrets(secrets))

	// A stale dialog copy with a one-shot password must not win.
	dialogHost := domainvault.Host{ID: saved.ID, Username: "quick-user"}
	options := telnetAutoLoginOptions(context.Background(), service, dialogHost, "one-shot")
	if options == nil {
		t.Fatal("a saved Host with a stored password produced no options")
	}
	if options.Username != "vault-user" || options.Password != password || !options.PasswordSet {
		t.Fatalf("options = %+v, want the saved Vault login", options)
	}
}
