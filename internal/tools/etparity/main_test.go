package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testTree   = "89abcdef0123456789abcdef0123456789abcdef"
	testRunID  = "1234567890"
	testNow    = "2026-11-05T12:00:00Z"
)

func TestMatrixIsCompleteAndUnique(t *testing.T) {
	if err := validateMatrix(Matrix); err != nil {
		t.Fatalf("matrix: %v", err)
	}
	docker := 0
	native := 0
	for _, cell := range Matrix {
		if cell.Native {
			native++
			if cell.GOOS != "darwin" || cell.GOARCH != "arm64" {
				t.Fatalf("native cell %s host = %s/%s, want darwin/arm64", cell.ID, cell.GOOS, cell.GOARCH)
			}
		} else {
			docker++
			if cell.GOOS != "linux" || cell.GOARCH != "amd64" {
				t.Fatalf("docker cell %s host = %s/%s, want linux/amd64", cell.ID, cell.GOOS, cell.GOARCH)
			}
			if !imageDigestPattern.MatchString(cell.BaseImageDigest) {
				t.Fatalf("docker cell %s digest = %q", cell.ID, cell.BaseImageDigest)
			}
		}
	}
	if docker != 8 {
		t.Fatalf("docker cells = %d, want 8 (four platforms x two stock versions)", docker)
	}
	if native != 2 {
		t.Fatalf("native cells = %d, want 2 (macOS 7.1.1 and 6.2.10)", native)
	}
	for _, platform := range []string{"debian-12", "ubuntu-24.04", "fedora-41", "arch-linux", "macos"} {
		cells := cellsForPlatform(platform)
		if len(cells) != 2 {
			t.Fatalf("%s has %d cells, want 2", platform, len(cells))
		}
	}
}

func TestMatrixRejectsMissingCellInvariants(t *testing.T) {
	valid := Cell{
		ID: "a", Platform: "p", Version: v711, Service: "s", SSHAddr: "127.0.0.1:1",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3",
	}
	other := Cell{
		ID: "b", Platform: "p", Version: v6210, Service: "s2", SSHAddr: "127.0.0.1:2",
		GOOS: "linux", GOARCH: "amd64",
		BaseImageDigest: "sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3",
	}
	cases := []struct {
		name  string
		cells []Cell
	}{
		{"unpinned version", []Cell{{ID: "x", Platform: "p", Version: "9.9"}}},
		{"duplicate service", []Cell{valid, other}},
		{"missing required version", []Cell{valid}},
		{"missing host platform", []Cell{{ID: "a", Platform: "p", Version: v711, Service: "s", SSHAddr: "127.0.0.1:1", BaseImageDigest: valid.BaseImageDigest}}},
		{"remote endpoint", []Cell{{ID: "a", Platform: "p", Version: v711, Service: "s", SSHAddr: "10.0.0.2:22", GOOS: "linux", GOARCH: "amd64", BaseImageDigest: valid.BaseImageDigest}}},
		{"truncated digest", []Cell{{ID: "a", Platform: "p", Version: v711, Service: "s", SSHAddr: "127.0.0.1:1", GOOS: "linux", GOARCH: "amd64", BaseImageDigest: "sha256:99c0"}}},
		{"wrong docker architecture", []Cell{{ID: "a", Platform: "p", Version: v711, Service: "s", SSHAddr: "127.0.0.1:1", GOOS: "darwin", GOARCH: "arm64", BaseImageDigest: valid.BaseImageDigest}}},
		{"native with service", []Cell{{ID: "a", Platform: "p", Version: v711, Native: true, Service: "s", SSHAddr: "127.0.0.1:22", GOOS: "darwin", GOARCH: "arm64"}}},
		{"native wrong host", []Cell{{ID: "a", Platform: "p", Version: v711, Native: true, SSHAddr: "127.0.0.1:22", GOOS: "linux", GOARCH: "amd64"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMatrix(tc.cells); err == nil {
				t.Fatalf("validateMatrix accepted %+v", tc.cells)
			}
		})
	}
}

func TestLabOverrideIsNativeOnlyAndLoopback(t *testing.T) {
	docker, _ := findCell("debian-12/7.1.1")
	native, _ := findCell("macos/7.1.1")
	if err := validateLabOverride(docker, "127.0.0.1:22"); err == nil {
		t.Fatal("-lab-addr accepted for a Docker cell")
	}
	if err := validateLabOverride(native, "10.0.0.5:22"); err == nil {
		t.Fatal("-lab-addr accepted a non-loopback endpoint")
	}
	if err := validateLabOverride(native, "example.com:22"); err == nil {
		t.Fatal("-lab-addr accepted a hostname")
	}
	if err := validateLabOverride(native, "127.0.0.1:notaport"); err == nil {
		t.Fatal("-lab-addr accepted an invalid port")
	}
	for _, address := range []string{"127.0.0.1:22", "localhost:22", "[::1]:22"} {
		if err := validateLabOverride(native, address); err != nil {
			t.Fatalf("-lab-addr %s: %v", address, err)
		}
	}
}

func TestSkipPatternNamesVersionSpecificTests(t *testing.T) {
	if got := skipPattern(v711); !strings.Contains(got, "TestInProcessLegacyReattachFailsLikeStock6210") {
		t.Fatalf("7.1.1 skip pattern = %q", got)
	}
	for _, name := range SkipForVersion(v6210) {
		if !strings.Contains(skipPattern(v6210), name) {
			t.Fatalf("6.2.10 skip pattern misses %q", name)
		}
	}
	if got := skipPattern("9.9"); got != "" {
		t.Fatalf("unknown version skip pattern = %q", got)
	}
}

func TestCellEnvSetsExactlyOneVersionEndpoint(t *testing.T) {
	for _, cell := range Matrix {
		env := CellEnv(cell, "127.0.0.1:9999")
		joined := strings.Join(env, "\n")
		if !strings.Contains(joined, "NATCATTY_ET_LAB_VERSION="+cell.Version) {
			t.Fatalf("%s env lacks the version filter", cell.ID)
		}
		has711 := strings.Contains(joined, "NATCATTY_ET_LAB_711=")
		has6210 := strings.Contains(joined, "NATCATTY_ET_LAB_6210=")
		if cell.Version == v711 && (!has711 || has6210) {
			t.Fatalf("%s env endpoints are wrong:\n%s", cell.ID, joined)
		}
		if cell.Version == v6210 && (!has6210 || has711) {
			t.Fatalf("%s env endpoints are wrong:\n%s", cell.ID, joined)
		}
	}
}

func TestParseGoTestJSONKeepsFinalAction(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestA"}`,
		`{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN\n"}`,
		`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.1}`,
		`{"Action":"skip","Package":"p","Test":"TestB"}`,
		`{"Action":"fail","Package":"p","Test":"TestC/sub"}`,
		`not json`,
		`{"Action":"fail","Package":"p"}`,
	}, "\n")
	tests, err := parseGoTestJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := make(map[string]string)
	for _, test := range tests {
		got[test.Name] = test.Action
	}
	if got["TestA"] != "pass" || got["TestB"] != "skip" || got["TestC/sub"] != "fail" {
		t.Fatalf("actions = %v", got)
	}
	if len(tests) != 3 {
		t.Fatalf("parsed %d tests, want 3", len(tests))
	}
}

// concreteName turns a required reference pattern into a plausible test name
// so a test can build a fully green synthetic suite.
func concreteName(pattern, version string) string {
	name := strings.ReplaceAll(pattern, `\.`, ".")
	name = strings.ReplaceAll(name, ".*", version)
	return name
}

// greenSuites synthesizes passing suites for every reference of a cell.
func greenSuites(cell Cell) []SuiteResult {
	labTests := make(map[string]TestResult)
	unitTests := make(map[string]TestResult)
	for _, behavior := range BehaviorsForVersion(cell.Version) {
		for _, ref := range RefsForVersion(behavior, cell.Version) {
			name := concreteName(ref.Name, cell.Version)
			target := unitTests
			if ref.Suite == suiteLab {
				target = labTests
			}
			target[ref.Package+"\x00"+name] = TestResult{
				Package: ref.Package, Name: name, Action: "pass",
			}
		}
	}
	makeSuite := func(name string, tests map[string]TestResult) SuiteResult {
		suite := SuiteResult{
			Name: name, Status: "pass",
			Output:   "synthetic go test -json output",
			Packages: []string{requiredSuitePackages[name]},
		}
		for _, test := range tests {
			suite.Tests = append(suite.Tests, test)
		}
		return suite
	}
	return []SuiteResult{makeSuite(suiteLab, labTests), makeSuite(suiteUnit, unitTests)}
}

// A failing suite keeps the tail of its captured output: go test prints the
// diagnosis last, after JSON progress lines that can exceed the capture limit
// on their own, so a head-only capture would lose it.
func TestSuiteOutputKeepsTheFailureTail(t *testing.T) {
	const limit = 64
	combined := strings.Repeat("progress line\n", 20) +
		`{"Action":"fail","Package":"p"}` + "\npanic: test timed out\n"
	failed := suiteOutput(combined, true, limit)
	if !strings.HasSuffix(failed, "panic: test timed out\n") {
		t.Fatalf("failing suite output does not keep the tail: %q", failed)
	}
	if !strings.Contains(failed, "... truncated ...") {
		t.Fatalf("failing suite output does not mark the dropped head: %q", failed)
	}
	passed := suiteOutput(combined, false, limit)
	if !strings.HasPrefix(passed, "progress line\n") {
		t.Fatalf("passing suite output does not keep the head: %q", passed)
	}
}

func TestEvaluateCellIsGreenForFullSyntheticResults(t *testing.T) {
	for _, cell := range Matrix {
		behaviors := evaluateCell(cell, greenSuites(cell))
		if len(behaviors) != len(BehaviorsForVersion(cell.Version)) {
			t.Fatalf("%s evaluated %d behaviors, want %d", cell.ID, len(behaviors), len(BehaviorsForVersion(cell.Version)))
		}
		for _, behavior := range behaviors {
			if behavior.Status != "pass" {
				t.Fatalf("%s behavior %s = %s: %+v", cell.ID, behavior.ID, behavior.Status, behavior.Checks)
			}
		}
	}
}

