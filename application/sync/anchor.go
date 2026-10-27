package sync

import (
	"context"
	"fmt"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// AnchorVersion is the merge-anchor format: the last successfully synced
// payload, serialized as compact JSON and sealed with the master password in
// the same envelope as the cloud copy, then stored in cloud-sync.json. Only
// ciphertext is persisted.
const AnchorVersion = 3

// loadBase decrypts the stored merge anchor. Any failure returns nil, which
// makes the merge fall back to the original's set-union (no base) and makes
// the shrink guard fall back to the current cloud copy. A corrupt anchor must
// never block an otherwise valid sync.
func (s *Service) loadBase(ctx context.Context) *Payload {
	config := s.currentConfig()
	if config.AnchorVersion != AnchorVersion || config.SyncBase == "" {
		return nil
	}
	password := s.passwordValue()
	if password == "" {
		return nil
	}
	file, err := domainsync.UnmarshalFile([]byte(config.SyncBase))
	if err != nil {
		return nil
	}
	plaintext, err := domainsync.DecryptPayload(file, password)
	if err != nil {
		return nil
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		return nil
	}
	signature, err := payload.Signature()
	if err != nil {
		return nil
	}
	if config.BaseSignature != "" && config.BaseSignature != signature {
		return nil
	}
	return &payload
}

// encodeAnchor seals a base payload and returns the ciphertext plus its
// plaintext signature.
func (s *Service) encodeAnchor(ctx context.Context, payload Payload) (string, string, error) {
	password := s.passwordValue()
	if password == "" {
		return "", "", ErrPasswordRequired
	}
	config := s.currentConfig()
	file, err := domainsync.EncryptPayload(payload, password, domainsync.EncryptOptions{
		Version:    1,
		UpdatedAt:  s.now().UnixMilli(),
		DeviceID:   config.DeviceID,
		DeviceName: config.DeviceName,
		AppVersion: config.AppVersion,
		Random:     s.random,
		Now:        s.now,
	})
	if err != nil {
		return "", "", fmt.Errorf("sync: encrypt the merge anchor: %w", err)
	}
	content, err := domainsync.MarshalFile(file)
	if err != nil {
		return "", "", fmt.Errorf("sync: encode the merge anchor: %w", err)
	}
	signature, err := payload.Signature()
	if err != nil {
		return "", "", fmt.Errorf("sync: sign the merge anchor: %w", err)
	}
	_ = ctx
	return string(content), signature, nil
}

// prepareOutgoing adds durable tombstones to a payload that is about to be
// uploaded. Tombstones for entities still present are removed; entities that
// disappeared relative to the base get a new tombstone. This is the original's
// withSyncReliabilityMeta, reduced to the fields this build uses.
func (s *Service) prepareOutgoing(payload Payload, base *Payload, remote *Payload, deviceID string) Payload {
	b := Payload{}
	if base != nil {
		b = *base
	}
	r := Payload{}
	if remote != nil {
		r = *remote
	}
	meta := mergeSyncMeta(b, payload, r, payload)
	if meta == nil {
		payload.SyncMeta = nil
		return payload
	}
	now := s.now().UnixMilli()
	meta.GeneratedAt = now
	meta.DeviceID = deviceID
	for index := range meta.Deletions {
		if meta.Deletions[index].DeletedAt == 0 {
			meta.Deletions[index].DeletedAt = now
		}
		if meta.Deletions[index].DeviceID == "" {
			meta.Deletions[index].DeviceID = deviceID
		}
	}
	payload.SyncMeta = meta
	return payload
}

// ensureProtectiveBackup writes a restore point before a destructive apply or
// overwrite. When the port is configured a failure fails closed (the Vault is
// left unchanged); a service embedded without a backup subsystem still syncs,
// while conflict resolution, which the ticket requires to take a backup,
// additionally calls requireProtectiveBackup.
func (s *Service) ensureProtectiveBackup(ctx context.Context) error {
	s.mu.Lock()
	backups := s.backups
	s.mu.Unlock()
	if backups == nil {
		return nil
	}
	if err := backups.CreateProtective(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrBackupUnavailable, err)
	}
	return nil
}

// applyGuard checks the destructive-sync rules and either records the finding
// and blocks, or (for an explicit manual force) records it and continues.
func (s *Service) applyGuard(outgoing Payload, base, remote *Payload, force bool, trigger Trigger) (bool, Result, error) {
	return s.applyGuardFinding(DetectDestructiveSync(outgoing, base, remote), force, trigger)
}

func (s *Service) applyGuardFinding(finding GuardFinding, force bool, trigger Trigger) (bool, Result, error) {
	s.mu.Lock()
	if !finding.Suspicious() {
		s.guard = GuardFinding{}
		s.mu.Unlock()
		return false, Result{}, nil
	}
	s.guard = finding
	s.mu.Unlock()
	if force && trigger == TriggerManual {
		return false, Result{}, nil
	}
	problem := ProblemShrinkGuard
	err := ErrShrinkGuard
	if finding.Kind == GuardTombstone {
		problem = ProblemTombstoneGuard
		err = ErrTombstoneGuard
	}
	s.mu.Lock()
	s.status = StatusFailed
	s.problem = problem
	s.syncing = false
	// A blocked guard must not immediately re-arm the automatic sync: the
	// user has to choose Restore or Force push.
	s.pending = false
	s.mu.Unlock()
	s.changed()
	return true, Result{Trigger: trigger}, err
}
