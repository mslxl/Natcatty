package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

// fakeConvergentSyncService is the application seam for the v2 panel. It
// records each user action and updates only its in-memory snapshot; it never
// touches a Vault or provider.
type fakeConvergentSyncService struct {
	*fakeSyncService

	convergentCalls     []bool
	restores            int
	convergentErr       error
	conflictResolutions []appsync.ConvergentConflictResolution
	bulkResolutions     []appsync.ConvergentConflictSide
}

func newFakeConvergentSyncService() *fakeConvergentSyncService {
	fake := &fakeConvergentSyncService{fakeSyncService: newFakeSyncService()}
	fake.snapshot.HasPassword = true
	fake.snapshot.Convergent = appsync.ConvergentInfo{
		SchemaVersion: 2,
		Enabled:       true,
		Phase:         "converged",
		TotalRounds:   appsync.ConvergentVerifyRounds,
		Writes:        0,
		EntityCount:   5,
		ConflictCount: 0,
		Rounds: []appsync.ConvergentRoundInfo{
			{Round: 1, Kind: appsync.ConvergentRoundManifest, Outcome: "verified"},
			{Round: 2, Kind: appsync.ConvergentRoundEntities, Outcome: "verified"},
			{Round: 3, Kind: appsync.ConvergentRoundState, Outcome: "verified"},
		},
		LastVerifiedAt: 1_700_000_000_000,
	}
	return fake
}

func (f *fakeConvergentSyncService) SetConvergentEnabled(_ context.Context, enabled bool) (appsync.ConvergentMigrationPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convergentCalls = append(f.convergentCalls, enabled)
	if f.convergentErr != nil {
		return appsync.ConvergentMigrationPreview{}, f.convergentErr
	}
	f.snapshot.Convergent.SchemaVersion = 2
	f.snapshot.Convergent.Enabled = enabled
	if enabled {
		f.snapshot.Convergent.Phase = "converged"
	} else {
		f.snapshot.Convergent.Phase = "paused"
	}
	return appsync.ConvergentMigrationPreview{
		SchemaVersion:      2,
		CanInitialize:      true,
		AlreadyInitialized: true,
	}, nil
}

func (f *fakeConvergentSyncService) ResolveConvergentMigrationConflict(_ context.Context, resolution appsync.ConvergentConflictResolution) (appsync.ConvergentMigrationPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conflictResolutions = append(f.conflictResolutions, resolution)
	if f.convergentErr != nil {
		return appsync.ConvergentMigrationPreview{}, f.convergentErr
	}
	return appsync.ConvergentMigrationPreview{SchemaVersion: 2}, nil
}

func (f *fakeConvergentSyncService) ResolveConvergentMigrationConflicts(_ context.Context, side appsync.ConvergentConflictSide) (appsync.ConvergentMigrationPreview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkResolutions = append(f.bulkResolutions, side)
	if f.convergentErr != nil {
		return appsync.ConvergentMigrationPreview{}, f.convergentErr
	}
	return appsync.ConvergentMigrationPreview{SchemaVersion: 2}, nil
}

func (f *fakeConvergentSyncService) RestoreLastVerified(context.Context) (appsync.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restores++
	f.snapshot.Convergent.Phase = "converged"
	f.snapshot.Convergent.Divergence = nil
	f.snapshot.Convergent.Outcome = "restored"
	return appsync.Result{}, nil
}

// scrollSettingsTo wheels the Settings content viewport until text is laid out
// fully inside it. It uses the real viewport bounds bound by viewWithLayout,
// the real wheel path, and a hard step bound; no sleeps and no guessed
// viewport height.
func scrollSettingsTo(t *testing.T, tester *ui.Tester, text string, w, h int, viewport ui.Rect) ui.Rect {
	t.Helper()
	if viewport.W <= 0 || viewport.H <= 0 {
		t.Fatalf("the settings content viewport has no size: %+v", viewport)
	}
	x, y := viewport.X+viewport.W-30, viewport.Y+viewport.H-12
	for step := 0; step < 240; step++ {
		if rect, ok := tester.Find(text); ok &&
			((insideContentViewport(rect, viewport) && rect.H >= 10) ||
				insidePinnedConvergentBar(rect, viewport, w, h)) {
			return rect
		}
		tester.Scroll(x, y, 0, 40)
	}
	rect, ok := tester.Find(text)
	t.Fatalf("%q was not reachable inside %dx%d settings content viewport %+v: rect=%+v found=%v texts=%q",
		text, w, h, viewport, rect, ok, tester.Texts())
	return ui.Rect{}
}

