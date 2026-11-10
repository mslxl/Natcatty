package plugin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

type workspaceOptions struct {
	hosts          []appplugin.HostSummary
	sessions       []appplugin.SessionSummary
	terminalInfos  []appplugin.TerminalInfo
	terminalChunks map[string][][]byte
	remote         *appplugin.MemoryRemoteFiles
	reserved       []string
}

type workspaceFixture struct {
	host      *appplugin.HostServices
	session   *appplugin.ServiceSession
	authority *appplugin.Authority
	audit     *recordingAudit
	ref       appplugin.PluginRef
	hosts     *appplugin.MemoryHostCatalog
	sessions  *appplugin.MemorySessionCatalog
	terminal  *appplugin.MemoryTerminalCatalog
	remote    *appplugin.MemoryRemoteFiles
	now       time.Time
}

func newWorkspaceFixture(t *testing.T, limits appplugin.ServiceLimits,
	options workspaceOptions) *workspaceFixture {
	t.Helper()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	audit := &recordingAudit{}
	authority := appplugin.NewAuthority()
	ref := appplugin.PluginRef{
		ID:      "acme.workspace",
		Name:    "Acme Workspace",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
	}
	fiber := domainplugin.NewFiberID("installed", ref.ID, 1)
	hosts := appplugin.NewMemoryHostCatalog(options.hosts...)
	sessions := appplugin.NewMemorySessionCatalog(options.sessions...)
	terminal := appplugin.NewMemoryTerminalCatalog(options.terminalInfos, options.terminalChunks)
	remote := options.remote
	if remote == nil {
		remote = appplugin.NewMemoryRemoteFiles()
	}
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority:     authority,
		Audit:         audit,
		Now:           func() time.Time { return now },
		Hosts:         hosts,
		Sessions:      sessions,
		Terminal:      terminal,
		RemoteFiles:   remote,
		ReservedPaths: options.reserved,
		Limits:        limits,
	})
	session := host.Open(ref, fiber, nil)
	session.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	t.Cleanup(func() { host.CloseAll() })
	return &workspaceFixture{
		host: host, session: session, authority: authority, audit: audit, ref: ref,
		hosts: hosts, sessions: sessions, terminal: terminal, remote: remote, now: now,
	}
}

func (f *workspaceFixture) grant(permission domainplugin.Permission, scope domainplugin.Scope) {
	f.authority.Grant(appplugin.SubjectForRef(f.ref), domainplugin.PermissionGrant{
		Permission: permission, Scope: scope, GrantedAt: f.now,
	})
}

func fileScope(root string) domainplugin.Scope {
	return domainplugin.Scope{Kind: domainplugin.ScopeFiles, Path: root}
}

func hostGrant(id string) domainplugin.Scope {
	return domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: id}
}

func sessionGrant(id string) domainplugin.Scope {
	return domainplugin.Scope{Kind: domainplugin.ScopeSession, Session: id}
}

func auditHasReason(audit *recordingAudit, reason string) bool {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	for _, event := range audit.events {
		if event.Event == domainplugin.AuditService && event.Reason == reason {
			return true
		}
	}
	return false
}

