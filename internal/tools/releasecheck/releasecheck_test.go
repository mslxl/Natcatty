package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func writeFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func releaseTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"darwin-universal/Natcatty 0.1.0.dmg":                   "dmg",
		"windows-amd64/Natcatty Setup 0.1.0 amd64.exe":          "setup",
		"windows-amd64/Natcatty.exe":                            "raw exe is carried, not a release artifact",
		"linux-amd64/natcatty-0.1.0-linux-amd64.tar.gz":         "tar amd64",
		"linux-amd64/natcatty_0.1.0_amd64.deb":                  "deb amd64",
		"linux-amd64/install.sh":                                "#!/bin/sh\n",
		"linux-arm64/natcatty-0.1.0-linux-arm64.tar.gz":         "tar arm64",
		"linux-arm64/natcatty_0.1.0_arm64.deb":                  "deb arm64",
		"linux-arm64/natcatty":                                  "raw linux executable",
		"darwin-universal/Natcatty.app/Contents/MacOS/Natcatty": "bundle executable",
	}
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	return root
}

func TestGenerateAndVerifySums(t *testing.T) {
	root := releaseTree(t)
	entries, err := writeSums(root, filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("sums list %d artifacts, want 7: %+v", len(entries), entries)
	}
	want := []string{
		"darwin-universal/Natcatty 0.1.0.dmg",
		"linux-amd64/install.sh",
		"linux-amd64/natcatty-0.1.0-linux-amd64.tar.gz",
		"linux-amd64/natcatty_0.1.0_amd64.deb",
		"linux-arm64/natcatty-0.1.0-linux-arm64.tar.gz",
		"linux-arm64/natcatty_0.1.0_arm64.deb",
		"windows-amd64/Natcatty Setup 0.1.0 amd64.exe",
	}
	for index, entry := range entries {
		if entry.Name != want[index] {
			t.Errorf("entry %d = %q, want %q", index, entry.Name, want[index])
		}
		if len(entry.Sum) != 64 {
			t.Errorf("%s: digest %q is not 64 hex characters", entry.Name, entry.Sum)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	// The format is sha256sum -c compatible: two spaces between digest and name.
	if !strings.Contains(string(data), "  darwin-universal/Natcatty 0.1.0.dmg\n") {
		t.Fatalf("SHA256SUMS is not sha256sum format:\n%s", data)
	}

	// Verify from a clean directory holding the same tree and sums.
	clean := t.TempDir()
	if err := os.CopyFS(clean, os.DirFS(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := verifySums(clean, filepath.Join(clean, "SHA256SUMS")); err != nil {
		t.Fatalf("verify in a clean directory: %v", err)
	}
}

func TestVerifySumsRejectsMismatchAndMissing(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		root := releaseTree(t)
		if _, err := writeSums(root, filepath.Join(root, "SHA256SUMS")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "linux-amd64", "install.sh"), "#!/bin/sh\necho tampered\n")
		_, err := verifySums(root, filepath.Join(root, "SHA256SUMS"))
		if err == nil || !strings.Contains(err.Error(), "checksum is") {
			t.Fatalf("tampered install.sh: %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		root := releaseTree(t)
		if _, err := writeSums(root, filepath.Join(root, "SHA256SUMS")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "linux-arm64", "natcatty_0.1.0_arm64.deb")); err != nil {
			t.Fatal(err)
		}
		_, err := verifySums(root, filepath.Join(root, "SHA256SUMS"))
		if err == nil {
			t.Fatal("a missing artifact verified")
		}
	})

	t.Run("unlisted artifact", func(t *testing.T) {
		root := releaseTree(t)
		if _, err := writeSums(root, filepath.Join(root, "SHA256SUMS")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "linux-arm64", "natcatty_0.1.0_arm64.delta"), "delta")
		writeFile(t, filepath.Join(root, "linux-arm64", "natcatty_0.1.1_arm64.deb"), "new release")
		_, err := verifySums(root, filepath.Join(root, "SHA256SUMS"))
		if err == nil || !strings.Contains(err.Error(), "not in") {
			t.Fatalf("an unlisted deb: %v", err)
		}
	})

	t.Run("duplicate and traversal", func(t *testing.T) {
		root := releaseTree(t)
		sums := "aaaa  x.dmg\nbbbb  x.dmg\n"
		sums = strings.ReplaceAll(sums, "aaaa", strings.Repeat("a", 64))
		sums = strings.ReplaceAll(sums, "bbbb", strings.Repeat("b", 64))
		writeFile(t, filepath.Join(root, "SHA256SUMS"), sums)
		if _, err := readSums(filepath.Join(root, "SHA256SUMS")); err == nil {
			t.Fatal("a duplicate entry parsed")
		}
		writeFile(t, filepath.Join(root, "SHA256SUMS"), strings.Repeat("a", 64)+"  ../escape.dmg\n")
		if _, err := readSums(filepath.Join(root, "SHA256SUMS")); err == nil {
			t.Fatal("a traversal entry parsed")
		}
	})
}

func TestCollectFlattensArtifactsAndRejectsDuplicates(t *testing.T) {
	root := releaseTree(t)
	out := t.TempDir()
	names, err := collect(root, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 7 {
		t.Fatalf("collected %d artifacts, want 7: %v", len(names), names)
	}
	if _, err := os.Stat(filepath.Join(out, "Natcatty 0.1.0.dmg")); err != nil {
		t.Fatalf("the DMG was not flattened: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "natcatty")); !os.IsNotExist(err) {
		t.Fatalf("a non-release executable was collected: %v", err)
	}

	duplicate := t.TempDir()
	writeFile(t, filepath.Join(duplicate, "a", "same.dmg"), "one")
	writeFile(t, filepath.Join(duplicate, "b", "same.dmg"), "two")
	if _, err := collect(duplicate, t.TempDir()); err == nil {
		t.Fatal("duplicate artifact base names with different bytes were collected")
	}

	// MyGo writes the same install.sh beside every Linux archive. The
	// aggregate must list it once rather than failing the release.
	identical := t.TempDir()
	writeFile(t, filepath.Join(identical, "linux-amd64", "install.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(identical, "linux-arm64", "install.sh"), "#!/bin/sh\nexit 0\n")
	out = t.TempDir()
	names, err = collect(identical, out)
	if err != nil {
		t.Fatalf("identical install.sh copies were rejected: %v", err)
	}
	if len(names) != 1 || names[0] != "install.sh" {
		t.Fatalf("identical install.sh copies collected as %v, want [install.sh]", names)
	}
	if _, err := os.Stat(filepath.Join(out, "install.sh")); err != nil {
		t.Fatalf("install.sh was not collected: %v", err)
	}
}

// collectedReleaseDir collects the sample platform tree into one flat
// directory, the state both release jobs are in immediately after collect.
func collectedReleaseDir(t *testing.T) string {
	t.Helper()
	out := t.TempDir()
	if _, err := collect(releaseTree(t), out); err != nil {
		t.Fatal(err)
	}
	return out
}

// publishedReleaseNames is the flat release set after normalize-assets, in
// sorted order: the seven installers under the names GitHub publishes.
func publishedReleaseNames() []string {
	return []string{
		"Natcatty.0.1.0.dmg",
		"Natcatty.Setup.0.1.0.amd64.exe",
		"install.sh",
		"natcatty-0.1.0-linux-amd64.tar.gz",
		"natcatty-0.1.0-linux-arm64.tar.gz",
		"natcatty_0.1.0_amd64.deb",
		"natcatty_0.1.0_arm64.deb",
	}
}

func TestNormalizeAssetsRenamesAsGitHubDoes(t *testing.T) {
	dir := collectedReleaseDir(t)
	renames, err := normalizeAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantRenames := []assetRename{
		{From: "Natcatty 0.1.0.dmg", To: "Natcatty.0.1.0.dmg"},
		{From: "Natcatty Setup 0.1.0 amd64.exe", To: "Natcatty.Setup.0.1.0.amd64.exe"},
	}
	if !reflect.DeepEqual(renames, wantRenames) {
		t.Fatalf("renames = %+v, want %+v", renames, wantRenames)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := publishedReleaseNames()
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("flat set after normalization = %v, want %v", names, want)
	}
}

func TestNormalizedReleaseSetManifestListsPublishedNames(t *testing.T) {
	dir := collectedReleaseDir(t)
	if _, err := normalizeAssets(dir); err != nil {
		t.Fatal(err)
	}
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	entries, err := writeSums(dir, sumsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("manifest lists %d artifacts, want 7: %+v", len(entries), entries)
	}
	got := make([]string, len(entries))
	for index, entry := range entries {
		got[index] = entry.Name
	}
	want := publishedReleaseNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest names = %v, want %v", got, want)
	}
	if _, err := verifySums(dir, sumsPath); err != nil {
		t.Fatalf("the normalized manifest does not verify: %v", err)
	}
}

func TestNormalizeAssetsFailsClosed(t *testing.T) {
	t.Run("collision", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "Natcatty 0.1.0.dmg"), "dmg")
		writeFile(t, filepath.Join(dir, "Natcatty.0.1.0.dmg"), "dmg")
		_, err := normalizeAssets(dir)
		if err == nil || !strings.Contains(err.Error(), "normalize to") {
			t.Fatalf("a normalized-name collision: %v", err)
		}
		// A failed run must not leave a half-renamed set behind.
		if _, err := os.Stat(filepath.Join(dir, "Natcatty 0.1.0.dmg")); err != nil {
			t.Fatalf("the source name was renamed despite the collision: %v", err)
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "notes.txt"), "not a release asset")
		_, err := normalizeAssets(dir)
		if err == nil || !strings.Contains(err.Error(), "not a release asset") {
			t.Fatalf("an unknown name: %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "linux-amd64", "install.sh"), "#!/bin/sh\n")
		_, err := normalizeAssets(dir)
		if err == nil || !strings.Contains(err.Error(), "directory") {
			t.Fatalf("a directory in the flat set: %v", err)
		}
		// The per-platform artifact must stay where it is.
		if _, err := os.Stat(filepath.Join(dir, "linux-amd64", "install.sh")); err != nil {
			t.Fatalf("a per-platform artifact was touched: %v", err)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		if _, err := normalizeAssets(t.TempDir()); err == nil {
			t.Fatal("an empty flat release set was normalized")
		}
	})
}

func TestAssertNoUpdateMetadata(t *testing.T) {
	manifest := `{"name":"Natcatty","identifier":"io.github.mslxl.natcatty","version":"0.1.0"}`
	writeManifest := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mygo.json")
		writeFile(t, path, content)
		return path
	}

	t.Run("clean release", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "Natcatty 0.1.0.dmg"), "dmg")
		writeFile(t, filepath.Join(dir, "et", "BUILD-PROVENANCE.json"), "{}")
		if err := checkNoUpdateMetadata(dir, writeManifest(t, manifest)); err != nil {
			t.Fatalf("a clean release was rejected: %v", err)
		}
	})

	for _, name := range []string{
		"update-linux-amd64.json",
		"update-darwin-universal.json",
		"latest.yml",
		"latest-mac.yaml",
		"appcast.xml",
		"natcatty-0.0.9-to-0.1.0-linux-amd64.delta",
		"DeveloperID.p12",
		"certificate.pfx",
		"profile.mobileprovision",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, name), "metadata")
			if err := checkNoUpdateMetadata(dir, ""); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}

	for _, content := range []string{
		`{"name":"Natcatty","identifier":"x","version":"0.1.0","updates":{"github":"o/r"}}`,
		`{"name":"Natcatty","identifier":"x","version":"0.1.0","macos":{"signingIdentity":"Developer ID Application: A (B)"}}`,
		`{"name":"Natcatty","identifier":"x","version":"0.1.0","macos":{"notarize":{"keychainProfile":"p"}}}`,
		`{"name":"Natcatty","identifier":"x","version":"0.1.0","windows":{"certificate":"a.pfx"}}`,
		`{"name":"Natcatty","identifier":"x","version":"0.1.0","windows":{"signCommand":"signtool"}}`,
		`{"name":"Natcatty","identifier":"x","version":"01.0.0"}`,
		`{"name":"Natcatty","version":"0.1.0"}`,
	} {
		if err := checkManifest(writeManifest(t, content)); err == nil {
			t.Fatalf("manifest accepted: %s", content)
		}
	}
}

