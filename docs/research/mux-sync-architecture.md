# Mux Sync: where the per-Host control loop belongs and which seams to reuse

Status: research
Date: 2026-11-08
Scope: answer ticket 02 of the Mux Sync effort. One long-lived per-Host loop must
discover tmux / zellij / herdr sessions, keep a 1:1 binding with Netcatty Mux
Panes, forward split / kill / delete, and survive reconnect. This report places
that loop in the existing layers and names the seams to reuse. It does not
implement the feature, and it does not repeat the companion research on backend
lifecycle (ticket 01) or platform/security (ticket 09).

Sources verified against:

- Netcatty `main` at `f13947127` (the base of branch
  `research/mux-sync-architecture`). Every `path:line` reference below is this
  commit unless marked `[herdr]`.
- Netcatty branch `research-herdr-core-connection` at `13c663951`. The herdr
  adapter, its ports, and ADR-0018 exist only on that branch; `main` has no
  `adapters/herdr`. References marked `[herdr]` point there.
- ADR-0001 (layered package boundaries and consumer-owned ports), ADR-0006
  (window-scoped UI state), ADR-0017 (SSH tmux auto-attach), ADR-0018 (herdr
  terminal mode).
- No external sources were used. The zellij/tmux CLI behavior is the companion
  lifecycle ticket's subject; this report treats it as an input.

## 1. Verdict

1. **Put the per-Host watcher in a new application context:
   `application/muxsync`.** It is a per-Host use case with its own long-lived
   lifecycle, snapshot, failure policy, and backend actions. It is not a
   Session, not a system-monitor surface, and not UI state.
2. **Do not introduce a shared application-level `Transport` per Host.** The
   Connection Manager already shares one physical Connection per endpoint and
   counts Leases over it. The control plane gets its own `LeaseChannel`; every
   Mux Pane keeps its own `appsession.Session` and `Transport`. This preserves
   Pane isolation and reuses the existing Session lifecycle, resize, and
   reconnect machinery.
3. **The control plane and the Pane data plane must be separate streams.** A
   watcher may hold one control lease (or one control stream) per Host, and
   mutations may open short-lived leases, but no Pane may be multiplexed over
   the watcher's stream. ADR-0018 rejected exactly that shape for herdr because
   one wedged Pane would block the others (`[herdr]
   docs/adr/0018-herdr-terminal-mode-modifier.md:20-23`).
4. **Split ownership between application and UI.** Application owns session
   identity, discovery, snapshots, actions, and backoff. UI owns the binding
   from a stable Mux Session key to a window-scoped Pane, plus layout, focus,
   collapse state, and "new" markers. Pane IDs must never enter
   `application/muxsync`; ADR-0006 puts window-scoped state in `ui/<area>/state`
   (`docs/adr/0006-native-window-and-tab-model.md:17`).
5. **Reuse the herdr pattern, not the herdr protocol.** Herdr contributed a
   good shape: a consumer-owned runtime port, a narrow remote-execution port
   implemented in `app` over the Connection Manager, structural error kinds,
   short-lived bridges for list/create, and one long-lived stream per Pane.
   tmux/zellij need different commands and detach semantics, not the framed
   herdr bridge.
6. **Start with zellij, then tmux, then herdr.** Do not build a grand unified
   adapter for all three up front. Define the consumer-owned port the loop
   needs, implement one backend, and let duplication prove what the other
   backends share.
7. **The UI's `Flush()` model fits without change.** A service subscription
   only requests a redraw; `Flush()` pulls immutable per-Host snapshots on the
   UI thread and applies them. This is exactly the System Manager pattern
   (`ui/sysmanager/state.go:240`, `:340`).

## 2. Fixed constraints and terms

The effort's fixed decisions are taken as given:

- One Mux Session maps to one Mux Pane. Backend-internal panes stay inside that
  Pane; they are never mapped to Netcatty Panes.
- Closing a Mux Pane detaches; it never kills or deletes the backend session.
  Kill/delete are explicit v1 actions.
- An externally created session appears in Live Sessions marked new; it is not
  opened automatically. An already open Pane follows external rename and
  closes when its session is authoritatively gone, but a transient "backend
  unavailable" must not close it.
- No layout-geometry sync, no auto-resurrect of EXITED sessions, no management
  of backend configuration, no zellij web.
- `TerminalMode` is single-select per Host (`none | tmux | herdr | zellij`);
  local terminals offer `none | herdr | zellij`. Mosh, EternalTerminal, serial,
  Telnet, and network-device Hosts never get a Mux mode.
- Do not extract a unified backend adapter interface until real duplication is
  proven.

Terms used below:

- **Mux Session**: the backend-owned unit (tmux session, zellij session, herdr
  terminal).
- **Mux Pane**: one Netcatty Pane rendering exactly one Mux Session.
- **control plane**: per-Host list/create/kill/delete/split operations and the
  loop that reconciles them.
- **data plane**: the PTY or attach transport that renders a Mux Session inside
  a Mux Pane.

## 3. What the existing seams do

### 3.1 `application/session` is a per-Session data plane

`Transport` is a byte stream plus `Resize` (`application/session/session.go:290`).
`Factory.Open` returns one Transport for one `StartSpec` (`:431`). `Service.Open`
creates one `Session`, one `TerminalConn`, and its loops (`:568`, `:693`).
`Session` owns resize, bounded output, one live view (`application/session/conn.go:18`,
`:384`), exit status, and reconnect. `Session.reconnect` retries
`factory.Open` on the policy delay and stops on permanent errors
(`application/session/session.go:1456-1485`).

This is the right seam for **one Mux Pane**, but it is the wrong seam for a
Host-wide control plane:

- It is one stream per Session; a control plane needs many independent
  commands and possibly a persistent event stream.
- Its reconnect policy is per-Pane and reopens the same `StartSpec`. It has no
  notion of "re-list and reconcile the Host".
- `StartSpec` has no Mux identity today (`application/session/session.go:347-408`).
  On the herdr branch it gained `Herdr *HerdrAttachSpec` and `HerdrTarget`
  (`[herdr] application/session/session.go:442-451`), but nothing generic.

One existing detail is directly relevant: `Factory.Open` can run a non-PTY
probe on the same Connection lease before opening the PTY. `acquireTerminal`
acquires a `LeaseShell`, runs `tmux.ProbeCommand` through `lease.Exec`, then
calls `lease.OpenTerminal` (`app/session_factory.go:157-221`). It deliberately
skips the probe when `lease.SingleChannel()` is true (`:229-235`). This proves
that non-PTY control and PTY attach can coexist on one Host connection, but
only because they are sequential and the peer supports multiple channels.

### 3.2 `application/connection` already shares the physical Connection

- `Manager.Acquire` takes a Request and returns a counted Lease
  (`application/connection/manager.go:35`, `:433`).
