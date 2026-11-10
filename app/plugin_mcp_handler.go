// Policy handler branches for plugin-contributed MCP tools. The registry and
// its plugin-side gate live in plugin_mcp.go; this file runs the external MCP
// gate (scope, mode, approval, target), forwards progress, records audit and
// activity, and renders the MCP result.

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

// pluginToolList returns the currently visible plugin tools. An app without a
// plugin manager (early startup, tests) simply has none.
func (h *mcpPolicyHandler) pluginToolList() map[string]any {
	tools := h.pluginTools()
	if tools == nil {
		return map[string]any{"ok": true, "tools": []any{}}
	}
	return map[string]any{"ok": true, "tools": tools.List()}
}

// pluginToolCall runs the two gates and returns either the normalized MCP
// result (ok:true plus content/structuredContent) or a typed failure envelope.
func (h *mcpPolicyHandler) pluginToolCall(ctx context.Context,
	arguments map[string]any, client string) map[string]any {
	name := strings.TrimSpace(stringParam(arguments, "name"))
	started := time.Now()
	progressToken := arguments["_progressToken"]
	delete(arguments, "_progressToken")
	if name == "" {
		return appmcp.Result(appmcp.InvalidArgumentError("plugin tool", "Tool name is required."))
	}
	toolArguments := map[string]any{}
	if raw, present := arguments["arguments"]; present && raw != nil {
		parsed, ok := raw.(map[string]any)
		if !ok {
			err := appmcp.InvalidArgumentError(name, "Tool arguments must be an object.")
			h.auditPluginTool(name, client, "", "", "", "invalid", started, 0)
			return appmcp.Result(err)
		}
		toolArguments = parsed
	}
	tools := h.pluginTools()
	if tools == nil {
		err := pluginToolUnavailable(name, "Plugin tools are not available in this build.")
		h.auditPluginTool(name, client, string(h.policy.CurrentMode()), "", "", "unavailable", started, 0)
		h.recordPluginActivity(name, err.Code, name)
		return appmcp.Result(err)
	}

	// First gate: declared, installed, enabled, ACTIVE, mcp.tools granted.
	entry, gateErr := tools.Descriptor(ctx, name)
	if gateErr != nil {
		h.auditPluginTool(name, client, string(h.policy.CurrentMode()), "",
			pluginAuditTarget(appmcp.Target{}, toolArguments),
			pluginAuditStatus(gateErr.Code), started, 0)
		h.recordPluginActivity(name, gateErr.Code, name)
		return appmcp.Result(gateErr)
	}
	if targetErr := validatePluginTarget(entry, toolArguments); targetErr != nil {
		h.auditPluginTool(name, client, string(h.policy.CurrentMode()),
			string(entry.Access()), pluginAuditTarget(appmcp.Target{}, toolArguments),
			"invalid", started, 0)
		h.recordPluginActivity(name, targetErr.Code, name)
		return appmcp.Result(targetErr)
	}

	// Second gate: the existing external MCP scope, mode, and approval.
	requested, err := appmcp.RequestedScopeFrom(toolArguments)
	if err != nil {
		h.auditPluginTool(name, client, string(h.policy.CurrentMode()),
			string(entry.Access()), pluginAuditTarget(appmcp.Target{}, toolArguments),
			"denied", started, 0)
		return mcpPolicyFailure(err, name)
	}
	target := h.describeTarget(ctx, name, toolArguments)
	decision, err := h.policy.Authorize(ctx, appmcp.Request{
		Tool: appmcp.Tool{
			Name:  name,
			Write: entry.Access() == domainplugin.MCPToolWrite,
		},
		Client:    client,
		Scope:     requested,
		Target:    target,
		Arguments: toolArguments,
	})
	if err != nil {
		h.auditPluginTool(name, client, string(h.policy.CurrentMode()),
			string(entry.Access()), pluginAuditTarget(target, toolArguments), "denied", started, 0)
		h.recordPluginActivity(name, errorCodeOf(err), target.Display)
		return mcpPolicyFailure(err, name)
	}
	if scopeErr := h.checkPluginHostScope(entry, toolArguments, decision.Sessions); scopeErr != nil {
		h.auditPluginTool(name, client, string(decision.Mode),
			string(entry.Access()), pluginAuditTarget(target, toolArguments), "denied", started, 0)
		h.recordPluginActivity(name, scopeErr.Code, target.Display)
		return appmcp.Result(scopeErr)
	}

	result, callErr := tools.Invoke(ctx, entry, toolArguments,
		h.pluginProgress(ctx, progressToken))
	if callErr != nil {
		h.auditPluginTool(name, client, string(decision.Mode), string(entry.Access()),
			pluginAuditTarget(target, toolArguments),
			pluginAuditStatus(callErr.Code), started, 0)
		h.recordPluginActivity(name, callErr.Code, target.Display)
		return appmcp.Result(callErr)
	}
	if result.IsError {
		message := result.GuestMessage
		if message == "" {
			message = "Plugin tool reported an error."
		}
		// A guest tool error is an MCP tool result, not a transport failure:
		// keep any content and structured content the guest returned and mark
		// the result isError. The typed host code travels beside the MCP body
		// so the app's activity and audit views still classify the outcome.
		response := result.MCPResult()
		response["ok"] = true
		response["tool"] = name
		response["code"] = string(appmcp.CodePluginToolFailed)
		response["error"] = message
		if result.GuestCode != "" {
			response["rule"] = result.GuestCode
		}
		if len(result.Content) == 0 {
			response["content"] = []any{map[string]any{
				"type": "text",
				"text": fmt.Sprintf("Error: %s (code: %s)",
					message, appmcp.CodePluginToolFailed),
			}}
		}
		h.auditPluginTool(name, client, string(decision.Mode), string(entry.Access()),
			pluginAuditTarget(target, toolArguments), "error", started, result.ResultBytes)
		h.recordPluginActivity(name, appmcp.CodePluginToolFailed, target.Display)
		return response
	}

	h.auditPluginTool(name, client, string(decision.Mode), string(entry.Access()),
		pluginAuditTarget(target, toolArguments), "ok", started, result.ResultBytes)
	h.policy.RecordActivity(appmcp.ActivityView{
		Tool:   name,
		Status: appmcp.ActivityOK,
		Detail: target.Display,
	})
	response := result.MCPResult()
	response["ok"] = true
	response["tool"] = name
	return response
}

