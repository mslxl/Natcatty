import { useCallback, useEffect, useMemo, useRef } from 'react';
import type { Snippet } from '@/domain/models';
import { snippetAppliesToOutputTrigger } from '@/domain/snippetTargets.ts';
import { isScriptSnippet } from '@/domain/snippetScript.ts';
import { createTerminalOutputTriggerFilter } from '@/domain/terminalOutputTriggerFilter.ts';
import { netcattyBridge } from '@/infrastructure/services/netcattyBridge.ts';
import { getActiveScriptRunForSession } from '@/application/state/scriptAutomationCoordinator.ts';

const OUTPUT_TRIGGER_SCAN_DELAY_MS = 16;
const OUTPUT_TRIGGER_SCAN_CHUNK_BYTES = 32 * 1024;
const OUTPUT_TRIGGER_SCAN_BYTES_PER_FLUSH = 128 * 1024;
const OUTPUT_TRIGGER_SCAN_TIME_BUDGET_MS = 4;
const OUTPUT_TRIGGER_SCAN_OVERLAP_CHARS = 64;

type OutputTriggerContext = {
  sessionId: string;
  hostId?: string;
  snippets: Snippet[];
  onRunScript: (snippet: Snippet, sessionId: string) => void | Promise<void>;
};

type DeferredOutputTriggerEvent =
  | { type: 'output'; chunk: string }
  | { type: 'input'; data: string };

type DeferredOutputTriggerEventProcessor = {
  enqueueOutput: (chunk: string) => void;
  enqueueInput: (data: string) => void;
  flush: () => void;
  reset: () => void;
};

type DeferredOutputTriggerEventProcessorOptions = {
  processOutput: (chunk: string) => void;
  processInput: (data: string) => void;
  schedule: (callback: () => void) => () => void;
  now?: () => number;
  maxOutputChunkBytes?: number;
  maxOutputBytesPerFlush?: number;
  maxFlushMs?: number;
};

type OutputTriggerScanWindow = {
  text: string;
  minEndOffset: number;
  baseOffset: number;
};

type OutputTriggerScanBuffer = {
  append: (chunk: string) => OutputTriggerScanWindow;
  reset: () => void;
};

function isSessionScriptRunActive(sessionId: string): boolean {
  return Boolean(getActiveScriptRunForSession(sessionId));
}

export function createOutputTriggerScanBuffer(
  overlapChars = OUTPUT_TRIGGER_SCAN_OVERLAP_CHARS,
): OutputTriggerScanBuffer {
  let overlap = '';
  let consumedLength = 0;

  return {
    append(chunk: string) {
      const text = `${overlap}${chunk}`;
      const minEndOffset = overlap.length;
      const baseOffset = consumedLength - overlap.length;
      consumedLength += chunk.length;
      overlap = text.slice(-overlapChars);
      return { text, minEndOffset, baseOffset };
    },
    reset() {
      overlap = '';
      consumedLength = 0;
    },
  };
}

export function createDeferredOutputTriggerEventProcessor({
  processOutput,
  processInput,
  schedule,
  now = () => performance.now(),
  maxOutputChunkBytes = OUTPUT_TRIGGER_SCAN_CHUNK_BYTES,
  maxOutputBytesPerFlush = OUTPUT_TRIGGER_SCAN_BYTES_PER_FLUSH,
  maxFlushMs = OUTPUT_TRIGGER_SCAN_TIME_BUDGET_MS,
}: DeferredOutputTriggerEventProcessorOptions): DeferredOutputTriggerEventProcessor {
  const events: DeferredOutputTriggerEvent[] = [];
  let cancelScheduledFlush: (() => void) | null = null;

  const scheduleFlush = () => {
    if (cancelScheduledFlush) return;
    cancelScheduledFlush = schedule(() => {
      cancelScheduledFlush = null;
      processor.flush();
    });
  };

  const processor: DeferredOutputTriggerEventProcessor = {
    enqueueOutput(chunk: string) {
      if (!chunk) return;
      const lastEvent = events.at(-1);
      if (
        lastEvent?.type === 'output'
        && lastEvent.chunk.length + chunk.length <= maxOutputBytesPerFlush
      ) {
        lastEvent.chunk += chunk;
      } else {
        events.push({ type: 'output', chunk });
      }
      scheduleFlush();
    },
    enqueueInput(data: string) {
      if (!data) return;
      const lastEvent = events.at(-1);
      if (lastEvent?.type === 'input') {
        lastEvent.data += data;
      } else {
        events.push({ type: 'input', data });
      }
      scheduleFlush();
    },
    flush() {
      const startedAt = now();
      let processedOutputBytes = 0;

      while (events.length > 0) {
        const event = events[0];
        if (event.type === 'input') {
          events.shift();
          processInput(event.data);
        } else {
          const chunk = event.chunk.slice(0, maxOutputChunkBytes);
          event.chunk = event.chunk.slice(chunk.length);
          if (!event.chunk) {
            events.shift();
          }
          processedOutputBytes += chunk.length;
          processOutput(chunk);
        }

        if (
          events.length > 0
          && (
            processedOutputBytes >= maxOutputBytesPerFlush
            || now() - startedAt >= maxFlushMs
          )
        ) {
          scheduleFlush();
          return;
        }
      }
    },
    reset() {
      events.splice(0, events.length);
      cancelScheduledFlush?.();
      cancelScheduledFlush = null;
    },
  };

  return processor;
}

