package app

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/tmux"
)

// The integration tests run against real sshd images and are opt-in, so a
// developer machine or CI lane without Docker skips them cleanly. The images
// are ordinary Ubuntu + openssh-server containers; the tmux image also ships
// tmux, the no-tmux image deliberately does not.
const (
	tmuxIntegrationEnvironment    = "NATCATTY_TMUX_INTEGRATION"
	defaultTmuxImage              = "natcatty/ssh-test:tmux"
	defaultNoTmuxImage            = "natcatty/ssh-test:no-tmux"
	integrationSSHUser            = "tester"
	integrationSSHPassword        = "testpass"
	integrationReadTimeout        = 20 * time.Second
	integrationContainerDeadline  = 60 * time.Second
	integrationCommandPauseMarker = "NATCATTY_READY"
)

// requireTmuxIntegration skips unless the operator opted in and Docker is
// usable. It checks the daemon, not just the CLI, so a stopped Docker Desktop
// produces a skip rather than a failure.
func requireTmuxIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(tmuxIntegrationEnvironment) == "" {
		t.Skipf("set %s=1 to run the real sshd+tmux integration tests", tmuxIntegrationEnvironment)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
		t.Skipf("docker daemon is unavailable: %v", err)
	}
}

func integrationImage(environment, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(environment)); value != "" {
		return value
	}
	return fallback
}

// startSSHContainer starts one image, publishes port 22 on a free loopback
// port, ensures the image has host keys, and waits for the SSH banner. The
// observable condition is the banner, with a hard container deadline.
func startSSHContainer(t *testing.T, image string) (host string, port int) {
	t.Helper()
	output, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::22", image).Output()
	if err != nil {
		t.Fatalf("docker run %s: %v", image, err)
	}
	id := strings.TrimSpace(string(output))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	ensureHostKeys(t, id)

	portOutput, err := exec.Command("docker", "port", id, "22").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	address := strings.TrimSpace(strings.Split(strings.TrimSpace(string(portOutput)), "\n")[0])
	separator := strings.LastIndex(address, ":")
	if separator < 0 {
		t.Fatalf("docker port output = %q, want host:port", address)
	}
	port, err = strconv.Atoi(address[separator+1:])
	if err != nil {
		t.Fatalf("docker port output = %q: %v", address, err)
	}
	host = address[:separator]

	waitForSSHBanner(t, net.JoinHostPort(host, strconv.Itoa(port)))
	return host, port
}

// ensureHostKeys generates the server keys once when an image ships without
// them. The test containers already have keys; the branch keeps the fixture
// usable with a fresh minimal image.
func ensureHostKeys(t *testing.T, container string) {
	t.Helper()
	if err := exec.Command("docker", "exec", container, "sh", "-c",
		"test -e /etc/ssh/ssh_host_ed25519_key").Run(); err == nil {
		return
	}
	if output, err := exec.Command("docker", "exec", container, "ssh-keygen", "-A").CombinedOutput(); err != nil {
		t.Fatalf("generate sshd host keys: %v\n%s", err, output)
	}
	// A container whose sshd already started without keys needs a restart to
	// pick them up. Both commands are best effort: the banner wait decides.
	_ = exec.Command("docker", "exec", container, "sh", "-c",
		"service ssh restart >/dev/null 2>&1 || true").Run()
	t.Log("the image had no sshd host keys; generated them once in the container")
}

