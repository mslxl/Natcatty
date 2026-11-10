package plugin

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/mslxl/natcatty/domain/model/plugindata"
)

// CurrentManifestVersion is the manifest schema this host understands.
const CurrentManifestVersion = 1

// SupportedABIs is the set of wire ABI major versions this host accepts. The
// spec allows the current and previous major; ABI 1 is the first published
// version, so there is no predecessor yet.
var SupportedABIs = []int{1}

// SupportsABI reports whether the host can run a module declaring abi.
func SupportsABI(abi int) bool {
	for _, supported := range SupportedABIs {
		if supported == abi {
			return true
		}
	}
	return false
}

// MinMemoryMaxMiB is the smallest explicit module memory maximum the host
// accepts. A zero value in the manifest means the default.
const MinMemoryMaxMiB = 1

// DefaultMemoryMaxMiB is the host's default module memory cap. It applies
// when the manifest omits wasm.memory_max_mib.
const DefaultMemoryMaxMiB = 64

// MaxMemoryMaxMiB is the hard cap for a module's declared maximum.
const MaxMemoryMaxMiB = 256

// Manifest is the declarative plugin description decoded from plugin.toml.
// Struct tags are inert; the TOML decoder lives in adapters/pluginmanifest,
// keeping this package free of third-party dependencies.
type Manifest struct {
	ManifestVersion int    `toml:"manifest_version"`
	ID              string `toml:"id"`
	Name            string `toml:"name"`
	Version         string `toml:"version"`
	PluginAPI       string `toml:"plugin_api"`
	ABI             int    `toml:"abi"`
	DefaultLocale   string `toml:"default_locale"`
	Description     string `toml:"description"`
	Author          string `toml:"author"`
	Homepage        string `toml:"homepage"`
	License         string `toml:"license"`

	Wasm          Wasm           `toml:"wasm"`
	Permissions   Permissions    `toml:"permissions"`
	Dependencies  []Dependency   `toml:"dependencies"`
	Contributions Contributions  `toml:"contributions"`
	Settings      SettingsSchema `toml:"settings"`
	// PluginDataSchemaVersion is the schema version this plugin declares for
	// the host-opaque namespaces it owns on Hosts and DataProfiles. Zero means
	// version 1. A namespace carrying a newer version is dormant and read-only
	// instead of being migrated automatically.
	PluginDataSchemaVersion int `toml:"plugin_data_schema_version" json:"pluginDataSchemaVersion,omitempty"`
	// Inject declares services the plugin consumes. A required injection keeps
	// the fiber PENDING until a provider is ACTIVE in an ancestor isolate
	// scope.
	Inject []Injection `toml:"inject" json:"inject,omitempty"`
	// Provides declares services the plugin offers under its own namespace.
	Provides []Provision `toml:"provides" json:"provides,omitempty"`
}

// EffectivePluginDataSchemaVersion returns the declared namespace schema
// version with the version-1 default applied.
func (m Manifest) EffectivePluginDataSchemaVersion() int {
	return plugindata.EffectiveSchemaVersion(m.PluginDataSchemaVersion)
}

// Wasm describes the module inside the package.
type Wasm struct {
	Path         string `toml:"path"`
	SHA256       string `toml:"sha256"`
	MemoryMaxMiB int    `toml:"memory_max_mib"`
	Toolchain    string `toml:"toolchain"`
}

// EffectiveMemoryMaxMiB returns the declared maximum, or the host default
// when the manifest omitted it.
func (w Wasm) EffectiveMemoryMaxMiB() int {
	if w.MemoryMaxMiB == 0 {
		return DefaultMemoryMaxMiB
	}
	return w.MemoryMaxMiB
}

// EffectiveMemoryMaxBytes returns the declared maximum in bytes, with the
// host default applied. Call Validate before relying on it: an out-of-range
// declaration is returned unchanged so the lower layers can reject it.
func (w Wasm) EffectiveMemoryMaxBytes() int64 {
	return int64(w.EffectiveMemoryMaxMiB()) << 20
}