// validatePluginTarget requires the concrete argument the target kind names.
// The value's authorization is the existing scope check, applied afterwards.
func validatePluginTarget(entry *mcpPluginTool, arguments map[string]any) *appmcp.Error {
	var required string
	switch entry.TargetKind() {
	case domainplugin.MCPTargetNone:
		return nil
	case domainplugin.MCPTargetSession:
		required = "sessionId"
	case domainplugin.MCPTargetHost:
		required = "hostId"
	case domainplugin.MCPTargetProfile:
		required = "profileId"
	default:
		return &appmcp.Error{
			Code:    appmcp.CodeInvalidArgument,
			Tool:    entry.FullName(),
			Message: "Tool declares an unsupported target kind.",
		}
	}
	if strings.TrimSpace(stringParam(arguments, required)) == "" {
		return &appmcp.Error{
			Code:    appmcp.CodeInvalidArgument,
			Tool:    entry.FullName(),
			Message: fmt.Sprintf("%s is required for this tool.", required),
		}
	}
	return nil
}

// checkPluginHostScope reuses the effective session scope for a host-targeted
// tool: the host must have at least one live Session inside the intersection.
func (h *mcpPolicyHandler) checkPluginHostScope(entry *mcpPluginTool,
	arguments map[string]any, sessions []string) *appmcp.Error {
	if entry.TargetKind() != domainplugin.MCPTargetHost {
		return nil
	}
	hostID := stringParam(arguments, "hostId")
	for _, description := range h.sessions.DescribeAll() {
		if string(description.HostID) == hostID &&
			appmcp.SessionAllowed(sessions, string(description.ID)) {
			return nil
		}
	}
	return &appmcp.Error{
		Code: appmcp.CodeSessionOutOfScope,
		Tool: entry.FullName(),
		Message: fmt.Sprintf(
			"Host %q has no live Session inside the external MCP scope.", hostID),
	}
}

