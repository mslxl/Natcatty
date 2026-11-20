package ssh

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

const rekeyPassword = "rekey-pw"

// rekeyDecisionPrompter trusts the first key immediately and hands every later
// prompt to the test. It proves the production path can still ask the user
// after the handshake returned: a cancellation would never reach Confirm.
type rekeyDecisionPrompter struct {
	initial string

	mu      sync.Mutex
	prompts []domainconn.Key
	offered chan domainconn.Key
	answers chan appconnection.PromptDecision
}

func newRekeyDecisionPrompter(initialFingerprint string) *rekeyDecisionPrompter {
	return &rekeyDecisionPrompter{
		initial: initialFingerprint,
		offered: make(chan domainconn.Key, 4),
		answers: make(chan appconnection.PromptDecision, 4),
	}
}

func (p *rekeyDecisionPrompter) Confirm(ctx context.Context, key domainconn.Key) (appconnection.PromptDecision, error) {
	p.mu.Lock()
	p.prompts = append(p.prompts, key)
	p.mu.Unlock()
	if key.Fingerprint == p.initial {
		return appconnection.PromptDecision{Trust: true, Persist: true}, nil
	}
	select {
	case p.offered <- key:
	case <-ctx.Done():
		return appconnection.PromptDecision{}, ctx.Err()
	}
	select {
	case decision := <-p.answers:
		return decision, nil
	case <-ctx.Done():
		return appconnection.PromptDecision{}, ctx.Err()
	}
}

func (p *rekeyDecisionPrompter) prompted() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.prompts)
}

type rekeyKnownHosts struct {
	mu      sync.Mutex
	records []domainconn.KnownHost
}

func (k *rekeyKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]domainconn.KnownHost(nil), k.records...), nil
}

func (k *rekeyKnownHosts) UpsertKnownHost(_ context.Context, record domainconn.KnownHost) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.records = append(k.records, record)
	return nil
}

type rekeyPasswords struct{}

func (rekeyPasswords) Password(context.Context, model.HostID) (string, error) {
	return rekeyPassword, nil
}

func startRekeyFixture(t *testing.T) (*fakessh.Server, DialFunc) {
	t.Helper()
	return startFixture(t, fakessh.Options{
		Password:       rekeyPassword,
		RekeyThreshold: rekeyThreshold,
	})
}

func awaitPrompt(t *testing.T, prompt *rekeyDecisionPrompter) domainconn.Key {
	t.Helper()
	select {
	case key := <-prompt.offered:
		return key
	case <-time.After(5 * time.Second):
		t.Fatal("the changed host key never reached the trust prompt")
		return domainconn.Key{}
	}
}

func answerPrompt(t *testing.T, prompt *rekeyDecisionPrompter, trust bool) {
	t.Helper()
	select {
	case prompt.answers <- appconnection.PromptDecision{Trust: trust, Persist: trust}:
	case <-time.After(2 * time.Second):
		t.Fatal("the prompt answer could not be delivered")
	}
}

