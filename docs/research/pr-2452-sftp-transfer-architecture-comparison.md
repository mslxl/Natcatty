# PR #2452：SFTP 传输架构对比研究

研究日期：2026-07-24

源码版本：

- Netcatty 当前工作分支：`e1793e382bf022f792a74cfca4d7de95c92bd5bf`
- Netcatty PR #2452 合并时的提交：`ad2730113c7a2c20a37bef4369d7a2b40bd2f060`
- Tabby：`14e2d60b9b6dee84a53c37f05eefeb803787de04`
- Electerm：`e68e61e3d0a8b2f66840282a4fc3dc7c40798699`
- OpenSSH portable：`7e446d3f5917c2f2770981a89d0e54d5d064bf0c`
- WinSCP：`b9307ef5f866a14dded9a330d8a2b8848d16dc7f`
- ssh2：`318d447ce3aca26e1ac73b63767b82a29b02467b`
- ssh2-sftp-client：`c690045a5d05e40f86db6b7321c6e627071b6c4a`

## 结论

[PR #2452](https://github.com/binaricat/Netcatty/pull/2452) 对症解决了已经测出的上传速度瓶颈。原来的 8 × 32 KiB 窗口最多只有约 256 KiB 数据在途；改成 32 × 32 KiB 后约为 1 MiB，同时保留了 Netcatty 已经验证过的安全分块大小。这个方向与 ssh2、OpenSSH 默认的 64 × 32 KiB，以及 WinSCP 默认 64 个上传请求一致，但取值更保守。

这次改动最终远大于一次参数调整，是因为旧代码没有一套统一、可靠的上传规则。提高并发后，取消、降级、源文件变化、临时文件清理、目标替换、符号链接和权限恢复中的竞态都被暴露出来。后续加固有必要，但目前仍有两套独立实现：

1. 面板和本地上传走 `sftpBridge`，有自己的一套暂存、替换、符号链接和失败恢复处理；
2. 传输中心的可续传上传走 `transferBridge`，另有一套断点、摘要、暂存、替换和恢复处理。

这份重复实现已经成为 Netcatty 当前最大的设计风险。下一步不应继续在两处分别打补丁，而应抽出统一的传输事务：目标检查、暂存与替换、取消收尾和完整性校验都只保留一套规则。

## 本后续 PR 的边界

本后续 PR 只承接原 PR 合并时漏掉的 6 个提交，并加入本研究文档：上传替换前竞态检查、源文件逐段校验、取消和失败清理、无 `lstat` 场景、临时空间预检及相应测试。它不顺手重构整个传输模块。

下面这些问题已经确认，但应分成后续工作，避免把高风险架构调整混入补漏 PR：异常退出后的续传身份校验、两套上传事务合并、自适应窗口、服务器能力矩阵，以及更完整的元数据约定。

## 对比表

| 实现 | 单文件传输路径 | 默认请求窗口 / 分块 | 续传与取消 | 最终路径安全 | 完整性与元数据 |
|---|---|---:|---|---|---|
| Netcatty 当前分支 | 固定偏移并发读写；优先独立通道，失败时尝试共享通道 | 上传 32 × 32 KiB；下载 64 × 32 KiB（[配置](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferLimits.cjs#L3-L23)） | 只把连续完成区间记作断点；暂停等待在途请求；取消时关闭临时通道 | 普通文件先暂存再替换；符号链接原位写入；面板上传有备份恢复 | 本地可续传上传逐块校验 SHA-256；检查远端大小；替换前恢复原权限 |
| Tabby | 逐块串行读写；应用层看不到单文件内部并发（[上传循环](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-ssh/src/session/sftp.ts#L113-L153)） | 应用层 1 × 256 KiB | 取消会关闭文件；没有暂停和按偏移续传（[传输接口](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-core/src/api/platform.ts#L23-L55)） | 上传使用 `.tabby-upload`，但先删旧文件再重命名；下载直接写最终路径 | 只在开始时检查一次源文件；结束时无摘要和源变化复查；上传协调层不恢复权限 |
| Electerm | 自定义固定偏移并发传输，上传和下载共用同一套逻辑 | 64 × 32 KiB（[默认值](https://github.com/electerm/electerm/blob/e68e61e3d0a8b2f66840282a4fc3dc7c40798699/src/app/server/transfer.js#L12-L40)） | 暂停只停止派发新任务；取消后等待短暂时间再关句柄；没有持久化断点 | 主路径直接覆盖最终文件，没有暂存替换 | 只检查一次源文件，结束只对比字节数；权限修改失败不会向上传递 |
| OpenSSH sftp | 流水线式读写请求队列 | 默认 64 × 32 KiB；服务器限制可能缩小分块（[默认值](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp-client.c#L59-L63)，[协商](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp-client.c#L552-L578)） | 支持 `reget`/`reput`；中断后停止新请求并收完在途请求 | 通常原位操作，不承诺事务式替换 | 续传假设已有前缀相同；手册明确警告不同时会损坏文件（[手册](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp.1#L657-L672)）；可选保留权限、时间并同步落盘 |
| WinSCP | 异步上传、下载队列，分块大小可调整 | 上传队列 64，下载 32（[默认值](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SessionData.cpp#L296-L305)）；最小 32 KiB，受协商结果和服务器包大小限制（[计算逻辑](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SftpFileSystem.cpp#L2243-L2321)） | 默认 100 KiB 以上启用智能续传；使用 `.filepart`；按偏移恢复 | 已知或疑似符号链接、非当前用户所有的文件不会走可续传替换；完成后才替换 | 保留已有或指定权限与时间；权限失败有明确处理规则 |
| ssh2 | `fastGet`、`fastPut` 共用 `fastXfer` | 64 × 32 KiB，可配置（[实现](https://github.com/mscdex/ssh2/blob/318d447ce3aca26e1ac73b63767b82a29b02467b/lib/protocol/SFTP.js#L2185-L2226)） | 回调或错误时关闭源和目标句柄；不自带可续传事务 | 以覆盖模式打开目标；暂存和重命名由调用者负责 | 不记录源文件快照或摘要；校验由调用者负责 |

## 1. 并发窗口、分块大小和传输路径

### Netcatty

Netcatty 有意把分块固定为 32 KiB，并分别设置上传和下载并发：上传 32 个请求，约 1 MiB 数据在途；下载 64 个请求，约 2 MiB 数据在途。这是产品兼容性选择，不是协议固定值（[源码](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferLimits.cjs#L3-L23)）。可续传上传优先在独立 SFTP 通道上做固定偏移并发写入，再尝试兼容的流水线方案；不会悄悄退回到串行流式上传（[策略](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L757-L1007)）。

这个设计抓住了成熟客户端最重要的做法：同时保留多个未完成请求。但它还没有采用成熟客户端的自适应能力。OpenSSH 会读取服务器的 `limits@openssh.com` 结果再决定读写长度，WinSCP 也会按传输层和服务器限制缩小分块。Netcatty 因为历史上放大分块曾造成真实损坏，所以对所有主机都使用 32 KiB。这个保守选择合理；以后若要自适应，应基于明确允许、实测和协商，而不是全局放大分块。

### Tabby 和 Electerm

Tabby 自己的 SFTP 协调层并不是高吞吐参考。它每次读写 256 KiB 并等待完成；多选上传使用不设上限的 `Promise.all`，递归目录上传却是串行的（[单文件循环](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-ssh/src/session/sftp.ts#L113-L153)，[多文件调度](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-ssh/src/components/sftpPanel.component.ts#L210-L233)）。底层 russh 依赖可能继续缓冲协议包，但 Tabby 没有配置或暴露这个窗口，因此不能据此推断它有某个固定的协议层并发数。

Electerm 是 PR #2452 最直接的对比对象。它的 `fastXfer` 默认同时执行 64 个 32 KiB 操作，上传和下载都按固定偏移调度（[初始化](https://github.com/electerm/electerm/blob/e68e61e3d0a8b2f66840282a4fc3dc7c40798699/src/app/server/transfer.js#L12-L40)，[调度器](https://github.com/electerm/electerm/blob/e68e61e3d0a8b2f66840282a4fc3dc7c40798699/src/app/server/transfer.js#L289-L370)）。因此 Netcatty 的 32 请求上传窗口较保守，但已经属于同一吞吐级别。

### OpenSSH、WinSCP 和 Node 生态

OpenSSH 和 ssh2 都默认使用 64 × 32 KiB。OpenSSH 会逐步扩大实际下载窗口到配置上限，并跟踪乱序响应（[下载循环](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp-client.c#L1677-L1802)）；上传则根据确认响应把未完成请求控制在上限以内（[上传循环](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp-client.c#L2111-L2198)）。ssh2 的 `fastXfer` 分配 `分块 × 并发数` 的缓冲区，并直接以覆盖方式打开目标，因此它只是快速传输原语，不是安全替换事务（[源码](https://github.com/mscdex/ssh2/blob/318d447ce3aca26e1ac73b63767b82a29b02467b/lib/protocol/SFTP.js#L2185-L2285)）。

`ssh2-sftp-client` 只是把 ssh2 的快速路径包装出来。其官方文档明确提醒：快速并发传输是否可用取决于服务器；追求广泛兼容时应优先普通 `get`/`put`（[说明](https://github.com/theophilusx/ssh2-sftp-client/blob/c690045a5d05e40f86db6b7321c6e627071b6c4a/README.md#L1160-L1163)）。这支持 Netcatty 记录失败原因、建立服务器兼容矩阵，但不支持悄悄退回串行并让性能功能变成完全不同的体验。

## 2. 续传、取消、临时文件和原子替换

OpenSSH 的续传很简单：从目标当前大小继续，并假定已有前缀与源文件一致。收到中断后，它不再派发新请求，收完未完成响应，并尽量把文件截到最高连续确认位置（[上传恢复](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp-client.c#L2116-L2239)，[下载恢复](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp-client.c#L1812-L1845)）。作为原位传输的命令行工具，它的中断处理可靠，但不承诺原子替换。

WinSCP 是更强的产品级参考。符合条件的文件先上传到 `最终文件.filepart`，按临时文件大小续传，完成后才替换最终文件。如果目标是符号链接，或者删除重建会改变文件所有者，它会禁用这种可续传替换（[上传判断](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SftpFileSystem.cpp#L4630-L4771)）。下载同样使用本地临时文件和续传偏移（[下载暂存](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SftpFileSystem.cpp#L5420-L5489)）。

Tabby 上传会写入 `.tabby-upload`，但重命名前先删除旧目标；如果重命名失败，没有备份可恢复。下载则直接打开本地最终路径（[上传](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-ssh/src/session/sftp.ts#L113-L153)，[本地下载句柄](https://github.com/Eugeny/tabby/blob/14e2d60b9b6dee84a53c37f05eefeb803787de04/tabby-electron/src/services/platform.service.ts#L425-L467)）。Electerm 也直接打开最终目标；取消只停止后续调度，并在短暂等待后关闭句柄（[传输生命周期](https://github.com/electerm/electerm/blob/e68e61e3d0a8b2f66840282a4fc3dc7c40798699/src/app/server/transfer.js#L340-L431)）。它们可以作为速度和界面参考，但不适合作为可靠性基准。

Netcatty 当前面板上传的替换流程已经强于 Tabby 和 Electerm：普通文件先暂存，符号链接原位写入，替换前再次检查取消；如果替换和恢复都失败，会保留可恢复文件并返回用户可理解的路径（[替换流程](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/sftpBridge.cjs#L625-L875)）。可续传引擎也只把最高连续完成区间当作断点，不会把累计进度误当成可恢复位置（[并发区间调度](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L1225-L1445)）。

## 3. 源文件变化和完整性

Tabby、Electerm 都只在传输开始时检查一次源文件，ssh2 的 `fastXfer` 也是如此。OpenSSH 也明确警告续传不会验证已有前缀。这说明静默忽略源变化很常见，但不代表这种做法安全。

Netcatty 的本地可续传上传明显更强：它在 Netcatty 自己的临时目录生成紧凑的 SHA-256 分块摘要，再读一遍确认基线，并在每个区间发送前与摘要对比（[摘要基线](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L1121-L1220)，[写入前校验](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L1455-L1540)）。即使文件系统时间精度很粗，也能避免一次上传把两个版本的本地文件拼到一起。

但保护并不统一。`uploadLocalToSftp` 和带进度的内存上传走面板辅助流程并检查远端大小，其中的本地路径上传却没有使用传输中心的摘要规则；远端下载依赖大小、元数据和部分区间检查，也没有统一的端到端摘要。这仍是两套引擎分裂造成的结果。

现有代码还存在一个**程序异常退出后续传的缺口**。恢复时只对比暂存文件和当前源文件的前 256 KiB（[续传抽样上限](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L187-L205)）；完整的源文件指纹则是在用户明确暂停时才首次记录（[暂停时记录指纹](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L2801-L2815)）。如果程序在来得及暂停和保存指纹之前退出，而源文件只在前 256 KiB 之后发生变化，再次续传可能保留旧的远端前缀，再接上新源文件的后半段。当前工作分支新增的逐块摘要能阻止**同一次运行中的源文件变化**，却不能证明上一次进程留下的暂存文件属于当前源文件。正确方向是在传输开始时就持久化源身份和已确认前缀摘要；缺少这份证据时应保守地重新上传。

## 4. 符号链接、权限和失败恢复

成熟客户端会把“替换一个目标”与原始 SFTP 读写分开处理：

- WinSCP 遇到已知或疑似符号链接，以及非当前用户所有的目标时，会避免使用临时文件替换，因为重命名替换会改变节点或所有者（[源码](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SftpFileSystem.cpp#L4661-L4700)）。
- WinSCP 替换后恢复指定或原有的权限和时间；权限恢复失败有明确策略，不会无条件忽略（[属性处理](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SftpFileSystem.cpp#L4800-L4839)，[错误处理](https://github.com/winscp/winscp/blob/b9307ef5f866a14dded9a330d8a2b8848d16dc7f/source/core/SftpFileSystem.cpp#L4972-L5033)）。
- OpenSSH 默认只传普通文件，递归传输时不跟随符号链接，并可选择保留权限和时间（[手册](https://github.com/openssh/openssh-portable/blob/7e446d3f5917c2f2770981a89d0e54d5d064bf0c/sftp.1#L637-L690)）。

Netcatty 面板上传目前优先使用 `lstat`，并对缺少该能力的服务器保守地回退到 `stat`/`readlink`；符号链接原位写入，普通文件先暂存，并在替换前把旧权限应用到暂存文件。边界也应明确：SFTP v3 无法在所有服务器上可靠保留所有者、访问控制列表、扩展属性和硬链接身份，因此“路径和权限相同”不等于“文件节点的全部属性都相同”。

剩余不一致在 `transferBridge.promoteRemoteTransfer`。它另有一套移走旧文件、放入新文件、删除备份的流程，忽略了部分恢复和清理失败，也没有复用面板上传对符号链接和可恢复路径的处理（[源码](https://github.com/binaricat/Netcatty/blob/e1793e382bf022f792a74cfca4d7de95c92bd5bf/electron/bridges/transferBridge.cjs#L243-L275)）。即使当前界面路径使某些情况不常见，这仍是需要处理的结构问题。

## 5. Netcatty 当前缺口和模块设计问题

### 高优先级

1. **合并两套上传事务。** 目标检查、暂存命名、替换、备份恢复、权限处理和恢复信息应放在同一个模块，供两条路径共同使用。
2. **明确每次上传的持久性模式。** 至少区分“事务式替换”“原位覆盖”“可续传临时文件”。当前代码在多处分支和降级路径中临时推断，难以完整证明取消后的状态。
3. **统一完整性规则。** 本地可续传上传有分块摘要，面板本地路径上传只检查大小，远端下载又是第三套规则。应由一个接口明确说明检查了什么、没有检查什么。
4. **保留失败恢复证据。** 恢复失败时必须返回暂存、备份和最终文件的用户可理解路径，也不能再被通用清理删除唯一完好的副本。面板上传已经这样做，传输中心还没有统一。
5. **补上异常退出续传的身份检查。** 只比较前 256 KiB 无法确认大文件仍与暂存文件对应；只在正常暂停时计算的指纹也无法覆盖此前的崩溃。这与运行中逐块校验是两个不同问题。
6. **按职责拆开过大的状态模块。** 当前版本的 `transferBridge.cjs` 有 3,085 行，`sftpBridge.cjs` 有 1,893 行，另有 `sftpBridge/fileOps.cjs` 1,109 行。它们混合了任务准入、界面通信、会话、上传下载调度、暂停取消、速度统计、完整性、暂存、替换和恢复。职责过度集中，是两条路径逐渐形成不同安全规则的直接原因。应先抽出共享替换事务和完整性规则，再拆分上传、下载调度，最后只保留薄的界面适配层。

### 中优先级

7. **协商能力，但保留安全下限。** 继续把 32 KiB 作为兼容基线，同时记录服务器 `limits@openssh.com`、拒绝大包和各主机结果。以后按主机选择并发窗口，比修改全局分块更安全。
8. **区分文件并发和单文件请求并发。** WinSCP 的请求队列在单个文件内部，Netcatty 另有全局文件准入队列。这两类限制应使用不同名称、统计和提示。
9. **明确元数据损失边界。** 恢复权限有价值，但所有者、访问控制列表、扩展属性、稀疏布局、硬链接和文件节点身份都不在当前保证内。测试和界面不应暗示替换后完全等同。
10. **建立服务器兼容矩阵。** ssh2-sftp-client 的官方提醒是可靠的：不同服务器的并发传输行为差异很大（[文档](https://github.com/theophilusx/ssh2-sftp-client/blob/c690045a5d05e40f86db6b7321c6e627071b6c4a/README.md#L1563-L1574)）。Netcatty 应重复验证 OpenSSH、Dropbear、Windows SFTP、NAS、提权 SFTP、不支持 `lstat`、不支持 `readlink` 和低 `MaxSessions` 等环境。

## 6. PR #2452 是否对症

**对报告中的速度问题是对症的；对整体架构只解决了一部分。**

- 原来的 8 请求上传窗口对已测延迟确实太小。保留 32 KiB 分块并提高到 32 个请求，有 Netcatty 自身测试以及 Electerm、OpenSSH、ssh2、WinSCP 的实现形态支持。
- 不悄悄退回串行是正确的。这个功能的目的就是高吞吐；明确报告兼容失败，比静默地把几分钟变成几小时更诚实。
- 审查中补上的问题不是无关润色。请求并发后，必须等在途操作结束或彻底隔离才算取消完成；累计进度不是可续传断点，只有连续完成位置才是；清理也不能与未完成写入同时发生。
- 该 PR 不能证明 32 对所有服务器都最优。OpenSSH 和 WinSCP 会协商或调整，ssh2-sftp-client 也明确记录了不兼容服务器。Netcatty 应保留内部可调整能力，先收集实际诊断数据，再决定是否改变。
- PR 合并时的提交是 `ad2730113...`，本研究还检查了之后工作分支 `e1793e382...` 上的进一步加固。讨论“合并时实际交付了什么”时，不能混淆这两个状态。
- 合并后的版本随后仍出现 3 条有效远程意见，分别涉及 SCP 断裂链接和无 `lstat` 时的断裂链接判断；对应修复在后续分支中，不在原合并结果中。

最简洁的判断是：**#2452 修复了速度瓶颈，也显著提高了安全性；同时它证明 Netcatty 需要一套共享传输事务，而不是由两个入口分别实现文件替换规则。**

## 证据质量与限制

- 上述行为判断只使用官方仓库、源码、第一方手册和 PR 本身；没有用二手文章支撑性能或架构结论。
- PR #2452 中两组实机速度数据来自该 PR 的维护者记录；本次研究没有重新连接那两台主机复测。本次独立确认的是实现路径、窗口大小、自动化测试和对照项目源码。
- OpenSSH、WinSCP、ssh2、Tabby、Electerm 的链接都固定到具体提交；Netcatty 链接明确区分合并时提交和后续工作分支。
- “没有发现”只表示所引用的应用协调路径中没有该机制，不证明更底层的 SSH 库或服务器不会额外缓冲或处理。
- Tabby 的 russh 内部行为和 Electerm 的 SCP 目录路径没有被用来推断单文件 SFTP 并发，因为应用没有配置对应窗口。
