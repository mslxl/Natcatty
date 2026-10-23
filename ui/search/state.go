// Package search renders the Quick Switcher and the command palette, and
// shares the ranked, sectioned result list with the Vault's own search.
//
// The ranking rules live in application/search; this package owns the
// interaction: the query text, the selected row, keyboard movement, and
// opening the chosen record.
package search

import (
	appsearch "github.com/mslxl/natcatty/application/search"
)

// Mode names which surface the state serves. The Quick Switcher and the
// command palette share one overlay and one query, so the mode selects the
// record source and the placeholder.
type Mode int

const (
	// ModeQuickSwitcher lists Hosts and open Sessions.
	ModeQuickSwitcher Mode = iota
	// ModeCommandPalette lists the available application commands.
	ModeCommandPalette
	// ModeVaultSearch is the Vault surface's inline search.
	ModeVaultSearch
)

// Section is one group of ranked results: a heading and the rows under it.
type Section struct {
	// Key is the stable section token the view localizes.
	Key string
	// Results keep their ranked order inside the section.
	Results []appsearch.Result
}

// Option customizes a State.
type Option func(*State)

// WithSource supplies the records for a mode in a locale. The source runs on
// every view build, so it may read live application state; the locale is what
// the view renders in, so localized records follow a language change.
func WithSource(source func(Mode, string) []appsearch.Record) Option {
	return func(s *State) { s.source = source }
}

// WithOpen handles the chosen record. Returning false leaves the surface
// open, which lets a caller report an error in place.
func WithOpen(open func(Mode, appsearch.Result) bool) Option {
	return func(s *State) { s.choose = open }
}

// WithSection groups results by a token derived from the record. The default
// groups by record kind.
func WithSection(section func(appsearch.Record) string) Option {
	return func(s *State) { s.section = section }
}

// State is the keyboard state of one search surface.
type State struct {
	showing bool
	mode    Mode
	query   string
	ranked  string
	locale  string
	index   int
	reveal  bool

	source  func(Mode, string) []appsearch.Record
	choose  func(Mode, appsearch.Result) bool
	section func(appsearch.Record) string
}

// New returns a closed state.
func New(opts ...Option) *State {
	s := &State{section: func(record appsearch.Record) string { return string(record.Kind) }}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// IsOpen reports whether the overlay is showing.
func (s *State) IsOpen() bool { return s != nil && s.showing }

// Mode returns the mode the overlay last opened with.
func (s *State) Mode() Mode {
	if s == nil {
		return ModeQuickSwitcher
	}
	return s.mode
}

// Open shows mode with an empty query, as every open surface starts fresh.
func (s *State) Open(mode Mode) {
	if s == nil {
		return
	}
	if s.showing && s.mode == mode && s.query == "" {
		return
	}
	s.showing = true
	s.mode = mode
	s.query = ""
	s.ranked = ""
	s.index = 0
	s.reveal = true
}

// Toggle opens mode, or closes it when that mode is already open. The Quick
// Switcher's chord toggles; the command palette's chord only opens.
func (s *State) Toggle(mode Mode) {
	if s == nil {
		return
	}
	if s.showing && s.mode == mode {
		s.Close()
		return
	}
	s.Open(mode)
}

// Close hides the overlay and forgets the query, so the next open starts
// from an empty field.
func (s *State) Close() {
	if s == nil {
		return
	}
	s.showing = false
	s.query = ""
	s.ranked = ""
	s.index = 0
	s.reveal = false
}

// SetLocale records the locale the view is rendering in, which localized
// record sources read.
func (s *State) SetLocale(tag string) {
	if s != nil {
		s.locale = tag
	}
}

// Locale returns the locale the last view build recorded.
func (s *State) Locale() string {
	if s == nil {
		return ""
	}
	return s.locale
}

// Query returns the current query text.
func (s *State) Query() string {
	if s == nil {
		return ""
	}
	return s.query
}

// QueryPtr is the field a text input edits in place. A change takes effect
// on the next build, where Sync resets the selection.
func (s *State) QueryPtr() *string {
	if s == nil {
		return new(string)
	}
	return &s.query
}

// Sync notices a query change typed into the bound field and starts the
// selection over at the first row.
func (s *State) Sync() {
	if s == nil || s.ranked == s.query {
		return
	}
	s.ranked = s.query
	s.index = 0
	s.reveal = true
}

// Sections ranks the current records and groups them. Sections keep the
// order in which the source lists their keys, so Hosts lead the Quick
// Switcher even when a Session scores higher, and rows inside a section
// keep their rank.
func (s *State) Sections() []Section {
	if s == nil {
		return nil
	}
	s.Sync()
	var records []appsearch.Record
	if s.source != nil {
		records = s.source(s.mode, s.locale)
	}
	ranked := appsearch.Rank(s.query, records)

	keys := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	for _, record := range records {
		key := s.sectionKey(record)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	grouped := make(map[string][]appsearch.Result, len(keys))
	for _, result := range ranked {
		key := s.sectionKey(result.Record)
		grouped[key] = append(grouped[key], result)
	}
	sections := make([]Section, 0, len(keys))
	for _, key := range keys {
		if results := grouped[key]; len(results) > 0 {
			sections = append(sections, Section{Key: key, Results: results})
		}
	}
	return sections
}

// Results flattens the sections in display order. Its index is the
// selection index.
func (s *State) Results() []appsearch.Result {
	sections := s.Sections()
	out := make([]appsearch.Result, 0, 8)
	for _, section := range sections {
		out = append(out, section.Results...)
	}
	return out
}

// Selected returns the index of the highlighted row.
func (s *State) Selected() int {
	if s == nil {
		return 0
	}
	return s.index
}

// Clamp keeps the selection inside count rows. The view calls it with the
// number of rows it just rendered.
func (s *State) Clamp(count int) {
	if s == nil {
		return
	}
	if count <= 0 {
		s.index = 0
		return
	}
	if s.index >= count {
		s.index = count - 1
	}
	if s.index < 0 {
		s.index = 0
	}
}

// Move steps the selection by delta and clamps at both ends, as the original
// Quick Switcher did: it never wraps. It reports whether the selection
// changed.
func (s *State) Move(delta int) bool {
	if s == nil {
		return false
	}
	count := len(s.Results())
	if count == 0 {
		return false
	}
	next := s.index + delta
	if next < 0 {
		next = 0
	}
	if next >= count {
		next = count - 1
	}
	if next == s.index {
		return false
	}
	s.index = next
	s.reveal = true
	return true
}

// Select highlights one row, for a click.
func (s *State) Select(index int) bool {
	if s == nil || index < 0 || index >= len(s.Results()) {
		return false
	}
	if index == s.index {
		return false
	}
	s.index = index
	s.reveal = true
	return true
}

// Activate opens the selected row. It reports whether the row was handled;
// a handled row closes the surface.
func (s *State) Activate() bool {
	if s == nil {
		return false
	}
	results := s.Results()
	if s.index < 0 || s.index >= len(results) {
		return false
	}
	if s.choose != nil && !s.choose(s.mode, results[s.index]) {
		return false
	}
	s.Close()
	return true
}

// TakeReveal reports whether the view should scroll the selected row into
// view this frame, and clears the request. Scrolling every frame would keep
// the user from scrolling the list away.
func (s *State) TakeReveal() bool {
	if s == nil || !s.reveal {
		return false
	}
	s.reveal = false
	return true
}

func (s *State) sectionKey(record appsearch.Record) string {
	if s.section != nil {
		if key := s.section(record); key != "" {
			return key
		}
	}
	return string(record.Kind)
}
