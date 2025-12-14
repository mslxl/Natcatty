/**
 * useAutoSync - Auto-sync Hook for Cloud Sync
 * 
 * Provides automatic sync capabilities:
 * - Sync when data changes (hosts, keys, snippets, port forwarding rules)
 * - Check remote version on app startup
 * - Debounced sync to avoid too frequent API calls
 */

import { useCallback, useEffect, useRef } from 'react';
import { useCloudSync } from './useCloudSync';
import type { SyncPayload } from '../../domain/sync';
import { toast } from '../../components/ui/toast';

interface AutoSyncConfig {
  // Data to sync
  hosts: SyncPayload['hosts'];
  keys: SyncPayload['keys'];
  snippets: SyncPayload['snippets'];
  customGroups: SyncPayload['customGroups'];
  portForwardingRules?: SyncPayload['portForwardingRules'];
  knownHosts?: SyncPayload['knownHosts'];
  
  // Callbacks
  onApplyPayload: (payload: SyncPayload) => void;
}

export const useAutoSync = (config: AutoSyncConfig) => {
  const sync = useCloudSync();
  const syncTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const lastSyncedDataRef = useRef<string>('');
  const hasCheckedRemoteRef = useRef(false);
  const isInitializedRef = useRef(false);
  
  // Build sync payload
  const buildPayload = useCallback((): SyncPayload => {
    return {
      hosts: config.hosts,
      keys: config.keys,
      snippets: config.snippets,
      customGroups: config.customGroups,
      portForwardingRules: config.portForwardingRules,
      knownHosts: config.knownHosts,
      syncedAt: Date.now(),
    };
  }, [config.hosts, config.keys, config.snippets, config.customGroups, config.portForwardingRules, config.knownHosts]);
  
  // Create a hash of current data for comparison
  const getDataHash = useCallback(() => {
    const data = {
      hosts: config.hosts,
      keys: config.keys,
      snippets: config.snippets,
      portForwardingRules: config.portForwardingRules,
    };
    return JSON.stringify(data);
  }, [config.hosts, config.keys, config.snippets, config.portForwardingRules]);
  
  // Sync now handler
  const syncNow = useCallback(async () => {
    if (!sync.hasAnyConnectedProvider || sync.isSyncing || !sync.isUnlocked) {
      return;
    }
    
    try {
      const payload = buildPayload();
      await sync.syncNow(payload);
      lastSyncedDataRef.current = getDataHash();
    } catch (error) {
      console.error('[AutoSync] Sync failed:', error);
      toast.error('Sync failed', error instanceof Error ? error.message : 'Unknown error');
    }
  }, [sync, buildPayload, getDataHash]);
  
  // Check remote version and pull if newer (on startup)
  const checkRemoteVersion = useCallback(async () => {
    if (!sync.hasAnyConnectedProvider || !sync.isUnlocked || hasCheckedRemoteRef.current) {
      return;
    }
    
    hasCheckedRemoteRef.current = true;
    
    // Find connected provider
    const connectedProvider = 
      sync.providers.github.status === 'connected' ? 'github' :
      sync.providers.google.status === 'connected' ? 'google' :
      sync.providers.onedrive.status === 'connected' ? 'onedrive' : null;
    
    if (!connectedProvider) return;
    
    try {
      console.log('[AutoSync] Checking remote version...');
      const remotePayload = await sync.downloadFromProvider(connectedProvider);
      
      if (remotePayload && remotePayload.syncedAt > sync.localUpdatedAt) {
        console.log('[AutoSync] Remote is newer, applying...');
        config.onApplyPayload(remotePayload);
        toast.success('Synced from cloud', 'Your data has been updated from the cloud.');
      }
    } catch (error) {
      console.error('[AutoSync] Failed to check remote version:', error);
      // Don't show error toast for initial check - it's not critical
    }
  }, [sync, config]);
  
  // Debounced auto-sync when data changes
  useEffect(() => {
    // Skip if not ready
    if (!sync.hasAnyConnectedProvider || !sync.autoSyncEnabled || !sync.isUnlocked) {
      return;
    }
    
    // Skip initial render
    if (!isInitializedRef.current) {
      isInitializedRef.current = true;
      lastSyncedDataRef.current = getDataHash();
      return;
    }
    
    const currentHash = getDataHash();
    
    // Skip if data hasn't changed
    if (currentHash === lastSyncedDataRef.current) {
      return;
    }
    
    // Clear existing timeout
    if (syncTimeoutRef.current) {
      clearTimeout(syncTimeoutRef.current);
    }
    
    // Debounce sync by 3 seconds
    syncTimeoutRef.current = setTimeout(() => {
      console.log('[AutoSync] Data changed, syncing...');
      syncNow();
    }, 3000);
    
    return () => {
      if (syncTimeoutRef.current) {
        clearTimeout(syncTimeoutRef.current);
      }
    };
  }, [sync.hasAnyConnectedProvider, sync.autoSyncEnabled, sync.isUnlocked, getDataHash, syncNow]);
  
  // Check remote version on startup/unlock
  useEffect(() => {
    if (sync.hasAnyConnectedProvider && sync.isUnlocked && !hasCheckedRemoteRef.current) {
      // Delay check to ensure everything is loaded
      const timer = setTimeout(() => {
        checkRemoteVersion();
      }, 1000);
      
      return () => clearTimeout(timer);
    }
  }, [sync.hasAnyConnectedProvider, sync.isUnlocked, checkRemoteVersion]);
  
  // Reset check flag when provider disconnects
  useEffect(() => {
    if (!sync.hasAnyConnectedProvider) {
      hasCheckedRemoteRef.current = false;
    }
  }, [sync.hasAnyConnectedProvider]);
  
  return {
    syncNow,
    buildPayload,
    isSyncing: sync.isSyncing,
    isConnected: sync.hasAnyConnectedProvider,
    autoSyncEnabled: sync.autoSyncEnabled,
  };
};

export default useAutoSync;
