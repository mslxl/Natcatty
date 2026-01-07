/**
 * SettingsFileAssociationsTab - Manage SFTP file opener associations
 */
import { FileType, Trash2 } from "lucide-react";
import React, { useCallback } from "react";
import { useI18n } from "../../../application/i18n/I18nProvider";
import { useSftpFileAssociations } from "../../../application/state/useSftpFileAssociations";
import type { FileOpenerType } from "../../../lib/sftpFileUtils";
import { Button } from "../../ui/button";
import { SectionHeader, SettingsTabContent } from "../settings-ui";

const getOpenerLabel = (openerType: FileOpenerType, t: (key: string) => string): string => {
  if (openerType === 'builtin-editor') {
    return t('sftp.opener.builtInEditor');
  } else if (openerType === 'builtin-image-viewer') {
    return t('sftp.opener.builtInImageViewer');
  }
  return openerType;
};

export default function SettingsFileAssociationsTab() {
  const { t } = useI18n();
  const { getAllAssociations, removeAssociation } = useSftpFileAssociations();
  const associations = getAllAssociations();

  const handleRemove = useCallback((extension: string) => {
    if (confirm(t('settings.sftpFileAssociations.removeConfirm', { ext: extension }))) {
      removeAssociation(extension);
    }
  }, [removeAssociation, t]);

  return (
    <SettingsTabContent tabValue="file-associations" className="space-y-6">
      <div>
        <SectionHeader title={t('settings.sftpFileAssociations.title')} />
        <p className="text-sm text-muted-foreground mb-4">
          {t('settings.sftpFileAssociations.desc')}
        </p>

        {associations.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
            <FileType size={48} strokeWidth={1} className="mb-4 opacity-50" />
            <p className="text-sm">{t('settings.sftpFileAssociations.noAssociations')}</p>
          </div>
        ) : (
          <div className="border border-border rounded-md overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/50 border-b border-border">
                  <th className="text-left px-4 py-2 font-medium">
                    {t('settings.sftpFileAssociations.extension')}
                  </th>
                  <th className="text-left px-4 py-2 font-medium">
                    {t('settings.sftpFileAssociations.application')}
                  </th>
                  <th className="text-right px-4 py-2 font-medium w-24">
                    {/* Actions */}
                  </th>
                </tr>
              </thead>
              <tbody>
                {associations.map(({ extension, openerType }) => (
                  <tr key={extension} className="border-b border-border last:border-b-0 hover:bg-muted/30">
                    <td className="px-4 py-3">
                      <code className="text-xs bg-muted px-1.5 py-0.5 rounded">.{extension}</code>
                    </td>
                    <td className="px-4 py-3 text-muted-foreground">
                      {getOpenerLabel(openerType, t)}
                    </td>
                    <td className="px-4 py-3 text-right">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-destructive hover:text-destructive hover:bg-destructive/10"
                        onClick={() => handleRemove(extension)}
                        title={t('settings.sftpFileAssociations.remove')}
                      >
                        <Trash2 size={14} />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </SettingsTabContent>
  );
}
