package fakehost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ToolCall invokes one declared MCP tool with the same dispatch command the
// container builds. The guest answers through the mcp_tool_result import.
func (r *Runner) ToolCall(ctx context.Context, call ToolCall) (ToolObservation, error) {
	if r == nil || r.instance == nil {
		return ToolObservation{}, fmt.Errorf("fake host: no live instance")
	}
	r.beginCall()
	defer r.endCall()
	name := call.Name
	if !strings.HasPrefix(name, "plugin.") {
		name = domainplugin.QualifiedMCPToolName(r.manifest.ID, name)
	}
	if !r.declaresTool(name) {
		return ToolObservation{}, fmt.Errorf("MCP tool %q is not declared by this plugin", name)
	}
	arguments := call.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return ToolObservation{}, fmt.Errorf("encode MCP tool arguments: %w", err)
	}
	r.mu.Lock()
	r.nextToolID++
	requestID := uint32(r.nextToolID)
	pending := &pendingToolCall{result: make(chan []byte, 1)}
	r.toolCalls[requestID] = pending
	r.mu.Unlock()
	command, err := json.Marshal(map[string]any{
		"kind":      "mcp_tool_call",
		"requestId": requestID,
		"tool":      name,
		"arguments": json.RawMessage(encoded),
	})
	if err != nil {
		r.deletePendingTool(requestID)
		return ToolObservation{}, fmt.Errorf("encode MCP tool command: %w", err)
	}
	if err := r.instance.Dispatch(ctx, string(command)); err != nil {
		r.deletePendingTool(requestID)
		return ToolObservation{Tool: name, Request: requestID, Error: err.Error()}, err
	}
	payload, ok := takeToolResult(pending)
	if !ok {
		if err := r.instance.Poll(ctx); err != nil {
			r.deletePendingTool(requestID)
			return ToolObservation{Tool: name, Request: requestID, Error: err.Error()}, err
		}
		payload, ok = takeToolResult(pending)
	}
	r.deletePendingTool(requestID)
	if !ok {
		err := fmt.Errorf("fake host: guest returned no result for MCP tool %q", name)
		return ToolObservation{Tool: name, Request: requestID, Error: err.Error()}, err
	}
	observation := ToolObservation{
		Tool: name, Request: requestID, Result: string(payload),
		Progress: append([]ToolProgress(nil), pending.progress...),
	}
	r.mu.Lock()
	r.tools = append(r.tools, observation)
	r.mu.Unlock()
	return observation, nil
}

func (r *Runner) declaresTool(name string) bool {
	for _, declared := range r.manifest.Contributions.MCPTools {
		if domainplugin.QualifiedMCPToolName(r.manifest.ID, declared.Name) == name {
			return true
		}
	}
	return false
}

func takeToolResult(pending *pendingToolCall) ([]byte, bool) {
	select {
	case payload := <-pending.result:
		return payload, true
	default:
		return nil, false
	}
}

func (r *Runner) deletePendingTool(requestID uint32) {
	r.mu.Lock()
	delete(r.toolCalls, requestID)
	r.mu.Unlock()
}

func (r *Runner) deliverToolResult(requestID uint32, payload []byte) int32 {
	r.mu.Lock()
	pending := r.toolCalls[requestID]
	r.mu.Unlock()
	if pending == nil {
		return -3
	}
	select {
	case pending.result <- append([]byte(nil), payload...):
		return 0
	default:
		return -3
	}
}

func (r *Runner) deliverToolProgress(requestID uint32, progress uint32, message string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.toolCalls[requestID]
	if pending == nil {
		return -3
	}
	pending.progress = append(pending.progress, ToolProgress{Progress: progress, Message: message})
	return 0
}
