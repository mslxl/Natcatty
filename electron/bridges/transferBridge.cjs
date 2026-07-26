/**
 * Transfer Bridge - Handles file transfers with progress and cancellation
 * Extracted from main.cjs for single responsibility
 */

const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const { Readable } = require("node:stream");
const tempDirBridge = require("./tempDirBridge.cjs");
const {
  encodePathForSession,
  ensureRemoteDirForSession,
  runRemoteUploadTransaction,
  requireSftpChannel,
  resolveEncodingForRequest,
} = require("./sftpBridge.cjs");
const { isScpModeClient, getScpBackendForClient } = require("./sftpBridge/scpBackend.cjs");
const {
  DOWNLOAD_TRANSFER_CONCURRENCY,
  FAST_DOWNLOAD_CHANNELS_PER_SESSION,
  TRANSFER_CHUNK_SIZE,
  UPLOAD_TRANSFER_CONCURRENCY,
} = require("./transferLimits.cjs");

/**
 * Soft cap for concurrent-range pause drain. Short grace lets already-finished
 * range callbacks land so checkpointBytes is not always 0, without waiting for
 * the full in-flight window (was multi-second "finish current step").
 */
const PAUSE_RANGE_DRAIN_MS = 50;
/** Cap stream drain / pending-open waits for non-concurrent (pipe) pauses. */
const PAUSE_STREAM_DRAIN_MS = 80;
/** Resume never overlaps old range writes; report a retryable miss if they stall. */
const RESUME_RANGE_SETTLE_MS = 1500;

/**
 * Fan transfer lifecycle to every window so the global transfer center keeps
 * updating after the originating SFTP panel is hidden or unmounted.
 * Panel-local callbacks (netcatty:transfer:progress) alone die with the panel.
 */
function broadcastGlobalTransferEvent(payload) {
  if (!payload || !payload.transferId) return;
  // Bare Node unit tests must never require("electron"): that package's entry
  // downloads the binary when dist/ is missing, hanging transferBridge tests
  // for tens of seconds per progress/pause event. process.versions.electron is
  // only set inside a real Electron main/renderer process.
  if (typeof process.versions?.electron !== "string") return;
  try {
    const electron = require("electron");
    // Outside Electron, require("electron") is a path string — not the API.
    if (!electron || typeof electron !== "object") return;
    const BrowserWindow = electron.BrowserWindow;
    if (!BrowserWindow?.getAllWindows) return;
    for (const win of BrowserWindow.getAllWindows()) {
      try {
        if (win?.isDestroyed?.()) continue;
        const wc = win.webContents;
        if (!wc || wc.isDestroyed?.()) continue;
        wc.send("netcatty:sftp:global-transfer", payload);
      } catch {
        // best-effort per window
      }
    }
  } catch {
    // electron unavailable
  }
}

/**
 * Verify a completed remote upload matches the expected byte count.
 * Without this check, fastPut/stream uploads can report success while leaving
 * a truncated file on servers that mishandle large WRITE packets (#2022).
 */
async function assertRemoteUploadSize(client, remotePath, expectedSize) {
  if (!Number.isFinite(expectedSize) || expectedSize < 0) return;
  if (!client || typeof client.stat !== "function") return;

  let attrs;
  try {
    attrs = await client.stat(remotePath);
  } catch (err) {
    throw new Error(
      `Upload completed but remote file could not be verified (${remotePath}): ${err.message || String(err)}`,
    );
  }

  const remoteSize = Number(attrs?.size);
  if (!Number.isFinite(remoteSize)) {
    throw new Error(`Upload completed but remote file size is unavailable (${remotePath})`);
  }
  if (remoteSize !== expectedSize) {
    throw new Error(
      `Upload size mismatch for ${remotePath}: expected ${expectedSize} bytes, got ${remoteSize}`,
    );
  }
}

/**
 * Safely ensure a local directory exists.
 * On Windows, `mkdir("E:\\", { recursive: true })` throws EPERM for drive roots.
 * We catch that and verify the directory already exists before re-throwing.
 */
async function ensureLocalDir(dir) {
  try {
    await fs.promises.mkdir(dir, { recursive: true });
  } catch (err) {
    // If the directory already exists, ignore the error (covers EPERM on drive roots)
    try {
      const stat = await fs.promises.stat(dir);
      if (stat.isDirectory()) return;
    } catch { /* stat failed, re-throw original */ }
    throw err;
  }
}

function buildRemoteTransferStagePath(targetPath, transferId) {
  const dir = path.posix.dirname(targetPath);
  const base = path.posix.basename(targetPath);
  const safeId = String(transferId).replace(/[^A-Za-z0-9_-]/g, "_");
  return path.posix.join(dir, `.${base}.netcatty-${safeId}.part`);
}

/**
 * Reconcile a claimed resume offset with durable staged bytes.
 *
 * Progress events update checkpointBytes as soon as data is handed to the write
 * stream — not when those bytes are fully flushed to disk/remote. After a hard
 * app quit the UI-persisted checkpoint is almost always ahead of the .part file,
 * so exact-size equality would reject resume ~100% of the time. Disk/remote size
 * is the source of truth for how far we can safely continue.
 *
 * Returns the safe resume offset (0 when the staged file is missing/unusable).
 */
async function resolveLocalResumeCheckpoint(filePath, claimedCheckpoint) {
  const claimed = Math.max(0, Number(claimedCheckpoint) || 0);
  if (!claimed) return 0;
  try {
    const stat = await fs.promises.stat(filePath);
    if (!stat.isFile()) return 0;
    // Never resume past durable bytes. If the file is larger than the claim,
    // keep the claim — the write stream overwrites from that offset.
    return Math.min(claimed, Math.max(0, Number(stat.size) || 0));
  } catch (error) {
    if (error?.code === "ENOENT") return 0;
    throw new Error(`Resume safety check failed for local temporary file: ${error.message || String(error)}`);
  }
}

async function resolveRemoteResumeCheckpoint(client, sftpId, filePath, encoding, claimedCheckpoint) {
  const claimed = Math.max(0, Number(claimedCheckpoint) || 0);
  if (!claimed) return 0;
  try {
    const stat = isScpModeClient(client)
      ? await getScpBackendForClient(client).stat(filePath, { encoding })
      : await client.stat(encodePathForSession(sftpId, filePath, encoding));
    return Math.min(claimed, Math.max(0, Number(stat?.size) || 0));
  } catch (error) {
    const message = error?.message || String(error);
    // Missing remote .part after kill/crash — start over instead of blocking forever.
    if (
      error?.code === "ENOENT"
      || error?.code === 2
      || /no such file|ENOENT|not found|does not exist/i.test(message)
    ) {
      return 0;
    }
    throw new Error(`Resume safety check failed for remote temporary file: ${message}`);
  }
}

async function hashReadable(readable) {
  const hash = crypto.createHash("sha256");
  for await (const chunk of readable) hash.update(chunk);
  return hash.digest("hex");
}

function hashLocalPrefix(filePath, bytes) {
  if (!bytes) return Promise.resolve(null);
  return hashReadable(fs.createReadStream(filePath, { start: 0, end: bytes - 1 }));
}

function hashLocalFile(filePath) {
  return hashReadable(fs.createReadStream(filePath));
}

async function hashRemoteFile(client, sftpId, filePath, encoding) {
  if (isScpModeClient(client)) return null;
  const sshClient = client?.client;
  if (sshClient && typeof sshClient.exec === "function") {
    const escapedPath = String(filePath).replace(/'/g, "'\\''");
    const digest = await new Promise((resolve, reject) => {
      sshClient.exec(`sha256sum -- '${escapedPath}'`, (error, stream) => {
        if (error) return reject(error);
        let stdout = "";
        let stderr = "";
        stream.on("data", (chunk) => { stdout += chunk.toString(); });
        stream.stderr?.on?.("data", (chunk) => { stderr += chunk.toString(); });
        stream.on("close", (code) => {
          const match = stdout.match(/^([a-fA-F0-9]{64})\s/);
          if (code === 0 && match) resolve(match[1].toLowerCase());
          else reject(new Error(stderr || "Remote SHA-256 is unavailable"));
        });
      });
    }).catch(() => null);
    if (digest) return digest;
  }
  if (!client.sftp) await requireSftpChannel(client);
  if (typeof client.sftp?.createReadStream !== "function") {
    throw new Error("Remote SHA-256 verification is unavailable");
  }
  return hashReadable(client.sftp.createReadStream(encodePathForSession(sftpId, filePath, encoding)));
}

async function computeSourceFingerprint({ sourceType, sourcePath, sourceSftpId, sourceEncoding }) {
  if (sourceType === "local") return `sha256:${await hashLocalFile(sourcePath)}`;
  const client = sftpClients.get(sourceSftpId);
  if (!client) throw new Error("Source SFTP session not found");
  const digest = await hashRemoteFile(client, sourceSftpId, sourcePath, sourceEncoding);
  return digest ? `sha256:${digest}` : null;
}

/**
 * Fast pause/resume identity: size + mtime + head/mid/tail content samples.
 * Full-file SHA-256 is too slow for the pause critical path and must not run
 * as a post-pause background network read (would keep remote I/O after the UI
 * shows paused). Size+mtime alone is unsafe for restart resumes — SFTP mtimes
 * are often whole seconds, so same-size rewrites can keep the meta stamp and
 * corrupt a checkpointed resume past the 256 KiB staged-content sample.
 */
const IDENTITY_SAMPLE_BYTES = TRANSFER_CHUNK_SIZE;

function sampleOffsetsForIdentity(fileSize) {
  if (fileSize <= 0) return [];
  const sampleSize = Math.min(IDENTITY_SAMPLE_BYTES, fileSize);
  return [...new Set([
    0,
    Math.max(0, Math.floor((fileSize - sampleSize) / 2)),
    Math.max(0, fileSize - sampleSize),
  ])].map((position) => ({
    position,
    length: Math.min(sampleSize, fileSize - position),
  }));
}

async function hashLocalIdentitySamples(filePath, fileSize) {
  const samples = sampleOffsetsForIdentity(fileSize);
  if (samples.length === 0) return null;
  const hash = crypto.createHash("sha256");
  const handle = await fs.promises.open(filePath, "r");
  try {
    for (const { position, length } of samples) {
      const buffer = Buffer.allocUnsafe(length);
      const { bytesRead } = await handle.read(buffer, 0, length, position);
      if (bytesRead !== length) {
        throw new Error("Could not sample source content for resume identity");
      }
      hash.update(buffer);
      hash.update(`@${position}:${length};`);
    }
  } finally {
    await handle.close().catch(() => {});
  }
  return hash.digest("hex");
}

async function hashRemoteIdentitySamples(client, sftpId, filePath, encoding, fileSize) {
  if (isScpModeClient(client)) return null;
  const samples = sampleOffsetsForIdentity(fileSize);
  if (samples.length === 0) return null;
  await requireSftpChannel(client);
  const encodedPath = encodePathForSession(sftpId, filePath, encoding);
  if (typeof client.sftp?.createReadStream !== "function") return null;
  const hash = crypto.createHash("sha256");
  for (const { position, length } of samples) {
    const digest = await hashReadable(
      client.sftp.createReadStream(encodedPath, {
        start: position,
        end: position + length - 1,
      }),
    );
    hash.update(digest);
    hash.update(`@${position}:${length};`);
  }
  return hash.digest("hex");
}

async function computeSourceIdentityLite({ sourceType, sourcePath, sourceSftpId, sourceEncoding }) {
  if (sourceType === "local") {
    const st = await fs.promises.stat(sourcePath);
    const mtime = Number.isFinite(st.mtimeMs) ? Math.trunc(st.mtimeMs) : 0;
    const sample = await hashLocalIdentitySamples(sourcePath, st.size);
    return sample ? `meta:${st.size}:${mtime}:${sample}` : `meta:${st.size}:${mtime}`;
  }
  const client = sftpClients.get(sourceSftpId);
  if (!client) throw new Error("Source SFTP session not found");
  const attrs = isScpModeClient(client)
    ? await getScpBackendForClient(client).stat(sourcePath, { encoding: sourceEncoding })
    : await client.stat(encodePathForSession(sourceSftpId, sourcePath, sourceEncoding));
  const size = Math.max(0, Number(attrs?.size) || 0);
  // ssh2-sftp-client / SCP backends expose modifyTime (ms). Raw ssh2 attrs use
  // mtime in whole seconds (or mtimeMs). Prefer modifyTime first or remote
  // identity collapses to meta:size:0 and same-size rewrites false-pass.
  const mtimeRaw = attrs?.modifyTime ?? attrs?.mtimeMs ?? attrs?.mtime;
  // modifyTime is already ms; raw mtime is often seconds.
  const mtime = Number.isFinite(Number(mtimeRaw))
    ? Math.trunc(Number(mtimeRaw) > 1e12 ? Number(mtimeRaw) : Number(mtimeRaw) * 1000)
    : 0;
  const sample = await hashRemoteIdentitySamples(client, sourceSftpId, sourcePath, sourceEncoding, size);
  return sample ? `meta:${size}:${mtime}:${sample}` : `meta:${size}:${mtime}`;
}

async function computeMatchingSourceFingerprint(storedFingerprint, params) {
  if (typeof storedFingerprint === "string" && storedFingerprint.startsWith("meta:")) {
    return computeSourceIdentityLite(params);
  }
  return computeSourceFingerprint(params);
}

/**
 * meta: fingerprints gained an optional content-sample suffix. Legacy
 * meta:size:mtime values still match when size+mtime agree; full checkpoint
 * content verify (see resumeContentVerifyBytes) covers same-size rewrites.
 */
function sourceFingerprintsMatch(storedFingerprint, currentFingerprint) {
  if (!storedFingerprint || !currentFingerprint) return false;
  if (storedFingerprint === currentFingerprint) return true;
  if (
    typeof storedFingerprint !== "string"
    || typeof currentFingerprint !== "string"
    || !storedFingerprint.startsWith("meta:")
    || !currentFingerprint.startsWith("meta:")
  ) {
    return false;
  }
  const storedParts = storedFingerprint.split(":");
  const currentParts = currentFingerprint.split(":");
  if (storedParts.length === 3 && currentParts.length >= 3) {
    return storedParts[1] === currentParts[1] && storedParts[2] === currentParts[2];
  }
  return false;
}

async function hashRemotePrefix(client, sftpId, filePath, encoding, bytes) {
  if (!bytes) return null;
  if (isScpModeClient(client)) return null;
  await requireSftpChannel(client);
  const encodedPath = encodePathForSession(sftpId, filePath, encoding);
  return hashReadable(client.sftp.createReadStream(encodedPath, { start: 0, end: bytes - 1 }));
}

/**
 * Cap content re-hash on resume for strong (sha256:) fingerprints. Full-prefix
 * hashing of multi-MB .part files over SFTP can take longer than the remaining
 * transfer and freezes the UI at "transferring" with no byte movement.
 *
 * meta: identities are only size+mtime(+samples). Same-size rewrites past the
 * sample points / 256 KiB leading window still need a full checkpoint compare
 * before appending, or the staged file mixes two source versions.
 */
const RESUME_CONTENT_VERIFY_MAX_BYTES = 256 * 1024;

function resumeContentVerifyBytes(checkpoint, fingerprint) {
  const claimed = Math.max(0, Number(checkpoint) || 0);
  if (!claimed) return 0;
  // Always cap the leading-window hash. Hashing the full multi-MB checkpoint
  // over SFTP after force-quit continue freezes the transfer-center bar at the
  // resume offset (status "transferring", 0 B/s) for a long time with no
  // progress events. meta: fingerprints already encode size/mtime/samples;
  // a 256 KiB window is enough to catch mixed-stage corruption without stalling.
  void fingerprint;
  return Math.min(claimed, RESUME_CONTENT_VERIFY_MAX_BYTES);
}

async function assertMatchingResumeContent(sourceHashPromise, stagedHashPromise) {
  const [sourceHash, stagedHash] = await Promise.all([sourceHashPromise, stagedHashPromise]);
  if (sourceHash && stagedHash && sourceHash !== stagedHash) {
    throw new Error("Resume safety check failed: saved content does not match the source");
  }
}

function stableLocalFileIdentity(statLike) {
  if (!statLike) return null;
  return [statLike.dev, statLike.ino, statLike.size].join(":");
}

async function promoteLocalTransfer(stagedPath, targetPath, options = {}) {
  const assertNotCancelled = typeof options.assertNotCancelled === "function"
    ? options.assertNotCancelled
    : () => {};
  const token = crypto.randomUUID().replace(/-/g, "");
  const targetDir = path.dirname(targetPath);
  const targetBase = path.basename(targetPath);
  const readyPath = path.join(targetDir, `.${targetBase}.netcatty-${token}.ready`);
  const backupPath = path.join(targetDir, `.${targetBase}.netcatty-${token}.backup`);
  let preparedPath = stagedPath;
  let backedUp = false;
  let published = false;
  let publishedIdentity = null;
  try {
    assertNotCancelled();
    try {
      await fs.promises.rename(stagedPath, readyPath);
    } catch (err) {
      if (err?.code !== "EXDEV") throw err;
      await fs.promises.copyFile(stagedPath, readyPath);
      preparedPath = readyPath;
    }
    if (preparedPath !== readyPath) preparedPath = readyPath;
    let appliedMode = null;
    let targetStable = false;
    let expectedStableIdentity = null;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      const validatedTarget = typeof options.validateTarget === "function"
        ? await options.validateTarget()
        : null;
      const existingMode = Number.isInteger(validatedTarget?.existingMode)
        ? validatedTarget.existingMode & 0o7777
        : Number.isInteger(options.existingMode)
          ? options.existingMode & 0o7777
          : null;
      if (existingMode !== null && existingMode !== appliedMode) {
        await fs.promises.chmod(readyPath, existingMode);
        appliedMode = existingMode;
        continue;
      }
      expectedStableIdentity = validatedTarget?.stableIdentity
        || (validatedTarget?.targetIdentity
          ? String(validatedTarget.targetIdentity).split(":").slice(0, 3).join(":")
          : null);
      targetStable = true;
      break;
    }
    if (!targetStable) {
      throw new Error("Local download target kept changing before replacement");
    }
    assertNotCancelled();
    try {
      await fs.promises.rename(targetPath, backupPath);
      backedUp = true;
    } catch (err) {
      if (err?.code !== "ENOENT") throw err;
    }
    // Another process may have replaced the destination between validateTarget
    // and rename. Re-check the moved backup before publishing the download.
    if (backedUp && expectedStableIdentity) {
      let backupStat;
      try {
        backupStat = await fs.promises.lstat(backupPath);
      } catch (err) {
        throw new Error(
          `Local download target disappeared during replacement: ${targetPath}`,
          { cause: err },
        );
      }
      if (!backupStat.isFile() || stableLocalFileIdentity(backupStat) !== expectedStableIdentity) {
        // If another writer already recreated targetPath, do not restore the
        // mismatched backup over it — leave both intact and fail closed.
        let targetOccupied = false;
        try {
          await fs.promises.lstat(targetPath);
          targetOccupied = true;
        } catch (err) {
          if (err?.code !== "ENOENT") throw err;
        }
        if (targetOccupied) {
          const conflict = new Error("Local download target changed during replacement");
          conflict.leaveConcurrentTarget = true;
          conflict.remoteBackupPath = backupPath;
          backedUp = false;
          throw conflict;
        }
        try {
          await fs.promises.rename(backupPath, targetPath);
          backedUp = false;
        } catch (restoreErr) {
          const recoveryFailure = new Error(
            `Could not restore the original file after a concurrent replacement was detected. `
            + `Backup: ${backupPath}; target: ${targetPath}`,
            { cause: restoreErr },
          );
          recoveryFailure.recoveryFailed = true;
          throw recoveryFailure;
        }
        throw new Error("Local download target changed during replacement");
      }
    }
    // After the backup move, a concurrent writer may recreate targetPath. Never
    // clobber that file: leave it in place, keep the original in backupPath for
    // recovery, and fail closed before publishing the download.
    if (backedUp) {
      let recreated = false;
      try {
        await fs.promises.lstat(targetPath);
        recreated = true;
      } catch (err) {
        if (err?.code !== "ENOENT") throw err;
      }
      if (recreated) {
        const conflict = new Error("Local download target changed during replacement");
        conflict.leaveConcurrentTarget = true;
        conflict.remoteBackupPath = backupPath;
        // Prevent the catch path from renaming backup over the concurrent file.
        backedUp = false;
        throw conflict;
      }
    }
    assertNotCancelled();
    // Capture identity of the ready file before publish so rollback can refuse
    // to clobber a concurrent replacement of the published target.
    try {
      publishedIdentity = stableLocalFileIdentity(await fs.promises.lstat(readyPath));
    } catch {
      publishedIdentity = null;
    }
    await fs.promises.rename(readyPath, targetPath);
    published = true;
    assertNotCancelled();
    options.onCommit?.();
    if (backedUp) await fs.promises.unlink(backupPath).catch(() => {});
    if (stagedPath !== readyPath) await fs.promises.unlink(stagedPath).catch(() => {});
  } catch (err) {
    let restoreError = null;
    if (err?.leaveConcurrentTarget) {
      // Caller already decided not to touch a concurrent target/backup pair.
    } else if (backedUp) {
      let targetMatchesPublished = false;
      let targetMissing = false;
      try {
        const targetStat = await fs.promises.lstat(targetPath);
        targetMatchesPublished = !!(
          published
          && publishedIdentity
          && stableLocalFileIdentity(targetStat) === publishedIdentity
        );
      } catch (statErr) {
        if (statErr?.code === "ENOENT") targetMissing = true;
        else restoreError = statErr;
      }
      if (!restoreError) {
        if (published && !targetMissing && !targetMatchesPublished) {
          // Concurrent writer replaced our published file — keep both.
          err.leaveConcurrentTarget = true;
          err.remoteBackupPath = backupPath;
          backedUp = false;
        } else {
          if (published && targetMatchesPublished) {
            await fs.promises.unlink(targetPath).catch(() => {});
          }
          if (targetMissing || targetMatchesPublished || !published) {
            try {
              // Only restore when the path is free or still holds our publish.
              if (!published) {
                let occupied = false;
                try {
                  await fs.promises.lstat(targetPath);
                  occupied = true;
                } catch (occErr) {
                  if (occErr?.code !== "ENOENT") throw occErr;
                }
                if (occupied) {
                  err.leaveConcurrentTarget = true;
                  err.remoteBackupPath = backupPath;
                  backedUp = false;
                } else {
                  await fs.promises.rename(backupPath, targetPath);
                  backedUp = false;
                }
              } else {
                await fs.promises.rename(backupPath, targetPath);
                backedUp = false;
              }
            } catch (recoveryErr) {
              restoreError = recoveryErr;
            }
          }
        }
      }
    } else if (published) {
      try {
        const targetStat = await fs.promises.lstat(targetPath);
        if (publishedIdentity && stableLocalFileIdentity(targetStat) === publishedIdentity) {
          await fs.promises.unlink(targetPath);
        }
      } catch (recoveryErr) {
        if (recoveryErr?.code !== "ENOENT") restoreError = recoveryErr;
      }
    }
    if (restoreError) {
      const recoveryFailure = new Error(
        `Could not restore the original file after replacement failed. `
        + `Backup: ${backupPath}; prepared replacement: ${readyPath}; `
        + `original staged path: ${stagedPath}; target: ${targetPath}`,
        { cause: restoreError },
      );
      recoveryFailure.recoveryFailed = true;
      throw recoveryFailure;
    }
    await fs.promises.unlink(readyPath).catch(() => {});
    throw err;
  }
}

