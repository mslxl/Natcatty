# Backend session lifecycle facts: tmux, zellij, herdr-core

Research report for `TICKET 01 — backend session lifecycle facts` in the Mux Session /
Mux Pane workstream. The feature maps **one backend session or terminal to one Netcatty
Pane**; this report records what each backend can report, create, attach, destroy, and
expose about identity, metadata, platforms, and security.

- Date: 2026-11-08
- Method: read-only inspection of pinned upstream source trees and the tmux manual
  page, plus the existing Netcatty herdr protocol research. No tmux, zellij, or
  `herdr-core` binary was installed or executed, so **no command output below is a
  live capture**. Samples derived from source formats are marked
  `sample (derived)`; behaviours not confirmed by source or documentation are marked
  `UNVERIFIED`.
- Versions verified for this report:
  - tmux **3.8**, tag commit `7f2a35ad3321f9ba57a1062ca73b1f3ff26aca53`.
  - zellij **v0.45.1**, commit `efd8fd5a89a20c07a111d248ad7fce53848d2c18`.
  - herdr-core **v0.1.18**, release tag `3965a46caae095a3ead01de237fa13955e52b8c6`;
    protocol details were previously pinned at commit
    `c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a`.
- Verification labels:
  - **SRC** — read in the pinned source tree.
  - **DOC** — stated by first-party documentation.
  - **UNVERIFIED** — not confirmed by execution or by the sources consulted.

---

## 1. Summary matrix

| Dimension | tmux 3.8 | zellij 0.45.1 | herdr-core 0.1.18 |
|---|---|---|---|
| Discovery | `list-sessions` text (formatable with `-F`, no JSON) | `list-sessions` text only (no JSON) | `terminal.list` JSON-RPC, JSON |
| No server | Exit 1, `no server running on <socket>` | Indistinguishable from "no sessions": exit 1, one message | Core is auto-started by `hc connect`; not an error state |
| Zero sessions | Exit 0, empty stdout (server alive, rare) | Exit 1, `No active zellij sessions found.` | Empty `terminals` array |
| Dead/exited sessions | None persisted; nothing to list | Mixed into the same list, suffix `(EXITED - attach to resurrect)` | `state:"finished"`; hidden unless `all:true` |
| Live changes | Control mode `-C` notifications; hooks; polling | No external lifecycle stream; poll `list-sessions` | `events.subscribe` push + `seq` + `missed` snapshot |
| Create without client PTY | `new-session -d`, `-x/-y` | `attach --create-background`, initial 50x50 | `terminal.run` with `size` |
| Attach | Attach client needs a TTY; `-C` control client does not | Full-session TUI; needs a PTY | `terminal.attach` on framed RPC; no local PTY |
| Kill | `kill-session` (in-memory only) | `kill-session` (leaves EXITED cache); `delete-session --force` removes it | `terminal.kill`; `terminal.rm` removes the record |
| Rename | `rename-session` | `action rename-session` (inside a session) | No session rename; labels only |
| Ownership marker | User option `@name` | None | Labels (`terminal.run.labels`, filterable) |
| Structured metadata | Formats (`-F`), `list-windows`/`list-panes`/`list-clients` | Pane/tab JSON only from inside a session; cache KDL files | Terminal record JSON |
| Native Windows | No | Since 0.44.0 | Yes |
| Socket/pipe protection | Per-user socket dir, socket mode controlled by umask | Per-user socket dir 0700, socket 01700 | Per-user Unix socket dir; Windows pipe ACL (DOC) |

The three backends are not interchangeable: only herdr has a structured, event-driven,
label-aware lifecycle API; tmux has the richest server-side metadata but text/format
output and no persisted dead sessions; zellij exposes the least to an outside process
and requires text parsing plus polling.

---

## 2. tmux

### 2.1 Versions and platforms

- Current release inspected: **tmux 3.8** (tag `3.8`; source commit
  `7f2a35ad3321f9ba57a1062ca73b1f3ff26aca53`). `tmux -V` prints `tmux 3.8` (SRC:
  `tmux.c`, `getversion`).
- Supported systems listed by the upstream README: OpenBSD, FreeBSD, NetBSD, Linux,
  macOS, Solaris. **There is no native Windows build.** Windows use means WSL,
  MSYS2/Cygwin, or a remote Unix host; a remote Windows host cannot run tmux natively.
  (DOC: `README` at the pinned tag.)
- Version floor for the features in this report is not a single number:
  `%client-session-changed` appears in `CHANGES` under 2.5 (2017); `%config-error`
  under 3.4; hooks and control-mode notifications were reworked internally in 3.8.
  The exact oldest version that supports the whole notification set used here is
  **UNVERIFIED**. Treat 3.8 as the target and probe features, not version numbers.

### 2.2 Discovery and listing

Commands:

```sh
# Machine-readable listing; use an explicit format instead of the default text.
tmux -L <label> list-sessions -F '#{session_id}\t#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}\t#{session_activity}\t#{session_path}'

# Exact target existence check.
tmux -L <label> has-session -t '=<name>'

# Extra metadata.
tmux -L <label> list-windows -a -F '#{session_id}\t#{window_index}\t#{window_name}\t#{window_panes}\t#{window_active}'
tmux -L <label> list-panes -a -F '#{session_id}\t#{window_index}\t#{pane_id}\t#{pane_title}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_width}x#{pane_height}\t#{pane_active}'
tmux -L <label> list-clients -F '#{client_name}\t#{client_session}\t#{client_width}x#{client_height}\t#{client_termname}\t#{client_uid}\t#{client_flags}'
```

`list-sessions` uses this default template (SRC: `cmd-list-sessions.c`):

```
#{session_name}: #{session_windows} windows (created #{t:session_created})#{?session_attached, (attached),}
```

A format-derived sample (`sample (derived)`; not executed):

```
$ tmux -L netcatty list-sessions
work: 2 windows (created Mon Nov  3 09:12:04 2026) (attached)
nc-4f7a: 1 windows (created Mon Nov  3 11:40:51 2026)
```

`-F` with literal tabs gives a stable line protocol; the default template is
human-oriented and localized by the `time_format` option.

Exit codes and states (SRC: `client.c`, `cmd-list-sessions.c`):

| Situation | stdout/stderr | Exit |
|---|---|---|
| Server running, one or more sessions | session lines | 0 |
| Server running, zero sessions (only reachable with `exit-empty off`, or transiently) | empty stdout | 0 |
| No server at all | `no server running on /tmp/tmux-<uid>/<label>` on stderr | 1 |
| Socket permission/connection error | `error connecting to <path> (<errno text>)` | 1 |
| `has-session` target missing | `can't find session: <name>` on stderr | 1 |

Key distinctions and side effects:

- **No server vs no sessions is distinguishable** for tmux: exit 1 plus the
  `no server running on` message versus exit 0 with no output.
- **"Only exited sessions" cannot occur**: tmux keeps session state only in the
  server's memory. There is no persistent session catalogue and no tombstone
  (SRC: `session.c`, `server.c`). This is also the answer to the "delete metadata"
  question: **tmux has no delete-metadata equivalent**, because there is no metadata
  to delete; `kill-session` destroys the in-memory object and its user options.
