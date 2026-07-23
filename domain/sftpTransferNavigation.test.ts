import assert from "node:assert/strict";
import test from "node:test";

import {
  resolveSftpTransferNavigationPath,
  resolveSftpTransferNavigationTarget,
} from "./sftpTransferNavigation";

test("resume of a direct download opens the remote source host", () => {
  const target = resolveSftpTransferNavigationTarget({
    direction: "download",
    sourceHostId: "host-1",
    targetConnectionId: "local",
    sourceConnectionId: "conn-remote",
    sourcePath: "/root/geoip.metadb",
    targetPath: "/Users/me/Desktop/geoip.metadb",
    isDirectory: false,
  }, true);

  assert.deepEqual(target, {
    kind: "remote-host",
    hostId: "host-1",
    useSourcePath: true,
  });
});

test("resume of a dual-pane download still opens the remote source", () => {
  // Dual-pane local targets use a real connection UUID, not the "local" sentinel.
  const target = resolveSftpTransferNavigationTarget({
    direction: "download",
    sourceHostId: "host-1",
    targetHostId: undefined,
    sourceConnectionId: "conn-remote",
    targetConnectionId: "conn-local-uuid",
    sourcePath: "/root/geoip.metadb",
    targetPath: "/Users/me/Desktop/geoip.metadb",
    isDirectory: false,
  }, true);

  assert.deepEqual(target, {
    kind: "remote-host",
    hostId: "host-1",
    useSourcePath: true,
  });
});

test("opening a finished download target uses the local folder", () => {
  const target = resolveSftpTransferNavigationTarget({
    direction: "download",
    sourceHostId: "host-1",
    targetConnectionId: "conn-local-uuid",
    sourceConnectionId: "conn-remote",
    sourcePath: "/root/geoip.metadb",
    targetPath: "/Users/me/Desktop/geoip.metadb",
    isDirectory: false,
  }, false);

  assert.equal(target.kind, "local-path");
  assert.equal(
    resolveSftpTransferNavigationPath({
      sourcePath: "/root/geoip.metadb",
      targetPath: "/Users/me/Desktop/geoip.metadb",
      isDirectory: false,
    }, false),
    "/Users/me/Desktop",
  );
});

test("resume of an upload opens the remote target host", () => {
  const target = resolveSftpTransferNavigationTarget({
    direction: "upload",
    targetHostId: "host-2",
    sourceConnectionId: "local",
    targetConnectionId: "conn-remote",
    sourcePath: "/Users/me/file.txt",
    targetPath: "/tmp/file.txt",
    isDirectory: false,
  }, true);

  assert.deepEqual(target, {
    kind: "remote-host",
    hostId: "host-2",
    useSourcePath: false,
  });
});

test("resume of a local copy only opens the SFTP panel", () => {
  const target = resolveSftpTransferNavigationTarget({
    direction: "local-copy",
    sourceConnectionId: "local",
    targetConnectionId: "local",
    sourcePath: "/Users/me/a.txt",
    targetPath: "/Users/me/b.txt",
    isDirectory: false,
  }, true);

  assert.deepEqual(target, {
    kind: "local-copy-panel",
    useSourcePath: false,
  });
});
