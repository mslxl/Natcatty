const test = require("node:test");
const assert = require("node:assert/strict");
const { once } = require("node:events");

const {
  createProxySocket,
  substituteProxyCommand,
} = require("./proxyUtils.cjs");

test("substituteProxyCommand replaces OpenSSH-style host and port tokens for POSIX shells", () => {
  assert.equal(
    substituteProxyCommand(
      "cloudflared access ssh --hostname %h --port %p --literal %%",
      "server's.example.com",
      2222,
      { platform: "linux" },
    ),
    "cloudflared access ssh --hostname 'server'\\''s.example.com' --port '2222' --literal %",
  );
});

test("substituteProxyCommand quotes safe OpenSSH-style host and port tokens for Windows cmd.exe", () => {
  assert.equal(
    substituteProxyCommand(
      "cloudflared access ssh --hostname %h --port %p --literal %%",
      "server.example.com",
      2222,
      { platform: "win32" },
    ),
    'cloudflared access ssh --hostname "server.example.com" --port "2222" --literal %',
  );
});

test("substituteProxyCommand rejects unsafe Windows cmd.exe placeholder values", () => {
  assert.throws(
    () => substituteProxyCommand("proxy --host %h", 'server" & whoami & "', 22, { platform: "win32" }),
    /cannot be safely substituted/,
  );
  assert.throws(
    () => substituteProxyCommand("proxy --host %h", "%USERPROFILE%.example.com", 22, { platform: "win32" }),
    /cannot be safely substituted/,
  );
});

test("createProxySocket exposes ProxyCommand stdout as socket data", async () => {
  const command = `${JSON.stringify(process.execPath)} -e ${JSON.stringify("process.stdout.write('hello')")}`;
  const socket = await createProxySocket(
    { type: "command", host: "", port: 0, command },
    "server.example.com",
    22,
  );

  const timeout = new Promise((_, reject) => {
    setTimeout(() => reject(new Error("Timed out waiting for ProxyCommand output")), 1000).unref();
  });

  try {
    const data = await Promise.race([
      once(socket, "data").then(([chunk]) => chunk),
      timeout,
    ]);

    assert.equal(data.toString(), "hello");
  } finally {
    socket.destroy();
  }
});
