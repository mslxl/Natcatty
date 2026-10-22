# Netcatty Native

The language of the SSH workspace: connections, sessions, layouts, vault records,
transfers, forwarding, and synchronization. Use these terms consistently in code,
tickets, tests, and UI strings.

## Language

**Host**:
A saved target the user can connect to, identified by an opaque `HostID` and carrying
connection settings, credentials references, and display metadata.
_Avoid_: server, target, machine

**Host Group**:
A user-defined grouping of hosts with shared display and connection defaults.
In Go, `domain/vault.Group` is a Host Group and `Host.Group` holds its path.
_Avoid_: folder, category

**Unsaved Host**:
A connection target the user typed but did not save as a Host, so it has no
`HostID`. Records keep the typed label and address and mark it as unsaved; it is
not the local shell and must not be reported or filtered as one. "Draft Host"
means the same thing but is not the preferred term in code or UI.
_Avoid_: local terminal, typed target, ad-hoc host, temporary host

**SSH Key**:
A saved private/public key pair, optionally protected by a passphrase.
_Avoid_: credential, keypair

**Identity**:
A named authentication identity that can be attached to one or more hosts.
_Avoid_: account, user

**Snippet**:
A reusable command or script fragment the user can run in a session.
_Avoid_: macro, command

**Snippet Package**:
A hierarchical path that groups Snippets in the Vault.
_Avoid_: folder, category

**Snippet Variable**:
A named placeholder in a Snippet command, optionally with a default, substituted before the command runs.
_Avoid_: parameter, macro

**Vault Note**:
Free-form Markdown documentation kept in the Vault, optionally under a Note Group.
_Avoid_: comment, memo

**Note Group**:
A hierarchical path that groups Vault Notes. It is independent of a Host Group;
deleting a Note Group moves its Notes to the Vault root and never deletes them.
_Avoid_: folder, category

**Known Host**:
A recorded host-key fingerprint and its trust decision.
_Avoid_: fingerprint entry

**Proxy Profile**:
A saved proxy configuration (ProxyCommand, HTTP CONNECT, or SOCKS5) usable by hosts.
_Avoid_: proxy, tunnel

**Port Forwarding Rule**:
A saved local, remote, or dynamic forwarding definition and its runtime status.
_Avoid_: tunnel, forward

**Connection**:
An authenticated transport to a single endpoint, shared by reference-counted leases.
_Avoid_: client, socket

**Connection Lease**:
A counted claim on a shared Connection held by a session or forwarding rule.
_Avoid_: reference, handle

**Connection Log**:
One recorded connect attempt or outcome for a Host or the local shell: Host, kind,
status, error, and start/end timestamps. Connection Logs are browsed in the Logs
surface and bounded by retention.
_Avoid_: session log, terminal log, connection history

**Idle Parking**:
The zero-lease state of a Connection kept open for reuse until its idle TTL expires
or the app quits.
_Avoid_: cache, warm pool

**Session**:
A user-visible terminal instance (local, SSH, serial, telnet, mosh, or EternalTerminal)
with its own lifecycle, transport, and terminal surface.
_Avoid_: terminal, tab, shell

**Command History**:
The persisted record of commands executed in Sessions and local shells, one row per
execution with Session, Host, command, source, and timestamp. Autocomplete aggregates
the same rows instead of keeping its own copy.
_Avoid_: shell history, command log

**Shell History File**:
A zsh, bash, or fish history file that autocomplete may read as a suggestion source.
It belongs to the user's shell and is distinct from the app's Command History.
_Avoid_: history, command history

**Hibernate**:
The state of a running Session whose one live terminal view is detached while its
transport and application state stay alive, so Resume can reattach a view.
_Avoid_: suspend, park, freeze

**Observer**:
A read-only view of a Session's bounded screen snapshot and later output, fed to a
shadow terminal without claiming the Session's one live view.
_Avoid_: mirror, duplicate view, second terminal

**Workspace**:
The persisted ordered Tabs and split layout of one window, independent of the live
Sessions it currently displays. Detached windows have their own Workspace.
_Avoid_: layout, arrangement

**Tab**:
One ordered entry in a Workspace's Tab strip. A Tab owns a recursive split tree of one
or more Panes and is shown as a single strip item.
_Avoid_: session, window, pane

**Pane**:
One leaf of a Tab's recursive split tree, bound to one Session or file editor. A Tab
with one Pane looks like a plain terminal tab.
_Avoid_: split, terminal, tab

**Transfer**:
A single tracked file or directory operation in the transfer queue.
_Avoid_: job, task, copy

**Sync Profile**:
A configured cloud-sync target (Gist, Google Drive, OneDrive, WebDAV, S3, or a plugin
provider) with its own credentials and strategy.
_Avoid_: account, backend

**Vault**:
The complete set of stored hosts, groups, keys, identities, snippets, notes, proxies,
forwarding rules, and known hosts.
_Avoid_: database, store

**Export**:
A written document of selected Vault records in one of the portable formats, produced
deterministically. A destructive Export includes credential plaintext and requires
confirmation.
_Avoid_: dump, backup, download

**Local Backup**:
An encrypted, format-versioned snapshot of every Vault family kept under
`vault-backups/` on this device so the user can undo a Vault change locally. It is not
an Export and it is not a Sync Profile copy.
_Avoid_: export, dump, cloud copy

**Backup Retention**:
The configured number of Local Backups kept, clamped to 1-100 and defaulting to 20;
older entries beyond it are pruned immediately.
_Avoid_: limit, cap, quota

**Protective Backup**:
The mandatory `before_restore` Local Backup taken immediately before a Restore or a
bulk apply. If it cannot be written, the destructive operation aborts.
_Avoid_: safety copy, pre-restore export

**Restore**:
Replacing every backed-up Vault family with one Local Backup's snapshot in a single
transaction. A failed Restore leaves the current Vault unchanged.
_Avoid_: import, sync down, rollback
