package app

import (
	"context"
	"errors"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeResolver struct {
	resolved appvault.ResolvedAuth
	err      error
	hostID   model.HostID
}

func (f *fakeResolver) ResolveAuth(_ context.Context, hostID model.HostID) (appvault.ResolvedAuth, error) {
	f.hostID = hostID
	return f.resolved, f.err
}

func authRequest() appconnection.Request {
	return appconnection.Request{HostID: "host-1", Hostname: "example.com", Port: 22, Username: "fallback"}
}

func TestAuthSourceUsesTheResolvedPasswordWithoutAskingTheVaultAgain(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{
			Username: "alice", Method: domainvault.HostAuthPassword,
		},
		Password: "s3cret",
	}}
	plan, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	if resolver.hostID != "host-1" {
		t.Fatalf("resolved HostID = %q, want host-1", resolver.hostID)
	}
	if plan.Mode != appconnection.AuthModePassword || plan.Username != "alice" || plan.Password != "s3cret" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Candidates) != 2 || plan.Candidates[0].Kind != appconnection.AuthKindPassword ||
		plan.Candidates[1].Kind != appconnection.AuthKindKeyboardInteractive {
		t.Fatalf("password-mode candidates = %+v", plan.Candidates)
	}
	if plan.FailClosed {
		t.Fatal("password mode reported fail-closed key behavior")
	}
}

func TestAuthSourceForwardsTheSelectedKeyAndFailsClosed(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{Username: "deploy", Method: domainvault.HostAuthKey},
		Password:  "fallback",
		Key: &appvault.ResolvedKey{
			ID: "key-1", Label: "work", PrivateKey: "PRIVATE", Passphrase: "pass",
		},
	}}
	plan, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	if plan.Mode != appconnection.AuthModeKey || !plan.FailClosed {
		t.Fatalf("plan mode = %q failClosed = %v", plan.Mode, plan.FailClosed)
	}
	first := plan.Candidates[0]
	if first.Kind != appconnection.AuthKindPublicKey || !first.Primary || first.Key == nil {
		t.Fatalf("first candidate = %+v", first)
	}
	if first.Key.PrivateKey != "PRIVATE" || first.Key.Passphrase != "pass" || first.Key.Label != "work" {
		t.Fatalf("key material = %+v", first.Key)
	}
}

func TestAuthSourceFailsClosedWhenTheSelectedKeyIsMissing(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection:  domainvault.AuthSelection{Username: "deploy", Method: domainvault.HostAuthKey},
		Password:   "fallback",
		MissingKey: true,
	}}
	_, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if err == nil {
		t.Fatal("AuthPlan succeeded despite a missing selected Key")
	}
	if !errors.Is(err, appconnection.ErrKeyUnavailable) {
		t.Fatalf("error = %v, want ErrKeyUnavailable", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("error = %v, want a typed auth failure", err)
	}
}

func TestAuthSourceUsesAgentBeforeTheSelectedKeyInAutomaticMode(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{
			Username: "root", Method: domainvault.HostAuthAuto,
			UseSSHAgent: true, RequiresMFA: false,
		},
		Password: "pw",
		Key:      &appvault.ResolvedKey{ID: "key-1", Label: "work", PrivateKey: "PRIVATE"},
	}}
	plan, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	if plan.Mode != appconnection.AuthModeAuto {
		t.Fatalf("mode = %q, want auto", plan.Mode)
	}
	want := []appconnection.AuthCandidateKind{
		appconnection.AuthKindAgent,
		appconnection.AuthKindPublicKey,
		appconnection.AuthKindPassword,
		appconnection.AuthKindKeyboardInteractive,
	}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("candidates = %+v, want %v", plan.Candidates, want)
	}
	for i := range want {
		if plan.Candidates[i].Kind != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, plan.Candidates[i].Kind, want[i])
		}
	}
}

func TestAuthSourceFailsClosedWhenCertificateModeHasNoCertificate(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{Username: "deploy", Method: domainvault.HostAuthCertificate},
		Key:       &appvault.ResolvedKey{ID: "key-1", Label: "work", PrivateKey: "PRIVATE"},
		Password:  "fallback",
	}}
	_, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if !errors.Is(err, appconnection.ErrKeyUnavailable) {
		t.Fatalf("error = %v, want ErrKeyUnavailable", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("error = %v, want a typed auth failure", err)
	}
}

