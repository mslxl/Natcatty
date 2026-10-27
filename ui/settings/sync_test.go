package settings

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeSyncService is the application seam the Settings sync surface drives.
// It never touches the network or the Vault.
type fakeSyncService struct {
	mu sync.Mutex

	snapshot appsync.Snapshot

	connectErr    error
	disconnectErr error
	autoErr       error
	strategyErr   error
	syncErr       error

	connectCalls    int
	connectPassword string
	disconnectCalls int
	syncCalls       int
	autoCalls       []bool
	strategies      []domainsync.Strategy
}

func newFakeSyncService() *fakeSyncService {
	return &fakeSyncService{snapshot: appsync.Snapshot{
		Connected: true,
		Login:     "octocat",
		Strategy:  domainsync.StrategyNewest,
		Status:    appsync.StatusSynced,
	}}
}

func (f *fakeSyncService) Snapshot() appsync.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeSyncService) Load(context.Context) error { return nil }
func (f *fakeSyncService) SetPassword(string) error   { return nil }
func (f *fakeSyncService) ClearPassword()             {}

func (f *fakeSyncService) SetAutoSync(_ context.Context, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.autoCalls = append(f.autoCalls, enabled)
	if f.autoErr != nil {
		return f.autoErr
	}
	f.snapshot.AutoSync = enabled
	return nil
}

func (f *fakeSyncService) SetStrategy(_ context.Context, strategy domainsync.Strategy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.strategies = append(f.strategies, strategy)
	if f.strategyErr != nil {
		return f.strategyErr
	}
	f.snapshot.Strategy = strategy
	return nil
}

func (f *fakeSyncService) Connect(_ context.Context, password string, onDevice func(appsync.DeviceCode)) error {
	f.mu.Lock()
	f.connectCalls++
	f.connectPassword = password
	connectErr := f.connectErr
	if connectErr == nil {
		f.snapshot.Connected = true
		f.snapshot.Login = "octocat"
		f.snapshot.HasPassword = true
	}
	f.mu.Unlock()
	if onDevice != nil {
		onDevice(appsync.DeviceCode{UserCode: "ABCD-1234"})
	}
	return connectErr
}

func (f *fakeSyncService) Disconnect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls++
	if f.disconnectErr != nil {
		return f.disconnectErr
	}
	f.snapshot.Connected = false
	return nil
}

func (f *fakeSyncService) Sync(_ context.Context, trigger appsync.Trigger) (appsync.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncCalls++
	if f.syncErr != nil {
		return appsync.Result{Trigger: trigger}, f.syncErr
	}
	return appsync.Result{Trigger: trigger, Action: domainsync.ActionNone}, nil
}

func waitSyncIdle(t *testing.T, state *SyncState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for state.Busy() {
		if !time.Now().Before(deadline) {
			t.Fatal("the sync worker did not finish in 5s")
		}
		state.Flush()
		<-ticker.C
	}
	state.Flush()
}

func TestSyncStateConnectsAndClearsThePassword(t *testing.T) {
	service := newFakeSyncService()
	service.snapshot.Connected = false
	state := NewSyncState(service)
	state.SetPassword("master password")
	state.Connect()

	if !state.Busy() {
		t.Fatal("Connect did not mark the surface busy")
	}
	waitSyncIdle(t, state)

	service.mu.Lock()
	calls, password := service.connectCalls, service.connectPassword
	service.mu.Unlock()
	if calls != 1 || password != "master password" {
		t.Fatalf("connect calls = %d password = %q", calls, password)
	}
	if state.Password() != "" {
		t.Fatalf("password field = %q, want cleared", state.Password())
	}
	if state.Error() != "" {
		t.Fatalf("error = %q", state.Error())
	}
}

func TestSyncStateSurfacesWorkerErrors(t *testing.T) {
	service := newFakeSyncService()
	service.syncErr = errors.New("cloud is down")
	state := NewSyncState(service)
	state.SetPassword("master")
	state.SyncNow()
	waitSyncIdle(t, state)

	if got := state.Error(); got != "cloud is down" {
		t.Fatalf("error = %q", got)
	}
	if state.Password() != "master" {
		t.Fatalf("password = %q, want retained for retry", state.Password())
	}
}

func TestSyncStatePersistsAutoSyncAndStrategy(t *testing.T) {
	service := newFakeSyncService()
	state := NewSyncState(service)
	state.SetAutoSync(true)
	waitSyncIdle(t, state)
	state.SetStrategy(domainsync.StrategyRemote)
	waitSyncIdle(t, state)

	service.mu.Lock()
	auto := append([]bool(nil), service.autoCalls...)
	strategies := append([]domainsync.Strategy(nil), service.strategies...)
	service.mu.Unlock()
	if len(auto) != 1 || !auto[0] {
		t.Fatalf("auto calls = %v", auto)
	}
	if len(strategies) != 1 || strategies[0] != domainsync.StrategyRemote {
		t.Fatalf("strategy calls = %v", strategies)
	}
}

