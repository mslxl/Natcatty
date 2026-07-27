import { useCallback } from "react";
import { sanitizeHost } from "../../domain/host";
import {
  buildPluginImporterSafePreview,
  mergePluginImporterDrafts,
  normalizePluginImporterRecords,
} from "../../domain/pluginImporter";
import type { Host, Identity, Snippet, SSHKey } from "../../types";

type Translation = (key: string, params?: Record<string, unknown>) => string;

type PluginImporterCommitData = {
  keys: SSHKey[];
  identities: Identity[];
  hosts: Host[];
  snippets: Snippet[];
  customGroups: string[];
};

type PluginImporterCommitOptions = {
  hosts: ReadonlyArray<Host>;
  identities: ReadonlyArray<Identity>;
  keys: ReadonlyArray<SSHKey>;
  snippets: ReadonlyArray<Snippet>;
  customGroups: ReadonlyArray<string>;
  onCommitPluginImporterData: (data: PluginImporterCommitData) => Promise<void> | void;
  onCommitSuccess?: (addedCount: number) => void;
  t: Translation;
};

export function usePluginImporterCommit({
  hosts,
  identities,
  keys,
  snippets,
  customGroups,
  onCommitPluginImporterData,
  onCommitSuccess,
  t,
}: PluginImporterCommitOptions) {
  const buildPluginImportMerge = useCallback((preview: NetcattyPluginImporterPreview) => {
    const drafts = normalizePluginImporterRecords(preview.records);
    return {
      drafts,
      merged: mergePluginImporterDrafts({
        hosts: [...hosts],
        identities: [...identities],
        keys: [...keys],
        snippets: [...snippets],
        customGroups: [...customGroups],
      }, drafts),
    };
  }, [customGroups, hosts, identities, keys, snippets]);

  const handlePluginPreviewCommit = useCallback(async (preview: NetcattyPluginImporterPreview) => {
    const { drafts, merged } = buildPluginImportMerge(preview);
    if (preview.result.errors > 0 || drafts.errors.length > 0) {
      throw new Error(drafts.errors[0] || t("vault.import.plugins.containsErrors"));
    }
    await onCommitPluginImporterData({
      keys: merged.keys,
      identities: merged.identities,
      hosts: merged.hosts.map(sanitizeHost),
      snippets: merged.snippets,
      customGroups: merged.customGroups,
    });
    onCommitSuccess?.(merged.addedCount);
  }, [buildPluginImportMerge, onCommitPluginImporterData, onCommitSuccess, t]);

  const getPluginPreviewAnalysis = useCallback((preview: NetcattyPluginImporterPreview) => {
    const { drafts, merged } = buildPluginImportMerge(preview);
    return {
      duplicateCount: merged.duplicateCount,
      validationErrorCount: drafts.errors.length,
      safePreview: buildPluginImporterSafePreview(drafts),
    };
  }, [buildPluginImportMerge]);

  return {
    handlePluginPreviewCommit,
    getPluginPreviewAnalysis,
  };
}