// Permissions lists requested permissions. Names come from the closed
// vocabulary; Scopes binds every resource-scoped permission to a structured,
// wildcard-free scope. Optional permissions omitted from the enable consent
// may be requested on first use for that declared scope and remembered there.
type Permissions struct {
	Required []string         `toml:"required"`
	Optional []string         `toml:"optional"`
	Scopes   map[string]Scope `toml:"scopes"`
}

// Requests validates the requested permission names and declared scopes and
// returns the typed consent model. Unknown names are fatal.
func (p Permissions) Requests() (required, optional []PermissionRequest, err error) {
	required, err = permissionRequests("required", p.Required, p.Scopes)
	if err != nil {
		return nil, nil, err
	}
	optional, err = permissionRequests("optional", p.Optional, p.Scopes)
	if err != nil {
		return nil, nil, err
	}
	for name := range p.Scopes {
		declared := false
		for _, request := range required {
			if string(request.Permission) == name {
				declared = true
				break
			}
		}
		for _, request := range optional {
			if string(request.Permission) == name {
				declared = true
				break
			}
		}
		if !declared {
			return nil, nil, invalid("permissions.scopes."+name,
				"scope declared for a permission that is neither required nor optional")
		}
	}
	if err := ValidatePermissionRequests(required, optional); err != nil {
		return nil, nil, err
	}
	return required, optional, nil
}

func permissionRequests(list string, names []string, scopes map[string]Scope) ([]PermissionRequest, error) {
	requests := make([]PermissionRequest, 0, len(names))
	for i, name := range names {
		info, ok := PermissionByName(name)
		if !ok {
			return nil, invalid(fmt.Sprintf("permissions.%s[%d]", list, i),
				"unknown permission %q", name)
		}
		request := PermissionRequest{Permission: info.Permission}
		if scope, ok := scopes[name]; ok {
			scope.Kind = info.Scope
			request.Scope = scope
		}
		requests = append(requests, request)
	}
	return requests, nil
}

// ValidationError reports one invalid manifest field with a readable reason.
type ValidationError struct {
	Field   string
	Problem string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return "plugin manifest: " + e.Problem
	}
	return "plugin manifest: " + e.Field + ": " + e.Problem
}

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Problem: fmt.Sprintf(format, args...)}
}

var (
	pluginIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	commandIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pluginIDErrorText  = "must be lowercase letters, digits, dot, underscore, or hyphen, at most 64 characters"
	commandIDErrorText = "must be lowercase letters, digits, dot, underscore, or hyphen, at most 64 characters"
)

// Validate checks identity, version, Plugin API, ABI, module path, memory
// maximum, the closed permission vocabulary with its declared scopes, and the
// command/menu/keybinding contributions. Package integrity is checked by the
// installer.
func (m Manifest) Validate() error {
	return m.validate(true)
}

// ValidateBuiltin checks the same manifest shape for a compiled-in first-party
// plugin. Identity, Plugin API compatibility, permissions, dependencies, and
// contributions use exactly the third-party rules. The wire ABI and the module
// path and hash are not required: a built-in Setup is a direct Go call, so no
// module crosses the WASM ABI. A module path that is present is still validated
// so a manifest copied from a package cannot hide a traversal.
func (m Manifest) ValidateBuiltin() error {
	return m.validate(false)
}

