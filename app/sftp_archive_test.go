package app

// Ticket 55: Create Archive runs on the Session's existing exec path. These
// tests use the real Manager, Vault, host-key policy, SFTP adapter, and
// transfer selection against the in-process fakessh peer. The peer's exec
// handler emulates the remote shell: it publishes a file beside the sources,
// refuses an existing target, and can model a host without tar.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	xssh "golang.org/x/crypto/ssh"

	apptransfer "github.com/mslxl/natcatty/application/transfer"
)

// execCommand answers the archive command the adapter sends. It never runs a
// local shell: the emitted script is inspected, the requested output is
// created in the fake host's root, and the same confirmation markers are
// printed.
func (n *sftpDialNetwork) execCommand(command string, channel xssh.Channel) int {
	n.mu.Lock()
	missingTool := n.missingArchiveTool
	root := n.root
	n.mu.Unlock()
	if missingTool {
		_, _ = fmt.Fprintln(channel.Stderr(), "tar is not installed on the remote host")
		return 127
	}
	directory, ok := shellAssignment(command, "cd ")
	name, named := shellAssignment(command, "name=")
	if !ok || !named {
		_, _ = fmt.Fprintln(channel.Stderr(), "unexpected archive command")
		return 2
	}
	if directory != remotePOSIXPath(root) {
		_, _ = fmt.Fprintf(channel.Stderr(), "archive ran in %q, want %q\n", directory, remotePOSIXPath(root))
		return 2
	}
	out := filepath.Join(root, name)
	file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		_, _ = fmt.Fprintln(channel, apptransfer.ArchiveResultExists)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(channel.Stderr(), err)
		return 1
	}
	_, _ = file.WriteString("archive")
	_ = file.Close()
	_, _ = fmt.Fprintln(channel, apptransfer.ArchiveResultOK)
	return 0
}

// shellAssignment finds one `prefix='value'` line in the generated script and
// decodes its single-quoted value, including the standard '\” escape.
func shellAssignment(command, prefix string) (string, bool) {
	for _, line := range strings.Split(command, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value := strings.TrimPrefix(line, prefix)
		if len(value) < 2 || value[0] != '\'' || value[len(value)-1] != '\'' {
			return "", false
		}
		var out strings.Builder
		for index := 1; index < len(value)-1; {
			if strings.HasPrefix(value[index:], `'\''`) {
				out.WriteByte('\'')
				index += 4
				continue
			}
			out.WriteByte(value[index])
			index++
		}
		return out.String(), true
	}
	return "", false
}

// remotePOSIXPath spells the fake host root the way pkg/sftp does: an absolute
// POSIX path, with a Windows drive kept as a path component.
func remotePOSIXPath(root string) string {
	slash := filepath.ToSlash(root)
	if strings.HasPrefix(slash, "/") {
		return slash
	}
	return "/" + slash
}

// TestCreateArchiveIsUnavailableOnASingleChannelHost covers the wiring the
// action must respect: a single-channel Host opens SFTP without an executor, so
// the pane reports no archive capability even though its type implements
// ArchiveCreator. Without the capability check the menu offered an operation
// that could only fail as unsupported.
func TestCreateArchiveIsUnavailableOnASingleChannelHost(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{serverVersion: "SSH-2.0-TERM-SSHD"})
	terminal := fixture.acquireTerminal(t)
	defer terminal.Release()

	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer files.Close()

	capable, ok := files.(apptransfer.ArchiveCapability)
	if !ok {
		t.Fatal("the SFTP filesystem does not report an archive capability")
	}
	if capable.CanArchive() {
		t.Fatal("a single-channel pane advertises Create Archive without an executor")
	}
	if apptransfer.CanCreateArchive(files) {
		t.Fatal("the shared gate offers Create Archive on a single-channel pane")
	}
}