async function inspectLocalPromotionTarget(targetPath) {
  const absoluteTargetPath = path.resolve(targetPath);
  const visitedLinkStates = new Set();
  let linkDepth = 0;
  let currentRoot = path.parse(absoluteTargetPath).root;
  let currentPath = currentRoot;
  let pendingParts = absoluteTargetPath
    .slice(currentRoot.length)
    .split(path.sep)
    .filter(Boolean);

  while (pendingParts.length > 0) {
    const nextPart = pendingParts.shift();
    const candidatePath = path.join(currentPath, nextPart);
    let targetLstat;
    try {
      targetLstat = await fs.promises.lstat(candidatePath);
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
      return {
        promotionTargetPath: path.join(candidatePath, ...pendingParts),
        existingMode: null,
        targetIdentity: "missing",
      };
    }

    if (targetLstat.isSymbolicLink()) {
      linkDepth += 1;
      const linkState = `${candidatePath}\0${pendingParts.join("\0")}`;
      if (linkDepth > 40 || visitedLinkStates.has(linkState)) {
        const error = new Error(`Local download target contains a symbolic-link loop: ${targetPath}`);
        error.code = "ELOOP";
        throw error;
      }
      visitedLinkStates.add(linkState);
      const linkTarget = await fs.promises.readlink(candidatePath);
      const resolvedLinkTarget = path.resolve(path.dirname(candidatePath), linkTarget);
      currentRoot = path.parse(resolvedLinkTarget).root;
      currentPath = currentRoot;
      pendingParts = resolvedLinkTarget
        .slice(currentRoot.length)
        .split(path.sep)
        .filter(Boolean)
        .concat(pendingParts);
      continue;
    }

    if (pendingParts.length > 0) {
      if (!targetLstat.isDirectory()) {
        const error = new Error(`Local download target parent is not a directory: ${candidatePath}`);
        error.code = "ENOTDIR";
        throw error;
      }
      currentPath = candidatePath;
      continue;
    }

    if (!targetLstat.isFile()) {
      const error = new Error(`Local download target is not a regular file: ${candidatePath}`);
      error.code = targetLstat.isDirectory() ? "EISDIR" : "EINVAL";
      throw error;
    }
    return {
      promotionTargetPath: candidatePath,
      existingMode: targetLstat.mode & 0o7777,
      stableIdentity: stableLocalFileIdentity(targetLstat),
      targetIdentity: [
        targetLstat.dev,
        targetLstat.ino,
        targetLstat.size,
        targetLstat.mtimeMs,
        targetLstat.ctimeMs,
      ].join(":"),
    };
  }

  const rootStat = await fs.promises.lstat(currentPath);
  if (!rootStat.isFile()) {
    const error = new Error(`Local download target is not a regular file: ${currentPath}`);
    error.code = rootStat.isDirectory() ? "EISDIR" : "EINVAL";
    throw error;
  }
  return {
    promotionTargetPath: currentPath,
    existingMode: rootStat.mode & 0o7777,
    stableIdentity: stableLocalFileIdentity(rootStat),
    targetIdentity: [
      rootStat.dev,
      rootStat.ino,
      rootStat.size,
      rootStat.mtimeMs,
      rootStat.ctimeMs,
    ].join(":"),
  };
}

// ── Transfer performance tuning ──────────────────────────────────────────────
// Progress IPC throttle: sending too many IPC messages bogs down the event loop
const PROGRESS_THROTTLE_MS = 100;         // Send IPC at most every 100ms
const PROGRESS_THROTTLE_BYTES = 256 * 1024; // Or every 256KB of progress
const ISOLATED_DOWNLOAD_IDLE_TTL_MS = 5000;

// Speed calculation uses strict sliding-window average:
// speed = bytes_delta_in_window / time_delta_in_window
const SPEED_WINDOW_MS = 3000;             // Keep 3s of samples
const SPEED_MIN_ELAPSED_MS = 50;          // Minimum elapsed time to avoid divide-by-near-zero spikes

// Shared references
let sftpClients = null;

// Active transfers storage
const activeTransfers = new Map();
const admittedTransferQueue = [];
const pausedAdmittedTransfers = new Map();
const workerTransferLifecycleEpochs = new Map();
/** Transfer ids cancelled before startTransferNow registered them (skipAdmission open window). */
const pendingCancelTransferIds = new Set();
const admittedActiveByResource = new Map();
let admittedTransferLimit = 2;
const isolatedDownloadChannelPools = new WeakMap();
// Cache sftpIds where remote cp is known to be unavailable, so we skip
// repeated failed exec attempts for each file in a multi-file transfer.
const cpUnavailableSet = new Set();

const {
  sftpTransferSessionLeaseStore,
} = require("./sftpTransferSessionLease.cjs");

/**
 * Initialize the transfer bridge with dependencies
 */
function init(deps) {
  sftpClients = deps.sftpClients;
}

async function runTransferCancelablePreflight(transfer, operation) {
  if (transfer.cancelled || transfer.signal?.aborted) {
    throw new Error("Transfer cancelled");
  }
  let rejectCancellation;
  const cancelled = new Promise((_, reject) => {
    rejectCancellation = reject;
  });
  const previousAbort = transfer.abort;
  const abortPreflight = () => {
    try { previousAbort?.(); } finally {
      rejectCancellation(new Error("Transfer cancelled"));
    }
  };
  const signal = transfer.signal;
  transfer.abort = abortPreflight;
  signal?.addEventListener?.("abort", abortPreflight, { once: true });
  try {
    return await Promise.race([
      Promise.resolve().then(operation),
      cancelled,
    ]);
  } finally {
    signal?.removeEventListener?.("abort", abortPreflight);
    if (transfer.abort === abortPreflight) transfer.abort = previousAbort;
  }
}

async function runTransferAbortableOperation(transfer, operation) {
  if (transfer.cancelled || transfer.signal?.aborted) {
    throw new Error("Transfer cancelled");
  }
  const controller = new AbortController();
  const previousAbort = transfer.abort;
  const abortOperation = () => {
    try { previousAbort?.(); } finally {
      controller.abort(new Error("Transfer cancelled"));
    }
  };
  const signal = transfer.signal;
  transfer.abort = abortOperation;
  signal?.addEventListener?.("abort", abortOperation, { once: true });
  try {
    return await operation(controller.signal);
  } finally {
    signal?.removeEventListener?.("abort", abortOperation);
    if (transfer.abort === abortOperation) transfer.abort = previousAbort;
  }
}

function listTransferSftpIds(payload = {}) {
  return [...new Set(
    [payload.sourceSftpId, payload.targetSftpId].filter((id) => typeof id === "string" && id.length > 0),
  )];
}

function acquireTransferSessionLeases(transferId, payload) {
  const sftpIds = listTransferSftpIds(payload);
  for (const sftpId of sftpIds) {
    sftpTransferSessionLeaseStore.acquire(sftpId, transferId);
  }
  return sftpIds;
}

async function hardCloseSftpSession(sftpId) {
  if (!sftpId) return;
  // TOCTOU: another transfer may have acquired between shouldHardClose and here.
  // Re-arm soft-close and abort the force close so we don't kill live work.
  if (sftpTransferSessionLeaseStore.isHeld(sftpId)) {
    sftpTransferSessionLeaseStore.markSoftClosed(sftpId);
    return;
  }
  try {
    const sftpBridge = require("./sftpBridge.cjs");
    if (typeof sftpBridge.closeSftp === "function") {
      const result = await sftpBridge.closeSftp(null, { sftpId, force: true });
      // fileOps may defer if a new lease appeared mid-close.
      if (result?.deferred) return;
      return;
    }
  } catch (err) {
    console.warn(`[Transfer] Failed to hard-close leased SFTP session ${sftpId}:`, err?.message || err);
  }
  // Fallback if bridge close is unavailable (unit tests with partial mocks).
  // Re-check again before wiping the client map.
  if (sftpTransferSessionLeaseStore.isHeld(sftpId)) {
    sftpTransferSessionLeaseStore.markSoftClosed(sftpId);
    return;
  }
  const client = sftpClients?.get?.(sftpId);
  if (!client) {
    sftpTransferSessionLeaseStore.clear(sftpId);
    return;
  }
  try { await client.end?.(); } catch { /* ignore */ }
  if (sftpTransferSessionLeaseStore.isHeld(sftpId)) {
    sftpTransferSessionLeaseStore.markSoftClosed(sftpId);
    return;
  }
  sftpClients.delete(sftpId);
  sftpTransferSessionLeaseStore.clear(sftpId);
}

function releaseTransferSessionLeases(transferId, sftpIds) {
  for (const sftpId of sftpIds || []) {
    const result = sftpTransferSessionLeaseStore.release(sftpId, transferId);
    if (result.shouldHardClose) {
      void hardCloseSftpSession(sftpId);
    }
  }
}

function setGlobalTransferConcurrency(limit) {
  const normalized = Number(limit);
  if (Number.isInteger(normalized) && normalized >= 1 && normalized <= 16) {
    admittedTransferLimit = normalized;
  }
  return admittedTransferLimit;
}

function getGlobalTransferConcurrency() {
  return admittedTransferLimit;
}

/**
 * Execute an SSH command with cancellation support.
 * Registers an abort hook on the transfer object that closes the exec stream,
 * which sends SIGHUP to the remote process.
 */
function execSshCommandCancellable(sshClient, command, transfer) {
  return new Promise((resolve, reject) => {
    if (transfer.cancelled) return reject(new Error('Transfer cancelled'));

    sshClient.exec(command, (err, stream) => {
      if (err) return reject(err);

      // If cancelled between exec() call and callback, kill immediately
      if (transfer.cancelled) {
        try { stream.close(); } catch { }
        return reject(new Error('Transfer cancelled'));
      }

      let stdout = '';
      let stderr = '';

      // Wire abort: closing the stream kills the remote process
      const prevAbort = transfer.abort;
      transfer.abort = () => {
        try { stream.close(); } catch { }
        if (typeof prevAbort === 'function') prevAbort();
      };

      stream.on('close', (code) => {
        transfer.abort = prevAbort; // restore
        if (transfer.cancelled) return reject(new Error('Transfer cancelled'));
        resolve({ stdout, stderr, code });
      });

      stream.on('data', (data) => { stdout += data.toString(); });
      stream.stderr.on('data', (data) => { stderr += data.toString(); });
    });
  });
}

async function openIsolatedSftpChannel(client) {
  const sshClient = client?.client;
  if (!sshClient || typeof sshClient.sftp !== "function") return null;

  return new Promise((resolve, reject) => {
    sshClient.sftp((err, sftp) => {
      if (err) reject(err);
      else resolve(sftp);
    });
  });
}

/**
 * After concurrent ranges fail, staged files may extend past the contiguous
 * checkpoint (sparse tail). Truncate to the durable offset before stream
 * fallback or pause-stat, so resume never skips holes.
 */
async function truncateStagedPathToCheckpoint(filePath, checkpointBytes) {
  const checkpoint = Math.max(0, Number(checkpointBytes) || 0);
  if (!filePath) return;
  try {
    const stat = await fs.promises.stat(filePath);
    if (!stat.isFile()) return;
    if (stat.size > checkpoint) {
      await fs.promises.truncate(filePath, checkpoint);
    }
  } catch (error) {
    if (error?.code === "ENOENT") return;
    // Best-effort: stream fallback can still proceed from checkpoint.
    console.warn(
      "[transferBridge] failed to truncate staged local file before fallback:",
      error?.message || String(error),
    );
  }
}

async function truncateRemoteStagedToCheckpoint(client, stagedRemote, checkpointBytes) {
  const checkpoint = Math.max(0, Number(checkpointBytes) || 0);
  if (!client || !stagedRemote?.path) return;
  if (isScpModeClient(client)) return;
  try {
    const encoded = encodePathForSession(
      stagedRemote.sftpId,
      stagedRemote.path,
      stagedRemote.encoding,
    );
    const stat = await client.stat(encoded);
    const size = Math.max(0, Number(stat?.size) || 0);
    if (size <= checkpoint) return;
    // Prefer native truncate when available (ssh2-sftp-client).
    if (typeof client.truncate === "function") {
      await client.truncate(encoded, checkpoint);
      return;
    }
    if (typeof client.sftp?.ftruncate === "function" && typeof client.sftp?.open === "function") {
      await new Promise((resolve, reject) => {
        client.sftp.open(encoded, "r+", (openErr, handle) => {
          if (openErr) return reject(openErr);
          client.sftp.ftruncate(handle, checkpoint, (truncErr) => {
            client.sftp.close(handle, () => {
              if (truncErr) reject(truncErr);
              else resolve();
            });
          });
        });
      });
    }
  } catch (error) {
    console.warn(
      "[transferBridge] failed to truncate staged remote file before fallback:",
      error?.message || String(error),
    );
  }
}

async function prepareStreamFallbackAfterRangeFailure(transfer, client) {
  const checkpoint = Math.max(0, Number(transfer?.checkpointBytes) || 0);
  // Truncate partial *target* staging only. In SFTP→SFTP copies, stagedLocalPath
  // is the fully-downloaded temp *source* during the upload phase — never shrink it.
  // Direct transfers keep resumeStage "direct"; S2S download uses "download".
  // Anything other than "upload" with a staged local path is a partial target.
  const localIsPartialTarget =
    Boolean(transfer?.stagedLocalPath)
    && transfer.resumeStage !== "upload";
  if (localIsPartialTarget) {
    await truncateStagedPathToCheckpoint(transfer.stagedLocalPath, checkpoint);
  }
  if (transfer?.stagedRemote) {
    await truncateRemoteStagedToCheckpoint(
      transfer.stagedRemote.client || client,
      transfer.stagedRemote,
      checkpoint,
    );
  }
}

function getIsolatedDownloadChannelPool(client) {
  let pool = isolatedDownloadChannelPools.get(client);
  if (!pool) {
    pool = {
      idle: [],
      idleTimers: new Map(),
      idleErrorHandlers: new Map(),
      busy: new Set(),
      opening: 0,
      maxChannels: FAST_DOWNLOAD_CHANNELS_PER_SESSION,
    };
    isolatedDownloadChannelPools.set(client, pool);
  }
  return pool;
}

function removeIdleIsolatedDownloadChannel(pool, sftp) {
  const index = pool.idle.indexOf(sftp);
  if (index !== -1) {
    pool.idle.splice(index, 1);
  }
}

function clearIdleIsolatedDownloadTimer(pool, sftp) {
  const timer = pool.idleTimers.get(sftp);
  if (timer) {
    clearTimeout(timer);
    pool.idleTimers.delete(sftp);
  }
}

function clearIdleIsolatedDownloadErrorHandler(pool, sftp) {
  const handler = pool.idleErrorHandlers.get(sftp);
  if (!handler) return;
  try { sftp?.removeListener?.("error", handler); } catch { }
  pool.idleErrorHandlers.delete(sftp);
}

function scheduleIdleIsolatedDownloadChannel(client, sftp) {
  const pool = isolatedDownloadChannelPools.get(client);
  if (!pool) return;

  clearIdleIsolatedDownloadTimer(pool, sftp);
  const timer = setTimeout(() => {
    clearIdleIsolatedDownloadTimer(pool, sftp);
    clearIdleIsolatedDownloadErrorHandler(pool, sftp);
    removeIdleIsolatedDownloadChannel(pool, sftp);
    try { sftp?.end?.(); } catch { }
  }, ISOLATED_DOWNLOAD_IDLE_TTL_MS);
  pool.idleTimers.set(sftp, timer);

  const onIdleError = () => {
    clearIdleIsolatedDownloadTimer(pool, sftp);
    clearIdleIsolatedDownloadErrorHandler(pool, sftp);
    removeIdleIsolatedDownloadChannel(pool, sftp);
    try { sftp?.end?.(); } catch { }
  };
  pool.idleErrorHandlers.set(sftp, onIdleError);
  sftp?.once?.("error", onIdleError);
}

