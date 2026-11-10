package wasm

import "testing"

// TestLanguageBridgeRejectsMismatchedAndDuplicateResponses covers the bridge's
// closed one-response rule directly: a response for another request id, a
// second response in the same call, and a response outside a call are all
// dropped instead of being delivered to the wrong caller.
func TestLanguageBridgeRejectsMismatchedAndDuplicateResponses(t *testing.T) {
	var bridge languageBridge

	// A response outside a call is dropped.
	if bridge.respond(1, []byte("late")) {
		t.Fatal("the bridge accepted a response outside a call")
	}
	if _, ok := bridge.take(1); ok {
		t.Fatal("the bridge returned a response outside a call")
	}

	bridge.begin(7)
	if bridge.respond(8, []byte("wrong request")) {
		t.Fatal("the bridge accepted a response for a different request id")
	}
	if !bridge.respond(7, []byte("first")) {
		t.Fatal("the bridge rejected the matching response")
	}
	if bridge.respond(7, []byte("second")) {
		t.Fatal("the bridge accepted a duplicate response")
	}
	payload, ok := bridge.take(7)
	if !ok || string(payload) != "first" {
		t.Fatalf("take(7) = %q, %v", payload, ok)
	}
	if _, ok := bridge.take(8); ok {
		t.Fatal("take accepted a different request id")
	}
	bridge.end()

	// begin resets the duplicate guard for the next call.
	bridge.begin(9)
	if bridge.respond(9, []byte("next")) == false {
		t.Fatal("the bridge rejected the first response of a new call")
	}
	if _, ok := bridge.take(10); ok {
		t.Fatal("take accepted a different request id in a new call")
	}
	bridge.end()
	if bridge.respond(9, []byte("after end")) {
		t.Fatal("the bridge accepted a response after the call ended")
	}
}
