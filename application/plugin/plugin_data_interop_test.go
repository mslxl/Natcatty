package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/domain/model/plugindata"
)

func TestDeleteNamespaceClearsAQuarantinedNamespace(t *testing.T) {
	repo := &fakePluginDataRepo{
		records: []appplugin.PluginDataRecord{{
			Kind: plugindata.RecordHost, ID: "h1",
			Data: plugindata.Map{"acme.one": testPluginNamespace(1, nil)},
		}},
	}
	key := repo.quarantineKey(plugindata.RecordHost, "h1", "acme.bad")
	repo.raw = map[string][]byte{key: []byte(`{"schemaVersion":0}`)}
	host := appplugin.NewPluginDataHost(repo, fakePluginDataStates{})

	if err := host.DeleteNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.bad", true); err != nil {
		t.Fatalf("explicit delete of a quarantined namespace: %v", err)
	}
	if len(repo.cleared) != 1 || repo.cleared[0] != "acme.bad" {
		t.Fatalf("cleared = %+v", repo.cleared)
	}
	if repo.sets != 0 {
		t.Fatal("deleting a quarantine rewrote the record")
	}
	if _, ok := repo.raw[key]; ok {
		t.Fatal("the quarantine row survived an explicit delete")
	}

	if err := host.DeleteNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.missing", true); !errors.Is(err, appplugin.ErrPluginDataNotFound) {
		t.Fatalf("deleting an absent namespace = %v", err)
	}
	if len(repo.cleared) != 1 {
		t.Fatalf("an absent namespace cleared another row: %+v", repo.cleared)
	}
}

func TestPluginDataDiagnosticsBoundSecretFieldLabels(t *testing.T) {
	hugeField := strings.Repeat("f", 1<<20)
	hugeProvider := "plugin." + strings.Repeat("p", 1<<20) + ".data.db"
	repo := &fakePluginDataRepo{records: []appplugin.PluginDataRecord{{
		Kind: plugindata.RecordDataProfile, ID: "p1",
		Provider:   hugeProvider,
		SecretRefs: map[string]string{hugeField: "foreign:reference"},
	}}}
	host := appplugin.NewPluginDataHost(repo, fakePluginDataStates{},
		appplugin.WithPluginDataSecrets(fakePluginDataSecrets{}))
	report, err := host.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(report.Namespaces) != 1 {
		t.Fatalf("namespaces = %+v", report.Namespaces)
	}
	status := report.Namespaces[0]
	if len(status.PluginID) > plugindata.MaxDiagnosticPluginIDBytes ||
		len(status.Detail) > plugindata.MaxDiagnosticDetailBytes {
		t.Fatalf("unbounded diagnostic = plugin %d, detail %d",
			len(status.PluginID), len(status.Detail))
	}
	if !utf8.ValidString(status.PluginID) || !utf8.ValidString(status.Detail) {
		t.Fatalf("diagnostic is not valid UTF-8: %+v", status)
	}
	if status.Reason != plugindata.ReasonUnresolvedSecret {
		t.Fatalf("reason = %q", status.Reason)
	}
}
