package plugin

import "time"

// AuditEventType is a member of the closed audit event vocabulary. Events
// record lifecycle and authority changes only; they never carry payload,
// clipboard, file, or query content.
type AuditEventType string

const (
	AuditInstall     AuditEventType = "install"
	AuditUpdate      AuditEventType = "update"
	AuditRollback    AuditEventType = "rollback"
	AuditUninstall   AuditEventType = "uninstall"
	AuditPurge       AuditEventType = "purge"
	AuditEnable      AuditEventType = "enable"
	AuditDisable     AuditEventType = "disable"
	AuditGrant       AuditEventType = "grant"
	AuditDeny        AuditEventType = "deny"
	AuditRevoke      AuditEventType = "revoke"
	AuditActivation  AuditEventType = "activation"
	AuditCrash       AuditEventType = "crash"
	AuditDevelopment AuditEventType = "development"
	AuditTrust       AuditEventType = "trust"
	// AuditService records one host-service access at the ABI boundary. The
	// Reason names the service and method, for example "clipboard.read"; no
	// clipboard, storage, notification, or settings content is recorded.
	AuditService AuditEventType = "service"
	// AuditMCPTool records one plugin-contributed external MCP tool call,
	// including denied calls. Arguments and result payloads are never
	// recorded; the explicit fields describe the call without content.
	AuditMCPTool AuditEventType = "mcp_tool"
	// AuditProvider records connection-provider protocol claims, opens,
	// failures, and denials. The reason names the action and result; no
	// stream bytes, terminal state, credential, or restore descriptor is
	// recorded.
	AuditProvider AuditEventType = "provider"
	// AuditData records data-provider opens, operation classes, queries,
	// hidden secret reads, limits, cancellations, and denials. The reason names
	// the action and result; no query input, parameter, result value, or secret
	// content is recorded.
	AuditData AuditEventType = "data"
)

// ServiceAuditReason formats the stable audit reason for one host-service
// access. It contains only names, never payload content.
func ServiceAuditReason(service, method, result string) string {
	reason := service + "." + method
	if result != "" {
		reason += ":" + result
	}
	return reason
}

// AuditEvent is one append-only JSONL row. Time is stored in UTC. Reason is a
// short typed/message key, never user content.
type AuditEvent struct {
	Time       time.Time      `json:"time"`
	Event      AuditEventType `json:"event"`
	PluginID   string         `json:"plugin_id,omitempty"`
	Source     Source         `json:"source,omitempty"`
	Version    string         `json:"version,omitempty"`
	Permission Permission     `json:"permission,omitempty"`
	Scope      *Scope         `json:"scope,omitempty"`
	State      State          `json:"state,omitempty"`
	Reason     string         `json:"reason,omitempty"`

	// MCP tool call fields. They carry names and counters only: never tool
	// arguments, result content, or a target's titles.
	Tool        string `json:"tool,omitempty"`
	Client      string `json:"client,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Access      string `json:"access,omitempty"`
	Target      string `json:"target,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	ResultBytes int    `json:"result_bytes,omitempty"`
	Status      string `json:"status,omitempty"`

	// Cross-plugin service call metadata. It records names, sizes, durations,
	// and statuses; it never carries a payload, schema value, or caller data.
	Provider string `json:"provider,omitempty"`
	Method   string `json:"method,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`

	// Protocol is the provider-owned Host protocol a connection event names.
	// It carries no endpoint, credential, or restore content.
	Protocol string `json:"protocol,omitempty"`

	// Class is the closed operation class a data-provider event reports. It is
	// a name only: no query input, parameter, or result content is recorded.
	Class string `json:"class,omitempty"`
}
