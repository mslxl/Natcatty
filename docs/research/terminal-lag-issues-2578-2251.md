# Netcatty 终端卡顿调查：#2578 新反馈与 #2581 后续

日期：2026-07-29

代码基线：`origin/main` 的 `c3067c8dc2aebf7817f1b7c918a6f26dda53414d`

## 结论

- [PR #2581](https://github.com/binaricat/Netcatty/pull/2581) 解决的是密集关键词高亮的已知重绘瓶颈，不能视为 [#2578](https://github.com/binaricat/Netcatty/issues/2578) 的完整修复。
- #2578 的新截图确认：卡顿时一个 Netcatty renderer 进程的常驻内存约为 2,467 MB。报告者同时确认终端基本静止、回滚上限为 100,000、开启隐藏标签休眠后没有明显改善。
- 当前代码存在一个可稳定复现的独立缺口：隐藏但已经结束的远程会话不会进入休眠，完整 xterm 运行资源会继续保留。这与另一位用户“4–5 个终端、会话已经结束、静置一夜后超过 2 GB”的场景直接相关，适合做窄修复。
- 这个缺口不能自动解释原报告者的 15 个可能仍连接的终端。100,000 行回滚是重要放大条件，但现有信息还不能证明它是根因，也不能确认存在持续泄漏。

## 新评论提供了什么

| 来源 | 新信息 | 能确认到什么程度 |
| --- | --- | --- |
| [原报告者补充](https://github.com/binaricat/Netcatty/issues/2578#issuecomment-5113575336) | 约 5 个工作区、每个约 3 个终端；约 3 小时后切换工作区、切终端和输入都会卡；回滚为 100,000；终端基本静止；打开隐藏休眠后无明显改善。 | 现场条件已明确，持续后台输出不再是首要解释。 |
| [原报告者 htop 截图](https://github.com/binaricat/Netcatty/issues/2578#issuecomment-5113575336) | 选中的 Netcatty renderer 为 RES 2467M、SHR 165M、CPU 3.3%、MEM 3.8%；整机内存约 47.1G/62.6G。 | 能确认高常驻内存集中在 renderer；单张截图不能区分 xterm 历史、V8、DOM、图片、Agent 内容或其他资源。 |
| [另一位用户补充](https://github.com/binaricat/Netcatty/issues/2578#issuecomment-5113368945) | 两次看到 Netcatty 超过 2 GB 后严重卡死；当时约 4–5 个终端、1–2 个 Agent、会话应该都已结束，整夜静置且经历锁屏。 | 是有价值的独立线索；没有截图和进程拆分，不能单独确认泄漏。 |
| [维护方追问](https://github.com/binaricat/Netcatty/issues/2578#issuecomment-5113589065) | 建议把回滚降到 10,000、完全重启并按同样拓扑运行数小时。 | 这是尚未完成的对照实验，不应把建议写成结论。 |

截图中的 `VIRT` 是虚拟地址空间，不等于实际占用；`TIME+` 是累计 CPU 时间，也不能当成应用已运行时长。Electron 还会把 renderer 的常驻、私有、Blink 和 V8 堆内存分开统计，因此后续需要时间序列，而不是另一张单点截图。[Electron ProcessMemoryInfo](https://www.electronjs.org/docs/latest/api/structures/process-memory-info)、[Electron process memory APIs](https://www.electronjs.org/docs/latest/api/process#processgetprocessmemoryinfo)

## 与 #2251、#2581 的关系

#2581 将 `@xterm/xterm` 从 beta.220 升至 beta.221，并加入真实 Electron 的密集装饰回归测试。上游 [xterm.js PR #5902](https://github.com/xtermjs/xterm.js/pull/5902) 把装饰查询改为按逻辑行索引，官方给出的 20,000 个装饰扫描基准从 6385.83 ms 降至 1.80 ms。这个改动直接对应 #2251 中“密集关键词高亮后 DOM 与 WebGL 都慢”的子问题。

#2578 的现场条件不同：终端基本静止、问题随长时间运行出现，而且 renderer 常驻内存已经达到约 2.4 GiB。beta.221 可以减少已有装饰的绘制成本，但不会主动释放结束会话、缩小回滚缓冲或解释 renderer 内存增长。因此 #2578 不应因为 #2581 合并而关闭。

#2581 新增的 Linux Electron 检查在合并前后都没有真正运行到测试逻辑。GitHub runner 启动 Electron 时被 `chrome-sandbox` 权限检查拦住；这需要一个只作用于该 CI 步骤的 `ELECTRON_DISABLE_SANDBOX=1`。本地在相同环境变量下，真实测试会正常执行并覆盖实际关键词高亮路径和 20,000 个装饰。[失败检查](https://github.com/binaricat/Netcatty/actions/runs/30425243594/job/90490233979)

## 已确认的生命周期缺口

当前休眠链路有三处只允许 `connected`：

1. 隐藏后是否安排休眠；
2. 输出未排空时是否继续重试；
3. 生成快照后是否最终释放 xterm 运行资源。

因此远程会话一旦进入 `disconnected`，即使标签已经隐藏且用户开启了休眠，也不会执行运行资源释放。只要标签仍然保留，终端历史、渲染对象和附加组件就仍由 renderer 持有。[调度门禁](https://github.com/binaricat/Netcatty/blob/c3067c8dc2aebf7817f1b7c918a6f26dda53414d/components/terminal/useTerminalHibernateEffect.ts#L96-L111)、[最终门禁](https://github.com/binaricat/Netcatty/blob/c3067c8dc2aebf7817f1b7c918a6f26dda53414d/components/Terminal.tsx#L1775-L1858)

大部分远程连接在结束回调中还会先清空 backend session ID，再把状态改成 `disconnected`。所以正确修复不能只放宽状态门禁，还必须允许已结束路径在没有存活 backend ID 时生成快照并释放 xterm；只有仍连接的路径才要求 ID 并做流控、listener 切换。[连接结束处理](https://github.com/binaricat/Netcatty/blob/c3067c8dc2aebf7817f1b7c918a6f26dda53414d/components/terminal/runtime/terminalSessionAttachment.ts#L946-L952)

本地新增的 hook 回归用例以“隐藏、已断开、休眠开启、运行资源仍存在”为输入。修正前连续 3 次都得到 `onHibernate = 0`；允许结束状态进入休眠后稳定通过。对应修复保持以下边界：

- 仍连接的会话保持原有流控和后台 listener 切换；
- 已结束的会话直接完整休眠，不占用“保留 2 个软隐藏 renderer”的名额；
- 已结束的会话不释放已经不存在的流控，也不重新订阅已经结束的 backend listener；
- 可见的结束会话仍保留画面；重新显示隐藏会话时，按原有离线唤醒路径恢复快照，状态仍为已断开。

这能修复另一位用户明确提到的“会话已经结束仍长期静置”场景，但不声称解决原报告者仍连接的多终端场景。

## 尚未证实的方向

### 100,000 行回滚

大回滚会提高每个终端允许保留的历史上限，是明显的内存放大器。但“上限设为 100,000”不等于每个终端已经填满 100,000 行。当前没有 10,000 与 100,000 的同机、同终端数、同运行时长对照，所以不能通过降低默认值或截断用户历史来冒充修复。

### 长时间泄漏

现有证据只有一张高内存截图和一次无截图的独立回忆，没有 renderer 私有内存、V8 堆、Blink 内存和 GPU 进程的时间曲线。现在可以说“资源长期保留值得优先查”，不能说“已经确认内存泄漏”。

### 后台输出竞争

先前真实 Electron 对照已证明：14 个后台终端持续写入时，活跃终端回显 p95 会从约 30 ms 上升到 100 ms 以上；只保留 2 个后台 renderer 后恢复到约 30 ms。xterm.js 也确认多个实例会竞争同一页面主线程。[xterm.js #3368](https://github.com/xtermjs/xterm.js/issues/3368)

这说明持续后台输出是一个真实性能风险，但原报告者明确说终端基本静止，所以不能把那个压力场景当作本次现场根因。

## 建议的交付顺序

### 本次窄 PR

- 修复 #2581 留下的 Linux Electron 流水线启动问题；
- 允许隐藏的已结束远程会话释放完整终端运行资源；
- 加入状态策略、hook 调度和 backend listener 边界回归测试；
- 不改变回滚设置，不丢弃输出，不改已连接会话的休眠语义，不把 #2578 标记为已解决。

### 后续诊断 PR

建立可自动运行的 5×3 终端矩阵，至少包含：

- 回滚上限 10,000 与 100,000；
- 静止、低速输出和持续输出；
- 连接与结束状态；
- 隐藏休眠开与关；
- renderer 常驻/私有内存、Blink/V8 内存、帧间隔和输入到画面更新延迟。

短时、确定性的 Electron 场景可以进普通 CI；数小时 soak 应放在手动或定时工作流，不能拖慢每个 PR。只有该矩阵把增长归因到回滚、终端历史、Agent、DOM/GPU 或某个具体生命周期后，才做更广的行为修复。

## 本次验证记录

- 通过 GitHub API 读取 #2578 正文和全部评论，并读取附图原始像素内容。
- 核对 #2581 合并提交、当前 `main`、xterm.js #5902 和 beta.221 npm 元数据。
- 隐藏结束会话的回归测试在修正前连续 3 次稳定失败，修正后通过。
- 休眠状态、无 backend ID 的结束路径、hook 调度、最终释放与流水线结构的 40 项专项测试全部通过。
- 在 `ELECTRON_DISABLE_SANDBOX=1` 下，真实 Electron 密集装饰测试通过；本次测得实际关键词高亮装饰 712 个、20,000 个装饰的最慢刷新约 50 ms。
- 生产构建通过。
- 全量测试最终共 8,226 项，其中 8,216 通过、10 跳过、0 失败。此前一次运行里有一个与本次终端改动无关的 AI 网络超时用例偶发失败；该用例随后单独连续运行 5 次通过，完整测试再跑两次也全部通过。
