package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// MaxMCPToolArgumentsBytes bounds the JSON arguments the host copies into one
// mcp_tool_call dispatch command. A tool call is a control message, not a data
// transfer; large payloads belong in the plugin's private storage or streams.
const MaxMCPToolArgumentsBytes = 1 << 20

// MaxMCPToolTimeout is the hard host cap for one plugin tool call: the
// existing external MCP timeout, which a declared class can only shorten.
const MaxMCPToolTimeout = domainplugin.MCPStandardToolTimeout

// ToolCaller invokes one declared MCP tool on the plugin instance that
// contributed it. The container implements it, serializing calls with the
// instance lifecycle and enforcing the one-call-per-instance rule.
type ToolCaller interface {
	CallTool(ctx context.Context, tool string, arguments map[string]any,
		timeout time.Duration, progress func(progress uint32, message string)) ([]byte, error)
}

// MCPToolRegistrar is the consumer-owned port the container uses to publish a
// manifest's declared MCP tools. The app implements it with the external MCP
// registry; tests use a recorder. Every registration creates a disposer on the
// fiber's effect stack, so tools disappear when the fiber leaves ACTIVE and
// `notifications/tools/list_changed` can be sent.
type MCPToolRegistrar interface {
	RegisterMCPTool(ref PluginRef, tool domainplugin.MCPTool, call ToolCaller) (Disposer, error)
}

// ToolQueueFullError reports a tool call rejected because every queue slot for
// the plugin instance was occupied. It is retryable: the caller can retry once
// an in-flight call completes.
type ToolQueueFullError struct {
	Limit int
}

func (e *ToolQueueFullError) Error() string {
	return fmt.Sprintf("plugin: MCP tool call queue is full (%d)", e.Limit)
}

// ToolNoResultError reports a guest that returned from dispatch and poll
// without calling mcp_tool_result for the request. The call fails closed with
// a typed error rather than returning an empty success.
type ToolNoResultError struct {
	Tool string
}

func (e *ToolNoResultError) Error() string {
	return fmt.Sprintf("plugin: guest returned no result for MCP tool %q", e.Tool)
}

// UndeclaredMCPToolError reports a call for a name the fiber did not declare in
// its manifest. The closed declaration set is what consent, the client-visible
// list, and the audit trail describe, so a name outside it never reaches the
// guest.
type UndeclaredMCPToolError struct {
	Tool string
}

func (e *UndeclaredMCPToolError) Error() string {
	return fmt.Sprintf("plugin: MCP tool %q is not declared by this plugin", e.Tool)
}

// ErrMCPToolsUnavailable reports a fiber without an MCP tool registrar. It is
// a host wiring failure, not a guest failure.
var ErrMCPToolsUnavailable = errors.New("plugin: MCP tool contributions are not configured")

// pendingToolCall is one in-flight tool call on a fiber. Only one call per
// instance is in flight, but request ids keep a late reply from a previous
// call from being mistaken for the current one.
type pendingToolCall struct {
	result   chan []byte
	progress func(progress uint32, message string)
}

// fiberToolCaller binds the ToolCaller port to one fiber and the container
// that can recycle an instance after a trap or deadline.
type fiberToolCaller struct {
	container *Container
	fiber     *Fiber
}

// CallTool implements ToolCaller.
func (t fiberToolCaller) CallTool(ctx context.Context, tool string,
	arguments map[string]any, timeout time.Duration,
	progress func(progress uint32, message string)) ([]byte, error) {
	return t.container.callTool(ctx, t.fiber, tool, arguments, timeout, progress)
}