func TestCheckBinaryFindsEveryCatalog(t *testing.T) {
	catalogs := map[string]string{
		"en":    `{"sessionMenuCopy":"Copy","workTabsLabel":"Session tabs"}`,
		"es":    `{"sessionMenuCopy":"Copiar","workTabsLabel":"Pestañas de sesiones"}`,
		"ru":    `{"sessionMenuCopy":"Копировать","workTabsLabel":"Вкладки сеансов"}`,
		"zh-CN": `{"sessionMenuCopy":"复制","workTabsLabel":"会话标签页"}`,
		"zh-TW": `{"sessionMenuCopy":"複製","workTabsLabel":"工作階段分頁"}`,
	}
	dir := t.TempDir()
	var binary strings.Builder
	for tag, content := range catalogs {
		writeFile(t, filepath.Join(dir, tag+".json"), content)
	}
	for _, content := range catalogs {
		binary.WriteString(content)
	}
	binaryPath := filepath.Join(dir, "app.exe")
	writeFile(t, binaryPath, binary.String())
	if err := checkEmbeddedCatalogs(binaryPath, dir); err != nil {
		t.Fatalf("all five catalogs embedded: %v", err)
	}

	// Drop the zh-TW marker: the check must name that catalog, not pass
	// because the other four are present.
	writeFile(t, binaryPath, strings.Replace(binary.String(), "工作階段分頁", "", 1))
	err := checkEmbeddedCatalogs(binaryPath, dir)
	if err == nil || !strings.Contains(err.Error(), "zh-TW") {
		t.Fatalf("a binary without the zh-TW catalog: %v", err)
	}
}

func TestValidateTagAgainstManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mygo.json")
	writeFile(t, path, `{"name":"Natcatty","identifier":"io.github.mslxl.natcatty","version":"0.1.0"}`)
	version, err := manifestVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.1.0" {
		t.Fatalf("version = %q", version)
	}
	if err := validateTagCommand([]string{"-tag", "v0.1.0", "-manifest", path}); err != nil {
		t.Fatalf("v0.1.0: %v", err)
	}
	if err := validateTagCommand([]string{"-tag", "v0.1.1", "-manifest", path}); err == nil {
		t.Fatal("v0.1.1 matched a 0.1.0 manifest")
	}
}
