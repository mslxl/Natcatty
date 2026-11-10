package workspace

import (
	"bytes"
	"encoding/json"
	"testing"
)

func pluginDescriptor() SessionDescriptor {
	return SessionDescriptor{
		Kind:     SessionPlugin,
		Label:    "provider sample",
		HostID:   "host-plugin",
		Hostname: "plugin.example.com",
		Port:     2222,
		Username: "demo",
		Provider: "acme.loop",
		Protocol: "x-acme-loop",
		Restore:  []byte("opaque-provider-state"),
	}
}

func pluginPayload(descriptor SessionDescriptor) Payload {
	return Payload{
		Version:   PayloadVersion,
		ActiveTab: 0,
		Tabs: []RestoredTab{{
			ID: "tab-plugin", Number: 1,
			Root: NewPaneNode("pane-plugin"), FocusPath: []int{0},
		}},
		Panes: []RestoredPane{restoredSessionPane("pane-plugin", descriptor)},
	}
}

func TestSanitizePayloadKeepsAPluginRestoreDescriptor(t *testing.T) {
	payload := pluginPayload(pluginDescriptor())
	sanitized := SanitizePayload(payload)
	if sanitized.Version != PayloadVersion {
		t.Fatalf("version = %d, want %d", sanitized.Version, PayloadVersion)
	}
	if len(sanitized.Panes) != 1 || sanitized.Panes[0].Session == nil {
		t.Fatalf("plugin Pane was dropped: %+v", sanitized.Panes)
	}
	descriptor := *sanitized.Panes[0].Session
	if descriptor.Kind != SessionPlugin || descriptor.Protocol != "x-acme-loop" ||
		descriptor.Provider != "acme.loop" ||
		!bytes.Equal(descriptor.Restore, []byte("opaque-provider-state")) {
		t.Fatalf("sanitized descriptor = %+v", descriptor)
	}
}

func TestSanitizePayloadDropsAnOversizedPluginRestore(t *testing.T) {
	descriptor := pluginDescriptor()
	descriptor.Restore = make([]byte, MaxSessionRestoreBytes+1)
	sanitized := SanitizePayload(pluginPayload(descriptor))
	if len(sanitized.Panes) != 1 || sanitized.Panes[0].Session == nil {
		t.Fatalf("the oversized descriptor dropped its Pane: %+v", sanitized.Panes)
	}
	if got := sanitized.Panes[0].Session.Restore; len(got) != 0 {
		t.Fatalf("oversized restore survived with %d bytes", len(got))
	}
}

func TestSanitizePayloadDropsAPluginWithABuiltinProtocol(t *testing.T) {
	descriptor := pluginDescriptor()
	descriptor.Protocol = "ssh"
	sanitized := SanitizePayload(pluginPayload(descriptor))
	if len(sanitized.Panes) != 0 {
		t.Fatalf("a plugin claiming a built-in protocol survived: %+v", sanitized.Panes)
	}
}

func TestPluginDescriptorJSONRoundTripsTheOpaqueRestore(t *testing.T) {
	payload := pluginPayload(pluginDescriptor())
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Payload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(decoded.Panes) != 1 || decoded.Panes[0].Session == nil {
		t.Fatalf("decoded panes = %+v", decoded.Panes)
	}
	descriptor := decoded.Panes[0].Session
	if descriptor.Kind != SessionPlugin ||
		descriptor.Provider != "acme.loop" ||
		descriptor.Protocol != "x-acme-loop" ||
		!bytes.Equal(descriptor.Restore, []byte("opaque-provider-state")) {
		t.Fatalf("round-tripped descriptor = %+v", descriptor)
	}
	if got := decoded.Panes[0].ID; got != "pane-plugin" {
		t.Fatalf("pane id = %q", got)
	}
}
