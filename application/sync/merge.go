package sync

// The merge engine below is a pure, deterministic port of the original app's
// three-way payload merge (domain/syncMerge.ts at 73d15da7). It uses a stored
// base anchor, not timestamps, to decide which side changed an entity, and it
// never performs IO.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ConflictKind is the per-item conflict classification the original's change
// summary used.
type ConflictKind string

const (
	ConflictBothAdded                  ConflictKind = "both-added"
	ConflictBothModified               ConflictKind = "both-modified"
	ConflictLocalDeletedRemoteModified ConflictKind = "local-deleted-remote-modified"
	ConflictRemoteDeletedLocalModified ConflictKind = "remote-deleted-local-modified"
)

// ConflictItem identifies one entity both sides changed. EntityType is the
// payload family name ("hosts", "notes", "settings", ...) and EntityID is the
// entity's ID or path; settings has no ID.
type ConflictItem struct {
	EntityType string       `json:"entityType"`
	EntityID   string       `json:"entityId,omitempty"`
	Kind       ConflictKind `json:"kind"`
}

// EntityChangeCounts is one family's contribution to a merge summary.
type EntityChangeCounts struct {
	AddedLocal     int `json:"addedLocal"`
	AddedRemote    int `json:"addedRemote"`
	DeletedLocal   int `json:"deletedLocal"`
	DeletedRemote  int `json:"deletedRemote"`
	ModifiedLocal  int `json:"modifiedLocal"`
	ModifiedRemote int `json:"modifiedRemote"`
	Conflicts      int `json:"conflicts"`
}

// MergeSummary is the original's MergeSummary, aggregated per family.
type MergeSummary struct {
	AddedLocal     int                           `json:"addedLocal"`
	AddedRemote    int                           `json:"addedRemote"`
	DeletedLocal   int                           `json:"deletedLocal"`
	DeletedRemote  int                           `json:"deletedRemote"`
	ModifiedLocal  int                           `json:"modifiedLocal"`
	ModifiedRemote int                           `json:"modifiedRemote"`
	Conflicts      int                           `json:"conflicts"`
	ByEntity       map[string]EntityChangeCounts `json:"byEntity,omitempty"`
}

// MergeOptions controls how the merge treats one-sided deletions.
type MergeOptions struct {
	// PreserveDeletions is smartMergePreserve: an entity missing from one side
	// is carried forward unless both sides dropped it.
	PreserveDeletions bool
}

// MergeResult is the merged payload plus the conflict detail the UI surfaces.
type MergeResult struct {
	Payload   Payload
	Summary   MergeSummary
	Conflicts []ConflictItem
}

// ErrMerge reports a payload that could not be decoded or encoded. Domain
// entity types marshal without error, so this is effectively a bug guard.
var ErrMerge = errors.New("sync: payload merge failed")

