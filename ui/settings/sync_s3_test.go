package settings

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// fakeS3SyncService adds the S3 provider capability to the existing fake. It
// never touches the network and never stores the access key in a snapshot.
type fakeS3SyncService struct {
	*fakeSyncService

	s3Mu            sync.Mutex
	s3Settings      appsync.S3Settings
	connectSettings []appsync.S3Settings
	connectConfirms []bool
	connectErr      error
	confirmCalls    int
	confirmed       bool
}

func newFakeS3SyncService() *fakeS3SyncService {
	return &fakeS3SyncService{fakeSyncService: newFakeSyncService()}
}

func (f *fakeS3SyncService) SetProvider(_ context.Context, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot.Provider = appsync.NormalizeProvider(provider)
	f.snapshot.Connected = false
	return nil
}

func (f *fakeS3SyncService) ConnectS3(_ context.Context, _ string, settings appsync.S3Settings, confirmInsecure bool) error {
	f.s3Mu.Lock()
	f.connectSettings = append(f.connectSettings, settings)
	f.connectConfirms = append(f.connectConfirms, confirmInsecure)
	connectErr := f.connectErr
	f.s3Mu.Unlock()
	if connectErr != nil {
		return connectErr
	}
	if strings.HasPrefix(settings.Endpoint, "http://") && !confirmInsecure {
		return appsync.ErrInsecureEndpoint
	}
	f.mu.Lock()
	f.snapshot.Connected = true
	f.snapshot.Provider = appsync.ProviderS3
	f.snapshot.Login = settings.Bucket + "@s3.test"
	f.snapshot.HasPassword = true
	f.snapshot.S3Endpoint = settings.Endpoint
	f.snapshot.S3Region = settings.Region
	f.snapshot.S3Bucket = settings.Bucket
	f.snapshot.S3Prefix = settings.Prefix
	f.snapshot.S3Insecure = strings.HasPrefix(settings.Endpoint, "http://")
	f.mu.Unlock()
	f.s3Mu.Lock()
	f.s3Settings = appsync.S3Settings{
		Endpoint: settings.Endpoint, Region: settings.Region,
		Bucket: settings.Bucket, Prefix: settings.Prefix,
	}
	f.s3Mu.Unlock()
	return nil
}

func (f *fakeS3SyncService) S3Settings() appsync.S3Settings {
	f.s3Mu.Lock()
	defer f.s3Mu.Unlock()
	return f.s3Settings
}

func (f *fakeS3SyncService) ConfirmInsecureEndpoint(context.Context) bool {
	f.s3Mu.Lock()
	defer f.s3Mu.Unlock()
	f.confirmCalls++
	f.confirmed = true
	return true
}

func (f *fakeS3SyncService) Sync(_ context.Context, trigger appsync.Trigger) (appsync.Result, error) {
	f.mu.Lock()
	f.syncCalls++
	insecure := f.snapshot.S3Insecure
	syncErr := f.syncErr
	f.mu.Unlock()
	if syncErr != nil {
		return appsync.Result{Trigger: trigger}, syncErr
	}
	if trigger == appsync.TriggerManual && insecure {
		f.s3Mu.Lock()
		confirmed := f.confirmed
		f.s3Mu.Unlock()
		if !confirmed {
			return appsync.Result{Trigger: trigger}, appsync.ErrInsecureEndpoint
		}
	}
	return appsync.Result{Trigger: trigger, Action: domainsync.ActionNone}, nil
}

func (f *fakeS3SyncService) settings() []appsync.S3Settings {
	f.s3Mu.Lock()
	defer f.s3Mu.Unlock()
	return append([]appsync.S3Settings(nil), f.connectSettings...)
}

func (f *fakeS3SyncService) confirms() []bool {
	f.s3Mu.Lock()
	defer f.s3Mu.Unlock()
	return append([]bool(nil), f.connectConfirms...)
}

