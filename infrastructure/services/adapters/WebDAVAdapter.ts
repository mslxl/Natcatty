/**
 * WebDAV Adapter - webdav client library
 */

import { AuthType, createClient } from 'webdav';
import {
  SYNC_CONSTANTS,
  type WebDAVConfig,
  type SyncedFile,
  type ProviderAccount,
  type OAuthTokens,
} from '../../../domain/sync';
import { netcattyBridge } from '../netcattyBridge';

type WebDAVClient = ReturnType<typeof createClient>;

const normalizeEndpoint = (endpoint: string): string => {
  const trimmed = endpoint.trim();
  if (!/^https?:\/\//i.test(trimmed)) {
    return `https://${trimmed}`;
  }
  return trimmed;
};

const ensureLeadingSlash = (value: string): string =>
  value.startsWith('/') ? value : `/${value}`;

/**
 * Recover from trailing garbage left by non-truncating WebDAV PUT overwrites
 * (#2223). Node/V8: "Unexpected non-whitespace character after JSON at position N".
 */
const parseSyncedFileJson = (raw: string): SyncedFile => {
  try {
    return JSON.parse(raw) as SyncedFile;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    const match = /Unexpected non-whitespace character after JSON at position (\d+)/i.exec(
      message,
    );
    if (match) {
      const pos = Number(match[1]);
      if (Number.isFinite(pos) && pos > 0 && pos <= raw.length) {
        return JSON.parse(raw.slice(0, pos)) as SyncedFile;
      }
    }
    throw error;
  }
};

/**
 * Atomic replace: temp PUT + MOVE, fallback DELETE + PUT.
 * Avoids trailing-byte corruption when a shorter body overwrites a longer file
 * on WebDAV servers that do not truncate on PUT (#2223).
 */
const putWebdavFileReplacing = async (
  client: WebDAVClient,
  path: string,
  body: string,
): Promise<void> => {
  const suffix = `${Date.now().toString(16)}${Math.random().toString(16).slice(2, 10)}`;
  const tmpPath = `${path}.tmp-${suffix}`;
  let tmpWritten = false;
  try {
    await client.putFileContents(tmpPath, body, { overwrite: true });
    tmpWritten = true;
    await client.moveFile(tmpPath, path, { overwrite: true });
    return;
  } catch {
    if (tmpWritten) {
      try {
        if (await client.exists(tmpPath)) {
          await client.deleteFile(tmpPath);
        }
      } catch {
        // best-effort cleanup
      }
    }
  }

  try {
    if (await client.exists(path)) {
      await client.deleteFile(path);
    }
  } catch {
    // continue; put with overwrite may still work
  }
  await client.putFileContents(path, body, { overwrite: true });
};

export class WebDAVAdapter {
  private config: WebDAVConfig | null;
  private resource: string | null;
  private account: ProviderAccount | null;
  private client: WebDAVClient | null;

  constructor(config?: WebDAVConfig, resourceId?: string) {
    this.config = config
      ? { ...config, endpoint: normalizeEndpoint(config.endpoint) }
      : null;
    this.resource = resourceId || null;
    this.account = this.buildAccountInfo(this.config);
    this.client = this.config ? this.createClient(this.config) : null;
  }

  get isAuthenticated(): boolean {
    return !!this.config;
  }

  get accountInfo(): ProviderAccount | null {
    return this.account;
  }

  get resourceId(): string | null {
    return this.resource;
  }

  signOut(): void {
    this.config = null;
    this.resource = null;
    this.account = null;
    this.client = null;
  }

  async initializeSync(): Promise<string | null> {
    return this.withWebdavErrorContext('initialize', async () => {
      if (!this.config) {
        throw new Error('Missing WebDAV config');
      }
      const bridge = netcattyBridge.get();
      if (bridge?.cloudSyncWebdavInitialize) {
        const result = await bridge.cloudSyncWebdavInitialize(this.config);
        this.resource = result?.resourceId || this.getSyncPath();
        return this.resource;
      }
      const client = this.getClient();
      const path = this.getSyncPath();
      await client.exists(path);
      this.resource = path;
      return this.resource;
    });
  }

  async upload(syncedFile: SyncedFile): Promise<string> {
    return this.withWebdavErrorContext('upload', async () => {
      if (!this.config) {
        throw new Error('Missing WebDAV config');
      }
      const bridge = netcattyBridge.get();
      if (bridge?.cloudSyncWebdavUpload) {
        const result = await bridge.cloudSyncWebdavUpload(this.config, syncedFile);
        this.resource = result?.resourceId || this.getSyncPath();
        return this.resource;
      }
      const client = this.getClient();
      const path = this.getSyncPath();
      await putWebdavFileReplacing(client, path, JSON.stringify(syncedFile));
      this.resource = path;
      return path;
    });
  }

