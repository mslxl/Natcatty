package preferences

import (
	"slices"
	"testing"
)

func TestFileExtensionReadsTheLastSuffix(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"report.txt", "txt"},
		{"REPORT.TXT", "txt"},
		{"archive.tar.gz", "gz"},
		{"notes", ""},
		{".bashrc", ""},
		{"name.", ""},
		{"/remote/dir/Report.PDF", "pdf"},
		{`C:\work\Report.CSV`, "csv"},
		{"dir.with.dots/file", ""},
	}
	for _, test := range tests {
		if got := FileExtension(test.name); got != test.want {
			t.Errorf("FileExtension(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNormalizeExtension(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"txt", "txt"},
		{" .TXT ", "txt"},
		{"*.TXT", "txt"},
		{"..tar", "tar"},
		{"c++", "c++"},
		{"", ""},
		{"../txt", ""},
		{"a/b", ""},
		{"tar.gz", ""},
		{".", ""},
	}
	for _, test := range tests {
		if got := NormalizeExtension(test.in); got != test.want {
			t.Errorf("NormalizeExtension(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestOpenerResolutionPrefersTheAssociationThenTheDefault(t *testing.T) {
	editor := SFTPEditor{
		DefaultOpener: Opener{Kind: OpenerSystem},
		FileAssociations: []FileAssociation{
			{Extension: "txt", Kind: OpenerInternal},
			{Extension: "png", Kind: OpenerExternal,
				Application: ExternalApplication{Path: `C:\Tools\viewer.exe`, Name: "Viewer"}},
		},
	}

	if got := editor.OpenerFor("notes.TXT"); got.Kind != OpenerInternal {
		t.Errorf("OpenerFor(notes.TXT) = %+v, want the internal editor", got)
	}
	if got := editor.OpenerFor("photo.png"); got.Kind != OpenerExternal ||
		got.Application.Name != "Viewer" {
		t.Errorf("OpenerFor(photo.png) = %+v, want the external viewer", got)
	}
	// A known extension is not the only path: anything unlisted and anything
	// with no extension goes to the default opener.
	for _, name := range []string{"notes.md", "README", ".env", "report."} {
		if got := editor.OpenerFor(name); got.Kind != OpenerSystem {
			t.Errorf("OpenerFor(%q) = %+v, want the default system opener", name, got)
		}
	}
}

func TestZeroValueSFTPEditorResolvesToTheSystemOpener(t *testing.T) {
	var editor SFTPEditor
	if got := editor.OpenerFor("report.txt"); got.Kind != OpenerSystem {
		t.Errorf("a zero-value editor resolves to %+v, want the system opener", got)
	}
}

func TestSetAssociationAddsReplacesAndDoesNotAlias(t *testing.T) {
	base := Defaults().SFTPEditor
	withText, ok := base.SetAssociation(FileAssociation{Extension: ".TXT", Kind: OpenerInternal})
	if !ok {
		t.Fatal("SetAssociation refused a valid association")
	}
	if len(base.FileAssociations) != 0 {
		t.Fatal("SetAssociation mutated the receiver's slice")
	}
	if got, want := withText.FileAssociations,
		[]FileAssociation{{Extension: "txt", Kind: OpenerInternal}}; !slices.Equal(got, want) {
		t.Fatalf("associations = %+v, want %+v", got, want)
	}

	replaced, ok := withText.SetAssociation(FileAssociation{Extension: "txt", Kind: OpenerExternal,
		Application: ExternalApplication{Path: "/usr/bin/editor"}})
	if !ok {
		t.Fatal("SetAssociation refused a replacement")
	}
	if len(replaced.FileAssociations) != 1 {
		t.Fatalf("replacing produced %d associations, want 1", len(replaced.FileAssociations))
	}
	if got := replaced.FileAssociations[0]; got.Kind != OpenerExternal ||
		got.Application.Path != "/usr/bin/editor" {
		t.Errorf("replacement = %+v, want the external editor", got)
	}
	if got := withText.FileAssociations[0]; got.Kind != OpenerInternal {
		t.Error("replacing mutated the previous value")
	}
}

func TestSetAssociationRefusesIncompleteExternalApplications(t *testing.T) {
	base := Defaults().SFTPEditor
	for _, assoc := range []FileAssociation{
		{Extension: "", Kind: OpenerSystem},
		{Extension: "txt", Kind: "bogus"},
		{Extension: "txt", Kind: OpenerExternal},
		{Extension: "a/b", Kind: OpenerSystem},
	} {
		if _, ok := base.SetAssociation(assoc); ok {
			t.Errorf("SetAssociation accepted %+v", assoc)
		}
	}
}

func TestRemoveAssociationIgnoresUnknownExtensions(t *testing.T) {
	editor, ok := Defaults().SFTPEditor.SetAssociation(
		FileAssociation{Extension: "txt", Kind: OpenerInternal})
	if !ok {
		t.Fatal("SetAssociation refused a valid association")
	}
	editor, ok = editor.SetAssociation(FileAssociation{Extension: "md", Kind: OpenerSystem})
	if !ok {
		t.Fatal("SetAssociation refused a valid association")
	}

	removed := editor.RemoveAssociation(".TXT")
	if got, want := removed.FileAssociations,
		[]FileAssociation{{Extension: "md", Kind: OpenerSystem}}; !slices.Equal(got, want) {
		t.Fatalf("after removal associations = %+v, want %+v", got, want)
	}
	unchanged := removed.RemoveAssociation("unknown")
	if !slices.Equal(unchanged.FileAssociations, removed.FileAssociations) {
		t.Error("removing an unknown extension changed the list")
	}
	if len(editor.FileAssociations) != 2 {
		t.Error("RemoveAssociation mutated the receiver's slice")
	}
}

func TestNormalizeRepairsOpenersAndAssociations(t *testing.T) {
	settings := Defaults()
	settings.SFTPEditor.DefaultOpener = Opener{
		Kind:        OpenerExternal,
		Application: ExternalApplication{Path: "  ", Name: "Ghost"},
	}
	settings.SFTPEditor.FileAssociations = []FileAssociation{
		{Extension: ".TXT", Kind: OpenerInternal},
		{Extension: "PNG", Kind: OpenerExternal,
			Application: ExternalApplication{Path: `C:\Viewer.exe`, Name: "Viewer"}},
		{Extension: "txt", Kind: OpenerSystem},
		{Extension: "", Kind: OpenerSystem},
		{Extension: "doc", Kind: "bogus"},
		{Extension: "bin", Kind: OpenerExternal},
		{Extension: "..png", Kind: OpenerSystem,
			Application: ExternalApplication{Path: "stale", Name: "stale"}},
	}

	normalized := Normalize(settings)
	if got := normalized.SFTPEditor.DefaultOpener; got.Kind != OpenerSystem ||
		got.Application.Path != "" || got.Application.Name != "" {
		t.Errorf("default opener = %+v, want the system opener with no application", got)
	}
	if got, want := normalized.SFTPEditor.FileAssociations, []FileAssociation{
		{Extension: "txt", Kind: OpenerSystem},
		{Extension: "png", Kind: OpenerSystem},
	}; !slices.Equal(got, want) {
		t.Fatalf("associations = %+v, want %+v", got, want)
	}
}

func TestNormalizeEmptyAssociationsStayNil(t *testing.T) {
	settings := Defaults()
	settings.SFTPEditor.FileAssociations = []FileAssociation{}
	normalized := Normalize(settings)
	if normalized.SFTPEditor.FileAssociations != nil {
		t.Errorf("empty associations normalized to %#v, want nil", normalized.SFTPEditor.FileAssociations)
	}
	if !Equal(normalized, Defaults()) {
		t.Error("a file with an empty association list no longer equals the defaults")
	}
}

func TestDefaultOpenerIsAResettableField(t *testing.T) {
	settings := Defaults()
	settings.SFTPEditor.DefaultOpener = Opener{Kind: OpenerInternal}
	settings.SFTPEditor.FileAssociations = []FileAssociation{
		{Extension: "txt", Kind: OpenerInternal},
	}

	reset := ResetField(settings, FieldDefaultOpener)
	if got := reset.SFTPEditor.DefaultOpener; got.Kind != OpenerSystem {
		t.Errorf("default opener after reset = %+v, want the system opener", got)
	}
	if len(reset.SFTPEditor.FileAssociations) != 1 {
		t.Error("resetting the default opener removed the associations")
	}
	if group, ok := GroupOf(FieldDefaultOpener); !ok || group != GroupSFTPEditor {
		t.Errorf("GroupOf(FieldDefaultOpener) = %q, %v", group, ok)
	}
	if !slices.Contains(Fields(), FieldDefaultOpener) {
		t.Error("Fields does not list the default opener")
	}
	if Defaults().SFTPEditor.DefaultOpener.Kind != OpenerSystem {
		t.Error("the default opener no longer defaults to the system editor")
	}
}
