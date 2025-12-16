/**
 * Biometric Key Bridge - Termius-style Biometric SSH Keys
 * 
 * This module implements a biometric-protected SSH key system where:
 * 1. Standard ED25519 keys are generated using ssh-keygen
 * 2. A random UUID passphrase encrypts the private key
 * 3. The passphrase is stored in OS Secure Storage (Keychain/DPAPI via keytar)
 * 4. On use, the OS prompts for biometrics before releasing the passphrase
 * 
 * Platform behavior:
 * - macOS: Keychain automatically prompts for Touch ID / password
 * - Windows: Uses WebAuthn via renderer process (navigator.credentials API)
 *            to trigger Windows Hello UI properly
 */

const { spawn, execSync } = require("node:child_process");
const path = require("node:path");
const fs = require("node:fs");
const os = require("node:os");
const crypto = require("node:crypto");

// Service name for keytar (identifies our app in the credential store)
const KEYTAR_SERVICE = "com.netcatty.biometric-keys";
const WEBAUTHN_SERVICE = "com.netcatty.webauthn-hello";
const WEBAUTHN_ACCOUNT_PREFIX = "uvpa-credential-id:";

// Lazy-load keytar to handle cases where native module isn't available
let keytar = null;
function getKeytar() {
  if (keytar === null) {
    try {
      keytar = require("keytar");
    } catch (err) {
      console.error("[Biometric] Failed to load keytar:", err.message);
      keytar = false;
    }
  }
  return keytar || null;
}

// Pending WebAuthn requests to renderer
const pendingWebAuthnRequests = new Map();
let webauthnRequestCounter = 0;

/**
 * Invoke WebAuthn operation in the renderer process via IPC.
 * The renderer uses navigator.credentials API which properly handles
 * Windows foreground requirements.
 */
async function webauthnInvoke(op, params) {
  const { BrowserWindow } = require("electron");
  
  // Find a window to send the request to
  const windows = BrowserWindow.getAllWindows?.() || [];
  const targetWin = windows.find(w => w && !w.isDestroyed?.()) || null;
  
  if (!targetWin) {
    throw new Error("No Electron window available for WebAuthn");
  }

  const requestId = `webauthn-${++webauthnRequestCounter}-${Date.now()}`;
  
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      pendingWebAuthnRequests.delete(requestId);
      reject(new Error("WebAuthn operation timed out"));
    }, 120000); // 2 minute timeout for user interaction

    pendingWebAuthnRequests.set(requestId, { resolve, reject, timeout });

    // Send request to renderer
    targetWin.webContents.send("netcatty:webauthn:request", {
      requestId,
      op,
      params,
    });
  });
}

/**
 * Handle WebAuthn response from renderer process
 */
function handleWebAuthnResponse(event, payload) {
  const { requestId, ok, result, error } = payload || {};
  
  const pending = pendingWebAuthnRequests.get(requestId);
  if (!pending) return;
  
  pendingWebAuthnRequests.delete(requestId);
  clearTimeout(pending.timeout);
  
  if (ok) {
    pending.resolve(result);
  } else {
    pending.reject(new Error(error || "WebAuthn operation failed"));
  }
}

/**
 * Get the path to ssh-keygen executable
 */
function getSSHKeygenPath() {
  const platform = process.platform;

  if (platform === "win32") {
    // Windows native OpenSSH paths
    const systemRoot = process.env.SystemRoot || "C:\\Windows";
    const nativePaths = [
      path.join(systemRoot, "System32", "OpenSSH", "ssh-keygen.exe"),
      path.join(systemRoot, "Sysnative", "OpenSSH", "ssh-keygen.exe"),
    ];

    // Git for Windows paths
    const gitPaths = [
      process.env.ProgramFiles && path.join(process.env.ProgramFiles, "Git", "usr", "bin", "ssh-keygen.exe"),
      process.env["ProgramFiles(x86)"] && path.join(process.env["ProgramFiles(x86)"], "Git", "usr", "bin", "ssh-keygen.exe"),
    ].filter(Boolean);

    // Prefer native OpenSSH on Windows
    for (const nativePath of nativePaths) {
      if (fs.existsSync(nativePath)) {
        return nativePath;
      }
    }
    for (const gitPath of gitPaths) {
      if (fs.existsSync(gitPath)) {
        return gitPath;
      }
    }

    // Fallback to PATH
    try {
      const whereResult = execSync("where ssh-keygen", { encoding: "utf8", timeout: 5000 });
      const firstPath = whereResult.split("\n")[0]?.trim();
      if (firstPath && fs.existsSync(firstPath)) {
        return firstPath;
      }
    } catch {
      // Not found
    }
  } else {
    // macOS/Linux
    try {
      const whichResult = execSync("which ssh-keygen", { encoding: "utf8", timeout: 5000 });
      const sshKeygenPath = whichResult.trim();
      if (sshKeygenPath && fs.existsSync(sshKeygenPath)) {
        return sshKeygenPath;
      }
    } catch {
      // Not found
    }

    const commonPaths = [
      "/usr/bin/ssh-keygen",
      "/usr/local/bin/ssh-keygen",
      "/opt/homebrew/bin/ssh-keygen",
    ];

    for (const commonPath of commonPaths) {
      if (fs.existsSync(commonPath)) {
        return commonPath;
      }
    }
  }

  return null;
}

