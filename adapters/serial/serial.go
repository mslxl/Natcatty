// Package serial implements the Session transport port for serial ports.
//
// The adapter is pure Go. The platform code lives behind build tags in this
// package: a POSIX termios backend on Linux and macOS, a DCB backend on
// Windows, and a typed unsupported error everywhere else. The application
// layer sees only a Transport, as it does for a local PTY or an SSH channel.
//
// Tests inject OpenPort and ListPortsFunc so a fake port exercises the same
// open, read, write, and close path without hardware.
package serial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	appsession "github.com/mslxl/natcatty/application/session"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// Factory opens serial Sessions and enumerates ports. The zero value uses the
// platform backend. The function fields are test seams; they are never set in
// the running app.
type Factory struct {
	// OpenPort overrides the platform port opener.
	OpenPort func(context.Context, domainserial.Config) (io.ReadWriteCloser, error)
	// ListPortsFunc overrides the platform enumerator.
	ListPortsFunc func(context.Context) ([]domainserial.PortInfo, error)
	// Capabilities overrides the platform support table. A nil value uses
	// domainserial.PlatformCapabilities.
	Capabilities *domainserial.Capabilities
}

var _ appsession.Factory = Factory{}

// Open validates spec's serial configuration, checks it against the platform
// capabilities, and opens the port. A malformed configuration is
// ErrInvalidConfig; a well-formed setting the platform cannot program is
// ErrUnsupported.
func (f Factory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Serial == nil {
		return nil, &domainserial.ValidationError{Field: "serial", Reason: "is required"}
	}
	cfg := spec.Serial.Normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := domainserial.CheckSupport(cfg, f.capabilities()); err != nil {
		return nil, err
	}

	port, err := f.openPort(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", cfg.Path, err)
	}
	if err := ctx.Err(); err != nil {
		_ = port.Close()
		return nil, err
	}
	return &transport{port: port}, nil
}

// ListPorts returns the ports the platform enumerates. A platform without a
// backend returns a typed unsupported error rather than an empty list that
// would look like "no hardware connected".
func (f Factory) ListPorts(ctx context.Context) ([]domainserial.PortInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.ListPortsFunc != nil {
		return f.ListPortsFunc(ctx)
	}
	caps := f.capabilities()
	if !caps.Supported {
		return nil, &domainserial.UnsupportedError{Platform: caps.Platform, Setting: "port enumeration"}
	}
	return platformListPorts(ctx)
}

func (f Factory) capabilities() domainserial.Capabilities {
	if f.Capabilities != nil {
		return *f.Capabilities
	}
	return domainserial.PlatformCapabilities()
}

func (f Factory) openPort(ctx context.Context, cfg domainserial.Config) (io.ReadWriteCloser, error) {
	if f.OpenPort != nil {
		return f.OpenPort(ctx, cfg)
	}
	return platformOpenPort(ctx, cfg)
}

// transport adapts a serial port to the Session transport port and classifies
// its end the way the original did: a user close is a clean end with exit 0,
// and a port error is an error end with exit 1.
type transport struct {
	port io.ReadWriteCloser

	mu     sync.Mutex
	ended  bool
	end    appsession.End
	closed atomic.Bool
	// closeOnce makes concurrent Close calls share one port close and its
	// result; closeErr is read under mu.
	closeOnce sync.Once
	closeErr  error
}

var (
	_ appsession.EndReporter = (*transport)(nil)
	_ appsession.Transport   = (*transport)(nil)
)

func (t *transport) Read(p []byte) (int, error) {
	n, err := t.port.Read(p)
	if err != nil {
		if t.closed.Load() || errors.Is(err, io.EOF) {
			t.remember(appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true})
		} else {
			t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
		}
	}
	return n, err
}

func (t *transport) Write(p []byte) (int, error) {
	n, err := t.port.Write(p)
	if err != nil {
		t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
	}
	return n, err
}

// Resize is a no-op: a serial port has no window size. The Session still
// accepts resize commands so one terminal surface serves every transport.
func (t *transport) Resize(int, int) error { return nil }

// End reports the first classified end, so a close after a read error does
// not turn a device failure into a clean exit.
func (t *transport) End() appsession.End {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return t.end
	}
	if t.closed.Load() {
		return appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true}
	}
	return appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true}
}

// Close hangs up the port. The first end classification wins, so an error
// reported by Read is preserved; a close that itself fails is an error end.
func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		err := t.port.Close()
		t.mu.Lock()
		t.closeErr = err
		t.mu.Unlock()
		if err != nil {
			t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
			return
		}
		t.remember(appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true})
	})
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeErr
}

func (t *transport) remember(end appsession.End) {
	t.mu.Lock()
	if !t.ended {
		t.ended = true
		t.end = end
	}
	t.mu.Unlock()
}