function releaseIsolatedDownloadChannel(client, sftp, options = {}) {
  const { dispose = false } = options;
  const pool = isolatedDownloadChannelPools.get(client);
  if (!pool) {
    if (dispose) {
      try { sftp?.end?.(); } catch { }
    }
    return;
  }

  pool.busy.delete(sftp);
  clearIdleIsolatedDownloadTimer(pool, sftp);
  clearIdleIsolatedDownloadErrorHandler(pool, sftp);

  if (dispose) {
    try { sftp?.end?.(); } catch { }
    return;
  }

  pool.idle.push(sftp);
  scheduleIdleIsolatedDownloadChannel(client, sftp);
}

async function acquireIsolatedDownloadChannel(client, transfer) {
  const pool = getIsolatedDownloadChannelPool(client);
  if (transfer?.cancelled) return null;

  const cached = pool.idle.pop();
  if (cached) {
    clearIdleIsolatedDownloadTimer(pool, cached);
    clearIdleIsolatedDownloadErrorHandler(pool, cached);
    pool.busy.add(cached);
    return cached;
  }

  const currentChannelCount = pool.idle.length + pool.busy.size + pool.opening;
  if (currentChannelCount >= pool.maxChannels) {
    return null;
  }

  pool.opening += 1;
  try {
    const opened = await openIsolatedSftpChannel(client);
    pool.opening -= 1;
    if (!opened) return null;
    if (transfer?.cancelled) {
      try { opened.end?.(); } catch { }
      return null;
    }
    pool.busy.add(opened);
    return opened;
  } catch (err) {
    pool.opening -= 1;
    console.warn(
      "[transferBridge] Failed to open isolated SFTP channel for fastGet, falling back to streams:",
      err.message || String(err),
    );
    return null;
  }
}

/**
 * After a concurrent-range attempt fails, staged remote bytes may extend past
 * the contiguous checkpoint. Truncate / reset so the next strategy resumes
 * safely instead of leaving sparse tails.
 */
async function prepareUploadFallbackCheckpoint(transfer, client, fileSize, sendProgress) {
  await prepareStreamFallbackAfterRangeFailure(transfer, client);
  const fallbackCheckpoint = Math.max(0, Number(transfer.checkpointBytes) || 0);
  if (fallbackCheckpoint > 0 && transfer.stagedRemote) {
    let stagedSize = Number.POSITIVE_INFINITY;
    try {
      const staged = transfer.stagedRemote;
      const encoded = encodePathForSession(staged.sftpId, staged.path, staged.encoding);
      stagedSize = Number((await (staged.client || client).stat(encoded))?.size);
    } catch { /* missing staged file — restart */ }
    if (!Number.isFinite(stagedSize) || stagedSize !== fallbackCheckpoint) {
      transfer.checkpointBytes = 0;
      sendProgress(0, fileSize, { force: true, checkpointBytes: 0 });
    }
  }
}

async function uploadViaFastPut(localPath, remotePath, sftp, fileSize, transfer, sendProgress, { disposeChannel }) {
  await new Promise((resolve, reject) => {
    let settled = false;
    let pendingError = null;
    let forceFinishTimer = null;
    let onFastSftpError = null;
    const clearForceFinish = () => {
      if (forceFinishTimer) {
        clearTimeout(forceFinishTimer);
        forceFinishTimer = null;
      }
    };
    const finish = (err) => {
      if (settled) return;
      settled = true;
      clearForceFinish();
      if (transfer.abort === abortFastTransfer) {
        transfer.abort = null;
      }
      if (onFastSftpError) {
        try { sftp.removeListener("error", onFastSftpError); } catch { /* ignore */ }
        onFastSftpError = null;
      }
      if (disposeChannel) {
        try { sftp.end(); } catch { /* ignore */ }
      }

      if (transfer.cancelled) reject(new Error("Transfer cancelled"));
      else if (err) reject(err);
      else resolve();
    };
    const scheduleForceFinish = (err) => {
      clearForceFinish();
      forceFinishTimer = setTimeout(() => finish(err), 2000);
    };
    const abortFastTransfer = () => {
      if (settled) return;
      transfer.cancelled = true;
      if (disposeChannel) {
        try { sftp.end(); } catch { /* ignore */ }
        // Wait for fastPut callback when possible; force after grace period.
        scheduleForceFinish(new Error("Transfer cancelled"));
        return;
      }
      // Shared channel: wait for callback.
    };
    transfer.abort = abortFastTransfer;
    onFastSftpError = (err) => {
      pendingError = err || new Error("SFTP channel error");
      if (disposeChannel) {
        try { sftp.end(); } catch { /* ignore */ }
        scheduleForceFinish(pendingError);
      }
    };
    sftp.on?.("error", onFastSftpError);

    if (transfer.cancelled) {
      finish(new Error("Transfer cancelled"));
      return;
    }

    sftp.fastPut(localPath, remotePath, {
      chunkSize: TRANSFER_CHUNK_SIZE,
      concurrency: UPLOAD_TRANSFER_CONCURRENCY,
      step: (transferred, _chunk, total) => {
        if (transfer.cancelled) return;
        // UI progress only. fastPut byte totals are not a durable contiguous
        // resume offset — keep checkpoint at 0 so a crash mid-fastPut cannot
        // resume past sparse holes on the next run.
        // Do not force every chunk: ssh2 steps per 32KB and would flood IPC.
        sendProgress(transferred, total || fileSize, {
          checkpointBytes: 0,
        });
      },
    }, (err) => {
      if (transfer.cancelled) {
        finish(new Error("Transfer cancelled"));
        return;
      }
      if (pendingError) {
        finish(pendingError);
        return;
      }
      finish(err || null);
    });
  });
}

/**
 * Upload a local file with pipelined SFTP WRITEs only.
 *
 * Aligns with OpenSSH sftp / Electerm / WinSCP: default is outstanding-request
 * fanout, not serial WriteStream. Strategy order:
 *   1. concurrent ranges on an isolated channel (resumable + cancel-safe)
 *   2. ssh2 fastPut on an isolated channel
 *   3. concurrent ranges on the shared browse channel
 *
 * There is no silent serial createWriteStream/put fallback — that path is
 * RTT-bound (~1 WRITE × 32KB) and was the usual cause of sub-MB/s uploads
 * (#2449). When every pipelined strategy fails, the transfer fails with the
 * last underlying error (Electerm-style: fail closed, do not crawl).
 */
async function uploadFile(
  localPath,
  remotePath,
  client,
  fileSize,
  transfer,
  sendProgress,
  encoding = "utf-8",
  onBytesCommitted = null,
) {
  if (isScpModeClient(client)) {
    transfer.pauseSupported = false;
    transfer.pauseUnavailableReason = "Pause is unavailable for SCP transfers";
    transfer.uploadStrategy = "scp";
    const backend = getScpBackendForClient(client);
    let scpSourcePath = localPath;
    let digestPath = null;
    let snapshotPath = null;
    let openReadStream = null;
    let initialSource = null;
    try {
      if (!transfer.sourceIsOwnedTemp) {
        initialSource = await fs.promises.stat(localPath);
        const snapshotId = crypto.createHash("sha256")
          .update(String(transfer.transferId || localPath))
          .digest("hex")
          .slice(0, 16);
        digestPath = tempDirBridge.getTransferTempFilePath(
          `upload-digest-${snapshotId}`,
          "ranges.sha256",
        );
        snapshotPath = tempDirBridge.getTransferTempFilePath(
          `upload-source-${snapshotId}`,
          "snapshot.bin",
        );
        await createUploadDigestBaseline(localPath, digestPath, fileSize, transfer);
        if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
        const sourceAfterBaseline = await fs.promises.stat(localPath);
        assertSourceMetadataUnchanged(initialSource, sourceAfterBaseline, fileSize);
        if (typeof onBytesCommitted === "function") {
          await createVerifiedUploadSnapshot(
            localPath,
            snapshotPath,
            digestPath,
            fileSize,
            transfer,
          );
          scpSourcePath = snapshotPath;
        } else {
          // Remote staging can safely discard a failed upload. Verify every
          // source chunk as SCP reads it, then rescan before promotion, without
          // requiring another full local copy of large files.
          snapshotPath = null;
          openReadStream = () => createVerifiedUploadReadStream(
            localPath,
            digestPath,
            fileSize,
            transfer,
          );
        }
      }
      await backend.uploadFile(scpSourcePath, remotePath, {
        fileSize,
        transfer,
        encoding,
        signal: transfer.signal,
        openReadStream,
        onProgress: (transferred, total) => sendProgress(transferred, total || fileSize),
      });
      if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
      if (digestPath && !snapshotPath) {
        const latestSource = await fs.promises.stat(localPath);
        assertSourceMetadataUnchanged(initialSource, latestSource, fileSize);
        await verifyUploadDigestBaseline(localPath, digestPath, fileSize, transfer);
      }
      onBytesCommitted?.();
      return;
    } finally {
      if (snapshotPath) await fs.promises.rm(snapshotPath, { force: true }).catch(() => {});
      if (digestPath) await fs.promises.rm(digestPath, { force: true }).catch(() => {});
    }
  }

  await requireSftpChannel(client);
  const sftp = client.sftp;
  if (!sftp) throw new Error("SFTP client not ready");
  transfer.pauseSupported = Boolean(transfer.resumable);
  const originalLocalPath = localPath;
  const initialSource = (transfer.resumable || !transfer.sourceIsOwnedTemp)
    ? await fs.promises.stat(originalLocalPath)
    : null;
  if (!transfer.sourceIsOwnedTemp) {
    const digestId = crypto.createHash("sha256")
      .update(String(transfer.transferId || "upload"))
      .digest("hex")
      .slice(0, 16);
    const digestPath = tempDirBridge.getTransferTempFilePath(
      `upload-digest-${digestId}`,
      "ranges.sha256",
    );
    transfer.sourceDigestPath = digestPath;
    await createUploadDigestBaseline(
      originalLocalPath,
      digestPath,
      fileSize,
      transfer,
    );
    if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
    const sourceAfterBaseline = await fs.promises.stat(originalLocalPath);
    assertSourceMetadataUnchanged(initialSource, sourceAfterBaseline, fileSize);
  }

  const cleanupSourceDigest = async () => {
    if (!transfer.sourceDigestPath) return;
    const digestPath = transfer.sourceDigestPath;
    transfer.sourceDigestPath = null;
    await fs.promises.rm(digestPath, { force: true }).catch(() => {});
  };

  const finishSuccessfulUpload = async () => {
    try {
      if (initialSource) {
        const latestSource = await fs.promises.stat(originalLocalPath);
        assertSourceMetadataUnchanged(initialSource, latestSource, fileSize);
      }
      // Metadata alone cannot catch same-size rewrites with unchanged/coarse
      // timestamps (e.g. all ranges already verified before the rewrite).
      // Re-scan the source against the digest baseline before promotion.
      if (transfer.sourceDigestPath) {
        await verifyUploadDigestBaseline(
          originalLocalPath,
          transfer.sourceDigestPath,
          fileSize,
          transfer,
        );
      }
      await assertRemoteUploadSize(client, remotePath, fileSize);
    } finally {
      await cleanupSourceDigest();
    }
  };

  /** @type {Error | null} */
  let lastPipelineError = null;
  const rememberPipelineError = (err) => {
    if (err && typeof err === "object") lastPipelineError = err;
    else lastPipelineError = new Error(String(err || "SFTP upload failed"));
  };

  // Prefer an isolated SFTP channel so cancellation cannot kill the browse session.
  if (!client.__netcattySudoMode) {
    let isolated = null;
    try {
      isolated = await openIsolatedSftpChannel(client);
    } catch (err) {
      rememberPipelineError(err);
      console.warn(
        "[transferBridge] Failed to open isolated SFTP channel for upload:",
        err.message || String(err),
      );
    }

    if (isolated) {
      let concurrentIsolatedOk = false;
      try {
        transfer.uploadStrategy = "concurrent-isolated";
        await uploadFileConcurrent(
          localPath,
          remotePath,
          isolated,
          fileSize,
          transfer,
          sendProgress,
          { disposeChannel: true, onBytesCommitted },
        );
        concurrentIsolatedOk = true;
      } catch (err) {
        // uploadFileConcurrent ends the isolated channel itself.
        isolated = null;
        if (transfer.cancelled) throw err;
        if (err?.noTransferFallback) throw err;
        rememberPipelineError(err);
        if (transfer.resumable) {
          await prepareUploadFallbackCheckpoint(transfer, client, fileSize, sendProgress);
        } else {
          transfer.checkpointBytes = 0;
        }
        console.warn(
          "[transferBridge] concurrent isolated upload failed, trying next pipelined strategy:",
          err?.message || String(err),
        );
      }
      // Verification errors must not fall through into other strategies.
      if (concurrentIsolatedOk) {
        await finishSuccessfulUpload();
        return;
      }
    }

    if (!isolated) {
      try {
        isolated = await openIsolatedSftpChannel(client);
      } catch (err) {
        rememberPipelineError(err);
        console.warn(
          "[transferBridge] Failed to reopen isolated SFTP channel for fastPut:",
          err.message || String(err),
        );
      }
    }

    // fastPut always truncates and rewrites from offset 0 — skip when we
    // already have a durable resume checkpoint from a prior concurrent attempt.
    // fastPut is not pause-aware; do not advertise pause while it runs.
    const hasResumeCheckpoint = Math.max(0, Number(transfer.checkpointBytes) || 0) > 0;
    if (
      isolated
      && typeof isolated.fastPut === "function"
      && !hasResumeCheckpoint
      && !transfer.resumable
    ) {
      let fastPutOk = false;
      let fastPutSourcePath = localPath;
      let fastPutSnapshotPath = null;
      try {
        transfer.uploadStrategy = "fastPut-isolated";
        transfer.pauseSupported = false;
        transfer.pauseUnavailableReason = "Pause is unavailable during fastPut upload";
        if (!transfer.sourceIsOwnedTemp) {
          const snapshotId = crypto.createHash("sha256")
            .update(String(transfer.transferId || localPath))
            .digest("hex")
            .slice(0, 16);
          fastPutSnapshotPath = tempDirBridge.getTransferTempFilePath(
            `upload-source-${snapshotId}`,
            "snapshot.bin",
          );
          await createVerifiedUploadSnapshot(
            originalLocalPath,
            fastPutSnapshotPath,
            transfer.sourceDigestPath,
            fileSize,
            transfer,
          );
          fastPutSourcePath = fastPutSnapshotPath;
        }
        sendProgress(Math.max(0, Number(transfer.checkpointBytes) || 0), fileSize, {
          force: true,
        });
        await uploadViaFastPut(
          fastPutSourcePath,
          remotePath,
          isolated,
          fileSize,
          transfer,
          sendProgress,
          { disposeChannel: true },
        );
        fastPutOk = true;
      } catch (err) {
        isolated = null;
        // Restore pause capability for subsequent pause-aware strategies.
        transfer.pauseSupported = Boolean(transfer.resumable);
        transfer.pauseUnavailableReason = transfer.resumable
          ? undefined
          : transfer.pauseUnavailableReason;
        if (transfer.cancelled) throw err;
        // Source-change / hard safety errors must not be retried on another path.
        if (err?.noTransferFallback || err?.sourceChanged) throw err;
        rememberPipelineError(err);
        // fastPut progress is not a durable contiguous checkpoint — reset so
        // concurrent-shared does not resume past holes left by the failed put.
        transfer.checkpointBytes = 0;
        sendProgress(0, fileSize, { force: true, checkpointBytes: 0 });
        console.warn(
          "[transferBridge] isolated fastPut failed, trying next pipelined strategy:",
          err?.message || String(err),
        );
      } finally {
        if (fastPutSnapshotPath) {
          await fs.promises.rm(fastPutSnapshotPath, { force: true }).catch(() => {});
        }
      }
      if (fastPutOk) {
        onBytesCommitted?.();
        await finishSuccessfulUpload();
        return;
      }
    } else if (isolated && typeof isolated.end === "function") {
      try { isolated.end(); } catch { /* ignore */ }
    }
  }

  // Concurrent WRITEs on the shared browse channel — still pipelined, does not
  // end the session on cancel/dispose (sudo mode and isolated-open failures).
  if (typeof sftp.open === "function" && typeof sftp.write === "function") {
    let sharedOk = false;
    try {
      transfer.uploadStrategy = "concurrent-shared";
      transfer.pauseSupported = Boolean(transfer.resumable);
      if (transfer.resumable) transfer.pauseUnavailableReason = undefined;
      await uploadFileConcurrent(
        localPath,
        remotePath,
        sftp,
        fileSize,
        transfer,
        sendProgress,
        { disposeChannel: false, onBytesCommitted },
      );
      sharedOk = true;
    } catch (err) {
      if (transfer.cancelled) throw err;
      if (err?.noTransferFallback) throw err;
      rememberPipelineError(err);
      await prepareUploadFallbackCheckpoint(transfer, client, fileSize, sendProgress);
      console.warn(
        "[transferBridge] concurrent shared upload failed (no serial stream fallback):",
        err?.message || String(err),
      );
    }
    if (sharedOk) {
      await finishSuccessfulUpload();
      return;
    }
  } else if (!lastPipelineError) {
    lastPipelineError = new Error(
      "SFTP session does not support pipelined WRITE (open/write missing)",
    );
  }

  // Fail closed — do not crawl via serial WriteStream (industry practice:
  // OpenSSH/Electerm/WinSCP keep outstanding-request fanout; they do not
  // silently degrade to 1-in-flight put on failure).
  // Main's #2458 pause/unpipe fix still applies to download/local stream paths.
  transfer.uploadStrategy = "failed";
  const cause = lastPipelineError;
  const message = cause?.message
    ? `SFTP pipelined upload failed: ${cause.message}`
    : "SFTP pipelined upload failed (no serial stream fallback)";
  const error = new Error(message, cause ? { cause } : undefined);
  if (cause?.code !== undefined) error.code = cause.code;
  if (cause?.noTransferFallback) error.noTransferFallback = true;
  await cleanupSourceDigest();
  throw error;
}

function openSftpHandle(sftp, filePath, flags) {
  return new Promise((resolve, reject) => {
    sftp.open(filePath, flags, (error, handle) => {
      if (error) reject(error);
      else resolve(handle);
    });
  });
}

function closeSftpHandle(sftp, handle) {
  return new Promise((resolve, reject) => {
    sftp.close(handle, (error) => {
      if (error) reject(error);
      else resolve();
    });
  });
}

async function readSftpRange(sftp, handle, buffer, position, length) {
  let received = 0;
  while (received < length) {
    const bytesRead = await new Promise((resolve, reject) => {
      sftp.read(
        handle,
        buffer,
        received,
        length - received,
        position + received,
        (error, count) => {
          if (error) reject(error);
          else resolve(Number(count) || 0);
        },
      );
    });
    if (bytesRead <= 0) {
      throw new Error("Download stream finished before the full source was received");
    }
    received += bytesRead;
  }
}

async function writeLocalRange(fileHandle, buffer, position, length) {
  let written = 0;
  while (written < length) {
    const result = await fileHandle.write(buffer, written, length - written, position + written);
    if (!result || result.bytesWritten <= 0) {
      throw new Error("Local download file stopped accepting data");
    }
    written += result.bytesWritten;
  }
}

async function readLocalRange(fileHandle, buffer, position, length) {
  let received = 0;
  while (received < length) {
    const result = await fileHandle.read(buffer, received, length - received, position + received);
    if (!result || result.bytesRead <= 0) {
      throw new Error("Upload source ended before the expected file size");
    }
    received += result.bytesRead;
  }
}

function writeSftpRange(sftp, handle, buffer, position, length) {
  return new Promise((resolve, reject) => {
    sftp.write(handle, buffer, 0, length, position, (error) => {
      if (error) reject(error);
      else resolve();
    });
  });
}

