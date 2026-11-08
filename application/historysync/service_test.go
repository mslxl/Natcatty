package historysync_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	"github.com/mslxl/natcatty/application/historysync/fake"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

const (
	testWriter     = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f"
	testServerURL  = "https://atuin.example.test"
	testSession    = "session-token-secret-value"
	testUserID     = "fake-atuin-user"
	remoteWriterID = "0198d000-0000-7000-8000-000000000001"
)

var (
	testSyncKey    = []byte("0123456789abcdef0123456789abcdef")
	testSyncKeyB64 = base64.StdEncoding.EncodeToString(testSyncKey)
	testStart      = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	identityJitter = func(delay time.Duration) time.Duration { return delay }
)

// --- settings and secrets -------------------------------------------------

type syncSettings struct {
	history domainpreferences.History
}

func newSyncSettings() *syncSettings {
	history := domainpreferences.Defaults().History
	history.SyncEnabled = true
	history.SyncAddress = testServerURL
	return &syncSettings{history: history}
}

func (s *syncSettings) HistorySettings() domainpreferences.History { return s.history }

func (s *syncSettings) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	change(&s.history)
	return nil
}

type syncSecrets struct {
	mode   historysync.SecretMode
	values map[historysync.SecretRef]string
}

func newSyncSecrets() *syncSecrets {
	return &syncSecrets{mode: historysync.SecretModeOS, values: map[historysync.SecretRef]string{
		historysync.SessionTokenRef(): testSession,
		historysync.MasterKeyRef():    testSyncKeyB64,
	}}
}

func (s *syncSecrets) Mode() historysync.SecretMode { return s.mode }

func (s *syncSecrets) Has(_ context.Context, ref historysync.SecretRef) (bool, error) {
	if s.mode == historysync.SecretModePassphraseLocked {
		return false, historysync.ErrSecretsLocked
	}
	if s.mode == historysync.SecretModeUnavailable {
		return false, historysync.ErrSecretsUnavailable
	}
	_, ok := s.values[ref]
	return ok, nil
}

func (s *syncSecrets) Get(_ context.Context, ref historysync.SecretRef) (string, error) {
	if s.mode == historysync.SecretModePassphraseLocked {
		return "", historysync.ErrSecretsLocked
	}
	if s.mode == historysync.SecretModeUnavailable {
		return "", historysync.ErrSecretsUnavailable
	}
	value, ok := s.values[ref]
	if !ok {
		return "", historysync.ErrSecretNotFound
	}
	return value, nil
}

func (s *syncSecrets) Set(_ context.Context, ref historysync.SecretRef, value string) error {
	s.values[ref] = value
	return nil
}

func (s *syncSecrets) Delete(_ context.Context, ref historysync.SecretRef) error {
	delete(s.values, ref)
	return nil
}

// --- in-memory record log and journal -------------------------------------

type memorySyncStore struct {
	mu       sync.Mutex
	metadata domainhistory.Metadata
	records  []domainhistory.Record
	entries  map[string]domainhistory.Entry
	applyErr error
	applies  int
}

func newMemorySyncStore() *memorySyncStore {
	return &memorySyncStore{
		metadata: domainhistory.Metadata{
			WriterUUID: testWriter,
			Account: domainhistory.AccountIdentity{
				ServerURL: testServerURL,
				UserID:    testUserID,
			},
		},
		entries: make(map[string]domainhistory.Entry),
	}
}

func (s *memorySyncStore) HistoryMetadata(context.Context) (domainhistory.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSyncMetadata(s.metadata), nil
}

func (s *memorySyncStore) SaveHistoryMetadata(_ context.Context, metadata domainhistory.Metadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata = cloneSyncMetadata(metadata)
	return nil
}

func (s *memorySyncStore) ListHistoryRecords(context.Context) ([]domainhistory.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]domainhistory.Record, len(s.records))
	copy(records, s.records)
	return records, nil
}

func (s *memorySyncStore) ApplyRemotePage(_ context.Context, page historysync.RemotePage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applies++
	if s.applyErr != nil {
		return s.applyErr
	}
	checkpoint := page.Checkpoint
	if len(page.Acknowledged) > 0 {
		if page.AcknowledgedAt.IsZero() {
			return errors.New("missing acknowledgement timestamp")
		}
		for _, id := range page.Acknowledged {
			if id == "" {
				return errors.New("invalid acknowledged record")
			}
		}
	}
	moves := make(map[string]int64, len(page.Reindexes))
	for _, reindex := range page.Reindexes {
		if reindex.RecordID == "" || reindex.Index <= checkpoint.AppliedIndex {
			return errors.New("invalid reindex")
		}
		found := false
		for _, existing := range s.records {
			if existing.ID != reindex.RecordID {
				continue
			}
			if existing.SeriesHost != checkpoint.SeriesHost || existing.Tag != checkpoint.Tag ||
				existing.Direction != domainhistory.RecordDirectionLocal ||
				!existing.UploadedAt.IsZero() {
				return errors.New("invalid reindex target")
			}
			moves[reindex.RecordID] = reindex.Index
			found = true
			break
		}
		if !found {
			return errors.New("reindex target missing")
		}
	}
	indexOf := func(record domainhistory.Record) int64 {
		if moved, ok := moves[record.ID]; ok {
			return moved
		}
		return record.Index
	}
	for _, record := range page.Records {
		if !record.Valid() || record.Direction != domainhistory.RecordDirectionRemote {
			return errors.New("invalid remote record")
		}
		for _, existing := range s.records {
			if existing.SeriesHost == record.SeriesHost && existing.Tag == record.Tag &&
				indexOf(existing) == record.Index {
				if !sameMemoryRecord(existing, record) {
					return errors.New("conflicting remote record")
				}
			}
		}
	}
	for index := range s.records {
		if moved, ok := moves[s.records[index].ID]; ok {
			s.records[index].Index = moved
		}
	}
	for _, record := range page.Records {
		already := false
		for _, existing := range s.records {
			if existing.SeriesHost == record.SeriesHost && existing.Tag == record.Tag &&
				existing.Index == record.Index {
				already = true
				break
			}
		}
		if !already {
			s.records = append(s.records, record)
		}
	}
	for _, entry := range page.Entries {
		if entry.DeletedAt.IsZero() {
			// Mirror the SQLite store: a delete record already in the log wins
			// over a create for the same history id, even when the tombstone
			// arrived first from another writer's series.
			for _, existing := range s.records {
				if existing.Kind != domainhistory.RecordKindDelete ||
					existing.HistoryID != entry.ID {
					continue
				}
				var payload domainhistory.DeleteRecordPayload
				if err := json.Unmarshal([]byte(existing.Payload), &payload); err != nil {
					return err
				}
				if payload.DeletedAt.IsZero() {
					payload.DeletedAt = existing.RecordedAt
				}
				entry.DeletedAt = payload.DeletedAt
				break
			}
		}
		s.entries[entry.ID] = entry
	}
	for _, deleted := range page.Deletes {
		if entry, ok := s.entries[deleted.HistoryID]; ok {
			entry.DeletedAt = deleted.DeletedAt
			s.entries[deleted.HistoryID] = entry
		}
	}
	for _, id := range page.Acknowledged {
		found := false
		for index := range s.records {
			record := &s.records[index]
			if record.ID == id && record.SeriesHost == checkpoint.SeriesHost &&
				record.Tag == checkpoint.Tag && record.Direction == domainhistory.RecordDirectionLocal {
				found = true
				break
			}
		}
		if !found {
			// Validate every acknowledgement before mutating so a failed apply
			// never leaves a partial acknowledgement behind.
			return errors.New("acknowledged record missing")
		}
	}
	for _, id := range page.Acknowledged {
		for index := range s.records {
			record := &s.records[index]
			if record.ID == id && record.SeriesHost == checkpoint.SeriesHost &&
				record.Tag == checkpoint.Tag && record.Direction == domainhistory.RecordDirectionLocal {
				record.UploadedAt = page.AcknowledgedAt
			}
		}
	}
	if s.metadata.Series == nil {
		s.metadata.Series = make(map[string]domainhistory.SeriesCheckpoint)
	}
	s.metadata.Series[domainhistory.SeriesKey(checkpoint.SeriesHost, checkpoint.Tag)] = checkpoint
	return nil
}

func (s *memorySyncStore) MarkUploaded(_ context.Context, ids []string, at time.Time,
	checkpoint domainhistory.SeriesCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		for _, id := range ids {
			if s.records[index].ID == id {
				s.records[index].UploadedAt = at
			}
		}
	}
	if s.metadata.Series == nil {
		s.metadata.Series = make(map[string]domainhistory.SeriesCheckpoint)
	}
	s.metadata.Series[domainhistory.SeriesKey(checkpoint.SeriesHost, checkpoint.Tag)] = checkpoint
	return nil
}

func (s *memorySyncStore) ResetUploaded(_ context.Context, checkpoint domainhistory.SeriesCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		if s.records[index].SeriesHost == checkpoint.SeriesHost &&
			s.records[index].Tag == checkpoint.Tag {
			s.records[index].UploadedAt = time.Time{}
		}
	}
	if s.metadata.Series == nil {
		s.metadata.Series = make(map[string]domainhistory.SeriesCheckpoint)
	}
	s.metadata.Series[domainhistory.SeriesKey(checkpoint.SeriesHost, checkpoint.Tag)] = checkpoint
	return nil
}

func (s *memorySyncStore) addLocalEntry(entry domainhistory.Entry) domainhistory.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	writer := s.metadata.WriterUUID
	index := s.nextIndexLocked(writer)
	payload, err := json.Marshal(entry)
	if err != nil {
		panic(err)
	}
	record := domainhistory.Record{
		ID: entry.ID + "-record", SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: index, RecordedAt: entry.RecordedAt, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindCreate, HistoryID: entry.ID, Payload: string(payload),
		Direction: domainhistory.RecordDirectionLocal,
	}
	s.records = append(s.records, record)
	s.entries[entry.ID] = entry
	return record
}

