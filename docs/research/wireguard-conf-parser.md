# wg-quick `.conf` 解析方案研究

Status: research / decision proposal
Date: 2026-11-04
Scope: 为 Netcatty Native 的 `WireGuardNet`（wireguard-go + `tun/netstack`，用户态、
`CGO_ENABLED=0`、不管理 OS 接口）确定 wg-quick `.conf` 的解析方案、字段支持边界、
错误模型与测试清单。实现代码不在本 ticket 内。

Sources verified against:

- `wireguard-tools` `master`，commit `a998407747005ea7e4e0258d96f105c97241e1d3`
  （2026-05-06）：`src/config.c`、`src/setconf.c`、`src/encoding.c`、`src/ctype.h`、
  `src/wg-quick/linux.bash`、`src/man/wg.8`、`src/man/wg-quick.8`。
  均通过 `https://git.zx2c4.com/wireguard-tools/plain/<path>` 下载核对。
- `wireguard-go` 模块 **v0.0.0-20261024220952-b3bc704ef0ee**（2026-10-24，
  `goproxy.cn` 元数据；本机 module cache），核对 `tun/netstack/tun.go`、
  `device/uapi.go`；`git.zx2c4.com/wireguard-go` commit
  `b3bc704ef0ee21a36298601a9e1e5ff90377ac52`。
- `wgctrl` 模块 **v0.0.0-20241231184526-a9ab2273dd10**（2024-12-31，
  `goproxy.cn`），源码取自模块 zip：`wgtypes/types.go`、`internal/wguser/configure.go`。
- `wireguard-windows` 模块 **v1.1.1**（2026-09-20，`goproxy.cn`），commit
  `6ece77bc487c8aa697e3c092197621c4f3e5ccb8`（2026-09-22）；核对
  `conf/parser.go`、`conf/config.go`、`conf/parser_test.go`、`driver/*_windows.go`。
- 候选库版本/日期来自 `goproxy.cn` 的 `@latest`/`@v/<v>.info` 与
  `cdn.jsdelivr.net` 上的源码；具体见第 3 节。
- `iproute2` `ip/ipaddress.c`（用于确认裸地址的默认前缀长度）。

---

## 1. 结论摘要

**推荐自研一个最小、纯函数式解析器（stdlib only），不要把 `.conf` 解析交给现成库。**
本仓库为 GPL-3.0-or-later；MIT 参考实现可用于移植，但移植文件必须保留原版权与
许可声明并在文件头注明来源。
官方 Go 实现（`golang.zx2c4.com/wireguard/windows/conf`）MIT 且覆盖大部分字段
（但会把 `FwMark`/`SaveConfig` 判为未知 key），且该包依赖
Windows-only 的 `driver`（全部 `*_windows.go`）和 `x/sys/windows`，无法在 Linux/macOS
构建；其余 Go 解析库要么停止维护、要么字段覆盖不全、要么在解析阶段做 DNS 解析，
还有的没有许可证。最小解析器约 300–500 行 + 表驱动测试，可完全控制错误 UX、
机密脱敏、资源上限和 netstack 映射。

**字段结论（用户态 netstack）：**

| 字段 | 结论 |
|---|---|
| `PrivateKey`、`ListenPort`、`PublicKey`、`PresharedKey`、`AllowedIPs`、`Endpoint`、`PersistentKeepalive` | **支持**（映射到 wireguard-go UAPI/device config） |
| `Address` | **支持**（`CreateNetTUN` 的 `localAddresses`；裸 IP 归一化为 /32 或 /128；要求至少一条） |
| `DNS`（IP 形式） | **支持**（`CreateNetTUN` 的 `dnsServers`；仅 IP，无 OS resolver 回退） |
| `MTU` | **支持**（`CreateNetTUN` 的 `mtu`；缺省 1420） |
| `DNS` 中的非 IP（search domain） | **解析但忽略并告警**：netstack 没有 search list 支持 |
| `FwMark` | **解析校验但忽略并告警**：无 OS policy routing/firewall，标记无意义 |
| `Table` | **解析校验但忽略并告警**：无 OS 路由表；netstack 内路由由 AllowedIPs 决定 |
| `SaveConfig` | **解析但忽略并告警**：Vault 是唯一事实源，绝不回写导入文件 |
| `PreUp` / `PostUp` / `PreDown` / `PostDown` | **硬拒绝**：不做 shell 执行；静默忽略会破坏用户防火墙/kill-switch 预期 |

