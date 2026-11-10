package wasm_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// TestRuntimeStatusReportsTheEngine proves the adapter reports the platform
// and a usable engine, and logs the one-line mode support needs. CI sets
// NATCATTY_PLUGIN_REQUIRE_COMPILER=1 for the darwin/amd64 Intel slice so that
// slice cannot ship without a recorded compiler smoke.
func TestRuntimeStatusReportsTheEngine(t *testing.T) {
	rt := wasm.NewRuntime()
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	status := rt.RuntimeStatus()
	if status.GOOS != runtime.GOOS || status.GOARCH != runtime.GOARCH {
		t.Fatalf("status platform = %s, want %s/%s", status.Platform(), runtime.GOOS, runtime.GOARCH)
	}
	if !appplugin.SupportsPlatform(status.GOOS, status.GOARCH) {
		t.Skipf("plugins are unavailable on %s; the smoke test only runs on supported targets", status.Platform())
	}
	if !status.Available() {
		t.Fatalf("status = %+v, want an available engine", status)
	}
	if status.Engine != appplugin.EngineCompiler && status.Engine != appplugin.EngineInterpreter {
		t.Fatalf("engine = %s, want compiler or interpreter", status.Engine)
	}
	if status.Compatibility != (status.Engine == appplugin.EngineInterpreter) {
		t.Fatalf("compatibility = %v with engine %s", status.Compatibility, status.Engine)
	}
	t.Log(status.StartupLogLine())
	if status.Notice() == "" && status.Engine == appplugin.EngineInterpreter {
		t.Fatal("interpreter mode has no diagnostics notice")
	}

	if os.Getenv("NATCATTY_PLUGIN_REQUIRE_COMPILER") == "1" && status.Engine != appplugin.EngineCompiler {
		t.Fatalf("the compiler is required on this slice but the runtime reports %s: %s",
			status.Engine, status.Reason)
	}
}
