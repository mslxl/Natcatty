package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetsAreTheFivePhoneArtifacts(t *testing.T) {
	if len(targets) != 5 {
		t.Fatalf("targets = %d, want 5", len(targets))
	}
	want := []string{
		"ios-arm64",
		"ios-arm64-simulator",
		"android-arm64-v8a",
		"android-armeabi-v7a",
		"android-x86_64",
	}
	got := make([]string, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.Key)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("target keys = %v, want %v", got, want)
	}

	byKey := map[string]target{}
	for _, target := range targets {
		byKey[target.Key] = target
	}
	for key, wantName := range map[string]string{
		"ios-arm64":           "libghostty-vt-befcdfd2-ios-arm64.a",
		"ios-arm64-simulator": "libghostty-vt-befcdfd2-ios-arm64-simulator.a",
		"android-arm64-v8a":   "libghostty-vt-befcdfd2-android-arm64-v8a.a",
		"android-armeabi-v7a": "libghostty-vt-befcdfd2-android-armeabi-v7a.a",
		"android-x86_64":      "libghostty-vt-befcdfd2-android-x86_64.a",
	} {
		if got := assetName(byKey[key]); got != wantName {
			t.Errorf("%s asset = %q, want %q", key, got, wantName)
		}
	}
	if got := byKey["ios-arm64"].XCFrameworkSlice; got != "ios-arm64" {
		t.Errorf("ios-arm64 slice = %q", got)
	}
	if got := byKey["ios-arm64-simulator"].XCFrameworkSlice; got != "ios-arm64-simulator" {
		t.Errorf("ios-arm64-simulator slice = %q", got)
	}
	for key, triple := range map[string]string{
		"android-arm64-v8a":   "aarch64-linux-android.26",
		"android-armeabi-v7a": "arm-linux-androideabi.26",
		"android-x86_64":      "x86_64-linux-android.26",
	} {
		if got := byKey[key].ZigTriple; got != triple {
			t.Errorf("%s triple = %q, want %q", key, got, triple)
		}
	}
}

func TestManifestRequiresEveryArtifactAndPinnedCommit(t *testing.T) {
	m := newManifest(defaultReleaseURL)
	m.NDK = "30.0.16248370"
	for _, target := range targets {
		m.Artifacts[target.Key] = artifact{
			File:   assetName(target),
			URL:    defaultReleaseURL + "/" + assetName(target),
			SHA256: strings.Repeat("a", 64),
			Size:   10,
			Commit: pinnedCommit,
		}
	}
	if err := m.validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	broken := m.clone()
	broken.Commit = strings.Repeat("0", 40)
	if err := broken.validate(); err == nil || !strings.Contains(err.Error(), "pinned commit") {
		t.Fatalf("wrong commit error = %v", err)
	}

	broken = m.clone()
	delete(broken.Artifacts, "android-x86_64")
	if err := broken.validate(); err == nil || !strings.Contains(err.Error(), "android-x86_64") {
		t.Fatalf("missing artifact error = %v", err)
	}

	broken = m.clone()
	a := broken.Artifacts["ios-arm64"]
	a.SHA256 = "not-a-checksum"
	broken.Artifacts["ios-arm64"] = a
	if err := broken.validate(); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("bad checksum error = %v", err)
	}

	broken = m.clone()
	is := broken.Artifacts["ios-arm64"]
	is.Commit = "sfx"
	broken.Artifacts["ios-arm64"] = is
	if err := broken.validate(); err == nil || !strings.Contains(err.Error(), "ios-arm64") {
		t.Fatalf("wrong per-artifact commit error = %v", err)
	}
}