func TestFilesPreopensRangeLockingAndEscapes(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	limits := defaultServiceLimits()
	limits.FileChunkBytes = 16
	fixture := newWorkspaceFixture(t, limits, workspaceOptions{})
	fixture.grant(domainplugin.PermissionFilesRead, fileScope(root))
	fixture.grant(domainplugin.PermissionFilesWrite, fileScope(root))

	preopens, err := fixture.session.FilePreopens()
	if err != nil || len(preopens) != 1 {
		t.Fatalf("FilePreopens = %+v, %v", preopens, err)
	}
	if preopens[0].Path != canonicalRoot || !preopens[0].Read || !preopens[0].Write {
		t.Fatalf("preopen = %+v, want %s read-write", preopens[0], canonicalRoot)
	}

	handle, err := fixture.session.FileOpen("data.bin", appplugin.FileModeReadWrite)
	if err != nil {
		t.Fatalf("FileOpen read-write: %v", err)
	}
	if written, err := fixture.session.FileWrite(handle, 0, []byte("hello world")); err != nil || written != 11 {
		t.Fatalf("FileWrite = %d, %v", written, err)
	}
	data, eof, err := fixture.session.FileRead(handle, 1, 5)
	if err != nil || string(data) != "ello " || eof {
		t.Fatalf("FileRead = %q eof=%v err=%v", data, eof, err)
	}
	if _, _, err := fixture.session.FileRead(handle, 0, limits.FileChunkBytes+1); err == nil {
		t.Fatal("oversized range read was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("oversized read code = %q, want limit", code)
	}
	if data, eof, err := fixture.session.FileRead(handle, 0, 0); err != nil ||
		len(data) != 0 || eof {
		t.Fatalf("zero-length range read = %q eof=%v err=%v", data, eof, err)
	}
	if err := fixture.session.FileTruncate(handle, 5); err != nil {
		t.Fatalf("FileTruncate: %v", err)
	}
	stat, err := fixture.session.FileStat("data.bin")
	if err != nil || stat.Size != 5 || stat.Dir {
		t.Fatalf("FileStat = %+v, %v", stat, err)
	}
	if err := fixture.session.FileSync(handle); err != nil {
		t.Fatalf("FileSync: %v", err)
	}

	second, err := fixture.session.FileOpen("data.bin", appplugin.FileModeReadWrite)
	if err != nil {
		t.Fatalf("second FileOpen: %v", err)
	}
	if err := fixture.session.FileLock(handle, true); err != nil {
		t.Fatalf("exclusive FileLock: %v", err)
	}
	if err := fixture.session.FileLock(second, false); err == nil {
		t.Fatal("shared lock coexisted with an exclusive lock")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeConflict {
		t.Fatalf("lock conflict code = %q, want conflict", code)
	}
	if err := fixture.session.FileUnlock(handle); err != nil {
		t.Fatalf("FileUnlock: %v", err)
	}
	if err := fixture.session.FileLock(second, false); err != nil {
		t.Fatalf("shared FileLock after unlock: %v", err)
	}
	if err := fixture.session.FileLock(handle, true); err == nil {
		t.Fatal("exclusive lock coexisted with a shared lock")
	}
	if err := fixture.session.FileClose(second); err != nil {
		t.Fatalf("FileClose second: %v", err)
	}
	if err := fixture.session.FileLock(handle, true); err != nil {
		t.Fatalf("exclusive FileLock after close released shared lock: %v", err)
	}
	if err := fixture.session.FileUnlock(handle); err != nil {
		t.Fatalf("FileUnlock exclusive: %v", err)
	}

	if err := fixture.session.FileRename("data.bin", "moved.bin"); err != nil {
		t.Fatalf("FileRename: %v", err)
	}
	entries, err := fixture.session.FileList(".")
	if err != nil {
		t.Fatalf("FileList: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "moved.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("FileList = %+v, moved.bin missing", entries)
	}
	if err := fixture.session.FileClose(handle); err != nil {
		t.Fatalf("FileClose: %v", err)
	}
	if _, _, err := fixture.session.FileRead(handle, 0, 1); err == nil {
		t.Fatal("read through a closed handle succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("closed handle code = %q, want invalid", code)
	}
	if err := fixture.session.FileUnlink("moved.bin"); err != nil {
		t.Fatalf("FileUnlink: %v", err)
	}
	if _, err := fixture.session.FileStat("moved.bin"); err == nil {
		t.Fatal("stat of an unlinked file succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("missing file code = %q, want not-found", code)
	}

	if _, err := fixture.session.FileOpen(filepath.Join(outside, "secret"), appplugin.FileModeRead); err == nil {
		t.Fatal("absolute path outside every preopen was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("outside path code = %q, want denied", code)
	}
	if _, err := fixture.session.FileStat(filepath.Join(outside, "secret")); err == nil {
		t.Fatal("stat outside every preopen was accepted")
	}
	if _, err := fixture.session.FileOpen("../escape", appplugin.FileModeRead); err == nil {
		t.Fatal("parent traversal was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("traversal code = %q, want invalid", code)
	}
	if _, err := fixture.session.FileOpen("*.bin", appplugin.FileModeRead); err == nil {
		t.Fatal("glob path was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("glob code = %q, want invalid", code)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := fixture.session.FileOpen(filepath.Join("link", "secret"), appplugin.FileModeRead); err == nil {
		t.Fatal("symlink escape was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("symlink escape code = %q, want denied", code)
	}

	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("files", "open", "ok")) {
		t.Fatal("successful files.open was not audited")
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("files", "open", "denied")) {
		t.Fatal("denied files.open was not audited")
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("files", "write", "ok")) {
		t.Fatal("files.write was not audited")
	}
}

func TestFilesNeverReachReservedAuditDirectory(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	auditDir := filepath.Join(root, "plugin-audit")
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		t.Fatalf("create audit directory: %v", err)
	}
	auditFile := filepath.Join(auditDir, "audit.jsonl")
	if err := os.WriteFile(auditFile, []byte("event\n"), 0o600); err != nil {
		t.Fatalf("write audit file: %v", err)
	}
	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{
		reserved: []string{auditDir},
	})
	fixture.grant(domainplugin.PermissionFilesRead, fileScope(root))
	fixture.grant(domainplugin.PermissionFilesWrite, fileScope(root))

	preopens, err := fixture.session.FilePreopens()
	if err != nil || len(preopens) != 1 || preopens[0].Path != canonicalRoot {
		t.Fatalf("FilePreopens = %+v, %v", preopens, err)
	}
	entries, err := fixture.session.FileList(".")
	if err != nil {
		t.Fatalf("FileList root: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == "plugin-audit" {
			t.Fatalf("reserved directory was listed: %+v", entries)
		}
	}
	for _, test := range []struct {
		path string
		mode string
	}{
		{"plugin-audit/audit.jsonl", appplugin.FileModeRead},
		{auditFile, appplugin.FileModeRead},
		{"plugin-audit/new.jsonl", appplugin.FileModeWrite},
	} {
		if _, err := fixture.session.FileOpen(test.path, test.mode); err == nil {
			t.Fatalf("FileOpen(%q) reached the reserved directory", test.path)
		} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
			t.Fatalf("FileOpen(%q) code = %q, want denied", test.path, code)
		}
	}
	if _, err := fixture.session.FileStat("plugin-audit"); err == nil {
		t.Fatal("FileStat reached the reserved directory")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("FileStat code = %q, want denied", code)
	}
	if err := fixture.session.FileUnlink("plugin-audit/audit.jsonl"); err == nil {
		t.Fatal("FileUnlink deleted the audit log")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("FileUnlink code = %q, want denied", code)
	}
	// Renaming an ancestor would move the reserved subtree.
	if err := fixture.session.FileRename(".", "moved"); err == nil {
		t.Fatal("FileRename moved the directory containing the audit log")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("FileRename code = %q, want denied", code)
	}
	if content, err := os.ReadFile(auditFile); err != nil || string(content) != "event\n" {
		t.Fatalf("audit log after denied calls = %q, %v", content, err)
	}
}

func TestFilesWithoutGrantsAreDenied(t *testing.T) {
	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{})
	preopens, err := fixture.session.FilePreopens()
	if err != nil || len(preopens) != 0 {
		t.Fatalf("ungranted FilePreopens = %+v, %v", preopens, err)
	}
	if _, err := fixture.session.FileOpen("anything", appplugin.FileModeRead); err == nil {
		t.Fatal("open without a files grant succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("ungranted open code = %q, want denied", code)
	}
	if _, err := fixture.session.FileStat("anything"); err == nil {
		t.Fatal("stat without a files grant succeeded")
	}
	if err := fixture.session.FileUnlink("anything"); err == nil {
		t.Fatal("unlink without a files grant succeeded")
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("files", "open", "denied")) {
		t.Fatal("ungranted files.open was not audited as denied")
	}
}

func TestRemoteFilesAreHostScoped(t *testing.T) {
	remote := appplugin.NewMemoryRemoteFiles()
	remote.SeedFile("host-a", "/data/a.txt", []byte("hello"))
	limits := defaultServiceLimits()
	limits.FileChunkBytes = 8
	fixture := newWorkspaceFixture(t, limits, workspaceOptions{remote: remote})
	fixture.grant(domainplugin.PermissionFilesRemoteRead, hostGrant("host-a"))
	fixture.grant(domainplugin.PermissionFilesRemoteWrite, hostGrant("host-a"))

	data, eof, err := fixture.session.RemoteRead("host-a", "/data/a.txt", 1, 3)
	if err != nil || string(data) != "ell" || eof {
		t.Fatalf("RemoteRead = %q eof=%v err=%v", data, eof, err)
	}
	if _, _, err := fixture.session.RemoteRead("host-a", "/data/a.txt", 0, 9); err == nil {
		t.Fatal("oversized remote read was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("remote read limit code = %q", code)
	}
	if written, err := fixture.session.RemoteWrite("host-a", "/data/b.txt", 0, []byte("world")); err != nil || written != 5 {
		t.Fatalf("RemoteWrite = %d, %v", written, err)
	}
	if stored, ok := remote.File("host-a", "/data/b.txt"); !ok || string(stored) != "world" {
		t.Fatalf("remote stored = %q, %v", stored, ok)
	}
	if err := fixture.session.RemoteRename("host-a", "/data/b.txt", "/data/c.txt"); err != nil {
		t.Fatalf("RemoteRename: %v", err)
	}
	stat, err := fixture.session.RemoteStat("host-a", "/data/c.txt")
	if err != nil || stat.Size != 5 {
		t.Fatalf("RemoteStat = %+v, %v", stat, err)
	}
	if err := fixture.session.RemoteMkdir("host-a", "/new"); err != nil {
		t.Fatalf("RemoteMkdir: %v", err)
	}
	dir, err := fixture.session.RemoteStat("host-a", "/new")
	if err != nil || !dir.Dir {
		t.Fatalf("RemoteStat dir = %+v, %v", dir, err)
	}
	entries, err := fixture.session.RemoteList("host-a", "/data")
	if err != nil || len(entries) != 2 {
		t.Fatalf("RemoteList = %+v, %v", entries, err)
	}
	if err := fixture.session.RemoteDelete("host-a", "/data/c.txt", false); err != nil {
		t.Fatalf("RemoteDelete: %v", err)
	}

	if _, _, err := fixture.session.RemoteRead("host-b", "/data/a.txt", 0, 1); err == nil {
		t.Fatal("remote read on an ungranted Host succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("wrong host code = %q, want denied", code)
	}
	if _, err := fixture.session.RemoteWrite("host-b", "/data/x", 0, []byte("x")); err == nil {
		t.Fatal("remote write on an ungranted Host succeeded")
	}
	if _, _, err := fixture.session.RemoteRead("host-a", "../etc/passwd", 0, 1); err == nil {
		t.Fatal("remote traversal was accepted")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("remote traversal code = %q, want invalid", code)
	}
	// The remote path never becomes a local preopen.
	if preopens, err := fixture.session.FilePreopens(); err != nil || len(preopens) != 0 {
		t.Fatalf("remote grants produced local preopens: %+v, %v", preopens, err)
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("files", "remote_read", "denied")) {
		t.Fatal("denied remote read was not audited")
	}
}

func TestHostsResourceScopingProvenanceAndAudit(t *testing.T) {
	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{
		hosts: []appplugin.HostSummary{
			{ID: "host-a", Label: "Alpha", Hostname: "a.example", Port: 22, Protocol: "ssh"},
			{ID: "host-b", Label: "Beta", Hostname: "b.example", Port: 22, Protocol: "ssh"},
		},
	})
	fixture.grant(domainplugin.PermissionHostsRead, hostGrant("host-a"))
	fixture.grant(domainplugin.PermissionHostsWrite, hostGrant("host-a"))

	hosts, err := fixture.session.HostsList()
	if err != nil || len(hosts) != 1 || hosts[0].ID != "host-a" {
		t.Fatalf("HostsList = %+v, %v", hosts, err)
	}
	if host, err := fixture.session.HostsGet("host-a"); err != nil || host.Label != "Alpha" {
		t.Fatalf("HostsGet(host-a) = %+v, %v", host, err)
	}
	if _, err := fixture.session.HostsGet("host-b"); err == nil {
		t.Fatal("HostsGet outside the Host scope succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("host-b get code = %q, want denied", code)
	}
	if result, err := fixture.session.HostsTest("host-a"); err != nil || !result.Reachable {
		t.Fatalf("HostsTest = %+v, %v", result, err)
	}
	if _, err := fixture.session.HostsTest("host-b"); err == nil {
		t.Fatal("HostsTest outside the Host scope succeeded")
	}

	created, err := fixture.session.HostsCreate(appplugin.HostDraft{
		Label: "Created", Hostname: "created.example", Port: 22, Protocol: "ssh",
	})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	if created.CreatedBy != fixture.ref.ID {
		t.Fatalf("created provenance = %q, want %q", created.CreatedBy, fixture.ref.ID)
	}
	label := "Renamed"
	updated, err := fixture.session.HostsUpdate(created.ID, appplugin.HostPatch{Label: &label})
	if err != nil || updated.Label != "Renamed" {
		t.Fatalf("HostsUpdate created = %+v, %v", updated, err)
	}
	if _, err := fixture.session.HostsUpdate("host-b", appplugin.HostPatch{Label: &label}); err == nil {
		t.Fatal("HostsUpdate outside the Host scope succeeded")
	}
	if _, err := fixture.session.HostsCreate(appplugin.HostDraft{Hostname: ""}); err == nil {
		t.Fatal("hosts.create with an empty hostname succeeded")
	}

	imported, err := fixture.session.HostsImport([]appplugin.HostDraft{
		{Label: "One", Hostname: "one.example", Port: 22, Protocol: "ssh"},
		{Label: "Two", Hostname: "two.example", Port: 22, Protocol: "ssh"},
	})
	if err != nil || len(imported) != 2 {
		t.Fatalf("HostsImport = %+v, %v", imported, err)
	}
	for _, host := range imported {
		if host.CreatedBy != fixture.ref.ID {
			t.Fatalf("imported provenance = %q", host.CreatedBy)
		}
	}
	if _, err := fixture.session.HostsImport(nil); err == nil {
		t.Fatal("empty hosts.import succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("empty import code = %q, want invalid", code)
	}
	hosts, err = fixture.session.HostsList()
	if err != nil || len(hosts) != 4 {
		t.Fatalf("HostsList after create/import = %+v, %v", hosts, err)
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("hosts", "create", "ok")) ||
		!auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("hosts", "test", "denied")) {
		t.Fatal("Hosts access was not audited")
	}

	denied := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{})
	if _, err := denied.session.HostsList(); err == nil {
		t.Fatal("ungranted hosts.list succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("ungranted hosts.list code = %q", code)
	}
	if _, err := denied.session.HostsCreate(appplugin.HostDraft{Hostname: "x"}); err == nil {
		t.Fatal("ungranted hosts.create succeeded")
	}
}

func TestSessionsGrantsProviderAndFiberCreated(t *testing.T) {
	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{
		hosts: []appplugin.HostSummary{
			{ID: "host-a", Label: "Alpha", Hostname: "a.example", Port: 22, Protocol: "ssh"},
			{ID: "host-b", Label: "Beta", Hostname: "b.example", Port: 22, Protocol: "ssh"},
		},
		sessions: []appplugin.SessionSummary{
			{ID: "user-1", Kind: "ssh", Title: "user", HostID: "host-a", State: "running"},
			{ID: "provider-1", Kind: "plugin", Title: "provider", ProviderCreatedBy: "acme.workspace"},
			{ID: "foreign-1", Kind: "ssh", Title: "foreign", ProviderCreatedBy: "other.plugin"},
		},
	})
	fixture.grant(domainplugin.PermissionSessionsRead, sessionGrant("user-1"))
	fixture.grant(domainplugin.PermissionSessionsControl, sessionGrant("user-1"))
	fixture.grant(domainplugin.PermissionHostsRead, hostGrant("host-a"))

	sessions, err := fixture.session.SessionsList()
	if err != nil {
		t.Fatalf("SessionsList: %v", err)
	}
	ids := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		ids[session.ID] = true
	}
	if !ids["user-1"] || !ids["provider-1"] || ids["foreign-1"] {
		t.Fatalf("SessionsList = %+v, want user-1 and provider-1 only", sessions)
	}
	if _, err := fixture.session.SessionsGet("provider-1"); err != nil {
		t.Fatalf("provider-created SessionsGet: %v", err)
	}
	if _, err := fixture.session.SessionsGet("foreign-1"); err == nil {
		t.Fatal("another plugin's provider Session was visible")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("foreign session code = %q, want denied", code)
	}
	if _, err := fixture.session.SessionsGet("missing"); err == nil {
		t.Fatal("missing session did not report not-found")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("missing session code = %q", code)
	}

	created, err := fixture.session.SessionsConnect("host-a")
	if err != nil {
		t.Fatalf("SessionsConnect: %v", err)
	}
	if created.HostID != "host-a" {
		t.Fatalf("connected session = %+v", created)
	}
	if _, err := fixture.session.SessionsGet(created.ID); err != nil {
		t.Fatalf("Get fiber-created session: %v", err)
	}
	if err := fixture.session.SessionsReconnect(created.ID); err != nil {
		t.Fatalf("Reconnect fiber-created session: %v", err)
	}
	if err := fixture.session.SessionsDisconnect(created.ID); err != nil {
		t.Fatalf("Disconnect fiber-created session: %v", err)
	}
	if err := fixture.session.SessionsReconnect("provider-1"); err != nil {
		t.Fatalf("provider Session needs no per-session grant: %v", err)
	}
	if err := fixture.session.SessionsDisconnect("foreign-1"); err == nil {
		t.Fatal("disconnecting another plugin's provider Session succeeded")
	}
	if _, err := fixture.session.SessionsConnect("host-b"); err == nil {
		t.Fatal("connecting to a Host outside hosts.read succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("connect host-b code = %q, want denied", code)
	}

	denied := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{})
	if _, err := denied.session.SessionsList(); err == nil {
		t.Fatal("ungranted sessions.list succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("ungranted sessions.list code = %q", code)
	}
	if _, err := denied.session.SessionsConnect("host-a"); err == nil {
		t.Fatal("ungranted sessions.connect succeeded")
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("sessions", "connect", "ok")) {
		t.Fatal("sessions.connect was not audited")
	}
}

func TestTerminalLossyReadOnlySeparateScopes(t *testing.T) {
	limits := defaultServiceLimits()
	limits.TerminalQueueBytes = 4
	limits.TerminalObservations = 1
	fixture := newWorkspaceFixture(t, limits, workspaceOptions{
		sessions: []appplugin.SessionSummary{
			{ID: "s1", Kind: "ssh", Title: "one"},
			{ID: "s2", Kind: "ssh", Title: "two"},
			{ID: "provider-1", Kind: "plugin", Title: "provider", ProviderCreatedBy: "acme.workspace"},
		},
		terminalInfos: []appplugin.TerminalInfo{
			{SessionID: "s1", Title: "one", Cols: 80, Rows: 24},
			{SessionID: "s2", Title: "two", Cols: 100, Rows: 40},
			{SessionID: "provider-1", Title: "provider", Cols: 80, Rows: 24},
		},
		terminalChunks: map[string][][]byte{
			"s1": {[]byte("abcdefgh")},
		},
	})
	fixture.grant(domainplugin.PermissionTerminalObserve, sessionGrant("s1"))
	fixture.grant(domainplugin.PermissionTerminalWrite, sessionGrant("s2"))
	fixture.grant(domainplugin.PermissionTerminalInfo, sessionGrant("s2"))

	handle, err := fixture.session.TerminalObserve("s1")
	if err != nil {
		t.Fatalf("TerminalObserve: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var data []byte
	var dropped uint64
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		chunk, count, _, readErr := fixture.session.TerminalRead(ctx, handle, 4)
		cancel()
		if readErr == nil {
			data = append(data, chunk...)
			dropped = count
		}
		if dropped >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lossy terminal tap did not report dropped bytes; data=%q dropped=%d", data, dropped)
		}
	}
	if string(data) != "efgh" {
		t.Fatalf("terminal data = %q, want the newest four bytes efgh", data)
	}
	if _, err := fixture.session.TerminalObserve("s1"); err == nil {
		t.Fatal("terminal observation quota was not enforced")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("observation quota code = %q, want limit", code)
	}
	if err := fixture.session.TerminalClose(handle); err != nil {
		t.Fatalf("TerminalClose: %v", err)
	}

	if _, err := fixture.session.TerminalObserve("s2"); err == nil {
		t.Fatal("observe without terminal.observe succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("observe denial code = %q", code)
	}
	providerHandle, err := fixture.session.TerminalObserve("provider-1")
	if err != nil {
		t.Fatalf("provider-created Session needed a per-session terminal grant: %v", err)
	}
	if err := fixture.session.TerminalClose(providerHandle); err != nil {
		t.Fatalf("close provider observation: %v", err)
	}

	if err := fixture.session.TerminalWrite("s2", []byte("ls\n")); err != nil {
		t.Fatalf("TerminalWrite s2: %v", err)
	}
	writes := fixture.terminal.Writes("s2")
	if len(writes) != 1 || string(writes[0]) != "ls\n" {
		t.Fatalf("terminal writes = %q", writes)
	}
	if err := fixture.session.TerminalWrite("s1", []byte("x")); err == nil {
		t.Fatal("terminal.write on an observe-only Session succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("terminal.write denial code = %q", code)
	}
	if info, err := fixture.session.TerminalInfo("s2"); err != nil || info.Cols != 100 {
		t.Fatalf("TerminalInfo = %+v, %v", info, err)
	}
	if _, err := fixture.session.TerminalInfo("s1"); err == nil {
		t.Fatal("terminal.info without the separate scope succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("terminal.info denial code = %q", code)
	}

	// Terminal grants imply neither local files nor remote files.
	if preopens, err := fixture.session.FilePreopens(); err != nil || len(preopens) != 0 {
		t.Fatalf("terminal grants exposed preopens: %+v, %v", preopens, err)
	}
	if _, _, err := fixture.session.RemoteRead("s1", "/etc/passwd", 0, 1); err == nil {
		t.Fatal("terminal grants allowed a remote file read")
	}
	if !auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("terminal", "observe", "ok")) ||
		!auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("terminal", "write", "ok")) ||
		!auditHasReason(fixture.audit, domainplugin.ServiceAuditReason("terminal", "info", "denied")) {
		t.Fatal("terminal access was not audited")
	}
}

func TestAuthoritySameScopesForEveryCallerKind(t *testing.T) {
	subject := appplugin.SubjectForInstalled(domainplugin.SourceLocalFile, "acme.workspace")
	scope := hostGrant("host-a")
	authority := appplugin.NewAuthority()
	authority.SetGrants(subject, []domainplugin.PermissionGrant{{
		Permission: domainplugin.PermissionHostsRead, Scope: scope,
	}})
	kinds := []appplugin.CallerKind{
		appplugin.CallerGuest,
		appplugin.CallerUI,
		appplugin.CallerMCP,
		appplugin.CallerPlugin,
	}
	for _, kind := range kinds {
		caller := appplugin.Caller{Subject: subject, Kind: kind}
		if err := authority.Authorize(caller, domainplugin.PermissionHostsRead, scope); err != nil {
			t.Fatalf("%s caller denied the granted scope: %v", kind, err)
		}
		if err := authority.Authorize(caller, domainplugin.PermissionHostsRead, hostGrant("host-b")); err == nil {
			t.Fatalf("%s caller acquired another Host scope", kind)
		}
		if err := authority.Authorize(caller, domainplugin.PermissionHostsWrite, scope); err == nil {
			t.Fatalf("%s caller acquired an ungranted permission", kind)
		}
		if err := authority.Authorize(appplugin.Caller{Kind: kind}, domainplugin.PermissionHostsRead, scope); err == nil {
			t.Fatalf("%s caller with an empty subject was authorized", kind)
		}
	}
	if err := authority.Authorize(appplugin.Caller{}, domainplugin.PermissionHostsRead, scope); err == nil {
		t.Fatal("zero caller acquired ambient authority")
	}

	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{})
	fixture.grant(domainplugin.PermissionHostsRead, scope)
	if err := fixture.host.Authorize(appplugin.Caller{Subject: appplugin.SubjectForRef(fixture.ref),
		Kind: appplugin.CallerUI}, domainplugin.PermissionHostsRead, scope); err != nil {
		t.Fatalf("HostServices.Authorize UI caller: %v", err)
	}
}

func TestWorkspaceDispatcherEnvelopes(t *testing.T) {
	root := t.TempDir()
	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{
		hosts: []appplugin.HostSummary{{ID: "host-a", Label: "Alpha", Hostname: "a.example", Protocol: "ssh"}},
		sessions: []appplugin.SessionSummary{
			{ID: "s1", Kind: "ssh", Title: "one", HostID: "host-a", State: "running"},
		},
		terminalInfos:  []appplugin.TerminalInfo{{SessionID: "s1", Title: "one", Cols: 80, Rows: 24}},
		terminalChunks: map[string][][]byte{"s1": {[]byte("output")}},
	})
	fixture.grant(domainplugin.PermissionFilesRead, fileScope(root))
	fixture.grant(domainplugin.PermissionFilesWrite, fileScope(root))
	fixture.grant(domainplugin.PermissionHostsRead, hostGrant("host-a"))
	fixture.grant(domainplugin.PermissionSessionsRead, sessionGrant("s1"))
	fixture.grant(domainplugin.PermissionTerminalObserve, sessionGrant("s1"))

	preopens := dispatchService(t, fixture.session, "files", domainpluginabi.FilesMethodPreopens, "")
	if preopens.Code != "ok" {
		t.Fatalf("files.preopens envelope = %+v", preopens)
	}
	opened := dispatchService(t, fixture.session, "files", domainpluginabi.FilesMethodOpen,
		`{"path":"dispatch.txt","mode":"read-write"}`)
	if opened.Code != "ok" {
		t.Fatalf("files.open envelope = %+v", opened)
	}
	var openedData struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(opened.Data, &openedData); err != nil || openedData.Handle == "" {
		t.Fatalf("files.open data = %s, %v", opened.Data, err)
	}
	written := dispatchService(t, fixture.session, "files", domainpluginabi.FilesMethodWrite,
		`{"handle":"`+openedData.Handle+`","offset":0,"data":"`+
			base64.StdEncoding.EncodeToString([]byte("dispatch"))+`"}`)
	if written.Code != "ok" {
		t.Fatalf("files.write envelope = %+v", written)
	}
	read := dispatchService(t, fixture.session, "files", domainpluginabi.FilesMethodRead,
		`{"handle":"`+openedData.Handle+`","offset":0,"length":8}`)
	if read.Code != "ok" {
		t.Fatalf("files.read envelope = %+v", read)
	}
	var readData struct {
		Data string `json:"data"`
		EOF  bool   `json:"eof"`
	}
	if err := json.Unmarshal(read.Data, &readData); err != nil || readData.Data == "" {
		t.Fatalf("files.read data = %s, %v", read.Data, err)
	}
	if hosts := dispatchService(t, fixture.session, "hosts", domainpluginabi.HostsMethodList, ""); hosts.Code != "ok" {
		t.Fatalf("hosts.list envelope = %+v", hosts)
	}
	if sessions := dispatchService(t, fixture.session, "sessions",
		domainpluginabi.SessionsMethodList, ""); sessions.Code != "ok" {
		t.Fatalf("sessions.list envelope = %+v", sessions)
	}
	observed := dispatchService(t, fixture.session, "terminal",
		domainpluginabi.TerminalMethodObserve, `{"sessionId":"s1"}`)
	if observed.Code != "ok" {
		t.Fatalf("terminal.observe envelope = %+v", observed)
	}
	var observedData struct {
		Handle string `json:"handle"`
	}
	if err := json.Unmarshal(observed.Data, &observedData); err != nil || observedData.Handle == "" {
		t.Fatalf("terminal.observe data = %s, %v", observed.Data, err)
	}
	deniedInfo := dispatchService(t, fixture.session, "terminal",
		domainpluginabi.TerminalMethodInfo, `{"sessionId":"s1"}`)
	if deniedInfo.Code != "denied" || deniedInfo.Permission != string(domainplugin.PermissionTerminalInfo) {
		t.Fatalf("terminal.info denial = %+v", deniedInfo)
	}
	deniedRemote := dispatchService(t, fixture.session, "files",
		domainpluginabi.FilesMethodRemoteList, `{"host":"host-a","path":"/"}`)
	if deniedRemote.Code != "denied" ||
		deniedRemote.Permission != string(domainplugin.PermissionFilesRemoteRead) {
		t.Fatalf("files.remote_list denial = %+v", deniedRemote)
	}
	if unknown := dispatchService(t, fixture.session, "files", 99, ""); unknown.Code != "invalid" {
		t.Fatalf("unknown method envelope = %+v", unknown)
	}
	if unknown := dispatchService(t, fixture.session, "unknown", 1, ""); unknown.Code != "invalid" {
		t.Fatalf("unknown service envelope = %+v", unknown)
	}
}

func TestWorkspaceRevocationClosesFileAndTerminalHandles(t *testing.T) {
	root := t.TempDir()
	fixture := newWorkspaceFixture(t, defaultServiceLimits(), workspaceOptions{
		sessions:       []appplugin.SessionSummary{{ID: "s1", Kind: "ssh", Title: "one"}},
		terminalInfos:  []appplugin.TerminalInfo{{SessionID: "s1", Title: "one"}},
		terminalChunks: map[string][][]byte{"s1": {[]byte("x")}},
	})
	fixture.grant(domainplugin.PermissionFilesRead, fileScope(root))
	fixture.grant(domainplugin.PermissionFilesWrite, fileScope(root))
	fixture.grant(domainplugin.PermissionTerminalObserve, sessionGrant("s1"))

	fileHandle, err := fixture.session.FileOpen("revoked.txt", appplugin.FileModeReadWrite)
	if err != nil {
		t.Fatalf("FileOpen: %v", err)
	}
	tap, err := fixture.session.TerminalObserve("s1")
	if err != nil {
		t.Fatalf("TerminalObserve: %v", err)
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref), domainplugin.PermissionFilesWrite)

	if _, _, err := fixture.session.FileRead(fileHandle, 0, 1); err == nil {
		t.Fatal("file handle survived an authority epoch change")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked file handle code = %q, want invalid", code)
	}
	if _, _, _, err := fixture.session.TerminalRead(context.Background(), tap, 1); err == nil {
		t.Fatal("terminal handle survived an authority epoch change")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked terminal handle code = %q, want invalid", code)
	}
	if count := fixture.terminal.ObservationCount(); count != 0 {
		t.Fatalf("revocation left %d live observations", count)
	}
}

func TestFilesHandleQuotaAndDirectoryQuota(t *testing.T) {
	root := t.TempDir()
	limits := defaultServiceLimits()
	limits.FileHandles = 1
	limits.DirectoryEntries = 1
	fixture := newWorkspaceFixture(t, limits, workspaceOptions{})
	fixture.grant(domainplugin.PermissionFilesRead, fileScope(root))
	fixture.grant(domainplugin.PermissionFilesWrite, fileScope(root))
	first, err := fixture.session.FileOpen("a.txt", appplugin.FileModeReadWrite)
	if err != nil {
		t.Fatalf("first FileOpen: %v", err)
	}
	if _, err := fixture.session.FileOpen("b.txt", appplugin.FileModeReadWrite); err == nil {
		t.Fatal("file handle quota was not enforced")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("handle quota code = %q, want limit", code)
	}
	if err := fixture.session.FileClose(first); err != nil {
		t.Fatalf("FileClose: %v", err)
	}
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, err := fixture.session.FileList("."); err == nil {
		t.Fatal("directory entry quota was not enforced")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("directory quota code = %q, want limit", code)
	}
}
