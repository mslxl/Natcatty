const test = require("node:test");
const assert = require("node:assert/strict");
const http = require("node:http");
const { URL } = require("node:url");

const {
  handleWebdavUpload,
  handleWebdavDownload,
  parseSyncedFileJson,
} = require("./cloudSyncBridge.cjs");

/**
 * Minimal WebDAV mock that reproduces #2223:
 * in-place PUT overwrites existing content WITHOUT truncating when the new
 * body is shorter — leaving trailing bytes from the previous longer body.
 *
 * MOVE replaces the destination resource wholesale (correct server behavior
 * for rename/replace). DELETE removes the resource.
 */
function startBuggyTruncateWebdavServer() {
  /** @type {Map<string, Buffer>} */
  const files = new Map();

  const normalizePath = (reqUrl) => {
    const u = new URL(reqUrl, "http://127.0.0.1");
    let p = decodeURIComponent(u.pathname);
    if (!p.startsWith("/")) p = `/${p}`;
    return p;
  };

  const propfindResponse = (path, length) =>
    `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>${path}</D:href>
    <D:propstat>
      <D:prop>
        <D:displayname>${path.split("/").pop()}</D:displayname>
        <D:getlastmodified>Sat, 10 May 2026 00:00:00 GMT</D:getlastmodified>
        <D:getcontentlength>${length}</D:getcontentlength>
        <D:resourcetype/>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`;

  const readBody = (req) =>
    new Promise((resolve, reject) => {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => resolve(Buffer.concat(chunks)));
      req.on("error", reject);
    });

  const server = http.createServer(async (req, res) => {
    const path = normalizePath(req.url || "/");
    const method = (req.method || "GET").toUpperCase();

    try {
      if (method === "PROPFIND") {
        if (!files.has(path)) {
          res.writeHead(404, { "Content-Type": "text/plain" });
          res.end("Not Found");
          return;
        }
        const body = files.get(path);
        res.writeHead(207, { "Content-Type": "application/xml; charset=utf-8" });
        res.end(propfindResponse(path, body.length));
        return;
      }

      if (method === "PUT") {
        const incoming = await readBody(req);
        const existing = files.get(path);
        if (existing && incoming.length < existing.length) {
          // Buggy non-truncate overwrite: keep the tail of the old file.
          const merged = Buffer.alloc(existing.length);
          existing.copy(merged);
          incoming.copy(merged, 0, 0, incoming.length);
          files.set(path, merged);
        } else {
          files.set(path, incoming);
        }
        res.writeHead(201, { "Content-Type": "text/plain" });
        res.end("Created");
        return;
      }

      if (method === "GET") {
        if (!files.has(path)) {
          res.writeHead(404);
          res.end("Not Found");
          return;
        }
        const body = files.get(path);
        res.writeHead(200, {
          "Content-Type": "application/octet-stream",
          "Content-Length": String(body.length),
        });
        res.end(body);
        return;
      }

      if (method === "DELETE") {
        files.delete(path);
        res.writeHead(204);
        res.end();
        return;
      }

      if (method === "MOVE") {
        const destHeader = req.headers.destination;
        if (!destHeader) {
          res.writeHead(400);
          res.end("Missing Destination");
          return;
        }
        const destUrl = new URL(destHeader);
        const destPath = normalizePath(destUrl.pathname);
        if (!files.has(path)) {
          res.writeHead(404);
          res.end("Not Found");
          return;
        }
        // Correct replace: destination becomes an exact copy of source.
        files.set(destPath, Buffer.from(files.get(path)));
        files.delete(path);
        res.writeHead(201);
        res.end("Created");
        return;
      }

      res.writeHead(405);
      res.end("Method Not Allowed");
    } catch (err) {
      res.writeHead(500);
      res.end(String(err && err.message ? err.message : err));
    }
  });

  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      resolve({
        server,
        endpoint: `http://127.0.0.1:${port}`,
        files,
      });
    });
  });
}

