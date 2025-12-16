/**
 * Generate FIDO2 Key Panel - Hardware security key (YubiKey, Windows Hello, etc.)
 * 
 * This panel provides a full FIDO2 key generation workflow:
 * 1. Device selection (USB keys and platform authenticators)
 * 2. Key configuration (label, options)
 * 3. PIN entry (when required by authenticator)
 * 4. Touch prompt (user presence verification)
 */

import { EyeOff, Fingerprint, RefreshCw, Shield, Usb } from 'lucide-react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useFido2Backend, Fido2Device } from '../../application/state/useFido2Backend';
import { cn } from '../../lib/utils';
import { SSHKey } from '../../types';
import { Button } from '../ui/button';
import { Input } from '../ui/input';
import { Label } from '../ui/label';
import { Switch } from '../ui/switch.tsx';

interface GenerateFido2PanelProps {
    draftKey: Partial<SSHKey>;
    setDraftKey: (key: Partial<SSHKey>) => void;
    isGenerating: boolean;
    onGenerate: (result: { success: boolean; publicKey?: string; privateKey?: string; error?: string }) => void;
}

// Device Card Component
const DeviceCard: React.FC<{
    device: Fido2Device;
    selected: boolean;
    onClick: () => void;
}> = ({ device, selected, onClick }) => {
    const isInternal = device.transport === 'internal';

    return (
        <button
            onClick={onClick}
            className={cn(
                "w-full flex items-center gap-3 p-3 rounded-lg border transition-all cursor-pointer text-left",
                selected
                    ? "border-primary bg-primary/5 ring-1 ring-primary/30"
                    : "border-border/60 hover:border-border hover:bg-muted/50"
            )}
        >
            <div className={cn(
                "w-12 h-12 rounded-lg flex items-center justify-center shrink-0",
                isInternal ? "bg-blue-500/10 text-blue-500" : "bg-amber-500/10 text-amber-500"
            )}>
                {isInternal ? (
                    <span className="text-xs font-bold text-white bg-[#1a365d] px-1.5 py-0.5 rounded">fido</span>
                ) : (
                    <Usb size={20} />
                )}
            </div>
            <div className="min-w-0 flex-1">
                <p className="text-sm font-medium truncate">{device.label}</p>
                <p className="text-xs text-muted-foreground truncate">{device.manufacturer}</p>
            </div>
        </button>
    );
};

// PIN Entry Modal Component
const PinEntryModal: React.FC<{
    open: boolean;
    onSubmit: (pin: string) => void;
    onCancel: () => void;
}> = ({ open, onSubmit, onCancel }) => {
    const [pin, setPin] = useState("");
    const [showPin, setShowPin] = useState(false);

    const handleSubmit = (e: React.FormEvent) => {
        e.preventDefault();
        if (pin.trim()) {
            onSubmit(pin);
            setPin("");
        }
    };

    useEffect(() => {
        if (!open) {
            setPin("");
            setShowPin(false);
        }
    }, [open]);

    if (!open) return null;

    return (
        <div className="absolute inset-0 bg-background/95 backdrop-blur-sm z-50 flex items-center justify-center p-4">
            <div className="w-full max-w-sm bg-card border border-border rounded-lg shadow-lg p-6 space-y-4">
                <div className="text-center">
                    <div className="w-16 h-16 mx-auto mb-4 bg-primary/10 rounded-full flex items-center justify-center">
                        <Shield size={32} className="text-primary" />
                    </div>
                    <h3 className="text-lg font-semibold">PIN Required</h3>
                    <p className="text-sm text-muted-foreground mt-1">
                        Enter the PIN for your security key
                    </p>
                </div>

                <form onSubmit={handleSubmit} className="space-y-4">
                    <div className="relative">
                        <Input
                            type={showPin ? "text" : "password"}
                            value={pin}
                            onChange={(e) => setPin(e.target.value)}
                            placeholder="Enter PIN"
                            autoFocus
                            className="pr-12"
                        />
                        <button
                            type="button"
                            onClick={() => setShowPin(!showPin)}
                            className="absolute right-3 top-1/2 -translate-y-1/2 p-1 text-muted-foreground hover:text-foreground cursor-pointer"
                        >
                            {showPin ? <EyeOff size={16} /> : <span className="text-xs">Show</span>}
                        </button>
                    </div>

                    <div className="flex gap-2">
                        <Button
                            type="button"
                            variant="outline"
                            className="flex-1"
                            onClick={onCancel}
                        >
                            Cancel
                        </Button>
                        <Button
                            type="submit"
                            className="flex-1"
                            disabled={!pin.trim()}
                        >
                            Submit
                        </Button>
                    </div>
                </form>
            </div>
        </div>
    );
};

