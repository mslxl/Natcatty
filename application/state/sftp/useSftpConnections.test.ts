// Created: 2026-07-21
// Purpose: Verify SFTP prefers live terminal session reuse before fresh auth.

import test from "node:test";
import assert from "node:assert/strict";

import { openSftpWithSessionPreference } from "./useSftpConnections.ts";

const openOptions = {
  sessionId: "sftp-request-1",
  hostname: "192.168.9.138",
  username: "zhlrs",
  port: 22,
} as NetcattySSHOptions;

test("openSftpWithSessionPreference opens session-backed SFTP before authing again", async () => {
  const calls: string[] = [];
  const sftpId = await openSftpWithSessionPreference({
    bridge: {
      openSftpForSession: async (sessionId: string) => {
        calls.push(`openForSession:${sessionId}`);
        return "session-backed-sftp";
      },
      openSftp: async () => {
        calls.push("openSftp");
        return "fresh-sftp";
      },
    },
    sourceSessionId: "ssh-session-1",
    openOptions,
  });

  assert.equal(sftpId, "session-backed-sftp");
  assert.deepEqual(calls, ["openForSession:ssh-session-1"]);
});

test("openSftpWithSessionPreference falls back to normal SFTP when session reuse fails", async () => {
  const calls: string[] = [];
  const sftpId = await openSftpWithSessionPreference({
    bridge: {
      openSftpForSession: async (sessionId: string) => {
        calls.push(`openForSession:${sessionId}`);
        throw new Error("channel unavailable");
      },
      openSftp: async (options: NetcattySSHOptions) => {
        calls.push(`openSftp:${options.sessionId}`);
        return "fresh-sftp";
      },
    },
    sourceSessionId: "ssh-session-1",
    openOptions,
  });

  assert.equal(sftpId, "fresh-sftp");
  assert.deepEqual(calls, ["openForSession:ssh-session-1", "openSftp:sftp-request-1"]);
});

test("openSftpWithSessionPreference opens normal SFTP without a source session", async () => {
  const calls: string[] = [];
  const sftpId = await openSftpWithSessionPreference({
    bridge: {
      openSftpForSession: async () => {
        calls.push("openForSession");
        return "session-backed-sftp";
      },
      openSftp: async (options: NetcattySSHOptions) => {
        calls.push(`openSftp:${options.sessionId}`);
        return "fresh-sftp";
      },
    },
    sourceSessionId: undefined,
    openOptions,
  });

  assert.equal(sftpId, "fresh-sftp");
  assert.deepEqual(calls, ["openSftp:sftp-request-1"]);
});
