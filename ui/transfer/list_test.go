package transfer

import (
	"reflect"
	"testing"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
)

func TestCrumbsSplitPOSIXAndWindowsPaths(t *testing.T) {
	posix, parts := crumbs("/home/me/src")
	if !posix {
		t.Fatal("a POSIX path was read as a drive path")
	}
	want := []crumb{{"home", "/home"}, {"me", "/home/me"}, {"src", "/home/me/src"}}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("crumbs = %v, want %v", parts, want)
	}
	if posix, parts := crumbs("/"); !posix || len(parts) != 0 {
		t.Fatalf("the root has crumbs %v", parts)
	}

	posix, parts = crumbs(`C:\Users\me`)
	if posix {
		t.Fatal("a drive path was read as POSIX")
	}
	want = []crumb{{"C:", `C:\`}, {"Users", `C:\Users`}, {"me", `C:\Users\me`}}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("crumbs = %v, want %v", parts, want)
	}
}

func TestSymbolicModeSpellsThePermissionBits(t *testing.T) {
	for mode, want := range map[uint32]string{
		0o755:  "rwxr-xr-x",
		0o644:  "rw-r--r--",
		0o000:  "---------",
		0o4710: "rwx--x---",
	} {
		if got := symbolicMode(mode); got != want {
			t.Errorf("symbolicMode(%o) = %q, want %q", mode, got, want)
		}
	}
}

func TestKindColumnNamesFoldersLinksAndExtensions(t *testing.T) {
	m := locales.In("en")
	for _, tc := range []struct {
		entry domainsftp.Entry
		want  string
	}{
		{domainsftp.Entry{Name: "src", Kind: domainsftp.KindDir}, m.SftpKindFolder()},
		{domainsftp.Entry{Name: "gone", Kind: domainsftp.KindLink}, m.SftpKindLink()},
		{domainsftp.Entry{Name: "Main.GO", Kind: domainsftp.KindFile}, "go"},
		{domainsftp.Entry{Name: "Makefile", Kind: domainsftp.KindFile}, m.SftpKindFile()},
	} {
		if got := kindText(m, tc.entry); got != tc.want {
			t.Errorf("kindText(%q) = %q, want %q", tc.entry.Name, got, tc.want)
		}
	}
}

func TestNarrowPanesDropTheLeastUsefulColumnsFirst(t *testing.T) {
	if got := columnsFor(360); got.modified || got.kind || got.owner || !got.size {
		t.Fatalf("a 360-DIP pane shows %+v", got)
	}
	if got := columnsFor(480); !got.modified || got.kind || got.owner {
		t.Fatalf("a 480-DIP pane shows %+v", got)
	}
	if got := columnsFor(640); !got.modified || !got.kind || !got.owner {
		t.Fatalf("a 640-DIP pane shows %+v", got)
	}
}