- `list-sessions`, `list-windows`, `list-panes`, and `list-clients` carry
  `CMD_AFTERHOOK`/read-only flags and **do not start a server** (SRC:
  `cmd-list-sessions.c`; `cmd-list-clients.c`). `new-session`, `attach-session`, and
  `start-server` do start one.
- Hang risk: these are local Unix-socket requests. tmux applies no client-side
  timeout; if the server is wedged, the client can block indefinitely. Netcatty must
  impose its own timeout and kill the CLI child. `list-sessions` is cheap (one short
  process), but polling it once per second per host is still process-heavy.

### 2.3 Live changes

Two independent mechanisms exist.

**Control mode** (`tmux -C`):

```sh
tmux -L <label> -C attach-session -t '=<name>'
```

- The control client does **not** need a TTY: `server_client_open` returns success
  early for `CLIENT_CONTROL` (SRC: `server-client.c`). `-CC` is the same stream with
  an initial DCS `1000p` sequence for nested terminals (SRC: `control.c`); it is not
  needed to consume notifications.
- Notifications are line-oriented and include the lifecycle events relevant here
  (SRC: `control-notify.c`, `tmux.1`):
  `%sessions-changed`, `%session-changed`, `%session-renamed`,
  `%session-window-changed`, `%client-session-changed`, `%client-detached`,
  `%window-add`, `%window-close`, `%window-renamed`, `%pane-mode-changed`,
  `%pane-exited`, and `%exit`.
  `%sessions-changed` is emitted when sessions are created or destroyed.
- Latency is effectively immediate: notifications are queued in the server loop.
  There is **no event sequence number and no replay**. After a dropped connection,
  the only resync is to re-read `list-sessions` and re-attach the control client.
- Side effects that matter for a monitor:
  - A control client is still a client. `recalculate_sizes_now` counts every client
    whose session is non-NULL and which is not `CLIENT_DEAD/SUSPENDED/EXIT`, so a
    control client increments `session_attached` (SRC: `resize.c`,
    `server_client_configure` path). It therefore also keeps a session from being
    "unattached" for `exit-unattached`/`destroy-unattached` purposes. A Netcatty
    monitor can accidentally keep external sessions alive.
  - For sizing, a control client is ignored until it reports a size:
    `ignore_client_size()` returns true for `CLIENT_CONTROL` without
    `CLIENT_SIZECHANGED`/`CLIENT_WINDOWSIZECHANGED` (SRC: `resize.c`). A plain `-C`
    monitor therefore does not shrink windows.
- A control client attaches to one session, but `%sessions-changed` covers the whole
  server. Switching the control client's session is possible with `switch-client`.

**Hooks**: `set-hook -g <hook> '<command>'`, `show-hooks -g`. Session-related hooks
include `session-created`, `session-closed`, `session-renamed`, `client-attached`,
`client-detached`, `client-session-changed`, `pane-exited`, `window-linked`, and
`window-unlinked`. Hooks run server-side; they cannot push to Netcatty unless the hook
command writes to a socket/file or runs a helper. Hooks are therefore useful for
marking sessions, not as a transport.

**Polling**: `list-sessions` plus optionally `list-clients` is the fallback. Cost is
one process and one local request per poll; there is no JSON.

### 2.4 Create, attach, detach, resize

```sh
# Background creation, no local TTY needed.
tmux -L <label> new-session -d -s '<name>' -x 120 -y 40 -c '<cwd>' -- <argv...>

# Create-or-attach in one command; -D detaches other clients.
tmux -L <label> new-session -A -D -s '<name>' -x 120 -y 40

# Attach an interactive client.
tmux -L <label> attach-session -t '=<name>'

# Resize while detached (attached windows follow the client).
tmux -L <label> resize-window -t '=<name>' -x 120 -y 40

# Detach.
tmux -L <label> detach-client -s '<name>'      # all clients on that session
tmux -L <label> detach-client -t '<client>'    # one client
```

Facts (SRC: `cmd-new-session.c`, `cmd-attach-session.c`, `cmd-detach-client.c`,
`options-table.c`, `server.c`):

- `new-session -d` starts the server if needed and returns without attaching. Its
  initial size is `-x`/`-y` when supplied; otherwise the detached session uses the
  `default-size` option, default **80x24** (`dsx=80`, `dsy=24`).
- `new-session` without `-A` on an existing name fails with
  `duplicate session: <name>` and exit 1. `-A` attaches to the existing session
  instead.
- `new-session` with a trailing command: multiple words are executed as `argv`
  directly; a single shell-command string is run through the shell. Passing an
  explicit argv avoids shell interpretation.
- `attach-session` requires a normal terminal (the client must open a PTY/TTY).
  `-d` detaches other clients from the session; `-x` detaches them with SIGHUP.
- Client PTY closure is a normal detach: the client exits (reason `lost tty`) and
  the session persists unless `exit-unattached` is on.
- Server lifetime options and defaults (SRC: `options-table.c`, `server.c`):
  `exit-empty on` (server exits when there are no sessions), `exit-unattached off`
  (server survives with sessions but no clients), `destroy-unattached off`.
- Size is decided from attached clients that are not ignored. With several real
  clients, the smallest participating size constrains the window; a control-mode
  client without a reported size does not participate. A regular second client
  (another SSH terminal) can therefore shrink a Netcatty Pane's window.

### 2.5 Kill, rename, idempotency

```sh
tmux -L <label> kill-session -t '=<name>'     # destroy one session
tmux -L <label> kill-server                   # SIGTERM the whole server
tmux -L <label> rename-session -t '=<old>' '<new>'
```

- `kill-session` flags are 0 (it needs an existing server). Missing target produces
  `can't find session: <name>` and exit 1 (SRC: `cmd-kill-session.c`,
  `cmd-queue.c`). It is **not idempotent**.
- Destroying a session fires the `session-closed` hook and `%sessions-changed`.
  Attached clients follow `detach-on-destroy` (default 1): with another session they
  move to it; with none they get `CLIENT_EXIT` (SRC: `session.c`, `server-fn.c`).
  A control client on the destroyed session receives `%exit`; the exact reason string
  after `kill-session` is **UNVERIFIED** (the generic reason strings in
  `client_exit_message()` are `detached`, `detached and SIGHUP`, `lost tty`,
  `terminated`, `server exited unexpectedly`, `exited`, `server exited`).
- `kill-server` sends SIGTERM and destroys all sessions; it is not a per-session
  operation. `start-server` is the explicit start command.
- `rename-session` validates only UTF-8 validity (`check_name`), not shell or target
  syntax. Renaming to the same name is a no-op; a duplicate name fails with
  `duplicate session: <new>` and exit 1. It fires `session-renamed` and
  `%session-renamed`; attached clients stay attached (SRC: `cmd-rename-session.c`,
  `tmux.c`).

### 2.6 Identity and ownership

