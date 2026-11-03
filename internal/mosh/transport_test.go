// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

const testKeyByte = 0x5a

func testKey() []byte { return bytes.Repeat([]byte{testKeyByte}, 16) }

// testPeer speaks the server side of a mosh session for deterministic SSP
// tests: it encrypts host instructions and decodes client instructions without
// any UDP.
type testPeer struct {
	t         *testing.T
	ocb       *OCB
	seq       uint64
	fragID    uint64
	assembler fragmentAssembler
}

func newTestPeer(t *testing.T) *testPeer {
	t.Helper()
	ocb, err := NewOCB(testKey())
	if err != nil {
		t.Fatalf("NewOCB: %v", err)
	}
	return &testPeer{t: t, ocb: ocb, assembler: newFragmentAssembler()}
}

func (p *testPeer) Close() { p.ocb.Close() }

func (p *testPeer) datagrams(instruction TransportInstruction, now time.Time) [][]byte {
	p.t.Helper()
	compressed, err := compressInstruction(instruction.Marshal())
	if err != nil {
		p.t.Fatalf("compressInstruction: %v", err)
	}
	p.fragID++
	var out [][]byte
	number := uint16(0)
	for offset := 0; offset < len(compressed); offset += maxFragmentPayload {
		end := offset + maxFragmentPayload
		if end > len(compressed) {
			end = len(compressed)
		}
		frag := fragment{id: p.fragID, number: number, final: end == len(compressed), content: compressed[offset:end]}
		p.seq++
		sequence := p.seq
		var nonce [12]byte
		binary.BigEndian.PutUint64(nonce[4:], directionServerToClient|sequence)
		plaintext := make([]byte, 4, 4+len(frag.marshal()))
		binary.BigEndian.PutUint16(plaintext[:2], timestamp16(now))
		binary.BigEndian.PutUint16(plaintext[2:4], 0xffff)
		plaintext = append(plaintext, frag.marshal()...)
		ciphertext, err := p.ocb.Encrypt(nonce[:], plaintext)
		if err != nil {
			p.t.Fatalf("Encrypt: %v", err)
		}
		wire := make([]byte, 8+len(ciphertext))
		binary.BigEndian.PutUint64(wire[:8], directionServerToClient|sequence)
		copy(wire[8:], ciphertext)
		out = append(out, wire)
		number++
	}
	return out
}

func (p *testPeer) hostState(oldNum, newNum, ack uint64, instructions []HostInstruction, now time.Time) [][]byte {
	var diff []byte
	if instructions != nil {
		diff = MarshalHostMessage(instructions)
	}
	return p.datagrams(TransportInstruction{
		ProtocolVersion: ProtocolVersion,
		OldNum:          oldNum,
		NewNum:          newNum,
		AckNum:          ack,
		Diff:            diff,
	}, now)
}

// feed decodes zero or more client datagrams. It returns the completed
// instruction and the user instructions in its diff.
func (p *testPeer) feed(wire []byte) (TransportInstruction, []UserInstruction, bool) {
	p.t.Helper()
	if len(wire) < 8+16 {
		return TransportInstruction{}, nil, false
	}
	directionSequence := binary.BigEndian.Uint64(wire[:8])
	if directionSequence&directionServerToClient != 0 {
		p.t.Fatal("client sent a server-direction datagram")
	}
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], directionSequence)
	plaintext, ok := p.ocb.Decrypt(nonce[:], wire[8:])
	if !ok {
		p.t.Fatal("client datagram failed authentication")
	}
	if len(plaintext) < 4+fragmentHeaderSize {
		p.t.Fatalf("client datagram too short: %d", len(plaintext))
	}
	frag, err := unmarshalFragment(plaintext[4:])
	if err != nil {
		p.t.Fatalf("unmarshalFragment: %v", err)
	}
	assembled, complete := p.assembler.add(frag)
	if !complete {
		return TransportInstruction{}, nil, false
	}
	raw, err := decompressInstruction(assembled)
	if err != nil {
		p.t.Fatalf("decompressInstruction: %v", err)
	}
	instruction, err := UnmarshalTransportInstruction(raw)
	if err != nil {
		p.t.Fatalf("UnmarshalTransportInstruction: %v", err)
	}
	var diff []UserInstruction
	if len(instruction.Diff) > 0 {
		diff, err = UnmarshalUserMessage(instruction.Diff)
		if err != nil {
			p.t.Fatalf("UnmarshalUserMessage: %v", err)
		}
	}
	return instruction, diff, true
}