**已确认：`wgctrl` 不提供 `.conf` 解析。** `wgtypes` 只有 `ParseKey`；`DeviceConfig`
只表达 UAPI 可设置项（private_key、listen_port、fwmark、peer/allowed_ip 等），
没有 Address/DNS/MTU/Table/hooks。

---

## 2. 官方格式与语义（一手来源）

### 2.1 文件结构

`wg(8)` 的 `CONFIGURATION FILE FORMAT`（`src/man/wg.8`，L131–176）规定：

- INI 风格；两个顶层 section：`[Interface]` 与 `[Peer]`；`[Peer]` 可重复，
  `[Interface]` 按 man page 只允许一个。
- `[Interface]`：`PrivateKey`（man 标为 Required）、`ListenPort`（16-bit，缺省随机）、
  `FwMark`（32-bit，`0`/`off` 关闭，可 `0x` 十六进制）。
- `[Peer]`：`PublicKey`（Required）、`PresharedKey`、`AllowedIPs`（逗号分隔，
  可重复，`0.0.0.0/0`、`::/0` 为 catch-all）、`Endpoint`（IP/主机名 + `:` + 端口，
  IPv6 用 `[...]`）、`PersistentKeepalive`（1–65535 秒，`0`/`off` 关闭）。
- 示例明确说明：`#` 及其后的字符是注释。

`wg-quick(8)`（`src/man/wg-quick.8`，L55–105）在此外增加仅由脚本处理的
`[Interface]` 字段：`Address`、`DNS`、`MTU`、`Table`、`PreUp/PostUp/PreDown/PostDown`、
`SaveConfig`；其余内容原样传给 `wg(8)`。

> 源码与 man 的差异：man 说传给 `wg setconf`，`linux.bash` 的 `set_config()`
>（L251–253）实际执行的是 **`wg addconf`**。对全新接口两者等价（addconf 不置
> replace_peers），本方案按“构造全新设备”处理，因此按 setconf 语义即可。

### 2.2 `wg` 配置文件解析器（`src/config.c`，一手核验）

| 语义 | 实际行为 | 源码位置 |
|---|---|---|
| 行读取 | `setconf.c` 用 `getline()`，**无行长/文件大小上限**；整文件解析成功后才 `ipc_set_device`，失败不部分应用 | `src/setconf.c` `setconf_main()` |
| 注释 | 任意位置第一个 `#` 起至行尾全部丢弃，无法转义；因此值中不能含 `#` | `config_read_line()`，`COMMENT_CHAR` |
| 空白 | 注释截断后，**删除所有** HT/LF/VT/FF/CR/SP（`char_is_space`）再匹配；所以 `Private Key = x` 也会被当作 `PrivateKey=x`，CRLF 也被处理 | `config_read_line()` L527–530；`src/ctype.h` |
| 空行 | 清理后长度为 0 → 忽略 | `config_read_line()` L531–532 |
| section | `[Interface]` / `[Peer]` 用 `strcasecmp`（**大小写不敏感**，且允许内部空白被删除后的形式）；多个 `[Interface]` 不报错 | `process_line()` L441–463 |
| key | `get_value()` 用 `strncasecmp`（**大小写不敏感**）；key 必须出现在对应 section，否则 “Line unrecognized” | `get_value()` L25–37；`process_line()` |
| 未知 key/section | 解析失败，打印 `Line unrecognized: ...`，整个文件拒绝 | `process_line()` L501–503 |
| 重复标量 key | 后值覆盖前值（PrivateKey/ListenPort/FwMark/Endpoint/PublicKey/PresharedKey/PersistentKeepalive） | `process_line()` |
| 重复 `AllowedIPs` 行 | 追加到该 peer 的 allowed-ip 列表 | `parse_allowedips()` L354–434 |
| 重复 `[Peer]` | 每个 section 新建 peer；同一个 PublicKey 不在解析器层去重（wireguard-go UAPI 会按 key 合并到同一 peer） | `process_line()` L446–462；`device/uapi.go` `handlePublicKeyLine()` |
| peer 缺 PublicKey | 解析结束时报 “A peer is missing a public key” | `config_read_finish()` L554–568 |
| 缺 PrivateKey / 缺 `[Interface]` | 解析器**不报错**；setconf 模式下会置 `WGDEVICE_HAS_PRIVATE_KEY` 并用全零 key，即清空私钥 | `config_read_init()` L541–552 |
| key 格式 | 标准 base64，长度必须 44（含结尾 `=`），解码 32 字节 | `src/encoding.c` `key_from_base64()` L50–72 |
| AllowedIPs | 逗号分隔；裸 IP 默认 /32 或 /128；支持 `+`/`-` 增量前缀；host part 非零只警告（不掩码）；空值表示清空 | `parse_allowedips()` |
| Endpoint | 必须是 `host:port`；IPv6 必须 `[...]`；主机名允许；**解析时**用 `getaddrinfo` 做 DNS 解析，默认失败重试 15 次（`WG_ENDPOINT_RESOLUTION_RETRIES`）；端口接受服务名 | `parse_endpoint()` L195–280 |
| ListenPort | 通过 `getaddrinfo(NULL, value)` 解析，接受数字或服务名 | `parse_port()` L39–75 |
| FwMark | `off`（大小写不敏感）或十进制/`0x` 十六进制 32-bit | `parse_fwmark()` L77–105 |
| PersistentKeepalive | `off`（大小写不敏感）或 0–65535 | `parse_persistent_keepalive()` L282–306 |

