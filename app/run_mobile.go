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
package app

import (
	"context"
	"log"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
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
		// A natcatty_smoke build opens the terminal diagnostic itself, so
		// the simulator job can grep the echo result without a UI driver.
		smokeStart(shell)
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
	if err != nil {
		secrets = nil
	}
	// The phone Vault registers the same record families as the desktop, so a
	// Host's jump chain, Identity, Proxy Profile, and Network Profile resolve
	// through the shared route code instead of being ignored here.
	vault := newPhoneVault(database, secrets)

	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(userData, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		return nil, nil, nil, nil, err
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(userData, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
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
	history := newPhoneHistory(database, preferences, terminal, secrets)
	history.Start(ctx)

	// The phone registers only remote transports: SSH, Telnet, Mosh, and ET.
	// Local and serial shells are not constructed here at all.
	sessions := newPhoneSessionRuntime(vault, database, userData, prompts, history)

	shell := phoneshell.New(
		phoneshell.WithVault(vault),
		phoneshell.WithHosts(vault),
		phoneshell.WithSessionOpener(sessions),
		phoneshell.WithPrompts(prompts),
		phoneshell.WithSettings(phoneshell.NewSettingsService(preferences, terminal)),
		phoneshell.WithHistorySync(history),
	)
	return shell, database, sessions, history, nil
}
