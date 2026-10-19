package forwarding

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeService is the forwarding use-case seam the UI state is tested against.
type fakeService struct {
	mu       sync.Mutex
	rules    map[model.ForwardingRuleID]domainforward.Rule
	status   map[model.ForwardingRuleID]domainforward.Status
	failures map[model.ForwardingRuleID]appforwarding.ErrorKind
	subs     map[int]func(appforwarding.Event)
	nextSub  int
	nextID   int
	revision uint64
	listErr  error
}

func newFakeService() *fakeService {
	return &fakeService{
		rules:    map[model.ForwardingRuleID]domainforward.Rule{},
		status:   map[model.ForwardingRuleID]domainforward.Status{},
		failures: map[model.ForwardingRuleID]appforwarding.ErrorKind{},
		subs:     map[int]func(appforwarding.Event){},
	}
}

func (f *fakeService) List(context.Context) ([]appforwarding.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.viewsLocked(), nil
}

func (f *fakeService) viewsLocked() []appforwarding.View {
	views := make([]appforwarding.View, 0, len(f.rules))
	for _, rule := range f.rules {
		status := f.status[rule.ID]
		if status == "" {
			status = domainforward.StatusInactive
		}
		views = append(views, appforwarding.View{
			Rule: rule, Status: status, Error: f.failures[rule.ID],
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Rule.OrderKey != views[j].Rule.OrderKey {
			return views[i].Rule.OrderKey < views[j].Rule.OrderKey
		}
		return views[i].Rule.ID < views[j].Rule.ID
	})
	return views
}

func (f *fakeService) Create(_ context.Context, draft domainforward.Draft) (domainforward.Rule, error) {
	normalized, err := domainforward.NormalizeDraft(draft)
	if err != nil {
		return domainforward.Rule{}, &appforwarding.Error{
			Kind: appforwarding.KindValidation, Cause: err,
		}
	}
	f.mu.Lock()
	f.nextID++
	rule := domainforward.ApplyDraft(domainforward.Rule{
		ID:       model.ForwardingRuleID("rule-" + strconv.Itoa(f.nextID)),
		OrderKey: int64(f.nextID * 1000),
	}, normalized)
	f.rules[rule.ID] = rule
	f.mu.Unlock()
	f.publish(rule.ID)
	return rule, nil
}

func (f *fakeService) Update(_ context.Context, id model.ForwardingRuleID, draft domainforward.Draft) (domainforward.Rule, error) {
	normalized, err := domainforward.NormalizeDraft(draft)
	if err != nil {
		return domainforward.Rule{}, &appforwarding.Error{
			Kind: appforwarding.KindValidation, Cause: err,
		}
	}
	f.mu.Lock()
	rule, ok := f.rules[id]
	if !ok {
		f.mu.Unlock()
		return domainforward.Rule{}, appforwarding.ErrNotFound
	}
	rule = domainforward.ApplyDraft(rule, normalized)
	f.rules[id] = rule
	f.mu.Unlock()
	f.publish(id)
	return rule, nil
}

func (f *fakeService) Delete(_ context.Context, id model.ForwardingRuleID) error {
	f.mu.Lock()
	if _, ok := f.rules[id]; !ok {
		f.mu.Unlock()
		return appforwarding.ErrNotFound
	}
	delete(f.rules, id)
	delete(f.status, id)
	f.mu.Unlock()
	f.publish(id)
	return nil
}

func (f *fakeService) Start(_ context.Context, id model.ForwardingRuleID) error {
	f.mu.Lock()
	rule, ok := f.rules[id]
	if !ok {
		f.mu.Unlock()
		return appforwarding.ErrNotFound
	}
	if !rule.IsEnabled() {
		f.mu.Unlock()
		return &appforwarding.Error{Kind: appforwarding.KindDisabled, Cause: errors.New("disabled")}
	}
	f.status[id] = domainforward.StatusActive
	f.mu.Unlock()
	f.publish(id)
	return nil
}

func (f *fakeService) Stop(_ context.Context, id model.ForwardingRuleID) error {
	f.mu.Lock()
	if _, ok := f.rules[id]; !ok {
		f.mu.Unlock()
		return appforwarding.ErrNotFound
	}
	f.status[id] = domainforward.StatusInactive
	delete(f.failures, id)
	f.mu.Unlock()
	f.publish(id)
	return nil
}

func (f *fakeService) Snapshot() appforwarding.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return appforwarding.Snapshot{Revision: f.revision, Rules: f.viewsLocked()}
}

