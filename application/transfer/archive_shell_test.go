package transfer

// The command-builder tests assert the generated script's text. This test runs
// that script so the parts that only exist at execution time are covered:
// mktemp staging, tar with the quoted sources, publishing by hard link, the
// collision marker, and the cleanup trap. It runs on any host with a POSIX
// shell and skips cleanly elsewhere.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBuiltArchiveCommandRunsOnAPOSIXShell executes the generated archive
// script twice: the first run publishes an archive beside the source folder,
// and the second reports the existing target without changing it.
func TestBuiltArchiveCommandRunsOnAPOSIXShell(t *testing.T) {
	shell := archiveTestShell(t)
	root := t.TempDir()
	directory := archiveTestDirectory(t, root)
	seedArchiveSources(t, root)

	command, plan, err := BuildArchiveCommand(ArchiveRequest{
		Directory: directory,
		Name:      "photos.tar.gz",
		Format:    ArchiveTarGz,
		Paths:     []string{path.Join(directory, "photos")},
	})
	if err != nil {
		t.Fatalf("BuildArchiveCommand: %v", err)
	}
	if len(plan.Paths) != 1 || plan.Paths[0] != "photos" {
		t.Fatalf("planned sources = %v", plan.Paths)
	}

	first := runArchiveScript(t, shell, root, command)
	if err := ParseArchiveResult(first); err != nil {
		t.Fatalf("the first run did not confirm: %v\n%s", err, first)
	}
	target := filepath.Join(root, "photos.tar.gz")
	names := archiveEntries(t, target)
	for _, want := range []string{"photos/a b.txt", "photos/it's.txt", "photos/nested/deep.txt"} {
		if !names[want] {
			t.Fatalf("archive entries %v are missing %q", names, want)
		}
	}
	if staging := archiveStagingDirs(t, root); len(staging) != 0 {
		t.Fatalf("the successful run left staging directories: %v", staging)
	}

	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the archive: %v", err)
	}
	second := runArchiveScript(t, shell, root, command)
	if err := ParseArchiveResult(second); !errors.Is(err, ErrArchiveExists) {
		t.Fatalf("the second run = %v, want ErrArchiveExists\n%s", err, second)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the archive after the collision: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the collision replaced the existing archive")
	}
	if staging := archiveStagingDirs(t, root); len(staging) != 0 {
		t.Fatalf("the collision run left staging directories: %v", staging)
	}
}

// archiveTestShell finds a shell that can run the generated command, or skips
// when this host has no POSIX environment.
func archiveTestShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on PATH")
	}
	probe := exec.Command(shell, "-c",
		"command -v mktemp >/dev/null 2>&1 && command -v tar >/dev/null 2>&1 && command -v ln >/dev/null 2>&1")
	if err := probe.Run(); err != nil {
		t.Skipf("the shell at %s lacks mktemp, tar, or ln: %v", shell, err)
	}
	return shell
}

// archiveTestDirectory spells the Windows temp directory the way the shell
// sees it, so PlanArchive's absolute-POSIX check holds. Other platforms pass
// their native path through.
func archiveTestDirectory(t *testing.T, root string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return root
	}
	cygpath, err := exec.LookPath("cygpath")
	if err != nil {
		t.Skip("no cygpath to spell the Windows temp directory as a POSIX path")
	}
	out, err := exec.Command(cygpath, "-u", root).Output()
	if err != nil {
		t.Skipf("cygpath -u %q: %v", root, err)
	}
	directory := strings.TrimSpace(string(out))
	if !path.IsAbs(directory) {
		t.Skipf("cygpath returned a non-absolute path %q", directory)
	}
	return directory
}

// seedArchiveSources creates a folder with a plain file, one with a space, one
// with a single quote, and a nested file, so the quoting is exercised for real.
func seedArchiveSources(t *testing.T, root string) {
	t.Helper()
	for _, directory := range []string{
		filepath.Join(root, "photos"),
		filepath.Join(root, "photos", "nested"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	for name, body := range map[string]string{
		filepath.Join(root, "photos", "a b.txt"):            "space",
		filepath.Join(root, "photos", "it's.txt"):           "quote",
		filepath.Join(root, "photos", "nested", "deep.txt"): "deep",
	} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// runArchiveScript runs one generated command and fails the test on a non-zero
// exit, returning stdout+stderr for the marker parser.
func runArchiveScript(t *testing.T, shell, dir, command string) string {
	t.Helper()
	cmd := exec.Command(shell, "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the generated archive command failed: %v\n%s\ncommand:\n%s", err, out, command)
	}
	return string(out)
}

// archiveEntries lists the tar.gz's member names.
func archiveEntries(t *testing.T, file string) map[string]bool {
	t.Helper()
	handle, err := os.Open(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer handle.Close()
	unzipped, err := gzip.NewReader(handle)
	if err != nil {
		t.Fatalf("read %s as gzip: %v", file, err)
	}
	defer unzipped.Close()
	reader := tar.NewReader(unzipped)
	names := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("read %s as tar: %v", file, err)
		}
		names[path.Clean(header.Name)] = true
	}
}

// archiveStagingDirs lists the private sibling directories the command should
// have removed through its EXIT trap.
func archiveStagingDirs(t *testing.T, root string) []string {
	t.Helper()
	staging, err := filepath.Glob(filepath.Join(root, ".netcatty-archive.*"))
	if err != nil {
		t.Fatalf("glob staging directories: %v", err)
	}
	return staging
}