async function verifyFastDownloadSamples(sftp, remoteHandle, localHandle, fileSize, transfer) {
  if (fileSize <= 0) return;
  const sampleSize = Math.min(TRANSFER_CHUNK_SIZE, fileSize);
  const offsets = [...new Set([
    0,
    Math.max(0, Math.floor((fileSize - sampleSize) / 2)),
    Math.max(0, fileSize - sampleSize),
  ])];
  for (const position of offsets) {
    if (transfer.cancelled) throw new Error("Transfer cancelled");
    const length = Math.min(sampleSize, fileSize - position);
    const remoteBuffer = Buffer.allocUnsafe(length);
    const localBuffer = Buffer.allocUnsafe(length);
    await readSftpRange(sftp, remoteHandle, remoteBuffer, position, length);
    await readLocalRange(localHandle, localBuffer, position, length);
    if (!remoteBuffer.equals(localBuffer)) {
      const error = new Error("Transfer source content changed during transfer");
      error.noTransferFallback = true;
      error.sourceChanged = true;
      throw error;
    }
  }
}

function createSourceSizeChangedError(expectedSize, actualSize) {
  const error = new Error(
    `Transfer source size changed during transfer: expected ${expectedSize}, got ${actualSize}`,
  );
  error.noTransferFallback = true;
  error.sourceChanged = true;
  return error;
}

function createSourceContentChangedError() {
  const error = new Error("Transfer source content changed during transfer");
  error.noTransferFallback = true;
  error.sourceChanged = true;
  return error;
}

function isTransferCancelled(transfer) {
  return Boolean(transfer?.cancelled || transfer?.signal?.aborted);
}

const UPLOAD_DIGEST_SCAN_SIZE = TRANSFER_CHUNK_SIZE * 128;

async function assertUploadDigestCapacity(digestPath, fileSize) {
  if (typeof fs.promises.statfs !== "function") return;
  const requiredBytes = BigInt(Math.ceil(fileSize / TRANSFER_CHUNK_SIZE)) * 32n;
  let stats;
  try {
    stats = await fs.promises.statfs(path.dirname(digestPath), { bigint: true });
  } catch {
    return;
  }
  const availableBytes = BigInt(stats.bavail) * BigInt(stats.bsize);
  if (availableBytes < requiredBytes) {
    const error = new Error(
      `Not enough Netcatty temporary storage for upload verification: requires ${requiredBytes} bytes, ${availableBytes} bytes available`,
    );
    error.noTransferFallback = true;
    throw error;
  }
}

async function verifyUploadDigestBaseline(sourcePath, digestPath, fileSize, transfer) {
  let sourceHandle = null;
  let digestHandle = null;
  try {
    sourceHandle = await fs.promises.open(sourcePath, "r");
    digestHandle = await fs.promises.open(digestPath, "r");
    const buffer = Buffer.allocUnsafe(Math.min(UPLOAD_DIGEST_SCAN_SIZE, Math.max(1, fileSize)));
    let position = 0;
    let chunkIndex = 0;
    while (position < fileSize) {
      if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
      const length = Math.min(buffer.length, fileSize - position);
      await readLocalRange(sourceHandle, buffer, position, length);
      if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
      const digestCount = Math.ceil(length / TRANSFER_CHUNK_SIZE);
      const expected = Buffer.allocUnsafe(digestCount * 32);
      await readLocalRange(digestHandle, expected, chunkIndex * 32, expected.length);
      for (let offset = 0, index = 0; offset < length; offset += TRANSFER_CHUNK_SIZE, index += 1) {
        const actual = crypto.createHash("sha256")
          .update(buffer.subarray(offset, Math.min(offset + TRANSFER_CHUNK_SIZE, length)))
          .digest();
        if (!expected.subarray(index * 32, (index + 1) * 32).equals(actual)) {
          throw createSourceContentChangedError();
        }
      }
      position += length;
      chunkIndex += digestCount;
    }
  } finally {
    await sourceHandle?.close().catch(() => {});
    await digestHandle?.close().catch(() => {});
  }
}

async function createUploadDigestBaseline(sourcePath, digestPath, fileSize, transfer) {
  // A crashed attempt may have left this transfer's old baseline behind. It is
  // fully replaceable and its blocks must be reclaimable before capacity is
  // evaluated for the new baseline.
  await fs.promises.rm(digestPath, { force: true });
  await assertUploadDigestCapacity(digestPath, fileSize);
  let sourceHandle = null;
  let digestHandle = null;
  let completed = false;
  try {
    sourceHandle = await fs.promises.open(sourcePath, "r");
    digestHandle = await fs.promises.open(digestPath, "w");
    const buffer = Buffer.allocUnsafe(Math.min(UPLOAD_DIGEST_SCAN_SIZE, Math.max(1, fileSize)));
    let position = 0;
    let digestPosition = 0;
    while (position < fileSize) {
      if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
      const length = Math.min(buffer.length, fileSize - position);
      await readLocalRange(sourceHandle, buffer, position, length);
      if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
      const digestCount = Math.ceil(length / TRANSFER_CHUNK_SIZE);
      const digests = Buffer.allocUnsafe(digestCount * 32);
      for (let offset = 0, index = 0; offset < length; offset += TRANSFER_CHUNK_SIZE, index += 1) {
        crypto.createHash("sha256")
          .update(buffer.subarray(offset, Math.min(offset + TRANSFER_CHUNK_SIZE, length)))
          .digest()
          .copy(digests, index * 32);
      }
      let written = 0;
      while (written < digests.length) {
        const result = await digestHandle.write(
          digests,
          written,
          digests.length - written,
          digestPosition + written,
        );
        if (!result || result.bytesWritten <= 0) {
          throw new Error("Upload digest baseline stopped accepting data");
        }
        written += result.bytesWritten;
      }
      position += length;
      digestPosition += digests.length;
    }
    completed = true;
  } finally {
    await sourceHandle?.close().catch(() => {});
    await digestHandle?.close().catch(() => {});
    if (!completed) await fs.promises.rm(digestPath, { force: true }).catch(() => {});
  }

  await verifyUploadDigestBaseline(sourcePath, digestPath, fileSize, transfer);
}

async function createVerifiedUploadSnapshot(
  sourcePath,
  snapshotPath,
  digestPath,
  fileSize,
  transfer,
) {
  await fs.promises.rm(snapshotPath, { force: true });
  let sourceHandle = null;
  let digestHandle = null;
  let snapshotHandle = null;
  let completed = false;
  try {
    sourceHandle = await fs.promises.open(sourcePath, "r");
    digestHandle = await fs.promises.open(digestPath, "r");
    snapshotHandle = await fs.promises.open(snapshotPath, "w");
    const sourceStats = await sourceHandle.stat();
    let position = 0;
    while (position < fileSize) {
      if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
      const length = Math.min(UPLOAD_DIGEST_SCAN_SIZE, fileSize - position);
      const buffer = await readVerifiedUploadRange(
        sourceHandle,
        digestHandle,
        position,
        length,
        fileSize,
      );
      let written = 0;
      while (written < buffer.length) {
        const result = await snapshotHandle.write(
          buffer,
          written,
          buffer.length - written,
          position + written,
        );
        if (!result || result.bytesWritten <= 0) {
          throw new Error("Upload snapshot stopped accepting data");
        }
        written += result.bytesWritten;
      }
      position += length;
    }
    await snapshotHandle.chmod(sourceStats.mode & 0o7777);
    completed = true;
  } finally {
    await sourceHandle?.close().catch(() => {});
    await digestHandle?.close().catch(() => {});
    await snapshotHandle?.close().catch(() => {});
    if (!completed) await fs.promises.rm(snapshotPath, { force: true }).catch(() => {});
  }
}

function createVerifiedUploadReadStream(
  sourcePath,
  digestPath,
  fileSize,
  transfer,
) {
  let resolveCompleted;
  const completed = new Promise((resolve) => { resolveCompleted = resolve; });
  const stream = Readable.from((async function* verifiedUploadChunks() {
    let sourceHandle = null;
    let digestHandle = null;
    try {
      sourceHandle = await fs.promises.open(sourcePath, "r");
      digestHandle = await fs.promises.open(digestPath, "r");
      let position = 0;
      while (position < fileSize) {
        if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
        const length = Math.min(UPLOAD_DIGEST_SCAN_SIZE, fileSize - position);
        const chunk = await readVerifiedUploadRange(
          sourceHandle,
          digestHandle,
          position,
          length,
          fileSize,
        );
        if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
        yield chunk;
        position += length;
      }
    } finally {
      await sourceHandle?.close().catch(() => {});
      await digestHandle?.close().catch(() => {});
      resolveCompleted();
    }
  })());
  return { stream, completed };
}

async function readVerifiedUploadRange(
  localHandle,
  digestHandle,
  position,
  length,
  fileSize,
) {
  const output = Buffer.allocUnsafe(length);
  let outputOffset = 0;
  while (outputOffset < length) {
    const rangePosition = position + outputOffset;
    const chunkStart = Math.floor(rangePosition / TRANSFER_CHUNK_SIZE) * TRANSFER_CHUNK_SIZE;
    const chunkLength = Math.min(TRANSFER_CHUNK_SIZE, fileSize - chunkStart);
    const chunk = Buffer.allocUnsafe(chunkLength);
    await readLocalRange(localHandle, chunk, chunkStart, chunkLength);
    const expected = Buffer.allocUnsafe(32);
    const chunkIndex = Math.floor(chunkStart / TRANSFER_CHUNK_SIZE);
    const digestResult = await digestHandle.read(expected, 0, 32, chunkIndex * 32);
    if (digestResult.bytesRead !== 32) throw createSourceContentChangedError();
    const actual = crypto.createHash("sha256").update(chunk).digest();
    if (!expected.equals(actual)) throw createSourceContentChangedError();
    const chunkOffset = rangePosition - chunkStart;
    const copyLength = Math.min(length - outputOffset, chunkLength - chunkOffset);
    chunk.copy(output, outputOffset, chunkOffset, chunkOffset + copyLength);
    outputOffset += copyLength;
  }
  return output;
}

function assertSourceMetadataUnchanged(initialSource, latestSource, expectedSize) {
  const latestSize = Number(latestSource?.size);
  if (latestSize !== expectedSize) {
    throw createSourceSizeChangedError(expectedSize, latestSize);
  }
  // Soft signal only — content fingerprint is the durable check for same-size
  // rewrites. Keep metadata as a cheap early reject when timestamps move.
  const versionFields = ["mtimeMs", "ctimeMs", "mtime", "ctime", "ino"];
  const changed = versionFields.some((field) => {
    const before = Number(initialSource?.[field]);
    const after = Number(latestSource?.[field]);
    return Number.isFinite(before) && Number.isFinite(after) && before !== after;
  });
  if (changed) {
    throw createSourceContentChangedError();
  }
}

async function runPausableConcurrentRanges({
  transfer,
  fileSize,
  checkpoint,
  concurrency,
  copyRange,
  sendProgress,
  abortChannel,
  sftp = null,
  forceSettleOnError = false,
}) {
  let nextOffset = checkpoint;
  // Progress may complete out of order, but the resume checkpoint must never
  // advance past a range that has not durably finished yet.
  let transferred = checkpoint;
  let contiguousCheckpoint = checkpoint;
  const completedRanges = new Map();
  let active = 0;
  let settled = false;
  let terminalError = null;
  // Each entry: { resolve, timer? } — timer is the soft-drain force-resolve.
  let pauseResolvers = [];

  const cancelPauseWait = () => {
    const resolvers = pauseResolvers;
    pauseResolvers = [];
    for (const entry of resolvers) {
      if (entry.timer) clearTimeout(entry.timer);
      entry.resolve();
    }
  };

  const publishContiguousCheckpoint = (force = false) => {
    if (transfer.cancelled) return;
    // Keep transfer.checkpointBytes in lockstep so a soft-drained pause that
    // returns early still exposes the highest contiguous durable offset.
    transfer.checkpointBytes = contiguousCheckpoint;
    sendProgress(transferred, fileSize, {
      checkpointBytes: contiguousCheckpoint,
      ...(force ? { force: true } : {}),
    });
  };

  const settlePauseWaiters = () => {
    if (!transfer.paused || active !== 0 || pauseResolvers.length === 0) return;
    // Flush the contiguous offset before resolving pause so pauseTransfer
    // never has to re-stat a sparse staged file.
    publishContiguousCheckpoint(true);
    const resolvers = pauseResolvers;
    pauseResolvers = [];
    for (const entry of resolvers) {
      if (entry.timer) clearTimeout(entry.timer);
      entry.resolve();
    }
  };

  transfer.getActiveRangeCount = () => active;

  let onSftpError = null;

  try {
    await new Promise((resolve, reject) => {
      let forceFinishTimer = null;
      const clearForceFinish = () => {
        if (forceFinishTimer) {
          clearTimeout(forceFinishTimer);
          forceFinishTimer = null;
        }
      };
      const finish = (error) => {
        if (settled) return;
        if (error) terminalError = terminalError || error;
        if (active > 0) {
          // Only an isolated channel may force-settle after aborting the
          // subsystem. A shared channel cannot discard outstanding WRITE
          // callbacks safely because the caller may clean up or reuse the
          // same remote path while those requests are still in flight.
          if (terminalError && forceSettleOnError) {
            if (!forceFinishTimer) {
              forceFinishTimer = setTimeout(() => {
                if (settled) return;
                active = 0;
                settled = true;
                reject(terminalError || new Error("Transfer cancelled"));
              }, 2000);
              forceFinishTimer.unref?.();
            }
          }
          return;
        }
        clearForceFinish();
        settled = true;
        if (terminalError) reject(terminalError);
        else resolve();
      };

      const abort = (error = new Error("Transfer cancelled")) => {
        terminalError = terminalError || error;
        try { abortChannel?.(); } catch { /* ignore */ }
        finish(terminalError);
      };

      // Channel-level errors must not become unhandled EventEmitter crashes.
      if (sftp && typeof sftp.on === "function") {
        onSftpError = (error) => {
          abort(error || new Error("Isolated SFTP channel error"));
        };
        sftp.on("error", onSftpError);
      }

      const pump = () => {
        if (settled) return;
        if (terminalError || transfer.cancelled) {
          finish(terminalError || new Error("Transfer cancelled"));
          return;
        }
        if (transfer.paused) {
          settlePauseWaiters();
          return;
        }

        while (
          active < concurrency
          && nextOffset < fileSize
          && !transfer.paused
          && !transfer.cancelled
        ) {
          const position = nextOffset;
          const length = Math.min(
            TRANSFER_CHUNK_SIZE - (position % TRANSFER_CHUNK_SIZE),
            fileSize - position,
          );
          nextOffset += length;
          active += 1;

          void copyRange(position, length)
            .then(() => {
              transferred += length;
              completedRanges.set(position, position + length);
              while (completedRanges.has(contiguousCheckpoint)) {
                const nextCheckpoint = completedRanges.get(contiguousCheckpoint);
                completedRanges.delete(contiguousCheckpoint);
                contiguousCheckpoint = nextCheckpoint;
              }
              publishContiguousCheckpoint(false);
            })
            .catch((error) => abort(error))
            .finally(() => {
              active -= 1;
              settlePauseWaiters();
              if (terminalError || transfer.cancelled) {
                finish(terminalError || new Error("Transfer cancelled"));
              } else if (contiguousCheckpoint === fileSize) finish();
              else pump();
            });
        }

        if (fileSize === checkpoint && active === 0) finish();
      };

      transfer.readStream = {
        pause() {
          transfer.paused = true;
        },
        resume() {
          transfer.paused = false;
          pump();
        },
        destroy() {
          abort();
        },
      };
      transfer.waitForPause = () => {
        publishContiguousCheckpoint(true);
        if (active === 0) {
          return Promise.resolve();
        }
        // Soft-drain: resolve after a short grace even if some ranges are still
        // in flight. Contiguous checkpoint never advances past holes; leftover
        // ranges finish under paused=true without scheduling new work.
        if (PAUSE_RANGE_DRAIN_MS <= 0) {
          return Promise.resolve();
        }
        return new Promise((resolvePause) => {
          const entry = {
            resolve: resolvePause,
            timer: null,
          };
          entry.timer = setTimeout(() => {
            entry.timer = null;
            const index = pauseResolvers.indexOf(entry);
            if (index === -1) return;
            pauseResolvers.splice(index, 1);
            publishContiguousCheckpoint(true);
            resolvePause();
          }, PAUSE_RANGE_DRAIN_MS);
          entry.timer.unref?.();
          pauseResolvers.push(entry);
        });
      };
      transfer.cancelPauseWait = cancelPauseWait;
      transfer.abort = abort;
      pump();
    });
  } finally {
    transfer.getActiveRangeCount = null;
    if (sftp && onSftpError && typeof sftp.removeListener === "function") {
      try { sftp.removeListener("error", onSftpError); } catch { }
    }
  }
}

/**
 * Pipelined SFTP WRITE upload (same fanout as ssh2 fastPut).
 *
 * @param {{ disposeChannel?: boolean, onBytesCommitted?: (() => void) | null }} [options]
 *   disposeChannel — when true (isolated channel), end the SFTP subsystem on
 *   cancel/finish. When false (shared browse session), never call sftp.end().
 */
