package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBinary(t *testing.T, root, kind, target, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, kind, target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func specFor(root, content string) Spec {
	return Spec{
		Kind:  ET,
		Roots: []string{root},
		Targets: map[string]Entry{
			"test-target": {
				Binary: "et-test",
				SHA256: digest(content),
				Size:   int64(len(content)),
			},
		},
	}
}

func TestResolveVerifiesGoodBinary(t *testing.T) {
	root := t.TempDir()
	path := writeBinary(t, root, "et", "test-target", "et-test", "binary")
	resolved, err := specFor(root, "binary").ResolveTarget("test-target")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if resolved.Path != path || resolved.Target != "test-target" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveRejectsBadChecksum(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, root, "et", "test-target", "et-test", "tampered")
	_, err := specFor(root, "binary").ResolveTarget("test-target")
	if !errors.Is(err, TargetError{Kind: ErrorChecksumMismatch}) {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Expected == typed.Actual {
		t.Fatalf("typed error = %+v", typed)
	}
}

func TestResolveRejectsWrongSize(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, root, "et", "test-target", "et-test", "binary")
	spec := specFor(root, "binary")
	entry := spec.Targets["test-target"]
	entry.Size++
	spec.Targets["test-target"] = entry
	_, err := spec.ResolveTarget("test-target")
	if !errors.Is(err, TargetError{Kind: ErrorChecksumMismatch}) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveRevalidatesEveryCall(t *testing.T) {
	root := t.TempDir()
	path := writeBinary(t, root, "et", "test-target", "et-test", "binary")
	spec := specFor(root, "binary")
	if _, err := spec.ResolveTarget("test-target"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.ResolveTarget("test-target"); err != nil {
		t.Fatalf("rewritten identical binary rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("evil-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.ResolveTarget("test-target"); !errors.Is(err, TargetError{Kind: ErrorChecksumMismatch}) {
		t.Fatalf("replaced binary accepted: %v", err)
	}
}

func TestResolveAbsentReportsSearchedPaths(t *testing.T) {
	root := t.TempDir()
	second := t.TempDir()
	spec := specFor(root, "binary")
	spec.Roots = []string{root, second}
	_, err := spec.ResolveTarget("test-target")
	if !errors.Is(err, TargetError{Kind: ErrorUnavailable}) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("not a *Error: %v", err)
	}
	if len(typed.Searched) != 2 {
		t.Fatalf("searched = %#v", typed.Searched)
	}
	for _, path := range typed.Searched {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("searched path exists: %s", path)
		}
	}
}

func TestResolveUnsupportedTargets(t *testing.T) {
	spec := MoshSpec([]string{t.TempDir()})
	if _, err := spec.ResolveTarget("windows-arm64"); !errors.Is(err, TargetError{Kind: ErrorUnsupportedPlatform}) {
		t.Fatalf("unknown target err = %v", err)
	}
	for _, test := range []struct {
		goos, goarch string
		ok           bool
	}{
		{"windows", "amd64", true},
		{"windows", "arm64", false},
		{"linux", "amd64", true},
		{"linux", "arm64", true},
		{"darwin", "amd64", true},
		{"darwin", "arm64", true},
		{"linux", "386", false},
	} {
		target, ok := TargetKey(test.goos, test.goarch)
		if ok != test.ok {
			t.Fatalf("TargetKey(%s,%s) ok = %v", test.goos, test.goarch, ok)
		}
		if ok && (target == "" || len(spec.Targets[target].SHA256) != 64) {
			t.Fatalf("TargetKey(%s,%s) = %q has no manifest", test.goos, test.goarch, target)
		}
	}
}

func TestManifestsComplete(t *testing.T) {
	for name, assets := range map[string]map[string]Asset{
		"mosh": MoshAssets(),
		"et":   ETAssets(),
	} {
		if len(assets) != 4 {
			t.Fatalf("%s targets = %d, want 4", name, len(assets))
		}
		for target, asset := range assets {
			if asset.Asset == "" || asset.Binary == "" || asset.Size <= 0 {
				t.Fatalf("%s/%s incomplete: %+v", name, target, asset)
			}
			if len(asset.ArchiveSHA256) != 64 || len(asset.BinarySHA256) != 64 {
				t.Fatalf("%s/%s digest length wrong: %+v", name, target, asset)
			}
		}
	}
	if MoshAssets()["win32-x64"].Binary != "mosh-client.exe" {
		t.Fatal("mosh Windows binary name")
	}
	if ETAssets()["linux-arm64"].Binary != "et" {
		t.Fatal("ET Linux binary name")
	}
}

func TestRootsOrderAndOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NATCATTY_RESOURCES", filepath.Join(root, "override"))
	exe := filepath.Join(root, "app", "natcatty.exe")
	roots := Roots(filepath.Join(root, "res"), exe, root)
	if len(roots) < 4 {
		t.Fatalf("roots = %#v", roots)
	}
	if roots[0] != filepath.Join(root, "override") {
		t.Fatalf("override is not first: %#v", roots)
	}
	if roots[1] != filepath.Join(root, "res") {
		t.Fatalf("resource dir is not second: %#v", roots)
	}
	found := false
	want := filepath.Join(root, "resources")
	for _, candidate := range roots {
		if strings.EqualFold(filepath.Clean(candidate), want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("working-directory resources missing: %#v", roots)
	}
	seen := map[string]int{}
	for _, candidate := range roots {
		seen[candidate]++
		if seen[candidate] > 1 {
			t.Fatalf("duplicate root %q in %#v", candidate, roots)
		}
	}
}

func TestReleasedBundleCarriesLicensesProvenanceAndVerifiedBinaries(t *testing.T) {
	resources := filepath.Join("..", "..", "resources")
	notices, err := os.ReadFile(filepath.Join(resources, "THIRD-PARTY-NOTICES.md"))
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	for _, want := range []string{"mosh-client", "GPL-3.0", "EternalTerminal", "Apache License 2.0"} {
		if !strings.Contains(string(notices), want) {
			t.Fatalf("notices do not mention %q", want)
		}
	}

	for _, bundle := range []struct {
		kind       Kind
		license    string
		provenance string
		release    string
		sha256     string
		spec       Spec
	}{
		{
			kind: Mosh, license: "LICENSE-MoshCatty-GPL-3.0.txt",
			provenance: "BUILD-PROVENANCE.json", release: MoshRelease,
			sha256: MoshAssets()["win32-x64"].BinarySHA256,
			spec:   MoshSpec([]string{resources}),
		},
		{
			kind: ET, license: "LICENSE-EternalTerminal-Apache-2.0.txt",
			provenance: "BUILD-PROVENANCE.json", release: ETRelease,
			sha256: ETAssets()["win32-x64"].BinarySHA256,
			spec:   ETSpec([]string{resources}),
		},
	} {
		dir := filepath.Join(resources, string(bundle.kind))
		for _, name := range []string{"README.md", bundle.license, bundle.provenance} {
			info, statErr := os.Stat(filepath.Join(dir, name))
			if statErr != nil || info.Size() == 0 {
				t.Fatalf("%s/%s: %v", bundle.kind, name, statErr)
			}
		}
		provenance, err := os.ReadFile(filepath.Join(dir, bundle.provenance))
		if err != nil {
			t.Fatalf("%s provenance: %v", bundle.kind, err)
		}
		if !strings.Contains(string(provenance), bundle.release) ||
			!strings.Contains(string(provenance), bundle.sha256) {
			t.Fatalf("%s provenance does not record release %s and binary %s",
				bundle.kind, bundle.release, bundle.sha256)
		}
		if _, err := bundle.spec.ResolveTarget("win32-x64"); err != nil {
			t.Fatalf("%s committed Windows binary does not verify: %v", bundle.kind, err)
		}
	}
}
