package phoneshell

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

// fakeForwarding is the shared forwarding runtime the page tests drive. Its
// Snapshot grows from the views, so the pages see the same records through
// List and through refresh.
type fakeForwarding struct {
	mu      sync.Mutex
	views   []appforwarding.View
	created []domainforward.Draft
	updated []forwardingUpdate
	deleted []model.ForwardingRuleID
	started []model.ForwardingRuleID
	stopped []model.ForwardingRuleID
	// startStatus is the runtime state a successful Start publishes.
	startStatus domainforward.Status
}

type forwardingUpdate struct {
	ID    model.ForwardingRuleID
	Draft domainforward.Draft
}

func newFakeForwarding(views ...appforwarding.View) *fakeForwarding {
	return &fakeForwarding{views: views, startStatus: domainforward.StatusActive}
}

func (f *fakeForwarding) List(context.Context) ([]appforwarding.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appforwarding.View(nil), f.views...), nil
}

func (f *fakeForwarding) Create(_ context.Context, draft domainforward.Draft) (domainforward.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, draft)
	rule, err := domainforward.NormalizeDraft(draft)
	if err != nil {
		return domainforward.Rule{}, err
	}
	id := model.ForwardingRuleID("rule-" + string(rune('a'+len(f.views))))
	stored := domainforward.ApplyDraft(domainforward.Rule{ID: id}, rule)
	f.views = append(f.views, appforwarding.View{Rule: stored, Status: domainforward.StatusInactive})
	return stored, nil
}

func (f *fakeForwarding) Update(_ context.Context, id model.ForwardingRuleID, draft domainforward.Draft) (domainforward.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, forwardingUpdate{ID: id, Draft: draft})
	for i := range f.views {
		if f.views[i].Rule.ID != id {
			continue
		}
		normalized, err := domainforward.NormalizeDraft(draft)
		if err != nil {
			return domainforward.Rule{}, err
		}
		f.views[i].Rule = domainforward.ApplyDraft(f.views[i].Rule, normalized)
		return f.views[i].Rule, nil
	}
	return domainforward.Rule{}, appforwarding.ErrNotFound
}

func (f *fakeForwarding) Delete(_ context.Context, id model.ForwardingRuleID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	for i := range f.views {
		if f.views[i].Rule.ID == id {
			f.views = append(f.views[:i], f.views[i+1:]...)
			return nil
		}
	}
	return appforwarding.ErrNotFound
}

func (f *fakeForwarding) Start(_ context.Context, id model.ForwardingRuleID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	return f.setStatusLocked(id, f.startStatus, appforwarding.ErrorNone)
}

func (f *fakeForwarding) Stop(_ context.Context, id model.ForwardingRuleID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return f.setStatusLocked(id, domainforward.StatusInactive, appforwarding.ErrorNone)
}

func (f *fakeForwarding) setStatusLocked(id model.ForwardingRuleID, status domainforward.Status, kind appforwarding.ErrorKind) error {
	for i := range f.views {
		if f.views[i].Rule.ID == id {
			f.views[i].Status = status
			f.views[i].Error = kind
			return nil
		}
	}
	return appforwarding.ErrNotFound
}

func (f *fakeForwarding) Snapshot() appforwarding.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return appforwarding.Snapshot{Rules: append([]appforwarding.View(nil), f.views...)}
}

func (f *fakeForwarding) Subscribe(func(appforwarding.Event)) func() { return func() {} }

func (f *fakeForwarding) startedIDs() []model.ForwardingRuleID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.ForwardingRuleID(nil), f.started...)
}

func forwardingRule(id, hostID, label string, kind domainforward.Type) domainforward.Rule {
	return domainforward.Rule{
		ID: model.ForwardingRuleID(id), Label: label, Type: kind,
		BindAddress: "127.0.0.1", LocalPort: 8080,
		RemoteHost: "db.internal", RemotePort: 5432,
		HostID: model.HostID(hostID),
	}
}