func waitForSSHBanner(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(integrationContainerDeadline)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, readErr := bufio.NewReader(conn).ReadString('\n')
			_ = conn.Close()
			if readErr == nil && strings.HasPrefix(line, "SSH-") {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("sshd at %s did not send a banner within %s", address, integrationContainerDeadline)
}

// realSSHFactory wires the production Session factory to a real SSH dialer.
func realSSHFactory(t *testing.T) *transportFactory {
	t.Helper()
	dialer := sshadapter.NewDialer(sshadapter.WithTimeouts(15*time.Second, 30*time.Second))
	manager := appconnection.NewManager(
		dialer,
		vaultPasswords{vault: factoryPasswords{value: integrationSSHPassword}},
		&factoryKnownHosts{},
		appconnection.WithPrompter(remoteCommandPrompter{}),
	)
	t.Cleanup(manager.CloseAll)
	return newTransportFactory(&factoryLocal{}, nil, nil, manager, nil)
}

func openIntegrationTransport(t *testing.T, factory *transportFactory, hostID model.HostID, host string, port int, optIn bool) appsession.Transport {
	t.Helper()
	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind:           appsession.KindSSH,
		Label:          "tmux integration",
		HostID:         hostID,
		Hostname:       host,
		Port:           port,
		Username:       integrationSSHUser,
		Cols:           120,
		Rows:           30,
		AutoAttachTmux: optIn,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// readTerminalUntil waits for a marker in the terminal stream with a hard
// deadline. Terminal echoes and tmux redraws add noise; the marker is unique.
func readTerminalUntil(t *testing.T, transport appsession.Transport, marker string) string {
	t.Helper()
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		var buffer bytes.Buffer
		chunk := make([]byte, 4096)
		for {
			count, err := transport.Read(chunk)
			buffer.Write(chunk[:count])
			if strings.Contains(buffer.String(), marker) {
				done <- outcome{text: buffer.String()}
				return
			}
			if err != nil {
				done <- outcome{text: buffer.String(), err: err}
				return
			}
		}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("terminal ended before %q: %v\n%s", marker, got.err, got.text)
		}
		return got.text
	case <-time.After(integrationReadTimeout):
		t.Fatalf("terminal never produced %q within %s", marker, integrationReadTimeout)
		return ""
	}
}

func writeTerminal(t *testing.T, transport appsession.Transport, text string) {
	t.Helper()
	if _, err := transport.Write([]byte(text)); err != nil {
		t.Fatalf("terminal write: %v", err)
	}
}

// readinessCommand prints the pause marker without spelling it literally in
// the command input, so the PTY echo cannot satisfy the read before the shell
// actually ran the command.
func readinessCommand() string {
	return "printf 'NATCATTY_%s\\n' READY\n"
}

func TestTmuxIntegrationCreatesAttachesDetachesAndReattaches(t *testing.T) {
	requireTmuxIntegration(t)
	image := integrationImage("NATCATTY_TMUX_IMAGE", defaultTmuxImage)
	host, port := startSSHContainer(t, image)
	factory := realSSHFactory(t)
	hostID := model.HostID("01JTMUXINTEGRATION0000001")
	sessionName := tmux.SessionName(hostID)

	first := openIntegrationTransport(t, factory, hostID, host, port, true)
	assertNoNotice(t, first)
	writeTerminal(t, first, "printf 'NATCATTY_TMUX=%s\\n' \"${TMUX:+inside}\"; "+
		"tmux display-message -p 'NATCATTY_NAME=#{session_name}'\n")
	output := readTerminalUntil(t, first, "NATCATTY_NAME="+sessionName)
	if !strings.Contains(output, "NATCATTY_TMUX=inside") {
		t.Fatalf("the first connection is not inside tmux:\n%s", output)
	}
	writeTerminal(t, first, "export NATCATTY_KEEP=alive; "+readinessCommand())
	readTerminalUntil(t, first, integrationCommandPauseMarker)

	// Detach through tmux's client command. It ends the remote client with
	// status 0 exactly like the default prefix (C-b d) does, while the server
	// keeps the session. Typing the prefix right after the pane produced
	// output can land in the client's first-attach input window, where tmux
	// forwards the keys to the pane instead of handling them; that is tmux's
	// own timing, so the test drives the deterministic detach path.
	writeTerminal(t, first, "tmux detach-client\n")
	_ = readUntilEOF(t, first)
	if end := endOf(t, first); end.Reason != appsession.EndReasonDetached {
		t.Fatalf("detach end = %+v, want EndReasonDetached", end)
	}

	second := openIntegrationTransport(t, factory, hostID, host, port, true)
	awaitNotice(t, second, appsession.NoticeTmuxRestored)
	writeTerminal(t, second, "printf 'NATCATTY_KEEP=%s\\n' \"$NATCATTY_KEEP\"; "+
		"tmux display-message -p 'NATCATTY_NAME=#{session_name}'\n")
	output = readTerminalUntil(t, second, "NATCATTY_NAME="+sessionName)
	if !strings.Contains(output, "NATCATTY_KEEP=alive") {
		t.Fatalf("the reattached session lost its shell state:\n%s", output)
	}
	writeTerminal(t, second, "exit\n")
	_ = readUntilEOF(t, second)
	// Exiting the shell destroys the tmux session, so the wrapper exits with
	// tmux's own status instead of the detach status: the Tab keeps the exit.
	if end := endOf(t, second); end.Reason != appsession.EndReasonExited {
		t.Fatalf("shell exit after reattach = %+v, want an ordinary exit", end)
	}
}