func TestEvaluateCellFailsOnFailedSkippedAndMissingTests(t *testing.T) {
	cell, _ := findCell("debian-12/7.1.1")
	behaviors := BehaviorsForVersion(cell.Version)
	ref := RefsForVersion(behaviors[0], cell.Version)[0]
	name := concreteName(ref.Name, cell.Version)

	cases := []struct {
		name   string
		mutate func([]SuiteResult) []SuiteResult
	}{
		{"failed", func(suites []SuiteResult) []SuiteResult {
			setAction(suites, name, "fail")
			return suites
		}},
		{"skipped", func(suites []SuiteResult) []SuiteResult {
			setAction(suites, name, "skip")
			return suites
		}},
		{"missing suite", func(suites []SuiteResult) []SuiteResult {
			return suites[1:]
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateCell(cell, tc.mutate(greenSuites(cell)))
			if got[0].Status != "fail" {
				t.Fatalf("behavior %s = %s, want fail", got[0].ID, got[0].Status)
			}
		})
	}
}

func setAction(suites []SuiteResult, testName, action string) {
	for i := range suites {
		for j := range suites[i].Tests {
			if suites[i].Tests[j].Name == testName {
				suites[i].Tests[j].Action = action
			}
		}
	}
}

func gateNow(t *testing.T) time.Time {
	t.Helper()
	now, err := time.Parse(time.RFC3339, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func greenReport(cell Cell, now time.Time, runID string) CellReport {
	suites := greenSuites(cell)
	behaviors := evaluateCell(cell, suites)
	status := "pass"
	for _, suite := range suites {
		if suite.Status != "pass" {
			status = "fail"
		}
	}
	for _, behavior := range behaviors {
		if behavior.Status != "pass" {
			status = "fail"
		}
	}
	return CellReport{
		Schema:        reportSchema,
		Cell:          cell,
		GeneratedAt:   now.UTC(),
		Commit:        testCommit,
		TreeHash:      testTree,
		GoVersion:     "go1.27.3",
		HostOS:        cell.GOOS,
		HostArch:      cell.GOARCH,
		RunID:         runID,
		RunnerName:    "test-runner",
		ServerVersion: cell.Version,
		LabAddress:    cell.SSHAddr,
		Status:        status,
		Suites:        suites,
		Behaviors:     behaviors,
	}
}

func greenReports(now time.Time, runID string) map[string]CellReport {
	reports := make(map[string]CellReport)
	for _, cell := range Matrix {
		reports[cell.ID] = greenReport(cell, now, runID)
	}
	return reports
}

func greenGate(now time.Time) gateOptions {
	return gateOptions{
		Commit:   testCommit,
		TreeHash: testTree,
		RunID:    testRunID,
		MaxAge:   defaultMaxReportAge,
		Now:      now,
	}
}

func cloneReports(in map[string]CellReport) map[string]CellReport {
	out := make(map[string]CellReport, len(in))
	for key, report := range in {
		report.Suites = append([]SuiteResult(nil), report.Suites...)
		for i := range report.Suites {
			report.Suites[i].Packages = append([]string(nil), report.Suites[i].Packages...)
			report.Suites[i].Tests = append([]TestResult(nil), report.Suites[i].Tests...)
		}
		report.Behaviors = append([]BehaviorResult(nil), report.Behaviors...)
		for i := range report.Behaviors {
			report.Behaviors[i].Checks = append([]CheckResult(nil), report.Behaviors[i].Checks...)
		}
		out[key] = report
	}
	return out
}

func TestVerifyReportsAcceptsFullyGreenMatrix(t *testing.T) {
	now := gateNow(t)
	reports := greenReports(now, testRunID)
	if err := verifyReports(Matrix, reports, greenGate(now)); err != nil {
		t.Fatalf("green reports rejected: %v", err)
	}
}

func TestVerifyReportsFailsClosed(t *testing.T) {
	now := gateNow(t)
	green := greenReports(now, testRunID)

	mutate := func(t *testing.T, cellID string, change func(*CellReport)) map[string]CellReport {
		t.Helper()
		reports := cloneReports(green)
		report := reports[cellID]
		change(&report)
		reports[cellID] = report
		return reports
	}

	cases := []struct {
		name  string
		opts  gateOptions
		build func(*testing.T) map[string]CellReport
	}{
		{
			name:  "unknown checkout commit",
			opts:  func() gateOptions { o := greenGate(now); o.Commit = "unknown"; return o }(),
			build: func(t *testing.T) map[string]CellReport { return green },
		},
		{
			name:  "unknown checkout tree",
			opts:  func() gateOptions { o := greenGate(now); o.TreeHash = ""; return o }(),
			build: func(t *testing.T) map[string]CellReport { return green },
		},
		{
			name: "missing cell",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				reports := cloneReports(green)
				delete(reports, "fedora-41/6.2.10")
				return reports
			},
		},
		{
			name: "unexpected report",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				reports := cloneReports(green)
				reports["extra/cell"] = reports["debian-12/7.1.1"]
				return reports
			},
		},
		{
			name: "failed cell status",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) { r.Status = "fail" })
			},
		},
		{
			name: "failed suite status",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) { r.Suites[0].Status = "fail" })
			},
		},
		{
			name: "missing required suite",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) { r.Suites = r.Suites[:1] })
			},
		},
		{
			name: "duplicate suite",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) { r.Suites = append(r.Suites, r.Suites[0]) })
			},
		},
		{
			name: "empty suite",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) { r.Suites[1].Tests = nil })
			},
		},
		{
			name: "suite without output",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) { r.Suites[0].Output = "" })
			},
		},
		{
			name: "suite with substituted packages",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) {
					r.Suites[0].Packages = []string{"./unrelated/package"}
				})
			},
		},
		{
			name: "test outside the adapter package",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "arch-linux/7.1.1", func(r *CellReport) {
					r.Suites[0].Tests[0].Package = "example.com/not-the-adapter"
				})
			},
		},
		{
			name: "failed behavior",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) { r.Behaviors[0].Status = "fail" })
			},
		},
		{
			name: "behavior reports pass with a failed check",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Behaviors[0].Checks[0].Status = "fail"
					r.Behaviors[0].Checks[0].Detail = "test failed"
				})
			},
		},
		{
			name: "behavior reports pass with zero checks",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Behaviors[0].Status = "pass"
					r.Behaviors[0].Checks = nil
				})
			},
		},
		{
			name: "behavior reports pass without a test name",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Behaviors[0].Checks[0].Test = ""
				})
			},
		},
		{
			name: "self-reported pass with a failing test",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					name := r.Behaviors[0].Checks[0].Test
					setAction(r.Suites, name, "fail")
				})
			},
		},
		{
			name: "unrelated failing test in a required suite",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Suites[0].Tests = append(r.Suites[0].Tests, TestResult{
						Package: etPackage, Name: "TestUnrelatedFailure", Action: "fail",
					})
				})
			},
		},
		{
			name: "synthetic report with no suites",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Status = "pass"
					r.Suites = nil
					r.Behaviors = nil
				})
			},
		},
		{
			name: "wrong server version",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "ubuntu-24.04/7.1.1", func(r *CellReport) { r.ServerVersion = v6210 })
			},
		},
		{
			name: "report from another commit",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Commit = "fedcba9876543210fedcba9876543210fedcba98"
				})
			},
		},
		{
			name: "report without commit",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) { r.Commit = "" })
			},
		},
		{
			name: "report from another tree",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.TreeHash = "fedcba9876543210fedcba9876543210fedcba98"
				})
			},
		},
		{
			name: "wrong harness platform",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.HostOS = "darwin"
					r.HostArch = "arm64"
				})
			},
		},
		{
			name: "missing harness platform",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.HostOS = ""
					r.HostArch = ""
				})
			},
		},
		{
			name: "spoofed native report",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "macos/7.1.1", func(r *CellReport) {
					r.HostOS = "linux"
					r.HostArch = "amd64"
				})
			},
		},
		{
			name: "cell metadata spoofed",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Cell.Service = "another-service"
				})
			},
		},
		{
			name: "docker lab address changed",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.LabAddress = "127.0.0.1:2999"
				})
			},
		},
		{
			name: "remote lab address",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.LabAddress = "10.0.0.9:2322"
				})
			},
		},
		{
			name: "stale report",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.GeneratedAt = now.Add(-defaultMaxReportAge - time.Minute)
				})
			},
		},
		{
			name: "future report",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.GeneratedAt = now.Add(time.Hour)
				})
			},
		},
		{
			name: "report without generation time",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) { r.GeneratedAt = time.Time{} })
			},
		},
		{
			name: "report from another CI run",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) { r.RunID = "9999999999" })
			},
		},
		{
			name: "report without CI run id",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) { r.RunID = "" })
			},
		},
		{
			name:  "run-bound report verified without a run",
			opts:  func() gateOptions { o := greenGate(now); o.RunID = ""; return o }(),
			build: func(t *testing.T) map[string]CellReport { return green },
		},
		{
			name: "deleted behavior",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) { r.Behaviors = r.Behaviors[1:] })
			},
		},
		{
			name: "extra behavior",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					extra := r.Behaviors[0]
					extra.ID = "not-a-required-behavior"
					r.Behaviors = append(r.Behaviors, extra)
				})
			},
		},
		{
			name: "duplicate behavior",
			opts: greenGate(now),
			build: func(t *testing.T) map[string]CellReport {
				return mutate(t, "debian-12/7.1.1", func(r *CellReport) {
					r.Behaviors = append(r.Behaviors, r.Behaviors[0])
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyReports(Matrix, tc.build(t), tc.opts); err == nil {
				t.Fatalf("verify accepted %s", tc.name)
			}
		})
	}
}

