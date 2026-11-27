package session

import (
	"strings"
	"testing"
)

// TestMemCredentialStoreRoundTrip proves the store seals a credential, opens
// it for the same owner, and never confuses one owner's entry with another's.
func TestMemCredentialStoreRoundTrip(t *testing.T) {
	store, err := NewMemCredentialStore()
	if err != nil {
		t.Fatalf("NewMemCredentialStore: %v", err)
	}
	if err := store.Set("pane-a", "s3cret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set("pane-b", "other"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok := store.Get("pane-a")
	if !ok || value != "s3cret" {
		t.Fatalf("Get(pane-a) = %q, %v; want the stored credential", value, ok)
	}
	value, ok = store.Get("pane-b")
	if !ok || value != "other" {
		t.Fatalf("Get(pane-b) = %q, %v; want the stored credential", value, ok)
	}
	if _, ok := store.Get("pane-c"); ok {
		t.Fatal("an unknown owner reported a credential")
	}

	// The memory representation never carries plaintext.
	for owner, sealed := range store.values {
		if strings.Contains(string(sealed), "s3cret") || strings.Contains(string(sealed), "other") {
			t.Fatalf("the sealed value for %s contains plaintext", owner)
		}
	}
}

// TestMemCredentialStoreDropAndWipe proves a drop removes exactly one owner's
// entry and a wipe empties the store.
func TestMemCredentialStoreDropAndWipe(t *testing.T) {
	store, err := NewMemCredentialStore()
	if err != nil {
		t.Fatalf("NewMemCredentialStore: %v", err)
	}
	_ = store.Set("pane-a", "s3cret")
	_ = store.Set("pane-b", "other")
	store.Drop("pane-a")
	if _, ok := store.Get("pane-a"); ok {
		t.Fatal("a dropped owner still reported a credential")
	}
	if _, ok := store.Get("pane-b"); !ok {
		t.Fatal("dropping one owner lost another owner's credential")
	}

	store.Wipe()
	if _, ok := store.Get("pane-b"); ok {
		t.Fatal("a wiped store still reported a credential")
	}
	if len(store.values) != 0 {
		t.Fatalf("wiped values = %d entries, want none", len(store.values))
	}
}

// TestMemCredentialStoreEmptyValueStoresNothing proves a Quick Connect without
// a password never holds an entry.
func TestMemCredentialStoreEmptyValueStoresNothing(t *testing.T) {
	store, err := NewMemCredentialStore()
	if err != nil {
		t.Fatalf("NewMemCredentialStore: %v", err)
	}
	if err := store.Set("pane-a", ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	if _, ok := store.Get("pane-a"); ok {
		t.Fatal("an empty password stored an entry")
	}
}
