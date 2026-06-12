import test from "node:test";
import assert from "node:assert/strict";

import type { DropEntry } from "../../lib/sftpFileUtils";
import type { Host } from "../../types";
import { handleTerminalDropEntries } from "./hooks/useTerminalDragDrop";
import { resolvePreferredTerminalCwd } from "./sftpCwd";

const host = {
  id: "host-1",
  label: "Host",
  hostname: "example.com",
  port: 22,
  username: "alice",
  protocol: "ssh",
} as Host;

const dropEntries: DropEntry[] = [
  {
    file: null,
    relativePath: "report.txt",
    isDirectory: false,
  },
];

test("remote SSH terminal drop triggers ZMODEM drag-drop upload", async () => {
  let uploadedFiles: unknown;
  let uploadedSessionId: string | undefined;

  await handleTerminalDropEntries({
    dropEntries: [
      {
        file: {
          name: "report.txt",
          arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer,
        } as File,
        relativePath: "report.txt",
        isDirectory: false,
      },
    ],
    host,
    isLocalConnection: false,
    resolveSftpInitialPath: async () => "/srv/app/current",
    scrollToBottomAfterProgrammaticInput: () => {},
    sessionId: "session-1",
    sessionRef: { current: "session-1" },
    terminalBackend: {
      writeToSession: () => {},
      startZmodemDragDropUpload: async (sessionId, files) => {
        uploadedSessionId = sessionId;
        uploadedFiles = files;
        return { success: true };
      },
    },
    termRef: { current: null },
  });

  assert.equal(uploadedSessionId, "session-1");
  assert.equal(Array.isArray(uploadedFiles), true);
  const files = uploadedFiles as Array<{ name: string; remoteName: string; data?: ArrayBuffer }>;
  assert.equal(files.length, 1);
  assert.equal(files[0].name, "report.txt");
  assert.equal(files[0].remoteName, "report.txt");
  assert.ok(files[0].data);
});

test("network device drop falls back to SFTP upload with a freshly resolved cwd", async () => {
  let receivedOptions: { preferFreshBackend?: boolean } | undefined;
  let openedPath: string | undefined;
  let openedEntries: DropEntry[] | undefined;
  let openedSessionId: string | undefined;

  await handleTerminalDropEntries({
    dropEntries,
    host,
    isLocalConnection: false,
    isNetworkDevice: true,
    onOpenSftp: (_host, initialPath, pendingUploadEntries, sourceSessionId) => {
      openedPath = initialPath;
      openedEntries = pendingUploadEntries;
      openedSessionId = sourceSessionId;
    },
    resolveSftpInitialPath: async (options) => {
      receivedOptions = options;
      return "/srv/app/current";
    },
    scrollToBottomAfterProgrammaticInput: () => {},
    sessionId: "session-1",
    sessionRef: { current: "session-1" },
    terminalBackend: {
      writeToSession: () => {},
    },
    termRef: { current: null },
  });

  assert.deepEqual(receivedOptions, { preferFreshBackend: true });
  assert.equal(openedPath, "/srv/app/current");
  assert.equal(openedEntries, dropEntries);
  assert.equal(openedSessionId, "session-1");
});

test("fresh cwd resolution falls back to the renderer cwd when backend probe has no real cwd", async () => {
  const cwd = await resolvePreferredTerminalCwd({
    rendererCwd: "/srv/app/current",
    sessionId: "session-1",
    preferFreshBackend: true,
    getSessionPwd: async (_sessionId, options) => {
      assert.deepEqual(options, { allowHomeFallback: false });
      return { success: false, error: "Could not determine cwd" };
    },
  });

  assert.equal(cwd, "/srv/app/current");
});
