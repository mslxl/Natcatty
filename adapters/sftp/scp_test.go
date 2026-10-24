package sftp

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/scptest/scpfixture"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// newSCPFixture builds an SCP-only remote with a home directory and a few
// entries, and returns the adapter plus a release counter.
func newSCPFixture(t *testing.T) (*SCP, *scpfixture.Server, *int) {
	t.Helper()
	server := scpfixture.New()
	server.WriteFile("/home/tester/notes.txt", "hello scp", 0o640)
	server.WriteFile("/home/tester/big.bin", strings.Repeat("x", 4096), 0o600)
	server.MkdirAll("/home/tester/docs")
	server.Symlink("/home/tester/docs-link", "docs")
	releases := 0
	files, err := NewSCP(server, func(error) error {
		releases++
		return nil
	}, WithSCPBacking("host-1"))
	if err != nil {
		t.Fatalf("NewSCP: %v", err)
	}
	t.Cleanup(func() { _ = files.Close() })
	return files, server, &releases
}

func TestSCPProbeAndBrowseAgainstAnSCPOnlyServer(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx := context.Background()

	if err := ProbeSCP(ctx, server); err != nil {
		t.Fatalf("ProbeSCP: %v", err)
	}
	root, err := files.Root(ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if root != "/home/tester" {
		t.Fatalf("Root = %q", root)
	}
	entries, err := files.List(ctx, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	want := []string{"big.bin", "docs", "docs-link", "notes.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v", names)
	}
	byName := map[string]domainsftp.Entry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	if byName["notes.txt"].Kind != domainsftp.KindFile || byName["notes.txt"].Size != int64(len("hello scp")) {
		t.Fatalf("notes.txt = %+v", byName["notes.txt"])
	}
	if byName["notes.txt"].Mode&0o777 != 0o640 {
		t.Fatalf("notes.txt mode = %04o", byName["notes.txt"].Mode)
	}
	if byName["docs"].Kind != domainsftp.KindDir {
		t.Fatalf("docs = %+v", byName["docs"])
	}
	link := byName["docs-link"]
	if !link.Symlink || link.Kind != domainsftp.KindDir {
		t.Fatalf("docs-link = %+v", link)
	}
	usedStructured := false
	for _, command := range server.Commands() {
		if strings.Contains(command, "for f in *") {
			usedStructured = true
		}
		if strings.HasPrefix(command, "LC_ALL=C ls -la") {
			t.Fatal("the structured listing fell back to ls on a POSIX server")
		}
	}
	if !usedStructured {
		t.Fatal("the structured listing command never ran")
	}

	stat, err := files.Stat(ctx, "/home/tester/notes.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Kind != domainsftp.KindFile || stat.Size != int64(len("hello scp")) || stat.Name != "notes.txt" {
		t.Fatalf("Stat = %+v", stat)
	}
	if _, err := files.Stat(ctx, "/home/tester/missing"); apptransfer.KindOf(err) != apptransfer.KindNotFound {
		t.Fatalf("missing Stat error = %v (kind %s)", err, apptransfer.KindOf(err))
	}
}

func TestSCPTransfersFilesBothWays(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx := context.Background()

	reader, err := files.Open(ctx, "/home/tester/notes.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	downloaded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	_ = reader.Close()
	if string(downloaded) != "hello scp" {
		t.Fatalf("downloaded = %q", downloaded)
	}

	sized, err := files.CreateSized(ctx, "/home/tester/upload.bin", 5)
	if err != nil {
		t.Fatalf("CreateSized: %v", err)
	}
	if _, err := io.WriteString(sized, "12345"); err != nil {
		t.Fatalf("write sized: %v", err)
	}
	if err := sized.Close(); err != nil {
		t.Fatalf("close sized: %v", err)
	}
	if got, ok := server.ReadFile("/home/tester/upload.bin"); !ok || got != "12345" {
		t.Fatalf("uploaded = %q, exists = %v", got, ok)
	}

	buffered, err := files.Create(ctx, "/home/tester/buffered.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := io.WriteString(buffered, "buffered body"); err != nil {
		t.Fatalf("write buffered: %v", err)
	}
	if err := buffered.Close(); err != nil {
		t.Fatalf("close buffered: %v", err)
	}
	if got, ok := server.ReadFile("/home/tester/buffered.txt"); !ok || got != "buffered body" {
		t.Fatalf("buffered upload = %q, exists = %v", got, ok)
	}

	// A declared size that does not match what was written fails closed and
	// leaves no destination behind.
	short, err := files.CreateSized(ctx, "/home/tester/short.bin", 10)
	if err != nil {
		t.Fatalf("CreateSized short: %v", err)
	}
	if _, err := io.WriteString(short, "abc"); err != nil {
		t.Fatalf("write short: %v", err)
	}
	if err := short.Close(); err == nil {
		t.Fatal("a short upload was accepted")
	}

	missing, err := files.Open(ctx, "/home/tester/absent")
	if err != nil {
		t.Fatalf("Open missing: %v", err)
	}
	_, readErr := io.ReadAll(missing)
	_ = missing.Close()
	if apptransfer.KindOf(readErr) != apptransfer.KindNotFound {
		t.Fatalf("missing download error = %v (kind %s)", readErr, apptransfer.KindOf(readErr))
	}
}

func TestSCPManagesDirectoriesAndModes(t *testing.T) {
	files, server, _ := newSCPFixture(t)
	ctx := context.Background()

	if err := files.Mkdir(ctx, "/home/tester/new"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if !server.Has("/home/tester/new") {
		t.Fatal("Mkdir created nothing")
	}
	if err := files.MkdirAll(ctx, "/home/tester/a/b/c"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if !server.Has("/home/tester/a/b/c") {
		t.Fatal("MkdirAll created nothing")
	}
	if err := files.Rename(ctx, "/home/tester/new", "/home/tester/renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if server.Has("/home/tester/new") || !server.Has("/home/tester/renamed") {
		t.Fatal("Rename did not move the directory")
	}
	if err := files.Chmod(ctx, "/home/tester/notes.txt", 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if mode, _ := server.Mode("/home/tester/notes.txt"); mode&0o777 != 0o600 {
		t.Fatalf("mode = %04o", mode)
	}
	if err := files.Remove(ctx, "/home/tester/notes.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if server.Has("/home/tester/notes.txt") {
		t.Fatal("Remove left the file")
	}
	if err := files.RemoveAll(ctx, "/home/tester/a"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if server.Has("/home/tester/a/b/c") {
		t.Fatal("RemoveAll left the tree")
	}
}

func TestSCPProbeRefusesAServerWithoutSCP(t *testing.T) {
	server := scpfixture.New(scpfixture.WithoutSCP())
	if err := ProbeSCP(context.Background(), server); !errors.Is(err, ErrSCPMissing) {
		t.Fatalf("ProbeSCP = %v, want ErrSCPMissing", err)
	}
}

func TestSCPCloseReleasesOnce(t *testing.T) {
	files, _, releases := newSCPFixture(t)
	if err := files.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := files.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if *releases != 1 {
		t.Fatalf("releases = %d", *releases)
	}
}
