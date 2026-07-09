import { useEffect, useMemo, useRef, useState } from 'react';

import { AI_STATE_CHANGED_EVENT } from '../../application/state/aiStateEvents';
import { readExternalMcpStoredEnabled } from '../../application/state/useExternalMcpToggleState';
import { STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED } from '../../infrastructure/config/storageKeys';
import { netcattyBridge } from '../../infrastructure/services/netcattyBridge';
import { detectLocalOs } from '../../lib/localShell';
import type { Host, PortForwardingRule, TerminalSession } from '../../types';
import { buildAITerminalSessionInfo } from './TerminalLayerSupport';

const EXTERNAL_MCP_CHAT_SESSION_ID = '__external_mcp__';

type UseExternalMcpSessionSyncOptions = {
  sessions: TerminalSession[];
  sessionHostsMap: Map<string, Host>;
  hosts: Host[];
  portForwardingRules: PortForwardingRule[];
};

/**
 * Keep the reserved External MCP scope aligned with every live terminal
 * session, independent of whether the Catty AI side panel is open.
 */
export function useExternalMcpSessionSync({
  sessions,
  sessionHostsMap,
  hosts,
  portForwardingRules,
}: UseExternalMcpSessionSyncOptions) {
  const [enabledTick, setEnabledTick] = useState(0);
  const enabled = useMemo(() => {
    void enabledTick;
    return readExternalMcpStoredEnabled();
  }, [enabledTick]);

  useEffect(() => {
    const onChanged = (event: Event) => {
      const detail = (event as CustomEvent<{ key?: string }>).detail;
      if (detail?.key && detail.key !== STORAGE_KEY_AI_EXTERNAL_MCP_ENABLED) return;
      setEnabledTick((value) => value + 1);
    };
    window.addEventListener(AI_STATE_CHANGED_EVENT, onChanged as EventListener);
    return () => {
      window.removeEventListener(AI_STATE_CHANGED_EVENT, onChanged as EventListener);
    };
  }, []);

  const payload = useMemo(() => {
    const localOs = detectLocalOs(navigator.userAgent || navigator.platform);
    return sessions.map((session) =>
      buildAITerminalSessionInfo(session, sessionHostsMap.get(session.id), localOs, {
        allHosts: hosts,
        portForwardingRules,
      }),
    );
  }, [sessions, sessionHostsMap, hosts, portForwardingRules]);

  const lastSerializedRef = useRef('');

  useEffect(() => {
    if (!enabled) return;
    const bridge = netcattyBridge.get();
    if (!bridge?.aiMcpUpdateSessions) return;

    const serialized = JSON.stringify(payload);
    if (serialized === lastSerializedRef.current) return;
    lastSerializedRef.current = serialized;

    const timeoutId = window.setTimeout(() => {
      void bridge.aiMcpUpdateSessions?.(payload, EXTERNAL_MCP_CHAT_SESSION_ID);
    }, 250);

    return () => {
      window.clearTimeout(timeoutId);
    };
  }, [enabled, payload]);
}
