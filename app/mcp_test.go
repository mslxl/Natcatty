package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

type fakeVault struct {
	hosts      []domainvault.Host
	notes      []domainvault.Note
	identities []domainvault.Identity
	proxies    []domainvault.ProxyProfile
	groups     []domainvault.Group
	snippets   []domainvault.Snippet
	err        error
}

func (f fakeVault) List(context.Context) ([]domainvault.Host, error) {
	return f.hosts, f.err
}

func (f fakeVault) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	if f.err != nil {
		return domainvault.Host{}, f.err
	}
	for _, host := range f.hosts {
		if host.ID == id {
			return host, nil
		}
	}
	return domainvault.Host{}, appvault.ErrNotFound
}

func (f fakeVault) ListNotes(context.Context) ([]domainvault.Note, error) {
	return f.notes, f.err
}

func (f fakeVault) GetNote(_ context.Context, id string) (domainvault.Note, error) {
	if f.err != nil {
		return domainvault.Note{}, f.err
	}
	for _, note := range f.notes {
		if note.ID == id {
			return note, nil
		}
	}
	return domainvault.Note{}, appvault.ErrNotFound
}

func (f fakeVault) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return f.identities, f.err
}

func (f fakeVault) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	return f.proxies, f.err
}

func (f fakeVault) ListGroups(context.Context) ([]domainvault.Group, error) {
	return f.groups, f.err
}

func (f fakeVault) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	return f.snippets, f.err
}

type fakeSessions struct {
	descriptions []appsession.SessionDescription
	screens      map[model.SessionID]appsession.ScreenTextSnapshot
}

func (f fakeSessions) Describe(id model.SessionID) (appsession.SessionDescription, bool) {
	for _, description := range f.descriptions {
		if description.ID == id {
			return description, true
		}
	}
	return appsession.SessionDescription{}, false
}

func (f fakeSessions) DescribeAll() []appsession.SessionDescription {
	return f.descriptions
}

func (f fakeSessions) ScreenText(id model.SessionID) (appsession.ScreenTextSnapshot, bool) {
	screen, ok := f.screens[id]
	return screen, ok
}

type fakeForwarding struct {
	views []appforwarding.View
	err   error
}

func (f fakeForwarding) List(context.Context) ([]appforwarding.View, error) {
	return f.views, f.err
}

type fakeFiles struct {
	apptransfer.FileSystem
	root      string
	entries   []domainsftp.Entry
	statEntry domainsftp.Entry
	content   string
}

func (f *fakeFiles) Root(context.Context) (string, error) { return f.root, nil }

func (f *fakeFiles) List(context.Context, string) ([]domainsftp.Entry, error) {
	return f.entries, nil
}

func (f *fakeFiles) Stat(context.Context, string) (domainsftp.Entry, error) {
	return f.statEntry, nil
}

func (f *fakeFiles) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.content)), nil
}

func (f *fakeFiles) Close() error { return nil }

type fakeTransfer struct {
	files *fakeFiles
	err   error
}

func (f fakeTransfer) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.files, nil
}

func callMap(t *testing.T, bridge *mcpReadBridge, method string, params map[string]any) map[string]any {
	t.Helper()
	value, err := bridge.Call(context.Background(), method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %#v", method, value)
	}
	return result
}

func mustOK(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	if result["ok"] != true {
		t.Fatalf("result = %v", result)
	}
	return result
}

