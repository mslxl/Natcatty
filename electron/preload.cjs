const { ipcRenderer, contextBridge } = require("electron");

const dataListeners = new Map();
const exitListeners = new Map();
const transferProgressListeners = new Map();
const transferCompleteListeners = new Map();
const transferErrorListeners = new Map();
const chainProgressListeners = new Map();
const authFailedListeners = new Map();

ipcRenderer.on("netcatty:data", (_event, payload) => {
  const set = dataListeners.get(payload.sessionId);
  if (!set) return;
  set.forEach((cb) => {
    try {
      cb(payload.data);
    } catch (err) {
      console.error("Data callback failed", err);
    }
  });
});

ipcRenderer.on("netcatty:exit", (_event, payload) => {
  const set = exitListeners.get(payload.sessionId);
  if (set) {
    set.forEach((cb) => {
      try {
        cb(payload);
      } catch (err) {
        console.error("Exit callback failed", err);
      }
    });
  }
  dataListeners.delete(payload.sessionId);
  exitListeners.delete(payload.sessionId);
});

// Chain progress events (for jump host connections)
ipcRenderer.on("netcatty:chain:progress", (_event, payload) => {
  const { hop, total, label, status } = payload;
  // Notify all registered chain progress listeners
  chainProgressListeners.forEach((cb) => {
    try {
      cb(hop, total, label, status);
    } catch (err) {
      console.error("Chain progress callback failed", err);
    }
  });
});



// Authentication failed events
ipcRenderer.on("netcatty:auth:failed", (_event, payload) => {
  const set = authFailedListeners.get(payload.sessionId);
  if (set) {
    set.forEach((cb) => {
      try {
        cb(payload);
      } catch (err) {
        console.error("Auth failed callback failed", err);
      }
    });
  }
});

// Transfer progress events
ipcRenderer.on("netcatty:transfer:progress", (_event, payload) => {
  const cb = transferProgressListeners.get(payload.transferId);
  if (cb) {
    try {
      cb(payload.transferred, payload.totalBytes, payload.speed);
    } catch (err) {
      console.error("Transfer progress callback failed", err);
    }
  }
});

ipcRenderer.on("netcatty:transfer:complete", (_event, payload) => {
  const cb = transferCompleteListeners.get(payload.transferId);
  if (cb) {
    try {
      cb();
    } catch (err) {
      console.error("Transfer complete callback failed", err);
    }
  }
  // Cleanup listeners
  transferProgressListeners.delete(payload.transferId);
  transferCompleteListeners.delete(payload.transferId);
  transferErrorListeners.delete(payload.transferId);
});

ipcRenderer.on("netcatty:transfer:error", (_event, payload) => {
  const cb = transferErrorListeners.get(payload.transferId);
  if (cb) {
    try {
      cb(payload.error);
    } catch (err) {
      console.error("Transfer error callback failed", err);
    }
  }
  // Cleanup listeners
  transferProgressListeners.delete(payload.transferId);
  transferCompleteListeners.delete(payload.transferId);
  transferErrorListeners.delete(payload.transferId);
});

ipcRenderer.on("netcatty:transfer:cancelled", (_event, payload) => {
  // Just cleanup listeners, the UI already knows it's cancelled
  transferProgressListeners.delete(payload.transferId);
  transferCompleteListeners.delete(payload.transferId);
  transferErrorListeners.delete(payload.transferId);
});

// Upload with progress listeners
const uploadProgressListeners = new Map();
const uploadCompleteListeners = new Map();
const uploadErrorListeners = new Map();

ipcRenderer.on("netcatty:upload:progress", (_event, payload) => {
  const cb = uploadProgressListeners.get(payload.transferId);
  if (cb) {
    try {
      cb(payload.transferred, payload.totalBytes, payload.speed);
    } catch (err) {
      console.error("Upload progress callback failed", err);
    }
  }
});

ipcRenderer.on("netcatty:upload:complete", (_event, payload) => {
  const cb = uploadCompleteListeners.get(payload.transferId);
  if (cb) {
    try {
      cb();
    } catch (err) {
      console.error("Upload complete callback failed", err);
    }
  }
  // Cleanup listeners
  uploadProgressListeners.delete(payload.transferId);
  uploadCompleteListeners.delete(payload.transferId);
  uploadErrorListeners.delete(payload.transferId);
});

