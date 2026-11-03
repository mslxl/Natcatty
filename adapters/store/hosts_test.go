package store

import (
	"context"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestHostAutoAttachTmuxPersists proves the opt-in tmux modifier survives the
// store's JSON record round-trip. The setting is a Host field, not a global
// preference, so every load must return the saved value.
func TestHostAutoAttachTmuxPersists(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	host := domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, AutoAttachTmux: true,
	}
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Get(ctx, host.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !loaded.AutoAttachTmux {
		t.Fatalf("loaded Host lost autoAttachTmux: %+v", loaded)
	}

	host.AutoAttachTmux = false
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save off: %v", err)
	}
	loaded, err = store.Get(ctx, host.ID)
	if err != nil {
		t.Fatalf("Get after disabling: %v", err)
	}
	if loaded.AutoAttachTmux {
		t.Fatalf("disabling autoAttachTmux did not persist: %+v", loaded)
	}
}
