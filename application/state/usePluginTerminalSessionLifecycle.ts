import { useCallback, useEffect, useMemo, useRef } from 'react';

import { getWindowPluginTerminalProviderRegistry } from './pluginTerminalProviderRegistry';

interface PluginTerminalSessionLifecycleOptions {
  sessionId: string;
  hostId?: string;
  workspaceId?: string;
  protocol?: string;
  status: 'connecting' | 'connected' | 'disconnected';
  shellType?: string;
  initialCwd?: string;
}

interface SnapshotState {
  cwd?: string;
  title?: string;
  cols?: number;
  rows?: number;
  alternateScreen?: boolean;
}

function normalizeProtocol(protocol: string | undefined): NetcattyTerminalSessionSnapshot['protocol'] {
  if (protocol === 'telnet' || protocol === 'local' || protocol === 'serial') return protocol;
  return 'ssh';
}

function normalizeShellType(shellType: string | undefined): NetcattyTerminalSessionSnapshot['shellType'] | undefined {
  if (shellType === 'posix' || shellType === 'fish' || shellType === 'powershell' || shellType === 'cmd') {
    return shellType;
  }
  return shellType ? 'unknown' : undefined;
}

export function usePluginTerminalSessionLifecycle(options: PluginTerminalSessionLifecycleOptions) {
  const registry = getWindowPluginTerminalProviderRegistry();
  const metadataRef = useRef(options);
  metadataRef.current = options;
  const snapshotStateRef = useRef<SnapshotState>({ cwd: options.initialCwd });
  const everConnectedRef = useRef(false);

  const snapshot = useCallback((): NetcattyTerminalSessionSnapshot => {
    const metadata = metadataRef.current;
    const state = snapshotStateRef.current;
    const shellType = normalizeShellType(metadata.shellType);
    return {
      sessionId: metadata.sessionId,
      ...(metadata.hostId ? { hostId: metadata.hostId } : {}),
      ...(metadata.workspaceId ? { workspaceId: metadata.workspaceId } : {}),
      protocol: normalizeProtocol(metadata.protocol),
      status: metadata.status,
      ...(state.cwd ? { cwd: state.cwd } : {}),
      ...(state.title ? { title: state.title } : {}),
      ...(shellType ? { shellType } : {}),
      ...(state.cols == null ? {} : { cols: state.cols }),
      ...(state.rows == null ? {} : { rows: state.rows }),
      ...(state.alternateScreen == null ? {} : { alternateScreen: state.alternateScreen }),
    };
  }, []);

  const publish = useCallback((type: NetcattyTerminalSessionEvent['type'], details: { exitCode?: number } = {}) => {
    if (!registry) return;
    void registry.publishSessionEvent({ type, session: snapshot(), ...details }).catch(() => {});
  }, [registry, snapshot]);

  useEffect(() => {
    publish('created');
    return () => {
      publish('disposed');
      registry?.cancelSession(metadataRef.current.sessionId);
    };
  }, [publish, registry]);

  useEffect(() => {
    if (options.status === 'connected') {
      publish(everConnectedRef.current ? 'reconnected' : 'connected');
      everConnectedRef.current = true;
    } else if (options.status === 'disconnected') {
      publish('disconnected');
    }
  }, [options.status, publish]);

  const onCwdChanged = useCallback((cwd: string | null) => {
    snapshotStateRef.current.cwd = cwd || undefined;
    publish('cwdChanged');
  }, [publish]);

  const onTitleChanged = useCallback((title: string | null) => {
    snapshotStateRef.current.title = title || undefined;
    publish('titleChanged');
  }, [publish]);

  const onResized = useCallback((cols: number, rows: number) => {
    snapshotStateRef.current.cols = cols;
    snapshotStateRef.current.rows = rows;
    publish('resized');
  }, [publish]);

  const onAlternateScreenChanged = useCallback((alternateScreen: boolean) => {
    if (snapshotStateRef.current.alternateScreen === alternateScreen) return;
    snapshotStateRef.current.alternateScreen = alternateScreen;
    publish('alternateScreenChanged');
  }, [publish]);

  const onCommandSubmitted = useCallback(() => {
    publish('commandSubmitted');
  }, [publish]);

  return useMemo(() => ({
    onAlternateScreenChanged,
    onCommandSubmitted,
    onCwdChanged,
    onResized,
    onTitleChanged,
  }), [onAlternateScreenChanged, onCommandSubmitted, onCwdChanged, onResized, onTitleChanged]);
}
