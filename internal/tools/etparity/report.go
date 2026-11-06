package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// reportSchema is bumped whenever report semantics change. `verify` refuses
// reports from another schema instead of guessing at their meaning.
const reportSchema = 2

const (
	// defaultMaxReportAge bounds how old a green report may be. Reports are
	// bound to the checkout commit and tree, but a very old green report can
	// outlive a test machine, base image, or network condition. A day covers a
	// full CI run with retries. The window is fixed rather than a flag so it
	// cannot be widened into a waiver.
	defaultMaxReportAge = 24 * time.Hour
	// reportClockSkew tolerates a cell clock slightly ahead of the verifier.
	reportClockSkew = 10 * time.Minute
)

// CellReport is the machine-readable result for one matrix cell. `verify`
// reads these files and fails closed: a missing file, a non-passing status, a
// version mismatch, or any required behavior that is not pass blocks the gate.
//
// Status and Behaviors are self-reported and are therefore not trusted on
// their own: verify recomputes them from Suites and requires the serialized
// values to agree with the recomputation.
type CellReport struct {
	Schema        int              `json:"schema"`
	Cell          Cell             `json:"cell"`
	GeneratedAt   time.Time        `json:"generatedAt"`
	Commit        string           `json:"commit"`
	TreeHash      string           `json:"treeHash"`
	GoVersion     string           `json:"goVersion"`
	HostOS        string           `json:"hostOS"`
	HostArch      string           `json:"hostArch"`
	RunID         string           `json:"runID,omitempty"`
	RunnerName    string           `json:"runnerName,omitempty"`
	ServerVersion string           `json:"serverVersion"`
	LabAddress    string           `json:"labAddress"`
	Status        string           `json:"status"`
	Suites        []SuiteResult    `json:"suites"`
	Behaviors     []BehaviorResult `json:"behaviors"`
}

// SuiteResult is one `go test -json` invocation.
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

