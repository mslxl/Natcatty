package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type sampleProbe struct {
	mu       sync.Mutex
	commands []string
	gate     chan struct{}
	done     chan struct{}
}

func (p *sampleProbe) invoke(_ context.Context, command string) error {
	p.mu.Lock()
	p.commands = append(p.commands, command)
	gate, done := p.gate, p.done
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if done != nil {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	return nil
}

func (p *sampleProbe) setGate(gate chan struct{}) {
	p.mu.Lock()
	p.gate = gate
	p.mu.Unlock()
}

func (p *sampleProbe) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	commands := p.commands
	p.commands = nil
	return commands
}

func sampleClock(t time.Time) (*atomic.Int64, func() time.Time) {
	nanos := &atomic.Int64{}
	nanos.Store(t.UnixNano())
	return nanos, func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
}

func sampleRef() PluginRef {
	return PluginRef{
		ID: "acme.sample", Name: "Acme Sample", Version: "1.0.0",
		Source: domainplugin.SourceLocalFile,
	}
}

func sampleFiber(ref PluginRef, sections []domainplugin.PanelSection,
	invoke func(context.Context, string) error) PanelFiber {
	return PanelFiber{
		Ref:      ref,
		Manifest: domainplugin.Manifest{Contributions: domainplugin.Contributions{SystemPanel: sections}},
		Active:   true,
		Invoke:   invoke,
	}
}

func waitSampleIdle(t *testing.T, panel *SystemPanel, ref PluginRef) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		panel.mu.Lock()
		busy := panel.inflight[refKey(ref)]
		panel.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the panel refresh did not finish")
}

func waitSampleCommands(t *testing.T, probe *sampleProbe, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		probe.mu.Lock()
		count := len(probe.commands)
		probe.mu.Unlock()
		if count >= want {
			return probe.take()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the panel dispatched fewer than %d commands", want)
	return nil
}

func sampleSectionIDs(t *testing.T, commands []string) []string {
	t.Helper()
	ids := make([]string, 0, len(commands))
	for _, command := range commands {
		request, ok := domainplugin.ParsePanelRefreshCommand(command)
		if !ok {
			t.Fatalf("command %q is not a reserved refresh", command)
		}
		ids = append(ids, request.SectionID)
	}
	return ids
}

func TestSampleDueNeverPollsPushSectionsAndStopsWithTheTab(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	nanos, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	probe := &sampleProbe{}
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "pushed", Title: "Pushed", Refresh: domainplugin.PanelRefreshPush},
		}, probe.invoke)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	waitSampleIdle(t, panel, ref)
	if ids := sampleSectionIDs(t, waitSampleCommands(t, probe, 1)); len(ids) != 1 ||
		ids[0] != "pushed" {
		t.Fatalf("activation wake commands = %v, want one pushed refresh", ids)
	}
	probe.take()

	nanos.Add(int64(time.Hour))
	panel.sampleDue()
	if commands := probe.take(); len(commands) != 0 {
		t.Fatalf("a push section was polled: %v", commands)
	}

	panel.DeactivatePanel(domainplugin.PanelSurfaceManager)
	nanos.Add(int64(time.Hour))
	panel.sampleDue()
	if commands := probe.take(); len(commands) != 0 {
		t.Fatalf("an inactive tab sampled: %v", commands)
	}
}

func TestSampleDueEnforcesTheFastCadenceAndOneRefreshPerPlugin(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	nanos, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	probe := &sampleProbe{}
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "fast", Title: "Fast", Refresh: domainplugin.PanelRefreshFast},
		}, probe.invoke)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	waitSampleIdle(t, panel, ref)
	probe.take()

	nanos.Add(int64(domainplugin.PanelFastDefault - time.Millisecond))
	panel.sampleDue()
	if commands := probe.take(); len(commands) != 0 {
		t.Fatalf("a fast section sampled before its cadence: %v", commands)
	}
	nanos.Add(int64(time.Millisecond))
	panel.sampleDue()
	if ids := sampleSectionIDs(t, waitSampleCommands(t, probe, 1)); ids[0] != "fast" {
		t.Fatalf("fast sample = %v", ids)
	}

	// While the one allowed refresh is in flight, further ticks do not queue
	// another call for the same plugin.
	// The section failed twice already, so its next cadence is doubled.
	gate := make(chan struct{})
	probe.setGate(gate)
	nanos.Add(int64(2 * domainplugin.PanelFastDefault))
	panel.sampleDue()
	waitSampleCommands(t, probe, 1)
	nanos.Add(int64(time.Hour))
	panel.sampleDue()
	panel.sampleDue()
	if commands := probe.take(); len(commands) != 0 {
		t.Fatalf("%d refreshes queued behind an in-flight one", len(commands))
	}
	close(gate)
	probe.setGate(nil)
	waitSampleIdle(t, panel, ref)

	nanos.Add(int64(domainplugin.PanelFastDefault))
	panel.sampleDue()
	if ids := sampleSectionIDs(t, waitSampleCommands(t, probe, 1)); ids[0] != "fast" {
		t.Fatalf("fast sample after the gate = %v", ids)
	}
	waitSampleIdle(t, panel, ref)
}

