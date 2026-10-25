package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeTransportRoute struct {
	route appconnection.Route
	err   error
}

func (f fakeTransportRoute) Route(context.Context, model.HostID) (appconnection.Route, error) {
	return f.route, f.err
}

func TestMoshRejectsAJumpChainBeforeOpeningAnything(t *testing.T) {
	factory := &moshTransportFactory{
		routes: fakeTransportRoute{route: appconnection.Route{
			Jumps: []appconnection.Hop{{HostID: "jump", Hostname: "jump.example"}},
		}},
	}
	cfg := moshmodel.Config{Hostname: "mosh.example", Port: 22}.Normalized()
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, Hostname: "mosh.example", Port: 22, Mosh: &cfg,
	})
	if !errors.Is(err, moshmodel.ErrJumpUnsupported) {
		t.Fatalf("Open = %v, want a typed jump restriction", err)
	}
}

func TestMoshRejectsAProxyBeforeOpeningAnything(t *testing.T) {
	factory := &moshTransportFactory{
		routes: fakeTransportRoute{route: appconnection.Route{
			Proxy: &appconnection.Proxy{Type: appconnection.ProxySOCKS5, Host: "proxy"},
		}},
	}
	cfg := moshmodel.Config{Hostname: "mosh.example", Port: 22}.Normalized()
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, Hostname: "mosh.example", Port: 22, Mosh: &cfg,
	})
	if !errors.Is(err, moshmodel.ErrProxyUnsupported) {
		t.Fatalf("Open = %v, want a typed proxy restriction", err)
	}
}

func TestKnownHostsContentRendersVaultPins(t *testing.T) {
	content := knownHostsContent([]domainconn.KnownHost{
		{Hostname: "web.example", Port: 22, PublicKey: "ssh-ed25519 AAAAWEB"},
		{Hostname: "db.example", Port: 2222, PublicKey: "ssh-rsa AAAADB"},
		{Hostname: "web.example", PublicKey: "SHA256:only-a-fingerprint"},
		{Hostname: "", PublicKey: "ssh-ed25519 AAAAEMPTY"},
	})
	want := "web.example ssh-ed25519 AAAAWEB\n[db.example]:2222 ssh-rsa AAAADB\n"
	if content != want {
		t.Fatalf("knownHostsContent =\n%q\nwant\n%q", content, want)
	}
}

type fakeETVault struct {
	hosts  map[string]domainvault.Host
	auth   map[string]appvault.ResolvedAuth
	known  []domainconn.KnownHost
	getErr error
}

func (f fakeETVault) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	if f.getErr != nil {
		return domainvault.Host{}, f.getErr
	}
	host, ok := f.hosts[string(id)]
	if !ok {
		return domainvault.Host{}, errors.New("missing host")
	}
	return host, nil
}

func (f fakeETVault) ResolveAuth(_ context.Context, id model.HostID) (appvault.ResolvedAuth, error) {
	return f.auth[string(id)], nil
}

func (f fakeETVault) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return f.known, nil
}

func TestETAuthUsesTheResolvedIdentityAndFailsClosedOnAMissingKey(t *testing.T) {
	vault := fakeETVault{auth: map[string]appvault.ResolvedAuth{
		"host": {
			Selection: domainvault.AuthSelection{Username: "alice", Method: domainvault.HostAuthAuto},
			Password:  "secret",
			Key: &appvault.ResolvedKey{
				PrivateKey: "KEY", Passphrase: "PASS", Certificate: "CERT",
			},
		},
		"missing": {
			Selection:  domainvault.AuthSelection{Username: "bob", Method: domainvault.HostAuthKey},
			MissingKey: true,
		},
	}}
	factory := &etTransportFactory{vault: vault}
	resolved, err := factory.auth(context.Background(), "host")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if resolved.username != "alice" {
		t.Fatalf("username = %q, want the resolved identity username", resolved.username)
	}
	if resolved.auth.Method != etmodel.AuthAuto || resolved.auth.Password != "secret" ||
		resolved.auth.PrivateKey != "KEY" || resolved.auth.Passphrase != "PASS" ||
		resolved.auth.Certificate != "CERT" {
		t.Fatalf("auth = %+v", resolved.auth)
	}
	if _, err := factory.auth(context.Background(), "missing"); err == nil {
		t.Fatal("a dangling Key reference did not fail closed")
	} else {
		var typed *appconnection.Error
		if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
			t.Fatalf("missing key error = %v, want a typed auth failure", err)
		}
	}
}

