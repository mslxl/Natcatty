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
    const client = this.getClient();
    const path = this.getSyncPath();
    await client.exists(path);
    this.resource = path;
    return this.resource;
  }

  async upload(syncedFile: SyncedFile): Promise<string> {
    const client = this.getClient();
    const path = this.getSyncPath();
    await client.putFileContents(path, JSON.stringify(syncedFile), { overwrite: true });
    this.resource = path;
    return path;
  }

  async download(): Promise<SyncedFile | null> {
    const client = this.getClient();
    const path = this.getSyncPath();
    const exists = await client.exists(path);
    if (!exists) return null;
    const data = await client.getFileContents(path, { format: 'text' });
    if (!data) return null;
    return JSON.parse(data as string) as SyncedFile;
  }

  async deleteSync(): Promise<void> {
    const client = this.getClient();
    const path = this.getSyncPath();
    const exists = await client.exists(path);
    if (!exists) return;
    await client.deleteFile(path);
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
    if (config.authType === 'token') {
      return createClient(config.endpoint, {
        authType: AuthType.Token,
        token: {
          access_token: config.token || '',
          token_type: 'Bearer',
        },
      });
    }

    if (config.authType === 'digest') {
      return createClient(config.endpoint, {
        authType: AuthType.Digest,
        username: config.username || '',
        password: config.password || '',
      });
    }

    return createClient(config.endpoint, {
      authType: AuthType.Password,
      username: config.username || '',
      password: config.password || '',
    });
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