// pluginProgress forwards guest progress to the MCP client when it supplied a
// progress token. Without a token, progress is dropped rather than invented.
func (h *mcpPolicyHandler) pluginProgress(ctx context.Context,
	token any) func(progress uint32, message string) {
	if token == nil {
		return nil
	}
	notifier, ok := mcprpc.NotifierFrom(ctx)
	if !ok {
		return nil
	}
	return func(progress uint32, message string) {
		params := map[string]any{"progressToken": token, "progress": progress}
		if message != "" {
			params["message"] = sanitizePluginErrorText(message)
		}
		_ = notifier.Notify(mcprpc.NotificationProgress, params)
	}
}

// pluginAuditTarget returns an identifier for the audit row. The audit
// contract stores names and counters only, so the caller's Session or Host
// label (the policy's display string) must never reach it.
func pluginAuditTarget(target appmcp.Target, arguments map[string]any) string {
	if target.SessionID != "" {
		return target.SessionID
	}
	if target.HostID != "" {
		return target.HostID
	}
	for _, key := range []string{"profileId", "hostId", "sessionId"} {
		if value := stringParam(arguments, key); value != "" {
			return value
		}
	}
	return ""
}

// auditPluginTool records one call outcome without any argument or result
// content.
func (h *mcpPolicyHandler) auditPluginTool(name, client, mode, access, target,
	status string, started time.Time, resultBytes int) {
	pluginID, _, _ := domainplugin.ParseQualifiedMCPToolName(name)
	tools := h.pluginTools()
	if tools == nil {
		return
	}
	tools.RecordAudit(appplugin.MCPToolAudit{
		PluginID:    pluginID,
		Tool:        name,
		Client:      client,
		Mode:        mode,
		Access:      access,
		Target:      target,
		Duration:    time.Since(started),
		Status:      status,
		ResultBytes: resultBytes,
	})
}

func (h *mcpPolicyHandler) recordPluginActivity(name string, code appmcp.ErrorCode, detail string) {
	h.policy.RecordActivity(appmcp.ActivityView{
		Tool:   name,
		Status: activityStatusForPlugin(code),
		Rule:   string(code),
		Detail: detail,
	})
}

func activityStatusForPlugin(code appmcp.ErrorCode) appmcp.ActivityStatus {
	switch code {
	case "":
		return appmcp.ActivityOK
	case appmcp.CodePluginToolBusy:
		return appmcp.ActivityBlocked
	case appmcp.CodePluginToolTimeout, appmcp.CodePluginToolCancelled:
		return appmcp.ActivityTimeout
	case appmcp.CodePluginToolResultLimit:
		return appmcp.ActivityTruncated
	case appmcp.CodePluginToolUnavailable, appmcp.CodePluginToolReloading:
		return appmcp.ActivityUnavailable
	case appmcp.CodePluginToolDenied, appmcp.CodePermissionDenied,
		appmcp.CodeApprovalDenied, appmcp.CodeApprovalExpired,
		appmcp.CodeSessionOutOfScope, appmcp.CodeScopeMismatch:
		return appmcp.ActivityDenied
	default:
		return appmcp.ActivityFailed
	}
}

func pluginAuditStatus(code appmcp.ErrorCode) string {
	return string(code)
}

// errorCodeOf extracts the stable code from a policy error, or an empty code
// for an untyped failure.
func errorCodeOf(err error) appmcp.ErrorCode {
	var typed *appmcp.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
