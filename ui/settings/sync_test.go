package settings

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
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

// testerRectInk names the ink the glyphs inside rect are painted with. At a
// small size a platform's rasterizer may never paint a glyph core at the
// exact token color — the Ubuntu CJK fallback at 11 DIP does not — so an
// exact pixel cannot identify the color there. Instead the same line is
// rendered in each candidate ink by the same tester pipeline; its darkest
// pixel has the same glyph coverage, so the nearest reference names the ink.
// bold must match the sampled node's weight, or the reference's coverage
// differs from the painted glyphs. A rect that is blank, clipped, or painted
// in a color matching no candidate (including the plain body text color)
// fails rather than guessing: a fill is far from every glyph reference, so
// the tolerance rejects it.
//
// A platform whose installed fonts cover none of the text's script paints no
// glyph at all (MyGo skips the boxes Pango draws for unknown glyphs), so both
// reference renders stay blank and no color could be told apart. That is not
// a color defect to guess at: it is an environment without a font for a
// shipped locale, and it fails with that reason so CI must install one.
func testerRectInk(t *testing.T, tester *ui.Tester, rect ui.Rect, text string, size float32, bold bool, inks ...ui.Color) ui.Color {
	t.Helper()
	ink, err := classifyRectInk(tester, rect, text, size, bold, inks...)
	if err != nil {
		t.Fatal(err)
	}
	return ink
}

// classifyRectInk is testerRectInk without a *testing.T, so the rejection
// paths are unit tested against a real frame.
func classifyRectInk(tester *ui.Tester, rect ui.Rect, text string, size float32, bold bool, inks ...ui.Color) (ui.Color, error) {
	if len(inks) < 2 {
		return ui.Color{}, fmt.Errorf("classifyRectInk needs at least two candidate inks")
	}
	img := tester.Image()
	// The fill behind the glyphs is the rect's dominant color, which is what
	// the references must blend into: a row on the panel's surface must not be
	// compared with a row on the page's background.
	fill, ok := dominantRectPixel(img, rect)
	if !ok {
		return ui.Color{}, fmt.Errorf("no pixels were painted inside %+v for %q", rect, text)
	}
	references := make([]ui.Color, len(inks))
	rasterized := false
	for i, ink := range inks {
		references[i] = renderedTextInk(text, size, bold, ink, fill)
		if i > 0 && !sameRGB(references[i], references[0]) {
			rasterized = true
		}
	}
	if !rasterized {
		return ui.Color{}, fmt.Errorf("the platform paints no glyphs for %q: no installed font covers its script, so neither candidate ink rasterizes differently; install one (on Linux, for example, fonts-noto-cjk)", text)
	}
	painted, ok := darkestRectPixel(img, rect)
	if !ok {
		return ui.Color{}, fmt.Errorf("no pixels were painted inside %+v for %q", rect, text)
	}
	best, second := -1, -1
	var bestDistance, secondDistance float64
	for i, reference := range references {
		distance := colorDistance(painted, reference)
		switch {
		case best < 0 || distance < bestDistance:
			second, secondDistance = best, bestDistance
			best, bestDistance = i, distance
		case second < 0 || distance < secondDistance:
			second, secondDistance = i, distance
		}
	}
	const matchTolerance, separation = 24, 8
	if bestDistance > matchTolerance || secondDistance-bestDistance < separation {
		return ui.Color{}, fmt.Errorf("the glyphs of %q in %+v are %v; nearest candidate is %v at %.1f but the next is only %.1f away",
			text, rect, painted, inks[best], bestDistance, secondDistance)
	}
	return inks[best], nil
}

// renderedTextInk renders one line in ink on fill and returns its darkest
// painted pixel: the coverage the same line gets at its most saturated point
// on this platform.
func renderedTextInk(text string, size float32, bold bool, ink, fill ui.Color) ui.Color {
	var rect ui.Rect
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Background(fill).Children(func() {
			line := ui.Text(c, text).FontSize(size).TextColor(ink)
			if bold {
				line.Bold()
			}
		})
	}, 1200, 200)
	tester.Frame()
	rect, ok := tester.Find(text)
	if !ok {
		return ui.Color{}
	}
	darkest, ok := darkestRectPixel(tester.Image(), rect)
	if !ok {
		return ui.Color{}
	}
	return darkest
}