  async download(): Promise<SyncedFile | null> {
    return this.withWebdavErrorContext('download', async () => {
      if (!this.config) {
        throw new Error('Missing WebDAV config');
      }
      const bridge = netcattyBridge.get();
      if (bridge?.cloudSyncWebdavDownload) {
        const result = await bridge.cloudSyncWebdavDownload(this.config);
        return (result?.syncedFile ?? null) as SyncedFile | null;
      }
      const client = this.getClient();
      const path = this.getSyncPath();
      const exists = await client.exists(path);
      if (!exists) return null;
      const data = await client.getFileContents(path, { format: 'text' });
      if (!data) return null;
      return parseSyncedFileJson(data as string);
    });
  }

  async deleteSync(): Promise<void> {
    return this.withWebdavErrorContext('delete', async () => {
      if (!this.config) {
        throw new Error('Missing WebDAV config');
      }
      const bridge = netcattyBridge.get();
      if (bridge?.cloudSyncWebdavDelete) {
        await bridge.cloudSyncWebdavDelete(this.config);
        return;
      }
      const client = this.getClient();
      const path = this.getSyncPath();
      const exists = await client.exists(path);
      if (!exists) return;
      await client.deleteFile(path);
    });
  }

  getTokens(): OAuthTokens | null {
    return null;
  }

  private getClient(): WebDAVClient {
    if (!this.config || !this.client) {
      throw new Error('Missing WebDAV config');
    }
    return this.client;
  }

  private createClient(config: WebDAVConfig): WebDAVClient {
    const extraOpts: Record<string, unknown> = {};
    if (config.allowInsecure && typeof globalThis.process !== 'undefined') {
      const https = require('https');
      extraOpts.httpsAgent = new https.Agent({ rejectUnauthorized: false });
    }

    if (config.authType === 'token') {
      return createClient(config.endpoint, {
        authType: AuthType.Token,
        token: {
          access_token: config.token || '',
          token_type: 'Bearer',
        },
        ...extraOpts,
      });
    }

    if (config.authType === 'digest') {
      return createClient(config.endpoint, {
        authType: AuthType.Digest,
        username: config.username || '',
        password: config.password || '',
        ...extraOpts,
      });
    }

    return createClient(config.endpoint, {
      authType: AuthType.Password,
      username: config.username || '',
      password: config.password || '',
      ...extraOpts,
    });
  }

  private async withWebdavErrorContext<T>(
    operation: string,
    fn: () => Promise<T>,
  ): Promise<T> {
    try {
      return await fn();
    } catch (error) {
      throw this.buildWebdavError(operation, error);
    }
  }

  private buildWebdavError(operation: string, error: unknown): Error {
    const baseMessage = error instanceof Error ? error.message : String(error);
    const details: Record<string, string | number | boolean | null | undefined> = {
      operation,
    };
    const raw = error as {
      status?: number;
      statusText?: string;
      url?: string;
      method?: string;
      code?: string;
      response?: {
        status?: number;
        statusText?: string;
        url?: string;
      };
      cause?: unknown;
    };

    if (raw?.status) details.status = raw.status;
    if (raw?.statusText) details.statusText = raw.statusText;
    if (raw?.url) details.url = raw.url;
    if (raw?.method) details.method = raw.method;
    if (raw?.code) details.code = raw.code;
    if (raw?.response?.status) details.status = raw.response.status;
    if (raw?.response?.statusText) details.statusText = raw.response.statusText;
    if (raw?.response?.url) details.url = raw.response.url;
    if (raw?.cause && typeof raw.cause === 'object') {
      Object.assign(details, raw.cause as Record<string, unknown>);
      details.operation = operation;
      const cause = raw.cause as { code?: string };
      if (cause?.code) details.causeCode = cause.code;
    } else if (raw?.cause) {
      details.cause = String(raw.cause);
    }

    const err = new Error(`WebDAV ${operation} failed: ${baseMessage}`);
    (err as Error & { cause?: unknown }).cause = details;
    return err;
  }

  private getSyncPath(): string {
    return ensureLeadingSlash(SYNC_CONSTANTS.SYNC_FILE_NAME);
  }

  private buildAccountInfo(config: WebDAVConfig | null): ProviderAccount | null {
    if (!config) return null;
    try {
      const url = new URL(config.endpoint);
      const host = url.host;
      const name = config.username ? `${config.username}@${host}` : host;
      return { id: host, name };
    } catch {
      return { id: config.endpoint, name: config.endpoint };
    }
  }
}

export default WebDAVAdapter;
