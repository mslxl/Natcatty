package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

// This file pins the CI wiring itself. A workflow is the only place where
// "every pull request runs this" can be stated, and GitHub Actions cannot be
// run from a test host, so the gates are checked structurally here and
// behaviourally in the scripts they call. Removing a job, widening an `if`,
// or pointing the link smoke at a hardcoded ABI list then fails `go test`,
// not a release.

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// jobBlock returns one job from a workflow, from its two-space key to the
// next two-space key. Nested keys are indented further, so this separates
// jobs without a YAML dependency.
func jobBlock(t *testing.T, workflow, job string) string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	start := -1
	for i, line := range lines {
		if line == "  "+job+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("workflow has no job %q", job)
	}
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.HasSuffix(line, ":") && !strings.HasPrefix(line, "  #") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

func wantContains(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%s does not contain %q", what, want)
		}
	}
}

func wantAbsent(t *testing.T, what, text string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(text, bad) {
			t.Errorf("%s must not contain %q", what, bad)
		}
	}
}

// TestPhoneCIGatesEveryPhoneTarget pins the compile and link jobs. Compile
// and link run on every pull request: neither job may grow an `if`, and the
// iOS matrix must keep both the device and the simulator slice because their
// native artifacts differ.
func TestPhoneCIGatesEveryPhoneTarget(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/phone-ci.yml")
	wantContains(t, "phone-ci.yml", wf,
		"\non:", "\n  pull_request:", "\n  push:", "\n  schedule:", "\n  workflow_call:",
		"\n  compile-ios:", "\n  compile-android:",
		"\n  link-smoke-ios:", "\n  link-smoke-android:",
		"\n  android-matrix:", "\n  phone-deps:",
	)

	compileIOS := jobBlock(t, wf, "compile-ios")
	wantContains(t, "compile-ios", compileIOS,
		"- simulator", "- device",
		"scripts/mobile/check-compile.sh ios/${{ matrix.target }}")
	wantAbsent(t, "compile-ios", compileIOS, "\n    if:")

	linkIOS := jobBlock(t, wf, "link-smoke-ios")
	wantContains(t, "link-smoke-ios", linkIOS,
		"- simulator", "- device",
		"scripts/mobile/link-smoke.sh ${{ matrix.target }}")
	wantAbsent(t, "link-smoke-ios", linkIOS, "\n    if:")

	// Every shipped Android ABI, not a copy of the list in YAML.
	rows := readAndroidABIs(t)
	if len(rows) == 0 {
		t.Fatal("android-abis.txt is empty; the Android link smoke has no ABIs")
	}
	matrix := jobBlock(t, wf, "android-matrix")
	wantContains(t, "android-matrix", matrix, "scripts/mobile/android-abis.txt")
	for _, job := range []string{"compile-android", "link-smoke-android"} {
		block := jobBlock(t, wf, job)
		wantContains(t, job, block,
			"fromJSON(needs.android-matrix.outputs.abis)",
			"scripts/mobile/android-libs.sh --abi ${{ matrix.abi }}")
		wantAbsent(t, job, block, "\n    if:")
	}
	linkAndroid := jobBlock(t, wf, "link-smoke-android")
	wantContains(t, "link-smoke-android", linkAndroid,
		"scripts/mobile/android-link-smoke.sh --abi ${{ matrix.abi }}")

	// The Android link smoke compares the linked object with the binding
	// list through check-symbols.sh, which is also what the iOS compile
	// calls. Both directions are pinned in TestCheckSymbolsFailsOnDrift.
	wantContains(t, "android-link-smoke.sh", readRepoFile(t, "scripts/mobile/android-link-smoke.sh"),
		"scripts/mobile/check-symbols.sh elf")
	wantContains(t, "check-compile.sh", readRepoFile(t, "scripts/mobile/check-compile.sh"),
		"scripts/mobile/check-symbols.sh macho")
	wantContains(t, "link-smoke.sh", readRepoFile(t, "scripts/mobile/link-smoke.sh"),
		"ui/terminal/internal/vt/symbols.txt", "duplicate")
}

