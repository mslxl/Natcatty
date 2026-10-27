package sync

import (
	"context"
	"errors"
	"fmt"
	"sort"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// Resolution is the user's choice for a surfaced conflict.
type Resolution string

const (
	// ResolutionLocal keeps the local Vault and overwrites the cloud copy.
	ResolutionLocal Resolution = "local"
	// ResolutionRemote keeps the cloud copy and applies it locally.
	ResolutionRemote Resolution = "remote"
	// ResolutionMerge reruns the three-way merge over both sides. It is only
	// offered by a merge strategy.
	ResolutionMerge Resolution = "merge"
)

var (
	// ErrNoGuard reports that RestoreGuard was called with no guard active.
	ErrNoGuard = errors.New("sync: no destructive-sync guard is active")
	// ErrResolutionUnsupported reports a merge resolution for a strategy that
	// does not merge.
	ErrResolutionUnsupported = errors.New("sync: the selected strategy does not merge")
)

// RemoteBackupRetention is how many overwritten cloud copies are kept in the
// Sync Profile. The original had no cloud-copy backup; this build keeps a
// small bounded history of the exact encrypted envelopes so a resolution or
// force push can be undone without growing cloud-sync.json without limit.
const RemoteBackupRetention = 5

// PendingConflict is a durable both-sides change. It is stored in
// cloud-sync.json and survives a restart. Only the already-encrypted cloud
// envelope, entity identifiers, counters, and device metadata are persisted;
// no decrypted credential or entity body enters the profile.
type PendingConflict struct {
	CreatedAt       int64          `json:"createdAt"`
	Trigger         Trigger        `json:"trigger,omitempty"`
	LocalSignature  string         `json:"localSignature,omitempty"`
	RemoteID        string         `json:"remoteId,omitempty"`
	RemoteETag      string         `json:"remoteEtag,omitempty"`
	RemoteUpdatedAt int64          `json:"remoteUpdatedAt"`
	RemoteDevice    string         `json:"remoteDevice,omitempty"`
	RemoteVersion   int            `json:"remoteVersion,omitempty"`
	RemoteContent   []byte         `json:"remoteContent,omitempty"`
	Items           []ConflictItem `json:"items,omitempty"`
	Summary         MergeSummary   `json:"summary,omitempty"`
}

// RemoteBackup is one overwritten cloud copy. Content is the original
// encrypted envelope; this build never stores a decrypted copy.
type RemoteBackup struct {
	ID              string `json:"id"`
	CreatedAt       int64  `json:"createdAt"`
	Reason          string `json:"reason"`
	RemoteID        string `json:"remoteId,omitempty"`
	RemoteETag      string `json:"remoteEtag,omitempty"`
	RemoteUpdatedAt int64  `json:"remoteUpdatedAt,omitempty"`
	Device          string `json:"device,omitempty"`
	Version         int    `json:"version,omitempty"`
	Content         []byte `json:"content,omitempty"`
}

// BackupSource distinguishes a local Vault restore point from an overwritten
// cloud copy.
type BackupSource string

const (
	// BackupSourceLocal is a local Vault backup written by the backup service.
	BackupSourceLocal BackupSource = "local"
	// BackupSourceRemote is a cloud copy this device overwrote and retained.
	BackupSourceRemote BackupSource = "remote"
)

// BackupInfo is one restorable entry for the UI. It carries no secret: a local
// entry names a file managed by the backup service, and a remote entry is
// identified by its retained record.
type BackupInfo struct {
	ID        string
	Source    BackupSource
	CreatedAt int64
	Reason    string
	SizeBytes int64
	Device    string
}

// Backups is the local Vault backup port. Implementations live outside this
// package because the application layer may not import a sibling application
// package.
type Backups interface {
	// CreateProtective writes a restore point of the current Vault.
	CreateProtective(ctx context.Context) error
	// List returns the local restore points, newest first.
	List(ctx context.Context) ([]BackupInfo, error)
	// Restore applies one restore point. Implementations write their own
	// before-restore backup first.
	Restore(ctx context.Context, id string) error
}

// ConflictInfo is the conflict detail the UI renders. Items identify the
// entities and the kind of clash; field values stay inside the encrypted
// envelopes and the local Vault.
type ConflictInfo struct {
	CreatedAt       int64
	RemoteUpdatedAt int64
	RemoteDevice    string
	RemoteVersion   int
	Items           []ConflictItem
	Summary         MergeSummary
	// CanMerge reports that the active strategy permits a merge resolution.
	// A conflict always has both sides, so keeping either one is always
	// available and needs no separate flag.
	CanMerge bool
}

// conflictInfoFor derives the UI state from the persisted profile without
// touching the network or decrypting anything.
func conflictInfoFor(config Config) *ConflictInfo {
	pending := config.PendingConflict
	if pending == nil {
		return nil
	}
	return &ConflictInfo{
		CreatedAt:       pending.CreatedAt,
		RemoteUpdatedAt: pending.RemoteUpdatedAt,
		RemoteDevice:    pending.RemoteDevice,
		RemoteVersion:   pending.RemoteVersion,
		Items:           append([]ConflictItem(nil), pending.Items...),
		Summary:         pending.Summary,
		CanMerge:        domainsync.Merges(config.Strategy),
	}
}

// remoteEnvelopeMeta reads the non-secret envelope metadata the conflict UI
// shows for the other device.
func remoteEnvelopeMeta(content []byte) (string, int) {
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return "", 0
	}
	return envelope.Meta.DeviceName, envelope.Meta.Version
}

