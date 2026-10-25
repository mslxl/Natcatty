package monitor

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const linuxStatsOutput = "NC_LATENCY_MARK|CPURAW:1000 400|CORES:2|" +
	"PERCORERAW:500:200,500:200|MEMINFO:8000 2000 500 1000 1024 512|" +
	"NET:eth0:1000:2000,eth1:500:700|" +
	"GPU:42 1024 8192 NVIDIA GeForce RTX 4090|" +
	"HOST:web-1|OS:Ubuntu 24.04|KERNEL:6.8.0|UPTIME:12345|" +
	"LOAD:0.50 0.40 0.30|" +
	"DISKS:/:10.000000:40.000000:25:fs=ext4:/dev/sda1"

const linuxDiskOutput = "DISKS:/:10.000000:40.000000:25:fs=ext4:/dev/sda1"

func TestParseStatsReadsEveryMetricLabel(t *testing.T) {
	parsed, err := parseStats(linuxStatsOutput)
	if err != nil {
		t.Fatalf("parseStats: %v", err)
	}
	sample, _ := parsed.sample(samplerState{}, time.Unix(100, 0), 20*time.Millisecond)
	if !math.IsNaN(sample.CPU) {
		t.Errorf("first CPU sample = %v, want NaN until a delta exists", sample.CPU)
	}
	if sample.CPUCores != 2 {
		t.Errorf("cores = %d", sample.CPUCores)
	}
	if sample.Memory != 56.25 {
		t.Errorf("memory = %v, want 56.25", sample.Memory)
	}
	if sample.MemUsedMB != 4500 || sample.MemTotalMB != 8000 {
		t.Errorf("memory figures = %v/%v", sample.MemUsedMB, sample.MemTotalMB)
	}
	if sample.SwapUsedMB != 512 || sample.SwapTotalMB != 1024 {
		t.Errorf("swap = %v/%v", sample.SwapUsedMB, sample.SwapTotalMB)
	}
	if sample.Disk != 25 || sample.DiskUsedGB != 10 || sample.DiskTotalGB != 40 {
		t.Errorf("disk = %v%% %v/%v GB", sample.Disk, sample.DiskUsedGB, sample.DiskTotalGB)
	}
	if len(sample.Disks) != 1 || sample.Disks[0].MountPoint != "/" ||
		sample.Disks[0].FilesystemType != "ext4" || sample.Disks[0].CapacityKey != "/dev/sda1" {
		t.Errorf("disks = %+v", sample.Disks)
	}
	if sample.GPU != 42 || sample.GPUMemUsedMB != 1024 || sample.GPUMemTotalMB != 8192 ||
		sample.GPUName != "NVIDIA GeForce RTX 4090" {
		t.Errorf("gpu = %+v", sample)
	}
	if sample.Hostname != "web-1" || sample.OSName != "Ubuntu 24.04" ||
		sample.KernelRelease != "6.8.0" || sample.UptimeSeconds != 12345 {
		t.Errorf("identity = %+v", sample)
	}
	if len(sample.LoadAverage) != 3 || sample.LoadAverage[0] != 0.5 {
		t.Errorf("load = %v", sample.LoadAverage)
	}
	if sample.Latency != 20 {
		t.Errorf("latency = %v ms", sample.Latency)
	}
	if !math.IsNaN(sample.Rx) || !math.IsNaN(sample.Tx) {
		t.Errorf("first network sample = %v/%v, want NaN", sample.Rx, sample.Tx)
	}
}

func TestParseStatsSecondSampleUsesCounters(t *testing.T) {
	first, err := parseStats(linuxStatsOutput)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(100, 0)
	_, previous := first.sample(samplerState{}, start, 10*time.Millisecond)

	second, err := parseStats("NC_LATENCY_MARK|CPURAW:1200 440|CORES:2|" +
		"PERCORERAW:600:220,600:220|MEMINFO:8000 2000 500 1000 0 0|" +
		"NET:eth0:2000:2500,eth1:1000:1000|HOST:web-1")
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := second.sample(previous, start.Add(2*time.Second), 10*time.Millisecond)
	if sample.CPU != 80 {
		t.Errorf("cpu = %v, want 80", sample.CPU)
	}
	if len(sample.CPUPerCore) != 2 || sample.CPUPerCore[0] != 80 || sample.CPUPerCore[1] != 80 {
		t.Errorf("per-core = %v", sample.CPUPerCore)
	}
	if sample.Rx != 750 || sample.Tx != 400 {
		t.Errorf("rates = %v/%v B/s, want 750/400", sample.Rx, sample.Tx)
	}
}