// evaluateCell maps each required behavior for the cell's version to test
// results. A reference must have at least one matching test whose final
// action is pass; a matching skip or fail is a failure, never a waiver.
func evaluateCell(cell Cell, suites []SuiteResult) []BehaviorResult {
	bySuite := make(map[string]map[string]TestResult)
	for _, suite := range suites {
		index := make(map[string]TestResult)
		for _, test := range suite.Tests {
			index[test.Package+"\x00"+test.Name] = test
		}
		bySuite[suite.Name] = index
	}
	var results []BehaviorResult
	for _, behavior := range BehaviorsForVersion(cell.Version) {
		result := BehaviorResult{ID: behavior.ID, Title: behavior.Title, Status: "pass"}
		for _, ref := range RefsForVersion(behavior, cell.Version) {
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

func sanitizeCellID(id string) string {
	replacer := strings.NewReplacer("/", "_", ":", "-", " ", "_")
	return replacer.Replace(id)
}

func reportPath(dir string, cell Cell) string {
	return filepath.Join(dir, sanitizeCellID(cell.ID)+".json")
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
	return os.WriteFile(reportPath(dir, report.Cell), data, 0o644)
}

func loadReports(dir string) (map[string]CellReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	reports := make(map[string]CellReport)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// Fuzz receipts and the gate summary share the merged artifact
		// directory. They are loaded and verified separately; treating one as
		// a cell report would both fail the cell schema and hide
		// missing/hand-edited evidence from the other verifiers.
		if strings.HasPrefix(entry.Name(), "fuzz-") || entry.Name() == "summary.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var report CellReport
		if err := json.Unmarshal(data, &report); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if report.Schema != reportSchema {
			return nil, fmt.Errorf("%s: unsupported report schema %d", entry.Name(), report.Schema)
		}
		if report.Cell.ID == "" {
			return nil, fmt.Errorf("%s: report has no cell ID", entry.Name())
		}
		if previous, ok := reports[report.Cell.ID]; ok {
			return nil, fmt.Errorf("%s: duplicate report for cell %q (previous generated at %s)",
				entry.Name(), report.Cell.ID, previous.GeneratedAt.Format(time.RFC3339))
		}
		reports[report.Cell.ID] = report
	}
	return reports, nil
}

// gateOptions binds a verification to the exact checkout being verified.
type gateOptions struct {
	// Commit and TreeHash identify the checkout the reports must come from.
	Commit   string
	TreeHash string
	// RunID is the CI run the reports are bound to. Empty outside CI; a
	// report carrying a run ID is rejected when verification is not itself
	// bound to that run.
	RunID string
	// MaxAge bounds report age; zero means defaultMaxReportAge.
	MaxAge time.Duration
	// Now is injectable for tests; zero means the current UTC time.
	Now time.Time
}

func (o gateOptions) normalized() gateOptions {
	if o.MaxAge <= 0 {
		o.MaxAge = defaultMaxReportAge
	}
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	} else {
		o.Now = o.Now.UTC()
	}
	return o
}

// verifyReports is the fail-closed gate consumed by ticket 34. It requires a
// passing, fresh, run-bound report for every matrix cell, the exact checkout
// commit and tree in each report, the exact pinned server version, the
// expected harness platform, and every required behavior green.
//
// The serialized Status and Behaviors are never trusted: verify recomputes
// them from the recorded suites and requires the report to agree exactly. A
// hand-written "all pass" report with no suites, a failed check marked pass,
// or a behavior set with a deleted behavior all fail.
func verifyReports(cells []Cell, reports map[string]CellReport, opts gateOptions) error {
	opts = opts.normalized()
	if opts.Commit == "" || opts.Commit == "unknown" {
		return errors.New("ET parity gate FAILED: the checkout commit is unknown")
	}
	if opts.TreeHash == "" || opts.TreeHash == "unknown" {
		return errors.New("ET parity gate FAILED: the checkout tree is unknown")
	}
	var problems []string
	known := make(map[string]bool, len(cells))
	for _, cell := range cells {
		known[cell.ID] = true
	}
	for id := range reports {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("%s: report does not match any matrix cell", id))
		}
	}
	for _, cell := range cells {
		report, ok := reports[cell.ID]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no report", cell.ID))
			continue
		}
		problems = append(problems, verifyCell(cell, report, opts)...)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("ET parity gate FAILED:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}

// verifyGate runs both halves of the fail-closed gate. Cell reports and fuzz
// receipts are separate artifact sets, but neither is optional: a green cell
// matrix with no fuzz receipts still fails. This is what makes workflow edits
// that remove, comment out, or skip a fuzz step fail closed, because no
// receipt is produced for verify to recompute.
//
// Both halves always run so a failed cell job cannot hide a missing or failed
// fuzz artifact (and vice versa). The gate fails if either half has a problem.
func verifyGate(root string, reports map[string]CellReport, receipts []fuzzReceiptFile, opts gateOptions) error {
	reportErr := verifyReports(Matrix, reports, opts)
	fuzzErr := verifyFuzzReceipts(root, requiredFuzzTargets, receipts, opts)
	switch {
	case reportErr != nil && fuzzErr != nil:
		return errors.Join(reportErr, fuzzErr)
	case reportErr != nil:
		return reportErr
	default:
		return fuzzErr
	}
}

// verifyCell returns every reason one cell's report is not acceptable.
func verifyCell(cell Cell, report CellReport, opts gateOptions) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if report.Schema != reportSchema {
		add("%s: report schema %d, want %d", cell.ID, report.Schema, reportSchema)
	}
	if report.Cell != cell {
		add("%s: report cell metadata %+v does not match matrix cell %+v", cell.ID, report.Cell, cell)
	}
	if report.Commit != opts.Commit {
		add("%s: report commit %q, want %q", cell.ID, report.Commit, opts.Commit)
	}
	if report.TreeHash != opts.TreeHash {
		add("%s: report tree %q, want %q", cell.ID, report.TreeHash, opts.TreeHash)
	}
	if report.GoVersion == "" {
		add("%s: report has no Go version", cell.ID)
	}
	if report.ServerVersion != cell.Version {
		add("%s: report server version %q, want %q", cell.ID, report.ServerVersion, cell.Version)
	}

	// Provenance: the harness process must have run on the platform the
	// matrix expects. A report generated on another OS/arch (or one that
	// simply omits provenance) cannot prove this cell.
	if report.HostOS == "" || report.HostArch == "" {
		add("%s: report has no host platform provenance (hostOS/hostArch)", cell.ID)
	} else if report.HostOS != cell.GOOS || report.HostArch != cell.GOARCH {
		add("%s: report host platform %s/%s, want %s/%s",
			cell.ID, report.HostOS, report.HostArch, cell.GOOS, cell.GOARCH)
	}
	if err := validateLoopbackAddress(report.LabAddress); err != nil {
		add("%s: reported lab address: %v", cell.ID, err)
	}
	if !cell.Native && report.LabAddress != cell.SSHAddr {
		add("%s: report lab address %q, want the pinned Docker address %q",
			cell.ID, report.LabAddress, cell.SSHAddr)
	}

	// Freshness and run binding: a matching commit alone can be satisfied by
	// an arbitrarily old report, so every report must be recent, and reports
	// from CI must belong to the run that is verifying them.
	if report.GeneratedAt.IsZero() {
		add("%s: report has no generation time", cell.ID)
	} else {
		age := opts.Now.Sub(report.GeneratedAt.UTC())
		if age > opts.MaxAge {
			add("%s: report is stale (generated %s ago, limit %s)",
				cell.ID, age.Round(time.Second), opts.MaxAge)
		}
		if age < -reportClockSkew {
			add("%s: report generation time is %s in the future", cell.ID, (-age).Round(time.Second))
		}
	}
	switch {
	case opts.RunID != "" && report.RunID == "":
		add("%s: report is not bound to CI run %q", cell.ID, opts.RunID)
	case opts.RunID != "" && report.RunID != opts.RunID:
		add("%s: report CI run %q, want %q", cell.ID, report.RunID, opts.RunID)
	case opts.RunID == "" && report.RunID != "":
		add("%s: report is bound to CI run %q but verify has no GITHUB_RUN_ID", cell.ID, report.RunID)
	}

	problems = append(problems, verifySuites(cell, report.Suites)...)

	// Recompute every behavior from the recorded test results. The report's
	// own status and behavior list are only compared against this result.
	recomputed := evaluateCell(cell, report.Suites)
	recomputedStatus := "pass"
	for _, behavior := range recomputed {
		if behavior.Status != "pass" {
			recomputedStatus = "fail"
			var failed []string
			for _, check := range behavior.Checks {
				if check.Status != "pass" {
					failed = append(failed, fmt.Sprintf("%s/%s (%s)", check.Package, check.Name, check.Detail))
				}
			}
			add("%s: recomputed behavior %q %s: %s",
				cell.ID, behavior.ID, behavior.Status, strings.Join(failed, "; "))
		}
	}
	if err := compareBehaviorIDs(cell.Version, behaviorIDs(recomputed)); err != nil {
		add("%s: recomputed %v", cell.ID, err)
	}
	if err := compareBehaviorIDs(cell.Version, behaviorIDs(report.Behaviors)); err != nil {
		add("%s: reported %v", cell.ID, err)
	}
	for _, diff := range behaviorDiff(recomputed, report.Behaviors) {
		add("%s: %s", cell.ID, diff)
	}
	for _, behavior := range report.Behaviors {
		if behavior.Status != "pass" {
			continue
		}
		if len(behavior.Checks) == 0 {
			add("%s: behavior %q reports pass with zero checks", cell.ID, behavior.ID)
		}
		for _, check := range behavior.Checks {
			if check.Status != "pass" {
				add("%s: behavior %q reports pass but check %s/%s is %q",
					cell.ID, behavior.ID, check.Package, check.Name, check.Status)
			}
			if check.Test == "" {
				add("%s: behavior %q reports a passing check without a test name", cell.ID, behavior.ID)
			}
		}
	}
	if report.Status != recomputedStatus {
		add("%s: self-reported status %q disagrees with recomputed status %q",
			cell.ID, report.Status, recomputedStatus)
	}
	return problems
}