### 2.3 `wg-quick` 的解析与字段处理（`src/wg-quick/linux.bash`，一手核验）

`parse_options()`（L40–75）逐行读取，**不是 `source` 执行**：

- `shopt -s nocasematch`：section/key 匹配大小写不敏感。
- `${line%%\#*}`：第一个 `#` 之后丢弃；但 `PreUp/PostUp/...` 使用
  `unstripped_value`（原行 `=` 之后、注释仍在），因为这本来就是 shell 片段。
- key/value 用 extglob 去掉两端 `[[:space:]]`；`Address`/`DNS` 还会按逗号
  和空白拆成数组。
- 只“消费”它认识的 `[Interface]` 扩展字段；`[Interface]`/`[Peer]` 行和其它
  所有 key（`PrivateKey`、`ListenPort`、`FwMark`、peer 字段）原样累积到
  `WG_CONFIG`，最终交给 `wg addconf`。

| 字段 | `linux.bash` 行为 |
|---|---|
| `Address` | 可重复；逗号/空白分隔；不校验，交给 `ip address add`。`iproute2` 对裸地址默认用 /32 或 /128（`ip/ipaddress.c` L2595–2596：`req.ifa.ifa_prefixlen = lcl.bitlen`） |
| `DNS` | 逗号分隔；匹配 `^[0-9.]+$` 或含 `:` 的值作为 nameserver，其余作为 search domain；调用 `resolvconf -a ... -m 0 -x` |
| `MTU` | 直接 `ip link set mtu`；未指定时按 endpoint/默认路由 MTU − 80 自动计算（`set_mtu_up()` L126–143），常见 1500−80 = 1420 |
| `Table` | `off` 不装路由；`auto`（或空）装默认表并特殊处理默认路由；其他值（数字或 `iproute2` 表名）传给 `ip route ... table`（`add_route()` L168–180）。比较区分大小写 |
| `PreUp/PostUp/PreDown/PostDown` | 可重复、按序执行；`%i` 替换为接口名；`(eval "$hook")`（`execute_hooks()` L291–298） |
| `SaveConfig` | 只接受 `true`/`false`（`read_bool()`，因 nocasematch 实际大小写不敏感）；`down` 时用当前状态回写文件 |

### 2.4 容易踩坑的语义点

1. `wg` 会**删除所有空白**再匹配，这比“trim 两端”宽松得多；本方案建议只 trim，
   含空白的 key 直接报错（与官方 Go 解析器一致，见 3.1）。
2. 注释无法转义；`#` 在 shell hook 中虽被 `linux.bash` 保留，但 netstack 方案
   根本不会执行 hook。
3. `AllowedIPs` 的 host bits 非零时 `wg` 只警告、不掩码；netstack/路由需要前缀，
   建议 `.Masked()` 并产生 warning。
