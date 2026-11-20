package transfer

import (
	"context"
	"io"
	"io/fs"
	"testing"
)

// unreadableSource hides one file's Open behind a permission error, so a
// recursive duplicate fails after it has already copied earlier entries.
type unreadableSource struct {
	FileSystem
	fail string
}

func (f unreadableSource) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if path == f.fail {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	return f.FileSystem.Open(ctx, path)
}

// TestDuplicateCopiesFilesAndFoldersBesideTheirOriginals is the copy-engine
// contract behind the context menu's Duplicate action: one request per click,
// no conflict prompt, and a second click picks the next free name.
func TestDuplicateCopiesFilesAndFoldersBesideTheirOriginals(t *testing.T) {
	files := newFakeFS("remote")
	files.addFile("/", "report.txt", "original data", 0o644)
	files.addDir("/photos")
	files.addFile("/photos", "nested.txt", "nested data", 0o644)

	service := NewService(nil, nil)
	for run := 1; run <= 2; run++ {
		if _, err := service.Copy(context.Background(), CopyRequest{
			Source:   files,
			Dest:     files,
			Sources:  []string{"/report.txt", "/photos"},
			DestDir:  "/",
			Conflict: ConflictDuplicate,
		}); err != nil {
			t.Fatalf("duplicate run %d: %v", run, err)
		}
	}

	for _, path := range []string{
		"/report (copy).txt", "/report (copy 2).txt",
	} {
		if data, ok := files.file(path); !ok || string(data) != "original data" {
			t.Fatalf("%s = %q, %v", path, data, ok)
		}
	}
	for _, path := range []string{
		"/photos (copy)/nested.txt", "/photos (copy 2)/nested.txt",
	} {
		if data, ok := files.file(path); !ok || string(data) != "nested data" {
			t.Fatalf("%s = %q, %v", path, data, ok)
		}
	}
	// Both originals remain untouched beside their copies.
	if data, ok := files.file("/report.txt"); !ok || string(data) != "original data" {
		t.Fatalf("original report = %q, %v", data, ok)
	}
	if data, ok := files.file("/photos/nested.txt"); !ok || string(data) != "nested data" {
		t.Fatalf("original photo = %q, %v", data, ok)
	}
}

// TestDuplicateFolderFailureKeepsTheOriginal is the partial-failure contract:
// a recursive duplicate reports the failure, and the source tree is intact.
func TestDuplicateFolderFailureKeepsTheOriginal(t *testing.T) {
	files := newFakeFS("remote")
	files.addDir("/tree")
	files.addFile("/tree", "a.txt", "a", 0o644)
	files.addFile("/tree", "b.txt", "b", 0o644)
	source := unreadableSource{FileSystem: files, fail: "/tree/b.txt"}

	service := NewService(nil, nil)
	_, err := service.Copy(context.Background(), CopyRequest{
		Source:   source,
		Dest:     files,
		Sources:  []string{"/tree"},
		DestDir:  "/",
		Conflict: ConflictDuplicate,
	})
	if err == nil {
		t.Fatal("a recursive duplicate with an unreadable file succeeded")
	}
	if kind := KindOf(err); kind != KindPermission {
		t.Fatalf("failure kind = %q, want %q (%v)", kind, KindPermission, err)
	}
	for path, want := range map[string]string{"/tree/a.txt": "a", "/tree/b.txt": "b"} {
		if data, ok := files.file(path); !ok || string(data) != want {
			t.Fatalf("original %s = %q, %v", path, data, ok)
		}
	}
}
