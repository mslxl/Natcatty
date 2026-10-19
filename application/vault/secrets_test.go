package vault

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeSecrets is the field-secret port the Service is tested against.
type fakeSecrets struct {
	mode        SecretMode
	values      map[SecretRef]string
	setErr      error
	setFieldErr map[string]error
	getErr      error
	deleteErr   error
	unlockErr   error
	setPassErr  error
	sessionErr  error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{mode: SecretModeOS, values: map[SecretRef]string{}}
}

func (f *fakeSecrets) Mode() SecretMode { return f.mode }

func (f *fakeSecrets) Set(_ context.Context, ref SecretRef, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if err := f.setFieldErr[ref.Field]; err != nil {
		return err
	}
	f.values[ref] = value
	return nil
}

func (f *fakeSecrets) Get(_ context.Context, ref SecretRef) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[ref]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func (f *fakeSecrets) Delete(_ context.Context, ref SecretRef) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.values[ref]; !ok {
		return ErrSecretNotFound
	}
	delete(f.values, ref)
	return nil
}

func (f *fakeSecrets) SetPassphrase(context.Context, string) error {
	if f.setPassErr != nil {
		return f.setPassErr
	}
	f.mode = SecretModePassphrase
	return nil
}

func (f *fakeSecrets) UnlockWithPassphrase(context.Context, string) error {
	if f.unlockErr != nil {
		return f.unlockErr
	}
	f.mode = SecretModePassphrase
	return nil
}

func (f *fakeSecrets) UseSessionOnly(context.Context) error {
	if f.sessionErr != nil {
		return f.sessionErr
	}
	f.mode = SecretModeSession
	return nil
}

func newSecretService(t *testing.T, repo *fakeHosts, secrets FieldSecrets) *Service {
	t.Helper()
	var ids int
	return NewService(repo,
		WithSecrets(secrets),
		WithIDGenerator(func() model.HostID {
			ids++
			return model.HostID("host-" + strconv.Itoa(ids))
		}),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
}

func TestCreateHostEncryptsThePasswordOutsideTheHostRecord(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)

	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Username: "root", Password: "hunter2",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !host.HasPassword {
		t.Error("HasPassword = false, want true")
	}
	if got := secrets.values[HostSecretRef(host.ID, FieldPassword)]; got != "hunter2" {
		t.Errorf("stored secret = %q, want the password", got)
	}

	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Fatalf("Host JSON contains the password: %s", data)
	}
	if strings.Contains(string(data), "enc:v1:") {
		t.Fatalf("Host JSON contains a placeholder instead of structured metadata: %s", data)
	}
}

func TestCreateAndUpdatePersistTheAlgorithmSettings(t *testing.T) {
	repo := &fakeHosts{}
	service := newSecretService(t, repo, newFakeSecrets())

	draft := domainvault.Draft{
		Hostname:         "example.com",
		LegacyAlgorithms: true,
		SkipEcdsaHostKey: true,
		Algorithms: &domainvault.AlgorithmOverrides{
			Ciphers: []string{"aes128-cbc"},
		},
	}
	host, err := service.Create(context.Background(), draft)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !host.LegacyAlgorithms || !host.SkipEcdsaHostKey || host.Algorithms == nil ||
		len(host.Algorithms.Ciphers) != 1 || host.Algorithms.Ciphers[0] != "aes128-cbc" {
		t.Fatalf("Host algorithms = %+v, %+v", host.Algorithms, host)
	}
	draft.Algorithms.Ciphers[0] = "mutated"
	if host.Algorithms.Ciphers[0] != "aes128-cbc" {
		t.Fatal("Create shared the draft's algorithm slice")
	}

	updated, err := service.Update(context.Background(), host.ID, domainvault.Draft{
		Hostname: "example.com",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.LegacyAlgorithms || updated.SkipEcdsaHostKey || updated.Algorithms != nil {
		t.Fatalf("updated algorithms = %+v, %+v; want them cleared", updated.Algorithms, updated)
	}
}

func TestCreateHostEncryptsTheProxyPasswordOutsideTheHostRecord(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)

	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com",
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy.example.com", Port: 8080,
			Username: "alice", Password: "proxy-secret",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if host.Proxy == nil || !host.Proxy.HasPassword || host.Proxy.Password != "" {
		t.Fatalf("Host proxy = %+v, want HasPassword without plaintext", host.Proxy)
	}
	if got := secrets.values[HostSecretRef(host.ID, FieldProxyPassword)]; got != "proxy-secret" {
		t.Errorf("stored proxy secret = %q", got)
	}
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "proxy-secret") {
		t.Fatalf("Host JSON contains the proxy password: %s", data)
	}

	loaded, err := service.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.ID != host.ID || loaded.Proxy == nil || loaded.Proxy.Type != domainvault.ProxyHTTP {
		t.Fatalf("Get = %+v", loaded)
	}
}

