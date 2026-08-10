import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { getSftpBreadcrumbSegments } from "../../application/state/sftp/utils.ts";
import { resolveSftpBreadcrumbVisibleParts } from "./SftpBreadcrumb.tsx";

const breadcrumbSource = fs.readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), "SftpBreadcrumb.tsx"),
  "utf8",
);

test("deep unix paths keep the trailing segments and hide the prefix", () => {
  const { segments } = getSftpBreadcrumbSegments(
    "/var/www/apps/netcatty/releases/current/public",
  );
  const resolved = resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts: 4,
    keepLeadingDrive: false,
  });

  assert.equal(resolved.needsTruncation, true);
  assert.deepEqual(
    resolved.visibleParts.map((part) => part.segment.label),
    ["netcatty", "releases", "current", "public"],
  );
  assert.deepEqual(
    resolved.hiddenParts.map((part) => part.segment.label),
    ["var", "www", "apps"],
  );
});

test("windows drive paths keep the drive letter while preferring the tail", () => {
  const { segments, isWindowsDrive } = getSftpBreadcrumbSegments(
    "C:\\Users\\alice\\projects\\netcatty\\src\\components",
  );
  assert.equal(isWindowsDrive, true);

  const resolved = resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts: 4,
    keepLeadingDrive: true,
  });

  assert.equal(resolved.needsTruncation, true);
  assert.equal(resolved.visibleParts[0]?.segment.label, "C:");
  assert.deepEqual(
    resolved.visibleParts.slice(1).map((part) => part.segment.label),
    ["netcatty", "src", "components"],
  );
});

test("breadcrumb overflow container prefers the path tail via rtl clip", () => {
  assert.match(breadcrumbSource, /dir="rtl"/);
  assert.match(breadcrumbSource, /dir="ltr"/);
  assert.match(breadcrumbSource, /overflow-hidden/);
});
