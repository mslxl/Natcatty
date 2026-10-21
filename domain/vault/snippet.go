package vault

import (
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// SnippetMultiLineRunMode selects how a multi-line Snippet reaches the shell.
// Paste is the default: one write containing the whole command.
type SnippetMultiLineRunMode string

const (
	// SnippetRunPaste sends the command as one write.
	SnippetRunPaste SnippetMultiLineRunMode = "paste"
	// SnippetRunLineDelay submits each line separately, paced like the
	// original AUTO_RUN_SNIPPET_LINE_DELAY_MS.
	SnippetRunLineDelay SnippetMultiLineRunMode = "lineDelay"
)

// Snippet is a reusable command the user can run in a Session. Its Targets,
// TargetGroups, and TargetsAllHosts describe where it may run; VariableValues
// remembers the last values the user typed for its {{variables}}.
type Snippet struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`
	// Package is the slash-separated Snippet package path, or "".
	Package string   `json:"package,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	// Targets lists Host ids. TargetGroups lists Host Group paths resolved
	// against the latest Host inventory when the Snippet runs. TargetsAllHosts
	// means every connectable Host.
	Targets         []model.HostID `json:"targets,omitempty"`
	TargetGroups    []string       `json:"targetGroups,omitempty"`
	TargetsAllHosts bool           `json:"targetsAllHosts,omitempty"`
	// NoAutoRun pastes the command without a trailing Enter.
	NoAutoRun bool `json:"noAutoRun,omitempty"`
	// MultiLineRunMode defaults to paste.
	MultiLineRunMode SnippetMultiLineRunMode `json:"multiLineRunMode,omitempty"`
	// VariableValues remembers the values the user entered, keyed by variable
	// name. It is not part of the editable draft.
	VariableValues map[string]string `json:"variableValues,omitempty"`
	OrderKey       int64             `json:"orderKey"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// SnippetDraft is the editable part of a Snippet.
type SnippetDraft struct {
	Label            string
	Command          string
	Package          string
	Tags             []string
	Targets          []model.HostID
	TargetGroups     []string
	TargetsAllHosts  bool
	NoAutoRun        bool
	MultiLineRunMode SnippetMultiLineRunMode
}

// Limits keep a pasted command or a sprawling target list out of the Vault
// while staying generous for real use.
const (
	maxSnippetCommandLength = 64 << 10
	maxSnippetTargets       = 512
	maxSnippetGroups        = 256
)

// NormalizeSnippetDraft trims and validates a Snippet draft. An all-hosts
// Snippet drops its explicit targets, because "all Hosts" already wins and a
// stale list would reappear if the flag were later cleared.
func NormalizeSnippetDraft(draft SnippetDraft) (SnippetDraft, error) {
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Package = strings.TrimSpace(draft.Package)
	draft.Tags = normalizeTags(draft.Tags)

	if draft.Label == "" {
		return SnippetDraft{}, validation(FieldSnippet, RuleRequired)
	}
	if err := validateText(FieldSnippet, draft.Label, maxLabelLength, false); err != nil {
		return SnippetDraft{}, err
	}
	if len(draft.Command) > maxSnippetCommandLength {
		return SnippetDraft{}, validation(FieldSnippetCommand, RuleTooLong)
	}
	normalizedPackage, err := NormalizeSnippetPackagePath(draft.Package)
	if err != nil {
		return SnippetDraft{}, err
	}
	draft.Package = normalizedPackage

	switch draft.MultiLineRunMode {
	case "":
		draft.MultiLineRunMode = SnippetRunPaste
	case SnippetRunPaste, SnippetRunLineDelay:
	default:
		return SnippetDraft{}, validation(FieldSnippet, RuleUnsupported)
	}

	if draft.TargetsAllHosts {
		draft.Targets = nil
		draft.TargetGroups = nil
		return draft, nil
	}
	draft.Targets = normalizeSnippetTargets(draft.Targets)
	draft.TargetGroups = normalizeSnippetTargetGroups(draft.TargetGroups)
	return draft, nil
}

// SnippetHasRunTargets reports whether the Snippet has a runnable scope:
// explicit targets, target groups, or the all-hosts flag.
func SnippetHasRunTargets(snippet Snippet) bool {
	if snippet.TargetsAllHosts {
		return true
	}
	return len(snippet.Targets) > 0 || len(snippet.TargetGroups) > 0
}

// SnippetAppliesToHost reports whether a Snippet may run against a Host.
func SnippetAppliesToHost(snippet Snippet, host SnippetTargetHost) bool {
	if snippet.TargetsAllHosts {
		return true
	}
	for _, id := range snippet.Targets {
		if id == host.ID {
			return true
		}
	}
	return SnippetTargetsHostGroup(snippet, host)
}

// SnippetTargetsHostGroup reports whether one of the Snippet's target group
// paths contains the Host.
func SnippetTargetsHostGroup(snippet Snippet, host SnippetTargetHost) bool {
	for _, path := range snippet.TargetGroups {
		if IsGroupAtOrBelow(host.Group, path) {
			return true
		}
	}
	return false
}

// SnippetTargetHost is the Host data target matching needs.
type SnippetTargetHost struct {
	ID    model.HostID
	Group string
}

// SnippetCanRunInTerminal reports whether a Snippet may run in a Session
// connected to host. An unscoped Snippet runs anywhere; a scoped one runs only
// where it applies.
func SnippetCanRunInTerminal(snippet Snippet, host SnippetTargetHost) bool {
	if !SnippetHasRunTargets(snippet) {
		return true
	}
	return SnippetAppliesToHost(snippet, host)
}

// SnippetRunnableHosts returns the Hosts a manual run targets: every
// connectable Host when the Snippet is all-hosts, otherwise the explicitly
// targeted and group-matched Hosts, in inventory order.
func SnippetRunnableHosts(snippet Snippet, hosts []Host) []Host {
	runnable := make([]Host, 0, len(hosts))
	for _, host := range hosts {
		if snippet.TargetsAllHosts {
			runnable = append(runnable, host)
			continue
		}
		if SnippetAppliesToHost(snippet, SnippetTargetHost{ID: host.ID, Group: host.Group}) {
			runnable = append(runnable, host)
		}
	}
	return runnable
}

// RemapSnippetTargetGroups rewrites target group paths after a Host Group
// rename, following descendants and dropping duplicates.
func RemapSnippetTargetGroups(snippet Snippet, oldPath, newPath string) Snippet {
	oldPath, newPath = NormalizeGroup(oldPath), NormalizeGroup(newPath)
	if oldPath == "" || newPath == "" || oldPath == newPath || len(snippet.TargetGroups) == 0 {
		return snippet
	}
	remapped := make([]string, 0, len(snippet.TargetGroups))
	seen := make(map[string]bool, len(snippet.TargetGroups))
	for _, path := range snippet.TargetGroups {
		if IsGroupAtOrBelow(path, oldPath) {
			path = ReplaceGroupPrefix(path, oldPath, newPath)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		remapped = append(remapped, path)
	}
	snippet.TargetGroups = remapped
	return snippet
}

// RemoveSnippetTargetGroups drops target group paths at or below a deleted
// Host Group. The Snippet itself is kept.
func RemoveSnippetTargetGroups(snippet Snippet, path string) Snippet {
	path = NormalizeGroup(path)
	if path == "" || len(snippet.TargetGroups) == 0 {
		return snippet
	}
	kept := make([]string, 0, len(snippet.TargetGroups))
	for _, candidate := range snippet.TargetGroups {
		if IsGroupAtOrBelow(candidate, path) {
			continue
		}
		kept = append(kept, candidate)
	}
	if len(kept) == 0 {
		snippet.TargetGroups = nil
	} else {
		snippet.TargetGroups = kept
	}
	return snippet
}

func normalizeSnippetTargets(targets []model.HostID) []model.HostID {
	if len(targets) == 0 {
		return nil
	}
	seen := make(map[model.HostID]bool, len(targets))
	out := make([]model.HostID, 0, len(targets))
	for _, target := range targets {
		target = model.HostID(strings.TrimSpace(string(target)))
		if target == "" || seen[target] {
			continue
		}
		if len(out) == maxSnippetTargets {
			break
		}
		seen[target] = true
		out = append(out, target)
	}
	return out
}

func normalizeSnippetTargetGroups(groups []string) []string {
	if len(groups) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(groups))
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		group = NormalizeGroup(group)
		if group == "" || seen[group] {
			continue
		}
		if len(out) == maxSnippetGroups {
			break
		}
		seen[group] = true
		out = append(out, group)
	}
	return out
}
