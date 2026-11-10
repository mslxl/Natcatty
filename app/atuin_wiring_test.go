package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	apphostatuin "github.com/mslxl/natcatty/application/hostatuin"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestCompositionWiresTheProductionAtuinFactory parses the composition root
// and fails if either constructor receives nil or a factory that did not come
// from adapters/atuin/remote.NewFactory. Both constructors must receive the
// same non-nil identifier.
func TestCompositionWiresTheProductionAtuinFactory(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	var helper *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "newHistorySyncServices" {
			helper = function
			break
		}
	}
	if helper == nil {
		t.Fatal("app.go has no newHistorySyncServices composition helper")
	}

	factoryName := ""
	ast.Inspect(helper.Body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && pkg.Name == "atuinremote" && selector.Sel.Name == "NewFactory" {
			factoryName = name.Name
		}
		return true
	})
	if factoryName == "" {
		t.Fatal("the composition helper does not construct the adapter factory with atuinremote.NewFactory")
	}

	constructors := map[string]int{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "historysync" {
			return true
		}
		switch selector.Sel.Name {
		case "NewAccountService", "NewService":
			constructors[selector.Sel.Name]++
			if len(call.Args) < 4 {
				t.Errorf("%s has %d arguments, want the factory as the fourth",
					selector.Sel.Name, len(call.Args))
				return true
			}
			factory, ok := call.Args[3].(*ast.Ident)
			if !ok || factory.Name != factoryName {
				t.Errorf("%s does not receive the production factory %q",
					selector.Sel.Name, factoryName)
			}
		}
		return true
	})
	for _, name := range []string{"NewAccountService", "NewService"} {
		if constructors[name] != 1 {
			t.Errorf("%s call sites = %d, want exactly 1", name, constructors[name])
		}
	}

	// The helper must be the one used by production composition, not a test-only
	// path that leaves the real run with a nil factory.
	used := false
	for _, declaration := range file.Decls {
		runner, ok := declaration.(*ast.FuncDecl)
		if !ok || runner.Name.Name != "run" || runner.Body == nil {
			continue
		}
		ast.Inspect(runner.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if ok && ident.Name == "newHistorySyncServices" {
				used = true
			}
			return true
		})
	}
	if !used {
		t.Fatal("the app composition does not call newHistorySyncServices")
	}
}

// TestCompositionWiresTheAutocompleteReload parses app.go and fails when the
// production atuinHistoryRecorder literal omits reload, sets it to anything but
// a function literal, or has a body that does not call
// autocompleteService.Load. The end-to-end import test injects its own reload
// closure, so this is what pins the production composition root.
func TestCompositionWiresTheAutocompleteReload(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	var recorder *ast.CompositeLit
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		ident, ok := literal.Type.(*ast.Ident)
		if ok && ident.Name == "atuinHistoryRecorder" {
			recorder = literal
		}
		return true
	})
	if recorder == nil {
		t.Fatal("app.go has no atuinHistoryRecorder composite literal")
	}
	for _, element := range recorder.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok || key.Name != "reload" {
			continue
		}
		literal, ok := field.Value.(*ast.FuncLit)
		if !ok {
			t.Fatal("the production atuinHistoryRecorder.reload is not a function literal")
		}
		// Only a call the closure executes counts. Inspecting the literal's
		// top-level expression statements rejects a Load call buried in a nested
		// literal that reload never invokes.
		callsLoad := false
		for _, statement := range literal.Body.List {
			expression, ok := statement.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expression.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Load" {
				continue
			}
			if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "autocompleteService" {
				callsLoad = true
				break
			}
		}
		if !callsLoad {
			t.Fatal("the production atuinHistoryRecorder.reload does not call autocompleteService.Load")
		}
		return
	}
	t.Fatal("the production atuinHistoryRecorder literal does not set reload")
}

// TestAtuinProbeReportMapsServiceFailures pins the glue between the Host-Atuin
// service result and the Session port: a failed probe keeps the Host present so
// the hook stays skipped, and Failed is set only when the Host is present and
// the probe returned an error.
func TestAtuinProbeReportMapsServiceFailures(t *testing.T) {
	probeErr := errors.New("unknown Atuin schema")
	cases := []struct {
		name   string
		result apphostatuin.ProbeResult
		err    error
		want   appsession.HostAtuinProbe
	}{
		{
			name: "absent with error",
			err:  probeErr,
			want: appsession.HostAtuinProbe{},
		},
		{
			name:   "present without error",
			result: apphostatuin.ProbeResult{Present: true, Imported: 3},
			want:   appsession.HostAtuinProbe{Present: true},
		},
		{
			name:   "present with error",
			result: apphostatuin.ProbeResult{Present: true},
			err:    probeErr,
			want:   appsession.HostAtuinProbe{Present: true, Failed: true},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := atuinProbeReport(testCase.result, testCase.err); got != testCase.want {
				t.Fatalf("atuinProbeReport(%+v, %v) = %+v, want %+v",
					testCase.result, testCase.err, got, testCase.want)
			}
		})
	}
}

