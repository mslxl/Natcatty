package plugini18n_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mslxl/natcatty/adapters/plugini18n"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func TestLoadFSReadsFlatCatalogsAndSkipsMeta(t *testing.T) {
	fsys := fstest.MapFS{
		"i18n/en.toml": {Data: []byte(strings.Join([]string{
			`"command.greet" = "Say Hello"`,
			`"view_title" = "Greeting"`,
			`"files.one" = "{count} file"`,
			`"files.other" = "{count} files"`,
			"[meta]",
			`locale = "en"`,
			`translator = "Acme"`,
		}, "\n"))},
		"i18n/es.toml":   {Data: []byte(`"command.greet" = "Hola"`)},
		"i18n/notes.txt": {Data: []byte("ignored")},
		"plugin.toml":    {Data: []byte("ignored")},
	}
	catalogs, err := plugini18n.LoadFS(fsys, "en")
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	if got := catalogs.Tags(); !reflect.DeepEqual(got, []string{"en", "es"}) {
		t.Fatalf("tags = %v", got)
	}
	en, _, ok := catalogs.Locale("en")
	if !ok {
		t.Fatal("en catalog missing")
	}
	if len(en) != 4 {
		t.Fatalf("en messages = %v", en)
	}
	if en["command.greet"] != "Say Hello" || en["files.one"] != "{count} file" {
		t.Fatalf("en messages = %v", en)
	}
	if _, exists := en["locale"]; exists {
		t.Fatal("meta entries leaked into the catalog")
	}
	if err := catalogs.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadFSRejectsMalformedCatalogs(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			"no directory",
			map[string]string{"plugin.toml": "x"},
			`default locale "en" has no catalog`,
		},
		{
			"empty directory",
			map[string]string{"i18n/notes.txt": "x"},
			`default locale "en" has no catalog`,
		},
		{
			"invalid TOML",
			map[string]string{"i18n/en.toml": `greet = `},
			"parse TOML",
		},
		{
			"duplicate key",
			map[string]string{"i18n/en.toml": "greet = \"a\"\ngreet = \"b\"\n"},
			"has already been defined",
		},
		{
			"nested TOML table",
			map[string]string{"i18n/en.toml": "greet = { other = \"x\" }\n"},
			`key "greet" is map[string]interface {}`,
		},
		{
			"dotted bare key is a table",
			map[string]string{"i18n/en.toml": "command.greet = \"hi\"\n"},
			`key "command" is map[string]interface {}`,
		},
		{
			"non-string message",
			map[string]string{"i18n/en.toml": "greet = 42\n"},
			`key "greet" is int64, want a string`,
		},
		{
			"bad key",
			map[string]string{"i18n/en.toml": "\"Bad Key\" = \"x\"\n"},
			"does not match",
		},
		{
			"meta not a table",
			map[string]string{"i18n/en.toml": "meta = \"x\"\n"},
			"meta must be a table",
		},
		{
			"meta value not a string",
			map[string]string{"i18n/en.toml": "[meta]\nlocale = 1\n"},
			"meta.locale must be a string",
		},
		{
			"message over the byte limit",
			map[string]string{"i18n/en.toml": fmt.Sprintf("greet = %q\n",
				strings.Repeat("x", domainplugin.MaxMessageBytes+1))},
			"byte limit",
		},
		{
			"bad locale tag",
			map[string]string{
				"i18n/en.toml":        "greet = \"Hi\"\n",
				"i18n/not_a_tag.toml": "greet = \"Hi\"\n",
			},
			"BCP 47",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, data := range test.files {
				fsys[name] = &fstest.MapFile{Data: []byte(data)}
			}
			_, err := plugini18n.LoadFS(fsys, "en")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadFS error = %v, want %q", err, test.want)
			}
			var catalogErr *domainplugin.CatalogError
			if !errors.As(err, &catalogErr) {
				t.Fatalf("error type = %T, want *domainplugin.CatalogError", err)
			}
		})
	}
}

