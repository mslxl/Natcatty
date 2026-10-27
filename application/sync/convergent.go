package sync

// Convergent sync v2 at the application layer.
//
// The domain CRDT (domain/sync/convergent.go) owns dots, registers, clocks,
// merge laws, canonical state, manifests, entity hashes, and divergence
// classification. This file owns migration, provider I/O, the three
// verification rounds, local persistence, and the UI state machine.
//
// Exact verification semantics implemented here (recorded in ticket 55):
//
//   Round 1 "manifest": the provider's global and per-entity version vectors
//   must dominate the expected manifest.
//   Round 2 "entities": round 1 plus every expected entity's canonical hash
//   must equal the provider's hash for that entity.
//   Round 3 "state": the provider's canonical full-state hash must equal the
//   expected full-state hash.
//
// Every round first downloads and joins any state that appeared, uploads the
// expected state only when the provider does not already satisfy the round,
// reads back, and joins a verified remote superset before trying the next,
// stronger round. Three rounds are attempted at most; exhaustion is a typed
// divergence and never an overwrite.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

const (
	// ConvergentVerifyRounds is the protocol's fixed verification budget.
	ConvergentVerifyRounds = 3
	// ConvergentRoundManifest, ConvergentRoundEntities, and
	// ConvergentRoundState name the progressively stronger comparisons.
	ConvergentRoundManifest = "manifest"
	ConvergentRoundEntities = "entities"
	ConvergentRoundState    = "state"
)

// ConvergentConfig is the non-secret profile's encrypted v2 records. Every
// blob is sealed with the master password; no CRDT state or Vault data is
// stored in plaintext.
type ConvergentConfig struct {
	// SchemaVersion is the explicit v2 migration marker. It is absent before
	// migration and must be 2 once set; any other value fails closed.
	SchemaVersion int `json:"schemaVersion,omitempty"`
	// Enabled is device-local. Disabling pauses v2 without deleting the
	// replica or the provider's metadata, and never falls back to v1.
	Enabled bool `json:"enabled,omitempty"`
	// Replica is the encrypted canonical ConvergentEnvelope.
	Replica          string `json:"replica,omitempty"`
	ReplicaSignature string `json:"replicaSignature,omitempty"`
	// VerifiedPayload is the encrypted materialized payload last verified on
	// the provider. It is the trusted legacy baseline and the safe restore
	// point; values are never shown in the migration/divergence UI.
	VerifiedPayload   string `json:"verifiedPayload,omitempty"`
	VerifiedSignature string `json:"verifiedSignature,omitempty"`
	LastVerifiedAt    int64  `json:"lastVerifiedAt,omitempty"`
	// PendingApply records that the provider was verified but the local Vault
	// apply did not finish. The next sync reapplies the materialization before
	// treating the stale Vault as local edits.
	PendingApply bool `json:"pendingApply,omitempty"`
}

// ConvergentBlockedReason is a typed, localizable migration refusal. Detail
// carries only provider/entity names and counts, never payload values.
type ConvergentBlockedReason struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// ConvergentMigrationPreview is what the UI shows before enabling v2 and when
// enablement is refused. The cloud bytes are untouched when CanInitialize is
// false.
type ConvergentMigrationPreview struct {
	SchemaVersion         int                       `json:"schemaVersion"`
	CanInitialize         bool                      `json:"canInitialize"`
	AlreadyInitialized    bool                      `json:"alreadyInitialized,omitempty"`
	ProviderSchemaVersion int                       `json:"providerSchemaVersion"`
	LocalSchemaVersion    int                       `json:"localSchemaVersion"`
	EntityCount           int                       `json:"entityCount"`
	ConflictCount         int                       `json:"conflictCount"`
	BlockedReasons        []ConvergentBlockedReason `json:"blockedReasons,omitempty"`
}

// ConvergentDivergenceInfo is the actionable divergence report. Values are
// never included; only register addresses, hashes, vectors, and a stable kind.
type ConvergentDivergenceInfo struct {
	Kind         string            `json:"kind"`
	Family       string            `json:"family,omitempty"`
	EntityID     string            `json:"entityId,omitempty"`
	Dot          string            `json:"dot,omitempty"`
	Detail       string            `json:"detail,omitempty"`
	LocalHash    string            `json:"localHash,omitempty"`
	RemoteHash   string            `json:"remoteHash,omitempty"`
	LocalVector  map[string]uint64 `json:"localVector,omitempty"`
	RemoteVector map[string]uint64 `json:"remoteVector,omitempty"`
	Actions      []string          `json:"actions"`
}

