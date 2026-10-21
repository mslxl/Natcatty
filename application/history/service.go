// Package history records connection logs and shell/command history, applies
// the configured retention, and exposes one change signal to the UI.
//
// The service owns no storage and no UI: adapters implement Store, and the
// composition root supplies the retention read from Settings. Command history
// is also the journal autocomplete aggregates, so both surfaces share one
// persisted record instead of forking it.
package history

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// ErrInvalid reports a record that cannot be persisted.
var ErrInvalid = errors.New("history: invalid record")

// Store is the persistence port the Vault database implements.
type Store interface {
	ListConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error)
	SaveConnectionLog(context.Context, domainhistory.ConnectionLog) error
	DeleteConnectionLog(context.Context, string) error
	ClearConnectionLogs(context.Context) (int, error)
	PruneConnectionLogs(context.Context, domainhistory.Retention, time.Time) (int, error)

	ListHistoryEntries(context.Context) ([]domainhistory.Entry, error)
	SaveHistoryEntry(context.Context, domainhistory.Entry) error
	DeleteHistoryEntry(context.Context, string) error
	ClearHistoryEntries(context.Context) (int, error)
	PruneHistoryEntries(context.Context, domainhistory.Retention, time.Time) (int, error)
}

// Retention is the configured bound for both record families.
type Retention struct {
	Logs    domainhistory.Retention
	History domainhistory.Retention
}

// PruneResult reports how many records retention removed.
type PruneResult struct {
	ConnectionLogs int
	Entries        int
}

// Option customizes a Service.
type Option func(*Service)

// WithRetention supplies the live retention. The function is read on every
// record and prune, so a Settings change applies without rebuilding.
func WithRetention(fn func() Retention) Option {
	return func(s *Service) { s.retention = fn }
}

// WithClock replaces the clock. Tests use it to make timestamps and age
// pruning deterministic.
func WithClock(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.now = fn
		}
	}
}

// WithIDGenerator replaces record identity generation. Tests use it for
// deterministic ordering.
func WithIDGenerator(fn func() string) Option {
	return func(s *Service) {
		if fn != nil {
			s.newID = fn
		}
	}
}

// Service records, lists, deletes, and prunes both record families. It is safe
// for concurrent use: command recording runs on a Session goroutine while the
// UI lists.
type Service struct {
	store     Store
	retention func() Retention
	now       func() time.Time
	newID     func() string

	mu         sync.Mutex
	revision   uint64
	listeners  map[int]func()
	nextListen int
}

// NewService returns a Service over store. A nil retention keeps every
// record.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{
		store:     store,
		now:       time.Now,
		newID:     newRecordID,
		listeners: make(map[int]func()),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// RecordConnection stores one connection attempt and applies log retention.
