package settings

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/ui/locales"
)

type backupRestoreCall struct {
	source appsync.BackupSource
	id     string
}

// fakeDestructiveSyncService adds the guard, conflict, and backup surface to
// the sync fake. It records each intent; it never touches a Vault or network.
type fakeDestructiveSyncService struct {
	*fakeSyncService

	mu sync.Mutex

	loadCalls      int
	restoreGuards  int
	forcePushes    int
	resolutions    []appsync.Resolution
	backupRestores []backupRestoreCall
}

func newFakeDestructiveSyncService() *fakeDestructiveSyncService {
	fake := &fakeDestructiveSyncService{fakeSyncService: newFakeSyncService()}
	fake.snapshot.Guard = appsync.GuardFinding{
		Kind:          appsync.GuardShrink,
		EntityType:    "hosts",
		BaseCount:     4,
		OutgoingCount: 1,
		Lost:          3,
	}
	fake.snapshot.Conflict = &appsync.ConflictInfo{
		CreatedAt:       1_700_000_000_000,
		RemoteUpdatedAt: 1_700_000_100_000,
		RemoteDevice:    "Other device",
		RemoteVersion:   7,
		CanMerge:        true,
		Items: []appsync.ConflictItem{{
			EntityType: "hosts",
			EntityID:   "host-1",
			Kind:       appsync.ConflictBothModified,
		}},
	}
	fake.snapshot.Backups = []appsync.BackupInfo{
		{ID: "remote-1", Source: appsync.BackupSourceRemote, Reason: "conflict", CreatedAt: 1_700_000_200_000},
		{ID: "local-1", Source: appsync.BackupSourceLocal, Reason: "vault_change", CreatedAt: 1_700_000_200_000},
	}
	return fake
}

func (f *fakeDestructiveSyncService) LoadBackups(context.Context) error {
	f.mu.Lock()
	f.loadCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeDestructiveSyncService) RestoreBackup(_ context.Context, source appsync.BackupSource, id string) error {
	f.mu.Lock()
	f.backupRestores = append(f.backupRestores, backupRestoreCall{source, id})
	f.mu.Unlock()
	return nil
}

func (f *fakeDestructiveSyncService) RestoreGuard(context.Context) (appsync.Result, error) {
	f.mu.Lock()
	f.restoreGuards++
	f.mu.Unlock()
	return appsync.Result{}, nil
}

func (f *fakeDestructiveSyncService) ForcePush(context.Context) (appsync.Result, error) {
	f.mu.Lock()
	f.forcePushes++
	f.mu.Unlock()
	return appsync.Result{}, nil
}

func (f *fakeDestructiveSyncService) ResolveConflict(_ context.Context, resolution appsync.Resolution) (appsync.Result, error) {
	f.mu.Lock()
	f.resolutions = append(f.resolutions, resolution)
	f.mu.Unlock()
	return appsync.Result{}, nil
}

func TestSyncSurfaceShowsGuardConflictAndBackupsFromTheSnapshot(t *testing.T) {
	fake := newFakeDestructiveSyncService()
	state := NewSyncState(fake)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 1600)
	tester.SetLocale("en")
	tester.Frame()
	waitSyncIdle(t, state)
	tester.Frame()
	m := locales.In("en")

	for _, want := range []string{
		m.SettingsSyncGuardTitle(),
		m.SettingsSyncGuardShrink("3", "4", m.SearchSectionHost(), "1"),
		m.SettingsSyncGuardRestore(),
		m.SettingsSyncGuardForce(),
		m.SettingsSyncConflictTitle(),
		m.SettingsSyncConflictRemoteDevice("Other device"),
		m.SettingsSyncConflictItem(m.SearchSectionHost(), "host-1", m.SettingsSyncConflictKindBothModified()),
		m.SettingsSyncConflictUseLocal(),
		m.SettingsSyncConflictUseRemote(),
		m.SettingsSyncConflictMerge(),
		m.SettingsSyncBackupsTitle(),
		m.SettingsSyncBackupRestore(),
		m.SettingsSyncBackupRemote(),
		m.SettingsSyncBackupLocal(),
		m.SettingsSyncBackupReasonConflict(),
	} {
		if !tester.HasText(want) {
			t.Fatalf("%q is not on the sync surface: %v", want, tester.Texts())
		}
	}

	fake.mu.Lock()
	loads := fake.loadCalls
	fake.mu.Unlock()
	if loads == 0 {
		t.Fatal("the backup list was never loaded")
	}
}

func TestSyncSurfaceLocalizesEntityFamilies(t *testing.T) {
	fake := newFakeDestructiveSyncService()
	fake.snapshot.Backups = nil
	state := NewSyncState(fake)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 1600)
	tester.SetLocale("zh-CN")
	tester.Frame()
	waitSyncIdle(t, state)
	tester.Frame()
	m := locales.In("zh-CN")

	wants := []string{
		m.SettingsSyncGuardShrink("3", "4", m.SearchSectionHost(), "1"),
		m.SettingsSyncConflictItem(m.SearchSectionHost(), "host-1", m.SettingsSyncConflictKindBothModified()),
	}
	for _, want := range wants {
		if !tester.HasText(want) {
			t.Fatalf("%q is not localized on the sync surface: %v", want, tester.Texts())
		}
	}
	// The backend family key must never reach the renderer.
	if tester.HasText("hosts host-1") || tester.HasText("of 4 hosts") {
		t.Fatalf("a raw entity key is visible: %v", tester.Texts())
	}

	en := locales.In("en")
	known := map[string]string{
		"hosts":               en.SearchSectionHost(),
		"keys":                en.SearchSectionKey(),
		"identities":          en.SearchSectionIdentity(),
		"proxyProfiles":       en.SearchSectionProxyProfile(),
		"snippets":            en.SearchSectionSnippet(),
		"groups":              en.SearchSectionHostGroup(),
		"snippetPackages":     en.SearchSectionSnippetPackage(),
		"notes":               en.SearchSectionNote(),
		"noteGroups":          en.SearchSectionNoteGroup(),
		"portForwardingRules": en.TrayForwarding(),
		"settings":            en.NavSettings(),
	}
	for entityType, want := range known {
		if got := syncEntityLabel(entityType, en); got != want {
			t.Errorf("syncEntityLabel(%q) = %q, want %q", entityType, got, want)
		}
	}
	if got := syncEntityLabel("futureFamily", en); got != en.SettingsSyncEntityUnknown() {
		t.Errorf("unknown family label = %q", got)
	}
}