func TestPinnedBehaviorSetRejectsDeletionAndAddition(t *testing.T) {
	wantCounts := map[string]int{v711: 25, v6210: 22}
	for _, version := range RequiredVersions {
		ids := behaviorIDsOf(BehaviorsForVersion(version))
		if err := compareBehaviorIDs(version, ids); err != nil {
			t.Fatalf("%s table does not match the pinned set: %v", version, err)
		}
		if len(ids) != wantCounts[version] {
			t.Fatalf("%s has %d behaviors, want %d", version, len(ids), wantCounts[version])
		}
		if err := compareBehaviorIDs(version, ids[1:]); err == nil {
			t.Fatalf("%s accepted a deleted behavior", version)
		}
		extra := append(append([]string(nil), ids...), "not-a-required-behavior")
		if err := compareBehaviorIDs(version, extra); err == nil {
			t.Fatalf("%s accepted an extra behavior", version)
		}
		duplicate := append(append([]string(nil), ids...), ids[0])
		if err := compareBehaviorIDs(version, duplicate); err == nil {
			t.Fatalf("%s accepted a duplicate behavior", version)
		}
	}
	if err := compareBehaviorIDs("9.9", nil); err == nil {
		t.Fatal("compareBehaviorIDs accepted an unpinned version")
	}
}

func behaviorIDsOf(behaviors []Behavior) []string {
	ids := make([]string, 0, len(behaviors))
	for _, behavior := range behaviors {
		ids = append(ids, behavior.ID)
	}
	return ids
}

func TestWriteAndLoadReportRoundTrip(t *testing.T) {
	now := gateNow(t)
	report := greenReport(Matrix[0], now, testRunID)
	dir := t.TempDir()
	if err := writeReport(dir, report); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := loadReports(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded[Matrix[0].ID].Cell != Matrix[0] {
		t.Fatalf("loaded cell = %+v, want %+v", loaded[Matrix[0].ID].Cell, Matrix[0])
	}
	if loaded[Matrix[0].ID].HostOS != Matrix[0].GOOS {
		t.Fatalf("loaded host = %s/%s", loaded[Matrix[0].ID].HostOS, loaded[Matrix[0].ID].HostArch)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReports(dir); err == nil {
		t.Fatal("loadReports accepted invalid JSON")
	}
}

func TestLoadReportsRejectsDuplicateCells(t *testing.T) {
	now := gateNow(t)
	report := greenReport(Matrix[0], now, testRunID)
	dir := t.TempDir()
	if err := writeReport(dir, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(reportPath(dir, Matrix[0]))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "copy.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReports(dir); err == nil {
		t.Fatal("loadReports accepted two reports for one cell")
	}
}

func TestBehaviorIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, behavior := range Behaviors {
		if behavior.ID == "" || behavior.Title == "" {
			t.Fatalf("behavior %+v has an empty ID or title", behavior)
		}
		if seen[behavior.ID] {
			t.Fatalf("duplicate behavior ID %q", behavior.ID)
		}
		seen[behavior.ID] = true
		if len(behavior.Versions) == 0 {
			t.Fatalf("behavior %q has no required versions", behavior.ID)
		}
	}
}

func TestEveryRequiredBehaviorHasARealServerTest(t *testing.T) {
	for _, behavior := range Behaviors {
		for _, version := range behavior.Versions {
			refs := RefsForVersion(behavior, version)
			hasLab := false
			for _, ref := range refs {
				if ref.Suite == suiteLab {
					hasLab = true
				}
			}
			if !hasLab {
				t.Fatalf("behavior %s/%s has no real-server test", behavior.ID, version)
			}
		}
	}
}

// composeServiceBases extracts the BASE build argument for every service that
// declares one.
func composeServiceBases(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	servicePattern := regexp.MustCompile(`^  ([A-Za-z0-9_.-]+):\s*$`)
	basePattern := regexp.MustCompile(`^\s*BASE:\s*"([^"]+)"\s*$`)
	bases := make(map[string]string)
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		if match := servicePattern.FindStringSubmatch(line); match != nil {
			current = match[1]
			continue
		}
		if match := basePattern.FindStringSubmatch(line); match != nil {
			if current == "" {
				t.Fatalf("BASE outside a service: %q", line)
			}
			bases[current] = match[1]
		}
	}
	return bases
}

func TestComposePinsEveryDockerCellWithFullDigests(t *testing.T) {
	bases := composeServiceBases(t, filepath.Join("..", "..", "..", "adapters", "et", "lab", "compose.yaml"))
	for _, cell := range Matrix {
		if cell.Native {
			continue
		}
		base, ok := bases[cell.Service]
		if !ok {
			t.Fatalf("compose lacks a BASE image for service %q", cell.Service)
		}
		at := strings.LastIndex(base, "@")
		if at < 0 {
			t.Fatalf("service %s base image %q is not pinned by digest", cell.Service, base)
		}
		digest := base[at+1:]
		if !imageDigestPattern.MatchString(digest) {
			t.Fatalf("service %s digest %q is not a full sha256 digest", cell.Service, digest)
		}
		if digest != cell.BaseImageDigest {
			t.Fatalf("service %s digest %q, matrix pins %q", cell.Service, digest, cell.BaseImageDigest)
		}
	}
	for service, base := range bases {
		at := strings.LastIndex(base, "@")
		if at < 0 || !imageDigestPattern.MatchString(base[at+1:]) {
			t.Fatalf("service %s base image %q is not pinned to a full digest", service, base)
		}
	}
}

// TestRuntimeBaseImageDescription covers the predicate used by
// startDockerCell's runtime check: the running image's description must name
// the matrix base digest, not a digest read from a possibly overridden
// compose file. The compose ps/docker inspect wiring is exercised by every
// real Docker cell.
func TestRuntimeBaseImageDescription(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	if err := checkBaseImageDescription("stock etserver 7.1.1 on debian:12-slim@"+digest, digest); err != nil {
		t.Fatalf("matching description rejected: %v", err)
	}
	if err := checkBaseImageDescription("stock etserver 7.1.1 on debian:12-slim@"+other, digest); err == nil {
		t.Fatal("description with another base digest accepted")
	}
	if err := checkBaseImageDescription("", digest); err == nil {
		t.Fatal("empty description accepted")
	}
	if err := checkBaseImageDescription("debian:12-slim@"+digest, other); err == nil {
		t.Fatal("wrong expected digest accepted")
	}
}

func TestMergeEnvReplacesWithoutDuplicates(t *testing.T) {
	base := []string{"A=1", "B=2", "NATCATTY_ET_LAB=old"}
	merged := mergeEnv(base, []string{"NATCATTY_ET_LAB=1", "C=3", "EMPTY="})
	joined := strings.Join(merged, "\n")
	if strings.Contains(joined, "NATCATTY_ET_LAB=old") || strings.Count(joined, "NATCATTY_ET_LAB=") != 1 {
		t.Fatalf("merged env = %v", merged)
	}
	if !strings.Contains(joined, "A=1") || !strings.Contains(joined, "C=3") {
		t.Fatalf("merged env = %v", merged)
	}
	for _, entry := range merged {
		if strings.HasPrefix(entry, "EMPTY=") {
			t.Fatalf("empty override was not removed: %v", merged)
		}
	}
}

// TestSuiteEnvAllowlistExcludesAmbientTestControls proves an ambient shell
// cannot enable golden rewriting, point the suites at another lab endpoint, or
// inject arbitrary test-update variables.
func TestSuiteEnvAllowlistExcludesAmbientTestControls(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HOME=/home/tester",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"NATCATTY_ET_LAB=1",
		"NATCATTY_ET_LAB_711=10.0.0.1:2222",
		"NATCATTY_ET_LAB_VERSION=6.2.10",
		"NATCATTY_ET_UPDATE_GOLDEN=1",
		"UPDATE_GOLDEN=1",
		"UNRELATED_SETTING=1",
	}
	env := suiteEnv(base, []string{"NATCATTY_ET_LAB=1", "NATCATTY_ET_LAB_VERSION=7.1.1"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"PATH=/bin", "HOME=/home/tester", "SSH_AUTH_SOCK=/tmp/agent.sock",
		"NATCATTY_ET_LAB=1", "NATCATTY_ET_LAB_VERSION=7.1.1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("suite env lacks %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{
		"NATCATTY_ET_UPDATE_GOLDEN", "UPDATE_GOLDEN", "NATCATTY_ET_LAB_711",
		"UNRELATED_SETTING",
	} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("suite env inherited %q:\n%s", unwanted, joined)
		}
	}
	if env == nil {
		t.Fatal("suiteEnv returned nil, which would inherit the parent environment")
	}
}

func TestRequireCleanWorktree(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "tracked.txt"), "clean\n")
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "init")

	outDir := filepath.Join(dir, "build", "et-parity")
	if err := requireCleanWorktree(dir, outDir); err != nil {
		t.Fatalf("clean tree rejected: %v", err)
	}

	// Reports and logs in the output directory must not make the tree dirty
	// for the next cell, even when the directory is not gitignored.
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outDir, "debian-12_7.1.1.json"), "{}\n")
	if err := requireCleanWorktree(dir, outDir); err != nil {
		t.Fatalf("output directory dirtied the check: %v", err)
	}

	writeFile(t, filepath.Join(dir, "tracked.txt"), "dirty\n")
	if err := requireCleanWorktree(dir, outDir); err == nil {
		t.Fatal("modified tracked file was accepted")
	}
	runGit(t, dir, "checkout", "--", "tracked.txt")

	writeFile(t, filepath.Join(dir, "untracked.txt"), "new\n")
	if err := requireCleanWorktree(dir, outDir); err == nil {
		t.Fatal("untracked source file was accepted")
	}
}

