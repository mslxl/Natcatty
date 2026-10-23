package themes

import (
	"context"
	"errors"
	"strings"
	"testing"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

type memoryStore struct {
	themes  []domaintheme.TerminalTheme
	loadErr error
	saveErr error
	saves   int
}

func (s *memoryStore) Load(context.Context) ([]domaintheme.TerminalTheme, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]domaintheme.TerminalTheme(nil), s.themes...), nil
}

func (s *memoryStore) Save(_ context.Context, themes []domaintheme.TerminalTheme) error {
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.themes = append([]domaintheme.TerminalTheme(nil), themes...)
	return nil
}

const validITerm = `<plist version="1.0"><dict>
<key>Background Color</key><dict>
<key>Red Component</key><real>0.05</real>
<key>Green Component</key><real>0.1</real>
<key>Blue Component</key><real>0.2</real></dict>
<key>Foreground Color</key><dict>
<key>Red Component</key><real>0.9</real>
<key>Green Component</key><real>0.9</real>
<key>Blue Component</key><real>0.9</real></dict>
</dict></plist>`

func validCustom(id, name string) domaintheme.TerminalTheme {
	return domaintheme.TerminalTheme{
		ID: id, Name: name, Dark: true, Custom: true,
		Colors: domaintheme.TerminalColors{
			Background: "#000000", Foreground: "#ffffff", Cursor: "#ffffff",
			Selection: "#264f78", Black: "#000000", Red: "#ff0000",
			Green: "#00ff00", Yellow: "#ffff00", Blue: "#0000ff",
			Magenta: "#ff00ff", Cyan: "#00ffff", White: "#cccccc",
			BrightBlack: "#666666", BrightRed: "#ff0000", BrightGreen: "#00ff00",
			BrightYellow: "#ffff00", BrightBlue: "#0000ff", BrightMagenta: "#ff00ff",
			BrightCyan: "#00ffff", BrightWhite: "#ffffff",
		},
	}
}

func TestLoadCombinesCatalogAndSanitizesTheStore(t *testing.T) {
	bad := validCustom("bad", "Bad")
	bad.Colors.Red = "red"
	store := &memoryStore{themes: []domaintheme.TerminalTheme{
		validCustom("custom-mine", "Mine"),
		validCustom("custom-mine", "Duplicate"),
		validCustom("netcatty-dark", "Builtin collision"),
		bad,
		{ID: "", Name: "Empty"},
	}}
	var hooked int
	service := NewService(store, WithChangeHook(func([]domaintheme.TerminalTheme) { hooked++ }))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	custom := service.CustomThemes()
	if len(custom) != 1 || custom[0].ID != "custom-mine" || !custom[0].Custom {
		t.Fatalf("custom themes = %+v", custom)
	}
	if hooked != 1 {
		t.Errorf("change hook called %d times, want 1", hooked)
	}
	if got := len(service.VisibleThemes()); got != 185 {
		t.Errorf("visible themes = %d, want 185", got)
	}
	if _, ok := service.Find("custom-mine"); !ok {
		t.Error("imported theme not found")
	}
	if _, ok := service.Find("netcatty-dark"); !ok {
		t.Error("built-in theme not found")
	}
}

func TestImportPersistsOnlyAfterParsingAndPublishesOnce(t *testing.T) {
	store := &memoryStore{}
	var published []domaintheme.TerminalTheme
	service := NewService(store, WithChangeHook(func(themes []domaintheme.TerminalTheme) {
		published = themes
	}))
	imported, err := service.Import(context.Background(), "My Scheme.itermcolors", []byte(validITerm))
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID != "custom-my-scheme" || imported.Name != "My Scheme" || !imported.Custom {
		t.Errorf("imported = %+v", imported)
	}
	if store.saves != 1 {
		t.Errorf("saves = %d, want 1", store.saves)
	}
	if len(store.themes) != 1 || len(published) != 1 {
		t.Errorf("stored = %d, published = %d", len(store.themes), len(published))
	}
	if _, ok := service.Find(imported.ID); !ok {
		t.Error("imported theme not published")
	}
	if got := len(service.VisibleThemes()); got != 185 {
		t.Errorf("visible themes = %d", got)
	}

	// A second import of the same name gets a distinct ID.
	second, err := service.Import(context.Background(), "My Scheme.xml", []byte(validITerm))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "custom-my-scheme-2" {
		t.Errorf("second ID = %q", second.ID)
	}
}

func TestImportFailureChangesNothing(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store)
	if _, err := service.Import(context.Background(), "x.itermcolors", []byte(validITerm)); err != nil {
		t.Fatal(err)
	}
	before := service.CustomThemes()
	if _, err := service.Import(context.Background(), "bad.itermcolors", []byte("<plist><dict></dict></plist>")); !errors.Is(err, domaintheme.ErrInvalidITermcolors) {
		t.Fatalf("invalid import err = %v", err)
	}
	if len(service.CustomThemes()) != len(before) || store.saves != 1 {
		t.Error("an invalid import changed the catalog")
	}

	store.saveErr = errors.New("disk full")
	if _, err := service.Import(context.Background(), "later.itermcolors", []byte(validITerm)); !errors.Is(err, ErrSave) {
		t.Fatalf("save failure err = %v", err)
	}
	if len(service.CustomThemes()) != len(before) {
		t.Error("a failed save published the theme")
	}
}

func TestThemeNameFromFile(t *testing.T) {
	cases := map[string]string{
		"C:\\schemes\\Solarized Dark.itermcolors": "Solarized Dark",
		"/home/me/One.xml":                        "One",
		"Plain":                                   "Plain",
		".itermcolors":                            "Imported Theme",
	}
	for input, want := range cases {
		if got := themeNameFromFile(input); got != want {
			t.Errorf("%q -> %q, want %q", input, got, want)
		}
	}
}

func TestSlugifyKeepsNonLatinNames(t *testing.T) {
	if got := slugify("My  Scheme!!"); got != "my-scheme" {
		t.Errorf("slug = %q", got)
	}
	if got := slugify("夜色"); !strings.Contains(got, "夜色") {
		t.Errorf("non-Latin slug = %q", got)
	}
	if got := slugify("!!!"); got != "" {
		t.Errorf("punctuation slug = %q", got)
	}
}

func TestStoreLoadErrorStillLeavesTheBuiltinsUsable(t *testing.T) {
	store := &memoryStore{loadErr: errors.New("io")}
	service := NewService(store)
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("load error was hidden")
	}
	if len(service.CustomThemes()) != 0 || len(service.VisibleThemes()) != 184 {
		t.Error("built-ins are unusable after a load failure")
	}
	if service.Problem() == nil {
		t.Error("load problem not recorded")
	}
}
