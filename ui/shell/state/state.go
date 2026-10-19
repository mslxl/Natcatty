// Package state owns the main window's shell state: the ordered work tabs and
// the requests the shell makes of the app. Window-scoped state lives in
// ui/<area>/state, as ADR 0006 requires, apart from the view that renders it.
package state

// Tab is one work tab in the shell. Its number is stable for its lifetime and
// is what the title shows. ADR 0006 gives tabs a name and a session later; the
// walking skeleton only numbers them.
type Tab struct {
	Number int
}

// State is the main window's shell state. It is mutated only on the UI thread.
type State struct {
	tabs            []Tab
	active          int
	nextTabNumber   int
	requestLocale   func(string)
	requestSettings func()
}

// Option customizes a State.
type Option func(*State)

// WithLocaleRequest sets the function called when the shell asks the app to
// switch languages. The app wires this to mygo.App.SetLocale.
func WithLocaleRequest(fn func(string)) Option {
	return func(s *State) { s.requestLocale = fn }
}

// WithSettingsRequest sets the function called when the user opens Settings.
// The app wires this to the singleton Settings window.
func WithSettingsRequest(fn func()) Option {
	return func(s *State) { s.requestSettings = fn }
}

// NewState returns the initial shell state: no work tabs.
func NewState(opts ...Option) *State {
	s := &State{active: -1, nextTabNumber: 1}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// TabCount returns the number of work tabs.
func (s *State) TabCount() int { return len(s.tabs) }

// ActiveTab returns the index of the active work tab, or -1 when there is
// none.
func (s *State) ActiveTab() int { return s.active }

// TabNumbers returns the stable numbers of the work tabs, in order.
func (s *State) TabNumbers() []int {
	numbers := make([]int, len(s.tabs))
	for i, tab := range s.tabs {
		numbers[i] = tab.Number
	}
	return numbers
}

// SelectTab makes the tab at index active. Out-of-range indexes are ignored.
func (s *State) SelectTab(index int) {
	if index >= 0 && index < len(s.tabs) {
		s.active = index
	}
}

// NewTab appends a work tab and makes it active. It returns the tab's index.
func (s *State) NewTab() int {
	s.tabs = append(s.tabs, Tab{Number: s.nextTabNumber})
	s.nextTabNumber++
	s.active = len(s.tabs) - 1
	return s.active
}

// CloseActiveTab removes the active work tab and selects the preceding tab,
// or the new first tab. It reports whether a tab was closed.
func (s *State) CloseActiveTab() bool {
	if s.active < 0 || s.active >= len(s.tabs) {
		return false
	}
	s.tabs = append(s.tabs[:s.active], s.tabs[s.active+1:]...)
	if len(s.tabs) == 0 {
		s.active = -1
		return true
	}
	if s.active >= len(s.tabs) {
		s.active = len(s.tabs) - 1
	}
	return true
}

// RequestLocale asks the app to show the language of tag.
func (s *State) RequestLocale(tag string) {
	if s.requestLocale != nil {
		s.requestLocale(tag)
	}
}

// RequestSettings asks the app to show the singleton Settings window.
func (s *State) RequestSettings() {
	if s.requestSettings != nil {
		s.requestSettings()
	}
}
