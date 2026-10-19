// Package fakessh is an in-process SSH server fixture for adapter tests. It
// runs over any net.Conn, including net.Pipe, so tests exercise the real
// x/crypto/ssh handshake without a network listener, Docker, or credentials.
package fakessh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
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
	// PublicKeys are the authorized user keys. The server offers publickey
	// authentication only when this list or CertificateAuthority is set.
	PublicKeys []ssh.PublicKey
	// CertificateAuthority accepts user certificates signed by this key.
	CertificateAuthority ssh.PublicKey
	// KeyboardInteractive, when set, handles challenge rounds. The fixture
	// records every round before and after the handler answers.
	KeyboardInteractive func(conn ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error)
	// PasswordThenKeyboardInteractive makes a correct password a first factor
	// and requires keyboard-interactive as the second, modelling MFA.
	PasswordThenKeyboardInteractive bool
	// Ciphers, KeyExchanges, and MACs override the server's offer so a test
	// can force a negotiation failure.
	Ciphers      []string
	KeyExchanges []string
	MACs         []string
	// ForwardDial answers direct-tcpip channel requests. It lets a fixture
	// play the next hop in a jump chain. Without it, forwarding is rejected.
	ForwardDial func(host string, port int) (net.Conn, error)
	// IgnoreRemoteForwardCancel leaves a cancel-tcpip-forward request
	// unanswered, modelling a server whose unforward hangs. The fixture then
	// closes the listener when the server itself closes.
	IgnoreRemoteForwardCancel bool
	// AcceptX11 accepts x11-req requests. The default rejects them, matching a
	// server with X11Forwarding disabled.
	AcceptX11 bool
}

// X11Request is one recorded x11-req session request.
type X11Request struct {
	SingleConnection bool
	Protocol         string
	Cookie           string
	ScreenNumber     uint32
}

// RemoteForward is one recorded tcpip-forward request.
type RemoteForward struct {
	Addr string
	Port int
}

// Forward is one recorded direct-tcpip request.
type Forward struct {
	Host       string
	Port       int
	OriginHost string
	OriginPort int
}

// KeyboardInteractiveRound is one recorded challenge and its answers.
type KeyboardInteractiveRound struct {
	Name        string
	Instruction string
	Questions   []string
	Echos       []bool
	Answers     []string
	Err         string
}

// Server is one fixture SSH server on one connection.
type Server struct {
	conn        net.Conn
	config      *ssh.ServerConfig
	signer      ssh.Signer
	done        chan struct{}
	closeOnce   sync.Once
	forwardDial func(host string, port int) (net.Conn, error)

	serverConn        *ssh.ServerConn
	mu                sync.Mutex
	acceptX11         bool
	x11Requests       []X11Request
	passwordAttempts  int
	lastPassword      string
	lastUsername      string
	publicKeyAttempts int
	lastPublicKeyType string
	lastPublicKeyID   string
	kiRounds          []KeyboardInteractiveRound
	handshakeErr      error
	pty               *Size
	resizes           []Size
	shellOpened       bool
	forwards          []Forward
	remoteForwards    []RemoteForward
	remoteListeners   map[string]net.Listener
	ignoreCancel      bool
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

	server := &Server{
		conn:            conn,
		signer:          signer,
		done:            make(chan struct{}),
		forwardDial:     options.ForwardDial,
		remoteListeners: map[string]net.Listener{},
		ignoreCancel:    options.IgnoreRemoteForwardCancel,
		acceptX11:       options.AcceptX11,
	}

	var keyboardInteractive func(ssh.ConnMetadata, ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error)
	if options.KeyboardInteractive != nil {
		keyboardInteractive = func(metadata ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			recorded := func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers, err := challenge(name, instruction, questions, echos)
				round := KeyboardInteractiveRound{
					Name:        name,
					Instruction: instruction,
					Questions:   append([]string(nil), questions...),
					Echos:       append([]bool(nil), echos...),
					Answers:     append([]string(nil), answers...),
				}
				if err != nil {
					round.Err = err.Error()
				}
				server.mu.Lock()
				server.kiRounds = append(server.kiRounds, round)
				server.mu.Unlock()
				return answers, err
			}
			return options.KeyboardInteractive(metadata, recorded)
		}
	}

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
			if options.PasswordThenKeyboardInteractive && keyboardInteractive != nil {
				return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{
					KeyboardInteractiveCallback: keyboardInteractive,
				}}
			}
			return nil, nil
		},
		KeyboardInteractiveCallback: keyboardInteractive,
	}
	if len(options.PublicKeys) > 0 || options.CertificateAuthority != nil {
		authority := options.CertificateAuthority
		checker := &ssh.CertChecker{
			IsUserAuthority: func(auth ssh.PublicKey) bool {
				return authority != nil && bytes.Equal(auth.Marshal(), authority.Marshal())
			},
		}
		server.config.PublicKeyCallback = func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			server.mu.Lock()
			server.publicKeyAttempts++
			server.lastPublicKeyType = key.Type()
			server.lastPublicKeyID = ""
			if certificate, ok := key.(*ssh.Certificate); ok {
				server.lastPublicKeyID = certificate.KeyId
			}
			server.mu.Unlock()

			if certificate, ok := key.(*ssh.Certificate); ok {
				if authority == nil {
					return nil, errors.New("fakessh: certificate rejected")
				}
				return checker.Authenticate(metadata, certificate)
			}
			for _, allowed := range options.PublicKeys {
				if bytes.Equal(allowed.Marshal(), key.Marshal()) {
					return nil, nil
				}
			}
			return nil, errors.New("fakessh: public key rejected")
		}
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

