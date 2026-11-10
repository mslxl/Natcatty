package plugin

import "testing"

func request(permission Permission, scope Scope) PermissionRequest {
	return PermissionRequest{Permission: permission, Scope: scope}
}

func TestDiffPermissionsSeparatesAddedRemovedAndScopeChanges(t *testing.T) {
	oldRequired := []PermissionRequest{
		request(PermissionCommands, Scope{}),
		request(PermissionNetworkDial, Scope{Host: "old.example", Ports: []int{443}}),
	}
	oldOptional := []PermissionRequest{
		request(PermissionClipboardRead, Scope{}),
		request(PermissionFilesWrite, Scope{Path: "/tmp/notes"}),
	}
	newRequired := []PermissionRequest{
		request(PermissionCommands, Scope{}),
		request(PermissionNetworkDial, Scope{Host: "new.example", Ports: []int{22}}),
		request(PermissionVaultSecrets, Scope{Field: "password"}),
	}
	newOptional := []PermissionRequest{
		request(PermissionFilesWrite, Scope{Path: "/tmp/notes"}),
		request(PermissionNotifications, Scope{}),
	}
	diff := DiffPermissions(oldRequired, oldOptional, newRequired, newOptional)

	if len(diff.AddedRequired) != 1 ||
		diff.AddedRequired[0].Permission != PermissionVaultSecrets {
		t.Fatalf("added required = %+v", diff.AddedRequired)
	}
	if len(diff.RemovedRequired) != 0 {
		t.Fatalf("removed required = %+v", diff.RemovedRequired)
	}
	if len(diff.ScopeChanged) != 1 ||
		diff.ScopeChanged[0].Permission != PermissionNetworkDial ||
		diff.ScopeChanged[0].Before.Host != "old.example" ||
		diff.ScopeChanged[0].After.Host != "new.example" {
		t.Fatalf("scope changed = %+v", diff.ScopeChanged)
	}
	if len(diff.AddedOptional) != 1 ||
		diff.AddedOptional[0].Permission != PermissionNotifications {
		t.Fatalf("added optional = %+v", diff.AddedOptional)
	}
	if len(diff.RemovedOptional) != 1 ||
		diff.RemovedOptional[0].Permission != PermissionClipboardRead {
		t.Fatalf("removed optional = %+v", diff.RemovedOptional)
	}
	needsConsent := diff.RequiringConsent()
	if len(needsConsent) != 2 {
		t.Fatalf("requiring consent = %+v, want the added permission and the expanded scope", needsConsent)
	}
	if diff.Empty() {
		t.Fatal("a non-empty diff reported empty")
	}
	if !(PermissionDiff{}).Empty() {
		t.Fatal("the zero diff reported non-empty")
	}
}

func TestDiffPermissionsScopeNarrowingAlsoRequiresFreshConsent(t *testing.T) {
	oldRequired := []PermissionRequest{
		request(PermissionFilesRead, Scope{Path: "/tmp"}),
	}
	newRequired := []PermissionRequest{
		request(PermissionFilesRead, Scope{Path: "/tmp/notes"}),
	}
	diff := DiffPermissions(oldRequired, nil, newRequired, nil)
	if len(diff.ScopeChanged) != 1 {
		t.Fatalf("scope change = %+v", diff.ScopeChanged)
	}
	if got := diff.RequiringConsent(); len(got) != 1 {
		t.Fatalf("requiring consent = %+v", got)
	}
}

func TestDiffPermissionsOptionalBecomingRequiredRequiresConsent(t *testing.T) {
	oldOptional := []PermissionRequest{request(PermissionClipboardWrite, Scope{})}
	newRequired := []PermissionRequest{request(PermissionClipboardWrite, Scope{})}
	diff := DiffPermissions(nil, oldOptional, newRequired, nil)
	if len(diff.AddedRequired) != 1 {
		t.Fatalf("added required = %+v", diff.AddedRequired)
	}
	if len(diff.RemovedOptional) != 1 {
		t.Fatalf("removed optional = %+v", diff.RemovedOptional)
	}
	if got := diff.RequiringConsent(); len(got) != 1 {
		t.Fatalf("requiring consent = %+v", got)
	}
}
