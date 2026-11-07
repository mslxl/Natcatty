# Tailscale 与 WireGuard 连接方式集成研究

Status: research / design proposal  
Date: 2026-11-04  
Scope: 评估在 Netcatty Native（`github.com/mslxl/natcatty`）中集成 Tailscale 与
WireGuard 连接方式的可行路径，覆盖 SSH、jump chain、代理、mosh UDP、ET、telnet，
并给出对现有架构的接入点、许可与安全影响、方案对比和实施顺序。

Sources verified against:

- 本仓库分支 `research-tailscale-wireguard-integration`，commit `2c75dcf86`；
  `go.mod` 声明 `go 1.27.1`，CI 构建与发布使用 `CGO_ENABLED=0`
  （`.github/workflows/ci.yml:32`、`.github/workflows/release.yml:31`；race
  测试作业例外，`.github/workflows/ci.yml:111-112`）。
- Tailscale Go module **v1.104.5**（2026-11-02 发布，`goproxy.cn` 元数据；
  源码核对使用本机 module cache `tailscale.com@v1.104.5`）。Tailscale `main`
  的 `VERSION.txt` 在检索日为 `1.105.0`，仅用于对照，不作为实现基线。
- Tailscale 官方文档：`tsnet`（Last validated 2026-07-24）、`tsnet.Server`
  （2026-07-07）、userspace networking（2025-11-12）、auth keys、Tailscale SSH、
  Tailscale Serve（2026-01-20）。
- `wireguard-go` 模块 **v0.0.0-20261024220952-b3bc704ef0ee**（2026-10-24，
  `goproxy.cn`），源码取自本机 module cache，并用 `git.zx2c4.com/wireguard-go`
  的 plain 文件交叉核对关键 API；依赖
  `gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c`。
- `wgctrl` 最新模块 `v0.0.0-20241231184526-a9ab2273dd10`，仅作备选评估。
- Headscale `main` README、`binaricat/Netcatty` `main` 源码归档，均于 2026-11-04
  下载核对。
- 本地实测环境：Windows 11、Go 1.27.1、`CGO_ENABLED=0`；构建探针于 2026-11-04。
  `pkg.go.dev` 与部分官网域名在本次网络环境中不可直接访问，API 以源码为准；
  未能核对的项集中列在第 9 节。

---

## 1. 结论摘要

**可以集成，而且有三条彼此独立的路线：**

1. **零代码（推荐立即可用）**：把 Tailscale 或 WireGuard 作为 OS 级 VPN 使用，
   本软件继续用普通 `net.Dial` 连接 tailnet IP / WireGuard IP。所有传输都能工作，
   包括 mosh UDP。前提是用户安装并登录/启用外部客户端。
2. **零代码 + 现有代理配置（Tailscale 特有）**：`tailscaled --tun=userspace-networking`
   暴露本地 SOCKS5/HTTP CONNECT，保存为现有 `ProxySOCKS5` / `ProxyHTTP`
   Proxy Profile。SSH 与 ET 直接可用，jump chain 也可用；mosh 和 telnet 不可用。
3. **原生内嵌（需要开发）**：在进程内嵌 `tailscale.com/tsnet` 节点，或
   `wireguard-go` 的 `tun/netstack` 用户态 TCP/IP 栈。两者都不需要 root、都不需要
   cgo，许可均与 GPL-3.0-or-later 兼容；代价是依赖体积、生命周期、密钥/状态管理，
   以及 mosh 需要改造。

### 逐项结论

| 方案 | 结论 | 关键理由 |
|---|---|---|
| A1. OS 级 Tailscale / WireGuard | **今天就能用**，所有传输 | 系统路由透明；mosh 也走 UDP；无代码。需要用户安装客户端/管理员权限。 |
| A2. Tailscale userspace SOCKS5/HTTP 作为 Proxy Profile | **今天就能用**，SSH/ET/jump | 现有 `ProxySOCKS5`/`ProxyHTTP` 直连；SOCKS5 发送域名，MagicDNS 由 tailscaled 解析。mosh/telnet 不支持代理。 |
| A3. `tailscale nc %h %p` 作为 `ProxyCommand` | **今天就能用**，SSH/ET | 官方 CLI 的 stdin/stdout 隧道；仅 TCP，要求 CLI 在 PATH。 |
| B. 新增 `ProxyType`（内嵌 tsnet / WG） | **可行，但只覆盖第一跳** | 改动小，但无法与现有代理叠加；mosh 的 `CheckRoute` 仍会拒绝任何 `Proxy`。 |
| C. 新增 Network Provider 抽象（推荐的原生形态） | **推荐** | VPN 位于 Proxy/jump 之下，可覆盖 SSH、ET、telnet，并为 mosh UDP 留出接口；可同时承载 tsnet 与 WG netstack。 |
| D. 管理外部守护进程 / LocalAPI | **Tailscale 可行；WireGuard OS 管理不推荐** | Tailscale LocalAPI 有稳定 `UserDial`；WireGuard 的 `wgctrl` 不能创建接口/配地址且无 macOS 后端，需管理员。 |

### 推荐

- **立刻**：产品文档/UI 帮助中写明 A1、A2（以及 A3 作为兜底）的操作方式。
  这是零代码、覆盖最广、维护成本最低的路径。
- **原生第一步**：实现一个 **Network Provider** 抽象，先接 `tsnet`。不要让 VPN 成为
  第四个 `ProxyType`；否则无法与 HTTP/SOCKS5 代理组合，也无法自然承载 mosh 的 UDP。
- **原生第二步**：把 mosh 的 `udpLink` 从 `*net.UDPConn` 抽象为 datagram 接口，允许
  Network Provider 提供 UDP；把 `CheckRoute` 的“有代理/有跳板就拒绝”改成“有代理
  或无法通过 Provider 直连 UDP 才拒绝”。
- **原生第三步（可选）**：用同一个 Network Provider 接口接 `wireguard-go` 的
  `tun/netstack`，支持导入 WireGuard 配置。WireGuard 没有控制面，DNS、Endpoint
  解析、路由和密钥管理都要应用自己做，工作量明显高于 tsnet。
- **不建议**由应用管理 OS 级 WireGuard 接口；把这件事留给官方客户端更安全、更可移植。

### 已发现的硬约束与坑

| 事项 | 结论 |
|---|---|
| cgo | 无阻塞。`tsnet v1.104.5` 与 `wireguard-go netstack` 均在 `CGO_ENABLED=0` 下成功交叉编译 windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64。 |
| 许可 | 无阻塞。Tailscale BSD-3-Clause、wireguard-go MIT、gVisor Apache-2.0、wgctrl MIT、Headscale BSD-3-Clause，均与 GPL-3.0-or-later 兼容。 |
| Go 工具链 | **最新 Tailscale v1.104.5 的 `go.mod` 要求 Go >= 1.27.3**；本仓库当前是 `go 1.27.1`。需升级工具链，或固定到 v1.104.0（其 `go.mod` 为 1.27.1）。 |
| mosh UDP | `mosh` 目前把 socket 硬编码为 `*net.UDPConn` 并 `WriteToUDP`（`adapters/mosh/inprocess.go:192-202`），且 `CheckRoute` 拒绝任何代理（`adapters/mosh/mosh.go:562-572`）。原生 VPN 要覆盖 mosh，必须改这两处。 |
| WireGuard Endpoint | `wireguard-go` 的 UAPI `endpoint=` 只接受 `IP:port`（`conn/bind_std.go:90-98`），而 `.conf` 允许主机名（`wg.8`）。应用必须先解析并在网络变化时重解析。 |
| WireGuard DNS | `netstack.CreateNetTUN` 只使用传入的 `dnsServers`，通过隧道查询，**没有 OS resolver 回退**（`tun/netstack/tun.go:653-689`, `813-898`）。不配 DNS 就只能用 IP。 |
| WireGuard 模块布局 | 官方主模块与旧的独立 `golang.zx2c4.com/wireguard/tun/netstack` 子模块同时提供同名包，`go mod tidy` 会报 `ambiguous import`；需 `replace` 或 `exclude`。本次用 `replace` 实测通过。 |
| 体积 | 最小探针：tsnet 约 **28.7–30.9 MiB**；wireguard-go netstack 约 **8.2–8.8 MiB**；两者同时链接约 **31.4 MiB**（Windows/amd64）。这是探针值，不是本应用的精确增量。 |
| 权限 | 内嵌 tsnet / netstack **不需要 root/管理员**；OS 级 Tailscale/WireGuard 的安装与 TUN/驱动需要管理员。 |

---

## 2. 现状与接入点

### 2.1 本仓库已经具备的形态

- 唯一 SSH 传输是 `golang.org/x/crypto/ssh` v0.58.0（`go.mod:10`；
  `docs/adr/0002-xcrypto-ssh-as-sole-transport.md:1-8`）。
- 路由模型是 `Route{Jumps, Proxy, Algorithms}`；`Proxy.Type` 恰好三种：
  `ProxyHTTP`、`ProxySOCKS5`、`ProxyCommand`
  （`application/connection/route.go:53-75`, `80-87`）。
- `Dialer` 只有一个底层 TCP 缝：`DialFunc func(ctx, network, address) (net.Conn, error)`
  （`adapters/ssh/dialer.go:34-36`）；生产值是 `net.Dialer.DialContext`
  （`adapters/ssh/dialer.go:154`）。`Dial` 在第一个 endpoint 上调用
  `openFirst`，有代理则走 `dialProxy`，否则直接 `d.DialFunc`
  （`adapters/ssh/dialer.go:336-347`）；后续 hop 用 SSH `direct-tcpip`
  （`adapters/ssh/dialer.go:352-359`）。
