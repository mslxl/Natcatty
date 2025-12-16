/* eslint-disable @typescript-eslint/no-var-requires */

if (process.platform !== "win32") {
  throw new Error("netcatty-windows-hello is only available on Windows");
}

const path = require("node:path");

module.exports = require(path.join(__dirname, "build", "Release", "netcatty_windows_hello.node"));

