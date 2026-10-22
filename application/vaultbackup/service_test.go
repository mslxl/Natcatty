package vaultbackup

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

const testSchemaVersion = 10

func hostSnapshot(label string) domainbackup.Snapshot {
	return domainbackup.Snapshot{
		SchemaVersion: testSchemaVersion,
		Tables: []domainbackup.Table{{
			Name: domainbackup.TableHosts,
			Rows: []domainbackup.Row{{
				"id":   domainbackup.Text(label),
				"data": domainbackup.Text(`{"id":"` + label + `"}`),
			}},
		}},
	}
}

type fakeSnapshotter struct {
	mu            sync.Mutex
	current       domainbackup.Snapshot
	validateErr   error
	restoreErr    error
	validateCalls int
	restoreCalls  int
}

func (f *fakeSnapshotter) Snapshot(context.Context) (domainbackup.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current, nil
}

func (f *fakeSnapshotter) Validate(context.Context, domainbackup.Snapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validateCalls++
	return f.validateErr
}

func (f *fakeSnapshotter) Restore(_ context.Context, snapshot domainbackup.Snapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCalls++
	if f.restoreErr != nil {
		return f.restoreErr
	}
	f.current = snapshot
	return nil
}

func (f *fakeSnapshotter) set(snapshot domainbackup.Snapshot) {
	f.mu.Lock()
	f.current = snapshot
	f.mu.Unlock()
}

func (f *fakeSnapshotter) calls() (validate, restore int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validateCalls, f.restoreCalls
}

type fakeStore struct {
	mu        sync.Mutex
	entries   []domainbackup.Summary
	payloads  map[string][]byte
	available bool
	saveErr   error
	readErr   error
	saves     chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{available: true, payloads: map[string][]byte{}, saves: make(chan struct{}, 16)}
}

func (f *fakeStore) List(context.Context) ([]domainbackup.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := append([]domainbackup.Summary(nil), f.entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt != entries[j].CreatedAt {
			return entries[i].CreatedAt > entries[j].CreatedAt
		}
		return entries[i].ID > entries[j].ID
	})
	return entries, nil
}

func (f *fakeStore) Save(_ context.Context, input SaveInput) (domainbackup.Summary, error) {
	f.mu.Lock()
	if f.saveErr != nil {
		f.mu.Unlock()
		return domainbackup.Summary{}, f.saveErr
	}
	summary := domainbackup.Summary{
		ID: input.ID, CreatedAt: input.CreatedAt, Reason: input.Reason,
		Fingerprint: input.Fingerprint, SizeBytes: int64(len(input.Payload)),
		Counts: input.Counts, SourceAppVersion: input.SourceAppVersion,
		TargetAppVersion: input.TargetAppVersion,
	}
	f.entries = append(f.entries, summary)
	f.payloads[input.ID] = append([]byte(nil), input.Payload...)
	f.mu.Unlock()
	select {
	case f.saves <- struct{}{}:
	default:
	}
	return summary, nil
}

func (f *fakeStore) Read(_ context.Context, id string) (domainbackup.Summary, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return domainbackup.Summary{}, nil, f.readErr
	}
	for _, entry := range f.entries {
		if entry.ID == id {
			return entry, append([]byte(nil), f.payloads[id]...), nil
		}
	}
	return domainbackup.Summary{}, nil, ErrNotFound
}

func (f *fakeStore) Prune(_ context.Context, keep int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sort.Slice(f.entries, func(i, j int) bool {
		if f.entries[i].CreatedAt != f.entries[j].CreatedAt {
			return f.entries[i].CreatedAt > f.entries[j].CreatedAt
		}
		return f.entries[i].ID > f.entries[j].ID
	})
	if keep < 0 {
		keep = 0
	}
	if keep >= len(f.entries) {
		return 0, nil
	}
	removed := len(f.entries) - keep
	for _, entry := range f.entries[keep:] {
		delete(f.payloads, entry.ID)
	}
	f.entries = f.entries[:keep]
	return removed, nil
}

func (f *fakeStore) Clear(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := len(f.entries)
	f.entries = nil
	f.payloads = map[string][]byte{}
	return count, nil
}

func (f *fakeStore) EncryptionAvailable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

func (f *fakeStore) payload(id string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.payloads[id]...)
}

func (f *fakeStore) corrupt(id string) {
	f.mu.Lock()
	f.payloads[id] = []byte("not a snapshot")
	f.mu.Unlock()
}

