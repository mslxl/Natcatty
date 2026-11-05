// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
)

const testResumeKey = "resume-abcdEFGH12345678"

type fakeSessionStore struct {
	mu      sync.Mutex
	records map[string]SessionRecord
	loadErr error
	saveErr error
	deletes int
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{records: make(map[string]SessionRecord)}
}

func (s *fakeSessionStore) Save(_ context.Context, resumeKey string, record SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.records[resumeKey] = record
	return nil
}

func (s *fakeSessionStore) Load(_ context.Context, resumeKey string) (SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return SessionRecord{}, s.loadErr
	}
	record, ok := s.records[resumeKey]
	if !ok {
		return SessionRecord{}, appvault.ErrSecretNotFound
	}
	return record, nil
}

func (s *fakeSessionStore) Delete(_ context.Context, resumeKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	delete(s.records, resumeKey)
	return nil
}

func (s *fakeSessionStore) record(resumeKey string) (SessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[resumeKey]
	return record, ok
}

func testForwarder(network *etservertest.Network) *recordingForwarder {
	return &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
		return network.DialContext(context.Background(), "tcp", "127.0.0.1:2022")
	}}
}

func resumeSpec() appsession.StartSpec {
	spec := inProcessStartSpec()
	spec.ResumeKey = testResumeKey
	return spec
}

func fakeServer(t *testing.T) (*etservertest.Server, *etservertest.Network) {
	t.Helper()
	server := etservertest.NewServer(testID, testPasskey)
	network := etservertest.NewNetwork(server)
	t.Cleanup(network.Close)
	return server, network
}

func terminalBuffer(t *testing.T, text string) protocol.Packet {
	t.Helper()
	return protocol.Packet{
		Header: protocol.HeaderTerminalBuffer,
		Payload: mustMarshal(t, &protocol.TerminalBuffer{}, func(m *protocol.TerminalBuffer) {
			m.SetBuffer([]byte(text))
		}),
	}
}

// transportOutput is one continuously reading consumer. A blocked Read cannot
// be cancelled, so a test that abandons a reader can steal the output a later
// read waits for; this reader owns the stream for the whole assertion.
type transportOutput struct {
	reads chan transportRead
}

type transportRead struct {
	data []byte
	err  error
}

func watchTransport(transport appsession.Transport) *transportOutput {
	output := &transportOutput{reads: make(chan transportRead, 8)}
	go func() {
		defer close(output.reads)
		buffer := make([]byte, 1024)
		for {
			n, err := transport.Read(buffer)
			if n > 0 {
				output.reads <- transportRead{data: append([]byte(nil), buffer[:n]...)}
			}
			if err != nil {
				output.reads <- transportRead{err: err}
				return
			}
		}
	}()
	return output
}

// expectNone fails if any output or an end arrives within wait, and otherwise
// leaves the reader running for expect.
func (o *transportOutput) expectNone(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case got := <-o.reads:
		if got.err != nil {
			t.Fatalf("transport ended before any expected output: %v", got.err)
		}
		t.Fatalf("transport replayed %q; a reset reattach starts blank", got.data)
	case <-time.After(wait):
	}
}

func (o *transportOutput) expect(t *testing.T, want string) {
	t.Helper()
	var text strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got, ok := <-o.reads:
			if !ok {
				t.Fatalf("transport ended after %q, want %q", text.String(), want)
			}
			if got.err != nil {
				t.Fatalf("read %q (err %v), want %q", text.String(), got.err, want)
			}
			text.Write(got.data)
			if strings.Contains(text.String(), want) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out after %q waiting for %q", text.String(), want)
		}
	}
}

