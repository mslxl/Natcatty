package release

import (
	"strings"
	"testing"
)

func TestSizeGrowth(t *testing.T) {
	growth := SizeGrowth{GOOS: "darwin", GOARCH: "arm64", Baseline: 1_000_000, WithPluginRuntime: 1_800_000}
	if got := growth.Platform(); got != "darwin/arm64" {
		t.Fatalf("Platform() = %q", got)
	}
	if got := growth.Growth(); got != 800_000 {
		t.Fatalf("Growth() = %d", got)
	}
	if !growth.WithinBudget() {
		t.Fatal("800 KB does not fit the 10 MiB budget")
	}
	if !strings.Contains(growth.String(), "within=true") {
		t.Fatalf("String() = %q", growth.String())
	}

	over := SizeGrowth{GOOS: "linux", GOARCH: "amd64",
		Baseline: 1 << 20, WithPluginRuntime: 1<<20 + PluginSizeGrowthBudget + 1}
	if over.WithinBudget() {
		t.Fatal("growth one byte over the budget is reported within budget")
	}
	if !strings.Contains(over.String(), "within=false") {
		t.Fatalf("String() = %q", over.String())
	}
}
