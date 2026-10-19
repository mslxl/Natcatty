// Package forwarding owns Port Forwarding Rule use cases: rule CRUD, the
// runtime manager that starts and stops local, remote, and dynamic forwards,
// auto-start, and bounded auto-reconnect. It declares the repository and
// connection ports it consumes, so storage and SSH adapters implement them
// without this package knowing either.
//
// Runtime status is a projection: the repository stores rules only, and a
// restart always begins inactive.
package forwarding

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

// Original runtime behavior preserved from the Electron implementation.
const (
	// ReconnectAttempts is the bounded number of auto-reconnect attempts.
	ReconnectAttempts = 5
	// ReconnectDelay is the pause between auto-reconnect attempts.
	ReconnectDelay = 3 * time.Second
	// UnforwardTimeout bounds how long a stop waits for a remote listener to
	// release its forward before the transport is discarded.
	UnforwardTimeout = 5 * time.Second
	// SocksIdleTimeout closes an idle dynamic SOCKS5 connection.
	SocksIdleTimeout = 15 * time.Second
	// orderStep spaces rules so later inserts can be placed between them.
	orderStep = 1000
)

// RuleRepository is the persistence port the manager consumes.
type RuleRepository interface {
	ListForwardingRules(ctx context.Context) ([]domainforward.Rule, error)
	GetForwardingRule(ctx context.Context, id model.ForwardingRuleID) (domainforward.Rule, error)
	SaveForwardingRule(ctx context.Context, rule domainforward.Rule) error
	DeleteForwardingRule(ctx context.Context, id model.ForwardingRuleID) error
}

// Connection is the forwarding capability of one authenticated Connection
// lease. DialContext opens a channel through the SSH Host; ListenTCP asks the
// SSH Host to listen for remote forwarding. Release returns a healthy lease to
// the pool; Discard abandons a transport that can no longer be trusted.
type Connection interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	ListenTCP(address string) (net.Listener, error)
	Release()
	Discard()
}

// Connector opens a Connection for a Host. The app adapts the shared
// connection Manager to this port; tests provide a fake.
type Connector interface {
	Connect(ctx context.Context, hostID model.HostID) (Connection, error)
}

// View is one rule and its runtime projection.
type View struct {
	Rule   domainforward.Rule
	Status domainforward.Status
	// Error is the localized-error identifier for StatusError.
	Error ErrorKind
	// Detail is a developer diagnostic, never the primary UI message.
	Detail string
	Hop    *Hop
	// Attempts is the number of auto-reconnect attempts scheduled since the
	// last successful run.
	Attempts int
}

// Snapshot is the atomic runtime view. Epoch changes only when the manager is
// replaced; Revision is monotonically increasing, so a consumer can discard a
// stale event.
type Snapshot struct {
	Epoch    uint64
	Revision uint64
	Rules    []View
}

// EventType distinguishes a rule upsert from a removal.
type EventType string

const (
	EventUpsert EventType = "upsert"
	EventRemove EventType = "remove"
)

// Event is one runtime change. It carries the whole View so a subscriber can
// ignore a stale revision without calling back into the manager.
type Event struct {
	Epoch    uint64
	Revision uint64
	Type     EventType
	ID       model.ForwardingRuleID
	View     View
}

// Option customizes a Manager.
type Option func(*Manager)

// WithClock sets the manager's source of time.
func WithClock(fn func() time.Time) Option {
	return func(m *Manager) {
		if fn != nil {
			m.now = fn
		}
	}
}

// WithIDGenerator sets how the manager names rules.
func WithIDGenerator(fn func() model.ForwardingRuleID) Option {
	return func(m *Manager) {
		if fn != nil {
			m.newID = fn
		}
	}
}

// WithScheduler replaces one-shot delays. Tests use it to fire reconnect
// attempts and the unforward deadline deterministically.
func WithScheduler(fn func(time.Duration, func()) (stop func())) Option {
	return func(m *Manager) {
		if fn != nil {
			m.schedule = fn
		}
	}
}

// WithListen replaces the local TCP listener. Tests use it to force bind
// failures.
func WithListen(fn func(network, address string) (net.Listener, error)) Option {
	return func(m *Manager) {
		if fn != nil {
			m.listen = fn
		}
	}
}