// TestPhoneCIDependencyGate pins the desktop-leak gate, on every pull
// request, and the message that names the forbidden package.
func TestPhoneCIDependencyGate(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/phone-ci.yml")
	deps := jobBlock(t, wf, "phone-deps")
	wantContains(t, "phone-deps", deps, "scripts/mobile/check-deps.sh")
	wantAbsent(t, "phone-deps", deps, "\n    if:")

	script := readRepoFile(t, "scripts/mobile/check-deps.sh")
	wantContains(t, "check-deps.sh", script,
		"desktop-only $forbidden_path is in the phone graph",
		"check ios arm64", "check android arm64", "exit 1")
}

// TestPhoneCISmokeCoverage pins when the emulator and simulator smoke run:
// phone-code pull requests through the `changes` job, nightly through cron,
// and before a tag because release.yml calls this workflow and its build
// waits for it.
func TestPhoneCISmokeCoverage(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/phone-ci.yml")
	changes := jobBlock(t, wf, "changes")
	wantContains(t, "changes", changes,
		"smoke: ${{ steps.changed.outputs.smoke }}",
		"REF_TYPE: ${{ github.ref_type }}",
		`[ "$REF_TYPE" = "tag" ]`,
		"smoke=1")

	for _, job := range []string{"smoke-ios", "smoke-android"} {
		block := jobBlock(t, wf, job)
		wantContains(t, job, block,
			"needs: changes",
			"if: needs.changes.outputs.smoke == 'true'")
	}
	wantContains(t, "smoke-ios", jobBlock(t, wf, "smoke-ios"),
		"scripts/mobile/ios-render.sh")
	smokeAndroid := jobBlock(t, wf, "smoke-android")
	wantContains(t, "smoke-android", smokeAndroid,
		"scripts/mobile/android-render.sh", "arch: arm64-v8a")
	// One ARM emulator is deliberate: every ABI is covered statically by
	// the linker gate above, and there is no x86 AVD run.
	wantAbsent(t, "smoke-android", smokeAndroid, "arch: x86")

	wantContains(t, "phone-ci.yml", wf, "\n    - cron: ")

	release := readRepoFile(t, ".github/workflows/release.yml")
	wantContains(t, "release phone-smoke", jobBlock(t, release, "phone-smoke"),
		"uses: ./.github/workflows/phone-ci.yml")
	build := jobBlock(t, release, "build")
	if !strings.Contains(build, "needs:") {
		t.Fatal("release build has no needs; it could start before the phone gates")
	}
	wantContains(t, "release build", build, "- phone-smoke")
}

// TestPhoneCIIOSArtifacts pins the simulator artifact and the Apple-account
// gate on the device package.
func TestPhoneCIIOSArtifacts(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/phone-ci.yml")

	sim := jobBlock(t, wf, "ios-simulator-artifact")
	wantContains(t, "ios-simulator-artifact", sim,
		"go tool mygo build -platform ios/simulator",
		"platform IOSSIMULATOR",
		"actions/upload-artifact@v4",
		"natcatty-ios-simulator-${{ github.sha }}",
		"if-no-files-found: error")

	device := jobBlock(t, wf, "ios-device-package")
	wantContains(t, "ios-device-package", device,
		"if: ${{ vars.APPLE_DEVELOPMENT_TEAM != '' && github.event_name != 'pull_request' }}",
		"go tool mygo build -platform ios",
		"dist/ios-arm64/*.ipa",
		"if-no-files-found: error")
	// The unsigned simulator app is the only package without an account;
	// phone-ci must not build a device package outside the guarded job.
	if n := strings.Count(wf, "go tool mygo build -platform ios -o"); n != 1 {
		t.Errorf("phone-ci has %d device builds, want 1 guarded by ios-device-package", n)
	}
}

// TestUpstreamSyncOnlyReports pins the scheduled sync: read-only contents,
// issue write, one issue updated in place, and no history-changing command.
func TestUpstreamSyncOnlyReports(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/upstream-sync.yml")
	wantContains(t, "upstream-sync.yml", wf,
		"\n  schedule:", "\n    - cron: ",
		"contents: read", "issues: write",
		"gh issue list --state open", "gh issue edit", "gh issue create")
	wantAbsent(t, "upstream-sync.yml", wf,
		"contents: write",
		// "git merge " with a space: `git merge-base` is a read-only query
		// and is the one merge-shaped command this workflow may run.
		"git merge ", "git rebase", "git push", "git cherry-pick",
		"gh pr merge", "gh pr create")
}

