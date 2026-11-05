// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const testAuthKey = "tskey-auth-test-secret-do-not-log"

// fakeBackend is the Network Provider's test double. It scripts start, status,
// and dial outcomes and records every call, so the state machine can be tested
// without a tailnet or a socket.
type fakeBackend struct {
	mu       sync.Mutex
	startFn  func(context.Context) (backendStatus, error)
	statusFn func(context.Context) (backendStatus, error)
	dialFn   func(context.Context, string, string) (net.Conn, error)
	started  chan struct{}
	startOne sync.Once

	networks  []string
	addresses []string
	dials     int
	closes    int
}

func (b *fakeBackend) Start(ctx context.Context) (backendStatus, error) {
	if b.started != nil {
		b.startOne.Do(func() { close(b.started) })
	}
	if b.startFn != nil {
		return b.startFn(ctx)
	}
	return backendStatus{Phase: appconnection.ProviderPhaseRunning, TailscaleIPs: []string{"100.64.0.2"}}, nil
}

func (b *fakeBackend) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	b.mu.Lock()
	b.dials++
	b.networks = append(b.networks, network)
	b.addresses = append(b.addresses, address)
	fn := b.dialFn
	b.mu.Unlock()
	if fn != nil {
		return fn(ctx, network, address)
	}
	server, client := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (b *fakeBackend) Status(ctx context.Context) (backendStatus, error) {
	if b.statusFn != nil {
		return b.statusFn(ctx)
	}
	return backendStatus{Phase: appconnection.ProviderPhaseRunning, TailscaleIPs: []string{"100.64.0.2"}}, nil
}

func (b *fakeBackend) Close() error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

func (b *fakeBackend) dialCalls() (int, []string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dials, append([]string(nil), b.networks...), append([]string(nil), b.addresses...)
}

// statusRecorder captures the status events a Provider emits.
type statusRecorder struct {
	mu       sync.Mutex
	statuses []Status
}

func (r *statusRecorder) observe(status Status) {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	r.mu.Unlock()
}

func (r *statusRecorder) all() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Status(nil), r.statuses...)
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ProfileID:    "net-1",
		Label:        "Office",
		Kind:         domainvault.NetworkKindTailscale,
		StateBaseDir: t.TempDir(),
		AuthKey:      testAuthKey,
	}
}

func fakeProvider(t *testing.T, cfg Config, fake *fakeBackend) (*Provider, *statusRecorder) {
	t.Helper()
	recorder := &statusRecorder{}
	provider, err := NewProvider(cfg,
		WithStatusObserver(recorder.observe),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return provider, recorder
}

func typedProviderError(t *testing.T, err error) *appconnection.Error {
	t.Helper()
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a typed connection error", err)
	}
	if typed.Provider == nil {
		t.Fatalf("typed error %v carries no ProviderInfo", typed)
	}
	return typed
}

func TestAuthKeyStartReachesRunningAndDialUsesTheBackend(t *testing.T) {
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{
				Phase:        appconnection.ProviderPhaseRunning,
				TailscaleIPs: []string{"100.64.0.2", "fd7a:115c:a1e0::2"},
				DNSName:      "natcatty-office.tailnet.ts.net",
			}, nil
		},
	}
	provider, recorder := fakeProvider(t, testConfig(t), fake)

	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
		t.Fatalf("Phase = %q, want running", got)
	}
	last := recorder.all()
	if len(last) == 0 || last[len(last)-1].Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("status events = %+v, want a final running event", last)
	}
	if last[len(last)-1].DNSName != "natcatty-office.tailnet.ts.net" {
		t.Fatalf("DNSName = %q", last[len(last)-1].DNSName)
	}

	conn, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
	calls, networks, addresses := fake.dialCalls()
	if calls != 1 || networks[0] != "tcp" || addresses[0] != "peer.tailnet.ts.net:22" {
		t.Fatalf("backend dials = %d %v %v", calls, networks, addresses)
	}
}

func TestMissingAuthKeyReportsNeedsLoginWithAuthURL(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = ""
	authURL := "https://login.tailscale.com/a/example"
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{
				Phase:   appconnection.ProviderPhaseNeedsLogin,
				AuthURL: authURL,
			}, nil
		},
	}
	provider, recorder := fakeProvider(t, cfg, fake)

	err := provider.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded without an Auth Key")
	}
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("error %v does not wrap ErrNeedsLogin", err)
	}
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider", typed.Kind)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseNeedsLogin {
		t.Fatalf("Phase = %q, want needs_login", typed.Provider.Phase)
	}
	if typed.Provider.ProfileID != "net-1" {
		t.Fatalf("ProfileID = %q, want net-1", typed.Provider.ProfileID)
	}
	if strings.Contains(err.Error(), authURL) {
		t.Fatalf("error leaked the Auth URL: %q", err)
	}

	events := recorder.all()
	found := false
	for _, event := range events {
		if event.Phase == appconnection.ProviderPhaseNeedsLogin && event.AuthURL == authURL {
			found = true
		}
	}
	if !found {
		t.Fatalf("status events = %+v, want needs_login with the Auth URL", events)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want a login-needing start to release the node", fake.closeCount())
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes after Close = %d, want exactly one", fake.closeCount())
	}
}

