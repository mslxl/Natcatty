package state

import (
	"context"
	"errors"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

type fakeBackups struct {
	items        []domainbackup.Summary
	retention    int
	encryption   bool
	status       appbackup.Status
	listErr      error
	restoreErr   error
	clearErr     error
	setErr       error
	clearCount   int
	setCalls     []int
	restoreCalls []string
	clearCalls   int
	onRestore    func()
}

func (f *fakeBackups) List(context.Context) ([]domainbackup.Summary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domainbackup.Summary(nil), f.items...), nil
}

func (f *fakeBackups) Restore(_ context.Context, id string) error {
	f.restoreCalls = append(f.restoreCalls, id)
	if f.restoreErr != nil {
		return f.restoreErr
	}
	if f.onRestore != nil {
		f.onRestore()
	}
	f.items = f.items[:0]
	f.status = appbackup.Status{Kind: appbackup.StatusRestored}
	return nil
}

func (f *fakeBackups) Clear(context.Context) (int, error) {
	f.clearCalls++
	if f.clearErr != nil {
		return 0, f.clearErr
	}
	count := len(f.items)
	f.items = nil
	f.status = appbackup.Status{Kind: appbackup.StatusCleared, Pruned: count}
	return count, nil
}

func (f *fakeBackups) Retention() int { return f.retention }

func (f *fakeBackups) SetRetention(_ context.Context, value int) (int, int, error) {
	f.setCalls = append(f.setCalls, value)
	if f.setErr != nil {
		return f.retention, 0, f.setErr
	}
	applied := domainbackup.SanitizeRetention(value)
	f.retention = applied
	pruned := 0
	if len(f.items) > applied {
		pruned = len(f.items) - applied
		f.items = f.items[:applied]
	}
	f.status = appbackup.Status{Kind: appbackup.StatusCreated, Pruned: pruned}
	return applied, pruned, nil
}

func (f *fakeBackups) EncryptionAvailable() bool    { return f.encryption }
func (f *fakeBackups) LastStatus() appbackup.Status { return f.status }

func backupSummary(id string, createdAt int64) domainbackup.Summary {
	return domainbackup.Summary{
		ID: id, CreatedAt: createdAt, Reason: domainbackup.ReasonVaultChange,
		Fingerprint: "0123456789abcdef", SizeBytes: 2048,
		Counts: domainbackup.Counts{Hosts: 2},
	}
}

func backupState(t *testing.T, backups *fakeBackups) (*State, *fakeHosts) {
	t.Helper()
	hosts := &fakeHosts{items: []domainvault.Host{{ID: model.HostID("before"), Label: "before"}}}
	state := NewState(hosts, WithBackups(backups), WithRedrawRequest(func() {}))
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	return state, hosts
}

func TestBeginBackupsLoadsHistoryRetentionAndNotice(t *testing.T) {
	backups := &fakeBackups{
		items:      []domainbackup.Summary{backupSummary("b1", 1_700_000_000_000)},
		retention:  7,
		encryption: true,
		status:     appbackup.Status{Kind: appbackup.StatusSkippedTooLarge},
	}
	state, _ := backupState(t, backups)

	state.BeginBackups()
	if !state.BackupsOpen() {
		t.Fatal("BeginBackups did not open the panel")
	}
	if len(state.BackupItems()) != 1 || state.BackupItems()[0].ID != "b1" {
		t.Fatalf("items = %+v", state.BackupItems())
	}
	if state.BackupRetention() != 7 || *state.BackupRetentionInput() != "7" {
		t.Fatalf("retention = %d input = %q", state.BackupRetention(), *state.BackupRetentionInput())
	}
	if state.BackupNotice().Kind != appbackup.StatusSkippedTooLarge {
		t.Fatalf("notice = %+v", state.BackupNotice())
	}
	if !state.BackupEncryptionAvailable() {
		t.Fatal("encryption availability was not read")
	}
	if state.BackupProblem() != ProblemNone {
		t.Fatalf("problem = %v", state.BackupProblem())
	}

	state.CancelBackups()
	if state.BackupsOpen() {
		t.Fatal("CancelBackups kept the panel open")
	}
}

func TestBackupPanelWithoutASeamReportsUnavailable(t *testing.T) {
	state := NewState(&fakeHosts{}, WithRedrawRequest(func() {}))
	state.BeginBackups()
	if state.BackupsOpen() {
		t.Fatal("the panel opened without a backup seam")
	}
}

