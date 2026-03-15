import type {
  ExternalAgentConfig,
} from './types';
import { parseAgentJsonLine, formatSegmentsAsMarkdown } from './agentOutputParser';

/** Callbacks for streaming external agent output */
export interface ExternalAgentCallbacks {
  onTextDelta: (text: string) => void;
  onError: (error: string) => void;
  onDone: () => void;
}

/**
 * Bridge interface matching the agent-related methods from window.netcatty
 */
interface AgentBridge {
  aiSpawnAgent(
    agentId: string,
    command: string,
    args?: string[],
    env?: Record<string, string>,
    options?: { closeStdin?: boolean },
  ): Promise<{ ok: boolean; pid?: number; error?: string }>;
  aiWriteToAgent(agentId: string, data: string): Promise<{ ok: boolean; error?: string }>;
  aiCloseAgentStdin(agentId: string): Promise<{ ok: boolean; error?: string }>;
  aiKillAgent(agentId: string): Promise<{ ok: boolean; error?: string }>;
  onAiAgentStdout(agentId: string, cb: (data: string) => void): () => void;
  onAiAgentStderr(agentId: string, cb: (data: string) => void): () => void;
  onAiAgentExit(agentId: string, cb: (code: number) => void): () => void;
}

const PROMPT_PLACEHOLDER = '{prompt}';

/**
 * Build the final command and args for an external agent.
 */
function buildAgentInvocation(
  config: ExternalAgentConfig,
  userMessage: string,
): { command: string; args: string[]; useStdin: boolean; jsonMode: boolean } {
  const command = config.command;
  const templateArgs = config.args || [];

  const hasPlaceholder = templateArgs.some(a => a.includes(PROMPT_PLACEHOLDER));
  const jsonMode = templateArgs.includes('--json');

  if (hasPlaceholder) {
    const args = templateArgs.map(a =>
      a === PROMPT_PLACEHOLDER ? userMessage : a.replaceAll(PROMPT_PLACEHOLDER, userMessage),
    );
    return { command, args, useStdin: false, jsonMode };
  }

  return { command, args: [...templateArgs], useStdin: true, jsonMode };
}

/**
 * Creates a stdout handler that parses JSON Lines (for --json mode agents)
 * and converts structured events to formatted markdown text.
 *
 * Handles partial lines since stdout chunks can split mid-line.
 */
function createJsonLinesHandler(onText: (text: string) => void): (data: string) => void {
  let lineBuffer = '';
  // Track seen item IDs to avoid duplicating command blocks
  // (item.started shows the command, item.completed shows command + output)
  const seenCommands = new Set<string>();

  return (data: string) => {
    lineBuffer += data;
    const lines = lineBuffer.split('\n');
    // Keep the last (possibly incomplete) line in the buffer
    lineBuffer = lines.pop() || '';

    for (const line of lines) {
      if (!line.trim()) continue;

      const segments = parseAgentJsonLine(line);
      if (segments === null) {
        // Not JSON — pass through as plain text
        onText(line + '\n');
        continue;
      }

      if (segments.length === 0) continue;

      // Deduplicate command_execution: skip started if we'll get completed
      const filtered = segments.filter(seg => {
        if (seg.type === 'command') {
          if (seenCommands.has(seg.content)) return false;
          seenCommands.add(seg.content);
        }
        return true;
      });

      if (filtered.length > 0) {
        const markdown = formatSegmentsAsMarkdown(filtered);
        onText(markdown);
      }
    }
  };
}

/**
 * Start an external agent and send a message through it.
 */
export async function runExternalAgentTurn(
  config: ExternalAgentConfig,
  userMessage: string,
  callbacks: ExternalAgentCallbacks,
  bridge: AgentBridge | undefined,
  signal?: AbortSignal,
): Promise<void> {
  if (!bridge) {
    callbacks.onError('Bridge not available');
    return;
  }

  const agentId = `ext_${config.id}_${Date.now()}`;
  const { command, args, useStdin, jsonMode } = buildAgentInvocation(config, userMessage);

  const cleanupFns: (() => void)[] = [];
  let done = false;

  const finish = () => {
    if (done) return;
    done = true;
    for (const fn of cleanupFns) {
      try { fn(); } catch { /* cleanup */ }
    }
    callbacks.onDone();
  };

  // ── Set up event listeners BEFORE spawning to avoid race condition ──

  // For JSON mode, parse structured events; otherwise, pass through raw text
  const stdoutHandler = jsonMode
    ? createJsonLinesHandler((text) => { if (!done) callbacks.onTextDelta(text); })
    : (data: string) => { if (!done) callbacks.onTextDelta(data); };

  const unsubStdout = bridge.onAiAgentStdout(agentId, stdoutHandler);
  cleanupFns.push(unsubStdout);

  // Collect stderr
  let stderrBuffer = '';
  const unsubStderr = bridge.onAiAgentStderr(agentId, (data) => {
    stderrBuffer += data;
  });
  cleanupFns.push(unsubStderr);

  let resolveExit: (code: number | null) => void;
  const exitPromise = new Promise<number | null>((resolve) => {
    resolveExit = resolve;
    const unsubExit = bridge.onAiAgentExit(agentId, (code) => {
      resolve(code);
    });
    cleanupFns.push(unsubExit);
  });

  // Handle abort
  if (signal) {
    if (signal.aborted) {
      finish();
      return;
    }
    const onAbort = () => {
      bridge.aiKillAgent(agentId).catch(() => {});
      callbacks.onError('Cancelled');
      resolveExit(null);
      finish();
    };
    signal.addEventListener('abort', onAbort, { once: true });
    cleanupFns.push(() => signal.removeEventListener('abort', onAbort));
  }

  // ── Spawn the process ──
  const result = await bridge.aiSpawnAgent(
    agentId,
    command,
    args,
    config.env,
    { closeStdin: !useStdin },
  );

  if (!result.ok) {
    callbacks.onError(`Failed to start ${config.name}: ${result.error}`);
    finish();
    return;
  }

  // Send the user message via stdin if needed, then close stdin (EOF)
  if (useStdin) {
    try {
      await bridge.aiWriteToAgent(agentId, userMessage + '\n');
      await bridge.aiCloseAgentStdin(agentId);
    } catch (err) {
      callbacks.onError(`Failed to write to agent: ${err}`);
      finish();
      return;
    }
  }

  // Timeout after 5 minutes
  const timeout = setTimeout(() => {
    if (!done) {
      bridge.aiKillAgent(agentId).catch(() => {});
      callbacks.onError('Agent timeout (5 minutes)');
      resolveExit(null);
      finish();
    }
  }, 300000);
  cleanupFns.push(() => clearTimeout(timeout));

  // Wait for the process to exit
  const exitCode = await exitPromise;

  // If process exited with error and no stdout was received, report stderr
  if (exitCode !== 0 && stderrBuffer.trim() && !done) {
    callbacks.onError(stderrBuffer.trim());
  }

  finish();
}

/**
 * Kill a running external agent session
 */
export async function killExternalAgent(
  agentId: string,
  bridge: AgentBridge | undefined,
): Promise<void> {
  if (bridge) {
    await bridge.aiKillAgent(agentId).catch(() => {});
  }
}
