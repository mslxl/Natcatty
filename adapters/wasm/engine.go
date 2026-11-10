package wasm

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// engineSelfTestTimeout bounds the darwin/amd64 startup compiler check. The
// module is tiny, so exceeding this is itself a failure worth falling back on.
const engineSelfTestTimeout = 5 * time.Second

// engineSelfTestModule is a 41-byte core module:
//
//	(module (func (export "selftest") (result i32) i32.const 42))
//
// It has no imports, no memory, and no start function, so a successful call
// exercises compilation and execution without depending on the plugin ABI.
var engineSelfTestModule = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic and version
	0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7f, // type: () -> i32
	0x03, 0x02, 0x01, 0x00, // function 0 uses type 0
	0x07, 0x0c, 0x01, 0x08, 's', 'e', 'l', 'f', 't', 'e', 's', 't', 0x00, 0x00, // export
	0x0a, 0x06, 0x01, 0x04, 0x00, 0x41, 0x2a, 0x0b, // i32.const 42; end
}

// engineSelection is the engine config plus the diagnostics state it implies.
type engineSelection struct {
	config wazero.RuntimeConfig
	status appplugin.RuntimeStatus
	// explicitInterpreter is true when selectEngine constructed the
	// interpreter config itself instead of trusting wazero's automatic
	// fallback. Tests use it to prove the darwin/amd64 path.
	explicitInterpreter bool
}

// selectEngine chooses the runtime config. It never forces the compiler:
// every supported path starts from wazero's automatic selection. The
// darwin/amd64 slice additionally runs a bounded self-test and constructs the
// explicit interpreter config when the CPU gate, executable memory, or the
// self-test is unavailable, marking the process as compatibility mode. Other
// supported platforms trust wazero's own automatic fallback.
func selectEngine(goos, goarch string, cpuSupported bool, probe func() error,
	selfTest func(wazero.RuntimeConfig) error) engineSelection {
	status := appplugin.RuntimeStatus{
		GOOS:   goos,
		GOARCH: goarch,
		Engine: appplugin.EngineUnavailable,
	}
	if !appplugin.SupportsPlatform(goos, goarch) {
		status.Reason = "unsupported platform"
		return engineSelection{status: status}
	}

	probeErr := probe()
	auto := wazero.NewRuntimeConfig()
	blocked := compilerBlockedReason(probeErr, cpuSupported)

	if goos == "darwin" && goarch == "amd64" {
		selfTestErr := selfTest(auto)
		if blocked != "" || selfTestErr != nil {
			return engineSelection{
				config:              wazero.NewRuntimeConfigInterpreter(),
				status:              interpreterStatus(status, compilerFallbackReason(blocked, selfTestErr)),
				explicitInterpreter: true,
			}
		}
	}

	if blocked != "" {
		// wazero runs the same CPU and executable-memory checks while
		// auto-selecting, so this process will use the interpreter. Report it
		// as compatibility mode instead of claiming the compiler.
		return engineSelection{config: auto, status: interpreterStatus(status, blocked)}
	}

	status.Engine = appplugin.EngineCompiler
	return engineSelection{config: auto, status: status}
}

func interpreterStatus(status appplugin.RuntimeStatus, reason string) appplugin.RuntimeStatus {
	status.Engine = appplugin.EngineInterpreter
	status.Compatibility = true
	status.Reason = reason
	return status
}

func compilerBlockedReason(probeErr error, cpuSupported bool) string {
	switch {
	case probeErr != nil:
		return "the compiler cannot allocate executable memory: " + probeErr.Error()
	case !cpuSupported:
		return "the CPU lacks SSE4.1, which the amd64 compiler requires"
	default:
		return ""
	}
}

func compilerFallbackReason(blocked string, selfTestErr error) string {
	if blocked != "" && selfTestErr != nil {
		return blocked + "; compiler self-test failed: " + selfTestErr.Error()
	}
	if selfTestErr != nil {
		return "compiler self-test failed: " + selfTestErr.Error()
	}
	return blocked
}

// currentEngine runs selection once per process. NewRuntime is called by the
// app and by every adapter test; the self-test and mmap probe must not repeat.
var currentEngine = sync.OnceValue(func() engineSelection {
	return selectEngine(runtime.GOOS, runtime.GOARCH, compilerCPUFeatures(runtime.GOARCH),
		probeExecutableMemory, runEngineSelfTest)
})

// runEngineSelfTest compiles and calls the bundled module under config. The
// caller passes wazero's automatic config, so a working compiler is exercised
// on darwin/amd64 without ever forcing it.
func runEngineSelfTest(config wazero.RuntimeConfig) (err error) {
	// A compiler failure inside wazero may panic rather than return an error.
	// The self-test's purpose is to fall back, so contain it here instead of
	// crashing startup.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), engineSelfTestTimeout)
	defer cancel()
	engineRuntime := wazero.NewRuntimeWithConfig(ctx, config)
	defer func() { _ = engineRuntime.Close(context.Background()) }()

	compiled, err := engineRuntime.CompileModule(ctx, engineSelfTestModule)
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}
	defer func() { _ = compiled.Close(context.Background()) }()

	module, err := engineRuntime.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return fmt.Errorf("instantiate: %w", err)
	}
	selftest := module.ExportedFunction("selftest")
	if selftest == nil {
		return errors.New("the self-test module does not export selftest")
	}
	results, err := selftest.Call(ctx)
	if err != nil {
		return fmt.Errorf("call selftest: %w", err)
	}
	if len(results) != 1 || uint32(results[0]) != 42 {
		return fmt.Errorf("selftest returned %v, want 42", results)
	}
	return nil
}