func newS3FormState(service *fakeS3SyncService) *SyncState {
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderS3, Status: appsync.StatusNeedsSetup}
	state := NewSyncState(service)
	state.SetPassword("master")
	state.SetS3Endpoint("http://s3.test:9000")
	state.SetS3Region("us-east-1")
	state.SetS3Bucket("natcatty-test")
	state.SetS3Prefix("team/vault")
	state.SetS3AccessKeyID("fake-access-key")
	state.SetS3SecretAccessKey("fake-secret")
	state.SetS3SessionToken("fake-session-token")
	return state
}

func TestSyncStateS3InsecureConnectNeedsConfirmation(t *testing.T) {
	service := newFakeS3SyncService()
	state := newS3FormState(service)

	state.Connect()
	waitSyncIdle(t, state)
	if !state.InsecurePrompt() || state.InsecureSyncPrompt() {
		t.Fatalf("prompt = %v syncPrompt = %v", state.InsecurePrompt(), state.InsecureSyncPrompt())
	}
	if calls := len(service.settings()); calls != 1 || service.confirms()[0] {
		t.Fatalf("first connect = %d calls, confirms %v", calls, service.confirms())
	}
	if service.Snapshot().Connected {
		t.Fatal("the fake connected before confirmation")
	}
	if state.S3SecretAccessKey() == "" || state.S3AccessKeyID() == "" {
		t.Fatal("the form lost the signing material before confirmation")
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
	connected := service.Snapshot()
	if !connected.Connected || connected.Provider != appsync.ProviderS3 ||
		connected.S3Endpoint != "http://s3.test:9000" || connected.S3Region != "us-east-1" ||
		connected.S3Bucket != "natcatty-test" || connected.S3Prefix != "team/vault" ||
		!connected.S3Insecure {
		t.Fatalf("connected snapshot = %+v", connected)
	}
	if state.S3AccessKeyID() != "" || state.S3SecretAccessKey() != "" ||
		state.S3SessionToken() != "" || state.Password() != "" {
		t.Fatal("the signing material or master password survived the connect")
	}
}

func TestSyncStateS3RejectsAnIncompleteForm(t *testing.T) {
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderS3}
	state := NewSyncState(service)
	state.SetPassword("master")
	state.SetS3Endpoint("https://s3.test")
	state.SetS3Region("us-east-1")
	state.SetS3Bucket("bucket")
	state.Connect()
	waitSyncIdle(t, state)
	if !errors.Is(state.Err(), appsync.ErrInvalidEndpoint) {
		t.Fatalf("error = %v, want ErrInvalidEndpoint", state.Err())
	}
	if calls := len(service.settings()); calls != 0 {
		t.Fatalf("an incomplete form reached ConnectS3 %d times", calls)
	}
}

func TestSyncStateS3ReconfigurePrefillsOnlyNonSecrets(t *testing.T) {
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderS3, HasPassword: true,
		S3Endpoint: "https://s3.test", S3Region: "eu-west-1", S3Bucket: "vault", S3Prefix: "team",
	}
	service.s3Settings = appsync.S3Settings{
		Endpoint: "https://s3.test", Region: "eu-west-1", Bucket: "vault", Prefix: "team",
	}
	state := NewSyncState(service)
	state.Reconfigure()
	if !state.Reconfiguring() {
		t.Fatal("Reconfigure did not open the form")
	}
	if state.S3Endpoint() != "https://s3.test" || state.S3Region() != "eu-west-1" ||
		state.S3Bucket() != "vault" || state.S3Prefix() != "team" {
		t.Fatalf("non-secret fields were not prefilled: %+v", service.s3Settings)
	}
	if state.S3AccessKeyID() != "" || state.S3SecretAccessKey() != "" || state.S3SessionToken() != "" {
		t.Fatal("signing material was prefilled from somewhere")
	}
	state.CancelReconfigure()
	if state.Reconfiguring() {
		t.Fatal("CancelReconfigure did not close the form")
	}
}