// sameRGB compares the sRGB value of two colors, ignoring their wide-gamut
// forms: a pixel read back from the frame carries none.
func sameRGB(a, b ui.Color) bool {
	return a.R == b.R && a.G == b.G && a.B == b.B
}

// darkestRectPixel returns the darkest pixel inside rect, which is the one a
// glyph's most covered stroke leaves on the light theme fill.
func darkestRectPixel(img *image.RGBA, rect ui.Rect) (ui.Color, bool) {
	left, top := max(int(rect.X), img.Rect.Min.X), max(int(rect.Y), img.Rect.Min.Y)
	right, bottom := min(int(rect.X+rect.W), img.Rect.Max.X), min(int(rect.Y+rect.H), img.Rect.Max.Y)
	best, found := 1<<30, false
	var darkest ui.Color
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			offset := img.PixOffset(x, y)
			sum := int(img.Pix[offset]) + int(img.Pix[offset+1]) + int(img.Pix[offset+2])
			if sum < best {
				best, found = sum, true
				darkest = ui.RGB(img.Pix[offset], img.Pix[offset+1], img.Pix[offset+2])
			}
		}
	}
	return darkest, found
}

// dominantRectPixel returns the color most pixels of rect share: the fill the
// glyphs are drawn on, so a reference render blends into the same one.
func dominantRectPixel(img *image.RGBA, rect ui.Rect) (ui.Color, bool) {
	left, top := max(int(rect.X), img.Rect.Min.X), max(int(rect.Y), img.Rect.Min.Y)
	right, bottom := min(int(rect.X+rect.W), img.Rect.Max.X), min(int(rect.Y+rect.H), img.Rect.Max.Y)
	counts := map[ui.Color]int{}
	var most ui.Color
	best := 0
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			offset := img.PixOffset(x, y)
			c := ui.RGB(img.Pix[offset], img.Pix[offset+1], img.Pix[offset+2])
			counts[c]++
			if counts[c] > best {
				most, best = c, counts[c]
			}
		}
	}
	return most, best > 0
}

func colorDistance(a, b ui.Color) float64 {
	dr := float64(int(a.R) - int(b.R))
	dg := float64(int(a.G) - int(b.G))
	db := float64(int(a.B) - int(b.B))
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// TestRectInkClassifierNamesThePaintedInkOrFails pins the three outcomes the
// refusal tests rely on, against a real frame: the wanted ink is named, the
// rejected ink is not, and a rect whose row is not painted is rejected rather
// than silently accepted because two blank references look alike.
func TestRectInkClassifierNamesThePaintedInkOrFails(t *testing.T) {
	want := syncDangerTextColor(ui.LightTheme())
	raw := ui.LightTheme().Danger
	if want == raw {
		t.Fatalf("the danger helper returned the raw token %v; the classifier test would be vacuous", raw)
	}
	const label = "The cloud copy uses a newer schema than this version supports."
	render := func(ink ui.Color) (*ui.Tester, ui.Rect) {
		var rect ui.Rect
		tester := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Padding(10).Children(func() {
				ui.Text(c, label).FontSize(11).TextColor(ink)
			})
		}, 600, 120)
		tester.Frame()
		found, ok := tester.Find(label)
		if !ok {
			t.Fatalf("the %v line is not laid out", ink)
		}
		rect = found
		return tester, rect
	}

	tester, rect := render(want)
	if ink, err := classifyRectInk(tester, rect, label, 11, false, want, raw); err != nil {
		t.Fatalf("the wanted ink was not classified: %v", err)
	} else if ink != want {
		t.Fatalf("classified %v, want syncDangerTextColor(%v)", ink, want)
	}

	tester, rect = render(raw)
	if ink, err := classifyRectInk(tester, rect, label, 11, false, want, raw); err != nil {
		t.Fatalf("the rejected ink was not classified: %v", err)
	} else if ink != raw {
		t.Fatalf("classified %v, want the raw theme danger %v", ink, raw)
	}

	// A rect below the painted row is still a real rect with pixels, but no
	// glyph ink; it must be reported, not matched to whichever reference is
	// nearest.
	tester, rect = render(want)
	blank := ui.Rect{X: rect.X, Y: rect.Y + rect.H + 40, W: rect.W, H: rect.H}
	if _, err := classifyRectInk(tester, blank, label, 11, false, want, raw); err == nil {
		t.Fatalf("a blank row %+v was classified as an ink", blank)
	}
}