func TestManifestRoundTripsThroughJSON(t *testing.T) {
	m := newManifest(defaultReleaseURL)
	m.NDK = "30.0.16248370"
	for _, target := range targets {
		m.Artifacts[target.Key] = artifact{
			File:   assetName(target),
			URL:    defaultReleaseURL + "/" + assetName(target),
			SHA256: strings.Repeat("b", 64),
			Size:   1234,
			Commit: pinnedCommit,
		}
	}
	data, err := m.marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back manifest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.validate(); err != nil {
		t.Fatalf("round-tripped manifest rejected: %v", err)
	}
	if back.LibVersion != libVersion || back.Zig != zigVersion {
		t.Fatalf("toolchain metadata lost: %+v", back)
	}
}

func TestParseNMSymbols(t *testing.T) {
	out := `
0000000000000000 T _ghostty_terminal_new
0000000000000000 T _ghostty_terminal_free
0000000000000000 U _ghostty_not_defined
                 U _ghostty_also_undefined
0000000000000010 D _ghostty_terminal_new
0000000000000000 t _local_helper
`
	got := parseNMSymbols(out)
	if got["ghostty_terminal_new"] != 2 {
		t.Errorf("defined count for ghostty_terminal_new = %d, want 2", got["ghostty_terminal_new"])
	}
	if got["ghostty_terminal_free"] != 1 {
		t.Errorf("ghostty_terminal_free = %d, want 1", got["ghostty_terminal_free"])
	}
	if _, ok := got["ghostty_not_defined"]; ok {
		t.Error("undefined symbol counted as defined")
	}
	if _, ok := got["ghostty_also_undefined"]; ok {
		t.Error("undefined symbol without address counted as defined")
	}
}

func TestSymbolReportNamesMissingAndDuplicateSymbols(t *testing.T) {
	got := map[string]int{
		"ghostty_terminal_new":  2,
		"ghostty_terminal_free": 1,
	}
	want := []string{"ghostty_terminal_new", "ghostty_terminal_free", "ghostty_type_json"}
	missing, duplicates := symbolProblems(got, want)
	if strings.Join(missing, ",") != "ghostty_type_json" {
		t.Errorf("missing = %v", missing)
	}
	if strings.Join(duplicates, ",") != "ghostty_terminal_new" {
		t.Errorf("duplicates = %v", duplicates)
	}
}

func TestDiffABIAcceptsThePinnedManifest(t *testing.T) {
	want := readTestdata(t, "abi-befcdfd2.json")
	if err := diffABI(want, want); err != nil {
		t.Fatalf("identical manifests rejected: %v", err)
	}

	// The host target name is not part of the binding contract: Linux and
	// macOS builds report different OS/target values for the same ABI.
	var altered map[string]any
	if err := json.Unmarshal(want, &altered); err != nil {
		t.Fatal(err)
	}
	altered["abi"].(map[string]any)["os"] = "linux"
	altered["abi"].(map[string]any)["target"] = "x86_64"
	altered["commit"] = "ignored"
	altered["dirty"] = true
	data, _ := json.Marshal(altered)
	if err := diffABI(want, data); err != nil {
		t.Fatalf("host-specific fields rejected: %v", err)
	}
}

func TestDiffABIRejectsVersionAndLayoutDrift(t *testing.T) {
	want := readTestdata(t, "abi-befcdfd2.json")
	var got map[string]any
	if err := json.Unmarshal(want, &got); err != nil {
		t.Fatal(err)
	}

	got["library_version"] = "1.0.1"
	data, _ := json.Marshal(got)
	err := diffABI(want, data)
	if err == nil || !strings.Contains(err.Error(), "library version") || !strings.Contains(err.Error(), "1.0.1") {
		t.Fatalf("version drift error = %v", err)
	}

	got["library_version"] = libVersion
	style := got["types"].(map[string]any)["GhosttyStyle"].(map[string]any)
	style["size"] = float64(16)
	data, _ = json.Marshal(got)
	err = diffABI(want, data)
	if err == nil || !strings.Contains(err.Error(), "GhosttyStyle") || !strings.Contains(err.Error(), "size") {
		t.Fatalf("layout drift error = %v", err)
	}

	got["types"].(map[string]any)["GhosttyStyle"].(map[string]any)["size"] = float64(72)
	delete(got["types"].(map[string]any), "GhosttyGridRef")
	data, _ = json.Marshal(got)
	err = diffABI(want, data)
	if err == nil || !strings.Contains(err.Error(), "GhosttyGridRef") {
		t.Fatalf("missing type error = %v", err)
	}
}

