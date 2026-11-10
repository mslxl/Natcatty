// Package fakehost runs a plugin module outside the app with a virtual clock,
// deterministic randomness, injected command and UI events, provider and data
// fixtures, MCP tool calls, and explicit lifecycle control. It is the CLI's
// `run` engine and the behavioral half of conformance.
//
// Host services are served by the production dispatcher over the deterministic
// fixture ports from sdk/internal/hostenv, so a service call observes the same
// permission, scope, and envelope behavior as the real runtime. StubServices
// instead answers every non-UI call from sdk/idl/stub.json, the one definition
// the Rust in-language mock is generated from.
package fakehost

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/sdk/internal/hostenv"
	"github.com/mslxl/natcatty/sdk/internal/plugindir"
	"github.com/mslxl/natcatty/sdk/stub"
)

// Options configures one fake-host run.
type Options struct {
	// Directory is the unpacked plugin directory containing plugin.toml.
	Directory string
	// Events are command events dispatched after init when Scenario.Events is
	// empty. A value without the "plugin." prefix is qualified with the plugin
	// id.
	Events []string
	// StartNS is the first value now_ns returns. Zero is a valid virtual time.
	StartNS int64
	// StepNS advances the virtual clock on every now_ns call.
	StepNS int64
	// Seed selects the deterministic random stream.
	Seed uint64
	// Limits overrides the runtime deadlines; zero means the contract defaults.
	Limits appplugin.Limits
	// Scenario adds command, UI, provider, data, and MCP tool calls to the run.
	// Scenario.Events replaces Events when it is non-empty.
	Scenario Scenario
	// StubServices answers every non-UI host-service call from the shared stub
	// definition instead of the live production dispatcher. The native UI is
	// always served live because it needs the real retained tree.
	StubServices bool
	// DeniedPermissions withholds named permissions even when the manifest
	// requires them. Optional permissions are granted unless listed here.
	DeniedPermissions []string
	// DataRoot is the root of the plugin's private storage. Empty creates a
	// temporary directory owned by the run.
	DataRoot string
}

// Scenario is one deterministic sequence layered on top of init and command
// dispatch. The same value drives the fake host and the real-runtime
// conformance runner.
type Scenario struct {
	// Events are command events. Local ids are qualified with the plugin id.
	Events []string
	// UIEvents are enqueued after init, then one guest poll drains them.
	UIEvents []UIEvent
	// ProviderCalls drive the optional connection-provider export.
	ProviderCalls []ProviderCall
	// DataCalls drive the optional data-provider export.
	DataCalls []DataCall
	// ToolCalls drive the manifest's declared MCP tools.
	ToolCalls []ToolCall
}

// UIEvent is one native UI event enqueued for the guest. Node is the guest
// node id; zero means a session-wide event. Kind is the generated EventKind
// value.
type UIEvent struct {
	Node      uint32
	Kind      int32
	Text      string
	Key       int32
	Modifiers uint32
	Button    int32
	Index     int32
	Related   uint64
	X         float64
	Y         float64
	DX        float64
	DY        float64
	Width     float64
	Height    float64
}

// ProviderCall is one host-to-guest connection-provider request. Method uses
// the generated provider method ids. Input is queued on Stream before the call
// so a following Pump can echo it.
type ProviderCall struct {
	Method  uint32
	Payload string
	Stream  uint32
	Input   []byte
	// ReadOutput drains and records the stream output after the call. Pump
	// calls default to true; other methods keep the stream untouched.
	ReadOutput bool
}

// DataCall is one host-to-guest data-provider request. CancelAfter makes the
// cooperative cancellation poll answer true after that many probes, which is
// how the fake host reaches the sample's cancelled fetch path deterministically.
// It is a fake-host fixture control; the scenario conformance shares with the
// real runtime does not rely on poll-count cancellation.
type DataCall struct {
	Method      uint32
	Payload     string
	CancelAfter int
}

// ToolCall is one MCP tool call. Name may be local or fully qualified.
type ToolCall struct {
	Name      string
	Arguments map[string]any
}

// LogEntry is one guest log line captured by the fake host.
type LogEntry struct {
	Level   appplugin.LogLevel
	Message string
}

// Event is one dispatched command.
type Event struct {
	Command string
}

