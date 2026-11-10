package plugin

import (
	"strings"
	"testing"
)

func registryVersion(version string, channel Channel) RegistryVersion {
	return RegistryVersion{
		Version: version, Channel: channel,
		PluginAPI: "^0.1", ABI: 1,
		Package: "packages/acme.notes/" + version + ".zip",
		SHA256:  strings.Repeat("a", 64), Size: 10,
	}
}

func TestChannelSelectionDefaultsToStableAndBetaIsOptIn(t *testing.T) {
	plugin := RegistryPlugin{
		ID: "acme.notes",
		Versions: []RegistryVersion{
			registryVersion("1.0.0", ChannelStable),
			registryVersion("1.2.0", ChannelStable),
			registryVersion("1.3.0-beta.1", ChannelBeta),
		},
	}
	stable, ok := plugin.Select("", ChannelStable)
	if !ok || stable.Version != "1.2.0" {
		t.Fatalf("stable selection = %+v, %v", stable, ok)
	}
	beta, ok := plugin.Select("", ChannelBeta)
	if !ok || beta.Version != "1.3.0-beta.1" {
		t.Fatalf("beta selection = %+v, %v", beta, ok)
	}
	if _, ok := plugin.Select("1.3.0-beta.1", ChannelStable); ok {
		t.Fatal("stable channel selected a beta-only version")
	}
	if explicit, ok := plugin.Select("1.0.0", ChannelStable); !ok || explicit.Version != "1.0.0" {
		t.Fatalf("explicit selection = %+v, %v", explicit, ok)
	}
	if _, ok := plugin.Select("9.9.9", ChannelStable); ok {
		t.Fatal("selected a missing version")
	}
}

func TestYankedVersionsAreHiddenFromSelection(t *testing.T) {
	yanked := registryVersion("1.2.0", ChannelStable)
	yanked.Yanked = true
	plugin := RegistryPlugin{
		ID: "acme.notes",
		Versions: []RegistryVersion{
			registryVersion("1.0.0", ChannelStable),
			yanked,
			registryVersion("1.1.0", ChannelStable),
		},
	}
	best, ok := plugin.Select("", ChannelStable)
	if !ok || best.Version != "1.1.0" {
		t.Fatalf("selection = %+v, %v, want the highest non-yanked version", best, ok)
	}
	if _, ok := plugin.Select("1.2.0", ChannelStable); ok {
		t.Fatal("a yanked version was selectable by explicit version")
	}
	if !yanked.Yanked || yanked.Selectable() {
		t.Fatal("yanked version reports selectable")
	}
}

func TestReleaseWarningsDistinguishYankedDeprecatedAndRevoked(t *testing.T) {
	version := registryVersion("1.0.0", ChannelStable)
	if warnings := version.Warnings(); len(warnings) != 0 {
		t.Fatalf("clean version warnings = %+v", warnings)
	}
	version.Yanked = true
	version.Deprecated = "use 2.0"
	version.Revoked = "security review"
	warnings := version.Warnings()
	if len(warnings) != 3 {
		t.Fatalf("warnings = %+v, want three", warnings)
	}
	if warnings[0].Kind != ReleaseWarningYanked ||
		warnings[1].Kind != ReleaseWarningDeprecated ||
		warnings[2].Kind != ReleaseWarningRevoked {
		t.Fatalf("warning order = %+v", warnings)
	}
	if warnings[1].Strong {
		t.Fatalf("deprecated warning is marked strong: %+v", warnings[1])
	}
	if !warnings[2].Strong {
		t.Fatalf("revoked warning is not marked strong: %+v", warnings[2])
	}
}

func TestRegistryVersionValidation(t *testing.T) {
	valid := registryVersion("1.0.0", ChannelStable)
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tests := []struct {
		name  string
		edit  func(*RegistryVersion)
		field string
	}{
		{"bad version", func(v *RegistryVersion) { v.Version = "1.0" }, "version"},
		{"bad channel", func(v *RegistryVersion) { v.Channel = "nightly" }, "channel"},
		{"bad hash", func(v *RegistryVersion) { v.SHA256 = "abc" }, "sha256"},
		{"negative size", func(v *RegistryVersion) { v.Size = -1 }, "size"},
		{"escaping package", func(v *RegistryVersion) { v.Package = "../x.zip" }, "package"},
		{"absolute package", func(v *RegistryVersion) { v.Package = "/x.zip" }, "package"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := registryVersion("1.0.0", ChannelStable)
			test.edit(&version)
			err := version.Validate()
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("Validate = %v, want %s error", err, test.field)
			}
		})
	}
}

func TestStableChannelRejectsPrereleaseVersions(t *testing.T) {
	stable := registryVersion("1.3.0-beta.1", ChannelStable)
	err := stable.Validate()
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("stable prerelease Validate = %v, want a beta-channel error", err)
	}
	beta := registryVersion("1.3.0-beta.1", ChannelBeta)
	if err := beta.Validate(); err != nil {
		t.Fatalf("beta prerelease Validate: %v", err)
	}
}

func TestRegistryPluginRejectsDuplicateVersions(t *testing.T) {
	plugin := RegistryPlugin{
		ID: "acme.notes",
		Versions: []RegistryVersion{
			registryVersion("1.0.0", ChannelStable),
			registryVersion("1.0.0", ChannelStable),
		},
	}
	if err := plugin.Validate(); err == nil {
		t.Fatal("duplicate versions were accepted")
	}
}

func TestSearchRegistryIndexIsCaseInsensitiveOverIdNameDescriptionAndKeywords(t *testing.T) {
	index := RegistryIndex{
		Schema: 1, Name: "Test Registry",
		Plugins: []RegistryPluginSummary{
			{ID: "acme.notes", Name: "Acme Notes", Description: "Markdown notebook",
				Keywords: []string{"writing"}},
			{ID: "acme.data", Name: "Data Viewer", Description: "Tabular data"},
		},
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"acme.data", "acme.notes"}},
		{"ACME", []string{"acme.data", "acme.notes"}},
		{"markdown", []string{"acme.notes"}},
		{"WRITING", []string{"acme.notes"}},
		{"viewer", []string{"acme.data"}},
		{"absent", nil},
	}
	for _, test := range tests {
		got := SearchRegistryIndex(index, test.query)
		if len(got) != len(test.want) {
			t.Fatalf("SearchRegistryIndex(%q) = %+v, want ids %v", test.query, got, test.want)
		}
		for i := range test.want {
			if got[i].ID != test.want[i] {
				t.Fatalf("SearchRegistryIndex(%q) = %+v, want ids %v", test.query, got, test.want)
			}
		}
	}
}
