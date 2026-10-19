# SFTP overwrite and restart contract

Transfers use the original staging contract: `.netcatty-upload-<uuid8>-<name>.part` plus
`.netcatty-backup-<hash16>-<name>.bak`, prefer `posix-rename@openssh.com`, and on restart
downgrade non-terminal tasks to `interrupted` with `reconnectRequired`. Directory
checkpoints v2 use the original manifest limits and chaining. Users, cleanup logic, and
resume behavior depend on these exact paths and states.

**Status**: accepted

**Consequences**:
- `pkg/sftp` has no transactional replace; the stage/verify/promote sequence is custom.
- A server without `posix-rename@openssh.com` must fail closed on replace unless the user
  explicitly chose replacement.
- Startup must sweep abandoned temp files without touching active checkpoints.
