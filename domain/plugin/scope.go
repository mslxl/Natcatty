package plugin

import (
	"fmt"
	"regexp"
	"strings"
)

// ScopeKind names the resource dimension a permission grant is bound to.
type ScopeKind string

const (
	// ScopeNone is the unscoped grant used by contribution, UI, and clipboard
	// permissions.
	ScopeNone ScopeKind = ""
	// ScopeHost binds a grant to one opaque Host ID.
	ScopeHost ScopeKind = "host"
	// ScopeSession binds a grant to one opaque Session ID. sessions.read may
	// also grant Host-wide access by naming only a Host; the System panel
	// snapshot service uses that form. Every other session permission still
	// requires a Session ID.
	ScopeSession ScopeKind = "session"
	// ScopeVault binds a grant to a field ID within one Host.
	ScopeVault ScopeKind = "vault"
	// ScopeFiles binds a grant to one canonical directory; the grant implies
	// that directory's subtree and nothing else.
	ScopeFiles ScopeKind = "files"
	// ScopeNetwork binds a grant to one exact endpoint.
	ScopeNetwork ScopeKind = "network"
	// ScopeEvents binds a grant to one event type name.
	ScopeEvents ScopeKind = "events"
	// ScopeSettings binds a read grant to one settings key.
	ScopeSettings ScopeKind = "settings"
	// ScopeProvider binds a provider grant to one plugin ID and access mode.
	ScopeProvider ScopeKind = "provider"
	// ScopeServices binds a service grant to a service, provider, version
	// range, and access class.
	ScopeServices ScopeKind = "services"
)

// Access modes for data providers. The user chooses the mode; a plugin cannot
// raise it.
const (
	AccessReadOnly  = "read-only"
	AccessReadWrite = "read-write"
	AccessFull      = "full"
)

// Service access classes.
const (
	ServiceAccessRead  = "read"
	ServiceAccessWrite = "write"
)

// Network transport protocols a scoped grant may name.
const (
	NetworkProtocolTCP = "tcp"
	NetworkProtocolTLS = "tls"
)

// TLS policies a network grant may name.
const (
	TLSPolicyVerify = "verify"
	TLSPolicySystem = "system"
	TLSPolicyNone   = "none"
)

// Scope is a structured, wildcard-free resource binding. It is a value type so
// grants can be compared and stored without shared mutable state.
type Scope struct {
	Kind     ScopeKind `toml:"-" json:"kind"`
	Host     string    `toml:"host" json:"host,omitempty"`
	Session  string    `toml:"session" json:"session,omitempty"`
	Field    string    `toml:"field" json:"field,omitempty"`
	Event    string    `toml:"event" json:"event,omitempty"`
	Path     string    `toml:"path" json:"path,omitempty"`
	Key      string    `toml:"key" json:"key,omitempty"`
	Provider string    `toml:"provider" json:"provider,omitempty"`
	Access   string    `toml:"access" json:"access,omitempty"`
	Ports    []int     `toml:"ports" json:"ports,omitempty"`
	Protocol string    `toml:"protocol" json:"protocol,omitempty"`
	TLS      string    `toml:"tls" json:"tls,omitempty"`
	Service  string    `toml:"service" json:"service,omitempty"`
	Version  string    `toml:"version" json:"version,omitempty"`
}

// IsZero reports whether the scope is the unscoped zero value.
func (s Scope) IsZero() bool {
	return s.Kind == ScopeNone && s.Host == "" && s.Session == "" && s.Field == "" &&
		s.Event == "" && s.Path == "" && s.Key == "" && s.Provider == "" &&
		s.Access == "" && len(s.Ports) == 0 && s.Protocol == "" && s.TLS == "" &&
		s.Service == "" && s.Version == ""
}

// IsServicePrefix reports whether a services scope names the provider's own
// namespace prefix ("plugin.<plugin-id>.") rather than one exact service. The
// trailing dot is the prefix marker; an exact service name never ends in one.
func (s Scope) IsServicePrefix() bool {
	return s.Kind == ScopeServices && strings.HasSuffix(s.Service, ".")
}

// Equal reports whether two scopes bind the same resource.
func (s Scope) Equal(other Scope) bool {
	if s.Kind != other.Kind || s.Host != other.Host || s.Session != other.Session ||
		s.Field != other.Field || s.Event != other.Event || s.Path != other.Path ||
		s.Key != other.Key || s.Provider != other.Provider || s.Access != other.Access ||
		s.Protocol != other.Protocol || s.TLS != other.TLS || s.Service != other.Service ||
		s.Version != other.Version || len(s.Ports) != len(other.Ports) {
		return false
	}
	for i := range s.Ports {
		if s.Ports[i] != other.Ports[i] {
			return false
		}
	}
	return true
}

