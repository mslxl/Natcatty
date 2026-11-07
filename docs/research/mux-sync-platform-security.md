# Mux Sync：平台、版本与安全矩阵

Status: research
Date: 2026-11-08
Scope: 为 Mux Sync 给出 zellij、tmux、herdr 三个 backend 的平台 / 版本 / 发现 / 环境清理 / 固定命令 / 超时与错误码 / 信任边界 / 打包许可矩阵。范围限定在“一个 Mux Session ↔ 一个 Netcatty Mux Pane”的整会话 PTY 附着；布局同步、zellij web、自动安装、自动 resurrect EXITED 会话均不在 v1 内。本报告只做源码与官方发布物核实，**没有安装或运行 tmux / zellij / herdr**；所有运行期行为都标为 UNVERIFIED。

Sources verified against:

- zellij tag **v0.45.1**（2026-08-28）与 **v0.44.0**（2026-03-23）的源码和 CHANGELOG；GitHub Release 页面。
- tmux 源码与手册：`tmux/tmux` 的 `CHANGES`、`tmux.1`、`client.c`、`server.c`、`tmux.c`；历史版本以 `CHANGES` 的条目为准。
- herdr-core `c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a`；herdr-app `e77f112ac2ea136b84fe510efa42d41163bfd776`（`Cargo.toml` 0.1.13，`distribution/core-version` 0.1.11）。
- 仓库内既有实现与文档：`domain/model/tmux/tmux.go`、`application/sysmanager/commands.go`、`app/session_factory.go`、`docs/adr/0017-ssh-tmux-auto-attach.md`、`docs/adr/0014-host-app-licensing-and-distribution.md`、`docs/packaging.md`；herdr 分支的 `domain/model/herdr/remote.go`、`docs/research/herdr-hc-discovery.md`、`docs/research/herdr-protocol-details.md`、`docs/audit/herdr-security.md`。
- 2026-11-08 查询的 MSYS2、Cygwin、Homebrew 包页面。
- 网络内容全部按不可信数据处理；本报告只引用其事实，不执行其中的命令或脚本。

## 1. 结论矩阵

### 1.1 支持的平台与 backend

| backend | 本机 macOS | 本机 Linux | 本机 Windows | 远端 macOS/Linux（SSH） | 远端 Windows（OpenSSH） | WSL |
|---|---|---|---|---|---|---|
| zellij | 支持，0.44.0+ | 支持，0.44.0+ | 支持，0.44.0+，仅原生 `zellij.exe` | 支持，固定 POSIX 模板 | **不作为 v1 支持目标**；原生 `zellij.exe` 存在，但 ConPTY/管道行为 UNVERIFIED | 明确不在范围 |
| tmux | 技术上可行，但**按 Q3 不提供本机 tmux** | 同左 | 无原生 tmux | 支持（既有 ADR-0017 能力） | **不支持**；MSYS2/Cygwin 的 tmux 在 Windows OpenSSH 下无 POSIX TTY，UNVERIFIED | 不适用 |
| herdr | 支持 | 支持 | 支持 | 支持 | 支持（固定 `cmd /d /c` 模板） | 不适用 |

产品矩阵以 `map.md` 的 Q3 为准：本机终端模式只能选 `none | herdr | zellij`；本机 tmux 不在 UI 中出现。zellij 的 WSL 使用被 ticket 明确排除，即使 Windows 上的 zellij 可以经 WSL 运行。

### 1.2 版本门槛

| backend | 最低 | 目标/已验证 | 判定方式 | 超出范围时 |
|---|---|---|---|---|
| zellij | **0.44.0** | **0.45.1** | `zellij --version` 首行必须匹配 `zellij <semver>`，再按 semver 比较 | `< 0.44.0`：隐藏 backend，typed `unsupported_version`；`> 0.45.1`：允许但提示“未验证”，协议错误按 typed 错误上报 |
| tmux | **2.1** | 远程侧以实际发现版本为准 | `tmux -V` 首行 `tmux <version>` | `< 2.1`：不启用 tmux 能力；本机 tmux 无论如何不提供 |
| herdr | **0.1.11** | core 自报版本（本 pin 的构建为 0.1.18）；协议 **generation 1** | `--version` 首行以 `hc ` 开头；连接后 `hello` 的 `generation` 必须为 1 | generation ≠ 1：`unsupported_generation`，不重试同一 core |

zellij 的 0.44.0 门槛不是“第一次支持 Windows”之外的选择：`contract_version_1` 的会话目录机制在 0.44.0 已存在，0.44.0 与 0.45.1 的客户端 / 服务端契约版本同为 1。0.45.0 增加 per-tab 尺寸；0.45.1 增加 attach 时携带初始命令（PR 5543，`zellij attach -c -- htop`）。v1 不使用初始命令，但把它作为目标版本，因为它是当前验证过的、包含 0.45.0 尺寸修复的最新补丁版。

### 1.3 五条硬规则

1. **命令模板固定，名字 / 路径只作为位置参数。** 不把 session 名、可执行文件路径、用户输入拼进 shell 字符串；远端 `sh -c '...' sh <path> <name>`，Windows 只接受固定字面路径。
2. **每个 backend 子进程先清环境。** zellij 清 `ZELLIJ*` 与会重定向配置的变量；tmux 清 `TMUX*`；herdr 清 `HC_*` / `HERDR_CORE_DIRS`。本地子进程与远端固定命令都做。
3. **发现只做只读探测。** zellij 只能用 `--version`（但它有磁盘副作用，见 §2.5）；herdr 只能用 `--version`，绝不能用 `connect` / `server status` / `ls` 做发现，因为它们会启动 core；tmux `-V` 无服务器副作用。
4. **信任边界 = OS 用户 / SSH 账户。** 没有应用层认证、没有 per-client 权限、没有网络监听。zellij web 绝不能被隐式启用；session 名不得是 `http://` / `https://` 远程 URL。
5. **不自动安装、不捆绑。** 缺失即降级或隐藏；未来若捆绑二进制，必须走 ADR-0014 复审并补齐 NOTICE / 许可证。

## 2. zellij

### 2.1 版本、契约与跨版本可见性

