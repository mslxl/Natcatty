package transfer

import (
	"strings"
	"testing"
	"time"
)

func TestCopyNameUsesTheOriginalCopySuffixScheme(t *testing.T) {
	free := CopyName("report.txt", func(string) bool { return false })
	if free != "report.txt" {
		t.Fatalf("a free name was renamed to %q", free)
	}

	taken := map[string]bool{"report.txt": true}
	if got := CopyName("report.txt", func(name string) bool { return taken[name] }); got != "report (copy).txt" {
		t.Fatalf("first duplicate = %q, want %q", got, "report (copy).txt")
	}

	taken["report (copy).txt"] = true
	if got := CopyName("report.txt", func(name string) bool { return taken[name] }); got != "report (copy 2).txt" {
		t.Fatalf("second duplicate = %q, want %q", got, "report (copy 2).txt")
	}

	if got := CopyName("src", func(name string) bool { return name == "src" || name == "src (copy)" }); got != "src (copy 2)" {
		t.Fatalf("directory duplicate = %q", got)
	}
	if got := CopyName(".bashrc", func(name string) bool { return name == ".bashrc" }); got != ".bashrc (copy)" {
		t.Fatalf("dotfile duplicate = %q", got)
	}
}

func TestCopyNameFallsBackToATimestampAfterAThousandCopies(t *testing.T) {
	calls := 0
	got := CopyName("report.txt", func(string) bool {
		calls++
		// The original name and the first thousand copy names are taken.
		return calls <= 1001
	})
	if !strings.HasPrefix(got, "report (copy ") || !strings.HasSuffix(got, ").txt") {
		t.Fatalf("the thousandth duplicate = %q", got)
	}
	suffix := strings.TrimSuffix(strings.TrimPrefix(got, "report (copy "), ").txt")
	if len(suffix) < 12 {
		t.Fatalf("the fallback is not a millisecond timestamp: %q", got)
	}
	for _, digit := range suffix {
		if digit < '0' || digit > '9' {
			t.Fatalf("the fallback is not numeric: %q", got)
		}
	}
}

func TestConflictTypeAndReplaceRules(t *testing.T) {
	fileIntoFile := Conflict{IsDirectory: false, ExistingKind: ExistingFile}
	if fileIntoFile.TypeKey() != "file:file" {
		t.Fatalf("file conflict key = %q", fileIntoFile.TypeKey())
	}
	if !fileIntoFile.CanReplace() || fileIntoFile.CanMerge() {
		t.Fatalf("file/file replace=%v merge=%v", fileIntoFile.CanReplace(), fileIntoFile.CanMerge())
	}

	dirIntoDir := Conflict{IsDirectory: true, ExistingKind: ExistingDirectory}
	if dirIntoDir.TypeKey() != "directory:directory" {
		t.Fatalf("directory conflict key = %q", dirIntoDir.TypeKey())
	}
	if !dirIntoDir.CanReplace() || !dirIntoDir.CanMerge() {
		t.Fatalf("dir/dir replace=%v merge=%v", dirIntoDir.CanReplace(), dirIntoDir.CanMerge())
	}

	dirIntoFile := Conflict{IsDirectory: true, ExistingKind: ExistingFile}
	if dirIntoFile.CanReplace() || dirIntoFile.CanMerge() {
		t.Fatalf("a folder may replace or merge a file")
	}
	fileIntoDir := Conflict{IsDirectory: false, ExistingKind: ExistingDirectory}
	if fileIntoDir.CanReplace() || fileIntoDir.CanMerge() {
		t.Fatalf("a file may replace or merge a folder")
	}
	link := Conflict{IsDirectory: false, ExistingKind: ExistingSymlink}
	if !link.CanReplace() || link.CanMerge() {
		t.Fatalf("a symlink replace=%v merge=%v", link.CanReplace(), link.CanMerge())
	}
	dirIntoLink := Conflict{IsDirectory: true, ExistingKind: ExistingSymlink}
	if !dirIntoLink.CanReplace() || dirIntoLink.CanMerge() {
		t.Fatalf("an incoming folder may replace a symlink but not merge it")
	}
	folderUnknown := Conflict{IsDirectory: true, ExistingKind: ExistingUnknown}
	if folderUnknown.CanReplace() || folderUnknown.CanMerge() {
		t.Fatalf("an unknown folder destination must fail closed")
	}
	fileUnknown := Conflict{IsDirectory: false, ExistingKind: ExistingUnknown}
	if fileUnknown.CanReplace() {
		t.Fatalf("an unknown file destination must fail closed")
	}
}

func TestExistingKindOfEntry(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
		want  ExistingKind
	}{
		{"directory", Entry{Kind: KindDir}, ExistingDirectory},
		{"file", Entry{Kind: KindFile}, ExistingFile},
		{"symlink", Entry{Kind: KindDir, Symlink: true}, ExistingSymlink},
		{"other", Entry{Kind: KindOther}, ExistingUnknown},
	}
	for _, test := range cases {
		if got := ExistingKindOf(test.entry); got != test.want {
			t.Errorf("%s: ExistingKindOf = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestUnchangedTransferUsesSizeAndWholeSecondMtime(t *testing.T) {
	second := time.Unix(1_700_000_000, 0)
	if !UnchangedTransfer(10, second.Add(120*time.Millisecond), 10, second.Add(900*time.Millisecond)) {
		t.Fatal("same size and same whole second was not unchanged")
	}
	if UnchangedTransfer(10, second, 11, second) {
		t.Fatal("a different size was unchanged")
	}
	if UnchangedTransfer(10, second, 10, second.Add(time.Second)) {
		t.Fatal("a different whole second was unchanged")
	}
	if !UnchangedTransfer(0, second, 0, second) {
		// An empty file is still a file, but the original's guard only rejects
		// negative sizes; a zero-size match with a trustworthy mtime is equal.
		t.Fatal("an empty file with matching size and mtime was not unchanged")
	}
	if UnchangedTransfer(10, time.Time{}, 10, second) {
		t.Fatal("a zero source mtime was unchanged")
	}
	if UnchangedTransfer(10, second, 10, time.Time{}) {
		t.Fatal("a zero target mtime was unchanged")
	}
	if UnchangedTransfer(-1, second, -1, second) {
		t.Fatal("a negative size was unchanged")
	}
}
