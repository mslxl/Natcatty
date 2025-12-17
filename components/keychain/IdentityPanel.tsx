/**
 * Identity Panel - Create/Edit identity
 */

import { BadgeCheck,ChevronDown,Eye,EyeOff,Key,User } from 'lucide-react';
import React from 'react';
import { useI18n } from '../../application/i18n/I18nProvider';
import { Identity,SSHKey } from '../../types';
import { Button } from '../ui/button';
import { Dropdown,DropdownContent,DropdownTrigger } from '../ui/dropdown';
import { Input } from '../ui/input';
import { Label } from '../ui/label';

interface IdentityPanelProps {
    draftIdentity: Partial<Identity>;
    setDraftIdentity: (identity: Partial<Identity>) => void;
    keys: SSHKey[];
    showPassphrase: boolean;
    setShowPassphrase: (show: boolean) => void;
    isNew: boolean;
    onSave: () => void;
}

export const IdentityPanel: React.FC<IdentityPanelProps> = ({
    draftIdentity,
    setDraftIdentity,
    keys,
    showPassphrase,
    setShowPassphrase,
    isNew,
    onSave,
}) => {
    const { t } = useI18n();
    return (
        <>
            <div className="flex items-center gap-3 mb-4">
                <div className="h-10 w-10 rounded-lg bg-green-500/15 text-green-500 flex items-center justify-center">
                    <User size={20} />
                </div>
                <Input
                    value={draftIdentity.label || ''}
                    onChange={e => setDraftIdentity({ ...draftIdentity, label: e.target.value })}
                    placeholder={t('keychain.field.label')}
                    className="flex-1"
                />
            </div>

            <div className="space-y-2">
                <Label>{t('keychain.identity.usernameRequired')}</Label>
                <div className="relative">
                    <User size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
                    <Input
                        value={draftIdentity.username || ''}
                        onChange={e => setDraftIdentity({ ...draftIdentity, username: e.target.value })}
                        placeholder={t('terminal.auth.username')}
                        className="pl-9"
                    />
                </div>
            </div>

            <div className="space-y-2">
                <Label>{t('terminal.auth.passwordLabel')}</Label>
                <div className="relative">
                    <Input
                        type={showPassphrase ? 'text' : 'password'}
                        value={draftIdentity.password || ''}
                        onChange={e => setDraftIdentity({ ...draftIdentity, password: e.target.value })}
                        placeholder={t('terminal.auth.password.placeholder')}
                        className="pr-10"
                    />
                    <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="absolute right-1 top-1/2 -translate-y-1/2 h-8 w-8"
                        onClick={() => setShowPassphrase(!showPassphrase)}
                    >
                        {showPassphrase ? <EyeOff size={14} /> : <Eye size={14} />}
                    </Button>
                </div>
            </div>

            <div className="space-y-2">
                <Label>{t('hostForm.auth.method')}</Label>
                <Dropdown>
                    <DropdownTrigger asChild>
                        <Button variant="secondary" className="w-full justify-between h-10">
                            <span className="flex items-center gap-2">
                                {draftIdentity.authMethod === 'key' && <><Key size={14} /> {t('terminal.auth.sshKey')}</>}
                                {draftIdentity.authMethod === 'certificate' && <><BadgeCheck size={14} /> {t('terminal.auth.certificate')}</>}
                                {(!draftIdentity.authMethod || draftIdentity.authMethod === 'password') && t('keychain.identity.method.passwordOnly')}
                            </span>
                            <ChevronDown size={14} />
                        </Button>
                    </DropdownTrigger>
                    <DropdownContent className="w-56" align="start">
                        <Button
                            variant="ghost"
                            className="w-full justify-start gap-2"
                            onClick={() => setDraftIdentity({ ...draftIdentity, authMethod: 'key', keyId: undefined })}
                        >
                            <Key size={14} /> {t('terminal.auth.sshKey')}
                        </Button>
                        <Button
                            variant="ghost"
                            className="w-full justify-start gap-2"
                            onClick={() => setDraftIdentity({ ...draftIdentity, authMethod: 'certificate', keyId: undefined })}
                        >
                            <BadgeCheck size={14} /> {t('terminal.auth.certificate')}
                        </Button>
                    </DropdownContent>
                </Dropdown>
            </div>

            {(draftIdentity.authMethod === 'key' || draftIdentity.authMethod === 'certificate') && (
                <div className="space-y-2">
                    <Label>
                        {t('keychain.identity.selectCredential', {
                            kind: draftIdentity.authMethod === 'key' ? t('terminal.auth.sshKey') : t('terminal.auth.certificate'),
                        })}
                    </Label>
                    <select
                        value={draftIdentity.keyId || ''}
                        onChange={e => setDraftIdentity({ ...draftIdentity, keyId: e.target.value || undefined })}
                        className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                    >
                        <option value="">{t('common.selectPlaceholder')}</option>
                        {keys
                            .filter(k => draftIdentity.authMethod === 'certificate' ? k.certificate : !k.certificate)
                            .map(k => (
                                <option key={k.id} value={k.id}>{k.label}</option>
                            ))
                        }
                    </select>
                </div>
            )}

            <Button
                className="w-full h-11"
                onClick={onSave}
                disabled={!draftIdentity.label?.trim() || !draftIdentity.username?.trim()}
            >
                {isNew ? t('keychain.identity.save') : t('keychain.identity.update')}
            </Button>
        </>
    );
};
