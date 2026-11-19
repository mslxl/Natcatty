// Phone Session wiring shared by the phone build and its tests.
//
// The phone composition root registers only remote transports: SSH, Telnet,
// Mosh, and EternalTerminal. There is no local PTY and no serial adapter, so
// neither can be created from the phone UI even if a caller passes a StartSpec
// that names one. The Session service, Connection Manager, Vault routes, and
// Network Provider registry are the same application objects the desktop
// uses, which is what keeps one Host opening the same way on both platforms.

package app

import (
	"context"
	"errors"
	"io"
	"sync"

	adapteret "github.com/mslxl/natcatty/adapters/et"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// errPhoneLocalSession is the closed failure for a Host that would run a shell
// on the phone. The domain still maps ProtocolLocal to KindLocal for the
// desktop; the phone's factory and opener both refuse it.
var errPhoneLocalSession = errors.New("app: local and serial Sessions are not available on a phone")

// phoneSessionRuntime opens Hosts as Sessions for the phone shell. It owns the
// Connection Manager and the Session service for the app's lifetime.
type phoneSessionRuntime struct {
	service     *appsession.Service
	connections *appconnection.Manager
	vault       *appvault.Service
}

// Ensure the runtime is the shell's opener seam.
var _ phoneshell.SessionOpener = (*phoneSessionRuntime)(nil)

// newPhoneSessionRuntime builds the phone's remote-only transport stack. The
// known-host repository is the shared Vault database, so a host key trusted on
// the desktop is trusted here after a sync and vice versa.
func newPhoneSessionRuntime(vault *appvault.Service, known appconnection.KnownHostRepository, userData string) *phoneSessionRuntime {
	providers := appconnection.NewNetworkProviderRegistry(&networkProviderFactory{
		vault:     vault,
		stateBase: networkProviderStateBase(userData),
	})
	connections := appconnection.NewManager(
		sshadapter.NewDialer(),
		vaultPasswords{vault: vault},
		known,
		appconnection.WithNetworkProviders(providers),
		// The phone has no interactive trust prompt yet. A Host whose key is
		// not already a Known Host fails closed with a host-key error instead
		// of being trusted silently.
		appconnection.WithAuthSource(vaultAuthSource{
			vault:    vault,
			defaults: newSSHDefaultKeys(),
		}),
		appconnection.WithAuthMethodCache(
			appconnection.NewAuthMethodCache(appconnection.MaxAuthMethodCacheEntries)),
	)
	routes := vaultRoutes{vault: vault}
	service := appsession.NewService(
		newPhoneTransportFactory(connections, routes, providers),
		// A successful open is the Vault's Recently Connected fact on the
		// phone too; it is device-local telemetry, exactly as on the desktop.
		appsession.WithConnectionHook(func(event appsession.ConnectionEvent) {
			if event.Err == nil && !event.UnsavedHost && event.HostID != "" {
				_ = vault.MarkConnected(context.Background(), event.HostID)
			}
		}),
	)
	return &phoneSessionRuntime{service: service, connections: connections, vault: vault}
}

// newPhoneTransportFactory is the phone's transport registry: remote
// transports only. A Local or Serial Session fails closed instead of reaching
// a shell or device on the phone.
func newPhoneTransportFactory(manager *appconnection.Manager, routes vaultRoutes,
	providers *appconnection.NetworkProviderRegistry) *transportFactory {
	return newTransportFactory(
		nil, nil,
		newTelnetFactory(adapterstnet.Factory{}, routes, providers),
		manager, routes,
	).withInProcess(
		newMoshFactory(manager, routes, providers),
		adapteret.InProcessFactory{Manager: manager, Routes: routes},
	)
}

// OpenHost opens one saved Host and attaches the terminal view the phone
// draws. The StartSpec comes from the shared application mapping, so a Mosh,
// ET, or Telnet Host behaves as it does on the desktop.
func (r *phoneSessionRuntime) OpenHost(ctx context.Context, host domainvault.Host) (phoneshell.SessionHandle, error) {
	spec := appsession.HostSpec(host)
	if phoneRefusesKind(spec.Kind) {
		return nil, errPhoneLocalSession
	}
	// A phone has no window size before its first frame; the terminal view
	// resizes the Session as soon as it is laid out.
	if spec.Cols == 0 {
		spec.Cols = 80
	}
	if spec.Rows == 0 {
		spec.Rows = 24
	}
	session, err := r.service.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	view, err := session.AttachView()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return &phoneSessionHandle{session: session, view: view}, nil
}

// Close ends every Session and shuts the Connection Manager down.
func (r *phoneSessionRuntime) Close() {
	if r == nil {
		return
	}
	if r.service != nil {
		r.service.CloseAll()
	}
	if r.connections != nil {
		r.connections.CloseAll()
	}
}

// phoneRefusesKind reports whether a Session kind has no transport on a
// phone. A Local Session would run a shell on the device; a serial device has
// no phone adapter and no Host record that could select it.
func phoneRefusesKind(kind appsession.Kind) bool {
	return kind == appsession.KindLocal || kind == appsession.KindSerial
}

// networkProviderStateBase is where a Network Provider keeps its per-Profile
// state on the phone: this app's user-data directory, never a system daemon's.
func networkProviderStateBase(userData string) string {
	return userData + "/network-providers"
}

// phoneSessionHandle adapts an attached Session view to the phone shell's
// handle. Read and Write are the terminal's output and input; Close detaches
// the view and ends the Session.
type phoneSessionHandle struct {
	session *appsession.Session
	view    *appsession.ViewConn
	once    sync.Once
	err     error
}

var _ phoneshell.SessionHandle = (*phoneSessionHandle)(nil)

func (h *phoneSessionHandle) Read(p []byte) (int, error)  { return h.view.Read(p) }
func (h *phoneSessionHandle) Write(p []byte) (int, error) { return h.view.Write(p) }
func (h *phoneSessionHandle) Resize(cols, rows int) error { return h.view.Resize(cols, rows) }
func (h *phoneSessionHandle) Done() <-chan struct{}       { return h.session.Done() }

func (h *phoneSessionHandle) Close() error {
	h.once.Do(func() {
		detachErr := h.view.Close()
		closeErr := h.session.Close()
		h.err = errors.Join(detachErr, closeErr)
	})
	return h.err
}

var _ io.ReadWriteCloser = (*phoneSessionHandle)(nil)
