// Plugin-contributed external MCP tools. This file is the app's one tool
// registry: the manifest's static declarations are published here when a fiber
// becomes ACTIVE, the policy handler lists and invokes them through it, and the
// plugin-side gate (installed, enabled, ACTIVE, mcp.tools granted) is checked
// before and after the external MCP permission gate.

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// mcpPluginTool is one registered tool. Registration order is manifest
// declaration order per plugin, and the slice preserves it across plugins.
type mcpPluginTool struct {
	ref  appplugin.PluginRef
	tool domainplugin.MCPTool
	name string
	call appplugin.ToolCaller
}

// mcpPluginTools is the single registry of plugin-contributed MCP tools. It is
// both the container's MCPToolRegistrar and the policy handler's catalog.
type mcpPluginTools struct {
	manager func() *appplugin.Manager

	mu      sync.RWMutex
	entries []*mcpPluginTool
	byName  map[string]*mcpPluginTool
}

// newMCPPluginTools returns an empty registry. The manager resolver is lazy so
// the registrar can be passed to NewManager before the manager variable is
// assigned.
func newMCPPluginTools(manager func() *appplugin.Manager) *mcpPluginTools {
	return &mcpPluginTools{
		manager: manager,
		byName:  make(map[string]*mcpPluginTool),
	}
}

// RegisterMCPTool implements appplugin.MCPToolRegistrar. A duplicate full name
// is rejected: the manifest already rejects duplicates, so a collision here
// means the same fiber tried to publish twice.
func (r *mcpPluginTools) RegisterMCPTool(ref appplugin.PluginRef,
	tool domainplugin.MCPTool, call appplugin.ToolCaller) (appplugin.Disposer, error) {
	if r == nil {
		return nil, errors.New("external MCP: plugin tool registry is not configured")
	}
	name := domainplugin.QualifiedMCPToolName(ref.ID, tool.Name)
	if call == nil {
		return nil, fmt.Errorf("external MCP: tool %q has no caller", name)
	}
	entry := &mcpPluginTool{ref: ref, tool: tool, name: name, call: call}
	r.mu.Lock()
	if _, exists := r.byName[name]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("external MCP: tool %q is already registered", name)
	}
	r.byName[name] = entry
	r.entries = append(r.entries, entry)
	r.mu.Unlock()

	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			delete(r.byName, name)
			for i, candidate := range r.entries {
				if candidate == entry {
					r.entries = append(r.entries[:i], r.entries[i+1:]...)
					break
				}
			}
			r.mu.Unlock()
		})
		return nil
	}, nil
}

// snapshot returns the current registration order.
func (r *mcpPluginTools) snapshot() []*mcpPluginTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*mcpPluginTool(nil), r.entries...)
}

// List returns the client-visible tools: entries whose fiber is ACTIVE and
// whose mcp.tools grant is valid. Ordering is registration (manifest
// declaration) order.
func (r *mcpPluginTools) List() []any {
	entries := r.snapshot()
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		if r.pluginGate(entry) != nil {
			continue
		}
		out = append(out, entry.listEntry())
	}
	return out
}

// Descriptor resolves one name and applies the plugin-side gate. A missing
// name is classified from the install record so a reload returns a retryable
// error instead of a permanent not-found.
func (r *mcpPluginTools) Descriptor(ctx context.Context, name string) (*mcpPluginTool, *appmcp.Error) {
	r.mu.RLock()
	entry := r.byName[name]
	r.mu.RUnlock()
	if entry == nil {
		return nil, r.missingError(ctx, name)
	}
	if err := r.pluginGate(entry); err != nil {
		return nil, withPluginTool(err, name)
	}
	return entry, nil
}