- Session target syntax: `$<id>` (stable for the server lifetime), exact name,
  unique prefix, glob, or `=<exact name>`. Names may contain characters that are
  meaningful to the target parser (`:`, `.`, `$`), so Netcatty should hold the
  numeric id and pass `$<id>` or `=<name>` explicitly.
- Default names when none is supplied are numeric (`0`, `1`, ...); `session_create`
  can also use a prefix (`<prefix>-<id>`) (SRC: `session.c`).
- Names are only checked for UTF-8 validity when renamed; the display path escapes
  control characters via `clean_name` (`VIS_OCTAL|VIS_CSTYLE|VIS_TAB|VIS_NL`), but
  the stored name is not restricted to a safe alphabet. Line-based parsing of names
  is therefore unsafe; use `-F` with a separator that cannot occur in the format
  values (tabs can technically occur in names too) or accept `$id` as the primary
  key and treat names as display text.
- Ownership marker options:
  - User options: `set-option -t '$0' @nc_owner netcatty`,
    `set-option -t '$0' @nc_uuid <uuid>`. Read back with
    `show-options -t '$0' -v @nc_owner` or in a format as `#{@nc_owner}`.
    This is the closest tmux equivalent to a Netcatty label. It is not a security
    boundary: any client with socket access can read or change it, and it disappears
    when the session or server dies.
  - A name convention (prefix or suffix) also works but breaks when a user renames
    the session.
- Multiple clients are normal. `list-clients` reports each client's name, session,
  size, terminal name, uid/user, and flags. There is no exclusive owner of a session;
  ownership is the OS user that owns the server and socket.

### 2.7 Title and metadata

| Field | tmux source |
|---|---|
| Session name | `#{session_name}` |
| Stable id | `#{session_id}` (`$N`) |
| Created | `#{session_created}` (epoch); `#{t:session_created}` formatted |
| Last activity | `#{session_activity}` |
| Attached clients | `#{session_attached}` |
| Window count | `#{session_windows}` |
| Session group | `#{session_group}`, `#{session_grouped}` |
| Current path | `#{session_path}` |
| Windows | `list-windows -a`: index, name, panes count, active flag, layout |
| Panes | `list-panes -a`: id, title, current command, current path, size, active |
| Windows/pane title | `#{pane_title}`, `rename-window`, `select-pane -T` |
| Command/cwd | `#{pane_current_command}`, `#{pane_current_path}` |

There is no separate session "title". `rename-session` changes the name; a title-like
label must be a user option (`@title`) or another convention. Window names may
auto-rename from the running command (`automatic-rename`).
(`session_path`, `pane_current_path`, and the listed format names are present in the
3.8 format table; the exact table can be dumped with `tmux display-message -p
'#{...}'`.)

### 2.8 Platform notes

- Linux/macOS/WSL/MSYS2/Cygwin: same server/client model; socket directory is
  `$TMUX_TMPDIR` (or `/tmp`) + `tmux-<uid>`.
- Native Windows: no tmux binary. A remote Windows host needs WSL or a POSIX layer;
  WSL behaves as Linux. A "Windows mux" backend would have to be herdr or zellij.
- Version floor: target 3.8; see §2.1 for the uncertainty around the oldest
  supported notification set.

### 2.9 Security

- Socket directory: `$TMUX_TMPDIR`/`/tmp` + `tmux-<uid>`, created mode **0700**. tmux
  refuses to use it if it is not a directory, is owned by another uid, or has group/
  other permission bits set (SRC: `tmux.c`, `make_label`).
- Socket mode: with the default socket, the bind umask yields **0660**; with an
  explicit `-S` path it yields **0600** (SRC: `server.c`, `server_create_socket`).
  `server_update_socket` adds execute bits for every read bit while at least one
  session is attached and removes them when none is (so the effective mode can move
  between 0660 and 0770 for the default socket). Netcatty should use its own `-L`
  label or `-S` path, not the user's default socket, to avoid colliding with an
  interactive tmux.
- Environment redirection:
  - `TMUX` (format `<socket path>,<pid>,<session id>`) selects the socket when
    neither `-L` nor `-S` is given. A Netcatty child process that inherits `TMUX`
    would silently target the user's interactive server. Pin `-S`/`-L` and remove
    `TMUX` (and `TMUX_PANE`) from the environment of tmux clients and of processes
    Netcatty starts.
  - `TMUX_TMPDIR` changes the default socket directory. Pin the socket path.
  - `update-environment` (default list: `DISPLAY`, `KRB5CCNAME`, `SSH_ASKPASS`,
    `SSH_AUTH_SOCK`, `SSH_AGENT_PID`, `SSH_CONNECTION`, `WINDOWID`, `XAUTHORITY`)
    copies selected client variables into the session environment on attach. `TMUX`
    is not in that default list, but the list is configurable.
- Injection surface:
  - Target arguments are parsed by tmux: a name like `a:b` can mean session `a`,
    window `b`. Use `$id` or `=<name>`, and pass names as single argv elements.
  - `-F` format strings and hook/`run-shell` commands are expanded/executed by tmux.
    Never concatenate an external session name or pane title into a format or hook;
    pass it as argv data or read it through a format.
  - `send-keys` writes terminal input; it is an intentional input channel, not a
    metadata API.
  - User options are readable by every client that can reach the socket; they are
    not protected metadata.

---

## 3. zellij

### 3.1 Versions and platforms

- Current release inspected: **v0.45.1** (2026-08-28; commit
  `efd8fd5a89a20c07a111d248ad7fce53848d2c18`).
- The changelog places `platform: native Windows support` in **0.44.0**
  (2026-03-23), together with `zellij action list-panes` and the pane/tab id
  returns. 0.44.x and 0.45.x contain Windows fixes (VT input path, argument
  quoting, PID-based socket liveness, path normalization).
- Therefore:
  - Native Windows: **0.44.0 or newer**.
  - Netcatty minimum can reasonably be **0.44.0**; the compatibility target is
    **0.45.1**.
  - WSL uses the Unix build and Unix sockets; a remote Windows host uses the
    Windows build and named pipes.
- The pre-research claim "native Windows since 0.44.0" is confirmed by the changelog.
  The claim "`list-sessions --no-formatting` has no JSON and mixes live/EXITED" is
  confirmed. The claim "attaching EXITED resurrects and re-runs programs" is only
  partly correct: see §3.4.

### 3.2 Discovery and listing

```sh
zellij list-sessions                 # default: ANSI-coloured text
zellij list-sessions --no-formatting # plain text, still includes created age + EXITED suffix
zellij list-sessions --short         # names only
zellij list-sessions --reverse       # oldest first
```

Source facts (SRC: `zellij-utils/src/sessions.rs`, `src/commands.rs`, `src/main.rs`):

- `--no-formatting` prints, per session:
  `"<name> [Created <humantime> ago] <suffix>"`, where suffix is `(current)`,
  `(EXITED - attach to resurrect)`, or empty. The line ends with a space when the
  suffix is empty (`println!("{} {} {}", ...)`).
