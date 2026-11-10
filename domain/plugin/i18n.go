package plugin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Plugin i18n contract limits. Catalogs are flat TOML key/value files shipped
// as package assets; exceeding any limit rejects the package at install.
const (
	// MaxLocales bounds the catalogs a package may ship.
	MaxLocales = 32
	// MaxCatalogKeys bounds one locale.
	MaxCatalogKeys = 10000
	// MaxMessageBytes bounds one message.
	MaxMessageBytes = 4 << 10
	// MaxCatalogsBytes bounds every catalog together.
	MaxCatalogsBytes = 2 << 20
)

// CLDR plural categories used by the sibling-key convention. A catalog spells
// a plural as key.zero, key.one, key.two, key.few, key.many, and key.other.
const (
	PluralZero  = "zero"
	PluralOne   = "one"
	PluralTwo   = "two"
	PluralFew   = "few"
	PluralMany  = "many"
	PluralOther = "other"
)

// PluralCategories returns the closed sibling-key vocabulary in stable order.
func PluralCategories() []string {
	return []string{PluralZero, PluralOne, PluralTwo, PluralFew, PluralMany, PluralOther}
}

var (
	messageLocalePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	messageKeyPattern    = regexp.MustCompile(`^[a-z0-9_.-]{1,128}$`)
	placeholderPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
)

// ValidLocaleTag reports whether tag is a BCP 47-shaped language tag accepted
// by the plugin manifest and catalog filenames.
func ValidLocaleTag(tag string) bool {
	return messageLocalePattern.MatchString(tag)
}

// ValidMessageKey reports whether key matches the plugin catalog key grammar.
func ValidMessageKey(key string) bool {
	return messageKeyPattern.MatchString(key)
}

// Catalog is one locale's flat message table.
type Catalog map[string]string

// Catalogs is a package's declared catalogs. Default names the locale whose
// catalog must exist; Locales is keyed by the tag the package used.
type Catalogs struct {
	Default string
	Locales map[string]Catalog
}

// NewCatalogs returns a catalogs value over the supplied tables.
func NewCatalogs(defaultLocale string, locales map[string]Catalog) Catalogs {
	return Catalogs{Default: defaultLocale, Locales: cloneLocales(locales)}
}

// Empty reports whether no catalog was loaded.
func (c Catalogs) Empty() bool { return len(c.Locales) == 0 }

// Clone returns a deep copy so callers cannot mutate a loaded package.
func (c Catalogs) Clone() Catalogs {
	return Catalogs{Default: c.Default, Locales: cloneLocales(c.Locales)}
}

func cloneLocales(locales map[string]Catalog) map[string]Catalog {
	if len(locales) == 0 {
		return map[string]Catalog{}
	}
	out := make(map[string]Catalog, len(locales))
	for tag, catalog := range locales {
		copy := make(Catalog, len(catalog))
		for key, message := range catalog {
			copy[key] = message
		}
		out[tag] = copy
	}
	return out
}

