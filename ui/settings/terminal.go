package settings

import (
	"strings"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Terminal is the terminal settings seam the Settings window drives. The
// application terminalsettings.Service implements it; tests provide a fake.
type Terminal interface {
	Settings() domainterminal.Settings
	SetLoggingEnabled(bool) error
	SetLogDirectory(string) error
	SetLogFormat(domainterminal.LogFormat) error
	SetLogTimestamps(bool) error
	SetOSC52(domainterminal.ClipboardAccess) error
	SetOSCNotifications(domainterminal.NotificationMode) error
	SetEncoding(domainterminal.Encoding) error
	SetPasteProtection(bool) error
	SetPasteMinLines(int) error
	SetDropAction(domainterminal.DropAction) error
	SetAutocompleteEnabled(bool) error
	SetAutocompleteGhostText(bool) error
	SetAutocompletePopupMenu(bool) error
	SetAutocompleteDebounceMs(int) error
	SetAutocompleteMinChars(int) error
	SetAutocompleteMaxSuggestions(int) error
	SetAutocompleteHistoryScope(domainterminal.AutocompleteHistoryScope) error
	SetKeywordHighlightEnabled(bool) error
	SetKeywordHighlightRules([]domainterminal.KeywordHighlightRule) error
	SetShowSessionStats(bool) error
	SetConnectionLogRetention(count, days int) error
	SetHistoryRetention(count, days int) error
}

// TerminalState owns the Settings window's terminal form. It is mutated only
// on the UI thread.
type TerminalState struct {
	terminal   Terminal
	directory  string
	problem    bool
	redraw     func()
	ruleDrafts map[string]string
}

// TerminalOption customizes a TerminalState.
type TerminalOption func(*TerminalState)

// WithTerminalRedrawRequest sets the function called after the form changes.
func WithTerminalRedrawRequest(fn func()) TerminalOption {
	return func(state *TerminalState) { state.redraw = fn }
}

// NewTerminalState returns the form for service.
func NewTerminalState(service Terminal, opts ...TerminalOption) *TerminalState {
	state := &TerminalState{
		terminal:   service,
		directory:  service.Settings().LogDirectory,
		ruleDrafts: make(map[string]string),
	}
	for _, rule := range service.Settings().KeywordHighlightRules {
		state.ruleDrafts[rule.ID] = strings.Join(rule.Patterns, "\n")
	}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// RulePatterns returns the editable newline-separated patterns for rule id. A
// newline, not a comma, separates them because the default patterns use RE2
// repetition counts such as {1,7} that a comma split would destroy. The
// draft is kept per rule so typing does not rewrite the whole list on every
// keystroke before the save succeeds.
func (s *TerminalState) RulePatterns(id string) *string {
	draft, ok := s.ruleDrafts[id]
	if !ok {
		for _, rule := range s.terminal.Settings().KeywordHighlightRules {
			if rule.ID == id {
				draft = strings.Join(rule.Patterns, "\n")
				break
			}
		}
		s.ruleDrafts[id] = draft
	}
	return &draft
}

// SetRulePatterns stores one rule's patterns. Blank input removes the rule.
func (s *TerminalState) SetRulePatterns(id, raw string) bool {
	s.ruleDrafts[id] = raw
	settings := s.terminal.Settings()
	rules := make([]domainterminal.KeywordHighlightRule, 0, len(settings.KeywordHighlightRules))
	for _, rule := range settings.KeywordHighlightRules {
		if rule.ID != id {
			rules = append(rules, rule)
			continue
		}
		patterns := splitPatterns(raw)
		if len(patterns) == 0 {
			continue
		}
		rule.Patterns = patterns
		rules = append(rules, rule)
	}
	return s.apply(func() error { return s.terminal.SetKeywordHighlightRules(rules) })
}

// splitPatterns splits the editor's newline-separated list. Regexes may
// legitimately contain commas, so commas are not separators.
func splitPatterns(raw string) []string {
	parts := strings.Split(raw, "\n")
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			patterns = append(patterns, trimmed)
		}
	}
	return patterns
}

// Settings returns the persisted terminal settings.
func (s *TerminalState) Settings() domainterminal.Settings { return s.terminal.Settings() }

// Directory returns the editable log directory draft.
func (s *TerminalState) Directory() *string { return &s.directory }

// Problem reports whether the last save failed.
func (s *TerminalState) Problem() bool { return s.problem }

// SetLoggingEnabled stores automatic logging.
func (s *TerminalState) SetLoggingEnabled(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetLoggingEnabled(enabled) })
}

// SetLogDirectory stores the log root.
func (s *TerminalState) SetLogDirectory(directory string) bool {
	return s.apply(func() error { return s.terminal.SetLogDirectory(directory) })
}

// SetLogFormat stores the log format.
func (s *TerminalState) SetLogFormat(format domainterminal.LogFormat) bool {
	return s.apply(func() error { return s.terminal.SetLogFormat(format) })
}

// SetLogTimestamps stores the timestamp policy.
func (s *TerminalState) SetLogTimestamps(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetLogTimestamps(enabled) })
}

