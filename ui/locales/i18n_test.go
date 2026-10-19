package locales

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/i18n"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func TestCatalogsHaveIdenticalKeySetsAndArguments(t *testing.T) {
	source := tagsIn(t)
	tags := slices.Clone(Tags())
	slices.Sort(tags)
	if !slices.Equal(source, tags) {
		t.Fatalf("catalog tags = %v, want %v", source, tags)
	}

	messages := make(map[string]map[string]string, len(source))
	for _, tag := range source {
		messages[tag] = readCatalog(t, tag)
	}

	sourceKeys := keysOf(messages["en"])
	for _, tag := range source {
		keys := keysOf(messages[tag])
		if !slices.Equal(keys, sourceKeys) {
			t.Errorf("%s keys differ from en:\n  missing: %v\n  extra:   %v",
				tag, missing(sourceKeys, keys), extra(sourceKeys, keys))
			continue
		}
		for _, key := range sourceKeys {
			if strings.TrimSpace(messages[tag][key]) == "" {
				t.Errorf("%s has an empty message for %q", tag, key)
			}
			compareArgs(t, tag, key, messages["en"][key], messages[tag][key])
		}
	}
}

func TestTagsStartWithTheSourceLanguage(t *testing.T) {
	tags := Tags()
	if len(tags) == 0 || tags[0] != Catalog.Source() {
		t.Fatalf("Tags() = %v, want %q first", tags, Catalog.Source())
	}
}

func TestCatalogFormatsTabTitlesInEveryLocale(t *testing.T) {
	tests := map[string]string{
		"en":    "Tab 2",
		"es":    "Pestaña 2",
		"ru":    "Вкладка 2",
		"zh-CN": "标签页 2",
		"zh-TW": "分頁 2",
	}
	for tag, want := range tests {
		if got := In(tag).TabTitle(2); got != want {
			t.Errorf("In(%q).TabTitle(2) = %q, want %q", tag, got, want)
		}
	}
}

func tagsIn(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("*.json")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, path := range paths {
		tag := strings.TrimSuffix(filepath.Base(path), ".json")
		if tag == "" {
			t.Fatalf("catalog %q has an empty language tag", path)
		}
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return tags
}

func readCatalog(t *testing.T, tag string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(tag + ".json")
	if err != nil {
		t.Fatalf("read %s.json: %v", tag, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse %s.json: %v", tag, err)
	}
	if got, _ := raw["@@locale"].(string); got != tag {
		t.Errorf("%s.json @@locale = %q, want %q", tag, got, tag)
	}

	out := make(map[string]string)
	for key, value := range raw {
		if strings.HasPrefix(key, "@") {
			continue
		}
		if !keyPattern.MatchString(key) {
			t.Errorf("%s.json key %q does not match %s", tag, key, keyPattern)
		}
		message, ok := value.(string)
		if !ok {
			t.Errorf("%s.json key %q is %T, want a string", tag, key, value)
			continue
		}
		if _, err := i18n.ParseMessage(message); err != nil {
			t.Errorf("%s.json key %q does not parse: %v", tag, key, err)
			continue
		}
		out[key] = message
	}
	return out
}

func keysOf(messages map[string]string) []string {
	keys := make([]string, 0, len(messages))
	for key := range messages {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func compareArgs(t *testing.T, tag, key, source, translated string) {
	t.Helper()
	sourceMessage, err := i18n.ParseMessage(source)
	if err != nil {
		t.Fatalf("parse en %q: %v", key, err)
	}
	translatedMessage, err := i18n.ParseMessage(translated)
	if err != nil {
		t.Fatalf("parse %s %q: %v", tag, key, err)
	}

	sourceArgs := sourceMessage.Args()
	translatedArgs := translatedMessage.Args()
	if len(sourceArgs) != len(translatedArgs) {
		t.Errorf("%s %q has arguments %+v, want %+v", tag, key, translatedArgs, sourceArgs)
		return
	}
	for i := range sourceArgs {
		// Arg carries both the name and the ICU kind (number, date, plural,
		// ...). ADR 0009 requires both to match across locales.
		if sourceArgs[i] != translatedArgs[i] {
			t.Errorf("%s %q argument %d = %+v, want %+v", tag, key, i, translatedArgs[i], sourceArgs[i])
		}
	}
}

func missing(want, got []string) []string {
	var out []string
	for _, key := range want {
		if !slices.Contains(got, key) {
			out = append(out, key)
		}
	}
	return out
}

func extra(want, got []string) []string {
	var out []string
	for _, key := range got {
		if !slices.Contains(want, key) {
			out = append(out, key)
		}
	}
	return out
}