// TestInProcessPersistsCredentialsAndClearsOnExplicitClose is the secret
// lifetime contract: a new ET session writes its id and passkey to the store,
// neither reaches the logs, and a user close removes both.
func TestInProcessPersistsCredentialsAndClearsOnExplicitClose(t *testing.T) {
	server, network := fakeServer(t)
	store := newFakeSessionStore()
	var logs bytes.Buffer
	lease := newFakeLease(credentialsExec(), testForwarder(network))
	transport := openFakeSpec(t, InProcessFactory{Sessions: store, Logger: testLogger(&logs)},
		lease, server, nil, resumeSpec())

	record, ok := store.record(testResumeKey)
	if !ok {
		t.Fatal("Open did not save the ET session credentials")
	}
	if record.ID != testID || record.Passkey != testPasskey {
		t.Fatalf("stored record = %+v, want the bootstrapped id and passkey", record)
	}
	if strings.Contains(logs.String(), testID) || strings.Contains(logs.String(), testPasskey) {
		t.Fatal("ET session credentials appeared in logs")
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := store.record(testResumeKey); ok {
		t.Fatal("an explicit close left the ET session credentials in the store")
	}
	if store.deletes == 0 {
		t.Fatal("an explicit close did not delete the saved credentials")
	}
}

// TestLegacyServerKeepsSessionMemoryOnly pins capability-aware persistence:
// a pre-v7 server cannot authenticate a reattach, so writing its pair to the
// secret store would only produce a permanent ErrReattachUnsupported after the
// next restart (and could never restore the shell). The live session still
// works and reconnects in-process; only persistence is skipped.
func TestLegacyServerKeepsSessionMemoryOnly(t *testing.T) {
	server, network := fakeServer(t)
	server.LegacyHandshake(true)
	store := newFakeSessionStore()
	lease := newFakeLease(credentialsExec(), testForwarder(network))
	transport := openFakeSpec(t, InProcessFactory{Sessions: store},
		lease, server, nil, resumeSpec())
	t.Cleanup(func() { _ = transport.Close() })

	if _, ok := store.record(testResumeKey); ok {
		t.Fatal("a pre-v7 server's session was persisted; a restart could never reattach it")
	}
	if command, _, _ := lease.state(); !strings.Contains(command, "etterminal") {
		t.Fatalf("the fresh legacy session did not run the etterminal bootstrap: %q", command)
	}
}

// TestLegacyReattachIsRefusedAndPairKept keeps stock's explicit refusal for a
// pair an older build saved on a legacy server. The adapter must not silently
// bootstrap a second shell in the same Open, and must not clear the pair:
// clearing it is the shell's explicit fallback decision.
func TestLegacyReattachIsRefusedAndPairKept(t *testing.T) {
	server, network := fakeServer(t)
	server.LegacyHandshake(true)
	store := newFakeSessionStore()
	store.records[testResumeKey] = SessionRecord{ID: testID, Passkey: testPasskey}
	lease := newFakeLease(credentialsExec(), testForwarder(network))
	factory := InProcessFactory{Sessions: store}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, resumeSpec())
	if !errors.Is(err, etmodel.ErrReattachUnsupported) {
		t.Fatalf("legacy reattach = %v, want ErrReattachUnsupported", err)
	}
	if command, _, _ := lease.state(); command != "" {
		t.Fatalf("a refused legacy reattach started a second shell: %q", command)
	}
	if _, ok := store.record(testResumeKey); !ok {
		t.Fatal("a refused legacy reattach cleared the saved pair instead of leaving it for the shell")
	}
}

// TestPreserveResumeKeepsCredentialsAcrossClose covers the reconnect and
// app-shutdown handover: the Session service marks the transport before
// closing, and the pair stays available for the next Open.
func TestPreserveResumeKeepsCredentialsAcrossClose(t *testing.T) {
	server, network := fakeServer(t)
	store := newFakeSessionStore()
	lease := newFakeLease(credentialsExec(), testForwarder(network))
	transport := openFakeSpec(t, InProcessFactory{Sessions: store},
		lease, server, nil, resumeSpec())

	preserver, ok := transport.(appsession.ResumePreserver)
	if !ok {
		t.Fatal("in-process transport does not implement ResumePreserver")
	}
	preserver.PreserveResume()
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := store.record(testResumeKey); !ok {
		t.Fatal("PreserveResume did not keep the credentials across Close")
	}
	if store.deletes != 0 {
		t.Fatalf("PreserveResume close deleted credentials %d times", store.deletes)
	}

	// A later explicit close (for example while a reconnect is pending) must
	// still clear the pair even though the transport already closed.
	discarder, ok := transport.(appsession.ResumeDiscarder)
	if !ok {
		t.Fatal("in-process transport does not implement ResumeDiscarder")
	}
	discarder.DiscardResume()
	if _, ok := store.record(testResumeKey); ok {
		t.Fatal("DiscardResume did not clear a preserved credential pair")
	}
}

