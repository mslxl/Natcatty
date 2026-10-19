package state

import (
	"context"
	"errors"
	"strconv"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeHosts is the application seam the Vault state is tested against.
type fakeHosts struct {
	items   []domainvault.Host
	created []domainvault.Draft
	updated []domainvault.Draft
	deleted []model.HostID
	err     error
	nextID  int
}

func (f *fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.Host(nil), f.items...), nil
}

func (f *fakeHosts) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	if f.err != nil {
		return domainvault.Host{}, f.err
	}
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	f.nextID++
	host := domainvault.Host{
		ID:       model.HostID("host-" + strconv.Itoa(f.nextID)),
		Label:    normalized.Label,
		Hostname: normalized.Hostname,
		Port:     normalized.Port,
		Username: normalized.Username,
		Protocol: normalized.Protocol,
		Group:    normalized.Group,
		Tags:     normalized.Tags,
		Notes:    normalized.Notes,
		Pinned:   normalized.Pinned,
		OrderKey: int64(f.nextID) * 1000,
	}
	f.created = append(f.created, draft)
	f.items = append(f.items, host)
	return host, nil
}

func (f *fakeHosts) Update(_ context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	if f.err != nil {
		return domainvault.Host{}, f.err
	}
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for i, host := range f.items {
		if host.ID != id {
			continue
		}
		host.Label = normalized.Label
		host.Hostname = normalized.Hostname
		host.Port = normalized.Port
		host.Username = normalized.Username
		host.Protocol = normalized.Protocol
		host.Group = normalized.Group
		host.Tags = normalized.Tags
		host.Notes = normalized.Notes
		host.Pinned = normalized.Pinned
		f.items[i] = host
		f.updated = append(f.updated, draft)
		return host, nil
	}
	return domainvault.Host{}, appvault.ErrNotFound
}

func (f *fakeHosts) Delete(_ context.Context, id model.HostID) error {
	if f.err != nil {
		return f.err
	}
	for i, host := range f.items {
		if host.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func newTestState(t *testing.T, hosts *fakeHosts) (*State, *int) {
	t.Helper()
	redraws := new(int)
	state := NewState(hosts, WithRedrawRequest(func() { *redraws++ }))
	return state, redraws
}

func TestReloadListsHosts(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "a", Label: "alpha"}, {ID: "b", Label: "beta"}}}
	state, redraws := newTestState(t, repo)

	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if state.HostCount() != 2 || state.Items()[0].ID != "a" {
		t.Fatalf("items = %+v, want two Hosts", state.Items())
	}
	if *redraws != 1 {
		t.Fatalf("redraws = %d, want 1", *redraws)
	}
}

func TestCreateFlowSavesAndReloads(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	if !state.Editing() {
		t.Fatal("BeginCreate did not open the editor")
	}
	form := state.Form()
	form.Label = "web"
	form.Hostname = "example.com"
	form.Username = "root"
	form.Group = "prod/web"
	form.Tags = "prod, web"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Editing() {
		t.Fatal("Save left the editor open")
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("Problem = %q, want none", state.Problem())
	}
	if len(repo.created) != 1 {
		t.Fatalf("created drafts = %d, want 1", len(repo.created))
	}
	if state.HostCount() != 1 {
		t.Fatalf("Hosts after Save = %d, want 1", state.HostCount())
	}
}

func TestInvalidHostProducesAProblemKey(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Label = "web"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemHostnameRequired {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemHostnameRequired)
	}
	if !state.Editing() {
		t.Fatal("Save closed the editor after a validation failure")
	}
	if len(repo.created) != 0 {
		t.Fatalf("created drafts = %d, want 0", len(repo.created))
	}
}

func TestNonNumericPortIsRejectedBeforeTheService(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Hostname = "example.com"
	state.Form().Port = "twenty-two"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemPortInvalid {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemPortInvalid)
	}
	if len(repo.created) != 0 {
		t.Fatalf("created drafts = %d, want 0", len(repo.created))
	}
}

func TestEditPopulatesTheFormAndUpdatesTheSameHost(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, Group: "prod",
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit returned false for a listed Host")
	}
	if state.Form().ID != "host-1" || state.Form().Hostname != "example.com" || state.Form().Port != "22" {
		t.Fatalf("form = %+v, want the Host's values", state.Form())
	}
	state.Form().Label = "web-2"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.updated) != 1 || len(repo.created) != 0 {
		t.Fatalf("updated = %d, created = %d, want 1 and 0", len(repo.updated), len(repo.created))
	}
	if state.Items()[0].Label != "web-2" {
		t.Fatalf("Label = %q, want web-2", state.Items()[0].Label)
	}
}

func TestConflictMapsToTheDuplicateProblem(t *testing.T) {
	repo := &fakeHosts{err: &appvault.Error{Kind: appvault.KindConflict}}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Hostname = "example.com"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemDuplicate {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemDuplicate)
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "host-1", Label: "web"}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	state.RequestDelete("host-1")
	if state.PendingDelete() != "host-1" {
		t.Fatalf("pending delete = %q, want host-1", state.PendingDelete())
	}
	state.CancelDelete()
	if state.PendingDelete() != "" || len(repo.deleted) != 0 {
		t.Fatal("CancelDelete deleted the Host or left the confirmation open")
	}

	state.RequestDelete("host-1")
	if err := state.ConfirmDelete(); err != nil {
		t.Fatalf("ConfirmDelete: %v", err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != "host-1" {
		t.Fatalf("deleted = %v, want [host-1]", repo.deleted)
	}
	if state.HostCount() != 0 {
		t.Fatalf("Hosts after Delete = %d, want 0", state.HostCount())
	}
}

func TestTogglePinnedPersistsThePreferenceThroughUpdate(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	if err := state.TogglePinned("host-1"); err != nil {
		t.Fatalf("TogglePinned: %v", err)
	}
	if len(repo.updated) != 1 || !repo.updated[0].Pinned {
		t.Fatalf("updated drafts = %+v, want Pinned true", repo.updated)
	}
	if !state.Items()[0].Pinned {
		t.Fatal("the pinned Host was not refreshed")
	}
}

func TestReloadDropsASelectionThatNoLongerExists(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "host-1", Label: "web"}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	state.Select("host-1")
	repo.items = nil
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	if state.Selected() != "" {
		t.Fatalf("selection = %q after the Host disappeared, want empty", state.Selected())
	}
}

func TestStorageFailureProducesTheInternalProblem(t *testing.T) {
	repo := &fakeHosts{err: errors.New("disk on fire")}
	state, _ := newTestState(t, repo)

	if err := state.Reload(); err == nil {
		t.Fatal("Reload returned nil, want the storage error")
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemInternal)
	}
}
