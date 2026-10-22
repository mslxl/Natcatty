package state

import (
	"context"
	"errors"
	"strconv"
	"strings"

	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

// Backups is the local-backup use-case seam. The application service
// implements it; tests provide a fake.
type Backups interface {
	List(ctx context.Context) ([]domainbackup.Summary, error)
	Restore(ctx context.Context, id string) error
	Clear(ctx context.Context) (int, error)
	Retention() int
	SetRetention(ctx context.Context, value int) (int, int, error)
	EncryptionAvailable() bool
	LastStatus() appbackup.Status
}

// BackupNotice is the last backup outcome the panel reports. It is the
// application's status, not a UI copy, so a new outcome cannot drift between
// the service and the banner.
type BackupNotice = appbackup.Status

// WithBackups gives the Vault surface its local-backup seam.
func WithBackups(backups Backups) Option {
	return func(s *State) { s.backups = backups }
}

// WithBackupAfterRestore sets the callback run after a successful restore so
// surfaces outside this State (Port Forwarding Rules) reload as well.
func WithBackupAfterRestore(fn func()) Option {
	return func(s *State) { s.backupAfterRestore = fn }
}

// BackupsOpen reports whether the backup panel is showing.
func (s *State) BackupsOpen() bool { return s.backupsOpen }

// BeginBackups opens the backup panel and loads its history.
func (s *State) BeginBackups() {
	if s.backups == nil {
		s.backupProblem = ProblemBackupUnavailable
		return
	}
	s.backupsOpen = true
	s.backupPendingID = ""
	s.backupPendingClear = false
	s.backupProblem = ProblemNone
	s.refreshBackups()
	s.redraw()
}

// CancelBackups returns to the Vault list.
func (s *State) CancelBackups() {
	s.backupsOpen = false
	s.backupPendingID = ""
	s.backupPendingClear = false
	s.backupProblem = ProblemNone
	s.redraw()
}

// BackupItems returns the listed backups, newest first.
func (s *State) BackupItems() []domainbackup.Summary { return s.backupItems }

// BackupRetention reports the persisted limit.
func (s *State) BackupRetention() int { return s.backupRetention }

// BackupRetentionInput is the editable retention field.
func (s *State) BackupRetentionInput() *string { return &s.backupInput }

// BackupNotice reports pruned and skipped outcomes.
func (s *State) BackupNotice() BackupNotice { return s.backupNotice }

// BackupProblem reports the panel's failure code.
func (s *State) BackupProblem() Problem { return s.backupProblem }

// BackupEncryptionAvailable reports whether the panel may offer restores.
func (s *State) BackupEncryptionAvailable() bool {
	return s.backups != nil && s.backups.EncryptionAvailable()
}

// BackupPendingRestore is the backup awaiting confirmation.
func (s *State) BackupPendingRestore() string { return s.backupPendingID }

// BackupPendingClear reports whether the clear action awaits confirmation.
func (s *State) BackupPendingClear() bool { return s.backupPendingClear }

// RefreshBackups reloads the history and the active retention.
func (s *State) RefreshBackups() {
	s.backupProblem = ProblemNone
	s.refreshBackups()
	s.redraw()
}

func (s *State) refreshBackups() {
	if s.backups == nil {
		s.backupProblem = ProblemBackupUnavailable
		return
	}
	items, err := s.backups.List(context.Background())
	if err != nil {
		s.backupProblem = ProblemBackupList
		return
	}
	s.backupItems = items
	s.backupRetention = s.backups.Retention()
	s.backupInput = strconv.Itoa(s.backupRetention)
	s.backupNotice = s.backups.LastStatus()
}

// SaveBackupRetention validates and persists the retention field, then prunes
// immediately.
func (s *State) SaveBackupRetention() bool {
	if s.backups == nil {
		s.backupProblem = ProblemBackupUnavailable
		s.redraw()
		return false
	}
	value := strings.TrimSpace(s.backupInput)
	parsed, err := strconv.Atoi(value)
	if err != nil || value == "" ||
		parsed < domainbackup.MinRetention || parsed > domainbackup.MaxRetention {
		s.backupProblem = ProblemBackupRetentionInvalid
		s.redraw()
		return false
	}
	applied, _, err := s.backups.SetRetention(context.Background(), parsed)
	if err != nil {
		s.backupProblem = ProblemBackupRetention
		s.redraw()
		return false
	}
	s.backupRetention = applied
	s.backupInput = strconv.Itoa(applied)
	s.backupNotice = s.backups.LastStatus()
	s.backupProblem = ProblemNone
	s.refreshBackups()
	s.redraw()
	return true
}

// RequestRestoreBackup selects a backup and shows the confirmation banner.
func (s *State) RequestRestoreBackup(id string) {
	s.backupPendingID = id
	s.backupPendingClear = false
	s.redraw()
}

// CancelRestoreBackup dismisses the restore confirmation.
func (s *State) CancelRestoreBackup() {
	s.backupPendingID = ""
	s.redraw()
}

// ConfirmRestoreBackup performs the confirmed restore. The service takes the
// protective backup and the state reloads every family afterwards.
func (s *State) ConfirmRestoreBackup() bool {
	if s.backups == nil || s.backupPendingID == "" {
		return false
	}
	id := s.backupPendingID
	s.backupPendingID = ""
	if err := s.backups.Restore(context.Background(), id); err != nil {
		s.backupProblem = backupProblemFor(err)
		s.backupNotice = s.backups.LastStatus()
		s.redraw()
		return false
	}
	// The restore rewrote every Vault family in one transaction; reload the
	// lists so the surface shows the restored state, and let the composition
	// root refresh surfaces this State does not own.
	if err := s.load(); err != nil {
		s.backupProblem = ProblemInternal
		s.redraw()
		return false
	}
	if s.backupAfterRestore != nil {
		s.backupAfterRestore()
	}
	s.backupProblem = ProblemNone
	s.backupNotice = s.backups.LastStatus()
	s.refreshBackups()
	s.redraw()
	return true
}

// RequestClearBackups asks for confirmation before deleting every backup.
func (s *State) RequestClearBackups() {
	s.backupPendingClear = true
	s.backupPendingID = ""
	s.redraw()
}

// CancelClearBackups dismisses the clear confirmation.
func (s *State) CancelClearBackups() {
	s.backupPendingClear = false
	s.redraw()
}

// ConfirmClearBackups deletes every backup. The Vault itself is untouched.
func (s *State) ConfirmClearBackups() bool {
	if s.backups == nil {
		s.backupProblem = ProblemBackupUnavailable
		s.redraw()
		return false
	}
	s.backupPendingClear = false
	_, err := s.backups.Clear(context.Background())
	if err != nil {
		s.backupProblem = ProblemBackupClear
		s.redraw()
		return false
	}
	s.backupProblem = ProblemNone
	s.backupNotice = s.backups.LastStatus()
	s.refreshBackups()
	s.redraw()
	return true
}

func backupProblemFor(err error) Problem {
	switch {
	case errors.Is(err, appbackup.ErrEncryptionUnavailable):
		return ProblemBackupUnavailable
	case errors.Is(err, appbackup.ErrCorrupt), errors.Is(err, appbackup.ErrIncompatibleSnapshot):
		return ProblemBackupCorrupt
	}
	var protective *appbackup.ProtectiveBackupError
	if errors.As(err, &protective) {
		return ProblemBackupProtective
	}
	return ProblemBackupRestore
}
