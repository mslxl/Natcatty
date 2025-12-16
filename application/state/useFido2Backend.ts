/**
 * FIDO2 Backend Hook
 * 
 * Provides React state management for FIDO2 SSH key generation workflow
 * including device enumeration, PIN/touch prompts, and key generation.
 */

import { useCallback, useEffect, useState } from "react";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";

export interface Fido2Device {
  id: string;
  label: string;
  manufacturer: string;
  path: string;
  transport: "usb" | "internal";
  vendorId?: number;
  productId?: number;
}

export interface Fido2Support {
  supported: boolean;
  sshKeygenPath: string | null;
  version?: string;
  error?: string;
}

export interface Fido2GenerateOptions {
  label: string;
  devicePath: string;
  requireUserPresence?: boolean;
  requirePinCode?: boolean;
  resident?: boolean;
  passphrase?: string;
}

export interface Fido2GenerateResult {
  success: boolean;
  publicKey?: string;
  privateKey?: string;
  keyType?: string;
  error?: string;
}

export type Fido2State = 
  | "idle"
  | "loading-devices"
  | "selecting-device"
  | "generating"
  | "waiting-pin"
  | "waiting-touch"
  | "success"
  | "error";

export const useFido2Backend = () => {
  const [state, setState] = useState<Fido2State>("idle");
  const [devices, setDevices] = useState<Fido2Device[]>([]);
  const [support, setSupport] = useState<Fido2Support | null>(null);
  const [selectedDevice, setSelectedDevice] = useState<Fido2Device | null>(null);
  const [currentRequestId, setCurrentRequestId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<Fido2GenerateResult | null>(null);

  // Check FIDO2 support on mount
  const checkSupport = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.fido2CheckSupport) {
      setSupport({ supported: false, sshKeygenPath: null, error: "FIDO2 not available" });
      return { supported: false, sshKeygenPath: null, error: "FIDO2 not available" };
    }
    
    try {
      const supportResult = await bridge.fido2CheckSupport();
      setSupport(supportResult);
      return supportResult;
    } catch (err) {
      const errorResult = { supported: false, sshKeygenPath: null, error: String(err) };
      setSupport(errorResult);
      return errorResult;
    }
  }, []);

  // List available FIDO2 devices
  const listDevices = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.fido2ListDevices) {
      setDevices([]);
      return [];
    }

    setState("loading-devices");
    setError(null);

    try {
      const deviceList = await bridge.fido2ListDevices();
      setDevices(deviceList);
      setState("selecting-device");
      return deviceList;
    } catch (err) {
      setError(`Failed to list devices: ${err}`);
      setState("error");
      return [];
    }
  }, []);

  // Refresh devices
  const refreshDevices = useCallback(async () => {
    return listDevices();
  }, [listDevices]);

  // Select a device
  const selectDevice = useCallback((device: Fido2Device | null) => {
    setSelectedDevice(device);
  }, []);

  // Generate FIDO2 key
  const generateKey = useCallback(async (options: Fido2GenerateOptions): Promise<Fido2GenerateResult> => {
    const bridge = netcattyBridge.get();
    if (!bridge?.fido2Generate) {
      const errorResult = { success: false, error: "FIDO2 generation not available" };
      setResult(errorResult);
      setState("error");
      setError(errorResult.error);
      return errorResult;
    }

    const requestId = `fido2_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
    setCurrentRequestId(requestId);
    setState("generating");
    setError(null);
    setResult(null);

    try {
      const generateResult = await bridge.fido2Generate({
        requestId,
        ...options,
      });

      if (generateResult.success) {
        setState("success");
        setResult(generateResult);
      } else {
        setState("error");
        setError(generateResult.error || "Generation failed");
        setResult(generateResult);
      }

      setCurrentRequestId(null);
      return generateResult;
    } catch (err) {
      const errorResult = { success: false, error: String(err) };
      setState("error");
      setError(String(err));
      setResult(errorResult);
      setCurrentRequestId(null);
      return errorResult;
    }
  }, []);

  // Submit PIN
  const submitPin = useCallback(async (pin: string) => {
    const bridge = netcattyBridge.get();
    if (!bridge?.fido2SubmitPin || !currentRequestId) {
      return { success: false, error: "No active PIN request" };
    }

    setState("generating"); // Back to generating after PIN entry
    return bridge.fido2SubmitPin(currentRequestId, pin);
  }, [currentRequestId]);

  // Cancel PIN entry
  const cancelPin = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.fido2CancelPin || !currentRequestId) {
      return { success: false };
    }

    return bridge.fido2CancelPin(currentRequestId);
  }, [currentRequestId]);

  // Cancel generation
  const cancelGeneration = useCallback(async () => {
    const bridge = netcattyBridge.get();
    if (!bridge?.fido2Cancel || !currentRequestId) {
      setState("idle");
      return { success: false };
    }

    const result = await bridge.fido2Cancel(currentRequestId);
    setState("idle");
    setCurrentRequestId(null);
    return result;
  }, [currentRequestId]);

  // Reset state
  const reset = useCallback(() => {
    setState("idle");
    setSelectedDevice(null);
    setCurrentRequestId(null);
    setError(null);
    setResult(null);
  }, []);

  // Start fresh flow
  const startFlow = useCallback(async () => {
    reset();
    await checkSupport();
    await listDevices();
  }, [reset, checkSupport, listDevices]);

  // Listen for PIN/touch prompts from main process
  useEffect(() => {
    const bridge = netcattyBridge.get();
    if (!bridge) return;

    const unsubscribers: (() => void)[] = [];

    if (bridge.onFido2PinRequest) {
      const unsubscribe = bridge.onFido2PinRequest((requestId) => {
        if (requestId === currentRequestId) {
          setState("waiting-pin");
        }
      });
      unsubscribers.push(unsubscribe);
    }

    if (bridge.onFido2TouchPrompt) {
      const unsubscribe = bridge.onFido2TouchPrompt((requestId) => {
        if (requestId === currentRequestId) {
          setState("waiting-touch");
        }
      });
      unsubscribers.push(unsubscribe);
    }

    return () => {
      unsubscribers.forEach((unsub) => unsub());
    };
  }, [currentRequestId]);

  return {
    // State
    state,
    devices,
    support,
    selectedDevice,
    error,
    result,
    isGenerating: state === "generating" || state === "waiting-pin" || state === "waiting-touch",

    // Actions
    checkSupport,
    listDevices,
    refreshDevices,
    selectDevice,
    generateKey,
    submitPin,
    cancelPin,
    cancelGeneration,
    reset,
    startFlow,
  };
};