- 代理握手在 `adapters/ssh/proxy.go`：HTTP CONNECT `136-157`，SOCKS5 `179-252`，
  ProxyCommand 进程封装 `51-75`；未知类型直接返回 `ErrUnsupportedProxy`
  （`31-46`）。
- 连接池按 endpoint、jump、proxy、算法、凭据摘要生成 key；proxy 的类型、host、
  port、command、username、password 全在 key 内（`application/connection/pool.go:137-190`）。
- 会话类型：SSH、local PTY、telnet、serial、mosh、ET。mosh 的纯 Go 客户端直接
  `net.ResolveUDPAddr` + `net.ListenUDP`（`adapters/mosh/inprocess.go:236-259`），并
  明确拒绝代理和 jump（`adapters/mosh/mosh.go:562-572`；`app/transports.go:105`）。
  ET 的 in-process 客户端把整条 route 交给 Connection Manager，通过 SSH
  `direct-tcpip` 到 `etserver`（`adapters/et/inprocess.go:229-270`）。
- telnet 有自己的 `Dial` 缝，但没有代理/route 支持
  （`adapters/telnet/telnet.go:33-40`, `78-84`）。
- 应用组装点：`app/app.go:503-505` 创建 `sshadapter.NewDialer(...)` 并交给 Manager；
  `app/app.go:714-721` 同时注册 mosh/ET 的 bundled 与 in-process 实现。
- 代理配置是 Vault 记录：`domain/vault/proxy.go:8-61` 定义 `ProxyProfile` 与校验；
  Host 侧 `ProxyConfig` 只有 `HasPassword`，密码进入字段密钥库
  （`domain/vault/host.go:126-151`, `234-237`）；解析在
  `application/vault/groups.go:244-298`、`app/session_factory.go:542-618`。
- 密钥库按 entity/field 做 AAD，字段名不可随意改
  （`application/vault/secrets.go:31-43`；`docs/adr/0010-field-secret-key-hierarchy.md:1-8`）。

### 2.2 原产品没有 Tailscale/WireGuard 功能

- 本仓库全树 grep `tailscale|wireguard|WireGuard|Tailscale|tsnet|wg-quick` 无命中
  （2026-11-04）。
- 上游 `binaricat/Netcatty` `main` 同样没有该功能：只有 Docker 镜像图标映射
  `tailscale`/`wireguard`（`domain/systemManager/dockerImageIcons.ts:126-127`,
  `domain/systemManager/dockerIconBundled.ts:153,169`），以及文档里泛指用户外部
  VPN 的故障描述（`docs/research/sftp-transfer-audit-2026-09.md:99,102`）。
  没有 Tailscale/WireGuard 依赖、UI 或连接实现。

结论：这是**新功能**，不是与 Electron 版的 parity 缺口。

---

## 3. Tailscale 集成方案

### 3.1 A1：OS 级 Tailscale（零代码，覆盖最广）

用户安装官方 Tailscale 并登录后，系统会创建 TUN 接口（Linux/macOS/Windows 各有
平台实现），应用程序照常用 `net.Dial` 连接：

- 目标写 tailnet IP（`100.64.0.0/10`）或 MagicDNS 名称；
- SSH、ET、telnet、mosh 全部透明工作，因为路由和 DNS 在 OS 层完成；
- 使用 subnet router / exit node 时，客户端需要接受对应路由（`--accept-routes`
  或 exit node 选择），这是 Tailscale 客户端行为，不是本应用代码。