func (f *fakeService) Subscribe(fn func(appforwarding.Event)) func() {
	f.mu.Lock()
	id := f.nextSub
	f.nextSub++
	f.subs[id] = fn
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.subs, id)
		f.mu.Unlock()
	}
}

func (f *fakeService) publish(id model.ForwardingRuleID) {
	f.mu.Lock()
	f.revision++
	revision := f.revision
	view := appforwarding.View{}
	for _, candidate := range f.viewsLocked() {
		if candidate.Rule.ID == id {
			view = candidate
		}
	}
	subs := make([]func(appforwarding.Event), 0, len(f.subs))
	for _, fn := range f.subs {
		subs = append(subs, fn)
	}
	f.mu.Unlock()
	for _, fn := range subs {
		fn(appforwarding.Event{Revision: revision, ID: id, View: view})
	}
}

func (f *fakeService) setFailure(id model.ForwardingRuleID, kind appforwarding.ErrorKind) {
	f.mu.Lock()
	f.status[id] = domainforward.StatusError
	f.failures[id] = kind
	f.mu.Unlock()
	f.publish(id)
}

func (f *fakeService) setStatus(id model.ForwardingRuleID, status domainforward.Status) {
	f.mu.Lock()
	f.status[id] = status
	f.mu.Unlock()
	f.publish(id)
}

func (f *fakeService) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rules)
}

type fakeHosts struct{ items []domainvault.Host }

func (h fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}

func newState(t *testing.T, service *fakeService, hosts ...domainvault.Host) *State {
	t.Helper()
	state := NewState(service, fakeHosts{items: hosts})
	t.Cleanup(state.Close)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state
}

func validForm() Form {
	return Form{
		Type:        string(domainforward.TypeLocal),
		Label:       "web",
		HostID:      "host-1",
		BindAddress: "127.0.0.1",
		LocalPort:   "8080",
		RemoteHost:  "localhost",
		RemotePort:  "80",
		Enabled:     true,
	}
}

func TestStateCreatesEditsAndDeletesARule(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})

	state.BeginCreate()
	form := state.Form()
	*form = validForm()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Editing() {
		t.Fatal("Save left the editor open")
	}
	if state.RuleCount() != 1 {
		t.Fatalf("rules = %d, want 1", state.RuleCount())
	}
	id := state.Items()[0].Rule.ID
	if got := state.Items()[0].Rule.Label; got != "web" {
		t.Fatalf("label = %q, want web", got)
	}

	if !state.BeginEdit(id) {
		t.Fatal("BeginEdit reported the rule missing")
	}
	state.Form().Label = "web-2"
	if err := state.Save(); err != nil {
		t.Fatalf("Save edit: %v", err)
	}
	if got := state.Items()[0].Rule.Label; got != "web-2" {
		t.Fatalf("edited label = %q, want web-2", got)
	}

	state.RequestDelete(id)
	if state.PendingDelete() != id {
		t.Fatalf("pending delete = %q, want %q", state.PendingDelete(), id)
	}
	if err := state.ConfirmDelete(); err != nil {
		t.Fatalf("ConfirmDelete: %v", err)
	}
	if service.count() != 0 || state.RuleCount() != 0 {
		t.Fatalf("rules after delete = %d/%d, want 0/0", service.count(), state.RuleCount())
	}
}

func TestStateRejectsMalformedPortsBeforeCallingTheService(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	state.Form().LocalPort = "not-a-port"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemLocalPortInvalid {
		t.Fatalf("problem = %q, want local port invalid", state.Problem())
	}
	if service.count() != 0 {
		t.Fatalf("service created %d rules, want 0", service.count())
	}

	state.Form().LocalPort = "70000"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemLocalPortRange {
		t.Fatalf("problem = %q, want local port range", state.Problem())
	}
}

func TestStateStartsAndStopsAndProjectsRuntimeStatus(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := state.Items()[0].Rule.ID

	if !state.Start(id) {
		t.Fatalf("Start failed: %s", state.Problem())
	}
	state.Flush()
	if got := state.Status(id); got != domainforward.StatusActive {
		t.Fatalf("status = %q, want active", got)
	}
	if !state.Stop(id) {
		t.Fatalf("Stop failed: %s", state.Problem())
	}
	state.Flush()
	if got := state.Status(id); got != domainforward.StatusInactive {
		t.Fatalf("status = %q, want inactive", got)
	}
}