// requiredSuiteNames are the only suites a cell may report, and all of them
// are required. requiredSuitePackages is the exact package selection each
// suite must name; a report cannot substitute narrower or unrelated tests.
var requiredSuiteNames = []string{suiteLab, suiteUnit}

var requiredSuitePackages = map[string]string{
	suiteLab:  "./adapters/et",
	suiteUnit: "./adapters/et/...",
}

// verifySuites requires each required suite exactly once, passing, with at
// least one recorded test result from the adapter package tree, a real package
// selection, and captured output. A suite's self-reported Status is checked
// here because it gates the recomputation; the behavior-level result is
// recomputed independently.
func verifySuites(cell Cell, suites []SuiteResult) []string {
	var problems []string
	seen := make(map[string]SuiteResult, len(suites))
	for _, suite := range suites {
		if _, ok := seen[suite.Name]; ok {
			problems = append(problems, fmt.Sprintf("%s: duplicate suite %q", cell.ID, suite.Name))
		}
		seen[suite.Name] = suite
	}
	for _, name := range requiredSuiteNames {
		suite, ok := seen[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: required suite %q did not run", cell.ID, name))
			continue
		}
		if suite.Status != "pass" {
			problems = append(problems, fmt.Sprintf("%s: suite %q status %q", cell.ID, name, suite.Status))
		}
		if want := requiredSuitePackages[name]; len(suite.Packages) != 1 || suite.Packages[0] != want {
			problems = append(problems, fmt.Sprintf("%s: suite %q packages %v, want [%s]",
				cell.ID, name, suite.Packages, want))
		}
		if len(suite.Tests) == 0 {
			problems = append(problems, fmt.Sprintf("%s: suite %q recorded no tests", cell.ID, name))
		}
		if strings.TrimSpace(suite.Output) == "" {
			problems = append(problems, fmt.Sprintf("%s: suite %q recorded no output", cell.ID, name))
		}
		for _, test := range suite.Tests {
			if test.Package != etPackage && !strings.HasPrefix(test.Package, etPackage+"/") {
				problems = append(problems, fmt.Sprintf("%s: suite %q test %s/%s is outside %s",
					cell.ID, name, test.Package, test.Name, etPackage))
			}
			switch test.Action {
			case "pass", "skip":
			case "fail":
				// A failing test anywhere in a required suite fails the cell,
				// even when no required behavior references it. Otherwise a
				// report could self-report suite status "pass" while carrying
				// a real failure.
				problems = append(problems, fmt.Sprintf("%s: suite %q has failing test %s/%s",
					cell.ID, name, test.Package, test.Name))
			default:
				problems = append(problems, fmt.Sprintf("%s: suite %q test %s/%s has invalid action %q",
					cell.ID, name, test.Package, test.Name, test.Action))
			}
		}
	}
	for name := range seen {
		if !slices.Contains(requiredSuiteNames, name) {
			problems = append(problems, fmt.Sprintf("%s: unexpected suite %q", cell.ID, name))
		}
	}
	return problems
}

