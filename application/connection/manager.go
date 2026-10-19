// Package connection owns authenticated SSH connections and the leases that
// Sessions and later features borrow from them. It declares the dial,
// credential, Known Host, and trust-prompt ports it consumes, so the SSH
// adapter and SQLite store implement them without this package knowing about
// x/crypto/ssh or storage.
//
// A Connection is one authenticated transport. A Lease is a counted claim on
// it. Ticket 06 dials one Connection per Session; the refcount and the
// Manager seam are the extension points for the shared pool and jump chains
// that later tickets add.
package connection

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// Request describes an endpoint to connect to. Credentials are deliberately
// absent: the Manager reads the saved password through PasswordSource.
type Request struct {
	HostID   model.HostID
	Hostname string
	Port     int
	Username string
}

// DialRequest is what the SSH adapter needs to dial one endpoint. VerifyHostKey
// runs during the handshake, before authentication; returning an error aborts
// the connection. Its context is cancelled when the handshake deadline
// expires, so a queued trust prompt cannot outlive the connection attempt.
type DialRequest struct {
	Hostname      string
	Port          int
	Username      string
	Password      string
	VerifyHostKey func(ctx context.Context, key domainconn.Key) error
}

// TerminalSpec is one interactive shell's initial terminal state.
type TerminalSpec struct {
	Cols int
	Rows int
	Term string
}

// Terminal is an interactive shell channel. Read is output, Write is input,
// Resize changes the remote PTY size, and Close hangs up the channel.
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// Client is an authenticated SSH transport. The adapter owns the concrete
// client; the application only opens channels and closes it.
type Client interface {
	OpenTerminal(ctx context.Context, spec TerminalSpec) (Terminal, error)
	Close() error
}

// Dialer opens an authenticated transport. Tests provide a fake; the app
// wires adapters/ssh.
type Dialer interface {
	Dial(ctx context.Context, req DialRequest) (Client, error)
}

// PasswordSource reads a Host's saved password. The app bridges the Vault
// secret service; tests provide a fake. The Manager never writes the value
// anywhere.
type PasswordSource interface {
	Password(ctx context.Context, hostID model.HostID) (string, error)
}

// KnownHostRepository persists Known Hosts. The app wires adapters/store;
// tests provide a fake.
type KnownHostRepository interface {
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
	UpsertKnownHost(ctx context.Context, known domainconn.KnownHost) error
}

// PromptDecision is the user's answer to a host-key prompt.
type PromptDecision struct {
	// Trust accepts the key for this connection. False rejects it.
	Trust bool
	// Persist records the key as a Known Host. It is ignored when Trust is
	// false.
	Persist bool
}

// Prompter asks the user to trust an unknown or changed host key. The UI
// implements it; a test provides a fake. A Manager without a Prompter rejects
// untrusted keys, because the trust boundary fails closed.
type Prompter interface {
	Confirm(ctx context.Context, key domainconn.Key) (PromptDecision, error)
}

// Manager opens Connections and reads their credentials, Known Hosts, and
// trust decisions through its ports.
type Manager struct {
	dialer    Dialer
	passwords PasswordSource
	known     KnownHostRepository
	prompter  Prompter
	newID     func() string
	now       func() time.Time
}

// Option customizes a Manager.
type Option func(*Manager)

// WithPrompter sets the trust prompt. A nil prompter rejects every untrusted
// key.
func WithPrompter(prompter Prompter) Option {
	return func(m *Manager) { m.prompter = prompter }
}

// WithIDGenerator sets how the Manager names Known Hosts.
func WithIDGenerator(fn func() string) Option {
	return func(m *Manager) {
		if fn != nil {
			m.newID = fn
		}
	}
}

// WithClock sets the Manager's source of time.
func WithClock(fn func() time.Time) Option {
	return func(m *Manager) {
		if fn != nil {
			m.now = fn
		}
	}
}

// NewManager returns a Manager over its ports.
func NewManager(dialer Dialer, passwords PasswordSource, known KnownHostRepository, opts ...Option) *Manager {
	manager := &Manager{
		dialer:    dialer,
		passwords: passwords,
		known:     known,
		newID:     newKnownHostID,
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(manager)
	}
	return manager
}

