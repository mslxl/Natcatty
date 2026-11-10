// Package wasm implements the plugin Runtime port on wazero. Modules are
// reactor-style core WebAssembly: no _start, one exported memory, scalar-only
// host imports, and one in-flight call per instance.
package wasm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/sys"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

const (
	// HostModuleName is the base host import module for the v1 ABI.
	HostModuleName = domainpluginabi.HostModuleName

	wasmPageSize = 65536

	// maxScratchCommand is the input limit when a module offers
	// natcatty_scratch() instead of an allocator.
	maxScratchCommand = 4096
	// maxOutputBytes bounds one guest output import call.
	maxOutputBytes = 1 << 20
	// maxLogBytes bounds one guest log line.
	maxLogBytes = 64 << 10
	// maxRandomBytes bounds one random_get fill call.
	maxRandomBytes = 64 << 10
	// maxServiceRequestBytes bounds one host-service request. A 1 MiB storage
	// value and a 4 MiB binary cross-plugin payload are base64-encoded inside
	// JSON, so the transport ceiling is larger than either quota.
	maxServiceRequestBytes = 8 << 20
	// maxServiceResponseBytes bounds one host-service response.
	maxServiceResponseBytes = 8 << 20
	// maxServiceProviderRequestBytes bounds one broker request envelope sent
	// to a provider guest. It carries the method payload, which for binary
	// methods is base64-encoded.
	maxServiceProviderRequestBytes = 8 << 20
	// maxServiceProviderResponseBytes bounds one provider response written
	// through service_respond.
	maxServiceProviderResponseBytes = 8 << 20
	// maxLanguageRequestBytes bounds one language request. The document
	// snapshot itself is capped at 1 MiB by the host; the JSON envelope and
	// cursor context add bounded overhead.
	maxLanguageRequestBytes = 2 << 20
	// maxLanguageResponseBytes bounds one language response. Provider results
	// are bounded per capability, and this is the hard transport ceiling.
	maxLanguageResponseBytes = 1 << 20
	// maxToolResultBytes bounds one mcp_tool_result reply, matching the
	// external MCP tool-result bound the stdio layer enforces.
	maxToolResultBytes = 16 << 20
	// maxToolProgressMessageBytes bounds one progress message.
	maxToolProgressMessageBytes = 64 << 10
	// maxDispatchTimeout is the hard cap for an explicit MCP tool-call
	// deadline. The existing external MCP timeout remains the host bound; a
	// manifest timeout class can only shorten it.
	maxDispatchTimeout = appplugin.MaxMCPToolTimeout
	// maxProviderResponseBytes bounds one provider JSON response written
	// through provider_respond.
	maxProviderResponseBytes = appplugin.MaxProviderResponseBytes
	// maxProviderChunkBytes bounds one stream chunk in either direction.
	maxProviderChunkBytes = appplugin.ProviderChunkBytes
	// maxDataProviderRequestBytes bounds one data-provider request payload.
	// It carries profile fields and resolved secrets on open and a bounded
	// query, command, or plan body on the other methods.
	maxDataProviderRequestBytes = appplugin.MaxDataRequestBytes
	// maxDataProviderResponseBytes bounds one data-provider JSON response
	// written through data_provider_respond.
	maxDataProviderResponseBytes = appplugin.MaxDataResponseBytes

	// watchdogExitCode marks a module force-closed because it overran its
	// deadline. It is never a guest exit code: guests are reactors.
	watchdogExitCode uint32 = 125
)

// HostEnv supplies the two host-controlled ambient values: the clock and the
// random source. The app leaves the defaults (wall clock, crypto/rand); the
// fake host injects a virtual clock and deterministic randomness.
type HostEnv struct {
	// Now returns the current time in Unix nanoseconds. A nil function means
	// time.Now.
	Now func() int64
	// Random supplies random_get bytes. A nil reader means crypto/rand.Reader.
	Random io.Reader
}

// Option customizes a Runtime.
type Option func(*Runtime)

// WithLimits overrides the contract deadlines. Zero fields keep their
// defaults; tests use short values so deadline tests finish quickly.
func WithLimits(limits appplugin.Limits) Option {
	return func(r *Runtime) { r.limits = limits.Normalize() }
}

// WithHostEnv replaces the clock and randomness the guest imports observe.
func WithHostEnv(env HostEnv) Option {
	return func(r *Runtime) {
		if env.Now != nil {
			r.env.Now = env.Now
		}
		if env.Random != nil {
			r.env.Random = env.Random
		}
	}
}

// Runtime is a wazero-backed plugin.Runtime. It keeps a shared compilation
// cache but gives each instance its own wazero.Runtime so the manifest's
// memory maximum applies per plugin.
type Runtime struct {
	cache       wazero.CompilationCache
	config      wazero.RuntimeConfig
	status      appplugin.RuntimeStatus
	timeoutHint string
	limits      appplugin.Limits
	env         HostEnv

	mu     sync.Mutex
	closed bool
}

