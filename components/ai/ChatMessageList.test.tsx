import test from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

import { I18nProvider } from "../../application/i18n/I18nProvider.tsx";
import type { ChatMessage } from "../../infrastructure/ai/types.ts";
import ChatMessageList from "./ChatMessageList.tsx";
import { TooltipProvider } from "../ui/tooltip.tsx";

const makeMessage = (index: number): ChatMessage => ({
  id: `msg-${index}`,
  role: index % 2 === 0 ? "user" : "assistant",
  content: `message-${index}`,
  timestamp: index,
});

test("ChatMessageList only renders the recent message batch by default", () => {
  const messages = Array.from({ length: 60 }, (_value, index) => makeMessage(index));

  const markup = renderToStaticMarkup(
    React.createElement(
      I18nProvider,
      { locale: "en" },
      React.createElement(
        TooltipProvider,
        null,
        React.createElement(ChatMessageList, { messages }),
      ),
    ),
  );

  assert.match(markup, /Load earlier messages \(10 more\)/);
  assert.doesNotMatch(markup, /message-0/);
  assert.match(markup, /message-10/);
  assert.match(markup, /message-59/);
});

test("ChatMessageList renders external MCP vault tool results as artifact cards", () => {
  const messages: ChatMessage[] = [
    {
      id: "tool-1",
      role: "tool",
      content: "",
      timestamp: 1,
      toolResults: [
        {
          toolCallId: "external-call-1",
          toolName: "mcp__netcatty__vault_notes_create",
          content: JSON.stringify({
            ok: true,
            note: { id: "note-1", title: "Deploy Runbook", group: "ops" },
          }),
        },
      ],
    },
  ];

  const markup = renderToStaticMarkup(
    React.createElement(
      I18nProvider,
      { locale: "en" },
      React.createElement(
        TooltipProvider,
        null,
        React.createElement(ChatMessageList, { messages }),
      ),
    ),
  );

  assert.match(markup, /Deploy Runbook/);
  assert.match(markup, /ops/);
  assert.doesNotMatch(markup, /external-call-1/);
});

test("ChatMessageList renders Netcatty CLI vault results as artifact cards", () => {
  const messages: ChatMessage[] = [
    {
      id: "assistant-1",
      role: "assistant",
      content: "",
      timestamp: 1,
      toolCalls: [
        {
          id: "cli-call-1",
          name: "shell",
          arguments: {
            command: `/bin/zsh -lc '"/Applications/Netcatty.app/netcatty-tool-cli" vault host get --host-id host_1 --json'`,
          },
        },
      ],
      executionStatus: "completed",
    },
    {
      id: "tool-1",
      role: "tool",
      content: "",
      timestamp: 2,
      toolResults: [
        {
          toolCallId: "cli-call-1",
          content: JSON.stringify({
            ok: true,
            host: { id: "host_1", label: "Prod", hostname: "prod.example.com", port: 22 },
          }),
        },
      ],
    },
  ];

  const markup = renderToStaticMarkup(
    React.createElement(
      I18nProvider,
      { locale: "en" },
      React.createElement(
        TooltipProvider,
        null,
        React.createElement(ChatMessageList, { messages }),
      ),
    ),
  );

  assert.match(markup, /Prod/);
  assert.match(markup, /prod\.example\.com:22/);
  assert.doesNotMatch(markup, /cli-call-1/);
});