// PublicKeyAttempts reports how many publickey offers the server saw,
// including certificate offers.
func (s *Server) PublicKeyAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publicKeyAttempts
}

// LastPublicKey reports the most recent offered key's type and, for a
// certificate, its KeyId.
func (s *Server) LastPublicKey() (keyType, keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPublicKeyType, s.lastPublicKeyID
}

// KeyboardInteractiveRounds returns every challenge the server issued in
// order.
func (s *Server) KeyboardInteractiveRounds() []KeyboardInteractiveRound {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]KeyboardInteractiveRound(nil), s.kiRounds...)
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

// Forwards reports every direct-tcpip request the server accepted or
// rejected, in order.
func (s *Server) Forwards() []Forward {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Forward(nil), s.forwards...)
}

// RemoteForwards reports every accepted tcpip-forward request in order.
func (s *Server) RemoteForwards() []RemoteForward {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RemoteForward(nil), s.remoteForwards...)
}

// X11Requests reports every x11-req session request in order.
func (s *Server) X11Requests() []X11Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]X11Request(nil), s.x11Requests...)
}

// OpenX11 opens a server-to-client x11 channel, standing in for a remote X11
// client that connected to the forwarded display. The caller must wait until
// the handshake and the x11-req have completed.
func (s *Server) OpenX11() (ssh.Channel, error) {
	s.mu.Lock()
	conn := s.serverConn
	s.mu.Unlock()
	if conn == nil {
		return nil, errors.New("fakessh: server is not ready")
	}
	payload := ssh.Marshal(struct {
		OriginHost string
		OriginPort uint32
	}{"127.0.0.1", 1234})
	channel, requests, err := conn.OpenChannel("x11", payload)
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(requests)
	return channel, nil
}

// Err reports the handshake error, if any. It is valid after Wait returns.
func (s *Server) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshakeErr
}

// Wait blocks until the server's channel loop ends.
func (s *Server) Wait() { <-s.done }

// Close closes every remote listener and the server connection.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.closeRemoteListeners()
		err = s.conn.Close()
	})
	return err
}

func (s *Server) closeRemoteListeners() {
	s.mu.Lock()
	listeners := make([]net.Listener, 0, len(s.remoteListeners))
	for key, listener := range s.remoteListeners {
		listeners = append(listeners, listener)
		delete(s.remoteListeners, key)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		_ = listener.Close()
	}
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
	s.mu.Lock()
	s.serverConn = serverConn
	s.mu.Unlock()
	go s.handleGlobalRequests(serverConn, requests)
	for incoming := range channels {
		switch incoming.ChannelType() {
		case "session":
			channel, channelRequests, err := incoming.Accept()
			if err != nil {
				continue
			}
			go s.handleSession(channel, channelRequests)
		case "direct-tcpip":
			s.handleDirectTCPIP(incoming)
		default:
			_ = incoming.Reject(ssh.UnknownChannelType, "only session and direct-tcpip channels are supported")
		}
	}
	s.closeRemoteListeners()
	_ = serverConn.Close()
}