// insideContentViewport reports whether r is wholly laid out inside the real
// scrolling content viewport, with sub-pixel tolerance for fractional layout.
func insideContentViewport(r ui.Rect, viewport ui.Rect) bool {
	return r.W > 0 && r.H > 0 &&
		r.X >= viewport.X-0.5 && r.Y >= viewport.Y-0.5 &&
		r.X+r.W <= viewport.X+viewport.W+0.5 &&
		r.Y+r.H <= viewport.Y+viewport.H+0.5
}

// scrollSettingsUp wheels the content toward its start until text is fully
// inside the viewport. Painted-pixel checks need the node inside the clip: a
// node left above the viewport is not painted at its laid-out coordinates.
func scrollSettingsUp(t *testing.T, tester *ui.Tester, text string, viewport ui.Rect) ui.Rect {
	t.Helper()
	x, y := viewport.X+viewport.W-30, viewport.Y+12
	for step := 0; step < 240; step++ {
		if rect, ok := tester.Find(text); ok && insideContentViewport(rect, viewport) {
			return rect
		}
		tester.Scroll(x, y, 0, -40)
	}
	rect, ok := tester.Find(text)
	t.Fatalf("%q was not reachable above the current scroll position inside %+v: rect=%+v found=%v texts=%q",
		text, viewport, rect, ok, tester.Texts())
	return ui.Rect{}
}

// insidePinnedConvergentBar reports whether r is a control laid out in the
// bottom action bar rather than in the scrolling report. The left edge is the
// real content column's, so a wider localized sidebar does not move the bar's
// controls into a guessed band. The content pane reaches the window's bottom
// edge, so a report row the viewport clips to a sliver there is not the bar's.
func insidePinnedConvergentBar(r ui.Rect, viewport ui.Rect, w, h int) bool {
	return r.W > 0 && r.H >= 10 &&
		r.X >= viewport.X-0.5 && r.X+r.W <= float32(w)+0.5 &&
		r.Y >= float32(h)-syncConvergentBarHeight-8-0.5 && r.Y+r.H <= float32(h)+0.5
}