func TestSideSurfaceSamplesOnlyCompactSections(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 45, 0, 0, time.UTC)
	nanos, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	probe := &sampleProbe{}
	sections := []domainplugin.PanelSection{
		{ID: "full", Title: "Full", Refresh: domainplugin.PanelRefreshFast},
		{ID: "compact", Title: "Compact", Refresh: domainplugin.PanelRefreshFast, Compact: true},
	}
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, sections, probe.invoke)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceSide, "alpha")
	waitSampleIdle(t, panel, ref)
	commands := waitSampleCommands(t, probe, 1)
	if ids := sampleSectionIDs(t, commands); len(ids) != 1 || ids[0] != "compact" {
		t.Fatalf("side activation refreshed %v, want only compact", ids)
	}
	probe.take()

	nanos.Add(int64(domainplugin.PanelFastDefault))
	panel.sampleDue()
	commands = waitSampleCommands(t, probe, 1)
	if ids := sampleSectionIDs(t, commands); len(ids) != 1 || ids[0] != "compact" {
		t.Fatalf("side sampling refreshed %v, want only compact", ids)
	}
	waitSampleIdle(t, panel, ref)
}

func TestSampleDueFollowsTheConfiguredSnapshotCadence(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		hostCadence time.Duration
		want        time.Duration
	}{
		// A snapshot section declared without an interval keeps its 3 s
		// default, but a 5 s Host collector cadence slows it down.
		{"slow host", 5 * time.Second, 5 * time.Second},
		// A 1 s Host cadence never speeds the section past its own default.
		{"fast host keeps the section default", time.Second, domainplugin.PanelSnapshotCeiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nanos, now := sampleClock(t0)
			panel := NewSystemPanel(PanelDeps{
				Now: now,
				Cadence: func(domainplugin.PanelCollector) time.Duration {
					return tc.hostCadence
				},
			})
			t.Cleanup(panel.Close)
			ref := sampleRef()
			probe := &sampleProbe{}
			panel.Bind(func() []PanelFiber {
				return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
					{ID: "snap", Title: "Snapshot", Refresh: domainplugin.PanelRefreshSnapshot},
				}, probe.invoke)}
			})
			panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
			waitSampleIdle(t, panel, ref)
			probe.take()

			nanos.Add(int64(tc.want - time.Millisecond))
			panel.sampleDue()
			if commands := probe.take(); len(commands) != 0 {
				t.Fatalf("a snapshot sampled before %s: %v", tc.want, commands)
			}
			nanos.Add(int64(time.Millisecond))
			panel.sampleDue()
			if ids := sampleSectionIDs(t, waitSampleCommands(t, probe, 1)); ids[0] != "snap" {
				t.Fatalf("snapshot sample = %v", ids)
			}
			waitSampleIdle(t, panel, ref)
		})
	}
}

