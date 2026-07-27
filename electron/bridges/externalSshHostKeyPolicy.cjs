/**
 * Host-key policy helpers for external OpenSSH-driven protocols (Mosh, ET).
 *
 * Netcatty's in-app SSH path uses ssh2 + hostKeyVerifier with a renderer
 * confirmation dialog. Mosh and Eternal Terminal bootstrap via system
 * OpenSSH instead, so they cannot share that dialog path. They still need
 * the vault known_hosts snapshot for MITM protection: keys the user already
 * trusted through Netcatty SSH must reject when the live server presents a
 * different key of the same type.
 *
 * Strategy (aligned with issue #2501 user priority — key-change intercept):
 *   - When verifyHostKeys is enabled (default) and the vault has usable
 *     public-key blobs, write a single GlobalKnownHostsFile that merges
 *     OpenSSH's default global files (/etc/ssh/ssh_known_hosts{,2} or the
 *     Windows ProgramData equivalents) with the vault. Replacing
 *     GlobalKnownHostsFile without merging would drop admin-pinned hosts.
 *   - ET keeps StrictHostKeyChecking=accept-new (cannot answer interactive
 *     yes/no via SSH_ASKPASS; accept-new still rejects changed keys).
 *   - Mosh runs OpenSSH in an interactive PTY, so it leaves the default
 *     ask/accept policy alone and only adds the merged trust source unless
 *     verification is disabled.
 *   - When verifyHostKeys is false, force StrictHostKeyChecking=no and point
 *     both UserKnownHostsFile and GlobalKnownHostsFile at an empty file.
 *     OpenSSH still consults known_hosts under `no` for password-auth MITM
 *     protection, so an outdated vault/system pin would otherwise still
 *     reject password / keyboard-interactive auth.
 */

const path = require("node:path");

const formatVaultKnownHostLine = (knownHost) => {
  if (!knownHost?.hostname) return null;
  const port = Number.isFinite(knownHost.port) ? Number(knownHost.port) : 22;
  const hostField = port !== 22 ? `[${knownHost.hostname}]:${port}` : knownHost.hostname;
  const pubKey = String(knownHost.publicKey || "").trim();
  const parts = pubKey.split(/\s+/);
  let keyType = typeof knownHost.keyType === "string" ? knownHost.keyType.trim() : "";
  let keyBlob = "";
  if (parts.length >= 2 && /^ssh-|^ecdsa-|^sk-/.test(parts[0])) {
    keyType = parts[0];
    keyBlob = parts[1];
  } else if (parts.length === 1 && parts[0].length > 0 && !/^SHA256:/i.test(parts[0])) {
    keyBlob = parts[0];
  } else {
    return null;
  }
  if (!keyType || !keyBlob) return null;
  return `${hostField} ${keyType} ${keyBlob}`;
};

const buildVaultKnownHostsContent = (knownHosts) => {
  if (!Array.isArray(knownHosts) || knownHosts.length === 0) return "";
  const lines = [];
  for (const knownHost of knownHosts) {
    const line = formatVaultKnownHostLine(knownHost);
    if (line) lines.push(line);
  }
  if (lines.length === 0) return "";
  return `${lines.join("\n")}\n`;
};

/**
 * OpenSSH default GlobalKnownHostsFile locations.
 * Matches `ssh -G -F /dev/null` on OpenSSH 9.x (Unix) and Windows OpenSSH.
 */
const getDefaultGlobalKnownHostsPaths = ({
  platform = process.platform,
  programData = process.env.ProgramData,
  pathModule = path,
} = {}) => {
  if (platform === "win32") {
    const base = programData || "C:\\ProgramData";
    return [
      pathModule.join(base, "ssh", "ssh_known_hosts"),
      pathModule.join(base, "ssh", "ssh_known_hosts2"),
    ];
  }
  return [
    "/etc/ssh/ssh_known_hosts",
    "/etc/ssh/ssh_known_hosts2",
  ];
};

const readKnownHostsFileContent = (fsApi, filePath) => {
  if (!fsApi || !filePath) return "";
  try {
    if (typeof fsApi.existsSync === "function" && !fsApi.existsSync(filePath)) {
      return "";
    }
    const content = fsApi.readFileSync(filePath, "utf8");
    return typeof content === "string" && content.trim() ? content.trimEnd() : "";
  } catch {
    return "";
  }
};

/**
 * Merge OpenSSH default global known_hosts files with Netcatty vault entries.
 * Returns "" when there is nothing usable to inject (caller should leave
 * GlobalKnownHostsFile unset so OpenSSH keeps its built-in defaults).
 */
const buildMergedGlobalKnownHostsContent = ({
  knownHosts,
  fs: fsApi,
  platform = process.platform,
  programData = process.env.ProgramData,
  pathModule = path,
  globalPaths,
} = {}) => {
  const chunks = [];
  const defaults = Array.isArray(globalPaths) && globalPaths.length > 0
    ? globalPaths
    : getDefaultGlobalKnownHostsPaths({ platform, programData, pathModule });
  for (const filePath of defaults) {
    const content = readKnownHostsFileContent(fsApi, filePath);
    if (content) chunks.push(content);
  }
  const vaultContent = buildVaultKnownHostsContent(knownHosts).trimEnd();
  // Only emit a custom GlobalKnownHostsFile when the vault contributes pins.
  // If the vault is empty, leave OpenSSH's built-in global defaults alone.
  if (!vaultContent) return "";
  chunks.push(vaultContent);
  return `${chunks.join("\n")}\n`;
};

