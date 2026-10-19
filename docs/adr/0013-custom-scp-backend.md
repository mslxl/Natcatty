# Custom SCP backend is retained

The rewrite implements SCP as a first-class transfer backend: shell-quoted browse/manage
commands plus a custom `scp -t/-f` protocol parser over SSH exec channels, with a 15 s
probe and no silent sudo downgrade. The original supports SCP, and dropping it would be a
user-visible parity regression; the cost is a substantial protocol implementation that
`pkg/sftp` cannot provide.

**Status**: accepted

**Consequences**:
- SCP mode has no recursive server-side helpers beyond what the remote shell provides.
- Single-channel hosts and sudo are handled with the original refusal rules.
- The SCP backend shares the transfer queue, conflict policy, and progress model with
  SFTP through the same `RemoteFS` port.
