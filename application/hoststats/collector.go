package hoststats

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// The collector's policy. One cached Stats Sample per Host serves every
// consumer inside CacheTTL; a demand cadence slower than CacheTTL lengthens
// the wait. A Host that fails transversally three times pauses before one
// more try, while a terminal reason stops at once.
const (
	// DefaultCacheTTL is the shortest time between two collections of one Host.
	DefaultCacheTTL = 4 * time.Second
	// DefaultTransientLimit is how many consecutive transient failures pause a
	// Host.
	DefaultTransientLimit = 3
	// DefaultRetryDelay is how long a paused Host waits before one more try.
	DefaultRetryDelay = 30 * time.Second
	// DefaultCollectionTimeout bounds one collection, including its Open.
	DefaultCollectionTimeout = 10 * time.Second
	// DefaultStdoutLimit caps one command's reply.
	DefaultStdoutLimit = 1 << 20
	// DefaultPollInterval is used when a consumer omits an interval.
	DefaultPollInterval = 3 * time.Second
	// CommandBudget is the largest command Dropbear accepts in one request.
	CommandBudget = 9000
)

// Status is a Host's collection state. It carries no display text.
type Status string

const (
	// StatusIdle is a Host no consumer has asked for.
	StatusIdle Status = "idle"
	// StatusConnecting is a Host whose first collection is under way.
	StatusConnecting Status = "connecting"
	// StatusLive is a Host whose last collection produced a usable sample.
	StatusLive Status = "live"
	// StatusEmpty is a Host that answered but produced no usable sample.
	StatusEmpty Status = "empty"
	// StatusError is a recoverable collection failure.
	StatusError Status = "error"
	// StatusLogin is a refused Host key or authentication.
	StatusLogin Status = "login"
	// StatusUnsupported is an operating system or transport the stats command
	// cannot inspect.
	StatusUnsupported Status = "unsupported"
	// StatusUnavailable is a typed Host limitation, such as no exec channel.
	StatusUnavailable Status = "unavailable"
)

// Collector reasons extend the parser reasons with transport and command
// failures. The consumers translate them to their own surface vocabulary.
const (
	ReasonHostKey        Reason = "host_key"
	ReasonAuth           Reason = "auth"
	ReasonNetwork        Reason = "network"
	ReasonTimeout        Reason = "timeout"
	ReasonCancelled      Reason = "cancelled"
	ReasonInternal       Reason = "internal"
	ReasonPermission     Reason = "permission"
	ReasonNoExec         Reason = "no_exec"
	ReasonMissingCommand Reason = "missing_command"
	// ReasonEmpty is an answer with no recognizable Stats Sample. The transport
	// worked, so the Host can retry.
	ReasonEmpty Reason = "empty"
)

// ExecResult is one finished metric command.
type ExecResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	HasExitCode bool
}

// Connection is one Host transport used only for stats commands.
type Connection interface {
	Exec(ctx context.Context, command string) (ExecResult, error)
	Close() error
}

// Connector opens a Host transport. The adapter runs the shared host-key trust
// path before returning.
type Connector interface {
	Open(ctx context.Context, host domainvault.Host) (Connection, error)
}

// singleChannel reports a peer that accepts one session channel per
// connection. The stats collector refuses such a peer: opening a channel to
// ask would use the only one.
type singleChannel interface {
	SingleChannel() bool
}

// Observation is one Host's latest collection state. A failed collection
// keeps the last Sample when HasSample is true, so a surface can show cached
// data with an inline error.
type Observation struct {
	HostID    string
	Status    Status
	Reason    Reason
	Detail    string
	At        time.Time
	Sample    Sample
	HasSample bool
	// Failures counts consecutive transient failures.
	Failures int
	// Paused reports a Host that stopped after too many transient failures.
	Paused bool
	// RetryAt is when a paused Host tries once more.
	RetryAt time.Time
}

