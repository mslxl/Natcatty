package locales

import (
	"strings"
	"testing"
)

// TestBackupRestoreWordingCoversTheIdenticalProtectiveBackup pins the promise
// the restore barrier can actually keep: the current Vault is protected by a
// backup, which may be a newly written one or an already identical newest
// entry. createLocked dedupes that case, so wording that says a file "is
// written first" would be false. The matching state is covered by
// application/vaultbackup.TestProtectiveBackupDedupesTheCurrentState: a
// restore of the newest identical entry leaves the backup count unchanged.
func TestBackupRestoreWordingCoversTheIdenticalProtectiveBackup(t *testing.T) {
	// One word for "identical" in each shipped catalog. Its presence ties the
	// wording to the state the service reports after a dedupe.
	identical := map[string]string{
		"en":    "identical",
		"es":    "idéntica",
		"ru":    "идентичной",
		"zh-CN": "相同",
		"zh-TW": "相同",
	}

	tags := Tags()
	if len(tags) != len(identical) {
		t.Fatalf("catalogs = %v, add the identical wording for every shipped locale", tags)
	}
	for _, tag := range tags {
		word, ok := identical[tag]
		if !ok {
			t.Fatalf("no identical wording is pinned for %s", tag)
		}
		msgs := In(tag)
		messages := map[string]string{
			"restore confirmation": msgs.VaultBackupRestoreConfirm("2026-10-23 14:05"),
			"unknown confirmation": msgs.VaultBackupRestoreConfirmUnknown(),
			"restored banner":      msgs.VaultBackupRestored(),
		}
		for name, text := range messages {
			if !strings.Contains(text, word) {
				t.Errorf("%s (%s) = %q, missing %q: the protective backup may reuse an identical entry",
					name, tag, text, word)
			}
		}
	}

	if got := In("en").VaultBackupRestoreConfirm("2026-10-23 14:05"); strings.Contains(got, "written first") {
		t.Errorf("the confirmation still claims a file is written first: %q", got)
	}
	if got := In("en").VaultBackupRestored(); strings.Contains(got, "saved as a backup") {
		t.Errorf("the success banner still claims a new file was saved: %q", got)
	}
}