func TestAuthSourceCertificateModeForwardsTheCertifiedKey(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{Username: "deploy", Method: domainvault.HostAuthCertificate},
		Key: &appvault.ResolvedKey{
			ID: "key-1", Label: "work", PrivateKey: "PRIVATE", Certificate: "CERT",
		},
	}}
	plan, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	if plan.Mode != appconnection.AuthModeCertificate || !plan.FailClosed {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Candidates) == 0 || plan.Candidates[0].Key == nil ||
		plan.Candidates[0].Key.Certificate != "CERT" {
		t.Fatalf("candidates = %+v, want the certified key first", plan.Candidates)
	}
}

type fakeDefaults struct {
	keys []appconnection.KeyMaterial
}

func (f fakeDefaults) DefaultKeys() []appconnection.KeyMaterial { return f.keys }

func TestAuthSourceAddsDefaultKeysAfterTheSelectedKeyOnlyInAutomaticMode(t *testing.T) {
	defaults := fakeDefaults{keys: []appconnection.KeyMaterial{
		{ID: "default:id_ed25519", Label: "id_ed25519", PrivateKey: "DEFAULT"},
	}}
	auto := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{Username: "root", Method: domainvault.HostAuthAuto},
		Key:       &appvault.ResolvedKey{ID: "key-1", Label: "work", PrivateKey: "PRIVATE"},
	}}
	plan, err := (vaultAuthSource{vault: auto, defaults: defaults}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	var kinds []appconnection.AuthCandidateKind
	for _, candidate := range plan.Candidates {
		kinds = append(kinds, candidate.Kind)
	}
	want := []appconnection.AuthCandidateKind{
		appconnection.AuthKindPublicKey,
		appconnection.AuthKindPublicKey,
		appconnection.AuthKindKeyboardInteractive,
	}
	if len(kinds) != len(want) {
		t.Fatalf("candidates = %+v, want %v", plan.Candidates, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, kinds[i], want[i])
		}
	}
	if plan.Candidates[1].Key == nil || plan.Candidates[1].Key.Label != "id_ed25519" {
		t.Fatalf("default candidate = %+v", plan.Candidates[1])
	}

	password := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection: domainvault.AuthSelection{Username: "root", Method: domainvault.HostAuthPassword},
		Password:  "pw",
	}}
	plan, err = (vaultAuthSource{vault: password, defaults: defaults}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	for _, candidate := range plan.Candidates {
		if candidate.Kind == appconnection.AuthKindPublicKey {
			t.Fatalf("password mode offered a default key: %+v", plan.Candidates)
		}
	}
}

func TestAuthSourceDoesNotFailClosedForAMissingKeyInAutomaticMode(t *testing.T) {
	resolver := &fakeResolver{resolved: appvault.ResolvedAuth{
		Selection:  domainvault.AuthSelection{Username: "root", Method: domainvault.HostAuthAuto},
		Password:   "fallback",
		MissingKey: true,
	}}
	plan, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
	if err != nil {
		t.Fatalf("AuthPlan: %v", err)
	}
	if plan.Mode != appconnection.AuthModeAuto || plan.FailClosed {
		t.Fatalf("plan = %+v, want automatic fallback", plan)
	}
}

func TestAuthSourceMapsVaultFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind appconnection.Kind
	}{
		{"missing Host", appvault.ErrNotFound, appconnection.KindAuth},
		{"locked secret store", &appvault.Error{Kind: appvault.KindPermission, Cause: appvault.ErrSecretsLocked}, appconnection.KindInternal},
		{"storage failure", errors.New("disk"), appconnection.KindInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &fakeResolver{err: test.err}
			_, err := (vaultAuthSource{vault: resolver}).AuthPlan(context.Background(), authRequest())
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != test.kind {
				t.Fatalf("error = %v, want kind %q", err, test.kind)
			}
			if test.kind == appconnection.KindAuth && !errors.Is(err, appconnection.ErrNoCredentials) {
				t.Fatalf("error = %v, want ErrNoCredentials", err)
			}
		})
	}
}