func TestTmuxIntegrationFallsBackWithoutTmux(t *testing.T) {
	requireTmuxIntegration(t)
	image := integrationImage("NATCATTY_TMUX_NO_TMUX_IMAGE", defaultNoTmuxImage)
	host, port := startSSHContainer(t, image)
	factory := realSSHFactory(t)
	hostID := model.HostID("01JTMUXINTEGRATION0000002")

	first := openIntegrationTransport(t, factory, hostID, host, port, true)
	awaitNotice(t, first, appsession.NoticeTmuxUnavailable)
	writeTerminal(t, first, "printf 'NATCATTY_TMUX=%s\\n' \"${TMUX:-none}\"; "+readinessCommand())
	output := readTerminalUntil(t, first, integrationCommandPauseMarker)
	if !strings.Contains(output, "NATCATTY_TMUX=none") {
		t.Fatalf("the fallback is not a plain login shell:\n%s", output)
	}
	writeTerminal(t, first, "exit\n")
	_ = readUntilEOF(t, first)
	if end := endOf(t, first); end.Reason != appsession.EndReasonExited {
		t.Fatalf("plain fallback end = %+v, want an ordinary exit", end)
	}

	// A missing tmux is announced once per Host, not on every connection.
	second := openIntegrationTransport(t, factory, hostID, host, port, true)
	assertNoNotice(t, second)
	writeTerminal(t, second, "printf 'NATCATTY_SECOND=%s\\n' \"${TMUX:-none}\"; "+readinessCommand())
	readTerminalUntil(t, second, integrationCommandPauseMarker)
	writeTerminal(t, second, "exit\n")
	_ = readUntilEOF(t, second)
}

// TestTmuxIntegrationCommandShape pins the deterministic name on the wire: the
// wrapper command carries the sanitized session name and the probe quotes it.
func TestTmuxIntegrationCommandShape(t *testing.T) {
	requireTmuxIntegration(t)
	image := integrationImage("NATCATTY_TMUX_IMAGE", defaultTmuxImage)
	host, port := startSSHContainer(t, image)
	factory := realSSHFactory(t)
	hostID := model.HostID("01JTMUXINTEGRATION0000003")
	sessionName := tmux.SessionName(hostID)

	transport := openIntegrationTransport(t, factory, hostID, host, port, true)
	writeTerminal(t, transport, "tmux display-message -p 'NATCATTY_NAME=#{session_name}'\n")
	output := readTerminalUntil(t, transport, "NATCATTY_NAME="+sessionName)
	if !strings.Contains(output, "NATCATTY_NAME="+sessionName+"\r") &&
		!strings.Contains(output, "NATCATTY_NAME="+sessionName+"\n") {
		t.Fatalf("session name not reported exactly:\n%s", output)
	}
	writeTerminal(t, transport, "exit\n")
	_ = readUntilEOF(t, transport)
}