/**
 * @param {object} opts
 * @param {boolean} [opts.verifyHostKeys=true]
 * @param {"et"|"mosh"} [opts.protocol="et"]
 * @returns {"accept-new"|"no"|null} null means "do not override OpenSSH default"
 */
const resolveExternalStrictHostKeyChecking = ({
  verifyHostKeys = true,
  protocol = "et",
} = {}) => {
  if (verifyHostKeys === false) return "no";
  // ET cannot answer OpenSSH's interactive host-key prompt (SSH_ASKPASS only
  // covers passwords/passphrases). accept-new still rejects a changed key.
  if (protocol === "et") return "accept-new";
  // Mosh's handshake PTY can show OpenSSH's ask prompt for first-seen hosts.
  return null;
};

/**
 * Build OpenSSH -o style option strings (or bare KEY=VALUE for ET --ssh-option).
 *
 * @param {object} opts
 * @param {string|null|undefined} opts.mergedGlobalKnownHostsPath
 *   Path to a file that already merges default global known_hosts + vault.
 *   Only used when verification is enabled. Omit / null when the vault is
 *   empty so OpenSSH keeps its built-in GlobalKnownHostsFile defaults.
 * @param {string|null|undefined} opts.emptyKnownHostsPath
 *   Empty trust file used when verification is disabled so OpenSSH cannot
 *   still block password auth against a stale vault/system pin.
 * @param {boolean} [opts.verifyHostKeys=true]
 * @param {"et"|"mosh"} [opts.protocol="et"]
 * @param {"args"|"values"} [opts.style="values"]
 * @param {(p: string) => string} [opts.normalizePath]
 * @returns {string[]}
 */
const buildExternalHostKeySshOptions = ({
  mergedGlobalKnownHostsPath,
  emptyKnownHostsPath,
  // Back-compat alias used by earlier call sites / tests.
  vaultKnownHostsPath,
  verifyHostKeys = true,
  protocol = "et",
  style = "values",
  normalizePath = (p) => p,
} = {}) => {
  const values = [];
  const normalize = (p) => {
    if (typeof p !== "string" || !p.trim()) return "";
    return normalizePath(p.trim());
  };

  if (verifyHostKeys === false) {
    const emptyPath = normalize(emptyKnownHostsPath);
    if (emptyPath) {
      // Neutralize every trust source. StrictHostKeyChecking=no alone is not
      // enough: OpenSSH still refuses password auth when a known_hosts pin
      // mismatches the live key.
      values.push(`UserKnownHostsFile=${emptyPath}`);
      values.push(`GlobalKnownHostsFile=${emptyPath}`);
    }
    values.push("StrictHostKeyChecking=no");
  } else {
    const trustPath = normalize(mergedGlobalKnownHostsPath || vaultKnownHostsPath);
    if (trustPath) {
      // Read-only trust input. New keys still land in the caller's
      // UserKnownHostsFile (system ~/.ssh/known_hosts for ET / default Mosh).
      values.push(`GlobalKnownHostsFile=${trustPath}`);
    }
    const strict = resolveExternalStrictHostKeyChecking({ verifyHostKeys, protocol });
    if (strict) {
      values.push(`StrictHostKeyChecking=${strict}`);
    }
  }

  if (style === "args") {
    const args = [];
    for (const value of values) {
      args.push("-o", value);
    }
    return args;
  }
  return values;
};

/**
 * SSH config Host-block lines (indented) for jump-host stanzas.
 *
 * Path-valued options (GlobalKnownHostsFile / UserKnownHostsFile) may need
 * quoting and path normalization. Enum-valued options such as
 * StrictHostKeyChecking=accept-new must stay literal — path-quoting helpers
 * would resolve "accept-new" into a filesystem path.
 */
const buildExternalHostKeyConfigLines = ({
  mergedGlobalKnownHostsPath,
  emptyKnownHostsPath,
  vaultKnownHostsPath,
  verifyHostKeys = true,
  protocol = "et",
  indent = "  ",
  normalizePath = (p) => p,
  quotePath = (v) => v,
} = {}) => {
  const values = buildExternalHostKeySshOptions({
    mergedGlobalKnownHostsPath,
    emptyKnownHostsPath,
    vaultKnownHostsPath,
    verifyHostKeys,
    protocol,
    style: "values",
    normalizePath,
  });
  return values.map((value) => {
    const eq = value.indexOf("=");
    if (eq <= 0) return `${indent}${value}`;
    const key = value.slice(0, eq);
    const raw = value.slice(eq + 1);
    if (key === "GlobalKnownHostsFile" || key === "UserKnownHostsFile") {
      return `${indent}${key} ${quotePath(raw)}`;
    }
    return `${indent}${key} ${raw}`;
  });
};

module.exports = {
  buildExternalHostKeyConfigLines,
  buildExternalHostKeySshOptions,
  buildMergedGlobalKnownHostsContent,
  buildVaultKnownHostsContent,
  formatVaultKnownHostLine,
  getDefaultGlobalKnownHostsPaths,
  resolveExternalStrictHostKeyChecking,
};
