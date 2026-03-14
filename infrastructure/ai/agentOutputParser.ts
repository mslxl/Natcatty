/**
 * Agent Output Parser
 *
 * Parses JSON Lines output from `codex exec --json` and similar structured
 * agent output into display-friendly text segments.
 */

export interface AgentOutputSegment {
  type: 'thinking' | 'text' | 'command' | 'command_output' | 'file_change' | 'plan' | 'error' | 'usage';
  content: string;
}

/**
 * Try to parse a single line of agent output.
 * Returns structured segment(s) if it's a recognized JSON event,
 * or null if it's not JSON / not recognized (caller should treat as plain text).
 */
export function parseAgentJsonLine(line: string): AgentOutputSegment[] | null {
  const trimmed = line.trim();
  if (!trimmed || !trimmed.startsWith('{')) return null;

  let event: Record<string, unknown>;
  try {
    event = JSON.parse(trimmed);
  } catch {
    return null;
  }

  if (!event.type) return null;

  const type = event.type as string;
  const item = event.item as Record<string, unknown> | undefined;

  // thread.started / turn.started — skip silently
  if (type === 'thread.started' || type === 'turn.started') {
    return [];
  }

  // turn.completed — show token usage
  if (type === 'turn.completed') {
    const usage = event.usage as { input_tokens?: number; output_tokens?: number } | undefined;
    if (usage) {
      return [{
        type: 'usage',
        content: `tokens: ${usage.input_tokens ?? '?'} in / ${usage.output_tokens ?? '?'} out`,
      }];
    }
    return [];
  }

  // error
  if (type === 'error' || type === 'turn.failed') {
    const msg = (event.message as string)
      || ((event.error as Record<string, unknown>)?.message as string)
      || JSON.stringify(event);
    return [{ type: 'error', content: msg }];
  }

  // item events
  if (type.startsWith('item.') && item) {
    return parseItemEvent(type, item);
  }

  return null;
}

function parseItemEvent(
  eventType: string,
  item: Record<string, unknown>,
): AgentOutputSegment[] {
  const itemType = item.type as string;

  // reasoning (thinking)
  if (itemType === 'reasoning') {
    if (eventType !== 'item.completed') return [];
    const text = item.text as string || '';
    if (!text.trim()) return [];
    return [{ type: 'thinking', content: text }];
  }

  // agent_message (final response text)
  if (itemType === 'agent_message') {
    if (eventType !== 'item.completed') return [];
    const text = item.text as string || '';
    if (!text.trim()) return [];
    return [{ type: 'text', content: text }];
  }

  // command_execution
  if (itemType === 'command_execution') {
    const segments: AgentOutputSegment[] = [];
    const command = item.command as string || '';
    const output = item.aggregated_output as string || '';
    const exitCode = item.exit_code as number | null;
    const status = item.status as string;

    if (eventType === 'item.started' && command) {
      segments.push({ type: 'command', content: command });
    }

    if (eventType === 'item.completed') {
      if (command) {
        segments.push({ type: 'command', content: command });
      }
      if (output.trim()) {
        segments.push({ type: 'command_output', content: output.trim() });
      }
      if (exitCode !== null && exitCode !== 0) {
        segments.push({ type: 'error', content: `exit code: ${exitCode}` });
      }
    }

    return segments;
  }

  // file_change
  if (itemType === 'file_change') {
    if (eventType !== 'item.completed') return [];
    const changes = item.changes as Array<{ path: string; kind: string }> | undefined;
    if (!changes?.length) return [];
    const lines = changes.map(c => `${c.kind}: ${c.path}`).join('\n');
    return [{ type: 'file_change', content: lines }];
  }

  // todo_list / plan
  if (itemType === 'todo_list') {
    const items = item.items as Array<{ text: string; completed: boolean }> | undefined;
    if (!items?.length) return [];
    const lines = items.map(t => `${t.completed ? '✓' : '○'} ${t.text}`).join('\n');
    return [{ type: 'plan', content: lines }];
  }

  // mcp_tool_call
  if (itemType === 'mcp_tool_call') {
    const tool = item.tool as string || 'unknown';
    const server = item.server as string || '';
    if (eventType === 'item.started') {
      return [{ type: 'command', content: `[MCP] ${server}/${tool}` }];
    }
    if (eventType === 'item.completed') {
      const result = item.result as Record<string, unknown> | null;
      const error = item.error as string | null;
      if (error) {
        return [{ type: 'error', content: `MCP ${tool}: ${error}` }];
      }
      if (result) {
        const content = (result.content as Array<{ text?: string }>) || [];
        const text = content.map(c => c.text || '').filter(Boolean).join('\n');
        if (text) return [{ type: 'command_output', content: text }];
      }
    }
    return [];
  }

  return [];
}

/**
 * Format AgentOutputSegments into markdown text for display.
 */
export function formatSegmentsAsMarkdown(segments: AgentOutputSegment[]): string {
  return segments.map(seg => {
    switch (seg.type) {
      case 'thinking':
        return `> **Thinking:** ${seg.content}\n\n`;
      case 'text':
        return seg.content + '\n\n';
      case 'command':
        return `\`\`\`bash\n$ ${seg.content}\n\`\`\`\n\n`;
      case 'command_output':
        return `\`\`\`\n${seg.content}\n\`\`\`\n\n`;
      case 'file_change':
        return `**Files changed:**\n\`\`\`\n${seg.content}\n\`\`\`\n\n`;
      case 'plan':
        return `**Plan:**\n${seg.content}\n\n`;
      case 'error':
        return `**Error:** ${seg.content}\n\n`;
      case 'usage':
        return `---\n*${seg.content}*\n`;
      default:
        return seg.content;
    }
  }).join('');
}