// Collector samples Hosts on demand and publishes one cached Stats Sample
// per Host. Consumers call Start and Stop as their surface becomes visible;
// the collector polls only while at least one consumer demands the Host.
type Collector struct {
	connector Connector
	now       func() time.Time
	schedule  func() time.Time
	cacheTTL  time.Duration
	limit     int
	retry     time.Duration
	timeout   time.Duration
	stdout    int
	localOS   string

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	hosts     map[string]*hostWatch
	listeners map[int]func(Observation)
	nextID    int
	closed    bool
	runs      sync.WaitGroup
}

type hostWatch struct {
	host       domainvault.Host
	id         string
	demands    map[string]time.Duration
	generation uint64
	running    bool
	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}

	previous       State
	observation    Observation
	hasObservation bool

	cacheUntil  time.Time
	nextAt      time.Time
	failures    int
	pausedUntil time.Time
	terminal    bool
	force       bool
	inFlight    bool
}

// CollectorOption customizes a Collector. The policy options exist so tests
// can make time observable without fixed sleeps.
type CollectorOption func(*Collector)

// WithClock replaces the collector's source of time for sample and state
// timestamps.
func WithClock(now func() time.Time) CollectorOption {
	return func(c *Collector) {
		if now != nil {
			c.now = now
		}
	}
}

// WithScheduleClock replaces the clock that decides cache expiry, the pause
// after transient failures, and the polling cadence. It is separate from
// WithClock, which timestamps samples, so a test can move scheduling time
// without moving SampledAt. Production uses the wall clock.
func WithScheduleClock(now func() time.Time) CollectorOption {
	return func(c *Collector) {
		if now != nil {
			c.schedule = now
		}
	}
}

// WithCacheTTL sets the minimum time between two collections of one Host.
func WithCacheTTL(ttl time.Duration) CollectorOption {
	return func(c *Collector) {
		if ttl >= 0 {
			c.cacheTTL = ttl
		}
	}
}

// WithTransientLimit sets how many consecutive transient failures pause a
// Host.
func WithTransientLimit(limit int) CollectorOption {
	return func(c *Collector) {
		if limit > 0 {
			c.limit = limit
		}
	}
}

// WithRetryDelay sets the pause after the transient limit is reached.
func WithRetryDelay(delay time.Duration) CollectorOption {
	return func(c *Collector) {
		if delay >= 0 {
			c.retry = delay
		}
	}
}

