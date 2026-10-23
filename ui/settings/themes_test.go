package settings

import (
	"context"
	"errors"
	"testing"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

type fakeThemeCatalog struct {
	themes   []domaintheme.TerminalTheme
	imported domaintheme.TerminalTheme
	err      error
}

func (f *fakeThemeCatalog) VisibleThemes() []domaintheme.TerminalTheme { return f.themes }

func (f *fakeThemeCatalog) Import(context.Context, string, []byte) (domaintheme.TerminalTheme, error) {
	if f.err != nil {
		return domaintheme.TerminalTheme{}, f.err
	}
	f.themes = append(f.themes, f.imported)
	return f.imported, nil
}

func TestThemesStateFiltersAndImports(t *testing.T) {
	catalog := &fakeThemeCatalog{
		themes: []domaintheme.TerminalTheme{
			{ID: "netcatty-dark", Name: "Netcatty Dark"},
			{ID: "solarized", Name: "Solarized Dark"},
			{ID: "light", Name: "Paper Light"},
		},
		imported: domaintheme.TerminalTheme{ID: "custom-mine", Name: "Mine", Custom: true},
	}
	var selected domaintheme.TerminalTheme
	state := NewThemesState(catalog,
		WithThemeImported(func(theme domaintheme.TerminalTheme) bool {
			selected = theme
			return true
		}))
	if got := len(state.Themes()); got != 3 {
		t.Errorf("themes = %d", got)
	}
	*state.Filter() = "dark"
	if got := state.Filtered(); len(got) != 2 || got[0].ID != "netcatty-dark" {
		t.Errorf("filtered = %+v", got)
	}
	*state.Filter() = "missing"
	if got := state.Filtered(); len(got) != 0 {
		t.Errorf("missing filter = %+v", got)
	}

	if !state.Import("Mine.itermcolors", []byte("x"), nil) {
		t.Fatalf("import failed: %v", state.Problem())
	}
	if selected.ID != "custom-mine" {
		t.Errorf("selected = %+v", selected)
	}
	if state.Problem() != ThemeImportOK || state.ImportedName() != "Mine" {
		t.Errorf("status = %q %q", state.Problem(), state.ImportedName())
	}
	if *state.Filter() != "" {
		t.Errorf("filter not cleared: %q", *state.Filter())
	}
}

func TestThemesStateImportFailuresDoNotSelect(t *testing.T) {
	cases := []struct {
		name    string
		catalog *fakeThemeCatalog
		readErr error
		want    ThemeImportProblem
	}{
		{name: "invalid", catalog: &fakeThemeCatalog{err: domaintheme.ErrInvalidITermcolors}, want: ThemeImportInvalid},
		{name: "save", catalog: &fakeThemeCatalog{err: errors.New("disk")}, want: ThemeImportFailed},
		{name: "read", catalog: &fakeThemeCatalog{}, readErr: errors.New("io"), want: ThemeImportFailed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			selected := false
			state := NewThemesState(test.catalog,
				WithThemeImported(func(domaintheme.TerminalTheme) bool { selected = true; return true }))
			if state.Import("bad.itermcolors", nil, test.readErr) {
				t.Fatal("import reported success")
			}
			if state.Problem() != test.want {
				t.Errorf("problem = %q, want %q", state.Problem(), test.want)
			}
			if selected {
				t.Error("a failed import selected a scheme")
			}
		})
	}
}

func TestThemesStateReportsSelectionSaveFailure(t *testing.T) {
	catalog := &fakeThemeCatalog{imported: domaintheme.TerminalTheme{ID: "custom-x", Name: "X"}}
	state := NewThemesState(catalog, WithThemeImported(func(domaintheme.TerminalTheme) bool { return false }))
	if state.Import("x.itermcolors", []byte("x"), nil) {
		t.Fatal("import reported success although selection failed")
	}
	if state.Problem() != ThemeImportFailed {
		t.Errorf("problem = %q", state.Problem())
	}
}
