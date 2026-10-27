package sync

// Guard thresholds from the original domain/syncGuards.ts at 73d15da7.
const (
	// BulkShrinkRatio blocks losing at least this fraction of one family.
	BulkShrinkRatio = 0.5
	// BulkShrinkMinAbsolute is the minimum loss for the ratio rule. Deleting
	// one of two hosts is an ordinary edit, not a bulk wipe.
	BulkShrinkMinAbsolute = 3
	// LargeShrinkAbsolute blocks a loss of this many records in one family
	// regardless of the ratio.
	LargeShrinkAbsolute = 10
)

// GuardKind names the protection that blocked a sync.
type GuardKind string

const (
	// GuardShrink is a partial copy that would delete too many records.
	GuardShrink GuardKind = "shrink"
	// GuardTombstone is a whole-Vault deletion, expressed by an empty
	// outgoing payload or a complete tombstone set.
	GuardTombstone GuardKind = "tombstone"
)

// GuardReason is the precise rule that fired, so the UI can localize it.
type GuardReason string

const (
	GuardReasonBulkShrink  GuardReason = "bulk-shrink"
	GuardReasonLargeShrink GuardReason = "large-shrink"
	GuardReasonTombstone   GuardReason = "tombstone"
)

// GuardFinding is a suspicious outgoing payload. The zero value is safe.
type GuardFinding struct {
	Kind          GuardKind   `json:"kind,omitempty"`
	Reason        GuardReason `json:"reason,omitempty"`
	EntityType    string      `json:"entityType,omitempty"`
	BaseCount     int         `json:"baseCount,omitempty"`
	OutgoingCount int         `json:"outgoingCount,omitempty"`
	Lost          int         `json:"lost,omitempty"`
	// ViaRemote reports that the comparison reference was the current cloud
	// copy because no base anchor was available.
	ViaRemote bool `json:"viaRemote,omitempty"`
	// Tombstones is the number of deletion records in the outgoing payload.
	Tombstones int `json:"tombstones,omitempty"`
}

// Suspicious reports whether the guard fired.
func (f GuardFinding) Suspicious() bool { return f.Kind != "" }

// DetectDestructiveSync applies the original shrink rules plus the tombstone
// rule for a payload that would remove the whole Vault.
//
// The reference is the stored base anchor, falling back to the current remote
// copy when the anchor is missing or empty (a first sync, a re-derived key, a
// base that failed to decrypt, or an empty Vault explicitly force-pushed
// earlier). An empty anchor is not a populated reference: treating it as one
// would admit an empty local payload unconditionally after another device has
// repopulated the cloud, wiping that device's data.
func DetectDestructiveSync(outgoing Payload, base *Payload, remote *Payload) GuardFinding {
	reference := base
	viaRemote := false
	if reference == nil || !reference.HasData() {
		if remote != nil && remote.HasData() {
			reference = remote
			viaRemote = true
		}
	}
	if reference == nil || !reference.HasData() {
		return GuardFinding{}
	}
	if finding := detectTombstone(outgoing, *reference, viaRemote); finding.Suspicious() {
		return finding
	}
	return detectShrink(outgoing, *reference, viaRemote)
}

// detectTombstone fires when the outgoing payload would empty the reference
// Vault: every entity family goes to zero while the reference had data. This
// is the classic "restored from an empty backup" wipe. Partial deletions are
// left to the shrink guard, whose thresholds treat the last record in one
// family as an ordinary delete.
func detectTombstone(outgoing, reference Payload, viaRemote bool) GuardFinding {
	referenceTotal := totalEntities(reference)
	if referenceTotal == 0 || totalEntities(outgoing) > 0 {
		return GuardFinding{}
	}
	tombstones := 0
	if outgoing.SyncMeta != nil {
		tombstones = len(outgoing.SyncMeta.Deletions)
	}
	family := ""
	for _, entry := range entityCountsInOriginalOrder(&reference) {
		if entry.count > 0 {
			family = entry.entityType
			break
		}
	}
	return GuardFinding{
		Kind:          GuardTombstone,
		Reason:        GuardReasonTombstone,
		EntityType:    family,
		BaseCount:     referenceTotal,
		OutgoingCount: 0,
		Lost:          referenceTotal,
		ViaRemote:     viaRemote,
		Tombstones:    tombstones,
	}
}

// detectShrink is a direct port of detectSuspiciousShrink, with the original
// threshold order and the group special case. This build's "groups" family is
// the closest analogue of the original's groupConfigs: a group holds shared
// connection defaults, and a hydration race that uploads []groups while other
// families remain would otherwise fall under the bulk/ratio gates.
func detectShrink(outgoing, reference Payload, viaRemote bool) GuardFinding {
	outgoingCounts := countsByFamily(&outgoing)
	referenceCounts := countsByFamily(&reference)
	otherOutgoing := 0
	for _, entry := range entityCountsInOriginalOrder(&outgoing) {
		if entry.entityType != "groups" {
			otherOutgoing += entry.count
		}
	}
	for _, entry := range entityCountsInOriginalOrder(&reference) {
		entityType := entry.entityType
		baseCount := referenceCounts[entityType]
		outgoingCount := outgoingCounts[entityType]
		lost := baseCount - outgoingCount
		if lost <= 0 {
			continue
		}
		if entityType == "groups" && outgoingCount == 0 && baseCount > 0 && otherOutgoing > 0 {
			reason := GuardReasonBulkShrink
			if lost >= LargeShrinkAbsolute {
				reason = GuardReasonLargeShrink
			}
			return GuardFinding{
				Kind: GuardShrink, Reason: reason, EntityType: entityType,
				BaseCount: baseCount, OutgoingCount: outgoingCount, Lost: lost, ViaRemote: viaRemote,
			}
		}
		if lost >= LargeShrinkAbsolute {
			return GuardFinding{
				Kind: GuardShrink, Reason: GuardReasonLargeShrink, EntityType: entityType,
				BaseCount: baseCount, OutgoingCount: outgoingCount, Lost: lost, ViaRemote: viaRemote,
			}
		}
		if baseCount > 0 && float64(lost)/float64(baseCount) >= BulkShrinkRatio && lost >= BulkShrinkMinAbsolute {
			return GuardFinding{
				Kind: GuardShrink, Reason: GuardReasonBulkShrink, EntityType: entityType,
				BaseCount: baseCount, OutgoingCount: outgoingCount, Lost: lost, ViaRemote: viaRemote,
			}
		}
	}
	return GuardFinding{}
}

type familyCount struct {
	entityType string
	count      int
}

func entityCountsInOriginalOrder(payload *Payload) []familyCount {
	return []familyCount{
		{"hosts", len(payload.Hosts)},
		{"keys", len(payload.Keys)},
		{"identities", len(payload.Identities)},
		{"proxyProfiles", len(payload.ProxyProfiles)},
		{"snippets", len(payload.Snippets)},
		{"groups", len(payload.Groups)},
		{"snippetPackages", len(payload.SnippetPackages)},
		{"notes", len(payload.Notes)},
		{"noteGroups", len(payload.NoteGroups)},
		{"portForwardingRules", len(payload.PortForwardingRules)},
	}
}

func countsByFamily(payload *Payload) map[string]int {
	counts := map[string]int{}
	for _, entry := range entityCountsInOriginalOrder(payload) {
		counts[entry.entityType] = entry.count
	}
	return counts
}

func totalEntities(payload Payload) int {
	total := 0
	for _, entry := range entityCountsInOriginalOrder(&payload) {
		total += entry.count
	}
	return total
}
