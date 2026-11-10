package plugin_test

import (
	"errors"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func TestAuthorityIssuesHandlesOnlyForExactScopes(t *testing.T) {
	authority := appplugin.NewAuthority()
	scope := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"}
	authority.Grant(installedSubject("acme.notes"), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionFilesRead,
		Scope:      scope,
	})

	handle, err := authority.Issue(installedSubject("acme.notes"), domainplugin.PermissionFilesRead, scope)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := handle.Check(); err != nil {
		t.Fatalf("Check fresh handle: %v", err)
	}
	if err := authority.Check(installedSubject("acme.notes"), domainplugin.PermissionFilesRead,
		domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/other"}); !errors.Is(err, appplugin.ErrScopeDenied) {
		t.Fatalf("Check wrong scope = %v, want ErrScopeDenied", err)
	}
	if err := authority.Check(installedSubject("acme.notes"), domainplugin.PermissionFilesWrite,
		scope); !errors.Is(err, appplugin.ErrPermissionNotGranted) {
		t.Fatalf("Check missing grant = %v, want ErrPermissionNotGranted", err)
	}
}

func TestAuthorityRevocationInvalidatesEveryIssuedHandleImmediately(t *testing.T) {
	authority := appplugin.NewAuthority()
	files := domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: "/tmp/notes"}
	host := domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: "host-1"}
	authority.Grant(installedSubject("acme.notes"), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionFilesRead, Scope: files,
	})
	authority.Grant(installedSubject("acme.notes"), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionHostsRead, Scope: host,
	})
	filesHandle, err := authority.Issue(installedSubject("acme.notes"), domainplugin.PermissionFilesRead, files)
	if err != nil {
		t.Fatalf("Issue files: %v", err)
	}
	hostHandle, err := authority.Issue(installedSubject("acme.notes"), domainplugin.PermissionHostsRead, host)
	if err != nil {
		t.Fatalf("Issue hosts: %v", err)
	}
	epoch := authority.Epoch(installedSubject("acme.notes"))

	if !authority.Revoke(installedSubject("acme.notes"), domainplugin.PermissionFilesRead) {
		t.Fatal("Revoke reported no grant")
	}
	if authority.Epoch(installedSubject("acme.notes")) == epoch {
		t.Fatal("Revoke did not advance the authority epoch")
	}
	var revoked *appplugin.CapabilityRevokedError
	if err := filesHandle.Check(); !errors.As(err, &revoked) {
		t.Fatalf("files handle after revoke = %v, want CapabilityRevokedError", err)
	}
	// The epoch is per plugin: revoking one permission invalidates every
	// handle, including one for another permission.
	if err := hostHandle.Check(); !errors.As(err, &revoked) {
		t.Fatalf("hosts handle after revoke = %v, want CapabilityRevokedError", err)
	}
}

func TestAuthorityPendingScopeAuthorizesNothing(t *testing.T) {
	authority := appplugin.NewAuthority()
	authority.Grant(installedSubject("acme.db"), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionProviderData,
		Pending:    true,
	})
	err := authority.Check(installedSubject("acme.db"), domainplugin.PermissionProviderData,
		domainplugin.Scope{Kind: domainplugin.ScopeProvider, Provider: "acme.db", Access: "read-only"})
	if !errors.Is(err, appplugin.ErrScopeRequired) {
		t.Fatalf("Check pending grant = %v, want ErrScopeRequired", err)
	}
}

func TestAuthoritySetGrantsStartsAFreshEpoch(t *testing.T) {
	authority := appplugin.NewAuthority()
	scope := domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: "host-1"}
	authority.Grant(installedSubject("acme.notes"), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionHostsRead, Scope: scope,
	})
	handle, err := authority.Issue(installedSubject("acme.notes"), domainplugin.PermissionHostsRead, scope)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	authority.SetGrants(installedSubject("acme.notes"), nil)
	var revoked *appplugin.CapabilityRevokedError
	if err := handle.Check(); !errors.As(err, &revoked) {
		t.Fatalf("handle after SetGrants = %v, want CapabilityRevokedError", err)
	}
}

func installedSubject(id string) appplugin.AuthoritySubject {
	return appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, id)
}
