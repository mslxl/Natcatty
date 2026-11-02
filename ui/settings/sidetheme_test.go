package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakeLook struct{ settings domain.Settings }

func (f *fakeLook) Settings() domain.Settings { return f.settings }

func (f *fakeLook) Update(change func(*domain.Settings)) error {
	change(&f.settings)
	return nil
}

func TestThemePanelChangesTheSchemeAndFontSize(t *testing.T) {
	m := locales.In("en")
	catalog := &fakeThemeCatalog{themes: []domaintheme.TerminalTheme{
		{ID: domain.DefaultTerminalTheme, Name: "Default Scheme"},
		{ID: "solarized", Name: "Solarized Dark"},
		{ID: "paper", Name: "Paper Light", Custom: true},
	}}
	look := &fakeLook{settings: domain.Defaults()}
	look.settings.Appearance.TerminalFontSize = domain.MaxTerminalFontSize - 1
	tt := ui.NewTester(NewThemePanel(NewThemesState(catalog), look).View(), 360, 700)
	tt.SetLocale("en")
	tt.Frame()

	for _, want := range []string{"Solarized Dark", "Paper Light", m.SidePanelThemeCustom()} {
		if !tt.HasText(want) {
			t.Fatalf("the Theme tool lacks %q, texts %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Solarized Dark"); err != nil {
		t.Fatalf("click a scheme: %v", err)
	}
	tt.Frame()
	if got := look.settings.Appearance.TerminalTheme; got != "solarized" {
		t.Fatalf("the terminal theme is %q, want solarized", got)
	}

	for range 2 {
		if err := tt.Click(m.SidePanelFontLarger()); err != nil {
			t.Fatalf("click Larger: %v", err)
		}
		tt.Frame()
	}
	if got := look.settings.Appearance.TerminalFontSize; got != domain.MaxTerminalFontSize {
		t.Fatalf("the font size is %v, want it held at %v", got, domain.MaxTerminalFontSize)
	}
	if err := tt.Click(m.SidePanelFontSmaller()); err != nil {
		t.Fatalf("click Smaller: %v", err)
	}
	tt.Frame()
	if got := look.settings.Appearance.TerminalFontSize; got != domain.MaxTerminalFontSize-1 {
		t.Fatalf("the font size is %v after Smaller", got)
	}
}