func TestParseLoadAlignments(t *testing.T) {
	out := `
Elf file type is DYN (Shared object file)
Program Headers:
  Type           Offset   VirtAddr           PhysAddr           FileSiz  MemSiz   Flg Align
  LOAD           0x000000 0x0000000000000000 0x0000000000000000 0x0005b8 0x0005b8 R   0x4000
  LOAD           0x005bc0 0x0000000000005bc0 0x0000000000005bc0 0x0001c0 0x0001c0 R E 0x4000
  GNU_RELRO      0x005bc0 0x0000000000005bc0 0x0000000000005bc0 0x0001c0 0x0001c0 R   0x1
`
	got, err := parseLoadAlignments(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 0x4000 || got[1] != 0x4000 {
		t.Fatalf("alignments = %#x, want two 0x4000", got)
	}
	if _, err := parseLoadAlignments("no program headers"); err == nil {
		t.Fatal("missing LOAD segments accepted")
	}
}

func TestCheck16KBPageAlignment(t *testing.T) {
	if err := checkPageAlignment([]uint64{0x4000, 0x10000}); err != nil {
		t.Fatalf("16 KB alignments rejected: %v", err)
	}
	err := checkPageAlignment([]uint64{0x4000, 0x1000})
	if err == nil || !strings.Contains(err.Error(), "0x1000") || !strings.Contains(err.Error(), "16384") {
		t.Fatalf("4 KB alignment error = %v", err)
	}
	if err := checkPageAlignment(nil); err == nil {
		t.Fatal("no LOAD segments accepted")
	}
}

func TestExtractRejectsEscapingPaths(t *testing.T) {
	// Extraction is fed a downloaded archive, so names must never leave
	// the destination directory.
	if _, err := safeJoin("/tmp/out", "../evil"); err == nil {
		t.Fatal("parent escape accepted")
	}
	if _, err := safeJoin("/tmp/out", "/etc/passwd"); err == nil {
		t.Fatal("absolute path accepted")
	}
	if _, err := safeJoin("/tmp/out", "ghostty/file"); err != nil {
		t.Fatalf("ordinary path rejected: %v", err)
	}
}

func TestExpectedSymbolsMatchTheBinding(t *testing.T) {
	if len(expectedSymbols) != 93 {
		t.Fatalf("expectedSymbols = %d, the binding looks up 93", len(expectedSymbols))
	}
	seen := map[string]bool{}
	for _, name := range expectedSymbols {
		if !strings.HasPrefix(name, "ghostty_") {
			t.Errorf("symbol %q is not a ghostty_ symbol", name)
		}
		if seen[name] {
			t.Errorf("symbol %q listed twice", name)
		}
		seen[name] = true
	}
}

func TestAppleHostEnvStripsNixCompiler(t *testing.T) {
	t.Setenv("SDKROOT", "/nix/store/example-apple-sdk")
	t.Setenv("MACOSX_DEPLOYMENT_TARGET", "14.0")
	t.Setenv("CC", "clang")
	t.Setenv("PATH", "/nix/store/example-clang/bin:/usr/bin:/bin")
	env := appleHostEnv()
	joined := strings.Join(env, "\n")
	for _, banned := range []string{"SDKROOT=", "MACOSX_DEPLOYMENT_TARGET=", "CC=", "CXX="} {
		if strings.Contains(joined, "\n"+banned) || strings.HasPrefix(joined, banned) {
			t.Errorf("appleHostEnv kept %s", banned)
		}
	}
	found := false
	for _, entry := range env {
		if entry == "PATH=/usr/bin:/bin:/usr/sbin:/sbin" {
			found = true
		}
	}
	if !found {
		t.Errorf("appleHostEnv did not pin PATH to the system tools: %v", env)
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
