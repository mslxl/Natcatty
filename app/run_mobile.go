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
	appvault "github.com/mslxl/natcatty/application/vault"
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
	shell, database, err := newPhoneShell(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
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
// shell to them. The database is returned so Run can close it.
func newPhoneShell(ctx context.Context) (*phoneshell.Shell, *store.Store, error) {
	userData, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return nil, nil, err
	}

	database, err := store.Open(ctx, filepath.Join(userData, "natcatty.db"))
	if err != nil {
		return nil, nil, err
	}

	// The field-secret store protects key material at rest on the desktop.
	// On a phone without an OS credential store it reports the unavailable
	// mode instead of failing; listing and deleting Keys still work against
	// the same Vault, and deletion simply has no ciphertext to remove.
	secrets, err := store.OpenSecretStore(ctx, database, platformsecrets.System{})
	if err != nil {
		secrets = nil
	}
	vaultOptions := []appvault.Option{
		appvault.WithKeys(database),
		appvault.WithKnownHosts(database),
	}
	if secrets != nil {
		vaultOptions = append(vaultOptions, appvault.WithSecrets(secrets))
	}
	vault := appvault.NewService(database, vaultOptions...)

	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(userData, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		return nil, nil, err
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(userData, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		return nil, nil, err
	}

	shell := phoneshell.New(
		phoneshell.WithVault(vault),
		phoneshell.WithSettings(phoneshell.NewSettingsService(preferences, terminal)),
	)
	return shell, database, nil
}