func TestParseEnforcesAggregateLimits(t *testing.T) {
	manyLocales := make(map[string][]byte, domainplugin.MaxLocales+1)
	manyLocales["i18n/en.toml"] = []byte("greet = \"Hi\"\n")
	for i := 0; i <= domainplugin.MaxLocales; i++ {
		manyLocales[fmt.Sprintf("i18n/l%02d.toml", i)] = []byte("greet = \"Hi\"\n")
	}
	_, err := plugini18n.Parse(manyLocales, "en")
	if err == nil || !strings.Contains(err.Error(), "locale limit") {
		t.Fatalf("locale limit error = %v", err)
	}

	// The whole-catalog limit is checked before any single message is parsed,
	// so a package cannot sidestep it with one huge default catalog.
	oversized := map[string][]byte{
		"i18n/en.toml": []byte(fmt.Sprintf("greet = %q\n", strings.Repeat("x", domainplugin.MaxCatalogsBytes))),
	}
	_, err = plugini18n.Parse(oversized, "en")
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("catalog size error = %v", err)
	}
}

func TestParseRejectsCatalogsOutsideTheFlatDirectory(t *testing.T) {
	_, err := plugini18n.Parse(map[string][]byte{
		"i18n/en.toml":     []byte("greet = \"Hi\"\n"),
		"i18n/sub/en.toml": []byte("greet = \"Hi\"\n"),
	}, "en")
	if err == nil || !strings.Contains(err.Error(), "directly under i18n/") {
		t.Fatalf("nested catalog error = %v", err)
	}
}

func TestParseRequiresTheDefaultCatalogCaseInsensitively(t *testing.T) {
	catalogs, err := plugini18n.Parse(map[string][]byte{
		"i18n/en.toml": []byte("greet = \"Hi\"\n"),
	}, "EN")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, _, ok := catalogs.Locale("en"); !ok {
		t.Fatal("en catalog missing")
	}
}

func TestLoadDirectoryMatchesLoadFS(t *testing.T) {
	// First-party plugins embed the same tree and use the same parser, so an
	// embedded catalog must resolve exactly like an unpacked package.
	files := map[string]string{
		"i18n/en.toml":    "greet = \"Hello, {name}!\"\n",
		"i18n/es.toml":    "greet = \"Hola, {name}!\"\n",
		"i18n/zh-CN.toml": "greet = \"你好，{name}！\"\n",
	}
	dir := t.TempDir()
	embedded := fstest.MapFS{}
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		embedded[name] = &fstest.MapFile{Data: []byte(data)}
	}
	fromDisk, err := plugini18n.LoadDirectory(dir, "en")
	if err != nil {
		t.Fatalf("LoadDirectory: %v", err)
	}
	fromEmbed, err := plugini18n.LoadFS(embedded, "en")
	if err != nil {
		t.Fatalf("LoadFS: %v", err)
	}
	if !reflect.DeepEqual(fromDisk, fromEmbed) {
		t.Fatalf("embedded = %+v, disk = %+v", fromEmbed, fromDisk)
	}
}

func TestValidateReferencedKeysRequiresDefaultTranslations(t *testing.T) {
	catalogs, err := plugini18n.Parse(map[string][]byte{
		"i18n/en.toml": []byte("command_greet = \"Greet\"\n"),
		"i18n/es.toml": []byte("command_greet = \"Saludar\"\n"),
	}, "en")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ok := []domainplugin.MessageKeyRef{{Field: "contributions.commands[0].title_key", Key: "command_greet"}}
	if err := plugini18n.ValidateReferencedKeys(catalogs, ok); err != nil {
		t.Fatalf("referenced key rejected: %v", err)
	}
	missing := append(ok, domainplugin.MessageKeyRef{
		Field: "contributions.menus[0].label_key", Key: "menu_label",
	})
	err = plugini18n.ValidateReferencedKeys(catalogs, missing)
	if err == nil || !strings.Contains(err.Error(), `manifest key "menu_label"`) ||
		!strings.Contains(err.Error(), "contributions.menus[0].label_key") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestSourceImplementsTheCatalogPort(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "i18n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "i18n", "en.toml"),
		[]byte("greet = \"Hi\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogs, err := (plugini18n.Source{}).LoadCatalogs(dir, "en")
	if err != nil {
		t.Fatalf("LoadCatalogs: %v", err)
	}
	if got := catalogs.Resolve("en", "greet", ""); got.Text != "Hi" {
		t.Fatalf("resolved = %+v", got)
	}
}
