// SPDX-License-Identifier: Apache-2.0
package etcp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
)

// The v7 proof vectors are BLAKE2b-256 keyed with testKey over the exact
// messages upstream CryptoHandler builds (src/base/CryptoHandler.cpp at
// et-v7.1.1), generated independently with Python hashlib for client
// testID="XXXtestclient001", version 6, challenge 00..1f, and salt 20..3f.
const (
	challengeHex      = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	saltHex           = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	connectProofHex   = "744245dafd548832df0810445f7758d4183c814cdd4f3c8b4e8c807fb651fdce"
	attachProofHex    = "3da684f8312548aab9667e697e406d6096503140f414c81fc3c1b7c1b582f4bd"
	newDecisionHex    = "f650cb270c64e5f6067a1adbde29effb00731cd28a133c6e54b2538d5dd88d3c"
	attachDecisionHex = "919db5de97eb5e65320617f0ce1d753261ba85508d5f4a9585c3dc66f1c2f9e9"
)

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("bad hex %q: %v", value, err)
	}
	return decoded
}

// TestDialChallengeAuthentication completes the v7 three-message handshake:
// the client advertises the capability, proves possession of the passkey over
// the server's fresh challenge, and verifies the server's reset-decision
// proof before accepting NEW_CLIENT.
func TestDialChallengeAuthentication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		challenge := mustHex(t, challengeHex)
		wantProof := mustHex(t, connectProofHex)
		wantDecision := mustHex(t, newDecisionHex)
		s := &scripted{handle: func(i int, c *rawServer) {
			if i != 0 {
				return
			}
			var req protocol.ConnectRequest
			if wire.ReadMessage(c.br, &req) != nil {
				t.Errorf("read ConnectRequest: %v", &req)
				return
			}
			if !req.GetSupportsChallenge() || req.GetResetIntent() {
				t.Errorf("ConnectRequest supportsChallenge=%v resetIntent=%v, want true/false",
					req.GetSupportsChallenge(), req.GetResetIntent())
			}
			first := &protocol.ConnectResponse{}
			first.SetAuthChallenge(challenge)
			if wire.WriteMessage(c.conn, first) != nil {
				return
			}
			var auth protocol.ConnectAuth
			if wire.ReadMessage(c.br, &auth) != nil {
				return
			}
			if !bytes.Equal(auth.GetProof(), wantProof) {
				t.Errorf("ConnectAuth proof = %x, want %x", auth.GetProof(), wantProof)
				return
			}
			final := &protocol.ConnectResponse{}
			final.SetStatus(protocol.ConnectStatus_NEW_CLIENT)
			final.SetResetProof(wantDecision)
			if wire.WriteMessage(c.conn, final) != nil {
				return
			}
			c.drain()
		}}
		d := etcp.Dialer{NetDialer: s}
		conn, err := d.Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			t.Fatalf("Dial after challenge authentication: %v", err)
		}
		defer func() {
			_ = conn.Close()
			s.wg.Wait()
		}()
		if conn.Resumed() {
			t.Fatal("a NEW_CLIENT challenge connect reported an attach")
		}
		if err := conn.WritePacket(t.Context(), numbered(0, 10)); err != nil {
			t.Fatalf("WritePacket after challenge authentication: %v", err)
		}
	})
}

