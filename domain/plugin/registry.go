package plugin

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Channel is a registry release channel. Stable is the default; beta is
// opt-in per plugin.
type Channel string

const (
	// ChannelStable is the default release channel.
	ChannelStable Channel = "stable"
	// ChannelBeta carries prerelease and opt-in builds.
	ChannelBeta Channel = "beta"
)

// Valid reports whether c is a defined channel.
func (c Channel) Valid() bool { return c == ChannelStable || c == ChannelBeta }

// Allows reports whether a version on channel other may be selected when the
// user opted into c. A stable subscriber never sees beta builds; a beta
// subscriber still receives stable releases.
func (c Channel) Allows(other Channel) bool {
	switch c {
	case ChannelBeta:
		return other == ChannelStable || other == ChannelBeta
	case ChannelStable:
		return other == ChannelStable
	default:
		return false
	}
}

// ReleaseWarningKind is one reason a registry version is flagged.
type ReleaseWarningKind string

const (
	ReleaseWarningYanked     ReleaseWarningKind = "yanked"
	ReleaseWarningDeprecated ReleaseWarningKind = "deprecated"
	ReleaseWarningRevoked    ReleaseWarningKind = "revoked"
)

// ReleaseWarning is a registry notice attached to one version. Yanked hides a
// version from new installs and updates; deprecated warns; revoked warns
// strongly but never uninstalls an existing package automatically.
type ReleaseWarning struct {
	Kind   ReleaseWarningKind
	Detail string
	Strong bool
}

