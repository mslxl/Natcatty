import type {
  JsonValue,
  StreamChunkData,
  StreamFrame,
} from "./generated/plugin-contract.js";
import { PLUGIN_WIRE_MAX_SAFE_INTEGER } from "./generated/plugin-contract-limits.js";
import { serializeJsonValue } from "./jsonValue.js";

const BASE64_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const BASE64_VALUE = new Map(
  [...BASE64_ALPHABET].map((character, index) => [character, index] as const),
);

export interface MessagePortStreamEnvelope {
  readonly frame: StreamFrame;
  readonly transfer?: ArrayBuffer;
}

export const PLUGIN_STREAM_MAX_CHUNK_BYTES = 16 * 1024 * 1024;
const PLUGIN_STREAM_MAX_BASE64_CHARACTERS = 4 * Math.ceil(PLUGIN_STREAM_MAX_CHUNK_BYTES / 3);

export type MaterializedStreamChunk =
  | { readonly encoding: "json"; readonly value: JsonValue }
  | { readonly encoding: "binary"; readonly bytes: Uint8Array };

const jsonEncoder = new TextEncoder();
const arrayBufferByteLength = Object.getOwnPropertyDescriptor(
  ArrayBuffer.prototype,
  "byteLength",
)?.get;

function materializeArrayBuffer(value: unknown): Uint8Array {
  if (!arrayBufferByteLength) {
    throw new TypeError("ArrayBuffer byteLength getter is unavailable");
  }
  try {
    arrayBufferByteLength.call(value);
    return new Uint8Array(value as ArrayBuffer);
  } catch {
    throw new TypeError("Transfer stream chunks require a real, attached ArrayBuffer");
  }
}

function serializedJsonByteLength(value: JsonValue): number {
  const serialized = serializeJsonValue(value);
  return jsonEncoder.encode(serialized).byteLength;
}

function assertChunkByteLength(byteLength: number): void {
  if (!Number.isInteger(byteLength)
    || byteLength < 0
    || byteLength > PLUGIN_STREAM_MAX_CHUNK_BYTES) {
    throw new RangeError(
      `Stream chunk byteLength must be an integer between 0 and ${PLUGIN_STREAM_MAX_CHUNK_BYTES}`,
    );
  }
}

function assertStreamSequence(frame: StreamFrame): void {
  const minimum = frame.kind === "open" || frame.kind === "windowUpdate" ? 0 : 1;
  if (!Number.isSafeInteger(frame.sequence)
    || frame.sequence < minimum
    || frame.sequence > PLUGIN_WIRE_MAX_SAFE_INTEGER
    || (frame.kind === "open" && frame.sequence !== 0)) {
    const expected = frame.kind === "open"
      ? "exactly 0"
      : `a safe integer between ${minimum} and ${PLUGIN_WIRE_MAX_SAFE_INTEGER}`;
    throw new RangeError(`Stream ${frame.kind} sequence must be ${expected}`);
  }
}

function encodeBase64(bytes: Uint8Array): string {
  let output = "";
  for (let offset = 0; offset < bytes.byteLength; offset += 3) {
    const first = bytes[offset];
    const hasSecond = offset + 1 < bytes.byteLength;
    const hasThird = offset + 2 < bytes.byteLength;
    const second = hasSecond ? bytes[offset + 1] : 0;
    const third = hasThird ? bytes[offset + 2] : 0;
    output += BASE64_ALPHABET[first >> 2];
    output += BASE64_ALPHABET[((first & 0x03) << 4) | (second >> 4)];
    output += hasSecond
      ? BASE64_ALPHABET[((second & 0x0f) << 2) | (third >> 6)]
      : "=";
    output += hasThird ? BASE64_ALPHABET[third & 0x3f] : "=";
  }
  return output;
}

