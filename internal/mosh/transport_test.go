// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
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
	// lastClientTS is echoed as the timestamp reply so the client can take
	// RTT samples. Zero means no client timestamp has been seen yet.
	lastClientTS uint16
	haveClientTS bool
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
		reply := uint16(0xffff)
		if p.haveClientTS {
			reply = p.lastClientTS
		}
		binary.BigEndian.PutUint16(plaintext[2:4], reply)
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
	return p.hostStateWithThrowaway(oldNum, newNum, ack, 0, instructions, now)
}

func (p *testPeer) hostStateWithThrowaway(oldNum, newNum, ack, throwaway uint64,
	instructions []HostInstruction, now time.Time) [][]byte {
	var diff []byte
	if instructions != nil {
		diff = MarshalHostMessage(instructions)
	}
	return p.datagrams(TransportInstruction{
		ProtocolVersion: ProtocolVersion,
		OldNum:          oldNum,
		NewNum:          newNum,
		AckNum:          ack,
		ThrowawayNum:    throwaway,
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
	if timestamp := binary.BigEndian.Uint16(plaintext[:2]); timestamp != 0xffff {
		p.lastClientTS = timestamp
		p.haveClientTS = true
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
		update.HostStates = append(update.HostStates, got.HostStates...)
		update.RemoteShutdown = update.RemoteShutdown || got.RemoteShutdown
	}
	return update
}

func requireHostState(t *testing.T, update Update, want HostState) {
	t.Helper()
	if len(update.HostStates) != 1 {
		t.Fatalf("HostStates = %+v, want one state", update.HostStates)
	}
	got := update.HostStates[0]
	if got.Num != want.Num || got.OldNum != want.OldNum ||
		got.ThrowawayNum != want.ThrowawayNum || got.Newest != want.Newest ||
		!reflect.DeepEqual(got.Instructions, want.Instructions) {
		t.Fatalf("HostState = %+v, want %+v", got, want)
	}
}

// TestTransportHostStateThrowaway covers the snapshot-pruning metadata the
// renderer needs: diffs carry their own old/new/throwaway numbers, and states
// below the sender's throwaway point are no longer references.
func TestTransportHostStateThrowaway(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	receiveAll(t, client, peer.hostState(0, 1, 0, []HostInstruction{{HostString: []byte("A")}}, now), now)
	receiveAll(t, client, peer.hostState(1, 2, 0, []HostInstruction{{HostString: []byte("B")}}, now), now)
	update := receiveAll(t, client, peer.hostStateWithThrowaway(
		2, 3, 0, 2, []HostInstruction{{HostString: []byte("C")}}, now), now)
	requireHostState(t, update, HostState{Num: 3, OldNum: 2, ThrowawayNum: 2, Newest: true,
		Instructions: []HostInstruction{{HostString: []byte("C")}}})

	client.mu.Lock()
	var kept []uint64
	for _, state := range client.received {
		kept = append(kept, state.num)
	}
	client.mu.Unlock()
	if len(kept) != 2 || kept[0] != 2 || kept[1] != 3 {
		t.Fatalf("received snapshots after throwaway = %v, want [2 3]", kept)
	}

	// A state whose reference was thrown away is ignored; it must not produce
	// a HostState for the renderer.
	update = receiveAll(t, client, peer.hostState(1, 4, 0,
		[]HostInstruction{{HostString: []byte("stale")}}, now), now)
	if len(update.HostStates) != 0 {
		t.Fatalf("stale reference produced HostStates %+v", update.HostStates)
	}
	if len(update.Output) != 0 {
		t.Fatalf("stale reference produced output %q", update.Output)
	}
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
	if len(duplicate.Output) != 0 || len(duplicate.HostStates) != 0 {
		t.Fatalf("duplicate update = %+v", duplicate)
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

// lossyPeer is a stateful mosh server used by the loss-convergence test. It
// implements enough of stock's SSP sender and receiver to retransmit from the
// oldest host state the client has acknowledged and to acknowledge every
// client state. Host output echoes user keystrokes so input duplication or
// loss is directly observable.
type lossyPeer struct {
	t       *testing.T
	wire    *testPeer
	inputs  map[uint64][]UserInstruction
	hosts   map[uint64][]byte
	hostAck uint64
	hostTop uint64

	// delivered is the full client instruction list of the newest state the
	// peer has applied, used to emit only newly delivered keystrokes.
	delivered []UserInstruction
	clientTop uint64
	clientAck uint64

	input  []byte
	output []byte
}

func newLossyPeer(t *testing.T) *lossyPeer {
	return &lossyPeer{
		t:      t,
		wire:   newTestPeer(t),
		inputs: map[uint64][]UserInstruction{0: nil},
		hosts:  map[uint64][]byte{0: nil},
	}
}

func (p *lossyPeer) Close() { p.wire.Close() }

func instructionKeys(in []UserInstruction) []byte {
	var out []byte
	for _, instruction := range in {
		out = append(out, instruction.Keys...)
	}
	return out
}

// receive consumes one client wire datagram and returns the host datagrams
// due in response.
func (p *lossyPeer) receive(wire []byte, now time.Time) [][]byte {
	instruction, diff, complete := p.wire.feed(wire)
	if !complete {
		return nil
	}
	if instruction.AckNum != ShutdownNum {
		if _, known := p.hosts[instruction.AckNum]; known && instruction.AckNum > p.hostAck {
			p.hostAck = instruction.AckNum
		}
	}
	if _, seen := p.inputs[instruction.NewNum]; !seen {
		if base, known := p.inputs[instruction.OldNum]; known {
			combined := append(append([]UserInstruction(nil), base...), diff...)
			p.inputs[instruction.NewNum] = combined
			if instruction.NewNum > p.clientTop {
				if len(combined) < len(p.delivered) ||
					!userInstructionsEqual(combined[:len(p.delivered)], p.delivered) {
					p.t.Fatalf("client state %d diverged from the delivered state", instruction.NewNum)
				}
				keys := instructionKeys(combined[len(p.delivered):])
				p.input = append(p.input, keys...)
				p.output = append(p.output, keys...)
				p.delivered = combined
				p.clientTop = instruction.NewNum
				// Only a state applied at the receiver's end may be
				// acknowledged; acknowledging an out-of-order or
				// unapplied state would let the peer prune data the
				// receiver still needs.
				p.clientAck = instruction.NewNum
				if len(keys) > 0 {
					p.hosts[p.hostTop+1] = append([]byte(nil), p.output...)
					p.hostTop++
				}
			}
		}
	}
	return p.reply(now)
}

// reply sends the pending host output from the acknowledged base, or an
// empty ack state when the client has nothing outstanding. Re-sending from
// the acknowledged base is stock's recovery path after an optimistic diff
// was lost.
func (p *lossyPeer) reply(now time.Time) [][]byte {
	old := p.hostAck
	if old > p.hostTop {
		old = p.hostTop
	}
	newNum := p.hostTop
	var diff []byte
	if old == newNum {
		newNum++
		p.hostTop = newNum
		p.hosts[newNum] = p.hosts[old]
	} else {
		diff = p.hosts[newNum][len(p.hosts[old]):]
	}
	instruction := TransportInstruction{
		ProtocolVersion: ProtocolVersion,
		OldNum:          old,
		NewNum:          newNum,
		AckNum:          p.clientAck,
	}
	if len(diff) > 0 {
		instruction.Diff = MarshalHostMessage([]HostInstruction{{HostString: diff}})
	}
	return p.wire.datagrams(instruction, now)
}

// lossPRNG is a deterministic xorshift used so the loss schedule is
// reproducible on every run.
type lossPRNG struct{ state uint64 }

func (r *lossPRNG) next() uint64 {
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	return r.state
}

func (r *lossPRNG) drop(percent uint64) bool { return r.next()%100 < percent }

func TestTransportSustainedLossConverges(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newLossyPeer(t)
	defer peer.Close()

	const chunks = 50
	const chunkSize = 40
	expected := make([]byte, 0, chunks*chunkSize)
	queued := make([][]byte, 0, chunks)
	for chunk := 0; chunk < chunks; chunk++ {
		data := make([]byte, chunkSize)
		for i := range data {
			data[i] = byte('a' + (chunk+i)%26)
		}
		queued = append(queued, data)
		expected = append(expected, data...)
	}

	now := time.Unix(1700000000, 0)
	const step = 5 * time.Millisecond
	loss := &lossPRNG{state: 0x9e3779b97f4a7c15}
	lost := 0
	nextChunk := 0
	var output []byte
	for tick := 0; tick < 12000; tick++ {
		if tick%10 == 0 && nextChunk < len(queued) {
			if err := client.Input(queued[nextChunk]); err != nil {
				t.Fatalf("Input: %v", err)
			}
			nextChunk++
		}
		// Drop 35% in each direction, deterministically.
		for _, datagram := range client.SendDatagrams(now) {
			if loss.drop(35) {
				lost++
				continue
			}
			for _, reply := range peer.receive(datagram, now) {
				if loss.drop(35) {
					lost++
					continue
				}
				update, authenticated, receiveErr := client.Receive(reply, now)
				if receiveErr != nil {
					t.Fatalf("Receive: %v", receiveErr)
				}
				if authenticated {
					output = append(output, update.Output...)
				}
			}
		}
		if nextChunk == len(queued) && bytes.Equal(peer.input, expected) && bytes.Equal(output, expected) {
			break
		}
		now = now.Add(step)
	}
	if lost == 0 {
		t.Fatal("the loss schedule dropped no datagrams; the test proved nothing")
	}
	if !bytes.Equal(peer.input, expected) {
		t.Fatalf("server input = %d bytes, want %d", len(peer.input), len(expected))
	}
	if !bytes.Equal(output, expected) {
		t.Fatalf("client output = %d bytes, want %d (%q)", len(output), len(expected), output)
	}
}

func TestTransportReorderedFragmentDatagramsReassemble(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	payload := make([]byte, 3000)
	state := uint64(0x2545f4914f6cdd1d)
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
	// Reverse the datagrams: the final fragment arrives first. Stock still
	// feeds each one to fragment reassembly; only timestamp freshness is
	// sequence-gated. The tracer dropped every datagram but the first.
	for i := len(datagrams) - 1; i >= 0; i-- {
		update, authenticated, receiveErr := client.Receive(datagrams[i], now)
		if receiveErr != nil {
			t.Fatalf("Receive: %v", receiveErr)
		}
		if !authenticated {
			t.Fatalf("fragment %d of %d was not authenticated", i, len(datagrams))
		}
		if i == 0 && !bytes.Equal(update.Output, payload) {
			t.Fatalf("reordered output length = %d, want %d", len(update.Output), len(payload))
		}
	}
}

func TestTransportDuplicateWireDatagramIsIdempotent(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	datagrams := peer.hostState(0, 1, 0, []HostInstruction{{HostString: []byte("once")}}, now)
	first := receiveAll(t, client, datagrams, now)
	if string(first.Output) != "once" {
		t.Fatalf("output = %q", first.Output)
	}
	second := receiveAll(t, client, datagrams, now)
	if len(second.Output) != 0 {
		t.Fatalf("duplicate output = %q", second.Output)
	}
}

func TestTransportMTUStaysWithinIPv6Budget(t *testing.T) {
	// IPv6 minimum MTU 1280 minus the base header (40), two minimum-sized
	// extension headers (16), and UDP (8) leaves 1216 bytes for the UDP
	// payload. Stock mosh's fragment payload is 1216-12-16-10 = 1178.
	const ipv6UDPBudget = 1280 - 40 - 16 - 8
	if maxFragmentPayload != 1178 {
		t.Fatalf("maxFragmentPayload = %d, want stock's 1178", maxFragmentPayload)
	}
	wire := 8 + 4 + fragmentHeaderSize + maxFragmentPayload + 16
	if wire > ipv6UDPBudget {
		t.Fatalf("largest datagram = %d bytes, exceeds the IPv6 UDP budget %d", wire, ipv6UDPBudget)
	}
}

func TestTransportDelaysDataAckAndSendsItImmediatelyForShutdown(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	client.SendDatagrams(now)
	receiveAll(t, client, peer.hostState(0, 1, 0, []HostInstruction{{HostString: []byte("x")}}, now), now)
	if datagrams := client.SendDatagrams(now.Add(2 * time.Millisecond)); datagrams != nil {
		t.Fatalf("ack was not delayed: %d datagrams", len(datagrams))
	}
	datagrams := client.SendDatagrams(now.Add(ackDelay + time.Millisecond))
	if len(datagrams) != 1 {
		t.Fatalf("delayed ack produced %d datagrams, want 1", len(datagrams))
	}
	instruction, _, complete := peer.feed(datagrams[0])
	if !complete {
		t.Fatal("delayed ack did not decode")
	}
	if instruction.AckNum != 1 {
		t.Fatalf("ack = %d, want 1", instruction.AckNum)
	}

	// The shutdown sentinel must be acknowledged without waiting for the
	// delayed-ack timer.
	receiveAll(t, client, peer.hostState(1, ShutdownNum, 1, []HostInstruction{{HostString: []byte("bye")}}, now), now)
	ack := client.SendDatagrams(now.Add(time.Millisecond))
	if len(ack) != 1 {
		t.Fatalf("shutdown ack produced %d datagrams, want 1", len(ack))
	}
	instruction, _, complete = peer.feed(ack[0])
	if !complete || instruction.AckNum != ShutdownNum {
		t.Fatalf("shutdown ack = %+v, complete %v", instruction, complete)
	}
}

func TestTransportStaleAckResendsFromAssumedBase(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	initial := client.SendDatagrams(now)
	if len(initial) == 0 {
		t.Fatal("initial state was not sent")
	}
	if _, _, complete := peer.feed(initial[0]); !complete {
		t.Fatal("initial state did not decode")
	}
	// The host does not acknowledge the client state, so the base stays 0.
	receiveAll(t, client, peer.hostState(0, 1, 0,
		[]HostInstruction{{HostString: []byte("x")}}, now), now)
	if datagrams := client.SendDatagrams(now.Add(ackDelay + time.Millisecond)); len(datagrams) != 1 {
		t.Fatalf("first ack produced %d datagrams, want 1", len(datagrams))
	}

	// A later host state re-arms the ack after every sent state has aged out
	// of the optimistic window. An empty ack here would reference state 1 or
	// 2, which the server may never have received, so stock resends the real
	// diff from its assumed receiver base (state 0).
	stale := now.Add(2*(maxRTO+ackDelay) + 20*time.Millisecond)
	receiveAll(t, client, peer.hostState(1, 2, 0,
		[]HostInstruction{{HostString: []byte("x")}, {HostString: []byte("y")}}, stale), stale)
	datagrams := client.SendDatagrams(stale.Add(ackDelay + time.Millisecond))
	if len(datagrams) != 1 {
		t.Fatalf("stale delayed ack produced %d datagrams, want 1", len(datagrams))
	}
	ack, _, complete := peer.feed(datagrams[0])
	if !complete {
		t.Fatal("stale ack did not decode")
	}
	if ack.AckNum != 2 {
		t.Fatalf("ack = %d, want 2", ack.AckNum)
	}
	if ack.OldNum != 0 {
		t.Fatalf("ack old_num = %d, want the assumed receiver base 0", ack.OldNum)
	}
	if len(ack.Diff) == 0 {
		t.Fatal("stale ack resent an empty diff; the peer may never learn the client state")
	}
}

func TestTransportShutdownRetransmitsAtFrameRate(t *testing.T) {
	client, err := NewClient(testKey(), 80, 24)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	peer := newTestPeer(t)
	defer peer.Close()

	now := time.Unix(1700000000, 0)
	client.SendDatagrams(now)
	first := client.BeginShutdown(now.Add(time.Millisecond))
	if len(first) != 1 {
		t.Fatalf("shutdown datagrams = %d", len(first))
	}
	if again := client.SendDatagrams(now.Add(sendIntervalMax)); again != nil {
		t.Fatalf("sentinel retransmitted after only %s", time.Duration(sendIntervalMax))
	}
	retransmit := client.SendDatagrams(now.Add(time.Millisecond + sendIntervalMax))
	if len(retransmit) != 1 {
		t.Fatalf("sentinel retransmissions = %d, want 1", len(retransmit))
	}
	instruction, _, complete := peer.feed(retransmit[0])
	if !complete || instruction.NewNum != ShutdownNum {
		t.Fatalf("sentinel = %+v, complete %v", instruction, complete)
	}
}

func TestTransportRetransmitSendsImmediately(t *testing.T) {
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
	// A second SendDatagrams would wait for the RTO; Retransmit is for the
	// probe's fallback address and must send now.
	if datagrams := client.SendDatagrams(now.Add(time.Millisecond)); datagrams != nil {
		t.Fatalf("unexpected RTO send: %d datagrams", len(datagrams))
	}
	datagrams := client.Retransmit(now.Add(time.Millisecond))
	if len(datagrams) != 1 {
		t.Fatalf("Retransmit produced %d datagrams, want 1", len(datagrams))
	}
	instruction, diff, complete := peer.feed(datagrams[0])
	if !complete || instruction.OldNum != 0 || instruction.NewNum != 1 {
		t.Fatalf("retransmit = %+v, complete %v", instruction, complete)
	}
	if len(diff) != 1 || !diff[0].Resize {
		t.Fatalf("retransmit diff = %+v", diff)
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
	requireHostState(t, update, HostState{Num: 1, OldNum: 0, Newest: true,
		Instructions: []HostInstruction{{HostString: []byte("A")}}})
	// State 3 arrives before state 2 but cites state 1, so it applies.
	update = receiveAll(t, client, peer.hostState(1, 3, 0, []HostInstruction{{HostString: []byte("C")}}, now), now)
	if string(update.Output) != "C" {
		t.Fatalf("state 3 output = %q", update.Output)
	}
	requireHostState(t, update, HostState{Num: 3, OldNum: 1, Newest: true,
		Instructions: []HostInstruction{{HostString: []byte("C")}}})
	// State 2 arrives late; it is retained but not delivered because state 3
	// is already the newest state.
	update = receiveAll(t, client, peer.hostState(1, 2, 0, []HostInstruction{{HostString: []byte("B")}}, now), now)
	if len(update.Output) != 0 {
		t.Fatalf("out-of-order state output = %q", update.Output)
	}
	requireHostState(t, update, HostState{Num: 2, OldNum: 1, Newest: false,
		Instructions: []HostInstruction{{HostString: []byte("B")}}})
	// State 4 is built on state 3 and must not repeat B or C. The diff it
	// reports is only D, not the accumulated reference instructions.
	update = receiveAll(t, client, peer.hostState(3, 4, 0, []HostInstruction{{HostString: []byte("D")}}, now), now)
	if string(update.Output) != "D" {
		t.Fatalf("state 4 output = %q", update.Output)
	}
	requireHostState(t, update, HostState{Num: 4, OldNum: 3, Newest: true,
		Instructions: []HostInstruction{{HostString: []byte("D")}}})
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
	// A replayed datagram is still authenticated and fed to SSP, like stock,
	// but its state number is already known, so it produces no output.
	if update, authenticated, err := client.Receive(datagrams[0], now); err != nil || !authenticated || len(update.Output) != 0 {
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
