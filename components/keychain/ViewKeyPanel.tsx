/**
 * View Key Panel - Display SSH key details
 */

import { Copy,Info } from 'lucide-react';
import React from 'react';
import { SSHKey } from '../../types';
import { Button } from '../ui/button';
import { Label } from '../ui/label';
import { copyToClipboard } from './utils';

interface ViewKeyPanelProps {
    keyItem: SSHKey;
    onExport: () => void;
}

export const ViewKeyPanel: React.FC<ViewKeyPanelProps> = ({
    keyItem,
    onExport,
}) => {
    return (
        <>
            <div className="space-y-2">
                <Label className="text-muted-foreground">Label</Label>
                <p className="text-sm">{keyItem.label}</p>
            </div>

            {keyItem.publicKey && (
                <div className="space-y-2">
                    <Label className="text-muted-foreground">Public Key</Label>
                    <div className="relative">
                        <div className="p-3 bg-card border border-border/80 rounded-lg font-mono text-xs break-all max-h-32 overflow-y-auto">
                            {keyItem.publicKey}
                        </div>
                        <Button
                            size="icon"
                            variant="ghost"
                            className="absolute top-2 right-2 h-7 w-7"
                            onClick={() => copyToClipboard(keyItem.publicKey || '')}
                        >
                            <Copy size={12} />
                        </Button>
                    </div>
                </div>
            )}

            <div className="space-y-1">
                <Label className="text-muted-foreground">Type</Label>
                <p className="text-sm">{keyItem.type}</p>
            </div>

            {/* Key Export section */}
            <div className="pt-4 mt-4 border-t border-border/60">
                <div className="flex items-center gap-2 mb-3">
                    <span className="text-sm font-medium">Key export</span>
                    <div className="h-4 w-4 rounded-full bg-muted flex items-center justify-center">
                        <Info size={10} className="text-muted-foreground" />
                    </div>
                </div>
                <Button className="w-full h-11" onClick={onExport}>
                    Export to host
                </Button>
            </div>
        </>
    );
};