// NewRuntime returns a runtime with the host's automatic compiler/interpreter
// selection, a shared in-memory compilation cache, and the contract deadlines.
// On darwin/amd64 the selection includes the bounded startup compiler
// self-test; a failure falls back to the interpreter instead of failing.
func NewRuntime(opts ...Option) *Runtime {
	selection := currentEngine()
	r := &Runtime{
		cache:  wazero.NewCompilationCache(),
		config: selection.config,
		status: selection.status,
		limits: appplugin.DefaultLimits(),
		env: HostEnv{
			Now:    func() int64 { return time.Now().UnixNano() },
			Random: rand.Reader,
		},
	}
	if selection.status.Compatibility {
		r.timeoutHint = selection.status.TimeoutHint()
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RuntimeStatus reports the selected engine for diagnostics and the startup
// log. It is safe to call at any time, including on an unavailable platform.
func (r *Runtime) RuntimeStatus() appplugin.RuntimeStatus {
	if r == nil {
		return appplugin.RuntimeStatus{Engine: appplugin.EngineUnknown}
	}
	return r.status
}

// Compile-time assertion.
var _ appplugin.Runtime = (*Runtime)(nil)

// Instantiate compiles, validates, and starts one module.
func (r *Runtime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	if !r.status.Available() {
		return nil, &appplugin.UnavailableError{GOOS: r.status.GOOS, GOARCH: r.status.GOARCH}
	}
	if len(spec.WASM) == 0 {
		return nil, fmt.Errorf("plugin %s: empty module", spec.Name)
	}
	maxBytes, err := normalizeMemoryMax(spec)
	if err != nil {
		return nil, err
	}
	spec.MemoryMaxBytes = maxBytes
	pages := uint32((maxBytes + wasmPageSize - 1) / wasmPageSize)

	r.mu.Lock()
	closed := r.closed
	cache := r.cache
	config := r.config
	limits := r.limits
	env := r.env
	timeoutHint := r.timeoutHint
	r.mu.Unlock()
	if closed {
		return nil, appplugin.ErrClosed
	}

	rt := wazero.NewRuntimeWithConfig(ctx, config.
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true).
		WithCompilationCache(cache))
	closeRuntime := true
	defer func() {
		if closeRuntime {
			_ = rt.Close(context.Background())
		}
	}()

	compiled, err := rt.CompileModule(ctx, spec.WASM)
	if err != nil {
		// wazero enforces the runtime memory limit while decoding, so a
		// module above the manifest's cap can fail here rather than in
		// validateModule. Re-decode without the limit to report the stable
		// typed error callers and diagnostics rely on.
		if typed := classifyCompileError(ctx, spec.WASM, err, spec.MemoryMaxBytes, cache, config); typed != nil {
			return nil, typed
		}
		return nil, fmt.Errorf("plugin %s: compile module: %w", spec.Name, err)
	}
	if err := validateModule(spec.WASM, compiled, spec.MemoryMaxBytes); err != nil {
		return nil, err
	}
	bridge := &languageBridge{}
	serviceBridge := &serviceResponseBridge{}
	providerBridge := &providerResponseBridge{}
	dataBridge := &dataResponseBridge{}
	// The service imports pause the dispatch compute budget while host work
	// runs, so a user confirmation or dial is not charged to the guest.
	var callBudgetRef atomic.Pointer[callBudget]
	// serviceLength records the bytes copied by the last successful service
	// call. The service ABI returns only a status, so generated typed bindings
	// read this import immediately after a zero status.
	serviceLength := &atomic.Uint32{}
	if _, err := instantiateHost(ctx, rt, sink, env, bridge, serviceBridge, providerBridge,
		dataBridge, serviceLength, &callBudgetRef); err != nil {
		return nil, fmt.Errorf("plugin %s: instantiate host imports: %w", spec.Name, err)
	}
	module, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("plugin %s: instantiate module: %w", spec.Name, err)
	}

	instance := &instance{
		name:            spec.Name,
		runtime:         rt,
		module:          module,
		limits:          limits,
		timeoutHint:     timeoutHint,
		handles:         appplugin.NewHandleTable(appplugin.MaxHandles),
		languageBridge:  bridge,
		serviceBridge:   serviceBridge,
		providerBridge:  providerBridge,
		dataBridge:      dataBridge,
		callBudget:      &callBudgetRef,
		abi:             module.ExportedFunction(domainpluginabi.ExportABIVersion),
		init:            module.ExportedFunction(domainpluginabi.ExportInit),
		dispatch:        module.ExportedFunction(domainpluginabi.ExportDispatch),
		poll:            module.ExportedFunction(domainpluginabi.ExportPoll),
		shutdown:        module.ExportedFunction(domainpluginabi.ExportShutdown),
		alloc:           module.ExportedFunction(domainpluginabi.ExportAlloc),
		scratch:         module.ExportedFunction(domainpluginabi.ExportScratch),
		languageRequest: module.ExportedFunction(domainpluginabi.ExportLanguageRequest),
		languageCancel:  module.ExportedFunction(domainpluginabi.ExportLanguageCancel),
		serviceRequest:  module.ExportedFunction(domainpluginabi.ExportServiceRequest),
		providerRequest: module.ExportedFunction(domainpluginabi.ExportProviderRequest),
		dataProviderRequest: module.ExportedFunction(
			domainpluginabi.ExportDataProviderRequest),
	}
	abiCtx, cancel := context.WithTimeout(ctx, limits.CallTimeout)
	defer cancel()
	got, err := instance.callInt(abiCtx, "abi_version", limits.CallTimeout, instance.abi)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: read ABI version: %w", spec.Name, err)
	}
	if err := domainpluginabi.CheckABIVersion(int(got)); err != nil {
		return nil, err
	}
	closeRuntime = false
	return instance, nil
}

