// Package fake is an in-memory Runtime used by container tests and by the
// shared Runtime contract suite. It models the fixture modules by name, with
// no WebAssembly involved, so lifecycle, deadlines, quotas, and failure
// policy stay testable without an engine.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// Fixture names understood by the fake. The wazero contract harness maps the
// same names to real modules.
const (
	Hello         = "hello"
	BadABI        = "bad-abi"
	MissingExport = "missing-export"
	ExtraExport   = "extra-export"
	InitFailure   = "init-fail"
	Trap          = "trap"
	BadImport     = "bad-import"
	InfiniteLoop  = "infinite-loop"
	MemoryGrowth  = "memory-growth"
	InitHang      = "init-hang"
	ShutdownHang  = "shutdown-hang"
	StaleHandle   = "stale-handle"
	Stream        = "stream"
)

// minimumMemory is the initial memory the fixture modules reserve.
const minimumMemory = 256 << 10

// Option customizes a Runtime.
type Option func(*Runtime)

// WithLimits overrides the contract deadlines for tests.
func WithLimits(limits appplugin.Limits) Option {
	return func(r *Runtime) { r.limits = limits.Normalize() }
}

// Runtime implements appplugin.Runtime.
type Runtime struct {
	limits appplugin.Limits

	mu     sync.Mutex
	closed bool
}

