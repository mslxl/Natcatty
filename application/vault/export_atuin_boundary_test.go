package vault

import (
	"bytes"
	"context"
	"testing"
)

// atuinCredentialEntity mirrors historysync.SecretEntityType. It is spelled
// out here because application/historysync imports this package, so importing
// it back from this test would create a cycle.
const atuinCredentialEntity = "atuin_account"

// TestPortableExportNeverCarriesAtuinCredentials proves the Vault export path
// asks for credentials only through explicit Host, Key, Identity, Proxy, and
// Network references. A planted Atuin token and master key must never be read
// and must never appear in any portable format, including with IncludeSecrets.
func TestPortableExportNeverCarriesAtuinCredentials(t *testing.T) {
	service := exportService(t)
	secrets, ok := service.secrets.(*fakeSecrets)
	if !ok {
		t.Fatalf("export service secrets = %T, want the test fake", service.secrets)
	}
	tokenRef := SecretRef{EntityType: atuinCredentialEntity, EntityID: "default", Field: "session_token"}
	keyRef := SecretRef{EntityType: atuinCredentialEntity, EntityID: "default", Field: "master_key"}
	const token = "atuin-session-token-sentinel-value"
	const masterKey = "atuin-master-key-sentinel-value"
	secrets.values[tokenRef] = token
	secrets.values[keyRef] = masterKey
	secrets.getRefs = nil

	ctx := context.Background()
	for _, format := range ExportFormats() {
		exported, err := service.ExportVault(ctx, ExportOptions{
			Format: format, IncludeSecrets: true, ExportedAt: fixedExportTime,
		})
		if err != nil {
			t.Fatalf("ExportVault(%s): %v", format, err)
		}
		for _, credential := range []string{token, masterKey} {
			if bytes.Contains(exported.Content, []byte(credential)) {
				t.Fatalf("%s export contains an Atuin credential", format)
			}
		}
	}

	// The format loop must actually exercise the secret-reading path, or the
	// absence of an Atuin read would prove nothing.
	if len(secrets.getRefs) == 0 {
		t.Fatal("the export never read a secret")
	}
	for _, ref := range secrets.getRefs {
		if ref.EntityType == atuinCredentialEntity {
			t.Fatalf("a portable export read the Atuin credential %+v", ref)
		}
	}
}
