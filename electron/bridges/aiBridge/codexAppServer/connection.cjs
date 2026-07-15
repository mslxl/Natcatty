"use strict";

const path = require("node:path");
const readline = require("node:readline");
const { spawn } = require("node:child_process");
const { createHash } = require("node:crypto");

const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;
const INITIALIZE_TIMEOUT_MS = 10_000;
const MAX_STDERR_CHARS = 32_000;

function buildCodexAppServerLaunch(binPath, args = ["app-server", "--stdio"], {
  nodePath = process.execPath,
} = {}) {
  const executable = String(binPath || "").trim();
  if (!executable) {
    throw new Error("Codex binary not found. Configure Codex in Settings -> AI.");
  }
  const extension = path.extname(executable).toLowerCase();
  if (extension === ".js" || extension === ".cjs" || extension === ".mjs") {
    return {
      command: nodePath,
      args: [executable, ...args],
      env: { ELECTRON_RUN_AS_NODE: "1" },
    };
  }
  if (extension === ".cmd" || extension === ".bat" || extension === ".ps1") {
    throw new Error(
      `Codex App Server cannot launch the shell shim ${executable}. ` +
      "Configure the native Codex executable or reinstall the Codex CLI.",
    );
  }
  return { command: executable, args };
}

function buildCodexAppServerKey(binPath, env) {
  const fingerprint = createHash("sha256")
    .update(JSON.stringify(
      Object.entries(env || {})
        .map(([key, value]) => [key, String(value)])
        .sort(([left], [right]) => left.localeCompare(right)),
    ))
    .digest("hex");
  return `${String(binPath || "")}\u0000${fingerprint}`;
}

class CodexAppServerConnection {
  constructor({
    binPath,
    env,
    appVersion = "0.0.0",
    spawnImpl = spawn,
    onNotification,
    onServerRequest,
    onFatal,
  }) {
    this.binPath = binPath;
    this.env = env || {};
    this.appVersion = appVersion;
    this.spawnImpl = spawnImpl;
    this.onNotification = onNotification;
    this.onServerRequest = onServerRequest;
    this.onFatal = onFatal;
    this.process = null;
    this.readline = null;
    this.nextRequestId = 1;
    this.pending = new Map();
    this.startPromise = null;
    this.initialized = false;
    this.closing = false;
    this.stderr = "";
  }

  async start() {
    if (this.initialized && this.process && !this.process.killed) return this;
    if (this.startPromise) return this.startPromise;
    this.startPromise = this.#startInternal().finally(() => {
      this.startPromise = null;
    });
    return this.startPromise;
  }

  async #startInternal() {
    this.closing = false;
    this.stderr = "";
    const launch = buildCodexAppServerLaunch(this.binPath);
    const child = this.spawnImpl(launch.command, launch.args, {
      cwd: process.cwd(),
      env: { ...this.env, ...(launch.env || {}) },
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
      shell: false,
    });
    this.process = child;

    child.stderr?.setEncoding?.("utf8");
    child.stderr?.on?.("data", (chunk) => {
      this.stderr = `${this.stderr}${String(chunk || "")}`.slice(-MAX_STDERR_CHARS);
    });

    this.readline = readline.createInterface({ input: child.stdout });
    this.readline.on("line", (line) => this.#handleLine(line));

    child.once("error", (error) => this.#handleFatal(error));
    child.once("exit", (code, signal) => {
      if (this.closing) return;
      const detail = this.stderr.trim();
      const suffix = detail ? `\n${detail}` : "";
      this.#handleFatal(new Error(
        `Codex App Server exited unexpectedly (code ${code ?? "null"}, signal ${signal ?? "none"}).${suffix}`,
      ));
    });

    try {
      await this.request("initialize", {
        clientInfo: {
          name: "netcatty",
          title: "Netcatty",
          version: this.appVersion,
        },
        capabilities: {
          experimentalApi: true,
          requestAttestation: false,
          mcpServerOpenaiFormElicitation: false,
        },
      }, INITIALIZE_TIMEOUT_MS, { skipStart: true });
      this.notify("initialized", {});
      this.initialized = true;
      return this;
    } catch (error) {
      this.close();
      const detail = this.stderr.trim();
      if (detail && !String(error?.message || error).includes(detail)) {
        throw new Error(`${error?.message || error}\n${detail}`);
      }
      throw error;
    }
  }

  async request(method, params = {}, timeoutMs = DEFAULT_REQUEST_TIMEOUT_MS, options = {}) {
    if (!options.skipStart) await this.start();
    const id = this.nextRequestId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`Codex App Server request timed out: ${method}`));
      }, timeoutMs);
      this.pending.set(id, { method, resolve, reject, timer });
      try {
        this.#write({ id, method, params });
      } catch (error) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(error);
      }
    });
  }

  notify(method, params = {}) {
    this.#write({ method, params });
  }

  respond(id, result) {
    this.#write({ id, result });
  }

  respondError(id, code, message, data) {
    const error = { code, message };
    if (data !== undefined) error.data = data;
    this.#write({ id, error });
  }

  #write(message) {
    const stdin = this.process?.stdin;
    if (!stdin || stdin.destroyed || !stdin.writable) {
      throw new Error("Codex App Server stdin is unavailable");
    }
    stdin.write(`${JSON.stringify(message)}\n`);
  }

  #handleLine(rawLine) {
    const line = String(rawLine || "").trim();
    if (!line) return;
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      this.#handleFatal(new Error(`Codex App Server emitted invalid JSON: ${line.slice(0, 500)}`));
      return;
    }

    if (Object.prototype.hasOwnProperty.call(message, "id") && !message.method) {
      const entry = this.pending.get(message.id);
      if (!entry) return;
      this.pending.delete(message.id);
      clearTimeout(entry.timer);
      if (message.error) {
        const error = new Error(message.error.message || `Codex App Server ${entry.method} failed`);
        error.code = message.error.code;
        error.data = message.error.data;
        entry.reject(error);
      } else {
        entry.resolve(message.result);
      }
      return;
    }

    if (message.method && Object.prototype.hasOwnProperty.call(message, "id")) {
      Promise.resolve(this.onServerRequest?.(message, this)).catch((error) => {
        try {
          this.respondError(message.id, -32603, error?.message || String(error));
        } catch {}
      });
      return;
    }

    if (message.method) {
      try {
        this.onNotification?.(message, this);
      } catch (error) {
        this.#handleFatal(error);
      }
    }
  }

  #handleFatal(error) {
    if (this.closing) return;
    this.initialized = false;
    const fatal = error instanceof Error ? error : new Error(String(error));
    for (const [, entry] of this.pending) {
      clearTimeout(entry.timer);
      entry.reject(fatal);
    }
    this.pending.clear();
    try { this.onFatal?.(fatal, this); } catch {}
    this.close();
  }

  close() {
    this.closing = true;
    this.initialized = false;
    try { this.readline?.close?.(); } catch {}
    this.readline = null;
    for (const [, entry] of this.pending) {
      clearTimeout(entry.timer);
      entry.reject(new Error("Codex App Server connection closed"));
    }
    this.pending.clear();
    try { this.process?.stdin?.end?.(); } catch {}
    try { this.process?.kill?.("SIGTERM"); } catch {}
    this.process = null;
  }
}

module.exports = {
  CodexAppServerConnection,
  buildCodexAppServerKey,
  buildCodexAppServerLaunch,
  DEFAULT_REQUEST_TIMEOUT_MS,
  INITIALIZE_TIMEOUT_MS,
};
