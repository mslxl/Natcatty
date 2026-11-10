package wasm_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// TestHostEnvironmentIsInjected proves the now_ns and random_get imports read
// the host-provided environment, not ambient wall clock and crypto/rand.
func TestHostEnvironmentIsInjected(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "host-env.wasm"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var output []string
	rt := wasm.NewRuntime(
		wasm.WithLimits(contractLimits),
		wasm.WithHostEnv(wasm.HostEnv{
			Now:    func() int64 { return 1000 },
			Random: bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7}),
		}),
	)
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	instance, err := rt.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name:           "host-env",
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}, appplugin.HostSink{
		Output: func(data []byte) { output = append(output, string(data)) },
	})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close(context.Background()) })
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := instance.Dispatch(context.Background(), "plugin.acme.hello.greet"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := instance.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if err := instance.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if len(output) != 1 || output[0] != "now=1000 rand=0001020304050607" {
		t.Fatalf("output = %v, want the injected clock and random bytes", output)
	}
}

// TestHostEnvironmentDefaultsRun proves a module using the new imports works
// without an injected environment.
func TestHostEnvironmentDefaultsRun(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	data, err := os.ReadFile(filepath.Join("testdata", "host-env.wasm"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var output []string
	instance, err := rt.Instantiate(context.Background(), appplugin.ModuleSpec{
		Name:           "host-env",
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}, appplugin.HostSink{
		Output: func(data []byte) { output = append(output, string(data)) },
	})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close(context.Background()) })
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := instance.Dispatch(context.Background(), "plugin.acme.hello.greet"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("output = %v", output)
	}
	if len(output[0]) < len("now=0 rand=") {
		t.Fatalf("output = %q", output[0])
	}
}
