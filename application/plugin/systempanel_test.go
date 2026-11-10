package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func panelClock(t time.Time) (*atomic.Int64, func() time.Time) {
	nanos := &atomic.Int64{}
	nanos.Store(t.UnixNano())
	return nanos, func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
}

func escapePanelClock(nanos *atomic.Int64, d time.Duration) {
	nanos.Add(int64(d))
}

func panelRef() appplugin.PluginRef {
	return appplugin.PluginRef{
		ID: "acme.panel", Name: "Acme Panel", Version: "1.0.0",
		Source: domainplugin.SourceLocalFile,
	}
}

func panelFiber(ref appplugin.PluginRef, sections []domainplugin.PanelSection,
	invoke func(context.Context, string) error) appplugin.PanelFiber {
	return appplugin.PanelFiber{
		Ref:      ref,
		Manifest: domainplugin.Manifest{Contributions: domainplugin.Contributions{SystemPanel: sections}},
		Active:   true,
		Invoke:   invoke,
	}
}

func panelRaw(t *testing.T, items ...domainplugin.PanelDescriptor) []byte {
	t.Helper()
	raw, err := json.Marshal(domainplugin.PanelPayload{Items: items})
	if err != nil {
		t.Fatalf("marshal panel payload: %v", err)
	}
	return raw
}

type panelProbe struct {
	mu       sync.Mutex
	fn       func(context.Context, string) error
	commands []string
}

func (p *panelProbe) invoke(ctx context.Context, command string) error {
	p.mu.Lock()
	p.commands = append(p.commands, command)
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, command)
}

func (p *panelProbe) set(fn func(context.Context, string) error) {
	p.mu.Lock()
	p.fn = fn
	p.mu.Unlock()
}

func (p *panelProbe) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.commands...)
}

