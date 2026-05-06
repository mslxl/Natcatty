/**
 * Proxy Configuration Sub-Panel
 * Panel for configuring HTTP/SOCKS5 proxy settings
 */
import { Check, Trash2 } from 'lucide-react';
import React, { useMemo } from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { cn } from '../../lib/utils';
import { ProxyConfig, ProxyProfile } from '../../types';
import { AsidePanel, AsidePanelContent, type AsidePanelLayout } from '../ui/aside-panel';
import { Badge } from '../ui/badge';
import { Button } from '../ui/button';
import { Card } from '../ui/card';
import { Input } from '../ui/input';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select';

export interface ProxyPanelProps {
    proxyConfig?: ProxyConfig;
    proxyProfiles?: ProxyProfile[];
    selectedProxyProfileId?: string;
    onUpdateProxy: (field: keyof ProxyConfig, value: string | number) => void;
    onSelectProxyProfile?: (profileId: string | undefined) => void;
    onClearProxy: () => void;
    onBack: () => void;
    onCancel: () => void;
    layout?: AsidePanelLayout;
}

export const ProxyPanel: React.FC<ProxyPanelProps> = ({
    proxyConfig,
    proxyProfiles = [],
    selectedProxyProfileId,
    onUpdateProxy,
    onSelectProxyProfile,
    onClearProxy,
    onBack,
    onCancel,
    layout = 'overlay',
}) => {
    const { t } = useI18n();
    const customValue = '__custom__';
    const selectedProfile = useMemo(
        () => proxyProfiles.find((profile) => profile.id === selectedProxyProfileId),
        [proxyProfiles, selectedProxyProfileId],
    );
    const isUsingProfile = Boolean(selectedProfile);
    const canSave = isUsingProfile || Boolean(proxyConfig?.host);

    return (
        <AsidePanel
            open={true}
            onClose={onCancel}
            title={t('hostDetails.proxyPanel.title')}
            showBackButton={true}
            onBack={onBack}
            layout={layout}
            actions={
                <Button size="sm" onClick={onBack} disabled={!canSave}>
                    {t('common.save')}
                </Button>
            }
        >
            <AsidePanelContent>
                {proxyProfiles.length > 0 && onSelectProxyProfile && (
                    <Card className="p-3 space-y-3 bg-card border-border/80">
                        <p className="text-xs font-semibold">{t('hostDetails.proxyPanel.savedProxy')}</p>
                        <Select
                            value={selectedProxyProfileId || customValue}
                            onValueChange={(value) => onSelectProxyProfile(value === customValue ? undefined : value)}
                        >
                            <SelectTrigger className="h-10">
                                <SelectValue placeholder={t('hostDetails.proxyPanel.selectSaved')} />
                            </SelectTrigger>
                            <SelectContent>
                                <SelectItem value={customValue}>{t('hostDetails.proxyPanel.customProxy')}</SelectItem>
                                {proxyProfiles.map((profile) => (
                                    <SelectItem key={profile.id} value={profile.id}>
                                        {profile.label}
                                    </SelectItem>
                                ))}
                            </SelectContent>
                        </Select>
                        {selectedProfile && (
                            <div className="min-w-0 rounded-md bg-secondary/50 p-2 text-sm">
                                <div className="flex min-w-0 items-center gap-2">
                                    <Badge variant="secondary" className="text-xs shrink-0">
                                        {selectedProfile.config.type.toUpperCase()}
                                    </Badge>
                                    <span className="truncate">
                                        {selectedProfile.config.host}:{selectedProfile.config.port}
                                    </span>
                                </div>
                            </div>
                        )}
                    </Card>
                )}

                {!isUsingProfile && (
                    <>
                        <Card className="p-3 space-y-3 bg-card border-border/80">
                            <div className="flex items-center justify-between">
                                <p className="text-xs font-semibold">{t('field.type')}</p>
                                <div className="flex gap-2">
                                    <Button
                                        variant={proxyConfig?.type === 'http' ? "secondary" : "ghost"}
                                        size="sm"
                                        className={cn("h-8", proxyConfig?.type === 'http' && "bg-primary/15")}
                                        onClick={() => onUpdateProxy('type', 'http')}
                                    >
                                        <Check size={14} className={cn("mr-1", proxyConfig?.type !== 'http' && "opacity-0")} />
                                        HTTP
                                    </Button>
                                    <Button
                                        variant={proxyConfig?.type === 'socks5' ? "secondary" : "ghost"}
                                        size="sm"
                                        className={cn("h-8", proxyConfig?.type === 'socks5' && "bg-primary/15")}
                                        onClick={() => onUpdateProxy('type', 'socks5')}
                                    >
                                        <Check size={14} className={cn("mr-1", proxyConfig?.type !== 'socks5' && "opacity-0")} />
                                        SOCKS5
                                    </Button>
                                </div>
                            </div>

                            <div className="flex gap-2">
                                <Input
                                    placeholder={t('hostDetails.proxyPanel.hostPlaceholder')}
                                    value={proxyConfig?.host || ""}
                                    onChange={(e) => onUpdateProxy('host', e.target.value)}
                                    className="h-10 flex-1"
                                />
                                <div className="flex items-center gap-1">
                                    <span className="text-xs text-muted-foreground">{t('hostDetails.port')}</span>
                                    <Input
                                        type="number"
                                        placeholder="3128"
                                        value={proxyConfig?.port || ""}
                                        onChange={(e) => onUpdateProxy('port', parseInt(e.target.value) || 0)}
                                        className="h-10 w-20 text-center"
                                    />
                                </div>
                            </div>
                        </Card>

                        <Card className="p-3 space-y-3 bg-card border-border/80">
                            <div className="flex items-center justify-between">
                                <p className="text-xs font-semibold">{t('hostDetails.proxyPanel.credentials')}</p>
                                <Badge variant="secondary" className="text-xs">{t('common.optional')}</Badge>
                            </div>
                            <Input
                                placeholder={t('hostDetails.proxyPanel.usernamePlaceholder')}
                                value={proxyConfig?.username || ""}
                                onChange={(e) => onUpdateProxy('username', e.target.value)}
                                className="h-10"
                            />
                            <Input
                                placeholder={t('hostDetails.proxyPanel.passwordPlaceholder')}
                                type="password"
                                value={proxyConfig?.password || ""}
                                onChange={(e) => onUpdateProxy('password', e.target.value)}
                                className="h-10"
                            />
                        </Card>
                    </>
                )}

                {(proxyConfig?.host || selectedProxyProfileId) && (
                    <Button variant="ghost" className="w-full h-10 text-destructive" onClick={onClearProxy}>
                        <Trash2 size={14} className="mr-2" /> {t('hostDetails.proxyPanel.remove')}
                    </Button>
                )}
            </AsidePanelContent>
        </AsidePanel>
    );
};

export default ProxyPanel;
