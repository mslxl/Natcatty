// Package forwarding holds the Port Forwarding Rule domain model: a saved
// local, remote, or dynamic forwarding definition and the rules that keep it
// meaningful. Runtime status is a projection owned by the application layer;
// a stored Rule never carries status or error text.
package forwarding

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mslxl/natcatty/domain/model"
)

// Type is the direction of a Port Forwarding Rule.
type Type string

const (
	// TypeLocal listens on this machine and reaches a destination through the
	// SSH Host (-L).
	TypeLocal Type = "local"
	// TypeRemote listens on the SSH Host and reaches a destination on this
	// machine (-R).
	TypeRemote Type = "remote"
	// TypeDynamic listens locally as a SOCKS5 proxy (-D).
	TypeDynamic Type = "dynamic"
)

// Status is the runtime projection of a rule. Only the first four values are
// ever shown by the UI; StatusUnknown reports a rule whose runtime state is
// not known yet, and is never persisted.
type Status string

const (
	StatusInactive   Status = "inactive"
	StatusConnecting Status = "connecting"
	StatusActive     Status = "active"
	StatusError      Status = "error"
	StatusUnknown    Status = "unknown"
)

// DefaultBindAddress is the loopback bind a blank form implies. Remote rules
// use the same default because the original form defaults to loopback.
const DefaultBindAddress = "127.0.0.1"

// Limits keep pasted garbage out of the Vault.
const (
	maxLabelLength       = 255
	maxBindAddressLength = 255
	maxRemoteHostLength  = 255
	minPort              = 1
	maxPort              = 65535
)

