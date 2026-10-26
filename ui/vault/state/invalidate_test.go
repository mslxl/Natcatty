package state

import (
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestInvalidateListsReloadsOnTheNextFlush covers the off-thread notification
// the sync service uses after applying a downloaded snapshot: InvalidateLists
// is safe from any goroutine and touches nothing; the view's FlushLists
// applies the change on the UI thread. An open editor keeps its unsaved
// buffer.
func TestInvalidateListsReloadsOnTheNextFlush(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "host-1", Label: "gui-alpha"}}}
	state, redraws := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit did not open the editor")
	}
	state.Form().Label = "unsaved-buffer"
	initialRedraws := *redraws

	// The remote snapshot lands while the editor is open.
	repo.items = []domainvault.Host{{ID: "host-cloud", Label: "cloud-beta"}}
	done := make(chan struct{})
	go func() {
		state.InvalidateLists()
		close(done)
	}()
	<-done

	if got := state.Items()[0].Label; got != "gui-alpha" {
		t.Fatalf("InvalidateLists mutated the UI-thread list directly: %q", got)
	}
	if *redraws != initialRedraws+1 {
		t.Fatalf("redraws = %d, want the invalidation to request one", *redraws)
	}

	state.FlushLists()
	if got := state.Items()[0].Label; got != "cloud-beta" {
		t.Fatalf("list after FlushLists = %q, want cloud-beta", got)
	}
	if !state.Editing() || state.Form().Label != "unsaved-buffer" {
		t.Fatalf("open editor lost its buffer: editing=%v label=%q",
			state.Editing(), state.Form().Label)
	}

	// A second FlushLists without a new invalidation must not reload: the list
	// change behind it is not an external Vault change.
	repo.items = []domainvault.Host{{ID: "host-3", Label: "gamma"}}
	state.FlushLists()
	if got := state.Items()[0].Label; got != "cloud-beta" {
		t.Fatalf("FlushLists reloaded without an invalidation: %q", got)
	}
}