ipcRenderer.on("netcatty:upload:error", (_event, payload) => {
  const cb = uploadErrorListeners.get(payload.transferId);
  if (cb) {
    try {
      cb(payload.error);
    } catch (err) {
      console.error("Upload error callback failed", err);
    }
  }
  // Cleanup listeners
  uploadProgressListeners.delete(payload.transferId);
  uploadCompleteListeners.delete(payload.transferId);
  uploadErrorListeners.delete(payload.transferId);
});

// Port forwarding status listeners
const portForwardStatusListeners = new Map();

ipcRenderer.on("netcatty:portforward:status", (_event, payload) => {
  const { tunnelId, status, error } = payload;
  const callbacks = portForwardStatusListeners.get(tunnelId);
  if (callbacks) {
    callbacks.forEach((cb) => {
      try {
        cb(status, error);
      } catch (err) {
        console.error("Port forward status callback failed", err);
      }
    });
  }
});

// WebAuthn/Windows Hello using browser's navigator.credentials API
// This runs in the renderer context where the API is available and properly handles foreground requirements.
const WEBAUTHN_RP_ID = "localhost";
const WEBAUTHN_RP_NAME = "Netcatty";

/**
 * Check if WebAuthn Platform Authenticator is available (Windows Hello, Touch ID, etc.)
 */
async function webauthnIsAvailable() {
  if (typeof PublicKeyCredential === "undefined") {
    return false;
  }
  if (typeof PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable !== "function") {
    return false;
  }
  try {
    return await PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
  } catch (err) {
    console.warn("[WebAuthn] Availability check failed:", err);
    return false;
  }
}

/**
 * Create a WebAuthn credential using the browser API (Windows Hello)
 * This MUST be called from the page context with a user gesture.
 */
async function webauthnCreateCredential(userName) {
  const userId = new TextEncoder().encode(userName || "netcatty-user");
  
  // Generate a random challenge
  const challenge = new Uint8Array(32);
  crypto.getRandomValues(challenge);

  const publicKeyCredentialCreationOptions = {
    challenge,
    rp: {
      name: WEBAUTHN_RP_NAME,
      id: WEBAUTHN_RP_ID,
    },
    user: {
      id: userId,
      name: userName || "netcatty-user",
      displayName: userName || "Netcatty User",
    },
    pubKeyCredParams: [
      { alg: -7, type: "public-key" },   // ES256
      { alg: -257, type: "public-key" }, // RS256
    ],
    authenticatorSelection: {
      authenticatorAttachment: "platform",  // Windows Hello / Touch ID only
      userVerification: "required",
      residentKey: "discouraged",
    },
    timeout: 120000,
    attestation: "none",
  };

  console.log("[WebAuthn] Creating credential with options:", {
    rpId: WEBAUTHN_RP_ID,
    rpName: WEBAUTHN_RP_NAME,
    userName,
    origin: typeof location !== "undefined" ? location.origin : "unknown",
  });

  try {
    const credential = await navigator.credentials.create({
      publicKey: publicKeyCredentialCreationOptions,
    });

    if (!credential) {
      throw new Error("Credential creation returned null");
    }

    console.log("[WebAuthn] Credential created successfully, id length:", credential.rawId.byteLength);
    
    // Return the credential ID as base64
    const credentialId = credential.rawId;
    return btoa(String.fromCharCode(...new Uint8Array(credentialId)));
  } catch (err) {
    console.error("[WebAuthn] Credential creation failed:", err.name, err.message);
    throw err;
  }
}

/**
 * Verify user with WebAuthn (Windows Hello assertion)
 */
async function webauthnGetAssertion(credentialIdB64) {
  // Decode base64 credential ID
  const credentialIdBytes = Uint8Array.from(atob(credentialIdB64), c => c.charCodeAt(0));
  
  // Generate a random challenge
  const challenge = new Uint8Array(32);
  crypto.getRandomValues(challenge);

  const publicKeyCredentialRequestOptions = {
    challenge,
    rpId: WEBAUTHN_RP_ID,
    allowCredentials: [{
      id: credentialIdBytes,
      type: "public-key",
      transports: ["internal"],
    }],
    userVerification: "required",
    timeout: 120000,
  };

  console.log("[WebAuthn] Getting assertion for credential");

  const assertion = await navigator.credentials.get({
    publicKey: publicKeyCredentialRequestOptions,
  });

  if (!assertion) {
    throw new Error("Assertion returned null");
  }

  console.log("[WebAuthn] Assertion successful");
  return true;
}