// addLocalDelete appends this device's tombstone for entry in the writer
// series and soft-deletes the projection, as the History service does for a
// delete, prune, or clear.
func (s *memorySyncStore) addLocalDelete(entry domainhistory.Entry, at time.Time) domainhistory.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	writer := s.metadata.WriterUUID
	payload, err := json.Marshal(domainhistory.DeleteRecordPayload{
		HistoryID: entry.ID, DeletedAt: at,
	})
	if err != nil {
		panic(err)
	}
	record := domainhistory.Record{
		ID: entry.ID + "-delete", SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: s.nextIndexLocked(writer), RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindDelete, HistoryID: entry.ID, Payload: string(payload),
		Direction: domainhistory.RecordDirectionLocal,
	}
	s.records = append(s.records, record)
	projected := s.entries[entry.ID]
	projected.DeletedAt = at
	s.entries[entry.ID] = projected
	return record
}

func (s *memorySyncStore) nextIndexLocked(writer string) int64 {
	index := int64(0)
	for _, record := range s.records {
		if record.SeriesHost == writer && record.Tag == domainhistory.RecordTagHistory &&
			record.Index >= index {
			index = record.Index + 1
		}
	}
	return index
}

func (s *memorySyncStore) setUploaded(ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.records {
		for _, id := range ids {
			if s.records[index].ID == id {
				s.records[index].UploadedAt = testStart.Add(-time.Hour)
			}
		}
	}
}

func (s *memorySyncStore) addRemoteRecord(record domainhistory.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	if record.Kind == domainhistory.RecordKindCreate {
		var entry domainhistory.Entry
		if err := json.Unmarshal([]byte(record.Payload), &entry); err != nil {
			panic(err)
		}
		s.entries[entry.ID] = entry
	}
}

func (s *memorySyncStore) snapshotEntries() map[string]domainhistory.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := make(map[string]domainhistory.Entry, len(s.entries))
	for id, entry := range s.entries {
		entries[id] = entry
	}
	return entries
}

func (s *memorySyncStore) snapshotRecords() []domainhistory.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]domainhistory.Record, len(s.records))
	copy(records, s.records)
	return records
}

func (s *memorySyncStore) checkpoint(seriesHost string) domainhistory.SeriesCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, ok := s.metadata.Series[domainhistory.SeriesKey(seriesHost, domainhistory.RecordTagHistory)]
	if !ok {
		checkpoint.AppliedIndex = -1
		checkpoint.UploadedIndex = -1
	}
	return checkpoint
}

func (s *memorySyncStore) setApplyError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyErr = err
}

func (s *memorySyncStore) setMetadata(change func(*domainhistory.Metadata)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.metadata)
}

func cloneSyncMetadata(metadata domainhistory.Metadata) domainhistory.Metadata {
	clone := metadata
	if metadata.Series != nil {
		clone.Series = make(map[string]domainhistory.SeriesCheckpoint, len(metadata.Series))
		for key, checkpoint := range metadata.Series {
			clone.Series[key] = checkpoint
		}
	}
	if metadata.HostImports != nil {
		clone.HostImports = make(map[string]domainhistory.ImportCheckpoint, len(metadata.HostImports))
		for key, checkpoint := range metadata.HostImports {
			clone.HostImports[key] = checkpoint
		}
	}
	return clone
}

func sameMemoryRecord(a, b domainhistory.Record) bool {
	return a.ID == b.ID && a.SeriesHost == b.SeriesHost && a.Tag == b.Tag &&
		a.Index == b.Index && a.Version == b.Version && a.Kind == b.Kind &&
		a.HistoryID == b.HistoryID && a.RecordedAt.UnixNano() == b.RecordedAt.UnixNano()
}

// --- manual scheduler ------------------------------------------------------

type manualScheduler struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	at      time.Time
	fn      func()
	stopped bool
	fired   bool
}

func newManualScheduler() *manualScheduler {
	return &manualScheduler{now: testStart}
}

func (s *manualScheduler) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

func (s *manualScheduler) AfterFunc(delay time.Duration, fn func()) historysync.Timer {
	s.mu.Lock()
	defer s.mu.Unlock()
	timer := &manualTimer{at: s.now.Add(delay), fn: fn}
	s.timers = append(s.timers, timer)
	return timer
}

func (s *manualScheduler) Advance(delta time.Duration) {
	s.mu.Lock()
	target := s.now.Add(delta)
	s.now = target
	for {
		var next *manualTimer
		for _, timer := range s.timers {
			if timer.stopped || timer.fired || timer.at.After(target) {
				continue
			}
			if next == nil || timer.at.Before(next.at) {
				next = timer
			}
		}
		if next == nil {
			break
		}
		next.fired = true
		s.mu.Unlock()
		next.fn()
		s.mu.Lock()
	}
	s.mu.Unlock()
}

func (t *manualTimer) Stop() {
	t.stopped = true
}

// --- helpers ---------------------------------------------------------------

func newEngine(t *testing.T, store *memorySyncStore, factory *fake.Factory,
	opts ...historysync.Option) (*historysync.Service, *manualScheduler, *syncSecrets) {
	t.Helper()
	scheduler := newManualScheduler()
	secrets := newSyncSecrets()
	options := []historysync.Option{
		historysync.WithScheduler(scheduler),
		historysync.WithJitter(identityJitter),
		historysync.WithCaptureDebounce(time.Second),
		historysync.WithFocusDebounce(time.Second),
	}
	options = append(options, opts...)
	service := historysync.NewService(newSyncSettings(), secrets, store, factory, options...)
	t.Cleanup(service.Stop)
	return service, scheduler, secrets
}

func localEntry(id, command string, at time.Time) domainhistory.Entry {
	return domainhistory.Entry{
		ID: id, Scope: "local", Command: command, RecordedAt: at,
		Source: domainhistory.SourceSession, Shell: "zsh", CWD: "/work",
	}
}

func remoteCreate(writer, id, command string, index int64, at time.Time) historysync.SyncRecord {
	return historysync.SyncRecord{
		ID: "remote-record-" + id, SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: index, RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindCreate, HistoryID: id,
		Entry: &domainhistory.Entry{
			ID: id, Command: command, Origin: "remotehost:alice",
			RecordedAt: at, Source: domainhistory.SourceAtuinSync,
			// A foreign record can carry another device's local identities and
			// capture tier; the projection must not trust them.
			HostID: "foreign-host", HostLabel: "foreign", UnsavedHost: true,
			SessionID: "foreign-session", CaptureTier: 3,
			Scope: "host:foreign-host",
		},
	}
}

func remoteDelete(writer, id string, index int64, at time.Time) historysync.SyncRecord {
	return historysync.SyncRecord{
		ID: "remote-delete-" + id, SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: index, RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindDelete, HistoryID: id, DeletedAt: at,
	}
}

func pageSizeCaps(size int) historysync.Capabilities {
	capabilities := fake.CompatibleCapabilities()
	capabilities.PageSize = size
	return capabilities
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the engine")
	}
}

func waitIdle(t *testing.T, service *historysync.Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.WaitIdle(ctx); err != nil {
		t.Fatalf("wait for sync idle: %v", err)
	}
}

func syncNow(t *testing.T, service *historysync.Service) historysync.SyncResult {
	t.Helper()
	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	return result
}

// --- tests -----------------------------------------------------------------

func TestSyncDownloadsAllPagesBeforeUploadAndAppliesRemoteRows(t *testing.T) {
	remote := []historysync.SyncRecord{
		remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000011", "remote one", 0, testStart.Add(time.Minute)),
		remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000012", "remote two", 1, testStart.Add(2*time.Minute)),
		remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000013", "remote three", 2, testStart.Add(3*time.Minute)),
		remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000014", "remote four", 3, testStart.Add(4*time.Minute)),
		remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000015", "remote five", 4, testStart.Add(5*time.Minute)),
	}
	factory := fake.NewFactory(fake.Options{
		Capabilities: pageSizeCaps(2),
		Records:      remote,
	})
	store := newMemorySyncStore()
	local := localEntry("0198d000-0000-7000-8000-000000000021", "echo local", testStart)
	store.addLocalEntry(local)
	service, scheduler, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Downloaded != 5 || result.Uploaded != 1 {
		t.Fatalf("result = %+v (events %v), want 5 downloaded and 1 uploaded",
			result, factory.Events())
	}

	events := factory.Events()
	var downloads, uploads int
	for index, event := range events {
		switch {
		case strings.HasPrefix(event, "download:"):
			downloads++
			if uploads > 0 {
				t.Fatalf("download %q ran after an upload: %v", event, events)
			}
		case strings.HasPrefix(event, "upload:"):
			uploads++
			if index == 0 {
				t.Fatalf("upload ran before any download: %v", events)
			}
		}
	}
	if downloads != 3 || uploads != 1 {
		t.Fatalf("events = %v, want three pages and one upload", events)
	}

	entries := store.snapshotEntries()
	for index := 0; index < 5; index++ {
		id := remote[index].HistoryID
		entry, ok := entries[id]
		if !ok {
			t.Fatalf("remote entry %s was not applied", id)
		}
		if entry.Source != domainhistory.SourceAtuinSync {
			t.Fatalf("entry %s source = %q, want atuin-sync provenance", id, entry.Source)
		}
		if entry.Scope != "remote:remotehost:alice" {
			t.Fatalf("entry %s scope = %q, want the remote origin scope", id, entry.Scope)
		}
		if entry.HostID != "" || entry.HostLabel != "" || entry.UnsavedHost ||
			entry.SessionID != "" || entry.CaptureTier != 0 {
			t.Fatalf("entry %s kept a foreign local identity: %+v", id, entry)
		}
	}
	if got := store.checkpoint(remoteWriterID); got.AppliedIndex != 4 {
		t.Fatalf("remote checkpoint = %+v, want applied index 4", got)
	}
	localCheckpoint := store.checkpoint(testWriter)
	if localCheckpoint.UploadedIndex != 0 {
		t.Fatalf("local checkpoint = %+v, want uploaded index 0", localCheckpoint)
	}

	status := service.Status()
	if status.State != atuinmodel.SyncIdle || status.PendingUploads != 0 {
		t.Fatalf("status = %+v, want idle with no pending uploads", status)
	}
	if !status.LastSyncAt.Equal(scheduler.Now()) {
		t.Fatalf("last sync = %v, want %v", status.LastSyncAt, scheduler.Now())
	}
	if status.NextRetryAt != (time.Time{}) {
		t.Fatalf("next retry = %v, want none", status.NextRetryAt)
	}
}