func TestFastTableSectionsUseTheOneSecondDefault(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	nanos, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	probe := &sampleProbe{}
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "table", Title: "Table", Refresh: domainplugin.PanelRefreshFast},
		}, probe.invoke)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	waitSampleIdle(t, panel, ref)
	probe.take()

	key := panelSectionKey{plugin: refKey(ref), sectionID: "table", hostID: "alpha"}
	table := &domainplugin.PanelPayload{Items: []domainplugin.PanelDescriptor{{
		Kind: domainplugin.PanelDescriptorTable, Columns: []string{"name"},
		TableRows: []domainplugin.PanelTableRow{{Key: "web", Cells: []string{"web"}}},
	}}}
	panel.mu.Lock()
	state := panel.sections[key]
	state.payload = table
	state.state = domainplugin.PanelStateLive
	state.awaiting = false
	state.lastAttempt = t0
	panel.mu.Unlock()

	nanos.Add(int64(time.Second - time.Millisecond))
	panel.sampleDue()
	if commands := probe.take(); len(commands) != 0 {
		t.Fatalf("a table refreshed before 1 s: %v", commands)
	}
	nanos.Add(int64(time.Millisecond))
	panel.sampleDue()
	if ids := sampleSectionIDs(t, waitSampleCommands(t, probe, 1)); ids[0] != "table" {
		t.Fatalf("table sample = %v", ids)
	}
	waitSampleIdle(t, panel, ref)

	// Metrics keep the same 1 s default.
	metric := &domainplugin.PanelPayload{Items: []domainplugin.PanelDescriptor{{
		Kind: domainplugin.PanelDescriptorMetric, Label: "load", Value: "1",
	}}}
	panel.mu.Lock()
	state = panel.sections[key]
	state.payload = metric
	state.state = domainplugin.PanelStateLive
	state.awaiting = false
	state.failures = 0
	state.lastAttempt = now()
	panel.mu.Unlock()
	nanos.Add(int64(domainplugin.PanelFastDefault))
	panel.sampleDue()
	if ids := sampleSectionIDs(t, waitSampleCommands(t, probe, 1)); ids[0] != "table" {
		t.Fatalf("metric sample = %v", ids)
	}
	waitSampleIdle(t, panel, ref)
}

func TestPanelSectionDegradesOnlyItselfAndBacksOff(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	nanos, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "fast", Title: "Fast", Refresh: domainplugin.PanelRefreshFast},
		}, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")

	payload := &domainplugin.PanelPayload{Items: []domainplugin.PanelDescriptor{
		{Kind: domainplugin.PanelDescriptorMetric, Label: "load", Value: "1"},
	}}
	key := panelSectionKey{plugin: refKey(ref), sectionID: "fast", hostID: "alpha"}
	panel.mu.Lock()
	panel.sections[key] = &panelSectionState{
		state: domainplugin.PanelStateLive, payload: payload,
		failures: 2, lastAttempt: t0,
	}
	panel.mu.Unlock()

	probe := &sampleProbe{}
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "fast", Title: "Fast", Refresh: domainplugin.PanelRefreshFast},
		}, probe.invoke)}
	})
	nanos.Add(int64(2*domainplugin.PanelFastDefault - time.Millisecond))
	panel.sampleDue()
	if commands := probe.take(); len(commands) != 0 {
		t.Fatalf("the failed section did not back off: %v", commands)
	}
	nanos.Add(int64(time.Millisecond))
	panel.sampleDue()
	waitSampleCommands(t, probe, 1)
	waitSampleIdle(t, panel, ref)

	panel.mu.Lock()
	failed := panel.sections[key]
	state, detail, kept := failed.state, failed.detail, failed.payload
	failures := failed.failures
	panel.mu.Unlock()
	if state != domainplugin.PanelStateStale {
		t.Fatalf("state after a missing reply = %q, want stale", state)
	}
	if detail == "" || kept == nil || kept.Items[0].Value != "1" {
		t.Fatalf("the last good payload was lost: detail %q, payload %+v", detail, kept)
	}
	if failures != 3 {
		t.Fatalf("consecutive failures = %d, want 3", failures)
	}

	// A section that never produced a payload shows an error, not a stale
	// snapshot, and a dispatch error is preserved for the surface.
	empty := panelSectionKey{plugin: refKey(ref), sectionID: "fast", hostID: "other"}
	panel.mu.Lock()
	panel.sections[empty] = &panelSectionState{
		state: domainplugin.PanelStateLoading, awaiting: true,
	}
	panel.mu.Unlock()
	panel.finishSectionRefresh(empty, errors.New("guest trapped"))
	panel.mu.Lock()
	failed = panel.sections[empty]
	state, detail = failed.state, failed.detail
	panel.mu.Unlock()
	if state != domainplugin.PanelStateError || detail != "guest trapped" {
		t.Fatalf("failed section = %q, %q", state, detail)
	}
	if _, err := json.Marshal(kept); err != nil {
		t.Fatalf("retained payload: %v", err)
	}
}

