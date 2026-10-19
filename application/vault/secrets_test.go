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
	mode       SecretMode
	values     map[SecretRef]string
	setErr     error
	getErr     error
	deleteErr  error
	unlockErr  error
	setPassErr error
	sessionErr error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{mode: SecretModeOS, values: map[SecretRef]string{}}
}

func (f *fakeSecrets) Mode() SecretMode { return f.mode }

func (f *fakeSecrets) Set(_ context.Context, ref SecretRef, value string) error {
	if f.setErr != nil {
		return f.setErr
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
