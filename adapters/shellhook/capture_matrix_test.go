package shellhook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// captureMatrix is the ticket 07 fixture: every shell, transport, and fallback
// path with the exact startup flags and the field fidelity it produces. The
// verification value separates observed behavior (a real shell ran here) from
// manual checks (a real BSD or OpenSSH host is required) and planned behavior.
type captureMatrix struct {
	Description    string              `json:"description"`
	FidelityLegend string              `json:"fidelityLegend"`
	Paths          []captureMatrixPath `json:"paths"`
}

type captureMatrixPath struct {
	Name      string   `json:"name"`
	Platforms []string `json:"platforms"`
	Kind      string   `json:"kind"`
	Shell     string   `json:"shell"`
	// LoginShell is the account shell an SSH transport detects through the
	// non-executing SFTP probe. Empty means detection was unavailable.
	LoginShell       string            `json:"loginShell"`
	Args             []string          `json:"args"`
	HookEnabled      bool              `json:"hookEnabled"`
	SpecEnv          map[string]string `json:"specEnv"`
	WantHookExpected bool              `json:"wantHookExpected"`
	WantCommand      []string          `json:"wantCommand"`
	WantEnv          map[string]string `json:"wantEnv"`
	WantFiles        map[string]string `json:"wantFiles"`
	// WantRemoteWrapper reports whether the transport may send the POSIX
	// wrapper after it establishes the login shell.
	WantRemoteWrapper bool `json:"wantRemoteWrapper"`
	// WantRemoteInvocations records the exact shell invocation lines the
	// wrapper must contain for this login shell.
	WantRemoteInvocations []string `json:"wantRemoteInvocations"`
	// Verification is observed, manual, or planned for the whole path.
	Verification string `json:"verification"`
	Fidelity     struct {
		Tier     string `json:"tier"`
		Cwd      string `json:"cwd"`
		Exit     string `json:"exit"`
		Duration string `json:"duration"`
	} `json:"fidelity"`
	Evidence string `json:"evidence"`
}

// TestCaptureMatrix keeps the fixture honest: it replans every local path on
// each recorded platform and checks the exact command, environment, and hook
// files; it replans every remote path and applies the same login-shell
// decision the SSH transport applies before opening the terminal.
func TestCaptureMatrix(t *testing.T) {
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
		for name, value := range fields {
			if value == "manual" || value == "planned" {
				t.Fatalf("observed path calls %s %q", name, value)
			}
		}
	case "manual", "planned":
		// Nothing on this machine ran the path, so no field may claim an
		// observation.
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
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		source.Write(data)
	}
	for _, name := range names {
		if !strings.Contains(source.String(), "func "+name+"(") {
			t.Fatalf("observed evidence cites %s, which no test in this package defines", name)
		}
	}
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
			assertMatrixCommand(t, plan.Spec.Command, entry.WantCommand, shellPath, home, hookDir)
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
	return value
}

func assertMatrixCommand(t *testing.T, got, want []string, shellPath, home, hookDir string) {
	t.Helper()
	normalized := make([]string, len(got))
	for index, value := range got {
		normalized[index] = normalizeMatrixValue(value, shellPath, home, hookDir)
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("command = %v, want %v", normalized, want)
	}
}

func assertMatrixEnv(t *testing.T, got []string, specEnv, want map[string]string,
	shellPath, home, hookDir string) {
	t.Helper()
	expected := map[string]string{}
	for key, value := range specEnv {
		expected[key] = value
	}
	for key, value := range want {
		expected[key] = value
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