func TestBackupRetentionValidation(t *testing.T) {
	backups := &fakeBackups{
		items:     []domainbackup.Summary{backupSummary("b1", 1), backupSummary("b2", 2), backupSummary("b3", 3)},
		retention: 20, encryption: true,
	}
	state, _ := backupState(t, backups)
	state.BeginBackups()

	for _, invalid := range []string{"", "abc", "0", "-1", "101"} {
		*state.BackupRetentionInput() = invalid
		if state.SaveBackupRetention() {
			t.Errorf("SaveBackupRetention accepted %q", invalid)
		}
		if state.BackupProblem() != ProblemBackupRetentionInvalid {
			t.Errorf("%q: problem = %v", invalid, state.BackupProblem())
		}
	}
	if len(backups.setCalls) != 0 {
		t.Fatalf("invalid values reached the service: %v", backups.setCalls)
	}

	*state.BackupRetentionInput() = "1"
	if !state.SaveBackupRetention() {
		t.Fatalf("valid retention failed: %v", state.BackupProblem())
	}
	if len(backups.setCalls) != 1 || backups.setCalls[0] != 1 {
		t.Fatalf("set calls = %v", backups.setCalls)
	}
	if state.BackupRetention() != 1 || len(state.BackupItems()) != 1 {
		t.Fatalf("retention = %d items = %d", state.BackupRetention(), len(state.BackupItems()))
	}
	if state.BackupNotice().Pruned != 2 {
		t.Fatalf("notice = %+v, want two pruned", state.BackupNotice())
	}
}

func TestBackupRetentionPersistenceFailure(t *testing.T) {
	backups := &fakeBackups{retention: 20, encryption: true, setErr: errors.New("read-only")}
	state, _ := backupState(t, backups)
	state.BeginBackups()
	*state.BackupRetentionInput() = "10"
	if state.SaveBackupRetention() {
		t.Fatal("SaveBackupRetention hid the persistence failure")
	}
	if state.BackupProblem() != ProblemBackupRetention {
		t.Fatalf("problem = %v", state.BackupProblem())
	}
}

func TestRestoreConfirmationReloadsTheVault(t *testing.T) {
	backups := &fakeBackups{
		items:     []domainbackup.Summary{backupSummary("b1", 1_700_000_000_000)},
		retention: 20, encryption: true,
	}
	state, hosts := backupState(t, backups)
	state.BeginBackups()
	after := 0
	state.backupAfterRestore = func() { after++ }
	backups.onRestore = func() {
		hosts.items = []domainvault.Host{{ID: model.HostID("restored"), Label: "restored"}}
	}

	state.RequestRestoreBackup("b1")
	if state.BackupPendingRestore() != "b1" {
		t.Fatalf("pending = %q", state.BackupPendingRestore())
	}
	state.CancelRestoreBackup()
	if state.BackupPendingRestore() != "" {
		t.Fatal("CancelRestoreBackup kept the confirmation")
	}

	state.RequestRestoreBackup("b1")
	if !state.ConfirmRestoreBackup() {
		t.Fatalf("ConfirmRestoreBackup failed: %v", state.BackupProblem())
	}
	if len(backups.restoreCalls) != 1 || backups.restoreCalls[0] != "b1" {
		t.Fatalf("restore calls = %v", backups.restoreCalls)
	}
	if state.HostCount() != 1 || state.Items()[0].ID != "restored" {
		t.Fatalf("the Vault was not reloaded after restore: %+v", state.Items())
	}
	if after != 1 {
		t.Fatalf("after-restore callback ran %d times", after)
	}
	if state.BackupNotice().Kind != appbackup.StatusRestored {
		t.Fatalf("notice = %+v", state.BackupNotice())
	}
}

func TestFailedRestoreMapsTheCauseAndKeepsTheVault(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Problem
	}{
		{"protective", &appbackup.ProtectiveBackupError{Cause: errors.New("disk")}, ProblemBackupProtective},
		{"corrupt", appbackup.ErrCorrupt, ProblemBackupCorrupt},
		{"incompatible", appbackup.ErrIncompatibleSnapshot, ProblemBackupCorrupt},
		{"unavailable", appbackup.ErrEncryptionUnavailable, ProblemBackupUnavailable},
		{"restore", errors.New("constraint"), ProblemBackupRestore},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			backups := &fakeBackups{
				items:     []domainbackup.Summary{backupSummary("b1", 1)},
				retention: 20, encryption: true, restoreErr: testCase.err,
			}
			state, _ := backupState(t, backups)
			state.BeginBackups()
			state.RequestRestoreBackup("b1")
			if state.ConfirmRestoreBackup() {
				t.Fatal("ConfirmRestoreBackup reported success")
			}
			if state.BackupProblem() != testCase.want {
				t.Fatalf("problem = %v, want %v", state.BackupProblem(), testCase.want)
			}
			if state.HostCount() != 1 || state.Items()[0].ID != "before" {
				t.Fatalf("a failed restore changed the Vault: %+v", state.Items())
			}
			if state.BackupPendingRestore() != "" {
				t.Fatal("the confirmation remained after a failure")
			}
		})
	}
}