// TestRequireCleanWorktreeRefusesTrackedOutput proves -out is only exempt for
// untracked report artifacts. A tracked file under -out is source or a
// committed fixture; letting it be dirty would produce a report that cannot
// prove the tree it names.
func TestRequireCleanWorktreeRefusesTrackedOutput(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	outDir := filepath.Join(dir, "adapters", "et")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "tracked.txt"), "clean\n")
	writeFile(t, filepath.Join(outDir, "tracked-report.json"), "{}\n")
	runGit(t, dir, "add", "tracked.txt", "adapters/et/tracked-report.json")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "init")

	// A clean tree with tracked files under -out is allowed.
	if err := requireCleanWorktree(dir, outDir); err != nil {
		t.Fatalf("clean tree with tracked output rejected: %v", err)
	}

	// Modifying a tracked file under -out must not be exempted.
	writeFile(t, filepath.Join(outDir, "tracked-report.json"), "{\"dirty\":true}\n")
	if err := requireCleanWorktree(dir, outDir); err == nil {
		t.Fatal("dirty tracked file under -out was exempted")
	}
	runGit(t, dir, "restore", "--", "adapters/et/tracked-report.json")

	// A staged new file under -out is tracked index state, not a report
	// artifact, and must not be exempted either.
	writeFile(t, filepath.Join(outDir, "staged-report.json"), "{}\n")
	runGit(t, dir, "add", "adapters/et/staged-report.json")
	if err := requireCleanWorktree(dir, outDir); err == nil {
		t.Fatal("staged file under -out was exempted")
	}
	runGit(t, dir, "rm", "--cached", "-q", "adapters/et/staged-report.json")
	if err := os.Remove(filepath.Join(outDir, "staged-report.json")); err != nil {
		t.Fatal(err)
	}

	// An untracked report written under -out is allowed.
	writeFile(t, filepath.Join(outDir, "report.json"), "{}\n")
	if err := requireCleanWorktree(dir, outDir); err != nil {
		t.Fatalf("untracked report output rejected: %v", err)
	}
}

// TestRequireCleanWorktreeAllowsGitignoredOutput proves the default CI report
// directory, which is gitignored, stays usable. Git reports no status entry
// for ignored files, which is exactly the exemption this test covers.
func TestRequireCleanWorktreeAllowsGitignoredOutput(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, ".gitignore"), "build/\n")
	runGit(t, dir, "add", ".gitignore")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "init")

	outDir := filepath.Join(dir, "build", "et-parity")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outDir, "debian-12_7.1.1.json"), "{}\n")
	if err := requireCleanWorktree(dir, outDir); err != nil {
		t.Fatalf("ignored report output rejected: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRequiredFuzzTargetsExistInSourceAndAreUnique keeps the compiled-in
// table honest. The workflow is no longer parsed: receipts are the evidence,
// and verify checks each receipt's target against this table and the source.
func TestRequiredFuzzTargetsExistInSourceAndAreUnique(t *testing.T) {
	if len(requiredFuzzTargets) != 4 {
		t.Fatalf("required fuzz targets = %d, want 4", len(requiredFuzzTargets))
	}
	root := repoRootAbs(t)
	seen := make(map[string]bool, len(requiredFuzzTargets))
	for _, target := range requiredFuzzTargets {
		if seen[target.key()] {
			t.Fatalf("duplicate required fuzz target %s", target.key())
		}
		seen[target.key()] = true
		if err := targetDefined(root, target); err != nil {
			t.Fatalf("required fuzz target: %v", err)
		}
	}
}

func TestResolveFuzzTargets(t *testing.T) {
	first := requiredFuzzTargets[0]
	targets, err := resolveFuzzTargets([]string{first.key()})
	if err != nil || len(targets) != 1 || targets[0] != first {
		t.Fatalf("resolve = %+v, %v", targets, err)
	}
	for _, spec := range []string{
		"", "FuzzReadFrame", "./adapters/et/internal/etgo/wire:", ":FuzzReadFrame",
		"./not/a/required/pkg:FuzzReadFrame",
	} {
		if _, err := resolveFuzzTargets([]string{spec}); err == nil {
			t.Fatalf("target spec %q accepted", spec)
		}
	}
	if _, err := resolveFuzzTargets(nil); err == nil {
		t.Fatal("empty target selection accepted")
	}
	if _, err := resolveFuzzTargets([]string{first.key(), first.key()}); err == nil {
		t.Fatal("duplicate target selection accepted")
	}
}

func TestRequiredFuzzTargetsAreBehaviorChecks(t *testing.T) {
	checked := make(map[string]bool)
	for _, behavior := range Behaviors {
		for _, ref := range behavior.Refs {
			checked[ref.Package+"\x00"+ref.Name] = true
		}
	}
	for _, target := range requiredFuzzTargets {
		importPath := moduleImportPath + "/" + strings.TrimPrefix(target.Package, "./")
		if !checked[importPath+"\x00"+target.Name] {
			t.Fatalf("fuzz target %s/%s is not a required behavior check", target.Package, target.Name)
		}
	}
}

// fuzzSuccessOutput and fuzzFailureOutput mirror the real `go test -fuzz`
// transcript shape. Receipt verification recomputes status from these markers
// instead of trusting the receipt's status field.
func fuzzSuccessOutput() string {
	return "fuzz: elapsed: 0s, gathering baseline coverage: 0/4 completed\n" +
		"fuzz: elapsed: 0s, gathering baseline coverage: 4/4 completed, now fuzzing with 8 workers\n" +
		"fuzz: elapsed: 1s, execs: 42 (42/sec), new interesting: 0 (total: 4)\n" +
		"PASS\n" +
		"ok  \tpkg\t1.2s\n"
}

func fuzzFailureOutput() string {
	return "--- FAIL: FuzzA (0.00s)\n" +
		"    fuzz_test.go:1: crash\n" +
		"FAIL\n" +
		"exit status 1\n" +
		"FAIL\tpkg\t0.1s\n"
}

func repoRootAbs(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// fuzzTestRepo builds a tiny git repo with one real fuzz target so receipt
// generation has deterministic commit/tree values and a source definition.
func fuzzTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	pkgDir := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "fuzz_test.go"),
		"package pkg\n\nimport \"testing\"\n\nfunc FuzzA(f *testing.F) {}\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test",
		"commit", "-q", "-m", "init")
	return dir
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func fakeFuzzRunner(calls *[][]string, listOutput, fuzzOutput string, listErr, fuzzErr error) goTestRunner {
	return func(_ context.Context, _ string, args []string) ([]byte, error) {
		*calls = append(*calls, append([]string(nil), args...))
		if hasArg(args, "-list") {
			return []byte(listOutput), listErr
		}
		return []byte(fuzzOutput), fuzzErr
	}
}

func steppingClock(start time.Time) func() time.Time {
	now := start
	return func() time.Time {
		now = now.Add(2 * time.Second)
		return now
	}
}

func cloneFuzzReceiptFiles(in []fuzzReceiptFile) []fuzzReceiptFile {
	out := make([]fuzzReceiptFile, len(in))
	for i, file := range in {
		out[i] = file
		out[i].Receipt.Command = append([]string(nil), file.Receipt.Command...)
	}
	return out
}

// greenFuzzReceiptFiles builds a passing receipt for every target. The
// timestamps are five seconds apart and FuzzTime is five seconds, so the
// recomputed elapsed equals the fuzzing window.
func greenFuzzReceiptFiles(now time.Time, runID, commit, tree string, targets []fuzzTarget, fuzzTime time.Duration) []fuzzReceiptFile {
	files := make([]fuzzReceiptFile, 0, len(targets))
	for _, target := range targets {
		receipt := fuzzReceipt{
			Schema:    fuzzReceiptSchema,
			Target:    target.key(),
			Package:   target.Package,
			Name:      target.Name,
			Status:    "pass",
			Command:   fuzzRunArgs(target, fuzzOptions{fuzzTime: fuzzTime, timeout: time.Minute}),
			Output:    fuzzSuccessOutput(),
			StartedAt: now.Add(-10 * time.Second),
			EndedAt:   now.Add(-5 * time.Second),
			FuzzTime:  fuzzTime,
			HostOS:    "linux",
			HostArch:  "amd64",
			Commit:    commit,
			TreeHash:  tree,
			RunID:     runID,
		}
		files = append(files, fuzzReceiptFile{FileName: "fuzz-" + target.Name + ".json", Receipt: receipt})
	}
	return files
}

func TestRunFuzzTargetsWritesPassingReceipt(t *testing.T) {
	root := fuzzTestRepo(t)
	out := t.TempDir()
	var calls [][]string
	opts := fuzzOptions{
		root: root, outDir: out, fuzzTime: minFuzzTime, timeout: time.Minute,
		runner: fakeFuzzRunner(&calls, "FuzzA\nok  \tpkg\t0.1s\n", fuzzSuccessOutput(), nil, nil),
		now:    steppingClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)),
	}
	target := fuzzTarget{Package: "./pkg", Name: "FuzzA"}
	receipts, err := runFuzzTargets(context.Background(), []fuzzTarget{target}, opts)
	if err != nil {
		t.Fatalf("runFuzzTargets: %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("receipts = %d, want 1", len(receipts))
	}
	receipt := receipts[0]
	if receipt.Schema != fuzzReceiptSchema || receipt.Target != target.key() ||
		receipt.Package != target.Package || receipt.Name != target.Name {
		t.Fatalf("receipt identity = %+v", receipt)
	}
	if receipt.Status != "pass" {
		t.Fatalf("receipt status = %q", receipt.Status)
	}
	if receipt.HostOS != runtime.GOOS || receipt.HostArch != runtime.GOARCH {
		t.Fatalf("receipt host = %s/%s", receipt.HostOS, receipt.HostArch)
	}
	if receipt.Commit != gitCommit(root) || receipt.TreeHash != gitTreeHash(root) {
		t.Fatalf("receipt commit/tree = %s/%s", receipt.Commit, receipt.TreeHash)
	}
	if elapsed := receipt.EndedAt.Sub(receipt.StartedAt); elapsed < receipt.FuzzTime {
		t.Fatalf("elapsed %s < fuzztime %s", elapsed, receipt.FuzzTime)
	}
	if len(calls) != 2 {
		t.Fatalf("go test calls = %d, want 2 (list then fuzz): %v", len(calls), calls)
	}
	wantList := []string{"test", "./pkg", "-list", "^FuzzA$"}
	if strings.Join(calls[0], " ") != strings.Join(wantList, " ") {
		t.Fatalf("list call = %v, want %v", calls[0], wantList)
	}
	wantFuzz := fuzzRunArgs(target, fuzzOptions{fuzzTime: minFuzzTime, timeout: time.Minute})
	if strings.Join(calls[1], " ") != strings.Join(wantFuzz, " ") {
		t.Fatalf("fuzz call = %v, want %v", calls[1], wantFuzz)
	}
	if !hasArg(calls[1], "-fuzztime") || !hasArg(calls[1], "^FuzzA$") || !hasArg(calls[1], "^$") {
		t.Fatalf("fuzz call lacks bounded fuzz flags: %v", calls[1])
	}
	data, err := os.ReadFile(fuzzReceiptPath(out, "FuzzA"))
	if err != nil {
		t.Fatalf("receipt file: %v", err)
	}
	if !strings.Contains(string(data), `"target": "./pkg:FuzzA"`) {
		t.Fatalf("receipt file does not carry the target: %s", data)
	}
}