// New returns a fake runtime with the contract deadlines.
func New(opts ...Option) *Runtime {
	r := &Runtime{limits: appplugin.DefaultLimits()}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Instantiate models module validation and instantiation.
func (r *Runtime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	closed := r.closed
	limits := r.limits
	r.mu.Unlock()
	if closed {
		return nil, appplugin.ErrClosed
	}
	maxBytes := spec.MemoryMaxBytes
	if maxBytes < 1<<20 || maxBytes > 256<<20 {
		return nil, &appplugin.LimitError{
			Resource: "module memory maximum bytes",
			Limit:    256 << 20,
		}
	}
	if maxBytes < minimumMemory {
		return nil, &appplugin.MemoryLimitError{
			Declared: maxBytes,
			Required: minimumMemory,
		}
	}
	base := &instance{
		name:    spec.Name,
		sink:    sink,
		limits:  limits,
		handles: appplugin.NewHandleTable(appplugin.MaxHandles),
	}
	base.memory.Store(minimumMemory)
	switch spec.Name {
	case Hello, StaleHandle:
		return base, nil
	case Stream:
		base.stream = true
		return base, nil
	case BadABI:
		return nil, &appplugin.ABIVersionError{Want: 1, Got: 2}
	case MissingExport:
		return nil, &appplugin.MissingExportError{Name: "natcatty_init"}
	case ExtraExport:
		return nil, &appplugin.UnexpectedExportError{Name: "natcatty_extra"}
	case InitFailure:
		base.initStatus = -7
		return base, nil
	case Trap:
		base.traps = true
		return base, nil
	case BadImport:
		return nil, &appplugin.ImportError{Module: "env", Name: "forbidden"}
	case InfiniteLoop:
		base.infiniteLoop = true
		return base, nil
	case MemoryGrowth:
		base.memoryGrowth = true
		return base, nil
	case InitHang:
		base.initHang = true
		return base, nil
	case ShutdownHang:
		base.shutdownHang = true
		return base, nil
	default:
		// Container tests use real plugin ids; only the contract suite uses
		// the fixture names above. Everything else behaves like hello.
		return base, nil
	}
}

// Close closes the runtime.
func (r *Runtime) Close(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

type instance struct {
	name   string
	sink   appplugin.HostSink
	limits appplugin.Limits

	initStatus   int32
	traps        bool
	infiniteLoop bool
	memoryGrowth bool
	initHang     bool
	shutdownHang bool
	stream       bool

	handles *appplugin.HandleTable
	memory  atomic.Int64

	callSlot sync.Mutex
	closed   atomic.Bool
	unusable atomic.Bool
}

var _ appplugin.Instance = (*instance)(nil)
var _ appplugin.InstanceStatsReporter = (*instance)(nil)
var _ appplugin.ServiceCapableInstance = (*instance)(nil)

// SupportsService reports that the fixture modules do not export the optional
// cross-plugin service provider function. A manifest that declares provides
// still resolves and publishes; a call returns the typed unsupported error.
func (i *instance) SupportsService() bool { return false }

// ServiceRequest reports the missing optional export. It still serializes on
// the call slot so it behaves like a real guest call boundary.
func (i *instance) ServiceRequest(ctx context.Context,
	request appplugin.ServiceRequest) ([]byte, error) {
	if !i.callSlot.TryLock() {
		return nil, appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return nil, err
	}
	return nil, &appplugin.MissingExportError{Name: "natcatty_service_request"}
}

// Handles returns the instance's generation-tagged host resource table.
func (i *instance) Handles() *appplugin.HandleTable { return i.handles }

// MemoryBytes reports the fixture's modeled memory footprint for diagnostics.
func (i *instance) MemoryBytes() int64 { return i.memory.Load() }

func (i *instance) Init(ctx context.Context) error {
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, i.limits.InitTimeout)
	defer cancel()
	if i.initHang {
		<-callCtx.Done()
		return i.deadlineError(callCtx, "init", i.limits.InitTimeout)
	}
	if i.initStatus != 0 {
		return &appplugin.GuestStatusError{Operation: "init", Status: i.initStatus}
	}
	return i.emit("init", func() {
		if i.sink.Log != nil {
			i.sink.Log(appplugin.LogInfo, "hello init")
		}
	})
}

func (i *instance) Dispatch(ctx context.Context, command string) error {
	return i.DispatchTimed(ctx, command, 0)
}

// DispatchTimed models a dispatch with an explicit MCP tool-call deadline. A
// tool-call command is answered through the HostSink tool callbacks, which is
// what the container's real command path observes.
func (i *instance) DispatchTimed(ctx context.Context, command string, timeout time.Duration) error {
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = i.limits.CallTimeout
	}
	if i.traps {
		i.fail()
		return &appplugin.TrapError{Operation: "dispatch", Err: fmt.Errorf("unreachable")}
	}
	if i.memoryGrowth {
		// The guest grows until the module maximum, then traps on the
		// out-of-bounds store.
		i.fail()
		return &appplugin.TrapError{Operation: "dispatch", Err: fmt.Errorf("out of bounds memory access")}
	}
	if i.stream {
		return i.echoProviderStream()
	}
	if call, ok := parseToolCommand(command); ok {
		return i.dispatchToolCall(ctx, call, timeout)
	}
	if i.infiniteLoop {
		if err := i.emit("dispatch", func() {
			if i.sink.Output != nil {
				i.sink.Output([]byte("spin"))
			}
		}); err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		<-callCtx.Done()
		return i.deadlineError(callCtx, "dispatch", timeout)
	}
	return i.emit("dispatch", func() {
		if i.sink.Log != nil {
			i.sink.Log(appplugin.LogInfo, "dispatch "+command)
		}
		if i.sink.Output != nil {
			i.sink.Output([]byte("hello from plugin"))
		}
	})
}

// echoProviderStream models the stream fixture: one input read at the same
// capacity the C guest uses, both credit counters, and an output echo. The
// callback panics are contained by emit exactly as the adapter contains a
// panicking host import.
func (i *instance) echoProviderStream() error {
	return i.emit("dispatch", func() {
		if i.sink.Provider == nil || i.sink.Provider.InputRead == nil ||
			i.sink.Provider.InputCredit == nil || i.sink.Provider.OutputWrite == nil ||
			i.sink.Provider.OutputCredit == nil {
			panic("provider stream callbacks are not configured")
		}
		data := i.sink.Provider.InputRead(7, 64)
		if i.sink.Provider.InputCredit(7) < 0 {
			panic("provider input credit was rejected")
		}
		if i.sink.Provider.OutputCredit(7) < 0 {
			panic("provider output credit was rejected")
		}
		if i.sink.Provider.OutputWrite(7, data) != 0 {
			panic("provider output write was rejected")
		}
	})
}