const longSyncedFile = {
  meta: {
    version: 1,
    updatedAt: 1,
    deviceId: "dev-a",
    deviceName: "A",
    appVersion: "1.0.0",
    iv: "aaaaaaaaaaaaaaaaaaaaaa==",
    salt: "bbbbbbbbbbbbbbbbbbbbbb==",
    algorithm: "AES-256-GCM",
    kdf: "PBKDF2",
    kdfIterations: 100000,
  },
  // Long payload so a shorter follow-up upload would leave a tail on buggy servers.
  payload:
    "KgdTYoctay9qud8Tq0QdtU6p1CGK8pppQrevpTErSMXqEnKtnJjEUrthXYI+zOcM3vaJ3J/0rzqhF0IglLU9MUToId6yE3GhT5FWWmSWCLei+UIjkaXnKBVxZ4VgrbX9R/IuJ/x5Ah80Oym1elVnWICNBrfjuhv3O8PWjR6TvEUvhG6jXAlok/akJjVLaPgDtU4jARH770BBO+U/W8AKdwNjdEipS0llY=n9lPHR9rL64LuAxzCMbND55onY+j9mCvRibnNm4lUX3ybfZLeqq+9cG/od4CrcdVGcde9FKYZ8UVS9g1UtxGzwW8zMW1kTH3L950WGQqrDosKusj+wTabh6lLdMdMRF7TOiMjmgRzMvQ==",
};

const shortSyncedFile = {
  ...longSyncedFile,
  meta: { ...longSyncedFile.meta, version: 2, updatedAt: 2 },
  payload: "KgdTYoctay9qud8Tq0QdtU6p1CGK8pppQrevpTErSMXqEnKtnJjEUrthXYI+zOcM3vaJ3J/0rzqhF0IglLU9MUToId6yE3GhT5FWWmSWCLei+UIjkaXnKBVxZ4VgrbX9R/IuJ/x5Ah80Oym1elVnWICNBrfjuhv3O8PWjR6TvEUvhG6jXAlok/akJjVLaPgDtU4jARH770BBO+U/W8AKdwNjdEipS0llY=",
};

test("parseSyncedFileJson recovers trailing garbage after valid JSON (#2223)", () => {
  const good = JSON.stringify({ meta: { version: 1 }, payload: "abc" });
  const corrupt = `${good}n9lPHR9rL64LuAxzCMbND55onY+j9mCvRibnNm4lUX3ybfZLeqq+9cG/od4CrcdVGcde9FKYZ8UVS9g1UtxGzwW8zMW1kTH3L950WGQqrDosKusj+wTabh6lLdMdMRF7TOiMjmgRzMvQ=="}`;
  assert.throws(() => JSON.parse(corrupt));
  const recovered = parseSyncedFileJson(corrupt);
  assert.deepEqual(recovered, { meta: { version: 1 }, payload: "abc" });
});

test("parseSyncedFileJson rethrows genuine JSON errors", () => {
  assert.throws(() => parseSyncedFileJson("{not-json"), /JSON|Unexpected|Expected/i);
});

test("handleWebdavUpload replaces shorter body without trailing garbage on buggy servers (#2223)", async () => {
  const { server, endpoint, files } = await startBuggyTruncateWebdavServer();
  const config = {
    endpoint,
    authType: "basic",
    username: "u",
    password: "p",
  };

  try {
    // First upload (create) — full long body.
    await handleWebdavUpload(config, longSyncedFile);
    const afterLong = files.get("/netcatty-vault.json");
    assert.ok(afterLong);
    assert.equal(afterLong.toString("utf8"), JSON.stringify(longSyncedFile));

    // Second upload — shorter body. Direct non-truncating PUT would corrupt;
    // atomic replace (temp + MOVE) must leave clean JSON only.
    await handleWebdavUpload(config, shortSyncedFile);
    const afterShort = files.get("/netcatty-vault.json");
    assert.ok(afterShort);
    const text = afterShort.toString("utf8");
    assert.equal(text, JSON.stringify(shortSyncedFile));
    // No leftover temp files.
    for (const key of files.keys()) {
      assert.ok(!key.includes(".tmp-"), `leftover temp file: ${key}`);
    }

    const downloaded = await handleWebdavDownload(config);
    assert.deepEqual(downloaded.syncedFile, shortSyncedFile);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("handleWebdavDownload recovers already-corrupt remote JSON (#2223)", async () => {
  const { server, endpoint, files } = await startBuggyTruncateWebdavServer();
  const config = {
    endpoint,
    authType: "basic",
    username: "u",
    password: "p",
  };

  try {
    const good = JSON.stringify(shortSyncedFile);
    const corrupt = Buffer.from(
      `${good}n9lPHR9rL64LuAxzCMbND55onY+j9mCvRibnNm4lUX3ybfZLeqq+9cG/od4CrcdVGcde9FKYZ8UVS9g1UtxGzwW8zMW1kTH3L950WGQqrDosKusj+wTabh6lLdMdMRF7TOiMjmgRzMvQ=="}`,
      "utf8"
    );
    files.set("/netcatty-vault.json", corrupt);

    const downloaded = await handleWebdavDownload(config);
    assert.deepEqual(downloaded.syncedFile, shortSyncedFile);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