// acquireRekeyLease dials the fixture through the production Manager and opens
// a shell. The initial key is trusted by the prompter, so Acquire completes
// without a live user.
func acquireRekeyLease(t *testing.T, dialFunc DialFunc, prompt *rekeyDecisionPrompter,
	known *rekeyKnownHosts) (*appconnection.Lease, appconnection.Terminal) {
	t.Helper()
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(5*time.Second, 5*time.Second))
	manager := appconnection.NewManager(dialer, rekeyPasswords{}, known,
		appconnection.WithPrompter(prompt),
		appconnection.WithIdleTTL(10*time.Second),
	)
	t.Cleanup(manager.CloseAll)

	lease, err := manager.Acquire(context.Background(), appconnection.Request{
		HostID:   "rekey-host",
		Hostname: "example.com",
		Port:     22,
		Username: "root",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	terminal, err := lease.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	return lease, terminal
}

// TestRekeyWithAChangedHostKeyThroughTheManagerPrompts proves the changed-key
// prompt is reachable after the initial handshake. The fixture is dialed by the
// real Manager, rotates its host key, then forces a server-initiated rekey; the
// prompt must appear (not a cancellation), and accepting it must keep the shell
// alive. The sibling test covers rejection.
func TestRekeyWithAChangedHostKeyThroughTheManagerPrompts(t *testing.T) {
	server, dialFunc := startRekeyFixture(t)
	initial := server.Fingerprint()
	prompt := newRekeyDecisionPrompter(initial)
	lease, terminal := acquireRekeyLease(t, dialFunc, prompt, &rekeyKnownHosts{})

	server.RotateHostKey()
	changed := server.CurrentFingerprint()
	if changed == initial {
		t.Fatal("the fixture did not rotate its host key")
	}
	payload := bytes.Repeat([]byte("r"), 16*1024)
	writeErr := make(chan error, 1)
	go func() {
		_, err := terminal.Write(payload)
		writeErr <- err
	}()
	echoed := make([]byte, len(payload))
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(terminal, echoed)
		readErr <- err
	}()

	offered := awaitPrompt(t, prompt)
	if offered.Fingerprint != changed {
		t.Fatalf("prompted fingerprint = %q, want the rotated key %q", offered.Fingerprint, changed)
	}
	answerPrompt(t, prompt, true)

	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("post-rekey Write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the payload write did not finish after trusting the changed key")
	}
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("post-rekey Read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the echoed payload never arrived after trusting the changed key")
	}
	if !bytes.Equal(echoed, payload) {
		t.Fatalf("echoed payload did not match: got %d bytes", len(echoed))
	}

	if _, err := terminal.Write([]byte("ping")); err != nil {
		t.Fatalf("post-rekey Write(ping): %v", err)
	}
	ping := make([]byte, 4)
	if err := readFull(t, terminal, ping); err != nil {
		t.Fatalf("post-rekey Read(ping): %v", err)
	}
	if string(ping) != "ping" {
		t.Fatalf("post-rekey echo = %q, want ping", ping)
	}
	if !lease.Alive() {
		t.Fatal("the transport died after the user trusted the changed key")
	}
	if got := prompt.prompted(); got != 2 {
		t.Fatalf("prompts = %d, want exactly the first key and the changed key", got)
	}
}

// TestRekeyWithAChangedHostKeyRejectedFailsClosed is the rejection half: the
// same production path must offer the prompt and, when the user rejects it,
// tear the transport down.
func TestRekeyWithAChangedHostKeyRejectedFailsClosed(t *testing.T) {
	server, dialFunc := startRekeyFixture(t)
	initial := server.Fingerprint()
	prompt := newRekeyDecisionPrompter(initial)
	lease, terminal := acquireRekeyLease(t, dialFunc, prompt, &rekeyKnownHosts{})

	server.RotateHostKey()
	changed := server.CurrentFingerprint()
	if changed == initial {
		t.Fatal("the fixture did not rotate its host key")
	}
	payload := bytes.Repeat([]byte("r"), 16*1024)
	go func() { _, _ = terminal.Write(payload) }()
	failed := make(chan struct{})
	go func() {
		defer close(failed)
		buffer := make([]byte, 4096)
		for {
			if _, err := terminal.Read(buffer); err != nil {
				return
			}
		}
	}()

	offered := awaitPrompt(t, prompt)
	if offered.Fingerprint != changed {
		t.Fatalf("prompted fingerprint = %q, want the rotated key %q", offered.Fingerprint, changed)
	}
	answerPrompt(t, prompt, false)

	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("the transport kept serving after the user rejected the changed key")
	}
	// The read failure and the manager's death observation are ordered by the
	// pool's own bookkeeping, so wait for the lease to notice rather than
	// assuming the signal implies the state change has already landed.
	deadline := time.Now().Add(5 * time.Second)
	for lease.Alive() {
		if time.Now().After(deadline) {
			t.Fatal("the lease still reports a live transport after rejection")
		}
		time.Sleep(time.Millisecond)
	}
}