async function uploadFileConcurrent(
  localPath,
  remotePath,
  sftp,
  fileSize,
  transfer,
  sendProgress,
  options = {},
) {
  const disposeChannel = options.disposeChannel !== false;
  const checkpoint = Math.max(0, Math.min(transfer.checkpointBytes || 0, fileSize));
  let channelError = null;
  const onChannelError = (error) => {
    channelError = channelError || error;
  };
  sftp.on?.("error", onChannelError);
  // Install cancel before OPEN so a stalled remote open can still end an
  // isolated channel (runPausableConcurrentRanges would install this later).
  const abortChannel = () => {
    if (disposeChannel) {
      try { sftp.end?.(); } catch { /* ignore */ }
    }
  };
  const abortEarly = () => {
    transfer.cancelled = true;
    abortChannel();
  };
  transfer.abort = abortEarly;

  let localHandle = null;
  let digestHandle = null;
  let ephemeralDigestPath = null;
  let initialSource = null;
  let remoteHandle = null;
  let failed = false;
  let noTransferFallback = false;
  try {
    if (transfer.cancelled) throw new Error("Transfer cancelled");
    try {
      localHandle = await fs.promises.open(localPath, "r");
    } catch (error) {
      failed = true;
      noTransferFallback = true;
      const localOpenError = new Error(error?.message || String(error), { cause: error });
      localOpenError.noTransferFallback = true;
      throw localOpenError;
    }
    if (transfer.cancelled) throw new Error("Transfer cancelled");
    if (transfer.sourceDigestPath) {
      digestHandle = await fs.promises.open(transfer.sourceDigestPath, "r");
    } else if (!transfer.sourceIsOwnedTemp) {
      // Non-resumable range uploads do not receive uploadFile's persistent
      // digest sidecar. Build the same stable baseline here so every range is
      // verified immediately before its remote WRITE instead of relying on a
      // before/after whole-file fingerprint that temporary rewrites can evade.
      initialSource = await localHandle.stat();
      const digestId = crypto.createHash("sha256")
        .update(String(transfer.transferId || localPath))
        .digest("hex")
        .slice(0, 16);
      ephemeralDigestPath = tempDirBridge.getTransferTempFilePath(
        `upload-digest-${digestId}`,
        "ranges.sha256",
      );
      await createUploadDigestBaseline(localPath, ephemeralDigestPath, fileSize, transfer);
      if (transfer.cancelled) throw new Error("Transfer cancelled");
      const sourceAfterBaseline = await localHandle.stat();
      assertSourceMetadataUnchanged(initialSource, sourceAfterBaseline, fileSize);
      digestHandle = await fs.promises.open(ephemeralDigestPath, "r");
    }
    if (transfer.cancelled) throw new Error("Transfer cancelled");
    remoteHandle = await openSftpHandle(sftp, remotePath, checkpoint > 0 ? "r+" : "w");
    if (channelError) throw channelError;
    if (transfer.cancelled) throw new Error("Transfer cancelled");

    try {
      await runPausableConcurrentRanges({
        transfer,
        fileSize,
        checkpoint,
        concurrency: UPLOAD_TRANSFER_CONCURRENCY,
        copyRange: async (position, length) => {
          const buffer = digestHandle
            ? await readVerifiedUploadRange(
              localHandle,
              digestHandle,
              position,
              length,
              fileSize,
            )
            : await (async () => {
              const directBuffer = Buffer.allocUnsafe(length);
              await readLocalRange(localHandle, directBuffer, position, length);
              return directBuffer;
            })();
          if (transfer.cancelled) throw new Error("Transfer cancelled");
          await writeSftpRange(sftp, remoteHandle, buffer, position, length);
        },
        sendProgress,
        abortChannel,
        sftp,
        forceSettleOnError: disposeChannel,
      });
      if (channelError) throw channelError;
      // Every remote WRITE has completed. In-place destinations are already
      // published at this point, so stop accepting cancellation before source
      // revalidation and handle cleanup; staged uploads pass no callback.
      options.onBytesCommitted?.();
      if (initialSource) {
        const latestSource = await localHandle.stat();
        assertSourceMetadataUnchanged(initialSource, latestSource, fileSize);
      }
      if (ephemeralDigestPath) {
        await verifyUploadDigestBaseline(localPath, ephemeralDigestPath, fileSize, transfer);
      }
    } catch (error) {
      failed = true;
      throw error;
    }
  } catch (error) {
    failed = true;
    if (noTransferFallback && error && typeof error === "object") {
      error.noTransferFallback = true;
    }
    throw error;
  } finally {
    transfer.readStream = null;
    transfer.waitForPause = null;
    transfer.cancelPauseWait = null;
    transfer.abort = null;
    if (localHandle) {
      await localHandle.close().catch(() => {});
    }
    if (digestHandle) {
      await digestHandle.close().catch(() => {});
    }
    if (ephemeralDigestPath) {
      await fs.promises.rm(ephemeralDigestPath, { force: true }).catch(() => {});
    }
    let remoteCloseError = null;
    // Close remote handles while the channel is still live. On disposeChannel
    // cancel/failure the channel is about to be ended (or already dead); a
    // CLOSE request would hang forever with no callback from ssh2.
    if (remoteHandle) {
      const skipClose = disposeChannel && (failed || transfer.cancelled);
      if (!skipClose) {
        try {
          await Promise.race([
            closeSftpHandle(sftp, remoteHandle),
            new Promise((_, reject) => {
              setTimeout(() => reject(new Error("SFTP close timed out")), 2000);
            }),
          ]);
        } catch (error) {
          if (!failed && !transfer.cancelled && !/timed out/i.test(error?.message || "")) {
            remoteCloseError = error;
          }
        }
      }
    }
    if (!failed && !transfer.cancelled && !remoteCloseError && channelError) {
      remoteCloseError = channelError;
    }
    if (disposeChannel) {
      try { sftp.end?.(); } catch { /* ignore */ }
    }
    try { sftp.removeListener?.("error", onChannelError); } catch { /* ignore */ }
    if (remoteCloseError) throw remoteCloseError;
  }
}

/**
 * Preserve fastGet's high-latency request window without giving up a safe
 * pause checkpoint. Progress and resume offsets track the highest contiguous
 * durable byte; out-of-order range completion cannot advance past a hole.
 * Once pause is requested, no new ranges are scheduled and we wait for every
 * in-flight range before acknowledging it.
 */
async function downloadFileResumableFast(
  remotePath,
  localPath,
  sftp,
  fileSize,
  transfer,
  sendProgress,
) {
  const checkpoint = Math.max(0, Math.min(transfer.checkpointBytes || 0, fileSize));
  let channelError = null;
  const onChannelError = (error) => {
    channelError = channelError || error;
  };
  sftp.on?.("error", onChannelError);
  // Install cancel before OPEN so a stalled remote open can still end the
  // isolated channel (runPausableConcurrentRanges would install this later).
  const abortEarly = () => {
    transfer.cancelled = true;
    try { sftp.end?.(); } catch { }
  };
  transfer.abort = abortEarly;

  let remoteHandle = null;
  let localHandle = null;
  let failed = false;
  try {
    if (transfer.cancelled) throw new Error("Transfer cancelled");
    remoteHandle = await openSftpHandle(sftp, remotePath, "r");
    if (channelError) throw channelError;
    if (transfer.cancelled) throw new Error("Transfer cancelled");
    localHandle = await fs.promises.open(localPath, checkpoint > 0 ? "r+" : "w+");
    if (transfer.cancelled) throw new Error("Transfer cancelled");

    try {
      await runPausableConcurrentRanges({
        transfer,
        fileSize,
        checkpoint,
        concurrency: DOWNLOAD_TRANSFER_CONCURRENCY,
        copyRange: async (position, length) => {
          const buffer = Buffer.allocUnsafe(length);
          await readSftpRange(sftp, remoteHandle, buffer, position, length);
          await writeLocalRange(localHandle, buffer, position, length);
        },
        sendProgress,
        abortChannel: () => sftp.end?.(),
        sftp,
        forceSettleOnError: true,
      });
      if (channelError) throw channelError;
      await verifyFastDownloadSamples(sftp, remoteHandle, localHandle, fileSize, transfer);
    } catch (error) {
      failed = true;
      throw error;
    }
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    transfer.readStream = null;
    transfer.waitForPause = null;
    transfer.cancelPauseWait = null;
    transfer.abort = null;
    let localCloseError = null;
    if (localHandle) {
      try {
        await localHandle.close();
      } catch (error) {
        localCloseError = error;
      }
    }
    let remoteCloseError = null;
    if (remoteHandle && !failed && !transfer.cancelled) {
      try {
        await closeSftpHandle(sftp, remoteHandle);
      } catch (error) {
        remoteCloseError = error;
      }
    }
    if (!failed && !transfer.cancelled && !remoteCloseError && channelError) {
      remoteCloseError = channelError;
    }
    try { sftp.removeListener?.("error", onChannelError); } catch { }
    if (!failed && !transfer.cancelled && localCloseError) {
      const error = new Error("Could not safely close the downloaded file", { cause: localCloseError });
      error.noTransferFallback = true;
      throw error;
    }
    if (!failed && !transfer.cancelled && remoteCloseError) {
      const error = new Error("The isolated SFTP channel failed while closing", { cause: remoteCloseError });
      error.completedWithUnhealthyChannel = true;
      throw error;
    }
  }
}

async function downloadFile(
  remotePath,
  localPath,
  client,
  fileSize,
  transfer,
  sendProgress,
  encoding = "utf-8",
  runCancelablePreflight = async (operation) => operation(),
) {
  if (isScpModeClient(client)) {
    transfer.pauseSupported = false;
    transfer.pauseUnavailableReason = "Pause is unavailable for SCP transfers";
    const backend = getScpBackendForClient(client);
    return backend.downloadFile(remotePath, localPath, {
      fileSize,
      transfer,
      encoding,
      signal: transfer.signal,
      onProgress: (transferred, total) => sendProgress(transferred, total || fileSize),
    });
  }

  await requireSftpChannel(client);
  const sftp = client.sftp;
  if (!sftp) throw new Error("SFTP client not ready");
  transfer.pauseSupported = Boolean(transfer.resumable);
  const initialSource = transfer.resumable
    ? await runCancelablePreflight(() => client.stat(remotePath))
    : null;

  // Prefer an isolated SFTP channel so cancellation cannot kill the browse session.
  if (!client.__netcattySudoMode) {
    const fastSftp = await acquireIsolatedDownloadChannel(client, transfer);
    if (transfer.cancelled) {
      if (fastSftp) {
        releaseIsolatedDownloadChannel(client, fastSftp, { dispose: true });
      }
      throw new Error("Transfer cancelled");
    }

    if (fastSftp && (transfer.resumable || typeof fastSftp.fastGet === "function")) {
      try {
        if (transfer.resumable) {
          await downloadFileResumableFast(
            remotePath,
            localPath,
            fastSftp,
            fileSize,
            transfer,
            sendProgress,
          );
          const latestSource = await runCancelablePreflight(() => client.stat(remotePath));
          assertSourceMetadataUnchanged(initialSource, latestSource, fileSize);
          releaseIsolatedDownloadChannel(client, fastSftp);
          return;
        }
        await new Promise((resolve, reject) => {
          let settled = false;
          let onFastSftpError = null;
          const finish = (err) => {
            if (settled) return;
            settled = true;
            if (transfer.abort === abortFastTransfer) {
              transfer.abort = null;
            }
            if (onFastSftpError) {
              try { fastSftp.removeListener("error", onFastSftpError); } catch { }
              onFastSftpError = null;
            }
            releaseIsolatedDownloadChannel(client, fastSftp, {
              dispose: !!err || transfer.cancelled,
            });

            if (transfer.cancelled) reject(new Error("Transfer cancelled"));
            else if (err) reject(err);
            else resolve();
          };
          const abortFastTransfer = () => {
            if (settled) return;
            transfer.cancelled = true;
            finish(new Error("Transfer cancelled"));
          };
          transfer.abort = abortFastTransfer;
          onFastSftpError = (err) => finish(err);
          fastSftp.once("error", onFastSftpError);

          if (transfer.cancelled) {
            finish(new Error("Transfer cancelled"));
            return;
          }

          fastSftp.fastGet(remotePath, localPath, {
            chunkSize: TRANSFER_CHUNK_SIZE,
            concurrency: DOWNLOAD_TRANSFER_CONCURRENCY,
            step: (transferred, _chunk, total) => {
              if (transfer.cancelled) return;
              sendProgress(transferred, total || fileSize);
            },
          }, finish);
        });
        return;
      } catch (err) {
        // Always release before rethrowing cancel — otherwise the channel stays
        // in pool.busy and the per-session fast-download budget is exhausted.
        releaseIsolatedDownloadChannel(client, fastSftp, { dispose: true });
        if (transfer.cancelled) throw err;
        if (err?.noTransferFallback) throw err;
        if (err?.completedWithUnhealthyChannel) {
          const latestSource = await runCancelablePreflight(() => client.stat(remotePath));
          assertSourceMetadataUnchanged(initialSource, latestSource, fileSize);
          return;
        }
        // Concurrent ranges may leave sparse tails past the contiguous
        // checkpoint; truncate the actual local target before stream resume.
        const checkpoint = Math.max(0, Math.min(transfer.checkpointBytes || 0, fileSize));
        try {
          await fs.promises.truncate(localPath, checkpoint);
        } catch (truncateError) {
          if (!(checkpoint === 0 && truncateError?.code === "ENOENT")) {
            throw truncateError;
          }
        }
        sendProgress(checkpoint, fileSize, { force: true, checkpointBytes: checkpoint });
        console.warn(
          "[transferBridge] fastGet failed, falling back to a compatible stream:",
          err?.message || String(err),
        );
      }
    } else if (fastSftp) {
      // Acquired a channel but cannot use fast path — return it to the pool.
      releaseIsolatedDownloadChannel(client, fastSftp);
    }
  }

  // Fallback: sequential stream piping
  await new Promise((resolve, reject) => {
    const checkpoint = Math.max(0, Math.min(transfer.checkpointBytes || 0, fileSize));
    const readStream = sftp.createReadStream(remotePath, { highWaterMark: TRANSFER_CHUNK_SIZE, start: checkpoint });
    const writeStream = fs.createWriteStream(localPath, {
      highWaterMark: TRANSFER_CHUNK_SIZE,
      flags: checkpoint > 0 ? "r+" : "w",
      start: checkpoint,
    });
    let transferred = checkpoint;
    let finished = false;

    transfer.readStream = readStream;
    transfer.writeStream = writeStream;
    if (transfer.paused) {
      try { readStream.pause(); } catch { }
      transfer.streamsUnpiped = true;
    } else {
      readStream.pipe(writeStream);
      transfer.streamsUnpiped = false;
    }

    const cleanup = (err) => {
      if (finished) return;
      finished = true;
      readStream.removeAllListeners();
      writeStream.removeAllListeners();
      if (err) {
        try { readStream.destroy(); } catch { }
        try { writeStream.destroy(); } catch { }
        reject(err);
      } else {
        resolve();
      }
    };

    readStream.on('data', (chunk) => {
      if (transfer.cancelled) { cleanup(new Error('Transfer cancelled')); return; }
      transferred += chunk.length;
      sendProgress(transferred, fileSize);
    });
    readStream.on('error', cleanup);
    writeStream.on('error', cleanup);
    writeStream.on('finish', () => {
      if (transfer.cancelled) {
        cleanup(new Error('Transfer cancelled'));
      } else if (!readStream.readableEnded || transferred !== fileSize) {
        cleanup(new Error('Download stream finished before the full source was received'));
      } else {
        cleanup(null);
      }
    });
    writeStream.on('close', () => {
      if (transfer.cancelled) cleanup(new Error('Transfer cancelled'));
    });
  });
  if (initialSource) {
    const latestSource = await runCancelablePreflight(() => client.stat(remotePath));
    assertSourceMetadataUnchanged(initialSource, latestSource, fileSize);
  }
}

/**
 * Start a file transfer
 */
