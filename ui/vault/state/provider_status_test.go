package state

import (
	"sync/atomic"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

func TestNetworkProviderStatusIsQueuedUntilFlushed(t *testing.T) {
	var redraws atomic.Int32
	state := NewState(nil, WithRedrawRequest(func() { redraws.Add(1) }))

	ips := []string{"100.64.0.2"}
	state.PostNetworkProviderStatus(NetworkProviderStatus{
		ProfileID:    "net-1",
		Label:        "Office",
		Kind:         "tailscale",
		Phase:        appconnection.ProviderPhaseStarting,
		TailscaleIPs: ips,
	})
	if _, ok := state.NetworkStatus("net-1"); ok {
		t.Fatal("a queued status was visible before the UI thread flushed it")
	}
	if redraws.Load() == 0 {
		t.Fatal("posting a status did not request a redraw")
	}

	// The queue owns a copy: mutating the caller's slice after posting must not
	// change the published status.
	ips[0] = "changed"
	state.FlushNetworkProviderStatus()
	status, ok := state.NetworkStatus("net-1")
	if !ok {
		t.Fatal("the flushed status is not available")
	}
	if status.Phase != appconnection.ProviderPhaseStarting {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if len(status.TailscaleIPs) != 1 || status.TailscaleIPs[0] != "100.64.0.2" {
		t.Fatalf("addresses = %v, want the posted copy", status.TailscaleIPs)
	}

	// A later event for the same profile replaces the row.
	state.PostNetworkProviderStatus(NetworkProviderStatus{
		ProfileID: "net-1",
		Label:     "Office",
		Kind:      "tailscale",
		Phase:     appconnection.ProviderPhaseRunning,
	})
	state.FlushNetworkProviderStatus()
	status, ok = state.NetworkStatus("net-1")
	if !ok || status.Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase = %q, want the latest running status", status.Phase)
	}
}

func TestNetworkProviderStatusAddressPrefersDNSThenFirstIP(t *testing.T) {
	tests := []struct {
		name   string
		status NetworkProviderStatus
		want   string
	}{
		{"dns name", NetworkProviderStatus{
			DNSName: "office.tailnet.ts.net", TailscaleIPs: []string{"100.64.0.2"},
		}, "office.tailnet.ts.net"},
		{"first ip", NetworkProviderStatus{
			TailscaleIPs: []string{"100.64.0.2", "fd7a::2"},
		}, "100.64.0.2"},
		{"wireguard endpoint", NetworkProviderStatus{
			WireGuardEndpoint: "203.0.113.7:51820",
		}, "203.0.113.7:51820"},
		{"dns name beats wireguard endpoint", NetworkProviderStatus{
			DNSName: "office.tailnet.ts.net", WireGuardEndpoint: "203.0.113.7:51820",
		}, "office.tailnet.ts.net"},
		{"none", NetworkProviderStatus{}, ""},
	}
	for _, test := range tests {
		if got := test.status.Address(); got != test.want {
			t.Errorf("%s Address = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNetworkProviderStatusIgnoresAnEmptyProfile(t *testing.T) {
	state := NewState(nil)
	state.PostNetworkProviderStatus(NetworkProviderStatus{Phase: appconnection.ProviderPhaseRunning})
	state.FlushNetworkProviderStatus()
	if len(state.networkStatuses) != 0 {
		t.Fatalf("statuses = %+v, want none without a profile id", state.networkStatuses)
	}
}