/**
 * Generate a high-entropy random passphrase
 */
function generateRandomPassphrase() {
  // Use UUID v4 for high-entropy passphrase (122 bits of randomness)
  return crypto.randomUUID();
}

/**
 * Check if biometric key features are available on this system
 */
async function checkBiometricSupport() {
  const kt = getKeytar();
  const sshKeygenPath = getSSHKeygenPath();
  const platform = process.platform;
  
  const result = {
    supported: false,
    hasKeytar: !!kt,
    hasSshKeygen: !!sshKeygenPath,
    sshKeygenPath,
    platform,
    hasWindowsHello: false,
    error: null,
  };

  if (!kt) {
    result.error = "Keytar (secure storage) is not available";
    return result;
  }

  if (!sshKeygenPath) {
    result.error = "ssh-keygen is not available";
    return result;
  }

  // Check Windows Hello availability on Windows
  // NOTE: We can't reliably check WebAuthn from main process anymore
  // The renderer will check it via window.netcatty.webauthn.isAvailable()
  if (platform === "win32") {
    // Just assume it's available if we're on Windows 10+
    // The actual check will be done in the renderer
    result.hasWindowsHello = true;
  }

  result.supported = true;
  return result;
}

/**
 * Generate a biometric-protected SSH key
 * 
 * @param {Object} options
 * @param {string} options.keyId - Unique ID for this key (used as account name in keytar)
 * @param {string} options.label - Human-readable label for the key
 * @param {string} [options.windowsHelloCredentialId] - Pre-created WebAuthn credential ID from renderer
 * @returns {Promise<Object>} Result with publicKey, privateKey, or error
 */
async function generateBiometricKey(options) {
  const { keyId, label, windowsHelloCredentialId } = options;

  if (!keyId || !label) {
    return { success: false, error: "keyId and label are required" };
  }

  const kt = getKeytar();
  if (!kt) {
    return { success: false, error: "Secure storage (keytar) is not available" };
  }

  const sshKeygenPath = getSSHKeygenPath();
  if (!sshKeygenPath) {
    return { success: false, error: "ssh-keygen is not available" };
  }

  // On Windows, the renderer must have already created a WebAuthn credential
  // and passed it to us via windowsHelloCredentialId
  if (process.platform === "win32") {
    if (!windowsHelloCredentialId) {
      return { success: false, error: "Windows Hello credential must be created by the renderer first" };
    }
    
    // Store the credential ID for later verification
    const account = getWebauthnAccountForKeyId(keyId);
    await kt.setPassword(WEBAUTHN_SERVICE, account, windowsHelloCredentialId);
    console.log("[Biometric] Windows Hello credential ID stored:", windowsHelloCredentialId.substring(0, 20) + "...");
  }

  // Generate random passphrase
  const passphrase = generateRandomPassphrase();

  // Create temp directory for key generation
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "netcatty-biometric-"));
  const keyPath = path.join(tempDir, "id_ed25519");

  try {
    // Generate ED25519 key with passphrase using ssh-keygen
    console.log("[Biometric] Generating ED25519 key with passphrase...");
    
    await new Promise((resolve, reject) => {
      const args = [
        "-t", "ed25519",
        "-f", keyPath,
        "-N", passphrase,
        "-C", `${label}@netcatty-biometric`,
      ];

      const proc = spawn(sshKeygenPath, args, {
        stdio: ["pipe", "pipe", "pipe"],
        windowsHide: true,
      });

      let stdout = "";
      let stderr = "";

      proc.stdout.on("data", (data) => {
        stdout += data.toString();
      });

      proc.stderr.on("data", (data) => {
        stderr += data.toString();
      });

      proc.on("close", (code) => {
        if (code === 0) {
          resolve({ stdout, stderr });
        } else {
          reject(new Error(`ssh-keygen exited with code ${code}: ${stderr || stdout}`));
        }
      });

      proc.on("error", (err) => {
        reject(err);
      });
    });

    // Read the generated keys
    const privateKey = fs.readFileSync(keyPath, "utf8");
    const publicKey = fs.readFileSync(`${keyPath}.pub`, "utf8");

    // Store passphrase in OS secure storage
    console.log("[Biometric] Storing passphrase in secure storage...");
    await kt.setPassword(KEYTAR_SERVICE, keyId, passphrase);

    // Verify storage worked
    const storedPassphrase = await kt.getPassword(KEYTAR_SERVICE, keyId);
    if (storedPassphrase !== passphrase) {
      throw new Error("Failed to verify passphrase was stored correctly");
    }

    console.log("[Biometric] Key generated and passphrase stored successfully");

    return {
      success: true,
      privateKey,
      publicKey: publicKey.trim(),
      keyType: "ED25519",
    };
  } catch (err) {
    console.error("[Biometric] Key generation failed:", err);
    // Clean up stored passphrase on failure
    try {
      await kt.deletePassword(KEYTAR_SERVICE, keyId);
    } catch {
      // Ignore cleanup errors
    }
    return {
      success: false,
      error: err.message || "Key generation failed",
    };
  } finally {
    // Clean up temp files
    try {
      if (fs.existsSync(keyPath)) fs.unlinkSync(keyPath);
      if (fs.existsSync(`${keyPath}.pub`)) fs.unlinkSync(`${keyPath}.pub`);
      if (fs.existsSync(tempDir)) fs.rmdirSync(tempDir);
    } catch (err) {
      console.warn("[Biometric] Temp cleanup failed:", err.message);
    }
  }
}