- The pool keys by endpoint and reuses a compatible live Connection
  (`application/connection/pool.go:271`, `:314`, `:370`, `:441`). Idle
  Connections park for five minutes, with at most 128 parked
  (`pool.go:17-21`).
- `LeaseKind` distinguishes `LeaseShell` from `LeaseChannel`
  (`pool.go:77-83`). `Request.Dedicated` asks for a fresh transport that is
  never lent to another lease or parked; `SingleChannel` and `X11Forwarding`
  transports never enter the reuse pool (`manager.go:63-73`,
  `pool.go:117-127`).
- `Lease.Exec` runs one command to completion and buffers stdout/stderr
  (`application/connection/exec.go:84`). `Lease.OpenExec` returns a raw stream
  (`:93`). `Lease.OpenTerminal` opens a PTY (`manager.go:1259`).
- `Lease.Release` drops a reference; the last release parks or closes the
  Connection (`manager.go:1308`). `Discard` marks the physical transport
  unusable (`:1292`). `Alive` and `Done` are Connection-level, not
  channel-level (`:1143`, `:1186`).
- `CloseAll` closes every pooled Connection and Network Provider
  (`manager.go:390`).

Consequences:

- **Multiple Panes do not need multiple SSH connections.** They already share
  one because every `Factory.Open` goes through the same pool. "One physical
  Connection per Host" is the default, not a new design.
- **A persistent lease prevents idle parking.** A watcher that holds a lease
  keeps the Connection active until it releases. That is acceptable only while
  the user has real interest in the Host.
- **`Done()` cannot detect one dead channel.** It fires when the Connection
  dies. A failed exec or a stream EOF is the per-operation signal.

### 3.3 `app/session_factory.go` is the composition point for SSH control

`transportFactory.Open` switches on `StartSpec.Kind` and routes SSH through
`acquireTerminal` (`app/session_factory.go:90-135`). The SSH Request built there
contains the full route: jumps, proxy, network profile, algorithms, config
fingerprint, `LeaseShell`, and keepalive (`:177-192`). `leasedTransport` ties
the Lease to the Session transport and decides Release versus Discard
(`:336-430`).

Any new control-plane bridge must build the **same** Request fields. If it
omits `ConfigFingerprint`, `Proxy`, `Network`, or changes `LeaseKind`, the pool
will see a different endpoint and open a second physical Connection. This is a
concrete duplicate-connection trap.

### 3.4 `adapters/herdr` is a working pattern for a backend port [herdr]

- `HerdrTarget` and `HerdrTerminalKey` give a stable, Host-scoped identity
  (`[herdr] application/session/herdr.go:20-42`).
- `HerdrRemote` is the narrow remote-execution port: `Run` to completion,
  `Open` for a raw stream (`:44-70`). The app implements it over the Connection
  Manager, acquiring a `LeaseChannel` per call and releasing it promptly
  (`[herdr] app/herdr_remote.go:87-188`). `Run` uses `lease.Exec`; `Open` uses
  `lease.OpenExec` and keeps the lease until the stream closes. stderr is
  drained and discarded so it cannot stall stdout (`:162-173`).
- `HerdrRuntime` puts Detect / List / Create / Attach behind one consumer-owned
  port (`[herdr] application/session/herdr.go:135-146`). List and Create use
  short-lived bridges; Attach owns one long-lived bridge per Pane
  (`[herdr] adapters/herdr/runtime.go:234-299`, `attach.go:50-121`).
- The error taxonomy is structural and permanent-aware
  (`[herdr] application/session/herdr.go:184-311`). `Permanent()` stops the
  Session reclaim loop; `NoticeCode()` maps to UI text without leaking payloads.
- ADR-0018 records the rejected shared-bridge option and the one-bridge-per-Pane
  decision (`[herdr] docs/adr/0018-herdr-terminal-mode-modifier.md:20-33`).

This is the closest existing model for a per-Host backend integration. What it
does not provide: kill/delete (the code never sends `terminal.kill`;
`[herdr] adapters/herdr/attach.go:534`), internal session split, or a generic
Mux Session model.

### 3.5 `application/sysmanager` is the closest existing polling loop

The System Manager service is per-surface, not per-Host, but its mechanics are
the right reference:

- A consumer-owned `Connector` port (`application/sysmanager/service.go:135`)
  opens a `Connection`; `Exec` runs fixed commands (`:140`).
- `Start`/`Stop` manage one sampling run; `Snapshot` returns an immutable copy;
  `Subscribe` adds a redraw listener that runs outside the lock
  (`service.go:468-610`).
- `run` holds one Connection across samples, closes it on stop or failure, and
  backs off: delay starts at the interval, doubles after two failures, and caps
  at six intervals (`service.go:612-653`). `fail` stops on auth, host-key,
  unsupported, or other non-retryable classes (`service.go:805-834`).
- `update` is generation-guarded so a late sample cannot overwrite a newer Host
  or run (`service.go:861-875`); `notify` copies listeners before calling them
  (`:877-887`).
- Actions open their own short-lived lease, bounded by `actionTimeout`, rather
  than racing the sampler (`application/sysmanager/actions.go:185-220`).
- tmux command builders already exist: list (`commands.go:122`), create
  (`:277`), kill (`:286`), attach (`:297`). These are a reuse candidate, not a
  dependency: `application/muxsync` may not import `application/sysmanager`
  (archtest rule below).

The System Manager is not the home for Mux Sync because its service is bound to
one visible system surface, starts and stops on Activate/Deactivate
(`ui/sysmanager/state.go:273-292`), and samples process/docker/service data
unrelated to a Host's Mux mode.

### 3.6 `domain/model/tmux` is the right kind of pure backend helper

`domain/model/tmux` builds probe and wrapper commands, parses answers, and
sanitizes/quotes names without any I/O (`domain/model/tmux/tmux.go:1-6`,
`:70-172`). `DetachedExitStatus` is how the app distinguishes a clean tmux
detach from a shell exit (`:20-24`), and
`ui/shell/state/workspace.go:1910-1951` acts on the resulting
`EndReasonDetached` to close the Pane without an error.

This package is the model for zellij/tmux command builders: pure, testable,
shell-quoting, and free of SSH, UI, and application types. It is also where a
shared tmux `list-sessions` parser should eventually live, rather than in
`application/sysmanager/commands.go`.

### 3.7 `ui/shell/state` fits asynchronous snapshots

- `State` is mutated on the UI thread except for explicit mutex-protected
  queues (`ui/shell/state/state.go:129-132`, `:232-249`).
- `OpenHostWithStartup` dials on a goroutine and queues an `openOutcome`;
  `Flush` applies it (`state.go:784-840`, `:1060-1130`).
- `Flush` is called at the start of the main view (`ui/shell/view.go:59`) and
  of a detached view (`ui/shell/detached_view.go:22`).
- Tabs and Panes are window-scoped; `Tab` holds a split tree and a Pane map
  (`ui/shell/state/workspace.go:37-70`). `openDescriptor` maps a persisted
  descriptor to a `StartSpec` (`:1741-1853`), and `closeDetachedSessions`
  removes Panes whose Session ended in a tmux detach (`:1910-1951`).
