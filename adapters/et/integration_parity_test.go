// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	xssh "golang.org/x/crypto/ssh"
)

// TestLabServerVersionIsPinned proves a cell really runs the stock server
// version the matrix claims, instead of a stray local etserver or a mock. The
// gate sets NATCATTY_ET_LAB_EXPECT_VERSION; an ad-hoc local lab may omit it,
// but still gets the server's self-reported version checked against the name.
func TestLabServerVersionIsPinned(t *testing.T) {
	requireLab(t)
	expected := strings.TrimSpace(os.Getenv("NATCATTY_ET_LAB_EXPECT_VERSION"))
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			if expected != "" && server.version != expected {
				t.Fatalf("cell advertises %s but the matrix expects %s", server.version, expected)
			}
			manager := labManager(t, server.password)
			output := labCommand(t, manager, labSpec(t, server), "etserver --version 2>&1")
			if !strings.Contains(output, "et version "+server.version) {
				t.Fatalf("etserver --version = %q, want version %s", output, server.version)
			}
		})
	}
}

// labKey is one test-generated SSH key.
type labKey struct {
	private    ed25519.PrivateKey
	privatePEM string
	authorized string
}

func newLabKey(t *testing.T) labKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("key signer: %v", err)
	}
	block, err := xssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return labKey{
		private:    private,
		privatePEM: string(pem.EncodeToMemory(block)),
		authorized: string(xssh.MarshalAuthorizedKey(signer.PublicKey())),
	}
}

// labCertificate signs the key's public half for principal and returns the
// CA public key and the user certificate in authorized_keys form.
func (k labKey) certificateFor(t *testing.T, principal string) (string, string) {
	t.Helper()
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caSigner, err := xssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatalf("CA signer: %v", err)
	}
	userSigner, err := xssh.NewSignerFromKey(k.private)
	if err != nil {
		t.Fatalf("user signer: %v", err)
	}
	certificate := &xssh.Certificate{
		Key:             userSigner.PublicKey(),
		CertType:        xssh.UserCert,
		KeyId:           "natcatty-et-parity",
		ValidPrincipals: []string{principal},
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
		// OpenSSH only permits these actions when the certificate carries the
		// extension; without it the certificate authenticates but direct-tcpip
		// (the ET bootstrap channel) is denied. ET additionally opens a PTY on
		// the remote shell side, so grant that too.
		Extensions: map[string]string{
			"permit-port-forwarding": "",
			"permit-pty":             "",
		},
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	return string(xssh.MarshalAuthorizedKey(caSigner.PublicKey())),
		string(xssh.MarshalAuthorizedKey(certificate))
}

func installAuthorizedKey(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec, key string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(key))
	command := "mkdir -p ~/.ssh && chmod 700 ~/.ssh && " +
		"printf %s " + encoded + " | base64 -d >> ~/.ssh/authorized_keys && " +
		"chmod 600 ~/.ssh/authorized_keys"
	if output := labCommand(t, manager, spec, command); strings.TrimSpace(output) != "" {
		t.Fatalf("install authorized key printed %q", output)
	}
}

func installTrustedCA(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec, ca string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(ca))
	command := "printf %s " + encoded + " | base64 -d | " +
		"sudo tee /etc/ssh/lab_user_ca.pub >/dev/null && sudo chmod 644 /etc/ssh/lab_user_ca.pub"
	if output := labCommand(t, manager, spec, command); strings.TrimSpace(output) != "" {
		t.Fatalf("install trusted CA printed %q", output)
	}
}

// labManagerWithAuth builds a Manager whose AuthSource returns plan for the
// session's Host. dialer is the shared SSH dialer (agent tests inject one).
func labManagerWithAuth(t *testing.T, dialer *sshadapter.Dialer,
	plan appconnection.AuthPlan, hostID model.HostID) *appconnection.Manager {
	t.Helper()
	manager := appconnection.NewManager(
		dialer, nil, labKnownHosts{},
		appconnection.WithPrompter(labTrust{}),
		appconnection.WithAuthSource(authPlanMap{hostID: plan}),
	)
	t.Cleanup(manager.CloseAll)
	return manager
}

func openAndCheckUser(t *testing.T, factory InProcessFactory, spec appsession.StartSpec, wantUser string) {
	t.Helper()
	transport := openLab(t, factory, spec)
	output := newOutputCollector(transport)
	writeCommand(t, transport, `printf 'ET_USER_%s\n' "$(id -un)"`)
	output.waitFor(t, "ET_USER_"+wantUser, 20*time.Second)
}

