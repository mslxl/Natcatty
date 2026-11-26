package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
)

// This file pins the parts of the CI wiring a test host can check. A workflow
// is the only place where "every pull request runs this" can be stated, and
// GitHub Actions cannot be run from here, so the wiring is checked
// structurally and the scripts it calls are exercised directly. Widening a
// job's permissions or dropping the desktop suite then fails `go test`, not a
// release.

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
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