func (m Manifest) validate(requireModule bool) error {
	if m.ManifestVersion != CurrentManifestVersion {
		return invalid("manifest_version", "want %d, got %d", CurrentManifestVersion, m.ManifestVersion)
	}
	if m.ID == "" {
		return invalid("id", "is required")
	}
	if err := validatePluginID(m.ID); err != nil {
		return err
	}
	if strings.TrimSpace(m.Name) == "" {
		return invalid("name", "is required")
	}
	if _, ok := ParseSemver(m.Version); !ok {
		return invalid("version", "%q is not a SemVer 2.0.0 version", m.Version)
	}
	if m.PluginAPI == "" {
		return invalid("plugin_api", "is required")
	}
	if !PluginAPIMatches(m.PluginAPI) {
		return invalid("plugin_api", "%q does not include host Plugin API %s", m.PluginAPI, HostPluginAPIVersion)
	}
	if requireModule {
		if !SupportsABI(m.ABI) {
			return invalid("abi", "%d is not supported; host supports %v", m.ABI, SupportedABIs)
		}
	}
	if m.PluginDataSchemaVersion < 0 || m.PluginDataSchemaVersion > plugindata.MaxSchemaVersion {
		return invalid("plugin_data_schema_version",
			"must be between 1 and %d, or 0 for version 1", plugindata.MaxSchemaVersion)
	}
	if strings.TrimSpace(m.DefaultLocale) == "" {
		return invalid("default_locale", "is required")
	}
	if !ValidLocaleTag(m.DefaultLocale) {
		return invalid("default_locale", "%q is not a BCP 47 language tag", m.DefaultLocale)
	}
	if requireModule {
		if err := m.Wasm.validate(); err != nil {
			return err
		}
	} else if m.Wasm.Path != "" {
		if err := m.Wasm.validate(); err != nil {
			return err
		}
	}
	required, optional, err := m.Permissions.Requests()
	if err != nil {
		return err
	}
	if len(m.Contributions.SystemPanel) > 0 && !hasRequestedPermission(required, optional, PermissionSystemPanel) {
		return invalid("permissions", "system_panel contributions require the %q permission", PermissionSystemPanel)
	}
	if len(m.Contributions.Connections) > 0 {
		if !hasRequestedPermission(required, optional, PermissionProviderConnection) {
			return invalid("permissions", "connection contributions require the %q permission", PermissionProviderConnection)
		}
		// The grant authorizes this plugin to serve its own protocols. A scope
		// naming another plugin would let one plugin expose authority it does
		// not own, so it is rejected at install.
		scope := m.Permissions.Scopes[string(PermissionProviderConnection)]
		if scope.Provider != m.ID {
			return invalid("permissions.scopes."+string(PermissionProviderConnection),
				"must name this plugin ID %q", m.ID)
		}
	}
	if len(m.Contributions.DataProviders) > 0 {
		if !hasRequestedPermission(required, optional, PermissionProviderData) {
			return invalid("permissions", "data provider contributions require the %q permission", PermissionProviderData)
		}
		// The grant authorizes this plugin to serve its own data providers and
		// caps the access mode a profile may select. A scope naming another
		// plugin, or one without a valid access mode, is rejected at install.
		scope := m.Permissions.Scopes[string(PermissionProviderData)]
		if scope.Provider != m.ID {
			return invalid("permissions.scopes."+string(PermissionProviderData),
				"must name this plugin ID %q", m.ID)
		}
		if !DataAccessMode(scope.Access).Valid() {
			return invalid("permissions.scopes."+string(PermissionProviderData)+".access",
				"must be read-only, read-write, or full")
		}
	}
	if err := m.validateDependencies(); err != nil {
		return err
	}
	if err := m.Contributions.validate(m.ID, m.Permissions); err != nil {
		return err
	}
	if err := ValidateInjections(m.Inject); err != nil {
		return invalid("inject", "%v", err)
	}
	if err := ValidateProvisions(m.ID, m.Provides); err != nil {
		return invalid("provides", "%v", err)
	}
	if err := m.validateMCPTools(); err != nil {
		return err
	}
	return m.Settings.Validate()
}

