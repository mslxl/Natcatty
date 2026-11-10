package sysmanager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/ui/locales"
)

type extensionsCall struct {
	surface domainplugin.PanelSurface
	hostID  string
}

type fakeExtensions struct {
	mu            sync.Mutex
	sections      []domainplugin.PanelSectionView
	sectionCalls  []extensionsCall
	activations   []extensionsCall
	deactivations []domainplugin.PanelSurface
	refreshes     []extensionsCall
	requests      []domainplugin.PanelActionRequest
	requestCh     chan domainplugin.PanelActionRequest
	listeners     []func()
	err           error
}

func newFakeExtensions(sections ...domainplugin.PanelSectionView) *fakeExtensions {
	return &fakeExtensions{
		sections:  sections,
		requestCh: make(chan domainplugin.PanelActionRequest, 8),
	}
}

func (f *fakeExtensions) PanelSections(surface domainplugin.PanelSurface, hostID string) []domainplugin.PanelSectionView {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sectionCalls = append(f.sectionCalls, extensionsCall{surface, hostID})
	return append([]domainplugin.PanelSectionView(nil), f.sections...)
}

func (f *fakeExtensions) ActivatePanel(surface domainplugin.PanelSurface, hostID string) {
	f.mu.Lock()
	f.activations = append(f.activations, extensionsCall{surface, hostID})
	f.mu.Unlock()
}

func (f *fakeExtensions) DeactivatePanel(surface domainplugin.PanelSurface) {
	f.mu.Lock()
	f.deactivations = append(f.deactivations, surface)
	f.mu.Unlock()
}

func (f *fakeExtensions) RefreshPanel(surface domainplugin.PanelSurface, hostID string) {
	f.mu.Lock()
	f.refreshes = append(f.refreshes, extensionsCall{surface, hostID})
	f.mu.Unlock()
}

func (f *fakeExtensions) RunPanelAction(_ context.Context, request domainplugin.PanelActionRequest) error {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	err := f.err
	f.mu.Unlock()
	f.requestCh <- request
	return err
}

func (f *fakeExtensions) Subscribe(fn func()) func() {
	f.mu.Lock()
	f.listeners = append(f.listeners, fn)
	f.mu.Unlock()
	return func() {}
}

func (f *fakeExtensions) lastActivation() (extensionsCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.activations) == 0 {
		return extensionsCall{}, false
	}
	return f.activations[len(f.activations)-1], true
}

func (f *fakeExtensions) deactivationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deactivations)
}

func (f *fakeExtensions) recordedRequests() []domainplugin.PanelActionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domainplugin.PanelActionRequest(nil), f.requests...)
}

func newExtensionsState(service *fakeService, prefs *fakePreferences,
	extensions *fakeExtensions) *State {
	return NewState(service, fakeHosts{hosts: testHosts()}, prefs, WithExtensions(extensions))
}

func waitPanelRequest(t *testing.T, fake *fakeExtensions) domainplugin.PanelActionRequest {
	t.Helper()
	select {
	case request := <-fake.requestCh:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("the panel action was never dispatched")
		return domainplugin.PanelActionRequest{}
	}
}

func waitPanelActionDone(t *testing.T, state *State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.Flush()
		if state.ActionDone() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the panel action did not finish")
}

