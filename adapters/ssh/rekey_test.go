package ssh

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// rekeyThreshold is low enough that a test payload of a few kilobytes makes the
// server initiate a key re-exchange, and high enough that authentication
// traffic alone does not.
const rekeyThreshold = 4096

type rekeyPromptLog struct {
	mu   sync.Mutex
	keys []domainconn.Key
	fail func(domainconn.Key) error
}

func (l *rekeyPromptLog) verify(_ context.Context, key domainconn.Key) error {
	l.mu.Lock()
	l.keys = append(l.keys, key)
	fail := l.fail
	l.mu.Unlock()
	if fail != nil {
		return fail(key)
	}
	return nil
}

func (l *rekeyPromptLog) prompted() []domainconn.Key {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domainconn.Key(nil), l.keys...)
}

// TestRekeyWithTheAcceptedHostKeyPromptsOnce forces a real server-initiated
// key re-exchange after the shell is open. The rekey presents the same host
// key, so the adapter answers it from the accepted-key memory and the
// application verifier (the prompt) runs exactly once.
func TestRekeyWithTheAcceptedHostKeyPromptsOnce(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{
		Password:       "s3cret",
		RekeyThreshold: rekeyThreshold,
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(5*time.Second, 5*time.Second))

	prompts := &rekeyPromptLog{}
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "s3cret",
		VerifyHostKey: prompts.verify,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}

	payload := bytes.Repeat([]byte("r"), 16*1024)
	writeErr := make(chan error, 1)
	go func() {
		_, err := terminal.Write(payload)
		writeErr <- err
	}()
	echoed := make([]byte, len(payload))
	if err := readFull(t, terminal, echoed); err != nil {
		t.Fatalf("Read echoed payload: %v", err)
	}
	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the payload write did not finish")
	}
	if !bytes.Equal(echoed, payload) {
		t.Fatalf("echoed payload did not match: got %d bytes", len(echoed))
	}

	// Crossing the threshold is what makes the server ask for a re-exchange.
	deadline := time.Now().Add(5 * time.Second)
	for server.HostKeySignatures() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("host key signatures = %d after %d bytes; the server never rekeyed",
				server.HostKeySignatures(), len(payload))
		}
		time.Sleep(time.Millisecond)
	}

	// A post-rekey round trip proves the client finished the key exchange and
	// its host-key callback ran.
	if _, err := terminal.Write([]byte("ping")); err != nil {
		t.Fatalf("post-rekey Write: %v", err)
	}
	ping := make([]byte, 4)
	if err := readFull(t, terminal, ping); err != nil {
		t.Fatalf("post-rekey Read: %v", err)
	}
	if string(ping) != "ping" {
		t.Fatalf("post-rekey echo = %q, want ping", ping)
	}

	if got := len(prompts.prompted()); got != 1 {
		t.Fatalf("host-key prompts = %d, want 1 after a rekey with the same key", got)
	}
}

// TestRekeyWithAChangedHostKeyPromptsAndFailsClosed rotates the server's host
// key before a rekey. The changed key is not the one accepted for this
// connection, so it reaches the application verifier; a rejection tears the
// transport down.
func TestRekeyWithAChangedHostKeyPromptsAndFailsClosed(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{
		Password:       "s3cret",
		RekeyThreshold: rekeyThreshold,
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(5*time.Second, 5*time.Second))

	initialFingerprint := server.Fingerprint()
	rejection := &appconnection.Error{Kind: appconnection.KindHostKey, Cause: appconnection.ErrHostKeyRejected}
	prompts := &rekeyPromptLog{fail: func(key domainconn.Key) error {
		if key.Fingerprint == initialFingerprint {
			return nil
		}
		return rejection
	}}
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "s3cret",
		VerifyHostKey: prompts.verify,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}

	server.RotateHostKey()
	changedFingerprint := server.CurrentFingerprint()
	if changedFingerprint == initialFingerprint {
		t.Fatal("the fixture did not rotate its host key")
	}

	payload := bytes.Repeat([]byte("r"), 16*1024)
	go func() { _, _ = terminal.Write(payload) }()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buffer := make([]byte, 4096)
		for {
			if _, err := terminal.Read(buffer); err != nil {
				return
			}
		}
	}()

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the transport kept serving after a rejected rekey host key")
	}

	prompted := prompts.prompted()
	if len(prompted) < 2 {
		t.Fatalf("host-key prompts = %d, want a second prompt for the changed key", len(prompted))
	}
	last := prompted[len(prompted)-1]
	if last.Fingerprint != changedFingerprint {
		t.Fatalf("last prompted fingerprint = %q, want the rotated key %q", last.Fingerprint, changedFingerprint)
	}
}

func TestKeyExchangeGuardAcceptsOnlyTheRememberedKey(t *testing.T) {
	guard := &keyExchangeGuard{}
	first := domainconn.Key{Fingerprint: "first"}
	calls := 0
	verify := func(context.Context, domainconn.Key) error {
		calls++
		return nil
	}

	if err := guard.verifyKey(context.Background(), []byte("key-a"), verify, first); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if err := guard.verifyKey(context.Background(), []byte("key-a"), verify, first); err != nil {
		t.Fatalf("rekey verify: %v", err)
	}
	if calls != 1 {
		t.Fatalf("verifier calls = %d, want 1 for the same key", calls)
	}

	changed := domainconn.Key{Fingerprint: "changed"}
	if err := guard.verifyKey(context.Background(), []byte("key-b"), verify, changed); err != nil {
		t.Fatalf("changed verify: %v", err)
	}
	if calls != 2 {
		t.Fatalf("verifier calls = %d, want 2 for a changed key", calls)
	}
}