Tailscale 官方把 tsnet 与系统守护进程描述为两种不同方式：系统 `tailscaled`
管理整机虚拟网卡，tsnet 在进程内运行独立节点
（[tsnet README:11-16](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md#L11-L16)）。
因此 A1 是“外部客户端负责连接，本应用只消费普通网络”的路径。

**优点**：零代码、全传输、DNS/路由/漫游由官方客户端处理。  
**限制**：用户必须安装并保持 Tailscale 登录；企业环境可能禁止；不走应用内
Proxy/route 展示；排障跨进程。

### 3.2 A2：`tailscaled --tun=userspace-networking` 的 SOCKS5/HTTP（零代码）

官方 userspace networking 模式让 `tailscaled` 在没有 TUN 的情况下，作为 SOCKS5
或 HTTP 代理供本机进程使用：

```shell
tailscaled --tun=userspace-networking --socks5-server=localhost:1055 --outbound-http-proxy-listen=localhost:1055 &
tailscale up --auth-key=<your-auth-key>
```

（[Userspace networking mode](https://tailscale.com/docs/concepts/userspace-networking#start-tailscale-in-userspace-networking-mode)）

关键事实：

- **没有内置默认端口**：两个 flag 的默认值都是空字符串，不传就不监听
  （`cmd/tailscaled/proxy.go:33-34`）。文档示例用 `1055`，这只是约定。
- 两个代理可以监听同一端口，`proxymux` 按首字节分流
  （`cmd/tailscaled/proxy.go:48-56`）。
- **HTTP 代理的源码支持 `CONNECT`**：handler 对 `r.Method == "CONNECT"` 会自行
  `dialer(..., "tcp", dst)`、劫持连接并双向拷贝
  （`cmd/tailscaled/proxy.go:115-174`），因此现有 `ProxyHTTP` 理论上也能用于 SSH。
  注意官方文档把该 HTTP 代理描述为“only proxies HTTP and HTTPS traffic”
  （[Userspace networking: SOCKS5 vs. HTTP](https://tailscale.com/docs/concepts/userspace-networking#socks5-vs-http)），
  源码行为与文档措辞不完全一致；这是推断可用，需实测（第 9 节）。
- **SOCKS5 的域名请求由 tailscaled 解析**：SOCKS5 server 的 `Dialer` 是
  `dialer.UserDial`（`cmd/tailscaled/proxy.go:97-104`）；`UserDial` 先查 MagicDNS
  映射，再走系统 resolver（`net/tsdial/tsdial.go:417-461`, `588-603`）。因此客户端只要把
  主机名放进 SOCKS5 请求，`MagicDNS` 短名和 tailnet FQDN 都能解析。
- **subnet router / exit node 的路径**：userspace dialer 维护 routes 表，命中
  Tailscale route 的地址走 peer dialer，否则走 system dial（`net/tsdial/tsdial.go:193-208`,
  `608-637`）；`wgengine/userspace.go:933-939` 在 router 配置变化时调用 `SetRoutes`。
  因此经该 dialer 的 SOCKS5/HTTP 请求可以复用 tailnet/subnet 路由；exit-node
  的公网地址经 netstack 处理（`UseNetstackForIP` 分支，`tsdial.go:609-616`），
  但 `tsdial.go:627-634` 对 macOS 上的 exit node/子网路由仍有 TODO。结论是
  “可行”，但需按平台实测（第 9 节）（推断）。
- **UDP**：SOCKS5 server 本身支持 UDP ASSOCIATE
  （`net/socks5/socks5.go:56`, `346-391`, `393-436`），但本应用没有 SOCKS5 UDP 客户端，
  所以这条路径不能直接服务 mosh。
- **平台**：官方文档以容器/Linux 为主；但源码中 macOS 的非 root 分支明确建议
  `--tun=userspace-networking`（`cmd/tailscaled/tailscaled.go:289-291`），Windows
  也用它绕开 `wintun.dll`（`cmd/tailscaled/tailscaled_windows.go:127`）。该 flag
  在帮助文本中仍标为 beta（`cmd/tailscaled/tailscaled.go:216`）。Windows/macOS
  的实际可用性需要实测（第 9 节）。

**在本应用中如何零代码落地**：

- 新建一个 `ProxySOCKS5` Proxy Profile，host `127.0.0.1`、port `1055`、无认证；
  在 Host 上选择它。SSH 与 ET 都经过 `adapters/ssh`，自动可用；jump chain 也
  可用（代理只作用于第一跳）。
- 关键前提已由本地代码满足：`socks5Connect` **总是把目标写成 domain-name
  ATYP `0x03`**（`adapters/ssh/proxy.go:210-217`），不会先在本地解析。因此
  MagicDNS 名称能原样交给 tailscaled。
- HTTP 路径同理：`httpConnect` 发送 `CONNECT host:port`（`adapters/ssh/proxy.go:136-157`），
  tailscaled 的 HTTP proxy 支持 CONNECT。
- **不覆盖**：mosh（`CheckRoute` 拒绝 `Proxy != nil`，`adapters/mosh/mosh.go:562-572`）
  和 telnet（无代理支持，`adapters/telnet/telnet.go:33-40`）。

### 3.3 A3：`tailscale nc %h %p` 作为 ProxyCommand（零代码）

Tailscale CLI 有官方 `nc` 子命令：

```
tailscale nc <hostname-or-IP> <port>
```

它把 stdin/stdout 接到 tailnet 连接（`cmd/tailscale/cli/nc.go:19-23`,
`47-84`）；当前仅 TCP，源码中还有 `TODO: also add UDP too`（`nc.go:68`）。

在本应用中保存一个 `ProxyCommand` Proxy Profile，命令写：

```
tailscale nc %h %p
```

`substituteProxyCommand` 会按平台 shell-quote `%h`/`%p`（`adapters/ssh/proxy.go:334-365`），
`process.Start` 通过 `cmd.exe /C` 或 `/bin/sh -c` 启动（`platform/process/process.go:20-49`,
`platform/process/shell_windows.go:10-18`, `shell_other.go:10-16`）。因此 SSH/ET 可用。

**限制**：要求 `tailscale` CLI 在 PATH（Windows GUI 进程的 PATH 可能不含安装目录）；
仅 TCP；仍要求系统 `tailscaled` 已运行并登录。

`tailscale serve` 不适合此用途：它的方向是“把本机服务发布给 tailnet”，不是
客户端出站隧道（[Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve)）。

### 3.4 B：内嵌 `tailscale.com/tsnet`

#### API 与生命周期

以 v1.104.5 源码为准（`tsnet/tsnet.go`）：

```go
func (s *Server) Dial(ctx context.Context, network, address string) (net.Conn, error)
func (s *Server) Up(ctx context.Context) (*ipnstate.Status, error)
func (s *Server) Listen(network, addr string) (net.Listener, error)
func (s *Server) ListenPacket(network, addr string) (net.PacketConn, error)
func (s *Server) LocalClient() (*local.Client, error)
func (s *Server) Close() error
```

- `Up` 返回 `*ipnstate.Status`，**不是 `*net.IPNet`**；其中包含
  `TailscaleIPs []netip.Addr`（`tsnet/tsnet.go:573-636`；
  `ipn/ipnstate/ipnstate.go:46-50`）。
- `Dial` 和 `Listen`/`ListenPacket` 会隐式调用 `Start`；
  `Dial` 内部是 `s.dialer.UserDial(ctx, network, address)`
  （`tsnet/tsnet.go:380-390`）。
- `Up` 会阻塞到 backend 进入 `Running`；源码明确 TODO 目前不会在
  `NeedsLogin`/`NeedsMachineAuth` 时返回错误（`tsnet/tsnet.go:573-636`）。
- `Close` 只能调用一次，且不能早于或并发于 `Start`（`tsnet/tsnet.go:641-651`）。

`Server` 的关键字段（`tsnet/tsnet.go:223-315`）：

- `Dir string`：状态目录；为空时在 `os.UserConfigDir` 下按二进制名选择。
  目录由 tsnet 以 0700 自动创建（`tsnet/tsnet.go:863`），但多个 `Server` 必须使用不同 `Dir`（`tsnet/tsnet.go:226-236`；
  [tsnet.Server 文档](https://tailscale.com/docs/reference/tsnet-server-api#serverdir)）。
- `Store ipn.StateStore`：自定义状态存储；nil 时使用 `Dir/tailscaled.state`。
- `Hostname string`：节点在 tailnet 中的名字；为空时用二进制名。
- `AuthKey string`：auth key；优先级依次为字段、`TS_AUTHKEY`、`TS_AUTH_KEY`
  （[tsnet README:38-58](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md#L38-L58)）。
- `Ephemeral bool`：临时节点，离线后由控制面清理。
- `ControlURL string`：自建控制面（如 Headscale）。
- `UserLogf` / `Logf`：面向用户的登录 URL/状态日志 vs. 调试日志。
- `AdvertiseTags []string`、`ClientSecret`、`ClientID`、`IDToken`、`Audience`、
  `RunWebClient`、`Port`、`Tun`。

#### 认证 UX

- **auth key**：最简单。支持 reusable、ephemeral、tag；有效期 1–90 天，
  默认 90 天；auth key 过期后设备仍可能凭 node key 继续有效
  （[Auth keys](https://tailscale.com/docs/features/access-control/auth-keys)）。
  适合存进 Vault 的字段密钥。
- **交互登录**：没有导出的 `ErrAuthURL`，也没有导出的 `LoginInteractive` 方法。
  首次无 key 且未注册时，tsnet 内部调用 `StartLoginInteractive` 并把 AuthURL
  打到 `UserLogf`（`tsnet/tsnet.go:1008-1017`, `1163-1200`）。应用可同时通过
  `LocalClient().Status(ctx)` / `WatchIPNBus` 读取 `ipnstate.Status.AuthURL`
  （`ipnstate/ipnstate.go:46-50`；`client/local/local.go:789-791`）。
  由于 `Up` 会一直等 `Running`，交互登录路径必须在 goroutine 中启动 `Up`，
  另开状态轮询把 URL 显示给用户。
- 一旦状态目录里已有注册信息，`AuthKey` 会被忽略，除非设置
  `TSNET_FORCE_LOGIN=1`（[tsnet README:58](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md#L58)）。
  因此“更换账号”不是改 key 就能完成，必须更换 `Dir` 或强制重新登录。

#### UDP、MagicDNS、exit node、subnet router

- **UDP 明确支持**：`Server.Dial(ctx, "udp", addr)` 走 `NetstackDialUDP`
  （`tsnet/tsnet.go` 的 `s.dialer.NetstackDialUDP` 回调），底层是
  `ns.DialContextUDPWithBind`；`ListenPacket` 明确接受 `udp`/`udp4`/`udp6`
  （`tsnet/tsnet.go:1399-1413`）。这是 mosh 未来可用的基础。
- **MagicDNS / FQDN**：`Dial` 使用 `tsdial.UserDial`，先查 MagicDNS 映射，再走
  系统 resolver（`net/tsdial/tsdial.go:417-461`, `588-603`）。tsnet README 也写明
  “tailnet DNS first, fallback to host resolver”
  （[README:109-113](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md#L109-L113)）。
  因此 `ssh myhost` 这类短名和 tailnet FQDN 都可以直接作为 Host 地址。
- **exit node**：可通过 `LocalClient().EditPrefs` 设置 `ExitNodeID`
  （`tsnet/tsnet.go:119-132`；[README:97-107](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md#L97-L107)）。
- **subnet router**：需要 `RouteAll`（等价于 `--accept-routes`）偏好；tsnet 的
  测试就是这样显式设置的（`tsnet/tsnet_test.go:1154-1158`, `2004-2011`）。
  没有这个偏好时，不能假设 subnet route 可用。

#### 状态目录与与系统 `tailscaled` 共存

- 状态目录至少包含 `Dir/tailscaled.state`（节点身份/状态）和
  `Dir/tailscaled.log.conf`；源码注释还说明日志会自动上传到
  `log.tailscale.com`（`tsnet/tsnet.go:241-243`）。这意味着
  `Dir` 是凭据材料，必须按密钥保护（目录权限 0700），且不应进入云同步。
- tsnet 在进程内运行自己的 `LocalBackend`，并创建内存 LocalAPI listener
  （`tsnet/tsnet.go:979-1034`）；它**不连接系统 `tailscaled` 的 LocalAPI**。
  因此内嵌节点和系统客户端是**两个独立设备/身份**，可以同时存在于同一 tailnet；
  只要 `Dir` 不同，就不会有状态冲突。若把 `Dir` 指向系统 tailscaled 的状态目录，
  则属于未定义/不支持用法，且可能破坏系统客户端。
- 如果不想出现“第二个设备”，应走 D（使用系统 daemon 的 LocalAPI），而不是 tsnet。

#### 平台、cgo、体积、许可

- **平台/cgo**：本地实测在 `CGO_ENABLED=0` 下成功构建
  windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64；
  使用 tsnet v1.104.5。Windows 二进制 `go version -m` 显示链接了
  `github.com/tailscale/wireguard-go`、`golang.zx2c4.com/wireguard/windows`、
  `golang.zx2c4.com/wintun` 和 `gvisor.dev/gvisor`，但默认 netstack 路径不创建
  OS TUN，因此不需要管理员。
- **最小探针体积**（`go build -trimpath`，Go 1.27.1/1.27.3，`CGO_ENABLED=0`；
  实测，不是官方数字）：

  | 目标 | tsnet 探针 | wireguard-go netstack 探针 | 两者同链 |
  |---|---:|---:|---:|
  | windows/amd64 | 32,372,224 B (30.9 MiB) | 9,199,616 B (8.8 MiB) | 32,899,072 B (31.4 MiB) |
  | linux/amd64 | 32,374,517 B (30.9 MiB) | 9,005,400 B (8.6 MiB) | — |
  | linux/arm64 | 30,146,180 B (28.7 MiB) | 8,588,902 B (8.2 MiB) | — |
  | darwin/amd64 | 31,912,368 B (30.4 MiB) | 9,018,784 B (8.6 MiB) | — |
  | darwin/arm64 | 30,046,866 B (28.7 MiB) | 8,600,850 B (8.2 MiB) | — |

  探针是最小程序，增量不能直接线性加到现有应用上；但可以确定“tsnet 是几十 MiB
  量级”的依赖。官方没有给出 tsnet 的总体二进制体积承诺；README 只说明
  workload identity federation 默认不链接，以避免把 AWS SDK 带进来
  （[README:48-54](https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md#L48-L54)）。
- **依赖数量**：tsnet 探针 `go version -m` 列出 36 个链接模块；wireguard-go
  netstack 探针 8 个。tsnet 的 `go.mod` 要求 Go **1.27.3**；本仓库当前
  `go 1.27.1`（`tailscale.com@v1.104.5/go.mod:3`）。v1.104.0 的 `go.mod` 是
  1.27.1，可作为不升工具链的退路，但会错过后续安全修复。
- **许可**：`tsnet/tsnet.go:1-2` 是 `BSD-3-Clause`；Tailscale 整体的依赖中
  gVisor 是 Apache-2.0，Tailscale 的 wireguard-go fork 是 MIT（`go version -m`
  证据 + 其 `LICENSE`）。GNU 认定 modified BSD 与 Apache-2.0 均与 GPLv3 兼容
  （[GNU license list: Modified BSD](https://www.gnu.org/licenses/license-list.html#ModifiedBSD)，
  [Apache-2.0](https://www.gnu.org/licenses/license-list.html#apache2)）。

### 3.5 D：使用系统 `tailscaled` 的 LocalAPI

`tailscale.com/client/local` 提供稳定的 LocalAPI 客户端：

- `Status`（`client/local/local.go:786-791`）；
- `EditPrefs`（`1005-1008`）；
- `DialTCP`，内部是 `UserDial(ctx, "tcp", ...)`（`1097-1099`）；
- `UserDial(ctx, network, host, port)`，host 可以是短名、FQDN 或 IP
  （`1110-1133`）。LocalAPI 的 `/localapi/v0/dial` handler 支持 `Dial-Network`
  参数，并把 `network` 原样交给 `UserDial`（`ipn/localapi/localapi.go:1304-1351`）。

**适用场景**：用户已经运行系统 Tailscale，应用只想复用其身份/ACL，不想再注册
一个设备。应用需要读取平台 socket（Unix 的 `tailscaled.sock`、Windows 的命名管道），
并处理权限：Unix socket 在无 peer credential 的平台是 0600/root
（`safesocket/unixsocket.go:84-110`）；Windows 默认命名管道在
`\\.\pipe\ProtectedPrefix\Administrators\...` 下（`safesocket/pipe_windows.go:161`）。
因此“普通用户的应用直接连系统 daemon”在某些平台上可能需要管理员/operator 配置。

**LocalAPI 不适合 mosh UDP**（推断）：`/localapi/v0/dial` 的返回是 HTTP upgrade
后的字节流，服务端用 `io.Copy` 双向搬运（`ipn/localapi/localapi.go:1371-1376`）；
UDP 数据报边界在字节流上不保证保留。用它承载 mosh 需要实测，不能假设可行。

**优点**：不增加 Go 依赖体积、不新增节点、复用用户已有登录与 ACL。  
**缺点**：强依赖外部安装、版本偏差、socket 权限、平台路径差异；无法控制节点
生命周期；不适合“应用自带 VPN”的 UX。

### 3.6 Tailscale SSH：相关但不是连接层

Tailscale SSH 是**目标主机上的 SSH 服务器**：`tailscale set --ssh` 让 tailscaled
接管 tailnet 到 22 端口的流量，由 tailnet ACL 做认证/授权，SSH 客户端仍建立
SSH 连接，但认证阶段服务端已知道对端身份，客户端通常走 `none` 认证
（[Tailscale SSH](https://tailscale.com/docs/features/tailscale-ssh) 的
“SSH as normal”“Tailscale takes over port 22”“SSH authentication type none”）。

对本应用的含义：

- 一旦网络层到达 tailnet，Tailscale SSH 只是 22 端口的普通 SSH 服务；现有
  `x/crypto/ssh` 客户端会在认证开始时先尝试 `none`
  （`golang.org/x/crypto@v0.58.0/ssh/client_auth.go:72-79`, `170-186`），
  因此技术上可能直接工作。
- 但本应用的认证模型要求配置密码/密钥/agent，且 host key 必须经过
  `VerifyHostKey` 回调（`adapters/ssh/dialer.go:397-419`）。Tailscale SSH 的
  主机密钥由控制面分发，仍需用户信任；ACL 也可能要求 re-auth/check。
- **它不解决“如何到达 tailnet”**，不是本问题的方案；不建议把它作为集成主线，
  最多在文档中说明“Tailscale SSH 目标可用普通 SSH 方式连接”。

### 3.7 Headscale（自建控制面）

- Headscale 的定位是“self-hosted, open source alternative to the Tailscale
  control server”，实现单一 tailnet 的控制面
  （[Headscale README:37-43](https://github.com/juanfont/headscale#design-goal)）。
- tsnet 的 `ControlURL` 明确支持指向自建控制面
  （`tsnet/tsnet.go:311-315`；[tsnet.Server 文档](https://tailscale.com/docs/reference/tsnet-server-api#servercontrolurl)）。
- Headscale 的客户端支持页存在（[Client OS support](https://headscale.net/stable/about/clients/)），
  但**没有找到 Headscale 官方对 tsnet 这一具体客户端的兼容性承诺**。
  从协议与 `ControlURL` 看应当可行，但必须用目标 Headscale 版本实测（第 9 节）。
  另外，Tailscale SSH、Serve、Funnel、Services 等控制面特性在 Headscale 上
  可能不完整；本方案的连接层只需 Dial/DNS，不依赖这些特性（推断）。
- 许可：BSD-3-Clause（`headscale/LICENSE:1-5`）。

---

## 4. WireGuard 集成方案

### 4.1 A：OS 级 WireGuard（零代码，覆盖最广）

用户用官方客户端导入配置并启用接口后，应用照常拨号 WireGuard 对端 IP：

- **Linux**：内核模块（wireguard-go README 明确建议优先用 kernel module，
  `README.md:27-29`）；`wg-quick up wg0` 创建接口、配地址/MTU/路由/DNS
  （`wg-quick.8:24-36`, `64-104`）。需要 root/CAP_NET_ADMIN。
- **Windows**：官方 WireGuard for Windows 使用 WireGuardNT 驱动，安装器/MSI
  负责驱动与服务（[wireguard-windows README:1-10](https://git.zx2c4.com/wireguard-windows/plain/README.md)）。
  安装需要管理员；之后应用只是普通拨号。
- **macOS**：官方 app 使用 Network Extension + `wireguard-go-bridge`
  （[wireguard-apple README:1-3, 35-75](https://git.zx2c4.com/wireguard-apple/plain/README.md)）。
  安装/启用扩展需要系统批准；之后应用只是普通拨号。
- **配置格式**（`wg(8)` / `wg-quick(8)`）：
  - `[Interface]`：`PrivateKey`、`ListenPort`、`FwMark`（`wg.8:131-146`）；
    `wg-quick` 额外支持 `Address`、`DNS`、`MTU`、`Table`、`PreUp/PostUp/...`
    （`wg-quick.8:64-104`）。
  - `[Peer]`：`PublicKey`、`PresharedKey`、`AllowedIPs`、`Endpoint`、
    `PersistentKeepalive`（`wg.8:147-176`）。
  - `AllowedIPs` 同时决定“允许该 peer 的入站来源”和“出站目标走哪个 peer”
    （`wg.8:158-163`）；`wg-quick` 会据此安装系统路由（`wg-quick.8:64-68`）。
  - `Endpoint` 支持主机名，且会在收到合法认证包后自动更新为最新源地址
    （`wg.8:165-167`）；`PersistentKeepalive` 用于保持 NAT 映射（`wg.8:169-176`）。

**对本应用**：如果接口已启用且 `AllowedIPs` 覆盖目标，SSH、ET、telnet、mosh
全部透明可用；DNS 取决于用户在 `[Interface] DNS=` 或客户端里是否配置。
这是 WireGuard 用户今天最稳妥的用法。

### 4.2 B：内嵌 `wireguard-go` + `tun/netstack`

#### 模块与许可

- 模块路径 `golang.zx2c4.com/wireguard`，本次核对版本
  `v0.0.0-20261024220952-b3bc704ef0ee`（2026-10-24）。
- `go.mod` 要求 `go 1.23.1`，直接依赖 `golang.org/x/crypto`、`x/net`、`x/sys`、
  `golang.zx2c4.com/wintun`、`gvisor.dev/gvisor`
  （`wireguard-go/go.mod:1-10`）。
- 许可 MIT（`wireguard-go/LICENSE:1-5`）。
- `gvisor.dev/gvisor` 是 Apache-2.0（`google/gvisor/LICENSE`），与 GPLv3 兼容
  （[GNU license list: Apache-2.0](https://www.gnu.org/licenses/license-list.html#apache2)）。

#### `tun/netstack` API

```go
func CreateNetTUN(localAddresses, dnsServers []netip.Addr, mtu int) (tun.Device, *Net, error)
func (tnet *Net) DialContext(ctx context.Context, network, address string) (net.Conn, error)
func (tnet *Net) Dial(network, address string) (net.Conn, error)
func (tnet *Net) DialUDPAddrPort(laddr, raddr netip.AddrPort) (*gonet.UDPConn, error)
func (tnet *Net) ListenUDPAddrPort(laddr netip.AddrPort) (*gonet.UDPConn, error)
func (tnet *Net) LookupHost(host string) ([]string, error)
```

- `CreateNetTUN` 创建的是 **gVisor channel endpoint + 用户态协议栈**，不是 OS TUN：
  它把 `localAddresses` 加到 NIC，并为 v4/v6 各加一条默认路由
  （`tun/netstack/tun.go:55-108`）。
- `Net.DialContext` 接受 `tcp`/`tcp4`/`tcp6`、`udp`/`udp4`/`udp6`、`ping`，
  内部先做 `LookupContextHost`，然后按网络类型调用
  `DialContextTCPAddrPort` / `DialUDPAddrPort` / `DialPingAddr`
  （`tun/netstack/tun.go:957-1053`）。
- `Dial`、`DialUDP`/`DialUDPAddrPort`、`ListenUDP`/`ListenUDPAddrPort` 都在同一文件
  （`tun/netstack/tun.go:247-280`）；**UDP 不是限制**。
- `LookupHost` 使用 `CreateNetTUN` 传入的 `dnsServers`，通过隧道内 UDP 53
  （失败再试 TCP 53）查询；没有 OS resolver 回退
  （`tun/netstack/tun.go:653-689`, `813-898`）。
- `Net` 的 `tun.Device.File()` 返回 `nil`（`tun/netstack/tun.go:115-117`），
  进一步证明它不打开 `/dev/net/tun` 或系统驱动。

#### 最小装配顺序（官方示例）

`tun/netstack/examples/http_client.go:22-45` 是上游的规范示例：

```go
tun, tnet, err := netstack.CreateNetTUN(
    []netip.Addr{netip.MustParseAddr("192.168.4.28")},
    []netip.Addr{netip.MustParseAddr("8.8.8.8")},
    1420)
dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(...))
dev.IpcSet(`private_key=...
public_key=...
allowed_ip=0.0.0.0/0
endpoint=127.0.0.1:58120
`)
dev.Up()
// tnet.DialContext / tnet.Dial
```

对应 API：`device.NewDevice(tunDevice, bind, logger)`（`device/device.go:284`）、
`device.IpcSet(string)`（`device/uapi.go:415-417`）、`Device.Up/Down`
（`device/device.go:209-215`）。

UAPI 支持的键包括 `private_key`、`public_key`、`preshared_key`、`allowed_ip`、
`replace_allowed_ips`、`endpoint`、`persistent_keepalive_interval`
（`device/uapi.go:199-403`）。

**两个必须注意的编码问题**：

1. `.conf` 的 `PrivateKey`/`PresharedKey` 是 base64（`wg.8:139,153`），而
   UAPI 的 `private_key` 用 hex（`device/uapi.go:199-203` 的
   `FromMaybeZeroHex`）。应用必须做 base64 → hex 转换。
2. `.conf` 的 `Endpoint` 可以是主机名（`wg.8:165-167`），但 `wireguard-go`
   默认 `StdNetBind.ParseEndpoint` 用 `netip.ParseAddrPort`，只接受 `IP:port`
   （`conn/bind_std.go:90-98`）。应用必须先解析主机名，并在网络变化/解析失败时
   重新解析；否则接口起来后永远连不上。

#### 路由与 DNS 语义

- `localAddresses` 必须是本端在 WireGuard 网络中的地址（例如 `10.7.0.2/32` 中的
  `10.7.0.2`）。`CreateNetTUN` 会把它们加到协议栈并安装默认路由
  （`tun/netstack/tun.go:79-105`）；如果传空，栈没有地址/路由，`Dial` 无法工作。
- 出站包先由 netstack 按默认路由送到 NIC，再由 wireguard-go 用 peer 的
  `AllowedIPs` 做 cryptokey routing：命中某个 peer 才加密发出，不命中通常被丢弃
  （`wg.8:158-163`）。因此：
  - 单个 peer + `AllowedIPs = 0.0.0.0/0, ::/0` 等价于全局隧道；
  - 多 peer / split tunnel 由 `AllowedIPs` 划分，应用只需正确填写，不必自己实现
    每包路由（推断，基于上述两处语义）；
  - 但“选哪个 peer/配置”仍是应用的产品逻辑。
- DNS 必须显式提供；`dnsServers` 为空时只能拨 IP（`tun/netstack/tun.go:813-898`）。
  若 DNS 服务器不在 `AllowedIPs` 内，查询也会失败。
- MTU 由调用者决定，示例用 1420（`examples/http_client.go:19-22`）；过大会导致
  分片/黑洞，应用应按配置或默认值设置。

#### 重连、漫游、keepalive

- WireGuard 协议本身会在收到合法认证包后把 peer endpoint 更新为最新源地址
  （`wg.8:165-167`），所以 NAT 漫游/对端换地址不需要应用处理。
- `PersistentKeepalive` 由配置决定，wireguard-go 负责发送（`wg.8:169-176`；
  UAPI `persistent_keepalive_interval`，`device/uapi.go:350-361`）。
- 应用仍需处理：Endpoint 主机名的重新解析、网络接口变化、设备关闭/重建、
  以及上层 SSH 会话的重连策略。macOS 上 wireguard-go 明确“不支持 sticky
  sockets”（`wireguard-go/README.md:31-33`），漫游体验可能弱于 Linux；
  需要实测。
- 状态/健康：`Device.IpcGet()` 可读取握手时间、rx/tx 等（`device/uapi.go:407-413`），
  可用于 UI 状态展示。

#### 权限、平台、cgo、体积

- **不需要管理员**：netstack 路径不创建 OS 接口，`File() == nil`
  （`tun/netstack/tun.go:115-117`）；`conn.NewDefaultBind()` 只开普通 UDP socket。
  这与默认 `tun.CreateTUN` 路径相反：后者创建系统 TUN 接口，需要 OS 权限
  （wireguard-go README 的用法是 `wireguard-go wg0` 创建接口，`README.md:7-19`）。
- **平台/cgo**：本地实测 `CGO_ENABLED=0` 下 windows/amd64、linux/amd64、
  linux/arm64、darwin/amd64、darwin/arm64 全部构建成功（wireguard-go
  v0.0.0-20261024220952-b3bc704ef0ee + 其 pinned gVisor）。
- **体积**：最小探针 8.2–8.8 MiB（见 3.4 表）；gVisor 源码约 21 MiB
  （module cache 实测）。没有官方“二进制增量”声明；本报告只给探针值。
- **模块歧义坑**：`go mod tidy` 会同时看到
  `golang.zx2c4.com/wireguard` 主模块内的 `tun/netstack` 和旧的独立模块
  `golang.zx2c4.com/wireguard/tun/netstack v0.0.0-20220703234212-c31a7b1ab478`，
  报：

  ```
  ambiguous import: found package golang.zx2c4.com/wireguard/tun/netstack
  in multiple modules
  ```

  本次实测的修法是固定主模块版本并加：

  ```
  replace golang.zx2c4.com/wireguard/tun/netstack => golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee
  ```

  加完后可正常构建。这个 `replace` 应写入项目 `go.mod` 并纳入依赖审计；
  不要依赖 `go mod tidy` 的自发选择。

#### 与 tsnet 同时链接

本地实测一个同时使用 tsnet 和 wireguard-go netstack 的 Windows/amd64 探针：
构建成功，产物 32,899,072 B；`go version -m` 同时列出
`github.com/tailscale/wireguard-go` 与 `golang.zx2c4.com/wireguard`，gVisor 由
MVS 统一到 Tailscale 要求的较新版本（`v0.0.0-20260915211658-a6f909f08a72`）。
**含义**：两者可以共存，但会带两套 WireGuard 实现；体积增量因为共享 gVisor
而远小于 9 MiB（本探针仅比纯 tsnet 多约 0.5 MiB），但代码面、审计面和依赖
更新面都会增加。若不需要同时支持，建议分批发布或做成构建变体。

### 4.3 D：管理外部 WireGuard 接口 / `wgctrl`

`wgctrl`（`golang.zx2c4.com/wireguard/wgctrl`）只能**配置已存在的 WireGuard
设备**：

- 支持 Linux（generic netlink）、FreeBSD/OpenBSD（ioctl）、Windows（ioctl）和
  userspace UAPI；**macOS 没有内核后端**；
- 明确说明“创建 WireGuard 设备、应用 IP 地址”不在其范围内
  （[wgctrl README](https://github.com/WireGuard/wgctrl-go#overview)）。
- MIT 许可。

因此：

- **Linux**：可用 `wgctrl` 配置内核 WireGuard，但创建接口/配地址/路由仍需
  `netlink`/`ip` 等操作和 root。
- **Windows**：需要与 WireGuard 服务/驱动交互，权限模型复杂。
- **macOS**：没有 `wgctrl` 后端，只能走 Network Extension 或 `wg-quick` 类工具。

结论：**不建议**由应用管理 OS WireGuard。把配置导入和接口生命周期交给官方
客户端；应用只在 A 路径上消费网络。

### 4.4 其他 Go 库的取舍

- **Tailscale 的 wireguard-go fork**（`github.com/tailscale/wireguard-go`）是
  tsnet 内部使用的 MIT fork（`go version -m` 证据；其 `LICENSE` 为 MIT）。
  如果同时使用 tsnet，再引官方 `golang.zx2c4.com/wireguard` 会造成两套实现；
  但官方上游是独立 WireGuard 集成的首选，不建议为了“少一个模块”改用 fork。
- `wgctrl` 只适合 D 路径，不适合内嵌数据面。
- 其他第三方 fork（如 sagernet 等）没有官方支持与长期维护承诺，不推荐。

---

## 5. 对现有传输的影响

### 5.1 SSH 与 jump chain

- **A1/A2/A3**：无需改代码。A2/A3 通过 `Proxy` 只作用于第一跳
  （`adapters/ssh/dialer.go:336-347`；`app/session_factory.go:534-587` 的
  “first jump's proxy wins”），jump chain 后续 hop 仍走 SSH `direct-tcpip`。
- **B/C**：
  - 若走 C（Network Provider），应把它放在 `DialFunc` 之下：`openFirst` 的直接
    dial 和“拨代理服务器”都经 Provider，代理握手仍在 `proxy.go` 完成。这样
    VPN + HTTP/SOCKS5 可以叠加。
  - 若走 B（新 `ProxyType`），`Route.Proxy` 只能有一个，无法再叠加现有代理；
    需要在 `application/connection/route.go:53-75`、`domain/vault/host.go:126-151`
    两处加类型，在 `adapters/ssh/proxy.go:35-45` 加 case，在
    `app/session_factory.go:589-618` 解析新字段，在 `pool.go:161-168` 把新字段
    纳入 key。要改的地方明确但分散。
- **池复用**：无论 B/C，都必须把 VPN profile 标识（以及其配置指纹）纳入
  `Request`/`Route` 与 `endpointFor` 的 key（`application/connection/pool.go:137-190`），
  否则同一个 SSH endpoint 在不同 VPN profile 下可能错误复用连接。

### 5.2 mosh（UDP）

这是原生集成的主要改造点：

- `CheckRoute` 目前一看到 `proxied` 就返回 `ErrProxyUnsupported`
  （`adapters/mosh/mosh.go:562-572`；`app/transports.go:105`）。
  如果 VPN 以 `ProxyType` 形式出现，mosh 会被直接拒绝。
- 探测端硬编码 `net.ResolveUDPAddr`、`net.ListenUDP`，并把 socket 存成
  `*net.UDPConn`（`adapters/mosh/inprocess.go:192-202`, `236-259`）。tsnet 的
  `Dial("udp")` 返回的是 `net.Conn`（connected UDP），wireguard-go netstack 的
  `DialUDP` 返回 `*gonet.UDPConn`；都不能直接塞进现有 `udpLink`。
- 需要的最小改造（推断）：
  1. 定义 `datagramLink`/`PacketDialer` 接口（`DialContext(ctx,"udp",addr) (net.Conn,error)`
     或 `net.PacketConn`），让 `udpLink` 用 `Read`/`Write` 而非 `WriteToUDP`；
  2. `probe` 通过 Provider 解析/拨号，而不是直接 `net.ResolveUDPAddr`；
  3. `CheckRoute` 区分“Proxy”和“Network Provider”：允许 Provider（如果它能
     提供 UDP），仍拒绝无法承载 UDP 的 HTTP/SOCKS5 代理；jump 的限制要重新
     评估（bootstrap 可以走 jump，但 UDP 必须由 Provider 或直连到达
     `mosh-server` 宣告的地址）。
- **A1/A OS WireGuard 天然覆盖 mosh**，因为 UDP 走系统路由，不需要改代码。

### 5.3 EternalTerminal

- in-process ET 把 `Jumps`/`Proxy` 整条 route 交给 `Connection.Manager`
  （`adapters/et/inprocess.go:229-270`），bootstrap 走 `OpenExec`，数据走
  `Forwarding().DialContext` 的 SSH `direct-tcpip`
  （`docs/adr/0016-pure-go-et-client.md:1-4`）。因此：
  - A2/A3 的 Proxy Profile 自动生效；
  - C 的 Provider 只要在 SSH 拨号层生效，ET 自动受益；
  - B 的新 ProxyType 也可用（ET 不涉及 UDP）。
- bundled ET 工厂仍保留 one-jump/no-proxy 检查（`app/transports.go:212`；
  `domain/model/et/et.go:253-259`），但它不是主路径；in-process 工厂是
  ADR 0016 选定的实现（`app/app.go:716-721` 同时注册两者，按实现选择）。
  集成时应以 in-process 路径为准。

### 5.4 telnet

- telnet 有自己的 `Dial` 缝（`adapters/telnet/telnet.go:33-40`, `78-84`），
  但不经过 Connection Manager，也没有 `Proxy` 字段。
- A1 的 OS 级 VPN 通过系统路由透明覆盖 telnet；A2/A3/B 都不覆盖 telnet
  （A2/A3 只是 SSH 代理，telnet 不经过它们）。
- C 的 Provider 可以注入 telnet Factory，使 telnet 也获得 VPN 连接；需要在
  `StartSpec` 或 Factory 上增加网络选择。B 的 ProxyType 不覆盖 telnet。

### 5.5 local PTY / serial

不涉及网络，完全不受影响。

### 5.6 各方案覆盖矩阵

| 传输 | A1 OS VPN | A2 Tailscale SOCKS5/HTTP | A3 `tailscale nc` | B ProxyType | C Provider | D LocalAPI / 外部 daemon |
|---|---|---|---|---|---|---|
| SSH | 是 | 是 | 是 | 是 | 是 | 是（TCP） |
| jump chain | 是 | 是（仅第一跳代理） | 是（仅第一跳） | 是（仅第一跳） | 是 | 是 |
| ET（in-process） | 是 | 是 | 是 | 是 | 是 | 是（TCP） |
| telnet | 是 | 否 | 否 | 否 | 是（需接线） | 是（TCP） |
| mosh（UDP） | 是 | 否 | 否 | 否（`CheckRoute`） | 是（需改造 `udpLink`） | 不确定（LocalAPI 字节流不适合，推断） |
| 组合现有 HTTP/SOCKS5 代理 | 是（OS 层叠加） | 是（VPN 即代理） | 是（VPN 即代理） | 否（单 Proxy） | 是 | 视实现 |

---

## 6. 安全与合规

### 6.1 密钥与状态存储

- **WireGuard**：`PrivateKey`（以及可选的 `PresharedKey`）就是身份。必须存入
  Vault 字段密钥，不得写入普通配置/日志。现有字段密钥模型按
  entity/field 做 AAD，字段名不可改（`docs/adr/0010-field-secret-key-hierarchy.md:1-8`；
  `application/vault/secrets.go:31-43`）。需要新增类似：
  - `EntityWireGuardProfile = "wireguard_profile"`，
  - `FieldWireGuardPrivateKey = "wireguard_private_key"`，
  - `FieldWireGuardPresharedKey = "wireguard_preshared_key"`。
  这些名字一旦持久化就属于 AAD/键的一部分，设计时要一次定好。
- **Tailscale**：
  - `AuthKey` / `ClientSecret` 是凭据，必须进 Vault；auth key 默认 90 天过期，
    reusable key 被盗风险高（[Auth keys](https://tailscale.com/docs/features/access-control/auth-keys)）。
    优先交互登录或 ephemeral/tagged key，避免长期 reusable key。
  - `Dir/tailscaled.state` 含节点身份/密钥；目录权限 0700，不应云同步或导出。
  - tsnet 源码注释称日志会自动上传到 `log.tailscale.com`，配置在
    `Dir/tailscaled.log.conf`（`tsnet/tsnet.go:241-243`）。企业/隐私敏感场景
    需要确认关闭方式；`Logf` 只控制本地调试日志，不等于关闭 logtail。
- **导出/同步**：现有 Vault 导出会显式处理 Proxy Profile 密码
  （`application/vault/export.go:262-279`）。新增 VPN 密钥必须明确决定是否导出：
  默认不应把 WG/Tailscale 私钥放进普通导出包；若支持“加密导出”，必须使用
  与现有字段密钥一致的 `enc:v1:` 语义（`application/vault/secrets.go:48`）。
- **日志**：不要记录 UAPI 字符串（其中含 `private_key`，
  `device/uapi.go:199-206`）；不要记录 Tailscale auth key、AuthURL 之外的完整
  状态文件内容。AuthURL 本身是短期登录凭据，展示给用户即可，不应写入持久日志
  （推断）。

### 6.2 许可与 GPL 兼容性

本应用是 GPL-3.0-or-later（`docs/adr/0014-host-app-licensing-and-distribution.md:1-17`）。
拟引入依赖的许可：

| 依赖 | 许可 | 与 GPLv3 兼容 | 备注 |
|---|---|---|---|
| `tailscale.com/tsnet` | BSD-3-Clause | 是 | 保留版权/许可声明；GNU 认定 modified BSD 兼容 GPL |
| `github.com/tailscale/wireguard-go`（tsnet 间接依赖） | MIT | 是 | 保留声明 |
| `gvisor.dev/gvisor` | Apache-2.0 | 是 | 需保留 LICENSE/NOTICE；GNU 明确与 GPLv3 兼容 |
| `golang.zx2c4.com/wireguard` | MIT | 是 | 保留声明 |
| `golang.zx2c4.com/wireguard/wgctrl` | MIT | 是 | 仅 D 路径候选 |
| Headscale（若作为外部服务） | BSD-3-Clause | 是 | 不链接进应用，单独分发 |

依据：[GNU license list: Apache-2.0](https://www.gnu.org/licenses/license-list.html#apache2)
（“compatible with version 3 of the GNU GPL”）；
[Modified BSD](https://www.gnu.org/licenses/license-list.html#ModifiedBSD)；
[Expat/MIT](https://www.gnu.org/licenses/license-list.html#Expat)。
这不是法律意见；发布前应把新依赖加入第三方 notices/SPDX 检查
（ADR 0014 已要求 per-release notices）。

### 6.3 供应链与维护

- Tailscale 迭代快、依赖多（探针链接 36 个模块），且 v1.104.x 在两个补丁版本内
  就把最低 Go 从 1.27.1 提到 1.27.3。必须固定版本、定期升级、跑
  `govulncheck`，不能跟 `main`。
- wireguard-go 的 gVisor pin 较旧（本次 wireguard-go go.mod 用 2025-05 的 gVisor），
  与 tsnet 的 2026-09 gVisor 合并时由 MVS 取高版本；本次实测能构建，但需要
  持续验证两者兼容性。
- 旧的 `golang.zx2c4.com/wireguard/tun/netstack` 独立模块仍可从代理获取，
  是 `ambiguous import` 的来源；固定主模块 + `replace` 是必要措施。
- 内嵌 tsnet 会把 Tailscale 的 wireguard-go fork、Wintun 模块等带进 Windows
  构建（`go version -m` 证据）；虽然 netstack 路径不安装驱动，但供应链面变大，
  需要 SBOM/漏洞扫描覆盖。

### 6.4 网络信任与权限

- tsnet 内嵌节点是 tailnet 中的**新设备**，受 ACL、device approval、tags 约束；
  企业用户可能需要在管理台批准。应用 UI 必须说明“这是本应用自己的节点”，而
  不是复用系统客户端。
- Headscale/自建控制面可避免依赖 Tailscale 的托管控制面，但控制面本身成为
  高价值目标；需要 TLS、访问控制、备份。
- 系统 LocalAPI 路径受 socket 权限保护：Unix 上无 peer credential 时为 0600/root，
  Windows 默认管道在 Administrators 保护前缀下
  （`safesocket/unixsocket.go:84-110`, `safesocket/pipe_windows.go:161`）。
  应用以普通用户运行时可能无法使用，需要安装器/服务配合。
- `ProxyCommand` 通过 shell 执行；`tailscale nc %h %p` 的参数会被
  `quoteShellArg`/`quoteWindowsArg` 转义（`adapters/ssh/proxy.go:334-365`），
  但仍应优先使用绝对路径或在设置中校验可执行文件，避免 PATH 劫持（推断）。

---

## 7. 方案对比表

| 方案 | 代码量 | 权限 | 覆盖传输 | mosh UDP | 维护成本 | 体积 | UX | 结论 |
|---|---|---|---|---|---|---|---|---|
| A1 OS Tailscale | 0 | 安装/登录 Tailscale（管理员安装，日常用户态） | 全部 | 是 | 低（外部） | 0 | 需外部客户端；透明 | **立即可用，推荐文档化** |
| A1 OS WireGuard | 0 | 安装驱动/服务（管理员） | 全部 | 是 | 低（外部） | 0 | 导入 .conf 并启用 | **立即可用，推荐文档化** |
| A2 Tailscale userspace SOCKS5/HTTP | 0 | 无（用户态 tailscaled） | SSH/ET/jump | 否 | 低（外部） | 0 | 需手动配 Proxy Profile；auth 在外部 | **立即可用，推荐文档化** |
| A3 `tailscale nc` ProxyCommand | 0 | 需系统 tailscaled + CLI | SSH/ET | 否 | 低 | 0 | PATH/平台差异 | **兜底方案** |
| B ProxyType + tsnet | 中（4–6 处改动 + UI/Vault） | 无 | SSH/ET/jump | 否 | 中 | +约 29–31 MiB | 应用内登录/key | 可作为 MVP，但不建议作为终态 |
| B ProxyType + WG netstack | 中高 | 无 | SSH/ET/jump | 否 | 中高 | +约 8–9 MiB（单独） | 导入 .conf/字段；DNS 限制 | 不推荐单独先做 |
| C Network Provider + tsnet | 高 | 无 | SSH/ET/telnet/jump | 是（需改造） | 中高 | +约 29–31 MiB | 最佳：应用内统一网络 | **推荐的原生形态** |
| C Network Provider + WG netstack | 高 | 无 | SSH/ET/telnet/jump | 是（需改造） | 高 | +约 8–9 MiB（与 tsnet 共享 gVisor 时更少） | 需自管 DNS/Endpoint/路由 | 第二阶段 |
| D Tailscale LocalAPI | 低中 | 受 socket/pipe 权限限制 | SSH/ET/telnet（TCP） | 不确定 | 低中 | 0 | 复用系统节点；依赖外部 | 可作为“使用已有 Tailscale”选项 |
| D 管理 WireGuard OS 接口 | 高 | 管理员 | 全部 | 是 | 高 | 0 | 跨平台差 | **不推荐** |

> 代码量与工期是工程估算（推断），不是承诺；体积为探针实测值，不是应用最终增量。

---

## 8. 建议与实施顺序

### Phase 0：零代码先落地（0.5 周，文档/UI 帮助）

1. 文档写明三种零代码用法：
   - OS 级 Tailscale / WireGuard：连接 tailnet IP / WG IP，所有传输可用；
   - Tailscale userspace：保存 SOCKS5/HTTP Proxy Profile 指向本地端口；
   - `tailscale nc %h %p` 作为 ProxyCommand 兜底。
2. 在 Proxy Profile 编辑页给出示例和“MagicDNS 名称可直接填 Host”的说明；
   明确 mosh/telnet 不在 A2 覆盖范围内。
3. 增加连接失败时的诊断提示：区分“Proxy 连接失败”“DNS 解析失败”
   （现有 typed errors 已区分 `KindNetwork`/`KindTimeout`/`KindUnsupported`，
   见 `application/connection/errors.go:15-23`，语义可复用）。

### Phase 1：Network Provider 缝 + tsnet（建议 3–5 周，推断）

1. 在 `application/connection` 定义最小接口（名称可再议）：

   ```go
   type NetworkProvider interface {
       DialContext(ctx context.Context, network, address string) (net.Conn, error)
   }
   ```

   先只要求 TCP；UDP 能力用可选接口
   （`DialPacketContext` 或 `net.PacketConn`）在 Phase 2 加。
2. 在 `Request`/`Route` 增加 `NetworkProviderID`（或等价的非秘密指纹）；
   `vaultRoutes.Route` 解析 Host/Group 上的 VPN profile
   （`app/session_factory.go:542-587`）。
3. 在组合根把 Provider 注入 `Dialer`：包装 `d.dial`，使 `openFirst` 的直接
   dial 和 proxy server 连接都经过 Provider（`adapters/ssh/dialer.go:336-347`）。
   `openForward` 不变（后续 hop 在 SSH 内）。
4. 池 key 纳入 Provider 指纹（`pool.go:137-190`），配置变化时新连接。
5. 实现 `tsnet` Provider：
   - 每个 Tailscale profile 一个 `tsnet.Server`；`Dir` 放应用数据目录下
     `tsnet/<profile-id>`（tsnet 以 0700 创建，`tsnet/tsnet.go:863`）；
   - 启动/登录状态机：`Up` 在 goroutine；交互登录用
     `LocalClient().Status`/`WatchIPNBus` 读取 `AuthURL` 并显示；
   - `Dial(ctx, network, address)` 直接满足 `DialFunc` 形状；
   - 应用关闭时对每个 server 调用一次 `Close`；
   - 支持 `ControlURL`（Headscale）、`Ephemeral`、`AdvertiseTags`；
   - 退出节点/接受路由通过 `LocalClient().EditPrefs` 暴露为高级选项。
6. Vault：新增 Tailscale profile（AuthKey/ClientSecret 字段密钥）与 UI；
   字段名按 ADR 0010 一次定好。
7. 测试：
   - 用 fake Provider + `adapters/sshtest/fakessh` 做完整 SSH 链测试；
   - tsnet 真实 tailnet 测试用 CI secret 或 Headscale 测试环境，标记为可选；
   - 跨平台 `CGO_ENABLED=0` 构建门禁。

**Go 工具链决策**：若用 v1.104.5，需把 `go.mod` 的 Go 版本升到 1.27.3
（CI 用 `go-version-file: go.mod`，会自动跟随）；若不接受升级，固定
`tailscale.com v1.104.0`（go.mod 1.27.1），但应计划尽快升级到最新补丁。

### Phase 2：mosh UDP（建议 2–3 周，推断）

1. 把 `udpLink` 改为 datagram 接口；Provider 可提供 connected UDP `net.Conn`
   或 `net.PacketConn`。
2. `probe` 通过 Provider 解析/拨号，不再直接 `net.ResolveUDPAddr`。
3. `CheckRoute` 增加“Network Provider”参数：允许能提供 UDP 的 Provider；
   仍拒绝 HTTP/SOCKS5 代理；jump 的限制按“bootstrap 可走 jump，UDP 必须
   Provider 可达”重新设计。
4. 测试：本地 `mosh-server` + 假 Provider；tsnet 真机/Headscale；无 VPN 回归。
5. 若不改造 mosh，则在 UI 明确“mosh 不支持内嵌 VPN”，避免用户误配。

### Phase 3：WireGuard netstack Provider（建议 4–6 周，推断，可选）

1. 定义 WireGuard profile：PrivateKey（秘密）、Address、DNS、MTU、Peers
   （PublicKey、PresharedKey、Endpoint、AllowedIPs、PersistentKeepalive）。
   支持导入 `.conf`（解析 `wg-quick` 字段）和手工表单。
2. 启动：base64→hex 密钥转换；Endpoint 主机名解析为 IP；`CreateNetTUN` →
   `device.NewDevice` → `IpcSet` → `Up`；`DialContext` 直接用 `tnet.DialContext`。
3. 网络变化：重新解析 Endpoint、必要时重建 device；`IpcGet` 做状态展示。
4. 密钥/配置变更使旧连接池失效；`Close` 时 `dev.Close`。
5. 可在同一 Provider 接口下与 tsnet 并存，但建议按发布变体/功能开关控制，
   避免同时引入两套 WireGuard 实现。
6. 安全：Key 只进 Vault；不记录 UAPI；DNS 服务器必须显式配置并在 AllowedIPs 内。

### Phase 4（可选）：D 路径

- 如果用户强烈要求“复用已登录的系统 Tailscale”，再加 LocalAPI Provider：
  `client/local` 的 `UserDial` 可直接拨 tailnet 名称/IP；但需要处理
  socket/pipe 权限和版本偏差，且 mosh UDP 需实测。
- 不建议做 WireGuard OS 接口管理。

### 为什么推荐 C 而不是 B

- **可组合**：Network Provider 在 Proxy 之下，VPN 可与现有 HTTP/SOCKS5/
  ProxyCommand 叠加；`ProxyType` 只能三选一。
- **覆盖 mosh/telnet**：Provider 是唯一能自然扩展到 UDP 和未走 Manager 的
  telnet 的位置。
- **单一生命周期**：一个 VPN profile 可服务所有 Host/会话，而不是每个 Host
  复制一份代理配置。
- **与现有 seam 匹配**：`DialFunc` 已经是 `func(ctx, network, address)`，
  tsnet 的 `Dial` 形状完全一致；Provider 只是把这个 seam 提升为可选择的
  网络层。

---

## 9. 未决问题 / 需要实测

以下项**未能**从 primary source 完全确认，落地前必须验证：

1. **`pkg.go.dev` 不可达**：本次网络环境无法直接访问 `pkg.go.dev`，API 以
   `tailscale.com@v1.104.5` 源码和 `git.zx2c4.com` 源码为准；若需要官方
   pkg.go.dev 快照，应在可访问环境复核。
2. **Tailscale v1.105/main 与 v1.104.5 的 API 差异**：main README 提到
   `Server.LookupIP`，但 v1.104.5 的 `tsnet` 包中没有该方法（`grep` 无命中）。
   不要按 main 文档实现；以锁定版本为准。
3. **Headscale + tsnet 兼容性**：`ControlURL` 是官方字段，Headscale 自称实现
   control server；但没有找到 Headscale 官方“支持 tsnet”的明确声明。
   需要用目标 Headscale 版本做 Dial/DNS/auth 实测。
4. **Windows/macOS 的 `tailscaled --tun=userspace-networking`**：源码显示
   macOS 非 root 分支与 Windows 绕开 Wintun 的提示，但官方文档主要面向
   Linux/容器；需在目标平台验证 SOCKS5/HTTP 代理的实际可用性、DNS 与
   subnet/exit node 行为。
5. **MagicDNS `*.ts.net` FQDN**：源码只保证“tailnet DNS first, system resolver
   fallback”；短名和 FQDN 的具体行为需在真实 tailnet 上验证（尤其 Headscale
   的 DNS 配置）。
6. **tsnet 交互登录的 UI 集成**：`AuthURL` 通过 `Status`/`WatchIPNBus` 可读，
   但 `Up` 在 `NeedsLogin` 时会一直阻塞（源码 TODO）；需要实测状态机、超时、
   取消和重新登录（`TSNET_FORCE_LOGIN=1` 或更换 `Dir`）的边界。
7. **tsnet 日志上传的关闭方式**：源码注释说日志自动上传到 `log.tailscale.com`；
   本次未找到官方“完全关闭 logtail”的配置说明。隐私敏感场景需实测/审计。
8. **mosh over tsnet Udp**：tsnet `Dial("udp")` 返回 connected `net.Conn`；
   mosh 需要 datagram 语义、候选地址回退和源地址不固定的行为。必须做
   端到端测试（真正的 `mosh-server`），不能只靠类型推断。
9. **mosh over wireguard-go netstack**：`gonet.UDPConn` 的 datagram 边界、
   `Read` 行为、与现有 `readRetry`/`reconnect` 的兼容性需实测。
10. **LocalAPI UDP**：`/localapi/v0/dial` 是 HTTP upgrade 字节流，UDP 数据报
    边界是否保留未知；不建议用于 mosh，除非实测证明可行。
11. **WireGuard Endpoint 重解析/漫游**：`wireguard-go` UAPI 只收 IP:port；
    应用层需要在网络切换后重新解析。macOS 无 sticky sockets
    （`wireguard-go/README.md:33`），漫游行为需真机测试。
12. **WireGuard DNS 语义**：netstack 只用传入的 `dnsServers`；若用户的
    `.conf` 没有 `DNS=` 或 DNS 不在 `AllowedIPs`，MagicDNS 类体验不成立。
    需要产品决策（强制 DNS 字段/只允许 IP/使用系统 DNS 但会泄漏）。
13. **本应用的真实体积增量**：探针值不等于最终二进制增量；需在接入后测量
    release 产物（各平台）。同时链接 tsnet + wireguard-go 会带两套 WG 实现，
    体积/代码面影响需实测。
14. **tsnet SSH 到 Tailscale SSH 的端到端**：`x/crypto/ssh` 会先尝试 `none`
    认证（源码确认），但 host key 分发、ACL check、re-auth 与现有
    `VerifyHostKey`/AuthPlan 的交互未在真实 tailnet 验证。
15. **系统 LocalAPI 的权限矩阵**：Unix peer credential、Windows 命名管道 ACL
    在不同安装方式（用户级 vs. 系统服务）下的实际访问权限需逐平台验证。
16. **上游 Netcatty 的归档版本**：本次用 `codeload` 下载 `main`，没有拿到
    具体 commit id；结论“无 Tailscale/WireGuard 功能”基于该归档的全文搜索，
    若上游在检索后新增功能需重新核对。
17. **WireGuard OS 客户端的管理员要求**：Windows MSI/驱动、macOS Network
    Extension、Linux `wg-quick` 的 root 要求来自官方安装/工具文档与命令语义；
    具体安装器行为（静默安装、企业策略）未逐一实测。

---

## 参考来源（按主题）

**Tailscale**

- tsnet 包文档与 README：<https://tailscale.com/docs/features/tsnet>，
  <https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/README.md>
- tsnet.Server API：<https://tailscale.com/docs/reference/tsnet-server-api>
- tsnet 源码（v1.104.5）：<https://github.com/tailscale/tailscale/blob/v1.104.5/tsnet/tsnet.go>
- userspace networking：<https://tailscale.com/docs/concepts/userspace-networking>
- tailscaled 代理实现：<https://github.com/tailscale/tailscale/blob/v1.104.5/cmd/tailscaled/proxy.go>
- tsdial（MagicDNS/路由/Dial）：<https://github.com/tailscale/tailscale/blob/v1.104.5/net/tsdial/tsdial.go>
- Tailscale CLI `nc`：<https://github.com/tailscale/tailscale/blob/v1.104.5/cmd/tailscale/cli/nc.go>
- LocalAPI client：<https://github.com/tailscale/tailscale/blob/v1.104.5/client/local/local.go>
- auth keys：<https://tailscale.com/docs/features/access-control/auth-keys>
- Tailscale SSH：<https://tailscale.com/docs/features/tailscale-ssh>
- Tailscale Serve：<https://tailscale.com/docs/features/tailscale-serve>
- 模块版本元数据：<https://goproxy.cn/tailscale.com/@latest>（v1.104.5）

**WireGuard**

- wireguard-go README/许可：<https://git.zx2c4.com/wireguard-go/plain/README.md>，
  <https://git.zx2c4.com/wireguard-go/plain/LICENSE>
- `tun/netstack`：<https://git.zx2c4.com/wireguard-go/plain/tun/netstack/tun.go>
- 官方示例：<https://git.zx2c4.com/wireguard-go/plain/tun/netstack/examples/http_client.go>
- UAPI/device：<https://git.zx2c4.com/wireguard-go/plain/device/uapi.go>，
  <https://git.zx2c4.com/wireguard-go/plain/device/device.go>
- Endpoint 解析：<https://git.zx2c4.com/wireguard-go/plain/conn/bind_std.go>
- `wg(8)`：<https://git.zx2c4.com/wireguard-tools/plain/src/man/wg.8>
- `wg-quick(8)`：<https://git.zx2c4.com/wireguard-tools/plain/src/man/wg-quick.8>
- Windows 客户端：<https://git.zx2c4.com/wireguard-windows/plain/README.md>
- macOS/iOS 客户端：<https://git.zx2c4.com/wireguard-apple/plain/README.md>
- wgctrl：<https://github.com/WireGuard/wgctrl-go>
- 模块版本元数据：<https://goproxy.cn/golang.zx2c4.com/wireguard/@latest>

**Headscale 与许可**

- Headscale：<https://github.com/juanfont/headscale>（README、LICENSE）
- GNU license list：<https://www.gnu.org/licenses/license-list.html>
- Apache-2.0：<https://www.gnu.org/licenses/license-list.html#apache2>
- Modified BSD：<https://www.gnu.org/licenses/license-list.html#ModifiedBSD>
- Expat/MIT：<https://www.gnu.org/licenses/license-list.html#Expat>

**本仓库**

- `application/connection/route.go`、`application/connection/manager.go`、
  `application/connection/pool.go`
- `adapters/ssh/dialer.go`、`adapters/ssh/proxy.go`
- `adapters/mosh/mosh.go`、`adapters/mosh/inprocess.go`
- `adapters/et/inprocess.go`、`adapters/telnet/telnet.go`
- `app/app.go`、`app/session_factory.go`、`app/transports.go`
- `domain/vault/host.go`、`domain/vault/proxy.go`
- `application/vault/secrets.go`、`application/vault/groups.go`
- `docs/adr/0002-xcrypto-ssh-as-sole-transport.md`、
  `docs/adr/0010-field-secret-key-hierarchy.md`、
  `docs/adr/0014-host-app-licensing-and-distribution.md`、
  `docs/adr/0015-pure-go-mosh-client.md`、`docs/adr/0016-pure-go-et-client.md`
- `.github/workflows/ci.yml`、`.github/workflows/release.yml`
