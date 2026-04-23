/**
 * OAuth Callback Bridge
 *
 * Handles OAuth loopback redirects for Google Drive and OneDrive.
 * Prepares a temporary HTTP server on 127.0.0.1 (preferred port 45678; falls
 * back to an OS-assigned free port if 45678 is already in use — issue #823)
 * and waits on it for the authorization code.
 */

const http = require("node:http");
const url = require("node:url");

let server = null;
let activePort = null;
let requestHandler = null;
let pendingResolve = null;
let pendingReject = null;
let pendingExpectedState = null;
let serverTimeout = null;

const PREFERRED_OAUTH_PORT = 45678;
const OAUTH_TIMEOUT = 5 * 60 * 1000; // 5 minutes

const escapeHtml = (value) =>
  String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");

const renderOAuthPage = ({ title, message, detail, status, autoClose }) => {
  const accent =
    status === "success" ? "200 100% 61%" : status === "error" ? "0 70% 50%" : "38 92% 50%";
  const safeTitle = escapeHtml(title);
  const safeMessage = escapeHtml(message);
  const safeDetail = detail ? escapeHtml(detail) : "";
  const titleIcon =
    status === "success"
      ? `<svg class="title-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><path d="m9 11 3 3L22 4"/></svg>`
      : status === "error"
        ? `<svg class="title-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><path d="M12 8v4"/><path d="M12 16h.01"/></svg>`
        : `<svg class="title-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 2v20"/><path d="M5 9h14"/><path d="M7 3h10"/><path d="M7 21h10"/></svg>`;
  const detailBlock = safeDetail
    ? `<div class="detail">${safeDetail}</div>`
    : "";
  const closeScript = autoClose
    ? "<script>setTimeout(() => window.close(), 1400);</script>"
    : "";

  return `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${safeTitle}</title>
  <style>
    :root {
      color-scheme: dark;
      --background: 220 28% 8%;
      --foreground: 210 40% 95%;
      --card: 220 22% 12%;
      --muted: 220 10% 70%;
      --border: 220 22% 18%;
      --accent: ${accent};
      --radius: 0.65rem;
      --ring: 200 100% 61%;
    }
    @media (prefers-color-scheme: light) {
      :root {
        color-scheme: light;
        --background: 216 33% 96%;
        --foreground: 222 47% 12%;
        --card: 0 0% 100%;
        --muted: 220 10% 45%;
        --border: 220 16% 82%;
        --ring: 208 100% 50%;
      }
    }
    * { box-sizing: border-box; }
    html, body { height: 100%; }
    body {
      margin: 0;
      font-family: "Space Grotesk", system-ui, -apple-system, "Segoe UI", sans-serif;
      background:
        radial-gradient(900px circle at 15% 0%, hsl(var(--accent) / 0.10), transparent 38%),
        radial-gradient(1200px circle at 85% 10%, hsl(var(--ring) / 0.16), transparent 40%),
        hsl(var(--background));
      color: hsl(var(--foreground));
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 32px;
    }
    .shell {
      width: min(560px, 92vw);
      border-radius: calc(var(--radius) + 10px);
      background: hsl(var(--card));
      border: 1px solid hsl(var(--border));
      box-shadow: 0 18px 40px hsl(var(--foreground) / 0.12);
      padding: 28px;
      animation: fadeUp 220ms ease-out;
    }
    .header {
      display: flex;
      align-items: center;
      gap: 12px;
      margin-bottom: 18px;
    }
    .logo {
      width: 36px;
      height: 36px;
    }
    .brand {
      font-size: 16px;
      font-weight: 700;
      letter-spacing: 0.02em;
      color: hsl(var(--foreground));
    }
    h1 {
      margin: 0 0 8px;
      font-size: 28px;
      letter-spacing: -0.02em;
    }
    .title-row {
      display: inline-flex;
      align-items: center;
      gap: 10px;
    }
    .title-icon {
      width: 22px;
      height: 22px;
      color: hsl(var(--accent));
    }
    .subtitle {
      margin: 0;
      font-size: 15px;
      line-height: 1.6;
      color: hsl(var(--muted));
    }
    .detail {
      margin-top: 16px;
      padding: 12px 14px;
      background: hsl(var(--background) / 0.6);
      border-radius: calc(var(--radius) - 4px);
      border: 1px dashed hsl(var(--border));
      font-size: 13px;
      line-height: 1.5;
      word-break: break-word;
    }
    .footer {
      margin-top: 22px;
      font-size: 13px;
      color: hsl(var(--muted));
    }
    .accent {
      color: hsl(var(--accent));
      font-weight: 600;
    }
    @keyframes fadeUp {
      from { opacity: 0; transform: translateY(10px); }
      to { opacity: 1; transform: translateY(0); }
    }
  </style>
</head>
<body>
  <div class="shell">
    <div class="header">
      <svg class="logo" viewBox="0 0 56 56" aria-hidden="true">
        <rect x="0" y="0" width="56" height="56" rx="12" fill="#2F7BFF"/>
        <rect x="10" y="13" width="36" height="24" rx="4" fill="#FFFFFF" stroke="#1D4FCF" stroke-opacity="0.12"/>
        <rect x="10" y="13" width="36" height="5" rx="4" fill="#E6EEFF"/>
        <circle cx="14" cy="15.5" r="1" fill="#1E4FD1"/>
        <circle cx="18" cy="15.5" r="1" fill="#1E4FD1" opacity="0.7"/>
        <circle cx="22" cy="15.5" r="1" fill="#1E4FD1" opacity="0.5"/>
        <path d="M16 28 L20 26 L16 24" stroke="#1E4FD1" fill="none" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
        <path d="M24 30 H30" stroke="#1E4FD1" stroke-width="1.6" stroke-linecap="round"/>
        <path d="M36 33 C40 36,42 38,42 42 C42 45,40 47,37 47" stroke="white" fill="none" stroke-width="3.2" stroke-linecap="round"/>
        <rect x="34" y="44" width="6" height="5" rx="1" fill="white" stroke="#1E4FD1"/>
      </svg>
      <div>
        <div class="brand">Netcatty</div>
      </div>
    </div>
    <h1 class="title-row"><span>${safeTitle}</span>${titleIcon}</h1>
    <p class="subtitle">${safeMessage}</p>
    ${detailBlock}
    <div class="footer">You can close this window and return to <span class="accent">Netcatty</span>.</div>
  </div>
  ${closeScript}
</body>
</html>`;
};