func TestApplyFailureLeavesCheckpointAndLogUnchangedThenRetrySucceeds(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		Capabilities: pageSizeCaps(2),
		Records: []historysync.SyncRecord{
			remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000031", "one", 0, testStart),
			remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000032", "two", 1, testStart.Add(time.Minute)),
		},
	})
	store := newMemorySyncStore()
	store.setApplyError(errors.New("disk full"))
	service, _, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); err == nil {
		t.Fatal("SyncNow succeeded despite a failed page commit")
	}
	if got := store.checkpoint(remoteWriterID); got.AppliedIndex != -1 {
		t.Fatalf("checkpoint advanced to %+v after a failed commit", got)
	}
	for _, record := range store.snapshotRecords() {
		if record.SeriesHost == remoteWriterID {
			t.Fatalf("remote record persisted without a commit: %+v", record)
		}
	}
	status := service.Status()
	if status.State != atuinmodel.SyncError || status.NextRetryAt.IsZero() {
		t.Fatalf("status = %+v, want an error with the next retry", status)
	}

	store.setApplyError(nil)
	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatalf("retry after commit recovery: %v", err)
	}
	if got := store.checkpoint(remoteWriterID); got.AppliedIndex != 1 {
		t.Fatalf("checkpoint = %+v, want applied index 1 after the retry", got)
	}
}

func TestRetryBackoffAndStatusShowPendingUploadsAndNextRetry(t *testing.T) {
	factory := fake.NewFactory(fake.Options{UploadErr: historysync.ErrOffline})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000041", "one", testStart))
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000042", "two", testStart.Add(time.Minute)))
	service, scheduler, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("SyncNow = %v, want ErrOffline", err)
	}
	status := service.Status()
	if status.State != atuinmodel.SyncOffline || status.PendingUploads != 2 {
		t.Fatalf("status = %+v, want offline with two pending uploads", status)
	}
	if want := scheduler.Now().Add(30 * time.Second); !status.NextRetryAt.Equal(want) {
		t.Fatalf("next retry = %v, want %v", status.NextRetryAt, want)
	}

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("second SyncNow = %v, want ErrOffline", err)
	}
	status = service.Status()
	if want := scheduler.Now().Add(time.Minute); !status.NextRetryAt.Equal(want) {
		t.Fatalf("second next retry = %v, want the one-minute backoff %v", status.NextRetryAt, want)
	}
}

// TestLastSuccessfulSyncSurvivesLaterFailures proves the status keeps naming
// the last run that finished while a later run is syncing or failing.
func TestLastSuccessfulSyncSurvivesLaterFailures(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		UploadErrSequence: []error{nil, historysync.ErrOffline},
	})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000041", "one", testStart))
	service, scheduler, _ := newEngine(t, store, factory)

	first, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("first SyncNow: %v", err)
	}
	if !first.LastSyncAt.Equal(testStart) {
		t.Fatalf("first last sync = %v, want %v", first.LastSyncAt, testStart)
	}
	scheduler.Advance(time.Minute)
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000042",
		"two", testStart.Add(time.Minute)))

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("second SyncNow = %v, want ErrOffline", err)
	}
	status := service.Status()
	if status.State != atuinmodel.SyncOffline {
		t.Fatalf("status state = %q, want offline", status.State)
	}
	if !status.LastSyncAt.Equal(testStart) {
		t.Fatalf("last sync = %v, want the previous successful run %v",
			status.LastSyncAt, testStart)
	}
}

func TestScheduledRetryResumesAfterATransientFailure(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		UploadErrSequence: []error{historysync.ErrOffline, nil},
	})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000051", "one", testStart))
	service, scheduler, _ := newEngine(t, store, factory)

	service.Start(context.Background())
	waitIdle(t, service)
	status := service.Status()
	if status.State != atuinmodel.SyncOffline || status.NextRetryAt.IsZero() {
		t.Fatalf("status after startup failure = %+v, want offline with retry", status)
	}

	scheduler.Advance(30 * time.Second)
	waitIdle(t, service)
	status = service.Status()
	if status.State != atuinmodel.SyncIdle || status.PendingUploads != 0 {
		t.Fatalf("status after retry = %+v, want idle and uploaded", status)
	}
	if status.NextRetryAt != (time.Time{}) {
		t.Fatalf("next retry after success = %v, want none", status.NextRetryAt)
	}
	if got := store.checkpoint(testWriter).UploadedIndex; got != 0 {
		t.Fatalf("uploaded checkpoint = %d, want 0", got)
	}
}

func TestLockedSecretsPauseSyncWithoutOpeningARemote(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	store := newMemorySyncStore()
	service, _, secrets := newEngine(t, store, factory)
	secrets.mode = historysync.SecretModePassphraseLocked

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrSecretsLocked) {
		t.Fatalf("SyncNow = %v, want ErrSecretsLocked", err)
	}
	if factory.Opens() != 0 {
		t.Fatalf("factory opened %d remotes while locked", factory.Opens())
	}
	status := service.Status()
	if status.State != atuinmodel.SyncLocked || status.NextRetryAt != (time.Time{}) {
		t.Fatalf("status = %+v, want locked with no retry", status)
	}
}

func TestAuthExpiryKeyMismatchAndVersionMismatchFailWithoutRetry(t *testing.T) {
	t.Run("auth expired", func(t *testing.T) {
		factory := fake.NewFactory(fake.Options{AuthErr: historysync.ErrAuthExpired})
		store := newMemorySyncStore()
		service, _, _ := newEngine(t, store, factory)
		if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrAuthExpired) {
			t.Fatalf("SyncNow = %v, want ErrAuthExpired", err)
		}
		status := service.Status()
		if status.State != atuinmodel.SyncAuthExpired || status.NextRetryAt != (time.Time{}) {
			t.Fatalf("status = %+v, want auth expired with no retry", status)
		}
		calls := factory.Last().Calls()
		if calls.Download != 0 || calls.Upload != 0 {
			t.Fatalf("calls = %+v, want no record sync", calls)
		}
	})

	t.Run("key mismatch", func(t *testing.T) {
		factory := fake.NewFactory(fake.Options{
			Records: []historysync.SyncRecord{
				remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000061", "one", 0, testStart),
			},
			Validation: historysync.KeyValidationMismatch,
		})
		store := newMemorySyncStore()
		service, _, _ := newEngine(t, store, factory)
		if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrKeyMismatch) {
			t.Fatalf("SyncNow = %v, want ErrKeyMismatch", err)
		}
		status := service.Status()
		if status.State != atuinmodel.SyncKeyMismatch || status.NextRetryAt != (time.Time{}) {
			t.Fatalf("status = %+v, want key mismatch with no retry", status)
		}
		calls := factory.Last().Calls()
		if calls.Download != 0 || calls.Upload != 0 {
			t.Fatalf("calls = %+v, want no records exchanged with a mismatched key", calls)
		}
	})

	t.Run("incompatible server", func(t *testing.T) {
		capabilities := fake.CompatibleCapabilities()
		capabilities.Major = 19
		factory := fake.NewFactory(fake.Options{Capabilities: capabilities})
		store := newMemorySyncStore()
		service, _, _ := newEngine(t, store, factory)
		if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrIncompatibleServer) {
			t.Fatalf("SyncNow = %v, want ErrIncompatibleServer", err)
		}
		status := service.Status()
		if status.State != atuinmodel.SyncIncompatibleServer || status.NextRetryAt != (time.Time{}) {
			t.Fatalf("status = %+v, want incompatible server with no retry", status)
		}
		calls := factory.Last().Calls()
		if calls.Download != 0 || calls.Upload != 0 {
			t.Fatalf("calls = %+v, want no record sync", calls)
		}
	})
}

func TestUnknownTagsAreNeverDownloaded(t *testing.T) {
	history := remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-000000000071", "one", 0, testStart)
	kv := []historysync.SyncRecord{
		{
			ID: "kv-0", SeriesHost: "kv-writer", Tag: "kv", Index: 0,
			RecordedAt: testStart, Version: 1, Kind: domainhistory.RecordKindCreate,
			HistoryID: "kv-0", Entry: &domainhistory.Entry{
				ID: "kv-0", Scope: "local", Command: "kv", RecordedAt: testStart,
			},
		},
	}
	factory := fake.NewFactory(fake.Options{Records: append([]historysync.SyncRecord{history}, kv...)})
	store := newMemorySyncStore()
	service, _, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	for _, event := range factory.Events() {
		if strings.Contains(event, "kv-writer") || strings.Contains(event, ":kv") {
			t.Fatalf("an unknown tag was synchronized: %v", factory.Events())
		}
	}
	for _, record := range store.snapshotRecords() {
		if record.Tag != domainhistory.RecordTagHistory {
			t.Fatalf("unknown tag stored locally: %+v", record)
		}
	}
}

func TestUploadsAreBatchedInIndexOrderAndNeverSkipAnIndex(t *testing.T) {
	factory := fake.NewFactory(fake.Options{Capabilities: pageSizeCaps(2)})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	var ids []string
	for index := 0; index < 5; index++ {
		id := "0198d000-0000-7000-8000-00000000008" + string(rune('0'+index))
		entry := localEntry(id, "command", testStart.Add(time.Duration(index)*time.Minute))
		store.addLocalEntry(entry)
		ids = append(ids, id+"-record")
	}
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Uploaded != 5 {
		t.Fatalf("uploaded = %d, want 5", result.Uploaded)
	}
	wantEvents := []string{
		"upload:" + testWriter + ":0-1",
		"upload:" + testWriter + ":2-3",
		"upload:" + testWriter + ":4-4",
	}
	if got := factory.Events(); !equalStrings(got, wantEvents) {
		t.Fatalf("events = %v, want %v", got, wantEvents)
	}
	server := factory.ServerRecords()
	if len(server) != 5 {
		t.Fatalf("server records = %d, want 5", len(server))
	}
	for index, record := range server {
		if record.Index != int64(index) {
			t.Fatalf("server index %d = %d, want a continuous series", index, record.Index)
		}
	}
	if len(ids) != 5 {
		t.Fatalf("seeded %d records, want 5", len(ids))
	}
}

