package plugin

import (
	"strings"
	"testing"
	"time"
)

func validMCPTool() MCPTool {
	return MCPTool{
		Name:        "echo",
		Title:       "Echo",
		Description: "Echoes text.",
		Access:      MCPToolRead,
		Target:      MCPTargetNone,
		Timeout:     MCPTimeoutShort,
		InputSchema: &MCPToolSchema{
			Properties: []MCPToolProperty{
				{Name: "text", Type: MCPPropertyString, Description: "Text."},
				{Name: "count", Type: MCPPropertyInteger},
			},
			Required: []string{"text"},
		},
	}
}

func manifestWithTools(tools ...MCPTool) Manifest {
	manifest := validManifest()
	manifest.Permissions.Required = append(manifest.Permissions.Required, string(PermissionMCPTools))
	manifest.Contributions.MCPTools = tools
	return manifest
}

func TestMCPToolsRequireThePermission(t *testing.T) {
	manifest := validManifest()
	manifest.Contributions.MCPTools = []MCPTool{validMCPTool()}
	if err := manifest.Validate(); err == nil ||
		!strings.Contains(err.Error(), string(PermissionMCPTools)) {
		t.Fatalf("Validate without %s = %v", PermissionMCPTools, err)
	}

	manifest.Permissions.Optional = append(manifest.Permissions.Optional, string(PermissionMCPTools))
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate with optional %s: %v", PermissionMCPTools, err)
	}
}

func TestMCPToolDeclarationsAreClosedAndUnique(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*MCPTool)
		field  string
	}{
		{"malformed name", func(tool *MCPTool) { tool.Name = "Echo Tool" }, "name"},
		{"missing title", func(tool *MCPTool) { tool.Title = " " }, "title"},
		{"control character", func(tool *MCPTool) { tool.Description = "echo\x00text" }, "description"},
		{"bad access", func(tool *MCPTool) { tool.Access = "admin" }, "access"},
		{"bad target", func(tool *MCPTool) { tool.Target = "window" }, "target"},
		{"bad timeout", func(tool *MCPTool) { tool.Timeout = "forever" }, "timeout"},
		{"unknown required property", func(tool *MCPTool) {
			tool.InputSchema.Required = []string{"missing"}
		}, "required"},
		{"bad property type", func(tool *MCPTool) {
			tool.InputSchema.Properties[0].Type = "object"
		}, "type"},
		{"enum on non-string", func(tool *MCPTool) {
			tool.InputSchema.Properties[1].Enum = []string{"one"}
		}, "enum"},
		{"duplicate property", func(tool *MCPTool) {
			tool.InputSchema.Properties = append(tool.InputSchema.Properties,
				MCPToolProperty{Name: "text", Type: MCPPropertyString})
		}, "duplicate property"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tool := validMCPTool()
			test.mutate(&tool)
			err := manifestWithTools(tool).Validate()
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("Validate = %v, want a %q error", err, test.field)
			}
		})
	}

	duplicate := validMCPTool()
	if err := manifestWithTools(validMCPTool(), duplicate).Validate(); err == nil ||
		!strings.Contains(err.Error(), "duplicate tool") {
		t.Fatalf("duplicate tool Validate = %v", err)
	}
}

func TestMCPToolQualifiedNamesStayBounded(t *testing.T) {
	tool := validMCPTool()
	tool.Name = strings.Repeat("a", MaxMCPToolNameBytes)
	err := manifestWithTools(tool).Validate()
	if err == nil || !strings.Contains(err.Error(), "qualified name") {
		t.Fatalf("long name Validate = %v", err)
	}

	qualified := QualifiedMCPToolName("acme.hello", "echo")
	if qualified != "plugin.acme.hello.echo" {
		t.Fatalf("qualified name = %q", qualified)
	}
	pluginID, local, ok := ParseQualifiedMCPToolName(qualified)
	if !ok || pluginID != "acme.hello" || local != "echo" {
		t.Fatalf("parse = %q, %q, %v", pluginID, local, ok)
	}
	for _, rejected := range []string{
		"echo",
		"plugin.acme.echo",
		"plugin.acme.hello.",
		"plugin.acme.hello.Echo",
		"plugin..hello.echo",
	} {
		if _, _, ok := ParseQualifiedMCPToolName(rejected); ok {
			t.Fatalf("ParseQualifiedMCPToolName(%q) accepted", rejected)
		}
	}
}

func TestMCPTimeoutClassesStayWithinTheHostBound(t *testing.T) {
	cases := map[MCPTimeoutClass]time.Duration{
		MCPTimeoutShort:    5 * time.Second,
		MCPTimeoutStandard: 60 * time.Second,
		"":                 60 * time.Second,
	}
	for class, want := range cases {
		if got := class.Duration(); got != want {
			t.Fatalf("%q.Duration() = %s, want %s", class, got, want)
		}
		if got := class.Duration(); got > MCPStandardToolTimeout {
			t.Fatalf("%q exceeds the existing %s MCP timeout", class, MCPStandardToolTimeout)
		}
	}
	if MCPTimeoutClass("long").Valid() {
		t.Fatal("an undeclared timeout class is valid")
	}
}

func TestMCPToolSchemaRendersClosedJSONSchema(t *testing.T) {
	schema := validMCPTool().InputSchema.JSONSchema()
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema = %#v", schema)
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "text" {
		t.Fatalf("required = %#v", schema["required"])
	}
	properties, _ := schema["properties"].(map[string]any)
	text, _ := properties["text"].(map[string]any)
	if text["type"] != MCPPropertyString || text["description"] != "Text." {
		t.Fatalf("text property = %#v", text)
	}
	empty := EmptyMCPToolSchema()
	if empty["type"] != "object" || empty["additionalProperties"] != false {
		t.Fatalf("empty schema = %#v", empty)
	}
}

func TestSanitizeMCPDisplayTextStripsControlBytesAndBounds(t *testing.T) {
	got := SanitizeMCPDisplayText("a\x00b\nc\td\x7f", 64)
	if got != "ab\nc\td" {
		t.Fatalf("sanitized = %q", got)
	}
	if got := SanitizeMCPDisplayText(strings.Repeat("x", 32), 8); len(got) != 8 {
		t.Fatalf("bounded text = %q", got)
	}
}