// SetOSC52 stores the clipboard policy.
func (s *TerminalState) SetOSC52(access domainterminal.ClipboardAccess) bool {
	return s.apply(func() error { return s.terminal.SetOSC52(access) })
}

// SetOSCNotifications stores when program notifications appear.
func (s *TerminalState) SetOSCNotifications(mode domainterminal.NotificationMode) bool {
	return s.apply(func() error { return s.terminal.SetOSCNotifications(mode) })
}

// SetEncoding stores the default terminal encoding.
func (s *TerminalState) SetEncoding(encoding domainterminal.Encoding) bool {
	return s.apply(func() error { return s.terminal.SetEncoding(encoding) })
}

// SetPasteProtection stores the multiline paste guard.
func (s *TerminalState) SetPasteProtection(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetPasteProtection(enabled) })
}

// SetPasteMinLines stores the line count at which the guard asks.
func (s *TerminalState) SetPasteMinLines(lines int) bool {
	return s.apply(func() error { return s.terminal.SetPasteMinLines(lines) })
}

// SetDropAction stores what dropping files on a terminal does.
func (s *TerminalState) SetDropAction(action domainterminal.DropAction) bool {
	return s.apply(func() error { return s.terminal.SetDropAction(action) })
}

// SetAutocompleteEnabled shows or hides command suggestions.
func (s *TerminalState) SetAutocompleteEnabled(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetAutocompleteEnabled(enabled) })
}

// SetAutocompleteGhostText draws the first suggestion inline.
func (s *TerminalState) SetAutocompleteGhostText(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetAutocompleteGhostText(enabled) })
}

// SetAutocompletePopupMenu shows or hides the suggestion list.
func (s *TerminalState) SetAutocompletePopupMenu(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetAutocompletePopupMenu(enabled) })
}

// SetAutocompleteDebounceMs stores the suggestion delay.
func (s *TerminalState) SetAutocompleteDebounceMs(milliseconds int) bool {
	return s.apply(func() error { return s.terminal.SetAutocompleteDebounceMs(milliseconds) })
}

// SetAutocompleteMinChars stores the prefix length that opens the popup.
func (s *TerminalState) SetAutocompleteMinChars(characters int) bool {
	return s.apply(func() error { return s.terminal.SetAutocompleteMinChars(characters) })
}

// SetAutocompleteMaxSuggestions stores the result cap.
func (s *TerminalState) SetAutocompleteMaxSuggestions(count int) bool {
	return s.apply(func() error { return s.terminal.SetAutocompleteMaxSuggestions(count) })
}

// SetAutocompleteHistoryScope stores host-only or global history.
func (s *TerminalState) SetAutocompleteHistoryScope(scope domainterminal.AutocompleteHistoryScope) bool {
	return s.apply(func() error { return s.terminal.SetAutocompleteHistoryScope(scope) })
}

// SetKeywordHighlightEnabled enables or disables output highlighting.
func (s *TerminalState) SetKeywordHighlightEnabled(enabled bool) bool {
	return s.apply(func() error { return s.terminal.SetKeywordHighlightEnabled(enabled) })
}

// SetKeywordHighlightRules stores the full rule list.
func (s *TerminalState) SetKeywordHighlightRules(rules []domainterminal.KeywordHighlightRule) bool {
	if !s.apply(func() error { return s.terminal.SetKeywordHighlightRules(rules) }) {
		return false
	}
	for _, rule := range rules {
		s.ruleDrafts[rule.ID] = strings.Join(rule.Patterns, "\n")
	}
	return true
}

// SetKeywordHighlightRuleEnabled enables or disables one rule without touching
// its patterns.
func (s *TerminalState) SetKeywordHighlightRuleEnabled(id string, enabled bool) bool {
	settings := s.terminal.Settings()
	rules := make([]domainterminal.KeywordHighlightRule, len(settings.KeywordHighlightRules))
	copy(rules, settings.KeywordHighlightRules)
	for index := range rules {
		if rules[index].ID == id {
			rules[index].Enabled = enabled
		}
	}
	return s.SetKeywordHighlightRules(rules)
}

// SetShowSessionStats shows or hides the counter row.
func (s *TerminalState) SetShowSessionStats(show bool) bool {
	return s.apply(func() error { return s.terminal.SetShowSessionStats(show) })
}

// SetConnectionLogRetention stores how many connection logs are kept and for
// how many days. Zero in either field keeps every matching log.
func (s *TerminalState) SetConnectionLogRetention(count, days int) bool {
	return s.apply(func() error { return s.terminal.SetConnectionLogRetention(count, days) })
}

// SetHistoryRetention stores how much command history is kept. Zero in either
// field keeps every matching entry.
func (s *TerminalState) SetHistoryRetention(count, days int) bool {
	return s.apply(func() error { return s.terminal.SetHistoryRetention(count, days) })
}

func (s *TerminalState) apply(change func() error) bool {
	if err := change(); err != nil {
		s.problem = true
		s.requestRedraw()
		return false
	}
	s.problem = false
	s.requestRedraw()
	return true
}

func (s *TerminalState) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}