func TestExtensionsTabIsCanonicalLastAndObeysLayoutPreferences(t *testing.T) {
	service := newFakeService()
	prefs := newFakePreferences()
	extensions := newFakeExtensions()
	state := newExtensionsState(service, prefs, extensions)

	tabs := state.Tabs()
	if len(tabs) != 8 || tabs[len(tabs)-1] != appsysmanager.TabExtensions {
		t.Fatalf("default tabs = %v, want Extensions last of eight", tabs)
	}
	if all := state.AllTabs(); all[len(all)-1] != appsysmanager.TabExtensions {
		t.Fatalf("AllTabs = %v", all)
	}
	if string(appsysmanager.TabExtensions) != "extensions" {
		t.Fatalf("canonical Extensions id = %q", appsysmanager.TabExtensions)
	}

	state.Activate()
	stopsBefore := service.stops
	state.SetTab(appsysmanager.TabExtensions)
	if state.Tab() != appsysmanager.TabExtensions {
		t.Fatalf("active tab = %q", state.Tab())
	}
	if activation, ok := extensions.lastActivation(); !ok ||
		activation.surface != domainplugin.PanelSurfaceManager || activation.hostID != "alpha" {
		t.Fatalf("manager activation = %+v, %v", activation, ok)
	}
	if service.stops <= stopsBefore {
		t.Fatal("selecting Extensions did not stop the built-in sampler")
	}

	if !state.MoveTab(-1) {
		t.Fatal("the Extensions tab could not be moved")
	}
	tabs = state.Tabs()
	if index := tabIndex(tabs, appsysmanager.TabExtensions); index != len(tabs)-2 {
		t.Fatalf("moved Extensions to index %d of %v", index, tabs)
	}
	if prefs.Settings().SysManager.TabOrder[len(tabs)-2] != "extensions" {
		t.Fatalf("persisted order = %v", prefs.Settings().SysManager.TabOrder)
	}

	deactivationsBefore := extensions.deactivationCount()
	if err := state.SetTabVisible(appsysmanager.TabExtensions, false); err != nil {
		t.Fatalf("hide Extensions: %v", err)
	}
	if state.TabVisible(appsysmanager.TabExtensions) || tabIndex(state.Tabs(), appsysmanager.TabExtensions) >= 0 {
		t.Fatalf("hidden Extensions stayed in the strip: %v", state.Tabs())
	}
	if state.Tab() == appsysmanager.TabExtensions {
		t.Fatal("hiding the active Extensions tab left it active")
	}
	if got := extensions.deactivationCount(); got <= deactivationsBefore {
		t.Fatalf("hiding Extensions did not stop sampling: %d deactivations", got)
	}
	if prefs.Settings().SysManager.HiddenTabs[0] != "extensions" {
		t.Fatalf("persisted hidden tabs = %v", prefs.Settings().SysManager.HiddenTabs)
	}

	if err := state.SetTabVisible(appsysmanager.TabExtensions, true); err != nil {
		t.Fatalf("show Extensions: %v", err)
	}
	tabs = state.Tabs()
	if index := tabIndex(tabs, appsysmanager.TabExtensions); index != len(tabs)-2 {
		t.Fatalf("shown Extensions returned at %d of %v", index, tabs)
	}
	state.SetTab(appsysmanager.TabExtensions)
	if activation, _ := extensions.lastActivation(); activation.hostID != "alpha" {
		t.Fatalf("reactivated Extensions = %+v", activation)
	}
	state.Close()
}

func tabIndex(tabs []appsysmanager.Tab, want appsysmanager.Tab) int {
	for index, tab := range tabs {
		if tab == want {
			return index
		}
	}
	return -1
}

func TestSideExtensionsTargetsTheCapturedHostOnly(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	extensions := newFakeExtensions()
	state := newExtensionsState(service, newFakePreferences(), extensions)

	state.ShowHost("beta")
	state.SetTab(appsysmanager.TabExtensions)
	activation, ok := extensions.lastActivation()
	if !ok || activation.surface != domainplugin.PanelSurfaceSide || activation.hostID != "beta" {
		t.Fatalf("side activation = %+v, %v", activation, ok)
	}
	state.ShowHost("alpha")
	if activation, _ := extensions.lastActivation(); activation.hostID != "beta" {
		t.Fatalf("the open side tool retargeted to %q", activation.hostID)
	}

	state.PanelSections()
	extensions.mu.Lock()
	call := extensions.sectionCalls[len(extensions.sectionCalls)-1]
	extensions.mu.Unlock()
	if call.surface != domainplugin.PanelSurfaceSide || call.hostID != "beta" {
		t.Fatalf("side sections asked for %+v", call)
	}

	state.Hide()
	if extensions.deactivationCount() == 0 {
		t.Fatal("hiding the side tool did not stop sampling")
	}
	state.ShowHost("alpha")
	if activation, _ := extensions.lastActivation(); activation.surface != domainplugin.PanelSurfaceSide ||
		activation.hostID != "alpha" {
		t.Fatalf("reopened side tool = %+v", activation)
	}
	state.Close()
}