// Acquire opens a new Connection and returns its first Lease. The caller's
// context bounds dialing, the trust prompt, and authentication; after Acquire
// returns, the Connection owns its transport.
func (m *Manager) Acquire(ctx context.Context, req Request) (*Lease, error) {
	if m.dialer == nil {
		return nil, internalError(ErrNoDialer)
	}

	password, err := m.password(ctx, req.HostID)
	if err != nil {
		return nil, err
	}

	known, err := m.knownHosts(ctx)
	if err != nil {
		return nil, err
	}

	client, err := m.dialer.Dial(ctx, DialRequest{
		Hostname: req.Hostname,
		Port:     req.Port,
		Username: req.Username,
		Password: password,
		VerifyHostKey: func(verifyCtx context.Context, key domainconn.Key) error {
			return m.verifyHostKey(verifyCtx, key, known)
		},
	})
	if err != nil {
		return nil, classifyDialError(err)
	}
	return newConnection(client).acquire(), nil
}

// password reads the saved credential. A missing credential is an auth error,
// not a storage error: the user can fix it by editing the Host.
func (m *Manager) password(ctx context.Context, hostID model.HostID) (string, error) {
	if m.passwords == nil {
		return "", authError(ErrNoCredentials)
	}
	value, err := m.passwords.Password(ctx, hostID)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return "", err
		}
		if errors.Is(err, context.Canceled) {
			return "", cancelledError(err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", timeoutError(err)
		}
		return "", authError(err)
	}
	return value, nil
}

func (m *Manager) knownHosts(ctx context.Context) ([]domainconn.KnownHost, error) {
	if m.known == nil {
		return nil, internalError(errors.New("connection: no Known Host store"))
	}
	known, err := m.known.ListKnownHosts(ctx)
	if err != nil {
		return nil, internalError(err)
	}
	return known, nil
}

// verifyHostKey classifies the live key and asks the user when the key is not
// already trusted. Trust upserts the Known Host before authentication starts.
func (m *Manager) verifyHostKey(ctx context.Context, key domainconn.Key, known []domainconn.KnownHost) error {
	classified := domainconn.Classify(known, key.Hostname, key.Port, key.KeyType, key.Fingerprint)
	if classified.PublicKey == "" {
		classified.PublicKey = key.PublicKey
	}
	if classified.Status == domainconn.StatusTrusted {
		return nil
	}
	if m.prompter == nil {
		return hostKeyError(ErrHostKeyRejected)
	}

	decision, err := m.prompter.Confirm(ctx, classified)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return err
		}
		if errors.Is(err, context.Canceled) {
			return cancelledError(err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return timeoutError(err)
		}
		return hostKeyError(err)
	}
	if !decision.Trust {
		return hostKeyError(ErrHostKeyRejected)
	}
	if !decision.Persist {
		return nil
	}

	record := domainconn.NewKnownHost(m.newID(), classified, m.now())
	if classified.KnownHostID != "" {
		record.ID = classified.KnownHostID
	}
	if err := m.known.UpsertKnownHost(ctx, record); err != nil {
		return internalError(err)
	}
	return nil
}

// classifyDialError maps an adapter failure to a typed connection error. An
// adapter that already returns *Error keeps its classification.
func classifyDialError(err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return cancelledError(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return timeoutError(err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return timeoutError(err)
		}
		return networkError(err)
	}
	return networkError(err)
}

// Connection is one authenticated transport with a lease count. It is the
// application's owner of the adapter's Client; the UI and domain never see it.
type Connection struct {
	mu     sync.Mutex
	client Client
	refs   int
	closed bool
}

func newConnection(client Client) *Connection {
	return &Connection{client: client}
}

// acquire takes a reference and returns a Lease. It returns nil when the
// connection is closed, mirroring a pool that has discarded it.
func (c *Connection) acquire() *Lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.refs++
	return &Lease{conn: c}
}

// release drops a reference and closes the physical connection at zero.
func (c *Connection) release() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.refs--
	if c.refs > 0 {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	client := c.client
	c.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

// Lease is a counted claim on a Connection. It is safe to release more than
// once.
type Lease struct {
	conn     *Connection
	mu       sync.Mutex
	released bool
}

// OpenTerminal opens an interactive shell channel on the leased Connection.
func (l *Lease) OpenTerminal(ctx context.Context, spec TerminalSpec) (Terminal, error) {
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released || l.conn == nil {
		return nil, ErrClosed
	}

	l.conn.mu.Lock()
	client := l.conn.client
	closed := l.conn.closed
	l.conn.mu.Unlock()
	if closed || client == nil {
		return nil, ErrClosed
	}

	terminal, err := client.OpenTerminal(ctx, spec)
	if err != nil {
		return nil, classifyDialError(err)
	}
	return terminal, nil
}

// Release drops the lease. Releasing the last lease closes the Connection.
func (l *Lease) Release() error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	return l.conn.release()
}

// knownHostIDEncoding is Crockford's base32, the alphabet ULIDs use.
var knownHostIDEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// newKnownHostID returns a ULID for a Known Host.
func newKnownHostID() string {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return knownHostIDEncoding.EncodeToString(id[:])
}