4. `Endpoint` 的 DNS 解析发生在 `wg` 应用配置时且可能长时间重试；解析器必须
   保持纯函数，把解析推迟到 Provider 启动阶段。
5. `Table` 只影响 OS 路由安装；用户态 netstack 没有 OS 路由表，语义上不适用。
6. 官方 `wg-quick` 无文件大小/行数上限；导入不可信文件时必须自行加限。

---

## 3. Go 生态候选库（许可证 / 维护状态 / 覆盖）

### 3.1 官方 Go 解析器：`golang.zx2c4.com/wireguard/windows/conf`

- 版本 **v1.1.1**（2026-09-20；commit `6ece77b...`，2026-09-22），**MIT**
  （`conf/parser.go` SPDX、仓库 `COPYING`）。
- `conf.FromWgQuick(s, name)`（`conf/parser.go` L160–404）覆盖 `Address`、
  `DNS`/`DNSSearch`、`MTU`、`TableOff`、四个 hook、`PrivateKey`、`ListenPort`
  与 peer 全部字段；**不识别 `FwMark` 与 `SaveConfig`**——switch 中没有对应 case，
  会按未知 key 报错，`Config` 结构体也没有这两个字段。
- 语义细节（一手核验）：
  - key 统一 `strings.ToLower`，section/key 大小写不敏感；`#` 行内注释；CRLF 由
    `TrimSpace` 处理；hook 值从原始行重新截取，保留引号内的 `#`。
  - `parseIPCidr`：裸 IP → /32 或 /128；`parseMTU` 限制 576–65535；`parsePort`
    0–65535；`parseTableOff` 接受 `off`/`auto`/`main`/十进制 uint32。
  - `splitList` 拒绝连续逗号/空项；未知 key 报错；结尾强制
    `PrivateKey` 存在且每个 peer 有 `PublicKey`（L394–401）。
  - 重复标量后值覆盖；`Address`/`DNS`/`AllowedIPs` 跨行追加；重复 `PreUp` 等
    会被覆盖（与 wg-quick 的“多次执行”不同）。
- **不能作为依赖直接使用**：`conf/parser.go` import
  `golang.zx2c4.com/wireguard/windows/driver` 与 `golang.org/x/sys/windows`；
  `driver/` 下所有文件都是 `*_windows.go`，在 Linux/macOS 构建会失败。该模块还
  引入 GUI/安装器等重依赖。**只能作为 MIT 参考实现阅读/移植。**

### 3.2 `golang.zx2c4.com/wireguard/wgctrl` —— 不提供解析

- 最新 **v0.0.0-20241231184526-a9ab2273dd10**（2024-12-31），**MIT**。
- `wgtypes` 只有 `ParseKey`（`wgtypes/types.go` L125），**没有** `ParseConfig` /
  `ParseConfigFile`，也不认识 `[Interface]`/`[Peer]`。
- `DeviceConfig`/`PeerConfig` 只覆盖 UAPI：`private_key`、`listen_port`、`fwmark`、
  `replace_peers`、`public_key`、`preshared_key`、`endpoint`、
  `persistent_keepalive_interval`、`replace_allowed_ips`/`allowed_ip`
  （`internal/wguser/configure.go` L55–98）。
- 结论：wgctrl 可用于设备控制（或直接调用 wireguard-go device API），但**不能**
  用于 `.conf` 解析；Address/DNS/MTU/Table/hooks 完全不在其模型内。

### 3.3 其它候选