func TestUploadRefusesALocalIndexGap(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	store := newMemorySyncStore()
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000091", "one", testStart))
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000092", "two", testStart.Add(time.Minute)))
	store.mu.Lock()
	store.records[len(store.records)-1].Index = 3
	store.mu.Unlock()
	service, _, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrIncompletePage) {
		t.Fatalf("SyncNow = %v, want ErrIncompletePage", err)
	}
	if calls := factory.Last().Calls(); calls.Upload != 0 {
		t.Fatalf("upload calls = %d, want none for a gapped series", calls.Upload)
	}
}

func TestDuplicateConflictSucceedsOnlyWhenMetadataMatches(t *testing.T) {
	t.Run("matching duplicate", func(t *testing.T) {
		store := newMemorySyncStore()
		store.setMetadata(func(metadata *domainhistory.Metadata) {
			metadata.UnverifiedKeyConfirmed = true
		})
		first := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000a1", "one", testStart))
		store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000a2", "two", testStart.Add(time.Minute)))
		existing := historysync.SyncRecord{
			ID: first.ID, SeriesHost: first.SeriesHost, Tag: first.Tag, Index: first.Index,
			RecordedAt: first.RecordedAt, Version: first.Version, Kind: first.Kind,
			HistoryID: first.HistoryID,
			Entry: &domainhistory.Entry{
				ID: first.HistoryID, Command: "one", Scope: "local", RecordedAt: first.RecordedAt,
			},
		}
		factory := fake.NewFactory(fake.Options{
			Records:              []historysync.SyncRecord{existing},
			StatusIgnoresRecords: true,
		})
		service, _, _ := newEngine(t, store, factory)

		result, err := service.SyncNow(context.Background())
		if err != nil {
			t.Fatalf("SyncNow: %v", err)
		}
		if result.Uploaded != 2 {
			t.Fatalf("uploaded = %d, want both records acknowledged", result.Uploaded)
		}
		server := factory.ServerRecords()
		if len(server) != 2 {
			t.Fatalf("server records = %d, want 2 without duplicates", len(server))
		}
	})

	t.Run("mismatched duplicate", func(t *testing.T) {
		store := newMemorySyncStore()
		store.setMetadata(func(metadata *domainhistory.Metadata) {
			metadata.UnverifiedKeyConfirmed = true
		})
		first := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000b1", "one", testStart))
		store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000b2", "two", testStart.Add(time.Minute)))
		foreign := historysync.SyncRecord{
			ID: "a-different-record-id", SeriesHost: first.SeriesHost, Tag: first.Tag,
			Index: first.Index, RecordedAt: first.RecordedAt, Version: first.Version,
			Kind: first.Kind, HistoryID: "0198d000-0000-7000-8000-0000000000b9",
			Entry: &domainhistory.Entry{
				ID: "0198d000-0000-7000-8000-0000000000b9", Command: "foreign",
				Scope: "local", RecordedAt: first.RecordedAt,
			},
		}
		factory := fake.NewFactory(fake.Options{
			Records:              []historysync.SyncRecord{foreign},
			StatusIgnoresRecords: true,
		})
		service, _, _ := newEngine(t, store, factory)

		if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrUploadConflict) {
			t.Fatalf("SyncNow = %v, want ErrUploadConflict", err)
		}
		status := service.Status()
		if status.State != atuinmodel.SyncError || status.NextRetryAt != (time.Time{}) {
			t.Fatalf("status = %+v, want a non-retrying error", status)
		}
		if len(factory.ServerRecords()) != 1 {
			t.Fatal("a mismatched duplicate still wrote a record")
		}
	})
}

func TestDownloadedTombstonesApplyToTheProjection(t *testing.T) {
	entryID := "0198d000-0000-7000-8000-0000000000c1"
	factory := fake.NewFactory(fake.Options{
		Records: []historysync.SyncRecord{
			remoteCreate(remoteWriterID, entryID, "doomed", 0, testStart),
			remoteDelete(remoteWriterID, entryID, 1, testStart.Add(time.Minute)),
			remoteDelete(remoteWriterID, "0198d000-0000-7000-8000-0000000000c9", 2, testStart.Add(2*time.Minute)),
		},
	})
	store := newMemorySyncStore()
	service, _, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	entry := store.snapshotEntries()[entryID]
	if entry.DeletedAt.IsZero() {
		t.Fatalf("downloaded tombstone did not soft-delete %s: %+v (entries %v, events %v)",
			entryID, entry, store.snapshotEntries(), factory.Events())
	}
	if got := store.checkpoint(remoteWriterID).AppliedIndex; got != 2 {
		t.Fatalf("applied index = %d, want the tombstone page committed (2)", got)
	}
}

// TestTwoIndependentWritersConvergeAndPropagateDeletes proves two devices with
// separate writer series end up with one copy of each row, and a delete on
// either side removes the row on the other side. The two rows carry inverted
// timestamps so neither convergence nor propagation can be timestamp-ordered.
func TestTwoIndependentWritersConvergeAndPropagateDeletes(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	storeA := newMemorySyncStore()
	storeA.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	storeB := newMemorySyncStore()
	storeB.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.WriterUUID = remoteWriterID
		metadata.UnverifiedKeyConfirmed = true
	})
	entryA := localEntry("0198d000-0000-7000-8000-000000000801",
		"from writer a", testStart.Add(10*time.Minute))
	entryB := localEntry("0198d000-0000-7000-8000-000000000802",
		"from writer b", testStart)
	storeA.addLocalEntry(entryA)
	storeB.addLocalEntry(entryB)

	engineA, _, _ := newEngine(t, storeA, factory)
	engineB, _, _ := newEngine(t, storeB, factory)
	syncNow(t, engineA)
	syncNow(t, engineB)
	syncNow(t, engineA)

	assertConverged := func(label string, store *memorySyncStore) {
		t.Helper()
		entries := store.snapshotEntries()
		if len(entries) != 2 {
			t.Fatalf("%s entries = %v, want one row per writer", label, sortedKeys(entries))
		}
		creates := make(map[string]int)
		for _, record := range store.snapshotRecords() {
			if record.Kind == domainhistory.RecordKindCreate {
				creates[record.HistoryID]++
			}
		}
		for _, entry := range []domainhistory.Entry{entryA, entryB} {
			if _, ok := entries[entry.ID]; !ok {
				t.Fatalf("%s is missing %s", label, entry.ID)
			}
			if creates[entry.ID] != 1 {
				t.Fatalf("%s holds %d create records for %s, want exactly one",
					label, creates[entry.ID], entry.ID)
			}
		}
	}
	assertConverged("writer a", storeA)
	assertConverged("writer b", storeB)
	for _, record := range factory.ServerRecords() {
		if record.Tag != domainhistory.RecordTagHistory {
			t.Fatalf("a non-history tag reached the server: %+v", record)
		}
	}
	if got := len(factory.ServerRecords()); got != 2 {
		t.Fatalf("server records = %d, want the two creates: %+v",
			got, factory.ServerRecords())
	}

	// Delete on writer A propagates to writer B.
	storeA.addLocalDelete(entryA, testStart.Add(30*time.Minute))
	syncNow(t, engineA)
	syncNow(t, engineB)
	if deleted := storeB.snapshotEntries()[entryA.ID].DeletedAt; deleted.IsZero() {
		t.Fatalf("writer B still shows %s after writer A deleted it", entryA.ID)
	}

	// Delete on writer B propagates back to writer A.
	storeB.addLocalDelete(entryB, testStart.Add(5*time.Minute))
	syncNow(t, engineB)
	syncNow(t, engineA)
	if deleted := storeA.snapshotEntries()[entryB.ID].DeletedAt; deleted.IsZero() {
		t.Fatalf("writer A still shows %s after writer B deleted it", entryB.ID)
	}

	tombstones := make(map[string]int)
	for _, record := range factory.ServerRecords() {
		if record.Kind == domainhistory.RecordKindDelete {
			tombstones[record.HistoryID]++
		}
	}
	for _, entry := range []domainhistory.Entry{entryA, entryB} {
		if tombstones[entry.ID] != 1 {
			t.Fatalf("server holds %d tombstones for %s, want one", tombstones[entry.ID], entry.ID)
		}
	}
}

// TestDownloadedTombstonesApplyBeforeTheWriterUploads proves a run applies
// another writer's tombstone before it sends this writer's pending record.
func TestDownloadedTombstonesApplyBeforeTheWriterUploads(t *testing.T) {
	const doomedID = "0198d000-0000-7000-8000-000000000811"
	remoteCreateRecord := remoteCreate(remoteWriterID, doomedID, "doomed", 0, testStart)
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	payload, err := json.Marshal(remoteCreateRecord.Entry)
	if err != nil {
		t.Fatalf("encode remote entry: %v", err)
	}
	store.addRemoteRecord(domainhistory.Record{
		ID: remoteCreateRecord.ID, SeriesHost: remoteWriterID,
		Tag: domainhistory.RecordTagHistory, Index: 0, RecordedAt: testStart,
		Version: domainhistory.RecordVersion, Kind: domainhistory.RecordKindCreate,
		HistoryID: doomedID, Payload: string(payload),
		Direction: domainhistory.RecordDirectionRemote,
	})
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.Series = map[string]domainhistory.SeriesCheckpoint{
			domainhistory.SeriesKey(remoteWriterID, domainhistory.RecordTagHistory): {
				SeriesHost: remoteWriterID, Tag: domainhistory.RecordTagHistory,
				AppliedIndex: 0, UploadedIndex: -1,
			},
		}
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000812",
		"pending local", testStart.Add(time.Minute)))

	appliedBeforeUpload := false
	factory := fake.NewFactory(fake.Options{
		Records: []historysync.SyncRecord{
			remoteDelete(remoteWriterID, doomedID, 1, testStart.Add(2*time.Minute)),
		},
		UploadHook: func(records []historysync.SyncRecord) {
			if len(records) == 0 || records[0].SeriesHost != testWriter {
				return
			}
			appliedBeforeUpload = !store.snapshotEntries()[doomedID].DeletedAt.IsZero()
		},
	})
	service, _, _ := newEngine(t, store, factory)
	result := syncNow(t, service)
	if result.Uploaded != 1 {
		t.Fatalf("uploaded = %d, want the pending local record: %v", result.Uploaded, factory.Events())
	}
	if !appliedBeforeUpload {
		t.Fatalf("the tombstone was not applied before upload: %v", factory.Events())
	}
	events := factory.Events()
	if len(events) != 2 || !strings.HasPrefix(events[0], "download:") ||
		!strings.HasPrefix(events[1], "upload:") {
		t.Fatalf("events = %v, want the download before the upload", events)
	}
}

