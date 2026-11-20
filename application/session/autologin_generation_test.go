package session

import (
	"testing"

	"github.com/mslxl/natcatty/application/session/autologin"
)

// TestAutoLoginGenerationGuardsRejectAReplacedTransport pins the generation
// checks in feedAutoLogin and writeAutoLogin. A reconnect replaces the detector
// and its transport; a late read from the abandoned generation must not feed
// the replacement, and a late answer bound to the abandoned generation must not
// write to it.
func TestAutoLoginGenerationGuardsRejectAReplacedTransport(t *testing.T) {
	oldTransport := newScriptedTransport()
	newTransport := newScriptedTransport()
	session := &Session{spec: StartSpec{
		Kind:      KindSerial,
		AutoLogin: &autologin.Options{Username: "admin", Password: "secret"},
	}}
	session.armAutoLogin(1, oldTransport)
	session.armAutoLogin(2, newTransport)

	// A read tagged with the abandoned generation matches the replacement
	// engine by pointer but must be rejected by the generation check.
	session.feedAutoLogin(1, "login: ")
	if got := oldTransport.input(); got != "" {
		t.Fatalf("the stale feed answered on the old transport: %q", got)
	}
	if got := newTransport.input(); got != "" {
		t.Fatalf("the stale feed answered on the replacement transport: %q", got)
	}

	// An answer bound to the abandoned generation must not reach a live
	// transport, even when the transport pointer is the current one.
	session.writeAutoLogin(1, newTransport, []byte("stale\r"))
	if got := newTransport.input(); got != "" {
		t.Fatalf("a stale answer reached the replacement transport: %q", got)
	}

	// The live generation still answers normally.
	session.feedAutoLogin(2, "login: ")
	if got := newTransport.input(); got != "admin\r" {
		t.Fatalf("the current generation wrote %q, want the username", got)
	}
	if got := oldTransport.input(); got != "" {
		t.Fatalf("the current generation wrote to the old transport: %q", got)
	}
}