async function startTransferNow(event, payload, onProgress) {
  const {
    transferId,
    sourcePath,
    targetPath,
    sourceType,
    targetType,
    sourceSftpId,
    targetSftpId,
    totalBytes,
    sourceEncoding,
    targetEncoding,
    sameHost,
  } = payload;
  const sender = event.sender;

  // Cancel may have won the race during open/reconnect before we register.
  if (transferId && pendingCancelTransferIds.has(transferId)) {
    pendingCancelTransferIds.delete(transferId);
    sender.send?.("netcatty:transfer:cancelled", { transferId });
    return { transferId, error: "Transfer cancelled", cancelled: true };
  }

  sender.send?.("netcatty:transfer:started", { transferId });

  const transfer = {
    transferId,
    cancelled: false,
    paused: false,
    lifecycleEpoch: Math.max(0, Number(payload.lifecycleEpoch) || 0),
    lifecycleState: "transferring",
    pauseSupported: false,
    pauseUnavailableReason: "This transfer cannot be paused safely",
    resumable: payload.resumable === true,
    checkpointBytes: Math.max(0, Number(payload.checkpointBytes) || 0),
    resumeStage: payload.resumeStage || 'direct',
    downloadCheckpointBytes: Math.max(0, Number(payload.downloadCheckpointBytes) || 0),
    uploadCheckpointBytes: Math.max(0, Number(payload.uploadCheckpointBytes) || 0),
    sourceFingerprint: payload.sourceFingerprint,
    sourceType,
    targetType,
    sourcePath,
    targetPath,
    sourceSftpId,
    targetSftpId,
    sourceEncoding,
    targetEncoding,
    readStream: null,
    writeStream: null,
    // True after unpipe (or when open skipped pipe while paused). Guards
    // resumeStreamPair against duplicate pipe() which doubles writes.
    streamsUnpiped: false,
    abort: null,
    sourceDigestPath: null,
    sourceIsOwnedTemp: payload.sourceIsOwnedTemp === true,
    signal: null,
  };
  // Own an AbortController so cancelTransfer always stops cancelable preflight
  // work (destination hashing, probes) even when callers pass no abortSignal.
  const ownedAbort = new AbortController();
  if (payload.abortSignal) {
    if (payload.abortSignal.aborted) {
      ownedAbort.abort(payload.abortSignal.reason);
    } else {
      payload.abortSignal.addEventListener(
        "abort",
        () => {
          try { ownedAbort.abort(payload.abortSignal.reason); } catch { /* ignore */ }
        },
        { once: true },
      );
    }
  }
  transfer.signal = ownedAbort.signal;
  transfer.abortOwnedSignal = () => {
    try { ownedAbort.abort(); } catch { /* ignore */ }
  };
  activeTransfers.set(transferId, transfer);
  // Hold panel/agent SFTP sessions for the full transfer lifetime (including
  // pause). Panel close becomes a soft-close until we release these leases.
  const leasedSftpIds = acquireTransferSessionLeases(transferId, payload);
  transfer.leasedSftpIds = leasedSftpIds;
  const transferCreatedAt = Date.now();

  const runCancelablePreflight = (operation) => runTransferCancelablePreflight(transfer, operation);

  // ── Progress/speed tracking ──────────────────────────────────────────────
  // Keep progress monotonic and compute speed from a strict sliding window.
  const speedSamples = [{ time: transferCreatedAt, bytes: transfer.checkpointBytes }]; // [{ time, bytes }]
  let lastObservedTransferred = transfer.checkpointBytes;
  let lastObservedTotal = Math.max(0, totalBytes || 0);
  let lastProgressSentTime = 0;
  let lastProgressSentBytes = -1;

  const computeWindowSpeed = (now, transferred) => {
    const targetTime = now - SPEED_WINDOW_MS;

    // Keep exactly one sample before targetTime for boundary interpolation.
    while (speedSamples.length >= 2 && speedSamples[1].time <= targetTime) {
      speedSamples.shift();
    }

    const first = speedSamples[0];
    if (!first) return 0;

    let boundaryTime = first.time;
    let boundaryBytes = first.bytes;

    if (speedSamples.length >= 2 && targetTime > first.time) {
      const next = speedSamples[1];
      const range = next.time - first.time;
      if (range > 0) {
        const ratio = (targetTime - first.time) / range;
        boundaryBytes = first.bytes + (next.bytes - first.bytes) * ratio;
        boundaryTime = targetTime;
      }
    }

    const elapsedMs = now - boundaryTime;
    if (elapsedMs < SPEED_MIN_ELAPSED_MS) return 0;

    const deltaBytes = transferred - boundaryBytes;
    if (deltaBytes <= 0) return 0;

    const speed = (deltaBytes * 1000) / elapsedMs;
    return Number.isFinite(speed) && speed > 0 ? Math.round(speed) : 0;
  };

  const emitProgress = (now, transferred, total, speed, force = false) => {
    const isComplete = total > 0 && transferred >= total;
    const transferredChanged = transferred !== lastProgressSentBytes;
    const timeSinceLast = now - lastProgressSentTime;
    const bytesSinceLast = transferred - lastProgressSentBytes;

    if (
      force ||
      isComplete ||
      (transferredChanged &&
        (timeSinceLast >= PROGRESS_THROTTLE_MS || bytesSinceLast >= PROGRESS_THROTTLE_BYTES))
    ) {
      lastProgressSentTime = now;
      lastProgressSentBytes = transferred;
      const progressPayload = {
        transferId,
        transferred,
        speed,
        totalBytes: total,
        checkpointBytes: transfer.checkpointBytes,
        resumeStage: transfer.resumeStage,
        downloadCheckpointBytes: transfer.downloadCheckpointBytes,
        uploadCheckpointBytes: transfer.uploadCheckpointBytes,
        sourceFingerprint: transfer.sourceFingerprint,
        lifecycleEpoch: transfer.lifecycleEpoch,
        lifecycleState: transfer.lifecycleState,
        resumable: transfer.resumable && transfer.pauseSupported,
        // Only surface a reason when pause is actually unavailable; never keep
        // the startup default once pauseSupported is true.
        pauseUnavailableReason: transfer.pauseSupported
          ? undefined
          : transfer.pauseUnavailableReason,
      };
      sender.send("netcatty:transfer:progress", progressPayload);
      // Global center must not depend on the panel still being mounted.
      broadcastGlobalTransferEvent({
        type: "progress",
        transferId,
        transferred,
        totalBytes: total,
        speed,
        checkpointBytes: transfer.checkpointBytes,
        resumeStage: transfer.resumeStage,
        downloadCheckpointBytes: transfer.downloadCheckpointBytes,
        uploadCheckpointBytes: transfer.uploadCheckpointBytes,
        sourceFingerprint: transfer.sourceFingerprint,
        lifecycleEpoch: transfer.lifecycleEpoch,
        lifecycleState: transfer.lifecycleState,
        sourceHostId: transfer.sourceHostId || payload?.sourceHostId,
        targetHostId: transfer.targetHostId || payload?.targetHostId,
      });
    }
  };

  let leasesReleased = false;
  const cleanupTransfer = () => {
    activeTransfers.delete(transferId);
    if (!leasesReleased) {
      leasesReleased = true;
      releaseTransferSessionLeases(transferId, transfer.leasedSftpIds || leasedSftpIds);
      transfer.leasedSftpIds = [];
    }
  };

  const sendProgress = (transferred, total, options = {}) => {
    if (transfer.cancelled) return;

    const now = Date.now();
    const force = options.force === true;

    let normalizedTotal = Number.isFinite(total) && total > 0 ? total : 0;
    if (normalizedTotal === 0) {
      normalizedTotal = lastObservedTotal || 0;
    }
    normalizedTotal = Math.max(normalizedTotal, lastObservedTotal, 0);

    let normalizedTransferred = Number.isFinite(transferred) && transferred > 0 ? transferred : 0;
    // Explicit force (resume clamp / restart) may lower the durable offset.
    if (!force) {
      if (normalizedTotal > 0) {
        normalizedTransferred = Math.min(normalizedTransferred, normalizedTotal);
      }
      normalizedTransferred = Math.max(normalizedTransferred, lastObservedTransferred);
    } else if (normalizedTotal > 0) {
      normalizedTransferred = Math.min(normalizedTransferred, normalizedTotal);
    }

    lastObservedTransferred = normalizedTransferred;
    lastObservedTotal = normalizedTotal;
    const requestedCheckpoint = Number(options.checkpointBytes);
    transfer.checkpointBytes = Number.isFinite(requestedCheckpoint) && requestedCheckpoint >= 0
      ? Math.min(requestedCheckpoint, normalizedTotal || requestedCheckpoint)
      : normalizedTransferred;

    const lastSample = speedSamples[speedSamples.length - 1];
    if (!lastSample || lastSample.bytes !== normalizedTransferred || now - lastSample.time >= PROGRESS_THROTTLE_MS) {
      speedSamples.push({ time: now, bytes: normalizedTransferred });
    }

    const speed = computeWindowSpeed(now, normalizedTransferred);

    if (onProgress) {
      onProgress(normalizedTransferred, normalizedTotal, speed);
    }

    emitProgress(now, normalizedTransferred, normalizedTotal, speed, force);
  };

  transfer.publishCurrentProgress = () => sendProgress(
    lastObservedTransferred,
    lastObservedTotal,
    { force: true, checkpointBytes: transfer.checkpointBytes },
  );

  const sendComplete = () => {
    sender.send("netcatty:transfer:complete", { transferId });
    broadcastGlobalTransferEvent({
      type: "completed",
      transferId,
      endedAt: Date.now(),
      transferred: lastObservedTransferred,
      totalBytes: lastObservedTotal,
    });
    cleanupTransfer();
  };

  const sendError = (error) => {
    cleanupTransfer();
    const message = error?.message || String(error);
    sender.send("netcatty:transfer:error", { transferId, error: message });
    const cancelled = /cancel/i.test(message);
    broadcastGlobalTransferEvent({
      type: cancelled ? "cancelled" : "failed",
      transferId,
      endedAt: Date.now(),
      error: message,
    });
  };

  try {
    let fileSize = totalBytes || 0;

    if (!fileSize) {
      if (sourceType === 'local') {
        const stat = await fs.promises.stat(sourcePath);
        fileSize = stat.size;
      } else if (sourceType === 'sftp') {
        const client = sftpClients.get(sourceSftpId);
        if (!client) throw new Error("Source SFTP session not found");
        if (isScpModeClient(client)) {
          const st = await getScpBackendForClient(client).stat(sourcePath, {
            encoding: resolveEncodingForRequest(sourceSftpId, sourceEncoding),
            signal: transfer.signal,
          });
          fileSize = st.size;
        } else {
          const stat = await runCancelablePreflight(async () => {
            await requireSftpChannel(client);
            const encodedSourcePath = encodePathForSession(sourceSftpId, sourcePath, sourceEncoding);
            return client.stat(encodedSourcePath);
          });
          fileSize = stat.size;
        }
      }
    }

    const sourceClient = sourceType === "sftp" ? sftpClients.get(sourceSftpId) : null;
    const targetClient = targetType === "sftp" ? sftpClients.get(targetSftpId) : null;
    if ((sourceClient && isScpModeClient(sourceClient)) || (targetClient && isScpModeClient(targetClient))) {
      transfer.resumable = false;
      transfer.pauseSupported = false;
      transfer.pauseUnavailableReason = "Pause is unavailable for SCP transfers; cancel and retry from the beginning instead";
    } else {
      transfer.pauseSupported = Boolean(transfer.resumable);
      // Clear the default "cannot be paused safely" once we know pause works.
      if (transfer.pauseSupported) {
        transfer.pauseUnavailableReason = undefined;
      }
    }

    if (transfer.resumable && transfer.sourceFingerprint) {
      const currentSourceFingerprint = await runCancelablePreflight(() => computeMatchingSourceFingerprint(
        transfer.sourceFingerprint,
        {
          sourceType,
          sourcePath,
          sourceSftpId,
          sourceEncoding,
        },
      ));
      if (
        transfer.sourceFingerprint
        && currentSourceFingerprint
        && !sourceFingerprintsMatch(transfer.sourceFingerprint, currentSourceFingerprint)
      ) {
        throw new Error("Resume safety check failed: the source file has changed");
      }
    }

    sendProgress(transfer.checkpointBytes, fileSize, { force: true });

    if (sourceType === 'local' && targetType === 'sftp') {
      const client = sftpClients.get(targetSftpId);
      if (!client) throw new Error("Target SFTP session not found");

      const dir = path.dirname(targetPath).replace(/\\/g, '/');
      try {
        await runTransferAbortableOperation(transfer, (signal) => ensureRemoteDirForSession(
          targetSftpId,
          dir,
          targetEncoding,
          { signal },
        ));
      } catch (error) {
        if (transfer.cancelled || transfer.signal?.aborted) throw new Error("Transfer cancelled");
      }

      const resolvedTargetEncoding = resolveEncodingForRequest(targetSftpId, targetEncoding);
      const deterministicStagePath = buildRemoteTransferStagePath(targetPath, transferId);
      await runRemoteUploadTransaction(client, sourcePath, targetPath, {
        encoding: resolvedTargetEncoding,
        expectedSize: fileSize,
        stagedPath: transfer.resumable ? deterministicStagePath : undefined,
        allowInPlaceFallback: !transfer.resumable,
        preserveStageOnUploadError: transfer.resumable,
        signal: transfer.signal,
        assertCanPromote() {
          if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
        },
        commitPromotion() {
          transfer.completionCommitted = true;
        },
        runCancelablePreflight,
        async uploadFile(encodedUploadPath, uploadTarget) {
          const uploadTargetPath = uploadTarget.logicalPath;
          const usesStage = uploadTarget.generatedStagePath === true;
          transfer.stagedRemote = usesStage
            ? { client, sftpId: targetSftpId, path: uploadTargetPath, encoding: targetEncoding }
            : null;
          transfer.checkpointBytes = usesStage && transfer.resumable
            ? await runCancelablePreflight(() => resolveRemoteResumeCheckpoint(
                  client,
                  targetSftpId,
                  uploadTargetPath,
                  targetEncoding,
                  transfer.checkpointBytes,
                ))
            : 0;
          sendProgress(transfer.checkpointBytes, fileSize, { force: true });
          if (usesStage && transfer.checkpointBytes > 0) {
            const verifyBytes = resumeContentVerifyBytes(
              transfer.checkpointBytes,
              transfer.sourceFingerprint,
            );
            await runCancelablePreflight(() => assertMatchingResumeContent(
                hashLocalPrefix(sourcePath, verifyBytes),
                hashRemotePrefix(client, targetSftpId, uploadTargetPath, targetEncoding, verifyBytes),
              ));
          }
          await uploadFile(
            sourcePath,
            encodedUploadPath,
            client,
            fileSize,
            transfer,
            sendProgress,
            resolvedTargetEncoding,
            usesStage ? null : () => { transfer.completionCommitted = true; },
          );
        },
      });
      transfer.stagedRemote = null;

    } else if (sourceType === 'sftp' && targetType === 'local') {
      const client = sftpClients.get(sourceSftpId);
      if (!client) throw new Error("Source SFTP session not found");

      const dir = path.dirname(targetPath);
      await ensureLocalDir(dir);

      const encodedSourcePath = isScpModeClient(client)
        ? sourcePath
        : encodePathForSession(sourceSftpId, sourcePath, sourceEncoding);
      // SCP cannot resume, but it must still stage locally so a failed/cancelled
      // overwrite never truncates or removes the existing destination.
      const stageLocalDownload = transfer.resumable || isScpModeClient(client);
      const downloadTargetPath = stageLocalDownload
        ? tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath))
        : targetPath;
      transfer.stagedLocalPath = stageLocalDownload ? downloadTargetPath : null;
      transfer.checkpointBytes = await resolveLocalResumeCheckpoint(
        downloadTargetPath, transfer.checkpointBytes,
      );
      sendProgress(transfer.checkpointBytes, fileSize, { force: true });
      await runCancelablePreflight(async () => {
        const verifyBytes = resumeContentVerifyBytes(
          transfer.checkpointBytes,
          transfer.sourceFingerprint,
        );
        await assertMatchingResumeContent(
          hashRemotePrefix(client, sourceSftpId, sourcePath, sourceEncoding, verifyBytes),
          hashLocalPrefix(downloadTargetPath, verifyBytes),
        );
      });
      const downloadResult = await downloadFile(
        encodedSourcePath,
        downloadTargetPath,
        client,
        fileSize,
        transfer,
        sendProgress,
        resolveEncodingForRequest(sourceSftpId, sourceEncoding),
        runCancelablePreflight,
      );
      if (
        isScpModeClient(client)
        && Number.isFinite(downloadResult?.fileSize)
        && downloadResult.fileSize >= 0
      ) {
        // SCP follows symlinks. Its wire header is authoritative for the bytes
        // received, while the preflight shell stat describes the link node.
        fileSize = downloadResult.fileSize;
        lastObservedTotal = fileSize;
        lastObservedTransferred = Math.min(lastObservedTransferred, fileSize);
      }
      if (stageLocalDownload) {
        if (transfer.cancelled) throw new Error("Transfer cancelled");
        const stagedStat = await fs.promises.stat(downloadTargetPath);
        if (transfer.cancelled) throw new Error("Transfer cancelled");
        if (stagedStat.size !== fileSize) {
          throw new Error(`Downloaded file size mismatch: expected ${fileSize}, got ${stagedStat.size}`);
        }
        if (transfer.cancelled) throw new Error("Transfer cancelled");
        const {
          promotionTargetPath,
          existingMode,
          targetIdentity,
        } = await inspectLocalPromotionTarget(targetPath);
        if (transfer.cancelled) throw new Error("Transfer cancelled");
        await promoteLocalTransfer(downloadTargetPath, promotionTargetPath, {
          existingMode,
          async validateTarget() {
            const latestTarget = await inspectLocalPromotionTarget(targetPath);
            if (latestTarget.promotionTargetPath !== promotionTargetPath) {
              throw new Error("Local download target changed before replacement");
            }
            if (latestTarget.targetIdentity !== targetIdentity) {
              throw new Error("Local download target changed before replacement");
            }
            return latestTarget;
          },
          assertNotCancelled() {
            if (transfer.cancelled) throw new Error("Transfer cancelled");
          },
          onCommit() {
            transfer.completionCommitted = true;
          },
        });
        transfer.stagedLocalPath = null;
      }

    } else if (sourceType === 'local' && targetType === 'local') {
      const dir = path.dirname(targetPath);
      await ensureLocalDir(dir);
      const localTargetPath = transfer.resumable
        ? tempDirBridge.getTransferTempFilePath(transferId, path.basename(targetPath))
        : targetPath;
      transfer.stagedLocalPath = transfer.resumable ? localTargetPath : null;
      const checkpoint = Math.max(
        0,
        Math.min(
          await resolveLocalResumeCheckpoint(localTargetPath, transfer.checkpointBytes || 0),
          fileSize,
        ),
      );
      transfer.checkpointBytes = checkpoint;
      sendProgress(checkpoint, fileSize, { force: true });
      {
        const verifyBytes = resumeContentVerifyBytes(checkpoint, transfer.sourceFingerprint);
        await assertMatchingResumeContent(
          hashLocalPrefix(sourcePath, verifyBytes),
          hashLocalPrefix(localTargetPath, verifyBytes),
        );
      }

      await new Promise((resolve, reject) => {
        transfer.pauseSupported = Boolean(transfer.resumable);
        const readStream = fs.createReadStream(sourcePath, { highWaterMark: TRANSFER_CHUNK_SIZE, start: checkpoint });
        const writeStream = fs.createWriteStream(localTargetPath, {
          highWaterMark: TRANSFER_CHUNK_SIZE,
          flags: checkpoint > 0 ? "r+" : "w",
          start: checkpoint,
        });
        let transferred = checkpoint;
        let finished = false;

        transfer.readStream = readStream;
        transfer.writeStream = writeStream;
        if (transfer.paused) {
          try { readStream.pause(); } catch { }
          transfer.streamsUnpiped = true;
        } else {
          readStream.pipe(writeStream);
          transfer.streamsUnpiped = false;
        }

        const cleanup = (err) => {
          if (finished) return;
          finished = true;
          readStream.removeAllListeners();
          writeStream.removeAllListeners();
          if (err) {
            try { readStream.destroy(); } catch { }
            try { writeStream.destroy(); } catch { }
            reject(err);
          } else {
            resolve();
          }
        };

        readStream.on('data', (chunk) => {
          if (transfer.cancelled) { cleanup(new Error('Transfer cancelled')); return; }
          transferred += chunk.length;
          sendProgress(transferred, fileSize);
        });
        readStream.on('error', cleanup);
        writeStream.on('error', cleanup);
        writeStream.on('finish', () => {
          if (transfer.cancelled) {
            cleanup(new Error('Transfer cancelled'));
          } else if (!readStream.readableEnded || transferred !== fileSize) {
            cleanup(new Error('Local copy finished before the full source was read'));
          } else {
            cleanup(null);
          }
        });
        writeStream.on('close', () => {
          if (transfer.cancelled) cleanup(new Error('Transfer cancelled'));
        });
      });
      if (transfer.resumable && transfer.stagedLocalPath) {
        await promoteLocalTransfer(transfer.stagedLocalPath, targetPath, {
          assertNotCancelled() {
            if (transfer.cancelled) throw new Error("Transfer cancelled");
          },
          onCommit() {
            transfer.completionCommitted = true;
          },
        });
        transfer.stagedLocalPath = null;
      }

    } else if (sourceType === 'sftp' && targetType === 'sftp') {
      // Try same-host optimization first: remote cp via SSH exec.
      // Falls back to download+upload if cp is unavailable (e.g. Windows SSH servers).
      let sameHostDone = false;
      const resolvedSourceEnc = sourceSftpId ? resolveEncodingForRequest(sourceSftpId, sourceEncoding) : sourceEncoding;
      const resolvedTargetEnc = targetSftpId ? resolveEncodingForRequest(targetSftpId, targetEncoding) : targetEncoding;
      if (!transfer.resumable
        && sameHost
        && (!resolvedSourceEnc || resolvedSourceEnc === 'utf-8')
        && (!resolvedTargetEnc || resolvedTargetEnc === 'utf-8')
        && !cpUnavailableSet.has(sourceSftpId)) {
        const srcClient = sftpClients.get(sourceSftpId);
        const sshClient = srcClient?.client;
        if (sshClient && typeof sshClient.exec === 'function') {
          try {
            const dir = path.dirname(targetPath).replace(/\\/g, '/');
            try {
              await runTransferAbortableOperation(transfer, (signal) => ensureRemoteDirForSession(
                sourceSftpId,
                dir,
                targetEncoding || sourceEncoding,
                { signal },
              ));
            } catch (error) {
              if (transfer.cancelled || transfer.signal?.aborted) throw new Error("Transfer cancelled");
            }

            const escapedSource = sourcePath.replace(/'/g, "'\\''");
            const escapedTarget = targetPath.replace(/'/g, "'\\''");
            const command = `cp -a '${escapedSource}' '${escapedTarget}'`;

            const result = await execSshCommandCancellable(sshClient, command, transfer);
            if (result.code === 0) {
              sendProgress(fileSize, fileSize);
              sameHostDone = true;
            } else if (result.code === 127) {
              // Exit 127 = command not found — cache to skip future attempts
              cpUnavailableSet.add(sourceSftpId);
            }
            // Other non-zero exits (permission denied, disk full, etc.)
            // fall through to download+upload without caching
          } catch (cpErr) {
            // If cancelled, re-throw; otherwise fall back to download+upload
            if (transfer.cancelled) throw cpErr;
          }
        }
      }

      if (!sameHostDone) {
        const tempPath = tempDirBridge.getTransferTempFilePath(transferId, path.basename(sourcePath));
        transfer.stagedLocalPath = tempPath;

        const sourceClient = sftpClients.get(sourceSftpId);
        const targetClient = sftpClients.get(targetSftpId);
        if (!sourceClient) throw new Error("Source SFTP session not found");
        if (!targetClient) throw new Error("Target SFTP session not found");

        if (transfer.resumeStage !== 'upload') {
          transfer.resumeStage = 'download';
          transfer.downloadCheckpointBytes = await resolveLocalResumeCheckpoint(
            tempPath, transfer.downloadCheckpointBytes,
          );
          transfer.checkpointBytes = transfer.downloadCheckpointBytes;
          lastObservedTransferred = Math.floor(transfer.downloadCheckpointBytes / 2);
          // sendProgress stores overall UI bytes on checkpointBytes — restore the
          // stage offset afterward so downloadFile resumes at the durable point.
          sendProgress(lastObservedTransferred, fileSize, { force: true });
          transfer.checkpointBytes = transfer.downloadCheckpointBytes;
          const encodedSourcePath = isScpModeClient(sourceClient)
            ? sourcePath
            : encodePathForSession(sourceSftpId, sourcePath, sourceEncoding);
          await runCancelablePreflight(async () => {
            const verifyBytes = resumeContentVerifyBytes(
              transfer.downloadCheckpointBytes,
              transfer.sourceFingerprint,
            );
            await assertMatchingResumeContent(
              hashRemotePrefix(sourceClient, sourceSftpId, sourcePath, sourceEncoding, verifyBytes),
              hashLocalPrefix(tempPath, verifyBytes),
            );
          });
          const downloadProgress = (transferred, reportedTotal, options = {}) => {
            if (
              isScpModeClient(sourceClient)
              && Number.isFinite(reportedTotal)
              && reportedTotal >= 0
              && reportedTotal !== fileSize
            ) {
              // SCP's first data event carries the followed file's wire size.
              // Adopt it before mapping download progress onto the first half
              // of an S2S transfer, otherwise a short link node shows 100% and
              // then appears to move backwards when the real size arrives.
              fileSize = reportedTotal;
              lastObservedTotal = fileSize;
              lastObservedTransferred = Math.min(lastObservedTransferred, Math.floor(transferred / 2));
            }
            const durableCheckpoint = Number.isFinite(options.checkpointBytes)
              ? options.checkpointBytes
              : transferred;
            transfer.downloadCheckpointBytes = durableCheckpoint;
            sendProgress(Math.floor(transferred / 2), fileSize, {
              checkpointBytes: durableCheckpoint,
              force: options.force === true,
            });
            transfer.checkpointBytes = durableCheckpoint;
          };
          const downloadResult = await downloadFile(
            encodedSourcePath,
            tempPath,
            sourceClient,
            fileSize,
            transfer,
            downloadProgress,
            resolveEncodingForRequest(sourceSftpId, sourceEncoding),
            runCancelablePreflight,
          );
          if (
            isScpModeClient(sourceClient)
            && Number.isFinite(downloadResult?.fileSize)
            && downloadResult.fileSize >= 0
          ) {
            fileSize = downloadResult.fileSize;
            lastObservedTotal = fileSize;
            lastObservedTransferred = Math.min(lastObservedTransferred, fileSize);
          }
        }

        const localStageStat = await fs.promises.stat(tempPath);
        if (localStageStat.size !== fileSize) {
          throw new Error(`Server copy download size mismatch: expected ${fileSize}, got ${localStageStat.size}`);
        }

        if (transfer.cancelled) {
          try { await fs.promises.unlink(tempPath); } catch { }
          throw new Error('Transfer cancelled');
        }

        const dir = path.dirname(targetPath).replace(/\\/g, '/');
        try {
          await runTransferAbortableOperation(transfer, (signal) => ensureRemoteDirForSession(
            targetSftpId,
            dir,
            targetEncoding,
            { signal },
          ));
        } catch (error) {
          if (transfer.cancelled || transfer.signal?.aborted) throw new Error("Transfer cancelled");
        }

        transfer.resumeStage = 'upload';
        const uploadProgress = (transferred, _total, options = {}) => {
          const durableCheckpoint = Number.isFinite(options.checkpointBytes)
            ? options.checkpointBytes
            : transferred;
          transfer.uploadCheckpointBytes = durableCheckpoint;
          sendProgress(Math.floor(fileSize / 2) + Math.floor(transferred / 2), fileSize, {
            checkpointBytes: durableCheckpoint,
            force: options.force === true,
          });
          transfer.checkpointBytes = durableCheckpoint;
        };
        transfer.sourceIsOwnedTemp = true;
        const resolvedTargetEncoding = resolveEncodingForRequest(targetSftpId, targetEncoding);
        const deterministicStagePath = buildRemoteTransferStagePath(targetPath, transferId);
        await runRemoteUploadTransaction(targetClient, tempPath, targetPath, {
          encoding: resolvedTargetEncoding,
          expectedSize: fileSize,
          stagedPath: transfer.resumable ? deterministicStagePath : undefined,
          allowInPlaceFallback: !transfer.resumable,
          preserveStageOnUploadError: transfer.resumable,
          signal: transfer.signal,
          assertCanPromote() {
            if (isTransferCancelled(transfer)) throw new Error("Transfer cancelled");
          },
          commitPromotion() {
            transfer.completionCommitted = true;
          },
          runCancelablePreflight,
          async uploadFile(encodedUploadPath, uploadTarget) {
            const uploadTargetPath = uploadTarget.logicalPath;
            const usesStage = uploadTarget.generatedStagePath === true;
            transfer.stagedRemote = usesStage
              ? { client: targetClient, sftpId: targetSftpId, path: uploadTargetPath, encoding: targetEncoding }
              : null;
            transfer.uploadCheckpointBytes = usesStage && transfer.resumable
              ? await runCancelablePreflight(() => resolveRemoteResumeCheckpoint(
                    targetClient,
                    targetSftpId,
                    uploadTargetPath,
                    targetEncoding,
                    transfer.uploadCheckpointBytes,
                  ))
              : 0;
            transfer.checkpointBytes = transfer.uploadCheckpointBytes;
            // Overall progress for R2R upload stage is ~50% + upload/2.
            lastObservedTransferred = Math.floor(fileSize / 2)
              + Math.floor(transfer.uploadCheckpointBytes / 2);
            sendProgress(lastObservedTransferred, fileSize, { force: true });
            transfer.checkpointBytes = transfer.uploadCheckpointBytes;
            if (usesStage && transfer.uploadCheckpointBytes > 0) {
              const verifyBytes = resumeContentVerifyBytes(
                transfer.uploadCheckpointBytes,
                transfer.sourceFingerprint,
              );
              await runCancelablePreflight(() => assertMatchingResumeContent(
                  hashLocalPrefix(tempPath, verifyBytes),
                  hashRemotePrefix(
                    targetClient,
                    targetSftpId,
                    uploadTargetPath,
                    targetEncoding,
                    verifyBytes,
                  ),
                ));
            }
            await uploadFile(
              tempPath,
              encodedUploadPath,
              targetClient,
              fileSize,
              transfer,
              uploadProgress,
              resolvedTargetEncoding,
              usesStage ? null : () => { transfer.completionCommitted = true; },
            );
          },
        });
        transfer.stagedRemote = null;

        try { await fs.promises.unlink(tempPath); } catch { }
        transfer.stagedLocalPath = null;
      }

    } else {
      throw new Error("Invalid transfer configuration");
    }

    sendProgress(fileSize, fileSize);
    sendComplete();

    return { transferId, totalBytes: fileSize };
  } catch (err) {
    if (transfer.sourceDigestPath) {
      try { await fs.promises.rm(transfer.sourceDigestPath, { force: true }); } catch { }
      transfer.sourceDigestPath = null;
    }
    if (err?.sourceChanged) {
      if (transfer.stagedLocalPath) {
        try { await fs.promises.rm(transfer.stagedLocalPath, { force: true }); } catch { }
        transfer.stagedLocalPath = null;
      }
      if (transfer.stagedRemote) {
        const staged = transfer.stagedRemote;
        try {
          if (isScpModeClient(staged.client)) {
            await getScpBackendForClient(staged.client).remove(staged.path, {
              recursive: false,
              encoding: staged.encoding,
            });
          } else {
            await staged.client.delete(encodePathForSession(staged.sftpId, staged.path, staged.encoding));
          }
        } catch { }
        transfer.stagedRemote = null;
      }
    }
    if (!err?.recoveryFailed && (transfer.cancelled || err.message === 'Transfer cancelled')) {
      if (transfer.stagedLocalPath) {
        try { await fs.promises.unlink(transfer.stagedLocalPath); } catch { }
      }
      if (transfer.stagedRemote) {
        const staged = transfer.stagedRemote;
        try {
          if (isScpModeClient(staged.client)) {
            await getScpBackendForClient(staged.client).remove(staged.path, { recursive: false, encoding: staged.encoding });
          } else {
            await staged.client.delete(encodePathForSession(staged.sftpId, staged.path, staged.encoding));
          }
        } catch { }
      }
      cleanupTransfer();
      sender.send("netcatty:transfer:cancelled", { transferId });
    } else {
      if (transfer.stagedLocalPath && !transfer.resumable) {
        try { await fs.promises.rm(transfer.stagedLocalPath, { force: true }); } catch { }
        transfer.stagedLocalPath = null;
      }
      sendError(err);
    }
    return { transferId, error: err.message };
  }
}

