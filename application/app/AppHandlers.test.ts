import { test } from "node:test";
import assert from "node:assert/strict";
import { copySessionWithCurrentShellImpl, splitSessionWithCurrentShellImpl } from "./AppHandlers";

type CloneOpts = { localShellType?: string; inheritedCwd?: string };
type Calls = {
  copy?: { id: string; opts: CloneOpts };
  split?: { id: string; dir: string; opts: CloneOpts };
};

function ctxFactory(overrides: Record<string, unknown>) {
  const calls: Calls = {};
  const base = {
    classifyLocalShellType: () => "posix",
    discoveredShells: [],
    resolveShellSetting: () => ({ command: "/bin/bash", args: [] }),
    terminalSettings: { localShell: "bash" },
    sessions: [{ id: "src", protocol: "ssh", status: "connected", lastCwd: "/var/log" }],
    netcattyBridge: { get: () => ({ getSessionPwd: async () => ({ success: false }) }) },
    copySession: (id: string, opts: CloneOpts) => { calls.copy = { id, opts }; },
    splitSession: (id: string, dir: string, opts: CloneOpts) => { calls.split = { id, dir, opts }; },
    ...overrides,
  };
  return { getCtx: () => base, calls };
}

test("copySessionWithCurrentShell passes inheritedCwd from lastCwd", async () => {
  const { getCtx, calls } = ctxFactory({});
  await copySessionWithCurrentShellImpl(getCtx, "src");
  assert.equal(calls.copy?.opts.inheritedCwd, "/var/log");
});

test("splitSessionWithCurrentShell passes inheritedCwd from lastCwd", async () => {
  const { getCtx, calls } = ctxFactory({});
  await splitSessionWithCurrentShellImpl(getCtx, "src", "horizontal");
  assert.equal(calls.split?.opts.inheritedCwd, "/var/log");
});
