import assert from "node:assert/strict";
import { createCipheriv, createHash } from "node:crypto";
import test from "node:test";

import {
  craftMobaConnectionKey,
  craftMobaSessionPKey,
  decodeMobaPlaintext,
  decryptMobaStoredSecret,
  decryptMobaWeakCipher,
  sha512,
} from "./mobaXtermCrypto.ts";

test("sha512 matches Node for MobaXterm key material", () => {
  const samples = ["12345678", "master-password", ""];
  for (const sample of samples) {
    assert.deepEqual(
      Buffer.from(sha512(Buffer.from(sample, "utf8"))),
      createHash("sha512").update(sample, "utf8").digest(),
    );
  }
});

test("master-password AES decrypts HyperSine session and credential vectors", () => {
  assert.equal(
    decryptMobaStoredSecret({
      ciphertext: "1du11XKQBOxud/FWh4ouWA==",
      masterPassword: "12345678",
    }),
    "Lw3+cZ2s.w@U@f]U",
  );
  assert.equal(
    decryptMobaStoredSecret({
      ciphertext: "0XROpGmLAYVx",
      masterPassword: "12345678",
    }),
    "HyperSine",
  );
});

test("master-password AES decrypts the v25 random-IV format", () => {
  const masterPassword = "netcatty-master";
  const plaintext = "imported-secret";
  const key = createHash("sha512").update(masterPassword, "utf8").digest().subarray(0, 32);
  const prefix = "_@ABCDEFGHIJKLMNOPQR";
  const iv = Buffer.from(prefix.slice(2, 18), "latin1");
  const cipher = createCipheriv("aes-256-cfb8", key, iv);
  cipher.setAutoPadding(false);
  const body = Buffer.concat([cipher.update(plaintext, "utf8"), cipher.final()]);
  const stored = `${prefix}${body.toString("base64").replaceAll("+", "@").replaceAll("/", "_")}`;

  assert.equal(
    decryptMobaStoredSecret({ ciphertext: stored, masterPassword }),
    plaintext,
  );
});

test("weak SessionP cipher decrypts the published credential vector", () => {
  const key = craftMobaSessionPKey("165821882556840");
  assert.equal(
    decodeMobaPlaintext(decryptMobaWeakCipher(
      "bSj4VWbHezNH3tTY9Nil2RzJX57p7/S6KqMw8VsiT/WH+I8p03pqnInAu",
      key,
    )),
    "HyperSine",
  );
});

test("weak connection cipher decrypts the published session-password vector", () => {
  const key = craftMobaConnectionKey("DoubleSine", "ShadowSurface", "root", "45.32.110.171");
  assert.equal(
    decodeMobaPlaintext(decryptMobaWeakCipher(
      "F0+wuBvbe9qPW6ypiOeYHTHhKdShRc/nXaM1Ky1jeTfw46TzQoSesX9buGm0WW36yP4lhH70ZCHZpEo4wLJhIl1",
      key,
    )),
    "Lw3+cZ2s.w@U@f]U",
  );
});

test("wrong master password does not look like a saved secret", () => {
  assert.equal(
    decryptMobaStoredSecret({
      ciphertext: "1du11XKQBOxud/FWh4ouWA==",
      masterPassword: "wrong-password",
    }),
    null,
  );
});
