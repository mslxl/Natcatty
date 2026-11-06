// SPDX-License-Identifier: GPL-3.0-or-later

// Command moshparity is the mosh parity gate. It starts real stock mosh-server
// cells from the pinned matrix, runs the required behavior suites against
// them, and writes one pass/fail report per cell. `verify` fails closed for
// ticket 34's default-switch decision.
//
// Typical use:
//
//	go run ./internal/tools/moshparity list
//	go run ./internal/tools/moshparity run -cell debian-12/1.4.0 -build
//	go run ./internal/tools/moshparity run -all -out build/mosh-parity
//	go run ./internal/tools/moshparity verify -dir build/mosh-parity
//
// CI runs one `run -cell` job per matrix entry and a final `verify` job over
// the downloaded reports. There is no waiver or partial-green switch: a
// missing report, a failed behavior, or a version mismatch fails `verify`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	os.Exit(runMain(os.Args[1:]))
}

// runMain maps a command result to the process exit status. Tests assert this
// directly instead of spawning a subprocess.
func runMain(args []string) int {
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "moshparity:", err)
		return 1
	}
	return 0
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
	case "verify":
		return verifyCommand(args[1:])
	case "report":
		return reportCommand(args[1:])
	case "fuzz":
		return fuzzCommand(args[1:])
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText)
	}
}

