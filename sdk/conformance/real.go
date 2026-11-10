package conformance

import (
	"context"
	"encoding/json"
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
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
	"github.com/mslxl/natcatty/sdk/fakehost"
	"github.com/mslxl/natcatty/sdk/internal/hostenv"
)

// runReal loads the same directory through the production Container, the
// adapterwasm runtime, host services, provider and data hosts, language host,
// and MCP tool registrar, then applies the same scenario. Its Report uses the
// same normalized observations as the fake host so the two can be compared.
func runReal(ctx context.Context, opts Options, scenario fakehost.Scenario) (fakehost.Report, error) {
	report := fakehost.Report{ClockStart: opts.StartNS, RandomSeed: opts.Seed}
	dir, err := filepath.Abs(opts.Directory)
	if err != nil {
		return report, fmt.Errorf("real conformance: resolve %q: %w", opts.Directory, err)
	}
	manifest, err := (adapterpluginmanifest.Source{}).Load(dir)
	if err != nil {
		return report, fmt.Errorf("real conformance: load manifest: %w", err)
	}
	report.PluginID = manifest.ID
	report.ManifestABI = manifest.ABI

	clock := fakehost.NewClock(opts.StartNS, opts.StepNS)
	random := fakehost.NewRandom(opts.Seed)
	now := func() time.Time { return time.Unix(0, clock.CurrentNS()) }
	dataRoot, err := hostenv.TempDataRoot()
	if err != nil {
		return report, fmt.Errorf("real conformance: create data root: %w", err)
	}
	defer os.RemoveAll(dataRoot)
	env, err := hostenv.New(hostenv.Options{Now: now, DataRoot: dataRoot})
	if err != nil {
		return report, fmt.Errorf("real conformance: %w", err)
	}
	defer env.Close()
	ref := appplugin.PluginRef{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Development: true, Source: domainplugin.SourceDevelopment,
	}
	if err := env.Grant(ref, manifest, true, opts.DeniedPermissions); err != nil {
		return report, fmt.Errorf("real conformance: grant permissions: %w", err)
	}

	runtime := adapterwasm.NewRuntime(
		adapterwasm.WithLimits(opts.Limits),
		adapterwasm.WithHostEnv(adapterwasm.HostEnv{
			Now: clock.AdvanceNS, Random: random,
		}),
	)
	defer runtime.Close(context.Background())
	providerHost := appplugin.NewProviderHost(appplugin.ProviderHostOptions{Now: now})
	defer providerHost.Close()
	connectionRegistry := &connectionRecorder{}
	profiles := appplugin.NewMemoryDataProfileStore()
	secrets := appplugin.NewMemoryDataProfileSecrets()
	dataHost := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Now: now, Profiles: profiles, Secrets: secrets,
		NewProfileID: func() string { return "conformance-profile" },
	})
	defer dataHost.Close()
	languageHost := appplugin.NewLanguageHost(appplugin.LanguageHostOptions{
		Authority: env.Authority, Now: now,
	})
	defer languageHost.Close()
	tools := newToolRecorder()
	commands := nopRegistrar{}

	container := appplugin.NewContainer(runtime, adapterpluginmanifest.Source{}, commands,
		appplugin.WithHostServices(env.Services),
		appplugin.WithServiceAuthority(env.Authority),
		appplugin.WithClock(now),
		appplugin.WithFeedback(func(_ appplugin.PluginRef, text string) {
			report.Outputs = append(report.Outputs, text)
		}),
		appplugin.WithLogf(func(_ appplugin.PluginRef, level appplugin.LogLevel, message string) {
			report.Logs = append(report.Logs, fakehost.LogEntry{Level: level, Message: message})
		}),
		appplugin.WithConnectionProviders(providerHost, connectionRegistry),
		appplugin.WithDataProviders(dataHost),
		appplugin.WithLanguageHost(languageHost),
		appplugin.WithMCPToolsRegistrar(tools),
	)
	closed := false
	defer func() {
		if !closed {
			_ = container.Close(context.Background())
		}
	}()
	fiber, err := container.LoadDevelopment(ctx, dir)
	if err != nil {
		return report, fmt.Errorf("real conformance: activate: %w", err)
	}
	ref = fiber.Ref()
	ui, _ := container.NativeUI(ref)

	events := scenario.Events
	if len(events) == 0 {
		events = opts.Events
	}
	for _, event := range scenario.UIEvents {
		if enqueueRealUI(ui, event) {
			report.UIEvents = append(report.UIEvents, fakehost.UIEventRecord{
				Node: event.Node, Kind: event.Kind, Text: event.Text,
			})
		}
	}
	if len(scenario.UIEvents) > 0 {
		if err := fiber.Poll(ctx); err != nil {
			return report, fmt.Errorf("real conformance: poll after ui events: %w", err)
		}
	}
	for _, injected := range events {
		command := qualify(manifest.ID, injected)
		report.Events = append(report.Events, fakehost.Event{Command: command})
		if err := fiber.Dispatch(ctx, command); err != nil {
			return report, fmt.Errorf("real conformance: dispatch %q: %w", command, err)
		}
	}
	provider, _ := providerHost.Lookup(providerProtocol(manifest))
	streams := &streamSlot{}
	for _, call := range scenario.ProviderCalls {
		observation, err := realProviderCall(ctx, provider, streams, call)
		report.Provider = append(report.Provider, observation)
		if err != nil {
			return report, fmt.Errorf("real conformance: provider call: %w", err)
		}
	}
	if len(scenario.DataCalls) > 0 {
		observations, err := realDataCalls(ctx, dataHost, manifest, scenario.DataCalls)
		report.Data = append(report.Data, observations...)
		if err != nil {
			return report, fmt.Errorf("real conformance: data call: %w", err)
		}
	}
	for _, call := range scenario.ToolCalls {
		observation, err := tools.call(ctx, manifest.ID, call)
		report.Tools = append(report.Tools, observation)
		if err != nil {
			return report, fmt.Errorf("real conformance: tool call %q: %w", call.Name, err)
		}
	}
	if err := fiber.Poll(ctx); err != nil {
		return report, fmt.Errorf("real conformance: poll: %w", err)
	}
	if ui != nil {
		report.UI = summarizeUI(ui)
	}
	report.ClockEnd = clock.CurrentNS()
	closed = true
	if err := container.Close(ctx); err != nil {
		return report, fmt.Errorf("real conformance: unload: %w", err)
	}
	return report, nil
}