// fakeToolCommand is the dispatch command the container sends for one MCP
// tool call.
type fakeToolCommand struct {
	Kind      string         `json:"kind"`
	RequestID uint32         `json:"requestId"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func parseToolCommand(command string) (fakeToolCommand, bool) {
	var parsed fakeToolCommand
	if err := json.Unmarshal([]byte(command), &parsed); err != nil {
		return fakeToolCommand{}, false
	}
	if parsed.Kind != "mcp_tool_call" || parsed.RequestID == 0 {
		return fakeToolCommand{}, false
	}
	return parsed, true
}

// dispatchToolCall models the guest half of the tool-call ABI. Tool names use
// suffixes so a container test can select a behavior without a real guest.
func (i *instance) dispatchToolCall(ctx context.Context,
	call fakeToolCommand, timeout time.Duration) error {
	suffix := call.Tool
	if index := strings.LastIndex(suffix, "."); index >= 0 {
		suffix = suffix[index+1:]
	}
	switch suffix {
	case "crash":
		i.fail()
		return &appplugin.TrapError{Operation: "dispatch", Err: fmt.Errorf("tool trap")}
	case "hang":
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		<-callCtx.Done()
		return i.deadlineError(callCtx, "dispatch", timeout)
	case "blocking":
		if i.sink.ToolProgress != nil {
			i.sink.ToolProgress(call.RequestID, 1, "started")
		}
		<-ctx.Done()
		return i.deadlineError(ctx, "dispatch", timeout)
	case "no-result":
		return i.emit("dispatch", func() {
			if i.sink.Log != nil {
				i.sink.Log(appplugin.LogInfo, "dispatch "+call.Tool)
			}
		})
	}
	var payload []byte
	switch suffix {
	case "empty":
		payload = nil
	case "error":
		payload = []byte(`{"isError":true,"code":"guest_failed","message":"guest tool failed"}`)
	case "binary":
		payload = []byte{0xff, 0xfe, 0x00, 0x01}
	case "progress":
		if i.sink.ToolProgress != nil {
			i.sink.ToolProgress(call.RequestID, 50, "halfway")
		}
		payload = []byte(`{"content":[{"type":"text","text":"progressed"}],"structuredContent":{"tool":"progress"}}`)
	default:
		payload, _ = json.Marshal(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "fake:" + call.Tool}},
			"structuredContent": map[string]any{
				"tool": call.Tool, "arguments": call.Arguments,
			},
		})
	}
	if i.sink.ToolResult != nil {
		i.sink.ToolResult(call.RequestID, payload)
	}
	return nil
}

func (i *instance) Poll(ctx context.Context) error {
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return err
	}
	return nil
}

func (i *instance) Shutdown(ctx context.Context) error {
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if i.closed.Load() {
		return nil
	}
	if i.unusable.Load() {
		i.handles.RevokeAll()
		return nil
	}
	if i.shutdownHang {
		callCtx, cancel := context.WithTimeout(ctx, i.limits.UnloadTimeout)
		defer cancel()
		<-callCtx.Done()
		err := i.deadlineError(callCtx, "shutdown", i.limits.UnloadTimeout)
		i.handles.RevokeAll()
		return err
	}
	i.unusable.Store(true)
	i.handles.RevokeAll()
	return nil
}

func (i *instance) Close(context.Context) error {
	i.handles.RevokeAll()
	i.closed.Store(true)
	return nil
}

func (i *instance) ready() error {
	if i.closed.Load() || i.unusable.Load() {
		return appplugin.ErrClosed
	}
	return nil
}

// deadlineError mirrors a guest that did not yield before its deadline. A
// caller cancellation is preserved rather than reported as a host deadline.
func (i *instance) deadlineError(ctx context.Context, operation string, limit time.Duration) error {
	i.fail()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	return &appplugin.DeadlineError{Operation: operation, Limit: limit}
}

func (i *instance) fail() {
	i.unusable.Store(true)
	i.handles.RevokeAll()
}

// emit invokes a host sink callback and contains a panic exactly as wazero
// does: the guest call fails as a typed trap and the instance is recycled.
func (i *instance) emit(operation string, fn func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			i.fail()
			err = &appplugin.TrapError{
				Operation: operation,
				Err:       fmt.Errorf("host import panic: %v", recovered),
			}
		}
	}()
	fn()
	return nil
}
