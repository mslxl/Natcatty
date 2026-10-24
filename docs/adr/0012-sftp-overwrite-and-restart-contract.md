# SFTP overwrite and restart contract

Transfers stage through `.netcatty-upload-<id>-<name>.part` plus
`.netcatty-backup-<hash16>-<name>.bak`, prefer `posix-rename@openssh.com`, and on restart
downgrade non-terminal tasks to `interrupted` with `reconnectRequired`. A queued Transfer
derives `<id>` as 16 hex of FNV-1a-64 over its durable Task ID, a NUL separator, and the
target path in that order, so a resume in a new process computes the same staging path,
truncates the interrupted process's file, and promotes it;
a request with no durable identity keeps the original random eight-hex suffix. Directory
checkpoints v2 use the original manifest limits and chaining. Users, cleanup logic, and
resume behavior depend on these exact paths and states.

**Status**: accepted

**Consequences**:
- `pkg/sftp` has no transactional replace; the stage/verify/promote sequence is custom.
- A server without `posix-rename@openssh.com` must fail closed on replace unless the user
  explicitly chose replacement.
- A resumed queued Transfer owns the deterministic staging path it computes, so it needs no
  sweep: it reuses its own interrupted file and never deletes another task's staging file.
  Unrelated abandoned temp files are left alone rather than guessed at.