// surfaceConflict records a conditional-write loss as a durable conflict. The
// losing cloud copy is kept byte-for-byte so resolution works offline and the
// other device's exact data can be applied or recovered.
func (s *Service) surfaceConflict(
	ctx context.Context,
	trigger Trigger,
	config Config,
	local Payload,
	localSignature string,
	fresh RemoteFile,
	freshPayload Payload,
	freshUpdatedAt int64,
) error {
	base := s.loadBase(ctx)
	mergeResult, mergeErr := MergePayloads(base, local, freshPayload, MergeOptions{
		PreserveDeletions: domainsync.IsPreserveDeletionsStrategy(config.Strategy),
	})
	pending := &PendingConflict{
		CreatedAt:       s.now().UnixMilli(),
		Trigger:         trigger,
		LocalSignature:  localSignature,
		RemoteID:        fresh.ID,
		RemoteETag:      fresh.ETag,
		RemoteUpdatedAt: freshUpdatedAt,
		RemoteContent:   append([]byte(nil), fresh.Content...),
	}
	pending.RemoteDevice, pending.RemoteVersion = remoteEnvelopeMeta(fresh.Content)
	if mergeErr == nil {
		pending.Items = append([]ConflictItem(nil), mergeResult.Conflicts...)
		pending.Summary = mergeResult.Summary
	}

	s.mu.Lock()
	config = s.config
	config.PendingConflict = pending
	s.config = config
	s.status = StatusConflict
	s.problem = ProblemConflict
	s.syncing = false
	// A pending conflict must not keep re-arming automatic sync.
	s.pending = false
	s.mu.Unlock()
	if err := s.configs.Save(context.Background(), config); err != nil {
		return fmt.Errorf("%w: saving the conflict: %v", ErrConflict, err)
	}
	s.changed()
	return fmt.Errorf("%w: the cloud copy changed on another device", ErrConflict)
}

// retainRemoteBackup stores an overwritten cloud copy. The envelope is already
// encrypted, so persisting it does not expose a plaintext secret. A save
// failure is returned: the caller must not overwrite the only recoverable copy
// without a restore point.
func (s *Service) retainRemoteBackup(ctx context.Context, remote RemoteFile, reason string) error {
	if remote.ID == "" || len(remote.Content) == 0 {
		return nil
	}
	device, version := remoteEnvelopeMeta(remote.Content)
	now := s.now().UnixMilli()
	entry := RemoteBackup{
		ID:              fmt.Sprintf("remote-%d-%d", now, version),
		CreatedAt:       now,
		Reason:          reason,
		RemoteID:        remote.ID,
		RemoteETag:      remote.ETag,
		RemoteUpdatedAt: remote.UpdatedAt,
		Device:          device,
		Version:         version,
		Content:         append([]byte(nil), remote.Content...),
	}
	s.mu.Lock()
	config := s.config
	for _, existing := range config.RemoteBackups {
		if existing.RemoteID == entry.RemoteID && existing.RemoteETag == entry.RemoteETag &&
			len(existing.Content) == len(entry.Content) {
			s.mu.Unlock()
			return nil
		}
	}
	config.RemoteBackups = append([]RemoteBackup{entry}, config.RemoteBackups...)
	if len(config.RemoteBackups) > RemoteBackupRetention {
		config.RemoteBackups = config.RemoteBackups[:RemoteBackupRetention]
	}
	s.config = config
	s.mu.Unlock()
	if err := s.configs.Save(context.Background(), config); err != nil {
		return fmt.Errorf("%w: saving the overwritten cloud copy: %v", ErrBackupUnavailable, err)
	}
	_ = ctx
	return nil
}

