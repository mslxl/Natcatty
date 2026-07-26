export const SDK_SESSION_ID_PREFIX = 'netcatty-sdk-session:';

export type CursorAuthModeIdentity = 'api-key' | 'cli-login';
export type CursorCliModeIdentity = 'ask' | 'agent';

export interface SdkSessionIdentityPayload {
  v: 1;
  id: string;
  backend: string;
  binPath: string;
  runtime?: 'sdk' | 'app-server';
  authMode?: CursorAuthModeIdentity;
  cliMode?: CursorCliModeIdentity;
}

export function normalizeCursorAuthMode(
  authMode: string | undefined | null,
): CursorAuthModeIdentity | undefined {
  return authMode === 'cli-login' ? 'cli-login' : authMode === 'api-key' ? 'api-key' : undefined;
}

export function normalizeCursorCliMode(
  cliMode: string | undefined | null,
): CursorCliModeIdentity | undefined {
  return cliMode === 'ask' ? 'ask' : cliMode === 'agent' ? 'agent' : undefined;
}

export function encodeSdkSessionIdentity(
  sessionId: string,
  sdkBackend?: string,
  binPath?: string,
  runtime: 'sdk' | 'app-server' = 'sdk',
  authMode?: string,
  cliMode?: string,
): string {
  if (!sessionId || !sdkBackend) return sessionId;
  const payload: SdkSessionIdentityPayload = {
    v: 1,
    id: sessionId,
    backend: sdkBackend,
    binPath: binPath || '',
    runtime,
  };
  const normalizedAuthMode = normalizeCursorAuthMode(authMode);
  if (normalizedAuthMode) payload.authMode = normalizedAuthMode;
  const normalizedCliMode = normalizeCursorCliMode(cliMode);
  if (normalizedCliMode) payload.cliMode = normalizedCliMode;
  return `${SDK_SESSION_ID_PREFIX}${encodeURIComponent(JSON.stringify(payload))}`;
}

export function parseSdkSessionIdentity(value: string | undefined | null): SdkSessionIdentityPayload | null {
  const raw = String(value || '').trim();
  if (!raw.startsWith(SDK_SESSION_ID_PREFIX)) return null;
  try {
    const parsed = JSON.parse(decodeURIComponent(raw.slice(SDK_SESSION_ID_PREFIX.length))) as SdkSessionIdentityPayload;
    if (parsed?.v !== 1 || !parsed.id || !parsed.backend) return null;
    const authMode = normalizeCursorAuthMode(parsed.authMode);
    const cliMode = normalizeCursorCliMode(parsed.cliMode);
    return {
      ...parsed,
      runtime: parsed.runtime === 'app-server' ? 'app-server' : 'sdk',
      ...(authMode ? { authMode } : {}),
      ...(cliMode ? { cliMode } : {}),
    };
  } catch {
    return null;
  }
}
