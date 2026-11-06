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
	"sync/atomic"
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
	server := labServerNamed(t, "7.1.1")
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
	server := labServerNamed(t, "6.2.10")
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
	server := labServerNamed(t, "7.1.1")
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

// labProxy is a single-process TCP relay on the lab host. The victim's ET
// socket is pointed at it while the second client reaches etserver directly,
// so killing the relay takes only the victim offline and leaving its SSH lease
// up. A single process matters: exiting closes the listener and every relayed
// connection at once.
const labProxy = `
use strict;
use warnings;
use IO::Socket::INET;
use IO::Select;
$SIG{PIPE} = "IGNORE";
$| = 1;
my $port = shift @ARGV;
my $target = shift @ARGV;
my $listen = IO::Socket::INET->new(LocalAddr => "127.0.0.1", LocalPort => $port,
    Listen => 16, ReuseAddr => 1) or die "listen: $!";
print "READY\n";
my $select = IO::Select->new($listen);
my %peer;
while (1) {
  for my $fh ($select->can_read) {
    if ($fh == $listen) {
      my $client = $listen->accept or next;
      my $server = IO::Socket::INET->new(PeerAddr => "127.0.0.1",
          PeerPort => $target, Proto => "tcp") or do { close $client; next };
      $peer{$client} = $server;
      $peer{$server} = $client;
      $select->add($client);
      $select->add($server);
      next;
    }
    my $buffer;
    my $n = sysread($fh, $buffer, 65536);
    if (!defined $n || $n == 0) {
      my $other = $peer{$fh};
      if (defined $other) {
        $select->remove($other);
        delete $peer{$other};
        close $other;
      }
      $select->remove($fh);
      delete $peer{$fh};
      close $fh;
      next;
    }
    my $other = $peer{$fh};
    if (defined $other) {
      my $off = 0;
      while ($off < $n) {
        my $written = syswrite($other, $buffer, $n - $off, $off);
        last if !defined $written || $written == 0;
        $off += $written;
      }
    }
  }
}
`

// startLabProxy starts labProxy on port and returns a stop function. Stopping
// is idempotent and also clears a relay left by an earlier failed run.
func startLabProxy(t *testing.T, manager *appconnection.Manager,
	spec appsession.StartSpec, port, target int) func() {
	t.Helper()
	logPath := fmt.Sprintf("/tmp/natcatty-proxy-%d.log", port)
	pidPath := fmt.Sprintf("/tmp/natcatty-proxy-%d.pid", port)
	scriptPath := fmt.Sprintf("/tmp/natcatty-proxy-%d.pl", port)
	stop := func() {
		_ = labCommand(t, manager, spec, fmt.Sprintf(
			`sh -c 'if [ -f %[1]s ]; then kill $(cat %[1]s) 2>/dev/null || true; rm -f %[1]s; fi'`,
			pidPath))
	}
	stop()
	command := fmt.Sprintf(`cat > %[1]s <<'PERL'
%[2]s
PERL
rm -f %[3]s %[4]s
nohup perl %[1]s %[5]d %[6]d >%[3]s 2>&1 </dev/null &
echo $! >%[4]s
i=0
while [ "$i" -lt 200 ]; do
  if grep -q READY %[3]s 2>/dev/null; then echo READY; exit 0; fi
  i=$((i+1))
  sleep 0.05
done
echo NOTREADY
exit 1`, scriptPath, labProxy, logPath, pidPath, port, target)
	if output := labCommand(t, manager, spec, command); !strings.Contains(output, "READY") {
		t.Fatalf("start lab proxy on %d: output %q", port, output)
	}
	return stop
}

// labSessionFactory hands out the victim transport the test already built on
// the Session's first Open and refuses every later one. A displaced Session
// must end instead of asking for a replacement; a refused replacement fails
// the test quickly rather than leaving it in the reconnect loop.
type labSessionFactory struct {
	first appsession.Transport
	opens atomic.Int32
}

type labReconnectRefused struct{}

func (labReconnectRefused) Error() string   { return "test: Session reconnected after displacement" }
func (labReconnectRefused) Permanent() bool { return true }

func (f *labSessionFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	if f.opens.Add(1) > 1 {
		return nil, labReconnectRefused{}
	}
	return f.first, nil
}

