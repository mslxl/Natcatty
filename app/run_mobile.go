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
// Vault sync is deliberately not configured in the phone build yet: there is
// no sync account or provider surface on the phone, so a portable document is
// written in the shared schema (adapters/syncedsettings) but travels to a
// desktop only through a build that has the sync engine and a configured
// remote. This is a known gap of this ticket, not an implied working feature.
package app

import (
	"context"
	"log"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appvault "github.com/mslxl/natcatty/application/vault"
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
	shell, database, sessions, history, err := newPhoneShell(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shell.Close()
		sessions.Close()
		history.Stop()
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
		_ = shell.HandleDeepLink(raw)
	})
	// Leaving the foreground releases the terminal view and persists the
	// Tabs; returning resumes or reconnects the visible Session. Registering
	// before Run makes the first foreground transition reach the shell.
	mygo.App.OnDidResignActive(shell.Background)
	mygo.App.OnDidBecomeActive(shell.Foreground)
	err = mygo.App.Run()
	if showErr != nil {
		return showErr
	}
	return err
}

// newPhoneShell opens the device's Vault and settings stores and wires the
// shell to them. The database and the Session runtime are returned so Run can
// close them.
func newPhoneShell(ctx context.Context) (*phoneshell.Shell, *store.Store, *phoneSessionRuntime, *phoneHistory, error) {
	userData, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	database, err := store.Open(ctx, filepath.Join(userData, "natcatty.db"))
	if err != nil {
		return nil, nil, nil, nil, err
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

	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(userData, "preferences.json")),
		apppreferences.WithChangeHook(func(domainpreferences.Settings) {
			// The font, cursor and scheme are read when a terminal is built.
			// Re-style the live ones now, as the desktop root does; this also
			// covers a preference document applied from outside the page.
			if shell != nil {
				shell.RefreshTerminalAppearance()
			}
		}))
	if err := preferences.Load(ctx); err != nil {
		return nil, nil, nil, nil, err
	}
	terminalSettings := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(userData, "terminal-settings.json")),
		appterminalsettings.WithChangeHook(func(current domainterminal.Settings) {
			// The OSC 52 policy is read when a Session opens; apply a change
			// to the Sessions that are already running.
			if sessionRuntime != nil {
				sessionRuntime.SetClipboardPolicy(current.OSC52)
			}
		}))
	if err := terminalSettings.Load(ctx); err != nil {
		return nil, nil, nil, nil, err
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

	// The phone registers only remote transports: SSH, Telnet, Mosh, and ET.
	// Local and serial shells are not constructed here at all. The terminal
	// settings supply the same OSC 52 policy the desktop's Session service
	// uses, so the Terminal page's Remote clipboard control has an effect.
	sessionRuntime = newPhoneSessionRuntime(vault, database, userData, prompts, history, fieldSecrets,
		func() domainterminal.Settings { return terminalSettings.Settings() })

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
	return shell, database, sessionRuntime, history, nil
}
