package sysmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func snapshotConnector(t *testing.T, probe, overview, processes, ports, services string) *fakeConnector {
	t.Helper()
	connector := newFakeConnector()
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch command {
		case probeCommand():
			return ExecResult{Stdout: probe}, nil
		case overviewCommand():
			return ExecResult{Stdout: overview}, nil
		case processesCommand():
			return ExecResult{Stdout: processes}, nil
		case portsCommand():
			return ExecResult{Stdout: ports}, nil
		case servicesCommand():
			return ExecResult{Stdout: services}, nil
		default:
			return ExecResult{}, fmt.Errorf("unexpected collector command %q", command)
		}
	}
	return connector
}

func fullSnapshotConnector(t *testing.T) *fakeConnector {
	t.Helper()
	return snapshotConnector(t, fakeProbeAll, fmt.Sprintf(fakeOverview, 100, 140),
		fakeProcesses, fakePorts, fakeServices)
}

func TestCollectSnapshotRunsOnlyTheFixedCollectorTemplates(t *testing.T) {
	connector := fullSnapshotConnector(t)
	now := time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC)
	service := NewService(connector, WithClock(func() time.Time { return now }))
	host := fakeHost("alpha")
	collectors := []Tab{TabOverview, TabProcesses, TabPorts, TabServices}

	result, err := service.CollectSnapshot(context.Background(), host, collectors)
	if err != nil {
		t.Fatalf("CollectSnapshot: %v", err)
	}
	if !result.SampledAt.Equal(now) {
		t.Errorf("sampled at %v, want %v", result.SampledAt, now)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("a healthy Host skipped %v", result.Skipped)
	}
	if len(result.Collectors) != len(collectors) {
		t.Fatalf("%d collectors returned, want %d: %v",
			len(result.Collectors), len(collectors), result.Collectors)
	}

	var overview Overview
	if err := json.Unmarshal(result.Collectors[string(TabOverview)], &overview); err != nil {
		t.Fatalf("overview payload: %v", err)
	}
	if overview.Hostname != "fake-host" || overview.OSName != "Debian 12" || overview.Cores != 4 {
		t.Errorf("overview = %+v", overview)
	}
	var processes []Process
	if err := json.Unmarshal(result.Collectors[string(TabProcesses)], &processes); err != nil {
		t.Fatalf("processes payload: %v", err)
	}
	if len(processes) != 2 {
		t.Errorf("processes = %d rows, want 2", len(processes))
	}
	var ports []Port
	if err := json.Unmarshal(result.Collectors[string(TabPorts)], &ports); err != nil {
		t.Fatalf("ports payload: %v", err)
	}
	if len(ports) != 1 || ports[0].Port != 22 {
		t.Errorf("ports = %+v", ports)
	}
	var units []Unit
	if err := json.Unmarshal(result.Collectors[string(TabServices)], &units); err != nil {
		t.Fatalf("services payload: %v", err)
	}
	if len(units) != 1 || units[0].Name != "nginx.service" {
		t.Errorf("services = %+v", units)
	}

	commands := connector.commands("alpha")
	want := []string{
		probeCommand(), overviewCommand(), processesCommand(), portsCommand(), servicesCommand(),
	}
	if len(commands) != len(want) {
		t.Fatalf("ran %d commands, want %d: %v", len(commands), len(want), commands)
	}
	for i, command := range want {
		if commands[i] != command {
			t.Errorf("command %d = %q, want the fixed template %q", i, commands[i], command)
		}
	}
	if connector.openCount("alpha") != 1 {
		t.Errorf("opened %d connections, want one lease", connector.openCount("alpha"))
	}
	for _, command := range commands {
		if command == commandFor(TabExtensions) && !strings.Contains(command, "uptime") {
			t.Errorf("the Extensions tab produced a command: %q", command)
		}
	}
}

func TestCollectSnapshotDeduplicatesAndDegradesOneCollector(t *testing.T) {
	connector := fullSnapshotConnector(t)
	service := NewService(connector)
	host := fakeHost("alpha")

	result, err := service.CollectSnapshot(context.Background(), host,
		[]Tab{TabOverview, TabOverview, TabPorts})
	if err != nil {
		t.Fatalf("CollectSnapshot: %v", err)
	}
	overviewCommands := 0
	for _, command := range connector.commands("alpha") {
		if command == overviewCommand() {
			overviewCommands++
		}
	}
	if overviewCommands != 1 {
		t.Fatalf("overview ran %d times for a duplicated collector", overviewCommands)
	}
	if len(result.Collectors) != 2 {
		t.Fatalf("collectors = %v", result.Collectors)
	}

	// One failed collector is named with its reason and the others survive.
	connector.exec = func(_ domainvault.Host, command string) (ExecResult, error) {
		switch command {
		case probeCommand():
			return ExecResult{Stdout: fakeProbeAll}, nil
		case overviewCommand():
			return ExecResult{Stdout: fmt.Sprintf(fakeOverview, 100, 140)}, nil
		case portsCommand():
			return ExecResult{}, errors.New("connection reset")
		case processesCommand():
			return ExecResult{Stdout: markerUnavailable + "=processes\n"}, nil
		}
		return ExecResult{}, fmt.Errorf("unexpected command %q", command)
	}
	result, err = service.CollectSnapshot(context.Background(), host,
		[]Tab{TabOverview, TabPorts, TabProcesses})
	if err != nil {
		t.Fatalf("degraded CollectSnapshot: %v", err)
	}
	if _, ok := result.Collectors[string(TabOverview)]; !ok {
		t.Fatal("a failed collector discarded the healthy overview")
	}
	if len(result.Collectors) != 1 {
		t.Fatalf("collectors = %v, want only overview", result.Collectors)
	}
	if result.Skipped[string(TabPorts)] == "" {
		t.Error("the failed ports collector has no skip reason")
	}
	if result.Skipped[string(TabProcesses)] != string(ReasonMissingProcesses) {
		t.Errorf("unavailable processes skip = %q, want missing_processes",
			result.Skipped[string(TabProcesses)])
	}
}