// MergePayloads implements the original mergeSyncPayloads:
//
//   - Only local -> local addition, kept
//   - Only remote -> remote addition, kept
//   - In base, removed on one side -> deletion (or, with preserve, carried
//     forward from the side that still has it)
//   - Modified on one side -> that side wins
//   - Modified on both -> a top-level field merge; a same-field clash keeps
//     the local value and is reported as a conflict
//   - No base -> set union preferring local for duplicate IDs
//
// The result is deterministic for a given (base, local, remote): entity order
// is base order, then local additions, then remote additions; tombstones and
// settings are keyed and sorted. SyncedAt is the maximum input timestamp
// rather than wall-clock time.
func MergePayloads(base *Payload, local, remote Payload, opts MergeOptions) (MergeResult, error) {
	b := Payload{}
	if base != nil {
		b = *base
	}
	result := MergeResult{
		Summary: MergeSummary{ByEntity: map[string]EntityChangeCounts{}},
	}

	hosts, err := mergeFamily("hosts", b.Hosts, local.Hosts, remote.Hosts,
		func(v domainvault.Host) string { return string(v.ID) },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	keys, err := mergeFamily("keys", b.Keys, local.Keys, remote.Keys,
		func(v domainvault.Key) string { return string(v.ID) },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	identities, err := mergeFamily("identities", b.Identities, local.Identities, remote.Identities,
		func(v domainvault.Identity) string { return string(v.ID) },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	proxies, err := mergeFamily("proxyProfiles", b.ProxyProfiles, local.ProxyProfiles, remote.ProxyProfiles,
		func(v domainvault.ProxyProfile) string { return v.ID },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	snippets, err := mergeFamily("snippets", b.Snippets, local.Snippets, remote.Snippets,
		func(v domainvault.Snippet) string { return v.ID },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	groups, err := mergeFamily("groups", b.Groups, local.Groups, remote.Groups,
		func(v domainvault.Group) string { return v.Path },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	packages, err := mergeFamily("snippetPackages", b.SnippetPackages, local.SnippetPackages, remote.SnippetPackages,
		func(v domainvault.SnippetPackage) string { return v.Path },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	notes, err := mergeFamily("notes", b.Notes, local.Notes, remote.Notes,
		func(v domainvault.Note) string { return v.ID },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	noteGroups, err := mergeFamily("noteGroups", b.NoteGroups, local.NoteGroups, remote.NoteGroups,
		func(v domainvault.NoteGroup) string { return v.Path },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}
	rules, err := mergeFamily("portForwardingRules", b.PortForwardingRules, local.PortForwardingRules, remote.PortForwardingRules,
		func(v domainforward.Rule) string { return string(v.ID) },
		local.SyncMeta, remote.SyncMeta, opts)
	if err != nil {
		return MergeResult{}, err
	}

	families := []struct {
		name   string
		counts entityCounts
	}{
		{"hosts", hosts.counts},
		{"keys", keys.counts},
		{"identities", identities.counts},
		{"proxyProfiles", proxies.counts},
		{"snippets", snippets.counts},
		{"groups", groups.counts},
		{"snippetPackages", packages.counts},
		{"notes", notes.counts},
		{"noteGroups", noteGroups.counts},
		{"portForwardingRules", rules.counts},
	}
	for _, family := range families {
		result.Summary.add(family.name, family.counts)
		result.Conflicts = append(result.Conflicts, family.counts.conflicts...)
	}

	payload := Payload{
		Hosts:               hosts.values,
		Keys:                keys.values,
		Identities:          identities.values,
		ProxyProfiles:       proxies.values,
		Snippets:            snippets.values,
		Groups:              groups.values,
		SnippetPackages:     packages.values,
		Notes:               notes.values,
		NoteGroups:          noteGroups.values,
		PortForwardingRules: rules.values,
		Secrets:             mergeSecrets(b.Secrets, local.Secrets, remote.Secrets),
	}

	settings, settingsConflict, err := mergeSettingsPayload(b.Settings, local.Settings, remote.Settings, base == nil)
	if err != nil {
		return MergeResult{}, err
	}
	payload.Settings = settings
	if settingsConflict {
		result.Summary.ByEntity["settings"] = EntityChangeCounts{ModifiedLocal: 1, ModifiedRemote: 1, Conflicts: 1}
		result.Summary.Conflicts++
		result.Conflicts = append(result.Conflicts, ConflictItem{EntityType: "settings", Kind: ConflictBothModified})
	}

	payload.SyncedAt = maxInt64(b.SyncedAt, maxInt64(local.SyncedAt, remote.SyncedAt))
	payload.SyncMeta = mergeSyncMeta(b, local, remote, payload)
	payload.Secrets = filterSecretsToPayload(payload, payload.Secrets)
	sortConflicts(result.Conflicts)
	result.Payload = payload
	return result, nil
}

type entityCounts struct {
	localAdded, remoteAdded       int
	localDeleted, remoteDeleted   int
	localModified, remoteModified int
	conflicts                     []ConflictItem
}

type familyResult[T any] struct {
	values []T
	counts entityCounts
}

func (s *MergeSummary) add(name string, counts entityCounts) {
	s.AddedLocal += counts.localAdded
	s.AddedRemote += counts.remoteAdded
	s.DeletedLocal += counts.localDeleted
	s.DeletedRemote += counts.remoteDeleted
	s.ModifiedLocal += counts.localModified
	s.ModifiedRemote += counts.remoteModified
	s.Conflicts += len(counts.conflicts)
	s.ByEntity[name] = EntityChangeCounts{
		AddedLocal: counts.localAdded, AddedRemote: counts.remoteAdded,
		DeletedLocal: counts.localDeleted, DeletedRemote: counts.remoteDeleted,
		ModifiedLocal: counts.localModified, ModifiedRemote: counts.remoteModified,
		Conflicts: len(counts.conflicts),
	}
}

// record is one entity as JSON fields, so the same three-way logic works for
// every family regardless of its Go struct.
type record struct {
	id     string
	fields map[string]json.RawMessage
}

func mergeFamily[T any](
	name string,
	base, local, remote []T,
	key func(T) string,
	localMeta, remoteMeta *SyncMeta,
	opts MergeOptions,
) (familyResult[T], error) {
	baseRecords, baseByID, err := decodeRecords(base, key)
	if err != nil {
		return familyResult[T]{}, err
	}
	localRecords, localByID, err := decodeRecords(local, key)
	if err != nil {
		return familyResult[T]{}, err
	}
	remoteRecords, remoteByID, err := decodeRecords(remote, key)
	if err != nil {
		return familyResult[T]{}, err
	}

	order := make([]string, 0, len(baseRecords)+len(localRecords)+len(remoteRecords))
	seen := map[string]bool{}
	for _, records := range [][]record{baseRecords, localRecords, remoteRecords} {
		for _, rec := range records {
			if seen[rec.id] {
				continue
			}
			seen[rec.id] = true
			order = append(order, rec.id)
		}
	}

	remoteDeleted := deletedIDs(remoteMeta, name)
	localDeleted := deletedIDs(localMeta, name)
	var result familyResult[T]
	var merged []record
	for _, id := range order {
		baseRec, inBase := baseByID[id]
		localRec, inLocal := localByID[id]
		remoteRec, inRemote := remoteByID[id]

		switch {
		case !opts.PreserveDeletions && !inBase && inLocal && !inRemote && remoteDeleted[id]:
			// A remote tombstone says this absence is intentional. Without a
			// base it is the only durable signal, so do not resurrect it.
			result.counts.remoteDeleted++
		case !opts.PreserveDeletions && !inBase && !inLocal && inRemote && localDeleted[id]:
			result.counts.localDeleted++
		case !inBase && inLocal && !inRemote:
			merged = append(merged, localRec)
			result.counts.localAdded++
		case !inBase && !inLocal && inRemote:
			merged = append(merged, remoteRec)
			result.counts.remoteAdded++
		case !inBase && inLocal && inRemote:
			merged = append(merged, localRec)
			if !recordsEqual(localRec, remoteRec) {
				result.counts.conflicts = append(result.counts.conflicts,
					ConflictItem{EntityType: name, EntityID: id, Kind: ConflictBothAdded})
			}
		case inBase && inLocal && inRemote:
			localChanged := !recordsEqual(localRec, baseRec)
			remoteChanged := !recordsEqual(remoteRec, baseRec)
			switch {
			case !localChanged && !remoteChanged:
				merged = append(merged, baseRec)
			case localChanged && !remoteChanged:
				merged = append(merged, localRec)
				result.counts.localModified++
			case !localChanged && remoteChanged:
				merged = append(merged, remoteRec)
				result.counts.remoteModified++
			default:
				record, clash := mergeRecordFields(baseRec.fields, localRec.fields, remoteRec.fields)
				merged = append(merged, record)
				result.counts.localModified++
				result.counts.remoteModified++
				if clash {
					result.counts.conflicts = append(result.counts.conflicts,
						ConflictItem{EntityType: name, EntityID: id, Kind: ConflictBothModified})
				}
			}
		case inBase && !inLocal && inRemote:
			remoteChanged := !recordsEqual(remoteRec, baseRec)
			switch {
			case remoteChanged:
				// Remote modified while local deleted: keep the modification,
				// the safer choice the original makes.
				merged = append(merged, remoteRec)
				result.counts.conflicts = append(result.counts.conflicts,
					ConflictItem{EntityType: name, EntityID: id, Kind: ConflictLocalDeletedRemoteModified})
			case opts.PreserveDeletions:
				merged = append(merged, remoteRec)
				result.counts.localDeleted++
			default:
				result.counts.localDeleted++
			}
		case inBase && inLocal && !inRemote:
			localChanged := !recordsEqual(localRec, baseRec)
			switch {
			case localChanged:
				merged = append(merged, localRec)
				result.counts.conflicts = append(result.counts.conflicts,
					ConflictItem{EntityType: name, EntityID: id, Kind: ConflictRemoteDeletedLocalModified})
			case opts.PreserveDeletions:
				merged = append(merged, localRec)
				result.counts.remoteDeleted++
			default:
				result.counts.remoteDeleted++
			}
		}
		// inBase && !inLocal && !inRemote: both deleted, gone in every mode.
	}

	result.values = make([]T, 0, len(merged))
	for _, rec := range merged {
		var value T
		data, err := json.Marshal(rec.fields)
		if err != nil {
			return familyResult[T]{}, fmt.Errorf("%w: encode %s %s: %v", ErrMerge, name, rec.id, err)
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return familyResult[T]{}, fmt.Errorf("%w: decode %s %s: %v", ErrMerge, name, rec.id, err)
		}
		result.values = append(result.values, value)
	}
	return result, nil
}

func decodeRecords[T any](values []T, key func(T) string) ([]record, map[string]record, error) {
	records := make([]record, 0, len(values))
	byID := make(map[string]record, len(values))
	order := make(map[string]bool, len(values))
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: encode entity: %v", ErrMerge, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, nil, fmt.Errorf("%w: decode entity: %v", ErrMerge, err)
		}
		rec := record{id: key(value), fields: fields}
		if !order[rec.id] {
			order[rec.id] = true
			records = append(records, rec)
		}
		byID[rec.id] = rec
	}
	return records, byID, nil
}

// recordsEqual compares the canonical JSON of two records. encoding/json sorts
// map keys, so field order and nested object order cannot cause a false diff.
func recordsEqual(a, b record) bool {
	return recordFingerprint(a.fields) == recordFingerprint(b.fields)
}

func recordFingerprint(fields map[string]json.RawMessage) string {
	canonical := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		canonical[key] = canonicalRaw(value)
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	return string(data)
}

// mergeRecordFields is the original's field-level merge: start from the
// remote record and apply every locally changed field. A field changed on
// both sides to different values keeps the local value and reports a clash.
func mergeRecordFields(base, local, remote map[string]json.RawMessage) (record, bool) {
	combined := make(map[string]json.RawMessage, len(remote))
	for key, value := range remote {
		combined[key] = value
	}
	clash := false
	for key, localValue := range local {
		localChanged := !rawEqual(localValue, base[key])
		if !localChanged {
			continue
		}
		combined[key] = localValue
		remoteValue, remoteHas := remote[key]
		baseValue, baseHas := base[key]
		remoteChanged := !rawPresentEqual(remoteValue, remoteHas, baseValue, baseHas)
		if remoteChanged && !rawEqual(localValue, remoteValue) {
			clash = true
		}
	}
	// Fields deleted on both sides disappear; a field deleted only locally is
	// kept from the remote, matching the original's local-key iteration.
	for key := range base {
		if _, inLocal := local[key]; inLocal {
			continue
		}
		if _, inRemote := remote[key]; !inRemote {
			delete(combined, key)
		}
	}
	return record{fields: combined}, clash
}

func rawEqual(a, b json.RawMessage) bool {
	return rawPresentEqual(a, a != nil, b, b != nil)
}

func rawPresentEqual(a json.RawMessage, aPresent bool, b json.RawMessage, bPresent bool) bool {
	if aPresent != bPresent {
		return false
	}
	if !aPresent {
		return true
	}
	return bytes.Equal(canonicalRaw(a), canonicalRaw(b))
}

// canonicalRaw decodes JSON and re-encodes it with sorted object keys and
// exact-number preservation.
func canonicalRaw(raw json.RawMessage) json.RawMessage {
	value, err := decodeAny(raw)
	if err != nil {
		return raw
	}
	data, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return data
}

func decodeAny(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func deletedIDs(meta *SyncMeta, entityType string) map[string]bool {
	if meta == nil {
		return nil
	}
	ids := map[string]bool{}
	for _, deletion := range meta.Deletions {
		if deletion.EntityType == entityType {
			ids[deletion.ID] = true
		}
	}
	return ids
}

func mergeSyncMeta(base, local, remote, merged Payload) *SyncMeta {
	deletions := map[string]SyncDeletion{}
	add := func(meta *SyncMeta) {
		if meta == nil {
			return
		}
		for _, deletion := range meta.Deletions {
			key := deletion.EntityType + "\x00" + deletion.ID
			if previous, ok := deletions[key]; !ok || deletion.DeletedAt >= previous.DeletedAt {
				deletions[key] = deletion
			}
		}
	}
	add(base.SyncMeta)
	add(local.SyncMeta)
	add(remote.SyncMeta)

	present := payloadEntityIDs(merged)
	for key, deletion := range deletions {
		if present[deletion.EntityType+"\x00"+deletion.ID] {
			delete(deletions, key)
		}
	}
	baseIDs := payloadEntityIDs(base)
	for key := range baseIDs {
		if present[key] {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		if _, ok := deletions[key]; !ok {
			deletions[key] = SyncDeletion{EntityType: parts[0], ID: parts[1]}
		}
	}
	if len(deletions) == 0 {
		return nil
	}
	result := &SyncMeta{
		SchemaVersion: 1,
		GeneratedAt:   metaGeneratedAt(base, local, remote),
	}
	if local.SyncMeta != nil && local.SyncMeta.DeviceID != "" {
		result.DeviceID = local.SyncMeta.DeviceID
	}
	for _, deletion := range deletions {
		result.Deletions = append(result.Deletions, deletion)
	}
	sort.Slice(result.Deletions, func(i, j int) bool {
		if result.Deletions[i].EntityType != result.Deletions[j].EntityType {
			return result.Deletions[i].EntityType < result.Deletions[j].EntityType
		}
		return result.Deletions[i].ID < result.Deletions[j].ID
	})
	return result
}

func metaGeneratedAt(payloads ...Payload) int64 {
	var newest int64
	for _, payload := range payloads {
		if payload.SyncMeta != nil && payload.SyncMeta.GeneratedAt > newest {
			newest = payload.SyncMeta.GeneratedAt
		}
	}
	return newest
}

func payloadEntityIDs(payload Payload) map[string]bool {
	ids := map[string]bool{}
	add := func(entityType string, values ...string) {
		for _, value := range values {
			ids[entityType+"\x00"+value] = true
		}
	}
	hosts := make([]string, len(payload.Hosts))
	for i, v := range payload.Hosts {
		hosts[i] = string(v.ID)
	}
	add("hosts", hosts...)
	keys := make([]string, len(payload.Keys))
	for i, v := range payload.Keys {
		keys[i] = string(v.ID)
	}
	add("keys", keys...)
	identities := make([]string, len(payload.Identities))
	for i, v := range payload.Identities {
		identities[i] = string(v.ID)
	}
	add("identities", identities...)
	proxies := make([]string, len(payload.ProxyProfiles))
	for i, v := range payload.ProxyProfiles {
		proxies[i] = v.ID
	}
	add("proxyProfiles", proxies...)
	snippets := make([]string, len(payload.Snippets))
	for i, v := range payload.Snippets {
		snippets[i] = v.ID
	}
	add("snippets", snippets...)
	groups := make([]string, len(payload.Groups))
	for i, v := range payload.Groups {
		groups[i] = v.Path
	}
	add("groups", groups...)
	packages := make([]string, len(payload.SnippetPackages))
	for i, v := range payload.SnippetPackages {
		packages[i] = v.Path
	}
	add("snippetPackages", packages...)
	notes := make([]string, len(payload.Notes))
	for i, v := range payload.Notes {
		notes[i] = v.ID
	}
	add("notes", notes...)
	noteGroups := make([]string, len(payload.NoteGroups))
	for i, v := range payload.NoteGroups {
		noteGroups[i] = v.Path
	}
	add("noteGroups", noteGroups...)
	rules := make([]string, len(payload.PortForwardingRules))
	for i, v := range payload.PortForwardingRules {
		rules[i] = string(v.ID)
	}
	add("portForwardingRules", rules...)
	return ids
}

// mergeSecrets applies the same three-way policy to the plaintext credential
// array. Secrets are keyed by entity, id, and field. The merged payload only
// keeps secrets whose entity survived the merge.
func mergeSecrets(base, local, remote []SecretValue) []SecretValue {
	type secretKey struct{ entityType, entityID, field string }
	makeMap := func(values []SecretValue) ([]secretKey, map[secretKey]SecretValue) {
		order := make([]secretKey, 0, len(values))
		byKey := make(map[secretKey]SecretValue, len(values))
		seen := map[secretKey]bool{}
		for _, value := range values {
			key := secretKey{value.EntityType, value.EntityID, value.Field}
			if !seen[key] {
				seen[key] = true
				order = append(order, key)
			}
			byKey[key] = value
		}
		return order, byKey
	}
	baseOrder, baseMap := makeMap(base)
	localOrder, localMap := makeMap(local)
	remoteOrder, remoteMap := makeMap(remote)
	order := make([]secretKey, 0, len(baseOrder)+len(localOrder)+len(remoteOrder))
	seen := map[secretKey]bool{}
	for _, keys := range [][]secretKey{baseOrder, localOrder, remoteOrder} {
		for _, key := range keys {
			if !seen[key] {
				seen[key] = true
				order = append(order, key)
			}
		}
	}
	var merged []SecretValue
	for _, key := range order {
		baseValue, inBase := baseMap[key]
		localValue, inLocal := localMap[key]
		remoteValue, inRemote := remoteMap[key]
		switch {
		case !inBase && inLocal:
			merged = append(merged, localValue)
		case !inBase && !inLocal && inRemote:
			merged = append(merged, remoteValue)
		case inBase && inLocal && inRemote:
			localChanged := localValue.Value != baseValue.Value
			remoteChanged := remoteValue.Value != baseValue.Value
			switch {
			case !localChanged && remoteChanged:
				merged = append(merged, remoteValue)
			case localChanged:
				merged = append(merged, localValue)
			default:
				merged = append(merged, baseValue)
			}
		case inBase && !inLocal && inRemote:
			if remoteValue.Value != baseValue.Value {
				merged = append(merged, remoteValue)
			}
		case inBase && inLocal && !inRemote:
			if localValue.Value != baseValue.Value {
				merged = append(merged, localValue)
			}
		}
	}
	return merged
}

func filterSecretsToPayload(payload Payload, secrets []SecretValue) []SecretValue {
	if len(secrets) == 0 {
		return nil
	}
	family := map[string]bool{
		"host":          true,
		"ssh_key":       true,
		"identity":      true,
		"proxy_profile": true,
	}
	entities := payloadEntityIDs(payload)
	familyFor := map[string]string{
		"host":          "hosts",
		"ssh_key":       "keys",
		"identity":      "identities",
		"proxy_profile": "proxyProfiles",
	}
	var filtered []SecretValue
	for _, secret := range secrets {
		entityFamily := familyFor[secret.EntityType]
		if !family[secret.EntityType] || !entities[entityFamily+"\x00"+secret.EntityID] {
			continue
		}
		filtered = append(filtered, secret)
	}
	return filtered
}

// mergeSettingsPayload implements the original mergeSettings/mergeSettingsDeep.
// preferRemoteOnConflict is true only when there is no base: a fresh client's
// defaults must not beat the established cloud replica on first merge.
func mergeSettingsPayload(
	base, local, remote map[string]json.RawMessage,
	preferRemoteOnConflict bool,
) (map[string]json.RawMessage, bool, error) {
	if local == nil && remote == nil {
		return nil, false, nil
	}
	if local == nil {
		return remote, false, nil
	}
	if remote == nil {
		return local, false, nil
	}
	baseValue, err := decodeSettingsMap(base)
	if err != nil {
		return nil, false, err
	}
	localValue, err := decodeSettingsMap(local)
	if err != nil {
		return nil, false, err
	}
	remoteValue, err := decodeSettingsMap(remote)
	if err != nil {
		return nil, false, err
	}
	merged, clash := mergeSettingsMaps(baseValue, localValue, remoteValue, preferRemoteOnConflict)
	normalizeAutoClose(merged, localValue, remoteValue)
	if len(merged) == 0 {
		return nil, clash, nil
	}
	encoded, err := encodeSettingsMap(merged)
	if err != nil {
		return nil, false, err
	}
	settingsConflict := clash || !settingsEqual(local, remote)
	if base != nil {
		settingsConflict = settingsConflict && settingsChanged(base, local) && settingsChanged(base, remote)
	}
	return encoded, settingsConflict, nil
}

func decodeSettingsMap(values map[string]json.RawMessage) (map[string]any, error) {
	decoded := make(map[string]any, len(values))
	for key, raw := range values {
		value, err := decodeAny(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: settings key %q: %v", ErrMerge, key, err)
		}
		decoded[key] = value
	}
	return decoded, nil
}

func encodeSettingsMap(values map[string]any) (map[string]json.RawMessage, error) {
	encoded := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("%w: settings key %q: %v", ErrMerge, key, err)
		}
		encoded[key] = data
	}
	return encoded, nil
}

func settingsEqual(a, b map[string]json.RawMessage) bool {
	aValue, aErr := decodeSettingsMap(a)
	bValue, bErr := decodeSettingsMap(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return anyEqual(aValue, bValue)
}

func settingsChanged(base, side map[string]json.RawMessage) bool {
	return !settingsEqual(base, side)
}

func mergeSettingsMaps(base, local, remote map[string]any, preferRemote bool) (map[string]any, bool) {
	keys := map[string]bool{}
	for _, values := range []map[string]any{base, local, remote} {
		for key := range values {
			keys[key] = true
		}
	}
	merged := make(map[string]any, len(keys))
	clash := false
	for key := range keys {
		bValue, bOK := base[key]
		lValue, lOK := local[key]
		rValue, rOK := remote[key]
		if !lOK && !rOK {
			// Both sides deleted this key: they agree, so there is nothing to
			// merge and no conflict. The original drops it silently too.
			continue
		}
		lChanged := !presentEqual(lValue, lOK, bValue, bOK)
		rChanged := !presentEqual(rValue, rOK, bValue, bOK)
		switch {
		case !lChanged && !rChanged:
			if bOK {
				merged[key] = bValue
			}
		case lChanged && !rChanged:
			if lOK {
				merged[key] = lValue
			}
		case !lChanged && rChanged:
			if rOK {
				merged[key] = rValue
			}
		default:
			lObject, lIsObject := plainObject(lValue)
			rObject, rIsObject := plainObject(rValue)
			if lOK && rOK && lIsObject && rIsObject {
				if preferRemote && isEmptyPlainObject(rObject) {
					merged[key] = rObject
					continue
				}
				bObject, _ := plainObject(bValue)
				if bObject == nil {
					bObject = map[string]any{}
				}
				value, childClash := mergeSettingsMaps(bObject, lObject, rObject, preferRemote)
				merged[key] = value
				clash = clash || childClash
				continue
			}
			lArray, lIsArray := lValue.([]any)
			rArray, rIsArray := rValue.([]any)
			if lOK && rOK && lIsArray && rIsArray {
				bArray, _ := bValue.([]any)
				if isIDArray(lArray) || isIDArray(rArray) || isIDArray(bArray) {
					value, arrayClash := mergeIDArrays(bArray, lArray, rArray, preferRemote)
					merged[key] = value
					clash = clash || arrayClash
					continue
				}
			}
			clash = true
			if preferRemote && rOK {
				merged[key] = rValue
			} else if lOK {
				merged[key] = lValue
			}
		}
	}
	return merged, clash
}

func presentEqual(a any, aOK bool, b any, bOK bool) bool {
	if aOK != bOK {
		return false
	}
	if !aOK {
		return true
	}
	return anyEqual(a, b)
}

func anyEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func plainObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func isEmptyPlainObject(value map[string]any) bool {
	return value != nil && len(value) == 0
}

func isIDArray(values []any) bool {
	if len(values) == 0 {
		return false
	}
	first, ok := values[0].(map[string]any)
	if !ok {
		return false
	}
	_, hasID := first["id"]
	return hasID
}

// mergeIDArrays merges an array of objects that carry an "id" field with the
// entity rules. It is used by the settings deep merge for arrays such as
// custom terminal themes.
func mergeIDArrays(base, local, remote []any, preferRemote bool) ([]any, bool) {
	preferred, other := local, remote
	if preferRemote {
		preferred, other = remote, local
	}
	baseRecords, baseByID := genericRecords(base)
	preferredRecords, preferredByID := genericRecords(preferred)
	otherRecords, otherByID := genericRecords(other)
	order := []string{}
	seen := map[string]bool{}
	for _, records := range [][]genericRecord{baseRecords, preferredRecords, otherRecords} {
		for _, rec := range records {
			if seen[rec.id] {
				continue
			}
			seen[rec.id] = true
			order = append(order, rec.id)
		}
	}
	var merged []any
	clash := false
	for _, id := range order {
		baseRec, inBase := baseByID[id]
		prefRec, inPreferred := preferredByID[id]
		otherRec, inOther := otherByID[id]
		switch {
		case !inBase && inPreferred && !inOther:
			merged = append(merged, prefRec.value)
		case !inBase && !inPreferred && inOther:
			merged = append(merged, otherRec.value)
		case !inBase && inPreferred && inOther:
			merged = append(merged, prefRec.value)
			if !anyEqual(prefRec.value, otherRec.value) {
				clash = true
			}
		case inBase && inPreferred && inOther:
			preferredChanged := !anyEqual(prefRec.value, baseRec.value)
			otherChanged := !anyEqual(otherRec.value, baseRec.value)
			switch {
			case !preferredChanged && !otherChanged:
				merged = append(merged, baseRec.value)
			case preferredChanged && !otherChanged:
				merged = append(merged, prefRec.value)
			case !preferredChanged && otherChanged:
				merged = append(merged, otherRec.value)
			default:
				merged = append(merged, prefRec.value)
				if !anyEqual(prefRec.value, otherRec.value) {
					clash = true
				}
			}
		case inBase && !inPreferred && inOther:
			if !anyEqual(otherRec.value, baseRec.value) {
				merged = append(merged, otherRec.value)
				clash = true
			}
		case inBase && inPreferred && !inOther:
			if !anyEqual(prefRec.value, baseRec.value) {
				merged = append(merged, prefRec.value)
				clash = true
			}
		}
	}
	return merged, clash
}

type genericRecord struct {
	id    string
	value any
}

func genericRecords(values []any) ([]genericRecord, map[string]genericRecord) {
	records := make([]genericRecord, 0, len(values))
	byID := make(map[string]genericRecord, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, _ := object["id"].(string)
		rec := genericRecord{id: id, value: value}
		if !seen[id] {
			seen[id] = true
			records = append(records, rec)
		}
		byID[id] = rec
	}
	return records, byID
}

// normalizeAutoClose mirrors the original's migration rule: a three-way merge
// can keep this device's migration flag while adopting a legacy remote
// autoCloseOnExit:true. Unless a side explicitly opted in after migrating, the
// merged value is false.
func normalizeAutoClose(merged, local, remote map[string]any) {
	terminal, ok := merged["terminalSettings"].(map[string]any)
	if !ok {
		return
	}
	if value, _ := terminal["autoCloseOnExit"].(bool); !value {
		return
	}
	if optsIntoAutoClose(local) || optsIntoAutoClose(remote) {
		return
	}
	terminal["autoCloseOnExit"] = false
}

func optsIntoAutoClose(settings map[string]any) bool {
	migrated, _ := settings["autoCloseOnExitDefaultMigrated"].(bool)
	if !migrated {
		return false
	}
	terminal, _ := settings["terminalSettings"].(map[string]any)
	enabled, _ := terminal["autoCloseOnExit"].(bool)
	return enabled
}

func sortConflicts(conflicts []ConflictItem) {
	sort.SliceStable(conflicts, func(i, j int) bool {
		if conflicts[i].EntityType != conflicts[j].EntityType {
			return conflicts[i].EntityType < conflicts[j].EntityType
		}
		if conflicts[i].EntityID != conflicts[j].EntityID {
			return conflicts[i].EntityID < conflicts[j].EntityID
		}
		return conflicts[i].Kind < conflicts[j].Kind
	})
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