// TestFreshProcessReattachesBlankAndSkipsBootstrap is the app-restart path: a
// second factory, a new SSH lease, and the same secret store attach the still
// running remote shell by reset. The new process skips etterminal, receives no
// replayed output, and terminal I/O continues under the new key.
func TestFreshProcessReattachesBlankAndSkipsBootstrap(t *testing.T) {
	server, network := fakeServer(t)
	store := newFakeSessionStore()

	first := newFakeLease(credentialsExec(), testForwarder(network))
	transport := openFakeSpec(t, InProcessFactory{Sessions: store},
		first, server, nil, resumeSpec())
	_ = recvPacket(t, server) // TERMINAL_INFO from the first process
	if err := server.Send(t.Context(), terminalBuffer(t, "before the restart")); err != nil {
		t.Fatalf("Send before: %v", err)
	}
	readTransport(t, transport, "before the restart")
	transport.(appsession.ResumePreserver).PreserveResume()
	if err := transport.Close(); err != nil {
		t.Fatalf("close the first process: %v", err)
	}
	if _, ok := store.record(testResumeKey); !ok {
		t.Fatal("the saved credentials did not survive the first process")
	}

	second := newFakeLease(nil, testForwarder(network))
	second.execErr = errors.New("bootstrap must not run during reattach")
	factory := InProcessFactory{Sessions: store}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return second, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	attached, err := factory.Open(ctx, resumeSpec())
	if err != nil {
		t.Fatalf("reattach Open: %v", err)
	}
	t.Cleanup(func() { _ = attached.Close() })

	command, _, _ := second.state()
	if command != "" {
		t.Fatalf("reattach ran the etterminal bootstrap: %q", command)
	}
	packet := recvPacket(t, server)
	if packet.Header != protocol.HeaderTerminalInfo {
		t.Fatalf("first packet after reattach = %v, want TERMINAL_INFO without INITIAL_PAYLOAD", packet.Header)
	}
	output := watchTransport(attached)
	output.expectNone(t, 150*time.Millisecond)

	if err := server.Send(t.Context(), terminalBuffer(t, "after the restart")); err != nil {
		t.Fatalf("Send after: %v", err)
	}
	output.expect(t, "after the restart")
	if _, ok := store.record(testResumeKey); !ok {
		t.Fatal("reattaching consumed the saved credentials")
	}

	if err := attached.Close(); err != nil {
		t.Fatalf("Close reattached transport: %v", err)
	}
	if _, ok := store.record(testResumeKey); ok {
		t.Fatal("an explicit close of the reattached session left its credentials")
	}
}

// TestUnavailableSavedSessionFallsBackToFreshSession covers a shell that
// exited while the app was closed: the saved id is rejected, the adapter clears
// it, and a fresh etterminal session is opened and saved under the same resume
// key.
func TestUnavailableSavedSessionFallsBackToFreshSession(t *testing.T) {
	server, network := fakeServer(t)
	store := newFakeSessionStore()
	store.records[testResumeKey] = SessionRecord{ID: "WRONGclient00000", Passkey: testPasskey}

	lease := newFakeLease(credentialsExec(), testForwarder(network))
	initial := make(chan protocol.Packet, 1)
	transport := openFakeSpec(t, InProcessFactory{Sessions: store},
		lease, server, initial, resumeSpec())
	t.Cleanup(func() { _ = transport.Close() })

	command, _, _ := lease.state()
	if !strings.Contains(command, "etterminal") {
		t.Fatalf("fallback did not bootstrap a fresh session: command %q", command)
	}
	if packet := <-initial; packet.Header != protocol.HeaderInitialPayload {
		t.Fatalf("fresh session first packet = %v, want INITIAL_PAYLOAD", packet.Header)
	}
	record, ok := store.record(testResumeKey)
	if !ok || record.ID != testID || record.Passkey != testPasskey {
		t.Fatalf("fallback record = %+v present=%v, want the fresh credentials", record, ok)
	}
	if store.deletes == 0 {
		t.Fatal("the unusable saved session was not cleared before the fresh bootstrap")
	}
}

// TestTransientReattachFailureKeepsSavedCredentials: a network outage while
// reattaching must not destroy the pair, or the next retry could not resume.
func TestTransientReattachFailureKeepsSavedCredentials(t *testing.T) {
	server, network := fakeServer(t)
	store := newFakeSessionStore()
	first := newFakeLease(credentialsExec(), testForwarder(network))
	transport := openFakeSpec(t, InProcessFactory{Sessions: store},
		first, server, nil, resumeSpec())
	_ = recvPacket(t, server)
	transport.(appsession.ResumePreserver).PreserveResume()
	if err := transport.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}

	network.SetRefuse(true)
	second := newFakeLease(nil, testForwarder(network))
	factory := InProcessFactory{Sessions: store}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return second, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, resumeSpec())
	if !errors.Is(err, etmodel.ErrHandshakeFailed) {
		t.Fatalf("transient reattach error = %v, want ErrHandshakeFailed", err)
	}
	if _, ok := store.record(testResumeKey); !ok {
		t.Fatal("a transient reattach failure deleted the saved credentials")
	}
	if store.deletes != 0 {
		t.Fatalf("a transient reattach failure deleted credentials %d times", store.deletes)
	}
}