// Tags returns the declared locale tags in stable order.
func (c Catalogs) Tags() []string {
	tags := make([]string, 0, len(c.Locales))
	for tag := range c.Locales {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// Locale returns the catalog for tag, matching tags case-insensitively, and
// the tag spelling the package used.
func (c Catalogs) Locale(tag string) (Catalog, string, bool) {
	if tag == "" {
		return nil, "", false
	}
	for _, declared := range c.Tags() {
		if strings.EqualFold(declared, tag) {
			return c.Locales[declared], declared, true
		}
	}
	return nil, "", false
}

// Validate checks the locale tags, key grammar, per-locale key count, message
// size, and total message bytes, and requires the default catalog to exist.
// Duplicate keys are rejected by the TOML decoder before this runs.
func (c Catalogs) Validate() error {
	if !ValidLocaleTag(c.Default) {
		return &CatalogError{Path: "i18n", Problem: fmt.Sprintf(
			"default locale %q is not a BCP 47 language tag", c.Default)}
	}
	if len(c.Locales) == 0 {
		return &CatalogError{Path: "i18n", Problem: fmt.Sprintf(
			"default locale %q has no catalog", c.Default)}
	}
	if len(c.Locales) > MaxLocales {
		return &CatalogError{Path: "i18n", Problem: fmt.Sprintf(
			"package ships %d catalogs, above the %d locale limit", len(c.Locales), MaxLocales)}
	}
	seen := make(map[string]string, len(c.Locales))
	total := 0
	haveDefault := false
	for _, tag := range c.Tags() {
		if !ValidLocaleTag(tag) {
			return &CatalogError{Path: catalogPath(tag), Problem: fmt.Sprintf(
				"%q is not a BCP 47 language tag", tag)}
		}
		folded := strings.ToLower(tag)
		if previous, ok := seen[folded]; ok {
			return &CatalogError{Path: catalogPath(tag), Problem: fmt.Sprintf(
				"locale %q is declared twice as %q and %q", tag, previous, tag)}
		}
		seen[folded] = tag
		if strings.EqualFold(tag, c.Default) {
			haveDefault = true
		}
		catalog := c.Locales[tag]
		if len(catalog) > MaxCatalogKeys {
			return &CatalogError{Path: catalogPath(tag), Problem: fmt.Sprintf(
				"catalog has %d keys, above the %d key limit", len(catalog), MaxCatalogKeys)}
		}
		for _, key := range sortedKeys(catalog) {
			message := catalog[key]
			if !ValidMessageKey(key) {
				return &CatalogError{Path: catalogPath(tag), Problem: fmt.Sprintf(
					"key %q does not match [a-z0-9_.-]{1,128}", key)}
			}
			if len(message) > MaxMessageBytes {
				return &CatalogError{Path: catalogPath(tag), Problem: fmt.Sprintf(
					"message %q is %d bytes, above the %d byte limit", key, len(message), MaxMessageBytes)}
			}
			total += len(key) + len(message)
			if total > MaxCatalogsBytes {
				return &CatalogError{Path: catalogPath(tag), Problem: fmt.Sprintf(
					"catalogs total more than the %d byte limit", MaxCatalogsBytes)}
			}
		}
	}
	if !haveDefault {
		return &CatalogError{Path: "i18n", Problem: fmt.Sprintf(
			"default locale %q has no catalog", c.Default)}
	}
	return nil
}

func catalogPath(tag string) string { return "i18n/" + tag + ".toml" }

func sortedKeys(catalog Catalog) []string {
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// CatalogError names one catalog validation failure with the package-relative
// path that caused it.
type CatalogError struct {
	Path    string
	Problem string
}

func (e *CatalogError) Error() string {
	if e.Path == "" {
		return "plugin catalog: " + e.Problem
	}
	return "plugin catalog: " + e.Path + ": " + e.Problem
}

// Effective returns the catalog for tag with the fallback chain applied per
// key: exact tag, language-only, then the default locale. Missing keys in a
// locale are filled from the next step. The returned map is a copy.
func (c Catalogs) Effective(tag string) Catalog {
	out := make(Catalog)
	if defaultCatalog, _, ok := c.Locale(c.Default); ok {
		for key, message := range defaultCatalog {
			out[key] = message
		}
	}
	if language := languageOnly(tag); language != "" && !strings.EqualFold(language, c.Default) {
		if catalog, _, ok := c.Locale(language); ok {
			for key, message := range catalog {
				out[key] = message
			}
		}
	}
	if catalog, _, ok := c.Locale(tag); ok {
		for key, message := range catalog {
			out[key] = message
		}
	}
	return out
}

// ResolutionStage names the fallback step a message came from.
type ResolutionStage string

const (
	// StageExact found the key in the requested tag's catalog.
	StageExact ResolutionStage = "exact"
	// StageLanguage found the key in the language-only catalog.
	StageLanguage ResolutionStage = "language"
	// StageDefault found the key in the default locale's catalog.
	StageDefault ResolutionStage = "default"
	// StageLiteral used the manifest's literal fallback.
	StageLiteral ResolutionStage = "literal"
	// StageKey used the key itself because no translation or literal exists.
	StageKey ResolutionStage = "key"
)

// Resolution is one message lookup.
type Resolution struct {
	Text  string
	Key   string
	Stage ResolutionStage
	// Missing reports that the requested locale's catalog did not contain the
	// key, so a fallback step supplied it.
	Missing bool
}

// Resolve applies the plugin fallback chain: exact tag, language-only,
// default locale, literal fallback, then the key itself.
func (c Catalogs) Resolve(locale, key, literal string) Resolution {
	out := Resolution{Key: key}
	if exact, _, ok := c.Locale(locale); ok {
		if message, exists := exact[key]; exists {
			out.Text, out.Stage = message, StageExact
			return out
		}
	}
	if language := languageOnly(locale); language != "" && !strings.EqualFold(language, locale) {
		if catalog, _, ok := c.Locale(language); ok {
			if message, exists := catalog[key]; exists {
				out.Text, out.Stage, out.Missing = message, StageLanguage, true
				return out
			}
		}
	}
	if defaultCatalog, _, ok := c.Locale(c.Default); ok {
		if message, exists := defaultCatalog[key]; exists {
			out.Text, out.Stage, out.Missing = message, StageDefault, true
			return out
		}
	}
	if literal != "" {
		out.Text, out.Stage, out.Missing = literal, StageLiteral, true
		return out
	}
	out.Text, out.Stage, out.Missing = key, StageKey, true
	return out
}

// ResolvePlural applies the fallback chain to key.category and falls back to
// the base key when the plural sibling is not translated anywhere.
func (c Catalogs) ResolvePlural(locale, key, literal, category string) Resolution {
	if category == "" {
		return c.Resolve(locale, key, literal)
	}
	sibling := c.Resolve(locale, key+"."+category, "")
	if sibling.Stage != StageLiteral && sibling.Stage != StageKey {
		return sibling
	}
	return c.Resolve(locale, key, literal)
}

func languageOnly(tag string) string {
	tag = strings.TrimSpace(tag)
	if index := strings.IndexByte(tag, '-'); index >= 0 {
		return tag[:index]
	}
	return tag
}

// RenderMessage substitutes named placeholders in template. A placeholder is
// {name}; doubled braces {{ and }} are literal braces. An unknown placeholder
// is left visible rather than silently dropped.
func RenderMessage(template string, args map[string]string) string {
	if template == "" || (!strings.Contains(template, "{") && !strings.Contains(template, "}")) {
		return template
	}
	var out strings.Builder
	out.Grow(len(template))
	for i := 0; i < len(template); {
		switch template[i] {
		case '{':
			if i+1 < len(template) && template[i+1] == '{' {
				out.WriteByte('{')
				i += 2
				continue
			}
			end := strings.IndexByte(template[i+1:], '}')
			if end >= 0 {
				end += i + 1
				name := template[i+1 : end]
				if placeholderPattern.MatchString(name) {
					if value, ok := args[name]; ok {
						out.WriteString(value)
					} else {
						out.WriteString(template[i : end+1])
					}
					i = end + 1
					continue
				}
			}
			out.WriteByte('{')
			i++
		case '}':
			if i+1 < len(template) && template[i+1] == '}' {
				out.WriteByte('}')
				i += 2
				continue
			}
			out.WriteByte('}')
			i++
		default:
			out.WriteByte(template[i])
			i++
		}
	}
	return out.String()
}

// PluralCategory returns the CLDR cardinal category for count in locale. The
// host formatter supplies the production answer; this fallback covers the
// app's shipped languages and falls back to English rules. CLDR operands read
// the absolute value, so -1 is "one" in English.
func PluralCategory(locale string, count int) string {
	if count < 0 {
		count = -count
	}
	switch strings.ToLower(languageOnly(locale)) {
	case "ru", "uk", "be":
		mod10 := count % 10
		mod100 := count % 100
		switch {
		case mod10 == 1 && mod100 != 11:
			return PluralOne
		case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
			return PluralFew
		case mod10 == 0 || (mod10 >= 5 && mod10 <= 9) || (mod100 >= 11 && mod100 <= 14):
			return PluralMany
		default:
			return PluralOther
		}
	case "zh", "ja", "ko", "th", "vi":
		return PluralOther
	case "es", "it", "pt", "fr":
		if count == 1 {
			return PluralOne
		}
		return PluralOther
	default:
		if count == 1 {
			return PluralOne
		}
		return PluralOther
	}
}

// MessageKeyRef names one manifest field that references a catalog key.
type MessageKeyRef struct {
	Field string
	Key   string
}

// ReferencedMessageKeys returns every catalog key the manifest references from
// a host-rendered contribution string, in declaration order.
func (m Manifest) ReferencedMessageKeys() []MessageKeyRef {
	var refs []MessageKeyRef
	for i, command := range m.Contributions.Commands {
		if command.TitleKey != "" {
			refs = append(refs, MessageKeyRef{
				Field: fmt.Sprintf("contributions.commands[%d].title_key", i),
				Key:   command.TitleKey,
			})
		}
	}
	for i, menu := range m.Contributions.Menus {
		if menu.LabelKey != "" {
			refs = append(refs, MessageKeyRef{
				Field: fmt.Sprintf("contributions.menus[%d].label_key", i),
				Key:   menu.LabelKey,
			})
		}
	}
	for i, view := range m.Contributions.Views {
		if view.TitleKey != "" {
			refs = append(refs, MessageKeyRef{
				Field: fmt.Sprintf("contributions.views[%d].title_key", i),
				Key:   view.TitleKey,
			})
		}
	}
	for i, field := range m.Settings.Fields {
		if field.LabelKey != "" {
			refs = append(refs, MessageKeyRef{
				Field: fmt.Sprintf("settings.fields[%d].label_key", i),
				Key:   field.LabelKey,
			})
		}
		if field.DescriptionKey != "" {
			refs = append(refs, MessageKeyRef{
				Field: fmt.Sprintf("settings.fields[%d].description_key", i),
				Key:   field.DescriptionKey,
			})
		}
	}
	for i, section := range m.Contributions.SystemPanel {
		if section.TitleKey != "" {
			refs = append(refs, MessageKeyRef{
				Field: fmt.Sprintf("contributions.system_panel[%d].title_key", i),
				Key:   section.TitleKey,
			})
		}
		for j, action := range section.Actions {
			if action.LabelKey != "" {
				refs = append(refs, MessageKeyRef{
					Field: fmt.Sprintf("contributions.system_panel[%d].actions[%d].label_key", i, j),
					Key:   action.LabelKey,
				})
			}
		}
	}
	return refs
}
