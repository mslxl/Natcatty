package settings

// This file is the ticket-54 acceptance regression for the Sync page's
// primary actions. Independent acceptance at 9ba887d6 found that at the
// required 720x480 and 1100x720 windows (704x421 and 1084x661 client areas)
// the conflict resolution row was cut at the bottom scroll edge, the zh-CN
// shrink-guard Restore/Force row was reduced to slivers, and the setup
// Connect and the backups Refresh controls were cut in several states.
//
// The actions must be pinned in a bar outside the scrolling content: fully
// inside the client, below the bounded content viewport, immutable while the
// content scrolls, and clickable there. The content itself must still scroll
// its last row fully into the viewport above that bar.

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

// viewAtWithLayout is viewAt with the measured content viewport and sync
// action bar bound.
func viewAtWithLayout(area Area, config Config, layout *viewLayout) func(*ui.Context) {
	search := NewSearchState()
	search.SetArea(area)
	config.Search = search
	return viewWithLayout(config, layout)
}

// syncActionShellCase builds one Sync surface state. actions are the labels
// the pinned bar must show; lastText is the final text row of the scrolling
// content, which must scroll fully into the viewport.
type syncActionShellCase struct {
	name     string
	service  func(t *testing.T) SyncService
	prepare  func(state *SyncState)
	actions  func(m locales.Messages) []string
	lastText func(m locales.Messages, service SyncService) string
	click    func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages)
	// assertConvergentBar also checks ticket 55's pinned bar, which is
	// rendered below ticket 54's bar, still sits in its bottom band.
	assertConvergentBar bool
}

func backupRows(count int) []appsync.BackupInfo {
	rows := make([]appsync.BackupInfo, 0, count)
	for i := 0; i < count; i++ {
		source := appsync.BackupSourceLocal
		reason := "vault_change"
		if i%3 == 1 {
			source = appsync.BackupSourceRemote
			reason = "merge"
		}
		if i%3 == 2 {
			reason = "guard-force"
		}
		rows = append(rows, appsync.BackupInfo{
			ID:        fmt.Sprintf("backup-%02d", i),
			Source:    source,
			Reason:    reason,
			CreatedAt: 1_700_000_000_000 + int64(i)*60_000,
		})
	}
	return rows
}

func backupRowText(m locales.Messages, backup appsync.BackupInfo) string {
	return fmt.Sprintf("%s · %s · %s",
		syncBackupSourceName(backup.Source, m),
		syncBackupReasonName(backup.Reason, m),
		formatSyncTime(backup.CreatedAt))
}

func lastBackupText(m locales.Messages, service SyncService) string {
	fake, ok := service.(*fakeDestructiveSyncService)
	if !ok {
		return ""
	}
	rows := fake.snapshot.Backups
	return backupRowText(m, rows[len(rows)-1])
}

// fakeActionsConvergentService implements both the destructive ticket-54
// capability and ticket 55's convergent capability, so the two pinned bars
// are rendered together.
type fakeActionsConvergentService struct {
	*fakeDestructiveSyncService
	convergentCalls []bool
	restores        int
}

func newFakeActionsConvergentService() *fakeActionsConvergentService {
	fake := &fakeActionsConvergentService{fakeDestructiveSyncService: newFakeDestructiveSyncService()}
	fake.snapshot.Convergent = appsync.ConvergentInfo{
		SchemaVersion: 2,
		Enabled:       true,
		Phase:         "converged",
		TotalRounds:   3,
		EntityCount:   4,
	}
	return fake
}

func (f *fakeActionsConvergentService) SetConvergentEnabled(_ context.Context, enabled bool) (appsync.ConvergentMigrationPreview, error) {
	f.mu.Lock()
	f.convergentCalls = append(f.convergentCalls, enabled)
	f.mu.Unlock()
	return appsync.ConvergentMigrationPreview{SchemaVersion: 2, AlreadyInitialized: true}, nil
}

func (f *fakeActionsConvergentService) RestoreLastVerified(context.Context) (appsync.Result, error) {
	f.mu.Lock()
	f.restores++
	f.mu.Unlock()
	return appsync.Result{}, nil
}