func receiveAll(t *testing.T, client *Client, datagrams [][]byte, now time.Time) Update {
	t.Helper()
	var update Update
	for _, datagram := range datagrams {
		got, authenticated, err := client.Receive(datagram, now)
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if !authenticated {
			t.Fatal("datagram was not authenticated")
		}
		update.Output = append(update.Output, got.Output...)
		update.RemoteShutdown = update.RemoteShutdown || got.RemoteShutdown
	}
	return update
}

func TestTransportInitialSend(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	datagrams := client.SendDatagrams(now)
	if len(datagrams) != 1 {
		t.Fatalf("initial send produced %d datagrams, want 1", len(datagrams))
	}
	if bytes.Contains(datagrams[0], testKey()) {
		t.Fatal("wire datagram contains the cleartext key")
	}
	instruction, diff, complete := peer.feed(datagrams[0])
	if !complete {
		t.Fatal("initial datagram did not decode")
	}
	if instruction.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %d", instruction.ProtocolVersion)
	}
	if instruction.OldNum != 0 || instruction.NewNum != 1 || instruction.AckNum != 0 {
		t.Fatalf("initial instruction = old %d new %d ack %d", instruction.OldNum, instruction.NewNum, instruction.AckNum)
	}
	if len(diff) != 1 || !diff[0].Resize || diff[0].Width != 80 || diff[0].Height != 24 {
		t.Fatalf("initial diff = %+v", diff)
	}
	// Nothing else is due immediately after the initial datagram.
	if extra := client.SendDatagrams(now.Add(10 * time.Millisecond)); extra != nil {
		t.Fatalf("sent %d unsolicited extra datagrams", len(extra))
	}
}