func TestClearBackupsRequiresConfirmation(t *testing.T) {
	backups := &fakeBackups{
		items:     []domainbackup.Summary{backupSummary("b1", 1), backupSummary("b2", 2)},
		retention: 20, encryption: true,
	}
	state, _ := backupState(t, backups)
	state.BeginBackups()

	state.RequestClearBackups()
	if !state.BackupPendingClear() {
		t.Fatal("RequestClearBackups did not ask for confirmation")
	}
	if backups.clearCalls != 0 {
		t.Fatal("clear ran without confirmation")
	}
	state.CancelClearBackups()
	if state.BackupPendingClear() || backups.clearCalls != 0 {
		t.Fatal("CancelClearBackups did not cancel")
	}

	state.RequestClearBackups()
	if !state.ConfirmClearBackups() {
		t.Fatalf("ConfirmClearBackups failed: %v", state.BackupProblem())
	}
	if backups.clearCalls != 1 || len(state.BackupItems()) != 0 {
		t.Fatalf("clear calls = %d items = %d", backups.clearCalls, len(state.BackupItems()))
	}
	if state.BackupNotice().Kind != appbackup.StatusCleared || state.BackupNotice().Pruned != 2 {
		t.Fatalf("notice = %+v", state.BackupNotice())
	}
}

func TestClearBackupFailureIsReported(t *testing.T) {
	backups := &fakeBackups{
		items:     []domainbackup.Summary{backupSummary("b1", 1)},
		retention: 20, encryption: true, clearErr: errors.New("locked"),
	}
	state, _ := backupState(t, backups)
	state.BeginBackups()
	state.RequestClearBackups()
	if state.ConfirmClearBackups() {
		t.Fatal("clear reported success")
	}
	if state.BackupProblem() != ProblemBackupClear {
		t.Fatalf("problem = %v", state.BackupProblem())
	}
	if len(state.BackupItems()) != 1 {
		t.Fatal("the list changed after a failed clear")
	}
}

func TestBackupListFailureIsReported(t *testing.T) {
	backups := &fakeBackups{retention: 20, encryption: true, listErr: errors.New("io")}
	state, _ := backupState(t, backups)
	state.BeginBackups()
	if state.BackupProblem() != ProblemBackupList {
		t.Fatalf("problem = %v", state.BackupProblem())
	}
}

// The Vault service takes the protective backup before the first chunk and
// reports a typed ApplyBarrierError. The state must surface that as the
// protective-backup problem and keep the panel open; dropping the mapping
// makes this test fail.
func TestApplyBarrierFailureIsReportedAtThePanel(t *testing.T) {
	imports := &fakeImports{
		preview: importPreview(10),
		failError: &appvault.ApplyBarrierError{
			Cause: errors.New("protective backup failed"),
		},
	}
	state := NewState(&fakeHosts{},
		WithImports(imports),
		WithImportPicker(fakeImportPicker{name: "hosts.csv", content: "Hostname\nhost.example.test\n"}),
		WithRedrawRequest(func() {}),
	)
	state.BeginImport()
	if !state.ChooseImportFile() {
		t.Fatal("ChooseImportFile failed")
	}
	err := state.ConfirmImport()
	if err == nil {
		t.Fatal("ConfirmImport hid the barrier failure")
	}
	var barrier *appvault.ApplyBarrierError
	if !errors.As(err, &barrier) {
		t.Fatalf("error = %v, want ApplyBarrierError", err)
	}
	if len(imports.commits) != 1 {
		t.Fatalf("the commit was not attempted: %+v", imports.commits)
	}
	if !state.ImportOpen() {
		t.Fatal("the import panel closed after a barrier failure")
	}
	if state.ImportProblem() != ProblemBackupProtective {
		t.Fatalf("problem = %v, want ProblemBackupProtective", state.ImportProblem())
	}
}