// ServiceCall is one host-service call observed at the ABI boundary.
type ServiceCall struct {
	Service  string
	Method   uint32
	Request  []byte
	Response []byte
}

// UIEventRecord is one event the fake host successfully enqueued.
type UIEventRecord struct {
	Node uint32
	Kind int32
	Text string
}

// UISummary is the observable retained-tree state after a run.
type UISummary struct {
	Revision  uint64
	Nodes     int
	Root      uint32
	Listeners int
	Pending   int
	Dropped   int
}

// ProviderObservation is one normalized provider call observation.
type ProviderObservation struct {
	Method  string
	Status  string
	Message string
	Cols    int
	Rows    int
	Restore []byte
	Output  []byte
	Error   string
}

// DataObservation is one normalized data-provider call observation.
type DataObservation struct {
	Method       string
	Status       string
	Message      string
	Kind         string
	Class        string
	Columns      []string
	Rows         int
	More         bool
	Partial      bool
	AffectedRows int64
	Error        string
}

// ToolProgress is one advisory progress notification.
type ToolProgress struct {
	Progress uint32
	Message  string
}

// ToolObservation is one normalized MCP tool call observation.
type ToolObservation struct {
	Tool     string
	Request  uint32
	Result   string
	Progress []ToolProgress
	Error    string
}

// Report is the observable result of a fake-host run.
type Report struct {
	PluginID    string
	Logs        []LogEntry
	Outputs     []string
	Events      []Event
	ClockStart  int64
	ClockEnd    int64
	RandomSeed  uint64
	ManifestABI int
	Service     []ServiceCall
	UIEvents    []UIEventRecord
	UI          *UISummary
	Provider    []ProviderObservation
	Data        []DataObservation
	Tools       []ToolObservation
}

// Feedback joins the visible outputs in order.
func (r Report) Feedback() string { return strings.Join(r.Outputs, "") }

// Run starts, drives, and unloads one module.
func Run(ctx context.Context, opts Options) (Report, error) {
	runner, err := Start(ctx, opts)
	if runner == nil {
		return Report{RandomSeed: opts.Seed, ClockStart: opts.StartNS}, err
	}
	if err == nil {
		err = runner.Apply(ctx, opts.Scenario)
	}
	report := runner.Report()
	if unloadErr := runner.Unload(ctx); unloadErr != nil && err == nil {
		err = unloadErr
	}
	runner.Close()
	return report, err
}

// Runner is one started module with explicit lifecycle control. Start performs
// init; Apply drives a scenario; Unload runs shutdown and close exactly once.
type Runner struct {
	opts     Options
	dir      string
	manifest domainplugin.Manifest
	clock    *Clock
	random   *Random
	runtime  *adapterwasm.Runtime
	instance appplugin.Instance

	env      *hostenv.Environment
	fiber    domainplugin.FiberID
	session  *appplugin.ServiceSession
	dataRoot string
	tempRoot bool

	mu           sync.Mutex
	logs         []LogEntry
	outputs      []string
	events       []Event
	serviceCalls []ServiceCall
	uiEvents     []UIEventRecord
	provider     []ProviderObservation
	data         []DataObservation
	tools        []ToolObservation

	providerInput  map[uint32][]byte
	providerOutput map[uint32][]byte
	providerClosed map[uint32]bool
	nextProviderID uint64

	cancelData map[uint64]int
	nextDataID uint64

	nextToolID uint64
	toolCalls  map[uint32]*pendingToolCall

	callMu   sync.Mutex
	inflight int
	callIdle chan struct{}

	closed    bool
	unloaded  bool
	unloading bool
}

type pendingToolCall struct {
	result   chan []byte
	progress []ToolProgress
}