function getWebauthnAccountForKeyId(keyId) {
  return `${WEBAUTHN_ACCOUNT_PREFIX}${keyId}`;
}

async function ensureWindowsHelloCredentialForKeyId(keyId, reason = "Set up Windows Hello") {
  if (process.platform !== "win32") return true;
  if (!keyId) return false;

  const kt = getKeytar();
  if (!kt) return false;

  try {
    console.log("[Biometric] " + reason + "...");
    
    // Check availability via renderer
    const available = await webauthnInvoke("isAvailable", {}).catch(() => false);
    if (!available) {
      console.warn("[Biometric] Windows Hello is not available on this system");
      return false;
    }

    // Ensure window is visible and focused
    try {
      const { BrowserWindow } = require("electron");
      const focused = BrowserWindow.getFocusedWindow?.();
      const parentWin = focused || BrowserWindow.getAllWindows?.()?.[0];
      if (parentWin && !parentWin.isDestroyed?.()) {
        if (parentWin.isMinimized?.()) {
          parentWin.restore?.();
        }
        parentWin.show?.();
        parentWin.focus?.();
      }
    } catch {
      // ignore
    }

    // Small delay to ensure window is ready
    await new Promise((r) => setTimeout(r, 100));

    await ensureWindowsHelloCredentialInternal({ kt, keyId });
    return true;
  } catch (err) {
    console.error("[Biometric] Windows Hello credential setup failed:", err);
    return false;
  }
}

async function ensureWindowsHelloCredentialInternal({ kt, keyId }) {
  if (!keyId) {
    throw new Error("keyId is required for Windows Hello credential storage");
  }

  const account = getWebauthnAccountForKeyId(keyId);

  let credentialIdB64 = await kt.getPassword(WEBAUTHN_SERVICE, account);

  if (!credentialIdB64) {
    const userName = (() => {
      try {
        return os.userInfo().username || "Netcatty";
      } catch {
        return "Netcatty";
      }
    })();

    console.log("[Biometric] Creating WebAuthn platform credential via renderer...");
    
    // Call renderer to create credential using navigator.credentials API
    credentialIdB64 = await webauthnInvoke("createCredential", { userName });
    
    if (!credentialIdB64) {
      throw new Error("Credential creation returned empty result");
    }

    await kt.setPassword(WEBAUTHN_SERVICE, account, credentialIdB64);
    console.log("[Biometric] WebAuthn credential created and stored");
  }

  return { credentialIdB64 };
}

/**
 * Retrieve the passphrase for a biometric key
 * On Windows, this first verifies the user with Windows Hello
 * On macOS, the Keychain automatically prompts for Touch ID
 * 
 * @param {string} keyId - The key ID used when generating the key
 * @returns {Promise<Object>} Result with passphrase or error
 */
async function getBiometricPassphrase(options) {
  const keyId = typeof options === "string" ? options : options?.keyId;
  const skipHello = typeof options === "object" && options?.skipHello === true;

  if (!keyId) return { success: false, error: "keyId is required" };

  const kt = getKeytar();
  if (!kt) {
    return { success: false, error: "Secure storage (keytar) is not available" };
  }

  try {
    // On Windows, verify with Windows Hello BEFORE accessing credential manager
    if (process.platform === "win32" && !skipHello) {
      console.log("[Biometric] Requesting Windows Hello verification...");
      const verified = await verifyWindowsHelloForKeyId(keyId, "Unlock SSH Key: " + keyId);
      if (!verified) {
        return { success: false, error: "Windows Hello verification failed or cancelled" };
      }
      console.log("[Biometric] Windows Hello verification successful");
    }

    // Retrieve passphrase from secure storage
    // On macOS, this will trigger Touch ID / password prompt automatically
    console.log("[Biometric] Retrieving passphrase from secure storage...");
    const passphrase = await kt.getPassword(KEYTAR_SERVICE, keyId);

    if (!passphrase) {
      return { success: false, error: "No passphrase found for this key" };
    }

    console.log("[Biometric] Passphrase retrieved successfully");
    return { success: true, passphrase };
  } catch (err) {
    console.error("[Biometric] Failed to retrieve passphrase:", err);
    return { success: false, error: err.message || "Failed to retrieve passphrase" };
  }
}

