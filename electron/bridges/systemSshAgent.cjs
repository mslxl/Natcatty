"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { execFile } = require("node:child_process");
const { promisify } = require("node:util");
const { utils } = require("ssh2");
const { BaseAgent, createAgent } = require("ssh2/lib/agent.js");

const execFileAsync = promisify(execFile);

function publicKeyBlob(key) {
  try {
    const parsed = typeof key?.getPublicSSH === "function" ? key : utils.parseKey(key);
    if (parsed instanceof Error || typeof parsed?.getPublicSSH !== "function") return null;
    return parsed.getPublicSSH().toString("base64");
  } catch {
    return null;
  }
}

function resolveIdentityPath(rawPath, { hostname = "", username = "", env = process.env } = {}) {
  if (typeof rawPath !== "string") return "";
  let resolved = rawPath.trim()
    .replace(/%h/g, hostname)
    .replace(/%r/g, username)
    .replace(/%d/g, os.homedir())
    .replace(/^~(?=$|[\\/])/, os.homedir())
    .replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (_match, name) => env[name] ?? "")
    .replace(/\$([A-Za-z_][A-Za-z0-9_]*)/g, (_match, name) => env[name] ?? "");
  if (!path.isAbsolute(resolved) && resolved) {
    resolved = path.resolve(resolved);
  }
  return resolved;
}

async function loadPreferredPublicKeyBlobs(identityFilePaths, options, deps) {
  const preferred = new Set();
  const resolvedIdentityPaths = [];
  for (const rawPath of identityFilePaths ?? []) {
    const identityPath = resolveIdentityPath(rawPath, options);
    if (!identityPath) continue;
    resolvedIdentityPaths.push(identityPath);
    const publicKeyPath = identityPath.endsWith(".pub") ? identityPath : `${identityPath}.pub`;
    try {
      const contents = await deps.readFile(publicKeyPath, "utf8");
      const blob = publicKeyBlob(contents);
      if (blob) preferred.add(blob);
    } catch (error) {
      deps.log?.("Configured SSH public key is unavailable", {
        publicKeyPath,
        error: error instanceof Error ? error.message : String(error),
      });
    }
  }
  return { preferred, resolvedIdentityPaths };
}

function getIdentities(agent) {
  return new Promise((resolve, reject) => {
    agent.getIdentities((error, identities) => {
      if (error) reject(error);
      else resolve(Array.isArray(identities) ? identities : []);
    });
  });
}

class IdentityAwareAgent extends BaseAgent {
  constructor(delegate, preferred, identitiesOnly) {
    super();
    this.delegate = delegate;
    this.preferred = preferred;
    this.identitiesOnly = identitiesOnly;
  }

  getIdentities(callback) {
    this.delegate.getIdentities((error, identities) => {
      if (error) return callback(error);
      const keys = Array.isArray(identities) ? identities : [];
      const matching = [];
      const remaining = [];
      for (const key of keys) {
        if (this.preferred.has(publicKeyBlob(key))) matching.push(key);
        else remaining.push(key);
      }
      callback(null, this.identitiesOnly ? matching : [...matching, ...remaining]);
    });
  }

  sign(publicKey, data, options, callback) {
    this.delegate.sign(publicKey, data, options, callback);
  }
}

function shouldLoadFromMacKeychain(options, platform) {
  return platform === "darwin"
    && options.useKeychain === true
    && typeof options.addKeysToAgent === "string"
    && options.addKeysToAgent.toLowerCase() === "yes"
    && Array.isArray(options.identityFilePaths)
    && options.identityFilePaths.length > 0;
}

async function defaultRunSshAdd(args, { socketPath, env }) {
  await execFileAsync("/usr/bin/ssh-add", args, {
    timeout: 5000,
    windowsHide: true,
    env: {
      ...env,
      SSH_AUTH_SOCK: socketPath,
      SSH_ASKPASS_REQUIRE: "never",
    },
  });
}

async function prepareSystemSshAgent(options, injected = {}) {
  if (!options?.socketPath) return null;
  const deps = {
    createAgent: injected.createAgent ?? createAgent,
    readFile: injected.readFile ?? fs.promises.readFile,
    runSshAdd: injected.runSshAdd,
    platform: injected.platform ?? process.platform,
    env: injected.env ?? process.env,
    log: injected.log,
  };
  const agent = deps.createAgent(options.socketPath);
  const { preferred, resolvedIdentityPaths } = await loadPreferredPublicKeyBlobs(
    options.identityFilePaths,
    { hostname: options.hostname, username: options.username, env: deps.env },
    deps,
  );

  if (shouldLoadFromMacKeychain(options, deps.platform) && preferred.size > 0) {
    let loadedBlobs = new Set();
    try {
      loadedBlobs = new Set((await getIdentities(agent)).map(publicKeyBlob).filter(Boolean));
    } catch (error) {
      deps.log?.("Could not inspect SSH agent identities before Keychain load", {
        error: error instanceof Error ? error.message : String(error),
      });
    }
    const hasPreferredIdentity = [...preferred].some((blob) => loadedBlobs.has(blob));
    if (!hasPreferredIdentity) {
      try {
        const args = ["--apple-load-keychain", ...resolvedIdentityPaths];
        if (deps.runSshAdd) await deps.runSshAdd(args);
        else await defaultRunSshAdd(args, { socketPath: options.socketPath, env: deps.env });
      } catch (error) {
        deps.log?.("macOS Keychain could not load the configured SSH identity", {
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }
  }

  return new IdentityAwareAgent(agent, preferred, options.identitiesOnly === true);
}

module.exports = {
  IdentityAwareAgent,
  prepareSystemSshAgent,
  publicKeyBlob,
  resolveIdentityPath,
  shouldLoadFromMacKeychain,
};
