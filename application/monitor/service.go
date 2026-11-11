package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mslxl/natcatty/application/hoststats"
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

// WithStatsCollector injects the shared Host Stats collector. Without it, a
// Service with a Connector creates a private collector, so a standalone use
// still has the one Host Stats path.
func WithStatsCollector(collector *hoststats.Collector) Option {
	return func(s *Service) {
		if collector != nil {
			s.collector = collector
		}
	}
}

var monitorConsumers atomic.Uint64

// Service samples demanded Hosts off the UI thread and publishes snapshots.
// Start and Stop are called on the UI thread; state changes happen only under
// the service lock. Sampling itself belongs to the shared Host Stats
// collector, so the monitoring surface and the System Tool share one reading.
type Service struct {
	connector Connector
	now       func() time.Time

	collector         *hoststats.Collector
	ownsCollector     bool
	collectorConsumer string
	unsubscribe       func()

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	hosts     map[string]*hostEntry
	panels    map[string]*panelDemand
	listeners map[int]func()
	nextID    int
	closed    bool
	runs      sync.WaitGroup
}

type hostEntry struct {
	state    domainmonitor.HostState
	interval time.Duration
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
	if service.collector == nil && connector != nil {
		service.collector = hoststats.NewCollector(
			monitorStatsConnector{connector: connector},
			hoststats.WithClock(service.now),
		)
		service.ownsCollector = true
	}
	if service.collector != nil {
		service.collectorConsumer = fmt.Sprintf("monitor-%d", monitorConsumers.Add(1))
		service.unsubscribe = service.collector.Watch(service.observe)
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
	if s.connector == nil && s.collector == nil {
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
// than once. A collector injected from outside keeps running for its other
// consumers.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	hostIDs := make([]string, 0, len(s.hosts))
	for id := range s.hosts {
		hostIDs = append(hostIDs, id)
	}
	s.hosts = make(map[string]*hostEntry)
	s.panels = make(map[string]*panelDemand)
	s.listeners = make(map[int]func())
	s.mu.Unlock()

	if s.unsubscribe != nil {
		s.unsubscribe()
		s.unsubscribe = nil
	}
	if s.collector != nil {
		for _, id := range hostIDs {
			s.collector.Stop(s.collectorConsumer, id)
		}
		if s.ownsCollector {
			s.collector.Close()
		}
	}
	s.cancel()
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

// reconcile aligns the collector's demands with the panels. The collector
// owns the sampling loops; this method only adds, moves, and removes consumer
// demands.
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
	var toStop []string
	type startDemand struct {
		host     domainvault.Host
		interval time.Duration
	}
	var toStart []startDemand
	for id, entry := range s.hosts {
		want, ok := demanded[id]
		if !ok {
			toStop = append(toStop, id)
			delete(s.hosts, id)
			continue
		}
		if want.interval != entry.interval {
			entry.interval = want.interval
			toStart = append(toStart, startDemand{host: want.host, interval: want.interval})
		}
	}
	for id, want := range demanded {
		if _, exists := s.hosts[id]; exists {
			continue
		}
		entry := &hostEntry{
			interval: want.interval,
			state: domainmonitor.HostState{
				HostID: id,
				Status: domainmonitor.StatusQueued,
				At:     s.now(),
			},
		}
		if observation, ok := s.collector.Observation(id); ok {
			applyObservation(entry, observation)
		}
		s.hosts[id] = entry
		toStart = append(toStart, startDemand{host: want.host, interval: want.interval})
	}
	s.mu.Unlock()

	for _, id := range toStop {
		s.collector.Stop(s.collectorConsumer, id)
	}
	for _, start := range toStart {
		s.collector.Start(s.collectorConsumer, start.host, start.interval)
	}
}

// observe folds one collector observation into the monitoring surface.
func (s *Service) observe(observation hoststats.Observation) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	entry, ok := s.hosts[observation.HostID]
	if !ok {
		s.mu.Unlock()
		return
	}
	applyObservation(entry, observation)
	s.mu.Unlock()
	s.notify()
}

func applyObservation(entry *hostEntry, observation hoststats.Observation) {
	status, reason := monitorStatus(observation)
	entry.state.Status = status
	entry.state.Reason = reason
	entry.state.Detail = observation.Detail
	entry.state.At = observation.At
	if !observation.HasSample {
		return
	}
	sample := domainSample(observation.Sample)
	if entry.state.Sample.At.IsZero() || !entry.state.Sample.At.Equal(sample.At) {
		entry.state.Sample = sample
		entry.state.Points = domainmonitor.AppendPoint(entry.state.Points, sample)
		return
	}
	entry.state.Sample = sample
}

func monitorStatus(observation hoststats.Observation) (domainmonitor.Status, Reason) {
	reason := monitorReason(observation.Reason)
	switch observation.Status {
	case hoststats.StatusLive:
		return domainmonitor.StatusLive, ReasonNone
	case hoststats.StatusConnecting:
		return domainmonitor.StatusConnecting, ReasonNone
	case hoststats.StatusEmpty:
		return domainmonitor.StatusUnsupported, ReasonUnsupported
	case hoststats.StatusLogin:
		return domainmonitor.StatusLogin, reason
	case hoststats.StatusUnsupported:
		return domainmonitor.StatusUnsupported, reason
	case hoststats.StatusUnavailable:
		return domainmonitor.StatusUnavailable, reason
	case hoststats.StatusError:
		if observation.Failures >= 2 {
			return domainmonitor.StatusUnavailable, reason
		}
		return domainmonitor.StatusError, reason
	default:
		return domainmonitor.StatusQueued, ReasonNone
	}
}

func monitorReason(reason hoststats.Reason) Reason {
	switch reason {
	case hoststats.ReasonHostKey:
		return ReasonHostKey
	case hoststats.ReasonAuth:
		return ReasonAuth
	case hoststats.ReasonNetwork:
		return ReasonNetwork
	case hoststats.ReasonTimeout:
		return ReasonTimeout
	case hoststats.ReasonOS:
		return ReasonOS
	case hoststats.ReasonCancelled:
		return ReasonCancelled
	case hoststats.ReasonUnsupported, hoststats.ReasonEmpty, hoststats.ReasonNoExec,
		hoststats.ReasonPermission, hoststats.ReasonMissingCommand:
		return ReasonUnsupported
	default:
		return ReasonInternal
	}
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
