package hoststats

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// manualClock is an injected clock so a test can move from one sample to the
// next without waiting.
type manualClock struct {
	mu sync.Mutex
	at time.Time
}

func newManualClock(at time.Time) *manualClock { return &manualClock{at: at} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

type tickingClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *tickingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(time.Millisecond)
	return c.at
}

type fakeStatsConnector struct {
	mu sync.Mutex

	opens        int
	closes       int
	commands     []string
	statsCalls   int
	statsOutputs []string
	diskOutputs  []string
	openErr      error
	single       bool
	execHook     func(ctx context.Context, command string, call int) (ExecResult, error)
}

func (f *fakeStatsConnector) Open(ctx context.Context, host domainvault.Host) (Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return &fakeStatsConnection{owner: f, single: f.single}, nil
}

func (f *fakeStatsConnector) counts() (opens, closes, commands, stats int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.closes, len(f.commands), f.statsCalls
}

func (f *fakeStatsConnector) commandCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commands)
}

func (f *fakeStatsConnector) statsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsCalls
}

func (f *fakeStatsConnector) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

type fakeStatsConnection struct {
	owner  *fakeStatsConnector
	single bool
	closed bool
}

func (c *fakeStatsConnection) Exec(ctx context.Context, command string) (ExecResult, error) {
	f := c.owner
	f.mu.Lock()
	f.commands = append(f.commands, command)
	index := 0
	if command == StatsCommand() {
		index = f.statsCalls
		f.statsCalls++
	} else if command == DiskCommand() {
		index = f.statsCalls - 1
	}
	hook := f.execHook
	stats := append([]string{}, f.statsOutputs...)
	disks := append([]string{}, f.diskOutputs...)
	f.mu.Unlock()

	if hook != nil {
		return hook(ctx, command, index)
	}
	switch command {
	case StatsCommand():
		if len(stats) == 0 {
			return ExecResult{}, fmt.Errorf("no stats output configured")
		}
		if index >= len(stats) {
			index = len(stats) - 1
		}
		return ExecResult{Stdout: stats[index]}, nil
	case DiskCommand():
		if len(disks) == 0 {
			return ExecResult{Stdout: "DISKS:"}, nil
		}
		if index < 0 {
			index = 0
		}
		if index >= len(disks) {
			index = len(disks) - 1
		}
		return ExecResult{Stdout: disks[index]}, nil
	default:
		return ExecResult{}, fmt.Errorf("unexpected command %q", command)
	}
}

func (c *fakeStatsConnection) Close() error {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.owner.closes++
	}
	return nil
}

func (c *fakeStatsConnection) SingleChannel() bool { return c.single }

func linuxReply(total, idle int64, coreTotal, coreIdle int64, rx, tx uint64,
	uptime, read, write uint64) string {
	return fmt.Sprintf("NC_LATENCY_MARK|MODE:linux|CPURAW:%d %d|CORES:2|"+
		"PERCORERAW:cpu0:%d:%d,cpu1:%d:%d|MEMINFO:8000 1000 100 500 0 0|PROCS:|"+
		"NET:eth0:%d:%d|GPU:|DISKIO:%d;vda:%d:%d,|HOST:alpha|OS:Linux|KERNEL:6.1|"+
		"UPTIME:%d|LOAD:0.1 0.2 0.3",
		total, idle, coreTotal, coreIdle, coreTotal, coreIdle, rx, tx,
		uptime, read, write, uptime)
}

func macOSReply() string {
	return "NC_LATENCY_MARK|MODE:darwin|CPU:42|CORES:8|MEMINFO:16000 8000 0 4000 0 0|" +
		"PROCS:|DISKS:/:10.000000:100.000000:10:/dev/disk1|NET:en0:1000:2000|" +
		"HOST:mac|OS:macOS|KERNEL:23|UPTIME:1000|LOAD:1 2 3"
}

func statsHost(id string, protocol domainvault.Protocol) domainvault.Host {
	return domainvault.Host{ID: model.HostID(id), Label: id, Hostname: id, Protocol: protocol}
}

