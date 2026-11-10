package wasm_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// TestRuntimeSmoke is the release smoke test that runs on every supported
// platform and on both macOS slices. It drives the real wazero adapter, not a
// fake: a fixture module goes through init, dispatch, poll, and shutdown; the
// declared memory cap is enforced; a trap is contained; and one host-import
// round trip reads the injected clock and randomness.
func TestRuntimeSmoke(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	status := rt.RuntimeStatus()
	t.Log(status.StartupLogLine())
	if !status.Available() {
		t.Fatalf("the plugin runtime is unavailable: %s", status.Reason)
	}

	t.Run("fixture lifecycle", func(t *testing.T) {
		var logs, outputs []string
		instance, err := rt.Instantiate(context.Background(), smokeFixture(t, "hello"),
			appplugin.HostSink{
				Log:    func(_ appplugin.LogLevel, line string) { logs = append(logs, line) },
				Output: func(data []byte) { outputs = append(outputs, string(data)) },
			})
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		defer func() { _ = instance.Close(context.Background()) }()
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
		if len(outputs) != 1 || outputs[0] != "hello from plugin" {
			t.Fatalf("visible output = %v, want the fixture's greeting", outputs)
		}
		if len(logs) == 0 {
			t.Fatal("the host log import received no lines")
		}
	})

	t.Run("host-import round trip", func(t *testing.T) {
		var output []string
		envRuntime := wasm.NewRuntime(
			wasm.WithLimits(contractLimits),
			wasm.WithHostEnv(wasm.HostEnv{
				Now:    func() int64 { return 1000 },
				Random: bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7}),
			}),
		)
		t.Cleanup(func() { _ = envRuntime.Close(context.Background()) })
		instance, err := envRuntime.Instantiate(context.Background(), smokeFixture(t, "host-env"), appplugin.HostSink{
			Output: func(data []byte) { output = append(output, string(data)) },
		})
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		defer func() { _ = instance.Close(context.Background()) }()
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.greet"); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if len(output) != 1 || output[0] != "now=1000 rand=0001020304050607" {
			t.Fatalf("output = %v, want the injected clock and random bytes", output)
		}
	})

	t.Run("memory cap", func(t *testing.T) {
		spec := smokeFixture(t, "memory-growth")
		spec.MemoryMaxBytes = 1 << 20
		instance, err := rt.Instantiate(context.Background(), spec, appplugin.HostSink{})
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		defer func() { _ = instance.Close(context.Background()) }()
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		err = instance.Dispatch(context.Background(), "plugin.acme.hello.grow")
		var trapped *appplugin.TrapError
		if !errors.As(err, &trapped) {
			t.Fatalf("Dispatch error = %v, want TrapError from the memory cap", err)
		}
	})

	t.Run("trap recovery", func(t *testing.T) {
		instance, err := rt.Instantiate(context.Background(), smokeFixture(t, "trap"),
			appplugin.HostSink{})
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		defer func() { _ = instance.Close(context.Background()) }()
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		err = instance.Dispatch(context.Background(), "plugin.acme.hello.boom")
		var trapped *appplugin.TrapError
		if !errors.As(err, &trapped) {
			t.Fatalf("Dispatch error = %v, want TrapError", err)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.again"); !errors.Is(err, appplugin.ErrClosed) {
			t.Fatalf("Dispatch after a trap = %v, want ErrClosed", err)
		}
	})
}

func smokeFixture(t *testing.T, name string) appplugin.ModuleSpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".wasm"))
	if err != nil {
		t.Fatalf("read %s fixture: %v", name, err)
	}
	return appplugin.ModuleSpec{
		Name:           name,
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}
}
