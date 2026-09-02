import test from "node:test";
import assert from "node:assert/strict";
import type { Host } from "./models";
import {
  canElevateSftpForTerminalDrop,
  hostHasUsableSftpSudoPassword,
  normalizePosixAbsolutePath,
  posixPathNeedsLoginUserElevation,
  resolveTerminalDropSftpHost,
  TerminalDropNeedsSudoError,
} from "./sftpDropElevation";

const host = {
  id: "host-1",
  label: "Host",
  hostname: "example.com",
  port: 22,
  username: "alice",
  protocol: "ssh",
} as Host;

const encryptedPassword = (() => {
  const blob = Buffer.alloc(19, 0);
  Buffer.from("v10", "utf8").copy(blob, 0);
  return `enc:v1:${blob.toString("base64")}`;
})();

test("normalizePosixAbsolutePath collapses slashes and trailing separators", () => {
  assert.equal(normalizePosixAbsolutePath("/root/"), "/root");
  assert.equal(normalizePosixAbsolutePath("/root//bin/"), "/root/bin");
  assert.equal(normalizePosixAbsolutePath("/"), "/");
  assert.equal(normalizePosixAbsolutePath("root"), null);
  assert.equal(normalizePosixAbsolutePath("  "), null);
});

test("posixPathNeedsLoginUserElevation only flags /root for non-root logins", () => {
  assert.equal(posixPathNeedsLoginUserElevation("/root", "alice"), true);
  assert.equal(posixPathNeedsLoginUserElevation("/root/bin", "alice"), true);
  assert.equal(posixPathNeedsLoginUserElevation("/root/", "alice"), true);
  assert.equal(posixPathNeedsLoginUserElevation("/home/alice", "alice"), false);
  assert.equal(posixPathNeedsLoginUserElevation("/tmp", "alice"), false);
  assert.equal(posixPathNeedsLoginUserElevation("/root", "root"), false);
  assert.equal(posixPathNeedsLoginUserElevation("/root", "  "), false);
  assert.equal(posixPathNeedsLoginUserElevation("/root", undefined), false);
});

test("hostHasUsableSftpSudoPassword ignores missing and encrypted placeholders", () => {
  assert.equal(hostHasUsableSftpSudoPassword({ password: "secret" }), true);
  assert.equal(hostHasUsableSftpSudoPassword({ password: "" }), false);
  assert.equal(hostHasUsableSftpSudoPassword({}), false);
  assert.equal(hostHasUsableSftpSudoPassword({ password: encryptedPassword }), false);
});

test("canElevateSftpForTerminalDrop rejects SCP and missing passwords", () => {
  assert.equal(canElevateSftpForTerminalDrop({ sftpSudo: true }), true);
  assert.equal(canElevateSftpForTerminalDrop({ password: "secret" }), true);
  assert.equal(canElevateSftpForTerminalDrop({ sftpFileProtocol: "scp", password: "secret" }), false);
  assert.equal(canElevateSftpForTerminalDrop({}), false);
});

test("resolveTerminalDropSftpHost clones sudo only for unelevated /root drops", () => {
  const withPassword = { ...host, password: "secret" };
  const elevated = resolveTerminalDropSftpHost(withPassword, "/root");
  assert.equal(elevated.sftpSudo, true);
  assert.notEqual(elevated, withPassword);

  const alreadySudo = { ...host, sftpSudo: true };
  assert.equal(resolveTerminalDropSftpHost(alreadySudo, "/root"), alreadySudo);

  const userHome = { ...host, password: "secret" };
  assert.equal(resolveTerminalDropSftpHost(userHome, "/home/alice"), userHome);
});

test("resolveTerminalDropSftpHost asks the user to enable sudo when no password is saved", () => {
  assert.throws(
    () => resolveTerminalDropSftpHost(host, "/root"),
    TerminalDropNeedsSudoError,
  );
  assert.throws(
    () => resolveTerminalDropSftpHost({ ...host, sftpFileProtocol: "scp", password: "secret" }, "/root"),
    TerminalDropNeedsSudoError,
  );
});
