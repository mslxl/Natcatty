package state

import (
	"context"
	"io"
	"io/fs"
	"testing"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
)

// failingOpen hides one file's content behind a permission error, so a folder
// duplicate fails after copying an earlier entry.
type failingOpen struct {
	apptransfer.FileSystem
	fail string
}

func (f failingOpen) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if path == f.fail {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	return f.FileSystem.Open(ctx, path)
}

// readOnlyFiles declares the pane unwritable without changing its reads.
type readOnlyFiles struct {
	apptransfer.FileSystem
}

func (readOnlyFiles) ReadOnly() bool { return true }

// TestDuplicateSelectionCreatesFilesBesideTheOriginal is the acceptance path:
// the action copies one click, leaves the original, and a second click gets a
// distinct name.
func TestDuplicateSelectionCreatesFilesBesideTheOriginal(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "alpha.txt")
	f.state.DuplicateSelection(Left)
	waitIdle(t, f.state)

	if data, ok := f.local.read("/home/me/alpha (copy).txt"); !ok || data != "alpha" {
		t.Fatalf("alpha copy = %q, %v", data, ok)
	}
	if data, ok := f.local.read("/home/me/alpha.txt"); !ok || data != "alpha" {
		t.Fatalf("original alpha = %q, %v", data, ok)
	}

	selectOne(t, f.state, Left, "alpha.txt")
	f.state.DuplicateSelection(Left)
	waitIdle(t, f.state)
	if data, ok := f.local.read("/home/me/alpha (copy 2).txt"); !ok || data != "alpha" {
		t.Fatalf("second alpha copy = %q, %v", data, ok)
	}
	if f.local.has("/home/me/alpha (copy 3).txt") {
		t.Fatal("the second duplicate reused a name without incrementing")
	}
}

// TestDuplicateSelectionCopiesFoldersRecursively covers the folder branch of
// the same action.
func TestDuplicateSelectionCopiesFoldersRecursively(t *testing.T) {
	f := newFixture(t)
	selectOne(t, f.state, Left, "folder")
	f.state.DuplicateSelection(Left)
	waitIdle(t, f.state)

	if data, ok := f.local.read("/home/me/folder (copy)/inside.txt"); !ok || data != "inside" {
		t.Fatalf("copied folder content = %q, %v", data, ok)
	}
	if data, ok := f.local.read("/home/me/folder/inside.txt"); !ok || data != "inside" {
		t.Fatalf("original folder content = %q, %v", data, ok)
	}
}

// TestDuplicateSelectionReportsAPartialFolderFailureWithoutLosingTheOriginal
// is the failure bullet: the error becomes the operation notice and the source
// tree is untouched.
func TestDuplicateSelectionReportsAPartialFolderFailureWithoutLosingTheOriginal(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	tab.FS = failingOpen{FileSystem: tab.FS, fail: "/home/me/folder/inside.txt"}
	selectOne(t, f.state, Left, "folder")
	f.state.DuplicateSelection(Left)
	waitIdle(t, f.state)

	notice := f.state.Notice()
	kind, ok := FailureKind(notice)
	if !ok || kind != apptransfer.KindPermission {
		t.Fatalf("notice = %q, want a permission failure", notice)
	}
	if data, ok := f.local.read("/home/me/folder/inside.txt"); !ok || data != "inside" {
		t.Fatalf("original folder after the failure = %q, %v", data, ok)
	}
}

// TestDuplicateSelectionIsUnavailableOnAReadOnlyPane holds the availability
// bullet: a filesystem that declares itself read-only gets no copy and a
// status explanation.
func TestDuplicateSelectionIsUnavailableOnAReadOnlyPane(t *testing.T) {
	f := newFixture(t)
	tab := f.state.Tab(Left)
	tab.FS = readOnlyFiles{FileSystem: tab.FS}
	selectOne(t, f.state, Left, "alpha.txt")

	if CanDuplicate(tab) {
		t.Fatal("a read-only pane reports Duplicate as available")
	}
	f.state.DuplicateSelection(Left)
	if f.state.Notice() != "pane-unavailable" {
		t.Fatalf("notice = %q", f.state.Notice())
	}
	if f.local.has("/home/me/alpha (copy).txt") {
		t.Fatal("a read-only pane wrote a copy")
	}
}
