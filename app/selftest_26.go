//go:build selftest

package app

// Ticket 26 self-test. Press F11, then 2, then 6. The check proves local Vault
// backups through the real SQLite store, encrypted backup adapter, backup
// service, Vault service, Vault state, and Vault view:
//
//   - Automatic backup: a committed Vault mutation reaches the service through
//     the store's change hook and writes an encrypted backup. An identical
//     payload is deduped against the newest entry, which keeps the existing
//     record rather than refreshing it.
//   - Retention and cap: the limit clamps to 1-100, choosing 1 prunes older
//     backups immediately and reports the count, and a snapshot above 25 MiB
//     is refused with skipped-too-large and no file.
//   - Restore barrier: restoring takes a named before_restore backup, applies
//     the snapshot, and returns the Vault to the backed-up state. A failing
//     apply leaves the Vault byte-for-byte unchanged while the protective
//     backup remains, and a tampered backup is rejected without touching it.
//   - Layout: the panel renders at 1100x720 and 720x480 in English and
//     Simplified Chinese without horizontal overflow; the row action stays
//     clear of the scroll bar gutter, and restore and clear are present.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-26.json. Every temporary store, secret
// store, backup service, and directory is closed and removed, so F11,2,6
// passes twice in one process.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	adapterbackup "github.com/mslxl/natcatty/adapters/vaultbackup"
	appvault "github.com/mslxl/natcatty/application/vault"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest26Ticket = 26

// selftest26AutomaticTimeout bounds the wait for the coalescing worker. The
// loop checks the backup list on every iteration; the deadline only prevents
// a hung worker from hanging the self-test.
const selftest26AutomaticTimeout = 5 * time.Second

type selftest26 struct {
	dir         string
	backupDir   string
	db          *store.Store
	secrets     *store.SecretStore
	keyring     *selftest24Keyring
	service     *appvault.Service
	backupStore *adapterbackup.Store
	backups     *appbackup.Service
	state       *vaultstate.State
}

func (t *selftest26) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest26Ticket,
		Name:   "Local Vault backups and the restore barrier",
		Steps: []selftest.Step{
			{Name: "automatic-create-and-dedupe", Run: t.automaticStep},
			{Name: "retention-and-cap", Run: t.retentionStep},
			{Name: "protective-backup-and-restore", Run: t.restoreStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest26) automaticStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	ctx := context.Background()

	first, err := t.backups.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		return t.fail(fmt.Errorf("create the first backup: %w", err))
	}
	if !first.Created || first.Backup.ID == "" {
		return t.fail(fmt.Errorf("the first backup was not written: %+v", first))
	}
	items, err := t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list after the first backup: %w", err))
	}
	if len(items) != 1 {
		return t.fail(fmt.Errorf("backups = %d after the first create, want 1", len(items)))
	}

	// The Vault did not change, so the identical canonical payload must not
	// create a second entry. The existing record is kept as it was.
	second, err := t.backups.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		return t.fail(fmt.Errorf("repeat the identical backup: %w", err))
	}
	if second.Created || second.Backup.ID != first.Backup.ID {
		return t.fail(fmt.Errorf("the identical payload was not deduped: %+v", second))
	}
	items, err = t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list after dedupe: %w", err))
	}
	if len(items) != 1 || items[0].ID != first.Backup.ID {
		return t.fail(fmt.Errorf("dedupe changed the history: %+v", items))
	}
	if t.backups.LastStatus().Kind != appbackup.StatusDeduped {
		return t.fail(fmt.Errorf("dedupe status = %q", t.backups.LastStatus().Kind))
	}

	// A real Vault mutation reaches the service through the store's hook.
	if _, err := t.service.Create(ctx, selftest26Draft("Automatic host")); err != nil {
		return t.fail(fmt.Errorf("create the host for the automatic backup: %w", err))
	}
	if err := t.waitForBackups(ctx, 2); err != nil {
		return t.fail(err)
	}
	items, err = t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list after the automatic backup: %w", err))
	}
	if len(items) != 2 || items[0].Fingerprint == items[1].Fingerprint {
		return t.fail(fmt.Errorf("the automatic backup is missing or duplicated: %+v", items))
	}
	if items[0].Reason != domainbackup.ReasonVaultChange {
		return t.fail(fmt.Errorf("automatic backup reason = %q", items[0].Reason))
	}
	if t.backups.LastStatus().Kind != appbackup.StatusCreated {
		return t.fail(fmt.Errorf("automatic backup status = %q", t.backups.LastStatus().Kind))
	}
	// Restore and retention steps drive explicit backups; an automatic worker
	// must not race their list assertions.
	t.db.SetChangeHook(nil)

	return selftest.Passed(
		"a committed Vault mutation wrote an encrypted backup through the change hook, and an identical payload deduped to the newest existing record",
		"store.SetChangeHook + appbackup.Service.Create + vaultbackup.Store.Save")
}

