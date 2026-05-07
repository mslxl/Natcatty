const test = require("node:test");
const assert = require("node:assert/strict");
const net = require("node:net");

const terminalBridge = require("./terminalBridge.cjs");

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve(server.address().port);
    });
  });
}

function waitFor(predicate, timeoutMs = 1000) {
  const startedAt = Date.now();
  return new Promise((resolve, reject) => {
    const tick = () => {
      if (predicate()) {
        resolve();
        return;
      }
      if (Date.now() - startedAt > timeoutMs) {
        reject(new Error("Timed out waiting for telnet auto-login"));
        return;
      }
      setTimeout(tick, 10);
    };
    tick();
  });
}

test("startTelnetSession answers login prompts with saved credentials", async () => {
  const received = [];
  const server = net.createServer((socket) => {
    socket.setEncoding("utf8");
    let promptedForUsername = false;
    socket.write("Device bannerPress RETURN to get started.");
    socket.on("data", (chunk) => {
      received.push(chunk);
      const joined = received.join("");
      if (!promptedForUsername && joined.includes("\r")) {
        promptedForUsername = true;
        socket.write("Username: ");
      }
      if (joined.includes("admin\r") && !joined.includes("secret\r")) {
        socket.write("\r\nPassword: ");
      }
      if (joined.includes("secret\r")) {
        socket.end("\r\nWelcome\r\n");
      }
    });
  });

  const port = await listen(server);
  const sessions = new Map();
  const sentEvents = [];
  terminalBridge.init({
    sessions,
    electronModule: {
      webContents: {
        fromId: () => ({
          send(channel, payload) {
            sentEvents.push({ channel, payload });
          },
        }),
      },
    },
  });

  try {
    const result = await terminalBridge.startTelnetSession(
      { sender: { id: 1 } },
      {
        sessionId: "telnet-auto-login-test",
        hostname: "127.0.0.1",
        port,
        username: "admin",
        password: "secret",
      },
    );

    assert.equal(result.sessionId, "telnet-auto-login-test");
    await waitFor(() => received.join("").includes("\radmin\rsecret\r"));
    assert.equal(received.join(""), "\radmin\rsecret\r");
  } finally {
    terminalBridge.cleanupAllSessions();
    await new Promise((resolve) => server.close(resolve));
  }
});
