// Package app is the composition root: it constructs the windows, the shell
// state, the command registry, and the native menu, and wires MyGo's
// application lifecycle to them. It is the only package besides main that
// knows the whole application graph.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	adapteret "github.com/mslxl/natcatty/adapters/et"
	adaptergdrive "github.com/mslxl/natcatty/adapters/gdrive"
	adaptergithub "github.com/mslxl/natcatty/adapters/github"
	"github.com/mslxl/natcatty/adapters/histfile"
	adapterslaunch "github.com/mslxl/natcatty/adapters/launch"
	"github.com/mslxl/natcatty/adapters/localpty"
	adapteronedrive "github.com/mslxl/natcatty/adapters/onedrive"
	adapters3 "github.com/mslxl/natcatty/adapters/s3"
	adapterserial "github.com/mslxl/natcatty/adapters/serial"
	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncbackup "github.com/mslxl/natcatty/adapters/syncbackup"
	adaptersyncedsettings "github.com/mslxl/natcatty/adapters/syncedsettings"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	"github.com/mslxl/natcatty/adapters/textcodec"
	adapterbackup "github.com/mslxl/natcatty/adapters/vaultbackup"
	adapterwebdav "github.com/mslxl/natcatty/adapters/webdav"
	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/application/autocomplete"
	"github.com/mslxl/natcatty/application/command"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appfileopen "github.com/mslxl/natcatty/application/fileopen"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	apphistory "github.com/mslxl/natcatty/application/history"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appmonitor "github.com/mslxl/natcatty/application/monitor"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	appsync "github.com/mslxl/natcatty/application/sync"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
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
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	editorui "github.com/mslxl/natcatty/ui/editor"
	uiforwarding "github.com/mslxl/natcatty/ui/forwarding"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	logsui "github.com/mslxl/natcatty/ui/logs"
	uimonitoring "github.com/mslxl/natcatty/ui/monitoring"
	uisearch "github.com/mslxl/natcatty/ui/search"
	serialui "github.com/mslxl/natcatty/ui/serial"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell/state"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
	transferui "github.com/mslxl/natcatty/ui/transfer"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultsection "github.com/mslxl/natcatty/ui/vault/section"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// Run starts Natcatty and blocks until the application quits.
func Run() error {
	// The packaged-artifact smoke mode is dormant unless the complete
	// environment contract is present. It isolates user data before any store
	// opens, so the real Vault and preferences are never touched.
	smoke, smokeActive, err := packagedSmokeFromEnv(os.Getenv)
	if err != nil {
		writePackagedSmokeActivationFailure(os.Getenv, err)
		return err
	}
	if !smokeActive {
		return run(nil, runOptions{})
	}
	if err := smoke.prepare(); err != nil {
		smoke.writeStartupFailure(err)
		return err
	}
	mygo.App.SetPath(mygo.PathUserData, smoke.cfg.ProfileDir)
	err = run(smoke.afterReady, runOptions{packagedSmoke: true, secretService: smoke.secretService})
	if !smoke.didFinish() {
		// The app can exit before readiness (for example when another
		// instance owns the single-instance lock). Never let that look like a
		// green smoke run.
		if err == nil {
			err = errors.New("packaged smoke: the app exited before the smoke ran")
		}
		smoke.writeStartupFailure(err)
	}
	return err
}

// runOptions are the composition-root decisions that must be made before the
// stores and services are built. They are explicit arguments, never globals.
type runOptions struct {
	// packagedSmoke keeps the isolated smoke process free of listeners. It
	// skips the single-instance lock, which would hand the launch to a running
	// app, and does not start the external MCP loopback listener.
	packagedSmoke bool
	// secretService, when set, scopes the OS credential store to this service.
	// The packaged smoke sets it to a per-profile name so an isolated run never
	// reads or writes the user's real entries.
	secretService string
	// secrets, when set, replaces the OS credential store outright. Debug
	// harnesses pass an in-memory store so an unattended run never reaches
	// the keychain or waits on its permission prompt.
	secrets platformsecrets.Store
}

// importsShellHistory reports whether the composition root may read the real
// shell history files. The packaged smoke keeps the real ~/.zsh_history and
// friends out of the isolated profile.
func (o runOptions) importsShellHistory() bool { return !o.packagedSmoke }

// registersURLScheme reports whether the app may claim the OS ssh handler. A
// packaged smoke must not switch the user's real handler to a temporary build.
func (o runOptions) registersURLScheme() bool { return !o.packagedSmoke }