// normalizeMemoryMax validates the declared maximum in bytes. Unlike the
// manifest, a Runtime ModuleSpec must carry an explicit value: the container
// applies the 64 MiB default before calling Instantiate. Zero, unbounded, and
// out-of-range maxima are rejected.
func normalizeMemoryMax(spec appplugin.ModuleSpec) (int64, error) {
	const (
		minBytes = int64(domainplugin.MinMemoryMaxMiB) << 20
		maxBytes = int64(domainplugin.MaxMemoryMaxMiB) << 20
	)
	declared := spec.MemoryMaxBytes
	if declared < minBytes || declared > maxBytes {
		return 0, &appplugin.LimitError{
			Resource: "module memory maximum bytes",
			Limit:    int(maxBytes),
		}
	}
	return declared, nil
}

// Close releases the shared compilation cache.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	cache := r.cache
	r.mu.Unlock()
	if cache != nil {
		return cache.Close(ctx)
	}
	return nil
}

func instantiateHost(ctx context.Context, rt wazero.Runtime, sink appplugin.HostSink,
	env HostEnv, bridge *languageBridge, serviceBridge *serviceResponseBridge,
	providerBridge *providerResponseBridge, dataBridge *dataResponseBridge,
	serviceLength *atomic.Uint32, callBudget *atomic.Pointer[callBudget]) (api.Module, error) {
	builder := rt.NewHostModuleBuilder(HostModuleName)
	builder.NewFunctionBuilder().
		WithFunc(func() int32 { return int32(domainpluginabi.Version) }).
		Export(domainpluginabi.HostFunctionABIVersion)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, level, ptr, length uint32) {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length}, maxLogBytes)
			if !ok || sink.Log == nil {
				return
			}
			// A panic in a host callback is recovered by wazero at the call
			// boundary and mapped to TrapError by the instance.
			sink.Log(appplugin.LogLevel(level), string(data))
		}).
		Export(domainpluginabi.HostFunctionLog)
	builder.NewFunctionBuilder().
		WithFunc(func() int64 {
			return env.Now()
		}).
		Export(domainpluginabi.HostFunctionNowNs)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) int32 {
			return fillGuestRandom(mod, ptr, length, env.Random)
		}).
		Export(domainpluginabi.HostFunctionRandomGet)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) int32 {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length}, maxOutputBytes)
			if !ok {
				return -1
			}
			if sink.Output != nil {
				sink.Output(data)
			}
			return int32(len(data))
		}).
		Export(domainpluginabi.HostFunctionOutputWrite)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, requestID uint64,
			ptr, length uint32) int32 {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length},
				maxLanguageResponseBytes)
			if !ok {
				return -1
			}
			if !bridge.respond(requestID, data) {
				return -1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionLanguageRespond)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, requestID uint64) int32 {
			if sink.LanguageCancelled != nil && sink.LanguageCancelled(requestID) {
				return 1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionLanguageCancelled)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, requestID, ptr, length uint32) int32 {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length},
				maxToolResultBytes)
			if !ok {
				return -1
			}
			if sink.ToolResult == nil {
				return -2
			}
			return sink.ToolResult(requestID, data)
		}).
		Export(domainpluginabi.HostFunctionMcpToolResult)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, requestID, progress,
			messagePtr, messageLen uint32) int32 {
			message, ok := readGuest(mod,
				domainpluginabi.GuestRange{Ptr: messagePtr, Len: messageLen},
				maxToolProgressMessageBytes)
			if !ok {
				return -1
			}
			if sink.ToolProgress == nil {
				return -2
			}
			return sink.ToolProgress(requestID, progress, string(message))
		}).
		Export(domainpluginabi.HostFunctionMcpToolProgress)
	builder.NewFunctionBuilder().
		WithFunc(func() uint32 {
			return serviceLength.Load()
		}).
		Export(domainpluginabi.HostFunctionServiceResponseLen)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, requestID uint64,
			ptr, length uint32) int32 {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length},
				maxServiceProviderResponseBytes)
			if !ok {
				return -1
			}
			if !serviceBridge.respond(requestID, data) {
				return -1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionServiceRespond)
	// Connection-provider response bridge. The guest answers one in-flight
	// provider call with its JSON envelope.
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, requestID uint64,
			ptr, length uint32) int32 {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length},
				maxProviderResponseBytes)
			if !ok {
				return -1
			}
			if !providerBridge.respond(requestID, data) {
				return -1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionProviderRespond)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, requestID uint64) int32 {
			if sink.Provider != nil && sink.Provider.Cancelled != nil &&
				sink.Provider.Cancelled(requestID) {
				return 1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionProviderCancelled)
	// Data-provider response bridge. The guest answers one in-flight data call
	// with its closed JSON result envelope.
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, requestID uint64,
			ptr, length uint32) int32 {
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length},
				maxDataProviderResponseBytes)
			if !ok {
				return -1
			}
			if !dataBridge.respond(requestID, data) {
				return -1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionDataProviderRespond)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, requestID uint64) int32 {
			if sink.Data != nil && sink.Data.Cancelled != nil &&
				sink.Data.Cancelled(requestID) {
				return 1
			}
			return 0
		}).
		Export(domainpluginabi.HostFunctionDataProviderCancelled)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, stream, ptr, capacity uint32) int32 {
			if sink.Provider == nil || sink.Provider.InputRead == nil {
				return -1
			}
			if capacity > maxProviderChunkBytes {
				capacity = maxProviderChunkBytes
			}
			data := sink.Provider.InputRead(stream, int(capacity))
			if len(data) == 0 {
				return 0
			}
			if len(data) > int(capacity) {
				return -1
			}
			memory := mod.Memory()
			if memory == nil || !memory.Write(ptr, data) {
				return -1
			}
			return int32(len(data))
		}).
		Export(domainpluginabi.HostFunctionProviderInputRead)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, stream uint32) int32 {
			if sink.Provider == nil || sink.Provider.InputCredit == nil {
				return -1
			}
			return int32(sink.Provider.InputCredit(stream))
		}).
		Export(domainpluginabi.HostFunctionProviderInputCredit)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, stream, ptr, length uint32) int32 {
			if sink.Provider == nil || sink.Provider.OutputWrite == nil {
				return -1
			}
			if length > maxProviderChunkBytes {
				return -1
			}
			data, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: ptr, Len: length},
				maxProviderChunkBytes)
			if !ok {
				return -1
			}
			return sink.Provider.OutputWrite(stream, data)
		}).
		Export(domainpluginabi.HostFunctionProviderOutputWrite)
	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, stream uint32) int32 {
			if sink.Provider == nil || sink.Provider.OutputCredit == nil {
				return -1
			}
			return int32(sink.Provider.OutputCredit(stream))
		}).
		Export(domainpluginabi.HostFunctionProviderOutputCredit)
	host, err := builder.Instantiate(ctx)
	if err != nil {
		return nil, err
	}
	for _, service := range domainpluginabi.Services {
		if _, err := instantiateServiceModule(ctx, rt, sink, service, serviceLength,
			callBudget); err != nil {
			return nil, fmt.Errorf("instantiate %s: %w", service.Module, err)
		}
	}
	return host, nil
}

