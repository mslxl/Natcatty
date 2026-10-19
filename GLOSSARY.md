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
_Avoid_: folder, category

**SSH Key**:
A saved private/public key pair, optionally protected by a passphrase.
_Avoid_: credential, keypair

**Identity**:
A named authentication identity that can be attached to one or more hosts.
_Avoid_: account, user

**Snippet**:
A reusable command or script fragment the user can run in a session.
_Avoid_: macro, command

**Vault Note**:
Free-form user documentation attached to a host or kept standalone.
_Avoid_: comment, memo

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

**Idle Parking**:
The zero-lease state of a Connection kept open for reuse until its idle TTL expires
or the app quits.
_Avoid_: cache, warm pool

**Session**:
A user-visible terminal instance (local, SSH, serial, telnet, mosh, or EternalTerminal)
with its own lifecycle, transport, and terminal surface.
_Avoid_: terminal, tab, shell

**Workspace**:
A persisted tree of panes that defines a window's layout, independent of the live
sessions it currently displays.
_Avoid_: layout, arrangement

**Tab**:
A named pane within a Workspace that hosts one Session or a file editor.
_Avoid_: session, window

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