// syncActionShellCases covers every ticket-54 surface the acceptance found
// clipped, with the worst case (conflict and guard together) first.
func syncActionShellCases() []syncActionShellCase {
	return []syncActionShellCase{
		{
			name: "conflict-and-shrink-guard",
			service: func(t *testing.T) SyncService {
				service := newFakeDestructiveSyncService()
				service.snapshot.Backups = backupRows(14)
				return service
			},
			actions: func(m locales.Messages) []string {
				return []string{
					m.SettingsSyncConflictUseLocal(),
					m.SettingsSyncConflictUseRemote(),
					m.SettingsSyncConflictMerge(),
					m.SettingsSyncGuardRestore(),
					m.SettingsSyncGuardForce(),
					m.SettingsSyncBackupsRefresh(),
				}
			},
			lastText: lastBackupText,
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				if err := tester.Click(m.SettingsSyncConflictUseRemote()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				fake := service.(*fakeDestructiveSyncService)
				fake.mu.Lock()
				resolutions := append([]appsync.Resolution(nil), fake.resolutions...)
				fake.mu.Unlock()
				if len(resolutions) != 1 || resolutions[0] != appsync.ResolutionRemote {
					t.Fatalf("pinned conflict click resolved %v, want one remote resolution", resolutions)
				}
			},
		},
		{
			name: "conflict-guard-backups-with-convergent-bar",
			service: func(t *testing.T) SyncService {
				service := newFakeActionsConvergentService()
				service.snapshot.Backups = backupRows(14)
				return service
			},
			actions: func(m locales.Messages) []string {
				return []string{
					m.SettingsSyncConflictUseLocal(),
					m.SettingsSyncConflictUseRemote(),
					m.SettingsSyncConflictMerge(),
					m.SettingsSyncGuardRestore(),
					m.SettingsSyncGuardForce(),
					m.SettingsSyncBackupsRefresh(),
				}
			},
			lastText: func(m locales.Messages, service SyncService) string {
				rows := service.Snapshot().Backups
				return backupRowText(m, rows[len(rows)-1])
			},
			assertConvergentBar: true,
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				// Ticket 55's own pinned bar must still be rendered below
				// ticket 54's bar while the content scrolled above both.
				if _, ok := tester.Find(m.SettingsSyncConvergentPause()); !ok {
					t.Fatalf("the convergent bar disappeared behind ticket 54's bar: %q", tester.Texts())
				}
				if err := tester.Click(m.SettingsSyncConflictUseRemote()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				fake := service.(*fakeActionsConvergentService)
				fake.mu.Lock()
				resolutions := append([]appsync.Resolution(nil), fake.resolutions...)
				fake.mu.Unlock()
				if len(resolutions) != 1 || resolutions[0] != appsync.ResolutionRemote {
					t.Fatalf("pinned conflict click resolved %v, want one remote resolution", resolutions)
				}
			},
		},
		{
			name: "shrink-guard-force-confirmation",
			service: func(t *testing.T) SyncService {
				service := newFakeDestructiveSyncService()
				service.snapshot.Conflict = nil
				service.snapshot.Backups = backupRows(8)
				return service
			},
			prepare: func(state *SyncState) { state.AskForcePush() },
			actions: func(m locales.Messages) []string {
				return []string{
					m.SettingsSyncGuardRestore(),
					m.SettingsSyncDoForcePush(),
					m.SettingsCancel(),
				}
			},
			lastText: lastBackupText,
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				if err := tester.Click(m.SettingsSyncDoForcePush()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				fake := service.(*fakeDestructiveSyncService)
				fake.mu.Lock()
				pushes := fake.forcePushes
				fake.mu.Unlock()
				if pushes != 1 {
					t.Fatalf("pinned force-push click pushed %d times, want 1", pushes)
				}
			},
		},
		{
			name: "tombstone-guard",
			service: func(t *testing.T) SyncService {
				service := newFakeDestructiveSyncService()
				service.snapshot.Conflict = nil
				service.snapshot.Guard = appsync.GuardFinding{
					Kind:      appsync.GuardTombstone,
					BaseCount: 1,
					Lost:      1,
				}
				service.snapshot.Backups = backupRows(8)
				return service
			},
			actions: func(m locales.Messages) []string {
				return []string{
					m.SettingsSyncGuardRestore(),
					m.SettingsSyncGuardForce(),
					m.SettingsSyncBackupsRefresh(),
				}
			},
			lastText: lastBackupText,
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				if err := tester.Click(m.SettingsSyncGuardRestore()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				fake := service.(*fakeDestructiveSyncService)
				fake.mu.Lock()
				restores := fake.restoreGuards
				fake.mu.Unlock()
				if restores != 1 {
					t.Fatalf("pinned guard restore clicked %d times, want 1", restores)
				}
			},
		},
		{
			name: "setup-connect",
			service: func(t *testing.T) SyncService {
				service := newFakeWebDAVSyncService()
				service.snapshot = appsync.Snapshot{
					Connected:      false,
					Provider:       appsync.ProviderWebDAV,
					Status:         appsync.StatusNeedsSetup,
					WebDAVEndpoint: "https://dav.example.test/dav",
					WebDAVAuthType: "basic",
				}
				return service
			},
			prepare: func(state *SyncState) {
				state.SetPassword("master password")
				state.EnsureWebDAVForm()
				state.SetWebDAVEndpoint("https://dav.example.test/dav")
				state.SetWebDAVAuthType("basic")
				state.SetWebDAVUsername("tester")
				state.SetWebDAVPassword("dav password")
			},
			actions: func(m locales.Messages) []string {
				return []string{m.SettingsSyncConnect()}
			},
			lastText: func(m locales.Messages, service SyncService) string {
				return m.SettingsSyncSecurityNote()
			},
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				if err := tester.Click(m.SettingsSyncConnect()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				fake := service.(*fakeWebDAVSyncService)
				fake.webdavMu.Lock()
				connects := len(fake.connectSettings)
				fake.webdavMu.Unlock()
				if connects != 1 {
					t.Fatalf("pinned Connect clicked %d times, want 1", connects)
				}
			},
		},
		{
			name: "setup-insecure-prompt",
			service: func(t *testing.T) SyncService {
				service := newFakeWebDAVSyncService()
				service.snapshot = appsync.Snapshot{
					Connected: false,
					Provider:  appsync.ProviderWebDAV,
					Status:    appsync.StatusNeedsSetup,
				}
				return service
			},
			prepare: func(state *SyncState) {
				state.SetPassword("master password")
				state.EnsureWebDAVForm()
				state.SetWebDAVEndpoint("http://dav.example.test/dav")
				state.SetWebDAVAuthType("basic")
				state.SetWebDAVUsername("tester")
				state.SetWebDAVPassword("dav password")
				state.Connect()
			},
			actions: func(m locales.Messages) []string {
				// The plain-HTTP decision replaces Connect; its Confirm and
				// Cancel are pinned so the decision is never clipped.
				return []string{m.SettingsSyncWebDAVInsecureConfirm(), m.SettingsCancel()}
			},
			lastText: func(m locales.Messages, service SyncService) string {
				return m.SettingsSyncWebDAVInsecureWarning()
			},
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				if !state.InsecurePrompt() {
					t.Fatal("the plain-HTTP prompt was not open before Cancel")
				}
				if err := tester.Click(m.SettingsCancel()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				if state.InsecurePrompt() {
					t.Fatal("the pinned Cancel did not dismiss the plain-HTTP prompt")
				}
			},
		},
		{
			name: "backups-only",
			service: func(t *testing.T) SyncService {
				service := newFakeDestructiveSyncService()
				service.snapshot.Conflict = nil
				service.snapshot.Guard = appsync.GuardFinding{}
				service.snapshot.Backups = backupRows(14)
				return service
			},
			actions: func(m locales.Messages) []string {
				return []string{m.SettingsSyncBackupsRefresh()}
			},
			lastText: lastBackupText,
			click: func(t *testing.T, tester *ui.Tester, state *SyncState, service SyncService, m locales.Messages) {
				t.Helper()
				fake := service.(*fakeDestructiveSyncService)
				fake.mu.Lock()
				before := fake.loadCalls
				fake.mu.Unlock()
				if err := tester.Click(m.SettingsSyncBackupsRefresh()); err != nil {
					t.Fatal(err)
				}
				waitSyncIdle(t, state)
				fake.mu.Lock()
				after := fake.loadCalls
				fake.mu.Unlock()
				if after <= before {
					t.Fatalf("pinned Refresh did not reload the backups: %d -> %d", before, after)
				}
			},
		},
	}
}

