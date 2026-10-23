package transfer

import (
	"context"
	"testing"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

func TestCopyRefusesDestinationInsideItsSource(t *testing.T) {
	files := newFakeFS("local")
	files.addDir("/src")
	files.addDir("/src/sub")
	files.addFile("/src/sub", "inside.txt", "inside", 0o644)

	service := NewService(nil, nil)
	_, err := service.Copy(context.Background(), CopyRequest{
		Source: files, Dest: files,
		Sources: []string{"/src"},
		DestDir: "/src/sub",
		Kind:    domainsftp.OpCopy,
	})
	if KindOf(err) != KindSelfCopy {
		t.Fatalf("Copy error = %v, kind = %q, want %q", err, KindOf(err), KindSelfCopy)
	}
	if files.has("/src/sub/src") {
		t.Fatal("the refused copy created a tree inside its source")
	}
	if got, ok := files.file("/src/sub/inside.txt"); !ok || string(got) != "inside" {
		t.Fatalf("the refused copy changed the source: %q, %v", got, ok)
	}
}

func TestSameLocationNeedsBothBackingAndPath(t *testing.T) {
	first := newFakeFS("first")
	second := newFakeFS("second")
	if !SameLocation(first, "/srv/a", first, "/srv/./a") {
		t.Fatal("one filesystem's cleaned path did not match itself")
	}
	if SameLocation(first, "/srv/a", second, "/srv/a") {
		t.Fatal("identical paths on different backings matched")
	}
	if !PathWithin(first, "/srv", first, "/srv/a") {
		t.Fatal("a child did not match its parent")
	}
	if PathWithin(first, "/srv/a", first, "/srv") {
		t.Fatal("a parent matched its child")
	}
	if PathWithin(first, "/srv", second, "/srv/a") {
		t.Fatal("a path on another backing matched")
	}
}