function decodeBase64(value: string): Uint8Array {
  if (value.length > PLUGIN_STREAM_MAX_BASE64_CHARACTERS) {
    throw new RangeError(
      `Stream base64 data exceeds ${PLUGIN_STREAM_MAX_BASE64_CHARACTERS} characters`,
    );
  }
  if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)) {
    throw new Error("Stream base64 data is not canonical RFC 4648 base64");
  }
  if (value.length === 0) return new Uint8Array(0);
  const padding = value.endsWith("==") ? 2 : value.endsWith("=") ? 1 : 0;
  const output = new Uint8Array((value.length / 4) * 3 - padding);
  let outputOffset = 0;
  for (let offset = 0; offset < value.length; offset += 4) {
    const first = BASE64_VALUE.get(value[offset]) ?? 0;
    const second = BASE64_VALUE.get(value[offset + 1]) ?? 0;
    const third = BASE64_VALUE.get(value[offset + 2]) ?? 0;
    const fourth = BASE64_VALUE.get(value[offset + 3]) ?? 0;
    if (outputOffset < output.byteLength) output[outputOffset++] = (first << 2) | (second >> 4);
    if (outputOffset < output.byteLength) output[outputOffset++] = (second << 4) | (third >> 2);
    if (outputOffset < output.byteLength) output[outputOffset++] = (third << 6) | fourth;
  }
  if (encodeBase64(output) !== value) {
    throw new Error("Stream base64 data is not canonical RFC 4648 base64");
  }
  return output;
}

export function createBase64StreamChunk(bytes: Uint8Array): StreamChunkData {
  assertChunkByteLength(bytes.byteLength);
  return {
    encoding: "base64",
    value: encodeBase64(bytes),
    byteLength: bytes.byteLength,
  };
}

export function createJsonStreamChunk(value: JsonValue): StreamChunkData {
  const byteLength = serializedJsonByteLength(value);
  assertChunkByteLength(byteLength);
  return {
    encoding: "json",
    value,
    byteLength,
  };
}

export function materializeStreamChunk(
  data: StreamChunkData,
  transfer?: ArrayBuffer,
): MaterializedStreamChunk {
  assertChunkByteLength(data.byteLength);
  if (data.encoding === "json") {
    if (transfer !== undefined) {
      throw new Error("JSON stream chunks must not include a transferable buffer");
    }
    const byteLength = serializedJsonByteLength(data.value);
    if (byteLength !== data.byteLength) {
      throw new Error(
        `Stream JSON byteLength mismatch: declared ${data.byteLength}, encoded ${byteLength}`,
      );
    }
    return { encoding: "json", value: data.value };
  }
  if (data.encoding === "base64") {
    if (transfer !== undefined) {
      throw new Error("Base64 stream chunks must not include a transferable buffer");
    }
    const bytes = decodeBase64(data.value);
    if (bytes.byteLength !== data.byteLength) {
      throw new Error(
        `Stream base64 byteLength mismatch: declared ${data.byteLength}, decoded ${bytes.byteLength}`,
      );
    }
    return { encoding: "binary", bytes };
  }
  if ((data as { readonly encoding: unknown }).encoding !== "transfer") {
    throw new Error("Unsupported stream chunk encoding");
  }
  if (transfer === undefined) {
    throw new Error("Transfer stream chunks require an ArrayBuffer in the message envelope");
  }
  const bytes = materializeArrayBuffer(transfer);
  if (bytes.byteLength !== data.byteLength) {
    throw new Error(
      `Stream transfer byteLength mismatch: declared ${data.byteLength}, received ${bytes.byteLength}`,
    );
  }
  return { encoding: "binary", bytes };
}

export function createMessagePortStreamEnvelope(
  frame: StreamFrame,
  transfer?: ArrayBuffer,
): MessagePortStreamEnvelope {
  assertStreamSequence(frame);
  if (frame.kind === "chunk") {
    materializeStreamChunk(frame.data, transfer);
  } else if (transfer !== undefined) {
    throw new Error("Only transfer-encoded chunk frames may include an ArrayBuffer");
  }
  return transfer === undefined ? { frame } : { frame, transfer };
}
