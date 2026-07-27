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
 *   - When verifyHostKeys is enabled and the vault has usable public-key
 *     blobs, build one authoritative known_hosts file that contains:
 *       1. vault pins (sole source of truth for those hosts), and
 *       2. OpenSSH default global + user known_hosts lines for hosts that
 *          are NOT vault-pinned.
 *     Setting GlobalKnownHostsFile=/vault-only would drop admin pins; unioning
 *     vault with the full system files would let a system K2 override vault K1
 *     because OpenSSH accepts an exact match from ANY trust source.
 *   - Point both UserKnownHostsFile and GlobalKnownHostsFile at that
 *     authoritative snapshot (or empty Global) so no unfiltered system file
 *     remains in the search path.
 *   - ET uses StrictHostKeyChecking=accept-new (SSH_ASKPASS cannot answer
 *     interactive yes/no). Mosh uses explicit StrictHostKeyChecking=ask so a
 *     permissive user ssh_config cannot disable verification.
 *   - When verifyHostKeys is false, force StrictHostKeyChecking=no and point
 *     both trust files at an empty snapshot (OpenSSH still consults
 *     known_hosts under `no` for password-auth MITM protection).
 *   - Path-valued option values are quoted when they contain whitespace so
 *     OpenSSH does not split them into multiple filenames.
 */

const crypto = require("node:crypto");
const path = require("node:path");
const os = require("node:os");

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
 * Host/port pairs that vault entries pin. Used to filter system known_hosts
 * so vault remains authoritative for those hosts.
 */
const extractVaultHostSelectors = (knownHosts) => {
  const selectors = [];
  if (!Array.isArray(knownHosts)) return selectors;
  for (const knownHost of knownHosts) {
    if (!formatVaultKnownHostLine(knownHost)) continue;
    const hostname = String(knownHost.hostname || "").trim().toLowerCase();
    if (!hostname) continue;
    const port = Number.isFinite(knownHost.port) ? Number(knownHost.port) : 22;
    selectors.push({ hostname, port });
  }
  return selectors;
};

const normalizeHostname = (value) => String(value || "").trim().toLowerCase();

const buildHashLookupTokens = (hostname, port) => {
  const raw = String(hostname || "").trim();
  if (!raw) return [];
  const variants = new Set([raw, raw.toLowerCase()]);
  const tokens = new Set();
  const usePort = Number.isFinite(port) && Number(port) !== 22;
  for (const variant of variants) {
    tokens.add(usePort ? `[${variant}]:${Number(port)}` : variant);
  }
  return [...tokens];
};

const plainHostPatternMatchesSelector = (token, selector) => {
  if (!token || token.startsWith("!") || token.includes("*") || token.includes("?")) {
    return false;
  }
  const bracket = token.match(/^\[([^\]]+)\]:(\d+)$/);
  if (bracket) {
    return (
      normalizeHostname(bracket[1]) === selector.hostname
      && Number.parseInt(bracket[2], 10) === selector.port
    );
  }
  return normalizeHostname(token) === selector.hostname && selector.port === 22;
};

const hashedHostFieldMatchesSelector = (hostField, selector) => {
  const field = String(hostField || "");
  if (!field.startsWith("|1|")) return false;
  const rest = field.slice(3);
  const sep = rest.indexOf("|");
  if (sep <= 0) return false;
  let salt;
  let expectedBuf;
  try {
    salt = Buffer.from(rest.slice(0, sep), "base64");
    expectedBuf = Buffer.from(rest.slice(sep + 1), "base64");
  } catch {
    return false;
  }
  if (!salt.length || !expectedBuf.length) return false;
  for (const token of buildHashLookupTokens(selector.hostname, selector.port)) {
    let computed;
    try {
      computed = crypto.createHmac("sha1", salt).update(token).digest();
    } catch {
      continue;
    }
    if (
      computed.length === expectedBuf.length
      && crypto.timingSafeEqual(computed, expectedBuf)
    ) {
      return true;
    }
  }
  return false;
};

