/**
 * Edit Key Panel - Edit existing SSH key
 */

import { Info } from 'lucide-react';
import React from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { SSHKey } from '../../types';
import { Button } from '../ui/button';
import { Input } from '../ui/input';
import { Label } from '../ui/label';
import { Textarea } from '../ui/textarea';

interface EditKeyPanelProps {
    draftKey: Partial<SSHKey>;
    _originalKey: SSHKey; // Reserved for future diff/comparison feature
    setDraftKey: (key: Partial<SSHKey>) => void;
    onExport: () => void;
    onSave: () => void;
}

export const EditKeyPanel: React.FC<EditKeyPanelProps> = ({
    draftKey,
    _originalKey, // Reserved for future diff/comparison feature
    setDraftKey,
    onExport,
    onSave,
}) => {
    const { t } = useI18n();
    return (
        <>
            <div className="space-y-2">
                <Label>{t('keychain.field.labelRequired')}</Label>
                <Input
                    value={draftKey.label || ''}
                    onChange={e => setDraftKey({ ...draftKey, label: e.target.value })}
                    placeholder={t('keychain.field.labelPlaceholder')}
                />
            </div>

            <div className="space-y-2">
                <Label className="text-destructive">{t('keychain.field.privateKeyRequired')}</Label>
                <Textarea
                    value={draftKey.privateKey || ''}
                    onChange={e => setDraftKey({ ...draftKey, privateKey: e.target.value })}
                    placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                    className="min-h-[180px] font-mono text-xs"
                />
            </div>

            <div className="space-y-2">
                <Label className="text-muted-foreground">{t('keychain.field.publicKey')}</Label>
                <Textarea
                    value={draftKey.publicKey || ''}
                    onChange={e => setDraftKey({ ...draftKey, publicKey: e.target.value })}
                    placeholder="ssh-ed25519 AAAA..."
                    className="min-h-[80px] font-mono text-xs"
                />
            </div>

            <div className="space-y-2">
                <Label className="text-muted-foreground">{t('terminal.auth.certificate')}</Label>
                <Textarea
                    value={draftKey.certificate || ''}
                    onChange={e => setDraftKey({ ...draftKey, certificate: e.target.value })}
                    placeholder={t('keychain.field.certificatePlaceholder')}
                    className="min-h-[60px] font-mono text-xs"
                />
            </div>

            {/* Key Export section */}
            <div className="pt-4 mt-4 border-t border-border/60">
                <div className="flex items-center gap-2 mb-3">
                    <span className="text-sm font-medium">{t('keychain.export.title')}</span>
                    <div className="h-4 w-4 rounded-full bg-muted flex items-center justify-center">
                        <Info size={10} className="text-muted-foreground" />
                    </div>
                </div>
                <Button className="w-full h-11" onClick={onExport}>
                    {t('keychain.export.exportToHost')}
                </Button>
            </div>

            {/* Save button */}
            <Button
                className="w-full h-11 mt-4"
                disabled={!draftKey.label?.trim() || !draftKey.privateKey?.trim()}
                onClick={onSave}
            >
                {t('common.saveChanges')}
            </Button>
        </>
    );
};