func (t *selftest26) retentionStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	ctx := context.Background()

	applied, _, err := t.backups.SetRetention(ctx, domainbackup.MinRetention-1)
	if err != nil {
		return t.fail(fmt.Errorf("clamp the low retention bound: %w", err))
	}
	if applied != domainbackup.MinRetention {
		return t.fail(fmt.Errorf("retention %d clamped to %d, want %d",
			domainbackup.MinRetention-1, applied, domainbackup.MinRetention))
	}
	applied, _, err = t.backups.SetRetention(ctx, domainbackup.MaxRetention+1)
	if err != nil {
		return t.fail(fmt.Errorf("clamp the high retention bound: %w", err))
	}
	if applied != domainbackup.MaxRetention {
		return t.fail(fmt.Errorf("retention %d clamped to %d, want %d",
			domainbackup.MaxRetention+1, applied, domainbackup.MaxRetention))
	}
	if _, _, err := t.backups.SetRetention(ctx, domainbackup.MinRetention); err != nil {
		return t.fail(fmt.Errorf("set retention to 1: %w", err))
	}

	// Two distinct payloads with a limit of one: each create writes, then
	// prunes the older entry.
	for index, label := range []string{"Retention one", "Retention two"} {
		if _, err := t.service.Create(ctx, selftest26Draft(label)); err != nil {
			return t.fail(fmt.Errorf("create host %q: %w", label, err))
		}
		result, err := t.backups.Create(ctx, domainbackup.ReasonVaultChange, "", "")
		if err != nil {
			return t.fail(fmt.Errorf("create backup %d: %w", index, err))
		}
		if !result.Created {
			return t.fail(fmt.Errorf("backup %d was not written", index))
		}
	}
	items, err := t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list at retention 1: %w", err))
	}
	if len(items) != 1 {
		return t.fail(fmt.Errorf("retention 1 kept %d backups, want 1", len(items)))
	}
	if pruned := t.backups.LastStatus().Pruned; pruned != 1 {
		return t.fail(fmt.Errorf("the prune count = %d, want 1", pruned))
	}

	// A payload above the 25 MiB cap is refused before anything is written.
	oversized := appbackup.NewService(oversizedSnapshotter{}, t.backupStore,
		appbackup.WithRetentionStore(t.backupStore))
	before := len(items)
	if _, err := oversized.Create(ctx, domainbackup.ReasonVaultChange, "", ""); !errors.Is(err, appbackup.ErrPayloadTooLarge) {
		return t.fail(fmt.Errorf("the oversized create error = %v, want ErrPayloadTooLarge", err))
	}
	if kind := oversized.LastStatus().Kind; kind != appbackup.StatusSkippedTooLarge {
		return t.fail(fmt.Errorf("the oversized create status = %q", kind))
	}
	items, err = t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list after the oversized create: %w", err))
	}
	if len(items) != before {
		return t.fail(fmt.Errorf("the oversized create wrote a backup: %d -> %d", before, len(items)))
	}

	if applied, _, err := t.backups.SetRetention(ctx, domainbackup.DefaultRetention); err != nil ||
		applied != domainbackup.DefaultRetention {
		return t.fail(fmt.Errorf("restore the default retention: applied %d, err %v", applied, err))
	}

	return selftest.Passed(
		"retention clamped at 1 and 100, a limit of 1 pruned the older backup and reported the count, and a payload above 25 MiB was skipped without writing a file",
		"appbackup.Service.SetRetention/Create + vaultbackup.Store.Save")
}

