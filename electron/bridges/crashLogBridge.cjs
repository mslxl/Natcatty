/**
 * Crash Log Bridge - Captures main-process errors and writes them to local log files.
 *
 * Log files are stored as JSONL (one JSON object per line) under
 * {userData}/crash-logs/crash-YYYY-MM-DD.log so that appending is cheap and
 * atomic.  Files older than 30 days are pruned on startup.
 */

const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

let logDir = null;
let electronApp = null;
let electronShell = null;

const LOG_RETENTION_DAYS = 30;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function ensureLogDir() {
  if (logDir) return logDir;

  try {
    const userDataPath = electronApp
      ? electronApp.getPath("userData")
      : null;
    if (!userDataPath) return null;

    logDir = path.join(userDataPath, "crash-logs");
    if (!fs.existsSync(logDir)) {
      fs.mkdirSync(logDir, { recursive: true });
    }
    return logDir;
  } catch {
    return null;
  }
}

function todayFileName() {
  const d = new Date();
  const ymd = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  return `crash-${ymd}.log`;
}

function buildEntry(source, err) {
  const error = err instanceof Error ? err : new Error(String(err ?? "unknown"));
  return {
    timestamp: new Date().toISOString(),
    source,
    message: error.message || String(err),
    stack: error.stack || undefined,
    platform: process.platform,
    arch: process.arch,
    version: electronApp?.getVersion?.() ?? "unknown",
  };
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Write a crash/error entry to today's log file (sync, safe for use in
 * uncaughtException handlers).
 */
function captureError(source, err) {
  try {
    const dir = ensureLogDir();
    if (!dir) return;

    const entry = buildEntry(source, err);
    const filePath = path.join(dir, todayFileName());
    fs.appendFileSync(filePath, JSON.stringify(entry) + "\n", "utf-8");
  } catch {
    // Never throw from the crash logger itself.
  }
}

/**
 * Delete log files older than LOG_RETENTION_DAYS.
 */
function pruneOldLogs() {
  try {
    const dir = ensureLogDir();
    if (!dir) return;

    const cutoff = Date.now() - LOG_RETENTION_DAYS * 86400000;
    const files = fs.readdirSync(dir);

    for (const file of files) {
      if (!file.startsWith("crash-") || !file.endsWith(".log")) continue;
      try {
        const filePath = path.join(dir, file);
        const stat = fs.statSync(filePath);
        if (stat.mtimeMs < cutoff) {
          fs.unlinkSync(filePath);
          console.log(`[CrashLog] Pruned old log: ${file}`);
        }
      } catch {
        // skip
      }
    }
  } catch {
    // skip
  }
}

// ---------------------------------------------------------------------------
// IPC handlers
// ---------------------------------------------------------------------------

async function listLogs() {
  const dir = ensureLogDir();
  if (!dir) return [];

  try {
    const files = await fs.promises.readdir(dir);
    const results = [];

    for (const file of files) {
      if (!file.startsWith("crash-") || !file.endsWith(".log")) continue;
      try {
        const filePath = path.join(dir, file);
        const stat = await fs.promises.stat(filePath);
        // Count entries (lines)
        const content = await fs.promises.readFile(filePath, "utf-8");
        const lines = content.split("\n").filter(Boolean);
        results.push({
          fileName: file,
          date: file.replace("crash-", "").replace(".log", ""),
          size: stat.size,
          entryCount: lines.length,
        });
      } catch {
        // skip unreadable files
      }
    }

    // Sort newest first
    results.sort((a, b) => b.date.localeCompare(a.date));
    return results;
  } catch {
    return [];
  }
}

async function readLog(fileName) {
  const dir = ensureLogDir();
  if (!dir) return [];

  // Validate fileName to prevent path traversal
  if (!/^crash-\d{4}-\d{2}-\d{2}\.log$/.test(fileName)) return [];

  try {
    const filePath = path.join(dir, fileName);
    const content = await fs.promises.readFile(filePath, "utf-8");
    const lines = content.split("\n").filter(Boolean);

    // Parse each JSONL line, limit to 500 most recent
    const entries = [];
    for (const line of lines) {
      try {
        entries.push(JSON.parse(line));
      } catch {
        // skip malformed lines
      }
    }
    return entries.slice(-500);
  } catch {
    return [];
  }
}

async function clearLogs() {
  const dir = ensureLogDir();
  if (!dir) return { deletedCount: 0 };

  let deletedCount = 0;
  try {
    const files = await fs.promises.readdir(dir);
    for (const file of files) {
      if (!file.startsWith("crash-") || !file.endsWith(".log")) continue;
      try {
        await fs.promises.unlink(path.join(dir, file));
        deletedCount++;
      } catch {
        // skip
      }
    }
  } catch {
    // skip
  }
  return { deletedCount };
}

async function openDir() {
  const dir = ensureLogDir();
  if (!dir || !electronShell?.openPath) return { success: false };
  try {
    await electronShell.openPath(dir);
    return { success: true };
  } catch {
    return { success: false };
  }
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

function init(deps) {
  const { electronModule } = deps;
  const { app, shell } = electronModule || {};
  electronApp = app;
  electronShell = shell;

  ensureLogDir();
  pruneOldLogs();

  console.log(`[CrashLog] Crash log directory: ${logDir}`);
}

function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:crashLogs:list", async () => listLogs());
  ipcMain.handle("netcatty:crashLogs:read", async (_event, { fileName }) => readLog(fileName));
  ipcMain.handle("netcatty:crashLogs:clear", async () => clearLogs());
  ipcMain.handle("netcatty:crashLogs:openDir", async () => openDir());
}

module.exports = {
  init,
  captureError,
  registerHandlers,
};
