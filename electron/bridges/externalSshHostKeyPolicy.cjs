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
 *   - When verifyHostKeys is enabled (default), inject vault entries that
 *     carry a full public-key blob as GlobalKnownHostsFile so OpenSSH checks
 *     them alongside the user's ~/.ssh/known_hosts.
 *   - ET keeps StrictHostKeyChecking=accept-new (cannot answer interactive
 *     yes/no via SSH_ASKPASS; accept-new still rejects changed keys).
 *   - Mosh runs OpenSSH in an interactive PTY, so it leaves the default
 *     ask/accept policy alone and only adds the vault trust source unless
 *     verification is disabled.
 *   - When verifyHostKeys is false, force StrictHostKeyChecking=no to match
 *     the in-app SSH setting.
 */

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
 * @param {string|null|undefined} opts.vaultKnownHostsPath
 * @param {boolean} [opts.verifyHostKeys=true]
 * @param {"et"|"mosh"} [opts.protocol="et"]
 * @param {"args"|"values"} [opts.style="values"]
 *   - values: ["StrictHostKeyChecking=accept-new", ...] (ET --ssh-option)
 *   - args: ["-o", "StrictHostKeyChecking=accept-new", ...] (Mosh ssh argv)
 * @param {(p: string) => string} [opts.normalizePath]
 * @returns {string[]}
 */
const buildExternalHostKeySshOptions = ({
  vaultKnownHostsPath,
  verifyHostKeys = true,
  protocol = "et",
  style = "values",
  normalizePath = (p) => p,
} = {}) => {
  const values = [];
  const vaultPath = typeof vaultKnownHostsPath === "string" && vaultKnownHostsPath.trim()
    ? normalizePath(vaultKnownHostsPath.trim())
    : "";
  if (vaultPath) {
    // GlobalKnownHostsFile is read-only trust input; new keys still land in
    // UserKnownHostsFile (system ~/.ssh/known_hosts for ET / default for Mosh).
    values.push(`GlobalKnownHostsFile=${vaultPath}`);
  }
  const strict = resolveExternalStrictHostKeyChecking({ verifyHostKeys, protocol });
  if (strict) {
    values.push(`StrictHostKeyChecking=${strict}`);
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
  vaultKnownHostsPath,
  verifyHostKeys = true,
  protocol = "et",
  indent = "  ",
  normalizePath = (p) => p,
  quotePath = (v) => v,
} = {}) => {
  const values = buildExternalHostKeySshOptions({
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
  buildVaultKnownHostsContent,
  formatVaultKnownHostLine,
  resolveExternalStrictHostKeyChecking,
};
