// Package runtimecontract is the shared Runtime contract suite. It runs
// against the in-memory fake and the real wazero adapter so the fake cannot
// drift from the wire contract, including the hostile-plugin limits.
package runtimecontract

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// Fixture names shared by both implementations.
const (
	FixtureHello         = "hello"
	FixtureBadABI        = "bad-abi"
	FixtureMissingExport = "missing-export"
	FixtureExtraExport   = "extra-export"
	FixtureInitFailure   = "init-fail"
	FixtureTrap          = "trap"
	FixtureBadImport     = "bad-import"
	FixtureInfiniteLoop  = "infinite-loop"
	FixtureMemoryGrowth  = "memory-growth"
	FixtureInitHang      = "init-hang"
	FixtureShutdownHang  = "shutdown-hang"
	FixtureStaleHandle   = "stale-handle"
	FixtureStream        = "stream"
)

// streamFixtureCapacity is the buffer size the stream fixture passes to
// provider_input_read. Both implementations model the same guest, so the
// contract can pin the exact capacity the host import observes.
const streamFixtureCapacity = 64

// FixtureSource returns the module to instantiate for a fixture name.
type FixtureSource func(t *testing.T, name string) appplugin.ModuleSpec

// RuntimeFactory returns a fresh runtime for one subtest.
type RuntimeFactory func(t *testing.T) appplugin.Runtime