// ConvergentRoundInfo is one round's observable progress/outcome.
type ConvergentRoundInfo struct {
	Round   int    `json:"round"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
}

// ConvergentInfo is the UI's convergent-sync state.
type ConvergentInfo struct {
	SchemaVersion  int                         `json:"schemaVersion"`
	Enabled        bool                        `json:"enabled"`
	Phase          string                      `json:"phase"` // disabled|idle|paused|migrating|verifying|converged|diverged|blocked|failed
	Round          int                         `json:"round"`
	TotalRounds    int                         `json:"totalRounds"`
	RoundKind      string                      `json:"roundKind,omitempty"`
	Outcome        string                      `json:"outcome,omitempty"`
	Writes         int                         `json:"writes"`
	ConflictCount  int                         `json:"conflictCount"`
	EntityCount    int                         `json:"entityCount"`
	LastVerifiedAt int64                       `json:"lastVerifiedAt,omitempty"`
	Rounds         []ConvergentRoundInfo       `json:"rounds,omitempty"`
	Divergence     *ConvergentDivergenceInfo   `json:"divergence,omitempty"`
	Migration      *ConvergentMigrationPreview `json:"migration,omitempty"`
}

func convergentInfoFor(config Config) ConvergentInfo {
	info := ConvergentInfo{
		SchemaVersion:  config.Convergent.SchemaVersion,
		Enabled:        config.Convergent.Enabled,
		TotalRounds:    ConvergentVerifyRounds,
		LastVerifiedAt: config.Convergent.LastVerifiedAt,
	}
	switch {
	case config.Convergent.SchemaVersion == 0:
		info.Phase = "disabled"
	case config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion:
		info.Phase = "blocked"
		info.Migration = &ConvergentMigrationPreview{
			SchemaVersion: domainsync.ConvergentSchemaVersion,
			BlockedReasons: []ConvergentBlockedReason{{
				Code:   "invalid-schema",
				Detail: fmt.Sprintf("local marker %d", config.Convergent.SchemaVersion),
			}},
		}
	case !config.Convergent.Enabled:
		info.Phase = "paused"
	default:
		info.Phase = "idle"
	}
	return info
}

// ---------------------------------------------------------------------------
// Payload <-> snapshot conversion
// ---------------------------------------------------------------------------

// convergentFamilies are the payload arrays covered by v2. Settings stay
// device-local per spec item 111 and are not CRDT registers; they are carried
// through unchanged so no field is silently dropped. SyncMeta is derived from
// entity tombstones.
func convergentFamilies() []domainsync.ConvergentFamily {
	return domainsync.DefaultConvergentFamilies()
}

func payloadSnapshot(payload Payload) (domainsync.ConvergentSnapshot, error) {
	data, err := domainsync.MarshalJSON(payload)
	if err != nil {
		return domainsync.ConvergentSnapshot{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return domainsync.ConvergentSnapshot{}, err
	}
	return domainsync.ParseConvergentSnapshot(raw, convergentFamilies())
}

func snapshotsEqual(left, right domainsync.ConvergentSnapshot) bool {
	families := map[string]bool{}
	for family := range left.Families {
		families[family] = true
	}
	for family := range right.Families {
		families[family] = true
	}
	for family := range families {
		leftRecords := left.Families[family]
		rightRecords := right.Families[family]
		if len(leftRecords) != len(rightRecords) {
			return false
		}
		byID := make(map[string]json.RawMessage, len(leftRecords))
		for _, record := range leftRecords {
			byID[record.ID] = record.Value
		}
		for _, record := range rightRecords {
			value, exists := byID[record.ID]
			if !exists || string(value) != string(record.Value) {
				return false
			}
		}
	}
	return true
}

func snapshotHasData(snapshot domainsync.ConvergentSnapshot) bool {
	for _, records := range snapshot.Families {
		if len(records) > 0 {
			return true
		}
	}
	return false
}

func snapshotEntityCount(snapshot domainsync.ConvergentSnapshot) int {
	count := 0
	for _, records := range snapshot.Families {
		count += len(records)
	}
	return count
}

// payloadFromState builds the complete v1 materialized payload plus the inline
// envelope. Settings are supplied by the caller (device-local, carried from
// the remote copy when present), and syncMeta is rebuilt from tombstones so an
// older client sees the same deletions.
func payloadFromState(state domainsync.ConvergentState, settings map[string]json.RawMessage, now int64, deviceID string) (Payload, error) {
	snapshot, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		return Payload{}, err
	}
	raw, err := domainsync.RawSnapshot(snapshot)
	if err != nil {
		return Payload{}, err
	}
	families := convergentFamilies()
	for _, family := range families {
		value, exists := raw[family.Name]
		if !exists {
			value = json.RawMessage("[]")
		}
		raw[family.Name] = value
	}
	if len(settings) > 0 {
		encoded, err := json.Marshal(settings)
		if err != nil {
			return Payload{}, err
		}
		raw["settings"] = encoded
	}
	tombstones, err := domainsync.ConvergentTombstones(state)
	if err != nil {
		return Payload{}, err
	}
	meta := SyncMeta{SchemaVersion: 1, GeneratedAt: now, DeviceID: deviceID}
	for _, tombstone := range tombstones {
		meta.Deletions = append(meta.Deletions, SyncDeletion{
			EntityType: tombstone.Family,
			ID:         tombstone.ID,
			DeletedAt:  tombstone.DeletedAt,
			DeviceID:   tombstone.Node,
		})
	}
	metaData, err := json.Marshal(meta)
	if err != nil {
		return Payload{}, err
	}
	raw["syncMeta"] = metaData
	raw["syncedAt"], _ = json.Marshal(now)
	envelope, err := domainsync.WrapConvergentEnvelope(state)
	if err != nil {
		return Payload{}, err
	}
	envelopeData, err := json.Marshal(envelope)
	if err != nil {
		return Payload{}, err
	}
	raw["convergentSync"] = envelopeData
	data, err := json.Marshal(raw)
	if err != nil {
		return Payload{}, err
	}
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Payload{}, err
	}
	payload.SyncedAt = now
	return payload, nil
}

// legacyConvergentDeletions converts v1 syncMeta tombstones into the v2
// import shape. Unknown families cannot be addressed by the CRDT family list,
// and a tombstone for an ID that is still present in the materialized winners
// is stale (a later v1 writer recreated the record), so both are skipped.
func legacyConvergentDeletions(payload Payload, snapshot domainsync.ConvergentSnapshot) []domainsync.ConvergentDeletion {
	if payload.SyncMeta == nil || len(payload.SyncMeta.Deletions) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, family := range convergentFamilies() {
		known[family.Name] = true
	}
	present := map[string]bool{}
	for family, records := range snapshot.Families {
		for _, record := range records {
			present[family+"\x00"+record.ID] = true
		}
	}
	deletions := make([]domainsync.ConvergentDeletion, 0, len(payload.SyncMeta.Deletions))
	for _, deletion := range payload.SyncMeta.Deletions {
		if !known[deletion.EntityType] || deletion.ID == "" {
			continue
		}
		if present[deletion.EntityType+"\x00"+deletion.ID] {
			continue
		}
		deletions = append(deletions, domainsync.ConvergentDeletion{
			Family:    deletion.EntityType,
			ID:        deletion.ID,
			DeletedAt: deletion.DeletedAt,
			Node:      deletion.DeviceID,
		})
	}
	return deletions
}

func chooseSettings(remote *Payload, verified *Payload, local Payload) map[string]json.RawMessage {
	if remote != nil && remote.Settings != nil {
		return remote.Settings
	}
	if verified != nil && verified.Settings != nil {
		return verified.Settings
	}
	return local.Settings
}

// ---------------------------------------------------------------------------
// Local sealing and strict loading
// ---------------------------------------------------------------------------

func (s *Service) convergentEncryptOptions(version int) domainsync.EncryptOptions {
	config := s.currentConfig()
	return domainsync.EncryptOptions{
		Version:    version,
		UpdatedAt:  s.now().UnixMilli(),
		DeviceID:   config.DeviceID,
		DeviceName: config.DeviceName,
		AppVersion: config.AppVersion,
		Random:     s.random,
		Now:        s.now,
	}
}

func (s *Service) sealConvergentState(state domainsync.ConvergentState) (string, string, error) {
	password := s.passwordValue()
	if password == "" {
		return "", "", ErrPasswordRequired
	}
	envelope, err := domainsync.WrapConvergentEnvelope(state)
	if err != nil {
		return "", "", err
	}
	file, err := domainsync.EncryptPayload(envelope, password, s.convergentEncryptOptions(1))
	if err != nil {
		return "", "", err
	}
	content, err := domainsync.MarshalFile(file)
	if err != nil {
		return "", "", err
	}
	signature, err := domainsync.ConvergentStateHash(state)
	if err != nil {
		return "", "", err
	}
	return string(content), signature, nil
}

func (s *Service) openSealedConvergent(blob, signature, password string, kind string) ([]byte, error) {
	if blob == "" || signature == "" {
		return nil, fmt.Errorf("%w: incomplete sealed %s", ErrConvergentCorrupt, kind)
	}
	file, err := domainsync.UnmarshalFile([]byte(blob))
	if err != nil {
		return nil, fmt.Errorf("%w: %s envelope: %v", ErrConvergentCorrupt, kind, err)
	}
	plaintext, err := domainsync.DecryptPayload(file, password)
	if err != nil {
		switch {
		case errors.Is(err, domainsync.ErrPasswordRequired):
			return nil, ErrPasswordRequired
		case errors.Is(err, domainsync.ErrWrongPassword):
			return nil, ErrWrongPassword
		default:
			return nil, fmt.Errorf("%w: %s ciphertext: %v", ErrConvergentCorrupt, kind, err)
		}
	}
	return plaintext, nil
}

// loadConvergentStateStrict never falls back to an empty state: a marker with
// missing, corrupt, or unsupported data is a partial migration and fails.
func (s *Service) loadConvergentStateStrict(config Config) (domainsync.ConvergentState, error) {
	if config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		return domainsync.ConvergentState{}, fmt.Errorf("%w: local marker %d", ErrConvergentCorrupt, config.Convergent.SchemaVersion)
	}
	plaintext, err := s.openSealedConvergent(config.Convergent.Replica, config.Convergent.ReplicaSignature, s.passwordValue(), "convergent replica")
	if err != nil {
		return domainsync.ConvergentState{}, err
	}
	envelope, err := domainsync.ParseConvergentEnvelope(plaintext)
	if err != nil {
		return domainsync.ConvergentState{}, fmt.Errorf("%w: %v", ErrConvergentCorrupt, err)
	}
	hash, err := domainsync.ConvergentStateHash(envelope.State)
	if err != nil {
		return domainsync.ConvergentState{}, fmt.Errorf("%w: %v", ErrConvergentCorrupt, err)
	}
	if hash != config.Convergent.ReplicaSignature {
		return domainsync.ConvergentState{}, fmt.Errorf("%w: replica signature mismatch", ErrConvergentCorrupt)
	}
	return envelope.State, nil
}

func (s *Service) sealVerifiedPayload(payload Payload) (string, string, error) {
	password := s.passwordValue()
	if password == "" {
		return "", "", ErrPasswordRequired
	}
	file, err := domainsync.EncryptPayload(payload, password, s.convergentEncryptOptions(1))
	if err != nil {
		return "", "", err
	}
	content, err := domainsync.MarshalFile(file)
	if err != nil {
		return "", "", err
	}
	signature, err := payload.Signature()
	if err != nil {
		return "", "", err
	}
	return string(content), signature, nil
}

// loadVerifiedPayloadStrict returns nil only when no verified baseline exists.
// A half-written record or corrupt ciphertext fails closed because it is the
// trusted legacy baseline and the safe restore point.
func (s *Service) loadVerifiedPayloadStrict(config Config) (*Payload, error) {
	blob := config.Convergent.VerifiedPayload
	signature := config.Convergent.VerifiedSignature
	if blob == "" && signature == "" {
		return nil, nil
	}
	plaintext, err := s.openSealedConvergent(blob, signature, s.passwordValue(), "verified payload")
	if err != nil {
		return nil, err
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		return nil, fmt.Errorf("%w: verified payload: %v", ErrConvergentCorrupt, err)
	}
	actual, err := payload.Signature()
	if err != nil {
		return nil, fmt.Errorf("%w: verified payload: %v", ErrConvergentCorrupt, err)
	}
	if actual != signature {
		return nil, fmt.Errorf("%w: verified payload signature mismatch", ErrConvergentCorrupt)
	}
	return &payload, nil
}

func (s *Service) saveConvergentConfig(config Config) error {
	s.mu.Lock()
	s.config = config
	s.mu.Unlock()
	if err := s.configs.Save(context.Background(), config); err != nil {
		return fmt.Errorf("sync: save the convergent profile: %w", err)
	}
	s.changed()
	return nil
}

func (s *Service) setConvergentInfo(info ConvergentInfo) {
	s.mu.Lock()
	s.convergent = info
	s.mu.Unlock()
	s.changed()
}

func (s *Service) mergeConvergentInfo(mutate func(*ConvergentInfo)) {
	s.mu.Lock()
	mutate(&s.convergent)
	s.mu.Unlock()
	s.changed()
}

// ---------------------------------------------------------------------------
// Remote reading
// ---------------------------------------------------------------------------

type convergentRemoteKind string

const (
	convergentRemoteMissing convergentRemoteKind = "missing"
	convergentRemoteV1      convergentRemoteKind = "v1"
	convergentRemoteV2      convergentRemoteKind = "v2"
	convergentRemoteFuture  convergentRemoteKind = "future"
	convergentRemoteInvalid convergentRemoteKind = "invalid"
)

type convergentRemote struct {
	Kind          convergentRemoteKind
	File          RemoteFile
	Payload       Payload
	State         *domainsync.ConvergentState
	Materialized  domainsync.ConvergentSnapshot
	UpdatedAt     int64
	SchemaVersion int
	Code          string
	Detail        string
}

func (r convergentRemote) exists() bool { return r.Kind != convergentRemoteMissing }

func (r convergentRemote) structuralError() error {
	switch r.Kind {
	case convergentRemoteFuture:
		return fmt.Errorf("%w: provider schema %d", ErrUnsupportedVersion, r.SchemaVersion)
	case convergentRemoteInvalid:
		return fmt.Errorf("%w: %s", ErrConvergentCorrupt, r.Detail)
	}
	return nil
}

func (s *Service) readConvergentRemote(ctx context.Context, token domainsync.Token) (convergentRemote, domainsync.Token, error) {
	remote := s.activeRemote()
	file, exists, nextToken, err := s.readRemote(ctx, remote, token)
	if err != nil {
		return convergentRemote{}, nextToken, err
	}
	if !exists {
		return convergentRemote{Kind: convergentRemoteMissing}, nextToken, nil
	}
	envelope, err := domainsync.UnmarshalFile(file.Content)
	if err != nil {
		return convergentRemote{
			Kind:   convergentRemoteInvalid,
			File:   file,
			Code:   "corrupt-envelope",
			Detail: err.Error(),
		}, nextToken, nil
	}
	updatedAt := envelope.Meta.UpdatedAt
	if file.UpdatedAt != 0 {
		updatedAt = file.UpdatedAt
	}
	base := convergentRemote{
		File:          file,
		UpdatedAt:     updatedAt,
		SchemaVersion: envelope.Meta.SyncSchemaVersion,
	}
	switch {
	case envelope.Meta.SyncSchemaVersion == 0 || envelope.Meta.SyncSchemaVersion == 1:
		base.Kind = convergentRemoteV1
	case envelope.Meta.SyncSchemaVersion == domainsync.ConvergentSchemaVersion:
		base.Kind = convergentRemoteV2
	case envelope.Meta.SyncSchemaVersion > domainsync.ConvergentSchemaVersion:
		base.Kind = convergentRemoteFuture
		base.Code = "unsupported-newer-schema"
		base.Detail = fmt.Sprintf("provider schema version %d", envelope.Meta.SyncSchemaVersion)
		return base, nextToken, nil
	default:
		base.Kind = convergentRemoteInvalid
		base.Code = "invalid-schema"
		base.Detail = fmt.Sprintf("provider schema version %d", envelope.Meta.SyncSchemaVersion)
		return base, nextToken, nil
	}
	plaintext, err := domainsync.DecryptPayload(envelope, s.passwordValue())
	if err != nil {
		switch {
		case errors.Is(err, domainsync.ErrPasswordRequired):
			return convergentRemote{}, nextToken, ErrPasswordRequired
		case errors.Is(err, domainsync.ErrWrongPassword):
			return convergentRemote{}, nextToken, ErrWrongPassword
		default:
			base.Kind = convergentRemoteInvalid
			base.Code = "corrupt-envelope"
			base.Detail = err.Error()
			return base, nextToken, nil
		}
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		base.Kind = convergentRemoteInvalid
		base.Code = "corrupt-envelope"
		base.Detail = err.Error()
		return base, nextToken, nil
	}
	base.Payload = payload
	if base.Kind == convergentRemoteV1 {
		if len(payload.ConvergentSync) > 0 && !isJSONNull(payload.ConvergentSync) {
			base.Kind = convergentRemoteInvalid
			base.Code = "envelope-metadata-mismatch"
			base.Detail = "v1 metadata carries a convergent envelope"
		}
		return base, nextToken, nil
	}
	// v2: the envelope is mandatory, must parse strictly, and its materialized
	// winners must match the inline v1 snapshot exactly.
	if len(payload.ConvergentSync) == 0 {
		base.Kind = convergentRemoteInvalid
		base.Code = "missing-envelope"
		base.Detail = "v2 payload has no convergent envelope"
		return base, nextToken, nil
	}
	parsed, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
	if err != nil {
		base.Kind = convergentRemoteInvalid
		base.Code = "damaged-envelope"
		base.Detail = err.Error()
		return base, nextToken, nil
	}
	materialized, err := payloadSnapshot(payload)
	if err != nil {
		base.Kind = convergentRemoteInvalid
		base.Code = "damaged-materialized-payload"
		base.Detail = err.Error()
		return base, nextToken, nil
	}
	if err := domainsync.EnvelopeMatchesSnapshot(parsed.State, materialized); err != nil {
		base.Kind = convergentRemoteInvalid
		base.Code = "envelope-materialized-mismatch"
		base.Detail = err.Error()
		return base, nextToken, nil
	}
	base.State = &parsed.State
	base.Materialized = materialized
	return base, nextToken, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// ---------------------------------------------------------------------------
// Migration planning
// ---------------------------------------------------------------------------

type convergentMigrationInput struct {
	DeviceID       string
	Now            int64
	Strategy       domainsync.Strategy
	Local          Payload
	LocalSnapshot  domainsync.ConvergentSnapshot
	LegacyBaseline *Payload
	Remote         convergentRemote
}

type convergentPlan struct {
	Preview      ConvergentMigrationPreview
	State        *domainsync.ConvergentState
	Materialized *Payload
	Settings     map[string]json.RawMessage
}

func (p *convergentPlan) block(code, detail string) {
	p.Preview.BlockedReasons = append(p.Preview.BlockedReasons, ConvergentBlockedReason{Code: code, Detail: detail})
	p.Preview.CanInitialize = false
	p.State = nil
	p.Materialized = nil
}

func planConvergentMigration(input convergentMigrationInput) convergentPlan {
	plan := convergentPlan{Preview: ConvergentMigrationPreview{
		SchemaVersion:         domainsync.ConvergentSchemaVersion,
		ProviderSchemaVersion: input.Remote.SchemaVersion,
		LocalSchemaVersion:    0,
	}}
	remote := input.Remote
	if remote.Kind == convergentRemoteFuture {
		plan.block("unsupported-newer-schema", fmt.Sprintf("provider schema %d", remote.SchemaVersion))
		return plan
	}
	if remote.Kind == convergentRemoteInvalid {
		plan.block(remote.Code, remote.Detail)
		return plan
	}
	localSnapshot := input.LocalSnapshot
	options := MergeOptions{PreserveDeletions: domainsync.IsPreserveDeletionsStrategy(input.Strategy)}

	var state domainsync.ConvergentState
	var materialized Payload
	var settings map[string]json.RawMessage
	legacyBootstrap := false

	switch remote.Kind {
	case convergentRemoteV2:
		if remote.State == nil {
			plan.block("damaged-envelope", "provider state is missing")
			return plan
		}
		state = *remote.State
		settings = chooseSettings(&remote.Payload, nil, input.Local)
		switch {
		case snapshotsEqual(localSnapshot, remote.Materialized):
			materialized = remote.Payload
		case input.LegacyBaseline != nil:
			merged, err := MergePayloads(input.LegacyBaseline, input.Local, remote.Payload, options)
			if err != nil {
				plan.block("legacy-merge-failed", err.Error())
				return plan
			}
			if len(merged.Conflicts) > 0 {
				plan.block("legacy-conflict", fmt.Sprintf("%d unresolved legacy changes", len(merged.Conflicts)))
				return plan
			}
			mergedSnapshot, err := payloadSnapshot(merged.Payload)
			if err != nil {
				plan.block("legacy-merge-failed", err.Error())
				return plan
			}
			next, _, err := domainsync.ApplyConvergentSnapshot(state, mergedSnapshot, input.DeviceID, input.Now, false)
			if err != nil {
				plan.block("damaged-envelope", err.Error())
				return plan
			}
			state = next
			materialized = merged.Payload
			settings = chooseSettings(&merged.Payload, input.LegacyBaseline, input.Local)
		case !snapshotHasData(localSnapshot):
			// Fresh install: adopt the v2 cloud copy rather than turning
			// first-launch defaults into edits.
			materialized = remote.Payload
		default:
			plan.block("no-trusted-baseline", "local and provider data differ and no trusted baseline exists")
			return plan
		}
	case convergentRemoteV1:
		// A v1 lineage is the only source of pre-CRDT tombstones; they must
		// enter the v2 state below or a stale replica can resurrect them.
		legacyBootstrap = true
		remoteSnapshot, err := payloadSnapshot(remote.Payload)
		if err != nil {
			plan.block("damaged-materialized-payload", err.Error())
			return plan
		}
		settings = chooseSettings(&remote.Payload, input.LegacyBaseline, input.Local)
		switch {
		case input.LegacyBaseline != nil:
			merged, err := MergePayloads(input.LegacyBaseline, input.Local, remote.Payload, options)
			if err != nil {
				plan.block("legacy-merge-failed", err.Error())
				return plan
			}
			if len(merged.Conflicts) > 0 {
				plan.block("legacy-conflict", fmt.Sprintf("%d unresolved legacy changes", len(merged.Conflicts)))
				return plan
			}
			mergedSnapshot, err := payloadSnapshot(merged.Payload)
			if err != nil {
				plan.block("legacy-merge-failed", err.Error())
				return plan
			}
			state, err = domainsync.CreateConvergentStateFromSnapshot(mergedSnapshot, input.DeviceID, input.Now)
			if err != nil {
				plan.block("legacy-merge-failed", err.Error())
				return plan
			}
			materialized = merged.Payload
			settings = chooseSettings(&merged.Payload, input.LegacyBaseline, input.Local)
		case !snapshotHasData(remoteSnapshot):
			if !snapshotHasData(localSnapshot) {
				plan.block("empty-vault", "both the local Vault and the provider are empty")
				return plan
			}
			state, err = domainsync.CreateConvergentStateFromSnapshot(localSnapshot, input.DeviceID, input.Now)
			if err != nil {
				plan.block("damaged-materialized-payload", err.Error())
				return plan
			}
			materialized = input.Local
		case !snapshotHasData(localSnapshot) || snapshotsEqual(localSnapshot, remoteSnapshot):
			state, err = domainsync.CreateConvergentStateFromSnapshot(remoteSnapshot, input.DeviceID, input.Now)
			if err != nil {
				plan.block("damaged-materialized-payload", err.Error())
				return plan
			}
			materialized = remote.Payload
		default:
			plan.block("no-trusted-baseline", "local and provider data differ and no trusted baseline exists")
			return plan
		}
	case convergentRemoteMissing:
		// The local v1 payload still carries tombstones for records deleted on
		// this device; preserve them when bootstrapping v2 locally.
		legacyBootstrap = true
		if !snapshotHasData(localSnapshot) {
			plan.block("empty-vault", "the local Vault has no syncable records")
			return plan
		}
		var err error
		state, err = domainsync.CreateConvergentStateFromSnapshot(localSnapshot, input.DeviceID, input.Now)
		if err != nil {
			plan.block("damaged-materialized-payload", err.Error())
			return plan
		}
		materialized = input.Local
		settings = chooseSettings(nil, nil, input.Local)
	default:
		plan.block("invalid-schema", "provider schema is not readable")
		return plan
	}

	materializedSnapshot, err := payloadSnapshot(materialized)
	if err != nil {
		plan.block("damaged-materialized-payload", err.Error())
		return plan
	}
	if legacyBootstrap {
		deletions := legacyConvergentDeletions(materialized, materializedSnapshot)
		imported, _, err := domainsync.ImportConvergentDeletions(state, deletions, input.DeviceID, input.Now)
		if err != nil {
			plan.block("damaged-materialized-payload", err.Error())
			return plan
		}
		state = imported
	}
	if err := domainsync.ValidateConvergentState(state); err != nil {
		plan.block("damaged-envelope", err.Error())
		return plan
	}
	conflicts, err := domainsync.ConvergentConflicts(state)
	if err != nil {
		plan.block("damaged-envelope", err.Error())
		return plan
	}
	plan.Preview.ConflictCount = len(conflicts)
	if len(conflicts) > 0 {
		plan.block("conflicts-unresolved", fmt.Sprintf("%d contested registers", len(conflicts)))
		return plan
	}
	plan.Preview.EntityCount = snapshotEntityCount(materializedSnapshot)
	var remotePtr *Payload
	if remote.exists() {
		remotePtr = &remote.Payload
	}
	if finding := DetectDestructiveSync(materialized, input.LegacyBaseline, remotePtr); finding.Suspicious() {
		plan.block("shrunk-vault", string(finding.Kind))
		return plan
	}
	full, err := payloadFromState(state, settings, input.Now, input.DeviceID)
	if err != nil {
		plan.block("damaged-materialized-payload", err.Error())
		return plan
	}
	plan.Preview.CanInitialize = true
	plan.State = &state
	plan.Materialized = &full
	plan.Settings = settings
	return plan
}

// ---------------------------------------------------------------------------
// Enable / pause / restore
// ---------------------------------------------------------------------------

// SetConvergentEnabled enables v2 (running the fail-closed migration when this
// is the first time) or pauses an initialized replica. It never downgrades.
func (s *Service) SetConvergentEnabled(ctx context.Context, enabled bool) (ConvergentMigrationPreview, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	config := s.currentConfig()
	if !config.Connected {
		return ConvergentMigrationPreview{}, ErrNotConfigured
	}
	if s.passwordValue() == "" {
		s.finishSync(ProblemPasswordRequired)
		return ConvergentMigrationPreview{}, ErrPasswordRequired
	}
	if !enabled {
		if config.Convergent.SchemaVersion == 0 {
			return ConvergentMigrationPreview{SchemaVersion: domainsync.ConvergentSchemaVersion, CanInitialize: true}, nil
		}
		if config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
			s.finishSync(ProblemConvergentCorrupt)
			return ConvergentMigrationPreview{}, ErrConvergentCorrupt
		}
		config.Convergent.Enabled = false
		if err := s.saveConvergentConfig(config); err != nil {
			return ConvergentMigrationPreview{}, err
		}
		s.setConvergentInfo(func() ConvergentInfo {
			info := convergentInfoFor(config)
			info.Migration = &ConvergentMigrationPreview{
				SchemaVersion:      domainsync.ConvergentSchemaVersion,
				CanInitialize:      true,
				AlreadyInitialized: true,
			}
			return info
		}())
		s.mu.Lock()
		s.status = StatusSynced
		s.problem = ProblemConvergentPaused
		s.syncing = false
		s.mu.Unlock()
		s.changed()
		return ConvergentMigrationPreview{
			SchemaVersion:      domainsync.ConvergentSchemaVersion,
			CanInitialize:      true,
			AlreadyInitialized: true,
		}, nil
	}

	token, err := s.ensureFreshToken(ctx)
	if err != nil {
		s.fail(problemFor(err))
		return ConvergentMigrationPreview{}, err
	}
	if endpoint := insecureEndpointForToken(token); endpoint != "" {
		s.mu.Lock()
		confirmed := s.insecureConfirmedEndpoint == endpoint
		s.mu.Unlock()
		if !confirmed {
			s.fail(ProblemInsecureEndpoint)
			return ConvergentMigrationPreview{}, ErrInsecureEndpoint
		}
	}

	if config.Convergent.SchemaVersion == domainsync.ConvergentSchemaVersion {
		// Already initialized (possibly paused): resume without a second
		// migration and without touching provider bytes.
		config.Convergent.Enabled = true
		if err := s.saveConvergentConfig(config); err != nil {
			return ConvergentMigrationPreview{}, err
		}
		s.setConvergentInfo(convergentInfoFor(config))
		return ConvergentMigrationPreview{
			SchemaVersion:      domainsync.ConvergentSchemaVersion,
			CanInitialize:      true,
			AlreadyInitialized: true,
			LocalSchemaVersion: domainsync.ConvergentSchemaVersion,
		}, nil
	}
	if config.Convergent.SchemaVersion != 0 {
		s.finishSync(ProblemConvergentCorrupt)
		return ConvergentMigrationPreview{}, ErrConvergentCorrupt
	}

	local, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.finishSync(ProblemLocal)
		return ConvergentMigrationPreview{}, err
	}
	localSnapshot, err := payloadSnapshot(local)
	if err != nil {
		s.finishSync(ProblemLocal)
		return ConvergentMigrationPreview{}, err
	}
	remote, _, err := s.readConvergentRemote(ctx, token)
	if err != nil {
		s.fail(problemFor(err))
		return ConvergentMigrationPreview{}, err
	}
	baseline, baselineProblem := s.strictLegacyBaseline(config)
	if baselineProblem != "" {
		preview := ConvergentMigrationPreview{
			SchemaVersion:         domainsync.ConvergentSchemaVersion,
			ProviderSchemaVersion: remote.SchemaVersion,
			BlockedReasons:        []ConvergentBlockedReason{{Code: "corrupt-local-baseline", Detail: baselineProblem}},
		}
		s.setConvergentInfo(ConvergentInfo{
			SchemaVersion: 0,
			Phase:         "blocked",
			TotalRounds:   ConvergentVerifyRounds,
			Migration:     &preview,
		})
		s.finishSync(ProblemConvergentMigration)
		return preview, fmt.Errorf("%w: %s", ErrConvergentMigration, baselineProblem)
	}
	plan := planConvergentMigration(convergentMigrationInput{
		DeviceID:       config.DeviceID,
		Now:            s.now().UnixMilli(),
		Strategy:       domainsync.NormalizeStrategy(config.Strategy),
		Local:          local,
		LocalSnapshot:  localSnapshot,
		LegacyBaseline: baseline,
		Remote:         remote,
	})
	if !plan.Preview.CanInitialize || plan.State == nil || plan.Materialized == nil {
		info := convergentInfoFor(config)
		info.Phase = "blocked"
		info.Migration = &plan.Preview
		info.ConflictCount = plan.Preview.ConflictCount
		info.EntityCount = plan.Preview.EntityCount
		s.setConvergentInfo(info)
		s.finishSync(ProblemConvergentMigration)
		detail := "migration is blocked"
		if len(plan.Preview.BlockedReasons) > 0 {
			detail = plan.Preview.BlockedReasons[0].Code
		}
		return plan.Preview, fmt.Errorf("%w: %s", ErrConvergentMigration, detail)
	}

	s.mu.Lock()
	s.status = StatusSyncing
	s.syncing = true
	s.problem = ProblemNone
	s.guard = GuardFinding{}
	s.mu.Unlock()
	info := convergentInfoFor(config)
	info.Phase = "migrating"
	info.Migration = &plan.Preview
	info.ConflictCount = plan.Preview.ConflictCount
	info.EntityCount = plan.Preview.EntityCount
	s.setConvergentInfo(info)

	// A protective backup is mandatory before the migration mutates local
	// data or the provider's bytes. There is no configuration in which
	// migration may proceed without a restore point.
	if err := s.requireProtectiveBackup(ctx); err != nil {
		s.finishSync(ProblemBackup)
		return plan.Preview, err
	}
	migratedSnapshot, err := payloadSnapshot(*plan.Materialized)
	if err != nil {
		s.finishSync(ProblemLocal)
		return plan.Preview, err
	}
	if !snapshotsEqual(localSnapshot, migratedSnapshot) {
		if err := s.vault.Apply(ctx, *plan.Materialized); err != nil {
			s.finishSync(ProblemLocal)
			return plan.Preview, err
		}
		s.notifySnapshotApplied()
	}
	applied, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.finishSync(ProblemLocal)
		return plan.Preview, err
	}
	replicaBlob, replicaSignature, err := s.sealConvergentState(*plan.State)
	if err != nil {
		s.finishSync(ProblemLocal)
		return plan.Preview, err
	}
	config = s.currentConfig()
	config.Convergent = ConvergentConfig{
		SchemaVersion:    domainsync.ConvergentSchemaVersion,
		Enabled:          true,
		Replica:          replicaBlob,
		ReplicaSignature: replicaSignature,
	}
	if remote.exists() {
		verifiedBlob, verifiedSignature, err := s.sealVerifiedPayload(remote.Payload)
		if err != nil {
			s.finishSync(ProblemLocal)
			return plan.Preview, err
		}
		config.Convergent.VerifiedPayload = verifiedBlob
		config.Convergent.VerifiedSignature = verifiedSignature
		config.Convergent.LastVerifiedAt = remote.UpdatedAt
	}
	// Persist the explicit marker and the durable replica before the first
	// network write. A failure after this point is a resumable partial
	// publication, never a v1 fallback.
	if err := s.saveConvergentConfig(config); err != nil {
		s.finishSync(ProblemLocal)
		return plan.Preview, err
	}
	info.Phase = "verifying"
	info.SchemaVersion = domainsync.ConvergentSchemaVersion
	info.Enabled = true
	s.setConvergentInfo(info)

	if _, syncErr := s.syncConvergent(ctx, TriggerManual, false, token, applied, s.currentConfig()); syncErr != nil {
		return plan.Preview, syncErr
	}
	plan.Preview.CanInitialize = true
	return plan.Preview, nil
}

// strictLegacyBaseline reads the v1 merge anchor without silently falling back
// when a stored anchor exists but cannot be decrypted. Migration needs a real
// baseline; a corrupt one blocks rather than guessing whether absent records
// were deleted.
func (s *Service) strictLegacyBaseline(config Config) (*Payload, string) {
	if config.AnchorVersion != AnchorVersion || config.SyncBase == "" {
		return nil, ""
	}
	file, err := domainsync.UnmarshalFile([]byte(config.SyncBase))
	if err != nil {
		return nil, "legacy anchor envelope is corrupt"
	}
	plaintext, err := domainsync.DecryptPayload(file, s.passwordValue())
	if err != nil {
		return nil, "legacy anchor cannot be decrypted"
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		return nil, "legacy anchor payload is corrupt"
	}
	signature, err := payload.Signature()
	if err != nil {
		return nil, "legacy anchor signature failed"
	}
	if config.BaseSignature != "" && signature != config.BaseSignature {
		return nil, "legacy anchor signature mismatch"
	}
	return &payload, ""
}

// RestoreLastVerified applies the last provider-verified materialized payload
// to the local Vault behind a protective backup. It never writes the provider
// and never mutates the replica, so it is always a safe divergence action.
func (s *Service) RestoreLastVerified(ctx context.Context) (Result, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	config := s.currentConfig()
	if config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		return Result{Trigger: TriggerManual}, ErrConvergentCorrupt
	}
	if s.passwordValue() == "" {
		return Result{Trigger: TriggerManual}, ErrPasswordRequired
	}
	verified, err := s.loadVerifiedPayloadStrict(config)
	if err != nil {
		s.finishSync(ProblemConvergentCorrupt)
		return Result{Trigger: TriggerManual}, err
	}
	if verified == nil {
		return Result{Trigger: TriggerManual}, fmt.Errorf("%w: no verified payload is available", ErrConvergentCorrupt)
	}
	if err := s.ensureProtectiveBackup(ctx); err != nil {
		s.finishSync(ProblemBackup)
		return Result{Trigger: TriggerManual}, err
	}
	if err := s.vault.Apply(ctx, *verified); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	s.notifySnapshotApplied()
	applied, err := s.vault.Snapshot(ctx)
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	signature, err := applied.Signature()
	if err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	config = s.currentConfig()
	config.LocalSignature = signature
	config.Convergent.PendingApply = false
	if err := s.saveConvergentConfig(config); err != nil {
		s.finishSync(ProblemLocal)
		return Result{Trigger: TriggerManual}, err
	}
	s.mergeConvergentInfo(func(info *ConvergentInfo) {
		info.Phase = "idle"
		info.Outcome = "restored"
	})
	s.completeSync(domainsync.ActionDownload)
	return Result{Trigger: TriggerManual, Action: domainsync.ActionDownload, Downloaded: true}, nil
}

// ---------------------------------------------------------------------------
// Convergent sync
// ---------------------------------------------------------------------------

func (s *Service) syncConvergent(
	ctx context.Context,
	trigger Trigger,
	force bool,
	token domainsync.Token,
	local Payload,
	config Config,
) (Result, error) {
	result := Result{Trigger: trigger}
	fail := func(problem Problem, phase string, err error) (Result, error) {
		s.mergeConvergentInfo(func(info *ConvergentInfo) {
			info.Phase = phase
			info.Outcome = "failed"
			info.Writes = result.Writes
			if errors.Is(err, ErrConvergentDivergence) {
				info.Phase = "diverged"
				info.Outcome = "diverged"
				info.Divergence = divergenceInfoFor(err)
			}
		})
		s.finishSync(problem)
		return result, err
	}

	state, err := s.loadConvergentStateStrict(config)
	if err != nil {
		return fail(convergentProblem(err), "blocked", err)
	}
	verified, err := s.loadVerifiedPayloadStrict(config)
	if err != nil {
		return fail(convergentProblem(err), "blocked", err)
	}

	info := convergentInfoFor(config)
	info.Phase = "verifying"
	info.SchemaVersion = domainsync.ConvergentSchemaVersion
	info.Enabled = true
	info.TotalRounds = ConvergentVerifyRounds
	info.LastVerifiedAt = config.Convergent.LastVerifiedAt
	for round := 1; round <= ConvergentVerifyRounds; round++ {
		info.Rounds = append(info.Rounds, ConvergentRoundInfo{Round: round, Kind: roundKind(round), Outcome: "pending"})
	}
	s.setConvergentInfo(info)

	// A previous run verified the provider but could not finish the local
	// apply. Reapply before interpreting the stale Vault as local edits.
	if config.Convergent.PendingApply {
		pendingSettings := chooseSettings(nil, verified, local)
		pendingPayload, err := payloadFromState(state, pendingSettings, s.now().UnixMilli(), config.DeviceID)
		if err != nil {
			return fail(ProblemLocal, "failed", err)
		}
		if err := s.ensureProtectiveBackup(ctx); err != nil {
			return fail(ProblemBackup, "failed", err)
		}
		if err := s.vault.Apply(ctx, pendingPayload); err != nil {
			return fail(ProblemLocal, "failed", err)
		}
		s.notifySnapshotApplied()
		local, err = s.vault.Snapshot(ctx)
		if err != nil {
			return fail(ProblemLocal, "failed", err)
		}
		config.Convergent.PendingApply = false
		if err := s.saveConvergentConfig(config); err != nil {
			return fail(ProblemLocal, "failed", err)
		}
	}

	localSnapshot, err := payloadSnapshot(local)
	if err != nil {
		return fail(ProblemLocal, "failed", err)
	}
	strategy := domainsync.NormalizeStrategy(config.Strategy)
	materializedBefore := mustMaterializeSnapshot(state)
	stateChanged := false

	// smartMerge and smartMergePreserve turn local edits into causal writes.
	// preferLocal waits until the remote join so its writes dominate every
	// observed candidate; preferCloud adopts the remote join.
	if strategy == domainsync.StrategySmartMerge || strategy == domainsync.StrategySmartMergePreserve {
		applySnapshot := localSnapshot
		if strategy == domainsync.StrategySmartMergePreserve {
			applySnapshot = preserveUnion(materializedBefore, localSnapshot, verified)
		}
		state, stateChanged, err = domainsync.ApplyConvergentSnapshot(state, applySnapshot, config.DeviceID, s.now().UnixMilli(), false)
		if err != nil {
			return fail(ProblemLocal, "failed", err)
		}
	}

	remote, token, err := s.readConvergentRemote(ctx, token)
	if err != nil {
		return fail(problemFor(err), "failed", err)
	}
	if err := remote.structuralError(); err != nil {
		return fail(problemFor(err), "blocked", err)
	}
	settings := chooseSettings(localOrNil(remote), verified, local)
	state, remoteChanged, err := s.joinConvergentRemote(state, remote, verified, config, s.now().UnixMilli())
	if err != nil {
		info.Divergence = divergenceInfoFor(err)
		return fail(convergentProblem(err), "diverged", err)
	}

	if strategy == domainsync.StrategyPreferLocal {
		state, stateChanged, err = domainsync.ApplyConvergentSnapshot(state, localSnapshot, config.DeviceID, s.now().UnixMilli(), true)
		if err != nil {
			return fail(ProblemLocal, "failed", err)
		}
	}
	if strategy == domainsync.StrategySmartMergePreserve {
		union := preserveUnion(mustMaterializeSnapshot(state), localSnapshot, verified)
		next, changedUnion, applyErr := domainsync.ApplyConvergentSnapshot(state, union, config.DeviceID, s.now().UnixMilli(), false)
		if applyErr != nil {
			return fail(ProblemLocal, "failed", applyErr)
		}
		state = next
		stateChanged = stateChanged || changedUnion
	}

	outgoing, err := payloadFromState(state, settings, s.now().UnixMilli(), config.DeviceID)
	if err != nil {
		return fail(ProblemLocal, "failed", err)
	}
	var baselinePtr, remotePtr *Payload
	if verified != nil {
		baselinePtr = verified
	}
	if remote.exists() {
		remotePtr = &remote.Payload
	}
	if blocked, blockedResult, guardErr := s.applyGuard(outgoing, baselinePtr, remotePtr, force, trigger); blocked {
		return blockedResult, guardErr
	}

	// Local dots are durable before any provider I/O.
	replicaBlob, replicaSignature, err := s.sealConvergentState(state)
	if err != nil {
		return fail(ProblemLocal, "failed", err)
	}
	config = s.currentConfig()
	config.Convergent.Replica = replicaBlob
	config.Convergent.ReplicaSignature = replicaSignature
	if err := s.saveConvergentConfig(config); err != nil {
		return fail(ProblemLocal, "failed", err)
	}

	expected := state
	writes := 0
	remoteAffecting := remoteChanged
	backupRetained := false
	verifiedRemote := convergentRemote{}
	success := false
	var lastErr error
	for round := 1; round <= ConvergentVerifyRounds; round++ {
		kind := roundKind(round)
		s.mergeConvergentInfo(func(info *ConvergentInfo) {
			info.Phase = "verifying"
			info.Round = round
			info.RoundKind = kind
			info.Outcome = "running"
			info.Writes = writes
		})

		fresh, nextToken, readErr := s.readConvergentRemote(ctx, token)
		token = nextToken
		if readErr != nil {
			return fail(problemFor(readErr), "failed", readErr)
		}
		if err := fresh.structuralError(); err != nil {
			return fail(problemFor(err), "blocked", err)
		}
		if fresh.exists() {
			settings = chooseSettings(&fresh.Payload, verified, local)
			joined, _, joinErr := s.joinConvergentRemote(expected, fresh, verified, config, s.now().UnixMilli())
			if joinErr != nil {
				s.markRoundOutcome(round, "retry")
				return result, s.finishConvergentDivergence(joinErr, result)
			}
			if !statesEqual(expected, joined) {
				expected = joined
				remoteAffecting = true
			}
		}

		needsUpload := true
		if fresh.Kind == convergentRemoteV2 && fresh.State != nil {
			if roundSatisfied(round, expected, *fresh.State) && manifestsDominate(*fresh.State, expected) {
				needsUpload = false
				verifiedRemote = fresh
			}
		}
		if needsUpload {
			if fresh.exists() && !backupRetained {
				if err := s.retainRemoteBackup(ctx, fresh.File, "convergent"); err != nil {
					return fail(ProblemBackup, "failed", err)
				}
				backupRetained = true
			}
			content, encodeErr := s.encodeConvergentCloudPayload(expected, settings, config)
			if encodeErr != nil {
				return fail(ProblemLocal, "failed", encodeErr)
			}
			written, nextToken, writeErr := s.writeRemote(ctx, s.activeRemote(), token, fresh.File, content)
			token = nextToken
			writes++
			result.Writes = writes
			if errors.Is(writeErr, ErrConflict) {
				// Another writer won the conditional write. Join it and retry
				// once in this round; a second conflict leaves the round
				// unverified for the next (stronger) round.
				retryRemote, retryToken, retryReadErr := s.readConvergentRemote(ctx, token)
				token = retryToken
				if retryReadErr != nil {
					return fail(problemFor(retryReadErr), "failed", retryReadErr)
				}
				if retryErr := retryRemote.structuralError(); retryErr != nil {
					return fail(problemFor(retryErr), "blocked", retryErr)
				}
				if retryRemote.exists() {
					joined, _, joinErr := s.joinConvergentRemote(expected, retryRemote, verified, config, s.now().UnixMilli())
					if joinErr != nil {
						return result, s.finishConvergentDivergence(joinErr, result)
					}
					if !statesEqual(expected, joined) {
						expected = joined
						remoteAffecting = true
					}
				}
				content, encodeErr = s.encodeConvergentCloudPayload(expected, settings, config)
				if encodeErr != nil {
					return fail(ProblemLocal, "failed", encodeErr)
				}
				written, token, writeErr = s.writeRemote(ctx, s.activeRemote(), token, retryRemote.File, content)
				writes++
				result.Writes = writes
			}
			if writeErr != nil {
				lastErr = writeErr
				s.markRoundOutcome(round, "retry")
				continue
			}
			if written.UpdatedAt == 0 {
				written.UpdatedAt = s.now().UnixMilli()
			}
			written.Content = content
			readBack, readBackToken, readBackErr := s.readConvergentRemote(ctx, token)
			token = readBackToken
			if readBackErr != nil {
				return fail(problemFor(readBackErr), "failed", readBackErr)
			}
			if readBackErr := readBack.structuralError(); readBackErr != nil {
				return fail(problemFor(readBackErr), "blocked", readBackErr)
			}
			if readBack.Kind != convergentRemoteV2 || readBack.State == nil {
				lastErr = fmt.Errorf("%w: provider did not return a v2 payload after the write", ErrConvergentDivergence)
				s.markRoundOutcome(round, "retry")
				continue
			}
			if divergence := domainsync.DetectDivergence(expected, *readBack.State); divergence != nil {
				s.markRoundOutcome(round, "retry")
				return result, s.finishConvergentDivergence(newConvergentDivergence(divergence), result)
			}
			if !roundSatisfied(round, expected, *readBack.State) {
				lastErr = fmt.Errorf("%w: round %d verification failed", ErrConvergentDivergence, round)
				s.markRoundOutcome(round, "retry")
				continue
			}
			verifiedRemote = readBack
		}

		// Join any concurrent superset discovered by the verification read.
		if verifiedRemote.Kind == convergentRemoteV2 && verifiedRemote.State != nil {
			if divergence := domainsync.DetectDivergence(expected, *verifiedRemote.State); divergence != nil {
				s.markRoundOutcome(round, "retry")
				return result, s.finishConvergentDivergence(newConvergentDivergence(divergence), result)
			}
			joined, mergeErr := domainsync.MergeConvergentStates(expected, *verifiedRemote.State)
			if mergeErr != nil {
				return result, s.finishConvergentDivergence(mergeErr, result)
			}
			if !statesEqual(expected, joined) {
				expected = joined
				remoteAffecting = true
				lastErr = fmt.Errorf("%w: round %d discovered concurrent state", ErrConvergentDivergence, round)
				s.markRoundOutcome(round, "retry")
				continue
			}
		}
		s.markRoundOutcome(round, "verified")
		if round == ConvergentVerifyRounds {
			success = true
		}
	}
	if !success {
		if lastErr == nil {
			lastErr = fmt.Errorf("%w: replicas did not converge", ErrConvergentDivergence)
		}
		divergence := &domainsync.ConvergentDivergence{
			Kind:   "unstable-replicas",
			Detail: "the provider did not satisfy all three verification rounds: " + lastErr.Error(),
		}
		info = s.Snapshot().Convergent
		info.Divergence = divergenceInfoFromDomain(divergence)
		info.Phase = "diverged"
		info.Outcome = "diverged"
		info.Writes = writes
		s.setConvergentInfo(info)
		return result, s.finishConvergentDivergence(newConvergentDivergence(divergence), result)
	}

	// Final materialization is applied locally before the next sync may
	// interpret this Vault as edits.
	finalPayload, err := payloadFromState(expected, settings, s.now().UnixMilli(), config.DeviceID)
	if err != nil {
		return fail(ProblemLocal, "failed", err)
	}
	finalSnapshot, err := payloadSnapshot(finalPayload)
	if err != nil {
		return fail(ProblemLocal, "failed", err)
	}
	downloadApplied := false
	if !snapshotsEqual(localSnapshot, finalSnapshot) {
		if err := s.ensureProtectiveBackup(ctx); err != nil {
			return fail(ProblemBackup, "failed", err)
		}
		if err := s.vault.Apply(ctx, finalPayload); err != nil {
			// Persist the verified state with PendingApply so a stale Vault is
			// never mistaken for local deletions on the next run.
			s.persistVerifiedConvergent(config, expected, finalPayload, verifiedRemote, writes, true)
			return fail(ProblemLocal, "failed", err)
		}
		s.notifySnapshotApplied()
		downloadApplied = true
	}

	if err := s.persistVerifiedConvergent(config, expected, finalPayload, verifiedRemote, writes, false); err != nil {
		return fail(ProblemLocal, "failed", err)
	}
	action := domainsync.ActionNone
	if writes > 0 {
		if remoteAffecting || downloadApplied {
			action = domainsync.ActionMerge
		} else {
			action = domainsync.ActionUpload
		}
	} else if remoteAffecting || downloadApplied {
		action = domainsync.ActionDownload
	}
	info = s.Snapshot().Convergent
	info.Phase = "converged"
	info.Outcome = "converged"
	info.Writes = writes
	info.Round = ConvergentVerifyRounds
	info.RoundKind = ConvergentRoundState
	info.ConflictCount = domainsync.ConvergentConflictCount(expected)
	snapshot, _ := domainsync.MaterializeConvergentSnapshot(expected)
	info.EntityCount = snapshotEntityCount(snapshot)
	info.LastVerifiedAt = s.now().UnixMilli()
	info.Divergence = nil
	s.setConvergentInfo(info)
	s.completeSync(action)
	return Result{
		Trigger:    trigger,
		Action:     action,
		Uploaded:   writes > 0,
		Downloaded: remoteChanged || downloadApplied,
		Writes:     writes,
	}, nil
}

func (s *Service) persistVerifiedConvergent(config Config, state domainsync.ConvergentState, payload Payload, remote convergentRemote, writes int, pendingApply bool) error {
	replicaBlob, replicaSignature, err := s.sealConvergentState(state)
	if err != nil {
		return err
	}
	verifiedBlob, verifiedSignature, err := s.sealVerifiedPayload(payload)
	if err != nil {
		return err
	}
	anchorBlob, anchorSignature, err := s.encodeAnchor(context.Background(), payload)
	if err != nil {
		return err
	}
	config = s.currentConfig()
	config.Convergent.SchemaVersion = domainsync.ConvergentSchemaVersion
	config.Convergent.Enabled = true
	config.Convergent.Replica = replicaBlob
	config.Convergent.ReplicaSignature = replicaSignature
	config.Convergent.VerifiedPayload = verifiedBlob
	config.Convergent.VerifiedSignature = verifiedSignature
	config.Convergent.LastVerifiedAt = s.now().UnixMilli()
	config.Convergent.PendingApply = pendingApply
	config.AnchorVersion = AnchorVersion
	config.SyncBase = anchorBlob
	config.BaseSignature = anchorSignature
	config.LastSyncedAt = s.now().UnixMilli()
	config.PendingConflict = nil
	if remote.exists() {
		config.RemoteID = remote.File.ID
		config.RemoteETag = remote.File.ETag
		config.RemoteUpdatedAt = remote.UpdatedAt
	}
	applied, err := s.vault.Snapshot(context.Background())
	if err == nil {
		if signature, sigErr := applied.Signature(); sigErr == nil && !pendingApply {
			config.LocalSignature = signature
		}
	}
	return s.saveConvergentConfig(config)
}

// joinConvergentRemote joins one provider view into state. v2 states are
// joined after divergence detection. A v1 payload written by an older client
// is converted through a three-way merge against the last verified payload and
// applied with a stable synthetic device, so two legacy writers stay
// concurrent instead of being ordered by provider iteration.
func (s *Service) joinConvergentRemote(state domainsync.ConvergentState, remote convergentRemote, verified *Payload, config Config, now int64) (domainsync.ConvergentState, bool, error) {
	switch remote.Kind {
	case convergentRemoteMissing:
		return state, false, nil
	case convergentRemoteV2:
		if remote.State == nil {
			return state, false, fmt.Errorf("%w: provider state is missing", ErrConvergentCorrupt)
		}
		if divergence := domainsync.DetectDivergence(state, *remote.State); divergence != nil {
			return state, false, newConvergentDivergence(divergence)
		}
		merged, err := domainsync.MergeConvergentStates(state, *remote.State)
		if err != nil {
			return state, false, fmt.Errorf("%w: %v", ErrConvergentDivergence, err)
		}
		return merged, !statesEqual(state, merged), nil
	case convergentRemoteV1:
		currentSnapshot := mustMaterializeSnapshot(state)
		if verified == nil {
			remoteSnapshot, err := payloadSnapshot(remote.Payload)
			if err != nil {
				return state, false, fmt.Errorf("%w: %v", ErrConvergentCorrupt, err)
			}
			if snapshotsEqual(currentSnapshot, remoteSnapshot) {
				return state, false, nil
			}
			return state, false, fmt.Errorf("%w: a legacy provider write has no trusted baseline", ErrConvergentDivergence)
		}
		verifiedSnapshot, err := payloadSnapshot(*verified)
		if err != nil {
			return state, false, fmt.Errorf("%w: %v", ErrConvergentCorrupt, err)
		}
		remoteSnapshot, err := payloadSnapshot(remote.Payload)
		if err != nil {
			return state, false, fmt.Errorf("%w: %v", ErrConvergentCorrupt, err)
		}
		if snapshotsEqual(verifiedSnapshot, remoteSnapshot) {
			return state, false, nil
		}
		currentPayload, err := payloadFromState(state, chooseSettings(&remote.Payload, verified, remote.Payload), now, config.DeviceID)
		if err != nil {
			return state, false, err
		}
		merged, err := MergePayloads(verified, currentPayload, remote.Payload, MergeOptions{
			PreserveDeletions: domainsync.IsPreserveDeletionsStrategy(config.Strategy),
		})
		if err != nil {
			return state, false, err
		}
		if len(merged.Conflicts) > 0 {
			return state, false, fmt.Errorf("%w: legacy provider write conflicts with local data (%d changes)", ErrConvergentDivergence, len(merged.Conflicts))
		}
		mergedSnapshot, err := payloadSnapshot(merged.Payload)
		if err != nil {
			return state, false, err
		}
		next, changed, err := domainsync.ApplyConvergentSnapshot(state, mergedSnapshot, legacyNodeID(config, verified), remote.UpdatedAt, false)
		if err != nil {
			return state, false, err
		}
		return next, changed, nil
	case convergentRemoteFuture:
		return state, false, fmt.Errorf("%w: provider schema %d", ErrUnsupportedVersion, remote.SchemaVersion)
	case convergentRemoteInvalid:
		if remote.Code == "envelope-metadata-mismatch" || remote.Code == "missing-envelope" || remote.Code == "damaged-envelope" {
			return state, false, fmt.Errorf("%w: %s", ErrConvergentCorrupt, remote.Detail)
		}
		return state, false, fmt.Errorf("%w: %s", ErrUnsupportedVersion, remote.Detail)
	default:
		return state, false, fmt.Errorf("%w: unknown provider schema", ErrConvergentCorrupt)
	}
}

func (s *Service) encodeConvergentCloudPayload(state domainsync.ConvergentState, settings map[string]json.RawMessage, config Config) ([]byte, error) {
	payload, err := payloadFromState(state, settings, s.now().UnixMilli(), config.DeviceID)
	if err != nil {
		return nil, err
	}
	password := s.passwordValue()
	if password == "" {
		return nil, ErrPasswordRequired
	}
	file, err := domainsync.EncryptPayload(payload, password, domainsync.EncryptOptions{
		Version:           1,
		UpdatedAt:         s.now().UnixMilli(),
		DeviceID:          config.DeviceID,
		DeviceName:        config.DeviceName,
		AppVersion:        config.AppVersion,
		Random:            s.random,
		Now:               s.now,
		SyncSchemaVersion: domainsync.ConvergentSchemaVersion,
	})
	if err != nil {
		return nil, err
	}
	content, err := domainsync.MarshalFile(file)
	if err != nil {
		return nil, err
	}
	return content, nil
}

func (s *Service) finishConvergentDivergence(err error, result Result) error {
	info := s.Snapshot().Convergent
	info.Phase = "diverged"
	info.Outcome = "diverged"
	info.Divergence = divergenceInfoFor(err)
	info.Writes = result.Writes
	s.setConvergentInfo(info)
	s.finishSync(ProblemConvergentDivergence)
	return err
}

func (s *Service) markRoundOutcome(round int, outcome string) {
	s.mergeConvergentInfo(func(info *ConvergentInfo) {
		for index := range info.Rounds {
			if info.Rounds[index].Round == round {
				info.Rounds[index].Outcome = outcome
			}
		}
		info.Round = round
		info.RoundKind = roundKind(round)
		info.Outcome = outcome
	})
}

func roundKind(round int) string {
	switch round {
	case 1:
		return ConvergentRoundManifest
	case 2:
		return ConvergentRoundEntities
	default:
		return ConvergentRoundState
	}
}

// roundSatisfied is the specified comparison for each round.
func roundSatisfied(round int, expected, remote domainsync.ConvergentState) bool {
	expectedManifest := domainsync.BuildConvergentManifest(expected)
	remoteManifest := domainsync.BuildConvergentManifest(remote)
	switch round {
	case 1:
		return domainsync.ManifestDominates(remoteManifest, expectedManifest)
	case 2:
		if !domainsync.ManifestDominates(remoteManifest, expectedManifest) {
			return false
		}
		expectedHashes, err := domainsync.ConvergentEntityHashes(expected)
		if err != nil {
			return false
		}
		remoteHashes, err := domainsync.ConvergentEntityHashes(remote)
		if err != nil {
			return false
		}
		for key, hash := range expectedHashes {
			if remoteHashes[key] != hash {
				return false
			}
		}
		return true
	default:
		expectedHash, err := domainsync.ConvergentStateHash(expected)
		if err != nil {
			return false
		}
		remoteHash, err := domainsync.ConvergentStateHash(remote)
		if err != nil {
			return false
		}
		return expectedHash == remoteHash
	}
}

func manifestsDominate(remote, expected domainsync.ConvergentState) bool {
	return domainsync.ManifestDominates(
		domainsync.BuildConvergentManifest(remote),
		domainsync.BuildConvergentManifest(expected),
	)
}

func statesEqual(left, right domainsync.ConvergentState) bool {
	leftHash, err := domainsync.ConvergentStateHash(left)
	if err != nil {
		return false
	}
	rightHash, err := domainsync.ConvergentStateHash(right)
	return err == nil && leftHash == rightHash
}

func mustMaterializeSnapshot(state domainsync.ConvergentState) domainsync.ConvergentSnapshot {
	snapshot, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		return domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{}}
	}
	return snapshot
}

// preserveUnion keeps every record either side still holds, so a one-sided
// deletion does not propagate under smartMergePreserve.
func preserveUnion(current, local domainsync.ConvergentSnapshot, verified *Payload) domainsync.ConvergentSnapshot {
	union := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{}}
	add := func(snapshot domainsync.ConvergentSnapshot) {
		for family, records := range snapshot.Families {
			existing := map[string]bool{}
			for _, record := range union.Families[family] {
				existing[record.ID] = true
			}
			for _, record := range records {
				if !existing[record.ID] {
					union.Families[family] = append(union.Families[family], record)
					existing[record.ID] = true
				}
			}
		}
	}
	add(current)
	add(local)
	if verified != nil {
		if snapshot, err := payloadSnapshot(*verified); err == nil {
			add(snapshot)
		}
	}
	return union
}

func legacyNodeID(config Config, baseline *Payload) string {
	signature := ""
	if baseline != nil {
		signature, _ = baseline.Signature()
	}
	sum := sha256.Sum256([]byte("legacy\x00" + config.Provider + "\x00" + signature))
	return "legacy-" + hex.EncodeToString(sum[:8])
}

func localOrNil(remote convergentRemote) *Payload {
	if remote.exists() {
		return &remote.Payload
	}
	return nil
}

// convergentDivergenceError keeps both the stable ErrConvergentDivergence
// sentinel (for errors.Is and the UI problem code) and the structured domain
// finding (for the actionable report).
type convergentDivergenceError struct {
	divergence *domainsync.ConvergentDivergence
}

func (e *convergentDivergenceError) Error() string {
	if e == nil || e.divergence == nil {
		return ErrConvergentDivergence.Error()
	}
	return fmt.Sprintf("%s: %s", ErrConvergentDivergence, e.divergence.Error())
}

func (e *convergentDivergenceError) Unwrap() []error {
	if e == nil || e.divergence == nil {
		return []error{ErrConvergentDivergence}
	}
	return []error{ErrConvergentDivergence, e.divergence}
}

func newConvergentDivergence(divergence *domainsync.ConvergentDivergence) error {
	return &convergentDivergenceError{divergence: divergence}
}

// insecureEndpointForToken mirrors syncLocked's plain-HTTP gate so enabling
// convergent sync cannot bypass the endpoint confirmation.
func insecureEndpointForToken(token domainsync.Token) string {
	switch {
	case token.WebDAV != nil && EndpointIsInsecure(token.WebDAV.Endpoint):
		return token.WebDAV.Endpoint
	case token.S3 != nil && EndpointIsInsecure(token.S3.Endpoint):
		return token.S3.Endpoint
	default:
		return ""
	}
}

func convergentProblem(err error) Problem {
	switch {
	case errors.Is(err, ErrWrongPassword):
		return ProblemWrongPassword
	case errors.Is(err, ErrPasswordRequired):
		return ProblemPasswordRequired
	case errors.Is(err, ErrConvergentDivergence):
		return ProblemConvergentDivergence
	case errors.Is(err, ErrUnsupportedVersion):
		return ProblemUnsupported
	case errors.Is(err, ErrConvergentCorrupt):
		return ProblemConvergentCorrupt
	default:
		return ProblemLocal
	}
}

func divergenceInfoFor(err error) *ConvergentDivergenceInfo {
	if err == nil {
		return nil
	}
	info := &ConvergentDivergenceInfo{
		Kind:    "unstable-replicas",
		Detail:  err.Error(),
		Actions: []string{"inspect", "restore", "resync"},
	}
	var divergence *domainsync.ConvergentDivergence
	if errors.As(err, &divergence) {
		return divergenceInfoFromDomain(divergence)
	}
	return info
}

func divergenceInfoFromDomain(divergence *domainsync.ConvergentDivergence) *ConvergentDivergenceInfo {
	if divergence == nil {
		return nil
	}
	info := &ConvergentDivergenceInfo{
		Kind:       divergence.Kind,
		Family:     divergence.Family,
		EntityID:   divergence.EntityID,
		Dot:        divergence.Dot,
		Detail:     divergence.Detail,
		LocalHash:  divergence.LocalHash,
		RemoteHash: divergence.RemoteHash,
		Actions:    []string{"inspect", "restore", "resync"},
	}
	if divergence.LocalVector != nil {
		info.LocalVector = map[string]uint64(divergence.LocalVector)
	}
	if divergence.RemoteVector != nil {
		info.RemoteVector = map[string]uint64(divergence.RemoteVector)
	}
	return info
}
