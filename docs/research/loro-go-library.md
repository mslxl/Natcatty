# Loro CRDT 的官方 Go 支持调研

Status: research
Date: 2026-11-03
Scope: 只核实 **loro-dev 官方**是否提供 Go 语言库或 Go 绑定：官方 GitHub 组织与仓库、官方发布物、pkg.go.dev、Go module proxy、官方文档，以及作为官方绑定策略的 C FFI / UniFFI / WASM 面。第三方与社区 Go 包按要求不在本报告范围内：不列出、不评估、不作为推荐。文中的“官方”仅指 `github.com/loro-dev/*` 下由 Loro 维护者发布的代码与包。

Sources verified against:

- `loro-dev/loro` `main` @ `75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73`（2026-10-23）；crate `loro` **1.17.1**（crates.io，2026-10-22）。
- `loro-dev/loro-ffi` tag **v1.17.1**（commit `2a5eb764bd9aba6ee50bb532ad0c5983e6e17c35`，2026-10-22）；crate `loro-ffi` **1.17.1**（crates.io，2026-10-22）；依赖 UniFFI **0.31.1**。
- `loro-dev/loro` 的 WASM crate **loro-wasm 1.17.1**；npm `loro-crdt` **1.17.1**（2026-10-22）。
- `loro-dev/loro-swift` **1.17.1**（GitHub Release，2026-10-22）；PyPI `loro` **1.17.1**（2026-10-22）。
- `loro-dev/loro-docs` `main` @ `fed85892aa01718072a862d29c48077d427a4e02`（2026-10-22）。官方文档站 `loro.dev` 对本次自动抓取返回 HTTP 403，因此文档结论以官方文档源仓库 `loro-dev/loro-docs` 的 MDX 为准（该仓库即站点源）。
- pkg.go.dev、proxy.golang.org、GitHub API 于 **2026-11-03** 查询。
- UniFFI 官方文档（`mozilla.github.io/uniffi-rs/latest`）于 **2026-11-03** 查询。

## 1. 结论

**没有。截至 2026-11-03，loro-dev 没有发布官方 Go 库、官方 Go binding，也没有可 `go get` 的官方 Go package。**

判断依据可以归纳为五条互相独立的证据：