// TestSyncConvergentEnablePauseResume drives the v2 control at the smallest
// supported window. The panel is below the fold, so each button must be
// reachable by real wheel scrolling before it can be clicked.
func TestSyncConvergentEnablePauseResume(t *testing.T) {
	m := locales.In("en")
	service := newFakeConvergentSyncService()
	service.snapshot.Convergent = appsync.ConvergentInfo{
		Phase:       "disabled",
		TotalRounds: appsync.ConvergentVerifyRounds,
	}
	state := NewSyncState(service)
	var layout viewLayout
	var context *ui.Context
	view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
	tester := ui.NewTester(func(c *ui.Context) {
		context = c
		view(c)
	}, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	viewport := layout.Content.Bounds(context)

	for _, want := range []string{
		m.SettingsSyncConvergentTitle(),
		m.SettingsSyncConvergentState(m.SettingsSyncConvergentPhaseDisabled()),
		m.SettingsSyncConvergentDescription(),
		m.SettingsSyncConvergentEnableHint(),
		m.SettingsSyncConvergentEnable(),
	} {
		scrollSettingsTo(t, tester, want, 720, 480, viewport)
	}

	if err := tester.Click(m.SettingsSyncConvergentEnable()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)
	tester.Frame()
	service.mu.Lock()
	enabled := append([]bool(nil), service.convergentCalls...)
	service.mu.Unlock()
	if len(enabled) != 1 || !enabled[0] {
		t.Fatalf("enable calls = %v", enabled)
	}

	// The same panel now reports the converged state and offers pause.
	for _, want := range []string{
		m.SettingsSyncConvergentState(m.SettingsSyncConvergentPhaseConverged()),
		m.SettingsSyncConvergentPause(),
	} {
		scrollSettingsTo(t, tester, want, 720, 480, viewport)
	}
	if err := tester.Click(m.SettingsSyncConvergentPause()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)
	tester.Frame()
	for _, want := range []string{
		m.SettingsSyncConvergentState(m.SettingsSyncConvergentPhasePaused()),
		m.SettingsSyncConvergentPausedHint(),
		m.SettingsSyncConvergentResume(),
	} {
		scrollSettingsTo(t, tester, want, 720, 480, viewport)
	}
	if err := tester.Click(m.SettingsSyncConvergentResume()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)
	service.mu.Lock()
	calls := append([]bool(nil), service.convergentCalls...)
	service.mu.Unlock()
	if len(calls) != 3 || !calls[0] || calls[1] || !calls[2] {
		t.Fatalf("enable/pause/resume calls = %v", calls)
	}
}

// TestSyncConvergentVerificationRoundsAreSurfaced checks every round, its
// outcome, the write count, and the record/conflict summary in English and
// Simplified Chinese at both supported sizes.
func TestSyncConvergentVerificationRoundsAreSurfaced(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {1084, 661}, {720, 480}, {704, 421}} {
			locale, w, h := locale, size[0], size[1]
			t.Run(locale+"/"+sizeName(w, h), func(t *testing.T) {
				m := locales.In(locale)
				service := newFakeConvergentSyncService()
				service.snapshot.Convergent.Phase = "verifying"
				service.snapshot.Convergent.Round = 3
				service.snapshot.Convergent.RoundKind = appsync.ConvergentRoundState
				service.snapshot.Convergent.Outcome = "running"
				service.snapshot.Convergent.Writes = 1
				service.snapshot.Convergent.EntityCount = 12
				service.snapshot.Convergent.ConflictCount = 1
				state := NewSyncState(service)
				var layout viewLayout
				var context *ui.Context
				view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
				tester := ui.NewTester(func(c *ui.Context) {
					context = c
					view(c)
				}, w, h)
				tester.SetLocale(locale)
				tester.Frame()
				viewport := layout.Content.Bounds(context)

				wants := []string{
					m.SettingsSyncConvergentTitle(),
					m.SettingsSyncConvergentState(m.SettingsSyncConvergentPhaseVerifying()),
					m.SettingsSyncConvergentDescription(),
					m.SettingsSyncConvergentRound("1", "3", m.SettingsSyncConvergentRoundManifest(), m.SettingsSyncConvergentOutcomeVerified()),
					m.SettingsSyncConvergentRound("2", "3", m.SettingsSyncConvergentRoundEntities(), m.SettingsSyncConvergentOutcomeVerified()),
					m.SettingsSyncConvergentRound("3", "3", m.SettingsSyncConvergentRoundState(), m.SettingsSyncConvergentOutcomeVerified()),
					m.SettingsSyncConvergentWrites("1"),
					m.SettingsSyncConvergentSummary("12", "1"),
					m.SettingsSyncConvergentLastVerified(formatSyncTime(1_700_000_000_000)),
					m.SettingsSyncConvergentPause(),
				}
				for _, want := range wants {
					scrollSettingsTo(t, tester, want, w, h, viewport)
				}
				if locale == "zh-CN" {
					en := locales.In("en")
					if tester.HasText(en.SettingsSyncConvergentTitle()) {
						t.Fatalf("English v2 title leaked into the Chinese panel: %q", tester.Texts())
					}
					if m.SettingsSyncConvergentPhaseVerifying() == en.SettingsSyncConvergentPhaseVerifying() {
						t.Fatal("the zh-CN verification phase is untranslated")
					}
				}
			})
		}
	}
}

