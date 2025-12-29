/**
 * GitHub OAuth Bridge (main process)
 *
 * Renderer fetches to `github.com/login/*` are blocked by CORS.
 * This bridge proxies GitHub Device Flow endpoints via the main process.
 */

const GITHUB_CLIENT_ID = process.env.VITE_SYNC_GITHUB_CLIENT_ID || "";
const GITHUB_DEVICE_CODE_URL = "https://github.com/login/device/code";
const GITHUB_ACCESS_TOKEN_URL = "https://github.com/login/oauth/access_token";

/**
 * @param {Electron.IpcMain} ipcMain
 */
function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:github:deviceFlow:start", async (_event, payload) => {
    const clientId = payload?.clientId || GITHUB_CLIENT_ID;
    const scope = payload?.scope || "gist read:user";

    const res = await fetch(GITHUB_DEVICE_CODE_URL, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
      },
      body: new URLSearchParams({
        client_id: clientId,
        scope,
      }).toString(),
    });

    const text = await res.text();
    if (!res.ok) {
      throw new Error(`GitHub device flow failed: ${res.status} - ${text}`);
    }

    let data;
    try {
      data = JSON.parse(text);
    } catch {
      throw new Error(`GitHub device flow invalid JSON: ${text.slice(0, 200)}`);
    }

    return {
      deviceCode: data.device_code,
      userCode: data.user_code,
      verificationUri: data.verification_uri,
      expiresAt: Date.now() + (data.expires_in || 0) * 1000,
      interval: data.interval || 5,
    };
  });

  ipcMain.handle("netcatty:github:deviceFlow:poll", async (_event, payload) => {
    const clientId = payload?.clientId || GITHUB_CLIENT_ID;
    const deviceCode = payload?.deviceCode;
    if (!deviceCode) throw new Error("Missing deviceCode");

    const res = await fetch(GITHUB_ACCESS_TOKEN_URL, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
      },
      body: new URLSearchParams({
        client_id: clientId,
        device_code: deviceCode,
        grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      }).toString(),
    });

    const text = await res.text();
    if (!res.ok) {
      throw new Error(`GitHub token polling failed: ${res.status} - ${text}`);
    }

    try {
      return JSON.parse(text);
    } catch {
      throw new Error(`GitHub token polling invalid JSON: ${text.slice(0, 200)}`);
    }
  });
}

module.exports = { registerHandlers };