// WithCollectionTimeout bounds one collection.
func WithCollectionTimeout(timeout time.Duration) CollectorOption {
	return func(c *Collector) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithStdoutLimit caps one command's reply.
func WithStdoutLimit(limit int) CollectorOption {
	return func(c *Collector) {
		if limit > 0 {
			c.stdout = limit
		}
	}
}

// WithLocalOS replaces the operating system this computer runs. A local Host
// on an unsupported system never opens a transport.
func WithLocalOS(goos string) CollectorOption {
	return func(c *Collector) {
		if goos != "" {
			c.localOS = goos
		}
	}
}

// NewCollector returns a collector over connector. A nil connector is allowed
// so a consumer can be wired before its transport, but every collection then
// fails with a typed internal reason.
func NewCollector(connector Connector, opts ...CollectorOption) *Collector {
	ctx, cancel := context.WithCancel(context.Background())
	collector := &Collector{
		connector: connector,
		now:       time.Now,
		schedule:  time.Now,
		cacheTTL:  DefaultCacheTTL,
		limit:     DefaultTransientLimit,
		retry:     DefaultRetryDelay,
		timeout:   DefaultCollectionTimeout,
		stdout:    DefaultStdoutLimit,
		localOS:   runtime.GOOS,
		ctx:       ctx,
		cancel:    cancel,
		hosts:     make(map[string]*hostWatch),
		listeners: make(map[int]func(Observation)),
	}
	for _, opt := range opts {
		opt(collector)
	}
	return collector
}

// Start demands host for consumer at interval. A second consumer of the same
// Host joins the first, and the shortest interval wins. Starting an already
// demanded Host only replaces that consumer's interval; it does not reset the
// retained counters.
func (c *Collector) Start(consumer string, host domainvault.Host, interval time.Duration) {
	if consumer == "" || string(host.ID) == "" {
		return
	}
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	var start bool
	var gen uint64
	var emit Observation
	send := false

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	id := string(host.ID)
	watch := c.hosts[id]
	if watch == nil {
		watch = &hostWatch{
			host:    host,
			id:      id,
			demands: make(map[string]time.Duration),
			wake:    make(chan struct{}, 1),
		}
		c.hosts[id] = watch
	} else {
		watch.host = host
	}
	watch.demands[consumer] = interval
	if !watch.hasObservation {
		watch.observation = Observation{
			HostID: id,
			Status: StatusConnecting,
			At:     c.now(),
		}
		watch.hasObservation = true
		emit = cloneObservation(watch.observation)
		send = true
	}
	gen, start = c.startLocked(watch)
	c.mu.Unlock()

	if send {
		c.emit(emit)
	}
	if start {
		c.runs.Add(1)
		go c.run(id, gen)
	}
}

// Stop ends consumer's demand. The Host's counters and last observation stay
// for the next demand, but polling stops when no consumer remains.
func (c *Collector) Stop(consumer, hostID string) {
	c.mu.Lock()
	watch := c.hosts[hostID]
	if watch == nil {
		c.mu.Unlock()
		return
	}
	delete(watch.demands, consumer)
	if len(watch.demands) == 0 && watch.running && watch.cancel != nil {
		watch.cancel()
	}
	c.mu.Unlock()
}

// Refresh asks for a fresh look at host. Inside the cache window it
// republishes the cached observation without a command; outside it, a paused
// or terminal Host gets one immediate try. Concurrent refreshes coalesce into
// one in-flight collection.
func (c *Collector) Refresh(hostID string) {
	var start bool
	var gen uint64
	var emit Observation
	send := false

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	watch := c.hosts[hostID]
	if watch == nil {
		c.mu.Unlock()
		return
	}
	if watch.inFlight {
		c.mu.Unlock()
		return
	}
	scheduledAt := c.schedule()
	dataAt := c.now()
	watch.force = true
	watch.terminal = false
	watch.pausedUntil = time.Time{}
	if watch.failures >= c.limit {
		watch.failures = 0
	}
	if watch.hasObservation && watch.observation.HasSample && scheduledAt.Before(watch.cacheUntil) {
		watch.force = false
		watch.observation.At = dataAt
		emit = cloneObservation(watch.observation)
		send = true
	} else if !watch.observation.HasSample {
		watch.cacheUntil = time.Time{}
	}
	if !send {
		c.signal(watch)
	}
	if len(watch.demands) > 0 {
		gen, start = c.startLocked(watch)
	}
	c.mu.Unlock()

	if send {
		c.emit(emit)
	}
	if start {
		c.runs.Add(1)
		go c.run(hostID, gen)
	}
}

// Observation returns the latest state for host and whether one exists.
func (c *Collector) Observation(hostID string) (Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	watch := c.hosts[hostID]
	if watch == nil || !watch.hasObservation {
		return Observation{}, false
	}
	return cloneObservation(watch.observation), true
}

// Watch registers a listener for every published observation and returns its
// unsubscribe function. The listener runs outside the collector lock and must
// not block.
func (c *Collector) Watch(fn func(Observation)) func() {
	if fn == nil {
		return func() {}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return func() {}
	}
	id := c.nextID
	c.nextID++
	c.listeners[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.listeners, id)
		c.mu.Unlock()
	}
}

