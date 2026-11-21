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
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
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
// fails closed on an untrusted key, as before. history receives the completed
// commands the background Atuin engine uploads. terminalSettings supplies the
// shared terminal document for the OSC 52 clipboard policy; nil keeps the
// original's write-only default.
func newPhoneSessionRuntime(vault *appvault.Service, known appconnection.KnownHostRepository,
	userData string, prompts *phoneshell.Prompts, history *phoneHistory,
	secrets appvault.FieldSecrets, terminalSettings func() domainterminal.Settings) *phoneSessionRuntime {
	return newPhoneSessionRuntimeWithDialer(
		sshadapter.NewDialer(), vault, known, userData, prompts, history, secrets, terminalSettings)
}

// newPhoneSessionRuntimeWithDialer is newPhoneSessionRuntime with the SSH
// dialer replaced. Tests use it to point the whole phone stack at an
// in-process SSH server while keeping the real Manager, Vault, and prompts.
// A nil history means completed commands are not recorded.
//
// secrets is the phone's encrypted field-secret store. EternalTerminal keeps
// its resumable session credentials there, under the app's user-data
// directory, so a relaunch can reattach without the key. A nil store keeps ET
// sessions memory-only; the adapter never writes credentials anywhere else.
func newPhoneSessionRuntimeWithDialer(dialer appconnection.Dialer, vault *appvault.Service,
	known appconnection.KnownHostRepository, userData string,
	prompts *phoneshell.Prompts, history *phoneHistory,
	secrets appvault.FieldSecrets, terminalSettings func() domainterminal.Settings) *phoneSessionRuntime {
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
		newPhoneTransportFactory(connections, routes, providers, secrets),
		phoneSessionOptions(vault, history, terminalSettings)...)
	return &phoneSessionRuntime{service: service, connections: connections, vault: vault}
}

// phoneSessionOptions is the shared Session behavior the phone composes. The
// clipboard policy is the same OSC 52 setting the desktop applies, read live:
// the Settings change hook calls SetClipboardPolicy so a change reaches
// Sessions that are already open. The Vault is optional so a test fixture can
// reuse this option set without a Vault service.
func phoneSessionOptions(vault *appvault.Service, history *phoneHistory,
	terminalSettings func() domainterminal.Settings) []appsession.Option {

	var options []appsession.Option
	if vault != nil {
		// A successful open is the Vault's Recently Connected fact on the
		// phone too; it is device-local telemetry, exactly as on the desktop.
		options = append(options, appsession.WithConnectionHook(func(event appsession.ConnectionEvent) {
			if event.Err == nil && !event.UnsavedHost && event.HostID != "" {
				_ = vault.MarkConnected(context.Background(), event.HostID)
			}
		}))
	}
	if history != nil {
		// Every completed remote command becomes exactly one journal row
		// through the shared completion hook. The row is what the background
		// Atuin engine uploads; capture itself never touches the network.
		options = append(options,
			appsession.WithCommandCompletionHook(history.commandHook()))
	}
	if terminalSettings != nil {
		options = append(options, appsession.WithClipboardPolicy(func() domainterminal.ClipboardAccess {
			return terminalSettings().OSC52
		}))
	}
	return options
}

// SetClipboardPolicy applies a changed OSC 52 setting to every live phone
// Session. The composition root's terminal-settings change hook calls it, so
// the Remote clipboard control takes effect without reopening anything.
func (r *phoneSessionRuntime) SetClipboardPolicy(policy domainterminal.ClipboardAccess) {
	if r == nil || r.service == nil {
		return
	}
	r.service.SetClipboardPolicy(policy)
}

