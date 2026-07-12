"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");

const {
  getAvailableAgentSocket,
  getNativeOpenSshAgentSocket,
  isWindowsNamedPipe,
  socketAgentConnectable,
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

test("Unix agent availability requires a working agent protocol response", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-agent-socket-"));
  const socketPath = path.join(dir, "agent.sock");
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(socketPath, resolve);
  });
  t.after(() => {
    server.close();
    fs.rmSync(dir, { recursive: true, force: true });
  });

  assert.equal(await getAvailableAgentSocket(socketPath, {
    platform: "linux",
    socketAgentConnectable: async () => false,
  }), null);
  assert.equal(await getAvailableAgentSocket(socketPath, {
    platform: "linux",
    socketAgentConnectable: async () => true,
  }), socketPath);
});

test("Unix agent protocol probe closes a connection that never responds", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-agent-hung-"));
  const socketPath = path.join(dir, "agent.sock");
  const server = net.createServer();
  let acceptedSocket = null;
  let probeSocket = null;
  let acceptedSocketClosed;
  const closed = new Promise((resolve) => { acceptedSocketClosed = resolve; });
  server.on("connection", (socket) => {
    acceptedSocket = socket;
    socket.resume();
    socket.once("close", acceptedSocketClosed);
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(socketPath, resolve);
  });
  t.after(() => {
    server.close();
    fs.rmSync(dir, { recursive: true, force: true });
  });

  assert.equal(await socketAgentConnectable(socketPath, {
    timeoutMs: 20,
    createConnectionImpl: (value) => {
      probeSocket = net.createConnection(value);
      return probeSocket;
    },
  }), false);
  assert.equal(probeSocket?.destroyed, true);
  await Promise.race([
    closed,
    new Promise((_, reject) => setTimeout(() => reject(new Error("agent probe connection stayed open")), 500)),
  ]);
  assert.equal(acceptedSocket?.destroyed, true);
});

test("native OpenSSH modes reject Pageant and Cygwin-only adapters clearly", async () => {
  await assert.rejects(
    getNativeOpenSshAgentSocket("pageant", {
      platform: "win32",
      ssh2AgentConnectable: async () => true,
    }),
    (error) => {
      assert.equal(error.code, "ERR_SSH_AGENT_NATIVE_UNSUPPORTED");
      assert.match(error.message, /named-pipe agent/);
      return true;
    },
  );
});