// Close stops every collection and releases every transport. It is safe to
// call more than once.
func (c *Collector) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	watches := make([]*hostWatch, 0, len(c.hosts))
	for _, watch := range c.hosts {
		watches = append(watches, watch)
	}
	c.listeners = make(map[int]func(Observation))
	c.mu.Unlock()

	c.cancel()
	for _, watch := range watches {
		if watch.cancel != nil {
			watch.cancel()
		}
	}
	c.runs.Wait()
}

// run is the single collection loop for one Host generation.
func (c *Collector) run(id string, gen uint64) {
	defer c.runs.Done()

	var connection Connection
	defer func() {
		if connection != nil {
			_ = connection.Close()
		}
		c.finish(id, gen)
	}()

	for {
		c.mu.Lock()
		watch := c.hosts[id]
		if c.closed || watch == nil || watch.generation != gen || len(watch.demands) == 0 {
			c.mu.Unlock()
			return
		}
		hostCtx := watch.ctx
		now := c.schedule()
		interval := watch.intervalLocked()
		force := watch.force

		if watch.terminal && !force {
			c.mu.Unlock()
			if !watch.wait(hostCtx) {
				return
			}
			continue
		}
		if !force && now.Before(watch.pausedUntil) {
			wait := watch.pausedUntil.Sub(now)
			c.mu.Unlock()
			if !watch.waitFor(hostCtx, wait) {
				return
			}
			continue
		}
		if !force && watch.hasObservation && watch.observation.HasSample &&
			now.Before(watch.cacheUntil) {
			wait := watch.cacheUntil.Sub(now)
			c.mu.Unlock()
			if !watch.waitFor(hostCtx, wait) {
				return
			}
			continue
		}
		if !force && now.Before(watch.nextAt) {
			wait := watch.nextAt.Sub(now)
			c.mu.Unlock()
			if !watch.waitFor(hostCtx, wait) {
				return
			}
			continue
		}
		watch.force = false
		watch.inFlight = true
		host := watch.host
		previous := watch.previous
		c.mu.Unlock()

		sample, next, err := c.collect(hostCtx, host, previous, &connection)

		if hostCtx.Err() != nil {
			if connection != nil {
				_ = connection.Close()
				connection = nil
			}
			return
		}

		c.mu.Lock()
		watch = c.hosts[id]
		if c.closed || watch == nil || watch.generation != gen || len(watch.demands) == 0 {
			if watch != nil && watch.generation == gen {
				watch.inFlight = false
			}
			c.mu.Unlock()
			if connection != nil {
				_ = connection.Close()
				connection = nil
			}
			return
		}
		watch.inFlight = false
		stateAt := c.now()
		scheduledAt := c.schedule()
		interval = watch.intervalLocked()
		watch.nextAt = scheduledAt.Add(interval)
		reason := ReasonNone
		status := StatusLive
		terminal := false
		paused := false
		var retryAt time.Time
		if err != nil {
			reason = failureReason(err)
			status, terminal = failureStatus(reason)
			if !terminal {
				// Only a transient failure counts toward the cutoff. An empty
				// reply or a cancellation is not a transport failure, so it
				// must not pause the Host or shorten its retry schedule.
				if isTransientReason(reason) {
					watch.failures++
					if watch.failures >= c.limit {
						paused = true
						retryAt = scheduledAt.Add(c.retry)
						watch.pausedUntil = retryAt
					}
				}
			} else {
				watch.terminal = true
				watch.failures = 0
			}
			if terminal && connection != nil {
				_ = connection.Close()
				connection = nil
			}
		} else {
			watch.failures = 0
			watch.previous = next
			watch.cacheUntil = scheduledAt.Add(c.cacheTTL)
		}

		cached := watch.observation
		if err == nil {
			cached.Sample = sample
			cached.HasSample = true
		}
		if err != nil && terminal {
			cached.Sample = Sample{}
			cached.HasSample = false
		}
		watch.observation = Observation{
			HostID:    id,
			Status:    status,
			Reason:    reason,
			Detail:    errorDetail(err),
			At:        stateAt,
			Sample:    cloneSample(cached.Sample),
			HasSample: cached.HasSample,
			Failures:  watch.failures,
			Paused:    paused,
			RetryAt:   retryAt,
		}
		watch.hasObservation = true
		observation := cloneObservation(watch.observation)
		c.mu.Unlock()

		c.emit(observation)
	}
}