const usageText = `usage: moshparity <command> [flags]

commands:
  list      print the required matrix
  run       run one or more cells and write a report per cell
  verify    fail closed unless every required cell has a green report
  report    print the summary of a report directory
  fuzz      run the bounded native fuzz targets once

run flags:
  -cell ID         matrix cell to run (repeatable)
  -all             run every matrix cell
  -out DIR         report directory (default build/mosh-parity)
  -commit SHA      gate commit for the reports; defaults to
                   $NATCATTY_MOSH_PARITY_COMMIT, then git HEAD
  -build           rebuild docker images even when they exist
  -keep            leave containers running (debugging)
  -verbose         print every behavior and full suite output
  -timeout DUR     per-suite timeout (default 20m)
  -fuzztime DUR    bounded fuzz run per target (default 5s, minimum 5s)
  -lab-addr ADDR   override the SSH address for a single native cell

verify flags:
  -dir DIR         report directory (default build/mosh-parity)
  -commit SHA      commit the reports must carry; defaults to
                   $NATCATTY_MOSH_PARITY_COMMIT, then git HEAD; must
                   resolve to a non-empty commit
  -require-summary require the summary.json written by an earlier gate run and
                   fail if it is missing, stale, or bound to another commit
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
	if err := validateMatrix(Matrix); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%-24s %-14s %-8s %-10s %s\n", "CELL", "PLATFORM", "SERVER", "KIND", "ENDPOINT")
	for _, cell := range Matrix {
		fmt.Fprintf(os.Stdout, "%-24s %-14s %-8s %-10s %s\n",
			cell.ID, cell.Platform, cell.Version, cell.Kind, cell.SSHAddr)
	}
	return nil
}

type cellList []string

func (c *cellList) String() string { return fmt.Sprint([]string(*c)) }
func (c *cellList) Set(value string) error {
	*c = append(*c, value)
	return nil
}

func resolveCellSelection(ids cellList, all bool) ([]Cell, error) {
	if all && len(ids) > 0 {
		return nil, fmt.Errorf("-all and -cell are mutually exclusive")
	}
	if all {
		return Matrix, nil
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("select at least one -cell or pass -all")
	}
	var cells []Cell
	for _, id := range ids {
		cell, ok := findCell(id)
		if !ok {
			return nil, fmt.Errorf("unknown cell %q", id)
		}
		cells = append(cells, cell)
	}
	return cells, nil
}

func runCommand(args []string) error {
	flags := newFlagSet("run")
	var cellIDs cellList
	flags.Var(&cellIDs, "cell", "matrix cell ID (repeatable)")
	all := flags.Bool("all", false, "run every matrix cell")
	out := flags.String("out", filepath.Join("build", "mosh-parity"), "report directory")
	keep := flags.Bool("keep", false, "leave containers running")
	verbose := flags.Bool("verbose", false, "print every behavior and suite output")
	build := flags.Bool("build", false, "rebuild docker images even when present")
	timeout := flags.Duration("timeout", 20*time.Minute, "per-suite timeout")
	fuzzTime := flags.Duration("fuzztime", 5*time.Second, "bounded fuzz run per target")
	labAddr := flags.String("lab-addr", "", "override the SSH address of one native cell")
	commitFlag := flags.String("commit", "", "gate commit (default: $NATCATTY_MOSH_PARITY_COMMIT or git HEAD)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateMatrix(Matrix); err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return err
	}
	commit, err := gitCommit(root, *commitFlag)
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
		if !cells[0].IsNative() {
			return fmt.Errorf("-lab-addr is only valid for native cells; %s is %s", cells[0].ID, cells[0].Kind)
		}
	}
	if *fuzzTime < minReportFuzzTime {
		for _, cell := range cells {
			if cell.Kind != kindStock {
				return fmt.Errorf("-fuzztime must be at least %s for server cells; the gate has no fuzz waiver", minReportFuzzTime)
			}
		}
	}
	if *fuzzTime > maxReportFuzzTime {
		for _, cell := range cells {
			if cell.Kind != kindStock {
				return fmt.Errorf("-fuzztime must be at most %s for server cells; verify rejects implausible durations", maxReportFuzzTime)
			}
		}
	}
	opts := runOptions{
		root:     root,
		labDir:   filepath.Join(root, "adapters", "mosh", "lab"),
		outDir:   *out,
		commit:   commit,
		keep:     *keep,
		verbose:  *verbose,
		build:    *build,
		timeout:  *timeout,
		labAddr:  *labAddr,
		fuzzTime: *fuzzTime,
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
	dir := flags.String("dir", filepath.Join("build", "mosh-parity"), "report directory")
	commitFlag := flags.String("commit", "", "expected commit (default: $NATCATTY_MOSH_PARITY_COMMIT or git HEAD)")
	requireSummary := flags.Bool("require-summary", false,
		"require summary.json from an earlier gate run and fail if it is missing or mismatched")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateMatrix(Matrix); err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*dir) {
		*dir = filepath.Join(root, *dir)
	}
	expectedCommit, err := gitCommit(root, *commitFlag)
	if err != nil {
		// Still leave a machine-readable fail verdict: the workflow uploads
		// summary.json even when verification cannot start.
		if summaryErr := writeSummary(*dir, buildSummary(Matrix, nil, err)); summaryErr != nil {
			return fmt.Errorf("mosh parity gate FAILED: %w (also could not write summary: %v)", err, summaryErr)
		}
		return fmt.Errorf("mosh parity gate FAILED: %w", err)
	}
	reports, err := loadReports(*dir)
	if err != nil {
		if summaryErr := writeSummary(*dir, buildSummary(Matrix, nil, err)); summaryErr != nil {
			return fmt.Errorf("mosh parity gate FAILED: cannot read reports: %w (also could not write summary: %v)", err, summaryErr)
		}
		return fmt.Errorf("mosh parity gate FAILED: cannot read reports: %w", err)
	}
	printSummary(os.Stdout, Matrix, reports)
	verifyErr := verifyReports(Matrix, reports, expectedCommit)
	if *requireSummary {
		// The release evidence job runs this against the artifacts produced by
		// the reusable gate workflow. A missing or mismatched summary is a gate
		// failure, not an optional report.
		verifyErr = errors.Join(verifyErr, requireSummaryFile(*dir, expectedCommit))
	}
	if err := writeSummary(*dir, buildSummary(Matrix, reports, verifyErr)); err != nil {
		return fmt.Errorf("mosh parity gate FAILED: write summary: %w", err)
	}
	if verifyErr != nil {
		return fmt.Errorf("mosh parity gate FAILED:\n%w", verifyErr)
	}
	fmt.Println("\nmosh parity gate PASSED: every required cell and behavior is green")
	return nil
}

func reportCommand(args []string) error {
	flags := newFlagSet("report")
	dir := flags.String("dir", filepath.Join("build", "mosh-parity"), "report directory")
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
	printSummary(os.Stdout, Matrix, reports)
	return nil
}

func fuzzCommand(args []string) error {
	flags := newFlagSet("fuzz")
	fuzzTime := flags.Duration("fuzztime", 30*time.Second, "bounded fuzz run per target")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *fuzzTime <= 0 {
		return fmt.Errorf("-fuzztime must be positive")
	}
	root, err := findRoot()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	suite, err := runFuzzSuite(ctx, runOptions{root: root, fuzzTime: *fuzzTime})
	if suite.Output != "" {
		fmt.Fprintln(os.Stdout, suite.Output)
	}
	if err != nil {
		return err
	}
	if suite.Status != "pass" {
		return fmt.Errorf("fuzz suite failed")
	}
	fmt.Printf("fuzz: %d/%d targets passed in %s\n", len(suite.Tests), len(fuzzTargets), suite.Elapsed.Round(time.Second))
	return nil
}
