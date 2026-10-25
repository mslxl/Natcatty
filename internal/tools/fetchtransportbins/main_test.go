package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/bundle"
)

func TestParseKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []bundle.Kind
	}{
		{"all", []bundle.Kind{bundle.Mosh, bundle.ET}},
		{"", []bundle.Kind{bundle.Mosh, bundle.ET}},
		{"mosh", []bundle.Kind{bundle.Mosh}},
		{"ET", []bundle.Kind{bundle.ET}},
		{" et ", []bundle.Kind{bundle.ET}},
	}
	for _, c := range cases {
		got, err := parseKinds(c.in)
		if err != nil {
			t.Fatalf("parseKinds(%q): %v", c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("parseKinds(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("parseKinds(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
	if _, err := parseKinds("telnet"); err == nil {
		t.Fatal("parseKinds accepted an unknown kind")
	}
}

func TestParseTargets(t *testing.T) {
	t.Parallel()
	all, err := parseTargets("all")
	if err != nil {
		t.Fatalf("parseTargets(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("parseTargets(all) = %v, want four targets", all)
	}
	got, err := parseTargets(" linux-x64 , darwin-universal ")
	if err != nil {
		t.Fatalf("parseTargets(list): %v", err)
	}
	if len(got) != 2 || got[0] != "linux-x64" || got[1] != "darwin-universal" {
		t.Fatalf("parseTargets(list) = %v", got)
	}
	host, err := parseTargets("")
	if err != nil {
		if _, ok := bundle.HostTarget(); !ok {
			return
		}
		t.Fatalf("parseTargets(empty): %v", err)
	}
	if want, ok := bundle.HostTarget(); ok && (len(host) != 1 || host[0] != want) {
		t.Fatalf("parseTargets(empty) = %v, want %q", host, want)
	}
	if _, err := parseTargets("plan9"); err == nil {
		t.Fatal("parseTargets accepted an unknown target")
	}
	if _, err := parseTargets("win32-x64,plan9"); err == nil {
		t.Fatal("parseTargets accepted a list containing an unknown target")
	}
}

func TestSafeArchiveName(t *testing.T) {
	t.Parallel()
	safe := []string{"mosh-client", "dist/mosh-client", "a/b/c/et.exe"}
	unsafe := []string{"", ".", "/etc/passwd", "../mosh-client", "dist/../../evil", "C:/evil.exe"}
	for _, name := range safe {
		if !safeArchiveName(name) {
			t.Fatalf("safeArchiveName(%q) = false", name)
		}
	}
	for _, name := range unsafe {
		if safeArchiveName(name) {
			t.Fatalf("safeArchiveName(%q) = true", name)
		}
	}
}

func writeTarGz(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range entries {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractBinary(t *testing.T) {
	t.Parallel()
	archive := writeTarGz(t, map[string][]byte{
		"dist/README":      []byte("not the client"),
		"dist/mosh-client": []byte("client-bytes"),
		"other/et":         []byte("wrong"),
	})
	destination := filepath.Join(t.TempDir(), "mosh-client")
	if err := extractBinary(archive, "mosh-client", destination); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "client-bytes" {
		t.Fatalf("extracted %q", got)
	}
}

func TestExtractBinaryMissing(t *testing.T) {
	t.Parallel()
	archive := writeTarGz(t, map[string][]byte{"dist/README": []byte("x")})
	destination := filepath.Join(t.TempDir(), "et")
	err := extractBinary(archive, "et", destination)
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("extractBinary missing = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after a failed extraction: %v", statErr)
	}
}

func TestExtractBinaryRejectsTraversal(t *testing.T) {
	t.Parallel()
	archive := writeTarGz(t, map[string][]byte{
		"../mosh-client": []byte("escape"),
	})
	destination := filepath.Join(t.TempDir(), "mosh-client")
	err := extractBinary(archive, "mosh-client", destination)
	if err == nil || !strings.Contains(err.Error(), "unsafe archive entry") {
		t.Fatalf("extractBinary traversal = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after a rejected entry: %v", statErr)
	}
}

func TestManifestHasEveryTarget(t *testing.T) {
	t.Parallel()
	for _, kind := range []bundle.Kind{bundle.Mosh, bundle.ET} {
		repository, release, assets := manifest(kind)
		if !strings.HasPrefix(repository, "https://github.com/") {
			t.Fatalf("%s repository = %q", kind, repository)
		}
		if release == "" {
			t.Fatalf("%s release is empty", kind)
		}
		for _, target := range []string{"win32-x64", "linux-x64", "linux-arm64", "darwin-universal"} {
			asset, ok := assets[target]
			if !ok {
				t.Fatalf("%s is missing %s", kind, target)
			}
			if asset.BinarySHA256 == "" || asset.ArchiveSHA256 == "" || asset.Size <= 0 {
				t.Fatalf("%s/%s manifest is incomplete: %+v", kind, target, asset)
			}
			if target == "win32-x64" {
				if !strings.HasSuffix(asset.Binary, ".exe") {
					t.Fatalf("%s/%s binary %q is not a Windows executable", kind, target, asset.Binary)
				}
			} else if strings.HasSuffix(asset.Binary, ".exe") {
				t.Fatalf("%s/%s binary %q should not be a Windows executable", kind, target, asset.Binary)
			}
		}
	}
}

func TestFileDigest(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	size, digest, err := fileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if size != 3 {
		t.Fatalf("size = %d", size)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if digest != want {
		t.Fatalf("digest = %s", digest)
	}
}
