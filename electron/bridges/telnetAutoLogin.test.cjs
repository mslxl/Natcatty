const test = require("node:test");
const assert = require("node:assert/strict");

const { createTelnetAutoLogin } = require("./telnetAutoLogin.cjs");

test("telnet auto-login sends saved username and password for split prompts", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    username: "admin",
    password: "secret",
    write: (data) => writes.push(data),
  });

  autoLogin.handleText("\x1b[32mUser");
  autoLogin.handleText("name:\x1b[0m ");
  autoLogin.handleText("\r\nPass");
  autoLogin.handleText("word: ");

  assert.deepEqual(writes, ["admin\r", "secret\r"]);
});

test("telnet auto-login supports password-only prompts", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    password: "line-password",
    write: (data) => writes.push(data),
  });

  autoLogin.handleText("Password: ");

  assert.deepEqual(writes, ["line-password\r"]);
});

test("telnet auto-login wakes devices that ask for return before login", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    username: "admin",
    password: "secret",
    write: (data) => writes.push(data),
  });

  autoLogin.handleText("Press RETURN to get started.");
  autoLogin.handleText("\r\nrouter login: ");
  autoLogin.handleText("\r\nPassword: ");

  assert.deepEqual(writes, ["\r", "admin\r", "secret\r"]);
});

test("telnet auto-login handles prompts concatenated after wake banners", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    username: "admin",
    password: "secret",
    write: (data) => writes.push(data),
  });

  autoLogin.handleText("Press RETURN to get started.");
  autoLogin.handleText("Username: ");
  autoLogin.handleText("\r\nPassword: ");

  assert.deepEqual(writes, ["\r", "admin\r", "secret\r"]);
});

test("telnet auto-login handles wake banners concatenated with preceding text", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    username: "admin",
    password: "secret",
    write: (data) => writes.push(data),
  });

  autoLogin.handleText("Netcatty local Telnet test servicePress RETURN to get started.");
  autoLogin.handleText("Username: ");
  autoLogin.handleText("\r\nPassword: ");

  assert.deepEqual(writes, ["\r", "admin\r", "secret\r"]);
});

test("telnet auto-login stops when the user starts typing manually", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    username: "admin",
    password: "secret",
    write: (data) => writes.push(data),
  });

  autoLogin.handleUserInput();
  autoLogin.handleText("Username: ");
  autoLogin.handleText("Password: ");

  assert.deepEqual(writes, []);
});

test("telnet auto-login avoids common non-prompt login text", () => {
  const writes = [];
  const autoLogin = createTelnetAutoLogin({
    username: "admin",
    password: "secret",
    write: (data) => writes.push(data),
  });

  autoLogin.handleText("Last login:");

  assert.deepEqual(writes, []);
});
