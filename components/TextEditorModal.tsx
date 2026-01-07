/**
 * TextEditorModal - Modal for editing text files in SFTP
 */
import { 
  CloudUpload,
  Loader2,
} from 'lucide-react';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useI18n } from '../application/i18n/I18nProvider';
import { getLanguageId, getLanguageName, getSupportedLanguages } from '../lib/sftpFileUtils';
import { Button } from './ui/button';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from './ui/dialog';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from './ui/select';
import { toast } from './ui/toast';

interface TextEditorModalProps {
  open: boolean;
  onClose: () => void;
  fileName: string;
  initialContent: string;
  onSave: (content: string) => Promise<void>;
}

export const TextEditorModal: React.FC<TextEditorModalProps> = ({
  open,
  onClose,
  fileName,
  initialContent,
  onSave,
}) => {
  const { t } = useI18n();
  const [content, setContent] = useState(initialContent);
  const [saving, setSaving] = useState(false);
  const [hasChanges, setHasChanges] = useState(false);
  const [languageId, setLanguageId] = useState(() => getLanguageId(fileName));

  // Reset content when file changes
  useEffect(() => {
    setContent(initialContent);
    setHasChanges(false);
    setLanguageId(getLanguageId(fileName));
  }, [initialContent, fileName]);

  // Track changes
  useEffect(() => {
    setHasChanges(content !== initialContent);
  }, [content, initialContent]);

  const handleSave = useCallback(async () => {
    if (saving) return;
    setSaving(true);
    try {
      await onSave(content);
      setHasChanges(false);
      toast.success(t('sftp.editor.saved'), 'SFTP');
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t('sftp.editor.saveFailed'),
        'SFTP'
      );
    } finally {
      setSaving(false);
    }
  }, [content, onSave, saving, t]);

  const handleClose = useCallback(() => {
    if (hasChanges) {
      const confirmed = confirm(t('sftp.editor.unsavedChanges'));
      if (!confirmed) return;
    }
    onClose();
  }, [hasChanges, onClose, t]);

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    // Save on Ctrl/Cmd + S
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      handleSave();
    }
  }, [handleSave]);

  const supportedLanguages = useMemo(() => getSupportedLanguages(), []);

  return (
    <Dialog open={open} onOpenChange={(isOpen) => !isOpen && handleClose()}>
      <DialogContent className="max-w-5xl h-[85vh] flex flex-col p-0 gap-0">
        {/* Header */}
        <DialogHeader className="px-4 py-3 border-b border-border/60 flex-shrink-0">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <DialogTitle className="text-sm font-semibold truncate max-w-[400px]">
                {fileName}
                {hasChanges && <span className="text-primary ml-1">*</span>}
              </DialogTitle>
            </div>
            <div className="flex items-center gap-2">
              {/* Language selector */}
              <Select value={languageId} onValueChange={setLanguageId}>
                <SelectTrigger className="h-7 w-[140px] text-xs">
                  <SelectValue placeholder={t('sftp.editor.syntaxHighlight')} />
                </SelectTrigger>
                <SelectContent>
                  {supportedLanguages.map((lang) => (
                    <SelectItem key={lang.id} value={lang.id} className="text-xs">
                      {lang.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              
              {/* Save button */}
              <Button
                variant="default"
                size="sm"
                className="h-7"
                onClick={handleSave}
                disabled={saving || !hasChanges}
              >
                {saving ? (
                  <Loader2 size={14} className="mr-1.5 animate-spin" />
                ) : (
                  <CloudUpload size={14} className="mr-1.5" />
                )}
                {saving ? t('sftp.editor.saving') : t('sftp.editor.save')}
              </Button>
            </div>
          </div>
        </DialogHeader>

        {/* Editor */}
        <div className="flex-1 min-h-0 relative">
          <textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            onKeyDown={handleKeyDown}
            className="w-full h-full resize-none bg-background text-foreground p-4 font-mono text-sm leading-relaxed focus:outline-none overflow-auto"
            style={{ tabSize: 2 }}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            placeholder=""
          />
        </div>

        {/* Footer */}
        <div className="px-4 py-2 border-t border-border/60 flex items-center justify-between text-xs text-muted-foreground bg-muted/30 flex-shrink-0">
          <span>
            {getLanguageName(languageId)}
          </span>
          <span>
            {content.split('\n').length} lines • {content.length} characters
          </span>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export default TextEditorModal;