function handleOAuthRequest(req, res) {
  const parsedUrl = url.parse(req.url, true);

  // Only handle the callback path
  if (parsedUrl.pathname !== "/oauth/callback") {
    res.writeHead(404, { "Content-Type": "text/html; charset=utf-8" });
    res.end("<h1>404 Not Found</h1>");
    return;
  }

  const { code, state, error, error_description } = parsedUrl.query;

  res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });

  if (error) {
    res.end(
      renderOAuthPage({
        title: "Authorization Failed",
        message: "We could not complete the sign-in flow.",
        detail: error_description || error || "Unknown error",
        status: "error",
      })
    );
    rejectPending(new Error(error_description || error || "Authorization failed"));
    return;
  }

  if (!code) {
    res.end(
      renderOAuthPage({
        title: "Missing Authorization Code",
        message: "The authorization response did not include a code.",
        status: "error",
      })
    );
    rejectPending(new Error("Missing authorization code"));
    return;
  }

  if (pendingExpectedState && state !== pendingExpectedState) {
    res.end(
      renderOAuthPage({
        title: "Security Check Failed",
        message: "State parameter mismatch. This may indicate a CSRF attack.",
        status: "error",
      })
    );
    rejectPending(new Error("State mismatch - possible CSRF attack"));
    return;
  }

  res.end(
    renderOAuthPage({
      title: "Authorization Complete",
      message: "You are signed in and ready to sync. You can close this tab now.",
      status: "success",
    })
  );

  const resolve = pendingResolve;
  cleanup();
  if (resolve) {
    resolve({ code, state });
  }
}

function rejectPending(err) {
  const reject = pendingReject;
  cleanup();
  if (reject) {
    reject(err);
  }
}

/**
 * Try to bind an HTTP server on the loopback interface. Prefers
 * PREFERRED_OAUTH_PORT; if the port is already in use (EADDRINUSE) or the OS
 * otherwise refuses it (EACCES), falls back to letting the OS pick a free
 * ephemeral port.
 *
 * @returns {Promise<{server: http.Server, port: number}>}
 */