func (c *Collector) collect(ctx context.Context, host domainvault.Host, previous State,
	connection *Connection) (Sample, State, error) {
	if ctx.Err() != nil {
		return Sample{}, State{}, ctx.Err()
	}
	if host.Protocol == domainvault.ProtocolTelnet {
		return Sample{}, State{}, &Error{Reason: ReasonNoExec,
			Cause: errors.New("hoststats: Telnet has no exec channel")}
	}
	if host.Protocol == domainvault.ProtocolLocal && c.localOS != "linux" && c.localOS != "darwin" {
		return Sample{}, State{}, &Error{Reason: ReasonOS,
			Cause: fmt.Errorf("hoststats: no POSIX shell on %s", c.localOS)}
	}

	collectionCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if *connection == nil {
		opened, err := c.open(collectionCtx, host)
		if err != nil {
			return Sample{}, State{}, err
		}
		*connection = opened
	}
	if peer, ok := (*connection).(singleChannel); ok && peer.SingleChannel() {
		_ = (*connection).Close()
		*connection = nil
		return Sample{}, State{}, &Error{Reason: ReasonNoExec,
			Cause: errors.New("hoststats: single-channel Host cannot open an exec channel")}
	}

	started := c.now()
	result, err := (*connection).Exec(collectionCtx, StatsCommand())
	finished := c.now()
	latency := finished.Sub(started)
	if err != nil {
		_ = (*connection).Close()
		*connection = nil
		return Sample{}, State{}, err
	}
	if result.HasExitCode && result.ExitCode != 0 && result.Stdout == "" {
		_ = (*connection).Close()
		*connection = nil
		return Sample{}, State{}, commandError(result)
	}
	output := c.capStdout(result.Stdout)
	if LinuxReply(output) {
		disk, diskErr := (*connection).Exec(collectionCtx, DiskCommand())
		if diskErr == nil {
			output += "|" + c.capStdout(disk.Stdout)
		}
	}

	parsed, err := Parse(output)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) && typed.Reason == ReasonUnsupported {
			return Sample{}, State{}, &Error{Reason: ReasonEmpty, Cause: typed.Cause}
		}
		return Sample{}, State{}, err
	}
	// SampledAt is the stats reply's own completion time. The disk probe that
	// follows on Linux does not move it, and neither does the UI state time.
	sample, next := parsed.Sample(previous, finished, latency)
	return sample, next, nil
}

// open runs the connector but turns a nil transport into a typed failure.
func (c *Collector) open(ctx context.Context, host domainvault.Host) (Connection, error) {
	if c.connector == nil {
		return nil, &Error{Reason: ReasonInternal, Cause: errors.New("hoststats: no connector")}
	}
	connection, err := c.connector.Open(ctx, host)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, &Error{Reason: ReasonInternal, Cause: errors.New("hoststats: connector returned no connection")}
	}
	return connection, nil
}

func (c *Collector) capStdout(text string) string {
	if c.stdout > 0 && len(text) > c.stdout {
		return text[:c.stdout]
	}
	return text
}

// startLocked gives watch a fresh running generation when no live one exists.
// A run whose context a Stop canceled can still be winding down; a demand that
// arrives in that window must not wait for a loop that is about to exit.
func (c *Collector) startLocked(watch *hostWatch) (uint64, bool) {
	if watch.running && watch.ctx != nil && watch.ctx.Err() == nil {
		return 0, false
	}
	watch.generation++
	ctx, cancel := context.WithCancel(c.ctx)
	watch.ctx = ctx
	watch.cancel = cancel
	watch.running = true
	return watch.generation, true
}

