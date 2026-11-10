package plugin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxServiceNameLength bounds a plugin service name. Service names are host
// broker identities, not user content.
const MaxServiceNameLength = 128

var groupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Group is a loader namespace, a default-configuration boundary, and an
// isolate scope. It is not a fiber: instances loaded in a group get the
// identity "group/id#instance", and two instances of one manifest entry are
// allowed. A group never creates a process, goroutine, or guest instance.
type Group struct {
	// Name is the namespace used in fiber identities. It must not contain the
	// '/' or '#' separators.
	Name string `json:"name" toml:"name"`
	// Parent is the enclosing group, empty for a root group. Isolate-scoped
	// service resolution walks parents from the consumer outward.
	Parent string `json:"parent,omitempty" toml:"parent"`
	// Defaults maps a plugin id to the configuration defaults inherited by
	// instances loaded directly in this group. A manifest-declared default and
	// a stored value both win over a group default.
	Defaults map[string]map[string]any `json:"defaults,omitempty" toml:"defaults"`
}

// Validate checks the group name, parent reference, and default keys. The
// parent chain itself is validated by the container because it owns the
// registry.
func (g Group) Validate() error {
	if !groupNamePattern.MatchString(g.Name) {
		return fmt.Errorf("plugin group: name %q must be lowercase letters, digits, dot, underscore, or hyphen, at most 64 characters", g.Name)
	}
	if g.Parent != "" {
		if !groupNamePattern.MatchString(g.Parent) {
			return fmt.Errorf("plugin group %s: parent %q is not a valid group name", g.Name, g.Parent)
		}
		if g.Parent == g.Name {
			return fmt.Errorf("plugin group %s: parent is itself", g.Name)
		}
	}
	for id := range g.Defaults {
		if err := validatePluginID(id); err != nil {
			return fmt.Errorf("plugin group %s: default for %q: %w", g.Name, id, err)
		}
	}
	return nil
}

// DefaultsFor returns the inherited defaults for one plugin id. The map is a
// copy, so callers cannot mutate the group through the returned value.
func (g Group) DefaultsFor(pluginID string) map[string]any {
	values := g.Defaults[pluginID]
	if len(values) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = CloneSettingValue(value)
	}
	return out
}

// Injection declares one service a plugin needs before it can become ACTIVE.
// A required injection that does not resolve leaves the fiber PENDING with a
// reason; an optional injection lets the plugin start unbound.
type Injection struct {
	Service  string `json:"service" toml:"service"`
	Version  string `json:"version,omitempty" toml:"version"`
	Optional bool   `json:"optional,omitempty" toml:"optional"`
}

// EffectiveRange returns the semver range a consumer accepts, defaulting to
// any version when the manifest omits one.
func (i Injection) EffectiveRange() string {
	if strings.TrimSpace(i.Version) == "" {
		return "*"
	}
	return i.Version
}

// RequiredInjections returns the non-optional service names in manifest
// order, without a copy of the optional rows.
func (m Manifest) RequiredInjections() []string {
	out := make([]string, 0, len(m.Inject))
	for _, injection := range m.Inject {
		if !injection.Optional {
			out = append(out, injection.Service)
		}
	}
	return out
}

// InjectionReason formats the stable PENDING reason for missing injected
// services. The services are sorted so the reason is deterministic.
func InjectionReason(services []string) string {
	names := append([]string(nil), services...)
	sort.Strings(names)
	if len(names) == 0 {
		return ReasonInjectionMissing
	}
	return ReasonInjectionMissing + ": " + strings.Join(names, ",")
}

// ParseInjectionReason returns the missing service names encoded in a reason
// produced by InjectionReason.
func ParseInjectionReason(reason string) ([]string, bool) {
	if reason == ReasonInjectionMissing {
		return nil, true
	}
	rest, ok := strings.CutPrefix(reason, ReasonInjectionMissing+": ")
	if !ok || rest == "" {
		return nil, false
	}
	return strings.Split(rest, ","), true
}