| 模块 | 许可证 | 最新发布 | 覆盖与问题 | 结论 |
|---|---|---|---|---|
| `github.com/nmiculinic/wg-quick-go` | MIT | `v0.1.2`，2019-11-04 | 完整 Linux wg-quick 实现；解析器大小写敏感、只识别行首 `#`、Address/AllowedIPs 强制 CIDR（拒绝裸 IP）、DNS 只接受 IP（拒绝 search domain）、Table 只接受数字、Endpoint 在解析阶段 `net.ResolveUDPAddr` 做 DNS、未知 key 报错；依赖 netlink/wgctrl 2019 | 不采用 |
| `github.com/parrotmac/wg-quick-go` | MIT | `v0.1.4`，2020-12-18 | 与 nmiculinic 同一代码血统（`config.go` 基本相同），go.mod 还写着 `github.com/uinta-labs/wg-quick-go` | 不采用 |
| `github.com/uinta-labs/wg-quick-go` | MIT | `v0.1.4`，2020-12-18 | 同上 | 不采用 |
| `github.com/afilipovski/wg-quick-go` | MIT | 伪版本 `v0.0.0-20250721094504-a4d57ce2e1a8`，2025-07-21 | 仍是同一解析器，仅增加 `[LazyWg]` section；无持续维护证据 | 不采用 |
| `github.com/QuantumGhost/wg-quick-go` | **无 LICENSE 文件** | `v0.1.0`，2019-11-05 | 解析器在 `internal/config`（不可导入）；只支持 PrivateKey/ListenPort/PublicKey/PresharedKey/AllowedIPs/Endpoint/PersistentKeepalive；FwMark 明确不支持 | 排除（许可+不可导入） |
| `github.com/jamescun/wireguard` | 未确认 | 模块代理无法解析（仓库可能已删除/私有） | 未能取得源码 | 排除（未验证） |
| `github.com/mdlayher/wgconf` | — | — | **不存在**（pkg.go.dev/GitHub 搜索均无；代理无法解析） | 不存在的候选，勿引用 |

> 另有 `github.com/gentlemanautomaton/wgconf`，它是 systemd `.netdev` 编组，
> 不是 wg-quick 解析器；`github.com/point-c/wgapi` 解析的是 UAPI key=value 文本，
> 也不是 `.conf`；`tailscale.com/wgengine/wgcfg` 是控制面配置的数据模型，
> 没有 INI 解析入口。三者均不满足本 ticket。

---

## 4. 用户态 netstack 能力映射

wireguard-go 的 `tun/netstack`（模块 v0.0.0-20261024220952-b3bc704ef0ee，
`tun/netstack/tun.go` L55–109）：

```go
func CreateNetTUN(localAddresses, dnsServers []netip.Addr, mtu int) (tun.Device, *Net, error)
```

- `localAddresses`：逐个 `AddProtocolAddress`；使用
  `tcpip.AddrFromSlice(ip.AsSlice()).WithPrefix()`，即**只保留主机地址 /32 或 /128，
  丢弃原 CIDR mask**；随后为 IPv4/IPv6 各加一条默认路由到 NIC。
- `dnsServers`：只接受 `netip.Addr`；`Net.LookupHost` 通过 netstack 向这些服务器
  发 DNS（UDP/TCP），**没有 search domain 概念**。
- `mtu`：传给 `channel.New`，即 TUN MTU。
- 无 OS 接口、无路由表、无 firewall mark、无 shell。

| `.conf` 字段 | netstack 映射 | 决策 |
|---|---|---|
| `PrivateKey` | UAPI `private_key`（base64→32B→hex） | 支持；导入必填（严格于 wg 解析器） |
| `ListenPort` | UAPI `listen_port`；0=随机 | 支持；0–65535 |
| `FwMark` | UAPI `fwmark` 可设，但无 OS policy routing/firewall | 解析校验；**忽略 + warning** |
| `Address` | `CreateNetTUN.localAddresses` | 支持；裸 IP 归一化为 /32 或 /128；≥1 条；mask 仅用于展示/校验 |
| `DNS`（IP） | `CreateNetTUN.dnsServers` | 支持；仅隧道内解析，无 OS resolver 回退 |
| `DNS`（非 IP/search domain） | 无对应能力 | **忽略 + warning**；若没有 IP DNS，则仅 IP literal 目标可用 |
| `MTU` | `CreateNetTUN.mtu` | 支持；576–65535；缺省 1420 |
| `Table` | 无 OS 路由表；netstack 路由由 AllowedIPs/默认路由决定 | 解析校验（`off` / `auto` / `main` / uint32）；**忽略 + warning** |
| `SaveConfig` | 无文件回写；Vault 为事实源 | 解析 true/false；**忽略 + warning** |
| `PreUp/PostUp/PreDown/PostDown` | 需 `eval` shell | **硬拒绝**（安全 + 语义不可静默丢弃） |
| `PublicKey`/`PresharedKey`/`AllowedIPs`/`Endpoint`/`PersistentKeepalive` | UAPI peer 字段 | 支持 |

