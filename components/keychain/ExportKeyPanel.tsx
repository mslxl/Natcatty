/**
 * Export Key Panel - Export SSH key to remote host
 */

import { ChevronRight, Info } from 'lucide-react';
import React, { useState } from 'react';
import { useKeychainBackend } from '../../application/state/useKeychainBackend';
import { cn } from '../../lib/utils';
import { Host, SSHKey } from '../../types';
import { Button } from '../ui/button';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible';
import { Input } from '../ui/input';
import { Label } from '../ui/label';
import { Textarea } from '../ui/textarea';
import { toast } from '../ui/toast';
import { getKeyIcon, getKeyTypeDisplay, isMacOS } from './utils';

interface ExportKeyPanelProps {
    keyItem: SSHKey;
    _hosts: Host[]; // Reserved for future inline host list/validation
    keys: SSHKey[];
    exportHost: Host | null;
    _setExportHost: (host: Host | null) => void; // Host selection handled by onShowHostSelector callback
    onShowHostSelector: () => void;
    onSaveHost?: (host: Host) => void;
    onClose: () => void;
}

const DEFAULT_EXPORT_SCRIPT = `DIR="$HOME/$1"
FILE="$DIR/$2"
if [ ! -d "$DIR" ]; then
  mkdir -p "$DIR"
  chmod 700 "$DIR"
fi
if [ ! -f "$FILE" ]; then
  touch "$FILE"
  chmod 600 "$FILE"
fi
echo $3 >> "$FILE"`;

