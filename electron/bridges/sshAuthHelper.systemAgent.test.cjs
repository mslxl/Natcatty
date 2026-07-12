"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  getAvailableAgentSocket,
  isWindowsNamedPipe,
  ssh2AgentConnectable,
} = require("./sshAuthHelper.cjs");

test("Windows named pipe detection accepts both slash styles", () => {
  assert.equal(isWindowsNamedPipe("\\\\.\\pipe\\openssh-ssh-agent"), true);
  assert.equal(isWindowsNamedPipe("//./pipe/openssh-ssh-agent"), true);
  assert.equal(isWindowsNamedPipe("pageant"), false);
  assert.equal(isWindowsNamedPipe("C:\\cygwin\\agent.socket"), false);
});

test("Windows Pageant and Cygwin overrides use ssh2 agent validation", async () => {
  const checked = [];
  const injected = {
    platform: "win32",
    windowsPipeConnectable: async () => {
      throw new Error("named-pipe validation should not run");
    },
    ssh2AgentConnectable: async (agentPath) => {
      checked.push(agentPath);
      return true;
    },
  };

  assert.equal(await getAvailableAgentSocket("pageant", injected), "pageant");
  assert.equal(
    await getAvailableAgentSocket("C:\\cygwin\\agent.socket", injected),
    "C:\\cygwin\\agent.socket",
  );
  assert.deepEqual(checked, ["pageant", "C:\\cygwin\\agent.socket"]);
});

test("Windows named pipe overrides retain the lightweight pipe probe", async () => {
  const pipePath = "\\\\.\\pipe\\custom-agent";
  let checkedPath = null;
  const result = await getAvailableAgentSocket(pipePath, {
    platform: "win32",
    windowsPipeConnectable: async (value) => {
      checkedPath = value;
      return true;
    },
    ssh2AgentConnectable: async () => {
      throw new Error("ssh2 validation should not run for a named pipe");
    },
  });

  assert.equal(result, pipePath);
  assert.equal(checkedPath, pipePath);
});

test("ssh2 agent validation times out when an agent does not respond", async () => {
  const start = Date.now();
  const available = await ssh2AgentConnectable("pageant", {
    timeoutMs: 20,
    createAgentImpl: () => ({ getIdentities() {} }),
  });

  assert.equal(available, false);
  assert.ok(Date.now() - start < 500);
});