- `CLIENT_SERVER_CONTRACT_VERSION: usize = 1`，目录名由它生成 `contract_version_1`，追加到 socket 目录之后（[consts.rs#L26-L91](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs#L26-L91)）。v0.44.0 的同一文件也是 `1`（[v0.44.0 consts.rs](https://github.com/zellij-org/zellij/blob/v0.44.0/zellij-utils/src/consts.rs)）。
- 会话 socket 与 `session_info` 缓存都位于 `.../contract_version_1/` 下；因此 **0.44.0 与 0.45.x 互相可见**，而使用不同 contract 目录的未来版本会互相不可见（列表为空，不是报错）。同一 contract 内混合版本由 protobuf 契约保证兼容；0.44 客户端连 0.45 服务端时会退回“整会话取最小尺寸”的旧语义，具体渲染差异 UNVERIFIED。
- 0.44.0 的 CHANGELOG 明确列出原生 Windows 支持；0.45.0 列出 per-tab sizes（PR 5133 “Independent tab sizes”）；0.45.1 列出 attach initial command（PR 5543）。见 [v0.44.0 release](https://github.com/zellij-org/zellij/releases/tag/v0.44.0)、[v0.45.0 release](https://github.com/zellij-org/zellij/releases/tag/v0.45.0)、[v0.45.1 release](https://github.com/zellij-org/zellij/releases/tag/v0.45.1)。
- 0.44.0 之前的会话使用旧的 socket 目录布局（contract 目录机制随 0.44.0 引入）；0.44.0+ 看不到它们。旧版本精确路径未在本次源码核对中确认，标 UNVERIFIED。因此升级 zellij 可能表现为“会话消失”，UI 应提示而不是当作错误。

**结论：** 版本门槛用 semver，不用 contract dir 探测。`list-sessions` 为空在正常情况（真的没有会话）与 contract 变化时不可区分；应用无权假定 `contract_version_1` 的目录布局。`zellij setup --check` 也不打印 session-info / socket 目录（它只打印 config / cache / data / plugin / layout / system data 目录，[setup.rs#L428-L531](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/setup.rs#L428-L531)），所以没有可靠的运行时契约探测。

### 2.2 会话列表与状态

`zellij list-sessions` 同时列出 live socket 与可 resurrect 的布局；`--short` 只输出名字；`--no-formatting` 输出 `NAME [Created X ago] (EXITED - attach to resurrect)` 或 `NAME [Created X ago] (current)`；没有会话时退出码 1，有会话时 0（`sessions.rs` 的 `print_sessions` / `list_sessions`，[sessions.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/sessions.rs)）。名字本身允许空格、方括号等字符（`validate_session_name` 只拒绝空、`.`、`..`、`/`），所以解析必须从行尾匹配已知后缀，并且只接受符合 Netcatty 严格字符集的会话名；其余会话视为“非 Netcatty 管理”，只读展示或忽略。

Mux Sync 的呈现规则：

- 因为列表前会清掉 `ZELLIJ_SESSION_NAME`，`(current)` 标记不会出现；live 的判定就是“没有 EXITED 后缀”。这正好避免把 Netcatty 自己所在的会话误标为 current。
- live → 可 attach；EXITED → 显示为“已退出”，v1 **不自动 attach**（attach 会触发 resurrect），只提供删除。
- 列表解析失败的行不猜测，记录一次脱敏日志并跳过。
- `kill-session` 只对 live 会话有效；对 EXITED 会退出 1。EXITED 的清理用 `delete-session`。

### 2.3 attach / create 语义与三个坑

源码路径：`src/commands.rs` 的 `start_client_plan` 只做 `assert_session_ne`（防自嵌套 panic），真正的分派在 `attach_with_session_name` 与 resurrect 分支（[commands.rs#L554-L611](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs#L554-L611)、[commands.rs#L689-L807](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs#L689-L807)）。

1. **不带 `-c` 是前缀匹配。** `attach NAME` 先找精确 live 匹配，再接受唯一前缀；歧义或不存在都退出 1（`match_session_name`）。这与 tmux 的 `=` 精确目标不同。Mux Sync 若直接 `attach NAME`，在会话刚消失、而另一个会话以该名字为前缀时可能附到错误会话。
   - 规避：attach 前用同一次列表结果做“精确 live 存在 + 无其他 live 会话以该名字为前缀”的判定；不满足就拒绝 attach。列表与 attach 之间仍有极小竞态，无法由 CLI 原子消除；把这个限制写进实现注释，并在 UI 上只在用户点击已有会话时 attach。
2. **`attach -c NAME` 是精确的，但会在缺失时新建，且复活优先。** `create=true` 走 `session_exists` 精确判断：live 则 attach，否则 `ClientInfo::New`。但在它之前，若存在 `session_id` 对应的 resurrection layout 且会话非 live，`(Some, Some)` 分支无条件产生 `ClientInfo::Resurrect`，**即使带了 `-c`**。也就是说 EXITED 会话无法用 `-c` 强制新建；必须先 `delete-session` 再创建。v1 的“无自动 resurrect”规则因此要求：恢复 Mux Pane 时先查 live 精确存在，EXITED 不 attach。
3. **同名嵌套会 panic。** 若继承的 `ZELLIJ_SESSION_NAME` 等于要 attach 的会话名，客户端 `panic!`（[commands.rs#L803-L807](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs#L803-L807)）。必须清理 `ZELLIJ_SESSION_NAME`（以及整个 `ZELLIJ*` 集合）。

其他语义：

- 普通 `attach` 一个 EXITED 会话会 resurrect（`resurrection_layout` 分支）；恢复出来的命令默认 suspended，除非 `--force-run-commands`。v1 不使用。
- `zellij attach` 还接受 `http://` / `https://` 开头的远程会话 URL，并拒绝与 `options`、`-c`、初始命令同时使用（[commands.rs#L711-L746](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs#L711-L746)）。Netcatty 的 session 名字校验必须拒绝这两个前缀，避免把用户输入变成“远程 attach + token”路径。
- `kill-session` 先用 `assert_session`（精确 live）再连接 socket（[commands.rs#L123-L130](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs#L123-L130)、[sessions.rs#L295-L329](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/sessions.rs#L295-L329)）。live 成功退出 0，会话结束后元数据留下成为 EXITED；不存在退出 1。
- `delete-session` 先 `assert_dead_session`：live 且没有 `--force` 退出 1；不存在退出 2；成功删除 session-info 目录，退出 0（[commands.rs#L136-L148](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs#L136-L148)、[sessions.rs#L331-L362](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/sessions.rs#L331-L362)）。v1 不使用 `--force`，因为那会先杀 live 再删元数据，等于把 kill 与 delete 合在一起。
- 退出码汇总（源码可验证的部分）：列表无会话 1；attach 歧义/不存在 1；kill 非 live 1；delete 非 live 的活会话 1、不存在 2；探测脚本自定的 126/127。attach 会话期间的退出码、SIGTERM、远端服务器消失等运行期码 UNVERIFIED。

### 2.4 `--version` 与 `setup --check` 的副作用

两者都不是“无副作用”的只读操作：

- `main()` 在 `CliArgs::parse()` 之前执行 `configure_logger()` 与 `create_config_and_cache_folders()`（[main.rs#L17-L20](https://github.com/zellij-org/zellij/blob/v0.45.1/src/main.rs#L17-L20)）。因此 **包括 `--version` 和 `setup --check` 在内的每次调用**都会：
  - 创建 `ZELLIJ_TMP_DIR`（Unix `/tmp/zellij-<uid>`，Windows `%TEMP%\zellij`）与 `zellij-log/`，并创建 / 打开 `zellij.log`，单文件 16 MiB 轮转，备份 `zellij.log.old.1`（[logging.rs#L20-L55](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/logging.rs#L20-L55)、[consts.rs#L333-L347](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs#L333-L347)）。目录 / 文件权限在 Unix 上被设为 0700 / 0600。
  - 创建用户 cache 目录、可解析时的默认 config 目录、`contract_version_1/session_info/`；0.45.1 还会清理空的 session-info 目录。0.44.0 不做这个清理。
  - `configure_logger()` 对目录 / 文件创建使用 `unwrap()`：若 `TEMP` 不可写，zellij 在解析参数前就 panic（Rust 默认退出 101）。只读家目录 / 只读临时目录下的“探测”会失败且返回的不是版本号。
- `setup --check` 读取配置并打印目录与 `[Version]: "x.y.z"`，配置错误时打印 `CONFIG ERROR`；不启动服务器。它仍然走上面的目录创建。退出码：成功 0，配置错误路径在 `setup.rs` 内部形成非零（具体码值未逐一核对，UNVERIFIED）。
- 版本输出是 `zellij 0.45.1` 形式（clap `#[clap(version, name = "zellij")]`）。

**实现要求：** 把 zellij 发现视为“会写磁盘的操作”，在 UI 上不要宣称只读；探测失败若发生在创建目录阶段，要映射为 `probe_failed` 而不是 `not_installed`。不要用 `setup --check` 做发现（更慢、解析更脆、同样有副作用）；`--version` 足够。

### 2.5 平台细节

- **本机 Unix：** socket 目录优先 `ZELLIJ_SOCKET_DIR`，否则 `XDG_RUNTIME_DIR/zellij`，再否则 `/tmp/zellij-<uid>`，最后追加 `contract_version_1`（[consts.rs#L331-L349](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs#L331-L349)）。目录 0700、socket 01700（[cli_client.rs#L27](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-client/src/cli_client.rs#L27)、[server lib.rs#L939](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-server/src/lib.rs#L939)）。UDS 路径长度上限 Linux 108、macOS 104 字节；超过时 zellij 自己会报错。
- **本机 Windows：** 原生支持从 0.44.0 开始；socket 目录为 canonicalize 后的 `%TEMP%\zellij`，再追加 `contract_version_1`；长度上限 256（[consts.rs#L352-L397](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs#L352-L397)）。会话“socket”在磁盘上是保存服务端 PID 的普通文件，真正的通道是 interprocess 的 namespaced named pipe；`ipc_connect_reply` 用同一名字加 `-reply`（[consts.rs#L247-L311](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs#L247-L311)）。MSI 的 per-user 目录是 `[LocalAppDataFolder]Zellij`，per-machine 是 `[ProgramFilesFolder]Zellij`，并写 PATH（`wix/main.wxs`）。**Windows 上没有 Unix 的 0700/01700 等价显式 ACL**；named pipe 使用创建进程的默认安全描述符，`set_permissions` 在非 Unix 是空实现（[shared.rs#L23-L31](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/shared.rs#L23-L31)）。同一台多用户 Windows 上其他标准用户是否能连接，UNVERIFIED。
- **远端 Windows：** 原生 `zellij.exe` 存在，但经 Windows OpenSSH 进入时默认 shell 是 cmd 或 PowerShell，且 `-T` 无 PTY；zellij 客户端在 Windows 上使用 named pipe + ConPTY 语义，跨 sshd 会话的行为未验证。v1 不承诺支持；若要做，只能用固定 `cmd /d /c` 模板并先做 `--version` 探测，把失败映射为 `remote_windows_unsupported`。不要尝试通过 `bash -lc` 调 MSYS2 的 zellij（那是另一套 POSIX 环境，且我们未验证）。
- **WSL：** 明确不在范围（ticket 与 map.md）。若用户在 Windows 上把 WSL 的 zellij 放上 PATH，`zellij.exe` 规则可避免误发现：只接受 `.exe`。

### 2.6 发现路径

本机 / 远端 Unix 的候选顺序（与 herdr 的既有实现一致，便于统一）：

1. `command -v zellij` 返回的绝对路径（拒绝 shell function / builtin 打印的裸名字）。
2. `$HOME/.cargo/bin/zellij`（`cargo install zellij`）。
3. `/opt/homebrew/bin/zellij`（Apple Silicon Homebrew）。
4. `/usr/local/bin/zellij`（Intel Homebrew / 手工安装）。
5. `/usr/bin/zellij`（发行版包）。
6. 可选：`$HOME/.local/bin/zellij`、mise / nix 的 shim 目录（按需，不默认执行登录 shell）。
7. 仅本机：`$SHELL -lc 'printf %s "$PATH"'` 作为最后兜底，3 秒硬超时后 kill；远端非交互 SSH 不能依赖 rc，不使用登录 shell。

本机 Windows：进程 PATH 中先 `zellij.exe`；再 `%LOCALAPPDATA%\Zellij\zellij.exe`、`%ProgramFiles%\Zellij\zellij.exe`、`%USERPROFILE%\.cargo\bin\zellij.exe`。只接受 `.exe`，不通过 PATHEXT 执行 `.cmd` / `.bat`（后者是任意批处理代码）。没有登录 shell 兜底。

远端 Windows：固定模板只检查上述两个安装目录的字面路径；不解析 `where.exe` 输出，不把它拼进下一条命令。

### 2.7 环境清理

zellij 读取且能被用来重定向会话 / 配置的变量：

| 变量 | 作用 | 处理 |
|---|---|---|
| `ZELLIJ` | 标识当前在 zellij 内（嵌套检测 / 行为分支） | unset |
| `ZELLIJ_SESSION_NAME` | 当前会话名；等于目标名时客户端 panic | unset（关键） |
| `ZELLIJ_SOCKET_DIR` | 重定向 socket 根目录，contract 目录追加其后 | unset（关键） |
| `ZELLIJ_CONFIG_FILE` | 指定配置文件（可含 `web_server true`） | unset |
| `ZELLIJ_CONFIG_DIR` | 指定配置目录 | unset |
| `ZELLIJ_LAYOUT_DIR` | 指定 layout 目录 | unset |
| `ZELLIJ_PANE_ID` | zellij 内部动作使用 | unset |

来源：[envs.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/envs.rs)、[consts.rs#L10-L12](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs#L10-L12)、[actions.rs#L2118](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/input/actions.rs#L2118)。

不要清理 `HOME`、`SHELL`、`PATH`、`TMPDIR` / `TEMP`、`XDG_RUNTIME_DIR`：它们是发现与 socket 位置所必需；清理它们会改变语义而不是去掉注入面。清理必须发生在**本地子进程的 env** 和**远端固定命令的 shell 内**两处；只在本地清、远端不清，等于把远端 rc 里的 `ZELLIJ_SOCKET_DIR` 留给攻击面。

### 2.8 zellij web：必须显式禁止

- 配置选项 `web_server` 默认 `false`，`web_sharing` 默认 off（[options.rs#L280-L305](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/input/options.rs#L280-L305)）。但用户配置可以把它设为 `true`。
- 客户端在 **New** 与 **Resurrect** 路径上读取 `config_options.web_server`，为 true 时 spawn 本地 web server；Attach 不启动（[zellij-client lib.rs#L964](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-client/src/lib.rs#L964)、[#L1103](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-client/src/lib.rs#L1103)、[#L1161](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-client/src/lib.rs#L1161)）。
- Mux Sync 的创建路径会触发 New，因此必须在 attach / create 命令行上覆盖 `web_server=false`：`attach ... options --web-server false` 在源码上是可行的（`SessionCommand::Options(Options)` 复用带 clap 注解的 `Options`，[cli.rs#L299-L304](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/cli.rs#L299-L304)），但实际命令行解析与 merge 行为未运行验证，UNVERIFIED。
- 双保险：绝不运行 `zellij web`；拒绝 `http://` / `https://` 会话名；若发现用户在配置里开了 `web_server`，在 Mux Sync 设置页提示，但不因用户配置拒绝整个 backend（我们已覆盖为 false）。
- 若命令行覆盖最终被证明不可行，创建路径必须直接拒绝并在 UI 说明“用户配置启用了 zellij web”，而不是默默创建出一个会把会话暴露到本机 web 端口的服务。

### 2.9 固定模板

以下模板都是**固定字符串**；`$1` 是探测得到的绝对路径，`$2` 是严格校验后的会话名，二者作为 `sh -c` 的位置参数传入，不参与字符串拼接。`<scrub>` 是 §2.7 的 `unset` 列表。

发现 / 版本：

```sh
sh -c '<scrub>
p=$(command -v zellij 2>/dev/null)
case "$p" in /*) ;; *) p= ;; esac
if [ -z "$p" ]; then
  for c in "$HOME/.cargo/bin/zellij" /opt/homebrew/bin/zellij /usr/local/bin/zellij /usr/bin/zellij; do
    if [ -x "$c" ]; then p=$c; break; fi
  done
fi
[ -n "$p" ] || exit 127
v=$("$p" --version </dev/null 2>/dev/null | head -n 1)
case "$v" in "zellij "*) printf "%s\n%s\n" "$p" "$v" ;; *) exit 126 ;; esac'
```

列表（一次调用同时拿 live / EXITED）：

```sh
sh -c '<scrub>; exec "$1" list-sessions --no-formatting' sh /abs/path/zellij
```

attach（已有精确会话；调用前必须用列表结果确认精确 live 存在且无前缀歧义）：

```sh
sh -c '<scrub>; exec "$1" attach "$2" options --web-server false' sh /abs/path/zellij NAME
```

创建并附着的用户动作（精确，允许新建；EXITED 会优先 resurrect，因此调用前必须先确认没有同名 EXITED）：

```sh
sh -c '<scrub>; exec "$1" attach -c "$2" options --web-server false' sh /abs/path/zellij NAME
```

kill live / delete EXITED：

```sh
sh -c '<scrub>; exec "$1" kill-session "$2"' sh /abs/path/zellij NAME
sh -c '<scrub>; exec "$1" delete-session "$2"' sh /abs/path/zellij NAME
```

会话名校验（Netcatty 管理的名字；比 zellij 自身更严格）：

- `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`
- 拒绝空、`.`、`..`、`/`、空白、控制字符、`:`、`http://`、`https://`。
- 自动生成的 Mux 会话名建议为 `natcatty-<sanitized-id>`，与 tmux 的现有前缀一致，避免与其他工具冲突。

### 2.10 超时与错误码

| 操作 | 超时 | 说明 |
|---|---|---|
| 每个候选 `--version` | 5 秒 | 候选可能落在慢速网络家目录；超时即视为不可用，不重试同一路径 |
| 发现总预算 | 10 秒 | 与 `tmuxProbeTimeout` 对齐 |
| `list-sessions` | 10 秒 | 失败不清空缓存，保留上一次列表并标记 stale |
| attach 首帧 | 15 秒 | 进程仍存活但 15 秒无输出 → 终止并报 `attach_timeout`；不假设“没输出就是卡住”之外的原因 |
| kill / delete | 10 秒 | 超时后不自动重试，避免重放删除 |
| 登录 shell PATH 兜底 | 3 秒 | 仅本机 Unix |

错误码（typed，UI 不解析英文 stderr）：

| code | 触发 |
|---|---|
| `zellij_not_installed` | 所有候选不存在 / 非绝对路径 |
| `zellij_not_executable` | 候选存在但 `-x` 失败或 `--version` 不以 `zellij ` 开头（126） |
| `zellij_unsupported_version` | 版本 < 0.44.0 |
| `zellij_version_untested` | 版本 > 0.45.1（警告 + 继续） |
| `zellij_ambiguous_session` | 名字是多个 live 会话的前缀 |
| `zellij_session_missing` | 精确 live 不存在（attach 前检查） |
| `zellij_session_exited` | 目标是 EXITED；v1 不 attach |
| `zellij_kill_failed` | kill 退出 1（非 live） |
| `zellij_delete_failed` | delete 退出 1/2 |
| `zellij_web_forbidden` | 配置要求 web server 且命令行覆盖不可用 |
| `zellij_probe_failed` | 探测进程崩溃 / 目录不可写 / 输出不可解析 |
| `zellij_timeout` | 上述任一超时 |

## 3. tmux

### 3.1 最低版本与能力

- **最低 2.1。** 这是 `=` 精确目标前缀被文档化的版本；Netcatty 依赖 `=name` 来避免前缀 / 模糊匹配。更老版本即使“碰巧”支持，也不在该能力的承诺范围内。
- control mode `-C` / `-CC` 自 **1.8** 起存在；`new-session -A` 也是 1.8；`list-sessions -F` 自 1.7（`CHANGES`）。
- `=` 目标匹配、`has-session -t '=name'` 的既有实现见 `domain/model/tmux/tmux.go` 的 `ProbeCommand` / `WrapperCommand`。
- 客户端与服务端协议版本必须一致；版本不一致时客户端拒绝连接并打印协议错误（`client.c`）。同一台机器上升级 tmux 后，旧 server 仍可能驻留；表现为 attach 失败而不是“会话消失”。UI 应提示“可能有旧 tmux server 在运行”。

### 3.2 control mode 可用性，以及为什么 v1 不用它

- `tmux -C attach-session -t '=name'` 在无 TTY 的 exec 通道上可用；`-CC` 需要 `tcgetattr` 的真实 TTY，不能从 app 的 exec 通道使用（`client.c`）。`-C` 输出 `%begin/%end/%error`、`%sessions-changed` 等通知；`%sessions-changed` 是判断会话增删的推荐信号。
- 但 **control-mode 客户端本身是一个 client**。tmux 按最小客户端尺寸裁剪会话；`-C` 客户端的尺寸来自哪里、是否会以 80x24 把 PTY attach 的窗口压小，源码上不直观，运行期未验证。Mux Sync v1 因此**不使用 control mode**，继续用 3 秒轮询 `list-sessions -F`（`DefaultInterval`），与既有系统管理界面一致，避免第二个客户端影响尺寸。
- 若未来要用 control mode：必须在同一控制连接里显式设置该客户端尺寸（例如 `refresh-client -C WxH`），并在 attach / detach / resize 时保持与 PTY 客户端一致；这属于后续 ticket，不在本报告承诺。

### 3.3 本机 tmux 是否可行

技术可行：macOS / Linux 上 tmux 有真正的 PTY，`new-session -A` / `attach` 都能工作。但**产品上不提供**：Q3 规定本机终端模式为 `none | herdr | zellij`；本机 tmux 不出现在 TerminalMode 单选中。不要把“技术上可行”误写成“支持”。

Windows 原生没有 tmux；本机 Windows 不适用。

### 3.4 远端 Windows：MSYS2 / Cygwin

- MSYS2 的 `msys` 仓库有 `tmux 3.8-1`，路径 `/usr/bin/tmux.exe`，`pacman -S tmux` 安装（MSYS2 包页面，2026-11-08 查询）。
- Cygwin 的 tmux 在稳定仓库是 **ORPHANED**，稳定版 3.3a-1（2024-01-31），test 版 3.7b-1（Cygwin 包页面，2026-11-08 查询）。
- 两者都需要真正的 POSIX TTY 与 MSYS2 / Cygwin 运行时（DLL、termios 模拟）。Windows OpenSSH 的默认 shell 是 cmd / PowerShell，`ssh -T` 没有 PTY；即使能启动 `tmux.exe`，客户端 `tcgetattr` 与交互式终端语义也不成立。若用户自行运行 MSYS2 sshd 或强制分配 PTY，可能可用，但这已不是 Windows OpenSSH 的标准路径，且本报告未运行验证。
- **v1 结论：远端 Windows 不支持 tmux。** 远端 Windows 的持久会话走 herdr（已支持）或未来的 zellij（UNVERIFIED）。发现阶段若在 Windows 上看到 `MINGW*` / `MSYS*` 的 `uname`，不要把它当作可用的 POSIX 环境；按 `tmux_remote_windows_unsupported` 处理。

### 3.5 发现路径

- Unix：`command -v tmux`（绝对路径）；`/opt/homebrew/bin`、`/usr/local/bin`、`/usr/bin`、`/bin` 兜底；本机可加登录 shell PATH（3 秒）。
- 远端：只发非交互 `sh -c`，使用 `command -v` + 上述绝对路径兜底；不读 rc，不执行登录 shell。
- Windows：不支持，不做发现。
- 验明：`tmux -V` 退出 0 且首行匹配 `tmux <version>`；只接受绝对路径。

注意：既有 `domain/model/tmux/tmux.go` 的 `ProbeCommand` / `WrapperCommand` 使用裸 `tmux` 命令（依赖非交互 PATH），且 `ProbeCommand` 只检查 `${TMUX:-}` 后直接 `command -v tmux`。Mux Sync 需要更严格的“解析绝对路径 + 位置参数”模板，属于对 ADR-0017 命令构造的收紧，不改变其“每 Host opt-in、缺失回退登录 shell”的产品语义。

### 3.6 环境清理

| 变量 | 作用 | 处理 |
|---|---|---|
| `TMUX` | 标识当前在 tmux 内；既有 `ProbeCommand` 用它判断 nested | unset（Mux Sync 的独立会话不应继承 nested 判定） |
| `TMUX_PANE` | 当前 pane 标识 | unset |
| `TMUX_TMPDIR` | 重定向 tmux socket 根目录 | unset（关键） |

不要清理 `HOME`、`SHELL`、`PATH`。不要使用 `-L` / `-S` 指定 socket 名或路径；那等于允许第二套 server 命名空间，v1 不需要。

### 3.7 固定模板

发现 / 版本：

```sh
sh -c '<scrub>
p=$(command -v tmux 2>/dev/null)
case "$p" in /*) ;; *) p= ;; esac
if [ -z "$p" ]; then
  for c in /opt/homebrew/bin/tmux /usr/local/bin/tmux /usr/bin/tmux /bin/tmux; do
    if [ -x "$c" ]; then p=$c; break; fi
  done
fi
[ -n "$p" ] || exit 127
v=$("$p" -V </dev/null 2>/dev/null | head -n 1)
case "$v" in "tmux "*) printf "%s\n%s\n" "$p" "$v" ;; *) exit 126 ;; esac'
```

列表（tab 由 shell `printf` 生成，tmux 不展开 `\t`）：

```sh
sh -c '<scrub>
f=$(printf "#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}\t#{session_activity}")
exec "$1" list-sessions -F "$f"' sh /abs/path/tmux
```

精确 attach（仅用户点击已有会话；调用前用列表确认）：

```sh
sh -c '<scrub>; exec "$1" attach-session -t "=$2"' sh /abs/path/tmux NAME
```

创建或附着（用户明确的“打开 / 新建”动作；`-A` 语义是 live 则 attach，否则 create）：

```sh
sh -c '<scrub>; exec "$1" new-session -A -s "$2"' sh /abs/path/tmux NAME
```

kill：

```sh
sh -c '<scrub>; exec "$1" kill-session -t "=$2"' sh /abs/path/tmux NAME
```

control mode（v1 不用，仅供后续）：

```sh
sh -c '<scrub>; exec "$1" -C attach-session -t "=$2"' sh /abs/path/tmux NAME
```

会话名校验：`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`；拒绝 `:`、`.`、控制字符、空白。tmux 自身允许更多字符，但 Netcatty 只管理严格子集，避免 target 语法歧义。

### 3.8 超时与错误码

| 操作 | 超时 |
|---|---|
| 发现 / `-V` | 5 秒（总预算 10 秒） |
| `list-sessions` | 10 秒 |
| attach 首帧 | 15 秒 |
| kill | 10 秒 |
| 登录 shell PATH 兜底（本机） | 3 秒 |

| code | 触发 |
|---|---|
| `tmux_not_installed` | 无绝对路径候选（127） |
| `tmux_not_executable` | `-x` 失败 / `-V` 不符合（126） |
| `tmux_unsupported_version` | < 2.1 |
| `tmux_no_server` | `list-sessions` 报 no server running / 退出 1；这是正常空列表 |
| `tmux_session_missing` | `=name` 不存在 |
| `tmux_duplicate_session` | 创建时同名已存在（`new-session -A` 不会触发，但 detached create 会） |
| `tmux_protocol_mismatch` | 客户端 / 服务端协议版本不一致 |
| `tmux_remote_windows_unsupported` | 远端是 Windows / MSYS2 / Cygwin |
| `tmux_timeout` | 上述超时 |

tmux 大多数错误都退出 1，靠 stderr 区分；应用不应把 stderr 原文直接当错误码。按操作上下文映射，stderr 仅作为脱敏后的 detail。

## 4. herdr

### 4.1 版本、generation 与方法

- 最低 core **0.1.11**（herdr-app 的 `distribution/core-version`），协议 **generation 1**（`api.rs`）。本 pin 的 core 自报 0.1.18、32 个方法、7 个 feature；`hello` 的 generation 不匹配返回 `-32000 unsupported_generation`。见 herdr 分支的 `docs/research/herdr-protocol-details.md` §9 与 [api.rs#L15](https://github.com/herdrdev/herdr-core/blob/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a/crates/herdr-core/src/api.rs#L15)。
- generation 语义：`hello.generation > 1` → 客户端过旧；`< 1` 或版本低于最低 core → core 过旧。core 没有“最低客户端版本”检查，只有 generation 与方法 / feature 能力错误。
- 传输：`hc connect`（本机 UDS / Windows named pipe），先 plain 后 framed；`terminal.attach` 必须 framed。普通 RPC 没有服务端超时，Go 客户端必须自己设。

### 4.2 kill / rm / events

- `terminal.kill`：params `{"terminal": "t7", "caller": null}`；对 live 终端按 HUP 500ms → TERM 500ms → KILL 2000ms 升级，返回时可能仍在运行；调用超时至少 3 秒。若终端带 `rm:true` 且无等待的父，kill 后记录可能已删除，随后结果可能是 `not_found`——即“杀掉了但结果 not_found”（[runtime.rs#L798-L833](https://github.com/herdrdev/herdr-core/blob/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a/crates/herdr-core/src/runtime.rs#L798-L833)、herdr 分支 `docs/research/herdr-protocol-details.md` §4.7）。
- `terminal.rm`：Mux Sync 的“关闭并删除”应显式使用该方法的语义，而不是假设 kill 一定删记录；具体 params / 前置条件以 schema 与 protocol-details 为准（`terminal.rm` 在 32 方法集合内，见 herdr 分支 `docs/research/herdr-protocol-details.md` §4）。
- `events.subscribe`：回复是同一把锁内产生的快照 `seq` / `terminals` / `channel`，之后事件作为 JSON 行 notification 到达；`seq` 每事件 +1、core 重启后重置；重连必须重新订阅拿新快照，不能沿用旧 seq。heartbeat 15 秒，且仅在队列为空时发送；不要把 15 秒无事件当断线（[api.rs#L589-L598](https://github.com/herdrdev/herdr-core/blob/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a/crates/herdr-core/src/api.rs#L589-L598)、[server.rs#L231-L234](https://github.com/herdrdev/herdr-core/blob/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a/crates/herdr-core/src/server.rs#L231-L234)）。
- 本地 / 远端、自启动、hello 的细节：`hc connect` 连不上时可能自启动 core（Unix 先查 `core.lock`，平台守卫通过后 detached spawn `herdr-core serve`，10ms 轮询最多 5 秒；Linux 尽量 systemd user service，macOS 非 SSH 走 launchd，Windows 用 `DETACHED_PROCESS`）。这对 Mux Sync 很重要：**打开 herdr 的会话列表就会连接，可能启动 core**；发现阶段不能这么做。

### 4.3 发现路径

本地优先级（herdr-app 实现优先于其模块注释）：

| 优先级 | 来源 | Unix | Windows |
|---|---|---|---|
| 1 | `settings.json` 的 `core` | 支持 `~/` 展开 | 同左 |
| 2 | 当前进程 PATH（每目录先 `herdr-core` 后 `hc`） | 同左 | 只看 `.exe` |
| 3 | 常见安装目录 | `~/.local/bin`、`/opt/homebrew/bin`、`/usr/local/bin`、`~/.local/share/mise/shims`、`~/.nix-profile/bin`、`/run/current-system/sw/bin`、`~/.cargo/bin`；macOS 追加 bundle 路径 | `%LOCALAPPDATA%\Programs\herdr-core`、`%USERPROFILE%\.cargo\bin` |
| 4 | 登录 shell PATH | `$SHELL -lc 'printf %s "$PATH"'`，3 秒超时 | 无 |
| 例外 | `HERDR_CORE_DIRS` | 设置后跳过 2–4 | 同左 |

远端发现：没有第一方“远端 `--version` 探测”；core 的远端链路只做连接（Unix `sh -c` + `$HOME/.local/bin` 兜底，Windows 固定 `.\AppData\Local\Programs\herdr-core\herdr-core.exe`）。Netcatty 应仿照其形状，只跑 `--version`。详见 herdr 分支的 `docs/research/herdr-hc-discovery.md`。

`--version` 是唯一可移植的只读探测；`hc server status` 在 Unix 会自启动 core，`ls` / `schema` / `templates` / `events` / 裸 `hc` 都会启动，`server stop` 会停掉 core，`hc connect` 无法关闭自启动。

### 4.4 固定模板与 Windows 远端

仓库 herdr 分支的 `domain/model/herdr/remote.go` 已经给出可复用的固定形状：

- `UnixProbeScript`：unset `HC_SESSION HC_SOCKET HC_SSH HERDR_CORE_DIRS`，依次尝试 `command -v herdr-core`、`command -v hc`、`$HOME/.local/bin/herdr-core`、`$HOME/.local/bin/hc`，只接受绝对可执行路径，`--version | grep -m1 "^hc "`，打印路径 + 版本；`found` 但不可执行 → 126，什么都没有 → 127。
- `UnixConnectScript`：`unset ...; exec "$1" connect`；路径作为 `sh -c '...' sh <path>` 的位置参数。
- `WindowsProbeCommand`：`cmd /d /c "set HC_SESSION=&...&if exist .\AppData\Local\Programs\herdr-core\herdr-core.exe (echo ...&... --version) else if exist ...\hc.exe (...) else (exit /b 127)"`；cmd 与 PowerShell 都能读同一行，`.\` 指向 SSH 会话的 home，绕开 PATH。
- `WindowsConnectCommand`：只接受 `WindowsCorePath` / `WindowsHCPath` 两个字面常量，其他路径拒绝；命令里只出现固定路径。
- 路径校验：`^/[A-Za-z0-9._+@%/-]+$`，拒绝 `.` / `..` 段。

Mux Sync 的 per-session 名字需要额外处理：现有 `UnixConnectScript` / `WindowsConnectCommand` 没有带 session。建议：

- Unix：`sh -c '<scrub>; exec "$1" connect --session "$2"' sh /abs/herdr-core NAME`（或 `HC_SESSION="$2" exec "$1" connect`）；`--session` 的可用性由第一方 Windows 模板佐证，Unix 的这两种形式都未运行验证。
- Windows：`cmd /d /c "set HC_SESSION=&...&<fixed path> connect --session "%~2""` 之类的写法涉及 cmd 引号规则，标 UNVERIFIED；更稳妥的是把名字放进 SSH 进程环境并让远端 `HC_SESSION` 生效，但 `set HC_SESSION=` 会清掉它，二者矛盾。实现前必须单独验证一个模板，不要直接拼接。

会话名校验：1–32 个 `[a-z0-9._-]`，且不能长得像终端 id（`t7`），与 `check_name()` 一致（[addr.rs#L5-L17](https://github.com/herdrdev/herdr-core/blob/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a/crates/herdr-core/src/addr.rs#L5-L17)、[api.rs#L614-L624](https://github.com/herdrdev/herdr-core/blob/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a/crates/herdr-core/src/api.rs#L614-L624)）。

### 4.5 环境清理

| 变量 | 作用 | 处理 |
|---|---|---|
| `HC_SESSION` | 选择 / 命名 core 会话 | unset（远端模板内） |
| `HC_SOCKET` | 重定向 core socket | unset（关键） |
| `HC_SSH` | 替换机器拨号使用的 ssh 命令（可指向任意可执行文件） | unset（关键） |
| `HERDR_CORE_DIRS` | 重定向本地发现目录（优先级高于 PATH 与登录 shell） | unset / 不继承 |

来源：herdr 分支的 `docs/research/herdr-hc-discovery.md` §7 与 `docs/audit/herdr-security.md`。`--via` 接受用户提供的本地 shell 行，属于用户代码执行，不要用于 Mux Sync。

### 4.6 超时与错误码

| 操作 | 超时 | 依据 |
|---|---|---|
| 本地 / 远端 `--version` | 5 秒 / 10 秒总预算 | 与 §1 统一 |
| core 自启动等待 | 5 秒（10ms 轮询） | core 自身行为 |
| `hello` | 5 秒 | 握手 |
| 普通 RPC（`terminal.list` / `terminal.run` / `terminal.rm`） | 10 秒 | herdr-app 的普通 call |
| `terminal.kill` | 10 秒（覆盖内部 3 秒） | 500+500+2000ms |
| `terminal.run` spawn | 10 秒 | `HC_SPAWN_MS` |
| 事件 heartbeat | 15 秒，不作为 liveness 超时 | 仅队列为空时发送 |

| code | 触发 |
|---|---|
| `herdr_not_installed` | 127 / 无绝对路径 |
| `herdr_not_executable` | 126 / `--version` 不以 `hc ` 开头 |
| `herdr_unsupported_version` | < 0.1.11 |
| `unsupported_generation` | hello.generation ≠ 1（-32000） |
| `herdr_unauthorized` | named pipe / UDS 权限拒绝（本地） |
| `herdr_not_found` | `terminal.kill` 后记录已被 rm 删除 |
| `herdr_needs_framed` | 未 framed 就调 `terminal.attach` |
| `herdr_timeout` | 上述超时 |
| `herdr_remote_windows_unsupported` | 远端 Windows 但固定模板不可用 / PowerShell 默认 shell 下模板失败 |

## 5. 信任边界、日志与注入面

### 5.1 信任模型

- **本机：** 同一 OS 用户。zellij Unix 有 0700 目录 + 01700 socket；tmux socket 目录由 server 以用户私有权限创建；herdr 本机 UDS 目录 0700、Windows named pipe 使用 ACL。**没有应用层认证**：同一用户下的任何进程都可以连接、attach、kill；Mux Sync 不能声称“只有 Netcatty 能管理这些会话”。
- **远端：** 只有 SSH 账户边界。任何以同一账户登录的进程都等价于完全权限；多用户主机上会话按 OS 用户隔离，不按 Netcatty 用户隔离。
- **网络：** 三个 backend 都不监听网络端口。zellij web 是唯一例外，必须显式禁止（§2.8）。不要把 Mux Sync 做成“转发 socket 到网络”的功能；那会越过本报告的信任边界，需要单独的安全评审。
- **Windows 本机：** zellij / herdr 的 named pipe 依赖默认安全描述符，不是显式的 per-user ACL；同机其他标准用户的可达性 UNVERIFIED。若产品要支持多用户 Windows，需要单独验证并在 UI 说明。

### 5.2 日志脱敏

- zellij 日志：Unix `/tmp/zellij-<uid>/zellij-log/zellij.log`（目录 0700 / 文件 0600），Windows `%TEMP%\zellij\zellij-log\zellij.log`；16 MiB 轮转保留一份 `.old.1`。内容可能包含配置、路径、会话名、插件 stderr。**不要上传、不要原样展示**；只允许截取末尾 N 行并做正则脱敏（绝对路径、用户名、token、环境变量值、终端控制序列）。
- tmux stderr：可能包含 session 名、socket 路径、旧 server 协议错误。按行截断 + 脱敏。
- herdr `core.log` / `hc` stderr：可能包含命令与环境；同样处理。
- 应用自身日志：session 名可以记录（它们是 Netcatty 生成的），但远端路径、SSH 目标、环境变量值、任何 `*token*` 键不得进入日志。保持现有的结构化日志字段约定。

### 5.3 注入面清单

1. **session 名**：严格字符集 + 位置参数；zellij 额外拒绝 URL 前缀；herdr 额外拒绝 `t7` 形状。绝不把名字拼进 `sh -c` 字符串。
2. **可执行文件路径**：只接受探测输出中的绝对路径，且通过 `[A-Za-z0-9._+@%/-]` 校验、拒绝 `.` / `..`；Windows 只接受两个字面常量。用户提供的 override 走 `$1`，不拼字符串。
3. **环境**：见各 backend 的清理表。特别是 `ZELLIJ_SOCKET_DIR` / `TMUX_TMPDIR` / `HC_SOCKET` 能把“管理会话”变成“连接攻击者指定的 socket”。
4. **stderr / stdout 解析**：不执行、不 eval；只做行扫描与已知 token 匹配。`where.exe` / PowerShell 的输出不进入下一条命令。
5. **远端 shell**：默认 shell 可能是 PowerShell / cmd；固定模板必须两种都能读（herdr 的 `cmd /d /c` + `.\` 形状），或者显式失败，不做“猜 shell”。
6. **zellij web / remote attach**：拒绝 URL 形式的 session 名；不运行 `zellij web`；创建时覆盖 `web_server=false`。

## 6. 打包与许可

| 组件 | 许可证 | 结论 |
|---|---|---|
| zellij | MIT | 与宿主 GPL-3.0-or-later 兼容；当前不捆绑 |
| tmux | ISC | 与 GPL 兼容；当前不捆绑 |
| herdr-core | Apache-2.0 | 与 GPL-3.0-or-later 兼容；若未来捆绑需携带 LICENSE + NOTICE |

- 宿主应用是 GPL-3.0-or-later（ADR-0014）。v1 只做“检测用户已安装的二进制”，不自动安装、不下载、不捆绑、不随安装包分发；因此不需要在安装包中加入三者的二进制或源码。
- 既有 `docs/packaging.md` 的八个 release asset 与 notices / source offer 机制不变；Mux Sync 不新增 release asset。
- 若未来要捆绑或自动安装任一 backend：必须先按 ADR-0014 复审，补齐许可证文本、NOTICE（Apache-2.0）、源码获取方式与更新 / 签名策略；同时重新评估 Windows 安装目录、PATH 修改与卸载行为。zellij MSI 自己会写 PATH，捆绑会与用户现有安装冲突。
- 不做版本自动升级：升级 zellij 可能改变 contract / 会话可见性；升级 tmux 可能让旧 server 残留；升级 herdr 可能改变 generation。只在 UI 提示“已安装版本”，由用户决定。

## 7. UNVERIFIED / 未决

以下项目没有运行验证（本任务禁止安装或运行 tmux / zellij / herdr），实现前必须单独验证：

1. **zellij `attach NAME options --web-server false` 的运行时解析**：源码上 `SessionCommand::Options(Options)` 支持 `--web-server`，但 `attach` 的位置参数 + 子命令组合未实际执行。
2. **zellij 远端 Windows** 经 Windows OpenSSH（cmd / PowerShell 默认 shell、无 PTY）的 `--version`、`list-sessions`、attach 行为；ConPTY 与 named pipe 跨 sshd 会话的表现。
3. **zellij Windows named pipe 的 ACL**：是否默认只允许当前用户 + SYSTEM；多用户 Windows 的可达性。
4. **zellij 0.44.0 与 0.45.1 混跑**时 per-tab sizing 的降级表现，以及 `session_info` 清理差异。
5. **zellij 0.44.0 之前的 socket 路径**（本报告只确认 0.44.0 起使用 contract 目录）。
6. **zellij attach 的运行期退出码**（干净 detach、被其他客户端 kill、服务器崩溃、SIGTERM）。
7. **zellij `setup --check` 配置错误的精确退出码**（源码路径确认非零，未逐一核对码值）。
8. **tmux control mode 在无 TTY exec 通道上的客户端尺寸**，以及它是否会把 PTY attach 压到 80x24；这是 v1 不用 control mode 的原因，但需要实测确认。
9. **tmux 2.1 老版本经 SSH 的 `-C` 通知行为**；本报告只做了源码 / 手册核对。
10. **tmux on MSYS2 / Cygwin** 在 Windows OpenSSH（无 POSIX TTY）或 MSYS2 sshd（有 PTY）下的可用性；两者都未运行验证。
11. **herdr 远端 `--session` 的 Unix / Windows 模板**：第一方模板使用 `HC_SESSION` 环境变量或 `--session`，但与本仓库 `remote.go` 的 unset 顺序组合后未验证；cmd 下 `%~2` 的引号规则尤其需要单测 / 实机验证。
12. **herdr 在 PowerShell 默认 shell 下的远端探测**：既有 audit 指出这是已知缺口；`cmd /d /c` 行在 PowerShell 中可读，但 `set` / `exit /b` 的边界未实测。
13. **所有 backend 的 attach 首帧 15 秒阈值**：这是建议值，不是源码常量；需要按真实网络 / 慢速 shell 校准。
14. **本机 tmux 的技术可行性**：只作为事实陈述，不是产品承诺；Q3 已排除。

## 8. 来源

zellij：

- [v0.45.1 release](https://github.com/zellij-org/zellij/releases/tag/v0.45.1)、[v0.45.0 release](https://github.com/zellij-org/zellij/releases/tag/v0.45.0)、[v0.44.0 release](https://github.com/zellij-org/zellij/releases/tag/v0.44.0)
- [v0.45.1 consts.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/consts.rs)、[sessions.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/sessions.rs)、[cli.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/cli.rs)、[setup.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/setup.rs)、[envs.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/envs.rs)、[options.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/input/options.rs)、[logging.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/logging.rs)、[shared.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-utils/src/shared.rs)
- [v0.45.1 src/main.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/src/main.rs)、[src/commands.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/src/commands.rs)、[zellij-client/src/lib.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-client/src/lib.rs)、[zellij-client/src/cli_client.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-client/src/cli_client.rs)、[zellij-server/src/lib.rs](https://github.com/zellij-org/zellij/blob/v0.45.1/zellij-server/src/lib.rs)
- [v0.44.0 consts.rs](https://github.com/zellij-org/zellij/blob/v0.44.0/zellij-utils/src/consts.rs)

tmux：

- [tmux CHANGES](https://github.com/tmux/tmux/blob/master/CHANGES)、[tmux.1](https://github.com/tmux/tmux/blob/master/tmux.1)、[client.c](https://github.com/tmux/tmux/blob/master/client.c)、[server.c](https://github.com/tmux/tmux/blob/master/server.c)、[tmux.c](https://github.com/tmux/tmux/blob/master/tmux.c)
- [MSYS2 tmux package](https://packages.msys2.org/packages/tmux)、[Cygwin tmux package](https://cygwin.com/packages/summary/tmux.html)

herdr：

- [herdr-core（pin c6b316f）](https://github.com/herdrdev/herdr-core/tree/c6b316f87c33e6f2ab1a1ceea3e3461e2b6bb61a)、[herdr-app（pin e77f112）](https://github.com/herdrdev/herdr-app/tree/e77f112ac2ea136b84fe510efa42d41163bfd776)
- 仓库内（herdr 分支）：`docs/research/herdr-hc-discovery.md`、`docs/research/herdr-protocol-details.md`、`docs/audit/herdr-security.md`

仓库内其余实现：

- `domain/model/tmux/tmux.go`、`application/sysmanager/commands.go`、`app/session_factory.go`
- `docs/adr/0017-ssh-tmux-auto-attach.md`、`docs/adr/0014-host-app-licensing-and-distribution.md`、`docs/packaging.md`
- herdr 分支：`domain/model/herdr/remote.go`、`domain/model/herdr/version.go`、`domain/model/herdr/name.go`