// validateMCPTools applies the static tool declaration rules and requires the
// mcp.tools permission whenever a tool is declared. A manifest may request the
// permission without declaring tools; it then simply exposes none.
func (m Manifest) validateMCPTools() error {
	if len(m.Contributions.MCPTools) == 0 {
		return nil
	}
	declared := false
	for _, name := range m.Permissions.Required {
		if Permission(name) == PermissionMCPTools {
			declared = true
			break
		}
	}
	if !declared {
		for _, name := range m.Permissions.Optional {
			if Permission(name) == PermissionMCPTools {
				declared = true
				break
			}
		}
	}
	if !declared {
		return invalid("contributions.mcp_tools",
			"declares MCP tools but permissions.required or permissions.optional does not include %q",
			PermissionMCPTools)
	}
	seen := make(map[string]bool, len(m.Contributions.MCPTools))
	for i, tool := range m.Contributions.MCPTools {
		if err := tool.Validate(m.ID, i); err != nil {
			return err
		}
		full := QualifiedMCPToolName(m.ID, tool.Name)
		if seen[full] {
			return invalid(fmt.Sprintf("contributions.mcp_tools[%d].name", i),
				"duplicate tool %q", full)
		}
		seen[full] = true
	}
	return nil
}

func (m Manifest) validateDependencies() error {
	seen := make(map[string]bool, len(m.Dependencies))
	for _, dependency := range m.Dependencies {
		if err := dependency.Validate(); err != nil {
			return err
		}
		if dependency.ID == m.ID {
			return invalid("dependencies.id", "%q declares a dependency on itself", m.ID)
		}
		if seen[dependency.ID] {
			return invalid("dependencies.id", "%q is declared twice", dependency.ID)
		}
		seen[dependency.ID] = true
	}
	return nil
}

// validatePluginID applies the shared plugin identity rule.
func validatePluginID(id string) error {
	if id == "" {
		return invalid("id", "is required")
	}
	if !pluginIDPattern.MatchString(id) {
		return invalid("id", "%q %s", id, pluginIDErrorText)
	}
	if strings.Count(id, ".") != 1 {
		return invalid("id", "%q must contain exactly one separating dot", id)
	}
	return nil
}

func (w Wasm) validate() error {
	if w.Path == "" {
		return invalid("wasm.path", "is required")
	}
	if strings.ContainsRune(w.Path, '\\') {
		return invalid("wasm.path", "%q must use forward slashes", w.Path)
	}
	if path.IsAbs(w.Path) || strings.HasPrefix(w.Path, "/") {
		return invalid("wasm.path", "%q must be relative to the plugin directory", w.Path)
	}
	clean := path.Clean(w.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return invalid("wasm.path", "%q escapes the plugin directory", w.Path)
	}
	if !strings.HasSuffix(clean, ".wasm") {
		return invalid("wasm.path", "%q must name a .wasm module", w.Path)
	}
	if w.SHA256 == "" {
		return invalid("wasm.sha256", "is required")
	}
	if !sha256Pattern.MatchString(strings.ToLower(w.SHA256)) {
		return invalid("wasm.sha256", "must be 64 hexadecimal characters")
	}
	if w.MemoryMaxMiB < 0 {
		return invalid("wasm.memory_max_mib", "must not be negative")
	}
	if w.MemoryMaxMiB > 0 && w.MemoryMaxMiB < MinMemoryMaxMiB {
		return invalid("wasm.memory_max_mib", "%d MiB is below the %d MiB minimum", w.MemoryMaxMiB, MinMemoryMaxMiB)
	}
	if w.MemoryMaxMiB > MaxMemoryMaxMiB {
		return invalid("wasm.memory_max_mib", "%d MiB exceeds the %d MiB cap", w.MemoryMaxMiB, MaxMemoryMaxMiB)
	}
	return nil
}

func hasRequestedPermission(required, optional []PermissionRequest, permission Permission) bool {
	for _, request := range append(append([]PermissionRequest(nil), required...), optional...) {
		if request.Permission == permission {
			return true
		}
	}
	return false
}

// validateMessageKey checks an optional host-rendered catalog key reference.
func validateMessageKey(field, key string) error {
	if key == "" {
		return nil
	}
	if !ValidMessageKey(key) {
		return invalid(field, "%q does not match [a-z0-9_.-]{1,128}", key)
	}
	return nil
}

