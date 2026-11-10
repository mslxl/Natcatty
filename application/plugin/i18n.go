package plugin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// ServiceI18n is the permission-free host i18n service. A plugin uses it for
// its own rendered text; host-rendered contribution strings use manifest
// *_key fields instead.
const ServiceI18n = domainpluginabi.ServiceI18n

// MaxMissingKeyDiagnostics bounds the distinct missing keys remembered per
// Plugin Instance. Diagnostics must not grow without bound when a plugin asks
// for random keys; further new keys are ignored, known keys keep counting.
const MaxMissingKeyDiagnostics = 256

// CatalogSource loads a package's i18n/<bcp47>.toml assets. The TOML adapter
// lives outside the application layer so this package stays dependency-free.
// The same loader validates .natplug packages at install time, so installed,
// development, and first-party plugins share one catalog format.
type CatalogSource interface {
	LoadCatalogs(directory, defaultLocale string) (domainplugin.Catalogs, error)
}

// LocaleFormatter writes locale-dependent values the plugin's own text needs.
// The host owns formatting, CLDR plural categories, and writing direction so a
// plugin cannot invent a locale or bypass the user's regional settings. A host
// without a formatter keeps the language-neutral defaults.
type LocaleFormatter interface {
	FormatNumber(locale string, value float64) string
	FormatDate(locale string, value time.Time) string
	FormatRelativeTime(locale string, value, now time.Time) string
	PluralCategory(locale string, count int) string
	Direction(locale string) UIDirection
}

// defaultLocaleFormatter is the language-neutral fallback for headless hosts
// and tests. It emits no prose: a relative time is the signed duration from now
// to value. The app supplies the MyGo-backed formatter for real locale output.
type defaultLocaleFormatter struct{}

func (defaultLocaleFormatter) FormatNumber(_ string, value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func (defaultLocaleFormatter) FormatDate(_ string, value time.Time) string {
	return value.Format("2006-01-02")
}

func (defaultLocaleFormatter) FormatRelativeTime(_ string, value, now time.Time) string {
	return value.Sub(now).Round(time.Second).String()
}

func (defaultLocaleFormatter) PluralCategory(locale string, count int) string {
	return domainplugin.PluralCategory(locale, count)
}

func (defaultLocaleFormatter) Direction(locale string) UIDirection {
	return fallbackDirection(locale)
}

// DirectionFor returns the writing direction of a locale from the built-in
// language list. A real host supplies the script-aware answer through its
// LocaleFormatter; this fallback covers headless hosts and tests.
func DirectionFor(locale string) UIDirection {
	return fallbackDirection(locale)
}

func fallbackDirection(locale string) UIDirection {
	language := strings.ToLower(locale)
	if index := strings.IndexByte(language, '-'); index >= 0 {
		language = language[:index]
	}
	if index := strings.IndexByte(language, '_'); index >= 0 {
		language = language[:index]
	}
	switch language {
	case "ar", "ckb", "dv", "fa", "he", "ku", "ps", "sd", "ug", "ur", "yi":
		return UIDirectionRTL
	default:
		return UIDirectionLTR
	}
}

func normalizeLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return "en"
	}
	return locale
}

// MessageTranslation is one resolved plugin message. Stage and Missing let the
// plugin and the host diagnostics distinguish a translated string from a
// fallback.
type MessageTranslation struct {
	Text    string                       `json:"text"`
	Key     string                       `json:"key"`
	Stage   domainplugin.ResolutionStage `json:"stage"`
	Missing bool                         `json:"missing"`
	Locale  string                       `json:"locale"`
	// Generation changes when the host locale changes, so a cached catalog can
	// be invalidated without another service call.
	Generation uint64 `json:"generation"`
}

// CatalogSnapshot is the whole effective catalog for the host locale. The SDK
// caches it at init so per-string translation never crosses the ABI.
type CatalogSnapshot struct {
	Locale        string            `json:"locale"`
	Direction     UIDirection       `json:"direction"`
	DefaultLocale string            `json:"default_locale"`
	Generation    uint64            `json:"generation"`
	Messages      map[string]string `json:"messages"`
}