// Handle WebAuthn requests from main process
// NOTE: These operations require user gesture context in the renderer.
// The preload can only handle "isAvailable" check; create/get must be done from the page.
ipcRenderer.on("netcatty:webauthn:request", async (_event, payload) => {
  const { requestId, op, params } = payload || {};

  const respond = (message) => {
    try {
      ipcRenderer.send("netcatty:webauthn:response", { requestId, ...message });
    } catch (err) {
      console.error("[WebAuthn] Failed to send response:", err?.message || String(err));
    }
  };

  if (!requestId) return;

  try {
    if (op === "isAvailable") {
      const available = await webauthnIsAvailable();
      respond({ ok: true, result: available });
      return;
    }

    // For createCredential and getAssertion, we need to forward to the page
    // since they require user gesture context
    if (op === "createCredential" || op === "getAssertion") {
      // Dispatch a custom event to the page, which will handle it with user gesture
      const eventData = { requestId, op, params };
      window.postMessage({ type: "netcatty:webauthn:request", ...eventData }, "*");
      // Response will come back via window message listener
      return;
    }

    respond({ ok: false, error: "Unknown WebAuthn op: " + op });
  } catch (err) {
    console.error("[WebAuthn] Operation failed:", err);
    respond({ ok: false, error: err?.message || String(err) });
  }
});

// Listen for WebAuthn responses from the page
window.addEventListener("message", (event) => {
  if (event.source !== window) return;
  if (!event.data || event.data.type !== "netcatty:webauthn:response") return;
  
  const { requestId, ok, result, error } = event.data;
  if (!requestId) return;
  
  ipcRenderer.send("netcatty:webauthn:response", { requestId, ok, result, error });
});

// Expose WebAuthn functions to the page for user-gesture-triggered calls
// These will be called by the React app when user clicks a button
const webauthnApi = {
  isAvailable: webauthnIsAvailable,
  createCredential: webauthnCreateCredential,
  getAssertion: webauthnGetAssertion,
};

