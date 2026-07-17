import assert from "node:assert/strict";
import test from "node:test";

import {
  PLUGIN_STREAM_MAX_CHUNK_BYTES,
  createBase64StreamChunk,
  createJsonStreamChunk,
  createMessagePortStreamEnvelope,
  materializeStreamChunk,
} from "./streamTransport.ts";

test("JSON stream chunks use verified UTF-8 byte accounting", () => {
  const chunk = createJsonStreamChunk({ text: "你好" });
  assert.equal(chunk.encoding, "json");
  assert.equal(chunk.byteLength, new TextEncoder().encode('{"text":"你好"}').byteLength);
  assert.deepEqual(materializeStreamChunk(chunk), {
    encoding: "json",
    value: { text: "你好" },
  });
  assert.throws(
    () => materializeStreamChunk({ ...chunk, byteLength: chunk.byteLength + 1 }),
    /JSON byteLength mismatch/,
  );
  assert.throws(
    () => createJsonStreamChunk({ value: Number.NaN } as never),
    /JSON numbers must be finite/,
  );
  assert.throws(
    () => createJsonStreamChunk({ value: undefined } as never),
    /Unsupported JSON value type/,
  );
  const sparse = new Array(2) as never;
  assert.throws(() => createJsonStreamChunk(sparse), /JSON arrays must be dense/);
  const accessor = {} as Record<string, unknown>;
  Object.defineProperty(accessor, "value", { enumerable: true, get: () => "unsafe" });
  assert.throws(
    () => createJsonStreamChunk(accessor as never),
    /must not contain accessor properties/,
  );
});

test("base64 stream chunks round-trip bytes and reject length or encoding ambiguity", () => {
  const bytes = new Uint8Array([0, 1, 2, 127, 128, 253, 254, 255]);
  const chunk = createBase64StreamChunk(bytes);
  const materialized = materializeStreamChunk(chunk);
  assert.equal(materialized.encoding, "binary");
  assert.deepEqual(materialized.bytes, bytes);

  assert.throws(
    () => materializeStreamChunk({ ...chunk, byteLength: bytes.byteLength + 1 }),
    /byteLength mismatch/,
  );
  assert.throws(
    () => materializeStreamChunk({ encoding: "base64", value: "not-base64", byteLength: 1 }),
    /canonical RFC 4648 base64/,
  );
  assert.throws(
    () => materializeStreamChunk({ encoding: "base64", value: "AB==", byteLength: 1 }),
    /canonical RFC 4648 base64/,
  );
  assert.throws(
    () => materializeStreamChunk({
      encoding: "base64",
      value: "",
      byteLength: PLUGIN_STREAM_MAX_CHUNK_BYTES + 1,
    }),
    /byteLength must be an integer between/,
  );

  for (let length = 0; length <= 257; length += 1) {
    const sample = Uint8Array.from(
      { length },
      (_, index) => (length * 17 + index * 31) & 0xff,
    );
    const roundTrip = materializeStreamChunk(createBase64StreamChunk(sample));
    assert.equal(roundTrip.encoding, "binary");
    assert.deepEqual(roundTrip.bytes, sample, `base64 length ${length}`);
  }
});

test("MessagePort stream envelopes carry and validate the transferred ArrayBuffer", () => {
  const transfer = new Uint8Array([1, 2, 3, 4]).buffer;
  const frame = {
    streamId: "stream-1",
    sequence: 1,
    kind: "chunk" as const,
    data: { encoding: "transfer" as const, byteLength: 4 },
  };
  const envelope = createMessagePortStreamEnvelope(frame, transfer);
  assert.equal(envelope.transfer, transfer);
  const materialized = materializeStreamChunk(frame.data, envelope.transfer);
  assert.equal(materialized.encoding, "binary");
  assert.deepEqual(materialized.bytes, new Uint8Array([1, 2, 3, 4]));

  assert.throws(
    () => createMessagePortStreamEnvelope(frame),
    /require an ArrayBuffer/,
  );
  assert.throws(
    () => createMessagePortStreamEnvelope(frame, { byteLength: 4 } as never),
    /require a real ArrayBuffer/,
  );
  assert.throws(
    () => createMessagePortStreamEnvelope(
      { streamId: "stream-1", sequence: 0, kind: "open", windowBytes: 65_536 },
      transfer,
    ),
    /Only transfer-encoded chunk frames/,
  );
});