// TestDialChallengeResetAttach walks stock `et --attach`: resetIntent is set,
// the challenge proof binds that bit, the server's authenticated decision asks
// for reset recovery with a salt, both sides reset sequence and nonce state,
// and the caller must skip INITIAL_PAYLOAD. The first packet after the reset
// is sealed under the derived epoch key.
func TestDialChallengeResetAttach(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		challenge := mustHex(t, challengeHex)
		salt := mustHex(t, saltHex)
		wantProof := mustHex(t, attachProofHex)
		wantDecision := mustHex(t, attachDecisionHex)
		gotPacket := make(chan int, 1)
		s := &scripted{handle: func(i int, c *rawServer) {
			if i != 0 {
				return
			}
			var req protocol.ConnectRequest
			if wire.ReadMessage(c.br, &req) != nil {
				return
			}
			if !req.GetSupportsChallenge() || !req.GetResetIntent() {
				t.Errorf("ConnectRequest supportsChallenge=%v resetIntent=%v, want true/true",
					req.GetSupportsChallenge(), req.GetResetIntent())
				return
			}
			first := &protocol.ConnectResponse{}
			first.SetAuthChallenge(challenge)
			if wire.WriteMessage(c.conn, first) != nil {
				return
			}
			var auth protocol.ConnectAuth
			if wire.ReadMessage(c.br, &auth) != nil {
				return
			}
			if !bytes.Equal(auth.GetProof(), wantProof) {
				t.Errorf("attach proof = %x, want %x", auth.GetProof(), wantProof)
				return
			}
			final := &protocol.ConnectResponse{}
			final.SetStatus(protocol.ConnectStatus_RETURNING_CLIENT)
			final.SetResetRequired(true)
			final.SetResetSalt(salt)
			final.SetResetProof(wantDecision)
			if wire.WriteMessage(c.conn, final) != nil {
				return
			}

			var theirs protocol.SequenceHeader
			if wire.ReadMessage(c.br, &theirs) != nil {
				return
			}
			if !theirs.GetReset() || theirs.GetSequenceNumber() != 0 || !bytes.Equal(theirs.GetResetSalt(), salt) {
				t.Errorf("client reset header = %+v", &theirs)
				return
			}
			mine := &protocol.SequenceHeader{}
			mine.SetReset(true)
			mine.SetResetSalt(salt)
			if wire.WriteMessage(c.conn, mine) != nil {
				return
			}
			var catchup protocol.CatchupBuffer
			if wire.ReadMessage(c.br, &catchup) != nil || len(catchup.GetBuffer()) != 0 {
				t.Errorf("reset catchup = %v", catchup.GetBuffer())
				return
			}
			if wire.WriteMessage(c.conn, &protocol.CatchupBuffer{}) != nil {
				return
			}
			if err := c.in.Rekey(salt); err != nil {
				t.Errorf("server inbound rekey: %v", err)
				return
			}
			frame, err := wire.ReadFrame(c.br, nil)
			if err != nil {
				t.Errorf("read first reset-epoch frame: %v", err)
				return
			}
			p, err := c.open(frame)
			if err != nil {
				t.Errorf("open first reset-epoch frame: %v", err)
				return
			}
			if n, err := number(p); err == nil {
				gotPacket <- n
			}
			if err := c.out.Rekey(salt); err != nil {
				t.Errorf("server outbound rekey: %v", err)
				return
			}
			sealed := c.out.Seal(nil, []byte("attached"))
			frame = wire.AppendPacket(nil, true, protocol.HeaderTerminalBuffer, sealed)
			if wire.WriteFrame(c.conn, frame) != nil {
				return
			}
			c.drain()
		}}
		d := etcp.Dialer{NetDialer: s, ResetIntent: true}
		conn, err := d.Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			t.Fatalf("Dial attach: %v", err)
		}
		defer func() {
			_ = conn.Close()
			s.wg.Wait()
		}()
		if !conn.Resumed() {
			t.Fatal("a reset-recovery attach did not report Resumed")
		}
		if err := conn.WritePacket(t.Context(), numbered(7, 10)); err != nil {
			t.Fatalf("WritePacket under the reset epoch: %v", err)
		}
		select {
		case n := <-gotPacket:
			if n != 7 {
				t.Fatalf("server opened packet %d, want 7", n)
			}
		case <-time.After(time.Minute):
			t.Fatal("the server never opened a packet under the reset epoch")
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		p, err := conn.ReadPacket(ctx)
		if err != nil {
			t.Fatalf("ReadPacket under the reset epoch: %v", err)
		}
		if string(p.Payload) != "attached" {
			t.Fatalf("server packet after reset = %q, want %q", p.Payload, "attached")
		}
	})
}

// TestChallengeBadResetDecisionProofIsFatal: an attacker cannot replay a
// success response without the passkey, because the decision proof binds the
// fresh challenge.
func TestChallengeBadResetDecisionProofIsFatal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		challenge := mustHex(t, challengeHex)
		s := &scripted{handle: func(i int, c *rawServer) {
			if i != 0 {
				return
			}
			var req protocol.ConnectRequest
			if wire.ReadMessage(c.br, &req) != nil {
				return
			}
			first := &protocol.ConnectResponse{}
			first.SetAuthChallenge(challenge)
			if wire.WriteMessage(c.conn, first) != nil {
				return
			}
			var auth protocol.ConnectAuth
			if wire.ReadMessage(c.br, &auth) != nil {
				return
			}
			final := &protocol.ConnectResponse{}
			final.SetStatus(protocol.ConnectStatus_NEW_CLIENT)
			final.SetResetProof(bytes.Repeat([]byte{0}, 32))
			_ = wire.WriteMessage(c.conn, final)
		}}
		d := etcp.Dialer{NetDialer: s}
		if _, err := d.Dial(t.Context(), testAddr, testID, testKey); !errors.Is(err, etcp.ErrIntegrity) {
			t.Fatalf("Dial = %v, want ErrIntegrity", err)
		}
		if got := s.dials.Load(); got != 1 {
			t.Fatalf("dials = %d, want 1: a bad decision proof is not retried", got)
		}
	})
}