// TestSyncConvergentMigrationRefusalIsFailClosed checks the refusal panel,
// its typed reason, and the shared accessible danger text color. It renders
// both sizes and both required locales.
func TestSyncConvergentMigrationRefusalIsFailClosed(t *testing.T) {
	wantDanger := syncDangerTextColor(ui.LightTheme())
	rawDanger := ui.LightTheme().Danger
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {1084, 661}, {720, 480}, {704, 421}} {
			locale, w, h := locale, size[0], size[1]
			t.Run(locale+"/"+sizeName(w, h), func(t *testing.T) {
				m := locales.In(locale)
				service := newFakeConvergentSyncService()
				service.snapshot.Convergent = appsync.ConvergentInfo{
					Phase:       "blocked",
					TotalRounds: appsync.ConvergentVerifyRounds,
					Migration: &appsync.ConvergentMigrationPreview{
						SchemaVersion:         2,
						ProviderSchemaVersion: 3,
						LocalSchemaVersion:    0,
						EntityCount:           5,
						BlockedReasons: []appsync.ConvergentBlockedReason{{
							Code: "unsupported-newer-schema",
						}},
					},
				}
				state := NewSyncState(service)
				var layout viewLayout
				var context *ui.Context
				view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
				tester := ui.NewTester(func(c *ui.Context) {
					context = c
					view(c)
				}, w, h)
				tester.SetLocale(locale)
				tester.Frame()
				viewport := layout.Content.Bounds(context)

				if _, ok := tester.Find(m.SettingsSyncConvergentMigrationBlocked()); !ok {
					// The refusal is below the fold at 720x480; wheel to it.
					scrollSettingsTo(t, tester, m.SettingsSyncConvergentState(
						m.SettingsSyncConvergentPhaseBlocked()), w, h, viewport)
				}
				for _, want := range []string{
					m.SettingsSyncConvergentState(m.SettingsSyncConvergentPhaseBlocked()),
					m.SettingsSyncConvergentMigrationBlocked(),
					m.SettingsSyncConvergentMigrationSummary("3", "0", "5"),
					m.SettingsSyncConvergentReasonNewer(),
				} {
					scrollSettingsTo(t, tester, want, w, h, viewport)
				}
				// The reason is the last content row, so the loop above leaves
				// it fully visible above the pinned bars. A small 11 DIP line
				// may have no exact token-colored pixel on some rasterizers,
				// so identify the ink actually painted against references of
				// the same line in the wanted and the rejected color.
				reasonText := m.SettingsSyncConvergentReasonNewer()
				reason := scrollSettingsTo(t, tester, reasonText, w, h, viewport)
				if ink := testerRectInk(t, tester, reason, reasonText, 11, false, wantDanger, rawDanger); ink != wantDanger {
					t.Errorf("the refusal reason %+v is painted %v, want syncDangerTextColor(%v)", reason, ink, wantDanger)
				}

				// The refusal title is above the reason, so after scrolling to
				// the last row wheel back up and sample it while it is painted
				// inside the viewport rather than clipped by its top edge. The
				// title is bold 12 DIP text, so its reference render uses the
				// same weight: a rasterizer that paints no exact token-colored
				// pixel must still be classified, not reported as wrong.
				titleText := m.SettingsSyncConvergentMigrationBlocked()
				refusal := scrollSettingsUp(t, tester, titleText, viewport)
				if ink := testerRectInk(t, tester, refusal, titleText, 12, true, wantDanger, rawDanger); ink != wantDanger {
					t.Errorf("the refusal title %+v is painted %v, want syncDangerTextColor(%v)", refusal, ink, wantDanger)
				}

				// The pinned text color clears 4.5:1 on the light surface.
				textLuminance, _ := domaintheme.HexLuminance("#b51616")
				backgroundLuminance, _ := domaintheme.HexLuminance("#f1f4f8")
				if ratio := domaintheme.ContrastRatio(textLuminance, backgroundLuminance); ratio < 4.5 {
					t.Errorf("refusal text contrast = %.2f:1, want at least 4.5:1", ratio)
				}

				// A refusal is not an automatic overwrite: re-attempting migration
				// is the only action offered while the marker is still absent.
				if !tester.HasText(m.SettingsSyncConvergentEnable()) {
					t.Fatalf("the safe retry action is missing: %q", tester.Texts())
				}
			})
		}
	}
}

