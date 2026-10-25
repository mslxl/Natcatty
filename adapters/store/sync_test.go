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
