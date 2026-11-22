package store

import (
	"context"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestHostTransportDecodeCanonicalization proves a row written by an older
// build, whose protocol and transport fields were not canonicalized, is still
// read with the values the current model expects. The stored bytes are not
// rewritten; only the decoded Host is canonicalized.
func TestHostTransportDecodeCanonicalization(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	hosts := []domainvault.Host{
		// An older row could carry a blank protocol, which means SSH.
		{ID: "blank", Label: "blank", Hostname: "blank.example", Port: 22},
		// The input spelling "ssh" means plain SSH.
		{ID: "spelled", Label: "spelled", Hostname: "spelled.example", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: domainvault.Transport("ssh")},
		{ID: "mosh", Label: "mosh", Hostname: "mosh.example", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: domainvault.TransportMosh},
		{ID: "et", Label: "et", Hostname: "et.example", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: domainvault.TransportET},
	}
	for _, host := range hosts {
		if err := store.Save(ctx, host); err != nil {
			t.Fatalf("Save %s: %v", host.ID, err)
		}
	}
	want := map[string]domainvault.Transport{
		"blank":   domainvault.TransportPlain,
		"spelled": domainvault.TransportPlain,
		"mosh":    domainvault.TransportMosh,
		"et":      domainvault.TransportET,
	}
	for id, transport := range want {
		loaded, err := store.Get(ctx, model.HostID(id))
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if loaded.Protocol != domainvault.ProtocolSSH {
			t.Errorf("%s protocol = %q, want ssh", id, loaded.Protocol)
		}
		if loaded.Transport != transport {
			t.Errorf("%s transport = %q, want %q", id, loaded.Transport, transport)
		}
	}
}

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
