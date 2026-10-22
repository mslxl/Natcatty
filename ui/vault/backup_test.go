package vault

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

type viewBackups struct {
	items        []domainbackup.Summary
	retention    int
	encryption   bool
	status       appbackup.Status
	restoreCalls []string
	clearCalls   int
}

func (f *viewBackups) List(context.Context) ([]domainbackup.Summary, error) {
	return append([]domainbackup.Summary(nil), f.items...), nil
}

func (f *viewBackups) Restore(_ context.Context, id string) error {
	f.restoreCalls = append(f.restoreCalls, id)
	f.items = nil
	f.status = appbackup.Status{Kind: appbackup.StatusRestored}
	return nil
}

func (f *viewBackups) Clear(context.Context) (int, error) {
	f.clearCalls++
	count := len(f.items)
	f.items = nil
	f.status = appbackup.Status{Kind: appbackup.StatusCleared, Pruned: count}
	return count, nil
}

func (f *viewBackups) Retention() int { return f.retention }

func (f *viewBackups) SetRetention(_ context.Context, value int) (int, int, error) {
	applied := domainbackup.SanitizeRetention(value)
	pruned := 0
	if len(f.items) > applied {
		pruned = len(f.items) - applied
		f.items = f.items[:applied]
	}
	f.retention = applied
	return applied, pruned, nil
}

func (f *viewBackups) EncryptionAvailable() bool    { return f.encryption }
func (f *viewBackups) LastStatus() appbackup.Status { return f.status }

func viewBackup(id string, createdAt int64, reason domainbackup.Reason) domainbackup.Summary {
	return domainbackup.Summary{
		ID: id, CreatedAt: createdAt, Reason: reason,
		Fingerprint: strings.Repeat("ab", 32), SizeBytes: 2048,
		Counts: domainbackup.Counts{Hosts: 2, Keys: 1},
	}
}

func newBackupView(t *testing.T, width, height int) (*vaultstate.State, *viewBackups, *ui.Tester) {
	t.Helper()
	backups := &viewBackups{
		retention: 20, encryption: true,
		items: []domainbackup.Summary{
			viewBackup("b1", 1_700_000_000_000, domainbackup.ReasonVaultChange),
			viewBackup("b2", 1_699_000_000_000, domainbackup.ReasonBeforeRestore),
			viewBackup("b3", 1_698_000_000_000, domainbackup.ReasonAppVersionChange),
		},
	}
	state := vaultstate.NewState(&fakeHosts{}, vaultstate.WithBackups(backups))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := newProductionTester(t, state, width, height)
	return state, backups, tt
}

func openBackupPanel(t *testing.T, state *vaultstate.State, tt *ui.Tester, locale string) locales.Messages {
	t.Helper()
	m := locales.In(locale)
	tt.SetLocale(locale)
	tt.Frame()
	if err := tt.Click(m.VaultBackupOpen()); err != nil {
		t.Fatalf("%s: click Backups: %v", locale, err)
	}
	tt.Frame()
	if !state.BackupsOpen() {
		t.Fatalf("%s: the Backups button did not open the panel", locale)
	}
	return m
}

func TestBackupPanelRendersAtBothSizesInEnglishAndChinese(t *testing.T) {
	for _, size := range editorSizes {
		for _, locale := range []string{"en", "zh-CN"} {
			name := fmt.Sprintf("%dx%d/%s", size.width, size.height, locale)
			t.Run(name, func(t *testing.T) {
				state, _, tt := newBackupView(t, size.width, size.height)
				m := openBackupPanel(t, state, tt, locale)

				for _, want := range []string{
					m.VaultBackupTitle(), m.VaultBackupRetentionLabel(),
					m.VaultBackupRetentionHint(
						domainbackup.MinRetention, domainbackup.MaxRetention,
						domainbackup.DefaultRetention),
					m.VaultBackupReasonVaultChange(),
					m.VaultBackupRecords(3),
					m.VaultBackupFingerprint("abababababab…"),
					m.VaultBackupRestore(), m.VaultBackupClear(),
					m.VaultBackupListTitle(3),
				} {
					if !tt.HasText(want) {
						t.Errorf("%q is missing, texts %q", want, tt.Texts())
					}
				}
				assertVisible(t, tt, m.VaultBackupTitle(), size.width, size.height)
				assertVisible(t, tt, m.VaultBackupRetentionLabel(), size.width, size.height)
				assertVisible(t, tt, m.VaultBackupRestore(), size.width, size.height)
				assertNoHorizontalOverflow(t, tt, size.width, size.height)
			})
		}
	}
}

// TestBackupRowsClearTheScrollbar proves the scrolling list reserves the
// overlay scroll bar's track: a row's action button must end left of it.
func TestBackupRowsClearTheScrollbar(t *testing.T) {
	const track = 12
	for _, size := range editorSizes {
		for _, locale := range []string{"en", "zh-CN"} {
			name := fmt.Sprintf("%dx%d/%s", size.width, size.height, locale)
			t.Run(name, func(t *testing.T) {
				state, _, tt := newBackupView(t, size.width, size.height)
				openBackupPanel(t, state, tt, locale)

				var layout backupPanelLayout
				previous := backupLayoutProbe
				backupLayoutProbe = func(found backupPanelLayout) { layout = found }
				defer func() { backupLayoutProbe = previous }()
				// The probe reports the frame that built the boxes, so a second
				// frame records the final layout.
				tt.Frame()
				tt.Frame()

				if layout.gutter != track {
					t.Fatalf("reserved %.1f DIPs for the scroll bar, want %d", layout.gutter, track)
				}
				if layout.viewport.W <= 0 || layout.viewport.H <= 0 {
					t.Fatalf("the list viewport was not laid out: %+v", layout.viewport)
				}
				if layout.action.W <= 0 || layout.action.H <= 0 {
					t.Fatalf("the row action was not laid out: %+v", layout.action)
				}
				const epsilon = 0.5
				right := layout.action.X + layout.action.W
				usable := layout.viewport.X + layout.viewport.W - track
				if right > usable+epsilon {
					t.Fatalf("the row action runs under the scroll bar: action right %.1f, usable right %.1f (action %+v, viewport %+v)",
						right, usable, layout.action, layout.viewport)
				}
				if layout.viewport.Y < 0 || layout.viewport.Y+layout.viewport.H > float32(size.height)+epsilon {
					t.Fatalf("the list viewport leaves the window: %+v", layout.viewport)
				}
			})
		}
	}
}