func TestAFailedProxyPasswordWriteDoesNotLeaveTheCreatePassword(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	secrets.setFieldErr = map[string]error{FieldProxyPassword: ErrSecretsUnavailable}
	service := newSecretService(t, repo, secrets)

	_, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "hunter2",
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080, Password: "proxy-secret",
		},
	})
	if err == nil {
		t.Fatal("Create succeeded while the proxy password could not be stored")
	}
	if len(secrets.values) != 0 {
		t.Fatalf("stored secrets = %v, want a rollback after the Host was not saved", secrets.values)
	}
	if hosts, _ := repo.List(context.Background()); len(hosts) != 0 {
		t.Fatalf("Hosts = %v, want none", hosts)
	}
}

func TestUpdateRemovesTheProxyPasswordWhenTheProxyIsRemoved(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com",
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "proxy", Port: 1080, Password: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Proxy != nil {
		t.Fatalf("Proxy = %+v, want nil", updated.Proxy)
	}
	if _, ok := secrets.values[HostSecretRef(created.ID, FieldProxyPassword)]; ok {
		t.Fatal("the proxy password survived proxy removal")
	}
}

func TestUpdateKeepsTheStoredProxyPasswordWhenNoneIsSubmitted(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com",
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080, Password: "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com",
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080,
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Proxy == nil || !updated.Proxy.HasPassword {
		t.Fatalf("Proxy = %+v, want HasPassword preserved", updated.Proxy)
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldProxyPassword)]; got != "secret" {
		t.Fatalf("stored proxy secret = %q", got)
	}
}

func TestCreateHostRefusesAPasswordWithoutASecretStore(t *testing.T) {
	service := newTestService(t, &fakeHosts{})
	_, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "hunter2",
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindUnsupported {
		t.Fatalf("Create error = %v, want an unsupported Error", err)
	}
}

func TestUpdateWithAnEmptyPasswordKeepsTheStoredCredential(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Username: "root", Password: "hunter2",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com", Username: "admin",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated.HasPassword {
		t.Error("HasPassword = false after an edit that left the password blank")
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldPassword)]; got != "hunter2" {
		t.Errorf("stored secret = %q, want the original password", got)
	}
}

func TestUpdateReplacesThePasswordWhenOneIsSubmitted(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "old",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com", Password: "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasPassword {
		t.Error("HasPassword = false after a password update")
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldPassword)]; got != "new" {
		t.Errorf("stored secret = %q, want the new password", got)
	}
}

func TestAFailedUpdateRestoresThePreviousPassword(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "old",
	})
	if err != nil {
		t.Fatal(err)
	}

	repo.saveErr = errors.New("disk full")
	_, err = service.Update(context.Background(), host.ID, domainvault.Draft{
		Hostname: "example.com", Password: "new",
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindInternal {
		t.Fatalf("Update error = %v, want an internal Error", err)
	}
	stored, err := repo.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HasPassword {
		t.Error("the failed update cleared HasPassword")
	}
	if got := secrets.values[HostSecretRef(host.ID, FieldPassword)]; got != "old" {
		t.Fatalf("stored secret = %q, want the previous password", got)
	}
}

func TestSessionModeDoesNotClaimAPersistedPassword(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	secrets.mode = SecretModeSession
	service := newSecretService(t, repo, secrets)

	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "hunter2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if host.HasPassword {
		t.Error("a session-only password was announced by the Host record")
	}
	if got := secrets.values[HostSecretRef(host.ID, FieldPassword)]; got != "hunter2" {
		t.Fatalf("session secret = %q, want the password", got)
	}
}

func TestSecretLifecycle(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	host, err := service.Create(context.Background(), domainvault.Draft{Hostname: "example.com"})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.SetSecret(context.Background(), host.ID, FieldPassword, "hunter2"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	value, err := service.Secret(context.Background(), host.ID, FieldPassword)
	if err != nil || value != "hunter2" {
		t.Fatalf("Secret = %q, %v, want hunter2", value, err)
	}
	stored, err := repo.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HasPassword {
		t.Error("SetSecret did not mark the Host as having a password")
	}

	if err := service.DeleteSecret(context.Background(), host.ID, FieldPassword); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := service.Secret(context.Background(), host.ID, FieldPassword); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Secret after delete = %v, want ErrNotFound", err)
	}
	stored, _ = repo.Get(context.Background(), host.ID)
	if stored.HasPassword {
		t.Error("DeleteSecret did not clear HasPassword")
	}
}

func TestSecretReadsOnlyKnownHostFields(t *testing.T) {
	repo := &fakeHosts{}
	service := newSecretService(t, repo, newFakeSecrets())
	host, err := service.Create(context.Background(), domainvault.Draft{Hostname: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	err = service.SetSecret(context.Background(), host.ID, "notes", "secret")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("SetSecret error = %v, want a validation Error", err)
	}
}

func TestDeleteHostRemovesItsCredentials(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "hunter2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetSecret(context.Background(), host.ID, FieldPassphrase, "key-pass"); err != nil {
		t.Fatal(err)
	}

	if err := service.Delete(context.Background(), host.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, field := range []string{FieldPassword, FieldPassphrase} {
		if _, ok := secrets.values[HostSecretRef(host.ID, field)]; ok {
			t.Errorf("the Host's %s survived the Host delete", field)
		}
	}
}

func TestSecretErrorsKeepTheirKind(t *testing.T) {
	tests := []struct {
		name string
		set  error
		want Kind
	}{
		{"locked", ErrSecretsLocked, KindPermission},
		{"unavailable", ErrSecretsUnavailable, KindUnsupported},
		{"wrong passphrase", ErrWrongPassphrase, KindPermission},
		{"unreadable", ErrSecretUnreadable, KindInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeHosts{}
			secrets := newFakeSecrets()
			secrets.setErr = test.set
			service := newSecretService(t, repo, secrets)
			_, err := service.Create(context.Background(), domainvault.Draft{
				Hostname: "example.com", Password: "hunter2",
			})
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != test.want {
				t.Fatalf("Create error = %v, want kind %q", err, test.want)
			}
		})
	}
}