`Endpoint` 注意：解析器只保存 `host:port`，不做 DNS；Provider 启动时用系统 resolver
解析（带超时/重试），失败返回 typed error，绝不直连回退。主机名重解析/漫游属于
ticket 04 与 map 中“Not yet specified”的遗留问题。

`Address` 注意：`CreateNetTUN` 丢弃 mask，只把地址加入 netstack 并加默认路由；
因此 `.conf` 中的 /24 等前缀不会产生子网路由。对本 Provider（应用内按
AllowedIPs 选路）没有功能影响，但必须在 UI/规格中写明。

---

## 5. 推荐：自研最小解析器

### 5.1 边界（parser boundary）

- API：`Parse(data []byte) (*Config, []Diagnostic, error)`。
  - 纯函数：不读文件、不解析 DNS、不执行 shell、不读环境变量、不打日志、不起 goroutine。
  - 文件读取/大小检查由调用方完成；parser 内部再限制行数/条目数。
- 输出（归一化、可直接映射 UAPI/netstack）：
  ```go
  type Config struct {
      Interface Interface
      Peers     []Peer
  }
  type Interface struct {
      PrivateKey  [32]byte      // 必填
      ListenPort  uint16        // 0 = random
      Addresses   []netip.Prefix // 必填 >=1；裸 IP 已补 /32|/128
      DNS         []netip.Addr   // 仅 IP
      MTU         uint16         // 0 = 默认 1420
  }
  type Peer struct {
      PublicKey           [32]byte
      PresharedKey        *[32]byte
      AllowedIPs          []netip.Prefix // 已 Masked()
      EndpointHost        string         // 未解析
      EndpointPort        uint16
      PersistentKeepalive uint16
  }
  ```
  另以 `Diagnostic{Line, Field, Code, Message}` 返回非阻断告警
  （FwMark/Table/SaveConfig/search domain/空 AllowedIPs/host bits 等）。
- 单独的 `ValidateForNetstack(Config) error`（Provider 层）：至少 1 个 Address、
  至少 1 个 Peer、私钥存在、端点可解析；与语法解析解耦，便于手工表单复用。
- 不把 `PreUp/...` 放进 Config；非空值即返回 `ErrUnsupportedHooks`，错误信息列出
  行号和字段名，提示用户移除后重新导入（或在 UI 中明确“丢弃这些行”再导入）；
  空值视为无操作（warning）。

### 5.2 语法与归一化规则

1. 编码：UTF-8；去掉 UTF-8 BOM；可选支持 UTF-16LE/BE BOM 转码（Windows 用户
   常见）；拒绝 NUL/非法 UTF-8。行尾支持 LF/CRLF/最后一行无换行。
2. 结构：`[Interface]`、`[Peer]` 大小写不敏感；重复 `[Interface]` 视为继续
   （与 wg 一致，标量后值覆盖）；`[Peer]` 可重复。
3. 注释：第一个 `#` 到行尾；不支持转义。
4. 空白：trim 两端；key 内部含空白 → `ErrSyntax`（比 wg 严格，避免
   `Private Key` 这类意外）。
5. 要求：`PrivateKey` 必填；每个 peer `PublicKey` 必填；同一文件内重复
   `PublicKey` → `ErrDuplicatePeer`（比 wg 的 UAPI 静默合并严格）。
6. 标量重复：后值覆盖（wg 兼容），建议产生 warning。
7. `Address`：逗号+空白分隔；裸 IP 补 /32|/128；不掩码（主机地址）。
8. `DNS`：逗号分隔；`netip.ParseAddr` 成功 → DNS；否则 → search domain
   warning（不进入 Config）。
9. `MTU`：十进制 576–65535；缺省 0（Provider 用 1420）。
10. `ListenPort`：十进制 0–65535（0=随机）；拒绝服务名。
11. `Endpoint`：最后一个 `:` 分隔 host/port；IPv6 必须 `[...]`；
    host 非空；port 1–65535（wg 允许 0，但无实际意义，本方案拒绝）；主机名原样保留，不做 DNS。
12. `AllowedIPs`：逗号分隔；裸 IP 补 host prefix；host bits 非零 → `.Masked()`
    + warning；拒绝 `+`/`-` 增量前缀（静态导入语义不明确，属刻意收紧）；
    空列表允许但 warning（该 peer 不接收流量）。