func (c *Collector) finish(id string, gen uint64) {
	c.mu.Lock()
	if watch := c.hosts[id]; watch != nil && watch.generation == gen {
		watch.running = false
		watch.cancel = nil
		watch.ctx = nil
	}
	c.mu.Unlock()
}

func (c *Collector) emit(observation Observation) {
	c.mu.Lock()
	listeners := make([]func(Observation), 0, len(c.listeners))
	for _, listener := range c.listeners {
		listeners = append(listeners, listener)
	}
	c.mu.Unlock()
	for _, listener := range listeners {
		listener(observation)
	}
}

func (c *Collector) signal(watch *hostWatch) {
	select {
	case watch.wake <- struct{}{}:
	default:
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

// isTransientReason reports whether reason counts toward the consecutive
// failure cutoff. Ticket 02 and the shared collector spec are explicit: only
// network, timeout, and internal failures are transient. A terminal reason
// stops polling at once, while ReasonEmpty (the transport worked but the reply
// was unusable) and ReasonCancelled leave the Host polling at its cadence and
// must not increment the counter or start the pause.
func isTransientReason(reason Reason) bool {
	switch reason {
	case ReasonNetwork, ReasonTimeout, ReasonInternal:
		return true
	default:
		return false
	}
}

func failureStatus(reason Reason) (Status, bool) {
	switch reason {
	case ReasonHostKey, ReasonAuth:
		return StatusLogin, true
	case ReasonOS:
		return StatusUnsupported, true
	case ReasonNoExec, ReasonPermission, ReasonMissingCommand, ReasonUnsupported:
		return StatusUnavailable, true
	case ReasonEmpty:
		return StatusEmpty, false
	default:
		return StatusError, false
	}
}

func commandError(result ExecResult) error {
	detail := result.Stderr
	if detail == "" {
		detail = fmt.Sprintf("stats command exited with code %d", result.ExitCode)
	}
	lower := detail
	reason := ReasonInternal
	switch {
	case containsFold(lower, "permission denied"),
		containsFold(lower, "operation not permitted"),
		containsFold(lower, "not authorized"):
		reason = ReasonPermission
	case containsFold(lower, "command not found"),
		containsFold(lower, "not found"),
		containsFold(lower, "no such file or directory"):
		reason = ReasonMissingCommand
	}
	return &Error{Reason: reason, Cause: errors.New(detail)}
}

func containsFold(text, sub string) bool {
	return len(sub) == 0 || len(text) >= len(sub) && indexFold(text, sub) >= 0
}

func indexFold(text, sub string) int {
	for i := 0; i+len(sub) <= len(text); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := text[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func errorDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (h *hostWatch) intervalLocked() time.Duration {
	if len(h.demands) == 0 {
		return DefaultPollInterval
	}
	interval := time.Duration(0)
	for _, demand := range h.demands {
		if interval == 0 || demand < interval {
			interval = demand
		}
	}
	if interval <= 0 {
		return DefaultPollInterval
	}
	return interval
}

func (h *hostWatch) wait(ctx context.Context) bool {
	return h.waitFor(ctx, 0)
}

// waitFor waits for a timer, a manual refresh, or cancellation. A zero
// duration waits only for refresh or cancellation.
func (h *hostWatch) waitFor(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-h.wake:
			return true
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-h.wake:
		return true
	case <-timer.C:
		return true
	}
}

func cloneObservation(observation Observation) Observation {
	observation.Sample = cloneSample(observation.Sample)
	return observation
}

func cloneSample(sample Sample) Sample {
	sample.LoadAverage = append([]float64{}, sample.LoadAverage...)
	sample.CPUPerCore = append([]Core{}, sample.CPUPerCore...)
	sample.Disks = append([]domainmonitor.Disk{}, sample.Disks...)
	sample.Interfaces = append([]Interface{}, sample.Interfaces...)
	sample.TopProcesses = append([]Process{}, sample.TopProcesses...)
	return sample
}