const hostFieldMatchesAnyVaultSelector = (hostField, selectors) => {
  if (!selectors.length) return false;
  const field = String(hostField || "").trim();
  if (!field) return false;
  if (field.startsWith("|1|")) {
    return selectors.some((selector) => hashedHostFieldMatchesSelector(field, selector));
  }
  const patterns = field.split(",");
  for (const pattern of patterns) {
    const token = pattern.trim();
    if (!token) continue;
    if (selectors.some((selector) => plainHostPatternMatchesSelector(token, selector))) {
      return true;
    }
  }
  return false;
};

/**
 * Drop known_hosts lines that pin any vault-covered host so vault keys are
 * the only trust source for those hosts. OpenSSH accepts a match from ANY
 * configured file; leaving a system K2 next to vault K1 would let K2 win.
 */
const filterKnownHostsContentExcludingVaultHosts = (content, vaultSelectors) => {
  if (!content || !vaultSelectors?.length) {
    return typeof content === "string" && content.trim() ? content.trimEnd() : "";
  }
  const kept = [];
  for (const rawLine of String(content).split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) {
      if (line.startsWith("#")) kept.push(rawLine.trimEnd());
      continue;
    }
    let rest = line;
    while (rest.startsWith("@")) {
      const spaceIdx = rest.search(/\s/);
      if (spaceIdx < 0) {
        rest = "";
        break;
      }
      rest = rest.slice(spaceIdx).trim();
    }
    if (!rest) {
      kept.push(rawLine.trimEnd());
      continue;
    }
    const hostField = rest.split(/\s+/)[0];
    if (hostFieldMatchesAnyVaultSelector(hostField, vaultSelectors)) {
      continue;
    }
    kept.push(rawLine.trimEnd());
  }
  return kept.filter(Boolean).join("\n");
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

const getDefaultUserKnownHostsPaths = ({
  homedir = os.homedir(),
  pathModule = path,
} = {}) => ([
  pathModule.join(homedir, ".ssh", "known_hosts"),
  pathModule.join(homedir, ".ssh", "known_hosts2"),
]);

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
 * Build the authoritative known_hosts content used when vault pins exist.
 * Returns "" when the vault has no usable pins (caller should leave OpenSSH
 * defaults alone).
 */
const buildAuthoritativeKnownHostsContent = ({
  knownHosts,
  fs: fsApi,
  platform = process.platform,
  programData = process.env.ProgramData,
  homedir = os.homedir(),
  pathModule = path,
  globalPaths,
  userPaths,
} = {}) => {
  const vaultContent = buildVaultKnownHostsContent(knownHosts).trimEnd();
  if (!vaultContent) return "";

  const vaultSelectors = extractVaultHostSelectors(knownHosts);
  const chunks = [vaultContent];

  const globals = Array.isArray(globalPaths) && globalPaths.length > 0
    ? globalPaths
    : getDefaultGlobalKnownHostsPaths({ platform, programData, pathModule });
  for (const filePath of globals) {
    const filtered = filterKnownHostsContentExcludingVaultHosts(
      readKnownHostsFileContent(fsApi, filePath),
      vaultSelectors,
    );
    if (filtered) chunks.push(filtered);
  }

  const users = Array.isArray(userPaths) && userPaths.length > 0
    ? userPaths
    : getDefaultUserKnownHostsPaths({ homedir, pathModule });
  for (const filePath of users) {
    const filtered = filterKnownHostsContentExcludingVaultHosts(
      readKnownHostsFileContent(fsApi, filePath),
      vaultSelectors,
    );
    if (filtered) chunks.push(filtered);
  }

  return `${chunks.join("\n")}\n`;
};

// Back-compat name used by earlier call sites / tests.
const buildMergedGlobalKnownHostsContent = (opts = {}) =>
  buildAuthoritativeKnownHostsContent(opts);

/**
 * @param {object} opts
 * @param {boolean} [opts.verifyHostKeys=true]
 * @param {"et"|"mosh"} [opts.protocol="et"]
 * @returns {"accept-new"|"ask"|"no"}
 */
