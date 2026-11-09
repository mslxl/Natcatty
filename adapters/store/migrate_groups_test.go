package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const legacyGroupsDDL = `CREATE TABLE host_groups (
	path TEXT PRIMARY KEY, id TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', username TEXT NOT NULL DEFAULT '',
	port INTEGER NOT NULL DEFAULT 0, protocol TEXT NOT NULL DEFAULT '',
	auth_method TEXT NOT NULL DEFAULT '', identity_id TEXT NOT NULL DEFAULT '',
	key_id TEXT NOT NULL DEFAULT '', use_ssh_agent INTEGER NOT NULL DEFAULT 0,
	requires_mfa INTEGER NOT NULL DEFAULT 0, legacy_algorithms INTEGER NOT NULL DEFAULT 0,
	skip_ecdsa_host_key INTEGER NOT NULL DEFAULT 0, proxy_profile_id TEXT NOT NULL DEFAULT '',
	order_key INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
	data TEXT NOT NULL)`

const legacyHostsDDL = `CREATE TABLE hosts (
	id TEXT PRIMARY KEY, label TEXT NOT NULL, hostname TEXT NOT NULL,
	port INTEGER NOT NULL DEFAULT 0, username TEXT NOT NULL DEFAULT '',
	protocol TEXT NOT NULL DEFAULT 'ssh', group_path TEXT NOT NULL DEFAULT '',
	pinned INTEGER NOT NULL DEFAULT 0, order_key INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, data TEXT NOT NULL)`

// TestOpeningAPathKeyedDatabaseMigratesMembership proves the upgrade keeps
// every Host in its group: paths become stable IDs, an implicit ancestor gets a
// record, and the physical schema stops carrying a path.
func TestOpeningAPathKeyedDatabaseMigratesMembership(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, statement := range []string{
		`DROP TABLE hosts`, `DROP TABLE host_groups`, legacyHostsDDL, legacyGroupsDDL,
	} {
		if _, err := legacy.db.Execute(ctx, statement); err != nil {
			t.Fatalf("prepare legacy schema: %v", err)
		}
	}
	when := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC).UnixMilli()
	groups := []struct{ path, data string }{
		{"prod", `{"path":"prod","username":"deploy","port":2222,"notes":"production","orderKey":1000}`},
		{"prod/web", `{"path":"prod/web","orderKey":2000}`},
	}
	for _, group := range groups {
		if _, err := legacy.db.Execute(ctx,
			`INSERT INTO host_groups (path, order_key, created_at, updated_at, data) VALUES (?, ?, ?, ?, ?)`,
			group.path, 1000, when, when, group.data); err != nil {
			t.Fatalf("insert legacy group: %v", err)
		}
	}
	hosts := []struct{ id, groupPath, data string }{
		{"h1", "prod/web", `{"id":"h1","label":"web","hostname":"web.example.com","port":22,"protocol":"ssh","group":"prod/web"}`},
		{"h2", "implicit/level", `{"id":"h2","label":"deep","hostname":"deep.example.com","port":22,"protocol":"ssh","group":"implicit/level"}`},
	}
	for _, host := range hosts {
		if _, err := legacy.db.Execute(ctx,
			`INSERT INTO hosts (id, label, hostname, group_path, created_at, updated_at, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			host.id, host.id, host.id+".example.com", host.groupPath, when, when, host.data); err != nil {
			t.Fatalf("insert legacy host: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen with the upgrade: %v", err)
	}
	defer upgraded.Close()

	listed, err := upgraded.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	byPath := make(map[string]domainvault.Group, len(listed))
	for _, group := range listed {
		byPath[group.Path] = group
	}
	web, ok := byPath["prod/web"]
	if !ok || web.ID != domainvault.GroupIDFromPath("prod/web") {
		t.Fatalf("prod/web = %+v, want its deterministic identity", web)
	}
	parent, ok := byPath["prod"]
	if !ok || web.ParentID != parent.ID {
		t.Fatalf("prod/web parent = %q, want %q", web.ParentID, parent.ID)
	}
	if parent.Username != "deploy" || parent.Port != 2222 || parent.Notes != "production" {
		t.Fatalf("group defaults lost: %+v", parent)
	}
	implicit, ok := byPath["implicit/level"]
	if !ok || implicit.ParentID != byPath["implicit"].ID {
		t.Fatalf("implicit path = %+v, want a synthesized ancestor chain", implicit)
	}

	hostRecords, err := upgraded.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := make(map[string]domainvault.Host, len(hostRecords))
	for _, host := range hostRecords {
		byID[string(host.ID)] = host
	}
	if byID["h1"].GroupID != web.ID || byID["h1"].Group != "prod/web" {
		t.Fatalf("h1 membership = %q/%q, want prod/web", byID["h1"].GroupID, byID["h1"].Group)
	}
	if byID["h2"].GroupID != implicit.ID || byID["h2"].Group != "implicit/level" {
		t.Fatalf("h2 membership = %q/%q, want implicit/level", byID["h2"].GroupID, byID["h2"].Group)
	}

	groupColumns, err := upgraded.db.Query(ctx, `PRAGMA table_info(host_groups)`)
	if err != nil {
		t.Fatalf("host_groups columns: %v", err)
	}
	for _, row := range groupColumns.Values {
		if row[1] == "path" {
			t.Fatal("host_groups still stores a path column")
		}
	}
	hostColumns, err := upgraded.db.Query(ctx, `PRAGMA table_info(hosts)`)
	if err != nil {
		t.Fatalf("hosts columns: %v", err)
	}
	hasGroupID, hasGroupPath := false, false
	for _, row := range hostColumns.Values {
		switch row[1] {
		case "group_id":
			hasGroupID = true
		case "group_path":
			hasGroupPath = true
		}
	}
	if !hasGroupID || hasGroupPath {
		t.Fatalf("hosts columns: group_id=%v group_path=%v, want id only", hasGroupID, hasGroupPath)
	}
}