func TestSecretModeFollowsTheStore(t *testing.T) {
	secrets := newFakeSecrets()
	service := newSecretService(t, &fakeHosts{}, secrets)
	if service.SecretMode() != SecretModeOS {
		t.Errorf("SecretMode = %q, want os", service.SecretMode())
	}
	secrets.mode = SecretModeUnavailable
	if service.SecretMode() != SecretModeUnavailable {
		t.Errorf("SecretMode = %q, want unavailable", service.SecretMode())
	}
	if newTestService(t, &fakeHosts{}).SecretMode() != SecretModeOS {
		t.Error("a service without a secret store did not report OS mode")
	}
}

func TestSecretModeSetupPassesThrough(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.mode = SecretModeUnavailable
	service := newSecretService(t, &fakeHosts{}, secrets)

	if err := service.SetSecretsPassphrase(context.Background(), "pass"); err != nil {
		t.Fatalf("SetSecretsPassphrase: %v", err)
	}
	if secrets.mode != SecretModePassphrase {
		t.Errorf("mode = %q, want passphrase", secrets.mode)
	}
	if err := service.UnlockSecrets(context.Background(), "pass"); err != nil {
		t.Fatalf("UnlockSecrets: %v", err)
	}
	if err := service.UseSessionSecrets(context.Background()); err != nil {
		t.Fatalf("UseSessionSecrets: %v", err)
	}
	if secrets.mode != SecretModeSession {
		t.Errorf("mode = %q, want session", secrets.mode)
	}
}

func TestSecretSetupReportsATypedPermissionError(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.unlockErr = ErrWrongPassphrase
	service := newSecretService(t, &fakeHosts{}, secrets)
	err := service.UnlockSecrets(context.Background(), "wrong")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindPermission {
		t.Fatalf("UnlockSecrets error = %v, want a permission Error", err)
	}
}

func TestAFailedProxySecretWriteRestoresTheNewPassword(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Password: "old",
		Proxy: &domainvault.ProxyConfig{Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Removing the proxy deletes its secret before the Host record is saved.
	// The new Host password must not survive that failure.
	secrets.deleteErr = errors.New("secret store unavailable")
	_, err = service.Update(context.Background(), host.ID, domainvault.Draft{
		Hostname: "example.com", Password: "new",
	})
	if err == nil {
		t.Fatal("Update succeeded while the proxy secret delete failed")
	}
	if got := secrets.values[HostSecretRef(host.ID, FieldPassword)]; got != "old" {
		t.Fatalf("stored password = %q, want the previous value after rollback", got)
	}
}

func TestSetSecretMaintainsTheProxyPasswordFlag(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com",
		Proxy:    &domainvault.ProxyConfig{Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.SetSecret(context.Background(), host.ID, FieldProxyPassword, "proxy-pw"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	loaded, err := service.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Proxy == nil || !loaded.Proxy.HasPassword {
		t.Fatalf("proxy = %+v, want HasPassword after a stored proxy password", loaded.Proxy)
	}

	if err := service.SetSecret(context.Background(), host.ID, FieldProxyPassword, ""); err != nil {
		t.Fatalf("clear proxy password: %v", err)
	}
	loaded, err = service.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Proxy != nil && loaded.Proxy.HasPassword {
		t.Fatal("HasPassword stayed set after the proxy password was removed")
	}
}

func TestFailedSaveDoesNotDeleteAnUnchangedPassword(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Username: "root", Password: "hunter2",
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080, Password: "proxy-secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The editor round-trips empty credential inputs to mean "keep the saved
	// secret". A failed Host save must not be mistaken for a credential write
	// and roll back (delete) the untouched values.
	repo.saveErr = errors.New("disk full")
	_, err = service.Update(context.Background(), created.ID, domainvault.Draft{
		Label:    created.Label,
		Hostname: created.Hostname,
		Port:     created.Port,
		Username: created.Username,
		Protocol: created.Protocol,
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxyHTTP, Host: "proxy", Port: 8080,
		},
	})
	if err == nil {
		t.Fatal("Update succeeded despite a failed save")
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldPassword)]; got != "hunter2" {
		t.Fatalf("password after a failed save = %q, want it preserved", got)
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldProxyPassword)]; got != "proxy-secret" {
		t.Fatalf("proxy password after a failed save = %q, want it preserved", got)
	}
}
