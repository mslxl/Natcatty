/**
 * FIDO2 SSH Key Generation Bridge
 * 
 * Provides hardware security key enumeration and ed25519-sk key generation
 * using ssh-keygen with interactive PIN/touch support.
 */

const { spawn, execSync } = require("node:child_process");
const path = require("node:path");
const fs = require("node:fs");
const os = require("node:os");

// Track active generation processes
const activeProcesses = new Map();

/**
 * Get the path to ssh-keygen executable
 * 
 * Priority depends on device type:
 * - For platform authenticators (Windows Hello): Use Windows native OpenSSH (has proper Windows FIDO2 support)
 * - For USB security keys: Git for Windows works well
 * - Fallback to PATH
 * 
 * @param {boolean} forPlatformAuth - True if using platform authenticator (Windows Hello/Touch ID)
 */
function getSSHKeygenPath(forPlatformAuth = false) {
  const platform = process.platform;

  if (platform === "win32") {
    // Windows native OpenSSH paths
    const systemRoot = process.env.SystemRoot || "C:\\Windows";
    const nativePaths = [
      path.join(systemRoot, "System32", "OpenSSH", "ssh-keygen.exe"),
      // Handle 32-bit app on 64-bit OS (Sysnative redirects to 64-bit System32)
      path.join(systemRoot, "Sysnative", "OpenSSH", "ssh-keygen.exe"),
    ];

    // Git for Windows paths
    const gitPaths = [
      process.env.ProgramFiles && path.join(process.env.ProgramFiles, "Git", "usr", "bin", "ssh-keygen.exe"),
      process.env["ProgramFiles(x86)"] && path.join(process.env["ProgramFiles(x86)"], "Git", "usr", "bin", "ssh-keygen.exe"),
    ].filter(Boolean);

    if (forPlatformAuth) {
      // For Windows Hello: Prefer Windows native OpenSSH (better platform auth support)
      for (const nativePath of nativePaths) {
        if (fs.existsSync(nativePath)) {
          console.log("[FIDO2] Using Windows native ssh-keygen for platform auth:", nativePath);
          return nativePath;
        }
      }
      // Fall back to Git if native not available
      for (const gitPath of gitPaths) {
        if (fs.existsSync(gitPath)) {
          console.log("[FIDO2] Using Git ssh-keygen (native unavailable):", gitPath);
          return gitPath;
        }
      }
    } else {
      // For USB keys: Git for Windows often has better USB FIDO2 library support
      for (const gitPath of gitPaths) {
        if (fs.existsSync(gitPath)) {
          console.log("[FIDO2] Using Git for Windows ssh-keygen for USB key:", gitPath);
          return gitPath;
        }
      }
      // Fall back to native if Git not available
      for (const nativePath of nativePaths) {
        if (fs.existsSync(nativePath)) {
          console.log("[FIDO2] Using Windows native ssh-keygen:", nativePath);
          return nativePath;
        }
      }
    }

    // Last resort: System PATH
    try {
      const whereResult = execSync("where ssh-keygen", { encoding: "utf8", timeout: 5000 });
      const firstPath = whereResult.split("\n")[0]?.trim();
      if (firstPath && fs.existsSync(firstPath)) {
        console.log("[FIDO2] Using ssh-keygen from PATH:", firstPath);
        return firstPath;
      }
    } catch {
      // Not found in PATH
    }
  } else {
    // macOS/Linux: use 'which' to find ssh-keygen
    try {
      const whichResult = execSync("which ssh-keygen", { encoding: "utf8", timeout: 5000 });
      const sshKeygenPath = whichResult.trim();
      if (sshKeygenPath && fs.existsSync(sshKeygenPath)) {
        console.log("[FIDO2] Using ssh-keygen:", sshKeygenPath);
        return sshKeygenPath;
      }
    } catch {
      // Not found
    }

    // Common locations on macOS/Linux
    const commonPaths = [
      "/usr/bin/ssh-keygen",
      "/usr/local/bin/ssh-keygen",
      "/opt/homebrew/bin/ssh-keygen",
    ];

    for (const commonPath of commonPaths) {
      if (fs.existsSync(commonPath)) {
        console.log("[FIDO2] Using ssh-keygen:", commonPath);
        return commonPath;
      }
    }
  }

  return null;
}

/**
 * List available FIDO2 authenticator devices
 * Returns an array of device info objects
 */