type fakeRetention struct {
	mu    sync.Mutex
	value int
	saves []int
	err   error
}

func (f *fakeRetention) Load() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.value == 0 {
		return domainbackup.DefaultRetention
	}
	return f.value
}

func (f *fakeRetention) SaveRetention(value int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.value = value
	f.saves = append(f.saves, value)
	return nil
}

type testService struct {
	service     *Service
	store       *fakeStore
	snapshotter *fakeSnapshotter
	retention   *fakeRetention
	now         time.Time
	nextID      int
}

func newTestService(t *testing.T, snapshot domainbackup.Snapshot, opts ...Option) *testService {
	t.Helper()
	graph := &testService{
		store:       newFakeStore(),
		snapshotter: &fakeSnapshotter{current: snapshot},
		retention:   &fakeRetention{},
		now:         time.UnixMilli(1_700_000_000_000),
	}
	all := []Option{
		WithClock(func() time.Time { return graph.now }),
		WithIDGenerator(func() string {
			graph.nextID++
			return "id-" + string(rune('a'+graph.nextID-1))
		}),
		WithRetentionStore(graph.retention),
	}
	all = append(all, opts...)
	graph.service = NewService(graph.snapshotter, graph.store, all...)
	return graph
}

func (g *testService) advance(d time.Duration) { g.now = g.now.Add(d) }

func TestCreateAndFingerprintDedupeKeepsTheExistingEntry(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	ctx := context.Background()

	first, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if !first.Created || graph.store.count() != 1 {
		t.Fatalf("first backup = %+v, count = %d", first, graph.store.count())
	}
	original := first.Backup

	graph.advance(time.Hour)
	second, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatalf("duplicate Create: %v", err)
	}
	if second.Created {
		t.Fatal("an identical payload created a second backup")
	}
	if second.Backup != original {
		t.Fatalf("dedupe refreshed the entry: before %+v, after %+v", original, second.Backup)
	}
	if graph.service.LastStatus().Kind != StatusDeduped {
		t.Fatalf("status = %q, want deduped", graph.service.LastStatus().Kind)
	}
	if graph.store.count() != 1 {
		t.Fatalf("count = %d, want 1", graph.store.count())
	}
}

func TestFingerprintDedupeComparesOnlyTheNewestEntry(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	ctx := context.Background()
	if _, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", ""); err != nil {
		t.Fatal(err)
	}
	graph.advance(time.Minute)
	graph.snapshotter.set(hostSnapshot("h2"))
	if _, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", ""); err != nil {
		t.Fatal(err)
	}
	graph.advance(time.Minute)
	graph.snapshotter.set(hostSnapshot("h1"))
	result, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || graph.store.count() != 3 {
		t.Fatalf("returning to an older payload should create a new backup: %+v count=%d",
			result, graph.store.count())
	}
}

func TestRetentionBoundaries(t *testing.T) {
	t.Run("minimum keeps one", func(t *testing.T) {
		graph := newTestService(t, hostSnapshot("h1"))
		ctx := context.Background()
		if _, _, err := graph.service.SetRetention(ctx, 1); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 3; index++ {
			graph.advance(time.Minute)
			graph.snapshotter.set(hostSnapshot("h" + string(rune('a'+index))))
			result, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if index > 0 && result.Pruned == 0 {
				t.Fatalf("create %d reported no prune", index)
			}
		}
		if graph.store.count() != 1 {
			t.Fatalf("count = %d, want 1", graph.store.count())
		}
	})

	t.Run("maximum keeps one hundred", func(t *testing.T) {
		graph := newTestService(t, hostSnapshot("h0"))
		ctx := context.Background()
		if _, _, err := graph.service.SetRetention(ctx, 100); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 101; index++ {
			graph.advance(time.Minute)
			graph.snapshotter.set(hostSnapshot("h" + string(rune(index%26+'a')) + string(rune(index/26+'a'))))
			if _, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", ""); err != nil {
				t.Fatal(err)
			}
		}
		if graph.store.count() != 100 {
			t.Fatalf("count = %d, want 100", graph.store.count())
		}
	})

	t.Run("default keeps twenty", func(t *testing.T) {
		graph := newTestService(t, hostSnapshot("h0"))
		ctx := context.Background()
		for index := 0; index < 25; index++ {
			graph.advance(time.Minute)
			graph.snapshotter.set(hostSnapshot("h" + string(rune(index%26+'a')) + string(rune(index/26+'a'))))
			if _, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", ""); err != nil {
				t.Fatal(err)
			}
		}
		if graph.store.count() != domainbackup.DefaultRetention {
			t.Fatalf("count = %d, want %d", graph.store.count(), domainbackup.DefaultRetention)
		}
	})

	t.Run("set retention prunes immediately and persists", func(t *testing.T) {
		graph := newTestService(t, hostSnapshot("h1"))
		ctx := context.Background()
		for index := 0; index < 5; index++ {
			graph.advance(time.Minute)
			graph.snapshotter.set(hostSnapshot("h" + string(rune('a'+index))))
			if _, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", ""); err != nil {
				t.Fatal(err)
			}
		}
		applied, pruned, err := graph.service.SetRetention(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if applied != 2 || pruned != 3 {
			t.Fatalf("SetRetention = %d/%d, want 2/3", applied, pruned)
		}
		if graph.store.count() != 2 {
			t.Fatalf("count = %d, want 2", graph.store.count())
		}
		if len(graph.retention.saves) == 0 || graph.retention.saves[len(graph.retention.saves)-1] != 2 {
			t.Fatalf("retention saves = %v", graph.retention.saves)
		}
	})
}

