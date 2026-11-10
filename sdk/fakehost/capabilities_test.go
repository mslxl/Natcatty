package fakehost_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/sdk/fakehost"
	"github.com/mslxl/natcatty/sdk/stub"
)

func sample(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", name)
}

func waitInFlight(t *testing.T, runner *fakehost.Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runner.InFlight() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("fixture call never started")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCounterSampleAcceptsUIEventsAndReportsState(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "counter-view"),
		Scenario: fakehost.Scenario{
			UIEvents: []fakehost.UIEvent{
				{Node: 3, Kind: int32(4), Text: "Ada"}, // input
				{Node: 4, Kind: int32(0)},              // click
			},
			Events: []string{"report"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.UIEvents) != 2 {
		t.Fatalf("queued UI events = %+v", report.UIEvents)
	}
	if report.UI == nil || report.UI.Listeners < 2 {
		t.Fatalf("UI summary = %+v", report.UI)
	}
	output := strings.Join(report.Outputs, "\n")
	for _, want := range []string{"counter=1", "name=Ada", "report.done"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestWebviewSampleReceivesSessionWideBridgeEvent(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "webview-view"),
		Scenario: fakehost.Scenario{
			UIEvents: []fakehost.UIEvent{{
				Node: 0, Kind: 18, // EventWebview
				Text: `{"type":"request","request":1,"method":"greet","payload":"fake host"}`,
			}},
			Events: []string{"report"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := strings.Join(report.Outputs, "\n")
	for _, want := range []string{"handled=1", "last=greet"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLoopbackProviderFixtureDrivesTheConnectionSurface(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "loopback-provider"),
		Scenario: fakehost.Scenario{
			ProviderCalls: []fakehost.ProviderCall{
				{Method: uint32(domainpluginabi.ProviderMethodValidate),
					Payload: `{"target":"sample"}`},
				{Method: uint32(domainpluginabi.ProviderMethodOpen),
					Payload: `{"target":"sample","hostId":"host-1"}`},
				{Method: uint32(domainpluginabi.ProviderMethodPump), Stream: 1,
					Payload: `{"stream":1}`, Input: []byte("fake host\n")},
				{Method: uint32(domainpluginabi.ProviderMethodClose), Stream: 1,
					Payload: `{"stream":1}`},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Provider) != 4 {
		t.Fatalf("provider observations = %+v", report.Provider)
	}
	for _, observation := range report.Provider {
		if observation.Status != "ok" {
			t.Fatalf("provider observation = %+v", observation)
		}
	}
	opened := report.Provider[1]
	if opened.Cols != 100 || opened.Rows != 30 || string(opened.Restore) != "echo-state" {
		t.Fatalf("open observation = %+v", opened)
	}
	pumped := report.Provider[2]
	if string(pumped.Output) != "fake host\n" {
		t.Fatalf("pump output = %q", pumped.Output)
	}
}

func TestDataProviderFixtureQueriesAndCooperativeCancellation(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "data-provider"),
		Scenario: fakehost.Scenario{
			DataCalls: []fakehost.DataCall{
				{Method: uint32(domainpluginabi.DataProviderMethodOpen), Payload: `{}`},
				{Method: uint32(domainpluginabi.DataProviderMethodQuery),
					Payload: `{"query":"rows"}`},
				{Method: uint32(domainpluginabi.DataProviderMethodFetch),
					Payload: `{"query":2}`, CancelAfter: 1},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Data) != 3 {
		t.Fatalf("data observations = %+v", report.Data)
	}
	rows := report.Data[1]
	if rows.Status != "ok" || rows.Kind != "table" || rows.Rows != 3 || !rows.More ||
		rows.Class != "read" || strings.Join(rows.Columns, ",") != "n" {
		t.Fatalf("rows observation = %+v", rows)
	}
	cancelled := report.Data[2]
	if cancelled.Status != "ok" || cancelled.Rows != 2 || !cancelled.More {
		t.Fatalf("cancelled fetch observation = %+v", cancelled)
	}
}

func TestMCPToolFixtureCoversResultsProgressAndErrors(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "mcp-tools"),
		Scenario: fakehost.Scenario{
			ToolCalls: []fakehost.ToolCall{
				{Name: "echo", Arguments: map[string]any{"text": "fake host"}},
				{Name: "write_note"},
				{Name: "echo_session"},
				{Name: "fail"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Tools) != 4 {
		t.Fatalf("tool observations = %+v", report.Tools)
	}
	echo := report.Tools[0]
	if echo.Tool != "plugin.acme.mcp-tools.echo" ||
		!strings.Contains(echo.Result, "echo: fake host") ||
		!strings.Contains(echo.Result, `"requestId":1`) {
		t.Fatalf("echo observation = %+v", echo)
	}
	if len(echo.Progress) != 1 || echo.Progress[0].Progress != 50 ||
		echo.Progress[0].Message != "halfway" {
		t.Fatalf("echo progress = %+v", echo.Progress)
	}
	if !strings.Contains(report.Tools[2].Result, "session ok") {
		t.Fatalf("echo_session observation = %+v", report.Tools[2])
	}
	if !strings.Contains(report.Tools[3].Result, "guest_failed") {
		t.Fatalf("fail observation = %+v", report.Tools[3])
	}
}

func TestStubServicesAnswerFromTheSharedDefinition(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory:    sample(t, "host-services"),
		Events:       []string{"selftest"},
		StubServices: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := stub.Definition(); err != nil {
		t.Fatalf("shared stub definition: %v", err)
	}
	var checked int
	for _, call := range report.Service {
		if call.Service == appplugin.ServiceUI {
			continue
		}
		want, err := stub.ResponseByID(call.Service, call.Method)
		if err != nil {
			t.Fatalf("stub has no response for %s/%d: %v", call.Service, call.Method, err)
		}
		if string(call.Response) != string(want) {
			t.Fatalf("service %s/%d answered %s, want shared stub %s",
				call.Service, call.Method, call.Response, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("selftest made no stubbed host-service calls")
	}
}

func TestProviderStreamOrderIsPreserved(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "loopback-provider"),
		Scenario: fakehost.Scenario{
			ProviderCalls: []fakehost.ProviderCall{
				{Method: uint32(domainpluginabi.ProviderMethodOpen),
					Payload: `{"target":"sample","hostId":"host-1"}`},
				{Method: uint32(domainpluginabi.ProviderMethodPump), Stream: 1,
					Input: []byte("first\n")},
				{Method: uint32(domainpluginabi.ProviderMethodPump), Stream: 1,
					Input: []byte("second\n")},
				{Method: uint32(domainpluginabi.ProviderMethodClose), Stream: 1,
					Payload: `{"stream":1}`},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantMethods := []string{"open", "pump", "pump", "close"}
	if len(report.Provider) != len(wantMethods) {
		t.Fatalf("provider observations = %+v", report.Provider)
	}
	for i, want := range wantMethods {
		if report.Provider[i].Method != want {
			t.Fatalf("observation %d method = %q, want %q", i, report.Provider[i].Method, want)
		}
	}
	if string(report.Provider[1].Output) != "first\n" ||
		string(report.Provider[2].Output) != "second\n" {
		t.Fatalf("stream output order = %q then %q",
			report.Provider[1].Output, report.Provider[2].Output)
	}
}

func TestUnknownFixtureMethodsAreRejected(t *testing.T) {
	provider, err := fakehost.Start(context.Background(), fakehost.Options{
		Directory: sample(t, "loopback-provider"),
	})
	if err != nil {
		t.Fatalf("Start provider sample: %v", err)
	}
	defer provider.Close()
	if _, err := provider.ProviderCall(context.Background(), fakehost.ProviderCall{
		Method: 99, Payload: `{}`,
	}); err == nil || !strings.Contains(err.Error(), "unknown provider method") {
		t.Fatalf("unknown provider method error = %v", err)
	}

	data, err := fakehost.Start(context.Background(), fakehost.Options{
		Directory: sample(t, "data-provider"),
	})
	if err != nil {
		t.Fatalf("Start data sample: %v", err)
	}
	defer data.Close()
	if _, err := data.DataCall(context.Background(), fakehost.DataCall{
		Method: 99, Payload: `{}`,
	}); err == nil || !strings.Contains(err.Error(), "unknown data provider method") {
		t.Fatalf("unknown data method error = %v", err)
	}
	if _, err := data.ToolCall(context.Background(), fakehost.ToolCall{Name: "missing"}); err == nil {
		t.Fatal("undeclared MCP tool was accepted")
	}
}

func TestUIEventOrderIsPreserved(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sample(t, "counter-view"),
		Scenario: fakehost.Scenario{
			UIEvents: []fakehost.UIEvent{
				{Node: 3, Kind: int32(4), Text: "first"},
				{Node: 3, Kind: int32(4), Text: "second"},
				{Node: 4, Kind: int32(0)},
			},
			Events: []string{"report"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.UIEvents) != 3 ||
		report.UIEvents[0].Text != "first" || report.UIEvents[1].Text != "second" {
		t.Fatalf("UI events = %+v", report.UIEvents)
	}
	if len(report.Events) != 1 || report.Events[0].Command != "plugin.acme.counter-view.report" {
		t.Fatalf("command events = %+v", report.Events)
	}
	output := strings.Join(report.Outputs, "\n")
	for _, want := range []string{"counter=1", "name=second"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLifecycleUnloadIsIdempotentAndClosesTheInstance(t *testing.T) {
	runner, err := fakehost.Start(context.Background(), fakehost.Options{
		Directory: sample(t, "hello-command"),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runner.Close()
	if err := runner.Unload(context.Background()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if err := runner.Unload(context.Background()); err != nil {
		t.Fatalf("second Unload: %v", err)
	}
	if err := runner.Dispatch(context.Background(), "greet"); !errors.Is(err, appplugin.ErrClosed) {
		t.Fatalf("Dispatch after unload = %v, want ErrClosed", err)
	}
	if err := runner.Poll(context.Background()); !errors.Is(err, appplugin.ErrClosed) {
		t.Fatalf("Poll after unload = %v, want ErrClosed", err)
	}
	if runner.UIEvent(fakehost.UIEvent{Node: 1, Kind: 0}) {
		t.Fatal("UIEvent after unload was queued")
	}
	if report := runner.Report(); report.PluginID != "acme.hello" {
		t.Fatalf("report after unload = %+v", report)
	}
}

func TestUnloadCancelsAnInFlightDataCall(t *testing.T) {
	runner, err := fakehost.Start(context.Background(), fakehost.Options{
		Directory: sample(t, "data-provider"),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runner.Close()
	done := make(chan error, 1)
	var observation fakehost.DataObservation
	go func() {
		var callErr error
		observation, callErr = runner.DataCall(context.Background(), fakehost.DataCall{
			Method:      uint32(domainpluginabi.DataProviderMethodFetch),
			Payload:     `{"query":2}`,
			CancelAfter: math.MaxInt,
		})
		done <- callErr
	}()
	waitInFlight(t, runner)
	if err := runner.Unload(context.Background()); err != nil {
		t.Fatalf("Unload during data call: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("in-flight data call: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight data call did not return after unload")
	}
	if observation.Status != "ok" || observation.Rows != 2 {
		t.Fatalf("cancelled fetch observation = %+v", observation)
	}
}

func TestUnloadDuringInFlightMCPToolIsBoundedAndRetryable(t *testing.T) {
	runner, err := fakehost.Start(context.Background(), fakehost.Options{
		Directory: sample(t, "mcp-tools"),
		Limits: appplugin.Limits{
			CallTimeout:   100 * time.Millisecond,
			WatchdogGrace: 200 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runner.Close()
	done := make(chan error, 1)
	go func() {
		_, callErr := runner.ToolCall(context.Background(), fakehost.ToolCall{Name: "slow"})
		done <- callErr
	}()
	waitInFlight(t, runner)
	unloadErr := runner.Unload(context.Background())
	if unloadErr != nil && !errors.Is(unloadErr, appplugin.ErrCallInFlight) {
		t.Fatalf("Unload during tool call = %v, want nil or ErrCallInFlight", unloadErr)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("slow tool call did not stop at the watchdog deadline")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := runner.Unload(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Unload did not succeed after the watchdog stopped the call: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