func TestGuardForcePushNeedsTheExplicitConfirmationAndNeverDefaultsOn(t *testing.T) {
	fake := newFakeDestructiveSyncService()
	fake.snapshot.Conflict = nil
	fake.snapshot.Backups = nil
	state := NewSyncState(fake)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 1600)
	tester.SetLocale("en")
	tester.Frame()
	waitSyncIdle(t, state)
	tester.Frame()
	m := locales.In("en")

	if state.ForcePushing() {
		t.Fatal("the destructive confirmation opened by default")
	}
	if err := tester.Click(m.SettingsSyncGuardForce()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !state.ForcePushing() || !tester.HasText(m.SettingsSyncGuardForceWarning()) {
		t.Fatalf("the confirmation is not visible: %v", tester.Texts())
	}
	fake.mu.Lock()
	pushes := fake.forcePushes
	fake.mu.Unlock()
	if pushes != 0 {
		t.Fatalf("force push ran before the confirmation: %d", pushes)
	}

	if err := tester.Click(m.SettingsSyncDoForcePush()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)
	tester.Frame()
	fake.mu.Lock()
	pushes = fake.forcePushes
	fake.mu.Unlock()
	if pushes != 1 {
		t.Fatalf("force push calls = %d", pushes)
	}
	if state.ForcePushing() {
		t.Fatal("the confirmation stayed open after the explicit push")
	}
}

func TestSyncSurfaceAppliesTheGuardRestore(t *testing.T) {
	fake := newFakeDestructiveSyncService()
	fake.snapshot.Conflict = nil
	fake.snapshot.Backups = nil
	state := NewSyncState(fake)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 1600)
	tester.SetLocale("en")
	tester.Frame()
	waitSyncIdle(t, state)
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsSyncGuardRestore()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)

	fake.mu.Lock()
	guards := fake.restoreGuards
	fake.mu.Unlock()
	if guards != 1 {
		t.Fatalf("restore guard calls = %d", guards)
	}
}

func TestSyncSurfaceAppliesTheConflictResolution(t *testing.T) {
	fake := newFakeDestructiveSyncService()
	fake.snapshot.Guard = appsync.GuardFinding{}
	fake.snapshot.Backups = nil
	state := NewSyncState(fake)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 1600)
	tester.SetLocale("en")
	tester.Frame()
	waitSyncIdle(t, state)
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsSyncConflictUseLocal()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)

	fake.mu.Lock()
	resolutions := append([]appsync.Resolution(nil), fake.resolutions...)
	fake.mu.Unlock()
	if len(resolutions) != 1 || resolutions[0] != appsync.ResolutionLocal {
		t.Fatalf("resolutions = %v", resolutions)
	}
}

func TestSuccessfulDestructiveActionsClearTheBlockedSyncError(t *testing.T) {
	cases := []struct {
		name string
		run  func(*SyncState)
	}{
		{"force push", func(state *SyncState) { state.ForcePush() }},
		{"guard restore", func(state *SyncState) { state.RestoreGuard() }},
		{"conflict resolution", func(state *SyncState) { state.ResolveConflict(appsync.ResolutionRemote) }},
		{"backup restore", func(state *SyncState) { state.RestoreBackup(appsync.BackupSourceLocal, "local-1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDestructiveSyncService()
			state := NewSyncState(fake)
			state.lastErr = errors.New("sync is blocked")
			tc.run(state)
			waitSyncIdle(t, state)
			if err := state.Err(); err != nil {
				t.Fatalf("a successful %s left the old error behind: %v", tc.name, err)
			}
		})
	}
}

func TestSyncSurfaceRestoresTheFirstListedBackup(t *testing.T) {
	fake := newFakeDestructiveSyncService()
	fake.snapshot.Guard = appsync.GuardFinding{}
	fake.snapshot.Conflict = nil
	state := NewSyncState(fake)
	tester := ui.NewTester(viewAt(AreaSync, Config{Sync: state}), 1100, 1600)
	tester.SetLocale("en")
	tester.Frame()
	waitSyncIdle(t, state)
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsSyncBackupRestore()); err != nil {
		t.Fatal(err)
	}
	waitSyncIdle(t, state)

	fake.mu.Lock()
	restores := append([]backupRestoreCall(nil), fake.backupRestores...)
	fake.mu.Unlock()
	if len(restores) != 1 || restores[0].source != appsync.BackupSourceRemote || restores[0].id != "remote-1" {
		t.Fatalf("backup restores = %v", restores)
	}
}