// pluginGate checks installed+enabled+ACTIVE and the mcp.tools grant. The
// manager owns enabled/installed state; the fiber owns ACTIVE; the authority
// owns the grant.
func (r *mcpPluginTools) pluginGate(entry *mcpPluginTool) *appmcp.Error {
	manager := r.manager()
	if manager == nil {
		return pluginToolUnavailable(entry.name, "Plugin manager is not available.")
	}
	fiber, ok := manager.Container().FiberByRef(entry.ref)
	if !ok || fiber.State() != domainplugin.StateActive {
		return &appmcp.Error{
			Code:      appmcp.CodePluginToolReloading,
			Tool:      entry.name,
			Message:   fmt.Sprintf("Plugin %q is not active; retry shortly.", entry.ref.ID),
			Retryable: true,
		}
	}
	if err := manager.Authority().Check(appplugin.SubjectForRef(entry.ref),
		domainplugin.PermissionMCPTools, domainplugin.Scope{}); err != nil {
		return &appmcp.Error{
			Code:    appmcp.CodePluginToolDenied,
			Tool:    entry.name,
			Message: fmt.Sprintf("Plugin %q has not been granted the %q permission.", entry.ref.ID, domainplugin.PermissionMCPTools),
		}
	}
	return nil
}

// missingError classifies a name the registry does not know. A declared tool
// of an enabled plugin that is not ACTIVE is a reload; anything else is
// unavailable.
func (r *mcpPluginTools) missingError(ctx context.Context, name string) *appmcp.Error {
	pluginID, _, ok := domainplugin.ParseQualifiedMCPToolName(name)
	if !ok {
		return pluginToolUnavailable(name, "Unknown plugin tool.")
	}
	manager := r.manager()
	if manager == nil {
		return pluginToolUnavailable(name, "Plugin manager is not available.")
	}
	detail, found, err := manager.Detail(ctx, pluginID)
	if err != nil {
		return pluginToolUnavailable(name, "Plugin state is not available.")
	}
	if !found {
		return pluginToolUnavailable(name, fmt.Sprintf("Plugin %q is not installed.", pluginID))
	}
	if !detail.Enabled {
		return pluginToolUnavailable(name, fmt.Sprintf("Plugin %q is not enabled.", pluginID))
	}
	if detail.State != domainplugin.StateActive {
		return &appmcp.Error{
			Code:      appmcp.CodePluginToolReloading,
			Tool:      name,
			Message:   fmt.Sprintf("Plugin %q is reloading; retry shortly.", pluginID),
			Retryable: true,
		}
	}
	return pluginToolUnavailable(name, fmt.Sprintf("Plugin %q does not expose tool %q.", pluginID, name))
}

// Invoke runs one already-authorized call and normalizes the guest payload.
// The plugin-side gate is re-checked so a revocation that raced an approval
// cannot run a call.
func (r *mcpPluginTools) Invoke(ctx context.Context, entry *mcpPluginTool,
	arguments map[string]any, progress func(progress uint32, message string)) (appmcp.PluginCallResult, *appmcp.Error) {
	if err := r.pluginGate(entry); err != nil {
		return appmcp.PluginCallResult{}, withPluginTool(err, entry.name)
	}
	payload, err := entry.call.CallTool(ctx, entry.name, arguments, entry.tool.Timeout.Duration(), progress)
	if err != nil {
		return appmcp.PluginCallResult{}, pluginCallError(entry.name, err)
	}
	result, normalizeErr := appmcp.NormalizePluginToolResult(payload)
	if normalizeErr != nil {
		var typed *appmcp.Error
		if errors.As(normalizeErr, &typed) {
			return appmcp.PluginCallResult{}, withPluginTool(typed, entry.name)
		}
		return appmcp.PluginCallResult{}, &appmcp.Error{
			Code:    appmcp.CodePluginToolInvalidResult,
			Tool:    entry.name,
			Message: "Plugin returned an invalid tool result.",
		}
	}
	return result, nil
}

// RecordAudit appends one tool-call row. It never carries payload content.
func (r *mcpPluginTools) RecordAudit(event appplugin.MCPToolAudit) {
	if r == nil {
		return
	}
	if manager := r.manager(); manager != nil {
		manager.RecordMCPToolCall(event)
	}
}

