package vault

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestNormalizeKeyDraftTrimsAndKeepsSupportedTypes(t *testing.T) {
	draft, err := NormalizeKeyDraft(KeyDraft{
		Label:      "  workstation  ",
		PrivateKey: "  key material  ",
		Source:     KeySourceImported,
		Type:       "ed25519",
	})
	if err != nil {
		t.Fatalf("NormalizeKeyDraft: %v", err)
	}
	if draft.Label != "workstation" {
		t.Errorf("label = %q, want workstation", draft.Label)
	}
	if draft.Type != KeyTypeED25519 {
		t.Errorf("type = %q, want %q", draft.Type, KeyTypeED25519)
	}
	if draft.PrivateKey != "  key material  " {
		t.Errorf("private key was rewritten; key bytes must be preserved")
	}
	if draft.Source != KeySourceImported {
		t.Errorf("source = %q, want %q", draft.Source, KeySourceImported)
	}
}

func TestNormalizeKeyDraftRejectsAnUnsupportedTypeOrMissingLabel(t *testing.T) {
	_, err := NormalizeKeyDraft(KeyDraft{Label: "k", Type: "DSA"})
	var invalid *ValidationError
	if err == nil || !asValidation(err, &invalid) || invalid.Field != FieldKeyType {
		t.Fatalf("unsupported type error = %v, want a key-type validation error", err)
	}

	_, err = NormalizeKeyDraft(KeyDraft{Type: KeyTypeED25519})
	if err == nil || !asValidation(err, &invalid) || invalid.Field != FieldLabel {
		t.Fatalf("missing label error = %v, want a label validation error", err)
	}
}

func TestKeyTypeForAlgorithmCoversSSHAlgorithms(t *testing.T) {
	cases := map[string]KeyType{
		"ssh-ed25519":                  KeyTypeED25519,
		"ssh-rsa":                      KeyTypeRSA,
		"rsa-sha2-256":                 KeyTypeRSA,
		"ecdsa-sha2-nistp256":          KeyTypeECDSA,
		"ssh-rsa-cert-v01@openssh.com": KeyTypeRSA,
	}
	for algorithm, want := range cases {
		got, ok := KeyTypeForAlgorithm(algorithm)
		if !ok || got != want {
			t.Errorf("KeyTypeForAlgorithm(%q) = %q, %v; want %q, true", algorithm, got, ok, want)
		}
	}
	if _, ok := KeyTypeForAlgorithm("ssh-dss"); ok {
		t.Error("ssh-dss was accepted; DSA is not a supported SSH Key type")
	}
}

func TestNormalizeIdentityDraftValidatesMethodAndUsername(t *testing.T) {
	draft, err := NormalizeIdentityDraft(IdentityDraft{
		Label:      " deploy ",
		Username:   " root ",
		AuthMethod: "password",
		Password:   "secret",
	})
	if err != nil {
		t.Fatalf("NormalizeIdentityDraft: %v", err)
	}
	if draft.Label != "deploy" || draft.Username != "root" {
		t.Fatalf("normalized identity = %+v", draft)
	}

	_, err = NormalizeIdentityDraft(IdentityDraft{Label: "a", Username: "root", AuthMethod: IdentityAuthPassword})
	if err != nil {
		t.Errorf("password identity without a password should be allowed: %v", err)
	}

	_, err = NormalizeIdentityDraft(IdentityDraft{Label: "a", AuthMethod: IdentityAuthPassword})
	var invalid *ValidationError
	if err == nil || !asValidation(err, &invalid) || invalid.Field != FieldUsername {
		t.Fatalf("missing username error = %v, want a username validation error", err)
	}

	_, err = NormalizeIdentityDraft(IdentityDraft{
		Label: "a", Username: "root", AuthMethod: IdentityAuthKey,
	})
	if err == nil || !asValidation(err, &invalid) || invalid.Field != FieldKey {
		t.Fatalf("key identity without a key error = %v, want a key validation error", err)
	}
}

func TestResolveHostAuthPrefersIdentityMethodAndUsername(t *testing.T) {
	host := Host{
		Username:    "host-user",
		AuthMethod:  HostAuthPassword,
		IdentityID:  "identity-1",
		UseSSHAgent: true,
	}
	identity := &Identity{
		ID: "identity-1", Username: "identity-user",
		AuthMethod: IdentityAuthCertificate, KeyID: "key-1",
	}
	selection := ResolveHostAuth(host, identity)

	if selection.Method != HostAuthCertificate {
		t.Errorf("method = %q, want certificate", selection.Method)
	}
	if selection.Username != "identity-user" {
		t.Errorf("username = %q, want identity-user", selection.Username)
	}
	if selection.KeyID != "key-1" {
		t.Errorf("key = %q, want key-1", selection.KeyID)
	}
	if !selection.UseSSHAgent {
		t.Error("UseSSHAgent was dropped")
	}
}

func TestResolveHostAuthKeepsAutomaticModeWithASelectedKey(t *testing.T) {
	host := Host{Username: "root", KeyID: "key-1", AuthMethod: HostAuthAuto}
	selection := ResolveHostAuth(host, nil)
	if selection.Method != HostAuthAuto {
		t.Errorf("method = %q, want auto; the selected key belongs in the automatic order", selection.Method)
	}
	if selection.KeyID != "key-1" {
		t.Errorf("key = %q, want key-1", selection.KeyID)
	}
}

func TestResolveHostAuthPasswordModeDiscardsTheKey(t *testing.T) {
	host := Host{Username: "root", KeyID: "key-1", AuthMethod: HostAuthPassword}
	selection := ResolveHostAuth(host, nil)
	if selection.Method != HostAuthPassword {
		t.Errorf("method = %q, want password", selection.Method)
	}
	if selection.KeyID != "" {
		t.Errorf("key = %q; password mode must not carry a selected key", selection.KeyID)
	}
}

func TestHostAuthMethodDefaultsToAutoAndValidates(t *testing.T) {
	draft, err := NormalizeDraft(Draft{Label: "h", Hostname: "example.com"})
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if draft.AuthMethod != HostAuthAuto {
		t.Errorf("auth method = %q, want auto", draft.AuthMethod)
	}
	if _, err := NormalizeDraft(Draft{
		Label: "h", Hostname: "example.com", AuthMethod: "magic",
	}); err == nil {
		t.Fatal("unsupported auth method was accepted")
	}
}

func TestHostMergeKeyIgnoresAuthSelection(t *testing.T) {
	base := Host{Hostname: "example.com", Port: 22, Username: "root", Protocol: ProtocolSSH}
	withAuth := base
	withAuth.IdentityID = model.IdentityID("identity-1")
	withAuth.KeyID = model.KeyID("key-1")
	if base.MergeKey() != withAuth.MergeKey() {
		t.Error("changing authentication should not create a duplicate endpoint")
	}
}

// asValidation is a tiny helper so the tests do not repeat errors.As.
func asValidation(err error, target **ValidationError) bool {
	validation, ok := err.(*ValidationError)
	if !ok {
		return false
	}
	*target = validation
	return true
}
