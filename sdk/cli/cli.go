// Package cli implements the natcatty-plugin developer tool. It scaffolds,
// builds, checks, runs, and launches a plugin without the app; the `run` verb
// uses the fake host and `dev` hands the same module to app development mode.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mslxl/natcatty/sdk/conformance"
	"github.com/mslxl/natcatty/sdk/fakehost"
)

// CommandRunner runs an external build command. Tests inject a runner so the
// build path never depends on a local Rust toolchain.
type CommandRunner func(ctx context.Context, dir, name string, args []string,
	stdout, stderr io.Writer) error

// Launcher starts the app in development mode. Tests inject a launcher so the
// path is exercised without a window.
type Launcher func(ctx context.Context, binary, directory string,
	stdout, stderr io.Writer) error

// Options carries the CLI's injectable edges.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	Runner CommandRunner
	// Launcher starts the app with NATCATTY_PLUGIN_DIR set.
	Launcher Launcher
}

// ToolchainError reports a missing external command.
type ToolchainError struct {
	Command string
	Purpose string
}

func (e *ToolchainError) Error() string {
	purpose := e.Purpose
	if purpose == "" {
		purpose = "the plugin SDK"
	}
	return fmt.Sprintf("%s requires %q on PATH", purpose, e.Command)
}

// ConformanceError reports structural or behavioral validation failures. The
// command prints the details to stderr; Error is a one-line summary so the
// process entry point does not repeat them.
type ConformanceError struct {
	Findings    []conformance.Finding
	BehaviorErr error
	RuntimeErr  error
	ParityErr   error
}

func (e *ConformanceError) Error() string {
	var parts []string
	if len(e.Findings) > 0 {
		parts = append(parts, fmt.Sprintf("%d structural findings", len(e.Findings)))
	}
	if e.BehaviorErr != nil {
		parts = append(parts, "fake-host check failed")
	}
	if e.RuntimeErr != nil {
		parts = append(parts, "real-runtime check failed")
	}
	if e.ParityErr != nil {
		parts = append(parts, "fake-host and real-runtime behavior differ")
	}
	if len(parts) == 0 {
		return "plugin does not conform to the v1 ABI"
	}
	return "plugin does not conform to the v1 ABI: " + strings.Join(parts, ", ")
}

// reportConformance writes the details behind a failed check.
func reportConformance(w io.Writer, report conformance.Report) {
	for _, finding := range report.Findings {
		fmt.Fprintln(w, finding)
	}
	if report.BehaviorErr != nil {
		fmt.Fprintf(w, "fake host: %v\n", report.BehaviorErr)
	}
	if report.RuntimeErr != nil {
		fmt.Fprintf(w, "real runtime: %v\n", report.RuntimeErr)
	}
	if report.ParityErr != nil {
		fmt.Fprintf(w, "parity: %v\n", report.ParityErr)
	}
}

func conformanceError(report conformance.Report) *ConformanceError {
	return &ConformanceError{
		Findings: report.Findings, BehaviorErr: report.BehaviorErr,
		RuntimeErr: report.RuntimeErr, ParityErr: report.ParityErr,
	}
}

// Run dispatches one CLI invocation. It returns an error for a failed check,
// a missing toolchain, or invalid arguments.
func Run(ctx context.Context, args []string, opts Options) error {
	opts = normalize(opts)
	if len(args) == 0 {
		writeUsage(opts.Stdout)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(opts.Stdout)
		return nil
	case "init":
		return runInit(ctx, args[1:], opts)
	case "build":
		return runBuild(ctx, args[1:], opts)
	case "check":
		return runCheck(ctx, args[1:], opts)
	case "run":
		return runFakeHost(ctx, args[1:], opts)
	case "dev":
		return runDev(ctx, args[1:], opts)
	default:
		return fmt.Errorf("natcatty-plugin: unknown command %q (try help)", args[0])
	}
}

func normalize(opts Options) Options {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Runner == nil {
		opts.Runner = execRunner
	}
	if opts.Launcher == nil {
		opts.Launcher = execLauncher
	}
	return opts
}

func writeUsage(w io.Writer) {
	fmt.Fprint(w, `natcatty-plugin - develop Natcatty plugins

Usage:
  natcatty-plugin init <dir> [--id ID] [--name NAME]
  natcatty-plugin build <dir> [--artifact PATH]
  natcatty-plugin check <dir> [--structural-only]
  natcatty-plugin run <dir> [--event ID]... [--seed N] [--start-ns N] [--step-ns N] [--json]
  natcatty-plugin dev <dir> [--app PATH]

init   scaffolds a standalone Rust plugin with the generated ABI bindings
build  runs the declared toolchain (cargo or build.sh), copies the wasm, and
       refreshes plugin.toml's sha256; --artifact copies a prebuilt module
check  runs structural conformance, the fake host, and the same scenario on
       the real runtime, then compares their observations
run    drives the module in the fake host with a virtual clock and events
dev    checks the plugin, then launches the app with NATCATTY_PLUGIN_DIR

Regenerate the checked-in Rust and Go bindings with:
  go run ./sdk/idl/gen
`)
}

