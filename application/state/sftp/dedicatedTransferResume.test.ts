import assert from "node:assert/strict";
import test from "node:test";

import type { Host } from "../../../domain/models";
import { resolveHostForTransferEndpoint } from "./dedicatedTransferResume";

const host = (id: string, label: string, hostname = label): Host => ({
  id,
  label,
  hostname,
  port: 22,
  username: "root",
  authMethod: "password",
  protocol: "ssh",
} as Host);

test("resolveHostForTransferEndpoint prefers id then label", () => {
  const hosts = [host("id-1", "CI-Build-01", "ci-01.example")];
  assert.equal(resolveHostForTransferEndpoint(hosts, "id-1", "other")?.id, "id-1");
  assert.equal(resolveHostForTransferEndpoint(hosts, "missing", "CI-Build-01")?.id, "id-1");
  assert.equal(resolveHostForTransferEndpoint(hosts, undefined, "ci-01.example")?.id, "id-1");
  assert.equal(resolveHostForTransferEndpoint(hosts, "missing", "gone"), null);
});
