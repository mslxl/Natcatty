/**
 * Biometric Backend Hook
 * 
 * Provides React state management for Termius-style biometric SSH keys.
 * These are standard ED25519 keys protected by OS Secure Storage (Keychain/DPAPI).
 */

import { useCallback, useState } from "react";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";

export interface BiometricSupport {
  supported: boolean;
  hasKeytar: boolean;
  hasSshKeygen: boolean;
  sshKeygenPath: string | null;
  platform: string;
  hasWindowsHello: boolean;
  error: string | null;
}

export interface BiometricGenerateResult {
  success: boolean;
  publicKey?: string;
  privateKey?: string;
  keyType?: string;
  error?: string;
}

export type BiometricState = 
  | "idle"
  | "checking"
  | "generating"
  | "success"
  | "error";

export const useBiometricBackend = () => {
  const [state, setState] = useState<BiometricState>("idle");
  const [support, setSupport] = useState<BiometricSupport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<BiometricGenerateResult | null>(null);

  /**
   * Check if biometric key features are available
   */
  const checkSupport = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.biometricCheckSupport) {
      const fallback: BiometricSupport = {
        supported: false,
        hasKeytar: false,
        hasSshKeygen: false,
        sshKeygenPath: null,
        platform: "unknown",
        hasWindowsHello: false,
        error: "Biometric API not available",
      };
      setSupport(fallback);
      return fallback;
    }

    setState("checking");
    setError(null);

    try {
      const supportResult = await bridge.biometricCheckSupport();
      setSupport(supportResult);
      setState("idle");
      return supportResult;
    } catch (err) {
      const errorResult: BiometricSupport = {
        supported: false,
        hasKeytar: false,
        hasSshKeygen: false,
        sshKeygenPath: null,
        platform: "unknown",
        hasWindowsHello: false,
        error: String(err),
      };
      setSupport(errorResult);
      setError(String(err));
      setState("error");
      return errorResult;
    }
  }, []);

  /**
   * Generate a new biometric-protected SSH key
   * @param keyId Unique identifier for this key (used for passphrase storage)
   * @param label Human-readable label for the key
   */
  const generateKey = useCallback(async (keyId: string, label: string): Promise<BiometricGenerateResult> => {
    const bridge = netcattyBridge.get();
    if (!bridge?.biometricGenerate) {
      const errorResult = { success: false, error: "Biometric API not available" };
      setError(errorResult.error);
      setState("error");
      return errorResult;
    }

    setState("generating");
    setError(null);
    setResult(null);

    try {
      const generateResult = await bridge.biometricGenerate({ keyId, label });
      setResult(generateResult);
      
      if (generateResult.success) {
        setState("success");
      } else {
        setError(generateResult.error || "Key generation failed");
        setState("error");
      }
      
      return generateResult;
    } catch (err) {
      const errorResult = { success: false, error: String(err) };
      setError(errorResult.error);
      setState("error");
      return errorResult;
    }
  }, []);

  /**
   * Retrieve the passphrase for a biometric key
   * This will trigger biometric verification (Touch ID / Windows Hello)
   * @param keyId The key identifier used during generation
   */
  const getPassphrase = useCallback(async (keyId: string): Promise<{ success: boolean; passphrase?: string; error?: string }> => {
    const bridge = netcattyBridge.get();
    if (!bridge?.biometricGetPassphrase) {
      return { success: false, error: "Biometric API not available" };
    }

    try {
      return await bridge.biometricGetPassphrase({ keyId });
    } catch (err) {
      return { success: false, error: String(err) };
    }
  }, []);

  /**
   * Delete the stored passphrase for a biometric key
   * Should be called when the key is deleted
   * @param keyId The key identifier
   */
  const deletePassphrase = useCallback(async (keyId: string): Promise<{ success: boolean; error?: string }> => {
    const bridge = netcattyBridge.get();
    if (!bridge?.biometricDeletePassphrase) {
      return { success: false, error: "Biometric API not available" };
    }

    try {
      return await bridge.biometricDeletePassphrase({ keyId });
    } catch (err) {
      return { success: false, error: String(err) };
    }
  }, []);

  /**
   * List all stored biometric key IDs
   */
  const listStoredKeys = useCallback(async (): Promise<{ success: boolean; keyIds?: string[]; error?: string }> => {
    const bridge = netcattyBridge.get();
    if (!bridge?.biometricListKeys) {
      return { success: false, error: "Biometric API not available" };
    }

    try {
      return await bridge.biometricListKeys();
    } catch (err) {
      return { success: false, error: String(err) };
    }
  }, []);

  /**
   * Reset state to idle
   */
  const reset = useCallback(() => {
    setState("idle");
    setError(null);
    setResult(null);
  }, []);

  return {
    state,
    support,
    error,
    result,
    checkSupport,
    generateKey,
    getPassphrase,
    deletePassphrase,
    listStoredKeys,
    reset,
  };
};