// TestSyncConvergentDivergenceOffersOnlySafeActions checks the divergence
// report, its value-free detail, and the three safe actions at both sizes in
// English and Simplified Chinese. It also proves each action is wired.
func TestSyncConvergentDivergenceOffersOnlySafeActions(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {1084, 661}, {720, 480}, {704, 421}} {
			locale, w, h := locale, size[0], size[1]
			t.Run(locale+"/"+sizeName(w, h), func(t *testing.T) {
				m := locales.In(locale)
				service := newFakeConvergentSyncService()
				service.snapshot.Convergent.Phase = "diverged"
				service.snapshot.Convergent.Writes = 0
				service.snapshot.Convergent.Divergence = &appsync.ConvergentDivergenceInfo{
					Kind:         "equal-vector-different-state",
					Family:       "hosts",
					EntityID:     "host-1",
					Dot:          "node-a:7",
					LocalHash:    "aaaaaaaaaaaaaaaaaaaaaaaa",
					RemoteHash:   "bbbbbbbbbbbbbbbbbbbbbbbb",
					LocalVector:  map[string]uint64{"node-a": 3, "node-b": 2},
					RemoteVector: map[string]uint64{"node-a": 3, "node-b": 2},
					Actions:      []string{"inspect", "restore", "resync"},
				}
				state := NewSyncState(service)
				var layout viewLayout
				var context *ui.Context
				view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
				tester := ui.NewTester(func(c *ui.Context) {
					context = c
					view(c)
				}, w, h)
				tester.SetLocale(locale)
				tester.Frame()
				viewport := layout.Content.Bounds(context)

				wantAddress := m.SettingsSyncConvergentDivergenceAddress(m.SearchSectionHost(), "host-1")
				for _, want := range []string{
					m.SettingsSyncConvergentTitle(),
					m.SettingsSyncConvergentState(m.SettingsSyncConvergentPhaseDiverged()),
					m.SettingsSyncConvergentDivergenceTitle(),
					m.SettingsSyncConvergentDivergenceDescription(),
					m.SettingsSyncConvergentDivergenceKind(m.SettingsSyncConvergentDivergenceEqualVector()),
					wantAddress,
					m.SettingsSyncConvergentDivergenceDot("node-a:7"),
					m.SettingsSyncConvergentInspect(),
					m.SettingsSyncConvergentRestore(),
					m.SettingsSyncConvergentResync(),
					m.SettingsSyncConvergentActionsHint(),
					m.SettingsSyncConvergentRestoreHint(),
				} {
					scrollSettingsTo(t, tester, want, w, h, viewport)
				}
				// The payload family key is a protocol identifier and must be
				// localized, never printed raw beside the entity id.
				if tester.HasText("hosts host-1") {
					t.Fatalf("a raw entity family leaked into the divergence report: %q", tester.Texts())
				}
				// The report states the action policy, and all three offered
				// actions are exercised below. The full settings surface also
				// carries unrelated backup labels, so asserting on every visible
				// string would confuse a backup reason with an action.
				if !tester.HasText(m.SettingsSyncConvergentActionsHint()) {
					t.Fatalf("the safe-action hint is missing: %q", tester.Texts())
				}

				if err := tester.Click(m.SettingsSyncConvergentInspect()); err != nil {
					t.Fatal(err)
				}
				tester.Frame()
				detail := m.SettingsSyncConvergentInspectDetails("aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb")
				scrollSettingsTo(t, tester, detail, w, h, viewport)
				if !tester.HasText(m.SettingsSyncConvergentVectorDetail("2", "2")) {
					t.Fatalf("the vector detail is missing: %q", tester.Texts())
				}
				if !state.ShowConvergentDetails() {
					t.Fatal("Inspect did not open the detail")
				}

				// Re-sync runs the normal manual sync path and leaves the report
				// in place, so the user can still choose a safe restore.
				scrollSettingsTo(t, tester, m.SettingsSyncConvergentResync(), w, h, viewport)
				if err := tester.Click(m.SettingsSyncConvergentResync()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				service.mu.Lock()
				syncCalls := service.syncCalls
				service.mu.Unlock()
				if syncCalls != 1 {
					t.Fatalf("re-sync calls = %d", syncCalls)
				}

				// Restore is local-only. It applies the last verified copy and
				// deliberately clears the divergence report.
				tester.Frame()
				scrollSettingsTo(t, tester, m.SettingsSyncConvergentRestore(), w, h, viewport)
				if err := tester.Click(m.SettingsSyncConvergentRestore()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				service.mu.Lock()
				restores := service.restores
				service.mu.Unlock()
				if restores != 1 {
					t.Fatalf("restore calls = %d", restores)
				}
			})
		}
	}
}

