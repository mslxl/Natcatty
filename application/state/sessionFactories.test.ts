import assert from "node:assert/strict";
import test from "node:test";

import type { Host } from "../../domain/models";
import { applyGroupDefaults } from "../../domain/groupConfig";
import { prepareSerialConfigForSavedHost } from "../../domain/serialBackspace";
import { buildTelnetDeepLinkConnectionHost } from "../../domain/telnetDeepLink";
import { createHostTerminalSession, createSerialTerminalSession } from "./sessionFactories";

const host = (overrides: Partial<Host>): Host => ({
  id: "host-1",
  label: "Host",
  hostname: "example.com",
  username: "alice",
  port: 22,
  group: "",
  tags: [],
  os: "linux",
  protocol: "ssh",
  createdAt: 1,
  ...overrides,
});

test("createHostTerminalSession keeps telnet deep-link default port for ssh hosts with telnet enabled", () => {
  const connectionHost = buildTelnetDeepLinkConnectionHost(
    host({
      protocol: "ssh",
      telnetEnabled: true,
      telnetPort: undefined,
    }),
  );

  const session = createHostTerminalSession("session-1", connectionHost);

  assert.equal(session.protocol, "telnet");
  assert.equal(session.port, 23);
});

test("serial session factories snapshot effective legacy Backspace behavior", () => {
  const savedHostSession = createHostTerminalSession("session-1", host({
    protocol: "serial",
    hostname: "COM3",
    port: 115200,
    username: "",
    serialConfig: {
      path: "COM3",
      baudRate: 115200,
    },
    backspaceBehavior: "ctrl-h",
  }));
  const quickSession = createSerialTerminalSession("session-2", {
    path: "COM4",
    baudRate: 9600,
  });
  const explicitDefaultSession = createHostTerminalSession("session-3", host({
    protocol: "serial",
    hostname: "COM5",
    port: 115200,
    username: "",
    backspaceBehavior: "ctrl-h",
    serialConfig: {
      path: "COM5",
      baudRate: 115200,
      backspaceBehavior: "default",
    },
  }));

  assert.equal(savedHostSession.serialConfig?.backspaceBehavior, "ctrl-h");
  assert.equal(quickSession.serialConfig?.backspaceBehavior, "default");
  assert.equal(explicitDefaultSession.serialConfig?.backspaceBehavior, "default");
});

test("saved quick-connect hosts can inherit Ctrl-H after moving into a group", () => {
  const savedHost = host({
    protocol: "serial",
    hostname: "COM3",
    port: 115200,
    username: "",
    group: "network/serial",
    serialConfig: prepareSerialConfigForSavedHost({
      path: "COM3",
      baudRate: 115200,
      backspaceBehavior: "default",
    }),
  });
  const effectiveHost = applyGroupDefaults(savedHost, {
    backspaceBehavior: "ctrl-h",
  });

  const session = createHostTerminalSession("session-1", effectiveHost);

  assert.equal(session.serialConfig?.backspaceBehavior, "ctrl-h");
});
