/**
 * Credential Bridge - Field-level encryption for sensitive data at rest
 *
 * Uses Electron's safeStorage API to encrypt individual sensitive fields
 * (passwords, tokens, private keys) before they are persisted to localStorage.
 *
 * Sentinel prefix "enc:v1:" on encrypted values enables:
 * - Detection of already-encrypted vs plaintext (migration)
 * - No double-encryption
 * - Future re-keying with enc:v2: etc.
 *
 * When safeStorage is unavailable (e.g. Linux without libsecret), all values
 * pass through unmodified so the app still works.
 */

const ENC_PREFIX = "enc:v1:";

let safeStorage = null;

/**
 * Register IPC handlers for credential encryption/decryption
 * @param {Electron.IpcMain} ipcMain
 * @param {typeof Electron} electronModule
 */
function registerHandlers(ipcMain, electronModule) {
  safeStorage = electronModule?.safeStorage ?? null;

  ipcMain.handle("netcatty:credentials:available", () => {
    return Boolean(safeStorage?.isEncryptionAvailable?.());
  });

  ipcMain.handle("netcatty:credentials:encrypt", (_event, plaintext) => {
    if (typeof plaintext !== "string" || plaintext.length === 0) {
      return plaintext ?? "";
    }
    if (!safeStorage?.isEncryptionAvailable?.()) {
      return plaintext;
    }
    // If value looks like it might already be encrypted, verify by attempting
    // to decode and decrypt.  If it succeeds the value is genuinely encrypted
    // and we return it as-is; if it fails, the prefix was a coincidence and
    // we proceed to encrypt the raw plaintext.
    if (plaintext.startsWith(ENC_PREFIX)) {
      try {
        const base64 = plaintext.slice(ENC_PREFIX.length);
        const buf = Buffer.from(base64, "base64");
        safeStorage.decryptString(buf); // throws on invalid ciphertext
        return plaintext; // verified — already encrypted
      } catch {
        // Not valid ciphertext — fall through to encrypt
      }
    }
    try {
      const encrypted = safeStorage.encryptString(plaintext);
      return ENC_PREFIX + encrypted.toString("base64");
    } catch (err) {
      console.warn("[Credentials] encrypt failed, returning plaintext:", err?.message || err);
      return plaintext;
    }
  });

  ipcMain.handle("netcatty:credentials:decrypt", (_event, value) => {
    if (typeof value !== "string" || value.length === 0) {
      return value ?? "";
    }
    // Not encrypted — pass through (supports migration from plaintext)
    if (!value.startsWith(ENC_PREFIX)) {
      return value;
    }
    if (!safeStorage?.isEncryptionAvailable?.()) {
      // Cannot decrypt without safeStorage; return raw value
      return value;
    }
    try {
      const base64 = value.slice(ENC_PREFIX.length);
      const buf = Buffer.from(base64, "base64");
      return safeStorage.decryptString(buf);
    } catch (err) {
      console.warn("[Credentials] decrypt failed:", err?.message || err);
      return value;
    }
  });
}

module.exports = { registerHandlers };