// Run executes the shared contract. Implementations supply the runtime and
// the fixture bytes; the suite owns every assertion.
func Run(t *testing.T, newRuntime RuntimeFactory, fixtures FixtureSource) {
	t.Helper()
	if newRuntime == nil || fixtures == nil {
		t.Fatal("runtimecontract: factory and fixture source are required")
	}

	t.Run("lifecycle and host imports", func(t *testing.T) {
		rt, instance, sink := instantiate(t, newRuntime, fixtures, FixtureHello)
		defer closeRuntime(t, rt)
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
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := sink.output(); got != "hello from plugin" {
			t.Fatalf("visible output = %q, want %q", got, "hello from plugin")
		}
		if logs := sink.logs(); !containsLog(logs, "hello init") {
			t.Fatalf("logs = %v, want an init log line", logs)
		}
	})

	t.Run("provider stream imports move bytes and expose credit", func(t *testing.T) {
		rt, instance, sink := instantiate(t, newRuntime, fixtures, FixtureStream)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.stream.pump"); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		output, stream, capacities := sink.providerOutput()
		if output != "in!" {
			t.Fatalf("provider output = %q, want %q", output, "in!")
		}
		if stream != 7 {
			t.Fatalf("provider output stream = %d, want 7", stream)
		}
		if len(capacities) != 1 || capacities[0] != streamFixtureCapacity {
			t.Fatalf("provider input capacities = %v, want [%d]", capacities, streamFixtureCapacity)
		}
		inputs, inputCredits, outputCredits := sink.providerStreams()
		if len(inputs) != 1 || inputs[0] != 7 {
			t.Fatalf("provider input streams = %v, want [7]", inputs)
		}
		if len(inputCredits) != 1 || inputCredits[0] != 7 {
			t.Fatalf("provider input credit streams = %v, want [7]", inputCredits)
		}
		if len(outputCredits) != 1 || outputCredits[0] != 7 {
			t.Fatalf("provider output credit streams = %v, want [7]", outputCredits)
		}
		if err := instance.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("ABI mismatch is typed", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)
		_, err := rt.Instantiate(context.Background(), fixtures(t, FixtureBadABI), appplugin.HostSink{})
		var mismatch *appplugin.ABIVersionError
		if !errors.As(err, &mismatch) {
			t.Fatalf("Instantiate error = %v, want ABIVersionError", err)
		}
		if mismatch.Got != 2 || mismatch.Want != 1 {
			t.Fatalf("mismatch = got %d want %d, want got 2 want 1", mismatch.Got, mismatch.Want)
		}
	})

	t.Run("missing export is typed", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)
		_, err := rt.Instantiate(context.Background(), fixtures(t, FixtureMissingExport), appplugin.HostSink{})
		var missing *appplugin.MissingExportError
		if !errors.As(err, &missing) {
			t.Fatalf("Instantiate error = %v, want MissingExportError", err)
		}
		if missing.Name == "" {
			t.Fatal("MissingExportError has no export name")
		}
	})

	t.Run("unexpected export is typed", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)
		_, err := rt.Instantiate(context.Background(), fixtures(t, FixtureExtraExport), appplugin.HostSink{})
		var unexpected *appplugin.UnexpectedExportError
		if !errors.As(err, &unexpected) {
			t.Fatalf("Instantiate error = %v, want UnexpectedExportError", err)
		}
		if unexpected.Name == "" {
			t.Fatal("UnexpectedExportError has no export name")
		}
	})

	t.Run("disallowed import is typed", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)
		_, err := rt.Instantiate(context.Background(), fixtures(t, FixtureBadImport), appplugin.HostSink{})
		var imported *appplugin.ImportError
		if !errors.As(err, &imported) {
			t.Fatalf("Instantiate error = %v, want ImportError", err)
		}
	})

	t.Run("init failure is contained", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureInitFailure)
		defer closeRuntime(t, rt)
		err := instance.Init(context.Background())
		var status *appplugin.GuestStatusError
		if !errors.As(err, &status) {
			t.Fatalf("Init error = %v, want GuestStatusError", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close after failed init: %v", err)
		}
	})

	t.Run("trap is contained", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureTrap)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		err := instance.Dispatch(context.Background(), "plugin.acme.hello.boom")
		var trapped *appplugin.TrapError
		if !errors.As(err, &trapped) {
			t.Fatalf("Dispatch error = %v, want TrapError", err)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.again"); !errors.Is(err, appplugin.ErrClosed) {
			t.Fatalf("Dispatch after a trap = %v, want ErrClosed", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close after a trap: %v", err)
		}
	})

	t.Run("memory maximum must be explicit and bounded", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)

		// The runtime only accepts explicit maxima in [1 MiB, 256 MiB]. The
		// manifest layer applies the 64 MiB default before Instantiate.
		for _, maximum := range []int64{0, 64 << 10, 257 << 20} {
			spec := fixtures(t, FixtureHello)
			spec.MemoryMaxBytes = maximum
			_, err := rt.Instantiate(context.Background(), spec, appplugin.HostSink{})
			var limit *appplugin.LimitError
			if !errors.As(err, &limit) {
				t.Fatalf("Instantiate with %d byte maximum error = %v, want LimitError", maximum, err)
			}
		}

		spec := fixtures(t, FixtureHello)
		spec.MemoryMaxBytes = 64 << 20
		instance, err := rt.Instantiate(context.Background(), spec, appplugin.HostSink{})
		if err != nil {
			t.Fatalf("Instantiate with an explicit 64 MiB maximum: %v", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("memory growth beyond the maximum traps and is contained", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)
		spec := fixtures(t, FixtureMemoryGrowth)
		spec.MemoryMaxBytes = 1 << 20
		instance, err := rt.Instantiate(context.Background(), spec, appplugin.HostSink{})
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		err = instance.Dispatch(context.Background(), "plugin.acme.hello.grow")
		var trapped *appplugin.TrapError
		if !errors.As(err, &trapped) {
			t.Fatalf("Dispatch error = %v, want TrapError", err)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.again"); !errors.Is(err, appplugin.ErrClosed) {
			t.Fatalf("Dispatch after memory exhaustion = %v, want ErrClosed", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("init deadline stops a non-yielding guest", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureInitHang)
		defer closeRuntime(t, rt)
		started := time.Now()
		err := instance.Init(context.Background())
		var deadline *appplugin.DeadlineError
		if !errors.As(err, &deadline) {
			t.Fatalf("Init error = %v, want DeadlineError", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Init error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("Init took %s, want a bounded deadline", elapsed)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close after deadline: %v", err)
		}
	})

	t.Run("call deadline stops an infinite loop", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureInfiniteLoop)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		started := time.Now()
		err := instance.Dispatch(context.Background(), "plugin.acme.hello.spin")
		var deadline *appplugin.DeadlineError
		if !errors.As(err, &deadline) {
			t.Fatalf("Dispatch error = %v, want DeadlineError", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Dispatch error = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("Dispatch took %s, want a bounded deadline", elapsed)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.again"); !errors.Is(err, appplugin.ErrClosed) {
			t.Fatalf("Dispatch after a deadline = %v, want ErrClosed", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close after deadline: %v", err)
		}
	})

	t.Run("unload deadline stops a non-yielding guest", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureShutdownHang)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		err := instance.Shutdown(context.Background())
		var deadline *appplugin.DeadlineError
		if !errors.As(err, &deadline) {
			t.Fatalf("Shutdown error = %v, want DeadlineError", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close after deadline: %v", err)
		}
	})

	t.Run("only one call is in flight per instance", func(t *testing.T) {
		rt, instance, sink := instantiate(t, newRuntime, fixtures, FixtureInfiniteLoop)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}

		done := make(chan error, 1)
		go func() {
			done <- instance.Dispatch(context.Background(), "plugin.acme.hello.spin")
		}()
		waitFor(t, 5*time.Second, func() bool {
			return strings.Contains(sink.output(), "spin")
		})
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.again"); !errors.Is(err, appplugin.ErrCallInFlight) {
			t.Fatalf("concurrent Dispatch = %v, want ErrCallInFlight", err)
		}

		select {
		case err := <-done:
			var deadline *appplugin.DeadlineError
			if !errors.As(err, &deadline) {
				t.Fatalf("first Dispatch error = %v, want DeadlineError", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the in-flight call did not finish within its deadline")
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("host import panic is contained", func(t *testing.T) {
		rt := newRuntime(t)
		defer closeRuntime(t, rt)
		sink := appplugin.HostSink{
			Log: func(appplugin.LogLevel, string) { panic("sink boom") },
		}
		instance, err := rt.Instantiate(context.Background(), fixtures(t, FixtureHello), sink)
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		err = instance.Init(context.Background())
		var trapped *appplugin.TrapError
		if !errors.As(err, &trapped) {
			t.Fatalf("Init error = %v, want TrapError", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close after host panic: %v", err)
		}
	})

	t.Run("stale handles are invalid after disposal", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureStaleHandle)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		handle, err := instance.Handles().Issue("capability")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, err := instance.Handles().Resolve(handle); err != nil {
			t.Fatalf("Resolve live handle: %v", err)
		}
		if err := instance.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		var invalid *appplugin.InvalidHandleError
		if _, err := instance.Handles().Resolve(handle); !errors.As(err, &invalid) {
			t.Fatalf("Resolve after Shutdown = %v, want InvalidHandleError", err)
		}

		fresh, err := instance.Handles().Issue("capability")
		if err != nil {
			t.Fatalf("Issue after revocation: %v", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := instance.Handles().Resolve(fresh); !errors.As(err, &invalid) {
			t.Fatalf("Resolve after Close = %v, want InvalidHandleError", err)
		}
	})

	t.Run("handles are invalid after a trap", func(t *testing.T) {
		rt, instance, _ := instantiate(t, newRuntime, fixtures, FixtureTrap)
		defer closeRuntime(t, rt)
		if err := instance.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		handle, err := instance.Handles().Issue("capability")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if err := instance.Dispatch(context.Background(), "plugin.acme.hello.boom"); err == nil {
			t.Fatal("Dispatch did not trap")
		}
		var invalid *appplugin.InvalidHandleError
		if _, err := instance.Handles().Resolve(handle); !errors.As(err, &invalid) {
			t.Fatalf("Resolve after a trap = %v, want InvalidHandleError", err)
		}
		if err := instance.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

func instantiate(t *testing.T, newRuntime RuntimeFactory, fixtures FixtureSource,
	name string) (appplugin.Runtime, appplugin.Instance, *recorder) {
	t.Helper()
	rt := newRuntime(t)
	sink := &recorder{}
	instance, err := rt.Instantiate(context.Background(), fixtures(t, name), sink.sink())
	if err != nil {
		closeRuntime(t, rt)
		t.Fatalf("Instantiate(%s): %v", name, err)
	}
	return rt, instance, sink
}

func closeRuntime(t *testing.T, rt appplugin.Runtime) {
	t.Helper()
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Runtime.Close: %v", err)
	}
}

// waitFor polls cond until it holds or the deadline expires. The interval is
// a cadence, not a synchronization assumption.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition was not met within %s", timeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type recorder struct {
	mu      sync.Mutex
	written strings.Builder
	lines   []string

	providerWritten       strings.Builder
	providerStream        uint32
	providerCapacities    []int
	providerInputStreams  []uint32
	providerInputCredits  []uint32
	providerOutputCredits []uint32
}

func (r *recorder) sink() appplugin.HostSink {
	return appplugin.HostSink{
		Log: func(_ appplugin.LogLevel, message string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.lines = append(r.lines, message)
		},
		Output: func(data []byte) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.written.Write(data)
		},
		Provider: &appplugin.ProviderCallbacks{
			InputRead: func(stream uint32, capacity int) []byte {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.providerInputStreams = append(r.providerInputStreams, stream)
				r.providerCapacities = append(r.providerCapacities, capacity)
				return []byte("in!")
			},
			InputCredit: func(stream uint32) int {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.providerInputCredits = append(r.providerInputCredits, stream)
				return 7
			},
			OutputWrite: func(stream uint32, data []byte) int32 {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.providerStream = stream
				r.providerWritten.Write(data)
				return 0
			},
			OutputCredit: func(stream uint32) int {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.providerOutputCredits = append(r.providerOutputCredits, stream)
				return 42
			},
			Cancelled: func(uint64) bool { return false },
		},
	}
}

func (r *recorder) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written.String()
}

func (r *recorder) logs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func (r *recorder) providerOutput() (string, uint32, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.providerWritten.String(), r.providerStream,
		append([]int(nil), r.providerCapacities...)
}

func (r *recorder) providerStreams() (inputs, inputCredits, outputCredits []uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.providerInputStreams...),
		append([]uint32(nil), r.providerInputCredits...),
		append([]uint32(nil), r.providerOutputCredits...)
}

func containsLog(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