async function verifyWindowsHelloForKeyId(keyId, reason = "Unlock your SSH Key") {
  if (process.platform !== "win32") return true;
  if (!keyId) return false;

  try {
    // Ensure window is visible and focused
    try {
      const { BrowserWindow } = require("electron");
      const focused = BrowserWindow.getFocusedWindow?.();
      const win = focused || BrowserWindow.getAllWindows?.()?.[0];
      if (win && !win.isDestroyed?.()) {
        if (win.isMinimized?.()) {
          win.restore?.();
        }
        win.show?.();
        win.focus?.();
      }
    } catch {
      // ignore
    }

    // Small delay to ensure window is ready
    await new Promise((r) => setTimeout(r, 100));

    const kt = getKeytar();
    if (!kt) return false;

    const available = await webauthnInvoke("isAvailable", {}).catch(() => false);
    if (!available) return false;

    const { credentialIdB64 } = await ensureWindowsHelloCredentialInternal({
      kt,
      keyId,
    });

    console.log("[Biometric] Verifying with Windows Hello via renderer...");
    const verified = await webauthnInvoke("getAssertion", { credentialIdB64 });
    return !!verified;
  } catch (err) {
    console.error("[Biometric] Windows Hello verification failed:", err);
    return false;
  }
}

/**
 * Delete the stored passphrase for a biometric key
 * Should be called when the key is deleted
 * 
 * @param {string} keyId - The key ID
 * @returns {Promise<Object>} Result
 */
async function deleteBiometricPassphrase(keyId) {
  if (!keyId) {
    return { success: false, error: "keyId is required" };
  }

  const kt = getKeytar();
  if (!kt) {
    return { success: false, error: "Secure storage (keytar) is not available" };
  }

  try {
    const result = await kt.deletePassword(KEYTAR_SERVICE, keyId);
    console.log("[Biometric] Passphrase deleted:", result);
    // Best-effort cleanup of our stored WebAuthn credential linkage for this key.
    try {
      await kt.deletePassword(WEBAUTHN_SERVICE, getWebauthnAccountForKeyId(keyId));
    } catch {
      // ignore
    }
    return { success: true };
  } catch (err) {
    console.error("[Biometric] Failed to delete passphrase:", err);
    return { success: false, error: err.message };
  }
}

/**
 * List all stored biometric key IDs
 * Useful for cleanup and debugging
 * 
 * @returns {Promise<Object>} Result with array of keyIds
 */
async function listBiometricKeys() {
  const kt = getKeytar();
  if (!kt) {
    return { success: false, error: "Secure storage (keytar) is not available" };
  }

  try {
    const credentials = await kt.findCredentials(KEYTAR_SERVICE);
    const keyIds = credentials.map((c) => c.account);
    return { success: true, keyIds };
  } catch (err) {
    console.error("[Biometric] Failed to list keys:", err);
    return { success: false, error: err.message };
  }
}

/**
 * Register IPC handlers for biometric key operations
 */
function registerHandlers(ipcMain) {
  // Handle WebAuthn responses from renderer
  ipcMain.on("netcatty:webauthn:response", (event, payload) => {
    handleWebAuthnResponse(event, payload);
  });

  ipcMain.handle("netcatty:biometric:checkSupport", async () => {
    return checkBiometricSupport();
  });

  ipcMain.handle("netcatty:biometric:generate", async (_event, options) => {
    return generateBiometricKey(options);
  });

  ipcMain.handle("netcatty:biometric:getPassphrase", async (_event, options) => {
    return getBiometricPassphrase(options);
  });

  ipcMain.handle("netcatty:biometric:deletePassphrase", async (_event, options) => {
    return deleteBiometricPassphrase(options?.keyId);
  });

  ipcMain.handle("netcatty:biometric:listKeys", async () => {
    return listBiometricKeys();
  });
}

module.exports = {
  registerHandlers,
  checkBiometricSupport,
  generateBiometricKey,
  getBiometricPassphrase,
  deleteBiometricPassphrase,
  listBiometricKeys,
  KEYTAR_SERVICE,
};