// listEntry renders the MCP tools/list entry. Descriptions are stripped of
// hidden characters and bounded; the annotations hint mirrors the access
// class so a client can avoid offering a write tool in a read-only context.
func (e *mcpPluginTool) listEntry() map[string]any {
	entry := map[string]any{
		"name":        e.name,
		"title":       domainplugin.SanitizeMCPDisplayText(e.tool.Title, domainplugin.MaxMCPToolTitleBytes),
		"description": domainplugin.SanitizeMCPDisplayText(e.tool.Description, domainplugin.MaxMCPToolDescriptionBytes),
		"inputSchema": e.tool.InputJSONSchema(),
		"annotations": map[string]any{
			"readOnlyHint": e.tool.Access == domainplugin.MCPToolRead,
		},
	}
	if output := e.tool.OutputJSONSchema(); output != nil {
		entry["outputSchema"] = output
	}
	return entry
}

// PluginID, Access, Target, and Timeout expose the descriptor to the policy
// handler.
func (e *mcpPluginTool) PluginID() string { return e.ref.ID }

func (e *mcpPluginTool) Access() domainplugin.MCPToolAccess { return e.tool.Access }

func (e *mcpPluginTool) TargetKind() domainplugin.MCPToolTargetKind {
	if e.tool.Target == "" {
		return domainplugin.MCPTargetNone
	}
	return e.tool.Target
}

func (e *mcpPluginTool) FullName() string { return e.name }

func pluginToolUnavailable(tool, message string) *appmcp.Error {
	return &appmcp.Error{
		Code:    appmcp.CodePluginToolUnavailable,
		Tool:    tool,
		Message: message,
	}
}

func withPluginTool(err *appmcp.Error, tool string) *appmcp.Error {
	if err == nil || err.Tool != "" {
		return err
	}
	copied := *err
	copied.Tool = tool
	return &copied
}

// pluginCallError maps a container or guest failure to a typed, retryable
// where retrying can succeed. Messages never include host paths or stacks.
func pluginCallError(tool string, err error) *appmcp.Error {
	var queueFull *appplugin.ToolQueueFullError
	var noResult *appplugin.ToolNoResultError
	var deadline *appplugin.DeadlineError
	var limit *appplugin.LimitError
	switch {
	case errors.Is(err, context.Canceled):
		return &appmcp.Error{
			Code: appmcp.CodePluginToolCancelled, Tool: tool,
			Message: "Plugin tool call was cancelled.", Retryable: true,
		}
	case errors.Is(err, context.DeadlineExceeded):
		return &appmcp.Error{
			Code: appmcp.CodePluginToolTimeout, Tool: tool,
			Message: "Plugin tool call timed out.", Retryable: true,
		}
	case errors.As(err, &deadline):
		return &appmcp.Error{
			Code: appmcp.CodePluginToolTimeout, Tool: tool,
			Message: "Plugin tool call timed out.", Retryable: true,
		}
	case errors.As(err, &queueFull):
		return &appmcp.Error{
			Code: appmcp.CodePluginToolBusy, Tool: tool,
			Message: "Plugin is busy; retry the tool call.", Retryable: true,
		}
	case errors.Is(err, appplugin.ErrNotActive):
		return &appmcp.Error{
			Code: appmcp.CodePluginToolReloading, Tool: tool,
			Message: "Plugin is reloading; retry the tool call.", Retryable: true,
		}
	case errors.As(err, &noResult):
		return &appmcp.Error{
			Code:    appmcp.CodePluginToolFailed,
			Tool:    tool,
			Message: "Plugin returned no result for the tool call.",
		}
	case errors.As(err, &limit):
		return &appmcp.Error{
			Code:    appmcp.CodePluginToolResultLimit,
			Tool:    tool,
			Message: "Plugin tool call exceeded a host limit.",
		}
	default:
		return &appmcp.Error{
			Code:    appmcp.CodePluginToolFailed,
			Tool:    tool,
			Message: "Plugin tool call failed: " + sanitizePluginErrorText(err.Error()),
		}
	}
}

// sanitizePluginErrorText strips control characters and bounds a diagnostic
// message before it reaches an MCP client.
func sanitizePluginErrorText(text string) string {
	text = strings.TrimSpace(text)
	var builder strings.Builder
	for _, character := range text {
		if builder.Len() >= 512 {
			break
		}
		if character < 0x20 || character == 0x7f {
			continue
		}
		builder.WriteRune(character)
	}
	if builder.Len() == 0 {
		return "unknown error"
	}
	return builder.String()
}
