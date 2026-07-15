// 创建时间: 2026-07-15
// 功能说明: 校验键盘交互认证弹窗的服务器提示文本格式

import test from "node:test";
import assert from "node:assert/strict";

import { formatKeyboardInteractiveServerPrompt } from "./KeyboardInteractiveModal.tsx";

test("formatKeyboardInteractiveServerPrompt preserves server instructions and prompt labels", () => {
  const text = formatKeyboardInteractiveServerPrompt({
    name: "Keyboard-interactive authentication prompts from server",
    instructions: "为保障主机安全，请输入二次认证密码，如有疑问，请联系xxx，电话xxx。",
    prompts: [
      {
        prompt: "Secondary Authentication Password:",
        echo: false,
      },
    ],
  });

  assert.equal(
    text,
    [
      "Keyboard-interactive authentication prompts from server:",
      "| 为保障主机安全，请输入二次认证密码，如有疑问，请联系xxx，电话xxx。",
      "| Secondary Authentication Password:",
    ].join("\n"),
  );
});