// behaviorDiff compares the recomputed results (want) with the serialized
// report (got). Both direction and content matter: a report may not omit,
// add, reorder, or relabel a check.
func behaviorDiff(want, got []BehaviorResult) []string {
	wantByID := make(map[string]BehaviorResult, len(want))
	gotByID := make(map[string]BehaviorResult, len(got))
	var diffs []string
	for _, behavior := range want {
		if _, ok := wantByID[behavior.ID]; ok {
			diffs = append(diffs, fmt.Sprintf("recomputed behavior set duplicates %q", behavior.ID))
		}
		wantByID[behavior.ID] = behavior
	}
	for _, behavior := range got {
		if _, ok := gotByID[behavior.ID]; ok {
			diffs = append(diffs, fmt.Sprintf("reported behavior set duplicates %q", behavior.ID))
		}
		gotByID[behavior.ID] = behavior
	}
	for id, wantBehavior := range wantByID {
		gotBehavior, ok := gotByID[id]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("reported result is missing behavior %q", id))
			continue
		}
		if wantBehavior.Title != gotBehavior.Title {
			diffs = append(diffs, fmt.Sprintf("behavior %q title %q, recomputed %q",
				id, gotBehavior.Title, wantBehavior.Title))
		}
		if wantBehavior.Status != gotBehavior.Status {
			diffs = append(diffs, fmt.Sprintf("behavior %q status %q, recomputed %q",
				id, gotBehavior.Status, wantBehavior.Status))
		}
		if len(wantBehavior.Checks) != len(gotBehavior.Checks) {
			diffs = append(diffs, fmt.Sprintf("behavior %q reports %d checks, recomputed %d",
				id, len(gotBehavior.Checks), len(wantBehavior.Checks)))
			continue
		}
		for i := range wantBehavior.Checks {
			if wantBehavior.Checks[i] != gotBehavior.Checks[i] {
				diffs = append(diffs, fmt.Sprintf("behavior %q check %d %+v, recomputed %+v",
					id, i, gotBehavior.Checks[i], wantBehavior.Checks[i]))
			}
		}
	}
	for id := range gotByID {
		if _, ok := wantByID[id]; !ok {
			diffs = append(diffs, fmt.Sprintf("reported result has unexpected behavior %q", id))
		}
	}
	sort.Strings(diffs)
	return diffs
}

