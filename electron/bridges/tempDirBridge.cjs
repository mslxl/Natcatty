/**
 * Temp Directory Bridge - Manages Netcatty's dedicated temp directory
 * 
 * All temporary files (SFTP downloads, etc.) are stored in a dedicated
 * Netcatty folder within the system temp directory for easier cleanup.
 */

const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");

// Netcatty temp directory name
const NETCATTY_TEMP_DIR_NAME = "Netcatty";
const MAX_TOOL_OUTPUT_TEMP_CHARS = 4_000_000;
const MAX_TOOL_OUTPUT_TEMP_BYTES = 8_000_000;
const TOOL_OUTPUT_TEMP_TTL_MS = 30 * 60 * 1_000;
const TOOL_OUTPUT_READ_MAX_CHARS = 12_000;
const TOOL_OUTPUT_SEARCH_CONTEXT_CHARS = 320;
const TOOL_OUTPUT_SEARCH_MAX_MATCHES = 20;

// Cached temp directory path
let cachedTempDir = null;
let cachedTempDirIdentity = null;
let tempFileCounter = 0;

/**
 * Get the Netcatty temp directory path
 * Creates the directory if it doesn't exist
 */
function getTempDir() {
  if (cachedTempDir) {
    assertSafeTempDir(cachedTempDir, cachedTempDirIdentity);
    return cachedTempDir;
  }
  
  const systemTempDir = os.tmpdir();
  const netcattyTempDir = path.join(systemTempDir, NETCATTY_TEMP_DIR_NAME);
  
  try {
    if (!fs.existsSync(netcattyTempDir)) {
      fs.mkdirSync(netcattyTempDir, { recursive: true, mode: 0o700 });
      console.log(`[TempDir] Created Netcatty temp directory: ${netcattyTempDir}`);
    }
    const safeStat = assertSafeTempDir(netcattyTempDir);
    cachedTempDir = netcattyTempDir;
    cachedTempDirIdentity = { dev: safeStat.dev, ino: safeStat.ino };
    return netcattyTempDir;
  } catch (err) {
    console.error(`[TempDir] Failed to create temp directory:`, err.message);
    throw err;
  }
}

function assertSafeTempDir(tempDir, expectedIdentity) {
  const stat = fs.lstatSync(tempDir);
  if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error("Netcatty temp path is not a safe directory.");
  if (typeof process.getuid === "function" && stat.uid !== process.getuid()) {
    throw new Error("Netcatty temp directory is not owned by the current user.");
  }
  if (expectedIdentity && (stat.dev !== expectedIdentity.dev || stat.ino !== expectedIdentity.ino)) {
    throw new Error("Netcatty temp directory identity changed during this process.");
  }
  fs.chmodSync(tempDir, 0o700);
  const expectedRealPath = path.join(fs.realpathSync(path.dirname(tempDir)), path.basename(tempDir));
  if (fs.realpathSync(tempDir) !== expectedRealPath) {
    throw new Error("Netcatty temp directory must not traverse symbolic links.");
  }
  return stat;
}

/**
 * Ensure the temp directory exists (call on app startup)
 */
function ensureTempDir() {
  const tempDir = getTempDir();
  console.log(`[TempDir] Netcatty temp directory: ${tempDir}`);
  return tempDir;
}

/**
 * Get temp directory info (path, size, file count)
 */
async function getTempDirInfo() {
  const tempDir = getTempDir();
  
  try {
    const files = await fs.promises.readdir(tempDir);
    let totalSize = 0;
    let fileCount = 0;
    
    for (const file of files) {
      try {
        const filePath = path.join(tempDir, file);
        const stat = await fs.promises.stat(filePath);
        if (stat.isFile()) {
          totalSize += stat.size;
          fileCount++;
        }
      } catch {
        // Skip files that can't be stat'd
      }
    }
    
    return {
      path: tempDir,
      totalSize,
      fileCount,
    };
  } catch (err) {
    console.error(`[TempDir] Failed to get temp dir info:`, err.message);
    return {
      path: tempDir,
      totalSize: 0,
      fileCount: 0,
    };
  }
}

/**
 * Clear all files in the temp directory
 * Returns the number of files deleted
 */
async function clearTempDir() {
  const tempDir = getTempDir();
  let deletedCount = 0;
  let failedCount = 0;
  
  try {
    const files = await fs.promises.readdir(tempDir);
    
    for (const file of files) {
      try {
        const filePath = path.join(tempDir, file);
        const stat = await fs.promises.stat(filePath);
        
        if (stat.isFile()) {
          await fs.promises.unlink(filePath);
          deletedCount++;
          console.log(`[TempDir] Deleted: ${file}`);
        } else if (stat.isDirectory()) {
          // Recursively delete subdirectories
          await fs.promises.rm(filePath, { recursive: true, force: true });
          deletedCount++;
          console.log(`[TempDir] Deleted directory: ${file}`);
        }
      } catch (err) {
        failedCount++;
        console.log(`[TempDir] Could not delete ${file}: ${err.message}`);
      }
    }
    
    console.log(`[TempDir] Cleanup complete: ${deletedCount} deleted, ${failedCount} failed`);
    return { deletedCount, failedCount };
  } catch (err) {
    console.error(`[TempDir] Failed to clear temp dir:`, err.message);
    return { deletedCount: 0, failedCount: 0, error: err.message };
  }
}

