package wasm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

func TestInstantiateRejectsAnUnavailablePlatform(t *testing.T) {
	runtime := &Runtime{status: appplugin.RuntimeStatus{
		GOOS: "windows", GOARCH: "arm64", Engine: appplugin.EngineUnavailable,
	}}
	_, err := runtime.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name: "anything", WASM: []byte{0}, MemoryMaxBytes: 64 << 20,
	}, appplugin.HostSink{})
	if !errors.Is(err, appplugin.ErrPluginsUnavailable) {
		t.Fatalf("Instantiate error = %v, want ErrPluginsUnavailable", err)
	}
}

func TestSelectEngineUnsupportedPlatform(t *testing.T) {
	selection := selectEngine("windows", "arm64", true,
		func() error { return nil },
		func(wazero.RuntimeConfig) error { return nil })
	if selection.status.Engine != appplugin.EngineUnavailable {
		t.Fatalf("engine = %s, want unavailable", selection.status.Engine)
	}
	if selection.config != nil {
		t.Fatal("unsupported platform selected a runtime config")
	}
	if selection.status.Available() {
		t.Fatal("unsupported status reports available")
	}
}

func TestSelectEngineOtherPlatformsTrustAuto(t *testing.T) {
	probed, selfTested := false, false
	selection := selectEngine("linux", "amd64", true,
		func() error { probed = true; return nil },
		func(wazero.RuntimeConfig) error { selfTested = true; return nil })
	if !probed {
		t.Fatal("the executable-memory probe did not run")
	}
	if selfTested {
		t.Fatal("the darwin/amd64 self-test ran on linux/amd64")
	}
	if selection.status.Engine != appplugin.EngineCompiler || selection.explicitInterpreter {
		t.Fatalf("selection = %+v, want the compiler", selection.status)
	}
	if selection.config == nil {
		t.Fatal("compiler selection has no runtime config")
	}
}

func TestSelectEngineReportsInterpreterWhenExecutableMemoryIsBlocked(t *testing.T) {
	selection := selectEngine("windows", "amd64", true,
		func() error { return errors.New("policy denied executable memory") },
		func(wazero.RuntimeConfig) error { return nil })
	if selection.status.Engine != appplugin.EngineInterpreter {
		t.Fatalf("engine = %s, want interpreter", selection.status.Engine)
	}
	if !selection.status.Compatibility {
		t.Fatal("interpreter fallback is not marked compatibility mode")
	}
	if selection.explicitInterpreter {
		t.Fatal("a non-darwin platform used the explicit interpreter config instead of auto")
	}
	if !strings.Contains(selection.status.Reason, "executable memory") {
		t.Fatalf("reason = %q", selection.status.Reason)
	}
	if selection.config == nil {
		t.Fatal("interpreter selection has no runtime config")
	}
}

func TestSelectEngineReportsInterpreterWithoutSSE41(t *testing.T) {
	selection := selectEngine("linux", "amd64", false,
		func() error { return nil },
		func(wazero.RuntimeConfig) error { return nil })
	if selection.status.Engine != appplugin.EngineInterpreter || selection.config == nil {
		t.Fatalf("selection = %+v, want auto interpreter", selection.status)
	}
	if !strings.Contains(selection.status.Reason, "SSE4.1") {
		t.Fatalf("reason = %q", selection.status.Reason)
	}
}

func TestSelectEngineDarwinAMD64SelfTest(t *testing.T) {
	t.Run("passing self-test uses the compiler", func(t *testing.T) {
		selection := selectEngine("darwin", "amd64", true,
			func() error { return nil },
			func(config wazero.RuntimeConfig) error {
				if config == nil {
					t.Fatal("the self-test received a nil config")
				}
				return nil
			})
		if selection.status.Engine != appplugin.EngineCompiler || selection.explicitInterpreter {
			t.Fatalf("selection = %+v, want the compiler", selection.status)
		}
	})

	t.Run("failing self-test falls back to the interpreter", func(t *testing.T) {
		selection := selectEngine("darwin", "amd64", true,
			func() error { return nil },
			func(wazero.RuntimeConfig) error { return errors.New("jit unavailable") })
		if selection.status.Engine != appplugin.EngineInterpreter || !selection.explicitInterpreter {
			t.Fatalf("selection = %+v, want the explicit interpreter", selection.status)
		}
		if !selection.status.Compatibility {
			t.Fatal("fallback is not marked compatibility mode")
		}
		if !strings.Contains(selection.status.Reason, "jit unavailable") {
			t.Fatalf("reason = %q", selection.status.Reason)
		}
		if selection.config == nil {
			t.Fatal("fallback selection has no runtime config")
		}
	})

	t.Run("blocked executable memory also falls back", func(t *testing.T) {
		selection := selectEngine("darwin", "amd64", true,
			func() error { return errors.New("mprotect permission denied") },
			func(wazero.RuntimeConfig) error { return nil })
		if selection.status.Engine != appplugin.EngineInterpreter || !selection.explicitInterpreter {
			t.Fatalf("selection = %+v, want the explicit interpreter", selection.status)
		}
		if !strings.Contains(selection.status.Reason, "mprotect permission denied") {
			t.Fatalf("reason = %q", selection.status.Reason)
		}
	})

	t.Run("missing SSE4.1 falls back to the explicit interpreter", func(t *testing.T) {
		selection := selectEngine("darwin", "amd64", false,
			func() error { return nil },
			func(wazero.RuntimeConfig) error { return nil })
		if selection.status.Engine != appplugin.EngineInterpreter || !selection.explicitInterpreter {
			t.Fatalf("selection = %+v, want the explicit interpreter", selection.status)
		}
		if !strings.Contains(selection.status.Reason, "SSE4.1") {
			t.Fatalf("reason = %q", selection.status.Reason)
		}
	})
}

// TestEngineSelfTestModule proves the bundled 41-byte module is valid and
// returns 42 under both engines. It is what the darwin/amd64 startup check
// runs.
func TestEngineSelfTestModule(t *testing.T) {
	for _, test := range []struct {
		name   string
		config wazero.RuntimeConfig
	}{
		{"auto", wazero.NewRuntimeConfig()},
		{"interpreter", wazero.NewRuntimeConfigInterpreter()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := runEngineSelfTest(test.config); err != nil {
				t.Fatalf("runEngineSelfTest: %v", err)
			}
		})
	}
}
