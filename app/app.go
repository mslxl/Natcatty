// Package app is the composition root: it constructs the windows, the shell
// state, the command registry, and the native menu, and wires MyGo's
// application lifecycle to them. It is the only package besides main that
// knows the whole application graph.
package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	"github.com/mslxl/natcatty/adapters/histfile"
	"github.com/mslxl/natcatty/adapters/localpty"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/adapters/textcodec"
	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/application/autocomplete"
	"github.com/mslxl/natcatty/application/command"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appvault "github.com/mslxl/natcatty/application/vault"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	uiforwarding "github.com/mslxl/natcatty/ui/forwarding"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// Run starts Natcatty and blocks until the application quits.
func Run() error { return run(nil) }

// run is the composition root proper. afterReady, when non-nil, runs on the UI
// thread after the main window exists; the launch smoke build uses it to quit.
func run(afterReady func(hostWindow)) error {
	var windows *windowManager
	var mainWindow hostWindow
	var gate *uiapplock.Gate
	var settingsState *settings.AppLockState
	var desktopState *settings.DesktopState
	var terminalState *settings.TerminalState
	var desktopService *appdesktop.Service
	var terminalService *appterminalsettings.Service
	var sessions *appsession.Service
	var desktopCtl *desktopController
	var ready bool
	var focusRequested bool

	// A later launch hands its command line to the running instance and exits.
	// Focus the existing main window and open any Host target it carried; a
	// URL is delivered separately to OnOpenURL. If readiness has not run yet,
	// the target is queued. The selftest build invokes this same callback to
	// prove the production second-instance path raises main.
	secondInstance := func(args []string, _ string) {
		if !ready {
			focusRequested = true
		} else if windows != nil {
			windows.SecondInstance()
		}
		if desktopCtl != nil {
			desktopCtl.HandleLaunchArgs(args)
		}
	}
	if !singleInstancePrimary(mygoInstanceLock{}, secondInstance) {
		return nil
	}

	invalidate := func() {
		if windows != nil {
			windows.InvalidateAll()
		} else if mainWindow != nil && !mainWindow.IsDestroyed() {
			mainWindow.Invalidate()
		}
		if desktopCtl != nil {
			desktopCtl.ScheduleSync()
		}
	}

	// openSettings is provided by the window manager below: one Settings
	// window per app, as ADR 0006 requires.

	userData, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return err
	}

	// Desktop integration lives in a versioned JSON file beside App Lock. The
	// OS login-item state is refreshed once the application is ready; MyGo's
	// native APIs cannot answer before Run.
	desktopService = appdesktop.NewService(
		store.OpenDesktop(filepath.Join(userData, "desktop-settings.json")),
		hotkeyRegistrar{},
		autoLauncher{},
		&nativeNotifier{
			locale: func() string { return mygo.App.Locale() },
			onClick: func() {
				if windows != nil {
					windows.FocusMain()
				}
			},
		},
		appdesktop.WithGOOS(runtime.GOOS),
	)
	if err := desktopService.Load(context.Background()); err != nil {
		return err
	}
	desktopState = settings.NewDesktopState(desktopService,
		settings.WithDesktopRedrawRequest(invalidate))

	// Terminal behavior settings live beside the desktop settings. A missing
	// file is the original defaults; a malformed one also loads as defaults so
	// it cannot stop the app from starting.
	terminalService = appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(userData, "terminal-settings.json")),
		appterminalsettings.WithChangeHook(func(current domainterminal.Settings) {
			invalidate()
			// The original's OSC 52 handler read Settings on every request.
			// Apply a change to live Sessions as well as future ones.
			if sessions != nil {
				sessions.SetClipboardPolicy(current.OSC52)
				sessions.SetHighlightPolicy(current.KeywordHighlightEnabled,
					current.KeywordHighlightRules)
			}
		}),
	)
	_ = terminalService.Load(context.Background())
	terminalState = settings.NewTerminalState(terminalService,
		settings.WithTerminalRedrawRequest(invalidate))

	// Autocomplete combines the commands recorded in this app with the shell's
	// own history files, as the original did. Both sources are bounded and
	// degrade to empty so a missing or malformed history cannot block startup.
	home, _ := os.UserHomeDir()
	autocompleteService := autocomplete.NewService(
		store.OpenCommandHistory(filepath.Join(userData, "command-history.json")),
		func() autocomplete.Config {
			current := terminalService.Settings()
			return autocomplete.Config{
				Enabled:        current.AutocompleteEnabled,
				MinChars:       current.AutocompleteMinChars,
				MaxSuggestions: current.AutocompleteMaxSuggestions,
				DebounceMs:     current.AutocompleteDebounceMs,
				Scope:          current.AutocompleteHistoryScope,
			}
		},
		autocomplete.WithSource(histfile.Default(home)),
	)
	autocompleteService.Load(context.Background())

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
		appvault.WithGroups(database),
		appvault.WithProxyProfiles(database),
		appvault.WithKnownHosts(database),
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

	// The Vault state is created below; a persisted host-key trust decision
	// marks its Known Host list stale through this pointer.
	var vaultState *vaultstate.State

	// One SSH Connection per Session. The lease seam is what tickets 08 and 09
	// extend into jump chains and a shared pool.
	connections := appconnection.NewManager(
		sshadapter.NewDialer(),
		vaultPasswords{vault: vaultService},
		database,
		appconnection.WithPrompter(trustPrompt),
		appconnection.WithKnownHostsChanged(func() {
			if vaultState != nil {
				vaultState.InvalidateKnownHosts()
			}
		}),
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
	routes := vaultRoutes{vault: vaultService}

	// Port Forwarding Rules reuse the same Vault routes and connection pool as
	// a Session. Runtime state is owned by this manager and never persisted.
	forwardingManager := appforwarding.NewManager(database, forwardingConnector{
		vault:       vaultService,
		routes:      routes,
		connections: connections,
	})
	forwardingState := uiforwarding.NewState(
		forwardingManager,
		vaultService,
		uiforwarding.WithRedrawRequest(invalidate),
	)

	var shellState *state.State
	vaultState = vaultstate.NewState(
		vaultService,
		vaultstate.WithSecrets(vaultService),
		vaultstate.WithKeys(vaultService),
		vaultstate.WithIdentities(vaultService),
		vaultstate.WithGroups(vaultService),
		vaultstate.WithProxyProfiles(vaultService),
		vaultstate.WithKnownHosts(vaultService),
		vaultstate.WithRedrawRequest(invalidate),
		vaultstate.WithConnect(func(host domainvault.Host) {
			if shellState != nil {
				_ = shellState.OpenHost(host)
			}
		}),
		vaultstate.WithConnectionTest(func(ctx context.Context, host domainvault.Host) error {
			return testConnection(ctx, connections, routes, host)
		}),
	)
	_ = vaultState.Reload()
	// A malformed stored rule is dropped by the repository; a read failure
	// leaves the section empty and shows its localized internal problem.
	_ = forwardingState.Reload()

	terminalLogDefaults := func(label string) appsession.LogOptions {
		current := terminalService.Settings()
		return appsession.LogOptions{
			Directory:  current.LogDirectory,
			Format:     current.LogFormat,
			Timestamps: current.LogTimestamps,
			Label:      label,
		}
	}

	sessions = appsession.NewService(newTransportFactory(
		localpty.Factory{}, connections, routes,
	),
		// Automatic logging starts with the Session, as the original's
		// configured log did. An empty directory keeps it off.
		appsession.WithAutoLogging(func(spec appsession.StartSpec) *appsession.LogOptions {
			current := terminalService.Settings()
			if !current.LoggingEnabled || current.LogDirectory == "" {
				return nil
			}
			label := spec.Label
			if label == "" {
				label = string(spec.Kind)
			}
			options := terminalLogDefaults(label)
			return &options
		}),
		appsession.WithClipboardPolicy(func() domainterminal.ClipboardAccess {
			return terminalService.Settings().OSC52
		}),
		// A new Session inherits the encoding remembered for its Host, or the
		// global default. The terminal surface applies live changes and
		// persists them through the same Settings service.
		appsession.WithEncodingResolver(func(spec appsession.StartSpec) domainterminal.Encoding {
			return terminalService.EncodingFor(spec.EncodingScope)
		}),
		// The legacy charset tables are an adapter; the Session service sees
		// only its Codec port.
		appsession.WithCodecFactory(func(encoding domainterminal.Encoding) appsession.Codec {
			return textcodec.New(encoding)
		}),
		// Typed input that passed a Session's tap fans out to the selected
		// broadcast targets. shellState is assigned after this service; the
		// closure reads it only when input arrives.
		appsession.WithInputHook(func(id model.SessionID, data []byte) {
			if shellState != nil {
				// State ignores a source outside the main window and only
				// delivers to main-window recipients, so detached windows
				// need no window-focus special case here.
				shellState.NoteSessionInput(id, data)
			}
		}),
		// Commands that ran in a Session feed autocomplete under that Session's
		// scope. The Host scope keeps an SSH Host's history separate.
		appsession.WithCommandHook(func(_ model.SessionID, scope, command string) {
			autocompleteService.Record(scope, command)
		}),
		// A BEL marks the Session active, matching the original's tab badge.
		// The notification mode is applied separately by the notification hook.
		appsession.WithBellHook(func(id model.SessionID) {
			if shellState != nil {
				shellState.NoteSessionActivity(id)
			}
		}),
		appsession.WithHighlightPolicy(func() (bool, []domainterminal.KeywordHighlightRule) {
			current := terminalService.Settings()
			return current.KeywordHighlightEnabled, current.KeywordHighlightRules
		}),
		// The app applies the notification mode and window focus before
		// showing an OS notification.
		appsession.WithNotificationHook(func(_ model.SessionID, notification appsession.TerminalNotification) {
			mode := terminalService.Settings().OSCNotifications
			deliverProgramNotification(mode, notification, func() bool {
				if mainWindow == nil || mainWindow.IsDestroyed() {
					return false
				}
				return mainWindow.IsVisible() && mainWindow.IsFocused()
			}, func() {
				if windows != nil {
					windows.FocusMain()
				}
			})
		}),
	)
	// The Workspace restores Tabs, splits, and working directories at startup
	// and persists them after every structural change. Output is never stored.
	workspaceService := appworkspace.NewService(database.Workspace())
	shellState = state.NewState(
		state.WithLocaleRequest(func(tag string) {
			mygo.App.SetLocale(tag)
		}),
		state.WithSettingsRequest(func() {
			if windows != nil {
				windows.OpenSettings()
			}
		}),
		// Opening and closing a Tab changes the shell's structure; ask for the
		// frame that shows the new tab strip or Vault placeholder. Every
		// window redraws so a lock or unlock propagates.
		state.WithRedrawRequest(invalidate),
		state.WithSessions(sessions, func(s *appsession.Session) (state.Surface, error) {
			return sessionui.New(s,
				sessionui.WithLogDefaults(func() appsession.LogOptions {
					return terminalLogDefaults(s.Label())
				}),
				sessionui.WithRedraw(invalidate),
				sessionui.WithPastePolicy(terminalService.PasteConfirmation),
				sessionui.WithDropAction(func() domainterminal.DropAction {
					return terminalService.Settings().DropAction
				}),
				sessionui.WithEncodingChange(func(encoding domainterminal.Encoding) {
					_ = terminalService.SetEncodingFor(s.EncodingScope(), encoding)
				}),
				// Suggestions and counters read Settings on each frame, so a
				// Settings change applies to open Sessions without reopening.
				sessionui.WithSuggestions(autocompleteService.Suggestions,
					func() (bool, bool) {
						current := terminalService.Settings()
						return current.AutocompletePopupMenu, current.AutocompleteGhostText
					}),
				sessionui.WithAutocompleteDelay(func() int {
					return terminalService.Settings().AutocompleteDebounceMs
				}),
				sessionui.WithSessionStats(func() bool {
					return terminalService.Settings().ShowSessionStats
				}),
				// Reconnect is the original's manual retry: it abandons the
				// current transport and dials again without the policy delay.
				sessionui.WithReconnect(func() { s.ReconnectNow() }, s.CanReconnect),
			)
		}),
		state.WithVault(vaultui.View(vaultState, vaultui.WithForwarding(forwardingState))),
		state.WithTrustPrompt(trustPrompt),
		state.WithAuthPrompt(authPrompt),
		state.WithWorkspace(workspaceService, func(ctx context.Context, id model.HostID) (domainvault.Host, bool) {
			host, err := vaultService.Get(ctx, id)
			if err != nil {
				return domainvault.Host{}, false
			}
			return host, true
		}),
		// Detached Tabs get a real window; moving one back closes that window.
		state.WithDetachHandler(func(id model.TabID) {
			if windows != nil {
				windows.OpenDetached(id)
			}
		}),
		state.WithDetachReattachHandler(func(id model.TabID) {
			if windows != nil {
				windows.ReattachClose(id)
			}
		}),
		state.WithDetachedCloseRequest(func(id model.TabID) {
			if windows != nil {
				windows.RequestDetachedClose(id)
			}
		}),
	)
	registry := newRegistry(shellState, mygo.App.Quit, gate.Lock)

	// Every window is created through this manager, which wraps its content in
	// the one App Lock gate and owns close/exit intent.
	windows = newWindowManager(
		mygoWindowHost{},
		nativeConfirmer{},
		gate,
		shellState,
		func() func(*ui.Context) {
			return settings.ViewWithDesktop(settingsState, desktopState, terminalState)
		},
		func() string { return mygo.App.Locale() },
	)
	// The tray owns show/hide, recent Hosts, active Port Forwarding Rules, and
	// quit. Close-to-tray asks the current settings live.
	desktopCtl = newDesktopController(
		windows, shellState, vaultState, forwardingManager, desktopService,
		mygoTrayBackend{},
		func() string { return mygo.App.Locale() },
		mygo.App.Quit,
	)
	windows.SetCloseToTray(desktopCtl.HideOnClose)
	// The selftest build installs the F11 keyboard facility here; the normal
	// build's installer is a no-op.
	installSelftest(windows, shellState, desktopCtl, secondInstance)

	// A Session's end may run on any goroutine; Invalidate is safe from
	// any goroutine and asks for the frame that shows the new status.
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		shellState.Persist()
		closeForwarding(forwardingManager, forwardingState)
		desktopCtl.Shutdown()
		shellState.Shutdown()
		shellState.CloseAllSessions()
		sessions.CloseAll()
		connections.CloseAll()
	})

	// Quit intent is asked once, before any window closes. Detached windows
	// then close without a second prompt.
	mygo.App.OnBeforeQuit(func(event *mygo.QuitEvent) {
		if windows == nil {
			return
		}
		if !windows.ConfirmQuit() {
			event.PreventDefault()
		}
	})

	mygo.App.WhenReady(func() {
		desktopService.RefreshAutoStart()
		mygo.App.SetMenu(BuildMenu(registry, mygo.App.Locale()))
		// Start is asynchronous, so launch never waits on a network handshake.
		forwardingManager.AutoStart(context.Background())

		mainWindow = windows.OpenMain()
		ready = true
		// The tray appears before launch targets so any deep link or
		// command-line Host opens into a running shell.
		desktopCtl.MarkReady()
		if err := desktopService.Apply(desktopCtl.ToggleMain); err != nil {
			// Another application may own the accelerator. The failure is
			// reported in Settings; the app keeps working.
			desktopState.RefreshProblem()
		}
		forwardingManager.Subscribe(desktopCtl.NoteForwarding)
		desktopCtl.HandleLaunchArgs(os.Args[1:])

		// A login launch starts quietly in the tray, as the original did,
		// but only when there is a tray icon to reopen the window from.
		if mygo.App.WasOpenedAtLogin() && desktopService.Settings().ShowTrayIcon &&
			desktopService.Capabilities().Tray && mainWindow.IsVisible() {
			mainWindow.Hide()
		}

		// Terminal widgets consume their own input, so the gate never sees
		// keystrokes typed there. A Session change (including the echo of
		// that input) is the observable activity.
		sessions.SetChangeHook(func(snapshot appsession.Snapshot) {
			gate.RecordActivity()
			shellState.NoteSessionActivity(snapshot.ID)
			windows.InvalidateAll()
			desktopCtl.NoteSession(snapshot)
		})

		// Tabs, splits, and working directories come back; their Sessions open
		// asynchronously and apply on the next frame. Output does not survive.
		shellState.Restore(context.Background())

		mygo.App.OnLocaleChanged(func(tag string) {
			windows.ApplyLocale()
			mygo.App.SetMenu(BuildMenu(registry, tag))
			desktopCtl.SyncNow()
		})

		// A second launch that handed over before readiness still gets its
		// existing window raised.
		if focusRequested {
			focusRequested = false
			windows.FocusMain()
		}

		if afterReady != nil {
			afterReady(mainWindow)
		}
	})

	// A deep link opens through the same window and shell as a Host click.
	// Register before Run so the URL the app was started with is delivered.
	mygo.App.OnOpenURL(func(raw string) {
		if desktopCtl != nil {
			desktopCtl.HandleDeepLink(raw)
		}
	})
	// Runtime registration covers Windows/Linux installs; macOS uses
	// urlSchemes in mygo.json. A development build may not be allowed to
	// register, which must not stop the app.
	_ = mygo.App.RegisterURLScheme("ssh")

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
	register(command.CloseOtherTabs, func(context.Context) error {
		shellState.CloseOtherTabs(shellState.ActiveTab())
		return nil
	})
	register(command.CloseTabsRight, func(context.Context) error {
		shellState.CloseTabsRight(shellState.ActiveTab())
		return nil
	})
	register(command.DuplicateTab, func(context.Context) error {
		return shellState.DuplicateTab(shellState.ActiveTab())
	})
	register(command.DetachTab, func(context.Context) error {
		shellState.DetachTab(shellState.ActiveTab())
		return nil
	})
	register(command.CopyTabToNewWindow, func(context.Context) error {
		return shellState.DuplicateTabToWindow(shellState.ActiveTab())
	})
	register(command.DetachPane, func(context.Context) error {
		index := shellState.ActiveTab()
		if paneID, ok := shellState.FocusedPaneID(index); ok {
			shellState.DetachPane(index, paneID)
		}
		return nil
	})
	register(command.SplitPaneRight, func(context.Context) error {
		return shellState.SplitRight()
	})
	register(command.SplitPaneDown, func(context.Context) error {
		return shellState.SplitDown()
	})
	register(command.ClosePane, func(context.Context) error {
		shellState.CloseFocusedPane()
		return nil
	})
	for id, direction := range map[command.ID]domainworkspace.FocusDirection{
		command.FocusPaneLeft:  domainworkspace.FocusLeft,
		command.FocusPaneRight: domainworkspace.FocusRight,
		command.FocusPaneUp:    domainworkspace.FocusUp,
		command.FocusPaneDown:  domainworkspace.FocusDown,
	} {
		direction := direction
		register(id, func(context.Context) error {
			shellState.FocusPane(direction)
			return nil
		})
	}
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