// MissingKey is one runtime missing-key diagnostic with its fallback count.
type MissingKey struct {
	Key   string `json:"key"`
	Count uint64 `json:"count"`
}

// i18nState returns the effective locale, catalogs, and generation for one
// session. The i18n service is permission-free but still requires a live,
// open Plugin Instance.
func (s *ServiceSession) i18nState() (string, domainplugin.Catalogs, uint64, error) {
	if s == nil || s.host == nil {
		return "", domainplugin.Catalogs{}, 0, &ServiceError{
			Service: ServiceI18n, Code: ServiceCodeFault,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable,
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", domainplugin.Catalogs{}, 0, &ServiceError{
			Service: ServiceI18n, Code: ServiceCodeInvalid,
			Message: "the plugin instance is closed",
		}
	}
	return s.locale, s.catalogs.Clone(), s.catalogGeneration, nil
}

// Translate resolves one plugin message key in the host locale. The fallback
// chain is exact tag, language-only, default locale, the literal fallback, and
// the key. Named placeholders are substituted after resolution.
func (s *ServiceSession) Translate(key, literal string,
	args map[string]string) (MessageTranslation, error) {
	return s.translate(key, literal, args, "")
}

// TranslatePlural resolves key.category, falling back to the base key when no
// plural sibling is translated. The count is also exposed to the message as
// the {count} placeholder.
func (s *ServiceSession) TranslatePlural(key, literal string,
	args map[string]string, count int) (MessageTranslation, error) {
	locale, formatter, err := s.formatterState()
	if err != nil {
		return MessageTranslation{}, err
	}
	category := formatter.PluralCategory(locale, count)
	if args == nil {
		args = make(map[string]string, 1)
	} else {
		copied := make(map[string]string, len(args)+1)
		for name, value := range args {
			copied[name] = value
		}
		args = copied
	}
	args["count"] = strconv.Itoa(count)
	return s.translate(key, literal, args, category)
}