// WithLocalDial replaces the local destination dial used by remote rules.
func WithLocalDial(fn func(network, address string) (net.Conn, error)) Option {
	return func(m *Manager) {
		if fn != nil {
			m.dialLocal = fn
		}
	}
}

// WithTimeouts overrides the unforward and SOCKS idle timeouts. Zero or
// negative values keep the original defaults.
func WithTimeouts(unforward, socksIdle time.Duration) Option {
	return func(m *Manager) {
		if unforward > 0 {
			m.unforwardTimeout = unforward
		}
		if socksIdle > 0 {
			m.socksIdleTimeout = socksIdle
		}
	}
}

// Manager implements the forwarding use cases.
type Manager struct {
	repo      RuleRepository
	connector Connector
	now       func() time.Time
	newID     func() model.ForwardingRuleID
	schedule  func(time.Duration, func()) (stop func())
	listen    func(network, address string) (net.Listener, error)
	dialLocal func(network, address string) (net.Conn, error)

	unforwardTimeout time.Duration
	socksIdleTimeout time.Duration

	mu          sync.Mutex
	runtime     map[model.ForwardingRuleID]*runtimeEntry
	subscribers map[int]func(Event)
	nextSub     int
	revision    uint64
	epoch       uint64
	closed      bool

	events chan Event
	done   chan struct{}
}

type runtimeEntry struct {
	view          View
	generation    uint64
	stopCh        chan struct{}
	reconnectStop func()
	conn          Connection
	listener      net.Listener
}

// NewManager returns a Manager over its ports.
func NewManager(repo RuleRepository, connector Connector, opts ...Option) *Manager {
	manager := &Manager{
		repo:             repo,
		connector:        connector,
		now:              time.Now,
		newID:            newForwardingRuleID,
		unforwardTimeout: UnforwardTimeout,
		socksIdleTimeout: SocksIdleTimeout,
		runtime:          make(map[model.ForwardingRuleID]*runtimeEntry),
		subscribers:      make(map[int]func(Event)),
		epoch:            nextEpoch(),
		events:           make(chan Event, 1024),
		done:             make(chan struct{}),
	}
	for _, opt := range opts {
		opt(manager)
	}
	if manager.schedule == nil {
		manager.schedule = func(duration time.Duration, fn func()) func() {
			timer := time.AfterFunc(duration, fn)
			return func() { timer.Stop() }
		}
	}
	if manager.listen == nil {
		manager.listen = net.Listen
	}
	if manager.dialLocal == nil {
		manager.dialLocal = net.Dial
	}
	go manager.dispatch()
	return manager
}

// Create validates and persists a new rule.
func (m *Manager) Create(ctx context.Context, draft domainforward.Draft) (domainforward.Rule, error) {
	if m.repo == nil {
		return domainforward.Rule{}, internal(errors.New("forwarding: no rule repository"))
	}
	normalized, err := domainforward.NormalizeDraft(draft)
	if err != nil {
		return domainforward.Rule{}, invalid(err)
	}
	existing, err := m.repo.ListForwardingRules(ctx)
	if err != nil {
		return domainforward.Rule{}, internal(err)
	}
	now := m.now()
	rule := domainforward.ApplyDraft(domainforward.Rule{
		ID:        m.newID(),
		OrderKey:  nextOrder(existing),
		CreatedAt: now,
		UpdatedAt: now,
	}, normalized)
	if err := m.repo.SaveForwardingRule(ctx, rule); err != nil {
		return domainforward.Rule{}, internal(err)
	}
	m.upsertRule(rule)
	return rule, nil
}

// Update replaces a rule's editable fields. A running rule is stopped first;
// the saved rule is always inactive after an edit, so a changed bind address
// cannot silently keep an old listener alive.
func (m *Manager) Update(ctx context.Context, id model.ForwardingRuleID, draft domainforward.Draft) (domainforward.Rule, error) {
	existing, err := m.getRule(ctx, id)
	if err != nil {
		return domainforward.Rule{}, err
	}
	normalized, err := domainforward.NormalizeDraft(draft)
	if err != nil {
		return domainforward.Rule{}, invalid(err)
	}
	_ = m.Stop(ctx, id)
	updated := domainforward.ApplyDraft(existing, normalized)
	updated.UpdatedAt = m.now()
	if err := m.repo.SaveForwardingRule(ctx, updated); err != nil {
		return domainforward.Rule{}, internal(err)
	}
	m.upsertRule(updated)
	return updated, nil
}