func awaitObservation(t *testing.T, collector *Collector, hostID string,
	what string, predicate func(Observation) bool) Observation {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	var last Observation
	var found bool
	for {
		observation, ok := collector.Observation(hostID)
		if ok && predicate(observation) {
			return observation
		}
		last, found = observation, ok
		if time.Now().After(deadline) {
			t.Fatalf("the collector never produced %s for %s; last observation %+v (found %v)",
				what, hostID, last, found)
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitCondition(t *testing.T, what string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		if predicate() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func watchRunning(collector *Collector, hostID string) bool {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	watch := collector.hosts[hostID]
	return watch != nil && watch.running
}

func watchTerminal(collector *Collector, hostID string) bool {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	watch := collector.hosts[hostID]
	return watch != nil && watch.terminal
}

// watchPolicyState reads the consecutive-failure counter and the pause
// deadline the cutoff policy keeps for a Host.
func watchPolicyState(collector *Collector, hostID string) (failures int, pausedUntil time.Time) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	watch := collector.hosts[hostID]
	if watch == nil {
		return 0, time.Time{}
	}
	return watch.failures, watch.pausedUntil
}

// wakeForTest interrupts the Host's scheduling wait without changing its
// pause or force state. A test that injected a scheduling clock advances it
// past a deadline and then wakes the loop to re-evaluate that deadline, as the
// wall clock would have done.
func (c *Collector) wakeForTest(hostID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if watch := c.hosts[hostID]; watch != nil {
		c.signal(watch)
	}
}

func closeTo(got, want float64) bool {
	return math.Abs(got-want) < 0.5
}

func TestCollectorPolicyDefaults(t *testing.T) {
	if DefaultCacheTTL != 4*time.Second {
		t.Fatalf("the cache window is %v, want 4s", DefaultCacheTTL)
	}
	if DefaultTransientLimit != 3 {
		t.Fatalf("the transient limit is %d, want 3", DefaultTransientLimit)
	}
	if DefaultRetryDelay != 30*time.Second {
		t.Fatalf("the retry delay is %v, want 30s", DefaultRetryDelay)
	}
	if DefaultCollectionTimeout != 10*time.Second {
		t.Fatalf("the collection timeout is %v, want 10s", DefaultCollectionTimeout)
	}
	if DefaultStdoutLimit != 1<<20 {
		t.Fatalf("the stdout cap is %d, want 1 MiB", DefaultStdoutLimit)
	}
	for name, command := range map[string]string{
		"stats": StatsCommand(),
		"disk":  DiskCommand(),
	} {
		if len(command) > CommandBudget {
			t.Fatalf("the %s command is %d bytes, past the %d-byte budget",
				name, len(command), CommandBudget)
		}
	}
}

// TestCollectorOneCachedSampleServesBothConsumers proves a second consumer
// joining inside the cache window receives the first consumer's sample without
// a second command, and that a refresh inside the window re-renders the cached
// sample for the joining consumer. Both clocks are injected, so wall time can
// never turn the cached answer into a collection.
func TestCollectorOneCachedSampleServesBothConsumers(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	connector := &fakeStatsConnector{statsOutputs: []string{
		linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000),
		linuxReply(200, 80, 100, 40, 3000, 6000, 110, 1200, 2400),
	}}
	collector := NewCollector(connector,
		WithClock(clock.Now),
		WithScheduleClock(clock.Now),
		WithCacheTTL(4*time.Second),
	)
	t.Cleanup(collector.Close)

	host := statsHost("h1", domainvault.ProtocolSSH)
	collector.Start("monitor", host, time.Hour)
	live := awaitObservation(t, collector, "h1", "a live sample",
		func(o Observation) bool { return o.Status == StatusLive && o.HasSample })
	if opens, _, commands, stats := connector.counts(); opens != 1 || commands != 2 || stats != 1 {
		t.Fatalf("the first demand opened %d connections and ran %d commands (%d stats), want 1 and 2 (1)",
			opens, commands, stats)
	}
	firstSampleAt := live.Sample.SampledAt

	// The joining consumer subscribes only now, so every observation it records
	// is one the collector served after the join.
	var joinedMu sync.Mutex
	var joined []Observation
	unwatchSecond := collector.Watch(func(o Observation) {
		if o.Status != StatusLive {
			return
		}
		joinedMu.Lock()
		joined = append(joined, cloneObservation(o))
		joinedMu.Unlock()
	})
	defer unwatchSecond()

	collector.Start("sysmanager", host, time.Hour)
	// Start publishes no observation; the services read the cached state
	// synchronously, so the joining consumer must see the first sample here.
	served, ok := collector.Observation("h1")
	if !ok || !served.HasSample || !served.Sample.SampledAt.Equal(firstSampleAt) {
		t.Fatalf("the joining consumer did not read the cached sample: %+v", served)
	}
	if stats := connector.statsCount(); stats != 1 {
		t.Fatalf("the joining consumer caused %d stats commands, want the one cached sample", stats)
	}

	// Two seconds later, still inside the four-second window, a refresh
	// republishes the cached sample to the joining consumer. Its state time
	// moves while SampledAt does not.
	clock.Advance(2 * time.Second)
	refreshedAt := firstSampleAt.Add(2 * time.Second)
	collector.Refresh("h1")
	awaitCondition(t, "the joining consumer to receive the cached sample", func() bool {
		joinedMu.Lock()
		defer joinedMu.Unlock()
		for _, observation := range joined {
			if observation.Sample.SampledAt.Equal(firstSampleAt) && observation.At.Equal(refreshedAt) {
				return true
			}
		}
		return false
	})
	if opens, _, commands, stats := connector.counts(); opens != 1 || commands != 2 || stats != 1 {
		t.Fatalf("a refresh inside the cache window ran %d commands (%d stats) over %d connections; want the cached sample",
			commands, stats, opens)
	}
}

// TestCollectorCachesUntilTheClockLeavesTheWindow proves a Host returning
// inside the cache window shows the cached sample with no command, and that
// leaving the window collects once more. The scheduling clock is injected, so
// the boundary is decided by the test, not by wall time.
func TestCollectorCachesUntilTheClockLeavesTheWindow(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	connector := &fakeStatsConnector{statsOutputs: []string{
		linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000),
		linuxReply(200, 80, 100, 40, 3000, 6000, 110, 1200, 2400),
	}}
	collector := NewCollector(connector,
		WithClock(clock.Now),
		WithScheduleClock(clock.Now),
		WithCacheTTL(4*time.Second),
	)
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Hour)
	first := awaitObservation(t, collector, "h1", "the first sample",
		func(o Observation) bool { return o.Status == StatusLive })
	collector.Stop("sysmanager", "h1")
	awaitCondition(t, "the loop to stop", func() bool { return !watchRunning(collector, "h1") })

	clock.Advance(2 * time.Second)
	collector.Start("sysmanager", host, time.Hour)
	cached, ok := collector.Observation("h1")
	if !ok || !cached.Sample.SampledAt.Equal(first.Sample.SampledAt) {
		t.Fatalf("the return inside the window lost the cached sample: %+v", cached)
	}
	if stats := connector.statsCount(); stats != 1 {
		t.Fatalf("the return inside the window ran %d stats commands, want 1", stats)
	}

	// Three more seconds leave the four-second window. The refreshed sample is
	// collected once and carries the new data-clock time.
	clock.Advance(3 * time.Second)
	expiredAt := first.Sample.SampledAt.Add(5 * time.Second)
	collector.Refresh("h1")
	awaitCondition(t, "the first collection after the window", func() bool {
		return connector.statsCount() >= 2
	})
	refreshed := awaitObservation(t, collector, "h1", "the post-cache sample",
		func(o Observation) bool {
			return o.Status == StatusLive && o.Sample.SampledAt.Equal(expiredAt)
		})
	if refreshed.Sample.SampledAt.Equal(first.Sample.SampledAt) {
		t.Fatalf("the post-cache sample still carries the cached time %v", refreshed.Sample.SampledAt)
	}
	if stats := connector.statsCount(); stats != 2 {
		t.Fatalf("the post-cache refresh ran %d stats commands, want 2", stats)
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorCoalescesConcurrentDemandsAndRefreshes proves two consumers
// arriving during one collection share it, even when both also refresh.
func TestCollectorCoalescesConcurrentDemandsAndRefreshes(t *testing.T) {
	release := make(chan struct{})
	connector := &fakeStatsConnector{}
	connector.execHook = func(ctx context.Context, command string, call int) (ExecResult, error) {
		if command == DiskCommand() {
			return ExecResult{Stdout: "DISKS:"}, nil
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ExecResult{}, ctx.Err()
		}
		return ExecResult{Stdout: linuxReply(
			100, 40, 50, 20, 1000, 2000, 100, 1000, 2000)}, nil
	}
	collector := NewCollector(connector, WithCacheTTL(time.Minute))
	t.Cleanup(collector.Close)

	var served atomic.Int32
	unwatch := collector.Watch(func(o Observation) {
		if o.Status == StatusLive {
			served.Add(1)
		}
	})
	defer unwatch()

	host := statsHost("h1", domainvault.ProtocolSSH)
	collector.Start("monitor", host, time.Hour)
	awaitCondition(t, "the first collection to start",
		func() bool { return connector.statsCount() == 1 })
	collector.Start("sysmanager", host, time.Hour)
	collector.Refresh("h1")
	collector.Refresh("h1")
	close(release)

	awaitObservation(t, collector, "h1", "the coalesced sample",
		func(o Observation) bool { return o.Status == StatusLive })
	awaitCondition(t, "the shared collection to reach a consumer",
		func() bool { return served.Load() >= 1 })
	if opens, _, commands, stats := connector.counts(); opens != 1 || commands != 2 || stats != 1 {
		t.Fatalf("concurrent demands ran %d commands (%d stats) over %d connections, want 1 stats over 1",
			commands, stats, opens)
	}
}

// TestCollectorDeltasSurviveStopAndStart proves the counters survive the Host
// leaving every surface: total CPU, per-core, disk I/O, and network rates are
// computed from the reading before the gap.
func TestCollectorDeltasSurviveStopAndStart(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	connector := &fakeStatsConnector{
		statsOutputs: []string{
			linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000),
			linuxReply(200, 80, 100, 40, 3000, 6000, 110, 1200, 2400),
			linuxReply(300, 120, 150, 60, 5000, 10000, 120, 1400, 2800),
		},
		diskOutputs: []string{
			"DISKS:/:10.000000:100.000000:10:/dev/vda1",
			"DISKS:/:10.000000:100.000000:10:/dev/vda1",
			"DISKS:/:10.000000:100.000000:10:/dev/vda1",
		},
	}
	collector := NewCollector(connector, WithClock(clock.Now), WithCacheTTL(0))
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Hour)
	first := awaitObservation(t, collector, "h1", "the first sample",
		func(o Observation) bool { return o.Status == StatusLive })
	if !math.IsNaN(first.Sample.CPU) {
		t.Fatalf("the first Linux sample reports CPU %.1f, want unknown", first.Sample.CPU)
	}
	for _, core := range first.Sample.CPUPerCore {
		if !math.IsNaN(core.Percent) {
			t.Fatalf("the first Linux sample reports core %s %.1f, want unknown",
				core.Name, core.Percent)
		}
	}
	if !math.IsNaN(first.Sample.DiskReadBps) || !math.IsNaN(first.Sample.NetworkRxBps) {
		t.Fatalf("the first sample fabricated rates: disk read %.1f, network rx %.1f",
			first.Sample.DiskReadBps, first.Sample.NetworkRxBps)
	}

	clock.Advance(10 * time.Second)
	collector.Refresh("h1")
	second := awaitObservation(t, collector, "h1", "the second sample", func(o Observation) bool {
		return o.Status == StatusLive && o.Sample.SampledAt.After(first.Sample.SampledAt)
	})
	if !closeTo(second.Sample.CPU, 60) {
		t.Fatalf("the second sample CPU is %.2f, want 60", second.Sample.CPU)
	}
	for _, core := range second.Sample.CPUPerCore {
		if !closeTo(core.Percent, 60) {
			t.Fatalf("core %s is %.2f, want 60", core.Name, core.Percent)
		}
	}
	if !closeTo(second.Sample.DiskReadBps, 10240) || !closeTo(second.Sample.DiskWriteBps, 20480) {
		t.Fatalf("disk I/O is %.1f/%.1f B/s, want 10240/20480",
			second.Sample.DiskReadBps, second.Sample.DiskWriteBps)
	}
	if !closeTo(second.Sample.NetworkRxBps, 200) || !closeTo(second.Sample.NetworkTxBps, 400) {
		t.Fatalf("network is %.1f/%.1f B/s, want 200/400",
			second.Sample.NetworkRxBps, second.Sample.NetworkTxBps)
	}

	collector.Stop("sysmanager", "h1")
	awaitCondition(t, "the stopped Host to close its connection", func() bool {
		return connector.closeCount() >= 1 && !watchRunning(collector, "h1")
	})

	clock.Advance(10 * time.Second)
	collector.Start("sysmanager", host, time.Hour)
	collector.Refresh("h1")
	third := awaitObservation(t, collector, "h1", "the third sample", func(o Observation) bool {
		return o.Status == StatusLive && o.Sample.SampledAt.After(second.Sample.SampledAt)
	})
	if !closeTo(third.Sample.CPU, 60) {
		t.Fatalf("the after-return sample CPU is %.2f, want 60 from the retained counters",
			third.Sample.CPU)
	}
	for _, core := range third.Sample.CPUPerCore {
		if !closeTo(core.Percent, 60) {
			t.Fatalf("the after-return core %s is %.2f, want 60", core.Name, core.Percent)
		}
	}
	if !closeTo(third.Sample.DiskReadBps, 10240) || !closeTo(third.Sample.DiskWriteBps, 20480) {
		t.Fatalf("the after-return disk I/O is %.1f/%.1f B/s, want 10240/20480",
			third.Sample.DiskReadBps, third.Sample.DiskWriteBps)
	}
	if !closeTo(third.Sample.NetworkRxBps, 200) || !closeTo(third.Sample.NetworkTxBps, 400) {
		t.Fatalf("the after-return network is %.1f/%.1f B/s, want 200/400",
			third.Sample.NetworkRxBps, third.Sample.NetworkTxBps)
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorPollsOnlyWhileDemanded proves no command happens after the last
// consumer leaves, and a new demand resumes with a fresh sample.
func TestCollectorPollsOnlyWhileDemanded(t *testing.T) {
	connector := &fakeStatsConnector{statsOutputs: []string{
		linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000),
		linuxReply(200, 80, 100, 40, 3000, 6000, 110, 1200, 2400),
	}}
	collector := NewCollector(connector, WithCacheTTL(0), WithCollectionTimeout(time.Second))
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Millisecond)
	awaitObservation(t, collector, "h1", "a live sample",
		func(o Observation) bool { return o.Status == StatusLive })
	collector.Stop("sysmanager", "h1")
	awaitCondition(t, "the polling loop to stop", func() bool {
		return !watchRunning(collector, "h1")
	})
	frozen := connector.commandCount()
	awaitCondition(t, "the connection to close", func() bool {
		return connector.closeCount() >= 1
	})
	if got := connector.commandCount(); got != frozen {
		t.Fatalf("commands ran after the last demand stopped: %d then %d", frozen, got)
	}

	collector.Start("sysmanager", host, time.Hour)
	awaitCondition(t, "the resumed demand to sample", func() bool {
		return connector.statsCount() >= 2
	})
	collector.Stop("sysmanager", "h1")
}

