package sysmanager

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/application/hoststats"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// sharedStatsTransport is a fake Host Stats transport; the System Tool reads
// it through the shared collector, which is the only sampler in these tests.
type sharedStatsTransport struct {
	mu sync.Mutex

	statsOutputs []string
	commands     []string
	opens        int
	statsCalls   int
}

func (t *sharedStatsTransport) Open(ctx context.Context, _ domainvault.Host) (hoststats.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.opens++
	t.mu.Unlock()
	return &sharedStatsConnection{transport: t}, nil
}

func (t *sharedStatsTransport) counts() (opens, stats int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opens, t.statsCalls
}

type sharedStatsConnection struct {
	transport *sharedStatsTransport
}

func (c *sharedStatsConnection) Exec(ctx context.Context, command string) (hoststats.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return hoststats.ExecResult{}, err
	}
	t := c.transport
	t.mu.Lock()
	t.commands = append(t.commands, command)
	var output string
	switch command {
	case hoststats.StatsCommand():
		index := t.statsCalls
		t.statsCalls++
		if len(t.statsOutputs) > 0 {
			if index >= len(t.statsOutputs) {
				index = len(t.statsOutputs) - 1
			}
			output = t.statsOutputs[index]
		}
	default:
		t.mu.Unlock()
		return hoststats.ExecResult{}, fmt.Errorf("unexpected command %q", command)
	}
	t.mu.Unlock()
	return hoststats.ExecResult{Stdout: output}, nil
}

func (c *sharedStatsConnection) Close() error { return nil }

func (c *sharedStatsConnection) SingleChannel() bool { return false }

// sharedClock is the collector's data and scheduling clock, so a test moves
// both the sample time and the cache window without waiting.
type sharedClock struct {
	mu sync.Mutex
	at time.Time
}

func newSharedClock(at time.Time) *sharedClock { return &sharedClock{at: at} }

func (c *sharedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *sharedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

const sharedDarwinReply = "NC_LATENCY_MARK|MODE:darwin|CPU:37|CORES:8|" +
	"MEMINFO:16384 12288 0 0 1024 0|PROCS:|" +
	"DISKS:/:10.000000:40.000000:25:/dev/disk1|NET:en0:1000:2000|" +
	"HOST:shared|OS:macOS 14|KERNEL:23|UPTIME:100|LOAD:1 2 3"

// TestRefreshInsideCacheWindowRepublishesWithoutACommand proves a manual
// refresh inside the four-second window re-renders the cached sample instead
// of sampling again. The state time moves; SampledAt does not.
func TestRefreshInsideCacheWindowRepublishesWithoutACommand(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	clock := newSharedClock(start)
	transport := &sharedStatsTransport{statsOutputs: []string{sharedDarwinReply}}
	collector := hoststats.NewCollector(transport,
		hoststats.WithClock(clock.Now),
		hoststats.WithScheduleClock(clock.Now),
		hoststats.WithCacheTTL(4*time.Second),
	)
	t.Cleanup(collector.Close)

	system := NewService(nil, WithStatsCollector(collector))
	t.Cleanup(system.Close)
	host := fakeHost("shared")
	if err := system.Start(host, TabOverview, time.Hour); err != nil {
		t.Fatalf("start system overview: %v", err)
	}
	waitFor(t, 5*time.Second, "the first sample", func() bool {
		state, ok := system.Snapshot().Tabs[TabOverview]
		return ok && state.Status == StatusLive && state.Stats.SampledAt.Equal(start)
	})

	clock.Advance(time.Second)
	retriedAt := start.Add(time.Second)
	system.Refresh()
	waitFor(t, 5*time.Second, "the refreshed cached state", func() bool {
		state, ok := system.Snapshot().Tabs[TabOverview]
		return ok && state.Status == StatusLive &&
			state.At.Equal(retriedAt) && state.Stats.SampledAt.Equal(start)
	})
	state := system.Snapshot().Tabs[TabOverview]
	if state.Stats.CPU != 37 {
		t.Errorf("cached CPU = %v, want 37", state.Stats.CPU)
	}

	if _, statsCalls := transport.counts(); statsCalls != 1 {
		t.Errorf("refresh inside the cache window ran %d stats commands; want none new", statsCalls)
	}
}
