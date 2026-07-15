import assert from "node:assert/strict";
import test from "node:test";

import type { Host } from "../../domain/models";
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

test("serial session factories snapshot legacy configs as explicit default Backspace", () => {
  const savedHostSession = createHostTerminalSession("session-1", host({
    protocol: "serial",
    hostname: "COM3",
    port: 115200,
    username: "",
    serialConfig: {
      path: "COM3",
      baudRate: 115200,
    },
  }));
  const quickSession = createSerialTerminalSession("session-2", {
    path: "COM4",
    baudRate: 9600,
  });

  assert.equal(savedHostSession.serialConfig?.backspaceBehavior, "default");
  assert.equal(quickSession.serialConfig?.backspaceBehavior, "default");
});
