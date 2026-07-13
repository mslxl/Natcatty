import type { Terminal as XTerm } from "@xterm/xterm";

import { XTERM_PERFORMANCE_CONFIG } from "../../../infrastructure/config/xtermPerformance";
import { TERMINAL_LONG_LINE_PRESSURE_BYTES } from "./terminalFlowConstants";

export type TerminalOutputPressureMode =
  | "normal"
  | "large-output"
  | "long-line"
  | "background";

export type TerminalOutputPressureSnapshot = {
  mode: TerminalOutputPressureMode;
  background: boolean;
  largeOutput: boolean;
  longLine: boolean;
  consecutiveUnbrokenBytes: number;
};

type OutputRateSample = {
  at: number;
  bytes: number;
};

type TerminalOutputPressureState = {
  background: boolean;
  largeOutput: boolean;
  largeOutputUntil: number;
  longLine: boolean;
  consecutiveUnbrokenBytes: number;
  /** True rolling window samples for high-rate small-chunk detection. */
  recentSamples: OutputRateSample[];
  recentSampleBytes: number;
};

/** Detect bulk streams that arrive as many small IPC chunks (e.g. `yes`). */
const LARGE_OUTPUT_RATE_WINDOW_MS = 100;
const LARGE_OUTPUT_RATE_BYTES = 64 * 1024;

const pressureStates = new WeakMap<XTerm, TerminalOutputPressureState>();

const getOrCreateState = (term: XTerm): TerminalOutputPressureState => {
  let state = pressureStates.get(term);
  if (!state) {
    state = {
      background: false,
      largeOutput: false,
      largeOutputUntil: 0,
      longLine: false,
      consecutiveUnbrokenBytes: 0,
      recentSamples: [],
      recentSampleBytes: 0,
    };
    pressureStates.set(term, state);
  }
  return state;
};

const noteRecentOutputRate = (
  state: TerminalOutputPressureState,
  now: number,
  bytes: number,
): number => {
  state.recentSamples.push({ at: now, bytes });
  state.recentSampleBytes += bytes;
  const cutoff = now - LARGE_OUTPUT_RATE_WINDOW_MS;
  while (state.recentSamples.length > 0 && state.recentSamples[0]!.at < cutoff) {
    const dropped = state.recentSamples.shift()!;
    state.recentSampleBytes -= dropped.bytes;
  }
  if (state.recentSampleBytes < 0) state.recentSampleBytes = 0;
  return state.recentSampleBytes;
};

const LINE_BREAK_SCAN = /[\n\r]/g;

const measureUnbrokenRuns = (
  data: string,
  initialRunBytes: number,
): { maxRunBytes: number; trailingRunBytes: number } => {
  // Hot path for every output batch: hop between line breaks with a native
  // regex scan instead of visiting each character in JS. A run only counts
  // toward the max when this chunk actually appended characters to it,
  // matching the original per-char accounting.
  let maxRunBytes = 0;
  let runStart = 0;
  let carriedRunBytes = initialRunBytes;
  LINE_BREAK_SCAN.lastIndex = 0;
  for (
    let match = LINE_BREAK_SCAN.exec(data);
    match !== null;
    match = LINE_BREAK_SCAN.exec(data)
  ) {
    const appendedBytes = match.index - runStart;
    if (appendedBytes > 0) {
      const runBytes = carriedRunBytes + appendedBytes;
      if (runBytes > maxRunBytes) {
        maxRunBytes = runBytes;
      }
    }
    carriedRunBytes = 0;
    runStart = match.index + 1;
  }
  const trailingAppendedBytes = data.length - runStart;
  const trailingRunBytes = carriedRunBytes + trailingAppendedBytes;
  if (trailingAppendedBytes > 0 && trailingRunBytes > maxRunBytes) {
    maxRunBytes = trailingRunBytes;
  }
  return { maxRunBytes, trailingRunBytes };
};

const markLargeOutput = (state: TerminalOutputPressureState, now: number): void => {
  state.largeOutputUntil = now + XTERM_PERFORMANCE_CONFIG.highlighting.largeOutputQuietMs;
  state.largeOutput = true;
};

export const noteTerminalOutputPressureData = (
  term: XTerm,
  data: string,
): void => {
  if (!data) return;
  const state = getOrCreateState(term);
  const now = performance.now();

  const recentBytes = noteRecentOutputRate(state, now, data.length);

  if (
    data.length >= TERMINAL_LONG_LINE_PRESSURE_BYTES
    || recentBytes >= LARGE_OUTPUT_RATE_BYTES
  ) {
    markLargeOutput(state, now);
  } else if (now >= state.largeOutputUntil) {
    state.largeOutput = false;
  }

  const { maxRunBytes, trailingRunBytes } = measureUnbrokenRuns(
    data,
    state.consecutiveUnbrokenBytes,
  );
  state.consecutiveUnbrokenBytes = trailingRunBytes;
  state.longLine = maxRunBytes >= TERMINAL_LONG_LINE_PRESSURE_BYTES;
};

export const setTerminalOutputPressureVisibility = (
  term: XTerm,
  visible: boolean,
): void => {
  getOrCreateState(term).background = !visible;
};

export const setTerminalOutputPressureLargeOutput = (
  term: XTerm,
  largeOutput: boolean,
): void => {
  const state = getOrCreateState(term);
  state.largeOutput = largeOutput;
  state.largeOutputUntil = largeOutput
    ? performance.now() + XTERM_PERFORMANCE_CONFIG.highlighting.largeOutputQuietMs
    : 0;
};

export const getTerminalOutputPressure = (
  term: XTerm,
): TerminalOutputPressureSnapshot => {
  const state = getOrCreateState(term);
  const largeOutput = state.largeOutput && performance.now() < state.largeOutputUntil;
  const mode: TerminalOutputPressureMode = state.background
    ? "background"
    : state.longLine
      ? "long-line"
      : largeOutput
        ? "large-output"
        : "normal";

  return {
    mode,
    background: state.background,
    largeOutput,
    longLine: state.longLine,
    consecutiveUnbrokenBytes: state.consecutiveUnbrokenBytes,
  };
};

/**
 * True when hot-path side work (timestamps, highlight scans) should degrade so
 * xterm can keep painting bulk output smoothly.
 */
export const shouldDegradeTerminalSideWork = (term: XTerm): boolean => {
  const pressure = getTerminalOutputPressure(term);
  return pressure.background || pressure.largeOutput || pressure.longLine;
};

export const resetTerminalOutputPressure = (term: XTerm): void => {
  pressureStates.delete(term);
};
