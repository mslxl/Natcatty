package connection

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// execClient is a transport fake that exposes exec and a remote version.
type execClient struct {
	version  string
	commands []string
	result   ExecResult
	err      error
	closed   bool
}

func (c *execClient) OpenTerminal(context.Context, TerminalSpec) (Terminal, error) {
	return &fakeTerminal{}, nil
}

func (c *execClient) Close() error {
	c.closed = true
	return nil
}

func (c *execClient) RemoteVersion() string { return c.version }

func (c *execClient) Exec(_ context.Context, command string) (ExecResult, error) {
	c.commands = append(c.commands, command)
	return c.result, c.err
}

func (c *execClient) OpenExec(_ context.Context, command string) (ExecStream, error) {
	c.commands = append(c.commands, command)
	return &fakeExecStream{}, nil
}

// fakeExecStream is a closed pair of pipes with a fixed exit status.
type fakeExecStream struct{}

func (s *fakeExecStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *fakeExecStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *fakeExecStream) Close() error                { return nil }
func (s *fakeExecStream) Stderr() io.Reader           { return strings.NewReader("") }
func (s *fakeExecStream) ExitCode() (int, error)      { return 0, nil }

// execDialer returns one fixed transport and counts dials.
type execDialer struct {
	client Client
	dials  int
}

func (d *execDialer) Dial(context.Context, DialRequest) (Client, error) {
	d.dials++
	return d.client, nil
}

func newExecManager(t *testing.T, client Client) (*Manager, *execDialer) {
	t.Helper()
	dialer := &execDialer{client: client}
	manager := NewManager(dialer, &fakePasswordSource{password: "s3cret"}, &fakeKnownHosts{},
		WithIDGenerator(func() string { return "kh" }),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
	t.Cleanup(manager.CloseAll)
	return manager, dialer
}

func TestRemoteRequiresSingleChannelMatchesOnlySoftwareTokens(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"SSH-2.0-OpenSSH_9.6", false},
		{"SSH-2.0-dropbear_2022.83", false},
		{"SSH-2.0-JumpServer", false},
		{"SSH-2.0-BHostSSH_7.0", true},
		{"SSH-2.0-Qizhi TERM-SSHD_1.0", true},
		{"SSH-2.0-CLOUDBILITY-4.14", true},
		{"SSH-2.0-CloudLinker", true},
		// A token inside another word or product name is not a match.
		{"SSH-2.0-SuperBHOSTSSHx", false},
		{"SSH-2.0-NotTERM-SSHD-Other", false},
		{"", false},
	}
	for _, test := range tests {
		if got := RemoteRequiresSingleChannel(test.version); got != test.want {
			t.Errorf("RemoteRequiresSingleChannel(%q) = %v, want %v", test.version, got, test.want)
		}
	}
}

func TestLeaseReportsSingleChannelAndReuse(t *testing.T) {
	client := &execClient{version: "SSH-2.0-BHostSSH_7.0"}
	manager, dialer := newExecManager(t, client)

	first, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if first.SingleChannel() != true {
		t.Fatal("a single-channel banner was not reported")
	}
	if first.Reused() {
		t.Fatal("a freshly dialed lease reported itself as reused")
	}
	if first.RemoteVersion() != "SSH-2.0-BHostSSH_7.0" {
		t.Fatalf("RemoteVersion = %q", first.RemoteVersion())
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if dialer.dials != 1 {
		t.Fatalf("dials = %d, want the parked transport to be reused", dialer.dials)
	}
	t.Cleanup(func() { _ = second.Release() })
	if !second.Reused() {
		t.Fatal("a parked transport lease did not report reuse")
	}

	plain := &execClient{version: "SSH-2.0-OpenSSH_9.6"}
	otherManager, _ := newExecManager(t, plain)
	lease, err := otherManager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if lease.SingleChannel() {
		t.Fatal("OpenSSH was classified as single-channel")
	}
}

func TestLeaseRunsCommandsAndFailsClosedWithoutExec(t *testing.T) {
	client := &execClient{
		version: "SSH-2.0-OpenSSH_9.6",
		result:  ExecResult{Stdout: "/home/tester\n", ExitCode: 0, HasExitCode: true},
	}
	manager, _ := newExecManager(t, client)
	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	result, err := lease.Exec(context.Background(), "printf '%s\\n' \"$HOME\"")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Stdout != "/home/tester\n" || !result.HasExitCode || result.ExitCode != 0 {
		t.Fatalf("Exec result = %+v", result)
	}
	stream, err := lease.OpenExec(context.Background(), "scp -t -- '/tmp'")
	if err != nil {
		t.Fatalf("OpenExec: %v", err)
	}
	_ = stream.Close()
	if len(client.commands) != 2 || !strings.HasPrefix(client.commands[1], "scp -t") {
		t.Fatalf("commands = %v", client.commands)
	}

	// A released lease fails closed rather than reaching the transport again.
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := lease.Exec(context.Background(), "true"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Exec on a released lease = %v, want ErrClosed", err)
	}

	// A transport without the capability reports a typed unsupported error.
	plain := &fakeClient{}
	plainManager, _ := newExecManager(t, plain)
	plainLease, err := plainManager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = plainLease.Release() })
	if _, err := plainLease.Exec(context.Background(), "true"); !errors.Is(err, ErrExecUnavailable) {
		t.Fatalf("Exec on a transport without exec = %v, want ErrExecUnavailable", err)
	}
}