func TestStartFailureIsTypedWithProfileContext(t *testing.T) {
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{}, errors.New("control server refused the node")
		},
	}
	provider, _ := fakeProvider(t, testConfig(t), fake)

	err := provider.Start(context.Background())
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider", typed.Kind)
	}
	if typed.Provider.ProfileID != "net-1" || typed.Provider.Label != "Office" ||
		typed.Provider.Kind != string(domainvault.NetworkKindTailscale) {
		t.Fatalf("ProviderInfo = %+v", typed.Provider)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseError {
		t.Fatalf("Phase = %q, want error", typed.Provider.Phase)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want the failed node released", fake.closeCount())
	}
}

func TestCanceledStartLeavesNoLiveServer(t *testing.T) {
	entered := make(chan struct{})
	fake := &fakeBackend{
		started: entered,
		startFn: func(ctx context.Context) (backendStatus, error) {
			<-ctx.Done()
			return backendStatus{}, ctx.Err()
		},
	}
	provider, _ := fakeProvider(t, testConfig(t), fake)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- provider.Start(ctx) }()
	<-entered
	cancel()

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context.Canceled", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want the canceled start released", fake.closeCount())
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseStopped {
		t.Fatalf("Phase = %q, want stopped", got)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes after Close = %d, want exactly one", fake.closeCount())
	}
}

func TestCloseIsIdempotentAndClosesTheServerOnce(t *testing.T) {
	fake := &fakeBackend{}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want exactly one", fake.closeCount())
	}
	if _, err := provider.DialContext(context.Background(), "tcp", "peer:22"); err == nil {
		t.Fatal("DialContext succeeded on a closed provider")
	}
}

func TestDialRejectsNetworksOtherThanTCP(t *testing.T) {
	fake := &fakeBackend{}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, network := range []string{"udp", "udp4", "udp6", "unix"} {
		_, err := provider.DialContext(context.Background(), network, "peer:22")
		if err == nil {
			t.Fatalf("DialContext(%q) succeeded", network)
		}
		typed := typedProviderError(t, err)
		if typed.Kind != appconnection.KindUnsupported {
			t.Fatalf("DialContext(%q) Kind = %q, want unsupported", network, typed.Kind)
		}
	}
	if calls, _, _ := fake.dialCalls(); calls != 0 {
		t.Fatalf("backend dials = %d, want none", calls)
	}
}

func TestAuthKeyNeverAppearsInErrorsLogsOrTheStateDirectory(t *testing.T) {
	cfg := testConfig(t)
	var logMu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{}, fmt.Errorf("control rejected key %s", testAuthKey)
		},
	}
	recorder := &statusRecorder{}
	provider, err := NewProvider(cfg,
		WithLogf(logf),
		WithStatusObserver(recorder.observe),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	startErr := provider.Start(context.Background())
	if startErr == nil {
		t.Fatal("Start succeeded with a failing backend")
	}
	if strings.Contains(startErr.Error(), testAuthKey) {
		t.Fatalf("start error leaked the Auth Key: %q", startErr)
	}
	for _, event := range recorder.all() {
		if event.Err != nil && strings.Contains(event.Err.Error(), testAuthKey) {
			t.Fatalf("status event leaked the Auth Key: %q", event.Err)
		}
	}

	redactingLogf(logf, testAuthKey)("control rejected key %s", testAuthKey)
	logMu.Lock()
	for _, line := range logs {
		if strings.Contains(line, testAuthKey) {
			t.Fatalf("log line leaked the Auth Key: %q", line)
		}
	}
	logMu.Unlock()

	stateDir := provider.cfg.stateDir
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			t.Fatalf("read state file: %v", err)
		}
		if strings.Contains(string(data), testAuthKey) {
			t.Fatalf("state file %q contains the Auth Key", entry.Name())
		}
	}
	if strings.Contains(stateDir, testAuthKey) {
		t.Fatalf("state path contains the Auth Key: %q", stateDir)
	}
}