func (t *selftest26) restoreStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	ctx := context.Background()

	if _, err := t.service.Create(ctx, selftest26Draft("Kept host")); err != nil {
		return t.fail(fmt.Errorf("create the host to keep: %w", err))
	}
	baseline, err := t.backups.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil || !baseline.Created {
		return t.fail(fmt.Errorf("write the baseline backup: %+v, %v", baseline, err))
	}
	if _, err := t.service.Create(ctx, selftest26Draft("Discarded host")); err != nil {
		return t.fail(fmt.Errorf("create the host to discard: %w", err))
	}

	if err := t.backups.Restore(ctx, baseline.Backup.ID); err != nil {
		return t.fail(fmt.Errorf("restore the baseline: %w", err))
	}
	if kind := t.backups.LastStatus().Kind; kind != appbackup.StatusRestored {
		return t.fail(fmt.Errorf("restore status = %q", kind))
	}
	labels, err := t.hostLabels(ctx)
	if err != nil {
		return t.fail(err)
	}
	if !labels["Kept host"] || labels["Discarded host"] {
		return t.fail(fmt.Errorf("the restored Vault has the wrong Hosts: %v", labels))
	}
	items, err := t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list after restore: %w", err))
	}
	if len(items) == 0 || items[0].Reason != domainbackup.ReasonBeforeRestore {
		return t.fail(fmt.Errorf("the protective backup is not the newest entry: %+v", items))
	}
	if items[0].Fingerprint == baseline.Backup.Fingerprint {
		return t.fail(fmt.Errorf("the protective backup has the restore target's fingerprint"))
	}
	validID := items[0].ID

	// A failing apply runs after the mandatory protective backup and must
	// leave the Vault exactly as it was.
	if _, err := t.service.Create(ctx, selftest26Draft("After restore host")); err != nil {
		return t.fail(fmt.Errorf("create the post-restore host: %w", err))
	}
	beforeBytes, err := t.vaultBytes(ctx)
	if err != nil {
		return t.fail(err)
	}
	beforeCount := len(items)
	failing := appbackup.NewService(failingRestoreSnapshotter{inner: t.db}, t.backupStore,
		appbackup.WithRetentionStore(t.backupStore))
	if err := failing.Restore(ctx, validID); err == nil {
		return t.fail(errors.New("the injected restore failure was not reported"))
	}
	if kind := failing.LastStatus().Kind; kind != appbackup.StatusRestoreFailed {
		return t.fail(fmt.Errorf("failed restore status = %q", kind))
	}
	afterBytes, err := t.vaultBytes(ctx)
	if err != nil {
		return t.fail(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		return t.fail(errors.New("the failed restore changed the Vault"))
	}
	labels, err = t.hostLabels(ctx)
	if err != nil {
		return t.fail(err)
	}
	if !labels["After restore host"] {
		return t.fail(fmt.Errorf("the failed restore lost the current Vault: %v", labels))
	}
	items, err = t.backups.List(ctx)
	if err != nil {
		return t.fail(fmt.Errorf("list after the failed restore: %w", err))
	}
	if len(items) != beforeCount+1 || items[0].Reason != domainbackup.ReasonBeforeRestore {
		return t.fail(fmt.Errorf("the failed restore did not take its protective backup: %d -> %d, newest %+v",
			beforeCount, len(items), items[0]))
	}

	// A tampered payload is rejected during verification, before any write.
	if err := tamperBackupPayload(t.backupDir, validID); err != nil {
		return t.fail(err)
	}
	if err := t.backups.Restore(ctx, validID); !errors.Is(err, appbackup.ErrCorrupt) {
		return t.fail(fmt.Errorf("the tampered restore error = %v, want ErrCorrupt", err))
	}
	tamperedBytes, err := t.vaultBytes(ctx)
	if err != nil {
		return t.fail(err)
	}
	if string(tamperedBytes) != string(beforeBytes) {
		return t.fail(errors.New("the rejected restore changed the Vault"))
	}

	// Bulk import is the other apply path. The Vault service owns the barrier,
	// so a direct caller cannot bypass it.
	preview, err := t.service.PreviewImport(ctx,
		"Hostname\napplied.example.test\n", appvault.PreviewImportOptions{Format: "csv"})
	if err != nil {
		return t.fail(fmt.Errorf("preview the import behind the barrier: %w", err))
	}
	applied, err := t.service.CommitImport(ctx, preview, appvault.ImportCommitOptions{})
	if err != nil || len(applied.Imported) != 1 {
		return t.fail(fmt.Errorf("commit behind the protective barrier = %+v, %v", applied, err))
	}
	labels, err = t.hostLabels(ctx)
	if err != nil {
		return t.fail(err)
	}
	if !labels["applied.example.test"] {
		return t.fail(fmt.Errorf("the barrier import wrote no Host: %v", labels))
	}
	blocked := appvault.NewService(t.db,
		appvault.WithSecrets(t.secrets),
		appvault.WithImportStore(t.db),
		appvault.WithApplyBarrier(func(context.Context) error {
			return errors.New("selftest: injected barrier failure")
		}),
	)
	blockedPreview, err := blocked.PreviewImport(ctx,
		"Hostname\nblocked.example.test\n", appvault.PreviewImportOptions{Format: "csv"})
	if err != nil {
		return t.fail(fmt.Errorf("preview the blocked import: %w", err))
	}
	_, commitErr := blocked.CommitImport(ctx, blockedPreview, appvault.ImportCommitOptions{})
	if commitErr == nil {
		return t.fail(errors.New("the apply barrier failure was not reported"))
	}
	var barrier *appvault.ApplyBarrierError
	if !errors.As(commitErr, &barrier) {
		return t.fail(fmt.Errorf("the blocked import error = %v, want ApplyBarrierError", commitErr))
	}
	labels, err = t.hostLabels(ctx)
	if err != nil {
		return t.fail(err)
	}
	if labels["blocked.example.test"] {
		return t.fail(errors.New("the blocked import wrote a Host despite the failed barrier"))
	}

	return selftest.Passed(
		"restore took a before_restore backup and applied the snapshot; an injected apply failure and a tampered payload both left the Vault identical; the bulk-import barrier allowed one apply and aborted another before writing",
		"appbackup.Service.Restore/CreateProtective + vaultbackup.Store.Read + store.Store.Transaction + appvault.CommitImport")
}