async function listFido2Devices() {
  const devices = [];

  // Try to use node-hid for USB device enumeration
  try {
    const HID = require("node-hid");
    const hidDevices = HID.devices();

    // FIDO Alliance usage page is 0xF1D0
    const fidoDevices = hidDevices.filter(d => d.usagePage === 0xF1D0);

    for (const device of fidoDevices) {
      const label = device.product || device.manufacturer || "Unknown FIDO2 Device";
      devices.push({
        id: `usb:${device.vendorId}:${device.productId}:${device.path || ""}`,
        label: label,
        manufacturer: device.manufacturer || "Unknown",
        path: device.path || "",
        transport: "usb",
        vendorId: device.vendorId,
        productId: device.productId,
      });
    }
  } catch (err) {
    console.warn("[FIDO2] node-hid not available or failed:", err.message);
    // Continue without USB enumeration
  }

  // Always add platform authenticator option
  const platform = process.platform;
  if (platform === "win32") {
    devices.push({
      id: "internal:windows-hello",
      label: "Windows Hello",
      manufacturer: "Microsoft Corporation",
      path: "internal",
      transport: "internal",
    });
  } else if (platform === "darwin") {
    devices.push({
      id: "internal:touch-id",
      label: "Touch ID",
      manufacturer: "Apple Inc.",
      path: "internal",
      transport: "internal",
    });
  } else {
    // Linux - check for platform authenticator support
    devices.push({
      id: "internal:platform",
      label: "Platform Authenticator",
      manufacturer: "System",
      path: "internal",
      transport: "internal",
    });
  }

  return devices;
}

/**
 * Generate an ed25519-sk SSH key using ssh-keygen
 * 
 * @param {Object} options Generation options
 * @param {string} options.requestId Unique request ID for tracking
 * @param {string} options.label Key label/comment
 * @param {string} options.devicePath Device path (or "internal" for platform authenticator)
 * @param {boolean} options.requireUserPresence Require touch for each use
 * @param {boolean} options.requirePinCode Require PIN verification
 * @param {boolean} options.resident Store key on device (resident key)
 * @param {string} options.passphrase Optional passphrase for key file
 * @param {Object} ipcHandlers IPC handlers for PIN/touch prompts
 * @returns {Promise<Object>} Result with public key, private key path, or error
 */