// TestInProcessOfflineVictimIsDisplacedAgainstStock711 reproduces the
// sleeping-laptop takeover end to end. The victim runs through a relay while
// its SSH lease stays up; killing the relay takes the victim offline, a second
// reset-intent client takes the session and sends packets, and the relay comes
// back. The victim's first reconnect then sees a server reader ahead of its own
// send count. That must surface as NoticeETDisplaced and EndReasonDisplaced:
// treating it as a resume failure would make the default policy reconnect over
// the new owner.
func TestInProcessOfflineVictimIsDisplacedAgainstStock711(t *testing.T) {
	requireLab(t)
	server := labServerNamed(t, "7.1.1")
	manager := labManager(t, server.password)
	spec := labSpec(t, server)

	const proxyPort = 2031
	stopProxy := startLabProxy(t, manager, spec, proxyPort, etmodel.DefaultETPort)
	defer stopProxy()

	victimLease := acquireLabLease(t, manager, spec)
	forwarding, err := victimLease.Forwarding()
	if err != nil {
		t.Fatalf("victim forwarding: %v", err)
	}
	id, passkey := fixedLabCredentials(t)
	credentials := startLabTerminal(t, victimLease, id, passkey)

	// Build the victim through the production transport pieces. The bootstrap
	// is skipped only because the test needs the same credentials for the
	// second client; InProcessFactory.dial supplies the notice wiring.
	proxyAddress := net.JoinHostPort(etServerHost, strconv.Itoa(proxyPort))
	notices := make(chan appsession.Notice, 8)
	factory := InProcessFactory{Manager: manager}
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 30*time.Second)
	conn, err := factory.dial(connectCtx, forwarding, proxyAddress, credentials, notices)
	if err != nil {
		connectCancel()
		t.Fatalf("dial victim through relay: %v", err)
	}
	if err := etsession.Start(connectCtx, conn, etsession.Options{}); err != nil {
		connectCancel()
		_ = conn.Close()
		t.Fatalf("start victim session: %v", err)
	}
	connectCancel()
	runCtx, cancelRun := context.WithCancel(context.Background())
	victimDialer, err := newLeaseDialer(victimLease, nil)
	if err != nil {
		t.Fatalf("victim lease dialer: %v", err)
	}
	victim := newInProcessTransport(conn, victimDialer, newLocalTerminal(spec.Cols, spec.Rows),
		notices, cancelRun, nil, nil)
	go victim.run(runCtx)
	t.Cleanup(func() { _ = victim.Close() })

	openCounter := &labSessionFactory{first: victim}
	service := appsession.NewService(openCounter)
	sessionSpec := spec
	sessionSpec.Reconnect = &appsession.ReconnectPolicy{Delay: 250 * time.Millisecond}
	openCtx, openCancel := context.WithTimeout(context.Background(), 15*time.Second)
	session, err := service.Open(openCtx, sessionSpec)
	openCancel()
	if err != nil {
		t.Fatalf("open victim Session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	collector := newOutputCollector(session.TerminalConn())
	writeCommand(t, session.TerminalConn(), `echo VICTIM$((2+3))_READY`)
	collector.waitFor(t, "VICTIM5_READY", 20*time.Second)

	// Offline: the victim's ET link dies with the relay, but its SSH lease --
	// and so the Session -- keeps running while it redials.
	stopProxy()
	time.Sleep(500 * time.Millisecond)

	second, err := dialLab(t, forwarding, spec, credentials, true)
	if err != nil {
		t.Fatalf("attach second client: %v", err)
	}
	defer func() { _ = second.Close() }()
	if !second.Resumed() {
		t.Fatal("the reset-intent attach did not report Resumed")
	}
	// Empty lines are one packet each: the server's reader moves past the
	// victim's three packets (INITIAL_PAYLOAD, TERMINAL_INFO, its command).
	for range 12 {
		sendETInput(t, second, "\n")
	}
	sendETInput(t, second, "echo ATT$((7+9))_READY\n")
	readETUntil(t, second, "ATT16_READY", 20*time.Second)

	// The victim's redials resume now; the server reports the second
	// client's reader position, ahead of everything the victim sent.
	stopProxy = startLabProxy(t, manager, spec, proxyPort, etmodel.DefaultETPort)
	defer stopProxy()

	select {
	case <-session.Done():
	case <-time.After(60 * time.Second):
		t.Fatalf("the displaced Session did not end: snapshot=%+v opens=%d",
			session.Snapshot(), openCounter.opens.Load())
	}
	snapshot := session.Snapshot()
	if snapshot.EndReason != appsession.EndReasonDisplaced {
		t.Fatalf("EndReason = %q, want %q (snapshot %+v)",
			snapshot.EndReason, appsession.EndReasonDisplaced, snapshot)
	}
	if snapshot.Notice == nil || snapshot.Notice.Code != appsession.NoticeETDisplaced {
		t.Fatalf("Notice = %+v, want the ET displaced notice (snapshot %+v)",
			snapshot.Notice, snapshot)
	}
	if got := openCounter.opens.Load(); got != 1 {
		t.Fatalf("Session opened its transport %d times, want 1: a displaced Session must not auto-reconnect", got)
	}
}
