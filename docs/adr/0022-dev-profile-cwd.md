# Development builds keep a cwd-local profile

A Go development build (`go run`, `go build`, and `go tool mygo dev`) keeps its
Vault and credentials under `<cwd>/.natcatty-test/` and never reaches the OS
credential store. The switch is `mygo.IsDev()` in `app.Run`; Natcatty adds no
environment variable or application flag, and a production build (`go tool
mygo build`, which links `mygo.production=1`) is unchanged. The profile
directory is `0700`, the secret file is `0600`, and the whole directory is
gitignored.

**Status**: accepted (2026-11-17)

**Considered Options**:
- An environment-variable contract, like the packaged smoke: explicit, but it
  adds a second activation surface and can be inherited by an unrelated child
  process.
- A `--test-mode` flag: `desktopController.HandleLaunchArgs` treats unknown
  arguments as Host targets, so a flag needs extra filtering, and a
  Finder-launched `.app` cannot receive one.
- A build tag (`go run -tags dev`): a clean compile-time split, but it creates
  a second binary flavor; `mygo.IsDev()` already distinguishes exactly the
  build the developer runs from the one CI ships.
- `go tool mygo dev` alone: it renames the app to `"<Name> Dev"` and uses
  `<identifier>.dev`, but its data still lives in the platform user-data
  directory and it still uses the OS keychain. It is a useful companion, not
  the isolation required here.

**Consequences**:
- `platform/secrets.FileStore` keeps the 32-byte field key, the vault-backup
  key, and sync tokens in plaintext at `0600`. This is a deliberate, opt-in
  deviation from ADR 0010, limited to a throwaway profile. The store must never
  be pointed at real data.
- An in-memory key was rejected: a fresh key on every launch makes every
  existing `secret_values` row `ErrSecretUnreadable`.
- The profile skips the single-instance lock, ssh URL-scheme registration,
  real shell-history import, the external MCP listener, the tray, global
  hotkeys, login-item writes, system notifications, and the Dock icon.
- HOME stays real, so SSH/Git configuration and ssh-agent work. The cost is
  that local shells still write their real history files, and WebView cookies
  and localStorage remain in the system WebView store; mygo v0.6.0 exposes no
  partition option. Both limits are documented in the spec.
- `cmd/natcatty-mcp` cannot discover a dev instance because it re-derives the
  platform user-data path. Exercising external MCP from a dev profile would
  need an explicit discovery-file override in a later change.
- Two dev instances must not share one working directory: the single-instance
  lock is skipped, so they would race on the same SQLite file and secret store.
  Run one instance per checkout.
- The switch is MyGo's build flavor, not a Natcatty-owned setting. MyGo itself
  reports production when `MYGO_ENV=production` is set, so a developer who
  exports it deliberately gets the normal app and the real profile.
- The opt-in launch smoke (`-tags launchsmoke`, `RunSmoke`) calls the
  composition root directly and keeps its own separate `launchsmoke` app
  identity and profile. It is a test harness outside this contract.
