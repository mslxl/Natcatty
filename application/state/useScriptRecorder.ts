import { useCallback, useEffect, useRef, useState } from 'react';
import { netcattyBridge } from '@/infrastructure/services/netcattyBridge.ts';
import { DEFAULT_RECORDING_PROMPT_TIMEOUT_MS } from '@/domain/snippetScript.ts';
import type { ScriptRecordingStep } from '@/types/global/netcatty-bridge-script.d.ts';
import { notify } from '../notification';

export const MAX_PENDING_SCRIPT_RECORDING_INPUT_CHARS = 256 * 1024;
export const SCRIPT_RECORDING_LIMIT_EVENT = 'netcatty:script:recording:limit';

type ScriptRecordingLimitDetail = {
  sessionId: string;
  error: string;
  steps: ScriptRecordingStep[];
  code: string;
};

export function useScriptRecorder(sessionId: string | undefined) {
  const [isRecording, setIsRecording] = useState(false);
  const [isPaused, setIsPaused] = useState(false);
  const [elapsedMs, setElapsedMs] = useState(0);
  const startedAtRef = useRef<number | null>(null);
  const inputBufferRef = useRef('');
  const lastStepAtRef = useRef<number>(Date.now());
  const isRecordingRef = useRef(false);
  const isPausedRef = useRef(false);
  const sessionIdRef = useRef(sessionId);

  sessionIdRef.current = sessionId;
  isRecordingRef.current = isRecording;
  isPausedRef.current = isPaused;

  useEffect(() => {
    if (!isRecording || isPaused) return undefined;
    const timer = window.setInterval(() => {
      if (startedAtRef.current) {
        setElapsedMs(Date.now() - startedAtRef.current);
      }
    }, 250);
    return () => window.clearInterval(timer);
  }, [isRecording, isPaused]);

  const startRecording = useCallback(async () => {
    const sid = sessionIdRef.current;
    const bridge = netcattyBridge.get();
    if (!sid || !bridge?.scriptRecordingStart) return;
    await bridge.scriptRecordingStart(sid);
    startedAtRef.current = Date.now();
    lastStepAtRef.current = Date.now();
    inputBufferRef.current = '';
    setElapsedMs(0);
    setIsPaused(false);
    isPausedRef.current = false;
    isRecordingRef.current = true;
    setIsRecording(true);
  }, []);

  const stopRecording = useCallback(async () => {
    const sid = sessionIdRef.current;
    const bridge = netcattyBridge.get();
    if (!sid || !bridge?.scriptRecordingStop) {
      return { steps: [] as ScriptRecordingStep[], code: '' };
    }
    const result = await bridge.scriptRecordingStop(sid);
    isRecordingRef.current = false;
    isPausedRef.current = false;
    setIsRecording(false);
    setIsPaused(false);
    startedAtRef.current = null;
    return result;
  }, []);

  const finishAutomaticStop = useCallback((detail: ScriptRecordingLimitDetail) => {
    isRecordingRef.current = false;
    isPausedRef.current = false;
    inputBufferRef.current = '';
    startedAtRef.current = null;
    setIsRecording(false);
    setIsPaused(false);
    notify.error(detail.error, 'Scripts');
    window.dispatchEvent(new CustomEvent(SCRIPT_RECORDING_LIMIT_EVENT, { detail }));
  }, []);

  const pauseRecording = useCallback(() => {
    isPausedRef.current = true;
    setIsPaused(true);
  }, []);

  const resumeRecording = useCallback(() => {
    isPausedRef.current = false;
    setIsPaused(false);
  }, []);

  const appendStep = useCallback(async (step: ScriptRecordingStep) => {
    const sid = sessionIdRef.current;
    if (!sid || !isRecordingRef.current || isPausedRef.current) return;
    const result = await netcattyBridge.get()?.scriptRecordingAppendStep?.(sid, step);
    if (result?.stopped) {
      finishAutomaticStop({
        sessionId: sid,
        error: result.error || 'Recording stopped because it reached the safety limit',
        steps: result.steps ?? [],
        code: result.code ?? '',
      });
      return;
    }
    lastStepAtRef.current = Date.now();
  }, [finishAutomaticStop]);

  const recordInput = useCallback((data: string) => {
    if (!isRecordingRef.current || isPausedRef.current) return;
    const next = `${inputBufferRef.current}${data}`;
    if (next.length <= MAX_PENDING_SCRIPT_RECORDING_INPUT_CHARS) {
      inputBufferRef.current = next;
      return;
    }
    const sid = sessionIdRef.current;
    if (!sid) return;
    // Stop immediately before awaiting IPC so further key events cannot grow
    // the renderer buffer while the partial recording is finalized.
    isRecordingRef.current = false;
    inputBufferRef.current = '';
    void netcattyBridge.get()?.scriptRecordingStop?.(sid).then((result) => {
      finishAutomaticStop({
        sessionId: sid,
        error: 'Recording stopped because the current input exceeded the safety limit',
        steps: result.steps,
        code: result.code,
      });
    }).catch(() => {
      finishAutomaticStop({
        sessionId: sid,
        error: 'Recording stopped because the current input exceeded the safety limit',
        steps: [],
        code: '',
      });
    });
  }, [finishAutomaticStop]);

  const recordBackspace = useCallback(() => {
    if (!isRecordingRef.current || isPausedRef.current) return;
    inputBufferRef.current = inputBufferRef.current.slice(0, -1);
  }, []);

  const recordClearLine = useCallback(() => {
    if (!isRecordingRef.current || isPausedRef.current) return;
    inputBufferRef.current = '';
  }, []);

  const recordEnter = useCallback(async (options?: { sensitive?: boolean }) => {
    const sid = sessionIdRef.current;
    if (!isRecordingRef.current || isPausedRef.current || !sid) return;
    const line = inputBufferRef.current;
    inputBufferRef.current = '';
    const now = Date.now();
    const gap = now - lastStepAtRef.current;
    if (gap > 1000) {
      await appendStep({ type: 'sleep', value: gap });
    }
    await appendStep({
      type: 'send',
      value: line,
      sensitive: options?.sensitive,
    });
    await appendStep({ type: 'waitForPrompt', timeoutMs: DEFAULT_RECORDING_PROMPT_TIMEOUT_MS });
    lastStepAtRef.current = now;
  }, [appendStep]);

  return {
    isRecording,
    isPaused,
    elapsedMs,
    startRecording,
    stopRecording,
    pauseRecording,
    resumeRecording,
    recordInput,
    recordBackspace,
    recordClearLine,
    recordEnter,
  };
}