func TestCollectSnapshotSkipsUnavailableCollectorsWithoutRunningThem(t *testing.T) {
	connector := snapshotConnector(t, "__NC_OS=Linux\n", fmt.Sprintf(fakeOverview, 100, 140),
		fakeProcesses, fakePorts, fakeServices)
	service := NewService(connector)
	result, err := service.CollectSnapshot(context.Background(), fakeHost("alpha"),
		[]Tab{TabOverview, TabServices, TabTmux})
	if err != nil {
		t.Fatalf("CollectSnapshot: %v", err)
	}
	if result.Skipped[string(TabServices)] != string(ReasonMissingSystemctl) {
		t.Errorf("services skip = %q, want missing_systemctl",
			result.Skipped[string(TabServices)])
	}
	if result.Skipped[string(TabTmux)] != string(ReasonMissingTmux) {
		t.Errorf("tmux skip = %q, want missing_tmux", result.Skipped[string(TabTmux)])
	}
	if _, ok := result.Collectors[string(TabOverview)]; !ok {
		t.Error("the available overview was skipped")
	}
	for _, command := range connector.commands("alpha") {
		if command == servicesCommand() || command == tmuxSessionsCommand() {
			t.Errorf("an unavailable collector ran %q", command)
		}
	}
}

func TestCollectSnapshotRejectsUnknownAndEmptyCollectors(t *testing.T) {
	connector := fullSnapshotConnector(t)
	service := NewService(connector)
	host := fakeHost("alpha")
	for _, collectors := range [][]Tab{{}, {TabExtensions}, {Tab("nope")}} {
		if _, err := service.CollectSnapshot(context.Background(), host, collectors); err == nil {
			t.Fatalf("collectors %v were accepted", collectors)
		}
	}
	if commands := connector.commands("alpha"); len(commands) != 0 {
		t.Fatalf("a rejected request ran commands: %v", commands)
	}
	if _, err := service.CollectSnapshot(context.Background(), domainvault.Host{}, []Tab{TabOverview}); err == nil {
		t.Fatal("a snapshot without a Host succeeded")
	}
	if _, err := (*Service)(nil).CollectSnapshot(context.Background(), host, []Tab{TabOverview}); !errors.Is(err, ErrNoConnector) {
		t.Fatalf("nil service error = %v", err)
	}
}

func TestCollectSnapshotNormalizesUnreportedMetricsForJSON(t *testing.T) {
	const gpu = "__NC_NVIDIA__\n" +
		"0, GPU-abc, NVIDIA A100, [N/A], [N/A], 40960, [N/A], [N/A], 300, [N/A], 535.10\n" +
		"__NC_GPU_END__\n"
	connector := snapshotConnector(t, fakeProbeAll, fmt.Sprintf(fakeOverview, 100, 140),
		fakeProcesses, fakePorts, fakeServices)
	previous := connector.exec
	connector.exec = func(host domainvault.Host, command string) (ExecResult, error) {
		if command == gpuCommand() {
			return ExecResult{Stdout: gpu}, nil
		}
		return previous(host, command)
	}
	service := NewService(connector)
	result, err := service.CollectSnapshot(context.Background(), fakeHost("alpha"),
		[]Tab{TabGPU, TabOverview})
	if err != nil {
		t.Fatalf("CollectSnapshot: %v", err)
	}
	if reason := result.Skipped[string(TabGPU)]; reason != "" {
		t.Fatalf("the GPU collector was skipped: %s", reason)
	}
	var info GPUInfo
	if err := json.Unmarshal(result.Collectors[string(TabGPU)], &info); err != nil {
		t.Fatalf("GPU payload is not JSON: %v", err)
	}
	if len(info.Devices) != 1 {
		t.Fatalf("GPU devices = %+v", info.Devices)
	}
	device := info.Devices[0]
	if device.Name != "NVIDIA A100" || device.MemoryTotalMB != 40960 ||
		device.PowerLimitW != 300 {
		t.Fatalf("reported GPU fields = %+v", device)
	}
	if device.UtilizationPercent != 0 || device.MemoryUsedMB != 0 ||
		device.TemperatureC != 0 || device.PowerDrawW != 0 || device.FanPercent != 0 {
		t.Fatalf("unreported GPU metrics were not normalized: %+v", device)
	}
}

func TestExtensionsIsNotARemoteTabOrCollector(t *testing.T) {
	if IsSnapshotCollector(TabExtensions) {
		t.Fatal("Extensions was listed as a built-in collector")
	}
	for _, tab := range SnapshotCollectors {
		if tab == TabExtensions {
			t.Fatal("SnapshotCollectors contains Extensions")
		}
	}
	if got := ExtendedTabs[len(ExtendedTabs)-1]; got != TabExtensions {
		t.Fatalf("last extended tab = %q, want extensions", got)
	}
	if len(ExtendedTabs) != len(SnapshotCollectors)+1 {
		t.Fatalf("ExtendedTabs = %v, SnapshotCollectors = %v", ExtendedTabs, SnapshotCollectors)
	}

	service := NewService(newFakeConnector())
	if err := service.Start(fakeHost("alpha"), TabExtensions, time.Second); !errors.Is(err, ErrPanelTab) {
		t.Fatalf("Start(extensions) = %v, want ErrPanelTab", err)
	}
	t.Cleanup(service.Close)
	if err := service.Start(fakeHost("alpha"), TabOverview, time.Second); err != nil {
		t.Fatalf("Start(overview) = %v", err)
	}
}