const api = {
  startSSHSession: async (options) => {
    const result = await ipcRenderer.invoke("netcatty:start", options);
    return result.sessionId;
  },
  startTelnetSession: async (options) => {
    const result = await ipcRenderer.invoke("netcatty:telnet:start", options);
    return result.sessionId;
  },
  startMoshSession: async (options) => {
    const result = await ipcRenderer.invoke("netcatty:mosh:start", options);
    return result.sessionId;
  },
  startLocalSession: async (options) => {
    const result = await ipcRenderer.invoke("netcatty:local:start", options || {});
    return result.sessionId;
  },
  writeToSession: (sessionId, data) => {
    ipcRenderer.send("netcatty:write", { sessionId, data });
  },
  execCommand: async (options) => {
    return ipcRenderer.invoke("netcatty:ssh:exec", options);
  },
  generateKeyPair: async (options) => {
    return ipcRenderer.invoke("netcatty:key:generate", options);
  },
  resizeSession: (sessionId, cols, rows) => {
    ipcRenderer.send("netcatty:resize", { sessionId, cols, rows });
  },
  closeSession: (sessionId) => {
    ipcRenderer.send("netcatty:close", { sessionId });
  },
  onSessionData: (sessionId, cb) => {
    if (!dataListeners.has(sessionId)) dataListeners.set(sessionId, new Set());
    dataListeners.get(sessionId).add(cb);
    return () => dataListeners.get(sessionId)?.delete(cb);
  },
  onSessionExit: (sessionId, cb) => {
    if (!exitListeners.has(sessionId)) exitListeners.set(sessionId, new Set());
    exitListeners.get(sessionId).add(cb);
    return () => exitListeners.get(sessionId)?.delete(cb);
  },
  onAuthFailed: (sessionId, cb) => {
    if (!authFailedListeners.has(sessionId)) authFailedListeners.set(sessionId, new Set());
    authFailedListeners.get(sessionId).add(cb);
    return () => authFailedListeners.get(sessionId)?.delete(cb);
  },
  openSftp: async (options) => {
    const result = await ipcRenderer.invoke("netcatty:sftp:open", options);
    return result.sftpId;
  },
  listSftp: async (sftpId, path) => {
    return ipcRenderer.invoke("netcatty:sftp:list", { sftpId, path });
  },
  readSftp: async (sftpId, path) => {
    return ipcRenderer.invoke("netcatty:sftp:read", { sftpId, path });
  },
  writeSftp: async (sftpId, path, content) => {
    return ipcRenderer.invoke("netcatty:sftp:write", { sftpId, path, content });
  },
  closeSftp: async (sftpId) => {
    return ipcRenderer.invoke("netcatty:sftp:close", { sftpId });
  },
  mkdirSftp: async (sftpId, path) => {
    return ipcRenderer.invoke("netcatty:sftp:mkdir", { sftpId, path });
  },
  deleteSftp: async (sftpId, path) => {
    return ipcRenderer.invoke("netcatty:sftp:delete", { sftpId, path });
  },
  renameSftp: async (sftpId, oldPath, newPath) => {
    return ipcRenderer.invoke("netcatty:sftp:rename", { sftpId, oldPath, newPath });
  },
  statSftp: async (sftpId, path) => {
    return ipcRenderer.invoke("netcatty:sftp:stat", { sftpId, path });
  },
  chmodSftp: async (sftpId, path, mode) => {
    return ipcRenderer.invoke("netcatty:sftp:chmod", { sftpId, path, mode });
  },
  // Write binary with real-time progress callback
  writeSftpBinaryWithProgress: async (sftpId, path, content, transferId, onProgress, onComplete, onError) => {
    // Register callbacks
    if (onProgress) uploadProgressListeners.set(transferId, onProgress);
    if (onComplete) uploadCompleteListeners.set(transferId, onComplete);
    if (onError) uploadErrorListeners.set(transferId, onError);
    
    return ipcRenderer.invoke("netcatty:sftp:writeBinaryWithProgress", { 
      sftpId, 
      path, 
      content, 
      transferId 
    });
  },
  // Local filesystem operations
  listLocalDir: async (path) => {
    return ipcRenderer.invoke("netcatty:local:list", { path });
  },
  readLocalFile: async (path) => {
    return ipcRenderer.invoke("netcatty:local:read", { path });
  },
  writeLocalFile: async (path, content) => {
    return ipcRenderer.invoke("netcatty:local:write", { path, content });
  },
  deleteLocalFile: async (path) => {
    return ipcRenderer.invoke("netcatty:local:delete", { path });
  },
  renameLocalFile: async (oldPath, newPath) => {
    return ipcRenderer.invoke("netcatty:local:rename", { oldPath, newPath });
  },
  mkdirLocal: async (path) => {
    return ipcRenderer.invoke("netcatty:local:mkdir", { path });
  },
  statLocal: async (path) => {
    return ipcRenderer.invoke("netcatty:local:stat", { path });
  },
  getHomeDir: async () => {
    return ipcRenderer.invoke("netcatty:local:homedir");
  },
  getSystemInfo: async () => {
    return ipcRenderer.invoke("netcatty:system:info");
  },
  // Read system known_hosts file
  readKnownHosts: async () => {
    return ipcRenderer.invoke("netcatty:known-hosts:read");
  },
  setTheme: async (theme) => {
    return ipcRenderer.invoke("netcatty:setTheme", theme);
  },
  // Streaming transfer with real progress
  startStreamTransfer: async (options, onProgress, onComplete, onError) => {
    const { transferId } = options;
    // Register callbacks
    if (onProgress) transferProgressListeners.set(transferId, onProgress);
    if (onComplete) transferCompleteListeners.set(transferId, onComplete);
    if (onError) transferErrorListeners.set(transferId, onError);
    
    return ipcRenderer.invoke("netcatty:transfer:start", options);
  },
  cancelTransfer: async (transferId) => {
    // Cleanup listeners
    transferProgressListeners.delete(transferId);
    transferCompleteListeners.delete(transferId);
    transferErrorListeners.delete(transferId);
    return ipcRenderer.invoke("netcatty:transfer:cancel", { transferId });
  },
  // Window controls for custom title bar
  windowMinimize: () => ipcRenderer.invoke("netcatty:window:minimize"),
  windowMaximize: () => ipcRenderer.invoke("netcatty:window:maximize"),
  windowClose: () => ipcRenderer.invoke("netcatty:window:close"),
  windowIsMaximized: () => ipcRenderer.invoke("netcatty:window:isMaximized"),
  
  // Settings window
  openSettingsWindow: () => ipcRenderer.invoke("netcatty:settings:open"),
  closeSettingsWindow: () => ipcRenderer.invoke("netcatty:settings:close"),

  // Cloud sync session (in-memory only, shared across windows)
  cloudSyncSetSessionPassword: (password) =>
    ipcRenderer.invoke("netcatty:cloudSync:session:setPassword", password),
  cloudSyncGetSessionPassword: () =>
    ipcRenderer.invoke("netcatty:cloudSync:session:getPassword"),
  cloudSyncClearSessionPassword: () =>
    ipcRenderer.invoke("netcatty:cloudSync:session:clearPassword"),
  
  // Open URL in default browser
  openExternal: (url) => ipcRenderer.invoke("netcatty:openExternal", url),
  
  // Port Forwarding API
  startPortForward: async (options) => {
    return ipcRenderer.invoke("netcatty:portforward:start", options);
  },
  stopPortForward: async (tunnelId) => {
    return ipcRenderer.invoke("netcatty:portforward:stop", { tunnelId });
  },
  getPortForwardStatus: async (tunnelId) => {
    return ipcRenderer.invoke("netcatty:portforward:status", { tunnelId });
  },
  listPortForwards: async () => {
    return ipcRenderer.invoke("netcatty:portforward:list");
  },
  onPortForwardStatus: (tunnelId, cb) => {
    if (!portForwardStatusListeners.has(tunnelId)) {
      portForwardStatusListeners.set(tunnelId, new Set());
    }
    portForwardStatusListeners.get(tunnelId).add(cb);
    return () => {
      portForwardStatusListeners.get(tunnelId)?.delete(cb);
      if (portForwardStatusListeners.get(tunnelId)?.size === 0) {
        portForwardStatusListeners.delete(tunnelId);
      }
    };
  },
  // Chain progress listener for jump host connections
  onChainProgress: (cb) => {
    const id = Date.now().toString() + Math.random().toString(16).slice(2);
    chainProgressListeners.set(id, cb);
    return () => {
      chainProgressListeners.delete(id);
    };
  },

  // OAuth callback server
  startOAuthCallback: (expectedState) => ipcRenderer.invoke("oauth:startCallback", expectedState),
  cancelOAuthCallback: () => ipcRenderer.invoke("oauth:cancelCallback"),

  // GitHub Device Flow (proxied via main process to avoid CORS)
  githubStartDeviceFlow: (options) => ipcRenderer.invoke("netcatty:github:deviceFlow:start", options),
  githubPollDeviceFlowToken: (options) => ipcRenderer.invoke("netcatty:github:deviceFlow:poll", options),

  // Google OAuth (proxied via main process to avoid CORS)
  googleExchangeCodeForTokens: (options) =>
    ipcRenderer.invoke("netcatty:google:oauth:exchange", options),
  googleRefreshAccessToken: (options) =>
    ipcRenderer.invoke("netcatty:google:oauth:refresh", options),
  googleGetUserInfo: (options) =>
    ipcRenderer.invoke("netcatty:google:oauth:userinfo", options),

  // Google Drive API (proxied via main process to avoid CORS/COEP issues in renderer)
  googleDriveFindSyncFile: (options) =>
    ipcRenderer.invoke("netcatty:google:drive:findSyncFile", options),
  googleDriveCreateSyncFile: (options) =>
    ipcRenderer.invoke("netcatty:google:drive:createSyncFile", options),
  googleDriveUpdateSyncFile: (options) =>
    ipcRenderer.invoke("netcatty:google:drive:updateSyncFile", options),
  googleDriveDownloadSyncFile: (options) =>
    ipcRenderer.invoke("netcatty:google:drive:downloadSyncFile", options),
  googleDriveDeleteSyncFile: (options) =>
    ipcRenderer.invoke("netcatty:google:drive:deleteSyncFile", options),

  // Biometric Key API (Termius-style: ED25519 + OS Secure Storage)
  biometricCheckSupport: () => ipcRenderer.invoke("netcatty:biometric:checkSupport"),
  biometricGenerate: (options) => ipcRenderer.invoke("netcatty:biometric:generate", options),
  biometricGetPassphrase: (options) => ipcRenderer.invoke("netcatty:biometric:getPassphrase", options),
  biometricDeletePassphrase: (options) =>
    ipcRenderer.invoke("netcatty:biometric:deletePassphrase", options),
  biometricListKeys: () =>
    ipcRenderer.invoke("netcatty:biometric:listKeys"),
  
  // WebAuthn API - must be called from user gesture (click handler) in renderer
  webauthn: webauthnApi,
};

// Merge with existing netcatty (if any) to avoid stale objects on hot reload
const existing = (typeof window !== "undefined" && window.netcatty) ? window.netcatty : {};
contextBridge.exposeInMainWorld("netcatty", { ...existing, ...api });
