import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { getSftpBreadcrumbSegments } from "../../application/state/sftp/utils.ts";
import {
  resolveSftpBreadcrumbVisibleParts,
  shouldKeepSftpBreadcrumbLeadingRoot,
} from "./SftpBreadcrumb.tsx";

const breadcrumbSource = fs.readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), "SftpBreadcrumb.tsx"),
  "utf8",
);

test("deep unix paths keep the trailing segments and hide the prefix", () => {
  const { segments, isWindowsDrive } = getSftpBreadcrumbSegments(
    "/var/www/apps/netcatty/releases/current/public",
  );
  const keepLeadingRoot = shouldKeepSftpBreadcrumbLeadingRoot({
    segments,
    isWindowsDrive,
  });
  assert.equal(keepLeadingRoot, false);

  const resolved = resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts: 4,
    keepLeadingRoot,
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
  const keepLeadingRoot = shouldKeepSftpBreadcrumbLeadingRoot({
    segments,
    isWindowsDrive,
  });
  assert.equal(keepLeadingRoot, true);

  const resolved = resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts: 4,
    keepLeadingRoot,
  });

  assert.equal(resolved.needsTruncation, true);
  assert.equal(resolved.visibleParts[0]?.segment.label, "C:");
  assert.deepEqual(
    resolved.visibleParts.slice(1).map((part) => part.segment.label),
    ["netcatty", "src", "components"],
  );
});

test("windows UNC paths keep the share root while preferring the tail", () => {
  const { segments, isWindowsDrive } = getSftpBreadcrumbSegments(
    "\\\\wsl.localhost\\Ubuntu-22.04\\home\\alice\\projects\\netcatty\\src",
  );
  assert.equal(isWindowsDrive, false);
  const keepLeadingRoot = shouldKeepSftpBreadcrumbLeadingRoot({
    segments,
    isWindowsDrive,
  });
  assert.equal(keepLeadingRoot, true);

  const resolved = resolveSftpBreadcrumbVisibleParts({
    segments,
    maxVisibleParts: 4,
    keepLeadingRoot,
  });

  assert.equal(resolved.needsTruncation, true);
  assert.equal(
    resolved.visibleParts[0]?.segment.label,
    "\\\\wsl.localhost\\Ubuntu-22.04",
  );
  assert.deepEqual(
    resolved.visibleParts.slice(1).map((part) => part.segment.label),
    ["projects", "netcatty", "src"],
  );
});

test("breadcrumb stays left-aligned without rtl clip", () => {
  assert.doesNotMatch(breadcrumbSource, /dir="rtl"/);
  assert.match(breadcrumbSource, /overflow-hidden/);
  assert.match(breadcrumbSource, /shouldKeepSftpBreadcrumbLeadingRoot/);
});
