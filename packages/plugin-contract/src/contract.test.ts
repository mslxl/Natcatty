import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const schema = JSON.parse(
  await readFile(
    new URL("../schema/plugin-contract.schema.json", import.meta.url),
    "utf8",
  ),
);
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
ajv.addSchema(schema);

function validator(definition: string) {
  return ajv.compile({ $ref: `${schema.$id}#/$defs/${definition}` });
}

const validManifest = {
  manifestVersion: 1,
  id: "com.example.contract-test",
  name: "contract-test",
  version: "1.2.3-beta.1",
  publisher: "example",
  engines: { netcatty: ">=1.0.0", api: "0.1.0-internal" },
  main: { browser: "dist/browser.js" },
  permissions: { required: ["commands"], optional: ["network"] },
  contributes: {
    commands: [{ id: "contract.run", title: "Run" }],
    menus: [{ command: "contract.run", location: "commandPalette" }],
    providers: [{ id: "contract.completion", label: "Completion", kind: "terminal.completion" }],
  },
};

test("plugin manifest schema accepts the internal contract", () => {
  const validate = validator("PluginManifest");
  assert.equal(validate(validManifest), true, JSON.stringify(validate.errors));
});

test("plugin manifest schema rejects unknown properties and traversal", () => {
  const validate = validator("PluginManifest");
  assert.equal(validate({ ...validManifest, unexpected: true }), false);
  assert.equal(
    validate({ ...validManifest, main: { browser: "../outside.js" } }),
    false,
  );
});

test("RPC, stream, permission, and provider schemas validate independently", () => {
  const rpc = validator("RpcRequest");
  const stream = validator("StreamFrame");
  const permission = validator("PermissionRequest");
  const provider = validator("ProviderRequest");

  assert.equal(rpc({ jsonrpc: "2.0", id: "1", method: "settings.get", deadlineMs: 1000 }), true);
  assert.equal(stream({ streamId: "s1", sequence: 0, kind: "open", windowBytes: 65536 }), true);
  assert.equal(permission({
    requestId: "p1",
    pluginId: "com.example.contract-test",
    permission: "network",
    reason: "Fetch completion metadata",
    resources: ["https://api.example.com"],
  }), true);
  assert.equal(provider({
    providerId: "contract.completion",
    operation: "provideCompletions",
    requestId: "r1",
    deadlineMs: 500,
  }), true);
});

test("secret setting defaults are excluded by the semantic CLI validator", async () => {
  const { validateManifestValue } = await import("../../plugin-cli/src/manifest.ts");
  const result = validateManifestValue({
    ...validManifest,
    contributes: {
      settings: [{
        id: "contract.token",
        label: "Token",
        control: "password",
        scope: "application",
        secret: true,
        default: "must-not-ship",
      }],
    },
  });
  assert.equal(result.valid, false);
  assert.match(result.errors.join("\n"), /must not declare a default/);
});