// TestSyncConvergentActionsStayPinnedAboveTheScroll proves the safe actions
// are not placed in the scrolling report: their rectangles are fully inside
// the client before any wheel event and do not move while the report scrolls.
func TestSyncConvergentActionsStayPinnedAboveTheScroll(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {720, 480}, {704, 421}} {
			locale, w, h := locale, size[0], size[1]
			t.Run(locale+"/"+sizeName(w, h), func(t *testing.T) {
				m := locales.In(locale)
				service := newFakeConvergentSyncService()
				service.snapshot.Convergent.Phase = "diverged"
				service.snapshot.Convergent.Divergence = &appsync.ConvergentDivergenceInfo{
					Kind: "equal-vector-different-state",
				}
				state := NewSyncState(service)
				var layout viewLayout
				var context *ui.Context
				view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
				tester := ui.NewTester(func(c *ui.Context) {
					context = c
					view(c)
				}, w, h)
				tester.SetLocale(locale)
				tester.Frame()
				viewport := layout.Content.Bounds(context)

				labels := []string{
					m.SettingsSyncConvergentInspect(),
					m.SettingsSyncConvergentRestore(),
					m.SettingsSyncConvergentResync(),
				}
				before := map[string]ui.Rect{}
				for _, label := range labels {
					rect, ok := tester.Find(label)
					if !ok {
						t.Fatalf("%q is not laid out: %q", label, tester.Texts())
					}
					if !insidePinnedConvergentBar(rect, viewport, w, h) ||
						rect.H+0.5 < measuredSettingsTextHeight(t, label, ui.LightTheme().FontSize) {
						t.Fatalf("%q is not fully inside the pinned bar: %+v", label, rect)
					}
					if insideContentViewport(rect, viewport) {
						t.Fatalf("%q is laid out in the scrolling content viewport %+v rather than the pinned bar: %+v",
							label, viewport, rect)
					}
					before[label] = rect
				}
				for step := 0; step < 8; step++ {
					tester.Scroll(viewport.X+viewport.W-30, viewport.Y+viewport.H-12, 0, 40)
				}
				for _, label := range labels {
					after, ok := tester.Find(label)
					if !ok {
						t.Fatalf("%q disappeared after scrolling: %q", label, tester.Texts())
					}
					if after != before[label] {
						t.Fatalf("%q moved from %+v to %+v while the report scrolled", label, before[label], after)
					}
				}
			})
		}
	}
}

