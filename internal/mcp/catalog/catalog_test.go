package catalog

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestCatalogMatchesTheExtractedOriginalSnapshot guards the embedded data
// against drift. testdata/original.json is the verbatim listMcpTools() output
// extracted from the original Electron tree at 73d15da7:
//
//	git archive 73d15da7 electron/capabilities | tar -x -C <empty dir>
//	node -e 'require(.../toolSurfaces.cjs).listMcpTools()' > original.json
//
// The embedded catalog adds only derived fields (group, surface,
// requiresApproval); the original fields must stay byte-for-byte equal after
// JSON normalization.
func TestCatalogMatchesTheExtractedOriginalSnapshot(t *testing.T) {
	raw, err := os.ReadFile("testdata/original.json")
	if err != nil {
		t.Fatalf("read the original snapshot: %v", err)
	}
	var want struct {
		Count int `json:"count"`
		Tools []struct {
			CapabilityID    string                `json:"capabilityId"`
			Domain          string                `json:"domain"`
			MCPTool         string                `json:"mcpTool"`
			RPCMethod       string                `json:"rpcMethod"`
			PublicRPCMethod string                `json:"publicRpcMethod"`
			Description     string                `json:"description"`
			Policy          Policy                `json:"policy"`
			InputShape      map[string]InputField `json:"inputShape"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode the original snapshot: %v", err)
	}
	if want.Count != 69 || len(want.Tools) != 69 {
		t.Fatalf("original snapshot count = %d/%d, want 69", want.Count, len(want.Tools))
	}

	got := All()
	if len(got) != len(want.Tools) {
		t.Fatalf("catalog tools = %d, want %d", len(got), len(want.Tools))
	}
	for index, expected := range want.Tools {
		actual := got[index]
		if actual.Name != expected.MCPTool ||
			actual.CapabilityID != expected.CapabilityID ||
			actual.Domain != expected.Domain ||
			actual.RPCMethod != expected.RPCMethod ||
			actual.PublicRPCMethod != expected.PublicRPCMethod ||
			actual.Description != expected.Description {
			t.Fatalf("tool %d differs from the original:\n got %+v\nwant %+v",
				index, actual, expected)
		}
		if !reflect.DeepEqual(actual.Policy, expected.Policy) {
			t.Fatalf("tool %q policy = %+v, want %+v", actual.Name, actual.Policy, expected.Policy)
		}
		if !reflect.DeepEqual(actual.Input, expected.InputShape) {
			t.Fatalf("tool %q input = %+v, want %+v", actual.Name, actual.Input, expected.InputShape)
		}
	}
}

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

// TestCatalogPolicyMatchesTheOriginalSecuritySplit pins the write/approval
// counts the rewrite spec records: 43 writes, 41 needing approval, and exactly
// session_close and terminal_stop bypassing it.
func TestCatalogPolicyMatchesTheOriginalSecuritySplit(t *testing.T) {
	writes := 0
	requiresApproval := 0
	bypass := []string{}
	for _, tool := range All() {
		if tool.Policy.Write {
			writes++
			if tool.RequiresApproval() {
				requiresApproval++
			} else {
				bypass = append(bypass, tool.Name)
			}
		}
	}
	if writes != 43 {
		t.Fatalf("write tools = %d, want 43", writes)
	}
	if requiresApproval != 41 {
		t.Fatalf("approval-requiring writes = %d, want 41", requiresApproval)
	}
	if !reflect.DeepEqual(bypass, []string{"session_close", "terminal_stop"}) {
		t.Fatalf("approval-bypassing writes = %v, want [session_close terminal_stop]", bypass)
	}
}

func TestCatalogKeepsTheOriginalToolShapes(t *testing.T) {
	tests := []struct {
		name        string
		description string
		fields      map[string]InputField
	}{
		{
			name:        "sftp_list",
			description: "List a remote directory over the session file backend (SFTP or SCP-mode).",
			fields: map[string]InputField{
				"sessionId": {Type: "string", Description: "SFTP session ID."},
				"path":      {Type: "string", Description: "Remote directory path."},
			},
		},
		{
			name:   "get_environment",
			fields: map[string]InputField{},
		},
		{
			name: "vault_notes_get",
			fields: map[string]InputField{
				"noteId":            {Type: "string", Description: "Vault note ID from vault_notes_list."},
				"offset":            {Type: "number", Optional: true, Description: "Zero-based UTF-16 offset; default 0. Continue with returned nextOffset."},
				"maxChars":          {Type: "number", Optional: true, Description: "Maximum excerpt length in UTF-16 units, at least 2; default and hard cap 6000."},
				"query":             {Type: "string", Optional: true, Description: "Optional case-sensitive literal search, 1-200 UTF-16 units. Returns an excerpt starting at the next match at/after offset; matchOffset=null means no match. Keep query when continuing search."},
				"expectedUpdatedAt": {Type: "number", Optional: true, Description: "Pass note.updatedAt from the first read on subsequent reads to detect changes; restart if it changed."},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool, ok := ByName(test.name)
			if !ok {
				t.Fatalf("tool %q is missing", test.name)
			}
			if test.description != "" && tool.Description != test.description {
				t.Fatalf("description = %q, want %q", tool.Description, test.description)
			}
			if !reflect.DeepEqual(tool.Input, test.fields) {
				t.Fatalf("input = %+v, want %+v", tool.Input, test.fields)
			}
		})
	}

	tool, ok := ByRPCMethod("public/vault/notes/get")
	if !ok || tool.Name != "vault_notes_get" {
		t.Fatalf("public/vault/notes/get -> %q, %v", tool.Name, ok)
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