// TestLabAuthenticationMethods runs every shared SSH authentication method
// against the real lab sshd, with the real Connection Manager and the real
// in-process ET bootstrap. The selected key, agent, certificate, and identity
// subtests use a different plan username from the fallback to prove the Vault
// plan wins.
func TestLabAuthenticationMethods(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			setupManager := labManager(t, server.password)
			spec := labSpec(t, server)
			key := newLabKey(t)
			installAuthorizedKey(t, setupManager, spec, key.authorized)
			caPublic, certificate := key.certificateFor(t, server.username)
			installTrustedCA(t, setupManager, spec, caPublic)

			fallback := labSpec(t, server)
			fallback.Username = "not-the-selected-user"
			fallback.ET.Username = fallback.Username

			t.Run("password", func(t *testing.T) {
				manager := labManager(t, server.password)
				openAndCheckUser(t, InProcessFactory{Manager: manager}, spec, server.username)
			})

			t.Run("selected-key", func(t *testing.T) {
				plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
					Username: server.username,
					Mode:     appconnection.AuthModeKey,
					SelectedKey: &appconnection.KeyMaterial{
						ID: "parity-key", Label: "parity key", PrivateKey: key.privatePEM,
					},
				})
				manager := labManagerWithAuth(t, sshadapter.NewDialer(), plan, spec.HostID)
				openAndCheckUser(t, InProcessFactory{Manager: manager}, fallback, server.username)
			})

			t.Run("agent", func(t *testing.T) {
				dialer := sshadapter.NewDialer(sshadapter.WithAgentDial(testAgentDial(t, key.private)))
				plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
					Username: server.username,
					Mode:     appconnection.AuthModeAuto,
					UseAgent: true,
				})
				manager := labManagerWithAuth(t, dialer, plan, spec.HostID)
				openAndCheckUser(t, InProcessFactory{Manager: manager}, fallback, server.username)
			})

			t.Run("certificate", func(t *testing.T) {
				plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
					Username: server.username,
					Mode:     appconnection.AuthModeCertificate,
					SelectedKey: &appconnection.KeyMaterial{
						ID: "parity-cert", Label: "parity certificate",
						PrivateKey: key.privatePEM, Certificate: certificate,
					},
				})
				manager := labManagerWithAuth(t, sshadapter.NewDialer(), plan, spec.HostID)
				openAndCheckUser(t, InProcessFactory{Manager: manager}, fallback, server.username)
			})

			t.Run("identity", func(t *testing.T) {
				plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
					Username: "tester2",
					Mode:     appconnection.AuthModePassword,
					Password: server.password,
				})
				manager := labManagerWithAuth(t, sshadapter.NewDialer(), plan, spec.HostID)
				openAndCheckUser(t, InProcessFactory{Manager: manager}, spec, "tester2")
			})
		})
	}
}

// TestLabKeepAliveKeepsTheLinkWithoutReconnect pins the ET keepalive contract
// against a real server: with a short probe period and an idle session, the
// server echoes probes and the supervisor never reacquires the SSH lease. A
// broken keepalive would declare the link dead and reconnect.
func TestLabKeepAliveKeepsTheLinkWithoutReconnect(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			var mu sync.Mutex
			acquires := 0
			factory := InProcessFactory{
				Manager:   manager,
				KeepAlive: 300 * time.Millisecond,
			}
			factory.Acquire = func(ctx context.Context, req appconnection.Request) (ConnectionLease, error) {
				lease, err := manager.Acquire(ctx, req)
				if err == nil {
					mu.Lock()
					acquires++
					mu.Unlock()
				}
				return lease, err
			}
			transport := openLab(t, factory, labSpec(t, server))
			output := newOutputCollector(transport)

			writeCommand(t, transport, `printf 'ET_KEEP_ALIVE_FIRST\n'`)
			output.waitFor(t, "ET_KEEP_ALIVE_FIRST", 20*time.Second)

			// Wait well past two keepalive periods: alive means the server
			// echoed probes. This is a protocol deadline, not a sleep used to
			// wait for an app to settle.
			time.Sleep(2 * time.Second)

			writeCommand(t, transport, `printf 'ET_KEEP_ALIVE_SECOND\n'`)
			output.waitFor(t, "ET_KEEP_ALIVE_SECOND", 20*time.Second)

			mu.Lock()
			got := acquires
			mu.Unlock()
			if got != 1 {
				t.Fatalf("the Session acquired %d leases; keepalive should have kept the first link", got)
			}
		})
	}
}