func TestParseStatsMacOSDirectCPU(t *testing.T) {
	parsed, err := parseStats("NC_LATENCY_MARK|CPU:37|CORES:8|" +
		"MEMINFO:16384 4096 0 6144 2048 1024|DISKS:/:20.000000:100.000000:20:apfs:disk3s1|" +
		"NET:en0:10:20|HOST:mac|OS:macOS 15|KERNEL:24.0|UPTIME:99|LOAD:1 2 3")
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := parsed.sample(samplerState{}, time.Unix(0, 0), 0)
	if sample.CPU != 37 {
		t.Errorf("cpu = %v", sample.CPU)
	}
	if sample.Memory != 37.5 {
		t.Errorf("memory = %v, want 37.5", sample.Memory)
	}
}

func TestParseStatsReportsTypedFailures(t *testing.T) {
	_, err := parseStats("NC_LATENCY_MARK|UNSUPPORTED_OS:FreeBSD")
	var typed *Error
	if !errors.As(err, &typed) || typed.Reason != ReasonOS {
		t.Fatalf("unsupported OS error = %v, want ReasonOS", err)
	}
	_, err = parseStats("NC_LATENCY_MARK|HOST:web-1|UPTIME:1")
	if !errors.As(err, &typed) || typed.Reason != ReasonUnsupported {
		t.Fatalf("empty metrics error = %v, want ReasonUnsupported", err)
	}
}

type fakeConnector struct {
	mu    sync.Mutex
	opens int
	hosts []domainvault.Host
	open  func(context.Context, domainvault.Host) (Connection, error)
}

func (f *fakeConnector) Open(ctx context.Context, host domainvault.Host) (Connection, error) {
	f.mu.Lock()
	f.opens++
	f.hosts = append(f.hosts, host)
	f.mu.Unlock()
	if f.open == nil {
		return nil, &Error{Reason: ReasonNetwork, Cause: errors.New("unreachable")}
	}
	return f.open(ctx, host)
}

func (f *fakeConnector) OpenCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

type fakeConnection struct {
	mu       sync.Mutex
	commands []string
	exec     func(context.Context, string) (ExecResult, error)
	closeOne sync.Once
	closed   chan struct{}
}

func newFakeConnection(exec func(context.Context, string) (ExecResult, error)) *fakeConnection {
	return &fakeConnection{exec: exec, closed: make(chan struct{})}
}

func (m *fakeConnection) Exec(ctx context.Context, command string) (ExecResult, error) {
	m.mu.Lock()
	m.commands = append(m.commands, command)
	m.mu.Unlock()
	return m.exec(ctx, command)
}

func (m *fakeConnection) Close() error {
	m.closeOne.Do(func() { close(m.closed) })
	return nil
}

func (m *fakeConnection) CommandCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.commands)
}

// await blocks until condition is true, waking only on service updates. It
// fails at the deadline; it never assumes a fixed delay.
func await(t *testing.T, updates <-chan struct{}, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("condition not met before the deadline")
		}
		select {
		case <-updates:
		case <-time.After(remaining):
			if !condition() {
				t.Fatal("condition not met before the deadline")
			}
		}
	}
}

func waitRuns(t *testing.T, service *Service) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		service.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("samplers did not stop before the deadline")
	}
}

func updatesFor(service *Service) chan struct{} {
	updates := make(chan struct{}, 1)
	service.Subscribe(func() {
		select {
		case updates <- struct{}{}:
		default:
		}
	})
	return updates
}