// TestVaultForwardingCreatesEditsAndDeletesRules drives the Vault section's
// CRUD through the shared manager seam.
func TestVaultForwardingCreatesEditsAndDeletesRules(t *testing.T) {
	store := &fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}
	service := newFakeForwarding()
	shell := New(WithVault(&fakeVault{}), WithHosts(store), WithForwarding(service))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Forwarding")
	if !tt.HasText("Port forwarding") {
		t.Fatalf("Forwarding page did not render; texts: %v", texts(t, tt))
	}

	if err := tt.Click("New rule"); err != nil {
		t.Fatalf("open the rule editor: %v; texts: %v", err, texts(t, tt))
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus the label: %v", err)
	}
	tt.Type("web")
	if err := tt.Click("bastion"); err != nil {
		t.Fatalf("choose a Host: %v", err)
	}
	if err := tt.Click("Local port"); err != nil {
		t.Fatalf("focus the local port: %v", err)
	}
	tt.Type("8080")
	if err := tt.Click("Remote host"); err != nil {
		t.Fatalf("focus the remote host: %v", err)
	}
	tt.Type("db.internal")
	if err := tt.Click("Remote port"); err != nil {
		t.Fatalf("focus the remote port: %v", err)
	}
	tt.Type("5432")
	if err := tt.Click("Save rule"); err != nil {
		t.Fatalf("save the rule: %v; texts: %v", err, texts(t, tt))
	}

	service.mu.Lock()
	if len(service.created) != 1 {
		service.mu.Unlock()
		t.Fatalf("created = %+v, want one rule", service.created)
	}
	created := service.created[0]
	service.mu.Unlock()
	if created.Label != "web" || created.HostID != "bastion" ||
		created.Type != domainforward.TypeLocal || created.LocalPort != 8080 ||
		created.RemoteHost != "db.internal" || created.RemotePort != 5432 {
		t.Fatalf("created draft = %+v", created)
	}
	if !tt.HasText("web") {
		t.Fatalf("the new rule is not listed; texts: %v", texts(t, tt))
	}

	// Edit appends to the existing label, as the phone's text inputs do.
	if err := tt.Click("Edit rule web"); err != nil {
		t.Fatalf("open the editor: %v; texts: %v", err, texts(t, tt))
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus the label: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Save rule"); err != nil {
		t.Fatalf("save the edit: %v", err)
	}
	service.mu.Lock()
	if len(service.updated) != 1 || service.updated[0].Draft.Label != "web v2" {
		service.mu.Unlock()
		t.Fatalf("updated = %+v, want the edited label", service.updated)
	}
	service.mu.Unlock()
	if !tt.HasText("web v2") {
		t.Fatalf("the edited rule is not listed; texts: %v", texts(t, tt))
	}

	if err := tt.Click("Delete rule web v2"); err != nil {
		t.Fatalf("request the delete: %v; texts: %v", err, texts(t, tt))
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm the delete: %v", err)
	}
	service.mu.Lock()
	deleted := append([]model.ForwardingRuleID(nil), service.deleted...)
	service.mu.Unlock()
	if len(deleted) != 1 {
		t.Fatalf("deleted = %v, want one rule", deleted)
	}
	if tt.HasText("web v2") {
		t.Fatalf("the deleted rule is still listed; texts: %v", texts(t, tt))
	}
}

