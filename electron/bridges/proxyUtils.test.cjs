const test = require("node:test");
const assert = require("node:assert/strict");
const { once } = require("node:events");
const { mkdtempSync, writeFileSync, rmSync } = require("node:fs");
const { tmpdir } = require("node:os");
const { join } = require("node:path");

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

test("substituteProxyCommand replaces OpenSSH-style host and port tokens for Windows cmd.exe", () => {
  assert.equal(
    substituteProxyCommand(
      "cloudflared access ssh --hostname %h --port %p --literal %%",
      'server "quoted" %USERPROFILE%.example.com',
      2222,
      { platform: "win32" },
    ),
    'cloudflared access ssh --hostname "server \\"quoted\\" ^%USERPROFILE^%.example.com" --port "2222" --literal %',
  );
});

test("createProxySocket exposes ProxyCommand stdin and stdout as a duplex stream", async () => {
  const tempDir = mkdtempSync(join(tmpdir(), "netcatty-proxy-command-"));
  const echoScript = join(tempDir, "echo-proxy.cjs");
  writeFileSync(
    echoScript,
    "process.stdin.on('data', (chunk) => process.stdout.write(chunk));\nprocess.stdin.resume();\nsetTimeout(() => process.exit(0), 250);\n",
  );

  const command = `${JSON.stringify(process.execPath)} ${JSON.stringify(echoScript)}`;
  const socket = await createProxySocket(
    { type: "command", host: "", port: 0, command },
    "server.example.com",
    22,
  );

  const timeout = new Promise((_, reject) => {
    setTimeout(() => reject(new Error("Timed out waiting for ProxyCommand echo")), 1000).unref();
  });

  try {
    const dataPromise = Promise.race([
      once(socket, "data").then(([data]) => data),
      once(socket, "error").then(([error]) => { throw error; }),
      once(socket, "close").then(() => { throw new Error("ProxyCommand socket closed before echoing data"); }),
      timeout,
    ]);

    socket.write(Buffer.from("hello"));
    const data = await dataPromise;

    assert.equal(data.toString(), "hello");
  } finally {
    socket.destroy();
    rmSync(tempDir, { recursive: true, force: true });
  }
});
