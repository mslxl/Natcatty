package store

import (
	"context"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestNetworkProfilesPersistAcrossReopenAndDelete(t *testing.T) {
	ctx := context.Background()
	store, path := openTestStore(t)
	when := time.Unix(1700000000, 0).UTC()
	tailscale := domainvault.NetworkProfile{
		ID: "network-1", Label: "tailnet",
		Kind: domainvault.NetworkKindTailscale,
		Tailscale: &domainvault.TailscaleNetwork{
			Hostname: "app-node", ControlURL: "https://headscale.example.com",
			Ephemeral: true, Tags: []string{"tag:prod"}, HasAuthKey: true,
		},
		OrderKey: 1000, CreatedAt: when, UpdatedAt: when,
	}
	wireguard := domainvault.NetworkProfile{
		ID: "network-2", Label: "wg",
		Kind: domainvault.NetworkKindWireGuard,
		WireGuard: &domainvault.WireGuardNetwork{
			Addresses: []string{"10.0.0.2/32"}, DNS: []string{"10.0.0.1"}, MTU: 1280,
			HasPrivateKey: true,
			Peers: []domainvault.WireGuardPeer{{
				ID: "peer-1", PublicKey: "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
				Endpoint: "vpn.example.com:51820", AllowedIPs: []string{"10.0.0.0/24"},
				PersistentKeepalive: 25, HasPresharedKey: true,
			}},
		},
		OrderKey: 2000, CreatedAt: when, UpdatedAt: when,
	}
	for _, profile := range []domainvault.NetworkProfile{wireguard, tailscale} {
		if err := store.SaveNetworkProfile(ctx, profile); err != nil {
			t.Fatalf("SaveNetworkProfile: %v", err)
		}
	}

	loaded, err := store.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatalf("ListNetworkProfiles: %v", err)
	}
	if len(loaded) != 2 || loaded[0].ID != "network-1" || loaded[1].ID != "network-2" {
		t.Fatalf("profiles = %+v, want OrderKey order", loaded)
	}
	if loaded[0].Tailscale == nil || !loaded[0].Tailscale.HasAuthKey ||
		loaded[0].Tailscale.ControlURL != "https://headscale.example.com" {
		t.Fatalf("Tailscale profile = %+v", loaded[0].Tailscale)
	}
	if loaded[1].WireGuard == nil || !loaded[1].WireGuard.HasPrivateKey ||
		loaded[1].WireGuard.MTU != 1280 || len(loaded[1].WireGuard.Peers) != 1 ||
		!loaded[1].WireGuard.Peers[0].HasPresharedKey {
		t.Fatalf("WireGuard profile = %+v", loaded[1].WireGuard)
	}

	// A phonebook-style restart is a fresh Store over the same file.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after restart: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.GetNetworkProfile(ctx, "network-2")
	if err != nil {
		t.Fatalf("GetNetworkProfile after restart: %v", err)
	}
	if persisted.Label != "wg" || persisted.WireGuard == nil ||
		persisted.WireGuard.Peers[0].PublicKey != wireguard.WireGuard.Peers[0].PublicKey {
		t.Fatalf("profile after restart = %+v", persisted)
	}

	if err := reopened.DeleteNetworkProfile(ctx, "network-2"); err != nil {
		t.Fatalf("DeleteNetworkProfile: %v", err)
	}
	if _, err := reopened.GetNetworkProfile(ctx, "network-2"); err == nil {
		t.Fatal("deleted profile is still readable")
	}
	if err := reopened.DeleteNetworkProfile(ctx, "missing"); err == nil {
		t.Fatal("deleting a missing profile succeeded")
	}
}