func execRunner(ctx context.Context, dir, name string, args []string,
	stdout, stderr io.Writer) error {
	if _, err := exec.LookPath(name); err != nil {
		return &ToolchainError{Command: name, Purpose: "building a plugin"}
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func execLauncher(ctx context.Context, binary, directory string,
	stdout, stderr io.Writer) error {
	if _, err := exec.LookPath(binary); err != nil {
		return &ToolchainError{Command: binary, Purpose: "development mode"}
	}
	command := exec.CommandContext(ctx, binary)
	command.Env = append(os.Environ(), "NATCATTY_PLUGIN_DIR="+directory)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("launch %s: %w", binary, err)
	}
	return nil
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// parseFlags accepts flags before or after the positional directory. The
// standard flag package stops at the first non-flag argument, so the parser
// collects positionals and reparses the remainder.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
		if len(rest) == 0 {
			break
		}
	}
	if len(positionals) != 1 {
		return nil, fmt.Errorf("%s: exactly one plugin directory is required", fs.Name())
	}
	return positionals, nil
}

func runCheck(ctx context.Context, args []string, opts Options) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	structuralOnly := fs.Bool("structural-only", false, "skip the behavioral run")
	events := stringList{}
	fs.Var(&events, "event", "behavioral event (repeatable; default: every declared command)")
	positionals, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	report, err := conformance.Check(ctx, conformance.Options{
		Directory:      positionals[0],
		Events:         events,
		StructuralOnly: *structuralOnly,
	})
	if err != nil {
		return err
	}
	if !report.OK() {
		reportConformance(opts.Stderr, report)
		return conformanceError(report)
	}
	if report.Behavior != nil {
		fmt.Fprintf(opts.Stdout, "%s: conformant (%d events, fake host + real runtime)\n",
			report.Behavior.PluginID, len(report.Behavior.Events))
		if report.ParitySkipped != "" {
			fmt.Fprintf(opts.Stdout, "parity skipped: %s\n", report.ParitySkipped)
		}
	} else {
		fmt.Fprintln(opts.Stdout, "structural: conformant")
	}
	return nil
}

func runFakeHost(ctx context.Context, args []string, opts Options) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	events := stringList{}
	fs.Var(&events, "event", "command event to dispatch (repeatable)")
	seed := fs.Uint64("seed", 0, "deterministic random seed")
	startNS := fs.Int64("start-ns", 0, "initial virtual clock value in nanoseconds")
	stepNS := fs.Int64("step-ns", 0, "virtual clock advance per now_ns call")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	positionals, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	report, err := fakehost.Run(ctx, fakehost.Options{
		Directory: positionals[0],
		Events:    events,
		Seed:      *seed,
		StartNS:   *startNS,
		StepNS:    *stepNS,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(opts.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(jsonReportFrom(report))
	}
	fmt.Fprintf(opts.Stdout, "plugin %s\n", report.PluginID)
	for _, entry := range report.Logs {
		fmt.Fprintf(opts.Stdout, "log %s: %s\n", entry.Level, entry.Message)
	}
	for _, event := range report.Events {
		fmt.Fprintf(opts.Stdout, "event %s\n", event.Command)
	}
	for _, output := range report.Outputs {
		fmt.Fprintf(opts.Stdout, "output %s\n", output)
	}
	fmt.Fprintf(opts.Stdout, "clock %d -> %d, seed %d\n",
		report.ClockStart, report.ClockEnd, report.RandomSeed)
	return nil
}

func runDev(ctx context.Context, args []string, opts Options) error {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	app := fs.String("app", "", "app binary (default: NATCATTY_APP or natcatty on PATH)")
	positionals, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(positionals[0])
	if err != nil {
		return err
	}
	report, err := conformance.Check(ctx, conformance.Options{Directory: dir})
	if err != nil {
		return err
	}
	if !report.OK() {
		reportConformance(opts.Stderr, report)
		return conformanceError(report)
	}
	binary := *app
	if binary == "" {
		binary = os.Getenv("NATCATTY_APP")
	}
	if binary == "" {
		binary = "natcatty"
	}
	fmt.Fprintf(opts.Stdout, "launching %s with NATCATTY_PLUGIN_DIR=%s\n", binary, dir)
	return opts.Launcher(ctx, binary, dir, opts.Stdout, opts.Stderr)
}

type jsonReport struct {
	PluginID   string    `json:"plugin"`
	Logs       []jsonLog `json:"logs,omitempty"`
	Outputs    []string  `json:"outputs,omitempty"`
	Events     []string  `json:"events,omitempty"`
	ClockStart int64     `json:"clock_start_ns"`
	ClockEnd   int64     `json:"clock_end_ns"`
	Seed       uint64    `json:"seed"`
	ABI        int       `json:"abi"`
}

type jsonLog struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func jsonReportFrom(report fakehost.Report) jsonReport {
	out := jsonReport{
		PluginID:   report.PluginID,
		Outputs:    report.Outputs,
		ClockStart: report.ClockStart,
		ClockEnd:   report.ClockEnd,
		Seed:       report.RandomSeed,
		ABI:        report.ManifestABI,
	}
	for _, entry := range report.Logs {
		out.Logs = append(out.Logs, jsonLog{Level: entry.Level.String(), Message: entry.Message})
	}
	for _, event := range report.Events {
		out.Events = append(out.Events, event.Command)
	}
	return out
}

// ErrNoManifest reports a directory that is not a plugin.
var ErrNoManifest = errors.New("natcatty-plugin: no plugin.toml in the directory")
