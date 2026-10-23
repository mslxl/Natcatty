package sftp

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pkg/sftp"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

func newLocalFS(t *testing.T) *localFiles {
	t.Helper()
	files, err := NewLocal().OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	local, ok := files.(*localFiles)
	if !ok {
		t.Fatalf("OpenLocal returned %T", files)
	}
	return local
}

func TestLocalCanonicalResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	files := newLocalFS(t)
	if got, want := files.Canonical(filepath.Join(link, ".", "child")), filepath.Join(target, "child"); got != want {
		t.Fatalf("Canonical(%q) = %q, want %q", link, got, want)
	}
	if files.Backing() == "" {
		t.Fatal("a local filesystem has no backing identity")
	}
}

func TestLocalListsTypesHiddenAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link")
	linked := os.Symlink(filepath.Join(dir, "a.txt"), linkPath) == nil

	files := newLocalFS(t)
	entries, err := files.List(context.Background(), dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]domainsftp.Entry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	if !byName["sub"].IsDir() {
		t.Errorf("sub kind = %q", byName["sub"].Kind)
	}
	if byName["a.txt"].Size != 5 {
		t.Errorf("a.txt = %+v", byName["a.txt"])
	}
	if runtime.GOOS != "windows" && byName["a.txt"].Mode != 0o640 {
		t.Errorf("a.txt mode = %04o", byName["a.txt"].Mode)
	}
	if domainsftp.Visible(byName[".hidden"], false) {
		t.Error(".hidden is visible with hidden files off")
	}
	if linked {
		entry, ok := byName["link"]
		if !ok || !entry.Symlink || entry.Kind != domainsftp.KindFile {
			t.Errorf("link = %+v, ok = %v", entry, ok)
		}
		if info, err := files.Stat(context.Background(), linkPath); err != nil || info.Kind != domainsftp.KindFile {
			t.Errorf("Stat(link) = %+v, %v", info, err)
		}
	}
}

func TestLocalOperationsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	files := newLocalFS(t)
	ctx := context.Background()

	if err := files.Mkdir(ctx, filepath.Join(dir, "docs")); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	writer, err := files.Create(ctx, filepath.Join(dir, "docs", "notes.txt"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := io.WriteString(writer, "notes"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := files.Chmod(ctx, filepath.Join(dir, "docs", "notes.txt"), 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	entry, err := files.Stat(ctx, filepath.Join(dir, "docs", "notes.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if entry.Size != 5 {
		t.Fatalf("entry = %+v", entry)
	}
	if runtime.GOOS != "windows" && entry.Mode != 0o600 {
		t.Fatalf("mode = %04o", entry.Mode)
	}
	if err := files.Rename(ctx, filepath.Join(dir, "docs", "notes.txt"), filepath.Join(dir, "docs", "renamed.txt")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := files.RemoveAll(ctx, filepath.Join(dir, "docs")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docs")); !os.IsNotExist(err) {
		t.Fatalf("docs still exists: %v", err)
	}
}

func TestLocalPathHelpersUseThePlatformNotation(t *testing.T) {
	files := newLocalFS(t)
	joined := files.Join("/tmp", "a", "b")
	if files.Base(joined) != "b" {
		t.Fatalf("Base(%q) = %q", joined, files.Base(joined))
	}
	if files.Parent(joined) == joined {
		t.Fatalf("Parent(%q) did not move up", joined)
	}
	want := apptransfer.StylePOSIX
	if runtime.GOOS == "windows" {
		want = apptransfer.StyleWindows
	}
	if files.Style() != want {
		t.Fatalf("Style = %q, want %q", files.Style(), want)
	}
}

func TestLocalRootIsReadable(t *testing.T) {
	files := newLocalFS(t)
	root, err := files.Root(context.Background())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root %q: %v", root, err)
	}
}

// newRemoteFS starts an in-process SFTP server over a pipe and returns the
// adapter connected to it.
func newRemoteFS(t *testing.T) (*Remote, string) {
	t.Helper()
	root := t.TempDir()
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = server.Serve() }()
	remote, err := NewRemote(clientSide, func(error) error { return nil })
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	t.Cleanup(func() {
		_ = remote.Close()
		_ = server.Close()
	})
	return remote, root
}

func TestRemoteWithoutBackingDoesNotClaimAnotherConnection(t *testing.T) {
	first, _ := newRemoteFS(t)
	second, _ := newRemoteFS(t)
	if first.Backing() != "" || second.Backing() != "" {
		t.Fatalf("unlabeled backings = %q, %q, want empty", first.Backing(), second.Backing())
	}
	if apptransfer.SameBacking(first, second) {
		t.Fatal("two unlabeled connections were treated as one store")
	}
}

func TestRemoteBrowsesAndMutates(t *testing.T) {
	remote, root := newRemoteFS(t)
	ctx := context.Background()

	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "a.txt"), []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := remote.Root(ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got == "" {
		t.Fatal("Root returned an empty directory")
	}

	dir := remote.Join(got, "dir")
	entries, err := remote.List(ctx, dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" || entries[0].Size != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Owner == "" {
		t.Error("a remote entry should carry its numeric owner")
	}

	if err := remote.MkdirAll(ctx, remote.Join(got, "x", "y")); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writer, err := remote.Create(ctx, remote.Join(got, "x", "y", "new.txt"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := writer.Write([]byte("new")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := remote.Rename(ctx, remote.Join(got, "x", "y", "new.txt"), remote.Join(got, "x", "y", "renamed.txt")); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := remote.Chmod(ctx, remote.Join(got, "x", "y", "renamed.txt"), 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	entry, err := remote.Stat(ctx, remote.Join(got, "x", "y", "renamed.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" && entry.Mode != 0o600 {
		t.Fatalf("mode = %04o", entry.Mode)
	}
	reader, err := remote.Open(ctx, remote.Join(got, "x", "y", "renamed.txt"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	if err := remote.RemoveAll(ctx, remote.Join(got, "x")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := remote.Stat(ctx, remote.Join(got, "x")); err == nil {
		t.Fatal("/x still exists")
	}
}

func TestRemoteCloseReleasesExactlyOnce(t *testing.T) {
	root := t.TempDir()
	clientSide, serverSide := net.Pipe()
	server, err := sftp.NewServer(serverSide, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = server.Serve() }()
	defer server.Close()

	releases := 0
	remote, err := NewRemote(clientSide, func(error) error {
		releases++
		return nil
	})
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	if err := remote.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := remote.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if releases != 1 {
		t.Fatalf("released %d times", releases)
	}
}