func summarizeUI(ui *appplugin.UISession) *fakehost.UISummary {
	tree := ui.Tree()
	summary := &fakehost.UISummary{
		Revision:  ui.Revision(),
		Listeners: len(ui.Listeners()),
		Pending:   ui.PendingEvents(),
		Dropped:   ui.DroppedEvents(),
	}
	if tree != nil {
		summary.Nodes = tree.Len()
		summary.Root = uint32(tree.Root)
	}
	return summary
}

func enqueueRealUI(ui *appplugin.UISession, event fakehost.UIEvent) bool {
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
	if event.Node == 0 {
		return ui.Enqueue(native)
	}
	return ui.EnqueueNode(domainnativeui.NodeID(event.Node), native)
}

func providerProtocol(manifest domainplugin.Manifest) string {
	if len(manifest.Contributions.Connections) == 0 {
		return ""
	}
	return manifest.Contributions.Connections[0].Protocol
}

func realProviderCall(ctx context.Context, provider *appplugin.Provider,
	streams *streamSlot, call fakehost.ProviderCall) (fakehost.ProviderObservation, error) {
	method := domainpluginabi.ProviderMethod(call.Method)
	observation := fakehost.ProviderObservation{Method: method.String(), Status: "ok"}
	if provider == nil {
		observation.Status = "error"
		observation.Error = "no connection provider is active"
		return observation, errors.New(observation.Error)
	}
	switch method {
	case domainpluginabi.ProviderMethodDescribe:
		_, err := provider.Describe(ctx)
		return providerObservation(observation, err)
	case domainpluginabi.ProviderMethodValidate:
		err := provider.Validate(ctx, []byte(call.Payload))
		return providerObservation(observation, err)
	case domainpluginabi.ProviderMethodProbe:
		err := provider.Probe(ctx, []byte(call.Payload))
		return providerObservation(observation, err)
	case domainpluginabi.ProviderMethodOpen:
		var request struct {
			Target string `json:"target"`
			HostID string `json:"hostId"`
			Cols   int    `json:"cols"`
			Rows   int    `json:"rows"`
		}
		if err := json.Unmarshal([]byte(call.Payload), &request); err != nil && call.Payload != "" {
			observation.Status = "error"
			observation.Error = "malformed open payload"
			return observation, err
		}
		cols, rows := request.Cols, request.Rows
		if cols <= 0 {
			cols = 80
		}
		if rows <= 0 {
			rows = 24
		}
		config := []byte("{}")
		if request.Target != "" {
			config, _ = json.Marshal(map[string]string{"target": request.Target})
		}
		stream, err := provider.Open(ctx, appplugin.ProviderOpenRequest{
			HostID: request.HostID, Cols: cols, Rows: rows, Config: config,
		})
		if err != nil {
			return providerObservation(observation, err)
		}
		observation.Cols, observation.Rows = stream.InitialSize()
		observation.Restore = stream.RestoreDescriptor()
		streams.set(stream)
		return observation, nil
	case domainpluginabi.ProviderMethodPump:
		stream := streams.get()
		if stream == nil {
			observation.Status = "error"
			observation.Error = "no provider stream"
			return observation, errors.New(observation.Error)
		}
		if len(call.Input) > 0 {
			if _, err := stream.Write(call.Input); err != nil {
				return providerObservation(observation, err)
			}
			output, err := readProviderStream(stream, len(call.Input))
			observation.Output = output
			return providerObservation(observation, err)
		}
		return observation, nil
	case domainpluginabi.ProviderMethodClose:
		stream := streams.get()
		if stream != nil {
			err := stream.Close()
			streams.clear()
			return providerObservation(observation, err)
		}
		return observation, nil
	case domainpluginabi.ProviderMethodResize:
		stream := streams.get()
		if stream == nil {
			return providerObservation(observation, errors.New("no provider stream"))
		}
		var request struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}
		if err := json.Unmarshal([]byte(call.Payload), &request); err != nil {
			return providerObservation(observation, err)
		}
		return providerObservation(observation, stream.Resize(request.Cols, request.Rows))
	case domainpluginabi.ProviderMethodSignal:
		stream := streams.get()
		if stream == nil {
			return providerObservation(observation, errors.New("no provider stream"))
		}
		var request struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(call.Payload), &request); err != nil {
			return providerObservation(observation, err)
		}
		return providerObservation(observation, stream.Signal(request.Name))
	default:
		return providerObservation(observation, fmt.Errorf("unknown provider method %d", call.Method))
	}
}

