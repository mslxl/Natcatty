# DBX on a WASM-only plugin: capability and protocol feasibility

Status: research
Date: 2026-11-06
Wayfinder ticket: `plugin-system/01-dbx-wasm-feasibility`
Reference workload: [`t8y2/dbx`](https://github.com/t8y2/dbx) at commit [`2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8`](https://github.com/t8y2/dbx/tree/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8) (2026-11-05)
Runtime basis: wazero v1.12.0, WASI `wasi_snapshot_preview1`

Premises taken from the NATCATTY plugin-system map and tickets:

- Third-party runtime is WASM only, using wazero; no sidecar, native helper process, JVM, JDBC driver, in-process dynamic Go, or `plugin` build mode ([`.scratch/plugin-system/map.md`](../../.scratch/plugin-system/map.md), locked premises Q9).
- Host services v1 are `hosts`, `sessions`, `terminal`, `vault`, `files`, `settings`/`storage`, `notifications`/`events`, `ui`, and `systemPanel`; connection providers run on host-mediated network and credential proxies; the host owns host-key trust ([`map.md`](../../.scratch/plugin-system/map.md); ticket `07-host-api-services`).
- There is no host AI service; DBX's AI features must use the plugin `network` permission ([`map.md`](../../.scratch/plugin-system/map.md), Q12).
- Plugins may contribute `mcp.tools`; data connections expose `read-only`/`read-write`/`full` access modes ([ticket `15-mcp-tools-contribution`](../../.scratch/plugin-system/issues/15-mcp-tools-contribution.md); ticket `09-data-provider-dbx`).

This report inventories DBX's feature areas, classifies each against those premises, and lists which database protocols are implementable by guest code over a host `network.dial` versus which are structurally tied to a JVM, JDBC, a native driver, or an out-of-process agent.

## 1. Bottom line

1. **DBX's "100+ databases" is a process-distribution strategy, not a single-binary capability.** The driver manifest at the pinned commit contains **88 connection targets: 34 `native`, 2 `file`, 49 `agent`, and 3 `external`** ([`database-drivers.manifest.json`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json)). The 49 agent targets run in standalone child processes speaking JSON-RPC over stdin/stdout; the system downloads and manages a JRE 21 for the Java agents ([`agents/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md#L7), [`agent_manager.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-agent/src/agent_manager.rs#L18-L21)). Under the no-sidecar/no-JVM premise, that majority of DBX's coverage disappears by construction.

2. **A WASM-only plugin can reproduce DBX's SQL-client experience for a focused protocol set, not its full driver matrix.** The credible core is PostgreSQL, MySQL/MariaDB, local SQLite (with a file-service change), Redis, and ClickHouse; SQL Server is also protocol-feasible; MongoDB is feasible but expensive. Query editor, ordinary result grid, schema tree, textual/tree explain plans, schema diff, CSV/JSON/SQL import-export, HTTP-provider AI, and MCP tool contribution are all achievable. Oracle, DB2, JDBC profiles, DuckDB-engine parity, remote SQLite over SSH, CLI-based AI providers, a standalone MCP server, and most message-queue consoles are not.

3. **The plan already implies host-mediated networking, but the v1 service list does not yet specify it.** WASI preview1 has no outbound connect; wazero only pre-opens inbound listeners. A host import (the planned `network.dial`) is therefore mandatory ([WASI preview1 docs](https://github.com/WebAssembly/WASI/blob/snapshot-01/phases/snapshot/docs.md), [wazero `experimental/sock`](https://github.com/tetratelabs/wazero/blob/v1.12.0/experimental/sock/sock.go)). The findings force three additions/decisions: in-band TLS upgrade, an SSH-tunnel/transport service, and a random-access or pre-opened file surface for SQLite.

4. **In-guest TLS and existing DBX driver crates cannot be assumed to port.** Tokio's `net` and `process` features are unsupported on `wasm32-wasip1` without `tokio_unstable`, and even then sockets must come from `FromRawFd` ([tokio `lib.rs`](https://github.com/tokio-rs/tokio/blob/master/tokio/src/lib.rs#L460-L470)). DBX's driver crates depend on `tokio` `full` plus native OpenSSL or C SQLite/DuckDB, so they are not reusable as-is. Host-terminated TLS with an in-band upgrade operation is the pragmatic design; otherwise each guest driver must carry a WASM-compatible TLS stack, which is a research risk rather than a settled capability.

## 2. What DBX actually implements

### 2.1 Application and plugin architecture

DBX is Tauri 2 + Vue 3 + CodeMirror 6 on the frontend and Rust on the backend; the backend uses `sqlx`, `tiberius`, `redis-rs`, and the MongoDB Rust driver ([README Tech Stack](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L638-L647)).

Its own plugin system is **not** WASM:

- A `.dbxp` package is a ZIP containing a platform-specific native binary under `bin/<platform>/` plus an optional sandboxed web UI ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L15-L42)). The platform contract is "manifest v1 + Host API 1.x + sidecar protocol v1" ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L15)).
- The backend is "a persistent child process with stdin/stdout reserved for the DBX protocol"; diagnostics go to stderr ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md), sidecar protocol section). The Rust runtime spawns the child with `tokio::process` ([`plugin-runtime/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-plugin-runtime/Cargo.toml), [`plugins/runtime.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-plugin-runtime/src/plugins/runtime.rs#L294-L327)).
- The UI is an iframe with `sandbox="allow-scripts"`, restrictive CSP, no Tauri object, no parent DOM access, and no direct network access by default; declared HTTPS origins may be added to `connect-src` ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L355-L385)).
- The legacy JDBC plugin is explicitly a "legacy JDBC sidecar" that connects through vendor JDBC drivers and custom JDBC URLs ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L32)).

This matters for the research: DBX is a reference workload for the **UX and feature surface**, not an existence proof that its capabilities can be delivered under a WASM-only runtime. DBX solves connection breadth by spawning processes; NATCATTY explicitly forbids that solution.

### 2.2 Driver runtimes

The manifest's `runtimeMode` is the authoritative split ([`database-drivers.manifest.json`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json)):

