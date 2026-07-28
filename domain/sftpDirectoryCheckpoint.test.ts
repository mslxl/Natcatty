import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import {
  accountSftpDirectoryEntries,
  appendDirectoryCheckpointIdentity,
  appendDirectoryManifestIdentity,
  claimSftpDirectoryVisit,
  createDirectoryManifestAccumulator,
  createEmptyDirectoryResumeCheckpoint,
  createSftpDirectoryTraversalBudget,
  EMPTY_DIRECTORY_MANIFEST_HASH,
  isValidDirectoryResumeCheckpoint,
  MAX_SFTP_FOLLOWED_SYMLINK_DEPTH,
  shouldFollowSftpSymlinkDirectory,
} from "./sftpDirectoryCheckpoint";

test("versioned directory manifests detect changed and reordered entries", () => {
  const first = "a".repeat(64);
  const second = "b".repeat(64);
  const changed = "c".repeat(64);
  const build = (identities: string[]) => {
    const checkpoint = createEmptyDirectoryResumeCheckpoint();
    for (const identity of identities) {
      checkpoint.manifestHash = appendDirectoryCheckpointIdentity(checkpoint, identity);
      checkpoint.coveredEntries += 1;
    }
    return checkpoint;
  };

  const original = build([first, second]);
  const batched = createDirectoryManifestAccumulator(createEmptyDirectoryResumeCheckpoint());
  batched.append(first);
  batched.append(second);
  assert.equal(original.version, 2);
  assert.equal(original.manifestHash, batched.digest());
  assert.equal(original.manifestHash, build([first, second]).manifestHash);
  assert.notEqual(original.manifestHash, build([first, changed]).manifestHash);
  assert.notEqual(original.manifestHash, build([second, first]).manifestHash);
});

test("legacy chained manifests remain valid and append-compatible", () => {
  const legacy = {
    version: 1 as const,
    coveredEntries: 1,
    completedEntries: 1,
    manifestHash: appendDirectoryManifestIdentity(
      EMPTY_DIRECTORY_MANIFEST_HASH,
      "a".repeat(64),
    ),
  };
  assert.equal(isValidDirectoryResumeCheckpoint(legacy), true);
  assert.equal(
    legacy.manifestHash,
    createHash("sha256")
      .update(`${EMPTY_DIRECTORY_MANIFEST_HASH}:${"a".repeat(64)}`)
      .digest("hex"),
  );
  assert.equal(
    appendDirectoryCheckpointIdentity(legacy, "b".repeat(64)),
    appendDirectoryManifestIdentity(legacy.manifestHash, "b".repeat(64)),
  );
});

test("live and resumed directory transfers share the same symlink depth policy", () => {
  assert.equal(MAX_SFTP_FOLLOWED_SYMLINK_DEPTH, 32);
  assert.equal(shouldFollowSftpSymlinkDirectory(8), true);
  assert.equal(shouldFollowSftpSymlinkDirectory(31), true);
  assert.equal(shouldFollowSftpSymlinkDirectory(32), false);
});

test("remote directory traversal skips canonical cycles and enforces total work budgets", () => {
  const budget = createSftpDirectoryTraversalBudget({ maxDirectories: 2, maxEntries: 3 });
  assert.equal(claimSftpDirectoryVisit(budget, "/srv/root"), true);
  accountSftpDirectoryEntries(budget, 2);
  assert.equal(claimSftpDirectoryVisit(budget, "/srv/root"), false);
  assert.equal(claimSftpDirectoryVisit(budget, "/srv/other"), true);
  assert.throws(() => accountSftpDirectoryEntries(budget, 2), /entry limit/i);
  assert.throws(() => claimSftpDirectoryVisit(budget, "/srv/third"), /directory limit/i);
});
