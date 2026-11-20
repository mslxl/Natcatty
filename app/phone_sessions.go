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
	"github.com/mslxl/natcatty/adapters/store"
	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
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

// The shared SQLite Workspace row is the phone's session persistence seam:
// the same payload format, read back through FlattenRestored.
var _ phoneshell.SessionStore = (*store.WorkspaceRepository)(nil)

// newPhoneVault builds the phone's Vault over the shared SQLite store. It
// registers the same record families the desktop root does, because route
// resolution is shared code: a Host that selects a Host Group, Identity, Proxy
// Profile, or Network Profile must resolve through the phone exactly as it
// does on the desktop. Without these repositories the phone would silently
// dial around a selected Proxy Profile and fail closed on a Network Profile.
//
// The secret store is optional. A phone whose OS credential store is
// unavailable reports the session-only mode instead of refusing to start, so
// listing and deleting Keys still work against the same Vault.
func newPhoneVault(database *store.Store, secrets *store.SecretStore) *appvault.Service {
	options := []appvault.Option{
		appvault.WithKeys(database),
		appvault.WithIdentities(database),
		appvault.WithGroups(database),
		appvault.WithProxyProfiles(database),
		appvault.WithNetworkProfiles(database),
		appvault.WithKnownHosts(database),
	}
	if secrets != nil {
		options = append(options, appvault.WithSecrets(secrets))
	}
	return appvault.NewService(database, options...)
}

// newPhonePrompts builds the phone's host-key and credential prompt queues and
// points the password saver at the Vault's field-secret store. An answered
// password is encrypted under the Host's identifier, exactly like one saved on
// the desktop; it never reaches a settings document or the log.
func newPhonePrompts(vault *appvault.Service) *phoneshell.Prompts {
	prompts := phoneshell.NewPrompts()
	prompts.Auth.SetPasswordSaver(func(ctx context.Context, id model.HostID, password string) error {
		return vault.SetSecret(ctx, id, appvault.FieldPassword, password)
	})
	return prompts
}

// newPhoneSessionRuntime builds the phone's remote-only transport stack. The
// known-host repository is the shared Vault database, so a host key trusted on
// the desktop is trusted here after a sync and vice versa. prompts answers the
// host-key and credential challenges the dial raises; without them the Manager
// fails closed on an untrusted key, as before.
func newPhoneSessionRuntime(vault *appvault.Service, known appconnection.KnownHostRepository,
	userData string, prompts *phoneshell.Prompts, history *phoneHistory) *phoneSessionRuntime {
	return newPhoneSessionRuntimeWithDialer(sshadapter.NewDialer(), vault, known, userData, prompts, history)
}

// newPhoneSessionRuntimeWithDialer is newPhoneSessionRuntime with the SSH
// dialer replaced. Tests use it to point the whole phone stack at an
// in-process SSH server while keeping the real Manager, Vault, and prompts.
func newPhoneSessionRuntimeWithDialer(dialer appconnection.Dialer, vault *appvault.Service,
	known appconnection.KnownHostRepository, userData string, prompts *phoneshell.Prompts,
	history *phoneHistory) *phoneSessionRuntime {
	providers := appconnection.NewNetworkProviderRegistry(&networkProviderFactory{
		vault:     vault,
		stateBase: networkProviderStateBase(userData),
	})
	options := []appconnection.Option{
		appconnection.WithNetworkProviders(providers),
		appconnection.WithAuthSource(vaultAuthSource{
			vault:    vault,
			defaults: newSSHDefaultKeys(),
		}),
		appconnection.WithAuthMethodCache(
			appconnection.NewAuthMethodCache(appconnection.MaxAuthMethodCacheEntries)),
	}
	if prompts != nil {
		options = append(options,
			appconnection.WithPrompter(prompts.HostKey),
			appconnection.WithAuthPrompter(prompts.Auth),
		)
	}
	connections := appconnection.NewManager(dialer, vaultPasswords{vault: vault}, known, options...)
	routes := vaultRoutes{vault: vault}
	service := appsession.NewService(
		newPhoneTransportFactory(connections, routes, providers),
		// Every completed remote command becomes exactly one journal row
		// through the shared completion hook. The row is what the background
		// Atuin engine uploads; capture itself never touches the network.
		appsession.WithCommandCompletionHook(history.commandHook()),
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

// OpenHost opens one saved Host as a Session. The StartSpec comes from the
// shared application mapping, so a Mosh, ET, or Telnet Host behaves as it does
// on the desktop.
//
// The returned handle does not attach a terminal view. The phone shell attaches
// one only while the Session is visible (and resumes it when the user returns),
// so a hidden Session never holds a live view.
func (r *phoneSessionRuntime) OpenHost(ctx context.Context, host domainvault.Host) (phoneshell.SessionHandle, error) {
	// The Hosts list carries stored records, while the desktop opens a Host
	// with its Host Group defaults and selected Proxy Profile applied. Resolve
	// the effective record here too so an inherited port or user reaches the
	// Session's endpoint instead of an empty field.
	if r.vault != nil {
		effective, err := r.vault.Get(ctx, host.ID)
		if err != nil {
			return nil, err
		}
		host = effective
	}
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
	return &phoneSessionHandle{session: session}, nil
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

// phoneSessionHandle adapts an application Session to the phone shell's
// handle. The Session owns the transport, the bounded replay, and the one live
// view. Attach claims that view; Hibernate detaches it and keeps the Session
// running; Resume replays what it produced while hidden. End is the only
// method that disconnects and ends the Session.
type phoneSessionHandle struct {
	session *appsession.Session
	once    sync.Once
	err     error
}

var _ phoneshell.SessionHandle = (*phoneSessionHandle)(nil)

func (h *phoneSessionHandle) Attach() (io.ReadWriteCloser, error) {
	return h.session.AttachView()
}

func (h *phoneSessionHandle) Hibernate() bool { return h.session.Hibernate() }

func (h *phoneSessionHandle) Resume() (io.ReadWriteCloser, error) {
	return h.session.Resume()
}

func (h *phoneSessionHandle) Done() <-chan struct{} { return h.session.Done() }

// End disconnects and ends the Session. An explicit close is the only caller.
func (h *phoneSessionHandle) End() error {
	h.once.Do(func() {
		h.err = h.session.Close()
	})
	return h.err
}
