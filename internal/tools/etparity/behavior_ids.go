package main

import (
	"fmt"
	"slices"
	"strings"
)

// RequiredBehaviorIDs pins the exact required behavior set per stock server
// version. It is deliberately a literal, independent of the Behaviors table:
// deleting a behavior and its tests shrinks BehaviorsForVersion and the
// recomputed report, but the pinned set still names the missing behavior, so
// `verify` fails instead of silently narrowing the gate.
//
// The counts are part of the gate contract: 25 required behaviors on 7.1.1 and
// 22 on 6.2.10 (6.2.10 lacks the v7 challenge/reset, RETRY_LATER, and
// displacement flows, and instead requires the typed legacy-reattach refusal).
var RequiredBehaviorIDs = map[string][]string{
	v711: {
		"server-identity",
		"bootstrap",
		"auth-password",
		"auth-key",
		"auth-agent",
		"auth-certificate",
		"auth-identity",
		"routing-proxy",
		"routing-jump",
		"framing",
		"secretbox",
		"initial-payload",
		"keepalive",
		"reconnect-network-change",
		"reconnect-client-restart",
		"replay-window",
		"v7-challenge",
		"v7-reset-attach",
		"v7-exit-status",
		"retry-later",
		"displacement",
		"resize",
		"scrollback",
		"golden-terminal-output",
		"error-paths",
	},
	v6210: {
		"server-identity",
		"bootstrap",
		"auth-password",
		"auth-key",
		"auth-agent",
		"auth-certificate",
		"auth-identity",
		"routing-proxy",
		"routing-jump",
		"framing",
		"secretbox",
		"initial-payload",
		"keepalive",
		"reconnect-network-change",
		"reconnect-client-restart",
		"replay-window",
		"legacy-reattach",
		"v7-exit-status",
		"resize",
		"scrollback",
		"golden-terminal-output",
		"error-paths",
	},
}

// pinnedBehaviorIDs returns a copy of the pinned set for a stock version.
func pinnedBehaviorIDs(version string) ([]string, bool) {
	ids, ok := RequiredBehaviorIDs[version]
	if !ok {
		return nil, false
	}
	return slices.Clone(ids), true
}

// compareBehaviorIDs fails unless got names exactly the pinned behavior set
// for version, with no duplicates, omissions, or additions.
func compareBehaviorIDs(version string, got []string) error {
	want, ok := pinnedBehaviorIDs(version)
	if !ok {
		return fmt.Errorf("no pinned behavior set for stock version %q", version)
	}
	seen := make(map[string]bool, len(got))
	var duplicates []string
	for _, id := range got {
		if seen[id] {
			duplicates = append(duplicates, id)
		}
		seen[id] = true
	}
	wantSeen := make(map[string]bool, len(want))
	for _, id := range want {
		wantSeen[id] = true
	}
	var missing, extra []string
	for _, id := range want {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	for _, id := range got {
		if !wantSeen[id] {
			extra = append(extra, id)
		}
	}
	if len(missing) == 0 && len(extra) == 0 && len(duplicates) == 0 {
		return nil
	}
	var details []string
	if len(missing) > 0 {
		details = append(details, "missing "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		details = append(details, "unexpected "+strings.Join(extra, ", "))
	}
	if len(duplicates) > 0 {
		details = append(details, "duplicated "+strings.Join(duplicates, ", "))
	}
	return fmt.Errorf("behavior set for %s is not the pinned set: %s", version, strings.Join(details, "; "))
}

// behaviorIDs returns the IDs in result order.
func behaviorIDs(results []BehaviorResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}