// TestCollectorPausesAfterThreeTransientFailuresThenRetriesOnce proves the
// cutoff, the timed retry, and the manual Refresh retry. The scheduling clock
// is injected, so the pause and the retry happen at the times the test names
// rather than after a real delay.
func TestCollectorPausesAfterThreeTransientFailuresThenRetriesOnce(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	connector := &fakeStatsConnector{openErr: &Error{
		Reason: ReasonNetwork, Cause: errors.New("connection reset"),
	}}
	collector := NewCollector(connector,
		WithClock(clock.Now),
		WithScheduleClock(clock.Now),
		WithCacheTTL(0),
		WithTransientLimit(3),
	)
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	// The poll cadence is longer than any wait the test itself creates, so no
	// automatic poll races the manual refreshes; it is shorter than the retry
	// delay, so an alarm-clock advance past the pause also passes the cadence.
	collector.Start("sysmanager", host, DefaultPollInterval)
	first := awaitObservation(t, collector, "h1", "the first transient failure",
		func(o Observation) bool { return o.Status == StatusError && o.Failures == 1 })
	if !first.At.Equal(clock.Now()) {
		t.Fatalf("the first failure is stamped %v, want the injected time %v", first.At, clock.Now())
	}

	collector.Refresh("h1")
	awaitObservation(t, collector, "h1", "the second transient failure",
		func(o Observation) bool { return o.Status == StatusError && o.Failures == 2 })
	collector.Refresh("h1")
	paused := awaitObservation(t, collector, "h1", "three failures and a pause",
		func(o Observation) bool { return o.Paused && o.Failures >= 3 })
	if paused.Status != StatusError || paused.Reason != ReasonNetwork {
		t.Fatalf("the paused observation is %s/%s, want error/network", paused.Status, paused.Reason)
	}
	opensAtPause, _, _, _ := connector.counts()
	if opensAtPause != 3 {
		t.Fatalf("the pause began after %d attempts, want 3", opensAtPause)
	}
	if !paused.At.Equal(clock.Now()) {
		t.Fatalf("the pause is stamped %v, want the injected time %v", paused.At, clock.Now())
	}
	if !paused.RetryAt.Equal(clock.Now().Add(DefaultRetryDelay)) {
		t.Fatalf("the retry is due %v, want %v", paused.RetryAt, clock.Now().Add(DefaultRetryDelay))
	}
	if !paused.RetryAt.After(paused.At) {
		t.Fatalf("the pause has no future retry: at %v retry %v", paused.At, paused.RetryAt)
	}

	// Move scheduling time past the retry deadline and wake the loop to
	// re-evaluate it, as the wall clock would have done. Exactly one attempt
	// runs, and its failure pauses the Host again.
	clock.Advance(DefaultRetryDelay)
	collector.wakeForTest("h1")
	awaitObservation(t, collector, "h1", "the one timed retry after the pause",
		func(o Observation) bool { return o.Failures == 4 })
	if opens, _, _, _ := connector.counts(); opens != 4 {
		t.Fatalf("the pause let %d attempts through, want exactly one more", opens)
	}

	// A manual Refresh retries at once, below the cutoff, so the Host is not
	// paused again.
	collector.Refresh("h1")
	awaitObservation(t, collector, "h1", "the manual retry",
		func(o Observation) bool { return o.Status == StatusError && o.Failures == 1 })
	if opens, _, _, _ := connector.counts(); opens != 5 {
		t.Fatalf("the manual retry ran %d attempts, want 5", opens)
	}
	if retried, ok := collector.Observation("h1"); !ok || retried.Paused || !retried.RetryAt.IsZero() {
		t.Fatalf("the manual retry left the Host paused: %+v", retried)
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorTerminalReasonStopsUntilRefresh proves a Host key failure stops
// the loop at once and only a manual Refresh tries again.
func TestCollectorTerminalReasonStopsUntilRefresh(t *testing.T) {
	connector := &fakeStatsConnector{openErr: &Error{
		Reason: ReasonHostKey, Cause: errors.New("host key mismatch"),
	}}
	collector := NewCollector(connector, WithCacheTTL(0))
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Millisecond)
	login := awaitObservation(t, collector, "h1", "the refused Host key",
		func(o Observation) bool { return o.Status == StatusLogin })
	if login.Reason != ReasonHostKey || login.HasSample {
		t.Fatalf("the login observation is %+v, want a sample-free host-key reason", login)
	}
	awaitCondition(t, "the terminal loop to park", func() bool {
		return watchTerminal(collector, "h1")
	})
	if opens, _, _, _ := connector.counts(); opens != 1 {
		t.Fatalf("a terminal reason ran %d attempts, want 1", opens)
	}

	collector.Refresh("h1")
	awaitCondition(t, "the manual retry after a terminal reason", func() bool {
		opens, _, _, _ := connector.counts()
		return opens >= 2
	})
	collector.Stop("sysmanager", "h1")
}

// TestCollectorTimeoutBoundsOneCollection proves a transport that never
// answers ends as a typed timeout rather than hanging the surface.
func TestCollectorTimeoutBoundsOneCollection(t *testing.T) {
	connector := &fakeStatsConnector{}
	connector.execHook = func(ctx context.Context, command string, call int) (ExecResult, error) {
		<-ctx.Done()
		return ExecResult{}, ctx.Err()
	}
	collector := NewCollector(connector,
		WithCacheTTL(0), WithCollectionTimeout(30*time.Millisecond))
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Hour)
	observation := awaitObservation(t, collector, "h1", "a timed-out collection",
		func(o Observation) bool {
			return o.Status == StatusError && o.Reason == ReasonTimeout
		})
	if observation.Reason != ReasonTimeout {
		t.Fatalf("the timeout reason is %q, want %q", observation.Reason, ReasonTimeout)
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorCapsStdout proves the reply cap applies before the parser: a
// valid reply after the cap is invisible, while the same reply under the
// default cap parses.
func TestCollectorCapsStdout(t *testing.T) {
	reply := strings.Repeat("x", 64) + linuxReply(
		100, 40, 50, 20, 1000, 2000, 100, 1000, 2000)

	cappedConnector := &fakeStatsConnector{statsOutputs: []string{reply}}
	capped := NewCollector(cappedConnector, WithCacheTTL(0), WithStdoutLimit(32))
	t.Cleanup(capped.Close)
	capped.Start("sysmanager", statsHost("h1", domainvault.ProtocolSSH), time.Hour)
	awaitObservation(t, capped, "h1", "an empty reply after the cap", func(o Observation) bool {
		return o.Status == StatusEmpty && o.Reason == ReasonEmpty
	})
	if opens, _, commands, _ := cappedConnector.counts(); opens != 1 || commands != 1 {
		t.Fatalf("the capped reply ran %d commands over %d connections, want 1/1", commands, opens)
	}
	capped.Stop("sysmanager", "h1")

	openConnector := &fakeStatsConnector{
		statsOutputs: []string{reply},
		diskOutputs:  []string{"DISKS:/:10.000000:100.000000:10:/dev/vda1"},
	}
	open := NewCollector(openConnector, WithCacheTTL(0))
	t.Cleanup(open.Close)
	open.Start("sysmanager", statsHost("h1", domainvault.ProtocolSSH), time.Hour)
	awaitObservation(t, open, "h1", "the uncapped live sample",
		func(o Observation) bool { return o.Status == StatusLive })
	open.Stop("sysmanager", "h1")
}

// TestCollectorCommandShape proves a Linux sample is the stats command plus
// the separate disk probe, while macOS is one command.
func TestCollectorCommandShape(t *testing.T) {
	linuxConnector := &fakeStatsConnector{
		statsOutputs: []string{linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000)},
	}
	linux := NewCollector(linuxConnector, WithCacheTTL(0))
	t.Cleanup(linux.Close)
	linux.Start("sysmanager", statsHost("linux", domainvault.ProtocolSSH), time.Hour)
	awaitObservation(t, linux, "linux", "the Linux sample",
		func(o Observation) bool { return o.Status == StatusLive })
	linuxConnector.mu.Lock()
	commands := append([]string{}, linuxConnector.commands...)
	linuxConnector.mu.Unlock()
	if len(commands) != 2 || commands[0] != StatsCommand() || commands[1] != DiskCommand() {
		t.Fatalf("the Linux commands are %d/%q, want stats then disk", len(commands), commands)
	}
	for index, command := range commands {
		if len(command) > CommandBudget {
			t.Fatalf("Linux command %d is %d bytes, past %d", index, len(command), CommandBudget)
		}
	}
	linux.Stop("sysmanager", "linux")

	macConnector := &fakeStatsConnector{statsOutputs: []string{macOSReply()}}
	mac := NewCollector(macConnector, WithCacheTTL(0))
	t.Cleanup(mac.Close)
	mac.Start("sysmanager", statsHost("mac", domainvault.ProtocolSSH), time.Hour)
	observation := awaitObservation(t, mac, "mac", "the macOS sample",
		func(o Observation) bool { return o.Status == StatusLive })
	if !closeTo(observation.Sample.CPU, 42) || observation.Sample.Cores != 8 {
		t.Fatalf("the macOS sample is CPU %.1f/%d cores, want 42/8",
			observation.Sample.CPU, observation.Sample.Cores)
	}
	if commands := macConnector.commandCount(); commands != 1 {
		t.Fatalf("macOS ran %d commands, want one", commands)
	}
	mac.Stop("sysmanager", "mac")
}

// TestCollectorLeaseMatrix proves the collector refuses a Host before opening
// a transport when the transport cannot serve a stats command, and stops
// immediately when the OS cannot be inspected.
func TestCollectorLeaseMatrix(t *testing.T) {
	t.Run("telnet", func(t *testing.T) {
		connector := &fakeStatsConnector{}
		collector := NewCollector(connector)
		t.Cleanup(collector.Close)
		collector.Start("sysmanager", statsHost("telnet", domainvault.ProtocolTelnet), time.Hour)
		observation := awaitObservation(t, collector, "telnet", "the typed reason",
			func(o Observation) bool { return o.Status == StatusUnavailable })
		if observation.Reason != ReasonNoExec {
			t.Fatalf("Telnet reason is %q, want %q", observation.Reason, ReasonNoExec)
		}
		if opens, _, commands, _ := connector.counts(); opens != 0 || commands != 0 {
			t.Fatalf("Telnet opened %d transports and ran %d commands, want 0/0", opens, commands)
		}
		collector.Stop("sysmanager", "telnet")
	})

	t.Run("local unsupported", func(t *testing.T) {
		connector := &fakeStatsConnector{}
		collector := NewCollector(connector, WithLocalOS("windows"))
		t.Cleanup(collector.Close)
		collector.Start("sysmanager", statsHost("local", domainvault.ProtocolLocal), time.Hour)
		observation := awaitObservation(t, collector, "local", "the unsupported OS",
			func(o Observation) bool { return o.Status == StatusUnsupported })
		if observation.Reason != ReasonOS {
			t.Fatalf("the local reason is %q, want %q", observation.Reason, ReasonOS)
		}
		if opens, _, _, _ := connector.counts(); opens != 0 {
			t.Fatalf("an unsupported local OS opened %d transports, want 0", opens)
		}
		collector.Stop("sysmanager", "local")
	})

	t.Run("single channel", func(t *testing.T) {
		connector := &fakeStatsConnector{single: true}
		collector := NewCollector(connector)
		t.Cleanup(collector.Close)
		collector.Start("sysmanager", statsHost("mosh", domainvault.ProtocolMosh), time.Hour)
		observation := awaitObservation(t, collector, "mosh", "the no-exec reason",
			func(o Observation) bool { return o.Status == StatusUnavailable })
		if observation.Reason != ReasonNoExec {
			t.Fatalf("the single-channel reason is %q, want %q", observation.Reason, ReasonNoExec)
		}
		opens, closes, commands, _ := connector.counts()
		if opens != 1 || closes != 1 || commands != 0 {
			t.Fatalf("the single-channel Host opened %d, closed %d, ran %d commands; want 1/1/0",
				opens, closes, commands)
		}
		collector.Stop("sysmanager", "mosh")
	})

	t.Run("mosh and eternal terminal", func(t *testing.T) {
		// Both protocols are reached through the Connector's existing
		// independent SSH lease; the collector must not open a second
		// channel of its own. A multi-channel fake proves the sample runs
		// on the one lease the Connector returned.
		for _, protocol := range []domainvault.Protocol{domainvault.ProtocolMosh, domainvault.ProtocolET} {
			t.Run(string(protocol), func(t *testing.T) {
				connector := &fakeStatsConnector{statsOutputs: []string{macOSReply()}}
				collector := NewCollector(connector, WithCacheTTL(0))
				t.Cleanup(collector.Close)
				id := string(protocol)
				collector.Start("sysmanager", statsHost(id, protocol), time.Hour)
				observation := awaitObservation(t, collector, id, "the SSH lease's sample",
					func(o Observation) bool { return o.Status == StatusLive })
				if !closeTo(observation.Sample.CPU, 42) || observation.Sample.Cores != 8 {
					t.Fatalf("%s sample is CPU %.1f/%d cores, want the reply's 42/8",
						protocol, observation.Sample.CPU, observation.Sample.Cores)
				}
				opens, _, commands, _ := connector.counts()
				if opens != 1 || commands != 1 {
					t.Fatalf("%s opened %d leases and ran %d commands; want one lease and one stats command",
						protocol, opens, commands)
				}
				collector.Stop("sysmanager", id)
			})
		}
	})

	t.Run("local posix", func(t *testing.T) {
		connector := &fakeStatsConnector{
			statsOutputs: []string{linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000)},
			diskOutputs:  []string{"DISKS:/:10.000000:100.000000:10:/dev/vda1"},
		}
		collector := NewCollector(connector, WithLocalOS("linux"), WithCacheTTL(0))
		t.Cleanup(collector.Close)
		collector.Start("sysmanager", statsHost("local", domainvault.ProtocolLocal), time.Hour)
		observation := awaitObservation(t, collector, "local", "the local POSIX sample",
			func(o Observation) bool { return o.Status == StatusLive })
		if observation.Sample.Cores != 2 {
			t.Fatalf("local cores = %d, want the Linux reply's 2", observation.Sample.Cores)
		}
		if !math.IsNaN(observation.Sample.CPU) {
			t.Fatalf("the first local sample reports CPU %.1f, want unknown", observation.Sample.CPU)
		}
		if opens, _, commands, _ := connector.counts(); opens != 1 || commands != 2 {
			t.Fatalf("the local POSIX Host opened %d transports and ran %d commands; want one lease and stats+disk",
				opens, commands)
		}
		collector.Stop("sysmanager", "local")
	})

	t.Run("unsupported os reply", func(t *testing.T) {
		connector := &fakeStatsConnector{statsOutputs: []string{"UNSUPPORTED_OS:Plan9"}}
		collector := NewCollector(connector, WithCacheTTL(0))
		t.Cleanup(collector.Close)
		collector.Start("sysmanager", statsHost("plan9", domainvault.ProtocolSSH), time.Hour)
		observation := awaitObservation(t, collector, "plan9", "the unsupported OS reply",
			func(o Observation) bool { return o.Status == StatusUnsupported })
		if observation.Reason != ReasonOS {
			t.Fatalf("the reply reason is %q, want %q", observation.Reason, ReasonOS)
		}
		awaitCondition(t, "the unsupported OS to stop", func() bool {
			return watchTerminal(collector, "plan9")
		})
		collector.Stop("sysmanager", "plan9")
	})

	t.Run("unrecognized reply", func(t *testing.T) {
		connector := &fakeStatsConnector{statsOutputs: []string{"not a stats reply"}}
		collector := NewCollector(connector, WithCacheTTL(0))
		t.Cleanup(collector.Close)
		collector.Start("sysmanager", statsHost("empty", domainvault.ProtocolSSH), time.Hour)
		observation := awaitObservation(t, collector, "empty", "the empty state",
			func(o Observation) bool { return o.Status == StatusEmpty })
		if observation.Reason != ReasonEmpty {
			t.Fatalf("the empty reason is %q, want %q", observation.Reason, ReasonEmpty)
		}
		if watchTerminal(collector, "empty") {
			t.Fatal("an unrecognized reply became terminal")
		}
		collector.Stop("sysmanager", "empty")
	})
}

