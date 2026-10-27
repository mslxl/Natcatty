package settings

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeWebDAVSyncService adds the WebDAV provider capability to the existing
// fake. It never touches the network.
type fakeWebDAVSyncService struct {
	*fakeSyncService

	webdavMu        sync.Mutex
	webdavSettings  appsync.WebDAVSettings
	connectSettings []appsync.WebDAVSettings
	connectConfirms []bool
	connectErr      error
	confirmCalls    int
	confirmed       bool
}

func newFakeWebDAVSyncService() *fakeWebDAVSyncService {
	return &fakeWebDAVSyncService{fakeSyncService: newFakeSyncService()}
}

func (f *fakeWebDAVSyncService) SetProvider(_ context.Context, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot.Provider = appsync.NormalizeProvider(provider)
	f.snapshot.Connected = false
	return nil
}

func (f *fakeWebDAVSyncService) ConnectWebDAV(_ context.Context, _ string, settings appsync.WebDAVSettings, confirmInsecure bool) error {
	f.webdavMu.Lock()
	f.connectSettings = append(f.connectSettings, settings)
	f.connectConfirms = append(f.connectConfirms, confirmInsecure)
	connectErr := f.connectErr
	f.webdavMu.Unlock()
	if connectErr != nil {
		return connectErr
	}
	if strings.HasPrefix(settings.Endpoint, "http://") && !confirmInsecure {
		return appsync.ErrInsecureEndpoint
	}
	f.mu.Lock()
	f.snapshot.Connected = true
	f.snapshot.Provider = appsync.ProviderWebDAV
	f.snapshot.Login = settings.Username + "@dav.test"
	f.snapshot.HasPassword = true
	f.snapshot.WebDAVEndpoint = settings.Endpoint
	f.snapshot.WebDAVAuthType = settings.AuthType
	f.snapshot.WebDAVUsername = settings.Username
	f.snapshot.WebDAVInsecure = strings.HasPrefix(settings.Endpoint, "http://")
	f.mu.Unlock()
	f.webdavMu.Lock()
	f.webdavSettings = appsync.WebDAVSettings{
		Endpoint: settings.Endpoint, AuthType: settings.AuthType, Username: settings.Username,
	}
	f.webdavMu.Unlock()
	return nil
}

func (f *fakeWebDAVSyncService) WebDAVSettings() appsync.WebDAVSettings {
	f.webdavMu.Lock()
	defer f.webdavMu.Unlock()
	return f.webdavSettings
}

func (f *fakeWebDAVSyncService) ConfirmInsecureEndpoint(context.Context) bool {
	f.webdavMu.Lock()
	defer f.webdavMu.Unlock()
	f.confirmCalls++
	f.confirmed = true
	return true
}

func (f *fakeWebDAVSyncService) Sync(_ context.Context, trigger appsync.Trigger) (appsync.Result, error) {
	f.mu.Lock()
	f.syncCalls++
	insecure := strings.HasPrefix(f.snapshot.WebDAVEndpoint, "http://")
	syncErr := f.syncErr
	f.mu.Unlock()
	if syncErr != nil {
		return appsync.Result{Trigger: trigger}, syncErr
	}
	if trigger == appsync.TriggerManual && insecure {
		f.webdavMu.Lock()
		confirmed := f.confirmed
		f.webdavMu.Unlock()
		if !confirmed {
			return appsync.Result{Trigger: trigger}, appsync.ErrInsecureEndpoint
		}
	}
	return appsync.Result{Trigger: trigger, Action: domainsync.ActionNone}, nil
}

func (f *fakeWebDAVSyncService) settings() []appsync.WebDAVSettings {
	f.webdavMu.Lock()
	defer f.webdavMu.Unlock()
	return append([]appsync.WebDAVSettings(nil), f.connectSettings...)
}

func (f *fakeWebDAVSyncService) confirms() []bool {
	f.webdavMu.Lock()
	defer f.webdavMu.Unlock()
	return append([]bool(nil), f.connectConfirms...)
}

