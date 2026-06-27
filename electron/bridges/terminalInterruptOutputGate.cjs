"use strict";

const DEFAULT_QUIET_MS = 500;
const DEFAULT_PROMPT_QUIET_MS = 80;
const DEFAULT_MAX_DRAIN_MS = 2500;
const DEFAULT_PROMPT_CANDIDATE_BYTES = 512;
const OUTPUT_GATE_UNACKED_THRESHOLD = 8192;

function nowFromOptions(options = {}) {
  return Number.isFinite(options.now) ? options.now : Date.now();
}

function byteLength(value) {
  if (Buffer.isBuffer(value)) return value.length;
  return Buffer.byteLength(String(value || ""));
}

function getStreamPaused(stream) {
  try {
    return typeof stream?.isPaused === "function" ? stream.isPaused() : false;
  } catch {
    return false;
  }
}

function stripAnsi(value) {
  return String(value || "").replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "");
}

function getPromptCandidateSuffix(text) {
  const raw = String(text || "");
  const normalized = stripAnsi(raw).replace(/\r/g, "\n");
  const lastLineStart = normalized.lastIndexOf("\n") + 1;
  const candidate = normalized.slice(lastLineStart).trimEnd();
  if (!candidate) return null;
  if (candidate.length > 160) return null;

  const looksLikePrompt = (
    /^[#$>%]\s*$/.test(candidate)
    || /^[^ \t\r\n<>]{1,80}[#$>%]\s*$/.test(candidate)
    || /^<[^>\r\n]{1,80}>\s*$/.test(candidate)
    || /^\[[^\]\r\n]{1,120}\]\s*[#$>%]\s*$/.test(candidate)
  );
  if (!looksLikePrompt) return null;

  const rawLastBreak = Math.max(raw.lastIndexOf("\n"), raw.lastIndexOf("\r"));
  return raw.slice(rawLastBreak + 1);
}

function shouldArmTerminalInterruptOutputGate(session) {
  if (!session?.stream) return false;
  const flowState = session.flowState;
  return Boolean(
    getStreamPaused(session.stream)
    || flowState?.appliedPause
    || flowState?.rendererPaused
    || (Number(flowState?.unackedBytes) || 0) >= OUTPUT_GATE_UNACKED_THRESHOLD
  );
}

function armTerminalInterruptOutputGate(session, options = {}) {
  if (!session) return false;
  session._interruptOutputGate = {
    active: true,
    startedAt: nowFromOptions(options),
    lastDroppedAt: 0,
    quietMs: Number.isFinite(options.quietMs) ? options.quietMs : DEFAULT_QUIET_MS,
    promptQuietMs: Number.isFinite(options.promptQuietMs) ? options.promptQuietMs : DEFAULT_PROMPT_QUIET_MS,
    maxDrainMs: Number.isFinite(options.maxDrainMs) ? options.maxDrainMs : DEFAULT_MAX_DRAIN_MS,
    promptCandidateBytes: Number.isFinite(options.promptCandidateBytes)
      ? options.promptCandidateBytes
      : DEFAULT_PROMPT_CANDIDATE_BYTES,
    droppedBytes: 0,
    droppedChunks: 0,
    pendingInterruptCaret: false,
  };
  return true;
}

function disarmTerminalInterruptOutputGate(session) {
  if (session?._interruptOutputGate) {
    session._interruptOutputGate.active = false;
  }
}

function filterTerminalInterruptOutput(session, data, options = {}) {
  const gate = session?._interruptOutputGate;
  const text = String(data || "");
  if (!gate?.active) {
    return { accepted: true, data: text, droppedBytes: 0, reason: "inactive" };
  }

  const now = nowFromOptions(options);
  const bytes = byteLength(data);
  const quietGapMs = gate.lastDroppedAt > 0 ? now - gate.lastDroppedAt : 0;

  if (gate.pendingInterruptCaret) {
    gate.pendingInterruptCaret = false;
    if (text.startsWith("C")) {
      disarmTerminalInterruptOutputGate(session);
      return {
        accepted: true,
        data: `^${text}`,
        droppedBytes: 0,
        reason: "interrupt-echo",
      };
    }
  }

  const interruptEchoIndex = text.indexOf("^C");
  if (interruptEchoIndex >= 0) {
    const droppedBytes = byteLength(text.slice(0, interruptEchoIndex));
    gate.droppedBytes += droppedBytes;
    gate.droppedChunks += droppedBytes > 0 ? 1 : 0;
    disarmTerminalInterruptOutputGate(session);
    return {
      accepted: true,
      data: text.slice(interruptEchoIndex),
      droppedBytes,
      reason: "interrupt-echo",
    };
  }

  if (gate.droppedBytes === 0 && bytes <= gate.promptCandidateBytes) {
    const promptCandidate = getPromptCandidateSuffix(text);
    if (promptCandidate) {
      const droppedBytes = byteLength(text.slice(0, text.length - promptCandidate.length));
      gate.droppedBytes += droppedBytes;
      gate.droppedChunks += droppedBytes > 0 ? 1 : 0;
      disarmTerminalInterruptOutputGate(session);
      return {
        accepted: true,
        data: promptCandidate,
        droppedBytes,
        reason: "prompt-candidate",
      };
    }
  }
  if (quietGapMs >= gate.promptQuietMs && bytes <= gate.promptCandidateBytes) {
    const promptCandidate = getPromptCandidateSuffix(text);
    if (promptCandidate) {
      const droppedBytes = byteLength(text.slice(0, text.length - promptCandidate.length));
      gate.droppedBytes += droppedBytes;
      gate.droppedChunks += droppedBytes > 0 ? 1 : 0;
      disarmTerminalInterruptOutputGate(session);
      return {
        accepted: true,
        data: promptCandidate,
        droppedBytes,
        reason: "prompt-gap",
      };
    }
  }

  if (quietGapMs >= gate.quietMs) {
    disarmTerminalInterruptOutputGate(session);
    return { accepted: true, data: text, droppedBytes: 0, reason: "quiet-gap" };
  }

  if (now - gate.startedAt >= gate.maxDrainMs) {
    disarmTerminalInterruptOutputGate(session);
    return { accepted: true, data: text, droppedBytes: 0, reason: "max-drain" };
  }

  gate.pendingInterruptCaret = text.endsWith("^");
  gate.lastDroppedAt = now;
  gate.droppedBytes += bytes;
  gate.droppedChunks += 1;
  return { accepted: false, data: "", droppedBytes: bytes, reason: "draining" };
}

module.exports = {
  armTerminalInterruptOutputGate,
  disarmTerminalInterruptOutputGate,
  filterTerminalInterruptOutput,
  shouldArmTerminalInterruptOutputGate,
};
