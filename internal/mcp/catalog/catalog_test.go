package catalog

import (
	"reflect"
	"testing"
)

func TestCatalogNamesRPCMethodsAndSchemasAreUniqueAndValid(t *testing.T) {
	tools := All()
	names := map[string]bool{}
	methods := map[string]bool{}
	capabilities := map[string]bool{}
	for _, tool := range tools {
		if names[tool.Name] {
			t.Fatalf("duplicate tool name %q", tool.Name)
		}
		if methods[tool.RPCMethod] {
			t.Fatalf("duplicate rpc method %q", tool.RPCMethod)
		}
		if capabilities[tool.CapabilityID] {
			t.Fatalf("duplicate capability id %q", tool.CapabilityID)
		}
		names[tool.Name] = true
		methods[tool.RPCMethod] = true
		capabilities[tool.CapabilityID] = true
		if tool.Description == "" {
			t.Fatalf("tool %q has no description", tool.Name)
		}
		if tool.Surface != "public" {
			t.Fatalf("tool %q surface = %q, want public", tool.Name, tool.Surface)
		}
		if tool.Group == "" {
			t.Fatalf("tool %q has no group", tool.Name)
		}
		for field, input := range tool.Input {
			if input.Type != "string" && input.Type != "number" {
				t.Fatalf("tool %q field %q type = %q", tool.Name, field, input.Type)
			}
			if input.Description == "" {
				t.Fatalf("tool %q field %q has no description", tool.Name, field)
			}
		}
	}
}

// TestCatalogPolicyKeepsTheApprovalBypassClosed pins the security invariant:
// exactly session_close and terminal_stop bypass approval.
func TestCatalogPolicyKeepsTheApprovalBypassClosed(t *testing.T) {
	bypass := []string{}
	for _, tool := range All() {
		if tool.Policy.Write && !tool.RequiresApproval() {
			bypass = append(bypass, tool.Name)
		}
	}
	if !reflect.DeepEqual(bypass, []string{"session_close", "terminal_stop"}) {
		t.Fatalf("approval-bypassing writes = %v, want [session_close terminal_stop]", bypass)
	}
}

func TestInputSchemaMarksRequiredFields(t *testing.T) {
	tool, _ := ByName("sftp_list")
	schema := tool.InputSchema()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	required, _ := schema["required"].([]string)
	if !reflect.DeepEqual(required, []string{"sessionId", "path"}) {
		t.Fatalf("required = %v", required)
	}
	properties, _ := schema["properties"].(map[string]any)
	path, _ := properties["path"].(map[string]any)
	if path["type"] != "string" || path["description"] != "Remote directory path." {
		t.Fatalf("path property = %v", path)
	}
	if _, exists := path["optional"]; exists {
		t.Fatalf("JSON schema leaked the internal optional flag: %v", path)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
}