// TestRunFuzzTargetsWritesFailureReceipts covers every failure path that must
// still leave evidence: the source check, exact-match preflight, and the fuzz
// invocation itself. A missing receipt is how a disabled workflow step
// manifests, so these receipt states must be observable.
func TestRunFuzzTargetsWritesFailureReceipts(t *testing.T) {
	root := fuzzTestRepo(t)
	target := fuzzTarget{Package: "./pkg", Name: "FuzzA"}
	cases := []struct {
		name        string
		target      fuzzTarget
		listOutput  string
		listErr     error
		fuzzOutput  string
		fuzzErr     error
		wantCalls   int
		wantMessage string
	}{
		{
			name: "preflight exact match", target: target, listOutput: "FuzzAExtra\n",
			wantCalls: 1, wantMessage: "exact",
		},
		{
			name: "preflight error", target: target, listErr: errors.New("list boom"),
			wantCalls: 1, wantMessage: "preflight",
		},
		{
			name: "fuzz execution failure", target: target, listOutput: "FuzzA\nok\n",
			fuzzOutput: fuzzFailureOutput(), fuzzErr: errors.New("exit status 1"),
			wantCalls: 2, wantMessage: "exit status 1",
		},
		{
			name: "fuzz output without evidence", target: target, listOutput: "FuzzA\nok\n",
			fuzzOutput: "FuzzA\nok  \tpkg\t0.1s\n",
			wantCalls:  2, wantMessage: "fuzzing progress",
		},
		{
			name: "target missing from source", target: fuzzTarget{Package: "./pkg", Name: "FuzzGone"},
			wantCalls: 0, wantMessage: "no `func FuzzGone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			var calls [][]string
			opts := fuzzOptions{
				root: root, outDir: out, fuzzTime: minFuzzTime, timeout: time.Minute,
				runner: fakeFuzzRunner(&calls, tc.listOutput, tc.fuzzOutput, tc.listErr, tc.fuzzErr),
			}
			receipts, err := runFuzzTargets(context.Background(), []fuzzTarget{tc.target}, opts)
			if err == nil {
				t.Fatal("failing fuzz target accepted")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("error %q does not mention %q", err, tc.wantMessage)
			}
			if len(calls) != tc.wantCalls {
				t.Fatalf("go test calls = %d, want %d: %v", len(calls), tc.wantCalls, calls)
			}
			if len(receipts) != 1 {
				t.Fatalf("receipts = %d, want 1", len(receipts))
			}
			if receipts[0].Status != "fail" {
				t.Fatalf("receipt status = %q, want fail", receipts[0].Status)
			}
			if strings.TrimSpace(receipts[0].Output) == "" {
				t.Fatal("failure receipt has no captured output")
			}
			if _, err := os.Stat(fuzzReceiptPath(out, tc.target.Name)); err != nil {
				t.Fatalf("failure receipt was not written: %v", err)
			}
		})
	}
}

func TestRunFuzzTargetsRejectsBelowMinimum(t *testing.T) {
	root := fuzzTestRepo(t)
	_, err := runFuzzTargets(context.Background(),
		[]fuzzTarget{{Package: "./pkg", Name: "FuzzA"}},
		fuzzOptions{root: root, outDir: t.TempDir(), fuzzTime: 500 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "minimum") {
		t.Fatalf("below-minimum fuzztime = %v, want minimum error", err)
	}
}

func TestFuzzReceiptOutputIsBoundedAndWrittenAtomically(t *testing.T) {
	root := fuzzTestRepo(t)
	out := t.TempDir()
	var calls [][]string
	// Keep the evidence markers inside the retained head, then exceed the
	// bound. Cell reports truncate the same way.
	output := fuzzSuccessOutput() + strings.Repeat("x", fuzzOutputLimit)
	opts := fuzzOptions{
		root: root, outDir: out, fuzzTime: minFuzzTime, timeout: time.Minute,
		runner: fakeFuzzRunner(&calls, "FuzzA\n", output, nil, nil),
	}
	receipts, err := runFuzzTargets(context.Background(),
		[]fuzzTarget{{Package: "./pkg", Name: "FuzzA"}}, opts)
	if err != nil {
		t.Fatalf("runFuzzTargets: %v", err)
	}
	if len(receipts[0].Output) > fuzzOutputLimit+len("\n... truncated ...") {
		t.Fatalf("receipt output length = %d, want <= %d", len(receipts[0].Output), fuzzOutputLimit)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "fuzz-FuzzA.json" {
		t.Fatalf("receipt directory = %v, want exactly fuzz-FuzzA.json", entries)
	}
}

func TestVerifyFuzzReceiptsAcceptsGreenSet(t *testing.T) {
	now := gateNow(t)
	root := repoRootAbs(t)
	files := greenFuzzReceiptFiles(now, testRunID, testCommit, testTree, requiredFuzzTargets, 5*time.Second)
	opts := gateOptions{
		Commit: testCommit, TreeHash: testTree, RunID: testRunID,
		MaxAge: defaultMaxReportAge, Now: now,
	}
	if err := verifyFuzzReceipts(root, requiredFuzzTargets, files, opts); err != nil {
		t.Fatalf("green fuzz receipts rejected: %v", err)
	}
}

// TestVerifyFuzzReceiptsFailsClosed covers the verifier's required failure
// modes: missing, extra, duplicate, wrong name/package, failed status,
// missing/forged evidence, stale/future timestamps, wrong commit/tree/run,
// too-short fuzztime/elapsed, and a target that disappeared from source.
func TestVerifyFuzzReceiptsFailsClosed(t *testing.T) {
	now := gateNow(t)
	root := repoRootAbs(t)
	greenOpts := gateOptions{
		Commit: testCommit, TreeHash: testTree, RunID: testRunID,
		MaxAge: defaultMaxReportAge, Now: now,
	}
	greenFiles := func() []fuzzReceiptFile {
		return greenFuzzReceiptFiles(now, testRunID, testCommit, testTree, requiredFuzzTargets, 5*time.Second)
	}
	mutate := func(index int, change func(*fuzzReceipt)) []fuzzReceiptFile {
		files := cloneFuzzReceiptFiles(greenFiles())
		change(&files[index].Receipt)
		return files
	}
	mutateFile := func(index int, change func(*fuzzReceiptFile)) []fuzzReceiptFile {
		files := cloneFuzzReceiptFiles(greenFiles())
		change(&files[index])
		return files
	}
	extraTarget := fuzzTarget{Package: requiredFuzzTargets[0].Package, Name: "FuzzNotRequired"}
	extra := fuzzReceiptFile{
		FileName: "fuzz-FuzzNotRequired.json",
		Receipt: fuzzReceipt{
			Schema: fuzzReceiptSchema, Target: extraTarget.key(),
			Package: extraTarget.Package, Name: extraTarget.Name, Status: "pass",
			Command:   fuzzRunArgs(extraTarget, fuzzOptions{fuzzTime: 5 * time.Second, timeout: time.Minute}),
			Output:    fuzzSuccessOutput(),
			StartedAt: now.Add(-10 * time.Second), EndedAt: now.Add(-5 * time.Second),
			FuzzTime: 5 * time.Second, HostOS: "linux", HostArch: "amd64",
			Commit: testCommit, TreeHash: testTree, RunID: testRunID,
		},
	}
	withoutRun := greenOpts
	withoutRun.RunID = ""

	cases := []struct {
		name        string
		root        string
		required    []fuzzTarget
		opts        gateOptions
		build       func(*testing.T) []fuzzReceiptFile
		wantMessage string
	}{
		{
			name: "missing receipt", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile { return greenFiles()[:3] },
		},
		{
			name: "no receipts at all", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile { return nil },
		},
		{
			name: "duplicate receipt", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return append(greenFiles(), fuzzReceiptFile{
					FileName: "fuzz-copy.json", Receipt: greenFiles()[0].Receipt,
				})
			},
			wantMessage: "duplicate",
		},
		{
			name: "extra receipt", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build:       func(t *testing.T) []fuzzReceiptFile { return append(greenFiles(), extra) },
			wantMessage: "not a required fuzz target",
		},
		{
			name: "wrong name", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Name = "FuzzOther" })
			},
		},
		{
			name: "wrong package", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Package = "./adapters/et/internal/etgo/etcp" })
			},
		},
		{
			name: "wrong target field", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Target = "./pkg:FuzzWrong" })
			},
		},
		{
			name: "renamed receipt file", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutateFile(0, func(f *fuzzReceiptFile) { f.FileName = "fuzz-copy.json" })
			},
		},
		{
			name: "failed status", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Status = "fail" })
			},
		},
		{
			name: "empty output", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Output = "   \n" })
			},
		},
		{
			name: "output without fuzz evidence", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Output = "FuzzReadFrame\nok  \tpkg\t0.1s\n" })
			},
			wantMessage: "fuzzing progress",
		},
		{
			name: "pass status over failing output", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Output = fuzzFailureOutput() })
			},
			wantMessage: "records a failure",
		},
		{
			name: "wrong schema", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Schema = 99 })
			},
		},
		{
			name: "commit mismatch", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Commit = testTree })
			},
			wantMessage: "commit",
		},
		{
			name: "tree mismatch", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.TreeHash = testCommit })
			},
			wantMessage: "tree",
		},
		{
			name: "run id mismatch", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.RunID = "9999999999" })
			},
			wantMessage: "CI run",
		},
		{
			name: "missing run id", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.RunID = "" })
			},
			wantMessage: "not bound to CI run",
		},
		{
			name: "run-bound receipts verified without a run", root: root, required: requiredFuzzTargets, opts: withoutRun,
			build: func(t *testing.T) []fuzzReceiptFile { return greenFiles() },
		},
		{
			name: "stale receipt", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.StartedAt = now.Add(-defaultMaxReportAge - time.Minute)
					r.EndedAt = now.Add(-defaultMaxReportAge - time.Minute)
				})
			},
			wantMessage: "stale",
		},
		{
			name: "future receipt", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.StartedAt = now.Add(time.Hour)
					r.EndedAt = now.Add(time.Hour)
				})
			},
			wantMessage: "future",
		},
		{
			name: "missing start time", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.StartedAt = time.Time{} })
			},
		},
		{
			name: "ends before it starts", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.StartedAt = now.Add(-time.Second)
					r.EndedAt = now.Add(-2 * time.Second)
				})
			},
			wantMessage: "before it starts",
		},
		{
			name: "fuzztime below minimum", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.FuzzTime = 500 * time.Millisecond
					r.Command = fuzzRunArgs(requiredFuzzTargets[0],
						fuzzOptions{fuzzTime: 500 * time.Millisecond, timeout: time.Minute})
				})
			},
			wantMessage: "minimum",
		},
		{
			name: "elapsed shorter than fuzztime", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.StartedAt = now.Add(-2 * time.Second)
					r.EndedAt = now.Add(-time.Second)
				})
			},
			wantMessage: "shorter than fuzztime",
		},
		{
			name: "missing host provenance", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.HostOS = "" })
			},
			wantMessage: "host platform",
		},
		{
			name: "command names another package", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.Command = fuzzRunArgs(requiredFuzzTargets[1],
						fuzzOptions{fuzzTime: 5 * time.Second, timeout: time.Minute})
				})
			},
			wantMessage: "does not run",
		},
		{
			name: "missing command", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) { r.Command = nil })
			},
			wantMessage: "does not run",
		},
		{
			name: "command fuzztime mismatch", root: root, required: requiredFuzzTargets, opts: greenOpts,
			build: func(t *testing.T) []fuzzReceiptFile {
				return mutate(0, func(r *fuzzReceipt) {
					r.Command = fuzzRunArgs(requiredFuzzTargets[0],
						fuzzOptions{fuzzTime: 10 * time.Second, timeout: time.Minute})
				})
			},
			wantMessage: "fuzztime",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyFuzzReceipts(tc.root, tc.required, tc.build(t), tc.opts)
			if err == nil {
				t.Fatalf("verify accepted %s", tc.name)
			}
			if tc.wantMessage != "" && !strings.Contains(err.Error(), tc.wantMessage) {
				t.Fatalf("error %q does not mention %q", err, tc.wantMessage)
			}
		})
	}

	// A receipt whose target disappeared from source fails even though every
	// artifact field is otherwise green.
	missingRoot := t.TempDir()
	pkgDir := filepath.Join(missingRoot, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "other_test.go"),
		"package pkg\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) {}\n")
	required := []fuzzTarget{{Package: "./pkg", Name: "FuzzA"}}
	missingFiles := greenFuzzReceiptFiles(now, testRunID, testCommit, testTree, required, 5*time.Second)
	err := verifyFuzzReceipts(missingRoot, required, missingFiles, greenOpts)
	if err == nil || !strings.Contains(err.Error(), "no `func FuzzA") {
		t.Fatalf("target missing from source = %v", err)
	}
}

