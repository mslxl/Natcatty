package settings

import (
	"strings"

	domainkb "github.com/mslxl/natcatty/domain/keybindings"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

// Area identifies one Settings area in the sidebar.
type Area string

const (
	AreaAppearance Area = "appearance"
	AreaTerminal   Area = "terminal"
	AreaLayout     Area = "layout"
	AreaSFTPEditor Area = "sftpEditor"
	AreaLogs       Area = "logs"
	AreaSystem     Area = "system"
	AreaSecurity   Area = "security"
	AreaSync       Area = "sync"
	AreaShortcuts  Area = "shortcuts"
)

// Areas returns the sidebar order.
func Areas() []Area {
	return []Area{AreaAppearance, AreaTerminal, AreaLayout, AreaSFTPEditor,
		AreaLogs, AreaSystem, AreaSecurity, AreaSync, AreaShortcuts}
}

// AreaTitle returns the localized area name.
func AreaTitle(m locales.Messages, area Area) string {
	switch area {
	case AreaAppearance:
		return m.SettingsAreaAppearance()
	case AreaTerminal:
		return m.SettingsAreaTerminal()
	case AreaLayout:
		return m.SettingsAreaLayout()
	case AreaSFTPEditor:
		return m.SettingsAreaSFTPEditor()
	case AreaLogs:
		return m.SettingsAreaLogs()
	case AreaSystem:
		return m.SettingsAreaSystem()
	case AreaSecurity:
		return m.SettingsAreaSecurity()
	case AreaSync:
		return m.SettingsAreaSync()
	case AreaShortcuts:
		return m.SettingsAreaShortcuts()
	default:
		return ""
	}
}

// AreaDescription returns the one-line description under an area title.
func AreaDescription(m locales.Messages, area Area) string {
	switch area {
	case AreaAppearance:
		return m.SettingsAppearanceDescription()
	case AreaTerminal:
		return m.SettingsTerminalDescription()
	case AreaLayout:
		return m.SettingsLayoutDescription()
	case AreaSFTPEditor:
		return m.SettingsSFTPEditorDescription()
	case AreaLogs:
		return m.SettingsLogsDescription()
	case AreaSystem:
		return m.SettingsSystemAreaDescription()
	case AreaSecurity:
		return m.SettingsSecurityDescription()
	case AreaSync:
		return m.SettingsSyncDescription()
	case AreaShortcuts:
		return m.SettingsShortcutsDescription()
	default:
		return ""
	}
}

// SearchHit is one searchable setting or area.
type SearchHit struct {
	Area   Area
	Field  domain.Field
	AreaID string
	Label  string
	// keywords holds stable identifiers and English terms so a search works
	// before the user switches language or knows the translated name.
	keywords string
}

// ShortcutSource exposes the effective chords and menu accelerators of the
// live binding document. Search indexes them so a chord query reaches the row
// it belongs to, including a user override.
type ShortcutSource interface {
	ChordFor(id string, scheme domainkb.Scheme) string
	MenuShortcuts() []domainkb.MenuShortcut
}

// SearchIndex returns every searchable setting with localized labels. A
// ShortcutSource adds both scheme columns' effective chords, the user's
// overrides, and the native menu's accelerators to the Shortcuts area.
func SearchIndex(m locales.Messages, sources ...ShortcutSource) []SearchHit {
	hits := make([]SearchHit, 0, 100)
	add := func(area Area, field domain.Field, label string, keywords ...string) {
		hits = append(hits, SearchHit{Area: area, Field: field, Label: label,
			keywords: strings.Join(keywords, " ")})
	}
	addArea := func(area Area, keywords ...string) {
		hits = append(hits, SearchHit{Area: area, AreaID: string(area),
			Label: AreaTitle(m, area), keywords: strings.Join(keywords, " ")})
	}

	for _, area := range Areas() {
		addArea(area, string(area))
	}

	add(AreaAppearance, domain.FieldThemeMode, m.SettingsThemeMode(), "theme mode light dark system")
	add(AreaAppearance, domain.FieldLightTheme, m.SettingsLightTheme(), "light theme preset palette")
	add(AreaAppearance, domain.FieldDarkTheme, m.SettingsDarkTheme(), "dark theme preset palette")
	add(AreaAppearance, domain.FieldAccentMode, m.SettingsAccentMode(), "accent color custom")
	add(AreaAppearance, domain.FieldAccentColor, m.SettingsAccentColor(), "accent color hsl")
	add(AreaAppearance, domain.FieldUIFont, m.SettingsUIFont(), "font interface")
	add(AreaAppearance, domain.FieldWindowOpacity, m.SettingsWindowOpacity(), "opacity transparency")
	add(AreaAppearance, domain.FieldAppIcon, m.SettingsAppIcon(), "icon app dock taskbar tray")
	add(AreaAppearance, domain.FieldTerminalTheme, m.SettingsTerminalTheme(), "terminal theme scheme palette")
	add(AreaAppearance, domain.FieldTerminalFontFamily, m.SettingsTerminalFontFamily(), "terminal font family")
	add(AreaAppearance, domain.FieldTerminalFontSize, m.SettingsTerminalFontSize(), "terminal font size")
	add(AreaAppearance, "", m.SettingsTerminalThemeImport(), "import iterm itermcolors scheme")
	add(AreaAppearance, "", m.SettingsTerminalThemeFilter(), "filter search scheme")

	add(AreaLayout, domain.FieldShowHostTreeSidebar, m.SettingsShowHostTreeSidebar(), "host sidebar tree")
	add(AreaLayout, domain.FieldShowSFTPTab, m.SettingsShowSFTPTab(), "sftp tab")
	add(AreaLayout, domain.FieldShowRecentHosts, m.SettingsShowRecentHosts(), "recent hosts")
	add(AreaLayout, domain.FieldTabBarPosition, m.SettingsTabBarPosition(), "tab bar top bottom")
	add(AreaLayout, domain.FieldShellOnlyTabNumberShortcuts, m.SettingsShellOnlyTabNumberShortcuts(), "tab number pinned")
	add(AreaLayout, domain.FieldShowTabNumberBadges, m.SettingsShowTabNumberBadges(), "tab badge number")
	add(AreaLayout, domain.FieldRestorePreviousSession, m.SettingsRestorePreviousSession(), "restore session startup")
	add(AreaLayout, domain.FieldStartupLanding, m.SettingsStartupLanding(), "startup landing vault")
	add(AreaLayout, domain.FieldSidePanelDock, m.SettingsSidePanelDock(), "side panel dock")

	add(AreaTerminal, "", m.SettingsTerminalLoggingTitle(), "logging log directory format timestamps output")
	add(AreaTerminal, "", m.SettingsLogEnabled(), "logging enabled save output")
	add(AreaTerminal, "", m.SettingsLogDirectory(), "log directory path")
	add(AreaTerminal, "", m.SettingsLogFormat(), "log format text html")
	add(AreaTerminal, "", m.SettingsLogTimestamps(), "log timestamps clock")
	add(AreaTerminal, "", m.SettingsOSC52Title(), "osc52 osc 52 clipboard selection")
	add(AreaTerminal, "", m.SettingsOSCNotificationsTitle(), "osc notifications notification mode")
	add(AreaTerminal, "", m.SettingsTerminalEncoding(), "encoding utf8 charset")
	add(AreaTerminal, "", m.SettingsPasteProtection(), "paste protection multiline warning")
	add(AreaTerminal, "", m.SettingsPasteMinLines(), "paste protection threshold lines")
	add(AreaTerminal, "", m.SettingsDropAction(), "drag drop file insert upload")
	add(AreaTerminal, "", m.SettingsAutocompleteTitle(), "autocomplete completion suggestions")
	add(AreaTerminal, "", m.SettingsAutocompleteEnabled(), "autocomplete enabled")
	add(AreaTerminal, "", m.SettingsAutocompletePopupMenu(), "autocomplete popup menu")
	add(AreaTerminal, "", m.SettingsAutocompleteGhostText(), "autocomplete ghost text inline")
	add(AreaTerminal, "", m.SettingsAutocompleteMinChars(), "autocomplete minimum characters")
	add(AreaTerminal, "", m.SettingsAutocompleteMaxSuggestions(), "autocomplete maximum suggestions")
	add(AreaTerminal, "", m.SettingsAutocompleteDebounce(), "autocomplete debounce delay")
	add(AreaTerminal, "", m.SettingsAutocompleteHistoryScope(), "autocomplete history scope session global")
	add(AreaTerminal, "", m.SettingsKeywordHighlightTitle(), "keyword highlight rules terminal")
	add(AreaTerminal, "", m.SettingsKeywordHighlightEnabled(), "keyword highlight enabled")
	add(AreaTerminal, "", m.SettingsKeywordHighlightPatterns(), "keyword highlight patterns regex")
	add(AreaTerminal, "", m.SettingsShowSessionStats(), "session statistics stats cpu memory")
	add(AreaTerminal, "", m.SettingsRetentionTitle(), "retention history logs cleanup")
	add(AreaTerminal, "", m.SettingsConnectionLogRetentionCount(), "connection log retention count")
	add(AreaTerminal, "", m.SettingsConnectionLogRetentionDays(), "connection log retention days")
	add(AreaTerminal, "", m.SettingsHistoryRetentionCount(), "command history retention count")
	add(AreaTerminal, "", m.SettingsHistoryRetentionDays(), "command history retention days")

	add(AreaSFTPEditor, domain.FieldDoubleClickBehavior, m.SettingsDoubleClickBehavior(), "double click open transfer")
	add(AreaSFTPEditor, domain.FieldAutoSync, m.SettingsAutoSync(), "sync directories")
	add(AreaSFTPEditor, domain.FieldShowHiddenFiles, m.SettingsShowHiddenFiles(), "hidden files")
	add(AreaSFTPEditor, domain.FieldUseCompressedUpload, m.SettingsUseCompressedUpload(), "compress upload")
	add(AreaSFTPEditor, domain.FieldAutoOpenSidebar, m.SettingsAutoOpenSidebar(), "side panel")
	add(AreaSFTPEditor, domain.FieldFollowTerminalCwd, m.SettingsFollowTerminalCwd(), "working directory")
	add(AreaSFTPEditor, domain.FieldDefaultViewMode, m.SettingsDefaultViewMode(), "view list tree")
	add(AreaSFTPEditor, domain.FieldEditorWordWrap, m.SettingsEditorWordWrap(), "editor wrap")
	add(AreaSFTPEditor, domain.FieldEditorFontSize, m.SettingsEditorFontSize(), "editor font size")

	add(AreaLogs, domain.FieldSSHDebug, m.SettingsSSHDebug(), "ssh debug logs")

	add(AreaSystem, "", m.SettingsHotkeyTitle(), "global hotkey toggle window")
	add(AreaSystem, "", m.SettingsAutostartTitle(), "launch login autostart")
	add(AreaSystem, "", m.SettingsShowTrayIcon(), "tray icon")
	add(AreaSystem, "", m.SettingsCloseToTray(), "close to tray")
	add(AreaSystem, "", m.SettingsDeepLinksTitle(), "ssh deep links")
	add(AreaSystem, "", m.SettingsNotificationsTitle(), "notifications")

	add(AreaSecurity, domain.FieldConfirmCloseSessions, m.SettingsConfirmCloseSessions(), "confirm close sessions")
	add(AreaSecurity, domain.FieldConfirmDestructive, m.SettingsConfirmDestructive(), "confirm destructive vault")
	add(AreaSecurity, "", m.SettingsAppLockTitle(), "app lock password")
	add(AreaSecurity, "", m.SettingsAppLockTimeout(), "timeout inactivity")

	add(AreaSync, domain.FieldSyncEnabled, m.SettingsSyncEnabled(), "cloud sync enabled")
	add(AreaSync, domain.FieldSyncAutoSync, m.SettingsSyncAutoSync(), "auto sync")
	add(AreaSync, domain.FieldSyncConflictStrategy, m.SettingsSyncConflictStrategy(), "conflict strategy")
	add(AreaSync, domain.FieldSyncProvider, m.SettingsSyncProvider(), "provider")

	add(AreaShortcuts, "", m.SettingsShortcutScheme(), "scheme mac pc disabled")
	add(AreaShortcuts, "", m.SettingsShortcutResetAll(), "reset all shortcuts")
	for _, binding := range catalogBindings() {
		keywords := []string{binding.ID, binding.Action, string(binding.Category)}
		if len(sources) == 0 {
			// Without the live document, the catalog defaults are the only
			// chords there are.
			keywords = append(keywords, binding.Mac, compactChord(binding.Mac),
				binding.PC, compactChord(binding.PC))
		}
		for _, source := range sources {
			// Index the effective chords only: a chord the user replaced is
			// no longer a way to reach this binding.
			for _, scheme := range []domainkb.Scheme{domainkb.SchemeMac, domainkb.SchemePC} {
				chord := source.ChordFor(binding.ID, scheme)
				keywords = append(keywords, chord, compactChord(chord))
			}
		}
		add(AreaShortcuts, "", bindingLabel(m, binding), keywords...)
	}
	for _, source := range sources {
		for _, menu := range source.MenuShortcuts() {
			if menu.Derived || menu.Label == "" || menu.Accelerator == "" {
				continue
			}
			keywords := []string{menu.ID, "menu", "shortcut"}
			keywords = append(keywords, acceleratorSearchTerms(menu.Accelerator)...)
			add(AreaShortcuts, "", menu.Label, keywords...)
		}
	}
	return hits
}

// compactChord removes the spacing of a display chord, so a query typed as
// Ctrl+Shift+B matches Ctrl + Shift + B.
func compactChord(chord string) string {
	return strings.ReplaceAll(chord, " ", "")
}

// acceleratorSearchTerms spells a native menu accelerator the ways a user may
// type it: as stored, without spaces, and with CmdOrCtrl resolved to either
// platform's modifier.
func acceleratorSearchTerms(accelerator string) []string {
	terms := []string{accelerator, compactChord(accelerator)}
	for _, modifier := range []string{"Ctrl", "Cmd", "Command", "Super"} {
		resolved := strings.ReplaceAll(accelerator, "CmdOrCtrl", modifier)
		terms = append(terms, resolved, compactChord(resolved))
	}
	return terms
}

// FilterHits returns the hits whose localized label or keywords contain
// query, case-insensitively. An empty query returns every hit.
func FilterHits(hits []SearchHit, query string) []SearchHit {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return hits
	}
	// A chord may be typed with or without spaces, but a label keeps its
	// words, so try the query as typed first and its compact chord form
	// second.
	compactQuery := compactChord(query)
	out := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		haystack := strings.ToLower(hit.Label + " " + hit.keywords + " " + hit.AreaID)
		matched := strings.Contains(haystack, query)
		if !matched && compactQuery != query {
			matched = strings.Contains(compactChord(haystack), compactQuery)
		}
		if matched {
			out = append(out, hit)
		}
	}
	return out
}