func TestPayloadCapRejectsBeforeWriting(t *testing.T) {
	huge := domainbackup.Snapshot{
		SchemaVersion: testSchemaVersion,
		Tables: []domainbackup.Table{{
			Name: domainbackup.TableHosts,
			Rows: []domainbackup.Row{{
				"id":   domainbackup.Text("h1"),
				"data": domainbackup.Text(strings.Repeat("x", domainbackup.MaxPayloadBytes+1)),
			}},
		}},
	}
	graph := newTestService(t, huge)
	result, err := graph.service.Create(context.Background(), domainbackup.ReasonVaultChange, "", "")
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("Create = %v, want ErrPayloadTooLarge", err)
	}
	if result.Created || graph.store.count() != 0 {
		t.Fatalf("oversized backup was written: %+v count=%d", result, graph.store.count())
	}
	if graph.service.LastStatus().Kind != StatusSkippedTooLarge {
		t.Fatalf("status = %q, want skipped_too_large", graph.service.LastStatus().Kind)
	}
}

func TestProtectiveBackupIsMandatoryBeforeRestore(t *testing.T) {
	graph := newTestService(t, hostSnapshot("old"))
	ctx := context.Background()
	created, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatal(err)
	}
	graph.snapshotter.set(hostSnapshot("current"))

	graph.store.mu.Lock()
	graph.store.saveErr = errors.New("disk full")
	graph.store.mu.Unlock()

	err = graph.service.Restore(ctx, created.Backup.ID)
	var protective *ProtectiveBackupError
	if !errors.As(err, &protective) {
		t.Fatalf("Restore = %v, want ProtectiveBackupError", err)
	}
	if _, restores := graph.snapshotter.calls(); restores != 0 {
		t.Fatalf("restore ran without the protective backup (%d calls)", restores)
	}
	current, _ := graph.snapshotter.Snapshot(ctx)
	if current.Tables[0].Rows[0]["id"].Text != "current" {
		t.Fatal("the Vault changed after an aborted restore")
	}
	if graph.service.LastStatus().Kind != StatusRestoreFailed {
		t.Fatalf("status = %q, want restore_failed", graph.service.LastStatus().Kind)
	}
}

func TestRestoreSucceedsAndKeepsAPreRestorePoint(t *testing.T) {
	graph := newTestService(t, hostSnapshot("old"))
	ctx := context.Background()
	created, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatal(err)
	}
	graph.snapshotter.set(hostSnapshot("current"))

	if err := graph.service.Restore(ctx, created.Backup.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	current, _ := graph.snapshotter.Snapshot(ctx)
	if current.Tables[0].Rows[0]["id"].Text != "old" {
		t.Fatal("the Vault was not restored")
	}
	if graph.store.count() != 2 {
		t.Fatalf("count = %d, want the original plus the protective backup", graph.store.count())
	}
	list, _ := graph.service.List(ctx)
	reasons := map[domainbackup.Reason]bool{}
	for _, entry := range list {
		reasons[entry.Reason] = true
	}
	if !reasons[domainbackup.ReasonBeforeRestore] {
		t.Fatalf("no before_restore backup: %+v", list)
	}
	if graph.service.LastStatus().Kind != StatusRestored {
		t.Fatalf("status = %q, want restored", graph.service.LastStatus().Kind)
	}
}

