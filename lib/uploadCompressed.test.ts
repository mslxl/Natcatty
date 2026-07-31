import assert from "node:assert/strict";
import test from "node:test";

import { uploadFoldersCompressed } from "./uploadCompressed";
import type { DropEntry } from "./sftpFileUtils";

test("compressed folder uploads use reconstructed drop-entry paths", async () => {
  const previousWindow = globalThis.window;
  Object.defineProperty(globalThis, "window", {
    configurable: true,
    value: {
      netcatty: {
        checkCompressedUploadSupport: async () => ({
          supported: false,
          localTar: false,
          remoteTar: false,
        }),
      },
    },
  });

  try {
    const entry: DropEntry = {
      file: { name: "child.txt", size: 1 } as File,
      localPath: "/tmp/example-folder/child.txt",
      relativePath: "example-folder/child.txt",
      isDirectory: false,
    };
    const results = await uploadFoldersCompressed(
      [["example-folder", [entry]]],
      "/remote",
      "sftp-1",
    );

    assert.deepEqual(results, [{
      fileName: "example-folder",
      success: false,
      error: "Compressed upload not supported - fallback needed",
    }]);
  } finally {
    if (previousWindow === undefined) {
      Reflect.deleteProperty(globalThis, "window");
    } else {
      Object.defineProperty(globalThis, "window", {
        configurable: true,
        value: previousWindow,
      });
    }
  }
});
