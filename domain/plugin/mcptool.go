package plugin

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MCP tool contributions are declared statically in the manifest. The runtime
// may enable or disable a declared tool, but it can never create one: the host
// knows every tool name before the guest starts, so install consent, the
// client-visible tool list, and the audit trail all describe the same closed
// set.

// MaxMCPToolNameBytes is the cap for the full client-visible tool name,
// including the "plugin.<plugin-id>." prefix.
const MaxMCPToolNameBytes = 64

// MaxMCPToolDescriptionBytes bounds one tool description. Descriptions are
// model-visible text, so their size is bounded and hidden control characters
// are stripped before the list is served.
const MaxMCPToolDescriptionBytes = 4096

// MaxMCPToolTitleBytes bounds one tool title.
const MaxMCPToolTitleBytes = 200

// MaxMCPToolSchemaProperties bounds one structural schema.
const MaxMCPToolSchemaProperties = 64

// MaxMCPToolSchemaEnum bounds one enum declaration.
const MaxMCPToolSchemaEnum = 64

// MCPToolAccess is the tool's access class. Reads are allowed in every MCP
// permission mode; writes are denied in observer, prompted in confirm, and
// allowed in auto.
type MCPToolAccess string

const (
	// MCPToolRead marks a read-only tool.
	MCPToolRead MCPToolAccess = "read"
	// MCPToolWrite marks a tool that changes host or plugin state.
	MCPToolWrite MCPToolAccess = "write"
)

// Valid reports whether a is a declared access class.
func (a MCPToolAccess) Valid() bool {
	return a == MCPToolRead || a == MCPToolWrite
}

// MCPToolTargetKind declares which concrete target a call must resolve before
// the existing scope checks apply. The host validates the argument names and
// reuses the external MCP session scope for session and host targets.
type MCPToolTargetKind string

const (
	// MCPTargetNone is governed by mode and tool policy only.
	MCPTargetNone MCPToolTargetKind = "none"
	// MCPTargetSession requires a sessionId inside the effective scope.
	MCPTargetSession MCPToolTargetKind = "session"
	// MCPTargetHost requires a hostId with at least one scoped live Session.
	MCPTargetHost MCPToolTargetKind = "host"
	// MCPTargetProfile requires a profileId. Profiles have no allowlist; the
	// mode and tool policy still govern the call.
	MCPTargetProfile MCPToolTargetKind = "profile"
)

// Valid reports whether t is a declared target kind.
func (t MCPToolTargetKind) Valid() bool {
	switch t {
	case MCPTargetNone, MCPTargetSession, MCPTargetHost, MCPTargetProfile:
		return true
	}
	return false
}

// MCPTimeoutClass selects the per-call deadline. The standard class is the
// existing 60 s default; plugins cannot request an unbounded call.
type MCPTimeoutClass string

const (
	// MCPTimeoutShort is for cheap, local tools.
	MCPTimeoutShort MCPTimeoutClass = "short"
	// MCPTimeoutStandard is the existing 60 s external MCP tool timeout. It is
	// also the hard host cap: a tool class can only shorten it.
	MCPTimeoutStandard MCPTimeoutClass = "standard"
)

// Per-class durations. The standard class matches the existing external MCP
// default so a plugin tool is never more permissive than a built-in tool, and
// the host never lets a plugin tool exceed it.
const (
	MCPShortToolTimeout    = 5 * time.Second
	MCPStandardToolTimeout = 60 * time.Second
)

// Valid reports whether c is a declared timeout class.
func (c MCPTimeoutClass) Valid() bool {
	switch c {
	case MCPTimeoutShort, MCPTimeoutStandard:
		return true
	}
	return false
}

// Duration returns the class deadline. An omitted class uses the standard
// 60 s value.
func (c MCPTimeoutClass) Duration() time.Duration {
	if c == MCPTimeoutShort {
		return MCPShortToolTimeout
	}
	return MCPStandardToolTimeout
}

// MCPTool is one statically declared tool contribution. Name is local to the
// plugin; the client-visible name is QualifiedMCPToolName(pluginID, Name).
type MCPTool struct {
	Name         string            `toml:"name"`
	Title        string            `toml:"title"`
	Description  string            `toml:"description"`
	Access       MCPToolAccess     `toml:"access"`
	Target       MCPToolTargetKind `toml:"target"`
	Timeout      MCPTimeoutClass   `toml:"timeout"`
	InputSchema  *MCPToolSchema    `toml:"input_schema"`
	OutputSchema *MCPToolSchema    `toml:"output_schema"`
}