// TestVerifyGateRequiresFuzzReceiptsEvenWhenCellsPass proves the fail-closed
// coupling: green cell reports and a would-be successful needs.fuzz do not
// matter. Without the fuzz artifact, verify fails.
func TestVerifyGateRequiresFuzzReceiptsEvenWhenCellsPass(t *testing.T) {
	now := gateNow(t)
	root := repoRootAbs(t)
	dir := t.TempDir()
	for _, cell := range Matrix {
		if err := writeReport(dir, greenReport(cell, now, testRunID)); err != nil {
			t.Fatal(err)
		}
	}
	reports, err := loadReports(dir)
	if err != nil {
		t.Fatalf("loadReports: %v", err)
	}
	receipts, err := loadFuzzReceipts(dir)
	if err != nil {
		t.Fatalf("loadFuzzReceipts: %v", err)
	}
	if len(receipts) != 0 {
		t.Fatalf("receipts = %d, want 0", len(receipts))
	}
	opts := greenGate(now)
	if err := verifyReports(Matrix, reports, opts); err != nil {
		t.Fatalf("cell reports should be green: %v", err)
	}
	err = verifyGate(root, reports, receipts, opts)
	if err == nil || !strings.Contains(err.Error(), "no receipt") {
		t.Fatalf("gate with a green matrix but no fuzz receipts = %v, want missing-receipt failure", err)
	}
}

func TestLoadReportsIgnoresFuzzReceipts(t *testing.T) {
	now := gateNow(t)
	dir := t.TempDir()
	report := greenReport(Matrix[0], now, testRunID)
	if err := writeReport(dir, report); err != nil {
		t.Fatal(err)
	}
	receipt := greenFuzzReceiptFiles(now, testRunID, testCommit, testTree,
		requiredFuzzTargets[:1], 5*time.Second)[0]
	if err := writeFuzzReceipt(dir, receipt.Receipt); err != nil {
		t.Fatal(err)
	}
	// summary.json shares the directory and is not a cell report either.
	if err := writeSummary(dir, summaryFile{Schema: summarySchema, Status: "fail"}); err != nil {
		t.Fatal(err)
	}
	reports, err := loadReports(dir)
	if err != nil {
		t.Fatalf("loadReports with a fuzz receipt and summary: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	receipts, err := loadFuzzReceipts(dir)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("fuzz receipts = %d, %v", len(receipts), err)
	}
	writeFile(t, filepath.Join(dir, "fuzz-bad.json"), "{")
	if _, err := loadFuzzReceipts(dir); err == nil {
		t.Fatal("loadFuzzReceipts accepted invalid JSON")
	}
}

// TestFuzzCommandEndToEnd runs the real four targets with the documented
// minimum fuzztime, writes receipts, then verifies them. It is intentionally
// not short-circuited: the gate's artifact path must work, not just its
// injected-runner unit tests.
func TestFuzzCommandEndToEnd(t *testing.T) {
	root := repoRootAbs(t)
	dir := t.TempDir()
	receipts, err := runFuzzTargets(context.Background(), requiredFuzzTargets, fuzzOptions{
		root: root, outDir: dir, fuzzTime: minFuzzTime, timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("real fuzz run: %v", err)
	}
	if len(receipts) != len(requiredFuzzTargets) {
		t.Fatalf("receipts = %d, want %d", len(receipts), len(requiredFuzzTargets))
	}
	loaded, err := loadFuzzReceipts(dir)
	if err != nil {
		t.Fatalf("load receipts: %v", err)
	}
	opts := gateOptions{
		Commit:   gitCommit(root),
		TreeHash: gitTreeHash(root),
		RunID:    strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")),
		// Give the verifier a little headroom over the receipts written just
		// now; the freshness rule still applies.
		Now: time.Now().UTC().Add(time.Minute),
	}
	if err := verifyFuzzReceipts(root, requiredFuzzTargets, loaded, opts); err != nil {
		t.Fatalf("verify real receipts: %v", err)
	}
}

// TestFuzzTargetDefinitionIgnoresComments proves targetDefined checks compiled
// declarations, not text that happens to contain a function signature.
func TestFuzzTargetDefinitionIgnoresComments(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "commented_test.go"), `package pkg

import "testing"

// func FuzzLineComment(f *testing.F) {}
/*
func FuzzBlockComment(f *testing.F) {}
*/
`)
	for _, name := range []string{"FuzzLineComment", "FuzzBlockComment"} {
		target := fuzzTarget{Package: "./pkg", Name: name}
		if err := targetDefined(root, target); err == nil {
			t.Fatalf("commented-out definition of %s was accepted", name)
		}
	}

	writeFile(t, filepath.Join(pkgDir, "real_test.go"), `package pkg

import "testing"

func FuzzReal(f *testing.F) {}
`)
	if err := targetDefined(root, fuzzTarget{Package: "./pkg", Name: "FuzzReal"}); err != nil {
		t.Fatalf("real definition rejected: %v", err)
	}
}