// handleGlobalRequests serves tcpip-forward and cancel-tcpip-forward, which are
// the server side of remote Port Forwarding Rules. Other global requests are
// declined.
func (s *Server) handleGlobalRequests(conn *ssh.ServerConn, requests <-chan *ssh.Request) {
	for request := range requests {
		switch request.Type {
		case "tcpip-forward":
			var payload struct {
				Addr string
				Port uint32
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
				request.Reply(false, nil)
				continue
			}
			listener, err := net.Listen("tcp", net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port))))
			if err != nil {
				request.Reply(false, nil)
				continue
			}
			assigned := payload.Port
			if address, ok := listener.Addr().(*net.TCPAddr); ok {
				assigned = uint32(address.Port)
			}
			key := net.JoinHostPort(payload.Addr, strconv.Itoa(int(assigned)))
			s.mu.Lock()
			s.remoteListeners[key] = listener
			s.remoteForwards = append(s.remoteForwards, RemoteForward{Addr: payload.Addr, Port: int(assigned)})
			s.mu.Unlock()

			var reply []byte
			if payload.Port == 0 {
				reply = ssh.Marshal(struct{ Port uint32 }{assigned})
			}
			request.Reply(true, reply)
			go s.acceptRemoteForward(conn, key, listener)
		case "cancel-tcpip-forward":
			if s.ignoreCancel {
				// Deliberately leave the request unanswered so a client-side
				// unforward timeout can be exercised.
				continue
			}
			var payload struct {
				Addr string
				Port uint32
			}
			if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
				request.Reply(false, nil)
				continue
			}
			key := net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port)))
			s.mu.Lock()
			listener := s.remoteListeners[key]
			delete(s.remoteListeners, key)
			s.mu.Unlock()
			if listener != nil {
				_ = listener.Close()
			}
			request.Reply(true, nil)
		default:
			request.Reply(false, nil)
		}
	}
}

// acceptRemoteForward turns each connection to the server-side listener into
// a forwarded-tcpip channel back to the client.
func (s *Server) acceptRemoteForward(conn *ssh.ServerConn, key string, listener net.Listener) {
	host, portText, err := net.SplitHostPort(key)
	if err != nil {
		return
	}
	port, _ := strconv.Atoi(portText)
	for {
		incoming, err := listener.Accept()
		if err != nil {
			return
		}
		originHost, originPort := remoteAddress(incoming.RemoteAddr())
		payload := ssh.Marshal(struct {
			Addr       string
			Port       uint32
			OriginHost string
			OriginPort uint32
		}{host, uint32(port), originHost, uint32(originPort)})
		channel, requests, err := conn.OpenChannel("forwarded-tcpip", payload)
		if err != nil {
			_ = incoming.Close()
			continue
		}
		go ssh.DiscardRequests(requests)
		go proxyStream(channel, incoming)
	}
}

func remoteAddress(address net.Addr) (string, int) {
	if tcp, ok := address.(*net.TCPAddr); ok {
		return tcp.IP.String(), tcp.Port
	}
	host, portText, err := net.SplitHostPort(address.String())
	if err != nil {
		return address.String(), 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
}

// handleDirectTCPIP serves one forwarding request by dialing the requested
// endpoint and copying bytes between it and the channel.
func (s *Server) handleDirectTCPIP(incoming ssh.NewChannel) {
	var payload struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(incoming.ExtraData(), &payload); err != nil {
		_ = incoming.Reject(ssh.Prohibited, "malformed direct-tcpip request")
		return
	}
	forward := Forward{
		Host: payload.Host, Port: int(payload.Port),
		OriginHost: payload.OriginHost, OriginPort: int(payload.OriginPort),
	}
	s.mu.Lock()
	s.forwards = append(s.forwards, forward)
	dial := s.forwardDial
	s.mu.Unlock()

	if dial == nil {
		_ = incoming.Reject(ssh.Prohibited, "forwarding disabled")
		return
	}
	target, err := dial(payload.Host, int(payload.Port))
	if err != nil {
		_ = incoming.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	channel, requests, err := incoming.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	go proxyStream(channel, target)
}

func proxyStream(channel ssh.Channel, target net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(channel, target)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(target, channel)
		done <- struct{}{}
	}()
	<-done
	_ = channel.Close()
	_ = target.Close()
	<-done
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
		case "x11-req":
			var payload struct {
				SingleConnection bool
				Protocol         string
				Cookie           string
				ScreenNumber     uint32
			}
			record := ssh.Unmarshal(request.Payload, &payload) == nil
			s.mu.Lock()
			accepted := record && s.acceptX11
			if record {
				s.x11Requests = append(s.x11Requests, X11Request{
					SingleConnection: payload.SingleConnection,
					Protocol:         payload.Protocol,
					Cookie:           payload.Cookie,
					ScreenNumber:     payload.ScreenNumber,
				})
			}
			s.mu.Unlock()
			request.Reply(accepted, nil)
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