13. `PresharedKey`：base64 44 字符、解码 32 字节；空值非法。
14. `FwMark`：`off`（大小写不敏感）/0/十进制/0x；值合法但忽略 + warning。
15. `Table`：`off`/`auto`/`main`/十进制 uint32；合法但忽略 + warning。
16. `SaveConfig`：`true`/`false`（大小写不敏感）；忽略 + warning。
17. hooks：任何非空值 → `ErrUnsupportedHooks`（硬错误）；空值视为无操作并 warning。
18. 未知 key/section → 错误；错误信息包含 1-based 行号与字段名，**不包含密钥值**。
19. 上限（防御性，非上游语义）：文件 ≤ 1 MiB、单行 ≤ 64 KiB、peer ≤ 256、
    Address ≤ 64、DNS ≤ 32、AllowedIPs ≤ 4096、Endpoint host ≤ 255 字节。

### 5.3 错误模型

```go
type Kind int // ErrSyntax, ErrUnknownKey, ErrUnknownSection, ErrMissingRequired,
             // ErrDuplicatePeer, ErrUnsupported, ErrLimit, ErrEncoding
type ParseError struct { Line int; Field string; Kind Kind; Msg string }
```

- `errors.Is/As` 可判别；UI 将 `Kind` 映射为不同提示（语法错误/不支持/超限/编码）。
- 解析失败返回 `nil` Config，绝不部分导入。
- 密钥字段在错误与日志中一律脱敏（`<redacted>`），只在内存中保留。
- Warning 不阻断导入；hooks 错误阻断导入。

### 5.4 测试用例清单

**有效路径**

1. 最小可用：`[Interface] PrivateKey + Address` + 单个 `[Peer] PublicKey + AllowedIPs + Endpoint`。
2. 多 Peer、多 Address（跨行 + 逗号）、多 AllowedIPs、IPv4/IPv6 混合。
3. IPv6 地址、CIDR、`[...]:port` endpoint。
4. 注释：行首、行尾、section 行尾 `[Interface] # x`、空行、CRLF、文件末尾无换行。
5. 大小写：`[interface]`、`privatekey`、`PUBLICKEY`、`ENDPOINT`。
6. 裸 IP：Address → /32|/128；AllowedIPs → /32|/128；IPv6 裸地址。
7. 值中的 `=`：base64 key 的 `=` 填充只按第一个 `=` 分割。
8. `ListenPort=0`、`FwMark=off/0/0x1f`、`PersistentKeepalive=off/0/65535`。
9. `Table=off/auto/main/1234`（warning，不阻断）。
10. `DNS=1.1.1.1, 2606:4700:4700::1111, corp.example`（后两者中，IP 支持，
    搜索域 warning）。
11. `MTU=576/1420/65535`。
12. 重复 `[Interface]`（合并）与重复标量（后值覆盖 + warning）。
13. 重复 AllowedIPs 行（追加）与重复 AllowedIPs 条目（去重或保留，需固定行为）。
14. `Endpoint` 为不可解析主机名：解析成功（证明 parser 不做 DNS）。
15. `PreUp/PostUp` 为空值 → 接受 + warning（无操作）；非空 → 第 25 条错误。

**错误路径**

16. 缺 `=`、key 在 section 外、空文件/仅注释文件。
17. 未知 key（两个 section 各一）、未知 section。
18. 缺 PrivateKey；缺 Peer PublicKey；重复 Peer PublicKey。
19. 非法 base64 / 长度不是 32 字节 / 非规范 base64。
20. 非法 CIDR（前缀越界、坏 octet）、裸 hostname 出现在 AllowedIPs。
21. 非法端口（`http`、65536、空）、IPv6 endpoint 缺少 `[]`、endpoint 缺端口。
22. `Address=` / `DNS=` / `PublicKey=` 空值。
23. `AllowedIPs = 10.0.0.0/8, , 192.168.0.0/16`（连续逗号）与尾逗号。
24. `AllowedIPs = +10.0.0.0/8` / `-10.0.0.0/8` → ErrUnsupported。
25. `PreUp = iptables ...` / `PostUp = ...` → ErrUnsupportedHooks；错误文本不得
    回显 PrivateKey/PresharedKey。
