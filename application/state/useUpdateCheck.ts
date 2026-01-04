import { useCallback, useEffect, useRef, useState } from 'react';
import { checkForUpdates, getReleaseUrl, type ReleaseInfo, type UpdateCheckResult } from '../../infrastructure/services/updateService';
import { localStorageAdapter } from '../../infrastructure/persistence/localStorageAdapter';
import { STORAGE_KEY_UPDATE_DISMISSED_VERSION, STORAGE_KEY_UPDATE_LAST_CHECK } from '../../infrastructure/config/storageKeys';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';

// Check for updates at most once per hour
const UPDATE_CHECK_INTERVAL_MS = 60 * 60 * 1000;
// Delay startup check to avoid slowing down app launch
const STARTUP_CHECK_DELAY_MS = 5000;
// Enable demo mode for development (set via localStorage: localStorage.setItem('debug.updateDemo', '1'))
const IS_UPDATE_DEMO_MODE = typeof window !== 'undefined' && 
  window.localStorage?.getItem('debug.updateDemo') === '1';

// Debug logging for update checks
const debugLog = (...args: unknown[]) => {
  if (IS_UPDATE_DEMO_MODE || (typeof window !== 'undefined' && window.localStorage?.getItem('debug.updateCheck') === '1')) {
    console.log('[UpdateCheck]', ...args);
  }
};

export interface UpdateState {
  isChecking: boolean;
  hasUpdate: boolean;
  currentVersion: string;
  latestRelease: ReleaseInfo | null;
  error: string | null;
  lastCheckedAt: number | null;
}

export interface UseUpdateCheckResult {
  updateState: UpdateState;
  checkNow: () => Promise<UpdateCheckResult | null>;
  dismissUpdate: () => void;
  openReleasePage: () => void;
}

/**
 * Hook for managing update checks
 * - Automatically checks for updates on startup (with delay)
 * - Respects dismissed version to avoid nagging
 * - Provides manual check capability
 */
