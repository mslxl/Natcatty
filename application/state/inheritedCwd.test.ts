import { test } from "node:test";
import assert from "node:assert/strict";
import { captureInheritedCwd } from "./inheritedCwd";

const neverProbe = async () => { throw new Error("should not probe"); };

test("prefers lastCwd when present", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected", lastCwd: "/a" },
    neverProbe,
  );
  assert.equal(cwd, "/a");
});

test("ssh connected with empty lastCwd probes getSessionPwd", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected" },
    async () => ({ success: true, cwd: "/probed" }),
  );
  assert.equal(cwd, "/probed");
});

test("ssh probe failure -> undefined", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected" },
    async () => ({ success: false }),
  );
  assert.equal(cwd, undefined);
});

test("local with empty lastCwd falls back to localStartDir (no probe)", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "local", status: "connected", localStartDir: "/home/u" },
    neverProbe,
  );
  assert.equal(cwd, "/home/u");
});

test("disconnected ssh does not probe", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "disconnected" },
    neverProbe,
  );
  assert.equal(cwd, undefined);
});