func waitPanelView(t *testing.T, panel *appplugin.SystemPanel,
	surface domainplugin.PanelSurface, hostID, sectionID string,
	check func(domainplugin.PanelSectionView) bool) domainplugin.PanelSectionView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, view := range panel.PanelSections(surface, hostID) {
			if view.Section.ID == sectionID && check(view) {
				return view
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("section %q never reached the expected state", sectionID)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPanelSectionsListOnlyLivePluginsForTheVisibleSurface(t *testing.T) {
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{})
	t.Cleanup(panel.Close)
	ref := panelRef()
	other := appplugin.PluginRef{
		ID: "zeta.panel", Name: "Zeta Panel", Version: "2.0.0",
		Source: domainplugin.SourceLocalFile,
	}
	inactive := appplugin.PluginRef{
		ID: "gone.panel", Name: "Gone Panel", Version: "1.0.0",
		Source: domainplugin.SourceLocalFile,
	}
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{
			panelFiber(ref, []domainplugin.PanelSection{
				{ID: "beta", Title: "Beta", Order: 10, Refresh: domainplugin.PanelRefreshPush},
				{ID: "alpha", Title: "Alpha", Order: 20, Refresh: domainplugin.PanelRefreshPush, Compact: true},
			}, nil),
			panelFiber(other, []domainplugin.PanelSection{
				{ID: "gamma", Title: "Gamma", Order: 5, Refresh: domainplugin.PanelRefreshPush, Compact: true},
			}, nil),
			{
				Ref: inactive,
				Manifest: domainplugin.Manifest{Contributions: domainplugin.Contributions{
					SystemPanel: []domainplugin.PanelSection{
						{ID: "hidden", Title: "Hidden", Refresh: domainplugin.PanelRefreshPush},
					},
				}},
				Active: false,
			},
		}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")

	manager := panel.PanelSections(domainplugin.PanelSurfaceManager, "alpha")
	got := make([]string, len(manager))
	for i, view := range manager {
		got[i] = view.Section.ID
	}
	if strings.Join(got, ",") != "gamma,beta,alpha" {
		t.Fatalf("manager section order = %v, want gamma,beta,alpha", got)
	}
	for _, view := range manager {
		if view.State != domainplugin.PanelStateLoading {
			t.Errorf("section %q initial state = %q, want loading", view.Section.ID, view.State)
		}
	}

	panel.ActivatePanel(domainplugin.PanelSurfaceSide, "alpha")
	side := panel.PanelSections(domainplugin.PanelSurfaceSide, "alpha")
	got = got[:0]
	for _, view := range side {
		got = append(got, view.Section.ID)
	}
	if strings.Join(got, ",") != "gamma,alpha" {
		t.Fatalf("side section order = %v, want only compact gamma,alpha", got)
	}

	if views := panel.PanelSections(domainplugin.PanelSurfaceManager, "beta"); len(views) != 0 {
		t.Fatalf("another Host saw %d manager sections", len(views))
	}
	if views := panel.PanelSections(domainplugin.PanelSurfaceSide, "beta"); len(views) != 0 {
		t.Fatalf("the side surface aggregated another Host: %d sections", len(views))
	}
	panel.DeactivatePanel(domainplugin.PanelSurfaceManager)
	if views := panel.PanelSections(domainplugin.PanelSurfaceManager, "alpha"); len(views) != 0 {
		t.Fatalf("an inactive surface kept %d sections", len(views))
	}
	if views := panel.PanelSections(domainplugin.PanelSurfaceSide, "alpha"); len(views) == 0 {
		t.Fatal("closing the manager closed the side surface")
	}
	panel.DeactivatePanel(domainplugin.PanelSurfaceSide)
	if views := panel.PanelSections(domainplugin.PanelSurfaceSide, "alpha"); len(views) != 0 {
		t.Fatalf("the closed side surface kept %d sections", len(views))
	}
}

func TestPanelPushUpdatesAreRateLimitedAndKeepTheLastGoodPayload(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	nanos, now := panelClock(t0)
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{Now: now})
	t.Cleanup(panel.Close)
	ref := panelRef()
	sections := []domainplugin.PanelSection{
		{ID: "live", Title: "Live", Refresh: domainplugin.PanelRefreshPush},
	}
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{panelFiber(ref, sections, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")

	first := panelRaw(t, domainplugin.PanelDescriptor{
		Kind: domainplugin.PanelDescriptorMetric, Label: "load", Value: "one",
	})
	if err := panel.Update(ref, "alpha", "live", first); err != nil {
		t.Fatalf("first push: %v", err)
	}
	second := panelRaw(t, domainplugin.PanelDescriptor{
		Kind: domainplugin.PanelDescriptorMetric, Label: "load", Value: "two",
	})
	if err := panel.Update(ref, "alpha", "live", second); err == nil {
		t.Fatal("a second push inside 100 ms was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("rate-limit code = %q, want limit", code)
	}

	oversize := panelRaw(t, domainplugin.PanelDescriptor{
		Kind: domainplugin.PanelDescriptorSection, Label: "big",
		Rows: []domainplugin.PanelRow{
			{Label: "a", Value: strings.Repeat("x", domainplugin.MaxPanelStringBytes-1)},
			{Label: "b", Value: strings.Repeat("x", domainplugin.MaxPanelStringBytes-1)},
		},
	})
	if len(oversize) <= domainplugin.MaxPanelPushBytes {
		t.Fatalf("fixture payload is only %d bytes", len(oversize))
	}
	if err := panel.Update(ref, "alpha", "live", oversize); err == nil {
		t.Fatal("an oversize push was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("oversize push code = %q, want invalid", code)
	}
	view := waitPanelView(t, panel, domainplugin.PanelSurfaceManager, "alpha", "live",
		func(view domainplugin.PanelSectionView) bool { return view.State == domainplugin.PanelStateLive })
	if view.Payload == nil || len(view.Payload.Items) != 1 || view.Payload.Items[0].Value != "one" {
		t.Fatalf("the rejected pushes replaced the last good payload: %+v", view.Payload)
	}
	if view.Rejected != 2 {
		t.Fatalf("rejected counter = %d, want 2", view.Rejected)
	}

	escapePanelClock(nanos, domainplugin.PanelPushFloor)
	if err := panel.Update(ref, "alpha", "live", second); err != nil {
		t.Fatalf("push after the floor: %v", err)
	}
	view = waitPanelView(t, panel, domainplugin.PanelSurfaceManager, "alpha", "live",
		func(view domainplugin.PanelSectionView) bool { return view.State == domainplugin.PanelStateLive })
	if view.Payload.Items[0].Value != "two" {
		t.Fatalf("accepted payload = %+v, want two", view.Payload.Items[0])
	}
	if !view.SampledAt.Equal(t0.Add(domainplugin.PanelPushFloor)) {
		t.Errorf("sampled at %v, want %v", view.SampledAt, t0.Add(domainplugin.PanelPushFloor))
	}

	if err := panel.Update(ref, "beta", "live", second); err == nil {
		t.Fatal("a push for an invisible Host was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeConflict {
		t.Fatalf("wrong-Host update code = %q, want conflict", code)
	}
	if err := panel.Update(ref, "alpha", "missing", second); err == nil {
		t.Fatal("an undeclared section accepted an update")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("undeclared section code = %q, want not-found", code)
	}
}

func TestPanelFastAndSnapshotUpdatesRequireARefresh(t *testing.T) {
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{})
	t.Cleanup(panel.Close)
	ref := panelRef()
	sections := []domainplugin.PanelSection{
		{ID: "health", Title: "Health", Refresh: domainplugin.PanelRefreshFast},
	}
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{panelFiber(ref, sections, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	payload := panelRaw(t, domainplugin.PanelDescriptor{
		Kind: domainplugin.PanelDescriptorMetric, Label: "load", Value: "1",
	})
	if err := panel.Update(ref, "alpha", "health", payload); err == nil {
		t.Fatal("an unsolicited fast update was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("unsolicited fast update code = %q, want invalid", code)
	}

	// A host refresh makes exactly one fast update acceptable.
	probe := &panelProbe{}
	replied := make(chan error, 1)
	probe.set(func(_ context.Context, command string) error {
		err := panel.Update(ref, "alpha", "health", payload)
		replied <- err
		return nil
	})
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{panelFiber(ref, sections, probe.invoke)}
	})
	panel.RefreshPanel(domainplugin.PanelSurfaceManager, "alpha")
	select {
	case err := <-replied:
		if err != nil {
			t.Fatalf("guest reply during refresh: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the host refresh was never dispatched")
	}
	view := waitPanelView(t, panel, domainplugin.PanelSurfaceManager, "alpha", "health",
		func(view domainplugin.PanelSectionView) bool { return view.State == domainplugin.PanelStateLive })
	if view.Payload == nil || view.Payload.Items[0].Value != "1" {
		t.Fatalf("fast payload = %+v", view.Payload)
	}
	commands := probe.recorded()
	if len(commands) != 1 {
		t.Fatalf("refresh dispatched %d commands, want 1", len(commands))
	}
	request, ok := domainplugin.ParsePanelRefreshCommand(commands[0])
	if !ok || request.HostID != "alpha" || request.SectionID != "health" ||
		request.Surface != domainplugin.PanelSurfaceManager {
		t.Fatalf("refresh command = %q (%+v, %v)", commands[0], request, ok)
	}
	// The accepted update must be consumed: a second unsolicited one is
	// rejected once the refresh completes.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := panel.Update(ref, "alpha", "health", payload); err != nil {
			if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
				t.Fatalf("post-refresh update code = %q, want invalid", code)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fast updates stayed accepted after the refresh")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPanelActionsAreScopedToDeclaredSectionsAndConfirmDestructiveOnes(t *testing.T) {
	audit := &recordingAudit{}
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{Audit: audit})
	t.Cleanup(panel.Close)
	ref := panelRef()
	sections := []domainplugin.PanelSection{{
		ID: "ops", Title: "Operations", Refresh: domainplugin.PanelRefreshPush,
		Actions: []domainplugin.PanelAction{
			{ID: "restart", Label: "Restart", Destructive: true},
			{ID: "copy", Label: "Copy"},
		},
	}}
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{panelFiber(ref, sections, nil)}
	})
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	payload := panelRaw(t, domainplugin.PanelDescriptor{
		Kind:    domainplugin.PanelDescriptorTable,
		Columns: []string{"Name"},
		TableRows: []domainplugin.PanelTableRow{
			{Key: "web", Cells: []string{"web"}},
		},
	})
	if err := panel.Update(ref, "alpha", "ops", payload); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	probe := &panelProbe{}
	panel.Bind(func() []appplugin.PanelFiber {
		return []appplugin.PanelFiber{panelFiber(ref, sections, probe.invoke)}
	})

	request := domainplugin.PanelActionRequest{
		Plugin:    domainplugin.PanelPlugin{ID: ref.ID, Name: ref.Name, Version: ref.Version, Source: ref.Source},
		HostID:    "alpha",
		Surface:   domainplugin.PanelSurfaceManager,
		SectionID: "ops",
		ActionID:  "restart",
		RowKey:    "web",
	}
	if err := panel.RunPanelAction(context.Background(), request); err == nil {
		t.Fatal("a destructive action ran without confirmation")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("unconfirmed action code = %q, want denied", code)
	}
	if len(probe.recorded()) != 0 {
		t.Fatal("an unconfirmed action reached the plugin")
	}

	request.Confirmed = true
	if err := panel.RunPanelAction(context.Background(), request); err != nil {
		t.Fatalf("confirmed action: %v", err)
	}
	commands := probe.recorded()
	if len(commands) != 1 {
		t.Fatalf("confirmed action dispatched %d commands, want 1", len(commands))
	}
	command, ok := domainplugin.ParsePanelActionCommand(commands[0])
	if !ok || command.SectionID != "ops" || command.ActionID != "restart" || command.RowKey != "web" {
		t.Fatalf("action command = %q (%+v, %v)", commands[0], command, ok)
	}

	request.RowKey = "missing"
	request.Confirmed = false
	request.ActionID = "copy"
	if err := panel.RunPanelAction(context.Background(), request); err == nil {
		t.Fatal("an action for an unknown row ran")
	}
	request.RowKey = "web"
	request.ActionID = "builtin-kill"
	if err := panel.RunPanelAction(context.Background(), request); err == nil {
		t.Fatal("an undeclared action ran")
	}
	builtin := request
	builtin.SectionID = "processes"
	builtin.ActionID = "kill"
	builtin.RowKey = "web"
	if err := panel.RunPanelAction(context.Background(), builtin); err == nil {
		t.Fatal("a built-in section accepted a plugin action")
	}
	if got := len(probe.recorded()); got != 1 {
		t.Fatalf("rejected actions reached the plugin: %d commands", got)
	}

	reasons := panelAuditReasons(audit)
	for _, want := range []string{
		"systempanel.action:ops:restart:denied-unconfirmed",
		"systempanel.action:ops:restart:request",
		"systempanel.action:ops:restart:ok",
		"systempanel.action:ops:copy:unknown-row",
		"systempanel.action:ops:builtin-kill:unknown-action",
		"systempanel.action:processes:kill:not-found",
	} {
		if !containsReason(reasons, want) {
			t.Errorf("audit lacks %q, reasons %v", want, reasons)
		}
	}
}

func panelAuditReasons(audit *recordingAudit) []string {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	reasons := make([]string, len(audit.events))
	for i, event := range audit.events {
		reasons[i] = event.Reason
	}
	return reasons
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

type panelSource struct {
	mu       sync.Mutex
	calls    int
	hosts    []string
	received [][]domainplugin.PanelCollector
	snapshot domainplugin.PanelSnapshot
	err      error
	// now, when set, stamps each successful collection the way the real
	// source does, so a test can prove sampled_at tracks the pass, not the
	// cached read.
	now func() time.Time
}

func (s *panelSource) CollectPanelSnapshot(_ context.Context, hostID string,
	collectors []domainplugin.PanelCollector) (domainplugin.PanelSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.hosts = append(s.hosts, hostID)
	s.received = append(s.received, append([]domainplugin.PanelCollector(nil), collectors...))
	if s.err != nil {
		return domainplugin.PanelSnapshot{}, s.err
	}
	sampledAt := s.snapshot.SampledAt
	if s.now != nil {
		sampledAt = s.now().UTC()
	}
	result := domainplugin.PanelSnapshot{
		HostID:     hostID,
		SampledAt:  sampledAt,
		Stale:      s.snapshot.Stale,
		Collectors: make(map[string]json.RawMessage, len(s.snapshot.Collectors)),
		Skipped:    make(map[string]string, len(s.snapshot.Skipped)),
	}
	for name, data := range s.snapshot.Collectors {
		result.Collectors[name] = append(json.RawMessage(nil), data...)
	}
	for name, reason := range s.snapshot.Skipped {
		result.Skipped[name] = reason
	}
	return result, nil
}

func (s *panelSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *panelSource) setTimeout(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func TestPanelSnapshotIsHostScopedCachedAndStaleWhenInactive(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	nanos, now := panelClock(t0)
	source := &panelSource{
		now: now,
		snapshot: domainplugin.PanelSnapshot{
			Collectors: map[string]json.RawMessage{
				"overview": json.RawMessage(`{"hostname":"alpha"}`),
			},
			Skipped: map[string]string{"gpu": "unavailable"},
		},
	}
	cadence := func(domainplugin.PanelCollector) time.Duration { return 2 * time.Second }
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{
		Source: source, Cadence: cadence, Now: now,
	})
	t.Cleanup(panel.Close)
	ctx := context.Background()

	inactive, err := panel.Snapshot(ctx, "alpha",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorOverview})
	if err != nil {
		t.Fatalf("inactive snapshot: %v", err)
	}
	if !inactive.Stale || inactive.HostID != "alpha" || source.callCount() != 0 {
		t.Fatalf("inactive snapshot = %+v, calls %d", inactive, source.callCount())
	}

	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	snapshot, err := panel.Snapshot(ctx, "alpha", []domainplugin.PanelCollector{
		domainplugin.PanelCollectorPorts, domainplugin.PanelCollectorOverview,
		domainplugin.PanelCollectorPorts, domainplugin.PanelCollectorGPU,
	})
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	if snapshot.Stale || string(snapshot.Collectors["overview"]) != `{"hostname":"alpha"}` {
		t.Fatalf("active snapshot = %+v", snapshot)
	}
	if snapshot.Skipped["gpu"] != "unavailable" {
		t.Fatalf("a skipped collector degraded the whole snapshot: %+v", snapshot)
	}
	if !snapshot.SampledAt.Equal(t0) {
		t.Fatalf("sampled_at = %v, want %v", snapshot.SampledAt, t0)
	}
	if source.callCount() != 1 {
		t.Fatalf("source calls = %d, want 1", source.callCount())
	}
	source.mu.Lock()
	gotCollectors := append([]domainplugin.PanelCollector(nil), source.received[0]...)
	source.mu.Unlock()
	if strings.Join(collectorNames(gotCollectors), ",") != "gpu,overview,ports" {
		t.Fatalf("collectors = %v, want deduplicated and sorted gpu,overview,ports", gotCollectors)
	}

	// A payload mutation cannot corrupt the cache.
	snapshot.Collectors["overview"] = json.RawMessage(`{"hostname":"mutated"}`)
	cached, err := panel.Snapshot(ctx, "alpha", []domainplugin.PanelCollector{
		domainplugin.PanelCollectorOverview, domainplugin.PanelCollectorPorts,
	})
	if err != nil {
		t.Fatalf("cached snapshot: %v", err)
	}
	if source.callCount() != 1 || string(cached.Collectors["overview"]) != `{"hostname":"alpha"}` {
		t.Fatalf("cache calls = %d, overview = %s", source.callCount(), cached.Collectors["overview"])
	}
	if !cached.Stale || !cached.SampledAt.Equal(t0) {
		t.Fatalf("a cadence-cached snapshot = stale %v, sampled_at %v", cached.Stale, cached.SampledAt)
	}

	// One millisecond before the configured interval the cache still answers.
	escapePanelClock(nanos, 2*time.Second-time.Millisecond)
	if cached, err = panel.Snapshot(ctx, "alpha", []domainplugin.PanelCollector{
		domainplugin.PanelCollectorOverview,
	}); err != nil {
		t.Fatalf("snapshot before the interval: %v", err)
	}
	if source.callCount() != 1 || !cached.SampledAt.Equal(t0) {
		t.Fatalf("before the interval: calls = %d, sampled_at = %v",
			source.callCount(), cached.SampledAt)
	}

	// Exactly at the interval there is one collection, not two.
	escapePanelClock(nanos, time.Millisecond)
	refreshed, err := panel.Snapshot(ctx, "alpha", []domainplugin.PanelCollector{
		domainplugin.PanelCollectorOverview, domainplugin.PanelCollectorPorts,
	})
	if err != nil {
		t.Fatalf("refreshed snapshot: %v", err)
	}
	if source.callCount() != 2 {
		t.Fatalf("source calls at the interval = %d, want 2", source.callCount())
	}
	if refreshed.Stale || !refreshed.SampledAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("refreshed snapshot = stale %v, sampled_at %v",
			refreshed.Stale, refreshed.SampledAt)
	}
	if _, err := panel.Snapshot(ctx, "alpha", []domainplugin.PanelCollector{
		domainplugin.PanelCollectorOverview,
	}); err != nil {
		t.Fatalf("repeat snapshot: %v", err)
	}
	if source.callCount() != 2 {
		t.Fatalf("a second pass ran at the same instant: %d calls", source.callCount())
	}

	source.setTimeout(errors.New("ssh timed out"))
	escapePanelClock(nanos, 2*time.Second)
	stale, err := panel.Snapshot(ctx, "alpha", []domainplugin.PanelCollector{
		domainplugin.PanelCollectorOverview, domainplugin.PanelCollectorPorts,
	})
	if err != nil {
		t.Fatalf("a failed collection discarded the cache: %v", err)
	}
	if !stale.Stale || string(stale.Collectors["overview"]) != `{"hostname":"alpha"}` {
		t.Fatalf("stale snapshot = %+v", stale)
	}

	// A Host with no surface and no cache never aggregates another Host's data.
	other, err := panel.Snapshot(ctx, "beta",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorOverview})
	if err != nil {
		t.Fatalf("other Host snapshot: %v", err)
	}
	if !other.Stale || len(other.Collectors) != 0 {
		t.Fatalf("beta snapshot leaked alpha data: %+v", other)
	}
	if _, err := panel.Snapshot(ctx, "alpha", nil); err == nil {
		t.Fatal("a snapshot with no collectors was accepted")
	}
	if _, err := panel.Snapshot(ctx, "alpha",
		[]domainplugin.PanelCollector{"nope"}); err == nil {
		t.Fatal("an unknown collector was accepted")
	}
}

func TestPanelSnapshotFollowsConfiguredCollectorIntervals(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		collector domainplugin.PanelCollector
		interval  time.Duration
	}{
		{domainplugin.PanelCollectorOverview, time.Second},
		{domainplugin.PanelCollectorProcesses, 2 * time.Second},
		{domainplugin.PanelCollectorServices, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(string(tc.collector), func(t *testing.T) {
			nanos, now := panelClock(t0)
			payload := json.RawMessage(`{"ok":true}`)
			source := &panelSource{now: now, snapshot: domainplugin.PanelSnapshot{
				Collectors: map[string]json.RawMessage{string(tc.collector): payload},
			}}
			// Only the tested collector is fast; the rest stay slow so the
			// Host-wide minimum is exactly the configured interval.
			cadence := func(collector domainplugin.PanelCollector) time.Duration {
				if collector == tc.collector {
					return tc.interval
				}
				return time.Minute
			}
			panel := appplugin.NewSystemPanel(appplugin.PanelDeps{
				Source: source, Cadence: cadence, Now: now,
			})
			t.Cleanup(panel.Close)
			panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
			ctx := context.Background()
			request := []domainplugin.PanelCollector{tc.collector}

			first, err := panel.Snapshot(ctx, "alpha", request)
			if err != nil {
				t.Fatalf("first snapshot: %v", err)
			}
			if source.callCount() != 1 || !first.SampledAt.Equal(t0) ||
				string(first.Collectors[string(tc.collector)]) != string(payload) {
				t.Fatalf("first snapshot = %+v, calls %d", first, source.callCount())
			}

			escapePanelClock(nanos, tc.interval-time.Millisecond)
			cached, err := panel.Snapshot(ctx, "alpha", request)
			if err != nil {
				t.Fatalf("cached snapshot: %v", err)
			}
			if source.callCount() != 1 {
				t.Fatalf("source called before the configured %s interval", tc.interval)
			}
			if !cached.Stale || !cached.SampledAt.Equal(t0) {
				t.Fatalf("cached snapshot = stale %v, sampled_at %v", cached.Stale, cached.SampledAt)
			}
			if string(cached.Collectors[string(tc.collector)]) != string(payload) {
				t.Fatalf("cached payload = %s, want %s",
					cached.Collectors[string(tc.collector)], payload)
			}

			escapePanelClock(nanos, time.Millisecond)
			fresh, err := panel.Snapshot(ctx, "alpha", request)
			if err != nil {
				t.Fatalf("snapshot at the interval: %v", err)
			}
			if source.callCount() != 2 {
				t.Fatalf("source calls at %s = %d, want exactly 2", tc.interval, source.callCount())
			}
			if fresh.Stale || !fresh.SampledAt.Equal(t0.Add(tc.interval)) {
				t.Fatalf("fresh snapshot = stale %v, sampled_at %v", fresh.Stale, fresh.SampledAt)
			}
			if _, err := panel.Snapshot(ctx, "alpha", request); err != nil {
				t.Fatalf("repeat snapshot: %v", err)
			}
			if source.callCount() != 2 {
				t.Fatalf("a second pass ran at the same instant: %d calls", source.callCount())
			}
		})
	}
}

func TestPanelSnapshotBoundsRotatingCollectorSets(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 12, 30, 0, 0, time.UTC)
	nanos, now := panelClock(t0)
	source := &panelSource{now: now, snapshot: domainplugin.PanelSnapshot{
		Collectors: map[string]json.RawMessage{},
	}}
	cadence := func(domainplugin.PanelCollector) time.Duration { return 3 * time.Second }
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{
		Source: source, Cadence: cadence, Now: now,
	})
	t.Cleanup(panel.Close)
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	ctx := context.Background()

	if _, err := panel.Snapshot(ctx, "alpha",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorOverview}); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if source.callCount() != 1 {
		t.Fatalf("first snapshot made %d source calls, want 1", source.callCount())
	}

	// Rotating through untouched collectors at the same instant must not
	// start another pass: the Host minimum, not the request, owns the rate.
	for _, collector := range []domainplugin.PanelCollector{
		domainplugin.PanelCollectorProcesses, domainplugin.PanelCollectorPorts,
		domainplugin.PanelCollectorServices, domainplugin.PanelCollectorTmux,
		domainplugin.PanelCollectorDocker, domainplugin.PanelCollectorGPU,
	} {
		result, err := panel.Snapshot(ctx, "alpha",
			[]domainplugin.PanelCollector{collector})
		if err != nil {
			t.Fatalf("rotated snapshot %s: %v", collector, err)
		}
		if !result.Stale {
			t.Errorf("rotated snapshot %s was marked fresh", collector)
		}
	}
	if _, err := panel.Snapshot(ctx, "alpha", domainplugin.PanelCollectors); err != nil {
		t.Fatalf("combined snapshot: %v", err)
	}
	if source.callCount() != 1 {
		t.Fatalf("rotating the requested collectors forced %d source calls, want 1",
			source.callCount())
	}

	escapePanelClock(nanos, 3*time.Second-time.Millisecond)
	if _, err := panel.Snapshot(ctx, "alpha",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorProcesses}); err != nil {
		t.Fatalf("snapshot before the minimum: %v", err)
	}
	if source.callCount() != 1 {
		t.Fatalf("a pass ran inside the Host minimum: %d calls", source.callCount())
	}
	escapePanelClock(nanos, time.Millisecond)
	if _, err := panel.Snapshot(ctx, "alpha",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorProcesses}); err != nil {
		t.Fatalf("snapshot at the minimum: %v", err)
	}
	if source.callCount() != 2 {
		t.Fatalf("source calls at the minimum = %d, want 2", source.callCount())
	}
	source.mu.Lock()
	gotCollectors := append([]domainplugin.PanelCollector(nil), source.received[1]...)
	source.mu.Unlock()
	if strings.Join(collectorNames(gotCollectors), ",") != "processes" {
		t.Fatalf("the pass collected %v, want only the due processes", gotCollectors)
	}
}

func collectorNames(collectors []domainplugin.PanelCollector) []string {
	names := make([]string, len(collectors))
	for i, collector := range collectors {
		names[i] = string(collector)
	}
	return names
}

func TestPanelWithoutASnapshotSourceFailsTyped(t *testing.T) {
	panel := appplugin.NewSystemPanel(appplugin.PanelDeps{})
	t.Cleanup(panel.Close)
	panel.ActivatePanel(domainplugin.PanelSurfaceManager, "alpha")
	if _, err := panel.Snapshot(context.Background(), "alpha",
		[]domainplugin.PanelCollector{domainplugin.PanelCollectorOverview}); err == nil {
		t.Fatal("a snapshot without a configured source succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeFault {
		t.Fatalf("missing source code = %q, want fault", code)
	}
}
