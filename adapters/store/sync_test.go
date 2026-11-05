package store

import (
	"context"
	"testing"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestApplySyncReplacesSyncedFamiliesAndPreservesLocalOnes(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.UnixMilli(1_700_000_000_000)

	if err := store.Save(ctx, testHost("old-host", "old")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGroup(ctx, domainvault.Group{Path: "old-group", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKnownHost(ctx, domainconn.KnownHost{
		ID: "known-1", Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "abc",
	}); err != nil {
		t.Fatal(err)
	}

	payload := appsync.Payload{
		Hosts: []domainvault.Host{{
			ID: "new-host", Label: "new", Hostname: "new.example.com", Port: 22,
			Username: "root", Protocol: domainvault.ProtocolSSH, CreatedAt: now, UpdatedAt: now,
		}},
		Groups: []domainvault.Group{{Path: "new-group", CreatedAt: now, UpdatedAt: now}},
		Keys: []domainvault.Key{{
			ID: "key-1", Label: "key", Type: domainvault.KeyTypeED25519,
			Source: domainvault.KeySourceImported, CreatedAt: now, UpdatedAt: now,
		}},
		Identities: []domainvault.Identity{{
			ID: "identity-1", Label: "id", Username: "u",
			AuthMethod: domainvault.IdentityAuthPassword, CreatedAt: now, UpdatedAt: now,
		}},
		Snippets:        []domainvault.Snippet{{ID: "snippet-1", Label: "s", Command: "ls", CreatedAt: now, UpdatedAt: now}},
		SnippetPackages: []domainvault.SnippetPackage{{Path: "pkg", CreatedAt: now, UpdatedAt: now}},
		Notes:           []domainvault.Note{{ID: "note-1", Title: "n", CreatedAt: now, UpdatedAt: now}},
		NoteGroups:      []domainvault.NoteGroup{{Path: "ng", CreatedAt: now, UpdatedAt: now}},
	}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}

	hosts, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != "new-host" {
		t.Fatalf("hosts = %+v", hosts)
	}
	groups, _ := store.ListGroups(ctx)
	if len(groups) != 1 || groups[0].Path != "new-group" {
		t.Fatalf("groups = %+v", groups)
	}
	keys, _ := store.ListKeys(ctx)
	if len(keys) != 1 || keys[0].ID != "key-1" {
		t.Fatalf("keys = %+v", keys)
	}
	identities, _ := store.ListIdentities(ctx)
	if len(identities) != 1 || identities[0].ID != "identity-1" {
		t.Fatalf("identities = %+v", identities)
	}
	snippets, _ := store.ListSnippets(ctx)
	if len(snippets) != 1 || snippets[0].ID != "snippet-1" {
		t.Fatalf("snippets = %+v", snippets)
	}
	packages, _ := store.ListSnippetPackages(ctx)
	if len(packages) != 1 || packages[0].Path != "pkg" {
		t.Fatalf("packages = %+v", packages)
	}
	notes, _ := store.ListNotes(ctx)
	if len(notes) != 1 || notes[0].ID != "note-1" {
		t.Fatalf("notes = %+v", notes)
	}
	noteGroups, _ := store.ListNoteGroups(ctx)
	if len(noteGroups) != 1 || noteGroups[0].Path != "ng" {
		t.Fatalf("note groups = %+v", noteGroups)
	}
	known, err := store.ListKnownHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != 1 || known[0].ID != "known-1" {
		t.Fatalf("known hosts were not preserved: %+v", known)
	}
}

func TestApplySyncWritesAndDeletesNetworkProfiles(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.UnixMilli(1_700_000_000_000)
	if err := store.SaveNetworkProfile(ctx, domainvault.NetworkProfile{
		ID: "old-network", Label: "old", Kind: domainvault.NetworkKindTailscale, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	payload := appsync.Payload{NetworkProfiles: []domainvault.NetworkProfile{
		{
			ID: "net-ts", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
			Tailscale: &domainvault.TailscaleNetwork{Hostname: "node", HasAuthKey: true},
			OrderKey:  1000, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "net-wg", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
			WireGuard: &domainvault.WireGuardNetwork{
				Addresses: []string{"10.0.0.2/32"}, MTU: 1280, HasPrivateKey: true,
				Peers: []domainvault.WireGuardPeer{{
					ID: "peer-1", PublicKey: "peer-public",
					AllowedIPs: []string{"10.0.0.0/24"}, HasPresharedKey: true,
				}},
			},
			OrderKey: 2000, CreatedAt: now, UpdatedAt: now,
		},
	}}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	profiles, err := store.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ID != "net-ts" || profiles[1].ID != "net-wg" {
		t.Fatalf("profiles = %+v", profiles)
	}
	if !profiles[0].Tailscale.HasAuthKey || !profiles[1].WireGuard.HasPrivateKey ||
		len(profiles[1].WireGuard.Peers) != 1 || !profiles[1].WireGuard.Peers[0].HasPresharedKey {
		t.Fatalf("markers or peers were lost: %+v", profiles)
	}
	if _, err := store.GetNetworkProfile(ctx, "old-network"); err == nil {
		t.Fatal("a profile absent from the payload survived the apply")
	}

	// The store applies exactly what it is given: an absent family means "no
	// Network Profiles". Preserving records when a payload predates the family
	// is the sync adapter's job, not this layer's.
	if err := store.ApplySync(ctx, appsync.Payload{}); err != nil {
		t.Fatalf("ApplySync without the family: %v", err)
	}
	profiles, err = store.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profiles after empty payload = %+v", profiles)
	}
}

func TestApplySyncRejectsAnInvalidPayloadBeforeDeleting(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, testHost("keep-me", "keep")); err != nil {
		t.Fatal(err)
	}
	payload := appsync.Payload{Hosts: []domainvault.Host{{ID: "bad", Label: ""}}}
	if err := store.ApplySync(ctx, payload); err == nil {
		t.Fatal("an invalid host was accepted")
	}
	hosts, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].ID != "keep-me" {
		t.Fatalf("the local Vault changed: %+v", hosts)
	}
}