1. **组织级不存在 Go 仓库。** `loro-dev` 的 30 个公开仓库中没有任何 Go 仓库，也没有 `loro-go` 仓库（访问返回 404）([GitHub org repos API](https://api.github.com/orgs/loro-dev/repos?per_page=100), [github.com/loro-dev/loro-go](https://github.com/loro-dev/loro-go))。
2. **核心仓库没有 Go 代码。** 当前 `loro` 的 Cargo workspace 有 15 个成员，全部是 Rust crate，没有 Go、没有 FFI crate、没有 C 头文件；`main` 的递归文件树中没有 `.go` 文件 ([Cargo.toml#L1-L18](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/Cargo.toml#L1-L18), [git tree](https://api.github.com/repos/loro-dev/loro/git/trees/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73?recursive=1))。
3. **Go 生态侧不存在官方模块。** `proxy.golang.org` 对 `github.com/loro-dev/loro-go` 和 `github.com/loro-dev/loro-ffi` 都返回 404；pkg.go.dev 也没有任何 loro-dev 的 Go library package ([proxy: loro-go](https://proxy.golang.org/github.com/loro-dev/loro-go/@latest), [proxy: loro-ffi](https://proxy.golang.org/github.com/loro-dev/loro-ffi/@latest), [pkg.go.dev search](https://pkg.go.dev/search?q=loro&m=package))。
4. **官方文档自己把 Go 归为社区维护。** 官方 Getting Started 在列出 NPM / Rust / Swift / Python 包之后，把 C# 与 Go 明确标为 “Community-maintained”；`loro-ffi` 的 README 同样把 Go binding 标为 “(community)” ([get_started.mdx#L8-L18](https://github.com/loro-dev/loro-docs/blob/fed85892aa01718072a862d29c48077d427a4e02/pages/docs/tutorial/get_started.mdx#L8-L18), [loro-ffi README#L15-L21](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/README.md#L15-L21))。
5. **维护者明确说过 Go 不受支持。** 在官方 issue #655 中，维护者 `zxch3n` 回复：“Go is not supported because we didn't find a good way to build the FFI from Rust”，并说明通用 WASM/WASI 支持“not in our plan right now”；该 issue 至 2026-11-03 仍为 open ([issue #655 comment](https://github.com/loro-dev/loro/issues/655#issuecomment-2676286832))。

因此，本报告不存在“官方 Go 包的版本 / 许可证 / 平台矩阵”可列；第 4 节列出的许可证与平台信息只适用于官方 Rust / JS-WASM / Swift / Python / FFI 产物。第三方和社区 Go 包按范围要求不列出、不评估。

## 2. 证据

### 2.1 官方核心仓库：只有 Rust 与 WASM crate

`loro-dev/loro` 的 workspace 成员如下（原文 15 项）：

```toml
[workspace]
members = [
    "crates/loro",
    "crates/examples",
    "crates/bench-utils",
    "crates/rle",
    "crates/loro-common",
    "crates/loro-internal",
    "crates/loro-wasm",
    "crates/fuzz",
    "crates/fractional_index",
    "crates/dev-utils",
    "crates/delta",
    "crates/kv-store",
    "crates/loro-wasm-tools",
    "crates/generic-btree",
    "crates/json-float-consumer",
]
```

来源：[Cargo.toml#L1-L18](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/Cargo.toml#L1-L18)。列表中没有 `crates/loro-ffi`，也没有任何 Go 相关目录；对 `main` 的递归 tree 做全量扫描也没有 `.go` 或 `.h` 文件（[git tree](https://api.github.com/repos/loro-dev/loro/git/trees/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73?recursive=1)）。

核心 README 对语言支持的表述是：

> “You can now use it in Rust, JS (via WASM), and Swift.”

来源：[README.md#L48](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/README.md#L48)。README 的 Bindings 一节没有再列 Go，只把其他语言指向独立的 `loro-ffi` 仓库（[README.md#L143-L145](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/README.md#L143-L145)）。

组织层面，`loro-dev` 的 30 个公开仓库按 GitHub `language` 字段统计为：Rust 10、TypeScript 13、Swift 2、JavaScript 2、MDX 2、未标注 1，没有 Go；仓库名中也没有 `loro-go`（[org repos API](https://api.github.com/orgs/loro-dev/repos?per_page=100)）。直接访问 `https://github.com/loro-dev/loro-go` 返回 404（[链接](https://github.com/loro-dev/loro-go)）。

### 2.2 Go module 与 pkg.go.dev：没有可导入的官方库

- `proxy.golang.org` 对 `github.com/loro-dev/loro-go` 返回 **404 not found**（[URL](https://proxy.golang.org/github.com/loro-dev/loro-go/@latest)）。
- `proxy.golang.org` 对 `github.com/loro-dev/loro-ffi` 同样返回 **404 not found**（[URL](https://proxy.golang.org/github.com/loro-dev/loro-ffi/@latest)）。
- pkg.go.dev 的 `loro` 搜索页没有任何 loro-dev 官方 Go library；唯一出现的 loro-dev 路径是历史示例模块 `github.com/loro-dev/loro/crates/loro-ffi/examples`，v0.6.0，类型是 **command**、没有 package 文档、不可作为库导入（[pkg.go.dev search](https://pkg.go.dev/search?q=loro&m=package), [examples command](https://pkg.go.dev/github.com/loro-dev/loro/crates/loro-ffi/examples)）。
- pkg.go.dev 上确实存在 `github.com/loro-dev/loro` 的 module 页面，但它指向 2023 年的 **v0.6.0**，页面没有 package 文档；该 tag 的仓库里也没有 `go.mod`，只有旧的 FFI 示例 `main.go`。这不是 Go 库，只是 Go 工具链对 Rust 仓库 tag 的历史索引副作用（[pkg.go.dev module page](https://pkg.go.dev/github.com/loro-dev/loro), [v0.6.0 tree](https://api.github.com/repos/loro-dev/loro/git/trees/v0.6.0?recursive=1)）。

结论：**不存在任何官方可导入的 Go package**。一个 Go 项目无法通过 `go get github.com/loro-dev/...` 获得 Loro 的官方 Go API。

### 2.3 官方文档与官方 README 的定性

官方文档的 Getting Started 列出了各语言的入口，并把其中两类明确标为社区维护：

- NPM `loro-crdt`
- Rust crate `loro`
- Swift package `loro-swift`
- Python package `loro-py`
- **C#：Community-maintained**
- **Go：Community-maintained**

来源：[get_started.mdx#L8-L18](https://github.com/loro-dev/loro-docs/blob/fed85892aa01718072a862d29c48077d427a4e02/pages/docs/tutorial/get_started.mdx#L8-L18)（按本报告范围，不复制其中的社区项目名与链接）。

`loro-ffi` 的 README 同样如此：Swift、Python（pyo3）、React Native 是官方仓库；C# 与 Go 标注为 “(community)”：

- [loro-ffi README#L15-L21](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/README.md#L15-L21)

这相当于官方自己给出了分类：**Go 不在 loro-dev 的官方支持语言之内**。

维护者在 issue #655（标题 “wasm-unknown-unknown/wasm-wasi(p1)”）中的原话进一步确认了原因：

> “Go is not supported because we didn't find a good way to build the FFI from Rust”

同一评论中还写到通用 WASM/WASI 支持 “It's not in our plan right now.” 来源：[issue #655, comment 2676286832, 2025-02-22](https://github.com/loro-dev/loro/issues/655#issuecomment-2676286832)。该 issue 在 2026-11-03 仍处于 open 状态（[issue #655](https://github.com/loro-dev/loro/issues/655)）。2026-10 的官方文档仍把 Go 标为 community，与 2025 年的这一表态一致。

补充历史：2024 年引入 UniFFI 的官方 issue #420 曾把 Go 列为 UniFFI “可以支持”的语言之一，但这只是当时的设想；最终上游 UniFFI 没有 Go backend，loro-dev 也没有产出 Go 绑定（[issue #420](https://github.com/loro-dev/loro/issues/420), [UniFFI docs](https://mozilla.github.io/uniffi-rs/latest/)）。

### 2.4 官方绑定策略：旧 C FFI 已移除，现行是 UniFFI + WASM + 各语言独立仓库

#### 2.4.1 历史：v0.6.0 曾有 cbindgen C FFI 和一个 Go cgo 示例（不是库）

在 **v0.6.0**（2023-11-27，commit `49f89c15569be5b338d12cb9401734514da061a0`）中，`loro` 仓库内确实存在 `crates/loro-ffi`：

- `Cargo.toml` 声明 `[lib] crate-type = ["staticlib", "cdylib"]`、`name = "loro"`，并使用 `cbindgen 0.24.3` 生成 C 头文件：[v0.6.0 ffi Cargo.toml](https://github.com/loro-dev/loro/blob/v0.6.0/crates/loro-ffi/Cargo.toml)。
- `cbindgen.toml` 明确 `language = "C"`：[v0.6.0 cbindgen.toml](https://github.com/loro-dev/loro/blob/v0.6.0/crates/loro-ffi/cbindgen.toml)。
- `examples/` 下有一个 **Go 示例** `main.go`，用 `#cgo LDFLAGS: -L./lib -framework Security -lloro` 直接调用 `loro_new` / `text_insert` 等 C 符号：[v0.6.0 examples/main.go#L1-L18](https://github.com/loro-dev/loro/blob/v0.6.0/crates/loro-ffi/examples/main.go#L1-L18)。
- 同目录的 README 在 “Go” 一节只写 `go run main.go`，并链接 cgo 文档；这是**示例程序**，不是可导入的 Go 包：[v0.6.0 ffi README](https://github.com/loro-dev/loro/blob/v0.6.0/crates/loro-ffi/README.md)。

这个 crate 后来被移除：commit `e08631a86d2821e9efb633771db1d8f0e02695da`，提交信息为 “refactor: rm loro-ffi (#749)”，日期 **2025-06-16**（[commit](https://github.com/loro-dev/loro/commit/e08631a86d2821e9efb633771db1d8f0e02695da)）。因此，**目前可核实到的、最接近“官方 Go 支持”的产物只是一段 cgo 示例代码，而且已在 2025 年被删除**。

#### 2.4.2 现在：独立的 `loro-ffi`，基于 UniFFI，没有 Go backend

`loro-dev/loro-ffi` 是 2025-06-06 创建的独立官方仓库，默认分支 `main`，MIT（[repo metadata](https://api.github.com/repos/loro-dev/loro-ffi)）。当前发布版本为 **1.17.1**：

- `Cargo.toml`：`name = "loro-ffi"`、`version = "1.17.1"`、`license = "MIT"`；依赖 `loro = "1.17.1"` 与 `uniffi = "0.31.1"`；`[[bin]] uniffi-bindgen`：[Cargo.toml#L1-L25](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/Cargo.toml#L1-L25)。
- `build.rs` 调用 `uniffi::generate_scaffolding("src/loro.udl")`；接口定义在 2236 行的 UDL 中，包含 `LoroDoc`、`LoroText`、`LoroList`、`LoroMap`、`LoroTree`、`LoroMovableList`、`LoroCounter`、`UndoManager`、`Awareness`、`EphemeralStore`、`VersionVector`、`Frontiers`、`Cursor` 以及多种回调接口（[loro.udl#L105](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/src/loro.udl#L105), [src/](https://github.com/loro-dev/loro-ffi/tree/v1.17.1/src)）。
- `uniffi.toml` **只配置了 Swift**（`[bindings.swift]`），没有 Go 或 C 的生成配置：[uniffi.toml#L1-L3](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/uniffi.toml#L1-L3)。
- 仓库内的绑定测试只有 Swift 与 Python（`tests/bindings/pending_updates.swift`、`tests/bindings/test_pending_updates.py`），没有任何 Go 测试或 Go 目录：[tests/bindings](https://github.com/loro-dev/loro-ffi/tree/v1.17.1/tests/bindings)。
- README 的 “Build Binding” 要求使用者“围绕 UniFFI 的绑定结果自行构建语言 wrapper 层”，并明确说 “The FFI package and generated language packages are versioned and published separately from the core.”（[README.md#L57-L61](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/README.md#L57-L61)）；Build Binding 一节要求使用者自行构建语言 wrapper（[README.md#L87-L100](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/README.md#L87-L100)）。

UniFFI 本身也没有官方 Go 目标。UniFFI 文档列出的完整支持语言是 **Kotlin、Swift、Python**，另提供 **Ruby**；Go 不在官方列表中（[UniFFI docs](https://mozilla.github.io/uniffi-rs/latest/)）。因此，即使 `loro-ffi` 使用 UniFFI，也不会自动产生 Go 绑定。

#### 2.4.3 `loro-ffi` 不发布预编译 C 库或头文件

这一点对“Go 用 cgo 调官方 FFI”的可行性很关键：

- `loro-ffi` 的 `Cargo.toml` **没有 `[lib]` 段**，即 registry 包默认只构建 Rust library，不声明 `cdylib` / `staticlib` crate-type（[Cargo.toml](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/Cargo.toml)）。
- 官方的验证脚本在临时副本中手动追加 `crate-type = ["rlib", "cdylib"]` 才生成 C 动态库：[scripts/verify-pending-updates.py#L43](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/scripts/verify-pending-updates.py#L43)。
- 仓库中没有任何 `.a` / `.so` / `.dylib` / `.dll` / `.h` / `.hpp` 产物；GitHub Releases 数量为 **0**；唯一的发布 workflow 只执行 `cargo publish`（发布源码到 crates.io）：[publish.yml](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/.github/workflows/publish.yml), [releases API](https://api.github.com/repos/loro-dev/loro-ffi/releases)。
- GitHub 上有 `v1.17.1` 等 tag，但 tag 页面只是源码快照，没有二进制资产（[tags](https://github.com/loro-dev/loro-ffi/tags)）。

也就是说，官方**没有提供一个可直接链接的 C 库或稳定的 C 头文件**；cgo 方案必须由使用方自行用 Rust toolchain 构建 UniFFI 的 `cdylib`/`staticlib`，并自行处理 UniFFI 的 buffer 所有权、错误映射和回调 vtable。

#### 2.4.4 WASM：官方产物是 wasm-bindgen + JS 胶水，不是 WASI

官方 WASM crate 是 `crates/loro-wasm`：

- `name = "loro-wasm"`、`version = "1.17.1"`、`crate-type = ["cdylib", "rlib"]`、`publish = false`；依赖 `js-sys = "0.3.60"`、`wasm-bindgen = "=0.2.100"`、`serde-wasm-bindgen`（[Cargo.toml#L8-L25](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/crates/loro-wasm/Cargo.toml#L8-L25)）。
- 它打包为 npm 官方包 `loro-crdt` **1.17.1**，`package.json` 的入口全部是 JS：`nodejs/index.js`、`bundler/index.js`、`browser/index.js`、`web/index.js`（[package.json#L2-L3](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/crates/loro-wasm/package.json#L2-L3), [npm metadata](https://registry.npmjs.org/loro-crdt/1.17.1)）。
- 对官方 tarball `loro-crdt-1.17.1.tgz` 的实际检查（[tarball](https://registry.npmjs.org/loro-crdt/-/loro-crdt-1.17.1.tgz)）显示：包内有 4 个 `loro_wasm_bg.wasm`（`browser`、`bundler`、`nodejs`、`web` 四个目标），每个 wasm 都配套 `loro_wasm.js` 和 `snippets/` JS 胶水（`browser` 与 `bundler` 目标还各有 `loro_wasm_bg.js`）；对 `package/bundler/loro_wasm_bg.wasm` 的 import section 解析结果为 **117 个导入，全部来自 `./loro_wasm_bg.js`**。这意味着该模块的运行前提是 JS 宿主提供 wasm-bindgen 导入，而不是独立的 WASI 模块。
- 官方 issue #655 正是请求 `wasm32-unknown-unknown/wasm32-wasi` 的通用支持，维护者回复该方向不在计划内（[issue #655](https://github.com/loro-dev/loro/issues/655#issuecomment-2676286832)）。

因此，官方 WASM 产物面向 browser / Node / bundler 等 JS 宿主（包内另有 `web` 目标）；官方没有发布 WASI 构建，也没有提供可被非 JS 运行时直接消费的 `wasm32-wasip1` 模块。

### 2.5 官方已发布的兄弟绑定（绑定策略的旁证）

| 语言 | 官方包 / 仓库 | 版本与日期 | 形态 | 许可 |
|---|---|---|---|---|
| Rust | crate `loro` | 1.17.1，2026-10-22 | Rust library | MIT |
| JavaScript / TypeScript | npm `loro-crdt`（`loro-dev/loro` 的 `loro-wasm`） | 1.17.1，2026-10-22 | WASM + JS（wasm-bindgen） | MIT |
| Swift | `loro-dev/loro-swift` | 1.17.1，2026-10-22 | 预编译 xcframework + Swift wrapper | MIT |
| Python | PyPI `loro`（`loro-dev/loro-py`） | 1.17.1，2026-10-22 | pyo3 cdylib wheels | MIT |
| React Native | `loro-dev/loro-react-native` | 官方 README 列为官方绑定 | Turbo Module | MIT |
| **Go** | **无官方包** | **—** | **—** | **—** |

证据：

- crate `loro` 1.17.1：[crates.io](https://crates.io/crates/loro/1.17.1)；`Cargo.toml` 声明 `license = "MIT"`（[Cargo.toml#L1-L8](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/crates/loro/Cargo.toml#L1-L8)）。
- npm `loro-crdt` 1.17.1：[registry](https://registry.npmjs.org/loro-crdt/1.17.1)（`license: MIT`，`repository: git+https://github.com/loro-dev/loro.git`）。
- Swift 1.17.1：[release](https://github.com/loro-dev/loro-swift/releases/tag/1.17.1)；`Package.swift` 的平台声明为 iOS 13、macOS 10.15、visionOS 1，并从 release 下载 `loroFFI.xcframework.zip`（[Package.swift](https://github.com/loro-dev/loro-swift/blob/main/Package.swift)）；仓库 LICENSE 为 MIT（[license API](https://api.github.com/repos/loro-dev/loro-swift/license)）。
- PyPI `loro` 1.17.1：[PyPI](https://pypi.org/project/loro/1.17.1/)；其仓库 `loro-dev/loro-py` 的 `Cargo.toml` 使用 `pyo3 = "0.26.0"`、`crate-type = ["cdylib"]`（[Cargo.toml](https://github.com/loro-dev/loro-py/blob/main/Cargo.toml)）；PyPI 元数据没有声明 license 字段，但仓库 LICENSE 为 MIT（[GitHub license API](https://api.github.com/repos/loro-dev/loro-py/license)）。
- React Native 官方仓库存在且被 `loro-ffi` README 列为官方绑定（[README#L19](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/README.md#L19)），仓库 LICENSE 为 MIT（[license API](https://api.github.com/repos/loro-dev/loro-react-native/license)）；本报告未把它的 npm 发布状态作为核心证据。

**注意**：表里的 Go 行标为“无官方包”，这不是遗漏。

## 3. 如果坚持用 Go：今天现实可走的官方集成面

前提：以下三条都不是“官方 Go 库”。它们只是官方发布的代码/产物，Go 侧集成需要自建。按范围要求，本节不列出任何第三方 Go 包。

### 3.1 方案 A：自建 cgo，封装官方 `loro-ffi` 的 UniFFI C ABI

**能拿到什么。** `loro-ffi` 的 UDL 覆盖了核心 API 的主要面：`LoroDoc`、文本/列表/映射/树/可移动列表/计数器、嵌套容器、undo、awareness、ephemeral store、版本向量/frontiers、cursor、事件订阅、JSONPath 订阅等（[loro.udl](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/src/loro.udl), [src/](https://github.com/loro-dev/loro-ffi/tree/v1.17.1/src)）。如果自己把这层 C ABI 接出来，API 覆盖不是主要问题。

**要自己承担什么。**

- **没有预编译库，也没有官方 C 头文件。** 必须用 Rust toolchain 为每个目标平台构建 `cdylib`/`staticlib`；官方验证脚本也是临时改 `Cargo.toml` 加 `crate-type = ["rlib", "cdylib"]` 才构建的（[verify script#L43](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/scripts/verify-pending-updates.py#L43)）。
- **UniFFI ABI 不是 Loro 承诺的稳定 C API。** `loro-ffi` 的 `uniffi.toml` 只配置 Swift，仓库没有任何 C 头文件或 C 示例；C ABI 是 UniFFI 生成的 scaffolding 细节，Loro 没有把它作为稳定 C API 承诺，跨 UniFFI 版本存在变化风险（[uniffi.toml](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/uniffi.toml), [Cargo.toml](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/Cargo.toml)）。
- **内存与错误映射要手写。** UniFFI 的 buffer/字符串/对象所有权约定需要在 Go 侧显式实现；`[Throws=...]` 的错误也要映射到 Go error。
- **回调需要 foreign vtable。** 事件订阅是 UniFFI trait interface，例如 `Subscriber: Sync + Send { fn on_diff(...) }`（[event.rs#L13-L16](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/src/event.rs#L13-L16)）；Go 侧必须实现对应的回调 vtable，并处理 Rust 线程回调进入 Go 的线程/栈语义。官方没有为此提供 Go 运行时或示例。
- **构建矩阵。** 每个 GOOS/GOARCH 都要有匹配的 Rust target 和 C 库；没有官方二进制发布，CI 需要自己交叉编译或分平台构建。

**适用场景。** 需要进程内调用、能接受自建 FFI 层和长期 ABI 维护成本的 Go 项目。

### 3.2 方案 B：用 wazero 跑官方 WASM

**不推荐，且不是官方支持路径。** 官方 npm 包里的 `.wasm` 是 wasm-bindgen + JS 目标，不是 WASI：四个 wasm 文件都配套 JS 胶水，`bundler/loro_wasm_bg.wasm` 从 `./loro_wasm_bg.js` 导入 117 个函数（见 2.4.4 的 tarball 检查）。wazero 能提供自定义 host functions，但不能执行 JS 胶水；要用 wazero，就得自己实现这 117 个 wasm-bindgen 导入（包括 JS 值/对象语义），相当于重写 wasm-bindgen 的 ABI 层。

此外，官方明确说过通用 WASM/WASI 不在计划内，issue #655 仍 open（[issue #655](https://github.com/loro-dev/loro/issues/655#issuecomment-2676286832)）。当前没有任何官方 `wasm32-wasip1` 构建可供 wazero 直接加载。

**适用场景。** 除非你愿意维护一个 wasm-bindgen ABI 兼容层，否则不建议把 wazero 作为 Loro 的 Go 集成方案。

### 3.3 方案 C：Rust sidecar / 子进程

**官方没有面向 Loro 文档操作的 CLI，但这是最容易保持官方语义的路径。** 官方 workspace 没有 CLI crate，`crates/loro` 是库 crate（[Cargo.toml#L1-L18](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/Cargo.toml#L1-L18)）。实际做法是：

1. 用官方 `loro` crate（MIT，1.17.1）写一个小的 Rust 二进制；
2. 定义 Go 与 sidecar 之间的稳定协议（例如 JSON/CBOR/protobuf over stdin/stdout 或本地 socket）；
3. 在 Go 侧管理进程生命周期、超时、重启和版本匹配。

**优点。** 完全复用官方 Rust API，不受 UniFFI ABI 和 cgo 构建矩阵影响；Rust 崩溃不会拖垮 Go 进程。
**缺点。** 有 IPC 序列化/延迟开销；部署多一个二进制；协议和并发模型要自己设计；这仍然不是“官方 Go 绑定”。

### 3.4 三条路径对比

| 路径 | 是否官方支持 | API 覆盖 | 主要成本 | 主要风险 |
|---|---|---|---|---|
| cgo over `loro-ffi` | 否（自建集成） | UDL 覆盖较全 | 自建 Rust cdylib、ABI 封装、回调 vtable | UniFFI ABI 漂移、跨平台构建、内存安全 |
| wazero + `loro-crdt` wasm | 否（官方无 WASI） | 受官方 WASM 导出限制 | 实现 117 个 wasm-bindgen 导入 | 实际上是在重写 wasm-bindgen |
| Rust sidecar | 否（自建集成） | 官方 Rust API 全量 | 写 Rust 二进制 + IPC 协议 | IPC 性能、进程管理、版本匹配 |

## 4. 许可证与平台约束（仅官方产物）

| 官方产物 | 版本 / 日期 | 许可证 | 平台 / 形态 |
|---|---|---|---|
| crate `loro` | 1.17.1 / 2026-10-22 | MIT（`Cargo.toml`、`LICENSE`） | Rust 支持的 target；无固定官方矩阵 |
| crate `loro-ffi` | 1.17.1 / 2026-10-22 | MIT（`Cargo.toml`、`LICENSE`） | 源码 only；无预编译库/头文件；需按 Rust target 自行构建 |
| npm `loro-crdt` | 1.17.1 / 2026-10-22 | MIT | browser / Node / bundler / web（wasm-bindgen） |
| `loro-dev/loro-swift` | 1.17.1 / 2026-10-22 | MIT | iOS 13+、macOS 10.15+、visionOS 1；预编译 xcframework |
| PyPI `loro` | 1.17.1 / 2026-10-22 | MIT（仓库 LICENSE；PyPI 元数据未声明） | macOS x86_64/arm64；Linux manylinux（aarch64、armv7l、ppc64le、s390x、x86_64、i686）与 musllinux（aarch64、armv7l、i686、x86_64）；Windows win32/win_amd64 |
| **官方 Go 包** | **不存在** | **—** | **—** |

证据：

- `loro` 的 MIT：`Cargo.toml` `license = "MIT"`（[Cargo.toml#L1-L8](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/crates/loro/Cargo.toml#L1-L8)），`LICENSE` 为 MIT（[LICENSE](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/LICENSE)）。
- `loro-ffi` 的 MIT：`Cargo.toml` `license = "MIT"`（[Cargo.toml#L7](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/Cargo.toml#L7)），`LICENSE` 为 MIT（[LICENSE](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/LICENSE)）。
- npm `loro-crdt` 的 MIT：registry 元数据（[1.17.1](https://registry.npmjs.org/loro-crdt/1.17.1)）。
- Swift 平台与二进制分发：[Package.swift](https://github.com/loro-dev/loro-swift/blob/main/Package.swift)、[1.17.1 release](https://github.com/loro-dev/loro-swift/releases/tag/1.17.1)。
- Python wheels 的平台标签：[PyPI files](https://pypi.org/project/loro/1.17.1/#files)。
- `loro-py` 仓库 LICENSE：[GitHub license API](https://api.github.com/repos/loro-dev/loro-py/license)。

## 5. 对 Go 项目的实际建议

1. **把“官方 Go 支持”当作不存在。** 截至 2026-11-03，没有官方 Go 库、Go module、pkg.go.dev package 或官方 Go 发布物；不要围绕一个不存在的官方包做架构假设。
2. **如果 Go 是硬约束，优先考虑 Rust sidecar。** 它使用官方 `loro` crate，API 最完整、语义最接近官方实现，也避开了 cgo 和 UniFFI ABI 的长期维护；代价是 IPC 和额外进程。对于桌面应用或服务端，这通常是可控的。
3. **cgo over `loro-ffi` 只适合愿意维护 FFI 层的团队。** 需要自建 Rust cdylib/staticlib、手写 UniFFI buffer/错误/回调映射，并维护多平台构建矩阵；官方没有 C 头文件、没有预编译库、也没有 Go 示例。
4. **不要把 wazero/WASI 作为近期方案。** 官方 WASM 是 wasm-bindgen + JS 胶水（117 个 JS 导入），没有 WASI 构建，维护者也明确说通用 WASM/WASI 不在计划内。
5. **如果语言可以选，优先官方 Rust / JS-WASM / Swift / Python。** 这些都有 loro-dev 自己发布并版本对齐（1.17.1，2026-10-22）的包。
6. **第三方 Go 包不在本报告范围。** 官方文档把它们标为 community-maintained；任何采用决定都应自行审计其版本、维护状态、许可证和 ABI 兼容性，本报告不为其背书。

## 6. 验证记录

| 检查项 | 结果 | 证据 |
|---|---|---|
| `github.com/loro-dev/loro-go` | HTTP 404 | [链接](https://github.com/loro-dev/loro-go) |
| Go proxy `github.com/loro-dev/loro-go` | HTTP 404 | [链接](https://proxy.golang.org/github.com/loro-dev/loro-go/@latest) |
| Go proxy `github.com/loro-dev/loro-ffi` | HTTP 404 | [链接](https://proxy.golang.org/github.com/loro-dev/loro-ffi/@latest) |
| pkg.go.dev `loro` 搜索 | 无 loro-dev Go library；仅有历史 example command | [搜索页](https://pkg.go.dev/search?q=loro&m=package)、[command](https://pkg.go.dev/github.com/loro-dev/loro/crates/loro-ffi/examples) |
| pkg.go.dev `github.com/loro-dev/loro` | module 页面，v0.6.0，无 package 文档 | [链接](https://pkg.go.dev/github.com/loro-dev/loro) |
| `loro-dev` 公开仓库 | 30 个；无 Go 仓库 | [API](https://api.github.com/orgs/loro-dev/repos?per_page=100) |
| `loro` workspace | 15 个 Rust crate；无 ffi/go | [Cargo.toml](https://github.com/loro-dev/loro/blob/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73/Cargo.toml#L1-L18) |
| `loro` main 文件树 | 无 `.go` / `.h` / ffi crate | [tree API](https://api.github.com/repos/loro-dev/loro/git/trees/75e4bf2bf01cb6f2f3e37c50156ca0b335ef9b73?recursive=1) |
| `loro-ffi` GitHub Releases | 0 个 release 资产 | [releases API](https://api.github.com/repos/loro-dev/loro-ffi/releases) |
| `loro-ffi` 二进制/头文件 | 无 `.a`/`.so`/`.dylib`/`.dll`/`.h` | [repo tree](https://github.com/loro-dev/loro-ffi/tree/v1.17.1) |
| `loro-ffi` 的 `[lib]` | 无 `[lib]` crate-type；验证脚本临时加 cdylib | [Cargo.toml](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/Cargo.toml)、[script#L43](https://github.com/loro-dev/loro-ffi/blob/v1.17.1/scripts/verify-pending-updates.py#L43) |
| 官方 npm wasm 导入 | 117 个导入，全部来自 `./loro_wasm_bg.js` | [tarball](https://registry.npmjs.org/loro-crdt/-/loro-crdt-1.17.1.tgz)（解析 `package/bundler/loro_wasm_bg.wasm`） |
| 维护者关于 Go 的表态 | “Go is not supported because we didn't find a good way to build the FFI from Rust” | [issue #655 comment](https://github.com/loro-dev/loro/issues/655#issuecomment-2676286832) |
| 旧 C FFI / Go 示例 | v0.6.0 存在，2025-06-16 移除 | [v0.6.0 main.go](https://github.com/loro-dev/loro/blob/v0.6.0/crates/loro-ffi/examples/main.go)、[commit #749](https://github.com/loro-dev/loro/commit/e08631a86d2821e9efb633771db1d8f0e02695da) |
| UniFFI 支持语言 | Kotlin / Swift / Python / Ruby；不含 Go | [UniFFI docs](https://mozilla.github.io/uniffi-rs/latest/) |

**一句话总结：** Loro 是一个官方支持 Rust、JS/WASM、Swift、Python（以及 React Native）的 CRDT 库；**Go 从来不是官方支持语言**。目前可核实到的、最接近官方 Go 支持的文件是 v0.6.0 的 cgo 示例，早已随旧 C FFI 一并删除。Go 项目若要使用 Loro，今天只能围绕官方 Rust crate / UniFFI / WASM 产物自建集成层，不能依赖一个不存在的官方 Go 库。