// MCPToolSchema is the structural TOML schema a manifest uses for one tool
// input or output. It renders as a closed JSON Schema object. Only scalar
// properties are supported; nested objects belong in the tool's arguments, not
// in the manifest schema language.
type MCPToolSchema struct {
	Properties []MCPToolProperty `toml:"properties"`
	Required   []string          `toml:"required"`
}

// MCPToolProperty is one named schema property.
type MCPToolProperty struct {
	Name        string   `toml:"name"`
	Type        string   `toml:"type"`
	Description string   `toml:"description"`
	Enum        []string `toml:"enum"`
}

// MCP property types. The set is closed so the rendered JSON Schema is
// predictable for every MCP client.
const (
	MCPPropertyString  = "string"
	MCPPropertyInteger = "integer"
	MCPPropertyNumber  = "number"
	MCPPropertyBoolean = "boolean"
)

var (
	mcpToolNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	mcpToolNameErrorText  = "must be lowercase letters, digits, dot, underscore, or hyphen, at most 64 characters"
	mcpToolSchemaTypes    = map[string]bool{MCPPropertyString: true, MCPPropertyInteger: true, MCPPropertyNumber: true, MCPPropertyBoolean: true}
	mcpToolFullNamePrefix = "plugin."
)

// QualifiedMCPToolName returns the client-visible name for a manifest-local
// tool id. The prefix keeps plugin tools disjoint from the built-in catalog
// and from every other plugin.
func QualifiedMCPToolName(pluginID, localName string) string {
	return mcpToolFullNamePrefix + pluginID + "." + localName
}

// ParseQualifiedMCPToolName splits a client-visible plugin tool name into its
// plugin id and local tool name. It reports false for a name outside the
// plugin namespace or with the wrong shape.
func ParseQualifiedMCPToolName(name string) (pluginID, localName string, ok bool) {
	rest, found := strings.CutPrefix(name, mcpToolFullNamePrefix)
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	pluginID = parts[0] + "." + parts[1]
	if !pluginIDPattern.MatchString(pluginID) || strings.Count(pluginID, ".") != 1 {
		return "", "", false
	}
	localName = parts[2]
	if !mcpToolNamePattern.MatchString(localName) {
		return "", "", false
	}
	return pluginID, localName, true
}

// Validate checks one declared tool. The pluginID supplies the qualified-name
// prefix and the locale-independent ownership check.
func (t MCPTool) Validate(pluginID string, index int) error {
	field := fmt.Sprintf("contributions.mcp_tools[%d]", index)
	if !mcpToolNamePattern.MatchString(t.Name) {
		return invalid(field+".name", "%q %s", t.Name, mcpToolNameErrorText)
	}
	full := QualifiedMCPToolName(pluginID, t.Name)
	if len(full) > MaxMCPToolNameBytes {
		return invalid(field+".name",
			"qualified name %q is %d bytes; the limit is %d", full, len(full), MaxMCPToolNameBytes)
	}
	if _, _, ok := ParseQualifiedMCPToolName(full); !ok {
		return invalid(field+".name", "%q is not a valid qualified tool name", full)
	}
	if err := validateMCPDisplayText(field+".title", t.Title, MaxMCPToolTitleBytes); err != nil {
		return err
	}
	if err := validateMCPDisplayText(field+".description", t.Description, MaxMCPToolDescriptionBytes); err != nil {
		return err
	}
	if !t.Access.Valid() {
		return invalid(field+".access", "%q is not one of read, write", t.Access)
	}
	target := t.Target
	if target == "" {
		target = MCPTargetNone
	}
	if !target.Valid() {
		return invalid(field+".target", "%q is not one of none, session, host, profile", t.Target)
	}
	if t.Timeout != "" && !t.Timeout.Valid() {
		return invalid(field+".timeout", "%q is not one of short, standard", t.Timeout)
	}
	if t.InputSchema != nil {
		if err := t.InputSchema.validate(field + ".input_schema"); err != nil {
			return err
		}
	}
	if t.OutputSchema != nil {
		if err := t.OutputSchema.validate(field + ".output_schema"); err != nil {
			return err
		}
	}
	return nil
}

