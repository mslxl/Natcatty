import assert from "node:assert/strict";
import test from "node:test";

import {
  COMPANION_STDIO_MAX_CONTENT_BYTES,
  ContentLengthFrameDecoder,
  encodeContentLengthFrame,
} from "./stdioFraming.ts";

test("content-length framing round-trips fragmented and coalesced JSON messages", () => {
  const first = encodeContentLengthFrame({ jsonrpc: "2.0", id: 1, method: "plugin.initialize" });
  const second = encodeContentLengthFrame({ jsonrpc: "2.0", id: 1, result: { ok: true } });
  const joined = new Uint8Array(first.byteLength + second.byteLength);
  joined.set(first);
  joined.set(second, first.byteLength);

  const decoder = new ContentLengthFrameDecoder();
  assert.deepEqual(decoder.push(joined.subarray(0, 7)), []);
  assert.deepEqual(decoder.push(joined.subarray(7, first.byteLength + 3)), [
    { jsonrpc: "2.0", id: 1, method: "plugin.initialize" },
  ]);
  assert.deepEqual(decoder.push(joined.subarray(first.byteLength + 3)), [
    { jsonrpc: "2.0", id: 1, result: { ok: true } },
  ]);
  assert.doesNotThrow(() => decoder.finish());

  for (let split = 0; split <= first.byteLength; split += 1) {
    const splitDecoder = new ContentLengthFrameDecoder();
    const message = { jsonrpc: "2.0", id: 1, method: "plugin.initialize" };
    assert.deepEqual(
      splitDecoder.push(first.subarray(0, split)),
      split === first.byteLength ? [message] : [],
    );
    assert.deepEqual(
      splitDecoder.push(first.subarray(split)),
      split === first.byteLength ? [] : [message],
    );
    assert.doesNotThrow(() => splitDecoder.finish());
  }
});

test("content-length framing rejects ambiguous headers and oversized payloads", () => {
  assert.throws(
    () => encodeContentLengthFrame(undefined as never),
    /Unsupported JSON value type/,
  );
  assert.throws(
    () => encodeContentLengthFrame({ value: Number.NaN } as never),
    /JSON numbers must be finite/,
  );
  assert.throws(
    () => encodeContentLengthFrame({
      streamId: "stream-1",
      sequence: 1,
      kind: "chunk",
      data: { encoding: "transfer", byteLength: 4 },
    }),
    /cannot be encoded over companion stdio/,
  );
  const duplicate = new ContentLengthFrameDecoder();
  assert.throws(
    () => duplicate.push("Content-Length: 2\r\ncontent-length: 2\r\n\r\n{}"),
    /Duplicate companion stdio header/,
  );

  const whitespaceBeforeColon = new ContentLengthFrameDecoder();
  assert.throws(
    () => whitespaceBeforeColon.push("Content-Length : 2\r\n\r\n{}"),
    /Malformed companion stdio header/,
  );

  const unsupported = new ContentLengthFrameDecoder();
  assert.throws(
    () => unsupported.push("Content-Length: 2\r\nX-Mode: unsafe\r\n\r\n{}"),
    /Unsupported companion stdio header/,
  );

  const oversized = new ContentLengthFrameDecoder({ maxContentBytes: 4 });
  assert.throws(
    () => oversized.push("Content-Length: 5\r\n\r\n12345"),
    /exceeds 4 bytes/,
  );

  const truncated = new ContentLengthFrameDecoder();
  assert.deepEqual(truncated.push("Content-Length: 5\r\n\r\n12"), []);
  assert.throws(() => truncated.finish(), /truncated frame/);
  assert.throws(
    () => new ContentLengthFrameDecoder({
      maxContentBytes: COMPANION_STDIO_MAX_CONTENT_BYTES + 1,
    }),
    /must be an integer between/,
  );

  for (const payload of ["1e999", '{"value":1e999}']) {
    const nonFinite = new ContentLengthFrameDecoder();
    const payloadBytes = new TextEncoder().encode(payload).byteLength;
    assert.throws(
      () => nonFinite.push(`Content-Length: ${payloadBytes}\r\n\r\n${payload}`),
      /outside the JSON value contract: JSON numbers must be finite/,
    );
  }
});

test("content-length framing accepts a split delimiter at the header byte limit", () => {
  const maxHeaderBytes = 32;
  const header = `Content-Length:${" ".repeat(16)}2`;
  assert.equal(new TextEncoder().encode(header).byteLength, maxHeaderBytes);

  const separator = "\r\n\r\n";
  for (let split = 0; split <= separator.length; split += 1) {
    const decoder = new ContentLengthFrameDecoder({ maxHeaderBytes });
    assert.deepEqual(decoder.push(`${header}${separator.slice(0, split)}`), []);
    assert.deepEqual(decoder.push(`${separator.slice(split)}{}`), [{}]);
    assert.doesNotThrow(() => decoder.finish());
  }

  const oversized = new ContentLengthFrameDecoder({ maxHeaderBytes });
  assert.throws(
    () => oversized.push(`${header} \r\n\r\n{}`),
    /header exceeds 32 bytes/,
  );
});
