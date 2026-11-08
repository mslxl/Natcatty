package shellhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// captureMatrix is the shared fixture for ticket 07 (shell/transport mechanics)
// and ticket 21 (complete Host/Shell matrix and release gates). The verification
// value separates observed behavior (a real shell ran here) from manual checks
// (a real runner or VM is required) and planned behavior.
type captureMatrix struct {
	Description    string              `json:"description"`
	FidelityLegend string              `json:"fidelityLegend"`
	ObservedRuns   []matrixObservedRun `json:"observedRuns"`
	Paths          []captureMatrixPath `json:"paths"`
	Cells          []matrixCell        `json:"cells"`
}

type captureMatrixPath struct {
	Name      string   `json:"name"`
	Platforms []string `json:"platforms"`
	Kind      string   `json:"kind"`
	Shell     string   `json:"shell"`
	// LoginShell is the account shell an SSH transport detects through the
	// non-executing SFTP probe. Empty means detection was unavailable.
	LoginShell string `json:"loginShell"`
	// RemoteShell is the interactive shell an SSH cell exercises. It binds a
	// remote fixture to one shell the way Shell binds a local fixture, since an
	// SSH path has no local command whose argv could name the shell.
	RemoteShell string `json:"remoteShell,omitempty"`
	// RemoteStartup is the exact startup the remote account's shell receives.
	// An account login shell started by sshd gets no added flags; a wrapper
	// path records the flags the wrapper itself adds.
	RemoteStartup    []string          `json:"remoteStartup,omitempty"`
	Args             []string          `json:"args"`
	HookEnabled      bool              `json:"hookEnabled"`
	SpecEnv          map[string]string `json:"specEnv"`
	WantHookExpected bool              `json:"wantHookExpected"`
	WantCommand      []string          `json:"wantCommand"`
	// WantCommandPrefix and WantCommandContains describe a planned command
	// whose full text is a generated bootstrap (PowerShell).
	WantCommandPrefix   []string          `json:"wantCommandPrefix"`
	WantCommandContains []string          `json:"wantCommandContains"`
	WantEnv             map[string]string `json:"wantEnv"`
	WantFiles           map[string]string `json:"wantFiles"`
	// WantRemoteWrapper reports whether the transport may send the POSIX
	// wrapper after it establishes the login shell.
	WantRemoteWrapper bool `json:"wantRemoteWrapper"`
	// WantRemoteInvocations records the exact shell invocation lines the
	// wrapper must contain for this login shell.
	WantRemoteInvocations []string `json:"wantRemoteInvocations"`
	// Verification applies to the evidence platform(s) named by
	// ObservedPlatforms. It never claims an observation for a platform this
	// machine did not run; per-platform truth is in Cells instead.
	Verification      string   `json:"verification"`
	ObservedPlatforms []string `json:"observedPlatforms"`
	Fidelity          struct {
		Tier     string `json:"tier"`
		Cwd      string `json:"cwd"`
		Exit     string `json:"exit"`
		Duration string `json:"duration"`
	} `json:"fidelity"`
	Evidence string `json:"evidence"`
}

// matrixObservedRun records one real run of a real-shell test. The host,
// architecture, and version fields bind the evidence to the cell it supports,
// so a Darwin PowerShell run cannot be cited as Windows fidelity.
type matrixObservedRun struct {
	ID          string `json:"id"`
	Harness     string `json:"harness"` // local, docker, github-actions, vm
	HostOS      string `json:"hostOS"`
	Arch        string `json:"arch"`
	Platform    string `json:"platform"`
	Image       string `json:"image"`
	ImageDigest string `json:"imageDigest"`
	Runtime     string `json:"runtime"`
	Runner      string `json:"runner"`
	// Target is the SSH endpoint a remote observation connected to, in
	// user@host:port form. It is required for the ssh harness and empty for
	// every local or hosted-runner harness.
	Target        string            `json:"target,omitempty"`
	ShellVersions map[string]string `json:"shellVersions"`
	Command       string            `json:"command"`
	Result        string            `json:"result"`
	RecordedAt    string            `json:"recordedAt"`
}

// matrixCell is one Host/Shell cell. Tier 1 means the authoritative hook, Tier
// 2 native OSC 133/7 without a Netcatty hook, and Tier 3 typed input plus
// prompt detection.
type matrixCell struct {
	Name string `json:"name"`
	// Host is the operating system in the Host/Shell matrix (linux, darwin,
	// windows, freebsd), not a saved Host target in the Vault.
	Host            string        `json:"host"`
	Arch            string        `json:"arch"`
	Shell           string        `json:"shell"`
	Path            string        `json:"path"`
	Tier            int           `json:"tier"`
	ReleaseBlocking bool          `json:"releaseBlocking"`
	Verification    string        `json:"verification"`
	Startup         []string      `json:"startup"`
	Observed        *cellObserved `json:"observed,omitempty"`
	CI              *cellCI       `json:"ci,omitempty"`
	Manual          *cellManual   `json:"manual,omitempty"`
	Evidence        string        `json:"evidence"`
}

type cellFidelity struct {
	Cwd      string `json:"cwd"`
	Exit     string `json:"exit"`
	Duration string `json:"duration"`
}

type cellObserved struct {
	Runs     []string     `json:"runs"`
	Test     string       `json:"test"`
	Fidelity cellFidelity `json:"fidelity"`
}

type cellManual struct {
	Runner           string       `json:"runner"`
	Steps            []string     `json:"steps"`
	ExpectedFidelity cellFidelity `json:"expectedFidelity"`
}

// cellCI binds a cell to a job in the release workflow. It is provisioning
// evidence: the job runs the named tests on the named runner, but this machine
// did not run it and the cell must not claim an observation. The checker reads
// release.yml itself, so deleting the job from the workflow fails the matrix
// instead of leaving a dangling citation.
type cellCI struct {
	Workflow string `json:"workflow"`
	Job      string `json:"job"`
	Runner   string `json:"runner"`
	// Platform is the Host/architecture the job exercises. It must agree with
	// the cell, so a Windows runner cannot be cited for a BSD cell.
	Platform string `json:"platform"`
	// Tests are the real-shell tests the job must run for this cell.
	Tests []string `json:"tests"`
	// Requires are the per-shell selection strings the job must contain, such
	// as NETCATTY_TEST_SSH_ZSH=. They bind a shared test to one shell.
	Requires []string `json:"requires,omitempty"`
	// Command is the exact command line the job runs. Every test in Tests must
	// appear in it.
	Command          string       `json:"command"`
	ExpectedFidelity cellFidelity `json:"expectedFidelity"`
}

// TestCaptureMatrix keeps the fixture honest: it replans every local path on
// each recorded platform and checks the exact command, environment, and hook
// files; it replans every remote path and applies the same login-shell
// decision the SSH transport applies before opening the terminal.
func TestCaptureMatrix(t *testing.T) {
	matrix := loadCaptureMatrix(t)
	if len(matrix.Paths) == 0 {
		t.Fatal("the capture matrix is empty")
	}
	seen := map[string]bool{}
	for _, entry := range matrix.Paths {
		entry := entry
		t.Run(entry.Name, func(t *testing.T) {
			if entry.Name == "" || seen[entry.Name] {
				t.Fatalf("the capture matrix has an empty or duplicate name %q", entry.Name)
			}
			seen[entry.Name] = true
			if entry.Evidence == "" {
				t.Fatal("the path has no evidence note")
			}
			checkCaptureFidelity(t, entry)
			switch entry.Kind {
			case "local":
				checkLocalCapturePath(t, entry)
			case "ssh":
				checkRemoteCapturePath(t, entry)
			default:
				t.Fatalf("unknown transport %q", entry.Kind)
			}
		})
	}
}