export function findMatchEndingAfter(text: string, pattern: string, minEndOffset: number): { value: string; endOffset: number } | null {
  const source = new RegExp(pattern);
  for (let startOffset = 0; startOffset <= text.length;) {
    const match = source.exec(text.slice(startOffset));
    if (!match || match.index === undefined) return null;
    const absoluteStart = startOffset + match.index;
    const absoluteEnd = absoluteStart + match[0].length;
    if (absoluteEnd > minEndOffset) {
      return { value: match[0], endOffset: absoluteEnd };
    }
    startOffset = Math.max(absoluteStart + 1, absoluteEnd);
  }
  return null;
}

export function hasApplicableOutputTriggerSnippet(snippets: Snippet[], hostId?: string): boolean {
  return snippets.some((snippet) => (
    isScriptSnippet(snippet)
    && snippet.trigger === 'onOutput'
    && Boolean(snippet.triggerPattern)
    && Boolean(snippet.id)
    && snippetAppliesToOutputTrigger(snippet, hostId)
  ));
}

export function useOutputTriggers({
  sessionId,
  hostId,
  snippets,
  onRunScript,
}: OutputTriggerContext) {
  const launchingRef = useRef(false);
  const lastTriggerMatchEndRef = useRef(new Map<string, number>());
  const serverOutputFilterRef = useRef(createTerminalOutputTriggerFilter());
  const scanBufferRef = useRef(createOutputTriggerScanBuffer());
  const hasOutputTriggers = useMemo(
    () => hasApplicableOutputTriggerSnippet(snippets, hostId),
    [hostId, snippets],
  );

  const scanOutput = useCallback((scannableText: string) => {
    if (!hasOutputTriggers || !scannableText) {
      return;
    }

    const scanWindow = scanBufferRef.current.append(scannableText);
    if (isSessionScriptRunActive(sessionId) || launchingRef.current) {
      return;
    }

    for (const snippet of snippets) {
      if (isSessionScriptRunActive(sessionId) || launchingRef.current) {
        return;
      }
      if (!isScriptSnippet(snippet) || snippet.trigger !== 'onOutput' || !snippet.triggerPattern || !snippet.id) {
        continue;
      }
      if (!snippetAppliesToOutputTrigger(snippet, hostId)) continue;
      try {
        const matched = findMatchEndingAfter(scanWindow.text, snippet.triggerPattern, scanWindow.minEndOffset);
        if (!matched) {
          continue;
        }
        const matchEnd = scanWindow.baseOffset + matched.endOffset;
        const lastMatchEnd = lastTriggerMatchEndRef.current.get(snippet.id) ?? -1;
        if (matchEnd <= lastMatchEnd) {
          continue;
        }
        const matchedSnippetId = snippet.id;
        launchingRef.current = true;
        lastTriggerMatchEndRef.current.set(matchedSnippetId, matchEnd);
        void Promise.resolve(onRunScript(snippet, sessionId))
          .catch(() => {
            // Failed starts can retry on the next matching output chunk.
          })
          .finally(() => {
            launchingRef.current = false;
          });
        return;
      } catch {
        // ignore invalid regex
      }
    }
  }, [hasOutputTriggers, hostId, onRunScript, sessionId, snippets]);

  const scanOutputRef = useRef(scanOutput);
  scanOutputRef.current = scanOutput;

  const processOutputTriggerOutput = useCallback((chunk: string) => {
    const { scannableText, alternateScreenActive } = serverOutputFilterRef.current.processServerChunk(chunk);
    if (!scannableText || alternateScreenActive) {
      return;
    }
    scanOutputRef.current(scannableText);
  }, []);

  const processOutputTriggerInput = useCallback((data: string) => {
    serverOutputFilterRef.current.noteUserInput(data);
  }, []);

  const outputTriggerEventProcessor = useMemo(() => createDeferredOutputTriggerEventProcessor({
    processOutput: processOutputTriggerOutput,
    processInput: processOutputTriggerInput,
    schedule: (callback) => {
      const timer = globalThis.setTimeout(callback, OUTPUT_TRIGGER_SCAN_DELAY_MS);
      return () => globalThis.clearTimeout(timer);
    },
  }), [processOutputTriggerInput, processOutputTriggerOutput]);

  const appendOutput = useCallback((chunk: string) => {
    if (!hasOutputTriggers || !chunk) return;
    outputTriggerEventProcessor.enqueueOutput(chunk);
  }, [hasOutputTriggers, outputTriggerEventProcessor]);

  const noteUserInput = useCallback((data: string) => {
    if (!hasOutputTriggers || !data) return;
    outputTriggerEventProcessor.enqueueInput(data);
  }, [hasOutputTriggers, outputTriggerEventProcessor]);

  useEffect(() => {
    outputTriggerEventProcessor.reset();
    scanBufferRef.current.reset();
    launchingRef.current = false;
    lastTriggerMatchEndRef.current = new Map();
    serverOutputFilterRef.current.reset();
  }, [sessionId, hostId, hasOutputTriggers, outputTriggerEventProcessor]);

  useEffect(() => () => {
    outputTriggerEventProcessor.reset();
  }, [outputTriggerEventProcessor]);

  return { appendOutput, noteUserInput };
}

export function setupScriptBridgeListeners(
  getSnapshot: (sessionId: string) => ReturnType<typeof import('@/infrastructure/scripts/screenSnapshotRegistry.ts').captureScreenSnapshot>,
) {
  const disposers: Array<() => void> = [];

  disposers.push(
    netcattyBridge.get()?.onScriptScreenSnapshotRequest?.(({ requestId, sessionId }) => {
      const snapshot = getSnapshot(sessionId);
      void netcattyBridge.get()?.scriptScreenSnapshotResponse?.(requestId, snapshot);
    }) ?? (() => {}),
  );

  return () => {
    disposers.forEach((dispose) => dispose());
  };
}