func TestExternalMCPAuthenticatesAndAnswersReadTools(t *testing.T) {
	host := domainvault.Host{
		ID: "h1", Label: "web", Hostname: "web.example", Port: 22,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	description := appsession.SessionDescription{
		ID: "s1", Label: "web shell", HostID: "h1", Kind: appsession.KindSSH,
		Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive,
		Hostname: "web.example", Username: "deploy", Connected: true,
	}
	rule := domainforward.Rule{
		ID: "r1", Label: "db", Type: domainforward.TypeLocal, BindAddress: "127.0.0.1",
		LocalPort: 5432, RemoteHost: "db.internal", RemotePort: 5432, HostID: "h1",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	bridge := newMCPReadBridge(
		fakeVault{hosts: []domainvault.Host{host}},
		fakeForwarding{views: []appforwarding.View{{Rule: rule, Status: domainforward.StatusActive}}},
		fakeSessions{descriptions: []appsession.SessionDescription{description}},
		fakeTransfer{},
	)
	path := filepath.Join(t.TempDir(), "external-mcp", "discovery.json")
	server, err := startExternalMCP(context.Background(), bridge, path, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	discovery, err := mcprpc.ReadDiscovery(path)
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	if discovery.Port != server.port || discovery.Token != server.token {
		t.Fatalf("discovery does not point at the listener: %+v", discovery)
	}
	if discovery.PermissionMode != "confirm" || discovery.ChatSessionID != mcprpc.ExternalScope {
		t.Fatalf("discovery defaults = %+v", discovery)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wrong, err := mcprpc.Dial(ctx, "127.0.0.1", discovery.Port)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := wrong.Verify(ctx, "not-the-token"); mcprpc.ErrorCode(err) != mcprpc.CodeAuthRequired {
		t.Fatalf("wrong token error = %v, code = %d", err, mcprpc.ErrorCode(err))
	}
	_ = wrong.Close()

	client, err := mcprpc.Dial(ctx, "127.0.0.1", discovery.Port)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if err := client.Verify(ctx, discovery.Token); err != nil {
		t.Fatalf("verify: %v", err)
	}
	raw, err := client.Call(ctx, "netcatty/getContext", map[string]any{})
	if err != nil {
		t.Fatalf("getContext: %v", err)
	}
	var environment map[string]any
	if err := json.Unmarshal(raw, &environment); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if environment["environment"] != "netcatty-terminal" || environment["hostCount"].(float64) != 1 {
		t.Fatalf("environment = %v", environment)
	}
	if tunnels, _ := environment["activePortForwardTunnels"].([]any); len(tunnels) != 1 {
		t.Fatalf("tunnels = %v", environment["activePortForwardTunnels"])
	}

	server.Close()
	if _, err := mcprpc.ReadDiscovery(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discovery survives Close: %v", err)
	}
}

func TestMCPVaultReadToolsProjectTheOriginalShapes(t *testing.T) {
	host := domainvault.Host{
		ID: "h1", Label: "web", Hostname: "web.example", Port: 22, Username: "deploy",
		Protocol: domainvault.ProtocolSSH, Group: "prod", Tags: []string{"linux"},
		Notes: "primary", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	note := domainvault.Note{
		ID: "n1", Title: "Runbook", Content: "a😀" + strings.Repeat("x", 7000),
		Group: "ops", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	snippet := domainvault.Snippet{
		ID: "sn1", Label: "deploy", Command: "make deploy", Package: "ops",
		Tags: []string{"release"}, Targets: []model.HostID{"h1"},
		MultiLineRunMode: domainvault.SnippetRunPaste,
	}
	bridge := newMCPReadBridge(
		fakeVault{
			hosts:  []domainvault.Host{host},
			notes:  []domainvault.Note{note},
			groups: []domainvault.Group{{Path: "prod", Username: "deploy", Port: 2222}},
			identities: []domainvault.Identity{{
				ID: "i1", Label: "deploy", Username: "deploy", AuthMethod: "password",
			}},
			proxies: []domainvault.ProxyProfile{{
				ID: "p1", Label: "corp", Config: domainvault.ProxyConfig{
					Type: domainvault.ProxySOCKS5, Host: "proxy", Port: 1080,
				},
			}},
			snippets: []domainvault.Snippet{snippet},
		},
		fakeForwarding{}, fakeSessions{}, fakeTransfer{},
	)

	hosts := mustOK(t, callMap(t, bridge, "public/vault/hosts/list", nil))
	hostList := hosts["hosts"].([]any)
	if len(hostList) != 1 {
		t.Fatalf("hosts = %v", hostList)
	}
	summary := hostList[0].(map[string]any)
	if summary["label"] != "web" || summary["group"] != "prod" || summary["protocol"] != "ssh" {
		t.Fatalf("summary = %v", summary)
	}

	detail := mustOK(t, callMap(t, bridge, "public/vault/host/get", map[string]any{"hostId": "h1"}))
	hostDetail := detail["host"].(map[string]any)
	if hostDetail["label"] != "web" || hostDetail["notes"] != "primary" {
		t.Fatalf("detail = %v", hostDetail)
	}
	if _, leaked := hostDetail["password"]; leaked {
		t.Fatal("host detail leaked a password field")
	}

	notes := mustOK(t, callMap(t, bridge, "public/vault/notes/list", nil))
	noteSummary := notes["notes"].([]any)[0].(map[string]any)
	if noteSummary["contentLength"].(int) != 7003 {
		t.Fatalf("contentLength = %v, want UTF-16 7002", noteSummary["contentLength"])
	}
	read := mustOK(t, callMap(t, bridge, "public/vault/notes/get", map[string]any{"noteId": "n1"}))
	body := read["note"].(map[string]any)
	if appvault.UTF16Length(body["content"].(string)) != appvault.MaxNoteReadChars {
		t.Fatalf("excerpt = %d UTF-16 units", appvault.UTF16Length(body["content"].(string)))
	}
	if read["nextOffset"].(int) != appvault.MaxNoteReadChars {
		t.Fatalf("nextOffset = %v", read["nextOffset"])
	}
	unmatched := mustOK(t, callMap(t, bridge, "public/vault/notes/get",
		map[string]any{"noteId": "n1", "query": "absent"}))
	if value, present := unmatched["matchOffset"]; !present || value != nil {
		t.Fatalf("unmatched matchOffset = %v (present %v), want null", value, present)
	}
	invalid := callMap(t, bridge, "public/vault/notes/get",
		map[string]any{"noteId": "n1", "maxChars": 1})
	if invalid["ok"] != false ||
		invalid["error"] != "maxChars must be an integer of at least 2 (capped at 6000)." {
		t.Fatalf("invalid note read = %v", invalid)
	}

	identities := mustOK(t, callMap(t, bridge, "public/vault/identities/list", nil))
	if identities["identities"].([]any)[0].(map[string]any)["label"] != "deploy" {
		t.Fatalf("identities = %v", identities)
	}
	proxies := mustOK(t, callMap(t, bridge, "public/vault/proxyProfiles/list", nil))
	proxy := proxies["proxyProfiles"].([]any)[0].(map[string]any)
	if proxy["type"] != "socks5" || proxy["host"] != "proxy" {
		t.Fatalf("proxy = %v", proxy)
	}
	groups := mustOK(t, callMap(t, bridge, "public/vault/groups/list", nil))
	group := groups["groups"].([]any)[0].(map[string]any)
	if group["path"] != "prod" || group["defaults"].(map[string]any)["port"].(int) != 2222 {
		t.Fatalf("group = %v", group)
	}
	snippets := mustOK(t, callMap(t, bridge, "public/vault/snippets/list", nil))
	snippetRecord := snippets["snippets"].([]any)[0].(map[string]any)
	if _, leaked := snippetRecord["command"]; leaked {
		t.Fatal("the list serializer leaked the command")
	}
	snippetDetail := mustOK(t, callMap(t, bridge, "public/vault/snippets/get",
		map[string]any{"snippetId": "sn1"}))
	if snippetDetail["snippet"].(map[string]any)["command"] != "make deploy" {
		t.Fatalf("snippet detail = %v", snippetDetail)
	}

	scripts := mustOK(t, callMap(t, bridge, "public/vault/hostConnectScripts/list",
		map[string]any{"hostId": "h1"}))
	if len(scripts["connectScriptIds"].([]any)) != 0 {
		t.Fatalf("connect scripts = %v", scripts)
	}

	missing := callMap(t, bridge, "public/vault/host/get", map[string]any{"hostId": "gone"})
	if missing["ok"] != false || missing["error"] != `Host "gone" was not found.` {
		t.Fatalf("missing host = %v", missing)
	}
}

func TestMCPTerminalContextIsBounded(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < 1000; index++ {
		builder.WriteString("line ")
		builder.WriteString(strings.Repeat("x", index%3))
		builder.WriteByte('\n')
	}
	descriptions := []appsession.SessionDescription{{
		ID: "s1", Label: "shell", Kind: appsession.KindLocal,
		Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive,
		Rows: 24, Cols: 80, Connected: true,
	}}
	bridge := newMCPReadBridge(fakeVault{}, fakeForwarding{}, fakeSessions{
		descriptions: descriptions,
		screens: map[model.SessionID]appsession.ScreenTextSnapshot{
			"s1": {SessionID: "s1", Text: builder.String(), Rows: 24, Cols: 80},
		},
	}, fakeTransfer{})

	viewport := mustOK(t, callMap(t, bridge, "netcatty/readContext",
		map[string]any{"sessionId": "s1"}))
	if viewport["returnedLines"].(int) != 24 {
		t.Fatalf("viewport returned %v lines", viewport["returnedLines"])
	}
	if viewport["range"] != "viewport" || viewport["hasMoreBefore"] != true {
		t.Fatalf("viewport = %v", viewport)
	}

	tail := mustOK(t, callMap(t, bridge, "netcatty/readContext",
		map[string]any{"sessionId": "s1", "range": "tail", "maxLines": 500}))
	if tail["returnedLines"].(int) != appsession.TerminalContextMaxLines {
		t.Fatalf("tail returned %v lines, want the 300 cap", tail["returnedLines"])
	}
	if tail["hasMoreAfter"] != false {
		t.Fatalf("tail = %v", tail)
	}

	lines := mustOK(t, callMap(t, bridge, "netcatty/readContext",
		map[string]any{"sessionId": "s1", "range": "lines", "startLine": 10, "maxLines": 5}))
	if lines["startLine"].(int) != 10 || lines["returnedLines"].(int) != 5 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.HasPrefix(lines["content"].(string), "line ") {
		t.Fatalf("content = %q", lines["content"])
	}

	// A single Session resolves without sessionId; none or many require it.
	if result := mustOK(t, callMap(t, bridge, "netcatty/readContext", nil)); result["sessionId"] != "s1" {
		t.Fatalf("implicit session = %v", result)
	}
	missing := callMap(t, bridge, "netcatty/readContext", map[string]any{"sessionId": "gone"})
	if missing["ok"] != false {
		t.Fatalf("missing session = %v", missing)
	}
}

func TestMCPSFTFReadsAreBounded(t *testing.T) {
	entries := make([]domainsftp.Entry, 0, maxMCPListEntries+1)
	for index := 0; index <= maxMCPListEntries; index++ {
		entries = append(entries, domainsftp.Entry{
			Name: "file", Path: "/file", Kind: domainsftp.KindFile,
			Size: 12, Mode: 0o644, Owner: "root",
			ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		})
	}
	entries[0] = domainsftp.Entry{
		Name: "dir", Path: "/dir", Kind: domainsftp.KindDir, Size: 12, Mode: 0o755,
		ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	entries[1] = domainsftp.Entry{
		Name: "link", Path: "/link", Kind: domainsftp.KindDir, Symlink: true,
		Mode: 0o777, ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	files := &fakeFiles{
		root:    "/home/deploy",
		entries: entries,
		statEntry: domainsftp.Entry{
			Name: "big", Path: "/big", Kind: domainsftp.KindFile,
			Size: maxMCPReadFileBytes + 10, Mode: 0o644,
			ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		content: strings.Repeat("x", maxMCPReadFileBytes+10),
	}
	descriptions := []appsession.SessionDescription{{
		ID: "s1", Label: "web", HostID: "h1", Kind: appsession.KindSSH,
		Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive,
		Connected: true,
	}, {
		ID: "s2", Label: "local", Kind: appsession.KindLocal,
		Status: appsession.StatusRunning, Connected: true,
	}}
	bridge := newMCPReadBridge(fakeVault{}, fakeForwarding{},
		fakeSessions{descriptions: descriptions}, fakeTransfer{files: files})

	list := mustOK(t, callMap(t, bridge, "netcatty/sftp/list", map[string]any{"sessionId": "s1"}))
	if len(list["entries"].([]any)) != maxMCPListEntries || list["truncated"] != true {
		t.Fatalf("list = %d entries, truncated = %v",
			len(list["entries"].([]any)), list["truncated"])
	}
	first := list["entries"].([]any)[0].(map[string]any)
	if first["name"] != "dir" || first["type"] != "directory" ||
		first["size"] != "12 bytes" || first["permissions"] != "rwxr-xr-x" ||
		first["lastModified"] != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("directory entry = %v", first)
	}
	if _, leaked := first["path"]; leaked {
		t.Fatal("the list entry leaked the native path field")
	}
	link := list["entries"].([]any)[1].(map[string]any)
	if link["type"] != "symlink" || link["linkTarget"] != "directory" {
		t.Fatalf("symlink entry = %v", link)
	}
	if owner, present := first["owner"]; present {
		t.Fatalf("the owner appeared on an entry without one: %v", owner)
	}

	large := callMap(t, bridge, "netcatty/sftp/read",
		map[string]any{"sessionId": "s1", "path": "/big"})
	if large["ok"] != false || !strings.Contains(large["error"].(string), "maximum 10 MB") {
		t.Fatalf("oversized read = %v", large)
	}

	// A file the original accepts comes back whole, without a truncation marker.
	files.content = strings.Repeat("y", 300<<10)
	files.statEntry.Size = int64(len(files.content))
	read := mustOK(t, callMap(t, bridge, "netcatty/sftp/read",
		map[string]any{"sessionId": "s1", "path": "/big"}))
	if len(read["content"].(string)) != 300<<10 {
		t.Fatalf("read = %d bytes", len(read["content"].(string)))
	}
	if _, truncated := read["truncated"]; truncated {
		t.Fatalf("read = %v", read)
	}

	stat := mustOK(t, callMap(t, bridge, "netcatty/sftp/stat",
		map[string]any{"sessionId": "s1", "path": "/big"}))
	statRecord := stat["stat"].(map[string]any)
	if statRecord["name"] != "big" || statRecord["type"] != "file" ||
		statRecord["size"] != int64(300<<10) || statRecord["sizeKnown"] != true ||
		statRecord["lastModified"] != int64(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()) ||
		statRecord["permissions"] != "644" {
		t.Fatalf("stat = %v", statRecord)
	}
	home := mustOK(t, callMap(t, bridge, "netcatty/sftp/home", map[string]any{"sessionId": "s1"}))
	if home["homeDir"] != "/home/deploy" {
		t.Fatalf("home = %v", home)
	}
	local := callMap(t, bridge, "netcatty/sftp/list", map[string]any{"sessionId": "s2"})
	if local["ok"] != false {
		t.Fatalf("local session SFTP = %v", local)
	}
}

func TestMCPWriteToolsAndUnavailableSubsystemsAreExplicit(t *testing.T) {
	bridge := newMCPReadBridge(fakeVault{}, fakeForwarding{}, fakeSessions{}, fakeTransfer{})

	for _, method := range []string{"netcatty/exec", "public/vault/hosts/create", "netcatty/sftp/write"} {
		result := callMap(t, bridge, method, map[string]any{})
		if result["ok"] != false || !strings.Contains(result["error"].(string), "read-only") {
			t.Fatalf("%s = %v", method, result)
		}
	}
	if _, err := bridge.Call(context.Background(), "netcatty/notAThing", nil); err == nil {
		t.Fatal("an unknown method was accepted")
	}

	attachments := mustOK(t, callMap(t, bridge, "netcatty/listAttachments", nil))
	if len(attachments["attachments"].([]any)) != 0 {
		t.Fatalf("attachments = %v", attachments)
	}
	scripts := mustOK(t, callMap(t, bridge, "public/vault/scripts/list", nil))
	if len(scripts["scripts"].([]any)) != 0 {
		t.Fatalf("scripts = %v", scripts)
	}
	runs := mustOK(t, callMap(t, bridge, "public/vault/scripts/runs/list", nil))
	if len(runs["runs"].([]any)) != 0 {
		t.Fatalf("runs = %v", runs)
	}
	if poll := callMap(t, bridge, "netcatty/jobPoll", map[string]any{"jobId": "j"}); poll["ok"] != false {
		t.Fatalf("jobPoll = %v", poll)
	}
}

func TestMCPEnvironmentAndForwardingShapes(t *testing.T) {
	rule := domainforward.Rule{
		ID: "r1", Label: "db", Type: domainforward.TypeLocal, BindAddress: "127.0.0.1",
		LocalPort: 5432, RemoteHost: "db.internal", RemotePort: 5432, HostID: "h1",
		AutoStart: true, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	stopped := domainforward.Rule{ID: "r2", Label: "off", Type: domainforward.TypeDynamic}
	bridge := newMCPReadBridge(fakeVault{}, fakeForwarding{views: []appforwarding.View{
		{Rule: rule, Status: domainforward.StatusActive},
		{Rule: stopped, Status: domainforward.StatusInactive},
	}}, fakeSessions{}, fakeTransfer{})

	rules := mustOK(t, callMap(t, bridge, "public/portforward/rules/list", nil))
	if len(rules["rules"].([]any)) != 2 {
		t.Fatalf("rules = %v", rules)
	}
	first := rules["rules"].([]any)[0].(map[string]any)
	if first["status"] != "active" || first["autoReconnect"] != true || first["localPort"].(int) != 5432 {
		t.Fatalf("rule = %v", first)
	}
	tunnels := mustOK(t, callMap(t, bridge, "public/portforward/tunnels/list", nil))
	if len(tunnels["tunnels"].([]any)) != 1 {
		t.Fatalf("tunnels = %v", tunnels)
	}

	environment := callMap(t, bridge, "netcatty/getContext", nil)
	if environment["environment"] != "netcatty-terminal" || environment["hostCount"].(int) != 0 {
		t.Fatalf("environment = %v", environment)
	}
}
