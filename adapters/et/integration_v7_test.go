// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/bootstrap"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	etsession "github.com/mslxl/natcatty/adapters/et/internal/etgo/session"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	"google.golang.org/protobuf/proto"
)

// fixedLabCredentials returns a fresh alphanumeric id/passkey pair that
// etterminal honours as-is. Unlike the bootstrap's XXX placeholder, the same
// values can be used for a second attach. Uniqueness keeps successive test
// runs from attaching to a stale server-side session.
func fixedLabCredentials(t *testing.T) (string, string) {
	t.Helper()
	id := "T" + rand.Text()[:15]
	passkey := (rand.Text() + rand.Text())[:32]
	return id, passkey
}

// acquireLabLease takes a lease that carries both the etterminal bootstrap
// exec channel and the ET forwarding channel.
func acquireLabLease(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec) *appconnection.Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, appconnection.Request{
		HostID:    spec.HostID,
		Hostname:  spec.Hostname,
		Port:      spec.Port,
		Username:  spec.Username,
		LeaseKind: appconnection.LeaseShell,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	return lease
}

// startLabTerminal runs etterminal with fixed credentials and waits until it
// prints IDPASSKEY, which proves the server registered the session.
func startLabTerminal(t *testing.T, lease ConnectionLease, id, passkey string) bootstrap.Credentials {
	t.Helper()
	command, err := bootstrap.RemoteCommand(id, passkey, bootstrap.DefaultTerm, "")
	if err != nil {
		t.Fatalf("RemoteCommand: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := lease.OpenExec(ctx, command)
	if err != nil {
		t.Fatalf("OpenExec(etterminal): %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readCancel()
	credentials, err := readBootstrap(readCtx, stream)
	if err != nil {
		t.Fatalf("read IDPASSKEY: %v", err)
	}
	if credentials.ID != id || credentials.Passkey() != passkey {
		t.Fatalf("etterminal returned id %q, want the fixed id", credentials.ID)
	}
	return credentials
}

func labETAddress(spec appsession.StartSpec) string {
	return net.JoinHostPort(etServerHost, strconv.Itoa(spec.ET.ETPort))
}

// dialLab dials the ET server through the lease's forwarding channel.
func dialLab(t *testing.T, forwarding appconnection.ForwardingClient, spec appsession.StartSpec,
	credentials bootstrap.Credentials, resetIntent bool) (*etcp.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dialer := &etcp.Dialer{
		NetDialer:   noDeadlineDialer{forwarding: forwarding},
		ResetIntent: resetIntent,
	}
	return dialer.Dial(ctx, labETAddress(spec), credentials.ID, credentials.Passkey())
}

func startLabSession(t *testing.T, conn *etcp.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := etsession.Start(ctx, conn, etsession.Options{}); err != nil {
		t.Fatalf("ETSESSION Start: %v", err)
	}
}

func sendETInput(t *testing.T, conn *etcp.Conn, text string) {
	t.Helper()
	buffer := &protocol.TerminalBuffer{}
	buffer.SetBuffer([]byte(text))
	payload, err := proto.Marshal(buffer)
	if err != nil {
		t.Fatalf("encode terminal input: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := conn.WritePacket(ctx, protocol.Packet{
		Header: protocol.HeaderTerminalBuffer, Payload: payload,
	}); err != nil {
		t.Fatalf("write terminal input: %v", err)
	}
}

// readETUntil collects terminal output until want appears, skipping the
// protocol's non-terminal packets. want must not also appear in the shell's
// echo of the command that produces it, or the echo would end the wait.
func readETUntil(t *testing.T, conn *etcp.Conn, want string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var output strings.Builder
	for {
		packet, err := conn.ReadPacket(ctx)
		if err != nil {
			t.Fatalf("read while waiting for %q: %v; output = %q", want, err, output.String())
		}
		if packet.Header != protocol.HeaderTerminalBuffer {
			continue
		}
		buffer := &protocol.TerminalBuffer{}
		if err := proto.Unmarshal(packet.Payload, buffer); err != nil {
			t.Fatalf("decode terminal buffer: %v", err)
		}
		output.Write(buffer.GetBuffer())
		if strings.Contains(output.String(), want) {
			return output.String()
		}
	}
}

// readETUntilAfter waits for first, then for then after it. The caller uses it
// to wait for a command's result and the prompt that follows, so no output
// from before the following attach is still in flight.
func readETUntilAfter(t *testing.T, conn *etcp.Conn, first, then string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var output strings.Builder
	seen, after := false, 0
	for {
		packet, err := conn.ReadPacket(ctx)
		if err != nil {
			t.Fatalf("read while waiting for %q after %q: %v; output = %q", then, first, err, output.String())
		}
		if packet.Header != protocol.HeaderTerminalBuffer {
			continue
		}
		buffer := &protocol.TerminalBuffer{}
		if err := proto.Unmarshal(packet.Payload, buffer); err != nil {
			t.Fatalf("decode terminal buffer: %v", err)
		}
		output.Write(buffer.GetBuffer())
		text := output.String()
		if !seen {
			if index := strings.Index(text, first); index >= 0 {
				seen, after = true, index+len(first)
			}
		}
		if seen && strings.Contains(text[after:], then) {
			return text
		}
	}
}

// TestInProcessAttachDisplacesFirstClientAgainstStock711 walks the full v7
// path against a real 7.1.1 server: a normal client starts a shell, a second
// reset-intent client attaches with the same credentials, the attaching client
// starts blank (no prior output replay), and the first client is told through
// the production notice channel that another client took its session.
func TestInProcessAttachDisplacesFirstClientAgainstStock711(t *testing.T) {
	requireLab(t)
	server := labServers()[0] // etserver 7.1.1
	manager := labManager(t, server.password)
	spec := labSpec(t, server)
	lease := acquireLabLease(t, manager, spec)
	id, passkey := fixedLabCredentials(t)
	credentials := startLabTerminal(t, lease, id, passkey)
	forwarding, err := lease.Forwarding()
	if err != nil {
		t.Fatalf("Forwarding: %v", err)
	}

	notices := make(chan appsession.Notice, 8)
	factory := InProcessFactory{}
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 30*time.Second)
	first, err := factory.dial(dialCtx, forwarding, labETAddress(spec), credentials, notices)
	dialCancel()
	if err != nil {
		t.Fatalf("dial first client: %v", err)
	}
	defer func() { _ = first.Close() }()
	startLabSession(t, first)
	// The result 3333 is absent from the command's echo, and the prompt after
	// it proves the whole old output was delivered before the attach.
	sendETInput(t, first, "echo $((1111+2222))\n")
	readETUntilAfter(t, first, "3333", "$ ", 20*time.Second)

	second, err := dialLab(t, forwarding, spec, credentials, true)
	if err != nil {
		t.Fatalf("attach second client: %v", err)
	}
	defer func() { _ = second.Close() }()
	if !second.Resumed() {
		t.Fatal("the reset-intent attach did not report Resumed, so INITIAL_PAYLOAD was not skipped")
	}

	// No INITIAL_PAYLOAD on the attach: the shell already runs. Stock ET has
	// no server-side screen snapshot, so the terminal starts blank; only
	// output produced after the attach may arrive. The command's result 12221
	// and its echo contain neither 3333 nor 1111+2222.
	sendETInput(t, second, "echo $((5555+6666))\n")
	attachedOutput := readETUntil(t, second, "12221", 20*time.Second)
	if strings.Contains(attachedOutput, "3333") || strings.Contains(attachedOutput, "1111+2222") {
		t.Fatalf("the attached terminal replayed the earlier screen: %q", attachedOutput)
	}

	select {
	case notice := <-notices:
		if notice.Code != appsession.NoticeETDisplaced {
			t.Fatalf("notice = %+v, want the ET displaced notice", notice)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the displaced first client never reported a notice")
	}
}

// TestInProcessLegacyReattachFailsLikeStock6210 proves a v7-only attach
// against a real 6.2.10 server fails with the same clear upgrade outcome as
// stock et, while the ordinary legacy session still works.
func TestInProcessLegacyReattachFailsLikeStock6210(t *testing.T) {
	requireLab(t)
	server := labServers()[1] // etserver 6.2.10
	manager := labManager(t, server.password)
	spec := labSpec(t, server)
	lease := acquireLabLease(t, manager, spec)
	id, passkey := fixedLabCredentials(t)
	credentials := startLabTerminal(t, lease, id, passkey)
	forwarding, err := lease.Forwarding()
	if err != nil {
		t.Fatalf("Forwarding: %v", err)
	}

	first, err := dialLab(t, forwarding, spec, credentials, false)
	if err != nil {
		t.Fatalf("legacy dial: %v", err)
	}
	defer func() { _ = first.Close() }()
	startLabSession(t, first)
	sendETInput(t, first, "echo $((1010+2020))\n")
	readETUntilAfter(t, first, "3030", "$ ", 20*time.Second)

	_, err = dialLab(t, forwarding, spec, credentials, true)
	if !errors.Is(err, etcp.ErrLegacyReattach) {
		t.Fatalf("legacy attach = %v, want ErrLegacyReattach", err)
	}
	if !strings.Contains(err.Error(), "does not support session reattach; upgrade etserver") {
		t.Fatalf("legacy attach error = %q, want stock's upgrade sentence", err)
	}
	mapped := classifyDialError(err)
	if !errors.Is(mapped, etmodel.ErrReattachUnsupported) {
		t.Fatalf("classified legacy attach = %v, want ErrReattachUnsupported", mapped)
	}
}

// TestRetryLaterDrivesBackoffAgainstStock711 starts a fresh etserver whose
// session table is empty. A connect for an unknown client inside the recovery
// grace window gets RETRY_LATER; the adapter must redial instead of ending the
// session, which the server log shows as repeated retry admissions.
func TestRetryLaterDrivesBackoffAgainstStock711(t *testing.T) {
	requireLab(t)
	server := labServers()[0]
	manager := labManager(t, server.password)
	spec := labSpec(t, server)
	lease := acquireLabLease(t, manager, spec)

	const port = 2026
	logPath := fmt.Sprintf("/tmp/natcatty-etserver-%d.log", port)
	pidPath := fmt.Sprintf("/tmp/natcatty-etserver-%d.pid", port)
	stop := startLabServer(t, manager, spec, port, logPath, pidPath)
	defer stop()

	id, passkey := fixedLabCredentials(t)
	credentials := bootstrap.NewCredentials(id, passkey)
	forwarding, err := lease.Forwarding()
	if err != nil {
		t.Fatalf("Forwarding: %v", err)
	}
	factory := InProcessFactory{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = factory.dial(ctx, forwarding, net.JoinHostPort(etServerHost, strconv.Itoa(port)),
		credentials, make(chan appsession.Notice, 8))
	if err == nil {
		t.Fatal("a deterministic RETRY_LATER server returned a connection")
	}
	if !errors.Is(err, etcp.ErrRetryLater) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial = %v, want a retry/deadline outcome", err)
	}

	output := labCommand(t, manager, spec, "grep -c 'Within the recovery grace window' "+logPath+" || true")
	count, convErr := strconv.Atoi(strings.TrimSpace(output))
	if convErr != nil {
		t.Fatalf("retry log count %q: %v", output, convErr)
	}
	if count < 2 {
		t.Fatalf("server saw %d RETRY_LATER admissions, want at least 2: the client did not back off and retry", count)
	}
}

// startLabServer runs a fresh stock etserver on port and waits until it logged
// its listening line. The returned stop kills only that pid.
func startLabServer(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec,
	port int, logPath, pidPath string) func() {
	t.Helper()
	script := fmt.Sprintf(
		`sh -c 'rm -f %[1]s %[2]s; nohup etserver --port %[3]d --logtostdout >%[1]s 2>&1 </dev/null & echo $! >%[2]s; i=0; while [ "$i" -lt 200 ]; do if grep -q "Listening on" %[1]s 2>/dev/null; then echo READY; exit 0; fi; i=$((i+1)); sleep 0.05; done; echo NOTREADY; exit 1'`,
		logPath, pidPath, port)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, appconnection.Request{
		HostID: spec.HostID, Hostname: spec.Hostname, Port: spec.Port,
		Username: spec.Username, LeaseKind: appconnection.LeaseShell,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		t.Fatalf("Acquire for etserver: %v", err)
	}
	stream, err := lease.OpenExec(ctx, script)
	if err != nil {
		_ = lease.Discard()
		t.Fatalf("start etserver: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		buffer := make([]byte, 256)
		var output strings.Builder
		for {
			n, readErr := stream.Read(buffer)
			output.Write(buffer[:n])
			if strings.Contains(output.String(), "READY") {
				ready <- nil
				return
			}
			if readErr != nil {
				ready <- fmt.Errorf("etserver output %q: %w", output.String(), readErr)
				return
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = stream.Close()
			_ = lease.Discard()
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		_ = stream.Close()
		_ = lease.Discard()
		t.Fatal("timed out waiting for the fresh etserver to listen")
	}
	return func() {
		_ = stream.Close()
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer killCancel()
		if killLease, killErr := manager.Acquire(killCtx, appconnection.Request{
			HostID: spec.HostID, Hostname: spec.Hostname, Port: spec.Port,
			Username: spec.Username, LeaseKind: appconnection.LeaseShell,
			Keepalive: &appconnection.DefaultKeepalive,
		}); killErr == nil {
			_, _ = killLease.Exec(killCtx, "kill $(cat "+pidPath+") 2>/dev/null || true")
			_ = killLease.Release()
		}
		_ = lease.Discard()
	}
}

// labCommand runs one command through a fresh lease and returns its stdout.
func labCommand(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec, command string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := manager.Acquire(ctx, appconnection.Request{
		HostID: spec.HostID, Hostname: spec.Hostname, Port: spec.Port,
		Username: spec.Username, LeaseKind: appconnection.LeaseShell,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		t.Fatalf("Acquire for command: %v", err)
	}
	defer func() { _ = lease.Release() }()
	output, err := lease.Exec(ctx, command)
	if err != nil {
		t.Fatalf("remote command %q: %v (stdout %q, stderr %q)", command, err, output.Stdout, output.Stderr)
	}
	return output.Stdout
}