// TestLabPasswordIsConsistent stops the lab password from drifting between
// the image setup, the gate environment, and the macOS runner job. A mismatch
// passes image builds and then fails every auth test for an unhelpful reason.
func TestLabPasswordIsConsistent(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	setup, err := os.ReadFile(filepath.Join(root, "adapters", "et", "lab", "setup-lab.sh"))
	if err != nil {
		t.Fatalf("read setup-lab.sh: %v", err)
	}
	match := regexp.MustCompile(`echo 'tester:([^']+)' \| chpasswd`).FindSubmatch(setup)
	if match == nil {
		t.Fatal("setup-lab.sh has no tester password")
	}
	setupPassword := string(match[1])

	envPassword := ""
	for _, entry := range CellEnv(Matrix[0], "127.0.0.1:2322") {
		if value, ok := strings.CutPrefix(entry, "NATCATTY_ET_LAB_PASSWORD="); ok {
			envPassword = value
		}
	}
	if envPassword == "" || envPassword != setupPassword {
		t.Fatalf("CellEnv password does not match setup-lab.sh")
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "et-parity.yml"))
	if err != nil {
		t.Fatalf("read et-parity workflow: %v", err)
	}
	match = regexp.MustCompile(`NATCATTY_ET_LAB_PASSWORD:\s*(\S+)`).FindSubmatch(workflow)
	if match == nil {
		t.Fatal("et-parity workflow sets no macOS lab password")
	}
	if string(match[1]) != setupPassword {
		t.Fatalf("the macOS job password does not match setup-lab.sh")
	}
}

func TestResolveCellSelection(t *testing.T) {
	cells, err := resolveCellSelection([]string{"macos/7.1.1"}, false)
	if err != nil || len(cells) != 1 || !cells[0].Native {
		t.Fatalf("selection = %+v, %v", cells, err)
	}
	if _, err := resolveCellSelection(nil, true); err != nil {
		t.Fatalf("-all: %v", err)
	}
	if _, err := resolveCellSelection(nil, false); err == nil {
		t.Fatal("empty selection accepted")
	}
	if _, err := resolveCellSelection([]string{"nope"}, false); err == nil {
		t.Fatal("unknown cell accepted")
	}
	if _, err := resolveCellSelection([]string{"macos/7.1.1", "macos/7.1.1"}, false); err == nil {
		t.Fatal("duplicate cell accepted")
	}
}

// TestReportPathCannotCollide keeps two matrix IDs from writing the same file.
func TestReportPathCannotCollide(t *testing.T) {
	seen := make(map[string]string)
	for _, cell := range Matrix {
		path := reportPath("out", cell)
		if other, ok := seen[path]; ok {
			t.Fatalf("%s and %s share report path %s", other, cell.ID, path)
		}
		seen[path] = cell.ID
	}
	if got := reportPath("out", Cell{ID: "a/b:c d"}); !strings.HasSuffix(got, "a_b-c_d.json") {
		t.Fatalf("sanitized report path = %q", got)
	}
}

func TestLoopbackValidation(t *testing.T) {
	for _, address := range []string{"127.0.0.1:1", "localhost:22", "[::1]:65535"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"", "127.0.0.1", "10.0.0.1:22", "example.com:22", "127.0.0.1:0", "127.0.0.1:65536"} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Fatalf("%q accepted", address)
		}
	}
	if host, port, err := net.SplitHostPort("127.0.0.1:2322"); err != nil || host == "" || port != "2322" {
		t.Fatal("test assumption broken")
	}
}

// writeGreenGateArtifacts writes a complete green matrix and the four fuzz
// receipts, bound to the given checkout and run. It is the fixture for the
// summary-generation tests.
func writeGreenGateArtifacts(t *testing.T, dir, commit, tree string, now time.Time, runID string, cells []Cell, targets []fuzzTarget) {
	t.Helper()
	for _, cell := range cells {
		report := greenReport(cell, now, runID)
		report.Commit = commit
		report.TreeHash = tree
		if err := writeReport(dir, report); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range greenFuzzReceiptFiles(now, runID, commit, tree, targets, 5*time.Second) {
		if err := writeFuzzReceipt(dir, file.Receipt); err != nil {
			t.Fatal(err)
		}
	}
}

func readSummary(t *testing.T, dir string) summaryFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("read summary.json: %v", err)
	}
	var summary summaryFile
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("summary.json is not valid JSON: %v", err)
	}
	return summary
}

func summaryCellByID(t *testing.T, summary summaryFile, id string) summaryCell {
	t.Helper()
	for _, cell := range summary.Cells {
		if cell.ID == id {
			return cell
		}
	}
	t.Fatalf("summary has no cell %q", id)
	return summaryCell{}
}

func summaryFuzzByTarget(t *testing.T, summary summaryFile, target string) summaryFuzzTarget {
	t.Helper()
	for _, entry := range summary.FuzzTargets {
		if entry.Target == target {
			return entry
		}
	}
	t.Fatalf("summary has no fuzz target %q", target)
	return summaryFuzzTarget{}
}

// TestVerifyCommandWritesPassingSummary proves the machine-readable verdict is
// written with the checkout commit, tree, run ID, and every per-cell and
// per-fuzz verdict on the pass path. It also proves -require-summary accepts
// the summary this gate just wrote.
func TestVerifyCommandWritesPassingSummary(t *testing.T) {
	root := repoRootAbs(t)
	commit, tree := gitCommit(root), gitTreeHash(root)
	if commit == "unknown" || tree == "unknown" {
		t.Fatalf("test repository has no commit/tree: %s/%s", commit, tree)
	}
	t.Setenv("GITHUB_RUN_ID", testRunID)
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGreenGateArtifacts(t, dir, commit, tree, now, testRunID, Matrix, requiredFuzzTargets)

	if err := run([]string{"verify", "-dir", dir}); err != nil {
		t.Fatalf("green gate: %v", err)
	}
	summary := readSummary(t, dir)
	if summary.Schema != summarySchema {
		t.Fatalf("summary schema = %d, want %d", summary.Schema, summarySchema)
	}
	if summary.Status != "pass" {
		t.Fatalf("summary status = %q, want pass", summary.Status)
	}
	if summary.Commit != commit || summary.TreeHash != tree {
		t.Fatalf("summary commit/tree = %s/%s, want %s/%s", summary.Commit, summary.TreeHash, commit, tree)
	}
	if summary.RunID != testRunID {
		t.Fatalf("summary run id = %q, want %q", summary.RunID, testRunID)
	}
	if len(summary.Cells) != len(Matrix) {
		t.Fatalf("summary cells = %d, want %d", len(summary.Cells), len(Matrix))
	}
	for _, cell := range summary.Cells {
		if cell.Status != "pass" || cell.BehaviorsTotal == 0 || cell.BehaviorsPassed != cell.BehaviorsTotal {
			t.Fatalf("cell %s verdict = %+v", cell.ID, cell)
		}
		if len(cell.Problems) != 0 {
			t.Fatalf("cell %s problems = %v", cell.ID, cell.Problems)
		}
	}
	if len(summary.FuzzTargets) != len(requiredFuzzTargets) {
		t.Fatalf("summary fuzz targets = %d, want %d", len(summary.FuzzTargets), len(requiredFuzzTargets))
	}
	for _, target := range summary.FuzzTargets {
		if target.Status != "pass" || target.FuzzTime == "" {
			t.Fatalf("fuzz %s verdict = %+v", target.Target, target)
		}
		if len(target.Problems) != 0 {
			t.Fatalf("fuzz %s problems = %v", target.Target, target.Problems)
		}
	}
	if len(summary.Problems) != 0 {
		t.Fatalf("summary problems = %v", summary.Problems)
	}

	if err := run([]string{"verify", "-dir", dir, "-require-summary"}); err != nil {
		t.Fatalf("-require-summary rejected the gate's own summary: %v", err)
	}
}

// TestVerifyCommandWritesFailingSummary proves a missing cell report and a
// missing fuzz receipt are named in the summary's per-cell and per-fuzz
// verdicts, and that the overall verdict is fail.
func TestVerifyCommandWritesFailingSummary(t *testing.T) {
	root := repoRootAbs(t)
	commit, tree := gitCommit(root), gitTreeHash(root)
	if commit == "unknown" || tree == "unknown" {
		t.Fatalf("test repository has no commit/tree: %s/%s", commit, tree)
	}
	t.Setenv("GITHUB_RUN_ID", testRunID)
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGreenGateArtifacts(t, dir, commit, tree, now, testRunID, Matrix, requiredFuzzTargets)

	missingCell := Matrix[0]
	if err := os.Remove(reportPath(dir, missingCell)); err != nil {
		t.Fatal(err)
	}
	missingFuzz := requiredFuzzTargets[0]
	if err := os.Remove(fuzzReceiptPath(dir, missingFuzz.Name)); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"verify", "-dir", dir})
	if err == nil || !strings.Contains(err.Error(), "no report") || !strings.Contains(err.Error(), "no receipt") {
		t.Fatalf("verify with missing evidence = %v, want missing report and receipt", err)
	}
	summary := readSummary(t, dir)
	if summary.Status != "fail" {
		t.Fatalf("summary status = %q, want fail", summary.Status)
	}
	cell := summaryCellByID(t, summary, missingCell.ID)
	if cell.Status != "fail" || !strings.Contains(strings.Join(cell.Problems, "; "), "no report") {
		t.Fatalf("missing cell verdict = %+v", cell)
	}
	other := summaryCellByID(t, summary, Matrix[1].ID)
	if other.Status != "pass" {
		t.Fatalf("present cell %s verdict = %+v", Matrix[1].ID, other)
	}
	fuzz := summaryFuzzByTarget(t, summary, missingFuzz.key())
	if fuzz.Status != "fail" || !strings.Contains(strings.Join(fuzz.Problems, "; "), "has no receipt") {
		t.Fatalf("missing fuzz verdict = %+v", fuzz)
	}
	if len(summary.Problems) == 0 {
		t.Fatal("summary has no gate problems")
	}
}