- The side panel has a `SideTool` abstraction with `View(target, width)` and a
  `Hide` callback (`ui/shell/state/files.go:73-83`), mounted for the Tab's open
  Files panel (`:277-304`). It is a possible mount point for Live Sessions, but
  it is gated by the Files panel being open (`SideTools` returns nil when
  `filesOpen[tab]` is false, `:109-125`).

`Flush` is the right place to apply Mux snapshots. The loop must never mutate
Tabs or Panes directly.

### 3.8 The persisted descriptor has no Mux identity

`SessionDescriptor` stores kind, Host, label, custom name, directory, X11,
tmux auto-attach, serial, and ET resume key (`domain/workspace/payload.go:33-64`).
It has no backend, session key, terminal address, or attach flag. `Valid`
accepts local and SSH descriptors without any Mux gate (`:66-78`). A Mux Pane
cannot be restored correctly until this changes. The payload is versioned and
`SanitizePayload` discards unknown versions (`:11-13`, `:123-127`), so adding a
field needs care; see risks.

## 4. Recommended placement by layer

### 4.1 Domain

Add a small pure package, `domain/model/mux`, containing only shared
vocabulary. It must live under `domain/model`: the archtest lets a domain
package import only `domain/model` and its subpackages
(`internal/archtest/arch.go:44-50`), so a top-level `domain/mux` could not be
imported by `domain/workspace`. The package contains only:

- `Backend` enum: `tmux | zellij | herdr`.
- `SessionKey`: an opaque, stable key within a Host and backend. It must survive
  a backend process restart and must not be derived from a display name that
  the user can change.
- `Session`: key, display name, state (`live | exited | unknown`), creation
  time, attached count, and other backend-neutral metadata the sidebar needs.
- `AttachSpec`: backend, key, resumed flag, requested grid, and an optional
  scrollback/history hint. This is the domain-level request a Mux Pane hands to
  the Session layer.
- `Side` (`left | right | up | down`) for Session Split.
- `ErrorKind` and `Error` with `Permanent()`: missing, too old, incompatible,
  timeout, network, auth, host-key, unsupported, not found, exited, invalid,
  mode changed.
  The herdr taxonomy is the template (`[herdr]
  application/session/herdr.go:184-311`); do not copy herdr's protocol-specific
  kinds.
- Naming/validation rules that must be shared by all backends (safe key
  charset, deterministic Netcatty-created names, collision policy). Backend
  syntax stays in `domain/model/<backend>`.

Backend-specific pure helpers:

- `domain/model/zellij`: list/create/kill/delete/split/attach command builders,
  output parsers, minimum version, environment-cleaning rules, and detach/exit
  classification. The companion lifecycle ticket defines the exact CLI facts.
- `domain/model/tmux`: extend the existing package with list/create/kill/split
  builders and a parser. Keep `ProbeCommand`/`WrapperCommand` for the legacy
  per-Host auto-attach path. Do not duplicate the parser in two application
  contexts.
- herdr already has `domain/model/herdr` on its branch; its address/version/
  naming rules can be reused when the herdr Mux slice starts.

Extend `domain/workspace.SessionDescriptor` with an additive, omitted-when-empty
`Mux *mux.Binding` (backend, key, display name, local flag if needed).
`Valid` must require the Mux fields exactly when they are present and must
reject a Mux binding on a transport that cannot support it. Do not encode SSH,
PTY, command lines, or credentials in the descriptor. The Host's terminal mode
remains the gate: a saved Mux Pane whose Host mode no longer matches must fail
closed with a typed error, never silently reopen a plain shell (the map's
ticket 05 requirement; herdr already has `HerdrErrorModeChanged` as prior art,
`[herdr] application/session/herdr.go:224-227`).

`TerminalMode` itself belongs to `domain/vault` (and local terminal
preferences). Adding `zellij` and the local variants is ticket 03's decision;
this report only notes that it changes the gate the watcher must read, and that
`AutoAttachTmux` should be treated as a legacy alias for `tmux`, not a second
independent switch.

### 4.2 Application: a new `application/muxsync` context

Create `application/muxsync` with:

- A `Service` that owns a registry keyed by `(HostID, Backend)`. A Host has at
  most one backend because `TerminalMode` is single-select.
- `Watch(ctx, Target, Backend, WatchOptions) (Handle, error)` or an equivalent
  ref-counted `Acquire`/`Release` pair. The first handle starts one watcher
  goroutine; the last release stops it (optionally after a short grace period
  for Tab moves).
- `Snapshot(Target) (HostSnapshot, bool)` returning an immutable copy with a
  monotonically increasing `Revision`, a status, a session list, and a typed
  error. Copy the slices; never return the live map (System Manager's
  `Snapshot` is the precedent, `application/sysmanager/service.go:589-610`).
- `Subscribe(fn func()) func()` for redraw only. Listeners run outside the
  lock and must not block (`service.go:466-482`).
- Actions: `Create`, `Kill`, `Delete`, `Split`, `Refresh`, and `SetCadence`.
  Each returns a typed `ActionResult` and error kind. Mutations are serialized
  per Host (single-flight queue) so create/kill/split do not race each other or
  the poll.
- A `Source` port, implemented by one backend adapter at a time:

```go
// Illustrative shape only; not an implementation.
type Source interface {
    List(ctx context.Context, target Target) ([]Session, error)
    Create(ctx context.Context, target Target, spec CreateSpec) (Session, error)
    Kill(ctx context.Context, target Target, key SessionKey) error
    Delete(ctx context.Context, target Target, key SessionKey) error
    Split(ctx context.Context, target Target, key SessionKey, side Side) error
}
```

`Target` carries only a Host ID and a local flag; the app resolves route,
credentials, and endpoint. `Source` returns domain records and domain errors.
It must not expose SSH, PTY, `exec.ExitError`, command lines, or backend
protocol types. This port is the loop's required capability, not a grand
adapter abstraction: start with the methods the first slice needs
(`List`/`Create` for zellij) and add `Kill`/`Delete`/`Split` only when that
slice implements them. A backend with no such operation returns a typed
unsupported error.

- A `Remote` port for backend adapters that need Host execution:

```go
type Remote interface {
    Run(ctx context.Context, command string) (RemoteResult, error)
    Open(ctx context.Context, command string) (RemoteStream, error)
}
```