async function generateFido2Key(options, ipcHandlers) {
  const { 
    requestId, 
    label, 
    devicePath, 
    requireUserPresence = true, 
    requirePinCode = false,
    resident = false,
    passphrase = "",
  } = options;

  // Determine if using platform authenticator
  const isPlatformAuth = devicePath === "internal";
  
  const sshKeygenPath = getSSHKeygenPath(isPlatformAuth);
  if (!sshKeygenPath) {
    return {
      success: false,
      error: "ssh-keygen not found. Please install OpenSSH or Git for Windows.",
    };
  }

  // Create temp directory for key generation
  const tempDir = path.join(os.tmpdir(), "netcatty-fido2");
  if (!fs.existsSync(tempDir)) {
    fs.mkdirSync(tempDir, { recursive: true, mode: 0o700 });
  }

  const keyFileName = `fido2_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
  const keyFilePath = path.join(tempDir, keyFileName);

  // Build ssh-keygen command arguments
  const args = [
    "-t", "ed25519-sk",
    "-f", keyFilePath,
    "-C", label || "FIDO2 Key",
  ];

  // Passphrase handling (empty string for no passphrase)
  if (passphrase) {
    args.push("-N", passphrase);
  } else {
    args.push("-N", "");
  }

  // Device selection (only for USB devices, not platform authenticator)
  if (devicePath && devicePath !== "internal") {
    args.push("-O", `application=ssh:${devicePath}`);
  }

  // FIDO2 options
  // Note: "no-touch-required" disables touch requirement (we want touch by default)
  // "verify-required" requires PIN/biometric verification for each use
  if (requirePinCode) {
    args.push("-O", "verify-required");
  }

  if (resident) {
    args.push("-O", "resident");
  }

  // Note: User presence (touch) is required by default in FIDO2
  // Only add no-touch-required if explicitly disabled
  if (!requireUserPresence) {
    args.push("-O", "no-touch-required");
  }

  return new Promise((resolve, reject) => {
    console.log("[FIDO2] Spawning ssh-keygen:", sshKeygenPath, args.join(" "));

    const proc = spawn(sshKeygenPath, args, {
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });

    activeProcesses.set(requestId, proc);

    let stdout = "";
    let stderr = "";
    let pinSent = false;
    let resolved = false;

    const cleanup = () => {
      activeProcesses.delete(requestId);
    };

    const safeResolve = (result) => {
      if (resolved) return;
      resolved = true;
      cleanup();
      resolve(result);
    };

    // Handle stdout
    proc.stdout.on("data", (data) => {
      const text = data.toString();
      stdout += text;
      console.log("[FIDO2] stdout:", text);

      // Detect touch prompt
      if (/confirm user presence|touch your|tap your/i.test(text)) {
        ipcHandlers.emitTouchPrompt(requestId);
      }
    });

    // Handle stderr (ssh-keygen outputs prompts to stderr)
    proc.stderr.on("data", async (data) => {
      const text = data.toString();
      stderr += text;
      console.log("[FIDO2] stderr:", text);

      // Detect PIN request
      if (/enter pin for authenticator/i.test(text) && !pinSent) {
        pinSent = true;
        try {
          const pin = await ipcHandlers.requestPin(requestId);
          if (pin === null) {
            // User cancelled
            proc.kill();
            safeResolve({ success: false, error: "PIN entry cancelled" });
            return;
          }
          // Write PIN to stdin with proper line ending
          const lineEnding = process.platform === "win32" ? "\r\n" : "\n";
          proc.stdin.write(pin + lineEnding);
        } catch (err) {
          console.error("[FIDO2] PIN request failed:", err);
          proc.kill();
          safeResolve({ success: false, error: "PIN request failed" });
        }
      }

      // Detect touch prompt
      if (/confirm user presence|touch your|tap your/i.test(text)) {
        ipcHandlers.emitTouchPrompt(requestId);
      }

      // Detect errors
      if (/error|failed|denied|timeout/i.test(text) && !/generating/i.test(text)) {
        // Don't immediately fail, wait for process exit
      }
    });

    proc.on("error", (err) => {
      console.error("[FIDO2] Process error:", err);
      safeResolve({ success: false, error: err.message });
    });

    proc.on("close", (code) => {
      console.log("[FIDO2] Process exited with code:", code);
      
      if (code === 0) {
        // Success - read the generated keys
        try {
          const publicKeyPath = keyFilePath + ".pub";
          const publicKey = fs.readFileSync(publicKeyPath, "utf8").trim();
          const privateKey = fs.readFileSync(keyFilePath, "utf8");

          // Clean up private key file for security (keep in memory only)
          fs.unlinkSync(keyFilePath);
          fs.unlinkSync(publicKeyPath);

          safeResolve({
            success: true,
            publicKey,
            privateKey,
            keyType: "ED25519",
          });
        } catch (readErr) {
          safeResolve({
            success: false,
            error: `Failed to read generated keys: ${readErr.message}`,
          });
        }
      } else {
        // Error - extract meaningful message
        let errorMessage = "Key generation failed";
        
        if (/no authenticator|device not found/i.test(stderr)) {
          errorMessage = "No FIDO2 authenticator found. Please connect your security key.";
        } else if (/pin required|invalid pin/i.test(stderr)) {
          errorMessage = "PIN verification failed.";
        } else if (/operation timed out|timeout/i.test(stderr)) {
          errorMessage = "Operation timed out. Please try again.";
        } else if (/user presence/i.test(stderr)) {
          errorMessage = "Touch not detected. Please tap your security key.";
        } else if (/cancelled|canceled/i.test(stderr)) {
          errorMessage = "Operation was cancelled.";
        } else if (stderr.trim()) {
          errorMessage = stderr.trim().split("\n").pop() || errorMessage;
        }

        // Clean up any partial files
        try {
          if (fs.existsSync(keyFilePath)) fs.unlinkSync(keyFilePath);
          if (fs.existsSync(keyFilePath + ".pub")) fs.unlinkSync(keyFilePath + ".pub");
        } catch {
          // Ignore cleanup errors
        }

        safeResolve({
          success: false,
          error: errorMessage,
          exitCode: code,
        });
      }
    });

    // Timeout after 2 minutes
    setTimeout(() => {
      if (!resolved) {
        proc.kill();
        safeResolve({
          success: false,
          error: "Operation timed out after 2 minutes",
        });
      }
    }, 120000);
  });
}

/**
 * Cancel an active key generation process
 */
function cancelGeneration(requestId) {
  const proc = activeProcesses.get(requestId);
  if (proc) {
    proc.kill();
    activeProcesses.delete(requestId);
    return true;
  }
  return false;
}

/**
 * Check if ssh-keygen supports FIDO2
 */
async function checkFido2Support() {
  const sshKeygenPath = getSSHKeygenPath();
  if (!sshKeygenPath) {
    return {
      supported: false,
      sshKeygenPath: null,
      error: "ssh-keygen not found",
    };
  }

  try {
    // Check if ssh-keygen supports ed25519-sk
    const helpOutput = execSync(`"${sshKeygenPath}" -t ed25519-sk -? 2>&1`, {
      encoding: "utf8",
      timeout: 5000,
      shell: true,
    });

    // If we get here without error, or error contains usage info, it's supported
    const supported = true;

    return {
      supported,
      sshKeygenPath,
      version: extractVersion(sshKeygenPath),
    };
  } catch (err) {
    // Check if error output indicates unsupported type
    const output = err.stdout || err.stderr || err.message || "";
    const unsupported = /unknown key type|unsupported|invalid/i.test(output);

    return {
      supported: !unsupported,
      sshKeygenPath,
      version: extractVersion(sshKeygenPath),
      error: unsupported ? "FIDO2 key type not supported by this ssh-keygen version" : null,
    };
  }
}

/**
 * Extract ssh-keygen version
 */
function extractVersion(sshKeygenPath) {
  try {
    const output = execSync(`"${sshKeygenPath}" -V 2>&1`, {
      encoding: "utf8",
      timeout: 5000,
      shell: true,
    });
    return output.trim();
  } catch {
    return "unknown";
  }
}

/**
 * Register IPC handlers for FIDO2 operations
 */
function registerHandlers(ipcMain, electronModule) {
  const { BrowserWindow } = electronModule;

  // Pending PIN requests
  const pendingPinRequests = new Map();

  // List available FIDO2 devices
  ipcMain.handle("netcatty:fido2:listDevices", async () => {
    return listFido2Devices();
  });

  // Check FIDO2 support
  ipcMain.handle("netcatty:fido2:checkSupport", async () => {
    return checkFido2Support();
  });

  // Generate FIDO2 key
  ipcMain.handle("netcatty:fido2:generate", async (event, options) => {
    const requestId = options.requestId || `fido2_${Date.now()}`;

    const ipcHandlers = {
      emitTouchPrompt: (reqId) => {
        // Send touch prompt to all windows
        BrowserWindow.getAllWindows().forEach((win) => {
          win.webContents.send("netcatty:fido2:touchPrompt", { requestId: reqId });
        });
      },
      requestPin: (reqId) => {
        return new Promise((resolve) => {
          pendingPinRequests.set(reqId, resolve);
          // Send PIN request to all windows
          BrowserWindow.getAllWindows().forEach((win) => {
            win.webContents.send("netcatty:fido2:pinRequest", { requestId: reqId });
          });
        });
      },
    };

    return generateFido2Key({ ...options, requestId }, ipcHandlers);
  });

  // Submit PIN (from renderer)
  ipcMain.handle("netcatty:fido2:submitPin", async (_event, { requestId, pin }) => {
    const resolve = pendingPinRequests.get(requestId);
    if (resolve) {
      pendingPinRequests.delete(requestId);
      resolve(pin);
      return { success: true };
    }
    return { success: false, error: "No pending PIN request" };
  });

  // Cancel PIN entry
  ipcMain.handle("netcatty:fido2:cancelPin", async (_event, { requestId }) => {
    const resolve = pendingPinRequests.get(requestId);
    if (resolve) {
      pendingPinRequests.delete(requestId);
      resolve(null);
      return { success: true };
    }
    return { success: false };
  });

  // Cancel generation
  ipcMain.handle("netcatty:fido2:cancel", async (_event, { requestId }) => {
    return { success: cancelGeneration(requestId) };
  });

  // Get ssh-keygen path (for debugging)
  ipcMain.handle("netcatty:fido2:getSshKeygenPath", async () => {
    return getSSHKeygenPath();
  });
}

module.exports = {
  getSSHKeygenPath,
  listFido2Devices,
  generateFido2Key,
  cancelGeneration,
  checkFido2Support,
  registerHandlers,
};
