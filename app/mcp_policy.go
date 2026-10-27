// External MCP policy surface. This file composes the ticket-56 read bridge
// with ticket 57's forced scope, session allowlist, ownership generations,
// permission modes, and expiring approvals. It is the handler the loopback
// listener serves in the running app; the read bridge itself stays unchanged
// so direct read-only calls keep their ticket-56 behavior.

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

// envMCPSessionIDs is the per-session allowlist the external controller reads
// at launch. An empty value is intentional no access, never a wildcard.
const envMCPSessionIDs = "NETCATTY_MCP_SESSION_IDS"

// mcpModeState is the discovery-backed permission mode. Get re-reads the
// discovery file before every write, so a mode changed in Settings applies to
// the next tool call without a restart.
type mcpModeState struct {
	mu   sync.Mutex
	mode appmcp.Mode
	path string
}

func newMCPModeState(path string) *mcpModeState {
	return &mcpModeState{mode: appmcp.DefaultMode, path: path}
}

// Get returns the mode recorded in the discovery file, falling back to the
// in-memory value when the file is missing or malformed.
func (m *mcpModeState) Get() appmcp.Mode {
	m.mu.Lock()
	path := m.path
	fallback := m.mode
	m.mu.Unlock()
	if path == "" {
		return fallback
	}
	discovery, err := mcprpc.ReadDiscovery(path)
	if err != nil {
		return fallback
	}
	mode, ok := appmcp.ParseMode(discovery.PermissionMode)
	if !ok {
		return fallback
	}
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
	return mode
}

// Set stores a new mode and rewrites the discovery file's permissionMode,
// preserving the port, token, PID, and scope pointers.
func (m *mcpModeState) Set(mode appmcp.Mode) error {
	if _, ok := appmcp.ParseMode(string(mode)); !ok {
		return fmt.Errorf("external MCP: unknown permission mode %q", mode)
	}
	m.mu.Lock()
	m.mode = mode
	path := m.path
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	discovery, err := mcprpc.ReadDiscovery(path)
	if err != nil {
		// The listener owns the file; a missing file means there is nothing to
		// persist yet (for example a directly constructed test graph).
		return nil
	}
	discovery.PermissionMode = string(mode)
	discovery.UpdatedAt = ""
	if _, err := mcprpc.WriteDiscovery(path, discovery); err != nil {
		return err
	}
	return nil
}

// mcpSessionCloser closes one live Session by ID.
type mcpSessionCloser interface {
	Close(model.SessionID) (bool, error)
}

// mcpJobSessions resolves a background job id to the Session that started it.
// Background job execution arrives with ticket 58; until then production has no
// resolver and every real jobId is honestly reported as not found. The seam
// lets tests prove terminal_stop's scope and generation checks against the
// catalog's actual jobId input instead of the read bridge's placeholder shape.
type mcpJobSessions interface {
	SessionForJob(jobID string) (model.SessionID, bool)
}

// mcpPolicyHandler evaluates policy before dispatching to the read bridge or
// one of the two approval-bypassing actions this build can execute.
type mcpPolicyHandler struct {
	bridge   *mcpReadBridge
	policy   *appmcp.Service
	sessions mcpSessionReader
	closer   mcpSessionCloser
	jobs     mcpJobSessions
}

func newMCPPolicyHandler(bridge *mcpReadBridge, policy *appmcp.Service,
	sessions mcpSessionReader, closer mcpSessionCloser, jobs mcpJobSessions) *mcpPolicyHandler {
	return &mcpPolicyHandler{
		bridge: bridge, policy: policy, sessions: sessions, closer: closer, jobs: jobs,
	}
}