// TestHostShellMatrix is the ticket 21 checker: every Host/Shell cell has a
// fixture and a Tier 1/2/3 classification, every observed cell cites a real
// run on the cell's own host and architecture, and no unrun platform claims an
// observation.
func TestHostShellMatrix(t *testing.T) {
	matrix := loadCaptureMatrix(t)
	if err := validateHostShellMatrix(matrix); err != nil {
		t.Fatal(err)
	}
}

// TestHostShellReleaseGates fails when a release-blocking cell is missing or
// classified below its required tier. The requirements name Tier 1 zsh, bash,
// fish, and PowerShell cells and a Tier 3 fallback for every release Host.
func TestHostShellReleaseGates(t *testing.T) {
	matrix := loadCaptureMatrix(t)
	if err := validateHostShellReleaseGates(matrix); err != nil {
		t.Fatal(err)
	}
}

// TestHostShellMatrixRejectsBadFixtures proves the checker catches the three
// failure modes the ticket names: a missing cell, a wrong tier, and a false
// observed or exact claim for a platform that did not run here.
func TestHostShellMatrixRejectsBadFixtures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*captureMatrix)
		want   string
	}{
		{
			name: "missing release-blocking cell",
			mutate: func(matrix *captureMatrix) {
				matrix.Cells = removeCell(matrix.Cells, "linux-amd64-zsh")
			},
			want: `release-blocking cell "linux-amd64-zsh" is missing`,
		},
		{
			name: "release-blocking cell below its tier",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.Tier = 2
			},
			want: "classified Tier 2, required Tier 1",
		},
		{
			name: "tier 3 fallback misclassified as tier 1",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-tier3-fallback")
				cell.Tier = 1
			},
			want: "classified Tier 1, required Tier 3",
		},
		{
			name: "observed claim cites a run on another host",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-powershell")
				cell.Verification = "observed"
				cell.Manual = nil
				cell.Observed = &cellObserved{
					Runs:     []string{"macos-arm64-local-2026-11-08"},
					Test:     "TestRealPowerShellLocalHookCapturesTierOne",
					Fidelity: cellFidelity{Cwd: "exact", Exit: "exact", Duration: "exact"},
				}
			},
			want: "observed on darwin/arm64",
		},
		{
			name: "observed claim with no run",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.Observed.Runs = nil
			},
			want: "no observation run",
		},
		{
			name: "manual cell carries observation evidence",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-nushell")
				cell.Observed = &cellObserved{
					Runs:     []string{"macos-arm64-native-fallback-local-2026-11-08"},
					Test:     "TestRealNushellWithoutHookKeepsNativeCapture",
					Fidelity: cellFidelity{Cwd: "exact", Exit: "missing", Duration: "observed"},
				}
			},
			want: "carries observation or CI evidence",
		},
		{
			name: "observed claim cites a test that does not exist",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.Observed.Test = "TestRealImaginary"
			},
			want: "no test in this package defines",
		},
		{
			name: "observed cell records startup flags its path does not use",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.Startup = []string{"--bogus-flag"}
			},
			want: "records startup flags",
		},
		{
			name: "tier 1 cell claims fallback fidelity",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.Observed.Fidelity.Cwd = "missing"
			},
			want: "Tier 1 has",
		},
		{
			name: "tier 3 cell claims an exact cwd",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-tier3-fallback")
				cell.Observed.Fidelity.Cwd = "exact"
			},
			want: "Tier 3 has",
		},
		{
			name: "docker observation without an image digest",
			mutate: func(matrix *captureMatrix) {
				run := findRun(matrix.ObservedRuns, "linux-arm64-docker-2026-11-08")
				run.ImageDigest = ""
			},
			want: "image digest",
		},
		{
			name: "release-blocking cell has only a planned check",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-powershell")
				cell.Verification = "planned"
			},
			want: "no observed or documented manual check",
		},
		{
			name: "release-blocking cell has no gate requirement",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-nushell")
				cell.ReleaseBlocking = true
			},
			want: "no release gate requirement",
		},
		{
			name: "non-gate matrix cell removed",
			mutate: func(matrix *captureMatrix) {
				matrix.Cells = removeCell(matrix.Cells, "windows-amd64-nushell")
			},
			want: `matrix cell "windows-amd64-nushell" is missing`,
		},
		{
			name: "observed run did not pass",
			mutate: func(matrix *captureMatrix) {
				run := findRun(matrix.ObservedRuns, "macos-arm64-local-2026-11-08")
				run.Result = "FAIL: the fish subtest timed out"
			},
			want: "not PASS",
		},
		{
			name: "observed cell cites a test for another shell",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.Observed.Test = "TestRealNushellSessionCapturesTierOneEndToEnd"
			},
			want: "does not cover zsh",
		},
		{
			name: "observed cell cites a run that selected another shell",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "macos-arm64-zsh")
				cell.Observed.Runs = []string{"macos-arm64-bash32-local-2026-11-08"}
			},
			want: "selected bash",
		},
		{
			name: "local run platform disagrees with its architecture",
			mutate: func(matrix *captureMatrix) {
				run := findRun(matrix.ObservedRuns, "macos-arm64-local-2026-11-08")
				run.Platform = "darwin/amd64"
			},
			want: "platform",
		},
		{
			name: "tier 2 cell claims an exact exit",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-nushell")
				cell.Manual.ExpectedFidelity.Exit = "exact"
			},
			want: "Tier 2 has exit",
		},
		{
			name: "ci cell cites a job the release workflow does not have",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-powershell")
				cell.CI.Job = "capture-imaginary"
			},
			want: "release workflow has no job",
		},
		{
			name: "ci cell names another platform",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-powershell")
				cell.CI.Platform = "darwin/arm64"
			},
			want: "platform",
		},
		{
			name: "ci cell cites a test for another shell",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "windows-amd64-powershell")
				cell.CI.Tests = []string{"TestRealNushellWithoutHookKeepsNativeCapture"}
			},
			want: "does not cover powershell",
		},
		{
			name: "observed cell cites a release job that does not exist",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "linux-amd64-zsh")
				cell.CI.Job = "capture-imaginary"
			},
			want: "release workflow has no job",
		},
		{
			name: "bsd cell uses a local PTY path",
			mutate: func(matrix *captureMatrix) {
				cell := findCell(matrix.Cells, "bsd-amd64-zsh")
				cell.Path = "bsd-local-zsh"
			},
			want: "SSH product path",
		},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			matrix := loadCaptureMatrix(t)
			testCase.mutate(&matrix)
			err := validateHostShellMatrix(matrix)
			if err == nil {
				t.Fatal("the checker accepted a bad fixture")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to contain %q", err, testCase.want)
			}
		})
	}
}

func loadCaptureMatrix(t *testing.T) captureMatrix {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "capture-matrix.json"))
	if err != nil {
		t.Fatal(err)
	}
	var matrix captureMatrix
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matrix); err != nil {
		t.Fatalf("decode the capture matrix: %v", err)
	}
	return matrix
}

func findCell(cells []matrixCell, name string) *matrixCell {
	for index := range cells {
		if cells[index].Name == name {
			return &cells[index]
		}
	}
	panic("no cell named " + name)
}

