package phoneshell

import (
	"context"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeCredentialService simulates the field-secret service: an outage at
// launch and a recovery the foreground transition can observe.
type fakeCredentialService struct {
	mu        sync.Mutex
	available bool
	reason    string
	next      bool
	retries   int
}

func (f *fakeCredentialService) Status() (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available, f.reason
}

func (f *fakeCredentialService) Retry(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retries++
	f.available = f.next
	if f.available {
		f.reason = ""
	}
	return f.available
}

func (f *fakeCredentialService) recover() {
	f.mu.Lock()
	f.next = true
	f.mu.Unlock()
}

func (f *fakeCredentialService) retryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retries
}

// TestVaultHomeShowsCredentialOutageUntilForegroundRetry drives the Vault home
// across a credential outage: the reason is visible while the sections remain
// usable, and a successful foreground retry clears it.
func TestVaultHomeShowsCredentialOutageUntilForegroundRetry(t *testing.T) {
	const reason = "The system credential store is unavailable. Vault Sync is paused."
	credentials := &fakeCredentialService{reason: reason}
	shell := New(WithCredentialService(credentials))
	home := ui.NewTester(shell.View(Vault), 390, 844)

	if !home.HasText(reason) {
		t.Fatalf("the Vault home does not state the credential outage; texts: %v", home.Texts())
	}
	// Non-secret navigation still works while credentials are unavailable.
	for _, section := range []string{"Hosts", "Keychain", "Notes", "Known hosts"} {
		if !home.HasText(section) {
			t.Fatalf("the Vault home hides %q during the outage; texts: %v", section, home.Texts())
		}
	}

	// The outage persists until a retry actually succeeds.
	shell.Foreground()
	home.Frame()
	if !home.HasText(reason) {
		t.Fatalf("a failed retry cleared the notice; texts: %v", home.Texts())
	}

	// Returning to the foreground after the service recovers retries and
	// clears the notice without a reload.
	credentials.recover()
	shell.Foreground()
	home.Frame()
	if home.HasText(reason) {
		t.Fatalf("the notice survived a successful foreground retry; texts: %v", home.Texts())
	}
	if got := credentials.retryCount(); got != 2 {
		t.Fatalf("credential retry count = %d, want one per failed and one per successful foreground", got)
	}
}

// TestVaultHomeHasNoCredentialNoticeWhenAvailable keeps the notice bound to
// the outage rather than to every Vault launch.
func TestVaultHomeHasNoCredentialNoticeWhenAvailable(t *testing.T) {
	credentials := &fakeCredentialService{available: true}
	shell := New(WithCredentialService(credentials))
	home := ui.NewTester(shell.View(Vault), 390, 844)

	if home.HasText("credential store is unavailable") {
		t.Fatalf("an available credential service still shows the outage notice; texts: %v", home.Texts())
	}
	shell.Foreground()
	if got := credentials.retryCount(); got != 0 {
		t.Fatalf("Foreground retried an available credential service %d time(s)", got)
	}
}
