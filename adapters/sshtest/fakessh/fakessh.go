// Package fakessh is an in-process SSH server fixture for adapter tests. It
// runs over any net.Conn, including net.Pipe, so tests exercise the real
// x/crypto/ssh handshake without a network listener, Docker, or credentials.
package fakessh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"

	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// Size is a PTY window size.
type Size struct {
	Cols int
	Rows int
}

// Options configures a fixture server.
type Options struct {
	// Password is the only accepted password. An empty password accepts any.
	Password string
	// Ciphers, KeyExchanges, and MACs override the server's offer so a test
	// can force a negotiation failure.
	Ciphers      []string
	KeyExchanges []string
	MACs         []string
}

// Server is one fixture SSH server on one connection.
type Server struct {
	conn      net.Conn
	config    *ssh.ServerConfig
	signer    ssh.Signer
	done      chan struct{}
	closeOnce sync.Once

	mu               sync.Mutex
	passwordAttempts int
	lastPassword     string
	lastUsername     string
	handshakeErr     error
	pty              *Size
	resizes          []Size
	shellOpened      bool
}

// Start configures and handshakes a server on conn in the background. It
// returns before the handshake completes so a synchronous transport such as
// net.Pipe's client end can dial.
func Start(conn net.Conn, options Options) (*Server, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, err
	}

	server := &Server{conn: conn, signer: signer, done: make(chan struct{})}
	server.config = &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			server.mu.Lock()
			server.passwordAttempts++
			server.lastUsername = metadata.User()
			server.lastPassword = string(password)
			server.mu.Unlock()
			if options.Password != "" && string(password) != options.Password {
				return nil, errors.New("fakessh: password rejected")
			}
			return nil, nil
		},
	}
	server.config.AddHostKey(signer)
	server.config.Ciphers = options.Ciphers
	server.config.KeyExchanges = options.KeyExchanges
	server.config.MACs = options.MACs

	go server.serve()
	return server, nil
}

// Fingerprint is the server host key's SHA-256 fingerprint.
func (s *Server) Fingerprint() string {
	return domainconn.Fingerprint(s.signer.PublicKey().Marshal())
}

// KeyType is the server host key algorithm.
func (s *Server) KeyType() string { return s.signer.PublicKey().Type() }

// PublicKey is the server host key in "keytype base64" form.
func (s *Server) PublicKey() string {
	key := s.signer.PublicKey()
	return domainconn.PublicKey(key.Type(), key.Marshal())
}

// PasswordAttempts reports how many password authentication attempts the
// server saw.
func (s *Server) PasswordAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passwordAttempts
}

// LastCredentials reports the most recent password attempt.
func (s *Server) LastCredentials() (username, password string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsername, s.lastPassword
}

// Pty reports the most recent PTY request.
func (s *Server) Pty() (Size, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pty == nil {
		return Size{}, false
	}
	return *s.pty, true
}

// Resizes reports every window-change request in order.
func (s *Server) Resizes() []Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Size(nil), s.resizes...)
}

// ShellOpened reports whether the client requested an interactive shell.
func (s *Server) ShellOpened() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shellOpened
}

// Err reports the handshake error, if any. It is valid after Wait returns.
func (s *Server) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshakeErr
}

// Wait blocks until the server's channel loop ends.
func (s *Server) Wait() { <-s.done }

// Close closes the server connection.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.conn.Close()
	})
	return err
}

func (s *Server) serve() {
	defer close(s.done)
	serverConn, channels, requests, err := ssh.NewServerConn(s.conn, s.config)
	if err != nil {
		s.mu.Lock()
		s.handshakeErr = err
		s.mu.Unlock()
		return
	}
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			_ = incoming.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, channelRequests, err := incoming.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(channel, channelRequests)
	}
	_ = serverConn.Close()
}

func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	for request := range requests {
		switch request.Type {
		case "pty-req":
			var payload struct {
				Term          string
				Cols, Rows    uint32
				Width, Height uint32
				Modes         string
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err == nil {
				s.mu.Lock()
				s.pty = &Size{Cols: int(payload.Cols), Rows: int(payload.Rows)}
				s.mu.Unlock()
			}
			request.Reply(true, nil)
		case "window-change":
			var payload struct {
				Cols, Rows    uint32
				Width, Height uint32
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err == nil {
				s.mu.Lock()
				s.resizes = append(s.resizes, Size{Cols: int(payload.Cols), Rows: int(payload.Rows)})
				s.mu.Unlock()
			}
			request.Reply(true, nil)
		case "shell":
			s.mu.Lock()
			s.shellOpened = true
			s.mu.Unlock()
			request.Reply(true, nil)
			go echo(channel)
		default:
			request.Reply(false, nil)
		}
	}
	_ = channel.Close()
}

// echo writes everything received back, then reports a clean exit.
func echo(channel ssh.Channel) {
	buffer := make([]byte, 4096)
	for {
		n, err := channel.Read(buffer)
		if n > 0 {
			if _, writeErr := channel.Write(buffer[:n]); writeErr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	_ = channel.Close()
}

// Pipe is defined in pipe.go. It returns a buffered, in-process client/server
// pair for a fixture.
