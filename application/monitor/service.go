package monitor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// DefaultInterval is the original's refresh cadence when a caller omits one.
const DefaultInterval = 5 * time.Second

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the source of time. Tests use it for stable samples.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// Service samples demanded Hosts off the UI thread and publishes snapshots.
// Start and Stop are called on the UI thread; sampling goroutines mutate only
// under the service lock.
type Service struct {
	connector Connector
	now       func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	hosts     map[string]*hostEntry
	panels    map[string]*panelDemand
	listeners map[int]func()
	nextID    int
	nextGen   uint64
	closed    bool
	runs      sync.WaitGroup
}

type hostEntry struct {
	state    domainmonitor.HostState
	cancel   context.CancelFunc
	interval time.Duration
	gen      uint64
}

type panelDemand struct {
	hosts     []domainvault.Host
	interval  time.Duration
	startedAt time.Time
}

// NewService returns a monitor service over connector.
func NewService(connector Connector, opts ...Option) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		connector: connector,
		now:       time.Now,
		ctx:       ctx,
		cancel:    cancel,
		hosts:     make(map[string]*hostEntry),
		panels:    make(map[string]*panelDemand),
		listeners: make(map[int]func()),
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Subscribe adds a redraw listener and returns its unsubscribe function. The
// listener runs outside the service lock and must not block.
func (s *Service) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Start demands panelID's Hosts at interval. Starting an already-running
// panel replaces its demand. A projected demand above the 256-Host cap is
// refused without changing anything.
func (s *Service) Start(panelID string, hosts []domainvault.Host, interval time.Duration) error {
	if s.connector == nil {
		return ErrNoConnector
	}
	if panelID == "" {
		return errors.New("monitor: empty panel id")
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	unique := uniqueHosts(hosts)
	if len(unique) == 0 {
		return errors.New("monitor: no Hosts")
	}
	now := s.now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("monitor: service is closed")
	}
	projected := make(map[string]bool)
	for id, panel := range s.panels {
		if id == panelID {
			continue
		}
		for _, host := range panel.hosts {
			projected[string(host.ID)] = true
		}
	}
	for _, host := range unique {
		projected[string(host.ID)] = true
	}
	if len(projected) > domainmonitor.MaxHosts {
		s.mu.Unlock()
		return ErrTooManyHosts
	}
	s.panels[panelID] = &panelDemand{hosts: unique, interval: interval, startedAt: now}
	s.mu.Unlock()

	s.reconcile()
	s.notify()
	return nil
}

// Stop ends panelID's demand. Hosts still demanded by another panel keep
// sampling.
func (s *Service) Stop(panelID string) {
	s.mu.Lock()
	delete(s.panels, panelID)
	s.mu.Unlock()
	s.reconcile()
	s.notify()
}

// StopAll ends every demand.
func (s *Service) StopAll() {
	s.mu.Lock()
	s.panels = make(map[string]*panelDemand)
	s.mu.Unlock()
	s.reconcile()
	s.notify()
}

// Close stops sampling and releases every transport. It is safe to call more
// than once.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	entries := make([]*hostEntry, 0, len(s.hosts))
	for _, entry := range s.hosts {
		entries = append(entries, entry)
	}
	s.hosts = make(map[string]*hostEntry)
	s.panels = make(map[string]*panelDemand)
	s.listeners = make(map[int]func())
	s.mu.Unlock()

	s.cancel()
	for _, entry := range entries {
		entry.cancel()
	}
}

// Snapshot returns an immutable copy of the current state.
func (s *Service) Snapshot() domainmonitor.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := domainmonitor.Snapshot{
		TakenAt:   s.now(),
		Hosts:     make(map[string]domainmonitor.HostState, len(s.hosts)),
		Running:   make(map[string]bool, len(s.panels)),
		StartedAt: make(map[string]time.Time, len(s.panels)),
	}
	for id, entry := range s.hosts {
		state := entry.state
		state.Points = domainmonitor.ClonePoints(entry.state.Points)
		snapshot.Hosts[id] = state
	}
	for id, panel := range s.panels {
		snapshot.Running[id] = true
		snapshot.StartedAt[id] = panel.startedAt
	}
	return snapshot
}