// TestLegacyServerRejectsResetIntent: a 6.2.10 server ignores the unknown
// resetIntent field and answers the legacy single response. Stock et fails
// with the upgrade message rather than reusing the old keystream, so the
// vendored client reports ErrLegacyReattach.
func TestLegacyServerRejectsResetIntent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &scripted{handle: func(i int, c *rawServer) {
			if i != 0 {
				return
			}
			var req protocol.ConnectRequest
			if wire.ReadMessage(c.br, &req) != nil {
				return
			}
			if !req.GetSupportsChallenge() || !req.GetResetIntent() {
				t.Errorf("request supportsChallenge=%v resetIntent=%v", req.GetSupportsChallenge(), req.GetResetIntent())
			}
			resp := &protocol.ConnectResponse{}
			resp.SetStatus(protocol.ConnectStatus_RETURNING_CLIENT)
			_ = wire.WriteMessage(c.conn, resp)
		}}
		d := etcp.Dialer{NetDialer: s, ResetIntent: true}
		_, err := d.Dial(t.Context(), testAddr, testID, testKey)
		if !errors.Is(err, etcp.ErrLegacyReattach) {
			t.Fatalf("Dial attach to a legacy server = %v, want ErrLegacyReattach", err)
		}
		if got := s.dials.Load(); got != 1 {
			t.Fatalf("dials = %d, want 1", got)
		}
	})
}

// TestUnsupportedVersionIsImmediate proves MISMATCHED_PROTOCOL is a typed,
// terminal answer on the first connect: no redial loop and no hang.
func TestUnsupportedVersionIsImmediate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &scripted{handle: func(i int, c *rawServer) {
			if i == 0 {
				_ = c.respond(protocol.ConnectStatus_MISMATCHED_PROTOCOL)
			}
		}}
		d := etcp.Dialer{NetDialer: s}
		if _, err := d.Dial(t.Context(), testAddr, testID, testKey); !errors.Is(err, etcp.ErrVersion) {
			t.Fatalf("Dial = %v, want ErrVersion", err)
		}
		if got := s.dials.Load(); got != 1 {
			t.Fatalf("dials = %d, want 1", got)
		}
	})
}

// TestRetryLaterDuringReconnectIsNotFatal: RETRY_LATER while a session's
// server rebuilds its state falls through the reconnect backoff and a later
// RETURNING_CLIENT recovery restores the link.
func TestRetryLaterDuringReconnectIsNotFatal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ready := make(chan struct{})
		got := make(chan int, 1)
		s := &scripted{handle: func(i int, c *rawServer) {
			switch i {
			case 0:
				if c.respond(protocol.ConnectStatus_NEW_CLIENT) != nil {
					return
				}
				frame, err := wire.ReadFrame(c.br, nil)
				if err != nil {
					return
				}
				if _, err := c.open(frame); err != nil {
					return
				}
			case 1, 2:
				_ = c.respond(protocol.ConnectStatus_RETRY_LATER)
			default:
				if c.respond(protocol.ConnectStatus_RETURNING_CLIENT) != nil {
					return
				}
				sh := &protocol.SequenceHeader{}
				sh.SetSequenceNumber(1) // the one packet the first link received
				if wire.WriteMessage(c.conn, sh) != nil {
					return
				}
				var theirs protocol.SequenceHeader
				if wire.ReadMessage(c.br, &theirs) != nil {
					return
				}
				var catchup protocol.CatchupBuffer
				if wire.ReadMessage(c.br, &catchup) != nil || len(catchup.GetBuffer()) != 0 {
					return
				}
				if wire.WriteMessage(c.conn, &protocol.CatchupBuffer{}) != nil {
					return
				}
				// The client's nonce for its next packet is one past the
				// packet the old link carried; this scripted server keeps no
				// state across links, so advance its inbound nonce once.
				_, _ = c.in.Open(nil, nil)
				close(ready)
				frame, err := wire.ReadFrame(c.br, nil)
				if err != nil {
					return
				}
				p, err := c.open(frame)
				if err != nil {
					return
				}
				if n, err := number(p); err == nil {
					got <- n
				}
			}
		}}
		d := etcp.Dialer{NetDialer: s}
		conn, err := d.Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer func() {
			_ = conn.Close()
			s.wg.Wait()
		}()
		if err := conn.WritePacket(t.Context(), numbered(0, 10)); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
		select {
		case <-ready:
		case <-time.After(time.Minute):
			t.Fatalf("the client never restored the link after RETRY_LATER; dials = %d", s.dials.Load())
		}
		if err := conn.WritePacket(t.Context(), numbered(1, 10)); err != nil {
			t.Fatalf("WritePacket after recovery: %v", err)
		}
		select {
		case n := <-got:
			if n != 1 {
				t.Fatalf("server got packet %d, want 1", n)
			}
		case <-time.After(time.Minute):
			t.Fatal("the restored link delivered nothing")
		}
		if got := s.dials.Load(); got != 4 {
			t.Fatalf("dials = %d, want 4 (two RETRY_LATER answers)", got)
		}
	})
}

