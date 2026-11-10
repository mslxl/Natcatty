package release

import "fmt"

// PluginSizeGrowthBudget is the soft per-target growth budget for adding the
// plugin runtime adapter with both wazero engines. Growth is measured by
// linking a minimal program with and without the adapter under the same
// toolchain, constructing a runtime so both engine implementations stay
// reachable; going beyond the budget requires a written justification recorded
// in the release check.
const PluginSizeGrowthBudget int64 = 10 << 20

// SizeGrowth is the linked-size delta for one target.
type SizeGrowth struct {
	GOOS   string
	GOARCH string
	// Baseline is the minimal binary without the plugin runtime, in bytes.
	Baseline int64
	// WithPluginRuntime is the same program importing the adapter and
	// constructing a runtime, in bytes.
	WithPluginRuntime int64
}

// Platform returns goos/goarch.
func (g SizeGrowth) Platform() string { return g.GOOS + "/" + g.GOARCH }

// Growth is the bytes added by the plugin runtime adapter, including both the
// wazero compiler and the interpreter.
func (g SizeGrowth) Growth() int64 { return g.WithPluginRuntime - g.Baseline }

// WithinBudget reports whether the growth fits the 10 MiB budget.
func (g SizeGrowth) WithinBudget() bool { return g.Growth() <= PluginSizeGrowthBudget }

func (g SizeGrowth) String() string {
	return fmt.Sprintf("%s baseline=%d with-plugin-runtime=%d growth=%d budget=%d within=%t",
		g.Platform(), g.Baseline, g.WithPluginRuntime, g.Growth(), PluginSizeGrowthBudget,
		g.WithinBudget())
}