// reconcile starts, stops, and re-cadences host samplers to match demand.
func (s *Service) reconcile() {
	type demand struct {
		host     domainvault.Host
		interval time.Duration
	}
	demanded := make(map[string]demand)
	s.mu.Lock()
	for _, panel := range s.panels {
		for _, host := range panel.hosts {
			id := string(host.ID)
			current, ok := demanded[id]
			if !ok || panel.interval < current.interval {
				demanded[id] = demand{host: host, interval: panel.interval}
			}
		}
	}
	var toStop []*hostEntry
	for id, entry := range s.hosts {
		want, ok := demanded[id]
		if !ok || want.interval != entry.interval {
			toStop = append(toStop, entry)
			delete(s.hosts, id)
		}
	}
	var toStart []struct {
		ctx      context.Context
		host     domainvault.Host
		interval time.Duration
		gen      uint64
	}
	for id, want := range demanded {
		if _, ok := s.hosts[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.nextGen++
		entry := &hostEntry{
			state: domainmonitor.HostState{
				HostID: id,
				Status: domainmonitor.StatusQueued,
				At:     s.now(),
			},
			cancel:   cancel,
			interval: want.interval,
			gen:      s.nextGen,
		}
		s.hosts[id] = entry
		toStart = append(toStart, struct {
			ctx      context.Context
			host     domainvault.Host
			interval time.Duration
			gen      uint64
		}{ctx: ctx, host: want.host, interval: want.interval, gen: entry.gen})
	}
	s.mu.Unlock()

	for _, entry := range toStop {
		entry.cancel()
	}
	for _, start := range toStart {
		s.runs.Add(1)
		go s.runHost(start.ctx, start.host, start.interval, start.gen)
	}
}

func (s *Service) runHost(ctx context.Context, host domainvault.Host, interval time.Duration, gen uint64) {
	defer s.runs.Done()

	var (
		connection Connection
		previous   samplerState
		failures   int
	)
	defer func() { closeConnection(&connection) }()

	for {
		if ctx.Err() != nil {
			return
		}
		err := s.sampleOnce(ctx, host, gen, &connection, &previous)
		if ctx.Err() != nil {
			return
		}
		delay := interval
		if err != nil {
			reason := failureReason(err)
			if reason == ReasonCancelled {
				return
			}
			failures++
			status := domainmonitor.StatusError
			switch reason {
			case ReasonHostKey, ReasonAuth:
				status = domainmonitor.StatusLogin
			case ReasonOS, ReasonUnsupported:
				status = domainmonitor.StatusUnsupported
			default:
				if failures >= 2 {
					status = domainmonitor.StatusUnavailable
				}
			}
			s.update(string(host.ID), gen, func(state *domainmonitor.HostState) {
				state.Status = status
				state.Reason = reason
				state.Detail = err.Error()
				state.At = s.now()
			})
			if status == domainmonitor.StatusLogin ||
				status == domainmonitor.StatusUnsupported {
				// A rejected trust decision or an unsupported OS needs the
				// user; retrying would only re-prompt or spin.
				return
			}
			multiplier := 1 << min(failures, 3)
			if multiplier > 6 {
				multiplier = 6
			}
			delay = interval * time.Duration(multiplier)
		} else {
			failures = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// sampleOnce opens the transport when needed and publishes one measurement.
func (s *Service) sampleOnce(ctx context.Context, host domainvault.Host, gen uint64,
	connection *Connection, previous *samplerState) error {
	if *connection == nil {
		s.update(string(host.ID), gen, func(state *domainmonitor.HostState) {
			state.Status = domainmonitor.StatusConnecting
			state.Reason = ReasonNone
			state.Detail = ""
			state.At = s.now()
		})
		opened, err := s.connector.Open(ctx, host)
		if err != nil {
			return err
		}
		*connection = opened
	}
	started := s.now()
	result, err := (*connection).Exec(ctx, statsCommand())
	latency := s.now().Sub(started)
	if err != nil {
		closeConnection(connection)
		return err
	}
	output := result.Stdout
	if containsMetric(output, "CPURAW:") {
		if disk, diskErr := (*connection).Exec(ctx, diskCommand()); diskErr == nil {
			output += "|" + disk.Stdout
		}
	}
	parsed, err := parseStats(output)
	if err != nil {
		return err
	}
	at := started.Add(latency)
	sample, next := parsed.sample(*previous, at, latency)
	*previous = next
	s.update(string(host.ID), gen, func(state *domainmonitor.HostState) {
		state.Status = domainmonitor.StatusLive
		state.Reason = ReasonNone
		state.Detail = ""
		state.At = at
		state.Sample = sample
		state.Points = domainmonitor.AppendPoint(state.Points, sample)
	})
	return nil
}

func (s *Service) update(hostID string, gen uint64, change func(*domainmonitor.HostState)) {
	s.mu.Lock()
	entry, ok := s.hosts[hostID]
	if !ok || entry.gen != gen {
		s.mu.Unlock()
		return
	}
	change(&entry.state)
	s.mu.Unlock()
	s.notify()
}

func (s *Service) notify() {
	s.mu.Lock()
	listeners := make([]func(), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func closeConnection(connection *Connection) {
	if connection != nil && *connection != nil {
		_ = (*connection).Close()
		*connection = nil
	}
}

func failureReason(err error) Reason {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Reason
	}
	if errors.Is(err, context.Canceled) {
		return ReasonCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	return ReasonInternal
}

func containsMetric(output, label string) bool {
	return strings.Contains(output, label)
}

func uniqueHosts(hosts []domainvault.Host) []domainvault.Host {
	seen := make(map[string]bool, len(hosts))
	out := make([]domainvault.Host, 0, len(hosts))
	for _, host := range hosts {
		id := string(host.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, host)
	}
	return out
}