func TestPanelUpdateRejectsUnknownHostsWithoutGrowingSections(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	_, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "pushed", Title: "Pushed", Refresh: domainplugin.PanelRefreshPush},
		}, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	payload := []byte(`{"items":[{"kind":"metric","label":"load","value":"1"}]}`)

	panel.mu.Lock()
	before := len(panel.sections)
	panel.mu.Unlock()
	for i := 0; i < 64; i++ {
		err := panel.Update(ref, fmt.Sprintf("ghost-%d", i), "pushed", payload)
		var service *ServiceError
		if !errors.As(err, &service) || service.Code != ServiceCodeConflict {
			t.Fatalf("unknown Host %d update = %v, want conflict", i, err)
		}
	}
	// A malformed payload for an unknown Host is rejected before decoding, so
	// it cannot create a state entry either.
	if err := panel.Update(ref, "ghost", "pushed", []byte("{")); err == nil {
		t.Fatal("a malformed payload for an unknown Host was accepted")
	}
	panel.mu.Lock()
	after := len(panel.sections)
	panel.mu.Unlock()
	if after != before {
		t.Fatalf("unknown Host IDs grew sections from %d to %d", before, after)
	}

	// The active Host still creates exactly one entry for its declared
	// section, and its payload is accepted.
	if err := panel.Update(ref, "alpha", "pushed", payload); err != nil {
		t.Fatalf("active Host update: %v", err)
	}
	panel.mu.Lock()
	after = len(panel.sections)
	panel.mu.Unlock()
	if after != before+1 {
		t.Fatalf("sections after one accepted update = %d, want %d", after, before+1)
	}
}

func TestPanelSectionMapIsBoundedPerPlugin(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 12, 30, 0, 0, time.UTC)
	_, now := sampleClock(t0)
	panel := NewSystemPanel(PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := sampleRef()
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "pushed", Title: "Pushed", Refresh: domainplugin.PanelRefreshPush},
		}, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")

	// Fill this plugin's budget with states for other Hosts, as a Host-switch
	// race or a compromised caller might.
	other := sampleRef()
	other.ID = "other.plugin"
	payload := []byte(`{"items":[{"kind":"metric","label":"load","value":"1"}]}`)
	panel.mu.Lock()
	panel.sections[panelSectionKey{plugin: refKey(other), sectionID: "pushed",
		hostID: "alpha"}] = &panelSectionState{state: domainplugin.PanelStateLoading}
	for i := 0; i < maxPanelSectionEntriesPerPlugin; i++ {
		panel.sections[panelSectionKey{plugin: refKey(ref), sectionID: "pushed",
			hostID: fmt.Sprintf("h%d", i)}] = &panelSectionState{
			state: domainplugin.PanelStateLoading,
		}
	}
	panel.mu.Unlock()

	err := panel.Update(ref, "alpha", "pushed", payload)
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != ServiceCodeLimit {
		t.Fatalf("over-budget update = %v, want limit", err)
	}
	panel.mu.Lock()
	_, created := panel.sections[panelSectionKey{plugin: refKey(ref),
		sectionID: "pushed", hostID: "alpha"}]
	total := len(panel.sections)
	panel.mu.Unlock()
	if created {
		t.Fatal("an over-budget update created a section entry")
	}
	if total != maxPanelSectionEntriesPerPlugin+1 {
		t.Fatalf("section map = %d entries, want %d", total, maxPanelSectionEntriesPerPlugin+1)
	}

	// Another plugin has its own budget and is unaffected.
	otherPayload := []byte(`{"items":[{"kind":"metric","label":"load","value":"2"}]}`)
	otherFiber := sampleFiber(other, []domainplugin.PanelSection{
		{ID: "pushed", Title: "Pushed", Refresh: domainplugin.PanelRefreshPush},
	}, nil)
	panel.Bind(func() []PanelFiber {
		return []PanelFiber{sampleFiber(ref, []domainplugin.PanelSection{
			{ID: "pushed", Title: "Pushed", Refresh: domainplugin.PanelRefreshPush},
		}, nil), otherFiber}
	})
	if err := panel.Update(other, "alpha", "pushed", otherPayload); err != nil {
		t.Fatalf("another plugin's update: %v", err)
	}
}
