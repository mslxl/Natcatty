// SPDX-License-Identifier: GPL-3.0-or-later

// Package moshtest provides an in-process mosh-server fixture and a roaming
// UDP relay. It speaks the production wire format (OCB3, fragments, zlib,
// protobuf) through internal/mosh, so an adapter or application test can drive
// the real in-process client without a remote host or a mosh-server binary.
//
// The fixture is intentionally small: it answers the initial probe, echoes the
// two inputs the phone tests use ("echo hi" and "exit"), records resizes, and
// performs the shutdown handshake. Every datagram is authenticated with the
// session key, so a reply from a new source address is accepted exactly as
// stock mosh accepts a NAT rebinding.
package moshtest

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	moshcore "github.com/mslxl/natcatty/internal/mosh"
)

// Server is one in-process mosh-server on loopback UDP.
type Server struct {
	t    testing.TB
	conn *net.UDPConn
	ocb  *moshcore.OCB
	key  []byte

	// sendMu guards the send path and every field the receive loop reads while
	// answering, so injected state cannot race a reply.
	sendMu       sync.Mutex
	seq          uint64
	fragID       uint64
	clientAddr   *net.UDPAddr
	hostHistory  []moshcore.TransportInstruction
	lastClientTS uint32

	assemblerID uint64
	haveID      bool
	parts       [][]byte
	present     []bool
	arrived     int
	total       int

	seen         map[uint64]bool
	clientAckNum uint64
	lastHostNum  uint64
	inputBuffer  []byte
	firstReply   bool

	input       chan []byte
	resizes     chan [2]int
	shutdown    chan struct{}
	shutdownAck chan struct{}
	stop        chan struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup
}

// New binds a server to 127.0.0.1:0 and starts it. The caller must not start
// another test on the same Server; Cleanup closes it.
func New(t testing.TB) *Server {
	t.Helper()
	key := bytes.Repeat([]byte{0x33}, 16)
	ocb, err := moshcore.NewOCB(key)
	if err != nil {
		t.Fatalf("moshtest: NewOCB: %v", err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("moshtest: ListenUDP: %v", err)
	}
	server := &Server{
		t:           t,
		conn:        conn,
		ocb:         ocb,
		key:         key,
		seen:        make(map[uint64]bool),
		firstReply:  true,
		input:       make(chan []byte, 64),
		resizes:     make(chan [2]int, 16),
		shutdown:    make(chan struct{}, 1),
		shutdownAck: make(chan struct{}, 1),
		stop:        make(chan struct{}),
	}
	server.wg.Add(1)
	go server.loop()
	t.Cleanup(server.Close)
	return server
}

// Close stops the server and zeroizes its key.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.conn.Close()
		s.wg.Wait()
		s.ocb.Close()
	})
}

// Port is the UDP port the server listens on.
func (s *Server) Port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

// Addr is the server's bound UDP address, for a relay.
func (s *Server) Addr() *net.UDPAddr { return s.conn.LocalAddr().(*net.UDPAddr) }

// Announcement is the MOSH CONNECT line an SSH bootstrap would print.
func (s *Server) Announcement() string { return s.AnnouncementOn(s.Port()) }

// AnnouncementOn builds the line for a relay listening on port.
func (s *Server) AnnouncementOn(port int) string {
	return "MOSH CONNECT " + strconv.Itoa(port) + " " +
		base64.RawStdEncoding.EncodeToString(s.key) + "\r\n"
}

// ClientAddress is the source address of the most recent client datagram.
func (s *Server) ClientAddress() *net.UDPAddr {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.clientAddr == nil {
		return nil
	}
	clone := *s.clientAddr
	return &clone
}

// Input reports each chunk of keys the client sent.
func (s *Server) Input() <-chan []byte { return s.input }

// Resizes reports each client resize instruction.
func (s *Server) Resizes() <-chan [2]int { return s.resizes }

// Send pushes a host state carrying text, as a running remote program would
// while the phone has no view attached. The client address is the most recent
// authenticated datagram's source.
func (s *Server) Send(text string) { s.sendHostState([]byte(text), 0) }

// Shutdown reports a client shutdown sentinel.
func (s *Server) Shutdown() <-chan struct{} { return s.shutdown }

// ShutdownAck reports the client's acknowledgement of the server's sentinel.
func (s *Server) ShutdownAck() <-chan struct{} { return s.shutdownAck }

