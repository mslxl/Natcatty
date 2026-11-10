package plugin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
)

func TestRuntimeStatusPlatform(t *testing.T) {
	for _, test := range []struct {
		status appplugin.RuntimeStatus
		want   string
	}{
		{appplugin.RuntimeStatus{GOOS: "darwin", GOARCH: "arm64"}, "darwin/arm64"},
		{appplugin.RuntimeStatus{GOOS: "windows", GOARCH: "amd64"}, "windows/amd64"},
		{appplugin.RuntimeStatus{GOOS: "linux"}, "linux"},
		{appplugin.RuntimeStatus{GOARCH: "amd64"}, "amd64"},
		{appplugin.RuntimeStatus{}, "unknown"},
	} {
		if got := test.status.Platform(); got != test.want {
			t.Errorf("Platform(%+v) = %q, want %q", test.status, got, test.want)
		}
	}
}

func TestRuntimeStatusStartupLogLine(t *testing.T) {
	for _, test := range []struct {
		name   string
		status appplugin.RuntimeStatus
		want   string
	}{
		{
			name: "compiler",
			status: appplugin.RuntimeStatus{
				GOOS: "darwin", GOARCH: "arm64", Engine: appplugin.EngineCompiler,
			},
			want: "plugin runtime: engine=compiler platform=darwin/arm64",
		},
		{
			name: "interpreter compatibility",
			status: appplugin.RuntimeStatus{
				GOOS: "darwin", GOARCH: "amd64", Engine: appplugin.EngineInterpreter,
				Compatibility: true, Reason: "compiler self-test failed",
			},
			want: `plugin runtime: engine=interpreter platform=darwin/amd64 compatibility=true reason="compiler self-test failed"`,
		},
		{
			name: "unavailable",
			status: appplugin.RuntimeStatus{
				GOOS: "windows", GOARCH: "arm64", Engine: appplugin.EngineUnavailable,
				Reason: "unsupported platform",
			},
			want: `plugin runtime: engine=unavailable platform=windows/arm64 reason="unsupported platform"`,
		},
		{
			name:   "zero value",
			status: appplugin.RuntimeStatus{},
			want:   "plugin runtime: engine=unknown platform=unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.status.StartupLogLine(); got != test.want {
				t.Fatalf("StartupLogLine() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRuntimeStatusNoticeAndTimeoutHint(t *testing.T) {
	compiler := appplugin.RuntimeStatus{Engine: appplugin.EngineCompiler}
	if notice := compiler.Notice(); notice != "" {
		t.Fatalf("compiler Notice() = %q, want empty", notice)
	}
	if hint := compiler.TimeoutHint(); hint != "" {
		t.Fatalf("compiler TimeoutHint() = %q, want empty", hint)
	}

	interpreter := appplugin.RuntimeStatus{
		GOOS: "darwin", GOARCH: "amd64", Engine: appplugin.EngineInterpreter, Compatibility: true,
	}
	if notice := interpreter.Notice(); !strings.Contains(notice, "compatibility mode") ||
		!strings.Contains(notice, "Timeouts are unchanged") {
		t.Fatalf("interpreter Notice() = %q", notice)
	}
	if hint := interpreter.TimeoutHint(); !strings.Contains(hint, "timeouts are not relaxed") {
		t.Fatalf("interpreter TimeoutHint() = %q", hint)
	}

	unavailable := appplugin.RuntimeStatus{
		GOOS: "windows", GOARCH: "arm64", Engine: appplugin.EngineUnavailable,
	}
	if notice := unavailable.Notice(); !strings.Contains(notice, "windows/arm64") ||
		!strings.Contains(notice, "rest of the app works") {
		t.Fatalf("unavailable Notice() = %q", notice)
	}
	if !strings.Contains((appplugin.RuntimeStatus{Engine: appplugin.EngineUnavailable}).Notice(),
		"this platform") {
		t.Fatal("unavailable Notice() without a platform does not say 'this platform'")
	}
}

func TestDeadlineErrorCarriesCompatibilityHint(t *testing.T) {
	err := &appplugin.DeadlineError{
		Operation: "dispatch", Limit: 2e9, Hint: "interpreter compatibility",
	}
	if got := err.Error(); !strings.Contains(got, "interpreter compatibility") {
		t.Fatalf("DeadlineError.Error() = %q, want the hint", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("DeadlineError no longer matches context.DeadlineExceeded")
	}
}

func TestUnavailableErrorMatchesSentinel(t *testing.T) {
	err := &appplugin.UnavailableError{GOOS: "windows", GOARCH: "arm64"}
	if !errors.Is(err, appplugin.ErrPluginsUnavailable) {
		t.Fatalf("UnavailableError does not match ErrPluginsUnavailable: %v", err)
	}
	if !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("UnavailableError.Error() = %q", err)
	}
}

// statusRuntime is a fake runtime that also reports an engine.
type statusRuntime struct {
	*fake.Runtime
	status appplugin.RuntimeStatus
}

func (r statusRuntime) RuntimeStatus() appplugin.RuntimeStatus { return r.status }

func TestContainerReportsRuntimeStatus(t *testing.T) {
	want := appplugin.RuntimeStatus{
		GOOS: "darwin", GOARCH: "amd64", Engine: appplugin.EngineInterpreter, Compatibility: true,
	}
	container := appplugin.NewContainer(statusRuntime{Runtime: fake.New(), status: want}, nil, nil)
	if got := container.RuntimeStatus(); got != want {
		t.Fatalf("RuntimeStatus() = %+v, want %+v", got, want)
	}

	plain := appplugin.NewContainer(fake.New(), nil, nil)
	if got := plain.RuntimeStatus(); got.Engine != appplugin.EngineUnknown {
		t.Fatalf("fake RuntimeStatus() = %+v, want unknown", got)
	}
	if got := (appplugin.RuntimeStatus{}).Available(); got {
		t.Fatal("the zero RuntimeStatus reports available")
	}
	if got := (&appplugin.Container{}).RuntimeStatus(); got.Engine != appplugin.EngineUnknown {
		t.Fatalf("zero Container RuntimeStatus() = %+v, want unknown", got)
	}
}
