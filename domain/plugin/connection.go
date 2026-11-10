package plugin

import (
	"errors"
	"fmt"
	"strings"

	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
)

// Connection-provider limits. They bound the static declaration a manifest may
// make; the runtime separately bounds each session's queues and chunks.
const (
	// MaxConnectionProviders bounds the number of protocols one manifest may
	// claim.
	MaxConnectionProviders = 8
	// MaxConnectionHostFields bounds the Host form schema.
	MaxConnectionHostFields = 32
	// MaxConnectionSignals bounds the closed signal vocabulary a provider can
	// accept.
	MaxConnectionSignals = 16
	// MaxConnectionAuthMethods bounds the host-owned authentication methods a
	// provider may name.
	MaxConnectionAuthMethods = 8
	// MaxConnectionRestoreSchema bounds the declared restore-state field names.
	MaxConnectionRestoreSchema = 16
	// DefaultConnectionMaxSessions is the session bound a provider gets when it
	// does not declare max_sessions.
	DefaultConnectionMaxSessions = 64
	// MaxConnectionSessions is the hard per-provider session bound. The host
	// never admits more streams than this even if a manifest asks for more.
	MaxConnectionSessions = 256
	// MaxConnectionRestoreBytes is the hard bound on one opaque restore
	// descriptor, matching the workspace panic bound.
	MaxConnectionRestoreBytes = 64 << 10
)

// ConnectionProvider is one terminal protocol a plugin contributes. The host
// owns terminal emulation, dimensions, authentication, trust, and restore
// policy; the provider owns only the network byte stream.
type ConnectionProvider struct {
	// Protocol is the namespaced x-<publisher>-<name> Host protocol id.
	Protocol string `toml:"protocol"`
	// Name is the display name. NameKey optionally names a translated catalog
	// message and takes precedence when present.
	Name    string `toml:"name"`
	NameKey string `toml:"name_key"`
	// Icon names a bundled icon asset, not a path.
	Icon string `toml:"icon"`
	// HostFields is the declarative form of the non-secret Host settings the
	// provider reads. Credential values stay host-owned and are never part of a
	// provider open request.
	HostFields []ConnectionField `toml:"host_fields"`
	// Credentials names Host fields that carry credential selectors. Their
	// values are resolved and used only by the host auth service.
	Credentials []string `toml:"credentials"`
	// Capabilities is the fixed per-version capability declaration.
	Capabilities ConnectionCapabilities `toml:"capabilities"`
	// AuthMethods names the host-owned methods this provider can drive. The
	// provider never collects or verifies a credential itself.
	AuthMethods []string `toml:"auth_methods"`
	// RestoreSchema names the opaque fields the provider's restore descriptor
	// may contain. The host stores the descriptor as bounded opaque bytes.
	RestoreSchema []string `toml:"restore_schema"`
}

// ConnectionField is one declarative Host form field. The host renders and
// validates the form; the provider receives only its JSON value.
type ConnectionField struct {
	Key      string `toml:"key"`
	Label    string `toml:"label"`
	LabelKey string `toml:"label_key"`
	// Kind is one of text, number, boolean, select, or secret. A secret field
	// is resolved by the host and never delivered to the provider.
	Kind     string `toml:"kind"`
	Required bool   `toml:"required"`
}

// ConnectionCapabilities is the static capability set read at activation. The
// host trims calls and UI to this set; a call outside it is rejected instead of
// being optimistically forwarded.
type ConnectionCapabilities struct {
	Resize    bool     `toml:"resize"`
	Probe     bool     `toml:"probe"`
	Reconnect bool     `toml:"reconnect"`
	Signals   []string `toml:"signals"`
	// MaxSessions bounds concurrent streams served by the one provider
	// instance. Zero means DefaultConnectionMaxSessions.
	MaxSessions int `toml:"max_sessions"`
	// RestoreStateMax is the maximum opaque restore descriptor length in bytes.
	// Zero declares that the provider cannot restore.
	RestoreStateMax int `toml:"restore_state_max"`
}

// EffectiveMaxSessions applies the default and the hard cap.
func (c ConnectionCapabilities) EffectiveMaxSessions() int {
	if c.MaxSessions <= 0 {
		return DefaultConnectionMaxSessions
	}
	if c.MaxSessions > MaxConnectionSessions {
		return MaxConnectionSessions
	}
	return c.MaxSessions
}

// EffectiveRestoreMax applies the hard restore-descriptor cap.
func (c ConnectionCapabilities) EffectiveRestoreMax() int {
	if c.RestoreStateMax <= 0 {
		return 0
	}
	if c.RestoreStateMax > MaxConnectionRestoreBytes {
		return MaxConnectionRestoreBytes
	}
	return c.RestoreStateMax
}

// SupportsSignal reports whether name is in the declared signal set.
func (c ConnectionCapabilities) SupportsSignal(name string) bool {
	for _, signal := range c.Signals {
		if signal == name {
			return true
		}
	}
	return false
}

