const test = require("node:test");
const assert = require("node:assert/strict");
const { once } = require("node:events");

const {
  createProxySocket,
  substituteProxyCommand,
} = require("./proxyUtils.cjs");

test("substituteProxyCommand replaces OpenSSH-style host and port tokens", () => {
  assert.equal(
    substituteProxyCommand(
      "cloudflared access ssh --hostname %h --port %p --literal %%",
      "server.example.com",
      2222,
    ),
    "cloudflared access ssh --hostname 'server.example.com' --port '2222' --literal %",
  );
});

test("createProxySocket exposes ProxyCommand stdin and stdout as a duplex stream", async () => {
  const command = `"${process.execPath}" -e "process.stdin.pipe(process.stdout)"`;
  const socket = await createProxySocket(
    { type: "command", host: "", port: 0, command },
    "server.example.com",
    22,
  );

  const dataPromise = once(socket, "data");
  socket.write(Buffer.from("hello"));
  const [data] = await dataPromise;

  assert.equal(data.toString(), "hello");
  socket.destroy();
});
