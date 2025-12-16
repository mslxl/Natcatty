/**
 * Environment Variables Sub-Panel
 * Panel for configuring environment variables for SSH connections
 */
import { Plus,X } from 'lucide-react';
import React from 'react';
import { EnvVar } from '../../types';
import { AsidePanel,AsidePanelContent } from '../ui/aside-panel';
import { Button } from '../ui/button';
import { Card } from '../ui/card';
import { Input } from '../ui/input';

export interface EnvVarsPanelProps {
    hostLabel: string;
    hostHostname: string;
    environmentVariables: EnvVar[];
    newEnvName: string;
    newEnvValue: string;
    setNewEnvName: (name: string) => void;
    setNewEnvValue: (value: string) => void;
    onAddEnvVar: () => void;
    onRemoveEnvVar: (index: number) => void;
    onUpdateEnvVar: (index: number, field: 'name' | 'value', value: string) => void;
    onSave: () => void;
    onBack: () => void;
    onCancel: () => void;
}

export const EnvVarsPanel: React.FC<EnvVarsPanelProps> = ({
    hostLabel,
    hostHostname,
    environmentVariables,
    newEnvName,
    newEnvValue,
    setNewEnvName,
    setNewEnvValue,
    onAddEnvVar,
    onRemoveEnvVar,
    onUpdateEnvVar,
    onSave,
    onBack,
    onCancel,
}) => {
    return (
        <AsidePanel
            open={true}
            onClose={onCancel}
            title="Environment Variables"
            showBackButton={true}
            onBack={onBack}
            actions={
                <Button size="sm" onClick={onSave}>
                    Save
                </Button>
            }
        >
            <AsidePanelContent>
                <div className="text-sm text-muted-foreground">
                    Set an environment variable for <span className="font-semibold text-foreground">{hostLabel || hostHostname}</span>.
                    <p className="text-xs mt-1">Some SSH servers by default only allow variables with prefix LC_ and LANG_.</p>
                </div>

                <Button className="w-full h-10" onClick={onAddEnvVar} disabled={!newEnvName.trim()}>
                    <Plus size={14} className="mr-2" /> Add a variable
                </Button>

                {/* Existing variables */}
                {environmentVariables.map((envVar, index) => (
                    <Card key={index} className="p-3 space-y-2 bg-card border-border/80">
                        <div className="flex items-center justify-between">
                            <span className="text-xs font-semibold">Variable</span>
                            <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 text-muted-foreground hover:text-destructive"
                                onClick={() => onRemoveEnvVar(index)}
                            >
                                <X size={14} />
                            </Button>
                        </div>
                        <Input
                            placeholder="Variable"
                            value={envVar.name}
                            onChange={(e) => onUpdateEnvVar(index, 'name', e.target.value)}
                            className="h-10"
                        />
                        <Input
                            placeholder="Value"
                            value={envVar.value}
                            onChange={(e) => onUpdateEnvVar(index, 'value', e.target.value)}
                            className="h-10"
                        />
                    </Card>
                ))}

                {/* New variable input */}
                <Card className="p-3 space-y-2 bg-card border-border/80">
                    <div className="flex items-center justify-between">
                        <span className="text-xs font-semibold">New Variable</span>
                        <X size={14} className="text-muted-foreground opacity-0" />
                    </div>
                    <Input
                        placeholder="Variable name"
                        value={newEnvName}
                        onChange={(e) => setNewEnvName(e.target.value)}
                        className="h-10"
                    />
                    <Input
                        placeholder="Value"
                        value={newEnvValue}
                        onChange={(e) => setNewEnvValue(e.target.value)}
                        className="h-10"
                    />
                </Card>
            </AsidePanelContent>
        </AsidePanel>
    );
};

export default EnvVarsPanel;