func demoPanelView() domainplugin.PanelSectionView {
	enabled := true
	return domainplugin.PanelSectionView{
		Plugin: domainplugin.PanelPlugin{
			ID: "acme.panel", Name: "Acme Panel", Version: "1.0.0",
			Source: domainplugin.SourceLocalFile,
		},
		Section: domainplugin.PanelSection{
			ID: "ops", Title: "Operations", Refresh: domainplugin.PanelRefreshPush,
			Actions: []domainplugin.PanelAction{
				{ID: "restart", Label: "Restart plugin", Destructive: true},
				{ID: "copy", Label: "Copy plugin"},
				{ID: "off", Label: "Disabled plugin"},
			},
		},
		State: domainplugin.PanelStateLive,
		Payload: &domainplugin.PanelPayload{Items: []domainplugin.PanelDescriptor{
			{Kind: domainplugin.PanelDescriptorMetric, Label: "Load", Value: "0.42", Unit: "%"},
			{Kind: domainplugin.PanelDescriptorSection, Label: "Details",
				Rows: []domainplugin.PanelRow{{Label: "Kernel", Value: "6.1"}}},
			{Kind: domainplugin.PanelDescriptorTable,
				Columns: []string{"Name", "State", "CPU", "Memory"},
				TableRows: []domainplugin.PanelTableRow{
					{Key: "web", Cells: []string{"web", "up", "5%", "120 MB"}},
				}},
			{Kind: domainplugin.PanelDescriptorStatus, Label: "Daemon", Detail: "running",
				Level: domainplugin.PanelLevelOK},
			{Kind: domainplugin.PanelDescriptorAction, Action: "restart", RowKey: "web",
				Enabled: &enabled},
			{Kind: domainplugin.PanelDescriptorAction, Action: "copy", RowKey: "web"},
			{Kind: domainplugin.PanelDescriptorTimeseries, Label: "RPS",
				Points: []float64{1, 2, 3}, Scale: 5},
		}},
	}
}

func TestExtensionsContentRendersTheClosedDescriptorSet(t *testing.T) {
	m := locales.In("en")
	service := newFakeService()
	extensions := newFakeExtensions(demoPanelView())
	state := newExtensionsState(service, newFakePreferences(), extensions)
	state.Activate()
	state.SetTab(appsysmanager.TabExtensions)
	tt := ui.NewTester(View(state), 900, 700)
	tt.SetLocale("en")
	tt.Frame()

	for _, want := range []string{
		"Operations", "Load", "0.42 %", "Details", "Kernel", "6.1",
		"Name", "State", "CPU", "Memory", "web", "up", "5%", "120 MB",
		"Daemon", "running", "Restart plugin", "Copy plugin", "RPS", "3",
		m.SysManagerExtensionsByPlugin("Acme Panel"),
		m.SysManagerStatusLive(),
	} {
		if !tt.HasText(want) {
			t.Errorf("the Extensions tab lacks %q, texts %q", want, tt.Texts())
		}
	}
	if tt.Click("Disabled plugin") == nil {
		t.Fatal("a disabled panel action was clickable")
	}
	if len(extensions.recordedRequests()) != 0 {
		t.Fatal("clicking a disabled action dispatched it")
	}
	state.Close()
}

func TestExtensionsTablesTruncateOnlyInTheSideSurface(t *testing.T) {
	managerService := newFakeService()
	manager := newExtensionsState(managerService, newFakePreferences(),
		newFakeExtensions(demoPanelView()))
	manager.Activate()
	manager.SetTab(appsysmanager.TabExtensions)
	full := ui.NewTester(View(manager), 900, 700)
	full.SetLocale("en")
	full.Frame()
	if !full.HasText("Memory") || !full.HasText("120 MB") {
		t.Fatalf("the manager truncated a four-column table: %q", full.Texts())
	}
	manager.Close()

	sideService := newFakeService()
	sideService.snapshot = demoSnapshot()
	side := newExtensionsState(sideService, newFakePreferences(),
		newFakeExtensions(demoPanelView()))
	side.ShowHost("alpha")
	side.SetTab(appsysmanager.TabExtensions)
	compact := ui.NewTester(SideView(side), 280, 600)
	compact.SetLocale("en")
	compact.Frame()
	if !compact.HasText("Name") || !compact.HasText("State") || !compact.HasText("CPU") {
		t.Fatalf("the side table lost a held column: %q", compact.Texts())
	}
	if compact.HasText("Memory") || compact.HasText("120 MB") {
		t.Fatalf("the side table kept a column the panel cannot hold: %q", compact.Texts())
	}
	if !compact.HasText("Load") || !compact.HasText("Restart plugin") {
		t.Fatalf("the side Extensions tab lost its descriptors: %q", compact.Texts())
	}
	side.Close()
}