func TestETRejectsAnUnsupportedRouteBeforeStartingAProcess(t *testing.T) {
	hosts := map[string]domainvault.Host{
		"host": {ID: "host", Protocol: domainvault.ProtocolET, Hostname: "et.example", Port: 22, ETPort: 8737},
	}
	vault := fakeETVault{hosts: hosts, auth: map[string]appvault.ResolvedAuth{
		"host": {Selection: domainvault.AuthSelection{Username: "alice", Method: domainvault.HostAuthAuto}},
	}}
	cases := []struct {
		name  string
		route appconnection.Route
		want  error
	}{
		{"proxy", appconnection.Route{Proxy: &appconnection.Proxy{Type: appconnection.ProxyHTTP, Host: "proxy"}}, etmodel.ErrProxyUnsupported},
		{"two jumps", appconnection.Route{Jumps: []appconnection.Hop{
			{HostID: "j1", Hostname: "j1.example"}, {HostID: "j2", Hostname: "j2.example"},
		}}, etmodel.ErrMultipleJumpHosts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory := &etTransportFactory{vault: vault, routes: fakeTransportRoute{route: tc.route}}
			cfg := etmodel.Config{Hostname: "et.example", Port: 22}.Normalized()
			_, err := factory.Open(context.Background(), appsession.StartSpec{
				Kind: appsession.KindET, HostID: "host", Hostname: "et.example", Port: 22, ET: &cfg,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestETEnrichesTheConfigFromTheVaultAndOneJump(t *testing.T) {
	hosts := map[string]domainvault.Host{
		"host": {ID: "host", Protocol: domainvault.ProtocolET, Hostname: "et.example", Port: 2222, ETPort: 8737},
		"jump": {ID: "jump", Protocol: domainvault.ProtocolET, Hostname: "jump.example", Port: 2022, ETPort: 3022,
			LegacyAlgorithms: true},
	}
	vault := fakeETVault{
		hosts: hosts,
		auth: map[string]appvault.ResolvedAuth{
			"host": {Selection: domainvault.AuthSelection{Username: "alice", Method: domainvault.HostAuthAuto}, Password: "target"},
			"jump": {Selection: domainvault.AuthSelection{Username: "jumper", Method: domainvault.HostAuthPassword}, Password: "jump"},
		},
		known: []domainconn.KnownHost{
			{Hostname: "et.example", Port: 2222, PublicKey: "ssh-ed25519 AAAAET"},
		},
	}
	factory := &etTransportFactory{vault: vault}
	cfg := etmodel.Config{Hostname: "et.example", Port: 2222}.Normalized()
	route := appconnection.Route{
		Jumps: []appconnection.Hop{{HostID: "jump", Hostname: "jump.example", Port: 2022, Username: "ignored"}},
	}
	enriched, err := factory.configure(context.Background(), appsession.StartSpec{
		Kind: appsession.KindET, HostID: "host", Hostname: "et.example", Port: 2222, ET: &cfg,
	}, cfg, route)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if enriched.ETPort != 8737 || enriched.Username != "alice" || enriched.Auth.Password != "target" {
		t.Fatalf("target config = %+v", enriched)
	}
	if !enriched.VerifyHostKeys || !strings.Contains(enriched.KnownHostsContent, "[et.example]:2222 ssh-ed25519 AAAAET") {
		t.Fatalf("known-hosts snapshot = %q", enriched.KnownHostsContent)
	}
	if enriched.Jump == nil || enriched.Jump.Hostname != "jump.example" || enriched.Jump.Port != 2022 ||
		enriched.Jump.ETPort != 3022 || enriched.Jump.Username != "jumper" ||
		enriched.Jump.Auth.Password != "jump" || !enriched.Jump.LegacyAlgorithms {
		t.Fatalf("jump = %+v", enriched.Jump)
	}
	if enriched.JumpKnownHostsContent != enriched.KnownHostsContent {
		t.Fatal("the jump did not receive the same known-hosts snapshot")
	}
}
