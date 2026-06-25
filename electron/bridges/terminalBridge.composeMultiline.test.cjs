/**
 * 复现：撰写栏（Compose Bar）发送多行内容时只发出第一行。
 *
 * 链路：撰写栏 textarea → executeSnippetCommand(text, false) → 多行+autoRun
 * 触发 lineDelayMs=250 → 后端 writeToSession 走"逐行延迟发送"：第一行立即发，
 * 行2+ 用 setTimeout 排进 session.pendingAutomatedWriteTimers。
 *
 * 根因：writeToSession 开头
 *   if (!payload.automated) clearPendingAutomatedWrites(session);
 * 会被【任何】非自动写入触发——包括 xterm 对第一行命令输出的自动回写
 * （光标位置查询 DSR 响应、焦点上报、bracketed-paste 响应等），它们同样走
 * netcatty:write 且不带 automated 标志 → 清空行2+ 的待发队列 → 只发第一行。
 * 与中文无关：shouldDelayAutoRunSnippetInput 只检测 \n，多行英文同样中招。
 */
const test = require("node:test");
const assert = require("node:assert/strict");

const terminalBridge = require("./terminalBridge.cjs");

function initBridge(sessions) {
  terminalBridge.init({
    sessions,
    electronModule: {
      webContents: { fromId: () => ({ send() {} }) },
    },
  });
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// 断言【期望的正确行为】：无害的终端自动回写不应取消后续行。
// 因此在修复前，这个测试会失败（实际只发出 "echo one\r" + 那次自动回写）。
test("[REPRO] terminal auto-reply between automated lines must NOT cancel pending lines", async () => {
  const calls = [];
  const sessions = new Map();
  sessions.set("ssh-1", {
    stream: { signal() {}, write(data) { calls.push(data); } },
  });
  initBridge(sessions);

  // 撰写栏多行（autoRun → 逐行延迟发送）
  terminalBridge.writeToSession(
    { sender: {} },
    {
      sessionId: "ssh-1",
      data: "echo one\necho two\necho three\r",
      automated: true,
      lineDelayMs: 20,
    },
  );
  assert.deepEqual(calls, ["echo one\r"], "第一行应立即发出");

  // 模拟 xterm 对第一行命令输出的自动回写（光标位置查询 DSR 响应），非 automated
  terminalBridge.writeToSession({ sender: {} }, { sessionId: "ssh-1", data: "\x1b[2;1R" });

  await delay(80);

  assert.deepEqual(
    calls,
    ["echo one\r", "\x1b[2;1R", "echo two\r", "echo three\r"],
    "终端自动回写不应清空逐行队列；行2/行3 应照常发出",
  );
});

// 对照（护栏）：真正的用户中断（Ctrl+C）应当取消剩余自动逐行发送。
// 这是既有的有意行为，修复 REPRO 时不能破坏。当前应通过。
test("[GUARD] Ctrl+C between automated lines SHOULD cancel pending lines", async () => {
  const calls = [];
  const sessions = new Map();
  sessions.set("ssh-1", {
    stream: { signal() {}, write(data) { calls.push(data); } },
  });
  initBridge(sessions);

  terminalBridge.writeToSession(
    { sender: {} },
    { sessionId: "ssh-1", data: "echo one\necho two\r", automated: true, lineDelayMs: 20 },
  );
  assert.deepEqual(calls, ["echo one\r"]);

  terminalBridge.writeToSession({ sender: {} }, { sessionId: "ssh-1", data: "\x03" }); // Ctrl+C
  await delay(60);

  assert.deepEqual(calls, ["echo one\r", "\x03"], "Ctrl+C 应取消行2（既有设计，修复后须保留）");
});