func (s *ServiceSession) effectiveLocale() string {
	if s == nil || s.host == nil {
		return "en"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locale == "" {
		return s.host.Locale()
	}
	return s.locale
}

func (s *ServiceSession) translate(key, literal string,
	args map[string]string, category string) (MessageTranslation, error) {
	locale, catalogs, generation, err := s.i18nState()
	if err != nil {
		return MessageTranslation{}, err
	}
	if !domainplugin.ValidMessageKey(key) {
		return MessageTranslation{}, &ServiceError{
			Service: ServiceI18n, Method: "translate", Code: ServiceCodeInvalid,
			Message: fmt.Sprintf("%q does not match [a-z0-9_.-]{1,128}", key),
		}
	}
	var resolution domainplugin.Resolution
	if category != "" {
		resolution = catalogs.ResolvePlural(locale, key, literal, category)
	} else {
		resolution = catalogs.Resolve(locale, key, literal)
	}
	if resolution.Stage != domainplugin.StageExact {
		s.host.recordMissingKey(s.fiber, key)
	}
	return MessageTranslation{
		Text:       domainplugin.RenderMessage(resolution.Text, args),
		Key:        key,
		Stage:      resolution.Stage,
		Missing:    resolution.Missing,
		Locale:     locale,
		Generation: generation,
	}, nil
}

// Catalog returns the whole effective catalog for the host locale and a
// generation that changes on every locale change.
func (s *ServiceSession) Catalog() (CatalogSnapshot, error) {
	locale, catalogs, generation, err := s.i18nState()
	if err != nil {
		return CatalogSnapshot{}, err
	}
	return CatalogSnapshot{
		Locale:        locale,
		Direction:     s.host.directionFor(locale),
		DefaultLocale: catalogs.Default,
		Generation:    generation,
		Messages:      catalogs.Effective(locale),
	}, nil
}

// FormatNumber writes value in the host locale.
func (s *ServiceSession) FormatNumber(value float64) (string, error) {
	locale, formatter, err := s.formatterState()
	if err != nil {
		return "", err
	}
	return formatter.FormatNumber(locale, value), nil
}

// FormatDate writes value in the host locale.
func (s *ServiceSession) FormatDate(value time.Time) (string, error) {
	locale, formatter, err := s.formatterState()
	if err != nil {
		return "", err
	}
	return formatter.FormatDate(locale, value), nil
}

// FormatRelativeTime writes the distance between value and now in the host
// locale.
func (s *ServiceSession) FormatRelativeTime(value, now time.Time) (string, error) {
	locale, formatter, err := s.formatterState()
	if err != nil {
		return "", err
	}
	return formatter.FormatRelativeTime(locale, value, now), nil
}

// PluralCategory returns the CLDR cardinal category of count in the host
// locale, as the host formatter computes it.
func (s *ServiceSession) PluralCategory(count int) (string, error) {
	locale, formatter, err := s.formatterState()
	if err != nil {
		return "", err
	}
	return formatter.PluralCategory(locale, count), nil
}

// Direction returns the writing direction of the session's host locale.
func (s *ServiceSession) Direction() (UIDirection, error) {
	locale, _, _, err := s.i18nState()
	if err != nil {
		return "", err
	}
	return s.host.directionFor(locale), nil
}

func (s *ServiceSession) formatterState() (string, LocaleFormatter, error) {
	locale, _, _, err := s.i18nState()
	if err != nil {
		return "", nil, err
	}
	return locale, s.host.Formatter(), nil
}

// SetLocale changes the host locale without reloading any Plugin Instance.
// Every live session re-reads its locale, generation, and native UI
// environment; the locale_changed event reaches subscribers and native views.
// It reports whether the locale changed.
func (h *HostServices) SetLocale(locale string) bool {
	if h == nil {
		return false
	}
	locale = normalizeLocale(locale)
	h.localeMu.Lock()
	if h.locale == locale {
		h.localeMu.Unlock()
		return false
	}
	h.locale = locale
	h.catalogGeneration++
	generation := h.catalogGeneration
	h.localeMu.Unlock()

	direction := h.directionFor(locale)
	h.mu.Lock()
	sessions := make([]*ServiceSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()
	for _, session := range sessions {
		session.applyLocale(locale, direction, generation)
	}
	h.Publish(domainplugin.EventLocaleChanged, map[string]string{
		"locale": locale, "direction": string(direction),
	})
	return true
}

// Locale returns the current host locale.
func (h *HostServices) Locale() string {
	if h == nil {
		return "en"
	}
	h.localeMu.RLock()
	defer h.localeMu.RUnlock()
	return h.locale
}

// Direction returns the writing direction of the current host locale.
func (h *HostServices) Direction() UIDirection { return h.directionFor(h.Locale()) }

// directionFor asks the host formatter for the script-aware direction and
// falls back to the built-in language list when no formatter is attached.
func (h *HostServices) directionFor(locale string) UIDirection {
	if h == nil {
		return fallbackDirection(locale)
	}
	h.localeMu.RLock()
	formatter := h.formatter
	h.localeMu.RUnlock()
	if formatter == nil {
		return fallbackDirection(locale)
	}
	return formatter.Direction(locale)
}

// Formatter returns the host locale formatter, never nil.
func (h *HostServices) Formatter() LocaleFormatter {
	if h == nil {
		return defaultLocaleFormatter{}
	}
	h.localeMu.RLock()
	defer h.localeMu.RUnlock()
	if h.formatter == nil {
		return defaultLocaleFormatter{}
	}
	return h.formatter
}

// CatalogGeneration returns the current catalog revision. It advances on every
// locale change, so a cached catalog can tell whether it is stale.
func (h *HostServices) CatalogGeneration() uint64 {
	if h == nil {
		return 0
	}
	h.localeMu.RLock()
	defer h.localeMu.RUnlock()
	return h.catalogGeneration
}

// applyLocale updates one session after a host locale change. The native UI
// session queues locale_changed for its renderer; the event bus carries the
// same change to event subscribers.
func (s *ServiceSession) applyLocale(locale string, direction UIDirection, generation uint64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.locale = locale
	s.catalogGeneration = generation
	ui := s.ui
	webview := s.webview
	s.mu.Unlock()
	if webview != nil {
		webview.setLocale(locale, direction)
	}
	if ui == nil {
		return
	}
	env := ui.Environment()
	env.Locale = locale
	env.Direction = direction
	ui.SetEnvironment(env)
}

// recordMissingKey counts one fallback for a fiber. Only distinct keys are
// remembered, up to MaxMissingKeyDiagnostics.
func (h *HostServices) recordMissingKey(fiber domainplugin.FiberID, key string) {
	if h == nil || key == "" {
		return
	}
	h.missingMu.Lock()
	defer h.missingMu.Unlock()
	if h.missing == nil {
		h.missing = make(map[domainplugin.FiberID]map[string]uint64)
	}
	keys := h.missing[fiber]
	if keys == nil {
		keys = make(map[string]uint64)
		h.missing[fiber] = keys
	}
	if _, exists := keys[key]; !exists && len(keys) >= MaxMissingKeyDiagnostics {
		return
	}
	keys[key]++
}

// MissingKeys returns the missing-key diagnostics of one fiber in key order.
func (h *HostServices) MissingKeys(fiber domainplugin.FiberID) []MissingKey {
	if h == nil {
		return nil
	}
	h.missingMu.Lock()
	defer h.missingMu.Unlock()
	keys := h.missing[fiber]
	out := make([]MissingKey, 0, len(keys))
	for key, count := range keys {
		out = append(out, MissingKey{Key: key, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// SetLocale changes the host locale for plugin surfaces and the i18n service.
// Every live instance sees the new locale through its session; none reloads.
func (m *Manager) SetLocale(locale string) bool {
	if m == nil || m.services == nil {
		return false
	}
	return m.services.SetLocale(locale)
}

// Locale returns the host locale plugins are displayed in.
func (m *Manager) Locale() string {
	if m == nil || m.services == nil {
		return "en"
	}
	return m.services.Locale()
}

// Localize resolves one host-rendered contribution string for a live plugin.
// The manifest's *_key is tried first, then its literal fallback, so a
// contribution without a key or with a missing translation still renders.
func (m *Manager) Localize(ref PluginRef, key, literal string) string {
	if key == "" {
		return literal
	}
	if m == nil || m.container == nil || m.services == nil {
		return localizedFallback(key, literal)
	}
	fiber, ok := m.container.FiberByRef(ref)
	if !ok || fiber == nil {
		return localizedFallback(key, literal)
	}
	return m.resolveLocalized(fiber.Catalogs(), fiber.ID(), key, literal)
}

// resolveLocalized applies the fallback chain and counts every non-exact
// lookup as a runtime missing-key diagnostic.
func (m *Manager) resolveLocalized(catalogs domainplugin.Catalogs,
	fiber domainplugin.FiberID, key, literal string) string {
	if key == "" {
		return literal
	}
	resolution := catalogs.Resolve(m.services.Locale(), key, literal)
	if resolution.Stage != domainplugin.StageExact && fiber != "" {
		m.services.recordMissingKey(fiber, key)
	}
	return resolution.Text
}

func localizedFallback(key, literal string) string {
	if literal != "" {
		return literal
	}
	return key
}

// localizerFor returns a display localizer for one plugin id. A live fiber's
// catalogs win; an installed but inactive plugin reads the package's catalogs
// so its settings page still renders translated fields.
func (m *Manager) localizerFor(ctx context.Context, id string) func(key, literal string) string {
	catalogs := domainplugin.Catalogs{}
	fiberID := domainplugin.FiberID("")
	if m == nil || m.services == nil {
		return func(key, literal string) string { return localizedFallback(key, literal) }
	}
	if fiber, ok := m.fiberByKey(id); ok && fiber != nil {
		catalogs = fiber.Catalogs()
		fiberID = fiber.ID()
	} else if m.catalogSource != nil && m.store != nil {
		if manifest, err := m.loadManifest(ctx, id); err == nil {
			if directory, err := m.store.PackageDir(ctx, id); err == nil {
				if loaded, err := m.catalogSource.LoadCatalogs(directory, manifest.DefaultLocale); err == nil {
					catalogs = loaded
				}
			}
		}
	}
	return func(key, literal string) string {
		return m.resolveLocalized(catalogs, fiberID, key, literal)
	}
}