func providerObservation(observation fakehost.ProviderObservation,
	err error) (fakehost.ProviderObservation, error) {
	if err != nil {
		observation.Status = "error"
		observation.Error = err.Error()
	}
	return observation, err
}

func readProviderStream(stream *appplugin.ProviderStream, expected int) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		buffer := make([]byte, expected)
		total := 0
		for total < expected {
			n, err := stream.Read(buffer[total:])
			total += n
			if err != nil {
				done <- result{data: buffer[:total], err: err}
				return
			}
			if n == 0 {
				break
			}
		}
		done <- result{data: buffer[:total]}
	}()
	select {
	case got := <-done:
		return got.data, got.err
	case <-time.After(10 * time.Second):
		return nil, errors.New("provider stream read timed out")
	}
}

// streamSlot holds the one stream opened by a sequential conformance
// scenario. Each run owns its own slot, so concurrent checks never share
// provider state.
type streamSlot struct {
	mu     sync.Mutex
	stream *appplugin.ProviderStream
}

func (s *streamSlot) set(stream *appplugin.ProviderStream) {
	s.mu.Lock()
	s.stream = stream
	s.mu.Unlock()
}

func (s *streamSlot) get() *appplugin.ProviderStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream
}

func (s *streamSlot) clear() {
	s.mu.Lock()
	s.stream = nil
	s.mu.Unlock()
}

func realDataCalls(ctx context.Context, host *appplugin.DataProviderHost,
	manifest domainplugin.Manifest, calls []fakehost.DataCall) ([]fakehost.DataObservation, error) {
	if len(manifest.Contributions.DataProviders) == 0 {
		return nil, errors.New("no data provider is declared")
	}
	contribution := manifest.Contributions.DataProviders[0]
	qualified := domainplugin.QualifiedDataProviderID(manifest.ID, contribution.ID)
	draft := domainplugin.DataProfileDraft{
		Label: "conformance", Provider: qualified, Mode: domainplugin.DataModeReadWrite,
		Fields: map[string]string{}, Secrets: map[string]string{},
	}
	for _, field := range contribution.ProfileFields {
		value := "conformance-value"
		if field.Secret {
			value = "conformance-secret"
			draft.Secrets[field.Key] = value
			continue
		}
		draft.Fields[field.Key] = value
	}
	profile, err := host.SaveProfile(ctx, "", draft)
	if err != nil {
		return nil, err
	}
	lease, err := host.Acquire(ctx, profile.ID, domainplugin.DataModeReadWrite)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	var observations []fakehost.DataObservation
	var query *appplugin.DataQuery
	for _, call := range calls {
		method := domainpluginabi.DataProviderMethod(call.Method)
		observation := fakehost.DataObservation{Method: method.String(), Status: "ok"}
		switch method {
		case domainpluginabi.DataProviderMethodOpen:
			observations = append(observations, observation)
		case domainpluginabi.DataProviderMethodQuery:
			var request struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(call.Payload), &request); err != nil {
				return observations, err
			}
			var result domainplugin.DataResult
			query, result, err = lease.Query(ctx, appplugin.DataQueryRequest{Query: request.Query})
			observations = append(observations, dataResultObservation(method.String(), result, err))
			if err != nil {
				return observations, err
			}
		case domainpluginabi.DataProviderMethodFetch:
			if query == nil {
				return observations, errors.New("no query to fetch")
			}
			result, err := query.Fetch(ctx)
			observations = append(observations, dataResultObservation(method.String(), result, err))
			if err != nil {
				return observations, err
			}
		case domainpluginabi.DataProviderMethodClose:
			if err := lease.Close(ctx); err != nil {
				return observations, err
			}
			observations = append(observations, observation)
		default:
			err := fmt.Errorf("%s is not covered by the real data fixture", method)
			observation.Status = "error"
			observation.Error = err.Error()
			observations = append(observations, observation)
			return observations, err
		}
	}
	return observations, nil
}

