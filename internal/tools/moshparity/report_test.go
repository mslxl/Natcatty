// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func testCell(t *testing.T, id string) Cell {
	t.Helper()
	cell, ok := findCell(id)
	if !ok {
		t.Fatalf("unknown test cell %q", id)
	}
	return cell
}

func testUname(osName string) string {
	switch osName {
	case hostOSLinux:
		return "Linux"
	case hostOSDarwin:
		return "Darwin"
	case hostOSFreeBSD:
		return "FreeBSD"
	}
	return osName
}

// passingReport builds a complete green report from synthetic raw suite
// results. verify recomputes behaviors from those results, so this exercises
// the same path as a real report rather than fabricating a stored summary.
func passingReport(t *testing.T, cell Cell) CellReport {
	t.Helper()
	suites := make(map[string]*SuiteResult)
	for _, name := range suitesForCell(cell) {
		suites[name] = &SuiteResult{Name: name, Status: "pass", Elapsed: time.Minute}
	}
	for _, behavior := range BehaviorsForCell(cell) {
		for _, ref := range RefsForVersion(behavior, cell.Version) {
			// A FreeBSD harness has no bundled client, so the real unit suite
			// skips bundle-dependent tests instead of running them.
			if ref.RequiresBundleTarget && !hostHasBundleTarget(cell, runtime.GOARCH) {
				continue
			}
			suite := suites[ref.Suite]
			if suite == nil {
				t.Fatalf("%s: reference %q uses suite %q, which the cell does not run",
					behavior.ID, ref.Name, ref.Suite)
			}
			name := strings.ReplaceAll(ref.Name, `.*`, "x")
			suite.Tests = append(suite.Tests, TestResult{
				Package: ref.Package, Name: name, Action: "pass",
			})
		}
	}
	report := CellReport{
		Schema:        reportSchema,
		Cell:          cell,
		GeneratedAt:   time.Now().UTC(),
		Commit:        testCommit,
		GoVersion:     runtime.Version(),
		ServerVersion: cell.Version,
		HostOS:        runtime.GOOS,
		HostArch:      runtime.GOARCH,
		HostUname:     hostUname(),
		LabOS:         cell.ExpectedLabOS(),
		LabArch:       runtime.GOARCH,
		LabAddress:    cell.SSHAddr,
		FuzzTime:      minReportFuzzTime.String(),
		Status:        "pass",
	}
	if cell.IsNative() {
		// Native reports can only be produced on the matching host; model that
		// so the fixture is what a real native runner would write.
		report.HostOS = cell.ExpectedLabOS()
		report.HostUname = testUname(cell.ExpectedLabOS())
	}
	for _, name := range suitesForCell(cell) {
		report.Suites = append(report.Suites, *suites[name])
	}
	report.Behaviors = evaluateCell(report)
	return report
}

func testCells(t *testing.T) []Cell {
	t.Helper()
	return []Cell{
		testCell(t, "debian-12/1.3.2"),
		testCell(t, "macos/1.3.2"),
		testCell(t, "freebsd-14/1.4.0"),
		testCell(t, "stock-client/1.4.0"),
	}
}

func testReports(t *testing.T, cells []Cell) map[string]CellReport {
	t.Helper()
	reports := make(map[string]CellReport)
	for _, cell := range cells {
		reports[cell.ID] = passingReport(t, cell)
	}
	return reports
}

func reportSuite(t *testing.T, report *CellReport, name string) *SuiteResult {
	t.Helper()
	for i := range report.Suites {
		if report.Suites[i].Name == name {
			return &report.Suites[i]
		}
	}
	t.Fatalf("report %s has no suite %q", report.Cell.ID, name)
	return nil
}

// tamperTest changes every result for one test (the fixture may contain the
// same package/test pair more than once because behaviors share references).
func tamperTest(suite *SuiteResult, action string) {
	if len(suite.Tests) == 0 {
		return
	}
	target := suite.Tests[0]
	for i := range suite.Tests {
		if suite.Tests[i].Package == target.Package && suite.Tests[i].Name == target.Name {
			suite.Tests[i].Action = action
		}
	}
}

// tamperNamedTest changes every result for one test name.
func tamperNamedTest(suite *SuiteResult, name, action string) {
	for i := range suite.Tests {
		if suite.Tests[i].Name == name {
			suite.Tests[i].Action = action
		}
	}
}

// removeTest drops every result for one test, so a required behavior proves
// the test did not run rather than letting a duplicate result mask it.
func removeTest(suite *SuiteResult) {
	if len(suite.Tests) == 0 {
		return
	}
	target := suite.Tests[0]
	kept := suite.Tests[:0]
	for _, result := range suite.Tests {
		if result.Package == target.Package && result.Name == target.Name {
			continue
		}
		kept = append(kept, result)
	}
	suite.Tests = kept
}