func removeCell(cells []matrixCell, name string) []matrixCell {
	result := make([]matrixCell, 0, len(cells))
	for _, cell := range cells {
		if cell.Name != name {
			result = append(result, cell)
		}
	}
	return result
}

func findRun(runs []matrixObservedRun, id string) *matrixObservedRun {
	for index := range runs {
		if runs[index].ID == id {
			return &runs[index]
		}
	}
	panic("no observation run named " + id)
}

// checkCaptureFidelity rejects a tier claim that contradicts the hook the path
// installs, an unrecognized fidelity value, and an observation claim the
// evidence cannot support.
func checkCaptureFidelity(t *testing.T, entry captureMatrixPath) {
	t.Helper()
	switch entry.Fidelity.Tier {
	case "1":
		if !entry.WantHookExpected {
			t.Fatal("tier 1 claims a hook this path does not install")
		}
	case "2", "3", "2/3":
		if entry.WantHookExpected {
			t.Fatal("a fallback tier claims a hook this path installs")
		}
	default:
		t.Fatalf("unknown fidelity tier %q", entry.Fidelity.Tier)
	}
	fields := map[string]string{
		"cwd": entry.Fidelity.Cwd, "exit": entry.Fidelity.Exit, "duration": entry.Fidelity.Duration,
	}
	for name, value := range fields {
		switch value {
		case "exact", "observed", "missing", "manual", "planned":
		default:
			t.Fatalf("unknown %s fidelity %q", name, value)
		}
	}
	switch entry.Verification {
	case "observed":
		assertCitedRealTestsExist(t, entry.Evidence)
		if len(entry.ObservedPlatforms) == 0 {
			t.Fatal("an observed path names no platform it was observed on")
		}
		for _, platform := range entry.ObservedPlatforms {
			if !slices.Contains(entry.Platforms, platform) {
				t.Fatalf("observed platform %q is not one of the planned platforms %v",
					platform, entry.Platforms)
			}
		}
		for name, value := range fields {
			if value == "manual" || value == "planned" {
				t.Fatalf("observed path calls %s %q", name, value)
			}
		}
	case "manual", "planned":
		// Nothing on this machine ran the path, so no field may claim an
		// observation and no platform may be named as observed.
		if len(entry.ObservedPlatforms) != 0 {
			t.Fatalf("%s path names observed platforms %v", entry.Verification, entry.ObservedPlatforms)
		}
		for name, value := range fields {
			if value == "exact" || value == "observed" {
				t.Fatalf("%s path claims %s = %q", entry.Verification, name, value)
			}
		}
	default:
		t.Fatalf("unknown verification %q", entry.Verification)
	}
}

// citedRealTest matches a test function name in the evidence note.
var citedRealTest = regexp.MustCompile(`TestReal[A-Za-z0-9_]+`)

// assertCitedRealTestsExist rejects an observed claim that names a test this
// package does not contain. A matrix entry cannot claim a real shell ran when
// its evidence is invented or stale.
func assertCitedRealTestsExist(t *testing.T, evidence string) {
	t.Helper()
	names := citedRealTest.FindAllString(evidence, -1)
	if len(names) == 0 {
		t.Fatalf("observed path cites no real-shell test: %q", evidence)
	}
	source, err := testPackageSource()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !strings.Contains(source, "func "+name+"(") {
			t.Fatalf("observed evidence cites %s, which no test in this package defines", name)
		}
	}
}

// testPackageSource returns every test file in this package, so a fixture can
// prove that the test it cites exists instead of trusting a stale name.
func testPackageSource() (string, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return "", err
	}
	var source strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			return "", err
		}
		source.Write(data)
	}
	return source.String(), nil
}

func testDefinedInSource(source, name string) bool {
	return strings.Contains(source, "func "+name+"(")
}

// checkLocalCapturePath plans the recorded local invocation on every recorded
// platform and compares the result with the fixture.
func checkLocalCapturePath(t *testing.T, entry captureMatrixPath) {
	t.Helper()
	if entry.LoginShell != "" || entry.WantRemoteWrapper || len(entry.WantRemoteInvocations) != 0 {
		t.Fatal("a local path records remote-only fields")
	}
	// The developer's own zsh and xonsh environment must not leak into the
	// planned startup; the StartSpec is the only source the fixture describes.
	t.Setenv("ZDOTDIR", "")
	t.Setenv("XONSHRC_DIR", "")
	t.Setenv("XONSH_CONFIG_DIR", "")
	t.Setenv("XONSH_SYS_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	platforms := entry.Platforms
	if len(platforms) == 0 {
		platforms = []string{"darwin"}
	}
	for _, platform := range platforms {
		platform := platform
		t.Run(platform, func(t *testing.T) {
			home := t.TempDir()
			hookDir := filepath.Join(t.TempDir(), "hook")
			if err := os.MkdirAll(hookDir, 0o700); err != nil {
				t.Fatal(err)
			}
			shellPath := filepath.Join("/opt/test/bin", entry.Shell)
			options := []Option{
				WithPlatform(platform),
				WithDefaultShell(func() (string, error) { return shellPath, nil }),
				WithTempDir(func() (string, error) { return hookDir, nil }),
			}
			if !entry.HookEnabled {
				options = append(options, WithEnabled(func() bool { return false }))
			}
			spec := appsession.StartSpec{
				Kind:    appsession.KindLocal,
				Command: append([]string{shellPath}, entry.Args...),
				Env:     matrixSpecEnv(entry.SpecEnv, home),
			}
			plan, failed := NewPlanner(options...).Plan(spec)
			if failed {
				t.Fatal("the planner reported a hook failure")
			}
			if plan.Cleanup != nil {
				defer plan.Cleanup()
			}
			if plan.HookExpected != entry.WantHookExpected {
				t.Fatalf("HookExpected = %t, want %t", plan.HookExpected, entry.WantHookExpected)
			}
			if !entry.WantHookExpected && plan.Cleanup != nil {
				t.Fatal("a path without a hook still created a session directory")
			}
			assertMatrixCommand(t, plan.Spec.Command, entry.WantCommand,
				entry.WantCommandPrefix, entry.WantCommandContains, shellPath, home, hookDir)
			assertMatrixEnv(t, plan.Spec.Env, entry.SpecEnv, entry.WantEnv, shellPath, home, hookDir)
			assertMatrixFiles(t, hookDir, entry.WantFiles)
		})
	}
}

// checkRemoteCapturePath plans the SSH path, applies the login-shell decision
// the transport makes with the SFTP probe, and checks the recorded exact
// invocation for the shells that keep the wrapper.
func checkRemoteCapturePath(t *testing.T, entry captureMatrixPath) {
	t.Helper()
	if entry.Shell != "" || len(entry.Args) != 0 || len(entry.WantCommand) != 0 {
		t.Fatal("an SSH path records local-only fields")
	}
	platforms := entry.Platforms
	if len(platforms) == 0 {
		platforms = []string{"freebsd"}
	}
	for _, platform := range platforms {
		platform := platform
		t.Run(platform, func(t *testing.T) {
			options := []Option{WithPlatform(platform)}
			if !entry.HookEnabled {
				options = append(options, WithEnabled(func() bool { return false }))
			}
			plan, failed := NewPlanner(options...).Plan(appsession.StartSpec{Kind: appsession.KindSSH})
			if failed {
				t.Fatal("the planner reported a hook failure")
			}
			if !entry.HookEnabled {
				if plan.HookExpected || plan.Spec.RemoteCommand != "" || plan.Spec.CaptureHookRemote {
					t.Fatalf("a disabled remote hook changed the StartSpec: %+v", plan.Spec)
				}
				if entry.WantHookExpected || entry.WantRemoteWrapper {
					t.Fatal("a disabled remote hook claims capture")
				}
				return
			}
			if plan.Spec.RemoteCommand == "" || !plan.Spec.CaptureHookRemote {
				t.Fatalf("the SSH plan did not mark the remote wrapper: %+v", plan.Spec)
			}
			command := RemoteCommandForLoginShell(plan.Spec.RemoteCommand, entry.LoginShell)
			wrapper := command != ""
			if wrapper != entry.WantRemoteWrapper {
				t.Fatalf("wrapper = %t, want %t (login shell %q)", wrapper, entry.WantRemoteWrapper, entry.LoginShell)
			}
			// The transport withdraws the hook exactly when it drops the
			// command, so the effective hook expectation follows the wrapper.
			if entry.WantHookExpected != wrapper {
				t.Fatalf("effective HookExpected = %t, want %t", wrapper, entry.WantHookExpected)
			}

			script := remoteWrapperScript()
			name := filepath.Base(strings.TrimSpace(entry.LoginShell))
			if wrapper {
				if !RemoteLoginShellSupported(entry.LoginShell) {
					t.Fatalf("the wrapper would be sent to unsupported shell %q", entry.LoginShell)
				}
				if name == "" || !strings.Contains(script, name+")\n") {
					t.Fatalf("the wrapper has no %q branch", name)
				}
				if len(entry.WantRemoteInvocations) == 0 {
					t.Fatal("a wrapper path records no exact invocation")
				}
				for _, invocation := range entry.WantRemoteInvocations {
					if !strings.Contains(script, invocation) {
						t.Fatalf("the remote wrapper is missing the exact invocation %q", invocation)
					}
				}
				return
			}

			if RemoteLoginShellSupported(entry.LoginShell) {
				t.Fatalf("a supported POSIX login shell %q lost the wrapper", entry.LoginShell)
			}
			if command != "" || len(entry.WantRemoteInvocations) != 0 {
				t.Fatal("a fallback path records a wrapper or invocation")
			}
			if name != "" && (strings.Contains(script, name+")\n") ||
				strings.Contains(script, name+"|") || strings.Contains(script, "|"+name+")")) {
				t.Fatalf("the wrapper is never sent to %q but still has its branch", name)
			}
		})
	}
}

func matrixSpecEnv(values map[string]string, home string) []string {
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+strings.ReplaceAll(value, "<home>", home))
	}
	sort.Strings(env)
	return env
}