// TestSyncOrdersBySeriesIndexNotTimestamps proves the checkpoint is a series
// index, not a timestamp: a higher index with an older timestamp is still
// downloaded, and a tombstone older than the row it deletes still wins.
func TestSyncOrdersBySeriesIndexNotTimestamps(t *testing.T) {
	const (
		firstID  = "0198d000-0000-7000-8000-000000000821"
		secondID = "0198d000-0000-7000-8000-000000000822"
		thirdID  = "0198d000-0000-7000-8000-000000000823"
	)
	factory := fake.NewFactory(fake.Options{
		Records: []historysync.SyncRecord{
			remoteCreate(remoteWriterID, firstID, "first", 0, testStart.Add(10*time.Minute)),
			remoteCreate(remoteWriterID, secondID, "second", 1, testStart),
			remoteDelete(remoteWriterID, secondID, 2, testStart.Add(-time.Hour)),
		},
	})
	store := newMemorySyncStore()
	service, _, _ := newEngine(t, store, factory)
	syncNow(t, service)

	entries := store.snapshotEntries()
	if deleted := entries[secondID].DeletedAt; deleted.IsZero() {
		t.Fatalf("a tombstone older than the row did not delete %s: %+v", secondID, entries[secondID])
	}
	if deleted := entries[firstID].DeletedAt; !deleted.IsZero() {
		t.Fatalf("the tombstone deleted an unrelated row: %+v", entries[firstID])
	}
	if got := store.checkpoint(remoteWriterID).AppliedIndex; got != 2 {
		t.Fatalf("applied index = %d, want 2", got)
	}

	// A record with a higher index but an older timestamp is still new work.
	factory.AppendRecord(remoteCreate(remoteWriterID, thirdID, "third", 3, testStart.Add(-24*time.Hour)))
	syncNow(t, service)
	if _, ok := store.snapshotEntries()[thirdID]; !ok {
		t.Fatalf("a higher index with an older timestamp was skipped: %+v",
			factory.Events())
	}
	if got := store.checkpoint(remoteWriterID).AppliedIndex; got != 3 {
		t.Fatalf("applied index = %d, want 3", got)
	}
}

// TestTombstoneFromOneWriterSuppressesALaterCreateFromAnother proves an
// out-of-order download (a tombstone from one writer's series, then the create
// from another writer's series) does not resurrect the row.
func TestTombstoneFromOneWriterSuppressesALaterCreateFromAnother(t *testing.T) {
	const (
		tombstoneWriter = "0198d000-0000-7000-8000-000000000830"
		createWriter    = "0198d000-0000-7000-8000-000000000831"
		historyID       = "0198d000-0000-7000-8000-000000000832"
	)
	factory := fake.NewFactory(fake.Options{
		Records: []historysync.SyncRecord{
			remoteDelete(tombstoneWriter, historyID, 0, testStart.Add(time.Minute)),
			remoteCreate(createWriter, historyID, "doomed", 0, testStart),
		},
	})
	store := newMemorySyncStore()
	service, _, _ := newEngine(t, store, factory)
	syncNow(t, service)

	entry, ok := store.snapshotEntries()[historyID]
	if !ok {
		t.Fatalf("the create was dropped instead of projected as deleted: %v", factory.Events())
	}
	if entry.DeletedAt.IsZero() {
		t.Fatalf("the earlier tombstone was undone by the later create: %+v", entry)
	}
	// A replay is idempotent and must not resurrect the row either.
	syncNow(t, service)
	if entry := store.snapshotEntries()[historyID]; entry.DeletedAt.IsZero() {
		t.Fatalf("a replay resurrected the row: %+v", entry)
	}
}

func TestAccountSwitchReuploadsLocalHistoryAndKeepsRemoteRows(t *testing.T) {
	store := newMemorySyncStore()
	first := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000d1", "one", testStart))
	second := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000d2", "two", testStart.Add(time.Minute)))
	store.setUploaded(first.ID, second.ID)
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
		metadata.Series = map[string]domainhistory.SeriesCheckpoint{
			domainhistory.SeriesKey(testWriter, domainhistory.RecordTagHistory): {
				SeriesHost: testWriter, Tag: domainhistory.RecordTagHistory,
				AppliedIndex: 0, UploadedIndex: 1,
			},
			domainhistory.SeriesKey(remoteWriterID, domainhistory.RecordTagHistory): {
				SeriesHost: remoteWriterID, Tag: domainhistory.RecordTagHistory,
				AppliedIndex: 0,
			},
		}
	})
	oldRemote := remoteCreate(remoteWriterID, "0198d000-0000-7000-8000-0000000000d3", "old account", 0, testStart)
	oldPayload, err := json.Marshal(oldRemote.Entry)
	if err != nil {
		t.Fatalf("encode old remote entry: %v", err)
	}
	store.addRemoteRecord(domainhistory.Record{
		ID: oldRemote.ID, SeriesHost: oldRemote.SeriesHost, Tag: oldRemote.Tag,
		Index: oldRemote.Index, RecordedAt: oldRemote.RecordedAt, Version: oldRemote.Version,
		Kind: oldRemote.Kind, HistoryID: oldRemote.HistoryID, Payload: string(oldPayload),
		Direction: domainhistory.RecordDirectionRemote,
	})

	factory := fake.NewFactory(fake.Options{})
	service, _, _ := newEngine(t, store, factory)
	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Uploaded != 2 {
		t.Fatalf("uploaded = %d, want the two local records sent to the new account", result.Uploaded)
	}
	server := factory.ServerRecords()
	if len(server) != 2 {
		t.Fatalf("new account holds %d records, want only the local history", len(server))
	}
	for _, record := range server {
		if record.SeriesHost != testWriter {
			t.Fatalf("uploaded a record from the old account: %+v", record)
		}
	}
	for _, record := range store.snapshotRecords() {
		if record.SeriesHost == remoteWriterID && record.ID != oldRemote.ID {
			t.Fatalf("an old remote row was uploaded or rewritten: %+v", record)
		}
	}
}

// TestServerLostSuffixReuploadsItWithoutLeavingThePrefixPending proves a
// cleared upload cache is rebuilt from the server status: the acknowledged
// prefix is marked again, only the missing suffix is sent, and nothing stays
// reported as pending.
func TestServerLostSuffixReuploadsItWithoutLeavingThePrefixPending(t *testing.T) {
	store := newMemorySyncStore()
	first := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000f1", "one", testStart))
	second := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000f2", "two", testStart.Add(time.Minute)))
	third := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000f3", "three", testStart.Add(2*time.Minute)))
	store.setUploaded(first.ID, second.ID, third.ID)
	factory := fake.NewFactory(fake.Options{
		Statuses: []historysync.SeriesStatus{{
			Series: historysync.Series{
				SeriesHost: testWriter, Tag: domainhistory.RecordTagHistory,
			},
			MaxIndex: 1,
		}},
		StatusIgnoresRecords: true,
	})
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Uploaded != 1 {
		t.Fatalf("uploaded = %d, want only the missing suffix", result.Uploaded)
	}
	events := factory.Events()
	if len(events) != 1 || events[0] != "upload:"+testWriter+":2-2" {
		t.Fatalf("uploads = %v, want one batch at index 2", events)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want the acknowledged prefix re-marked", status.PendingUploads)
	}
	if got := store.checkpoint(testWriter).UploadedIndex; got != 2 {
		t.Fatalf("upload checkpoint = %d, want 2", got)
	}
	for _, record := range store.snapshotRecords() {
		if record.UploadedAt.IsZero() {
			t.Fatalf("record %d stayed pending: %+v", record.Index, store.snapshotRecords())
		}
	}
}

// TestLocalWriterSeriesAheadOnServerIsDownloaded proves a restored device
// fetches records the server already holds for its own writer series instead
// of skipping those indexes.
func TestLocalWriterSeriesAheadOnServerIsDownloaded(t *testing.T) {
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	first := store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000101", "one", testStart))
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000102", "two", testStart.Add(time.Minute)))
	serverOnly := []historysync.SyncRecord{
		remoteCreate(testWriter, "0198d000-0000-7000-8000-000000000103", "three", 2, testStart.Add(2*time.Minute)),
		remoteCreate(testWriter, "0198d000-0000-7000-8000-000000000104", "four", 3, testStart.Add(3*time.Minute)),
	}
	records := []historysync.SyncRecord{
		remoteCreate(testWriter, first.HistoryID, "one", 0, first.RecordedAt),
		remoteCreate(testWriter, "0198d000-0000-7000-8000-000000000102", "two", 1, testStart.Add(time.Minute)),
	}
	records = append(records, serverOnly...)
	var completed historysync.SyncResult
	factory := fake.NewFactory(fake.Options{Records: records})
	service, _, _ := newEngine(t, store, factory,
		historysync.WithSyncCompleted(func(result historysync.SyncResult) { completed = result }))

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Downloaded != 2 || result.Applied != 2 {
		t.Fatalf("result = %+v, want two downloaded records", result)
	}
	if completed.Applied != 2 {
		t.Fatalf("completion hook = %+v, want the applied count", completed)
	}
	for _, record := range serverOnly {
		entry, ok := store.snapshotEntries()[record.HistoryID]
		if !ok {
			t.Fatalf("server-only record %d was not applied", record.Index)
		}
		if entry.Source != domainhistory.SourceAtuinSync {
			t.Fatalf("entry %s source = %q", record.HistoryID, entry.Source)
		}
	}
	if got := store.checkpoint(testWriter).AppliedIndex; got != 3 {
		t.Fatalf("applied checkpoint = %d, want 3", got)
	}
}