func TestStateReportsDisabledRulesAndRuntimeFailures(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	state.Form().Enabled = false
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := state.Items()[0].Rule.ID
	if state.Start(id) {
		t.Fatal("Start accepted a disabled rule")
	}
	if state.Problem() != ProblemDisabled {
		t.Fatalf("problem = %q, want disabled", state.Problem())
	}

	service.setFailure(id, appforwarding.ErrorBindConflict)
	state.Flush()
	if got := state.RuntimeError(id); got != appforwarding.ErrorBindConflict {
		t.Fatalf("runtime error = %q, want bind conflict", got)
	}
}

func TestStateTogglesTheDurableEnabledFlag(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := state.Items()[0].Rule.ID

	if !state.ToggleEnabled(id) {
		t.Fatalf("ToggleEnabled failed: %s", state.Problem())
	}
	if state.Items()[0].Rule.IsEnabled() {
		t.Fatal("rule is still enabled after the toggle")
	}
	if !state.ToggleEnabled(id) {
		t.Fatalf("ToggleEnabled failed: %s", state.Problem())
	}
	if !state.Items()[0].Rule.IsEnabled() {
		t.Fatal("rule is still disabled after the second toggle")
	}
}

func TestStateDeleteRequiresConfirmation(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := state.Items()[0].Rule.ID

	state.RequestDelete(id)
	if service.count() != 1 {
		t.Fatal("RequestDelete deleted without confirmation")
	}
	state.CancelDelete()
	if state.PendingDelete() != "" {
		t.Fatal("CancelDelete left a pending delete")
	}
	if service.count() != 1 {
		t.Fatal("CancelDelete deleted the rule")
	}
}

func TestStateKeepsTheAutoReconnectTriState(t *testing.T) {
	service := newFakeService()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	// The reconnect box is untouched; auto-start alone must enable reconnect.
	state.Form().AutoReconnect = AutoReconnectFollow
	state.Form().AutoStart = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rule := state.Items()[0].Rule
	if rule.AutoReconnect != nil {
		t.Fatalf("autoReconnect = %v, want nil so auto-start decides", *rule.AutoReconnect)
	}
	if !rule.AutoReconnectEnabled() {
		t.Fatal("an auto-start rule does not reconnect by default")
	}

	// Choosing "never" is an explicit override, not a fallback.
	state.BeginEdit(rule.ID)
	if state.Form().AutoReconnect != AutoReconnectFollow {
		t.Fatalf("editor reconnect = %q, want follow", state.Form().AutoReconnect)
	}
	state.Form().AutoReconnect = AutoReconnectOff
	if err := state.Save(); err != nil {
		t.Fatalf("Save edit: %v", err)
	}
	rule = state.Items()[0].Rule
	if rule.AutoReconnect == nil || *rule.AutoReconnect {
		t.Fatalf("autoReconnect = %v, want an explicit false", rule.AutoReconnect)
	}
	if rule.AutoReconnectEnabled() {
		t.Fatal("the explicit reconnect override was lost")
	}
}

func TestStateShowsTheLocalProblemForAFailedReload(t *testing.T) {
	service := newFakeService()
	service.listErr = errors.New("database unavailable")
	state := NewState(service, fakeHosts{})
	t.Cleanup(state.Close)
	if err := state.Reload(); err == nil {
		t.Fatal("Reload accepted a failing service")
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("problem = %q, want internal", state.Problem())
	}
}

func TestStateIgnoresStaleRuntimeEvents(t *testing.T) {
	service := newFakeService()
	redraws := 0
	state := NewState(service, fakeHosts{}, WithRedrawRequest(func() { redraws++ }))
	t.Cleanup(state.Close)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	state.BeginCreate()
	*state.Form() = validForm()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before := redraws
	service.setStatus(state.Items()[0].Rule.ID, domainforward.StatusActive)
	if redraws <= before {
		t.Fatal("runtime event did not request a redraw")
	}
	state.Flush()
	if got := state.Status(state.Items()[0].Rule.ID); got != domainforward.StatusActive {
		t.Fatalf("status = %q, want active", got)
	}
}