func normalizeMatrixValue(value, shellPath, home, hookDir string) string {
	value = strings.ReplaceAll(value, hookDir, "<hook-dir>")
	value = strings.ReplaceAll(value, home, "<home>")
	value = strings.ReplaceAll(value, shellPath, "<shell>")
	// The fixture records one command shape for every platform; Windows
	// planners build the same paths with backslashes and joins path lists
	// with a semicolon.
	value = filepath.ToSlash(value)
	return normalizeMatrixPathList(value)
}

func normalizeMatrixPathList(value string) string {
	return strings.ReplaceAll(value, string(os.PathListSeparator), "<path-list>")
}

func assertMatrixCommand(t *testing.T, got, want, prefix, contains []string,
	shellPath, home, hookDir string) {
	t.Helper()
	normalized := make([]string, len(got))
	for index, value := range got {
		normalized[index] = normalizeMatrixValue(value, shellPath, home, hookDir)
	}
	if want != nil {
		if !reflect.DeepEqual(normalized, want) {
			t.Fatalf("command = %v, want %v", normalized, want)
		}
		return
	}
	if len(prefix) == 0 {
		t.Fatal("the path records no command expectation")
	}
	if len(normalized) < len(prefix) || !reflect.DeepEqual(normalized[:len(prefix)], prefix) {
		t.Fatalf("command = %v, want prefix %v", normalized, prefix)
	}
	joined := strings.Join(normalized, " ")
	for _, value := range contains {
		if !strings.Contains(joined, value) {
			t.Fatalf("command = %q, want it to contain %q", joined, value)
		}
	}
}

func assertMatrixEnv(t *testing.T, got []string, specEnv, want map[string]string,
	shellPath, home, hookDir string) {
	t.Helper()
	expected := map[string]string{}
	for key, value := range specEnv {
		expected[key] = normalizeMatrixPathList(value)
	}
	for key, value := range want {
		expected[key] = normalizeMatrixPathList(value)
	}
	actual := map[string]string{}
	for _, entry := range got {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed environment entry %q", entry)
		}
		actual[key] = normalizeMatrixValue(value, shellPath, home, hookDir)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("environment = %v, want %v", actual, expected)
	}
}

func assertMatrixFiles(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	sort.Strings(names)
	sort.Strings(wantNames)
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("hook files = %v, want %v", names, wantNames)
	}
	for name, marker := range want {
		contents, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), marker) {
			t.Fatalf("%s does not contain %q", name, marker)
		}
	}
}