- `--short` prints only the name; **it loses the live/EXITED distinction entirely**.
- Default (coloured) output is the same structure with ANSI SGR around the name and
  duration, and a red `EXITED`.
- There is **no JSON mode**. The data is a human listing.
- Exit codes:
  - At least one live or resurrectable session: **0**.
  - None: stderr `No active zellij sessions found.`, exit **1**.
  - Error reading the socket directory: `Error occurred: <Debug io error>`, exit **1**.
- **"No server" and "no sessions" are the same observable**: exit 1 plus the one
  message. "Only exited sessions" is also exit 1, because the same `all_sessions`
  map is checked after merging live and resurrectable entries (SRC: `list_sessions`).
  To distinguish "only exited", Netcatty must use `--no-formatting` (not `--short`)
  and parse the `(EXITED - attach to resurrect)` suffix, or inspect the cache
  directory directly (§3.7).
- Side effects and hang risk:
  - Listing reads `ZELLIJ_SOCK_DIR`, and for every socket-like entry calls
    `assert_socket`, which connects, sends `ClientToServerMsg::ConnStatus`, and
    waits for `ServerToClientMsg::Connected`. On Unix, `ConnectionRefused` causes the
    stale socket file to be deleted. On Windows, the "socket" is a marker file
    containing the server PID; liveness is checked with `OpenProcess` and stale
    marker files are deleted.
  - `assert_socket` has **no timeout** on the Unix receive. A wedged zellij server can
    therefore hang `list-sessions`; Netcatty must time out and kill the child.
  - Each listing also reads the session-info cache directory and the layout file
    mtime/ctime for each resurrectable entry.
- Session creation time in the listing is the socket file's creation time, falling
  back to modification time (Unix), or the layout cache file's time for resurrectable
  sessions. It is not a stored creation timestamp.

### 3.3 Live changes

- There is **no external session-lifecycle event stream**. The CLI has:
  - `zellij subscribe` (top-level command; not `action subscribe`) with
    `--pane-id`, `--scrollback`, `--format raw|json`, `--ansi`. It subscribes to
    **pane render** updates, not to session creation/destruction, and it requires
    an active session / `ZELLIJ_SESSION_NAME`.
  - `zellij action list-panes [--tab] [--command] [--state] [--geometry] [--all]
    [--json]`, `list-tabs [--all] [--json]`, `list-clients`, `query-tab-names`.
    These require being inside a session (a client id / active session), and they
    report the current session's contents rather than the machine's session list.
  - Plugin API: the server sends `Event::SessionUpdate(live_sessions,
    resurrectable_sessions)` to plugins (SRC: `zellij-server/src/screen.rs`). This is
    only available to WASM plugins loaded in a session; there is no first-party CLI
    that turns it into a stream for an outside process. Building a Netcatty plugin
    merely to observe the session list would be a much larger dependency than polling.
- Consequence: external discovery is **polling `list-sessions`**. Cost per poll is a
  process launch plus a `ConnStatus` round trip per live session; latency is the poll
  interval; there are no sequence numbers, no missed-event marker, and no replay.
  Resync after reconnect is simply "list again". A session name with leading/
  trailing whitespace or newlines can break line parsing; names are only validated
  against empty, `.`, `..`, and `/`.

### 3.4 Create, attach, detach, resize, resurrection

```sh
zellij attach --create <name>             # create if no live session with that name
zellij attach --create-background <name>  # create server with no client
zellij attach <name>                      # exact or unique-prefix live session
zellij action detach                      # from inside a session
```

Facts (SRC: `src/commands.rs`, `zellij-client/src/lib.rs`,
`zellij-client/src/os_input_output.rs`):

- **Attach is a whole-session TUI**, not a per-pane attach. It enters the alternate
  screen, sets raw mode, queries/sets terminal modes, and uses the client terminal
  size. Netcatty must provide a PTY for `zellij attach`. There is no CLI to attach
  to a single pane interactively.
- `--create-background` spawns a detached server with **no client**; the initial
  size is the constant **50x50** until a client connects (source comment: static
  number until a client connects). If a live session with that name already exists,
  the client prints `Session already exists` and exits **1** (SRC:
  `zellij-client/src/lib.rs`).
- `--create` on an existing **live** name attaches; on a dead name it does not
  attach the dead session: `session_exists` only consults live sockets.
- Attach name resolution: exact match, then unique prefix; ambiguous prefix prints a
  list and exits 1; no match prints `No session with the name '<name>' found!` and
  exits 1. If no name is given and there is exactly one live session, it attaches;
  with none it prints `No active zellij sessions found.` and exits 1; with several it
  demands a name and exits 1. `ZELLIJ_SESSION_NAME` is used to detect attaching to
  the current session, which is refused.
- **Resurrection** (attach a name that has a cached `session-layout.kdl` but no live
  socket):
  - `zellij attach <name>` builds `ClientInfo::Resurrect(name, layout_path,
    force_run_commands, cwd)` and starts a new server from the cached layout.
  - The cached layout serializer writes `start_suspended true` for panes that have a
    command (SRC: `zellij-utils/src/session_serialization.rs`,
    `serialize_start_suspended`). Therefore, **by default the resurrected panes are
    held/suspended; their commands are not re-run**. `--force-run-commands` calls
    `recursively_add_start_suspended(Some(false))`, which un-suspends them and makes
    them run (SRC: `src/commands.rs`, `zellij-utils/src/input/cli_assets.rs`).
  - Pane contents are restored from the serialized layout (`pane_contents` in the
    manifest); this is a layout/contents restore, not a replay of terminal output.
  - The pre-research statement "attaching EXITED resurrects and re-runs programs"
    should be corrected to: **resurrects the layout; commands stay suspended unless
    `--force-run-commands` is given.**
