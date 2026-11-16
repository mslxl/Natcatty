package state

import (
	"context"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// savingGroups is the Host Group seam for the editor round-trip tests. It
// normalizes every draft the way the application Service does, so a field the
// editor fails to submit is lost exactly as it would be in production.
type savingGroups struct {
	items   []domainvault.Group
	updated []domainvault.GroupDraft
}

func (f *savingGroups) ListGroups(context.Context) ([]domainvault.Group, error) {
	return append([]domainvault.Group(nil), f.items...), nil
}

func (f *savingGroups) CreateGroup(context.Context, domainvault.GroupDraft) (domainvault.Group, error) {
	return domainvault.Group{}, nil
}

func (f *savingGroups) UpdateGroup(_ context.Context, _ string, draft domainvault.GroupDraft) (domainvault.Group, error) {
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, err
	}
	f.updated = append(f.updated, draft)
	for i := range f.items {
		if f.items[i].Path != domainvault.NormalizeGroup(normalized.Path) {
			continue
		}
		group := f.items[i]
		group.ProxyProfileID = normalized.ProxyProfileID
		group.DirectProxy = normalized.DirectProxy
		group.NetworkProfileID = normalized.NetworkProfileID
		f.items[i] = group
		return group, nil
	}
	return domainvault.Group{}, nil
}

func (f *savingGroups) DeleteGroup(context.Context, string) error { return nil }

func (f *savingGroups) AssignHostGroup(context.Context, model.HostID, string) error {
	return nil
}

// TestHostDetailsRoundTripsAnExplicitDirectProxy proves the editor both shows
// a saved direct choice and submits it again, so editing an unrelated field
// cannot silently turn the Host back into an inheriting one.
func TestHostDetailsRoundTripsAnExplicitDirectProxy(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, DirectProxy: true,
	}}}
	state := NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if !state.BeginEdit("host-1") {
		t.Fatalf("BeginEdit rejected a listed Host")
	}
	if !state.Form().DirectProxy {
		t.Fatalf("Form().DirectProxy = false, want the saved direct choice")
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("updated drafts = %d, want 1", len(repo.updated))
	}
	normalized, err := domainvault.NormalizeDraft(repo.updated[0])
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if !normalized.DirectProxy || normalized.ProxyProfileID != "" {
		t.Fatalf("saved draft = %+v, want an explicit direct connection", normalized)
	}
	if !repo.items[0].DirectProxy {
		t.Fatalf("stored Host lost its direct choice: %+v", repo.items[0])
	}
}

// TestHostDirectChoiceClearsAnInheritedProfile is the other half of the
// editor: selecting Direct must submit DirectProxy and drop the Profile
// reference even when the form still carries one.
func TestHostDirectChoiceClearsAnInheritedProfile(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, ProxyProfileID: "office",
	}}}
	state := NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatalf("BeginEdit rejected a listed Host")
	}
	// This is what the proxy radio writes when the user selects Direct.
	state.Form().DirectProxy = true
	state.Form().ProxyProfileID = "office"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	normalized, err := domainvault.NormalizeDraft(repo.updated[0])
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if !normalized.DirectProxy || normalized.ProxyProfileID != "" {
		t.Fatalf("saved draft = %+v, want direct with no Profile reference", normalized)
	}
}

// TestPinningAHostKeepsItsExplicitDirectProxy covers the second Host save path,
// which writes a draft built from the stored record instead of the editor.
func TestPinningAHostKeepsItsExplicitDirectProxy(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, DirectProxy: true,
	}}}
	state := NewState(repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := state.TogglePinned("host-1"); err != nil {
		t.Fatalf("TogglePinned: %v", err)
	}
	normalized, err := domainvault.NormalizeDraft(repo.updated[0])
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if !normalized.DirectProxy || normalized.ProxyProfileID != "" {
		t.Fatalf("pinned draft = %+v, want the direct choice preserved", normalized)
	}
	if !repo.items[0].DirectProxy || !repo.items[0].Pinned {
		t.Fatalf("stored Host = %+v, want direct and pinned", repo.items[0])
	}
}

// TestGroupEditorRoundTripsAnExplicitDirectProxy proves the Group editor shows
// and resubmits its saved direct choice.
func TestGroupEditorRoundTripsAnExplicitDirectProxy(t *testing.T) {
	groups := &savingGroups{items: []domainvault.Group{{
		ID: "group-1", Path: "prod", DirectProxy: true,
	}}}
	state := NewState(&fakeHosts{}, WithGroups(groups))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEditGroup("prod") {
		t.Fatalf("BeginEditGroup rejected a listed Group")
	}
	if !state.GroupForm().DirectProxy {
		t.Fatalf("GroupForm().DirectProxy = false, want the saved direct choice")
	}
	if err := state.SaveGroup(); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	if len(groups.updated) != 1 {
		t.Fatalf("updated drafts = %d, want 1", len(groups.updated))
	}
	normalized, err := domainvault.NormalizeGroupDraft(groups.updated[0])
	if err != nil {
		t.Fatalf("NormalizeGroupDraft: %v", err)
	}
	if !normalized.DirectProxy || normalized.ProxyProfileID != "" {
		t.Fatalf("saved Group draft = %+v, want an explicit direct connection", normalized)
	}
	if !groups.items[0].DirectProxy {
		t.Fatalf("stored Group lost its direct choice: %+v", groups.items[0])
	}
}