// Rule is one saved Port Forwarding Rule. It has no status or error field:
// runtime state is never persisted, so a restart always begins inactive.
type Rule struct {
	ID    model.ForwardingRuleID `json:"id"`
	Label string                 `json:"label"`
	Type  Type                   `json:"type"`
	// BindAddress is the listen address. Local and dynamic rules bind on this
	// machine; a remote rule binds on the SSH Host.
	BindAddress string `json:"bindAddress"`
	// LocalPort is the listen port for every type: this machine for local and
	// dynamic rules, the SSH Host for a remote rule.
	LocalPort int `json:"localPort"`
	// RemoteHost and RemotePort are the destination reached through the SSH
	// Host for local rules, or the destination reached on this machine for
	// remote rules. They are empty for dynamic rules.
	RemoteHost string       `json:"remoteHost,omitempty"`
	RemotePort int          `json:"remotePort,omitempty"`
	HostID     model.HostID `json:"hostId"`
	AutoStart  bool         `json:"autoStart,omitempty"`
	// AutoReconnect overrides AutoStart when set, matching the original
	// autoReconnect ?? autoStart rule.
	AutoReconnect *bool `json:"autoReconnect,omitempty"`
	// Enabled gates starting the rule, including auto-start. A nil value means
	// enabled, so rules written before this field existed keep running.
	Enabled   *bool     `json:"enabled,omitempty"`
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Draft is the editable part of a rule.
type Draft struct {
	Label         string
	Type          Type
	BindAddress   string
	LocalPort     int
	RemoteHost    string
	RemotePort    int
	HostID        model.HostID
	AutoStart     bool
	AutoReconnect *bool
	Enabled       *bool
}

// Field names the part of a rule a validation error concerns.
type Field string

const (
	FieldLabel       Field = "label"
	FieldType        Field = "type"
	FieldBindAddress Field = "bind_address"
	FieldLocalPort   Field = "local_port"
	FieldRemoteHost  Field = "remote_host"
	FieldRemotePort  Field = "remote_port"
	FieldHost        Field = "host"
)

// Issue names the validation rule a value broke.
type Issue string

const (
	RuleRequired    Issue = "required"
	RuleInvalid     Issue = "invalid"
	RuleOutOfRange  Issue = "out_of_range"
	RuleTooLong     Issue = "too_long"
	RuleUnsupported Issue = "unsupported"
)

// ValidationError reports the first rule a value broke.
type ValidationError struct {
	Field Field
	Rule  Issue
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("forwarding: %s is %s", e.Field, e.Rule)
}

// NormalizeDraft trims user input, fills defaults, and validates.
func NormalizeDraft(draft Draft) (Draft, error) {
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Type = Type(strings.ToLower(strings.TrimSpace(string(draft.Type))))
	draft.BindAddress = strings.TrimSpace(draft.BindAddress)
	draft.RemoteHost = strings.TrimSpace(draft.RemoteHost)
	draft.HostID = model.HostID(strings.TrimSpace(string(draft.HostID)))
	if draft.BindAddress == "" {
		draft.BindAddress = DefaultBindAddress
	}
	// A dynamic rule has no fixed destination. Clearing the fields here keeps
	// a type switch from leaving stale values in the record.
	if draft.Type == TypeDynamic {
		draft.RemoteHost = ""
		draft.RemotePort = 0
	}
	draft.AutoReconnect = cloneBool(draft.AutoReconnect)
	draft.Enabled = cloneBool(draft.Enabled)
	if err := validateDraft(draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// Validate checks a rule already in canonical form.
func (r Rule) Validate() error {
	return validateDraft(Draft{
		Label:         r.Label,
		Type:          r.Type,
		BindAddress:   r.BindAddress,
		LocalPort:     r.LocalPort,
		RemoteHost:    r.RemoteHost,
		RemotePort:    r.RemotePort,
		HostID:        r.HostID,
		AutoStart:     r.AutoStart,
		AutoReconnect: r.AutoReconnect,
		Enabled:       r.Enabled,
	})
}

func validateDraft(draft Draft) error {
	switch draft.Type {
	case TypeLocal, TypeRemote, TypeDynamic:
	default:
		return validation(FieldType, RuleUnsupported)
	}
	if draft.Label == "" {
		return validation(FieldLabel, RuleRequired)
	}
	if err := validateText(FieldLabel, draft.Label, maxLabelLength, false); err != nil {
		return err
	}
	if draft.HostID == "" {
		return validation(FieldHost, RuleRequired)
	}
	if draft.BindAddress == "" {
		return validation(FieldBindAddress, RuleRequired)
	}
	if err := validateText(FieldBindAddress, draft.BindAddress, maxBindAddressLength, true); err != nil {
		return err
	}
	if draft.LocalPort < minPort || draft.LocalPort > maxPort {
		return validation(FieldLocalPort, RuleOutOfRange)
	}
	if draft.Type == TypeDynamic {
		return nil
	}
	if draft.RemoteHost == "" {
		return validation(FieldRemoteHost, RuleRequired)
	}
	if err := validateText(FieldRemoteHost, draft.RemoteHost, maxRemoteHostLength, true); err != nil {
		return err
	}
	if draft.RemotePort < minPort || draft.RemotePort > maxPort {
		return validation(FieldRemotePort, RuleOutOfRange)
	}
	return nil
}

// AutoReconnectEnabled applies the original tri-state rule: an explicit value
// wins, otherwise AutoStart decides.
func (r Rule) AutoReconnectEnabled() bool {
	if r.AutoReconnect != nil {
		return *r.AutoReconnect
	}
	return r.AutoStart
}

// IsEnabled reports whether the rule may run. A nil flag is enabled.
func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// SanitizeRule normalizes a rule decoded from storage. It reports false when
// the record is unusable, so the caller can drop it without failing the whole
// Vault.
func SanitizeRule(rule Rule) (Rule, bool) {
	if strings.TrimSpace(string(rule.ID)) == "" {
		return Rule{}, false
	}
	normalized, err := NormalizeDraft(Draft{
		Label:         rule.Label,
		Type:          rule.Type,
		BindAddress:   rule.BindAddress,
		LocalPort:     rule.LocalPort,
		RemoteHost:    rule.RemoteHost,
		RemotePort:    rule.RemotePort,
		HostID:        rule.HostID,
		AutoStart:     rule.AutoStart,
		AutoReconnect: rule.AutoReconnect,
		Enabled:       rule.Enabled,
	})
	if err != nil {
		return Rule{}, false
	}
	return Rule{
		ID:            rule.ID,
		Label:         normalized.Label,
		Type:          normalized.Type,
		BindAddress:   normalized.BindAddress,
		LocalPort:     normalized.LocalPort,
		RemoteHost:    normalized.RemoteHost,
		RemotePort:    normalized.RemotePort,
		HostID:        normalized.HostID,
		AutoStart:     normalized.AutoStart,
		AutoReconnect: normalized.AutoReconnect,
		Enabled:       normalized.Enabled,
		OrderKey:      rule.OrderKey,
		CreatedAt:     rule.CreatedAt,
		UpdatedAt:     rule.UpdatedAt,
	}, true
}

// ApplyDraft copies a normalized draft's editable fields onto a rule.
func ApplyDraft(rule Rule, draft Draft) Rule {
	rule.Label = draft.Label
	rule.Type = draft.Type
	rule.BindAddress = draft.BindAddress
	rule.LocalPort = draft.LocalPort
	rule.RemoteHost = draft.RemoteHost
	rule.RemotePort = draft.RemotePort
	rule.HostID = draft.HostID
	rule.AutoStart = draft.AutoStart
	rule.AutoReconnect = cloneBool(draft.AutoReconnect)
	rule.Enabled = cloneBool(draft.Enabled)
	return rule
}

// DraftOf returns the editable fields of a rule. It is used to round-trip a
// rule through an edit without losing its runtime-neutral settings.
func DraftOf(rule Rule) Draft {
	return Draft{
		Label:         rule.Label,
		Type:          rule.Type,
		BindAddress:   rule.BindAddress,
		LocalPort:     rule.LocalPort,
		RemoteHost:    rule.RemoteHost,
		RemotePort:    rule.RemotePort,
		HostID:        rule.HostID,
		AutoStart:     rule.AutoStart,
		AutoReconnect: cloneBool(rule.AutoReconnect),
		Enabled:       cloneBool(rule.Enabled),
	}
}

// ListenAddress is the host:port a rule binds.
func (r Rule) ListenAddress() string {
	return net.JoinHostPort(r.BindAddress, strconv.Itoa(r.LocalPort))
}

// Destination is the remoteHost:remotePort a local or remote rule reaches.
// Dynamic rules have no fixed destination.
func (r Rule) Destination() string {
	return net.JoinHostPort(r.RemoteHost, strconv.Itoa(r.RemotePort))
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateText(field Field, value string, max int, noSpace bool) error {
	if len(value) > max {
		return validation(field, RuleTooLong)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return validation(field, RuleInvalid)
		}
		if noSpace && unicode.IsSpace(r) {
			return validation(field, RuleInvalid)
		}
	}
	return nil
}

func validation(field Field, rule Issue) error {
	return &ValidationError{Field: field, Rule: rule}
}