func (s *Server) loop() {
	defer s.wg.Done()
	buffer := make([]byte, 2048)
	for {
		n, addr, err := s.conn.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-s.stop:
				return
			default:
				return
			}
		}
		s.sendMu.Lock()
		s.clientAddr = addr
		s.sendMu.Unlock()
		instruction, keys, resize, complete := s.decode(buffer[:n])
		if !complete {
			continue
		}
		// Stock mosh-server resends every host state the client has not
		// acknowledged. Without this, a client that receives a newer diff
		// before the state its OldNum names can never apply it, and the
		// session stalls even though both datagrams arrived.
		s.recoverHostState(instruction.AckNum)
		if instruction.NewNum == moshcore.ShutdownNum {
			select {
			case s.shutdown <- struct{}{}:
			default:
			}
			oldNum := s.lastHostNum
			s.lastHostNum++
			s.send(moshcore.TransportInstruction{
				ProtocolVersion: 2,
				OldNum:          oldNum,
				NewNum:          s.lastHostNum,
				AckNum:          moshcore.ShutdownNum,
			})
			continue
		}
		if instruction.AckNum == moshcore.ShutdownNum {
			select {
			case s.shutdownAck <- struct{}{}:
			default:
			}
		}
		if s.seen[instruction.NewNum] {
			// recoverHostState already resent everything newer than the
			// client's acknowledgement, which is stock's recovery.
			continue
		}
		s.seen[instruction.NewNum] = true
		s.noteClientState(instruction.NewNum)
		if len(keys) > 0 {
			s.inputBuffer = append(s.inputBuffer, keys...)
			select {
			case s.input <- append([]byte(nil), keys...):
			default:
			}
		}
		if resize != nil {
			select {
			case s.resizes <- [2]int{resize[0], resize[1]}:
			case <-s.stop:
				return
			}
		}
		if len(keys) > 0 && strings.Contains(string(s.inputBuffer), "exit") {
			s.sendHostState([]byte("bye\r\n"), moshcore.ShutdownNum)
			continue
		}
		if s.firstReply || len(keys) > 0 || resize != nil {
			output := []byte("fake$ ")
			if strings.Contains(string(s.inputBuffer), "echo hi") && len(keys) > 0 {
				output = []byte("hi\r\n")
			}
			s.firstReply = false
			s.sendHostState(output, 0)
		}
	}
}

func (s *Server) noteClientState(num uint64) {
	s.sendMu.Lock()
	if num > s.clientAckNum {
		s.clientAckNum = num
	}
	s.sendMu.Unlock()
}

func (s *Server) sendHostState(output []byte, newNum uint64) {
	var instructions []moshcore.HostInstruction
	if len(output) > 0 {
		instructions = []moshcore.HostInstruction{{HostString: output}}
	}
	var diff []byte
	if len(instructions) > 0 {
		diff = moshcore.MarshalHostMessage(instructions)
	}
	s.sendMu.Lock()
	if newNum == 0 {
		newNum = s.lastHostNum + 1
	}
	instruction := moshcore.TransportInstruction{
		ProtocolVersion: 2,
		OldNum:          s.lastHostNum,
		NewNum:          newNum,
		AckNum:          s.clientAckNum,
		Diff:            diff,
	}
	if newNum != moshcore.ShutdownNum {
		s.lastHostNum = newNum
		stored := instruction
		stored.Diff = append([]byte(nil), instruction.Diff...)
		s.hostHistory = append(s.hostHistory, stored)
		if len(s.hostHistory) > 64 {
			s.hostHistory = s.hostHistory[len(s.hostHistory)-64:]
		}
	}
	s.sendMu.Unlock()
	s.send(instruction)
}

// recoverHostState resends, in order, every host state newer than the one the
// client acknowledged. Stock mosh-server keeps unacknowledged states and
// replays them, which lets a client that saw a diff before the state its
// OldNum names still make progress.
func (s *Server) recoverHostState(ack uint64) {
	s.sendMu.Lock()
	kept := s.hostHistory[:0]
	resend := make([]moshcore.TransportInstruction, 0, len(s.hostHistory))
	for _, state := range s.hostHistory {
		if state.NewNum <= ack {
			continue
		}
		kept = append(kept, state)
		resend = append(resend, state)
	}
	s.hostHistory = kept
	s.sendMu.Unlock()
	for _, state := range resend {
		s.send(state)
	}
}