/**
 * Generate a unique temp file path for a given filename
 */
function getTempFilePath(fileName) {
  const tempDir = getTempDir();
  const timestamp = Date.now();
  tempFileCounter = (tempFileCounter + 1) % 1000000;
  const safeFileName = fileName.replace(/[<>:"/\\|?*]/g, "_");
  return path.join(tempDir, `${timestamp}_${tempFileCounter}_${safeFileName}`);
}

function isNetcattyTempPath(filePath) {
  if (typeof filePath !== "string" || !filePath) return false;
  const tempDir = path.resolve(getTempDir());
  const resolved = path.resolve(filePath);
  const relative = path.relative(tempDir, resolved);
  return relative !== "" && !relative.startsWith("..") && !path.isAbsolute(relative);
}

async function openSafeToolOutputFile(filePath) {
  if (!isNetcattyTempPath(filePath)) return null;
  let file;
  try {
    const noFollow = fs.constants.O_NOFOLLOW ?? 0;
    file = await fs.promises.open(filePath, fs.constants.O_RDONLY | noFollow);
    const stat = await file.stat();
    assertSafeTempDir(getTempDir(), cachedTempDirIdentity);
    const pathStat = await fs.promises.lstat(filePath);
    if (pathStat.isSymbolicLink() || pathStat.dev !== stat.dev || pathStat.ino !== stat.ino) {
      await file.close();
      return null;
    }
    if (!stat.isFile() || stat.size > MAX_TOOL_OUTPUT_TEMP_BYTES) {
      await file.close();
      return null;
    }
    return { file, stat };
  } catch {
    await file?.close().catch(() => {});
    return null;
  }
}

async function cleanupExpiredToolOutputFiles(now = Date.now()) {
  const tempDir = getTempDir();
  let deletedCount = 0;
  try {
    const files = await fs.promises.readdir(tempDir);
    for (const file of files) {
      if (!file.includes("_tool-output-") || !file.endsWith(".log")) continue;
      const filePath = path.join(tempDir, file);
      try {
        const stat = await fs.promises.lstat(filePath);
        if (stat.isSymbolicLink() || !stat.isFile()) continue;
        if (now - stat.mtimeMs < TOOL_OUTPUT_TEMP_TTL_MS) continue;
        await fs.promises.unlink(filePath);
        deletedCount += 1;
      } catch {
        // Best-effort startup cleanup.
      }
    }
  } catch {
    // Temp persistence is optional; keep startup resilient.
  }
  return deletedCount;
}

function safeUtf16SliceBounds(content, requestedStart, requestedEnd) {
  let start = Math.min(content.length, Math.max(0, requestedStart));
  let end = Math.min(content.length, Math.max(start, requestedEnd));
  const isHigh = value => value >= 0xd800 && value <= 0xdbff;
  const isLow = value => value >= 0xdc00 && value <= 0xdfff;
  if (start > 0 && start < content.length && isLow(content.charCodeAt(start))) start -= 1;
  if (end > start && end < content.length && isHigh(content.charCodeAt(end - 1))) end -= 1;
  return [start, end];
}

async function readToolOutputChunk(file, request, stat) {
  const storedChars = Math.floor(stat.size / 2);
  const requestedMax = Number.isFinite(request?.maxChars) ? Math.floor(request.maxChars) : TOOL_OUTPUT_READ_MAX_CHARS;
  const maxChars = Math.min(TOOL_OUTPUT_READ_MAX_CHARS, Math.max(1, requestedMax));
  const mode = request?.mode ?? "head";

  if (mode === "search") {
    const query = String(request?.query ?? "");
    if (!query) {
      return { mode, content: "Search query is required.", totalChars: storedChars, startOffset: 0, endOffset: 0, nextOffset: 0, hasMore: false, matchOffsets: [] };
    }
    const content = await file.readFile({ encoding: "utf16le" });
    const haystack = content.toLocaleLowerCase();
    const needle = query.toLocaleLowerCase();
    const offsets = [];
    let cursor = Math.max(0, Math.floor(request?.offset ?? 0));
    while (offsets.length < TOOL_OUTPUT_SEARCH_MAX_MATCHES) {
      const match = haystack.indexOf(needle, cursor);
      if (match < 0) break;
      offsets.push(match);
      cursor = match + Math.max(1, needle.length);
    }
    const excerpts = [];
    let renderedChars = 0;
    for (const match of offsets) {
      const [start, end] = safeUtf16SliceBounds(content, match - TOOL_OUTPUT_SEARCH_CONTEXT_CHARS, match + query.length + TOOL_OUTPUT_SEARCH_CONTEXT_CHARS);
      const excerpt = `[match offset=${match}]\n${content.slice(start, end)}`;
      if (renderedChars + excerpt.length > maxChars) break;
      excerpts.push(excerpt);
      renderedChars += excerpt.length + 2;
    }
    const nextOffset = offsets.length ? offsets[offsets.length - 1] + Math.max(1, query.length) : storedChars;
    return {
      mode,
      content: excerpts.join("\n\n") || `No matches found for "${query}".`,
      totalChars: storedChars,
      startOffset: Math.max(0, Math.floor(request?.offset ?? 0)),
      endOffset: nextOffset,
      nextOffset,
      hasMore: haystack.indexOf(needle, nextOffset) >= 0,
      matchOffsets: offsets,
    };
  }

  let startOffset = mode === "tail"
    ? Math.max(0, storedChars - maxChars)
    : mode === "range" ? Math.min(storedChars, Math.max(0, Math.floor(request?.offset ?? 0))) : 0;
  const readStart = Math.max(0, startOffset - 1);
  const readChars = Math.min(storedChars - readStart, maxChars + 2);
  const buffer = Buffer.alloc(Math.max(0, readChars * 2));
  const { bytesRead } = await file.read(buffer, 0, buffer.length, readStart * 2);
  const window = buffer.subarray(0, bytesRead).toString("utf16le");
  const relativeStart = startOffset - readStart;
  const [safeStart, safeEnd] = safeUtf16SliceBounds(window, relativeStart, relativeStart + maxChars);
  startOffset = readStart + safeStart;
  const content = window.slice(safeStart, safeEnd);
  const endOffset = startOffset + content.length;
  return { mode, content, totalChars: storedChars, startOffset, endOffset, nextOffset: endOffset, hasMore: endOffset < storedChars };
}

/**
 * Register IPC handlers
 */
function registerHandlers(ipcMain, shell) {
  void cleanupExpiredToolOutputFiles();
  ipcMain.handle("netcatty:tempdir:getInfo", async () => {
    return getTempDirInfo();
  });
  
  ipcMain.handle("netcatty:tempdir:clear", async () => {
    return clearTempDir();
  });
  
  ipcMain.handle("netcatty:tempdir:getPath", () => {
    return getTempDir();
  });
  
  ipcMain.handle("netcatty:tempdir:open", async () => {
    const tempDir = getTempDir();
    if (shell?.openPath) {
      await shell.openPath(tempDir);
      return { success: true };
    }
    return { success: false };
  });

  ipcMain.handle("netcatty:tempdir:toolOutputWrite", async (_event, payload = {}) => {
    const content = String(payload.content ?? "");
    if (content.length > MAX_TOOL_OUTPUT_TEMP_CHARS) {
      return { ok: false, error: "Tool output exceeds the temp-file limit." };
    }
    const handleId = String(payload.handleId ?? "tool-output").replace(/[^A-Za-z0-9_.-]/g, "_");
    const filePath = getTempFilePath(`${handleId}.log`);
    await fs.promises.writeFile(filePath, content, { encoding: "utf16le", mode: 0o600, flag: "wx" });
    return { ok: true, path: filePath };
  });

  ipcMain.handle("netcatty:tempdir:toolOutputRead", async (_event, payload = {}) => {
    const filePath = payload.path;
    const opened = await openSafeToolOutputFile(filePath);
    if (!opened) return null;
    try {
      if (!payload.request) return await opened.file.readFile({ encoding: "utf16le" });
      return await readToolOutputChunk(opened.file, payload.request, opened.stat);
    } finally {
      await opened.file.close();
    }
  });

  ipcMain.handle("netcatty:tempdir:toolOutputDelete", async (_event, payload = {}) => {
    const filePath = payload.path;
    if (!isNetcattyTempPath(filePath)) return { ok: false };
    try {
      const stat = await fs.promises.lstat(filePath);
      if (!stat.isFile() || stat.isSymbolicLink()) return { ok: false };
      await fs.promises.unlink(filePath);
      return { ok: true };
    } catch (error) {
      if (error?.code === "ENOENT") return { ok: true };
      return { ok: false };
    }
  });
}

module.exports = {
  getTempDir,
  ensureTempDir,
  getTempDirInfo,
  clearTempDir,
  getTempFilePath,
  cleanupExpiredToolOutputFiles,
  registerHandlers,
};