func printSummary(w io.Writer, reports map[string]CellReport) {
	fmt.Fprintf(w, "%-24s %-10s %-8s %-14s %s\n", "CELL", "SERVER", "STATUS", "BEHAVIORS", "DURATION")
	for _, cell := range Matrix {
		report, ok := reports[cell.ID]
		if !ok {
			fmt.Fprintf(w, "%-24s %-10s %-8s %-14s %s\n", cell.ID, cell.Version, "MISSING", "-", "-")
			continue
		}
		green, total := 0, 0
		var elapsed time.Duration
		for _, behavior := range report.Behaviors {
			total++
			if behavior.Status == "pass" {
				green++
			}
		}
		for _, suite := range report.Suites {
			elapsed += suite.Elapsed
		}
		fmt.Fprintf(w, "%-24s %-10s %-8s %-14s %s\n",
			cell.ID, report.ServerVersion, strings.ToUpper(report.Status),
			fmt.Sprintf("%d/%d", green, total), elapsed.Round(time.Second))
	}
}

// summarySchema versions the machine-readable gate verdict ticket 34 consumes.
// It is separate from reportSchema because a summary describes the whole gate,
// not one cell report.
const summarySchema = 1

// summaryFile is the machine-readable roll-up ticket 34 consumes. It is
// written for both pass and fail, so a failed gate still leaves evidence of
// which cell or fuzz target blocked the default switch.
type summaryFile struct {
	Schema      int                 `json:"schema"`
	Status      string              `json:"status"`
	GeneratedAt time.Time           `json:"generatedAt"`
	Commit      string              `json:"commit"`
	TreeHash    string              `json:"treeHash"`
	RunID       string              `json:"runID"`
	Cells       []summaryCell       `json:"cells"`
	FuzzTargets []summaryFuzzTarget `json:"fuzzTargets"`
	Problems    []string            `json:"problems,omitempty"`
}

type summaryCell struct {
	ID              string   `json:"id"`
	Platform        string   `json:"platform"`
	Version         string   `json:"version"`
	Status          string   `json:"status"`
	BehaviorsPassed int      `json:"behaviorsPassed"`
	BehaviorsTotal  int      `json:"behaviorsTotal"`
	Problems        []string `json:"problems,omitempty"`
}

