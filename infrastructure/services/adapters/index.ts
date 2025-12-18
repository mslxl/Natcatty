/**
 * Cloud Sync Adapters - Unified Export
 */

import type { CloudProvider, SyncedFile, OAuthTokens, ProviderAccount } from '../../../domain/sync';

/**
 * Unified adapter interface
 */
export interface CloudAdapter {
  readonly isAuthenticated: boolean;
  readonly accountInfo: ProviderAccount | null;
  readonly resourceId: string | null;
  
  signOut(): void;
  initializeSync(): Promise<string | null>;
  upload(syncedFile: SyncedFile): Promise<string>;
  download(): Promise<SyncedFile | null>;
  deleteSync(): Promise<void>;
  getTokens(): OAuthTokens | null;
}

/**
 * Create adapter for a specific provider
 */
export const createAdapter = async (
  provider: CloudProvider,
  tokens?: OAuthTokens,
  resourceId?: string
): Promise<CloudAdapter> => {
  switch (provider) {
    case 'github': {
      const { GitHubAdapter } = await import('./GitHubAdapter');
      return new GitHubAdapter(tokens, resourceId);
    }
    case 'google': {
      const { GoogleDriveAdapter } = await import('./GoogleDriveAdapter');
      return new GoogleDriveAdapter(tokens, resourceId);
    }
    case 'onedrive': {
      const { OneDriveAdapter } = await import('./OneDriveAdapter');
      return new OneDriveAdapter(tokens, resourceId);
    }
    default:
      throw new Error(`Unknown provider: ${provider}`);
  }
};
