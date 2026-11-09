package syncvault

import (
	"context"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// The original stripped lastConnectedAt before upload. Keeping it device-local
// means another device's clock can never reorder this one's Recently
// Connected list, and a sync apply cannot overwrite the local timestamp.
func TestSnapshotKeepsConnectionTelemetryOnTheDevice(t *testing.T) {
	records := &fakeRecords{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
		LastConnectedAt: 1_700_000_000_000,
		UpdatedAt:       time.Unix(1_700_000_000, 0).UTC(),
	}}}
	vault := New(records, nil)

	payload, err := vault.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(payload.Hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(payload.Hosts))
	}
	if payload.Hosts[0].LastConnectedAt != 0 {
		t.Fatalf("LastConnectedAt = %d, want it stripped from the payload",
			payload.Hosts[0].LastConnectedAt)
	}
	// The stored record keeps it; only the copy that travels is cleared.
	if records.hosts[0].LastConnectedAt == 0 {
		t.Fatal("Snapshot cleared the local record")
	}
}