var (
	imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	recordedAtPattern  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// hostShellGate is one release-blocking requirement. Tier numbers are capture
// quality (1 best), so a cell classified Tier 2 or 3 fails a Tier 1 gate, and a
// fallback cell classified Tier 1 fails the Tier 3 gate because typed input
// plus prompt detection is the required mechanism there.
type hostShellGate struct {
	Name     string
	Tier     int
	Category string
}

// requiredHostShellGates is the release contract from the spec: every Tier 1
// zsh, bash, fish, and PowerShell cell is release-blocking, as is a Tier 3
// fallback on every Host. Linux covers x86_64 and aarch64, macOS arm64 and
// x86_64, Windows x86_64 and arm64, and BSD is represented by FreeBSD amd64
// (the other BSDs use the same POSIX startup files; their support is planned
// by the path fixture and does not add a release gate). PowerShell on BSD is
// out of scope: BSD is in scope for POSIX shells. nushell and xonsh are
// best-effort cells and are never release gates, whatever tier they reach.
var requiredHostShellGates = []hostShellGate{
	{"linux-amd64-zsh", 1, "zsh-tier1"},
	{"linux-amd64-bash", 1, "bash-tier1"},
	{"linux-amd64-fish", 1, "fish-tier1"},
	{"linux-amd64-powershell", 1, "powershell-tier1"},
	{"linux-amd64-tier3-fallback", 3, "tier3-fallback"},
	{"linux-arm64-zsh", 1, "zsh-tier1"},
	{"linux-arm64-bash", 1, "bash-tier1"},
	{"linux-arm64-fish", 1, "fish-tier1"},
	{"linux-arm64-powershell", 1, "powershell-tier1"},
	{"linux-arm64-tier3-fallback", 3, "tier3-fallback"},
	{"macos-arm64-zsh", 1, "zsh-tier1"},
	{"macos-arm64-bash", 1, "bash-tier1"},
	{"macos-arm64-fish", 1, "fish-tier1"},
	{"macos-arm64-powershell", 1, "powershell-tier1"},
	{"macos-arm64-tier3-fallback", 3, "tier3-fallback"},
	{"macos-amd64-zsh", 1, "zsh-tier1"},
	{"macos-amd64-bash", 1, "bash-tier1"},
	{"macos-amd64-fish", 1, "fish-tier1"},
	{"macos-amd64-powershell", 1, "powershell-tier1"},
	{"macos-amd64-tier3-fallback", 3, "tier3-fallback"},
	{"windows-amd64-powershell", 1, "powershell-tier1"},
	{"windows-amd64-tier3-fallback", 3, "tier3-fallback"},
	{"windows-arm64-powershell", 1, "powershell-tier1"},
	{"windows-arm64-tier3-fallback", 3, "tier3-fallback"},
	{"bsd-amd64-zsh", 1, "zsh-tier1"},
	{"bsd-amd64-bash", 1, "bash-tier1"},
	{"bsd-amd64-fish", 1, "fish-tier1"},
	{"bsd-amd64-tier3-fallback", 3, "tier3-fallback"},
}

var requiredGateCategories = []string{
	"zsh-tier1", "bash-tier1", "fish-tier1", "powershell-tier1", "tier3-fallback",
}

// releaseWorkflowJob is the part of a release.yml job the matrix checker
// needs: what it runs on, whether it can be skipped, and what it depends on.
type releaseWorkflowJob struct {
	Name   string
	RunsOn string
	HasIf  bool
	Needs  []string
	Text   string
}

// releaseWorkflowJobs parses the subset of the release workflow the matrix
// contract needs. It deliberately reads the file itself instead of trusting a
// copy in the fixture: deleting or silently skipping a capture job must fail
// the matrix.
func releaseWorkflowJobs() (map[string]releaseWorkflowJob, error) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		return nil, err
	}
	jobs := map[string]releaseWorkflowJob{}
	inJobs := false
	current := ""
	inNeeds := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inJobs = trimmed == "jobs:"
			current = ""
			inNeeds = false
			continue
		}
		if !inJobs {
			continue
		}
		if indent == 2 && strings.HasSuffix(trimmed, ":") {
			name := strings.TrimSuffix(trimmed, ":")
			if name == "" || strings.ContainsAny(name, " {}[]$") {
				current = ""
				continue
			}
			jobs[name] = releaseWorkflowJob{Name: name}
			current = name
			inNeeds = false
			continue
		}
		if current == "" {
			continue
		}
		job := jobs[current]
		job.Text += line + "\n"
		if indent == 4 {
			switch {
			case strings.HasPrefix(trimmed, "runs-on:"):
				job.RunsOn = strings.TrimSpace(strings.TrimPrefix(trimmed, "runs-on:"))
				inNeeds = false
			case strings.HasPrefix(trimmed, "if:"):
				job.HasIf = true
				inNeeds = false
			case trimmed == "needs:":
				inNeeds = true
			case strings.HasPrefix(trimmed, "needs:"):
				job.Needs = parseWorkflowList(strings.TrimSpace(strings.TrimPrefix(trimmed, "needs:")))
				inNeeds = false
			default:
				inNeeds = false
			}
		} else if inNeeds && indent > 4 && strings.HasPrefix(trimmed, "- ") {
			job.Needs = append(job.Needs, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
		jobs[current] = job
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("release.yml has no jobs")
	}
	return jobs, nil
}

