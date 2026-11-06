// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mslxl/natcatty/adapters/bundle"
)

// reportSchema versions the report contract. Schema 2 binds every report to a
// non-empty commit and records host and lab platform provenance; schema 1
// reports cannot prove either and are rejected by verify.
const reportSchema = 2

// minReportFuzzTime is the least fuzz time verify accepts per target. CI runs
// 10s per target and local evidence runs at least 5s; a shorter run is not a
// gate run.
const minReportFuzzTime = 5 * time.Second

// maxReportFuzzTime is the most fuzz time verify accepts per target. Gate runs
// use seconds, not hours; rejecting implausible values keeps a forged report
// from overflowing the elapsed-time product below into a negative bound.
const maxReportFuzzTime = time.Hour

// CellReport is the machine-readable result for one matrix cell. `verify`
// reads these files and fails closed: a missing file, a non-passing status, a
// version, identity, commit, or platform mismatch, or any required behavior
// that is not pass blocks the gate.
type CellReport struct {
	Schema        int       `json:"schema"`
	Cell          Cell      `json:"cell"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Commit        string    `json:"commit"`
	GoVersion     string    `json:"goVersion"`
	ServerVersion string    `json:"serverVersion"`
	// HostOS, HostArch, and HostUname describe the harness that produced the
	// report (runtime.GOOS/GOARCH and `uname -s`, with the FreeBSD sysctl
	// fallback). LabOS and LabArch describe the exercised endpoint: the
	// Docker image platform for Docker/stock cells and the native host for
	// macOS/FreeBSD cells. Verify requires the lab platform to match the cell
	// kind and, for native cells, the host fields to match too, so a report
	// cannot be relabelled by pointing -lab-addr at an unrelated host.
	HostOS     string        `json:"hostOS"`
	HostArch   string        `json:"hostArch"`
	HostUname  string        `json:"hostUname,omitempty"`
	LabOS      string        `json:"labOS"`
	LabArch    string        `json:"labArch,omitempty"`
	LabAddress string        `json:"labAddress"`
	FuzzTime   string        `json:"fuzzTime,omitempty"`
	Status     string        `json:"status"`
	Suites     []SuiteResult `json:"suites"`
	// Behaviors is a derived summary for reporting. Verify never trusts it:
	// it recomputes every behavior from Suites (the raw `go test -json`
	// results), so zeroing or editing Behaviors alone cannot turn the gate
	// green.
	Behaviors []BehaviorResult `json:"behaviors"`
}

// SuiteResult is one `go test -json` invocation or one fuzz run.
type SuiteResult struct {
	Name     string        `json:"name"`
	Packages []string      `json:"packages"`
	Status   string        `json:"status"`
	Elapsed  time.Duration `json:"elapsed"`
	Output   string        `json:"output,omitempty"`
	Tests    []TestResult  `json:"tests"`
}

// TestResult is the final result of one test (including a subtest).
type TestResult struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	Action  string  `json:"action"`
	Elapsed float64 `json:"elapsed,omitempty"`
}

// BehaviorResult is one required behavior's gate outcome.
type BehaviorResult struct {
	ID     string        `json:"id"`
	Title  string        `json:"title"`
	Status string        `json:"status"`
	Checks []CheckResult `json:"checks"`
}

// CheckResult is one required test reference's outcome.
type CheckResult struct {
	Suite   string `json:"suite"`
	Package string `json:"package"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Test    string `json:"test,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

// parseGoTestJSON extracts final test actions from `go test -json`. Compile
// failures and other package-level failures have no Test field; callers use
// the process exit status for those.
func parseGoTestJSON(r io.Reader) ([]TestResult, error) {
	type key struct{ pkg, test string }
	actions := make(map[key]TestResult)
	var order []key
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue // non-JSON line from a tool or a panicking test
		}
		if event.Test == "" {
			continue
		}
		if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
			continue
		}
		k := key{event.Package, event.Test}
		if _, seen := actions[k]; !seen {
			order = append(order, k)
		}
		actions[k] = TestResult{
			Package: event.Package, Name: event.Test,
			Action: event.Action, Elapsed: event.Elapsed,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	results := make([]TestResult, 0, len(order))
	for _, k := range order {
		results = append(results, actions[k])
	}
	return results, nil
}

// evaluateCell maps each required behavior for the cell to test results. A
// reference must have at least one matching test whose final action is pass; a
// matching skip or fail is a failure, never a waiver. The one exception is a
// reference the cell's harness cannot run at all: a bundle-dependent test on a
// host adapters/bundle has no target for is recorded as skipped and does not
// fail the behavior.
func evaluateCell(report CellReport) []BehaviorResult {
	cell := report.Cell
	suites := report.Suites
	bySuite := make(map[string]map[string]TestResult)
	for _, suite := range suites {
		index := make(map[string]TestResult)
		for _, test := range suite.Tests {
			index[test.Package+"\x00"+test.Name] = test
		}
		bySuite[suite.Name] = index
	}
	var results []BehaviorResult
	for _, behavior := range BehaviorsForCell(cell) {
		result := BehaviorResult{ID: behavior.ID, Title: behavior.Title, Status: "pass"}
		for _, ref := range RefsForVersion(behavior, cell.Version) {
			if ref.RequiresBundleTarget && !hostHasBundleTarget(cell, report.HostArch) {
				result.Checks = append(result.Checks, CheckResult{
					Suite: ref.Suite, Package: ref.Package, Name: ref.Name,
					Status: "skip",
					Detail: "the cell host has no bundled mosh-client target",
				})
				continue
			}
			check := evaluateRef(ref, bySuite)
			result.Checks = append(result.Checks, check)
			if check.Status != "pass" {
				result.Status = "fail"
			}
		}
		if len(result.Checks) == 0 {
			result.Status = "fail"
			result.Checks = append(result.Checks, CheckResult{
				Status: "fail", Detail: "behavior has no applicable test reference",
			})
		}
		results = append(results, result)
	}
	return results
}

// hostHasBundleTarget reports whether the harness host can resolve a bundled
// mosh-client. Docker and stock cells run the unit suite on Linux; native
// cells run it on the lab OS. adapters/bundle ships Linux and macOS targets
// only, so the FreeBSD cells cannot run bundle-dependent unit tests.
func hostHasBundleTarget(cell Cell, hostArch string) bool {
	_, ok := bundle.TargetKey(cell.ExpectedLabOS(), hostArch)
	return ok
}

func evaluateRef(ref TestRef, bySuite map[string]map[string]TestResult) CheckResult {
	check := CheckResult{Suite: ref.Suite, Package: ref.Package, Name: ref.Name, Status: "fail"}
	index, ok := bySuite[ref.Suite]
	if !ok {
		check.Detail = "suite did not run"
		return check
	}
	pattern, err := regexp.Compile("^(?:" + ref.Name + ")$")
	if err != nil {
		check.Detail = "invalid test pattern: " + err.Error()
		return check
	}
	var matched []TestResult
	for key, test := range index {
		if !strings.HasPrefix(key, ref.Package+"\x00") {
			continue
		}
		if pattern.MatchString(test.Name) {
			matched = append(matched, test)
		}
	}
	if len(matched) == 0 {
		check.Detail = "test did not run"
		return check
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	// A passing subtest under a failing parent, or any failed match, must fail
	// the check: the behavior proved only part of itself.
	failed := false
	passed := false
	for _, test := range matched {
		switch test.Action {
		case "pass":
			passed = true
		case "fail":
			failed = true
		}
	}
	if failed {
		check.Detail = "test failed"
		return check
	}
	if !passed {
		check.Detail = "test skipped"
		return check
	}
	check.Status = "pass"
	check.Test = matched[0].Name
	return check
}

// reportFilename maps a cell ID to a stable file name.
func reportFilename(id string) string {
	replacer := strings.NewReplacer("/", "-", " ", "_")
	return replacer.Replace(id) + ".json"
}

func writeReport(dir string, report CellReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, reportFilename(report.Cell.ID)), data, 0o644)
}

// loadReports reads every cell report in dir, keyed by cell ID.
func loadReports(dir string) (map[string]CellReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	reports := make(map[string]CellReport)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "summary.json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var report CellReport
		if err := json.Unmarshal(data, &report); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if report.Cell.ID == "" {
			return nil, fmt.Errorf("%s: report has no cell ID", path)
		}
		if _, duplicate := reports[report.Cell.ID]; duplicate {
			return nil, fmt.Errorf("%s: duplicate report for cell %s", path, report.Cell.ID)
		}
		reports[report.Cell.ID] = report
	}
	return reports, nil
}

// verifyReports fails closed. It recomputes every behavior from the recorded
// suite results rather than trusting the stored summary, so a stale or edited
// report cannot turn a failed suite green.
//
// The expected commit is mandatory: without it the gate cannot distinguish a
// current report from a stale one, so verify refuses to run rather than skip
// the comparison.
func verifyReports(cells []Cell, reports map[string]CellReport, expectedCommit string) error {
	if strings.TrimSpace(expectedCommit) == "" {
		return fmt.Errorf("cannot verify: the expected commit is empty; pass -commit or set %s", commitEnv)
	}
	var failures []string
	expected := make(map[string]Cell)
	for _, cell := range cells {
		expected[cell.ID] = cell
	}
	// Reports from different commits, or from a commit other than the one
	// being verified, cannot describe this tree. Fail closed rather than let a
	// stale green report authorize the default switch.
	reportCommits := make(map[string]bool)
	for id, report := range reports {
		if strings.TrimSpace(report.Commit) == "" {
			failures = append(failures, fmt.Sprintf("%s: report has no commit", id))
			continue
		}
		reportCommits[report.Commit] = true
		if report.Commit != expectedCommit {
			failures = append(failures, fmt.Sprintf("%s: report commit %s, verifying %s", id, report.Commit, expectedCommit))
		}
	}
	if len(reportCommits) > 1 {
		failures = append(failures, "reports come from different commits")
	}
	for id := range reports {
		if _, ok := expected[id]; !ok {
			failures = append(failures, fmt.Sprintf("%s: report does not match any required matrix cell", id))
		}
	}
	for _, cell := range cells {
		report, ok := reports[cell.ID]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: missing report", cell.ID))
			continue
		}
		if report.Schema != reportSchema {
			failures = append(failures, fmt.Sprintf("%s: report schema %d, want %d", cell.ID, report.Schema, reportSchema))
		}
		if got, want := cellIdentity(report.Cell), cellIdentity(cell); got != want {
			failures = append(failures, fmt.Sprintf("%s: report identity does not match the matrix entry", cell.ID))
		}
		if cell.Kind != kindStock && report.ServerVersion != cell.Version {
			failures = append(failures, fmt.Sprintf("%s: server version %q, want %q", cell.ID, report.ServerVersion, cell.Version))
		}
		failures = append(failures, verifyCellProvenance(cell, report)...)
		failures = append(failures, verifyCellFuzzTime(cell, report)...)
		if report.Status != "pass" {
			failures = append(failures, fmt.Sprintf("%s: cell status %q", cell.ID, report.Status))
		}
		required := suitesForCell(cell)
		for _, name := range required {
			found := false
			for _, suite := range report.Suites {
				if suite.Name == name {
					found = true
					if suite.Status != "pass" {
						failures = append(failures, fmt.Sprintf("%s: suite %s status %q", cell.ID, name, suite.Status))
					}
				}
			}
			if !found {
				failures = append(failures, fmt.Sprintf("%s: required suite %s did not run", cell.ID, name))
			}
		}
		behaviors := evaluateCell(report)
		if len(behaviors) == 0 {
			failures = append(failures, fmt.Sprintf("%s: no required behaviors evaluated", cell.ID))
		}
		for _, behavior := range behaviors {
			if behavior.Status != "pass" {
				var details []string
				for _, check := range behavior.Checks {
					if check.Status != "pass" {
						details = append(details, fmt.Sprintf("%s/%s (%s)", check.Suite, check.Name, check.Detail))
					}
				}
				failures = append(failures, fmt.Sprintf("%s: behavior %s failed: %s",
					cell.ID, behavior.ID, strings.Join(details, "; ")))
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "\n"))
	}
	return nil
}

// verifyCellProvenance binds a report to the platform the cell requires.
// Docker and stock cells must have been exercised through a Linux container
// image; macOS and FreeBSD cells must have been produced on their own host OS,
// with `uname -s` agreeing. Without this, `run -lab-addr` could point a
// macos/* or freebsd-14/* report at any host and still pass the identity
// check, which excludes the lab endpoint.
func verifyCellProvenance(cell Cell, report CellReport) []string {
	var failures []string
	wantOS := cell.ExpectedLabOS()
	if report.HostOS == "" {
		failures = append(failures, fmt.Sprintf("%s: report has no host OS provenance", cell.ID))
	}
	if report.HostArch == "" {
		failures = append(failures, fmt.Sprintf("%s: report has no host architecture", cell.ID))
	}
	if report.LabOS == "" {
		failures = append(failures, fmt.Sprintf("%s: report has no lab platform provenance", cell.ID))
	} else if !strings.EqualFold(report.LabOS, wantOS) {
		failures = append(failures, fmt.Sprintf("%s: lab platform %q, want %q", cell.ID, report.LabOS, wantOS))
	}
	if report.LabArch == "" {
		failures = append(failures, fmt.Sprintf("%s: report has no lab architecture", cell.ID))
	}
	// The stock cell has no SSH endpoint; every other cell must record the
	// endpoint it exercised. Docker cells always run at the published matrix
	// endpoint, so the address must match exactly; native cells may use a
	// documented -lab-addr override, so only non-emptiness is required.
	if cell.Kind != kindStock && report.LabAddress == "" {
		failures = append(failures, fmt.Sprintf("%s: report has no lab address", cell.ID))
	}
	if cell.Kind == kindDocker && report.LabAddress != cell.SSHAddr {
		failures = append(failures, fmt.Sprintf("%s: lab address %q, want %q", cell.ID, report.LabAddress, cell.SSHAddr))
	}
	if cell.IsNative() {
		if !strings.EqualFold(report.HostOS, wantOS) {
			failures = append(failures, fmt.Sprintf("%s: host OS %q, want %q", cell.ID, report.HostOS, wantOS))
		}
		if !strings.EqualFold(report.HostUname, wantOS) {
			failures = append(failures, fmt.Sprintf("%s: uname %q, want %q", cell.ID, report.HostUname, wantOS))
		}
	}
	return failures
}

// verifyCellFuzzTime enforces the minimum recorded bounded fuzz run. The fuzz
// suite must have actually run long enough for the gate to count it.
func verifyCellFuzzTime(cell Cell, report CellReport) []string {
	if cell.Kind == kindStock {
		return nil
	}
	if report.FuzzTime == "" {
		return []string{fmt.Sprintf("%s: report has no fuzz time", cell.ID)}
	}
	fuzzTime, err := time.ParseDuration(report.FuzzTime)
	if err != nil {
		return []string{fmt.Sprintf("%s: invalid fuzz time %q: %v", cell.ID, report.FuzzTime, err)}
	}
	if fuzzTime < minReportFuzzTime {
		return []string{fmt.Sprintf("%s: fuzz time %s, minimum %s", cell.ID, fuzzTime, minReportFuzzTime)}
	}
	if fuzzTime > maxReportFuzzTime {
		return []string{fmt.Sprintf("%s: fuzz time %s, maximum %s", cell.ID, fuzzTime, maxReportFuzzTime)}
	}
	// The recorded time is a claim; bind it to the recorded suite duration.
	// The fuzz suite runs one bounded target per fuzzTargets entry, so a real
	// run cannot have finished faster than fuzzTime * len(fuzzTargets). The
	// product is checked so a forged duration cannot wrap negative and make
	// every elapsed value look long enough.
	requiredElapsed, ok := fuzzElapsedBound(fuzzTime, len(fuzzTargets))
	if !ok {
		return []string{fmt.Sprintf("%s: cannot bound fuzz elapsed time for %d targets at %s each", cell.ID, len(fuzzTargets), fuzzTime)}
	}
	for _, suite := range report.Suites {
		if suite.Name != suiteFuzz {
			continue
		}
		if suite.Elapsed < requiredElapsed {
			return []string{fmt.Sprintf("%s: fuzz suite elapsed %s, want at least %s for %d targets at %s each",
				cell.ID, suite.Elapsed, requiredElapsed, len(fuzzTargets), fuzzTime)}
		}
		break
	}
	return nil
}

// fuzzElapsedBound returns the minimum elapsed time a fuzz suite must record
// for one bounded run of targets targets at fuzzTime each. It reports false for
// non-positive inputs and when the multiplication would overflow
// time.Duration, so the elapsed check cannot be satisfied by a wrapped value.
func fuzzElapsedBound(fuzzTime time.Duration, targets int) (time.Duration, bool) {
	if fuzzTime <= 0 || targets <= 0 {
		return 0, false
	}
	if int64(fuzzTime) > math.MaxInt64/int64(targets) {
		return 0, false
	}
	return fuzzTime * time.Duration(targets), true
}

// printSummary prints one line per required cell and details for failures.
func printSummary(w io.Writer, cells []Cell, reports map[string]CellReport) {
	fmt.Fprintf(w, "%-24s %-14s %-8s %-6s %s\n", "CELL", "PLATFORM", "SERVER", "STATUS", "BEHAVIORS")
	for _, cell := range cells {
		report, ok := reports[cell.ID]
		if !ok {
			fmt.Fprintf(w, "%-24s %-14s %-8s %-6s %s\n", cell.ID, cell.Platform, cell.Version, "FAIL", "missing report")
			continue
		}
		behaviors := evaluateCell(report)
		green, total := 0, len(behaviors)
		for _, behavior := range behaviors {
			if behavior.Status == "pass" {
				green++
			}
		}
		status := report.Status
		if status == "" {
			status = "fail"
		}
		fmt.Fprintf(w, "%-24s %-14s %-8s %-6s %d/%d\n",
			cell.ID, cell.Platform, cell.Version, strings.ToUpper(status), green, total)
		if strings.EqualFold(status, "pass") {
			continue
		}
		for _, behavior := range behaviors {
			if behavior.Status == "pass" {
				continue
			}
			fmt.Fprintf(w, "  %-24s %s\n", behavior.ID, behavior.Status)
			for _, check := range behavior.Checks {
				if check.Status == "pass" {
					continue
				}
				fmt.Fprintf(w, "    %-6s %s/%s: %s (%s)\n",
					check.Suite, check.Package, check.Name, check.Status, check.Detail)
			}
		}
		for _, suite := range report.Suites {
			if suite.Status == "pass" {
				continue
			}
			fmt.Fprintf(w, "  suite %s: %s in %s\n", suite.Name, suite.Status, suite.Elapsed.Round(time.Second))
			if suite.Output != "" {
				fmt.Fprintln(w, indent(suite.Output, "    "))
			}
		}
	}
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// summaryFile is the machine-readable roll-up ticket 34 consumes.
type summaryFile struct {
	Schema      int           `json:"schema"`
	Status      string        `json:"status"`
	GeneratedAt time.Time     `json:"generatedAt"`
	Commit      string        `json:"commit,omitempty"`
	Cells       []summaryCell `json:"cells"`
}

type summaryCell struct {
	ID              string `json:"id"`
	Platform        string `json:"platform"`
	Version         string `json:"version"`
	Status          string `json:"status"`
	BehaviorsPassed int    `json:"behaviorsPassed"`
	BehaviorsTotal  int    `json:"behaviorsTotal"`
}

func buildSummary(cells []Cell, reports map[string]CellReport, verifyErr error) summaryFile {
	summary := summaryFile{
		Schema: reportSchema, Status: "pass", GeneratedAt: time.Now().UTC(),
	}
	for _, cell := range cells {
		entry := summaryCell{ID: cell.ID, Platform: cell.Platform, Version: cell.Version, Status: "fail"}
		report, ok := reports[cell.ID]
		if ok {
			behaviors := evaluateCell(report)
			entry.BehaviorsTotal = len(behaviors)
			for _, behavior := range behaviors {
				if behavior.Status == "pass" {
					entry.BehaviorsPassed++
				}
			}
			entry.Status = report.Status
			if entry.Status == "" {
				entry.Status = "fail"
			}
			if summary.Commit == "" {
				summary.Commit = report.Commit
			}
		}
		if entry.Status != "pass" {
			summary.Status = "fail"
		}
		summary.Cells = append(summary.Cells, entry)
	}
	if verifyErr != nil {
		summary.Status = "fail"
	}
	return summary
}

func writeSummary(dir string, summary summaryFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644)
}

const (
	maxSummaryAge    = 24 * time.Hour
	summaryClockSkew = 10 * time.Minute
)

// requireSummaryFile is the release-side check: summary.json produced by the
// per-gate workflow must exist, be green, and belong to the same commit this
// verifier checked out. Artifacts are already run-scoped, so the commit and
// the verifier's own GITHUB_RUN_ID context bind the evidence to this release.
func requireSummaryFile(dir, expectedCommit string) error {
	path := filepath.Join(dir, "summary.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("mosh parity gate FAILED: required summary.json: %w", err)
	}
	var summary summaryFile
	if err := json.Unmarshal(data, &summary); err != nil {
		return fmt.Errorf("mosh parity gate FAILED: required summary.json: %w", err)
	}
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if summary.Schema != reportSchema {
		add("summary schema %d, want %d", summary.Schema, reportSchema)
	}
	if summary.Status != "pass" {
		add("summary status %q", summary.Status)
	}
	if summary.Commit != expectedCommit {
		add("summary commit %q, want %q", summary.Commit, expectedCommit)
	}
	if summary.GeneratedAt.IsZero() {
		add("summary has no generation time")
	} else {
		age := time.Since(summary.GeneratedAt.UTC())
		if age > maxSummaryAge {
			add("summary is stale (generated %s ago, limit %s)", age.Round(time.Second), maxSummaryAge)
		}
		if age < -summaryClockSkew {
			add("summary generation time is %s in the future", (-age).Round(time.Second))
		}
	}

	cells := make(map[string]summaryCell, len(summary.Cells))
	for _, cell := range summary.Cells {
		if _, ok := cells[cell.ID]; ok {
			add("summary has a duplicate cell %q", cell.ID)
		}
		cells[cell.ID] = cell
	}
	knownCells := make(map[string]bool, len(Matrix))
	for _, cell := range Matrix {
		knownCells[cell.ID] = true
	}
	for _, cell := range Matrix {
		entry, ok := cells[cell.ID]
		if !ok {
			add("%s: summary has no cell", cell.ID)
			continue
		}
		if entry.Status != "pass" {
			add("%s: summary cell status %q", cell.ID, entry.Status)
		}
		if entry.Platform != cell.Platform || entry.Version != cell.Version {
			add("%s: summary cell is %s/%s, want %s/%s",
				cell.ID, entry.Platform, entry.Version, cell.Platform, cell.Version)
		}
		if entry.BehaviorsTotal <= 0 {
			add("%s: summary has no behavior verdicts", cell.ID)
		} else if entry.BehaviorsPassed != entry.BehaviorsTotal {
			add("%s: summary has %d/%d behaviors passing",
				cell.ID, entry.BehaviorsPassed, entry.BehaviorsTotal)
		}
	}
	for id := range cells {
		if !knownCells[id] {
			add("%s: summary has an unexpected cell", id)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("mosh parity gate FAILED: required summary.json failed:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}