func TestExtensionsDestructiveActionConfirmsWithActionPluginAndTarget(t *testing.T) {
	m := locales.In("en")
	service := newFakeService()
	extensions := newFakeExtensions(demoPanelView())
	state := newExtensionsState(service, newFakePreferences(), extensions)
	state.Activate()
	state.SetTab(appsysmanager.TabExtensions)
	tt := ui.NewTester(View(state), 900, 700)
	tt.SetLocale("en")
	tt.Frame()

	if err := tt.Click("Restart plugin"); err != nil {
		t.Fatalf("click Restart plugin: %v", err)
	}
	tt.Frame()
	pending, ok := state.Pending()
	if !ok || pending.Kind != appsysmanager.ActionPanel || pending.Panel == nil {
		t.Fatalf("pending = %+v, %v", pending, ok)
	}
	if pending.Panel.ActionID != "restart" || pending.Panel.RowKey != "web" ||
		pending.Panel.Confirmed {
		t.Fatalf("pending panel request = %+v", pending.Panel)
	}
	if !strings.Contains(pending.PanelTarget, "Alpha") ||
		!strings.Contains(pending.PanelTarget, "Operations") ||
		!strings.Contains(pending.PanelTarget, "web") {
		t.Fatalf("confirmation target = %q", pending.PanelTarget)
	}
	message := m.SysManagerConfirmPanelAction("Restart plugin", "Acme Panel", pending.PanelTarget)
	if !tt.HasText(message) {
		t.Fatalf("the confirmation lacks %q, texts %q", message, tt.Texts())
	}
	if len(extensions.recordedRequests()) != 0 {
		t.Fatal("a destructive action ran before confirmation")
	}

	state.ConfirmPending()
	request := waitPanelRequest(t, extensions)
	if !request.Confirmed || request.ActionID != "restart" || request.RowKey != "web" ||
		request.HostID != "alpha" || request.Surface != domainplugin.PanelSurfaceManager ||
		request.Plugin.Name != "Acme Panel" {
		t.Fatalf("confirmed request = %+v", request)
	}
	waitPanelActionDone(t, state)
	tt.Frame()
	if !tt.HasText(m.SysManagerPanelActionSucceeded()) {
		t.Fatalf("no success notice, texts %q", tt.Texts())
	}

	// A non-destructive action runs immediately without a confirmation.
	if err := tt.Click("Copy plugin"); err != nil {
		t.Fatalf("click Copy plugin: %v", err)
	}
	request = waitPanelRequest(t, extensions)
	if request.ActionID != "copy" || request.RowKey != "web" || !request.Confirmed {
		t.Fatalf("immediate request = %+v", request)
	}
	if _, pending := state.Pending(); pending {
		t.Fatal("a non-destructive action opened a confirmation")
	}
	waitPanelActionDone(t, state)
	state.Close()
}

func TestExtensionActionKeysDistinguishPluginCopies(t *testing.T) {
	installed := domainplugin.PanelPlugin{
		ID: "acme.panel", Name: "Acme Panel", Source: domainplugin.SourceLocalFile,
	}
	development := installed
	development.Development = true
	registry := installed
	registry.Source = domainplugin.SourceRegistry

	cases := map[string]domainplugin.PanelPlugin{
		"installed":   installed,
		"development": development,
		"registry":    registry,
	}
	seen := make(map[string]string, len(cases))
	for name, plugin := range cases {
		key := extensionActionKey(plugin, "ops", "restart", "web")
		if other, ok := seen[key]; ok {
			t.Fatalf("%s and %s copies share the action key %q", name, other, key)
		}
		seen[key] = name
	}
}

func TestExtensionsNeverRenderActionsOnBuiltInRows(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	extensions := newFakeExtensions(demoPanelView())
	state := newExtensionsState(service, newFakePreferences(), extensions)
	state.Activate()
	state.SetTab(appsysmanager.TabProcesses)
	tt := ui.NewTester(View(state), 900, 700)
	tt.SetLocale("en")
	tt.Frame()
	if tt.HasText("Restart plugin") || tt.HasText("Copy plugin") {
		t.Fatalf("a built-in row carried a plugin action, texts %q", tt.Texts())
	}
	if len(extensions.recordedRequests()) != 0 {
		t.Fatal("a built-in tab dispatched a plugin action")
	}
	// The UI never targets a built-in section: only declared panel views carry
	// actions, and the coordinator rejects anything else.
	if err := state.RequestPanelAction(domainplugin.PanelActionRequest{
		Plugin:    domainplugin.PanelPlugin{ID: "acme.panel", Name: "Acme Panel"},
		SectionID: "processes",
		ActionID:  "kill",
	}, "Kill", "process 42", true); err != nil {
		t.Fatalf("RequestPanelAction: %v", err)
	}
	if pending, ok := state.Pending(); !ok || pending.Panel.SectionID != "processes" {
		t.Fatalf("pending = %+v, %v", pending, ok)
	}
	extensions.err = errors.New("plugin: section \"processes\" is not declared")
	state.ConfirmPending()
	waitPanelRequest(t, extensions)
	waitPanelActionDone(t, state)
	if state.ActionError() == nil {
		t.Fatal("the coordinator's refusal was lost")
	}
	state.Close()
}
