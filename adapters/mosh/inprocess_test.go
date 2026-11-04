// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	moshcore "github.com/mslxl/natcatty/internal/mosh"
)

// bannerBootstrap is an SSH bootstrap fixture that emits a fixed banner.
type bannerBootstrap struct {
	banner string
	err    error
}

func (b bannerBootstrap) OpenBootstrap(context.Context, appsession.StartSpec, moshmodel.Config) (io.ReadWriteCloser, error) {
	if b.err != nil {
		return nil, b.err
	}
	return &bufferStream{Reader: strings.NewReader(b.banner)}, nil
}

type bufferStream struct{ *strings.Reader }

func (s *bufferStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *bufferStream) Close() error                { return nil }

func inProcessTestSpec() appsession.StartSpec {
	return appsession.StartSpec{
		Kind:     appsession.KindMosh,
		Hostname: "127.0.0.1",
		Cols:     80,
		Rows:     24,
		Mosh: &moshmodel.Config{
			Hostname: "127.0.0.1",
			Port:     22,
			Cols:     80,
			Rows:     24,
		},
	}
}

// fakeServer is a minimal mosh server for adapter lifecycle tests. It speaks
// the real wire format (OCB3, fragments, zlib, protobuf) but only implements
// the behaviors these tests exercise.
type fakeServer struct {
	t    *testing.T
	conn *net.UDPConn
	ocb  *moshcore.OCB
	key  []byte

	// sendMu guards the mutable send path: concurrent injected datagrams
	// (protocol-error and roaming tests) must not race the receive loop's
	// replies.
	sendMu     sync.Mutex
	seq        uint64
	fragID     uint64
	clientAddr *net.UDPAddr
	lastHost   *moshcore.TransportInstruction
	// lastClientTS is the most recent client timestamp, echoed in replies so
	// the client can sample RTT.
	lastClientTS uint32

	assemblerID uint64
	haveID      bool
	parts       [][]byte
	present     []bool
	arrived     int
	total       int

	seen            map[uint64]bool
	clientAckNum    uint64
	lastHostNum     uint64
	inputBuffer     []byte
	remoteGone      bool
	protocol        uint32
	input           chan []byte
	resizes         chan [2]int
	shutdown        chan struct{}
	shutdownAck     chan struct{}
	badProtocol     chan uint32
	stop            chan struct{}
	closeOnce       sync.Once
	wg              sync.WaitGroup
	firstResponse   bool
	shutdownOnFirst bool
}

func startFakeServer(t *testing.T) *fakeServer {
	return startFakeServerWithProtocol(t, 2)
}

func startFakeServerShutdownOnFirst(t *testing.T) *fakeServer {
	return startFakeServerWithOptions(t, 2, true)
}

func startFakeServerWithProtocol(t *testing.T, protocol uint32) *fakeServer {
	return startFakeServerWithOptions(t, protocol, false)
}

func startFakeServerWithOptions(t *testing.T, protocol uint32, shutdownOnFirst bool) *fakeServer {
	t.Helper()
	key := bytes.Repeat([]byte{0x33}, 16)
	ocb, err := moshcore.NewOCB(key)
	if err != nil {
		t.Fatalf("NewOCB: %v", err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	server := &fakeServer{
		t:               t,
		conn:            conn,
		ocb:             ocb,
		key:             key,
		seen:            make(map[uint64]bool),
		protocol:        protocol,
		input:           make(chan []byte, 32),
		resizes:         make(chan [2]int, 8),
		shutdown:        make(chan struct{}, 1),
		shutdownAck:     make(chan struct{}, 1),
		badProtocol:     make(chan uint32, 1),
		stop:            make(chan struct{}),
		firstResponse:   true,
		shutdownOnFirst: shutdownOnFirst,
	}
	server.wg.Add(2)
	go server.loop()
	go server.injectLoop()
	t.Cleanup(server.Close)
	return server
}

// injectLoop sends test-triggered datagrams from the server side without
// racing the receive loop.
func (s *fakeServer) injectLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			return
		case version := <-s.badProtocol:
			s.send(moshcore.TransportInstruction{ProtocolVersion: version, OldNum: 0, NewNum: 1})
		}
	}
}

// sendProtocolError asks the fixture to send an authenticated instruction
// with an unsupported protocol version. The client must classify it as a fatal
// protocol failure.
func (s *fakeServer) sendProtocolError(version uint32) {
	select {
	case s.badProtocol <- version:
	case <-s.stop:
	}
}

func (s *fakeServer) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.conn.Close()
		// Wait for the receive loops before zeroizing the cipher: a datagram
		// already read may still be in flight through send().
		s.wg.Wait()
		s.ocb.Close()
	})
}

func (s *fakeServer) Port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

// clientAddress returns the source address of the most recent client datagram.
func (s *fakeServer) clientAddress() *net.UDPAddr {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.clientAddr == nil {
		return nil
	}
	clone := *s.clientAddr
	return &clone
}

func (s *fakeServer) announcement() string { return s.announcementOn(s.Port()) }

