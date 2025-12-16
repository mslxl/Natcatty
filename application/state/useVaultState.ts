import { useCallback, useEffect, useState } from "react";
import { normalizeDistroId, sanitizeHost } from "../../domain/host";
import {
  ConnectionLog,
  Host,
  KeyCategory,
  KnownHost,
  ShellHistoryEntry,
  Snippet,
  SSHKey,
} from "../../domain/models";
import {
  INITIAL_HOSTS,
  INITIAL_SNIPPETS,
} from "../../infrastructure/config/defaultData";
import {
  STORAGE_KEY_CONNECTION_LOGS,
  STORAGE_KEY_GROUPS,
  STORAGE_KEY_HOSTS,
  STORAGE_KEY_KEYS,
  STORAGE_KEY_KNOWN_HOSTS,
  STORAGE_KEY_LEGACY_KEYS,
  STORAGE_KEY_SHELL_HISTORY,
  STORAGE_KEY_SNIPPET_PACKAGES,
  STORAGE_KEY_SNIPPETS,
} from "../../infrastructure/config/storageKeys";
import { localStorageAdapter } from "../../infrastructure/persistence/localStorageAdapter";

type ExportableVaultData = {
  hosts: Host[];
  keys: SSHKey[];
  snippets: Snippet[];
  customGroups: string[];
  knownHosts?: KnownHost[];
};

type LegacyKeyRecord = Record<string, unknown> & { id?: string; source?: string };

// Migration helper for old SSHKey format to new format
const migrateKey = (key: Partial<SSHKey>): SSHKey => {
  const id = key.id ?? crypto.randomUUID();
  const label = key.label ?? `Key ${id.slice(0, 8)}`;

  const source =
    key.source === "generated" || key.source === "imported"
      ? key.source
      : key.privateKey
        ? "imported"
        : "generated";

  return {
    id,
    label,
    type: key.type || "ED25519",
    privateKey: key.privateKey || "",
    publicKey: key.publicKey,
    certificate: key.certificate,
    passphrase: key.passphrase,
    savePassphrase: key.savePassphrase,
    source,
    category:
      key.category ||
      ((key.certificate ? "certificate" : "key") as KeyCategory),
    created: key.created || Date.now(),
  };
};

const isLegacyUnsupportedKey = (key: LegacyKeyRecord): boolean => {
  const source = key.source;
  if (source === "biometric" || source === "fido2" || source === "passkey") return true;
  // Legacy experimental WebAuthn fields
  if ("credentialId" in key || "rpId" in key || "userVerification" in key) return true;
  return false;
};