func TestVerifyReportsAcceptsCompleteGreenMatrix(t *testing.T) {
	cells := testCells(t)
	if err := verifyReports(cells, testReports(t, cells), testCommit); err != nil {
		t.Fatalf("complete green reports must pass verify: %v", err)
	}
}

// The unit suite runs on the harness host. A FreeBSD harness has no bundled
// mosh-client target, so TestLocaleFromEnvironment skips there; that skip is
// inapplicable evidence, not a missing test, because the behavior's model and
// lab references still run. On a Linux-hosted cell the same skip fails.
func TestVerifyReportsRequiresBundleDependentTestsOnlyWhereTheyCanRun(t *testing.T) {
	cells := testCells(t)
	reports := testReports(t, cells)

	report := reports["freebsd-14/1.4.0"]
	suite := reportSuite(t, &report, suiteUnit)
	// A real FreeBSD run reports the test and skips it.
	suite.Tests = append(suite.Tests, TestResult{
		Package: adapterMosh, Name: "TestLocaleFromEnvironment", Action: "skip",
	})
	reports["freebsd-14/1.4.0"] = report
	if err := verifyReports(cells, reports, testCommit); err != nil {
		t.Fatalf("a bundle-dependent skip on FreeBSD must not fail verify: %v", err)
	}

	report = reports["debian-12/1.3.2"]
	tamperNamedTest(reportSuite(t, &report, suiteUnit), "TestLocaleFromEnvironment", "skip")
	reports["debian-12/1.3.2"] = report
	if err := verifyReports(cells, reports, testCommit); err == nil {
		t.Fatal("a bundle-dependent skip on a Linux-hosted cell must fail verify")
	}
}

func TestVerifyReportsIgnoresStoredBehaviors(t *testing.T) {
	cells := testCells(t)
	reports := testReports(t, cells)
	// Behaviors is derived. Zeroing it must not fail a report whose raw
	// suites prove every required behavior...
	for id, report := range reports {
		report.Behaviors = nil
		reports[id] = report
	}
	if err := verifyReports(cells, reports, testCommit); err != nil {
		t.Fatalf("stored behaviors must not be authoritative: %v", err)
	}

	// ...and claiming green stored behaviors must not rescue suites that did
	// not run.
	for id, report := range reports {
		report.Suites = nil
		report.Behaviors = passingReport(t, report.Cell).Behaviors
		reports[id] = report
	}
	err := verifyReports(cells, reports, testCommit)
	if err == nil || !strings.Contains(err.Error(), "required suite") {
		t.Fatalf("green stored behaviors with missing suites must fail, got %v", err)
	}
}

