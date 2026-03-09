import React, { useCallback } from "react";
import type { PortForwardingRule } from "../../../domain/models";
import type { SyncPayload } from "../../../domain/sync";
import { buildSyncPayload, applySyncPayload } from "../../../domain/syncPayload";
import type { SyncableVaultData } from "../../../domain/syncPayload";
import { CloudSyncSettings } from "../../CloudSyncSettings";
import { SettingsTabContent } from "../settings-ui";

export default function SettingsSyncTab(props: {
  vault: SyncableVaultData;
  portForwardingRules: PortForwardingRule[];
  importDataFromString: (data: string) => void;
  importPortForwardingRules: (rules: PortForwardingRule[]) => void;
  clearVaultData: () => void;
}) {
  const {
    vault,
    portForwardingRules,
    importDataFromString,
    importPortForwardingRules,
    clearVaultData,
  } = props;

  const onBuildPayload = useCallback((): SyncPayload => {
    return buildSyncPayload(vault, portForwardingRules);
  }, [vault, portForwardingRules]);

  const onApplyPayload = useCallback(
    (payload: SyncPayload) => {
      applySyncPayload(payload, {
        importVaultData: importDataFromString,
        importPortForwardingRules,
      });
    },
    [importDataFromString, importPortForwardingRules],
  );

  return (
    <SettingsTabContent value="sync">
      <CloudSyncSettings
        onBuildPayload={onBuildPayload}
        onApplyPayload={onApplyPayload}
        onClearLocalData={clearVaultData}
      />
    </SettingsTabContent>
  );
}