// TestServerAcknowledgedPrefixIsRecordedWithoutResending proves the uploaded
// prefix, verified against the server's records after a re-login cleared the
// acknowledgment marks, is recorded without being sent again.
func TestServerAcknowledgedPrefixIsRecordedWithoutResending(t *testing.T) {
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	var serverPrefix []historysync.SyncRecord
	for index, command := range []string{"one", "two", "three"} {
		id := "0198d000-0000-7000-8000-0000000000e" + string(rune('1'+index))
		at := testStart.Add(time.Duration(index) * time.Minute)
		store.addLocalEntry(localEntry(id, command, at))
		if index < 2 {
			// The server already holds the first two commands; the local log
			// is the same events with the upload marks cleared.
			serverPrefix = append(serverPrefix, remoteCreate(testWriter, id, command, int64(index), at))
		}
	}
	factory := fake.NewFactory(fake.Options{
		Records: serverPrefix,
		Statuses: []historysync.SeriesStatus{{
			Series: historysync.Series{
				SeriesHost: testWriter, Tag: domainhistory.RecordTagHistory,
			},
			MaxIndex: 1,
		}},
		StatusIgnoresRecords: true,
	})
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Uploaded != 1 {
		t.Fatalf("uploaded = %d, want only the index the server lacks", result.Uploaded)
	}
	var uploads []string
	for _, event := range factory.Events() {
		if strings.HasPrefix(event, "upload:") {
			uploads = append(uploads, event)
		}
	}
	if len(uploads) != 1 || uploads[0] != "upload:"+testWriter+":2-2" {
		t.Fatalf("uploads = %v, want one batch starting at index 2", uploads)
	}
	if got := store.checkpoint(testWriter).UploadedIndex; got != 2 {
		t.Fatalf("upload checkpoint = %d, want the server high-water mark 2", got)
	}
	for _, record := range store.snapshotRecords() {
		if record.UploadedAt.IsZero() {
			t.Fatalf("record %d was not acknowledged, records %+v",
				record.Index, store.snapshotRecords())
		}
	}
}

func TestStoredAccountIdentityMustMatchTheRemoteAccount(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.Account = domainhistory.AccountIdentity{
			ServerURL: "https://another.example.test",
			UserID:    testUserID,
		}
	})
	service, _, _ := newEngine(t, store, factory)
	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrAccountMismatch) {
		t.Fatalf("SyncNow = %v, want ErrAccountMismatch", err)
	}
	if factory.Opens() != 0 {
		t.Fatalf("the engine opened %d remotes for a foreign account", factory.Opens())
	}
}

func TestSchedulingCoversStartupFocusIntervalCaptureAndManual(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	store := newMemorySyncStore()
	service, scheduler, _ := newEngine(t, store, factory)

	service.Start(context.Background())
	waitIdle(t, service)
	afterStartup := factory.Opens()
	if afterStartup == 0 {
		t.Fatal("startup did not run a sync")
	}

	service.NotifyFocus()
	if factory.Opens() != afterStartup {
		t.Fatal("focus synced before the debounce elapsed")
	}
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	if factory.Opens() != afterStartup+1 {
		t.Fatalf("focus opened %d remotes, want one more", factory.Opens()-afterStartup)
	}

	service.NotifyLocalChange()
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	if factory.Opens() != afterStartup+2 {
		t.Fatalf("capture opened %d remotes, want one more", factory.Opens()-afterStartup-1)
	}

	scheduler.Advance(5 * time.Minute)
	waitIdle(t, service)
	if factory.Opens() != afterStartup+3 {
		t.Fatalf("interval opened %d remotes, want one more", factory.Opens()-afterStartup-2)
	}

	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatalf("manual SyncNow: %v", err)
	}
	if factory.Opens() != afterStartup+4 {
		t.Fatalf("manual sync opened %d remotes, want one more", factory.Opens()-afterStartup-3)
	}
}

func TestConcurrentTriggersCoalesceIntoOneRun(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	factory := fake.NewFactory(fake.Options{
		UploadHook: func([]historysync.SyncRecord) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		},
	})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000000e1", "one", testStart))
	service, scheduler, _ := newEngine(t, store, factory)

	service.Start(context.Background())
	waitSignal(t, started)

	// Three concurrent reasons arrive while the startup upload is blocked:
	// a capture, a focus, and the interval tick. They must coalesce into one
	// follow-up run rather than three.
	service.NotifyLocalChange()
	service.NotifyFocus()
	scheduler.Advance(time.Second)
	scheduler.Advance(5 * time.Minute)
	close(release)

	waitIdle(t, service)
	if factory.Opens() != 2 {
		t.Fatalf("opens = %d, want startup plus one coalesced run", factory.Opens())
	}
}

