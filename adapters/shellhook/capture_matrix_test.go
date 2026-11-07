package shellhook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// captureMatrix is the ticket 07 fixture: every shell, transport, and fallback
// path with the exact startup flags and the field fidelity it produces.
type captureMatrix struct {
	Description    string              `json:"description"`
	FidelityLegend string              `json:"fidelityLegend"`
	Paths          []captureMatrixPath `json:"paths"`
}

type captureMatrixPath struct {
	Name              string            `json:"name"`
	Platforms         []string          `json:"platforms"`
	Kind              string            `json:"kind"`
	Shell             string            `json:"shell"`
	Args              []string          `json:"args"`
	HookEnabled       bool              `json:"hookEnabled"`
	SpecEnv           map[string]string `json:"specEnv"`
	WantHookExpected  bool              `json:"wantHookExpected"`
	RuntimeSkip       bool              `json:"runtimeSkip"`
	WantCommand       []string          `json:"wantCommand"`
	WantEnv           map[string]string `json:"wantEnv"`
	WantFiles         map[string]string `json:"wantFiles"`
	WantRemoteContain []string          `json:"wantRemoteContains"`
	Fidelity          struct {
		Tier     string `json:"tier"`
		Cwd      string `json:"cwd"`
		Exit     string `json:"exit"`
		Duration string `json:"duration"`
	} `json:"fidelity"`
	Evidence string `json:"evidence"`
}

// TestCaptureMatrix keeps the fixture honest: it replans every local path on
// each recorded platform and checks the exact command, environment, and hook
// files, and checks that every remote path's wrapper carries the recorded
// invocation. Fidelity values are evidence claims; the test checks them for
// consistency with the hook each path installs.
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
// installs, and an unrecognized fidelity value.
func checkCaptureFidelity(t *testing.T, entry captureMatrixPath) {
	t.Helper()
	switch entry.Fidelity.Tier {
	case "1":
		if !entry.WantHookExpected || entry.RuntimeSkip {
			t.Fatalf("tier 1 claims a hook this path does not install")
		}
	case "2", "3", "2/3":
		if entry.WantHookExpected && !entry.RuntimeSkip {
			t.Fatalf("tier %q claims a fallback on a path that installs the hook", entry.Fidelity.Tier)
		}
	default:
		t.Fatalf("unknown fidelity tier %q", entry.Fidelity.Tier)
	}
	for name, value := range map[string]string{
		"cwd": entry.Fidelity.Cwd, "exit": entry.Fidelity.Exit, "duration": entry.Fidelity.Duration,
	} {
		switch value {
		case "exact", "observed", "missing", "manual":
		default:
			t.Fatalf("unknown %s fidelity %q", name, value)
		}
	}
}

// checkLocalCapturePath plans the recorded local invocation on every recorded
// platform and compares the result with the fixture.
func checkLocalCapturePath(t *testing.T, entry captureMatrixPath) {
	t.Helper()
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

// checkRemoteCapturePath plans the SSH path and checks the recorded shell
// invocation is present in the wrapper that runs on the Host.
func checkRemoteCapturePath(t *testing.T, entry captureMatrixPath) {
	t.Helper()
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
			if plan.HookExpected != entry.WantHookExpected {
				t.Fatalf("HookExpected = %t, want %t", plan.HookExpected, entry.WantHookExpected)
			}
			if !entry.HookEnabled {
				return
			}
			if plan.Spec.RemoteCommand == "" {
				t.Fatal("the SSH path has no remote wrapper")
			}
			script := remoteWrapperScript()
			for _, want := range entry.WantRemoteContain {
				if !strings.Contains(script, want) {
					t.Fatalf("the remote wrapper is missing %q", want)
				}
			}
			if entry.RuntimeSkip {
				if strings.Contains(script, entry.Shell+")") {
					t.Fatalf("the wrapper has a hook branch for the unsupported shell %q", entry.Shell)
				}
				if !strings.Contains(script, "NetcattyHookSkip") {
					t.Fatal("the wrapper never announces an unsupported-shell skip")
				}
			} else if !strings.Contains(script, entry.Shell+")\n") {
				t.Fatalf("the wrapper has no %q branch", entry.Shell)
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
