// Command etparity is the ET parity gate. It starts real stock etserver cells
// from the pinned matrix, runs the required behavior suites against them, and
// writes one pass/fail report per cell. `fuzz` runs the required bounded fuzz
// targets and writes one receipt per target. `verify` fails closed for ticket
// 34's default-switch decision.
//
// Typical use:
//
//	go run ./internal/tools/etparity list
//	go run ./internal/tools/etparity run -cell debian-12/7.1.1
//	go run ./internal/tools/etparity run -all -out build/et-parity
//	go run ./internal/tools/etparity fuzz -target ./adapters/et/internal/etgo/wire:FuzzReadFrame -out build/et-parity -fuzztime 30s
//	go run ./internal/tools/etparity verify -dir build/et-parity
//
// CI runs one `run -cell` job per matrix entry and a bounded fuzz job that
// produces receipts, then a final `verify` job over the merged artifacts.
// There is no waiver or partial-green switch: a missing report or receipt, a
// report or receipt from another commit, a failed behavior, a version
// mismatch, or a skipped fuzz step fails `verify`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "etparity:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "list":
		return listCommand(args[1:])
	case "run":
		return runCommand(args[1:])
	case "fuzz":
		return fuzzCommand(args[1:])
	case "verify":
		return verifyCommand(args[1:])
	case "report":
		return reportCommand(args[1:])
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText)
	}
}

const usageText = `usage: etparity <command> [flags]

commands:
  list      print the required matrix
  run       run one or more cells and write a report per cell
  fuzz      run required bounded fuzz targets and write a receipt per target
  verify    fail closed unless every required cell and fuzz target is green
  report    print the summary of a report directory

run flags:
  -cell ID         matrix cell to run (repeatable)
  -all             run every matrix cell
  -out DIR         report directory (default build/et-parity)
  -compose FILE    compose file (default adapters/et/lab/compose.yaml)
  -keep            leave containers running (debugging)
  -verbose         print every behavior and full suite output
  -timeout DUR     per-suite timeout (default 20m)
  -lab-addr ADDR   loopback SSH address override for a single native cell;
                   rejected for Docker cells

fuzz flags:
  -target PKG:NAME required fuzz target to run (repeatable; must be in the
                   compiled-in requiredFuzzTargets table)
  -out DIR         receipt directory (default build/et-parity)
  -fuzztime DUR    bounded fuzzing time per target (default 30s; minimum 1s)
  -timeout DUR     per-target go test timeout (default 10m)

verify flags:
  -dir DIR         report and receipt directory (default build/et-parity)
  -require-summary require the summary.json written by an earlier gate run and
                   fail if it is missing, stale, or bound to another
                   commit/tree/run
  -job-result NAME=RESULT
                   upstream job result (repeatable); any result other than
                   success fails the gate and is recorded in summary.json
`

func usage() error {
	fmt.Fprint(os.Stderr, usageText)
	return flag.ErrHelp
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	return flags
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func listCommand(args []string) error {
	flags := newFlagSet("list")
	if err := flags.Parse(args); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%-24s %-14s %-8s %-14s %-8s %s\n", "CELL", "PLATFORM", "SERVER", "HOST", "KIND", "LAB ADDRESS")
	for _, cell := range Matrix {
		kind := "docker"
		if cell.Native {
			kind = "native"
		}
		fmt.Fprintf(os.Stdout, "%-24s %-14s %-8s %-14s %-8s %s\n",
			cell.ID, cell.Platform, cell.Version, cell.GOOS+"/"+cell.GOARCH, kind, cell.SSHAddr)
	}
	return nil
}

func runCommand(args []string) error {
	flags := newFlagSet("run")
	var cellIDs cellList
	flags.Var(&cellIDs, "cell", "matrix cell ID (repeatable)")
	all := flags.Bool("all", false, "run every matrix cell")
	out := flags.String("out", filepath.Join("build", "et-parity"), "report directory")
	composeFile := flags.String("compose", filepath.Join("adapters", "et", "lab", "compose.yaml"), "compose file")
	keep := flags.Bool("keep", false, "leave containers running")
	verbose := flags.Bool("verbose", false, "print every behavior and suite output")
	timeout := flags.Duration("timeout", 20*time.Minute, "per-suite timeout")
	labAddr := flags.String("lab-addr", "", "override the SSH address of one native cell")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return err
	}
	cells, err := resolveCellSelection(cellIDs, *all)
	if err != nil {
		return err
	}
	if *labAddr != "" {
		if len(cells) != 1 {
			return fmt.Errorf("-lab-addr needs exactly one -cell")
		}
		if err := validateLabOverride(cells[0], *labAddr); err != nil {
			return err
		}
	}
	opts := runOptions{
		composeFile: *composeFile,
		outDir:      *out,
		keep:        *keep,
		verbose:     *verbose,
		timeout:     *timeout,
		labAddr:     *labAddr,
		root:        root,
	}
	if !filepath.IsAbs(opts.composeFile) {
		opts.composeFile = filepath.Join(root, opts.composeFile)
	}
	if !filepath.IsAbs(opts.outDir) {
		opts.outDir = filepath.Join(root, opts.outDir)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	failed := false
	for _, cell := range cells {
		if _, err := runCell(ctx, cell, opts); err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "%s: %v\n", cell.ID, err)
		}
	}
	if failed {
		return fmt.Errorf("one or more cells failed; reports are in %s", opts.outDir)
	}
	fmt.Printf("\nall %d selected cells passed; reports are in %s\n", len(cells), opts.outDir)
	return nil
}