func TestBackupRestoreAndClearRequireConfirmation(t *testing.T) {
	m := locales.In("en")
	state, backups, tt := newBackupView(t, 1100, 720)
	openBackupPanel(t, state, tt, "en")

	if err := tt.Click(m.VaultBackupRestore()); err != nil {
		t.Fatalf("click Restore: %v", err)
	}
	tt.Frame()
	id := state.BackupPendingRestore()
	if id == "" {
		t.Fatal("the restore confirmation did not open")
	}
	if len(backups.restoreCalls) != 0 {
		t.Fatal("restore ran without confirmation")
	}
	var pending domainbackup.Summary
	for _, item := range backups.items {
		if item.ID == id {
			pending = item
		}
	}
	confirm := m.VaultBackupRestoreConfirm(backupTime(pending))
	if !tt.HasText(confirm) {
		t.Fatalf("the confirmation text is missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultHostCancel()); err != nil {
		t.Fatalf("cancel restore: %v", err)
	}
	tt.Frame()
	if state.BackupPendingRestore() != "" || len(backups.restoreCalls) != 0 {
		t.Fatal("Cancel did not dismiss the restore confirmation")
	}

	if err := tt.Click(m.VaultBackupRestore()); err != nil {
		t.Fatalf("reopen restore confirmation: %v", err)
	}
	tt.Frame()
	if err := tt.Click(m.VaultBackupRestoreConfirmAction()); err != nil {
		t.Fatalf("confirm restore: %v", err)
	}
	tt.Frame()
	if len(backups.restoreCalls) != 1 || backups.restoreCalls[0] != id {
		t.Fatalf("restore calls = %v, want %q", backups.restoreCalls, id)
	}
	if !tt.HasText(m.VaultBackupEmpty()) {
		t.Fatalf("the empty state is missing after restore, texts %q", tt.Texts())
	}

	// Clear has its own confirmation and never runs before it.
	backups.items = []domainbackup.Summary{viewBackup("b4", 1_700_000_000_000, domainbackup.ReasonVaultChange)}
	state.RefreshBackups()
	tt.Frame()
	if err := tt.Click(m.VaultBackupClear()); err != nil {
		t.Fatalf("click Clear: %v", err)
	}
	tt.Frame()
	if backups.clearCalls != 0 || !state.BackupPendingClear() {
		t.Fatal("Clear ran without confirmation")
	}
	if !tt.HasText(m.VaultBackupClearConfirm(1)) {
		t.Fatalf("the clear confirmation is missing, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultBackupClear()); err != nil {
		t.Fatalf("confirm clear: %v", err)
	}
	tt.Frame()
	if backups.clearCalls != 1 {
		t.Fatalf("clear calls = %d, want 1", backups.clearCalls)
	}
}

func TestBackupPanelEmptyUnavailableAndNotice(t *testing.T) {
	backups := &viewBackups{
		retention: 20, encryption: false,
		status: appbackup.Status{Kind: appbackup.StatusSkippedTooLarge, Pruned: 2},
	}
	state := vaultstate.NewState(&fakeHosts{}, vaultstate.WithBackups(backups))
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(View(state), 720, 480)
	tt.SetLocale("en")
	tt.Frame()
	m := locales.In("en")
	state.BeginBackups()
	tt.Frame()

	for _, want := range []string{
		m.VaultBackupEmpty(), m.VaultBackupUnavailable(),
		m.VaultBackupSkippedTooLarge(), m.VaultBackupPruned(2),
	} {
		if !tt.HasText(want) {
			t.Errorf("%q is missing, texts %q", want, tt.Texts())
		}
	}
	if tt.HasText(m.VaultBackupRestore()) {
		t.Error("a restore action is offered without the keychain")
	}
	if tt.HasText(m.VaultBackupClear()) {
		t.Error("a clear action is offered with no backups")
	}
	assertNoHorizontalOverflow(t, tt, 720, 480)
}

func TestBackupPanelIsLocalizedInEveryLocale(t *testing.T) {
	for _, locale := range locales.Tags() {
		t.Run(locale, func(t *testing.T) {
			state, _, tt := newBackupView(t, 720, 480)
			m := openBackupPanel(t, state, tt, locale)
			for _, want := range []string{
				m.VaultBackupTitle(), m.VaultBackupRetentionLabel(),
				m.VaultBackupListTitle(3), m.VaultBackupClear(),
			} {
				if !tt.HasText(want) {
					t.Errorf("%q is missing in %s, texts %q", want, locale, tt.Texts())
				}
			}
			assertNoHorizontalOverflow(t, tt, 720, 480)
		})
	}
}