func (s *Server) send(instruction moshcore.TransportInstruction) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.clientAddr == nil {
		return
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(instruction.Marshal()); err != nil {
		s.t.Errorf("moshtest: zlib write: %v", err)
		return
	}
	if err := writer.Close(); err != nil {
		s.t.Errorf("moshtest: zlib close: %v", err)
		return
	}
	s.fragID++
	payload := compressed.Bytes()
	number := uint16(0)
	for offset := 0; offset < len(payload); offset += 1180 {
		end := offset + 1180
		if end > len(payload) {
			end = len(payload)
		}
		header := make([]byte, 10)
		binary.BigEndian.PutUint64(header[:8], s.fragID)
		combined := number
		if end == len(payload) {
			combined |= 0x8000
		}
		binary.BigEndian.PutUint16(header[8:], combined)
		fragment := append(header, payload[offset:end]...)

		s.seq++
		sequence := s.seq
		var nonce [12]byte
		binary.BigEndian.PutUint64(nonce[4:], 1<<63|sequence)
		plaintext := make([]byte, 4, 4+len(fragment))
		binary.BigEndian.PutUint16(plaintext[:2], uint16(time.Now().UnixMilli()))
		reply := uint16(0xffff)
		if s.lastClientTS != 0 {
			reply = uint16(s.lastClientTS)
		}
		binary.BigEndian.PutUint16(plaintext[2:4], reply)
		plaintext = append(plaintext, fragment...)
		ciphertext, err := s.ocb.Encrypt(nonce[:], plaintext)
		if err != nil {
			s.t.Errorf("moshtest: Encrypt: %v", err)
			return
		}
		wire := make([]byte, 8+len(ciphertext))
		binary.BigEndian.PutUint64(wire[:8], 1<<63|sequence)
		copy(wire[8:], ciphertext)
		if _, err := s.conn.WriteToUDP(wire, s.clientAddr); err != nil {
			select {
			case <-s.stop:
			default:
				s.t.Logf("moshtest: server write: %v", err)
			}
			return
		}
		number++
	}
}

// decode parses client datagrams with the production wire format.
func (s *Server) decode(wire []byte) (moshcore.TransportInstruction, []byte, []int, bool) {
	if len(wire) < 24 {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	directionSequence := binary.BigEndian.Uint64(wire[:8])
	if directionSequence&(1<<63) != 0 {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], directionSequence)
	plaintext, ok := s.ocb.Decrypt(nonce[:], wire[8:])
	if !ok || len(plaintext) < 14 {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	s.sendMu.Lock()
	s.lastClientTS = uint32(binary.BigEndian.Uint16(plaintext[:2]))
	s.sendMu.Unlock()
	header := plaintext[4:]
	id := binary.BigEndian.Uint64(header[:8])
	combined := binary.BigEndian.Uint16(header[8:10])
	number := combined & 0x7fff
	final := combined&0x8000 != 0
	content := header[10:]
	if !s.haveID || s.assemblerID != id {
		s.haveID = true
		s.assemblerID = id
		s.parts = nil
		s.present = nil
		s.arrived = 0
		s.total = -1
	}
	index := int(number)
	for len(s.parts) <= index {
		s.parts = append(s.parts, nil)
		s.present = append(s.present, false)
	}
	if !s.present[index] {
		s.parts[index] = append([]byte(nil), content...)
		s.present[index] = true
		s.arrived++
	}
	if final {
		s.total = index + 1
	}
	if s.total < 0 || s.arrived != s.total {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	var joined []byte
	for i := 0; i < s.total; i++ {
		joined = append(joined, s.parts[i]...)
	}
	s.parts = nil
	s.present = nil
	s.arrived = 0
	s.total = -1
	reader, err := zlib.NewReader(bytes.NewReader(joined))
	if err != nil {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	_ = reader.Close()
	if err != nil {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	instruction, err := moshcore.UnmarshalTransportInstruction(raw)
	if err != nil {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	userInstructions, err := moshcore.UnmarshalUserMessage(instruction.Diff)
	if err != nil {
		return moshcore.TransportInstruction{}, nil, nil, false
	}
	var keys []byte
	var resize []int
	for _, user := range userInstructions {
		keys = append(keys, user.Keys...)
		if user.Resize {
			resize = []int{int(user.Width), int(user.Height)}
		}
	}
	return instruction, keys, resize, true
}