func TestSyncSectionShowsTheSetupSurface(t *testing.T) {
	m := locales.In("en")
	config := Config{Sync: NewSyncState(&fakeSyncService{})}
	tester := ui.NewTester(viewAt(AreaSync, config), 720, 480)
	tester.Frame()

	for _, want := range []string{
		m.SettingsSyncNotConnected(),
		m.SettingsSyncMasterPassword(),
		m.SettingsSyncConnect(),
		m.SettingsSyncSecurityNote(),
	} {
		if !tester.HasText(want) {
			t.Errorf("%q is not rendered: %q", want, tester.Texts())
		}
	}
}

func TestSyncSectionShowsTheDeviceCodeBeforeConnecting(t *testing.T) {
	m := locales.In("en")
	service := &fakeSyncService{snapshot: appsync.Snapshot{DeviceCode: appsync.DeviceCode{
		UserCode:        "ABCD-1234",
		VerificationURI: "https://github.test/device",
	}}}
	config := Config{Sync: NewSyncState(service)}
	tester := ui.NewTester(viewAt(AreaSync, config), 720, 480)
	tester.Frame()

	for _, want := range []string{
		m.SettingsSyncDeviceCode(),
		"ABCD-1234",
		m.SettingsSyncDeviceCodeHint("https://github.test/device"),
	} {
		if !tester.HasText(want) {
			t.Errorf("%q is not rendered while authorizing: %q", want, tester.Texts())
		}
	}
}

func TestSyncSectionLocalizesWorkerErrors(t *testing.T) {
	m := locales.In("en")
	service := newFakeSyncService()
	service.syncErr = errors.New("sync: raw implementation detail")
	state := NewSyncState(service)
	state.SetPassword("master")
	state.SyncNow()
	waitSyncIdle(t, state)

	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 720, 480)
	tester.Frame()
	if !tester.HasText(m.SettingsSyncOperationFailed()) {
		t.Errorf("the generic localized error is missing: %q", tester.Texts())
	}
	if tester.HasText(service.syncErr.Error()) {
		t.Errorf("raw Go error text was rendered: %q", tester.Texts())
	}
}

// TestSyncProviderPromptsUseTheAccessibleDangerTextColor renders both
// providers' plain-HTTP confirmation prompts and checks the warning body text
// against the pixels, so a provider cannot keep the raw theme danger token.
func TestSyncProviderPromptsUseTheAccessibleDangerTextColor(t *testing.T) {
	m := locales.In("en")
	want := syncDangerTextColor(ui.LightTheme())
	raw := ui.LightTheme().Danger

	webdavService := newFakeWebDAVSyncService()
	webdavService.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderWebDAV, HasPassword: true,
		WebDAVEndpoint: "http://dav.test/dav", WebDAVAuthType: domainsync.WebDAVAuthBasic,
		WebDAVInsecure: true,
	}
	webdavState := NewSyncState(webdavService)
	webdavState.SyncNow()
	waitSyncIdle(t, webdavState)

	s3Service := newFakeS3SyncService()
	s3Service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderS3, HasPassword: true,
		S3Endpoint: "http://s3.test", S3Region: "us-east-1", S3Bucket: "vault",
		S3Insecure: true, Status: appsync.StatusFailed, Problem: appsync.ProblemInsecureEndpoint,
	}
	s3State := NewSyncState(s3Service)
	s3State.SyncNow()
	waitSyncIdle(t, s3State)

	for _, test := range []struct {
		name    string
		state   *SyncState
		warning string
	}{
		{"webdav", webdavState, m.SettingsSyncWebDAVInsecureWarning()},
		{"s3", s3State, m.SettingsSyncS3InsecureWarning()},
	} {
		t.Run(test.name, func(t *testing.T) {
			tester := ui.NewTester(viewAt(AreaSync, Config{Sync: test.state}), 1100, 720)
			tester.Frame()
			rect, ok := tester.Find(test.warning)
			if !ok {
				t.Fatalf("the confirmation warning %q is not rendered: %q", test.warning, tester.Texts())
			}
			if !testerRectHasColor(tester, rect, want) {
				t.Errorf("the confirmation warning does not use the accessible danger text color %v", want)
			}
			if testerRectHasColor(tester, rect, raw) {
				t.Errorf("the confirmation warning still uses the raw theme danger text color %v", raw)
			}
		})
	}
}

// testerRectHasColor reports whether any pixel in rect paints color exactly.
// Glyph cores carry the text color; anti-aliased edges blend toward the fill.
func testerRectHasColor(tester *ui.Tester, rect ui.Rect, color ui.Color) bool {
	img := tester.Image()
	left, top := max(int(rect.X), img.Rect.Min.X), max(int(rect.Y), img.Rect.Min.Y)
	right, bottom := min(int(rect.X+rect.W), img.Rect.Max.X), min(int(rect.Y+rect.H), img.Rect.Max.Y)
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			offset := img.PixOffset(x, y)
			if img.Pix[offset] == color.R && img.Pix[offset+1] == color.G && img.Pix[offset+2] == color.B {
				return true
			}
		}
	}
	return false
}