func TestSyncStateWebDAVInsecureConnectNeedsConfirmation(t *testing.T) {
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderWebDAV, Status: appsync.StatusNeedsSetup}
	state := NewSyncState(service)
	state.SetPassword("master")
	state.SetWebDAVEndpoint("http://dav.test/dav")
	state.SetWebDAVAuthType("basic")
	state.SetWebDAVUsername("alice")
	state.SetWebDAVPassword("webdav-secret")

	state.Connect()
	waitSyncIdle(t, state)
	if !state.InsecurePrompt() || state.InsecureSyncPrompt() {
		t.Fatalf("prompt = %v syncPrompt = %v", state.InsecurePrompt(), state.InsecureSyncPrompt())
	}
	if calls := len(service.settings()); calls != 1 || service.confirms()[0] {
		t.Fatalf("first connect = %v calls, confirms %v", calls, service.confirms())
	}
	if service.Snapshot().Connected {
		t.Fatal("the fake reported a connection before confirmation")
	}

	state.ConfirmInsecure()
	waitSyncIdle(t, state)
	if state.InsecurePrompt() {
		t.Fatal("the prompt stayed open after confirmation")
	}
	if calls := len(service.settings()); calls != 2 {
		t.Fatalf("connect calls = %d, want 2", calls)
	}
	confirms := service.confirms()
	if len(confirms) != 2 || confirms[0] || !confirms[1] {
		t.Fatalf("confirm flags = %v, want [false true]", confirms)
	}
	if service.Snapshot().WebDAVEndpoint != "http://dav.test/dav" {
		t.Fatalf("snapshot endpoint = %q", service.Snapshot().WebDAVEndpoint)
	}
	if state.WebDAVPassword() != "" || state.Password() != "" {
		t.Fatalf("secrets were not cleared after connect: webdav=%q master=%q", state.WebDAVPassword(), state.Password())
	}
}

func TestSyncStateWebDAVRestoredInsecureSyncNeedsConfirmation(t *testing.T) {
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderWebDAV, HasPassword: true,
		WebDAVEndpoint: "http://dav.test/dav", WebDAVAuthType: domainsync.WebDAVAuthBasic,
		WebDAVInsecure: true,
	}
	state := NewSyncState(service)
	state.SyncNow()
	waitSyncIdle(t, state)
	if !state.InsecurePrompt() || !state.InsecureSyncPrompt() {
		t.Fatalf("prompt = %v syncPrompt = %v", state.InsecurePrompt(), state.InsecureSyncPrompt())
	}
	if service.confirmCalls != 0 {
		t.Fatal("the sync warning recorded a confirmation before the user acted")
	}

	state.ConfirmInsecure()
	waitSyncIdle(t, state)
	if state.InsecurePrompt() {
		t.Fatal("the sync prompt stayed open")
	}
	if service.confirmCalls != 1 {
		t.Fatalf("confirm calls = %d", service.confirmCalls)
	}
	if service.syncCalls != 2 {
		t.Fatalf("sync calls = %d, want the refused try plus the confirmed retry", service.syncCalls)
	}
}

func TestSyncStateWebDAVReconfigurePrefillsAndCancels(t *testing.T) {
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderWebDAV, HasPassword: true,
		WebDAVEndpoint: "https://dav.test/dav", WebDAVAuthType: domainsync.WebDAVAuthBasic,
		WebDAVUsername: "alice",
	}
	service.webdavSettings = appsync.WebDAVSettings{
		Endpoint: "https://dav.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "alice",
	}
	state := NewSyncState(service)
	state.Reconfigure()
	if !state.Reconfiguring() {
		t.Fatal("Reconfigure did not open the form")
	}
	if state.WebDAVEndpoint() != "https://dav.test/dav" || state.WebDAVUsername() != "alice" {
		t.Fatalf("form = %q %q", state.WebDAVEndpoint(), state.WebDAVUsername())
	}
	state.CancelReconfigure()
	if state.Reconfiguring() {
		t.Fatal("CancelReconfigure did not close the form")
	}
}