func TestVerifyReportsFailClosed(t *testing.T) {
	cells := testCells(t)
	tests := []struct {
		name   string
		head   string
		mutate func(*testing.T, map[string]CellReport)
		want   string
	}{
		{
			name: "missing report",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				delete(reports, "debian-12/1.3.2")
			},
			want: "missing report",
		},
		{
			name: "stale commit",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				for id, report := range reports {
					report.Commit = "stale"
					reports[id] = report
				}
			},
			want: "report commit stale",
		},
		{
			name: "reports from different commits",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["macos/1.3.2"]
				report.Commit = "another"
				reports["macos/1.3.2"] = report
			},
			want: "reports come from different commits",
		},
		{
			name: "empty expected commit",
			head: "",
			want: "expected commit is empty",
		},
		{
			name: "empty report commit",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Commit = ""
				reports["debian-12/1.3.2"] = report
			},
			want: "report has no commit",
		},
		{
			name: "missing required suite",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Suites = report.Suites[:1] // drop lab and fuzz
				reports["debian-12/1.3.2"] = report
			},
			want: "required suite",
		},
		{
			name: "zero suites",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Suites = nil
				reports["debian-12/1.3.2"] = report
			},
			want: "required suite",
		},
		{
			name: "tampered suite status",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				reportSuite(t, &report, suiteLab).Status = "fail"
				reports["debian-12/1.3.2"] = report
			},
			want: "suite lab status",
		},
		{
			name: "tampered test action",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				tamperTest(reportSuite(t, &report, suiteUnit), "fail")
				reports["debian-12/1.3.2"] = report
			},
			want: "behavior",
		},
		{
			name: "skipped test",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				tamperTest(reportSuite(t, &report, suiteUnit), "skip")
				reports["debian-12/1.3.2"] = report
			},
			want: "behavior",
		},
		{
			name: "missing test",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				removeTest(reportSuite(t, &report, suiteUnit))
				reports["debian-12/1.3.2"] = report
			},
			want: "behavior",
		},
		{
			name: "schema mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Schema = reportSchema - 1
				reports["debian-12/1.3.2"] = report
			},
			want: "report schema",
		},
		{
			name: "identity mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Cell.SSHAddr = "127.0.0.1:9999"
				reports["debian-12/1.3.2"] = report
			},
			want: "report identity",
		},
		{
			name: "unknown report",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				extra := passingReport(t, testCell(t, "debian-12/1.4.0"))
				extra.Cell.ID = "unknown/1.4.0"
				reports[extra.Cell.ID] = extra
			},
			want: "does not match any required matrix cell",
		},
		{
			name: "cell status",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Status = "fail"
				reports["debian-12/1.3.2"] = report
			},
			want: "cell status",
		},
		{
			name: "server version mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.ServerVersion = v140
				reports["debian-12/1.3.2"] = report
			},
			want: "server version",
		},
		{
			name: "fuzz time below minimum",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.FuzzTime = "4s"
				reports["debian-12/1.3.2"] = report
			},
			want: "minimum 5s",
		},
		{
			name: "missing fuzz time",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.FuzzTime = ""
				reports["debian-12/1.3.2"] = report
			},
			want: "no fuzz time",
		},
		{
			name: "invalid fuzz time",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.FuzzTime = "a while"
				reports["debian-12/1.3.2"] = report
			},
			want: "invalid fuzz time",
		},
		{
			name: "fuzz suite elapsed too short",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.FuzzTime = "10s"
				reportSuite(t, &report, suiteFuzz).Elapsed = 40 * time.Second
				reports["debian-12/1.3.2"] = report
			},
			want: "fuzz suite elapsed",
		},
		{
			name: "zero stored behaviors is not authoritative",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.Behaviors = nil
				reports["debian-12/1.3.2"] = report
			},
			want: "", // suites still prove every behavior
		},
		{
			name: "docker lab platform mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.LabOS = hostOSDarwin
				reports["debian-12/1.3.2"] = report
			},
			want: `lab platform "darwin", want "linux"`,
		},
		{
			name: "native lab platform mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["macos/1.3.2"]
				report.LabOS = hostOSLinux
				report.HostOS = hostOSLinux
				report.HostUname = "Linux"
				reports["macos/1.3.2"] = report
			},
			want: `lab platform "linux", want "darwin"`,
		},
		{
			name: "native host OS mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["macos/1.3.2"]
				report.HostOS = hostOSLinux
				reports["macos/1.3.2"] = report
			},
			want: `host OS "linux", want "darwin"`,
		},
		{
			name: "native uname mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["macos/1.3.2"]
				report.HostUname = "Linux"
				reports["macos/1.3.2"] = report
			},
			want: `uname "Linux", want "darwin"`,
		},
		{
			name: "missing host provenance",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.HostOS = ""
				report.HostArch = ""
				reports["debian-12/1.3.2"] = report
			},
			want: "no host OS provenance",
		},
		{
			name: "missing lab provenance",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.LabOS = ""
				report.LabArch = ""
				reports["debian-12/1.3.2"] = report
			},
			want: "no lab platform provenance",
		},
		{
			name: "missing lab address",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.LabAddress = ""
				reports["debian-12/1.3.2"] = report
			},
			want: "no lab address",
		},
		{
			name: "docker lab address mismatch",
			head: testCommit,
			mutate: func(t *testing.T, reports map[string]CellReport) {
				report := reports["debian-12/1.3.2"]
				report.LabAddress = "127.0.0.1:22"
				reports["debian-12/1.3.2"] = report
			},
			want: "lab address",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reports := testReports(t, cells)
			if test.mutate != nil {
				test.mutate(t, reports)
			}
			err := verifyReports(cells, reports, test.head)
			if test.want == "" {
				if err != nil {
					t.Fatalf("verify must pass (%s): %v", test.name, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("verify must fail (%s)", test.name)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verify error %q does not contain %q", err, test.want)
			}
		})
	}
}

func TestLoadReportsFailClosed(t *testing.T) {
	cell := testCell(t, "debian-12/1.3.2")
	writeGreen := func(t *testing.T, dir string) []byte {
		t.Helper()
		if err := writeReport(dir, passingReport(t, cell)); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, reportFilename(cell.ID)))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	writeFile := func(t *testing.T, dir, name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{
			name: "corrupt JSON",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "broken.json", []byte("{not json"))
			},
			want: "broken.json",
		},
		{
			name: "duplicate cell report",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "duplicate.json", writeGreen(t, dir))
			},
			want: "duplicate report",
		},
		{
			name: "report without a cell ID",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "anonymous.json", []byte(`{"schema":2,"status":"pass"}`))
			},
			want: "no cell ID",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			test.setup(t, dir)
			if _, err := loadReports(dir); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadReports must fail with %q, got %v", test.want, err)
			}
		})
	}
}

