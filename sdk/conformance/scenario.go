package conformance

import (
	"encoding/json"
	"fmt"
	"reflect"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
	"github.com/mslxl/natcatty/sdk/fakehost"
)

// DefaultScenario derives the behavioral scenario from the manifest. Every
// declared command is dispatched; provider, data, and MCP contributions get
// deterministic fixture calls; the two native UI reference samples get the
// events their tests use. A caller can replace the whole scenario.
func DefaultScenario(manifest domainplugin.Manifest) fakehost.Scenario {
	scenario := fakehost.Scenario{}
	for _, command := range manifest.Contributions.Commands {
		scenario.Events = append(scenario.Events, command.ID)
	}
	switch manifest.ID {
	case "acme.counter-view":
		scenario.UIEvents = []fakehost.UIEvent{
			{Node: 3, Kind: int32(4), Text: "Ada"}, // EventInput on the name field
			{Node: 4, Kind: int32(0), Index: 0},    // EventClick on the button
		}
	case "acme.webview-view":
		scenario.UIEvents = []fakehost.UIEvent{{
			Node: 0, Kind: int32(18), // EventWebview is session-wide
			Text: `{"type":"request","request":1,"method":"greet","payload":"fake host"}`,
		}}
	}
	if len(manifest.Contributions.Connections) > 0 {
		scenario.ProviderCalls = []fakehost.ProviderCall{
			{Method: uint32(domainpluginabi.ProviderMethodValidate),
				Payload: `{"target":"sample"}`},
			{Method: uint32(domainpluginabi.ProviderMethodOpen),
				Payload: `{"target":"sample","hostId":"host-1"}`},
			{Method: uint32(domainpluginabi.ProviderMethodPump), Stream: 1,
				Payload: `{"stream":1}`, Input: []byte("fake host\n")},
			{Method: uint32(domainpluginabi.ProviderMethodClose), Stream: 1,
				Payload: `{"stream":1}`},
		}
	}
	if len(manifest.Contributions.DataProviders) > 0 {
		scenario.DataCalls = []fakehost.DataCall{
			{Method: uint32(domainpluginabi.DataProviderMethodOpen), Payload: `{}`},
			{Method: uint32(domainpluginabi.DataProviderMethodQuery),
				Payload: `{"query":"rows"}`},
		}
	}
	for _, tool := range manifest.Contributions.MCPTools {
		if tool.Name == "slow" {
			// The sample intentionally blocks until the host cancels or the
			// instance is torn down; the conformance scenario is finite.
			continue
		}
		arguments := map[string]any{}
		if tool.Name == "echo" {
			arguments["text"] = "fake host"
		}
		scenario.ToolCalls = append(scenario.ToolCalls, fakehost.ToolCall{
			Name: tool.Name, Arguments: arguments,
		})
	}
	return scenario
}

// parityNotApplicable explains why the single-instance fake host cannot mirror
// an observation. Cross-plugin service calls need a second live plugin, which
// the fake host deliberately does not model; the real runtime still runs the
// scenario and the structural gate still covers the package.
func parityNotApplicable(manifest domainplugin.Manifest) string {
	if len(manifest.Provides) > 0 || len(manifest.Inject) > 0 {
		return "cross-plugin service peers are not simulated by the fake host"
	}
	return ""
}

type parityView struct {
	Logs     []fakehost.LogEntry
	Outputs  []string
	Events   []string
	UI       *fakehost.UISummary
	UIEvents []fakehost.UIEventRecord
	Provider []fakehost.ProviderObservation
	Data     []fakehost.DataObservation
	Tools    []toolParity
}

// toolParity omits the internal request id: the real ToolCaller port does not
// expose it, while the guest-visible result already proves it was echoed.
type toolParity struct {
	Tool     string
	Result   string
	Progress []fakehost.ToolProgress
	Error    string
}

func parityViewOf(report fakehost.Report) parityView {
	view := parityView{
		Logs:     report.Logs,
		Outputs:  report.Outputs,
		UI:       report.UI,
		UIEvents: report.UIEvents,
		Provider: report.Provider,
		Data:     report.Data,
	}
	for _, event := range report.Events {
		view.Events = append(view.Events, event.Command)
	}
	for _, tool := range report.Tools {
		view.Tools = append(view.Tools, toolParity{
			Tool: tool.Tool, Result: tool.Result,
			Progress: tool.Progress, Error: tool.Error,
		})
	}
	return view
}

// compareReports returns an empty string when the normalized fake-host and
// real-runtime observations match.
func compareReports(fake, real fakehost.Report) string {
	fakeView := parityViewOf(fake)
	realView := parityViewOf(real)
	if reflect.DeepEqual(fakeView, realView) {
		return ""
	}
	fakeJSON, _ := json.MarshalIndent(fakeView, "", "  ")
	realJSON, _ := json.MarshalIndent(realView, "", "  ")
	return fmt.Sprintf("\nfake host: %s\nreal runtime: %s", fakeJSON, realJSON)
}
