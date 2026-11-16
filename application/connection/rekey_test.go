package connection

import (
	"context"
	"testing"

	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// TestRekeyWithAnAcceptedKeyDoesNotPromptAgain covers the manager half of the
// rekey fix: the Known Host snapshot is read once per dial, so a key trusted
// earlier in the same Connection must be answered from the accepted-key memory.
// A changed key is not remembered and therefore still prompts and can fail
// closed.
func TestRekeyWithAnAcceptedKeyDoesNotPromptAgain(t *testing.T) {
	manager, _, dialer, known, prompter := newTestManager(t)
	dialer.verify = true
	key := domainconn.Key{
		Hostname:    "example.com",
		Port:        22,
		KeyType:     "ssh-ed25519",
		Fingerprint: "SHA256:first",
		PublicKey:   "ssh-ed25519 AAAAfirst",
	}
	dialer.key = key

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	verify := dialer.request().VerifyHostKey
	if verify == nil {
		t.Fatal("the dial request carries no host-key verifier")
	}
	if got := len(prompter.prompted()); got != 1 {
		t.Fatalf("prompts after the first key exchange = %d, want 1", got)
	}

	// The server rekeys and presents the same key. The persisted record is not
	// in the snapshot captured at Acquire, so only the in-Connection memory can
	// prevent a second prompt.
	if err := verify(context.Background(), key); err != nil {
		t.Fatalf("rekey with the accepted key: %v", err)
	}
	if got := len(prompter.prompted()); got != 1 {
		t.Fatalf("prompts after a same-key rekey = %d, want 1", got)
	}
	if len(known.upserts) != 1 {
		t.Fatalf("known-host upserts = %d, want 1", len(known.upserts))
	}

	changed := key
	changed.Fingerprint = "SHA256:changed"
	changed.PublicKey = "ssh-ed25519 AAAAchanged"
	prompter.decision = PromptDecision{Trust: false}
	if err := verify(context.Background(), changed); err == nil {
		t.Fatal("a changed host key on rekey was accepted, want a failure")
	}
	if got := len(prompter.prompted()); got != 2 {
		t.Fatalf("prompts after a changed-key rekey = %d, want 2", got)
	}
}
