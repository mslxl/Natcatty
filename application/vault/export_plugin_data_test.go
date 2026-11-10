package vault

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type fakeDataProfileReader []domainplugin.DataProfile

func (f fakeDataProfileReader) ListProfiles(context.Context) ([]domainplugin.DataProfile, error) {
	return f, nil
}

func TestJSONExportIncludesDataProfilePluginData(t *testing.T) {
	service := exportService(t)
	service.dataProfiles = fakeDataProfileReader{{
		ID: "p1", Provider: "plugin.acme.data.db", Label: "Profile",
		Mode: domainplugin.DataModeReadOnly,
		PluginData: plugindata.Map{"acme.data": {
			SchemaVersion: 2,
			Fields: map[string]json.RawMessage{
				"name": json.RawMessage(`"profile"`),
			},
			Extra: map[string]json.RawMessage{
				"future": json.RawMessage(`{"keep":true}`),
			},
		}},
		SecretRefs: map[string]string{"token": "vault:data-profile:p1:token"},
	}}
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}
	var document struct {
		DataProfiles []struct {
			ID         string            `json:"id"`
			PluginData map[string]any    `json:"pluginData"`
			SecretRefs map[string]string `json:"secretRefs"`
		} `json:"dataProfiles"`
	}
	if err := json.Unmarshal(export.Content, &document); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(document.DataProfiles) != 1 {
		t.Fatalf("dataProfiles = %+v", document.DataProfiles)
	}
	namespace, _ := document.DataProfiles[0].PluginData["acme.data"].(map[string]any)
	fields, _ := namespace["fields"].(map[string]any)
	if fields["name"] != "profile" || namespace["future"] == nil {
		t.Fatalf("exported pluginData = %+v", document.DataProfiles[0].PluginData)
	}
	if document.DataProfiles[0].SecretRefs["token"] != "vault:data-profile:p1:token" {
		t.Fatalf("exported secretRefs = %+v", document.DataProfiles[0].SecretRefs)
	}
}
