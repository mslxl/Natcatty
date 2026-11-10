package plugin

import (
	"fmt"
	"strings"
)

// LanguageCapability is one member of the closed editor language-service
// capability set. A provider declares the subset it implements; the host never
// invents a capability a provider did not declare.
type LanguageCapability string

const (
	LanguageHighlight     LanguageCapability = "highlight"
	LanguageCompletion    LanguageCapability = "completion"
	LanguageDiagnostics   LanguageCapability = "diagnostics"
	LanguageFormatting    LanguageCapability = "formatting"
	LanguageHover         LanguageCapability = "hover"
	LanguageSignatureHelp LanguageCapability = "signature_help"
	LanguageNavigation    LanguageCapability = "navigation"
	LanguageCodeActions   LanguageCapability = "code_actions"
)

// languageCapabilities is the closed capability vocabulary in stable display
// order. It never grows by manifest declaration alone.
var languageCapabilities = []LanguageCapability{
	LanguageHighlight,
	LanguageCompletion,
	LanguageDiagnostics,
	LanguageFormatting,
	LanguageHover,
	LanguageSignatureHelp,
	LanguageNavigation,
	LanguageCodeActions,
}

// LanguageCapabilities returns the closed capability set in display order.
func LanguageCapabilities() []LanguageCapability {
	return append([]LanguageCapability(nil), languageCapabilities...)
}

// IsLanguageCapability reports whether name belongs to the closed set.
func IsLanguageCapability(name string) bool {
	for _, capability := range languageCapabilities {
		if string(capability) == name {
			return true
		}
	}
	return false
}

// builtinLanguageIDs are the host-reserved language identifiers. A plugin may
// add providers for a built-in language, but it never owns that identifier:
// the built-in base highlighting always remains, and another plugin may add its
// own provider to the same language. The list is add-only across host releases.
var builtinLanguageIDs = map[string]bool{
	"c":          true,
	"cpp":        true,
	"css":        true,
	"go":         true,
	"html":       true,
	"javascript": true,
	"json":       true,
	"markdown":   true,
	"python":     true,
	"rust":       true,
	"shell":      true,
	"sql":        true,
	"toml":       true,
	"typescript": true,
	"xml":        true,
	"yaml":       true,
}

// BuiltinLanguageIDs returns the reserved language identifiers in stable
// order.
func BuiltinLanguageIDs() []string {
	return []string{
		"c", "cpp", "css", "go", "html", "javascript", "json", "markdown",
		"python", "rust", "shell", "sql", "toml", "typescript", "xml", "yaml",
	}
}

// IsBuiltinLanguageID reports whether id is a host-reserved built-in language.
func IsBuiltinLanguageID(id string) bool { return builtinLanguageIDs[id] }

// IsPluginOwnedLanguageID reports whether id is a custom language exclusively
// owned by pluginID. Plugin-owned identifiers are namespaced under the
// contributing plugin id, which makes ownership verifiable and collisions
// across plugins impossible.
func IsPluginOwnedLanguageID(pluginID, id string) bool {
	prefix := pluginID + "."
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	rest := strings.TrimPrefix(id, prefix)
	return rest != "" && validLanguageIdentifier(rest)
}

// LanguageProvider is one `language.provider` contribution. ID is local to the
// plugin; Languages names the built-in or plugin-owned language identifiers the
// provider serves; Capabilities is a subset of the closed capability set;
// Priority orders stacked providers (higher wins) and selects the single
// formatting winner. Contexts optionally restricts the provider to editor
// contexts whose kind is listed; an empty list serves every context.
type LanguageProvider struct {
	ID           string               `toml:"id"`
	Languages    []string             `toml:"languages"`
	Capabilities []LanguageCapability `toml:"capabilities"`
	Priority     int                  `toml:"priority"`
	Contexts     []string             `toml:"contexts"`
}

// LanguageContributions is the decoded `[contributions.language]` table. The
// contribution type is named `language.provider`; TOML expresses the dot as a
// nested table (`[[contributions.language.provider]]`).
type LanguageContributions struct {
	Providers []LanguageProvider `toml:"provider"`
}

const (
	// MinLanguageProviderPriority and MaxLanguageProviderPriority bound the
	// declared priority so a manifest cannot pin itself above every future
	// provider by unbounded values.
	MinLanguageProviderPriority = -1000
	MaxLanguageProviderPriority = 1000

	// MaxLanguageContexts is the per-provider context-kind allowlist cap.
	MaxLanguageContexts = 16
	// MaxLanguageProviderLanguages is the per-provider language cap.
	MaxLanguageProviderLanguages = 32
)