func TestVerifyCommandExitStatus(t *testing.T) {
	dir := t.TempDir()
	for _, cell := range Matrix {
		if err := writeReport(dir, passingReport(t, cell)); err != nil {
			t.Fatal(err)
		}
	}
	if code := runMain([]string{"verify", "-dir", dir, "-commit", testCommit}); code != 0 {
		t.Fatalf("verify over a complete green matrix must exit 0, got %d", code)
	}
	// The release evidence job requires the summary this verify just wrote.
	if code := runMain([]string{"verify", "-dir", dir, "-commit", testCommit, "-require-summary"}); code != 0 {
		t.Fatalf("verify -require-summary rejected the gate's own summary, got %d", code)
	}
	if err := os.Remove(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if code := runMain([]string{"verify", "-dir", dir, "-commit", testCommit, "-require-summary"}); code != 1 {
		t.Fatalf("verify -require-summary without a summary must exit 1, got %d", code)
	}

	// A missing report is a non-zero verify.
	if err := os.Remove(filepath.Join(dir, reportFilename("debian-12/1.3.2"))); err != nil {
		t.Fatal(err)
	}
	if code := runMain([]string{"verify", "-dir", dir, "-commit", testCommit}); code != 1 {
		t.Fatalf("missing report must exit 1, got %d", code)
	}
	err := run([]string{"verify", "-dir", dir, "-commit", testCommit})
	if err == nil || !strings.Contains(err.Error(), "missing report") {
		t.Fatalf("missing report error does not name the cell: %v", err)
	}

	// Corrupt JSON is a non-zero verify before any cell comparison.
	corruptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(corruptDir, "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runMain([]string{"verify", "-dir", corruptDir, "-commit", testCommit}); code != 1 {
		t.Fatalf("corrupt report must exit 1, got %d", code)
	}
}

func TestVerifyCommandFailsWhenCommitCannotBeDetermined(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(commitEnv, "")
	t.Setenv("PATH", t.TempDir())
	if code := runMain([]string{"verify", "-dir", dir}); code != 1 {
		t.Fatalf("verify without a determinable commit must exit 1, got %d", code)
	}
	// The workflow uploads summary.json even when verification cannot start,
	// so the failed verdict must still be written.
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("summary.json was not written on the commit failure path: %v", err)
	}
	var summary summaryFile
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("summary.json is not valid JSON: %v", err)
	}
	if summary.Status != "fail" {
		t.Fatalf("summary status = %q, want fail", summary.Status)
	}
}

// TestVerifyCellFuzzTimeRejectsImplausibleClaims proves the elapsed-time
// contract fails closed for forged, zero, negative, and overflowing fuzz
// durations. In particular, a multi-year fuzzTime used to wrap the required
// elapsed product negative so that elapsed 0 passed.
func TestVerifyCellFuzzTimeRejectsImplausibleClaims(t *testing.T) {
	cell := testCell(t, "debian-12/1.3.2")
	tests := []struct {
		name      string
		fuzzTime  string
		elapsed   time.Duration
		wantError string
	}{
		{name: "missing", fuzzTime: "", wantError: "no fuzz time"},
		{name: "invalid", fuzzTime: "a while", wantError: "invalid fuzz time"},
		{name: "zero", fuzzTime: "0s", elapsed: time.Minute, wantError: "minimum 5s"},
		{name: "negative", fuzzTime: "-1000000h", elapsed: time.Hour, wantError: "minimum 5s"},
		{name: "below minimum", fuzzTime: "4s", elapsed: time.Minute, wantError: "minimum 5s"},
		{name: "too large", fuzzTime: "1000000h", elapsed: 0, wantError: "maximum 1h0m0s"},
		{name: "just over maximum", fuzzTime: "1h0m0.000000001s", elapsed: 5 * time.Hour, wantError: "maximum 1h0m0s"},
		{name: "valid minimum", fuzzTime: "5s", elapsed: 25 * time.Second},
		{name: "valid maximum", fuzzTime: "1h", elapsed: 5 * time.Hour},
		{name: "elapsed too short", fuzzTime: "10s", elapsed: 49*time.Second + 999*time.Millisecond, wantError: "fuzz suite elapsed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := CellReport{
				Cell:     cell,
				FuzzTime: test.fuzzTime,
				Suites: []SuiteResult{{
					Name:    suiteFuzz,
					Status:  "pass",
					Elapsed: test.elapsed,
				}},
			}
			failures := verifyCellFuzzTime(cell, report)
			if test.wantError == "" {
				if len(failures) != 0 {
					t.Fatalf("fuzz time %q with elapsed %s must pass, got %v", test.fuzzTime, test.elapsed, failures)
				}
				return
			}
			if len(failures) == 0 {
				t.Fatalf("fuzz time %q with elapsed %s must fail with %q", test.fuzzTime, test.elapsed, test.wantError)
			}
			if !strings.Contains(strings.Join(failures, "\n"), test.wantError) {
				t.Fatalf("failures %q do not contain %q", failures, test.wantError)
			}
		})
	}
}