| Mode | Count | Examples | What it means |
|---|---:|---|---|
| `native` | 34 | MySQL, PostgreSQL, Redis, ClickHouse, SQL Server, Elasticsearch, DynamoDB, Nacos, Consul, MQTT | Rust code in the DBX process |
| `file` | 2 | SQLite, DuckDB | Embedded engines; SQLite uses bundled native C, DuckDB uses a worker process |
| `agent` | 49 | Oracle, DB2, MongoDB (legacy profile), Snowflake, BigQuery, Cassandra, Kafka, ZooKeeper, etcd, RabbitMQ, RocketMQ | Standalone helper process, Java/JDBC, Go, or Rust |
| `external` | 3 | JDBC, plugin, PrestoSQL | Out-of-process or externally supplied |

The `agents/drivers` tree contains **54 agent source directories**: 37 Java-only Gradle modules, 1 mixed Java+Go module (`iotdb`), 14 Go-only modules, and 2 Rust modules (`duckdb`, `tdengine`) ([directory listing](https://github.com/t8y2/dbx/tree/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers)). The agents README states: "Each agent runs as a standalone process and communicates with DBX via stdin/stdout JSON-RPC 2.0"; "Most Java agents target JRE 21"; "DBX downloads and manages the JRE 21 installation automatically for Java agents" ([`agents/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md#L7), [`agents/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md#L71)). The base app's "no Java JRE" claim therefore describes the default install, not the full 100+ target matrix.

Notable per-driver evidence:

- **PostgreSQL**: `tokio-postgres` 0.7 with `deadpool-postgres`, plus vendored OpenSSL and rustls variants ([`dbx-driver-postgres/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-postgres/Cargo.toml)).
- **MySQL/MariaDB**: `mysql_async` 0.37 with rustls and `client_ed25519` ([`dbx-driver-mysql/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mysql/Cargo.toml)).
- **Redis**: `redis` 1.7.1 with tokio, TLS, sentinel, and cluster features ([`dbx-driver-redis/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-redis/Cargo.toml)).
- **MongoDB**: the `mongodb` Rust driver 3.2.5 is present as a native crate, and the core uses `PoolKind::MongoDb` with `mongo_driver::connect`; the manifest additionally lists a legacy agent profile ([`dbx-driver-mongodb/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/Cargo.toml), [`mongo_driver.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/src/mongo_driver.rs#L71-L116)).
- **ClickHouse**: the native driver is HTTP (`reqwest`, default port 8123), not the native TCP protocol ([`clickhouse_driver.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/clickhouse_driver.rs#L3-L67)).
- **SQL Server**: `tiberius` 0.12.3 (TDS 7.3, rustls) ([`dbx-driver-sqlserver/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-sqlserver/Cargo.toml)).
- **SQLite**: local connections use `rusqlite` with the `bundled` feature (native SQLite C); remote-file SQLite uses a separate worker process that is uploaded and executed over SSH ([`dbx-drivers/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/Cargo.toml), [`dbx-sqlite-worker/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-sqlite-worker/Cargo.toml), [`sqlite_worker.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/sqlite_worker.rs#L536-L546)).
- **DuckDB**: `duckdb-sidecar` feature launches a separate worker process ([`duckdb_worker_process.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/duckdb_worker_process.rs#L1-L27), [`dbx-drivers/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/Cargo.toml)).
- **Oracle**: agent only: `go-ora` thin and `godror` thick (OCI) in the Go agent, plus JDBC profiles ([`agents/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md#L30-L31), [`oracle-go/go.mod`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/oracle-go/go.mod), [`oracle_oci.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-agent/src/oracle_oci.rs#L1-L12)).
- **DB2**: a Java agent extending `AbstractJdbcAgent` with driver class `com.ibm.db2.jcc.DB2Driver` ([`Db2Agent.java`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/db2/src/main/java/com/dbx/agent/db2/Db2Agent.java#L1-L45)).

### 2.3 AI, MCP, and SSH

- **AI**: DBX supports HTTP model providers (Claude, OpenAI, Ollama, any OpenAI-compatible endpoint) and CLI agents. The CLI path spawns a local executable with `std::process::Stdio` ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L279-L281), [`ai_claude_code_cli.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-ai-provider/src/ai_claude_code_cli.rs#L11-L61)).
- **MCP**: DBX ships a separate Rust MCP executable, distributed independently, with its own `main.rs` and stdio/HTTP transports ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L333-L340), [`dbx-mcp/main.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-mcp/src/main.rs#L1-L60)).
- **SSH tunnels**: implemented in-process with `russh`, with explicit host-key verification and known-hosts persistence ([`ssh_tunnel.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/ssh_tunnel.rs#L11-L20), [`ssh_tunnel.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/ssh_tunnel.rs#L77-L125)).
- **Plugins**: connection providers own validation and the connect/disconnect lifecycle in a **native sidecar**; the plugin UI is sandboxed and cannot call the network directly ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L178-L205), [`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L355-L385)).

## 3. WASM runtime reality

### 3.1 No outbound sockets in WASI preview1

WASI preview1 defines `sock_recv`, `sock_send`, and `sock_shutdown`; it does not define an outbound `sock_open`, `sock_connect`, or `sock_bind` ([WASI snapshot-01 docs](https://github.com/WebAssembly/WASI/blob/snapshot-01/phases/snapshot/docs.md)). wazero's stable WASI support likewise lists only receive/send/shutdown (plus `sock_accept` added after the tag) and no connect ([wazero specs](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/specs.md#wasi)). wazero's experimental socket API only lets the host pre-open **TCP listeners** (`WithTCPListener`), not dial outbound ([`experimental/sock/sock.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/experimental/sock/sock.go#L11-L36)).

Consequence: a WASM plugin cannot open a database socket by itself. Every database, HTTP, AI, MCP, and SSH connection must go through a host import. The ticket's premise ("host-mediated network") is not an optimization; it is the only possible design.

### 3.2 No stable threads, atomics, or shared memory

The WebAssembly core abstraction has no true parallelism. Wazero's stable `CoreFeaturesV2` set is mutable-global, bulk-memory, multi-value, non-trapping float-to-int, reference types, sign-extension, and SIMD; threads/atomics/shared memory are not in it ([`api/features.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/api/features.go#L16-L36)). Threads are an **experimental** feature flag (`experimental.CoreFeaturesThreads`), and atomics are guest-only until the host exposes them ([`experimental/features.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/experimental/features.go#L5-L18)). Wazero's language guide puts it plainly: "WebAssembly does not yet support true parallelism; it lacks support for multiple threads, atomics, and memory barriers" ([wazero language constraints](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/_index.md#L99-L103)).

Consequence: DBX-style pools, parallel schema fetches, concurrent import/export, background heartbeats, and blocking socket reads cannot use guest threads. The plugin runtime must be single-threaded and cooperative, with the host driving I/O, chunked row delivery, and cancellation. Any design that assumes a thread-per-connection or a normal async executor is invalid.

### 3.3 No WASI preview2 or component model

Wazero targets WASI preview1. Issues asking for preview2/component-model support were closed not planned; maintainers stated the project will not support the component model until it is a W3C recommendation ([wazero #2289](https://github.com/tetratelabs/wazero/issues/2289), [wazero #2200](https://github.com/tetratelabs/wazero/issues/2200)). A WASM plugin therefore cannot rely on preview2 sockets, streams, or component-model imports. The ABI must be a wazero host-module ABI (host functions plus linear memory), not WIT/world definitions.

### 3.4 Host ABI and resource limits

Wazero provides the primitives the plan needs, but they must be designed into the ABI:

- Host functions are registered through `HostModuleBuilder`/`NewFunctionBuilder` and called synchronously through WASM linear memory ([`builder.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/builder.go#L15-L167)).
- Memory is bounded per instance with `WithMemoryLimitPages` and optionally eagerly allocated with `WithMemoryCapacityFromMax` ([`config.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/config.go#L55-L78)).
- Long-running guest code can be interrupted by context cancellation or module close when `WithCloseOnContextDone(true)` is enabled ([`config.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/config.go#L149-L170)).

Consequence: row streaming, query cancellation, and per-plugin memory budgets are implementable, but they are ABI work, not free runtime features. There is no preemptive guest thread to kill; cancellation is cooperative at host-call boundaries or requires closing the module.

### 3.5 Language and library constraints

- Rust with `wasm32-wasip1`, TinyGo, Zig, and C via `wasi-libc` are the supported practical targets ([wazero specs](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/specs.md), [Rust guide](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/rust.md), [TinyGo guide](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/tinygo.md), [Zig guide](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/zig.md)).
- TinyGo's `encoding/json` compiles but panics at runtime because of limited reflection; other serializers are affected ([TinyGo guide](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/tinygo.md#L67-L80)). That is a serious constraint for database metadata and JSON-based protocols; Rust is the safer substrate for binary drivers.
- Tokio's WASM profile allows only `sync`, `macros`, `io-util`, `rt`, and `time` by default; `net`, `process`, `fs`, `io-std`, `signal`, and `rt-multi-thread` are gated or rejected, and on `wasm32-wasip1` sockets must be created via `FromRawFd` under `tokio_unstable` ([tokio `lib.rs`](https://github.com/tokio-rs/tokio/blob/master/tokio/src/lib.rs#L460-L470), [tokio `lib.rs`](https://github.com/tokio-rs/tokio/blob/master/tokio/src/lib.rs#L499-L517)). DBX's driver crates use `tokio` with `full` and network/process features, so they cannot be compiled into the guest unchanged.
- Rustls is platform-independent in principle but delegates cryptography to a provider; both `aws-lc-rs` and `ring` have platform/architecture constraints, and WASI is not a documented first-class target ([rustls README](https://github.com/rustls/rustls/blob/main/README.md#L47-L82)). In-guest TLS is therefore a risk, not a baseline. Host-terminated TLS is the pragmatic choice.

### 3.6 What this means for "driver porting"

The unit of work is **not** "compile the DBX driver crate to WASM." The work is "implement or port the wire protocol against the host network ABI." That is a substantial difference:

| Approach | Reuses DBX code? | Under WASM-only? |
|---|---|---|
| Compile `tokio-postgres` / `mysql_async` / `redis` / `mongodb` / `tiberius` as-is | No, tokio `net`/`process` unsupported | No |
| Rewrite the protocol against host `network.dial` + host TLS | Protocol semantics only | Yes, but per-driver effort |
| Put the driver behind a host "query" service | Driver runs in host, not plugin | Contradicts plugin-owned provider model |
| Use a WASI socket preopen | No outbound connect in WASI p1 | No |

The plan's model — plugin owns the data provider, host mediates network and audit — implies the second row. That is feasible for the protocols in Section 5, but the reference scope should be chosen accordingly.

## 4. Feature-area classification

Classification key:

- **Feasible**: achievable with the planned host services (`network` permission/host dial, `files`, `vault`, `settings`, `ui`, `events`, `mcp.tools`) plus guest code.
- **Needs new service**: the plan lacks a host capability the feature structurally requires.
- **Impossible**: conflicts with the WASM-only/no-helper/no-JVM premises, or requires an out-of-process native engine.

| DBX feature area | DBX evidence | Classification | Why / required capability |
|---|---|---|---|
| **Query editor** | CodeMirror 6, SQL highlighting, metadata autocomplete, execution, formatting, diagnostics, history ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L275-L277)) | **Feasible** | Native MyGo editor already supports SQL, themes, completion, diagnostics, and diff (NATCATTY UI research, commit `9325d1762`, `docs/research/plugin-mygo-ui-native-webview.md`). Guest runs SQL parsing/diagnostics; execution goes through the data-provider contract. |
| **Data grid** | Virtual scrolling, inline edit, filters, sorting, pagination, CSV/JSON/Markdown/XLSX/INSERT export ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L283-L285)) | **Feasible** (with UI widget gap) | Ordinary grids are native today; very wide/spreadsheet grids need column virtualisation, typed editors, and matrix clipboard, or a WebView grid ([ticket `14-rich-widgets-language-services`](../../.scratch/plugin-system/issues/14-rich-widgets-language-services.md); NATCATTY UI research (commit `9325d1762`, `docs/research/plugin-mygo-ui-native-webview.md`)). Row streaming/backpressure is an ABI requirement. |
| **Schema tools** (browser, object browser, structure editor, diff, search, lineage) | Schema browser, object browser, DDL edits, schema diff, field lineage, object search ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L287-L296)) | **Feasible for Tier-1 protocols** | All are metadata queries plus guest-side SQL/DDL analysis. Coverage shrinks to the protocols in Section 5; Oracle/DB2 schema tooling is not available. |
| **ER diagrams** | Visualise table relationships ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L291)) | **Feasible** | Metadata comes from the same schema queries. Rendering needs a native graph/diagram canvas or WebView; neither is a new host service ([ticket `14`](../../.scratch/plugin-system/issues/14-rich-widgets-language-services.md); NATCATTY UI research (commit `9325d1762`, `docs/research/plugin-mygo-ui-native-webview.md`)). |
| **Explain plans** | Visual query execution plan ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L292)) | **Feasible** (text/tree); graph needs widget/WebView | `EXPLAIN`/plan commands are per-protocol; text/tree/table rendering is native. A plan graph uses the same canvas gap as ER diagrams. |
| **Import/export** | CSV/Excel import, cross-DB transfer, full dump, data compare, SQL file execution, Parquet/CSV/JSON preview via DuckDB, DBeaver/Navicat import ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L298-L306)) | **Feasible** with `files`; **Parquet/DuckDB preview needs new service** | CSV/JSON/Markdown/INSERT generation and DB-to-DB transfer are guest work over data-provider streams plus `files` read/write. XLSX needs a WASM-compatible writer or WebView. DBX's Parquet preview is explicitly "powered by DuckDB" — that engine is a sidecar in DBX and has no host service in the plan. Connection-profile import needs the `hosts` service to allow create/import, which ticket 07 does not yet state. |
| **SSH tunnels** | Key/password SSH tunnel, proxy settings, auto-reconnect ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L325-L327)) | **Needs new host service** | DBX implements SSH in-process with `russh` and owns host-key verification ([`ssh_tunnel.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/ssh_tunnel.rs#L11-L20)). The plan says the host owns host-key trust and plugins cannot own it ([ticket `08`](../../.scratch/plugin-system/issues/08-connection-provider-terminal.md)), but v1 has no SSH/tunnel service. Add `network.dial` chaining through a Host's SSH transport, or a dedicated tunnel service. |
| **AI assistant** | Natural-language SQL, explain/optimise/fix, Claude/OpenAI/Ollama/OpenAI-compatible ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L279-L281)) | **Feasible for HTTP providers; CLI providers impossible** | The map already says there is no host AI service and plugins use `network` ([`map.md`](../../.scratch/plugin-system/map.md)). HTTP/SSE to model APIs, with keys from `vault`, is feasible. DBX's Claude Code/Codex/Cursor-style providers spawn local executables ([`ai_claude_code_cli.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-ai-provider/src/ai_claude_code_cli.rs#L11-L61)); that is a native helper and is forbidden. |
| **MCP** | Separate Rust MCP server; list connections, browse tables, execute SQL, open tables; plugin tools exposed via `mcp/tools` ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L333-L360), [`dbx-mcp/main.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-mcp/src/main.rs#L1-L60)) | **Feasible via `mcp.tools` contribution; standalone server impossible** | NATCATTY already plans plugin-contributed tools ([ticket `15`](../../.scratch/plugin-system/issues/15-mcp-tools-contribution.md)). The plugin cannot ship/run DBX's separate MCP binary. Tool registration, scopes, access modes, and audit stay host-owned. |
| **Redis browser** | Key search, batch ops, command runner, TTL, all data types ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L308-L311)) | **Feasible** | RESP2/RESP3 over host TCP + TLS. Simplest real protocol after HTTP; good first driver. |
| **MongoDB browser** | Document CRUD with pagination; Atlas and replica-set URLs ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L310-L311)) | **Feasible but high effort; SRV/DNS support needed** | BSON + OP_MSG + SCRAM + TLS + replica-set discovery. DBX has a native Rust driver and a legacy agent ([`mongo_driver.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/src/mongo_driver.rs#L71-L116)). Atlas URLs require SRV DNS lookups; a host dialer that only resolves A/AAAA is insufficient. |
| **Middleware consoles** | Kafka/RocketMQ/RabbitMQ/Pulsar, MQTT, Nacos/Consul/ZooKeeper/etcd ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L313-L317)) | **Mixed**: HTTP-based feasible; broker/session consoles need new streaming service or defer | Nacos/Consul are native HTTP drivers; MQTT is native. Kafka/RocketMQ/RabbitMQ are agent-backed profiles of the `mq` target, and ZooKeeper/etcd are agent-mode in the manifest; Pulsar is claimed in the README but is not a driver in the pinned manifest. These consoles need long-lived binary or gRPC sessions, watches, and push streams. A generic pub/sub streaming primitive is not in the v1 service list. |
| **Local SQLite** | `file` runtime; `rusqlite` bundled ([manifest](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json), [`dbx-drivers/Cargo.toml`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/Cargo.toml)) | **Needs new file capability** | SQLite is not a network protocol. `rusqlite` can target `wasm32-wasi` with the `wasm32-wasi-vfs` feature ([rusqlite `Cargo.toml`](https://github.com/rusqlite/rusqlite/blob/master/Cargo.toml#L103)), but that VFS expects pre-opened directories or an equivalent file ABI with seek/truncate/sync/locking. `files` currently promises "scoped read/write and streaming" ([ticket `07`](../../.scratch/plugin-system/issues/07-host-api-services.md)); random access and locking are not specified. |
| **Remote SQLite over SSH** | Worker uploaded and executed on the remote host ([`sqlite_worker.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/sqlite_worker.rs#L536-L546)) | **Impossible** | Requires shipping and executing a native helper on the remote machine. Forbidden by the no-native-helper premise. |
| **DuckDB / file preview** | `duckdb-sidecar` worker process; Parquet/CSV/JSON preview "powered by DuckDB" ([`duckdb_worker_process.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/duckdb_worker_process.rs#L1-L27), [README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L303)) | **Needs new host service / out of scope** | DBX runs DuckDB out of process. A guest DuckDB build is large and still needs a VFS; a host DuckDB engine service is outside the WASM-only premise. CSV/JSON preview can be done in-guest; Parquet needs a WASM parser or the host engine. |
| **JDBC profiles / driver store** | Legacy JDBC sidecar; optional JDBC plugin ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L32), [README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L325-L327)) | **Impossible** | Requires a JVM and vendor JDBC drivers. No WASM-only path. |
| **Scheduled backups** (source tree `dbx-core/src/scheduled_backup`) | Background scheduling and execution ([directory](https://github.com/t8y2/dbx/tree/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/src/scheduled_backup)) | **Needs new host service** | A plugin fiber only lives while the plugin/app is active; durable schedules and wake-ups need a host scheduler or background-job service, which is not in v1. Import/export can still be user-triggered. |
| **Connection import (DBeaver/Navicat)** | Import connection profiles ([README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md#L305)) | **Feasible if `hosts` allows create** | Parsing profiles is guest work; persisting them needs a create/import method on `hosts`. Ticket 07 lists metadata, provider fields, and connect/test, not create/import. |
| **PL/SQL debug, Oracle object management** | Oracle agent features ([`oracle-go`](https://github.com/t8y2/dbx/tree/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/oracle-go)) | **Impossible** | Oracle-only agent functionality; no WASM-only driver. |

## 5. Database protocol matrix

The question asks which protocols can be implemented over host `network.dial` in WASM and which require JDBC/JVM/native drivers. The answer must distinguish **protocol feasibility** from **DBX code reuse**: almost none of DBX's driver crates compile into a WASI guest, but several protocols are implementable from scratch or by porting the wire layer.

### 5.1 Feasible over host `network.dial`

| Database / family | DBX runtime and library | Transport and auth | WASM-only assessment |
|---|---|---|---|
| **PostgreSQL** (and wire-compatible: Redshift, GaussDB, openGauss, CockroachDB, etc.) | `native`; `tokio-postgres` 0.7 + deadpool ([Cargo](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-postgres/Cargo.toml)) | TCP; optional in-band TLS (`SSLRequest` → `S`/`N` → TLS → `StartupMessage`) ([PostgreSQL protocol flow](https://www.postgresql.org/docs/current/protocol-flow.html)); MD5/SCRAM-SHA-256 | **Yes.** Wire protocol is documented and stable. The tokio crate is not reusable; implement/port the v3 protocol over host dial and host TLS upgrade. Moderate-high effort. |
| **MySQL / MariaDB / TiDB / OceanBase / Doris / StarRocks** | `native`; `mysql_async` 0.37 ([Cargo](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mysql/Cargo.toml)) | TCP; optional in-band TLS after server `Handshake`, via `SSLRequest`, then `HandshakeResponse` ([MySQL connection phase](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_connection_phase.html)); `mysql_native_password`, `caching_sha2_password`, ed25519 | **Yes.** Same pattern as PostgreSQL: documented protocol, in-band TLS upgrade, auth plugins in guest. Moderate-high effort. |
| **Redis** | `native`; `redis` 1.7.1 ([Cargo](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-redis/Cargo.toml)) | TCP or TLS-from-start; RESP2/RESP3; AUTH/ACL; sentinel/cluster | **Yes.** Lowest-effort real driver. Long-lived connection and pub/sub still need host streaming/cancellation. |
| **ClickHouse** | `native`; HTTP via `reqwest`, default port 8123 ([`clickhouse_driver.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/clickhouse_driver.rs#L3-L67)) | HTTP/HTTPS; basic auth / query parameters | **Yes.** Use host HTTP or raw HTTP/1.1 over `network.dial`. TLS is from connection start, so no in-band upgrade needed. Lowest-effort SQL driver. |
| **SQL Server** | `native`; `tiberius` 0.12.3 (TDS 7.3) ([Cargo](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-sqlserver/Cargo.toml)) | TCP 1433; TDS `PRELOGIN` negotiates encryption and TLS follows ([MS-TDS PRELOGIN](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-tds/60f56408-0188-4cd5-8b90-25c6f2423868)); SQL auth; Windows integrated auth needs host Kerberos/SSPI | **Yes, with explicit host/port and SQL auth.** The prompt's example implying SQL Server needs JDBC is not true for DBX's native path: TDS is implementable over host TCP with an in-band TLS upgrade. Named-instance discovery needs UDP 1434, which v1 networking does not specify; require explicit port or add UDP. Integrated auth is a host-service question, not a driver-protocol one. High effort. |
| **MongoDB** | Native Rust `mongodb` 3.2.5 crate used by core; manifest also has a legacy agent profile ([Cargo](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/Cargo.toml), [`mongo_driver.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/src/mongo_driver.rs#L71-L116)) | TCP; TLS-from-start; BSON + OP_MSG; SCRAM; replica-set/SRV discovery | **Yes, but high effort.** The wire protocol is documented and DBX already has a native implementation, but the crate depends on tokio/net. Atlas SRV requires host DNS SRV resolution; otherwise accept explicit replica-set host lists. |
| **HTTP/JSON data services** (Elasticsearch, Easysearch, Meilisearch, Solr, CouchDB, Qdrant, Milvus, Weaviate, Chroma, InfluxDB, QuestDB, VictoriaMetrics, Cloudflare D1, RQLite, Turso, Salesforce, DynamoDB, Nacos, Consul) | `native`; `reqwest` or dedicated HTTP drivers | HTTP/HTTPS; JSON; various auth (API key, OAuth, SigV4) | **Yes.** These are the easiest targets: host HTTP/HTTPS plus JSON. DynamoDB needs SigV4 in guest; Salesforce needs OAuth. |
| **MQTT** | `native` ([manifest](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json)) | TCP/TLS; MQTT 3.1.1/5; long-lived subscribe/publish | **Yes**, but requires long-lived host streaming, keepalive, and cancellation. Good stretch target, not core reference. |

### 5.2 Not feasible / requires a JVM, JDBC, or native helper

| Database / family | DBX runtime and driver | Why it fails under WASM-only |
|---|---|---|
| **Oracle** | `agent`; Go agent with `go-ora` (thin) and `godror` (OCI thick), plus JDBC profiles ([`agents/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md#L30-L31), [`go.mod`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/oracle-go/go.mod)) | DBX requires a helper process; the thick profile requires the Oracle Client C library, and JDBC profiles require a JVM. The thin `go-ora` implementation proves a pure protocol implementation is theoretically possible, but there is no WASM port and TNS/OCI semantics are large. **Not feasible for the reference scope.** |
| **DB2** | `agent`; Java/JDBC agent (`com.ibm.db2.jcc.DB2Driver`) ([`Db2Agent.java`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/db2/src/main/java/com/dbx/agent/db2/Db2Agent.java#L1-L45)) | JDBC/JVM required. No credible WASM-only DRDA driver. **Impossible.** |
| **JDBC / custom JDBC URLs** | `external`; legacy JDBC sidecar ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L32)) | JVM + vendor drivers. **Impossible.** |
| **Snowflake, BigQuery, Databricks, SAP HANA, Teradata, Vertica, Firebird, Exasol, Informix, H2, Trino/Presto, Spark, Kylin, Spanner, Neo4j, Cassandra, etc.** | `agent` (Java/JDBC or Go) ([manifest](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json), [agents/README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md#L11-L40)) | DBX's implementation requires an agent/JVM/native helper. Some (Snowflake, BigQuery, Databricks, Spanner) expose HTTP APIs, so a subset could be reimplemented over host HTTP, but that is not DBX parity and is outside the recommended reference scope. **Out of scope.** |
| **Kafka, RabbitMQ, RocketMQ, Pulsar, ZooKeeper, etcd** | `agent` for Kafka/RabbitMQ/RocketMQ/ZooKeeper/etcd; MQTT is native; Pulsar is README-claimed but absent from the pinned manifest ([manifest](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json), [agents tree](https://github.com/t8y2/dbx/tree/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers)) | Not strictly impossible, but they are long-lived, stateful binary/gRPC protocols with consumer groups, watches, heartbeats, and push streams; DBX uses agent implementations. They need substantial guest protocol work plus a host streaming/session service the plan does not have. **Defer/out of reference scope.** |

### 5.3 SQLite is a special case

SQLite is not a client-server protocol and `network.dial` does not solve it:

- **Local SQLite**: needs an embedded SQLite engine and a file VFS. `rusqlite` has a `wasm32-wasi-vfs` feature ([rusqlite `Cargo.toml`](https://github.com/rusqlite/rusqlite/blob/master/Cargo.toml#L103)), but the host must provide pre-opened scoped directories or an equivalent range-read/write/lock file API. This is **feasible with a file-service extension**, not feasible with the current "scoped read/write and streaming" wording alone.
- **Remote SQLite over SSH**: DBX uploads a worker binary and runs `exec <remote_path>` over SSH ([`sqlite_worker.rs`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/sqlite_worker.rs#L536-L546)). That is a native helper executable on the remote host. **Impossible under the premise.**

### 5.4 Direct answer to the protocol question

- Implementable over host `network.dial` in WASM: **PostgreSQL, MySQL/MariaDB, Redis, ClickHouse, SQL Server (TDS, explicit port), MongoDB (high effort), the HTTP/JSON family, and MQTT (with streaming)**.
- Embedded/file, not `network.dial`: **local SQLite** (needs an embedded engine plus a capable file service); **DuckDB** (needs a host engine or an out-of-scope WASM build).
- Requires JDBC/JVM/native driver: **Oracle, DB2, JDBC profiles**, and DBX's 49 agent-mode databases as DBX implements them. SQL Server does **not** require JDBC in DBX; its native `tiberius` path is protocol-feasible.

## 6. Recommended DBX reference scope

The reference workload should be a **DBX-class data plugin**, not a DBX clone. Its purpose is to stress the plugin model's hardest plausible requirements without importing forbidden helpers.

### Tier 0 — must-pass reference scope

- **Drivers:** PostgreSQL, MySQL/MariaDB, local SQLite, Redis.
- **UX:** SQL query editor, ordinary result grid, schema/object browser, textual/tree explain plans, CSV/JSON/SQL import-export, query history, saved snippets.
- **Host capabilities exercised:** `network.dial` (+ TLS), data-provider contract, streaming rows, cancellation, `files`, `vault`, `settings`/`storage`, native `ui`.
- **Why:** Exercises SQL, KV, embedded-file, TLS, typed metadata, transactions, and UI without any helper process. This is the smallest set that proves the model is real.

### Tier 1 — should-pass, if the plan adds the named services

- **Drivers:** ClickHouse (HTTP), SQL Server (TDS).
- **Features:** ER diagram, schema diff, data compare, XLSX export, HTTP-provider AI assistant, `mcp.tools` contribution, SSH tunnel through a host transport service.
- **Host capabilities required:** in-band TLS upgrade, SSH/tunnel service, graph/plan canvas or WebView, `mcp.tools`, model-provider network access.
- **Why:** These are the highest-value DBX features that still fit the model, and they directly test the services the plan is missing.

### Tier 2 — stretch / explicitly optional

- **Drivers:** MongoDB (document browser with pagination), MQTT or Consul/Nacos (HTTP), Parquet preview via a host DuckDB engine.
- **Features:** wide spreadsheet grid, visual plan graph, connection-profile import.
- **Caveat:** MongoDB needs SRV/DNS and a large guest protocol; Parquet/DuckDB needs a host engine; connection import needs `hosts` create/import.

### Tier 3 — out of reference scope

- Oracle, DB2, JDBC profiles, and all agent-only databases.
- DuckDB engine parity, remote SQLite over SSH.
- CLI-based AI providers (Claude Code CLI, Codex, Cursor, etc.) and a standalone MCP server.
- Kafka/RocketMQ/RabbitMQ/Pulsar/ZooKeeper/etcd consoles.
- PL/SQL debug and other agent-only oracle/admin features.
- Durable scheduled backups unless a host scheduler is added.

### Why this scope is defensible

- It tests the three hardest premises: no outbound sockets (every driver uses host dial), no threads (streaming/cancellation), no native helpers (SQL/KV/file drivers implemented in guest).
- It covers all four DBX data shapes that matter: relational SQL, key-value, embedded file, and HTTP/JSON.
- It leaves out exactly the capabilities whose only DBX implementation is a JVM, a native executable, or a large native engine — which is the honest boundary of the feasibility claim.

## 7. Plugin-system capabilities forced by the findings

These are the decisions the plugin system must make; each is a concrete requirement, not a preference.

### 7.1 Network and TLS

- **`network.dial(host, port)`** with per-Host scoping, DNS resolution (A/AAAA, and SRV for Atlas), timeouts, connect/read/write cancellation, half-close, max sockets per plugin, and audit events.
- **TLS from start** for HTTP/HTTPS, Redis, MongoDB, and ClickHouse HTTPS.
- **In-band TLS upgrade** for PostgreSQL (`SSLRequest`), MySQL (`SSLRequest`), and SQL Server (`PRELOGIN` encryption). A transparent TLS proxy from byte 0 is incorrect for these protocols; the guest must negotiate the protocol first, then ask the host to upgrade the same connection. This is a forced ABI decision.
- **SNI, ALPN, custom CA, and hostname verification** owned by the host. If the host terminates TLS, the guest never handles plaintext credentials on the wire.
- **No raw sockets in guest.** The plan must not expose a guest socket API that bypasses host policy; wazero's only built-in socket support is pre-opened listeners, which is not useful for database clients.

### 7.2 SSH

- Add an **SSH tunnel/transport service** (or let `network.dial` resolve a target through a Host's saved SSH configuration) that:
  - authenticates with host-owned credentials/keys from `vault`;
  - performs host-key verification and known-hosts policy in the host;
  - returns a duplex stream or a scoped dial handle to the guest;
  - supports jump hosts/proxy commands if DBX parity is required.
- Without this, SSH tunnels are the one DBX feature that is unambiguously blocked by the current v1 service list ([ticket `08`](../../.scratch/plugin-system/issues/08-connection-provider-terminal.md), [`map.md`](../../.scratch/plugin-system/map.md)).

### 7.3 Files and embedded databases

- **`files`** must support, beyond streaming:
  - random-access read/write with offset and length;
  - truncate, sync/flush, stat, rename/unlink within scope;
  - advisory locking or single-writer guarantees;
  - optional scoped directory preopens for WASI VFS-based SQLite.
- Either pre-open a user-selected directory (simplest, works with `rusqlite`'s `wasm32-wasi-vfs`) or specify a custom VFS ABI. Without one, local SQLite is not a credible reference feature.
- Large-file export/import needs chunked streaming and backpressure, not whole-file buffers.

### 7.4 Data provider contract (ticket 09)

The contract must cover, at minimum:

- `open`, `ping`, `capabilities`, `query`, `exec`, `cancel`, `begin`/`commit`/`rollback`, `schema`, `explain`;
- streaming rows with row/byte limits and backpressure;
- connection pooling, idle TTL, health checks, reconnect;
- provider-defined Host config and credential schemas, plus test-connection;
- access modes `read-only`/`read-write`/`full`, enforced by the provider and audited by the host;
- capability descriptors for SQL, transactions, schemas, DDL, explain, KV, document, pubsub, and streaming;
- for MongoDB/Redis/MQTT, the contract must not assume SQL-shaped results; document/KV/stream results need their own result shapes or a generic row/document envelope.

### 7.5 Streaming, cancellation, and limits

- Because there are no threads, the host ABI must deliver network bytes and result rows in bounded chunks and let the guest return control.
- `WithCloseOnContextDone(true)` is the hard cancellation backstop; cooperative cancellation at host-call boundaries is the normal path ([wazero `config.go`](https://github.com/tetratelabs/wazero/blob/v1.12.0/config.go#L149-L170)).
- Per-plugin memory pages, socket count, row/byte ceilings, and per-call time budgets are required to make untrusted drivers safe.
- Schema fetch and import/export must be sequential or pipelined by the host; parallel fan-out is not available in guest threads.

### 7.6 Credentials and secrets

- Database passwords, SSH keys/passphrases, AI API keys, and cloud tokens come from **`vault` secret leases**, not plugin storage.
- Host config fields contributed by the provider must be schema-validated and persisted by the host; the plugin cannot write arbitrary connection keys.
- Test-connection must not leak secrets in errors or audit logs.

### 7.7 UI

- Query editor, ordinary grid, schema tree, textual explain, and diff are native today (NATCATTY UI research, commit `9325d1762`, `docs/research/plugin-mygo-ui-native-webview.md`).
- DBX parity needs either new native widgets for **wide DataGrid**, **graph/diagram canvas**, and **plan graph**, or a WebView fallback ([ticket `14`](../../.scratch/plugin-system/issues/14-rich-widgets-language-services.md)).
- The WebView must remain sandboxed with no direct network; all data flows through the host bridge. This matches DBX's own plugin UI constraint ([`plugins/README.md`](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md#L355-L385)).

### 7.8 MCP and AI

- **MCP:** the only viable path is plugin-contributed `mcp.tools`; the host exposes them through its existing MCP server, with scopes, access modes, and audit ([ticket `15`](../../.scratch/plugin-system/issues/15-mcp-tools-contribution.md)). A plugin cannot run DBX's standalone MCP executable.
- **AI:** no host AI service is needed. The plugin uses `network` to call model APIs; keys come from `vault`; streaming SSE needs host streaming reads. CLI-agent providers are excluded.
- If the product wants a host-owned AI experience, that is a separate host service decision; the DBX reference does not force it.

### 7.9 Optional host services outside the premise

- **DuckDB engine**: needed for DBX's DuckDB connections and Parquet preview parity. This is a host-side native engine, so it is outside "WASM-only plugin" but inside the host. If added, it should be a first-party service, not a plugin capability.
- **Background scheduler**: needed for scheduled backups; not needed for the reference scope.
- **SQLite engine**: not needed if the plugin bundles SQLite and the host provides preopens/VFS; a host SQLite service would be a larger, less plugin-like design.

## 8. Decisions unblocked or forced

| Ticket / decision | Effect of these findings |
|---|---|
| `09-data-provider-dbx` | **Unblocked.** Defines the driver scope: Tier 0/1 protocols, non-SQL result shapes, streaming, pooling, access modes. The contract must not assume all providers are SQL. |
| `07-host-api-services` | **Forced.** v1 service list must add or explicitly defer: TLS-capable `network.dial` with in-band upgrade, SSH tunnel/transport, random-access/preopened `files`, and a streaming primitive. Without these, SSH, SQLite, and middleware consoles are cut. |
| `02-wasm-runtime-abi` | **Forced.** ABI must be synchronous host-import based, single-threaded/cooperative, with bounded chunks, cancellation, memory/socket quotas, and no preview2/component-model assumptions. |
| `05-permissions-consent-audit` | **Forced.** Define network endpoint scopes, TLS/CA policy, SSH host scope, file scopes, data access modes, and secret leases. `network` cannot be a wildcard at activation. |
| `08-connection-provider-terminal` | **Forced.** Separate the terminal transport from the data connection; decide whether SSH is a host service or a `network.dial` chain. The host must own host-key trust. |
| `12-ui-abi-widgets` / `14-rich-widgets-language-services` | **Forced.** Choose native widgets vs WebView for wide DataGrid, ER canvas, and plan graph. DBX parity cannot be claimed without one of them. |
| `15-mcp-tools-contribution` | **Confirmed.** Plugin-contributed tools are the only MCP path; standalone plugin MCP servers are impossible. |
| ADR / parity scope | **Forced.** DBX reference is a plugin, not a fork or clone. Parity is tiered by protocol feasibility, not by DBX's headline database count. |

## 9. Risks and open questions

1. **Guest TLS viability.** Host-terminated TLS solves the crypto-provider problem, but requires an in-band upgrade ABI. If the host cannot upgrade mid-stream, each SQL driver needs a WASM-compatible TLS stack; rustls/ring/aws-lc-rs on WASI is not a safe assumption ([rustls README](https://github.com/rustls/rustls/blob/main/README.md#L47-L82)).
2. **Driver effort.** PostgreSQL/MySQL/SQLite are realistic reference implementations; SQL Server and MongoDB are high-effort. The plan should not promise DBX's 100+ coverage.
3. **SQLite VFS.** `rusqlite`'s `wasm32-wasi-vfs` is a real path, but it depends on preopens and WASI file semantics. A host RPC `files` service is not automatically a VFS. This needs a prototype before committing to local SQLite.
4. **Streaming/backpressure ABI.** No threads means the host must own the read loop and chunking. If the ABI is request/response-only, large result sets and long-lived MQTT/Mongo cursors will be painful.
5. **DNS SRV.** MongoDB Atlas and some service-discovery targets need SRV lookups; a simple A/AAAA dialer is insufficient.
6. **TinyGo JSON.** If the plugin SDK uses TinyGo, `encoding/json` is not usable; binary protocols and metadata need hand-written serializers. Rust is the safer default for drivers.
7. **NATCATTY wayfinder docs are local scratch.** The referenced `.scratch/plugin-system/*` tickets are ignored by git and absent from this worktree; they are cited as planning context, not as published sources.

## 10. Sources

DBX (pinned to `2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8`):

- [Repository](https://github.com/t8y2/dbx/tree/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8)
- [README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/README.md)
- [Driver manifest](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-core/assets/database-drivers.manifest.json)
- [Agents README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/README.md)
- [Plugins README](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/plugins/README.md)
- Driver manifests: [Postgres](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-postgres/Cargo.toml), [MySQL](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mysql/Cargo.toml), [Redis](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-redis/Cargo.toml), [MongoDB](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/Cargo.toml), [SQL Server](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-sqlserver/Cargo.toml), [Drivers](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/Cargo.toml), [SQLite worker](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-sqlite-worker/Cargo.toml)
- Source: [ClickHouse driver](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/clickhouse_driver.rs), [SSH tunnel](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/ssh_tunnel.rs), [DuckDB worker](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/duckdb_worker_process.rs), [remote SQLite worker](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-drivers/src/db/sqlite_worker.rs), [Mongo driver](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-mongodb/src/mongo_driver.rs), [Agent manager](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-agent/src/agent_manager.rs), [Oracle OCI](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-driver-agent/src/oracle_oci.rs), [DB2 agent](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/db2/src/main/java/com/dbx/agent/db2/Db2Agent.java), [Oracle Go agent](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/agents/drivers/oracle-go/go.mod), [AI CLI provider](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-ai-provider/src/ai_claude_code_cli.rs), [MCP main](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-mcp/src/main.rs), [plugin runtime](https://github.com/t8y2/dbx/blob/2aaaf831a5fc0f13ed43ca4147ef84fad00b22f8/crates/dbx-plugin-runtime/src/plugins/runtime.rs)

Wazero v1.12.0:

- [Specifications / WASI preview1 table](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/specs.md)
- [Core feature flags](https://github.com/tetratelabs/wazero/blob/v1.12.0/api/features.go)
- [Experimental feature flags (threads)](https://github.com/tetratelabs/wazero/blob/v1.12.0/experimental/features.go)
- [Experimental socket config](https://github.com/tetratelabs/wazero/blob/v1.12.0/experimental/sock/sock.go)
- [Runtime config (memory, cancellation)](https://github.com/tetratelabs/wazero/blob/v1.12.0/config.go)
- [Language constraints](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/_index.md)
- [TinyGo guide](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/tinygo.md)
- [Rust guide](https://github.com/tetratelabs/wazero/blob/v1.12.0/site/content/languages/rust.md)
- [Host function builder](https://github.com/tetratelabs/wazero/blob/v1.12.0/builder.go)
- [WASI Preview 2 issue #2289](https://github.com/tetratelabs/wazero/issues/2289)
- [Component model issue #2200](https://github.com/tetratelabs/wazero/issues/2200)

WASI / protocols / libraries:

- [WASI snapshot-01 docs](https://github.com/WebAssembly/WASI/blob/snapshot-01/phases/snapshot/docs.md)
- [PostgreSQL protocol flow](https://www.postgresql.org/docs/current/protocol-flow.html)
- [MySQL connection phase](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_connection_phase.html)
- [MS-TDS PRELOGIN](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-tds/60f56408-0188-4cd5-8b90-25c6f2423868)
- [Tokio WASI support and feature gates](https://github.com/tokio-rs/tokio/blob/master/tokio/src/lib.rs)
- [rusqlite `wasm32-wasi-vfs`](https://github.com/rusqlite/rusqlite/blob/master/Cargo.toml)
- [rustls platform support](https://github.com/rustls/rustls/blob/main/README.md)

NATCATTY local planning and research:

- [`.scratch/plugin-system/map.md`](../../.scratch/plugin-system/map.md) (locked premises, v1 services, Q12)
- [Ticket `01-dbx-wasm-feasibility`](../../.scratch/plugin-system/issues/01-dbx-wasm-feasibility.md)
- [Ticket `02-wasm-runtime-abi`](../../.scratch/plugin-system/issues/02-wasm-runtime-abi.md)
- [Ticket `05-permissions-consent-audit`](../../.scratch/plugin-system/issues/05-permissions-consent-audit.md)
- [Ticket `07-host-api-services`](../../.scratch/plugin-system/issues/07-host-api-services.md)
- [Ticket `08-connection-provider-terminal`](../../.scratch/plugin-system/issues/08-connection-provider-terminal.md)
- [Ticket `09-data-provider-dbx`](../../.scratch/plugin-system/issues/09-data-provider-dbx.md)
- [Ticket `14-rich-widgets-language-services`](../../.scratch/plugin-system/issues/14-rich-widgets-language-services.md)
- [Ticket `15-mcp-tools-contribution`](../../.scratch/plugin-system/issues/15-mcp-tools-contribution.md)
- Prior UI research, commit `9325d1762`, `docs/research/plugin-mygo-ui-native-webview.md`