func (c Contributions) validate(pluginID string, permissions Permissions) error {
	if len(c.SystemPanel) > MaxPanelSections {
		return invalid("contributions.system_panel", "at most %d sections are allowed", MaxPanelSections)
	}
	panels := make(map[string]bool, len(c.SystemPanel))
	for i, section := range c.SystemPanel {
		field := fmt.Sprintf("contributions.system_panel[%d]", i)
		if err := ValidatePanelSection(section); err != nil {
			return invalid(field, "%v", err)
		}
		if panels[section.ID] {
			return invalid(field+".id", "duplicate section %q", section.ID)
		}
		panels[section.ID] = true
	}
	commands := make(map[string]bool, len(c.Commands))
	for i, command := range c.Commands {
		field := fmt.Sprintf("contributions.commands[%d]", i)
		if !commandIDPattern.MatchString(command.ID) {
			return invalid(field+".id", "%q %s", command.ID, commandIDErrorText)
		}
		if commands[command.ID] {
			return invalid(field+".id", "duplicate command %q", command.ID)
		}
		commands[command.ID] = true
		if err := validateMessageKey(field+".title_key", command.TitleKey); err != nil {
			return err
		}
		if strings.TrimSpace(command.Title) == "" && command.TitleKey == "" {
			return invalid(field+".title", "is required unless title_key is set")
		}
	}
	for i, menu := range c.Menus {
		field := fmt.Sprintf("contributions.menus[%d]", i)
		if !commands[menu.Command] {
			return invalid(field+".command", "%q is not a declared command", menu.Command)
		}
		if err := validateMessageKey(field+".label_key", menu.LabelKey); err != nil {
			return err
		}
		if strings.TrimSpace(menu.Label) == "" && menu.LabelKey == "" {
			return invalid(field+".label", "is required unless label_key is set")
		}
	}
	for i, binding := range c.Keybindings {
		field := fmt.Sprintf("contributions.keybindings[%d]", i)
		if !commands[binding.Command] {
			return invalid(field+".command", "%q is not a declared command", binding.Command)
		}
		if strings.TrimSpace(binding.Accelerator) == "" {
			return invalid(field+".accelerator", "is required")
		}
		if len(binding.Accelerator) > 64 {
			return invalid(field+".accelerator", "must be at most 64 characters")
		}
	}
	if err := c.validateViews(permissions); err != nil {
		return err
	}
	if len(c.Connections) > MaxConnectionProviders {
		return invalid("contributions.connections",
			"at most %d protocols are allowed", MaxConnectionProviders)
	}
	connections := make(map[string]bool, len(c.Connections))
	for i, provider := range c.Connections {
		if err := ValidateConnectionProvider(pluginID, provider, i); err != nil {
			return err
		}
		if connections[provider.Protocol] {
			return invalid(fmt.Sprintf("contributions.connections[%d].protocol", i),
				"duplicate protocol %q", provider.Protocol)
		}
		connections[provider.Protocol] = true
	}
	if len(c.DataProviders) > MaxDataProviders {
		return invalid("contributions.data_providers",
			"at most %d providers are allowed", MaxDataProviders)
	}
	dataProviders := make(map[string]bool, len(c.DataProviders))
	for i, provider := range c.DataProviders {
		if err := ValidateDataProvider(pluginID, provider, i); err != nil {
			return err
		}
		qualified := QualifiedDataProviderID(pluginID, provider.ID)
		if dataProviders[qualified] {
			return invalid(fmt.Sprintf("contributions.data_providers[%d].id", i),
				"duplicate provider %q", provider.ID)
		}
		dataProviders[qualified] = true
	}
	return validateLanguageProviders(permissions, pluginID, c.Language.Providers)
}

// QualifiedCommandID returns the host command ID for a manifest-local
// command id. The "plugin." prefix keeps plugin commands disjoint from the
// built-in command catalog.
func QualifiedCommandID(pluginID, localID string) string {
	return "plugin." + pluginID + "." + localID
}