// validateMCPDisplayText requires visible text and rejects hidden control
// characters. Newline and tab are allowed so a description can be formatted.
func validateMCPDisplayText(field, value string, limit int) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return invalid(field, "is required")
	}
	if len(value) > limit {
		return invalid(field, "must be at most %d bytes", limit)
	}
	for _, character := range value {
		if character == '\n' || character == '\t' {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return invalid(field, "must not contain control characters")
		}
	}
	return nil
}

// SanitizeMCPDisplayText bounds and strips hidden control characters from a
// description or title before it is served to an MCP client.
func SanitizeMCPDisplayText(value string, limit int) string {
	var builder strings.Builder
	for _, character := range value {
		if builder.Len() >= limit {
			break
		}
		if character != '\n' && character != '\t' && (character < 0x20 || character == 0x7f) {
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func (s MCPToolSchema) validate(field string) error {
	if len(s.Properties) > MaxMCPToolSchemaProperties {
		return invalid(field+".properties", "must declare at most %d properties", MaxMCPToolSchemaProperties)
	}
	seen := make(map[string]bool, len(s.Properties))
	for i, property := range s.Properties {
		where := fmt.Sprintf("%s.properties[%d]", field, i)
		if !mcpToolNamePattern.MatchString(property.Name) {
			return invalid(where+".name", "%q %s", property.Name, mcpToolNameErrorText)
		}
		if seen[property.Name] {
			return invalid(where+".name", "duplicate property %q", property.Name)
		}
		seen[property.Name] = true
		if !mcpToolSchemaTypes[property.Type] {
			return invalid(where+".type", "%q is not one of string, integer, number, boolean", property.Type)
		}
		if len(property.Description) > MaxMCPToolDescriptionBytes {
			return invalid(where+".description", "must be at most %d bytes", MaxMCPToolDescriptionBytes)
		}
		if len(property.Enum) > 0 {
			if property.Type != MCPPropertyString {
				return invalid(where+".enum", "enum is only valid for string properties")
			}
			if len(property.Enum) > MaxMCPToolSchemaEnum {
				return invalid(where+".enum", "must declare at most %d values", MaxMCPToolSchemaEnum)
			}
			values := make(map[string]bool, len(property.Enum))
			for j, value := range property.Enum {
				if value == "" {
					return invalid(fmt.Sprintf("%s.enum[%d]", where, j), "must not be empty")
				}
				if values[value] {
					return invalid(fmt.Sprintf("%s.enum[%d]", where, j), "duplicate value %q", value)
				}
				values[value] = true
			}
		}
	}
	required := make(map[string]bool, len(s.Required))
	for i, name := range s.Required {
		where := fmt.Sprintf("%s.required[%d]", field, i)
		if !seen[name] {
			return invalid(where, "%q is not a declared property", name)
		}
		if required[name] {
			return invalid(where, "duplicate required property %q", name)
		}
		required[name] = true
	}
	return nil
}

// JSONSchema renders the structural schema as the closed JSON Schema object an
// MCP client expects. Property order in the required list follows the manifest
// declaration order, and the properties map itself is a JSON object.
func (s MCPToolSchema) JSONSchema() map[string]any {
	properties := make(map[string]any, len(s.Properties))
	for _, property := range s.Properties {
		entry := map[string]any{"type": property.Type}
		if property.Description != "" {
			entry["description"] = SanitizeMCPDisplayText(property.Description, MaxMCPToolDescriptionBytes)
		}
		if len(property.Enum) > 0 {
			entry["enum"] = append([]string(nil), property.Enum...)
		}
		properties[property.Name] = entry
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(s.Required) > 0 {
		schema["required"] = append([]string(nil), s.Required...)
	}
	return schema
}

// EmptyMCPToolSchema is the schema used when a manifest omits input_schema.
func EmptyMCPToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

// InputJSONSchema returns the declared input schema or the empty closed
// object.
func (t MCPTool) InputJSONSchema() map[string]any {
	if t.InputSchema == nil {
		return EmptyMCPToolSchema()
	}
	return t.InputSchema.JSONSchema()
}

// OutputJSONSchema returns the optional output schema, or nil when the tool
// does not declare one.
func (t MCPTool) OutputJSONSchema() map[string]any {
	if t.OutputSchema == nil {
		return nil
	}
	return t.OutputSchema.JSONSchema()
}