// newPhoneTransportFactory is the phone's transport registry: remote
// transports only. A Local or Serial Session fails closed instead of reaching
// a shell or device on the phone.
func newPhoneTransportFactory(manager *appconnection.Manager, routes vaultRoutes,
	providers *appconnection.NetworkProviderRegistry,
	secrets appvault.FieldSecrets) *transportFactory {

	etFactory := adapteret.InProcessFactory{Manager: manager, Routes: routes}
	if secrets != nil {
		// The same encrypted field store the Vault uses keeps an ET session's
		// id and passkey. They never reach a workspace descriptor, a settings
		// document, or a log: the descriptor carries only the opaque resume
		// key.
		etFactory.Sessions = adapteret.SecretSessionStore{Secrets: secrets}
	}
	return newTransportFactory(
		nil, nil,
		newTelnetFactory(adapterstnet.Factory{}, routes, providers, manager),
		manager, routes,
	).withInProcess(
		newMoshFactory(manager, routes, providers),
		etFactory,
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
	return r.openHost(ctx, host, "")
}

// OpenHostWithResume opens a restored EternalTerminal Tab under the resume key
// its workspace descriptor carried. The key is an opaque, non-secret locator:
// the adapter reads the encrypted id and passkey for it, so a relaunch
// reattaches the still-running remote shell without asking for the key again.
// Every other transport ignores the key.
func (r *phoneSessionRuntime) OpenHostWithResume(ctx context.Context, host domainvault.Host,
	resumeKey string) (phoneshell.SessionHandle, error) {

	return r.openHost(ctx, host, resumeKey)
}

func (r *phoneSessionRuntime) openHost(ctx context.Context, host domainvault.Host,
	resumeKey string) (phoneshell.SessionHandle, error) {

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
	// HostSpec generates a fresh locator for a new ET Session. A restore
	// supplies the one its descriptor saved, which is what makes the saved
	// credentials reachable in this process.
	if spec.Kind == appsession.KindET && resumeKey != "" {
		spec.ResumeKey = resumeKey
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
	view, err := h.session.AttachView()
	if err != nil {
		return nil, err
	}
	return phoneView{phoneSessionView: view, remote: h.session.ScreenSizeFromRemote()}, nil
}

func (h *phoneSessionHandle) Hibernate() bool { return h.session.Hibernate() }

func (h *phoneSessionHandle) Resume() (io.ReadWriteCloser, error) {
	view, err := h.session.Resume()
	if err != nil {
		return nil, err
	}
	return phoneView{phoneSessionView: view, remote: h.session.ScreenSizeFromRemote()}, nil
}

func (h *phoneSessionHandle) Done() <-chan struct{} { return h.session.Done() }

// End disconnects and ends the Session. An explicit close is the only caller.
func (h *phoneSessionHandle) End() error {
	h.once.Do(func() {
		h.err = h.session.Close()
	})
	return h.err
}

// EndPreservingResume ends the Session when the app itself is quitting. A
// resumable transport (EternalTerminal) keeps the credentials the next app run
// needs to reattach; every other transport behaves exactly like End. A user
// closing one Tab still uses End, which clears them.
func (h *phoneSessionHandle) EndPreservingResume() error {
	h.once.Do(func() {
		h.err = h.session.ClosePreservingResume()
	})
	return h.err
}

// phoneSessionView is every optional Conn method a Session view offers and the
// terminal probes. The terminal is told the grid the view fits through Resize,
// and a peer-authoritative Session installs the terminal's Resize as its
// screen-size sink. Embedding io.ReadWriteCloser alone would hide both, so the
// phone would neither report its size nor accept the peer's.
type phoneSessionView interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	SetScreenSizeSink(func(cols, rows int))
}

// phoneView is the attached view plus the Session's screen-size ownership.
// The terminal screen is built over an io.ReadWriteCloser, so the flag has to
// travel with the view: a Mosh or Eternal Terminal peer owns the size, and
// the screen must hand that size to the terminal instead of resizing the view.
// It embeds the Session view's method set rather than io.ReadWriteCloser so
// the terminal's optional Conn methods stay reachable through the wrapper.
type phoneView struct {
	phoneSessionView
	remote bool
}

// ScreenSizeFromRemote reports whether the peer owns the screen size.
func (v phoneView) ScreenSizeFromRemote() bool { return v.remote }