- Detach: `zellij action detach` from inside (default keybinding `Ctrl-o d`); there is
  no top-level `detach-session`. Closing the client terminal detaches the client;
  the server and its panes keep running. `on_force_close` in config controls whether
  a force-closed client detaches or quits (default detach in the default config;
  verify against the user's config).
- Resize: the client reports its terminal size; the server resizes the session view.
  There is no whole-session resize CLI. Multiple clients with different sizes are a
  zellij-specific behaviour; the exact policy (smallest/last/active client) was not
  established here and is **UNVERIFIED**. Netcatty should attach with the measured
  Pane grid and forward resize events.

### 3.5 Kill, delete, rename

```sh
zellij kill-session <name>              # kill a live session
zellij delete-session <name> [--force]  # remove cache/metadata; --force kills live first
zellij kill-all-sessions
zellij delete-all-sessions
```

Facts (SRC: `zellij-utils/src/sessions.rs`, `src/commands.rs`):

- `kill-session` connects to the socket and sends `ClientToServerMsg::KillSession`.
  Missing/dead name: `ipc_connect` fails and the command prints
  `Error occurred: <Debug io error>` and exits **1**. It is not idempotent. Killing
  leaves the session's cache on disk, so it becomes an **EXITED** entry that can be
  resurrected or deleted.
- `delete-session <name>` without `--force`: for a live session it prints
  `A session by the name "<name>" exists and is active, use --force to delete it.`
  on **stdout** and exits **1**. With `--force`, it first sends `KillSession` to the
  live server, then removes the session-info directory. If the directory does not
  exist, it prints `Session: "<name>" not found.` on stderr and exits **2**. Success
  prints `Session: "<name>" successfully deleted.` and exits **0**. This is zellij's
  delete-metadata equivalent: it removes both the EXITED record and the resurrection
  layout/contents.
- `kill-session`/`delete-session` affect attached clients by killing the server; the
  clients lose their session and exit. Exact client exit code/message is
  **UNVERIFIED** (not executed).
- Rename is an **action**, not a top-level session command: `zellij action
  rename-session <new-name>` (the CLI also exposes `RenameSession { name }`). It
  requires an active session / client context; `zellij action --session <name>` or a
  `ZELLIJ_SESSION_NAME` fallback is used to select the target. It renames the live
  session (socket/cache naming follows the session name). External rename semantics
  and output are **UNVERIFIED** beyond the CLI definition.
- Name validation for sessions: not empty after `trim()`, not `.` or `..`, and no
  `/`. Other characters, including spaces, colons, and newlines, are accepted, which
  makes the text listing and any shell-free line protocol fragile.

### 3.6 Identity and ownership

- The identity is the **session name** (also the socket file name). There is no
  separate stable id exposed by `list-sessions`.
- Rename changes the name and therefore the socket/cache names; external tools that
  cached the name must re-list.
- Multiple clients are supported; `session-metadata.kdl` contains
  `connected_clients`, `web_client_count`, and per-client tab/pane history.
- There is **no ownership or creator field** anywhere in the public CLI listing or in
  `SessionInfo` / `SessionLayoutMetadata`. `session-metadata.kdl` has tabs, panes,
  plugins, available layouts, client counts, and history, but not "created by
  Netcatty". A Netcatty-created session can only be recognized by:
  - a name convention it controls at creation time (and tolerates user renames), or
  - its own side-car registry (name → uuid, revalidated by listing), or
  - a layout `env` entry / plugin configuration written at creation (not visible
    from the external listing).
  Distinguishing Netcatty-created from external sessions is therefore a **design
  gap**, not a backend feature.

### 3.7 Title and metadata

External (outside a session):

| Field | Availability |
|---|---|
| Session name | `list-sessions` |
| Live vs EXITED | `--no-formatting`/default text suffix; lost with `--short` |
| Created age | `[Created <duration> ago]` from socket/layout file mtime |
| Current marker | `(current)` if `ZELLIJ_SESSION_NAME` matches |
| Tab/pane metadata | Not in `list-sessions`; read cache files or query inside |

Cache files (internal, contract-versioned, not a stable public API):
`$ZELLIJ_CACHE_DIR/contract_version_<n>/session_info/<name>/session-metadata.kdl`
and `session-layout.kdl` (SRC: `zellij-utils/src/consts.rs`,
`zellij-server/src/background_jobs.rs`). They contain `SessionInfo` with `name`,
`tabs`, `panes`, `connected_clients`, `is_current_session`, `available_layouts`,
`plugins`, `web_clients_allowed`, `web_client_count`, `tab_history`,
`pane_history`, and `creation_time`. The serialized layout contains per-pane
`geom`, `run`, `cwd`, `title`, `is_focused`, `pane_contents`, colors, and global cwd.
`session-metadata.kdl` is also written for live sessions (background job), so it is
a possible external metadata source, but the directory name includes the client/
server contract version and the format can change between zellij versions.

Inside a session (structured, supported CLI):

- `zellij action list-panes --all --json` returns a vector of `PaneListEntry`:
  flattened `PaneInfo` (`id`, `is_plugin`, `is_focused`, `is_fullscreen`,
  `is_floating`, `is_suppressed`, `title`, `exited`, `exit_status`, `is_held`,
  geometry, cursor, `terminal_command`, `plugin_url`, `is_selectable`, colors) plus
  `tab_id`, `tab_position`, `tab_name`, and optional `pane_command`, `pane_cwd`
  (SRC: `zellij-utils/src/data.rs`, `zellij-server/src/route.rs`). `--all` adds all
  fields; `--json` prints pretty JSON.
- `zellij action list-tabs --all --json` returns `TabInfo` with position, name,
  active, hidden-pane count, fullscreen/sync/floating flags, viewport dimensions, and
  related fields.
- `zellij action list-clients` prints a fixed header
  `CLIENT_ID ZELLIJ_PANE_ID RUNNING_COMMAND` followed by one line per focused client
  (SRC: `session_layout_metadata.rs`, `ClientMetadata::render_many`). It reports
  which pane each client is focused on and the command, not a session creator.
- `zellij action query-tab-names` prints tab names only.
- Pane titles are settable via `rename-pane`/`--name` and are also driven by terminal
  title escapes; tab names via `rename-tab`/`--name`. There is no session-level
  title distinct from the session name.

### 3.8 Platform notes

- Linux/macOS: Unix sockets in the runtime directory (or temp) under
  `zellij-<uid>/contract_version_<n>`; `ZELLIJ_SOCKET_DIR` overrides the socket
  directory. Directory permissions are set to 0700; the socket to 01700
  (SRC: `zellij-client/src/lib.rs`, `cli_client.rs`, `zellij-server/src/lib.rs`).
- Native Windows (0.44.0+): no Unix sockets; a marker file containing the server PID
  and a dual-pipe architecture (main pipe client→server, reply pipe server→client).
  `%TEMP%\zellij` and the same contract-version directory naming are used. Windows
  named-pipe ACL details were not established here: **UNVERIFIED**.
- WSL: Unix build, Unix sockets; behaves as Linux.
- Remote Windows: Windows build; path and pipe handling differ; `ZELLIJ_SOCKET_DIR`
  is still the override point.
- Version floor: 0.44.0 for native Windows and `action list-panes`; 0.45.1 is the
  target. `list-tabs`/`--json` are present in 0.45.1; their introduction version is
  **UNVERIFIED** (likely the same 0.44.0 CLI-metadata work, not confirmed).

### 3.9 Security

- Unix socket directory 0700 and socket 01700 restrict access to the owning user
  (SRC as above). There is no application-level authentication beyond filesystem
  permissions. On Windows the corresponding pipe ACL was not verified.
- Environment redirection:
  - `ZELLIJ_SOCKET_DIR` selects the socket directory. Inheriting it can point
    Netcatty at a different zellij instance.
  - `ZELLIJ_SESSION_NAME` and `ZELLIJ` make commands target/resolve the current
    session and trigger nested-session behaviour. Scrub `ZELLIJ*` from the child
    environment (at minimum `ZELLIJ`, `ZELLIJ_SESSION_NAME`, `ZELLIJ_SOCKET_DIR`;
    config variables such as `ZELLIJ_CONFIG_FILE`/`ZELLIJ_CONFIG_DIR` also exist and
    are better removed with a prefix scrub).
- Injection surface:
  - Session names are not restricted to a safe alphabet; spaces, newlines, and
    control characters can be embedded. Never interpolate a session name into a
    shell command or a line protocol that Netcatty parses without framing. Spawn
    `zellij` with argv and validate names created by Netcatty to `[A-Za-z0-9._-]+`.
  - `zellij action` positional arguments are parsed by clap; use the `--session`
    selector and separate argv elements rather than one concatenated string.
  - Clipboard: the default clipboard provider is **OSC 52** (`ClipboardProvider::
    Osc52`), so copy actions in zellij emit OSC 52 to the outer terminal; Netcatty
    must either honour or safely ignore it (SRC: `zellij-server/src/tab/clipboard.rs`,
    `zellij-utils/src/input/options.rs`).
  - **DCS 26661**: the CLI client unconditionally writes a nested-session
    announcement to stdout on startup (`NestedSessionMessage::Announce`), and a
    reannounce scheduler writes more frames. The frame is
    `ESC P 26661 n <base64 payload> ESC \` (SRC:
    `zellij-utils/src/nested_session.rs`, `zellij-client/src/lib.rs`). Netcatty's Pane
    terminal must ignore unknown DCS sequences (xterm.js normally does) or strip
    them; treating all output as plain text will expose them to search/selection.
  - The `zellij subscribe` JSON stream is pane-render data; it is not a lifecycle
    feed and should not be used as one.

---

## 4. herdr-core

This section restates the lifecycle-relevant parts of the existing Netcatty herdr
protocol research, re-checked against the upstream repository. The protocol is
JSON-RPC 2.0, generation 1, over `hc connect` stdio; after `hello` with
`{"framed":true}` the connection uses binary frames with per-channel credit.

### 4.1 Versions and platforms

- Latest release tag checked on 2026-11-08: **v0.1.18**
  (`3965a46caae095a3ead01de237fa13955e52b8c6`). The Netcatty adapter's stated floor
  is **0.1.11**, target **0.1.18**, protocol generation **1**, with the `hello`,
  `terminal.list`, `terminal.run`, `terminal.attach`, `framed`, and `attach`
  capabilities.
- `hello` reports `version`, `generation`, `methods` (32 names), and `features`.
  Require generation 1 and the method/feature set; do not parse only a version
  string.
- Platforms: Linux, macOS, and native Windows. The core owns the PTY (ConPTY on
  Windows); Netcatty only speaks the RPC protocol. Remote use is over SSH; the core
  runs on the remote machine (Unix or Windows).
- Discovery is by executable probe (`--version`), not by a list command. Starting a
  core is a side effect of `hc connect`: there is no "do not autostart" switch.
  `hc --version` and similar non-connection commands do not start a core.

### 4.2 Discovery and listing

```json
{"jsonrpc":"2.0","id":1,"method":"hello","params":{"framed":true}}
{"jsonrpc":"2.0","id":2,"method":"terminal.list","params":{"all":true,"labels":{}}}
{"jsonrpc":"2.0","id":3,"method":"terminal.get","params":{"terminal":"t7"}}
```

- `terminal.list` returns `{"terminals":[Terminal...], "hidden_finished":N,
  "machines":[...]}`. With `all:false` (default) finished terminals are omitted and
  counted in `hidden_finished`; with `all:true` they are included with
  `state:"finished"`.
- `Terminal` required fields: `address`, `labels`, `argv`, `cwd`, `state`
  (`"live"`/`"finished"`), `created_ms`, `screen` (`"exact"`/`"inexact"`), `cols`,
  `rows`. Optional: `name`, `pid`, `exit_code`, `signal`, `reason`, `finished_ms`,
  `status`, `status_ms`, `records`, `reported`, `turn`, `title`, `parent`, `notify`,
  `die_with_parent`, `rm`, `keep`, `identity`, `template`, `first_message`,
  `submit`, `stop`, `paste_gap_ms`, `foreground`.
- States and exit codes:
  - There is no "no server" state visible to a client: `hc connect` starts the core
    if needed. A missing/unavailable core surfaces as a process/probe failure
    (`unavailable`, CLI exit 5) rather than as an empty list.
  - "No terminals" is simply an empty `terminals` array.
  - "Only finished terminals" is visible with `all:true` (`state:"finished"`), or
    inferred from `hidden_finished` with `all:false`.
  - Error kinds relevant to listing/attach: `not_found`, `finished`, `unavailable`,
    `needs_framed`, `invalid_params`, `machine_unknown`, `refused` (undocumented in
    the attach schema but returned by the code), `spawn_failed`, `unsupported_generation`.
    The `hc` CLI maps some to exit codes (unavailable 5, invalid_params 6,
    machine_unknown 4, usage 2, others 1); Go code should primarily use the JSON-RPC
    error kind.
- Side effects: `terminal.list` itself is read-only, but establishing the connection
  may leave a long-running `herdr-core serve` process on the machine. `terminal.list`
  with machines added can contact each peer (3 s budget per machine) and may return
  `machine/tN` addresses; Netcatty's v1 filters these out.
- Cost: one short-lived framed connection is enough for a list; no polling is needed
  if the event stream is used. There is no hang risk beyond the client's own RPC
  timeout (Netcatty uses a 15 s list timeout).

### 4.3 Live changes

`events.subscribe` returns a snapshot and a channel:

```json
{"jsonrpc":"2.0","id":4,"method":"events.subscribe","params":{"terminal":null,"children_of":null,"labels":{}}}
```

```json
{"seq":42,"terminals":[ ...Terminal... ],"channel":4}
```

- The snapshot and subscription are registered under one lock, so events between
  them are not lost. Framed event lines on the returned channel:
  `{"jsonrpc":"2.0","method":"event","params":{"seq":42,"type":"status","terminal":"t7","data":{...}}}`,
  plus `heartbeat` (`{seq}` every 15 s when the queue is empty) and `missed`
  (`{seq, terminals}` full snapshot when the subscriber falls behind).
- Event types and application: `created`, `removed`, `lost`, `restarted`, `keep`,
  `finished`, `status`, `turn`, `title`, `labels`, `size`, `foreground`, `doc`.
  `labels` events may carry the full `record` when a terminal newly matches the
  filter.
- Resync: `seq` is per core process and resets on core restart. On reconnect,
  re-subscribe and take the new snapshot; do not reuse old seq values. `missed`
  replaces the terminal set. This is the most complete resync model of the three
  backends.
- Event flow control: initial credit 256 KiB, queue up to 4 MiB before a `missed`
  snapshot; a client must keep consuming and returning credit. Heartbeat is not a
  strict liveness timeout.

### 4.4 Create, attach, detach, resize

```json
{"jsonrpc":"2.0","id":5,"method":"terminal.run","params":{
  "size":{"cols":120,"rows":40},
  "name":"nc-4f7a",
  "line":"exec bash",
  "cwd":"/home/alice",
  "labels":{"nc_owner":"netcatty","nc_uuid":"..."},
  "keep":true
}}
```

- `terminal.run` creates the terminal and its PTY in the core; no local PTY exists on
  the Netcatty side. `size` is the initial size. If omitted, the core's headless
  default is 200x120 (not suitable for a Pane). Name rules: 1–32 characters from
  `a-z0-9-_.`, unique among live terminals. `argv` and `line` are alternatives; if
  neither is given, the machine's shell is used. `line` is executed by the machine's
  shell (`sh -c` on Unix, `cmd /d /s /c` on Windows), so it is a code-execution
  surface by design. `labels`, `cwd`, `env`, `parent`, `rm`, `keep`, `wait`, and
  `outlive_parent` are available.
- Spawn timeout: 10 s (`HC_SPAWN_MS`); on timeout `spawn_failed`. With `keep:true`
  the record may remain live with `reason:"spawn_pending"`; with `keep:false` it is
  removed. The error does not return the terminal address, so a client must find it
  by name/labels or list.
- `terminal.attach` requires framed mode and required `cols`/`rows`; `observe:true`
  avoids taking size ownership. Attach is not read-only: it adds a viewer and may
  resize the PTY (RESIZE to the holder) before returning the first frame. The first
  frame is a VT snapshot (`CAN` + full paint + mode/title records), then DATA frames;
  input is DATA; detach is CLOSE on the stream. Sending CLOSE does not kill the
  terminal; closing `hc` does not either. Multiple viewers are allowed and share one
  grid; the most recently active viewer becomes the size leader. A non-leader may
  show a temporary size mismatch until the next repaint.
- Resize after creation is done through an attach stream RESIZE, not a separate
  `terminal.resize` method (there is none in the 32-method list). Creating with the
  measured grid is therefore important; a later size correction requires an attached
  viewer.

### 4.5 Kill, delete, rename

```json
{"jsonrpc":"2.0","id":6,"method":"terminal.kill","params":{"terminal":"t7"}}
{"jsonrpc":"2.0","id":7,"method":"terminal.rm","params":{"terminal":"t7"}}
{"jsonrpc":"2.0","id":8,"method":"terminal.label","params":{"terminal":"t7","labels":{...}}}
```

- `terminal.kill` sends HUP, then TERM after 500 ms, then KILL after another 500 ms,
  waiting up to 2000 ms. It returns the Terminal record, which may still be `live`;
  with `rm:true` and no waiting parent the record may already be removed, so the
  reply can be `not_found`. Call timeout must cover about 3 s. It is not idempotent
  in the sense that a second call on a finished/removed terminal returns
  `not_found`/`finished`.
- Attached viewers see the stream close (CLOSE payload carries the reason) and the
  event stream sees `finished`/`removed`/`lost` transitions.
- `terminal.rm` appears in the method table but its exact params/result and whether
  it can remove a live terminal are **UNVERIFIED**. It is the closest analogue to
  "delete the record" and is distinct from `kill` (which ends the program).
- `terminal.label` appears in the method table; it updates labels (the `labels` event
  exists). Exact request/result shape is **UNVERIFIED**; labels are clearly the
  ownership/renaming-adjacent mechanism.
- There is no `terminal.rename`/session rename. The run-time `name` is fixed; the
  mutable display metadata is `title` (driven by the program), `labels`, and
  `status`/`turn`.

### 4.6 Identity and ownership

- Addresses are per-core (`tN`, or `machine/tN` for a peer). The core has a stable
  random `core.id` and a hostname-derived `core.name` reported by `hello`; the core
  process restart may reset address allocation. Address stability across core
  restarts is **UNVERIFIED**; Netcatty's restore path assumes the saved address still
  resolves or fails with "choose another terminal".
- `name` is optional and unique among live terminals; `labels` is a free-form
  string map. `terminal.list` and `events.subscribe` accept a `labels` filter with
  all-pairs matching. This is the best ownership mechanism of the three backends:
  create with `{"nc_owner":"netcatty","nc_uuid":"..."}` and filter on it.
- Any process running as the same OS user (or with access to the core socket/pipe)
  can see and mutate labels/terminals. Labels are not a security boundary.
- Multiple viewers are explicitly supported; there is no exclusive client ownership.

### 4.7 Title and metadata

- Per terminal: address, name, labels, argv, cwd, state, created_ms, screen, cols,
  rows, pid, exit_code, signal, reason, finished_ms, status, status_ms, records,
  reported, turn, title, parent, notify, die_with_parent, rm, keep, identity,
  template, first_message, submit, stop, paste_gap_ms, foreground
  (`{pid,name,argv,cwd}`).
- `title` is updated by the `title` event (terminal title escapes), `foreground` by
  the `foreground` event, `status`/`turn` by status/turn events, and `size` by
  resize. There is no window/pane count: one herdr terminal is one PTY.
- Creation time is `created_ms` (epoch ms). Finished reason/exit code/signal are
  available after `finished`.

### 4.8 Platform notes

- Local Unix: the core listens on a Unix socket under a directory restricted to the
  user. Local Windows: named pipes with an ACL for the user and SYSTEM. These are
  documented in Netcatty's herdr design notes and match the upstream trust model;
  the exact upstream paths/ACL implementation were not re-read for this report.
- Remote: the core is reached through the SSH account; there is no additional
  authentication or encryption. On a remote Windows host, the fixed probe path and
  PowerShell `DefaultShell` behaviour are the known risk areas (the probe may exit 1
  instead of 127/126).
- Version floor: 0.1.11 for the protocol generation/features Netcatty needs; 0.1.18
  is the tested target. Native Windows releases exist for x86_64 and were exercised
  by Netcatty's opt-in lifecycle tests (per the herdr design notes).

### 4.9 Security

- No application-layer authentication. Trust is the OS user (local) or the SSH
  account (remote). Unix socket directory is user-only; Windows pipe ACL is
  user+SYSTEM (DOC).
- Environment redirection: the adapter starts `hc` with `HC_SESSION`, `HC_SOCKET`,
  `HC_SSH`, and `HERDR_CORE_DIRS` removed, so an inherited value cannot point the
  child at a different core. Netcatty always uses the default session and never
  passes a session name. `hc connect` has no no-autostart flag, so probing a target
  may start a core.
- Injection surface:
  - All payloads are JSON-RPC values. `line` is executed by the target shell; use
    `argv` for structured commands and never pass untrusted text as `line`.
  - `name` is charset-restricted. `labels` are free-form but only metadata; they may
    appear in logs/diagnostics.
  - Remote `hc` paths are fixed templates / validated absolute positional arguments,
    never interpolated into a shell command.

---

## 5. Consequences for one backend session per Netcatty Pane

1. **State models differ and cannot be folded into one tri-state without per-backend
   logic.**
   - tmux: server absent (exit 1) is a distinct state; there are no dead sessions.
   - zellij: exit 1 conflates "no server", "no sessions", and "only EXITED"; the
     EXITED set is only visible in the formatted text/cache.
   - herdr: the core is assumed reachable (connect starts it); emptiness and
     finished terminals are explicit in JSON.
2. **Only herdr has structured identity and ownership.** tmux needs a user option
   (`@nc_owner`, `@nc_uuid`) plus `$id`; zellij has no marker at all and needs a name
   convention or a side-car registry; herdr can use labels and label filters.
   None of these are security boundaries.
3. **Metadata deletion exists only for zellij and (probably) herdr.** tmux has
   nothing to delete; `kill-session` destroys the record. zellij `delete-session`
   removes both the EXITED record and resurrection files; herdr has `terminal.rm`
   (semantics unverified) separate from `terminal.kill`.
4. **Attach is whole-session for tmux and zellij; herdr is per-terminal.** A Pane
   can host a tmux session (with its internal windows/panes), a zellij session (with
   its tabs/panes), or one herdr terminal. Any "one Pane per pane" model must either
   keep the backend's internal panes inside one Pane or use backend commands to
   split, and only herdr's unit is already a single PTY.
5. **Only tmux can observe without a PTY.** `tmux -C attach-session` is a control
   client with no TUI and no TTY, but it counts as attached and can keep a session
   alive. zellij and herdr both require a PTY for their interactive attach; herdr's
   event stream plus `terminal.list` can observe without attaching, and zellij has
   no equivalent external lifecycle feed.
6. **Event strategy is backend-specific.** tmux control mode (push) or polling;
   zellij polling only; herdr `events.subscribe` (push, with seq/missed) and
   reconnect resubscribe. A common interface should expose a "resync" operation
   rather than assuming event replay.
7. **Polling zellij is not free**: every list launches a process and probes every
   session socket without a timeout; stale sockets are deleted as a side effect.
   tmux polling is cheaper but still process-per-poll; herdr list can start a core.
8. **Creation sizes differ.** tmux `new-session -d` defaults to 80x24 unless
   `-x/-y`; zellij background creation uses 50x50 until a client attaches; herdr
   `terminal.run` uses the supplied `size` (and a headless 200x120 default if
   omitted). Every backend must be created/attached with the measured Pane grid.
9. **Zellij output is not plain text.** Unconditional DCS 26661 announcements and
   OSC 52 clipboard writes require a VT parser that ignores unknown DCS; Naive
   string handling will leak frames into search/selection.
10. **Pin the endpoint and scrub the environment for all three.** tmux: `-S`/`-L`,
    remove `TMUX`/`TMUX_PANE`/`TMUX_TMPDIR` influence. zellij: pin/scrub
    `ZELLIJ_SOCKET_DIR`, `ZELLIJ_SESSION_NAME`, `ZELLIJ`. herdr: remove the `HC_*`
    variables and `HERDR_CORE_DIRS`. Otherwise a Netcatty Pane can silently attach
    to the user's interactive session or another core.

---

## 6. UNVERIFIED items and open questions

- No backend binary was run; every sample output is `sample (derived)` from source.
- tmux: the `%exit` reason string after `kill-session`; the exact oldest tmux
  version supporting the full notification set; the behaviour of
  `destroy-unattached`/`exit-unattached` with a Netcatty control client (code says
  it counts as attached, runtime not tested); whether `session_path`/`pane_title`
  are populated in every platform build.
- tmux: exact rules for names containing `:`/`.`, tab, or newline when passed to
  `new-session` (only UTF-8 validity is checked in `check_name`; the display path
  escapes controls).
- zellij: multiple-client resize policy; `list-tabs`/`--json` introduction version;
  Windows named-pipe ACL; exact client exit status when its server is killed;
  external `action rename-session` selection/output; whether `--create` on a
  resurrectable name behaves as create or resurrect in all versions (source shows
  create path only consults live sockets, so it creates).
- zellij: the exact default `on_force_close` value in 0.45.1's shipped config (the
  source supports both detach and quit; the default config file was not read here).
- herdr: exact `terminal.rm` and `terminal.label` request/result shapes and whether
  `rm` can remove a live terminal; kill on an already-finished terminal; address
  stability across core restarts; upstream socket/pipe path details beyond the
  Netcatty design notes; `hc` CLI text output (not used by the adapter).
- herdr: whether `terminal.run` without `size` truly uses 200x120 on every platform
  (the type default and docs say headless 200x120; Windows ConPTY may differ).

---

## 7. Sources

tmux, tag 3.8 (commit `7f2a35ad3321f9ba57a1062ca73b1f3ff26aca53`):

- Repository: <https://github.com/tmux/tmux/tree/7f2a35ad3321f9ba57a1062ca73b1f3ff26aca53>
- `cmd-list-sessions.c`, `cmd-new-session.c`, `cmd-attach-session.c`,
  `cmd-detach-client.c`, `cmd-kill-session.c`, `cmd-kill-server.c`,
  `cmd-rename-session.c`, `cmd-list-clients.c`, `client.c`, `server-client.c`,
  `server.c`, `session.c`, `server-fn.c`, `resize.c`, `control.c`,
  `control-notify.c`, `options-table.c`, `tmux.c`, `tmux.1`, `CHANGES`, `README`.
- Manual page (repository `tmux.1`; also <https://man.openbsd.org/tmux.1>).

zellij, v0.45.1 (commit `efd8fd5a89a20c07a111d248ad7fce53848d2c18`):

- Repository: <https://github.com/zellij-org/zellij/tree/efd8fd5a89a20c07a111d248ad7fce53848d2c18>
- `zellij-utils/src/{sessions.rs,cli.rs,consts.rs,envs.rs,data.rs,shared.rs,session_serialization.rs,input/layout.rs,input/cli_assets.rs,nested_session.rs}`
- `zellij-client/src/{lib.rs,cli_client.rs,os_input_output.rs}`
- `zellij-server/src/{lib.rs,screen.rs,route.rs,pty.rs,background_jobs.rs,session_layout_metadata.rs,tab/clipboard.rs}`
- `src/{commands.rs,main.rs}`, `CHANGELOG.md`.
- Clipboard option: `zellij-utils/src/input/options.rs`.

herdr-core, v0.1.18 (release tag `3965a46caae095a3ead01de237fa13955e52b8c6`;
protocol files pinned at `c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a`):

- Repository: <https://github.com/herdrdev/herdr-core>
- Protocol: `schema/README.md`, `schema/hc.openrpc.json`,
  `crates/herdr-core/src/{api.rs,api/run.rs,api/types.rs,server.rs,events.rs,wire.rs,terminals.rs,runtime.rs,viewers.rs,rpc.rs}`.
- The preceding Netcatty protocol research (same pinned commit) covers the exact
  request/result shapes, frame/credit rules, first-paint format, error mapping, and
  event application table; this report only restates the lifecycle-relevant parts.

Netcatty context (not modified by this report):

- `.scratch/mux-sync/map.md` and `.scratch/mux-sync/issues/01-backend-session-lifecycle.md`
  (ticket and shared facts).
- Existing herdr design/verification notes consulted for the herdr v1 behaviour
  (0.1.11 floor, 0.1.18 target, detach-on-close, no kill in v1, env scrubbing).