// ResolveConflict applies the user's choice for the pending conflict. It is
// idempotent: resolving an already resolved conflict succeeds without doing
// anything, so a double click or a retry after a restart cannot apply a stale
// choice twice.
func (s *Service) ResolveConflict(ctx context.Context, resolution Resolution) (Result, error) {
	if !s.syncMu.TryLock() {
		return Result{Trigger: TriggerManual}, ErrAlreadyInProgress
	}
	defer s.syncMu.Unlock()
	return s.resolveLocked(ctx, resolution, false)
}

func (s *Service) resolveLocked(ctx context.Context, resolution Resolution, force bool) (Result, error) {
	config := s.currentConfig()
	pending := config.PendingConflict
	if pending == nil {
		// Already resolved, or resolved by another window. The caller's intent
		// is satisfied; applying a stale choice would be wrong.
		return Result{Trigger: TriggerManual}, nil
	}
	if resolution == ResolutionMerge && !domainsync.Merges(config.Strategy) {
		s.fail(ProblemConflict)
		return Result{Trigger: TriggerManual}, ErrResolutionUnsupported
	}
	if s.passwordValue() == "" {
		s.fail(ProblemPasswordRequired)
		return Result{Trigger: TriggerManual}, ErrPasswordRequired
	}

	remoteFile := RemoteFile{
		ID:        pending.RemoteID,
		ETag:      pending.RemoteETag,
		UpdatedAt: pending.RemoteUpdatedAt,
		Content:   append([]byte(nil), pending.RemoteContent...),
	}
	remotePayload, _, problem, err := s.decodeRemote(remoteFile)
	if err != nil {
		s.fail(problem)
		return Result{Trigger: TriggerManual}, err
	}
	token, err := s.ensureFreshToken(ctx)
	if err != nil {
		s.fail(problemFor(err))
		return Result{Trigger: TriggerManual}, err
	}
	local, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.fail(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	localSignature, err := local.Signature()
	if err != nil {
		s.fail(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	base := s.loadBase(ctx)

	// Both resolutions overwrite one side. A restore point is mandatory.
	if err := s.requireProtectiveBackup(ctx); err != nil {
		s.fail(ProblemBackup)
		return Result{Trigger: TriggerManual}, err
	}

	s.mu.Lock()
	s.status = StatusSyncing
	s.syncing = true
	s.problem = ProblemNone
	s.guard = GuardFinding{}
	s.mu.Unlock()
	s.changed()

	switch resolution {
	case ResolutionRemote:
		if err := s.vault.Apply(ctx, remotePayload); err != nil {
			s.fail(ProblemLocal)
			return Result{Trigger: TriggerManual}, err
		}
		s.notifySnapshotApplied()
		applied, err := s.vault.Snapshot(ctx)
		if err != nil {
			s.fail(ProblemLocal)
			return Result{Trigger: TriggerManual}, err
		}
		appliedSignature, err := applied.Signature()
		if err != nil {
			s.fail(ProblemLocal)
			return Result{Trigger: TriggerManual}, err
		}
		if err := s.recordSync(ctx, remoteFile, appliedSignature, pending.RemoteUpdatedAt, &applied); err != nil {
			s.fail(ProblemLocal)
			return Result{Trigger: TriggerManual}, err
		}
		s.completeSync(domainsync.ActionDownload)
		return Result{Trigger: TriggerManual, Action: domainsync.ActionDownload, Downloaded: true}, nil

	case ResolutionLocal, ResolutionMerge:
		outgoing := local
		if resolution == ResolutionMerge {
			merged, mergeErr := MergePayloads(base, local, remotePayload, MergeOptions{
				PreserveDeletions: domainsync.IsPreserveDeletionsStrategy(config.Strategy),
			})
			if mergeErr != nil {
				s.fail(ProblemLocal)
				return Result{Trigger: TriggerManual}, mergeErr
			}
			outgoing = merged.Payload
		}
		outgoing = s.prepareOutgoing(outgoing, base, &remotePayload, config.DeviceID)
		if blocked, result, guardErr := s.applyGuard(outgoing, base, &remotePayload, force, TriggerManual); blocked {
			return result, guardErr
		}
		if resolution == ResolutionMerge {
			if err := s.vault.Apply(ctx, outgoing); err != nil {
				s.fail(ProblemLocal)
				return Result{Trigger: TriggerManual}, err
			}
			s.notifySnapshotApplied()
			applied, err := s.vault.Snapshot(ctx)
			if err != nil {
				s.fail(ProblemLocal)
				return Result{Trigger: TriggerManual}, err
			}
			local, localSignature = applied, ""
			localSignature, err = applied.Signature()
			if err != nil {
				s.fail(ProblemLocal)
				return Result{Trigger: TriggerManual}, err
			}
		}
		if _, err := s.upload(ctx, TriggerManual, force, token, outgoing, local, localSignature,
			base, &remotePayload, remoteFile, config, "conflict"); err != nil {
			return Result{Trigger: TriggerManual}, err
		}
		action := domainsync.ActionUpload
		downloaded := false
		if resolution == ResolutionMerge {
			action = domainsync.ActionMerge
			downloaded = true
		}
		s.completeSync(action)
		return Result{Trigger: TriggerManual, Action: action, Uploaded: true, Downloaded: downloaded}, nil
	}
	s.fail(ProblemConflict)
	return Result{Trigger: TriggerManual}, ErrConflict
}

// requireProtectiveBackup always writes a restore point and fails closed when
// the backup subsystem is unavailable. Conflict resolution must never
// overwrite a side without a backup.
func (s *Service) requireProtectiveBackup(ctx context.Context) error {
	s.mu.Lock()
	backups := s.backups
	s.mu.Unlock()
	if backups == nil {
		return ErrBackupUnavailable
	}
	if err := backups.CreateProtective(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrBackupUnavailable, err)
	}
	return nil
}

// RestoreGuard restores the last good copy after a shrink or tombstone guard
// blocked an upload. It prefers the current cloud copy; when the cloud is
// empty it falls back to the encrypted merge anchor. It never pushes anything.
func (s *Service) RestoreGuard(ctx context.Context) (Result, error) {
	if !s.syncMu.TryLock() {
		return Result{Trigger: TriggerManual}, ErrAlreadyInProgress
	}
	defer s.syncMu.Unlock()

	s.mu.Lock()
	finding := s.guard
	s.mu.Unlock()
	if !finding.Suspicious() {
		return Result{Trigger: TriggerManual}, ErrNoGuard
	}
	if s.passwordValue() == "" {
		s.fail(ProblemPasswordRequired)
		return Result{Trigger: TriggerManual}, ErrPasswordRequired
	}
	config := s.currentConfig()
	if !config.Connected {
		s.fail(ProblemNotConfigured)
		return Result{Trigger: TriggerManual}, ErrNotConfigured
	}
	token, err := s.ensureFreshToken(ctx)
	if err != nil {
		s.fail(problemFor(err))
		return Result{Trigger: TriggerManual}, err
	}
	remoteFile, exists, token, err := s.readRemote(ctx, s.activeRemote(), token)
	if err != nil {
		s.fail(problemFor(err))
		return Result{Trigger: TriggerManual}, err
	}
	var restore Payload
	haveRemote := false
	if exists {
		payload, updatedAt, problem, decodeErr := s.decodeRemote(remoteFile)
		if decodeErr != nil {
			s.fail(problem)
			return Result{Trigger: TriggerManual}, decodeErr
		}
		if payload.HasData() {
			restore, remoteFile.UpdatedAt, haveRemote = payload, updatedAt, true
		}
	}
	if !haveRemote {
		base := s.loadBase(ctx)
		if base == nil || !base.HasData() {
			s.fail(ProblemEmptyVault)
			return Result{Trigger: TriggerManual}, ErrEmptyVault
		}
		restore = *base
		remoteFile = RemoteFile{}
	}

	if err := s.requireProtectiveBackup(ctx); err != nil {
		s.fail(ProblemBackup)
		return Result{Trigger: TriggerManual}, err
	}
	if err := s.vault.Apply(ctx, restore); err != nil {
		s.fail(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	s.notifySnapshotApplied()
	applied, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.fail(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	signature, err := applied.Signature()
	if err != nil {
		s.fail(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	if err := s.recordSync(ctx, remoteFile, signature, remoteFile.UpdatedAt, &applied); err != nil {
		s.fail(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	action := domainsync.ActionDownload
	downloaded := true
	if !haveRemote {
		action = domainsync.ActionNone
		downloaded = false
	}
	s.mu.Lock()
	s.guard = GuardFinding{}
	s.mu.Unlock()
	s.completeSync(action)
	return Result{Trigger: TriggerManual, Action: action, Downloaded: downloaded}, nil
}

// LoadBackups refreshes the restore-point list shown in settings. Local
// entries come from the backup service; remote entries are overwritten cloud
// copies retained in this profile. The list never contains plaintext secrets.
func (s *Service) LoadBackups(ctx context.Context) error {
	s.mu.Lock()
	backups := s.backups
	config := s.config
	s.mu.Unlock()

	var infos []BackupInfo
	problem := ProblemNone
	if backups != nil {
		local, err := backups.List(ctx)
		if err != nil {
			problem = ProblemLocal
		} else {
			for _, entry := range local {
				entry.Source = BackupSourceLocal
				infos = append(infos, entry)
			}
		}
	}
	for _, entry := range config.RemoteBackups {
		infos = append(infos, BackupInfo{
			ID:        entry.ID,
			Source:    BackupSourceRemote,
			CreatedAt: entry.CreatedAt,
			Reason:    entry.Reason,
			SizeBytes: int64(len(entry.Content)),
			Device:    entry.Device,
		})
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].CreatedAt > infos[j].CreatedAt })

	s.mu.Lock()
	s.backupsList = infos
	s.backupsProblem = problem
	s.mu.Unlock()
	s.changed()
	return nil
}

// RestoreBackup restores one listed restore point locally. A local backup is
// applied by the backup service (which writes its own before-restore backup);
// a retained cloud copy is decrypted and applied after a protective backup.
// Neither path writes to the provider: after restoring an older cloud copy the
// next sync treats the local Vault as changed, so the ordinary guards still
// apply before anything leaves the device.
func (s *Service) RestoreBackup(ctx context.Context, source BackupSource, id string) error {
	if !s.syncMu.TryLock() {
		return ErrAlreadyInProgress
	}
	defer s.syncMu.Unlock()

	switch source {
	case BackupSourceLocal:
		s.mu.Lock()
		backups := s.backups
		s.mu.Unlock()
		if backups == nil {
			return ErrBackupUnavailable
		}
		if err := backups.Restore(ctx, id); err != nil {
			return err
		}
		s.notifySnapshotApplied()
		s.mu.Lock()
		config := s.config
		// The restored Vault is deliberately treated as a local change. The
		// remote baseline stays so a concurrent cloud edit is still detected.
		config.LocalSignature = ""
		s.config = config
		s.guard = GuardFinding{}
		s.mu.Unlock()
		if err := s.configs.Save(ctx, config); err != nil {
			return err
		}
		s.changed()
		return nil

	case BackupSourceRemote:
		password := s.passwordValue()
		if password == "" {
			return ErrPasswordRequired
		}
		s.mu.Lock()
		config := s.config
		s.mu.Unlock()
		var retained *RemoteBackup
		for index := range config.RemoteBackups {
			if config.RemoteBackups[index].ID == id {
				retained = &config.RemoteBackups[index]
				break
			}
		}
		if retained == nil {
			return fmt.Errorf("%w: no retained cloud copy %q", ErrBackupUnavailable, id)
		}
		file := RemoteFile{
			ID:        retained.RemoteID,
			ETag:      retained.RemoteETag,
			UpdatedAt: retained.RemoteUpdatedAt,
			Content:   append([]byte(nil), retained.Content...),
		}
		payload, _, problem, err := s.decodeRemote(file)
		if err != nil {
			s.fail(problem)
			return err
		}
		if err := s.ensureProtectiveBackup(ctx); err != nil {
			s.fail(ProblemBackup)
			return err
		}
		if err := s.vault.Apply(ctx, payload); err != nil {
			s.fail(ProblemLocal)
			return err
		}
		s.notifySnapshotApplied()
		s.mu.Lock()
		config = s.config
		config.LocalSignature = ""
		s.config = config
		s.guard = GuardFinding{}
		s.mu.Unlock()
		if err := s.configs.Save(ctx, config); err != nil {
			return err
		}
		s.changed()
		return nil
	default:
		return fmt.Errorf("sync: unknown backup source %q", source)
	}
}