// TestLabLargeOutputFramingAndSecretbox drives a 256 KiB terminal write
// through a real etserver. Every byte crosses the 4-byte framing, the 16 KiB
// frame limit, secretbox stream chunking, and the session router; a framing or
// keystream defect loses or corrupts output.
func TestLabLargeOutputFramingAndSecretbox(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			transport := openLab(t, InProcessFactory{Manager: manager}, labSpec(t, server))
			output := newOutputCollector(transport)

			disableEcho(t, transport, output)
			writeCommand(t, transport, `printf 'ET_BIG_%s\n' "$((100+1))"; head -c 262144 /dev/zero | tr '\0' 'A'; printf '\nET_BIG_%s\n' "$((100+2))"`)
			text := output.waitFor(t, "ET_BIG_102", 60*time.Second)
			body, ok := betweenMarkers(text, "ET_BIG_101", "ET_BIG_102")
			if !ok {
				t.Fatalf("large output markers missing in %d bytes", len(text))
			}
			if got := strings.Count(body, "A"); got != 262144 {
				t.Fatalf("large output carried %d A bytes, want 262144", got)
			}
		})
	}
}

// TestLabScrollbackKeepsAllOutput proves the transport delivers more lines
// than any terminal screen holds, in order and without loss. The client
// terminal keeps those lines as scrollback; the adapter's contract is to hand
// every byte over.
func TestLabScrollbackKeepsAllOutput(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			transport := openLab(t, InProcessFactory{Manager: manager}, labSpec(t, server))
			output := newOutputCollector(transport)

			disableEcho(t, transport, output)
			writeCommand(t, transport, `for i in $(seq 1 400); do printf 'ET_LINE_%s\n' "$i"; done; printf 'ET_SCROLL_%s\n' DONE`)
			text := output.waitFor(t, "ET_SCROLL_DONE", 60*time.Second)
			position := 0
			for line := 1; line <= 400; line++ {
				marker := fmt.Sprintf("ET_LINE_%d\n", line)
				index := strings.Index(text[position:], marker)
				if index < 0 {
					t.Fatalf("line %d missing or out of order after byte %d", line, position)
				}
				position += index + len(marker)
			}
		})
	}
}

// goldenScript is the deterministic terminal payload used by the golden and
// stock-client comparisons. It exercises SGR colour, a tab, UTF-8 bytes, CR,
// and backspace. Output processing is disabled so the bytes are the same on
// every distribution.
const goldenScript = `printf 'ET_GOLDEN_%s\n' START
printf '\033[31mred\033[0m\tTAB\n'
printf 'UTF8: caf\303\251 \344\270\226\347\225\214\n'
printf 'CR:\rX\n'
printf 'BS:a\bB\n'
printf 'ET_GOLDEN_%s\n' END`

const (
	goldenStart = "ET_GOLDEN_START"
	goldenEnd   = "ET_GOLDEN_END"
)

// disableEcho turns off input echo and output NL translation, then waits for
// a computed marker. The marker is not present literally in the command, so
// the terminal driver's echo of the setup line cannot satisfy the wait before
// stty has run.
func disableEcho(t *testing.T, transport appsession.Transport, output *outputCollector) {
	t.Helper()
	writeCommand(t, transport, `stty -echo -onlcr; PS1=; printf 'ET_SETUP_%s\n' "$((40+2))"`)
	output.waitFor(t, "ET_SETUP_42", 20*time.Second)
}

func betweenMarkers(text, start, end string) (string, bool) {
	startIndex := strings.Index(text, start)
	if startIndex < 0 {
		return "", false
	}
	afterStart := startIndex + len(start)
	endIndex := strings.Index(text[afterStart:], end)
	if endIndex < 0 {
		return "", false
	}
	return text[afterStart : afterStart+endIndex], true
}

func goldenPath() string {
	return filepath.Join("testdata", "golden", "terminal-output.golden")
}

