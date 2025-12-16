/**
 * Biometric Backend Hook
 * 
 * Provides React state management for Termius-style biometric SSH keys.
 * These are standard ED25519 keys protected by OS Secure Storage (Keychain/DPAPI).
 * 
 * On Windows, WebAuthn (Windows Hello) must be called directly from the renderer
 * during a user gesture (click handler). The credential is then passed to the
 * main process for storage.
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

/**
 * Check if WebAuthn platform authenticator is available (Windows Hello / Touch ID)
 */
async function checkWebAuthnAvailable(): Promise<boolean> {
  if (typeof window === 'undefined' || !window.PublicKeyCredential) {
    return false;
  }
  try {
    return await PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
  } catch {
    return false;
  }
}

/**
 * Create a WebAuthn credential directly in the page context
 * This MUST be called from a user gesture (click handler)
 */
async function createWebAuthnCredential(userName: string): Promise<{ credentialId: string; rawId: string } | null> {
  if (typeof window === 'undefined' || !window.PublicKeyCredential) {
    throw new Error("WebAuthn is not available");
  }
  
  // Generate a random challenge
  const challenge = new Uint8Array(32);
  crypto.getRandomValues(challenge);
  
  // Generate a random user ID
  const userId = new Uint8Array(16);
  crypto.getRandomValues(userId);
  
  const publicKeyCredentialCreationOptions: PublicKeyCredentialCreationOptions = {
    challenge,
    rp: {
      name: "Netcatty",
      id: "localhost", // For Electron, we use localhost
    },
    user: {
      id: userId,
      name: userName,
      displayName: userName,
    },
    pubKeyCredParams: [
      { type: "public-key", alg: -7 },   // ES256
      { type: "public-key", alg: -257 }, // RS256
    ],
    authenticatorSelection: {
      authenticatorAttachment: "platform", // Force platform authenticator (Windows Hello)
      userVerification: "required",
      residentKey: "discouraged",
    },
    timeout: 120000,
    attestation: "none",
  };
  
  console.log("[WebAuthn] Creating credential with platform authenticator...");
  
  const credential = await navigator.credentials.create({
    publicKey: publicKeyCredentialCreationOptions,
  }) as PublicKeyCredential | null;
  
  if (!credential) {
    return null;
  }
  
  // Convert ArrayBuffer to base64 for storage
  const rawIdArray = new Uint8Array(credential.rawId);
  const rawIdB64 = btoa(String.fromCharCode(...rawIdArray));
  
  console.log("[WebAuthn] Credential created successfully");
  
  return {
    credentialId: credential.id,
    rawId: rawIdB64,
  };
}

/**
 * Verify a WebAuthn credential (get assertion)
 * This MUST be called from a user gesture (click handler)
 */
async function verifyWebAuthnCredential(credentialIdB64: string): Promise<{ authenticatorData: string; signature: string } | null> {
  if (typeof window === 'undefined' || !window.PublicKeyCredential) {
    throw new Error("WebAuthn is not available");
  }
  
  // Decode the credential ID from base64
  const credentialIdBytes = Uint8Array.from(atob(credentialIdB64), c => c.charCodeAt(0));
  
  // Generate a random challenge
  const challenge = new Uint8Array(32);
  crypto.getRandomValues(challenge);
  
  const publicKeyCredentialRequestOptions: PublicKeyCredentialRequestOptions = {
    challenge,
    rpId: "localhost",
    allowCredentials: [{
      type: "public-key",
      id: credentialIdBytes,
      transports: ["internal"],
    }],
    userVerification: "required",
    timeout: 120000,
  };
  
  console.log("[WebAuthn] Getting assertion...");
  
  const assertion = await navigator.credentials.get({
    publicKey: publicKeyCredentialRequestOptions,
  }) as PublicKeyCredential | null;
  
  if (!assertion) {
    return null;
  }
  
  const response = assertion.response as AuthenticatorAssertionResponse;
  const authenticatorDataArray = new Uint8Array(response.authenticatorData);
  const signatureArray = new Uint8Array(response.signature);
  
  return {
    authenticatorData: btoa(String.fromCharCode(...authenticatorDataArray)),
    signature: btoa(String.fromCharCode(...signatureArray)),
  };
}

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
      
      // On Windows, also check WebAuthn availability directly from the page context
      if (supportResult.platform === 'win32') {
        try {
          const webauthnAvailable = await checkWebAuthnAvailable();
          supportResult.hasWindowsHello = webauthnAvailable;
          supportResult.supported = supportResult.hasSshKeygen && webauthnAvailable;
        } catch {
          // WebAuthn check failed, keep original values
        }
      }
      
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
   * 
   * On Windows, this function MUST be called from a user gesture (click handler)
   * because WebAuthn requires user activation. The flow is:
   * 1. Call WebAuthn to create credential (triggers Windows Hello)
   * 2. Send credential ID to main process
   * 3. Main process generates SSH key and stores passphrase
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
      // On Windows, call WebAuthn directly in the page context (requires user gesture)
      // This MUST be called from a click handler to satisfy WebAuthn requirements
      let credentialId: string | undefined;
      
      const isWindows = navigator.platform?.toLowerCase().includes('win') || 
                        navigator.userAgent?.toLowerCase().includes('windows');
      
      if (isWindows) {
        console.log("[BiometricBackend] Calling WebAuthn createCredential directly in page context...");
        const credential = await createWebAuthnCredential(label || keyId);
        if (!credential) {
          throw new Error("Windows Hello credential creation was cancelled or failed");
        }
        credentialId = credential.rawId; // Use rawId (base64) for storage
        console.log("[BiometricBackend] WebAuthn credential created:", credentialId.substring(0, 20) + "...");
      }
      
      // Call main process to generate SSH key, passing the credential ID if we have one
      const generateResult = await bridge.biometricGenerate({ 
        keyId, 
        label,
        // Pass credential ID to main process so it can store for later verification
        ...(credentialId && { windowsHelloCredentialId: credentialId }),
      });
      setResult(generateResult);
      
      if (generateResult.success) {
        setState("success");
      } else {
        setError(generateResult.error || "Key generation failed");
        setState("error");
      }
      
      return generateResult;
    } catch (err) {
      const errorMsg = err instanceof Error ? err.message : String(err);
      const errorResult = { success: false, error: errorMsg };
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