// Delete removes a rule and stops its runtime.
func (m *Manager) Delete(ctx context.Context, id model.ForwardingRuleID) error {
	if m.repo == nil {
		return internal(errors.New("forwarding: no rule repository"))
	}
	_ = m.Stop(ctx, id)
	if err := m.repo.DeleteForwardingRule(ctx, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return notFound()
		}
		return internal(err)
	}
	m.mu.Lock()
	delete(m.runtime, id)
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventRemove, ID: id,
	})
	m.mu.Unlock()
	return nil
}

// List reads the repository and returns every sanitized rule with its runtime
// projection. Malformed stored records are dropped rather than failing the
// whole list.
func (m *Manager) List(ctx context.Context) ([]View, error) {
	if m.repo == nil {
		return nil, internal(errors.New("forwarding: no rule repository"))
	}
	stored, err := m.repo.ListForwardingRules(ctx)
	if err != nil {
		return nil, internal(err)
	}
	rules := make([]domainforward.Rule, 0, len(stored))
	seen := make(map[model.ForwardingRuleID]bool, len(stored))
	for _, candidate := range stored {
		sanitized, ok := domainforward.SanitizeRule(candidate)
		if !ok || seen[sanitized.ID] {
			continue
		}
		seen[sanitized.ID] = true
		rules = append(rules, sanitized)
	}

	m.mu.Lock()
	for id, entry := range m.runtime {
		if !seen[id] {
			// The rule disappeared behind the manager's back. Cancel anything
			// it is doing so a stale runtime cannot outlive the record.
			m.stopEntryLocked(entry)
			delete(m.runtime, id)
		}
	}
	for _, rule := range rules {
		entry, ok := m.runtime[rule.ID]
		if !ok {
			m.runtime[rule.ID] = &runtimeEntry{view: View{
				Rule: rule, Status: domainforward.StatusInactive,
			}}
			continue
		}
		entry.view.Rule = rule
	}
	views := m.viewsLocked()
	m.mu.Unlock()
	return views, nil
}

// Snapshot returns the current runtime view atomically.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{Epoch: m.epoch, Revision: m.revision, Rules: m.viewsLocked()}
}

// Subscribe registers a runtime event callback and returns an unsubscribe
// function. The callback runs on the manager's dispatcher goroutine.
func (m *Manager) Subscribe(fn func(Event)) func() {
	if fn == nil {
		return func() {}
	}
	m.mu.Lock()
	id := m.nextSub
	m.nextSub++
	m.subscribers[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.subscribers, id)
		m.mu.Unlock()
	}
}

// Close stops every rule and the event dispatcher. It is safe to call more
// than once.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	type cleanupPair struct {
		entry    *runtimeEntry
		listener net.Listener
		conn     Connection
	}
	entries := make([]cleanupPair, 0, len(m.runtime))
	for _, entry := range m.runtime {
		m.stopEntryLocked(entry)
		entries = append(entries, cleanupPair{
			entry: entry, listener: entry.listener, conn: entry.conn,
		})
		entry.listener, entry.conn = nil, nil
	}
	m.mu.Unlock()

	for _, pair := range entries {
		m.cleanup(pair.entry, pair.listener, pair.conn)
	}
	close(m.done)
}

// getRule reads and sanitizes one stored rule.
func (m *Manager) getRule(ctx context.Context, id model.ForwardingRuleID) (domainforward.Rule, error) {
	if m.repo == nil {
		return domainforward.Rule{}, internal(errors.New("forwarding: no rule repository"))
	}
	rule, err := m.repo.GetForwardingRule(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return domainforward.Rule{}, notFound()
		}
		return domainforward.Rule{}, internal(err)
	}
	sanitized, ok := domainforward.SanitizeRule(rule)
	if !ok {
		// A row that reached the repository but no longer parses is a storage
		// defect, not user input to correct.
		return domainforward.Rule{}, internal(errors.New("forwarding: stored rule is malformed"))
	}
	return sanitized, nil
}

