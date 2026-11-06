package histfile

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseZshHistoryHandlesExtendedRecordsAndContinuations(t *testing.T) {
	text := strings.Join([]string{
		": 1700000000:0;git status",
		": 1700000001:0;echo one \\",
		"two",
		"plain command",
		"",
	}, "\n")
	entries := ParseZshHistory(text)
	if len(entries) != 3 {
		t.Fatalf("entries = %+v, want 3", entries)
	}
	if entries[0].Command != "git status" || entries[0].Timestamp != 1700000000 {
		t.Fatalf("first = %+v", entries[0])
	}
	if entries[1].Command != "echo one \ntwo" {
		t.Fatalf("continuation = %q", entries[1].Command)
	}
	if entries[2].Command != "plain command" || entries[2].Timestamp != 0 {
		t.Fatalf("plain = %+v", entries[2])
	}
}

func TestParseBashHistoryGroupsTimestampedMultilineRecords(t *testing.T) {
	text := strings.Join([]string{
		"first plain",
		"#1700000000",
		"for i in 1 2; do",
		"  echo $i",
		"done",
		"#1700000100",
		"ls -la",
		"",
	}, "\n")
	entries := ParseBashHistory(text)
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Command != "first plain" {
		t.Fatalf("first = %+v", entries[0])
	}
	if entries[1].Command != "for i in 1 2; do\n  echo $i\ndone" ||
		entries[1].Timestamp != 1700000000 {
		t.Fatalf("multiline = %+v", entries[1])
	}
	if entries[2].Command != "ls -la" || entries[2].Timestamp != 1700000100 {
		t.Fatalf("last = %+v", entries[2])
	}
}

func TestParseFishHistoryUnescapesValues(t *testing.T) {
	text := strings.Join([]string{
		"- cmd: echo hello\\nworld",
		"  when: 1700000000",
		"  paths:",
		"    - /tmp",
		"- cmd: printf \\\\",
		"  when: 1700000100",
		"- cmd:",
		"",
	}, "\n")
	entries := ParseFishHistory(text)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Command != "echo hello\nworld" || entries[0].Timestamp != 1700000000 {
		t.Fatalf("first = %+v", entries[0])
	}
	if entries[1].Command != "printf \\" || entries[1].Timestamp != 1700000100 {
		t.Fatalf("second = %+v", entries[1])
	}
}

func TestHistoryReadsEveryShellHistoryFileNewestFirst(t *testing.T) {
	directory := t.TempDir()
	zsh := filepath.Join(directory, ".zsh_history")
	bash := filepath.Join(directory, ".bash_history")
	writeFile(t, zsh, ": 1700000000:0;git status\n: 1700000001:0;git log --oneline\n")
	writeFile(t, bash, "git branch\n__NCMCP_hidden\ndeploy staging\n")

	got := Default(directory).History()
	if len(got) != 4 {
		t.Fatalf("history = %+v, want 4", got)
	}
	if got[0].Command != "git log --oneline" || got[1].Command != "git status" {
		t.Fatalf("timestamped order = %+v", got)
	}
}

func TestHistoryDeduplicatesAndSkipsNetcattyCommands(t *testing.T) {
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, ".zsh_history"), strings.Join([]string{
		": 1700000000:0;ls",
		": 1700000001:0;ls",
		": 1700000002:0;echo __NCMCP_x",
		"",
	}, "\n"))
	got := Default(directory).History()
	if len(got) != 1 || got[0].Command != "ls" {
		t.Fatalf("history = %+v", got)
	}
}

func TestHistoryToleratesMissingAndUnreadableFiles(t *testing.T) {
	directory := t.TempDir()
	if got := Default(directory).History(); len(got) != 0 {
		t.Fatalf("missing files produced %+v", got)
	}

	failing := New(Options{
		Files: []File{{Path: "ignored", Kind: Zsh}},
		ReadFile: func(string) ([]byte, error) {
			return nil, errors.New("denied")
		},
	})
	if got := failing.History(); len(got) != 0 {
		t.Fatalf("unreadable file produced %+v", got)
	}
}

func TestHistoryCapsTheImport(t *testing.T) {
	directory := t.TempDir()
	var builder strings.Builder
	for index := 0; index < maxEntries+50; index++ {
		builder.WriteString(": 1700000000:0;command ")
		builder.WriteString(strconv.Itoa(index))
		builder.WriteString("\n")
	}
	writeFile(t, filepath.Join(directory, ".zsh_history"), builder.String())
	got := Default(directory).History()
	if len(got) != maxEntries {
		t.Fatalf("history = %d, want the %d cap", len(got), maxEntries)
	}
}

func TestHistoryReturnsTheDeduplicatedImportedEntries(t *testing.T) {
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, ".zsh_history"), strings.Join([]string{
		": 1700000000:0;git status",
		": 1700000002:0;make test",
		": 1700000001:0;git status",
		"",
	}, "\n"))
	source := Default(directory)
	history := source.History()
	if len(history) != 2 {
		t.Fatalf("history = %+v, want two deduplicated entries", history)
	}
	if history[0].Command != "make test" || history[1].Command != "git status" {
		t.Fatalf("history order = %+v, want newest first", history)
	}
	// The returned slice is a copy: callers importing it into the shared
	// command history must not corrupt later suggestion reads.
	history[0].Command = "mutated"
	if again := source.History(); again[0].Command != "make test" {
		t.Fatalf("History shared its backing slice: %+v", again)
	}
}

func TestDefaultUsesTheShellHistoryLocations(t *testing.T) {
	home := t.TempDir()
	source := Default(home)
	paths := source.paths()
	want := []string{
		filepath.Join(home, ".zsh_history"),
		filepath.Join(home, ".bash_history"),
		filepath.Join(home, ".local", "share", "fish", "fish_history"),
		filepath.Join(home, ".config", "fish", "fish_history"),
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %+v, want %+v", paths, want)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("path %d = %q, want %q", index, paths[index], want[index])
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