func TestSyncStateWebDAVRejectsAnIncompleteForm(t *testing.T) {
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderWebDAV}
	state := NewSyncState(service)
	state.SetPassword("master")
	state.SetWebDAVEndpoint("https://dav.test/dav")
	state.SetWebDAVAuthType("basic")
	state.SetWebDAVUsername("alice")
	state.Connect()
	waitSyncIdle(t, state)
	if !errors.Is(state.Err(), appsync.ErrInvalidEndpoint) {
		t.Fatalf("error = %v, want ErrInvalidEndpoint", state.Err())
	}
	if calls := len(service.settings()); calls != 0 {
		t.Fatalf("an incomplete form reached ConnectWebDAV %d times", calls)
	}
}

func TestSyncSectionShowsTheWebDAVFormAndInsecureWarning(t *testing.T) {
	m := locales.In("en")
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderWebDAV, Status: appsync.StatusNeedsSetup}
	service.webdavSettings = appsync.WebDAVSettings{
		Endpoint: "http://dav.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "alice",
	}
	state := NewSyncState(service)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 720, 480)
	tester.Frame()

	for _, want := range []string{
		m.SettingsSyncProviderWebDAV(),
		m.SettingsSyncWebDAVHint(),
		m.SettingsSyncWebDAVEndpoint(),
		m.SettingsSyncWebDAVAuth(),
		m.SettingsSyncWebDAVUsername(),
		m.SettingsSyncWebDAVPassword(),
		m.SettingsSyncWebDAVInsecureWarning(),
		m.SettingsSyncMasterPassword(),
		m.SettingsSyncConnect(),
	} {
		if !tester.HasText(want) {
			t.Errorf("%q is not rendered: %q", want, tester.Texts())
		}
	}
}

func TestSyncSectionShowsTheInsecureSyncPromptInsteadOfSyncNow(t *testing.T) {
	m := locales.In("en")
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderWebDAV, HasPassword: true,
		WebDAVEndpoint: "http://dav.test/dav", WebDAVAuthType: domainsync.WebDAVAuthBasic,
		WebDAVInsecure: true, Status: appsync.StatusFailed, Problem: appsync.ProblemInsecureEndpoint,
	}
	state := NewSyncState(service)
	state.SyncNow()
	waitSyncIdle(t, state)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 720, 480)
	tester.Frame()
	if !tester.HasText(m.SettingsSyncWebDAVInsecureSyncConfirm()) {
		t.Fatalf("the sync confirmation is missing: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsSyncWebDAVInsecureWarning()) {
		t.Fatalf("the warning is missing: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsSyncWebDAVEndpointValue("http://dav.test/dav")) {
		t.Fatalf("the endpoint line is missing: %q", tester.Texts())
	}
	if tester.HasText(m.SettingsSyncSyncNow()) {
		t.Fatalf("Sync now is still actionable behind the warning: %q", tester.Texts())
	}
}

func TestSyncSectionShowsReconfigureForARejectedCredential(t *testing.T) {
	m := locales.In("en")
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderWebDAV, HasPassword: true,
		WebDAVEndpoint: "https://dav.test/dav", WebDAVAuthType: domainsync.WebDAVAuthDigest,
		Status: appsync.StatusFailed, Problem: appsync.ProblemCredentials,
	}
	state := NewSyncState(service)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 720)
	tester.Frame()
	for _, want := range []string{
		m.SettingsSyncProblemCredentials(),
		m.SettingsSyncWebDAVReconfigure(),
		m.SettingsSyncWebDAVAuthDigest(),
	} {
		if !tester.HasText(want) {
			t.Errorf("%q is not rendered: %q", want, tester.Texts())
		}
	}
}

func TestSyncSectionDoesNotWarnForHTTPS(t *testing.T) {
	m := locales.In("en")
	service := newFakeWebDAVSyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderWebDAV, Status: appsync.StatusNeedsSetup}
	service.webdavSettings = appsync.WebDAVSettings{
		Endpoint: "https://dav.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "alice",
	}
	state := NewSyncState(service)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 720)
	tester.Frame()
	if tester.HasText(m.SettingsSyncWebDAVInsecureWarning()) {
		t.Fatalf("HTTPS form showed the plain-HTTP warning: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsSyncWebDAVHint()) {
		t.Fatalf("the WebDAV hint is missing: %q", tester.Texts())
	}
}