// Start loads, validates, instantiates, and initializes one module. On failure
// it returns the runner when enough state exists to inspect partial output.
func Start(ctx context.Context, opts Options) (*Runner, error) {
	if strings.TrimSpace(opts.Directory) == "" {
		return nil, errors.New("fake host: plugin directory is required")
	}
	dir, err := filepath.Abs(opts.Directory)
	if err != nil {
		return nil, fmt.Errorf("fake host: resolve %q: %w", opts.Directory, err)
	}
	manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		return nil, fmt.Errorf("fake host: load manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("fake host: invalid manifest: %w", err)
	}
	wasmPath, err := plugindir.Resolve(dir, manifest.Wasm.Path)
	if err != nil {
		return nil, fmt.Errorf("fake host: %w", err)
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("fake host: read module %q: %w", manifest.Wasm.Path, err)
	}

	clock := NewClock(opts.StartNS, opts.StepNS)
	random := NewRandom(opts.Seed)
	runner := &Runner{
		opts:           opts,
		dir:            dir,
		manifest:       manifest,
		clock:          clock,
		random:         random,
		providerInput:  make(map[uint32][]byte),
		providerOutput: make(map[uint32][]byte),
		providerClosed: make(map[uint32]bool),
		cancelData:     make(map[uint64]int),
		toolCalls:      make(map[uint32]*pendingToolCall),
		callIdle:       closedChannel(),
	}
	runner.runtime = adapterwasm.NewRuntime(
		adapterwasm.WithLimits(opts.Limits),
		adapterwasm.WithHostEnv(adapterwasm.HostEnv{
			Now:    clock.AdvanceNS,
			Random: random,
		}),
	)
	dataRoot := opts.DataRoot
	if dataRoot == "" {
		dataRoot, err = hostenv.TempDataRoot()
		if err != nil {
			runner.runtime.Close(context.Background())
			return nil, fmt.Errorf("fake host: create data root: %w", err)
		}
		runner.tempRoot = true
	}
	runner.dataRoot = dataRoot
	env, err := hostenv.New(hostenv.Options{
		Now:      func() time.Time { return time.Unix(0, clock.CurrentNS()) },
		DataRoot: dataRoot,
	})
	if err != nil {
		runner.runtime.Close(context.Background())
		runner.cleanupDataRoot()
		return nil, fmt.Errorf("fake host: %w", err)
	}
	runner.env = env
	ref := runner.ref()
	if err := env.Grant(ref, manifest, true, opts.DeniedPermissions); err != nil {
		runner.runtime.Close(context.Background())
		env.Close()
		runner.cleanupDataRoot()
		return nil, fmt.Errorf("fake host: grant permissions: %w", err)
	}
	runner.fiber = domainplugin.NewFiberID("fakehost", manifest.ID, 1)
	runner.session = env.Services.Open(ref, runner.fiber, map[string]any{})
	if runner.session == nil {
		runner.runtime.Close(context.Background())
		env.Close()
		runner.cleanupDataRoot()
		return nil, errors.New("fake host: host services are closed")
	}

	limiter := appplugin.NewLogLimiter(appplugin.MaxLogsPerSecond)
	var dispatcher appplugin.ServiceDispatcher
	live := runner.session.Dispatcher()
	if opts.StubServices {
		dispatcher = stubDispatcher{live: live, record: runner.recordService}
	} else {
		dispatcher = recordingDispatcher{inner: live, record: runner.recordService}
	}
	instance, err := runner.runtime.Instantiate(ctx, appplugin.ModuleSpec{
		Name:           manifest.ID,
		WASM:           wasm,
		MemoryMaxBytes: manifest.Wasm.EffectiveMemoryMaxBytes(),
	}, appplugin.HostSink{
		Log: func(level appplugin.LogLevel, message string) {
			if err := limiter.Take(time.Unix(0, clock.CurrentNS())); err != nil {
				return
			}
			runner.mu.Lock()
			runner.logs = append(runner.logs, LogEntry{Level: level, Message: message})
			runner.mu.Unlock()
		},
		Output: func(data []byte) {
			runner.mu.Lock()
			runner.outputs = append(runner.outputs, string(data))
			runner.mu.Unlock()
		},
		Services: dispatcher,
		Provider: runner.providerCallbacks(),
		Data: &appplugin.DataProviderCallbacks{
			Cancelled: func(requestID uint64) bool {
				runner.mu.Lock()
				defer runner.mu.Unlock()
				return consumeCancellation(runner.cancelData, requestID)
			},
		},
		ToolResult: func(requestID uint32, payload []byte) int32 {
			return runner.deliverToolResult(requestID, payload)
		},
		ToolProgress: func(requestID uint32, progress uint32, message string) int32 {
			return runner.deliverToolProgress(requestID, progress, message)
		},
	})
	if err != nil {
		runner.runtime.Close(context.Background())
		env.Close()
		runner.cleanupDataRoot()
		return runner, fmt.Errorf("fake host: instantiate: %w", err)
	}
	runner.instance = instance
	runner.session.BindHandles(instance.Handles())
	if err := instance.Init(ctx); err != nil {
		return runner, fmt.Errorf("fake host: init: %w", err)
	}
	return runner, nil
}