export const useVaultState = () => {
  const [hosts, setHosts] = useState<Host[]>([]);
  const [keys, setKeys] = useState<SSHKey[]>([]);
  const [snippets, setSnippets] = useState<Snippet[]>([]);
  const [customGroups, setCustomGroups] = useState<string[]>([]);
  const [snippetPackages, setSnippetPackages] = useState<string[]>([]);
  const [knownHosts, setKnownHosts] = useState<KnownHost[]>([]);
  const [shellHistory, setShellHistory] = useState<ShellHistoryEntry[]>([]);
  const [connectionLogs, setConnectionLogs] = useState<ConnectionLog[]>([]);

  const updateHosts = useCallback((data: Host[]) => {
    const cleaned = data.map(sanitizeHost);
    setHosts(cleaned);
    localStorageAdapter.write(STORAGE_KEY_HOSTS, cleaned);
  }, []);

  const updateKeys = useCallback((data: SSHKey[]) => {
    setKeys(data);
    localStorageAdapter.write(STORAGE_KEY_KEYS, data);
  }, []);

  const updateSnippets = useCallback((data: Snippet[]) => {
    setSnippets(data);
    localStorageAdapter.write(STORAGE_KEY_SNIPPETS, data);
  }, []);

  const updateSnippetPackages = useCallback((data: string[]) => {
    setSnippetPackages(data);
    localStorageAdapter.write(STORAGE_KEY_SNIPPET_PACKAGES, data);
  }, []);

  const updateCustomGroups = useCallback((data: string[]) => {
    setCustomGroups(data);
    localStorageAdapter.write(STORAGE_KEY_GROUPS, data);
  }, []);

  const updateKnownHosts = useCallback((data: KnownHost[]) => {
    setKnownHosts(data);
    localStorageAdapter.write(STORAGE_KEY_KNOWN_HOSTS, data);
  }, []);

  const addShellHistoryEntry = useCallback(
    (entry: Omit<ShellHistoryEntry, "id" | "timestamp">) => {
      const newEntry: ShellHistoryEntry = {
        ...entry,
        id: crypto.randomUUID(),
        timestamp: Date.now(),
      };
      setShellHistory((prev) => {
        // Keep only the last 1000 entries
        const updated = [newEntry, ...prev].slice(0, 1000);
        localStorageAdapter.write(STORAGE_KEY_SHELL_HISTORY, updated);
        return updated;
      });
    },
    [],
  );

  const clearShellHistory = useCallback(() => {
    setShellHistory([]);
    localStorageAdapter.write(STORAGE_KEY_SHELL_HISTORY, []);
  }, []);

  // Connection logs management
  const addConnectionLog = useCallback(
    (log: Omit<ConnectionLog, "id">) => {
      const newLog: ConnectionLog = {
        ...log,
        id: crypto.randomUUID(),
      };
      setConnectionLogs((prev) => {
        // Keep only the last 500 non-saved entries plus all saved entries
        const savedLogs = prev.filter((l) => l.saved);
        const unsavedLogs = prev.filter((l) => !l.saved);
        const updated = [newLog, ...unsavedLogs].slice(0, 500);
        const final = [...updated, ...savedLogs].sort(
          (a, b) => b.startTime - a.startTime
        );
        localStorageAdapter.write(STORAGE_KEY_CONNECTION_LOGS, final);
        return final;
      });
      return newLog.id;
    },
    []
  );

  const updateConnectionLog = useCallback(
    (id: string, updates: Partial<ConnectionLog>) => {
      setConnectionLogs((prev) => {
        const updated = prev.map((log) =>
          log.id === id ? { ...log, ...updates } : log
        );
        localStorageAdapter.write(STORAGE_KEY_CONNECTION_LOGS, updated);
        return updated;
      });
    },
    []
  );

  const toggleConnectionLogSaved = useCallback((id: string) => {
    setConnectionLogs((prev) => {
      const updated = prev.map((log) =>
        log.id === id ? { ...log, saved: !log.saved } : log
      );
      localStorageAdapter.write(STORAGE_KEY_CONNECTION_LOGS, updated);
      return updated;
    });
  }, []);

  const deleteConnectionLog = useCallback((id: string) => {
    setConnectionLogs((prev) => {
      const updated = prev.filter((log) => log.id !== id);
      localStorageAdapter.write(STORAGE_KEY_CONNECTION_LOGS, updated);
      return updated;
    });
  }, []);

  const clearUnsavedConnectionLogs = useCallback(() => {
    setConnectionLogs((prev) => {
      const saved = prev.filter((log) => log.saved);
      localStorageAdapter.write(STORAGE_KEY_CONNECTION_LOGS, saved);
      return saved;
    });
  }, []);

  // Convert a known host to a managed host
  const convertKnownHostToHost = useCallback((knownHost: KnownHost): Host => {
    const newHost: Host = {
      id: `host-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`,
      label: knownHost.hostname,
      hostname: knownHost.hostname,
      port: knownHost.port,
      username: "", // Will be set when connecting
      os: "linux",
      group: "",
      tags: [],
      protocol: "ssh",
    };

    // Update the known host to mark it as converted using functional update
    setKnownHosts((prevKnownHosts) => {
      const updated = prevKnownHosts.map((kh) =>
        kh.id === knownHost.id ? { ...kh, convertedToHostId: newHost.id } : kh,
      );
      localStorageAdapter.write(STORAGE_KEY_KNOWN_HOSTS, updated);
      return updated;
    });

    // Add to hosts using functional update
    setHosts((prevHosts) => {
      const updated = [...prevHosts, sanitizeHost(newHost)];
      localStorageAdapter.write(STORAGE_KEY_HOSTS, updated);
      return updated;
    });

    return newHost;
  }, []);

  useEffect(() => {
    const savedHosts = localStorageAdapter.read<Host[]>(STORAGE_KEY_HOSTS);
    const savedKeysRaw = localStorageAdapter.read<unknown[]>(STORAGE_KEY_KEYS);
    const savedGroups = localStorageAdapter.read<string[]>(STORAGE_KEY_GROUPS);
    const savedSnippets =
      localStorageAdapter.read<Snippet[]>(STORAGE_KEY_SNIPPETS);
    const savedSnippetPackages = localStorageAdapter.read<string[]>(
      STORAGE_KEY_SNIPPET_PACKAGES,
    );

    if (savedHosts?.length) {
      const sanitized = savedHosts.map(sanitizeHost);
      setHosts(sanitized);
      localStorageAdapter.write(STORAGE_KEY_HOSTS, sanitized);
    } else {
      updateHosts(INITIAL_HOSTS);
    }

    // Migrate old keys to new format with source/category fields
    if (savedKeysRaw?.length) {
      const migratedKeys: SSHKey[] = [];
      const legacyKeys: LegacyKeyRecord[] = [];

      for (const entry of savedKeysRaw) {
        const record =
          entry && typeof entry === "object" ? (entry as LegacyKeyRecord) : null;
        if (!record) continue;

        if (isLegacyUnsupportedKey(record)) {
          legacyKeys.push(record);
          continue;
        }

        migratedKeys.push(migrateKey(record as Partial<SSHKey>));
      }

      setKeys(migratedKeys);
      // Persist migrated keys
      localStorageAdapter.write(STORAGE_KEY_KEYS, migratedKeys);
      if (legacyKeys.length) {
        localStorageAdapter.write(STORAGE_KEY_LEGACY_KEYS, legacyKeys);
      }
    }

    if (savedSnippets) setSnippets(savedSnippets);
    else updateSnippets(INITIAL_SNIPPETS);

    if (savedGroups) setCustomGroups(savedGroups);
    if (savedSnippetPackages) setSnippetPackages(savedSnippetPackages);

    // Load known hosts
    const savedKnownHosts = localStorageAdapter.read<KnownHost[]>(
      STORAGE_KEY_KNOWN_HOSTS,
    );
    if (savedKnownHosts) setKnownHosts(savedKnownHosts);

    // Load shell history
    const savedShellHistory = localStorageAdapter.read<ShellHistoryEntry[]>(
      STORAGE_KEY_SHELL_HISTORY,
    );
    if (savedShellHistory) setShellHistory(savedShellHistory);

    // Load connection logs
    const savedConnectionLogs = localStorageAdapter.read<ConnectionLog[]>(
      STORAGE_KEY_CONNECTION_LOGS,
    );
    if (savedConnectionLogs) setConnectionLogs(savedConnectionLogs);
  }, [updateHosts, updateSnippets]);

  const updateHostDistro = useCallback((hostId: string, distro: string) => {
    const normalized = normalizeDistroId(distro);
    setHosts((prev) => {
      const next = prev.map((h) =>
        h.id === hostId ? { ...h, distro: normalized } : h,
      );
      localStorageAdapter.write(STORAGE_KEY_HOSTS, next);
      return next;
    });
  }, []);

  const exportData = useCallback(
    (): ExportableVaultData => ({
      hosts,
      keys,
      snippets,
      customGroups,
      knownHosts,
    }),
    [hosts, keys, snippets, customGroups, knownHosts],
  );

  const importData = useCallback(
    (payload: Partial<ExportableVaultData>) => {
      if (payload.hosts) updateHosts(payload.hosts);
      if (payload.keys) updateKeys(payload.keys);
      if (payload.snippets) updateSnippets(payload.snippets);
      if (payload.customGroups) updateCustomGroups(payload.customGroups);
      if (payload.knownHosts) updateKnownHosts(payload.knownHosts);
    },
    [
      updateHosts,
      updateKeys,
      updateSnippets,
      updateCustomGroups,
      updateKnownHosts,
    ],
  );

  const importDataFromString = useCallback(
    (jsonString: string) => {
      const data = JSON.parse(jsonString);
      importData(data);
    },
    [importData],
  );

  return {
    hosts,
    keys,
    snippets,
    customGroups,
    snippetPackages,
    knownHosts,
    shellHistory,
    connectionLogs,
    updateHosts,
    updateKeys,
    updateSnippets,
    updateSnippetPackages,
    updateCustomGroups,
    updateKnownHosts,
    addShellHistoryEntry,
    clearShellHistory,
    addConnectionLog,
    updateConnectionLog,
    toggleConnectionLogSaved,
    deleteConnectionLog,
    clearUnsavedConnectionLogs,
    updateHostDistro,
    convertKnownHostToHost,
    exportData,
    importDataFromString,
  };
};
