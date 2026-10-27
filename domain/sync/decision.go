package sync

// Strategy is the user's whole-payload conflict policy. The four values are
// the original app's CloudSyncStrategy union:
//
//   - smartMerge         three-way merge; one-sided deletions propagate
//   - smartMergePreserve three-way merge; a one-sided deletion never propagates
//   - preferCloud        the cloud copy wins
//   - preferLocal        this device's copy wins
//
// The three values StrategyNewest, StrategyLocal, and StrategyRemote are the
// interim strategy names this build persisted before issue 54. They are kept
// only so an existing profile can be migrated; NormalizeStrategy maps them
// onto the canonical four.
type Strategy string

const (
	// StrategySmartMerge is the default: a three-way merge where a deletion
	// observed on one side is propagated to the other.
	StrategySmartMerge Strategy = "smartMerge"
	// StrategySmartMergePreserve is a three-way merge where an entity survives
	// until both sides have dropped it. A partially loaded Vault or a restore
	// from an older backup must not delete cloud data.
	StrategySmartMergePreserve Strategy = "smartMergePreserve"
	// StrategyPreferCloud applies the cloud copy whenever both sides changed.
	StrategyPreferCloud Strategy = "preferCloud"
	// StrategyPreferLocal uploads this device's copy whenever both sides
	// changed.
	StrategyPreferLocal Strategy = "preferLocal"

	// StrategyNewest is the pre-issue-54 default. New configs never store it;
	// NormalizeStrategy migrates it to StrategySmartMerge.
	StrategyNewest Strategy = "newest"
	// StrategyLocal is the pre-issue-54 "this device wins" value.
	StrategyLocal Strategy = "local"
	// StrategyRemote is the pre-issue-54 "the remote wins" value.
	StrategyRemote Strategy = "remote"
)

// DefaultStrategy is the original app's default.
const DefaultStrategy = StrategySmartMerge

// NormalizeStrategy maps unknown and legacy values onto a canonical strategy.
// Legacy "newest" becomes smartMerge, the original default it was standing in
// for; "local" and "remote" keep their meaning as preferLocal/preferCloud.
func NormalizeStrategy(value Strategy) Strategy {
	switch value {
	case StrategySmartMerge, StrategySmartMergePreserve, StrategyPreferCloud, StrategyPreferLocal:
		return value
	case StrategyLocal:
		return StrategyPreferLocal
	case StrategyRemote:
		return StrategyPreferCloud
	default:
		return DefaultStrategy
	}
}

// IsPreserveDeletionsStrategy reports the non-destructive merge variant.
func IsPreserveDeletionsStrategy(value Strategy) bool {
	return NormalizeStrategy(value) == StrategySmartMergePreserve
}

// Merges reports whether the strategy resolves a two-sided change by a
// three-way merge rather than by choosing one whole side.
func Merges(value Strategy) bool {
	switch NormalizeStrategy(value) {
	case StrategySmartMerge, StrategySmartMergePreserve:
		return true
	default:
		return false
	}
}

// Action is the sync step a decision selects.
type Action string

const (
	// ActionNone leaves both copies alone.
	ActionNone Action = "none"
	// ActionUpload publishes the local payload.
	ActionUpload Action = "upload"
	// ActionDownload applies the remote payload.
	ActionDownload Action = "download"
	// ActionMerge combines base, local, and remote with the selected
	// three-way merge strategy.
	ActionMerge Action = "merge"
)

// Decide chooses the next sync action.
//
// localChanged and remoteChanged say whether each side differs from the copy
// observed at the last successful sync. The original's strategies are policy
// choices and its smart merges use the stored base anchor, not wall-clock
// time, so a two-sided change resolves by strategy.
//
// A remote that does not exist is the caller's concern: the service uploads a
// first copy without calling Decide.
func Decide(localChanged, remoteChanged bool, strategy Strategy) Action {
	switch {
	case !localChanged && !remoteChanged:
		return ActionNone
	case localChanged && !remoteChanged:
		return ActionUpload
	case !localChanged && remoteChanged:
		return ActionDownload
	}
	switch NormalizeStrategy(strategy) {
	case StrategyPreferLocal:
		return ActionUpload
	case StrategyPreferCloud:
		return ActionDownload
	default:
		return ActionMerge
	}
}
