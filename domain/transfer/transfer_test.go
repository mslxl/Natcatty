package transfer

import (
	"testing"
	"time"
)

func entry(name string, kind Kind, size int64, modified int64) Entry {
	return Entry{
		Name:    name,
		Path:    "/x/" + name,
		Kind:    kind,
		Size:    size,
		ModTime: time.Unix(modified, 0),
	}
}

func names(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Name
	}
	return out
}

func equalNames(got []Entry, want ...string) bool {
	gotNames := names(got)
	if len(gotNames) != len(want) {
		return false
	}
	for i := range want {
		if gotNames[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSortKeepsDirectoriesFirstByDefault(t *testing.T) {
	entries := []Entry{
		entry("dir-a", KindDir, 0, 100),
		entry("newest.log", KindFile, 0, 300),
		entry("dir-b", KindDir, 0, 200),
	}
	sorted := SortEntries(entries, Sort{Field: SortModified, Descending: true})
	if !equalNames(sorted, "dir-b", "dir-a", "newest.log") {
		t.Fatalf("sorted = %v", names(sorted))
	}
}

func TestSortCanMixKindsWhenTheOptionIsOff(t *testing.T) {
	entries := []Entry{
		entry("dir-a", KindDir, 0, 100),
		entry("newest.log", KindFile, 0, 300),
		entry("dir-b", KindDir, 0, 200),
	}
	sorted := SortEntries(entries, Sort{Field: SortModified, Descending: true, MixKinds: true})
	if !equalNames(sorted, "newest.log", "dir-b", "dir-a") {
		t.Fatalf("sorted = %v", names(sorted))
	}
}

func TestSortByTypeGroupsExtensionsAndKeepsDirectoriesFirst(t *testing.T) {
	entries := []Entry{
		entry("BUILD", KindFile, 0, 100),
		entry("src", KindDir, 0, 100),
		entry("archive.zip", KindFile, 0, 100),
	}
	sorted := SortEntries(entries, Sort{Field: SortType})
	if !equalNames(sorted, "src", "BUILD", "archive.zip") {
		t.Fatalf("ascending = %v", names(sorted))
	}
	sorted = SortEntries(entries, Sort{Field: SortType, Descending: true})
	if !equalNames(sorted, "src", "archive.zip", "BUILD") {
		t.Fatalf("descending = %v", names(sorted))
	}
}

func TestSortByNameIsCaseInsensitiveAndStable(t *testing.T) {
	entries := []Entry{
		entry("b.txt", KindFile, 0, 1),
		entry("A.txt", KindFile, 0, 1),
		entry("a.txt", KindFile, 0, 1),
	}
	sorted := SortEntries(entries, Sort{})
	if sorted[0].Name != "A.txt" || sorted[1].Name != "a.txt" || sorted[2].Name != "b.txt" {
		t.Fatalf("sorted = %v", names(sorted))
	}
}

func TestSortBySizeAndOwner(t *testing.T) {
	entries := []Entry{
		{Name: "big", Kind: KindFile, Size: 20, Owner: "www-data"},
		{Name: "small", Kind: KindFile, Size: 5, Owner: "root"},
	}
	if got := SortEntries(entries, Sort{Field: SortSize}); !equalNames(got, "small", "big") {
		t.Fatalf("size order = %v", names(got))
	}
	if got := SortEntries(entries, Sort{Field: SortOwner}); !equalNames(got, "small", "big") {
		t.Fatalf("owner order = %v", names(got))
	}
}

func TestSortDoesNotMutateTheInput(t *testing.T) {
	entries := []Entry{entry("b", KindFile, 0, 0), entry("a", KindFile, 0, 0)}
	_ = SortEntries(entries, Sort{})
	if entries[0].Name != "b" {
		t.Fatal("SortEntries mutated its input")
	}
}

func TestHiddenRulesKeepParentEntryVisible(t *testing.T) {
	cases := []struct {
		name        string
		hidden      bool
		show        bool
		wantShown   bool
		description string
	}{
		{"..", false, false, true, "parent entry"},
		{".bashrc", false, false, false, "dotfile"},
		{".bashrc", false, true, true, "dotfile with hidden shown"},
		{"secret.txt", true, false, false, "Windows hidden attribute"},
		{"secret.txt", true, true, true, "Windows hidden attribute with hidden shown"},
		{"plain.txt", false, false, true, "ordinary file"},
	}
	for _, test := range cases {
		entry := Entry{Name: test.name, Hidden: test.hidden}
		if got := Visible(entry, test.show); got != test.wantShown {
			t.Errorf("%s: Visible(%q, %v) = %v, want %v", test.description, test.name, test.show, got, test.wantShown)
		}
	}
}

func TestFormatModeRendersLsStyle(t *testing.T) {
	cases := []struct {
		entry Entry
		want  string
	}{
		{Entry{Kind: KindDir, Mode: 0o755}, "drwxr-xr-x"},
		{Entry{Kind: KindFile, Mode: 0o644}, "-rw-r--r--"},
		{Entry{Kind: KindFile, Mode: 0o4755}, "-rwsr-xr-x"},
		{Entry{Kind: KindDir, Mode: 0o1777}, "drwxrwxrwt"},
		{Entry{Kind: KindLink, Symlink: true, Mode: 0o777}, "lrwxrwxrwx"},
		{Entry{Kind: KindOther, Mode: 0}, "?---------"},
	}
	for _, test := range cases {
		if got := FormatMode(test.entry); got != test.want {
			t.Errorf("FormatMode(%+v) = %q, want %q", test.entry, got, test.want)
		}
	}
}

func TestParseModeAcceptsOctalAndSymbolic(t *testing.T) {
	cases := map[string]uint32{
		"644":        0o644,
		"0644":       0o644,
		"0o755":      0o755,
		"4755":       0o4755,
		"rwxr-xr-x":  0o755,
		"drwxr-xr-x": 0o755,
		"rwsr-xr-x":  0o4755,
	}
	for text, want := range cases {
		got, err := ParseMode(text)
		if err != nil {
			t.Errorf("ParseMode(%q) error = %v", text, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %04o, want %04o", text, got, want)
		}
	}
}

func TestParseModeRejectsInvalidValues(t *testing.T) {
	for _, text := range []string{"", "8", "999", "77777", "abc", "rwxrwxrw"} {
		if _, err := ParseMode(text); err == nil {
			t.Errorf("ParseMode(%q) accepted an invalid mode", text)
		}
	}
}

func TestValidateNameRefusesSeparatorsAndReservedNames(t *testing.T) {
	for _, name := range []string{"", " ", ".", "..", "a/b", `a\b`, "a\x00b", "a\nb"} {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted an invalid name", name)
		}
	}
	for _, name := range []string{"a", "report.txt", ".bashrc", "with space", "ünïcode"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
}

func TestCopyNameAddsCopySuffixBeforeTheExtension(t *testing.T) {
	taken := map[string]bool{"report.txt": true, "report (copy 1).txt": true}
	got := CopyName("report.txt", func(name string) bool { return taken[name] })
	if got != "report (copy 2).txt" {
		t.Fatalf("CopyName = %q", got)
	}
	if got := CopyName(".bashrc", func(name string) bool { return name == ".bashrc" }); got != ".bashrc (copy 1)" {
		t.Fatalf("dotfile CopyName = %q", got)
	}
	if got := CopyName("free.txt", func(string) bool { return false }); got != "free.txt" {
		t.Fatalf("free CopyName = %q", got)
	}
}

func TestBookmarkSetDeduplicatesByLocationAndKeepsOrder(t *testing.T) {
	set := NewBookmarkSet([]Bookmark{
		{Label: "Home", Path: "/home/user"},
		{Label: "Again", Path: "/home/user/"},
		{Label: "Root", Path: "/", HostID: "host-1", HostLabel: "box"},
	})
	if set.Len() != 2 {
		t.Fatalf("Len = %d, want 2", set.Len())
	}
	items := set.Items()
	if items[0].Label != "Again" {
		t.Fatalf("first label = %q, want the replaced label", items[0].Label)
	}
	if !set.Contains(Bookmark{Path: "/home/user"}) {
		t.Fatal("trailing separator changed the key")
	}
	if set.Contains(Bookmark{Path: "/", HostID: "host-2"}) {
		t.Fatal("different hosts shared a key")
	}
	if !set.Toggle(Bookmark{Path: "/tmp"}) {
		t.Fatal("Toggle did not add")
	}
	if set.Toggle(Bookmark{Path: "/tmp"}) {
		t.Fatal("Toggle did not remove")
	}
}

func TestBookmarkSetIgnoresEmptyPaths(t *testing.T) {
	set := NewBookmarkSet(nil)
	if set.Add(Bookmark{Label: "x"}) {
		t.Fatal("an empty path was added")
	}
	if set.Len() != 0 {
		t.Fatalf("Len = %d", set.Len())
	}
}