func verifyCommand(args []string) error {
	flags := newFlagSet("verify")
	dir := flags.String("dir", filepath.Join("build", "et-parity"), "report directory")
	requireSummary := flags.Bool("require-summary", false,
		"require summary.json from an earlier gate run and fail if it is missing or mismatched")
	var jobResults jobResultList
	flags.Var(&jobResults, "job-result", "upstream job result as NAME=RESULT (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		gateErr := fmt.Errorf("ET parity gate FAILED: %w", err)
		summaryErr := writeSummary(*dir, buildSummary("", nil, nil,
			gateOptions{RunID: strings.TrimSpace(os.Getenv("GITHUB_RUN_ID"))}, gateErr))
		if summaryErr != nil {
			return fmt.Errorf("%w (also could not write summary: %v)", gateErr, summaryErr)
		}
		return gateErr
	}
	if !filepath.IsAbs(*dir) {
		*dir = filepath.Join(root, *dir)
	}
	opts := gateOptions{
		Commit:   gitCommit(root),
		TreeHash: gitTreeHash(root),
		// GitHub Actions sets GITHUB_RUN_ID for every job in a run. Binding
		// the reports and fuzz receipts to it rejects artifacts copied from
		// an earlier run even when they name the same commit, and an artifact
		// that claims a run ID can only be verified by a verifier in that run.
		RunID: strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")),
	}
	reports, err := loadReports(*dir)
	if err != nil {
		return finishVerify(*dir, root, nil, nil, opts,
			fmt.Errorf("ET parity gate FAILED: cannot read reports: %w", err))
	}
	fuzzReceipts, err := loadFuzzReceipts(*dir)
	if err != nil {
		return finishVerify(*dir, root, reports, nil, opts,
			fmt.Errorf("ET parity gate FAILED: cannot read fuzz receipts: %w", err))
	}
	printSummary(os.Stdout, reports)
	printFuzzSummary(os.Stdout, fuzzReceipts)
	// Upstream job results are part of the gate even when the artifacts they
	// uploaded happen to be complete. Recording them here keeps the summary
	// truthful when a job failed after producing (or while uploading) evidence.
	verifyErr := errors.Join(verifyGate(root, reports, fuzzReceipts, opts), verifyJobResults(jobResults))
	if *requireSummary {
		// The release evidence job runs this against the artifacts produced by
		// the reusable gate workflows. Missing or mismatched summaries are a
		// gate failure, not an optional report.
		verifyErr = errors.Join(verifyErr, requireSummaryFile(*dir, opts))
	}
	if err := finishVerify(*dir, root, reports, fuzzReceipts, opts, verifyErr); err != nil {
		return err
	}
	fmt.Printf("\nET parity gate PASSED: every required cell, behavior, and fuzz target is green for %s (tree %s)\n",
		opts.Commit, opts.TreeHash)
	return nil
}

// finishVerify writes the machine-readable verdict for both pass and fail and
// returns the gate error. Writing the summary is part of the gate: a failing
// gate that cannot leave evidence is also a failure.
func finishVerify(dir, root string, reports map[string]CellReport, receipts []fuzzReceiptFile, opts gateOptions, verifyErr error) error {
	if err := writeSummary(dir, buildSummary(root, reports, receipts, opts, verifyErr)); err != nil {
		if verifyErr != nil {
			return fmt.Errorf("%w (also could not write summary: %v)", verifyErr, err)
		}
		return fmt.Errorf("ET parity gate FAILED: write summary: %w", err)
	}
	return verifyErr
}

func reportCommand(args []string) error {
	flags := newFlagSet("report")
	dir := flags.String("dir", filepath.Join("build", "et-parity"), "report directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*dir) {
		root, err := findRoot()
		if err != nil {
			return err
		}
		*dir = filepath.Join(root, *dir)
	}
	reports, err := loadReports(*dir)
	if err != nil {
		return err
	}
	printSummary(os.Stdout, reports)
	return nil
}

// cellList is a repeatable -cell flag.
type cellList []string

func (l *cellList) String() string { return fmt.Sprint([]string(*l)) }

func (l *cellList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// jobResultList is a repeatable -job-result NAME=RESULT flag. The workflow
// passes the needs.<job>.result values of the cell and fuzz jobs, so a job
// that failed after uploading a green artifact still fails the gate and is
// recorded in summary.json.
type jobResultList []string

func (l *jobResultList) String() string { return fmt.Sprint([]string(*l)) }

func (l *jobResultList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

var allowedJobResults = map[string]bool{
	"success": true, "failure": true, "cancelled": true, "skipped": true,
}

// verifyJobResults fails the gate for any upstream result other than success.
// An unknown or malformed result fails closed rather than being ignored.
func verifyJobResults(results jobResultList) error {
	var problems []string
	seen := make(map[string]bool, len(results))
	for _, spec := range results {
		name, result, ok := strings.Cut(strings.TrimSpace(spec), "=")
		name, result = strings.TrimSpace(name), strings.TrimSpace(result)
		if !ok || name == "" || result == "" {
			problems = append(problems, fmt.Sprintf("job result %q must be NAME=RESULT", spec))
			continue
		}
		if seen[name] {
			problems = append(problems, fmt.Sprintf("job result for %s appears more than once", name))
			continue
		}
		seen[name] = true
		if !allowedJobResults[result] {
			problems = append(problems, fmt.Sprintf("required job %s has invalid result %q", name, result))
			continue
		}
		if result != "success" {
			problems = append(problems, fmt.Sprintf("required job %s result %q", name, result))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("ET parity gate FAILED:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}