func (t *selftest26) layoutStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}

	var action, viewport ui.Rect
	var gutter float32
	restoreLayout := vaultui.SelftestWatchBackupLayout(func(a, v ui.Rect, g float32) {
		action, viewport, gutter = a, v, g
	})
	defer restoreLayout()

	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		for _, locale := range []string{"en", "zh-CN"} {
			m := locales.In(locale)
			t.state.BeginBackups()
			panel := ui.NewTester(vaultui.View(t.state), size.width, size.height)
			panel.SetLocale(locale)
			panel.Frame()
			panel.Frame()

			for _, want := range []string{
				m.VaultBackupTitle(), m.VaultBackupRetentionLabel(),
				m.VaultBackupRetentionHint(domainbackup.MinRetention,
					domainbackup.MaxRetention, domainbackup.DefaultRetention),
				m.VaultBackupRestore(), m.VaultBackupClear(),
			} {
				if err := selftest24Visible(panel, want, size.width, size.height); err != nil {
					return t.fail(fmt.Errorf("%dx%d %s: %w", size.width, size.height, locale, err))
				}
			}
			if err := selftest24NoOverflow(panel, size.width, size.height); err != nil {
				return t.fail(fmt.Errorf("%dx%d %s: %w", size.width, size.height, locale, err))
			}
			if gutter <= 0 {
				return t.fail(fmt.Errorf("%dx%d %s: the list reserved no scroll-bar gutter",
					size.width, size.height, locale))
			}
			if action.W <= 0 || viewport.W <= 0 {
				return t.fail(fmt.Errorf("%dx%d %s: the row action or viewport was not laid out: %+v %+v",
					size.width, size.height, locale, action, viewport))
			}
			const epsilon = 0.5
			usableRight := viewport.X + viewport.W - gutter
			if action.X < viewport.X-epsilon || action.X+action.W > usableRight+epsilon {
				return t.fail(fmt.Errorf("%dx%d %s: the row action runs under the scroll bar: %+v, usable right %.1f",
					size.width, size.height, locale, action, usableRight))
			}
			t.state.CancelBackups()
		}
	}

	detail := "the backups panel renders at 1100x720 and 720x480 in English and Simplified Chinese, with restore and clear visible, no horizontal overflow, and row actions clear of the scroll bar"
	t.cleanup()
	return selftest.Passed(detail, "vaultui.View + vaultstate.BeginBackups + ui.Tester")
}