func TestFailedRestoreLeavesTheVaultUnchanged(t *testing.T) {
	graph := newTestService(t, hostSnapshot("old"))
	ctx := context.Background()
	created, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatal(err)
	}
	graph.snapshotter.set(hostSnapshot("current"))
	graph.snapshotter.mu.Lock()
	graph.snapshotter.restoreErr = errors.New("constraint failed")
	graph.snapshotter.mu.Unlock()

	if err := graph.service.Restore(ctx, created.Backup.ID); err == nil {
		t.Fatal("Restore hid the store failure")
	}
	current, _ := graph.snapshotter.Snapshot(ctx)
	if current.Tables[0].Rows[0]["id"].Text != "current" {
		t.Fatal("a failed restore did not roll back")
	}
	if graph.service.LastStatus().Kind != StatusRestoreFailed {
		t.Fatalf("status = %q, want restore_failed", graph.service.LastStatus().Kind)
	}
	// The protective backup exists even though the apply failed, so the user
	// can still recover the pre-restore state.
	if graph.store.count() != 2 {
		t.Fatalf("count = %d, want 2", graph.store.count())
	}
}

func TestRestoreRejectsCorruptAndIncompatiblePayloads(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		graph := newTestService(t, hostSnapshot("old"))
		ctx := context.Background()
		created, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
		if err != nil {
			t.Fatal(err)
		}
		graph.store.corrupt(created.Backup.ID)
		if err := graph.service.Restore(ctx, created.Backup.ID); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Restore = %v, want ErrCorrupt", err)
		}
		if _, restores := graph.snapshotter.calls(); restores != 0 {
			t.Fatal("a corrupt backup reached the store")
		}
	})

	t.Run("incompatible", func(t *testing.T) {
		graph := newTestService(t, hostSnapshot("old"))
		ctx := context.Background()
		created, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
		if err != nil {
			t.Fatal(err)
		}
		graph.snapshotter.validateErr = errors.New("schema mismatch")
		if err := graph.service.Restore(ctx, created.Backup.ID); !errors.Is(err, ErrIncompatibleSnapshot) {
			t.Fatalf("Restore = %v, want ErrIncompatibleSnapshot", err)
		}
		if _, restores := graph.snapshotter.calls(); restores != 0 {
			t.Fatal("an incompatible snapshot reached the store")
		}
	})
}

func TestEmptyVaultSkipsAutomaticButKeepsProtectiveBackup(t *testing.T) {
	graph := newTestService(t, domainbackup.Snapshot{SchemaVersion: testSchemaVersion})
	ctx := context.Background()
	result, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || graph.store.count() != 0 {
		t.Fatalf("empty Vault wrote a routine backup: %+v", result)
	}
	protective, err := graph.service.CreateProtective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !protective.Created || graph.store.count() != 1 {
		t.Fatalf("protective backup for an empty Vault = %+v count=%d", protective, graph.store.count())
	}
}

func TestEncryptionUnavailableSkipsAndAbortsRestore(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	ctx := context.Background()
	created, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil {
		t.Fatal(err)
	}
	graph.store.available = false
	if _, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", ""); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatalf("Create = %v, want ErrEncryptionUnavailable", err)
	}
	if graph.service.LastStatus().Kind != StatusSkippedUnavailable {
		t.Fatalf("status = %q", graph.service.LastStatus().Kind)
	}
	var protective *ProtectiveBackupError
	if err := graph.service.Restore(ctx, created.Backup.ID); !errors.As(err, &protective) {
		t.Fatalf("Restore = %v, want ProtectiveBackupError", err)
	}
	if _, restores := graph.snapshotter.calls(); restores != 0 {
		t.Fatal("restore ran without encryption")
	}
}

func TestAutomaticBackupsCoalesceNotifiedChanges(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	graph.service.Start()
	for index := 0; index < 10; index++ {
		graph.service.NotifyVaultChanged()
	}
	select {
	case <-graph.store.saves:
	case <-time.After(5 * time.Second):
		t.Fatal("the automatic backup worker never wrote a backup")
	}
	graph.service.Stop()
	if graph.store.count() != 1 {
		t.Fatalf("count = %d, want one coalesced backup", graph.store.count())
	}
	if graph.service.LastStatus().Kind != StatusCreated {
		t.Fatalf("status = %q, want created", graph.service.LastStatus().Kind)
	}
}

