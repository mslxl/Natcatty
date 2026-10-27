package forwarding

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	"github.com/mslxl/natcatty/domain/model"
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

// TestEditorKeepsActionsAndProblemPinnedAtTheMinimumWindow is the ticket 61
// regression for the forwarding editor: at 704x421 a long Host list must not
// push Save, Cancel, or the save failure below the window. The last form row
// scrolls fully above the pinned actions.
func TestEditorKeepsActionsAndProblemPinnedAtTheMinimumWindow(t *testing.T) {
	hosts := make([]domainvault.Host, 0, 12)
	for index := 0; index < 12; index++ {
		hosts = append(hosts, domainvault.Host{
			ID:    domainvaultHostID(index),
			Label: "bastion-" + string(rune('a'+index)),
		})
	}
	state := newState(t, newFakeService(), hosts...)
	state.BeginCreate()
	*state.Form() = validForm()
	state.Form().HostID = string(hosts[0].ID)
	state.Form().LocalPort = "not-a-port"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tt := ui.NewTester(View(state), 704, 421)
	tt.SetLocale("en")
	tt.Frame()

	m := locales.In("en")
	inside := func(name string) ui.Rect {
		t.Helper()
		rect, ok := tt.Find(name)
		if !ok {
			t.Fatalf("704x421 does not render %q, texts %q", name, tt.Texts())
		}
		if rect.X < -0.5 || rect.Y < -0.5 ||
			rect.X+rect.W > 704.5 || rect.Y+rect.H > 421.5 {
			t.Fatalf("704x421 clips %q: %+v", name, rect)
		}
		return rect
	}
	save := inside(m.VaultHostSave())
	inside(m.VaultHostCancel())
	problem := inside(m.VaultForwardingProblemLocalPortInvalid())
	if problem.Y+problem.H > save.Y+0.5 {
		t.Fatalf("the save problem is below the pinned actions: problem %+v save %+v", problem, save)
	}

	// The last form row starts below the fold or close to it; scrolling must
	// bring it fully above the pinned action bar.
	tt.Scroll(352, 180, 0, 4000)
	tt.Frame()
	enabled, ok := tt.Find(m.VaultForwardingEnabled())
	if !ok || enabled.W <= 0 || enabled.H <= 0 {
		t.Fatalf("the last form row is not reachable by scrolling: %+v (%v)", enabled, ok)
	}
	if enabled.Y < 0 || enabled.Y+enabled.H > save.Y+0.5 {
		t.Fatalf("the last form row did not clear the pinned actions: row %+v save %+v", enabled, save)
	}
	// The problem stays pinned above the actions after scrolling.
	problem = inside(m.VaultForwardingProblemLocalPortInvalid())
	if problem.Y+problem.H > save.Y+0.5 {
		t.Fatalf("scrolling moved the save problem under the actions: problem %+v save %+v", problem, save)
	}
}

func domainvaultHostID(index int) model.HostID {
	return model.HostID("host-" + string(rune('a'+index)))
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
