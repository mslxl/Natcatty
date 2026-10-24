// Package app is the composition root: it constructs the windows, the shell
// state, the command registry, and the native menu, and wires MyGo's
// application lifecycle to them. It is the only package besides main that
// knows the whole application graph.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	"github.com/mslxl/natcatty/adapters/histfile"
	"github.com/mslxl/natcatty/adapters/localpty"
	adapterserial "github.com/mslxl/natcatty/adapters/serial"
	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/adapters/textcodec"
	adapterbackup "github.com/mslxl/natcatty/adapters/vaultbackup"
	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/application/autocomplete"
	"github.com/mslxl/natcatty/application/command"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	apphistory "github.com/mslxl/natcatty/application/history"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appthemes "github.com/mslxl/natcatty/application/themes"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/domain/model"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	uiforwarding "github.com/mslxl/natcatty/ui/forwarding"
	"github.com/mslxl/natcatty/ui/locales"
	logsui "github.com/mslxl/natcatty/ui/logs"
	uisearch "github.com/mslxl/natcatty/ui/search"
	serialui "github.com/mslxl/natcatty/ui/serial"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell/state"
	transferui "github.com/mslxl/natcatty/ui/transfer"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
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
	var preferenceService *apppreferences.Service
	var themeService *appthemes.Service
	var themesState *settings.ThemesState
	var currentTerminalTheme func() domaintheme.TerminalTheme
	var keybindingService *appkeybindings.Service
	var transferState *transferstate.State
	var shellState *state.State
	var preferencesState *settings.PreferencesState
	var shortcutsState *settings.ShortcutsState
	var searchState *settings.SearchState
	var registry *command.Registry
	var currentMenu *mygo.Menu
	var historyService *apphistory.Service
	var logsState *logsui.State
	var sessions *appsession.Service
	var externalMCPServer *externalMCP
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
			// A lowered retention limit takes effect immediately instead of
			// waiting for the next recorded command.
			if historyService != nil {
				_, _ = historyService.Prune(context.Background())
			}
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

	// Non-AI preferences and the keyboard-binding document live beside the
	// terminal settings. Both load before the shell exists, so a new Session
	// reads the current font and key bindings.
	preferenceService = apppreferences.NewService(
		store.OpenPreferences(filepath.Join(userData, "preferences.json")))
	if err := preferenceService.Load(context.Background()); err != nil {
		return err
	}
	preferencesState = settings.NewPreferencesState(preferenceService,
		settings.WithPreferencesRedrawRequest(invalidate))

	// Imported terminal color schemes live beside the preferences. The
	// preference stores only the scheme ID, so an import is a second document
	// that must load before a Session resolves its colors.
	themeService = appthemes.NewService(store.OpenThemes(filepath.Join(userData, "themes.json")))
	_ = themeService.Load(context.Background())
	themePicker := themeFilePicker{locale: func() string { return mygo.App.Locale() }}
	themesState = settings.NewThemesState(themeService,
		settings.WithThemeImportRequest(func() {
			name, data, err := themePicker.Pick()
			if name == "" && err == nil {
				return
			}
			themesState.Import(name, data, err)
			invalidate()
		}),
		settings.WithThemeImported(func(theme domaintheme.TerminalTheme) bool {
			err := preferenceService.Update(func(next *domainpreferences.Settings) {
				next.Appearance.TerminalTheme = theme.ID
			})
			return err == nil
		}),
		settings.WithThemesRedrawRequest(invalidate),
	)

	keybindingService = appkeybindings.NewService(
		store.OpenKeyBindings(filepath.Join(userData, "keybindings.json")),
		appkeybindings.WithGOOS(runtime.GOOS))
	if err := keybindingService.Load(context.Background()); err != nil {
		return err
	}
	shortcutsState = settings.NewShortcutsState(keybindingService,
		settings.WithBindingAvailability(catalogBindingAvailable),
		// Conflict detection reads the menu the user actually sees, so a
		// binding never silently shadows a foreign accelerator.
		settings.WithMenuShortcuts(func() []domainkeybindings.MenuShortcut {
			return menuShortcuts(currentMenu)
		}),
		settings.WithShortcutsRedrawRequest(invalidate))
	searchState = settings.NewSearchState()

	database, err := store.Open(context.Background(), filepath.Join(userData, "natcatty.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	// Connection logs and command history share the Vault database. Retention
	// is read live from Settings on every record and prune, so a change
	// applies without rebuilding the service.
	historyService = apphistory.NewService(database,
		apphistory.WithRetention(func() apphistory.Retention {
			current := terminalService.Settings()
			return apphistory.Retention{
				Logs: domainhistory.Retention{
					Count:  current.ConnectionLogRetentionCount,
					MaxAge: time.Duration(current.ConnectionLogRetentionDays) * 24 * time.Hour,
				},
				History: domainhistory.Retention{
					Count:  current.HistoryRetentionCount,
					MaxAge: time.Duration(current.HistoryRetentionDays) * 24 * time.Hour,
				},
			}
		}),
	)
	// Apply retention once at startup so a lowered limit takes effect even
	// before the next record arrives.
	_, _ = historyService.Prune(context.Background())

	// Autocomplete combines the commands recorded in this app with the shell's
	// own history files, as the original did. Both sources are bounded and
	// degrade to empty so a missing or malformed history cannot block startup.
	// The journal is the same per-execution store the History surface lists,
	// so the two views never fork the records.
	home, _ := os.UserHomeDir()
	shellHistory := histfile.Default(home)
	legacyCommandHistory := store.OpenCommandHistory(filepath.Join(userData, "command-history.json"))
	// Import the pre-journal aggregate and the local shell's files once, so
	// the History surface lists them too and an upgrade does not silently
	// drop autocomplete's earlier history.
	importLegacyCommandHistory(context.Background(), historyService, legacyCommandHistory)
	importShellHistory(context.Background(), historyService, shellHistory)
	autocompleteService := autocomplete.NewService(
		legacyCommandHistory,
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
		autocomplete.WithSource(shellHistory),
		autocomplete.WithJournal(historyService),
	)
	autocompleteService.Load(context.Background())

	// Host credentials live in the field-secret store. It reports OS,
	// passphrase, or session mode; it never falls back to plaintext.
	secretStore, err := store.OpenSecretStore(context.Background(), database, platformsecrets.System{})
	if err != nil {
		return err
	}
	defer secretStore.Close()

	// Local backups snapshot the Vault after committed changes. The store keeps
	// its own keychain key, separate from the field-secret key, so a backup can
	// be written even when credential support is in session-only mode.
	backupStore := adapterbackup.Open(
		filepath.Join(userData, "vault-backups"), platformsecrets.System{})
	backupService := appbackup.NewService(database, backupStore,
		appbackup.WithRetentionStore(backupStore))
	backupService.Start()
	defer backupService.Stop()
	database.SetChangeHook(backupService.NotifyVaultChanged)

	vaultService := appvault.NewService(
		database,
		appvault.WithSecrets(secretStore),
		appvault.WithKeys(database),
		appvault.WithIdentities(database),
		appvault.WithGroups(database),
		appvault.WithProxyProfiles(database),
		appvault.WithKnownHosts(database),
		appvault.WithSnippets(database),
		appvault.WithNotes(database),
		appvault.WithKeyInspector(sshadapter.KeyParser{}),
		appvault.WithImportStore(database),
		appvault.WithImportFiles(importFileReader{}),
		appvault.WithApplyBarrier(func(ctx context.Context) error {
			_, err := backupService.CreateProtective(ctx)
			return err
		}),
		appvault.WithHistoryReader(historyService),
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

	// SFTP reuses the Vault's routes and the same connection manager as a
	// terminal Session. The local pane and the global bookmark file belong to
	// the same service, so both panes share one copy engine.
	transferService := apptransfer.NewService(
		adaptersftp.NewLocal(),
		&sftpRemoteOpener{manager: connections, routes: routes, vault: vaultService},
		apptransfer.WithHosts(sftpHostSource{vault: vaultService}),
		apptransfer.WithBookmarks(adaptersftp.NewBookmarks(
			filepath.Join(userData, "sftp-bookmarks.json"),
			filepath.Join(userData, "sftp-local-bookmarks.json"),
		)),
	)
	// The global Transfer queue schedules every pane copy, enforces the
	// per-resource limit, and persists terminal history under the same user
	// data directory. A malformed history starts empty instead of blocking the
	// window; the load error is reported and the damaged file is left in place
	// rather than overwritten with the empty result.
	transferQueue := apptransfer.NewQueue(
		transferService,
		apptransfer.WithQueueRepository(adaptersftp.NewTransferQueue(
			filepath.Join(userData, "sftp-transfer-queue.json"),
		)),
	)
	if err := transferQueue.Start(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "natcatty: transfer history:", err)
	}
	transferState = transferstate.NewState(
		transferService,
		transferstate.WithQueue(transferQueue),
		transferstate.WithRedrawRequest(invalidate),
		transferstate.WithKeybindings(keybindingService),
		// The SFTP surface reads the live settings, so a toggled auto-sync,
		// follow-cwd, or compressed upload applies without reopening the pane.
		transferstate.WithPreferences(func() domainpreferences.SFTPEditor {
			return preferenceService.Settings().SFTPEditor
		}),
		transferstate.WithFocusedSession(func() model.SessionID {
			focused, _ := shellState.FocusedSessionID()
			return focused
		}),
	)

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

	vaultState = vaultstate.NewState(
		vaultService,
		vaultstate.WithSecrets(vaultService),
		vaultstate.WithKeys(vaultService),
		vaultstate.WithIdentities(vaultService),
		vaultstate.WithGroups(vaultService),
		vaultstate.WithProxyProfiles(vaultService),
		vaultstate.WithKnownHosts(vaultService),
		vaultstate.WithSnippets(vaultService),
		vaultstate.WithNotes(vaultService),
		vaultstate.WithNoteFileWriter(noteFileWriter{}),
		vaultstate.WithImports(vaultService),
		vaultstate.WithImportPicker(importFilePicker{locale: func() string { return mygo.App.Locale() }}),
		vaultstate.WithExports(vaultService),
		vaultstate.WithExportWriter(noteFileWriter{}),
		vaultstate.WithBackups(backupService),
		vaultstate.WithBackupAfterRestore(func() { _ = forwardingState.Reload() }),
		vaultstate.WithSnippetRunner(snippetRunner{
			// The runner resolves both pointers lazily: vaultState is built
			// before the Session service and shell state exist.
			shell:    func() *state.State { return shellState },
			sessions: func() *appsession.Service { return sessions },
		}),
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
		localpty.Factory{}, adapterserial.Factory{}, connections, routes,
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
		// Every connect attempt, successful or failed, becomes one connection
		// log row. Failures record the error so the Logs surface can explain
		// them; local attempts keep an empty Host identity.
		appsession.WithConnectionHook(func(event appsession.ConnectionEvent) {
			if historyService == nil {
				return
			}
			_ = historyService.RecordConnection(context.Background(),
				connectionLogFromEvent(event))
		}),
		// Commands that ran in a Session feed autocomplete under that Session's
		// scope. The Host scope keeps an SSH Host's history separate.
		// RecordEntry updates autocomplete's aggregation and forwards the same
		// per-execution row to the shared history journal, so a command is
		// persisted exactly once.
		appsession.WithCommandHook(func(id model.SessionID, scope, command string) {
			hostID, label, kind, ok := sessions.SessionInfo(id)
			// A command from a typed Host has no identity but is not a local
			// shell: keep the typed label and mark the row unsaved so the
			// History surface and its Local filter do not call it local.
			unsaved := ok && hostID == "" && kind != appsession.KindLocal
			if hostID == "" && !unsaved {
				label = ""
			}
			autocompleteService.RecordEntry(domainhistory.Entry{
				SessionID:   id,
				HostID:      hostID,
				HostLabel:   label,
				UnsavedHost: unsaved,
				Scope:       scope,
				Command:     command,
				Source:      domainhistory.SourceSession,
			})
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
	// The external MCP listener publishes an authenticated loopback pointer so
	// the natcatty-mcp stdio process can reach the same application services.
	// It is read-only in this build; a bind or discovery failure is reported
	// without blocking startup.
	externalMCPServer, err = startExternalMCP(context.Background(), newMCPReadBridge(
		vaultService, forwardingManager, sessions, transferService,
	), externalMCPDiscoveryPath(userData), func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "natcatty: external MCP:", err)
	}
	runningExternalMCP = externalMCPServer
	// The Workspace restores Tabs, splits, and working directories at startup
	// and persists them after every structural change. Output is never stored.
	workspaceService := appworkspace.NewService(database.Workspace())
	logsState = logsui.NewState(historyService, terminalActionsFunc(func(
		sessionID model.SessionID, hostID model.HostID) error {
		if shellState == nil {
			return state.ErrNoSessions
		}
		err := shellState.OpenHistoryTarget(sessionID, hostID)
		if errors.Is(err, state.ErrHostUnavailable) {
			return logsui.ErrHostMissing
		}
		return err
	}), logsui.WithRefreshRequest(invalidate),
		logsui.WithMutationHook(func() {
			// Autocomplete aggregates the same journal; a deleted or cleared
			// row must stop being suggested without a restart.
			autocompleteService.Load(context.Background())
		}))
	// The Quick Switcher and command palette read live Vault and Tab state.
	// Their closures resolve the shell and registry lazily, as both are
	// assigned below, and the records rerun on every frame.
	paletteSource := &palette{
		vault:    vaultState,
		shell:    func() *state.State { return shellState },
		registry: func() *command.Registry { return registry },
		bindings: keybindingService,
	}
	overlayState := uisearch.New(
		uisearch.WithSource(paletteSource.records),
		uisearch.WithOpen(paletteSource.open),
		uisearch.WithSection(searchSection),
	)
	// The serial dialog enumerates with the same adapter that opens the port, so
	// the names it offers are the names the transport accepts. The opener
	// closure resolves the shell lazily, as the dialog is built just before it.
	serialDialog := serialui.NewState(
		adapterserial.Factory{}.ListPorts,
		func(cfg domainserial.Config) error {
			return shellState.OpenSerial(cfg)
		},
		serialui.WithRedraw(invalidate),
	)
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
				// The terminal font and its catalog bindings are read when a
				// terminal is created; an Appearance change restyles open
				// terminals through State.SetTerminalFont.
				sessionui.WithTerminalFont(func() terminal.Font {
					appearance := preferenceService.Settings().Appearance
					return terminal.Font{
						Family: appearance.TerminalFontFamily,
						Size:   float32(appearance.TerminalFontSize),
					}
				}),
				sessionui.WithTerminalTheme(currentTerminalTheme),
				sessionui.WithTerminalKeybinds(func() []terminal.Keybind {
					return terminalKeybinds(keybindingService)
				}),
				// Reconnect is the original's manual retry: it abandons the
				// current transport and dials again without the policy delay.
				sessionui.WithReconnect(func() { s.ReconnectNow() }, s.CanReconnect),
			)
		}),
		state.WithVault(vaultui.View(vaultState, vaultui.WithForwarding(forwardingState))),
		state.WithLogs(logsui.View(logsState)),
		state.WithSFTP(transferui.View(transferState)),
		state.WithSearch(overlayState),
		state.WithSerialDialog(serialui.View(serialDialog), serialDialog.Show),
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
	registry = newRegistry(shellState, mygo.App.Quit, gate.Lock, vaultState, func() {
		if windows != nil {
			windows.FocusMain()
		}
	})

	// Every window is created through this manager, which wraps its content in
	// the one App Lock gate and owns close/exit intent.
	windows = newWindowManager(
		mygoWindowHost{},
		nativeConfirmer{},
		gate,
		shellState,
		func() func(*ui.Context) {
			return settings.ViewWithSettings(settings.Config{
				Lock:        settingsState,
				Desktop:     desktopState,
				Terminal:    terminalState,
				Themes:      themesState,
				Preferences: preferencesState,
				Shortcuts:   shortcutsState,
				Search:      searchState,
			})
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
	// The Security preference governs close intent live.
	windows.SetSessionConfirmation(func() bool {
		return preferenceService.Settings().Security.ConfirmCloseSessions
	})

	// currentTerminalTheme resolves the stored scheme ID to a catalog entry.
	// A malformed or removed ID falls back to the original's Netcatty Dark
	// instead of leaving a terminal with MyGo's default palette.
	currentTerminalTheme = func() domaintheme.TerminalTheme {
		return resolveTerminalTheme(preferenceService.Settings().Appearance.TerminalTheme,
			themeService.Find)
	}

	// resolveUITheme maps the selected window mode, light/dark preset, accent,
	// and interface font onto MyGo's widget theme. System mode follows the
	// desktop's live appearance through MyGo's theme module.
	windows.SetThemeProvider(func() *ui.Theme {
		return resolveUITheme(preferenceService.Settings().Appearance, mygo.Theme.IsDark())
	})
	desktopCtl.SetIconProvider(func() []byte {
		return trayIconPNG(preferenceService.Settings().Appearance.AppIcon)
	})

	// Appearance changes apply without reopening a window or a Session. The
	// values that only seed a future surface (terminal bindings, tab layout,
	// SFTP and sync) are read by those surfaces when they are built.
	applyPreferences := func(current domainpreferences.Settings) {
		if windows != nil {
			windows.SetOpacity(current.Appearance.WindowOpacity)
			windows.SetIconProvider(func() []byte { return appIconPNG(current.Appearance.AppIcon) })
		}
		if desktopCtl != nil {
			desktopCtl.SetIconProvider(func() []byte { return trayIconPNG(current.Appearance.AppIcon) })
		}
		if runtime.GOOS == "darwin" {
			_ = mygo.App.Dock.SetIcon(appIconPNG(current.Appearance.AppIcon))
		}
		if shellState != nil {
			shellState.SetTerminalFont(current.Appearance.TerminalFontFamily,
				float32(current.Appearance.TerminalFontSize))
			shellState.SetTerminalTheme(currentTerminalTheme())
		}
		invalidate()
	}
	preferenceService.SetChangeHook(applyPreferences)
	applyPreferences(preferenceService.Settings())
	// System mode follows the desktop: a live appearance change invalidates
	// every window, whose root resolves IsDark again.
	mygo.Theme.OnUpdated(invalidate)

	dispatcher := &bindingDispatcher{
		service:   keybindingService,
		shell:     shellState,
		search:    overlayState,
		transfer:  transferState,
		focusMain: func() { windows.FocusMain() },
		toggleOpacity: func() {
			current := preferenceService.Settings().Appearance.WindowOpacity
			_ = preferenceService.Update(func(next *domainpreferences.Settings) {
				next.Appearance.WindowOpacity = toggleOpacityTarget(current)
			})
		},
	}

	// The selftest build installs the F11 keyboard facility here; the normal
	// build's installer is a no-op.
	installSelftest(windows, shellState, desktopCtl, secondInstance)

	// MyGo's native surface has no TranslateAccelerator path: a menu label's
	// shortcut only works if the app dispatches it. Build the table from the
	// same menu the OS shows, so the labels stay truthful and follow the
	// active keyboard scheme.
	setMenu := func(tag string) {
		currentMenu = BuildMenuWithBindings(registry, tag,
			menuBindingSource{service: keybindingService})
		mygo.App.SetMenu(currentMenu)
		windows.SetAccelerators(MenuAccelerators(currentMenu))
		windows.SetShortcuts(dispatcher.Shortcuts())
	}
	// A binding change rebuilds both tables and repaints every window. New
	// terminals read the new terminal bindings; open terminals keep the table
	// MyGo captured when their terminal was created.
	keybindingService.SetChangeHook(func(appkeybindings.State) {
		setMenu(mygo.App.Locale())
		invalidate()
	})

	// A Session's end may run on any goroutine; Invalidate is safe from
	// any goroutine and asks for the frame that shows the new status.
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		shellState.Persist()
		// Stop accepting external MCP calls before the services they read are
		// torn down, then remove the discovery pointer if it is still ours.
		externalMCPServer.Close()
		closeForwarding(forwardingManager, forwardingState)
		desktopCtl.Shutdown()
		// Stop the queue before the surface closes the Tab filesystems its
		// rows are still reading and writing.
		transferQueue.Shutdown()
		transferState.Shutdown()
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
		setMenu(mygo.App.Locale())
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
			// OSC 7 is the only source of a shell's working directory. The
			// surface decides whether follow-cwd is on and whether this
			// Session's Host has a pane; reporting here costs nothing when it
			// is off.
			forwardTerminalCwd(sessions, transferState, snapshot)
		})

		// Tabs, splits, and working directories come back; their Sessions open
		// asynchronously and apply on the next frame. Output does not survive.
		layout := preferenceService.Settings().Layout
		if layout.RestorePreviousSession {
			shellState.Restore(context.Background())
		} else if layout.StartupLanding == domainpreferences.StartupVault {
			shellState.ShowVault()
		} else {
			_ = shellState.NewTab()
		}

		mygo.App.OnLocaleChanged(func(tag string) {
			windows.ApplyLocale()
			setMenu(tag)
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
func newRegistry(shellState *state.State, quit func(), lockApp func() error,
	vaultState *vaultstate.State, focusMain func()) *command.Registry {
	registry := command.NewRegistry()
	register := func(id command.ID, handler command.Handler) {
		if err := registry.Register(id, handler); err != nil {
			panic(err)
		}
	}

	register(command.NewTab, func(context.Context) error {
		return shellState.NewTab()
	})
	register(command.ConnectSerial, func(context.Context) error {
		shellState.RequestSerial()
		return nil
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
	register(command.OpenSettings, func(context.Context) error {
		shellState.RequestSettings()
		return nil
	})
	register(command.OpenBackups, func(context.Context) error {
		if vaultState == nil {
			return nil
		}
		vaultState.BeginBackups()
		shellState.ShowVault()
		if focusMain != nil {
			focusMain()
		}
		return nil
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

// importLegacyCommandHistory moves the pre-journal aggregate file into the
// shared per-execution store. Rows already present are left alone, so running
// the migration on every launch is idempotent.
func importLegacyCommandHistory(ctx context.Context, service *apphistory.Service,
	legacy *store.CommandHistoryStore) {
	if service == nil || legacy == nil {
		return
	}
	entries, err := legacy.Load(ctx)
	if err != nil {
		return
	}
	stored, err := service.Entries(ctx)
	if err != nil {
		return
	}
	seen := make(map[string]bool, len(stored))
	for _, entry := range stored {
		seen[entry.Scope+"\x00"+entry.Command] = true
	}
	for _, entry := range entries {
		key := entry.Scope + "\x00" + entry.Command
		if entry.Command == "" || entry.Scope == "" || seen[key] {
			continue
		}
		recordedAt := entry.LastUsed
		if recordedAt.IsZero() {
			recordedAt = time.Now()
		}
		if err := service.Record(ctx, domainhistory.Entry{
			Scope:      entry.Scope,
			Command:    entry.Command,
			Source:     domainhistory.SourceSession,
			RecordedAt: recordedAt,
		}); err != nil {
			return
		}
		seen[key] = true
	}
}

// importShellHistory persists the local shell's deduplicated commands as
// shell-history rows. Without it the History surface's shell-history source
// would exist only as an autocomplete suggestion. Commands already imported
// are skipped, so repeated launches do not duplicate them.
func importShellHistory(ctx context.Context, service *apphistory.Service,
	source *histfile.Source) {
	if service == nil || source == nil {
		return
	}
	entries := source.History()
	if len(entries) == 0 {
		return
	}
	stored, err := service.Entries(ctx)
	if err != nil {
		return
	}
	seen := make(map[string]bool, len(stored))
	for _, entry := range stored {
		if entry.Source == domainhistory.SourceShellHistory {
			seen[entry.Command] = true
		}
	}
	for _, entry := range entries {
		if entry.Command == "" || seen[entry.Command] {
			continue
		}
		recordedAt := time.Now()
		if entry.Timestamp > 0 {
			recordedAt = time.Unix(entry.Timestamp, 0)
		}
		if err := service.Record(ctx, domainhistory.Entry{
			Scope:      domainhistory.NormalizeScope(""),
			Command:    entry.Command,
			Source:     domainhistory.SourceShellHistory,
			RecordedAt: recordedAt,
		}); err != nil {
			return
		}
		seen[entry.Command] = true
	}
}

// connectionLogFromEvent maps one Session connection outcome to the persisted
// log row. Classification (local versus an unsaved Host, and dropping the
// local application label) belongs to domain/history.NormalizeConnectionLog,
// which every recorded row passes through.
func connectionLogFromEvent(event appsession.ConnectionEvent) domainhistory.ConnectionLog {
	errorText := ""
	if event.Err != nil {
		errorText = event.Err.Error()
	}
	return domainhistory.ConnectionLog{
		SessionID:   event.SessionID,
		HostID:      event.HostID,
		HostLabel:   event.Label,
		Hostname:    event.Hostname,
		Username:    event.Username,
		UnsavedHost: event.UnsavedHost,
		Kind:        string(event.Kind),
		Error:       errorText,
		StartedAt:   event.StartedAt,
		EndedAt:     event.EndedAt,
	}
}

// terminalActionsFunc adapts a function to the Logs surface's row action.
type terminalActionsFunc func(model.SessionID, model.HostID) error

// OpenTerminal opens or focuses the terminal related to a log or history row.
func (fn terminalActionsFunc) OpenTerminal(sessionID model.SessionID, hostID model.HostID) error {
	return fn(sessionID, hostID)
}