function getAdmissionResourceKeys(payload) {
  const keys = [
    payload?.sourceHostId ? `host:${payload.sourceHostId}` : payload?.sourceSftpId ? `session:${payload.sourceSftpId}` : null,
    payload?.targetHostId ? `host:${payload.targetHostId}` : payload?.targetSftpId ? `session:${payload.targetSftpId}` : null,
  ].filter(Boolean);
  return [...new Set(keys.length > 0 ? keys : ["local"])];
}

function canAdmitTransfer(job) {
  return job.resourceKeys.every((key) => (admittedActiveByResource.get(key) || 0) < admittedTransferLimit);
}

function adjustAdmittedResources(job, delta) {
  for (const key of job.resourceKeys) {
    const next = (admittedActiveByResource.get(key) || 0) + delta;
    if (next > 0) admittedActiveByResource.set(key, next);
    else admittedActiveByResource.delete(key);
  }
}

function pumpAdmittedTransfers() {
  while (admittedTransferQueue.length > 0) {
    const runnableIndex = admittedTransferQueue.findIndex(canAdmitTransfer);
    if (runnableIndex < 0) return;
    const [job] = admittedTransferQueue.splice(runnableIndex, 1);
    if (!job) return;
    adjustAdmittedResources(job, 1);
    void job.run()
      .then(job.resolve, job.reject)
      .finally(() => {
        adjustAdmittedResources(job, -1);
        pumpAdmittedTransfers();
      });
  }
}

function findQueuedTransfer(transferId) {
  const index = admittedTransferQueue.findIndex((job) => job.payload?.transferId === transferId);
  return index === -1 ? null : { index, job: admittedTransferQueue[index] };
}

function pauseQueuedTransfer(transferId) {
  const queued = findQueuedTransfer(transferId);
  if (!queued) return null;
  admittedTransferQueue.splice(queued.index, 1);
  pausedAdmittedTransfers.set(transferId, queued.job);
  // Preserve the resume checkpoint from the payload — never report 0, or the
  // UI/store will wipe a real checkpoint and restart from byte 0 after restart.
  const checkpointBytes = Math.max(
    0,
    Number(queued.job.payload?.checkpointBytes) || 0,
    Number(queued.job.payload?.downloadCheckpointBytes) || 0,
    Number(queued.job.payload?.uploadCheckpointBytes) || 0,
  );
  const resumeStage = queued.job.payload?.resumeStage || "direct";
  const lifecycleEpoch = Math.max(0, Number(queued.job.payload?.lifecycleEpoch) || 0) + 1;
  queued.job.payload.lifecycleEpoch = lifecycleEpoch;
  queued.job.event?.sender?.send?.("netcatty:transfer:paused", {
    transferId,
    checkpointBytes,
    resumeStage,
    lifecycleEpoch,
  });
  const result = {
    success: true,
    checkpointBytes,
    resumeStage,
    downloadCheckpointBytes: queued.job.payload?.downloadCheckpointBytes,
    uploadCheckpointBytes: queued.job.payload?.uploadCheckpointBytes,
    sourceFingerprint: queued.job.payload?.sourceFingerprint,
    lifecycleEpoch,
  };
  broadcastGlobalTransferEvent({
    type: "paused",
    transferId,
    checkpointBytes: result.checkpointBytes,
    resumeStage: result.resumeStage,
    downloadCheckpointBytes: result.downloadCheckpointBytes,
    uploadCheckpointBytes: result.uploadCheckpointBytes,
    sourceFingerprint: result.sourceFingerprint,
    lifecycleEpoch: result.lifecycleEpoch,
  });
  return result;
}

function cancelQueuedTransfer(transferId) {
  const queued = findQueuedTransfer(transferId);
  const job = queued?.job ?? pausedAdmittedTransfers.get(transferId);
  if (!job) return false;
  if (queued) admittedTransferQueue.splice(queued.index, 1);
  pausedAdmittedTransfers.delete(transferId);
  job.event?.sender?.send?.("netcatty:transfer:cancelled", { transferId });
  job.resolve({ transferId, error: "Transfer cancelled", cancelled: true });
  return true;
}

function resumeQueuedTransfer(transferId) {
  const job = pausedAdmittedTransfers.get(transferId);
  if (!job) return false;
  pausedAdmittedTransfers.delete(transferId);
  const lifecycleEpoch = Math.max(0, Number(job.payload?.lifecycleEpoch) || 0) + 1;
  job.payload.lifecycleEpoch = lifecycleEpoch;
  admittedTransferQueue.push(job);
  job.event?.sender?.send?.("netcatty:transfer:queued", { transferId });
  broadcastGlobalTransferEvent({ type: "queued", transferId, lifecycleEpoch, lifecycleState: "queued" });
  pumpAdmittedTransfers();
  return true;
}

function prioritizeQueuedTransfer(transferId) {
  const queued = findQueuedTransfer(transferId);
  if (!queued) return false;
  admittedTransferQueue.splice(queued.index, 1);
  admittedTransferQueue.unshift(queued.job);
  return true;
}

function runAdmittedTransfer(event, payload, onProgress, runner) {
  const requestedLimit = Number(payload?.globalConcurrency);
  if (Number.isInteger(requestedLimit) && requestedLimit >= 1 && requestedLimit <= 16) {
    setGlobalTransferConcurrency(requestedLimit);
  }
  return new Promise((resolve, reject) => {
    admittedTransferQueue.push({
      event,
      payload,
      resourceKeys: getAdmissionResourceKeys(payload),
      onProgress,
      resolve,
      reject,
      run: runner ?? (() => startTransferNow(event, payload, onProgress)),
    });
    event?.sender?.send?.("netcatty:transfer:queued", { transferId: payload?.transferId });
    pumpAdmittedTransfers();
  });
}

function startTransfer(event, payload, onProgress) {
  if (payload?.skipAdmission === true) {
    return startTransferNow(event, payload, onProgress);
  }
  return runAdmittedTransfer(event, payload, onProgress);
}

/**
 * Cancel a transfer
 */
async function cancelTransfer(event, payload) {
  const { transferId } = payload;
  if (transferId) pendingCancelTransferIds.add(String(transferId));
  if (cancelQueuedTransfer(transferId)) {
    // Queued cancel already settled the job; clear pending so a later retry
    // with a new open can proceed if the UI reuses the id unexpectedly.
    pendingCancelTransferIds.delete(String(transferId));
    return { success: true };
  }
  const transfer = activeTransfers.get(transferId);
  if (transfer) {
    if (transfer.completionCommitted) {
      pendingCancelTransferIds.delete(String(transferId));
      return { success: true };
    }
    transfer.cancelled = true;
    pendingCancelTransferIds.delete(String(transferId));
    if (typeof transfer.abortOwnedSignal === "function") {
      try { transfer.abortOwnedSignal(); } catch { /* ignore */ }
    }
    if (typeof transfer.abort === "function") {
      try { transfer.abort(); } catch { }
    }

    // Destroy streams for stream-based fallback transfers
    if (transfer.readStream) {
      try { transfer.readStream.destroy(); } catch { }
    }
    if (transfer.writeStream) {
      try { transfer.writeStream.destroy(); } catch { }
    }
  }
  return { success: true };
}

/** Clear a pre-start cancel latch (used when retrying the same transfer id). */
function clearPendingCancel(transferId) {
  if (transferId) pendingCancelTransferIds.delete(String(transferId));
}

/**
 * Re-attach a paused stream pair and continue reading.
 * pauseTransfer unpipes so destination drain cannot auto-resume the source.
 * Idempotent: Node does not dedupe pipe(), so only re-pipe while unpiped.
 */
function resumeStreamPair(transfer) {
  if (transfer.readStream && transfer.writeStream && transfer.streamsUnpiped) {
    try { transfer.readStream.pipe(transfer.writeStream); } catch { }
    transfer.streamsUnpiped = false;
  }
  try { transfer.readStream?.resume?.(); } catch { }
}