// TestLabGoldenTerminalOutput captures the exact bytes a real stock etserver
// returns for a scripted session and compares them to the committed golden.
// Set NATCATTY_ET_UPDATE_GOLDEN=1 to regenerate intentionally.
func TestLabGoldenTerminalOutput(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			transport := openLab(t, InProcessFactory{Manager: manager}, labSpec(t, server))
			output := newOutputCollector(transport)

			disableEcho(t, transport, output)
			text := runGoldenScript(t, transport, output)
			body, ok := betweenMarkers(text, goldenStart, goldenEnd)
			if !ok {
				t.Fatalf("golden markers missing in %q", text)
			}
			body = strings.TrimSuffix(body, "\n")

			if os.Getenv("NATCATTY_ET_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(goldenPath()), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath(), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("updated %s (%d bytes)", goldenPath(), len(body))
				return
			}
			want, err := os.ReadFile(goldenPath())
			if err != nil {
				t.Fatalf("read golden (run with NATCATTY_ET_UPDATE_GOLDEN=1 to create): %v", err)
			}
			if body != string(want) {
				t.Fatalf("terminal golden mismatch\n got: %s\nwant: %s\n got hex: %s\nwant hex: %s",
					hex.EncodeToString([]byte(body)), hex.EncodeToString(want),
					hex.EncodeToString([]byte(body)), hex.EncodeToString(want))
			}
		})
	}
}

// runGoldenScript writes the script into the lab home and runs it in a
// non-interactive shell, so readline's own bracketed-paste escapes cannot
// pollute the captured bytes. The interactive login shell reads only the
// base64 write command; its echo is already disabled.
func runGoldenScript(t *testing.T, transport appsession.Transport, output *outputCollector) string {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(goldenScript + "\n"))
	writeCommand(t, transport,
		"printf %s "+encoded+" | base64 -d > /tmp/natcatty-et-golden.sh && sh /tmp/natcatty-et-golden.sh")
	return output.waitFor(t, goldenEnd, 30*time.Second)
}

// prepareStockET installs the generated key, SSH config, and golden script
// for the stock et client.
func prepareStockET(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec,
	key labKey, scriptPath string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(key.privatePEM))
	setup := "set -e; mkdir -p ~/.ssh; chmod 700 ~/.ssh; " +
		"printf %s " + encoded + " | base64 -d > /tmp/natcatty-stock-key; chmod 600 /tmp/natcatty-stock-key; " +
		"printf '%s\n' 'Host 127.0.0.1' '  IdentityFile /tmp/natcatty-stock-key' " +
		"'  StrictHostKeyChecking no' '  UserKnownHostsFile /dev/null' " +
		"'  BatchMode yes' > ~/.ssh/config; chmod 600 ~/.ssh/config"
	if output := labCommand(t, manager, spec, setup); strings.TrimSpace(output) != "" {
		t.Fatalf("stock et setup printed %q", output)
	}
	encodedScript := base64.StdEncoding.EncodeToString([]byte(goldenScript + "\n"))
	writeScript := "printf %s " + encodedScript + " | base64 -d > " + scriptPath
	if output := labCommand(t, manager, spec, writeScript); strings.TrimSpace(output) != "" {
		t.Fatalf("write stock script printed %q", output)
	}
}

// stockETCommand7 runs stock 7.1.1 in command mode. In 7.1.1 `-c` is the
// bootstrap cipher, not a command; --command is the command flag.
func stockETCommand7(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec,
	scriptPath string) string {
	t.Helper()
	return labCommand(t, manager, spec,
		"et --silent --port 2022 -u "+spec.Username+" --command 'sh "+scriptPath+"' 127.0.0.1 2>/dev/null")
}

// stockETInteractive runs stock 6.2.10 under tmux and returns the rendered
// pane. Its client aborts in command mode (a stock 6.2.10 std::bad_cast bug),
// so the oracle drives an interactive session and reads the rendered screen,
// which is what a user of stock et sees.
func stockETInteractive(t *testing.T, manager *appconnection.Manager, spec appsession.StartSpec,
	scriptPath string) string {
	t.Helper()
	run := func(command string) string { return labCommand(t, manager, spec, command) }
	_ = run("tmux kill-session -t natcatty-stock 2>/dev/null || true")
	// 6.2.10's -u/--username option has no cxxopts value type and aborts
	// with std::bad_cast; the user@host positional is the supported spelling.
	_ = run("tmux new-session -d -s natcatty-stock -x 120 -y 40 'et --silent " +
		spec.Username + "@127.0.0.1'")
	defer run("tmux kill-session -t natcatty-stock 2>/dev/null || true")

	waitForPane(t, run, func(text string) bool {
		// capture-pane trims trailing whitespace, so the prompt ends at "$".
		return strings.HasSuffix(strings.TrimSpace(text), "$")
	}, 20*time.Second)
	_ = run("tmux send-keys -t natcatty-stock 'sh " + scriptPath + "' Enter")
	waitForPane(t, run, func(text string) bool {
		return strings.Contains(text, goldenEnd)
	}, 30*time.Second)
	return run("tmux capture-pane -p -S -2000 -t natcatty-stock")
}