// TestAtuinHostPolicyHostRefCoversSSHBootstrappedHosts pins which Sessions can
// be probed: saved SSH, Mosh, and ET Hosts bootstrap over SSH. Telnet, Local,
// unnamed kinds, and unsaved Hosts keep the journaled capture path.
func TestAtuinHostPolicyHostRefCoversSSHBootstrappedHosts(t *testing.T) {
	policy := atuinHostPolicy{service: apphostatuin.New()}
	cases := []struct {
		name string
		spec appsession.StartSpec
		want bool
	}{
		{name: "ssh", spec: appsession.StartSpec{Kind: appsession.KindSSH, HostID: "host-1"}, want: true},
		{name: "mosh", spec: appsession.StartSpec{Kind: appsession.KindMosh, HostID: "host-1"}, want: true},
		{name: "et", spec: appsession.StartSpec{Kind: appsession.KindET, HostID: "host-1"}, want: true},
		{name: "telnet", spec: appsession.StartSpec{Kind: appsession.KindTelnet, HostID: "host-1"}, want: false},
		{name: "local", spec: appsession.StartSpec{Kind: appsession.KindLocal, HostID: "host-1"}, want: false},
		{name: "empty kind", spec: appsession.StartSpec{HostID: "host-1"}, want: false},
		{name: "empty host id", spec: appsession.StartSpec{Kind: appsession.KindSSH}, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, ok := policy.hostRef(testCase.spec); ok != testCase.want {
				t.Fatalf("hostRef(%+v) = %v, want %v", testCase.spec, ok, testCase.want)
			}
		})
	}

	withoutService := atuinHostPolicy{}
	if _, ok := withoutService.hostRef(appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1",
	}); ok {
		t.Fatal("a policy without a service accepted a Host")
	}
}

// TestAtuinProbeProtocolCoversSSHBootstrappedHosts pins the protocol gate used
// by the read-only channel: SSH, Mosh, and ET bootstrap over SSH and can be
// probed; Telnet and Local cannot.
func TestAtuinProbeProtocolCoversSSHBootstrappedHosts(t *testing.T) {
	cases := []struct {
		protocol domainvault.Protocol
		want     bool
	}{
		{protocol: domainvault.ProtocolSSH, want: true},
		{protocol: domainvault.ProtocolMosh, want: true},
		{protocol: domainvault.ProtocolET, want: true},
		{protocol: domainvault.ProtocolTelnet, want: false},
		{protocol: domainvault.ProtocolLocal, want: false},
		{protocol: "", want: false},
	}
	for _, testCase := range cases {
		if got := atuinProbeProtocol(testCase.protocol); got != testCase.want {
			t.Errorf("atuinProbeProtocol(%q) = %v, want %v",
				testCase.protocol, got, testCase.want)
		}
	}
}

type atuinWiringSettings struct {
	mu      sync.Mutex
	history domainpreferences.History
}

func (s *atuinWiringSettings) HistorySettings() domainpreferences.History {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history
}

func (s *atuinWiringSettings) UpdateHistorySettings(update func(*domainpreferences.History)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.history)
	return nil
}

type atuinWiringSecrets struct {
	mu     sync.Mutex
	values map[historysync.SecretRef]string
}

func newAtuinWiringSecrets() *atuinWiringSecrets {
	return &atuinWiringSecrets{values: make(map[historysync.SecretRef]string)}
}

func (s *atuinWiringSecrets) Mode() historysync.SecretMode {
	return historysync.SecretModeOS
}

func (s *atuinWiringSecrets) Has(_ context.Context, ref historysync.SecretRef) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.values[ref]
	return ok, nil
}

func (s *atuinWiringSecrets) Get(_ context.Context, ref historysync.SecretRef) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[ref]
	if !ok {
		return "", historysync.ErrSecretNotFound
	}
	return value, nil
}

func (s *atuinWiringSecrets) Set(_ context.Context, ref historysync.SecretRef, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[ref] = value
	return nil
}

func (s *atuinWiringSecrets) Delete(_ context.Context, ref historysync.SecretRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref)
	return nil
}