// instantiateServiceModule defines one natcatty:svc/<name>_v1 module. The
// single exported call function copies the request into host memory, runs the
// application dispatcher, and copies the response envelope back into guest
// memory. Service-level failures travel inside the envelope; the return value
// is the transport status only.
func instantiateServiceModule(ctx context.Context, rt wazero.Runtime,
	sink appplugin.HostSink, service domainpluginabi.Service, serviceLength *atomic.Uint32,
	callBudget *atomic.Pointer[callBudget]) (api.Module, error) {
	builder := rt.NewHostModuleBuilder(service.Module)
	builder.NewFunctionBuilder().
		WithFunc(func(callCtx context.Context, mod api.Module, methodID, reqPtr, reqLen,
			respPtr, respCap uint32) (status int32) {
			serviceLength.Store(0)
			defer func() {
				if recover() != nil {
					serviceLength.Store(0)
					status = int32(domainpluginabi.ServiceStatusTrap)
				}
			}()
			if err := callCtx.Err(); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return int32(domainpluginabi.ServiceStatusDeadline)
				}
				return int32(domainpluginabi.ServiceStatusCancelled)
			}
			if sink.Services == nil {
				return int32(domainpluginabi.ServiceStatusUnavailable)
			}
			if respCap > maxServiceResponseBytes {
				return int32(domainpluginabi.ServiceStatusLimit)
			}
			request, ok := readGuest(mod, domainpluginabi.GuestRange{Ptr: reqPtr, Len: reqLen},
				maxServiceRequestBytes)
			if !ok {
				return int32(domainpluginabi.ServiceStatusInvalid)
			}
			// Host work is not guest compute: pause the dispatch budget while
			// the dispatcher runs, and resume it even if the dispatcher
			// panics. The dispatcher still receives the call context so
			// caller cancellation and unload stop it.
			response := func() (response []byte) {
				if budget := loadCallBudget(callBudget); budget != nil {
					budget.pause()
					defer budget.resume()
				}
				return sink.Services.Call(callCtx, appplugin.ServiceCall{
					Service: service.Name,
					Method:  methodID,
					Request: request,
				})
			}()
			if len(response) > int(respCap) {
				return int32(domainpluginabi.ServiceStatusLimit)
			}
			if len(response) > maxServiceResponseBytes {
				return int32(domainpluginabi.ServiceStatusLimit)
			}
			if len(response) > 0 {
				memory := mod.Memory()
				if memory == nil || !memory.Write(respPtr, response) {
					return int32(domainpluginabi.ServiceStatusInvalid)
				}
			}
			serviceLength.Store(uint32(len(response)))
			return int32(domainpluginabi.ServiceStatusOk)
		}).
		Export(domainpluginabi.ServiceCallFunction)
	return builder.Instantiate(ctx)
}

