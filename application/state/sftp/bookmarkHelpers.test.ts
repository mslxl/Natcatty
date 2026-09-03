import test from "node:test";
import assert from "node:assert/strict";

import {
  createSftpBookmark,
  getSftpBookmarkScope,
  moveSftpBookmark,
  renameSftpBookmark,
} from "./bookmarkHelpers.ts";

test("getSftpBookmarkScope uses persisted scope instead of the bookmark id shape", () => {
  const bookmarks = [
    { id: "opaque-synced-id", global: true },
    { id: "gbm-looking-location-id" },
  ];

  assert.equal(getSftpBookmarkScope(bookmarks, "opaque-synced-id"), "global");
  assert.equal(getSftpBookmarkScope(bookmarks, "gbm-looking-location-id"), "location");
  assert.equal(getSftpBookmarkScope(bookmarks, "missing"), null);
});

test("moveSftpBookmark reorders by id and ignores unknown ids", () => {
  const bookmarks = [
    { id: "a", path: "/a" },
    { id: "b", path: "/b" },
    { id: "c", path: "/c" },
  ];
  assert.deepEqual(
    moveSftpBookmark(bookmarks, "c", "a").map((item) => item.id),
    ["c", "a", "b"],
  );
  assert.equal(moveSftpBookmark(bookmarks, "missing", "a"), bookmarks);
  assert.equal(moveSftpBookmark(bookmarks, "a", "a"), bookmarks);
});

test("renameSftpBookmark updates a trimmed label", () => {
  const bookmarks = [
    createSftpBookmark("/var/www"),
    createSftpBookmark("/etc"),
  ];
  const renamed = renameSftpBookmark(bookmarks, bookmarks[0].id, "  Web  ");
  assert.equal(renamed[0].label, "Web");
  assert.equal(renamed[1].label, bookmarks[1].label);
  assert.equal(renameSftpBookmark(bookmarks, bookmarks[0].id, "   "), bookmarks);
});