// atuinWiringStore is both the account metadata store and the sync store. It
// holds no records, so the integration run never uploads or downloads.
type atuinWiringStore struct {
	mu       sync.Mutex
	metadata domainhistory.Metadata
}

func (s *atuinWiringStore) HistoryMetadata(context.Context) (domainhistory.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata, nil
}

func (s *atuinWiringStore) SaveHistoryMetadata(_ context.Context, metadata domainhistory.Metadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata = metadata
	return nil
}

func (s *atuinWiringStore) ListHistoryRecords(context.Context) ([]domainhistory.Record, error) {
	return nil, nil
}

func (s *atuinWiringStore) ApplyRemotePage(context.Context, historysync.RemotePage) error {
	return nil
}

func (s *atuinWiringStore) MarkUploaded(context.Context, []string, time.Time,
	domainhistory.SeriesCheckpoint) error {
	return nil
}

func (s *atuinWiringStore) ResetUploaded(context.Context, domainhistory.SeriesCheckpoint) error {
	return nil
}

// TestComposedAtuinServicesReachALoopbackServer proves the services built by
// the composition helper carry a real adapter factory, not nil: account login
// and a sync run both reach a loopback Atuin v18 server. This is the app-level
// replacement for a full GUI acceptance run.
func TestComposedAtuinServicesReachALoopbackServer(t *testing.T) {
	var hitsMu sync.Mutex
	hits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Atuin-Version", historysync.PinnedAtuinVersion)
		writer.Header().Set("Content-Type", "application/json")
		hitsMu.Lock()
		hits[request.URL.Path]++
		hitsMu.Unlock()
		switch request.URL.Path {
		case "/login":
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"session": "loopback-session", "auth": "cli", "user_id": "user-1",
			})
		case "/api/v0/me":
			_ = json.NewEncoder(writer).Encode(map[string]string{"username": "alice"})
		case "/api/v0/capabilities":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"version": "1",
				"capabilities": map[string]any{
					historysync.CapabilityServer:   map[string]int{"version": 1},
					historysync.CapabilityPageSize: map[string]int{"version": 1, "page_size": 100},
				},
			})
		case "/api/v0/record":
			_ = json.NewEncoder(writer).Encode(map[string]any{"hosts": map[string]any{}})
		case "/api/v0/record/next":
			_ = json.NewEncoder(writer).Encode([]any{})
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	settings := &atuinWiringSettings{history: domainpreferences.Defaults().History}
	settings.history.SyncEnabled = true
	settings.history.SyncAddress = server.URL
	settings.history.Username = "alice"
	secrets := newAtuinWiringSecrets()
	store := &atuinWiringStore{}

	account, syncService := newHistorySyncServices(settings, secrets, store, store,
		func() bool { return false })
	if account == nil || syncService == nil {
		t.Fatal("the composition helper returned a nil service")
	}
	t.Cleanup(syncService.Stop)

	ctx := context.Background()
	status, err := account.Login(ctx, historysync.LoginRequest{
		ServerURL: server.URL, Username: "alice", Password: "hunter2",
	})
	if err != nil {
		// Never print err: a regression here could be ErrRemoteUnavailable (the
		// nil-factory bug) or a credential-bearing adapter error.
		t.Fatalf("account login through the composed factory failed: %T", err)
	}
	if !status.Configured || status.ServerVersion != historysync.PinnedAtuinVersion {
		t.Fatalf("login status = %+v", status)
	}

	key := make([]byte, domainhistory.MasterKeyBytes)
	for index := range key {
		key[index] = byte(index + 1)
	}
	if err := secrets.Set(ctx, historysync.MasterKeyRef(),
		base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatalf("store master key: %v", err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	metadata.KeyFingerprint = domainhistory.KeyFingerprint(key)
	if err := store.SaveHistoryMetadata(ctx, metadata); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	result, err := syncService.SyncNow(ctx)
	if err != nil {
		t.Fatalf("sync through the composed factory failed: %T", err)
	}
	if result.LastSyncAt.IsZero() {
		t.Fatal("the sync run did not complete")
	}

	hitsMu.Lock()
	loginCalls := hits["/login"]
	meCalls, recordCalls := hits["/api/v0/me"], hits["/api/v0/record"]
	hitsMu.Unlock()
	// The account service logs in and inspects the account; the sync service
	// then authenticates with the stored token and reads record status. Both
	// therefore opened the real loopback remote.
	if loginCalls < 1 || meCalls < 1 || recordCalls < 2 {
		t.Fatalf("loopback requests: /login=%d /api/v0/me=%d /api/v0/record=%d",
			loginCalls, meCalls, recordCalls)
	}
}