// Handle is the mcprpc handler. Every call is forced through the policy before
// any read runs or any action starts.
func (h *mcpPolicyHandler) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	arguments := map[string]any{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &arguments); err != nil {
			return nil, fmt.Errorf("external MCP: invalid params for %s: %w", method, err)
		}
	}
	tool, known := catalog.ByRPCMethod(method)
	if !known {
		return nil, fmt.Errorf("external MCP: unknown method %s", method)
	}
	client := mcpClientLabel(stringParam(arguments, "_mcpClient"))
	delete(arguments, "_mcpClient")
	requested, err := appmcp.RequestedScopeFrom(arguments)
	if err != nil {
		return mcpPolicyFailure(err, tool.Name), nil
	}
	target := h.describeTarget(ctx, tool.Name, arguments)
	decision, err := h.policy.Authorize(ctx, appmcp.Request{
		Tool: appmcp.Tool{
			Name:                  tool.Name,
			Write:                 tool.Policy.Write,
			BypassesApproval:      tool.Policy.BypassesApproval,
			BypassesObserverBlock: tool.Policy.BypassesObserverBlock,
		},
		Client:    client,
		Scope:     requested,
		Target:    target,
		Arguments: arguments,
	})
	if err != nil {
		return mcpPolicyFailure(err, tool.Name), nil
	}

	switch method {
	case "netcatty/getContext":
		return h.environment(ctx, decision.Sessions), nil
	case "netcatty/readContext":
		if stringParam(arguments, "sessionId") == "" {
			live := h.liveSessions(decision.Sessions)
			if len(live) != 1 {
				return mcpFailure("sessionId is required."), nil
			}
			arguments["sessionId"] = string(live[0].ID)
		}
	case "public/session/close":
		return h.closeSession(arguments, tool.Name), nil
	case "public/vault/hosts/open":
		return h.openHost(arguments, decision), nil
	case "netcatty/jobStop":
		// terminal_stop bypasses approval and the observer block, but a job
		// that resolves to a Session from a superseded generation is still
		// rejected. A never-owned Session is fine: jobs are not owned Sessions.
		if sessionID := target.SessionID; sessionID != "" {
			var typed *appmcp.Error
			if err := h.policy.ValidateOwnership(sessionID); errors.As(err, &typed) &&
				typed.Code == appmcp.CodeStaleGeneration {
				return mcpPolicyFailure(err, tool.Name), nil
			}
		}
		return mcpFailure("Background job not found."), nil
	}
	if tool.Policy.Write {
		return mcpFailure("External MCP write execution is not available in this build yet."), nil
	}
	return h.bridge.Call(ctx, method, arguments)
}

// describeTarget builds the redaction-safe target shown in an approval prompt.
// A Session label wins, then a Vault Host label; otherwise the most specific id
// argument identifies the action.
func (h *mcpPolicyHandler) describeTarget(ctx context.Context, toolName string, arguments map[string]any) appmcp.Target {
	if sessionID := stringParam(arguments, "sessionId"); sessionID != "" {
		target := appmcp.Target{SessionID: sessionID, Display: sessionID}
		if description, ok := h.sessions.Describe(model.SessionID(sessionID)); ok {
			target.HostID = string(description.HostID)
			target.Display = mcpTargetLabel(description.Label, sessionID)
		}
		return target
	}
	if hostID := stringParam(arguments, "hostId"); hostID != "" {
		target := appmcp.Target{HostID: hostID, Display: hostID}
		if h.bridge.vault != nil {
			if host, err := h.bridge.vault.Get(ctx, model.HostID(hostID)); err == nil {
				target.Display = mcpTargetLabel(host.Label, hostID)
			}
		}
		return target
	}
	if jobID := stringParam(arguments, "jobId"); jobID != "" {
		target := appmcp.Target{Display: jobID}
		if h.jobs != nil {
			if sessionID, ok := h.jobs.SessionForJob(jobID); ok && sessionID != "" {
				target.SessionID = string(sessionID)
				if description, ok := h.sessions.Describe(sessionID); ok {
					target.HostID = string(description.HostID)
					target.Display = mcpTargetLabel(description.Label, string(sessionID))
				}
			}
		}
		return target
	}
	for _, key := range []string{"noteId", "snippetId", "scriptId", "ruleId", "tunnelId", "runId"} {
		if value := stringParam(arguments, key); value != "" {
			return appmcp.Target{Display: value}
		}
	}
	return appmcp.Target{Display: toolName}
}

func mcpTargetLabel(label, id string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", label, id)
}

// environment mirrors getContext but lists only live Sessions inside the
// effective scope. A tunnel is visible only when its Host has at least one
// Session inside that intersection, so a Host with no scoped Session cannot
// leak its forwarding rules through the environment.
func (h *mcpPolicyHandler) environment(ctx context.Context, sessions []string) map[string]any {
	environment := h.bridge.environment(ctx)
	hosts := make([]any, 0, len(sessions))
	allowedHosts := make(map[string]bool)
	for _, description := range h.sessions.DescribeAll() {
		if !appmcp.SessionAllowed(sessions, string(description.ID)) {
			continue
		}
		hosts = append(hosts, sessionHostView(description))
		if description.HostID != "" {
			allowedHosts[string(description.HostID)] = true
		}
	}
	environment["hosts"] = hosts
	environment["hostCount"] = len(hosts)
	tunnels, _ := environment["activePortForwardTunnels"].([]any)
	filtered := make([]any, 0, len(tunnels))
	for _, raw := range tunnels {
		tunnel, _ := raw.(map[string]any)
		hostID, _ := tunnel["hostId"].(string)
		if hostID != "" && allowedHosts[hostID] {
			filtered = append(filtered, tunnel)
		}
	}
	environment["activePortForwardTunnels"] = filtered
	return environment
}

