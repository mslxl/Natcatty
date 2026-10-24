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

Each staged file is read back before promotion. Its size must equal the source size the
plan observed, and a single-file replacement must carry the destination's original mode:
the mode is applied to the stage, a refused chmod fails the transfer, and the mode is
verified before the rename. The original is renamed to its stable backup before the first
stage is promoted; on success the backup is removed, on an ordinary failure the partial
tree is removed and the backup is renamed back, and a pause or lost transport keeps both
for the ticket-32 resume. A stable backup beside a live file destination is stale and
removed; beside a missing destination it is the only known-good copy and is restored
before the new transaction begins. A directory makes the live-root case ambiguous: an
attempt that died before writing its checkpoint created the new root but copied no
original into it, so a backup beside a directory root is always restored over that root.
A backup that cannot be removed after a committed promotion is reported, leaving the
destination promoted and the backup recoverable.

**Status**: accepted

**Consequences**:
- `pkg/sftp` has no transactional replace; the stage/verify/promote sequence is custom.
- A server without `posix-rename@openssh.com` must fail closed on replace unless the user
  explicitly chose replacement.
- A resumed queued Transfer owns the deterministic staging path it computes, so it needs no
  sweep: it reuses its own interrupted file and never deletes another task's staging file.
  Unrelated abandoned temp files are left alone rather than guessed at.
- A directory replacement keeps the ticket-32 in-place tree model (backup the old root,
  write staged files into the destination, roll back on failure) rather than the
  original's whole-directory staged tree. Resume checkpoints record progress inside the
  final tree, which a staged root does not express without moving it twice; the backup
  plus rollback provides the same user-visible guarantee.
