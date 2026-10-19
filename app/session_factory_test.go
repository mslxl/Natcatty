package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

type factoryPasswords struct {
	value string
	err   error
}

func (p factoryPasswords) Secret(context.Context, model.HostID, string) (string, error) {
	return p.value, p.err
}

type factoryKnownHosts struct {
	records []domainconn.KnownHost
	saved   []domainconn.KnownHost
}

func (k *factoryKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return k.records, nil
}

func (k *factoryKnownHosts) UpsertKnownHost(_ context.Context, record domainconn.KnownHost) error {
	k.saved = append(k.saved, record)
	return nil
}

type factoryTerminal struct {
	mu     sync.Mutex
	closed bool
}

func (t *factoryTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (t *factoryTerminal) Write(p []byte) (int, error) {
	return len(p), nil
}
func (t *factoryTerminal) Resize(int, int) error { return nil }
func (t *factoryTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

type factoryClient struct {
	mu        sync.Mutex
	terminal  *factoryTerminal
	spec      appconnection.TerminalSpec
	closed    bool
	closeCall int
}

func (c *factoryClient) OpenTerminal(_ context.Context, spec appconnection.TerminalSpec) (appconnection.Terminal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spec = spec
	return c.terminal, nil
}

func (c *factoryClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCall++
	c.closed = true
	return nil
}

type factoryDialer struct {
	client  *factoryClient
	request appconnection.DialRequest
	calls   int
}

func (d *factoryDialer) Dial(_ context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	d.calls++
	d.request = req
	return d.client, nil
}

type factoryLocal struct {
	spec appsession.StartSpec
}

func (f *factoryLocal) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.spec = spec
	return &wiringTransport{closed: make(chan struct{})}, nil
}

func TestSSHSessionAcquiresAndReleasesALease(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{value: "s3cret"}}, &factoryKnownHosts{})
	local := &factoryLocal{}
	factory := newTransportFactory(local, manager)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com",
		Port: 2222, Username: "root", Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("dial calls = %d, want 1", dialer.calls)
	}
	if dialer.request.Hostname != "example.com" || dialer.request.Port != 2222 ||
		dialer.request.Username != "root" || dialer.request.Password != "s3cret" {
		t.Fatalf("dial request = %+v, want the Host endpoint and saved password", dialer.request)
	}
	if client.spec.Cols != 100 || client.spec.Rows != 30 {
		t.Fatalf("terminal spec = %+v, want 100x30", client.spec)
	}
	if local.spec.Kind != "" {
		t.Fatalf("local factory was called for an SSH Session: %+v", local.spec)
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.closeCall != 1 {
		t.Fatalf("SSH client close calls = %d, want 1", client.closeCall)
	}
	client.terminal.mu.Lock()
	terminalClosed := client.terminal.closed
	client.terminal.mu.Unlock()
	if !terminalClosed {
		t.Fatal("the terminal was not closed with the transport")
	}
}

func TestSSHSessionWithoutASavedPasswordIsAnAuthFailure(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{err: appvault.ErrNotFound}}, &factoryKnownHosts{})
	factory := newTransportFactory(&factoryLocal{}, manager)

	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
	})
	if err == nil {
		t.Fatal("Open succeeded, want an auth failure")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("error = %v, want an auth connection error", err)
	}
	if !errors.Is(err, appconnection.ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}
	if dialer.calls != 0 {
		t.Fatalf("dial calls = %d, want 0 before credentials are available", dialer.calls)
	}
}

func TestSSHSessionWithALockedSecretStoreIsNotAnAuthFailure(t *testing.T) {
	client := &factoryClient{terminal: &factoryTerminal{}}
	dialer := &factoryDialer{client: client}
	locked := &appvault.Error{Kind: appvault.KindPermission, Cause: appvault.ErrSecretsLocked}
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: factoryPasswords{err: locked}}, &factoryKnownHosts{})
	factory := newTransportFactory(&factoryLocal{}, manager)

	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Hostname: "example.com", Username: "root",
	})
	if err == nil {
		t.Fatal("Open succeeded, want a Vault failure")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindInternal {
		t.Fatalf("error = %v, want an internal connection error", err)
	}
	if errors.Is(err, appconnection.ErrNoCredentials) {
		t.Fatalf("error = %v must not claim the password is missing", err)
	}
	if dialer.calls != 0 {
		t.Fatalf("dial calls = %d before the Vault could be read", dialer.calls)
	}
}

func TestLocalSessionStillUsesTheLocalFactory(t *testing.T) {
	local := &factoryLocal{}
	factory := newTransportFactory(local, nil)

	transport, err := factory.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if local.spec.Kind != appsession.KindLocal {
		t.Fatalf("local factory spec = %+v, want a local Session", local.spec)
	}
}