// announcementOn builds an announcement for a relay in front of the server.
func (s *fakeServer) announcementOn(port int) string {
	return "MOSH CONNECT " + strconv.Itoa(port) + " " +
		base64.RawStdEncoding.EncodeToString(s.key) + "\r\n"
}

func (s *fakeServer) loop() {
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
		if s.shutdownOnFirst && !s.remoteGone {
			// The remote shell exits before the probe completes; mosh-server
			// sends its shutdown sentinel as the first host state.
			s.remoteGone = true
			s.sendMu.Lock()
			s.clientAckNum = instruction.NewNum
			s.sendMu.Unlock()
			s.sendHostState([]byte("bye\r\n"), moshcore.ShutdownNum)
			continue
		}
		if instruction.NewNum == moshcore.ShutdownNum {
			select {
			case s.shutdown <- struct{}{}:
			default:
			}
			oldNum := s.lastHostNum
			s.lastHostNum++
			s.send(moshcore.TransportInstruction{
				ProtocolVersion: s.protocol,
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
			// A retransmitted client state means the peer missed our last
			// host state (or its ack). Stock's sender resends from the last
			// acknowledged state; the fixture only ever has one host state
			// outstanding, so resending it is equivalent here.
			s.resendLastHost()
			continue
		}
		s.seen[instruction.NewNum] = true
		s.sendMu.Lock()
		if instruction.NewNum > s.clientAckNum {
			s.clientAckNum = instruction.NewNum
		}
		s.sendMu.Unlock()
		if len(keys) > 0 {
			s.inputBuffer = append(s.inputBuffer, keys...)
			select {
			case s.input <- keys:
			case <-s.stop:
				return
			}
		}
		if resize != nil {
			s.resizes <- [2]int{resize[0], resize[1]}
		}
		if len(keys) > 0 && strings.Contains(string(s.inputBuffer), "exit") && !s.remoteGone {
			s.remoteGone = true
			s.sendHostState([]byte("bye\r\n"), moshcore.ShutdownNum)
			continue
		}
		if s.firstResponse || len(keys) > 0 || resize != nil {
			output := []byte("fake$ ")
			if strings.Contains(string(s.inputBuffer), "echo hi") && len(keys) > 0 {
				output = []byte("hi\r\n")
			}
			s.firstResponse = false
			s.sendHostState(output, 0)
		}
	}
}

// sendHostState sends one host instruction. newNum 0 means "advance the host
// state"; ShutdownNum sends the sentinel state.
func (s *fakeServer) sendHostState(output []byte, newNum uint64) {
	var diff []byte
	if len(output) > 0 {
		diff = moshcore.MarshalHostMessage([]moshcore.HostInstruction{{HostString: output}})
	}
	if newNum == 0 {
		newNum = s.lastHostNum + 1
	}
	instruction := moshcore.TransportInstruction{
		ProtocolVersion: s.protocol,
		OldNum:          s.lastHostNum,
		NewNum:          newNum,
		AckNum:          s.clientAckNum,
		Diff:            diff,
	}
	if newNum != moshcore.ShutdownNum {
		s.lastHostNum = newNum
	}
	s.send(instruction)
}

func (s *fakeServer) send(instruction moshcore.TransportInstruction) {
	s.t.Helper()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	// Remember the last host instruction so a retransmitted client state
	// triggers the same recovery a stock sender would provide.
	stored := instruction
	stored.Diff = append([]byte(nil), instruction.Diff...)
	s.lastHost = &stored
	if s.clientAddr == nil {
		return
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(instruction.Marshal()); err != nil {
		s.t.Errorf("zlib write: %v", err)
		return
	}
	if err := writer.Close(); err != nil {
		s.t.Errorf("zlib close: %v", err)
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
			s.t.Errorf("Encrypt: %v", err)
			return
		}
		wire := make([]byte, 8+len(ciphertext))
		binary.BigEndian.PutUint64(wire[:8], 1<<63|sequence)
		copy(wire[8:], ciphertext)
		if _, err := s.conn.WriteToUDP(wire, s.clientAddr); err != nil {
			select {
			case <-s.stop:
			default:
				s.t.Logf("fake server write: %v", err)
			}
			return
		}
		number++
	}
}

// resendLastHost resends the most recent host instruction with fresh sequence
// and fragment numbers.
func (s *fakeServer) resendLastHost() {
	s.sendMu.Lock()
	instruction := s.lastHost
	s.sendMu.Unlock()
	if instruction != nil {
		s.send(*instruction)
	}
}

// decode parses client datagrams with the production wire format.
func (s *fakeServer) decode(wire []byte) (moshcore.TransportInstruction, []byte, []int, bool) {
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

// readUntil reads transport output until it contains marker or the read fails.
func readUntil(t *testing.T, transport appsession.Transport, marker string, timeout time.Duration) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		var collected bytes.Buffer
		buffer := make([]byte, 4096)
		for {
			n, err := transport.Read(buffer)
			if n > 0 {
				collected.Write(buffer[:n])
				if strings.Contains(collected.String(), marker) {
					result <- collected.String()
					return
				}
			}
			if err != nil {
				result <- collected.String()
				return
			}
		}
	}()
	select {
	case text := <-result:
		if !strings.Contains(text, marker) {
			t.Fatalf("output %q does not contain %q", text, marker)
		}
		return text
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %q", marker)
		return ""
	}
}

