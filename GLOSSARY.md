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
In Go, `domain/vault.Group` is a Host Group with a stable ID and a
`(ParentID, Name)` position; its `Path` is derived from the tree. A Host
belongs to at most one group through `Host.GroupID`; `Host.Group` is the
derived path used for display and for the portable payload.
_Avoid_: folder, category

**Backing**:
The tree a transfer pane reads and writes. Two panes share a Backing exactly when
they address the same tree - one local device, or one Host - even when they are
separate connections or Tabs. Canonical paths are compared within one Backing.
_Avoid_: filesystem, connection, adapter

**File protocol**:
A Host's preferred way to browse and transfer files: automatic, SFTP only, or SCP
mode. Automatic tries SFTP first and may fall back to SCP. SCP mode uses shell exec
and `scp -t/-f` for servers without an SFTP subsystem. Sudo SFTP elevates the
subsystem through a password-on-stdin handshake. A single-channel Host permits one
SSH channel, so it refuses a second one and any SCP fallback that would need it.
_Avoid_: transfer mode, legacy SCP

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

**Network Profile**:
A saved Vault record describing how to join one virtual network, identified by a
Kind (`tailscale` or `wireguard`). Hosts and Host Groups select it by soft reference,
like a Proxy Profile. Its credentials live only in the field secret store; the record
carries `HasAuthKey`, `HasPrivateKey`, and `HasPresharedKey` presence markers.
_Avoid_: VPN, network, tunnel

**Network Provider**:
The runtime dialing port that opens connections through one Network Profile: TCP
for the first SSH dial, plus an optional datagram capability that carries the
in-process mosh client's UDP flow. Tailscale Net and WireGuard Net are its two
implementations.
_Avoid_: VPN client, tunnel, sidecar

**Tailscale Net**:
A Network Provider that joins a tailnet through tsnet, identified by a Control URL,
an optional auth key, a hostname, tags, and the Ephemeral preference.
_Avoid_: Tailscale profile, tailnet

**WireGuard Net**:
A Network Provider that joins a WireGuard network from addresses, DNS servers, an
MTU, an optional listen port, and a peer list, using the embedded netstack. A
wg-quick `.conf` can fill its editor after a reviewed import.
_Avoid_: WireGuard profile, VPN

**First endpoint**:
The first dial target of a whole route: the first jump when a jump chain exists,
otherwise the target Host. A Network Profile applies to the First endpoint.
_Avoid_: destination, final host

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

**Editing Session**:
The service's record of one file handed to an opener: the materialized local copy,
its remote write-back target, and the version it read. An internal editor or an
external application owns the document; the editing session is released when that
document or its pane Tab closes. It is not a terminal Session.
_Avoid_: edit, file session

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

**Transfer Checkpoint**:
A bounded, fixed-width record of the completed prefix of a directory Transfer.
It lets a resumed Transfer skip entries it already copied after validating them
against a fresh walk. It is not a per-file manifest.
_Avoid_: resume list, file log, progress cache

**Reattach**:
Binding live Backings to a Transfer restored after a restart or cut off by a
lost transport, so the same row can resume from its checkpoint. The surface
reattaches a Host's rows when that Host's SFTP pane reconnects, using the
durable `sourceHost`/`destHost` identity of each row. Reattach keeps the row
identity; Retry is a separate full replay.
_Avoid_: reconnect, rebind, resume

**Pause Latch**:
The sticky request that a running Transfer stop and become paused. It survives
until Resume or Cancel; a second Pause is a no-op.
_Avoid_: pause flag, stop request

**Cancel Latch**:
The sticky, winning request that a Transfer stop for good. It clears the Pause
Latch, drops the checkpoint, and makes a later Retry start over.
_Avoid_: abort flag, stop flag

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

**Interface Style**:
The look of the interface's shapes and materials: radii, control and bar sizes, type
scale, translucency, and layout. Classic is the original's shadcn look; macOS is the
macOS Liquid Glass look. It is independent of the color preset, which decides colors
only, and it is device-local: sync never carries it.
_Avoid_: theme, skin, UI mode
