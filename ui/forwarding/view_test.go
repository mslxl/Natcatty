package forwarding

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

func newView(t *testing.T, state *State) *ui.Tester {
	t.Helper()
	return ui.NewTester(View(state), 900, 600)
}

func stateWithRule(t *testing.T, service *fakeService) *State {
	t.Helper()
	state := newState(t, service, domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return state
}

func TestViewShowsTheEmptyStateAndOpensTheEditor(t *testing.T) {
	state := newState(t, newFakeService(), domainvault.Host{ID: "host-1", Label: "bastion"})
	tt := newView(t, state)

	if !tt.HasText(locales.In("en").VaultForwardingEmpty()) {
		t.Fatalf("forwarding section does not show its empty state, texts %q", tt.Texts())
	}
	if err := tt.Click(locales.In("en").VaultForwardingNew()); err != nil {
		t.Fatalf("click New Rule: %v", err)
	}
	tt.Frame()

	for _, want := range []string{
		locales.In("en").VaultForwardingFormCreateTitle(),
		locales.In("en").VaultForwardingTypeLocal(),
		locales.In("en").VaultForwardingLabel(),
		"bastion",
		locales.In("en").VaultForwardingEnabled(),
	} {
		if !tt.HasText(want) {
			t.Errorf("editor does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestViewListsRuleSummaryStatusAndActions(t *testing.T) {
	service := newFakeService()
	state := stateWithRule(t, service)
	tt := newView(t, state)

	for _, want := range []string{
		"web",
		"bastion",
		"127.0.0.1:8080",
		"localhost:80",
		locales.In("en").VaultForwardingStatusInactive(),
		locales.In("en").VaultForwardingStart(),
		locales.In("en").VaultHostEdit(),
	} {
		if !tt.HasText(want) {
			t.Errorf("forwarding section does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestViewShowsRuntimeErrorsAndTheFailingHop(t *testing.T) {
	service := newFakeService()
	state := stateWithRule(t, service)
	id := state.Items()[0].Rule.ID
	service.setFailure(id, appforwarding.ErrorAuth)
	state.Flush()
	tt := newView(t, state)

	if !tt.HasText(locales.In("en").VaultForwardingStatusError()) {
		t.Fatalf("forwarding section does not show the error status, texts %q", tt.Texts())
	}
	if !tt.HasText(locales.In("en").VaultForwardingErrorAuth()) {
		t.Fatalf("forwarding section does not show the auth failure, texts %q", tt.Texts())
	}
}

func TestViewStartsAndStopsARule(t *testing.T) {
	service := newFakeService()
	state := stateWithRule(t, service)
	tt := newView(t, state)

	if err := tt.Click(locales.In("en").VaultForwardingStart()); err != nil {
		t.Fatalf("click Start: %v", err)
	}
	tt.Frame()
	if !tt.HasText(locales.In("en").VaultForwardingStatusActive()) {
		t.Fatalf("rule did not become active, texts %q", tt.Texts())
	}
	if err := tt.Click(locales.In("en").VaultForwardingStop()); err != nil {
		t.Fatalf("click Stop: %v", err)
	}
	tt.Frame()
	if !tt.HasText(locales.In("en").VaultForwardingStatusInactive()) {
		t.Fatalf("rule did not become inactive, texts %q", tt.Texts())
	}
}

func TestViewLocalizesFormProblems(t *testing.T) {
	state := newState(t, newFakeService(), domainvault.Host{ID: "host-1", Label: "bastion"})
	state.BeginCreate()
	*state.Form() = validForm()
	state.Form().LocalPort = "bad"
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	tt := newView(t, state)

	if !tt.HasText(locales.In("en").VaultForwardingProblemLocalPortInvalid()) {
		t.Fatalf("editor does not show the English problem, texts %q", tt.Texts())
	}
	tt.SetLocale("es")
	if !tt.HasText(locales.In("es").VaultForwardingProblemLocalPortInvalid()) {
		t.Fatalf("editor does not show the Spanish problem, texts %q", tt.Texts())
	}
}

func TestViewDeleteConfirmationNamesTheRule(t *testing.T) {
	state := stateWithRule(t, newFakeService())
	tt := newView(t, state)

	if err := tt.Click(locales.In("en").VaultHostDelete()); err != nil {
		t.Fatalf("click Delete: %v", err)
	}
	tt.Frame()
	if !tt.HasText(locales.In("en").VaultForwardingDeleteConfirm("web")) {
		t.Fatalf("forwarding section does not show the named confirmation, texts %q", tt.Texts())
	}
}
