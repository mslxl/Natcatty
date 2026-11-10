package vaultexport

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func exportTestProfile(id, label string, data plugindata.Map) domainplugin.DataProfile {
	return domainplugin.DataProfile{
		ID: id, Provider: "plugin.acme.data.db", Label: label,
		Mode: domainplugin.DataModeReadOnly, PluginData: data,
		SecretRefs: map[string]string{"token": "vault:data-profile:" + id + ":token"},
	}
}

func exportTestNamespace(fields map[string]string) plugindata.Namespace {
	values := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		values[key] = json.RawMessage(value)
	}
	return plugindata.Namespace{SchemaVersion: 3, Fields: values}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buffer.String()
}

func TestJSONExportCarriesDataProfilePluginDataVerbatim(t *testing.T) {
	namespace := exportTestNamespace(map[string]string{
		"name":   `"profile"`,
		"nested": `{"a":[1,{"b":"c"}]}`,
	})
	namespace.Extra = map[string]json.RawMessage{"future": json.RawMessage(`{"x":1}`)}
	dataset := Dataset{DataProfiles: []domainplugin.DataProfile{
		exportTestProfile("p2", "Second", plugindata.Map{"acme.data": namespace}),
		exportTestProfile("p1", "First", plugindata.Map{"acme.data": exportTestNamespace(
			map[string]string{"name": `"first"`})}),
	}}
	content, err := Export(dataset, FormatJSON, Options{ExportedAt: stamp})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	again, err := Export(dataset, FormatJSON, Options{ExportedAt: stamp})
	if err != nil {
		t.Fatalf("Export again: %v", err)
	}
	if !bytes.Equal(content, again) {
		t.Fatal("the same Vault exported different bytes")
	}
	var document struct {
		DataProfiles []struct {
			ID         string            `json:"id"`
			Provider   string            `json:"provider"`
			SecretRefs map[string]string `json:"secretRefs"`
			PluginData map[string]struct {
				SchemaVersion int                        `json:"schemaVersion"`
				Fields        map[string]json.RawMessage `json:"fields"`
				Future        json.RawMessage            `json:"future"`
			} `json:"pluginData"`
		} `json:"dataProfiles"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(document.DataProfiles) != 2 ||
		document.DataProfiles[0].ID != "p1" || document.DataProfiles[1].ID != "p2" {
		t.Fatalf("dataProfiles = %+v, want ID-sorted records", document.DataProfiles)
	}
	exported := document.DataProfiles[1].PluginData["acme.data"]
	if document.DataProfiles[1].Provider != "plugin.acme.data.db" ||
		exported.SchemaVersion != 3 ||
		string(exported.Fields["name"]) != `"profile"` ||
		compactJSON(t, exported.Fields["nested"]) != `{"a":[1,{"b":"c"}]}` ||
		compactJSON(t, exported.Future) != `{"x":1}` {
		t.Fatalf("exported profile = %+v", document.DataProfiles[1])
	}
	// Secret fields stay references; the export never dereferences them.
	if document.DataProfiles[1].SecretRefs["token"] != "vault:data-profile:p2:token" {
		t.Fatalf("secretRefs = %+v", document.DataProfiles[1].SecretRefs)
	}
	if !bytes.Contains(content, []byte("dataProfiles")) {
		t.Fatal("the export omitted dataProfiles")
	}
}

func TestJSONExportLeavesHTMLCharactersInProviderFieldsUnescaped(t *testing.T) {
	fields := map[string]string{
		"less":    `"` + strings.Repeat("<", 3000) + `"`,
		"greater": `"` + strings.Repeat(">", 3000) + `"`,
		"amp":     `"` + strings.Repeat("&", 3000) + `"`,
	}
	namespace := exportTestNamespace(fields)
	host := domainvault.Host{
		ID: "h-html", Label: "html", Hostname: "html.example.test", Port: 22,
		Username: "demo", Protocol: domainvault.ProtocolSSH,
		PluginData: plugindata.Map{"acme.data": namespace},
	}
	profile := exportTestProfile("p-html", "HTML", plugindata.Map{
		"acme.data": namespace,
	})
	dataset := Dataset{
		Hosts:        []Host{{Host: host}},
		DataProfiles: []domainplugin.DataProfile{profile},
	}
	content, err := Export(dataset, FormatJSON, Options{ExportedAt: stamp})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	for _, escape := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if bytes.Contains(content, []byte(escape)) {
			t.Fatalf("JSON export expanded provider fields to %s", escape)
		}
	}
	var document struct {
		Hosts []struct {
			PluginData map[string]struct {
				Fields map[string]json.RawMessage `json:"fields"`
			} `json:"pluginData"`
		} `json:"hosts"`
		DataProfiles []struct {
			PluginData map[string]struct {
				Fields map[string]json.RawMessage `json:"fields"`
			} `json:"pluginData"`
		} `json:"dataProfiles"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(document.Hosts) != 1 || len(document.DataProfiles) != 1 {
		t.Fatalf("exported hosts = %d, profiles = %d",
			len(document.Hosts), len(document.DataProfiles))
	}
	for name, exported := range map[string]map[string]json.RawMessage{
		"host":    document.Hosts[0].PluginData["acme.data"].Fields,
		"profile": document.DataProfiles[0].PluginData["acme.data"].Fields,
	} {
		for key, want := range fields {
			if string(exported[key]) != want {
				t.Fatalf("%s %s field = %d bytes, want %d unchanged bytes",
					name, key, len(exported[key]), len(want))
			}
		}
	}
}

func TestJSONExportOmitsDataProfilesWhenTheVaultHasNone(t *testing.T) {
	content := exportFor(t, FormatJSON, Options{})
	if bytes.Contains(content, []byte("dataProfiles")) {
		t.Fatal("an empty DataProfile family left a document key behind")
	}
}