// Apply dispatches a scenario: UI events first, one poll, then commands,
// provider, data, and MCP tool calls in that stable order, followed by a final
// poll. Callers that only pass Options.Events keep the historical behavior.
func (r *Runner) Apply(ctx context.Context, scenario Scenario) error {
	events := scenario.Events
	if len(events) == 0 {
		events = r.opts.Events
	}
	for _, event := range scenario.UIEvents {
		r.UIEvent(event)
	}
	if len(scenario.UIEvents) > 0 {
		if err := r.Poll(ctx); err != nil {
			return fmt.Errorf("fake host: poll after ui events: %w", err)
		}
	}
	for _, injected := range events {
		command := qualify(r.manifest.ID, injected)
		r.mu.Lock()
		r.events = append(r.events, Event{Command: command})
		r.mu.Unlock()
		if err := r.instance.Dispatch(ctx, command); err != nil {
			return fmt.Errorf("fake host: dispatch %q: %w", command, err)
		}
	}
	for _, call := range scenario.ProviderCalls {
		if _, err := r.ProviderCall(ctx, call); err != nil {
			return fmt.Errorf("fake host: provider call: %w", err)
		}
	}
	for _, call := range scenario.DataCalls {
		if _, err := r.DataCall(ctx, call); err != nil {
			return fmt.Errorf("fake host: data call: %w", err)
		}
	}
	for _, call := range scenario.ToolCalls {
		if _, err := r.ToolCall(ctx, call); err != nil {
			return fmt.Errorf("fake host: tool call %q: %w", call.Name, err)
		}
	}
	if err := r.Poll(ctx); err != nil {
		return fmt.Errorf("fake host: poll: %w", err)
	}
	return nil
}

// Dispatch runs one command on the live instance.
func (r *Runner) Dispatch(ctx context.Context, command string) error {
	if r == nil || r.instance == nil {
		return errors.New("fake host: no live instance")
	}
	qualified := qualify(r.manifest.ID, command)
	r.mu.Lock()
	r.events = append(r.events, Event{Command: qualified})
	r.mu.Unlock()
	return r.instance.Dispatch(ctx, qualified)
}

// Poll runs one guest poll turn.
func (r *Runner) Poll(ctx context.Context) error {
	if r == nil || r.instance == nil {
		return errors.New("fake host: no live instance")
	}
	return r.instance.Poll(ctx)
}

// UIEvent enqueues one native UI event for the guest. It reports false when
// the guest has no matching listener or the node is not live.
func (r *Runner) UIEvent(event UIEvent) bool {
	ui := r.NativeUI()
	if ui == nil {
		return false
	}
	native := domainnativeui.Event{
		Kind:   domainnativeui.EventKind(event.Kind),
		Text:   event.Text,
		Key:    domainnativeui.Key(event.Key),
		Mods:   domainnativeui.Modifiers(event.Modifiers),
		Button: event.Button,
		Index:  event.Index,
		X:      float32(event.X),
		Y:      float32(event.Y),
		DX:     float32(event.DX),
		DY:     float32(event.DY),
		Width:  float32(event.Width),
		Height: float32(event.Height),
	}
	if event.Related != 0 {
		native.Related = domainnativeui.NodeRef(event.Related)
	}
	var queued bool
	if event.Node == 0 {
		queued = ui.Enqueue(native)
	} else {
		queued = ui.EnqueueNode(domainnativeui.NodeID(event.Node), native)
	}
	if queued {
		r.mu.Lock()
		r.uiEvents = append(r.uiEvents, UIEventRecord{
			Node: event.Node, Kind: event.Kind, Text: event.Text,
		})
		r.mu.Unlock()
	}
	return queued
}

// NativeUI returns the live native UI session.
func (r *Runner) NativeUI() *appplugin.UISession {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.NativeUI()
}