// TestHostDetailShowsOnlyItsRulesAndRetriesTheFailedOne covers the Host detail
// surface: the rules are filtered to the Host, Start/Stop act on the shared
// runtime, and a failure offers Retry only on the failed rule.
func TestHostDetailShowsOnlyItsRulesAndRetriesTheFailedOne(t *testing.T) {
	broken := forwardingRule("broken", "prod", "broken rule", domainforward.TypeDynamic)
	good := forwardingRule("good", "prod", "web rule", domainforward.TypeLocal)
	other := forwardingRule("other", "bastion", "bastion rule", domainforward.TypeLocal)
	service := newFakeForwarding(
		appforwarding.View{Rule: broken, Status: domainforward.StatusError, Error: appforwarding.ErrorNetwork},
		appforwarding.View{Rule: good, Status: domainforward.StatusActive},
		appforwarding.View{Rule: other, Status: domainforward.StatusActive},
	)
	shell := New(WithVault(&fakeVault{}),
		WithHosts(&fakeHostStore{hosts: vaultHosts(), groups: vaultGroups()}),
		WithForwarding(service))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Hosts)
	tt := ui.NewTester(shell.View(Hosts), 390, 844)
	if err := tt.Click("Details for prod box"); err != nil {
		t.Fatalf("open the Host detail: %v; texts: %v", err, texts(t, tt))
	}
	tt.Advance(600 * time.Millisecond)
	if !tt.HasText("Port forwarding · prod box") {
		t.Fatalf("Host detail does not show the Host's forwarding section; texts: %v", texts(t, tt))
	}
	if !tt.HasText("web rule") || !tt.HasText("broken rule") {
		t.Fatalf("Host detail is missing its rules; texts: %v", texts(t, tt))
	}
	if tt.HasText("bastion rule") {
		t.Fatalf("Host detail shows another Host's rule; texts: %v", texts(t, tt))
	}

	// Only the failed rule offers Retry; the running rule offers Stop.
	if !tt.HasText("Error · the network connection failed") {
		t.Fatalf("the failed rule shows no error; texts: %v", texts(t, tt))
	}
	if tt.HasText("Retry web rule") {
		t.Fatalf("a running rule offers Retry; texts: %v", texts(t, tt))
	}
	if err := tt.Click("Retry broken rule"); err != nil {
		t.Fatalf("retry the failed rule: %v; texts: %v", err, texts(t, tt))
	}
	if got := service.startedIDs(); len(got) != 1 || got[0] != "broken" {
		t.Fatalf("started = %v, want only the retried rule", got)
	}

	if err := tt.Click("Stop web rule"); err != nil {
		t.Fatalf("stop the running rule: %v; texts: %v", err, texts(t, tt))
	}
	service.mu.Lock()
	stopped := append([]model.ForwardingRuleID(nil), service.stopped...)
	service.mu.Unlock()
	if len(stopped) != 1 || stopped[0] != "good" {
		t.Fatalf("stopped = %v, want only web rule", stopped)
	}
}

// forwardingOpener is a Session opener that also supplies the forwarding
// runtime, which is how the production phone runtime reaches the Tools
// surface.
type forwardingOpener struct {
	*fakeOpener
	service ForwardingService
}

func (o *forwardingOpener) ForwardingService() ForwardingService { return o.service }

// TestSessionToolsForwardingActsOnTheSessionHost checks the Session Tools
// entry lists the current Host's rules and starts one without leaving the
// terminal.
func TestSessionToolsForwardingActsOnTheSessionHost(t *testing.T) {
	opener := &fakeOpener{output: "remote prompt $ "}
	service := newFakeForwarding(
		appforwarding.View{Rule: forwardingRule("good", "prod", "web rule", domainforward.TypeLocal),
			Status: domainforward.StatusInactive},
		appforwarding.View{Rule: forwardingRule("elsewhere", "bastion", "other", domainforward.TypeLocal),
			Status: domainforward.StatusInactive},
	)
	store := &fakeHostStore{hosts: phoneHosts()}
	settings := newFakeSettings()
	provider := &forwardingOpener{fakeOpener: opener, service: service}
	shell := New(WithHosts(store), WithSessionOpener(provider),
		WithScreenFactory(fakeScreens), WithSettings(settings), WithForwarding(service))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	shell.Select(Hosts)
	fixture := &phoneFixture{
		shell: shell, store: store, opener: opener, settings: settings,
		hosts: phoneHosts(), context: context.Background(),
	}
	fixture.tester = ui.NewTester(func(c *ui.Context) {
		shell.View(shell.Selected())(c)
	}, 390, 844)
	fixture.tester.Frame()

	fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("remote prompt $")
	})
	path := shell.Router(Sessions).Path()
	fixture.openToolsPanel(t, "prod box")
	if err := fixture.tester.Click("Open Port forwarding"); err != nil {
		t.Fatalf("open Port forwarding: %v; texts: %v", err, fixture.tester.Texts())
	}
	fixture.tester.Frame()
	if shell.Router(Sessions).Path() != path {
		t.Fatalf("the forwarding tool changed the route to %q", shell.Router(Sessions).Path())
	}
	if !fixture.tester.HasText("web rule") {
		t.Fatalf("the tool does not list the Host's rule; texts: %v", fixture.tester.Texts())
	}
	if fixture.tester.HasText("other") {
		t.Fatalf("the tool lists another Host's rule; texts: %v", fixture.tester.Texts())
	}
	if err := fixture.tester.Click("Start web rule"); err != nil {
		t.Fatalf("start the rule: %v; texts: %v", err, fixture.tester.Texts())
	}
	if got := service.startedIDs(); len(got) != 1 || got[0] != "good" {
		t.Fatalf("started = %v, want the Session Host's rule", got)
	}
}