export function useUpdateCheck(): UseUpdateCheckResult {
  const [updateState, setUpdateState] = useState<UpdateState>({
    isChecking: false,
    hasUpdate: false,
    currentVersion: '',
    latestRelease: null,
    error: null,
    lastCheckedAt: null,
  });

  const hasCheckedOnStartupRef = useRef(false);
  const isCheckingRef = useRef(false);
  const startupCheckTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Get current app version
  useEffect(() => {
    const loadVersion = async () => {
      try {
        const bridge = netcattyBridge.get();
        const info = await bridge?.getAppInfo?.();
        if (info?.version) {
          setUpdateState((prev) => ({ ...prev, currentVersion: info.version }));
        }
      } catch {
        // Ignore - running without Electron bridge
      }
    };
    void loadVersion();
  }, []);

  const performCheck = useCallback(async (currentVersion: string): Promise<UpdateCheckResult | null> => {
    debugLog('performCheck called', { currentVersion, IS_UPDATE_DEMO_MODE });
    
    // In demo mode, use a fake version to allow checking
    const effectiveVersion = IS_UPDATE_DEMO_MODE ? '0.0.1' : currentVersion;
    
    if (!effectiveVersion || effectiveVersion === '0.0.0') {
      debugLog('Skipping check - invalid version:', effectiveVersion);
      // Skip check for dev builds
      return null;
    }

    if (isCheckingRef.current) {
      debugLog('Already checking, skipping');
      return null;
    }

    isCheckingRef.current = true;
    setUpdateState((prev) => ({ ...prev, isChecking: true, error: null }));

    try {
      let result: UpdateCheckResult;
      
      if (IS_UPDATE_DEMO_MODE) {
        debugLog('Demo mode: creating fake update result');
        // Simulate a short delay like a real API call
        await new Promise(resolve => setTimeout(resolve, 500));
        // In demo mode, create a fake update result
        result = {
          hasUpdate: true,
          currentVersion: '0.0.1',
          latestRelease: {
            version: '1.0.0',
            tagName: 'v1.0.0',
            name: 'Netcatty v1.0.0',
            body: 'Demo release for testing update notification',
            htmlUrl: 'https://github.com/binaricat/Netcatty/releases',
            publishedAt: new Date().toISOString(),
            assets: [],
          },
        };
      } else {
        result = await checkForUpdates(currentVersion);
      }
      debugLog('Check result:', result);
      debugLog('Latest release version:', result.latestRelease?.version);
      const now = Date.now();

      // Save last check time
      localStorageAdapter.writeNumber(STORAGE_KEY_UPDATE_LAST_CHECK, now);

      // Check if this version was dismissed
      const dismissedVersion = localStorageAdapter.readString(STORAGE_KEY_UPDATE_DISMISSED_VERSION);
      const showUpdate = result.hasUpdate && 
        result.latestRelease?.version !== dismissedVersion;
      
      debugLog('Show update:', showUpdate, 'dismissed version:', dismissedVersion);
      debugLog('Setting state with hasUpdate:', showUpdate);

      setUpdateState((prev) => {
        debugLog('State updated:', { ...prev, hasUpdate: showUpdate, latestRelease: result.latestRelease });
        return {
          ...prev,
          isChecking: false,
          hasUpdate: showUpdate,
          latestRelease: result.latestRelease,
          error: result.error || null,
          lastCheckedAt: now,
        };
      });

      return result;
    } catch (error) {
      const errorMsg = error instanceof Error ? error.message : 'Unknown error';
      setUpdateState((prev) => ({
        ...prev,
        isChecking: false,
        error: errorMsg,
      }));
      return null;
    } finally {
      isCheckingRef.current = false;
    }
  }, []);

  const checkNow = useCallback(async () => {
    // In demo mode, use fake version to allow checking
    const version = IS_UPDATE_DEMO_MODE ? '0.0.1' : updateState.currentVersion;
    return performCheck(version);
  }, [performCheck, updateState.currentVersion]);

  const dismissUpdate = useCallback(() => {
    if (updateState.latestRelease?.version) {
      localStorageAdapter.writeString(
        STORAGE_KEY_UPDATE_DISMISSED_VERSION,
        updateState.latestRelease.version
      );
    }
    setUpdateState((prev) => ({ ...prev, hasUpdate: false }));
  }, [updateState.latestRelease?.version]);

  const openReleasePage = useCallback(async () => {
    const url = updateState.latestRelease
      ? getReleaseUrl(updateState.latestRelease.version)
      : getReleaseUrl();

    try {
      const bridge = netcattyBridge.get();
      if (bridge?.openExternal) {
        await bridge.openExternal(url);
        return;
      }
    } catch {
      // Fallback to window.open
    }
    window.open(url, '_blank', 'noopener,noreferrer');
  }, [updateState.latestRelease]);

  // Startup check with delay - runs once on mount
  useEffect(() => {
    debugLog('Startup check effect mounted, IS_UPDATE_DEMO_MODE:', IS_UPDATE_DEMO_MODE);
    
    // In demo mode, trigger check immediately after a short delay
    if (IS_UPDATE_DEMO_MODE) {
      debugLog('Demo mode: scheduling update check in', STARTUP_CHECK_DELAY_MS, 'ms');
      
      startupCheckTimeoutRef.current = setTimeout(() => {
        debugLog('=== Demo mode: Triggering update check ===');
        void performCheck('0.0.1');
      }, STARTUP_CHECK_DELAY_MS);
      
      return () => {
        if (startupCheckTimeoutRef.current) {
          clearTimeout(startupCheckTimeoutRef.current);
        }
      };
    }
    
    // Normal mode: wait for version to be loaded, then check
    // This is handled by the version-dependent effect below
  }, [performCheck]);

  // Normal mode startup check - depends on currentVersion
  useEffect(() => {
    // Skip in demo mode (handled above)
    if (IS_UPDATE_DEMO_MODE) {
      return;
    }
    
    debugLog('Version check effect', { 
      hasChecked: hasCheckedOnStartupRef.current, 
      currentVersion: updateState.currentVersion
    });
    
    if (hasCheckedOnStartupRef.current) {
      return;
    }

    if (!updateState.currentVersion || updateState.currentVersion === '0.0.0') {
      return;
    }

    // Check if we've checked recently
    const lastCheck = localStorageAdapter.readNumber(STORAGE_KEY_UPDATE_LAST_CHECK);
    const now = Date.now();
    if (lastCheck && now - lastCheck < UPDATE_CHECK_INTERVAL_MS) {
      hasCheckedOnStartupRef.current = true;
      return;
    }

    hasCheckedOnStartupRef.current = true;
    debugLog('Starting delayed update check for version:', updateState.currentVersion);

    startupCheckTimeoutRef.current = setTimeout(() => {
      debugLog('=== Delayed check triggered ===');
      void performCheck(updateState.currentVersion);
    }, STARTUP_CHECK_DELAY_MS);

    return () => {
      if (startupCheckTimeoutRef.current) {
        clearTimeout(startupCheckTimeoutRef.current);
      }
    };
  }, [updateState.currentVersion, performCheck]);

  return {
    updateState,
    checkNow,
    dismissUpdate,
    openReleasePage,
  };
}
