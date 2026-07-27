# Issue #2506: Tabby / Electerm 字体选择对照

研究日期：2026-07-27

源码版本：

- Tabby：[`14e2d60b9b6dee84a53c37f05eefeb803787de04`](https://github.com/Eugeny/tabby/commit/14e2d60b9b6dee84a53c37f05eefeb803787de04)
- Electerm：[`7dfb33ed19352430f0303ca14e379d9b2387f390`](https://github.com/electerm/electerm/commit/7dfb33ed19352430f0303ca14e379d9b2387f390)

## 结论

[Issue #2506](https://github.com/binaricat/Netcatty/issues/2506) 提议的字体搜索是成熟且范围可控的改进。Tabby 和 Electerm 的共同做法不是继续扩充一份内置字体清单，而是：读取本机字体、支持按名称搜索，同时保留手动输入，避免系统字体读取失败或漏报时把用户卡死。

两者在备用字体上的取舍不同：Tabby 是“主字体 + 一个备用字体”，含义清楚，最接近 Netcatty 现有模型；Electerm 允许用户排列任意长度的字体链，更灵活，但也更容易出现顺序配置问题。对 #2506，建议沿用 Netcatty 的主字体 / 中文字体分工，把界面字体和终端主字体改成可搜索选择即可，不必引入任意字体链。

## 对照

| 问题 | Tabby | Electerm |
|---|---|---|
| 支持搜索 | 是。字体框是带自动补全的文本框；输入后等待 200 ms，按名称大小写不敏感地包含匹配并去重（[界面](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-terminal/src/components/appearanceSettingsTab.component.pug#L6-L12)，[过滤逻辑](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-terminal/src/components/appearanceSettingsTab.component.ts#L22-L32)）。 | 是。字体选择器明确开启搜索，按名称大小写不敏感地包含匹配（[源码](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/common/font-select.jsx#L32-L43)）。 |
| 枚举系统字体 | 是。Windows / macOS 读取所有可用字体族；Linux 用 `fc-list :spacing=mono` 只列等宽字体（[源码](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-electron/src/services/platform.service.ts#L209-L224)）。Web 版无法枚举时返回空列表（[源码](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-web/src/platform.ts#L66-L68)）。 | 是。主进程通过 `font-list` 读取字体族，去掉名称中的引号；失败时返回空列表，不阻塞启动（[源码](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/app/lib/font-list.js#L7-L16)）。 |
| 允许手动输入 | 是。控件本身就是文本输入框，建议列表不是封闭选项。 | 是。选择器使用标签模式，既能选本机字体，也能输入新字体（[源码](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/common/font-select.jsx#L32-L43)）。 |
| 备用 / 中文字体 | 有独立的备用字体输入框，说明文字明确表示它用于主字体缺失的字符；同样支持系统字体自动补全（[源码](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-terminal/src/components/appearanceSettingsTab.component.pug#L151-L160)）。最终顺序是主字体、用户备用字体、内置后备、系统等宽字体（[源码](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-core/src/utils.ts#L20-L28)）。它不单独命名为“中文字体”，但可以把中文字体填在这里。 | 没有独立的中文 / 备用字体字段。用户添加多个字体标签，保存时按顺序拼成一个字体链（[保存逻辑](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/setting-panel/setting-terminal.jsx#L89-L94)，[界面](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/setting-panel/setting-terminal.jsx#L432-L439)），再直接交给终端渲染（[源码](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/terminal/terminal.jsx#L1288-L1293)）。中文字体可以放在后续标签里。 |
| 预览与局部覆盖 | 没有在候选行逐项预览；主字体和备用字体共用同一个简单自动补全模型。 | 每个候选名称用该字体自身显示，能提供轻量预览（[源码](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/common/font-select.jsx#L14-L25)）。但单个连接的字体覆盖仍是普通文本框，并未复用全局搜索器（[源码](https://github.com/electerm/electerm/blob/7dfb33ed19352430f0303ca14e379d9b2387f390/src/client/components/bookmark-form/config/common-fields.js#L170-L175)）。 |

## 对 Netcatty 的建议

1. 界面字体和终端主字体使用同一种可搜索选择体验，按名称包含匹配；候选项用自身字体显示名称。
2. 继续读取本机字体，不要靠不断扩充内置清单来解决“字体少”。终端主字体仍优先展示等宽字体，避免比例字体破坏列对齐。
3. 保留手动输入。字体读取可能被拒绝、失败或漏报；跨设备同步过来的字体也可能只在另一台机器安装。
4. 保持现有“主字体 + 中文字体”模型。它比 Electerm 的任意字体链更容易理解，也和 Tabby 的做法一致。
5. 系统字体读取失败时，仍显示当前值和安全的内置选项；不要让设置页变成空列表。

## 本地验收字体包（macOS）

以下 8 个字体用于验收 #2506，不是对 X 热度的量化排名。选择标准是：开发者社交圈常见、开源、官方项目仍可访问，并且截至 2026-07-27 均能从 Homebrew 官方字体仓库安装。它们刻意覆盖了连字、窄字面、Nerd Font 图标、简体中文、手写风格中文，以及同一安装包内多个相近字体名等情况。

### 编程字体

| 字体 | Homebrew cask | 等宽 / CJK | Nerd Font | 验收价值 |
|---|---|---|---|---|
| [JetBrains Mono](https://www.jetbrains.com/lp/mono/) | [`font-jetbrains-mono`](https://formulae.brew.sh/cask/font-jetbrains-mono) | 等宽；不含 CJK 汉字 | 否；基础字体带少量 Powerline 符号 | 常见基准字体，名称搜索和连字预览都容易辨认。 |
| [Fira Code](https://github.com/tonsky/FiraCode) | [`font-fira-code`](https://formulae.brew.sh/cask/font-fira-code) | 等宽；不含 CJK 汉字 | 否；基础字体支持 Powerline | 连字丰富，适合检查候选项用自身字体展示时是否清晰。 |
| [Cascadia Code](https://github.com/microsoft/cascadia-code) | [`font-cascadia-code`](https://formulae.brew.sh/cask/font-cascadia-code) | 等宽；不含 CJK 汉字 | 此 cask 否；官方另有 NF 变体 | 官方明确区分 Code、Mono、Powerline、Nerd Font，适合检查相似名称搜索。 |
| [Iosevka](https://github.com/be5invis/Iosevka) | [`font-iosevka`](https://formulae.brew.sh/cask/font-iosevka) | 等宽家族；不含 CJK 汉字 | 此 cask 否；Homebrew 另有 Nerd Font 变体 | 字面较窄且家族变体多，适合检查长列表、相似名称和终端列宽。 |

### CJK / 中文等宽字体

| 字体 | Homebrew cask | 等宽 / CJK | Nerd Font | 验收价值 |
|---|---|---|---|---|
| [Maple Mono NF CN](https://font.subf.dev/en/) | [`font-maple-mono-nf-cn`](https://formulae.brew.sh/cask/font-maple-mono-nf-cn) | 中英文 2:1 等宽；含简体中文 | 是 | 一套字体同时覆盖代码、中文和图标，是最完整的终端实测样本。 |
| [Sarasa Gothic / 更纱黑体](https://github.com/be5invis/Sarasa-Gothic) | [`font-sarasa-gothic`](https://formulae.brew.sh/cask/font-sarasa-gothic) | 安装包含 `Sarasa Mono SC`、`Term SC`、`Fixed SC` 等 CJK 等宽变体，也含非等宽变体 | 否 | 同一安装包会出现大量相近家族名，最适合压力测试搜索和过滤。 |
| [LXGW WenKai GB / 霞鹜文楷 GB](https://github.com/lxgw/LxgwWenkaiGB) | [`font-lxgw-wenkai-gb`](https://formulae.brew.sh/cask/font-lxgw-wenkai-gb) | 安装包同时含 `LXGW WenKai Mono GB` 和非等宽版本；含简体中文 | 否 | 能验证搜索是否准确区分 Mono 与普通版本，也能观察中文风格差异。 |
| [Noto Sans Mono CJK SC](https://github.com/notofonts/noto-cjk/tree/main/Sans) | [`font-noto-sans-mono-cjk-sc`](https://formulae.brew.sh/cask/font-noto-sans-mono-cjk-sc) | 半角 ASCII + 全角简体中文，适合终端 2:1 排版 | 否 | 中性基准字体，适合判断中文列宽和回退是否正确。 |

建议一次安装这 8 个 cask，重启 Netcatty 后测试搜索、键盘选择、主字体与中文字体组合，以及 `A中B文 0O1lI -> !=` 和 Powerline / Nerd Font 图标的显示：

```sh
brew install --cask font-jetbrains-mono font-fira-code font-cascadia-code font-iosevka font-maple-mono-nf-cn font-sarasa-gothic font-lxgw-wenkai-gb font-noto-sans-mono-cjk-sc
```

基础版 JetBrains Mono 和 Fira Code 虽带少量 Powerline 符号，但不等同于完整 Nerd Font；本组只让 Maple Mono NF CN 承担完整图标字体测试，避免安装重复变体后让字体列表变得难以辨认。

## 范围说明

Tabby 与 Electerm 上述官方设置均针对终端字体。没有在所查官方设置源码中找到与 Netcatty“界面字体”完全对应的独立选择器；因此它们能直接证明的是搜索、系统字体枚举、手动输入和备用链的通用做法，而不是界面字体必须采用某个特定模型。
