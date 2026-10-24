// Package catalog is the external MCP tool catalog extracted from the
// original Electron implementation at 73d15da7.
//
// The original derived its catalog from electron/capabilities/catalog/*.cjs
// (tool names, descriptions, policies, and input shapes) and
// electron/mcp/progressiveMcpTools.cjs (the 14 initial core tools and seven
// deferred toolsets). catalog.json is that data verbatim; the Go code adds
// only the derived group, surface, and approval flag. The package is pure
// data: it imports no application or transport package, so the stdio process
// and the app can share it without crossing the process boundary.
//
// Regeneration (see TestCatalogMatchesTheExtractedOriginalSnapshot):
//
//	git archive 73d15da7 electron/capabilities | tar -x -C <empty dir>
//	node -e 'const {listMcpTools}=require("<dir>/electron/capabilities/codegen/toolSurfaces.cjs");
//	          process.stdout.write(JSON.stringify({count:listMcpTools().length,tools:listMcpTools()},null,2))'
package catalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

//go:embed catalog.json
var catalogFS embed.FS

// Policy is one catalog entry's security classification. It is copied
// verbatim from the original capability definition.
type Policy struct {
	Write                 bool `json:"write"`
	SensitiveRead         bool `json:"sensitiveRead"`
	LongRunning           bool `json:"longRunning"`
	RequiresChatSession   bool `json:"requiresChatSession"`
	BypassesObserverBlock bool `json:"bypassesObserverBlock"`
	BypassesApproval      bool `json:"bypassesApproval"`
	BypassesChatCancel    bool `json:"bypassesChatCancel"`
}

// InputField is one tool argument. The original schemas support only strings
// and non-negative integers.
type InputField struct {
	Type        string   `json:"type"`
	Optional    bool     `json:"optional"`
	Description string   `json:"description"`
	Enum        []string `json:"-"`
}

// Tool is one catalog entry.
type Tool struct {
	Name            string                `json:"name"`
	CapabilityID    string                `json:"capabilityId"`
	Domain          string                `json:"domain"`
	Group           string                `json:"group"`
	RPCMethod       string                `json:"rpcMethod"`
	PublicRPCMethod string                `json:"publicRpcMethod"`
	Surface         string                `json:"surface"`
	Description     string                `json:"description"`
	Policy          Policy                `json:"policy"`
	Input           map[string]InputField `json:"input"`

	inputOrder []string
}

// RequiresApproval reports the original confirm requirement: every write needs
// approval except the two lifecycle tools the catalog marks as bypassing it.
func (t Tool) RequiresApproval() bool {
	return t.Policy.Write && !t.Policy.BypassesApproval
}

// InputSchema renders the original input shape as a JSON Schema object. The
// MCP SDK's zod envelope adds "$schema"; the properties, required list, and
// additionalProperties flag are the same.
func (t Tool) InputSchema() map[string]any {
	return inputSchema(t.Input, t.inputOrder)
}