// insideClientRect reports whether r lies wholly in the w x h client area.
func insideClientRect(r ui.Rect, w, h int) bool {
	return r.W > 0 && r.H > 0 &&
		r.X >= -0.5 && r.Y >= -0.5 &&
		r.X+r.W <= float32(w)+0.5 && r.Y+r.H <= float32(h)+0.5
}

// TestSyncPrimaryActionsStayPinnedAtTheRealClientSizes renders the ticket-54
// surfaces at the real 704x421 and 1084x661 client areas of the minimum and
// default windows, in English and Simplified Chinese.
func TestSyncPrimaryActionsStayPinnedAtTheRealClientSizes(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{"704x421", 704, 421},
		{"1084x661", 1084, 661},
	} {
		for _, locale := range []string{"en", "zh-CN"} {
			for _, test := range syncActionShellCases() {
				size, locale, test := size, locale, test
				t.Run(locale+"/"+size.name+"/"+test.name, func(t *testing.T) {
					m := locales.In(locale)
					service := test.service(t)
					state := NewSyncState(service)
					if test.prepare != nil {
						test.prepare(state)
					}
					var layout viewLayout
					var context *ui.Context
					view := viewAtWithLayout(AreaSync, Config{Sync: state}, &layout)
					tester := ui.NewTester(func(c *ui.Context) {
						context = c
						view(c)
					}, size.width, size.height)
					tester.SetLocale(locale)
					tester.Frame()
					waitSyncIdle(t, state)
					tester.Frame()

					actions := test.actions(m)
					lastText := test.lastText(m, service)
					client := ui.Rect{W: float32(size.width), H: float32(size.height)}
					viewport := layout.Content.Bounds(context)
					bar := layout.SyncActions.Bounds(context)
					if viewport.W <= 0 || viewport.H <= 0 {
						t.Fatalf("%s %dx%d: the content viewport has no size: %+v",
							locale, size.width, size.height, viewport)
					}
					if !insideClientRect(viewport, size.width, size.height) {
						t.Fatalf("%s %dx%d: the content viewport is outside the client %+v: %+v",
							locale, size.width, size.height, client, viewport)
					}

					// Measure the actions themselves first, so the red run
					// reports the acceptance's clipping symptoms (a row cut at
					// the scroll edge or reduced to slivers) rather than only
					// the missing bar.
					before := map[string]ui.Rect{}
					for _, label := range actions {
						rect, ok := tester.Find(label)
						if !ok {
							t.Fatalf("%s %dx%d: the primary action %q is not rendered: %q",
								locale, size.width, size.height, label, tester.Texts())
						}
						t.Logf("%s %dx%d: action %q at %+v", locale, size.width, size.height, label, rect)
						if rect.W <= 0 || rect.H < 18 {
							t.Fatalf("%s %dx%d: the primary action %q is a clipped sliver: %+v",
								locale, size.width, size.height, label, rect)
						}
						if !insideClientRect(rect, size.width, size.height) {
							t.Fatalf("%s %dx%d: the primary action %q is cut at the client edge %+v: %+v",
								locale, size.width, size.height, label, client, rect)
						}
						if rect.Y < viewport.Y+viewport.H-0.5 {
							t.Fatalf("%s %dx%d: the primary action %q %+v sits inside the scrolling content viewport %+v",
								locale, size.width, size.height, label, rect, viewport)
						}
						before[label] = rect
					}

					if bar.W <= 0 || bar.H <= 0 {
						t.Fatalf("%s %dx%d: there is no pinned action bar: %+v (actions %v)",
							locale, size.width, size.height, bar, actions)
					}
					if !insideClientRect(bar, size.width, size.height) {
						t.Fatalf("%s %dx%d: the pinned action bar is outside the client %+v: %+v",
							locale, size.width, size.height, client, bar)
					}
					if viewport.Y+viewport.H > bar.Y+0.5 {
						t.Fatalf("%s %dx%d: the content viewport %+v overlaps the pinned action bar %+v",
							locale, size.width, size.height, viewport, bar)
					}
					for _, label := range actions {
						rect := before[label]
						if rect.X < bar.X-0.5 || rect.Y < bar.Y-0.5 ||
							rect.X+rect.W > bar.X+bar.W+0.5 || rect.Y+rect.H > bar.Y+bar.H+0.5 {
							t.Fatalf("%s %dx%d: the primary action %q %+v is not fully inside the pinned bar %+v",
								locale, size.width, size.height, label, rect, bar)
						}
					}

					if test.assertConvergentBar {
						// Ticket 54's bar is a sibling above ticket 55's bar;
						// ticket 55 requires its own controls in the bottom
						// [h-52-8, h] band, so the two must not overlap.
						pause, ok := tester.Find(m.SettingsSyncConvergentPause())
						if !ok {
							t.Fatalf("%s %dx%d: ticket 55's convergent bar is missing: %q",
								locale, size.width, size.height, tester.Texts())
						}
						if bar.Y+bar.H > float32(size.height)-syncConvergentBarHeight+0.5 {
							t.Fatalf("%s %dx%d: ticket 54's bar %+v reaches into ticket 55's bottom band",
								locale, size.width, size.height, bar)
						}
						if pause.X < 190-0.5 || pause.Y < float32(size.height)-syncConvergentBarHeight-8-0.5 ||
							pause.Y+pause.H > float32(size.height)+0.5 {
							t.Fatalf("%s %dx%d: ticket 55's pinned action %q %+v left its bottom band",
								locale, size.width, size.height, m.SettingsSyncConvergentPause(), pause)
						}
						t.Logf("%s %dx%d: convergent action %q at %+v below ticket 54's bar %+v",
							locale, size.width, size.height, m.SettingsSyncConvergentPause(), pause, bar)
					}

					last, ok := tester.Find(lastText)
					if !ok {
						t.Fatalf("%s %dx%d: the last content row %q is not rendered: %q",
							locale, size.width, size.height, lastText, tester.Texts())
					}
					if last.H > viewport.H+0.5 {
						t.Fatalf("%s %dx%d: the last content row is taller than the viewport: %+v viewport %+v",
							locale, size.width, size.height, last, viewport)
					}
					scrollX := viewport.X + viewport.W - 30
					scrollY := viewport.Y + viewport.H - 12
					revealed := func() bool {
						row, ok := tester.Find(lastText)
						// A row clipped by the viewport keeps only its visible
						// sliver as the measured height; require the full
						// body-text height, not just a positive fragment.
						return ok && row.H >= 12 &&
							row.Y >= viewport.Y-0.5 &&
							row.Y+row.H <= viewport.Y+viewport.H+0.5
					}
					for step := 0; step < 240 && !revealed(); step++ {
						tester.Scroll(scrollX, scrollY, 0, 60)
						tester.Frame()
					}
					last, _ = tester.Find(lastText)
					t.Logf("%s %dx%d/%s: viewport %+v bar %+v last row %+v actions %v",
						locale, size.width, size.height, test.name, viewport, bar, last, actions)
					if !revealed() {
						t.Fatalf("%s %dx%d: the content never scrolled its last row %q into the viewport %+v: %+v",
							locale, size.width, size.height, lastText, viewport, last)
					}
					if !insideClientRect(last, size.width, size.height) {
						t.Fatalf("%s %dx%d: the last content row is outside the client: %+v",
							locale, size.width, size.height, last)
					}
					if last.Y+last.H > bar.Y+0.5 {
						t.Fatalf("%s %dx%d: the last content row runs under the pinned bar %+v: %+v",
							locale, size.width, size.height, bar, last)
					}

					for _, label := range actions {
						after, ok := tester.Find(label)
						if !ok {
							t.Fatalf("%s %dx%d: the pinned action %q disappeared while scrolling: %q",
								locale, size.width, size.height, label, tester.Texts())
						}
						if after != before[label] {
							t.Fatalf("%s %dx%d: the pinned action %q moved from %+v to %+v while the content scrolled",
								locale, size.width, size.height, label, before[label], after)
						}
					}

					test.click(t, tester, state, service, m)
				})
			}
		}
	}
}