func TestTransportReceivesOutputAndSendsInput(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	if datagrams := client.SendDatagrams(now); len(datagrams) != 1 {
		t.Fatalf("initial datagrams = %d", len(datagrams))
	}
	// The server acknowledged our initial state 1 and sent host state 1.
	update := receiveAll(t, client, peer.hostState(0, 1, 1, []HostInstruction{{HostString: []byte("hello ")}}, now), now)
	if string(update.Output) != "hello " {
		t.Fatalf("output = %q", update.Output)
	}
	if update.RemoteShutdown {
		t.Fatal("unexpected remote shutdown")
	}

	// A duplicate state must not be delivered twice.
	duplicate := receiveAll(t, client, peer.hostState(0, 1, 1, []HostInstruction{{HostString: []byte("hello ")}}, now.Add(time.Millisecond)), now.Add(time.Millisecond))
	if len(duplicate.Output) != 0 {
		t.Fatalf("duplicate output = %q", duplicate.Output)
	}

	if err := client.Input([]byte("ls\r")); err != nil {
		t.Fatalf("Input: %v", err)
	}
	datagrams := client.SendDatagrams(now.Add(2 * time.Millisecond))
	if len(datagrams) != 1 {
		t.Fatalf("input produced %d datagrams", len(datagrams))
	}
	instruction, diff, complete := peer.feed(datagrams[0])
	if !complete {
		t.Fatal("input datagram did not decode")
	}
	if instruction.OldNum != 1 || instruction.NewNum != 2 || instruction.AckNum != 1 {
		t.Fatalf("input instruction = old %d new %d ack %d", instruction.OldNum, instruction.NewNum, instruction.AckNum)
	}
	if len(diff) != 1 || string(diff[0].Keys) != "ls\r" {
		t.Fatalf("input diff = %+v", diff)
	}

	// Acknowledgement prunes the sent history, so the next state starts at 2.
	receiveAll(t, client, peer.hostState(1, 2, 2, []HostInstruction{{HostString: []byte("ok\r\n")}}, now.Add(3*time.Millisecond)), now.Add(3*time.Millisecond))
	if err := client.Resize(132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	datagrams = client.SendDatagrams(now.Add(4 * time.Millisecond))
	instruction, diff, complete = peer.feed(datagrams[0])
	if !complete {
		t.Fatal("resize datagram did not decode")
	}
	if instruction.OldNum != 2 || instruction.NewNum != 3 {
		t.Fatalf("resize instruction = old %d new %d", instruction.OldNum, instruction.NewNum)
	}
	if len(diff) != 1 || !diff[0].Resize || diff[0].Width != 132 || diff[0].Height != 43 {
		t.Fatalf("resize diff = %+v", diff)
	}
}

func TestTransportLargeHostOutputFragments(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	payload := make([]byte, 6000)
	state := uint64(0x9e3779b97f4a7c15)
	for i := range payload {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		payload[i] = byte(state)
	}
	datagrams := peer.hostState(0, 1, 0, []HostInstruction{{HostString: payload}}, now)
	if len(datagrams) < 2 {
		t.Fatalf("large output used %d datagrams, want a fragmented instruction", len(datagrams))
	}
	update := receiveAll(t, client, datagrams, now)
	if !bytes.Equal(update.Output, payload) {
		t.Fatalf("output length = %d, want %d", len(update.Output), len(payload))
	}
}

func TestTransportOutOfOrderStates(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	update := receiveAll(t, client, peer.hostState(0, 1, 0, []HostInstruction{{HostString: []byte("A")}}, now), now)
	if string(update.Output) != "A" {
		t.Fatalf("state 1 output = %q", update.Output)
	}
	// State 3 arrives before state 2 but cites state 1, so it applies.
	update = receiveAll(t, client, peer.hostState(1, 3, 0, []HostInstruction{{HostString: []byte("C")}}, now), now)
	if string(update.Output) != "C" {
		t.Fatalf("state 3 output = %q", update.Output)
	}
	// State 2 arrives late; it is retained but not delivered because state 3
	// is already the newest state.
	update = receiveAll(t, client, peer.hostState(1, 2, 0, []HostInstruction{{HostString: []byte("B")}}, now), now)
	if len(update.Output) != 0 {
		t.Fatalf("out-of-order state output = %q", update.Output)
	}
	// State 4 is built on state 3 and must not repeat B or C.
	update = receiveAll(t, client, peer.hostState(3, 4, 0, []HostInstruction{{HostString: []byte("D")}}, now), now)
	if string(update.Output) != "D" {
		t.Fatalf("state 4 output = %q", update.Output)
	}
	client.mu.Lock()
	ack := client.ackNum
	client.mu.Unlock()
	if ack != 4 {
		t.Fatalf("ack number = %d, want 4", ack)
	}
}

func TestTransportIgnoresReplayAndTampering(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	datagrams := peer.hostState(0, 1, 0, []HostInstruction{{HostString: []byte("once")}}, now)
	update := receiveAll(t, client, datagrams, now)
	if string(update.Output) != "once" {
		t.Fatalf("output = %q", update.Output)
	}
	// Replaying the same wire datagram is rejected before decryption.
	if update, authenticated, err := client.Receive(datagrams[0], now); err != nil || authenticated || len(update.Output) != 0 {
		t.Fatalf("replay = %+v, authenticated %v, error %v", update, authenticated, err)
	}

	tampered := append([]byte(nil), peer.hostState(1, 2, 0, []HostInstruction{{HostString: []byte("bad")}}, now)[0]...)
	tampered[20] ^= 0x01
	if _, authenticated, err := client.Receive(tampered, now); err != nil || authenticated {
		t.Fatalf("tampered datagram accepted: authenticated %v, error %v", authenticated, err)
	}
}

func TestTransportRejectsWrongProtocolVersion(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	bad := TransportInstruction{ProtocolVersion: 3, OldNum: 0, NewNum: 1}
	if _, _, err := client.Receive(peer.datagrams(bad, now)[0], now); !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
	// A protocol failure is sticky and stops further sends.
	if client.SendDatagrams(now) != nil {
		t.Fatal("client sent after a protocol failure")
	}
}

func TestTransportRetransmitsAfterRTO(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	initial := client.SendDatagrams(now)
	if len(initial) != 1 {
		t.Fatalf("initial datagrams = %d", len(initial))
	}
	if retransmit := client.SendDatagrams(now.Add(100 * time.Millisecond)); retransmit != nil {
		t.Fatal("retransmitted before the RTO")
	}
	retransmit := client.SendDatagrams(now.Add(maxRTO + ackDelay))
	if len(retransmit) != 1 {
		t.Fatalf("retransmit datagrams = %d", len(retransmit))
	}
	instruction, diff, complete := peer.feed(retransmit[0])
	if !complete {
		t.Fatal("retransmit did not decode")
	}
	if instruction.NewNum != 1 || instruction.OldNum != 0 {
		t.Fatalf("retransmit instruction = old %d new %d", instruction.OldNum, instruction.NewNum)
	}
	if len(diff) != 1 || !diff[0].Resize {
		t.Fatalf("retransmit diff = %+v", diff)
	}
}

func TestTransportRemoteShutdown(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	update := receiveAll(t, client, peer.hostState(0, ShutdownNum, 0, []HostInstruction{{HostString: []byte("bye\r\n")}}, now), now)
	if string(update.Output) != "bye\r\n" {
		t.Fatalf("final output = %q", update.Output)
	}
	if !update.RemoteShutdown || !client.RemoteShutdown() {
		t.Fatal("remote shutdown not reported")
	}
	ackDatagrams := client.SendDatagrams(now.Add(time.Millisecond))
	if len(ackDatagrams) == 0 {
		t.Fatal("client did not acknowledge the shutdown sentinel")
	}
	instruction, _, complete := peer.feed(ackDatagrams[0])
	if !complete {
		t.Fatal("ack datagram did not decode")
	}
	if instruction.AckNum != ShutdownNum {
		t.Fatalf("ack = %d, want the shutdown sentinel", instruction.AckNum)
	}
	if !client.CounterpartyShutdownAcknowledged() {
		t.Fatal("counterparty shutdown acknowledgement not recorded")
	}
}

func TestTransportLocalShutdown(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	client.SendDatagrams(now)
	shutdownDatagrams := client.BeginShutdown(now.Add(time.Millisecond))
	if len(shutdownDatagrams) != 1 {
		t.Fatalf("shutdown datagrams = %d", len(shutdownDatagrams))
	}
	instruction, _, complete := peer.feed(shutdownDatagrams[0])
	if !complete {
		t.Fatal("shutdown datagram did not decode")
	}
	if instruction.NewNum != ShutdownNum {
		t.Fatalf("shutdown new_num = %d", instruction.NewNum)
	}
	if client.ShutdownComplete() {
		t.Fatal("shutdown reported complete before the server acked")
	}
	// The server acknowledges the sentinel while also sending a host state.
	receiveAll(t, client, peer.hostState(0, 1, ShutdownNum, nil, now.Add(2*time.Millisecond)), now.Add(2*time.Millisecond))
	if !client.ShutdownComplete() {
		t.Fatal("shutdown not complete after the server ack")
	}
	if retransmit := client.SendDatagrams(now.Add(3 * time.Second)); retransmit != nil {
		t.Fatal("still sending after shutdown completed")
	}
}

func TestTransportCloseZeroizesAndRejectsUse(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.Close()
	for i, b := range client.ocb.key {
		if b != 0 {
			t.Fatalf("key byte %d not zeroized: %#x", i, b)
		}
	}
	if err := client.Input([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Input after Close = %v, want ErrClosed", err)
	}
	if _, _, err := client.Receive(nil, time.Now()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Receive after Close = %v, want ErrClosed", err)
	}
	client.Close()
}

func TestNewClientRejectsBadKey(t *testing.T) {
	if _, err := NewClient(make([]byte, 15), 80, 24); !errors.Is(err, ErrKey) {
		t.Fatalf("error = %v, want ErrKey", err)
	}
}
