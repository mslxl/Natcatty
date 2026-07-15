import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

import ChatInput from './ChatInput';
import { TooltipProvider } from '../ui/tooltip';

test('does not render a standalone slash command toolbar button', () => {
  const html = renderToStaticMarkup(
    <TooltipProvider>
      <ChatInput
        value=""
        onChange={() => {}}
        onSend={() => {}}
        isStreaming={false}
        disabled={false}
        agentName="Catty Agent"
        quickMessages={[{
          id: 'qm-1',
          slug: 'hello',
          name: 'Hello',
          description: 'Greeting',
          content: 'Say hello',
        }]}
      />
    </TooltipProvider>,
  );

  assert.match(html, /textarea/);
  assert.doesNotMatch(html, /aria-label="ai\.chat\.slashCommands"/);
});

test('renders separate steer and stop actions for a running Codex App Server turn', () => {
  const html = renderToStaticMarkup(
    <TooltipProvider>
      <ChatInput
        value="change direction"
        onChange={() => {}}
        onSend={() => {}}
        onSteer={() => {}}
        onStop={() => {}}
        isStreaming
        canSteer
        lockTurnConfiguration
        disabled={false}
        agentName="Codex"
        modelPresets={[{ id: 'gpt-test', name: 'GPT Test' }]}
        selectedModelId="gpt-test"
        onModelSelect={() => {}}
      />
    </TooltipProvider>,
  );

  assert.match(html, /aria-label="ai\.codex\.steer\.addInstruction"/);
  assert.match(html, /placeholder="ai\.codex\.steer\.placeholder"/);
  assert.match(html, /aria-label="Stop"/);
  assert.match(html, /disabled=""[^>]*aria-label="Select model"/);
});

test('allows terminal-selection-only steering submissions', () => {
  const html = renderToStaticMarkup(
    <TooltipProvider>
      <ChatInput
        value=""
        onChange={() => {}}
        onSend={() => {}}
        onSteer={() => {}}
        onStop={() => {}}
        isStreaming
        canSteer
        disabled={false}
        agentName="Codex"
        files={[{
          id: 'terminal-selection',
          filename: 'terminal-selection.txt',
          dataUrl: 'data:text/plain;base64,dGVzdA==',
          base64Data: 'dGVzdA==',
          mediaType: 'text/plain',
          terminalSelection: true,
          lineCount: 1,
        }]}
      />
    </TooltipProvider>,
  );

  assert.match(html, /<form[^>]*data-allow-empty-submit="true"/);
  assert.match(html, /aria-label="ai\.codex\.steer\.addInstruction"/);
  assert.doesNotMatch(
    html,
    /<button[^>]*disabled=""[^>]*aria-label="ai\.codex\.steer\.addInstruction"/,
  );
});