// run is the composition root proper. afterReady, when non-nil, runs on the UI
// thread after the main window exists; the smoke modes use it to assert
// readiness, optionally drive real Sessions, and quit.
func run(afterReady func(hostWindow, packagedSmokeDeps), options runOptions) error {
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
	var licensesState *settings.LicensesState
	var currentTerminalTheme func() domaintheme.TerminalTheme
	var keybindingService *appkeybindings.Service
	var transferState *transferstate.State
	var filesState *transferstate.State
	var fileOpenService *appfileopen.Service
	var editorState *editorui.State
	var associationsState *settings.AssociationsState
	var shellState *state.State
	var preferencesState *settings.PreferencesState
	var shortcutsState *settings.ShortcutsState
	var searchState *settings.SearchState
	var syncService *appsync.Service
	var vaultState *vaultstate.State
	var syncState *settings.SyncState
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
	// the target is queued.
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
	if !options.packagedSmoke && !singleInstancePrimary(mygoInstanceLock{}, secondInstance) {
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

	// notifySyncLocalChange feeds portable-setting writes into the same
	// debounced automatic sync as Vault writes. The sync service ignores the
	// call while stopped or while auto-sync is off.
	notifySyncLocalChange := func() {
		if syncService != nil {
			syncService.NotifyLocalChange()
		}
	}

	// openSettings is provided by the window manager below: one Settings
	// window per app, as ADR 0006 requires.

	userData, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return err
	}
	if err := setupBrowserStores(userData); err != nil {
		return err
	}
	browserBookmarks.Subscribe(notifySyncLocalChange)

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
			// A lowered Connection Log retention limit takes effect
			// immediately instead of waiting for the next recorded command.
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
			notifySyncLocalChange()
		}),
	)
	_ = terminalService.Load(context.Background())
	terminalState = settings.NewTerminalState(terminalService,
		settings.WithTerminalRedrawRequest(invalidate))

	// Non-AI preferences and the keyboard-binding document live beside the
	// terminal settings. Both load before the shell exists, so a new Session
	// reads the current font and key bindings.
	preferenceService = apppreferences.NewService(
		store.OpenPreferences(filepath.Join(userData, "preferences.json")),
		apppreferences.WithChangeHook(func(domainpreferences.Settings) {
			invalidate()
			notifySyncLocalChange()
		}))
	if err := preferenceService.Load(context.Background()); err != nil {
		return err
	}
	preferencesState = settings.NewPreferencesState(preferenceService,
		settings.WithPreferencesRedrawRequest(invalidate),
		settings.WithLocaleRequest(func(tag string) { mygo.App.SetLocale(tag) }),
		settings.WithIconArtwork(appIconPNG))

	// File associations and the default opener live in the same preferences
	// document. The launch adapter is the only platform-aware piece. The
	// internal editor and the external-edit observer are wired after the
	// service is built because the editor writes through it.
	fileOpenService = appfileopen.New(preferenceService, adapterslaunch.New())
	editorState = editorui.NewState(fileOpenService,
		editorui.WithRedrawRequest(invalidate))
	fileOpenService.SetEditor(editorState)
	fileOpenService.SetObserver(editorState.Notify)
	// A previous process may have left a materialized remote copy behind. Sweep
	// before the first open so it cannot accumulate across restarts.
	if _, err := fileOpenService.SweepTemp(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "natcatty: editor temp sweep:", err)
	}
	associationsState = settings.NewAssociationsState(preferenceService,
		settings.WithApplicationPicker(chooseOpenApplication),
		settings.WithAssociationsRedrawRequest(invalidate))

	// Imported terminal color schemes live beside the preferences. The
	// preference stores only the scheme ID, so an import is a second document
	// that must load before a Session resolves its colors.
	themeService = appthemes.NewService(
		store.OpenThemes(filepath.Join(userData, "themes.json")),
		appthemes.WithChangeHook(func([]domaintheme.TerminalTheme) {
			invalidate()
			notifySyncLocalChange()
		}))
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
				// An imported scheme is a choice the terminal must show, so
				// it stops following the app.
				next.Appearance.FollowAppTerminalTheme = false
			})
			return err == nil
		}),
		settings.WithThemesRedrawRequest(invalidate),
	)

	keybindingService = appkeybindings.NewService(
		store.OpenKeyBindings(filepath.Join(userData, "keybindings.json")),
		appkeybindings.WithGOOS(runtime.GOOS),
		appkeybindings.WithChangeHook(func(appkeybindings.State) {
			invalidate()
			notifySyncLocalChange()
		}))
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

	// Connection logs and Command History share the Vault database. Only
	// Connection Log retention is configurable; Command History is never
	// pruned automatically. The limit is read live from Settings on every
	// record and prune, so a change applies without rebuilding the service.
	historyService = apphistory.NewService(database,
		apphistory.WithRetention(func() apphistory.Retention {
			current := terminalService.Settings()
			return apphistory.Retention{
				Logs: domainhistory.Retention{
					Count:  current.ConnectionLogRetentionCount,
					MaxAge: time.Duration(current.ConnectionLogRetentionDays) * 24 * time.Hour,
				},
			}
		}),
	)
	// Apply Connection Log retention once at startup so a lowered limit takes
	// effect even before the next record arrives.
	_, _ = historyService.Prune(context.Background())

	// Command History is the only autocomplete source. The local shell history
	// files are imported once into that journal; they are never queried live.
	// Both the History surface and autocomplete read the same per-execution
	// rows, so the two views never fork the records.
	home, _ := os.UserHomeDir()
	shellHistory := histfile.Default(home)
	// The packaged smoke skips the real shell files: its result must not
	// depend on, or copy, the user's history.
	if options.importsShellHistory() {
		importShellHistory(context.Background(), historyService, shellHistory)
	}
	autocompleteService := autocomplete.NewService(
		historyService,
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
	)
	autocompleteService.Load(context.Background())

	// The packaged smoke scopes OS credential-store entries to a per-profile
	// service. The store is still the platform keychain, but an isolated run
	// neither reads nor writes the user's real entries.
	secretBackend := platformsecrets.Store(platformsecrets.System{})
	if options.secretService != "" {
		secretBackend = platformsecrets.Scoped{Service: options.secretService}
	}
	if options.secrets != nil {
		secretBackend = options.secrets
	}

	// Host credentials live in the field-secret store. It reports OS,
	// passphrase, or session mode; it never falls back to plaintext.
	secretStore, err := store.OpenSecretStore(context.Background(), database, secretBackend)
	if err != nil {
		return err
	}
	defer secretStore.Close()

	// Local backups snapshot the Vault after committed changes. The store keeps
	// its own keychain key, separate from the field-secret key, so a backup can
	// be written even when credential support is in session-only mode.
	backupStore := adapterbackup.Open(
		filepath.Join(userData, "vault-backups"), secretBackend)
	backupService := appbackup.NewService(database, backupStore,
		appbackup.WithRetentionStore(backupStore))
	backupService.Start()
	defer backupService.Stop()
	// One committed Vault write feeds both the local backup queue and the
	// cloud automatic sync debounce. Neither hook may block the write.
	database.SetChangeHook(func() {
		backupService.NotifyVaultChanged()
		if syncService != nil {
			syncService.NotifyLocalChange()
		}
	})

	// Embedded Network Providers keep per-Profile runtime state here. The path
	// is inside the app's user data, never a system Tailscale directory, and is
	// deliberately not part of the SQLite Vault that backup and sync carry.
	networkStateBase := filepath.Join(userData, "network-providers")
	// Profile edits and deletes must retire the live Provider and its idle
	// Connections. The Vault service is built first, so the runtime is bound to
	// the registry and Manager once they exist.
	networkRuntime := &networkProfileRuntime{}
	vaultService := appvault.NewService(
		database,
		appvault.WithSecrets(secretStore),
		appvault.WithKeys(database),
		appvault.WithIdentities(database),
		appvault.WithGroups(database),
		appvault.WithProxyProfiles(database),
		appvault.WithNetworkProfiles(database),
		appvault.WithNetworkProfileStateCleaner(networkStateCleaner{base: networkStateBase}),
		appvault.WithNetworkProfileChanged(networkRuntime.InvalidateProfile),
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

	// Cloud sync: the Vault adapter reads the SQLite records and decrypts field
	// credentials for the encrypted payload; applying a payload re-encrypts them
	// under this device's key. Both GitHub base URLs are injectable so GUI
	// acceptance can point the whole device flow and Gist surface at a local
	// fake server without touching the real network.
	syncConfigStore := adaptersyncstore.OpenConfig(filepath.Join(userData, "cloud-sync.json"))
	syncProfile, _ := syncConfigStore.Load(context.Background())
	deviceID := syncProfile.DeviceID
	if deviceID == "" {
		deviceID = newSyncDeviceID()
	}
	deviceName, _ := os.Hostname()
	version := mygo.App.Version()
	if version == "" {
		version = "dev"
	}
	githubClient := adaptergithub.New(adaptergithub.Options{
		APIBase:  os.Getenv("NATCATTY_GITHUB_API_BASE"),
		AuthBase: syncGitHubAuthBase(),
		ClientID: os.Getenv("NATCATTY_GITHUB_CLIENT_ID"),
	})
	// Google Drive uses an authorization-code grant in the system browser.
	// The NATCATTY_GDRIVE_* overrides point the whole app at the local fake for
	// acceptance runs; production leaves them empty.
	googleClient := adaptergdrive.New(adaptergdrive.OptionsFromEnv())
	// OneDrive uses the same installed-app shape against Microsoft's consumer
	// tenant. NATCATTY_ONEDRIVE_* overrides point the whole app at the local
	// fake for acceptance runs; production leaves them empty.
	oneDriveClient := adapteronedrive.New(adapteronedrive.OptionsFromEnv())
	// WebDAV has no OAuth endpoints: the user supplies the collection URL and
	// a Basic, Digest, or bearer credential. The adapter never disables TLS
	// verification and refuses unsafe redirects.
	webDAVClient := adapterwebdav.New(adapterwebdav.Options{})
	// S3-compatible storage is addressed path-style with SigV4. The signer is
	// in-house; certificates are never skipped, and a wrong-region redirect is
	// reported rather than followed.
	s3Client := adapters3.New(adapters3.Options{})
	// The global SFTP bookmark list is portable, so it is both the transfer
	// service's repository and a cloud-sync document. The wrapper reports only
	// global saves; local-pane bookmarks stay device-local.
	sftpBookmarks := notifyingSFTPBookmarks{
		BookmarkRepository: adaptersftp.NewBookmarks(
			filepath.Join(userData, "sftp-bookmarks.json"),
			filepath.Join(userData, "sftp-local-bookmarks.json"),
		),
		notify: notifySyncLocalChange,
	}
	syncedSettings := adaptersyncedsettings.New(
		preferenceService, terminalService, keybindingService, themeService,
		sftpBookmarks, browserBookmarks,
	)
	syncService = appsync.NewService(
		githubClient,
		adaptersyncvault.New(database, secretStore, adaptersyncvault.WithSettings(syncedSettings)),
		syncConfigStore,
		adaptersyncstore.NewTokenStore(secretBackend),
		appsync.WithDevice(deviceID, deviceName, version),
		appsync.WithRemote(appsync.ProviderGoogleDrive, googleClient),
		appsync.WithRemote(appsync.ProviderOneDrive, oneDriveClient),
		appsync.WithRemote(appsync.ProviderWebDAV, webDAVClient),
		appsync.WithRemote(appsync.ProviderS3, s3Client),
		appsync.WithBrowserOpener(mygo.Shell.OpenExternal),
		appsync.WithRedrawRequest(invalidate),
		// Protective restore points come from the same local backup service
		// the Vault editor already uses, including its retention policy.
		appsync.WithBackups(adaptersyncbackup.New(backupService)),
		// A downloaded cloud copy rewrites the Vault families. Mark the live
		// Vault state stale so its lists refresh on the next frame without a
		// restart. The callback resolves vaultState lazily because the state is
		// built below; it runs on the sync goroutine and only sets the guarded
		// stale flag and requests a redraw.
		appsync.WithAppliedSnapshot(appliedSnapshotRefresh(func() *vaultstate.State {
			return vaultState
		})),
	)
	if err := syncService.Load(context.Background()); err != nil {
		return err
	}
	defer syncService.Stop()
	syncState = settings.NewSyncState(syncService, settings.WithSyncRedrawRequest(invalidate))

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

	// A persisted host-key trust decision marks the Vault's Known Host list
	// stale through this pointer, which is declared with the app's other
	// surface states so the sync service above can resolve it lazily.

	// One SSH Connection per Session. The lease seam is what tickets 08 and 09
	// extend into jump chains and a shared pool. Hardware security keys go
	// through the same system OpenSSH agent as ordinary agent identities.
	securityKeys := sshadapter.NewSecurityKeyAgent()
	// One in-process Network Provider per Network Profile, started lazily.
	// The factory dispatches on the Profile's kind: Tailscale Net uses the
	// embedded tsnet adapter and WireGuard Net the in-process wireguard-go
	// netstack device. Neither creates a TUN interface or needs root. A Provider
	// failure is reported as KindProvider and never becomes a direct dial. The
	// state directory lives under this app's user data, isolated per Profile;
	// no system tailscaled state or root access is used.
	networkStatusSink := &networkProviderStatusSink{
		// vaultState is assigned later; resolve it when a status arrives.
		vault:   func() *vaultstate.State { return vaultState },
		history: historyService,
	}
	networkProviderFactory := &networkProviderFactory{
		vault:            vaultService,
		stateBase:        networkStateBase,
		observe:          networkStatusSink.Observe,
		observeWireGuard: networkStatusSink.ObserveWireGuard,
	}
	networkProviders := appconnection.NewNetworkProviderRegistry(networkProviderFactory)
	// The Vault's interactive-login actions drive the same registry without a
	// Host dial. Starting here keeps the Auth URL flow independent of any one
	// connection attempt; cancel/retry act on this profile's node only.
	networkProviderControl := &networkProviderController{
		vault:    vaultService,
		registry: networkProviders,
	}
	// Registry shutdown rides on connections.CloseAll.
	connections := appconnection.NewManager(
		sshadapter.NewDialer(sshadapter.WithSecurityKeyAgent(securityKeys)),
		vaultPasswords{vault: vaultService},
		database,
		appconnection.WithNetworkProviders(networkProviders),
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
	networkRuntime.providers = networkProviders
	networkRuntime.connections = connections
	// A Provider that stops, fails, or loses authorization makes every parked
	// transport for its Profile unusable. Drop those idle Connections now; an
	// active lease keeps its transport and fails typed on its next I/O.
	networkStatusSink.onPhase = networkRuntime.PhaseChanged

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
		apptransfer.WithBookmarks(sftpBookmarks),
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
	transferOptions := []transferstate.Option{
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
		// Opening a file resolves its association at call time, so a Settings
		// change applies without reopening the pane.
		transferstate.WithFileOpener(fileOpenService.Open),
		// An external application's edit is watched through its pane's Tab, so
		// closing the Tab releases the session and its materialized copy.
		transferstate.WithFileSessionCloser(fileOpenService.Close),
	}
	transferState = transferstate.NewState(transferService, transferOptions...)
	// A Session Tab's SFTP panel browses its Host on a state of its own, so
	// the SFTP Tab's two panes keep what the user chose there.
	filesState = transferstate.NewState(transferService, transferOptions...)

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

	// Monitoring samples Hosts through the same Connection Manager and route
	// source as a Session, so a Host's key must be trusted before its first
	// metric command. The surface starts and stops sampling as it becomes
	// visible; the service owns every sampler goroutine.
	monitorService := appmonitor.NewService(monitorConnector{
		routes:      routes,
		connections: connections,
	})
	monitorState := uimonitoring.NewState(
		monitorService,
		vaultService,
		preferenceService,
		func(host domainvault.Host) error {
			if shellState == nil {
				return errors.New("monitoring: the shell is not ready")
			}
			return shellState.OpenHost(host)
		},
		uimonitoring.WithRedrawRequest(invalidate),
	)

	// The System manager samples and acts through the same Connection Manager
	// and route source as a Session. Every exec and every confirmed kill or
	// systemctl call opens a trusted channel lease, so a Host's key must be
	// accepted before any system command runs.
	sysManagerService := appsysmanager.NewService(sysmanagerConnector{
		routes:      routes,
		connections: connections,
	})
	sysManagerState := uisysmanager.NewState(
		sysManagerService,
		vaultService,
		preferenceService,
		uisysmanager.WithRedrawRequest(invalidate),
		// tmux attach opens a terminal Tab against the same Host and sends
		// the attach command once the Session is connected.
		uisysmanager.WithTerminalOpener(func(host domainvault.Host, command string) error {
			if shellState == nil {
				return errors.New("sysmanager: the shell is not ready")
			}
			preview := appsession.TransformCompose(command, appsession.ComposeOptions{})
			return shellState.OpenHostWithStartup(host, state.StartupInput{Chunks: preview.Chunks})
		}),
	)
	// The side panel's System tool samples the Session's own Host, so it has
	// a sampler apart from the System manager page's, which keeps its own.
	sideSystemService := appsysmanager.NewService(sysmanagerConnector{
		routes:      routes,
		connections: connections,
	})
	sideSystemState := uisysmanager.NewState(
		sideSystemService,
		withThisComputer{vaultService},
		preferenceService,
		uisysmanager.WithRedrawRequest(invalidate),
		uisysmanager.WithTerminalOpener(func(host domainvault.Host, command string) error {
			if shellState == nil {
				return errors.New("sysmanager: the shell is not ready")
			}
			preview := appsession.TransformCompose(command, appsession.ComposeOptions{})
			return shellState.OpenHostWithStartup(host, state.StartupInput{Chunks: preview.Chunks})
		}),
	)

	vaultState = vaultstate.NewState(
		vaultService,
		vaultstate.WithSecrets(vaultService),
		vaultstate.WithKeys(vaultService),
		vaultstate.WithIdentities(vaultService),
		vaultstate.WithGroups(vaultService),
		vaultstate.WithProxyProfiles(vaultService),
		vaultstate.WithNetworkProfiles(vaultService),
		vaultstate.WithNetworkProviderActions(networkProviderControl),
		vaultstate.WithNetworkProfileRuntimeUsage(func(profileID string) bool {
			// Neither the Session service nor the Connection Manager exists when
			// the Vault state is built; both are resolved at call time.
			var live liveSessionLister
			if sessions != nil {
				live = sessions
			}
			return networkProfileRuntimeInUse(context.Background(), profileID,
				live, routes, connections)
		}),
		vaultstate.WithKnownHosts(vaultService),
		vaultstate.WithHostCredentials(vaultService),
		vaultstate.WithSnippets(vaultService),
		vaultstate.WithNotes(vaultService),
		vaultstate.WithNoteFileWriter(noteFileWriter{}),
		vaultstate.WithImports(vaultService),
		vaultstate.WithImportPicker(importFilePicker{locale: func() string { return mygo.App.Locale() }}),
		vaultstate.WithWireGuardConfPicker(wireguardConfPicker{locale: func() string { return mygo.App.Locale() }}),
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
		vaultstate.WithSecurityKeyProbe(func(ctx context.Context) appconnection.SecurityKeySummary {
			// Listing identities is not a device operation; three seconds is
			// enough for a healthy agent, and a stuck agent must not pin the
			// probe goroutine forever.
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			return appconnection.SecurityKeyService{Agent: securityKeys}.Summary(probeCtx)
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
		localpty.Factory{}, adapterserial.Factory{},
		newTelnetFactory(adapterstnet.Factory{}, routes, networkProviders), connections, routes,
	).withInProcess(
		newMoshFactory(connections, routes, networkProviders),
		adapteret.InProcessFactory{
			Manager:  connections,
			Routes:   routes,
			Sessions: adapteret.SecretSessionStore{Secrets: secretStore},
		},
	).withPreferences(preferenceService),
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
			// A successful open is the Vault's Recently Connected fact. It is
			// device-local telemetry, so the list is only invalidated here; the
			// UI thread reloads it before the next frame. This covers every
			// connect path, including the palette and a restored Workspace,
			// which never pass through the Vault's own button.
			if event.Err == nil && !event.UnsavedHost && event.HostID != "" && vaultState != nil {
				if err := vaultService.MarkConnected(context.Background(), event.HostID); err == nil {
					vaultState.InvalidateLists()
				}
			}
			if historyService == nil {
				return
			}
			_ = historyService.RecordConnection(context.Background(),
				connectionLogFromEvent(event))
		}),
		// A command reaches history only when it finishes or its Session ends.
		// RecordEntry updates autocomplete's aggregation and forwards the same
		// per-execution row to the shared history journal, so one event is
		// persisted exactly once and autocomplete reads the same row.
		appsession.WithCommandCompletionHook(
			commandCompletionHook(autocompleteService, resolveCommandLocalIdentity())),
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
	// Every request is forced into the synthetic external scope, constrained by
	// the session allowlist, and evaluated against the discovery-backed
	// permission mode before any read runs or any action starts. A bind or
	// discovery failure is reported without blocking startup.
	mcpBridge := newMCPReadBridge(vaultService, forwardingManager, sessions, transferService)
	mcpMode := newMCPModeState(externalMCPDiscoveryPath(userData))
	// The live-Session scope mirrors the original useExternalMcpSessionSync:
	// every Session the shell has open is shared while the External MCP
	// sharing switch is on. The launch-time NETCATTY_MCP_SESSION_IDS value, when
	// present, narrows that set instead of replacing it.
	mcpLive := newMCPLiveSessions(sessions, userData, invalidate)
	allowlist, allowlistSet := mcpSessionAllowlist()
	mcpPolicy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: mcprpc.ExternalScope,
		Allowlist:           allowlist,
		AllowlistSet:        allowlistSet,
		Sessions:            mcpLive,
		Mode:                mcpMode.Get,
		Redraw:              invalidate,
	})
	mcpHandler := newMCPPolicyHandler(mcpBridge, mcpPolicy, sessions, sessions, nil)
	mcpSettingsState := settings.NewMCPState(&mcpSettingsController{
		policy: mcpPolicy, mode: mcpMode, sessions: mcpLive,
	})
	if !options.packagedSmoke {
		externalMCPServer, err = startExternalMCP(context.Background(), mcpBridge,
			externalMCPDiscoveryPath(userData), func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}, withMCPHandler(mcpHandler), withMCPModeState(mcpMode))
		if err != nil {
			fmt.Fprintln(os.Stderr, "natcatty: external MCP:", err)
		}
	}
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
	// The side panel's Scripts, History, Theme, and Notes read the states of
	// the pages they mirror; each keeps its own search.
	vaultSide := vaultui.NewSidePanels(vaultState, func(page vaultsection.Section) {
		if shellState != nil {
			shellState.ShowVaultSection(page)
		}
	})
	historyPanel := logsui.NewHistoryPanel(logsState, func(command string, run bool) bool {
		return shellState != nil && shellState.SendToFocused(command, run)
	})
	themePanel := settings.NewThemePanel(themesState, preferenceService)
	shellState = state.NewState(
		state.WithLocaleRequest(func(tag string) {
			mygo.App.SetLocale(tag)
		}),
		state.WithSettingsRequest(func() {
			if windows != nil {
				windows.OpenSettings()
			}
		}),
		state.WithBrandMark(func() *ui.Bitmap {
			return brandMarkBitmap(preferenceService.Settings().Appearance.AppIcon)
		}),
		// Opening and closing a Tab changes the shell's structure; ask for the
		// frame that shows the new tab strip or Vault placeholder. Every
		// window redraws so a lock or unlock propagates.
		state.WithRedrawRequest(invalidate),
		// Tab titles follow the shell-reported OSC 0/2 title according to the
		// live setting, and a Host rename reaches its already-open Tabs.
		state.WithDynamicTabTitleMode(func() domainterminal.DynamicTabTitleMode {
			return terminalService.Settings().DynamicTabTitleMode
		}),
		state.WithHostLabel(func(id model.HostID) (string, bool) {
			if vaultState == nil {
				return "", false
			}
			host, ok := vaultState.EffectiveHost(id)
			if !ok {
				return "", false
			}
			return host.Label, true
		}),
		state.WithSessions(sessions, func(s *appsession.Session) (state.Surface, error) {
			options := []sessionui.Option{
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
				// The terminal font and its catalog bindings are read when a
				// terminal is created; an Appearance change restyles open
				// terminals through State.SetTerminalFont.
				sessionui.WithTerminalFont(func() terminal.Font {
					appearance := preferenceService.Settings().Appearance
					return terminal.Font{
						Family: domainpreferences.TerminalFontFamily(appearance.TerminalFontFamily),
						Size:   float32(appearance.TerminalFontSize),
					}
				}),
				sessionui.WithTerminalCursor(func() (domainpreferences.CursorStyle, bool) {
					appearance := preferenceService.Settings().Appearance
					return appearance.TerminalCursorStyle, !appearance.TerminalCursorSteady
				}),
				sessionui.WithTerminalTheme(currentTerminalTheme),
				sessionui.WithTerminalKeybinds(func() []terminal.Keybind {
					return terminalKeybinds(keybindingService)
				}),
				// Reconnect is the original's manual retry: it abandons the
				// current transport and dials again without the policy delay.
				sessionui.WithReconnect(func() { s.ReconnectNow() }, s.CanReconnect),
			}
			// An SSH Session with a saved Host routes its browser through the
			// Host, and a local Session reaches this computer directly. Serial,
			// Telnet, Mosh, and ET Sessions leave the menu item disabled
			// instead of offering a route that cannot exist.
			switch {
			case s.Snapshot().Kind == appsession.KindLocal:
				options = append(options, sessionui.WithBrowser(
					func(onClose func()) (sessionui.BrowserPanel, error) {
						return openSessionBrowser(s, windows, localBrowserConnector{}, invalidate, onClose)
					}))
			case s.Snapshot().Kind == appsession.KindSSH && s.HostID() != "":
				options = append(options, sessionui.WithBrowser(
					func(onClose func()) (sessionui.BrowserPanel, error) {
						return openSessionBrowser(s, windows, browserConnector{
							vault:       vaultService,
							routes:      routes,
							connections: connections,
						}, invalidate, onClose)
					}))
			}
			return sessionui.New(s, options...)
		}),
		state.WithVault(vaultui.View(vaultState, vaultui.WithForwarding(forwardingState),
			// The Hosts page's view and sort switches are this device's
			// layout; the preference service persists them.
			vaultui.WithLayout(
				func() domainpreferences.Layout { return preferenceService.Settings().Layout },
				func(change func(*domainpreferences.Layout)) {
					_ = preferenceService.Update(func(next *domainpreferences.Settings) {
						change(&next.Layout)
					})
				},
			),
			// Dual-pane SFTP and the Host browser open from a Host card. Both
			// actions resolve against the live state, so they are wired lazily.
			vaultui.WithOpenSFTP(func(host domainvault.Host) {
				if transferState != nil {
					transferState.OpenRemote(transferstate.Left, host.ID, host.Label)
				}
				if shellState != nil {
					shellState.ShowSFTP()
				}
			}, func() bool {
				return preferenceService.Settings().Layout.ShowSFTPTab
			}),
			vaultui.WithOpenBrowser(func(host domainvault.Host) {
				if shellState != nil {
					_ = shellState.OpenHostBrowser(host)
				}
			}),
			vaultui.WithSection(func() vaultsection.Section {
				if shellState == nil {
					return vaultsection.Hosts
				}
				return shellState.VaultSection()
			}),
			vaultui.WithLocalTerminal(func() {
				if shellState != nil {
					_ = shellState.NewTab()
				}
			}),
			vaultui.WithSerial(func() {
				if shellState != nil {
					shellState.RequestSerial()
				}
			}))),
		state.WithMonitor(uimonitoring.View(monitorState),
			monitorState.Activate, monitorState.Deactivate),
		state.WithSystemManager(uisysmanager.View(sysManagerState),
			sysManagerState.Activate, sysManagerState.Deactivate),
		state.WithLogs(logsui.View(logsState)),
		state.WithSFTP(transferui.View(transferState)),
		state.WithSFTPTabShown(func() bool {
			return preferenceService.Settings().Layout.ShowSFTPTab
		}),
		state.WithFilesPanel(state.FilesPanel{
			View: func(width float32) func(*ui.Context) { return transferui.SidePanel(filesState, width) },
			ShowRemote: func(hostID model.HostID, label string) {
				filesState.ShowRemote(transferstate.Left, hostID, label)
			},
			ShowLocal: func() { filesState.ShowLocal(transferstate.Left) },
			Dock: func() state.PanelDock {
				return state.PanelDock(preferenceService.Settings().Layout.SidePanelDock)
			},
			SetDock: func(dock state.PanelDock) {
				_ = preferenceService.Update(func(next *domainpreferences.Settings) {
					next.Layout.SidePanelDock = domainpreferences.SidePanelDock(dock)
				})
			},
		}),
		// The side panel's other tools, in the original's order.
		state.WithSideTools(
			state.SideTool{ID: state.SideScripts, View: func(state.SideTarget, float32) func(*ui.Context) {
				return vaultSide.Scripts()
			}},
			state.SideTool{ID: state.SideHistory, View: func(target state.SideTarget, _ float32) func(*ui.Context) {
				return historyPanel.View(target.Host)
			}},
			state.SideTool{ID: state.SideTheme, View: func(state.SideTarget, float32) func(*ui.Context) {
				return themePanel.View()
			}},
			state.SideTool{ID: state.SideSystem,
				View: func(target state.SideTarget, _ float32) func(*ui.Context) {
					// A local Session's panel samples this computer.
					host := string(target.Host)
					if host == "" {
						host = thisComputerID
					}
					sideSystemState.ShowHost(host)
					return uisysmanager.SideView(sideSystemState)
				},
				Hide: sideSystemState.Hide},
			state.SideTool{ID: state.SideNotes, View: func(state.SideTarget, float32) func(*ui.Context) {
				return vaultSide.Notes()
			}},
		),
		state.WithEditor(editorui.View(editorState)),
		state.WithSearch(overlayState),
		state.WithSerialDialog(serialui.View(serialDialog), serialDialog.Show),
		state.WithTrustPrompt(trustPrompt),
		state.WithAuthPrompt(authPrompt),
		// The shell clears a stale ET pair when no live Session owns it: a
		// restored Pane a legacy server refuses to reattach, or one closed
		// while its Open never produced a Session.
		state.WithETResumeStore(adapteret.SecretSessionStore{Secrets: secretStore}),
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
	// The write executor is attached after the shell exists because host_open
	// can open a new Tab; the closure resolves the shell lazily so MCP calls
	// arriving at any point after startup still work.
	mcpHandler.attachWrites(newMCPWriteExecutor(
		mcpPolicy, sessions, vaultService, forwardingManager, transferService,
		func(ctx context.Context, host domainvault.Host) (model.SessionID, error) {
			if shellState == nil {
				return "", errors.New("external MCP: the shell is not available")
			}
			return shellState.OpenHostForMCP(ctx, host)
		},
	))
	registry = newRegistry(shellState, mygo.App.Quit, gate.Lock, vaultState, func() {
		if windows != nil {
			windows.FocusMain()
		}
	})

	// The About and Licenses area reads the GPL text, the modification notice,
	// and the third-party notices that ship beside the executable. The source
	// URL names the exact release tag's Corresponding Source.
	licensesState = settings.NewLicensesState(loadLegalDocuments(version),
		settings.WithLicensesSourceOpener(mygo.Shell.OpenExternal))

	// Every window is created through this manager, which wraps its content in
	// the one App Lock gate and owns close/exit intent.
	windows = newWindowManager(
		mygoWindowHost{},
		nativeConfirmer{},
		gate,
		shellState,
		func() func(*ui.Context) {
			return settings.ViewWithSettings(settings.Config{
				Lock:         settingsState,
				Desktop:      desktopState,
				Terminal:     terminalState,
				Themes:       themesState,
				Preferences:  preferencesState,
				Associations: associationsState,
				Shortcuts:    shortcutsState,
				Search:       searchState,
				Sync:         syncState,
				MCP:          mcpSettingsState,
				Licenses:     licensesState,
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

	// currentTerminalTheme resolves the scheme terminals draw with: the one
	// matched to the active UI preset while the terminal follows the app,
	// otherwise the stored scheme. A malformed or removed ID falls back to the
	// original's Netcatty Dark instead of leaving a terminal with MyGo's
	// default palette.
	currentTerminalTheme = func() domaintheme.TerminalTheme {
		return resolveAppTerminalTheme(preferenceService.Settings().Appearance,
			mygo.Theme.IsDark(), themeService.Find)
	}

	// resolveUITheme maps the selected window mode, light/dark preset, accent,
	// and interface font onto MyGo's widget theme. System mode follows the
	// desktop's live appearance through MyGo's theme module.
	windows.SetThemeProvider(func() *ui.Theme {
		return resolveUITheme(preferenceService.Settings().Appearance, mygo.Theme.IsDark())
	})
	// The Interface Style is per device and follows the setting live.
	windows.SetStyleProvider(func() kit.Style {
		return kit.StyleFor(string(preferenceService.Settings().Appearance.InterfaceStyle))
	})
	desktopCtl.SetIconProvider(func() []byte {
		return trayIconPNG(preferenceService.Settings().Appearance.AppIcon)
	})

	// Appearance changes apply without reopening a window or a Session. The
	// values that only seed a future surface (terminal bindings, tab layout,
	// SFTP and sync) are read by those surfaces when they are built.
	applyPreferences := func(current domainpreferences.Settings) {
		if windows != nil {
			// SetOpacity also syncs the windows' vibrancy, so an Interface Style
			// change lands here too.
			windows.SetOpacity(current.Appearance.WindowOpacity)
			windows.SetIconProvider(func() []byte { return appIconPNG(current.Appearance.AppIcon) })
		}
		if desktopCtl != nil {
			desktopCtl.SetIconProvider(func() []byte { return trayIconPNG(current.Appearance.AppIcon) })
		}
		if runtime.GOOS == "darwin" {
			// MyGo panics when Dock.SetIcon runs before App.Run, and the
			// initial preference apply below happens before the event loop
			// starts. WhenReady also schedules immediately after startup, so
			// later appearance changes still update the Dock icon.
			icon := appIconPNG(current.Appearance.AppIcon)
			mygo.App.WhenReady(func() {
				_ = mygo.App.Dock.SetIcon(icon)
			})
		}
		if shellState != nil {
			shellState.SetTerminalFont(domainpreferences.TerminalFontFamily(current.Appearance.TerminalFontFamily),
				float32(current.Appearance.TerminalFontSize))
			shellState.SetTerminalCursor(current.Appearance.TerminalCursorStyle,
				!current.Appearance.TerminalCursorSteady)
			// currentTerminalTheme reads Theme.IsDark, which panics before
			// App.Run; the initial preference apply below happens before the
			// event loop starts. WhenReady also schedules immediately after
			// startup, so later appearance changes still apply the theme at
			// once.
			applyTerminalTheme := func() { shellState.SetTerminalTheme(currentTerminalTheme()) }
			if mygo.App.IsReady() {
				applyTerminalTheme()
			} else {
				mygo.App.WhenReady(applyTerminalTheme)
			}
		}
		invalidate()
	}
	preferenceService.SetChangeHook(applyPreferences)
	applyPreferences(preferenceService.Settings())
	// System mode follows the desktop: a live appearance change invalidates
	// every window, whose root resolves IsDark again, and a terminal that
	// follows the app takes the new appearance's scheme.
	mygo.Theme.OnUpdated(func() {
		if shellState != nil {
			shellState.SetTerminalTheme(currentTerminalTheme())
		}
		invalidate()
	})

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
		// Monitoring leases are connection channels; release them before the
		// manager closes every remaining transport.
		closeMonitoring(monitorState, monitorService)
		// System manager leases are channel leases too; release confirmed
		// actions and samplers before the manager closes every transport.
		closeSystemManager(sysManagerState, sysManagerService)
		closeSystemManager(sideSystemState, sideSystemService)
		desktopCtl.Shutdown()
		// Stop the queue before the surface closes the Tab filesystems its
		// rows are still reading and writing.
		transferQueue.Shutdown()
		transferState.Shutdown()
		filesState.Shutdown()
		editorState.Shutdown()
		// Close every editing session before the surface's connections go
		// away, then remove any materialized copy still on disk.
		fileOpenService.Shutdown()
		syncState.Shutdown()
		syncService.Stop()
		syncService.ClearPassword()
		shellState.Shutdown()
		shutdownSessions(shellState, sessions)
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
		var mcpObserveMu sync.Mutex
		mcpObserved := ""
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
			forwardTerminalCwd(sessions, filesState, snapshot)
			// A Session replaced under the same Host (host re-open, tab
			// replacement) advances the external scope's ownership generation,
			// invalidating prior grants and pending approvals. The hook runs for
			// every output/state change, so only a changed ID/Host set is worth
			// a policy observation.
			identities := sessions.LiveIdentities()
			var signature strings.Builder
			for _, identity := range identities {
				signature.WriteString(string(identity.ID))
				signature.WriteByte('@')
				signature.WriteString(string(identity.HostID))
				signature.WriteByte(';')
			}
			current := signature.String()
			mcpObserveMu.Lock()
			changed := current != mcpObserved
			if changed {
				mcpObserved = current
			}
			mcpObserveMu.Unlock()
			if changed {
				refs := make([]appmcp.SessionRef, 0, len(identities))
				for _, identity := range identities {
					refs = append(refs, appmcp.SessionRef{
						ID:     string(identity.ID),
						HostID: string(identity.HostID),
					})
				}
				mcpPolicy.ObserveSessions(refs)
			}
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
			afterReady(mainWindow, packagedSmokeDeps{
				vault:       vaultService,
				preferences: preferenceService,
				shell:       shellState,
				sessions:    sessions,
				knownHosts:  database,
			})
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
	// register, which must not stop the app. The packaged smoke skips it so a
	// temporary build never takes over the real ssh handler.
	if options.registersURLScheme() {
		_ = mygo.App.RegisterURLScheme("ssh")
	}

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
	register(command.OpenMonitor, func(context.Context) error {
		shellState.ShowMonitor()
		return nil
	})
	register(command.OpenSystemManager, func(context.Context) error {
		shellState.ShowSystemManager()
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

// importShellHistory persists the local shell's deduplicated commands as
// shell-history rows in the Command History journal. This is the only path
// Shell History File rows take; they are never queried live for suggestions.
// Commands already imported are skipped, so repeated launches do not duplicate
// them.
func importShellHistory(ctx context.Context, service *apphistory.Service,
	source *histfile.Reader) {
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

// commandLocalIdentity is this machine's origin identity for a local shell.
// Empty parts let the fallback rule choose the recorded Host label or
// "unknown" rather than inventing a value.
type commandLocalIdentity struct {
	Host string
	User string
}

// resolveCommandLocalIdentity reads the local OS hostname and username once
// when capture is wired. A failure leaves that part empty.
func resolveCommandLocalIdentity() commandLocalIdentity {
	host, _ := os.Hostname()
	name := ""
	if current, err := user.Current(); err == nil {
		name = current.Username
	}
	return commandLocalIdentity{Host: host, User: name}
}

// commandCompletionHook turns one completed Session command into exactly one
// journal row. The row goes through autocomplete.RecordEntry, which is the
// existing single write path to the history journal.
func commandCompletionHook(service *autocomplete.Service, local commandLocalIdentity) func(appsession.CommandEvent) {
	return func(event appsession.CommandEvent) {
		if service == nil {
			return
		}
		service.RecordEntry(commandEntryFromEvent(event, local))
	}
}

// commandEntryFromEvent maps a completion event to the persisted history row.
// The app resolves the Host context, scope, origin, and author here; the tap
// only captured what it observed. Origin is captured now and never rewritten,
// and a serial Session leaves it empty because a serial port is not a host.
func commandEntryFromEvent(event appsession.CommandEvent, local commandLocalIdentity) domainhistory.Entry {
	// An empty Kind is a local shell, matching Service.Open's normalization.
	isLocal := event.Kind == appsession.KindLocal || event.Kind == ""
	unsaved := event.HostID == "" && !isLocal && event.Kind != appsession.KindSerial
	origin := commandOrigin(event, local)
	scope := event.Scope
	switch {
	case scope != "":
	case event.HostID != "":
		scope = domainhistory.NormalizeScope(event.HostID)
	case isLocal:
		scope = "local"
	case event.Kind == appsession.KindSerial:
		scope = "serial"
	default:
		// An unsaved Host's scope carries the origin captured for the row, so
		// typed-Host suggestions stay separate from the local shell and saved
		// Hosts.
		scope = "host:unsaved:" + origin
	}
	label := event.HostLabel
	if event.HostID == "" && !unsaved {
		// A local shell and a serial Session keep no Host label; the UI names
		// them from their scope. An unsaved Host keeps the typed label.
		label = ""
	}
	var duration time.Duration
	if !event.FinishedAt.IsZero() && !event.StartedAt.IsZero() {
		duration = event.FinishedAt.Sub(event.StartedAt)
		if duration < 0 {
			duration = 0
		}
	}
	return domainhistory.Entry{
		SessionID:   event.SessionID,
		HostID:      event.HostID,
		HostLabel:   label,
		UnsavedHost: unsaved,
		Scope:       scope,
		Command:     event.Command,
		CWD:         event.CWD,
		Shell:       event.Shell,
		RecordedAt:  event.StartedAt,
		FinishedAt:  event.FinishedAt,
		Duration:    duration,
		ExitCode:    event.ExitCode,
		HasExitCode: event.HasExitCode,
		Origin:      origin,
		Source:      domainhistory.SourceSession,
		CaptureTier: event.CaptureTier,
	}
}

// commandOrigin derives the captured host:user origin. A saved Host uses its
// network hostname and SSH username, an unsaved Host its typed hostname and
// username, and the local shell the OS hostname and username. A missing host
// part falls back to the recorded Host label, then "unknown"; a missing user
// part is "unknown". A serial Session has no origin.
func commandOrigin(event appsession.CommandEvent, local commandLocalIdentity) string {
	if event.Kind == appsession.KindSerial {
		return ""
	}
	host, name := event.Hostname, event.Username
	if event.Kind == appsession.KindLocal {
		host, name = local.Host, local.User
	}
	return domainhistory.CaptureOrigin(host, name, event.HostLabel)
}

// terminalActionsFunc adapts a function to the Logs surface's row action.
type terminalActionsFunc func(model.SessionID, model.HostID) error

// OpenTerminal opens or focuses the terminal related to a log or history row.
func (fn terminalActionsFunc) OpenTerminal(sessionID model.SessionID, hostID model.HostID) error {
	return fn(sessionID, hostID)
}

// newSyncDeviceID names this device in the encrypted envelope. It is generated
// once and then persisted in cloud-sync.json; it is not a hardware identifier.
func newSyncDeviceID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("device-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes[:])
}

// syncGitHubAuthBase resolves the OAuth base. NATCATTY_GITHUB_API_BASE alone
// also moves the auth endpoints, so one override is enough for a local fake
// server; NATCATTY_GITHUB_AUTH_BASE takes precedence when both are set.
func syncGitHubAuthBase() string {
	if auth := os.Getenv("NATCATTY_GITHUB_AUTH_BASE"); auth != "" {
		return auth
	}
	return os.Getenv("NATCATTY_GITHUB_API_BASE")
}

// appliedSnapshotRefresh returns the sync callback that marks the live Vault
// state stale after a downloaded cloud copy has been written to the local
// database. The state is resolved lazily because the sync service is built
// before the Vault state. The callback runs on the sync goroutine and only
// sets the state's guarded flag; the UI thread reloads the lists with
// FlushLists before the next frame.
func appliedSnapshotRefresh(state func() *vaultstate.State) func() {
	return func() {
		if s := state(); s != nil {
			s.InvalidateLists()
		}
	}
}

// shutdownSessions is the session half of app shutdown. MyGo runs OnWillQuit
// only after every window closed, so the window hooks preserve resumable
// transports first; this is the last-resort path for a Session that no window
// hook saw. Every close here is a graceful one and keeps ET resume
// credentials, unlike an explicit tab or pane close.
func shutdownSessions(shellState *state.State, sessions *appsession.Service) {
	shellState.CloseAllSessions()
	sessions.CloseAll()
}