// TestCollectorSampledAtIsTheReplyTime proves the sample time is when the
// stats reply completed, not when the following disk probe or state update
// happened.
func TestCollectorSampledAtIsTheReplyTime(t *testing.T) {
	clock := &tickingClock{at: time.Unix(1_700_000_000, 0)}
	connector := &fakeStatsConnector{
		statsOutputs: []string{linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000)},
	}
	collector := NewCollector(connector, WithClock(clock.Now), WithCacheTTL(0))
	t.Cleanup(collector.Close)
	collector.Start("sysmanager", statsHost("h1", domainvault.ProtocolSSH), time.Hour)
	observation := awaitObservation(t, collector, "h1", "a live sample",
		func(o Observation) bool { return o.Status == StatusLive })
	if !observation.Sample.SampledAt.Before(observation.At) {
		t.Fatalf("SampledAt %v is not before the state time %v",
			observation.Sample.SampledAt, observation.At)
	}
	if observation.Sample.LatencyMS <= 0 {
		t.Fatalf("the sample latency is %.3f ms, want the measured round trip",
			observation.Sample.LatencyMS)
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorEmptyRepliesNeverCountOrPause proves an unusable reply is not a
// transient failure: the transport worked, so repeated empty answers keep the
// Host polling at its cadence with the empty state, never increment the
// consecutive-failure counter, and never start the pause. The scheduling clock
// is injected, so each cadence tick is the test's, not wall time's.
func TestCollectorEmptyRepliesNeverCountOrPause(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	connector := &fakeStatsConnector{statsOutputs: []string{"not a stats reply"}}
	collector := NewCollector(connector,
		WithClock(clock.Now),
		WithScheduleClock(clock.Now),
		WithCacheTTL(0),
	)
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, DefaultPollInterval)
	// Two attempts past the cutoff: neither the cutoff nor the pause may fire.
	attempts := DefaultTransientLimit + 2
	for attempt := 1; attempt <= attempts; attempt++ {
		expectedAt := clock.Now()
		observation := awaitObservation(t, collector, "h1",
			fmt.Sprintf("empty reply %d", attempt), func(o Observation) bool {
				return o.Status == StatusEmpty && o.Reason == ReasonEmpty &&
					o.At.Equal(expectedAt)
			})
		if observation.Failures != 0 || observation.Paused || !observation.RetryAt.IsZero() {
			t.Fatalf("empty reply %d is failures %d paused %v retry %v, want an uncounted empty state",
				attempt, observation.Failures, observation.Paused, observation.RetryAt)
		}
		if got := connector.statsCount(); got != attempt {
			t.Fatalf("empty reply %d followed %d stats commands, want %d", attempt, got, attempt)
		}
		clock.Advance(DefaultPollInterval)
		collector.wakeForTest("h1")
	}

	// One more cadence tick still samples, proving the Host was never parked.
	expectedAt := clock.Now()
	observation := awaitObservation(t, collector, "h1",
		"the next cadence tick after the empty replies", func(o Observation) bool {
			return o.Status == StatusEmpty && o.Reason == ReasonEmpty && o.At.Equal(expectedAt)
		})
	if observation.Failures != 0 || observation.Paused {
		t.Fatalf("the Host after %d empty replies is failures %d paused %v, want neither",
			attempts+1, observation.Failures, observation.Paused)
	}
	if got := connector.statsCount(); got != attempts+1 {
		t.Fatalf("the Host ran %d stats commands after %d empty replies, want %d",
			got, attempts+1, attempts+1)
	}
	failures, pausedUntil := watchPolicyState(collector, "h1")
	if failures != 0 || !pausedUntil.IsZero() {
		t.Fatalf("empty replies left failures %d and a pause until %v, want 0 and none",
			failures, pausedUntil)
	}
	if !watchRunning(collector, "h1") {
		t.Fatal("the collector stopped polling after empty replies")
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorCancelledReasonDoesNotCountOrPause proves a cancellation is
// neither transient nor terminal: it keeps the typed error state, does not
// increment the counter, and leaves the Host polling. The fake transport
// returns the cancellation itself, so the Host's own context stays alive.
func TestCollectorCancelledReasonDoesNotCountOrPause(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	connector := &fakeStatsConnector{}
	connector.execHook = func(ctx context.Context, command string, call int) (ExecResult, error) {
		return ExecResult{}, context.Canceled
	}
	collector := NewCollector(connector,
		WithClock(clock.Now),
		WithScheduleClock(clock.Now),
		WithCacheTTL(0),
	)
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Hour)
	expectedAt := clock.Now()
	first := awaitObservation(t, collector, "h1", "the first cancelled collection",
		func(o Observation) bool {
			return o.Status == StatusError && o.Reason == ReasonCancelled && o.At.Equal(expectedAt)
		})
	if first.Failures != 0 || first.Paused {
		t.Fatalf("the first cancellation is failures %d paused %v, want neither",
			first.Failures, first.Paused)
	}

	for attempt := 2; attempt <= DefaultTransientLimit+1; attempt++ {
		clock.Advance(time.Second)
		expectedAt = clock.Now()
		collector.Refresh("h1")
		observation := awaitObservation(t, collector, "h1",
			fmt.Sprintf("cancelled collection %d", attempt), func(o Observation) bool {
				return o.Status == StatusError && o.Reason == ReasonCancelled &&
					o.At.Equal(expectedAt)
			})
		if observation.Failures != 0 || observation.Paused || !observation.RetryAt.IsZero() {
			t.Fatalf("cancellation %d is failures %d paused %v retry %v, want an uncounted error state",
				attempt, observation.Failures, observation.Paused, observation.RetryAt)
		}
	}
	if got := connector.statsCount(); got != DefaultTransientLimit+1 {
		t.Fatalf("cancellations ran %d stats commands, want %d", got, DefaultTransientLimit+1)
	}
	failures, pausedUntil := watchPolicyState(collector, "h1")
	if failures != 0 || !pausedUntil.IsZero() {
		t.Fatalf("cancellations left failures %d and a pause until %v, want 0 and none",
			failures, pausedUntil)
	}
	if !watchRunning(collector, "h1") {
		t.Fatal("the collector stopped polling after cancellations")
	}
	collector.Stop("sysmanager", "h1")
}

// TestCollectorSuccessfulSampleResetsFailureCounter proves a good sample clears
// the consecutive-failure count, so a later transient failure starts again
// from one rather than continuing toward the cutoff.
func TestCollectorSuccessfulSampleResetsFailureCounter(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	reply := linuxReply(100, 40, 50, 20, 1000, 2000, 100, 1000, 2000)
	connector := &fakeStatsConnector{}
	connector.execHook = func(ctx context.Context, command string, call int) (ExecResult, error) {
		if command == DiskCommand() {
			return ExecResult{Stdout: "DISKS:"}, nil
		}
		if call < 2 || call >= 3 {
			return ExecResult{}, &Error{Reason: ReasonNetwork, Cause: errors.New("connection reset")}
		}
		return ExecResult{Stdout: reply}, nil
	}
	collector := NewCollector(connector,
		WithClock(clock.Now),
		WithScheduleClock(clock.Now),
		WithCacheTTL(0),
	)
	t.Cleanup(collector.Close)
	host := statsHost("h1", domainvault.ProtocolSSH)

	collector.Start("sysmanager", host, time.Hour)
	awaitObservation(t, collector, "h1", "the first transient failure",
		func(o Observation) bool { return o.Status == StatusError && o.Failures == 1 })

	clock.Advance(time.Second)
	collector.Refresh("h1")
	awaitObservation(t, collector, "h1", "the second transient failure",
		func(o Observation) bool { return o.Status == StatusError && o.Failures == 2 })

	clock.Advance(time.Second)
	collector.Refresh("h1")
	awaitObservation(t, collector, "h1", "the successful sample",
		func(o Observation) bool {
			return o.Status == StatusLive && o.HasSample && o.Failures == 0
		})

	clock.Advance(time.Second)
	collector.Refresh("h1")
	after := awaitObservation(t, collector, "h1", "the failure after the success",
		func(o Observation) bool {
			return o.Status == StatusError && o.Reason == ReasonNetwork && o.Failures == 1
		})
	if after.Paused {
		t.Fatalf("the failure after the success is paused: %+v", after)
	}
	collector.Stop("sysmanager", "h1")
}
