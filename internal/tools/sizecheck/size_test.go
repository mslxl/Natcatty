package main

import (
	"runtime"
	"strings"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
)

func TestParsePlatform(t *testing.T) {
	goos, goarch, err := parsePlatform("darwin/amd64")
	if err != nil {
		t.Fatalf("parsePlatform: %v", err)
	}
	if goos != "darwin" || goarch != "amd64" {
		t.Fatalf("parsed %s/%s", goos, goarch)
	}
	for _, invalid := range []string{"", "darwin", "darwin/", "/amd64", "a/b/c"} {
		if _, _, err := parsePlatform(invalid); err == nil {
			t.Errorf("parsePlatform(%q) accepted", invalid)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" darwin/arm64, ,linux/amd64 ,")
	if len(got) != 2 || got[0] != "darwin/arm64" || got[1] != "linux/amd64" {
		t.Fatalf("splitList = %v", got)
	}
}

func TestSizeBuildEnvOverridesTargetAndCgo(t *testing.T) {
	t.Setenv("GOOS", "plan9")
	t.Setenv("GOARCH", "mips")
	t.Setenv("CGO_ENABLED", "1")
	environment := sizeBuildEnv("windows", "amd64")
	var sawOS, sawArch, sawCgo bool
	for _, entry := range environment {
		switch entry {
		case "GOOS=windows":
			sawOS = true
		case "GOARCH=amd64":
			sawArch = true
		case "CGO_ENABLED=0":
			sawCgo = true
		case "GOOS=plan9", "GOARCH=mips", "CGO_ENABLED=1":
			t.Fatalf("inherited environment entry %q was not replaced", entry)
		}
	}
	if !sawOS || !sawArch || !sawCgo {
		t.Fatalf("environment = %v", environment)
	}
}

func TestCheckSizeRejectsAnEmptyPlatformList(t *testing.T) {
	root := testutil.ModuleRoot(t)
	if err := run([]string{"-root", root, "-platforms", " , "}); err == nil {
		t.Fatal("check-size accepted an empty platform list")
	}
}

func TestCheckSizeRejectsAnUnsupportedPlatform(t *testing.T) {
	root := testutil.ModuleRoot(t)
	err := run([]string{"-root", root, "-platforms", "windows/arm64"})
	if err == nil || !strings.Contains(err.Error(), "not a supported plugin target") {
		t.Fatalf("check-size error = %v", err)
	}
}

func TestCheckSizeDefaultsToTheSupportedMatrix(t *testing.T) {
	platforms := strings.Join(appplugin.SupportedPlatforms(), ",")
	for _, want := range []string{"darwin/arm64", "darwin/amd64", "windows/amd64", "linux/amd64", "linux/arm64"} {
		if !strings.Contains(platforms, want) {
			t.Fatalf("the default size matrix %q is missing %s", platforms, want)
		}
	}
}

// TestMeasurePluginRuntimeGrowth links the real probes for the host target, so
// the budget is exercised rather than only its arithmetic. Cross-compilation
// works, but building for the host keeps the test independent of installed
// cross toolchains.
func TestMeasurePluginRuntimeGrowth(t *testing.T) {
	if !appplugin.SupportsPlatform(runtime.GOOS, runtime.GOARCH) {
		t.Skipf("plugins are unavailable on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	growth, err := measurePluginRuntimeGrowth(testutil.ModuleRoot(t), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("measurePluginRuntimeGrowth: %v", err)
	}
	if growth.WithPluginRuntime <= growth.Baseline {
		t.Fatalf("with-plugin-runtime binary (%d bytes) is not larger than the baseline (%d bytes)",
			growth.WithPluginRuntime, growth.Baseline)
	}
	if !growth.WithinBudget() {
		t.Fatalf("%s exceeds the growth budget: %s", growth.Platform(), growth)
	}
	t.Log(growth.String())
}
