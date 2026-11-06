// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGitCommitPrefersExplicitOverride(t *testing.T) {
	t.Setenv(commitEnv, "from-environment")
	got, err := gitCommit(t.TempDir(), "from-flag")
	if err != nil {
		t.Fatalf("explicit override must not need git: %v", err)
	}
	if got != "from-flag" {
		t.Fatalf("gitCommit = %q, want from-flag", got)
	}
}

func TestGitCommitFallsBackToEnvironment(t *testing.T) {
	t.Setenv(commitEnv, "from-environment")
	got, err := gitCommit(t.TempDir(), "")
	if err != nil {
		t.Fatalf("environment override must not need git: %v", err)
	}
	if got != "from-environment" {
		t.Fatalf("gitCommit = %q, want from-environment", got)
	}
}

func TestGitCommitFallsBackToGit(t *testing.T) {
	t.Setenv(commitEnv, "")
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	want, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skipf("git is unavailable in this environment: %v", err)
	}
	got, err := gitCommit(root, "")
	if err != nil {
		t.Fatalf("gitCommit: %v", err)
	}
	if got != strings.TrimSpace(string(want)) {
		t.Fatalf("gitCommit = %q, want git HEAD %q", got, strings.TrimSpace(string(want)))
	}
}

func TestGitCommitFailsClosedWithoutOverrideOrGit(t *testing.T) {
	t.Setenv(commitEnv, "")
	t.Setenv("PATH", t.TempDir())
	if _, err := gitCommit(t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "cannot determine the gate commit") {
		t.Fatalf("missing commit must fail closed, got %v", err)
	}
}

func TestRunRejectsShortFuzzTime(t *testing.T) {
	err := run([]string{
		"run", "-cell", "debian-12/1.3.2", "-commit", testCommit,
		"-fuzztime", "4s", "-out", t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "at least 5s") {
		t.Fatalf("short fuzz time must be rejected before a cell runs, got %v", err)
	}
}

func TestRunRejectsLabAddrForDockerCell(t *testing.T) {
	err := run([]string{
		"run", "-cell", "debian-12/1.3.2", "-commit", testCommit,
		"-fuzztime", "5s", "-lab-addr", "127.0.0.1:22", "-out", t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "only valid for native cells") {
		t.Fatalf("-lab-addr on a Docker cell must be rejected, got %v", err)
	}
}

func TestRemotePlatformRejectsUnreachableEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := remotePlatform(ctx, addr); err == nil {
		t.Fatalf("remotePlatform against closed endpoint %s must fail", addr)
	}
}

func TestRunCellRejectsMissingCommit(t *testing.T) {
	cell := testCell(t, "debian-12/1.3.2")
	_, err := runCell(context.Background(), cell, runOptions{
		root:     t.TempDir(),
		outDir:   t.TempDir(),
		fuzzTime: minReportFuzzTime,
		timeout:  time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "no gate commit") {
		t.Fatalf("runCell without a commit must fail closed, got %v", err)
	}
}

func TestRunCellRejectsMislabelledNativeHost(t *testing.T) {
	// Pick a native cell that cannot match this host, whatever the host is.
	cellID := "macos/1.3.2"
	switch runtime.GOOS {
	case hostOSDarwin:
		cellID = "freebsd-14/1.3.2"
	case hostOSFreeBSD:
		cellID = "macos/1.3.2"
	}
	cell := testCell(t, cellID)
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	_, err = runCell(context.Background(), cell, runOptions{
		root:     root,
		outDir:   t.TempDir(),
		commit:   testCommit,
		fuzzTime: minReportFuzzTime,
		timeout:  time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "requires host OS") {
		t.Fatalf("native cell on the wrong host must fail before running, got %v", err)
	}
}

func TestRunCellRejectsShortFuzzTime(t *testing.T) {
	cell := testCell(t, "debian-12/1.3.2")
	_, err := runCell(context.Background(), cell, runOptions{
		root:     t.TempDir(),
		outDir:   t.TempDir(),
		commit:   testCommit,
		fuzzTime: minReportFuzzTime - time.Second,
		timeout:  time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "gate minimum") {
		t.Fatalf("runCell with a short fuzz time must fail closed, got %v", err)
	}
}

// TestFuzzSuiteRejectsMissingTarget proves the negative path that keeps a
// deleted or renamed fuzz target from silently passing: `go test -fuzz` exits
// zero when no target matches, so runFuzzSuite must detect that no real fuzz
// run happened.
func TestFuzzSuiteRejectsMissingTarget(t *testing.T) {
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goBinary(); err != nil {
		t.Setenv("GO", filepath.Join(runtime.GOROOT(), "bin", "go"))
	}
	original := fuzzTargets
	fuzzTargets = []fuzzSpec{{pkg: "./internal/mosh", target: "FuzzTargetThatDoesNotExist"}}
	defer func() { fuzzTargets = original }()

	suite, err := runFuzzSuite(context.Background(), runOptions{
		root: root, fuzzTime: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("runFuzzSuite returned a setup error: %v", err)
	}
	if suite.Status != "fail" {
		t.Fatalf("missing fuzz target must fail the suite, got status %q", suite.Status)
	}
	if !strings.Contains(suite.Output, "FuzzTargetThatDoesNotExist") {
		t.Fatalf("fuzz failure does not name the missing target:\n%s", suite.Output)
	}
}

func TestHostUnameMatchesRuntimeOS(t *testing.T) {
	uname := hostUname()
	if uname == "" {
		t.Fatal("hostUname returned an empty value on this platform")
	}
	switch runtime.GOOS {
	case hostOSLinux, hostOSDarwin, hostOSFreeBSD:
		if !strings.EqualFold(uname, runtime.GOOS) {
			t.Fatalf("hostUname = %q, runtime.GOOS = %q", uname, runtime.GOOS)
		}
	}
}