// Language identifiers and context kinds share the manifest-local identifier
// grammar: lowercase letters, digits, dot, underscore, and hyphen.
func validLanguageIdentifier(id string) bool { return commandIDPattern.MatchString(id) }

// Validate checks one language-provider declaration. pluginID supplies the
// ownership prefix for custom language identifiers.
func (p LanguageProvider) Validate(pluginID, field string) error {
	if !commandIDPattern.MatchString(p.ID) {
		return invalid(field+".id", "%q %s", p.ID, commandIDErrorText)
	}
	if len(p.Languages) == 0 {
		return invalid(field+".languages", "must name at least one language")
	}
	if len(p.Languages) > MaxLanguageProviderLanguages {
		return invalid(field+".languages", "declares %d languages, above the %d cap",
			len(p.Languages), MaxLanguageProviderLanguages)
	}
	seenLanguages := make(map[string]bool, len(p.Languages))
	for i, language := range p.Languages {
		languageField := fmt.Sprintf("%s.languages[%d]", field, i)
		if !validLanguageIdentifier(language) {
			return invalid(languageField, "%q is not a valid language identifier", language)
		}
		if seenLanguages[language] {
			return invalid(languageField, "duplicate language %q", language)
		}
		seenLanguages[language] = true
		if !IsBuiltinLanguageID(language) && !IsPluginOwnedLanguageID(pluginID, language) {
			return invalid(languageField,
				"%q is neither a built-in language nor owned by plugin %q (expected prefix %q)",
				language, pluginID, pluginID+".")
		}
	}
	if len(p.Capabilities) == 0 {
		return invalid(field+".capabilities", "must declare at least one capability")
	}
	seenCapabilities := make(map[LanguageCapability]bool, len(p.Capabilities))
	for i, capability := range p.Capabilities {
		capabilityField := fmt.Sprintf("%s.capabilities[%d]", field, i)
		if !IsLanguageCapability(string(capability)) {
			return invalid(capabilityField, "unknown capability %q", capability)
		}
		if seenCapabilities[capability] {
			return invalid(capabilityField, "duplicate capability %q", capability)
		}
		seenCapabilities[capability] = true
	}
	if p.Priority < MinLanguageProviderPriority || p.Priority > MaxLanguageProviderPriority {
		return invalid(field+".priority", "%d is outside the %d..%d range",
			p.Priority, MinLanguageProviderPriority, MaxLanguageProviderPriority)
	}
	if len(p.Contexts) > MaxLanguageContexts {
		return invalid(field+".contexts", "declares %d contexts, above the %d cap",
			len(p.Contexts), MaxLanguageContexts)
	}
	seenContexts := make(map[string]bool, len(p.Contexts))
	for i, context := range p.Contexts {
		contextField := fmt.Sprintf("%s.contexts[%d]", field, i)
		if !validLanguageIdentifier(context) {
			return invalid(contextField, "%q is not a valid context kind", context)
		}
		if seenContexts[context] {
			return invalid(contextField, "duplicate context %q", context)
		}
		seenContexts[context] = true
	}
	return nil
}

// SupportsLanguage reports whether the provider serves languageID and, when
// the provider declares contexts, whether contextKind is accepted.
func (p LanguageProvider) SupportsLanguage(languageID, contextKind string) bool {
	language := false
	for _, candidate := range p.Languages {
		if candidate == languageID {
			language = true
			break
		}
	}
	if !language {
		return false
	}
	if len(p.Contexts) == 0 {
		return true
	}
	for _, candidate := range p.Contexts {
		if candidate == contextKind {
			return true
		}
	}
	return false
}

// Supports reports whether the provider declares capability.
func (p LanguageProvider) Supports(capability LanguageCapability) bool {
	for _, candidate := range p.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// validateLanguageProviders checks the language contributions of a manifest
// and enforces that a language provider is only usable with the
// editor.language permission granted at enable time.
func validateLanguageProviders(permissions Permissions, pluginID string,
	providers []LanguageProvider) error {
	if len(providers) == 0 {
		return nil
	}
	required := false
	for _, name := range permissions.Required {
		if name == string(PermissionEditorLanguage) {
			required = true
			break
		}
	}
	if !required {
		return invalid("permissions.required",
			"a language.provider contribution requires the %s permission in permissions.required",
			PermissionEditorLanguage)
	}
	seen := make(map[string]bool, len(providers))
	for i, provider := range providers {
		field := fmt.Sprintf("contributions.language.provider[%d]", i)
		if seen[provider.ID] {
			return invalid(field+".id", "duplicate language provider %q", provider.ID)
		}
		seen[provider.ID] = true
		if err := provider.Validate(pluginID, field); err != nil {
			return err
		}
	}
	return nil
}