// SearchState owns the query, the selected area, and the highlighted match.
// It is mutated only on the UI thread.
type SearchState struct {
	query     string
	area      Area
	selected  int
	lastQuery string
}

// NewSearchState returns the Settings search with the first area selected.
func NewSearchState() *SearchState { return &SearchState{area: AreaAppearance} }

// Query returns the search field's bound value.
func (s *SearchState) Query() *string { return &s.query }

// Clear empties the query and the highlight.
func (s *SearchState) Clear() {
	s.query = ""
	s.selected = 0
}

// Filtering reports whether a query is active.
func (s *SearchState) Filtering() bool { return strings.TrimSpace(s.query) != "" }

// Area returns the selected area.
func (s *SearchState) Area() Area { return s.area }

// SetArea selects an area and clears the search, so the area stays visible.
func (s *SearchState) SetArea(area Area) {
	s.area = area
	s.query = ""
	s.selected = 0
}

// Selected returns the highlighted match index.
func (s *SearchState) Selected() int { return s.selected }

// Clamp resets the highlight when the query changed and keeps it inside the
// current result set.
func (s *SearchState) Clamp(count int) {
	if s.query != s.lastQuery {
		s.selected = 0
		s.lastQuery = s.query
	}
	if count <= 0 {
		s.selected = 0
		return
	}
	if s.selected >= count {
		s.selected = count - 1
	}
}

// Move shifts the highlight within count matches, clamped at both ends. It
// reports whether the highlight changed.
func (s *SearchState) Move(delta, count int) bool {
	if count <= 0 {
		s.selected = 0
		return false
	}
	next := s.selected + delta
	if next < 0 {
		next = 0
	}
	if next >= count {
		next = count - 1
	}
	if next == s.selected {
		return false
	}
	s.selected = next
	return true
}

// OpenHit selects the area of the highlighted match and clears the search.
// It reports whether there was a match.
func (s *SearchState) OpenHit(hits []SearchHit) bool {
	if len(hits) == 0 {
		return false
	}
	index := s.selected
	if index < 0 || index >= len(hits) {
		index = 0
	}
	s.area = hits[index].Area
	s.Clear()
	return true
}

// HitAt returns the match at index, or false.
func HitAt(hits []SearchHit, index int) (SearchHit, bool) {
	if index < 0 || index >= len(hits) {
		return SearchHit{}, false
	}
	return hits[index], true
}