func (s *Service) RecordConnection(ctx context.Context, log domainhistory.ConnectionLog) error {
	if s.store == nil {
		return ErrInvalid
	}
	log = domainhistory.NormalizeConnectionLog(log)
	if log.ID == "" {
		log.ID = s.newID()
	}
	if log.StartedAt.IsZero() {
		log.StartedAt = s.now()
	}
	if log.EndedAt.IsZero() {
		log.EndedAt = s.now()
	}
	if !log.Valid() {
		return ErrInvalid
	}
	if err := s.store.SaveConnectionLog(ctx, log); err != nil {
		return err
	}
	if retention := s.retentionFor().Logs; retention.Bounded() {
		if _, err := s.store.PruneConnectionLogs(ctx, retention, s.now()); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}

// Record stores one command execution and applies history retention. It is
// autocomplete's journal entry point.
func (s *Service) Record(ctx context.Context, entry domainhistory.Entry) error {
	if s.store == nil {
		return ErrInvalid
	}
	entry = domainhistory.NormalizeEntry(entry)
	if entry.ID == "" {
		entry.ID = s.newID()
	}
	if entry.Scope == "" {
		entry.Scope = domainhistory.NormalizeScope(entry.HostID)
	}
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = s.now()
	}
	if !entry.Valid() {
		return ErrInvalid
	}
	if err := s.store.SaveHistoryEntry(ctx, entry); err != nil {
		return err
	}
	if retention := s.retentionFor().History; retention.Bounded() {
		if _, err := s.store.PruneHistoryEntries(ctx, retention, s.now()); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}

// Load returns the persisted command history newest first. It implements
// autocomplete's journal.
func (s *Service) Load(ctx context.Context) ([]domainhistory.Entry, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListHistoryEntries(ctx)
}

// Entries is Load under the history surface's vocabulary.
func (s *Service) Entries(ctx context.Context) ([]domainhistory.Entry, error) {
	return s.Load(ctx)
}

// ConnectionLogs returns the persisted connection logs newest first.
func (s *Service) ConnectionLogs(ctx context.Context) ([]domainhistory.ConnectionLog, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListConnectionLogs(ctx)
}

// DeleteConnectionLog removes one log.
func (s *Service) DeleteConnectionLog(ctx context.Context, id string) error {
	if s.store == nil {
		return ErrInvalid
	}
	if err := s.store.DeleteConnectionLog(ctx, id); err != nil {
		return err
	}
	s.notify()
	return nil
}

// DeleteEntry removes one recorded command.
func (s *Service) DeleteEntry(ctx context.Context, id string) error {
	if s.store == nil {
		return ErrInvalid
	}
	if err := s.store.DeleteHistoryEntry(ctx, id); err != nil {
		return err
	}
	s.notify()
	return nil
}

// ClearConnectionLogs removes every connection log and reports how many were
// deleted. Command history is a separate record family and is untouched.
func (s *Service) ClearConnectionLogs(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, ErrInvalid
	}
	removed, err := s.store.ClearConnectionLogs(ctx)
	if err != nil {
		return 0, err
	}
	s.notify()
	return removed, nil
}

// ClearHistory removes every recorded command and reports how many were
// deleted.
func (s *Service) ClearHistory(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, ErrInvalid
	}
	removed, err := s.store.ClearHistoryEntries(ctx)
	if err != nil {
		return 0, err
	}
	s.notify()
	return removed, nil
}

// Prune applies the configured retention to both record families.
func (s *Service) Prune(ctx context.Context) (PruneResult, error) {
	if s.store == nil {
		return PruneResult{}, ErrInvalid
	}
	retention := s.retentionFor()
	now := s.now()
	result := PruneResult{}
	if retention.Logs.Bounded() {
		removed, err := s.store.PruneConnectionLogs(ctx, retention.Logs, now)
		if err != nil {
			return result, err
		}
		result.ConnectionLogs = removed
	}
	if retention.History.Bounded() {
		removed, err := s.store.PruneHistoryEntries(ctx, retention.History, now)
		if err != nil {
			return result, err
		}
		result.Entries = removed
	}
	if result.ConnectionLogs > 0 || result.Entries > 0 {
		s.notify()
	}
	return result, nil
}

// Subscribe registers fn for record and prune changes. It returns an
// unsubscribe function. The callback runs outside the service lock and only
// asks the UI for a redraw.
func (s *Service) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	id := s.nextListen
	s.nextListen++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Revision increments on every change, so a UI that misses a callback can
// still observe that a reload is needed.
func (s *Service) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

func (s *Service) notify() {
	s.mu.Lock()
	s.revision++
	listeners := make([]func(), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (s *Service) retentionFor() Retention {
	if s.retention == nil {
		return Retention{}
	}
	return s.retention()
}

// newRecordID returns a time-prefixed random identity, so IDs sort in
// recording order and stay unique across processes.
func newRecordID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		// crypto/rand does not fail on supported platforms; the timestamp and
		// a zero suffix still keep the record usable in unusual sandboxes.
		return fmt.Sprintf("%013x0000000000000000", time.Now().UnixMilli())
	}
	return fmt.Sprintf("%013x%s", time.Now().UnixMilli(), hex.EncodeToString(random[:]))
}