// TestInitialResponseSetsPeerFloor: the server answers INITIAL_RESPONSE only
// after reading INITIAL_PAYLOAD, so a later reconnect that claims zero
// received packets has had its reader reset by another client. No earlier
// sequence report is needed for the first reconnect to notice. This is the
// shape of a second `et --attach` displacing a fresh session.
func TestInitialResponseSetsPeerFloor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var displaced atomic.Int32
		s := &scripted{handle: func(i int, c *rawServer) {
			switch i {
			case 0:
				if c.respond(protocol.ConnectStatus_NEW_CLIENT) != nil {
					return
				}
				frame, err := wire.ReadFrame(c.br, nil)
				if err != nil {
					return
				}
				if _, err := c.open(frame); err != nil {
					return
				}
				sealed := c.out.Seal(nil, nil)
				_ = wire.WriteFrame(c.conn, wire.AppendPacket(nil, true, protocol.HeaderInitialResponse, sealed))
			default:
				if c.respond(protocol.ConnectStatus_RETURNING_CLIENT) != nil {
					return
				}
				if wire.WriteMessage(c.conn, &protocol.SequenceHeader{}) != nil {
					return
				}
				c.drain()
			}
		}}
		d := etcp.Dialer{
			NetDialer:   s,
			OnDisplaced: func() { displaced.Add(1) },
		}
		conn, err := d.Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer func() {
			_ = conn.Close()
			s.wg.Wait()
		}()
		if err := conn.WritePacket(t.Context(), protocol.Packet{
			Header: protocol.HeaderInitialPayload, Payload: []byte("start"),
		}); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		if _, err := conn.ReadPacket(ctx); err != nil {
			t.Fatalf("ReadPacket(INITIAL_RESPONSE): %v", err)
		}
		if _, err := conn.ReadPacket(ctx); !errors.Is(err, etcp.ErrDisplaced) {
			t.Fatalf("ReadPacket after a zero reader sequence = %v, want ErrDisplaced", err)
		}
		if got := displaced.Load(); got != 1 {
			t.Fatalf("OnDisplaced ran %d times, want 1", got)
		}
	})
}

// TestServerSequenceGoingBackwardsIsDisplacement: once the server has
// reported receiving N packets, a reconnect that reports fewer means its
// reader was reset behind this client's back. Another client's reset recovery
// does exactly that. The Conn ends with ErrDisplaced and the callback runs.
func TestServerSequenceGoingBackwardsIsDisplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var displaced atomic.Int32
		s := &scripted{handle: func(i int, c *rawServer) {
			switch i {
			case 0:
				if c.respond(protocol.ConnectStatus_NEW_CLIENT) != nil {
					return
				}
				frame, err := wire.ReadFrame(c.br, nil)
				if err != nil {
					return
				}
				_, _ = c.open(frame)
			case 1:
				if c.respond(protocol.ConnectStatus_RETURNING_CLIENT) != nil {
					return
				}
				sh := &protocol.SequenceHeader{}
				sh.SetSequenceNumber(1)
				if wire.WriteMessage(c.conn, sh) != nil {
					return
				}
				var theirs protocol.SequenceHeader
				if wire.ReadMessage(c.br, &theirs) != nil {
					return
				}
				var catchup protocol.CatchupBuffer
				if wire.ReadMessage(c.br, &catchup) != nil {
					return
				}
				_ = wire.WriteMessage(c.conn, &protocol.CatchupBuffer{})
			default:
				if c.respond(protocol.ConnectStatus_RETURNING_CLIENT) != nil {
					return
				}
				if wire.WriteMessage(c.conn, &protocol.SequenceHeader{}) != nil {
					return
				}
				c.drain()
			}
		}}
		d := etcp.Dialer{
			NetDialer:   s,
			OnDisplaced: func() { displaced.Add(1) },
		}
		conn, err := d.Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer func() {
			_ = conn.Close()
			s.wg.Wait()
		}()
		if err := conn.WritePacket(t.Context(), numbered(0, 10)); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		if _, err := conn.ReadPacket(ctx); !errors.Is(err, etcp.ErrDisplaced) {
			t.Fatalf("ReadPacket = %v, want ErrDisplaced", err)
		}
		if got := displaced.Load(); got != 1 {
			t.Fatalf("OnDisplaced ran %d times, want 1", got)
		}
		if got := s.dials.Load(); got != 3 {
			t.Fatalf("dials = %d, want 3", got)
		}
	})
}
