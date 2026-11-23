//go:build ios || android

// The phone composition root. Phones have no desktop chrome and no local
// shell: this root builds the four-section shell and nothing that a phone
// cannot run. The desktop root in app.go is excluded by build tags, so
// desktop wiring (tray, menus, the single-instance lock, the browser, MCP,
// SFTP panels, local and serial transports) is not linked into a phone build.
//
// The Vault and Settings pages read the same SQLite Vault and the same
// versioned settings documents the desktop uses. The store lives in this
// device's application-data directory, which is what makes interface style
// and app icon device-local; the portable fields are the ones
// domainpreferences.Portable leaves in the sync document.
//
// Vault sync is wired here too. The phone configures WebDAV or S3-compatible
// storage on the Settings page, and the shared sync engine carries the Vault
// families and the portable settings documents to any other device configured
// for the same account. Interface style, app icon, filesystem paths and the
// other device-local fields are stripped by domainpreferences.Portable and
// domainterminal.Portable, so they never leave this phone.
package app

import (
	"context"
	"log"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncbackup "github.com/mslxl/natcatty/adapters/syncbackup"
	adapterbackup "github.com/mslxl/natcatty/adapters/vaultbackup"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appvault "github.com/mslxl/natcatty/application/vault"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/phoneshell"
	"github.com/mslxl/natcatty/ui/terminal"
)

// Run starts the phone app and blocks until it quits.
func Run() error {
	// Assert the statically linked libghostty-vt before any view exists:
	// Load resolves its symbols and fails when the library reports another
	// version than the one the bindings mirror.
	if err := terminal.Load(); err != nil {
		return err
	}
	v, err := terminal.LibraryVersion()
	if err != nil {
		return err
	}
	log.Printf("natcatty: libghostty-vt %s", v)

	ctx := context.Background()
	shell, database, sessions, history, vaultSync, err := newPhoneShell(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shell.Close()
		sessions.Close()
		history.Stop()
		if vaultSync != nil {
			vaultSync.Stop()
		}
		_ = database.Close()
	}()
	var showErr error
	mygo.App.WhenReady(func() {
		// Load before the first frame. A read failure is the Vault pages'
		// error state, not a startup failure, so the shell still opens.
		_ = shell.Load(ctx)
		if err := shell.Show(); err != nil {
			showErr = err
			mygo.App.Quit()
			return
		}
		// The link that launched the app may have arrived before the Host
		// list was readable. Ready applies it now, after Load and Show, so it
		// matches against loaded Hosts and can navigate a shown shell.
		shell.Ready()
		// A natcatty_smoke build opens the terminal diagnostic itself, so
		// the simulator job can grep the echo result without a UI driver.
		smokeStart(shell)
	})
	// A link from another app reaches the shell through the same handler as
	// the launch URL. Registering before Run makes the started-with URL arrive
	// too; before Ready the shell buffers it.
	mygo.App.OnOpenURL(func(raw string) {
		// A Vault-sync OAuth redirect belongs to the sync engine, not the
		// Host deep-link router. HandleOpenURL claims every URL of the app's
		// custom scheme and reports whether it did.
		if vaultSync != nil && vaultSync.HandleOpenURL(raw) {
			return
		}
		_ = shell.HandleDeepLink(raw)
	})
	// Leaving the foreground releases the terminal view and persists the
	// Tabs; returning resumes or reconnects the visible Session. Registering
	// before Run makes the first foreground transition reach the shell.
	// The sync engine pauses its OAuth waiting window while the browser owns
	// the screen, so a suspended app never spends its callback budget.
	mygo.App.OnDidResignActive(func() {
		shell.Background()
		if vaultSync != nil {
			vaultSync.Background()
		}
	})
	mygo.App.OnDidBecomeActive(func() {
		shell.Foreground()
		if vaultSync != nil {
			vaultSync.Foreground()
		}
	})
	err = mygo.App.Run()
	if showErr != nil {
		return showErr
	}
	return err
}

