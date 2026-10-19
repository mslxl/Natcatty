package state

import "testing"

func TestStateStartsWithoutTabs(t *testing.T) {
	state := NewState()

	if state.TabCount() != 0 || state.ActiveTab() != -1 {
		t.Fatalf("tabs = %d, active = %d, want 0 and -1", state.TabCount(), state.ActiveTab())
	}
}

func TestNewTabAppendsAndClosesActiveTab(t *testing.T) {
	state := NewState()

	state.NewTab()
	state.NewTab()
	if state.TabCount() != 2 || state.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d, want 2 and 1", state.TabCount(), state.ActiveTab())
	}
	if got := state.TabNumbers(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("tab numbers = %v, want [1 2]", got)
	}

	if !state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false with an active tab")
	}
	if state.TabCount() != 1 || state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d, want 1 and 0", state.TabCount(), state.ActiveTab())
	}

	if !state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false with one tab left")
	}
	if state.TabCount() != 0 || state.ActiveTab() != -1 {
		t.Fatalf("tabs = %d, active = %d, want 0 and -1", state.TabCount(), state.ActiveTab())
	}
	if state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned true with no tabs")
	}
}

func TestCloseActiveTabSelectsPreviousNeighbour(t *testing.T) {
	state := NewState()
	state.NewTab()
	state.NewTab()
	state.NewTab()
	state.SelectTab(2)

	if !state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if state.TabCount() != 2 || state.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d, want 2 and 1", state.TabCount(), state.ActiveTab())
	}
}

func TestRequestLocaleReportsTheRequestedTag(t *testing.T) {
	var requested []string
	state := NewState(WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))

	state.RequestLocale("es")
	state.RequestLocale("zh-TW")

	if len(requested) != 2 || requested[0] != "es" || requested[1] != "zh-TW" {
		t.Fatalf("requested locales = %v, want [es zh-TW]", requested)
	}
}

func TestRequestSettingsReportsTheRequest(t *testing.T) {
	requests := 0
	state := NewState(WithSettingsRequest(func() { requests++ }))

	state.RequestSettings()
	state.RequestSettings()

	if requests != 2 {
		t.Fatalf("settings requests = %d, want 2", requests)
	}
}