func TestBackendConfigurationIsDerivedFromTheProfile(t *testing.T) {
	cfg := testConfig(t)
	cfg.Hostname = "My Node!"
	cfg.ControlURL = "https://headscale.example.com"
	cfg.Ephemeral = true
	cfg.AdvertiseTags = []string{"tag:prod", " tag:prod ", "tag:ops"}

	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	raw, err := newTSNetBackend(provider.cfg)
	if err != nil {
		t.Fatalf("newTSNetBackend: %v", err)
	}
	backend := raw.(*tsnetBackend)
	if backend.server.Dir != provider.cfg.stateDir {
		t.Fatalf("server.Dir = %q, want %q", backend.server.Dir, provider.cfg.stateDir)
	}
	if backend.server.AuthKey != testAuthKey {
		t.Fatalf("server.AuthKey = %q, want the profile key", backend.server.AuthKey)
	}
	if backend.server.Hostname != "my-node" {
		t.Fatalf("server.Hostname = %q, want my-node", backend.server.Hostname)
	}
	if backend.server.ControlURL != cfg.ControlURL || !backend.server.Ephemeral {
		t.Fatalf("server ControlURL/Ephemeral = %q/%t", backend.server.ControlURL, backend.server.Ephemeral)
	}
	if len(backend.server.AdvertiseTags) != 2 ||
		backend.server.AdvertiseTags[0] != "tag:prod" || backend.server.AdvertiseTags[1] != "tag:ops" {
		t.Fatalf("AdvertiseTags = %v", backend.server.AdvertiseTags)
	}
	if isSystemTailscaleDir(backend.server.Dir) {
		t.Fatalf("server.Dir points at a system Tailscale directory: %q", backend.server.Dir)
	}
}

func TestStateDirectoryIsIsolatedPerProfileAndPrivate(t *testing.T) {
	base := t.TempDir()
	first, err := prepareStateDir(base, "net-one")
	if err != nil {
		t.Fatalf("prepareStateDir first: %v", err)
	}
	second, err := prepareStateDir(base, "net-two")
	if err != nil {
		t.Fatalf("prepareStateDir second: %v", err)
	}
	if first == second {
		t.Fatalf("two profiles share the state directory %q", first)
	}
	for _, dir := range []string{first, second} {
		if !within(base, dir) {
			t.Fatalf("state directory %q escapes %q", dir, base)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %q: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%q mode = %o, want 700", dir, info.Mode().Perm())
		}
	}
}

func TestStateDirectoryRejectsPathTraversalProfileIDs(t *testing.T) {
	base := t.TempDir()
	dir, err := prepareStateDir(base, "../../escape")
	if err != nil {
		t.Fatalf("prepareStateDir: %v", err)
	}
	if !within(base, dir) {
		t.Fatalf("state directory %q escapes %q", dir, base)
	}
	if strings.Contains(filepath.Base(dir), string(filepath.Separator)) {
		t.Fatalf("state directory name still contains a separator: %q", dir)
	}
	if filepath.Dir(dir) != filepath.Join(base, "tailscale") {
		t.Fatalf("state directory parent = %q", filepath.Dir(dir))
	}
}

func TestSystemTailscaleDirectoriesAreRecognized(t *testing.T) {
	if isSystemTailscaleDir(t.TempDir()) {
		t.Fatal("a temporary directory was classified as system Tailscale state")
	}
	roots := systemTailscaleDirs()
	if len(roots) == 0 {
		t.Skip("this platform exposes no known system Tailscale directory")
	}
	for _, root := range roots {
		if !isSystemTailscaleDir(root) || !isSystemTailscaleDir(filepath.Join(root, "tailscaled.state")) {
			t.Fatalf("%q is not classified as system Tailscale state", root)
		}
	}
}

func TestNodeHostnameSanitization(t *testing.T) {
	tests := map[string]string{
		"My Node!":               "my-node",
		"  office.tailnet  ":     "office-tailnet",
		"___":                    "",
		"UPPER_case-123":         "upper-case-123",
		"控制台":                    "",
		strings.Repeat("a", 100): strings.Repeat("a", 63),
		"a..b__c":                "a-b-c",
		"nodeé":                  "node",
	}
	for input, want := range tests {
		if got := sanitizeHostname(input); got != want {
			t.Errorf("sanitizeHostname(%q) = %q, want %q", input, got, want)
		}
	}

	cfg := testConfig(t)
	cfg.Hostname = ""
	cfg.Label = "My Office"
	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if provider.cfg.nodeHostname != "natcatty-my-office" {
		t.Fatalf("derived hostname = %q", provider.cfg.nodeHostname)
	}
}