26. `AllowedIPs = 10.0.0.1/24` → 掩码为 10.0.0.0/24 + warning。
27. `MTU=0/575/65536`、`ListenPort=-1/65536`、`PersistentKeepalive=65536`。
28. UTF-8 BOM、UTF-16LE BOM（可选支持）、非法 UTF-8、含 NUL。
29. 超限：>1 MiB 文件、>64 KiB 行、>256 peers、>4096 AllowedIPs。
30. Fuzz：任意输入不 panic；成功解析后再次序列化/解析应稳定（若实现 writer）。

---

## 6. 未验证 / 待实测

- **wireguard-windows `conf` 在非 Windows 构建失败**：由 import
  `driver`/`x/sys/windows` 且 `driver/*_windows.go` 推断，未实际交叉编译验证。
- **内核实现 vs 用户态实现的重复 PublicKey 行为**：wireguard-go UAPI 明确按 key
  合并（`device/uapi.go` `handlePublicKeyLine()`）；Linux kernel netlink 路径未核验。
  本方案选择“解析期报错”，不依赖该差异。
- **`Address` 前缀在 netstack 中的效果**：源码显示只用主机地址并加默认路由；
  子网路由/多地址优先级未实测。
- **macOS/BSD 的 `wg-quick` 脚本**：只核验了 `linux.bash`；`darwin.bash` 等
  的解析逻辑假设一致，未逐行核对。
- **搜索域的 app 层模拟**（按后缀追加查询）未实现，是否需要属于产品决策。
- **Endpoint 主机名重解析/漫游**：ticket 04 与 map 的遗留问题；本 ticket 只规定
  “解析期不解析”。
- **GitHub API 不可用**：维护状态用模块代理的发布时间与源码判断，未核对
  star/issue/最后 commit（`jamescun/wireguard` 是否仍存在也未确认）。
- `mdlayher/wgconf` 的“不存在”结论基于 pkg.go.dev/GitHub 搜索与模块代理失败；
  未直接查询 GitHub API。

---

## 7. 参考（一手 URL）

- wireguard-tools 解析器：https://git.zx2c4.com/wireguard-tools/plain/src/config.c
- wireguard-tools setconf：https://git.zx2c4.com/wireguard-tools/plain/src/setconf.c
- key 编码：https://git.zx2c4.com/wireguard-tools/plain/src/encoding.c
- 空白定义：https://git.zx2c4.com/wireguard-tools/plain/src/ctype.h
- wg-quick Linux 脚本：https://git.zx2c4.com/wireguard-tools/plain/src/wg-quick/linux.bash
- wg(8)：https://git.zx2c4.com/wireguard-tools/plain/src/man/wg.8
- wg-quick(8)：https://git.zx2c4.com/wireguard-tools/plain/src/man/wg-quick.8
- wireguard-go netstack：https://git.zx2c4.com/wireguard-go/plain/tun/netstack/tun.go
- wireguard-go UAPI：https://git.zx2c4.com/wireguard-go/plain/device/uapi.go
- wireguard-windows conf parser：https://git.zx2c4.com/wireguard-windows/plain/conf/parser.go
- wireguard-windows conf types：https://git.zx2c4.com/wireguard-windows/plain/conf/config.go
- wireguard-windows tests：https://git.zx2c4.com/wireguard-windows/plain/conf/parser_test.go
- wgctrl（模块）：https://golang.zx2c4.com/wireguard/wgctrl
- wgctrl UAPI 编码：模块 zip `internal/wguser/configure.go`
- nmiculinic/wg-quick-go：https://github.com/nmiculinic/wg-quick-go
- parrotmac/wg-quick-go：https://github.com/parrotmac/wg-quick-go
- uinta-labs/wg-quick-go：https://github.com/uinta-labs/wg-quick-go
- afilipovski/wg-quick-go：https://github.com/afilipovski/wg-quick-go
- QuantumGhost/wg-quick-go：https://github.com/QuantumGhost/wg-quick-go
- iproute2 地址前缀默认值：https://git.kernel.org/pub/scm/network/iproute2/iproute2.git/plain/ip/ipaddress.c
- 模块版本/日期元数据：`https://goproxy.cn/<module>/@v/list`、`@latest`、`@v/<v>.info`
