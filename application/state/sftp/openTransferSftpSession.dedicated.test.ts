import assert from "node:assert/strict";
import test from "node:test";

import {
  openTransferSftpSession,
  resetDedicatedSessionOpenGateForTests,
} from "./dedicatedTransferResume.ts";
import type { Host } from "../../../domain/models.ts";

const host: Host = {
  id: "h1",
  label: "box",
  hostname: "1.2.3.4",
  port: 22,
  username: "root",
  protocol: "ssh",
  authType: "password",
  password: "secret",
  created: 0,
  order: 0,
} as Host;

test("openTransferSftpSession defaults to dedicated vault open (ignores terminal session)", async () => {
  resetDedicatedSessionOpenGateForTests();
  let openSftpCalls = 0;
  let openForSessionCalls = 0;

  const original = (globalThis as { window?: unknown }).window;
  (globalThis as { window?: unknown }).window = {
    electron: {
      openSftp: async () => {
        openSftpCalls += 1;
        return "dedicated-sftp";
      },
      openSftpForSession: async () => {
        openForSessionCalls += 1;
        return "session-sftp";
      },
    },
  };

  try {
    // netcattyBridge reads window.electron — ensure adapter path works via mock.
    const { netcattyBridge } = await import("../../../infrastructure/services/netcattyBridge.ts");
    const restore = netcattyBridge.get;
    (netcattyBridge as { get: () => unknown }).get = () => ({
      openSftp: async () => {
        openSftpCalls += 1;
        return "dedicated-sftp";
      },
      openSftpForSession: async () => {
        openForSessionCalls += 1;
        return "session-sftp";
      },
    });

    try {
      const id = await openTransferSftpSession(
        host,
        { hosts: [host], keys: [], identities: [] },
        { sourceSessionId: "term-1", dedicated: true },
      );
      assert.equal(id, "dedicated-sftp");
      assert.equal(openSftpCalls, 1);
      assert.equal(openForSessionCalls, 0, "dedicated bulk path must not use terminal session channel");
    } finally {
      (netcattyBridge as { get: typeof restore }).get = restore;
    }
  } finally {
    if (original === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = original;
    resetDedicatedSessionOpenGateForTests();
  }
});

test("openTransferSftpSession can use terminal session only when dedicated:false", async () => {
  resetDedicatedSessionOpenGateForTests();
  let openForSessionCalls = 0;
  const { netcattyBridge } = await import("../../../infrastructure/services/netcattyBridge.ts");
  const restore = netcattyBridge.get;
  (netcattyBridge as { get: () => unknown }).get = () => ({
    openSftp: async () => "dedicated-sftp",
    openSftpForSession: async () => {
      openForSessionCalls += 1;
      return "session-sftp";
    },
  });
  try {
    const id = await openTransferSftpSession(
      host,
      { hosts: [host], keys: [], identities: [] },
      { sourceSessionId: "term-1", dedicated: false },
    );
    assert.equal(id, "session-sftp");
    assert.equal(openForSessionCalls, 1);
  } finally {
    (netcattyBridge as { get: typeof restore }).get = restore;
    resetDedicatedSessionOpenGateForTests();
  }
});
