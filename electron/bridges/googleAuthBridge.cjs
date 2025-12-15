/**
 * Google OAuth Bridge (main process)
 *
 * Renderer fetches to Google's OAuth token endpoint can be blocked by CORS.
 * This bridge proxies Google token exchange/refresh and userinfo via the main process.
 */

const GOOGLE_TOKEN_URL = "https://oauth2.googleapis.com/token";
const GOOGLE_USERINFO_URL = "https://www.googleapis.com/oauth2/v2/userinfo";

const isNonEmptyString = (v) => typeof v === "string" && v.trim().length > 0;

/**
 * @param {Electron.IpcMain} ipcMain
 */
function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:google:oauth:exchange", async (_event, payload) => {
    const clientId = payload?.clientId;
    const clientSecret = payload?.clientSecret;
    const code = payload?.code;
    const codeVerifier = payload?.codeVerifier;
    const redirectUri = payload?.redirectUri;

    if (!isNonEmptyString(clientId)) throw new Error("Missing Google clientId");
    if (!isNonEmptyString(code)) throw new Error("Missing authorization code");
    if (!isNonEmptyString(codeVerifier)) throw new Error("Missing codeVerifier");
    if (!isNonEmptyString(redirectUri)) throw new Error("Missing redirectUri");

    const body = new URLSearchParams({
      client_id: clientId,
      code,
      code_verifier: codeVerifier,
      grant_type: "authorization_code",
      redirect_uri: redirectUri,
    });
    if (isNonEmptyString(clientSecret)) {
      body.set("client_secret", clientSecret);
    }

    const res = await fetch(GOOGLE_TOKEN_URL, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
      },
      body: body.toString(),
    });

    const text = await res.text();
    if (!res.ok) {
      let data;
      try {
        data = JSON.parse(text);
      } catch {
        data = { error: text };
      }
      throw new Error(`Google token exchange failed: ${data.error_description || data.error || res.status}`);
    }

    let data;
    try {
      data = JSON.parse(text);
    } catch {
      throw new Error(`Google token exchange invalid JSON: ${text.slice(0, 200)}`);
    }

    return {
      accessToken: data.access_token,
      refreshToken: data.refresh_token,
      expiresAt: Date.now() + (data.expires_in || 0) * 1000,
      tokenType: data.token_type,
      scope: data.scope,
    };
  });

  ipcMain.handle("netcatty:google:oauth:refresh", async (_event, payload) => {
    const clientId = payload?.clientId;
    const clientSecret = payload?.clientSecret;
    const refreshToken = payload?.refreshToken;

    if (!isNonEmptyString(clientId)) throw new Error("Missing Google clientId");
    if (!isNonEmptyString(refreshToken)) throw new Error("Missing refreshToken");

    const body = new URLSearchParams({
      client_id: clientId,
      refresh_token: refreshToken,
      grant_type: "refresh_token",
    });
    if (isNonEmptyString(clientSecret)) {
      body.set("client_secret", clientSecret);
    }

    const res = await fetch(GOOGLE_TOKEN_URL, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
      },
      body: body.toString(),
    });

    const text = await res.text();
    if (!res.ok) {
      let data;
      try {
        data = JSON.parse(text);
      } catch {
        data = { error: text };
      }
      throw new Error(`Google token refresh failed: ${data.error_description || data.error || res.status}`);
    }

    let data;
    try {
      data = JSON.parse(text);
    } catch {
      throw new Error(`Google token refresh invalid JSON: ${text.slice(0, 200)}`);
    }

    return {
      accessToken: data.access_token,
      refreshToken,
      expiresAt: Date.now() + (data.expires_in || 0) * 1000,
      tokenType: data.token_type,
      scope: data.scope,
    };
  });

  ipcMain.handle("netcatty:google:oauth:userinfo", async (_event, payload) => {
    const accessToken = payload?.accessToken;
    if (!isNonEmptyString(accessToken)) throw new Error("Missing accessToken");

    const res = await fetch(GOOGLE_USERINFO_URL, {
      headers: {
        Authorization: `Bearer ${accessToken}`,
      },
    });

    const text = await res.text();
    if (!res.ok) {
      throw new Error(`Google userinfo failed: ${res.status} - ${text.slice(0, 200)}`);
    }

    try {
      return JSON.parse(text);
    } catch {
      throw new Error(`Google userinfo invalid JSON: ${text.slice(0, 200)}`);
    }
  });
}

module.exports = { registerHandlers };