func (t *selftest26) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-26-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	t.dir = dir
	t.backupDir = filepath.Join(dir, "vault-backups")
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, "vault.db"))
	if err != nil {
		t.cleanup()
		return fmt.Errorf("open store: %w", err)
	}
	t.db = db
	t.keyring = &selftest24Keyring{values: map[string][]byte{}}
	secrets, err := store.OpenSecretStore(ctx, db, t.keyring)
	if err != nil {
		t.cleanup()
		return fmt.Errorf("open the field-secret store: %w", err)
	}
	t.secrets = secrets
	backupStore := adapterbackup.Open(t.backupDir, t.keyring)
	t.backupStore = backupStore
	backups := appbackup.NewService(db, backupStore, appbackup.WithRetentionStore(backupStore))
	t.backups = backups
	backups.Start()
	service := appvault.NewService(db,
		appvault.WithSecrets(secrets),
		appvault.WithImportStore(db),
		appvault.WithApplyBarrier(func(ctx context.Context) error {
			_, err := backups.CreateProtective(ctx)
			return err
		}),
	)
	t.service = service
	if _, err := service.Create(ctx, selftest26Draft("Seed host")); err != nil {
		t.cleanup()
		return fmt.Errorf("seed the Vault: %w", err)
	}
	db.SetChangeHook(backups.NotifyVaultChanged)

	state := vaultstate.NewState(service, vaultstate.WithBackups(backups))
	if err := state.Reload(); err != nil {
		t.cleanup()
		return fmt.Errorf("load the Vault: %w", err)
	}
	t.state = state
	return nil
}

func (t *selftest26) waitForBackups(ctx context.Context, want int) error {
	deadline := time.Now().Add(selftest26AutomaticTimeout)
	for {
		items, err := t.backups.List(ctx)
		if err != nil {
			return fmt.Errorf("list while waiting for the automatic backup: %w", err)
		}
		if len(items) >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %d backups: %d present",
				selftest26AutomaticTimeout, want, len(items))
		}
		runtime.Gosched()
	}
}