// TestSyncConvergentMigrationListsAndResolvesConflicts drives ticket 66's
// surface: the blocked preview lists every contested field with both values,
// a per-field choice reaches the service, and the bulk default does too.
func TestSyncConvergentMigrationListsAndResolvesConflicts(t *testing.T) {
	m := locales.In("en")
	service := newFakeConvergentSyncService()
	label := appsync.ConvergentMigrationConflict{
		Family:        "hosts",
		EntityID:      "host-1",
		Field:         "label",
		Register:      "hosts\x00host-1\x00label",
		LocalPresent:  true,
		RemotePresent: true,
		LocalValue:    json.RawMessage(`"This device name"`),
		RemoteValue:   json.RawMessage(`"Cloud name"`),
	}
	secret := appsync.ConvergentMigrationConflict{
		Family:        "secrets",
		EntityID:      "host-1/password",
		Field:         "value",
		Register:      "secrets\x00host-1/password\x00value",
		LocalPresent:  true,
		RemotePresent: true,
		// A blocked preview never carries these values; set them here so the
		// leak assertion below would actually catch a surface that printed a
		// secret-marked conflict's raw sides.
		LocalValue:  json.RawMessage(`"local-hunter2"`),
		RemoteValue: json.RawMessage(`"cloud-hunter2"`),
		Secret:      true,
	}
	presence := appsync.ConvergentMigrationConflict{
		Family:        "hosts",
		EntityID:      "host-2",
		Field:         domainsync.PresenceField,
		Register:      domainsync.RegisterIdentity("hosts", "host-2", domainsync.PresenceField),
		LocalPresent:  true,
		RemotePresent: false,
	}
	service.snapshot.Convergent = appsync.ConvergentInfo{
		Phase:         "blocked",
		ConflictCount: 3,
		Migration: &appsync.ConvergentMigrationPreview{
			SchemaVersion:  2,
			ConflictCount:  3,
			Conflicts:      []appsync.ConvergentMigrationConflict{label, presence, secret},
			BlockedReasons: []appsync.ConvergentBlockedReason{{Code: "conflicts-unresolved"}},
		},
	}
	state := NewSyncState(service)
	var layout viewLayout
	var context *ui.Context
	view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
	const w, h = 1100, 720
	tester := ui.NewTester(func(c *ui.Context) {
		context = c
		view(c)
	}, w, h)
	tester.SetLocale("en")
	tester.Frame()
	viewport := layout.Content.Bounds(context)

	for _, want := range []string{
		m.SettingsSyncConvergentConflictsTitle("3"),
		m.SettingsSyncConvergentConflictEntry(m.SearchSectionHost(), "host-1", "label"),
		m.SettingsSyncConvergentConflictValues("This device name", "Cloud name"),
		m.SettingsSyncConvergentConflictEntry(m.SearchSectionHost(), "host-2", m.SettingsSyncConvergentConflictFieldPresence()),
		m.SettingsSyncConvergentConflictValues(m.SettingsSyncConvergentConflictValuePresent(), m.SettingsSyncConvergentConflictValueMissing()),
		m.SettingsSyncConvergentConflictEntry(m.SettingsSyncEntityUnknown(), "host-1/password", "value"),
		m.SettingsSyncConvergentConflictValues(m.SettingsSyncConvergentConflictValueHidden(), m.SettingsSyncConvergentConflictValueHidden()),
	} {
		scrollSettingsTo(t, tester, want, w, h, viewport)
	}
	for _, leaked := range []string{
		`Cloud name"`, "local-hunter2", "cloud-hunter2",
		`"local-hunter2"`, `"cloud-hunter2"`, "secret",
	} {
		if tester.HasText(leaked) {
			t.Fatalf("raw conflict value %q leaked: %q", leaked, tester.Texts())
		}
	}

	if _, ok := tester.Find(m.SettingsSyncConflictUseLocal()); !ok {
		scrollSettingsTo(t, tester, m.SettingsSyncConflictUseLocal(), w, h, viewport)
	}
	if err := tester.Click(m.SettingsSyncConflictUseLocal()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)
	service.mu.Lock()
	resolutions := append([]appsync.ConvergentConflictResolution(nil), service.conflictResolutions...)
	service.mu.Unlock()
	if len(resolutions) != 1 || resolutions[0].Register != label.Register || resolutions[0].Side != appsync.ConvergentConflictKeepLocal {
		t.Fatalf("per-field resolutions = %+v", resolutions)
	}

	tester.Frame()
	if err := tester.Click(m.SettingsSyncConvergentKeepRemoteAll()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)
	service.mu.Lock()
	bulk := append([]appsync.ConvergentConflictSide(nil), service.bulkResolutions...)
	service.mu.Unlock()
	if len(bulk) != 1 || bulk[0] != appsync.ConvergentConflictKeepRemote {
		t.Fatalf("bulk resolutions = %+v", bulk)
	}
}

func sizeName(w, h int) string {
	return fmt.Sprintf("%dx%d", w, h)
}