func TestSyncStateS3RestoredInsecureSyncNeedsConfirmation(t *testing.T) {
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderS3, HasPassword: true,
		S3Endpoint: "http://s3.test", S3Region: "us-east-1", S3Bucket: "vault", S3Insecure: true,
		Status: appsync.StatusFailed, Problem: appsync.ProblemInsecureEndpoint,
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

func TestSyncSectionShowsTheS3FormAndInsecureWarning(t *testing.T) {
	m := locales.In("en")
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderS3, Status: appsync.StatusNeedsSetup}
	service.s3Settings = appsync.S3Settings{
		Endpoint: "http://s3.test:9000", Region: "us-east-1", Bucket: "natcatty-test",
	}
	state := NewSyncState(service)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 720, 480)
	tester.Frame()

	for _, want := range []string{
		m.SettingsSyncProviderS3(),
		m.SettingsSyncS3Hint(),
		m.SettingsSyncS3Endpoint(),
		m.SettingsSyncS3Region(),
		m.SettingsSyncS3Bucket(),
		m.SettingsSyncS3Prefix(),
		m.SettingsSyncS3AccessKeyID(),
		m.SettingsSyncS3SecretAccessKey(),
		m.SettingsSyncS3SessionToken(),
		m.SettingsSyncS3InsecureWarning(),
		m.SettingsSyncMasterPassword(),
		m.SettingsSyncConnect(),
	} {
		if !tester.HasText(want) {
			t.Errorf("%q is not rendered: %q", want, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsSyncWebDAVHint()) {
		t.Errorf("the S3 form rendered the WebDAV hint: %q", tester.Texts())
	}
}

func TestSyncSectionS3HTTPSDoesNotWarn(t *testing.T) {
	m := locales.In("en")
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{Provider: appsync.ProviderS3, Status: appsync.StatusNeedsSetup}
	service.s3Settings = appsync.S3Settings{
		Endpoint: "https://s3.test", Region: "us-east-1", Bucket: "natcatty-test",
	}
	state := NewSyncState(service)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 720)
	tester.Frame()
	if tester.HasText(m.SettingsSyncS3InsecureWarning()) {
		t.Fatalf("the HTTPS form showed the plain-HTTP warning: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsSyncS3Hint()) {
		t.Fatalf("the S3 hint is missing: %q", tester.Texts())
	}
}

func TestSyncSectionShowsS3ProblemsAndReconfigure(t *testing.T) {
	m := locales.In("en")
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderS3, HasPassword: true,
		S3Endpoint: "https://s3.test", S3Region: "us-east-1", S3Bucket: "vault",
		S3Prefix: "team", Status: appsync.StatusFailed, Problem: appsync.ProblemWrongRegion,
	}
	state := NewSyncState(service)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 720)
	tester.Frame()
	for _, want := range []string{
		m.SettingsSyncS3ProblemWrongRegion(),
		m.SettingsSyncS3Reconfigure(),
		m.SettingsSyncS3EndpointValue("https://s3.test"),
		m.SettingsSyncS3PrefixValue("team"),
	} {
		if !tester.HasText(want) {
			t.Errorf("%q is not rendered: %q", want, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsSyncProblemCredentials()) {
		t.Errorf("the S3 view used the WebDAV credentials wording: %q", tester.Texts())
	}
}

func TestSyncSectionS3InsecurePromptUsesS3Wording(t *testing.T) {
	m := locales.In("en")
	service := newFakeS3SyncService()
	service.snapshot = appsync.Snapshot{
		Connected: true, Provider: appsync.ProviderS3, HasPassword: true,
		S3Endpoint: "http://s3.test", S3Region: "us-east-1", S3Bucket: "vault",
		S3Insecure: true, Status: appsync.StatusFailed, Problem: appsync.ProblemInsecureEndpoint,
	}
	state := NewSyncState(service)
	state.SyncNow()
	waitSyncIdle(t, state)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 720, 480)
	tester.Frame()
	if !tester.HasText(m.SettingsSyncWebDAVInsecureSyncConfirm()) {
		t.Fatalf("the sync confirmation is missing: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsSyncS3InsecureWarning()) {
		t.Fatalf("the S3 warning is missing: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsSyncS3EndpointValue("http://s3.test")) {
		t.Fatalf("the endpoint line is missing: %q", tester.Texts())
	}
	if tester.HasText(m.SettingsSyncSyncNow()) {
		t.Fatalf("Sync now is still actionable behind the warning: %q", tester.Texts())
	}
}

func TestSyncWarningTextColorKeepsLightModeReadable(t *testing.T) {
	// The theme warning token is the border/icon amber. As 11-point body text
	// on the light surface it measures about 1.9:1, so light mode must use the
	// darker amber that reaches the 4.5:1 contrast target.
	light := &ui.Theme{Warning: ui.Hex("#f59e0b")}
	if got := syncWarningTextColor(light); got != ui.Hex("#9a5c00") {
		t.Fatalf("light warning text = %+v, want the darker amber", got)
	}
	dark := &ui.Theme{Dark: true, Warning: ui.Hex("#fbbf24")}
	if got := syncWarningTextColor(dark); got != dark.Warning {
		t.Fatalf("dark warning text = %+v, want the theme warning", got)
	}
}

func TestSyncDangerTextColorStaysReadableInBothAppearances(t *testing.T) {
	// The theme destructive token is tuned for borders and filled buttons. As
	// 12-point body text on the light sync panel's Snow fill it measures about
	// 4.47:1, below the 4.5:1 target, so light mode uses a darker red. Dark
	// mode picks the first ladder red that clears the target on the selected
	// preset's fill, because no single red clears every dark preset.
	snow, ok := domaintheme.LookupPreset(false, "snow")
	if !ok {
		t.Fatal("the Snow preset is missing")
	}
	light := uitheme.UI(false, snow, uitheme.Options{})
	want := ui.Hex("#b51616")
	if got := syncDangerTextColor(light); got != want {
		t.Fatalf("light danger text = %+v, want the darker red", got)
	}

	// The light branch must keep its measured 6.158:1 on the Snow fill.
	textLuminance, ok := domaintheme.HexLuminance("#b51616")
	if !ok {
		t.Fatal("the pinned danger color is not a hex color")
	}
	snowLuminance, ok := domaintheme.HexLuminance("#f1f4f8")
	if !ok {
		t.Fatal("the Snow fill is not a hex color")
	}
	if ratio := domaintheme.ContrastRatio(textLuminance, snowLuminance); ratio < 6.15 || ratio > 6.17 {
		t.Fatalf("the light danger text measures %.3f:1 on Snow, want the pinned 6.158:1", ratio)
	}
	for _, background := range []string{"#ffffff"} {
		backgroundLuminance, ok := domaintheme.HexLuminance(background)
		if !ok {
			t.Fatalf("%s is not a hex color", background)
		}
		if ratio := domaintheme.ContrastRatio(textLuminance, backgroundLuminance); ratio < 4.5 {
			t.Errorf("danger text on %s = %.2f:1, want at least 4.5:1", background, ratio)
		}
	}

	// Pin the dark branch on the default preset: the first ladder candidate,
	// not the raw theme token. Midnight's raw danger is about 3.6:1 there.
	midnight, ok := domaintheme.LookupPreset(true, "midnight")
	if !ok {
		t.Fatal("the Midnight preset is missing")
	}
	dark := uitheme.UI(true, midnight, uitheme.Options{})
	if got := syncDangerTextColor(dark); got != ui.Hex("#ff6b6b") {
		t.Fatalf("dark danger text on Midnight = %+v, want the first ladder red #ff6b6b", got)
	}
	if ratio := syncColorContrast(dark.Danger, dark.Background); ratio >= 4.5 {
		t.Fatalf("the raw dark theme danger measures %.2f:1 on Midnight; the fix would be unnecessary", ratio)
	}

	// Every prompt fill, on both appearances, must clear the target. Compute
	// the luminance through the independent domain WCAG helper from the hex
	// value the helper actually returned.
	for _, test := range []struct {
		name    string
		dark    bool
		presets []domaintheme.Preset
	}{
		{"light", false, domaintheme.LightPresets()},
		{"dark", true, domaintheme.DarkPresets()},
	} {
		for _, preset := range test.presets {
			theme := uitheme.UI(test.dark, preset, uitheme.Options{})
			text := syncDangerTextColor(theme)
			textLum, ok := domaintheme.HexLuminance(dangerHex(text))
			if !ok {
				t.Fatalf("%s preset %s: %+v is not a hex color", test.name, preset.ID, text)
			}
			backgroundLum, ok := domaintheme.HexLuminance(dangerHex(theme.Background))
			if !ok {
				t.Fatalf("%s preset %s: background %+v is not a hex color", test.name, preset.ID, theme.Background)
			}
			if ratio := domaintheme.ContrastRatio(textLum, backgroundLum); ratio < 4.5 {
				t.Errorf("%s preset %s: danger text %+v = %.2f:1 on its fill, want at least 4.5:1",
					test.name, preset.ID, text, ratio)
			}
		}
	}

	// The premise: the raw light theme token really does fall short on the
	// Snow fill, so this test would catch a regression to it.
	rawLuminance, _ := domaintheme.HexLuminance("#d92626")
	if ratio := domaintheme.ContrastRatio(rawLuminance, snowLuminance); ratio >= 4.5 {
		t.Fatalf("the raw theme danger measures %.2f:1 on Snow; the fix would be unnecessary", ratio)
	}
}

// dangerHex renders an opaque ui.Color as the "#rrggbb" form the domain WCAG
// helper parses.
func dangerHex(c ui.Color) string {
	return syncColorHex(c)
}

// TestSyncColorContrastMatchesWCAG pins the helper's math to published WCAG
// values, so the dark ladder is judged against the real definition.
func TestSyncColorContrastMatchesWCAG(t *testing.T) {
	if ratio := syncColorContrast(ui.Hex("#000000"), ui.Hex("#ffffff")); ratio < 20.99 || ratio > 21.01 {
		t.Fatalf("black on white = %.3f:1, want 21:1", ratio)
	}
	if ratio := syncColorContrast(ui.Hex("#b51616"), ui.Hex("#f1f4f8")); ratio < 6.15 || ratio > 6.17 {
		t.Fatalf("the light danger on Snow = %.3f:1, want 6.158:1", ratio)
	}
}

// TestSyncForcePushPromptUsesTheAccessibleDangerTextColor renders the
// guard's explicit force-push confirmation in both themes and reads the
// pixels: the prompt uses syncDangerTextColor and never the raw theme token.
func TestSyncForcePushPromptUsesTheAccessibleDangerTextColor(t *testing.T) {
	m := locales.In("en")
	for _, test := range []struct {
		name   string
		dark   bool
		preset string
	}{
		{"light", false, "snow"},
		{"dark", true, "midnight"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preset, ok := domaintheme.LookupPreset(test.dark, test.preset)
			if !ok {
				t.Fatalf("%s preset is missing", test.preset)
			}
			theme := uitheme.UI(test.dark, preset, uitheme.Options{})
			want := syncDangerTextColor(theme)
			raw := theme.Danger
			if want == raw {
				t.Fatalf("the helper returned the raw theme danger %v; the pixel check would be vacuous", raw)
			}

			service := newFakeDestructiveSyncService()
			service.snapshot.Conflict = nil
			state := NewSyncState(service)
			state.AskForcePush()
			view := viewAt(AreaSync, Config{Sync: state})
			tester := ui.NewTester(func(c *ui.Context) {
				c.SetTheme(theme)
				view(c)
			}, 1100, 720)
			tester.SetLocale("en")
			tester.Frame()
			waitSyncIdle(t, state)
			tester.Frame()

			confirm := m.SettingsSyncGuardForceConfirm()
			rect, ok := tester.Find(confirm)
			if !ok {
				t.Fatalf("the force-push confirmation %q is not rendered: %q", confirm, tester.Texts())
			}
			for step := 0; step < 240 && rect.H < 12; step++ {
				tester.Scroll(700, 400, 0, 40)
				rect, _ = tester.Find(confirm)
			}
			if rect.H < 12 {
				t.Fatalf("the force-push confirmation was never fully laid out: %+v", rect)
			}
			if !testerRectHasColor(tester, rect, want) {
				t.Errorf("the force-push prompt does not use the accessible danger text color %v", want)
			}
			if testerRectHasColor(tester, rect, raw) {
				t.Errorf("the force-push prompt still uses the raw theme danger text color %v", raw)
			}
		})
	}
}