// newPhoneShell opens the device's Vault and settings stores and wires the
// shell to them. The database and the Session runtime are returned so Run can
// close them.
func newPhoneShell(ctx context.Context) (*phoneshell.Shell, *store.Store, *phoneSessionRuntime, *phoneHistory, *phoneSync, error) {
	userData, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	database, err := store.Open(ctx, filepath.Join(userData, "natcatty.db"))
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// The field-secret store protects key material at rest on the desktop.
	// On a phone without an OS credential store it reports the unavailable
	// mode instead of failing; listing and deleting Keys still work against
	// the same Vault, and deletion simply has no ciphertext to remove.
	secrets, err := store.OpenSecretStore(ctx, database, platformsecrets.System{})
	var fieldSecrets appvault.FieldSecrets
	if err != nil {
		secrets = nil
	} else {
		fieldSecrets = secrets
	}
	// The phone Vault registers the same record families as the desktop, so a
	// Host's jump chain, Identity, Proxy Profile, and Network Profile resolve
	// through the shared route code instead of being ignored here.
	vault := newPhoneVault(database, secrets)

	// The shell is assigned after the services are built. The change hooks
	// below only fire on a successful Update, which a user can only reach
	// through the shown UI, so the nil checks cover construction only.
	var shell *phoneshell.Shell
	var sessionRuntime *phoneSessionRuntime
	var vaultSync *phoneSync

	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(userData, "preferences.json")),
		apppreferences.WithChangeHook(func(domainpreferences.Settings) {
			// The font, cursor and scheme are read when a terminal is built.
			// Re-style the live ones now, as the desktop root does; this also
			// covers a preference document applied from outside the page.
			if shell != nil {
				shell.RefreshTerminalAppearance()
			}
			// A portable theme or font change is a Vault-sync fact: schedule
			// an automatic upload when sync is configured and enabled.
			if vaultSync != nil {
				vaultSync.NotifyLocalChange()
			}
		}))
	if err := preferences.Load(ctx); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	terminalSettings := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(userData, "terminal-settings.json")),
		appterminalsettings.WithChangeHook(func(current domainterminal.Settings) {
			// The OSC 52 policy is read when a Session opens; apply a change
			// to the Sessions that are already running.
			if sessionRuntime != nil {
				sessionRuntime.SetClipboardPolicy(current.OSC52)
			}
			if vaultSync != nil {
				vaultSync.NotifyLocalChange()
			}
		}))
	if err := terminalSettings.Load(ctx); err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// Host-key and credential prompts are answered on the Sessions page. A
	// password typed there is recorded through the Vault's field-secret store
	// under the Host's identifier, so it is encrypted at rest and never
	// written to the settings documents or the log.
	prompts := newPhonePrompts(vault)

	// Command History and Atuin sync use the same account configuration
	// document, field-secret store, and sync services as the desktop. The
	// engine runs on its own goroutine; a failed upload updates the status
	// the Settings page shows and never reaches a Session.
	history := newPhoneHistory(database, preferences, terminalSettings, secrets)
	history.Start(ctx)

	// Cloud sync uses the same shared Vault and settings documents. A failure
	// to open the profile must not stop the phone from starting: the Settings
	// page then reports that sync is unavailable, exactly as a Vault read
	// failure reports its own error state.
	if secrets != nil {
		// The phone takes the same protective restore points the desktop does
		// before a sync overwrites either side. Conflict resolution fails
		// closed when this adapter is missing or its key is unavailable.
		backupStore := adapterbackup.Open(
			filepath.Join(userData, "vault-backups"), platformsecrets.System{})
		backupService := appbackup.NewService(database, backupStore,
			appbackup.WithRetentionStore(backupStore))
		vaultSync, err = newPhoneSync(ctx, database, secrets, preferences, terminalSettings, userData,
			withPhoneSyncBackups(adaptersyncbackup.New(backupService)),
			// A downloaded copy rewrites the Vault and the portable settings
			// documents. Reload the lists the shell shows and re-style the live
			// terminals before the next frame. The callback runs on the sync
			// goroutine, so the shell is touched on the UI thread.
			withPhoneSyncAppliedSnapshot(func() {
				mygo.RunOnMain(func() {
					if shell != nil {
						_ = shell.Load(ctx)
						shell.RefreshTerminalAppearance()
					}
				})
			}),
			// Drive/OneDrive sign-in opens the consent page in the system
			// browser; the callback returns through the app's URL type or
			// intent-filter and is routed below.
			withPhoneSyncBrowserOpener(mygo.Shell.OpenExternal),
		)
		if err != nil {
			log.Printf("natcatty: Vault sync unavailable: %v", err)
			vaultSync = nil
		} else {
			// A committed Vault record write is a sync fact. The change hook
			// never blocks; the engine debounces and coalesces the run. A
			// payload applied by the engine itself is suppressed inside the
			// service, so a download does not queue a redundant sync.
			database.SetChangeHook(func() {
				if vaultSync != nil {
					vaultSync.NotifyLocalChange()
				}
			})
		}
	}

	// The phone registers only remote transports: SSH, Telnet, Mosh, and ET.
	// Local and serial shells are not constructed here at all. The terminal
	// settings supply the same OSC 52 policy the desktop's Session service
	// uses, so the Terminal page's Remote clipboard control has an effect.
	sessionRuntime = newPhoneSessionRuntime(vault, database, userData, prompts, history, fieldSecrets,
		func() domainterminal.Settings { return terminalSettings.Settings() }, preferences)

	shell = phoneshell.New(
		phoneshell.WithVault(vault),
		phoneshell.WithHosts(vault),
		phoneshell.WithSessionOpener(sessionRuntime),
		// A deep link for an endpoint the Vault does not have asks to create
		// that Host. The shared Vault service writes the record, so the new
		// Host is the same one the desktop lists.
		phoneshell.WithHostCreator(vault),
		phoneshell.WithPrompts(prompts),
		phoneshell.WithSettings(phoneshell.NewSettingsService(preferences, terminalSettings)),
		phoneshell.WithHistorySync(history),
		// The Session Tools surface reads the same journal the Settings History
		// page configures, so a command captured here is searchable, fillable,
		// and clearable from the terminal it came from.
		phoneshell.WithCommandHistory(history),
		// The phone configures WebDAV or S3-compatible storage on the Settings
		// page; the engine carries portable settings documents and Vault
		// records to a desktop configured for the same account.
		phoneshell.WithVaultSync(vaultSync),
		// The terminal is built with and re-styled from the shared Appearance
		// document. mygo.Theme is read when a screen is built, which is after
		// App.Run has started the event loop.
		phoneshell.WithTerminalAppearance(func() phoneshell.TerminalAppearance {
			return phoneshell.TerminalAppearanceFromAppearance(
				preferences.Settings().Appearance, mygo.Theme.IsDark())
		}),
		// The open Tabs survive a relaunch in the shared Workspace restore
		// format; the phone's payload is its flat one-Pane-per-Tab form, which
		// FlattenRestored reads back as dormant entries.
		phoneshell.WithSessionStore(database.Workspace()),
	)
	return shell, database, sessionRuntime, history, vaultSync, nil
}
