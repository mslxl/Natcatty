package vault

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"strings"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func testTOTPSecret() string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString([]byte("12345678901234567890"))
}

func TestCreateHostEncryptsTheTOTPOutsideTheHostRecord(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	raw := testTOTPSecret()

	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Username: "root", TOTP: raw,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !host.HasTOTP {
		t.Error("HasTOTP = false, want true")
	}
	stored := secrets.values[HostSecretRef(host.ID, FieldTOTP)]
	if !strings.HasPrefix(stored, "otpauth://totp/") || !strings.Contains(stored, "secret="+raw) {
		t.Fatalf("stored TOTP = %q, want a canonical URI with the secret", stored)
	}

	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), raw) || strings.Contains(string(data), "otpauth") ||
		strings.Contains(string(data), "secret") {
		t.Fatalf("Host JSON carries credential material: %s", data)
	}

	loaded, err := service.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !loaded.HasTOTP {
		t.Fatal("Get lost the HasTOTP marker")
	}

	resolved, err := service.ResolveAuth(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("ResolveAuth: %v", err)
	}
	if resolved.TOTP != stored {
		t.Fatalf("resolved TOTP = %q, want %q", resolved.TOTP, stored)
	}
}

func TestUpdateKeepsOrReplacesTheStoredTOTP(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	first := testTOTPSecret()
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", TOTP: first,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored := secrets.values[HostSecretRef(created.ID, FieldTOTP)]

	// A Host save that leaves the authenticator field blank keeps the value.
	kept, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com", Label: "web",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !kept.HasTOTP {
		t.Fatal("a blank TOTP field cleared the marker")
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldTOTP)]; got != stored {
		t.Fatalf("stored TOTP = %q, want the previous value", got)
	}

	second := base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString([]byte("abcdefghijabcdefghij"))
	replaced, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com", TOTP: second,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !replaced.HasTOTP {
		t.Fatal("a new TOTP did not set the marker")
	}
	if got := secrets.values[HostSecretRef(created.ID, FieldTOTP)]; !strings.Contains(got, second) {
		t.Fatalf("stored TOTP = %q, want the new secret", got)
	}
}

func TestUpdateClearsTheTOTPOnlyWhenAsked(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", TOTP: testTOTPSecret(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cleared, err := service.Update(context.Background(), created.ID, domainvault.Draft{
		Hostname: "example.com", ClearTOTP: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if cleared.HasTOTP {
		t.Fatal("HasTOTP survived an explicit clear")
	}
	if _, ok := secrets.values[HostSecretRef(created.ID, FieldTOTP)]; ok {
		t.Fatal("the TOTP ciphertext survived an explicit clear")
	}
}

func TestSessionModeTOTPIsNotAdvertisedOnTheHost(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	secrets.mode = SecretModeSession
	service := newSecretService(t, repo, secrets)

	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", TOTP: testTOTPSecret(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if host.HasTOTP {
		t.Fatal("a session-only TOTP was advertised on the Host record")
	}
	if _, ok := secrets.values[HostSecretRef(host.ID, FieldTOTP)]; !ok {
		t.Fatal("the session-only TOTP was not stored for this run")
	}
}

func TestDeleteRemovesTheStoredTOTP(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	created, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", TOTP: testTOTPSecret(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := service.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := secrets.values[HostSecretRef(created.ID, FieldTOTP)]; ok {
		t.Fatal("the TOTP survived deleting its Host")
	}
}

func TestSetSecretStoresATOTPAndTogglesTheMarker(t *testing.T) {
	repo := &fakeHosts{}
	secrets := newFakeSecrets()
	service := newSecretService(t, repo, secrets)
	host, err := service.Create(context.Background(), domainvault.Draft{Hostname: "example.com"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := service.SetSecret(context.Background(), host.ID, FieldTOTP, testTOTPSecret()); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	loaded, err := service.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !loaded.HasTOTP {
		t.Fatal("SetSecret did not set HasTOTP")
	}
	if err := service.DeleteSecret(context.Background(), host.ID, FieldTOTP); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	loaded, err = service.Get(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.HasTOTP {
		t.Fatal("DeleteSecret did not clear HasTOTP")
	}
}