// fillGuestRandom copies deterministic or cryptographic random bytes into the
// guest range. Like every host import it never retains the guest memory view.
func fillGuestRandom(mod api.Module, ptr, length uint32, source io.Reader) int32 {
	if mod == nil || source == nil || length > maxRandomBytes {
		return -1
	}
	buffer := make([]byte, length)
	if _, err := io.ReadFull(source, buffer); err != nil {
		return -1
	}
	memory := mod.Memory()
	if memory == nil || !memory.Write(ptr, buffer) {
		return -1
	}
	return 0
}

// readGuest copies a guest memory range into host memory. The view returned
// by Memory.Read is never retained past this call.
func readGuest(mod api.Module, guest domainpluginabi.GuestRange, limit uint32) ([]byte, bool) {
	if mod == nil {
		return nil, false
	}
	memory := mod.Memory()
	if memory == nil || guest.Len > limit {
		return nil, false
	}
	view, ok := memory.Read(guest.Ptr, guest.Len)
	if !ok {
		return nil, false
	}
	data := make([]byte, len(view))
	copy(data, view)
	return data, true
}

// responseBridge carries the single in-flight host-to-guest response from a
// host import back to the runtime call. It rejects a response for a different
// request id, a duplicate response, and a report that arrives outside a call.
// Language providers and service providers use separate bridges, but the
// correlation rule is identical.
type responseBridge struct {
	mu        sync.Mutex
	activeID  uint64
	inCall    bool
	responded bool
	payload   []byte
}

// languageBridge is the response bridge used by language request calls.
type languageBridge = responseBridge

// serviceResponseBridge is the response bridge used by cross-plugin service
// request calls.
type serviceResponseBridge = responseBridge

// providerResponseBridge is the response bridge used by connection-provider
// calls.
type providerResponseBridge = responseBridge

// dataResponseBridge is the response bridge used by data-provider calls.
type dataResponseBridge = responseBridge

func (b *responseBridge) begin(requestID uint64) {
	b.mu.Lock()
	b.activeID = requestID
	b.inCall = true
	b.responded = false
	b.payload = nil
	b.mu.Unlock()
}

func (b *responseBridge) end() {
	b.mu.Lock()
	b.inCall = false
	b.responded = false
	b.payload = nil
	b.mu.Unlock()
}

func (b *responseBridge) respond(requestID uint64, payload []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.inCall || b.activeID != requestID || b.responded {
		return false
	}
	b.responded = true
	b.payload = append([]byte(nil), payload...)
	return true
}

func (b *responseBridge) take(requestID uint64) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.inCall || b.activeID != requestID || !b.responded {
		return nil, false
	}
	return b.payload, true
}

// instance is one module with its own wazero runtime.
type instance struct {
	name           string
	runtime        wazero.Runtime
	module         api.Module
	limits         appplugin.Limits
	timeoutHint    string
	handles        *appplugin.HandleTable
	languageBridge *languageBridge
	serviceBridge  *serviceResponseBridge
	providerBridge *providerResponseBridge
	dataBridge     *dataResponseBridge

	abi             api.Function
	init            api.Function
	dispatch        api.Function
	poll            api.Function
	shutdown        api.Function
	alloc           api.Function
	scratch         api.Function
	languageRequest api.Function
	languageCancel  api.Function
	serviceRequest  api.Function
	providerRequest api.Function

	dataProviderRequest api.Function

	// callSlot admits one in-flight guest call. TryLock rejects a second
	// call instead of queueing it.
	callSlot sync.Mutex
	// callBudget is the active dispatch compute budget. Service imports pause
	// it while host work runs so the guest's ordinary compute limit still
	// applies without charging host work to the guest.
	callBudget *atomic.Pointer[callBudget]
	closed     atomic.Bool
	unusable   atomic.Bool
}

var _ appplugin.Instance = (*instance)(nil)
var _ appplugin.InstanceStatsReporter = (*instance)(nil)

// Handles returns the instance's generation-tagged host resource table.
func (i *instance) Handles() *appplugin.HandleTable { return i.handles }

// MemoryBytes reports the live linear-memory size for plugin diagnostics. A
// module without an exported memory reports zero.
func (i *instance) MemoryBytes() int64 {
	if i == nil || i.module == nil {
		return 0
	}
	memory := i.module.Memory()
	if memory == nil {
		return 0
	}
	return int64(memory.Size())
}

// Init runs the guest init export under the init deadline.
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
	stop := i.startWatchdog(i.limits.InitTimeout + i.limits.WatchdogGrace)
	defer stop()
	return i.callStatus(callCtx, "init", i.limits.InitTimeout, i.init)
}

// Dispatch copies command into guest memory and runs the dispatch export.
// The visible result is delivered through the host output import.
func (i *instance) Dispatch(ctx context.Context, command string) error {
	return i.DispatchTimed(ctx, command, 0)
}