// TestRestoredLocalCaptureIsReindexedAndUploaded reproduces the restoration
// scenario: an older log holds indexes 0-4 without any local upload
// acknowledgement, the server already stores indexes 5-6 for the same writer,
// and a command captured before the first download takes index 5. The server
// record must be applied, the unsent capture must be moved after the server's
// maximum, and the capture must reach the server. The restored 0-4 cannot be
// assumed present from the server's high-water mark either: they are sent too,
// so the server ends up holding the whole series.
func TestRestoredLocalCaptureIsReindexedAndUploaded(t *testing.T) {
	serverRecords := []historysync.SyncRecord{
		remoteCreate(testWriter, "0198d000-0000-7000-8000-00000000020a", "server five", 5, testStart.Add(5*time.Minute)),
		remoteCreate(testWriter, "0198d000-0000-7000-8000-00000000020b", "server six", 6, testStart.Add(6*time.Minute)),
	}
	factory := fake.NewFactory(fake.Options{Records: serverRecords})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	for index := 0; index < 5; index++ {
		store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000002a"+string(rune('0'+index)),
			"old", testStart.Add(time.Duration(index)*time.Minute)))
	}
	captured := store.addLocalEntry(localEntry(
		"0198d000-0000-7000-8000-0000000002b1", "captured after restore", testStart.Add(7*time.Minute)))
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Downloaded != 2 || result.Applied != 2 || result.Uploaded != 6 {
		t.Fatalf("result = %+v (events %v), want two downloaded and the five restored indexes plus the capture uploaded",
			result, factory.Events())
	}
	if _, ok := store.snapshotEntries()[serverRecords[0].HistoryID]; !ok {
		t.Fatal("the server's index-5 record was not applied")
	}
	var captureRecord domainhistory.Record
	for _, record := range store.snapshotRecords() {
		if record.ID == captured.ID {
			captureRecord = record
		}
	}
	if captureRecord.ID == "" {
		t.Fatal("the captured local record disappeared from the log")
	}
	if captureRecord.Index <= serverRecords[1].Index {
		t.Fatalf("capture index = %d, want it moved after the server maximum %d",
			captureRecord.Index, serverRecords[1].Index)
	}
	if captureRecord.UploadedAt.IsZero() {
		t.Fatalf("the capture was not acknowledged: %+v", captureRecord)
	}
	capturedOnServer := false
	for _, record := range factory.ServerRecords() {
		if record.ID == captured.ID && record.Index == captureRecord.Index {
			capturedOnServer = true
		}
	}
	if !capturedOnServer {
		t.Fatalf("the captured record never reached the server: %v", factory.Events())
	}
	if got := store.checkpoint(testWriter); got.AppliedIndex != 6 || got.UploadedIndex != captureRecord.Index {
		t.Fatalf("checkpoint = %+v, want applied 6 and uploaded %d", got, captureRecord.Index)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// TestRestoredLocalCapturesReachingServerMaxAreReindexed proves the applied
// checkpoint is never rebuilt from unacknowledged captures even when their
// indexes catch up with the server's high-water mark. A restored log with
// indexes 0-4 acknowledged holds two captures at 5-6 while different server
// records occupy those same indexes; the server suffix must still be
// downloaded, both captures moved after it, and both uploaded.
func TestRestoredLocalCapturesReachingServerMaxAreReindexed(t *testing.T) {
	serverRecords := []historysync.SyncRecord{
		remoteCreate(testWriter, "0198d000-0000-7000-8000-00000000020c", "server five", 5, testStart.Add(5*time.Minute)),
		remoteCreate(testWriter, "0198d000-0000-7000-8000-00000000020d", "server six", 6, testStart.Add(6*time.Minute)),
	}
	factory := fake.NewFactory(fake.Options{Records: serverRecords})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	var acknowledged []string
	for index := 0; index < 5; index++ {
		record := store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000002c"+string(rune('0'+index)),
			"old", testStart.Add(time.Duration(index)*time.Minute)))
		acknowledged = append(acknowledged, record.ID)
	}
	store.setUploaded(acknowledged...)
	captures := []domainhistory.Record{
		store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000002d1", "captured five", testStart.Add(7*time.Minute))),
		store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000002d2", "captured six", testStart.Add(8*time.Minute))),
	}
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Downloaded != 2 || result.Applied != 2 || result.Uploaded != 2 {
		t.Fatalf("result = %+v (events %v), want two downloaded and both captures uploaded",
			result, factory.Events())
	}
	if _, ok := store.snapshotEntries()[serverRecords[0].HistoryID]; !ok {
		t.Fatal("the server's index-5 record was not applied")
	}
	stored := make(map[string]domainhistory.Record)
	for _, record := range store.snapshotRecords() {
		stored[record.ID] = record
	}
	for _, capture := range captures {
		record, ok := stored[capture.ID]
		if !ok {
			t.Fatalf("captured record %s disappeared from the log", capture.ID)
		}
		if record.Index <= 6 {
			t.Fatalf("capture index = %d, want it moved after the server maximum 6", record.Index)
		}
		if record.UploadedAt.IsZero() {
			t.Fatalf("the capture was not acknowledged: %+v", record)
		}
	}
	sent := make(map[string]bool)
	for _, record := range factory.ServerRecords() {
		sent[record.ID] = true
	}
	for _, capture := range captures {
		if !sent[capture.ID] {
			t.Fatalf("capture %s never reached the server: %v", capture.ID, factory.Events())
		}
	}
	if got := store.checkpoint(testWriter); got.AppliedIndex != 6 || got.UploadedIndex != 8 {
		t.Fatalf("checkpoint = %+v, want applied 6 and uploaded 8", got)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// TestServerSeriesHoleWithLocalCaptureIsFilled models a pinned server whose
// writer series has a hole at index 5 while the local log holds an unsent
// capture there. The download may use the local capture to skip the gap, but
// the server's high-water mark must not turn that capture into an
// acknowledgement: it must actually be uploaded into the hole.
func TestServerSeriesHoleWithLocalCaptureIsFilled(t *testing.T) {
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	var acknowledged []string
	server := make([]historysync.SyncRecord, 0, 7)
	for index := 0; index < 5; index++ {
		id := "0198d000-0000-7000-8000-0000000003a" + string(rune('0'+index))
		at := testStart.Add(time.Duration(index) * time.Minute)
		record := store.addLocalEntry(localEntry(id, "old", at))
		acknowledged = append(acknowledged, record.ID)
		server = append(server, remoteCreate(testWriter, id, "old", int64(index), at))
	}
	store.setUploaded(acknowledged...)
	captured := store.addLocalEntry(localEntry(
		"0198d000-0000-7000-8000-0000000003b1", "capture in the server hole",
		testStart.Add(7*time.Minute)))
	server = append(server, remoteCreate(testWriter,
		"0198d000-0000-7000-8000-0000000003d6", "server six", 6, testStart.Add(6*time.Minute)))
	factory := fake.NewFactory(fake.Options{Records: server})
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Downloaded != 1 || result.Applied != 1 || result.Uploaded != 1 {
		t.Fatalf("result = %+v (events %v), want the server record applied and the capture uploaded",
			result, factory.Events())
	}
	stored, ok := recordByID(store.snapshotRecords(), captured.ID)
	if !ok {
		t.Fatal("captured record vanished")
	}
	if stored.Index != 5 || stored.UploadedAt.IsZero() {
		t.Fatalf("capture = %+v, want index 5 and acknowledged only by a real upload", stored)
	}
	serverRecord, onServer := syncRecordByID(factory.ServerRecords(), captured.ID)
	if !onServer {
		t.Fatalf("capture %s was acknowledged but never reached the server: %v",
			captured.ID, factory.Events())
	}
	if serverRecord.Index != 5 {
		t.Fatalf("server capture index = %d, want the hole at 5", serverRecord.Index)
	}
	if got := store.checkpoint(testWriter); got.AppliedIndex != 6 || got.UploadedIndex != 6 {
		t.Fatalf("checkpoint = %+v, want applied 6 and uploaded 6", got)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// TestServerSeriesHoleWithoutLocalCaptureFailsClosed proves a server hole
// with no local record to fill it stops the sync instead of advancing past a
// record this device never received.
func TestServerSeriesHoleWithoutLocalCaptureFailsClosed(t *testing.T) {
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	var acknowledged []string
	server := make([]historysync.SyncRecord, 0, 6)
	for index := 0; index < 5; index++ {
		id := "0198d000-0000-7000-8000-0000000003e" + string(rune('0'+index))
		at := testStart.Add(time.Duration(index) * time.Minute)
		record := store.addLocalEntry(localEntry(id, "old", at))
		acknowledged = append(acknowledged, record.ID)
		server = append(server, remoteCreate(testWriter, id, "old", int64(index), at))
	}
	store.setUploaded(acknowledged...)
	server = append(server, remoteCreate(testWriter,
		"0198d000-0000-7000-8000-0000000003f6", "server six", 6, testStart.Add(6*time.Minute)))
	factory := fake.NewFactory(fake.Options{Records: server})
	service, _, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrIncompletePage) {
		t.Fatalf("SyncNow = %v, want ErrIncompletePage for the unfilled hole", err)
	}
	if calls := factory.Last().Calls(); calls.Upload != 0 {
		t.Fatalf("upload calls = %d, want none after a failed download", calls.Upload)
	}
	for _, record := range store.snapshotRecords() {
		if record.SeriesHost != testWriter {
			continue
		}
		if record.UploadedAt.IsZero() {
			t.Fatalf("record at index %d was left unacknowledged: %+v", record.Index, record)
		}
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
	if checkpoint := store.checkpoint(testWriter); checkpoint.AppliedIndex > 4 {
		t.Fatalf("applied checkpoint = %d, want no advance past the trusted prefix", checkpoint.AppliedIndex)
	}
}

// TestServerSeriesMultipleHolesWithLocalCapturesAreFilled proves every hole
// in the server series that a local capture covers is uploaded, including
// holes in the middle of a page, and that the download applies the records on
// either side.
func TestServerSeriesMultipleHolesWithLocalCapturesAreFilled(t *testing.T) {
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	var acknowledged []string
	server := make([]historysync.SyncRecord, 0, 6)
	for index := 0; index < 4; index++ {
		id := "0198d000-0000-7000-8000-0000000004a" + string(rune('0'+index))
		at := testStart.Add(time.Duration(index) * time.Minute)
		record := store.addLocalEntry(localEntry(id, "old", at))
		acknowledged = append(acknowledged, record.ID)
		server = append(server, remoteCreate(testWriter, id, "old", int64(index), at))
	}
	store.setUploaded(acknowledged...)
	captures := []domainhistory.Record{
		store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000004b1", "capture four", testStart.Add(4*time.Minute))),
		store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000004b2", "capture five", testStart.Add(5*time.Minute))),
		store.addLocalEntry(localEntry(
			"0198d000-0000-7000-8000-0000000004b3", "capture seven", testStart.Add(7*time.Minute))),
	}
	// The third capture was appended at index 6; move it to 7 so the server's
	// record at 6 and its hole at 7 model two separate gaps.
	thirdID := captures[2].ID
	store.mu.Lock()
	for index := range store.records {
		if store.records[index].ID == thirdID {
			store.records[index].Index = 7
		}
	}
	store.mu.Unlock()
	captures[2].Index = 7
	server = append(server,
		remoteCreate(testWriter, "0198d000-0000-7000-8000-0000000004c6", "server six", 6, testStart.Add(6*time.Minute)),
		remoteCreate(testWriter, "0198d000-0000-7000-8000-0000000004c8", "server eight", 8, testStart.Add(8*time.Minute)))
	factory := fake.NewFactory(fake.Options{Records: server})
	service, _, _ := newEngine(t, store, factory)

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if result.Applied != 2 || result.Uploaded != 3 {
		t.Fatalf("result = %+v (events %v), want two applied and three captures uploaded",
			result, factory.Events())
	}
	records := store.snapshotRecords()
	for _, capture := range captures {
		stored, ok := recordByID(records, capture.ID)
		if !ok {
			t.Fatalf("capture %s vanished", capture.ID)
		}
		if stored.UploadedAt.IsZero() {
			t.Fatalf("capture %s was not acknowledged: %+v", capture.ID, stored)
		}
		serverRecord, onServer := syncRecordByID(factory.ServerRecords(), capture.ID)
		if !onServer {
			t.Fatalf("capture %s was acknowledged but never reached the server: %v",
				capture.ID, factory.Events())
		}
		if serverRecord.Index != stored.Index {
			t.Fatalf("capture %s server index %d, local %d", capture.ID, serverRecord.Index, stored.Index)
		}
	}
	if got := store.checkpoint(testWriter); got.AppliedIndex != 8 || got.UploadedIndex != 8 {
		t.Fatalf("checkpoint = %+v, want applied 8 and uploaded 8", got)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// recordByID and syncRecordByID are small lookup helpers for the hole tests.
func recordByID(records []domainhistory.Record, id string) (domainhistory.Record, bool) {
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
	}
	return domainhistory.Record{}, false
}

func syncRecordByID(records []historysync.SyncRecord, id string) (historysync.SyncRecord, bool) {
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
	}
	return historysync.SyncRecord{}, false
}

// TestAppliedRowsNotifyObserversWhenUploadFails proves downloaded projections
// are announced as soon as they commit, even though the completion callback
// still reports only a fully successful run.
func TestAppliedRowsNotifyObserversWhenUploadFails(t *testing.T) {
	remote := remoteCreate(remoteWriterID,
		"0198d000-0000-7000-8000-000000000281", "remote", 0, testStart)
	factory := fake.NewFactory(fake.Options{
		Records:   []historysync.SyncRecord{remote},
		UploadErr: historysync.ErrOffline,
	})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-000000000282", "local", testStart))
	var applied []historysync.SyncResult
	completed := 0
	service, _, _ := newEngine(t, store, factory,
		historysync.WithSyncApplied(func(result historysync.SyncResult) {
			applied = append(applied, result)
		}),
		historysync.WithSyncCompleted(func(historysync.SyncResult) { completed++ }))

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("SyncNow = %v, want ErrOffline", err)
	}
	if _, ok := store.snapshotEntries()[remote.HistoryID]; !ok {
		t.Fatal("the remote row was not applied")
	}
	if len(applied) != 1 || applied[0].Applied == 0 {
		t.Fatalf("applied callbacks = %+v, want one with the committed rows", applied)
	}
	if completed != 0 {
		t.Fatalf("completion callback ran %d times for a failed run", completed)
	}
}

// TestAppLockPausesEveryAutomaticTrigger proves a locked UI is a pause: no
// trigger opens a remote until the gate opens again.
func TestAppLockPausesEveryAutomaticTrigger(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	store := newMemorySyncStore()
	var locked atomic.Bool
	locked.Store(true)
	service, scheduler, _ := newEngine(t, store, factory,
		historysync.WithAppLocked(func() bool { return locked.Load() }))

	service.Start(context.Background())
	waitIdle(t, service)
	if factory.Opens() != 0 {
		t.Fatalf("startup opened %d remotes while App Lock was engaged", factory.Opens())
	}
	service.NotifyFocus()
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	service.NotifyLocalChange()
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	scheduler.Advance(5 * time.Minute)
	waitIdle(t, service)
	if factory.Opens() != 0 {
		t.Fatalf("automatic triggers opened %d remotes while App Lock was engaged", factory.Opens())
	}
	status := service.Status()
	if status.State != atuinmodel.SyncLocked || status.Problem != atuinmodel.ProblemLocked ||
		!status.NextRetryAt.IsZero() {
		t.Fatalf("status = %+v, want locked with no retry", status)
	}

	locked.Store(false)
	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatalf("SyncNow after unlock: %v", err)
	}
	if factory.Opens() != 1 {
		t.Fatalf("opens after unlock = %d, want one", factory.Opens())
	}
}

// TestSecretsLockedDuringRunKeepsLockedStatus proves the final status is not
// overwritten with idle when the field-secret store locks mid-run.
func TestSecretsLockedDuringRunKeepsLockedStatus(t *testing.T) {
	remote := remoteCreate(remoteWriterID,
		"0198d000-0000-7000-8000-000000000291", "remote", 0, testStart)
	var secrets *syncSecrets
	factory := fake.NewFactory(fake.Options{
		Records: []historysync.SyncRecord{remote},
		DownloadHook: func(historysync.Series, int64, int) {
			secrets.mode = historysync.SecretModePassphraseLocked
		},
	})
	store := newMemorySyncStore()
	service, _, engineSecrets := newEngine(t, store, factory)
	secrets = engineSecrets

	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if _, ok := store.snapshotEntries()[remote.HistoryID]; !ok {
		t.Fatal("the downloaded row was not applied before the lock")
	}
	status := service.Status()
	if status.State != atuinmodel.SyncLocked || status.Problem != atuinmodel.ProblemLocked {
		t.Fatalf("status = %+v, want the locked state preserved", status)
	}
	if !status.NextRetryAt.IsZero() {
		t.Fatalf("next retry = %v, want none while locked", status.NextRetryAt)
	}
}

// TestTerminalAuthErrorWaitsForUserUntilConfigurationChanges proves automatic
// triggers stop opening remotes after an auth failure, while a manual retry
// and a real configuration change still resume work.
func TestTerminalAuthErrorWaitsForUserUntilConfigurationChanges(t *testing.T) {
	factory := fake.NewFactory(fake.Options{AuthErr: historysync.ErrAuthExpired})
	store := newMemorySyncStore()
	service, scheduler, _ := newEngine(t, store, factory)

	service.Start(context.Background())
	waitIdle(t, service)
	opened := factory.Opens()
	if opened == 0 {
		t.Fatal("the startup run did not open a remote")
	}
	service.NotifyFocus()
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	service.NotifyLocalChange()
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	scheduler.Advance(5 * time.Minute)
	waitIdle(t, service)
	if factory.Opens() != opened {
		t.Fatalf("automatic triggers opened %d remotes after an auth failure", factory.Opens()-opened)
	}
	status := service.Status()
	if status.State != atuinmodel.SyncAuthExpired || !status.NextRetryAt.IsZero() {
		t.Fatalf("status = %+v, want auth expired with no retry", status)
	}

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrAuthExpired) {
		t.Fatalf("manual SyncNow = %v, want a manual retry to hit the remote", err)
	}
	if factory.Opens() != opened+1 {
		t.Fatalf("opens after manual retry = %d, want %d", factory.Opens(), opened+1)
	}
	// The manual failure re-arms the park; focus stays quiet until a real
	// configuration change.
	service.NotifyFocus()
	scheduler.Advance(time.Second)
	waitIdle(t, service)
	if factory.Opens() != opened+1 {
		t.Fatalf("focus opened %d remotes after the manual retry", factory.Opens()-opened-1)
	}

	service.NotifyConfigurationChanged()
	scheduler.Advance(5 * time.Minute)
	waitIdle(t, service)
	if factory.Opens() != opened+2 {
		t.Fatalf("opens after a configuration change = %d, want %d", factory.Opens(), opened+2)
	}
}