// RegistryVersion is one version entry from a plugin's registry metadata.
type RegistryVersion struct {
	Version      string       `json:"version"`
	Channel      Channel      `json:"channel"`
	PluginAPI    string       `json:"plugin_api"`
	ABI          int          `json:"abi"`
	Package      string       `json:"package"`
	SHA256       string       `json:"sha256"`
	Size         int64        `json:"size"`
	Required     []string     `json:"required,omitempty"`
	Optional     []string     `json:"optional,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	Yanked       bool         `json:"yanked,omitempty"`
	Deprecated   string       `json:"deprecated,omitempty"`
	Revoked      string       `json:"revoked,omitempty"`
}

// Validate checks the registry metadata for one version. Package is a
// registry-relative path; absolute paths, traversal, and backslashes are
// rejected before any fetch.
func (v RegistryVersion) Validate() error {
	parsed, ok := ParseSemver(v.Version)
	if !ok {
		return fmt.Errorf("plugin registry: version %q is not a SemVer version", v.Version)
	}
	if !v.Channel.Valid() {
		return fmt.Errorf("plugin registry: version %s has invalid channel %q", v.Version, v.Channel)
	}
	if v.Channel == ChannelStable && parsed.Pre != "" {
		return fmt.Errorf(
			"plugin registry: version %s is a prerelease and must use the beta channel",
			v.Version)
	}
	if !ValidRange(v.PluginAPI) {
		return fmt.Errorf("plugin registry: version %s plugin_api %q is not a range", v.Version, v.PluginAPI)
	}
	if v.ABI <= 0 {
		return fmt.Errorf("plugin registry: version %s abi %d is invalid", v.Version, v.ABI)
	}
	if err := checkRegistryPackagePath(v.Package); err != nil {
		return fmt.Errorf("plugin registry: version %s package: %w", v.Version, err)
	}
	if !sha256Pattern.MatchString(strings.ToLower(v.SHA256)) {
		return fmt.Errorf("plugin registry: version %s sha256 must be 64 hexadecimal characters", v.Version)
	}
	if v.Size < 0 {
		return fmt.Errorf("plugin registry: version %s size must not be negative", v.Version)
	}
	for _, name := range append(append([]string(nil), v.Required...), v.Optional...) {
		if _, ok := PermissionByName(name); !ok {
			return fmt.Errorf("plugin registry: version %s declares unknown permission %q", v.Version, name)
		}
	}
	for _, dep := range v.Dependencies {
		if err := dep.Validate(); err != nil {
			return fmt.Errorf("plugin registry: version %s: %w", v.Version, err)
		}
	}
	return nil
}

func checkRegistryPackagePath(name string) error {
	if name == "" {
		return fmt.Errorf("is required")
	}
	if strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%q must use forward slashes", name)
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || clean == ".." ||
		path.IsAbs(name) || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q must be a clean relative path", name)
	}
	return nil
}

// Compatible reports whether this host can run the version. A registry may
// list versions for future hosts; selection skips them.
func (v RegistryVersion) Compatible() bool {
	return SupportsABI(v.ABI) && PluginAPIMatches(v.PluginAPI)
}

// Selectable reports whether a new install or update may pick the version.
// Yanked versions stay running for existing installs but leave selection.
func (v RegistryVersion) Selectable() bool { return !v.Yanked }

// Warnings returns the registry notices for the version in a stable order.
func (v RegistryVersion) Warnings() []ReleaseWarning {
	var out []ReleaseWarning
	if v.Yanked {
		out = append(out, ReleaseWarning{Kind: ReleaseWarningYanked})
	}
	if v.Deprecated != "" {
		out = append(out, ReleaseWarning{
			Kind: ReleaseWarningDeprecated, Detail: v.Deprecated,
		})
	}
	if v.Revoked != "" {
		out = append(out, ReleaseWarning{
			Kind: ReleaseWarningRevoked, Detail: v.Revoked, Strong: true,
		})
	}
	return out
}

// RegistryPlugin is the full version metadata file for one plugin id.
type RegistryPlugin struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Versions    []RegistryVersion `json:"versions"`
}

// Validate checks the plugin identity and every version entry.
func (p RegistryPlugin) Validate() error {
	if err := validatePluginID(p.ID); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	if len(p.Versions) == 0 {
		return fmt.Errorf("plugin registry: %s declares no versions", p.ID)
	}
	seen := make(map[string]bool, len(p.Versions))
	for _, version := range p.Versions {
		if err := version.Validate(); err != nil {
			return err
		}
		if seen[version.Version] {
			return fmt.Errorf("plugin registry: %s declares version %s twice", p.ID, version.Version)
		}
		seen[version.Version] = true
	}
	return nil
}

// Select picks a version for install or update. An explicit version must exist,
// be compatible with this host, be on a channel the user opted into, and not be
// yanked. An empty version picks the highest compatible, non-yanked version on
// an allowed channel.
func (p RegistryPlugin) Select(version string, channel Channel) (RegistryVersion, bool) {
	if !channel.Valid() {
		channel = ChannelStable
	}
	if version != "" {
		for _, candidate := range p.Versions {
			if candidate.Version != version {
				continue
			}
			if !candidate.Selectable() || !candidate.Compatible() || !channel.Allows(candidate.Channel) {
				return RegistryVersion{}, false
			}
			return candidate, true
		}
		return RegistryVersion{}, false
	}
	var best RegistryVersion
	var bestSemver Semver
	found := false
	for _, candidate := range p.Versions {
		if !candidate.Selectable() || !candidate.Compatible() || !channel.Allows(candidate.Channel) {
			continue
		}
		parsed, ok := ParseSemver(candidate.Version)
		if !ok {
			continue
		}
		if !found || parsed.Compare(bestSemver) > 0 {
			best = candidate
			bestSemver = parsed
			found = true
		}
	}
	return best, found
}

// RegistryPluginSummary is one searchable index row. The client searches the
// index locally; there is no server query API.
type RegistryPluginSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
}

// RegistryIndex is the static registry index.
type RegistryIndex struct {
	Schema  int                     `json:"schema"`
	Name    string                  `json:"name,omitempty"`
	Plugins []RegistryPluginSummary `json:"plugins"`
}

// Validate checks the index shape and unique plugin ids.
func (i RegistryIndex) Validate() error {
	if i.Schema != 1 {
		return fmt.Errorf("plugin registry: unsupported index schema %d", i.Schema)
	}
	seen := make(map[string]bool, len(i.Plugins))
	for _, entry := range i.Plugins {
		if err := validatePluginID(entry.ID); err != nil {
			return fmt.Errorf("plugin registry: %w", err)
		}
		if seen[entry.ID] {
			return fmt.Errorf("plugin registry: index lists %s twice", entry.ID)
		}
		seen[entry.ID] = true
	}
	return nil
}

// SearchRegistryIndex filters the index client-side. The query is matched
// case-insensitively against the plugin id, display name, description, and
// keywords. Results are sorted by id and never expose map order.
func SearchRegistryIndex(index RegistryIndex, query string) []RegistryPluginSummary {
	needle := strings.ToLower(strings.TrimSpace(query))
	var out []RegistryPluginSummary
	for _, entry := range index.Plugins {
		if needle == "" || summaryMatches(entry, needle) {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func summaryMatches(entry RegistryPluginSummary, needle string) bool {
	fields := []string{entry.ID, entry.Name, entry.Description}
	fields = append(fields, entry.Keywords...)
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}