func waitInput(t *testing.T, server *fakeServer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	var collected []byte
	for !strings.Contains(string(collected), want) {
		select {
		case chunk := <-server.input:
			collected = append(collected, chunk...)
		case <-deadline:
			t.Fatalf("timed out waiting for input %q, got %q", want, collected)
		}
	}
}

func TestInProcessAdapterLifecycle(t *testing.T) {
	server := startFakeServer(t)
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcement()},
		ProbeTimeout: 500 * time.Millisecond,
	}
	transport, err := factory.Open(context.Background(), inProcessTestSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	first := readUntil(t, transport, "fake$ ", 2*time.Second)
	if reset, prompt := strings.Index(first, viewportReset), strings.Index(first, "fake$ "); reset < 0 || reset > prompt {
		t.Fatalf("first output %q does not reset the viewport before the first frame", first)
	}
	if _, err := transport.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitInput(t, server, "echo hi", time.Second)
	readUntil(t, transport, "hi\r\n", 2*time.Second)

	if err := transport.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case size := <-server.resizes:
			if size[0] == 120 && size[1] == 40 {
				goto resized
			}
		case <-deadline:
			t.Fatal("timed out waiting for resize")
		}
	}
resized:

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-server.shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("mosh-server never saw the shutdown sentinel")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
}

func TestInProcessAdapterRemoteExit(t *testing.T) {
	server := startFakeServer(t)
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcement()},
		ProbeTimeout: 500 * time.Millisecond,
	}
	transport, err := factory.Open(context.Background(), inProcessTestSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	readUntil(t, transport, "fake$ ", 2*time.Second)

	if _, err := transport.Write([]byte("exit\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	readUntil(t, transport, "bye\r\n", 2*time.Second)

	waiter, ok := transport.(appsession.ExitWaiter)
	if !ok {
		t.Fatal("in-process transport does not report Wait")
	}
	wait := make(chan struct{})
	go func() {
		_, _ = waiter.Wait()
		close(wait)
	}()
	select {
	case <-wait:
	case <-time.After(2 * time.Second):
		t.Fatal("transport did not end after the server's shutdown sentinel")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want exited", end)
	}
	select {
	case <-server.shutdownAck:
	case <-time.After(time.Second):
		t.Fatal("the client never acknowledged the server's shutdown sentinel")
	}
}

func TestInProcessAdapterFallsBackFromAnnouncedAddress(t *testing.T) {
	server := startFakeServer(t)
	banner := "MOSH IP 192.0.2.1\r\n" + server.announcement()
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: banner},
		ProbeTimeout: 150 * time.Millisecond,
	}
	transport, err := factory.Open(context.Background(), inProcessTestSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	readUntil(t, transport, "fake$ ", 3*time.Second)
}

func TestInProcessAdapterAnnouncementErrors(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))

	t.Run("missing announcement", func(t *testing.T) {
		factory := InProcessFactory{Bootstrap: bannerBootstrap{banner: "Welcome to the server\r\n"}, ProbeTimeout: 100 * time.Millisecond}
		_, err := factory.Open(context.Background(), inProcessTestSpec())
		if !errors.Is(err, moshmodel.ErrHandshake) {
			t.Fatalf("error = %v, want ErrHandshake", err)
		}
	})

	t.Run("invalid port", func(t *testing.T) {
		factory := InProcessFactory{
			Bootstrap:    bannerBootstrap{banner: "MOSH CONNECT 70000 " + key + "\r\n"},
			ProbeTimeout: 100 * time.Millisecond,
		}
		_, err := factory.Open(context.Background(), inProcessTestSpec())
		if !errors.Is(err, moshmodel.ErrAnnouncement) {
			t.Fatalf("error = %v, want ErrAnnouncement", err)
		}
	})

	t.Run("no server", func(t *testing.T) {
		listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatalf("ListenUDP: %v", err)
		}
		port := listener.LocalAddr().(*net.UDPAddr).Port
		_ = listener.Close()
		banner := "MOSH CONNECT " + strconv.Itoa(port) + " " + key + "\r\n"
		factory := InProcessFactory{Bootstrap: bannerBootstrap{banner: banner}, ProbeTimeout: 150 * time.Millisecond}
		_, err = factory.Open(context.Background(), inProcessTestSpec())
		if !errors.Is(err, moshmodel.ErrTimeout) {
			t.Fatalf("error = %v, want ErrTimeout", err)
		}
	})
}

func TestInProcessAdapterProtocolError(t *testing.T) {
	server := startFakeServerWithProtocol(t, 3)
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcement()},
		ProbeTimeout: 500 * time.Millisecond,
	}
	_, err := factory.Open(context.Background(), inProcessTestSpec())
	if !errors.Is(err, moshmodel.ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
}
