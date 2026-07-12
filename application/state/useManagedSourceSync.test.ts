import assert from "node:assert/strict";
import test from "node:test";

import type { Host } from "../../domain/models.ts";
import { haveSameManagedSshAgentFields } from "./useManagedSourceSync.ts";

const host: Host = {
  id: "host-1",
  label: "Managed host",
  hostname: "managed.example.com",
  username: "root",
  port: 22,
  protocol: "ssh",
  os: "linux",
  tags: [],
};

test("managed SSH source comparison tracks every agent setting", () => {
  const changedFields: Array<keyof Host> = [
    "useSshAgent",
    "identityAgent",
    "identitiesOnly",
    "addKeysToAgent",
    "useKeychain",
  ];

  for (const field of changedFields) {
    const changed = {
      ...host,
      [field]: field === "identityAgent" || field === "addKeysToAgent"
        ? "changed"
        : true,
    };
    assert.equal(haveSameManagedSshAgentFields(host, changed), false, field);
  }

  assert.equal(haveSameManagedSshAgentFields(host, { ...host }), true);
});