// TestReplayWindowExceededIsATypedUnavailableSession drives writeRecover's
// window check through Open: the server accepts the saved session with a v7
// challenge but claims to have received client packets the client never sent.
// The session cannot continue, and Open must report the domain's typed
// unavailable-session error, not a generic handshake failure.
func TestReplayWindowExceededIsATypedUnavailableSession(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		var request protocol.ConnectRequest
		if err := wire.ReadMessage(conn, &request); err != nil {
			serverErr <- err
			return
		}
		challenge := bytes.Repeat([]byte{0x24}, 32)
		challengeResponse := &protocol.ConnectResponse{}
		challengeResponse.SetAuthChallenge(challenge)
		if err := wire.WriteMessage(conn, challengeResponse); err != nil {
			serverErr <- err
			return
		}
		var auth protocol.ConnectAuth
		if err := wire.ReadMessage(conn, &auth); err != nil {
			serverErr <- err
			return
		}
		status := protocol.ConnectStatus_RETURNING_CLIENT
		proof, err := etcp.ResetDecisionProof([]byte(testPasskey), testID,
			protocol.Version, challenge, status, false, nil)
		if err != nil {
			serverErr <- err
			return
		}
		final := &protocol.ConnectResponse{}
		final.SetStatus(status)
		final.SetResetRequired(false)
		final.SetResetProof(proof)
		if err := wire.WriteMessage(conn, final); err != nil {
			serverErr <- err
			return
		}
		// Normal recovery, not reset: the server's receive position is ahead
		// of the client's empty replay ring.
		sequence := &protocol.SequenceHeader{}
		sequence.SetSequenceNumber(5)
		if err := wire.WriteMessage(conn, sequence); err != nil {
			serverErr <- err
			return
		}
		var clientSequence protocol.SequenceHeader
		if err := wire.ReadMessage(conn, &clientSequence); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	store := newFakeSessionStore()
	store.records[testResumeKey] = SessionRecord{ID: testID, Passkey: testPasskey}
	lease := newFakeLease(credentialsExec(), &recordingForwarder{
		dial: func(context.Context) (net.Conn, error) {
			return net.Dial("tcp", listener.Addr().String())
		},
	})
	factory := InProcessFactory{Sessions: store}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = factory.Open(ctx, resumeSpec())
	if !errors.Is(err, etmodel.ErrSessionUnavailable) {
		t.Fatalf("Open error = %v, want ErrSessionUnavailable", err)
	}
	if !errors.Is(err, etcp.ErrReplayExceeded) {
		t.Fatalf("Open error = %v, want the underlying ErrReplayExceeded", err)
	}
	if serverFailure := <-serverErr; serverFailure != nil {
		t.Fatalf("fake server: %v", serverFailure)
	}
	if _, ok := store.record(testResumeKey); !ok {
		t.Fatal("a replay-window failure deleted the saved credentials")
	}
}

// TestSavedSessionReadFailureIsTyped: if the secret store cannot be read, the
// adapter must not silently start a second remote shell.
func TestSavedSessionReadFailureIsTyped(t *testing.T) {
	_, network := fakeServer(t)
	store := newFakeSessionStore()
	store.loadErr = errors.New("secret store is unavailable")
	lease := newFakeLease(credentialsExec(), testForwarder(network))
	factory := InProcessFactory{Sessions: store}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, resumeSpec())
	if !errors.Is(err, etmodel.ErrSessionUnavailable) {
		t.Fatalf("Open error = %v, want ErrSessionUnavailable", err)
	}
	if strings.Contains(err.Error(), testID) || strings.Contains(err.Error(), testPasskey) {
		t.Fatalf("saved-session error leaked credentials: %v", err)
	}
	if command, _, _ := lease.state(); command != "" {
		t.Fatalf("an unreadable saved session still ran bootstrap: %q", command)
	}
}