const resolveExternalStrictHostKeyChecking = ({
  verifyHostKeys = true,
  protocol = "et",
} = {}) => {
  if (verifyHostKeys === false) return "no";
  // ET cannot answer OpenSSH's interactive host-key prompt (SSH_ASKPASS only
  // covers passwords/passphrases). accept-new still rejects a changed key.
  if (protocol === "et") return "accept-new";
  // Force ask for Mosh so a user ssh_config StrictHostKeyChecking=no/off
  // cannot disable Netcatty's verification setting.
  return "ask";
};

/**
 * Quote an OpenSSH option value when it contains whitespace or quotes so
 * path-valued options are not split into multiple filenames.
 */
const quoteOpenSshOptionValue = (value) => {
  const text = String(value ?? "");
  if (!text) return text;
  if (!/[\s"]/.test(text)) return text;
  return `"${text.replace(/(["\\])/g, "\\$1")}"`;
};

/**
 * Build OpenSSH -o style option strings (or bare KEY=VALUE for ET --ssh-option).
 *
 * @param {object} opts
 * @param {string|null|undefined} opts.authoritativeKnownHostsPath
 *   Path to the vault-authoritative known_hosts snapshot (vault pins +
 *   filtered system entries). When set under verifyHostKeys=true, both
 *   UserKnownHostsFile and GlobalKnownHostsFile point here so no unfiltered
 *   system file remains.
 * @param {string|null|undefined} opts.mergedGlobalKnownHostsPath
 *   Alias for authoritativeKnownHostsPath (back-compat).
 * @param {string|null|undefined} opts.emptyKnownHostsPath
 *   Empty trust file used when verification is disabled.
 * @param {boolean} [opts.verifyHostKeys=true]
 * @param {"et"|"mosh"} [opts.protocol="et"]
 * @param {"args"|"values"} [opts.style="values"]
 * @param {(p: string) => string} [opts.normalizePath]
 * @returns {string[]}
 */
const buildExternalHostKeySshOptions = ({
  authoritativeKnownHostsPath,
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
      const quoted = quoteOpenSshOptionValue(emptyPath);
      // Neutralize every trust source. StrictHostKeyChecking=no alone is not
      // enough: OpenSSH still refuses password auth when a known_hosts pin
      // mismatches the live key.
      values.push(`UserKnownHostsFile=${quoted}`);
      values.push(`GlobalKnownHostsFile=${quoted}`);
    }
    values.push("StrictHostKeyChecking=no");
  } else {
    const trustPath = normalize(
      authoritativeKnownHostsPath
      || mergedGlobalKnownHostsPath
      || vaultKnownHostsPath,
    );
    if (trustPath) {
      const quoted = quoteOpenSshOptionValue(trustPath);
      // Vault-authoritative snapshot for both slots so OpenSSH cannot fall
      // back to an unfiltered system known_hosts that still pins a rotated key.
      values.push(`UserKnownHostsFile=${quoted}`);
      values.push(`GlobalKnownHostsFile=${quoted}`);
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
  authoritativeKnownHostsPath,
  mergedGlobalKnownHostsPath,
  emptyKnownHostsPath,
  vaultKnownHostsPath,
  verifyHostKeys = true,
  protocol = "et",
  indent = "  ",
  normalizePath = (p) => p,
  quotePath = (v) => quoteOpenSshOptionValue(v),
} = {}) => {
  const values = buildExternalHostKeySshOptions({
    authoritativeKnownHostsPath,
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
    let raw = value.slice(eq + 1);
    // Values may already be quoted by buildExternalHostKeySshOptions.
    if (
      (key === "GlobalKnownHostsFile" || key === "UserKnownHostsFile")
      && !(raw.startsWith('"') && raw.endsWith('"'))
    ) {
      raw = quotePath(raw);
    }
    return `${indent}${key} ${raw}`;
  });
};

module.exports = {
  buildAuthoritativeKnownHostsContent,
  buildExternalHostKeyConfigLines,
  buildExternalHostKeySshOptions,
  buildMergedGlobalKnownHostsContent,
  buildVaultKnownHostsContent,
  extractVaultHostSelectors,
  filterKnownHostsContentExcludingVaultHosts,
  formatVaultKnownHostLine,
  getDefaultGlobalKnownHostsPaths,
  getDefaultUserKnownHostsPaths,
  quoteOpenSshOptionValue,
  resolveExternalStrictHostKeyChecking,
};