// DispatchTimed runs one dispatch under an explicit deadline. The deadline is
// clamped to the host maximum so a caller cannot turn it into an unbounded
// call, and the watchdog is armed just past the same deadline.
func (i *instance) DispatchTimed(ctx context.Context, command string, timeout time.Duration) error {
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return err
	}
	timeout = i.dispatchDeadline(timeout)
	// The call context carries cancellation, not the compute deadline. The
	// budget is paused by service imports while host work runs, so a dial or a
	// user trust confirmation is not charged against the guest's compute
	// slice; ordinary guest compute is still bounded by timeout.
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := newCallBudget(timeout, i.markUnusable)
	if i.callBudget != nil {
		i.callBudget.Store(budget)
		defer func() {
			i.callBudget.Store(nil)
			budget.stop()
		}()
	} else {
		defer budget.stop()
	}

	ptr, err := i.writeBytes(callCtx, []byte(command), 0)
	if err != nil {
		return err
	}
	return i.callStatus(callCtx, "dispatch", timeout, i.dispatch,
		uint64(ptr), uint64(len(command)))
}

// dispatchDeadline applies the call deadline rules: zero means the ordinary
// dispatch deadline, a positive value is clamped to the host maximum.
func (i *instance) dispatchDeadline(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return i.limits.CallTimeout
	}
	if timeout > maxDispatchTimeout {
		return maxDispatchTimeout
	}
	return timeout
}

// writeBytes copies data into guest memory using the module's allocator, or
// its fixed scratch buffer when the allocator is absent. limit, when positive,
// rejects an oversized payload before it reaches guest memory.
func (i *instance) writeBytes(ctx context.Context, data []byte, limit uint32) (uint32, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if limit > 0 && uint64(len(data)) > uint64(limit) {
		return 0, &appplugin.LimitError{Resource: "guest call payload bytes", Limit: int(limit)}
	}
	var ptr uint64
	switch {
	case i.alloc != nil:
		results, err := i.callInt(ctx, "alloc", i.limits.CallTimeout, i.alloc, uint64(len(data)))
		if err != nil {
			return 0, err
		}
		ptr = results
	case i.scratch != nil:
		if len(data) > maxScratchCommand {
			return 0, &appplugin.GuestStatusError{Operation: "scratch", Status: -1}
		}
		results, err := i.callInt(ctx, "scratch", i.limits.CallTimeout, i.scratch)
		if err != nil {
			return 0, err
		}
		ptr = results
	default:
		return 0, &appplugin.MissingExportError{Name: domainpluginabi.ExportAlloc}
	}
	memory := i.module.Memory()
	if memory == nil || ptr > uint64(^uint32(0)) || !memory.Write(uint32(ptr), data) {
		return 0, &appplugin.GuestStatusError{Operation: "write", Status: -1}
	}
	return uint32(ptr), nil
}

// LanguageRequest runs one host-to-guest language request and returns the
// response the guest wrote through the language_respond import. The guest
// export is optional, so a module without it reports MissingExportError; the
// container rejects a language.provider contribution on such a module before
// any request is routed.
func (i *instance) LanguageRequest(ctx context.Context,
	request appplugin.LanguageRequest) ([]byte, error) {
	if !i.callSlot.TryLock() {
		return nil, appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return nil, err
	}
	if i.languageRequest == nil {
		return nil, &appplugin.MissingExportError{Name: domainpluginabi.ExportLanguageRequest}
	}
	kind, ok := languageKindID(request.Kind)
	if !ok {
		return nil, &appplugin.LanguageNoResponseError{Provider: i.name, Kind: request.Kind}
	}
	callCtx, cancel := context.WithTimeout(ctx, i.limits.CallTimeout)
	defer cancel()
	stop := i.startWatchdog(i.limits.CallTimeout + i.limits.WatchdogGrace)
	defer stop()

	if i.languageBridge == nil {
		return nil, appplugin.ErrClosed
	}
	i.languageBridge.begin(request.ID)
	defer i.languageBridge.end()
	ptr, err := i.writeBytes(callCtx, request.Payload, maxLanguageRequestBytes)
	if err != nil {
		return nil, err
	}
	if err := i.callStatus(callCtx, "language_request", i.limits.CallTimeout,
		i.languageRequest, request.ID, uint64(kind), uint64(ptr), uint64(len(request.Payload))); err != nil {
		return nil, err
	}
	payload, ok := i.languageBridge.take(request.ID)
	if !ok {
		return nil, &appplugin.LanguageNoResponseError{Provider: i.name, Kind: request.Kind}
	}
	return payload, nil
}

// CancelLanguage asks the guest to discard a superseded request. A provider
// that omits the optional export still supports cooperative cancellation only
// through the language_cancelled poll, so a missing export is not an error.
func (i *instance) CancelLanguage(ctx context.Context, requestID uint64) error {
	if i.languageCancel == nil {
		return nil
	}
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, i.limits.CallTimeout)
	defer cancel()
	stop := i.startWatchdog(i.limits.CallTimeout + i.limits.WatchdogGrace)
	defer stop()
	return i.callStatus(callCtx, "language_cancel", i.limits.CallTimeout,
		i.languageCancel, requestID)
}

var _ appplugin.ServiceCapableInstance = (*instance)(nil)

// SupportsService reports whether the module exports the optional service
// request function. A manifest that declares provides needs it to answer
// calls; without it every call returns unsupported.
func (i *instance) SupportsService() bool { return i.serviceRequest != nil }