// TestFuzzElapsedBound proves the multiplication guard cannot overflow. The
// per-target cap already makes this unreachable for the real target list; the
// checked helper keeps the contract safe if the list ever grows.
func TestFuzzElapsedBound(t *testing.T) {
	tests := []struct {
		name     string
		fuzzTime time.Duration
		targets  int
		want     time.Duration
		ok       bool
	}{
		{name: "minimum", fuzzTime: 5 * time.Second, targets: 5, want: 25 * time.Second, ok: true},
		{name: "maximum", fuzzTime: time.Hour, targets: 5, want: 5 * time.Hour, ok: true},
		{name: "zero fuzz time", fuzzTime: 0, targets: 5},
		{name: "negative fuzz time", fuzzTime: -time.Second, targets: 5},
		{name: "zero targets", fuzzTime: time.Second, targets: 0},
		{name: "negative targets", fuzzTime: time.Second, targets: -1},
		{name: "product overflow", fuzzTime: time.Duration(math.MaxInt64/2) + 1, targets: 2},
		{name: "exact division", fuzzTime: time.Duration(math.MaxInt64 / 2), targets: 2, want: time.Duration(math.MaxInt64 / 2 * 2), ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := fuzzElapsedBound(test.fuzzTime, test.targets)
			if ok != test.ok {
				t.Fatalf("fuzzElapsedBound(%s, %d) ok = %v, want %v", test.fuzzTime, test.targets, ok, test.ok)
			}
			if ok && got != test.want {
				t.Fatalf("fuzzElapsedBound(%s, %d) = %s, want %s", test.fuzzTime, test.targets, got, test.want)
			}
		})
	}
}

func greenSummary(t *testing.T) summaryFile {
	t.Helper()
	reports := make(map[string]CellReport)
	for _, cell := range Matrix {
		reports[cell.ID] = passingReport(t, cell)
	}
	return buildSummary(Matrix, reports, nil)
}

// TestRequireSummaryFile proves the release-side summary check fails closed
// for a missing summary, a non-green verdict, and a summary bound to another
// commit or a different matrix.
func TestRequireSummaryFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeSummary(dir, greenSummary(t)); err != nil {
		t.Fatal(err)
	}
	if err := requireSummaryFile(dir, testCommit); err != nil {
		t.Fatalf("green summary rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if err := requireSummaryFile(dir, testCommit); err == nil || !strings.Contains(err.Error(), "required summary.json") {
		t.Fatalf("missing summary = %v, want required-summary failure", err)
	}

	mutations := []struct {
		name   string
		change func(*summaryFile)
	}{
		{name: "schema", change: func(s *summaryFile) { s.Schema++ }},
		{name: "status", change: func(s *summaryFile) { s.Status = "fail" }},
		{name: "commit", change: func(s *summaryFile) { s.Commit = "other" }},
		{name: "generation time", change: func(s *summaryFile) { s.GeneratedAt = time.Time{} }},
		{name: "stale", change: func(s *summaryFile) { s.GeneratedAt = time.Now().Add(-2 * maxSummaryAge) }},
		{name: "missing cell", change: func(s *summaryFile) { s.Cells = s.Cells[1:] }},
		{name: "failed cell", change: func(s *summaryFile) { s.Cells[0].Status = "fail" }},
		{name: "failed behavior count", change: func(s *summaryFile) { s.Cells[0].BehaviorsPassed-- }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			summary := greenSummary(t)
			mutation.change(&summary)
			if err := writeSummary(dir, summary); err != nil {
				t.Fatal(err)
			}
			err := requireSummaryFile(dir, testCommit)
			if err == nil || !strings.Contains(err.Error(), "required summary.json failed") {
				t.Fatalf("tampered summary = %v, want failure", err)
			}
		})
	}
}
