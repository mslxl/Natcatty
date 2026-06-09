import type { ModelMessage } from "ai";

const RETRY_MAX_TOOL_RESULT_CHARS = 4_000;
const RETRY_MAX_MESSAGE_TEXT_CHARS = 8_000;
const TRUNCATION_MARKER = "\n\n[... output truncated for request size ...]\n\n";
const HEAD_CHARS = 800;
const TAIL_CHARS = 4_000;

export interface CompressMessagesForRequestTooLargeRetryResult {
  messages: ModelMessage[];
  didAdjust: boolean;
}

/**
 * Collapse noisy terminal/build output.
 * Keeps semantics while removing repeated blank lines and long duplicate runs.
 */
export function compressVerboseText(value: string): string {
  if (!value) return value;

  let compressed = value.replace(/\r\n/g, "\n");
  compressed = compressed.replace(/\n{4,}/g, "\n\n\n");

  const lines = compressed.split("\n");
  const deduped: string[] = [];
  let repeatCount = 0;
  for (const line of lines) {
    const previous = deduped[deduped.length - 1];
    if (previous === line) {
      repeatCount += 1;
      if (repeatCount <= 2) deduped.push(line);
      continue;
    }
    repeatCount = 0;
    deduped.push(line);
  }

  return deduped.join("\n");
}

export function truncateTextWithHeadAndTail(
  value: string,
  maxChars: number,
  {
    headChars = HEAD_CHARS,
    tailChars = TAIL_CHARS,
    marker = TRUNCATION_MARKER,
  }: {
    headChars?: number;
    tailChars?: number;
    marker?: string;
  } = {},
): string {
  if (value.length <= maxChars) return value;
  if (maxChars <= marker.length + 16) {
    return value.slice(0, maxChars);
  }

  const budget = maxChars - marker.length;
  const head = Math.min(headChars, budget);
  let tail = Math.min(tailChars, Math.max(0, budget - head));
  if (head + tail > budget) {
    tail = Math.max(0, budget - head);
  }
  if (head + tail >= value.length) {
    return value.slice(0, maxChars);
  }
  if (head + tail <= 0) {
    return value.slice(0, maxChars);
  }

  return `${value.slice(0, head).trimEnd()}${marker}${value.slice(-tail).trimStart()}`;
}

export function compressMessagesForRequestTooLargeRetry(
  messages: ModelMessage[],
): CompressMessagesForRequestTooLargeRetryResult {
  let didAdjust = false;
  const compressedMessages = messages.map((message) => {
    const compressed = compressModelMessageForRequestRetry(message);
    if (compressed !== message) didAdjust = true;
    return compressed;
  });

  return {
    messages: didAdjust ? compressedMessages : messages,
    didAdjust,
  };
}

function compressModelMessageForRequestRetry(message: ModelMessage): ModelMessage {
  if (typeof message.content === "string") {
    const content = compressAndTruncateText(message.content, RETRY_MAX_MESSAGE_TEXT_CHARS);
    return content === message.content ? message : { ...message, content };
  }

  if (!Array.isArray(message.content)) return message;

  let didAdjust = false;
  const content = message.content.map((part) => {
    const compressed = compressContentPartForRequestRetry(part);
    if (compressed !== part) didAdjust = true;
    return compressed;
  });

  return didAdjust ? { ...message, content } : message;
}

function compressContentPartForRequestRetry(part: unknown): unknown {
  if (!part || typeof part !== "object") return part;
  const record = part as Record<string, unknown>;
  const type = record.type;

  if (type === "text" && typeof record.text === "string") {
    const text = compressAndTruncateText(record.text, RETRY_MAX_MESSAGE_TEXT_CHARS);
    return text === record.text ? part : { ...record, text };
  }

  if (type === "tool-result") {
    const output = record.output;
    if (output && typeof output === "object") {
      const outputRecord = output as Record<string, unknown>;
      if (outputRecord.type === "text" && typeof outputRecord.value === "string") {
        const value = compressAndTruncateText(outputRecord.value, RETRY_MAX_TOOL_RESULT_CHARS);
        if (value === outputRecord.value) return part;
        return {
          ...record,
          output: {
            ...outputRecord,
            value,
          },
        };
      }
    }
  }

  if (type === "image" && typeof record.image === "string") {
    return omittedAttachmentTextPart("image", record.image, record);
  }

  if (type === "file" && typeof record.data === "string") {
    return omittedAttachmentTextPart("file", record.data, record);
  }

  return part;
}

function compressAndTruncateText(value: string, maxChars: number): string {
  return truncateTextWithHeadAndTail(compressVerboseText(value), maxChars);
}

function omittedAttachmentTextPart(
  label: "image" | "file",
  payload: string,
  record: Record<string, unknown>,
): { type: "text"; text: string } {
  const details = [
    typeof record.filename === "string" ? `filename=${record.filename}` : undefined,
    typeof record.mediaType === "string" ? `mediaType=${record.mediaType}` : undefined,
    `${payload.length} chars`,
  ].filter(Boolean).join(", ");

  return {
    type: "text",
    text: `[${label} attachment omitted to keep the AI request small: ${details}]`,
  };
}