// Report returns the observations collected so far. The UI summary is captured
// before Unload so callers still see the final tree and listener state.
func (r *Runner) Report() Report {
	if r == nil {
		return Report{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	report := Report{
		PluginID:    r.manifest.ID,
		Logs:        append([]LogEntry(nil), r.logs...),
		Outputs:     append([]string(nil), r.outputs...),
		Events:      append([]Event(nil), r.events...),
		ClockStart:  r.opts.StartNS,
		ClockEnd:    r.clock.CurrentNS(),
		RandomSeed:  r.opts.Seed,
		ManifestABI: r.manifest.ABI,
		Service:     cloneServiceCalls(r.serviceCalls),
		UIEvents:    append([]UIEventRecord(nil), r.uiEvents...),
		Provider:    append([]ProviderObservation(nil), r.provider...),
		Data:        append([]DataObservation(nil), r.data...),
		Tools:       append([]ToolObservation(nil), r.tools...),
	}
	if ui := r.NativeUI(); ui != nil {
		tree := ui.Tree()
		summary := &UISummary{
			Revision:  ui.Revision(),
			Listeners: len(ui.Listeners()),
			Pending:   ui.PendingEvents(),
			Dropped:   ui.DroppedEvents(),
		}
		if tree != nil {
			summary.Nodes = tree.Len()
			summary.Root = uint32(tree.Root)
		}
		report.UI = summary
	}
	return report
}

func cloneServiceCalls(calls []ServiceCall) []ServiceCall {
	out := make([]ServiceCall, len(calls))
	for i, call := range calls {
		out[i] = ServiceCall{
			Service: call.Service, Method: call.Method,
			Request:  append([]byte(nil), call.Request...),
			Response: append([]byte(nil), call.Response...),
		}
	}
	return out
}

func (r *Runner) recordService(call appplugin.ServiceCall, response []byte) {
	r.mu.Lock()
	r.serviceCalls = append(r.serviceCalls, ServiceCall{
		Service: call.Service, Method: call.Method,
		Request:  append([]byte(nil), call.Request...),
		Response: append([]byte(nil), response...),
	})
	r.mu.Unlock()
}

// Unload runs shutdown and close once and releases the service session. It is
// the explicit lifecycle control the fake host exposes to tests. An in-flight
// fixture call is cancelled cooperatively first; if the guest does not yield
// within the runtime's call deadline, Unload returns ErrCallInFlight and the
// caller can retry once the watchdog has stopped the call.
func (r *Runner) Unload(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.unloaded {
		r.mu.Unlock()
		return nil
	}
	if r.unloading {
		r.mu.Unlock()
		return appplugin.ErrCallInFlight
	}
	r.unloading = true
	// A data or provider fixture that is still running observes cancellation
	// on its next poll, so the fake host can unload deterministically without
	// asking the caller to cancel first.
	for id := range r.cancelData {
		r.cancelData[id] = 1
	}
	r.mu.Unlock()

	limits := r.opts.Limits.Normalize()
	if !r.drainCalls(limits.CallTimeout + limits.WatchdogGrace) {
		r.mu.Lock()
		r.unloading = false
		r.mu.Unlock()
		return appplugin.ErrCallInFlight
	}

	var errs []error
	if r.instance != nil {
		if err := r.instance.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		if err := r.instance.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if r.env != nil {
		r.env.Services.Close(r.fiber)
	}
	r.mu.Lock()
	r.unloaded = true
	r.unloading = false
	r.mu.Unlock()
	return errors.Join(errs...)
}

// InFlight reports the number of fake-host fixture calls currently executing
// a guest export. Tests use it to wait for a call to start without a fixed
// delay.
func (r *Runner) InFlight() int {
	if r == nil {
		return 0
	}
	r.callMu.Lock()
	defer r.callMu.Unlock()
	return r.inflight
}

func (r *Runner) beginCall() {
	r.callMu.Lock()
	if r.inflight == 0 {
		r.callIdle = make(chan struct{})
	}
	r.inflight++
	r.callMu.Unlock()
}

func (r *Runner) endCall() {
	r.callMu.Lock()
	r.inflight--
	if r.inflight == 0 {
		close(r.callIdle)
	}
	r.callMu.Unlock()
}

// drainCalls waits until every fixture call has returned. The timeout is the
// runtime's own call budget, so a spinning caller unblocks no later than the
// watchdog would stop it.
func (r *Runner) drainCalls(timeout time.Duration) bool {
	r.callMu.Lock()
	idle := r.callIdle
	r.callMu.Unlock()
	if timeout <= 0 {
		timeout = appplugin.DefaultCallTimeout + appplugin.DefaultWatchdogGrace
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return r.InFlight() == 0
	}
}

func closedChannel() chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}

// Close releases the runtime and every fixture port. It does not run the guest
// shutdown; call Unload first for the full lifecycle.
func (r *Runner) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()
	if r.instance != nil && !r.unloaded {
		_ = r.instance.Close(context.Background())
	}
	if r.env != nil {
		r.env.Close()
	}
	if r.runtime != nil {
		_ = r.runtime.Close(context.Background())
	}
	r.cleanupDataRoot()
}

func (r *Runner) cleanupDataRoot() {
	if r != nil && r.tempRoot && r.dataRoot != "" {
		_ = os.RemoveAll(r.dataRoot)
		r.tempRoot = false
	}
}

func (r *Runner) ref() appplugin.PluginRef {
	return appplugin.PluginRef{
		ID:          r.manifest.ID,
		Name:        r.manifest.Name,
		Version:     r.manifest.Version,
		Development: true,
		Source:      domainplugin.SourceDevelopment,
	}
}

// stubDispatcher answers every non-UI call from the shared stub definition.
// The UI stays on the live dispatcher because it needs the retained tree.
type stubDispatcher struct {
	live   appplugin.ServiceDispatcher
	record func(appplugin.ServiceCall, []byte)
}

func (d stubDispatcher) Call(ctx context.Context, call appplugin.ServiceCall) []byte {
	var payload []byte
	if call.Service == appplugin.ServiceUI {
		payload = d.live.Call(ctx, call)
	} else {
		response, err := stub.ResponseByID(call.Service, call.Method)
		if err == nil {
			payload = response
		} else {
			payload = stub.NotFoundResponse(err)
		}
	}
	if d.record != nil {
		d.record(call, payload)
	}
	return payload
}

// recordingDispatcher records every service call the guest makes while
// delegating to the live production dispatcher.
type recordingDispatcher struct {
	inner  appplugin.ServiceDispatcher
	record func(appplugin.ServiceCall, []byte)
}

func (d recordingDispatcher) Call(ctx context.Context, call appplugin.ServiceCall) []byte {
	response := d.inner.Call(ctx, call)
	if d.record != nil {
		d.record(call, response)
	}
	return response
}

// qualify accepts either a local command id or an already-qualified host
// command id. The guest receives one string; the fake host only normalizes
// the common local form.
func qualify(pluginID, event string) string {
	if strings.HasPrefix(event, "plugin.") {
		return event
	}
	return domainplugin.QualifiedCommandID(pluginID, event)
}

// Clock is a virtual nanosecond clock. AdvanceNS returns the current value
// and then advances by step, so every call is observable and reproducible.
type Clock struct {
	mu   sync.Mutex
	now  int64
	step int64
}

// NewClock returns a virtual clock starting at startNS and advancing by stepNS
// per AdvanceNS call.
func NewClock(startNS, stepNS int64) *Clock {
	return &Clock{now: startNS, step: stepNS}
}

// AdvanceNS returns the current virtual time and advances it one step.
func (c *Clock) AdvanceNS() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.now
	c.now += c.step
	return value
}

// CurrentNS returns the current virtual time without advancing it.
func (c *Clock) CurrentNS() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Random is a deterministic byte stream based on SplitMix64. One seed always
// produces the same bytes on every platform.
type Random struct {
	mu    sync.Mutex
	state uint64
	block [8]byte
	used  int
}

// NewRandom returns a deterministic stream for seed.
func NewRandom(seed uint64) *Random { return &Random{state: seed, used: 8} }

// Read fills p from the deterministic stream. It never returns an error.
func (r *Random) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		if r.used == len(r.block) {
			r.state += 0x9E3779B97F4A7C15
			value := r.state
			value = (value ^ (value >> 30)) * 0xBF58476D1CE4E5B9
			value = (value ^ (value >> 27)) * 0x94D049BB133111EB
			value ^= value >> 31
			binary.LittleEndian.PutUint64(r.block[:], value)
			r.used = 0
		}
		p[i] = r.block[r.used]
		r.used++
	}
	return len(p), nil
}
