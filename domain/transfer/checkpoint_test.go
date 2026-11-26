package transfer

import (
	"errors"
	"strings"
	"testing"
)

// buildCheckpoint appends identities to a fresh v2 checkpoint.
func buildCheckpoint(t *testing.T, identities []string) DirectoryCheckpoint {
	t.Helper()
	checkpoint := CreateEmptyDirectoryCheckpoint()
	for _, identity := range identities {
		var err error
		checkpoint, err = AppendDirectoryCheckpointIdentity(checkpoint, identity)
		if err != nil {
			t.Fatalf("append identity: %v", err)
		}
		// Append advances CoveredEntries; the completed prefix matches it for
		// a sequential walk.
		checkpoint.CompletedEntries++
	}
	return checkpoint
}

// TestDirectoryManifestDetectsChangedAndReorderedEntries proves the fixed
// chain identifies the exact traversal prefix, so a resume cannot skip the
// wrong files after the source changed or reordered.
func TestDirectoryManifestDetectsChangedAndReorderedEntries(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	changed := strings.Repeat("c", 64)

	original := buildCheckpoint(t, []string{first, second})
	if original.Version != 2 {
		t.Fatalf("version = %d, want 2", original.Version)
	}
	if !original.Valid() {
		t.Fatalf("checkpoint is invalid: %+v", original)
	}
	if len(original.ManifestHash) != 64 {
		t.Fatalf("manifest is not fixed width: %q", original.ManifestHash)
	}
	if again := buildCheckpoint(t, []string{first, second}); again.ManifestHash != original.ManifestHash {
		t.Fatalf("the same prefix produced %q and %q", original.ManifestHash, again.ManifestHash)
	}
	if batch := buildCheckpoint(t, []string{first, changed}); batch.ManifestHash == original.ManifestHash {
		t.Fatal("a changed entry kept the manifest")
	}
	if reordered := buildCheckpoint(t, []string{second, first}); reordered.ManifestHash == original.ManifestHash {
		t.Fatal("a reordered prefix kept the manifest")
	}
}

// TestDirectoryCheckpointValidationRefusesUnsafeValues covers malformed
// persisted records and a prefix longer than the plan.
func TestDirectoryCheckpointValidationRefusesUnsafeValues(t *testing.T) {
	valid := buildCheckpoint(t, []string{strings.Repeat("a", 64)})
	for name, checkpoint := range map[string]DirectoryCheckpoint{
		"unknown version": {Version: 3, ManifestHash: valid.ManifestHash},
		"negative cover":  {Version: 2, CoveredEntries: -1, ManifestHash: valid.ManifestHash},
		"completed ahead": {Version: 2, CoveredEntries: 1, CompletedEntries: 2, ManifestHash: valid.ManifestHash},
		"short hash":      {Version: 2, ManifestHash: "abc"},
		"non-hex hash":    {Version: 2, ManifestHash: strings.Repeat("z", 64)},
	} {
		if checkpoint.Valid() {
			t.Errorf("%s was accepted: %+v", name, checkpoint)
		}
	}
	if !VerifyDirectoryCheckpoint(valid, []string{strings.Repeat("a", 64)}) {
		t.Error("a matching plan failed verification")
	}
	if VerifyDirectoryCheckpoint(valid, nil) {
		t.Error("a checkpoint covered more entries than its plan")
	}
	if VerifyDirectoryCheckpoint(valid, []string{strings.Repeat("b", 64)}) {
		t.Error("a changed plan passed verification")
	}
	if !VerifyDirectoryCheckpoint(buildCheckpoint(t, nil), []string{strings.Repeat("a", 64)}) {
		t.Error("an empty checkpoint did not validate an empty prefix")
	}
}

// TestDirectoryEntryIdentityCoversPathSizeAndModification keeps a changed
// source from reusing a completed entry.
func TestDirectoryEntryIdentityCoversPathSizeAndModification(t *testing.T) {
	base := CreateDirectoryEntryIdentity("/srv/a.txt", "/dest/a.txt", 10, 1_700_000_000_000)
	if len(base) != 64 {
		t.Fatalf("identity = %q", base)
	}
	for name, identity := range map[string]string{
		"source": CreateDirectoryEntryIdentity("/srv/b.txt", "/dest/a.txt", 10, 1_700_000_000_000),
		"target": CreateDirectoryEntryIdentity("/srv/a.txt", "/dest/b.txt", 10, 1_700_000_000_000),
		"size":   CreateDirectoryEntryIdentity("/srv/a.txt", "/dest/a.txt", 11, 1_700_000_000_000),
		"mtime":  CreateDirectoryEntryIdentity("/srv/a.txt", "/dest/a.txt", 10, 1_700_000_000_001),
	} {
		if identity == base {
			t.Errorf("a changed %s kept the identity", name)
		}
	}
}

// TestDirectoryTraversalBudgetEnforcesLimitsAndCycles proves the two original
// bounds and the cycle rule: an ancestor is refused, while sibling aliases of
// one canonical directory are both allowed.
func TestDirectoryTraversalBudgetEnforcesLimitsAndCycles(t *testing.T) {
	budget := NewDirectoryTraversalBudget(3, 3)
	ancestors := map[string]struct{}{}
	root, err := budget.Visit("/srv/root", ancestors)
	if err != nil || root == "" {
		t.Fatalf("root claim = %q, %v", root, err)
	}
	if err := budget.Account(2); err != nil {
		t.Fatalf("account: %v", err)
	}
	// Re-entering the same canonical path on the current branch is a cycle.
	if claimed, err := budget.Visit("/srv/root/", ancestors); err != nil || claimed != "" {
		t.Fatalf("cycle claim = %q, %v", claimed, err)
	}
	alias, err := budget.Visit("/srv/shared", ancestors)
	if err != nil || alias == "" {
		t.Fatalf("alias claim = %q, %v", alias, err)
	}
	budget.Release(alias, ancestors)
	second, err := budget.Visit("/srv/shared", ancestors)
	if err != nil || second == "" {
		t.Fatalf("second alias claim = %q, %v", second, err)
	}
	budget.Release(second, ancestors)
	if err := budget.Account(2); !errors.Is(err, ErrDirectoryTraversalEntryLimit) {
		t.Fatalf("entry limit = %v", err)
	}
	if _, err := budget.Visit("/srv/third", ancestors); !errors.Is(err, ErrDirectoryTraversalDirectoryLimit) {
		t.Fatalf("directory limit = %v", err)
	}
	budget.Release(root, ancestors)
	if len(ancestors) != 0 {
		t.Fatalf("ancestors = %v", ancestors)
	}
}

// TestDirectorySymlinkDepthIsEnforced fixes the shared policy.
func TestDirectorySymlinkDepthIsEnforced(t *testing.T) {
	if !ShouldFollowSymlinkDirectory(0) || !ShouldFollowSymlinkDirectory(31) {
		t.Error("a depth below the limit was refused")
	}
	if ShouldFollowSymlinkDirectory(32) || ShouldFollowSymlinkDirectory(33) {
		t.Error("a depth at or beyond the limit was followed")
	}
}