// providerAuthMethods is the closed set of host-owned authentication methods a
// connection provider may name.
var providerAuthMethods = map[string]bool{
	"none":                 true,
	"password":             true,
	"publickey":            true,
	"agent":                true,
	"keyboard-interactive": true,
	"challenge":            true,
}

var providerFieldKinds = map[string]bool{
	"text":    true,
	"number":  true,
	"boolean": true,
	"select":  true,
	"secret":  true,
}

// connectionFieldKeyPattern matches a Host-field key.
func validConnectionToken(text string, max int) bool {
	if text == "" || len(text) > max {
		return false
	}
	for i, r := range text {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case (r == '_' || r == '-' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

// ValidateConnectionProvider checks one static contribution. index selects the
// manifest field path in the error.
func ValidateConnectionProvider(pluginID string, provider ConnectionProvider, index int) error {
	field := fmt.Sprintf("contributions.connections[%d]", index)
	if !protocolmodel.ValidProviderID(protocolmodel.ID(provider.Protocol)) {
		return invalid(field+".protocol",
			"%q must name an x-<publisher>-<name> protocol", provider.Protocol)
	}
	if protocolmodel.Reserved(protocolmodel.ID(provider.Protocol)) {
		return invalid(field+".protocol", "%q is a reserved built-in protocol", provider.Protocol)
	}
	if strings.TrimSpace(provider.Name) == "" && provider.NameKey == "" {
		return invalid(field+".name", "is required unless name_key is set")
	}
	if len(provider.Name) > 128 {
		return invalid(field+".name", "must be at most 128 characters")
	}
	if err := validateMessageKey(field+".name_key", provider.NameKey); err != nil {
		return err
	}
	if provider.Icon != "" && !validConnectionToken(provider.Icon, 128) {
		return invalid(field+".icon", "must be a lowercase asset name of at most 128 characters")
	}
	if len(provider.HostFields) > MaxConnectionHostFields {
		return invalid(field+".host_fields",
			"at most %d fields are allowed", MaxConnectionHostFields)
	}
	fields := make(map[string]bool, len(provider.HostFields))
	for i, hostField := range provider.HostFields {
		sub := fmt.Sprintf("%s.host_fields[%d]", field, i)
		if !validConnectionToken(hostField.Key, 64) {
			return invalid(sub+".key", "%q must be a lowercase field key", hostField.Key)
		}
		if fields[hostField.Key] {
			return invalid(sub+".key", "duplicate field %q", hostField.Key)
		}
		fields[hostField.Key] = true
		if strings.TrimSpace(hostField.Label) == "" && hostField.LabelKey == "" {
			return invalid(sub+".label", "is required unless label_key is set")
		}
		if len(hostField.Label) > 128 {
			return invalid(sub+".label", "must be at most 128 characters")
		}
		if err := validateMessageKey(sub+".label_key", hostField.LabelKey); err != nil {
			return err
		}
		if !providerFieldKinds[hostField.Kind] {
			return invalid(sub+".kind", "unknown field kind %q", hostField.Kind)
		}
	}
	if len(provider.Credentials) > MaxConnectionHostFields {
		return invalid(field+".credentials", "at most %d entries are allowed", MaxConnectionHostFields)
	}
	credentials := make(map[string]bool, len(provider.Credentials))
	for i, name := range provider.Credentials {
		if !fields[name] {
			return invalid(fmt.Sprintf("%s.credentials[%d]", field, i),
				"%q is not a declared host field", name)
		}
		if credentials[name] {
			return invalid(fmt.Sprintf("%s.credentials[%d]", field, i),
				"duplicate credential field %q", name)
		}
		credentials[name] = true
	}
	if len(provider.AuthMethods) > MaxConnectionAuthMethods {
		return invalid(field+".auth_methods",
			"at most %d methods are allowed", MaxConnectionAuthMethods)
	}
	methods := make(map[string]bool, len(provider.AuthMethods))
	for i, method := range provider.AuthMethods {
		if !providerAuthMethods[method] {
			return invalid(fmt.Sprintf("%s.auth_methods[%d]", field, i),
				"unknown authentication method %q", method)
		}
		if methods[method] {
			return invalid(fmt.Sprintf("%s.auth_methods[%d]", field, i),
				"duplicate authentication method %q", method)
		}
		methods[method] = true
	}
	if len(provider.Capabilities.Signals) > MaxConnectionSignals {
		return invalid(field+".capabilities.signals",
			"at most %d signals are allowed", MaxConnectionSignals)
	}
	signals := make(map[string]bool, len(provider.Capabilities.Signals))
	for i, signal := range provider.Capabilities.Signals {
		if !validConnectionToken(signal, 32) {
			return invalid(fmt.Sprintf("%s.capabilities.signals[%d]", field, i),
				"%q must be a lowercase signal name", signal)
		}
		if signals[signal] {
			return invalid(fmt.Sprintf("%s.capabilities.signals[%d]", field, i),
				"duplicate signal %q", signal)
		}
		signals[signal] = true
	}
	if provider.Capabilities.MaxSessions < 0 ||
		provider.Capabilities.MaxSessions > MaxConnectionSessions {
		return invalid(field+".capabilities.max_sessions",
			"must be between 1 and %d, or 0 for the default", MaxConnectionSessions)
	}
	if provider.Capabilities.RestoreStateMax < 0 ||
		provider.Capabilities.RestoreStateMax > MaxConnectionRestoreBytes {
		return invalid(field+".capabilities.restore_state_max",
			"must be between 0 and %d bytes", MaxConnectionRestoreBytes)
	}
	if len(provider.RestoreSchema) > MaxConnectionRestoreSchema {
		return invalid(field+".restore_schema",
			"at most %d fields are allowed", MaxConnectionRestoreSchema)
	}
	schema := make(map[string]bool, len(provider.RestoreSchema))
	for i, name := range provider.RestoreSchema {
		if !validConnectionToken(name, 64) {
			return invalid(fmt.Sprintf("%s.restore_schema[%d]", field, i),
				"%q must be a lowercase field name", name)
		}
		if schema[name] {
			return invalid(fmt.Sprintf("%s.restore_schema[%d]", field, i),
				"duplicate restore field %q", name)
		}
		schema[name] = true
	}
	if provider.Capabilities.RestoreStateMax > 0 && len(provider.RestoreSchema) == 0 {
		return invalid(field+".restore_schema",
			"is required when restore_state_max is non-zero")
	}
	if provider.Capabilities.RestoreStateMax == 0 && len(provider.RestoreSchema) > 0 {
		return invalid(field+".restore_schema",
			"requires a non-zero capabilities.restore_state_max")
	}
	return nil
}

// ProviderErrorKind is the closed mapping from a provider failure to a typed
// Session status. The wire values are stable.
type ProviderErrorKind string

const (
	// ProviderErrorAuthFailed reports rejected host-owned authentication.
	ProviderErrorAuthFailed ProviderErrorKind = "auth-failed"
	// ProviderErrorUnreachable reports a network or endpoint failure.
	ProviderErrorUnreachable ProviderErrorKind = "unreachable"
	// ProviderErrorHostKeyChanged reports a trust verdict that blocks by
	// default.
	ProviderErrorHostKeyChanged ProviderErrorKind = "host-key-changed"
	// ProviderErrorProtocol reports a malformed request, response, or method
	// outside the capability set.
	ProviderErrorProtocol ProviderErrorKind = "protocol-error"
	// ProviderErrorTimeout reports a bounded operation that did not finish.
	ProviderErrorTimeout ProviderErrorKind = "timeout"
	// ProviderErrorCancelled reports a host or user cancellation.
	ProviderErrorCancelled ProviderErrorKind = "cancelled"
	// ProviderErrorFaulted reports that the provider instance trapped, was
	// unloaded, or became unusable.
	ProviderErrorFaulted ProviderErrorKind = "provider-faulted"
)

// ProviderError is a typed failure from a connection provider. Message is a
// short provider-supplied explanation; it never carries payload or credential
// content.
type ProviderError struct {
	Kind     ProviderErrorKind
	Provider string
	Protocol string
	Message  string
}

func (e *ProviderError) Error() string {
	kind := string(e.Kind)
	if kind == "" {
		kind = "provider-error"
	}
	context := ""
	if e.Provider != "" {
		context = " from " + e.Provider
	} else if e.Protocol != "" {
		context = " for " + e.Protocol
	}
	if e.Message != "" {
		return fmt.Sprintf("provider %s%s: %s", kind, context, e.Message)
	}
	return fmt.Sprintf("provider %s%s", kind, context)
}

// ProviderErrorKindByName maps a guest status string to the closed kind. An
// unknown or host-internal value is a protocol error, so a guest cannot invent
// a new Session status.
func ProviderErrorKindByName(name string) (ProviderErrorKind, bool) {
	switch ProviderErrorKind(strings.ToLower(strings.TrimSpace(name))) {
	case ProviderErrorAuthFailed:
		return ProviderErrorAuthFailed, true
	case ProviderErrorUnreachable:
		return ProviderErrorUnreachable, true
	case ProviderErrorHostKeyChanged:
		return ProviderErrorHostKeyChanged, true
	case ProviderErrorProtocol:
		return ProviderErrorProtocol, true
	case ProviderErrorTimeout:
		return ProviderErrorTimeout, true
	case ProviderErrorCancelled:
		return ProviderErrorCancelled, true
	case ProviderErrorFaulted:
		return ProviderErrorFaulted, true
	default:
		return ProviderErrorProtocol, false
	}
}

// ErrProviderUnsupported reports a call outside the provider's declared
// capability set. The host rejects it before reaching the guest.
var ErrProviderUnsupported = errors.New("provider: operation is not supported")

// ProviderAuditReason formats a stable provider audit reason. It carries names
// and a result only, never connection payload or credential content.
func ProviderAuditReason(action, result string) string {
	if result == "" {
		return "provider." + action
	}
	return "provider." + action + ":" + result
}