var (
	pathHasWildcard = regexp.MustCompile(`[*?\[\]{}]`)
	windowsAbsPath  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// Validate checks a scope against its kind. It rejects wildcards, relative or
// escaping paths, empty resources, and unknown access classes.
func (s Scope) Validate() error {
	if s.Kind == ScopeNone {
		if !s.IsZero() {
			return fmt.Errorf("a permission without a resource scope declares no scope fields")
		}
		return nil
	}
	if err := s.validateKindFields(); err != nil {
		return err
	}
	if err := checkNoWildcard("host", s.Host); err != nil {
		return err
	}
	if err := checkNoWildcard("session", s.Session); err != nil {
		return err
	}
	if err := checkNoWildcard("field", s.Field); err != nil {
		return err
	}
	if err := checkNoWildcard("event", s.Event); err != nil {
		return err
	}
	if err := checkNoWildcard("key", s.Key); err != nil {
		return err
	}
	if err := checkNoWildcard("provider", s.Provider); err != nil {
		return err
	}
	if err := checkNoWildcard("service", s.Service); err != nil {
		return err
	}

	switch s.Kind {
	case ScopeHost:
		if strings.TrimSpace(s.Host) == "" {
			return fmt.Errorf("host scope requires a Host ID")
		}
	case ScopeSession:
		if strings.TrimSpace(s.Session) == "" && strings.TrimSpace(s.Host) == "" {
			return fmt.Errorf("session scope requires a Session ID or a Host ID")
		}
		if strings.TrimSpace(s.Session) != "" && strings.TrimSpace(s.Host) != "" {
			return fmt.Errorf("session scope binds either one Session or one Host, not both")
		}
	case ScopeVault:
		if strings.TrimSpace(s.Host) == "" || strings.TrimSpace(s.Field) == "" {
			return fmt.Errorf("vault scope requires a Host ID and a field ID")
		}
	case ScopeFiles:
		if err := validateScopePath(s.Path); err != nil {
			return err
		}
	case ScopeNetwork:
		if strings.TrimSpace(s.Host) == "" {
			return fmt.Errorf("network scope requires an exact host")
		}
		if len(s.Ports) == 0 {
			return fmt.Errorf("network scope requires at least one port")
		}
		for _, port := range s.Ports {
			if port < 1 || port > 65535 {
				return fmt.Errorf("network scope port %d is outside 1-65535", port)
			}
		}
		switch s.Protocol {
		case NetworkProtocolTCP:
			if s.TLS != "" && s.TLS != TLSPolicyNone {
				return fmt.Errorf("tcp scope cannot declare TLS policy %q", s.TLS)
			}
		case NetworkProtocolTLS:
			switch s.TLS {
			case TLSPolicyVerify, TLSPolicySystem, TLSPolicyNone:
			default:
				return fmt.Errorf("tls scope requires a TLS policy of verify, system, or none")
			}
		default:
			return fmt.Errorf("network scope protocol must be tcp or tls")
		}
	case ScopeEvents:
		if strings.TrimSpace(s.Event) == "" {
			return fmt.Errorf("events scope requires an event type name")
		}
	case ScopeSettings:
		if strings.TrimSpace(s.Key) == "" {
			return fmt.Errorf("settings scope requires a settings key")
		}
	case ScopeProvider:
		if strings.TrimSpace(s.Provider) == "" {
			return fmt.Errorf("provider scope requires a provider plugin ID")
		}
		switch s.Access {
		case AccessReadOnly, AccessReadWrite, AccessFull:
		default:
			return fmt.Errorf("provider scope access must be read-only, read-write, or full")
		}
	case ScopeServices:
		if strings.TrimSpace(s.Service) == "" {
			return fmt.Errorf("services scope requires a service name")
		}
		switch s.Access {
		case ServiceAccessRead, ServiceAccessWrite:
		default:
			return fmt.Errorf("services scope access must be read or write")
		}
		if s.Version != "" && !ValidRange(s.Version) {
			return fmt.Errorf("services scope version %q is not a supported semver range", s.Version)
		}
	default:
		return fmt.Errorf("unknown scope kind %q", s.Kind)
	}
	return nil
}

// validateKindFields rejects fields that do not belong to the scope kind, so a
// host grant cannot smuggle a path and a session grant cannot carry a port.
func (s Scope) validateKindFields() error {
	fields := []struct {
		name    string
		value   string
		allowed bool
	}{
		{"host", s.Host, s.Kind == ScopeHost || s.Kind == ScopeVault ||
			s.Kind == ScopeNetwork || s.Kind == ScopeSession},
		{"session", s.Session, s.Kind == ScopeSession},
		{"field", s.Field, s.Kind == ScopeVault},
		{"event", s.Event, s.Kind == ScopeEvents},
		{"path", s.Path, s.Kind == ScopeFiles},
		{"key", s.Key, s.Kind == ScopeSettings},
		{"provider", s.Provider, s.Kind == ScopeProvider || s.Kind == ScopeServices},
		{"access", s.Access, s.Kind == ScopeProvider || s.Kind == ScopeServices},
		{"protocol", s.Protocol, s.Kind == ScopeNetwork},
		{"tls", s.TLS, s.Kind == ScopeNetwork},
		{"service", s.Service, s.Kind == ScopeServices},
		{"version", s.Version, s.Kind == ScopeServices},
	}
	for _, field := range fields {
		if field.value != "" && !field.allowed {
			return fmt.Errorf("%s scope does not take field %q", s.Kind, field.name)
		}
	}
	if len(s.Ports) > 0 && s.Kind != ScopeNetwork {
		return fmt.Errorf("%s scope does not take ports", s.Kind)
	}
	return nil
}

func checkNoWildcard(field, value string) error {
	if value == "" {
		return nil
	}
	if strings.Contains(value, "*") || strings.Contains(value, "?") {
		return fmt.Errorf("%s %q is a wildcard; activation wildcards are not allowed", field, value)
	}
	return nil
}

func validateScopePath(raw string) error {
	path := strings.TrimSpace(raw)
	if path == "" {
		return fmt.Errorf("files scope requires a directory path")
	}
	if pathHasWildcard.MatchString(path) {
		return fmt.Errorf("files scope path %q contains a glob", raw)
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("files scope path contains a NUL byte")
	}
	if !strings.HasPrefix(path, "/") && !windowsAbsPath.MatchString(path) {
		return fmt.Errorf("files scope path %q must be absolute", raw)
	}
	normalized := strings.ReplaceAll(path, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." || segment == "." {
			return fmt.Errorf("files scope path %q must be canonical and may not contain %q", raw, segment)
		}
	}
	return nil
}
