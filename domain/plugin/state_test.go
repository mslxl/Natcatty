package plugin

import "testing"

func TestFiberID(t *testing.T) {
	id := NewFiberID("dev", "acme.hello", 2)
	if got, want := string(id), "dev/acme.hello#2"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
	if got := id.Group(); got != "dev" {
		t.Fatalf("group = %q", got)
	}
	if got := id.PluginID(); got != "acme.hello" {
		t.Fatalf("plugin = %q", got)
	}
}

func TestStateValid(t *testing.T) {
	for _, state := range []State{
		StatePending, StateLoading, StateActive, StateUnloading, StateDisposed, StateFailed,
	} {
		if !state.Valid() {
			t.Fatalf("%q is not valid", state)
		}
	}
	if State("BROKEN").Valid() {
		t.Fatal("an unknown state is valid")
	}
}
