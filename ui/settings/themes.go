package settings

import (
	"context"
	"errors"
	"strings"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// ThemeCatalog is the terminal color-scheme seam the Appearance area drives.
// The application themes service implements it; tests provide a fake.
type ThemeCatalog interface {
	VisibleThemes() []domaintheme.TerminalTheme
	Import(ctx context.Context, name string, data []byte) (domaintheme.TerminalTheme, error)
}

// ThemeImportProblem identifies the outcome of the last import.
type ThemeImportProblem string

const (
	ThemeImportNone    ThemeImportProblem = ""
	ThemeImportOK      ThemeImportProblem = "ok"
	ThemeImportInvalid ThemeImportProblem = "invalid"
	ThemeImportFailed  ThemeImportProblem = "failed"
)

// ThemesState owns the terminal color-scheme picker and its import status. It
// is mutated only on the UI thread.
type ThemesState struct {
	catalog       ThemeCatalog
	filter        string
	problem       ThemeImportProblem
	imported      string
	requestImport func()
	onImported    func(domaintheme.TerminalTheme) bool
	requestRedraw func()
}

// ThemesOption customizes a ThemesState.
type ThemesOption func(*ThemesState)

// WithThemeImportRequest sets the function that opens the native file picker.
func WithThemeImportRequest(fn func()) ThemesOption {
	return func(s *ThemesState) { s.requestImport = fn }
}

// WithThemeImported sets the function that selects an imported scheme. It
// reports whether the selection could be saved; a false result shows the
// import as failed, because the scheme is not in use.
func WithThemeImported(fn func(domaintheme.TerminalTheme) bool) ThemesOption {
	return func(s *ThemesState) { s.onImported = fn }
}

// WithThemesRedrawRequest sets the function called after the form changes.
func WithThemesRedrawRequest(fn func()) ThemesOption {
	return func(s *ThemesState) { s.requestRedraw = fn }
}

// NewThemesState returns the terminal color-scheme form for catalog.
func NewThemesState(catalog ThemeCatalog, opts ...ThemesOption) *ThemesState {
	state := &ThemesState{catalog: catalog}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Filter returns the picker's search field.
func (s *ThemesState) Filter() *string { return &s.filter }

// Themes returns every visible scheme, built-in and imported.
func (s *ThemesState) Themes() []domaintheme.TerminalTheme {
	if s == nil || s.catalog == nil {
		return nil
	}
	return s.catalog.VisibleThemes()
}

// Filtered returns the schemes whose name contains the filter.
func (s *ThemesState) Filtered() []domaintheme.TerminalTheme {
	themes := s.Themes()
	query := strings.ToLower(strings.TrimSpace(s.filter))
	if query == "" {
		return themes
	}
	out := make([]domaintheme.TerminalTheme, 0, len(themes))
	for _, theme := range themes {
		if strings.Contains(strings.ToLower(theme.Name), query) {
			out = append(out, theme)
		}
	}
	return out
}

// RequestImport opens the native file picker.
func (s *ThemesState) RequestImport() {
	if s == nil || s.requestImport == nil {
		return
	}
	s.requestImport()
}

// Import applies a file the picker returned. readErr covers a file that could
// not be read; the scheme is validated and saved before it is published, so a
// failure changes neither the catalog nor the selected scheme.
func (s *ThemesState) Import(name string, data []byte, readErr error) bool {
	if s == nil {
		return false
	}
	if readErr != nil {
		s.problem = ThemeImportFailed
		s.redraw()
		return false
	}
	if s.catalog == nil {
		s.problem = ThemeImportFailed
		s.redraw()
		return false
	}
	imported, err := s.catalog.Import(context.Background(), name, data)
	if err != nil {
		if errors.Is(err, domaintheme.ErrInvalidITermcolors) {
			s.problem = ThemeImportInvalid
		} else {
			s.problem = ThemeImportFailed
		}
		s.redraw()
		return false
	}
	if s.onImported != nil && !s.onImported(imported) {
		s.problem = ThemeImportFailed
		s.redraw()
		return false
	}
	s.problem = ThemeImportOK
	s.imported = imported.Name
	s.filter = ""
	s.redraw()
	return true
}

// Problem returns the outcome of the last import.
func (s *ThemesState) Problem() ThemeImportProblem {
	if s == nil {
		return ThemeImportNone
	}
	return s.problem
}

// ImportedName returns the name of the last imported scheme.
func (s *ThemesState) ImportedName() string {
	if s == nil {
		return ""
	}
	return s.imported
}

func (s *ThemesState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
