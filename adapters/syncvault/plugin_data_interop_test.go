package syncvault

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func quarantinedHostDiagnostic() plugindata.Diagnostic {
	return plugindata.Diagnostic{
		RecordKind: plugindata.RecordHost, RecordID: "h1", PluginID: "acme.bad",
		Reason: plugindata.ReasonNamespace, State: plugindata.StateQuarantined,
	}
}

func TestSnapshotReportsStoreLevelQuarantines(t *testing.T) {
	records := &fakeRecords{diagnostics: []plugindata.Diagnostic{
		quarantinedHostDiagnostic(),
	}}
	vault := New(records, &fakeSecrets{mode: appvault.SecretModeOS})
	if _, err := vault.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	diagnostics := vault.PluginDataDiagnostics()
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.bad" ||
		diagnostics[0].RecordID != "h1" {
		t.Fatalf("store-level quarantine was not reported: %+v", diagnostics)
	}
}

func TestApplyReportsStoreQuarantinesAlongsideUnresolvedSecrets(t *testing.T) {
	previous := dataProfileForSync("p1", "token")
	records := &fakeRecords{
		dataProfiles: []domainplugin.DataProfile{previous},
		diagnostics:  []plugindata.Diagnostic{quarantinedHostDiagnostic()},
	}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS}
	incoming := dataProfileForSync("p1", "token")
	payload := appsync.Payload{DataProfiles: []domainplugin.DataProfile{incoming}}
	vault := New(records, secrets)
	if err := vault.Apply(context.Background(), payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	diagnostics := vault.PluginDataDiagnostics()
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want the unresolved secret and the quarantine",
			diagnostics)
	}
	reasons := map[plugindata.Reason]bool{}
	for _, diagnostic := range diagnostics {
		reasons[diagnostic.Reason] = true
	}
	if !reasons[plugindata.ReasonUnresolvedSecret] ||
		!reasons[plugindata.ReasonNamespace] {
		t.Fatalf("reasons = %v", reasons)
	}
}

func TestSyncVaultBoundsUnresolvedSecretDiagnostics(t *testing.T) {
	hugeField := strings.Repeat("f", 1<<20)
	hugeProvider := "plugin." + strings.Repeat("p", 1<<20) + ".data.db"
	profile := domainplugin.DataProfile{
		ID: "p1", Provider: hugeProvider, Mode: domainplugin.DataModeReadOnly,
		SecretRefs: map[string]string{hugeField: "foreign:reference"},
	}
	records := &fakeRecords{dataProfiles: []domainplugin.DataProfile{profile}}
	vault := New(records, &fakeSecrets{mode: appvault.SecretModeOS})
	if _, err := vault.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	diagnostics := vault.PluginDataDiagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d", len(diagnostics))
	}
	diagnostic := diagnostics[0]
	if len(diagnostic.PluginID) > plugindata.MaxDiagnosticPluginIDBytes ||
		len(diagnostic.Detail) > plugindata.MaxDiagnosticDetailBytes {
		t.Fatalf("unbounded syncvault diagnostic = plugin %d, detail %d",
			len(diagnostic.PluginID), len(diagnostic.Detail))
	}
	if !utf8.ValidString(diagnostic.PluginID) ||
		!utf8.ValidString(diagnostic.Detail) {
		t.Fatalf("syncvault diagnostic is not valid UTF-8: %+v", diagnostic)
	}
}

func TestSyncVaultBoundsNamespaceKeysInUnresolvedSecretDiagnostics(t *testing.T) {
	hugeNamespace := strings.Repeat("n", 1<<20)
	profile := domainplugin.DataProfile{
		ID: "p2", Provider: "plugin.acme.data.db", Mode: domainplugin.DataModeReadOnly,
		PluginData: plugindata.Map{
			hugeNamespace: {SchemaVersion: 1},
		},
		SecretRefs: map[string]string{"token": "foreign:reference"},
	}
	records := &fakeRecords{dataProfiles: []domainplugin.DataProfile{profile}}
	vault := New(records, &fakeSecrets{mode: appvault.SecretModeOS})
	if _, err := vault.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	diagnostics := vault.PluginDataDiagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d", len(diagnostics))
	}
	diagnostic := diagnostics[0]
	if len(diagnostic.PluginID) > plugindata.MaxDiagnosticPluginIDBytes ||
		len(diagnostic.Detail) > plugindata.MaxDiagnosticDetailBytes {
		t.Fatalf("unbounded namespace diagnostic = plugin %d, detail %d",
			len(diagnostic.PluginID), len(diagnostic.Detail))
	}
	if !utf8.ValidString(diagnostic.PluginID) {
		t.Fatalf("namespace diagnostic is not valid UTF-8: %q", diagnostic.PluginID)
	}
}

func TestSyncVaultPropagatesAQuarantineReadFailure(t *testing.T) {
	records := &fakeRecords{diagErr: errors.New("read failed")}
	vault := New(records, &fakeSecrets{mode: appvault.SecretModeOS})
	if _, err := vault.Snapshot(context.Background()); !errors.Is(err, records.diagErr) {
		t.Fatalf("Snapshot error = %v", err)
	}
	if err := vault.Apply(context.Background(), appsync.Payload{}); !errors.Is(err, records.diagErr) {
		t.Fatalf("Apply error = %v", err)
	}
}
