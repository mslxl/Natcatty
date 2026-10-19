// Package app is the composition root: it constructs the windows, the shell
// state, the command registry, and the native menu, and wires MyGo's
// application lifecycle to them. It is the only package besides main that
// knows the whole application graph.
package app

import (
	"context"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	"github.com/mslxl/natcatty/adapters/localpty"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/application/command"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// Run starts Natcatty and blocks until the application quits.
func Run() error { return run(nil) }

// run is the composition root proper. afterReady, when non-nil, runs on the UI
// thread after the main window exists; the launch smoke build uses it to quit.
func run(afterReady func(*mygo.Window)) error {
	var settingsWindow *mygo.Window
	var mainWindow *mygo.Window
	var gate *uiapplock.Gate
	var settingsState *settings.AppLockState

	invalidate := func() {
		if mainWindow != nil && !mainWindow.IsDestroyed() {
			mainWindow.Invalidate()
		}
		if settingsWindow != nil && !settingsWindow.IsDestroyed() {
			settingsWindow.Invalidate()
		}
	}

	// openSettings keeps one Settings window per app, as ADR 0006 requires.
	// Shell callbacks run on the UI thread, so creating and focusing the
	// window here is safe.
	openSettings := func() {
		if settingsWindow != nil && !settingsWindow.IsDestroyed() {
			settingsWindow.Show()
			settingsWindow.Focus()
			return
		}

		settingsWindow = mygo.NewWindow(mygo.WindowOptions{
			Title:     locales.In(mygo.App.Locale()).NavSettings(),
			Width:     640,
			Height:    480,
			MinWidth:  480,
			MinHeight: 360,
			StateKey:  "settings",
			Content:   ui.View(gate.Wrap(settings.View(settingsState))),
		})
		settingsWindow.OnFocus(func() { gate.RecordActivity() })
		settingsWindow.OnClosed(func() { settingsWindow = nil })
	}

	userData, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return err
	}
	database, err := store.Open(context.Background(), filepath.Join(userData, "natcatty.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	// Host credentials live in the field-secret store. It reports OS,
	// passphrase, or session mode; it never falls back to plaintext.
	secretStore, err := store.OpenSecretStore(context.Background(), database, platformsecrets.System{})
	if err != nil {
		return err
	}
	defer secretStore.Close()

	vaultService := appvault.NewService(
		database,
		appvault.WithSecrets(secretStore),
		appvault.WithKeys(database),
		appvault.WithIdentities(database),
		appvault.WithKeyInspector(sshadapter.KeyParser{}),
	)

	// App Lock is a UI gate backed by its own versioned verifier file.
	appLock := appapplock.NewService(
		fileapplock.Open(filepath.Join(userData, "app-lock-settings.json")),
		appapplock.WithRedrawRequest(invalidate),
	)
	// A malformed file means the fail-safe defaults inside the adapter. An
	// IO failure must not silently disable a security control.
	if err := appLock.Load(context.Background()); err != nil {
		return err
	}
	gate = uiapplock.NewGate(appLock, uiapplock.WithRedrawRequest(invalidate))
	settingsState = settings.NewAppLockState(appLock, settings.WithAppLockRedrawRequest(invalidate))

	// The trust prompt is both the Manager's Prompter and the shell's modal.
	// It bridges the dial goroutine, which blocks on the user's decision, and
	// the UI thread.
	trustPrompt := sessionui.NewTrustPrompt(sessionui.WithTrustRedrawRequest(invalidate))
	authPrompt := sessionui.NewAuthPrompt(sessionui.WithAuthPromptRedrawRequest(invalidate))

	// One SSH Connection per Session. The lease seam is what tickets 08 and 09
	// extend into jump chains and a shared pool.
	connections := appconnection.NewManager(
		sshadapter.NewDialer(),
		vaultPasswords{vault: vaultService},
		database,
		appconnection.WithPrompter(trustPrompt),
		appconnection.WithAuthSource(vaultAuthSource{
			vault:    vaultService,
			defaults: newSSHDefaultKeys(),
		}),
		appconnection.WithAuthPrompter(authPrompt),
		appconnection.WithAuthMethodCache(
			appconnection.NewAuthMethodCache(appconnection.MaxAuthMethodCacheEntries)),
	)

	// The Vault starts from the database. A read failure leaves the surface
	// empty and shows the localized internal-error message rather than
	// preventing the app from starting.
	var shellState *state.State
	vaultState := vaultstate.NewState(
		vaultService,
		vaultstate.WithSecrets(vaultService),
		vaultstate.WithKeys(vaultService),
		vaultstate.WithIdentities(vaultService),
		vaultstate.WithRedrawRequest(invalidate),
		vaultstate.WithConnect(func(host domainvault.Host) {
			if shellState != nil {
				_ = shellState.OpenHost(host)
			}
		}),
	)
	_ = vaultState.Reload()

	sessions := appsession.NewService(newTransportFactory(
		localpty.Factory{}, connections, vaultRoutes{vault: vaultService},
	))
	shellState = state.NewState(
		state.WithLocaleRequest(func(tag string) {
			mygo.App.SetLocale(tag)
		}),
		state.WithSettingsRequest(openSettings),
		// Opening and closing a Tab changes the shell's structure; ask for the
		// frame that shows the new tab strip or Vault placeholder.
		state.WithRedrawRequest(func() {
			if mainWindow != nil && !mainWindow.IsDestroyed() {
				mainWindow.Invalidate()
			}
		}),
		state.WithSessions(sessions, func(s *appsession.Session) (state.Surface, error) {
			return sessionui.New(s)
		}),
		state.WithVault(vaultui.View(vaultState)),
		state.WithTrustPrompt(trustPrompt),
		state.WithAuthPrompt(authPrompt),
	)
	registry := newRegistry(shellState, mygo.App.Quit, gate.Lock)

	// A Session's end may run on any goroutine; Invalidate is safe from
	// any goroutine and asks for the frame that shows the new status.
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		shellState.Shutdown()
		sessions.CloseAll()
	})

	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(BuildMenu(registry, mygo.App.Locale()))

		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     locales.In(mygo.App.Locale()).AppName(),
			Width:     1100,
			Height:    720,
			MinWidth:  720,
			MinHeight: 480,
			StateKey:  "main",
			Content:   ui.View(gate.Wrap(shell.View(shellState))),
		})
		mainWindow = win
		win.OnFocus(func() { gate.RecordActivity() })
		// Terminal widgets consume their own input, so the gate never sees
		// keystrokes typed there. A Session change (including the echo of
		// that input) is the observable activity.
		sessions.SetChangeHook(func(appsession.Snapshot) {
			gate.RecordActivity()
			win.Invalidate()
		})
		win.OnClosed(func() {
			shellState.Shutdown()
			sessions.CloseAll()
		})

		mygo.App.OnLocaleChanged(func(tag string) {
			win.SetTitle(locales.In(tag).AppName())
			if settingsWindow != nil && !settingsWindow.IsDestroyed() {
				settingsWindow.SetTitle(locales.In(tag).NavSettings())
				settingsWindow.Invalidate()
			}
			mygo.App.SetMenu(BuildMenu(registry, tag))
			win.Invalidate()
		})

		if afterReady != nil {
			afterReady(win)
		}
	})

	return mygo.App.Run()
}

// newRegistry wires every command the shell exposes. Menu items and any later
// key binding or command palette dispatch through this one registry.
func newRegistry(shellState *state.State, quit func(), lockApp func() error) *command.Registry {
	registry := command.NewRegistry()
	register := func(id command.ID, handler command.Handler) {
		if err := registry.Register(id, handler); err != nil {
			panic(err)
		}
	}

	register(command.NewTab, func(context.Context) error {
		return shellState.NewTab()
	})
	register(command.CloseTab, func(context.Context) error {
		shellState.CloseActiveTab()
		return nil
	})
	register(command.LockApp, func(context.Context) error {
		return lockApp()
	})
	register(command.Quit, func(context.Context) error {
		quit()
		return nil
	})
	for _, tag := range locales.Tags() {
		tag := tag
		register(command.LocaleCommandID(tag), func(context.Context) error {
			shellState.RequestLocale(tag)
			return nil
		})
	}

	return registry
}