// ServiceRequest runs one host-to-guest cross-plugin request and returns the
// envelope the guest wrote through service_respond. The deadline comes from
// the caller, not from the ordinary dispatch limit: a cross-plugin call may
// use its declared 5 s or 30 s class, always capped by the remaining outer
// deadline.
func (i *instance) ServiceRequest(ctx context.Context,
	request appplugin.ServiceRequest) ([]byte, error) {
	if !i.callSlot.TryLock() {
		return nil, appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return nil, err
	}
	if i.serviceRequest == nil {
		return nil, &appplugin.MissingExportError{Name: domainpluginabi.ExportServiceRequest}
	}
	if i.serviceBridge == nil {
		return nil, appplugin.ErrClosed
	}
	limit := appplugin.MaxServiceCallTimeout
	if deadline, ok := ctx.Deadline(); ok {
		limit = time.Until(deadline)
		if limit <= 0 {
			return nil, context.DeadlineExceeded
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stop := i.startWatchdog(limit + i.limits.WatchdogGrace)
	defer stop()

	i.serviceBridge.begin(request.ID)
	defer i.serviceBridge.end()
	ptr, err := i.writeBytes(callCtx, request.Payload, maxServiceProviderRequestBytes)
	if err != nil {
		return nil, err
	}
	if err := i.callStatus(callCtx, "service_request", limit, i.serviceRequest,
		request.ID, uint64(ptr), uint64(len(request.Payload))); err != nil {
		return nil, err
	}
	payload, ok := i.serviceBridge.take(request.ID)
	if !ok {
		return nil, &appplugin.ServiceNoResponseError{Provider: i.name}
	}
	return payload, nil
}

var _ appplugin.ProviderCapableInstance = (*instance)(nil)

// SupportsProvider reports whether the module exports the optional provider
// request function. A manifest that declares a connection contribution needs
// it; without it the container rejects the contribution at activation.
func (i *instance) SupportsProvider() bool { return i.providerRequest != nil }

// InvokeProvider runs one host-to-guest provider call and returns the response
// the guest wrote through provider_respond. The caller supplies the deadline;
// it is always clamped to the host's maximum, and a miss marks the instance
// unusable like any other guest call.
func (i *instance) InvokeProvider(ctx context.Context,
	request appplugin.ProviderRequest) ([]byte, error) {

	if !i.callSlot.TryLock() {
		return nil, appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return nil, err
	}
	if i.providerRequest == nil {
		return nil, &appplugin.MissingExportError{Name: domainpluginabi.ExportProviderRequest}
	}
	if i.providerBridge == nil {
		return nil, appplugin.ErrClosed
	}
	limit := appplugin.MaxProviderCallTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < limit {
			limit = remaining
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stop := i.startWatchdog(limit + i.limits.WatchdogGrace)
	defer stop()

	i.providerBridge.begin(request.ID)
	defer i.providerBridge.end()
	ptr, err := i.writeBytes(callCtx, request.Payload, maxProviderResponseBytes)
	if err != nil {
		return nil, err
	}
	if err := i.callStatus(callCtx, "provider_request", limit, i.providerRequest,
		request.ID, uint64(uint32(request.Method)), uint64(ptr),
		uint64(len(request.Payload))); err != nil {
		return nil, err
	}
	payload, ok := i.providerBridge.take(request.ID)
	if !ok {
		return nil, &appplugin.ProviderNoResponseError{Provider: i.name}
	}
	return payload, nil
}

var _ appplugin.DataProviderCapableInstance = (*instance)(nil)

// SupportsDataProvider reports whether the module exports the optional data
// provider request function. A manifest that declares a data contribution
// needs it; without it the container rejects the contribution at activation.
func (i *instance) SupportsDataProvider() bool { return i.dataProviderRequest != nil }

// InvokeDataProvider runs one host-to-guest data-provider call and returns the
// response the guest wrote through data_provider_respond. The caller supplies
// the deadline; it is always clamped to the host maximum, and a miss marks the
// instance unusable like any other guest call.
func (i *instance) InvokeDataProvider(ctx context.Context,
	request appplugin.DataProviderRequest) ([]byte, error) {

	if !i.callSlot.TryLock() {
		return nil, appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return nil, err
	}
	if i.dataProviderRequest == nil {
		return nil, &appplugin.MissingExportError{Name: domainpluginabi.ExportDataProviderRequest}
	}
	if i.dataBridge == nil {
		return nil, appplugin.ErrClosed
	}
	limit := appplugin.MaxDataCallTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		if remaining < limit {
			limit = remaining
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stop := i.startWatchdog(limit + i.limits.WatchdogGrace)
	defer stop()

	i.dataBridge.begin(request.ID)
	defer i.dataBridge.end()
	ptr, err := i.writeBytes(callCtx, request.Payload, maxDataProviderRequestBytes)
	if err != nil {
		return nil, err
	}
	if err := i.callStatus(callCtx, "data_provider_request", limit, i.dataProviderRequest,
		request.ID, uint64(uint32(request.Method)), uint64(ptr),
		uint64(len(request.Payload))); err != nil {
		return nil, err
	}
	payload, ok := i.dataBridge.take(request.ID)
	if !ok {
		return nil, &appplugin.DataNoResponseError{Provider: i.name}
	}
	return payload, nil
}

var _ appplugin.LanguageCapableInstance = (*instance)(nil)

// SupportsLanguage reports whether the module exports the optional language
// request function. A module without it activates normally unless its manifest
// declares a language.provider contribution.
func (i *instance) SupportsLanguage() bool { return i.languageRequest != nil }

// languageKindID maps the domain capability to its stable wire value.
func languageKindID(capability domainplugin.LanguageCapability) (domainpluginabi.LanguageKind, bool) {
	switch capability {
	case domainplugin.LanguageHighlight:
		return domainpluginabi.LanguageKindHighlight, true
	case domainplugin.LanguageCompletion:
		return domainpluginabi.LanguageKindCompletion, true
	case domainplugin.LanguageDiagnostics:
		return domainpluginabi.LanguageKindDiagnostics, true
	case domainplugin.LanguageFormatting:
		return domainpluginabi.LanguageKindFormatting, true
	case domainplugin.LanguageHover:
		return domainpluginabi.LanguageKindHover, true
	case domainplugin.LanguageSignatureHelp:
		return domainpluginabi.LanguageKindSignatureHelp, true
	case domainplugin.LanguageNavigation:
		return domainpluginabi.LanguageKindNavigation, true
	case domainplugin.LanguageCodeActions:
		return domainpluginabi.LanguageKindCodeActions, true
	default:
		return 0, false
	}
}

// Poll gives the guest a host-driven opportunity to advance work.
func (i *instance) Poll(ctx context.Context) error {
	if !i.callSlot.TryLock() {
		return appplugin.ErrCallInFlight
	}
	defer i.callSlot.Unlock()
	if err := i.ready(); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, i.limits.CallTimeout)
	defer cancel()
	stop := i.startWatchdog(i.limits.CallTimeout + i.limits.WatchdogGrace)
	defer stop()
	return i.callStatus(callCtx, "poll", i.limits.CallTimeout, i.poll)
}

// Shutdown asks the guest to stop once under the unload deadline. The
// instance accepts no further calls afterwards and its handles are revoked.
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
	callCtx, cancel := context.WithTimeout(ctx, i.limits.UnloadTimeout)
	defer cancel()
	stop := i.startWatchdog(i.limits.UnloadTimeout + i.limits.WatchdogGrace)
	defer stop()
	err := i.callStatus(callCtx, "shutdown", i.limits.UnloadTimeout, i.shutdown)
	i.unusable.Store(true)
	i.handles.RevokeAll()
	return err
}

// Close releases the instance's wazero runtime, its module, its host imports,
// and every handle. It is idempotent.
func (i *instance) Close(ctx context.Context) error {
	i.handles.RevokeAll()
	if i.closed.CompareAndSwap(false, true) {
		return i.runtime.Close(ctx)
	}
	return nil
}

func (i *instance) ready() error {
	if i.closed.Load() || i.unusable.Load() {
		return appplugin.ErrClosed
	}
	return nil
}

func (i *instance) callStatus(ctx context.Context, operation string,
	limit time.Duration, fn api.Function, params ...uint64) error {
	raw, err := i.callInt(ctx, operation, limit, fn, params...)
	if err != nil {
		return err
	}
	status := int32(uint32(raw))
	if status < 0 {
		return &appplugin.GuestStatusError{Operation: operation, Status: status}
	}
	return nil
}

func (i *instance) callInt(ctx context.Context, operation string,
	limit time.Duration, fn api.Function, params ...uint64) (uint64, error) {
	if fn == nil {
		return 0, &appplugin.MissingExportError{Name: "natcatty_" + operation}
	}
	results, err := fn.Call(ctx, params...)
	if err != nil {
		return 0, i.callError(ctx, operation, limit, err)
	}
	if len(results) != 1 {
		return 0, &appplugin.GuestStatusError{Operation: operation, Status: -1}
	}
	return results[0], nil
}

// callError maps a wazero call failure to the stable typed errors. Traps,
// host-import panics, cancellations, and missed deadlines are recovered here
// and never escape as host panics.
func (i *instance) callError(ctx context.Context, operation string,
	limit time.Duration, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		i.markUnusable()
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return &appplugin.DeadlineError{Operation: operation, Limit: limit, Hint: i.timeoutHint}
		}
		return ctxErr
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == watchdogExitCode {
		i.markUnusable()
		return &appplugin.DeadlineError{Operation: operation, Limit: limit, Hint: i.timeoutHint}
	}
	i.markUnusable()
	return &appplugin.TrapError{Operation: operation, Err: err}
}

// markUnusable recycles the module after a trap, panic, or deadline: the
// instance never accepts another call and every handle is invalid.
func (i *instance) markUnusable() {
	i.unusable.Store(true)
	i.handles.RevokeAll()
	_ = i.module.CloseWithExitCode(context.Background(), watchdogExitCode)
}

// startWatchdog arms the instance watchdog for one call.
func (i *instance) startWatchdog(after time.Duration) func() {
	return startWatchdog(after, i.markUnusable)
}

// startWatchdog runs kill when after elapses unless stop is called first. It
// is the backstop for a host import that ignores context cancellation and
// leaves a call running past its deadline.
func startWatchdog(after time.Duration, kill func()) func() {
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(after)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
			kill()
		}
	}()
	return func() { close(done) }
}