This is the same shape as herdr's `HerdrRemote` (`[herdr]
application/session/herdr.go:44-70`) and the app-side bridge is the same shape
as `herdrRemoteHost` (`[herdr] app/herdr_remote.go:87-188`). `Run` is for
list/create/kill/delete/split; `Open` is only needed for a persistent control
or event stream. Local targets do not use `Remote`; the adapter runs a local
child process with `os/exec` (see 4.3). Declaring the port in
`application/muxsync` follows ADR-0001: adapters import application for the
interfaces they implement, and `app` implements the remote bridge
(`docs/adr/0001-layered-package-boundaries.md:20-22`).

Why not put the loop in `application/session`?

- It is not per-Session. It outlives Panes, has its own backoff, and must
  survive when all Panes for a Host are closed but the Tab/sidebar remains.
- It would mix a Host-wide control plane into the package that owns PTY
  semantics and `TerminalConn` backpressure.
- `application/session` already became herdr-aware on its branch; adding every
  backend's list/create/kill/split there would make it the god package ADR-0001
  warns about.

There is a valid fallback: `application/session/muxsync` as a subpackage. The
archtest allows imports between packages of the same application context
(`internal/archtest/arch.go:51-61`, `:101-114`), so that placement could reuse
`StartSpec` and `Transport` directly. Choose it only if implementation shows
that the loop needs those types rather than merely constructing a `StartSpec`
in the UI. The new-context design keeps the dependency arrow cleaner and is the
recommendation.

Why not `application/sysmanager`? Its service is per visible surface, its
connector only exposes `Exec`, and it is not keyed per Host. Reuse its loop and
command patterns conceptually, but do not extend its lifecycle.

#### Binding ownership

The ticket says the loop "maintains a 1:1 binding to Netcatty Mux Panes". In
this codebase that binding cannot live in application:

- A Pane is a window-scoped `ui/shell/state` object with a `model.PaneID`
  (`ui/shell/state/workspace.go:37-70`).
- ADR-0006 requires window-scoped state to live in `ui/<area>/state`
  (`docs/adr/0006-native-window-and-tab-model.md:17`).
- Multiple windows can show the same Host; application has no window identity.

So split it:

- Application owns the **session-side** identity and snapshot: a stable
  `(HostID, Backend, SessionKey)` and its current metadata.
- Each window's shell state owns the **Pane-side** binding: which of its Panes
  currently renders that key, plus focus and layout. It can find a Pane by
  scanning its own Pane descriptors (`Tab.panes`,
  `ui/shell/state/workspace.go:57-70`); the number of Panes is small.
- Application publishes keyed snapshots, not Pane events. The shell decides
  whether an external disappearance closes a Pane, and whether a new session is
  only a sidebar row.

If a cross-window registry is ever needed, it belongs in an app-level UI
coordinator, not in `application/muxsync`, and it counts interest, not Panes.

#### Reconciliation rules the service must encode

- Publish a new `Revision` on every authoritative list, even when the set is
  unchanged, so the UI can clear a "checking" state without guessing.
- Keep the last good list while reconnecting. Do not replace it with an empty
  list on a transport error (System Manager keeps a live sample visible while
  retrying, `application/sysmanager/service.go:551-557`).
- A successful list after reconnect is authoritative and replaces the set.
- Track pending local mutations (create/kill/delete/split) by key and revision.
  A list that started before the mutation finished must not erase the new
  session or resurrect a killed one. System Manager's generation guard
  (`service.go:861-875`) is the minimal precedent; Mux Sync needs a per-key
  pending set in addition to the run generation.
- Deduplicate actions by key and make kill/delete idempotent: "already gone"
  is success or a typed not-found, according to ticket 07.
- Never kill on Pane close. Never resurrect an EXITED session. Attach failures
  for `not_found`/`finished` are permanent; `unavailable`/EOF without a CLOSE is
  retryable (herdr's classifier is the model, `[herdr]
  application/session/herdr.go:267-283`, `:285-302`).

### 4.3 Adapters

One adapter per backend, added in slices:

- `adapters/zellij` first. It implements `muxsync.Source` and, for attach,
  exposes an attach-spec builder. It uses `os/exec` for local targets and
  `muxsync.Remote` for Host targets. Command templates and parsers live in
  `domain/model/zellij`.
- `adapters/tmux` later. It can start from the existing pure probe/wrapper and
  the System Manager tmux commands, but the application-level command strings
  should move to `domain/model/tmux` so both callers share one parser.
- `adapters/herdr` last. It already has the runtime and remote bridge. Add a
  `muxsync.Source` implementation that reuses `HerdrRuntime.List/Create` and
  adds kill/delete only after the herdr protocol slice provides
  `terminal.kill`/`terminal.rm`. Session Split returns an unsupported error
  because herdr has no internal panes (ADR-0018).

Local versus remote execution:

- Remote: acquire a `LeaseChannel` via the app-implemented `Remote` port. Use
  `Run` for short commands. Use `Open` only for a persistent stream. Release
  the lease on completion; never `Discard` on a command failure, because the
  Connection may still serve Panes (herdr's `Run` uses Release for this reason,
  `[herdr] app/herdr_remote.go:108-126`).
- Local: run a child process with `exec.CommandContext`, a timeout, and a
  bounded stdout/stderr buffer. Clean `ZELLIJ*`/`TMUX*` environment variables
  as the lifecycle ticket requires. This is the same split herdr already uses:
  local child via `bridge.go`, remote via `HerdrRemote`.
- Do not use `platform/process.Start` for mux control. It is a shell wrapper
  with stdin/stdout pipes, no exit status, no context, and it kills the process
  on Close (`platform/process/process.go:23-69`). It is intended for
  ProxyCommand-style streams, not for parsing command results.
- Attach: do not build a new PTY implementation. For tmux/zellij, the attach is
  a PTY running a fixed command. The clean seam is a generic domain
  `mux.AttachSpec` on `StartSpec`; the app factory maps it to the backend's
  attach command or attach transport. On the herdr branch, `StartSpec.Herdr` is
  routed to `adapters/herdr` before the Kind switch (`[herdr]
  adapters/herdr/attach.go:50-55`, `[herdr] app/session_factory.go:123-130`).
  Generalize
  that pattern instead of overloading `RemoteCommand` and hoping detach
  semantics match.
- For tmux, preserve the existing `DetachedExitStatus` mapping
  (`domain/model/tmux/tmux.go:20-24`, `app/session_factory.go:336-430`,
  `ui/shell/state/workspace.go:1910-1951`). For zellij, closing the client
  process is the detach; the lifecycle ticket owns the signal details. For
  herdr, Close sends detach, never kill (`[herdr] adapters/herdr/attach.go:534`).

Reuse from herdr, concretely:

| Herdr artifact | Reuse for Mux Sync | Not reusable |
|---|---|---|
| `HerdrTarget`, `HerdrTerminalKey` (`[herdr] application/session/herdr.go:20-42`) | Host-scoped stable identity, broadcast dedup key | herdr-specific address format |
| `HerdrRemote` + `app/herdr_remote.go` (`[herdr] application/session/herdr.go:44-70`, `app/herdr_remote.go:87-188`) | Shape of a narrow exec/stream port over the Connection Manager | herdr's fixed command templates |
| `HerdrErrorKind`, `Permanent`, `NoticeCode` (`[herdr] application/session/herdr.go:184-311`) | Structural error and permanence model | herdr protocol kinds |
| `HerdrRuntime` short-lived list/create (`[herdr] adapters/herdr/runtime.go:234-299`) | Per-operation bridge/branch lifetime; no shared state | `terminal.list`/`terminal.run` methods |
| `adapters/herdr` attach transport (`[herdr] adapters/herdr/attach.go`) | Credit/backpressure, stderr drain, detach-on-close, Close timeout | framed protocol, attach, resume, history |
| `HerdrPicker` (`[herdr] application/session/herdr.go:503`) | A list snapshot with a stable selection is a useful UI shape | herdr picker UI |
| `adapters/herdr/fake_test.go` | Fake child/CLI test harness pattern | protocol assertions |
| ADR-0018 decisions | One stream per Pane; close means detach; fail closed on permanent modes | herdr-specific size leader rules |

### 4.4 Composition root (`app`)

Add `app/muxsync.go`:

- A `muxRemoteProvider` that resolves a Host and route and returns a
  `muxsync.Remote` implemented over `Manager.Acquire` with `LeaseChannel`.
  Copy the Request construction from `herdrRemoteHost.acquire`
  (`[herdr] app/herdr_remote.go:87-104`) and `sysmanagerConnector.Open`
  (`app/sysmanager.go:25-57`), including `Jumps`, `Proxy`, `Network`,
  `Algorithms`, `ConfigFingerprint`, and `Keepalive`. This is what makes the
  control plane share the Pane connection instead of opening a second one.
- A backend registry that selects the adapter from the Host's current
  `TerminalMode`; a Host with `none` has no watcher.
- Construct one app-scoped `muxsync.Service`, pass it to every shell state via
  an option (like `WithSystemManager` / `WithSideTools`), and close it on
  shutdown.
- Shutdown order: stop the watcher and release its leases **before**
  `shutdownSessions` and `connections.CloseAll` (`app/app.go:1440-1441`,
  `:1846-1849`), otherwise a watcher can re-acquire while the pool is closing.
- Do not start a watcher during Vault browsing. Start it when a Mux Tab/Pane is
  created or restored, after the Host has a successful connection (or at least
  after the user's explicit open action). `Acquire` can block on host-key and
  auth prompts; a background Vault selection must not trigger them. System
  Manager starts sampling only on Activate (`ui/sysmanager/state.go:273-280`);
  herdr lists only inside the picker flow. Follow those precedents.

### 4.5 UI

Add `ui/muxsessions` for the Live Sessions list and its per-window state:

- `State` holds the current `muxsync.Snapshot` per watched Host, a "new since revision"
  marker, selection, collapse/filter state, and a subscription handle. It reads
  snapshots; it does not command the backend.
- `View` renders rows and emits intents: open a Pane for a key, kill, delete,
  split, refresh.
- The shell `state.State` owns the Pane binding map and the actions that touch
  Tabs/Panes. It can scan a Tab's Pane descriptors for the key and reuse
  `openDescriptor`/`SplitFocusedPane`/`CloseFocusedPane` rather than inventing a
  parallel layout path (`ui/shell/state/workspace.go:1187`, `:1233`, `:1741`).
- Mounting options:
  1. **Dedicated Live Sessions panel (recommended).** It is part of every Mux
     Tab, can be collapsed, and does not depend on the Files panel being open.
     Reuse the dock/width ideas and `SideTarget`, but give it its own open state.
  2. **`SideTool` extension.** Add a `SideSessions` tool and auto-open the Files
     panel for Mux Tabs. This reuses the strip, dock, and `FinishSideFrame`
     cleanup (`ui/shell/state/files.go:73-83`, `:156-165`), but it inherits the
     Files panel's toggle semantics and the current gate that hides all tools
     when `filesOpen[tab]` is false (`:109-125`). Use it only if the prototype
     (ticket 04) shows the UX is acceptable.
- The detached-window view also calls `Flush` (`ui/shell/detached_view.go:22`).
  Put the apply logic in one shared helper so every window gets the same
  reconciliation.

Async path:

1. Watcher publishes a snapshot and calls each listener.
2. The UI listener only calls `RequestRedraw` (the System Manager pattern,
   `ui/sysmanager/state.go:240`). It must not touch Tabs, terminals, or the
   service lock.
3. The next frame calls `State.Flush` (`ui/shell/view.go:59`).
4. `Flush` pulls `Snapshot(host)` for each interested Host and applies:
   - update Live Sessions rows and the new marker;
   - update an open Pane's display label/title if the backend renamed the
     session, without overwriting `CustomName`;
   - close or mark Panes for sessions that are authoritatively gone;
   - record typed action outcomes and clear "busy" state.
5. `Flush` finishes with `closeDetachedSessions` and `refreshBroadcastState`
   (`ui/shell/state/state.go:1123-1129`). Add the Mux apply step before or after
   `closeDetachedSessions` explicitly; do not let two independent closers race.

"New" marker and click-to-open:

- Application's snapshot can carry first-seen revision/time. The UI decides
  when the marker is cleared (open or dismiss). If it must survive window
  recreation, persist it in application or preferences rather than in the
  ephemeral UI state (ticket 04/08).
- Clicking a row opens a Pane only if none exists in that window. If another
  window already shows the key, the current window may still open a second
  viewer; ticket 08 decides whether that is allowed. The UI needs an explicit
  dedup policy; application cannot enforce it without Pane identity.

Pane Split vs Session Split:

- Pane Split stays in `State.SplitFocusedPane` (`workspace.go:1187`). After the
  outer Pane exists, call `muxsync.Create` and open the new Pane bound to the
  created key. The ordering must avoid a blank Pane and must handle a failed
  create without leaving an orphan split (ticket 06).
- Session Split calls `muxsync.Split` and leaves the Pane untouched. It is
  disabled for herdr and unavailable when the backend version or mode does not
  support it. The menu label should name the backend, per the map.

### 4.6 Tests and architecture constraints

Application tests (`application/muxsync`):

- Fake `Source` and `Remote`; fake clock/ticker; deterministic IDs.
- Ref-counting: two Watches start one loop; the first Release keeps it; the
  last stops it; a grace period is testable with the fake clock.
- Backoff: interval, doubling after two failures, six-interval cap, reset after
  success; stop on auth/host-key/unsupported; context cancel stops promptly.
- Reconnect: stale run generation is ignored; last good snapshot is retained
  with a degraded status; a later list replaces it.
- Reconciliation: external add does not produce an open event; external remove
  is reported; rename updates name; a stale list does not erase a pending
  create or resurrect a killed key; duplicate kill is idempotent.
- Snapshot immutability: mutating the returned slices does not affect the
  service; `Subscribe` listeners are copied and cannot block the loop.

UI tests (`ui/shell/state`, `ui/muxsessions`):

- Fake mux service implementing `Snapshot`/`Subscribe`; call `Flush` directly
  (existing tests already do, e.g. `ui/shell/state/mcp_host_test.go:68`).
- Sidebar new marker, click-to-open, open-Pane rename, close-on-kill, and
  no-close-on-unavailable.
- Multi-window refcount: two shell states share the service; closing one
  releases only its handle.
- Detached view applies the same snapshot path.

Adapter tests:

- Pure command builder/parser tests in `domain/model/zellij` and
  `domain/model/tmux`, including quoting and hostile names.
- Local adapter tests with a fake CLI executable or a test hook, following the
  herdr fake pattern (`[herdr] adapters/herdr/fake_test.go`). No real zellij or
  tmux in the default suite.
- Remote adapter tests with a fake `muxsync.Remote`; assert the exact fixed
  command and that stderr is drained and never logged.
- Opt-in integration tests gated by an environment variable, as herdr does
  (`NATCATTY_HERDR_INTEGRATION=1`, ADR-0018:69-75). CI should not require a
  backend binary.
- The environment has no race detector on Windows; concurrency tests must be
  deterministic and condition-based (no fixed sleeps), as the herdr work
  already learned.

Architecture test:

- `application/muxsync` may import only the standard library and `domain/...`
  (`internal/archtest/arch.go:51-61`). It cannot import `application/session`
  or `application/sysmanager`. If it needs `StartSpec`, use the
  `application/session/muxsync` fallback or move the type to a domain package.
- `adapters/zellij`/`adapters/tmux` may import `application/muxsync`,
  `domain/model/*`, `platform`, and other adapters (`arch.go:62-74`).
- `ui/muxsessions` may import `application/muxsync`, `domain`, `ui`, and MyGo
  (`arch.go:75-80`).
- `app` is the only place allowed to wire the Connection Manager to the
  `Remote` port.

## 5. Shared Connection or per-Pane transport?

The question is best answered in three separate layers:

| Layer | Decision | Why |
|---|---|---|
| Physical SSH Connection | One per Host endpoint, shared by the pool | `Manager.Acquire` + pool already does this (`manager.go:433`, `pool.go:271-441`). Do not build a second sharing layer. |
| Control plane | One watcher lease per Host while watched; mutations may use their own short-lived leases | A persistent lease keeps the Connection alive and avoids re-acquire per poll. Short-lived action leases match `sysmanager.execute` (`actions.go:185-220`) and herdr's per-call acquire. |
| Data plane | One `appsession.Session`/`Transport` per Mux Pane | Required for one live view, resize, detach, reconnect, and failure isolation (`session.go:290`, `:693`; `conn.go:384`). |

Capabilities and limits:

| Capability | What it does | Limits for Mux Sync |
|---|---|---|
| `Lease.Exec` (`exec.go:84`) | One command to completion, buffered stdout/stderr, context cancellation | Output is unbounded (`adapters/ssh/terminal.go:157-172` uses `bytes.Buffer`). Add timeouts and either a bounded parser or `OpenExec` + `io.LimitReader`. No PTY. |
| `Lease.OpenExec` (`exec.go:93`) | Raw stdin/stdout stream + stderr; caller owns it until Close | The caller must keep its lease alive until Close and drain stderr (`[herdr] app/herdr_remote.go:162-173`). Good for a persistent control/event stream, not for a Pane's terminal. |
| `Lease.OpenTerminal` (`manager.go:1259`) | PTY channel with resize | Do not run control commands through it; bytes would mix with the TUI. |
| `Transport` / `Factory.Open` (`session.go:290`, `:431`) | Per-Session byte stream and reconnect | Wrong granularity for Host-wide discovery; right for one Pane. |
| `Lease.Done` (`manager.go:1186`) | Connection closed | Not per channel; a dead exec channel appears as an exec error or stream EOF. |
| `Lease.SingleChannel` (`exec.go:116`) | Peer supports only one channel | Control + Pane attach cannot coexist. Fail Mux Sync closed or degrade explicitly; never silently open a plain shell. |

Coexistence facts and rules:

- SSH transports open a new session channel per `Exec`, `OpenExec`, or
  `OpenTerminal`; multiple channels on one Connection are normal.
- The tmux auto-attach path already runs an exec probe and then a PTY on the
  same Host connection (`app/session_factory.go:157-221`). It skips the probe
  for single-channel peers (`:229-235`). Mux Sync can do the same: control
  commands first or in parallel, attach independently.
- Local backends have no Connection Manager. The attach is a local PTY child;
  control commands are separate local child processes. They talk to the same
  backend server, so they coexist; environment cleaning and process-group
  handling matter.
- Do not multiplex Panes over the watcher's stream. A persistent control
  stream (for example tmux control mode or a zellij event subscription) is for
  control events only. Each Pane keeps its own attach stream. This is the
  direct lesson of ADR-0018's rejected shared `hc connect`
  (`[herdr] docs/adr/0018-herdr-terminal-mode-modifier.md:20-23`).
- Serialize mutations per Host in the service even though each `Exec` opens its
  own channel. Backend CLIs can lock their own state, and ordering between
  create/kill/split matters for reconciliation. Sysmanager's action queue and
  its per-action lease are the pattern (`actions.go:185-220`).
- Never `Discard` the watcher lease because one command failed. `Discard`
  invalidates the physical transport and would take Panes with it
  (`manager.go:1292`). Let the Manager own health; release only.

## 6. Lifecycle

### 6.1 Who starts and stops the loop

The loop is app-scoped, but its lifetime is driven by user interest:

| Event | Action |
|---|---|
| A Mux Tab's Host Session connects, or a Mux Pane is restored | `Watch` the Host's backend |
| A Mux Pane opens | Keep or add a handle (a Tab handle usually covers it) |
| A Mux Pane closes | Release the Pane's handle; keep the Tab's handle |
| A Mux Tab closes or moves windows | Release/move the handle; refcount across windows |
| The last interested window closes | Release the last handle; stop after a short grace period |
| Host terminal mode changes | Stop the old watcher; release Panes with a typed mode-changed error (fail closed) |
| Host removed or route invalidated | Stop; surface a typed error |
| App quits | `muxsync.Close()` before Sessions and Connections |

The service should not start on Vault selection or on app startup. It may block
on host-key or auth prompts through `Manager.Acquire`; those must follow a user
action, as they do for System Manager activation and for a Session open. A
restore that validates saved bindings is an explicit user action too; it should
show the same typed connection state as an open, not start a silent poll.

### 6.2 Cadence and backoff

Start with polling. Zellij's v1 update source is not settled by this report;
the lifecycle ticket decides whether a usable event subscription exists. tmux
control mode and any zellij event stream can be added behind the same `Source`
port once that ticket proves them. The first implementation does not need to
solve both.

- Default cadence: the System Manager's 3 seconds is a reasonable visible
  default; hidden Panes-only interest can use a slower cadence. The lifecycle
  ticket should set the latency target.
- Backoff: copy `sysmanager.Service.run` (`service.go:612-653`): delay starts
  at the interval; after two consecutive failures it doubles; it caps at six
  times the interval; a success resets failures. Add jitter only if many Hosts
  make synchronized polling a real problem; deterministic tests are easier
  without it.
- Stop retrying on auth, host-key, unsupported, invalid mode, missing backend
  version, or invalid configuration. Keep retrying on timeout, network, and
  transient backend unavailability. The herdr error classifier is the model
  (`[herdr] application/session/herdr.go:267-283`).
- Keep the last good snapshot and a degraded status while retrying, so the
  sidebar does not blink empty. On the first successful list after reconnect,
  publish a new revision and reconcile.

### 6.3 Relationship to Session reconnect

They are independent by design:

- A Pane's `Session` reconnects through `Session.reconnect` at
  `DefaultReconnectDelay` (5s) only for retryable Error/Closed ends
  (`application/session/session.go:153-185`, `:1328-1352`, `:1456-1485`). It
  reopens through `Factory.Open` with the same spec.
- The watcher must not call `Session.ReconnectNow` or restart Panes. It retries
  its own control operations.
- Both use `Manager.Acquire`; the pool joins in-flight dials
  (`pool.begin`/`pendingDial`, `pool.go:271`), so one physical reconnect serves
  both. This is why endpoint parity in the Request matters.
- A watcher lease held during a Pane reconnect keeps the Connection alive and
  makes the Pane's re-acquire reuse it. That is beneficial while the user is
  watching, and a reason to release the watcher when interest ends.

**A concrete gap to close for Mux attach:** on `main`, reconnect passes the
original `StartSpec` unchanged (`session.go:1472`). On the herdr branch,
`reconnectSpec` flips `Herdr.Resumed` to true, sets history to 0, and uses the
Pane's current grid (`[herdr] application/session/session.go:1604-1623`). A
generic Mux attach needs the same first-attach versus reattach distinction:

- first attach: request initial scrollback/history and `resumed=false`;
- reconnect: keep the same key, request no duplicate history,
  `resumed=true`, and use the current `Snapshot().Cols/Rows`.

Generalize this as a backend-specific `ReconnectSpec` transform (a small
interface or a method on the Mux attach spec holder) rather than adding another
herdr-only branch. Without it, a reconnect will either duplicate scrollback or
fail to reattach the same session. This is a design requirement for ticket 05.

### 6.4 Idle, hibernate, and shutdown

- Hibernate releases the live view but keeps the transport and application
  state (`application/session/session.go:238-241`). The Pane still exists, so
  the watcher should keep enough interest to update its title and close it on
  an authoritative external kill. Reduce cadence when no view is visible; do
  not necessarily stop.
- Idle parking: if the watcher holds the last lease, the Connection never parks.
  Release the watcher when the last Tab/Pane/window interest is gone. If a
  grace period is used, keep it short and visible in tests.
- App shutdown: stop watchers, cancel their contexts, release leases, wait for
  goroutines, then close Sessions and Connections. The existing order is
  `shutdownSessions` then `connections.CloseAll` (`app/app.go:1440-1441`);
  insert the watcher shutdown before both.

## 7. Risks

1. **Unbounded `Exec` output.** The SSH adapter buffers stdout and stderr in
   `bytes.Buffer` (`adapters/ssh/terminal.go:157-172`). A huge session list or
   a banner can allocate without bound. Use a timeout plus either a bounded
   list format, `OpenExec` with `io.LimitReader`, or a size cap in the adapter.
2. **Single-channel peers.** `Lease.SingleChannel()` (`exec.go:116`) makes
   control plus attach impossible. Fail closed with a typed state; do not
   silently fall back to a plain shell (the ticket's "never silent fallback"
   rule). The tmux auto-attach code already has a limited fallback for a
   missing probe; do not copy that behavior into Mux Sync.
3. **Holding a lease prevents idle parking.** `DefaultIdleTTL` is five minutes
   and the pool parks only at zero references (`pool.go:17-21`, `:441`). Many
   watched Hosts mean many live SSH connections. Refcount interest, cap active
   watchers, and release on idle.
4. **Endpoint mismatch opens a second physical Connection.** The Request must
   match the Session factory's fields exactly (`app/session_factory.go:177-192`,
   `app/sysmanager.go:38-52`, `[herdr] app/herdr_remote.go:87-104`). A missing
   `ConfigFingerprint`, `Network`, or `Proxy` is a duplicate-connection bug that
   is easy to miss.
5. **Per-channel liveness is not exposed.** `Lease.Done` is Connection-level
   (`manager.go:1186`). A dead control channel appears only as an exec error or
   stream EOF. Treat both as control-plane loss; re-acquire and re-list; do not
   discard the Connection.
6. **Reconnect storms.** N Panes plus a watcher retrying can produce repeated
   Acquires. The pool joins concurrent dials, but each Acquire still goes
   through auth and host-key checks. Keep one watcher backoff and do not add
   per-Pane extra retries.
7. **A wedged attach blocks the Session loop.** ADR-0018 documents a herdr
   child that stops reading stdin while `Resize` holds the write token; the
   Session loop then cannot observe Close (`[herdr]
   docs/adr/0018-herdr-terminal-mode-modifier.md:58-65`). Separate control and
   Pane streams contain the blast radius. Do not multiplex all Panes over one
   stream.
8. **Default 80x24 attach.** `Service.Open` defaults Cols/Rows when the caller
   does not set them (`application/session/session.go:693-706`). A Mux attach
   must be opened with the Pane's measured grid, and reconnect must use the
   current grid, or the backend and other viewers get a wrong size. Herdr
   already requires this; tmux/zellij are affected too.
9. **Descriptor migration.** `SessionDescriptor` has no Mux fields
   (`domain/workspace/payload.go:33-64`), and `SanitizePayload` drops unknown
   payload versions (`:123-127`). A version bump would silently discard every
   saved workspace. Add an optional field compatibly, validate it strictly, and
   gate it on the Host's terminal mode. Never restore a Mux Pane as a plain
   shell.
10. **Terminal mode migration.** `TerminalMode` is single-select and the local
    variants are narrower. `AutoAttachTmux` is a legacy alias for `tmux`
    (ADR-0017, `[herdr] docs/adr/0018-herdr-terminal-mode-modifier.md:26-30`).
    The watcher must read the current mode, not the descriptor alone, and must
    fail closed when they disagree. This is ticket 03/05's gate.
11. **Local tmux is not generally available.** The map allows local
    `none | herdr | zellij`, not local tmux. Do not install a tmux control
    runner on Windows or pretend a local tmux mode exists. Remote tmux only.
12. **Polling cost and trust prompts.** A 3-second poll over a jump chain is
    expensive and can wake a laptop. Start on explicit interest, use a slower
    hidden cadence, and never trigger a trust prompt from Vault browsing.
13. **New-marker persistence and multi-window semantics are unresolved.**
    Window-scoped UI state cannot be the authority for "seen" if the marker
    must survive a window close. Ticket 04/08 must decide; application can
    carry first-seen revisions, but it must not carry Pane or window IDs.
14. **No race detector in this environment.** Concurrency correctness must be
    proven with deterministic fakes and condition-based waits, not only
    `-race`. This matches the herdr test practice.

## 8. Open questions routed to other tickets

- **Ticket 03 (domain/terminal mode):** exact `Backend`/`SessionKey` domain
  shape, descriptor compatibility, `TerminalMode` zellij/local variants,
  naming/collision rules, and the exact `domain/model/mux` vocabulary.
- **Ticket 04 (sidebar prototype):** dedicated Live Sessions panel versus
  `SideTool`; collapse/width/dock; where "new" state lives; click-to-open
  behavior.
- **Ticket 05 (binding/restore/reconnect):** generic `MuxAttachSpec` and the
  `reconnectSpec` transform; measured grid at attach; mode-change gate;
  EXITED/missing/version-mismatch classification; multi-view size leader.
- **Ticket 06 (splits):** ordering of outer Pane Split versus backend create;
  failure/orphan handling; Session Split menu gating.
- **Ticket 07 (kill/delete):** herdr `terminal.kill`/`terminal.rm` availability,
  confirmation, idempotency, and "close Pane = detach" enforcement.
- **Ticket 08 (reconciliation):** poll versus event stream per backend,
  cadence/latency targets, pending mutation/revision rules, external rename
  behavior, new-marker clearing, multi-window duplicate viewers, and conflict
  policy.
- **Ticket 09 (platform/security):** local process execution, environment
  cleaning, command injection, trust prompts, logging policy, Windows/ConPTY.
- **Ticket 10 (zellij slice):** exact CLI commands, version floor, detach
  signals, EXITED handling, and whether a persistent event stream exists.
- **Ticket 11 (tests/done):** fake backend contract, opt-in integration target,
  multi-window and reconnect acceptance tests.

## 9. Reference index

Application and connection:

- `application/session/session.go:153-185` reconnect policy and delay.
- `application/session/session.go:188-242` immutable `Snapshot`.
- `application/session/session.go:290-293` `Transport`.
- `application/session/session.go:347-408` `StartSpec`.
- `application/session/session.go:431-433` `Factory`.
- `application/session/session.go:568-589` `Service`.
- `application/session/session.go:693-706` `Service.Open` and grid defaults.
- `application/session/session.go:1063-1075` `CloseAll`.
- `application/session/session.go:1297-1443` Session loop.
- `application/session/session.go:1456-1485` reconnect.
- `application/session/conn.go:18-29` `TerminalConn`; `:384` one live view.
- `application/connection/exec.go:15-55` exec result/stream/executor.
- `application/connection/exec.go:84-99` `Lease.Exec`/`OpenExec`.
- `application/connection/exec.go:116-130` `SingleChannel`/`Reused`.
- `application/connection/manager.go:35` `Request`.
- `application/connection/manager.go:390-400` `CloseAll`.
- `application/connection/manager.go:433` `Acquire`.
- `application/connection/manager.go:1120-1320` `Lease` lifecycle.
- `application/connection/pool.go:17-21` idle TTL and cap.
- `application/connection/pool.go:77-83` LeaseKind.
- `application/connection/pool.go:118-127` reuse rules.
- `application/connection/pool.go:271-441` begin/borrow/park.

Composition and existing bridges:

- `app/session_factory.go:90-135` factory switch and SSH path.
- `app/session_factory.go:157-253` acquire, tmux probe, SingleChannel.
- `app/session_factory.go:336-430` leased transport.
- `app/sysmanager.go:25-131` Connector over the Manager.
- `app/app.go:1440-1441` shutdown order; `:1846-1849` shutdown helper.
- `platform/process/process.go:23-69` why it is not a control exec port.

Application patterns:

- `application/sysmanager/service.go:135-140` ports.
- `application/sysmanager/service.go:426-464` service state.
- `application/sysmanager/service.go:468-482` Subscribe.
- `application/sysmanager/service.go:487-526` Start/Stop.
- `application/sysmanager/service.go:589-610` Snapshot.
- `application/sysmanager/service.go:612-653` backoff.
- `application/sysmanager/service.go:805-834` failure stop classes.
- `application/sysmanager/service.go:861-887` generation guard and notify.
- `application/sysmanager/actions.go:185-220` short-lived action lease.
- `application/sysmanager/commands.go:122-132` tmux list command.
- `application/sysmanager/commands.go:277-330` tmux create/kill/attach.

Domain and UI:

- `domain/model/tmux/tmux.go:20-172` pure probe/wrapper/quoting.
- `domain/workspace/payload.go:11-78` descriptor and validity.
- `domain/workspace/payload.go:123-127` unknown-version discard policy.
- `ui/shell/state/state.go:129-280` UI-thread state and queues.
- `ui/shell/state/state.go:784-840` async Host open.
- `ui/shell/state/state.go:1060-1130` `Flush`.
- `ui/shell/state/workspace.go:37-70` Tab/pane.
- `ui/shell/state/workspace.go:1187-1245` split/close.
- `ui/shell/state/workspace.go:1741-1853` descriptor to StartSpec.
- `ui/shell/state/workspace.go:1910-1951` detach close.
- `ui/shell/state/files.go:53-165` SideTool IDs, contract, cleanup.
- `ui/shell/view.go:59` main Flush call.
- `ui/shell/detached_view.go:22` detached Flush call.
- `ui/sysmanager/state.go:240-292` Subscribe/Activate/Deactivate.
- `ui/sysmanager/state.go:340-375` Flush.
- `internal/archtest/arch.go:51-61` application import rule.
- `internal/archtest/arch.go:101-114` same application context.

Herdr branch (13c663951):

- `[herdr] application/session/herdr.go:20-146` target, remote, runtime ports.
- `[herdr] application/session/herdr.go:184-311` errors and permanence.
- `[herdr] application/session/herdr.go:503` picker.
- `[herdr] application/session/session.go:1604-1623` reconnectSpec.
- `[herdr] adapters/herdr/runtime.go:234-299` list/create bridges.
- `[herdr] adapters/herdr/attach.go:50-121` one bridge per attach.
- `[herdr] adapters/herdr/attach.go:534` close is detach, not kill.
- `[herdr] app/herdr_remote.go:87-188` app-side Remote bridge.
- `[herdr] docs/adr/0018-herdr-terminal-mode-modifier.md` shared-bridge
  rejection, per-Pane bridge, resume/size rules, wedged-stdin hazard.

ADRs:

- `docs/adr/0001-layered-package-boundaries.md`
- `docs/adr/0006-native-window-and-tab-model.md`
- `docs/adr/0017-ssh-tmux-auto-attach.md`
- `[herdr] docs/adr/0018-herdr-terminal-mode-modifier.md`
