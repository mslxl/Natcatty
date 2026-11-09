package state

import (
	"context"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeCredentials struct {
	username string
	password string
	err      error
}

func (f *fakeCredentials) HostCredentials(context.Context, model.HostID) (appvault.HostCredentials, error) {
	if f.err != nil {
		return appvault.HostCredentials{}, f.err
	}
	return appvault.HostCredentials{Username: f.username, Password: f.password}, nil
}

func credentialState(t *testing.T, credentials HostCredentials) *State {
	t.Helper()
	hosts := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "10.0.0.1", Port: 2222,
		Protocol: domainvault.ProtocolSSH, Group: "prod",
	}}}
	groups := &effectiveGroups{items: []domainvault.Group{{
		Path: "prod", Username: "deploy", Port: 2222,
	}}}
	state := NewState(hosts, WithGroups(groups), WithHostCredentials(credentials))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state
}

func TestCopyCredentialsBuildsTheClipboardText(t *testing.T) {
	state := credentialState(t, &fakeCredentials{username: "deploy", password: "s3cret"})

	text, ok := state.CopyCredentials("host-1")
	if !ok {
		t.Fatalf("CopyCredentials failed: %v", state.Problem())
	}
	want := "host: 10.0.0.1:2222\nusername: deploy\npassword: s3cret"
	if text != want {
		t.Fatalf("clipboard text = %q, want %q", text, want)
	}
}

func TestCopyCredentialsEscapesAnIPv6Literal(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "2001:db8::1", Port: 2200,
		Protocol: domainvault.ProtocolSSH, Username: "root",
	}}}
	state := NewState(hosts, WithHostCredentials(&fakeCredentials{password: "s3cret"}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	text, ok := state.CopyCredentials("host-1")
	if !ok {
		t.Fatalf("CopyCredentials failed: %v", state.Problem())
	}
	if want := "host: [2001:db8::1]:2200\nusername: root\npassword: s3cret"; text != want {
		t.Fatalf("clipboard text = %q, want %q", text, want)
	}
}

func TestCopyCredentialsWithoutAPasswordReportsTheProblem(t *testing.T) {
	state := credentialState(t, &fakeCredentials{})
	if _, ok := state.CopyCredentials("host-1"); ok {
		t.Fatal("CopyCredentials succeeded without a password")
	}
	if state.Problem() != ProblemNoPassword {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemNoPassword)
	}
}

func TestCopyCredentialsWithoutASourceIsInternal(t *testing.T) {
	state := credentialState(t, nil)
	if _, ok := state.CopyCredentials("host-1"); ok {
		t.Fatal("CopyCredentials succeeded without a credential source")
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemInternal)
	}
}

func TestCopyCredentialsReportsALockedStore(t *testing.T) {
	state := credentialState(t, &fakeCredentials{err: appvault.ErrSecretsLocked})
	if _, ok := state.CopyCredentials("host-1"); ok {
		t.Fatal("CopyCredentials succeeded on a locked store")
	}
	if state.Problem() != ProblemSecretsLocked {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemSecretsLocked)
	}
}