// TestDesktopSuiteStaysInCI pins the desktop side: a desktop-only pull
// request still runs the suite, because ci.yml triggers on every pull
// request and its paths-ignore only drops documentation.
func TestDesktopSuiteStaysInCI(t *testing.T) {
	wf := readRepoFile(t, ".github/workflows/ci.yml")
	wantContains(t, "ci.yml", wf,
		"\n  pull_request:", "go test ./... -count=1")
}

// TestPhoneGateScriptsAreExecutable keeps the workflows runnable: job steps
// invoke these paths directly.
func TestPhoneGateScriptsAreExecutable(t *testing.T) {
	root := testutil.ModuleRoot(t)
	for _, rel := range []string{
		"scripts/mobile/fetch-artifacts.sh",
		"scripts/mobile/check-symbols.sh",
		"scripts/mobile/android-link-smoke.sh",
		"scripts/mobile/ios-render.sh",
		"scripts/mobile/check-deps.sh",
		"scripts/mobile/link-smoke.sh",
		"scripts/mobile/check-compile.sh",
		"scripts/mobile/android-libs.sh",
		"scripts/mobile/ios-syso.sh",
	} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s is not executable; a workflow step cannot invoke it", rel)
		}
	}
}

// TestCheckSymbolsFailsOnDrift runs the gate itself against a synthetic
// symbol listing generated from the binding list. The listing is what a
// linker would emit, so this exercises the real comparison: a missing symbol
// and a duplicated symbol must both fail and name the symbol.
func TestCheckSymbolsFailsOnDrift(t *testing.T) {
	root := testutil.ModuleRoot(t)
	script := filepath.Join(root, "scripts/mobile/check-symbols.sh")
	data, err := os.ReadFile(filepath.Join(root, "ui/terminal/internal/vt/symbols.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var symbols []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			symbols = append(symbols, line)
		}
	}
	if len(symbols) < 2 {
		t.Fatalf("symbols.txt has %d symbols, want the binding list", len(symbols))
	}

	run := func(t *testing.T, listing string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		artifact := filepath.Join(dir, "libapp.a")
		if err := os.WriteFile(artifact, []byte("not really an archive"), 0o644); err != nil {
			t.Fatal(err)
		}
		nm := filepath.Join(dir, "nm")
		stub := "#!/bin/sh\ncat '" + filepath.Join(dir, "nmlist") + "'\n"
		if err := os.WriteFile(nm, []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nmlist"), []byte(listing), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", script, "macho", artifact, nm).CombinedOutput()
		return string(out), err
	}

	var complete strings.Builder
	for _, sym := range symbols {
		complete.WriteString("0000000000000000 T _" + sym + "\n")
	}
	out, err := run(t, complete.String())
	if err != nil {
		t.Fatalf("complete listing failed: %v\n%s", err, out)
	}
	wantContains(t, "complete listing", out, "defines all", "exactly once")

	var missing strings.Builder
	for _, sym := range symbols[1:] {
		missing.WriteString("0000000000000000 T _" + sym + "\n")
	}
	out, err = run(t, missing.String())
	if err == nil {
		t.Fatalf("a listing without %s passed the gate:\n%s", symbols[0], out)
	}
	wantContains(t, "missing-symbol output", out,
		"is missing the pinned symbol "+symbols[0])

	var duplicated strings.Builder
	for _, sym := range symbols {
		duplicated.WriteString("0000000000000000 T _" + sym + "\n")
	}
	duplicated.WriteString("0000000000000000 T _" + symbols[0] + "\n")
	out, err = run(t, duplicated.String())
	if err == nil {
		t.Fatalf("a listing with %s twice passed the gate:\n%s", symbols[0], out)
	}
	wantContains(t, "duplicate-symbol output", out,
		"defines the pinned symbol "+symbols[0]+" 2 times")
}