func host(id string) domainvault.Host {
	return domainvault.Host{ID: model.HostID(id), Label: id, Hostname: id + ".example", Port: 22, Username: "root"}
}

func TestServiceSamplesWithoutBlockingAndPublishesHistory(t *testing.T) {
	connection := newFakeConnection(func(_ context.Context, command string) (ExecResult, error) {
		if strings.HasPrefix(command, "disks=$(") {
			return ExecResult{Stdout: linuxDiskOutput}, nil
		}
		return ExecResult{Stdout: linuxStatsOutput}, nil
	})
	connector := &fakeConnector{open: func(context.Context, domainvault.Host) (Connection, error) {
		return connection, nil
	}}
	service := NewService(connector)
	defer service.Close()
	updates := updatesFor(service)

	if err := service.Start("panel-a", []domainvault.Host{host("h1")}, time.Hour); err != nil {
		t.Fatalf("Start: %v", err)
	}
	await(t, updates, func() bool {
		state := service.Snapshot().Hosts["h1"]
		return state.Status == domainmonitor.StatusLive
	})
	snapshot := service.Snapshot()
	state := snapshot.Hosts["h1"]
	if len(state.Points) != 1 {
		t.Errorf("points = %d, want 1", len(state.Points))
	}
	if state.Sample.Memory != 56.25 || state.Sample.Disk != 25 {
		t.Errorf("sample = %+v", state.Sample)
	}
	if !snapshot.Running["panel-a"] || snapshot.StartedAt["panel-a"].IsZero() {
		t.Errorf("running = %v, started = %v", snapshot.Running, snapshot.StartedAt)
	}
	if connector.OpenCount() != 1 {
		t.Errorf("opens = %d, want 1", connector.OpenCount())
	}
	if connection.CommandCount() != 2 {
		t.Errorf("commands = %d, want stats and disk", connection.CommandCount())
	}

	service.Stop("panel-a")
	if service.Snapshot().Running["panel-a"] {
		t.Error("panel still running after Stop")
	}
	select {
	case <-connection.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("connection not closed after Stop")
	}
	waitRuns(t, service)
}

func TestServiceTrustRejectionRunsNoMetricCommand(t *testing.T) {
	connection := newFakeConnection(func(context.Context, string) (ExecResult, error) {
		return ExecResult{}, nil
	})
	connector := &fakeConnector{open: func(context.Context, domainvault.Host) (Connection, error) {
		return nil, &Error{Reason: ReasonHostKey, Cause: errors.New("host key rejected")}
	}}
	service := NewService(connector)
	defer service.Close()
	updates := updatesFor(service)

	if err := service.Start("panel-a", []domainvault.Host{host("h1")}, time.Hour); err != nil {
		t.Fatalf("Start: %v", err)
	}
	await(t, updates, func() bool {
		state := service.Snapshot().Hosts["h1"]
		return state.Status == domainmonitor.StatusLogin
	})
	state := service.Snapshot().Hosts["h1"]
	if state.Reason != domainmonitor.ReasonHostKey {
		t.Errorf("reason = %v, want host_key", state.Reason)
	}
	waitRuns(t, service)
	if connector.OpenCount() != 1 {
		t.Errorf("opens = %d, want exactly 1 (no retry after trust rejection)", connector.OpenCount())
	}
	if connection.CommandCount() != 0 {
		t.Errorf("metric commands ran before trust: %d", connection.CommandCount())
	}
}

func TestServiceDegradesToUnavailableOnRepeatedNetworkFailure(t *testing.T) {
	connector := &fakeConnector{open: func(context.Context, domainvault.Host) (Connection, error) {
		return newFakeConnection(func(context.Context, string) (ExecResult, error) {
			return ExecResult{}, &Error{Reason: ReasonNetwork, Cause: errors.New("connection reset")}
		}), nil
	}}
	service := NewService(connector)
	defer service.Close()
	updates := updatesFor(service)

	if err := service.Start("panel-a", []domainvault.Host{host("h1")}, time.Millisecond); err != nil {
		t.Fatalf("Start: %v", err)
	}
	await(t, updates, func() bool {
		state := service.Snapshot().Hosts["h1"]
		return state.Status == domainmonitor.StatusUnavailable
	})
	state := service.Snapshot().Hosts["h1"]
	if state.Reason != domainmonitor.ReasonNetwork {
		t.Errorf("reason = %v, want network", state.Reason)
	}
	service.Stop("panel-a")
	waitRuns(t, service)
}