func dataResultObservation(method string, result domainplugin.DataResult,
	err error) fakehost.DataObservation {
	observation := fakehost.DataObservation{Method: method, Status: "ok"}
	if err != nil {
		observation.Status = "error"
		observation.Error = err.Error()
		return observation
	}
	observation.Kind = string(result.Kind)
	observation.Class = string(result.Class)
	observation.More = result.More
	observation.Partial = result.Partial
	if result.Table != nil {
		for _, column := range result.Table.Columns {
			observation.Columns = append(observation.Columns, column.Name)
		}
		observation.Rows = len(result.Table.Rows)
	}
	if result.Summary != nil {
		observation.AffectedRows = result.Summary.AffectedRows
	}
	return observation
}

// connectionRecorder remembers the provider published by activation so the
// scenario can call it directly.
type connectionRecorder struct {
	mu        sync.Mutex
	providers []*appplugin.Provider
}

func (r *connectionRecorder) RegisterConnectionProvider(_ appplugin.PluginRef,
	_ domainplugin.ConnectionProvider, provider *appplugin.Provider) (appplugin.Disposer, error) {
	r.mu.Lock()
	r.providers = append(r.providers, provider)
	r.mu.Unlock()
	return nopDisposer, nil
}

// toolRecorder maps qualified tool names to the container's caller.
type toolRecorder struct {
	mu    sync.Mutex
	calls map[string]appplugin.ToolCaller
}

func newToolRecorder() *toolRecorder {
	return &toolRecorder{calls: make(map[string]appplugin.ToolCaller)}
}

func (r *toolRecorder) RegisterMCPTool(ref appplugin.PluginRef, tool domainplugin.MCPTool,
	call appplugin.ToolCaller) (appplugin.Disposer, error) {
	name := domainplugin.QualifiedMCPToolName(ref.ID, tool.Name)
	r.mu.Lock()
	r.calls[name] = call
	r.mu.Unlock()
	return nopDisposer, nil
}

func (r *toolRecorder) call(ctx context.Context, pluginID string,
	call fakehost.ToolCall) (fakehost.ToolObservation, error) {
	name := call.Name
	if !strings.HasPrefix(name, "plugin.") {
		name = domainplugin.QualifiedMCPToolName(pluginID, name)
	}
	r.mu.Lock()
	caller := r.calls[name]
	r.mu.Unlock()
	if caller == nil {
		return fakehost.ToolObservation{Tool: name, Error: "tool is not registered"},
			fmt.Errorf("tool %q is not registered", name)
	}
	var progress []fakehost.ToolProgress
	payload, err := caller.CallTool(ctx, name, call.Arguments, 0,
		func(value uint32, message string) {
			progress = append(progress, fakehost.ToolProgress{Progress: value, Message: message})
		})
	observation := fakehost.ToolObservation{
		Tool: name, Result: string(payload), Progress: progress,
	}
	if err != nil {
		observation.Error = err.Error()
	}
	return observation, err
}

type nopRegistrar struct{}

func (nopRegistrar) RegisterCommand(appplugin.PluginRef, domainplugin.Command,
	appplugin.Handler) (appplugin.Disposer, error) {
	return nopDisposer, nil
}

func (nopRegistrar) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return nopDisposer, nil
}

func (nopRegistrar) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return nopDisposer, nil
}

func nopDisposer(context.Context) error { return nil }

func qualify(pluginID, event string) string {
	if strings.HasPrefix(event, "plugin.") {
		return event
	}
	return domainplugin.QualifiedCommandID(pluginID, event)
}
