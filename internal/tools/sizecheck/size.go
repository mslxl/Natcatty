// Command sizecheck measures the linked cost of the plugin runtime adapter on
// every supported target. It is checked Go tooling, not part of the shipped
// app; the release pipeline runs it as a soft size gate.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "sizecheck:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("sizecheck", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: sizecheck [-root DIR] [-platforms LIST] [-justify REASON]")
		flags.PrintDefaults()
	}
	root := flags.String("root", ".", "module root containing go.mod")
	platforms := flags.String("platforms", strings.Join(appplugin.SupportedPlatforms(), ","),
		"comma-separated goos/goarch targets")
	justify := flags.String("justify", "", "written reason for exceeding the size budget")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("sizecheck takes no positional arguments")
	}
	if _, err := os.Stat(filepath.Join(*root, "go.mod")); err != nil {
		return fmt.Errorf("sizecheck: -root %q is not a Go module root: %w", *root, err)
	}

	targets := splitList(*platforms)
	if len(targets) == 0 {
		return fmt.Errorf("sizecheck: -platforms must name at least one target")
	}
	var over []release.SizeGrowth
	for _, platform := range targets {
		goos, goarch, err := parsePlatform(platform)
		if err != nil {
			return err
		}
		if !appplugin.SupportsPlatform(goos, goarch) {
			return fmt.Errorf("sizecheck: %s is not a supported plugin target", platform)
		}
		growth, err := measurePluginRuntimeGrowth(*root, goos, goarch)
		if err != nil {
			return err
		}
		fmt.Println(growth.String())
		if !growth.WithinBudget() {
			over = append(over, growth)
		}
	}
	if len(over) == 0 {
		return nil
	}
	if strings.TrimSpace(*justify) == "" {
		return fmt.Errorf("the wazero growth budget of %d bytes is exceeded on %s; "+
			"rerun with -justify <reason> to record accepted growth",
			release.PluginSizeGrowthBudget, joinPlatforms(over))
	}
	fmt.Printf("accepted growth justification: %s\n", strings.TrimSpace(*justify))
	return nil
}

// measurePluginRuntimeGrowth links two minimal programs under the target
// toolchain: one empty and one importing the real plugin runtime adapter and
// constructing a runtime. The delta is the adapter's linked cost, including
// both wazero engines. Constructing Runtime keeps the engine selection
// reachable so the linker cannot drop the unused engine; a blank import of
// wazero would let dead-code elimination remove both and understate the growth.
func measurePluginRuntimeGrowth(root, goos, goarch string) (release.SizeGrowth, error) {
	// The probe lives under the module root so its imports resolve in module
	// mode. The leading dot keeps it out of ./... patterns.
	dir, err := os.MkdirTemp(root, ".size-probe-*")
	if err != nil {
		return release.SizeGrowth{}, fmt.Errorf("sizecheck: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	baselineSource := filepath.Join(dir, "without.go")
	adapterSource := filepath.Join(dir, "with.go")
	if err := os.WriteFile(baselineSource, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		return release.SizeGrowth{}, fmt.Errorf("sizecheck: %w", err)
	}
	adapterProbe := `package main

import (
	"context"

	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
)

func main() {
	runtime := adapterwasm.NewRuntime()
	_ = runtime.RuntimeStatus()
	_ = runtime.Close(context.Background())
}
`
	if err := os.WriteFile(adapterSource, []byte(adapterProbe), 0o600); err != nil {
		return release.SizeGrowth{}, fmt.Errorf("sizecheck: %w", err)
	}

	baselineBinary := filepath.Join(dir, "without")
	adapterBinary := filepath.Join(dir, "with")
	if err := buildSizeProbe(root, goos, goarch, baselineSource, baselineBinary); err != nil {
		return release.SizeGrowth{}, err
	}
	if err := buildSizeProbe(root, goos, goarch, adapterSource, adapterBinary); err != nil {
		return release.SizeGrowth{}, err
	}

	baseline, err := fileSize(baselineBinary)
	if err != nil {
		return release.SizeGrowth{}, err
	}
	withRuntime, err := fileSize(adapterBinary)
	if err != nil {
		return release.SizeGrowth{}, err
	}
	return release.SizeGrowth{
		GOOS:              goos,
		GOARCH:            goarch,
		Baseline:          baseline,
		WithPluginRuntime: withRuntime,
	}, nil
}

func buildSizeProbe(root, goos, goarch, source, output string) error {
	command := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", output, source)
	command.Dir = root
	command.Env = sizeBuildEnv(goos, goarch)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("sizecheck: build %s/%s: %w: %s",
			goos, goarch, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// sizeBuildEnv replaces any target selection inherited from the caller so the
// probe always builds for goos/goarch with cgo disabled.
func sizeBuildEnv(goos, goarch string) []string {
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		switch key {
		case "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("sizecheck: %w", err)
	}
	return info.Size(), nil
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func parsePlatform(platform string) (string, string, error) {
	parts := strings.Split(platform, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("sizecheck: platform %q must be goos/goarch", platform)
	}
	return parts[0], parts[1], nil
}

func joinPlatforms(growth []release.SizeGrowth) string {
	platforms := make([]string, 0, len(growth))
	for _, target := range growth {
		platforms = append(platforms, target.Platform())
	}
	return strings.Join(platforms, ", ")
}
