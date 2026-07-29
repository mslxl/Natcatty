import { test } from "node:test";
import assert from "node:assert/strict";
import { copySessionWithCurrentShellImpl, splitSessionWithCurrentShellImpl } from "./AppHandlers";

function ctxFactory(overrides: Record<string, any>) {
  const calls: any = {};
  const base = {
    classifyLocalShellType: () => "posix",
    discoveredShells: [],
    resolveShellSetting: () => ({ command: "/bin/bash", args: [] }),
    terminalSettings: { localShell: "bash" },
    sessions: [{ id: "src", protocol: "ssh", status: "connected", lastCwd: "/var/log" }],
    netcattyBridge: { get: () => ({ getSessionPwd: async () => ({ success: false }) }) },
    copySession: (id: string, opts: any) => { calls.copy = { id, opts }; },
    splitSession: (id: string, dir: any, opts: any) => { calls.split = { id, dir, opts }; },
    ...overrides,
  };
  return { getCtx: () => base, calls };
}

test("copySessionWithCurrentShell passes inheritedCwd from lastCwd", async () => {
  const { getCtx, calls } = ctxFactory({});
  await copySessionWithCurrentShellImpl(getCtx, "src");
  assert.equal(calls.copy.opts.inheritedCwd, "/var/log");
});

test("splitSessionWithCurrentShell passes inheritedCwd from lastCwd", async () => {
  const { getCtx, calls } = ctxFactory({});
  await splitSessionWithCurrentShellImpl(getCtx, "src", "horizontal");
  assert.equal(calls.split.opts.inheritedCwd, "/var/log");
});
