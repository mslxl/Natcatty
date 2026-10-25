package sync

// Strategy is the user's whole-payload conflict choice. The full three-way
// merge strategies arrive with issue 54; this build compares the payloads'
// timestamps for the newest-wins choice and lets the user pin one side.
type Strategy string

const (
	// StrategyNewest chooses the side whose content changed most recently.
	// Ties go to the remote so two devices converge on the same copy.
	StrategyNewest Strategy = "newest"
	// StrategyLocal always uploads this device's copy.
	StrategyLocal Strategy = "local"
	// StrategyRemote always applies the cloud copy.
	StrategyRemote Strategy = "remote"
)

// NormalizeStrategy maps an unknown value onto the default.
func NormalizeStrategy(value Strategy) Strategy {
	switch value {
	case StrategyLocal, StrategyRemote, StrategyNewest:
		return value
	default:
		return StrategyNewest
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
)

// Decide chooses the next sync action.
//
// localChanged and remoteChanged say whether each side differs from the copy
// observed at the last successful sync. localUpdatedAt and remoteUpdatedAt are
// Unix milliseconds and are consulted only when both sides changed. A remote
// that does not exist is the caller's concern: the service uploads a first
// copy without calling Decide.
func Decide(localChanged, remoteChanged bool, strategy Strategy, localUpdatedAt, remoteUpdatedAt int64) Action {
	switch {
	case !localChanged && !remoteChanged:
		return ActionNone
	case localChanged && !remoteChanged:
		return ActionUpload
	case !localChanged && remoteChanged:
		return ActionDownload
	}
	switch NormalizeStrategy(strategy) {
	case StrategyLocal:
		return ActionUpload
	case StrategyRemote:
		return ActionDownload
	default:
		// Last write wins. A tie goes to the remote so both devices make the
		// same choice without another round trip.
		if localUpdatedAt > remoteUpdatedAt {
			return ActionUpload
		}
		return ActionDownload
	}
}