// Touch Prompt Modal Component
const TouchPromptModal: React.FC<{
    open: boolean;
    onCancel: () => void;
}> = ({ open, onCancel }) => {
    if (!open) return null;

    return (
        <div className="absolute inset-0 bg-background/95 backdrop-blur-sm z-50 flex items-center justify-center p-4">
            <div className="w-full max-w-sm bg-card border border-border rounded-lg shadow-lg p-6 space-y-4">
                <div className="text-center">
                    {/* Animated touch indicator */}
                    <div className="w-24 h-24 mx-auto mb-4 relative">
                        <div className="absolute inset-0 bg-primary/10 rounded-full animate-ping" />
                        <div className="absolute inset-2 bg-primary/20 rounded-full animate-pulse" />
                        <div className="absolute inset-0 flex items-center justify-center">
                            <Fingerprint size={40} className="text-primary" />
                        </div>
                    </div>
                    <h3 className="text-lg font-semibold">Touch Your Security Key</h3>
                    <p className="text-sm text-muted-foreground mt-1">
                        Tap or touch your security key to confirm
                    </p>
                </div>

                <Button
                    variant="outline"
                    className="w-full"
                    onClick={onCancel}
                >
                    Cancel
                </Button>
            </div>
        </div>
    );
};

// Main FIDO2 Panel Component
export const GenerateFido2Panel: React.FC<GenerateFido2PanelProps> = ({
    draftKey,
    setDraftKey,
    isGenerating: externalIsGenerating,
    onGenerate,
}) => {
    const {
        state,
        devices,
        support,
        selectedDevice,
        error,
        isGenerating: internalIsGenerating,
        checkSupport,
        listDevices,
        refreshDevices,
        selectDevice,
        generateKey,
        submitPin,
        cancelPin,
        cancelGeneration,
    } = useFido2Backend();

    const [requireUserPresence, setRequireUserPresence] = useState(true);
    const [requirePinCode, setRequirePinCode] = useState(false);
    const [passphrase, setPassphrase] = useState("");
    const [savePassphrase, setSavePassphrase] = useState(false);
    const [showPassphrase, setShowPassphrase] = useState(false);
    const [isInitialized, setIsInitialized] = useState(false);

    // Initialize on mount
    useEffect(() => {
        if (!isInitialized) {
            checkSupport().then(() => {
                listDevices();
                setIsInitialized(true);
            });
        }
    }, [isInitialized, checkSupport, listDevices]);

    // Combined generating state
    const isGenerating = externalIsGenerating || internalIsGenerating;

    // Handle device selection
    const handleDeviceSelect = useCallback((device: Fido2Device) => {
        selectDevice(device);
    }, [selectDevice]);

    // Handle key generation
    const handleGenerate = useCallback(async () => {
        if (!selectedDevice || !draftKey.label?.trim()) return;

        const result = await generateKey({
            label: draftKey.label.trim(),
            devicePath: selectedDevice.path,
            requireUserPresence,
            requirePinCode,
            passphrase: passphrase || undefined,
        });

        // Update draft key with generated key data
        if (result.success && result.publicKey && result.privateKey) {
            setDraftKey({
                ...draftKey,
                publicKey: result.publicKey,
                privateKey: result.privateKey,
                type: 'ECDSA', // ed25519-sk reports as ECDSA in OpenSSH format
                source: 'fido2',
                passphrase: savePassphrase ? passphrase : undefined,
                savePassphrase,
            });
        }

        onGenerate(result);
    }, [
        selectedDevice,
        draftKey,
        setDraftKey,
        requireUserPresence,
        requirePinCode,
        passphrase,
        savePassphrase,
        generateKey,
        onGenerate,
    ]);

    // Handle PIN submission
    const handlePinSubmit = useCallback((pin: string) => {
        submitPin(pin);
    }, [submitPin]);

    // Handle cancellation
    const handleCancel = useCallback(() => {
        if (state === 'waiting-pin') {
            cancelPin();
        } else {
            cancelGeneration();
        }
    }, [state, cancelPin, cancelGeneration]);

    // Show appropriate view based on state
    const showDeviceSelector = state === 'selecting-device' || state === 'loading-devices' || state === 'idle';
    const showPinModal = state === 'waiting-pin';
    const showTouchModal = state === 'waiting-touch';

    // Check if we have USB devices (excluding internal)
    const usbDevices = useMemo(() =>
        devices.filter(d => d.transport === 'usb'),
        [devices]
    );
    const internalDevices = useMemo(() =>
        devices.filter(d => d.transport === 'internal'),
        [devices]
    );

    // Error display for unsupported systems
    if (support && !support.supported) {
        return (
            <div className="space-y-4">
                <div className="bg-destructive/10 border border-destructive/20 rounded-lg p-4">
                    <p className="text-sm text-destructive font-medium">FIDO2 Not Supported</p>
                    <p className="text-xs text-muted-foreground mt-1">
                        {support.error || "Your system does not support FIDO2 key generation."}
                    </p>
                </div>
            </div>
        );
    }

    return (
        <div className="space-y-4 relative">
            {/* Device Selector */}
            {showDeviceSelector && (
                <>
                    {/* Section Header with Refresh */}
                    <div className="flex items-center justify-between">
                        <Label className="text-muted-foreground">Please select the hardware key:</Label>
                        <Button
                            variant="ghost"
                            size="sm"
                            onClick={refreshDevices}
                            disabled={state === 'loading-devices'}
                            className="h-7 px-2"
                        >
                            <RefreshCw size={14} className={state === 'loading-devices' ? 'animate-spin' : ''} />
                        </Button>
                    </div>

                    {/* Device List */}
                    <div className="bg-card border border-border/80 rounded-lg p-3 space-y-2">
                        {state === 'loading-devices' ? (
                            <div className="py-8 text-center">
                                <div className="w-8 h-8 mx-auto mb-2 border-2 border-primary/30 border-t-primary rounded-full animate-spin" />
                                <p className="text-sm text-muted-foreground">Detecting devices...</p>
                            </div>
                        ) : devices.length === 0 ? (
                            <div className="py-8 text-center">
                                <Usb size={32} className="mx-auto mb-2 text-muted-foreground/50" />
                                <p className="text-sm text-muted-foreground">No FIDO2 devices found</p>
                                <p className="text-xs text-muted-foreground mt-1">
                                    Connect a security key and click refresh
                                </p>
                            </div>
                        ) : (
                            <>
                                {/* Platform authenticators first */}
                                {internalDevices.map((device) => (
                                    <DeviceCard
                                        key={device.id}
                                        device={device}
                                        selected={selectedDevice?.id === device.id}
                                        onClick={() => handleDeviceSelect(device)}
                                    />
                                ))}

                                {/* USB devices */}
                                {usbDevices.length > 0 && internalDevices.length > 0 && (
                                    <div className="border-t border-border/60 my-2" />
                                )}
                                {usbDevices.map((device) => (
                                    <DeviceCard
                                        key={device.id}
                                        device={device}
                                        selected={selectedDevice?.id === device.id}
                                        onClick={() => handleDeviceSelect(device)}
                                    />
                                ))}
                            </>
                        )}
                    </div>
                </>
            )}

            {/* Key Configuration - Only show when device is selected */}
            {selectedDevice && showDeviceSelector && (
                <>
                    {/* Label */}
                    <div className="bg-card border border-border/80 rounded-lg p-4 space-y-4">
                        <div className="space-y-2">
                            <Label>Label</Label>
                            <Input
                                value={draftKey.label || ''}
                                onChange={e => setDraftKey({ ...draftKey, label: e.target.value })}
                                placeholder="My Security Key"
                            />
                        </div>

                        {/* Options */}
                        <div className="space-y-3">
                            <div className="flex items-center justify-between">
                                <div>
                                    <Label>Require User Presence</Label>
                                    <p className="text-xs text-muted-foreground">Touch key for each use</p>
                                </div>
                                <Switch
                                    checked={requireUserPresence}
                                    onCheckedChange={setRequireUserPresence}
                                />
                            </div>

                            <div className="flex items-center justify-between">
                                <div>
                                    <Label>Require Pin Code</Label>
                                    <p className="text-xs text-muted-foreground">Enter PIN for each use</p>
                                </div>
                                <Switch
                                    checked={requirePinCode}
                                    onCheckedChange={setRequirePinCode}
                                />
                            </div>
                        </div>
                    </div>

                    {/* Passphrase (optional) */}
                    <div className="bg-card border border-border/80 rounded-lg p-4 space-y-3">
                        <div className="space-y-2">
                            <Label className="text-muted-foreground">Passphrase (optional)</Label>
                            <div className="relative">
                                <Input
                                    type={showPassphrase ? "text" : "password"}
                                    value={passphrase}
                                    onChange={e => setPassphrase(e.target.value)}
                                    placeholder="Passphrase"
                                    className="pr-12"
                                />
                                <button
                                    type="button"
                                    onClick={() => setShowPassphrase(!showPassphrase)}
                                    className="absolute right-3 top-1/2 -translate-y-1/2 p-1 text-muted-foreground hover:text-foreground cursor-pointer"
                                >
                                    {showPassphrase ? <EyeOff size={16} /> : <span className="text-xs">Show</span>}
                                </button>
                            </div>
                        </div>

                        <div className="flex items-center justify-between">
                            <Label className="text-muted-foreground">Save passphrase</Label>
                            <Switch
                                checked={savePassphrase}
                                onCheckedChange={setSavePassphrase}
                                disabled={!passphrase}
                            />
                        </div>
                    </div>
                </>
            )}

            {/* Error Display */}
            {error && state === 'error' && (
                <div className="bg-destructive/10 border border-destructive/20 rounded-lg p-3">
                    <p className="text-sm text-destructive">{error}</p>
                </div>
            )}

            {/* Generate Button */}
            {showDeviceSelector && (
                <Button
                    className="w-full h-11"
                    onClick={handleGenerate}
                    disabled={isGenerating || !draftKey.label?.trim() || !selectedDevice}
                >
                    {isGenerating ? (
                        <div className="h-4 w-4 border-2 border-primary-foreground/30 border-t-primary-foreground rounded-full animate-spin" />
                    ) : (
                        <>
                            <Shield size={14} className="mr-2" />
                            Register Security Key
                        </>
                    )}
                </Button>
            )}

            {/* PIN Entry Modal */}
            <PinEntryModal
                open={showPinModal}
                onSubmit={handlePinSubmit}
                onCancel={handleCancel}
            />

            {/* Touch Prompt Modal */}
            <TouchPromptModal
                open={showTouchModal}
                onCancel={handleCancel}
            />

            {/* Generating Overlay (when not showing PIN/Touch modals) */}
            {state === 'generating' && !showPinModal && !showTouchModal && (
                <div className="absolute inset-0 bg-background/80 backdrop-blur-sm z-40 flex items-center justify-center">
                    <div className="text-center">
                        <div className="w-12 h-12 mx-auto mb-3 border-2 border-primary/30 border-t-primary rounded-full animate-spin" />
                        <p className="text-sm font-medium">Generating key...</p>
                        <Button
                            variant="ghost"
                            size="sm"
                            className="mt-2"
                            onClick={handleCancel}
                        >
                            Cancel
                        </Button>
                    </div>
                </div>
            )}
        </div>
    );
};