type summaryFuzzTarget struct {
	Target   string   `json:"target"`
	Package  string   `json:"package"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	FuzzTime string   `json:"fuzzTime,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// buildSummary recomputes the per-cell and per-fuzz verdicts from the same
// raw artifacts verify checks. It never trusts a report's or receipt's
// self-reported status. failure is the gate error (if any); it forces a fail
// verdict and its lines are recorded so a summary written on an early load
// failure still explains why the gate could not pass.
func buildSummary(root string, reports map[string]CellReport, receipts []fuzzReceiptFile, opts gateOptions, failure error) summaryFile {
	opts = opts.normalized()
	summary := summaryFile{
		Schema:      summarySchema,
		Status:      "pass",
		GeneratedAt: opts.Now,
		Commit:      opts.Commit,
		TreeHash:    opts.TreeHash,
		RunID:       opts.RunID,
	}

	knownCells := make(map[string]bool, len(Matrix))
	for _, cell := range Matrix {
		knownCells[cell.ID] = true
	}
	for _, cell := range Matrix {
		entry := summaryCell{ID: cell.ID, Platform: cell.Platform, Version: cell.Version, Status: "fail"}
		report, ok := reports[cell.ID]
		if !ok {
			entry.Problems = []string{"no report"}
		} else {
			entry.Problems = verifyCell(cell, report, opts)
			behaviors := evaluateCell(cell, report.Suites)
			entry.BehaviorsTotal = len(behaviors)
			for _, behavior := range behaviors {
				if behavior.Status == "pass" {
					entry.BehaviorsPassed++
				}
			}
			if len(entry.Problems) == 0 {
				entry.Status = "pass"
			}
		}
		if entry.Status != "pass" {
			summary.Status = "fail"
		}
		summary.Cells = append(summary.Cells, entry)
	}
	for id := range reports {
		if !knownCells[id] {
			summary.Problems = append(summary.Problems,
				fmt.Sprintf("%s: report does not match any matrix cell", id))
		}
	}

	required := make(map[string]fuzzTarget, len(requiredFuzzTargets))
	for _, target := range requiredFuzzTargets {
		required[target.key()] = target
	}
	byKey := make(map[string][]fuzzReceiptFile, len(receipts))
	for _, file := range receipts {
		key := file.Receipt.Package + ":" + file.Receipt.Name
		byKey[key] = append(byKey[key], file)
	}
	for _, target := range requiredFuzzTargets {
		entry := summaryFuzzTarget{
			Target: target.key(), Package: target.Package, Name: target.Name, Status: "fail",
		}
		files := byKey[target.key()]
		switch {
		case len(files) == 0:
			entry.Problems = []string{
				fmt.Sprintf("required fuzz target %s has no receipt", target.key()),
			}
		default:
			if len(files) > 1 {
				names := make([]string, 0, len(files))
				for _, file := range files {
					names = append(names, file.FileName)
				}
				entry.Problems = append(entry.Problems,
					fmt.Sprintf("duplicate receipts: %s", strings.Join(names, ", ")))
			}
			for _, file := range files {
				entry.Problems = append(entry.Problems, verifyFuzzReceipt(root, target, file, opts)...)
				if entry.FuzzTime == "" && file.Receipt.FuzzTime > 0 {
					entry.FuzzTime = file.Receipt.FuzzTime.String()
				}
			}
			if len(entry.Problems) == 0 {
				entry.Status = "pass"
			}
		}
		if entry.Status != "pass" {
			summary.Status = "fail"
		}
		summary.FuzzTargets = append(summary.FuzzTargets, entry)
	}
	for key := range byKey {
		if _, ok := required[key]; !ok {
			summary.Problems = append(summary.Problems,
				fmt.Sprintf("fuzz receipt names %s, which is not a required fuzz target", key))
		}
	}

	if failure != nil {
		summary.Status = "fail"
		summary.Problems = appendUnique(summary.Problems, summaryErrorLines(failure)...)
	}
	if len(summary.Problems) > 0 {
		summary.Status = "fail"
	}
	return summary
}

func appendUnique(dst []string, values ...string) []string {
	seen := make(map[string]bool, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = true
	}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		dst = append(dst, value)
	}
	return dst
}

// summaryErrorLines flattens a gate error into problem lines. verify errors
// are multi-line and may repeat the gate prefix, so normalize each line.
func summaryErrorLines(err error) []string {
	if err == nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "ET parity gate FAILED:"))
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
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