// liveSessions filters the effective scope to Sessions that are actually live.
func (h *mcpPolicyHandler) liveSessions(sessions []string) []appsession.SessionDescription {
	live := make([]appsession.SessionDescription, 0, len(sessions))
	for _, description := range h.sessions.DescribeAll() {
		if appmcp.SessionAllowed(sessions, string(description.ID)) {
			live = append(live, description)
		}
	}
	return live
}

// closeSession implements session_close. Ownership is checked in the current
// generation after the scope was already enforced, so a stale or foreign
// Session is rejected before the close starts.
func (h *mcpPolicyHandler) closeSession(arguments map[string]any, toolName string) map[string]any {
	sessionID := stringParam(arguments, "sessionId")
	if sessionID == "" {
		return mcpFailure("sessionId is required.")
	}
	if err := h.policy.ValidateOwnership(sessionID); err != nil {
		return mcpPolicyFailure(err, toolName)
	}
	if h.closer == nil {
		return mcpFailure("Session close is not available in this build.")
	}
	found, err := h.closer.Close(model.SessionID(sessionID))
	if err != nil {
		return mcpFailure(err.Error())
	}
	if !found {
		return mcpFailure(fmt.Sprintf("Session %q was not found.", sessionID))
	}
	h.policy.ReleaseOwnedSession(sessionID)
	return map[string]any{"ok": true, "sessionId": sessionID, "status": "closed"}
}

// openHost implements host_open's policy path. Full Host opening belongs to the
// write-execution slice; this build adopts the one live Session already in
// scope for the Host and records ownership in the current generation. Reuse in
// the original Electron bridge is visibility-only; the adoption divergence is
// recorded in ticket 57 so session_close's bypass path is exercisable here.
func (h *mcpPolicyHandler) openHost(arguments map[string]any, decision appmcp.Decision) map[string]any {
	hostID := stringParam(arguments, "hostId")
	if hostID == "" {
		return mcpFailure("hostId is required.")
	}
	if newTab, _ := arguments["newTab"].(string); strings.EqualFold(strings.TrimSpace(newTab), "true") {
		return mcpFailure("Opening a new Host tab is not available in this build; omit newTab to adopt an existing scoped Session.")
	}
	candidates := make([]appsession.SessionDescription, 0, 1)
	for _, description := range h.sessions.DescribeAll() {
		if string(description.HostID) == hostID &&
			appmcp.SessionAllowed(decision.Sessions, string(description.ID)) {
			candidates = append(candidates, description)
		}
	}
	switch len(candidates) {
	case 0:
		return mcpFailure(fmt.Sprintf(
			"Host %q has no live Session inside the external MCP scope. Opening a new Host tab is not available in this build.", hostID))
	case 1:
	default:
		return mcpFailure(fmt.Sprintf(
			"Host %q has multiple Sessions inside the external MCP scope; this build cannot choose one for host_open.", hostID))
	}
	description := candidates[0]
	generation := h.policy.GrantOwnedSession(string(description.ID), hostID)
	return map[string]any{
		"ok":         true,
		"sessionId":  string(description.ID),
		"hostId":     hostID,
		"status":     "adopted",
		"generation": generation,
	}
}

// mcpPolicyFailure renders a typed denial with the catalog tool name.
func mcpPolicyFailure(err error, tool string) map[string]any {
	var typed *appmcp.Error
	if errors.As(err, &typed) && typed.Tool == "" {
		copied := *typed
		copied.Tool = tool
		return appmcp.Result(&copied)
	}
	return appmcp.Result(err)
}

// mcpClientLabel sanitizes the client identity the stdio layer forwards. It is
// shown in the approval prompt, so control characters and an unbounded name
// never reach the UI. An empty result means the client sent no usable identity;
// the Settings surface localizes that fallback instead of the application layer
// embedding an English display string.
func mcpClientLabel(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, character := range value {
		if builder.Len() >= 120 {
			break
		}
		if character < 0x20 || character == 0x7f {
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

// mcpSessionAllowlist reads the launch-time allowlist.
func mcpSessionAllowlist() []string {
	return appmcp.ParseSessionAllowlist(os.Getenv(envMCPSessionIDs))
}

// mcpSettingsController adapts the policy and its discovery-backed mode to
// the Settings surface without teaching the application layer about MyGo.
type mcpSettingsController struct {
	policy *appmcp.Service
	mode   *mcpModeState
}

func (c *mcpSettingsController) Snapshot() appmcp.Snapshot { return c.policy.Snapshot() }

func (c *mcpSettingsController) SetMode(mode appmcp.Mode) error { return c.mode.Set(mode) }

func (c *mcpSettingsController) Approve(id string) error { return c.policy.Approve(id) }

func (c *mcpSettingsController) Deny(id string) error { return c.policy.Deny(id) }

func (c *mcpSettingsController) Dismiss(id string) { c.policy.Dismiss(id) }