// inputSchema builds the JSON Schema for a shape. order preserves the
// original declaration order for the required list.
func inputSchema(shape map[string]InputField, order []string) map[string]any {
	properties := make(map[string]any, len(shape))
	required := make([]string, 0, len(shape))
	for _, name := range order {
		field, ok := shape[name]
		if !ok {
			continue
		}
		properties[name] = schemaProperty(field)
		if !field.Optional {
			required = append(required, name)
		}
	}
	// A shape decoded without an order still renders every field.
	if len(properties) != len(shape) {
		names := make([]string, 0, len(shape))
		for name := range shape {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			field := shape[name]
			properties[name] = schemaProperty(field)
			if !field.Optional {
				required = append(required, name)
			}
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func schemaProperty(field InputField) map[string]any {
	property := map[string]any{
		"type":        field.Type,
		"description": field.Description,
	}
	if len(field.Enum) > 0 {
		property["enum"] = field.Enum
	}
	return property
}

type rawEntry struct {
	CapabilityID    string                `json:"capabilityId"`
	Domain          string                `json:"domain"`
	MCPTool         string                `json:"mcpTool"`
	RPCMethod       string                `json:"rpcMethod"`
	PublicRPCMethod string                `json:"publicRpcMethod"`
	Description     string                `json:"description"`
	Policy          Policy                `json:"policy"`
	InputShape      map[string]InputField `json:"inputShape"`
}

type rawCatalog struct {
	Count int        `json:"count"`
	Tools []rawEntry `json:"tools"`
}

type catalogData struct {
	tools  []Tool
	byName map[string]Tool
	byRPC  map[string]Tool
}

// Load parses and validates the embedded catalog. It is exported so tests and
// the parity generator can prove the data independently; production code uses
// All, ByName, and ByRPCMethod.
func Load() ([]Tool, error) {
	raw, err := catalogFS.ReadFile("catalog.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: read embedded data: %w", err)
	}
	data, err := parse(raw)
	if err != nil {
		return nil, err
	}
	return append([]Tool(nil), data.tools...), nil
}

func parse(raw []byte) (*catalogData, error) {
	var decoded rawCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("catalog: decode embedded data: %w", err)
	}
	if decoded.Count != len(decoded.Tools) {
		return nil, fmt.Errorf("catalog: count %d does not match %d entries",
			decoded.Count, len(decoded.Tools))
	}
	if len(decoded.Tools) != 69 {
		return nil, fmt.Errorf("catalog: %d tools, want the original 69", len(decoded.Tools))
	}

	groupByName := map[string]string{}
	for _, name := range CoreNames {
		groupByName[name] = "core"
	}
	for _, toolset := range ToolsetNames {
		for _, name := range Toolsets[toolset] {
			groupByName[name] = toolset
		}
	}

	data := &catalogData{
		tools:  make([]Tool, 0, len(decoded.Tools)),
		byName: map[string]Tool{},
		byRPC:  map[string]Tool{},
	}
	for _, entry := range decoded.Tools {
		group, ok := groupByName[entry.MCPTool]
		if !ok {
			return nil, fmt.Errorf("catalog: tool %q is not assigned to core or a toolset", entry.MCPTool)
		}
		tool := Tool{
			Name:            entry.MCPTool,
			CapabilityID:    entry.CapabilityID,
			Domain:          entry.Domain,
			Group:           group,
			RPCMethod:       entry.RPCMethod,
			PublicRPCMethod: entry.PublicRPCMethod,
			Surface:         "public",
			Description:     entry.Description,
			Policy:          entry.Policy,
			Input:           entry.InputShape,
			inputOrder:      orderForTool(raw, entry.MCPTool),
		}
		if _, exists := data.byName[tool.Name]; exists {
			return nil, fmt.Errorf("catalog: duplicate tool name %q", tool.Name)
		}
		if _, exists := data.byRPC[tool.RPCMethod]; exists {
			return nil, fmt.Errorf("catalog: duplicate rpc method %q", tool.RPCMethod)
		}
		data.byName[tool.Name] = tool
		data.byRPC[tool.RPCMethod] = tool
		data.tools = append(data.tools, tool)
	}
	return data, nil
}

// orderForTool decodes just enough of each tool object to recover its
// inputShape key order, so the rendered required list keeps the original
// declaration order.
func orderForTool(raw []byte, toolName string) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Position at the "tools" array.
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		if key, ok := token.(string); ok && key == "tools" {
			break
		}
	}
	if _, err := decoder.Token(); err != nil { // '['
		return nil
	}
	for decoder.More() {
		var entry map[string]json.RawMessage
		if err := decoder.Decode(&entry); err != nil {
			return nil
		}
		var name string
		if err := json.Unmarshal(entry["mcpTool"], &name); err != nil || name != toolName {
			continue
		}
		var shape json.RawMessage
		if rawShape, ok := entry["inputShape"]; ok {
			shape = rawShape
		}
		return objectKeyOrder(shape)
	}
	return nil
}

// objectKeyOrder returns the keys of a JSON object in document order.
func objectKeyOrder(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil
	}
	var order []string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return order
		}
		name, _ := key.(string)
		order = append(order, name)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return order
		}
	}
	return order
}

var (
	defaultOnce sync.Once
	defaultData *catalogData
	defaultErr  error
)

// data parses the embedded catalog once. It is lazy because parse reads the
// progressive split declared in the sibling file; package variable
// initialization order cannot be relied on for that dependency.
func data() *catalogData {
	defaultOnce.Do(func() {
		raw, err := catalogFS.ReadFile("catalog.json")
		if err != nil {
			defaultErr = fmt.Errorf("catalog: read embedded data: %w", err)
			return
		}
		defaultData, defaultErr = parse(raw)
	})
	if defaultErr != nil {
		panic(defaultErr)
	}
	return defaultData
}

// All returns every catalog tool in the original capability order.
func All() []Tool {
	return append([]Tool(nil), data().tools...)
}

// ByName returns the tool with the MCP name.
func ByName(name string) (Tool, bool) {
	tool, ok := data().byName[name]
	return tool, ok
}

// ByRPCMethod returns the tool with the app-side RPC method.
func ByRPCMethod(method string) (Tool, bool) {
	tool, ok := data().byRPC[method]
	return tool, ok
}
