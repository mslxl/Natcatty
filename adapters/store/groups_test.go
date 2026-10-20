package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestHostGroupsPersistAndReorder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "natcatty.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0).UTC()
	groups := []domainvault.Group{
		{Path: "prod/web", Username: "deploy", Port: 2222,
			Protocol: domainvault.ProtocolSSH, AuthMethod: domainvault.HostAuthPassword,
			UseSSHAgent: true, ProxyProfileID: "proxy-1", OrderKey: 2000,
			CreatedAt: when, UpdatedAt: when},
		{Path: "prod", Notes: "production", OrderKey: 1000,
			CreatedAt: when, UpdatedAt: when},
	}
	for _, group := range groups {
		if err := first.SaveGroup(ctx, group); err != nil {
			t.Fatalf("SaveGroup(%s): %v", group.Path, err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	loaded, err := second.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Path != "prod" || loaded[1].Path != "prod/web" {
		t.Fatalf("groups = %+v, want order prod then prod/web", loaded)
	}
	if loaded[1].Username != "deploy" || loaded[1].Port != 2222 ||
		loaded[1].AuthMethod != domainvault.HostAuthPassword || !loaded[1].UseSSHAgent ||
		loaded[1].ProxyProfileID != "proxy-1" {
		t.Fatalf("prod/web = %+v", loaded[1])
	}

	loaded[1].Username = "ops"
	if err := second.SaveGroup(ctx, loaded[1]); err != nil {
		t.Fatalf("update SaveGroup: %v", err)
	}
	got, err := second.GetGroup(ctx, "prod/web")
	if err != nil || got.Username != "ops" {
		t.Fatalf("GetGroup = %+v, %v", got, err)
	}

	if err := second.DeleteGroup(ctx, "prod/web"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, err := second.GetGroup(ctx, "prod/web"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("deleted group error = %v, want ErrNotFound", err)
	}
	if _, err := second.GetGroup(ctx, "prod"); err != nil {
		t.Fatalf("sibling group was removed: %v", err)
	}
}

func TestDeletingAGroupDoesNotDeleteItsHosts(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	when := time.Unix(1700000000, 0).UTC()

	if err := store.SaveGroup(ctx, domainvault.Group{Path: "prod", CreatedAt: when, UpdatedAt: when}); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	host := domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, Group: "prod", CreatedAt: when, UpdatedAt: when,
	}
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.DeleteGroup(ctx, "prod"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	stored, err := store.Get(ctx, "host-1")
	if err != nil {
		t.Fatalf("Host after group delete: %v", err)
	}
	// The adapter removes only the group row; the service decides what to do
	// with the Host's soft reference.
	if stored.ID != host.ID {
		t.Fatalf("stored Host = %+v", stored)
	}
}

func TestProxyProfilesPersistAndDeleteIndependentlyOfHosts(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	when := time.Unix(1700000000, 0).UTC()
	profile := domainvault.ProxyProfile{
		ID: "proxy-1", Label: "office",
		Config: domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080,
		},
		OrderKey: 1000, CreatedAt: when, UpdatedAt: when,
	}
	if err := store.SaveProxyProfile(ctx, profile); err != nil {
		t.Fatalf("SaveProxyProfile: %v", err)
	}
	host := domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "web.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, ProxyProfileID: profile.ID,
		CreatedAt: when, UpdatedAt: when,
	}
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.ListProxyProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProxyProfiles: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Config.Type != domainvault.ProxySOCKS5 ||
		loaded[0].Config.Host != "socks.example.com" || loaded[0].Config.Port != 1080 {
		t.Fatalf("profiles = %+v", loaded)
	}
	if got, err := store.GetProxyProfile(ctx, profile.ID); err != nil || got.Label != "office" {
		t.Fatalf("GetProxyProfile = %+v, %v", got, err)
	}

	profile.Label = "office vpn"
	if err := store.SaveProxyProfile(ctx, profile); err != nil {
		t.Fatalf("update SaveProxyProfile: %v", err)
	}
	if got, _ := store.GetProxyProfile(ctx, profile.ID); got.Label != "office vpn" {
		t.Fatalf("profile label = %q", got.Label)
	}

	if err := store.DeleteProxyProfile(ctx, profile.ID); err != nil {
		t.Fatalf("DeleteProxyProfile: %v", err)
	}
	if _, err := store.GetProxyProfile(ctx, profile.ID); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("deleted profile error = %v, want ErrNotFound", err)
	}
	stored, err := store.Get(ctx, "host-1")
	if err != nil {
		t.Fatalf("Host after profile delete: %v", err)
	}
	if stored.ProxyProfileID != profile.ID {
		t.Fatalf("ProxyProfileID = %q, want the soft reference preserved", stored.ProxyProfileID)
	}
}