// parseWorkflowList reads an inline YAML flow sequence such as [a, b].
func parseWorkflowList(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

// releaseReachable reports whether job is required by the publish job, so a
// failed, skipped, or deleted capture job makes the release impossible.
func releaseReachable(jobs map[string]releaseWorkflowJob, job string) bool {
	seen := map[string]bool{}
	queue := append([]string(nil), jobs["publish"].Needs...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		if name == job {
			return true
		}
		queue = append(queue, jobs[name].Needs...)
	}
	return false
}

// requiredHostShellCells is the full spec grid, independent of which cells
// release-blocking. Deleting a best-effort cell must fail the matrix just as
// deleting a gate does.
var requiredHostShellCells = buildRequiredHostShellCells()

func buildRequiredHostShellCells() map[string]bool {
	hosts := []struct{ prefix, host, arch string }{
		{"linux", "linux", "amd64"},
		{"linux", "linux", "arm64"},
		{"macos", "darwin", "arm64"},
		{"macos", "darwin", "amd64"},
		{"windows", "windows", "amd64"},
		{"windows", "windows", "arm64"},
		{"bsd", "freebsd", "amd64"},
	}
	shells := []string{"zsh", "bash", "fish", "nushell", "xonsh", "powershell"}
	cells := map[string]bool{}
	for _, host := range hosts {
		for _, shell := range shells {
			if host.host == "freebsd" && shell == "powershell" {
				continue
			}
			cells[fmt.Sprintf("%s-%s-%s", host.prefix, host.arch, shell)] = true
		}
		cells[fmt.Sprintf("%s-%s-tier3-fallback", host.prefix, host.arch)] = true
	}
	return cells
}

func validateHostShellMatrix(matrix captureMatrix) error {
	if len(matrix.Cells) == 0 {
		return fmt.Errorf("the Host/Shell matrix has no cells")
	}
	// The release contract is the first thing a broken matrix should report:
	// a missing gate cell or a misclassified tier is the failure a release
	// owner has to fix, not a detail of the cell that happens to be invalid.
	if err := validateHostShellReleaseGates(matrix); err != nil {
		return err
	}
	paths := map[string]captureMatrixPath{}
	for _, path := range matrix.Paths {
		paths[path.Name] = path
	}
	runs := map[string]matrixObservedRun{}
	for _, run := range matrix.ObservedRuns {
		if run.ID == "" {
			return fmt.Errorf("an observation run has an empty id")
		}
		if _, exists := runs[run.ID]; exists {
			return fmt.Errorf("observation run %q is duplicated", run.ID)
		}
		if err := validateObservedRun(run); err != nil {
			return fmt.Errorf("observation run %q: %w", run.ID, err)
		}
		runs[run.ID] = run
	}
	source, err := testPackageSource()
	if err != nil {
		return fmt.Errorf("read the test package source: %w", err)
	}
	jobs, err := releaseWorkflowJobs()
	if err != nil {
		return fmt.Errorf("read the release workflow: %w", err)
	}
	seen := map[string]bool{}
	for _, cell := range matrix.Cells {
		if cell.Name == "" || seen[cell.Name] {
			return fmt.Errorf("cell %q has an empty or duplicate name", cell.Name)
		}
		seen[cell.Name] = true
		if !requiredHostShellCells[cell.Name] {
			return fmt.Errorf("cell %q is not part of the spec Host/Shell grid", cell.Name)
		}
		if err := validateHostShellCell(cell, paths, runs, source, jobs); err != nil {
			return fmt.Errorf("cell %q: %w", cell.Name, err)
		}
	}
	for name := range requiredHostShellCells {
		if !seen[name] {
			return fmt.Errorf("matrix cell %q is missing", name)
		}
	}
	return nil
}

func validateObservedRun(run matrixObservedRun) error {
	if len(run.ShellVersions) == 0 {
		return fmt.Errorf("records no shell version")
	}
	if run.Command == "" || run.Result == "" {
		return fmt.Errorf("records no command or result")
	}
	// An observation exists only when the command passed. A FAIL, a timeout,
	// or a partial run cannot support a cell's fidelity claim.
	if !strings.HasPrefix(run.Result, "PASS") {
		return fmt.Errorf("result %q is not PASS", run.Result)
	}
	if !recordedAtPattern.MatchString(run.RecordedAt) {
		return fmt.Errorf("recordedAt %q is not YYYY-MM-DD", run.RecordedAt)
	}
	// The platform is the actual execution target the shells ran on. It must
	// agree with the recorded host and architecture; otherwise a locally
	// recorded amd64 run could be relabelled as an arm64 observation.
	if run.Platform != run.HostOS+"/"+run.Arch {
		return fmt.Errorf("platform %q does not match %s/%s", run.Platform, run.HostOS, run.Arch)
	}
	switch run.Harness {
	case "local":
		if run.HostOS != "darwin" {
			return fmt.Errorf("local observation host %q is not darwin", run.HostOS)
		}
		if run.Runtime == "" {
			return fmt.Errorf("local observation records no runtime version")
		}
	case "docker":
		if run.HostOS != "linux" {
			return fmt.Errorf("docker observation host %q is not linux", run.HostOS)
		}
		if run.Image == "" {
			return fmt.Errorf("docker observation records no image")
		}
		if !imageDigestPattern.MatchString(run.ImageDigest) {
			return fmt.Errorf("docker observation records no sha256 image digest")
		}
		if run.Runtime == "" {
			return fmt.Errorf("docker observation records no runtime version")
		}
	case "github-actions":
		if run.HostOS != "windows" && run.HostOS != "darwin" {
			return fmt.Errorf("runner observation host %q is not Windows or macOS", run.HostOS)
		}
		if run.Runner == "" {
			return fmt.Errorf("runner observation records no runner label")
		}
	case "vm":
		if run.HostOS != "freebsd" {
			return fmt.Errorf("VM observation host %q is not freebsd", run.HostOS)
		}
		if run.Image == "" || run.Runner == "" {
			return fmt.Errorf("VM observation records no image or hypervisor")
		}
	case "ssh":
		if run.HostOS != "freebsd" && run.HostOS != "linux" {
			return fmt.Errorf("SSH observation host %q is not freebsd or linux", run.HostOS)
		}
		if run.Target == "" {
			return fmt.Errorf("SSH observation records no target")
		}
	default:
		return fmt.Errorf("unknown harness %q", run.Harness)
	}
	return nil
}

func validateHostShellCell(cell matrixCell, paths map[string]captureMatrixPath,
	runs map[string]matrixObservedRun, testSource string,
	jobs map[string]releaseWorkflowJob) error {
	if !slices.Contains([]string{"linux", "darwin", "windows", "freebsd"}, cell.Host) {
		return fmt.Errorf("unknown Host %q", cell.Host)
	}
	if !slices.Contains([]string{"amd64", "arm64"}, cell.Arch) {
		return fmt.Errorf("unknown architecture %q", cell.Arch)
	}
	if !slices.Contains([]string{"zsh", "bash", "fish", "nu", "xonsh", "powershell"}, cell.Shell) {
		return fmt.Errorf("unknown shell %q", cell.Shell)
	}
	path, ok := paths[cell.Path]
	if !ok {
		return fmt.Errorf("names missing path fixture %q", cell.Path)
	}
	// BSD has no local PTY implementation (platform/pty returns
	// ErrUnsupported), so a BSD cell must be exercised through the product's
	// SSH transport against a FreeBSD account. A local BSD path here would
	// describe an acceptance step that cannot run.
	wantKind := "local"
	if cell.Host == "freebsd" {
		wantKind = "ssh"
	}
	if path.Kind != wantKind {
		if cell.Host == "freebsd" {
			return fmt.Errorf("uses %q, but BSD acceptance must use the SSH product path, not a local PTY", cell.Path)
		}
		return fmt.Errorf("uses the non-local path %q", cell.Path)
	}
	if path.Kind == "ssh" {
		if path.RemoteShell == "" {
			return fmt.Errorf("uses SSH path %q, which names no remote shell", cell.Path)
		}
		if !pathShellMatches(cell.Shell, path.RemoteShell) {
			return fmt.Errorf("shell %q does not match path %q remote shell %q",
				cell.Shell, cell.Path, path.RemoteShell)
		}
	} else if path.Shell != "" && !pathShellMatches(cell.Shell, path.Shell) {
		return fmt.Errorf("shell %q does not match path %q shell %q", cell.Shell, cell.Path, path.Shell)
	}
	// An empty list is a real value: a shell that takes no startup flags.
	// A missing field is not, so the cell cannot silently omit them.
	if cell.Startup == nil {
		return fmt.Errorf("records no exact startup flags")
	}
	wantStartup := pathStartup(path)
	if !reflect.DeepEqual(cell.Startup, wantStartup) {
		return fmt.Errorf("records startup flags %v, but path %q starts with %v",
			cell.Startup, cell.Path, wantStartup)
	}
	if cell.Evidence == "" {
		return fmt.Errorf("has no evidence note")
	}
	switch cell.Tier {
	case 1:
		if !cellHookExpected(cell.Host, cell.Shell) {
			return fmt.Errorf("Tier 1 has no local hook on %s", cell.Host)
		}
		if !path.WantHookExpected {
			return fmt.Errorf("Tier 1 uses a path that installs no hook")
		}
	case 2, 3:
		if path.Kind == "ssh" && path.WantHookExpected {
			return fmt.Errorf("Tier %d uses an SSH path that keeps the wrapper", cell.Tier)
		}
		if cellHookExpected(cell.Host, cell.Shell) && path.WantHookExpected {
			return fmt.Errorf("Tier %d uses a path that installs a supported hook", cell.Tier)
		}
	default:
		return fmt.Errorf("unknown capture tier %d", cell.Tier)
	}
	switch cell.Verification {
	case "observed":
		if cell.Observed == nil {
			return fmt.Errorf("is observed but has no observation")
		}
		if cell.Manual != nil {
			return fmt.Errorf("is observed but also carries manual evidence")
		}
		// A release may re-run an observed cell; the CI object is the
		// release-time job, and it is validated with the same rules as a cell
		// that has no local observation.
		if cell.CI != nil {
			if err := validateCellCI(cell, *cell.CI, jobs, testSource); err != nil {
				return fmt.Errorf("CI evidence: %w", err)
			}
		}
		if len(cell.Observed.Runs) == 0 {
			return fmt.Errorf("is observed but records no observation run")
		}
		if err := validateCellFidelity(cell.Observed.Fidelity); err != nil {
			return fmt.Errorf("observed fidelity: %w", err)
		}
		if err := validateTierFidelity(cell.Tier, cell.Observed.Fidelity); err != nil {
			return fmt.Errorf("observed fidelity: %w", err)
		}
		if !testDefinedInSource(testSource, cell.Observed.Test) {
			return fmt.Errorf("cites %s, which no test in this package defines", cell.Observed.Test)
		}
		if !testCoversShell(cell.Observed.Test, cell.Shell) {
			return fmt.Errorf("cites %s, which does not cover %s", cell.Observed.Test, cell.Shell)
		}
		for _, id := range cell.Observed.Runs {
			run, ok := runs[id]
			if !ok {
				return fmt.Errorf("cites missing observation run %q", id)
			}
			if run.HostOS != cell.Host || run.Arch != cell.Arch {
				return fmt.Errorf("observed on %s/%s by run %q, want %s/%s",
					run.HostOS, run.Arch, id, cell.Host, cell.Arch)
			}
			// A command may select one subtest of a shared POSIX test, as
			// TestRealShellSessionCapturesTierOneEndToEnd/bash does. That
			// selector is the run's real shell, so a zsh cell cannot cite it.
			for _, selector := range testSubtestSelectors(run.Command, cell.Observed.Test) {
				if selector != cell.Shell {
					return fmt.Errorf("run %q selected %s, but the cell is %s", id, selector, cell.Shell)
				}
			}
			if shellVersion(run, cell.Shell) == "" {
				return fmt.Errorf("run %q records no %s version", id, cell.Shell)
			}
			if !strings.Contains(run.Command, cell.Observed.Test) {
				return fmt.Errorf("run %q does not run %s", id, cell.Observed.Test)
			}
		}
	case "ci":
		if cell.Observed != nil || cell.Manual != nil {
			return fmt.Errorf("is CI-provisioned but also carries observed or manual evidence")
		}
		if cell.CI == nil {
			return fmt.Errorf("is CI-provisioned but records no release job")
		}
		if err := validateCellCI(cell, *cell.CI, jobs, testSource); err != nil {
			return err
		}
	case "manual", "planned":
		if cell.Observed != nil || cell.CI != nil {
			return fmt.Errorf("%s cell carries observation or CI evidence", cell.Verification)
		}
		if cell.Manual == nil {
			return fmt.Errorf("%s cell has no documented check", cell.Verification)
		}
		if cell.Manual.Runner == "" || len(cell.Manual.Steps) == 0 {
			return fmt.Errorf("%s cell has no runner or steps", cell.Verification)
		}
		if err := validateCellFidelity(cell.Manual.ExpectedFidelity); err != nil {
			return fmt.Errorf("expected fidelity: %w", err)
		}
		if err := validateTierFidelity(cell.Tier, cell.Manual.ExpectedFidelity); err != nil {
			return fmt.Errorf("expected fidelity: %w", err)
		}
		if err := validateManualShellBinding(cell, testSource); err != nil {
			return err
		}
		if cell.Verification == "planned" && cell.ReleaseBlocking {
			return fmt.Errorf("a release-blocking cell has only a planned check")
		}
	default:
		return fmt.Errorf("unknown verification %q", cell.Verification)
	}
	return nil
}

// validateCellCI binds a cell to a real release-workflow job. The job must run
// on the recorded runner, run the cell's test, name the per-shell binding, and
// sit in the publish dependency chain without a condition that could skip it.
func validateCellCI(cell matrixCell, ci cellCI, jobs map[string]releaseWorkflowJob,
	testSource string) error {
	if err := validateCellFidelity(ci.ExpectedFidelity); err != nil {
		return fmt.Errorf("expected fidelity: %w", err)
	}
	if err := validateTierFidelity(cell.Tier, ci.ExpectedFidelity); err != nil {
		return fmt.Errorf("expected fidelity: %w", err)
	}
	if ci.Workflow != ".github/workflows/release.yml" {
		return fmt.Errorf("CI evidence names workflow %q, want the release workflow", ci.Workflow)
	}
	if ci.Platform != cell.Host+"/"+cell.Arch {
		return fmt.Errorf("CI platform %q does not match %s/%s", ci.Platform, cell.Host, cell.Arch)
	}
	if ci.Runner == "" || len(ci.Tests) == 0 || ci.Command == "" {
		return fmt.Errorf("CI evidence has no runner, test, or command")
	}
	for _, test := range ci.Tests {
		if !testDefinedInSource(testSource, test) {
			return fmt.Errorf("CI evidence cites %s, which no test in this package defines", test)
		}
		if !testCoversShell(test, cell.Shell) {
			return fmt.Errorf("CI evidence cites %s, which does not cover %s", test, cell.Shell)
		}
		if !strings.Contains(ci.Command, test) {
			return fmt.Errorf("CI command does not run %s", test)
		}
	}
	job, ok := jobs[ci.Job]
	if !ok {
		return fmt.Errorf("release workflow has no job %q", ci.Job)
	}
	if job.HasIf {
		return fmt.Errorf("release job %q has a condition and can be skipped", ci.Job)
	}
	if job.RunsOn != ci.Runner {
		return fmt.Errorf("release job %q runs on %q, but the cell records %q",
			ci.Job, job.RunsOn, ci.Runner)
	}
	if !releaseReachable(jobs, ci.Job) {
		return fmt.Errorf("release job %q is not in the publish dependency chain", ci.Job)
	}
	for _, test := range ci.Tests {
		if !strings.Contains(job.Text, test) {
			return fmt.Errorf("release job %q does not run %s", ci.Job, test)
		}
	}
	for _, requirement := range ci.Requires {
		if !strings.Contains(job.Text, requirement) {
			return fmt.Errorf("release job %q does not bind the cell with %q", ci.Job, requirement)
		}
	}
	return nil
}

// shellTestCoverage binds each real-shell test to the shells it actually
// drives. A cell cannot borrow another shell's test even when the cited run
// happens to record both versions.
var shellTestCoverage = map[string][]string{
	"TestRealShellSessionCapturesTierOneEndToEnd":          {"zsh", "bash", "fish"},
	"TestRealShellCapture":                                 {"zsh", "bash", "fish"},
	"TestRealNushellSessionCapturesTierOneEndToEnd":        {"nu"},
	"TestRealXonshSessionCapturesTierOneEndToEnd":          {"xonsh"},
	"TestRealPowerShellLocalHookCapturesTierOne":           {"powershell"},
	"TestRealTierThreeFallbackCapturesTypedInput":          {"zsh"},
	"TestRealPowerShellTierThreeFallback":                  {"powershell"},
	"TestRealNushellWithoutHookKeepsNativeCapture":         {"nu"},
	"TestRealXonshWithoutHookKeepsNativeFallback":          {"xonsh"},
	"TestRealSSHRemoteHookCapturesTierOne":                 {"zsh", "bash", "fish"},
	"TestRealSSHRemoteTierThreeFallback":                   {"zsh"},
	"TestRealSSHRemoteNativeFallback":                      {"nu", "xonsh"},
	"TestRealBashCapturesACommandHiddenFromHistory":        {"bash"},
	"TestRealBashSourcesTheProfileAndBashrcOnce":           {"bash"},
	"TestRealFishPrivateModeStaysUncaptured":               {"fish"},
	"TestRealShellSessionCapturesMultilineCommandsOnce":    {"zsh", "bash", "fish"},
	"TestRealShellSessionCapturesAnEarlyCommandOnce":       {"zsh", "bash", "fish"},
	"TestRealShellSessionCapturesCommandsWhenTheHookSkips": {"zsh", "bash", "fish"},
	"TestRealShellSessionCapturesTypeAheadOnce":            {"zsh", "bash", "fish"},
	"TestRealShellSessionCapturesASlowMultilineOnce":       {"zsh", "bash", "fish"},
	"TestRealNestedShellFallsBackToTypedCapture":           {"zsh", "bash"},
	"TestRealZshWithoutEncoderKeepsTheTypedFallback":       {"zsh"},
	"TestRealBashWithoutEncoderSurvivesSetE":               {"bash"},
	"TestRealZshWithoutEncoderEmitsNoHandshake":            {"zsh"},
	"TestRealZshTmuxAttachPathFallsBackToTypedCapture":     {"zsh", "bash"},
	"TestRealShellSkipsWhenAnIntegrationIsActive":          {"zsh", "bash", "fish"},
	"TestRealNestedShellCapturesBackToBackInnerCommands":   {"zsh", "bash", "fish"},
}

func testCoversShell(test, shell string) bool {
	for _, covered := range shellTestCoverage[test] {
		if covered == shell {
			return true
		}
	}
	return false
}

// testSubtestSelectors returns the /shell selectors a go test command applies
// to a shared test, such as TestRealShellSessionCapturesTierOneEndToEnd/bash.
func testSubtestSelectors(command, test string) []string {
	var selectors []string
	rest := command
	for {
		index := strings.Index(rest, test)
		if index < 0 {
			return selectors
		}
		rest = rest[index+len(test):]
		if !strings.HasPrefix(rest, "/") {
			continue
		}
		rest = rest[1:]
		end := strings.IndexAny(rest, ` "'\`)
		if end < 0 {
			end = len(rest)
		}
		selector := rest[:end]
		if selector != "" {
			selectors = append(selectors, selector)
		}
	}
}

// validateManualShellBinding keeps a manual step honest about the shell it
// exercises: a zsh cell must name zsh, and any cited real-shell test must
// cover that shell.
func validateManualShellBinding(cell matrixCell, testSource string) error {
	text := strings.ToLower(cell.Manual.Runner + "\n" + strings.Join(cell.Manual.Steps, "\n"))
	names := map[string][]string{
		"zsh":        {"zsh"},
		"bash":       {"bash"},
		"fish":       {"fish"},
		"nu":         {"nushell", " nu "},
		"xonsh":      {"xonsh"},
		"powershell": {"powershell", "pwsh"},
	}
	matched := false
	for _, candidate := range names[cell.Shell] {
		if strings.Contains(text, candidate) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("manual steps do not name the cell's shell %s", cell.Shell)
	}
	for _, name := range citedRealTest.FindAllString(text, -1) {
		if !testDefinedInSource(testSource, name) {
			return fmt.Errorf("manual steps cite %s, which no test in this package defines", name)
		}
		if !testCoversShell(name, cell.Shell) {
			return fmt.Errorf("manual steps cite %s, which does not cover %s", name, cell.Shell)
		}
	}
	return nil
}

func validateCellFidelity(fidelity cellFidelity) error {
	for name, value := range map[string]string{
		"cwd": fidelity.Cwd, "exit": fidelity.Exit, "duration": fidelity.Duration,
	} {
		switch value {
		case "exact", "observed", "missing":
		default:
			return fmt.Errorf("unknown %s value %q", name, value)
		}
	}
	return nil
}

// pathStartup returns the flags the planned command adds after the shell
// executable. A generated bootstrap is represented by <bootstrap> in the
// fixture, because its text is not a startup flag the cell can name exactly.
// Comparing this with the cell's startup keeps the two records from drifting.
func pathStartup(path captureMatrixPath) []string {
	if path.Kind == "ssh" {
		return path.RemoteStartup
	}
	var flags []string
	bootstrap := false
	switch {
	case len(path.WantCommandPrefix) > 0:
		flags = path.WantCommandPrefix[1:]
		bootstrap = true
	case len(path.WantCommand) > 0:
		flags = path.WantCommand[1:]
	}
	startup := make([]string, 0, len(flags)+1)
	startup = append(startup, flags...)
	if bootstrap {
		startup = append(startup, "<bootstrap>")
	}
	return startup
}

// validateTierFidelity rejects a tier claim that contradicts the capture
// mechanism. Tier 1 is the authoritative hook, so all three fields are exact.
// Tier 3 is typed input plus prompt detection, so it cannot know the cwd or
// exit code and cannot measure an exact duration.
func validateTierFidelity(tier int, fidelity cellFidelity) error {
	switch tier {
	case 1:
		if fidelity.Cwd != "exact" || fidelity.Exit != "exact" || fidelity.Duration != "exact" {
			return fmt.Errorf("Tier 1 has cwd=%q exit=%q duration=%q, want exact for all three",
				fidelity.Cwd, fidelity.Exit, fidelity.Duration)
		}
	case 2:
		// Tier 2 is the shell's own OSC 133/7 integration. Netcatty never
		// supplies an exit code there, and the native finish is not an exact
		// Netcatty duration. The cwd may be known (OSC 7) or absent.
		if fidelity.Exit != "missing" {
			return fmt.Errorf("Tier 2 has exit=%q, want missing (no Netcatty exit code)",
				fidelity.Exit)
		}
		if fidelity.Duration == "exact" {
			return fmt.Errorf("Tier 2 has duration=%q, want a native measurement or missing", fidelity.Duration)
		}
		if fidelity.Cwd != "exact" && fidelity.Cwd != "missing" {
			return fmt.Errorf("Tier 2 has cwd=%q, want exact or missing", fidelity.Cwd)
		}
	case 3:
		if fidelity.Cwd != "missing" || fidelity.Exit != "missing" || fidelity.Duration == "exact" {
			return fmt.Errorf("Tier 3 has cwd=%q exit=%q duration=%q, want missing cwd and exit and a non-exact duration",
				fidelity.Cwd, fidelity.Exit, fidelity.Duration)
		}
	}
	return nil
}

// cellHookExpected reports whether the local planner can install its session
// hook for this Host and shell. Windows is PowerShell-only; every POSIX Host
// supports every shell in the matrix, including an explicitly started
// PowerShell.
func cellHookExpected(host, shell string) bool {
	if host == "windows" {
		return shell == "powershell"
	}
	switch shell {
	case "zsh", "bash", "fish", "nu", "xonsh", "powershell":
		return true
	default:
		return false
	}
}

func pathShellMatches(cellShell, pathShell string) bool {
	if cellShell == "powershell" {
		return pathShell == "pwsh" || pathShell == "powershell"
	}
	return cellShell == pathShell
}

func shellVersion(run matrixObservedRun, shell string) string {
	if version := run.ShellVersions[shell]; version != "" {
		return version
	}
	if shell == "powershell" {
		for _, key := range []string{"pwsh", "powershell.exe", "powershell"} {
			if version := run.ShellVersions[key]; version != "" {
				return version
			}
		}
	}
	return ""
}

func validateHostShellReleaseGates(matrix captureMatrix) error {
	cells := map[string]matrixCell{}
	for _, cell := range matrix.Cells {
		cells[cell.Name] = cell
	}
	required := map[string]hostShellGate{}
	categories := map[string]bool{}
	for _, gate := range requiredHostShellGates {
		required[gate.Name] = gate
		categories[gate.Category] = true
	}
	for _, category := range requiredGateCategories {
		if !categories[category] {
			return fmt.Errorf("no release gate requires a %s cell", category)
		}
	}
	for _, gate := range requiredHostShellGates {
		cell, ok := cells[gate.Name]
		if !ok {
			return fmt.Errorf("release-blocking cell %q is missing", gate.Name)
		}
		if !cell.ReleaseBlocking {
			return fmt.Errorf("required cell %q is not marked releaseBlocking", gate.Name)
		}
		if cell.Tier != gate.Tier {
			return fmt.Errorf("release-blocking cell %q is classified Tier %d, required Tier %d",
				gate.Name, cell.Tier, gate.Tier)
		}
		if cell.Verification == "planned" {
			return fmt.Errorf("release-blocking cell %q has no observed or documented manual check", gate.Name)
		}
	}
	for _, cell := range matrix.Cells {
		if !cell.ReleaseBlocking {
			continue
		}
		if _, ok := required[cell.Name]; !ok {
			return fmt.Errorf("cell %q is releaseBlocking but has no release gate requirement", cell.Name)
		}
	}
	return nil
}
