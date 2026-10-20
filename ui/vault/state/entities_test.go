package state

import (
	"context"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// effectiveGroups implements the Host Group use-case seam for the connection
// tests; only ListGroups takes part.
type effectiveGroups struct {
	items []domainvault.Group
}

func (f *effectiveGroups) ListGroups(context.Context) ([]domainvault.Group, error) {
	return append([]domainvault.Group(nil), f.items...), nil
}

func (f *effectiveGroups) CreateGroup(context.Context, domainvault.GroupDraft) (domainvault.Group, error) {
	return domainvault.Group{}, nil
}

func (f *effectiveGroups) UpdateGroup(context.Context, string, domainvault.GroupDraft) (domainvault.Group, error) {
	return domainvault.Group{}, nil
}

func (f *effectiveGroups) DeleteGroup(context.Context, string) error { return nil }

func (f *effectiveGroups) AssignHostGroup(context.Context, model.HostID, string) error { return nil }

// effectiveProxies implements the Proxy Profile use-case seam for the
// connection tests; only ListProxyProfiles takes part.
type effectiveProxies struct {
	items []domainvault.ProxyProfile
}

func (f *effectiveProxies) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	return append([]domainvault.ProxyProfile(nil), f.items...), nil
}

func (f *effectiveProxies) CreateProxyProfile(context.Context, domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	return domainvault.ProxyProfile{}, nil
}

func (f *effectiveProxies) UpdateProxyProfile(context.Context, string, domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error) {
	return domainvault.ProxyProfile{}, nil
}

func (f *effectiveProxies) DeleteProxyProfile(context.Context, string) error { return nil }

// TestConnectAndTestCarryTheEffectiveHost proves both connection actions use
// the same resolved view the list shows: group defaults fill the stored Host's
// empty fields and the selected Proxy Profile is materialized, while the
// stored Host stays unchanged.
func TestConnectAndTestCarryTheEffectiveHost(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "10.0.0.1",
		Protocol: domainvault.ProtocolSSH, Group: "prod",
	}}}
	groups := &effectiveGroups{items: []domainvault.Group{{
		Path: "prod", Username: "deploy", Port: 2200, ProxyProfileID: "proxy-1",
	}}}
	proxies := &effectiveProxies{items: []domainvault.ProxyProfile{{
		ID: "proxy-1", Label: "office",
		Config: domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080,
		},
	}}}

	var connected domainvault.Host
	tested := make(chan domainvault.Host, 1)
	state := NewState(hosts,
		WithGroups(groups),
		WithProxyProfiles(proxies),
		WithConnect(func(host domainvault.Host) { connected = host }),
		WithConnectionTest(func(_ context.Context, host domainvault.Host) error {
			tested <- host
			return nil
		}),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if !state.Connect("host-1") {
		t.Fatalf("Connect rejected a listed Host: %v", state.Problem())
	}
	assertEffectiveConnectionHost(t, connected)
	if raw := hosts.items[0]; raw.Username != "" || raw.Port != 0 || raw.Proxy != nil {
		t.Fatalf("the stored Host changed: %+v", raw)
	}

	if !state.Test("host-1") {
		t.Fatalf("Test rejected a listed Host: %v", state.Problem())
	}
	select {
	case got := <-tested:
		assertEffectiveConnectionHost(t, got)
	case <-time.After(2 * time.Second):
		t.Fatal("Test never reached the connection seam")
	}
	waitForTestOutcome(t, state, "host-1")
}

func assertEffectiveConnectionHost(t *testing.T, host domainvault.Host) {
	t.Helper()
	if host.Username != "deploy" || host.Port != 2200 {
		t.Fatalf("effective Host = %+v, want the group defaults", host)
	}
	if host.Proxy == nil || host.Proxy.Type != domainvault.ProxySOCKS5 ||
		host.Proxy.Host != "socks.example.com" || host.Proxy.Port != 1080 {
		t.Fatalf("effective Proxy = %+v, want the selected profile", host.Proxy)
	}
}