async function pauseTransfer(_event, payload) {
  const queuedResult = pauseQueuedTransfer(payload?.transferId);
  if (queuedResult) return queuedResult;
  const transfer = activeTransfers.get(payload?.transferId);
  if (!transfer) {
    return { success: false, reason: "Transfer is no longer active" };
  }
  if (!transfer.pauseSupported) {
    return {
      success: false,
      reason: transfer.pauseUnavailableReason || "This transfer cannot be paused safely",
    };
  }
  // Refuse until streams (or an abortable fast path) exist — otherwise the UI
  // latches "paused" while content-verify/fingerprint still runs and then the
  // stream starts under a paused row.
  if (
    !transfer.readStream
    && !transfer.writeStream
    && typeof transfer.abort !== "function"
  ) {
    return { success: false, reason: "This transfer cannot be paused yet" };
  }
  if (transfer.pauseOperation) return transfer.pauseOperation;
  if (transfer.paused && transfer.lifecycleState === "paused") {
    const result = {
      success: true,
      checkpointBytes: transfer.checkpointBytes || 0,
      resumeStage: transfer.resumeStage,
      downloadCheckpointBytes: transfer.downloadCheckpointBytes || 0,
      uploadCheckpointBytes: transfer.uploadCheckpointBytes || 0,
      lifecycleEpoch: transfer.lifecycleEpoch,
      ...(transfer.sourceFingerprint ? { sourceFingerprint: transfer.sourceFingerprint } : {}),
    };
    broadcastGlobalTransferEvent({
      type: "paused",
      transferId: payload.transferId,
      checkpointBytes: result.checkpointBytes,
      resumeStage: result.resumeStage,
      downloadCheckpointBytes: result.downloadCheckpointBytes,
      uploadCheckpointBytes: result.uploadCheckpointBytes,
      sourceFingerprint: result.sourceFingerprint,
      lifecycleEpoch: result.lifecycleEpoch,
      lifecycleState: "paused",
    });
    return result;
  }
  transfer.pauseSuperseded = false;
  const pauseOperation = (async () => {
  transfer.paused = true;
  transfer.lifecycleEpoch += 1;
  transfer.lifecycleState = "pausing";
  broadcastGlobalTransferEvent({
    type: "pausing",
    transferId: payload.transferId,
    checkpointBytes: transfer.checkpointBytes || 0,
    resumeStage: transfer.resumeStage,
    downloadCheckpointBytes: transfer.downloadCheckpointBytes || 0,
    uploadCheckpointBytes: transfer.uploadCheckpointBytes || 0,
    lifecycleEpoch: transfer.lifecycleEpoch,
    lifecycleState: transfer.lifecycleState,
  });
  // Stream transfers use readStream.pipe(writeStream). Node's pipe resumes the
  // source on destination 'drain', and pauseTransfer waits for that drain to
  // flush durable bytes — so pause() alone is undone and upload continues while
  // the UI shows paused. Unpipe first; resumeTransfer re-pipes.
  if (transfer.readStream && transfer.writeStream) {
    try { transfer.readStream.unpipe?.(transfer.writeStream); } catch { }
    transfer.streamsUnpiped = true;
  }
  try { transfer.readStream?.pause?.(); } catch { }
  const usesContiguousRangeCheckpoint = typeof transfer.waitForPause === "function";
  if (usesContiguousRangeCheckpoint) {
    await transfer.waitForPause();
    if (!transfer.paused || transfer.pauseSuperseded) {
      return { success: false, reason: "Pause was superseded by resume" };
    }
    // Concurrent path already tracks contiguous durable bytes — do not spend
    // hundreds of ms waiting for writeStream drain before acknowledging pause.
  } else {
    if (transfer.writeStream?.pending) {
      await new Promise((resolve) => {
        const timer = setTimeout(resolve, PAUSE_STREAM_DRAIN_MS);
        timer.unref?.();
        transfer.writeStream.once?.('open', () => {
          clearTimeout(timer);
          resolve();
        });
      });
    }
    if (transfer.writeStream?.writableNeedDrain) {
      await new Promise((resolve) => {
        const timer = setTimeout(resolve, PAUSE_STREAM_DRAIN_MS);
        timer.unref?.();
        transfer.writeStream.once?.('drain', () => {
          clearTimeout(timer);
          resolve();
        });
      });
    }
    if (
      transfer.writeStream
      && Number.isFinite(transfer.writeStream.bytesWritten)
      && transfer.writeStream.bytesWritten < transfer.checkpointBytes
    ) {
      const deadline = Date.now() + PAUSE_STREAM_DRAIN_MS;
      while (transfer.writeStream.bytesWritten < transfer.checkpointBytes && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 10));
      }
    }
  }
  try {
    // Concurrent range transfers already track the highest contiguous durable
    // byte. File size may extend past a hole when ranges finish out of order.
    if (usesContiguousRangeCheckpoint) {
      const activeRanges = typeof transfer.getActiveRangeCount === "function"
        ? transfer.getActiveRangeCount()
        : 0;
      // Soft-drained pause may still have in-flight ranges writing past the
      // contiguous checkpoint. Truncating now races those writes — defer until
      // the pump is idle (or resume), and trust contiguousCheckpointBytes.
      if (activeRanges === 0) {
        try {
          await prepareStreamFallbackAfterRangeFailure(transfer, transfer.stagedRemote?.client);
          transfer.deferredSparseTruncate = false;
        } catch (truncateError) {
          // Truncate can race with the last soft-drain write. Contiguous
          // checkpoint is still valid for resume — do not abort the pause.
          console.warn(
            "[transferBridge] sparse truncate on pause failed; keeping contiguous checkpoint:",
            truncateError?.message || String(truncateError),
          );
          transfer.deferredSparseTruncate = false;
        }
      } else {
        transfer.deferredSparseTruncate = true;
      }
    } else if (transfer.stagedLocalPath) {
      try {
        const stat = await fs.promises.stat(transfer.stagedLocalPath);
        transfer.checkpointBytes = stat.size;
      } catch (statError) {
        // Fall through to outer catch only when we have no usable checkpoint.
        if (!(Number.isFinite(transfer.checkpointBytes) && transfer.checkpointBytes >= 0)) {
          throw statError;
        }
      }
    } else if (transfer.stagedRemote) {
      try {
        const staged = transfer.stagedRemote;
        const stat = isScpModeClient(staged.client)
          ? await getScpBackendForClient(staged.client).stat(staged.path, { encoding: staged.encoding })
          : await staged.client.stat(encodePathForSession(staged.sftpId, staged.path, staged.encoding));
        transfer.checkpointBytes = stat.size;
      } catch (statError) {
        if (!(Number.isFinite(transfer.checkpointBytes) && transfer.checkpointBytes >= 0)) {
          throw statError;
        }
      }
    }
  } catch {
    // Last resort: keep pause if we already have a contiguous/progress checkpoint.
    // Unpausing here made folder pause look broken (amber error + children keep going).
    if (Number.isFinite(transfer.checkpointBytes) && transfer.checkpointBytes >= 0) {
      transfer.deferredSparseTruncate = true;
    } else {
      transfer.deferredSparseTruncate = false;
      transfer.paused = false;
      transfer.lifecycleEpoch += 1;
      transfer.lifecycleState = "transferring";
      resumeStreamPair(transfer);
      broadcastGlobalTransferEvent({
        type: "resumed",
        transferId: payload.transferId,
        lifecycleEpoch: transfer.lifecycleEpoch,
      });
      return { success: false, reason: "Could not verify the saved transfer checkpoint" };
    }
  }
  if (transfer.resumeStage === 'download') transfer.downloadCheckpointBytes = transfer.checkpointBytes;
  if (transfer.resumeStage === 'upload') transfer.uploadCheckpointBytes = transfer.checkpointBytes;
  if (transfer.cancelled || activeTransfers.get(payload?.transferId) !== transfer) {
    return { success: false, reason: "Transfer is no longer active" };
  }
  if (!transfer.paused || transfer.pauseSuperseded) {
    return { success: false, reason: "Pause was superseded by resume" };
  }
  // Confirm pause as soon as soft-drain + durable checkpoint are ready.
  // Source identity (remote sample reads on download) used to block this IPC
  // for seconds while the UI sat on "finishing current step". Fingerprint is
  // only required at resume — compute it after the user already sees paused.
  transfer.lifecycleState = "paused";
  const result = {
    success: true,
    checkpointBytes: transfer.checkpointBytes || 0,
    resumeStage: transfer.resumeStage,
    downloadCheckpointBytes: transfer.downloadCheckpointBytes || 0,
    uploadCheckpointBytes: transfer.uploadCheckpointBytes || 0,
    lifecycleEpoch: transfer.lifecycleEpoch,
    ...(transfer.sourceFingerprint ? { sourceFingerprint: transfer.sourceFingerprint } : {}),
  };
  broadcastGlobalTransferEvent({
    type: "paused",
    transferId: payload.transferId,
    checkpointBytes: result.checkpointBytes,
    resumeStage: result.resumeStage,
    downloadCheckpointBytes: result.downloadCheckpointBytes,
    uploadCheckpointBytes: result.uploadCheckpointBytes,
    ...(result.sourceFingerprint ? { sourceFingerprint: result.sourceFingerprint } : {}),
    lifecycleEpoch: result.lifecycleEpoch,
    lifecycleState: transfer.lifecycleState,
  });
  if (transfer.resumable && !transfer.sourceFingerprint) {
    const captureId = payload.transferId;
    void computeSourceIdentityLite({
      sourceType: transfer.sourceType,
      sourcePath: transfer.sourcePath,
      sourceSftpId: transfer.sourceSftpId,
      sourceEncoding: transfer.sourceEncoding,
    }).then((fingerprint) => {
      const live = activeTransfers.get(captureId);
      if (!live || live !== transfer || !live.paused || live.cancelled) return;
      live.sourceFingerprint = fingerprint;
      try { live.publishCurrentProgress?.(); } catch { /* best-effort */ }
      broadcastGlobalTransferEvent({
        type: "paused",
        transferId: captureId,
        checkpointBytes: live.checkpointBytes || 0,
        resumeStage: live.resumeStage,
        downloadCheckpointBytes: live.downloadCheckpointBytes || 0,
        uploadCheckpointBytes: live.uploadCheckpointBytes || 0,
        sourceFingerprint: fingerprint,
        lifecycleEpoch: live.lifecycleEpoch,
        lifecycleState: "paused",
      });
    }).catch(() => {
      // Resume can recompute identity if pause-time fingerprint is missing.
    });
  }
  return result;
  })();
  transfer.pauseOperation = pauseOperation;
  try {
    return await pauseOperation;
  } finally {
    if (transfer.pauseOperation === pauseOperation) transfer.pauseOperation = null;
  }
}

async function resumeTransfer(_event, payload) {
  if (resumeQueuedTransfer(payload?.transferId)) return { success: true };
  const transfer = activeTransfers.get(payload?.transferId);
  if (!transfer) {
    return { success: false, reason: "Transfer is no longer active" };
  }
  if (!transfer.pauseSupported) {
    return {
      success: false,
      reason: transfer.pauseUnavailableReason || "This transfer cannot be resumed safely",
    };
  }
  if (transfer.pauseOperation) {
    transfer.pauseSuperseded = true;
    try { transfer.cancelPauseWait?.(); } catch { }
    await transfer.pauseOperation.catch(() => {});
  }
  const currentTransfer = activeTransfers.get(payload?.transferId);
  if (currentTransfer !== transfer || transfer.cancelled) {
    return { success: false, reason: "Transfer is no longer active" };
  }
  // Already flowing (e.g. double-click resume): do not pipe() again.
  if (!transfer.paused) {
    transfer.lifecycleState = "transferring";
    broadcastGlobalTransferEvent({
      type: "resumed",
      transferId: payload.transferId,
      lifecycleEpoch: transfer.lifecycleEpoch,
    });
    return { success: true };
  }
  // Soft-drained concurrent pause may leave a sparse tail past the contiguous
  // checkpoint. Stay paused until leftover ranges settle, then truncate before
  // unpausing. No new ranges are scheduled while paused. A stalled range gets
  // a retryable failure while the transfer remains paused; resuming on top of
  // an old write can corrupt the staged file.
  if (transfer.deferredSparseTruncate) {
    // Soft-drain already returned after PAUSE_RANGE_DRAIN_MS; leftover ranges
    // almost always settle quickly. A multi-second wait made folder resume
    // feel stuck (each child could block the resume path for up to 6s).
    const settleDeadline = Date.now() + RESUME_RANGE_SETTLE_MS;
    while (
      typeof transfer.getActiveRangeCount === "function"
      && transfer.getActiveRangeCount() > 0
      && Date.now() < settleDeadline
    ) {
      if (transfer.cancelled || activeTransfers.get(payload?.transferId) !== transfer) {
        return { success: false, reason: "Transfer is no longer active" };
      }
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    if (transfer.cancelled || activeTransfers.get(payload?.transferId) !== transfer) {
      return { success: false, reason: "Transfer is no longer active" };
    }
    const activeLeft = typeof transfer.getActiveRangeCount === "function"
      ? transfer.getActiveRangeCount()
      : 0;
    if (activeLeft > 0) {
      return {
        success: false,
        reason: "The current file is still finishing. Try resume again.",
      };
    }
    if (activeLeft === 0) {
      try {
        await prepareStreamFallbackAfterRangeFailure(transfer, transfer.stagedRemote?.client);
      } catch {
        // Best-effort; resume from contiguous checkpoint still overwrites holes.
      }
    }
    if (transfer.cancelled || activeTransfers.get(payload?.transferId) !== transfer) {
      return { success: false, reason: "Transfer is no longer active" };
    }
    transfer.deferredSparseTruncate = false;
  }
  transfer.paused = false;
  transfer.pauseSuperseded = false;
  transfer.lifecycleEpoch += 1;
  transfer.lifecycleState = "transferring";
  resumeStreamPair(transfer);
  broadcastGlobalTransferEvent({
    type: "resumed",
    transferId: payload.transferId,
    lifecycleEpoch: transfer.lifecycleEpoch,
  });
  return { success: true };
}

async function prioritizeTransfer(_event, payload) {
  return { success: prioritizeQueuedTransfer(payload?.transferId) };
}

async function cleanupTransferArtifacts(_event, payload) {
  const transferId = payload?.transferId;
  if (!transferId) return { success: false };
  const stageNames = new Set([
    path.basename(payload.targetPath || "transfer"),
    path.basename(payload.sourcePath || "transfer"),
  ]);
  for (const fileName of stageNames) {
    const localStage = tempDirBridge.getTransferTempFilePath(transferId, fileName);
    await fs.promises.rm(localStage, { recursive: true, force: true }).catch(() => {});
  }

  if (payload.targetSftpId && payload.targetPath) {
    const client = sftpClients.get(payload.targetSftpId);
    if (client) {
      const stagePath = buildRemoteTransferStagePath(payload.targetPath, transferId);
      try {
        if (isScpModeClient(client)) {
          await getScpBackendForClient(client).remove(stagePath, { recursive: false, encoding: payload.targetEncoding });
        } else {
          await client.delete(encodePathForSession(payload.targetSftpId, stagePath, payload.targetEncoding));
        }
      } catch { /* artifact may not exist */ }
    }
  }

  if (payload.stagedTargetPath) {
    try {
      if (payload.targetSftpId) {
        const client = sftpClients.get(payload.targetSftpId);
        if (client) await client.rmdir(encodePathForSession(payload.targetSftpId, payload.stagedTargetPath, payload.targetEncoding), true);
      } else {
        await fs.promises.rm(payload.stagedTargetPath, { recursive: true, force: true });
      }
    } catch { /* best effort */ }
  }
  return { success: true };
}

/**
 * Same-host directory copy: uses a single `cp -ra` command on the remote server
 * instead of recursively transferring files one by one.
 */
async function sameHostCopyDirectory(event, payload) {
  const { sftpId, sourcePath, targetPath, encoding, transferId } = payload;

  // Register in activeTransfers so cancelTransfer can flag it
  const transfer = { cancelled: false, leasedSftpIds: [] };
  if (transferId) {
    activeTransfers.set(transferId, transfer);
    transfer.leasedSftpIds = acquireTransferSessionLeases(transferId, {
      sourceSftpId: sftpId,
      targetSftpId: sftpId,
    });
  }

  try {
    if (cpUnavailableSet.has(sftpId)) return { success: false };

    const client = sftpClients.get(sftpId);
    if (!client) return { success: false };

    const sshClient = client.client;
    if (!sshClient || typeof sshClient.exec !== 'function') {
      return { success: false };
    }

    if (transfer.cancelled) throw new Error("Transfer cancelled");

    // Ensure target directory itself exists (not just its parent),
    // so cp copies contents into it rather than creating a nested subdirectory.
    const targetDir = targetPath.replace(/\\/g, '/');
    try {
      await runTransferAbortableOperation(
        transfer,
        (signal) => ensureRemoteDirForSession(sftpId, targetDir, encoding, { signal }),
      );
    } catch (error) {
      if (transfer.cancelled) throw new Error("Transfer cancelled");
    }

    // Use "source/." to copy directory *contents* into target, preserving merge
    // semantics consistent with the recursive per-file transfer path.
    // Without "/.", `cp -ra source target` would create target/source/ when target exists.
    const escapedSource = sourcePath.replace(/'/g, "'\\''");
    const escapedTarget = targetPath.replace(/'/g, "'\\''");
    const command = `cp -ra '${escapedSource}/.' '${escapedTarget}/'`;

    try {
      const result = await execSshCommandCancellable(sshClient, command, transfer);
      if (result.code === 127) {
        cpUnavailableSet.add(sftpId);
        return { success: false };
      }
      if (result.code !== 0) {
        return { success: false };
      }
    } catch (cpErr) {
      if (transfer.cancelled) throw cpErr;
      return { success: false };
    }

    return { success: true };
  } finally {
    if (transferId) {
      activeTransfers.delete(transferId);
      releaseTransferSessionLeases(transferId, transfer.leasedSftpIds || [sftpId]);
      transfer.leasedSftpIds = [];
    }
  }
}

function registerWorkerHandle(ipcMain, terminalWorkerManager, channel) {
  ipcMain.handle(channel, (event, payload) => terminalWorkerManager.request(channel, payload, {
    webContentsId: event?.sender?.id,
  }));
}

/**
 * Register IPC handlers for transfer operations
 */
function registerHandlers(ipcMain, options = {}) {
  const terminalWorkerManager = options.terminalWorkerManager || null;
  if (terminalWorkerManager) {
    const nextWorkerLifecycleEpoch = (transferId, suggestedEpoch) => {
      const current = Math.max(0, Number(workerTransferLifecycleEpochs.get(transferId)) || 0);
      const suggested = Number(suggestedEpoch);
      const next = Number.isFinite(suggested) && suggested > current ? suggested : current + 1;
      workerTransferLifecycleEpochs.set(transferId, next);
      return next;
    };
    const workerRequest = (event, channel, payload) => terminalWorkerManager.request(channel, payload, {
      webContentsId: event?.sender?.id,
    });
    ipcMain.handle("netcatty:transfer:start", (event, payload) => {
      if (payload?.transferId) {
        workerTransferLifecycleEpochs.set(
          payload.transferId,
          Math.max(0, Number(payload.lifecycleEpoch) || 0),
        );
      }
      // Renderer (or outer main) already admitted — skip a second queue so
      // dedicated pool leases are not pinned while waiting on main admission.
      if (payload?.skipAdmission === true) {
        return workerRequest(event, "netcatty:transfer:start", payload);
      }
      return runAdmittedTransfer(
        event,
        payload,
        undefined,
        () => workerRequest(event, "netcatty:transfer:start", {
          ...payload,
          skipAdmission: true,
        }),
      );
    });
    ipcMain.handle("netcatty:transfer:cancel", (event, payload) => (
      cancelQueuedTransfer(payload?.transferId)
        ? { success: true }
        : workerRequest(event, "netcatty:transfer:cancel", payload)
    ));
    ipcMain.handle("netcatty:transfer:pause", async (event, payload) => {
      const queued = pauseQueuedTransfer(payload?.transferId);
      if (queued) return queued;
      const lifecycleEpoch = nextWorkerLifecycleEpoch(payload?.transferId);
      broadcastGlobalTransferEvent({
        type: "pausing",
        transferId: payload?.transferId,
        lifecycleEpoch,
        lifecycleState: "pausing",
      });
      try {
        const result = await workerRequest(event, "netcatty:transfer:pause", payload);
        if (!result?.success) {
          const rollbackEpoch = nextWorkerLifecycleEpoch(payload?.transferId);
          broadcastGlobalTransferEvent({
            type: "resumed",
            transferId: payload?.transferId,
            lifecycleEpoch: rollbackEpoch,
            lifecycleState: "transferring",
          });
          return result;
        }
        broadcastGlobalTransferEvent({
          type: "paused",
          transferId: payload?.transferId,
          checkpointBytes: result.checkpointBytes,
          resumeStage: result.resumeStage,
          downloadCheckpointBytes: result.downloadCheckpointBytes,
          uploadCheckpointBytes: result.uploadCheckpointBytes,
          sourceFingerprint: result.sourceFingerprint,
          lifecycleEpoch,
          lifecycleState: "paused",
        });
        return result;
      } catch (error) {
        const rollbackEpoch = nextWorkerLifecycleEpoch(payload?.transferId);
        broadcastGlobalTransferEvent({
          type: "resumed",
          transferId: payload?.transferId,
          lifecycleEpoch: rollbackEpoch,
          lifecycleState: "transferring",
        });
        throw error;
      }
    });
    ipcMain.handle("netcatty:transfer:resume", async (event, payload) => {
      if (resumeQueuedTransfer(payload?.transferId)) return { success: true };
      const result = await workerRequest(event, "netcatty:transfer:resume", payload);
      if (result?.success) {
        const lifecycleEpoch = nextWorkerLifecycleEpoch(payload?.transferId, result.lifecycleEpoch);
        broadcastGlobalTransferEvent({
          type: "resumed",
          transferId: payload?.transferId,
          lifecycleEpoch,
          lifecycleState: "transferring",
        });
      }
      return result;
    });
    ipcMain.handle("netcatty:transfer:prioritize", (event, payload) => (
      prioritizeQueuedTransfer(payload?.transferId)
        ? { success: true }
        : workerRequest(event, "netcatty:transfer:prioritize", payload)
    ));
    [
      "netcatty:transfer:cleanup",
      "netcatty:transfer:same-host-copy-dir",
    ].forEach((channel) => registerWorkerHandle(ipcMain, terminalWorkerManager, channel));
    ipcMain.handle("netcatty:transfer:set-concurrency", async (_event, payload) => {
      const limit = setGlobalTransferConcurrency(payload?.limit);
      await terminalWorkerManager.request("netcatty:transfer:set-concurrency", { limit }).catch(() => {});
      return { success: true, limit };
    });
    // With skipAdmission, cancel latches pendingCancel inside the worker process.
    // Clear must reach the same process that owns startTransferNow.
    ipcMain.handle("netcatty:transfer:clear-pending-cancel", (event, payload) => (
      workerRequest(event, "netcatty:transfer:clear-pending-cancel", payload)
        .catch(() => {
          clearPendingCancel(payload?.transferId);
          return { success: true };
        })
    ));
    return;
  }
  ipcMain.handle("netcatty:transfer:start", startTransfer);
  ipcMain.handle("netcatty:transfer:cancel", cancelTransfer);
  ipcMain.handle("netcatty:transfer:pause", pauseTransfer);
  ipcMain.handle("netcatty:transfer:resume", resumeTransfer);
  ipcMain.handle("netcatty:transfer:prioritize", prioritizeTransfer);
  ipcMain.handle("netcatty:transfer:set-concurrency", (_event, payload) => ({
    success: true,
    limit: setGlobalTransferConcurrency(payload?.limit),
  }));
  ipcMain.handle("netcatty:transfer:cleanup", cleanupTransferArtifacts);
  ipcMain.handle("netcatty:transfer:same-host-copy-dir", sameHostCopyDirectory);
  ipcMain.handle("netcatty:transfer:clear-pending-cancel", (_event, payload) => {
    clearPendingCancel(payload?.transferId);
    return { success: true };
  });
}

module.exports = {
  init,
  registerHandlers,
  startTransfer,
  runAdmittedTransfer,
  cancelTransfer,
  clearPendingCancel,
  pauseTransfer,
  resumeTransfer,
  prioritizeTransfer,
  setGlobalTransferConcurrency,
  getGlobalTransferConcurrency,
  broadcastGlobalTransferEvent,
  cleanupTransferArtifacts,
  sameHostCopyDirectory,
  // Test / integration helpers for session leases
  acquireTransferSessionLeases,
  releaseTransferSessionLeases,
  listTransferSftpIds,
  _promoteLocalTransferForTests: promoteLocalTransfer,
  _stableLocalFileIdentityForTests: stableLocalFileIdentity,
};
