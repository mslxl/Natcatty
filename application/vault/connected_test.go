package vault

import (
	"context"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// MarkConnected records the Vault's Recently Connected ordering without
// turning a connection into an edit: UpdatedAt must not move, or the Host
// would win conflicts and jump the modification-order lists.
func TestMarkConnectedRecordsTelemetryWithoutTouchingUpdatedAt(t *testing.T) {
	updated := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &fakeHosts{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, UpdatedAt: updated,
	}}}
	service := newEntityService(t, repo, &fakeGroups{}, &fakeProxies{}, &fakeKnownHosts{})

	if err := service.MarkConnected(context.Background(), "host-1"); err != nil {
		t.Fatalf("MarkConnected: %v", err)
	}
	saved := repo.hosts[0]
	if saved.LastConnectedAt == 0 {
		t.Fatal("LastConnectedAt was not recorded")
	}
	if !saved.UpdatedAt.Equal(updated) {
		t.Fatalf("UpdatedAt = %v, want the stored %v", saved.UpdatedAt, updated)
	}
}

func TestMarkConnectedReportsAMissingHost(t *testing.T) {
	service := newEntityService(t, &fakeHosts{}, &fakeGroups{}, &fakeProxies{}, &fakeKnownHosts{})
	if err := service.MarkConnected(context.Background(), "gone"); err != ErrNotFound {
		t.Fatalf("MarkConnected(gone) = %v, want ErrNotFound", err)
	}
}
