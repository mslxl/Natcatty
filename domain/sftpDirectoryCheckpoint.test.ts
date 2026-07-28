import assert from "node:assert/strict";
import test from "node:test";

import {
  accountSftpDirectoryEntries,
  claimSftpDirectoryVisit,
  createSftpDirectoryTraversalBudget,
  MAX_SFTP_FOLLOWED_SYMLINK_DEPTH,
  shouldFollowSftpSymlinkDirectory,
} from "./sftpDirectoryCheckpoint";

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
