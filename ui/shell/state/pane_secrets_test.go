package state

import (
	"strings"
	"testing"
)

// TestPaneSecretsRoundTrip proves the store seals a credential, opens it for
// the same Pane, and never confuses one Pane's entry with another's.
func TestPaneSecretsRoundTrip(t *testing.T) {
	store, err := newPaneSecrets()
	if err != nil {
		t.Fatalf("newPaneSecrets: %v", err)
	}
	if err := store.set("pane-a", "s3cret"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := store.set("pane-b", "other"); err != nil {
		t.Fatalf("set: %v", err)
	}
	value, ok := store.get("pane-a")
	if !ok || value != "s3cret" {
		t.Fatalf("get(pane-a) = %q, %v; want the stored credential", value, ok)
	}
	value, ok = store.get("pane-b")
	if !ok || value != "other" {
		t.Fatalf("get(pane-b) = %q, %v; want the stored credential", value, ok)
	}
	if _, ok := store.get("pane-c"); ok {
		t.Fatal("an unknown Pane reported a credential")
	}

	// The memory representation never carries plaintext.
	for paneID, sealed := range store.values {
		if strings.Contains(string(sealed), "s3cret") || strings.Contains(string(sealed), "other") {
			t.Fatalf("the sealed value for %s contains plaintext", paneID)
		}
	}
}

// TestPaneSecretsDropAndWipe proves a drop removes exactly one Pane's entry
// and a wipe empties the store.
func TestPaneSecretsDropAndWipe(t *testing.T) {
	store, err := newPaneSecrets()
	if err != nil {
		t.Fatalf("newPaneSecrets: %v", err)
	}
	_ = store.set("pane-a", "s3cret")
	_ = store.set("pane-b", "other")
	store.drop("pane-a")
	if _, ok := store.get("pane-a"); ok {
		t.Fatal("a dropped Pane still reported a credential")
	}
	if _, ok := store.get("pane-b"); !ok {
		t.Fatal("dropping one Pane lost another Pane's credential")
	}

	store.wipe()
	if _, ok := store.get("pane-b"); ok {
		t.Fatal("a wiped store still reported a credential")
	}
	if len(store.values) != 0 {
		t.Fatalf("wiped values = %d entries, want none", len(store.values))
	}
}

// TestPaneSecretsEmptyValueStoresNothing proves a Quick Connect without a
// password never holds an entry.
func TestPaneSecretsEmptyValueStoresNothing(t *testing.T) {
	store, err := newPaneSecrets()
	if err != nil {
		t.Fatalf("newPaneSecrets: %v", err)
	}
	if err := store.set("pane-a", ""); err != nil {
		t.Fatalf("set empty: %v", err)
	}
	if _, ok := store.get("pane-a"); ok {
		t.Fatal("an empty password stored an entry")
	}
}
