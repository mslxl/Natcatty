import test from "node:test";
import assert from "node:assert/strict";
import { applyGroupDefaults, resolveGroupDefaults } from "./groupConfig.ts";
import type { GroupConfig, Host } from "./models.ts";

const host = (overrides: Partial<Host> = {}): Host => ({
  id: "host-1",
  label: "Host",
  hostname: "example.com",
  username: "root",
  tags: [],
  os: "linux",
  ...overrides,
});

test("applyGroupDefaults lets a host proxy profile override a group custom proxy", () => {
  const groupDefaults: Partial<GroupConfig> = {
    proxyConfig: { type: "http", host: "group-proxy.example.com", port: 3128 },
  };

  const result = applyGroupDefaults(host({ proxyProfileId: "proxy-1" }), groupDefaults);

  assert.equal(result.proxyProfileId, "proxy-1");
  assert.equal(result.proxyConfig, undefined);
});

test("applyGroupDefaults lets a host custom proxy override a group proxy profile", () => {
  const groupDefaults: Partial<GroupConfig> = {
    proxyProfileId: "group-proxy",
  };
  const customProxy = { type: "socks5" as const, host: "host-proxy.example.com", port: 1080 };

  const result = applyGroupDefaults(host({ proxyConfig: customProxy }), groupDefaults);

  assert.equal(result.proxyProfileId, undefined);
  assert.deepEqual(result.proxyConfig, customProxy);
});

test("resolveGroupDefaults treats saved and custom proxies as one inherited setting", () => {
  const resolved = resolveGroupDefaults("prod/api", [
    {
      path: "prod",
      proxyConfig: { type: "http", host: "parent-proxy.example.com", port: 3128 },
    },
    {
      path: "prod/api",
      proxyProfileId: "child-proxy",
    },
  ]);

  assert.equal(resolved.proxyProfileId, "child-proxy");
  assert.equal(resolved.proxyConfig, undefined);
});