// TestVerifyCommandWritesSummaryOnLoadFailure proves the summary is written
// even when verify cannot read the report set at all.
func TestVerifyCommandWritesSummaryOnLoadFailure(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", testRunID)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "broken.json"), "{")
	if err := run([]string{"verify", "-dir", dir}); err == nil || !strings.Contains(err.Error(), "cannot read reports") {
		t.Fatalf("verify with corrupt reports = %v", err)
	}
	summary := readSummary(t, dir)
	if summary.Status != "fail" {
		t.Fatalf("summary status = %q, want fail", summary.Status)
	}
	if len(summary.Problems) == 0 || !strings.Contains(strings.Join(summary.Problems, "; "), "cannot read reports") {
		t.Fatalf("summary problems = %v", summary.Problems)
	}
}

// TestRequireSummaryFileRejectsMissingAndMismatched proves the release-side
// summary check fails closed for the fields the release evidence job relies
// on: schema, status, commit, tree, run ID, freshness, and per-cell/per-fuzz
// verdicts.
func TestRequireSummaryFileRejectsMissingAndMismatched(t *testing.T) {
	opts := gateOptions{
		Commit: testCommit, TreeHash: testTree, RunID: testRunID,
		MaxAge: defaultMaxReportAge, Now: gateNow(t),
	}
	green := func() summaryFile {
		summary := summaryFile{
			Schema: summarySchema, Status: "pass", GeneratedAt: opts.Now,
			Commit: testCommit, TreeHash: testTree, RunID: testRunID,
		}
		for _, cell := range Matrix {
			summary.Cells = append(summary.Cells, summaryCell{
				ID: cell.ID, Platform: cell.Platform, Version: cell.Version,
				Status: "pass", BehaviorsPassed: 3, BehaviorsTotal: 3,
			})
		}
		for _, target := range requiredFuzzTargets {
			summary.FuzzTargets = append(summary.FuzzTargets, summaryFuzzTarget{
				Target: target.key(), Package: target.Package, Name: target.Name,
				Status: "pass", FuzzTime: minFuzzTime.String(),
			})
		}
		return summary
	}

	dir := t.TempDir()
	if err := writeSummary(dir, green()); err != nil {
		t.Fatal(err)
	}
	if err := requireSummaryFile(dir, opts); err != nil {
		t.Fatalf("green summary rejected: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if err := requireSummaryFile(dir, opts); err == nil || !strings.Contains(err.Error(), "required summary.json") {
		t.Fatalf("missing summary = %v, want required-summary failure", err)
	}

	mutations := []struct {
		name   string
		change func(*summaryFile)
	}{
		{name: "schema", change: func(s *summaryFile) { s.Schema++ }},
		{name: "status", change: func(s *summaryFile) { s.Status = "fail" }},
		{name: "commit", change: func(s *summaryFile) { s.Commit = "other" }},
		{name: "tree", change: func(s *summaryFile) { s.TreeHash = "other" }},
		{name: "run id", change: func(s *summaryFile) { s.RunID = "other" }},
		{name: "missing run id", change: func(s *summaryFile) { s.RunID = "" }},
		{name: "generation time", change: func(s *summaryFile) { s.GeneratedAt = time.Time{} }},
		{name: "stale", change: func(s *summaryFile) { s.GeneratedAt = opts.Now.Add(-2 * opts.MaxAge) }},
		{name: "missing cell", change: func(s *summaryFile) { s.Cells = s.Cells[1:] }},
		{name: "failed cell", change: func(s *summaryFile) { s.Cells[0].Status = "fail" }},
		{name: "failed behavior count", change: func(s *summaryFile) { s.Cells[0].BehaviorsPassed-- }},
		{name: "cell problems", change: func(s *summaryFile) { s.Cells[0].Problems = []string{"broken"} }},
		{name: "missing fuzz target", change: func(s *summaryFile) { s.FuzzTargets = s.FuzzTargets[1:] }},
		{name: "failed fuzz target", change: func(s *summaryFile) { s.FuzzTargets[0].Status = "fail" }},
		{name: "short fuzz time", change: func(s *summaryFile) { s.FuzzTargets[0].FuzzTime = "0s" }},
		{name: "fuzz problems", change: func(s *summaryFile) { s.FuzzTargets[0].Problems = []string{"broken"} }},
		{name: "gate problems", change: func(s *summaryFile) { s.Problems = []string{"broken"} }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			summary := green()
			mutation.change(&summary)
			if err := writeSummary(dir, summary); err != nil {
				t.Fatal(err)
			}
			err := requireSummaryFile(dir, opts)
			if err == nil || !strings.Contains(err.Error(), "required summary.json failed") {
				t.Fatalf("tampered summary = %v, want failure", err)
			}
		})
	}
}

// jobBlock returns one top-level job from a workflow file. Job keys are the
// only lines indented exactly two spaces.
func jobBlock(t *testing.T, workflow, job string) string {
	t.Helper()
	lines := strings.Split(workflow, "\n")
	start := -1
	for i, line := range lines {
		if line == "  "+job+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("workflow has no job %q", job)
	}
	block := strings.Join(lines[start:], "\n")
	bounds := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:$`).FindAllStringIndex(block, -1)
	if len(bounds) > 1 {
		block = block[:bounds[1][0]]
	}
	return block
}

// TestReleaseWorkflowWiresBothParityGates is the locally testable half of the
// release contract: both reusable gates are called, their summaries and
// artifacts are consumed by parity-verify, and no release/publish job can
// bypass a failed gate. The Actions semantics themselves are CI-only.
func TestReleaseWorkflowWiresBothParityGates(t *testing.T) {
	root := repoRootAbs(t)
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}

	etWorkflow := read("et-parity.yml")
	for _, want := range []string{
		"workflow_call:",
		"name: et-parity-summary",
		"path: ${{ env.ET_PARITY_OUT }}/summary.json",
		"if: always()",
		"if-no-files-found: error",
		"-job-result \"linux-cells=${CELL_JOB_RESULT}\"",
		"-job-result \"fuzz=${FUZZ_JOB_RESULT}\"",
	} {
		if !strings.Contains(etWorkflow, want) {
			t.Fatalf("et-parity.yml lacks %q", want)
		}
	}
	moshWorkflow := read("mosh-parity.yml")
	for _, want := range []string{
		"workflow_call:",
		"name: mosh-parity-summary",
		"path: build/mosh-parity/summary.json",
	} {
		if !strings.Contains(moshWorkflow, want) {
			t.Fatalf("mosh-parity.yml lacks %q", want)
		}
	}

	release := read("release.yml")
	for _, want := range []string{
		"uses: ./.github/workflows/mosh-parity.yml",
		"uses: ./.github/workflows/et-parity.yml",
		"parity-verify:",
		"pattern: mosh-parity-*",
		"pattern: et-parity-*",
		"Require both gate summaries",
		"moshparity verify",
		"etparity verify",
		"-require-summary",
		"ref: ${{ github.sha }}",
	} {
		if !strings.Contains(release, want) {
			t.Fatalf("release.yml lacks %q", want)
		}
	}
	for _, job := range []string{"mosh-parity", "et-parity"} {
		block := jobBlock(t, release, job)
		if strings.Contains(block, "\n    with:") || strings.Contains(block, "\n    with:\n") {
			t.Fatalf("%s takes an input that could redirect the gate", job)
		}
		if !strings.Contains(block, "uses: ./.github/workflows/"+job+".yml") {
			t.Fatalf("%s does not call its reusable workflow", job)
		}
	}
	for _, job := range []string{"checksums", "publish"} {
		if !strings.Contains(jobBlock(t, release, job), "- parity-verify") {
			t.Fatalf("%s does not depend on parity-verify", job)
		}
	}
	publish := jobBlock(t, release, "publish")
	if strings.Contains(publish, "if: always()") {
		t.Fatal("publish waives a failed gate with always()")
	}
	if !strings.Contains(publish, "if: success() && github.event_name == 'push'") {
		t.Fatal("publish does not require success() before publishing")
	}
}

func TestVerifyJobResults(t *testing.T) {
	tests := []struct {
		name    string
		results jobResultList
		want    string
	}{
		{name: "success", results: jobResultList{"linux-cells=success", "fuzz=success"}},
		{name: "failure", results: jobResultList{"fuzz=failure"}, want: `required job fuzz result "failure"`},
		{name: "skipped", results: jobResultList{"linux-cells=skipped"}, want: `required job linux-cells result "skipped"`},
		{name: "cancelled", results: jobResultList{"macos-native=cancelled"}, want: `required job macos-native result "cancelled"`},
		{name: "unknown result", results: jobResultList{"fuzz=bogus"}, want: `invalid result "bogus"`},
		{name: "missing value", results: jobResultList{"fuzz="}, want: "must be NAME=RESULT"},
		{name: "missing separator", results: jobResultList{"fuzz"}, want: "must be NAME=RESULT"},
		{name: "duplicate", results: jobResultList{"fuzz=success", "fuzz=success"}, want: "appears more than once"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyJobResults(test.results)
			if test.want == "" {
				if err != nil {
					t.Fatalf("verifyJobResults(%v) = %v", test.results, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyJobResults(%v) = %v, want %q", test.results, err, test.want)
			}
		})
	}
}

// TestVerifyCommandRecordsJobResultFailure proves an upstream job failure is
// both a gate failure and visible in summary.json, even when the artifacts
// themselves are green.
func TestVerifyCommandRecordsJobResultFailure(t *testing.T) {
	root := repoRootAbs(t)
	commit, tree := gitCommit(root), gitTreeHash(root)
	if commit == "unknown" || tree == "unknown" {
		t.Fatalf("test repository has no commit/tree: %s/%s", commit, tree)
	}
	t.Setenv("GITHUB_RUN_ID", testRunID)
	dir := t.TempDir()
	now := time.Now().UTC()
	writeGreenGateArtifacts(t, dir, commit, tree, now, testRunID, Matrix, requiredFuzzTargets)

	err := run([]string{"verify", "-dir", dir, "-job-result", "fuzz=failure"})
	if err == nil || !strings.Contains(err.Error(), `required job fuzz result "failure"`) {
		t.Fatalf("verify with a failed upstream job = %v", err)
	}
	summary := readSummary(t, dir)
	if summary.Status != "fail" {
		t.Fatalf("summary status = %q, want fail", summary.Status)
	}
	if !strings.Contains(strings.Join(summary.Problems, "; "), `required job fuzz result "failure"`) {
		t.Fatalf("summary problems = %v", summary.Problems)
	}
}
