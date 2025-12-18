import React, { useCallback } from "react";
import type { Host, Identity, Snippet, SSHKey } from "../../../domain/models";
import type { SyncPayload } from "../../../domain/sync";
import { CloudSyncSettings } from "../../CloudSyncSettings";
import { SettingsTabContent } from "../settings-ui";

export default function SettingsSyncTab(props: {
  hosts: Host[];
  keys: SSHKey[];
  identities: Identity[];
  snippets: Snippet[];
  importDataFromString: (data: string) => void;
}) {
  const { hosts, keys, identities, snippets, importDataFromString } = props;

  const buildSyncPayload = useCallback((): SyncPayload => {
    return {
      hosts,
      keys,
      identities,
      snippets,
      customGroups: [],
      syncedAt: Date.now(),
    };
  }, [hosts, keys, identities, snippets]);

  const applySyncPayload = useCallback(
    (payload: SyncPayload) => {
      importDataFromString(
        JSON.stringify({
          hosts: payload.hosts,
          keys: payload.keys,
          identities: payload.identities,
          snippets: payload.snippets,
          customGroups: payload.customGroups,
        }),
      );
    },
    [importDataFromString],
  );

  return (
    <SettingsTabContent value="sync">
      <CloudSyncSettings onBuildPayload={buildSyncPayload} onApplyPayload={applySyncPayload} />
    </SettingsTabContent>
  );
}

