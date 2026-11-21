// SPDX-License-Identifier: GPL-3.0-or-later

// Package ettest exposes the in-process EternalTerminal server fixture to
// packages outside adapters/et. The server implementation deliberately lives
// under internal/etgo so only the adapter may depend on its protocol details;
// this package is the small, stable surface an application test needs: a
// running etserver, the forwarding dial the SSH fixture returns, start
// handshake answers, and terminal output.
package ettest

import (
	"context"
	"net"

	"google.golang.org/protobuf/proto"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
)

// Server is one in-process etserver plus the in-memory network its clients
// dial instead of a real TCP socket.
type Server struct {
	server  *etservertest.Server
	network *etservertest.Network
}

// New starts a server that accepts id and passkey. passkey must be 32 bytes.
func New(id, passkey string) *Server {
	server := etservertest.NewServer(id, passkey)
	return &Server{server: server, network: etservertest.NewNetwork(server)}
}

// Close cuts every connection and stops the fixture.
func (s *Server) Close() {
	s.network.Close()
}

// DialContext connects a client to the server. fakessh's ForwardDial uses it
// to serve the ET socket over the SSH direct-tcpip channel.
func (s *Server) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return s.network.DialContext(ctx, network, address)
}

// AnswerStart answers the fresh connect's INITIAL_PAYLOAD with an empty
// INITIAL_RESPONSE, which lets the adapter finish its start handshake. A
// reattach sends no INITIAL_PAYLOAD; the goroutine then waits until ctx ends.
func (s *Server) AnswerStart(ctx context.Context) {
	go func() {
		for {
			packet, err := s.server.Recv(ctx)
			if err != nil {
				return
			}
			if packet.Header != protocol.HeaderInitialPayload {
				continue
			}
			encoded, err := proto.Marshal(&protocol.InitialResponse{})
			if err != nil {
				return
			}
			_ = s.server.Send(ctx, protocol.Packet{
				Header: protocol.HeaderInitialResponse, Payload: encoded,
			})
			return
		}
	}()
}

// SendText delivers one terminal buffer to the connected client, as a running
// remote program would.
func (s *Server) SendText(ctx context.Context, text string) error {
	var buffer protocol.TerminalBuffer
	buffer.SetBuffer([]byte(text))
	payload, err := proto.Marshal(&buffer)
	if err != nil {
		return err
	}
	return s.server.Send(ctx, protocol.Packet{
		Header: protocol.HeaderTerminalBuffer, Payload: payload,
	})
}

// EndSession makes the server reject the saved credentials, as it would after
// the remote shell exited while the app was closed.
func (s *Server) EndSession() { s.server.EndSession() }
