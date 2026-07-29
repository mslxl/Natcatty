import { test } from "node:test";
import assert from "node:assert/strict";
import { captureInheritedCwd } from "./inheritedCwd";

const neverProbe = async () => { throw new Error("should not probe"); };

test("connected ssh probes live cwd first, ignoring stale lastCwd", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected", lastCwd: "/stale" },
    async () => ({ success: true, cwd: "/live" }),
  );
  assert.equal(cwd, "/live");
});

test("connected ssh falls back to lastCwd when probe reports failure", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected", lastCwd: "/a" },
    async () => ({ success: false }),
  );
  assert.equal(cwd, "/a");
});

test("connected ssh falls back to lastCwd when probe throws", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected", lastCwd: "/a" },
    async () => { throw new Error("boom"); },
  );
  assert.equal(cwd, "/a");
});

test("connected ssh with no lastCwd and failed probe -> undefined", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "connected" },
    async () => ({ success: false }),
  );
  assert.equal(cwd, undefined);
});

test("disconnected ssh uses lastCwd without probing", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "ssh", status: "disconnected", lastCwd: "/a" },
    neverProbe,
  );
  assert.equal(cwd, "/a");
});

test("local uses lastCwd without probing", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "local", status: "connected", lastCwd: "/a", localStartDir: "/home/u" },
    neverProbe,
  );
  assert.equal(cwd, "/a");
});

test("local with empty lastCwd falls back to localStartDir (no probe)", async () => {
  const cwd = await captureInheritedCwd(
    { id: "s", protocol: "local", status: "connected", localStartDir: "/home/u" },
    neverProbe,
  );
  assert.equal(cwd, "/home/u");
});