// upsertRule refreshes the stored definition in the runtime projection.
func (m *Manager) upsertRule(rule domainforward.Rule) {
	m.mu.Lock()
	entry, ok := m.runtime[rule.ID]
	if !ok {
		entry = &runtimeEntry{view: View{Rule: rule, Status: domainforward.StatusInactive}}
		m.runtime[rule.ID] = entry
	} else {
		entry.view.Rule = rule
	}
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventUpsert,
		ID: rule.ID, View: entry.view,
	})
	m.mu.Unlock()
}

// stopEntryLocked cancels a runtime entry. The caller owns the lock.
func (m *Manager) stopEntryLocked(entry *runtimeEntry) {
	entry.generation++
	if entry.reconnectStop != nil {
		entry.reconnectStop()
		entry.reconnectStop = nil
	}
	if entry.stopCh != nil {
		close(entry.stopCh)
		entry.stopCh = nil
	}
	entry.view.Status = domainforward.StatusInactive
	entry.view.Error = ErrorNone
	entry.view.Detail = ""
	entry.view.Hop = nil
	entry.view.Attempts = 0
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventUpsert,
		ID: entry.view.Rule.ID, View: entry.view,
	})
}

// cleanup releases the resources a stopped entry still owns. A remote
// listener is closed off the caller's goroutine and bounded by the unforward
// timeout; a listener that will not release its forward forces the transport
// to be discarded.
func (m *Manager) cleanup(entry *runtimeEntry, listener net.Listener, conn Connection) {
	if listener == nil && conn == nil {
		return
	}
	if listener != nil {
		closed := make(chan error, 1)
		go func() { closed <- listener.Close() }()
		timer := time.NewTimer(m.unforwardTimeout)
		defer timer.Stop()
		select {
		case err := <-closed:
			if err != nil && conn != nil {
				conn.Discard()
				m.setCleanupError(entry, err)
				return
			}
		case <-timer.C:
			if conn != nil {
				conn.Discard()
			}
			m.setCleanupError(entry, errors.New("forwarding: unforward timed out"))
			return
		}
	}
	if conn != nil {
		conn.Release()
	}
}

// setCleanupError records a stop that could not release its forward cleanly.
func (m *Manager) setCleanupError(entry *runtimeEntry, cause error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	entry.view.Status = domainforward.StatusError
	entry.view.Error = ErrorUnforward
	entry.view.Detail = cause.Error()
	entry.view.Hop = nil
	m.revision++
	m.publishLocked(Event{
		Epoch: m.epoch, Revision: m.revision, Type: EventUpsert,
		ID: entry.view.Rule.ID, View: entry.view,
	})
	m.mu.Unlock()
}

func (m *Manager) viewsLocked() []View {
	views := make([]View, 0, len(m.runtime))
	for _, entry := range m.runtime {
		views = append(views, entry.view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Rule.OrderKey != views[j].Rule.OrderKey {
			return views[i].Rule.OrderKey < views[j].Rule.OrderKey
		}
		if views[i].Rule.Label != views[j].Rule.Label {
			return views[i].Rule.Label < views[j].Rule.Label
		}
		return views[i].Rule.ID < views[j].Rule.ID
	})
	return views
}

func (m *Manager) publishLocked(event Event) {
	select {
	case m.events <- event:
	default:
		// Subscribers can always recover the latest state with Snapshot; a
		// full queue must never block a runtime goroutine.
	}
}

func (m *Manager) dispatch() {
	for {
		select {
		case <-m.done:
			return
		case event := <-m.events:
			m.mu.Lock()
			subscribers := make([]func(Event), 0, len(m.subscribers))
			for _, fn := range m.subscribers {
				subscribers = append(subscribers, fn)
			}
			m.mu.Unlock()
			for _, fn := range subscribers {
				fn(event)
			}
		}
	}
}

func nextOrder(rules []domainforward.Rule) int64 {
	var max int64
	for _, rule := range rules {
		if rule.OrderKey > max {
			max = rule.OrderKey
		}
	}
	return max + orderStep
}

var forwardIDEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func newForwardingRuleID() model.ForwardingRuleID {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return model.ForwardingRuleID(forwardIDEncoding.EncodeToString(id[:]))
}

var epochCounter struct {
	mu  sync.Mutex
	cur uint64
}

func nextEpoch() uint64 {
	epochCounter.mu.Lock()
	defer epochCounter.mu.Unlock()
	epochCounter.cur++
	return epochCounter.cur
}