// TestStopCancelsAnInFlightManualRun proves shutdown cancels work started by a
// manual SyncNow whose caller passed context.Background().
func TestStopCancelsAnInFlightManualRun(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	factory := fake.NewFactory(fake.Options{
		UploadHook: func([]historysync.SyncRecord) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		},
	})
	store := newMemorySyncStore()
	store.setMetadata(func(metadata *domainhistory.Metadata) {
		metadata.UnverifiedKeyConfirmed = true
	})
	store.addLocalEntry(localEntry("0198d000-0000-7000-8000-0000000002c1", "one", testStart))
	service, _, _ := newEngine(t, store, factory)

	results := make(chan error, 1)
	go func() {
		_, err := service.SyncNow(context.Background())
		results <- err
	}()
	waitSignal(t, started)
	service.Stop()
	close(release)

	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SyncNow after Stop = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the in-flight manual run")
	}
}

// TestAddressChangedAfterStatusSnapshotFailsClosed proves credentials are
// never opened for an address the stored account does not own even when the
// setting changes between the status snapshot and the remote open.
func TestAddressChangedAfterStatusSnapshotFailsClosed(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	store := newMemorySyncStore()
	settings := &flipSyncSettings{
		syncSettings: newSyncSettings(),
		alternate:    "https://attacker.example.test",
	}
	service := historysync.NewService(settings, newSyncSecrets(), store, factory,
		historysync.WithScheduler(newManualScheduler()),
		historysync.WithJitter(identityJitter))
	t.Cleanup(service.Stop)

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrAccountMismatch) {
		t.Fatalf("SyncNow = %v, want ErrAccountMismatch", err)
	}
	if factory.Opens() != 0 {
		t.Fatalf("the engine opened %d remotes for a changed address", factory.Opens())
	}
	status := service.Status()
	if status.State != atuinmodel.SyncError || !status.NextRetryAt.IsZero() {
		t.Fatalf("status = %+v, want a non-retrying error", status)
	}
}

// TestStoredAccountUserIDMismatchFailsClosedBeforeExchange covers the same
// server address with a different user id.
func TestStoredAccountUserIDMismatchFailsClosedBeforeExchange(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		Account: historysync.Account{ServerURL: testServerURL, UserID: "a-different-account"},
	})
	store := newMemorySyncStore()
	record := store.addLocalEntry(localEntry(
		"0198d000-0000-7000-8000-0000000002d1", "one", testStart))
	service, _, _ := newEngine(t, store, factory)

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrAccountMismatch) {
		t.Fatalf("SyncNow = %v, want ErrAccountMismatch", err)
	}
	calls := factory.Last().Calls()
	if calls.Download != 0 || calls.Upload != 0 {
		t.Fatalf("calls = %+v, want no records exchanged with a different user", calls)
	}
	for _, stored := range store.snapshotRecords() {
		if stored.ID == record.ID && !stored.UploadedAt.IsZero() {
			t.Fatalf("record was acknowledged for the wrong account: %+v", stored)
		}
	}
	status := service.Status()
	if status.State != atuinmodel.SyncError || !status.NextRetryAt.IsZero() {
		t.Fatalf("status = %+v, want a non-retrying account error", status)
	}
}

// TestStoredAccountChangedAfterSnapshotFailsBeforeCredentialRead flips the
// stored account on the same server immediately after the status snapshot.
// The engine must fail closed before it reads a credential or opens a remote,
// even though the stale snapshot still names the configured address.
func TestStoredAccountChangedAfterSnapshotFailsBeforeCredentialRead(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		Account: historysync.Account{ServerURL: testServerURL, UserID: "a-different-account"},
	})
	store := newAccountChangeStore(newMemorySyncStore(), domainhistory.AccountIdentity{
		ServerURL: testServerURL, UserID: "a-different-account",
	})
	secrets := newCountingSecrets(newSyncSecrets())
	service := historysync.NewService(newSyncSettings(), secrets, store, factory,
		historysync.WithScheduler(newManualScheduler()),
		historysync.WithJitter(identityJitter))
	t.Cleanup(service.Stop)

	if _, err := service.SyncNow(context.Background()); !errors.Is(err, historysync.ErrAccountMismatch) {
		t.Fatalf("SyncNow = %v, want ErrAccountMismatch", err)
	}
	if factory.Opens() != 0 {
		t.Fatalf("the engine opened %d remotes before noticing the stored account change",
			factory.Opens())
	}
	if reads := secrets.Reads(); reads != 0 {
		t.Fatalf("the engine read %d credentials after the stored account changed", reads)
	}
	status := service.Status()
	if status.State != atuinmodel.SyncError || !status.NextRetryAt.IsZero() {
		t.Fatalf("status = %+v, want a non-retrying account error", status)
	}
}

// accountChangeStore rewrites the stored account right after the first
// HistoryMetadata read, modelling another actor changing the account between
// the status snapshot and the credential exchange.
type accountChangeStore struct {
	*memorySyncStore
	account domainhistory.AccountIdentity
	once    sync.Once
}

func newAccountChangeStore(inner *memorySyncStore, account domainhistory.AccountIdentity) *accountChangeStore {
	return &accountChangeStore{memorySyncStore: inner, account: account}
}

func (s *accountChangeStore) HistoryMetadata(ctx context.Context) (domainhistory.Metadata, error) {
	metadata, err := s.memorySyncStore.HistoryMetadata(ctx)
	s.once.Do(func() {
		s.memorySyncStore.setMetadata(func(stored *domainhistory.Metadata) {
			stored.Account = s.account
		})
	})
	return metadata, err
}

// countingSecrets counts credential reads so a test can prove none happened.
type countingSecrets struct {
	*syncSecrets
	mu    sync.Mutex
	reads int
}

func newCountingSecrets(inner *syncSecrets) *countingSecrets {
	return &countingSecrets{syncSecrets: inner}
}

func (s *countingSecrets) Get(ctx context.Context, ref historysync.SecretRef) (string, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.syncSecrets.Get(ctx, ref)
}

func (s *countingSecrets) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// flipSyncSettings changes the persisted address after the first read, which
// models the user editing Settings between the status snapshot and the remote
// open.
type flipSyncSettings struct {
	*syncSettings
	mu        sync.Mutex
	alternate string
	reads     int
}

func (s *flipSyncSettings) HistorySettings() domainpreferences.History {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	history := s.syncSettings.history
	if s.reads > 1 {
		history.SyncAddress = s.alternate
	}
	return history
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedKeys(values map[string]domainhistory.Entry) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
