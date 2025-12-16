/**
 * Identity Card component for displaying saved identities
 */

import { Pencil,User } from 'lucide-react';
import React from 'react';
import { cn } from '../../lib/utils';
import { Identity } from '../../types';
import { Button } from '../ui/button';

interface IdentityCardProps {
    identity: Identity;
    viewMode: 'grid' | 'list';
    isSelected: boolean;
    onClick: () => void;
}

export const IdentityCard: React.FC<IdentityCardProps> = ({
    identity,
    viewMode,
    isSelected,
    onClick,
}) => {
    const getAuthMethodDisplay = (method: string) => {
        switch (method) {
            case 'password': return 'Password';
            case 'key': return 'Key';
            case 'certificate': return 'Certificate';
            default: return method;
        }
    };

    return (
        <div
            className={cn(
                "group cursor-pointer",
                viewMode === 'grid'
                    ? "soft-card elevate rounded-xl h-[68px] px-3 py-2"
                    : "h-14 px-3 py-2 hover:bg-secondary/60 rounded-lg transition-colors",
                isSelected && "ring-2 ring-primary"
            )}
            onClick={onClick}
        >
            <div className="flex items-center gap-3 h-full">
                <div className="h-11 w-11 rounded-xl bg-green-500/15 text-green-500 flex items-center justify-center">
                    <User size={18} />
                </div>
                <div className="min-w-0 flex-1">
                    <div className="text-sm font-semibold truncate">{identity.label || 'Add a label...'}</div>
                    <div className="text-[11px] font-mono text-muted-foreground truncate">
                        {getAuthMethodDisplay(identity.authMethod)}
                    </div>
                </div>
                <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                    {viewMode === 'list' && (
                        <Button
                            size="icon"
                            variant="ghost"
                            className="h-8 w-8"
                            onClick={(e) => {
                                e.stopPropagation();
                                onClick();
                            }}
                        >
                            <Pencil size={14} />
                        </Button>
                    )}
                </div>
            </div>
        </div>
    );
};
