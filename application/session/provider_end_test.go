package session

import (
	"context"
	"errors"
	"testing"
)

// typedProviderEnd is the classification a provider transport reports after
// its stream traps. The write and read paths must both preserve it.
func typedProviderEnd() End {
	return End{
		Reason: EndReasonError,
		ProviderFailure: &ProviderFailure{
			Status:   ProviderStatusFaulted,
			Provider: "acme.echo",
			Protocol: "x-acme-echo",
			Message:  "provider trapped",
		},
	}
}

func assertTypedProviderFailure(t *testing.T, session *Session, want End) {
	t.Helper()
	snapshot := session.Snapshot()
	if snapshot.Status != StatusExited {
		t.Fatalf("status = %q, want %q", snapshot.Status, StatusExited)
	}
	if snapshot.Connection != ConnectionStatusError {
		t.Fatalf("connection = %q, want %q", snapshot.Connection, ConnectionStatusError)
	}
	if snapshot.EndReason != want.Reason {
		t.Fatalf("end reason = %q, want %q", snapshot.EndReason, want.Reason)
	}
	if snapshot.ProviderFailure == nil {
		t.Fatal("ProviderFailure is nil; the typed provider end was lost")
	}
	if *snapshot.ProviderFailure != *want.ProviderFailure {
		t.Fatalf("ProviderFailure = %+v, want %+v",
			snapshot.ProviderFailure, want.ProviderFailure)
	}
}

func openTypedEndSession(t *testing.T, transport Transport) *Session {
	t.Helper()
	service := reconnectTestService(&staticFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{
		Kind:     KindPlugin,
		Protocol: "x-acme-echo",
		Provider: "acme.echo",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestSessionWriteFailureKeepsTheTypedProviderEnd covers a provider that dies
// while the Session is writing to it. The write path and the read path race to
// report the end; whichever wins must ask the EndReporter instead of assuming
// a bare transport error.
func TestSessionWriteFailureKeepsTheTypedProviderEnd(t *testing.T) {
	want := typedProviderEnd()
	transport := newScriptedTransport()
	// The read side stays open: only a write failure may end this Session, so
	// the assertion cannot accidentally pass through the read path.
	transport.setEnd(want)
	transport.failWrite(errors.New("provider stream closed"))
	session := openTypedEndSession(t, transport)

	if _, err := session.TerminalConn().Write([]byte("input\n")); err != nil {
		t.Fatalf("TerminalConn().Write: %v", err)
	}
	waitForDone(t, session)
	assertTypedProviderFailure(t, session, want)
}

// TestSessionReadFailureKeepsTheTypedProviderEnd is the read-path control: an
// EndReporter that has already classified a provider failure must surface that
// failure when Read returns.
func TestSessionReadFailureKeepsTheTypedProviderEnd(t *testing.T) {
	want := typedProviderEnd()
	transport := newScriptedTransport()
	transport.setEnd(want)
	// fail records the end and closes the read side, so Read reports io.EOF
	// and the read loop classifies it through endOf.
	transport.fail(want)
	session := openTypedEndSession(t, transport)

	waitForDone(t, session)
	assertTypedProviderFailure(t, session, want)
}