function bindLoopbackServer() {
  const ports = [PREFERRED_OAUTH_PORT, 0];
  return (async () => {
    let lastErr;
    for (const port of ports) {
      try {
        const s = http.createServer((req, res) => {
          if (requestHandler) requestHandler(req, res);
        });
        await new Promise((resolve, reject) => {
          const onError = (err) => {
            s.removeListener("listening", onListening);
            reject(err);
          };
          const onListening = () => {
            s.removeListener("error", onError);
            resolve();
          };
          s.once("error", onError);
          s.once("listening", onListening);
          s.listen(port, "127.0.0.1");
        });
        const bound = s.address();
        return { server: s, port: bound && typeof bound === "object" ? bound.port : port };
      } catch (err) {
        lastErr = err;
        if (err && (err.code === "EADDRINUSE" || err.code === "EACCES")) {
          // Try the next candidate (OS-assigned)
          continue;
        }
        throw err;
      }
    }
    throw lastErr || new Error("Failed to bind OAuth loopback server");
  })();
}

/**
 * Bind a loopback HTTP server for the OAuth callback. Returns the chosen
 * port and fully-qualified redirect URI so the caller can build the
 * provider's authorization URL against it.
 *
 * @returns {Promise<{port: number, redirectUri: string}>}
 */
async function prepareOAuthCallback() {
  // Close any previously-prepared server (e.g. from an abandoned flow).
  closeServer();

  const bound = await bindLoopbackServer();
  server = bound.server;
  activePort = bound.port;
  requestHandler = handleOAuthRequest;

  // Don't let the callback server itself keep the process alive — the
  // awaitOAuthCallback Promise is what should pin the event loop. In Electron
  // main this is a no-op (the UI keeps things alive), but in tests and CLI
  // contexts it means the process can exit cleanly between runs.
  if (typeof server.unref === "function") server.unref();

  server.on("error", (err) => {
    console.error("OAuth server error:", err);
    rejectPending(err);
  });

  const redirectUri = `http://127.0.0.1:${activePort}/oauth/callback`;
  console.log(`OAuth callback server listening on ${redirectUri}`);
  return { port: activePort, redirectUri };
}

/**
 * Wait for the authorization code to arrive at the prepared callback
 * server. Must be called after prepareOAuthCallback().
 *
 * @param {string} [expectedState] - State parameter to validate
 * @returns {Promise<{code: string, state?: string}>}
 */
function awaitOAuthCallback(expectedState) {
  return new Promise((resolve, reject) => {
    if (!server) {
      reject(new Error("OAuth callback server not prepared"));
      return;
    }
    // Only one await may be outstanding at a time.
    if (pendingResolve || pendingReject) {
      reject(new Error("An OAuth callback is already in progress"));
      return;
    }
    pendingResolve = resolve;
    pendingReject = reject;
    pendingExpectedState = expectedState || null;

    serverTimeout = setTimeout(() => {
      rejectPending(
        new Error("OAuth timeout - user did not complete authorization in time")
      );
    }, OAUTH_TIMEOUT);
  });
}

/**
 * Return the port the OAuth callback server is currently listening on, or
 * null when no callback is in flight. Used by windowManager to decide
 * whether a loopback `/oauth/callback` URL is trustworthy for the in-app
 * popup fallback.
 */
function getActiveOAuthPort() {
  return activePort;
}

/**
 * Cancel pending OAuth flow
 */
function cancelOAuthCallback() {
  rejectPending(new Error("OAuth flow cancelled"));
}

function closeServer() {
  if (server) {
    try {
      // closeAllConnections (Node 18.2+) forces any keep-alive sockets shut
      // so the port is immediately reusable — otherwise server.close() would
      // wait on idle keep-alive connections before fully releasing.
      if (typeof server.closeAllConnections === "function") {
        try {
          server.closeAllConnections();
        } catch {
          // ignore
        }
      }
      server.close();
    } catch {
      // Ignore
    }
    server = null;
  }
  activePort = null;
  requestHandler = null;
}

/**
 * Clean up pending state and close the server. Called after the code is
 * delivered, on error, and on cancellation.
 */
function cleanup() {
  if (serverTimeout) {
    clearTimeout(serverTimeout);
    serverTimeout = null;
  }
  pendingResolve = null;
  pendingReject = null;
  pendingExpectedState = null;
  closeServer();
}

/**
 * Setup IPC handlers
 * @param {Electron.IpcMain} ipcMain
 */
function setupOAuthBridge(ipcMain) {
  ipcMain.handle("oauth:prepareCallback", async () => {
    return prepareOAuthCallback();
  });

  ipcMain.handle("oauth:awaitCallback", async (_event, expectedState) => {
    return awaitOAuthCallback(expectedState);
  });

  ipcMain.handle("oauth:cancelCallback", async () => {
    cancelOAuthCallback();
  });
}

module.exports = {
  setupOAuthBridge,
  prepareOAuthCallback,
  awaitOAuthCallback,
  cancelOAuthCallback,
  getActiveOAuthPort,
};