// waitForPane polls the real tmux pane until check sees the expected state or
// the hard deadline passes. The cadence is not a synchronization assumption;
// every attempt reads the current pane.
func waitForPane(t *testing.T, run func(string) string, check func(string) bool, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var pane string
	for {
		pane = run("tmux capture-pane -p -S -2000 -t natcatty-stock")
		if check(pane) {
			return pane
		}
		if time.Now().After(deadline) {
			t.Fatalf("tmux pane did not reach the expected state in %s; pane:\n%s", timeout, pane)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// renderedGoldenLines extracts the visible lines the stock client displayed
// between the markers.
func renderedGoldenLines(pane string) ([]string, bool) {
	inBody := false
	var lines []string
	for _, line := range strings.Split(pane, "\n") {
		if strings.Contains(line, goldenStart) {
			inBody = true
			continue
		}
		if strings.Contains(line, goldenEnd) {
			return lines, inBody
		}
		if inBody {
			lines = append(lines, strings.TrimRight(line, " "))
		}
	}
	return nil, false
}

// TestLabMatchesStockETOutput runs stock et for the same scripted session and
// compares it with the in-process client. 7.1.1 is compared byte for byte;
// 6.2.10's stock client only works interactively, so its rendered screen is
// compared to the expected terminal rendering of the same bytes.
func TestLabMatchesStockETOutput(t *testing.T) {
	requireLab(t)
	for _, server := range labServers() {
		t.Run(server.name, func(t *testing.T) {
			manager := labManager(t, server.password)
			spec := labSpec(t, server)
			stockKey := newLabKey(t)
			installAuthorizedKey(t, manager, spec, stockKey.authorized)
			const scriptPath = "/tmp/natcatty-stock-golden.sh"
			prepareStockET(t, manager, spec, stockKey, scriptPath)

			// The in-process client's bytes, from the same script.
			transport := openLab(t, InProcessFactory{Manager: manager}, labSpec(t, server))
			output := newOutputCollector(transport)
			disableEcho(t, transport, output)
			text := runGoldenScript(t, transport, output)
			inProcessBody, ok := betweenMarkers(text, goldenStart, goldenEnd)
			if !ok {
				t.Fatalf("in-process output lacks markers: %q", text)
			}
			inProcessBody = normalizeTerminalBytes(inProcessBody)

			if server.version == "7.1.1" {
				stockOutput := stockETCommand7(t, manager, spec, scriptPath)
				stockBody, ok := betweenMarkers(stockOutput, goldenStart, goldenEnd)
				if !ok {
					t.Fatalf("stock et output lacks markers: %q", stockOutput)
				}
				stockBody = normalizeTerminalBytes(stockBody)
				if inProcessBody != stockBody {
					t.Fatalf("in-process output differs from stock et\n got: %s\nwant: %s\n got hex: %s\nwant hex: %s",
						hex.EncodeToString([]byte(inProcessBody)), hex.EncodeToString([]byte(stockBody)),
						hex.EncodeToString([]byte(inProcessBody)), hex.EncodeToString([]byte(stockBody)))
				}
				return
			}

			pane := stockETInteractive(t, manager, spec, scriptPath)
			rendered, ok := renderedGoldenLines(pane)
			if !ok {
				t.Fatalf("stock et pane lacks markers: %q", pane)
			}
			want := []string{"red     TAB", "UTF8: caf\u00e9 \u4e16\u754c", "XR:", "BS:B"}
			if strings.Join(rendered, "\n") != strings.Join(want, "\n") {
				t.Fatalf("stock et rendered screen = %q, want %q", rendered, want)
			}
		})
	}
}

func normalizeTerminalBytes(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	return text
}