// callTool runs one tool call on a fiber: admission, instance dispatch under
// an explicit deadline, result receipt, and crash containment.
func (c *Container) callTool(ctx context.Context, fiber *Fiber, tool string,
	arguments map[string]any, timeout time.Duration,
	progress func(progress uint32, message string)) ([]byte, error) {
	if fiber == nil || fiber.toolQueue == nil {
		return nil, ErrMCPToolsUnavailable
	}
	// Only the closed set the manifest declared is callable. This is defense in
	// depth below the app's registry, which already resolves from that set.
	if !fiber.declaresMCPTool(tool) {
		return nil, &UndeclaredMCPToolError{Tool: tool}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = domainplugin.MCPStandardToolTimeout
	} else if timeout > MaxMCPToolTimeout {
		timeout = MaxMCPToolTimeout
	}
	// Admission is the bounded queue. One slot is held by the executing call,
	// so depth N admits N waiting calls and the next caller gets a busy error.
	select {
	case fiber.toolQueue <- struct{}{}:
	default:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, &ToolQueueFullError{Limit: fiber.toolQueueDepth}
	}
	defer func() { <-fiber.toolQueue }()

	// Execution is context-aware so a client disconnect while queued cancels
	// the call instead of running it after the disconnect.
	select {
	case fiber.toolExec <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-fiber.toolExec }()

	fiber.mu.Lock()
	instance := fiber.instanceRT
	active := fiber.state == domainplugin.StateActive && instance != nil
	fiber.mu.Unlock()
	if !active {
		return nil, fmt.Errorf("%w: %s", ErrNotActive, fiber.id)
	}

	fiber.callMu.Lock()
	defer fiber.callMu.Unlock()
	fiber.mu.Lock()
	active = fiber.state == domainplugin.StateActive && fiber.instanceRT == instance
	fiber.mu.Unlock()
	if !active {
		return nil, fmt.Errorf("%w: %s", ErrNotActive, fiber.id)
	}

	if arguments == nil {
		arguments = map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: encode MCP tool arguments: %w", fiber.ref.ID, err)
	}
	if len(encoded) > MaxMCPToolArgumentsBytes {
		return nil, &LimitError{
			Resource: "MCP tool arguments bytes",
			Limit:    MaxMCPToolArgumentsBytes,
		}
	}
	requestID := fiber.nextToolRequest.Add(1)
	if requestID == 0 {
		requestID = fiber.nextToolRequest.Add(1)
	}
	command, err := json.Marshal(map[string]any{
		"kind":      "mcp_tool_call",
		"requestId": requestID,
		"tool":      tool,
		"arguments": json.RawMessage(encoded),
	})
	if err != nil {
		return nil, fmt.Errorf("plugin %s: encode MCP tool command: %w", fiber.ref.ID, err)
	}
	pending := &pendingToolCall{result: make(chan []byte, 1), progress: progress}
	fiber.toolMu.Lock()
	fiber.toolPending[requestID] = pending
	fiber.toolMu.Unlock()
	defer func() {
		fiber.toolMu.Lock()
		delete(fiber.toolPending, requestID)
		fiber.toolMu.Unlock()
	}()

	if err := instance.DispatchTimed(ctx, string(command), timeout); err != nil {
		if IsUnusable(err) {
			c.recycleLocked(fiber, err)
		}
		return nil, err
	}
	select {
	case payload := <-pending.result:
		return payload, nil
	default:
	}
	// A guest may complete on the next poll instead of inside dispatch. One
	// poll is enough for the reactor contract; a guest that still has no
	// result fails closed.
	if err := instance.Poll(ctx); err != nil {
		if IsUnusable(err) {
			c.recycleLocked(fiber, err)
		}
		return nil, err
	}
	select {
	case payload := <-pending.result:
		return payload, nil
	default:
		return nil, &ToolNoResultError{Tool: tool}
	}
}

// declaresMCPTool reports whether the manifest declared this qualified tool
// name on this fiber. The runtime may enable or disable a declaration, but it
// can never create a name the host did not consent to.
func (f *Fiber) declaresMCPTool(tool string) bool {
	for _, declared := range f.manifest.Contributions.MCPTools {
		if domainplugin.QualifiedMCPToolName(f.ref.ID, declared.Name) == tool {
			return true
		}
	}
	return false
}

// deliverToolResult is the mcp_tool_result host import. It copies the payload
// into the waiting call and reports an unknown or already-answered request.
func (f *Fiber) deliverToolResult(requestID uint32, payload []byte) int32 {
	f.toolMu.Lock()
	pending := f.toolPending[requestID]
	f.toolMu.Unlock()
	if pending == nil {
		return -3
	}
	copied := append([]byte(nil), payload...)
	select {
	case pending.result <- copied:
		return 0
	default:
		return -3
	}
}

// deliverToolProgress is the mcp_tool_progress host import. Progress is
// advisory: a call without a client progress token simply drops it.
func (f *Fiber) deliverToolProgress(requestID uint32, progress uint32, message string) int32 {
	f.toolMu.Lock()
	pending := f.toolPending[requestID]
	f.toolMu.Unlock()
	if pending == nil {
		return -3
	}
	if pending.progress != nil {
		pending.progress(progress, message)
	}
	return 0
}