export const ExportKeyPanel: React.FC<ExportKeyPanelProps> = ({
    keyItem,
    _hosts, // Reserved for future inline host list/validation
    keys,
    exportHost,
    _setExportHost, // Host selection handled by onShowHostSelector callback
    onShowHostSelector,
	onSaveHost,
	onClose,
}) => {
	const { execCommand } = useKeychainBackend();
	const [exportLocation, setExportLocation] = useState('.ssh');
	const [exportFilename, setExportFilename] = useState('authorized_keys');
	const [exportAdvancedOpen, setExportAdvancedOpen] = useState(false);
	const [exportScript, setExportScript] = useState(DEFAULT_EXPORT_SCRIPT);
	const [isExporting, setIsExporting] = useState(false);

    const isMac = isMacOS();

    const handleExport = async () => {
        if (!exportHost || !keyItem.publicKey) return;

        setIsExporting(true);

        try {
            // Check for authentication method
            if (!exportHost.password && !exportHost.identityFileId) {
                throw new Error('Host has no saved password or key. Please add password credentials to the host first.');
            }

            // Get private key for authentication if host uses key auth
            const hostPrivateKey = exportHost.identityFileId
                ? keys.find(k => k.id === exportHost.identityFileId)?.privateKey
                : undefined;

            // Escape the public key for shell
            const escapedPublicKey = keyItem.publicKey.replace(/'/g, "'\\''");

            // Build the command by replacing $1, $2, $3
            const scriptWithVars = exportScript
                .replace(/\$1/g, exportLocation)
                .replace(/\$2/g, exportFilename)
                .replace(/\$3/g, `'${escapedPublicKey}'`);

			const command = scriptWithVars;

			// Execute via SSH
			const result = await execCommand({
				hostname: exportHost.hostname,
				username: exportHost.username,
				port: exportHost.port || 22,
				password: exportHost.password,
                privateKey: hostPrivateKey,
                command,
                timeout: 30000,
            });

            // Check result
            const exitCode = result?.code;
            const hasError = result?.stderr?.trim();
            if (exitCode === 0 || (exitCode == null && !hasError)) {
                // Update host to use this key for authentication
                if (onSaveHost) {
                    const updatedHost: Host = {
                        ...exportHost,
                        identityFileId: keyItem.id,
                        authMethod: 'key',
                    };
                    onSaveHost(updatedHost);
                }
                toast.success(`Public key exported and attached to ${exportHost.label}`, 'Export Successful');
                onClose();
            } else {
                const errorMsg = hasError || result?.stdout?.trim() || `Command exited with code ${exitCode}`;
                toast.error(`Failed to export key: ${errorMsg}`, 'Export Failed');
            }
        } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            toast.error(`Export failed: ${message}`, 'Export Failed');
        } finally {
            setIsExporting(false);
        }
    };

    return (
        <>
            {/* Key info card */}
            <div className="flex items-center gap-3 p-3 bg-card border border-border/80 rounded-lg">
                <div className={cn(
                    "h-10 w-10 rounded-md flex items-center justify-center",
                    keyItem.certificate
                      ? "bg-emerald-500/15 text-emerald-500"
                      : "bg-primary/15 text-primary"
                )}>
                    {getKeyIcon(keyItem)}
                </div>
                <div className="min-w-0 flex-1">
                    <p className="text-sm font-semibold truncate">{keyItem.label}</p>
                    <p className="text-xs text-muted-foreground">Type {getKeyTypeDisplay(keyItem, isMac)}</p>
                </div>
            </div>

            {/* Export to field */}
            <div className="space-y-2">
                <div className="flex items-center justify-between">
                    <Label className="text-muted-foreground">Export to *</Label>
                    <Button
                        variant="link"
                        className="h-auto p-0 text-primary text-sm"
                        onClick={onShowHostSelector}
                    >
                        Select Host
                    </Button>
                </div>
                <Input
                    value={exportHost?.label || ''}
                    readOnly
                    placeholder="Select a host..."
                    className="bg-muted/50 cursor-pointer"
                    onClick={onShowHostSelector}
                />
            </div>

            {/* Location field */}
            <div className="space-y-2">
                <Label className="text-muted-foreground">Location ~ $1 *</Label>
                <Input
                    value={exportLocation}
                    onChange={e => setExportLocation(e.target.value)}
                    placeholder=".ssh"
                />
            </div>

            {/* Filename field */}
            <div className="space-y-2">
                <Label className="text-muted-foreground">Filename ~ $2 *</Label>
                <Input
                    value={exportFilename}
                    onChange={e => setExportFilename(e.target.value)}
                    placeholder="authorized_keys"
                />
            </div>

            {/* Info note */}
            <div className="flex items-start gap-2 p-3 bg-muted/50 border border-border/60 rounded-lg">
                <Info size={14} className="mt-0.5 text-muted-foreground shrink-0" />
                <p className="text-xs text-muted-foreground">
                    Key export currently supports only <span className="font-semibold text-foreground">UNIX</span> systems.
                    Use <span className="font-semibold text-foreground">Advanced</span> section to customize the export script.
                </p>
            </div>

            {/* Advanced collapsible */}
            <Collapsible open={exportAdvancedOpen} onOpenChange={setExportAdvancedOpen}>
                <CollapsibleTrigger asChild>
                    <Button variant="ghost" className="w-full justify-between px-0 h-10 hover:bg-transparent hover:text-current">
                        <span className="font-medium">Advanced</span>
                        <ChevronRight size={16} className={cn(
                            "transition-transform",
                            exportAdvancedOpen && "rotate-90"
                        )} />
                    </Button>
                </CollapsibleTrigger>
                <CollapsibleContent className="space-y-2 pt-2">
                    <Label className="text-muted-foreground">Script *</Label>
                    <Textarea
                        value={exportScript}
                        onChange={e => setExportScript(e.target.value)}
                        className="min-h-[180px] font-mono text-xs"
                        placeholder="Export script..."
                    />
                </CollapsibleContent>
            </Collapsible>

            {/* Export button */}
            <Button
                className="w-full h-11"
                disabled={!exportHost || !exportLocation || !exportFilename || isExporting}
                onClick={handleExport}
            >
                {isExporting ? 'Exporting...' : 'Export and Attach'}
            </Button>
        </>
    );
};