func (t *selftest26) hostLabels(ctx context.Context) (map[string]bool, error) {
	hosts, err := t.service.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Hosts: %w", err)
	}
	labels := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		labels[host.Label] = true
	}
	return labels, nil
}

func (t *selftest26) vaultBytes(ctx context.Context) ([]byte, error) {
	snapshot, err := t.db.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot the Vault: %w", err)
	}
	data, err := domainbackup.CanonicalBytes(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode the Vault snapshot: %w", err)
	}
	return data, nil
}

func (t *selftest26) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest26) cleanup() {
	if t.backups != nil {
		t.backups.Stop()
	}
	if t.secrets != nil {
		t.secrets.Close()
	}
	if t.db != nil {
		_ = t.db.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.backupDir = "", ""
	t.db, t.secrets, t.keyring = nil, nil, nil
	t.service, t.backupStore, t.backups, t.state = nil, nil, nil, nil
}

func selftest26Draft(label string) domainvault.Draft {
	slug := strings.ToLower(strings.ReplaceAll(label, " ", "-"))
	return domainvault.Draft{
		Label: label, Hostname: slug + ".example.test", Port: 22,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	}
}

// oversizedSnapshotter returns a valid snapshot above the plaintext cap. It
// exists so the self-test exercises the service's cap decision without
// writing 25 MiB through SQLite.
type oversizedSnapshotter struct{}

func (oversizedSnapshotter) Snapshot(context.Context) (domainbackup.Snapshot, error) {
	return domainbackup.Snapshot{
		SchemaVersion: 1,
		Tables: []domainbackup.Table{{
			Name: domainbackup.TableNotes,
			Rows: []domainbackup.Row{{
				"content": domainbackup.Text(strings.Repeat("x", domainbackup.MaxPayloadBytes+1024)),
			}},
		}},
	}, nil
}

func (oversizedSnapshotter) Validate(context.Context, domainbackup.Snapshot) error { return nil }
func (oversizedSnapshotter) Restore(context.Context, domainbackup.Snapshot) error  { return nil }

// failingRestoreSnapshotter passes snapshots and validation through to the
// real SQLite adapter but fails the apply. The service must take the
// protective backup before this seam, and the adapter must leave the database
// untouched when the apply fails.
type failingRestoreSnapshotter struct {
	inner appbackup.Snapshotter
}

func (f failingRestoreSnapshotter) Snapshot(ctx context.Context) (domainbackup.Snapshot, error) {
	return f.inner.Snapshot(ctx)
}

func (f failingRestoreSnapshotter) Validate(ctx context.Context, snapshot domainbackup.Snapshot) error {
	return f.inner.Validate(ctx, snapshot)
}

func (failingRestoreSnapshotter) Restore(context.Context, domainbackup.Snapshot) error {
	return errors.New("selftest: injected restore failure")
}

// tamperBackupPayload flips one ciphertext byte while leaving the cleartext
// envelope intact, so a restore reaches the authenticated decryption and is
// rejected as corrupt.
func tamperBackupPayload(dir, id string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "vault-backup-*-"+id+".json"))
	if err != nil {
		return fmt.Errorf("find the backup file: %w", err)
	}
	if len(matches) != 1 {
		return fmt.Errorf("found %d backup files for %q, want 1", len(matches), id)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return fmt.Errorf("read the backup file: %w", err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		return fmt.Errorf("parse the backup file: %w", err)
	}
	var payload []byte
	if err := json.Unmarshal(record["payload"], &payload); err != nil {
		return fmt.Errorf("decode the backup payload: %w", err)
	}
	if len(payload) == 0 {
		return errors.New("the backup payload is empty")
	}
	payload[len(payload)/2] ^= 0xff
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode the tampered payload: %w", err)
	}
	record["payload"] = encoded
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the tampered record: %w", err)
	}
	if err := os.WriteFile(matches[0], data, 0o600); err != nil {
		return fmt.Errorf("write the tampered backup: %w", err)
	}
	return nil
}