// TestNotifyDoesNotBlockWhileABackupRuns guards the store hook contract: a
// snapshot, encryption, fsync, and prune must never stall the mutation that
// queued them.
func TestNotifyDoesNotBlockWhileABackupRuns(t *testing.T) {
	store := newFakeStore()
	snapshotter := &blockingSnapshotter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	snapshotter.current = hostSnapshot("h1")
	service := NewService(snapshotter, store)

	createDone := make(chan error, 1)
	go func() {
		_, err := service.Create(context.Background(), domainbackup.ReasonVaultChange, "", "")
		createDone <- err
	}()
	<-snapshotter.entered

	notified := make(chan struct{})
	go func() {
		service.NotifyVaultChanged()
		close(notified)
	}()
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyVaultChanged blocked behind an in-flight backup")
	}

	close(snapshotter.release)
	select {
	case err := <-createDone:
		if err != nil {
			t.Fatalf("create: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight backup never finished")
	}
}

// TestClearReportsDeletedSeparatelyFromPruned keeps the explicit clear count
// out of Pruned. A panel that prints the clear message and then appends the
// pruned message must report "Deleted N backups" once, not twice.
func TestClearReportsDeletedSeparatelyFromPruned(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	ctx := context.Background()

	first, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil || !first.Created {
		t.Fatalf("first create = %+v, %v", first, err)
	}
	graph.advance(time.Second)
	graph.snapshotter.set(hostSnapshot("h2"))
	second, err := graph.service.Create(ctx, domainbackup.ReasonVaultChange, "", "")
	if err != nil || !second.Created {
		t.Fatalf("second create = %+v, %v", second, err)
	}

	deleted, err := graph.service.Clear(ctx)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("Clear deleted %d, want 2", deleted)
	}
	status := graph.service.LastStatus()
	if status.Kind != StatusCleared {
		t.Fatalf("status = %q, want %q", status.Kind, StatusCleared)
	}
	if status.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2", status.Deleted)
	}
	if status.Pruned != 0 {
		t.Errorf("Pruned = %d after a clear, want 0: the deleted entries are not retention pruning",
			status.Pruned)
	}
}

// TestProtectiveBackupDedupesTheCurrentState documents the barrier's dedupe:
// an identical state already has a restore point, so a restore reuses the
// newest record instead of writing a duplicate. The restore still applies.
func TestProtectiveBackupDedupesTheCurrentState(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	first, err := graph.service.Create(context.Background(), domainbackup.ReasonVaultChange, "", "")
	if err != nil || !first.Created {
		t.Fatalf("first create = %+v, %v", first, err)
	}
	if err := graph.service.Restore(context.Background(), first.Backup.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if graph.store.count() != 1 {
		t.Fatalf("count = %d, want the existing identical backup to be reused", graph.store.count())
	}
	if validate, restore := graph.snapshotter.calls(); validate != 1 || restore != 1 {
		t.Fatalf("validate/restore calls = %d/%d, want 1/1", validate, restore)
	}
	if graph.service.LastStatus().Kind != StatusRestored {
		t.Fatalf("status = %q, want restored", graph.service.LastStatus().Kind)
	}
}

type blockingSnapshotter struct {
	fakeSnapshotter
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSnapshotter) Snapshot(ctx context.Context) (domainbackup.Snapshot, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.fakeSnapshotter.Snapshot(ctx)
}

func TestRetentionStoreLoadsAndClamps(t *testing.T) {
	graph := &testService{
		store:       newFakeStore(),
		snapshotter: &fakeSnapshotter{current: hostSnapshot("h1")},
		retention:   &fakeRetention{value: 5},
	}
	service := NewService(graph.snapshotter, graph.store, WithRetentionStore(graph.retention))
	if got := service.Retention(); got != 5 {
		t.Fatalf("Retention = %d, want 5", got)
	}
	applied, _, err := service.SetRetention(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if applied != domainbackup.MaxRetention {
		t.Fatalf("applied = %d, want %d", applied, domainbackup.MaxRetention)
	}
	last := graph.retention.saves[len(graph.retention.saves)-1]
	if last != domainbackup.MaxRetention {
		t.Fatalf("saved %d, want %d", last, domainbackup.MaxRetention)
	}
}

func TestSetRetentionReportsPersistenceFailure(t *testing.T) {
	graph := newTestService(t, hostSnapshot("h1"))
	graph.retention.err = errors.New("read-only")
	if _, _, err := graph.service.SetRetention(context.Background(), 10); err == nil {
		t.Fatal("SetRetention hid a persistence failure")
	}
	if got := graph.service.Retention(); got != domainbackup.DefaultRetention {
		t.Fatalf("retention changed despite the failure: %d", got)
	}
}