// requireSummaryFile is the release-side check: the gate summary produced by
// the per-gate workflow must exist and agree with this verifier's run. It
// makes the release evidence job consume summary.json rather than only
// re-running the raw artifact checks, so a missing or stale summary blocks
// publication.
func requireSummaryFile(dir string, opts gateOptions) error {
	opts = opts.normalized()
	path := filepath.Join(dir, "summary.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("ET parity gate FAILED: required summary.json: %w", err)
	}
	var summary summaryFile
	if err := json.Unmarshal(data, &summary); err != nil {
		return fmt.Errorf("ET parity gate FAILED: required summary.json: %w", err)
	}
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if summary.Schema != summarySchema {
		add("summary schema %d, want %d", summary.Schema, summarySchema)
	}
	if summary.Status != "pass" {
		add("summary status %q", summary.Status)
	}
	if summary.Commit != opts.Commit {
		add("summary commit %q, want %q", summary.Commit, opts.Commit)
	}
	if summary.TreeHash != opts.TreeHash {
		add("summary tree %q, want %q", summary.TreeHash, opts.TreeHash)
	}
	switch {
	case opts.RunID != "" && summary.RunID == "":
		add("summary is not bound to CI run %q", opts.RunID)
	case opts.RunID != "" && summary.RunID != opts.RunID:
		add("summary CI run %q, want %q", summary.RunID, opts.RunID)
	case opts.RunID == "" && summary.RunID != "":
		add("summary is bound to CI run %q but verify has no GITHUB_RUN_ID", summary.RunID)
	}
	if summary.GeneratedAt.IsZero() {
		add("summary has no generation time")
	} else {
		age := opts.Now.Sub(summary.GeneratedAt.UTC())
		if age > opts.MaxAge {
			add("summary is stale (generated %s ago, limit %s)", age.Round(time.Second), opts.MaxAge)
		}
		if age < -reportClockSkew {
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
		if len(entry.Problems) > 0 {
			add("%s: summary records problems: %s", cell.ID, strings.Join(entry.Problems, "; "))
		}
	}
	knownCells := make(map[string]bool, len(Matrix))
	for _, cell := range Matrix {
		knownCells[cell.ID] = true
	}
	for id := range cells {
		if !knownCells[id] {
			add("%s: summary has an unexpected cell", id)
		}
	}

	fuzz := make(map[string]summaryFuzzTarget, len(summary.FuzzTargets))
	for _, target := range summary.FuzzTargets {
		if _, ok := fuzz[target.Target]; ok {
			add("summary has a duplicate fuzz target %q", target.Target)
		}
		fuzz[target.Target] = target
	}
	for _, required := range requiredFuzzTargets {
		entry, ok := fuzz[required.key()]
		if !ok {
			add("%s: summary has no fuzz target verdict", required.key())
			continue
		}
		if entry.Status != "pass" {
			add("%s: summary fuzz status %q", required.key(), entry.Status)
		}
		if entry.Package != required.Package || entry.Name != required.Name {
			add("%s: summary fuzz identity is %s:%s", required.key(), entry.Package, entry.Name)
		}
		fuzzTime, err := time.ParseDuration(entry.FuzzTime)
		if err != nil || fuzzTime < minFuzzTime {
			add("%s: summary fuzz time %q is below the documented minimum %s",
				required.key(), entry.FuzzTime, minFuzzTime)
		}
		if len(entry.Problems) > 0 {
			add("%s: summary records problems: %s", required.key(), strings.Join(entry.Problems, "; "))
		}
	}
	requiredKeys := make(map[string]bool, len(requiredFuzzTargets))
	for _, target := range requiredFuzzTargets {
		requiredKeys[target.key()] = true
	}
	for key := range fuzz {
		if !requiredKeys[key] {
			add("%s: summary has an unexpected fuzz target", key)
		}
	}

	if len(summary.Problems) > 0 {
		add("summary records gate problems: %s", strings.Join(summary.Problems, "; "))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("ET parity gate FAILED: required summary.json failed:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}