// TestCreateArchiveWritesBesideTheFolderAndListsIt is the acceptance path: a
// folder becomes name.tar.gz beside itself, on the remote exec path, and a
// fresh listing shows it.
func TestCreateArchiveWritesBesideTheFolderAndListsIt(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{})
	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer files.Close()

	creator, ok := files.(apptransfer.ArchiveCreator)
	if !ok {
		t.Fatal("the SFTP filesystem does not expose archive creation")
	}
	if capable, ok := files.(apptransfer.ArchiveCapability); !ok || !capable.CanArchive() {
		t.Fatal("a normal SFTP pane does not advertise archive creation")
	}
	root, err := files.Root(fixture.ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.network.root, "photos"), 0o755); err != nil {
		t.Fatalf("seed the folder: %v", err)
	}
	if err := creator.CreateArchive(fixture.ctx, apptransfer.ArchiveRequest{
		Directory: root,
		Name:      "photos.tar.gz",
		Format:    apptransfer.ArchiveTarGz,
		Paths:     []string{files.Join(root, "photos")},
	}); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	entries, err := files.List(fixture.ctx, root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, entry := range entries {
		if files.Base(entry.Name) == "photos.tar.gz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refreshed listing has no archive: %+v", entries)
	}
	reader, err := files.Open(fixture.ctx, files.Join(root, "photos.tar.gz"))
	if err != nil {
		t.Fatalf("open the archive: %v", err)
	}
	defer reader.Close()
	body := make([]byte, len("archive"))
	if _, err := reader.Read(body); err != nil {
		t.Fatalf("read the archive: %v", err)
	}
	if string(body) != "archive" {
		t.Fatalf("archive content = %q", body)
	}
}

// TestCreateArchiveRefusesAnExistingNameWithoutTouchingIt is the collision
// bullet: the remote command reports the existing target, the error is typed,
// and the old file keeps its contents.
func TestCreateArchiveRefusesAnExistingNameWithoutTouchingIt(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{})
	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer files.Close()
	root, err := files.Root(fixture.ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	target := filepath.Join(fixture.network.root, "photos.tar.gz")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed the existing archive: %v", err)
	}

	err = files.(apptransfer.ArchiveCreator).CreateArchive(fixture.ctx, apptransfer.ArchiveRequest{
		Directory: root,
		Name:      "photos.tar.gz",
		Format:    apptransfer.ArchiveTarGz,
		Paths:     []string{files.Join(root, "photos")},
	})
	if !errors.Is(err, apptransfer.ErrArchiveExists) {
		t.Fatalf("error = %v, want ErrArchiveExists", err)
	}
	if kind := apptransfer.KindOf(err); kind != apptransfer.KindExists {
		t.Fatalf("kind = %q, want exists", kind)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the existing archive: %v", err)
	}
	if string(body) != "keep" {
		t.Fatalf("the collision changed the existing file to %q", body)
	}
}

// TestCreateArchiveReportsAMissingRemoteTool is the typed-error bullet: exit
// 127 from the command's tool probe becomes KindToolMissing.
func TestCreateArchiveReportsAMissingRemoteTool(t *testing.T) {
	fixture := newSFTPFixture(t, sftpFixtureOptions{})
	files, err := fixture.openRemote()
	if err != nil {
		t.Fatalf("OpenRemote: %v", err)
	}
	defer files.Close()
	root, err := files.Root(fixture.ctx)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	fixture.network.mu.Lock()
	fixture.network.missingArchiveTool = true
	fixture.network.mu.Unlock()

	err = files.(apptransfer.ArchiveCreator).CreateArchive(fixture.ctx, apptransfer.ArchiveRequest{
		Directory: root,
		Name:      "photos.tar.gz",
		Format:    apptransfer.ArchiveTarGz,
		Paths:     []string{files.Join(root, "photos")},
	})
	if !errors.Is(err, apptransfer.ErrArchiveToolMissing) {
		t.Fatalf("error = %v, want ErrArchiveToolMissing", err)
	}
	if kind := apptransfer.KindOf(err); kind != apptransfer.KindToolMissing {
		t.Fatalf("kind = %q, want tool-missing", kind)
	}
}