func TestServiceCancellationStopsTheSamplerPromptly(t *testing.T) {
	execStarted := make(chan struct{})
	connection := newFakeConnection(func(ctx context.Context, _ string) (ExecResult, error) {
		close(execStarted)
		<-ctx.Done()
		return ExecResult{}, ctx.Err()
	})
	connector := &fakeConnector{open: func(context.Context, domainvault.Host) (Connection, error) {
		return connection, nil
	}}
	service := NewService(connector)
	defer service.Close()

	if err := service.Start("panel-a", []domainvault.Host{host("h1")}, time.Hour); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-execStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("metric command never started")
	}
	service.Stop("panel-a")
	waitRuns(t, service)
	select {
	case <-connection.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("connection not closed after cancellation")
	}
}

func TestServiceRejectsMoreThan256HostsAndDoesNotOpen(t *testing.T) {
	connector := &fakeConnector{}
	service := NewService(connector)
	defer service.Close()
	hosts := make([]domainvault.Host, domainmonitor.MaxHosts+1)
	for index := range hosts {
		hosts[index] = host("h" + strconv.Itoa(index))
	}
	if err := service.Start("panel-a", hosts, time.Hour); !errors.Is(err, ErrTooManyHosts) {
		t.Fatalf("Start(257) = %v, want ErrTooManyHosts", err)
	}
	if connector.OpenCount() != 0 {
		t.Errorf("opens = %d, want 0", connector.OpenCount())
	}
	if len(service.Snapshot().Running) != 0 {
		t.Errorf("running = %v", service.Snapshot().Running)
	}
}

func TestServiceCapIsSharedAcrossPanels(t *testing.T) {
	connector := &fakeConnector{open: func(ctx context.Context, _ domainvault.Host) (Connection, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	service := NewService(connector)
	hosts := make([]domainvault.Host, 200)
	for index := range hosts {
		hosts[index] = host("a" + strconv.Itoa(index))
	}
	if err := service.Start("panel-a", hosts, time.Hour); err != nil {
		t.Fatalf("Start(200): %v", err)
	}
	extra := make([]domainvault.Host, 57)
	for index := range extra {
		extra[index] = host("b" + strconv.Itoa(index))
	}
	if err := service.Start("panel-b", extra, time.Hour); !errors.Is(err, ErrTooManyHosts) {
		t.Fatalf("Start(200+57) = %v, want ErrTooManyHosts", err)
	}
	service.Close()
	waitRuns(t, service)
}

func TestStatsCommandCoversLinuxMacOSAndUnsupported(t *testing.T) {
	command := statsCommand()
	for _, want := range []string{"uname -s", "Darwin", "Linux", "UNSUPPORTED_OS", "/proc/stat", "vm_stat"} {
		if !strings.Contains(command, want) {
			t.Errorf("stats command is missing %q", want)
		}
	}
	if command == diskCommand() {
		t.Error("disk probe must be a separate command")
	}
	if !strings.Contains(diskCommand(), "df -kP") {
		t.Error("disk probe does not read df")
	}
}

func TestServiceNoConnector(t *testing.T) {
	service := NewService(nil)
	if err := service.Start("panel-a", []domainvault.Host{host("h1")}, time.Second); !errors.Is(err, ErrNoConnector) {
		t.Fatalf("Start = %v, want ErrNoConnector", err)
	}
}

func TestServiceCloseIsIdempotent(t *testing.T) {
	service := NewService(&fakeConnector{})
	service.Close()
	service.Close()
	if err := service.Start("panel-a", []domainvault.Host{host("h1")}, time.Second); err == nil {
		t.Fatal("Start succeeded after Close")
	}
}
